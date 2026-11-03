// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

// Exact-delivery integrity check on the production shape: a 3-node TLS route cluster running
// JetStream, a file stream with Replicas=3, publishers acking every message, and two durable
// pull consumers (on different nodes) that must each see every acknowledged message exactly
// once, byte-for-byte, in per-publisher order. Run it against stock and coalescing builds.
//
// Skipped unless DCB_RUN=1 and DCB_JSRUN=1. Knobs: DCB_N (total messages, default 200000),
// DCB_PUBS (default 4), DCB_TLS.
//
// Payload sizes are deterministic per (publisher, seq): mostly 200-500 B, some 0 B, some
// straddling the 16 KiB coalescing bound (15.9-16.5 KiB) and some well above it.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func jsPayloadSize(pub, seq int) int {
	switch seq % 100 {
	case 0:
		return 0
	case 1, 2, 3:
		return 15900 + (seq*7+pub*13)%601 // 15.9 - 16.5 KiB-ish, straddles 16384
	case 4:
		return 20000 + (seq*31)%20000
	default:
		return 200 + (seq*17+pub)%301
	}
}

func jsPayload(pub, seq int) []byte {
	b := make([]byte, jsPayloadSize(pub, seq))
	rand.New(rand.NewSource(int64(pub)<<32 | int64(seq))).Read(b)
	return b
}

type jsVerdict struct {
	unique, dups, hashBad, orderBad, missing, unexpected int
}

