// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// BenchmarkCaptureIngestUnderPubAckLatency is BenchmarkCaptureIngestThroughput with the
// broker's answers to the inbound-events WRITER delayed, which is the one thing loopback
// cannot show: what the publish window does when a PubAck takes as long as it does on a
// loaded replicated broker.
//
// GKE perf round 12 drove 12,000 events/s over MQTT and the capture source drained about
// 7,100/s at 0.38 cores, with the durable's acknowledgement backlog at the whole pipeline
// (window + decode queue + a fetch batch) and the writer's publish p50 at 38 ms. By Little's
// law 512 in flight at 7,100/s is a mean settle time of about 72 ms, so a pod there was
// bounded by window / settle time, not by anything it computes. This reproduces that shape:
// the reader connects directly (a fetch round trip there is a millisecond or two), and only
// the writer's connection passes through a proxy that holds every byte the broker sends it
// for `delay`, so a PubAck lands one delay after the publish.
//
// Expect each arm to land just under window / delay: the window is the ceiling, and the
// source's own work is far below it. Absolute numbers depend on the host; the ratio between
// windows is the result.
//
// Run with: go test ./processor -run '^$' -bench BenchmarkCaptureIngestUnderPubAckLatency -benchtime 1x -count 3 -p 1
func BenchmarkCaptureIngestUnderPubAckLatency(b *testing.B) {
	const events = 40000
	prev := zerolog.GlobalLevel()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(prev) })

	srv := captureBenchServer(b)
	n := 0
	for _, delay := range []time.Duration{36 * time.Millisecond, 72 * time.Millisecond} {
		proxy := newDelayProxy(b, srv.Addr().String(), delay)
		for _, mode := range []string{"w=512", "w=1024"} {
			n++
			instance := fmt.Sprintf("caplat%d", n)
			b.Run(fmt.Sprintf("pubAckDelay=%s/publish=%s", delay, mode), func(b *testing.B) {
				for iter := 0; iter < b.N; iter++ {
					rate := runCapturePipelineVia(b, srv, proxy, fmt.Sprintf("%s-%d-%d", instance, iter, time.Now().UnixNano()),
						1, mode, events)
					b.ReportMetric(rate, "ev/s")
				}
			})
		}
	}
}

// newDelayProxy listens on loopback and forwards every connection to target, holding each
// chunk the TARGET sends back for delay before passing it on. Client-to-server bytes pass
// at once, so a request/response round trip costs one delay. It is a delay line, not a
// bandwidth limit: chunks queue behind one another only by their own timestamps.
func newDelayProxy(b *testing.B, target string, delay time.Duration) string {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	var conns sync.WaitGroup
	b.Cleanup(func() {
		_ = ln.Close()
		conns.Wait()
	})
	go func() {
		for {
			client, err := ln.Accept()
			if err != nil {
				return
			}
			server, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close()
				continue
			}
			conns.Add(2)
			go func() {
				defer conns.Done()
				_, _ = io.Copy(server, client)
				_ = server.Close()
			}()
			go func() {
				defer conns.Done()
				delayCopy(client, server, delay)
				_ = client.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

type delayedChunk struct {
	at   time.Time
	data []byte
}

func delayCopy(dst io.Writer, src io.Reader, delay time.Duration) {
	chunks := make(chan delayedChunk, 1<<16)
	go func() {
		defer close(chunks)
		buf := make([]byte, 64<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				chunks <- delayedChunk{at: time.Now().Add(delay), data: append([]byte(nil), buf[:n]...)}
			}
			if err != nil {
				return
			}
		}
	}()
	for c := range chunks {
		if wait := time.Until(c.at); wait > 0 {
			time.Sleep(wait)
		}
		if _, err := dst.Write(c.data); err != nil {
			for range chunks {
			}
			return
		}
	}
}

// hostPort splits a proxy address for the manager configuration.
func hostPort(b *testing.B, addr string) (string, uint32) {
	b.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		b.Fatal(err)
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		b.Fatal(err)
	}
	return host, uint32(p)
}
