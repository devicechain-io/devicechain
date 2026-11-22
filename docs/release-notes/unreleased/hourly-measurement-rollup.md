---
section: whats-new
---

## en

- **Faster long-range charts.** Measurement reads bucketed by a whole number of hours (1h, 2h, 6h, 24h and so on) are now served from a second, one-hour rollup built over the existing one-minute rollup, so a month-long chart reads a few hundred pre-aggregated rows per metric instead of tens of thousands. The results are the same as before: hours the range cuts through, including the hour still filling, are read from the one-minute level. The new rollup is created and filled from existing history by the upgrade, refreshed in the background, and kept for the same retention window as the one-minute rollup. The existing `DisableRollupReads` switch turns both rollup levels off.

## es

- **Gráficos de largo alcance más rápidos.** Las lecturas de medidas agrupadas por un número entero de horas (1 h, 2 h, 6 h, 24 h, etc.) se sirven ahora desde un segundo rollup de una hora construido sobre el rollup existente de un minuto, de modo que un gráfico de un mes lee unos cientos de filas preagregadas por métrica en lugar de decenas de miles. Los resultados son los mismos que antes: las horas que el rango corta a medias, incluida la hora en curso, se leen del nivel de un minuto. La actualización crea el nuevo rollup y lo rellena con el historial existente; se refresca en segundo plano y se conserva durante la misma ventana de retención que el rollup de un minuto. El interruptor existente `DisableRollupReads` desactiva ambos niveles.

## zh-CN

- **更快的长时间范围图表。** 按整数小时（1 小时、2 小时、6 小时、24 小时等）分桶的测量读取，现在改由建立在现有 1 分钟汇总之上的第二层 1 小时汇总提供，因此一个月范围的图表每个指标只需读取几百行预聚合数据，而不再是数万行。结果与之前相同：范围中途截断的小时（包括仍在进行的当前小时）仍从 1 分钟层读取。升级时会创建新的汇总并用现有历史数据填充，随后在后台刷新，并沿用与 1 分钟汇总相同的保留期。现有的 `DisableRollupReads` 开关会同时关闭两层汇总读取。
