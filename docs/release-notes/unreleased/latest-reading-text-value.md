---
section: whats-new
---

## en

- **A device's latest reading can hold text.** The latest-reading projection that `latestMeasurements` serves gains a nullable `textValue` field beside `value`: a reading of a STRING metric keeps its text there and leaves `value` null, and a numeric reading is unchanged. The device page shows the text. Every reading stored before the upgrade keeps its `value` and has a null `textValue`. The change adds one nullable column to the device-state store, with no backfill and no default.

## es

- **La última lectura de un dispositivo puede contener texto.** La proyección de última lectura que sirve `latestMeasurements` incorpora un campo `textValue`, que admite nulos, junto a `value`: una lectura de una métrica STRING guarda ahí su texto y deja `value` en nulo, y una lectura numérica no cambia. La página del dispositivo muestra el texto. Toda lectura guardada antes de la actualización conserva su `value` y tiene un `textValue` nulo. El cambio añade una columna que admite nulos al almacén de device-state, sin relleno de datos ni valor por defecto.

## zh-CN

- **设备的最新读数可以是文本。** `latestMeasurements` 所返回的最新读数投影在 `value` 旁新增了可为空的 `textValue` 字段：STRING 指标的读数把文本保存在其中，并将 `value` 置为 null，数值读数保持不变。设备页面会显示该文本。升级前存储的所有读数仍保留其 `value`，`textValue` 为 null。此变更为 device-state 存储新增一个可为空的列，不回填数据，也没有默认值。
