// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
	"github.com/devicechain-io/dc-microservice/core"
)

// celOps maps a schema comparison operator to its CEL operator token. Only these fixed,
// closed-enum tokens ever reach the generated source — an author never supplies an
// operator string, so no operator can be injected.
var celOps = map[CompareOp]string{
	OpGt: ">",
	OpGe: ">=",
	OpLt: "<",
	OpLe: "<=",
	OpEq: "==",
	OpNe: "!=",
}

// generateComparison renders a structured `<metric> <op> <threshold>` leaf into a CEL
// boolean expression, guarding against the CEL-injection analog of SQL injection (the
// review's concern): the metric name is validated against the ADR-042 token grammar
// (letters/digits/-/_ only — no quote, bracket, dot, or operator can appear) and then
// emitted as a quoted string literal; the threshold is emitted as a finite double literal.
// Nothing an author supplies is spliced into source as raw text. The `<metric> in m`
// guard makes evaluation total — an event missing the metric is a clean non-match, not a
// no-such-key evaluation error.
func generateComparison(metric string, op CompareOp, threshold float64) (string, error) {
	if err := validateMetric(metric); err != nil {
		return "", err
	}
	celOp, ok := celOps[op]
	if !ok {
		return "", fmt.Errorf("unsupported comparison operator %q", op)
	}
	lit, err := doubleLiteral(threshold)
	if err != nil {
		return "", err
	}
	key := strconv.Quote(metric) // grammar-validated, so this needs no escaping — belt-and-braces
	return fmt.Sprintf("%s in %s && %s[%s] %s %s", key, predicate.VarM, predicate.VarM, key, celOp, lit), nil
}

// validateMetric enforces the ADR-042 token grammar on a metric name — the injection
// whitelist. It is the single guard shared by the leaf comparison, the value selector, and
// the correlation anchor type, so every author-supplied identifier that reaches generated
// CEL (or a map key) is grammar-safe.
func validateMetric(name string) error {
	if err := core.ValidateToken(name); err != nil {
		// Deliberately generic ("identifier", not "metric"): the same guard validates the
		// value metric, the leaf metric, AND a correlation anchor type — the calling field
		// (metric / anchorType) supplies the concept, this supplies the grammar reason.
		return fmt.Errorf("identifier %q is not valid: %w", name, err)
	}
	return nil
}

// doubleLiteral renders a finite float as a CEL double literal. CEL is strictly typed:
// `m["x"] > 30` is a type error because 30 parses as an int and there is no double/int
// comparison overload — so the literal must always carry a decimal point or exponent to
// parse as a double. Non-finite thresholds are rejected (they cannot be authored meaningfully
// and are not representable as CEL literals).
func doubleLiteral(v float64) (string, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "", fmt.Errorf("threshold must be a finite number, got %v", v)
	}
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0" // force double parsing for an integer-valued threshold
	}
	return s, nil
}
