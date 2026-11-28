---
section: before-upgrade
---

## en

- **GraphQL operations have a cost limit.** Every service now gives each operation a cost before it
  runs: every selected field costs 1, and everything under a page of results costs as many times
  over as the page holds. An operation above 100,000 is refused, and so is one that selects the same
  field under more than 20 names (aliases) at one level. Requests from the console, the dashboard
  app, the SDKs, `dcctl` and the MCP server stay well within both limits. A refused request gets
  HTTP 200 with one error whose `extensions.code` is `QUERY_TOO_COSTLY` or `TOO_MANY_ALIASES`, and
  each service counts refusals in the `devicechain_graphql_work_refused_total` metric. If an
  integration of your own is refused, have it ask for smaller pages, or raise the limit for that
  service with `DC_GRAPHQL_MAX_QUERY_COST` or `DC_GRAPHQL_MAX_FIELD_ALIASES`. See
  [Query cost](../reference/graphql-api.md#query-cost).

## es

- **Las operaciones GraphQL tienen un límite de coste.** Cada servicio asigna ahora un coste a cada
  operación antes de ejecutarla: cada campo seleccionado cuesta 1, y todo lo que está bajo una página
  de resultados cuesta tantas veces como filas tenga la página. Se rechaza una operación que supere
  100.000, y también una que seleccione un mismo campo con más de 20 nombres (alias) en un mismo
  nivel. Las solicitudes de la consola, la aplicación de paneles, los SDK, `dcctl` y el servidor MCP
  quedan muy por debajo de ambos límites. Una solicitud rechazada recibe HTTP 200 con un solo error
  cuyo `extensions.code` es `QUERY_TOO_COSTLY` o `TOO_MANY_ALIASES`, y cada servicio cuenta los
  rechazos en la métrica `devicechain_graphql_work_refused_total`. Si se rechaza una integración
  propia, haz que pida páginas más pequeñas o sube el límite de ese servicio con
  `DC_GRAPHQL_MAX_QUERY_COST` o `DC_GRAPHQL_MAX_FIELD_ALIASES`. Consulta
  [Coste de la consulta](../reference/graphql-api.md#query-cost).

## zh-CN

- **GraphQL 操作有了成本限制。** 每个服务现在会在执行前为每个操作计算成本：每个选择的字段成本为 1，
  一页结果下的所有内容成本乘以该页的行数。成本超过 100,000 的操作会被拒绝；在同一层级以超过 20
  个名称（别名）选择同一字段的操作也会被拒绝。控制台、仪表盘应用、SDK、`dcctl` 和 MCP 服务器的请求远低于这两个限制。
  被拒绝的请求返回 HTTP 200，带一个错误，其 `extensions.code` 为 `QUERY_TOO_COSTLY` 或 `TOO_MANY_ALIASES`，
  每个服务在 `devicechain_graphql_work_refused_total` 指标中统计拒绝次数。如果你自己的集成被拒绝，
  请让它请求更小的页，或用 `DC_GRAPHQL_MAX_QUERY_COST` 或 `DC_GRAPHQL_MAX_FIELD_ALIASES` 提高该服务的限制。
  见[查询成本](../reference/graphql-api.md#query-cost)。
