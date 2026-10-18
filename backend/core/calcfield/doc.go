// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package calcfield compiles and evaluates calculated-metric formulas: a metric on a device
// profile whose value is a CEL expression over the other metrics of the SAME sample entry
// (`voltage * current / 1000`, `degC * 1.8 + 32`, `temp > 80 && fan == 0`).
//
// It is the one evaluator every consumer shares — device-management's resolver, the rule
// preview's draft override, the Test-formula panel and a future backfill — so they all run
// byte-identical semantics. It holds no wiring to any of them.
//
// # The language
//
// An [Env] is built from the profile's declared, MEASURED inputs. Each input whose key is a
// plain identifier is a typed top-level variable (DOUBLE → double, INT → int, BOOLEAN →
// bool). Every input, identifier or not, is also reachable through the map `m`: `m["a-b"]`
// reads a value (and must be wrapped in the conversion matching the input's type, e.g.
// `double(m["a-b"])`, so nothing dynamic ever reaches the result), while `has(m.fan)` and
// `"a-b" in m` test presence.
//
// Allowed: `+ - * / %`, comparisons, `&& || !`, the ternary, `has()`, `in m`, int/double/bool
// literals, the conversions int(), double() and bool() (the last only to read a BOOLEAN input
// through m), and the fixed-arity functions math.abs, math.floor, math.ceil, math.round,
// math.sqrt, math.pow (with an int exponent: a fractional power is not bit-identical across
// CPU architectures), math.min, math.max and math.clamp. int and double mix in + - * / and
// in ordering (the int operand is promoted to double), so `voltage * current / 1000`
// compiles; they do not mix in == != or in the two branches of a ternary, where `80.0` or
// `0.0` is needed. Everything else is refused at [Env.Compile] — first by the checker, since
// the environment declares only a subset of the CEL standard library, then by an allowlist
// over the checked expression: no time, durations, randomness, strings (except as a constant
// key of `m`), lists, maps, macros other than has, comprehensions, regex, or any function
// reaching outside the sample.
//
// # Per-device constants
//
// A formula may also read numbers its DEVICE carries — a calibration factor, a CT ratio — as
// `device.name` (or `device["a-name"]`, `has(device.name)`, `"name" in device`). Referencing a
// constant is declaring it: there is no list to keep in step with the formula. Every constant
// is a DOUBLE (int(device.k) reads a whole number as an int), a formula may read at most
// [MaxConstants], and one that reads constants and no measured input is refused. The caller
// reads the values from the device record it already holds and hands them over with the
// sample ([Sample.UseConstants], built once per event from [NewConstant], [ConstantFromString]
// and [InvalidConstant]); this package never reads a device. An unguarded constant a device
// lacks skips the formula as [SkipMissingInput], one that is not a number as
// [SkipInvalidInput], one that is not finite as [SkipNonFinite] — before the formula runs, as
// for an input. The value is frozen into the computed result: nothing here remembers a
// constant, so editing the device later never rewrites a stored value.
//
// # Chaining
//
// A formula may read another calculated metric of the same profile version, to a chain of
// at most [MaxChainDepth] calculated metrics (kW, then kVA from it, then power factor). A
// [Plan] compiles the metrics together: it declares each calculated metric as an input after
// the measured ones, refuses a ring of metrics reading each other ([CycleError], naming the
// ring) and a chain too deep ([DepthError], naming the chain), and orders the formulas so each
// follows what it reads. [Plan.Eval] runs one in that order and writes the value it computed
// back into the sample as its declared type, the exact string that is stored, so a downstream
// formula reads what is persisted and not a wider intermediate. A metric that produced no value
// leaves its slot absent: a formula that reads it unguarded skips as [SkipMissingInput], one
// that tests it with has() runs. A profile whose formulas read only measured metrics has no
// edges and pays nothing for any of this. Every consumer that evaluates a profile's formulas
// together (the resolver, the draft preview) goes through a Plan.
//
// # Fail closed at compile, skip loudly at eval
//
// [Env.Compile] parses, type-checks the result to the declared type exactly (an int formula
// for a DOUBLE metric is refused; write double()), requires at least one input, bounds the
// source length and the static worst-case cost. The static cost is the only cost bound: the
// language admits nothing whose cost grows with its input, so the estimate bounds every
// evaluation and the program carries no runtime cost limit (see [CostCeiling]).
//
// [Formula.Eval] never fails the sample: a formula that cannot produce a value for it
// returns a [SkipReason] and no value — never a plausible default. An input the formula never
// tests for presence (no `has(m.k)`, no `"k" in m`) is REQUIRED: when it is absent the
// formula is skipped as [SkipMissingInput] before it runs, so `temp > 80 || fan == 0` skips
// on a reading without fan even though CEL would short-circuit it to true on a hot one —
// whether an absent input is noticed never depends on operand order or on the other inputs'
// values. `has(m.fan) ? fan : 0` is the author's explicit choice of a default. An input that
// is guarded is read like any other value; if a read of it while absent reaches the result,
// the formula skips, and if && or || absorbs it (`temp > 80.0 || fan > 1` on a hot reading),
// the absorbed value is the result, the same in either operand order.
//
// No double inside an evaluation is ever NaN or ±Inf: a non-finite input value is refused
// at [Sample.Set], and a division, overflow or square root producing one is an error. Either
// skips the formula as [SkipNonFinite] wherever it occurs, so `voltage / current > 10.0`
// skips when current is 0 instead of storing a plausible true. A DOUBLE or INT result the
// measurement store cannot hold (magnitude [StoredMagnitude] or more) skips as
// [SkipUnstorable] whatever the definition's bounds, rather than failing its reading there.
//
// # Determinism
//
// The stored measurement value is a string and the value of record, so formatting is a
// correctness requirement: [FormatDouble], [FormatInt] and [FormatBool] render a double as
// the shortest round-trip form (strconv 'g', -1) with negative zero normalized to "0", an int
// in base 10, and a bool as "1"/"0" (as the resolver stores booleans). The environment has no
// clock, no randomness and no state, so a formula is a pure function of the sample.
//
// # No state — a red line
//
// Nothing in this package keeps a value between samples. A formula that needs a previous
// reading, a window or another message is not a calculated metric; it belongs in a windowed
// derivation stage, never in a cache here.
package calcfield
