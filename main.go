package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	// No explicit import for github.com/user/go-gsm-modem needed if all .go files are in package main
	// and in the same directory. The `go run .` or `go build .` will compile them together.
)

var (
	portList        string
	baudRate        int
	recipientNumber string
	ussdCommand     string
	debugMode       bool
	autoHangup      bool
	reconnect       bool
)

func init() {
	flag.StringVar(&portList, "ports", "/dev/ttyUSB0,/dev/ttyUSB1,/dev/ttyUSB2", "Comma-separated list of serial ports to try")
	flag.IntVar(&baudRate, "baud", 115200, "Serial port baud rate")
	flag.StringVar(&recipientNumber, "recipient", "", "Recipient phone number for sending SMS (e.g., +1234567890)")
	flag.StringVar(&ussdCommand, "ussd", "", "USSD command to send (e.g., *100#)")
	flag.BoolVar(&debugMode, "debug", true, "Enable debug logging for the modem library")
	flag.BoolVar(&autoHangup, "autohangup", true, "Automatically hang up incoming calls")
	flag.BoolVar(&reconnect, "reconnect", false, "Enable modem reconnection attempts on disconnect")
	flag.Parse()
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile | log.Lmicroseconds)
	log.Println("--- Go GSM Modem Example Application Starting ---")

	// --- Configuration ---
	ports := strings.Split(portList, ",")
	if len(ports) == 0 || ports[0] == "" {
		log.Fatalf("No serial ports specified. Use -ports /dev/ttyUSB0,...")
	}

	log.Printf("Configuration: Ports=%v, Baud=%d, Recipient=%s, USSD=%s, Debug=%t, AutoHangup=%t, Reconnect=%t",
		ports, baudRate, recipientNumber, ussdCommand, debugMode, autoHangup, reconnect)

	// --- Initialize Modem ---
	opts := ModemOptions{ // Assuming ModemOptions is in the same package 'main'
		Ports:             ports,
		BaudRate:          baudRate,
		CommandTimeoutSec: 15, // Increased for potentially slow networks/modems
		USSDFailTimeoutSec:30, // Increased for USSD
		AutoHangup:        autoHangup,
		Forever:           reconnect,
		Debug:             debugMode,
	}

	// Use the global logger from logger.go, set its level.
	// Assuming logger.go's SetLogLevel and logger instance are accessible.
	// For simplicity here, Modem's internal logger will use its Debug option.
	// If logger.go has a global SetLogLevel:
	// if debugMode {
	//    SetLogLevel(DEBUG)
	// } else {
	//    SetLogLevel(INFO)
	// }

	modem, err := NewModem(opts) // Assuming NewModem is in the same package 'main'
	if err != nil {
		log.Fatalf("MAIN: Failed to create modem: %v", err)
	}

	// --- Setup Signal Handling for Graceful Shutdown ---
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	appShutdown := make(chan struct{}) // To signal consumer goroutines

	var wg sync.WaitGroup

	// --- Event Channel Consumers ---
	events := modem.eventChannels // Accessing directly as per current Modem struct

	// Incoming SMS
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("MAIN: SMS event handler started.")
		for {
			select {
			case sms := <-events.IncomingSMS:
				log.Printf("MAIN: === New SMS Received ===\n          From: %s\n          Time: %s\n          Text: %s\n          PDU: %s\n",
					sms.Sender, sms.Timestamp.Format(time.RFC1123), sms.Text, sms.RawPDU)
				if sms.UDH.IsPresent {
					log.Printf("          UDH: Ref=%d, Part %d/%d\n", sms.UDH.Reference, sms.UDH.CurrentPart, sms.UDH.TotalParts)
				}
				log.Println("MAIN: ========================")
			case <-appShutdown:
				log.Println("MAIN: SMS event handler shutting down.")
				return
			}
		}
	}()

	// Status Reports
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("MAIN: Status Report event handler started.")
		for {
			select {
			case sr := <-events.StatusReport:
				log.Printf("MAIN: === Status Report Received ===\n          Msg Ref: %d\n          Recipient: %s\n          Status: %s (0x%02X)\n          Discharge Time: %s\n",
					sr.MessageReference, sr.RecipientAddress, sr.StatusStr, sr.Status, sr.DischargeTime.Format(time.RFC1123))
				log.Println("MAIN: ============================")
			case <-appShutdown:
				log.Println("MAIN: Status Report event handler shutting down.")
				return
			}
		}
	}()

	// USSD Responses
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("MAIN: USSD event handler started.")
		for {
			select {
			case ussdResp := <-events.USSDResponse:
				if ussdResp.Error != nil {
					log.Printf("MAIN: === USSD Error ===\n          Error: %v\n", ussdResp.Error)
				} else {
					log.Printf("MAIN: === USSD Response ===\n          Status: %d\n          Response: %s\n          DCS: %d\n",
						ussdResp.Status, ussdResp.ResponseText, ussdResp.DCS)
				}
				log.Println("MAIN: =====================")
			case <-appShutdown:
				log.Println("MAIN: USSD event handler shutting down.")
				return
			}
		}
	}()

	// Calls
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("MAIN: Call event handler started.")
		for {
			select {
			case call := <-events.Call:
				log.Printf("MAIN: === Call Event ===\n          Type: %s\n          Caller ID: %s (Type: %d)\n          Raw: %s\n",
					call.EventType, call.CallerID, call.CallerType, call.Raw)
				log.Println("MAIN: ==================")
			case <-appShutdown:
				log.Println("MAIN: Call event handler shutting down.")
				return
			}
		}
	}()

	// Disconnects
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("MAIN: Disconnect event handler started.")
		for {
			select {
			case err := <-events.Disconnect:
				log.Printf("MAIN: Modem disconnected: %v", err)
				if !reconnect { // If not in 'forever' mode handled by modem itself
					log.Println("MAIN: Not configured to reconnect automatically. Signaling app shutdown.")
					// Could send to sigs chan to trigger main shutdown sequence
					// sigs <- syscall.SIGINT // Or close(appShutdown) directly if that's the only trigger
				}
			case <-appShutdown:
				log.Println("MAIN: Disconnect event handler shutting down.")
				return
			}
		}
	}()
	
	// Other Unsolicited Responses
	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Println("MAIN: Other URC handler started.")
		for {
			select {
			case urc := <-events.Other:
				log.Printf("MAIN: Other URC: %s", urc)
			case <-appShutdown:
				log.Println("MAIN: Other URC handler shutting down.")
				return
			}
		}
	}()


	// --- Connect ---
	log.Println("MAIN: Connecting to modem...")
	err = modem.Connect() // This now also starts the AT command processor and listener
	if err != nil {
		log.Printf("MAIN: Failed to connect to modem: %v. Closing application.", err)
		close(appShutdown) // Signal consumers to stop
		modem.Close()      // Attempt to clean up partially initialized modem state
		wg.Wait()
		os.Exit(1)
	}
	log.Println("MAIN: Modem connected successfully.")

	// --- Perform Actions ---
	log.Println("MAIN: --- Performing Modem Actions ---")

	imei, err := modem.GetIMEI()
	if err != nil { log.Printf("MAIN: Error getting IMEI: %v", err) } else { log.Printf("MAIN: IMEI: %s", imei) }

	imsi, err := modem.GetIMSI()
	if err != nil { log.Printf("MAIN: Error getting IMSI: %v", err) } else { log.Printf("MAIN: IMSI: %s", imsi) }

	mfr, err := modem.GetManufacturer()
	if err != nil { log.Printf("MAIN: Error getting Manufacturer: %v", err) } else { log.Printf("MAIN: Manufacturer: %s", mfr) }

	mdl, err := modem.GetModel()
	if err != nil { log.Printf("MAIN: Error getting Model: %v", err) } else { log.Printf("MAIN: Model: %s", mdl) }

	signal, err := modem.GetSignalStrength()
	if err != nil { log.Printf("MAIN: Error getting Signal Strength: %v", err) } else { log.Printf("MAIN: Signal Strength: RSSI=%d, BER=%d (%s)", signal.RSSI, signal.BER, signal.Condition) }

	smsc, err := modem.GetSMSCenter()
	if err != nil { log.Printf("MAIN: Error getting SMSC: %v", err) } else { log.Printf("MAIN: SMSC: %s", smsc) }
	
	storages, err := modem.GetStorages()
	if err != nil { log.Printf("MAIN: Error getting Storages: %v", err) } else { 
		log.Printf("MAIN: Storages: S1=%s(%d/%d), S2=%s(%d/%d), S3=%s(%d/%d)",
			storages.Storage1.Name, storages.Storage1.Used, storages.Storage1.Total,
			storages.Storage2.Name, storages.Storage2.Used, storages.Storage2.Total,
			storages.Storage3.Name, storages.Storage3.Used, storages.Storage3.Total)
	}


	if recipientNumber != "" {
		log.Printf("MAIN: Sending SMS to %s...", recipientNumber)
		// Test with a message that might include non-GSM7 characters to test UCS2 encoding
		// And long enough to test multipart if library supports it well.
		// smsText := "Hello from go-gsm-modem! This is a test message. Привет мир! €$@"
		smsText := "Test SMS from go-gsm-modem. Part1. And this is Part2 to make it a multipart message for sure."
		results, err := modem.SendSMS(recipientNumber, smsText, true) // Request status report
		if err != nil {
			log.Printf("MAIN: Error sending SMS: %v", err)
		} else {
			for _, res := range results {
				if res.IsSuccessful {
					log.Printf("MAIN: SMS part sent successfully! Message Reference: %d. Modem Response: %s", res.MessageID, res.FinalResponse)
				} else {
					log.Printf("MAIN: Failed to send SMS part: %v. Modem Response: %s", res.Err, res.FinalResponse)
				}
			}
		}
	} else {
		log.Println("MAIN: No recipient number provided, skipping SendSMS test.")
	}

	if ussdCommand != "" {
		log.Printf("MAIN: Sending USSD command %s...", ussdCommand)
		// The SendUSSD function in modem.go now waits for the response from the channel.
		ussdResp, err := modem.SendUSSD(ussdCommand)
		if err != nil {
			log.Printf("MAIN: Error sending USSD command: %v", err)
		} else if ussdResp != nil { // Should always be non-nil if err is nil
			log.Printf("MAIN: USSD command submission successful. Response (from SendUSSD direct return): Status=%d, Text='%s', DCS=%d",
				ussdResp.Status, ussdResp.ResponseText, ussdResp.DCS)
			if ussdResp.Error != nil {
				log.Printf("MAIN: Error embedded in USSD response object: %v", ussdResp.Error)
			}
		}
	} else {
		log.Println("MAIN: No USSD command provided, skipping SendUSSD test.")
	}
	
	log.Println("MAIN: --- Modem Actions Complete ---")
	log.Println("MAIN: Application is running. Waiting for events or shutdown signal (Ctrl+C)...")


	// --- Wait for Shutdown Signal ---
	<-sigs
	log.Println("MAIN: Shutdown signal received.")

	// --- Initiate Shutdown ---
	log.Println("MAIN: Signaling event handlers to stop...")
	close(appShutdown) // Signal event consumers to stop

	log.Println("MAIN: Closing modem connection...")
	if err := modem.Close(); err != nil { // modem.Close() handles its goroutines (listener, AT command processor, reconnectLoop)
		log.Printf("MAIN: Error closing modem: %v", err)
	}

	log.Println("MAIN: Waiting for application event handlers to finish...")
	// Timeout for application-level goroutines
	appWgDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(appWgDone)
	}()

	select {
	case <-appWgDone:
		log.Println("MAIN: All application event handlers finished.")
	case <-time.After(3 * time.Second): // Shorter timeout for app-level goroutines
		log.Println("MAIN: Timeout waiting for application event handlers.")
	}

	log.Println("MAIN: Application shut down gracefully.")
	log.Println("--- Go GSM Modem Example Application Terminated ---")
}
