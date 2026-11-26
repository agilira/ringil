// tracing_test.go: the runs' traces
//
// Copyright (c) 2026 AGILira - A. Giordano
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agilira/go-errors"

	"github.com/agilira/ringil-agent-sdk/provider/fake"
)

// collector is an OTLP endpoint that keeps what it is sent.
func collector(t *testing.T) (*httptest.Server, func() [][]byte, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var bodies [][]byte
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		if r.URL.Path == "/v1/traces" {
			bodies, headers = append(bodies, b), append(headers, r.Header.Clone())
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() [][]byte { mu.Lock(); defer mu.Unlock(); return bodies }, func() []http.Header { mu.Lock(); defer mu.Unlock(); return headers }
}

// With an endpoint set, a run's spans reach it at exit, with the headers
// asked, and no word of the conversation.
func TestTracesReachTheEndpoint(t *testing.T) {
	srv, bodies, headers := collector(t)
	h := newHarness(t, "", fake.Reply("the secret answer"))
	env(h, map[string]string{"RINGIL_PROVIDER": "openai", "RINGIL_MODEL": "m", "OPENAI_API_KEY": "k",
		"OTEL_EXPORTER_OTLP_ENDPOINT": srv.URL, "OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer%20tok,x-team=a"})
	if code := h.run("run", "the secret question"); code != exitOK {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	got := bodies()
	if len(got) == 0 {
		t.Fatal("no traces sent")
	}
	all := bytes.Join(got, nil)
	if !bytes.Contains(all, []byte("invoke_agent")) || !bytes.Contains(all, []byte("chat fake")) || !bytes.Contains(all, []byte("ringil")) {
		t.Fatalf("spans missing from %q", all)
	}
	if bytes.Contains(all, []byte("secret")) {
		t.Fatal("conversation text in the traces")
	}
	if hd := headers()[0]; hd.Get("Authorization") != "Bearer tok" || hd.Get("X-Team") != "a" {
		t.Fatalf("headers %v", hd)
	}
}

// Without an endpoint nothing is sent and nothing is said.
func TestNoEndpointNoTraces(t *testing.T) {
	h := newHarness(t, "", fake.Reply("ok"))
	if code := h.run("run", "x"); code != exitOK || strings.Contains(h.err.String(), "traces") {
		t.Fatalf("exit %d %q", code, h.err)
	}
}

// A bad endpoint is said once, and the run goes on.
func TestBadEndpointIsSaid(t *testing.T) {
	h := newHarness(t, "", fake.Reply("ok"))
	env(h, map[string]string{"RINGIL_PROVIDER": "openai", "RINGIL_MODEL": "m", "OPENAI_API_KEY": "k", "OTEL_EXPORTER_OTLP_ENDPOINT": "ftp://x"})
	if code := h.run("run", "x"); code != exitOK || strings.Count(h.err.String(), "* traces:") != 1 {
		t.Fatalf("exit %d %q", code, h.err)
	}
}

func TestParseHeaders(t *testing.T) {
	got := parseHeaders(" a = 1 ,b=x%3Dy,,=skip,c")
	if len(got) != 2 || got["a"] != "1" || got["b"] != "x=y" {
		t.Fatalf("%v", got)
	}
}

// The base address gets /v1/traces, path and all; the traces' own address
// is used as it is.
func TestTraceEndpointVariables(t *testing.T) {
	for vars, want := range map[[2]string]string{
		{"https://otel.example.com:4318", ""}:                        "https://otel.example.com:4318/v1/traces",
		{"https://gw.example.com/otlp/", ""}:                         "https://gw.example.com/otlp/v1/traces",
		{"https://base.example.com", "https://t.example.com/ingest"}: "https://t.example.com/ingest",
		{"", ""}: "",
	} {
		h := newHarness(t, "")
		env(h, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": vars[0], "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": vars[1]})
		if got, _ := h.c.traceEndpoint(); got != want {
			t.Errorf("%v: %s, want %s", vars, got, want)
		}
	}
}

// A revision reaches the traces as its digests, the keys that changed and
// what needs a restart; a refusal as its code. No value of the file, no
// message: they name secrets, headers and addresses.
func TestConfigRevisionsTraced(t *testing.T) {
	srv, bodies, _ := collector(t)
	h := newHarness(t, "")
	env(h, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": srv.URL})
	before := &config{Models: []string{"openai:m"}, digest: "aaaaaaaaaaaaaaaa", Providers: map[string]providerEntry{"gw": {Base: "https://gw.example.com/v1", KeyEnv: "GW_KEY"}}}
	after := &config{Models: []string{"openai:m", "secret-provider:secret-model"}, digest: "bbbbbbbbbbbbbbbb", MaxCost: 2,
		Providers: map[string]providerEntry{"gw": {Base: "https://secret.example.com/v1", KeyEnv: "GW_KEY"}}}
	h.c.traceConfig(configApplied, before, after, "allow_http, provider and providers for the chats' voice", nil)
	h.c.traceConfig(configRefused, after, nil, "", errors.New(ErrUsage, "models secret-provider:x: no provider secret-provider"))
	h.c.flushTraces()
	all := bytes.Join(bodies(), nil)
	for _, want := range []string{"ringil.config applied", "ringil.config refused", "aaaaaaaaaaaa", "bbbbbbbbbbbb", "models", "providers", "max_cost", "USAGE"} {
		if !bytes.Contains(all, []byte(want)) {
			t.Errorf("missing %q", want)
		}
	}
	if bytes.Contains(all, []byte("secret")) {
		t.Fatal("a value or a message of the file reached the traces")
	}
}

// Only what differs is named, the instructions' text included.
func TestChangedKeys(t *testing.T) {
	a := &config{Models: []string{"openai:m"}, texts: map[string]string{"x.md": "one"}}
	b := &config{Models: []string{"openai:m"}, MaxTurns: 3, texts: map[string]string{"x.md": "two"}}
	if got := strings.Join(changedKeys(a, b), ","); got != "max_turns,instructions" {
		t.Fatalf("%s", got)
	}
	if got := changedKeys(a, a); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
