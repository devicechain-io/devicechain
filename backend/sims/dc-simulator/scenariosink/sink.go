// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package scenariosink is the receiving end of a scenario's deliveries OUT of the platform: an HTTP
// endpoint for webhooks, an MQTT endpoint that accepts publishes, and an SMTP endpoint that accepts
// mail. It records what arrives, exactly as it arrived, and serves the record on GET /received for a
// verifier to judge. It judges nothing itself: a sink that checked signatures would be a second
// verifier nobody verifies.
//
// It runs as a pod inside the cluster (cmd/scenario-sink, deployed by hack/harborpulse-rig.sh), so
// the platform reaches it without any route out of the cluster. Everything it holds is bounded.
//
// The MQTT and SMTP endpoints are the least a client of each protocol needs to deliver, not
// general servers: MQTT 3.1.1 CONNECT, PUBLISH at QoS 0, 1 and 2, PINGREQ, SUBSCRIBE (granted at
// QoS 0, nothing is ever forwarded) and DISCONNECT; SMTP HELO/EHLO, MAIL, RCPT, DATA, RSET, NOOP and
// QUIT, with no TLS and no authentication.
package scenariosink

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MaxRecords bounds each record list; the oldest are dropped past it.
const MaxRecords = 20000

// maxBody bounds one recorded webhook body, MQTT payload or mail.
const maxBody = 256 << 10

