---
section: whats-new
---

## en

- **The JetStream reservation check judges one broker server.** With [shard groups](#v160-nats-shard-groups) on, `dcctl` now counts what a single server can be asked to hold: every unsharded stream, plus each sharded stream once per group that can share a server. The chart pins each group to its own `dc-nats` server, so a three-group install no longer has three copies summed against one server's volume; a `perf-20k` install with three groups fits the preset's 40Gi volume. The check before the infrastructure apply also uses a `nats_jetstream_storage` you state in `terraform.tfvars`, an `*.auto.tfvars` file or `TF_VAR_nats_jetstream_storage`, instead of the preset's size. A volume too small for one server's share is still refused before anything is created when its size is a literal. A size given as an expression or a reference cannot be read before the apply, so `dcctl` prints a notice, runs the infrastructure apply, and checks the provisioned volume after it, before the chart is installed.

## es

- **La comprobación de la reserva de JetStream evalúa un único servidor del broker.** Con los [grupos de fragmentación](#v160-nats-shard-groups) activados, `dcctl` cuenta ahora lo que se le puede pedir a un solo servidor: cada stream sin fragmentar, más cada stream fragmentado una vez por cada grupo que pueda compartir servidor. El chart fija cada grupo a su propio servidor `dc-nats`, de modo que una instalación de tres grupos ya no suma tres copias contra el volumen de un único servidor; una instalación `perf-20k` con tres grupos cabe en el volumen de 40Gi del preset. La comprobación previa a la aplicación de la infraestructura usa además el `nats_jetstream_storage` que usted declare en `terraform.tfvars`, en un archivo `*.auto.tfvars` o en `TF_VAR_nats_jetstream_storage`, en lugar del tamaño del preset. Un volumen demasiado pequeño para la parte de un servidor se sigue rechazando antes de crear nada cuando su tamaño es un literal. Un tamaño dado como expresión o referencia no se puede leer antes de la aplicación, así que `dcctl` muestra un aviso, aplica la infraestructura y comprueba el volumen aprovisionado después, antes de instalar el chart.

## zh-CN

- **JetStream 预留检查按单个代理服务器评估。** 启用[分片组](#v160-nats-shard-groups)后，`dcctl` 现在计算单个服务器可能需要承载的量：所有未分片的流，加上每个分片流中可能共用同一服务器的各组各计一次。chart 将每个组固定到各自的 `dc-nats` 服务器，因此三组安装不再把三份副本累加后与单个服务器的卷比较；启用三组的 `perf-20k` 安装可以放入该预设的 40Gi 卷。基础设施应用之前的检查现在也会使用您在 `terraform.tfvars`、`*.auto.tfvars` 文件或 `TF_VAR_nats_jetstream_storage` 中声明的 `nats_jetstream_storage`，而不是预设的大小。当大小为字面值时，对单个服务器的份额而言过小的卷，仍会在创建任何内容之前被拒绝。以表达式或引用给出的大小在应用之前无法读取，因此 `dcctl` 会打印一条提示，执行基础设施应用，然后在安装 chart 之前检查实际配置的卷。
