---
section: whats-new
---

## en

- **Event lists without a total.** `EventSearchCriteria.includeTotal: false` skips the exact count of matching events, which dominates a page read on a large history; `totalRecords` is then null and the new `SearchResultsPagination.hasMore` says whether another page follows. `hasMore` is also set with the default. During the rolling upgrade, a pod still on v1.6 rejects both `includeTotal` and a selection of `hasMore` as unknown fields, so use either only once every event-management pod runs v1.7.

## es

- **Listas de eventos sin total.** `EventSearchCriteria.includeTotal: false` omite el recuento exacto de eventos coincidentes, que domina la lectura de una página en un historial grande; `totalRecords` pasa a ser null y el nuevo `SearchResultsPagination.hasMore` indica si hay otra página. `hasMore` también se establece con el valor predeterminado. Durante la actualización gradual, un pod que aún ejecuta v1.6 rechaza como campos desconocidos tanto `includeTotal` como la selección de `hasMore`; use cualquiera de los dos solo cuando todos los pods de event-management ejecuten v1.7.

## zh-CN

- **不计算总数的事件列表。** `EventSearchCriteria.includeTotal: false` 跳过匹配事件的精确计数；在大型历史记录中，计数占据了页面读取的大部分时间。此时 `totalRecords` 为 null，新增的 `SearchResultsPagination.hasMore` 表示是否还有下一页。使用默认设置时也会返回 `hasMore`。滚动升级期间，仍运行 v1.6 的 Pod 会将 `includeTotal` 以及对 `hasMore` 的选择都作为未知字段拒绝，因此请等所有 event-management Pod 均运行 v1.7 后再使用其中任何一个。
