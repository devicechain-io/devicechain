---
section: whats-new
---

## en

- **`dcctl broker facts` lists the deletion facts the platform announced.** When an entity is deleted, `device-management` announces the deletion on the broker so that every service holding a reference to it can remove that reference. `dcctl broker facts <instance>` reads those announcements from the broker as the broker administrator. It shows when each one was stored, the tenant, the entity's type, id and token, the deletion time and the message id, and takes `--tenant`, `--token` and `--since` filters. It answers "was the deletion of X announced, and when?" without access to the broker's disks. The broker administrator login gains read access to the deletion announcements and nothing more: the new access adds no right to delete, purge or publish a message. An instance gains the access when it is upgraded with this release's `dcctl`.

## es

- **`dcctl broker facts` enumera los hechos de eliminación que anunció la plataforma.** Cuando se elimina una entidad, `device-management` anuncia la eliminación en el broker para que cada servicio que guarda una referencia a ella pueda quitarla. `dcctl broker facts <instance>` lee esos anuncios del broker como el administrador del broker. Muestra cuándo se almacenó cada uno, el tenant, el tipo, el id y el token de la entidad, la hora de eliminación y el id del mensaje, y admite los filtros `--tenant`, `--token` y `--since`. Responde a "¿se anunció la eliminación de X, y cuándo?" sin acceso a los discos del broker. El inicio de sesión del administrador del broker obtiene acceso de lectura a los anuncios de eliminación y nada más: el nuevo acceso no añade ningún derecho a eliminar, purgar ni publicar mensajes. Una instancia obtiene el acceso cuando se actualiza con el `dcctl` de esta versión.

## zh-CN

- **`dcctl broker facts` 列出平台已发布的删除事实。** 删除实体时，`device-management` 会在 broker 上发布删除通知，以便每个持有该实体引用的服务都能移除该引用。`dcctl broker facts <instance>` 以 broker 管理员身份从 broker 读取这些通知。它显示每条通知的存储时间、租户、实体的类型、id 和 token、删除时间以及消息 id，并支持 `--tenant`、`--token` 和 `--since` 过滤条件。无需访问 broker 的磁盘，即可回答"X 的删除是否已发布，何时发布？"。broker 管理员登录仅获得对删除通知的读取权限：新增的访问权限不包含删除、清除或发布消息的权利。实例在使用本版本的 `dcctl` 升级后获得该访问权限。
