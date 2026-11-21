---
section: whats-new
---

## en

- **In-cluster executor (Preview).** `dcctl executor enroll/status/unenroll` install, inspect and remove an owner-pinned executor: its namespace, ServiceAccount, Deployment, RBAC, enrollment and an unbound requester role. It executes nothing in this release: requests are refused as `NotExecutableInThisBuild`, and no instances change. Enrollment requires verified `OwnerOffline` root-key escrow for every instance. `dcctl install` installs two new resource definitions, `ExecutorEnrollment` and `InstanceOperation`, without deploying the executor. As with every release, an older dcctl refuses a cluster whose definitions a 1.9 `dcctl install` has moved.

## es

- **Ejecutor dentro del clúster (Preview).** `dcctl executor enroll/status/unenroll` instalan, inspeccionan y retiran un ejecutor fijado por su propietario: su espacio de nombres, ServiceAccount, Deployment, RBAC, registro y un rol de solicitante sin vincular. No ejecuta nada en esta versión: las solicitudes se rechazan con `NotExecutableInThisBuild` y ninguna instancia cambia. El registro exige custodia `OwnerOffline` verificada de la clave raíz de cada instancia. `dcctl install` instala dos nuevas definiciones de recursos, `ExecutorEnrollment` e `InstanceOperation`, sin desplegar el ejecutor. Como en cada versión, un dcctl anterior rechaza un clúster cuyas definiciones haya actualizado un `dcctl install` de la versión 1.9.

## zh-CN

- **集群内执行器（Preview）。** `dcctl executor enroll/status/unenroll` 用于安装、查看和移除由所有者固定镜像的执行器，包括其命名空间、ServiceAccount、Deployment、RBAC、注册资源和未绑定的请求方角色。本版本不会执行任何操作：请求会被拒绝，原因为 `NotExecutableInThisBuild`，现有实例不会改变。注册要求每个实例均有经过验证的 `OwnerOffline` 根密钥托管文件。`dcctl install` 会安装 `ExecutorEnrollment` 和 `InstanceOperation` 两种新资源定义，但不会部署执行器。与每次发布一样，旧版 dcctl 会拒绝操作资源定义已由 1.9 版 `dcctl install` 更新的集群。
