---
section: whats-new
---

## en

- **`dcctl sim create` no longer points a sim at another instance's broker.** On a local cluster where every published MQTT port belongs to other instances, an instance is reachable inside the cluster only. `dcctl sim create` for such an instance used to fall back to the cluster's first MQTT port, which is another instance's broker. It now refuses and tells you to pass `--mqtt-broker`. The instance record now says explicitly when an instance has no MQTT port. For an instance recorded before this release, dcctl reads the instance's own Services instead, so an instance that does hold the first port still resolves to it.

## es

- **`dcctl sim create` ya no apunta una simulación al broker de otra instancia.** En un clúster local donde todos los puertos MQTT publicados pertenecen a otras instancias, una instancia solo es accesible dentro del clúster. `dcctl sim create` para esa instancia recurría antes al primer puerto MQTT del clúster, que es el broker de otra instancia. Ahora se niega e indica que se pase `--mqtt-broker`. El registro de la instancia indica ahora explícitamente cuándo una instancia no tiene puerto MQTT. Para una instancia registrada antes de esta versión, dcctl consulta en su lugar los Services de la propia instancia, de modo que una instancia que sí tiene el primer puerto sigue resolviéndose a él.

## zh-CN

- **`dcctl sim create` 不再把模拟器指向另一个实例的代理。** 在本地集群上，如果所有已发布的 MQTT 端口都属于其他实例，该实例只能在集群内访问。此前对这类实例执行 `dcctl sim create` 会退回到集群的第一个 MQTT 端口，而那是另一个实例的代理。现在它会拒绝并提示传入 `--mqtt-broker`。实例记录现在会明确写明实例没有 MQTT 端口。对于本版本之前记录的实例，dcctl 会改为读取该实例自己的 Service，因此确实持有第一个端口的实例仍会解析到该端口。
