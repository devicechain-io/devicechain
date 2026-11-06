---
sidebar_position: 6
title: Decoder Function Reference
---

# Decoder Function Reference {#decoder-functions}

Every function a [payload decoder](../guides/payload-decoders.md) can call, and the limits a decoder is held to. This page is generated from the function library in the platform, so it cannot fall behind it.

## Functions {#decoder-functions-library}

A decoder names the library version it was written for in `envVersion`; this page describes version 1. What a function does never changes once it has shipped: a fix or a variant arrives under a new name in a new version, so a saved decoder keeps giving the same answers. The descriptions are the library's own text and are in English in every language. **Cost** is what the call itself adds, in cost units; every name, literal, operator and conversion in an expression adds about 1 as well. A function that takes `bytes` reads them from `frame` or from another function that returns bytes.

### Integers {#decoder-functions-integers}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `u8` | `u8(bytes, int) -> int` | Reads a 8-bit unsigned value at a byte offset. | 1 |
| `u16be` | `u16be(bytes, int) -> int` | Reads a 16-bit unsigned big-endian value at a byte offset. | 1 |
| `u16le` | `u16le(bytes, int) -> int` | Reads a 16-bit unsigned little-endian value at a byte offset. | 1 |
| `u24be` | `u24be(bytes, int) -> int` | Reads a 24-bit unsigned big-endian value at a byte offset. | 1 |
| `u24le` | `u24le(bytes, int) -> int` | Reads a 24-bit unsigned little-endian value at a byte offset. | 1 |
| `u32be` | `u32be(bytes, int) -> int` | Reads a 32-bit unsigned big-endian value at a byte offset. | 1 |
| `u32le` | `u32le(bytes, int) -> int` | Reads a 32-bit unsigned little-endian value at a byte offset. | 1 |
| `i8` | `i8(bytes, int) -> int` | Reads a 8-bit signed value at a byte offset. | 1 |
| `i16be` | `i16be(bytes, int) -> int` | Reads a 16-bit signed big-endian value at a byte offset. | 1 |
| `i16le` | `i16le(bytes, int) -> int` | Reads a 16-bit signed little-endian value at a byte offset. | 1 |
| `i24be` | `i24be(bytes, int) -> int` | Reads a 24-bit signed big-endian value at a byte offset. | 1 |
| `i24le` | `i24le(bytes, int) -> int` | Reads a 24-bit signed little-endian value at a byte offset. | 1 |
| `i32be` | `i32be(bytes, int) -> int` | Reads a 32-bit signed big-endian value at a byte offset. | 1 |
| `i32le` | `i32le(bytes, int) -> int` | Reads a 32-bit signed little-endian value at a byte offset. | 1 |

### Floating point {#decoder-functions-floats}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `f32be` | `f32be(bytes, int) -> double` | Reads an IEEE-754 32-bit big-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f32le` | `f32le(bytes, int) -> double` | Reads an IEEE-754 32-bit little-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f64be` | `f64be(bytes, int) -> double` | Reads an IEEE-754 64-bit big-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f64le` | `f64le(bytes, int) -> double` | Reads an IEEE-754 64-bit little-endian float at a byte offset; a non-finite value is an error. | 1 |

### Bits {#decoder-functions-bits}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `bit` | `bit(bytes, int, int) -> int` | Reads one bit (0 or 1): bit 0 is the least significant bit of the byte at byteOff. | 1 |
| `bits` | `bits(bytes, int, int, int) -> int` | Reads an unsigned bit field of width 1..32 starting at bitOff, counted from the least significant bit of the byte at byteOff and continuing into the following bytes. | 1 |
| `sbits` | `sbits(bytes, int, int, int) -> int` | Like bits, sign-extended from the top bit of the field. | 1 |

### Binary-coded decimal {#decoder-functions-bcd}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `bcd` | `bcd(bytes, int, int) -> int` | Reads len (1 to 8) bytes of packed binary-coded decimal, most significant digit first; a nibble above 9 is an error. | 1 per byte read (the len argument) |

### Bytes {#decoder-functions-bytes}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `slice` | `slice(bytes, int, int) -> bytes` | Copies len bytes starting at off; a range outside the value is an error. | 1 per byte copied, at least 1 |

### Text frames {#decoder-functions-text}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `hex.decode` | `hex.decode(string) -> bytes` | Decodes hexadecimal text (even length, no separators) of at most 8192 characters. | 1 per character of the text, at least 1 |
| `base64.decode` | `base64.decode(string) -> bytes` | Decodes standard padded base64 text of at most 8192 characters into at most 4096 bytes. | 1 per character of the text, at least 1 |

### Codecs {#decoder-functions-codecs}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `lpp.decode` | `lpp.decode(bytes) -> map(string, double)` | Decodes a Cayenne LPP frame into channel-suffixed keys such as temperature_1; a truncated or unknown record is an error. | 1 per byte of the frame, at least 1 |

### Maths {#decoder-functions-math}

| Function | Signature | What it does | Cost |
| --- | --- | --- | --- |
| `math.abs` | `math.abs(double\|int) -> double\|int` | Absolute value. | 1 |
| `math.floor` | `math.floor(double) -> double` | Largest integral value not above the argument. | 1 |
| `math.ceil` | `math.ceil(double) -> double` | Smallest integral value not below the argument. | 1 |
| `math.round` | `math.round(double) -> double` | Rounds half away from zero. | 1 |
| `math.sqrt` | `math.sqrt(double\|int) -> double` | Square root; a negative argument is an error. | 1 |
| `math.pow` | `math.pow(double\|int, int) -> double` | Integer power by repeated multiplication. | 1 |
| `math.min` | `math.min(a, b) -> double\|int` | The smaller of two numbers. | 1 |
| `math.max` | `math.max(a, b) -> double\|int` | The larger of two numbers. | 1 |
| `math.clamp` | `math.clamp(x, lo, hi) -> double\|int` | x limited to [lo, hi]; lo above hi is an error. | 1 |

## Limits {#decoder-functions-limits}

A decoder that is over a limit is refused when you save or publish it. An input over an input limit fails that one decode.

| Limit | Value |
| --- | --- |
| Raw frame, in bytes | 4,096 |
| JSON input (`json` or `decoded`), in bytes | 65,536 |
| JSON nesting depth | 32 |
| The whole decoder document, in bytes | 32,768 |
| One expression, in bytes | 2,048 |
| `let` bindings | 16 |
| Sections | 8 |
| Readings one section can emit (scalar rows plus the `maxKeys` of map rows) | 64 |
| Saved vectors | 20 |
| Text given to `hex.decode` or `base64.decode`, in characters | 8,192 |
| Literals in one `in` list | 16 |
| A `let`, section or vector name, in bytes | 64 |
| An output key or prefix, in bytes | 128 |
| Estimated cost of one expression | 10,000 |
| Estimated cost of a whole decoder | 50,000 |
