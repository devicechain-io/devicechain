// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/releaseutil"
	"sigs.k8s.io/yaml"
)

// An instance's namespace is spelled TWICE — once in Go and once in the chart — and
// nothing but these tests makes the two agree.
//
//   - Go:    InstanceNamespace() in instancenamespace.go, which every client-go call,
//     every namespace dcctl creates/deletes/waits on, the broker certificate's DNS
//     names and the `instance_namespace` tfvar come through.
//   - Chart: the named template devicechain.instanceNamespace in
//     templates/_helpers.tpl, which every `metadata.namespace`, the Namespace object's
//     own name, the monitors' namespaceSelectors, every alert expression's
//     `namespace="…"` matcher and the dashboards' hidden `namespace` constant come
//     through.
//
// They are independent expressions of one rule. When they disagree, dcctl creates and
// labels one namespace while the chart renders every object into another: no pod mounts
// the instance configuration Secret, the secret-store root key is written where nothing
// reads it, `dcctl destroy` deletes a namespace that holds none of the workload, alerts
// match no series and every dashboard is blank. Nothing fails loudly; the instance just
// never comes up, and the reason is two strings in two languages.
//
// 🔴 THIS FILE WAS WRITTEN BEFORE THE PREFIX EXISTED, DELIBERATELY, AND THAT IS WHY IT IS
// WORTH KEEPING. While both functions returned the instance id unchanged every assertion
// below compared a string to itself and could not fail — which is exactly what made it the
// right instrument: it was in place BEFORE the two bodies changed, so when they did, every
// site the flip had missed failed here. A test written afterwards could only ever confirm
// the flip that was made; this one was able to say whether the flip was COMPLETE. It is
// now live — `InstanceNamespace(id)` is `dci-<id>` and the chart computes the same string
// independently — and it goes on guarding the agreement between two expressions that have
// no other reason to stay equal.
//
// 🔑 WHAT IS COMPARED AGAINST WHAT, AND WHY IT DIFFERS BY ASSERTION. For the NAMESPACE,
// InstanceNamespace() IS the contract, so calling it is exactly right — the question is
// whether the chart agrees with Go, not whether either matches a literal. For everything
// the instance id names that is NOT a namespace, the expected strings are written as
// LITERALS below. Deriving those from a constant would move both sides of the comparison
// together on a rename and the test would pass through the very change it exists to
// catch.
//
// 🔴 NO TEMPLATE IS NAMED ANYWHERE IN THIS FILE. The walks below traverse whatever the
// chart rendered, so a template added later is covered without anyone remembering. The
// only exemptions are two NAMESPACE VALUES that are legitimately not this instance's —
// the shared database namespace and the CloudNativePG operator's — and both are read from
// the values this test supplies rather than written as literals, and both are asserted to
// be actually exercised, so an exemption that stops applying fails rather than rotting.

const (
	// The instance id under test. `acme` is deliberately a string that turns up
	// INSIDE other rendered strings — `dci-acme-config`, `acme-outbound-connectors-
	// egress`, `devicechain-acme` — and, once the prefix lands, the namespace
	// `dci-acme` is itself a prefix of `dci-acme-config`. Everything below compares
	// with equality for that reason; nothing here asks whether a string CONTAINS the
	// id.
	nsTestInstanceID = "acme"

	// The two namespaces in the rendered output that are deliberately NOT this
	// instance's. Supplied as values (metrics.databaseNamespace, metrics.cnpgNamespace)
	// so the exemption is the operator's declaration rather than a literal this test
	// invented, and asserted to be exercised below.
	nsTestDatabaseNamespace = "dc-system"
	nsTestCNPGNamespace     = "cnpg-system"
)

// renderedChartDoc is one document out of the rendered manifest, carrying the
// `# Source:` comment Helm writes above it so a failure can name the template
// without this file holding a list of them.
type renderedChartDoc struct {
	source string
	kind   string
	name   string
	obj    map[string]interface{}
}

