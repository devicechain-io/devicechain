---
section: whats-new
---

## en

- **Rule-health statistics no longer accumulate for superseded profile versions.** Each publish of a device profile starts fresh firing statistics for that version's rules, and the statistics of the version it replaced were kept indefinitely even though the rule-health view never shows them. Event processing now removes them once a week has passed since that version's rules last fired. The statistics of a profile's active version are always kept, as are those of a profile that has no active version. One replica runs the cleanup each hour, in bounded batches per tenant. The new metric `devicechain_eventprocessing_rule_stat_sweep_deleted_total` counts the rows it removes.

## es

- **Las estadísticas de salud de reglas ya no se acumulan para versiones de perfil reemplazadas.** Cada publicación de un perfil de dispositivo inicia nuevas estadísticas de disparo para las reglas de esa versión, y las estadísticas de la versión reemplazada se conservaban indefinidamente aunque la vista de salud de reglas nunca las muestra. El procesamiento de eventos ahora las elimina cuando ha pasado una semana desde el último disparo de las reglas de esa versión. Las estadísticas de la versión activa de un perfil se conservan siempre, al igual que las de un perfil que no tiene versión activa. Una sola réplica ejecuta la limpieza cada hora, en lotes acotados por tenant. La nueva métrica `devicechain_eventprocessing_rule_stat_sweep_deleted_total` cuenta las filas que elimina.

## zh-CN

- **规则健康统计不再为已被取代的配置文件版本累积。** 每次发布设备配置文件都会为该版本的规则建立新的触发统计，而被取代版本的统计此前会无限期保留，尽管规则健康视图从不显示它们。现在，事件处理会在该版本的规则最后一次触发满一周后删除这些统计。配置文件活动版本的统计始终保留，没有活动版本的配置文件的统计也会保留。清理每小时由一个副本执行，按租户分批且每批有上限。新指标 `devicechain_eventprocessing_rule_stat_sweep_deleted_total` 统计其删除的行数。
