// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"crypto/tls"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/devicechain-io/dc-simulator/sim"
)

// utf8BOM is the byte order mark a spreadsheet export puts before the header.
var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

// DevicePlaceholder is the token a topic template carries where the device goes.
const DevicePlaceholder = "{device}"

// DefaultTopicTemplate is where a target fleet publishes when neither the credential list nor
// the operator names a topic.
const DefaultTopicTemplate = "devices/{device}/events"

// Credential is one device of a bring-your-own fleet: who it is on the wire and how it
// authenticates to a broker that is not a DeviceChain gateway.
type Credential struct {
	// Device is the identity the run files its events under (it is also what the event body
	// names), unique within the list.
	Device string
	// ClientID is the MQTT client id; Device when the list leaves it empty.
	ClientID string
	// Username/Password authenticate the CONNECT. A "token" column is a username with an empty
	// password, the shape most device brokers take a bearer token in.
	Username, Password string
	// Cert, when set, is the client certificate offered in the TLS handshake.
	Cert *tls.Certificate
	// Topic is the topic this device publishes to.
	Topic string
}

// Authentication names how a credential authenticates, without revealing it.
func (c Credential) Authentication() string {
	switch {
	case c.Cert != nil:
		return "x509"
	case c.Username != "" || c.Password != "":
		return "password"
	default:
		return "anonymous"
	}
}

// ParseCredentials reads a bring-your-own credential CSV. The first row is a header; the
// columns, in any order, are:
//
//	device     required: the device's identity, unique in the file
//	client_id  the MQTT client id (default: the device)
//	token      a bearer token, presented as the username with an empty password
//	username   a username (with password); not together with token
//	password   a password
//	cert_file  PEM client certificate, with key_file (the pair x509sim writes)
//	key_file   PEM private key for cert_file
//	topic      this device's publish topic (default: the template, with {device} replaced)
//
// A relative cert_file or key_file is read relative to baseDir. A row with none of token,
// username, password or cert_file connects anonymously, which the report states. A malformed
// row is an error naming its line: a rehearsal must drive the fleet it was given, not the part
// of it that parsed.
func ParseCredentials(r io.Reader, baseDir, topicTemplate string) ([]Credential, error) {
	if topicTemplate == "" {
		topicTemplate = DefaultTopicTemplate
	}
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("credential list is empty: want a header row and one row per device")
	}
	if err != nil {
		return nil, fmt.Errorf("credential list header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, utf8BOM)))
		switch name {
		case "device", "client_id", "token", "username", "password", "cert_file", "key_file", "topic":
		default:
			return nil, fmt.Errorf("credential list: unknown column %q (known: device, client_id, token, username, password, cert_file, key_file, topic)", h)
		}
		if _, dup := col[name]; dup {
			return nil, fmt.Errorf("credential list: column %q appears twice", name)
		}
		col[name] = i
	}
	if _, ok := col["device"]; !ok {
		return nil, fmt.Errorf("credential list: no \"device\" column")
	}

	var out []Credential
	devices, clients := map[string]int{}, map[string]int{}
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// A parse error leaves no record to take a position from (FieldPos panics on it),
			// so the line comes from the error itself.
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return nil, fmt.Errorf("credential list line %d: %w", pe.Line, err)
			}
			return nil, fmt.Errorf("credential list: %w", err)
		}
		line, _ := cr.FieldPos(0)
		get := func(name string) string {
			i, ok := col[name]
			if !ok || i >= len(rec) {
				return ""
			}
			return strings.TrimSpace(rec[i])
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue // a blank line
		}
		if len(rec) != len(header) {
			return nil, fmt.Errorf("credential list line %d: %d field(s), the header has %d: a truncated row would connect anonymously and a surplus field is ignored, so either way the rehearsal is not of the fleet written", line, len(rec), len(header))
		}
		c := Credential{Device: get("device"), ClientID: get("client_id"), Username: get("username"), Password: get("password"), Topic: get("topic")}
		if c.Device == "" {
			return nil, fmt.Errorf("credential list line %d: no device", line)
		}
		if err := validateDeviceName(c.Device); err != nil {
			return nil, fmt.Errorf("credential list line %d: %w", line, err)
		}
		if c.ClientID == "" {
			c.ClientID = c.Device
		}
		if tok := get("token"); tok != "" {
			if c.Username != "" || c.Password != "" {
				return nil, fmt.Errorf("credential list line %d (%s): token and username/password are two ways to authenticate: give one", line, c.Device)
			}
			c.Username = tok
		}
		if c.Password != "" && c.Username == "" {
			return nil, fmt.Errorf("credential list line %d (%s): a password with no username", line, c.Device)
		}
		certFile, keyFile := get("cert_file"), get("key_file")
		if (certFile == "") != (keyFile == "") {
			return nil, fmt.Errorf("credential list line %d (%s): cert_file and key_file go together", line, c.Device)
		}
		if certFile != "" {
			if c.Username != "" {
				return nil, fmt.Errorf("credential list line %d (%s): a client certificate and a token or password are two ways to authenticate: give one", line, c.Device)
			}
			pair, err := tls.LoadX509KeyPair(resolvePath(baseDir, certFile), resolvePath(baseDir, keyFile))
			if err != nil {
				return nil, fmt.Errorf("credential list line %d (%s): %w", line, c.Device, err)
			}
			c.Cert = &pair
		}
		if c.Topic == "" {
			c.Topic = strings.ReplaceAll(topicTemplate, DevicePlaceholder, c.Device)
		}
		if err := validatePublishTopic(c.Topic); err != nil {
			return nil, fmt.Errorf("credential list line %d (%s): %w", line, c.Device, err)
		}
		if prev, dup := devices[c.Device]; dup {
			return nil, fmt.Errorf("credential list line %d: device %q already on line %d", line, c.Device, prev)
		}
		if prev, dup := clients[c.ClientID]; dup {
			return nil, fmt.Errorf("credential list line %d: client id %q already on line %d (two sessions cannot share one: the broker would evict the first)", line, c.ClientID, prev)
		}
		devices[c.Device], clients[c.ClientID] = line, line
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("credential list has a header and no devices")
	}
	return out, nil
}

