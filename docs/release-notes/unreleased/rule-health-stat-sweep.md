---
section: whats-new
---

## en

- **Rule-health statistics no longer accumulate for superseded profile versions.** Each publish of a device profile starts fresh firing statistics for that version's rules. The statistics of the version it replaced were kept indefinitely, although the rule-health view shows them only if a rollback makes that version active again. Event processing now removes them once a week has passed since that version's rules last fired. The statistics of a profile's active version are always kept, as are those of a profile that has no active version. If a rollback later makes a version active again after its statistics were removed, rule health says so: the new `historyRemovedAt` field on `RuleHealth` gives the time of the removal, the console shows a notice, and fire counts and last-fired times then cover only the fires since that time. One replica runs the cleanup each hour, in bounded batches per tenant. The new metric `devicechain_eventprocessing_rule_stat_sweep_deleted_total` counts the rows it removes.

## es

- **Las estadísticas de salud de reglas ya no se acumulan para versiones de perfil reemplazadas.** Cada publicación de un perfil de dispositivo inicia nuevas estadísticas de disparo para las reglas de esa versión. Las estadísticas de la versión reemplazada se conservaban indefinidamente, aunque la vista de salud de reglas solo las muestra si una reversión vuelve a activar esa versión. El procesamiento de eventos ahora las elimina cuando ha pasado una semana desde el último disparo de las reglas de esa versión. Las estadísticas de la versión activa de un perfil se conservan siempre, al igual que las de un perfil que no tiene versión activa. Si una reversión vuelve a activar más tarde una versión cuyas estadísticas se eliminaron, la salud de reglas lo indica: el nuevo campo `historyRemovedAt` de `RuleHealth` da la hora de la eliminación, la consola muestra un aviso, y los recuentos de disparos y la hora del último disparo solo incluyen los disparos desde ese momento. Una sola réplica ejecuta la limpieza cada hora, en lotes acotados por tenant. La nueva métrica `devicechain_eventprocessing_rule_stat_sweep_deleted_total` cuenta las filas que elimina.

## zh-CN

- **规则健康统计不再为已被取代的配置文件版本累积。** 每次发布设备配置文件都会为该版本的规则建立新的触发统计。被取代版本的统计此前会无限期保留，尽管规则健康视图只有在回滚使该版本重新生效时才会显示它们。现在，事件处理会在该版本的规则最后一次触发满一周后删除这些统计。配置文件活动版本的统计始终保留，没有活动版本的配置文件的统计也会保留。如果某个版本的统计被删除后又因回滚而重新生效，规则健康会明确说明：`RuleHealth` 上新增的 `historyRemovedAt` 字段给出删除时间，控制台会显示提示，此时触发次数和最近触发时间只包含该时间之后的触发。清理每小时由一个副本执行，按租户分批且每批有上限。新指标 `devicechain_eventprocessing_rule_stat_sweep_deleted_total` 统计其删除的行数。
