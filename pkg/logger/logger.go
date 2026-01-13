package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sirupsen/logrus"
)

// Fields type is an alias for logrus.Fields
type Fields = logrus.Fields

// Logger is a wrapper around logrus.Logger
type Logger struct {
	*logrus.Logger
	module string
}

// Global logger instance
var globalLogger *Logger

// Configuration for the logger
type Config struct {
	Level      string `mapstructure:"level"`
	Format     string `mapstructure:"format"`
	OutputPath string `mapstructure:"output_path"`
	Module     string `mapstructure:"module"`
}

// CustomTextFormatter wraps logrus.TextFormatter to customize module field placement
type CustomTextFormatter struct {
	*logrus.TextFormatter
}

// Format renders a single log entry with module field placed after timestamp
func (f *CustomTextFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	// Extract module field from entry data
	var moduleStr string
	if module, hasModule := entry.Data["module"]; hasModule {
		moduleStr = fmt.Sprint(module)
		// Create a copy of Data without module to avoid duplication
		newData := make(logrus.Fields)
		for k, v := range entry.Data {
			if k != "module" {
				newData[k] = v
			}
		}
		entry.Data = newData
	}

	// Get base formatted output from TextFormatter
	formatted, err := f.TextFormatter.Format(entry)
	if err != nil {
		return nil, err
	}

	// If no module, return as-is
	if moduleStr == "" {
		return formatted, nil
	}

	// Parse the formatted log line and insert [module] after timestamp
	// Expected format: "LEVEL  [timestamp]filename:line message fields..."
	// We want: "LEVEL  [timestamp][module]filename:line message fields..."

	line := string(formatted)

	// Find the closing bracket of timestamp
	timestampEnd := strings.Index(line, "]")
	if timestampEnd == -1 {
		// If no timestamp found, return original
		return formatted, nil
	}

	// Insert [module] right after timestamp with spacing
	var result strings.Builder
	result.WriteString(line[:timestampEnd+1])      // "LEVEL  [timestamp]"
	result.WriteString(" [")                       // Space before module
	result.WriteString(moduleStr)
	result.WriteString("] ")                       // Space after module
	result.WriteString(line[timestampEnd+1:])      // "filename:line message fields..."

	return []byte(result.String()), nil
}

// Init initializes the global logger with the provided configuration
func Init(config Config) error {
	level, err := logrus.ParseLevel(config.Level)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}

	logger := logrus.New()
	logger.SetLevel(level)

	// Set formatter based on config
	if config.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			CallerPrettyfier: callerPrettyfier,
		})
	} else {
		logger.SetFormatter(&CustomTextFormatter{
			TextFormatter: &logrus.TextFormatter{
				FullTimestamp:          true,
				CallerPrettyfier:       callerPrettyfier,
				DisableSorting:         false, // Enable sorting for consistent field order
				DisableTimestamp:       false,
				DisableLevelTruncation: true,
				ForceColors:            true,  // Force colors for all outputs
				DisableColors:          false, // Keep colors enabled
				PadLevelText:           true,  // Keep level padding for consistency
				SortingFunc: func(keys []string) {
					// Define custom field order for HTTP logs
					order := map[string]int{
						"statusCode":     1,
						"responseTime":   2,
						"clientIP":       3,
						"requestMethod":  4,
						"requestPath":    5,
						"requestUri":     6,
						"username":       7,
						"userAgent":      8,
						"requestReferer": 9,
					}

					// Sort based on predefined order, then alphabetically for unknown fields
					sort.Slice(keys, func(i, j int) bool {
						orderI, hasI := order[keys[i]]
						orderJ, hasJ := order[keys[j]]

						if hasI && hasJ {
							return orderI < orderJ
						}
						if hasI {
							return true
						}
						if hasJ {
							return false
						}
						return keys[i] < keys[j]
					})
				},
			},
		})
	}

	// Set output based on config
	if config.OutputPath != "stdout" {
		// Ensure the directory exists with secure permissions
		// 0750 = rwxr-x--- (owner: rwx, group: r-x, others: no access)
		dir := filepath.Dir(config.OutputPath)
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}

		// 0600 = rw------- (owner: rw, group: no access, others: no access)
		// Only the owner can read/write log files for security
		file, err := os.OpenFile(config.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		logger.SetOutput(file)
	}

	// Enable caller info
	logger.SetReportCaller(true)

	globalLogger = &Logger{
		Logger: logger,
		module: config.Module,
	}

	return nil
}

// NewLogger creates a new logger instance with the specified module
func NewLogger(module string) *Logger {
	if globalLogger == nil {
		panic("logger not initialized. Call logger.Init() first")
	}

	return &Logger{
		Logger: globalLogger.Logger,
		module: module,
	}
}

// callerPrettyfier is used to format the caller information
func callerPrettyfier(f *runtime.Frame) (string, string) {
	// Walk up the stack until we find the actual caller
	pcs := make([]uintptr, 15)
	n := runtime.Callers(4, pcs) // Start from 4 to skip more internal frames
	if n == 0 {
		return "", fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
	}

	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		// Skip logrus and our logger package frames
		if !strings.Contains(frame.File, "pkg/logger") &&
			!strings.Contains(frame.File, "sirupsen/logrus") {
			return "", fmt.Sprintf("%s:%d", filepath.Base(frame.File), frame.Line)
		}
		if !more {
			break
		}
	}

	// Fallback to original frame if we couldn't find a better one
	return "", fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
}

// withModule adds the module field to the entry
func (l *Logger) withModule(fields Fields) *logrus.Entry {
	if fields == nil {
		fields = Fields{}
	}
	fields["module"] = l.module
	return l.WithFields(fields)
}

// Debug logs a message at the debug level
func (l *Logger) Debug(args ...any) {
	l.withModule(nil).Debug(args...)
}

// Debugf logs a formatted message at the debug level
func (l *Logger) Debugf(format string, args ...any) {
	l.withModule(nil).Debugf(format, args...)
}

// Info logs a message at the info level
func (l *Logger) Info(args ...any) {
	l.withModule(nil).Info(args...)
}

// Infof logs a formatted message at the info level
func (l *Logger) Infof(format string, args ...any) {
	l.withModule(nil).Infof(format, args...)
}

// Warn logs a message at the warn level
func (l *Logger) Warn(args ...any) {
	l.withModule(nil).Warn(args...)
}

// Warnf logs a formatted message at the warn level
func (l *Logger) Warnf(format string, args ...any) {
	l.withModule(nil).Warnf(format, args...)
}

// Error logs a message at the error level
func (l *Logger) Error(args ...any) {
	l.withModule(nil).Error(args...)
}

// Errorf logs a formatted message at the error level
func (l *Logger) Errorf(format string, args ...any) {
	l.withModule(nil).Errorf(format, args...)
}

// Fatal logs a message at the fatal level and then exits
func (l *Logger) Fatal(args ...any) {
	l.withModule(nil).Fatal(args...)
}

// Fatalf logs a formatted message at the fatal level and then exits
func (l *Logger) Fatalf(format string, args ...any) {
	l.withModule(nil).Fatalf(format, args...)
}

// WithFields adds fields to the logger
func (l *Logger) WithFields(fields Fields) *logrus.Entry {
	if l.module != "" {
		if fields == nil {
			fields = Fields{}
		}
		fields["module"] = l.module
	}
	return l.Logger.WithFields(fields)
}

// WithError adds an error to the logger
func (l *Logger) WithError(err error) *logrus.Entry {
	return l.WithFields(Fields{"error": err})
}

func Errorf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Errorf(format, args...)
	}
}

func Fatalf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Fatalf(format, args...)
	}
}
