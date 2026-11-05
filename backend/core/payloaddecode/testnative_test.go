// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// The natives below exist only in the test binary. They are registered before the first
// environment is built (package init), and they are the deliberately dishonest functions the
// cost machinery has to catch: one that spends more than it declared, one that declares
// almost nothing but walks the whole frame, and one that sleeps.

// nativeUnderRuntime: declares static cost 1, but REPORTS 500 at run time. The runtime
// CostLimit is the only thing that can stop it.
func nativeUnderRuntime() fnSpec {
	return fnSpec{
		name: "test.underRuntime", overload: "test_under_runtime", sig: "test.underRuntime(bytes) -> int", doc: "test only",
		args: []*cel.Type{cel.BytesType}, ret: cel.IntType,
		bind:   cel.UnaryBinding(func(ref.Val) ref.Val { return types.Int(1) }),
		static: constCost(1), actual: constActual(500),
	}
}

// nativeWalker: declares cost 1 statically AND at run time, but walks the whole byte slice
// `rounds` times. Static and runtime agree, so only measuring ns per cost can catch it.
func nativeWalker(name string, rounds int) fnSpec {
	return fnSpec{
		name: name, overload: "test_" + name, sig: name + "(bytes) -> int", doc: "test only",
		args: []*cel.Type{cel.BytesType}, ret: cel.IntType,
		bind: cel.UnaryBinding(func(v ref.Val) ref.Val {
			b := []byte(v.(types.Bytes))
			var sum int64
			for r := 0; r < rounds; r++ {
				for _, c := range b {
					sum += int64(c) ^ int64(r)
				}
			}
			return types.Int(sum)
		}),
		static: constCost(1), actual: constActual(1),
	}
}

// nativeSleeper: an honest-looking cost, a slow body. Used to show the perf gate goes red.
func nativeSleeper() fnSpec {
	return fnSpec{
		name: "test.sleep", overload: "test_sleep", sig: "test.sleep(int) -> int", doc: "test only",
		args: []*cel.Type{cel.IntType}, ret: cel.IntType,
		bind: cel.UnaryBinding(func(v ref.Val) ref.Val {
			time.Sleep(time.Duration(v.(types.Int)) * time.Microsecond)
			return v
		}),
		static: constCost(1), actual: constActual(1),
	}
}

var _ checker.AstNode

func init() {
	ownedFuncs = append(ownedFuncs, nativeUnderRuntime(), nativeWalker("test.walk", 200), nativeSleeper())
	for i := range ownedFuncs {
		byName[ownedFuncs[i].name] = &ownedFuncs[i]
	}
}
