// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
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
	Mqtt string `json:"mqtt"`
	Http string `json:"http"`
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
func checkSnippetEvent(t *testing.T, c snippetCase, event *model.UnresolvedEvent, built interface{}) {
	t.Helper()
	if event.Device != c.Input.DeviceToken {
		t.Errorf("event device = %q, want %q", event.Device, c.Input.DeviceToken)
	}
	if event.EventType != eventtype.Measurement {
		t.Errorf("event type = %v, want Measurement", event.EventType)
	}
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
			checkSnippetEvent(t, c, gotEvent, gotPayload)
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
			checkSnippetEvent(t, c, event, built)
		})
	}
}
