// Package obs wires structured logging (slog) and OpenTelemetry traces/metrics
// for a single binary.
//
// Logs always go to stdout/file via slog. Traces and metrics are exported via
// OTLP only when an endpoint is configured (OTEL_* env vars); otherwise the
// footprint is effectively zero. Even with no backend a TracerProvider is still
// installed (with a no-op sampler) so trace IDs are minted and log lines stay
// correlatable across binaries.
package obs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// Options configures observability for a binary.
type Options struct {
	ServiceName string
	Version     string
	LogLevel    string // debug|info|warn|error
	LogFormat   string // text|json
	LogOutput   string // stdout|file|both
	LogFile     string // path used when LogOutput is file|both
}

// Init installs the global slog logger and OpenTelemetry providers. It returns a
// shutdown func that flushes exporters; call it on process exit.
func Init(ctx context.Context, opt Options) (func(context.Context) error, error) {
	logger, err := newLogger(opt)
	if err != nil {
		return nil, err
	}
	slog.SetDefault(logger)

	// Default the exporter selection: export only when an OTLP endpoint is set.
	// This overrides the OTel spec default of "otlp", so a plain self-host run
	// produces zero telemetry egress and no connection-refused noise.
	if hasOTLPEndpoint() {
		setDefaultEnv("OTEL_TRACES_EXPORTER", "otlp")
		setDefaultEnv("OTEL_METRICS_EXPORTER", "otlp")
	} else {
		setDefaultEnv("OTEL_TRACES_EXPORTER", "none")
		setDefaultEnv("OTEL_METRICS_EXPORTER", "none")
	}
	setDefaultEnv("OTEL_LOGS_EXPORTER", "none") // logs travel via slog -> stdout

	res := resource.NewSchemaless(
		attribute.String("service.name", opt.ServiceName),
		attribute.String("service.version", cmp.Or(opt.Version, "dev")),
	)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	tracesOn := !isNone(os.Getenv("OTEL_TRACES_EXPORTER"))
	metricsOn := !isNone(os.Getenv("OTEL_METRICS_EXPORTER"))

	// Traces: always install a provider so trace IDs are minted (log
	// correlation). Record/export only when enabled.
	spanExp, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return nil, fmt.Errorf("obs: span exporter: %w", err)
	}
	sampler := sdktrace.NeverSample()
	if tracesOn {
		sampler = sdktrace.ParentBased(sdktrace.AlwaysSample())
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(spanExp),
	)
	otel.SetTracerProvider(tp)

	// Metrics: install a provider only when enabled.
	var mp *sdkmetric.MeterProvider
	if metricsOn {
		reader, err := autoexport.NewMetricReader(ctx)
		if err != nil {
			return nil, fmt.Errorf("obs: metric reader: %w", err)
		}
		mp = sdkmetric.NewMeterProvider(
			sdkmetric.WithResource(res),
			sdkmetric.WithReader(reader),
		)
		otel.SetMeterProvider(mp)
	}

	slog.InfoContext(ctx, "observability initialized",
		"service", opt.ServiceName,
		"traces", tracesOn,
		"metrics", metricsOn,
	)

	return func(ctx context.Context) error {
		errs := []error{tp.Shutdown(ctx)}
		if mp != nil {
			errs = append(errs, mp.Shutdown(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

// --- logging ---

func newLogger(opt Options) (*slog.Logger, error) {
	w, err := logWriter(opt)
	if err != nil {
		return nil, err
	}
	h := &slog.HandlerOptions{Level: parseLevel(opt.LogLevel)}
	var base slog.Handler
	if strings.EqualFold(opt.LogFormat, "json") {
		base = slog.NewJSONHandler(w, h)
	} else {
		base = slog.NewTextHandler(w, h)
	}
	return slog.New(&traceHandler{Handler: base}), nil
}

func logWriter(opt Options) (io.Writer, error) {
	switch strings.ToLower(opt.LogOutput) {
	case "file":
		return openLogFile(opt.LogFile)
	case "both":
		f, err := openLogFile(opt.LogFile)
		if err != nil {
			return nil, err
		}
		return io.MultiWriter(os.Stdout, f), nil
	default: // "", "stdout"
		return os.Stdout, nil
	}
}

func openLogFile(path string) (*os.File, error) {
	if path == "" {
		path = "skali.log"
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("obs: open log file %q: %w", path, err)
	}
	return f, nil
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// traceHandler decorates every record with trace_id/span_id from the context so
// logs and traces cross-reference. WithAttrs/WithGroup must re-wrap: embedding
// alone would call the inner handler's methods and silently drop the decoration.
type traceHandler struct{ slog.Handler }

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithAttrs(as)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return &traceHandler{Handler: h.Handler.WithGroup(name)}
}

// --- env helpers ---

func hasOTLPEndpoint() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != ""
}

func setDefaultEnv(key, val string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, val)
	}
}

func isNone(v string) bool {
	return v == "" || strings.EqualFold(v, "none")
}
