---
section: before-upgrade
---

## en

- **Shares left behind by earlier deletions are removed after the upgrade.** On its first sweep, 15 minutes after its first start after the upgrade, `dashboard-management` removes the dashboard shares that name a customer or access group deleted earlier, including deletions from before the upgrade. The sweep needs the service secret and the `device-management` endpoint configured; without them it does not start, and a warning is logged at startup. See [Shares naming a deleted customer or group are removed](#v170-deleted-anchor-shares).

## es

- **Las comparticiones que dejaron eliminaciones anteriores se eliminan tras la actualización.** En su primer barrido, 15 minutos después de su primer arranque tras la actualización, `dashboard-management` elimina las comparticiones de dashboards que nombran un cliente o grupo de acceso eliminado antes, incluidas las eliminaciones anteriores a la actualización. El barrido necesita el secreto de servicio y el endpoint de `device-management` configurados; sin ellos no se inicia, y se registra una advertencia al arrancar. Consulte [Se eliminan las comparticiones que nombran un cliente o grupo eliminado](#v170-deleted-anchor-shares).

## zh-CN

- **升级后会清除此前删除遗留的共享。** 升级后首次启动 15 分钟后的第一次清理中，`dashboard-management` 会删除指向此前已删除的客户或访问分组的仪表板共享，包括升级前的删除。该清理需要配置服务密钥和 `device-management` 端点；未配置时清理不会启动，并在启动时记录警告。参见[指向已删除客户或分组的共享会被清除](#v170-deleted-anchor-shares)。
