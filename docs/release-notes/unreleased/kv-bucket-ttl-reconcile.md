---
section: whats-new
---

## en

- **A changed refresh-token lifetime now reaches a running instance.** Raising or lowering `auth.refreshTokenTtlSeconds` used to change only fresh installs: an existing refresh-token bucket kept expiring entries at the age it was created with. `user-management` now brings the bucket to the configured lifetime when it starts, and the broker administrator does the same for the buckets whose lifetime it declares. Lowering the lifetime expires stored entries older than the new value at that restart, so the sessions they back end and those people sign in again.

## es

- **Un cambio en la duración de los tokens de actualización llega ahora a una instancia en ejecución.** Subir o bajar `auth.refreshTokenTtlSeconds` solo cambiaba las instalaciones nuevas: un bucket de tokens de actualización existente seguía expirando las entradas con la antigüedad con la que se creó. `user-management` lleva ahora el bucket a la duración configurada al arrancar, y el administrador del broker hace lo mismo con los buckets cuya duración declara. Bajar la duración expira en ese reinicio las entradas guardadas más antiguas que el nuevo valor, de modo que las sesiones que respaldan terminan y esas personas vuelven a iniciar sesión.

## zh-CN

- **更改刷新令牌的有效期现在会作用于正在运行的实例。** 以前调高或调低 `auth.refreshTokenTtlSeconds` 只影响全新安装：现有的刷新令牌 bucket 仍按创建时的时长使条目过期。现在 `user-management` 在启动时会把该 bucket 调整为配置的有效期，代理管理员也会对其声明有效期的 bucket 做同样的调整。调低有效期会在该次重启时使早于新值的已存条目过期，因此由它们支撑的会话会结束，相关用户需要重新登录。
