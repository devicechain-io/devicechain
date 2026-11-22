---
section: before-upgrade
---

## en

- **Deprecated: the `InstanceConfiguration` custom resource, and the `spec.grafanaSSO` and `spec.compact` fields of an `Instance` declaration.** Nothing changes for an instance in this release, and a later major release removes them. `InstanceConfiguration` (`dcic`) is unused, because each service reads the instance configuration from its mounted Secret, and the API server now warns when one is read or written. `spec.grafanaSSO` has no effect: Grafana's login is configured for the whole cluster. `spec.compact` is replaced by `spec.size: compact`, which dcctl already writes beside it.

## es

- **Obsoletos: el recurso personalizado `InstanceConfiguration` y los campos `spec.grafanaSSO` y `spec.compact` de una declaración `Instance`.** En esta versión nada cambia para una instancia, y una versión mayor posterior los elimina. `InstanceConfiguration` (`dcic`) no se usa, porque cada servicio lee la configuración de la instancia del Secret montado, y el servidor de API avisa ahora cuando se lee o se escribe uno. `spec.grafanaSSO` no tiene efecto: el inicio de sesión de Grafana se configura para todo el clúster. `spec.compact` se sustituye por `spec.size: compact`, que dcctl ya escribe junto a él.

## zh-CN

- **已弃用：`InstanceConfiguration` 自定义资源，以及 `Instance` 声明中的 `spec.grafanaSSO` 和 `spec.compact` 字段。** 本版本中实例不受任何影响，后续的主版本将删除它们。`InstanceConfiguration`（`dcic`）未被使用，因为每个服务都从挂载的 Secret 读取实例配置；现在读取或写入该资源时 API 服务器会发出警告。`spec.grafanaSSO` 不起作用：Grafana 的登录针对整个集群配置。`spec.compact` 由 `spec.size: compact` 取代，dcctl 已在其旁写入后者。
