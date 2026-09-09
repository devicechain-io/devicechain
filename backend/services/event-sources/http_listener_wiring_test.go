// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/processor"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The wire from configuration to the listener that has to obey it.
//
// 🔴 EVERY OTHER TEST OF THESE BOUNDS HANDS THEM TO A SOURCE IT BUILT ITSELF, so all of
// them pass whether or not the one production construction site ever reads the
// configuration. Replace the argument there with a zero value and nothing else in the
// suite notices — the settings would be un-tunable, and an operator raising the header
// bound for a fleet on a slow radio would watch the old value keep closing connections.
// The same goes for the early-close accounting: a source built without it counts nothing
// and reports success.
func TestTheConfiguredListenerBoundsReachTheSource(t *testing.T) {
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	t.Cleanup(func() { Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs })

	Microservice = &core.Microservice{InstanceId: "inst-1"}

	// Deliberately NOT the platform defaults, so a construction site that ignored the
	// configuration would be caught rather than accidentally agreeing.
	const headerSeconds, requestSeconds = 17, 43
	if headerSeconds == config.DefaultHttpHeaderTimeoutSeconds || requestSeconds == config.DefaultHttpRequestTimeoutSeconds {
		t.Fatal("pick values that differ from the defaults, or this test cannot fail")
	}
	Configuration = &config.EventSourcesConfiguration{
		HttpIngest: config.HttpIngest{HeaderTimeoutSeconds: headerSeconds, RequestTimeoutSeconds: requestSeconds},
		EventSources: []config.EventSource{{
			Id:            "http1",
			Type:          config.SourceTypeHttp,
			Configuration: map[string]string{"port": "8081"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}

	if err := buildEventSources(); err != nil {
		t.Fatalf("buildEventSources: %v", err)
	}
	if len(EventSources) != 1 {
		t.Fatalf("built %d sources, want 1", len(EventSources))
	}
	source, ok := EventSources[0].(*processor.HttpEventSource)
	if !ok {
		t.Fatalf("built a %T, want an HTTP event source", EventSources[0])
	}

	if got := source.Ingest.HeaderTimeout(); got != headerSeconds*time.Second {
		t.Errorf("the listener would wait %v for headers, operator configured %ds", got, headerSeconds)
	}
	if got := source.Ingest.RequestTimeout(); got != requestSeconds*time.Second {
		t.Errorf("the listener would allow %v for a whole request, operator configured %ds", got, requestSeconds)
	}
	if source.Port != 8081 {
		t.Errorf("the listener would bind %d, configured 8081", source.Port)
	}
}

// The wire from the listener to the counter that reports on it.
//
// The accounting is one argument at one construction site. A source built without it
// counts nothing, reports a healthy start, and leaves an operator reading a flat metric
// while the bound closes connections — which is exactly the invisibility the counter
// exists to end, restored by omission.
func TestTheEarlyCloseCounterIsWiredToTheSource(t *testing.T) {
	savedConfig, savedSources, savedMs := Configuration, EventSources, Microservice
	t.Cleanup(func() { Configuration, EventSources, Microservice = savedConfig, savedSources, savedMs })

	Microservice = &core.Microservice{InstanceId: "inst-1"}
	Microservice.UseMetricsRegistry(prometheus.NewRegistry())
	initializeMetrics()

	Configuration = &config.EventSourcesConfiguration{
		// One second, so the bound is reached inside a test rather than waited out.
		HttpIngest: config.HttpIngest{HeaderTimeoutSeconds: 1},
		EventSources: []config.EventSource{{
			Id:            "http1",
			Type:          config.SourceTypeHttp,
			Configuration: map[string]string{"port": "8081"},
			Decoder:       config.EventDecoder{Type: processor.DECODER_TYPE_JSON},
		}},
	}
	if err := buildEventSources(); err != nil {
		t.Fatalf("buildEventSources: %v", err)
	}
	source := EventSources[0].(*processor.HttpEventSource)
	source.Port = 0 // let the OS choose, so this test needs no fixed port

	ctx := context.Background()
	if err := source.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := source.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = source.Stop(context.Background()) })

	conn, err := net.Dial("tcp", listenerAddr(t))
	if err != nil {
		t.Fatalf("dialling the listener: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("POST /inst-1/acme/events HTTP/1.1\r\nHost: example\r\n")); err != nil {
		t.Fatalf("writing partial headers: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
		t.Fatalf("setting a read deadline: %v", err)
	}
	if _, err := conn.Read(make([]byte, 128)); err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			t.Fatal("the server held the connection past its bound; the configured bound never reached it")
		}
	}

	// The state hook runs on the connection's own goroutine, so the count is observed
	// with a bounded wait rather than assumed to have landed already.
	deadline := time.Now().Add(2 * time.Second)
	var got float64
	for time.Now().Before(deadline) {
		got = testutil.ToFloat64(EarlyCloseCounter.WithLabelValues("http1"))
		if got > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got != 1 {
		t.Errorf("total_http_connections_closed_before_request = %v for the source that held the listener, want 1", got)
	}
}

// listenerAddr is the address the started source is actually serving on.
func listenerAddr(t *testing.T) string {
	t.Helper()
	source := EventSources[0].(*processor.HttpEventSource)
	addr := source.Addr()
	if addr == "" {
		t.Fatal("the started source reports no address")
	}
	return addr
}
