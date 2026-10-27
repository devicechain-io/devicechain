// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/require"
)

// THE INTEGRATION MARKS. An event whose Integration and SealedIntegrationToken are set is one the
// resolver trusts to name a device: it opens the token, authenticates the integration and then
// believes the event's Device. So the two fields must be obtainable from exactly one place, the
// integration route, after it authenticated the sender. Three guards hold that, from three
// sides.

// markFields are the UnresolvedEvent fields that carry an integration's authority, found by
// reflection so a field added to the pair later is covered without anyone remembering to.
func markFields(t *testing.T) []string {
	t.Helper()
	var out []string
	typ := reflect.TypeOf(model.UnresolvedEvent{})
	for i := 0; i < typ.NumField(); i++ {
		if strings.Contains(strings.ToLower(typ.Field(i).Name), "integration") {
			out = append(out, typ.Field(i).Name)
		}
	}
	require.ElementsMatch(t, []string{"Integration", "SealedIntegrationToken", "IntegrationIdentifier"}, out,
		"the event's integration fields changed: decide whether the new one is an authority marker, and update this guard")
	return out
}

func requireNoMarks(t *testing.T, event *model.UnresolvedEvent, context string) {
	t.Helper()
	v := reflect.ValueOf(event).Elem()
	for _, name := range markFields(t) {
		require.True(t, v.FieldByName(name).IsZero(), "%s: %s is set", context, name)
	}
}

// 1. REFLECTION: the DC-JSON device input has no field that could carry them. Every field
// reachable from JsonEvent, by name and by JSON key, is checked against the vocabulary.
func TestTheDeviceJsonInputHasNoFieldThatCouldCarryAnIntegrationMark(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type, string)
	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Ptr || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			require.NotContains(t, key, "integration", "%s.%s can carry an integration mark", path, f.Name)
			require.NotContains(t, key, "sealed", "%s.%s can carry a sealed token", path, f.Name)
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(reflect.TypeOf(JsonEvent{}), "JsonEvent")
	require.NotEmpty(t, seen, "the walk visited nothing")
	// The walk can fail: a type that did carry one is caught.
	type leaky struct {
		Sealed string `json:"sealedIntegrationToken"`
	}
	require.True(t, strings.Contains(strings.ToLower(reflect.TypeOf(leaky{}).Field(0).Tag.Get("json")), "integration"))
}

// 2. BEHAVIOUR: a body that names the marks is ignored by every decode path (the fast scanner,
// the single pass and the reference decode), in any spelling.
func TestADeviceBodyThatNamesIntegrationMarksSetsNone(t *testing.T) {
	bodies := map[string]string{
		"lower camel": `{"device":"sensor-001","eventType":"Measurement","integration":"cs","sealedIntegrationToken":"AAAA",` +
			`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`,
		"upper camel": `{"device":"sensor-001","eventType":"Measurement","Integration":"cs","SealedIntegrationToken":"AAAA",` +
			`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`,
		"after the payload": `{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temp":"21.5"}}]},` +
			`"integration":"cs","sealedIntegrationToken":"AAAA","integrationIdentifier":"70B3D57ED0000001"}`,
		"inside the payload": `{"device":"sensor-001","eventType":"Measurement","payload":{"integration":"cs",` +
			`"entries":[{"integration":"cs","measurements":{"temp":"21.5"}}]}}`,
		"with a credential, forcing the reference path": `{"device":"sensor-001","eventType":"Measurement","integration":"cs",` +
			`"sealedIntegrationToken":"AAAA","credentialType":"MQTT_BASIC","credentialId":"c","credentialSecret":"s",` +
			`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]},"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`,
	}
	for name, body := range bodies {
		event, _, err := NewJsonDecoder(map[string]string{}).Decode([]byte(body), time.Time{})
		require.NoError(t, err, name)
		requireNoMarks(t, event, name)
		ref, _, err := NewJsonDecoder(map[string]string{}).decodeReference([]byte(body), time.Time{})
		require.NoError(t, err, name)
		requireNoMarks(t, ref, name+" (reference path)")
	}
}

// 3. WORKSPACE: nothing outside the integration route (and the proto round trip, which only
// copies the fields between the wire type and this one) writes the marks of an UnresolvedEvent.
// The check reads every non-test Go file under backend/ that mentions UnresolvedEvent, so a new
// ingest service that builds events cannot grow the write without this failing.
func TestNothingOutsideTheIntegrationRouteWritesTheIntegrationMarks(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	allowed := map[string]bool{
		filepath.Join("services", "event-sources", "processor", "http_integration.go"): true,
		filepath.Join("services", "event-sources", "proto", "marshal.go"):              true,
	}
	scanned, sawRoute := 0, false
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasSuffix(name, ".pb.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)
		src, err := os.ReadFile(path)
		require.NoError(t, err)
		scanned++
		if !strings.Contains(string(src), "UnresolvedEvent") || !writesIntegrationMark(string(src)) {
			return nil
		}
		if allowed[rel] {
			sawRoute = sawRoute || strings.HasSuffix(rel, "http_integration.go")
			return nil
		}
		t.Errorf("%s writes an UnresolvedEvent's Integration or SealedIntegrationToken: only the integration route may", rel)
		return nil
	}))
	require.Greater(t, scanned, 500, "the walk must have covered the workspace, not an empty directory")
	require.True(t, sawRoute, "the route itself must be found, or the pattern matches nothing")
}

// integrationMarkClear is a write that REMOVES authority (nil or the empty string), which grants
// nothing: the copy that strips what a device presented, and the route's own clamp, do it.
var integrationMarkClear = regexp.MustCompile(`\.(Integration|SealedIntegrationToken|IntegrationIdentifier)\s*=\s*(nil|"")\s*$`)

var integrationMarkWrite = regexp.MustCompile(`(\b(Integration|SealedIntegrationToken|IntegrationIdentifier)\s*:[^:=]|\.(Integration|SealedIntegrationToken|IntegrationIdentifier)\s*=[^=])`)

// writesIntegrationMark reports whether Go source assigns either field, by struct literal or by
// selector assignment. Comment lines are not code.
func writesIntegrationMark(src string) bool {
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if integrationMarkClear.MatchString(line) {
			continue
		}
		if integrationMarkWrite.MatchString(line) {
			return true
		}
	}
	return false
}

// The pattern is shown able to see each form of write, and to leave reads and comments alone.
func TestTheWorkspaceGuardSeesEveryFormOfWrite(t *testing.T) {
	for _, writes := range []string{
		`e.Integration = "cs"`,
		`e.SealedIntegrationToken = sealed`,
		`e.SealedIntegrationToken = other`,
		`ev := &model.UnresolvedEvent{Integration: "cs"}`,
		"x := &UnresolvedEvent{\n\tSealedIntegrationToken: tok,\n}",
		`Integration:            event.Integration,`,
		`e.IntegrationIdentifier = "70B3D57ED0000001"`,
		`ev := &model.UnresolvedEvent{IntegrationIdentifier: id}`,
	} {
		require.True(t, writesIntegrationMark(writes), writes)
	}
	for _, reads := range []string{
		`if e.Integration == "" {`,
		`slug := event.Integration`,
		`use(event.SealedIntegrationToken)`,
		`// e.Integration = "cs" in a comment`,
		`x.Integration != ""`,
		`unrez.SealedIntegrationToken = nil`,
		`event.Integration = ""`,
		`event.IntegrationIdentifier = ""`,
		`use(event.IntegrationIdentifier)`,
	} {
		require.False(t, writesIntegrationMark(reads), reads)
	}
}