// Webhook is one HTTP request the sink received.
type Webhook struct {
	At      time.Time           `json:"at"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	// Body is the exact bytes received, as a string (JSON escapes anything not UTF-8-safe).
	Body string `json:"body"`
}

// Message is one MQTT publish the sink received.
type Message struct {
	At      time.Time `json:"at"`
	Topic   string    `json:"topic"`
	QoS     int       `json:"qos"`
	Payload string    `json:"payload"`
}

// Mail is one message the SMTP endpoint accepted.
type Mail struct {
	At   time.Time `json:"at"`
	From string    `json:"from"`
	To   []string  `json:"to"`
	Data string    `json:"data"`
}

// Received is everything the sink holds, as GET /received serves it.
type Received struct {
	Webhooks []Webhook `json:"webhooks"`
	Messages []Message `json:"messages"`
	Mail     []Mail    `json:"mail"`
	// Dropped counts records evicted past MaxRecords, so a verifier knows the record is partial.
	Dropped int `json:"dropped"`
}

// Sink records deliveries.
type Sink struct {
	mu  sync.Mutex
	rec Received
	now func() time.Time
}

// New returns an empty sink.
func New() *Sink { return &Sink{now: time.Now} }

func bounded[T any](list []T, item T, dropped *int) []T {
	list = append(list, item)
	if len(list) > MaxRecords {
		*dropped += len(list) - MaxRecords
		list = append([]T(nil), list[len(list)-MaxRecords:]...)
	}
	return list
}

// Snapshot copies what the sink holds.
func (s *Sink) Snapshot() Received {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Received{
		Webhooks: append([]Webhook{}, s.rec.Webhooks...),
		Messages: append([]Message{}, s.rec.Messages...),
		Mail:     append([]Mail{}, s.rec.Mail...),
		Dropped:  s.rec.Dropped,
	}
}

// ServeHTTP records a POST anywhere but /received and /healthz, serves the record on GET /received,
// and answers 200 on GET /healthz.
func (s *Sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/received":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Snapshot())
	case r.Method == http.MethodGet && r.URL.Path == "/healthz":
		_, _ = io.WriteString(w, "ok\n")
	case r.Method == http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
		if err != nil || len(body) > maxBody {
			http.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
			return
		}
		h := map[string][]string{}
		for k, v := range r.Header {
			h[k] = append([]string(nil), v...)
		}
		s.mu.Lock()
		s.rec.Webhooks = bounded(s.rec.Webhooks, Webhook{At: s.now().UTC(), Path: r.URL.Path, Headers: h, Body: string(body)}, &s.rec.Dropped)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ---- MQTT ----------------------------------------------------------------------------------------

// ServeMQTT accepts MQTT connections on l until it is closed.
func (s *Sink) ServeMQTT(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.mqttConn(c)
	}
}

func readRemainingLength(r *bufio.Reader) (int, error) {
	n, mult := 0, 1
	for i := 0; i < 4; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		n += int(b&0x7f) * mult
		if b&0x80 == 0 {
			return n, nil
		}
		mult *= 128
	}
	return 0, errors.New("mqtt: malformed remaining length")
}

func (s *Sink) mqttConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Minute))
		first, err := r.ReadByte()
		if err != nil {
			return
		}
		n, err := readRemainingLength(r)
		if err != nil || n > maxBody+1024 {
			return
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		var reply []byte
		switch first >> 4 {
		case 1: // CONNECT
			reply = []byte{0x20, 0x02, 0x00, 0x00}
		case 3: // PUBLISH
			reply = s.mqttPublish(first, body)
			if reply == nil && (first>>1)&3 != 0 {
				return
			}
		case 6: // PUBREL
			if len(body) >= 2 {
				reply = []byte{0x70, 0x02, body[0], body[1]}
			}
		case 8: // SUBSCRIBE: grant QoS 0 to each filter
			if len(body) < 2 {
				return
			}
			granted := 0
			for i := 2; i+2 <= len(body); {
				l := int(binary.BigEndian.Uint16(body[i:]))
				i += 2 + l + 1
				granted++
			}
			reply = append([]byte{0x90, byte(2 + granted), body[0], body[1]}, make([]byte, granted)...)
		case 12: // PINGREQ
			reply = []byte{0xd0, 0x00}
		case 14: // DISCONNECT
			return
		}
		if reply != nil {
			if _, err := c.Write(reply); err != nil {
				return
			}
		}
	}
}

// mqttPublish records one PUBLISH and returns its acknowledgement (nil for QoS 0, and for a
// malformed packet, which ends the connection when it needed one).
func (s *Sink) mqttPublish(first byte, body []byte) []byte {
	qos := int(first>>1) & 3
	if len(body) < 2 {
		return nil
	}
	tl := int(binary.BigEndian.Uint16(body))
	if 2+tl > len(body) {
		return nil
	}
	topic := string(body[2 : 2+tl])
	rest := body[2+tl:]
	var id []byte
	if qos > 0 {
		if len(rest) < 2 {
			return nil
		}
		id, rest = rest[:2], rest[2:]
	}
	s.mu.Lock()
	s.rec.Messages = bounded(s.rec.Messages, Message{At: s.now().UTC(), Topic: topic, QoS: qos, Payload: string(rest)}, &s.rec.Dropped)
	s.mu.Unlock()
	switch qos {
	case 1:
		return []byte{0x40, 0x02, id[0], id[1]}
	case 2:
		return []byte{0x50, 0x02, id[0], id[1]}
	}
	return nil
}

// ---- SMTP ----------------------------------------------------------------------------------------

// ServeSMTP accepts SMTP connections on l until it is closed.
func (s *Sink) ServeSMTP(l net.Listener) error {
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go s.smtpConn(c)
	}
}

func (s *Sink) smtpConn(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	say := func(line string) bool {
		_, _ = w.WriteString(line + "\r\n")
		return w.Flush() == nil
	}
	if !say("220 scenario-sink ESMTP") {
		return
	}
	var from string
	var to []string
	for {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Minute))
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"):
			_, _ = w.WriteString("250-scenario-sink\r\n")
			if !say("250 8BITMIME") {
				return
			}
		case strings.HasPrefix(verb, "HELO"):
			say("250 scenario-sink")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			from, to = addr(line[len("MAIL FROM:"):]), nil
			say("250 OK")
		case strings.HasPrefix(verb, "RCPT TO:"):
			to = append(to, addr(line[len("RCPT TO:"):]))
			say("250 OK")
		case verb == "DATA":
			if len(to) == 0 {
				say("503 RCPT first")
				continue
			}
			say("354 end with <CR><LF>.<CR><LF>")
			data, ok := readData(r)
			if !ok {
				say("552 message too large")
				return
			}
			s.mu.Lock()
			s.rec.Mail = bounded(s.rec.Mail, Mail{At: s.now().UTC(), From: from, To: to, Data: data}, &s.rec.Dropped)
			s.mu.Unlock()
			from, to = "", nil
			say("250 OK")
		case verb == "RSET":
			from, to = "", nil
			say("250 OK")
		case verb == "NOOP":
			say("250 OK")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

// addr is the address inside a MAIL FROM or RCPT TO argument.
func addr(arg string) string {
	arg = strings.TrimSpace(arg)
	if i := strings.IndexByte(arg, ' '); i >= 0 {
		arg = arg[:i]
	}
	return strings.Trim(arg, "<>")
}

// readData reads a DATA section up to the lone ".", undoing dot-stuffing.
func readData(r *bufio.Reader) (string, bool) {
	var b strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", false
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "." {
			return b.String(), true
		}
		if strings.HasPrefix(trimmed, "..") {
			trimmed = trimmed[1:]
		}
		if b.Len()+len(trimmed) > maxBody {
			return "", false
		}
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
}

// Fetch reads a sink's record from its base URL.
func Fetch(c *http.Client, base string) (Received, error) {
	resp, err := c.Get(strings.TrimRight(base, "/") + "/received")
	if err != nil {
		return Received{}, fmt.Errorf("read the sink: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Received{}, fmt.Errorf("read the sink: HTTP %d", resp.StatusCode)
	}
	var rec Received
	if err := json.NewDecoder(resp.Body).Decode(&rec); err != nil {
		return Received{}, fmt.Errorf("decode the sink's record: %w", err)
	}
	return rec, nil
}
