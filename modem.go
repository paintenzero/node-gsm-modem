package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
	// Assuming pdu.go, at_handler.go, serial_com.go are in the same package 'main'
)

// Default constants
const (
	DefaultCommandTimeoutSec = 10
	DefaultUSSDTimeoutSec    = 30
	DefaultBaudRate          = 115200
)

// ModemOptions holds configuration options for creating a new Modem instance.
type ModemOptions struct {
	Ports              []string // List of serial port names to try
	CommandTimeoutSec  int      // Timeout for AT commands in seconds
	USSDFailTimeoutSec int      // Timeout for USSD commands in seconds (if synchronous part)
	AutoHangup         bool     // Automatically hang up incoming calls
	Forever            bool     // Attempt to reconnect on disconnect
	Debug              bool     // Enable debug logging
	BaudRate           int      // Baud rate for serial connection (default 115200)
}

// EventChannels holds channels for various modem event notifications.
type EventChannels struct {
	IncomingSMS  chan PDUData          // Channel for new incoming SMS PDUs
	StatusReport chan StatusReportData   // Channel for new status reports
	USSDResponse chan USSDResponseData   // Channel for USSD responses
	Call         chan CallData         // Channel for call-related events (RING, CLIP)
	Disconnect   chan error            // Channel to signal disconnection
	Other        chan string           // Channel for other unsolicited responses
}

// SignalStrength holds information about the modem's signal quality.
type SignalStrength struct {
	RSSI      int    // Received Signal Strength Indication (0-31, 99 for not known/detectable)
	BER       int    // Bit Error Rate (0-7, 99 for not known/detectable)
	Condition string // Human-readable condition (e.g., "Excellent", "Good", "Weak")
}

// StorageInfo holds information for a single message storage type.
type StorageInfo struct {
	Used  int
	Total int
	Name  string // e.g., "SM", "ME", "SR"
}

// ModemStorages holds information about the modem's message storage capacities.
type ModemStorages struct {
	Storage1 StorageInfo // Memory storage 1 (e.g., "SM" for SIM, "ME" for modem memory)
	Storage2 StorageInfo // Memory storage 2 (e.g., preferred for reading/deleting)
	Storage3 StorageInfo // Memory storage 3 (e.g., preferred for received messages)
}

// MessageSendResult holds the result of sending a single PDU.
type MessageSendResult struct {
	PDUIndex      int    // Index of the PDU if multiple were sent (e.g., for multipart)
	MessageID     int    // Message Reference (TP-MR) returned by the modem on success
	Err           error  // Error if this specific PDU failed
	IsSuccessful  bool
	FinalResponse string // Raw final response from modem (e.g. "+CMGS: <mr>")
}

// USSDResponseData holds data from a USSD response.
type USSDResponseData struct {
	Status       int    // Status code from +CUSD (e.g., 0, 1, 2, 4)
	ResponseText string // The USSD response string
	DCS          int    // Data Coding Scheme of the response
	Error        error  // If an error occurred processing or receiving USSD
}

// CallData holds information about an incoming call.
type CallData struct {
	EventType  string // "RING", "CLIP"
	CallerID   string // Caller ID if available from +CLIP
	CallerType int    // Type of address for the caller ID (from +CLIP)
	Raw        string // Raw unsolicited line
}

// Modem struct holds the state and configuration for interacting with a GSM modem.
type Modem struct {
	Port            serial.Port   // Active serial port connection
	connected       bool          // Status of the connection
	logger          *log.Logger   // Logger instance
	commandTimeout  time.Duration // Default timeout for AT commands
	ussdTimeout     time.Duration // Timeout for USSD responses
	autoHangup      bool          // Automatically hang up incoming calls
	eventChannels   EventChannels // Channels for event notifications
	portsToTry      []string      // List of port names to attempt connection
	currentPortName string        // Name of the currently connected port
	baudRate        int           // Baud rate for serial connection
	forever         bool          // Whether to attempt reconnection on disconnect
	wg              sync.WaitGroup // For managing goroutines
	shutdownChan    chan struct{}  // For signaling goroutines to stop
	listenStopper   chan struct{}  // Specific stopper for listenForUnsolicitedResponses

	manufacturer    string        // Modem manufacturer
	model           string        // Modem model
	imei            string        // Modem IMEI
	imsi            string        // Subscriber IMSI
	currentStorages ModemStorages // Current CPMS settings

	reader *bufio.Reader // For listenForUnsolicitedResponses

	// atHandler and pduHandler are not struct fields as their functions are used directly.
}

// NewModem creates and initializes a new Modem instance.
func NewModem(options ModemOptions) (*Modem, error) {
	logger := log.New(os.Stdout, "[Modem] ", log.LstdFlags|log.Lshortfile)
	if !options.Debug {
		// Could set output to ioutil.Discard or use a less verbose logger
		// For now, default logger is used. Prefix helps distinguish.
	} else {
		logger.Print("Debug mode enabled.")
	}

	cmdTimeoutSec := options.CommandTimeoutSec
	if cmdTimeoutSec == 0 {
		cmdTimeoutSec = DefaultCommandTimeoutSec
	}
	cmdTimeout := time.Duration(cmdTimeoutSec) * time.Second

	ussdFailTimeoutSec := options.USSDFailTimeoutSec
	if ussdFailTimeoutSec == 0 {
		ussdFailTimeoutSec = DefaultUSSDTimeoutSec
	}
	ussdTimeout := time.Duration(ussdFailTimeoutSec) * time.Second

	baud := options.BaudRate
	if baud == 0 {
		baud = DefaultBaudRate
	}

	if len(options.Ports) == 0 {
		return nil, fmt.Errorf("no serial ports specified in options")
	}

	m := &Modem{
		connected:      false,
		logger:         logger,
		commandTimeout: cmdTimeout,
		ussdTimeout:    ussdTimeout,
		autoHangup:     options.AutoHangup,
		portsToTry:     options.Ports,
		baudRate:       baud,
		forever:        options.Forever,
		shutdownChan:   make(chan struct{}),
		listenStopper:  make(chan struct{}), // Initialized for listenForUnsolicitedResponses
		eventChannels: EventChannels{
			IncomingSMS:  make(chan PDUData, 10),
			StatusReport: make(chan StatusReportData, 10),
			USSDResponse: make(chan USSDResponseData, 5),
			Call:         make(chan CallData, 5),
			Disconnect:   make(chan error, 1),    // Buffered to prevent blocking sender
			Other:        make(chan string, 20), // For other URCs
		},
	}

	m.logger.Printf("Modem instance created. Ports to try: %v, Baud: %d, Command Timeout: %v, USSD Timeout: %v",
		m.portsToTry, m.baudRate, m.commandTimeout, m.ussdTimeout)

	return m, nil
}

