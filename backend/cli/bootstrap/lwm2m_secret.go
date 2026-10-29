// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// Where the LwM2M pre-shared keys live: a Secret dcctl writes itself, never the Helm release.
//
// 🔴 A PSK IS NEVER A HELM VALUE. It used to be: dcctl put every key into extraSecrets[].stringData
// and the chart rendered the Secret, so `helm get values` printed them, every revision the release
// kept (helmMaxHistory) still held a key after a rotation, and an unsalted hash of them was stamped
// on every pod template. Now dcctl writes dci-<instance>-lwm2m-identities before the release and
// outside its manifest, the way it writes the Sparkplug broker Secret; the release carries only the
// Secret's NAME (the pods' secretKeyRef) and a stamp, derived from the Secret's version and salted
// per instance, that rolls the pods when a key changes. The runtime contract is unchanged: each key
// reaches lwm2m-ingest as the DC_LWM2M_PSK_<n> env var its identity's pskEnv names.
//
// The rest of the rendered identity (identity handle, tenant, external id, device type) is not
// secret and is CARRIED from the previous release by every later upgrade (carriedValuePaths); a
// run given --lwm2m-identities again replaces it and rewrites the Secret, and withdrawing is
// explicit (--withdraw-lwm2m-identities).
//
// MIGRATION. An instance built before this change holds its keys in the release (extraSecrets) and
// in a chart-rendered Secret, dci-<instance>-lwm2m-psk. The first run of this dcctl against it:
//
//  1. reads the keys out of the release values ONCE (lwm2mLegacyPSKs) and writes them to the new
//     Secret, before anything else changes;
//  2. rewrites the carried extraEnv to name the new Secret and drops the legacy extraSecrets entry
//     from the values;
//  3. upgrades the release, which rolls lwm2m-ingest onto the new Secret and, because the legacy
//     Secret is no longer in the manifest, has Helm delete it.
//
// The new Secret therefore exists before any pod refers to it, and the pods that remain on the old
// Secret already hold their keys in their environment. Helm's history is not rewritten: the
// revisions that predate this run still hold the old values, so an operator who needs those keys
// retired should rotate them after the upgrade.

import (
	"context"
	"fmt"
	"time"

	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const lwm2mAreaName = "lwm2m-ingest"

// lwm2mCredentialsStamp is the value stamped on the lwm2m-ingest pods so a rotated key rolls them:
// a secretKeyRef is read once, at container start. Like the Sparkplug stamp it is derived from the
// Secret's identity and version, never from a key or a digest of one.
func lwm2mCredentialsStamp(instanceUID string, s *corev1.Secret) string {
	return credentialsStamp("lwm2m-identities-stamp", instanceUID, s)
}

// syncLwm2mIdentitiesSecret settles the identities Secret for this run, before the release, and
// returns the stamp for the pods.
//
//   - A run GIVEN identities (or told to withdraw them) is the authority: the Secret is rewritten to
//     hold exactly its keys, and removed when the run has none.
//   - A run over a legacy-layout instance writes the keys it read out of the release (legacy) into
//     the new Secret: the migration.
//   - Any other run keeps what is there and only reads its identity for the stamp. If the release
//     holds identities (held) and the Secret is gone, that is an error and not a stamp of "": the
//     pods would otherwise be rolled onto a reference that cannot resolve.
func syncLwm2mIdentitiesSecret(ctx context.Context, typed kubernetes.Interface, st *State, legacy map[string]string, held bool) (string, error) {
	name, ns := lwm2mIdentitiesSecretName(st.Instance), InstanceNamespace(st.Instance)
	owner := instanceOwner(st.Instance, st.InstanceUID)
	var data map[string]string
	switch {
	case st.Lwm2mIdentitiesGiven:
		if data = lwm2mSecretData(st.Lwm2mIdentities); len(data) == 0 {
			return "", deleteOwnedSecretIfOurs(ctx, typed, owner, ns, name)
		}
	case len(legacy) > 0:
		data = legacy
	}
	if data != nil {
		if err := writeOwnedSecret(ctx, typed, owner, ownedSecret{
			Name: name, Namespace: ns, Type: corev1.SecretTypeOpaque,
			Labels: map[string]string{"devicechain.io/instance": st.Instance}, Data: data,
		}, time.Now); err != nil {
			return "", err
		}
	}
	s, err := typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if held && !st.Lwm2mIdentitiesGiven {
			return "", fmt.Errorf("instance %q runs LwM2M identities, but the Secret %s/%s that holds their pre-shared keys is gone. "+
				"Give --lwm2m-identities <file> to write it again", st.Instance, ns, name)
		}
		return "", nil
	case err != nil:
		return "", fmt.Errorf("reading Secret %s/%s, which holds the LwM2M pre-shared keys: %w", ns, name, err)
	}
	return lwm2mCredentialsStamp(st.InstanceUID, s), nil
}

