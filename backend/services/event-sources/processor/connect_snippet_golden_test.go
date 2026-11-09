// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/config"
	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// The console's connectivity helper shows a user a ready-to-run mosquitto_pub and curl for
// a device. Its rendered commands are frozen in this file, which the console's own test
// holds its builder to byte for byte (connectSnippet.test.ts). This test is the other half:
// it takes those commands apart the way a shell would and runs what they send through this
// service's real ingest code, so a snippet the platform would refuse fails here rather than
// on a user's first attempt. device-management runs the same file through credential
// parsing and measurement resolution (its processor/connect_snippet_golden_test.go).
const connectSnippetCases = "../../../../frontend/testdata/connect-snippets/cases.json"

// The placeholders the console leaves for what it cannot know, and what this test puts
// in their place. They are the console's strings: a renamed placeholder is caught by the
// substitution leaving a '<' behind.
const (
	snippetInstancePlaceholder = "<instance-id>"
	snippetInstance            = "inst-1"
)

type snippetCase struct {
	Name  string `json:"name"`
	Input struct {
		Tenant      string `json:"tenant"`
		DeviceToken string `json:"deviceToken"`
		Credential  struct {
			CredentialType string `json:"credentialType"`
			CredentialId   string `json:"credentialId"`
		} `json:"credential"`
	} `json:"input"`
	Mqtt       string `json:"mqtt"`
	Http       string `json:"http"`
	MqttUplink string `json:"mqttUplink"`
	HttpUplink string `json:"httpUplink"`
}

func loadSnippetCases(t *testing.T) []snippetCase {
	t.Helper()
	raw, err := os.ReadFile(connectSnippetCases)
	// Fail, never skip: a moved file must not turn this into a test of nothing.
	if err != nil {
		t.Fatalf("read %s: %v", connectSnippetCases, err)
	}
	var cases []snippetCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 6 {
		t.Fatalf("%s holds %d cases; an emptied file would pass every loop below", connectSnippetCases, len(cases))
	}
	return cases
}

