// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

// withFakeDNS makes the process resolver answer names from answers (and nothing else) for
// the rest of the test. It lets a test give one hostname two addresses on any machine,
// rather than depending on how the host's /etc/hosts spells localhost.
func withFakeDNS(t *testing.T, answers map[string][]netip.Addr) {
	t.Helper()
	prev := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			client, server := net.Pipe()
			go serveDNS(server, answers)
			return client, nil
		},
	}
	t.Cleanup(func() { net.DefaultResolver = prev })
}

// serveDNS answers length-prefixed (stream) DNS queries on c.
func serveDNS(c net.Conn, answers map[string][]netip.Addr) {
	defer c.Close()
	for {
		var l [2]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return
		}
		msg := make([]byte, binary.BigEndian.Uint16(l[:]))
		if _, err := io.ReadFull(c, msg); err != nil {
			return
		}
		var p dnsmessage.Parser
		h, err := p.Start(msg)
		if err != nil {
			return
		}
		q, err := p.Question()
		if err != nil {
			return
		}
		b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true})
		b.EnableCompression()
		_ = b.StartQuestions()
		_ = b.Question(q)
		_ = b.StartAnswers()
		name := strings.TrimSuffix(q.Name.String(), ".")
		rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}
		for _, a := range answers[name] {
			switch {
			case a.Is4() && q.Type == dnsmessage.TypeA:
				_ = b.AResource(rh, dnsmessage.AResource{A: a.As4()})
			case a.Is6() && q.Type == dnsmessage.TypeAAAA:
				_ = b.AAAAResource(rh, dnsmessage.AAAAResource{AAAA: a.As16()})
			}
		}
		out, err := b.Finish()
		if err != nil {
			return
		}
		binary.BigEndian.PutUint16(l[:], uint16(len(out)))
		if _, err := c.Write(append(l[:], out...)); err != nil {
			return
		}
	}
}