var chartSourceComment = regexp.MustCompile(`(?m)^#\s*Source:\s*(\S+)`)

// renderNamespaceFixture renders the EMBEDDED chart in process — the same chart dcctl
// installs, through the same helpers the pre-flight uses — and returns its documents.
//
// It deliberately turns on every optional object the chart can render (the NetworkPolicy,
// the blob claim, the PodDisruptionBudgets, the Ingress, an extraSecret), because an
// object that does not render is an object these walks cannot see. The default render
// omits five templates; with them off, a namespace written wrongly in any of them would
// be invisible here and the test would report green over less.
//
// 🔴 THE BROKER HOSTNAME IS LEFT AT THE CHART DEFAULT (`dc-nats`, unqualified) ON PURPOSE.
// templates/networkpolicy.yaml has a branch that, for a QUALIFIED broker hostname
// (<release>.<namespace>), renders a namespaceSelector naming that broker's OWN namespace
// — a bring-your-own broker running somewhere else entirely. That selector is correctly
// not this instance's namespace, so rendering it would put a string into the walk below
// that must not be held to InstanceNamespace(). Unqualified, the branch does not fire and
// the policy selects the broker by pod labels instead.
func renderNamespaceFixture(t *testing.T) []renderedChartDoc {
	t.Helper()

	vals := map[string]interface{}{
		// `full` so outbound-connectors is deployed: the NetworkPolicy refuses to
		// render without it.
		"profile":  "full",
		"replicas": 2, // >1, or no PodDisruptionBudget renders at all
		"networkPolicy": map[string]interface{}{
			"enabled": true,
		},
		"blobStorage": map[string]interface{}{
			"persistence": map[string]interface{}{
				"enabled": true,
				// RWX because replicas>1 above; the chart fails the render on RWO.
				"accessModes": []interface{}{"ReadWriteMany"},
			},
		},
		"ingress": map[string]interface{}{
			"enabled": true,
		},
		"extraSecrets": []interface{}{
			map[string]interface{}{
				"name":       "dci-devicechain-lwm2m-psk",
				"stringData": map[string]interface{}{"DC_LWM2M_PSK_1": "not-a-key"},
			},
		},
		"metrics": map[string]interface{}{
			"databaseNamespace": nsTestDatabaseNamespace,
			"cnpgNamespace":     nsTestCNPGNamespace,
		},
		"instance": map[string]interface{}{
			"id": nsTestInstanceID,
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					// The chart refuses a profile carrying a secret-store area with no
					// root key; a throwaway is fine since nothing decrypts anything.
					"secrets": map[string]interface{}{
						"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
					"blob": map[string]interface{}{
						"backend":   "filesystem",
						"directory": "/var/lib/devicechain/blob",
					},
				},
			},
		},
	}

	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	manifest, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("rendering the embedded chart: %v", err)
	}

	split := releaseutil.SplitManifests(manifest)
	keys := make([]string, 0, len(split))
	for k := range split {
		keys = append(keys, k)
	}
	// SplitManifests returns a MAP, so the iteration order is random. Sorted, so a
	// failure report reads the same way twice.
	sort.Strings(keys)

	docs := make([]renderedChartDoc, 0, len(split))
	for _, k := range keys {
		raw := split[k]
		if strings.TrimSpace(raw) == "" {
			continue
		}
		obj := map[string]interface{}{}
		if err := yaml.Unmarshal([]byte(raw), &obj); err != nil {
			t.Fatalf("decoding rendered document %s: %v", k, err)
		}
		if len(obj) == 0 {
			continue
		}
		src := "(unattributed)"
		if m := chartSourceComment.FindStringSubmatch(raw); m != nil {
			src = m[1]
		}
		kind, _ := obj["kind"].(string)
		name := ""
		if md, ok := obj["metadata"].(map[string]interface{}); ok {
			name, _ = md["name"].(string)
		}
		docs = append(docs, renderedChartDoc{source: src, kind: kind, name: name, obj: obj})
	}
	if len(docs) == 0 {
		t.Fatal("the chart rendered no documents at all, so nothing below is measuring anything")
	}
	return docs
}

