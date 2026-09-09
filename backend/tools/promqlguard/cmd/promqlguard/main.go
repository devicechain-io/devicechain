// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Command promqlguard refuses an alerting rule that can never return nothing.
//
//	promqlguard -min-alerts=34 rule-a.yaml rule-b.yaml
//
// Prometheus fires an alert on the PRESENCE of a sample, so an expression that always
// yields at least one sample is an alert that is permanently firing — which trains
// whoever receives it to ignore the channel, including the alerts that matter. The
// expression is valid PromQL and every series in it is spelled correctly, so promtool
// accepts it and the dashboard guard accepts it; the defect is entirely in where the
// comparison binds. See the package comment for the worked example.
//
// Exit codes are three-valued on purpose:
//
//	0  every alert can be false, and enough alerts were read to say so
//	1  findings — an alert can never be false
//	2  the INSTRUMENT is broken: no files, a file that does not parse, an expression
//	   that is not PromQL, a file whose two alert counts disagree, fewer alerts than
//	   the floor, bad arguments
//
// 🔴 2 IS NOT 1, AND IT IS NOT 0. A run that read nothing has no opinion about the
// rules. Reporting that as "clean" is an absence read as an answer — the same failure
// this check exists to catch in the rules it reads — and reporting it as a finding
// would send a reader hunting a defect that is not there.
package main

import (
	"flag"
	"fmt"
	"os"

	promqlguard "github.com/devicechain-io/dc-promqlguard"
)

func main() {
	// 🔴 THE LIVENESS FLOOR, and it is the half of this instrument a green tick cannot
	// otherwise distinguish from a broken one. Zero findings is what a clean corpus
	// reports AND what a run over an empty directory, a renamed rule file or a chart
	// that stopped rendering its rules reports. The caller passes the number of alerts
	// the repository knows it ships; fewer than that means the run has no evidence.
	minAlerts := flag.Int("min-alerts", 1, "fail (exit 2) unless at least this many alerts were checked")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "promqlguard: no rule files given, so nothing would have been checked")
		os.Exit(2)
	}
	if *minAlerts < 1 {
		fmt.Fprintln(os.Stderr, "promqlguard: -min-alerts must be at least 1; a floor of zero is not a floor")
		os.Exit(2)
	}

	findings, checked, err := promqlguard.Check(flag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "promqlguard: %v\n", err)
		os.Exit(2)
	}

	if checked < *minAlerts {
		fmt.Fprintf(os.Stderr,
			"promqlguard: checked %d alert(s) across %d file(s), expected at least %d.\n"+
				"  Rules are not being read, so a clean result from this run means nothing. Either\n"+
				"  the chart stopped rendering alerts, or alerts were deliberately removed and the\n"+
				"  floor has not been lowered to match.\n",
			checked, flag.NArg(), *minAlerts)
		os.Exit(2)
	}

	if len(findings) > 0 {
		for _, f := range findings {
			fmt.Println(f)
		}
		fmt.Fprintf(os.Stderr, "\npromqlguard: %d alert(s) out of %d can never return nothing.\n\n"+
			"An alert fires on the PRESENCE of a sample, not on its value, so an expression with\n"+
			"no reachable empty state is an alert that is always firing. It parses, it renders,\n"+
			"promtool accepts it and every series in it is spelled correctly — the defect is in\n"+
			"where the comparison binds. `>` binds tighter than `or`, so `a or vector(0) > 0`\n"+
			"parses as `a or (vector(0) > 0)` and leaves a bare `a` with no comparison at all.\n"+
			"Parenthesise what the comparison is meant to apply to: `(a or vector(0)) > 0`.\n",
			len(findings), checked)
		os.Exit(1)
	}

	fmt.Printf("promqlguard: %d alert expression(s) checked across %d file(s), each has a reachable false state.\n",
		checked, flag.NArg())
}
