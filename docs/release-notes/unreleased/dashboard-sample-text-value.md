---
section: whats-new
---

## en

- **A dashboard sample can carry a text reading.** `MeasurementSample` in `@devicechain/dashboards` gains an optional `textValue`, and the live measurement stream now selects it, so a reading from a `STRING` metric reaches a widget as text instead of an unexplained empty value. It is absent on a numeric reading and on a server that predates `STRING` metrics. The built-in widgets still plot and aggregate numbers only and skip a sample whose `value` is null, so a text reading is never drawn as a gap or a zero.

## es

- **Una muestra de panel puede llevar una lectura de texto.** `MeasurementSample` en `@devicechain/dashboards` incorpora un `textValue` opcional, y el flujo de mediciones en vivo ahora lo selecciona, de modo que una lectura de una métrica `STRING` llega al widget como texto en lugar de un valor vacío sin explicación. Está ausente en una lectura numérica y en un servidor anterior a las métricas `STRING`. Los widgets integrados siguen graficando y agregando solo números y omiten una muestra cuyo `value` es nulo, así que una lectura de texto nunca se dibuja como un hueco ni como un cero.

## zh-CN

- **仪表板样本可以携带文本读数。** `@devicechain/dashboards` 中的 `MeasurementSample` 新增可选的 `textValue`，实时测量流现在会选取它，因此 `STRING` 指标的读数会作为文本到达小组件，而不是一个无法解释的空值。数值读数以及早于 `STRING` 指标的服务器上不会有该字段。内置小组件仍然只绘制和聚合数字，并会跳过 `value` 为空的样本，因此文本读数绝不会被画成缺口或零。
