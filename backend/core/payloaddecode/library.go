// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package payloaddecode

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/checker"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
)

// nativeError is the error a library function reports. Its text is built only from the
// function name and the offset, so it is safe to show a tenant.
type nativeError struct {
	Fn     string
	Offset int // -1 when the failure has no offset
	Msg    string
}

func (e *nativeError) Error() string {
	if e.Offset >= 0 {
		return fmt.Sprintf("%s: %s (offset %d)", e.Fn, e.Msg, e.Offset)
	}
	return fmt.Sprintf("%s: %s", e.Fn, e.Msg)
}

func nativeErr(fn string, off int64, format string, args ...any) ref.Val {
	o := -1
	if off >= 0 {
		o = int(min(off, math.MaxInt32))
	}
	return types.WrapErr(&nativeError{Fn: fn, Offset: o, Msg: fmt.Sprintf(format, args...)})
}

// errNonFinite is a double operation whose result is NaN or ±Inf.
var errNonFinite = errors.New("non-finite double")

// finiteDouble is f as a CEL double, or the non-finite error. Every double-producing function
// of the environment returns through it, so no double inside an evaluation is NaN or ±Inf.
func finiteDouble(f float64) ref.Val {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return types.WrapErr(errNonFinite)
	}
	return types.Double(f)
}

// fnSpec is one owned function: its declaration, its binding, and BOTH of its costs, in one
// place so the static estimate and the runtime charge cannot drift apart silently.
type fnSpec struct {
	name, overload, doc, sig string
	// costDoc is the call's own charge in words, for the generated reference; it is checked
	// against static and actual by TestFunctionCostDocsMatchTheCharges.
	costDoc string
	args    []*cel.Type
	ret     *cel.Type
	bind    cel.OverloadOpt
	// static is the worst-case call cost and, for a function returning bytes or a map, the
	// worst-case result size. It receives the argument nodes, whose computed sizes the
	// estimator has propagated.
	static func(args []checker.AstNode) (cost uint64, size *checker.SizeEstimate)
	// actual is the call's real cost from its arguments and result.
	actual func(args []ref.Val, result ref.Val) uint64
	// sizeDependent says the call's work grows with the size of its input (a copy, a decode).
	// An expression containing one is always run under the runtime CostLimit; see
	// trackedExpression.
	sizeDependent bool
}

func constCost(c uint64) func([]checker.AstNode) (uint64, *checker.SizeEstimate) {
	return func([]checker.AstNode) (uint64, *checker.SizeEstimate) { return c, nil }
}

func constActual(c uint64) func([]ref.Val, ref.Val) uint64 {
	return func([]ref.Val, ref.Val) uint64 { return c }
}

// sizeMax is the largest size a node can have, bounded by limit: a native that refuses input
// over limit never works on more, so the bound is sound even for an unsized dynamic value.
func sizeMax(n checker.AstNode, limit uint64) uint64 {
	if sz := n.ComputedSize(); sz != nil && sz.Max < limit {
		return sz.Max
	}
	return limit
}

// textSizeMax is the largest byte length a text argument can have, bounded by MaxTextBytes. A
// string literal is measured in BYTES (cel-go's size estimate counts characters, and the
// native's cap and its runtime cost count bytes).
func textSizeMax(n checker.AstNode) uint64 {
	if n.Expr().Kind() == celast.LiteralKind {
		if s, ok := n.Expr().AsLiteral().(types.String); ok {
			return min(uint64(len(s)), MaxTextBytes)
		}
	}
	return sizeMax(n, MaxTextBytes)
}

// literalInt is the value of an integer literal node.
func literalInt(n checker.AstNode) (int64, bool) {
	if n.Expr().Kind() != celast.LiteralKind {
		return 0, false
	}
	v, ok := n.Expr().AsLiteral().(types.Int)
	return int64(v), ok
}

func asBytes(v ref.Val) ([]byte, bool) {
	b, ok := v.(types.Bytes)
	return []byte(b), ok
}

func asInt(v ref.Val) (int64, bool) {
	n, ok := v.(types.Int)
	return int64(n), ok
}

