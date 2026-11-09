// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/egress"
	"github.com/fatih/color"
	"k8s.io/client-go/dynamic"
)

// The local-development egress allowance: `dcctl bootstrap|upgrade --dev-allow-egress`.
//
// The tenant-egress guard refuses private, loopback and link-local destinations, which is
// right for production and wrong for a developer pointing a webhook at a service on their
// own machine. The operator's escape hatch (egress.allowedDestinations) is deliberately not
// tunable through dcctl, because an exemption that reads as one host and grants a subnet is
// the whole boundary. This is the supported, narrower alternative, and it is built so that
// it cannot be mistaken for the production one:
//
//   - LOCAL CLUSTERS ONLY. Refused when the instance's cluster context is not a local one
//     (kind, minikube, k3d, docker-desktop, rancher-desktop). The check runs on the SETTLED
//     value, so a value recorded on a declaration is judged too.
//   - A SEPARATE CONFIG KEY (infrastructure.egress.devAllowedDestinations) that the guard
//     treats differently: it can never admit the cloud metadata services, loopback,
//     link-local, multicast or the unspecified address, however it is written (egress/dev.go).
//   - RECORDED on the declaration (AnnotationDevAllowEgress), so every later upgrade keeps
//     it and no upgrade silently drops it; cleared only by `--dev-allow-egress none`.
//   - LOUD: printed by every run, listed by `dcctl instances`, and logged as a warning by
//     every service that builds a guard from it.
const AnnotationDevAllowEgress = "core.devicechain.io/dev-allow-egress"

// DevAllowEgressNone is the value that clears a recorded allowance.
const DevAllowEgressNone = "none"

// ParseDevAllowEgress turns the flag's values into the canonical list: sorted, de-duplicated
// CIDR strings. A bare address is a single-host prefix. The lone value "none" yields an empty
// non-nil list, which a run reads as "clear what is recorded". Anything the guard would refuse
// at startup (a metadata range, an unmasked prefix) is refused here, before a cluster exists.
func ParseDevAllowEgress(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) == 1 && strings.EqualFold(strings.TrimSpace(raw[0]), DevAllowEgressNone) {
		return []string{}, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		e := strings.TrimSpace(r)
		switch {
		case e == "":
			return nil, fmt.Errorf("--dev-allow-egress: an empty entry")
		case strings.EqualFold(e, DevAllowEgressNone):
			return nil, fmt.Errorf("--dev-allow-egress: %q clears the allowance and cannot be combined with addresses", DevAllowEgressNone)
		}
		if a, err := netip.ParseAddr(e); err == nil {
			e = netip.PrefixFrom(a, a.BitLen()).String()
		}
		if _, err := egress.ParseDevPrefixes([]string{e}); err != nil {
			return nil, fmt.Errorf("--dev-allow-egress: %w. Give an IP address or a CIDR such as "+
				"192.168.1.0/24 (a hostname cannot be allowed: the guard judges the resolved address)", err)
		}
		p, _ := netip.ParsePrefix(e)
		if !seen[p.String()] {
			seen[p.String()] = true
			out = append(out, p.String())
		}
	}
	sort.Strings(out)
	return out, nil
}

// isLocalDevContext reports whether a kube context names a local development cluster. It is
// STRICTER than looksLocal (prefix only, never a substring): a production context that merely
// contains "kind-" must not unlock a development allowance.
func isLocalDevContext(kubeContext string) bool {
	for _, p := range localPrefixes {
		if strings.HasPrefix(kubeContext, p) {
			return true
		}
	}
	return false
}

// CheckDevAllowEgressContext refuses a non-empty allowance on a cluster that is not local.
// An empty kubeContext is not judged (the pipeline judges the settled binding).
func CheckDevAllowEgressContext(kubeContext string, list []string) error {
	if len(list) == 0 || kubeContext == "" || isLocalDevContext(kubeContext) {
		return nil
	}
	return fmt.Errorf("--dev-allow-egress is for a local development cluster (a kube context starting with one of %s), "+
		"and %q is not one. It relaxes the tenant-egress boundary for private addresses; on a real cluster an "+
		"operator grants a destination in the instance configuration (egress.allowedDestinations) after reading "+
		"its sharp edges, which dcctl does not write",
		strings.Join(localPrefixes, ", "), kubeContext)
}

