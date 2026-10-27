// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// The operator tuning path: `dcctl bootstrap|upgrade --values <file.yaml>`.
//
// An operator tunes an instance (replica counts, resources, an area's own configuration)
// by handing dcctl a values overlay. dcctl merges it OVER the values it computes, and
// RECORDS it on the instance's declaration so the next `dcctl upgrade` re-applies it
// without being asked. Without that record every upgrade rebuilds the release's values
// from the declaration alone and the tuning is reverted -- silently, and on the very
// verb an operator runs to be safe.
//
// 🔴 THREE DECISIONS, EACH OF WHICH HAS A REVISIT-IF IN THE PULL REQUEST THAT SHIPPED IT.
//
//  1. WHERE IT LIVES: an annotation on the Instance declaration (AnnotationValuesOverlay).
//     Not the Helm release (the release holds dcctl's computed values and the overlay merged
//     together, and an upgrade cannot tell which keys were the operator's), not a file on this
//     machine (an upgrade from another machine would revert it), and not a Secret (the
//     overlay holds no credentials -- the allowlist below has no room for one). The
//     declaration is what every other fact about the instance's shape already lives in, it
//     is read by every upgrade before anything else, and it goes when the instance goes.
//
//  2. WHAT IT MAY SET: an ALLOWLIST of tunable subtrees, not a denylist of owned ones. A
//     denylist is correct on the day it is written and wrong the first time the chart grows
//     a key: a new credential, projection or security switch would be operator-settable
//     until somebody remembered to list it. An allowlist fails the other way -- a new tunable
//     is refused until it is added -- and that failure is loud, named and fixed in one line.
//     What dcctl OWNS (images, credentials, broker and database coordinates, ingress host and
//     TLS, the area set, what a flag already decides) is therefore refused by omission, and
//     TestTunableValuesNeverCollideWithWhatDcctlSets holds that by GENERATING the set dcctl
//     writes from helmValues and checking the allowlist against it.
//
//  3. WHERE IT SITS IN THE MERGE: chart defaults < what dcctl computes < the overlay. The
//     overlay is merged into the AUTHORING values (authoringValues) rather than the install
//     values, because the shutdown budget and the area set are read by the chart while it
//     composes the instance configuration document.

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chartutil"
	"sigs.k8s.io/yaml"
)

// AnnotationValuesOverlay is the declaration annotation holding the operator's overlay, as
// JSON. Absent means no overlay. Written only by dcctl (recordValuesOverlay); a value that
// does not parse is refused rather than ignored, because ignoring it is the revert this
// whole path exists to prevent.
const AnnotationValuesOverlay = "core.devicechain.io/values-overlay"

// maxValuesOverlayBytes bounds the serialised overlay. Kubernetes caps ALL of an object's
// annotations at 256 KiB together, and the declaration carries others; half of that leaves
// room for them and is far above any tuning overlay.
const maxValuesOverlayBytes = 128 * 1024

// tunableValues are the value subtrees an operator may set. A path is a prefix match on
// dotted segments; "*" matches exactly one segment (an area name).
//
// 🔴 WHAT IS NOT HERE IS THE POINT. image, instance, profile, enabledFunctionalAreas,
// extraSecrets, blobStorage, the rest of ingress and metrics and networkPolicy, the pod and
// container security contexts, an area's image / extraEnv / extraPorts and its profiler
// listener are all dcctl's or the declaration's. None of them is a tuning knob: they are
// credentials, wiring, exposure or a way to weaken the pod's isolation.
var tunableValues = []string{
	// Scale and rollout.
	"replicas",
	"rollingUpdate",
	"terminationGracePeriodSeconds",
	"nodeLossTolerationSeconds",
	"shutdownDrainSeconds",
	"podDisruptionBudget",
	"startupProbe",
	"livenessProbe",
	// Resources and the Go runtime's memory behaviour.
	"resources",
	"useMeasuredRequests",
	"goMemLimitPercent",
	"gogc",
	"goMemLimit",
	// The console's own pods.
	"frontend.replicas",
	"frontend.resources",
	// Alerting rules and dashboards.
	"metrics.alerts",
	"metrics.grafanaDashboards",
	// How the ingress is fronted, not where it points or whether it is encrypted.
	//
	// className, annotations and uploadMaxBodySize are the Ingress front end's: dcctl retires
	// them for an instance on the Gateway (effectiveOverlay). Which front end serves the
	// host (ingress.controller) and the cutover window (ingress.legacy) are dcctl's, so they are
	// refused here by omission.
	"ingress.className",
	"ingress.annotations",
	"ingress.uploadMaxBodySize",
	"ingress.gateway.className",
	"ingress.gateway.annotations",
	"networkPolicy.additionalAllowedCidrs",
	"serviceAccount.annotations",
	// One area's own scale, resources and configuration.
	"functionalAreas.*.replicas",
	"functionalAreas.*.strategy",
	"functionalAreas.*.resources",
	"functionalAreas.*.measuredRequests",
	"functionalAreas.*.goMemLimitPercent",
	"functionalAreas.*.memoryLimitFloor",
	"functionalAreas.*.gogc",
	"functionalAreas.*.goMemLimit",
	"functionalAreas.*.avoidEventStorePrimary",
	"functionalAreas.*.eventPathSpread",
	"functionalAreas.*.config",
}

