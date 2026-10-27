// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package lorawan is the simulator's integration mode: it plays a LoRaWAN network server that
// forwards uplinks to a tenant's inbound integration over HTTPS, the way a real ChirpStack v4 HTTP
// integration or a The Things Stack v3 webhook does.
//
// Like the rest of the simulator it is an untrusted external client. It holds no handshake, no
// service token and no database access: all it knows is a URL and the integration's bearer token,
// which is exactly what a network server's settings page holds. The integration and the devices
// whose DevEUIs it names are created beforehand, through the tenant API.
package lorawan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Network servers the simulator can play.
const (
	ChirpStack = "chirpstack"
	TTS        = "tts"
)

// TTSPath is the webhook path the simulator posts uplinks to. It is one of the paths the platform
// reads as the uplink message type; the suggested one is the most specific.
const TTSPath = "uplink-message"

// Config is one run.
type Config struct {
	// NS is ChirpStack or TTS.
	NS string
	// URL is the integration's base URL, https://<host>/api/integrations/<tenant>/<integration>,
	// with no query and no message type: the simulator appends what the network server appends.
	URL string
	// Token is the integration's bearer token.
	Token string
	// HostHeader, when set, is sent as the request's Host. For an instance whose ingress host does not
	// resolve from this machine (a name under .localhost does not, in Go's resolver): point URL at the
	// address that does and name the ingress host here, as curl's -H 'Host: ...' would.
	HostHeader string
	// DevEUIs are the devices that uplink, 16 hex digits each.
	DevEUIs []string
	// Uplinks is how many uplinks each device sends.
	Uplinks int
	// Interval is the pause between rounds of uplinks; zero sends them back to back.
	Interval time.Duration
	// Resend posts every uplink a second time, byte for byte, as a network server retrying after a
	// timeout does. The platform stores each uplink once.
	Resend bool
	// RunID keeps one run's deduplication keys apart from another's. Empty derives one from the clock.
	RunID string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Client posts the requests; nil means a client with a 15 second timeout.
	Client *http.Client
	// Gateways is how many gateways report each uplink (default 2).
	Gateways int
}

// Summary is what a run did, printed as one line of JSON.
type Summary struct {
	NS       string         `json:"ns"`
	Sent     int            `json:"sent"`
	Resent   int            `json:"resent"`
	Statuses map[string]int `json:"statuses"`
	Errors   int            `json:"errors"`
	// FirstError is the first transport failure, so a run that could not reach the platform says why.
	FirstError string `json:"firstError,omitempty"`
	RunID      string `json:"runId"`
}

// Request is one HTTP request the simulator makes.
type Request struct {
	URL         string
	ContentType string
	Body        []byte
}

// Validate reports a configuration that cannot produce a run.
func (c *Config) Validate() error {
	switch c.NS {
	case ChirpStack, TTS:
	default:
		return fmt.Errorf("--lorawan-ns must be %q or %q, not %q", ChirpStack, TTS, c.NS)
	}
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("--integration-url must be an http(s) URL, such as https://host/api/integrations/<tenant>/<integration>")
	}
	if u.RawQuery != "" {
		return fmt.Errorf("--integration-url must not carry a query: the simulator appends ?event=up itself")
	}
	if c.Token == "" {
		return fmt.Errorf("an integration bearer token is required (--integration-token or DC_SIM_INTEGRATION_TOKEN)")
	}
	if len(c.DevEUIs) == 0 {
		return fmt.Errorf("--deveui names no device")
	}
	for _, e := range c.DevEUIs {
		if !validDevEUI(e) {
			return fmt.Errorf("%q is not a DevEUI: 16 hexadecimal digits", e)
		}
	}
	if c.Uplinks < 1 {
		return fmt.Errorf("--uplinks must be at least 1")
	}
	return nil
}

