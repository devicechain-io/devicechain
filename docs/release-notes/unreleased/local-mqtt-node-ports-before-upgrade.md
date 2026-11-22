---
section: before-upgrade
---

## en

- **Upgrade a second or third local instance only with this release's `dcctl` or a later one.** An instance that this release gave MQTT node port 31884 or 31885 keeps it on every later upgrade. An older `dcctl` knows only 31883, and its upgrade removes that instance's MQTT port, leaving the broker reachable inside the cluster only.

## es

- **Actualice una segunda o tercera instancia local solo con el `dcctl` de esta versión o de una posterior.** Una instancia a la que esta versión asignó el puerto de nodo MQTT 31884 o 31885 lo conserva en todas las actualizaciones posteriores. Un `dcctl` anterior solo conoce el 31883, y su actualización elimina el puerto MQTT de esa instancia, de modo que el broker queda accesible solo dentro del clúster.

## zh-CN

- **第二个或第三个本地实例只能用本版本或更高版本的 `dcctl` 升级。** 本版本分配了 MQTT 节点端口 31884 或 31885 的实例，在之后的每次升级中都会保留该端口。旧版 `dcctl` 只认识 31883，用它升级会移除该实例的 MQTT 端口，使代理只能在集群内访问。
