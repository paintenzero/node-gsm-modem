package main

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"go.bug.st/serial"
)

// ATCommand holds the details for an AT command execution.
type ATCommand struct {
	Cmd                      string
	ExpectedResponsePatterns []string      // Patterns to look for in the response
	ResponseChannel          chan string   // Channel to send the raw response string
	ErrorChannel             chan error    // Channel to send an error
	Timeout                  time.Duration // Timeout for the command
}

// commandQueue is a buffered channel for AT commands.
// A buffer can be useful if commands are submitted slightly faster than they can be processed.
var commandQueue = make(chan ATCommand, 10) // Buffer size of 10

// processCommands is a goroutine that reads commands from the commandQueue and executes them.
// It needs to be started once the serial port is open.
func processCommands(port serial.Port, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	}
	Info("AT Command Processor started.")
	for cmd := range commandQueue {
		Info("Processing command from queue: %s", cmd.Cmd)

		if port == nil {
			err := fmt.Errorf("serial port is not open, cannot process command: %s", cmd.Cmd)
			Error(err.Error())
			if cmd.ErrorChannel != nil {
				cmd.ErrorChannel <- err
			}
			continue // Skip to the next command
		}

		// Write the command to the port. WriteToPort already appends \r.
		_, err := WriteToPort(port, []byte(cmd.Cmd))
		if err != nil {
			detailedErr := fmt.Errorf("failed to write command '%s': %w", cmd.Cmd, err)
			Error(detailedErr.Error())
			if cmd.ErrorChannel != nil {
				cmd.ErrorChannel <- detailedErr
			}
			continue
		}

		// Read the response from the port.
		rawResponseBytes, err := ReadFromPort(port, cmd.Timeout)
		rawResponse := string(rawResponseBytes)
		rawResponse = strings.TrimSpace(rawResponse) // Clean up response

		if err != nil {
			// ReadFromPort might return data even on timeout error, so log it.
			detailedErr := fmt.Errorf("error reading response for command '%s': %w. Partial response: %q", cmd.Cmd, err, rawResponse)
			Error(detailedErr.Error())
			if cmd.ErrorChannel != nil {
				cmd.ErrorChannel <- detailedErr
			}
			continue
		}

		Info("Received response for command '%s': [%s]", cmd.Cmd, rawResponse)

		// Basic response parsing and validation
		if strings.Contains(rawResponse, "ERROR") {
			err := fmt.Errorf("command '%s' failed with ERROR: %s", cmd.Cmd, rawResponse)
			Error(err.Error())
			if cmd.ErrorChannel != nil {
				cmd.ErrorChannel <- err
			}
			continue
		}

		// Check for expected response patterns if provided
		// This is a simple check; more complex parsing might be needed by the caller.
		if len(cmd.ExpectedResponsePatterns) > 0 {
			foundExpected := false
			for _, pattern := range cmd.ExpectedResponsePatterns {
				if strings.Contains(rawResponse, pattern) {
					foundExpected = true
					break
				}
			}
			if !foundExpected {
				// If "OK" is present, we might consider it a success even if other specific patterns are missing.
				// However, if specific patterns were given, their absence usually means something is not as expected.
				if !strings.Contains(rawResponse, "OK") { // Only error if "OK" is also missing
					err := fmt.Errorf("command '%s' response did not contain expected patterns (%v) or OK. Response: [%s]", cmd.Cmd, cmd.ExpectedResponsePatterns, rawResponse)
					Error(err.Error())
					if cmd.ErrorChannel != nil {
						cmd.ErrorChannel <- err
					}
					continue
				}
				// If "OK" is present, but other expected patterns are not, log it but proceed.
				// The caller might need to parse the rawResponse to confirm.
				Info("Command '%s' response contained 'OK' but missed some expected patterns (%v). Response: [%s]", cmd.Cmd, cmd.ExpectedResponsePatterns, rawResponse)
			}
		} else {
			// If no specific patterns are expected, "OK" is a good sign of success.
			// ReadFromPort already tries to wait for "OK", "ERROR", or ">".
			// If an error didn't occur and "ERROR" wasn't found, it's likely okay.
			// However, explicitly checking for "OK" is safer for commands that should return it.
			// Some commands (e.g., AT+CMGS for sending SMS) might return ">" first, then "OK" after message body.
			// The current ReadFromPort tries to get the whole response including "OK".
			if !strings.Contains(rawResponse, "OK") && !strings.Contains(rawResponse, ">") {
				// This condition might be too strict for some commands that don't explicitly return "OK"
				// or for unsolicited responses. For now, we assume commands processed here expect an "OK" or similar.
				// Let's be a bit more lenient: if no ERROR and ReadFromPort didn't error out,
				// it might be a valid response for some commands.
				// The primary check for ERROR is above.
				// Info("Command '%s' response did not explicitly contain 'OK', but no error reported. Response: [%s]", cmd.Cmd, rawResponse)
			}
		}

		// If we reach here, the command is considered successful.
		if cmd.ResponseChannel != nil {
			cmd.ResponseChannel <- rawResponse
		}
	}
	Info("AT Command Processor stopped.")
}

