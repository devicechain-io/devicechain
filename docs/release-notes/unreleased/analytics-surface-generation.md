---
section: whats-new
---

## en

- **The analytics views survive a rolling upgrade.** An event-management pod of an older release that finds the analytics (SQL/BI) views already at a newer generation leaves them as they are instead of rebuilding them, logs the skip at info level and counts it in `devicechain_eventmanagement_analytics_reconcile_newer_surface_skips_total`.

## es

- **Las vistas analíticas sobreviven a una actualización progresiva.** Un pod de event-management de una versión anterior que encuentra las vistas analíticas (SQL/BI) ya en una generación más reciente las deja como están en lugar de reconstruirlas, registra la omisión con nivel informativo y la cuenta en `devicechain_eventmanagement_analytics_reconcile_newer_surface_skips_total`.

## zh-CN

- **分析视图在滚动升级中保持不变。** 旧版本的 event-management Pod 如果发现分析（SQL/BI）视图已处于更新的代次，会保持原样而不重建，并以 info 级别记录这次跳过，计入 `devicechain_eventmanagement_analytics_reconcile_newer_surface_skips_total`。