func (d renderedChartDoc) String() string {
	return fmt.Sprintf("%s %s (%s)", d.kind, d.name, d.source)
}

// walkRendered traverses one decoded document, calling onMap for every map and onString
// for every string, with a dotted path for the failure message. Either callback may be
// nil. Map keys are visited in sorted order so failures are reproducible.
func walkRendered(path string, v interface{}, onMap func(string, map[string]interface{}), onString func(string, string)) {
	switch t := v.(type) {
	case map[string]interface{}:
		if onMap != nil {
			onMap(path, t)
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkRendered(path+"."+k, t[k], onMap, onString)
		}
	case []interface{}:
		for i, e := range t {
			walkRendered(fmt.Sprintf("%s[%d]", path, i), e, onMap, onString)
		}
	case string:
		if onString != nil {
			onString(path, t)
		}
	}
}

// (1) Every namespaced object the chart renders must land in the instance's namespace.
//
// Walks every document rather than enumerating the kinds it expects: a Kind the chart
// gains later is covered the day it renders, which is the whole reason this is not a
// list.
func TestEveryRenderedObjectIsInTheInstanceNamespace(t *testing.T) {
	want := InstanceNamespace(nsTestInstanceID)
	docs := renderNamespaceFixture(t)

	placed := 0
	for _, d := range docs {
		md, ok := d.obj["metadata"].(map[string]interface{})
		if !ok {
			continue
		}
		ns, ok := md["namespace"].(string)
		if !ok || ns == "" {
			// A cluster-scoped object (the Namespace itself) states none, which is
			// correct. (2) is what holds that one.
			continue
		}
		placed++
		if ns != want {
			t.Errorf("%s renders into namespace %q; dcctl creates, labels and destroys %q.\n"+
				"The chart and InstanceNamespace() disagree, so this object is written where "+
				"nothing dcctl did will find it.", d, ns, want)
		}
	}
	if placed == 0 {
		t.Fatal("not one rendered object carried a metadata.namespace: this test walked the " +
			"manifest and asserted nothing")
	}
	// A floor, not a count: the render carries dozens of namespaced objects today, and
	// a render that collapsed to a handful would pass every comparison above while
	// covering almost nothing. Deliberately far below the real number so it does not
	// need editing when an area is added.
	if placed < 20 {
		t.Errorf("only %d namespaced objects rendered; this fixture is meant to turn on every "+
			"optional template, so something stopped rendering and the walk above is now "+
			"green over much less than it was written to cover", placed)
	}
}

// (2) The Namespace object the chart creates must be the namespace dcctl believes in.
//
// This is the one that fails LOUDEST when the two disagree — the chart makes a namespace
// nobody else touches — and it is also the one an install can hide, because
// instance.createNamespace is optional and dcctl creates the namespace itself.
func TestTheRenderedNamespaceObjectIsTheInstanceNamespace(t *testing.T) {
	want := InstanceNamespace(nsTestInstanceID)
	docs := renderNamespaceFixture(t)

	found := 0
	for _, d := range docs {
		if d.kind != "Namespace" {
			continue
		}
		found++
		if d.name != want {
			t.Errorf("the chart creates Namespace %q and dcctl creates, labels and destroys %q "+
				"(%s): the instance's objects and the namespace dcctl manages are two different "+
				"namespaces", d.name, want, d.source)
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one Namespace object in the render, got %d: with none, "+
			"the assertion above holds nothing", found)
	}
}

