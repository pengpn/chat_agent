// Package logger provides the application's context-aware Zap logging API.
package logger

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type requestIDContextKey struct{}

var (
	defaultLogger = zap.NewNop()
	loggerMu      sync.RWMutex
)

const dailyLogPrefix = "agent-platform-"

// FileConfig controls the date-rotated JSON log files in addition to stdout.
type FileConfig struct {
	Directory     string
	RetentionDays int
}

// Field is a Zap field. Re-exporting it keeps callers independent from the
// logger setup while retaining Zap's efficient structured field encoding.
type Field = zap.Field

// Init configures the process-wide JSON logger. Every entry is written to the
// supplied output (normally stdout) and to one date-rotated file.
func Init(level string, output io.Writer, fileConfig FileConfig) error {
	fileOutput, err := NewDailyFileWriter(fileConfig.Directory, fileConfig.RetentionDays)
	if err != nil {
		return err
	}
	logger, err := newLoggerWithFile(level, output, fileOutput)
	if err != nil {
		_ = fileOutput.Close()
		return err
	}
	setDefault(logger)
	return nil
}

func newLogger(level string, output io.Writer) (*zap.Logger, error) {
	return newLoggerWithFile(level, output, nil)
}

func newLoggerWithFile(level string, output io.Writer, fileOutput *DailyFileWriter) (*zap.Logger, error) {
	var parsed zapcore.Level
	if err := parsed.Set(strings.ToLower(strings.TrimSpace(level))); err != nil {
		return nil, err
	}
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	cores := []zapcore.Core{zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(output), parsed)}
	if fileOutput != nil {
		cores = append(cores, zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(fileOutput), parsed))
	}
	core := zapcore.NewTee(cores...)
	return zap.New(core, zap.AddStacktrace(zapcore.ErrorLevel)), nil
}

// DailyFileWriter appends JSON logs to one local-date file at a time. It
// changes files on the first log entry after midnight, so rotation does not
// require a background goroutine.
type DailyFileWriter struct {
	directory     string
	retentionDays int
	now           func() time.Time

	mu          sync.Mutex
	date        string
	currentFile *os.File
}

func NewDailyFileWriter(directory string, retentionDays int) (*DailyFileWriter, error) {
	return newDailyFileWriter(directory, retentionDays, time.Now)
}

func newDailyFileWriter(directory string, retentionDays int, now func() time.Time) (*DailyFileWriter, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("log directory is required")
	}
	if retentionDays <= 0 {
		return nil, fmt.Errorf("log retention days must be positive")
	}
	writer := &DailyFileWriter{directory: directory, retentionDays: retentionDays, now: now}
	writer.mu.Lock()
	err := writer.rotateLocked(now())
	writer.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *DailyFileWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.rotateLocked(w.now()); err != nil {
		return 0, err
	}
	return w.currentFile.Write(data)
}

func (w *DailyFileWriter) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.currentFile == nil {
		return nil
	}
	return w.currentFile.Sync()
}

func (w *DailyFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.currentFile == nil {
		return nil
	}
	err := w.currentFile.Close()
	w.currentFile = nil
	return err
}

func (w *DailyFileWriter) rotateLocked(now time.Time) error {
	date := now.Local().Format("2006-01-02")
	if w.currentFile != nil && w.date == date {
		return nil
	}
	if err := os.MkdirAll(w.directory, 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	if w.currentFile != nil {
		if err := w.currentFile.Close(); err != nil {
			return fmt.Errorf("close previous log file: %w", err)
		}
	}
	file, err := os.OpenFile(filepath.Join(w.directory, dailyLogPrefix+date+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open daily log file: %w", err)
	}
	w.currentFile = file
	w.date = date
	if err := w.cleanupLocked(now); err != nil {
		return err
	}
	return nil
}

func (w *DailyFileWriter) cleanupLocked(now time.Time) error {
	entries, err := os.ReadDir(w.directory)
	if err != nil {
		return fmt.Errorf("read log directory: %w", err)
	}
	localNow := now.Local()
	cutoff := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, localNow.Location()).AddDate(0, 0, -w.retentionDays)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), dailyLogPrefix) || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		dateText := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), dailyLogPrefix), ".log")
		date, err := time.ParseInLocation("2006-01-02", dateText, now.Location())
		if err != nil || !date.Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(w.directory, entry.Name())); err != nil {
			return fmt.Errorf("remove expired log file %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func setDefault(value *zap.Logger) {
	if value == nil {
		value = zap.NewNop()
	}
	loggerMu.Lock()
	defer loggerMu.Unlock()
	defaultLogger = value
}

// Sync flushes buffered log entries. Errors are intentionally ignored because
// stdout/stderr commonly return an unsupported-sync error on Unix systems.
func Sync() {
	_ = defaultZap().Sync()
}

func defaultZap() *zap.Logger {
	loggerMu.RLock()
	defer loggerMu.RUnlock()
	return defaultLogger
}

// WithRequestID attaches the request identifier to a context.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}

func zapFor(ctx context.Context) *zap.Logger {
	if value := requestID(ctx); value != "" {
		return defaultZap().With(zap.String("request_id", value))
	}
	return defaultZap()
}

// Debug, Info, Warn, and Error are the standard application logging methods.
// They automatically attach request_id when the supplied context has one.
func Debug(ctx context.Context, message string, fields ...Field) {
	zapFor(ctx).Debug(message, fields...)
}
func Info(ctx context.Context, message string, fields ...Field) { zapFor(ctx).Info(message, fields...) }
func Warn(ctx context.Context, message string, fields ...Field) { zapFor(ctx).Warn(message, fields...) }
func Error(ctx context.Context, message string, fields ...Field) {
	zapFor(ctx).Error(message, fields...)
}

// Field helpers keep normal logging calls concise.
func String(key, value string) Field           { return zap.String(key, value) }
func Int(key string, value int) Field          { return zap.Int(key, value) }
func Int64(key string, value int64) Field      { return zap.Int64(key, value) }
func Bool(key string, value bool) Field        { return zap.Bool(key, value) }
func Err(err error) Field                      { return zap.Error(err) }
func Strings(key string, value []string) Field { return zap.Strings(key, value) }
func Any(key string, value any) Field          { return zap.Any(key, value) }
