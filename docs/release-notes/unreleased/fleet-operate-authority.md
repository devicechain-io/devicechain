---
section: before-upgrade
---

## en

- **Reserved fleet authority.** The new tenant authority `fleet:operate` is reserved for fleet operations in a later release; no API checks it yet. It is not added to any built-in role (tenant administrators holding `*` already satisfy it). Grant it to a custom role only **after the upgrade has finished rolling out**: a user-management pod that has not yet been upgraded refuses any role edit whose authority set contains a name it does not know, including an unrelated edit of a role that already holds it.

## es

- **Permiso reservado para operaciones de flota.** El nuevo permiso de inquilino `fleet:operate` queda reservado para operaciones de flota en una versión posterior; ninguna API lo comprueba todavía. No se añade a ningún rol integrado (los administradores de inquilino que tienen `*` ya lo satisfacen). Concédalo a un rol personalizado solo **después de que haya finalizado el despliegue de la actualización**: un pod de user-management que aún no se haya actualizado rechaza cualquier edición de un rol cuyo conjunto de permisos contenga un nombre que no reconoce, incluso una edición no relacionada de un rol que ya lo tenga.

## zh-CN

- **预留的设备群操作权限。** 新增的租户权限 `fleet:operate` 为后续版本中的设备群操作预留；本版本尚无任何 API 检查它。它不会被添加到任何内置角色中（持有 `*` 的租户管理员已满足该权限）。请仅在**升级的滚动部署全部完成后**才将其授予自定义角色：尚未升级的 user-management Pod 会拒绝权限集合中包含未知名称的任何角色编辑，即使该角色已持有此权限，且此次编辑与该权限无关。
