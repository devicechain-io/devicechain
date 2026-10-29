// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// distinctPSK is a base64 key that appears nowhere else in a render (a zero-filled one would match
// the zero root key's base64).
func distinctPSK(tag string) string {
	return base64.StdEncoding.EncodeToString([]byte("lwm2m-device-key-" + tag + "-0123456789"))
}

func lwm2mIDs(psk string) []Lwm2mIdentity {
	return []Lwm2mIdentity{{Identity: "opaque-1", PSK: psk, Tenant: "acme", ExternalID: "plant-a/s1", DeviceTypeToken: "sensor", AutoRegister: true}}
}

func lwm2mState(ids []Lwm2mIdentity) *State {
	enabled, err := ResolveEnabledAreas("default", []string{"lwm2m-ingest"})
	if err != nil {
		panic(err)
	}
	return &State{
		Instance:             "dctest",
		InstanceUID:          "uid-1",
		Profile:              "default",
		ImageRegistry:        DefaultImageRegistry,
		ImageVersion:         "v0.0.0-test",
		EnabledAreas:         enabled,
		Lwm2mIdentities:      ids,
		Lwm2mIdentitiesGiven: len(ids) > 0,
		Values: map[string]string{
			"ingressHost":    "localhost",
			"secretsRootKey": base64.StdEncoding.EncodeToString(make([]byte, 32)),
		},
	}
}

func lwm2mSecret(t *testing.T, typed kubernetes.Interface) (map[string][]byte, bool) {
	t.Helper()
	s, err := typed.CoreV1().Secrets("dci-dctest").Get(context.Background(), "dci-dctest-lwm2m-identities", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for k, v := range s.Data {
		out[k] = v
	}
	for k, v := range s.StringData {
		out[k] = []byte(v)
	}
	return out, true
}

func lwm2mPodAnnotations(t *testing.T, vals map[string]interface{}) map[string]interface{} {
	t.Helper()
	for _, d := range renderDocs(t, vals) {
		meta, _ := d["metadata"].(map[string]interface{})
		if d["kind"] == "Deployment" && meta["name"] == "lwm2m-ingest" {
			return d["spec"].(map[string]interface{})["template"].(map[string]interface{})["metadata"].(map[string]interface{})["annotations"].(map[string]interface{})
		}
	}
	t.Fatal("lwm2m-ingest Deployment not rendered")
	return nil
}

// 🔴 NO PRE-SHARED KEY IS A HELM VALUE, AND NONE IS IN THE RENDERED MANIFEST. A release records the
// values of every revision it keeps (`helm get values`), so a key that is a value outlives its own
// rotation; and the pod template must not carry a hash of one either.
func TestNoLwm2mPSKIsAHelmValueOrInTheRenderedManifest(t *testing.T) {
	t.Parallel()
	psk := distinctPSK("a")
	st := lwm2mState(lwm2mIDs(psk))
	vals := helmValues(st)
	if anywhere(vals, psk) {
		t.Fatal("the pre-shared key is a Helm release value: `helm get values` prints it, and every revision keeps it")
	}
	bare := sha256.Sum256([]byte(psk))
	for _, d := range renderDocs(t, vals) {
		if anywhere(d, psk) {
			t.Fatalf("the pre-shared key is in the rendered manifest: %.80v", d)
		}
		if anywhere(d, fmt.Sprintf("%x", bare)) {
			t.Fatal("an unsalted hash of the pre-shared key is in the rendered manifest")
		}
	}
	// The unsalted checksum/extra-secrets that used to hash dcctl's key material is over nothing now.
	ann := lwm2mPodAnnotations(t, vals)
	empty := sha256.Sum256([]byte("[]"))
	if ann["checksum/extra-secrets"] != fmt.Sprintf("%x", empty) {
		t.Fatalf("checksum/extra-secrets covers more than the (empty) operator extraSecrets: %v", ann["checksum/extra-secrets"])
	}
}

// The stamp is the Secret's version, salted per instance, and nothing derived from a key.
func TestTheLwm2mPodsCarryAVersionStampAndNothingDerivedFromAKey(t *testing.T) {
	t.Parallel()
	secret := func(rv, key string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "dci-dctest-lwm2m-identities", UID: "su", ResourceVersion: rv},
			Data:       map[string][]byte{"DC_LWM2M_PSK_1": []byte(key)},
		}
	}
	psk := distinctPSK("a")
	stamp := lwm2mCredentialsStamp("uid-1", secret("7", psk))
	if stamp == "" {
		t.Fatal("no stamp for a Secret")
	}
	if stamp != lwm2mCredentialsStamp("uid-1", secret("7", distinctPSK("completely-different"))) {
		t.Fatal("the stamp depends on the key: a digest of a secret in the release values is a guessing oracle")
	}
	if stamp == lwm2mCredentialsStamp("uid-1", secret("8", psk)) {
		t.Fatal("a new version of the Secret does not change the stamp, so the pods would not roll")
	}
	if stamp == lwm2mCredentialsStamp("uid-2", secret("7", psk)) {
		t.Fatal("two instances share a stamp: it is not salted")
	}
	if lwm2mCredentialsStamp("uid-1", nil) != "" {
		t.Fatal("an absent Secret has a stamp")
	}
	if stamp == sparkplugCredentialsStamp("uid-1", secret("7", psk)) {
		t.Fatal("the LwM2M and Sparkplug stamps share a domain")
	}
	bare := sha256.Sum256([]byte(psk))
	if stamp == fmt.Sprintf("%x", bare) {
		t.Fatal("the stamp is the bare SHA-256 of the key")
	}

	vals := helmValues(lwm2mState(lwm2mIDs(psk)))
	withLwm2mCredentialsChecksum(vals, stamp)
	if got := lwm2mPodAnnotations(t, vals)["checksum/area-credentials"]; got != stamp {
		t.Fatalf("the pod template does not carry the stamp: %v", got)
	}
}