// ownedWithinTunable are values INSIDE a tunable subtree that dcctl nonetheless owns.
// The reason is printed to the operator, so it says what to do instead.
var ownedWithinTunable = []struct {
	path   string
	reason string
}{
	{"functionalAreas.event-management.replicas",
		"the event-management replica count is set by --ha, and the event store's connection reserve is sized to it; changing it here would leave the reserve counting a different number of pods"},
	// 🔴 AN AREA'S CONFIG IS TUNABLE AS A WHOLE, SO WHAT INSIDE IT IS NOT HAS TO BE SAID HERE.
	// These are its credentials, wiring, trust anchors and security switches: each one is
	// either decided elsewhere (by a flag, by the declaration, by the instance's own
	// identity) or weakens a check, and none is a dial an operator turns for capacity.
	// Matched case-insensitively, because the services bind their configuration as
	// encoding/json does.
	{"functionalAreas.*.config.tsdbConfiguration",
		"an area's event-store pool is not tunable: the event store's connection reserve is fixed per event-management pod, and a larger pool would take connections the reserve does not hold"},
	{"functionalAreas.*.config.rdbConfiguration.sqlDebug",
		"SQL debug logging writes statements with their bound values (credentials, sealed secrets, tenant data) to the logs"},
	{"functionalAreas.device-management.config.deviceAuthMode",
		"device authentication is a security switch, not a tuning value: relaxing it accepts events from devices that present no credential"},
	{"functionalAreas.user-management.config.auth",
		"the identity service's issuer, signing keys, seeded clients and token lifetimes are the instance's identity, not tuning"},
	{"functionalAreas.mcp.config.resourceUrl",
		"the MCP server's resource URL is the audience every token is bound to; it follows the instance's host"},
	{"functionalAreas.mcp.config.issuerUrl",
		"the MCP server's issuer is the authorization server tokens are trusted from; it follows the instance's host"},
	{"functionalAreas.update-management.config.updates.allowUnverifiedArtifacts",
		"assigning unverified firmware turns off the signature check on every update"},
	{"functionalAreas.update-management.config.updates.objectStore",
		"whether update-management uses the instance object store follows how the store is mounted: it is set by dcctl where the store's volume cannot be shared with it, and is not a tuning value"},
	{"functionalAreas.update-management.config.updates.downloadBaseURL",
		"the download URL devices are sent to follows the instance's host"},
	{"functionalAreas.event-sources.config.eventSources",
		"event-sources' listeners and broker connections are wiring, not tuning"},
	{"functionalAreas.sparkplug-ingest.config.sources",
		"sparkplug-ingest's broker connections, credential references and tenant bindings are wiring, not tuning"},
}