// classifyNamespaceValue is the single verdict on a string that NAMES A NAMESPACE,
// wherever it was written — a namespaceSelector entry or a PromQL matcher.
//
// It is one function because the question is one question: this string is a namespace, so
// it is either this instance's (in which case it must have come through the helper) or it
// is a namespace outside this instance that the operator declared in values. Anything
// else is a namespace spelled by hand.
func classifyNamespaceValue(t *testing.T, where, value, want string, exercised map[string]bool) {
	t.Helper()
	switch value {
	case want:
		return
	case nsTestDatabaseNamespace, nsTestCNPGNamespace:
		// Legitimately not this instance's: the shared relational store and the
		// CloudNativePG operator are cluster infrastructure, and both namespaces came
		// from the values this test supplied.
		exercised[value] = true
		return
	}
	extra := ""
	if value == nsTestInstanceID && want != nsTestInstanceID {
		extra = fmt.Sprintf("\nThis is the bare instance id. The instance's namespace is %q; "+
			"a site that still writes the id here was missed when the namespace gained its "+
			"prefix.", want)
	}
	t.Errorf("%s names namespace %q.\nIt is neither this instance's namespace (%q) nor one of "+
		"the two namespaces declared as outside it (%q, %q). Either it should have come "+
		"through devicechain.instanceNamespace, or it is a new foreign namespace that has to "+
		"be declared here so it stops being ambiguous.%s",
		where, value, want, nsTestDatabaseNamespace, nsTestCNPGNamespace, extra)
}

// promQLNamespaceMatcher finds `namespace="x"` / `namespace=~"a|b"` (and the negated
// forms) anywhere in a rendered string. The polarity does not change the question: the
// quoted value is a namespace name either way, and if it is this instance's it has to
// have been spelled through the helper.
var promQLNamespaceMatcher = regexp.MustCompile(`\bnamespace\s*(=~|!~|!=|=)\s*"([^"]*)"`)

// (3) + (4) Every namespace NAMED in the rendered output — as a monitor's
// namespaceSelector or inside an alert expression — is either the instance's or one the
// operator declared as somebody else's.
//
// These two are one test because they share one exemption ledger. An exemption that
// nothing exercises is free to delete, and the only way to know is to count both walks
// together: the CloudNativePG namespace appears only in a selector, the shared database
// namespace only in expressions.
//
// The walks look for the SHAPE (a namespaceSelector's matchNames, a `namespace=` matcher
// in any string) rather than for the objects known to have one, so the ServiceMonitors,
// both PodMonitors and all six PrometheusRules are covered by construction, along with
// anything added next to them.
func TestEveryNamespaceNamedInTheRenderedManifestIsAccountedFor(t *testing.T) {
	want := InstanceNamespace(nsTestInstanceID)
	docs := renderNamespaceFixture(t)

	exercised := map[string]bool{}
	selectorEntries, matcherValues := 0, 0

	for _, d := range docs {
		doc := d
		walkRendered(doc.kind, doc.obj,
			func(path string, m map[string]interface{}) {
				sel, ok := m["namespaceSelector"].(map[string]interface{})
				if !ok {
					return
				}
				names, ok := sel["matchNames"].([]interface{})
				if !ok {
					// A label-based namespaceSelector (the NetworkPolicy's) names no
					// namespace, so there is nothing here to hold to the helper.
					return
				}
				for i, n := range names {
					s, ok := n.(string)
					if !ok {
						continue
					}
					selectorEntries++
					classifyNamespaceValue(t,
						fmt.Sprintf("%s %s.namespaceSelector.matchNames[%d]", doc, path, i),
						s, want, exercised)
				}
			},
			func(path, s string) {
				for _, m := range promQLNamespaceMatcher.FindAllStringSubmatch(s, -1) {
					for _, alt := range strings.Split(m[2], "|") {
						alt = strings.TrimSpace(alt)
						if alt == "" {
							// Not skipped, because an empty branch is not an absent
							// one. `namespace=""` selects the series that carry no
							// namespace label — an alert that can never fire — and
							// `namespace=~"|x"` is anchored, so the empty branch
							// widens the matcher rather than narrowing it. Either
							// way it is a value that was meant to be a namespace and
							// rendered as nothing.
							t.Errorf("%s %s, matcher %q selects the EMPTY namespace. "+
								"A value that should have named a namespace rendered "+
								"as nothing", doc, path, m[0])
							continue
						}
						// Grafana/Prometheus template variables (`$namespace`) and any
						// unrendered Go template are not namespace NAMES. The
						// dashboards' panels are full of the first, and they are held
						// instead by (5), which checks what the variable is bound to.
						if strings.ContainsAny(alt, "${") {
							continue
						}
						matcherValues++
						classifyNamespaceValue(t,
							fmt.Sprintf("%s %s, matcher %q", doc, path, m[0]),
							alt, want, exercised)
					}
				}
			})
	}

	if selectorEntries == 0 {
		t.Error("no namespaceSelector.matchNames entry rendered at all: the ServiceMonitors and " +
			"the two PodMonitors are how Prometheus is told where to look, and this walk saw none")
	}
	if matcherValues == 0 {
		t.Error("no PromQL `namespace=` matcher rendered at all: every alert expression is " +
			"supposed to be scoped to one instance, and this walk saw nothing to check")
	}
	for _, foreign := range []string{nsTestDatabaseNamespace, nsTestCNPGNamespace} {
		if !exercised[foreign] {
			t.Errorf("namespace %q is exempted here as deliberately NOT this instance's, and "+
				"nothing in the render names it any more. An exemption nothing exercises is a "+
				"hole nobody is watching: delete it, or find out what stopped rendering", foreign)
		}
	}
}