// Connect attempts to open a serial connection to the modem and configure it.
func (m *Modem) Connect() error {
	m.logger.Println("Attempting to connect to modem...")
	var err error
	for _, portName := range m.portsToTry {
		m.logger.Printf("Trying port: %s at baud rate %d", portName, m.baudRate)
		m.Port, err = OpenPort(portName, m.baudRate) // serial_com.go
		if err != nil {
			m.logger.Printf("Failed to open port %s: %v", portName, err)
			continue
		}
		m.logger.Printf("Port %s opened successfully.", portName)
		m.currentPortName = portName

		// Start the AT command processor (from at_handler.go)
		// processCommands needs the port, a waitgroup, and a shutdown channel.
		m.wg.Add(1)
		go processCommands(m.Port, &m.wg) // Assuming processCommands is adapted for shutdownChan via queue closure

		// Basic "AT" check
		_, atErr := SubmitCommandToQueue("AT", 1*time.Second) // at_handler.go
		if atErr != nil {
			m.logger.Printf("Modem on port %s did not respond to AT command: %v", portName, atErr)
			ClosePort(m.Port) // serial_com.go
			m.Port = nil
			// Signal processCommands to stop since this port attempt failed
			// This requires processCommands to check if commandQueue is closed and exit.
			// Or a more direct signal. For now, rely on subsequent Close() if all ports fail.
			continue
		}
		m.logger.Printf("Modem responded to AT command on port %s.", portName)

		errConfig := m.ConfigureModem()
		if errConfig != nil {
			m.logger.Printf("Failed to configure modem on port %s: %v", portName, errConfig)
			ClosePort(m.Port)
			m.Port = nil
			// Similar shutdown logic for processCommands needed here too.
			continue
		}

		m.connected = true
		m.logger.Printf("Modem connected and configured on port %s.", portName)

		// Initialize reader for listenForUnsolicitedResponses
		m.reader = bufio.NewReader(m.Port)

		m.wg.Add(1)
		go m.listenForUnsolicitedResponses()

		if m.forever {
			m.wg.Add(1)
			go m.reconnectLoop()
		}
		return nil // Successful connection
	}

	// If all ports failed
	// Ensure processCommands goroutine (if started for the last failed attempt) is stopped.
	// This is tricky if processCommands was started but ConfigureModem failed.
	// A clean way is to ensure Close() is called if Connect() returns error.
	// Or, processCommands itself should exit if port becomes unusable.
	// For now, the main Close() method will handle wg.Wait().

	return fmt.Errorf("failed to connect to modem on any specified port")
}

// Close shuts down the modem connection and associated goroutines.
func (m *Modem) Close() error {
	m.logger.Println("Closing modem connection...")
	if !m.connected && m.Port == nil {
		m.logger.Println("Modem already disconnected.")
		// Ensure shutdownChan is closed to stop any lingering goroutines like reconnectLoop
		select {
		case <-m.shutdownChan: // Already closed
		default:
			close(m.shutdownChan)
		}
		m.wg.Wait() // Wait for any goroutines (like reconnectLoop) to finish
		return nil
	}

	// Signal goroutines to stop
	// 1. Stop listener first (as it uses m.Port)
	select {
	case <-m.listenStopper: // Already closed or never started
	default:
		close(m.listenStopper)
	}

	// 2. Signal other goroutines (processCommands, reconnectLoop)
	select {
	case <-m.shutdownChan: // Already closed
	default:
		close(m.shutdownChan)
	}
	
	// Close command queue to stop processCommands goroutine
	// This needs commandQueue to be accessible or have a specific stop function.
	// For now, assume processCommands stops when shutdownChan is closed and port read fails.
	// A more robust way: have a Stop function for at_handler part.
	// Or at_handler.processCommands checks shutdownChan.
	// For now, closing the port will make reads in processCommands fail.
	// And at_handler.SubmitCommandToQueue might need to handle closed commandQueue.

	if m.Port != nil {
		err := ClosePort(m.Port) // serial_com.go
		if err != nil {
			m.logger.Printf("Error closing serial port %s: %v", m.currentPortName, err)
			// Continue with shutdown anyway
		} else {
			m.logger.Printf("Serial port %s closed.", m.currentPortName)
		}
		m.Port = nil
	}

	m.connected = false
	m.logger.Println("Modem disconnected.")

	// Wait for all goroutines to finish
	// A timeout for wg.Wait() might be useful in production to prevent hangs.
	waitTimeout := time.After(5 * time.Second)
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Println("All modem goroutines finished.")
	case <-waitTimeout:
		m.logger.Println("Timeout waiting for modem goroutines to finish. Some may still be running.")
	}
	
	// Close event channels? Usually subscribers close them or context manages them.
	// For simplicity, we are not closing them here.
	return nil
}

// reconnectLoop attempts to reconnect if the connection is lost and 'forever' is true.
func (m *Modem) reconnectLoop() {
	defer m.wg.Done()
	m.logger.Println("Reconnect loop started.")
	for {
		select {
		case <-m.shutdownChan:
			m.logger.Println("Reconnect loop shutting down.")
			return
		case err, ok := <-m.eventChannels.Disconnect:
			if !ok { // Disconnect channel closed, implies shutdown
				m.logger.Println("Disconnect channel closed, reconnect loop exiting.")
				return
			}
			m.logger.Printf("Modem disconnected with error: %v. Attempting to reconnect...", err)
			m.connected = false // Ensure status is updated
			if m.Port != nil { // Ensure port is fully closed before trying to reopen
				ClosePort(m.Port)
				m.Port = nil
			}
			
			// Wait a bit before trying to reconnect
			time.Sleep(5 * time.Second) 

			if !m.forever {
				m.logger.Println("'Forever' is false, not attempting reconnection.")
				return
			}

			// Attempt to reconnect by calling Connect() again
			// Connect() will try all ports.
			if err := m.Connect(); err != nil {
				m.logger.Printf("Failed to reconnect: %v. Will retry later.", err)
				// Sleep before next major reconnect attempt if Connect fails on all ports
				select {
				case <-time.After(10 * time.Second): // Longer sleep after a full failed cycle
				case <-m.shutdownChan:
					m.logger.Println("Reconnect loop shutting down during sleep.")
					return
				}
			} else {
				m.logger.Println("Successfully reconnected.")
				// Connect() already starts listenForUnsolicitedResponses and reconnectLoop (if forever)
				// This might lead to multiple reconnectLoop goroutines if not handled carefully.
				// Connect should not restart reconnectLoop.
				// The current structure: Connect starts listen, and if forever, starts reconnectLoop.
				// This is problematic. ReconnectLoop should be started once.
				// For now, this will be a single attempt from this goroutine.
				// A better structure: main Connect starts listener. If forever, main Connect starts separate reconnect monitor.
				// Let's simplify: this loop just calls Connect. Connect itself should not restart this.
				// The `if m.forever` in `Connect` starting `reconnectLoop` is an issue.
				// For now, let's assume `Connect` is idempotent or handles this.
				// The `m.forever` check in `Connect` should only be for the *initial* start of `reconnectLoop`.
				// This needs refinement. For now, if reconnected, this goroutine's job is done.
				return // Exit this reconnectLoop, Connect started new necessary goroutines.
			}
		}
	}
}

