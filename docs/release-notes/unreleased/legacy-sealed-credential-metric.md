---
section: whats-new
---

## en

- **Use of the legacy text seal is counted.** Two process counters, `devicechain_credential_legacy_text_seal_reads_total` and `devicechain_credential_legacy_text_seal_writes_total`, count use of the legacy text form of sealed device credentials on the event wire. Writes count text seals encoded when `infrastructure.messaging.wireFormat` is `legacy`; reads count every conversion of a text seal found in a message, including malformed or empty values and redeliveries. Binary-only traffic increments neither. Both start at zero and carry no labels, so they expose no credential or tenant data. The legacy text form is still read and written exactly as before; nothing is removed. No configuration or migration is needed; during a rolling upgrade only updated pods expose the counters.

## es

- **Se cuenta el uso del sello de texto heredado.** Dos contadores por proceso, `devicechain_credential_legacy_text_seal_reads_total` y `devicechain_credential_legacy_text_seal_writes_total`, cuentan el uso de la forma de texto heredada de las credenciales de dispositivo selladas en los mensajes de eventos. Las escrituras cuentan los sellos de texto codificados cuando `infrastructure.messaging.wireFormat` es `legacy`; las lecturas cuentan cada conversión de un sello de texto encontrado en un mensaje, incluidos valores malformados o vacíos y reentregas. El tráfico exclusivamente binario no incrementa ninguno. Ambos empiezan en cero y no tienen etiquetas, por lo que no exponen credenciales ni inquilinos. La forma de texto heredada se sigue leyendo y escribiendo exactamente igual; no se retira nada. No se requiere cambiar la configuración ni migrar datos; durante una actualización gradual solo los pods actualizados exponen los contadores.

## zh-CN

- **旧版文本密封的使用会被计数。** 新增两个进程计数器 `devicechain_credential_legacy_text_seal_reads_total` 和 `devicechain_credential_legacy_text_seal_writes_total`，统计事件消息中设备凭据旧版文本密封形式的使用情况。写入计数统计 `infrastructure.messaging.wireFormat` 为 `legacy` 时编码的文本密封；读取计数统计从消息中读到的每次文本密封转换，包括格式错误、空值和重新投递。纯二进制流量不会增加这两个计数器。两者均从零开始且不带标签，因此不暴露凭据或租户数据。旧版文本形式的读取和写入与之前完全相同，没有移除任何内容。无需修改配置或迁移数据；滚动升级期间仅更新后的 Pod 提供这些计数器。