// LoadCredentials reads a credential CSV from a file.
func LoadCredentials(path, topicTemplate string) ([]Credential, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open credential list: %w", err)
	}
	defer f.Close()
	return ParseCredentials(f, filepath.Dir(path), topicTemplate)
}

func resolvePath(base, p string) string {
	if filepath.IsAbs(p) || base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// validateDeviceName keeps a device name usable as a topic level and a JSON identity.
func validateDeviceName(d string) error {
	if strings.ContainsAny(d, "+#/\x00") || strings.TrimSpace(d) != d {
		return fmt.Errorf("device %q must not contain + # / or surrounding space", d)
	}
	return nil
}

func validatePublishTopic(t string) error {
	if t == "" || strings.ContainsAny(t, "+#\x00") {
		return fmt.Errorf("publish topic %q must be non-empty and carry no wildcard", t)
	}
	return nil
}

// ValidateTopicTemplate rejects a template that cannot name a device's topic.
func ValidateTopicTemplate(t string) error {
	if !strings.Contains(t, DevicePlaceholder) {
		return fmt.Errorf("topic template %q has no %s: every device would publish to one topic and the read-back could not tell them apart by topic", t, DevicePlaceholder)
	}
	if strings.ContainsAny(t, "+#") {
		return fmt.Errorf("topic template %q must carry no wildcard", t)
	}
	return nil
}

// TapFilter is the subscription filter that covers every device a template names: the
// template with each {device} level replaced by the single-level wildcard.
func TapFilter(template string) (string, error) {
	if err := ValidateTopicTemplate(template); err != nil {
		return "", err
	}
	levels := strings.Split(template, "/")
	for i, l := range levels {
		switch {
		case l == DevicePlaceholder:
			levels[i] = "+"
		case strings.Contains(l, DevicePlaceholder):
			return "", fmt.Errorf("topic template %q has {device} inside a level: the tap filter cannot wildcard part of a level; pass --tap-topic", template)
		}
	}
	return strings.Join(levels, "/"), nil
}

// FilterMatches reports whether an MQTT subscription filter covers a topic name: "+" is one
// level and a trailing "#" is every level below.
func FilterMatches(filter, topic string) bool {
	f, t := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, l := range f {
		if l == "#" {
			return i == len(f)-1
		}
		if i >= len(t) || (l != "+" && l != t[i]) {
			return false
		}
	}
	return len(f) == len(t)
}

// NewTargetFleet builds a fleet whose sessions present the identities of a bring-your-own
// credential list to a broker that is not a DeviceChain gateway. cfg.Target must be set.
func NewTargetFleet(cfg Config, creds []Credential) (*Fleet, error) {
	if !cfg.Target {
		return nil, fmt.Errorf("a target fleet needs Config.Target: without it the sessions would claim DeviceChain identities")
	}
	cfg = cfg.withDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("no devices to connect")
	}
	broker, err := ResolveBroker(cfg.Broker, cfg.TLS)
	if err != nil {
		return nil, err
	}
	f := &Fleet{
		cfg: cfg, broker: broker,
		byToken:     make(map[string]*session, len(creds)),
		sem:         make(chan struct{}, cfg.ConnectConcurrency),
		tlsVersions: map[string]int64{},
	}
	f.ivLat.Store(&LatencyHist{})
	for i, c := range creds {
		if _, dup := f.byToken[c.Device]; dup {
			return nil, fmt.Errorf("device %q appears twice", c.Device)
		}
		if c.Cert != nil && !cfg.TLS {
			return nil, fmt.Errorf("device %q authenticates with a client certificate, which needs TLS: use an mqtts:// target", c.Device)
		}
		s := f.newSession(i, sim.DeviceInstance{Token: c.Device}, sessionIdentity{
			clientID: c.ClientID, username: c.Username, password: c.Password, topic: c.Topic, cert: c.Cert,
		})
		f.sessions = append(f.sessions, s)
		f.byToken[c.Device] = s
	}
	return f, nil
}