// refusedIngressAnnotations are ingress.annotations keys an overlay may not set. The chart
// writes its overlay annotations AFTER its own on every Ingress, and a repeated key is not
// refused by the render -- so an overlay naming a key the chart sets would REPLACE it: the access
// log kept off the download route (its capability rides in the query string), the upload
// route's body limit, the TLS issuer. TestNoOverlayAnnotationReplacesOneTheChartSets renders
// the chart and holds this list to what it actually sets. Snippets (nginx configuration
// injected verbatim; the chart sets none, and the shared controller is to refuse them) and turning the TLS redirect off are refused for the same reason: the
// ingress host and its TLS are dcctl's.
var refusedIngressAnnotations = []string{
	"nginx.ingress.kubernetes.io/rewrite-target",
	"nginx.ingress.kubernetes.io/configuration-snippet",
	"nginx.ingress.kubernetes.io/enable-access-log",
	"nginx.ingress.kubernetes.io/proxy-body-size",
	"nginx.ingress.kubernetes.io/proxy-request-buffering",
	"nginx.ingress.kubernetes.io/limit-rps",
	"nginx.ingress.kubernetes.io/ssl-redirect",
	"nginx.ingress.kubernetes.io/force-ssl-redirect",
}

// refuseIngressAnnotation returns why an ingress annotation key may not be set, or "".
func refuseIngressAnnotation(key string) string {
	k := strings.ToLower(key)
	switch {
	case strings.HasPrefix(k, "cert-manager.io/"):
		return "the TLS certificate's issuer is dcctl's (it follows --no-tls and the cluster's issuer)"
	case strings.HasSuffix(k, "-snippet"):
		return "a snippet injects nginx configuration verbatim, and can undo the routes the chart refuses at the edge"
	}
	for _, r := range refusedIngressAnnotations {
		if k == r {
			return "the chart sets this annotation itself (or it turns the TLS redirect off), and an overlay value would replace the chart's"
		}
	}
	return ""
}

// presetDefaults are values dcctl writes as a PRESET DEFAULT that the overlay may
// deliberately replace, because they are the dials a sizing preset exists to move. Every
// other value dcctl writes is owned by it. TestTunableValuesNeverCollideWithWhatDcctlSets
// reads this list.
var presetDefaults = []string{
	"resources.requests.cpu",
	"resources.requests.memory",
	"useMeasuredRequests",
}

func splitPath(p string) []string { return strings.Split(p, ".") }

// pathHasPrefix reports whether path begins with pattern, where a "*" pattern segment
// matches any one segment.
func pathHasPrefix(path, pattern []string) bool {
	if len(path) < len(pattern) {
		return false
	}
	for i, seg := range pattern {
		if seg != "*" && seg != path[i] {
			return false
		}
	}
	return true
}

// overlayLeaves returns every leaf of an overlay as a path, in sorted order. An empty map
// is a no-op and yields nothing; an array or scalar (or null) is a leaf.
func overlayLeaves(m map[string]interface{}) [][]string {
	var out [][]string
	var walk func(prefix []string, v map[string]interface{})
	walk = func(prefix []string, v map[string]interface{}) {
		for k, child := range v {
			p := append(append([]string(nil), prefix...), k)
			if sub, ok := child.(map[string]interface{}); ok {
				walk(p, sub)
				continue
			}
			out = append(out, p)
		}
	}
	walk(nil, m)
	sort.Slice(out, func(i, j int) bool { return strings.Join(out[i], ".") < strings.Join(out[j], ".") })
	return out
}

