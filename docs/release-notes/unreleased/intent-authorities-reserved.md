---
section: whats-new
---

## en

- **Reserved intent authorities.** The new tenant authorities `intent:read` and `intent:write` are reserved for an upcoming feature, declared intents; nothing checks them yet, so granting them gives a role no access in this release. They are not added to any built-in role or to the access every member receives (tenant administrators holding `*` already satisfy both). Grant them to a custom role only **after the upgrade has finished rolling out**: a user-management pod that has not yet been upgraded refuses any role edit whose authority set contains a name it does not know, including an unrelated edit of a role that already holds one.

## es

- **Permisos reservados para intenciones.** Los nuevos permisos de inquilino `intent:read` e `intent:write` quedan reservados para una próxima funcionalidad, las intenciones declaradas; nada los comprueba todavía, así que concederlos no da ningún acceso a un rol en esta versión. No se añaden a ningún rol integrado ni al acceso que recibe cada miembro (los administradores de inquilino que tienen `*` ya satisfacen ambos). Concédalos a un rol personalizado solo **después de que haya finalizado el despliegue de la actualización**: un pod de user-management que aún no se haya actualizado rechaza cualquier edición de un rol cuyo conjunto de permisos contenga un nombre que no reconoce, incluso una edición no relacionada de un rol que ya tenga uno de ellos.

## zh-CN

- **预留的意图权限。** 新增的租户权限 `intent:read` 和 `intent:write` 为即将推出的功能（声明式意图）预留；目前尚无任何功能检查它们，因此在本版本中授予它们不会给角色带来任何访问权限。它们不会被添加到任何内置角色中，也不会加入每个成员默认获得的访问权限（持有 `*` 的租户管理员已满足这两项权限）。请仅在**升级的滚动部署全部完成后**才将其授予自定义角色：尚未升级的 user-management Pod 会拒绝权限集合中包含未知名称的任何角色编辑，即使该角色已持有其中之一，且此次编辑与其无关。
