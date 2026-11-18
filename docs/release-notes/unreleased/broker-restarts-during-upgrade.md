---
section: before-upgrade
---

## en

- **Upgrade note: the broker restarts.** The upgrade replaces the broker image, so every broker pod restarts. With `--mqtt-fail-closed` off (the default), a QoS 1 message the broker acknowledges while a stream changes leader can be lost. Turn it on before you upgrade (`dcctl upgrade --mqtt-fail-closed`) if devices must not lose acknowledged messages ([fail-closed MQTT](./observability.md#mqtt-fail-closed)).

## es

- **Nota de actualización: el bróker se reinicia.** La actualización sustituye la imagen del bróker, así que cada pod del bróker se reinicia. Con `--mqtt-fail-closed` desactivado (el valor predeterminado), un mensaje QoS 1 que el bróker confirma mientras un stream cambia de líder puede perderse. Actívalo antes de actualizar (`dcctl upgrade --mqtt-fail-closed`) si los dispositivos no deben perder mensajes confirmados ([MQTT con cierre seguro](./observability.md#mqtt-fail-closed)).

## zh-CN

- **升级说明：broker 会重启。** 此次升级会替换 broker 镜像，因此每个 broker pod 都会重启。在 `--mqtt-fail-closed` 关闭（默认）时，broker 在某个 stream 切换 leader 期间已确认的 QoS 1 消息可能丢失。如果设备不能丢失已确认的消息，请在升级前将其开启（`dcctl upgrade --mqtt-fail-closed`）（[fail-closed MQTT](./observability.md#mqtt-fail-closed)）。
