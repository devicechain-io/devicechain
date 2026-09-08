// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command policyranges reads a rendered Kubernetes manifest stream on stdin and
// prints the egress NetworkPolicy's `except` prefixes, one per line, prefixed by the
// address family of the ipBlock that carries them.
//
// It is the chart half of hack/check-egress-ranges.sh, and it exists because the
// previous version of that gate read values.yaml with awk. That was the wrong input
// AND the wrong method:
//
//   - Wrong input. The policy is produced by a TEMPLATE. Every mutation applied to
//     the template — deleting the `except` range, wrapping it in a false `if`,
//     pointing it at a different values key — left values.yaml untouched, so the gate
//     reported the two lists in perfect agreement while the rendered policy allowed
//     all of 0.0.0.0/0. Five such edits were tried and five survived.
//   - Wrong method. A scan for `blockedIPv4Ranges:` anywhere in a 700-line file
//     matches a key under any parent, so a list moved to an unrelated block still
//     counted, and one moved from the v4 list to the v6 list was invisible because
//     both were merged into a single sorted set.
//
// Reading the rendered object with a YAML parser removes both, FOR THE PREFIX LISTS.
//
// 🔴 BE PRECISE ABOUT WHAT THAT DOES AND DOES NOT COVER, because the first version of
// this comment was not. It said "there is no spelling of a template edit that changes
// what the policy permits without changing what this prints", and that was false: this
// mode reads the `except` lists and NOTHING ELSE. A review found eight edits that leave
// them identical and change what the policy permits — flipping `policyTypes` to Ingress
// so every egress rule is inert, a podSelector matching no pods, an empty `namespaceSelector`
// on the DNS rule, an extra `- {}` rule allowing everything, and a SECOND NetworkPolicy
// selecting the same pods (policies union, so one allow-all opens everything). All of
// them are valid Kubernetes, so a server-side dry-run accepts them too.
//
// That is what -policies is for: it emits every NetworkPolicy document in the stream so
// the gate can pin the WHOLE rendered object against a golden file, not just the two
// lists. The prefix mode relates the chart to the Go table; the golden pins everything
// else. Neither is sufficient alone.
//
// It also VALIDATES, because two of the three defects this whole area has produced
// were invalid Kubernetes rather than drift, and a client-side dry-run does not catch
// them (it accepted every invalid form we tried; only the API server refused them).
// Checked here, cheaply and with no cluster: every except entry parses, sits in the
// same family as its ipBlock, is a strict subset of it, and is not IPv4-mapped —
// which is the exact pair of errors the API server returned for `::ffff:0:0/96`.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// refuseListWrapper rejects any document carrying a top-level `items` SEQUENCE.
//
// 🔴 A WRAPPED DOCUMENT IS A HOLE, AND THE PREDICATE HAS TO BE THE ONE HELM USES.
// Helm's manifest builder flattens a wrapper before applying, so a NetworkPolicy inside one
// is created by a real `helm install` — verified on a cluster — while a filter selecting on
// `kind == "NetworkPolicy"` reads straight past it.
//
// 🔴 THE FIRST VERSION OF THIS FUNCTION TESTED `strings.HasSuffix(kind, "List")`, AND THAT
// WAS THE SAME MISTAKE ONE LEVEL DOWN. apimachinery's `Unstructured.IsList` asks whether
// `items` is a sequence; the KIND IS NOT CONSULTED. So `kind: ConfigMap` with an `items:`
// list containing a NetworkPolicy is flattened and created — reproduced against a real
// cluster, where `helm get manifest` showed only the ConfigMap and `kubectl get netpol`
// showed the policy. A kind ending in "List" was a symptom of the shape, not the shape.
//
// (`kind: Bundle` looked safe for an unrelated reason: Helm resolves the OUTER document's
// REST mapping first, so an unknown kind fails there. Any kind the server knows carries the
// payload.)
//
// This chart should never emit a wrapper, so refusing the shape is cheaper than teaching
// every consumer to flatten it.
func refuseListWrapper(doc map[string]any, index int) error {
	items, present := doc["items"]
	if !present {
		return nil
	}
	if _, isSequence := items.([]any); !isSequence {
		return nil
	}
	kind, _ := doc["kind"].(string)
	return fmt.Errorf("document %d (kind %q) carries a top-level `items` sequence: a wrapped "+
		"document is refused here, because Helm flattens one before applying — anything inside "+
		"it is created by an install while a kind filter reads straight past the wrapper. The "+
		"kind is deliberately not part of this test: Helm keys on `items`, not on the name. "+
		"Emit the objects directly", index, kind)
}

type manifest struct {
	Kind string `yaml:"kind"`
	// Items exists only so the wrapper check can see it; see refuseListWrapper.
	Items []any `yaml:"items"`
	Spec  struct {
		Egress []struct {
			To []struct {
				IPBlock *struct {
					CIDR   string   `yaml:"cidr"`
					Except []string `yaml:"except"`
				} `yaml:"ipBlock"`
			} `yaml:"to"`
		} `yaml:"egress"`
	} `yaml:"spec"`
}

func main() {
	policies := flag.Bool("policies", false,
		"emit every NetworkPolicy document in the stream verbatim, instead of the except prefixes")
	flag.Parse()

	emit := run
	if *policies {
		emit = runPolicies
	}
	if err := emit(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "policyranges:", err)
		os.Exit(1)
	}
}