// (5) The Grafana boards' hidden `namespace` constant is what scopes every panel.
//
// Every panel filters `namespace="$namespace"`, and the chart replaces that variable with
// a hidden constant. The board therefore reads exactly the namespace this constant names
// and there is no picker to correct it: bound to the wrong string, every panel is
// silently empty and the board still loads perfectly.
func TestGrafanaBoardsAreScopedToTheInstanceNamespace(t *testing.T) {
	want := InstanceNamespace(nsTestInstanceID)
	docs := renderNamespaceFixture(t)

	boards, constants := 0, 0
	for _, d := range docs {
		if d.kind != "ConfigMap" {
			continue
		}
		md, _ := d.obj["metadata"].(map[string]interface{})
		labels, _ := md["labels"].(map[string]interface{})
		if labels["grafana_dashboard"] != "1" {
			continue
		}
		data, _ := d.obj["data"].(map[string]interface{})
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			raw, _ := data[k].(string)
			board := map[string]interface{}{}
			if err := json.Unmarshal([]byte(raw), &board); err != nil {
				t.Errorf("%s data[%q] is not valid JSON: %v", d, k, err)
				continue
			}
			boards++
			templating, _ := board["templating"].(map[string]interface{})
			list, _ := templating["list"].([]interface{})
			for _, e := range list {
				entry, ok := e.(map[string]interface{})
				if !ok || entry["name"] != "namespace" {
					continue
				}
				constants++
				// query, plus text/value on the current selection and on every
				// option — all three are read by different parts of Grafana, and a
				// board with the right `query` and a stale `current` renders the
				// stale one.
				check := func(field string, got interface{}) {
					if s, ok := got.(string); !ok || s != want {
						t.Errorf("%s data[%q]: the hidden `namespace` constant's %s is %v, "+
							"not %q. Every panel filters namespace=\"$namespace\", so this "+
							"board reads a namespace the instance does not run in and comes "+
							"up blank with no picker to fix it", d, k, field, got, want)
					}
				}
				check("query", entry["query"])
				if cur, ok := entry["current"].(map[string]interface{}); ok {
					check("current.text", cur["text"])
					check("current.value", cur["value"])
				} else {
					t.Errorf("%s data[%q]: the `namespace` constant has no `current`", d, k)
				}
				opts, _ := entry["options"].([]interface{})
				if len(opts) == 0 {
					t.Errorf("%s data[%q]: the `namespace` constant offers no options", d, k)
				}
				for i, o := range opts {
					opt, ok := o.(map[string]interface{})
					if !ok {
						continue
					}
					check(fmt.Sprintf("options[%d].text", i), opt["text"])
					check(fmt.Sprintf("options[%d].value", i), opt["value"])
				}
			}
		}
	}
	if boards == 0 {
		t.Fatal("no Grafana dashboard ConfigMap rendered, so nothing above was checked")
	}
	if constants != boards {
		t.Errorf("%d boards rendered but only %d carried a `namespace` constant: a board without "+
			"one is unscoped and reads every instance's series", boards, constants)
	}
}

