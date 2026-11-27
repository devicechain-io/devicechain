---
section: whats-new
---

## en

- **A metric can have the data type `STRING`.** A device can report a state or a mode as text, for example `"heating"`, under a metric declared `STRING` on its profile. Declare it in the console's metric form, through the API or with `dcctl`. The reading is stored with the device's history, kept as its last-known value (`textValue` on `latestMeasurements` and on the event queries), and shown in the console's readings list. It is never averaged, summed or charted as a number: an aggregate over it is refused, and a detection rule does not see it as a number, even when the text looks like one (`"12"`). A rule that names a `STRING` metric never fires, and the console's rule authoring and the AI rule drafter do not offer one. A value is at most 4,096 bytes of valid UTF-8 with no NUL byte; a longer or invalid value is dropped on its own, counted as `declared_not_storable`, and shown on the device's page like any other dropped reading, and the rest of the message is kept. Text for a key the profile does not declare is still dropped. A `STRING` metric cannot have a formula. The analytics view of measurements does not expose the text column yet.

## es

- **Una métrica puede tener el tipo de dato `STRING`.** Un dispositivo puede notificar un estado o un modo como texto, por ejemplo `"heating"`, bajo una métrica declarada `STRING` en su perfil. Se declara en el formulario de métricas de la consola, mediante la API o con `dcctl`. La lectura se guarda en el historial del dispositivo, se conserva como su último valor conocido (`textValue` en `latestMeasurements` y en las consultas de eventos) y aparece en la lista de lecturas de la consola. Nunca se promedia, se suma ni se grafica como número: un agregado sobre ella se rechaza, y una regla de detección no la ve como un número, ni siquiera cuando el texto lo parece (`"12"`). Una regla que nombre una métrica `STRING` nunca se dispara, y la creación de reglas de la consola y el asistente de IA no la ofrecen. Un valor ocupa como máximo 4.096 bytes de UTF-8 válido sin bytes NUL; un valor más largo o no válido se descarta por sí solo, se cuenta como `declared_not_storable` y aparece en la página del dispositivo como cualquier otra lectura descartada, y el resto del mensaje se conserva. El texto de una clave que el perfil no declara se sigue descartando. Una métrica `STRING` no puede tener fórmula. La vista analítica de las mediciones todavía no expone la columna de texto.

## zh-CN

- **指标可以使用 `STRING` 数据类型。** 设备可以把状态或模式作为文本上报（例如 `"heating"`），前提是其配置文件中该指标已声明为 `STRING`。可在控制台的指标表单中、通过 API 或使用 `dcctl` 声明。该读数会与设备的历史一起存储，并作为其最近已知值保留（`latestMeasurements` 和事件查询中的 `textValue`），还会显示在控制台的读数列表中。它绝不会被求平均、求和或按数字绘图：对它做聚合会被拒绝，检测规则也不会把它当作数字，即使文本看起来像数字（`"12"`）。引用 `STRING` 指标的规则永远不会触发，控制台的规则编写和 AI 规则草拟也不会提供它。值最多 4,096 字节，必须是有效的 UTF-8 且不含 NUL 字节；过长或无效的值会被单独丢弃，计入 `declared_not_storable`，并像其他被丢弃的读数一样显示在设备页面上，消息的其余部分会保留。配置文件未声明的键所带的文本仍会被丢弃。`STRING` 指标不能带公式。测量值的分析视图暂未提供文本列。
