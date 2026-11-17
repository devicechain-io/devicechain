---
section: whats-new
---

## en

- **AI inference rate limits are shared across replicas.** AI inference now keeps each tenant's rate and burst budget in its own service database, so every ai-inference replica spends from the same budget and a restarted replica no longer starts with a fresh burst. The upgrade appends one migration that creates the admission-state table; configuration and APIs are unchanged. The shared limiter fails closed: if the database cannot be read or updated, the request is refused rather than admitted against a local allowance. The per-tenant ceiling itself is still looked up from user-management and still falls back to the platform default when user-management is unreachable. During a rolling upgrade, old pods keep their process-local budgets until they are replaced, so the shared ceiling is fully in force once every ai-inference pod runs `v1.7.0`.

## es

- **Los límites de frecuencia de la inferencia de IA se comparten entre réplicas.** La inferencia de IA guarda ahora el presupuesto de frecuencia y de ráfaga de cada inquilino en la base de datos de su propio servicio, de modo que todas las réplicas de ai-inference consumen del mismo presupuesto y una réplica reiniciada ya no empieza con una ráfaga nueva. La actualización añade una migración que crea la tabla de estado de admisión; la configuración y las API no cambian. El limitador compartido falla de forma cerrada: si la base de datos no se puede leer ni actualizar, la solicitud se rechaza en lugar de admitirse con un cupo local. El límite de cada inquilino se sigue consultando en user-management y sigue recurriendo al valor predeterminado de la plataforma cuando user-management no responde. Durante una actualización gradual, los pods antiguos conservan sus presupuestos locales hasta ser sustituidos, por lo que el límite compartido rige por completo cuando todos los pods de ai-inference ejecutan `v1.7.0`.

## zh-CN

- **AI 推理的速率限制在副本之间共享。** AI 推理现在将每个租户的速率和突发预算保存在其自身的服务数据库中，因此所有 ai-inference 副本共用同一份预算，重启后的副本也不会再获得新的突发额度。升级会追加一个创建准入状态表的迁移；配置和 API 均不变。共享限流器采用失败即拒绝的策略：如果无法读取或更新数据库，请求将被拒绝，而不会按本地额度放行。每个租户的上限仍从 user-management 获取，当 user-management 不可达时仍回退到平台默认值。滚动升级期间，旧 pod 在被替换之前仍使用各自进程内的预算，因此当所有 ai-inference pod 都运行 `v1.7.0` 后，共享上限才完全生效。
