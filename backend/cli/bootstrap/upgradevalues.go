// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

// The values an upgrade cannot derive from the cluster, and has to take from the
// release it is replacing.
//
// 🔴 THESE EXIST BECAUSE `dcctl upgrade` DOES NOT RUN THE INFRASTRUCTURE APPLY, AND
// EVERY ONE OF THEM FAILS SILENTLY. They are not credentials and not topology — they
// are what an apply REPORTED, recorded in the release because nothing else keeps
// them. An upgrade that recomputed them from an apply it never ran would compute
// their zero values, and a zero value here is never an error:
//
//   - metrics.databaseBackups gates the WAL-archiving alerts. False renders no rules
//     at all, which looks exactly like an instance that does not archive.
//   - metrics.databaseNamespace scopes those rules to where the database Clusters
//     actually run. Wrong, and every rule selects no series and never fires.
//   - metrics.cnpgNamespace gates the database operator's PodMonitor and the
//     control-plane rules as a unit.
//   - the Grafana SSO client's bcrypt hash. Its cleartext went only to the Grafana
//     subchart and CANNOT be recovered (see stepRenderConfig), so re-minting the pair
//     would update one side of a login and break it permanently. Carrying the hash is
//     what makes "upgrade mints nothing" true for this credential too.
//
// 🔑 THE SHAPE TO NOTICE: each of these turns monitoring OFF by looking like a
// perfectly ordinary install. That is the reason they are carried explicitly and
// named here rather than left to whatever helmValues happens to produce from an empty
// State — an absence that renders cleanly is the kind this codebase has been bitten
// by most.
const grafanaSSOSeedClientID = "grafana"

// carryForwardFromRelease lifts those values out of the previous release's recorded
// values and into the State the new one is composed from.
//
// Absence is tolerated on purpose, and it is not the same tolerance in each case. An
// instance installed with monitoring off has no metrics values to carry and wants
// none; an instance with SSO off has no seeded client. What must not happen is an
// upgrade INVENTING any of them, so every one of these is copied or left alone —
// never defaulted.
func carryForwardFromRelease(st *State, previous map[string]interface{}) {
	metrics, _ := previous["metrics"].(map[string]interface{})
	if enabled, ok := metrics["databaseBackups"].(bool); ok && enabled {
		st.Values[databaseBackupsKey] = "true"
	}
	if ns, ok := metrics["databaseNamespace"].(string); ok && ns != "" {
		st.Values[databaseNamespaceKey] = ns
	}
	if ns, ok := metrics["cnpgNamespace"].(string); ok && ns != "" {
		st.Values[cnpgNamespaceKey] = ns
	}

	if hash := seededGrafanaClientHash(previous); hash != "" {
		st.Values["grafanaOAuthSecretBcrypt"] = hash
	}
}

// seededGrafanaClientHash digs the Grafana client's bcrypt hash out of the previous
// release's user-management configuration.
//
// It reads the value at the path helmValues writes it to, which is a coupling worth
// naming: this is the one credential recovered from a RENDERED structure rather than
// from a Secret or the declaration, because it has nowhere else to live. Giving it a
// home dcctl owns is the fix, and it is a design change rather than a read — until
// then, this path and the one that writes it move together.
func seededGrafanaClientHash(previous map[string]interface{}) string {
	areas, _ := previous["functionalAreas"].(map[string]interface{})
	um, _ := areas["user-management"].(map[string]interface{})
	cfg, _ := um["config"].(map[string]interface{})
	auth, _ := cfg["auth"].(map[string]interface{})
	clients, _ := auth["seedClients"].([]interface{})
	for _, c := range clients {
		client, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := client["clientId"].(string); id != grafanaSSOSeedClientID {
			continue
		}
		hash, _ := client["secretHash"].(string)
		return hash
	}
	return ""
}

// carriedValuePaths are the value blocks an upgrade copies WHOLE from the previous
// release, because the State they were rendered from cannot be reconstructed.
//
// 🔴 THE LwM2M PRE-SHARED KEYS ARE THE REASON THIS MECHANISM EXISTS AT ALL. They are
// supplied by a file at bootstrap (--lwm2m-identities) and rendered into a
// chart-owned Secret; nothing records the file. Recomputing helmValues without them
// does not leave the Secret alone — it takes the Secret OUT OF THE MANIFEST, and Helm
// deletes what leaves the manifest. Every provisioned device would lose its
// credential on an ordinary version bump.
//
// Copied as opaque structures rather than parsed back into State: reconstructing the
// identities from their own rendering would mean depending on the exact shape
// lwm2mProvisioning emits, which is the fragile direction. The values are carried
// forward byte for byte, and the next bootstrap is what changes them.
var carriedValuePaths = [][]string{
	{"extraSecrets"},
	{"functionalAreas", "lwm2m-ingest"},
}

// carryReleaseValues copies those blocks into the freshly computed values, leaving
// anything the new computation already decided alone.
//
// 🔴 IT MUST NOT OVERWRITE. The fresh values are what this upgrade has DECIDED; the
// carried ones are what it has no opinion about. An LwM2M block computed by this run
// — because the operator supplied the file again — is the newer answer, and taking
// the release's copy over it would make the flag inert.
func carryReleaseValues(vals, previous map[string]interface{}) {
	for _, path := range carriedValuePaths {
		src := previous
		ok := true
		for _, step := range path[:len(path)-1] {
			if src, ok = src[step].(map[string]interface{}); !ok {
				break
			}
		}
		if !ok || src == nil {
			continue
		}
		leaf := path[len(path)-1]
		value, present := src[leaf]
		if !present {
			continue
		}

		dst := vals
		for _, step := range path[:len(path)-1] {
			next, isMap := dst[step].(map[string]interface{})
			if !isMap {
				next = map[string]interface{}{}
				dst[step] = next
			}
			dst = next
		}
		if _, already := dst[leaf]; already {
			continue
		}
		dst[leaf] = value
	}
}
