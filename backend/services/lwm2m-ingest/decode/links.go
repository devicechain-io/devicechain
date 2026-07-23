// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decode

import (
	"regexp"
	"strconv"
	"strings"
)

// MaxObservationsPerRegistration bounds how many object instances one registration can turn
// into observations. The /rd link list is device-controlled — a hostile or buggy client could
// register thousands of instances in the telemetry range (</3300/0>…</3300/9999>), each of
// which would become a serial CON Observe exchange plus a retained token handler, re-done on
// every re-Register. The cap is a DoS bound; instances beyond it are dropped and counted.
const MaxObservationsPerRegistration = 32

// ObjectRange is an inclusive range of LwM2M object ids treated as telemetry.
type ObjectRange struct {
	Lo, Hi int
}

// ObjectAllowlist decides which LwM2M objects are telemetry worth observing. It is a
// parameter (config supplies the concrete set at L2b) so the pure selector never hard-codes a
// policy: the allowlist keeps the server from issuing Observe requests against management
// objects (0 Security, 1 Server, 3 Device, …) — only sensor/telemetry objects are observed.
type ObjectAllowlist struct {
	Ranges []ObjectRange
}

// Permits reports whether an object id falls in the allowlist.
func (a ObjectAllowlist) Permits(objectID int) bool {
	for _, r := range a.Ranges {
		if objectID >= r.Lo && objectID <= r.Hi {
			return true
		}
	}
	return false
}

// DefaultObjectAllowlist is the IPSO Smart Object sensor range — the objects that carry
// numeric readings (3200 Digital Input … 3441 and neighbours). It deliberately excludes the
// OMA management objects (0/1/2/3/4/…). Config may override it.
var DefaultObjectAllowlist = ObjectAllowlist{Ranges: []ObjectRange{{Lo: 3200, Hi: 3441}}}

// linkTarget matches the path URI-Reference between the angle brackets of a CoRE Link
// (RFC 6690): `<...>`. Splitting the body on commas is unsafe (a comma can appear inside a
// quoted attribute value, `;rt="a,b"`), and so is matching any `<...>` (a `<` can appear inside
// a quoted value too, `;title="a<b"`, which would swallow the following link). An LwM2M
// registration target is always an absolute path — leading `/`, and containing none of
// `>`, `"`, `,` — so anchoring on that shape extracts exactly the link targets and skips
// quoted-attribute noise regardless of what it contains.
var linkTarget = regexp.MustCompile(`<(/[^>",]*)>`)

// Observations parses an LwM2M registration payload (CoRE Link Format, RFC 6690) and returns
// the object-instance paths worth observing: each `/object/instance` whose object is in the
// allowlist, de-duplicated, in registration order, capped at MaxObservationsPerRegistration.
// overflow is the number of allowed instances dropped by the cap (the DoS-relevant count);
// links outside the allowlist or not in `/object/instance` form are simply not telemetry and
// are not counted as overflow.
func Observations(rdBody []byte, allow ObjectAllowlist) (paths []string, overflow int) {
	seen := make(map[string]struct{})
	for _, m := range linkTarget.FindAllStringSubmatch(string(rdBody), -1) {
		objectID, path, ok := objectInstancePath(m[1])
		if !ok || !allow.Permits(objectID) {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		if len(paths) >= MaxObservationsPerRegistration {
			overflow++
			continue
		}
		paths = append(paths, path)
	}
	return paths, overflow
}

// objectInstancePath extracts the object id and the canonical `/object/instance` path from a
// CoRE link target, reporting false for anything that is not an object-instance reference (the
// root link `</>`, an object-only link `</3303>`, or a non-numeric segment). A resource-level
// link `</3303/0/5700>` is reduced to its instance `/3303/0` — we observe at the instance
// level so one notification carries the instance's whole SenML batch.
func objectInstancePath(target string) (objectID int, path string, ok bool) {
	trimmed := strings.Trim(target, "/")
	if trimmed == "" {
		return 0, "", false
	}
	segs := strings.Split(trimmed, "/")
	if len(segs) < 2 {
		return 0, "", false // object-only or malformed — not an instance
	}
	obj, err := strconv.Atoi(segs[0])
	if err != nil || obj < 0 || obj > 65535 {
		return 0, "", false
	}
	// LwM2M object-instance ids are 0–65534 (65535 is reserved); a negative (Atoi accepts
	// "-1") or out-of-range instance is a garbage link we would otherwise issue a doomed
	// Observe against on every re-Register.
	inst, err := strconv.Atoi(segs[1])
	if err != nil || inst < 0 || inst > 65534 {
		return 0, "", false
	}
	return obj, "/" + strconv.Itoa(obj) + "/" + strconv.Itoa(inst), true
}
