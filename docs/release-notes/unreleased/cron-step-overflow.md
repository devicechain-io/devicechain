---
section: whats-new
---

## en

- **Campaign cron steps.** A step far larger than its field's range, such as `40-59/9223372036854775803`, no longer overflows into minutes outside the range it names. It selects only the range's first value, as any step larger than the range already did. Every expression accepted before is still accepted.

## es

- **Pasos cron de las campañas.** Un paso mucho mayor que el rango de su campo, como `40-59/9223372036854775803`, ya no se desborda hacia minutos fuera del rango que indica. Selecciona solo el primer valor del rango, como ya hacía cualquier paso mayor que el rango. Toda expresión aceptada antes se sigue aceptando.

## zh-CN

- **活动的 cron 步长。** 远大于字段范围的步长（例如 `40-59/9223372036854775803`）不再溢出到其指定范围之外的分钟。它只选中范围的第一个值，与此前任何大于范围的步长一致。此前接受的所有表达式仍然被接受。
