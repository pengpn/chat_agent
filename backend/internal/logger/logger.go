// Package logger provides the application's context-aware Zap logging API.
package logger

import (
	"context"
	"io"
	"strings"
	"sync"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type requestIDContextKey struct{}

var (
	defaultLogger = zap.NewNop()
	loggerMu      sync.RWMutex
)

// Field is a Zap field. Re-exporting it keeps callers independent from the
// logger setup while retaining Zap's efficient structured field encoding.
type Field = zap.Field

// Init configures the process-wide JSON logger. It must be called once during
// application startup before emitting runtime logs.
func Init(level string, output io.Writer) error {
	logger, err := newLogger(level, output)
	if err != nil {
		return err
	}
	setDefault(logger)
	return nil
}

func newLogger(level string, output io.Writer) (*zap.Logger, error) {
	var parsed zapcore.Level
	if err := parsed.Set(strings.ToLower(strings.TrimSpace(level))); err != nil {
		return nil, err
	}
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encoderConfig), zapcore.AddSync(output), parsed)
	return zap.New(core, zap.AddStacktrace(zapcore.ErrorLevel)), nil
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
