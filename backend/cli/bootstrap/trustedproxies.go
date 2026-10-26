// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	dcv1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/devicechain-io/dc-microservice/config"
	"github.com/fatih/color"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// infrastructure.http.trustedProxies, rendered by dcctl.
//
// 🔴 WHY THIS IS NOT LEFT EMPTY. Device self-registration limits requests per source address,
// and behind the ingress controller the connection's own peer is the controller's pod, so with
// no trusted proxy every device of every tenant shares ONE bucket per controller pod. A
// stranger sending a few junk bodies a second then answers 429 to every device on the
// instance, which is exactly the lever the per-source design exists to deny. Naming the
// controller's range makes the services believe the X-Forwarded-For header it writes (the
// real TCP peer: ingress-nginx with use-forwarded-headers left OFF, Traefik with no trusted IPs of its
// own, both of which replace what a client sent) and key on the device.
//
// The range is the cluster's POD range, read from the nodes: every ingress controller pod is
// in it, wherever it is scheduled, and so is anything else that could write to the services.
// Entries are per-node slices of the pod network, merged into the fewest prefixes.
//
// THE CHOICE LIVES IN THE DECLARATION, like --mqtt-fail-closed (see mqttfailclosed.go), because
// dcctl writes the instance configuration document over the last one on every run:
//
//   - the operator's own list (--trusted-proxies) is recorded as explicit and kept as written;
//   - otherwise the list is derived from the nodes on every run, so a cluster that grew is
//     followed, and what was rendered is recorded;
//   - a run that cannot derive one (the nodes expose no pod range, or cannot be read) keeps
//     what the declaration last recorded rather than rendering the instance without it;
//   - an instance with neither renders none, and says so loudly.

// AutoTrustedProxies is the --trusted-proxies value that drops an explicit list and returns the
// instance to deriving it from the cluster.
const AutoTrustedProxies = "auto"

// trustedProxiesSource says where a settled list came from, for the report.
type trustedProxiesSource string

const (
	trustedFromOperator   trustedProxiesSource = "the --trusted-proxies given"
	trustedFromRecord     trustedProxiesSource = "the instance declaration"
	trustedFromNodes      trustedProxiesSource = "the cluster's node pod ranges"
	trustedFromRecordWarn trustedProxiesSource = "the instance declaration (the nodes could not be read for a pod range)"
	trustedNone           trustedProxiesSource = "nothing: no pod range could be found"
)

// CanonicalTrustedProxies validates an operator's list the way the services will read it (a
// masked CIDR, IPv4 or IPv6) and returns it merged and sorted. An invalid entry is refused
// here, while nothing has been written, instead of stopping every service at startup.
func CanonicalTrustedProxies(entries []string) ([]string, error) {
	prefixes, err := config.HttpConfiguration{TrustedProxies: entries}.TrustedProxyPrefixes()
	if err != nil {
		return nil, err
	}
	return prefixStrings(aggregatePrefixes(prefixes)), nil
}

// aggregatePrefixes merges a set of prefixes into the fewest that cover exactly the same
// addresses: duplicates and contained prefixes are dropped and sibling pairs are joined,
// repeatedly. The result is sorted, so the same cluster renders the same document every run.
func aggregatePrefixes(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		out = append(out, p.Masked())
	}
	for changed := true; changed; {
		changed = false
		slices.SortFunc(out, func(a, b netip.Prefix) int {
			if c := a.Addr().Compare(b.Addr()); c != 0 {
				return c
			}
			return a.Bits() - b.Bits()
		})
		merged := out[:0:0]
		for i := 0; i < len(out); i++ {
			cur := out[i]
			if n := len(merged); n > 0 {
				last := merged[n-1]
				switch {
				case last.Overlaps(cur) && last.Bits() <= cur.Bits(): // cur is inside last (or equal)
					changed = true
					continue
				case last.Bits() == cur.Bits() && last.Bits() > 0 && last.Addr().Is4() == cur.Addr().Is4():
					parent, _ := last.Addr().Prefix(last.Bits() - 1)
					if cp, _ := cur.Addr().Prefix(cur.Bits() - 1); cp == parent && last != cur {
						merged[n-1] = parent
						changed = true
						continue
					}
				}
			}
			merged = append(merged, cur)
		}
		out = merged
	}
	return out
}

func prefixStrings(ps []netip.Prefix) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// derivePodCIDRs reads the pod ranges the cluster's nodes were given (spec.podCIDRs, else
// spec.podCIDR) and returns them merged. Empty means the nodes expose none, which is the case
// for a CNI that allocates pod addresses itself.
func derivePodCIDRs(ctx context.Context, typed kubernetes.Interface) ([]string, error) {
	nodes, err := typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing the cluster's nodes: %w", err)
	}
	var prefixes []netip.Prefix
	for _, n := range nodes.Items {
		cidrs := n.Spec.PodCIDRs
		if len(cidrs) == 0 && n.Spec.PodCIDR != "" {
			cidrs = []string{n.Spec.PodCIDR}
		}
		for _, c := range cidrs {
			p, err := netip.ParsePrefix(c)
			if err != nil {
				return nil, fmt.Errorf("node %s reports pod range %q, which is not a CIDR: %w", n.Name, c, err)
			}
			prefixes = append(prefixes, p)
		}
	}
	return prefixStrings(aggregatePrefixes(prefixes)), nil
}

// trustedProxiesSettlement is what one run decided.
type trustedProxiesSettlement struct {
	List     []string
	Explicit bool
	Source   trustedProxiesSource
	// Warning is set when the result is weaker than the instance deserves.
	Warning string
}

