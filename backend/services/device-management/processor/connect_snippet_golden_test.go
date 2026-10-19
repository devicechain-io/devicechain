// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
	"gorm.io/datatypes"

	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	esprocessor "github.com/devicechain-io/dc-event-sources/processor"
)

// The console's connectivity helper shows a user a ready-to-run mosquitto_pub for a device,
// built from the device's credential and its profile's PUBLISHED metrics. The rendered
// commands are frozen in this file; the console's test holds its builder to it byte for
// byte, and event-sources runs it through the real HTTP ingest route and JSON decoder
// (processor/connect_snippet_golden_test.go there). This test is the device-management
// half: the broker's auth-callout must accept the connection the command opens, and the
// resolver must store every reading it sends — a value the profile would refuse, or a key
// the resolver would quietly drop, fails here instead of on the user's first try.
const connectSnippetCases = "../../../../frontend/testdata/connect-snippets/cases.json"

const (
	snippetInstancePlaceholder = "<instance-id>"
	snippetInstance            = "inst-1"
	snippetPasswordPlaceholder = "<password>"
	snippetPassword            = "a-password-of-sixteen-bytes-or-more"
)

type snippetMetric struct {
	MetricKey     string   `json:"metricKey"`
	DataType      string   `json:"dataType"`
	MinValue      *float64 `json:"minValue"`
	MaxValue      *float64 `json:"maxValue"`
	AllowedValues []string `json:"allowedValues"`
}

type snippetCase struct {
	Name  string `json:"name"`
	Input struct {
		Tenant      string `json:"tenant"`
		DeviceToken string `json:"deviceToken"`
		Credential  struct {
			CredentialType string `json:"credentialType"`
			CredentialId   string `json:"credentialId"`
		} `json:"credential"`
		Metrics []snippetMetric `json:"metrics"`
	} `json:"input"`
	Mqtt string `json:"mqtt"`
}

// shellWords splits a command the way a POSIX shell would for the subset the console
// emits (blanks, single quotes, backslash escapes and line joins), and fails on any bare
// character a shell would act on. event-sources' copy is the same function.
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

// mqttFlags maps each mosquitto_pub flag to its single value.
func mqttFlags(t *testing.T, argv []string) map[string]string {
	t.Helper()
	if len(argv) == 0 || argv[0] != "mosquitto_pub" {
		t.Fatalf("not a mosquitto_pub: %v", argv)
	}
	out := map[string]string{}
	for i := 1; i < len(argv); i += 2 {
		if !strings.HasPrefix(argv[i], "-") || i+1 >= len(argv) {
			t.Fatalf("unexpected argument %q in %v", argv[i], argv)
		}
		if _, dup := out[argv[i]]; dup {
			t.Fatalf("flag %s given twice", argv[i])
		}
		out[argv[i]] = argv[i+1]
	}
	return out
}

// profileFor builds the resolution ingest would validate the device's measurements
// against, from the published metrics the console was given.
func profileFor(t *testing.T, metrics []snippetMetric) model.ProfileResolution {
	t.Helper()
	defs := make([]*model.MetricDefinition, 0, len(metrics))
	for i, m := range metrics {
		def := &model.MetricDefinition{MetricKey: m.MetricKey, DataType: m.DataType}
		def.ID = uint(i + 1)
		if m.MinValue != nil {
			def.MinValue = sql.NullFloat64{Float64: *m.MinValue, Valid: true}
		}
		if m.MaxValue != nil {
			def.MaxValue = sql.NullFloat64{Float64: *m.MaxValue, Valid: true}
		}
		if m.AllowedValues != nil {
			raw, err := json.Marshal(m.AllowedValues)
			if err != nil {
				t.Fatal(err)
			}
			enum := datatypes.JSON(raw)
			def.Enum = &enum
		}
		defs = append(defs, def)
	}
	return model.NewProfileResolution(model.ProfileScope{}, defs)
}

func TestConnectSnippetIsAuthorizedAndStoredWhole(t *testing.T) {
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

	fill := strings.NewReplacer(snippetInstancePlaceholder, snippetInstance, snippetPasswordPlaceholder, snippetPassword)
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			flags := mqttFlags(t, shellWords(t, c.Mqtt))

			// The connection: what the auth-callout reads from the CONNECT.
			tenant, presented, ok := parseDeviceCredential(fill.Replace(flags["-u"]), fill.Replace(flags["-P"]))
			if !ok {
				t.Fatalf("the auth-callout refuses username %q", flags["-u"])
			}
			if tenant != c.Input.Tenant {
				t.Errorf("connect tenant = %q, want %q", tenant, c.Input.Tenant)
			}
			if presented.CredentialType != c.Input.Credential.CredentialType || presented.CredentialId != c.Input.Credential.CredentialId {
				t.Errorf("connect presents %s %q, want %s %q", presented.CredentialType, presented.CredentialId,
					c.Input.Credential.CredentialType, c.Input.Credential.CredentialId)
			}
			required, err := messaging.DeviceClientID(snippetInstance, c.Input.Tenant, c.Input.DeviceToken)
			if err != nil {
				t.Fatal(err)
			}
			if clientID := fill.Replace(flags["-i"]); !messaging.DeviceClientIDMatches(clientID, required) {
				t.Errorf("the auth-callout refuses client id %q (requires %q)", clientID, required)
			}

			// The event: decoded by event-sources, then validated and resolved here.
			event, built, err := esprocessor.NewJsonDecoder(nil).Decode([]byte(flags["-m"]), time.Time{})
			if err != nil {
				t.Fatalf("the decoder refuses the message: %v", err)
			}
			event.Payload = built
			sent, ok := built.(*esmodel.UnresolvedMeasurementsPayload)
			if !ok {
				t.Fatalf("payload is %T", built)
			}
			readings := 0
			for _, e := range sent.Entries {
				readings += len(e.Measurements)
			}

			profile := profileFor(t, c.Input.Metrics)
			if reason, err := validateMeasurements(profile, event); err != nil {
				t.Fatalf("the profile refuses the event (reason %d): %v", reason, err)
			}
			rez := &EventResolver{}
			resolved, err := rez.ResolveMeasurementsEventPayload(context.Background(),
				&model.Device{}, profile, event)
			if err != nil {
				t.Fatal(err)
			}
			stored := 0
			for _, e := range resolved.(*resolvedevent.ResolvedMeasurementsPayload).Entries {
				stored += len(e.Entries)
			}
			if readings == 0 || stored != readings {
				t.Errorf("sent %d readings, the resolver keeps %d", readings, stored)
			}
		})
	}
}
