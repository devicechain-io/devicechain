---
section: whats-new
---

## en

#### Shares naming a deleted customer or group are removed {#v170-deleted-anchor-shares}

- When a customer or access group is deleted, `dashboard-management` removes the dashboard shares that name it. If that cleanup keeps failing, the announcement is now kept on the dead-letter stream instead of being discarded.
- `dashboard-management` also checks every 15 minutes, with the instance's service credentials, that each share's customer or group still exists, and removes shares left behind by earlier deletions, including ones from before the upgrade. It never removes anything when that lookup fails. Without the service secret or the `device-management` endpoint configured, the check is not started and a warning is logged at startup.

## es

#### Se eliminan las comparticiones que nombran un cliente o grupo eliminado {#v170-deleted-anchor-shares}

- Al eliminar un cliente o un grupo de acceso, `dashboard-management` elimina las comparticiones de dashboards que lo nombran. Si esa limpieza falla de forma persistente, el aviso se conserva ahora en el flujo de mensajes fallidos en lugar de descartarse.
- `dashboard-management` también comprueba cada 15 minutos, con las credenciales de servicio de la instancia, que el cliente o grupo de cada compartición sigue existiendo, y elimina las comparticiones que dejaron eliminaciones anteriores, incluso de antes de la actualización. Nunca elimina nada si esa consulta falla. Si el secreto de servicio o el endpoint de `device-management` no están configurados, la comprobación no se inicia y se registra una advertencia al arrancar.

## zh-CN

#### 指向已删除客户或分组的共享会被清除 {#v170-deleted-anchor-shares}

- 删除客户或访问分组时，`dashboard-management` 会删除指向它的仪表板共享。如果该清理持续失败，相应的通知现在会保留在死信流中，而不会被丢弃。
- `dashboard-management` 还会每 15 分钟使用实例的服务凭据检查每个共享的客户或分组是否仍然存在，并清除此前（包括升级之前）的删除遗留的共享。查询失败时不会删除任何内容。如果未配置服务密钥或 `device-management` 端点，则不会启动该检查，并在启动时记录警告。
