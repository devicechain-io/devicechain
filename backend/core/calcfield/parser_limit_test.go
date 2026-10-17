// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"errors"
	"strings"
	"testing"
)

// TestARefusedFormulaReportsABoundedNumberOfSyntaxErrors pins the parser's error-recovery
// limit. The source is a run of dangling ternaries, which the parser recovers from at
// every operator: unbounded it reports 185 errors for this 1 KiB source, and with the
// limit in baseEnv it stops after a handful. The formula must still be refused either way.
func TestARefusedFormulaReportsABoundedNumberOfSyntaxErrors(t *testing.T) {
	env, err := NewEnv([]Input{{Key: "a", Type: Double}})
	if err != nil {
		t.Fatal(err)
	}
	src := strings.Repeat("a ? : ", 170)
	_, err = env.Compile(Definition{Key: "x", Expression: src, Type: Double})
	var ce *CompileError
	if !errors.As(err, &ce) {
		t.Fatalf("a malformed formula must be refused with a CompileError, got %v", err)
	}
	if n := strings.Count(err.Error(), "ERROR:"); n > 40 {
		t.Fatalf("the refusal reports %d syntax errors; the parser's error recovery is not bounded", n)
	}
}
