// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package sim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
)

// MetricSpec is one numeric, unit-bearing metric a ProfileSpec declares (ADR-016).
// DataType uses the device-management MetricDataType vocabulary verbatim
// ("DOUBLE"/"INT"/"BOOLEAN"/"STRING") so bootstrap.go can pass it straight
// through to createMetricDefinition without translation.
type MetricSpec struct {
	Key      string // metricKey — the wire name a measurement entry keys by
	Name     string
	DataType string
	Unit     string
}

// ProfileSpec is a static-singleton device profile (+ its metric definitions) a
// manifest provisions once and publishes. Profiles are shared capability
// contracts (ADR-045) — a manifest declares few of them even when it fans out
// many devices.
type ProfileSpec struct {
	Token    string
	Name     string
	Category string
	Metrics  []MetricSpec
}

// DeviceTypeSpec is a static-singleton device type referencing a profile.
type DeviceTypeSpec struct {
	Token        string
	Name         string
	ProfileToken string
}

// PopulationSpec is the populations seam (ADR-050): a declarative "N devices of
// this type" spec, rendered deterministically by Expand. TokenPattern and
// ExternalIdPattern each support one "{n:0Wd}" placeholder (W = zero-pad width;
// "{n}" with no width is unpadded), filled with the 1-based instance index.
//
// distributeAcross (spreading a population over areas/customers) and per-metric
// emit cadence are intentionally left unimplemented — the field space is
// reserved by the comment here, not by dead struct fields, so a later slice adds
// them without a breaking reshape.
type PopulationSpec struct {
	OfType            string // device-type token this population instantiates
	Count             int
	TokenPattern      string
	ExternalIdPattern string
}

// DeviceInstance is one concrete device Expand renders from a PopulationSpec:
// its addressing token, its business-id externalId (ADR-049), the device type
// it belongs to, and the ACCESS_TOKEN credential bootstrap.go will provision for
// it (CredentialToken is the DeviceCredential's own token; CredentialId is the
// bearer value a device presents, ADR-014).
type DeviceInstance struct {
	Token           string
	ExternalId      string
	DeviceTypeToken string
	CredentialToken string
	CredentialId    string
}

// SimManifest is the whole declarative shape of one sim scenario: the static
// singletons (profiles + device types) provisioned once, and the populations
// that fan out into concrete devices. Seed is the ONLY source of non-pattern
// randomness (credential material derivation) — Expand never consults the
// system clock or crypto/rand, so (manifest, seed) deterministically reproduces
// the same tokens/externalIds/credentials on every run (ADR-050), which is what
// makes bootstrap's create-or-ignore idempotent across resets.
type SimManifest struct {
	Name        string
	Seed        int64
	Profiles    []ProfileSpec
	DeviceTypes []DeviceTypeSpec
	Populations []PopulationSpec
}

// placeholderPattern matches "{n}" or "{n:0Wd}" (W one or more digits).
var placeholderPattern = regexp.MustCompile(`\{n(?::0(\d+)d)?\}`)

// renderPattern fills a TokenPattern/ExternalIdPattern's "{n}"/"{n:0Wd}"
// placeholder with a 1-based index. Pure string formatting — no randomness.
func renderPattern(pattern string, n int) string {
	return placeholderPattern.ReplaceAllStringFunc(pattern, func(match string) string {
		sub := placeholderPattern.FindStringSubmatch(match)
		if sub[1] == "" {
			return strconv.Itoa(n)
		}
		width, err := strconv.Atoi(sub[1])
		if err != nil {
			return strconv.Itoa(n)
		}
		return fmt.Sprintf("%0*d", width, n)
	})
}

// deriveCredential deterministically derives an ACCESS_TOKEN credential's own
// token and bearer id from (seed, deviceToken). Both are lowercase hex — always
// grammar-safe (core.ValidateToken) regardless of what characters the device
// token itself contains, and stable across resets since they depend only on
// values already fixed by the manifest + seed.
func deriveCredential(seed int64, deviceToken string) (credentialToken, credentialId string) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:cred", seed, deviceToken)))
	digest := hex.EncodeToString(sum[:])
	return deviceToken + "-cred", digest[:32]
}

// Expand deterministically renders every PopulationSpec into concrete
// DeviceInstances. The same (manifest, seed) always yields identical output —
// no unseeded randomness anywhere in this path (ADR-050).
func (m SimManifest) Expand(seed int64) []DeviceInstance {
	instances := make([]DeviceInstance, 0)
	for _, pop := range m.Populations {
		for n := 1; n <= pop.Count; n++ {
			token := renderPattern(pop.TokenPattern, n)
			externalId := ""
			if strings.TrimSpace(pop.ExternalIdPattern) != "" {
				externalId = renderPattern(pop.ExternalIdPattern, n)
			}
			credToken, credId := deriveCredential(seed, token)
			instances = append(instances, DeviceInstance{
				Token:           token,
				ExternalId:      externalId,
				DeviceTypeToken: pop.OfType,
				CredentialToken: credToken,
				CredentialId:    credId,
			})
		}
	}
	return instances
}

// Validate checks the manifest's static shape and every token Expand would
// render, so a malformed pattern (e.g. one producing an empty or grammar-unsafe
// token) fails fast at bootstrap rather than surfacing as an opaque GraphQL
// error deep in provisioning.
func (m SimManifest) Validate() error {
	profileTokens := make(map[string]bool, len(m.Profiles))
	for _, p := range m.Profiles {
		if err := core.ValidateToken(p.Token); err != nil {
			return fmt.Errorf("profile token: %w", err)
		}
		for _, mx := range p.Metrics {
			// Mirrors bootstrap.go's ensureMetricDefinition token derivation
			// (profileToken + "-" + metricKey) so a grammar-unsafe metric key
			// fails here rather than deep inside a GraphQL round-trip.
			if err := core.ValidateToken(p.Token + "-" + mx.Key); err != nil {
				return fmt.Errorf("metric definition token: %w", err)
			}
		}
		profileTokens[p.Token] = true
	}
	typeTokens := make(map[string]bool, len(m.DeviceTypes))
	for _, dt := range m.DeviceTypes {
		if err := core.ValidateToken(dt.Token); err != nil {
			return fmt.Errorf("device type token: %w", err)
		}
		if dt.ProfileToken != "" && !profileTokens[dt.ProfileToken] {
			return fmt.Errorf("device type %q references unknown profile %q", dt.Token, dt.ProfileToken)
		}
		typeTokens[dt.Token] = true
	}
	for _, pop := range m.Populations {
		if !typeTokens[pop.OfType] {
			return fmt.Errorf("population references unknown device type %q", pop.OfType)
		}
		if pop.Count < 0 {
			return fmt.Errorf("population for %q has a negative count", pop.OfType)
		}
	}
	for _, d := range m.Expand(m.Seed) {
		if err := core.ValidateToken(d.Token); err != nil {
			return fmt.Errorf("rendered device token: %w", err)
		}
		if err := core.ValidateToken(d.CredentialToken); err != nil {
			return fmt.Errorf("rendered credential token: %w", err)
		}
	}
	return nil
}