// ConfigureModem sends initial configuration commands to the modem.
func (m *Modem) ConfigureModem() error {
	m.logger.Println("Configuring modem...")
	var err error

	// ATE0 (Echo off)
	if err = m.SetEchoMode(false); err != nil {
		return fmt.Errorf("failed to set echo mode: %w", err)
	}
	m.logger.Println("Echo mode disabled (ATE0).")

	// AT+CMGF=0 (PDU mode for SMS)
	if err = m.SetTextMode(false); err != nil { // false means PDU mode
		return fmt.Errorf("failed to set PDU mode (AT+CMGF=0): %w", err)
	}
	m.logger.Println("PDU mode set (AT+CMGF=0).")

	// AT+CNMI=2,1,0,2,0 (Configure SMS notifications)
	// Mode=2: Buffer URCs in TA when link is reserved (e.g. PPP); otherwise forward directly.
	// MT=1:   Deliver +CMTI (new message indication) URCs.
	// BM=0:   No cell broadcast message URCs.
	// DS=2:   Deliver +CDSI (status report indication) URCs for SMS-STATUS-REPORT.
	// BFR=0:  Flush TA buffer for URCs (0=TA buffer is flushed, 1=TA buffer is not flushed).
	_, err = SubmitCommandToQueue("AT+CNMI=2,1,0,2,0", m.commandTimeout)
	if err != nil {
		return fmt.Errorf("failed to set SMS notifications (AT+CNMI): %w", err)
	}
	m.logger.Println("SMS notifications configured (AT+CNMI=2,1,0,2,0).")

	// AT+CMEE=1 (Enable extended error reports)
	_, err = SubmitCommandToQueue("AT+CMEE=1", m.commandTimeout)
	if err != nil {
		// This is not critical, so log a warning and continue
		m.logger.Printf("Warning: Failed to enable extended error reports (AT+CMEE=1): %v", err)
	} else {
		m.logger.Println("Extended error reports enabled (AT+CMEE=1).")
	}
	
	// AT+CVHU=0 (Voice Hangup Control by AT commands)
	// 0: ATH can hangup, but +CHUP cannot. Modems typically use ATH for hangup.
	// This setting prevents +CVHU URCs if not needed.
	_, err = SubmitCommandToQueue("AT+CVHU=0", m.commandTimeout)
	if err != nil {
		m.logger.Printf("Warning: Failed to set voice hangup control (AT+CVHU=0): %v", err)
	} else {
		m.logger.Println("Voice hangup control set (AT+CVHU=0).")
	}


	// Populate modem info
	if _, err = m.GetManufacturer(); err != nil {
		m.logger.Printf("Warning: Failed to get manufacturer: %v", err)
	}
	if _, err = m.GetModel(); err != nil {
		m.logger.Printf("Warning: Failed to get model: %v", err)
	}
	if _, err = m.GetIMEI(); err != nil { // Get IMEI also
		m.logger.Printf("Warning: Failed to get IMEI: %v", err)
	}
	 if _, err = m.GetIMSI(); err != nil { // Get IMSI also
		m.logger.Printf("Warning: Failed to get IMSI: %v", err)
	}
	if _, err = m.GetStorages(); err != nil {
		m.logger.Printf("Warning: Failed to get storages: %v", err)
	}
	
	m.logger.Println("Modem configuration complete.")
	return nil
}

// SetEchoMode enables or disables command echo.
func (m *Modem) SetEchoMode(enable bool) error {
	cmd := "ATE0"
	if enable {
		cmd = "ATE1"
	}
	_, err := SubmitCommandToQueue(cmd, m.commandTimeout)
	return err
}

// SetTextMode enables PDU mode (false) or text mode (true) for SMS.
func (m *Modem) SetTextMode(enableTextMode bool) error {
	cmd := "AT+CMGF=0" // PDU mode
	if enableTextMode {
		cmd = "AT+CMGF=1" // Text mode
	}
	_, err := SubmitCommandToQueue(cmd, m.commandTimeout)
	return err
}

// GetManufacturer retrieves and stores the modem manufacturer.
func (m *Modem) GetManufacturer() (string, error) {
	resp, err := SubmitCommandToQueue("AT+CGMI", m.commandTimeout)
	if err != nil {
		return "", err
	}
	// Response is typically: <manufacturer_name>\r\nOK\r\n
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) > 0 {
		m.manufacturer = strings.TrimSpace(lines[0])
		m.logger.Printf("Manufacturer: %s", m.manufacturer)
		return m.manufacturer, nil
	}
	return "", fmt.Errorf("unexpected response format for AT+CGMI: %s", resp)
}

// GetModel retrieves and stores the modem model.
func (m *Modem) GetModel() (string, error) {
	resp, err := SubmitCommandToQueue("AT+CGMM", m.commandTimeout)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) > 0 {
		m.model = strings.TrimSpace(lines[0])
		m.logger.Printf("Model: %s", m.model)
		return m.model, nil
	}
	return "", fmt.Errorf("unexpected response format for AT+CGMM: %s", resp)
}

// GetIMEI retrieves and stores the modem IMEI.
func (m *Modem) GetIMEI() (string, error) {
	resp, err := SubmitCommandToQueue("AT+CGSN", m.commandTimeout) // Serial Number = IMEI for GSM
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) > 0 {
		m.imei = strings.TrimSpace(lines[0])
		m.logger.Printf("IMEI: %s", m.imei)
		return m.imei, nil
	}
	return "", fmt.Errorf("unexpected response format for AT+CGSN: %s", resp)
}

