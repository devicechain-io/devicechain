// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// writeOverlayFile writes an operator's values file and returns its path.
func writeOverlayFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const tuningYAML = `
replicas: 2
functionalAreas:
  device-management:
    replicas: 3
    resources:
      limits:
        cpu: "3"
  event-sources:
    config:
      capture:
        publishWindow: 512
`

func areaField(vals map[string]interface{}, area, field string) interface{} {
	fa, _ := vals["functionalAreas"].(map[string]interface{})
	a, _ := fa[area].(map[string]interface{})
	return a[field]
}

func loadTuning(t *testing.T) map[string]interface{} {
	t.Helper()
	overlay, err := LoadValuesOverlay([]string{writeOverlayFile(t, "tuning.yaml", tuningYAML)})
	if err != nil {
		t.Fatalf("a plain tuning file was refused: %v", err)
	}
	return overlay
}

// The overlay reaches the values the chart is installed with, over what dcctl computed --
// and does not disturb what dcctl owns beside it.
func TestTheOverlayIsAppliedOverWhatDcctlComputes(t *testing.T) {
	t.Parallel()
	st := persistenceState(true, false, "default") // --ha: dcctl sets event-management.replicas
	st.ValuesOverlay = loadTuning(t)

	vals, _, err := releaseValues(t.Context(), chartForTest(t), st, nil)
	if err != nil {
		t.Fatalf("releaseValues: %v", err)
	}
	if got := areaField(vals, "device-management", "replicas"); got != float64(3) {
		t.Errorf("device-management replicas = %v (%T), want the overlay's 3", got, got)
	}
	if vals["replicas"] != float64(2) {
		t.Errorf("top-level replicas = %v, want the overlay's 2", vals["replicas"])
	}
	// What dcctl computed survives beside it.
	if got := areaField(vals, "event-management", "replicas"); got != persistenceFor(st).Replicas {
		t.Errorf("event-management replicas = %v, want dcctl's %d", got, persistenceFor(st).Replicas)
	}
	if vals["image"].(map[string]interface{})["tag"] != st.ImageVersion {
		t.Errorf("image tag moved off dcctl's value: %v", vals["image"])
	}
	// ...and a capture.bodyCredential left in the previous release's values (the strip was
	// removed in v0.26.0; event-sources refuses to start on the key) is NOT carried into the
	// new release, while the overlay's own tuning of the same config block still lands.
	st2 := compactState(false)
	st2.Evolving = true
	st2.ValuesOverlay = loadTuning(t)
	previous := map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"event-sources": map[string]interface{}{
				"config": map[string]interface{}{
					"capture": map[string]interface{}{"bodyCredential": "strip"},
				},
			},
		},
	}
	vals2, _, err := releaseValues(t.Context(), chartForTest(t), st2, previous)
	if err != nil {
		t.Fatalf("releaseValues over a previous release: %v", err)
	}
	capture := vals2["functionalAreas"].(map[string]interface{})["event-sources"].(map[string]interface{})["config"].(map[string]interface{})["capture"].(map[string]interface{})
	if _, carried := capture["bodyCredential"]; carried {
		t.Errorf("the removed capture.bodyCredential was carried into the new release: %v", capture)
	}
	if capture["publishWindow"] != float64(512) {
		t.Errorf("the overlay's capture tuning did not land: %v", capture)
	}
}

// The overlay is judged before it can touch a render: with no overlay the values are
// exactly what they were (the change is inert for every existing caller).
func TestNoOverlayLeavesTheValuesAlone(t *testing.T) {
	t.Parallel()
	st := compactState(false)
	a, _, err := releaseValues(t.Context(), chartForTest(t), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.ValuesOverlay = map[string]interface{}{}
	b, _, err := releaseValues(t.Context(), chartForTest(t), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if diff := diffOverlays(a, b); len(diff) > 0 {
		t.Errorf("an empty overlay changed the values: %v", diff)
	}
}

func declarationWithOverlay(t *testing.T, overlay map[string]interface{}) *dcv1beta1.Instance {
	t.Helper()
	inst := &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "prod", UID: "u-1"}}
	if overlay != nil {
		enc, err := encodeValuesOverlay(overlay)
		if err != nil {
			t.Fatal(err)
		}
		inst.Annotations = map[string]string{AnnotationValuesOverlay: enc}
	}
	return inst
}