// pathsOverlap reports whether one path is a prefix of the other, either way round, with
// "*" in either matching any one segment and segments compared case-insensitively. Either
// way round, because a leaf ABOVE a protected value (a null, or a scalar, over its parent)
// replaces it as surely as a leaf AT it; case-insensitively, because below an area's config
// the services bind keys as encoding/json does, and a check that `capture` passed while
// `Capture` slipped by would be no check.
func pathsOverlap(a, b []string) bool {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != "*" && b[i] != "*" && !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// refuseOverlayKey returns why this leaf may not be set from an overlay, or "" when it may.
func refuseOverlayKey(path []string) string {
	// What an upgrade copies from the previous release by itself. An overlay that touched
	// one would either be overwritten by the carry or, worse, make the carry skip.
	for _, carried := range carriedValuePaths {
		if pathsOverlap(path, carried) {
			return "dcctl carries this value from the previous release itself (it is set by --lwm2m-identities, or is provisioning state), so an overlay cannot set it"
		}
	}
	for _, owned := range ownedWithinTunable {
		if pathsOverlap(path, splitPath(owned.path)) {
			return owned.reason
		}
	}
	if len(path) == 3 && path[0] == "ingress" && path[1] == "annotations" {
		if why := refuseIngressAnnotation(path[2]); why != "" {
			return why
		}
	}
	// The Gateway's annotations are written after the chart's own, like the Ingress objects',
	// so the one it sets (the TLS issuer) must not be replaceable from here either.
	if len(path) == 4 && path[0] == "ingress" && path[1] == "gateway" && path[2] == "annotations" {
		if strings.HasPrefix(strings.ToLower(path[3]), "cert-manager.io/") {
			return "the TLS certificate's issuer is dcctl's (it follows --no-tls and the cluster's issuer)"
		}
	}
	for _, ok := range tunableValues {
		if pathHasPrefix(path, splitPath(ok)) {
			return ""
		}
	}
	return "this is not an operator-tunable value: dcctl or the chart owns it (images, credentials, endpoints, exposure, the area set, security contexts)"
}

// refuseOverlayKeys checks every leaf of an overlay and reports ALL the ones it refuses,
// each by its dotted key, in one error -- an operator fixing a file should not have to
// discover its problems one run at a time.
func refuseOverlayKeys(overlay map[string]interface{}, source string) error {
	var lines []string
	for _, leaf := range overlayLeaves(overlay) {
		if why := refuseOverlayKey(leaf); why != "" {
			lines = append(lines, fmt.Sprintf("  %s: %s", strings.Join(leaf, "."), why))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return fmt.Errorf("%s sets values dcctl does not let an overlay set, so nothing has been changed:\n%s\n"+
		"An overlay may set only tunable values (see 'Tuning an instance' in the documentation).",
		source, strings.Join(lines, "\n"))
}

// overlaySchemaProbe is the minimum a values document needs for the chart's schema to
// judge an overlay on its own: the required values dcctl computes for a real run.
func overlaySchemaProbe() map[string]interface{} {
	return map[string]interface{}{
		"instance": map[string]interface{}{"id": "overlay-check"},
		"image":    map[string]interface{}{"registry": DefaultImageRegistry, "tag": "overlay-check"},
		"ingress":  map[string]interface{}{"host": "overlay-check.invalid"},
	}
}

// ValidateValuesOverlay judges an overlay against this build's chart: the key allowlist,
// then values.schema.json over the chart's own defaults with the overlay on top. It reads
// nothing from a cluster, so it runs before anything exists to change.
func ValidateValuesOverlay(overlay map[string]interface{}) error {
	if err := refuseOverlayKeys(overlay, "the values overlay"); err != nil {
		return err
	}
	ch, err := loadEmbeddedChart()
	if err != nil {
		return fmt.Errorf("loading the chart to validate the values overlay: %w", err)
	}
	probe := overlaySchemaProbe()
	mergeOverlay(probe, deepCopyValues(overlay))
	coalesced, err := chartutil.CoalesceValues(ch, probe)
	if err != nil {
		return fmt.Errorf("the values overlay does not combine with the chart's defaults: %w", err)
	}
	if err := chartutil.ValidateAgainstSchema(ch, coalesced); err != nil {
		return fmt.Errorf("the values overlay is refused by the chart's values schema, so nothing has been changed:\n%w", err)
	}
	return nil
}

// LoadValuesOverlay reads --values files in order (later files win, keys merged) and
// validates the result. Every file is checked on its own first so a refusal names the
// file it came from.
func LoadValuesOverlay(files []string) (map[string]interface{}, error) {
	merged := map[string]interface{}{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("--values %s: %w", f, err)
		}
		if len(raw) > maxValuesOverlayBytes {
			return nil, fmt.Errorf("--values %s is %d bytes; an overlay is recorded on the instance and may not exceed %d",
				f, len(raw), maxValuesOverlayBytes)
		}
		var one map[string]interface{}
		if err := yaml.UnmarshalStrict(raw, &one); err != nil {
			return nil, fmt.Errorf("--values %s: not a YAML mapping of values: %w", f, err)
		}
		if len(overlayLeaves(one)) == 0 {
			return nil, fmt.Errorf("--values %s sets no values. To remove the overlay recorded on an instance, "+
				"use `dcctl upgrade --reset-values`", f)
		}
		if err := refuseOverlayKeys(one, "--values "+f); err != nil {
			return nil, err
		}
		mergeOverlay(merged, one)
	}
	if err := ValidateValuesOverlay(merged); err != nil {
		return nil, err
	}
	if _, err := encodeValuesOverlay(merged); err != nil {
		return nil, err
	}
	return merged, nil
}

// mergeOverlay merges src into dst: maps merge key by key, anything else (a scalar, an
// array, null) replaces. It is the merge `helm --values` performs between two files.
func mergeOverlay(dst, src map[string]interface{}) {
	for k, v := range src {
		if sm, ok := v.(map[string]interface{}); ok {
			if dm, ok := dst[k].(map[string]interface{}); ok {
				mergeOverlay(dm, sm)
				continue
			}
			dst[k] = deepCopyValues(sm)
			continue
		}
		dst[k] = v
	}
}

func deepCopyValues(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]interface{}); ok {
			out[k] = deepCopyValues(sub)
			continue
		}
		out[k] = v
	}
	return out
}