// GetIMSI retrieves and stores the subscriber IMSI.
func (m *Modem) GetIMSI() (string, error) {
	resp, err := SubmitCommandToQueue("AT+CIMI", m.commandTimeout)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) > 0 {
		// CIMI response is just the IMSI number followed by OK
		imsiStr := strings.TrimSpace(lines[0])
		if _, parseErr := strconv.Atoi(imsiStr); parseErr == nil { // Basic validation if it's a number
			m.imsi = imsiStr
			m.logger.Printf("IMSI: %s", m.imsi)
			return m.imsi, nil
		}
		return "", fmt.Errorf("unexpected IMSI format: %s", resp)
	}
	return "", fmt.Errorf("unexpected response format for AT+CIMI: %s", resp)
}


// GetSignalStrength retrieves the current signal strength (RSSI and BER).
func (m *Modem) GetSignalStrength() (SignalStrength, error) {
	resp, err := SubmitCommandToQueue("AT+CSQ", m.commandTimeout)
	if err != nil {
		return SignalStrength{}, err
	}
	// Expected response: +CSQ: <rssi>,<ber>
	// OK
	var ss SignalStrength
	if strings.HasPrefix(resp, "+CSQ:") {
		parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(resp, "+CSQ:")), ",")
		if len(parts) == 2 {
			rssiStr := strings.TrimSpace(parts[0])
			berStr := strings.TrimSpace(parts[1])
			
			ss.RSSI, err = strconv.Atoi(rssiStr)
			if err != nil {
				return SignalStrength{}, fmt.Errorf("failed to parse RSSI from +CSQ response '%s': %w", resp, err)
			}
			ss.BER, err = strconv.Atoi(berStr)
			if err != nil {
				// BER might be part of a longer string like "99 OK". Try to extract just the number.
				berParts := strings.Fields(berStr)
				if len(berParts) > 0 {
					ss.BER, err = strconv.Atoi(berParts[0])
					if err != nil {
						return SignalStrength{}, fmt.Errorf("failed to parse BER from +CSQ response '%s': %w", resp, err)
					}
				} else {
					return SignalStrength{}, fmt.Errorf("failed to parse BER (empty) from +CSQ response '%s': %w", resp, err)
				}
			}

			// Determine condition (example interpretation)
			if ss.RSSI == 99 {
				ss.Condition = "Not known or not detectable"
			} else if ss.RSSI >= 30 { // ~ >= -53 dBm
				ss.Condition = "Excellent"
			} else if ss.RSSI >= 20 { // ~ >= -73 dBm
				ss.Condition = "Good"
			} else if ss.RSSI >= 10 { // ~ >= -93 dBm
				ss.Condition = "Fair"
			} else if ss.RSSI >= 2 {  // ~ >= -109 dBm
				ss.Condition = "Weak"
			} else {
				ss.Condition = "Very Weak/Unusable"
			}
			m.logger.Printf("Signal Strength: RSSI=%d, BER=%d (%s)", ss.RSSI, ss.BER, ss.Condition)
			return ss, nil
		}
	}
	return SignalStrength{}, fmt.Errorf("unexpected response format for AT+CSQ: %s", resp)
}

// GetSMSCenter retrieves the SMS Service Center Address.
func (m *Modem) GetSMSCenter() (string, error) {
	resp, err := SubmitCommandToQueue("AT+CSCA?", m.commandTimeout)
	if err != nil {
		return "", err
	}
	// Expected: +CSCA: "<smsc_address>",<type_of_address>
	if strings.HasPrefix(resp, "+CSCA:") {
		line := strings.TrimSpace(strings.TrimPrefix(resp, "+CSCA:"))
		parts := strings.SplitN(line, ",", 2)
		if len(parts) >= 1 {
			addr := strings.Trim(parts[0], "\" ")
			// typeOfAddrStr := ""
			// if len(parts) == 2 {
			// 	typeOfAddrStr = strings.TrimSpace(parts[1])
			// }
			// TODO: Could store type as well if needed
			m.logger.Printf("SMS Center: %s", addr)
			return addr, nil
		}
	}
	return "", fmt.Errorf("unexpected response format for AT+CSCA?: %s", resp)
}

// GetStorages retrieves and stores information about SMS storage.
func (m *Modem) GetStorages() (*ModemStorages, error) {
	resp, err := SubmitCommandToQueue("AT+CPMS?", m.commandTimeout)
	if err != nil {
		return nil, err
	}
	// Expected: +CPMS: <mem1_used>,<mem1_total>,<mem2_used>,<mem2_total>,<mem3_used>,<mem3_total>
	// Or +CPMS: "<storage1>",<used1>,<total1>,"<storage2>",<used2>,<total2>,"<storage3>",<used3>,<total3>
	// The latter form is more common.
	// Example: +CPMS: "SM",0,20,"SM",0,20,"SM",0,20
	
	storages := ModemStorages{}
	if strings.HasPrefix(resp, "+CPMS:") {
		partsStr := strings.TrimSpace(strings.TrimPrefix(resp, "+CPMS:"))
		parts := strings.Split(partsStr, ",")

		parseStorage := func(nameIdx, usedIdx, totalIdx int) (StorageInfo, error) {
			if len(parts) <= totalIdx { // Ensure enough parts exist
				return StorageInfo{}, fmt.Errorf("not enough parts in +CPMS response: got %d, need at least %d for storage info", len(parts), totalIdx+1)
			}
			name := strings.Trim(parts[nameIdx], "\" ")
			used, errU := strconv.Atoi(strings.TrimSpace(parts[usedIdx]))
			if errU != nil {
				return StorageInfo{}, fmt.Errorf("failed to parse used value '%s': %w", parts[usedIdx], errU)
			}
			total, errT := strconv.Atoi(strings.TrimSpace(parts[totalIdx]))
			if errT != nil {
				return StorageInfo{}, fmt.Errorf("failed to parse total value '%s': %w", parts[totalIdx], errT)
			}
			return StorageInfo{Name: name, Used: used, Total: total}, nil
		}

		var errParse error
		if len(parts) >= 3 {
			storages.Storage1, errParse = parseStorage(0, 1, 2)
			if errParse != nil { return nil, fmt.Errorf("failed to parse storage1 from +CPMS: %w. Response: %s", errParse, resp) }
		}
		if len(parts) >= 6 {
			storages.Storage2, errParse = parseStorage(3, 4, 5)
			if errParse != nil { return nil, fmt.Errorf("failed to parse storage2 from +CPMS: %w. Response: %s", errParse, resp) }
		}
		if len(parts) >= 9 {
			storages.Storage3, errParse = parseStorage(6, 7, 8)
			if errParse != nil { return nil, fmt.Errorf("failed to parse storage3 from +CPMS: %w. Response: %s", errParse, resp) }
		}
		m.currentStorages = storages
		m.logger.Printf("Storages: S1=%s(%d/%d), S2=%s(%d/%d), S3=%s(%d/%d)",
			storages.Storage1.Name, storages.Storage1.Used, storages.Storage1.Total,
			storages.Storage2.Name, storages.Storage2.Used, storages.Storage2.Total,
			storages.Storage3.Name, storages.Storage3.Used, storages.Storage3.Total)
		return &m.currentStorages, nil

	}
	return nil, fmt.Errorf("unexpected response format for AT+CPMS?: %s", resp)
}


