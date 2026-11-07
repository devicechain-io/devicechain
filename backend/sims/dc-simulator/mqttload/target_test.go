// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package mqttload

import (
	"strings"
	"testing"
)

func parse(t *testing.T, csv string) ([]Credential, error) {
	t.Helper()
	return ParseCredentials(strings.NewReader(csv), "", "")
}

func TestParseCredentialsAuthenticationShapes(t *testing.T) {
	cs, err := parse(t, "device,token,username,password,client_id,topic\n"+
		"a,tok-a,,,,\n"+
		"b,,bob,hunter2,bob-client,\n"+
		"c,,,,,custom/c\n")
	if err != nil {
		t.Fatal(err)
	}
	if cs[0].Username != "tok-a" || cs[0].Password != "" || cs[0].ClientID != "a" || cs[0].Topic != "devices/a/events" || cs[0].Authentication() != "password" {
		t.Errorf("token row: %+v", cs[0])
	}
	if cs[1].Username != "bob" || cs[1].Password != "hunter2" || cs[1].ClientID != "bob-client" {
		t.Errorf("password row: %+v", cs[1])
	}
	if cs[2].Authentication() != "anonymous" || cs[2].Topic != "custom/c" {
		t.Errorf("anonymous row: %+v", cs[2])
	}
}

// A malformed list is refused whole, with the line: a rehearsal of the part that parsed would
// be a rehearsal of a fleet nobody asked for.
func TestParseCredentialsRefusesMalformedLists(t *testing.T) {
	for name, tc := range map[string]struct{ csv, want string }{
		"empty":             {"", "empty"},
		"no devices":        {"device,token\n", "no devices"},
		"no device column":  {"token\nx\n", `no "device" column`},
		"unknown column":    {"device,bearer\na,x\n", "unknown column"},
		"duplicate column":  {"device,device\na,a\n", "twice"},
		"duplicate device":  {"device,token\na,1\na,2\n", "already on line 2"},
		"duplicate client":  {"device,client_id\na,same\nb,same\n", "already on line 2"},
		"token + password":  {"device,token,password\na,t,p\n", "give one"},
		"password only":     {"device,password\na,p\n", "no username"},
		"cert without key":  {"device,cert_file\na,x.crt\n", "go together"},
		"wildcard device":   {"device\na+b\n", "must not contain"},
		"wildcard topic":    {"device,topic\na,x/#\n", "wildcard"},
		"missing device":    {"device,token\n,t\n", "no device"},
		"truncated row":     {"device,token\na\n", "1 field(s)"},
		"truncated mid-row": {"device,token,password\na,t\n", "2 field(s)"},
		"surplus field":     {"device,token\na,t,extra\n", "3 field(s)"},
		"cert file missing": {"device,cert_file,key_file\na,/nonexistent.crt,/nonexistent.key\n", "nonexistent"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parse(t, tc.csv)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("err %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestParseCredentialsToleratesBOMAndBlankLines(t *testing.T) {
	cs, err := parse(t, utf8BOM+"device,token\na,1\n\nb,2\n")
	if err != nil || len(cs) != 2 {
		t.Fatalf("%v %v", cs, err)
	}
}

func TestTapFilter(t *testing.T) {
	for in, want := range map[string]string{
		"devices/{device}/events": "devices/+/events",
		"{device}/up":             "+/up",
		"a/{device}":              "a/+",
	} {
		if got, err := TapFilter(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"devices/events", "devices/x-{device}/events", "d/+/{device}"} {
		if _, err := TapFilter(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestTargetFleetRequiresTargetConfig(t *testing.T) {
	if _, err := NewTargetFleet(Config{Broker: "tcp://127.0.0.1:1", QoS: 1}, []Credential{{Device: "a", Topic: "t"}}); err == nil {
		t.Fatal("a fleet without Target would claim DeviceChain identities")
	}
}

func TestFilterMatches(t *testing.T) {
	for _, tc := range []struct {
		f, topic string
		want     bool
	}{
		{"devices/+/events", "devices/a/events", true},
		{"devices/+/events", "devices/a/b/events", false},
		{"devices/+/events", "other/a/events", false},
		{"devices/#", "devices/a/events", true},
		{"devices/+", "devices/a/events", false},
		{"a/b", "a/b", true},
	} {
		if got := FilterMatches(tc.f, tc.topic); got != tc.want {
			t.Errorf("%q vs %q: %v", tc.f, tc.topic, got)
		}
	}
}

// A parse error must be reported with its line, never panic: the first record can be absent.
func TestParseCredentialsBadQuoteReportsInsteadOfPanicking(t *testing.T) {
	for _, in := range []string{"device\"x\n", "device,token\n\"a,t\n", "device,token\na,\"t\"x\n"} {
		if _, err := parse(t, in); err == nil || !strings.Contains(err.Error(), "line") {
			t.Fatalf("%q: err %v", in, err)
		}
	}
}

// A full-width row with empty optional cells is still a deliberate anonymous device.
func TestParseCredentialsEmptyCellsStayAnonymous(t *testing.T) {
	cs, err := parse(t, "device,token\na,\n")
	if err != nil || len(cs) != 1 || cs[0].Authentication() != "anonymous" {
		t.Fatalf("%+v %v", cs, err)
	}
}