// runPolicies re-emits every NetworkPolicy document in the stream, in order, verbatim.
//
// Two callers, one output. The gate pins it against a golden file so that ANY change to
// the rendered policy — a selector, policyTypes, a peer, an added document — is a diff a
// human has to accept, and CI applies the same bytes to a real API server with
// --dry-run=server so validity is checked on the whole object rather than a slice of it.
//
// 🔴 It must select by KIND from the parsed stream rather than by filename. Both of this
// gate's previous incarnations rendered with `--show-only templates/networkpolicy.yaml`,
// which means a policy added in a SECOND template file is invisible to the check, to the
// dry-run, and to the golden all at once — and a second policy selecting the same pods
// opens everything, because policies union.
//
// The documents are re-marshalled rather than echoed, so the golden is canonical: helm's
// output formatting cannot make a no-op diff, and key order cannot vary between runs.
func runPolicies(in io.Reader, out io.Writer) error {
	w := bufio.NewWriter(out)
	defer w.Flush()

	dec := yaml.NewDecoder(in)
	found := 0
	docIndex := 0
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse manifest stream: %w", err)
		}
		docIndex++
		if err := refuseListWrapper(doc, docIndex); err != nil {
			return err
		}
		if doc == nil || doc["kind"] != "NetworkPolicy" {
			continue
		}
		found++
		body, err := yaml.Marshal(doc)
		if err != nil {
			return fmt.Errorf("re-marshal NetworkPolicy document %d: %w", found, err)
		}
		if _, err := fmt.Fprintf(w, "---\n%s", body); err != nil {
			return err
		}
	}
	if found == 0 {
		return errors.New("the rendered stream contains no NetworkPolicy document; " +
			"the policy is meant to be enabled for this check, so an empty result would " +
			"otherwise validate nothing and pass")
	}
	return nil
}

func run(in io.Reader, out io.Writer) error {
	w := bufio.NewWriter(out)
	defer w.Flush()

	dec := yaml.NewDecoder(in)
	blocks := 0
	docIndex := 0
	for {
		var m manifest
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse manifest stream: %w", err)
		}
		docIndex++
		if len(m.Items) > 0 {
			return refuseListWrapper(map[string]any{"kind": m.Kind, "items": []any{nil}}, docIndex)
		}
		if m.Kind != "NetworkPolicy" {
			continue
		}
		for _, rule := range m.Spec.Egress {
			for _, to := range rule.To {
				// An ipBlock with no `except` is an operator allowance
				// (additionalAllowedCidrs), not the deny boundary. Keying on the
				// presence of `except` rather than on the cidr value keeps the two
				// apart without hard-coding which cidrs the boundary uses.
				if to.IPBlock == nil || len(to.IPBlock.Except) == 0 {
					continue
				}
				blocks++
				if err := emit(w, to.IPBlock.CIDR, to.IPBlock.Except); err != nil {
					return err
				}
			}
		}
	}

	// 🔴 The boundary is TWO ipBlocks, one per family. Requiring exactly two is what
	// makes a deleted block a failure here rather than a smaller set that still
	// compares cleanly against a smaller Go table — and a family folded into one
	// block would otherwise read as agreement.
	if blocks != 2 {
		return fmt.Errorf("expected exactly 2 ipBlocks carrying an `except` list "+
			"(one per address family), found %d — a deleted or merged block means the "+
			"policy no longer refuses what it claims to", blocks)
	}
	return nil
}

func emit(w io.Writer, cidr string, except []string) error {
	outer, err := netip.ParsePrefix(cidr)
	if err != nil {
		return fmt.Errorf("ipBlock cidr %q does not parse: %w", cidr, err)
	}
	for _, e := range except {
		inner, err := netip.ParsePrefix(e)
		if err != nil {
			return fmt.Errorf("except entry %q under cidr %q does not parse: %w", e, cidr, err)
		}
		// Kubernetes refuses a mapped address in an ipBlock, and refuses it a second
		// time as "not a strict subset" — Go's net.IPNet.Contains collapses a mapped
		// address to four bytes, so ::/0 does not contain it. Either way the WHOLE
		// policy is invalid and the release fails to install.
		if inner.Addr().Is4In6() {
			return fmt.Errorf("except entry %q under cidr %q is an IPv4-mapped IPv6 prefix; "+
				"Kubernetes rejects those in an ipBlock and the whole policy is then invalid", e, cidr)
		}
		if inner.Addr().Is4() != outer.Addr().Is4() {
			return fmt.Errorf("except entry %q is not the same address family as its ipBlock cidr %q; "+
				"Kubernetes requires every except to be a strict subset of the cidr", e, cidr)
		}
		// STRICT subset, so `<=` and not `<`: an except equal to its own cidr denies the
		// whole allowance and is refused by the API server. The first version of this
		// line used `<` and let that through.
		if !outer.Contains(inner.Addr()) || inner.Bits() <= outer.Bits() {
			return fmt.Errorf("except entry %q is not a strict subset of its ipBlock cidr %q", e, cidr)
		}
		family := "v6"
		if inner.Addr().Is4() {
			family = "v4"
		}
		if _, err := fmt.Fprintf(w, "%s %s\n", family, inner); err != nil {
			return err
		}
	}
	return nil
}