// shellWords splits a command the way a POSIX shell would for the subset the console
// emits: words separated by blanks, single-quoted strings, a backslash-newline joining
// lines, and a backslash quoting the next character. Anything else a shell would act on
// ($, <, >, |, ;, &, `, a double quote, a glob) outside quotes FAILS the test: a snippet
// that leaves one of those bare does not send what it says.
func shellWords(t *testing.T, cmd string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				t.Fatalf("unterminated single quote in %q", cmd)
			}
			cur.WriteString(cmd[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case c == '\\':
			if i+1 >= len(cmd) {
				t.Fatalf("trailing backslash in %q", cmd)
			}
			i++
			if cmd[i] == '\n' {
				continue
			}
			cur.WriteByte(cmd[i])
			inWord = true
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.IndexByte("$<>|;&`\"*?[]#~(){}", c) >= 0:
			t.Fatalf("unquoted shell metacharacter %q at %d in %q", c, i, cmd)
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// flagValues maps each flag in argv to its values, in order. Every flag the console emits
// takes exactly one value; a bare positional (curl's URL) is filed under "".
func flagValues(t *testing.T, argv []string, valueless ...string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	skip := map[string]bool{}
	for _, f := range valueless {
		skip[f] = true
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			out[""] = append(out[""], a)
			continue
		}
		if skip[a] {
			out[a] = append(out[a], "")
			continue
		}
		if i+1 >= len(argv) {
			t.Fatalf("flag %s has no value in %v", a, argv)
		}
		out[a] = append(out[a], argv[i+1])
		i++
	}
	return out
}

func one(t *testing.T, flags map[string][]string, name string) string {
	t.Helper()
	if len(flags[name]) != 1 {
		t.Fatalf("want exactly one %q, got %v", name, flags[name])
	}
	return flags[name][0]
}

func fillInstance(s string) string {
	return strings.ReplaceAll(s, snippetInstancePlaceholder, snippetInstance)
}

// checkSnippetEvent asserts a decoded event is the device's own Measurement, carrying the
// credential the snippet was built for, with at least one reading.
func checkSnippetEvent(t *testing.T, c snippetCase, viaMqtt bool, event *model.UnresolvedEvent, built interface{}) {
	t.Helper()
	if event.Device != c.Input.DeviceToken {
		t.Errorf("event device = %q, want %q", event.Device, c.Input.DeviceToken)
	}
	if event.EventType != eventtype.Measurement {
		t.Errorf("event type = %v, want Measurement", event.EventType)
	}
	if viaMqtt {
		// The platform MQTT broker authenticated the connection (-u/-P) and the capture source
		// REFUSES a body that carries a credential, so the MQTT snippet's body carries none.
		if credentialInBody(event) {
			t.Error("the MQTT snippet's body carries a credential member, which the platform broker refuses")
		}
	} else {
		checkSnippetCredential(t, c, event)
	}
	checkSnippetPayload(t, built)
}

// checkSnippetCredential holds the HTTP snippet to presenting the credential in the body, the
// only authentication that transport has.
func checkSnippetCredential(t *testing.T, c snippetCase, event *model.UnresolvedEvent) {
	t.Helper()
	if event.CredentialType == nil || *event.CredentialType != c.Input.Credential.CredentialType {
		t.Errorf("credential type = %v, want %q", event.CredentialType, c.Input.Credential.CredentialType)
	}
	if event.CredentialId == nil || *event.CredentialId != c.Input.Credential.CredentialId {
		t.Errorf("credential id = %v, want %q", event.CredentialId, c.Input.Credential.CredentialId)
	}
	// An MQTT_BASIC event carries the password too, as the console's placeholder; an access
	// token is the whole credential and must not.
	hasSecret := event.CredentialSecret != nil
	if wantSecret := c.Input.Credential.CredentialType == "MQTT_BASIC"; hasSecret != wantSecret {
		t.Errorf("credential secret present = %t, want %t", hasSecret, wantSecret)
	}
}

func checkSnippetPayload(t *testing.T, built interface{}) {
	t.Helper()
	payload, ok := built.(*model.UnresolvedMeasurementsPayload)
	if !ok {
		t.Fatalf("payload is %T", built)
	}
	readings := 0
	for _, e := range payload.Entries {
		readings += len(e.Measurements)
	}
	if readings == 0 {
		t.Error("the snippet sends no reading")
	}
}

// The curl is POSTed, as the shell would send it, to this service's real ingest route.
func TestConnectSnippetCurlIsAcceptedByHttpIngest(t *testing.T) {
	for _, c := range loadSnippetCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			argv := shellWords(t, c.Http)
			if len(argv) == 0 || argv[0] != "curl" {
				t.Fatalf("not a curl: %v", argv)
			}
			flags := flagValues(t, argv, "-i")
			if one(t, flags, "-X") != http.MethodPost {
				t.Fatalf("method = %v", flags["-X"])
			}
			if ct := one(t, flags, "-H"); !strings.EqualFold(ct, "Content-Type: application/json") {
				t.Errorf("header = %q", ct)
			}
			target, err := url.Parse(fillInstance(one(t, flags, "")))
			if err != nil {
				t.Fatal(err)
			}
			if target.Port() != strconv.Itoa(config.DefaultHttpIngestPort) {
				t.Errorf("port = %q, want the HTTP ingest listener's %d", target.Port(), config.DefaultHttpIngestPort)
			}
			body := one(t, flags, "-d")

			// newTestHttpSource's capture keeps the event but not the payload built beside
			// it, and the payload is what the readings are counted from.
			var gotTenant string
			var gotEvent *model.UnresolvedEvent
			var gotPayload interface{}
			var failErr error
			es, err := NewHttpEventSource("http-test", map[string]string{}, snippetInstance, config.HttpIngest{},
				NewJsonDecoder(map[string]string{}),
				func(string, []byte) {},
				func(_ string, tenant string, event *model.UnresolvedEvent, payload interface{}, _ string) error {
					gotTenant, gotEvent, gotPayload = tenant, event, payload
					return nil
				},
				func(_ string, _ string, _ []byte, err error) error {
					failErr = err
					return nil
				},
				nil, admitAllReadings, admitAll, nil)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, target.Path, strings.NewReader(body))
			rec := httptest.NewRecorder()
			es.handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Fatalf("POST %s answered %d: %s", target.Path, rec.Code, rec.Body.String())
			}
			if failErr != nil || gotEvent == nil {
				t.Fatalf("decoded=%t failure=%v", gotEvent != nil, failErr)
			}
			if gotTenant != c.Input.Tenant {
				t.Errorf("tenant = %q, want %q", gotTenant, c.Input.Tenant)
			}
			checkSnippetEvent(t, c, false, gotEvent, gotPayload)
		})
	}
}

