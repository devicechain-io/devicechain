// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package promqlguard

import (
	"errors"
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
// WHAT COUNTS AS ONE NAME PER TARGET. An exact name (or a regex with no metacharacters),
// obviously. And one regex shape, `devicechain_[a-z0-9]+_<fixed suffix>`: the only
// variable segment is the metrics subsystem. A DeviceChain service registers its own
// metrics under one subsystem (core.Microservice.MetricsSubsystem); the shared client
// libraries register a few under fixed subsystems of their own (`svcclient`,
// `userclient`) in the same process, and none of their suffixes is one a rule reads
// under a range function. So on one target such a regex matches one name. That is a
// convention of this codebase, not a property of PromQL, and it is the one assumption
// this check takes on trust: a client-library metric registered with a suffix a rule
// already reads this way would break it. Anything wider — a variable segment in the
// suffix, an alternation, a selector with no name at all — is refused.
//
// WHAT COUNTS AS CARRYING THE NAME. `label_replace(x, "<dst>", "$1", "__name__", "(.+)")`
// (or `(.*)`, or `${1}`) into a label that is not itself reserved, or `label_join` with
// `__name__` among its sources. A copy that does not preserve the WHOLE name — a fixed
// replacement, a partial capture — is not accepted as one. A later transformation that
// overwrites the copied label again is not tracked; that is a residual this accepts.
var singleSubsystemName = regexp.MustCompile(`^devicechain_\[a-z0-9\]\+_[a-z0-9_]+$`)

// keepsName lists the range functions that keep `__name__` (the engine exempts them
// by name), plus absent_over_time, whose output carries only the selector's
// equality-matched labels and so is at most one series.
var keepsName = map[string]bool{
	"last_over_time":   true,
	"first_over_time":  true,
	"absent_over_time": true,
}

// errStop ends the walk once a reason has been found.
var errStop = errors.New("stop")

// NameCollapse reports why e contains a range function that will collapse several
// metric names into identical label sets, or "" when it does not.
func NameCollapse(e parser.Expr) string {
	var why string
	parser.Inspect(e, func(node parser.Node, _ []parser.Node) error {
		if why != "" {
			return errStop
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
	case *parser.UnaryExpr:
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
		if !n.Without && contains(n.Grouping, labels.MetricName) {
			// `by (__name__, ...)` keeps the name in its groups.
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
		if n.Op == parser.LOR && n.VectorMatching != nil && n.VectorMatching.On &&
			contains(n.VectorMatching.MatchingLabels, labels.MetricName) {
			// A plain `or` drops a right-hand series whose labels (name aside) match one
			// on the left, so its union stays distinct. Matching ON the name keeps both.
			return false, "unions series with `or on(__name__, ...)`, which keeps two names that share every other label"
		}
		if ok, what := distinctWithoutName(n.LHS); !ok {
			return false, what
		}
		return distinctWithoutName(n.RHS)
	}
	return false, fmt.Sprintf("is a %T this check does not know how to read", e)
}

// copiesName reports whether a label_replace / label_join call copies the WHOLE
// `__name__` into an ordinary label, which is how an expression keeps the name once a
// range function drops it.
func copiesName(call *parser.Call) bool {
	str := func(i int) (string, bool) {
		if i >= len(call.Args) {
			return "", false
		}
		lit, ok := call.Args[i].(*parser.StringLiteral)
		if !ok {
			return "", false
		}
		return lit.Val, true
	}
	dst, ok := str(1)
	if !ok || dst == "" || strings.HasPrefix(dst, "__") {
		return false
	}
	switch call.Func.Name {
	case "label_replace":
		repl, ok1 := str(2)
		src, ok2 := str(3)
		re, ok3 := str(4)
		return ok1 && ok2 && ok3 && src == labels.MetricName &&
			(repl == "$1" || repl == "${1}") && (re == "(.+)" || re == "(.*)")
	case "label_join":
		for i := 3; i < len(call.Args); i++ {
			if v, ok := str(i); ok && v == labels.MetricName {
				return true
			}
		}
	}
	return false
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
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
			if singleSubsystemName.MatchString(m.Value) || regexp.QuoteMeta(m.Value) == m.Value {
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