func validDevEUI(s string) bool {
	if len(s) != 16 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// Run posts every uplink and returns what the platform answered. A transport failure is counted
// in Errors and does not stop the run.
func Run(ctx context.Context, c Config) (Summary, error) {
	if err := c.Validate(); err != nil {
		return Summary{}, err
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	runID := c.RunID
	if runID == "" {
		runID = fmt.Sprintf("%d", now().UnixNano())
	}
	sum := Summary{NS: c.NS, Statuses: map[string]int{}, RunID: runID}
	post := func(r Request) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(r.Body))
		if err != nil {
			sum.Errors++
			return
		}
		if c.HostHeader != "" {
			req.Host = c.HostHeader
		}
		req.Header.Set("Content-Type", r.ContentType)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := client.Do(req)
		if err != nil {
			sum.Errors++
			if sum.FirstError == "" {
				sum.FirstError = err.Error()
			}
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		sum.Statuses[fmt.Sprint(resp.StatusCode)]++
	}
	for n := 0; n < c.Uplinks; n++ {
		at := now()
		for _, eui := range c.DevEUIs {
			if err := ctx.Err(); err != nil {
				return sum, err
			}
			r, err := Build(c, runID, eui, n, at)
			if err != nil {
				return sum, err
			}
			post(r)
			sum.Sent++
			if c.Resend {
				post(r)
				sum.Resent++
			}
		}
		if c.Interval > 0 && n < c.Uplinks-1 {
			select {
			case <-ctx.Done():
				return sum, ctx.Err()
			case <-time.After(c.Interval):
			}
		}
	}
	return sum, nil
}

// Build makes the n-th uplink of one device, as the network server would post it. It is a pure
// function of its arguments, so a resend is the same bytes and the same deduplication key.
func Build(c Config, runID, devEUI string, n int, at time.Time) (Request, error) {
	devEUI = strings.ToUpper(devEUI)
	r := ReadingsOf(devEUI, n)
	gateways := c.Gateways
	if gateways < 1 {
		gateways = 2
	}
	switch c.NS {
	case ChirpStack:
		body, err := chirpstackUplink(runID, devEUI, n, at, r, gateways)
		if err != nil {
			return Request{}, err
		}
		return Request{URL: strings.TrimRight(c.URL, "/") + "?event=up", ContentType: "application/json", Body: body}, nil
	case TTS:
		body, err := ttsUplink(devEUI, n, at, r, gateways)
		if err != nil {
			return Request{}, err
		}
		return Request{URL: strings.TrimRight(c.URL, "/") + "/" + TTSPath, ContentType: "application/json", Body: body}, nil
	}
	return Request{}, fmt.Errorf("unknown network server %q", c.NS)
}

// Readings is what a device's codec decodes an uplink into. The keys are what a tenant declares
// metrics for: nested objects flatten to dot paths (battery.voltage), a boolean is stored as 1 or 0,
// and the string is skipped.
type Readings struct {
	Temperature float64
	Humidity    float64
	Voltage     float64
	Door        bool
}

// ReadingsOf is the deterministic value set of a device's n-th uplink.
func ReadingsOf(devEUI string, n int) Readings {
	h := sha256.Sum256([]byte(strings.ToUpper(devEUI)))
	base := float64(h[0]%100) / 10 // 0.0 .. 9.9
	return Readings{
		Temperature: round1(15 + base + float64(n%10)/2),
		Humidity:    round1(40 + float64(h[1]%30) + float64(n%5)),
		Voltage:     round1(3.0 + float64(h[2]%5)/10),
		Door:        n%2 == 1,
	}
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

// decoded is the codec's output object.
func (r Readings) decoded() map[string]any {
	return map[string]any{
		"temperature": r.Temperature,
		"humidity":    r.Humidity,
		"battery":     map[string]any{"voltage": r.Voltage},
		"door":        r.Door,
		"status":      "ok",
	}
}

// frame is a stand-in for the LoRaWAN application payload the codec would have decoded.
func frame(devEUI string, n int) []byte {
	b := make([]byte, 6)
	binary.BigEndian.PutUint16(b[0:], uint16(n))
	copy(b[2:], devEUI[:4])
	return b
}

// uuidOf derives a version-4 shaped UUID from its parts: ChirpStack's deduplication id is a UUID,
// and the platform refuses anything else.
func uuidOf(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	b := h[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func gatewayID(i int) string { return fmt.Sprintf("0016c001ff10%04x", i+1) }

func chirpstackUplink(runID, devEUI string, n int, at time.Time, r Readings, gateways int) ([]byte, error) {
	rx := make([]map[string]any, gateways)
	for i := range rx {
		rx[i] = map[string]any{"gatewayId": gatewayID(i), "rssi": -80 - 7*i, "snr": 8.5 - 2.5*float64(i)}
	}
	return json.Marshal(map[string]any{
		"deduplicationId": uuidOf("cs", runID, devEUI, fmt.Sprint(n)),
		"time":            at.UTC().Format(time.RFC3339Nano),
		"deviceInfo": map[string]any{
			"tenantName": "sim", "applicationName": "sim", "deviceProfileName": "sim",
			"deviceName": "sim-" + strings.ToLower(devEUI), "devEui": devEUI,
		},
		"devAddr":   "01020304",
		"adr":       true,
		"dr":        5,
		"fCnt":      n,
		"fPort":     1,
		"confirmed": false,
		"data":      base64.StdEncoding.EncodeToString(frame(devEUI, n)),
		"object":    r.decoded(),
		"rxInfo":    rx,
	})
}

func ttsUplink(devEUI string, n int, at time.Time, r Readings, gateways int) ([]byte, error) {
	rx := make([]map[string]any, gateways)
	for i := range rx {
		rx[i] = map[string]any{
			"gateway_ids": map[string]any{"gateway_id": fmt.Sprintf("sim-gw-%d", i+1)},
			"rssi":        -80 - 7*i, "channel_rssi": -80 - 7*i, "snr": 8.5 - 2.5*float64(i),
		}
	}
	stamp := at.UTC().Format(time.RFC3339Nano)
	return json.Marshal(map[string]any{
		"end_device_ids": map[string]any{
			"device_id":       "sim-" + strings.ToLower(devEUI),
			"application_ids": map[string]any{"application_id": "sim"},
			"dev_eui":         devEUI,
			"join_eui":        "0000000000000000",
			"dev_addr":        "01020304",
		},
		"correlation_ids": []string{fmt.Sprintf("as:up:SIM%d", n)},
		"received_at":     stamp,
		"uplink_message": map[string]any{
			"f_port":          1,
			"f_cnt":           n,
			"frm_payload":     base64.StdEncoding.EncodeToString(frame(devEUI, n)),
			"decoded_payload": r.decoded(),
			"rx_metadata":     rx,
			"received_at":     stamp,
		},
	})
}

// ParseDevEUIs splits a comma-separated list, dropping blanks and repeats.
func ParseDevEUIs(list string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(list, ",") {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
