// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package calcfield

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAllowlistIsTheSecondFence checks the allowlist walker on its own, against expressions
// a WIDER environment type-checks: the full CEL standard library with its macros, over the
// same inputs. Through Compile these never reach the walker, because the checker refuses them
// first, so without this test the walker's refusals would be untested — and the day a cel-go
// release adds something to the declared subset, the walker is all that stands in the way.
func TestAllowlistIsTheSecondFence(t *testing.T) {
	env := fixtureEnv(t)
	opts := []cel.EnvOption{cel.Variable(VarM, cel.MapType(cel.StringType, cel.DynType))}
	for _, in := range fixtureInputs {
		if isTopLevel(in.Key) {
			opts = append(opts, cel.Variable(in.Key, celType(in.Type)))
		}
	}
	wide, err := cel.NewEnv(opts...)
	require.NoError(t, err)
	cases := []struct{ expr, want string }{
		{"[temp].all(x, x > 0.0)", "comprehensions are not allowed"},
		{"[temp].exists(x, x > 0.0)", "comprehensions are not allowed"},
		{"[temp].size() > 0", "method call .size() is not allowed"},
		{"size(m) > 0", "function size() is not allowed"},
		{"temp == temp && [1] == [1]", "lists are not allowed"},
		{"{1: temp} == {1: temp}", "map literals are not allowed"},
		{"uint(fan) > 1u", "of type uint is not allowed"},
	}
	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			ast, iss := wide.Compile(c.expr)
			require.NoError(t, iss.Err(), "the wide environment must admit %q, or this case tests nothing", c.expr)
			w := &allowlist{env: env}
			_, err := w.check(ast.NativeRep().Expr())
			require.Error(t, err)
			assert.ErrorContains(t, err, c.want)
		})
	}
}
