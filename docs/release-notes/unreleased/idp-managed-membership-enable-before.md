---
section: before-upgrade
---

## en

- **Enabling an IdP-managed membership is refused with `MANAGED_BY_IDP` too.** The operator mutation that enables a membership now returns `MANAGED_BY_IDP` while the membership's connection exists and still serves the tenant, as disable, remove and role changes already do. Each SSO sign-in sets that membership's enabled state from the person's groups, so an enable made by an operator would contradict the provider until the next sign-in disabled it again. To give the person access, add them to a group the provider maps to a role in the tenant; the membership is enabled at their next sign-in. Enabling an unmanaged membership is unchanged, and a managed membership whose connection is gone or no longer serves the tenant still cannot be re-enabled: remove it and add a membership instead.

## es

- **Habilitar una membresía gestionada por un IdP también se rechaza con `MANAGED_BY_IDP`.** La mutación del operador que habilita una membresía ahora devuelve `MANAGED_BY_IDP` mientras la conexión de la membresía exista y siga atendiendo al tenant, como ya ocurre al deshabilitarla, eliminarla o cambiar sus roles. Cada inicio de sesión SSO fija el estado habilitado de esa membresía a partir de los grupos de la persona, así que una habilitación hecha por un operador contradiría al proveedor hasta que el siguiente inicio de sesión la deshabilitara de nuevo. Para dar acceso a la persona, añádela a un grupo que el proveedor asigne a un rol en el tenant; la membresía se habilita en su siguiente inicio de sesión. Habilitar una membresía no gestionada no cambia, y una membresía gestionada cuya conexión ya no existe o ya no atiende al tenant sigue sin poder volver a habilitarse: elimínala y añade una membresía en su lugar.

## zh-CN

- **启用 IdP 管理的成员资格同样会以 `MANAGED_BY_IDP` 被拒绝。** 当成员资格的连接仍然存在且仍为该租户提供服务时，运维人员启用成员资格的操作现在会返回 `MANAGED_BY_IDP`，与禁用、移除或更改角色的操作一致。每次 SSO 登录都会根据此人的组设置该成员资格的启用状态，因此运维人员所做的启用会与提供商相矛盾，直到下次登录再次将其禁用。要为此人授予访问权限，请将其加入提供商映射到该租户中某个角色的组；该成员资格会在其下次登录时启用。启用非托管成员资格的行为不变；连接已删除或不再为该租户提供服务的托管成员资格仍然无法重新启用：请将其移除，然后添加新的成员资格。
