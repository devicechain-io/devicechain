// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package decoderfuncs

// pageText is every translated string of the page. The function names, signatures,
// descriptions and costs are the library's own and are not here.
type pageText struct {
	position, title, intro                    string
	libraryHeading, libraryIntro              string
	colFunction, colSignature, colDescription string
	colCost, colLimit, colValue               string
	limitsHeading, limitsIntro                string
	families                                  map[string]string
	limitLabels                               map[string]string
}

func (t pageText) familyHeading(id string) string {
	if h, ok := t.families[id]; ok {
		return h
	}
	return t.families[otherFamily]
}

var texts = map[string]pageText{
	"en": {
		position: "6",
		title:    "Decoder Function Reference",
		intro: "The functions of the [payload decoder](../guides/payload-decoders.md) library, and the limits a decoder is held to. " +
			"This page is generated from the function library in the platform, so it cannot fall behind it. " +
			"The language's own built-ins are not listed: `size`, `has` and the conversions `int`, `uint`, `double` and `bool`.",
		libraryHeading: "Functions",
		libraryIntro: "A decoder names the library version it was written for in `envVersion`; this page describes version %d. " +
			"What a function does never changes once it has shipped: a fix or a variant arrives under a new name in a new version, " +
			"so a saved decoder keeps giving the same answers. " +
			"The descriptions are the library's own text and are in English in every language. " +
			"**Cost** is what the call itself adds, in cost units; every name, literal, operator and conversion in an expression adds about 1 as well. " +
			"A function that takes `bytes` reads them from `frame` or from another function that returns bytes.",
		colFunction: "Function", colSignature: "Signature", colDescription: "What it does", colCost: "Cost",
		colLimit: "Limit", colValue: "Value",
		limitsHeading: "Limits",
		limitsIntro:   "A decoder that is over a limit is refused when you save or publish it. An input over an input limit fails that one decode.",
		families: map[string]string{
			"integers": "Integers", "floats": "Floating point", "bits": "Bits", "bcd": "Binary-coded decimal",
			"bytes": "Bytes", "text": "Text frames", "codecs": "Codecs", "math": "Maths", "other": "Other functions",
		},
		limitLabels: map[string]string{
			"frame":       "Raw frame, in bytes",
			"json":        "JSON input (`json` or `decoded`), in bytes",
			"depth":       "JSON nesting depth",
			"document":    "The whole decoder document, in bytes",
			"expression":  "One expression, in bytes",
			"lets":        "`let` bindings",
			"sections":    "Sections",
			"readings":    "Readings one section can emit (scalar rows plus the `maxKeys` of map rows)",
			"vectors":     "Saved vectors",
			"text":        "Text given to `hex.decode` or `base64.decode`, in bytes",
			"inlist":      "Literals in one `in` list",
			"name":        "A `let`, section or vector name, in bytes",
			"key":         "An output key or prefix, in bytes",
			"exprcost":    "Estimated cost of one expression",
			"decodercost": "Estimated cost of a whole decoder",
		},
	},
	"es": {
		position: "6",
		title:    "Referencia de funciones del decodificador",
		intro: "Las funciones de la biblioteca de los [decodificadores de payload](../guides/payload-decoders.md) y los límites a los que está sujeto un decodificador. " +
			"Esta página se genera a partir de la biblioteca de funciones de la plataforma, así que no puede quedarse atrás. " +
			"No se enumeran las funciones incorporadas del propio lenguaje: `size`, `has` y las conversiones `int`, `uint`, `double` y `bool`.",
		libraryHeading: "Funciones",
		libraryIntro: "Un decodificador indica en `envVersion` la versión de la biblioteca para la que se escribió; esta página describe la versión %d. " +
			"Lo que hace una función no cambia nunca una vez publicada: una corrección o una variante llega con un nombre nuevo en una versión nueva, " +
			"de modo que un decodificador guardado sigue dando las mismas respuestas. " +
			"Las descripciones son el texto de la propia biblioteca y están en inglés en todos los idiomas. " +
			"**Coste** es lo que añade la propia llamada, en unidades de coste; cada nombre, literal, operador y conversión de una expresión añade además aproximadamente 1. " +
			"Una función que recibe `bytes` los lee de `frame` o de otra función que devuelve bytes.",
		colFunction: "Función", colSignature: "Firma", colDescription: "Qué hace", colCost: "Coste",
		colLimit: "Límite", colValue: "Valor",
		limitsHeading: "Límites",
		limitsIntro:   "Un decodificador que supera un límite se rechaza al guardarlo o publicarlo. Una entrada que supera un límite de entrada hace fallar esa decodificación.",
		families: map[string]string{
			"integers": "Enteros", "floats": "Coma flotante", "bits": "Bits", "bcd": "Decimal codificado en binario",
			"bytes": "Bytes", "text": "Tramas de texto", "codecs": "Códecs", "math": "Matemáticas", "other": "Otras funciones",
		},
		limitLabels: map[string]string{
			"frame":       "Trama sin procesar, en bytes",
			"json":        "Entrada JSON (`json` o `decoded`), en bytes",
			"depth":       "Profundidad de anidamiento del JSON",
			"document":    "El documento completo del decodificador, en bytes",
			"expression":  "Una expresión, en bytes",
			"lets":        "Enlaces `let`",
			"sections":    "Secciones",
			"readings":    "Lecturas que puede emitir una sección (filas escalares más el `maxKeys` de las filas de mapa)",
			"vectors":     "Vectores guardados",
			"text":        "Texto que recibe `hex.decode` o `base64.decode`, en bytes",
			"inlist":      "Literales en una lista `in`",
			"name":        "Nombre de un `let`, sección o vector, en bytes",
			"key":         "Una clave de salida o un prefijo, en bytes",
			"exprcost":    "Coste estimado de una expresión",
			"decodercost": "Coste estimado de un decodificador completo",
		},
	},
	"zh-CN": {
		position: "6",
		title:    "解码器函数参考",
		intro: "[载荷解码器](../guides/payload-decoders.md)函数库中的函数，以及解码器所受的限制。" +
			"本页由平台中的函数库自动生成，因此不会落后于实现。" +
			"语言自身的内置函数不在此列：`size`、`has`，以及转换函数 `int`、`uint`、`double` 和 `bool`。",
		libraryHeading: "函数",
		libraryIntro: "解码器在 `envVersion` 中写明它所针对的函数库版本；本页描述的是版本 %d。" +
			"函数一旦发布，其行为就不会再改变：修复或变体会以新名称出现在新版本中，因此已保存的解码器始终给出相同的结果。" +
			"说明文字是函数库自带的文本，在所有语言中均为英文。" +
			"**成本**是调用本身增加的成本，单位为成本单位；表达式中的每个名称、字面量、运算符和类型转换还各增加约 1。" +
			"接受 `bytes` 的函数从 `frame` 或另一个返回字节的函数读取字节。",
		colFunction: "函数", colSignature: "签名", colDescription: "作用", colCost: "成本",
		colLimit: "限制", colValue: "取值",
		limitsHeading: "限制",
		limitsIntro:   "超出限制的解码器在保存或发布时会被拒绝。超出输入限制的输入只会使这一次解码失败。",
		families: map[string]string{
			"integers": "整数", "floats": "浮点数", "bits": "位", "bcd": "二进制编码十进制",
			"bytes": "字节", "text": "文本帧", "codecs": "编解码器", "math": "数学", "other": "其他函数",
		},
		limitLabels: map[string]string{
			"frame":       "原始帧，单位为字节",
			"json":        "JSON 输入（`json` 或 `decoded`），单位为字节",
			"depth":       "JSON 嵌套深度",
			"document":    "整个解码器文档，单位为字节",
			"expression":  "单个表达式，单位为字节",
			"lets":        "`let` 绑定",
			"sections":    "分段",
			"readings":    "单个分段可输出的读数（标量行加上映射行的 `maxKeys`）",
			"vectors":     "已保存的测试向量",
			"text":        "传给 `hex.decode` 或 `base64.decode` 的文本，单位为字节",
			"inlist":      "单个 `in` 列表中的字面量",
			"name":        "`let`、分段或测试向量的名称，单位为字节",
			"key":         "输出键或前缀，单位为字节",
			"exprcost":    "单个表达式的估算成本",
			"decodercost": "整个解码器的估算成本",
		},
	},
}
