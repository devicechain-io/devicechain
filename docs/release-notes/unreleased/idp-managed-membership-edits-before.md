---
section: before-upgrade
---

## en

- **IdP-managed memberships:** operator mutations that disable, remove or change the roles of a membership now return `MANAGED_BY_IDP` while its connection exists and still serves the tenant; SSO sign-in would otherwise undo those edits. To revoke access, disable the identity, remove the person from the provider's groups, or stop the connection serving the tenant. Unmanaged memberships and managed memberships whose connection is gone or no longer serves the tenant remain editable; enabling keeps its existing checks.

## es

- **Membresías gestionadas por un IdP:** las mutaciones del operador que deshabilitan, eliminan o cambian los roles de una membresía ahora devuelven `MANAGED_BY_IDP` mientras su conexión exista y siga atendiendo al tenant; de lo contrario, el siguiente inicio de sesión SSO desharía esos cambios. Para revocar el acceso, deshabilita la identidad, retira a la persona de los grupos del proveedor o deja de atender al tenant con esa conexión. Las membresías no gestionadas y aquellas cuya conexión ya no existe o ya no atiende al tenant siguen siendo editables; la habilitación conserva sus comprobaciones actuales.

## zh-CN

- **IdP 管理的成员资格：** 当连接仍然存在且仍为该租户提供服务时，运维人员禁用、移除成员资格或更改其角色的操作现在会返回 `MANAGED_BY_IDP`；否则，下次 SSO 登录会撤销这些更改。要撤销访问权限，请禁用该身份、将此人从提供商的组中移除，或停止该连接为该租户提供服务。非托管成员资格，以及连接已删除或不再为该租户提供服务的托管成员资格，仍可编辑；启用操作保留现有检查。
