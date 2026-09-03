// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command rangedump prints the egress guard's deny table, one prefix per line,
// prefixed by its address family.
//
// It is the Go half of hack/check-egress-ranges.sh. The gate needs the table as
// DATA, and the only trustworthy source of that is the compiled package — a regex
// over ranges.go was the previous design and it could be defeated by commenting a
// row out, because a comment is still text that matches.
//
// The family prefix is not decoration. The chart states its boundary as two
// separate ipBlocks, one per family, and a prefix in the WRONG block renders a
// policy that Kubernetes rejects (an IPv4 prefix is not a strict subset of ::/0).
// Comparing a single merged set cannot see that, so both sides are compared per
// family.
//
// A third field, "mapped", marks a prefix that Kubernetes REFUSES inside an
// ipBlock: an IPv4-mapped IPv6 prefix is rejected twice over, as a mapped address
// and as "not a strict subset of ::/0" (Go's net.IPNet.Contains collapses a mapped
// address to four bytes, so ::/0 does not contain it). Such a prefix therefore
// cannot appear in the chart, and the gate has to exclude it from the comparison.
//
// 🔴 It is marked HERE, from the address itself, rather than listed as a literal
// exception in the gate. A hard-coded exception list is a blanket nobody verifies:
// it silently covers a prefix that stops existing, and it silently covers a SECOND
// prefix that ought to have been questioned. Deriving it from Is4In6 means the set
// of exceptions is exactly the set of prefixes that genuinely cannot be expressed.
//
// Note the canonical rendering differs from the source literal — the table writes
// ::ffff:0:0/96 and this prints ::ffff:0.0.0.0/96 — which is by itself a reason the
// gate should not be matching source text.
//
// Output is stable and machine-read; do not add banners.
package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/devicechain-io/dc-microservice/egress"
)

func main() {
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for _, p := range egress.DeniedPrefixes() {
		family := "v6"
		if p.Addr().Is4() {
			family = "v4"
		}
		if p.Addr().Is4In6() {
			fmt.Fprintf(w, "%s %s mapped\n", family, p)
			continue
		}
		fmt.Fprintf(w, "%s %s\n", family, p)
	}
}