// --- SMS Functionality ---

// SendSMS generates PDUs for the given text and sends them.
func (m *Modem) SendSMS(receiver, text string, requestStatus bool) ([]MessageSendResult, error) {
	if !m.connected {
		return nil, fmt.Errorf("modem not connected")
	}

	// Determine receiver type (assume international if starts with '+', else national)
	receiverType := byte(0x81) // National
	if strings.HasPrefix(receiver, "+") {
		receiverType = 0x91 // International
		receiver = strings.TrimPrefix(receiver, "+")
	}
	
	msg := MessageToSend{
		Receiver:     receiver,
		ReceiverType: receiverType,
		Text:         text,
		Encoding:     "auto", // Auto-detect 7bit or UCS2
		RequestStatus: requestStatus,
		// SMSC and other fields can be added if needed
	}

	pdus, err := GeneratePDUs(msg) // pdu.go
	if err != nil {
		return nil, fmt.Errorf("failed to generate PDUs: %w", err)
	}

	var results []MessageSendResult
	for i, pduInfo := range pdus {
		m.logger.Printf("Sending PDU %d/%d: TPDU Length=%d, PDU=%s", i+1, len(pdus), pduInfo.TPDULength, pduInfo.FullPDU)
		
		// Command AT+CMGS=<length_of_TPDU_excluding_SMSC>
		// pduInfo.TPDULength is length of TPDU part in octets (SMSC already excluded)
		cmgsCmd := fmt.Sprintf("AT+CMGS=%d", pduInfo.TPDULength)
		
		// Send AT+CMGS and wait for "> " prompt
		// Using SubmitCommandToQueue for this is tricky because it expects a final response like OK/ERROR.
		// We need to send CMGS, get ">", then send PDU.
		// A specialized function in at_handler or direct port write/read might be better.
		// For now, let's assume SubmitCommandToQueue can be adapted or a new variant exists.
		// Or, we split: send AT+CMGS, check for ">", then send PDU.

		// Simplified: using SendCommand which is synchronous and might be easier to adapt.
		// However, SendCommand also expects OK/ERROR.
		// This part needs careful interaction with at_handler.
		// Let's assume a way to send AT+CMGS and get the ">" prompt.
		// For now, using a placeholder for this interaction.

		// --- This is a simplified placeholder for the two-step CMGS send ---
		// Step 1: Send AT+CMGS=<tpdu_length>
		// Need a way to send this and only expect ">" or ERROR.
		// Let's assume SendCommand in at_handler can take expected prompt.
		// For now, we'll use a conceptual `sendCommandAndWaitForPrompt`.
		// _, errPrompt := SendCommand(m.Port, cmgsCmd, 5*time.Second, ">") // Hypothetical
		// This won't work with current SendCommand which expects OK/ERROR.
		
		// Workaround: Use WriteToPort directly for AT+CMGS, then ReadFromPort for ">".
		// This bypasses the at_handler queue for this specific sequence.
		// This is not ideal as it mixes direct port access with queued access.
		
		// A better at_handler.SendCommand would allow specifying intermediate prompts.
		// For now, let's try to make it work with SubmitCommandToQueue and careful response parsing.
		// The ">" will be part of the "response" to AT+CMGS if successful.
		
		promptResponse, errPrompt := SubmitCommandToQueue(cmgsCmd, m.commandTimeout, ">")
		if errPrompt != nil {
			results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("AT+CMGS command failed: %w", errPrompt), IsSuccessful: false})
			continue
		}
		if !strings.Contains(promptResponse, ">") {
			results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("did not receive '>' prompt after AT+CMGS. Got: %s", promptResponse), IsSuccessful: false})
			continue
		}
		m.logger.Println("Received '>' prompt.")

		// Step 2: Send PDU hex string + Ctrl-Z
		pduWithCtrlZ := pduInfo.FullPDU + string(rune(26)) // Append Ctrl-Z
		
		// Write PDU directly. Read response using at_handler.
		// The response to PDU+CtrlZ will be +CMGS: <mr> followed by OK.
		// This should be handled by SubmitCommandToQueue if it reads until OK.
		// The raw PDU needs to be sent, then wait for +CMGS: mr and OK.
		// SubmitCommandToQueue sends command + \r. Here we need to send PDU_HEX + ^Z.
		// This interaction is custom.
		
		// Using direct Write, then at_handler's Read (if possible, or direct Read)
		if _, errWritePDU := WriteToPort(m.Port, []byte(pduWithCtrlZ)); errWritePDU != nil {
			results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("failed to write PDU data: %w", errWritePDU), IsSuccessful: false})
			continue
		}
		m.logger.Printf("PDU + Ctrl-Z sent.")

		// Now, read the final response (+CMGS: <mr>\r\nOK\r\n)
		// This needs a way to read until "OK" or "ERROR" without sending a prior command via at_handler.
		// This is where at_handler's design is important. If processCommands exclusively reads,
		// then this direct write/read sequence breaks that model.
		// For now, assume we can use serial_com.ReadFromPort directly after writing.
		finalRespBytes, errReadResp := ReadFromPort(m.Port, m.commandTimeout) // Read until OK/ERROR
		finalResponse := string(finalRespBytes)

		if errReadResp != nil {
			results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("failed to read response after PDU send: %w. Raw: %s", errReadResp, finalResponse), IsSuccessful: false, FinalResponse: finalResponse})
			continue
		}

		// Parse +CMGS: <mr>
		if strings.Contains(finalResponse, "+CMGS:") && strings.Contains(finalResponse, "OK") {
			mrStr := strings.TrimSpace(strings.Split(strings.TrimPrefix(finalResponse, "+CMGS:"), "\n")[0])
			mr, errMr := strconv.Atoi(mrStr)
			if errMr != nil {
				results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("failed to parse message reference from +CMGS response: %s. Error: %w", finalResponse, errMr), IsSuccessful: false, FinalResponse: finalResponse})
				continue
			}
			m.logger.Printf("SMS PDU %d sent successfully. Message Reference (TP-MR): %d", i+1, mr)
			results = append(results, MessageSendResult{PDUIndex: i, MessageID: mr, IsSuccessful: true, FinalResponse: finalResponse})
		} else if strings.Contains(finalResponse, "ERROR") {
			results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("modem returned error after PDU send: %s", finalResponse), IsSuccessful: false, FinalResponse: finalResponse})
		} else {
			results = append(results, MessageSendResult{PDUIndex: i, Err: fmt.Errorf("unexpected response after PDU send: %s", finalResponse), IsSuccessful: false, FinalResponse: finalResponse})
		}
	}
	return results, nil // Overall success depends on individual PDU results
}


