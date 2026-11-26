// tracing.go: the runs as OpenTelemetry traces, when an endpoint is set
//
// Copyright (c) 2026 AGILira - A. Giordano
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agilira/go-errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.38.0"
	"go.opentelemetry.io/otel/trace"

	agent "github.com/agilira/ringil-agent-sdk"
	"github.com/agilira/ringil-agent-sdk/eventlog"
	ringilotel "github.com/agilira/ringil-agent-sdk/otel"
)

// tracing is the process's tracer provider, made once, the first time a
// run needs it; nil inside when no endpoint is set.
type tracing struct {
	once sync.Once
	tp   *sdktrace.TracerProvider
	err  error
}

// traceEndpoint is where traces go, by the standard OpenTelemetry
// variables of the environment: OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is the
// whole address, OTEL_EXPORTER_OTLP_ENDPOINT a base that /v1/traces is
// added to. "" means no tracing.
func (c *cli) traceEndpoint() (endpoint, headers string) {
	if v := strings.TrimSpace(c.getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); v != "" {
		endpoint = v
	} else if v := strings.TrimSpace(c.getenv("OTEL_EXPORTER_OTLP_ENDPOINT")); v != "" {
		endpoint = strings.TrimRight(v, "/") + "/v1/traces"
	}
	for _, name := range []string{"OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_HEADERS"} {
		if v := c.getenv(name); v != "" {
			return endpoint, v
		}
	}
	return endpoint, ""
}

// tracer is the provider, made on first use.
func (c *cli) tracer(ctx context.Context) (*sdktrace.TracerProvider, error) {
	c.traces.once.Do(func() {
		endpoint, headers := c.traceEndpoint()
		if endpoint == "" {
			return
		}
		c.traces.tp, c.traces.err = newTracerProvider(ctx, endpoint, headers, c.getenv("OTEL_SERVICE_NAME"))
	})
	return c.traces.tp, c.traces.err
}

func newTracerProvider(ctx context.Context, endpoint, headers, service string) (*sdktrace.TracerProvider, error) {
	traces, err := tracesURL(endpoint)
	if err != nil {
		return nil, err
	}
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(traces)}
	if h := parseHeaders(headers); len(h) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(h))
	}
	exp, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, ErrUsage, "the trace exporter could not start")
	}
	if service == "" {
		service = "ringil"
	}
	res := resource.NewWithAttributes(semconv.SchemaURL, semconv.ServiceName(service), semconv.ServiceVersion(version))
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res)), nil
}

// tracesURL checks where to post the traces: an http(s) address.
func tracesURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New(ErrUsage, "OTEL_EXPORTER_OTLP_ENDPOINT must be an http(s) address, such as https://otel.example.com:4318")
	}
	return u.String(), nil
}

// parseHeaders reads "key=value,key2=value2", values URL-encoded, as the
// OpenTelemetry variables write them.
func parseHeaders(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(pair, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		if dec, err := url.QueryUnescape(strings.TrimSpace(v)); err == nil {
			v = dec
		}
		out[k] = v
	}
	return out
}

// observers are the run's observers: the CLI's, and the traces when an
// endpoint is set. A tracer that cannot start is said once; the run goes
// on without traces.
func (c *cli) observers(ctx context.Context) []agent.Observer {
	out := append([]agent.Observer(nil), c.events...)
	tp, err := c.tracer(ctx)
	switch {
	case err != nil && c.traceWarned.CompareAndSwap(false, true):
		_ = c.line("  * traces: " + describe(err))
	case tp != nil:
		name := c.roleName
		if name == "" {
			name = "ringil"
		}
		out = append(out, ringilotel.Observer(tp.Tracer("ringil"), ringilotel.Options{Agent: name}))
	}
	return out
}

// flushTraces sends the traces still queued, at exit: at most a few
// seconds, so an endpoint that does not answer does not hold the exit.
func (c *cli) flushTraces() {
	if c.traces == nil || c.traces.tp == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.traces.tp.Shutdown(ctx)
}

// Config outcomes on the ringil.config span.
const (
	configLoaded  = "loaded"
	configApplied = "applied"
	configRefused = "refused"
)

// traceConfig records a revision of ringil.yaml: loaded at the start,
// applied, or refused. Its digest, the one before, the keys that changed
// and what needs serve restarted; never a value (the file names secrets,
// headers and addresses), nor a refusal's message (it may quote them):
// its code.
func (c *cli) traceConfig(outcome string, previous, next *config, restart string, err error) {
	tp, terr := c.tracer(context.Background())
	if tp == nil || terr != nil {
		return
	}
	attrs := []attribute.KeyValue{attribute.String("ringil.config.outcome", outcome)}
	if previous != nil {
		attrs = append(attrs, attribute.String("ringil.config.previous", previous.shortDigest()))
	}
	if next != nil {
		attrs = append(attrs, attribute.String("ringil.config.digest", next.shortDigest()))
		if previous != nil {
			attrs = append(attrs, attribute.StringSlice("ringil.config.changed", changedKeys(previous, next)))
		}
	}
	if restart != "" {
		attrs = append(attrs, attribute.StringSlice("ringil.config.restart", strings.Split(restart, ", ")))
	}
	_, span := tp.Tracer("ringil").Start(context.Background(), "ringil.config "+outcome, trace.WithAttributes(attrs...))
	if err != nil {
		code := eventlog.Code(err)
		span.SetAttributes(attribute.String("error.type", code))
		span.SetStatus(codes.Error, code)
	}
	span.End()
}

// changedKeys are ringil.yaml's top-level keys whose value differs, by
// the file's own fields (a key added to the file is counted with no list
// to keep), and "instructions" when an instructions file's text changed.
func changedKeys(a, b *config) []string {
	var out []string
	va, vb, t := reflect.ValueOf(*a), reflect.ValueOf(*b), reflect.TypeOf(*a)
	for i := range t.NumField() {
		key := t.Field(i).Tag.Get("argus")
		if key == "" || !t.Field(i).IsExported() {
			continue
		}
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			out = append(out, key)
		}
	}
	if !maps.Equal(a.texts, b.texts) && !slices.Contains(out, "instructions") {
		out = append(out, "instructions")
	}
	return out
}
