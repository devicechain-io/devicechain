---
section: whats-new
---

## en

- **Changing a device type's profile, or creating devices in bulk, no longer stalls while `device-management` cannot reach the broker.** These changes announce each affected device to the detection engine, one message per device. With the broker unreachable, each message waited about five seconds before failing, so a type with a thousand devices held the call for over an hour. The change now stops announcing devices at the first message the broker refuses and answers. The change itself is saved either way, and the detection engine picks up the devices it was not told about at its next comparison with `device-management`, which runs every five minutes.

## es

- **Cambiar el perfil de un tipo de dispositivo, o crear dispositivos en lote, ya no se bloquea mientras `device-management` no puede llegar al broker.** Estos cambios anuncian cada dispositivo afectado al motor de detección, un mensaje por dispositivo. Con el broker inaccesible, cada mensaje esperaba unos cinco segundos antes de fallar, así que un tipo con mil dispositivos retenía la llamada más de una hora. Ahora el cambio deja de anunciar dispositivos en el primer mensaje que el broker rechaza y responde. El cambio se guarda en cualquier caso, y el motor de detección incorpora los dispositivos que no se le anunciaron en su siguiente comparación con `device-management`, que se ejecuta cada cinco minutos.

## zh-CN

- **在 `device-management` 无法连接 broker 时，更改设备类型的配置文件或批量创建设备不再长时间挂起。** 这些变更会逐个向检测引擎通告每个受影响的设备，每个设备一条消息。broker 不可达时，每条消息要等待约五秒才失败，因此一个拥有一千台设备的类型会使调用挂起一个多小时。现在，变更在 broker 拒绝第一条消息时即停止通告并返回。变更本身无论如何都会保存，未被通告的设备会在检测引擎下一次与 `device-management` 比对时被纳入，该比对每五分钟运行一次。
