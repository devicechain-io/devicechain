---
sidebar_position: 6
title: 解码器函数参考
---

# 解码器函数参考 {#decoder-functions}

[载荷解码器](../guides/payload-decoders.md)函数库中的函数，以及解码器所受的限制。本页由平台中的函数库自动生成，因此不会落后于实现。语言自身的内置函数不在此列：`size`、`has`，以及转换函数 `int`、`uint`、`double` 和 `bool`。

## 函数 {#decoder-functions-library}

解码器在 `envVersion` 中写明它所针对的函数库版本；本页描述的是版本 1。函数一旦发布，其行为就不会再改变：修复或变体会以新名称出现在新版本中，因此已保存的解码器始终给出相同的结果。说明文字是函数库自带的文本，在所有语言中均为英文。**成本**是调用本身增加的成本，单位为成本单位；表达式中的每个名称、字面量、运算符和类型转换还各增加约 1。接受 `bytes` 的函数从 `frame` 或另一个返回字节的函数读取字节。

### 整数 {#decoder-functions-integers}

| 函数 | 签名 | 作用 | 成本 |
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

### 浮点数 {#decoder-functions-floats}

| 函数 | 签名 | 作用 | 成本 |
| --- | --- | --- | --- |
| `f32be` | `f32be(bytes, int) -> double` | Reads an IEEE-754 32-bit big-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f32le` | `f32le(bytes, int) -> double` | Reads an IEEE-754 32-bit little-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f64be` | `f64be(bytes, int) -> double` | Reads an IEEE-754 64-bit big-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f64le` | `f64le(bytes, int) -> double` | Reads an IEEE-754 64-bit little-endian float at a byte offset; a non-finite value is an error. | 1 |

### 位 {#decoder-functions-bits}

| 函数 | 签名 | 作用 | 成本 |
| --- | --- | --- | --- |
| `bit` | `bit(bytes, int, int) -> int` | Reads one bit (0 or 1): bit 0 is the least significant bit of the byte at byteOff. | 1 |
| `bits` | `bits(bytes, int, int, int) -> int` | Reads an unsigned bit field of width 1..32 starting at bitOff, counted from the least significant bit of the byte at byteOff and continuing into the following bytes. | 1 |
| `sbits` | `sbits(bytes, int, int, int) -> int` | Like bits, sign-extended from the top bit of the field. | 1 |

### 二进制编码十进制 {#decoder-functions-bcd}

| 函数 | 签名 | 作用 | 成本 |
| --- | --- | --- | --- |
| `bcd` | `bcd(bytes, int, int) -> int` | Reads len (1 to 8) bytes of packed binary-coded decimal, most significant digit first; a nibble above 9 is an error. | 1 per byte read (the len argument) |

### 字节 {#decoder-functions-bytes}

| 函数 | 签名 | 作用 | 成本 |
| --- | --- | --- | --- |
| `slice` | `slice(bytes, int, int) -> bytes` | Copies len bytes starting at off; a range outside the value is an error. | 1 per byte copied, at least 1 |

### 文本帧 {#decoder-functions-text}

| 函数 | 签名 | 作用 | 成本 |
| --- | --- | --- | --- |
| `hex.decode` | `hex.decode(string) -> bytes` | Decodes hexadecimal text (even length, no separators) of at most 8192 bytes. | 1 per byte of the text, at least 1 |
| `base64.decode` | `base64.decode(string) -> bytes` | Decodes standard padded base64 text of at most 8192 bytes into at most 4096 bytes. | 1 per byte of the text, at least 1 |

### 编解码器 {#decoder-functions-codecs}

| 函数 | 签名 | 作用 | 成本 |
| --- | --- | --- | --- |
| `lpp.decode` | `lpp.decode(bytes) -> map(string, double)` | Decodes a Cayenne LPP frame into channel-suffixed keys such as temperature_1; a truncated or unknown record is an error. | 1 per byte of the frame, at least 1 |

### 数学 {#decoder-functions-math}

| 函数 | 签名 | 作用 | 成本 |
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

## 限制 {#decoder-functions-limits}

超出限制的解码器在保存或发布时会被拒绝。超出输入限制的输入只会使这一次解码失败。

| 限制 | 取值 |
| --- | --- |
| 原始帧，单位为字节 | 4,096 |
| JSON 输入（`json` 或 `decoded`），单位为字节 | 65,536 |
| JSON 嵌套深度 | 32 |
| 整个解码器文档，单位为字节 | 32,768 |
| 单个表达式，单位为字节 | 2,048 |
| `let` 绑定 | 16 |
| 分段 | 8 |
| 单个分段可输出的读数（标量行加上映射行的 `maxKeys`） | 64 |
| 已保存的测试向量 | 20 |
| 传给 `hex.decode` 或 `base64.decode` 的文本，单位为字节 | 8,192 |
| 单个 `in` 列表中的字面量 | 16 |
| `let`、分段或测试向量的名称，单位为字节 | 64 |
| 输出键或前缀，单位为字节 | 128 |
| 单个表达式的估算成本 | 10,000 |
| 整个解码器的估算成本 | 50,000 |