// The mosquitto_pub publishes on the device's own events topic — the one the broker grants
// it and this service ingests — a message the real decoder accepts.
func TestConnectSnippetMqttPublishesTheDevicesOwnEvent(t *testing.T) {
	cfg := config.EventSourcesConfiguration{}
	cfg.ApplyDefaults()
	defaultPort := cfg.EventSources[0].Configuration["port"]

	for _, c := range loadSnippetCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			argv := shellWords(t, c.Mqtt)
			if len(argv) == 0 || argv[0] != "mosquitto_pub" {
				t.Fatalf("not a mosquitto_pub: %v", argv)
			}
			flags := flagValues(t, argv)
			if p := one(t, flags, "-p"); p != defaultPort {
				t.Errorf("port = %q, want the MQTT listener's %q", p, defaultPort)
			}
			if len(flags["--cafile"]) != 1 {
				t.Error("the listener is TLS; the snippet must name the instance CA")
			}
			topic := fillInstance(one(t, flags, "-t"))
			want := messaging.SubjectToMqttTopic(messaging.DeviceEventsSubject(snippetInstance, c.Input.Tenant, c.Input.DeviceToken))
			if topic != want {
				t.Fatalf("topic = %q, want the device's own events topic %q", topic, want)
			}
			if tenant, ok := tenantFromTopic(topic); !ok || tenant != c.Input.Tenant {
				t.Errorf("tenantFromTopic = %q, %t", tenant, ok)
			}

			event, built, err := NewJsonDecoder(nil).Decode([]byte(one(t, flags, "-m")), time.Time{})
			if err != nil {
				t.Fatalf("the decoder refuses the message: %v", err)
			}
			// The topic's token and the body's device must agree, or the event is refused.
			if got := deviceFromTopic(topic); got != event.Device {
				t.Errorf("topic names %q, body names %q", got, event.Device)
			}
			checkSnippetEvent(t, c, true, event, built)
		})
	}
}

