---
section: whats-new
---

## en

- **Rejecting a single sign-on link request is now audited.** Rejecting a pending request with `rejectSsoLinkRequest` records an `identity_link_rejected` row in the authentication audit log, the way approving one records `identity_linked`. The row names the administrator who rejected it and the request and connection; it never records the email or subject the provider asserted. Rejecting a request that is already gone, or being refused, records nothing.

## es

- **El rechazo de una solicitud de vinculación de inicio de sesión único ahora se audita.** Al rechazar una solicitud pendiente con `rejectSsoLinkRequest` se registra una fila `identity_link_rejected` en el registro de auditoría de autenticación, igual que la aprobación registra `identity_linked`. La fila indica el administrador que la rechazó, la solicitud y la conexión; nunca registra el correo ni el subject que declaró el proveedor. Rechazar una solicitud que ya no existe, o que se deniega, no registra nada.

## zh-CN

- **拒绝单点登录链接请求现在会被审计。** 使用 `rejectSsoLinkRequest` 拒绝待处理的请求时，认证审计日志会记录一条 `identity_link_rejected`，与批准请求时记录 `identity_linked` 的方式一致。该记录包含执行拒绝的管理员、请求和连接，不会记录提供方声明的邮箱或 subject。拒绝已不存在的请求，或被拒绝执行的操作，不会产生记录。
