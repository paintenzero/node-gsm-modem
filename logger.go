package main

import (
	"log"
	"os"
)

// LogLevel type for defining log levels
type LogLevel int

const (
	// DEBUG level
	DEBUG LogLevel = iota
	// INFO level
	INFO
	// ERROR level
	ERROR
)

var (
	currentLogLevel = INFO // Default log level
	logger          = log.New(os.Stdout, "", log.Ldate|log.Ltime|log.Lshortfile)
)

// SetLogLevel sets the current log level
func SetLogLevel(level LogLevel) {
	currentLogLevel = level
}

// Debug logs a message at DEBUG level
func Debug(format string, v ...interface{}) {
	if currentLogLevel <= DEBUG {
		logger.Printf("[DEBUG] "+format, v...)
	}
}

// Info logs a message at INFO level
func Info(format string, v ...interface{}) {
	if currentLogLevel <= INFO {
		logger.Printf("[INFO] "+format, v...)
	}
}

// Error logs a message at ERROR level
func Error(format string, v ...interface{}) {
	if currentLogLevel <= ERROR {
		logger.Printf("[ERROR] "+format, v...)
	}
}