// settleTrustedProxies decides the list a run renders. recorded and recordedExplicit are the
// declaration's; override is this run's --trusted-proxies (nil when not given). derive reads the
// cluster. It never returns an error for a cluster it cannot read: that is a warning and the
// recorded list, because failing an upgrade over a defence-in-depth input would be worse than
// carrying the last good value forward. A bad override IS an error.
func settleTrustedProxies(recorded []string, recordedExplicit bool, override []string,
	derive func() ([]string, error)) (trustedProxiesSettlement, error) {
	if len(override) > 0 && !(len(override) == 1 && strings.EqualFold(strings.TrimSpace(override[0]), AutoTrustedProxies)) {
		list, err := CanonicalTrustedProxies(override)
		if err != nil {
			return trustedProxiesSettlement{}, fmt.Errorf("--trusted-proxies: %w", err)
		}
		return trustedProxiesSettlement{List: list, Explicit: true, Source: trustedFromOperator}, nil
	}
	if len(override) == 0 && recordedExplicit && len(recorded) > 0 {
		return trustedProxiesSettlement{List: slices.Clone(recorded), Explicit: true, Source: trustedFromRecord}, nil
	}
	derived, derr := derive()
	switch {
	case derr == nil && len(derived) > 0:
		return trustedProxiesSettlement{List: derived, Source: trustedFromNodes}, nil
	case len(recorded) > 0:
		w := ""
		if derr != nil {
			w = derr.Error()
		} else {
			w = "the nodes expose no pod range"
		}
		return trustedProxiesSettlement{List: slices.Clone(recorded), Source: trustedFromRecordWarn, Warning: w}, nil
	default:
		w := "the nodes expose no pod range"
		if derr != nil {
			w = derr.Error()
		}
		return trustedProxiesSettlement{Source: trustedNone, Warning: w}, nil
	}
}

// applyTrustedProxies settles the list into the State. override is nil when the run was given
// no --trusted-proxies.
func applyTrustedProxies(ctx context.Context, st *State, typed kubernetes.Interface,
	recorded []string, recordedExplicit bool, override []string) error {
	got, err := settleTrustedProxies(recorded, recordedExplicit, override, func() ([]string, error) {
		if typed == nil {
			return nil, fmt.Errorf("no cluster connection to read the nodes from")
		}
		return derivePodCIDRs(ctx, typed)
	})
	if err != nil {
		return err
	}
	st.TrustedProxiesChange = got.Explicit != recordedExplicit || !slices.Equal(got.List, recorded)
	st.TrustedProxies, st.TrustedProxiesExplicit = got.List, got.Explicit
	sayTrustedProxies(got)
	return nil
}

func sayTrustedProxies(got trustedProxiesSettlement) {
	if len(got.List) > 0 {
		fmt.Printf("  trusted proxies  %s (from %s)\n", strings.Join(got.List, ", "), got.Source)
	}
	if got.Warning != "" {
		fmt.Println(color.YellowString("  warning: infrastructure.http.trustedProxies - %s.", got.Warning))
		if len(got.List) == 0 {
			fmt.Println(color.YellowString("  Without it every device behind the ingress shares one self-registration allowance per\n" +
				"  ingress pod, so a stranger can exhaust it for all of them. Name the ingress controller's pod\n" +
				"  range with --trusted-proxies (and add a fronting load balancer's ranges if there is one)."))
		}
	}
}

// recordTrustedProxies writes the settled list onto a declaration spec.
func recordTrustedProxies(spec *dcv1beta1.InstanceSpec, st *State) {
	spec.TrustedProxies = slices.Clone(st.TrustedProxies)
	spec.TrustedProxiesExplicit = st.TrustedProxiesExplicit
}

// trustedProxiesValues is the infrastructure.http block, or nil when there is nothing to say
// (an empty list is never rendered: it would change every existing document for no meaning).
func trustedProxiesValues(st *State) map[string]interface{} {
	if len(st.TrustedProxies) == 0 {
		return nil
	}
	list := make([]interface{}, len(st.TrustedProxies))
	for i, p := range st.TrustedProxies {
		list[i] = p
	}
	return map[string]interface{}{"trustedProxies": list}
}

// trustedProxiesRecorded reports whether the declaration already holds what the State settled.
func trustedProxiesRecorded(spec dcv1beta1.InstanceSpec, st *State) bool {
	return spec.TrustedProxiesExplicit == st.TrustedProxiesExplicit && slices.Equal(spec.TrustedProxies, st.TrustedProxies)
}

// settleBootstrapTrustedProxies is the bootstrap half: the declaration (a re-run over a live
// instance) supplies the recorded list, the flag the override, the cluster the derivation. A
// dry run reads the nodes too if it can; it writes nothing either way.
func settleBootstrapTrustedProxies(ctx context.Context, st *State) error {
	var recorded []string
	var recordedExplicit bool
	if !st.DryRun {
		existing, err := readInstanceDeclaration(ctx, st.KubeContext, st.Instance)
		if err != nil {
			return err
		}
		if existing != nil {
			recorded, recordedExplicit = existing.Spec.TrustedProxies, existing.Spec.TrustedProxiesExplicit
		}
	}
	var typed kubernetes.Interface
	if _, _, t, err := kubeClients(st.KubeContext); err == nil {
		typed = t
	}
	return applyTrustedProxies(ctx, st, typed, recorded, recordedExplicit, st.TrustedProxiesOverride)
}