// withLwm2mCredentialsChecksum stamps the credentials stamp into the install values, on a copy of
// the area's block: vals shares sub-maps with the authoring values.
func withLwm2mCredentialsChecksum(vals map[string]interface{}, stamp string) {
	if stamp == "" {
		return
	}
	fa := mergedStringKeyMap(vals["functionalAreas"], map[string]interface{}{})
	fa[lwm2mAreaName] = mergedStringKeyMap(fa[lwm2mAreaName], map[string]interface{}{"credentialsChecksum": stamp})
	vals["functionalAreas"] = fa
}

// withoutLwm2mIdentities returns the previous release's values without the leaves a run given the
// identities (or told to withdraw them) replaces, so the carry cannot put them back. It copies only
// the path it edits.
func withoutLwm2mIdentities(previous map[string]interface{}) map[string]interface{} {
	fa, ok := previous["functionalAreas"].(map[string]interface{})
	if !ok {
		return previous
	}
	area, ok := fa[lwm2mAreaName].(map[string]interface{})
	if !ok {
		return previous
	}
	newArea := make(map[string]interface{}, len(area))
	for k, v := range area {
		if k == "extraEnv" || k == "credentialsChecksum" {
			continue
		}
		if k == "config" {
			if cfg, ok := v.(map[string]interface{}); ok {
				c2 := make(map[string]interface{}, len(cfg))
				for ck, cv := range cfg {
					if ck == "security" {
						if sec, ok := cv.(map[string]interface{}); ok {
							s2 := make(map[string]interface{}, len(sec))
							for sk, sv := range sec {
								if sk != "identities" {
									s2[sk] = sv
								}
							}
							cv = s2
						}
					}
					c2[ck] = cv
				}
				v = c2
			}
		}
		newArea[k] = v
	}
	newFA := make(map[string]interface{}, len(fa))
	for k, v := range fa {
		newFA[k] = v
	}
	newFA[lwm2mAreaName] = newArea
	out := make(map[string]interface{}, len(previous))
	for k, v := range previous {
		out[k] = v
	}
	out["functionalAreas"] = newFA
	return out
}

// releaseHoldsLwm2mIdentities says whether a release's values carry LwM2M identities.
func releaseHoldsLwm2mIdentities(previous map[string]interface{}) bool {
	fa, _ := previous["functionalAreas"].(map[string]interface{})
	area, _ := fa[lwm2mAreaName].(map[string]interface{})
	cfg, _ := area["config"].(map[string]interface{})
	sec, _ := cfg["security"].(map[string]interface{})
	ids, _ := sec["identities"].([]interface{})
	return len(ids) > 0
}

// lwm2mLegacyPSKs reads the keys out of an instance built before dcctl wrote the Secret itself:
// the chart-rendered one's stringData, in the release values. It is the ONE place a key is read
// from the release, and it exists only so the migration can move them out. Nil when the release
// has no such entry, which is every instance this dcctl has built or already migrated.
func lwm2mLegacyPSKs(instance string, previous map[string]interface{}) (map[string]string, error) {
	list, _ := previous["extraSecrets"].([]interface{})
	for _, e := range list {
		entry, _ := e.(map[string]interface{})
		if entry["name"] != lwm2mLegacyPSKSecretName(instance) {
			continue
		}
		raw, _ := entry["stringData"].(map[string]interface{})
		out := make(map[string]string, len(raw))
		for k, v := range raw {
			s, ok := v.(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("the release's LwM2M Secret %q holds no usable value under %q; nothing has been changed", lwm2mLegacyPSKSecretName(instance), k)
			}
			out[k] = s
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("the release's LwM2M Secret %q holds no keys; nothing has been changed", lwm2mLegacyPSKSecretName(instance))
		}
		return out, nil
	}
	return nil, nil
}

