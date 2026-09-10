// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
)

// 🔴 WHAT THIS FILE IS FOR. instance.existingSecret hands the instance config to
// a supplier outside the chart. Every check the chart performs on that document
// is a check it performs while AUTHORING it, so switching the source switches
// them all off at once — silently, because a template that is not rendered
// raises nothing and a pre-flight with no document to load returns "valid".
//
// Nothing sets that value yet. These tests exist so that the day something does,
// the guards are already closed rather than being closed afterwards against a
// path that already shipped without them.

// digest is a well-formed checksum for a fixture — the shape the chart now
// demands, computed rather than typed so a test cannot accidentally pin a value
// the guard would reject.
func digest(seed string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(seed)))
}

// externalValues builds a values map whose instance config comes from a Secret
// the chart cannot see. checksum is passed through verbatim, including empty.
//
// metrics.natsBrokerHost is set because the NATS PodMonitor is on by default and
// derives its target namespace from the config the chart cannot read; the chart
// requires it restated. That is the subject of its own test below — here it is
// just fixture noise, kept in one place.
func externalValues(checksum string) map[string]interface{} {
	inst := map[string]interface{}{
		"id":             "dctest",
		"existingSecret": "dci-dctest-config",
	}
	if checksum != "" {
		inst["existingSecretChecksum"] = checksum
	}
	return map[string]interface{}{
		"instance": inst,
		"metrics":  map[string]interface{}{"natsBrokerHost": "dc-nats.dc-system"},
	}
}

// inlineValues is the same instance with the config supplied in values, which is
// the path every other test in this package uses.
func inlineValues() map[string]interface{} {
	return map[string]interface{}{
		"instance": map[string]interface{}{
			"id": "dctest",
			"config": map[string]interface{}{
				"infrastructure": map[string]interface{}{
					"secrets": map[string]interface{}{
						"rootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
					},
				},
			},
		},
	}
}

func chartForTest(t *testing.T) *chart.Chart {
	t.Helper()
	ch, err := loadEmbeddedChart()
	if err != nil {
		t.Fatalf("loading embedded chart: %v", err)
	}
	return ch
}

// OBLIGATION: the pod annotation that rolls workloads on a config change must
// still change when the config changes.
//
// It hashes the document the chart renders, and under an external Secret the
// chart renders none — so the hash is a constant. A credential rotation would
// update the Secret, apply cleanly, restart nothing, and report success, with
// every pod still holding the old value: secretKeyRef and mounted-Secret content
// are read at container start. The chart cannot compute the digest itself, so it
// requires the supplier to declare one.
func TestExternalInstanceConfigRequiresADeclaredChecksum(t *testing.T) {
	ch := chartForTest(t)

	_, err := renderChartClientSide(t.Context(), ch, externalValues(""))
	if err == nil {
		t.Fatal("the chart rendered with instance.existingSecret and no checksum: pods would " +
			"carry a constant annotation, and a config change would roll nothing")
	}
	if !strings.Contains(err.Error(), "existingSecretChecksum") {
		t.Errorf("the refusal does not name the value that fixes it: %v", err)
	}

	// The counterweight: supplying it must actually work, or this is a check that
	// bans the feature rather than one that guards it.
	out, err := renderChartClientSide(t.Context(), ch, externalValues(digest("a")))
	if err != nil {
		t.Fatalf("a declared checksum was rejected: %v", err)
	}
	if !strings.Contains(out, "checksum/instance-secret: "+strconv.Quote(digest("a"))) {
		t.Error("the declared checksum did not reach the pod annotation, so the value is " +
			"accepted and then ignored — which is the failure it was added to prevent")
	}
}

