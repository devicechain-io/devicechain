---
section: whats-new
---

## en

- **A second and a third local instance get their own MQTT port.** On a local kind cluster, `dcctl bootstrap` now gives each new instance the first free MQTT node port the cluster publishes. Previously only the first instance could be reached from the host and the others were in-cluster only. A cluster created by this release publishes three: hosts 1883, 1884 and 1885. A cluster created earlier publishes 1883 alone and behaves as before. The bootstrap summary prints the address (`MQTT: ssl://localhost:<port>`). The port is recorded with the instance, so `dcctl sim create` uses it. An upgrade keeps the port an instance already has and never gives it a new one. `dcctl preflight` now also checks that host ports 1884 and 1885 are free before it creates a cluster.

## es

- **Una segunda y una tercera instancia local reciben su propio puerto MQTT.** En un clúster kind local, `dcctl bootstrap` asigna ahora a cada instancia nueva el primer puerto de nodo MQTT libre que publica el clúster. Antes solo la primera instancia era accesible desde el host y las demás quedaban accesibles solo dentro del clúster. Un clúster creado con esta versión publica tres: los puertos de host 1883, 1884 y 1885. Un clúster creado antes publica solo el 1883 y se comporta como hasta ahora. El resumen del bootstrap muestra la dirección (`MQTT: ssl://localhost:<puerto>`). El puerto se registra con la instancia, de modo que `dcctl sim create` lo utiliza. Una actualización conserva el puerto que ya tiene la instancia y nunca le asigna uno nuevo. `dcctl preflight` comprueba ahora también que los puertos de host 1884 y 1885 estén libres antes de crear un clúster.

## zh-CN

- **第二个和第三个本地实例也有各自的 MQTT 端口。** 在本地 kind 集群上，`dcctl bootstrap` 现在会为每个新实例分配集群发布的第一个空闲 MQTT 节点端口。此前只有第一个实例能从主机访问，其余实例只能在集群内访问。本版本创建的集群发布三个端口：主机端口 1883、1884 和 1885。较早创建的集群只发布 1883，行为与以前相同。bootstrap 摘要会打印地址（`MQTT: ssl://localhost:<端口>`）。该端口会记录在实例中，因此 `dcctl sim create` 会使用它。升级会保留实例已有的端口，绝不会分配新端口。`dcctl preflight` 现在还会在创建集群前检查主机端口 1884 和 1885 是否空闲。
