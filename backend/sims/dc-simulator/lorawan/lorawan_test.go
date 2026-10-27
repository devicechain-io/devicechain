// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-event-sources/integration"
)

var fixedNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func cfg(ns string) Config {
	return Config{
		NS: ns, URL: "https://dc.example.com/api/integrations/acme/lora", Token: "t",
		DevEUIs: []string{"70b3d57ed0012345"}, Uplinks: 3,
	}
}

// request parses what Build made through the REAL codec the platform's route uses, so a payload the
// simulator and the platform disagree about fails here and not on a cluster.
func decode(t *testing.T, ns string, r Request, link bool) *integration.Result {
	t.Helper()
	u, err := url.Parse(r.URL)
	require.NoError(t, err)
	typ := integration.ChirpStackV4
	msgType := ""
	if ns == TTS {
		typ = integration.TTSV3
		msgType = strings.TrimPrefix(u.Path[strings.LastIndex(u.Path, "/lora"):], "/lora/")
	}
	d := &integration.Descriptor{Token: "lora", Type: typ, Enabled: true, Identity: "DEV_EUI", LinkMetrics: link}
	require.NoError(t, d.Prepare())
	res, err := integration.Decode(d, &integration.Request{
		Body: r.Body, Query: u.Query(), ContentType: r.ContentType, MessageType: msgType,
		Header: http.Header{}, ReceivedAt: fixedNow,
	})
	require.NoError(t, err)
	return res
}

func TestBuildIsReadByThePlatformCodecs(t *testing.T) {
	for _, ns := range []string{ChirpStack, TTS} {
		t.Run(ns, func(t *testing.T) {
			c := cfg(ns)
			r, err := Build(c, "run1", "70b3d57ed0012345", 1, fixedNow)
			require.NoError(t, err)
			res := decode(t, ns, r, true)
			require.False(t, res.Ignored)
			require.Len(t, res.Uplinks, 1)
			u := res.Uplinks[0]
			require.Equal(t, "70B3D57ED0012345", u.Identifier)
			require.True(t, u.Time.Equal(fixedNow), "the envelope time is the sender's")
			require.NotNil(t, u.Link)
			require.Equal(t, 2, u.Link.Gateways)
			require.True(t, u.Link.HasSignal)

			flat, err := integration.Flatten(u.Decoded, u.Link)
			require.NoError(t, err)
			require.Contains(t, flat.Readings, "temperature")
			require.Contains(t, flat.Readings, "battery.voltage")
			require.Equal(t, "1", flat.Readings["door"], "an odd uplink has the door open: a boolean is stored as 1")
			require.Equal(t, 4, flat.Leaves, "temperature, humidity, battery.voltage and door; the status string is skipped")
			require.Equal(t, 1, flat.Skipped[integration.SkipString])
			require.Contains(t, flat.Readings, integration.KeyRSSI)
			require.Contains(t, flat.Readings, integration.KeyGateways)
			require.Equal(t, "2", flat.Readings[integration.KeyGateways])
		})
	}
}

func TestChirpStackRequestShape(t *testing.T) {
	r, err := Build(cfg(ChirpStack), "run1", "70B3D57ED0012345", 0, fixedNow)
	require.NoError(t, err)
	require.Equal(t, "https://dc.example.com/api/integrations/acme/lora?event=up", r.URL)
	require.Equal(t, "application/json", r.ContentType)
}

func TestTTSPostsToAnUplinkPathAndStaysInsideTheAgeWindow(t *testing.T) {
	r, err := Build(cfg(TTS), "run1", "70B3D57ED0012345", 0, fixedNow)
	require.NoError(t, err)
	require.Equal(t, "https://dc.example.com/api/integrations/acme/lora/uplink-message", r.URL)
	var body struct {
		ReceivedAt string `json:"received_at"`
	}
	require.NoError(t, json.Unmarshal(r.Body, &body))
	at, err := time.Parse(time.RFC3339Nano, body.ReceivedAt)
	require.NoError(t, err)
	require.WithinDuration(t, fixedNow, at, time.Second)
	// And it is the uplink path the codec reads, not one it ignores.
	require.False(t, decode(t, TTS, r, false).Ignored)
}