// authoringValues is the value map a run renders from: what dcctl computes, with the
// operator's overlay merged over it.
//
// 🔴 ONE FUNCTION FOR EVERY CALLER THAT RENDERS FOR REAL (releaseValues, the pre-flight
// renders). A pre-flight that rendered helmValues alone would approve a run the install
// then refuses, or the reverse.
func authoringValues(st *State) map[string]interface{} {
	vals := helmValues(st)
	if overlay := effectiveOverlay(st); len(overlay) > 0 {
		mergeOverlay(vals, overlay)
	}
	return vals
}

// encodeValuesOverlay is the annotation form: compact JSON, bounded.
func encodeValuesOverlay(overlay map[string]interface{}) (string, error) {
	raw, err := json.Marshal(overlay)
	if err != nil {
		return "", fmt.Errorf("recording the values overlay: %w", err)
	}
	if len(raw) > maxValuesOverlayBytes {
		return "", fmt.Errorf("the values overlay is %d bytes once recorded; the limit is %d", len(raw), maxValuesOverlayBytes)
	}
	return string(raw), nil
}

// decodeValuesOverlay reads the annotation back. Absent is no overlay; present and
// unreadable is an error, never an empty overlay.
func decodeValuesOverlay(annotations map[string]string) (map[string]interface{}, error) {
	raw, ok := annotations[AnnotationValuesOverlay]
	if !ok || raw == "" {
		return nil, nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("the values overlay recorded on this instance (annotation %s) cannot be read: %w. "+
			"Replace it with --values <file>, or remove it with --reset-values", AnnotationValuesOverlay, err)
	}
	return out, nil
}

// diffOverlays lists what moving from old to new changes, one line per leaf, as
// "+ key: value" (added), "- key: value" (removed) and "~ key: old -> new" (changed).
func diffOverlays(old, new map[string]interface{}) []string {
	flat := func(m map[string]interface{}) map[string]string {
		out := map[string]string{}
		for _, leaf := range overlayLeaves(m) {
			var cur interface{} = m
			for _, seg := range leaf {
				cur = cur.(map[string]interface{})[seg]
			}
			b, _ := json.Marshal(cur)
			out[strings.Join(leaf, ".")] = string(b)
		}
		return out
	}
	o, n := flat(old), flat(new)
	keys := map[string]bool{}
	for k := range o {
		keys[k] = true
	}
	for k := range n {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	var lines []string
	for _, k := range sorted {
		ov, inOld := o[k]
		nv, inNew := n[k]
		switch {
		case inOld && !inNew:
			lines = append(lines, fmt.Sprintf("- %s: %s", k, ov))
		case !inOld && inNew:
			lines = append(lines, fmt.Sprintf("+ %s: %s", k, nv))
		case ov != nv:
			lines = append(lines, fmt.Sprintf("~ %s: %s -> %s", k, ov, nv))
		}
	}
	return lines
}