// ---------------------------------------------------------------------------
// The counterweight. Rejecting the wrong thing is only safe while the right thing
// still passes untouched.
//
// The instance id has jobs that are NOT the namespace: it is the value of the
// devicechain.io/instance label (which is also a POD SELECTOR — prefix it and every
// Deployment selects nothing), the devicechain_instance alert label that alert routing
// groups on, DC_INSTANCE_ID, and the stem of a set of object names that are published
// interfaces. `dci-` wears two unrelated hats: it is the coming namespace prefix AND a
// long-standing prefix on object names, and `dci-<id>-config` is not "the namespace plus
// -config".
//
// 🔴 EVERY EXPECTED STRING BELOW IS A LITERAL. Building them from nsTestInstanceID, or
// from anything the chart also reads, would move both sides of the comparison together
// and the test would pass through exactly the over-eager rename it exists to catch.
// ---------------------------------------------------------------------------

// (6) The devicechain.io/instance label — everywhere it appears, including as a selector.
func TestTheInstanceLabelDidNotGainTheNamespacePrefix(t *testing.T) {
	docs := renderNamespaceFixture(t)

	seen := 0
	for _, d := range docs {
		doc := d
		walkRendered(doc.kind, doc.obj, func(path string, m map[string]interface{}) {
			v, ok := m["devicechain.io/instance"]
			if !ok {
				return
			}
			seen++
			if s, ok := v.(string); !ok || s != "acme" {
				t.Errorf("%s %s.devicechain.io/instance = %v, want \"acme\". This label is the instance's IDENTITY and "+
					"it is also a pod selector: a Deployment whose selector says one thing and "+
					"whose pod template says another selects nothing, and dcctl's own ownership "+
					"check compares it against the bare instance id", doc, path, v)
			}
		}, nil)
	}
	if seen == 0 {
		t.Fatal("the devicechain.io/instance label appeared nowhere in the render")
	}
}

// (7) The devicechain_instance alert label. Alert routing groups on it.
func TestTheAlertInstanceLabelDidNotGainTheNamespacePrefix(t *testing.T) {
	docs := renderNamespaceFixture(t)

	seen := 0
	for _, d := range docs {
		doc := d
		walkRendered(doc.kind, doc.obj, func(path string, m map[string]interface{}) {
			v, ok := m["devicechain_instance"]
			if !ok {
				return
			}
			seen++
			if s, ok := v.(string); !ok || s != "acme" {
				t.Errorf("%s %s.devicechain_instance = %v, want \"acme\". This label names the INSTANCE an alert is "+
					"about, not where its pods run; Alertmanager groups and routes on it, so "+
					"renaming it silently re-routes every alert for this instance", doc, path, v)
			}
		}, nil)
	}
	if seen == 0 {
		t.Fatal("the devicechain_instance alert label appeared nowhere in the render: either no " +
			"PrometheusRule rendered, or the alerts stopped naming their instance")
	}
}

// (8) DC_INSTANCE_ID in the pod environment. Every service derives its database name, its
// messaging subjects and its MQTT topics from this.
func TestTheInstanceIdEnvVarDidNotGainTheNamespacePrefix(t *testing.T) {
	docs := renderNamespaceFixture(t)

	seen := 0
	for _, d := range docs {
		doc := d
		walkRendered(doc.kind, doc.obj, func(path string, m map[string]interface{}) {
			if m["name"] != "DC_INSTANCE_ID" {
				return
			}
			seen++
			if s, ok := m["value"].(string); !ok || s != "acme" {
				t.Errorf("%s %s: DC_INSTANCE_ID = %v, want \"acme\". Every service derives its "+
					"database name, its login, the first segment of every messaging subject and "+
					"the device-plane MQTT topic from this value", doc, path, m["value"])
			}
		}, nil)
	}
	if seen == 0 {
		t.Fatal("no container declared DC_INSTANCE_ID: either no Deployment rendered, or the " +
			"services are no longer told which instance they are")
	}
}