func TestJetStreamIntegrityBench(t *testing.T) {
	if os.Getenv("DCB_RUN") != "1" || os.Getenv("DCB_JSRUN") != "1" {
		t.Skip("measurement only: set DCB_RUN=1 DCB_JSRUN=1")
	}
	useTLS := envInt("DCB_TLS", 1) == 1
	total := envInt("DCB_N", 200000)
	npubs := envInt("DCB_PUBS", 4)
	per := total / npubs
	total = per * npubs

	dir := t.TempDir()
	writeBenchCerts(t, dir)
	var ports [3][2]int
	for i := range ports {
		ports[i] = [2]int{benchFreePort(), benchFreePort()}
	}
	names := []string{"A", "B", "C"}
	nodes := make([]*benchNode, 3)
	// Clustered JetStream is not "ready" until a meta leader exists, which needs the other
	// nodes up, so the three children are started together.
	var swg sync.WaitGroup
	for i, n := range names {
		sd := dir + "/store-" + n
		os.MkdirAll(sd, 0o755)
		// A ring (A->B->C->A) gives every pair exactly one solicited route; having every node
		// solicit every other makes each pair dial twice and churn on "Duplicate Route".
		peer := fmt.Sprintf("nats-route://127.0.0.1:%d", ports[(i+1)%3][1])
		swg.Add(1)
		go func(i int, n, sd, peer string) {
			defer swg.Done()
			nodes[i] = startBenchNode(t, n, dir, ports[i][0], ports[i][1], peer, useTLS, "DCB_JS=1", "DCB_STORE="+sd)
		}(i, n, sd, peer)
	}
	swg.Wait()
	stopped := false
	stopAll := func() {
		if !stopped {
			stopped = true
			for _, n := range nodes {
				n.stop(t)
			}
		}
	}
	defer stopAll()

	pool := x509.NewCertPool()
	pb, _ := os.ReadFile(dir + "/c.pem")
	pool.AppendCertsFromPEM(pb)
	connect := func(port int) *nats.Conn {
		o := []nats.Option{nats.MaxReconnects(0)}
		url := fmt.Sprintf("nats://127.0.0.1:%d", port)
		if useTLS {
			o = append(o, nats.Secure(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}))
			url = fmt.Sprintf("tls://127.0.0.1:%d", port)
		}
		nc, err := nats.Connect(url, o...)
		if err != nil {
			t.Fatal(err)
		}
		return nc
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	admin := connect(ports[0][0])
	defer admin.Close()
	ajs, _ := jetstream.New(admin)
	// Wait for the meta group to form, then create the replicated stream.
	var stream jetstream.Stream
	for i := 0; i < 60; i++ {
		var err error
		cctx, ccancel := context.WithTimeout(ctx, 15*time.Second)
		stream, err = ajs.CreateStream(cctx, jetstream.StreamConfig{
			Name: "S", Subjects: []string{"js.>"}, Replicas: 3, Storage: jetstream.FileStorage,
		})
		ccancel()
		if err == nil {
			break
		}
		fmt.Fprintln(os.Stderr, "create stream:", err)
		time.Sleep(time.Second)
	}
	if stream == nil {
		t.Fatal("could not create replicated stream")
	}

	fmt.Fprintln(os.Stderr, "stream created")
	var acked, failed atomic.Int64
	go func() {
		for ctx.Err() == nil {
			time.Sleep(5 * time.Second)
			fmt.Fprintf(os.Stderr, "progress: acked=%d failed=%d\n", acked.Load(), failed.Load())
		}
	}()
	var pubsDone atomic.Bool
	ackedSet := make([][]bool, npubs) // [pub][seq]
	for i := range ackedSet {
		ackedSet[i] = make([]bool, per)
	}

	// Consumers: node B and node C, each a durable pull consumer.
	verdicts := make([]jsVerdict, 2)
	var cwg sync.WaitGroup
	for ci, node := range []int{1, 2} {
		cwg.Add(1)
		go func(ci, node int) {
			defer cwg.Done()
			nc := connect(ports[node][0])
			defer nc.Close()
			js, _ := jetstream.New(nc)
			cons, err := js.CreateOrUpdateConsumer(ctx, "S", jetstream.ConsumerConfig{
				Durable: "d" + strconv.Itoa(ci), AckPolicy: jetstream.AckExplicitPolicy,
			})
			if err != nil {
				t.Errorf("consumer: %v", err)
				return
			}
			seen := make([][]bool, npubs)
			for i := range seen {
				seen[i] = make([]bool, per)
			}
			lastSeq := make([]int, npubs)
			for i := range lastSeq {
				lastSeq[i] = -1
			}
			v := &verdicts[ci]
			idle := time.Time{}
			for {
				batch, err := cons.Fetch(500, jetstream.FetchMaxWait(2*time.Second))
				if err != nil {
					time.Sleep(200 * time.Millisecond)
					continue
				}
				n := 0
				for m := range batch.Messages() {
					n++
					h := m.Headers()
					p, _ := strconv.Atoi(h.Get("X-Pub"))
					s, _ := strconv.Atoi(h.Get("X-Seq"))
					if p < 0 || p >= npubs || s < 0 || s >= per {
						v.unexpected++
						m.Ack()
						continue
					}
					sum := sha256.Sum256(m.Data())
					if hex.EncodeToString(sum[:]) != h.Get("X-Sha") || !bytes.Equal(m.Data(), jsPayload(p, s)) {
						v.hashBad++
					}
					if seen[p][s] {
						v.dups++
					} else {
						seen[p][s] = true
						v.unique++
					}
					if s <= lastSeq[p] {
						v.orderBad++
					}
					if s > lastSeq[p] {
						lastSeq[p] = s
					}
					m.Ack()
				}
				if pubsDone.Load() {
					if int64(v.unique) >= acked.Load() {
						break
					}
					if n == 0 {
						if idle.IsZero() {
							idle = time.Now()
						} else if time.Since(idle) > 30*time.Second {
							break
						}
					} else {
						idle = time.Time{}
					}
				}
			}
			// Everything acked must have been seen.
			for p := 0; p < npubs; p++ {
				for s := 0; s < per; s++ {
					if ackedSet[p][s] && !seen[p][s] {
						v.missing++
					}
				}
			}
		}(ci, node)
	}

	start := time.Now()
	var pwg sync.WaitGroup
	for p := 0; p < npubs; p++ {
		pwg.Add(1)
		go func(p int) {
			defer pwg.Done()
			nc := connect(ports[0][0])
			defer nc.Close()
			js, _ := jetstream.New(nc, jetstream.WithPublishAsyncMaxPending(256))
			futs := make([]jetstream.PubAckFuture, 0, 512)
			idxs := make([]int, 0, 512)
			drain := func() {
				for i, f := range futs {
					select {
					case <-f.Ok():
						ackedSet[p][idxs[i]] = true
						acked.Add(1)
					case <-f.Err():
						failed.Add(1)
					case <-time.After(60 * time.Second):
						failed.Add(1)
					}
				}
				futs, idxs = futs[:0], idxs[:0]
			}
			for s := 0; s < per; s++ {
				data := jsPayload(p, s)
				sum := sha256.Sum256(data)
				msg := nats.NewMsg(fmt.Sprintf("js.%d", p))
				msg.Data = data
				msg.Header.Set("X-Pub", strconv.Itoa(p))
				msg.Header.Set("X-Seq", strconv.Itoa(s))
				msg.Header.Set("X-Sha", hex.EncodeToString(sum[:]))
				f, err := js.PublishMsgAsync(msg)
				if err != nil {
					failed.Add(1)
					continue
				}
				futs = append(futs, f)
				idxs = append(idxs, s)
				if len(futs) >= 500 {
					drain()
				}
			}
			drain()
		}(p)
	}
	pwg.Wait()
	pubsDone.Store(true)
	pubWall := time.Since(start)
	cwg.Wait()
	wall := time.Since(start)

	info, err := stream.Info(ctx)
	var lastSeq, msgs uint64
	if err == nil {
		lastSeq, msgs = info.State.LastSeq, info.State.Msgs
	}
	admin.Close()
	stopAll()
	var cpu time.Duration
	var slow int64
	for _, n := range nodes {
		cpu += n.cpu
		slow += n.slow
	}
	ok := failed.Load() == 0 && acked.Load() == int64(total) && lastSeq == uint64(total) && msgs == uint64(total)
	for _, v := range verdicts {
		if v.unique != total || v.dups != 0 || v.hashBad != 0 || v.orderBad != 0 || v.missing != 0 || v.unexpected != 0 {
			ok = false
		}
	}
	fmt.Printf("JSRESULT tls=%v ok=%v total=%d acked=%d failed=%d lastseq=%d msgs=%d "+
		"B_unique=%d B_dups=%d B_hashbad=%d B_orderbad=%d B_missing=%d C_unique=%d C_dups=%d C_hashbad=%d C_orderbad=%d C_missing=%d "+
		"pubwall=%.1fs wall=%.1fs cpu=%.2fs cpu_s_per_1k=%.4f slow_consumers=%d\n",
		useTLS, ok, total, acked.Load(), failed.Load(), lastSeq, msgs,
		verdicts[0].unique, verdicts[0].dups, verdicts[0].hashBad, verdicts[0].orderBad, verdicts[0].missing,
		verdicts[1].unique, verdicts[1].dups, verdicts[1].hashBad, verdicts[1].orderBad, verdicts[1].missing,
		pubWall.Seconds(), wall.Seconds(), cpu.Seconds(), cpu.Seconds()/float64(total)*1000, slow)
	if !ok {
		t.Fail()
	}
}
