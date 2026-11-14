// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package backup

import (
	"fmt"
	"slices"

	"github.com/devicechain-io/dc-k8s/functionalarea"
	"github.com/devicechain-io/dc-microservice/backupset"
	"github.com/devicechain-io/dc-microservice/streams"
)

func validateAreas(areas []string) error {
	if len(areas) == 0 {
		return fmt.Errorf("enabled functional areas must be explicit and nonempty")
	}
	typed := make([]functionalarea.FunctionalArea, len(areas))
	for i, a := range areas {
		typed[i] = functionalarea.FunctionalArea(a)
	}
	return functionalarea.Validate(typed)
}

// Match ReconcileBroker: writers and consuming readers open a stream; live and
// ephemeral readers do not. A dormant declaration is not deployed yet.
func streamEnabled(suffix string, areas []string) bool {
	for _, st := range streams.All {
		if st.Suffix == suffix && st.Dormant {
			return false
		}
	}
	for _, a := range streams.AdminsOf(suffix) {
		if slices.Contains(areas, a) {
			return true
		}
	}
	return false
}

// Legacy v1 manifests without area metadata retain their original strict
// coverage rules. New absence claims must name a preserved, disabled stream and
// cannot contradict either the source areas or a snapshot in the same manifest.
func (m Manifest) validateDisabledStreams() error {
	if m.EnabledAreas == nil {
		if len(m.DisabledStreams) != 0 {
			return fmt.Errorf("disabled streams require source enabled areas")
		}
		return nil
	}
	if err := validateAreas(m.EnabledAreas); err != nil {
		return err
	}
	allowed, present := map[string]bool{}, map[string]bool{}
	for _, item := range backupset.Items() {
		if item.Kind == backupset.KindStream && preserved(item) && !streamEnabled(item.Name, m.EnabledAreas) {
			_, name := resource(item, m.Instance)
			allowed[name] = true
		}
	}
	for _, e := range m.Entries {
		if e.Kind == "stream" {
			present[e.Name] = true
		}
	}
	seen := map[string]bool{}
	for _, name := range m.DisabledStreams {
		if !allowed[name] || present[name] || seen[name] {
			return fmt.Errorf("invalid disabled stream %q: unknown, enabled, present or duplicate", name)
		}
		seen[name] = true
	}
	for name := range allowed {
		if !present[name] && !seen[name] {
			return fmt.Errorf("missing disabled stream attestation %q", name)
		}
	}
	return nil
}