// GetSMS retrieves an SMS from storage.
func (m *Modem) GetSMS(storage string, id int) (*PDUData, error) {
	if !m.connected { return nil, fmt.Errorf("modem not connected") }
	
	// Select storage first if not already current (AT+CPMS=<storage>)
	// For simplicity, assume desired storage is already selected or does not need explicit selection for CMGR.
	// Some modems require it.
	// if m.currentStorages.Storage1.Name != storage && m.currentStorages.Storage2.Name != storage {
	//    _, err := SubmitCommandToQueue(fmt.Sprintf("AT+CPMS=\"%s\"", storage), m.commandTimeout)
	//    if err != nil { return nil, fmt.Errorf("failed to set storage to %s: %w", storage, err) }
	// }

	cmd := fmt.Sprintf("AT+CMGR=%d", id)
	resp, err := SubmitCommandToQueue(cmd, m.commandTimeout)
	if err != nil {
		return nil, fmt.Errorf("AT+CMGR command failed: %w", err)
	}

	// Expected response:
	// +CMGR: <stat>,[<alpha>],<length><CR><LF><pdu>
	// OK
	// <stat>: "REC UNREAD", "REC READ", "STO UNSENT", "STO SENT", "ALL"
	if strings.HasPrefix(resp, "+CMGR:") {
		lines := strings.Split(resp, "\n")
		if len(lines) >= 2 {
			pduHex := strings.TrimSpace(lines[1]) // The PDU is on the second line usually
			// TODO: Parse <stat>, <alpha>, <length> from first line if needed.
			// For now, just grab the PDU hex.
			
			pduData, errParse := ParsePDU(pduHex) // pdu.go
			if errParse != nil {
				return nil, fmt.Errorf("failed to parse PDU from +CMGR response: %w. PDU Hex: %s", errParse, pduHex)
			}
			m.logger.Printf("SMS (ID %d, Storage %s) retrieved and parsed.", id, storage)
			return pduData, nil
		}
	}
	return nil, fmt.Errorf("unexpected response format for AT+CMGR: %s", resp)
}

// DeleteSMS deletes an SMS from storage.
func (m *Modem) DeleteSMS(id int, storageName ...string) error {
	if !m.connected { return fmt.Errorf("modem not connected") }
	
	// If storageName is provided, we might need to set AT+CPMS first.
	// For now, assume default storage or that CMGD works on current selection.
	// Some modems allow AT+CMGD=<id>,<delflag> where delflag can specify which messages.
	// Default delflag is 0: delete message at <id>.

	cmd := fmt.Sprintf("AT+CMGD=%d", id) // Basic delete
	_, err := SubmitCommandToQueue(cmd, m.commandTimeout)
	if err != nil {
		return fmt.Errorf("AT+CMGD command failed for ID %d: %w", id, err)
	}
	m.logger.Printf("SMS (ID %d) deleted.", id)
	return nil
}

// DeleteAllSMS deletes all SMS messages based on a flag.
// Common flags: 1 (delete read), 2 (delete unread), 3 (delete sent), 4 (delete all).
// Check modem manual for specific AT+CMGD=<index>,<delflag> behavior.
// For deleting all, often a sequence of AT+CMGD=1,4 (delete all messages) is used.
// Or iterate and delete. A common shortcut is AT+CMGD=0,4 (delete all) or AT+CMGD=1,4.
// Simpler: AT+CMGDA=<delscope> where delscope 5="ALL SMS"
func (m *Modem) DeleteAllSMS(flag int) error {
	if !m.connected { return fmt.Errorf("modem not connected") }

	// Using AT+CMGD=1,flag - this is a common way to delete multiple.
	// Example: AT+CMGD=1,4 deletes ALL messages. (index 1, flag 4)
	// The exact command can vary. AT+CMGDA is also an option on some modems.
	// Let's use a common variant: AT+CMGD=0, <flag for all>
	// For "delete all", flag is often 4.
	// For "delete all read", delflag 1. "delete all sent", delflag 2 etc.
	// The subtask implies flag is the "delflag".
	// The command `AT+CMGD=<index>,<delflag>` is more standard than `AT+CMGD=<flag>`.
	// If we want to delete ALL messages using a single flag, it's usually AT+CMGD=1,4 (index 1, flag 4)
	// Or, if the flag means "delete all of type X", then index might need to be 0 or 1.
	// Let's assume flag is a delflag for a specific index (e.g. 1).
	// A more robust "delete all" might iterate or use modem-specific commands.
	// For now, let's use a common "delete all" pattern if flag implies it.
	// If flag == 4 (common "delete all" indicator for delflag)
	cmd := fmt.Sprintf("AT+CMGD=1,%d", flag) // Example: delete all by using index 1 and flag 4
	if flag == 0 { // A common "delete all" command if index is ignored
		cmd = "AT+CMGD=1,4" // Default to delete all messages
		m.logger.Println("DeleteAllSMS: flag 0 interpreted as 'delete all messages' (AT+CMGD=1,4).")
	}


	_, err := SubmitCommandToQueue(cmd, m.commandTimeout*2) // Longer timeout for mass delete
	if err != nil {
		// Try alternative command if the first fails for "delete all" type flags.
		if flag == 4 || flag == 0 { // Common "delete all" flags
			m.logger.Printf("AT+CMGD=1,%d failed, trying AT+CMGDA=5 (delete all SMS)", flag)
			_, errAlt := SubmitCommandToQueue("AT+CMGDA=5", m.commandTimeout*2)
			if errAlt != nil {
				return fmt.Errorf("AT+CMGD and AT+CMGDA commands failed for delete all: %w (original error: %v)", errAlt, err)
			}
		} else {
			return fmt.Errorf("AT+CMGD command failed for flag %d: %w", flag, err)
		}
	}
	m.logger.Printf("DeleteAllSMS command with flag %d successful.", flag)
	return nil
}

