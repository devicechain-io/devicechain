// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package promqlguard

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// The second question this package asks of an alert:
//
//	CAN THIS EXPRESSION BE EVALUATED AT ALL ON A REAL INSTANCE?
//
// 🔴 A RANGE FUNCTION DROPS THE METRIC NAME. `increase`, `rate`, `max_over_time` and
// every other range-vector function except `last_over_time` / `first_over_time` return
// their series WITHOUT `__name__`. Over a selector that matches several metric names
// on one scrape target, such as
//
//	increase({__name__=~"devicechain_[a-z0-9]+_[a-z0-9_]+_messages_total", result="dropped"}[15m])
//
// the series that differed only by their name are left with identical label sets, and
// Prometheus refuses the whole evaluation: "vector cannot contain metrics with the
// same labelset". The rule's health turns to err, it can never fire, and
// PrometheusRuleFailures fires instead, permanently. That shipped in ProcessorLoopDropping
// in v1.8.0-rc.1: device-management runs several processing loops in one pod, each with
// its own `*_messages_total`, and their remaining labels are identical. promtool
// accepted it, and its unit tests passed, because their synthetic series carried one
// metric name per target. The failure needs two names on ONE target, which only a real
// instance had.
//
// So this refuses a name-dropping range function whose argument can carry more than
// one metric name per target, unless the expression has carried the name into a label
// of its own first (`label_replace(..., "__name__", ...)` inside a subquery), which is
// what keeps the series distinct once the name is gone.
//
// WHAT COUNTS AS ONE NAME PER TARGET. An exact name, obviously. And one regex shape,
// `devicechain_[a-z0-9]+_<fixed suffix>`: the only variable segment is the service's
// metrics subsystem, and a DeviceChain process registers its own metrics under exactly
// one subsystem (core.Microservice.MetricsSubsystem), so on one target the regex can
// match one name. That is a convention of this codebase, not a property of PromQL, and
// it is the one assumption this check takes on trust. Anything wider — a variable
// segment in the suffix, an alternation, a selector with no name at all — is refused.
var singleSubsystemName = regexp.MustCompile(`^devicechain_\[a-z0-9\]\+_[a-z0-9_]+$`)

// keepsName lists the range functions that keep `__name__` (the engine exempts them
// by name), plus absent_over_time, whose output carries only the selector's
// equality-matched labels and so is at most one series.
var keepsName = map[string]bool{
	"last_over_time":   true,
	"first_over_time":  true,
	"absent_over_time": true,
}

// NameCollapse reports why e contains a range function that will collapse several
// metric names into identical label sets, or "" when it does not.
func NameCollapse(e parser.Expr) string {
	var why string
	parser.Inspect(e, func(node parser.Node, _ []parser.Node) error {
		if why != "" {
			return nil
		}
		call, ok := node.(*parser.Call)
		if !ok || call.Func == nil || keepsName[call.Func.Name] {
			return nil
		}
		for i, t := range call.Func.ArgTypes {
			if t != parser.ValueTypeMatrix || i >= len(call.Args) {
				continue
			}
			if ok, what := distinctWithoutName(call.Args[i]); !ok {
				why = fmt.Sprintf(
					"`%s(...)` drops the metric name from its result, and its argument %s. "+
						"Two such metrics on one scrape target are left with identical label sets, "+
						"and Prometheus refuses the evaluation (\"vector cannot contain metrics with "+
						"the same labelset\"): the rule errors on every evaluation and can never fire. "+
						"Carry the name into a label first, inside a subquery: "+
						"`%s(label_replace(<selector>, \"metric\", \"$1\", \"__name__\", \"(.+)\")[<range>:<step>])`",
					call.Func.Name, what, call.Func.Name)
				return nil
			}
		}
		return nil
	})
	return why
}

// distinctWithoutName reports whether every series e yields stays distinct once
// `__name__` is removed, and when it does not, what in e can carry several names.
// It is conservative: an operator it does not understand is answered through its
// operands, never assumed safe.
func distinctWithoutName(e parser.Expr) (bool, string) {
	switch n := e.(type) {
	case *parser.ParenExpr:
		return distinctWithoutName(n.Expr)
	case *parser.StepInvariantExpr:
		return distinctWithoutName(n.Expr)
	case *parser.MatrixSelector:
		return distinctWithoutName(n.VectorSelector)
	case *parser.SubqueryExpr:
		return distinctWithoutName(n.Expr)
	case *parser.VectorSelector:
		return oneNamePerTarget(n)
	case *parser.NumberLiteral, *parser.StringLiteral:
		return true, ""
	case *parser.AggregateExpr:
		switch n.Op {
		case parser.TOPK, parser.BOTTOMK, parser.LIMITK, parser.LIMIT_RATIO:
			// These select series and keep their labels, the name included.
			return distinctWithoutName(n.Expr)
		}
		// Every other aggregation emits one series per grouping, with no name.
		return true, ""
	case *parser.Call:
		if n.Func != nil && (n.Func.Name == "label_replace" || n.Func.Name == "label_join") {
			if copiesName(n) {
				return true, ""
			}
			if len(n.Args) > 0 {
				return distinctWithoutName(n.Args[0])
			}
		}
		for _, a := range n.Args {
			if ok, what := distinctWithoutName(a); !ok {
				return false, what
			}
		}
		return true, ""
	case *parser.BinaryExpr:
		if ok, what := distinctWithoutName(n.LHS); !ok {
			return false, what
		}
		return distinctWithoutName(n.RHS)
	}
	return false, fmt.Sprintf("is a %T this check does not know how to read", e)
}

// copiesName reports whether a label_replace / label_join call takes `__name__` as a
// source label, which is how an expression keeps the name once a range function drops it.
func copiesName(call *parser.Call) bool {
	var sources []parser.Expr
	switch call.Func.Name {
	case "label_replace":
		if len(call.Args) == 5 {
			sources = call.Args[3:4]
		}
	case "label_join":
		if len(call.Args) > 3 {
			sources = call.Args[3:]
		}
	}
	for _, s := range sources {
		if lit, ok := s.(*parser.StringLiteral); ok && lit.Val == labels.MetricName {
			return true
		}
	}
	return false
}

func oneNamePerTarget(vs *parser.VectorSelector) (bool, string) {
	var regexes []string
	for _, m := range vs.LabelMatchers {
		if m.Name != labels.MetricName {
			continue
		}
		switch m.Type {
		case labels.MatchEqual:
			return true, ""
		case labels.MatchRegexp:
			if singleSubsystemName.MatchString(m.Value) {
				return true, ""
			}
			regexes = append(regexes, m.Value)
		}
	}
	if len(regexes) == 0 {
		return false, fmt.Sprintf("selects `%s` with no exact metric name, so it can match several", vs.String())
	}
	return false, fmt.Sprintf("selects metric names by the regex %q, which can match several "+
		"names on one target (only `devicechain_[a-z0-9]+_<fixed suffix>` is read as one name "+
		"per target)", strings.Join(regexes, `", "`))
}
