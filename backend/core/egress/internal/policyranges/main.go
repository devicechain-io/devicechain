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
// Reading the rendered object with a YAML parser removes both. There is no spelling
// of a template edit that changes what the policy permits without changing what this
// prints.
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
	"fmt"
	"io"
	"net/netip"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

type manifest struct {
	Kind string `yaml:"kind"`
	Spec struct {
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
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "policyranges:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	w := bufio.NewWriter(out)
	defer w.Flush()

	dec := yaml.NewDecoder(in)
	blocks := 0
	for {
		var m manifest
		err := dec.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("parse manifest stream: %w", err)
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
		if !outer.Contains(inner.Addr()) || inner.Bits() < outer.Bits() {
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