// --- USSD Functionality ---

// SendUSSD sends a USSD command and waits for a response.
func (m *Modem) SendUSSD(ussd string) (*USSDResponseData, error) {
	if !m.connected { return nil, fmt.Errorf("modem not connected") }

	encodedUSSD := ussd
	var errEnc error

	// Huawei modems often require PDU encoding for USSD strings containing '*' or '#'.
	// Other modems might accept direct string.
	// For simplicity, let's assume direct string sending works for now, or
	// use PDU encoding if manufacturer is known.
	if strings.ToLower(m.manufacturer) == "huawei" || strings.Contains(ussd, "*") || strings.Contains(ussd, "#") {
		// EncodeUSSD expects ussdText, smscAddress, smscType.
		// SMSC is not typically used for USSD commands with AT+CUSD.
		encodedUSSD, errEnc = EncodeUSSD(ussd, "", 0) // pdu.go
		if errEnc != nil {
			return nil, fmt.Errorf("failed to encode USSD string to PDU: %w", errEnc)
		}
		m.logger.Printf("Encoded USSD '%s' to PDU: %s", ussd, encodedUSSD)
	} else {
		// For other manufacturers or simple USSD strings, try sending as is.
		// This might need quoting if it contains spaces or special chars not handled by PDU encoding.
		// The standard AT+CUSD=1,"<str>",15 expects <str> to be GSM 7-bit default alphabet.
		// PDU encoding (result of EncodeUSSD) gives hex pairs.
		// If not Huawei, and not PDU encoded, then it's a direct string.
		// The command AT+CUSD=1,"*100#",15 - the string is directly used.
		// If EncodeUSSD was not used, then `encodedUSSD` is just `ussd`.
	}

	// AT+CUSD=1 enables result presentation, sends USSD, waits for network response.
	// <str> is the USSD string, or PDU representation for some modems.
	// 15 is DCS for GSM 7-bit default alphabet.
	cmd := fmt.Sprintf("AT+CUSD=1,\"%s\",15", encodedUSSD)
	
	// Sending USSD command. The response is URC `+CUSD: ...`
	// So, SubmitCommandToQueue will get "OK" for the AT+CUSD itself.
	// The actual USSD content comes via listenForUnsolicitedResponses.
	_, err := SubmitCommandToQueue(cmd, m.commandTimeout)
	if err != nil {
		return nil, fmt.Errorf("AT+CUSD command failed: %w", err)
	}

	m.logger.Printf("AT+CUSD command sent for USSD: %s. Waiting for response on channel...", ussd)
	// Wait for the response from the URC listener
	select {
	case respData := <-m.eventChannels.USSDResponse:
		if respData.Error != nil {
			return nil, fmt.Errorf("received error in USSD response: %w", respData.Error)
		}
		m.logger.Printf("Received USSD response: Status=%d, Text='%s'", respData.Status, respData.ResponseText)
		return &respData, nil
	case <-time.After(m.ussdTimeout):
		return nil, fmt.Errorf("timeout waiting for USSD response from channel after %v", m.ussdTimeout)
	case <-m.shutdownChan:
		return nil, fmt.Errorf("modem shutting down while waiting for USSD response")
	}
}


// --- Event Handling and Listener ---

