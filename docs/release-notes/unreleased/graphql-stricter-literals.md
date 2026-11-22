---
section: whats-new
---

## en

- **GraphQL requests are parsed more strictly.** The GraphQL library is updated, and number literals that were previously accepted are now refused as syntax errors: a leading zero (`01`), a trailing or leading decimal point (`1.`, `.5`), an integer out of range, and a minus sign before something that is not a number. A document is also limited to 100,000 field-overlap comparisons during validation. Well-formed requests are unaffected.

## es

- **Las peticiones GraphQL se analizan de forma más estricta.** Se actualiza la biblioteca GraphQL, y literales numéricos que antes se aceptaban ahora se rechazan como errores de sintaxis: un cero inicial (`01`), un punto decimal al final o al principio (`1.`, `.5`), un entero fuera de rango y un signo menos delante de algo que no es un número. Además, un documento queda limitado a 100.000 comparaciones de solapamiento de campos durante la validación. Las peticiones bien formadas no se ven afectadas.

## zh-CN

- **GraphQL 请求的解析更加严格。** GraphQL 库已更新，以前被接受的数字字面量现在会作为语法错误被拒绝：前导零（`01`）、末尾或开头的小数点（`1.`、`.5`）、超出范围的整数，以及位于非数字之前的减号。此外，每个文档在验证期间的字段重叠比较次数上限为 100,000 次。格式正确的请求不受影响。
