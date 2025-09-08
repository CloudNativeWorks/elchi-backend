package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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

// Init initializes the global logger with the provided configuration
func Init(config Config) error {
	level, err := logrus.ParseLevel(config.Level)
	if err != nil {
		return fmt.Errorf("invalid log level: %v", err)
	}

	logger := logrus.New()
	logger.SetLevel(level)

	// Set formatter based on config
	if config.Format == "json" {
		logger.SetFormatter(&logrus.JSONFormatter{
			CallerPrettyfier: callerPrettyfier,
		})
	} else {
		logger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp:          true,
			CallerPrettyfier:       callerPrettyfier,
			DisableSorting:         true,
			DisableTimestamp:       false,
			DisableLevelTruncation: true,
			ForceColors:            true,
			PadLevelText:           true,
		})
	}

	// Set output based on config
	if config.OutputPath != "stdout" {
		// Ensure the directory exists
		dir := filepath.Dir(config.OutputPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %v", err)
		}

		file, err := os.OpenFile(config.OutputPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return fmt.Errorf("failed to open log file: %v", err)
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

// SetOutput sets the logger output
func SetOutput(output io.Writer) {
	if globalLogger != nil {
		globalLogger.SetOutput(output)
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

// Middleware functions for global access if needed
func Debug(args ...any) {
	if globalLogger != nil {
		globalLogger.Debug(args...)
	}
}

func Debugf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Debugf(format, args...)
	}
}

func Info(args ...any) {
	if globalLogger != nil {
		globalLogger.Info(args...)
	}
}

func Infof(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Infof(format, args...)
	}
}

func Warn(args ...any) {
	if globalLogger != nil {
		globalLogger.Warn(args...)
	}
}

func Warnf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Warnf(format, args...)
	}
}

func Error(args ...any) {
	if globalLogger != nil {
		globalLogger.Error(args...)
	}
}

func Errorf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Errorf(format, args...)
	}
}

func Fatal(args ...any) {
	if globalLogger != nil {
		globalLogger.Fatal(args...)
	}
}

func Fatalf(format string, args ...any) {
	if globalLogger != nil {
		globalLogger.Fatalf(format, args...)
	}
}

func WithFields(fields Fields) *logrus.Entry {
	if globalLogger != nil {
		return globalLogger.WithFields(fields)
	}
	return nil
}

func WithError(err error) *logrus.Entry {
	if globalLogger != nil {
		return globalLogger.WithError(err)
	}
	return nil
}

// SetLevel sets the log level for the logger
func SetLevel(level string) error {
	if globalLogger == nil {
		return fmt.Errorf("logger not initialized")
	}

	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("invalid log level: %v", err)
	}

	globalLogger.SetLevel(logLevel)
	return nil
}

// GetLevel returns the current log level
func GetLevel() string {
	if globalLogger == nil {
		return "unknown"
	}
	return globalLogger.GetLevel().String()
}

// IsLevelEnabled checks if a log level is enabled
func IsLevelEnabled(level string) bool {
	if globalLogger == nil {
		return false
	}

	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		return false
	}

	return globalLogger.IsLevelEnabled(logLevel)
}

// BufferedLogger provides buffered logging capabilities
type BufferedLogger struct {
	*Logger
	buffer    []string
	bufferMux sync.Mutex
	flushSize int
}

// NewBufferedLogger creates a new buffered logger
func NewBufferedLogger(module string, flushSize int) *BufferedLogger {
	return &BufferedLogger{
		Logger:    NewLogger(module),
		buffer:    make([]string, 0, flushSize),
		flushSize: flushSize,
	}
}

// BufferInfo buffers an info message
func (bl *BufferedLogger) BufferInfo(format string, args ...interface{}) {
	bl.bufferMux.Lock()
	defer bl.bufferMux.Unlock()

	msg := fmt.Sprintf(format, args...)
	bl.buffer = append(bl.buffer, msg)

	if len(bl.buffer) >= bl.flushSize {
		bl.Flush()
	}
}

// Flush writes all buffered messages
func (bl *BufferedLogger) Flush() {
	bl.bufferMux.Lock()
	defer bl.bufferMux.Unlock()

	for _, msg := range bl.buffer {
		bl.Info(msg)
	}
	bl.buffer = bl.buffer[:0]
}
