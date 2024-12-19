package logger

import (
	"fmt"
	"log"
	"os"
	"time"
)

const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorPurple = "\033[35m"
	colorCyan   = "\033[36m"
)

type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARNING
	ERROR
	NOT_FOUND
)

var levelIcons = map[LogLevel]string{
	DEBUG:     "D", // Green Circle
	INFO:      "i", // Information Symbol
	WARNING:   "w", // Triangle Warning without the heavy exclamation mark
	ERROR:     "E", // Simple Exclamation Mark
	NOT_FOUND: "N", // Circle
}

var levelColors = map[LogLevel]string{
	DEBUG:     colorBlue,
	INFO:      colorGreen,
	WARNING:   colorYellow,
	ERROR:     colorRed,
	NOT_FOUND: colorPurple,
}

var levelText = map[LogLevel]string{
	DEBUG:     "DEBUG",
	INFO:      "INFO",
	WARNING:   "WARNING",
	ERROR:     "ERROR",
	NOT_FOUND: "NOT_FOUND",
}

type Logger struct {
	logger *log.Logger
}

func New() *Logger {
	logger := &Logger{
		logger: log.New(os.Stdout, "", 0),
	}
	// Configure logger to exclude sensitive information
	return logger
}

func (l *Logger) Log(level LogLevel, component, method, message string) {
	timestamp := time.Now().Format("06-01-02 15:04:05")
	icon := levelIcons[level]
	color := levelColors[level]
	levelStr := levelText[level]

	logMessage := fmt.Sprintf("%s | %s %s | %s.%s - %s",
		timestamp,
		icon,
		color+levelStr+colorReset,
		component,
		method,
		message,
	)

	l.logger.Println(logMessage)
}

func (l *Logger) Debug(component, method, message string) {
	l.Log(DEBUG, component, method, message)
}

func (l *Logger) Info(component, method, message string) {
	l.Log(INFO, component, method, message)
}

func (l *Logger) Warning(component, method, message string) {
	l.Log(WARNING, component, method, message)
}

func (l *Logger) Error(component, method, message string) {
	l.Log(ERROR, component, method, message)
}

func (l *Logger) NotFound(component, method, message string) {
	l.Log(NOT_FOUND, component, method, message)
}
