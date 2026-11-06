// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
)

type recordedWarnings struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordedWarnings) HandleWarningHeaderWithContext(_ context.Context, _ int, _ string, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, text)
}

// The warnings travel as `Warning:` response headers, so this sends them the way an API
// server does and lets client-go pick the handler: what matters is what a real client
// built without a handler of its own (as Helm's are) ends up showing.
func warningsSeenFor(t *testing.T, sent ...string) []string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for _, s := range sent {
			w.Header().Add("Warning", `299 - `+strconv.Quote(s))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"30"}`))
	}))
	defer srv.Close()

	rec := &recordedWarnings{}
	installWarningFilter(rec)
	t.Cleanup(func() { installWarningFilter(rest.WarningLogger{}) })

	c, err := rest.RESTClientFor(&rest.Config{
		Host: srv.URL,
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &schema.GroupVersion{Version: "v1"},
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get().AbsPath("/version").Do(context.Background()).Raw(); err != nil {
		t.Fatal(err)
	}
	return rec.seen
}

func TestIntegerFormatWarningsNeverReachTheHandler(t *testing.T) {
	got := warningsSeenFor(t, `unrecognized format "int32"`, `unrecognized format "int64"`, `unrecognized format "frobnicate"`)
	if len(got) != 1 || got[0] != `unrecognized format "frobnicate"` {
		t.Fatalf("a client saw %q; only the genuinely unrecognized format should get through", got)
	}
}

func TestBenignFormatWarning(t *testing.T) {
	for _, tc := range []struct {
		text string
		drop bool
	}{
		{`unrecognized format "int32"`, true},
		{`unrecognized format "int64"`, true},
		{`spec.versions[0].schema.properties[x]: unrecognized format "int32"`, true},
		{`unrecognized format "frobnicate"`, false},
		{`unrecognized format "int32" in something else`, false},
		{`v1 Endpoints is deprecated in v1.33+`, false},
		{``, false},
	} {
		if got := benignFormatWarning.MatchString(tc.text); got != tc.drop {
			t.Errorf("%q: dropped=%v, want %v", tc.text, got, tc.drop)
		}
	}
}

func TestFilterPassesEverythingElseThrough(t *testing.T) {
	rec := &recordedWarnings{}
	f := formatWarningFilter{next: rec}
	for _, text := range []string{
		`unrecognized format "int32"`,
		`unrecognized format "frobnicate"`,
		`v1 PodSecurityPolicy is deprecated`,
		`unrecognized format "int64"`,
	} {
		f.HandleWarningHeaderWithContext(context.Background(), 299, "-", text)
	}
	want := []string{`unrecognized format "frobnicate"`, `v1 PodSecurityPolicy is deprecated`}
	if len(rec.seen) != len(want) {
		t.Fatalf("passed through %q, want %q", rec.seen, want)
	}
	for i := range want {
		if rec.seen[i] != want[i] {
			t.Fatalf("passed through %q, want %q", rec.seen, want)
		}
	}
}