// SubmitCommandToQueue creates an ATCommand and sends it to the commandQueue.
// It waits for a response or an error on the respective channels.
func SubmitCommandToQueue(cmdStr string, timeout time.Duration, expectedResponses ...string) (string, error) {
	// Create channels for this specific command
	responseChan := make(chan string, 1) // Buffered to avoid blocking sender if receiver is slow
	errorChan := make(chan error, 1)   // Buffered

	cmd := ATCommand{
		Cmd:                      cmdStr,
		ExpectedResponsePatterns: expectedResponses,
		ResponseChannel:          responseChan,
		ErrorChannel:             errorChan,
		Timeout:                  timeout,
	}

	Info("Submitting command to queue: %s", cmdStr)
	select {
	case commandQueue <- cmd:
		// Command submitted successfully
		Debug("Command %s submitted to queue.", cmdStr)
	case <-time.After(1 * time.Second): // Timeout for submitting to the queue itself
		return "", fmt.Errorf("timeout submitting command '%s' to queue", cmdStr)
	}

	// Wait for the response or an error
	select {
	case response := <-responseChan:
		Info("Received response for %s via channel: %s", cmdStr, response)
		return response, nil
	case err := <-errorChan:
		Error("Received error for %s via channel: %v", cmdStr, err)
		return "", err
	case <-time.After(timeout + 1*time.Second): // Overall timeout, slightly longer than command timeout
		return "", fmt.Errorf("overall timeout waiting for response to command '%s'", cmdStr)
	}
}

// SendCommand is a synchronous function for sending AT commands.
// This can be used if the full queue system isn't running or for simple cases.
// For robustness, using SubmitCommandToQueue with processCommands goroutine is preferred.
func SendCommand(port serial.Port, cmdStr string, timeout time.Duration, expectedResponses ...string) (string, error) {
	if port == nil {
		return "", fmt.Errorf("serial port is not open")
	}

	Info("Sending AT command (synchronous): %s", cmdStr)

	_, err := WriteToPort(port, []byte(cmdStr))
	if err != nil {
		Error("Error writing command '%s' to port: %v", cmdStr, err)
		return "", fmt.Errorf("failed to write command '%s': %w", cmdStr, err)
	}

	rawResponseBytes, err := ReadFromPort(port, timeout)
	rawResponse := string(rawResponseBytes)
	rawResponse = strings.TrimSpace(rawResponse)

	if err != nil {
		Error("Error reading response for command '%s': %v. Raw response: [%s]", cmdStr, err, rawResponse)
		if strings.Contains(err.Error(), "read timeout") {
			return rawResponse, fmt.Errorf("timeout waiting for response to command '%s': %w. Partial response: %q", cmdStr, err, rawResponse)
		}
		return rawResponse, fmt.Errorf("failed to read response for command '%s': %w. Partial response: %q", cmdStr, err, rawResponse)
	}

	Info("Received response for command '%s': [%s]", cmdStr, rawResponse)

	if strings.Contains(rawResponse, "ERROR") {
		Error("Command '%s' returned an ERROR: %s", cmdStr, rawResponse)
		return rawResponse, fmt.Errorf("command '%s' failed with ERROR: %s", cmdStr, rawResponse)
	}
	
	if len(expectedResponses) > 0 {
		foundExpected := false
		for _, pattern := range expectedResponses {
			if strings.Contains(rawResponse, pattern) {
				foundExpected = true
				break
			}
		}
		if !foundExpected {
			if !strings.Contains(rawResponse, "OK") { // Only error if "OK" is also missing
				Error("Command '%s' response did not contain expected patterns (%v) or OK. Response: [%s]", cmdStr, expectedResponses, rawResponse)
				return rawResponse, fmt.Errorf("command '%s' failed: unexpected response. Expected one of %v, got: %s", cmdStr, expectedResponses, rawResponse)
			}
			Info("Command '%s' response contained 'OK' but missed some expected patterns (%v). Response: [%s]", cmdStr, expectedResponses, rawResponse)
		}
	} else {
		// If no specific patterns, but "OK" is expected for most simple commands
		if !strings.Contains(rawResponse, "OK") && !strings.Contains(rawResponse, ">") { 
			// This might be too strict. If ReadFromPort was successful and no ERROR, it might be fine.
			// Info("Command '%s' response did not explicitly contain 'OK' or '>', but no error reported. Response: [%s]", cmdStr, rawResponse)
		}
	}
	return rawResponse, nil
}