// (9) The object names derived from the id. Each is a published interface: an existing
// install's Secret, ConfigMap, claim, ServiceAccount or policy is ORPHANED by a rename,
// and the mount, the token and the egress boundary go with it.
func TestObjectNamesDerivedFromTheInstanceIdDidNotGainTheNamespacePrefix(t *testing.T) {
	docs := renderNamespaceFixture(t)

	present := map[string]bool{}
	for _, d := range docs {
		present[d.kind+"/"+d.name] = true
	}

	for _, want := range []struct{ ref, why string }{
		{"Secret/dci-acme-config", "the instance configuration every pod mounts at /etc/dci-config"},
		{"ConfigMap/dct-acme-config", "the per-microservice configuration every pod mounts"},
		{"PersistentVolumeClaim/dci-acme-blob", "the filesystem object store's durable volume"},
		{"ServiceAccount/dc-acme", "the identity every pod runs as"},
		{"NetworkPolicy/acme-outbound-connectors-egress", "the egress boundary around tenant-configured destinations"},
	} {
		if !present[want.ref] {
			kind, name, _ := strings.Cut(want.ref, "/")
			var sameKind []string
			for _, d := range docs {
				if d.kind == kind {
					sameKind = append(sameKind, d.name)
				}
			}
			sort.Strings(sameKind)
			t.Errorf("no %s named %q rendered — that is %s.\nThe %s objects that DID render: %v.\n"+
				"These names are built from the instance id, not from its namespace; `dci-` is a "+
				"prefix on object names as well as the coming namespace prefix, and they are not "+
				"the same prefix. A rename orphans the object on every existing install.",
				kind, name, want.why, kind, sameKind)
		}
	}
}

// (9b) The pod's REFERENCES to those objects. A name is half a contract; the other half
// is every pod that names it.
//
// These four fields are what actually break when a rename is applied to the objects and
// not to the pods, or the other way round. The symptom is not a wrong value anywhere — it
// is a pod that never starts, because the kubelet cannot mount a Secret, a ConfigMap or a
// claim that is not there, and the API server refuses a pod naming a ServiceAccount that
// does not exist. Nothing in (9) can see that: an object list is complete and correct
// while every pod points somewhere else.
//
// So each reference is checked twice — against the LITERAL it must be, and against the
// set of objects this render actually produced, which is the half that survives a rename
// applied consistently to the wrong string.
func TestPodObjectReferencesDidNotGainTheNamespacePrefix(t *testing.T) {
	docs := renderNamespaceFixture(t)

	rendered := map[string]bool{}
	for _, d := range docs {
		rendered[d.kind+"/"+d.name] = true
	}

	// field path -> (kind it references, the literal it must be)
	type ref struct{ kind, want string }
	seen := map[string]int{}
	for _, d := range docs {
		doc := d
		walkRendered(doc.kind, doc.obj, func(path string, m map[string]interface{}) {
			var found []struct {
				field string
				value string
				ref
			}
			add := func(field string, v interface{}, kind, want string) {
				s, ok := v.(string)
				if !ok || s == "" {
					return
				}
				found = append(found, struct {
					field string
					value string
					ref
				}{field, s, ref{kind, want}})
			}
			add("serviceAccountName", m["serviceAccountName"], "ServiceAccount", "dc-acme")
			if s, ok := m["secret"].(map[string]interface{}); ok {
				add("secret.secretName", s["secretName"], "Secret", "dci-acme-config")
			}
			if c, ok := m["configMap"].(map[string]interface{}); ok {
				add("configMap.name", c["name"], "ConfigMap", "dct-acme-config")
			}
			if p, ok := m["persistentVolumeClaim"].(map[string]interface{}); ok {
				add("persistentVolumeClaim.claimName", p["claimName"], "PersistentVolumeClaim", "dci-acme-blob")
			}
			for _, f := range found {
				seen[f.field]++
				if f.value != f.want {
					t.Errorf("%s %s.%s = %q, want %q. These names are built from the instance "+
						"id, not from its namespace", doc, path, f.field, f.value, f.want)
				}
				if !rendered[f.kind+"/"+f.value] {
					t.Errorf("%s %s.%s names %s %q, and this render produced no such object. "+
						"A pod cannot start while it references a Secret, ConfigMap, claim or "+
						"ServiceAccount that is not there — the object list can be perfectly "+
						"correct and every pod still point somewhere else",
						doc, path, f.field, f.kind, f.value)
				}
			}
		}, nil)
	}

	for _, field := range []string{"serviceAccountName", "secret.secretName", "configMap.name", "persistentVolumeClaim.claimName"} {
		if seen[field] == 0 {
			t.Errorf("no pod spec in the render carried a %s: that reference stopped being "+
				"produced, so this walk is checking one fewer thing than it was written to",
				field)
		}
	}
}