// The raw-payload curl is sent, as the shell would send it, through this service's real
// device-uplink route: the path, the credential headers and the body (a file, so the test
// stands in bytes for it) must be what the route accepts and hands on.
func TestConnectSnippetRawPayloadCurlIsAcceptedByTheDeviceUplinkRoute(t *testing.T) {
	for _, c := range loadSnippetCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			argv := shellWords(t, c.HttpUplink)
			if len(argv) == 0 || argv[0] != "curl" {
				t.Fatalf("not a curl: %v", argv)
			}
			flags := flagValues(t, argv, "-i")
			if one(t, flags, "-X") != http.MethodPost {
				t.Fatalf("method = %v", flags["-X"])
			}
			headers := map[string]string{}
			for _, h := range flags["-H"] {
				name, value, ok := strings.Cut(h, ": ")
				if !ok {
					t.Fatalf("malformed header %q", h)
				}
				headers[name] = value
			}
			if headers["Content-Type"] != "application/octet-stream" {
				t.Errorf("Content-Type = %q", headers["Content-Type"])
			}
			if one(t, flags, "--data-binary") != "@payload.bin" {
				t.Errorf("body = %v, want the contents of payload.bin", flags["--data-binary"])
			}
			// The harness's instance id is "inst", so the placeholder is filled with that.
			target, err := url.Parse(strings.ReplaceAll(one(t, flags, ""), snippetInstancePlaceholder, "inst"))
			if err != nil {
				t.Fatal(err)
			}
			if target.Port() != strconv.Itoa(config.DefaultHttpIngestPort) {
				t.Errorf("port = %q, want the HTTP ingest listener's %d", target.Port(), config.DefaultHttpIngestPort)
			}
			wantPath := "/inst" + "/" + c.Input.Tenant + "/devices/" + c.Input.DeviceToken + "/uplink"
			if target.Path != wantPath {
				t.Fatalf("path = %q, want %q", target.Path, wantPath)
			}

			// The harness's route is the real one; its plan fake authenticates the credential the
			// snippet presents and reports a frame decoder.
			h := newDeviceHarness(t, fplan())
			h.plans.valid[c.Input.Credential.CredentialId] = true
			rec := h.post(target.Path, headers["Content-Type"], "", headers)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("POST %s answered %d: %s", target.Path, rec.Code, rec.Body.String())
			}
			if len(h.published) != 1 {
				t.Fatalf("published %d uplinks", len(h.published))
			}
			e := h.published[0]
			if !bytes.Equal(h.payloads[0].Frame, []byte("")) {
				t.Errorf("the route carried %x, not the bytes the snippet's file holds", h.payloads[0].Frame)
			}
			if e.Device != c.Input.DeviceToken {
				t.Errorf("device = %q, want %q", e.Device, c.Input.DeviceToken)
			}
			if e.CredentialType == nil || *e.CredentialType != c.Input.Credential.CredentialType {
				t.Errorf("credential type = %v, want %q", e.CredentialType, c.Input.Credential.CredentialType)
			}
			if e.CredentialSecret != nil && *e.CredentialSecret != "<password>" {
				t.Errorf("credential secret = %q, want the console's placeholder", *e.CredentialSecret)
			}
			if e.CredentialId == nil || *e.CredentialId != c.Input.Credential.CredentialId {
				t.Errorf("credential id = %v, want %q", e.CredentialId, c.Input.Credential.CredentialId)
			}
			if wantSecret := c.Input.Credential.CredentialType == "MQTT_BASIC"; (e.CredentialSecret != nil) != wantSecret {
				t.Errorf("credential secret present = %t, want %t", e.CredentialSecret != nil, wantSecret)
			}
		})
	}
}

// The raw-payload mosquitto_pub publishes a file on the device's own uplink topic, the one the
// broker grants it (when the operator opts in) and this service captures, with the same
// connection arguments the events command uses and no message body of its own.
func TestConnectSnippetRawPayloadMqttPublishesOnTheDevicesOwnUplinkTopic(t *testing.T) {
	for _, c := range loadSnippetCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			argv := shellWords(t, c.MqttUplink)
			if len(argv) == 0 || argv[0] != "mosquitto_pub" {
				t.Fatalf("not a mosquitto_pub: %v", argv)
			}
			flags := flagValues(t, argv)
			if one(t, flags, "-f") != "payload.bin" {
				t.Errorf("message = %v, want the contents of payload.bin", flags["-f"])
			}
			if len(flags["-m"]) != 0 {
				t.Error("a raw payload command must not carry a message body of its own")
			}
			topic := fillInstance(one(t, flags, "-t"))
			want := messaging.SubjectToMqttTopic(messaging.DeviceUplinkSubject(snippetInstance, c.Input.Tenant, c.Input.DeviceToken))
			if topic != want {
				t.Fatalf("topic = %q, want the device's own uplink topic %q", topic, want)
			}
			if tenant, ok := tenantFromTopic(topic); !ok || tenant != c.Input.Tenant {
				t.Errorf("tenantFromTopic = %q, %t", tenant, ok)
			}
			// The connection arguments are the events command's, so the broker accepts the same
			// client id and username.
			events := flagValues(t, shellWords(t, c.Mqtt))
			for _, f := range []string{"-i", "-u", "-P", "-h", "-p", "--cafile"} {
				if strings.Join(flags[f], "|") != strings.Join(events[f], "|") {
					t.Errorf("%s = %v, the events command has %v", f, flags[f], events[f])
				}
			}
			device, ok := messaging.ParseDeviceUplinkSubject(strings.ReplaceAll(topic, "/", "."))
			if !ok || device != c.Input.DeviceToken {
				t.Errorf("the capture stream reads the device as %q, %t", device, ok)
			}
		})
	}
}
