---
section: whats-new
---

## en

- **Agent command proposals (Preview).** Commands expose additive `causeDetails` and `proposal` fields. Legacy `cause` keeps its detection meaning and is null for a command created by an agent proposal.

- Command-delivery serves `proposeCommand`, `approveAgentProposal`, `rejectAgentProposal`, `agentProposal` and `agentProposals` (Preview). The `infrastructure.mcp.commandProposals` switch defaults off; proposals and approvals are refused while off. Enable it only in a separate upgrade after the release rollout completes: pods from an earlier release refuse the key. The broker's auth callout grants command-delivery's new reader permission on the agent kill stream, so complete the rollout, including the callout, before enabling it.
- Each proposal addresses one device. People approve it (`approveAgentProposal`) against a snapshot of the device's published command definition, re-checked at approval. This fresh check is not a cross-service lock: a later remote definition change can still race the commit. The command delivery cutoff is fixed when proposed: one hour by default, at most 24 hours.
- The platform defaults are 50 executions per hour per agent client and independently per person, 100 open proposals and 1000 proposals per hour per tenant, and 200 executions per hour per tenant. Decided proposals and their request keys are kept for 30 days. A tenant kill ends pending proposals and cancels agent commands still waiting (queued, held or parked); it cannot recall one already sent.

## es

- **Propuestas de comandos de agentes (Preview).** Los comandos incorporan los campos aditivos `causeDetails` y `proposal`. El campo heredado `cause` conserva su significado de detección y es nulo para un comando creado por una propuesta de agente.

- Command-delivery ofrece `proposeCommand`, `approveAgentProposal`, `rejectAgentProposal`, `agentProposal` y `agentProposals` (Preview). El ajuste `infrastructure.mcp.commandProposals` está desactivado por defecto; mientras lo esté, se rechazan las propuestas y las aprobaciones. Actívelo solo mediante una actualización independiente después de completar el despliegue de la versión: los pods de una versión anterior rechazan la clave. El servicio de autorización del broker concede a command-delivery el nuevo permiso de lectura del flujo de desactivación de agentes; complete el despliegue, incluido dicho servicio, antes de activarlo.
- Cada propuesta se dirige a un único dispositivo. Las personas la aprueban (`approveAgentProposal`) sobre una instantánea de la definición de comando publicada del dispositivo, que se vuelve a comprobar al aprobar. Esta comprobación actualizada no es un bloqueo entre servicios: un cambio remoto posterior aún puede coincidir con la confirmación. El plazo de entrega del comando queda fijado al proponerlo: una hora por defecto y un máximo de 24 horas.
- Los valores predeterminados de la plataforma son 50 ejecuciones por hora por cliente de agente e independientemente por persona, 100 propuestas abiertas y 1000 propuestas por hora por inquilino, y 200 ejecuciones por hora por inquilino. Las propuestas decididas y sus claves de solicitud se conservan 30 días. Una desactivación del inquilino finaliza las propuestas pendientes y cancela los comandos de agentes que siguen esperando (en cola, retenidos o aparcados); no puede recuperar uno ya enviado.

## zh-CN

- **代理命令提案（Preview）。** 命令新增 `causeDetails` 和 `proposal` 字段。原有 `cause` 保持检测来源的含义，对于代理提案创建的命令返回 null。

- Command-delivery 提供 `proposeCommand`、`approveAgentProposal`、`rejectAgentProposal`、`agentProposal` 和 `agentProposals`（Preview）。`infrastructure.mcp.commandProposals` 默认关闭；关闭时拒绝提案和批准。仅在版本滚动部署全部完成后，通过单独的一次升级启用：较早版本的 Pod 会拒绝该配置键。Command-delivery 对代理终止消息流的新增读取权限由消息代理的授权回调授予，因此启用前必须完成包含授权回调在内的整个版本部署。
- 每个提案仅面向一个指定设备。人员通过 `approveAgentProposal` 根据设备已发布命令定义的快照批准提案，批准时会重新检查该定义。此即时检查并非跨服务锁：后续远程定义变更仍可能与事务提交并发发生。命令交付截止时间在提出提案时固定：默认一小时，最长 24 小时。
- 平台默认限制为：每个代理客户端和每个人分别每小时 50 次执行；每个租户 100 个待决提案、每小时 1000 个提案及每小时 200 次执行。已决提案及其请求键保留 30 天。租户级终止操作会结束待决提案，并取消仍在等待的代理命令（排队、暂缓或停放）；无法撤回已发送的命令。
