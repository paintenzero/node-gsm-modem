package main

import (
	"bufio"
	"bytes"
	"fmt"
	"time"

	"go.bug.st/serial"
)

// ListSerialPorts returns a list of available serial port names.
func ListSerialPorts() ([]string, error) {
	ports, err := serial.GetPortsList()
	if err != nil {
		return nil, fmt.Errorf("error listing serial ports: %w", err)
	}
	if len(ports) == 0 {
		return nil, fmt.Errorf("no serial ports found")
	}
	return ports, nil
}

// OpenPort opens a serial port with the given name and baud rate.
// It uses default settings for DataBits (8), StopBits (1), and Parity (None).
func OpenPort(portName string, baudRate int) (serial.Port, error) {
	mode := &serial.Mode{
		BaudRate: baudRate,
		DataBits: 8,
		StopBits: serial.OneStopBit,
		Parity:   serial.NoParity,
	}
	port, err := serial.Open(portName, mode)
	if err != nil {
		return nil, fmt.Errorf("error opening port %s: %w", portName, err)
	}
	Info("Port %s opened successfully with baud rate %d", portName, baudRate)
	return port, nil
}

// WriteToPort writes data to the serial port.
// It appends a carriage return (\r) to the data, as is standard for AT commands.
func WriteToPort(port serial.Port, data []byte) (int, error) {
	dataWithCR := append(data, '\r')
	n, err := port.Write(dataWithCR)
	if err != nil {
		return 0, fmt.Errorf("error writing to port: %w", err)
	}
	Debug("Wrote to port: %s", string(dataWithCR))
	return n, nil
}

// ReadFromPort reads data from the serial port until a delimiter or timeout.
// Common AT command delimiters are \r\nOK\r\n or \r\nERROR\r\n.
// For notifications or other responses, it might just be \r\n.
// This function tries to read until "OK", "ERROR", or a timeout.
func ReadFromPort(port serial.Port, timeout time.Duration) ([]byte, error) {
	if err := port.SetReadTimeout(timeout); err != nil {
		return nil, fmt.Errorf("error setting read timeout: %w", err)
	}

	var buffer bytes.Buffer
	reader := bufio.NewReader(port)
	startTime := time.Now()

	for {
		if time.Since(startTime) > timeout {
			Debug("Read timeout occurred. Current buffer: %s", buffer.String())
			return buffer.Bytes(), fmt.Errorf("read timeout after %v. Partial data: %q", timeout, buffer.String())
		}

		line, err := reader.ReadBytes('\n') // Read up to \n
		if err != nil {
			// If it's a timeout error, we check if we've already exceeded the overall timeout.
			// serial.Port.SetReadTimeout is not always respected on a per-Read basis by all drivers.
			if err == serial.ErrTimeout {
				if time.Since(startTime) > timeout {
					Debug("Serial library timeout coincided with overall timeout. Buffer: %s", buffer.String())
					return buffer.Bytes(), fmt.Errorf("read timeout after %v. Partial data: %q", timeout, buffer.String())
				}
				// If not overall timeout, continue trying to read
				continue
			}
			Debug("Error reading from port: %v. Buffer: %s", err, buffer.String())
			return buffer.Bytes(), fmt.Errorf("error reading from port: %w. Partial data: %q", err, buffer.String())
		}

		buffer.Write(line)
		Debug("Read line: %s", string(line))

		// Check for common AT command terminators.
		// These might be part of a larger response, so we keep reading until timeout,
		// but this gives an idea of what we're looking for.
		// A more robust solution would involve a state machine for AT command parsing.
		if bytes.Contains(buffer.Bytes(), []byte("OK\r\n")) ||
			bytes.Contains(buffer.Bytes(), []byte("ERROR\r\n")) ||
			bytes.Contains(buffer.Bytes(), []byte(">\r\n")) { // For commands expecting input like SMS PDU
			Debug("Detected command terminator. Buffer: %s", buffer.String())
			break
		}
	}
	Info("Read from port: %s", buffer.String())
	return buffer.Bytes(), nil
}

// ClosePort closes the serial port.
func ClosePort(port serial.Port) error {
	if port == nil {
		return fmt.Errorf("port is nil, cannot close")
	}
	err := port.Close()
	if err != nil {
		return fmt.Errorf("error closing port: %w", err)
	}
	Info("Port closed successfully.")
	return nil
}
