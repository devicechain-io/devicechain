---
section: whats-new
---

## en

- **List totals saturate instead of wrapping.** A list whose matching rows exceed 2,147,483,647 now reports `totalRecords` as 2,147,483,647 instead of a wrapped, possibly negative number, and `pageStart`/`pageEnd` no longer overflow on very large page numbers.

## es

- **Los totales de las listas se saturan en lugar de desbordarse.** Una lista con más de 2.147.483.647 filas coincidentes ahora indica 2.147.483.647 en `totalRecords` en lugar de un número desbordado, posiblemente negativo, y `pageStart`/`pageEnd` ya no se desbordan con números de página muy grandes.

## zh-CN

- **列表总数现在会饱和到上限，而不再溢出回绕。** 当列表中匹配的行数超过 2,147,483,647 时，`totalRecords` 现在报告为 2,147,483,647，而不是回绕后可能为负数的值；页码非常大时，`pageStart`/`pageEnd` 也不再溢出。
