// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"go/ast"
	"go/parser"
	"go/token"
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
//
// markViolations is the walk, returning what it finds so the test can show it fails on a type that
// does carry one (a guard that has not been shown to fail does not count).
func markViolations(root reflect.Type) (violations []string, visited int) {
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
		if typ == reflect.TypeOf(model.UnresolvedIntegrationUplinkPayload{}) {
			violations = append(violations, path+" reaches the integration uplink payload, which only the integration route may build")
		}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			key := strings.ToLower(f.Name + " " + f.Tag.Get("json"))
			for _, banned := range []string{"integration", "sealed",
				// The integration uplink's own fields: a device-facing input that could carry them
				// would let a device hand the resolver the raw input of a decoder, or pick the
				// charge it was admitted under.
				"maxreadings", "max_readings", "profileversion", "profile_version"} {
				if strings.Contains(key, banned) {
					violations = append(violations, path+"."+f.Name+" can carry "+banned)
				}
			}
			walk(f.Type, path+"."+f.Name)
		}
	}
	walk(root, root.Name())
	return violations, len(seen)
}

func TestTheDeviceJsonInputHasNoFieldThatCouldCarryAnIntegrationMark(t *testing.T) {
	violations, visited := markViolations(reflect.TypeOf(JsonEvent{}))
	require.Empty(t, violations)
	require.NotZero(t, visited, "the walk visited nothing")
}

// The walk can fail: each way a device input could grow the authority is caught, including the
// integration uplink's payload type and its charged-count and version fields.
func TestTheMarkWalkSeesEachWayADeviceInputCouldCarryAnIntegrationAuthority(t *testing.T) {
	type sealed struct {
		Sealed string `json:"sealedIntegrationToken"`
	}
	type payloadInside struct {
		Nested []*model.UnresolvedIntegrationUplinkPayload `json:"nested"`
	}
	type chargedCount struct {
		Deep struct{ MaxReadings int } `json:"deep"`
	}
	type versionToken struct {
		ProfileVersionToken string `json:"profileVersionToken"`
	}
	type snake struct {
		X int `json:"max_readings"`
	}
	for name, typ := range map[string]reflect.Type{
		"a sealed token":                  reflect.TypeOf(sealed{}),
		"the uplink payload type, nested": reflect.TypeOf(payloadInside{}),
		"a charged-count field, nested":   reflect.TypeOf(chargedCount{}),
		"a profile version token":         reflect.TypeOf(versionToken{}),
		"a snake-case charged-count key":  reflect.TypeOf(snake{}),
	} {
		violations, _ := markViolations(typ)
		require.NotEmpty(t, violations, name)
	}
	clean, _ := markViolations(reflect.TypeOf(struct{ Device string }{}))
	require.Empty(t, clean, "the counterweight: an ordinary input passes")
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

// 4. EVENT TYPE: the IntegrationUplink type is the integration route's alone. A device naming it in
// DC JSON, in any decode path, is refused; and no Go file outside the producer, the consumer and
// the type's own plumbing mentions it.
func TestADeviceBodyThatNamesTheIntegrationUplinkTypeIsRefused(t *testing.T) {
	bodies := map[string]string{
		"with the uplink's own fields": `{"device":"sensor-001","eventType":"IntegrationUplink","payload":{"frame":"AQID","fPort":2,` +
			`"fCnt":7,"maxReadings":9,"profileVersionToken":"v1","decoded":{"t":21.5}}}`,
		"with a measurement payload": `{"device":"sensor-001","eventType":"IntegrationUplink",` +
			`"payload":{"entries":[{"measurements":{"temp":"21.5"}}]}}`,
		"with marks": `{"device":"sensor-001","eventType":"IntegrationUplink","integration":"cs","sealedIntegrationToken":"AAAA","payload":{}}`,
	}
	for name, body := range bodies {
		event, built, err := NewJsonDecoder(map[string]string{}).Decode([]byte(body), time.Time{})
		require.Error(t, err, name)
		require.Nil(t, event, name)
		require.Nil(t, built, name)
		require.Contains(t, err.Error(), "integration route", name)
		_, _, err = NewJsonDecoder(map[string]string{}).decodeReference([]byte(body), time.Time{})
		require.Error(t, err, name+" (reference path)")
	}
}

// The files that may name eventtype.IntegrationUplink, relative to backend/: the registry and wire
// plumbing of the type, the device-facing refusals, and the one producer and one consumer.
var integrationUplinkAllowed = map[string]bool{
	filepath.Join("services", "event-sources", "model", "events.go"):                       true,
	filepath.Join("services", "event-sources", "proto", "marshal.go"):                      true,
	filepath.Join("services", "event-sources", "processor", "decoders.go"):                 true,
	filepath.Join("services", "event-sources", "processor", "http_integration.go"):         true,
	filepath.Join("services", "event-sources", "processor", "http_integration_decoder.go"): true,
	filepath.Join("services", "event-sources", "main.go"):                                  true,
	filepath.Join("services", "device-management", "processor", "integration_resolve.go"):  true,
}

func TestNothingOutsideTheIntegrationPathNamesTheIntegrationUplinkType(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	scanned := 0
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
		if namesIntegrationUplinkType(string(src)) && !integrationUplinkAllowed[rel] {
			t.Errorf("%s names eventtype.IntegrationUplink: only the integration path may (see integrationUplinkAllowed)", rel)
		}
		return nil
	}))
	require.Greater(t, scanned, 500, "the walk must have covered the workspace, not an empty directory")
}

