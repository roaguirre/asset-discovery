package telemetry

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
)

type contextKey string

const providerContextKey contextKey = "telemetry.provider"

type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelError Level = "error"
)

type Attr struct {
	Key   string
	Value interface{}
}

type Span interface {
	End(attrs ...Attr)
}

type Provider interface {
	Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span)
	Log(ctx context.Context, level Level, message string, attrs ...Attr)
}

type noopProvider struct{}
type noopSpan struct{}

func Noop() Provider {
	return noopProvider{}
}

func OrNoop(provider Provider) Provider {
	if provider == nil {
		return Noop()
	}
	return provider
}

func WithProvider(ctx context.Context, provider Provider) context.Context {
	return context.WithValue(ctx, providerContextKey, OrNoop(provider))
}

func ProviderFromContext(ctx context.Context) Provider {
	if provider, ok := ctx.Value(providerContextKey).(Provider); ok && provider != nil {
		return provider
	}
	return Noop()
}

func Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	return ProviderFromContext(ctx).Start(ctx, name, attrs...)
}

func Log(ctx context.Context, level Level, message string, attrs ...Attr) {
	ProviderFromContext(ctx).Log(ctx, level, message, attrs...)
}

func Info(ctx context.Context, message string, attrs ...Attr) {
	Log(ctx, LevelInfo, message, attrs...)
}

func Debug(ctx context.Context, message string, attrs ...Attr) {
	Log(ctx, LevelDebug, message, attrs...)
}

func Error(ctx context.Context, message string, attrs ...Attr) {
	Log(ctx, LevelError, message, attrs...)
}

func Infof(ctx context.Context, format string, args ...interface{}) {
	Info(ctx, fmt.Sprintf(format, args...))
}

func Debugf(ctx context.Context, format string, args ...interface{}) {
	Debug(ctx, fmt.Sprintf(format, args...))
}

func Errorf(ctx context.Context, format string, args ...interface{}) {
	Error(ctx, fmt.Sprintf(format, args...))
}

func String(key, value string) Attr {
	return Attr{Key: key, Value: value}
}

func Int(key string, value int) Attr {
	return Attr{Key: key, Value: value}
}

func Bool(key string, value bool) Attr {
	return Attr{Key: key, Value: value}
}

func Err(err error) Attr {
	if err == nil {
		return Attr{}
	}
	return Attr{Key: "error", Value: err.Error()}
}

func (noopProvider) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (noopProvider) Log(ctx context.Context, level Level, message string, attrs ...Attr) {}

func (noopSpan) End(attrs ...Attr) {}

type stdlibProvider struct {
	logger *log.Logger
}

func NewStdlibProvider(logger *log.Logger) Provider {
	if logger == nil {
		logger = log.Default()
	}
	return &stdlibProvider{logger: logger}
}

func (p *stdlibProvider) Start(ctx context.Context, name string, attrs ...Attr) (context.Context, Span) {
	return ctx, noopSpan{}
}

func (p *stdlibProvider) Log(ctx context.Context, level Level, message string, attrs ...Attr) {
	if strings.TrimSpace(message) == "" {
		return
	}

	line := strings.ToUpper(string(level)) + " " + message
	if suffix := formatAttrs(attrs); suffix != "" {
		line += " " + suffix
	}
	p.logger.Print(line)
}

func formatAttrs(attrs []Attr) string {
	if len(attrs) == 0 {
		return ""
	}

	values := make([]string, 0, len(attrs))
	for _, attr := range attrs {
		key := strings.TrimSpace(attr.Key)
		if key == "" {
			continue
		}
		values = append(values, fmt.Sprintf("%s=%v", key, attr.Value))
	}
	if len(values) == 0 {
		return ""
	}
	sort.Strings(values)
	return strings.Join(values, " ")
}
