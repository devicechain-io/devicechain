// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backupset

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/durability"
	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// A stream or bucket declared without a class is the failure this package exists to catch: the
// zero value is Unclassified, so forgetting is a test failure rather than a quiet "rebuildable".
func TestEveryDeclaredItemIsClassifiedAndExplained(t *testing.T) {
	for _, s := range streams.All {
		if !s.Durability.Valid() {
			t.Errorf("stream %q has no durability class: set Durability and DurabilityWhy on its declaration in core/streams", s.Suffix)
		}
		if strings.TrimSpace(s.DurabilityWhy) == "" {
			t.Errorf("stream %q has no DurabilityWhy", s.Suffix)
		}
	}
	for _, b := range kv.All {
		if !b.Durability.Valid() {
			t.Errorf("bucket %q has no durability class: set Durability and DurabilityWhy on its declaration in core/kv", b.Name)
		}
		if strings.TrimSpace(b.DurabilityWhy) == "" {
			t.Errorf("bucket %q has no DurabilityWhy", b.Name)
		}
	}
}

func TestItemsCoverEverythingOnceAndNameTheRestoreOwed(t *testing.T) {
	items := Items()
	if want := len(streams.All) + len(kv.All) + len(gateway) + 1; len(items) != want {
		t.Fatalf("Items() has %d entries, want %d (every stream, bucket, gateway stream and the object store)", len(items), want)
	}
	seen := map[string]bool{}
	for _, it := range items {
		key := string(it.Kind) + "/" + it.Name
		if seen[key] {
			t.Errorf("%s appears twice", key)
		}
		seen[key] = true
		if !it.Class.Valid() {
			t.Errorf("%s has class %v", key, it.Class)
		}
		if it.Why == "" {
			t.Errorf("%s has no reason", key)
		}
		// A class that promises a backup must say how; nothing else may claim a procedure.
		if (it.Class == durability.MustBackUp) != (it.Procedure != "") {
			t.Errorf("%s: class %v with procedure %q (a procedure belongs to exactly the must-back-up items)", key, it.Class, it.Procedure)
		}
	}
}

// The classes the disaster-recovery decision names. If one of these moves, the documentation and
// the people who read it must move with it, so the move has to be deliberate and visible here.
func TestTheNamedExceptionsAreMustBackUp(t *testing.T) {
	got := map[string]bool{}
	for _, it := range MustBackUp() {
		got[string(it.Kind)+"/"+it.Name] = true
	}
	want := []string{
		"stream/" + streams.DeviceEventsCapture,
		"stream/" + streams.ConnectorDispatchDead,
		"object store/" + ObjectStoreName,
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("%s must be must-back-up", w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("must-back-up set is %v, want exactly %v: widening it is a documentation and backup-procedure decision", got, want)
	}
}

func TestCachesAreRebuildableAndLeasesAreNotBackedUp(t *testing.T) {
	for _, b := range kv.All {
		if b.Tier == kv.Cache && b.Durability != durability.Rebuildable {
			t.Errorf("cache bucket %q is %v, want rebuildable (it is recomputed from the database)", b.Name, b.Durability)
		}
	}
	b, _ := kv.Lookup(kv.BucketLeases)
	if b.Durability != durability.Transient || !strings.Contains(b.DurabilityWhy, "fence") {
		t.Errorf("the lease bucket must be transient and say that a restore invalidates the fence; got %v %q", b.Durability, b.DurabilityWhy)
	}
}

func TestGatewayNamesMatchTheTransport(t *testing.T) {
	want := []string{messaging.MqttMessageStore, messaging.MqttPubRelStore, messaging.MqttQoS2InStore, messaging.MqttSessionStore, messaging.MqttRetainedStore}
	var got []string
	for _, it := range gateway {
		got = append(got, it.Name)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("gateway inventory %v differs from the transport's stream names %v", got, want)
	}
}

// backendRoot is backend/, found from this package's directory (core/backupset).
func backendRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "core", "go.mod")); err != nil {
		t.Fatalf("backend root not found at %s: %v", root, err)
	}
	return root
}