// ...and it must be the DECLARED value that lands, not a constant that happens to
// differ from the inline one. Two renders that declare different digests must
// produce different annotations; a hard-coded or empty-document hash would make
// them equal.
func TestExternalChecksumIsNotConstant(t *testing.T) {
	ch := chartForTest(t)

	first, err := renderChartClientSide(t.Context(), ch, externalValues(digest("one")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderChartClientSide(t.Context(), ch, externalValues(digest("two")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "checksum/instance-secret: "+strconv.Quote(digest("one"))) ||
		!strings.Contains(second, "checksum/instance-secret: "+strconv.Quote(digest("two"))) {
		t.Fatal("the annotation does not track the declared checksum")
	}
}

// OBLIGATION: the root-key guard turns a CrashLoopBackOff into an apply-time
// refusal, and it used to live INSIDE the Secret template — the one thing an
// external config source stops rendering. It is invoked from the unconditional
// gate now; this pins that the move did not drop it on the path where it still
// works.
func TestRootKeyGuardStillFiresOnTheInlinePath(t *testing.T) {
	ch := chartForTest(t)

	_, err := renderChartClientSide(t.Context(), ch, map[string]interface{}{
		"instance": map[string]interface{}{"id": "dctest"},
	})
	if err == nil {
		t.Fatal("a deployment with a secret-store area and no root key rendered cleanly: the " +
			"operator would learn about it as a crash-loop")
	}
	if !strings.Contains(err.Error(), "rootKey is required") {
		t.Errorf("the render failed for some other reason, so this proves nothing: %v", err)
	}

	if _, err := renderChartClientSide(t.Context(), ch, inlineValues()); err != nil {
		t.Errorf("a root key was supplied and the render still failed: %v", err)
	}
}

// OBLIGATION: the hand-set shutdown refusal must survive the checksum change.
//
// It lived inside devicechain.instanceConfig, which the pod annotation called
// unconditionally — so it ran even under an external Secret, by accident. Making
// the annotation stop hashing an unrendered document removes that accident, and
// would have taken the refusal with it. The two numbers it protects are one
// budget checked against itself: a service refuses to start if its drain window
// does not fit inside the pod's grace period.
func TestHandSetShutdownIsRefusedOnBothConfigSources(t *testing.T) {
	ch := chartForTest(t)

	for _, tc := range []struct {
		name string
		vals map[string]interface{}
	}{
		{"external Secret", externalValues(digest("a"))},
		{"inline config", inlineValues()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := tc.vals["instance"].(map[string]interface{})
			inline, ok := cfg["config"].(map[string]interface{})
			if !ok {
				inline = map[string]interface{}{}
				cfg["config"] = inline
			}
			infra, ok := inline["infrastructure"].(map[string]interface{})
			if !ok {
				infra = map[string]interface{}{}
				inline["infrastructure"] = infra
			}
			infra["shutdown"] = map[string]interface{}{"drainSeconds": 9}

			_, err := renderChartClientSide(t.Context(), ch, tc.vals)
			if err == nil {
				t.Fatal("a hand-set shutdown block was accepted: the document's drain window " +
					"and the pod's grace period would come from two places")
			}
			if !strings.Contains(err.Error(), "set by the chart, not by hand") {
				t.Errorf("the render failed for some other reason: %v", err)
			}
		})
	}
}

// OBLIGATION: the bootstrap pre-flight must not read "no document" as "valid".
//
// It runs the services' own strict loader over the bytes the chart is about to
// hand them, which is the only thing that turns a rejected config into a sentence
// instead of a readiness timeout. Under an external Secret the chart renders
// nothing, and the pre-flight returned nil — answering "valid" for every document
// including the ones it exists to catch, on the exact path where the chart's own
// guards are off too.
func TestPreflightRefusesADeployNobodyAuthoredAConfigFor(t *testing.T) {
	ch := chartForTest(t)
	external := externalValues(digest("a"))

	err := validateRenderedInstanceConfig(t.Context(), ch, external, nil)
	if err == nil {
		t.Fatal("the pre-flight passed a deploy whose instance config it never saw")
	}
	if !strings.Contains(err.Error(), "no instance configuration document") {
		t.Errorf("the refusal does not say what is missing: %v", err)
	}

	// An authored document goes through the SAME strict loader — the whole point of
	// the parameter. A bad one must be refused with the loader's own sentence...
	bad := []byte(`{"infrastructure":{"thisIsNotAField":true}}`)
	err = validateRenderedInstanceConfig(t.Context(), ch, external, bad)
	if err == nil {
		t.Fatal("an authored document with an unknown key passed the strict load")
	}
	if !strings.Contains(err.Error(), "refuse to start") {
		t.Errorf("the refusal did not come from the services' own loader: %v", err)
	}

	// ...and a good one must pass, or the parameter bans the path it enables.
	good, err := renderInstanceConfigDocument(t.Context(), ch, inlineValues())
	if err != nil {
		t.Fatal(err)
	}
	if good == nil {
		t.Fatal("the inline path rendered no document, so this test has no known-good bytes")
	}
	if err := validateRenderedInstanceConfig(t.Context(), ch, external, good); err != nil {
		t.Errorf("a valid authored document was rejected: %v", err)
	}
}

// Two documents for one deploy is a contradiction, not a preference: the pods
// mount exactly one. Resolving it by precedence is how the two drift apart.
func TestPreflightRefusesTwoConfigSources(t *testing.T) {
	ch := chartForTest(t)
	err := validateRenderedInstanceConfig(t.Context(), ch, inlineValues(), []byte(`{}`))
	if err == nil {
		t.Fatal("a deploy carrying both a chart-rendered Secret and an authored document was accepted")
	}
	if !strings.Contains(err.Error(), "two instance configuration documents") {
		t.Errorf("the refusal names something else: %v", err)
	}
}

// 🔴 THE COUNT WAS WRONG, AND THESE ARE THE FOUR THAT WERE MISSING. The first
// pass at this file closed five behaviours; an independent enumeration of
// everything that changes under instance.existingSecret found ten, of which four
// were silent and unguarded. Each is below. The lesson is worth the comment: the
// five were the ones reachable by asking "what does the Secret template do", and
// the four were reachable only by asking "what reads .Values.instance.config" —
// which is a different question with a longer answer, because values.schema.json
// makes that block REQUIRED, so under an external Secret it is not absent. It is
// the chart's defaults, and every template reading it renders happily from
// coordinates the pods may not be using.

// The instance config Secret's NAME is load-bearing, and letting it be free costs
// every credential the instance has.
//
// dcctl reads the config back by the name the chart would have given it, and a
// NotFound there is not "cannot tell" — it is "fresh install, mint everything".
// So an external Secret under any other name does not fail. The next bootstrap
// re-run reads nothing, decides the instance is new, and rotates the root key,
// both database passwords and every broker credential out from under a live
// instance. The chart offered the flexibility; nothing enforced the constraint it
// depended on.
func TestExternalSecretMustKeepTheNameDcctlReadsBack(t *testing.T) {
	ch := chartForTest(t)

	vals := externalValues(digest("a"))
	vals["instance"].(map[string]interface{})["existingSecret"] = "my-external-secret"

	_, err := renderChartClientSide(t.Context(), ch, vals)
	if err == nil {
		t.Fatal("a differently-named external Secret was accepted: the next bootstrap re-run " +
			"would read no config, call the instance fresh, and rotate every credential")
	}
	if !strings.Contains(err.Error(), "dci-dctest-config") {
		t.Errorf("the refusal does not name what the Secret must be called: %v", err)
	}

	// The counterweight: the required name must actually work.
	if _, err := renderChartClientSide(t.Context(), ch, externalValues(digest("a"))); err != nil {
		t.Errorf("the conventional name was rejected: %v", err)
	}
}

// The NATS PodMonitor derives its target namespace from the broker hostname in
// the instance config. Under an external Secret that value is the chart's
// DEFAULT, not the broker the pods use — so the PodMonitor would watch the wrong
// namespace and collect nothing, with a clean render and a successful install.
// It is on by default, so this is the path an ordinary external-Secret deploy
// takes.
func TestNatsPodMonitorNeedsItsBrokerRestatedUnderAnExternalSecret(t *testing.T) {
	ch := chartForTest(t)

	vals := externalValues(digest("a"))
	delete(vals, "metrics")

	_, err := renderChartClientSide(t.Context(), ch, vals)
	if err == nil {
		t.Fatal("the PodMonitor rendered from the chart's default broker hostname: it would " +
			"monitor a namespace this instance may not use, and report nothing rather than fail")
	}
	if !strings.Contains(err.Error(), "natsBrokerHost") {
		t.Errorf("the refusal does not name the value that fixes it: %v", err)
	}

	// ...and the restated host must reach the PodMonitor, or the value is accepted
	// and ignored — which is the failure it was added to prevent, wearing a hat.
	vals = externalValues(digest("a"))
	vals["metrics"].(map[string]interface{})["natsBrokerHost"] = "dc-nats.somewhere-else"
	out, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("a restated broker host was rejected: %v", err)
	}
	if !strings.Contains(out, `- "somewhere-else"`) {
		t.Error("the PodMonitor did not follow the restated broker host")
	}
}

// The NetworkPolicy's egress ports come from the instance config for a stated
// reason — "so a port change in one place cannot leave the policy behind". Under
// an external Secret it reads the chart's defaults instead, and a port that does
// not match silently blocks the services' own egress, which presents as a broker
// or database outage rather than as a policy error.
func TestNetworkPolicyNeedsItsPortsRestatedUnderAnExternalSecret(t *testing.T) {
	ch := chartForTest(t)

	base := func() map[string]interface{} {
		v := externalValues(digest("a"))
		v["profile"] = "full"
		v["ingress"] = map[string]interface{}{"enabled": true, "host": "dc.example.com"}
		v["networkPolicy"] = map[string]interface{}{"enabled": true}
		return v
	}

	if _, err := renderChartClientSide(t.Context(), ch, base()); err == nil {
		t.Fatal("the NetworkPolicy rendered from default ports under an external Secret")
	} else if !strings.Contains(err.Error(), "externalConfigPorts") {
		t.Errorf("the refusal does not name the value that fixes it: %v", err)
	}

	vals := base()
	vals["networkPolicy"].(map[string]interface{})["externalConfigPorts"] =
		map[string]interface{}{"nats": 4333, "rdb": 6543}
	out, err := renderChartClientSide(t.Context(), ch, vals)
	if err != nil {
		t.Fatalf("restated ports were rejected: %v", err)
	}
	for _, want := range []string{"port: 4333", "port: 6543"} {
		if !strings.Contains(out, want) {
			t.Errorf("the policy does not carry %q, so the restated ports were ignored", want)
		}
	}
}

// The chart does not merely WRITE the config document, it transforms it: it
// injects the shutdown budget from the top-level values and unsets the
// ai-inference coordinates when that area is not deployed. Under an external
// Secret neither happens, and neither omission is INVALID — the strict loader
// accepts both, which is precisely why the strict load alone was closing half a
// hole.
func TestAuthoredConfigMustCarryTheTransformsTheChartWouldHaveApplied(t *testing.T) {
	ch := chartForTest(t)
	external := externalValues(digest("a"))

	// 🔴 THE FIXTURES ARE BUILT FROM THE CHART'S OWN OUTPUT, then mutated. Writing
	// them by hand produced documents the strict loader rejected before any of
	// these checks ran, so every assertion below passed for the wrong reason — the
	// exact shape of a test that cannot fail. Starting from a document the chart
	// authored means the ONLY thing wrong with each fixture is the transform.
	base, err := renderInstanceConfigDocument(t.Context(), ch, inlineValues())
	if err != nil {
		t.Fatal(err)
	}
	if base == nil {
		t.Fatal("the inline path rendered no document, so there is nothing to mutate")
	}
	mutate := func(t *testing.T, fn func(infra map[string]interface{})) []byte {
		t.Helper()
		var doc map[string]interface{}
		if err := json.Unmarshal(base, &doc); err != nil {
			t.Fatal(err)
		}
		fn(doc["infrastructure"].(map[string]interface{}))
		out, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// A document whose grace period disagrees with the pod's. The service validates
	// the drain window against the document's OWN copy, so both numbers look
	// consistent to it and the pod is SIGKILLed mid-drain anyway.
	mismatched := mutate(t, func(infra map[string]interface{}) {
		infra["shutdown"] = map[string]interface{}{
			"drainSeconds": 5, "terminationGracePeriodSeconds": 600,
		}
	})
	err = validateRenderedInstanceConfig(t.Context(), ch, external, mismatched)
	if err == nil {
		t.Fatal("a grace period disagreeing with the pod spec was accepted")
	}
	if !strings.Contains(err.Error(), "grace period") {
		t.Errorf("the refusal names something else: %v", err)
	}

	// A document naming an inference host on a deploy that does not run the area.
	// The service builds its rule drafter, fails at DNS, and blames tenant consent.
	stray := mutate(t, func(infra map[string]interface{}) {
		infra["aiInference"] = map[string]interface{}{"hostname": "dc-ai.dctest", "port": 8080}
	})
	err = validateRenderedInstanceConfig(t.Context(), ch, external, stray)
	if err == nil {
		t.Fatal("coordinates for an undeployed ai-inference were accepted")
	}
	if !strings.Contains(err.Error(), "ai-inference") {
		t.Errorf("the refusal names something else: %v", err)
	}

	// The counterweight: a document carrying both transforms must pass, or these
	// checks ban the path they exist to make safe.
	if err := validateRenderedInstanceConfig(t.Context(), ch, external, base); err != nil {
		t.Errorf("a correctly transformed document was rejected: %v", err)
	}
}

// The document is picked out of the rendered stream BY NAME. It used to be picked
// by the shape of its content — the first Secret carrying a `stringData.instance`
// key — and the chart renders operator-supplied Secrets with arbitrary key names
// through extraSecrets. One entry keyed `instance` was therefore
// indistinguishable from the real thing, and because the manifest splitter
// returns a map, WHICH one came back varied between runs of identical values.
func TestAnExtraSecretCannotImpersonateTheInstanceConfig(t *testing.T) {
	ch := chartForTest(t)

	vals := inlineValues()
	vals["extraSecrets"] = []interface{}{
		map[string]interface{}{
			"name":       "lwm2m-psk",
			"stringData": map[string]interface{}{"instance": `{"infrastructure":{"nats":{"hostname":"impostor"}}}`},
		},
	}

	// Run it repeatedly: the defect was nondeterministic, so a single pass proves
	// nothing about a map iteration.
	for i := 0; i < 20; i++ {
		raw, err := renderInstanceConfigDocument(t.Context(), ch, vals)
		if err != nil {
			t.Fatal(err)
		}
		if raw == nil {
			t.Fatal("no instance config document was found at all")
		}
		if strings.Contains(string(raw), "impostor") {
			t.Fatalf("run %d returned the extraSecrets document: the pre-flight would validate "+
				"a document nobody mounts, and only sometimes", i)
		}
	}
}