// An upgrade given no --values keeps applying the recorded overlay: this is the whole
// point of the feature.
func TestAnUpgradeWithoutValuesCarriesTheRecordedOverlay(t *testing.T) {
	recorded := loadTuning(t)
	st := &State{Instance: "prod"}
	if err := applyUpgradeValuesOverlay(st, declarationWithOverlay(t, recorded), UpgradeOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(diffOverlays(recorded, st.ValuesOverlay)) != 0 {
		t.Errorf("the recorded overlay was not carried: %v", st.ValuesOverlay)
	}
	if valuesOverlayChanges(st) {
		t.Error("carrying the overlay unchanged was reported as a change, so every upgrade would rewrite the declaration")
	}
}

// --values replaces the recorded overlay as a whole; it is not merged into it.
func TestValuesReplacesTheRecordedOverlay(t *testing.T) {
	recorded := loadTuning(t)
	next, err := LoadValuesOverlay([]string{writeOverlayFile(t, "n.yaml", "functionalAreas:\n  device-state:\n    replicas: 2\n")})
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Instance: "prod"}
	if err := applyUpgradeValuesOverlay(st, declarationWithOverlay(t, recorded), UpgradeOptions{Options: Options{ValuesOverlay: next}}); err != nil {
		t.Fatal(err)
	}
	if _, kept := st.ValuesOverlay["replicas"]; kept {
		t.Errorf("the old overlay's keys survived a replacing --values: %v", st.ValuesOverlay)
	}
	if !valuesOverlayChanges(st) {
		t.Error("a replacement was not reported as a change")
	}
	got := strings.Join(diffOverlays(st.ValuesOverlayRecorded, st.ValuesOverlay), "\n")
	for _, want := range []string{"- replicas: 2", "+ functionalAreas.device-state.replicas: 2", "- functionalAreas.device-management.replicas: 3"} {
		if !strings.Contains(got, want) {
			t.Errorf("the change summary lacks %q:\n%s", want, got)
		}
	}
}

