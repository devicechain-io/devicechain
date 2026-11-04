// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNeedsHostHeader(t *testing.T) {
	for server, want := range map[string]bool{
		"34.85.231.141":         true,
		"34.85.231.141:8080":    true,
		"localhost":             true,
		"LocalHost:8080":        true,
		"127.0.0.1:8080":        true,
		"[::1]:8080":            true,
		"::1":                   true,
		"devicechain.local":     false,
		"rc.1.2.3.4.nip.io":     false,
		"rc.1.2.3.4.nip.io:443": false,
	} {
		if got := NeedsHostHeader(server); got != want {
			t.Errorf("NeedsHostHeader(%q) = %v, want %v", server, got, want)
		}
	}
}

func TestPickHostHeader(t *testing.T) {
	cases := []struct{ name, server, explicit, recorded, want string }{
		{"address uses the recorded host", "34.85.231.141", "", "devicechain.local", "devicechain.local"},
		{"port-forward uses the recorded host", "localhost:8080", "", "devicechain.local", "devicechain.local"},
		{"explicit wins over everything", "34.85.231.141", "rc.example.io", "devicechain.local", "rc.example.io"},
		{"explicit applies even to a DNS name", "a.example.io", "b.example.io", "", "b.example.io"},
		{"a DNS name is the operator's own host", "rc.example.io", "", "devicechain.local", ""},
		{"loopback instance needs no header", "localhost", "", "localhost", ""},
		{"loopback address of a localhost instance", "127.0.0.1", "", "localhost", "localhost"},
		{"nothing recorded, nothing to send", "34.85.231.141", "", "", ""},
		{"blank explicit is not an override", "34.85.231.141", "  ", "devicechain.local", "devicechain.local"},
	}
	for _, c := range cases {
		if got := PickHostHeader(c.server, c.explicit, c.recorded); got != c.want {
			t.Errorf("%s: PickHostHeader(%q,%q,%q) = %q, want %q", c.name, c.server, c.explicit, c.recorded, got, c.want)
		}
	}
}

// The client must dial the URL's address but present the instance host, which is the
// only thing a hostname-bound Gateway listener routes on.
func TestHostHeaderClientPresentsHostWhileDialingTheAddress(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := HostHeaderClient("devicechain.local").Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if seen != "devicechain.local" {
		t.Errorf("server saw Host %q, want devicechain.local", seen)
	}

	// Control: the default client sends the dialled address, which is the bug.
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.HasPrefix(seen, "127.0.0.1:") {
		t.Errorf("control: default client sent Host %q, expected the dialled address", seen)
	}
}
