---
section: whats-new
---

## en

- **The console can promote an update rollout cohort again.** The console's Promote action for a staged update rollout sent a request the update-management API refused at validation, so promoting a cohort from the console failed every time. The request now validates and promotes the cohort, and accepting failed or UNKNOWN outcomes still requires the explicit override. The `promoteUpdateRollout` API itself is unchanged.

## es

- **La consola vuelve a poder promover una cohorte de un despliegue de actualización.** La acción Promover de la consola para un despliegue de actualización por etapas enviaba una solicitud que la API de gestión de actualizaciones rechazaba en la validación, de modo que promover una cohorte desde la consola fallaba siempre. Ahora la solicitud se valida y promueve la cohorte, y aceptar resultados fallidos o UNKNOWN sigue requiriendo la anulación explícita. La API `promoteUpdateRollout` no cambia.

## zh-CN

- **控制台现在可以再次提升更新发布的批次。** 控制台中针对分阶段更新发布的“提升”操作发送的请求在验证阶段被更新管理 API 拒绝，因此从控制台提升批次每次都会失败。现在该请求可以通过验证并提升批次，接受失败或 UNKNOWN 结果仍需明确的覆盖选项。`promoteUpdateRollout` API 本身没有变化。
