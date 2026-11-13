// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

func TestStopMetricsJoinsAndReleasesListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	addr := ln.Addr().String()
	s, err := server.NewServer(&server.Options{NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	b := &broker{s: s, metrics: newBrokerMetrics()}
	b.serveMetrics(ln)
	t.Cleanup(func() { b.metricsSrv.Close() })
	client := &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	resp, err := client.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "dc_broker_info{") {
		t.Fatalf("GET /metrics: status=%d body=%s", resp.StatusCode, body)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.stopMetrics(ctx); err != nil {
		t.Errorf("stopMetrics: %v", err)
	}
	select {
	case <-b.metricsDone:
	case <-ctx.Done():
		t.Error("metrics Serve goroutine did not return before the deadline")
	}
	rebound, err := net.Listen("tcp", addr)
	if err != nil {
		t.Errorf("metrics address %s was not released: %v", addr, err)
	} else {
		rebound.Close()
	}
	if err := b.stopMetrics(ctx); err != nil {
		t.Errorf("repeated stopMetrics: %v", err)
	}
}

func TestStopMetricsBeforeStart(t *testing.T) {
	b := &broker{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.stopMetrics(ctx); err != nil {
		t.Fatal(err)
	}
}