// --reset-values returns the instance to what dcctl computes.
func TestResetValuesClearsTheRecordedOverlay(t *testing.T) {
	t.Parallel()
	st := &State{Instance: "prod"}
	if err := applyUpgradeValuesOverlay(st, declarationWithOverlay(t, loadTuning(t)),
		UpgradeOptions{Options: Options{ResetValues: true}}); err != nil {
		t.Fatal(err)
	}
	if len(st.ValuesOverlay) != 0 || !valuesOverlayChanges(st) {
		t.Errorf("reset left overlay %v (changes=%v)", st.ValuesOverlay, valuesOverlayChanges(st))
	}
	vals, _, err := releaseValues(t.Context(), chartForTest(t), func() *State { s := compactState(false); s.ValuesOverlay = st.ValuesOverlay; return s }(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := areaField(vals, "device-management", "replicas"); got != nil {
		t.Errorf("a reset instance still renders the tuned replicas: %v", got)
	}
}

// A refused key fails with the key's name, and the file's, before anything exists to change.
func TestARefusedKeyIsNamedAndRefusedBeforeAnyChange(t *testing.T) {
	for key, body := range map[string]string{
		"image.tag": "image:\n  tag: v9\n",
		"instance.config.infrastructure.secrets.rootKey": "instance:\n  config:\n    infrastructure:\n      secrets:\n        rootKey: AAAA\n",
		"profile":             "profile: full\n",
		"extraSecrets":        "extraSecrets: []\n",
		"ingress.host":        "ingress:\n  host: evil.example\n",
		"ingress.tls.enabled": "ingress:\n  tls:\n    enabled: false\n",
		"functionalAreas.device-management.extraEnv": "functionalAreas:\n  device-management:\n    extraEnv:\n      - name: X\n        value: y\n",
		"functionalAreas.event-management.replicas":  "functionalAreas:\n  event-management:\n    replicas: 1\n",
		"functionalAreas.lwm2m-ingest.replicas":      "functionalAreas:\n  lwm2m-ingest:\n    replicas: 1\n",
		"securityContext.runAsUser":                  "securityContext:\n  runAsUser: 0\n",
		// A security switch or wiring inside an area's otherwise tunable config.
		"functionalAreas.device-management.config.deviceAuthMode":                      "functionalAreas:\n  device-management:\n    config:\n      deviceAuthMode: disabled\n",
		"functionalAreas.update-management.config.updates.allowUnverifiedArtifacts":    "functionalAreas:\n  update-management:\n    config:\n      updates:\n        allowUnverifiedArtifacts: true\n",
		"functionalAreas.user-management.config.auth.issuerUrl":                        "functionalAreas:\n  user-management:\n    config:\n      auth:\n        issuerUrl: https://evil.example\n",
		"functionalAreas.device-state.config.rdbConfiguration.sqlDebug":                "functionalAreas:\n  device-state:\n    config:\n      rdbConfiguration:\n        sqlDebug: true\n",
		"functionalAreas.event-management.config.tsdbConfiguration.maxOpenConnections": "functionalAreas:\n  event-management:\n    config:\n      tsdbConfiguration:\n        maxOpenConnections: 80\n",
		// ...in any spelling the service would bind: the services match keys as encoding/json does.
		"functionalAreas.device-management.config.DeviceAuthMode":                "functionalAreas:\n  device-management:\n    config:\n      DeviceAuthMode: disabled\n",
		"functionalAreas.event-management.config.TsdbConfiguration.MaxOpenConns": "functionalAreas:\n  event-management:\n    config:\n      TsdbConfiguration:\n        MaxOpenConns: 80\n",
		// An ingress annotation the chart sets itself would REPLACE the chart's.
		"ingress.annotations.nginx.ingress.kubernetes.io/configuration-snippet": "ingress:\n  annotations:\n    nginx.ingress.kubernetes.io/configuration-snippet: \"#\"\n",
		"ingress.annotations.nginx.ingress.kubernetes.io/enable-access-log":     "ingress:\n  annotations:\n    nginx.ingress.kubernetes.io/enable-access-log: \"true\"\n",
		"ingress.annotations.nginx.ingress.kubernetes.io/server-snippet":        "ingress:\n  annotations:\n    nginx.ingress.kubernetes.io/server-snippet: \"#\"\n",
		"ingress.annotations.cert-manager.io/cluster-issuer":                    "ingress:\n  annotations:\n    cert-manager.io/cluster-issuer: other\n",
		"ingress.annotations.nginx.ingress.kubernetes.io/ssl-redirect":          "ingress:\n  annotations:\n    nginx.ingress.kubernetes.io/ssl-redirect: \"false\"\n",
	} {
		f := writeOverlayFile(t, "bad.yaml", body)
		_, err := LoadValuesOverlay([]string{f})
		if err == nil {
			t.Errorf("%s was accepted in an overlay", key)
			continue
		}
		if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), f) {
			t.Errorf("the refusal for %s does not name the key and the file: %v", key, err)
		}
		if !strings.Contains(err.Error(), "nothing has been changed") {
			t.Errorf("the refusal for %s does not say nothing changed: %v", key, err)
		}
	}
	// All offenders are reported at once.
	_, err := LoadValuesOverlay([]string{writeOverlayFile(t, "two.yaml", "profile: full\nimage:\n  tag: v9\n")})
	if err == nil || !strings.Contains(err.Error(), "profile") || !strings.Contains(err.Error(), "image.tag") {
		t.Errorf("a file with two bad keys should name both: %v", err)
	}
}

