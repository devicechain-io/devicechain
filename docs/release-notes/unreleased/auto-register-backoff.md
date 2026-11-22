---
section: whats-new
---

## en

- **A device whose auto-registration keeps failing no longer slows its source.** When an integration, Sparkplug or LwM2M source auto-registers a device and device-management refuses the create (for example, because the source names a device type that does not exist), that device now waits out a short backoff before it is tried again. The backoff starts at 10 seconds and doubles while the failures continue, up to 5 minutes. Messages it sends inside the window are dropped and counted with the other unregistered-device drops, without another call to device-management. The backoff is per device, so other devices on the same source are not held back, and it ends as soon as the device registers. An LwM2M device in the window is told to register again later.

## es

- **Un dispositivo cuyo registro automático sigue fallando ya no ralentiza su origen.** Cuando un origen de integración, Sparkplug o LwM2M registra automáticamente un dispositivo y device-management rechaza la creación (por ejemplo, porque el origen indica un tipo de dispositivo que no existe), ese dispositivo espera ahora un breve periodo de espera antes de volver a intentarlo. El periodo empieza en 10 segundos y se duplica mientras continúan los fallos, hasta 5 minutos. Los mensajes que envía dentro de ese periodo se descartan y se cuentan junto con los demás descartes de dispositivos no registrados, sin otra llamada a device-management. El periodo de espera es por dispositivo, de modo que los demás dispositivos del mismo origen no se ven afectados, y termina en cuanto el dispositivo se registra. A un dispositivo LwM2M dentro de ese periodo se le indica que vuelva a registrarse más tarde.

## zh-CN

- **自动注册持续失败的设备不再拖慢其数据源。** 当集成、Sparkplug 或 LwM2M 数据源自动注册设备，而 device-management 拒绝创建（例如数据源指定的设备类型不存在）时，该设备现在会先等待一段短暂的退避期再重试。退避期从 10 秒开始，在持续失败期间加倍，最长 5 分钟。设备在退避期内发送的消息会被丢弃，并与其他未注册设备的丢弃一起计数，不会再次调用 device-management。退避按设备计算，因此同一数据源上的其他设备不受影响；设备一旦注册成功，退避立即结束。处于退避期的 LwM2M 设备会被告知稍后重新注册。
