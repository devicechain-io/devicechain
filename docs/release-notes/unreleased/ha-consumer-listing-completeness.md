---
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
section: whats-new
---

## en

- **HA verification rejects incomplete consumer listings.** `dcctl ha verify` now refuses to judge a snapshot when a stream's listed consumer count differs from the broker's reported count. The error identifies the stream and both counts; rerun the command to collect a complete snapshot.

## es

- **La verificación de alta disponibilidad rechaza las listas incompletas de consumidores.** `dcctl ha verify` ahora se niega a evaluar una instantánea cuando la cantidad de consumidores enumerados de un flujo difiere de la indicada por el broker. El error identifica el flujo y ambas cantidades; vuelva a ejecutar el comando para recopilar una instantánea completa.

## zh-CN

- **高可用性验证会拒绝不完整的消费者列表。** 当某个流列出的消费者数量与消息代理报告的数量不一致时，`dcctl ha verify` 现在会拒绝评估该快照。错误信息会指出流名称和两个数量；请重新运行命令以收集完整快照。