// A well-formed overlay whose VALUE the chart's schema refuses is refused too.
func TestASchemaInvalidOverlayIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"a string where an integer is required": "functionalAreas:\n  device-management:\n    replicas: three\n",
		"a negative replica count":              "functionalAreas:\n  device-management:\n    replicas: -1\n",
		"an unknown strategy":                   "functionalAreas:\n  device-management:\n    strategy: Eventually\n",
		"a GOMEMLIMIT percent above 99":         "goMemLimitPercent: 150\n",
	} {
		_, err := LoadValuesOverlay([]string{writeOverlayFile(t, "s.yaml", body)})
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "schema") {
			t.Errorf("%s: the refusal does not say it is the schema: %v", name, err)
		}
	}
}

func TestMalformedAndEmptyFilesAreRefused(t *testing.T) {
	if _, err := LoadValuesOverlay([]string{filepath.Join(t.TempDir(), "missing.yaml")}); err == nil {
		t.Error("a missing file was accepted")
	}
	if _, err := LoadValuesOverlay([]string{writeOverlayFile(t, "x.yaml", "replicas: 2\nreplicas: 3\n")}); err == nil {
		t.Error("a file with a duplicate key was accepted")
	}
	if _, err := LoadValuesOverlay([]string{writeOverlayFile(t, "e.yaml", "# nothing\n")}); err == nil ||
		!strings.Contains(err.Error(), "--reset-values") {
		t.Errorf("an empty file should be refused and point at --reset-values: %v", err)
	}
	if _, err := LoadValuesOverlay([]string{writeOverlayFile(t, "l.yaml", "- a\n- b\n")}); err == nil {
		t.Error("a YAML list was accepted as values")
	}
}

