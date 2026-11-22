---
section: before-upgrade
---

## en

- `dcctl bootstrap` and `dcctl upgrade` now read InstanceOperations before acting. The identity running either command needs `list` on `instanceoperations.core.devicechain.io`; cluster-admin already has this permission. An absent resource definition is treated as no requests.

## es

- `dcctl bootstrap` y `dcctl upgrade` ahora leen InstanceOperations antes de actuar. La identidad que ejecuta cualquiera de los comandos necesita `list` sobre `instanceoperations.core.devicechain.io`; cluster-admin ya tiene este permiso. Si la definición del recurso no existe, se considera que no hay solicitudes.

## zh-CN

- `dcctl bootstrap` 和 `dcctl upgrade` 现在会在操作前读取 InstanceOperation。运行这两个命令的身份需要对 `instanceoperations.core.devicechain.io` 拥有 `list` 权限；cluster-admin 已有此权限。资源定义不存在时，视为没有请求。