func asString(v ref.Val) (string, bool) {
	s, ok := v.(types.String)
	return string(s), ok
}

func badArgs(fn string) ref.Val { return nativeErr(fn, -1, "unexpected argument type") }

// readerSpec builds a fixed-width numeric reader `name(b bytes, off int)`.
func readerSpec(name string, width int, signed, bigEndian, float bool) fnSpec {
	ret, retName := cel.IntType, "int"
	doc := fmt.Sprintf("Reads a %d-bit %s value at a byte offset.", width*8, map[bool]string{true: "signed", false: "unsigned"}[signed])
	if width > 1 {
		doc = fmt.Sprintf("Reads a %d-bit %s %s-endian value at a byte offset.", width*8,
			map[bool]string{true: "signed", false: "unsigned"}[signed], map[bool]string{true: "big", false: "little"}[bigEndian])
	}
	if float {
		ret, retName = cel.DoubleType, "double"
		doc = fmt.Sprintf("Reads an IEEE-754 %d-bit %s-endian float at a byte offset; a non-finite value is an error.",
			width*8, map[bool]string{true: "big", false: "little"}[bigEndian])
	}
	return fnSpec{
		name: name, overload: "payloaddecode_" + name, sig: name + "(bytes, int) -> " + retName, doc: doc, costDoc: costOne,
		args: []*cel.Type{cel.BytesType, cel.IntType}, ret: ret,
		bind: cel.BinaryBinding(func(bv, ov ref.Val) ref.Val {
			b, ok1 := asBytes(bv)
			off, ok2 := asInt(ov)
			if !ok1 || !ok2 {
				return badArgs(name)
			}
			if off < 0 || off > int64(len(b)) || int64(width) > int64(len(b))-off {
				return nativeErr(name, off, "reads %d byte(s) at offset %d of a %d-byte frame", width, off, len(b))
			}
			p := b[off : off+int64(width)]
			var u uint64
			if bigEndian {
				for _, c := range p {
					u = u<<8 | uint64(c)
				}
			} else {
				for i := width - 1; i >= 0; i-- {
					u = u<<8 | uint64(p[i])
				}
			}
			switch {
			case float && width == 4:
				return finiteDouble(float64(math.Float32frombits(uint32(u))))
			case float:
				return finiteDouble(math.Float64frombits(u))
			case signed:
				shift := uint(64 - width*8)
				return types.Int(int64(u<<shift) >> shift)
			}
			return types.Int(int64(u))
		}),
		static: constCost(1), actual: constActual(1),
	}
}

// bitField reads width bits starting at bit bitOff, counted from the least significant bit of
// b[byteOff] and continuing into the following bytes (bit 8 is the LSB of b[byteOff+1]). The
// field's own least significant bit is bit bitOff.
func bitField(fn string, a []ref.Val, sign, withWidth bool) ref.Val {
	b, ok1 := asBytes(a[0])
	byteOff, ok2 := asInt(a[1])
	bitOff, ok3 := asInt(a[2])
	if !ok1 || !ok2 || !ok3 {
		return badArgs(fn)
	}
	width := int64(1)
	if withWidth {
		w, ok := asInt(a[3])
		if !ok {
			return badArgs(fn)
		}
		width = w
	}
	if width < 1 || width > 32 {
		return nativeErr(fn, -1, "width %d is outside 1..32", width)
	}
	if bitOff < 0 || (!withWidth && bitOff > 7) {
		return nativeErr(fn, byteOff, "bit offset %d is out of range", bitOff)
	}
	// The field starts in byte `first` at bit `shift` and touches `need` bytes. Every
	// comparison is made before any addition, so no offset can overflow.
	if byteOff < 0 || byteOff >= int64(len(b)) || bitOff/8 >= int64(len(b))-byteOff {
		return nativeErr(fn, byteOff, "reads past the end of a %d-byte frame", len(b))
	}
	first, shift := byteOff+bitOff/8, bitOff%8
	need := (shift + width + 7) / 8
	if need > int64(len(b))-first {
		return nativeErr(fn, byteOff, "reads past the end of a %d-byte frame", len(b))
	}
	var v uint64
	for i := need - 1; i >= 0; i-- {
		v = v<<8 | uint64(b[first+i])
	}
	v = (v >> uint(shift)) & (1<<uint(width) - 1)
	if sign {
		sh := uint(64 - width)
		return types.Int(int64(v<<sh) >> sh)
	}
	return types.Int(int64(v))
}