// Repeated --values merge in order, later files winning.
func TestRepeatedValuesFilesMergeInOrder(t *testing.T) {
	a := writeOverlayFile(t, "a.yaml", "replicas: 2\nfunctionalAreas:\n  device-state:\n    replicas: 2\n")
	b := writeOverlayFile(t, "b.yaml", "replicas: 4\n")
	got, err := LoadValuesOverlay([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if got["replicas"] != float64(4) || areaField(got, "device-state", "replicas") != float64(2) {
		t.Errorf("merge order wrong: %v", got)
	}
}

// A recorded overlay this release no longer accepts stops the upgrade -- named, with both
// ways out -- instead of failing in the Helm step.
func TestARecordedOverlayThisReleaseRejectsStopsTheUpgrade(t *testing.T) {
	stale := map[string]interface{}{"image": map[string]interface{}{"tag": "x"}} // not tunable
	err := applyUpgradeValuesOverlay(&State{Instance: "prod"}, declarationWithOverlay(t, stale), UpgradeOptions{})
	if err == nil {
		t.Fatal("a recorded overlay the allowlist refuses was applied")
	}
	for _, want := range []string{"image.tag", "--values", "--reset-values"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	// ...but --values or --reset-values gets past it, because they do not carry it.
	fresh := loadTuning(t)
	if err := applyUpgradeValuesOverlay(&State{Instance: "prod"}, declarationWithOverlay(t, stale),
		UpgradeOptions{Options: Options{ValuesOverlay: fresh}}); err != nil {
		t.Errorf("--values did not replace an unusable recorded overlay: %v", err)
	}
	if err := applyUpgradeValuesOverlay(&State{Instance: "prod"}, declarationWithOverlay(t, stale),
		UpgradeOptions{Options: Options{ResetValues: true}}); err != nil {
		t.Errorf("--reset-values did not clear an unusable recorded overlay: %v", err)
	}
}

func TestACorruptRecordedOverlayIsRefusedNotIgnored(t *testing.T) {
	inst := &dcv1beta1.Instance{Name: "prod"}
	inst.Annotations = map[string]string{AnnotationValuesOverlay: "{not json"}
	if err := applyUpgradeValuesOverlay(&State{Instance: "prod"}, inst, UpgradeOptions{}); err == nil {
		t.Fatal("an unreadable recorded overlay was treated as no overlay, which reverts the tuning silently")
	}
}

// --dry-run prints the overlay change and the record it would write.
func TestDryRunShowsTheOverlayDiff(t *testing.T) {
	st := &State{Instance: "prod", DryRun: true,
		ValuesOverlayRecorded: map[string]interface{}{"replicas": float64(2)},
		ValuesOverlay:         map[string]interface{}{"replicas": float64(3), "gogc": float64(400)}}
	out := captureStdout(t, func() { sayValuesOverlay(st) })
	for _, want := range []string{"~ replicas: 2 -> 3", "+ gogc: 400", "would record the values overlay"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run lacks %q:\n%s", want, out)
		}
	}
	// An unchanged, carried overlay is said to be carried and records nothing.
	st.ValuesOverlayRecorded = st.ValuesOverlay
	out = captureStdout(t, func() { sayValuesOverlay(st) })
	if !strings.Contains(out, "carried unchanged") || strings.Contains(out, "would record") {
		t.Errorf("a carried overlay should say so and record nothing:\n%s", out)
	}
	// Nothing recorded and nothing given says nothing.
	if out := captureStdout(t, func() { sayValuesOverlay(&State{DryRun: true}) }); out != "" {
		t.Errorf("no overlay should print nothing, got %q", out)
	}
}

// The record round-trips through the declaration, survives every other declaration write
// (the version bump, a phase change), and is removed when cleared.
func TestTheOverlayIsRecordedOnTheDeclarationAndSurvivesItsOtherWrites(t *testing.T) {
	dyn := declarationClient(declaredInstance(t, "prod", nil))
	overlay := loadTuning(t)
	if err := recordValuesOverlay(t.Context(), dyn, "prod", overlay); err != nil {
		t.Fatal(err)
	}
	// An upgrade's own declaration write (version + phase) must not drop it.
	if err := writeInstanceCR(t.Context(), dyn, "prod", desiredSpec(), "v1", dcv1beta1.PhaseUpgrading); err != nil {
		t.Fatal(err)
	}
	if err := writeInstanceDecl(t.Context(), dyn, "prod", desiredSpec(), "v1", dcv1beta1.PhaseBootstrapping, markLive); err != nil {
		t.Fatal(err)
	}
	got, err := decodeValuesOverlay(readBack(t, dyn, "prod").Annotations)
	if err != nil {
		t.Fatal(err)
	}
	if d := diffOverlays(overlay, got); len(d) != 0 {
		t.Errorf("the recorded overlay changed across declaration writes: %v", d)
	}
	if err := recordValuesOverlay(t.Context(), dyn, "prod", nil); err != nil {
		t.Fatal(err)
	}
	if _, has := readBack(t, dyn, "prod").Annotations[AnnotationValuesOverlay]; has {
		t.Error("clearing the overlay left the annotation behind")
	}
}

// A bootstrap records its overlay with the declaration, and a re-run that names no
// --values keeps the recorded one rather than rendering without it.
func TestBootstrapRecordsTheOverlayAndARerunKeepsIt(t *testing.T) {
	prev, prevW, prevR := readInstanceDeclaration, writeInstanceDeclaration, recordValuesOverlayFor
	t.Cleanup(func() { readInstanceDeclaration, writeInstanceDeclaration, recordValuesOverlayFor = prev, prevW, prevR })

	var existing *dcv1beta1.Instance
	var recorded map[string]interface{}
	writes := 0
	readInstanceDeclaration = func(context.Context, string, string) (*dcv1beta1.Instance, error) {
		if existing == nil {
			return &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "prod", UID: "u"},
				Spec: dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default"}}, nil
		}
		return existing, nil
	}
	writeInstanceDeclaration = func(context.Context, string, string, dcv1beta1.InstanceSpec, string, bool) error { return nil }
	recordValuesOverlayFor = func(_ context.Context, _, _ string, o map[string]interface{}) error {
		writes++
		recorded = o
		return nil
	}

	// First bootstrap, given --values: recorded.
	st := &State{Instance: "prod", Provider: "kind", Profile: "default", ValuesOverlay: loadTuning(t)}
	if err := stepDeclareInstance(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || len(diffOverlays(recorded, st.ValuesOverlay)) != 0 {
		t.Fatalf("the bootstrap did not record its overlay (writes=%d, recorded=%v)", writes, recorded)
	}

	// A re-run over a declaration that holds one, with no --values: adopted, not rewritten.
	existing = declarationWithOverlay(t, loadTuning(t))
	existing.Spec = dcv1beta1.InstanceSpec{Provider: "kind", Profile: "default"}
	st2 := &State{Instance: "prod", Provider: "kind", Profile: "default"}
	if err := stepDeclareInstance(context.Background(), st2); err != nil {
		t.Fatal(err)
	}
	if len(diffOverlays(loadTuning(t), st2.ValuesOverlay)) != 0 {
		t.Errorf("a re-run without --values dropped the recorded overlay: %v", st2.ValuesOverlay)
	}
	if writes != 1 {
		t.Errorf("an unchanged overlay was written again (%d writes)", writes)
	}
}

// The render-time check: a combination the schema accepts but the chart refuses is
// stopped before anything is written.
func TestAnOverlayTheChartRefusesAtRenderIsStoppedEarly(t *testing.T) {
	st := compactState(false)
	st.ValuesOverlay = map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"device-management": map[string]interface{}{
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{"cpu": "8"},
					"limits":   map[string]interface{}{"cpu": "1"},
				},
			},
		},
	}
	if err := ValidateValuesOverlay(st.ValuesOverlay); err != nil {
		t.Fatalf("the schema should accept this (the chart refuses it at render): %v", err)
	}
	if err := precheckValuesOverlay(t.Context(), st); err == nil ||
		!strings.Contains(err.Error(), "nothing has been changed") {
		t.Errorf("a request above its limit was not stopped by the pre-flight render: %v", err)
	}
	st.ValuesOverlay = loadTuning(t)
	if err := precheckValuesOverlay(t.Context(), st); err != nil {
		t.Errorf("a good overlay failed the pre-flight render: %v", err)
	}
}