// withoutLegacyLwm2mSecret returns the previous values without the extraSecrets entry that held the
// keys (other entries, which an operator may have set, stay). It copies only what it edits.
func withoutLegacyLwm2mSecret(instance string, previous map[string]interface{}) map[string]interface{} {
	list, ok := previous["extraSecrets"].([]interface{})
	if !ok {
		return previous
	}
	kept := make([]interface{}, 0, len(list))
	for _, e := range list {
		if entry, _ := e.(map[string]interface{}); entry["name"] == lwm2mLegacyPSKSecretName(instance) {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) == len(list) {
		return previous
	}
	out := make(map[string]interface{}, len(previous))
	for k, v := range previous {
		out[k] = v
	}
	if len(kept) == 0 {
		delete(out, "extraSecrets")
	} else {
		out["extraSecrets"] = kept
	}
	return out
}

// pointLwm2mEnvAtTheIdentitiesSecret rewrites the carried extraEnv of an instance that was built on
// the legacy layout so its secretKeyRefs name the Secret dcctl now writes. It builds new maps: the
// carried structures are the release's own record.
func pointLwm2mEnvAtTheIdentitiesSecret(instance string, vals map[string]interface{}) {
	fa, _ := vals["functionalAreas"].(map[string]interface{})
	area, _ := fa[lwm2mAreaName].(map[string]interface{})
	env, ok := area["extraEnv"].([]interface{})
	if !ok {
		return
	}
	legacy, current := lwm2mLegacyPSKSecretName(instance), lwm2mIdentitiesSecretName(instance)
	out := make([]interface{}, len(env))
	for i, e := range env {
		out[i] = e
		item, _ := e.(map[string]interface{})
		vf, _ := item["valueFrom"].(map[string]interface{})
		ref, _ := vf["secretKeyRef"].(map[string]interface{})
		if ref["name"] != legacy {
			continue
		}
		newRef := map[string]interface{}{}
		for k, v := range ref {
			newRef[k] = v
		}
		newRef["name"] = current
		newItem := map[string]interface{}{}
		for k, v := range item {
			newItem[k] = v
		}
		newItem["valueFrom"] = map[string]interface{}{"secretKeyRef": newRef}
		out[i] = newItem
	}
	newArea := make(map[string]interface{}, len(area))
	for k, v := range area {
		newArea[k] = v
	}
	newArea["extraEnv"] = out
	newFA := make(map[string]interface{}, len(fa))
	for k, v := range fa {
		newFA[k] = v
	}
	newFA[lwm2mAreaName] = newArea
	vals["functionalAreas"] = newFA
}

// errSilentLwm2mWithdrawal is the refusal of a bootstrap that would drop the identities.
func errSilentLwm2mWithdrawal(instance string) error {
	return fmt.Errorf("instance %q already runs LwM2M identities, and this bootstrap was given none, which would remove them "+
		"(a bootstrap composes the instance from its flags). Give --lwm2m-identities <file> to keep or replace them, "+
		"or --withdraw-lwm2m-identities to remove them. Nothing has been changed", instance)
}

// lwm2mReleaseValues reads the instance's release values; a seam for the withdrawal check.
var lwm2mReleaseValues = func(st *State) (map[string]interface{}, error) { return sparkplugReleaseValues(st) }

// CheckLwm2mFlags refuses the two ways of deciding the identities being given together.
func CheckLwm2mFlags(ids []Lwm2mIdentity, withdraw bool) error {
	if len(ids) > 0 && withdraw {
		return fmt.Errorf("--lwm2m-identities sets the LwM2M identities and --withdraw-lwm2m-identities removes them; give one")
	}
	return nil
}

// precheckLwm2mWithdrawal refuses, before anything is written, a bootstrap that is given no
// identities over a release that holds some. An unreadable release is said, not refused: a fresh
// install has nothing to read, and refusing on a read error would block it.
func precheckLwm2mWithdrawal(ctx context.Context, st *State) error {
	if st.Evolving || st.Lwm2mIdentitiesGiven || st.DryRun {
		return nil
	}
	previous, err := lwm2mReleaseValues(st)
	if err != nil {
		fmt.Println(color.YellowString("could not check the release for LwM2M identities: %v", err))
		return nil
	}
	if releaseHoldsLwm2mIdentities(previous) {
		return errSilentLwm2mWithdrawal(st.Instance)
	}
	return nil
}
