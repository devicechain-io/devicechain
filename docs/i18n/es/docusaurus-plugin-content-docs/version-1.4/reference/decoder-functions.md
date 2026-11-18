---
sidebar_position: 6
title: Referencia de funciones del decodificador
---

# Referencia de funciones del decodificador {#decoder-functions}

Las funciones de la biblioteca de los [decodificadores de payload](../guides/payload-decoders.md) y los límites a los que está sujeto un decodificador. Esta página se genera a partir de la biblioteca de funciones de la plataforma, así que no puede quedarse atrás. No se enumeran las funciones incorporadas del propio lenguaje: `size`, `has` y las conversiones `int`, `uint`, `double` y `bool`.

## Funciones {#decoder-functions-library}

Un decodificador indica en `envVersion` la versión de la biblioteca para la que se escribió; esta página describe la versión 1. Lo que hace una función no cambia nunca una vez publicada: una corrección o una variante llega con un nombre nuevo en una versión nueva, de modo que un decodificador guardado sigue dando las mismas respuestas. Las descripciones son el texto de la propia biblioteca y están en inglés en todos los idiomas. **Coste** es lo que añade la propia llamada, en unidades de coste; cada nombre, literal, operador y conversión de una expresión añade además aproximadamente 1. Una función que recibe `bytes` los lee de `frame` o de otra función que devuelve bytes.

### Enteros {#decoder-functions-integers}

| Función | Firma | Qué hace | Coste |
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

### Coma flotante {#decoder-functions-floats}

| Función | Firma | Qué hace | Coste |
| --- | --- | --- | --- |
| `f32be` | `f32be(bytes, int) -> double` | Reads an IEEE-754 32-bit big-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f32le` | `f32le(bytes, int) -> double` | Reads an IEEE-754 32-bit little-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f64be` | `f64be(bytes, int) -> double` | Reads an IEEE-754 64-bit big-endian float at a byte offset; a non-finite value is an error. | 1 |
| `f64le` | `f64le(bytes, int) -> double` | Reads an IEEE-754 64-bit little-endian float at a byte offset; a non-finite value is an error. | 1 |

### Bits {#decoder-functions-bits}

| Función | Firma | Qué hace | Coste |
| --- | --- | --- | --- |
| `bit` | `bit(bytes, int, int) -> int` | Reads one bit (0 or 1): bit 0 is the least significant bit of the byte at byteOff. | 1 |
| `bits` | `bits(bytes, int, int, int) -> int` | Reads an unsigned bit field of width 1..32 starting at bitOff, counted from the least significant bit of the byte at byteOff and continuing into the following bytes. | 1 |
| `sbits` | `sbits(bytes, int, int, int) -> int` | Like bits, sign-extended from the top bit of the field. | 1 |

### Decimal codificado en binario {#decoder-functions-bcd}

| Función | Firma | Qué hace | Coste |
| --- | --- | --- | --- |
| `bcd` | `bcd(bytes, int, int) -> int` | Reads len (1 to 8) bytes of packed binary-coded decimal, most significant digit first; a nibble above 9 is an error. | 1 per byte read (the len argument) |

### Bytes {#decoder-functions-bytes}

| Función | Firma | Qué hace | Coste |
| --- | --- | --- | --- |
| `slice` | `slice(bytes, int, int) -> bytes` | Copies len bytes starting at off; a range outside the value is an error. | 1 per byte copied, at least 1 |

### Tramas de texto {#decoder-functions-text}

| Función | Firma | Qué hace | Coste |
| --- | --- | --- | --- |
| `hex.decode` | `hex.decode(string) -> bytes` | Decodes hexadecimal text (even length, no separators) of at most 8192 bytes. | 1 per byte of the text, at least 1 |
| `base64.decode` | `base64.decode(string) -> bytes` | Decodes standard padded base64 text of at most 8192 bytes into at most 4096 bytes. | 1 per byte of the text, at least 1 |

### Códecs {#decoder-functions-codecs}

| Función | Firma | Qué hace | Coste |
| --- | --- | --- | --- |
| `lpp.decode` | `lpp.decode(bytes) -> map(string, double)` | Decodes a Cayenne LPP frame into channel-suffixed keys such as temperature_1; a truncated or unknown record is an error. | 1 per byte of the frame, at least 1 |

### Matemáticas {#decoder-functions-math}

| Función | Firma | Qué hace | Coste |
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

## Límites {#decoder-functions-limits}

Un decodificador que supera un límite se rechaza al guardarlo o publicarlo. Una entrada que supera un límite de entrada hace fallar esa decodificación.

| Límite | Valor |
| --- | --- |
| Trama sin procesar, en bytes | 4,096 |
| Entrada JSON (`json` o `decoded`), en bytes | 65,536 |
| Profundidad de anidamiento del JSON | 32 |
| El documento completo del decodificador, en bytes | 32,768 |
| Una expresión, en bytes | 2,048 |
| Enlaces `let` | 16 |
| Secciones | 8 |
| Lecturas que puede emitir una sección (filas escalares más el `maxKeys` de las filas de mapa) | 64 |
| Vectores guardados | 20 |
| Texto que recibe `hex.decode` o `base64.decode`, en bytes | 8,192 |
| Literales en una lista `in` | 16 |
| Nombre de un `let`, sección o vector, en bytes | 64 |
| Una clave de salida o un prefijo, en bytes | 128 |
| Coste estimado de una expresión | 10,000 |
| Coste estimado de un decodificador completo | 50,000 |