// 🔴 THE ALLOWLIST AND WHAT DCCTL WRITES MUST NOT OVERLAP. The allowlist is hand-written;
// what dcctl sets is GENERATED here from helmValues across every variant that writes
// something (HA, compact, an object store, LwM2M provisioning, the MQTT bridge). A leaf
// dcctl writes that the allowlist also admits would let an overlay silently override a
// value dcctl owns -- this is the check that fails when the chart grows a key, or when
// someone widens the allowlist, in a way that breaks the fence.
func TestTunableValuesNeverCollideWithWhatDcctlSets(t *testing.T) {
	variants := map[string]*State{}
	base := func() *State {
		st := persistenceState(true, false, "default")
		st.Values["natsCalloutXKeyPublic"] = "x"
		st.Values["natsSysPassword"] = "x"
		st.Values[identityEnabledValue] = "true"
		st.Values["serviceAuthSecret"] = "x"
		st.Values[databaseBackupsKey] = "true"
		st.Values[databaseNamespaceKey] = "dc-system"
		st.Values[cnpgNamespaceKey] = "cnpg-system"
		st.Credentials = &credentialSet{RDBInstancePassword: "x", TSDBPassword: "x"}
		return st
	}
	variants["ha"] = base()
	c := base()
	c.Compact, c.HA = true, false
	variants["compact"] = c
	areas := base()
	areas.EnabledAreas = []string{"device-management", "event-management"}
	variants["explicit areas"] = areas
	fs := base()
	fs.ObjectStore = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreFilesystem, Size: "5Gi", StorageClass: "fast", AccessMode: "ReadWriteMany"}
	variants["filesystem store"] = fs
	s3 := base()
	s3.ObjectStore = &dcv1beta1.ObjectStoreSpec{Backend: dcv1beta1.ObjectStoreS3,
		S3: &dcv1beta1.S3ObjectStoreSpec{Bucket: "b", Region: "r", Endpoint: "https://e", UsePathStyle: true, CredentialsSecret: &dcv1beta1.SecretReference{Namespace: "n", Name: "s"}}}
	variants["s3 store"] = s3
	lw := base()
	lw.Lwm2mIdentities = []Lwm2mIdentity{{Identity: "i", PSK: "AAAAAAAAAAAAAAAAAAAAAA==", Tenant: "t", ExternalID: "e"}}
	variants["lwm2m"] = lw

	preset := map[string]bool{}
	for _, p := range presetDefaults {
		preset[p] = true
	}
	checked := 0
	for name, st := range variants {
		for _, leaf := range overlayLeaves(helmValues(st)) {
			checked++
			dotted := strings.Join(leaf, ".")
			if preset[dotted] {
				continue
			}
			if why := refuseOverlayKey(leaf); why == "" {
				t.Errorf("[%s] dcctl sets %s and the overlay allowlist admits it: an overlay would override a value dcctl owns. "+
					"Narrow tunableValues, or add the key to ownedWithinTunable with the reason", name, dotted)
			}
		}
	}
	if checked < 50 {
		t.Fatalf("only %d dcctl-set leaves were generated; the variants no longer exercise helmValues", checked)
	}
	// The preset defaults ARE admitted -- that is what makes them presets.
	for _, p := range presetDefaults {
		if why := refuseOverlayKey(splitPath(p)); why != "" {
			t.Errorf("preset default %s is not tunable: %s", p, why)
		}
	}
}

