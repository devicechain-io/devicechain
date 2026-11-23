---
section: whats-new
---

## en

- **`get_latest_measurements` returns a `STRING` metric's text.** Until now an assistant reading a device's latest values saw a `STRING` metric as an empty value. The text now comes back as `text` under the result's `untrusted` member, marked as device-reported, so an assistant treats it as data and never as instructions. A numeric reading is unchanged.

## es

- **`get_latest_measurements` devuelve el texto de una métrica `STRING`.** Hasta ahora, un asistente que leía los últimos valores de un dispositivo veía una métrica `STRING` como un valor vacío. Ahora el texto se devuelve como `text` dentro del miembro `untrusted` del resultado, marcado como notificado por el dispositivo, de modo que el asistente lo trata como datos y nunca como instrucciones. Una lectura numérica no cambia.

## zh-CN

- **`get_latest_measurements` 会返回 `STRING` 指标的文本。** 此前，助手读取设备最新值时会把 `STRING` 指标看作空值。现在文本会作为 `text` 返回在结果的 `untrusted` 成员下，并标记为设备上报，因此助手会把它当作数据，而不是指令。数值读数保持不变。
