// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package lorawan

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Downlink is one run's ChirpStack v4 queue and serialized event log.
// Construct it with NewDownlink before starting the HTTP listener or Run.
type Downlink struct {
	mu       sync.Mutex
	key      string
	ack      string
	writer   io.Writer
	devices  map[string]bool
	pending  map[string][]queueItem
	counters map[string]uint32
	queued   int
	closed   bool
}

type queueItem struct {
	DevEUI    string `json:"devEui"`
	FPort     int    `json:"fPort"`
	Confirmed bool   `json:"confirmed"`
	Data      string `json:"data"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	id        string
}

// NewDownlink creates a queue for the run's devices. The writer receives JSON event lines;
// nil discards them. The API key is separate from the integration token used by Run.
func NewDownlink(key, ack string, devices []string, writer io.Writer) (*Downlink, error) {
	if key == "" {
		return nil, fmt.Errorf("--lorawan-downlink-listen requires DC_SIM_LORAWAN_API_KEY")
	}
	if ack != "ack" && ack != "nack" && ack != "none" {
		return nil, fmt.Errorf("--lorawan-ack must be ack, nack or none")
	}
	d := &Downlink{key: key, ack: ack, writer: writer, devices: map[string]bool{}, pending: map[string][]queueItem{}, counters: map[string]uint32{}}
	for _, e := range devices {
		d.devices[strings.ToUpper(e)] = true
	}
	return d, nil
}

func (d *Downlink) log(v any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.writer != nil {
		_ = json.NewEncoder(d.writer).Encode(v)
	}
}

// Listening logs the actual socket address, including an OS-selected port.
func (d *Downlink) Listening(addr string) {
	d.log(map[string]any{"event": "downlink_listening", "addr": addr, "ack": d.ack})
}

// ServeHTTP implements the v1.4.0 platform contract in
// backend/services/outbound-connectors/lpwan/chirpstack.go: L73-81 and L91-98
// define the queueItem envelope; L103-105 append /api/devices/{devEui}/queue;
// L110-112 set JSON headers and bearer auth; L135-145 require a nonempty id;
// L146-156 and L182-204 require unambiguous gRPC codes for clean refusals.
// A base URL path prefix is accepted by matching this path suffix.
func (d *Downlink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	refuse := func(status, code int, reason string) {
		d.log(map[string]any{"event": "downlink_refused", "status": status, "reason": reason})
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": reason})
	}
	parts := strings.Split(r.URL.Path, "/")
	n := len(parts)
	if n < 5 || parts[n-4] != "api" || parts[n-3] != "devices" || parts[n-1] != "queue" {
		refuse(404, 5, "queue endpoint not found")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		refuse(405, 3, "method must be POST")
		return
	}
	want := sha256.Sum256([]byte("Bearer " + d.key))
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		refuse(401, 16, "invalid bearer key")
		return
	}
	eui := strings.ToUpper(parts[n-2])
	if !d.devices[eui] {
		refuse(404, 5, "unknown DevEUI")
		return
	}
	var body struct {
		Item *queueItem `json:"queueItem"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil || json.Unmarshal(data, &body) != nil || body.Item == nil {
		refuse(400, 3, "malformed queueItem body")
		return
	}
	item := *body.Item
	decoded, err := base64.StdEncoding.Strict().DecodeString(item.Data)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != item.Data || !strings.EqualFold(item.DevEUI, eui) || item.FPort < 1 || item.FPort > 223 {
		refuse(400, 3, "invalid devEui, fPort or standard base64 data")
		return
	}
	if item.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, item.ExpiresAt); err != nil {
			refuse(400, 3, "invalid expiresAt")
			return
		}
	}
	item.id, err = randomUUID()
	if err != nil {
		refuse(500, 13, "could not generate queue id")
		return
	}
	item.DevEUI = eui
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		refuse(http.StatusConflict, 9, "the simulated network server has stopped taking downlinks")
		return
	}
	d.queued++
	if item.Confirmed && d.ack != "none" {
		d.pending[eui] = append(d.pending[eui], item)
	}
	d.mu.Unlock()
	d.log(struct {
		Event     string `json:"event"`
		DevEUI    string `json:"devEui"`
		ID        string `json:"id"`
		FPort     int    `json:"fPort"`
		Hex       string `json:"hex"`
		Confirmed bool   `json:"confirmed"`
		ExpiresAt string `json:"expiresAt,omitempty"`
	}{"downlink_queued", eui, item.id, item.FPort, hex.EncodeToString(decoded), item.Confirmed, item.ExpiresAt})
	_ = json.NewEncoder(w).Encode(map[string]string{"id": item.id})
}

func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

func (d *Downlink) snapshot(eui string) []queueItem {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	eui = strings.ToUpper(eui)
	items := d.pending[eui]
	delete(d.pending, eui)
	return items
}

// closeIfDrained atomically stops acceptance when there is nothing left to report.
func (d *Downlink) closeIfDrained() bool {
	if d == nil {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pending) > 0 {
		return false
	}
	d.closed = true
	return true
}

func (d *Downlink) count() int { d.mu.Lock(); defer d.mu.Unlock(); return d.queued }

func (d *Downlink) reports(c Config, item queueItem, at time.Time, post func(Request) int, sum *Summary) {
	n := d.counters[item.DevEUI]
	d.counters[item.DevEUI]++
	for _, kind := range []string{"txack", "ack"} {
		msg := map[string]any{
			"time": at.UTC().Format(time.RFC3339Nano), "queueItemId": item.id, "fCntDown": n,
			"deviceInfo": map[string]any{"tenantName": "sim", "applicationName": "sim", "deviceProfileName": "sim", "deviceName": "sim-" + strings.ToLower(item.DevEUI), "devEui": item.DevEUI},
		}
		log := map[string]any{"event": "downlink_reported", "devEui": item.DevEUI, "id": item.id, "report": kind}
		if kind == "txack" {
			msg["downlinkId"] = n
			msg["gatewayId"] = gatewayID(0)
			msg["txInfo"] = map[string]int{"frequency": 868100000, "power": 14}
		} else {
			msg["deduplicationId"] = uuidOf(item.id, "ack")
			msg["acknowledged"] = d.ack == "ack"
			log["acknowledged"] = d.ack == "ack"
		}
		body, _ := json.Marshal(msg)
		status := post(Request{URL: strings.TrimRight(c.URL, "/") + "?event=" + kind, ContentType: "application/json", Body: body})
		sum.Reported[kind][fmt.Sprint(status)]++
		log["status"] = status
		d.log(log)
	}
}