// namesIntegrationUplinkType reports whether Go source uses the IntegrationUplink event type or
// builds its payload. It parses the file and resolves the local name of the eventtype import, so
// an import alias (et.IntegrationUplink) is seen as well as the plain spelling, and it also flags:
//   - a conversion of the literal 7 to the eventtype's EventType (EventType(7));
//   - a dot import of the eventtype package, whose bare IntegrationUplink is then the type;
//   - a composite literal of UnresolvedIntegrationUplinkPayload, from any package name.
//
// REMAINING LIMITS (this is syntax, not go/types): a constant that happens to equal 7 and is
// converted by name (const seven = 7; EventType(seven)), a value read back from a variable or the
// wire, and a type reached through a re-declared alias of the payload struct. The backstop for
// those is the resolver's own guard, which refuses a type it does not trust.
func namesIntegrationUplinkType(src string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		// A file that does not parse cannot be shown clean.
		return true
	}
	local, dot := "", false
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, "\"") != "github.com/devicechain-io/dc-microservice/eventtype" {
			continue
		}
		switch {
		case imp.Name == nil:
			local = "eventtype"
		case imp.Name.Name == ".":
			dot = true
		case imp.Name.Name != "_":
			local = imp.Name.Name
		}
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && local != "" && id.Name == local && x.Sel.Name == "IntegrationUplink" {
				found = true
			}
		case *ast.Ident:
			if dot && x.Name == "IntegrationUplink" {
				found = true
			}
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && len(x.Args) == 1 {
				if id, ok := sel.X.(*ast.Ident); ok && local != "" && id.Name == local && sel.Sel.Name == "EventType" {
					if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "7" {
						found = true
					}
				}
			}
		case *ast.CompositeLit:
			switch t := x.Type.(type) {
			case *ast.SelectorExpr:
				found = found || t.Sel.Name == "UnresolvedIntegrationUplinkPayload"
			case *ast.Ident:
				found = found || t.Name == "UnresolvedIntegrationUplinkPayload"
			}
		}
		return !found
	})
	return found
}

func TestTheIntegrationUplinkGuardSeesEachSpellingAndIgnoresAComment(t *testing.T) {
	const imp = `"github.com/devicechain-io/dc-microservice/eventtype"`
	head := "package x\nimport " + imp + "\n"
	aliased := "package x\nimport et " + imp + "\n"
	for name, src := range map[string]string{
		"plain":           head + "var _ = eventtype.IntegrationUplink",
		"switch case":     head + "func f(e eventtype.EventType) { switch e { case eventtype.IntegrationUplink: } }",
		"import alias":    aliased + "var _ = et.IntegrationUplink",
		"literal 7":       head + "var _ = eventtype.EventType(7)",
		"aliased literal": aliased + "var _ = et.EventType(7)",
		"payload literal": "package x\nvar _ = &model.UnresolvedIntegrationUplinkPayload{}",
		"dot import":      "package x\nimport . " + imp + "\nvar _ = IntegrationUplink",
		"does not parse":  "package x\nfunc (",
	} {
		require.True(t, namesIntegrationUplinkType(src), name)
	}
	for name, src := range map[string]string{
		"a comment":            head + "// eventtype.IntegrationUplink is the route's\nvar _ = eventtype.Measurement",
		"another type":         head + "var _ = eventtype.EventType(2)",
		"another alias":        aliased + "var _ = et.Measurement",
		"a local of that name": "package x\nvar et struct{ IntegrationUplink int }\nvar _ = et.IntegrationUplink",
	} {
		require.False(t, namesIntegrationUplinkType(src), name)
	}
}
