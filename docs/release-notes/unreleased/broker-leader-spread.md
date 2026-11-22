---
section: whats-new
---

## en

- **Broker leadership is spread at install, and you can see where it sits.** `dcctl bootstrap` now ends by evening out which message broker server leads which stream, the same best-effort step `dcctl upgrade` runs after it restarts the broker. It never fails the bootstrap. The step, `dcctl upgrade` and `dcctl broker rebalance` now also keep the resolved-events stream off the server that leads the broker's MQTT message store, and move the resolved-events consumers off a server that leads more of the per-event streams than one of their replicas does. The device-ingest streams and the MQTT streams are still never moved. The new `dcctl ha leaders` command reports, without moving anything, which server leads each per-event stream and each resolved-events consumer, and whether the rebalance could separate them further (`PASS`, `FAIL` or `UNSETTLED`, with `--json` and `--expect-fail`). An older `dcctl` does not know about the MQTT message store and can move resolved-events onto its server, so run the rebalance with this release's `dcctl`.

## es

- **El liderazgo del broker se reparte en la instalación, y puede ver dónde está.** `dcctl bootstrap` termina ahora equilibrando qué servidor del broker de mensajes lidera cada stream, el mismo paso de mejor esfuerzo que `dcctl upgrade` ejecuta después de reiniciar el broker. Nunca hace fallar el bootstrap. Ese paso, `dcctl upgrade` y `dcctl broker rebalance` mantienen además el stream de eventos resueltos fuera del servidor que lidera el almacén de mensajes MQTT del broker, y mueven los consumidores de eventos resueltos fuera de un servidor que lidera más streams por evento que alguna de sus réplicas. Los streams de ingesta de dispositivos y los streams MQTT siguen sin moverse nunca. El nuevo comando `dcctl ha leaders` informa, sin mover nada, de qué servidor lidera cada stream por evento y cada consumidor de eventos resueltos, y de si el reequilibrado podría separarlos más (`PASS`, `FAIL` o `UNSETTLED`, con `--json` y `--expect-fail`). Un `dcctl` anterior no conoce el almacén de mensajes MQTT y puede mover los eventos resueltos a su servidor, así que ejecute el reequilibrado con el `dcctl` de esta versión.

## zh-CN

- **安装时即分散代理领导权，并且可以查看其位置。** `dcctl bootstrap` 现在最后会均衡消息代理各服务器所领导的流，这与 `dcctl upgrade` 在重启代理后执行的尽力而为步骤相同，且绝不会导致 bootstrap 失败。该步骤、`dcctl upgrade` 和 `dcctl broker rebalance` 现在还会让已解析事件流避开领导代理 MQTT 消息存储的服务器，并在某个服务器领导的逐事件流多于其副本所在服务器时，将已解析事件的消费者移走。设备接入流和 MQTT 流仍然永远不会被移动。新命令 `dcctl ha leaders` 在不移动任何内容的情况下，报告每个逐事件流和每个已解析事件消费者由哪个服务器领导，以及重新均衡能否进一步分离它们（`PASS`、`FAIL` 或 `UNSETTLED`，支持 `--json` 和 `--expect-fail`）。旧版 `dcctl` 不了解 MQTT 消息存储，可能会把已解析事件流移到该服务器上，因此请使用本版本的 `dcctl` 执行重新均衡。