// Every tunable pattern must be real: a value the chart's schema does not know is a typo
// in the allowlist, which would silently never match anything useful.
func TestEveryTunableValueExistsInTheChart(t *testing.T) {
	ch := chartForTest(t)
	schema := string(ch.Schema)
	for _, p := range tunableValues {
		segs := splitPath(p)
		last := segs[len(segs)-1]
		if !strings.Contains(schema, `"`+last+`"`) {
			t.Errorf("tunable %s: the chart's values schema has no %q", p, last)
		}
	}
}

// 🔴 THE CONNECTION LIMIT COUNTS WHAT THE OVERLAY RUNS. An overlay that raises a relational
// area's pods or pool raises what the instance's login has to hold; sized from dcctl's
// topology alone, the login would refuse the extra pods' connections under load while
// the store reported healthy.
func TestTheConnectionLimitCountsTheOverlaysReplicasAndPools(t *testing.T) {
	per := servicePoolSize * rolloutSurge
	base := 7 * per // the default profile's seven relational areas, one pod each
	for _, tc := range []struct {
		name    string
		overlay string
		want    int
	}{
		{"no overlay", "", base},
		{"one relational area's replicas", "functionalAreas:\n  device-management:\n    replicas: 3\n", base + 2*per},
		// event-processing's chart default pins its own count at 1, so the top-level count
		// reaches the other six.
		{"the top-level replicas", "replicas: 2\n", base + 6*per},
		{"an area's own pool", "functionalAreas:\n  device-state:\n    config:\n      rdbConfiguration:\n        maxOpenConnections: 50\n",
			base - per + 50*rolloutSurge},
		{"a pool in another spelling", "functionalAreas:\n  device-state:\n    config:\n      RdbConfiguration:\n        MaxOpenConnections: 50\n",
			base - per + 50*rolloutSurge},
		{"replicas and pool together", "functionalAreas:\n  device-state:\n    replicas: 2\n    config:\n      rdbConfiguration:\n        maxOpenConnections: 30\n",
			base - per + 2*30*rolloutSurge},
		{"a non-relational area's replicas", "functionalAreas:\n  event-sources:\n    replicas: 4\n", base},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &State{Profile: "default"}
			if tc.overlay != "" {
				o, err := LoadValuesOverlay([]string{writeOverlayFile(t, "o.yaml", tc.overlay)})
				if err != nil {
					t.Fatal(err)
				}
				st.ValuesOverlay = o
			}
			got, err := instanceConnectionLimit(st)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("limit %d, want %d", got, tc.want)
			}
		})
	}
	// The refusal names the overlay as part of the need.
	if h := overlayBudgetHint(&State{ValuesOverlay: map[string]interface{}{"replicas": float64(2)}}); !strings.Contains(h, "--reset-values") {
		t.Errorf("a budget refusal with an overlay does not name the way out: %q", h)
	}
}

