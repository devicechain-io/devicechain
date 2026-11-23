---
section: whats-new
---

## en

- **A metric can have the data type `STRING`.** A device can report a state or a mode as text, for example `"heating"`, under a metric declared `STRING` on its profile. The reading is stored with the device's history, kept as its last-known value (`textValue` on `latestMeasurements` and on the event queries), and shown in the console's readings list. It is never averaged, summed or charted as a number: an aggregate over it is refused, and a detection rule does not see it as a number, even when the text looks like one (`"12"`). Declaring a `STRING` metric was refused before; it is accepted now, with a value of up to 4,096 bytes of valid UTF-8 and no NUL byte. A longer or invalid value is dropped on its own, counted as `declared_not_storable`, and shown on the device's page like any other dropped reading; the rest of the message is kept. Text for a key the profile does not declare is still dropped.
- **During a rolling upgrade**, a device-state pod that has not been upgraded yet stores a `STRING` value that looks like a number (`"12"`) as the number 12 until the next reading replaces it, and an event-processing pod that has not been upgraded yet lets a rule read such a value as a number. Declare `STRING` metrics after the upgrade has finished.

## es

- **Una métrica puede tener el tipo de dato `STRING`.** Un dispositivo puede notificar un estado o un modo como texto, por ejemplo `"heating"`, bajo una métrica declarada `STRING` en su perfil. La lectura se guarda en el historial del dispositivo, se conserva como su último valor conocido (`textValue` en `latestMeasurements` y en las consultas de eventos) y aparece en la lista de lecturas de la consola. Nunca se promedia, se suma ni se grafica como número: un agregado sobre ella se rechaza, y una regla de detección no la ve como un número, ni siquiera cuando el texto lo parece (`"12"`). Antes se rechazaba declarar una métrica `STRING`; ahora se acepta, con un valor de hasta 4.096 bytes de UTF-8 válido y sin bytes NUL. Un valor más largo o no válido se descarta por sí solo, se cuenta como `declared_not_storable` y aparece en la página del dispositivo como cualquier otra lectura descartada; el resto del mensaje se conserva. El texto de una clave que el perfil no declara se sigue descartando.
- **Durante una actualización progresiva**, un pod de device-state que aún no se ha actualizado guarda un valor `STRING` que parece un número (`"12"`) como el número 12 hasta que la siguiente lectura lo sustituya, y un pod de event-processing que aún no se ha actualizado deja que una regla lea ese valor como número. Declara las métricas `STRING` cuando la actualización haya terminado.

## zh-CN

- **指标可以使用 `STRING` 数据类型。** 设备可以把状态或模式作为文本上报（例如 `"heating"`），前提是其配置文件中该指标已声明为 `STRING`。该读数会与设备的历史一起存储，并作为其最近已知值保留（`latestMeasurements` 和事件查询中的 `textValue`），还会显示在控制台的读数列表中。它绝不会被求平均、求和或按数字绘图：对它做聚合会被拒绝，检测规则也不会把它当作数字，即使文本看起来像数字（`"12"`）。此前声明 `STRING` 指标会被拒绝，现在可以声明，值最多 4,096 字节，必须是有效的 UTF-8 且不含 NUL 字节。过长或无效的值会被单独丢弃，计入 `declared_not_storable`，并像其他被丢弃的读数一样显示在设备页面上，消息的其余部分会保留。配置文件未声明的键所带的文本仍会被丢弃。
- **滚动升级期间**，尚未升级的 device-state Pod 会把看起来像数字的 `STRING` 值（`"12"`）存为数字 12，直到下一个读数将其替换；尚未升级的 event-processing Pod 会让规则把这样的值当作数字读取。请在升级完成之后再声明 `STRING` 指标。