func textActual(a []ref.Val, _ ref.Val) uint64 {
	if s, ok := asString(a[0]); ok && len(s) <= MaxTextBytes {
		return max(uint64(len(s)), 1)
	}
	return 1
}

const (
	costOne     = "1"
	costPerChar = "1 per character of the text, at least 1"
)

// ownedFuncs is the function library, env version 1. A function's semantics NEVER change
// once shipped: a fix or variant is a new name in a new env version.
var ownedFuncs = buildOwned()

func buildOwned() []fnSpec {
	fs := []fnSpec{
		readerSpec("u8", 1, false, true, false),
		readerSpec("u16be", 2, false, true, false), readerSpec("u16le", 2, false, false, false),
		readerSpec("u24be", 3, false, true, false), readerSpec("u24le", 3, false, false, false),
		readerSpec("u32be", 4, false, true, false), readerSpec("u32le", 4, false, false, false),
		readerSpec("i8", 1, true, true, false),
		readerSpec("i16be", 2, true, true, false), readerSpec("i16le", 2, true, false, false),
		readerSpec("i24be", 3, true, true, false), readerSpec("i24le", 3, true, false, false),
		readerSpec("i32be", 4, true, true, false), readerSpec("i32le", 4, true, false, false),
		readerSpec("f32be", 4, true, true, true), readerSpec("f32le", 4, true, false, true),
		readerSpec("f64be", 8, true, true, true), readerSpec("f64le", 8, true, false, true),
	}
	fs = append(fs,
		fnSpec{
			costDoc: costOne, name: "bit", overload: "payloaddecode_bit", sig: "bit(bytes, int, int) -> int",
			doc:  "Reads one bit (0 or 1): bit 0 is the least significant bit of the byte at byteOff.",
			args: []*cel.Type{cel.BytesType, cel.IntType, cel.IntType}, ret: cel.IntType,
			bind:   cel.FunctionBinding(func(a ...ref.Val) ref.Val { return bitField("bit", a, false, false) }),
			static: constCost(1), actual: constActual(1),
		},
		fnSpec{
			costDoc: costOne, name: "bits", overload: "payloaddecode_bits", sig: "bits(bytes, int, int, int) -> int",
			doc:  "Reads an unsigned bit field of width 1..32 starting at bitOff, counted from the least significant bit of the byte at byteOff and continuing into the following bytes.",
			args: []*cel.Type{cel.BytesType, cel.IntType, cel.IntType, cel.IntType}, ret: cel.IntType,
			bind:   cel.FunctionBinding(func(a ...ref.Val) ref.Val { return bitField("bits", a, false, true) }),
			static: constCost(1), actual: constActual(1),
		},
		fnSpec{
			costDoc: costOne, name: "sbits", overload: "payloaddecode_sbits", sig: "sbits(bytes, int, int, int) -> int",
			doc:  "Like bits, sign-extended from the top bit of the field.",
			args: []*cel.Type{cel.BytesType, cel.IntType, cel.IntType, cel.IntType}, ret: cel.IntType,
			bind:   cel.FunctionBinding(func(a ...ref.Val) ref.Val { return bitField("sbits", a, true, true) }),
			static: constCost(1), actual: constActual(1),
		},
		fnSpec{
			costDoc: "1 per byte read (the len argument)", name: "bcd", overload: "payloaddecode_bcd", sig: "bcd(bytes, int, int) -> int",
			doc:  "Reads len (1 to 8) bytes of packed binary-coded decimal, most significant digit first; a nibble above 9 is an error.",
			args: []*cel.Type{cel.BytesType, cel.IntType, cel.IntType}, ret: cel.IntType,
			bind: cel.FunctionBinding(func(a ...ref.Val) ref.Val {
				b, ok1 := asBytes(a[0])
				off, ok2 := asInt(a[1])
				n, ok3 := asInt(a[2])
				if !ok1 || !ok2 || !ok3 {
					return badArgs("bcd")
				}
				if n < 1 || n > 8 {
					return nativeErr("bcd", off, "length %d is outside 1..8", n)
				}
				if off < 0 || off > int64(len(b)) || n > int64(len(b))-off {
					return nativeErr("bcd", off, "reads %d byte(s) at offset %d of a %d-byte frame", n, off, len(b))
				}
				var v int64
				for _, c := range b[off : off+n] {
					hi, lo := int64(c>>4), int64(c&0x0f)
					if hi > 9 || lo > 9 {
						return nativeErr("bcd", off, "byte 0x%02x is not packed decimal", c)
					}
					v = v*100 + hi*10 + lo
				}
				return types.Int(v)
			}),
			static: func(a []checker.AstNode) (uint64, *checker.SizeEstimate) {
				if n, ok := literalInt(a[2]); ok && n >= 1 && n <= 8 {
					return uint64(n), nil
				}
				return 8, nil
			},
			actual: func(a []ref.Val, _ ref.Val) uint64 {
				if n, ok := asInt(a[2]); ok && n >= 1 && n <= 8 {
					return uint64(n)
				}
				return 1
			},
		},
		fnSpec{
			sizeDependent: true, costDoc: "1 per byte copied, at least 1", name: "slice", overload: "payloaddecode_slice", sig: "slice(bytes, int, int) -> bytes",
			doc:  "Copies len bytes starting at off; a range outside the value is an error.",
			args: []*cel.Type{cel.BytesType, cel.IntType, cel.IntType}, ret: cel.BytesType,
			bind: cel.FunctionBinding(func(a ...ref.Val) ref.Val {
				b, ok1 := asBytes(a[0])
				off, ok2 := asInt(a[1])
				n, ok3 := asInt(a[2])
				if !ok1 || !ok2 || !ok3 {
					return badArgs("slice")
				}
				if off < 0 || n < 0 || off > int64(len(b)) || n > int64(len(b))-off {
					return nativeErr("slice", off, "range of %d byte(s) at offset %d is outside a %d-byte value", n, off, len(b))
				}
				out := make([]byte, n)
				copy(out, b[off:off+n])
				return types.Bytes(out)
			}),
			static: func(a []checker.AstNode) (uint64, *checker.SizeEstimate) {
				src := sizeMax(a[0], MaxFrameBytes)
				if n, ok := literalInt(a[2]); ok && n >= 0 && uint64(n) < src {
					src = uint64(n)
				}
				return max(src, 1), &checker.SizeEstimate{Min: 0, Max: src}
			},
			actual: func(_ []ref.Val, r ref.Val) uint64 {
				if b, ok := r.(types.Bytes); ok {
					return max(uint64(len(b)), 1)
				}
				return 1
			},
		},
		fnSpec{
			sizeDependent: true, costDoc: costPerChar, name: "hex.decode", overload: "payloaddecode_hex_decode", sig: "hex.decode(string) -> bytes",
			doc:  "Decodes hexadecimal text (even length, no separators) of at most 8192 characters.",
			args: []*cel.Type{cel.StringType}, ret: cel.BytesType,
			bind: cel.UnaryBinding(func(v ref.Val) ref.Val {
				s, ok := asString(v)
				if !ok {
					return badArgs("hex.decode")
				}
				if len(s) > MaxTextBytes {
					return nativeErr("hex.decode", -1, "text of %d characters is above the cap of %d", len(s), MaxTextBytes)
				}
				out, err := hex.DecodeString(s)
				if err != nil {
					return nativeErr("hex.decode", -1, "not hexadecimal text")
				}
				return types.Bytes(out)
			}),
			static: func(a []checker.AstNode) (uint64, *checker.SizeEstimate) {
				n := textSizeMax(a[0])
				return max(n, 1), &checker.SizeEstimate{Min: 0, Max: n / 2}
			},
			actual: textActual,
		},
		fnSpec{
			sizeDependent: true, costDoc: costPerChar, name: "base64.decode", overload: "payloaddecode_base64_decode", sig: "base64.decode(string) -> bytes",
			doc:  "Decodes standard padded base64 text of at most 8192 characters into at most 4096 bytes.",
			args: []*cel.Type{cel.StringType}, ret: cel.BytesType,
			bind: cel.UnaryBinding(func(v ref.Val) ref.Val {
				s, ok := asString(v)
				if !ok {
					return badArgs("base64.decode")
				}
				if len(s) > MaxTextBytes || len(s)/4*3 > MaxFrameBytes+3 {
					return nativeErr("base64.decode", -1, "text of %d characters is above the cap", len(s))
				}
				out, err := base64.StdEncoding.DecodeString(s)
				if err != nil {
					return nativeErr("base64.decode", -1, "not base64 text")
				}
				if len(out) > MaxFrameBytes {
					return nativeErr("base64.decode", -1, "decodes to more than %d bytes", MaxFrameBytes)
				}
				return types.Bytes(out)
			}),
			static: func(a []checker.AstNode) (uint64, *checker.SizeEstimate) {
				n := textSizeMax(a[0])
				return max(n, 1), &checker.SizeEstimate{Min: 0, Max: min(n/4*3, MaxFrameBytes)}
			},
			actual: textActual,
		},
		fnSpec{
			sizeDependent: true, costDoc: "1 per byte of the frame, at least 1", name: "lpp.decode", overload: "payloaddecode_lpp_decode", sig: "lpp.decode(bytes) -> map(string, double)",
			doc:  "Decodes a Cayenne LPP frame into channel-suffixed keys such as temperature_1; a truncated or unknown record is an error.",
			args: []*cel.Type{cel.BytesType}, ret: cel.MapType(cel.StringType, cel.DoubleType),
			bind: cel.UnaryBinding(func(v ref.Val) ref.Val {
				b, ok := asBytes(v)
				if !ok {
					return badArgs("lpp.decode")
				}
				if len(b) > MaxFrameBytes {
					return nativeErr("lpp.decode", -1, "frame of %d bytes is above the cap of %d", len(b), MaxFrameBytes)
				}
				m, err := decodeLPP(b)
				if err != nil {
					return types.WrapErr(err)
				}
				return types.DefaultTypeAdapter.NativeToValue(m)
			}),
			static: func(a []checker.AstNode) (uint64, *checker.SizeEstimate) {
				n := sizeMax(a[0], MaxFrameBytes)
				return max(n, 1), &checker.SizeEstimate{Min: 0, Max: n/3 + 1}
			},
			actual: func(a []ref.Val, _ ref.Val) uint64 {
				if b, ok := asBytes(a[0]); ok && len(b) <= MaxFrameBytes {
					return max(uint64(len(b)), 1)
				}
				return 1
			},
		},
	)
	return fs
}

// byName indexes the library by function name.
var byName = func() map[string]*fnSpec {
	m := make(map[string]*fnSpec, len(ownedFuncs))
	for i := range ownedFuncs {
		m[ownedFuncs[i].name] = &ownedFuncs[i]
	}
	return m
}()

// FunctionDoc describes one library function for help text and the generated reference.
type FunctionDoc struct {
	Name      string
	Signature string
	Doc       string
	// Cost is the function's own charge in words ("1", "1 per byte copied"). Every evaluated
	// node also has the standard charge of the expression language.
	Cost string
}

// Functions lists the function library of env version 1 in a stable order: the owned
// functions, then the math functions.
func Functions() []FunctionDoc {
	out := make([]FunctionDoc, 0, len(ownedFuncs)+len(mathDocs))
	for _, f := range ownedFuncs {
		out = append(out, FunctionDoc{Name: f.name, Signature: f.sig, Doc: f.doc, Cost: f.costDoc})
	}
	return append(out, mathDocs...)
}