// parseGo calls fn for every non-test Go source under backend/ with its slash path relative to it
// and its syntax tree. The scans below read the tree, not the text, so a comment, a string that
// happens to contain a call, odd spacing or a line break before a parenthesis cannot hide a call
// or invent one.
func parseGo(t *testing.T, fn func(rel string, file *ast.File)) {
	t.Helper()
	root := backendRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == "vendor" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		fn(filepath.ToSlash(rel), file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// creator is a file that creates JetStream state, how many create references it holds, and why that
// is covered. The count is pinned as well as the file: a second creation path added to a file already
// on the list would otherwise pass unseen. (It cannot tell whether an existing call still goes through
// a registry; that is what review of a change to these files is for.)
type creator struct {
	calls int
	why   string
}

// creators are the only files that create a stream or bucket. Each is accounted for: the registries'
// own creation paths create only what core/streams and core/kv declare, so a class on the
// declaration covers them; the others are named for why they are outside.
var creators = map[string]creator{
	"core/messaging/nats.go":                {1, "creates platform streams, only for a suffix core/streams declares (streams.IsDeclared)"},
	"core/messaging/retention_migration.go": {1, "recreates a declared stream after a retention change"},
	"core/messaging/kv.go":                  {1, "creates buckets declared in core/kv"},
	"core/messaging/mqtt_failclosed.go":     {1, "creates the gateway message store, listed in this package's gateway inventory"},
	"core/test/jscluster.go":                {1, "test cluster helper; not an instance"},
	"edge/dc-edge-agent/agent/agent.go":     {1, "the edge agent's local spool; an edge unit, never in the cluster"},
	"edge/dc-edge-agent/agent/commands.go":  {2, "the edge agent's command inbox, responses and ledger; an edge unit, never in the cluster"},
}

// createMethods are the client methods that create JetStream state. A reference counts whether it is
// called or taken as a method value.
var createMethods = map[string]bool{
	"AddStream": true, "CreateStream": true, "CreateOrUpdateStream": true,
	"CreateKeyValue": true, "CreateOrUpdateKeyValue": true,
	"CreateObjectStore": true, "CreateOrUpdateObjectStore": true,
}

// A new path that creates JetStream state without going through a registry would hold data nobody
// classified. The test names the file so the author has to either route it through the registries or
// account for it here. (The object store is not JetStream's: it is core/blob, one declared item.)
func TestOnlyKnownFilesCreateJetStreamState(t *testing.T) {
	found := map[string]int{}
	parseGo(t, func(rel string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && createMethods[sel.Sel.Name] {
				found[rel]++
			}
			return true
		})
	})
	for rel, n := range found {
		c, ok := creators[rel]
		if !ok {
			t.Errorf("%s creates JetStream state but is not accounted for: declare the stream or bucket in core/streams or core/kv (which carry a durability class), or add the file to creators with the reason it is outside", rel)
			continue
		}
		if n != c.calls {
			t.Errorf("%s holds %d create references, creators says %d: a new creation path in an accounted-for file has to be accounted for too", rel, n, c.calls)
		}
	}
	for rel := range creators {
		if found[rel] == 0 {
			t.Errorf("creators lists %s, which no longer creates JetStream state: remove it", rel)
		}
	}
}

// A gateway stream the platform starts naming must be classified. The scan decodes every string
// literal, so a backtick string or an escaped one is read as the name it is.
func TestEveryGatewayStreamNamedInSourceIsClassified(t *testing.T) {
	known := map[string]bool{}
	for _, it := range gateway {
		known[it.Name] = true
	}
	parseGo(t, func(rel string, file *ast.File) {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err == nil && strings.HasPrefix(v, "$MQTT_") && len(v) > len("$MQTT_") && !strings.ContainsAny(v, " .*>") && !known[v] {
				t.Errorf("%s names gateway stream %q, which backupset.gateway does not classify", rel, v)
			}
			return true
		})
	})
}

// docsPages are the published disaster-recovery page in every language. The table in each is the
// same generated block: its cells are identifiers and reasons, not prose.
var docsPages = []string{
	"docs/docs/deployment/disaster-recovery.md",
	"docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/disaster-recovery.md",
	"docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/disaster-recovery.md",
}

// The page cannot say something the code does not, and the code cannot change without the page.
func TestDisasterRecoveryPageCarriesTheGeneratedTable(t *testing.T) {
	repo := filepath.Join(backendRoot(t), "..")
	want := Table()
	for _, rel := range docsPages {
		raw, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		i, j := strings.Index(text, TableStart), strings.Index(text, TableEnd)
		if i < 0 || j < i {
			t.Errorf("%s has no %s ... %s block", rel, TableStart, TableEnd)
			continue
		}
		got := text[i:j+len(TableEnd)] + "\n"
		if got != want {
			t.Errorf("%s: the durability table is out of step with the code. Replace the block between the markers with:\n%s", rel, want)
		}
	}
}
