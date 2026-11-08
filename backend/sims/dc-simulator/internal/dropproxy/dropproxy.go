// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package dropproxy is the rehearsal's NEGATIVE CONTROL: an MQTT proxy that acknowledges a
// publish and then does not deliver it. It sits between the publishers and the target broker and,
// for every Nth QoS 1 PUBLISH it reads (counted across all connections), answers the publisher
// with a PUBACK of its own and never forwards the message. To the publisher that is a broker that
// promised and lost; to the read-back tap it is a message that never arrives.
//
// A loss verdict is worth nothing until it has been shown to fail: a gate that has only ever
// passed may be unable to fail. Running the harness through this proxy at N must exit with the
// loss code, naming exactly the number of messages the proxy dropped (Dropped), and running it
// without the proxy must pass. Both halves are the control.
package dropproxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	"github.com/eclipse/paho.mqtt.golang/packets"
)

// Proxy is a running drop-every-Nth MQTT proxy.
type Proxy struct {
	upstream string
	every    int64
	ln       net.Listener

	seen, dropped atomic.Int64

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
	done  atomic.Bool
}

// Start listens on listen (host:port; port 0 picks one) and proxies to upstream (host:port),
// dropping every every-th QoS 1 PUBLISH. every must be at least 1.
func Start(listen, upstream string, every int) (*Proxy, error) {
	if every < 1 {
		return nil, fmt.Errorf("drop every %d: must be at least 1", every)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	p := &Proxy{upstream: upstream, every: int64(every), ln: ln, conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.accept()
	return p, nil
}

// Addr is the address clients dial.
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Seen is the QoS 1 PUBLISH packets read from publishers so far.
func (p *Proxy) Seen() int64 { return p.seen.Load() }

// Dropped is how many of them were acknowledged and not forwarded.
func (p *Proxy) Dropped() int64 { return p.dropped.Load() }

// Close stops accepting, closes every connection and waits for the pumps to end.
func (p *Proxy) Close() {
	if p.done.Swap(true) {
		return
	}
	_ = p.ln.Close()
	p.mu.Lock()
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// Serve blocks until ctx ends, then closes the proxy.
func (p *Proxy) Serve(ctx context.Context) {
	<-ctx.Done()
	p.Close()
}

func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done.Load() {
		_ = c.Close()
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	_ = c.Close()
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

func (p *Proxy) accept() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(c) {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.serve(c)
		}()
	}
}

// lockedWriter serialises writes to the client: the upstream pump and the drop path both write.
type lockedWriter struct {
	mu sync.Mutex
	c  net.Conn
}

func (w *lockedWriter) write(pk packets.ControlPacket) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return pk.Write(w.c)
}

func (p *Proxy) serve(client net.Conn) {
	defer p.untrack(client)
	up, err := net.Dial("tcp", p.upstream)
	if err != nil {
		return
	}
	if !p.track(up) {
		return
	}
	defer p.untrack(up)

	cw := &lockedWriter{c: client}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // broker -> client, untouched
		defer wg.Done()
		defer p.untrack(client)
		for {
			pk, err := packets.ReadPacket(up)
			if err != nil {
				return
			}
			if cw.write(pk) != nil {
				return
			}
		}
	}()

	// client -> broker, with the drop.
	for {
		pk, err := packets.ReadPacket(client)
		if err != nil {
			break
		}
		if pub, ok := pk.(*packets.PublishPacket); ok && pub.Qos == 1 {
			if n := p.seen.Add(1); n%p.every == 0 {
				p.dropped.Add(1)
				ack := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
				ack.MessageID = pub.MessageID
				if cw.write(ack) != nil {
					break
				}
				continue
			}
		}
		if pk.Write(up) != nil {
			break
		}
	}
	_ = up.Close()
	wg.Wait()
}
