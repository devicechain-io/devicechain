---
section: whats-new
---

## en

- **The rule editor warns when a rule names a `STRING` metric.** A detection rule never reads a text reading, so a rule that names a metric declared `STRING` on its profile can never fire. The console's rule form now says so while the rule is being written, naming the metric; the rule is still accepted and saved. The check is also available to API clients: `validateDetectionRules` takes an optional `metrics` list (each entry a `key` and a `dataType`, at most 1,000) and answers a `namesStringMetric` warning for each such metric a rule names. Leave the list out and no such warning is given. An unknown `dataType`, or a key listed twice with different types, is refused. The console sends the list only for a profile that has a `STRING` metric, so during a rolling upgrade an older event-processing pod simply gives no inline check for such a profile until the upgrade finishes.

## es

- **El editor de reglas avisa cuando una regla nombra una métrica `STRING`.** Una regla de detección nunca lee una lectura de texto, así que una regla que nombra una métrica declarada `STRING` en su perfil no puede dispararse. El formulario de reglas de la consola ahora lo indica mientras se escribe la regla, con el nombre de la métrica; la regla se sigue aceptando y guardando. La comprobación también está disponible para los clientes de la API: `validateDetectionRules` admite una lista opcional `metrics` (cada entrada con `key` y `dataType`, como máximo 1.000) y responde con una advertencia `namesStringMetric` por cada una de esas métricas que nombre una regla. Si se omite la lista, no se emite esa advertencia. Un `dataType` desconocido, o una clave repetida con tipos distintos, se rechaza. La consola envía la lista solo para un perfil que tenga una métrica `STRING`, así que durante una actualización progresiva un pod antiguo de event-processing simplemente no ofrece la comprobación en línea para ese perfil hasta que la actualización termine.

## zh-CN

- **规则编辑器会在规则引用 `STRING` 指标时给出警告。** 检测规则从不读取文本读数，因此引用了在其配置文件中声明为 `STRING` 的指标的规则永远不会触发。控制台的规则表单现在会在编写规则时指出这一点并给出指标名称；规则仍会被接受并保存。API 客户端也可使用此检查：`validateDetectionRules` 接受可选的 `metrics` 列表（每项包含 `key` 和 `dataType`，最多 1,000 项），并为规则引用的每个此类指标返回一条 `namesStringMetric` 警告。省略该列表则不会给出此类警告。未知的 `dataType`，或同一键以不同类型重复列出，会被拒绝。控制台仅在配置文件含有 `STRING` 指标时才发送该列表，因此在滚动升级期间，旧版 event-processing Pod 只是暂时不为此类配置文件提供行内检查，直到升级完成。