// 🔴 AN UPGRADE KEEPS THE IDENTITIES WITHOUT READING ANY SECRET FROM THE RELEASE. The release
// holds no key; the carry puts the bindings back from it; the keys stay in the Secret dcctl wrote,
// which the upgrade leaves alone and reads only for the pods' stamp.
func TestAnUpgradeKeepsTheLwm2mIdentitiesWithoutReadingASecretFromTheRelease(t *testing.T) {
	t.Parallel()
	psk := distinctPSK("a")
	typed := versionedCluster()

	first := lwm2mState(lwm2mIDs(psk))
	stamp1, err := syncLwm2mIdentitiesSecret(context.Background(), typed, first, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	previous := helmValues(first)
	withLwm2mCredentialsChecksum(previous, stamp1)
	if anywhere(previous, psk) {
		t.Fatal("the release values hold the key")
	}
	if data, ok := lwm2mSecret(t, typed); !ok || string(data["DC_LWM2M_PSK_1"]) != psk {
		t.Fatalf("the Secret was not written: %v", data)
	}

	// The upgrade: no file, and the operator's overlay tunes the same area.
	st := lwm2mState(nil)
	st.Evolving = true
	st.ValuesOverlay = map[string]interface{}{
		"functionalAreas": map[string]interface{}{
			"lwm2m-ingest": map[string]interface{}{"resources": map[string]interface{}{"limits": map[string]interface{}{"memory": "512Mi"}}},
		},
	}
	fresh := authoringValues(st)
	if lwm2mLegacy, err := lwm2mLegacyPSKs(st.Instance, previous); err != nil || lwm2mLegacy != nil {
		t.Fatalf("an upgrade found keys in the release: %v %v", lwm2mLegacy, err)
	}
	stamp2, err := syncLwm2mIdentitiesSecret(context.Background(), typed, st, nil, releaseHoldsLwm2mIdentities(previous))
	if err != nil {
		t.Fatal(err)
	}
	withLwm2mCredentialsChecksum(fresh, stamp2)
	carryReleaseValues(fresh, previous)

	if stamp2 != stamp1 {
		t.Fatal("an upgrade that was given no file changed the pods' stamp, which would roll them for nothing")
	}
	if data, _ := lwm2mSecret(t, typed); string(data["DC_LWM2M_PSK_1"]) != psk {
		t.Fatalf("an upgrade disturbed the key Secret: %v", data)
	}
	if !releaseHoldsLwm2mIdentities(fresh) || !anywhere(fresh, "DC_LWM2M_PSK_1") || !anywhere(fresh, "dci-dctest-lwm2m-identities") {
		t.Fatalf("an upgrade dropped the identities: %v", fresh["functionalAreas"])
	}
	if anywhere(fresh, psk) {
		t.Fatal("the upgraded release holds the key")
	}
	area := fresh["functionalAreas"].(map[string]interface{})["lwm2m-ingest"].(map[string]interface{})
	if _, ok := area["resources"]; !ok {
		t.Fatalf("the overlay's tuning was lost by the carry: %v", area)
	}
	for _, d := range renderDocs(t, fresh) {
		if anywhere(d, psk) {
			t.Fatal("the upgraded manifest holds the key")
		}
	}
}

// A rotation replaces the dcctl-written Secret in place, moves the stamp (so the pods roll), and
// the file given again replaces the carried identities rather than being overwritten by them.
func TestAFileGivenAgainRotatesTheLwm2mSecretAndReplacesTheCarriedIdentities(t *testing.T) {
	t.Parallel()
	oldPSK, newPSK := distinctPSK("old"), distinctPSK("new")
	typed := versionedCluster()
	first := lwm2mState(lwm2mIDs(oldPSK))
	stamp1, err := syncLwm2mIdentitiesSecret(context.Background(), typed, first, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	previous := helmValues(first)
	withLwm2mCredentialsChecksum(previous, stamp1)

	ids := lwm2mIDs(newPSK)
	ids[0].Identity = "opaque-rotated"
	st := lwm2mState(ids)
	st.Evolving = true
	fresh := authoringValues(st)
	stamp2, err := syncLwm2mIdentitiesSecret(context.Background(), typed, st, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	withLwm2mCredentialsChecksum(fresh, stamp2)
	// What releaseValues does for a run that decides the identities.
	carryReleaseValues(fresh, withoutLwm2mIdentities(previous))

	if data, _ := lwm2mSecret(t, typed); string(data["DC_LWM2M_PSK_1"]) != newPSK {
		t.Fatalf("the Secret still holds the old key: %v", data)
	}
	if stamp1 == stamp2 {
		t.Fatal("a rotation did not change the stamp, so the pods would not roll")
	}
	if !anywhere(fresh, "opaque-rotated") || anywhere(fresh, `"opaque-1"`) {
		t.Fatalf("the carry overwrote the new identities: %v", fresh["functionalAreas"])
	}
	if anywhere(fresh, oldPSK) || anywhere(fresh, newPSK) {
		t.Fatal("a key is in the release")
	}
}

// Withdrawing (an explicit run with no identities) removes the identities from the release and the
// keys from the cluster, and the carry cannot put either back.
func TestWithdrawingTheLwm2mIdentitiesRemovesTheSecretAndTheCarriedLeaves(t *testing.T) {
	t.Parallel()
	typed := versionedCluster()
	first := lwm2mState(lwm2mIDs(distinctPSK("a")))
	stamp1, err := syncLwm2mIdentitiesSecret(context.Background(), typed, first, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	previous := helmValues(first)
	withLwm2mCredentialsChecksum(previous, stamp1)

	st := lwm2mState(nil)
	st.Evolving = true
	st.Lwm2mIdentitiesGiven = true // a withdrawal gives no identities and still decides
	fresh := authoringValues(st)
	stamp2, err := syncLwm2mIdentitiesSecret(context.Background(), typed, st, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	withLwm2mCredentialsChecksum(fresh, stamp2)
	carryReleaseValues(fresh, withoutLwm2mIdentities(previous))

	if _, ok := lwm2mSecret(t, typed); ok {
		t.Fatal("the withdrawn keys' Secret is still in the cluster")
	}
	if stamp2 != "" {
		t.Fatalf("a stamp is set for a Secret that is gone: %q", stamp2)
	}
	if releaseHoldsLwm2mIdentities(fresh) || anywhere(fresh, "DC_LWM2M_PSK_1") || anywhere(fresh, "credentialsChecksum") {
		t.Fatalf("the withdrawn identities were carried back: %v", fresh["functionalAreas"])
	}
}

// A Secret named like ours that dcctl did not write is neither deleted by a withdrawal nor
// overwritten by a rotation.
func TestALwm2mWithdrawalOrRotationLeavesAForeignSecretAlone(t *testing.T) {
	foreign := func() *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "dci-dctest-lwm2m-identities", Namespace: "dci-dctest"},
			Data:       map[string][]byte{"theirs": []byte("theirs")},
		}
	}
	for name, st := range map[string]*State{
		"withdrawal": func() *State { s := lwm2mState(nil); s.Lwm2mIdentitiesGiven = true; return s }(),
		"rotation":   lwm2mState(lwm2mIDs(distinctPSK("a"))),
	} {
		t.Run(name, func(t *testing.T) {
			typed := versionedCluster(foreign())
			if _, err := syncLwm2mIdentitiesSecret(context.Background(), typed, st, nil, false); err == nil {
				t.Fatal("a Secret dcctl did not write was changed")
			}
			data, ok := lwm2mSecret(t, typed)
			if !ok || string(data["theirs"]) != "theirs" || len(data) != 1 {
				t.Fatalf("the foreign Secret was touched: %v", data)
			}
		})
	}
}

// A run that keeps the identities but finds their Secret gone says so, rather than rolling the pods
// onto a reference that cannot resolve.
func TestAMissingLwm2mSecretIsAnErrorNotAnEmptyStamp(t *testing.T) {
	st := lwm2mState(nil)
	st.Evolving = true
	if _, err := syncLwm2mIdentitiesSecret(context.Background(), versionedCluster(), st, nil, true); err == nil ||
		!strings.Contains(err.Error(), "--lwm2m-identities") {
		t.Fatalf("a missing Secret under held identities was not refused with the way out: %v", err)
	}
	if stamp, err := syncLwm2mIdentitiesSecret(context.Background(), versionedCluster(), st, nil, false); err != nil || stamp != "" {
		t.Fatalf("an instance with no identities was refused: %q %v", stamp, err)
	}
}

// legacyLwm2mRelease is a release as the dcctl before this change recorded it: the key in
// extraSecrets (and so in `helm get values`), and the pods' projection naming the chart-rendered
// Secret.
func legacyLwm2mRelease(psk string) map[string]interface{} {
	return map[string]interface{}{
		"extraSecrets": []interface{}{
			map[string]interface{}{"name": "operator-secret", "stringData": map[string]interface{}{"K": "v"}},
			map[string]interface{}{"name": "dci-dctest-lwm2m-psk", "stringData": map[string]interface{}{"DC_LWM2M_PSK_1": psk}},
		},
		"functionalAreas": map[string]interface{}{"lwm2m-ingest": map[string]interface{}{
			"config": map[string]interface{}{"security": map[string]interface{}{"identities": []interface{}{
				map[string]interface{}{"identity": "opaque-1", "pskEnv": "DC_LWM2M_PSK_1", "tenant": "acme", "externalId": "plant-a/s1"},
			}}},
			"extraEnv": []interface{}{map[string]interface{}{
				"name": "DC_LWM2M_PSK_1",
				"valueFrom": map[string]interface{}{"secretKeyRef": map[string]interface{}{
					"name": "dci-dctest-lwm2m-psk", "key": "DC_LWM2M_PSK_1"}},
			}},
		}},
	}
}

// 🔴 MIGRATION: AN INSTANCE BUILT ON THE OLD LAYOUT ENDS WITH THE KEYS IN THE dcctl-WRITTEN SECRET,
// NOTHING SECRET IN THE NEW RELEASE'S VALUES, AND THE CHART-RENDERED SECRET GONE FROM THE MANIFEST
// (which is how Helm removes it) -- with the new Secret written first.
func TestAnInstanceOnTheOldLwm2mLayoutIsMigratedWithoutAWindowWithoutKeys(t *testing.T) {
	t.Parallel()
	psk := distinctPSK("legacy")
	previous := legacyLwm2mRelease(psk)
	typed := versionedCluster()

	// Step 1 (before the release): read the keys out of the old release and write them.
	legacy, err := lwm2mLegacyPSKs("dctest", previous)
	if err != nil || legacy["DC_LWM2M_PSK_1"] != psk {
		t.Fatalf("the keys were not read out of the old release: %v %v", legacy, err)
	}
	st := lwm2mState(nil)
	st.Evolving = true
	stamp, err := syncLwm2mIdentitiesSecret(context.Background(), typed, st, legacy, releaseHoldsLwm2mIdentities(previous))
	if err != nil {
		t.Fatal(err)
	}
	if data, ok := lwm2mSecret(t, typed); !ok || string(data["DC_LWM2M_PSK_1"]) != psk {
		t.Fatalf("the keys are not in the new Secret before the release changes: %v", data)
	}
	if stamp == "" {
		t.Fatal("the migrated instance's pods have no stamp")
	}

	// Step 2 (the release's values): the same sequence releaseValues runs for an upgrade.
	p2 := withoutLegacyLwm2mSecret(st.Instance, previous)
	vals := authoringValues(st)
	carryReleaseValues(vals, p2)
	pointLwm2mEnvAtTheIdentitiesSecret(st.Instance, vals)
	withLwm2mCredentialsChecksum(vals, stamp)

	if anywhere(vals, psk) {
		t.Fatal("the new release's values still hold the key")
	}
	if anywhere(vals, "dci-dctest-lwm2m-psk") {
		t.Fatal("the new release still names the chart-rendered Secret")
	}
	if !anywhere(vals, "dci-dctest-lwm2m-identities") {
		t.Fatal("the pods are not pointed at the new Secret")
	}
	if !releaseHoldsLwm2mIdentities(vals) {
		t.Fatal("the identities were lost in the migration")
	}
	// An operator's own extraSecrets entry is not ours to drop.
	if !anywhere(vals["extraSecrets"], "operator-secret") || anywhere(vals["extraSecrets"], "dci-dctest-lwm2m-psk") {
		t.Fatalf("extraSecrets after the migration: %v", vals["extraSecrets"])
	}
	// The old release record was not edited (it is Helm's, and shared with the carry).
	if !anywhere(previous, psk) || !anywhere(previous, "dci-dctest-lwm2m-psk") {
		t.Fatal("the migration edited the previous release's own record")
	}

	// Step 3 (the manifest): no chart-rendered key Secret, so Helm removes the old one; the pods
	// project the new one.
	var sawEnv bool
	for _, d := range renderDocs(t, vals) {
		meta, _ := d["metadata"].(map[string]interface{})
		if d["kind"] == "Secret" && meta["name"] == "dci-dctest-lwm2m-psk" {
			t.Fatal("the manifest still renders the legacy Secret, so Helm would keep it")
		}
		if anywhere(d, psk) {
			t.Fatal("the manifest holds the key")
		}
		if d["kind"] == "Deployment" && meta["name"] == "lwm2m-ingest" && anywhere(d, "dci-dctest-lwm2m-identities") {
			sawEnv = true
		}
	}
	if !sawEnv {
		t.Fatal("lwm2m-ingest does not project the new Secret")
	}

	// A second run over the migrated instance reads nothing from the release and changes nothing.
	again, err := lwm2mLegacyPSKs("dctest", vals)
	if err != nil || again != nil {
		t.Fatalf("the migrated release still holds keys: %v %v", again, err)
	}
	stamp2, err := syncLwm2mIdentitiesSecret(context.Background(), typed, st, nil, true)
	if err != nil || stamp2 != stamp {
		t.Fatalf("the second run moved the stamp (%q -> %q): %v", stamp, stamp2, err)
	}
}

// Migration reads a malformed legacy entry as an error, before anything is written.
func TestAMalformedLegacyLwm2mEntryStopsTheMigration(t *testing.T) {
	for name, entry := range map[string]map[string]interface{}{
		"no keys":      {"name": "dci-dctest-lwm2m-psk", "stringData": map[string]interface{}{}},
		"a non-string": {"name": "dci-dctest-lwm2m-psk", "stringData": map[string]interface{}{"DC_LWM2M_PSK_1": 7}},
		"an empty key": {"name": "dci-dctest-lwm2m-psk", "stringData": map[string]interface{}{"DC_LWM2M_PSK_1": ""}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lwm2mLegacyPSKs("dctest", map[string]interface{}{"extraSecrets": []interface{}{entry}}); err == nil {
				t.Fatal("a legacy entry that cannot be moved was accepted")
			}
		})
	}
}

// A bootstrap that is given no identities over an instance that has some is refused, unless it says
// it means to withdraw them.
func TestABootstrapThatWouldSilentlyDropLwm2mIdentitiesIsRefused(t *testing.T) {
	held := legacyLwm2mRelease(distinctPSK("a"))
	old := lwm2mReleaseValues
	t.Cleanup(func() { lwm2mReleaseValues = old })

	for name, tc := range map[string]struct {
		release map[string]interface{}
		st      State
		refused bool
	}{
		"given none, release holds identities":  {held, State{Instance: "i"}, true},
		"given a file":                          {held, State{Instance: "i", Lwm2mIdentitiesGiven: true}, false},
		"release holds none":                    {map[string]interface{}{}, State{Instance: "i"}, false},
		"a fresh install (no release)":          {nil, State{Instance: "i"}, false},
		"an upgrade is judged elsewhere":        {held, State{Instance: "i", Evolving: true}, false},
		"a rehearsal does not read the cluster": {held, State{Instance: "i", DryRun: true}, false},
	} {
		t.Run(name, func(t *testing.T) {
			lwm2mReleaseValues = func(*State) (map[string]interface{}, error) { return tc.release, nil }
			err := precheckLwm2mWithdrawal(context.Background(), &tc.st)
			if tc.refused != (err != nil) {
				t.Fatalf("refused=%v, want %v (%v)", err != nil, tc.refused, err)
			}
			if err != nil && !strings.Contains(err.Error(), "--withdraw-lwm2m-identities") {
				t.Fatalf("the refusal does not name the way out: %v", err)
			}
		})
	}
	if err := CheckLwm2mFlags(lwm2mIDs(distinctPSK("a")), true); err == nil {
		t.Fatal("--lwm2m-identities with --withdraw-lwm2m-identities was accepted")
	}
	if err := CheckLwm2mFlags(nil, true); err != nil {
		t.Fatalf("a withdrawal alone was refused: %v", err)
	}
}

// An overlay cannot set the identity bindings dcctl carries.
func TestAnOverlayStillCannotSetLwm2mIdentities(t *testing.T) {
	if why := refuseOverlayKey([]string{"functionalAreas", "lwm2m-ingest", "config", "security", "identities"}); why == "" {
		t.Fatal("an overlay may set the LwM2M identity bindings")
	}
	if why := refuseOverlayKey([]string{"functionalAreas", "lwm2m-ingest", "extraEnv"}); why == "" {
		t.Fatal("an overlay may set lwm2m-ingest's extraEnv")
	}
}