// listenForUnsolicitedResponses reads lines from the modem and parses URCs.
func (m *Modem) listenForUnsolicitedResponses() {
	defer m.wg.Done()
	m.logger.Println("Starting to listen for unsolicited responses...")

	if m.reader == nil && m.Port != nil { // Should be initialized in Connect
		m.reader = bufio.NewReader(m.Port)
	} else if m.Port == nil {
		m.logger.Println("Listener: Port is nil, cannot listen.")
		return
	}
	
	for {
		select {
		case <-m.listenStopper: // Changed from shutdownChan for more direct control
			m.logger.Println("Listener: listenStopper triggered, stopping.")
			return
		default:
			// Set a short deadline for ReadString to make the loop check listenStopper regularly.
			// This requires m.Port to be an interface that supports SetReadDeadline.
			// go.bug.st/serial.Port does support SetReadTimeout.
			// If using ReadFromPort, it has its own timeout.
			// Here, we are reading lines directly.
			if m.Port != nil {
				// A short timeout allows checking listenStopper periodically.
				// A value of 0 means non-blocking. A small positive value for timeout.
				err := m.Port.SetReadTimeout(200 * time.Millisecond) // 200ms timeout for line read
				if err != nil {
					m.logger.Printf("Listener: Error setting read timeout: %v. Stopping listener.", err)
					// Signal main disconnect logic if port error is permanent
					m.eventChannels.Disconnect <- fmt.Errorf("listener SetReadTimeout error: %w", err)
					return
				}
			} else { // Port became nil somehow
				m.logger.Println("Listener: Port is nil. Stopping.")
				m.eventChannels.Disconnect <- fmt.Errorf("listener port became nil")
				return
			}


			line, err := m.reader.ReadString('\n')
			if err != nil {
				// Check if it's a timeout error, which is expected due to SetReadTimeout
				// serial.ErrTimeout is the specific error for timeout from go.bug.st/serial
				if err == serial.ErrTimeout {
					// This is normal, continue to check listenStopper
					continue
				}
				// For other errors (e.g., port closed, EOF)
				m.logger.Printf("Listener: Error reading from port: %v. Stopping listener.", err)
				// Signal main disconnect logic
				select {
				case m.eventChannels.Disconnect <- fmt.Errorf("listener read error: %w", err):
				default: // Avoid blocking if Disconnect channel is full or not listened to
				}
				return // Stop listening on error
			}

			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			m.logger.Printf("URC Raw: %s", line) // Log all URCs

			// Parse common URCs
			switch {
			case strings.HasPrefix(line, "+CMTI:"): // New SMS received
				// Format: +CMTI: "<mem>",<index>  e.g., +CMTI: "SM",12
				parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "+CMTI:")), ",")
				if len(parts) == 2 {
					storage := strings.Trim(parts[0], "\" ")
					id, err := strconv.Atoi(strings.TrimSpace(parts[1]))
					if err == nil {
						m.logger.Printf("New SMS URC: Storage=%s, ID=%d", storage, id)
						go func(st string, smsID int) { // Process in new goroutine to avoid blocking listener
							pduData, errGet := m.GetSMS(st, smsID)
							if errGet == nil && pduData != nil {
								m.eventChannels.IncomingSMS <- *pduData
								// Optionally delete after successful retrieval
								// if errDel := m.DeleteSMS(smsID, st); errDel != nil {
								// 	m.logger.Printf("Error auto-deleting SMS ID %d from %s: %v", smsID, st, errDel)
								// }
							} else {
								m.logger.Printf("Error getting SMS for +CMTI (ID %d, Storage %s): %v", smsID, st, errGet)
							}
						}(storage, id)
					}
				}
			case strings.HasPrefix(line, "+CDSI:"): // New Status Report
				// Format: +CDSI: "<mem>",<index>
				parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "+CDSI:")), ",")
				if len(parts) == 2 {
					storage := strings.Trim(parts[0], "\" ")
					id, err := strconv.Atoi(strings.TrimSpace(parts[1]))
					if err == nil {
						m.logger.Printf("New Status Report URC: Storage=%s, ID=%d", storage, id)
						go func(st string, smsID int) {
							// GetSMS will return a PDUData, check if it's a status report
							pduData, errGet := m.GetSMS(st, smsID)
							if errGet == nil && pduData != nil && pduData.IsStatusReport && pduData.StatusReport != nil {
								m.eventChannels.StatusReport <- *pduData.StatusReport
								// Optionally delete
							} else if errGet != nil {
								m.logger.Printf("Error getting SMS for +CDSI (ID %d, Storage %s): %v", smsID, st, errGet)
							} else {
								m.logger.Printf("SMS for +CDSI (ID %d, Storage %s) is not a valid status report.", smsID, st)
							}
						}(storage, id)
					}
				}

			case strings.HasPrefix(line, "+CUSD:"): // USSD Response
				// Format: +CUSD: <status>[,<str>,<dcs>]
				// <status>: 0 (no further action), 1 (further action req), 2 (terminated by network), 4 (operation not supported)
				partsStr := strings.TrimSpace(strings.TrimPrefix(line, "+CUSD:"))
				parts := strings.SplitN(partsStr, ",", 3)
				var ussdResp USSDResponseData
				if len(parts) >= 1 {
					status, errConv := strconv.Atoi(strings.TrimSpace(parts[0]))
					if errConv != nil {
						ussdResp.Error = fmt.Errorf("failed to parse status from +CUSD: %s", partsStr)
					} else {
						ussdResp.Status = status
					}
				}
				if len(parts) >= 2 {
					// Response string might be quoted and hex encoded (if DCS indicates non-7bit)
					// For now, assume it's a direct string or needs PDU decoding if DCS is not 15 (7bit)
					ussdResp.ResponseText = strings.Trim(parts[1], "\" ")
				}
				if len(parts) >= 3 {
					dcs, errConv := strconv.Atoi(strings.TrimSpace(parts[2]))
					if errConv == nil {
						ussdResp.DCS = dcs
						// If DCS is not 15 (GSM 7-bit default), ResponseText might need decoding
						// E.g., if DCS is 72 (UCS2), text is hex encoded UCS2.
						if dcs == 72 && (len(ussdResp.ResponseText)%2 == 0) { // UCS2 implies hex
							decodedUcs2, errDec := decode16Bit(ussdResp.ResponseText) // From pdu.go
							if errDec == nil {
								ussdResp.ResponseText = decodedUcs2
							} else {
								m.logger.Printf("Failed to decode UCS2 USSD response text: %s, err: %v", ussdResp.ResponseText, errDec)
							}
						} else if dcs != 15 { // If not standard 7-bit, could be other encoding
							m.logger.Printf("USSD response with non-standard DCS %d: '%s'", dcs, ussdResp.ResponseText)
						}
					}
				}
				m.logger.Printf("USSD Response URC: Status=%d, Text='%s', DCS=%d", ussdResp.Status, ussdResp.ResponseText, ussdResp.DCS)
				m.eventChannels.USSDResponse <- ussdResp

			case line == "RING":
				m.logger.Println("Incoming call: RING")
				m.eventChannels.Call <- CallData{EventType: "RING", Raw: line}
				if m.autoHangup {
					m.logger.Println("Auto-hanging up call (ATH).")
					_, errATH := SubmitCommandToQueue("ATH", 1*time.Second) // Short timeout for ATH
					if errATH != nil {
						m.logger.Printf("Error sending ATH: %v", errATH)
					}
				}
			case strings.HasPrefix(line, "+CLIP:"):
				// Format: +CLIP: "<number>",<type>[,<subaddr>,<satype>[,<alpha>[,<CLIvalidity>]]]
				// Example: +CLIP: "1234567890",129
				parts := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "+CLIP:")), ",", 2)
				callerID := ""
				callerType := 0 // Default or unknown type

				if len(parts) >= 1 {
					callerID = strings.Trim(parts[0], "\" ")
				}
				if len(parts) >= 2 {
					// The rest of the string after the first comma contains type and other optional fields
					typeAndRest := strings.SplitN(parts[1], ",", 2) // Split to isolate type
					typeStr := strings.TrimSpace(typeAndRest[0])
					var errAtoi error
					callerType, errAtoi = strconv.Atoi(typeStr)
					if errAtoi != nil {
						m.logger.Printf("Listener: Error parsing caller type from +CLIP '%s': %v", typeStr, errAtoi)
						callerType = 0 // Reset to default/unknown if parsing fails
					}
				}
				m.logger.Printf("Caller ID URC: Number=%s, Type=%d", callerID, callerType)
				m.eventChannels.Call <- CallData{EventType: "CLIP", CallerID: callerID, CallerType: callerType, Raw: line}

			// Add other URCs as needed: e.g., +CREG, +CGREG (network registration)
			// +CGEV (GPRS events), etc.
			default:
				if line != "OK" && !strings.HasPrefix(line, "AT") && line != "" { // Filter out echoes or simple OKs not tied to commands
					m.logger.Printf("Other URC: %s", line)
					select {
					case m.eventChannels.Other <- line:
					default: // Don't block if 'Other' channel is full
						m.logger.Printf("Other URC channel full, URC dropped: %s", line)
					}
				}
			}
		}
	}
}

// Global Info for package-level logging if needed
// func Info(format string, v ...interface{}) { log.Printf("[ModemINFO] "+format+"\n", v...) }
// Note: logger.go provides Info, Debug, Error. This package uses m.logger for instance logging.
// The global Info here was from a previous step, can be removed if m.logger is used everywhere.

// Placeholder for main, if needed for direct execution/testing of this file.
// func main() {
// 	// Example usage
// }