func TestARetryIsTheSameMessageAndANewUplinkIsNot(t *testing.T) {
	for _, ns := range []string{ChirpStack, TTS} {
		a, _ := Build(cfg(ns), "run1", "70B3D57ED0012345", 4, fixedNow)
		b, _ := Build(cfg(ns), "run1", "70B3D57ED0012345", 4, fixedNow)
		require.Equal(t, a.Body, b.Body, ns+": a retry is byte for byte the same body")
		idA := decode(t, ns, a, false).Uplinks[0].AltId
		require.Equal(t, idA, decode(t, ns, b, false).Uplinks[0].AltId)

		next, _ := Build(cfg(ns), "run1", "70B3D57ED0012345", 5, fixedNow)
		require.NotEqual(t, idA, decode(t, ns, next, false).Uplinks[0].AltId)
		other, _ := Build(cfg(ns), "run1", "70B3D57ED0099999", 4, fixedNow)
		require.NotEqual(t, idA, decode(t, ns, other, false).Uplinks[0].AltId)
		if ns == ChirpStack {
			again, _ := Build(cfg(ns), "run2", "70B3D57ED0012345", 4, fixedNow)
			require.NotEqual(t, idA, decode(t, ns, again, false).Uplinks[0].AltId, "another run does not dedup against this one")
		}
	}
}

func TestDeduplicationIdIsAUUID(t *testing.T) {
	r, _ := Build(cfg(ChirpStack), "run1", "70B3D57ED0012345", 0, fixedNow)
	var body struct {
		D string `json:"deduplicationId"`
	}
	require.NoError(t, json.Unmarshal(r.Body, &body))
	require.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, body.D)
}

func TestRunPostsWithBearerAndCountsStatuses(t *testing.T) {
	var mu sync.Mutex
	var auth []string
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := cfg(ChirpStack)
	c.URL = srv.URL + "/api/integrations/acme/lora"
	c.Token = "secret-token"
	c.DevEUIs = ParseDevEUIs("70b3d57ed0012345, 70B3D57ED0012346,70b3d57ed0012345")
	c.Resend = true
	c.Now = func() time.Time { return fixedNow }
	sum, err := Run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, 6, sum.Sent, "3 uplinks of 2 devices")
	require.Equal(t, 6, sum.Resent)
	require.Equal(t, map[string]int{"202": 12}, sum.Statuses)
	require.Zero(t, sum.Errors)
	for _, a := range auth {
		require.Equal(t, "Bearer secret-token", a)
	}
	require.Equal(t, "/api/integrations/acme/lora?event=up", paths[0])
}

func TestHostHeaderIsSent(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	c := cfg(ChirpStack)
	c.URL = srv.URL + "/api/integrations/acme/lora"
	c.Uplinks = 1
	c.HostHeader = "acme.localhost"
	_, err := Run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, "acme.localhost", host)
}

func TestRunCountsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := cfg(TTS)
	c.URL = srv.URL + "/api/integrations/acme/lora"
	c.Uplinks = 1
	sum, err := Run(context.Background(), c)
	require.NoError(t, err)
	require.Equal(t, map[string]int{"401": 1}, sum.Statuses)
}

func TestValidateRefusesWhatCannotRun(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"unknown ns":     func(c *Config) { c.NS = "other" },
		"no token":       func(c *Config) { c.Token = "" },
		"url with query": func(c *Config) { c.URL += "?event=up" },
		"not a url":      func(c *Config) { c.URL = "lora" },
		"no devices":     func(c *Config) { c.DevEUIs = nil },
		"short deveui":   func(c *Config) { c.DevEUIs = []string{"abc"} },
		"non-hex deveui": func(c *Config) { c.DevEUIs = []string{"ZZB3D57ED0012345"} },
		"no uplinks":     func(c *Config) { c.Uplinks = 0 },
	} {
		c := cfg(ChirpStack)
		mutate(&c)
		require.Error(t, c.Validate(), name)
	}
	c := cfg(TTS)
	require.NoError(t, c.Validate())
}