// settleDevAllowEgress is the run's decision: this run's flag over what the declaration
// records (nil given: keep the recorded one; empty non-nil: clear). It judges the settled
// value against the cluster, so a recorded allowance on a non-local context is refused.
func settleDevAllowEgress(st *State, recorded, given []string) error {
	st.DevAllowEgressRecorded = slices.Clone(recorded)
	if given != nil {
		st.DevAllowEgress = slices.Clone(given)
	} else {
		st.DevAllowEgress = slices.Clone(recorded)
	}
	if len(st.DevAllowEgress) > 0 {
		// A recorded value is re-validated: this release's guard may refuse what an older one let by.
		if _, err := ParseDevAllowEgress(st.DevAllowEgress); err != nil {
			return fmt.Errorf("the development egress allowance recorded on instance %q is not accepted by this release: %w", st.Instance, err)
		}
		if err := CheckDevAllowEgressContext(st.KubeContext, st.DevAllowEgress); err != nil {
			return err
		}
	}
	st.DevAllowEgressChange = !slices.Equal(st.DevAllowEgress, st.DevAllowEgressRecorded)
	return nil
}

// recordedDevAllowEgress reads the declaration's annotation.
func recordedDevAllowEgress(annotations map[string]string) []string {
	raw := strings.TrimSpace(annotations[AnnotationDevAllowEgress])
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// DevAllowEgressOf is the list a declaration records, for listings.
func DevAllowEgressOf(inst *dcv1beta1.Instance) []string {
	return recordedDevAllowEgress(inst.Annotations)
}

// devAllowEgressValues is what helmValues renders under infrastructure.egress; nil when none.
func devAllowEgressValues(st *State) map[string]interface{} {
	if len(st.DevAllowEgress) == 0 {
		return nil
	}
	list := make([]interface{}, len(st.DevAllowEgress))
	for i, p := range st.DevAllowEgress {
		list[i] = p
	}
	return map[string]interface{}{"devAllowedDestinations": list}
}

// sayDevAllowEgress prints the allowance in effect, loudly, and what this run changes.
func sayDevAllowEgress(st *State) {
	if len(st.DevAllowEgress) == 0 {
		if st.DevAllowEgressChange {
			fmt.Printf("  %s %s\n", color.WhiteString("Dev egress:"),
				color.YellowString("cleared (--dev-allow-egress none); tenant egress is back to the default boundary"))
		}
		return
	}
	fmt.Printf("  %s %s\n", color.WhiteString("Dev egress:"),
		color.YellowString("DEVELOPMENT ONLY - tenant webhooks and connectors may reach "+strings.Join(st.DevAllowEgress, ", ")+
			" (cloud metadata, loopback and link-local stay blocked)"))
	if st.DryRun && st.DevAllowEgressChange {
		wouldDo("record the development egress allowance in the instance declaration")
	}
}

// recordDevAllowEgress writes (or removes) the annotation, a compare-and-swap like every
// declaration write.
func recordDevAllowEgress(ctx context.Context, dyn dynamic.Interface, instance string, list []string) error {
	return updateInstanceMetadata(ctx, dyn, instance, "the development egress allowance", func(inst *dcv1beta1.Instance) (bool, error) {
		if len(list) == 0 {
			if _, had := inst.Annotations[AnnotationDevAllowEgress]; !had {
				return false, nil
			}
			delete(inst.Annotations, AnnotationDevAllowEgress)
			return true, nil
		}
		if inst.Annotations == nil {
			inst.Annotations = map[string]string{}
		}
		enc := strings.Join(list, ",")
		if inst.Annotations[AnnotationDevAllowEgress] == enc {
			return false, nil
		}
		inst.Annotations[AnnotationDevAllowEgress] = enc
		return true, nil
	})
}

// recordDevAllowEgressFor is the bootstrap path's entry, a seam like recordValuesOverlayFor.
var recordDevAllowEgressFor = func(ctx context.Context, kubeContext, instance string, list []string) error {
	dyn, _, _, err := kubeClients(kubeContext)
	if err != nil {
		return fmt.Errorf("connecting to the cluster to record the development egress allowance: %w", err)
	}
	return recordDevAllowEgress(ctx, dyn, instance, list)
}