// 🔴 NO OVERLAY ANNOTATION MAY REPLACE ONE THE CHART SETS. The chart appends
// ingress.annotations after its own on every Ingress, and a repeated key renders: the
// overlay's value would replace the chart's. The set the chart writes is GENERATED here
// from a real render, so a new chart annotation fails this until it is refused.
func TestNoOverlayAnnotationReplacesOneTheChartSets(t *testing.T) {
	manifest, err := renderChartClientSide(t.Context(), chartForTest(t), renderOnlyAuthoring(compactState(false)))
	if err != nil {
		t.Fatal(err)
	}
	ingresses := 0
	for _, doc := range strings.Split(manifest, "\n---") {
		var obj struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil || obj.Kind != "Ingress" {
			continue
		}
		ingresses++
		for k := range obj.Metadata.Annotations {
			if refuseIngressAnnotation(k) == "" {
				t.Errorf("Ingress %s carries annotation %s, and an overlay may set it -- which would replace the chart's value. "+
					"Add it to refusedIngressAnnotations", obj.Metadata.Name, k)
			}
		}
	}
	if ingresses < 3 {
		t.Fatalf("only %d Ingresses rendered; this check is not looking at the chart", ingresses)
	}
	// An annotation the chart does not set stays the operator's.
	if why := refuseOverlayKey([]string{"ingress", "annotations", "nginx.ingress.kubernetes.io/proxy-read-timeout"}); why != "" {
		t.Errorf("a timeout annotation was refused: %s", why)
	}
}

// An unreadable record is refused when it would be carried, and REPLACED by the two ways out
// the refusal names.
func TestACorruptRecordedOverlayIsReplacedByEitherWayOut(t *testing.T) {
	corrupt := func() *dcv1beta1.Instance {
		inst := &dcv1beta1.Instance{ObjectMeta: metav1.ObjectMeta{Name: "prod"}}
		inst.Annotations = map[string]string{AnnotationValuesOverlay: "{not json"}
		return inst
	}
	st := &State{Instance: "prod"}
	if err := applyUpgradeValuesOverlay(st, corrupt(), UpgradeOptions{Options: Options{ResetValues: true}}); err != nil {
		t.Fatalf("--reset-values did not get past an unreadable record: %v", err)
	}
	if len(st.ValuesOverlay) != 0 || !valuesOverlayChanges(st) {
		t.Errorf("--reset-values over an unreadable record would not rewrite it (overlay %v, changes %v)",
			st.ValuesOverlay, valuesOverlayChanges(st))
	}
	st = &State{Instance: "prod"}
	if err := applyUpgradeValuesOverlay(st, corrupt(), UpgradeOptions{Options: Options{ValuesOverlay: loadTuning(t)}}); err != nil {
		t.Fatalf("--values did not get past an unreadable record: %v", err)
	}
	if !valuesOverlayChanges(st) {
		t.Error("--values over an unreadable record would not rewrite it")
	}
	// The bootstrap side: given --values, the record is replaced; given none, refused.
	st = &State{Instance: "prod", ValuesOverlay: loadTuning(t)}
	if err := adoptRecordedValuesOverlay(st, corrupt()); err != nil || !valuesOverlayChanges(st) {
		t.Errorf("a bootstrap with --values did not replace an unreadable record (err %v)", err)
	}
	if err := adoptRecordedValuesOverlay(&State{Instance: "prod"}, corrupt()); err == nil {
		t.Error("a bootstrap without --values carried an unreadable record")
	}
}
