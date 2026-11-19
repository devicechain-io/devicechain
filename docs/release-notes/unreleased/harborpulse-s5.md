---
section: whats-new
---

## en

- **Harborpulse now demonstrates signed charger updates, governed restore and physical replacement.** Its six chargers take a synthetic 2.4.1 firmware rollout at 02:00 the next night, with two updates in flight and one deliberate verification failure. The East Quay restore at 21:15 retries real ingest rate refusals while preserving original readings. At day 0 12:30, a gauge receives a new physical unit and credential while retaining its logical identity. `scenario-verify` checks eight new invariants against platform read-backs. `dcctl sim create --manifest harborpulse` now defaults to `hp-governed` (40 readings/second, burst 256, one calculated metric per profile); manually packaged sims need the same tier for a clean verdict. Handshakes add optional `endpoints.updateMgmtGraphQL`; older records derive it from the chart's `/api/device-management/graphql` endpoint; custom routes must supply it explicitly. Each charger logs about 90 outage entries independently of uplinks, keeping the restore bounded at every supported day length.

## es

- **Harborpulse incorpora actualizaciones firmadas, recuperación regulada y sustitución física.** Sus seis cargadores reciben firmware sintético 2.4.1 a las 02:00 de la noche siguiente, con dos actualizaciones simultáneas y un fallo de verificación deliberado. La recuperación de East Quay a las 21:15 reintenta los rechazos por límite de ingesta y conserva las lecturas originales. El día 0 a las 12:30, un medidor recibe una unidad física y una credencial nuevas sin cambiar su identidad lógica. `scenario-verify` comprueba ocho invariantes nuevas mediante consultas a la plataforma. `dcctl sim create --manifest harborpulse` usa ahora `hp-governed` por defecto (40 lecturas por segundo, ráfaga de 256 y una métrica calculada por perfil); las simulaciones creadas manualmente necesitan el mismo nivel para obtener un resultado limpio. El intercambio inicial añade el campo opcional `endpoints.updateMgmtGraphQL`; los registros anteriores lo derivan de `/api/device-management/graphql`; las rutas personalizadas deben indicarlo explícitamente. Cada cargador registra unas 90 entradas durante el corte, independientemente de los envíos y de la duración del día.

## zh-CN

- **Harborpulse 新增签名更新、受限恢复和实体设备更换演示。** 六台充电器在次日凌晨 02:00 接收合成的 2.4.1 固件，每次最多同时更新两台，其中一台故意验证失败。East Quay 在 21:15 恢复供电时会重试真实的摄取限流拒绝，并保留原始读数。第 0 天 12:30，一个水位计更换实体单元和凭据，同时保留逻辑身份。`scenario-verify` 通过平台回读检查八项新增不变量。`dcctl sim create --manifest harborpulse` 现在默认使用 `hp-governed` 层级（每秒 40 个读数、突发容量 256、每个配置文件一项计算指标）；手动创建的模拟也需要使用相同层级才能获得全部通过的结果。握手新增可选字段 `endpoints.updateMgmtGraphQL`；旧记录会从 `/api/device-management/graphql` 推导该端点，自定义路由必须明确提供。每台充电器在停电期间记录约 90 条数据，不受发送间隔和模拟日长度影响。