// (10) The Grafana board's identity: ConfigMap name, data key, folder annotation and
// title. All four name the INSTANCE, and all four are how two instances on one cluster
// stay apart in a sidecar that writes every board into one directory.
func TestGrafanaBoardIdentityDidNotGainTheNamespacePrefix(t *testing.T) {
	docs := renderNamespaceFixture(t)

	boards := 0
	for _, d := range docs {
		if d.kind != "ConfigMap" {
			continue
		}
		md, _ := d.obj["metadata"].(map[string]interface{})
		labels, _ := md["labels"].(map[string]interface{})
		if labels["grafana_dashboard"] != "1" {
			continue
		}
		boards++

		// ConfigMap name: "acme-<stem>-dashboard".
		if !strings.HasPrefix(d.name, "acme-") || !strings.HasSuffix(d.name, "-dashboard") {
			t.Errorf("Grafana ConfigMap %q is not named acme-<board>-dashboard (%s). The name is "+
				"a published interface: renaming it orphans the ConfigMap a sidecar has already "+
				"loaded, and anything that pinned the name", d.name, d.source)
		}

		annotations, _ := md["annotations"].(map[string]interface{})
		if got := annotations["grafana_folder"]; got != "devicechain-acme" {
			t.Errorf("Grafana ConfigMap %q: grafana_folder = %v, want \"devicechain-acme\". The "+
				"sidecar turns this into the folder every one of this instance's boards lands "+
				"in", d.name, got)
		}

		data, _ := d.obj["data"].(map[string]interface{})
		for k, v := range data {
			// Data key: "acme-<stem>.json" — this is the FILE NAME the sidecar writes,
			// and it is what keeps two instances' boards from being one file.
			if !strings.HasPrefix(k, "acme-") || !strings.HasSuffix(k, ".json") {
				t.Errorf("Grafana ConfigMap %q data key %q is not acme-<board>.json: two "+
					"instances whose boards share a data key are one file in the sidecar's "+
					"directory, and deleting either takes the other's board with it", d.name, k)
			}
			board := map[string]interface{}{}
			if err := json.Unmarshal([]byte(fmt.Sprint(v)), &board); err != nil {
				continue // (5) reports the parse failure
			}
			if title, _ := board["title"].(string); !strings.HasSuffix(title, " [acme]") {
				t.Errorf("Grafana ConfigMap %q data[%q]: board title %q does not end in "+
					"\" [acme]\". The suffix is how a search across folders says which "+
					"INSTANCE a board reads", d.name, k, title)
			}
		}
	}
	if boards == 0 {
		t.Fatal("no Grafana dashboard ConfigMap rendered, so nothing above was checked")
	}
}
