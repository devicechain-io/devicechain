---
section: whats-new
---

## en

- **Faster long-range charts for new data.** Measurement reads bucketed by a whole number of hours (1h, 2h, 6h, 24h and so on) are now served from a second, one-hour rollup built over the existing one-minute rollup, so a long chart reads a few hundred pre-aggregated rows per metric instead of tens of thousands. The upgrade creates the new rollup without filling it, so the speed-up applies to data written after the upgrade; readings from before it are read from the one-minute level exactly as before, and the results are the same either way. Hours a range cuts through, including the hour still filling, also come from the one-minute level. The rollup is refreshed in the background every minute and kept for the same retention window as the one-minute rollup. The existing `DisableRollupReads` switch turns both rollup levels off.

## es

- **Gráficos de largo alcance más rápidos para los datos nuevos.** Las lecturas de medidas agrupadas por un número entero de horas (1 h, 2 h, 6 h, 24 h, etc.) se sirven ahora desde un segundo rollup de una hora construido sobre el rollup existente de un minuto, de modo que un gráfico largo lee unos cientos de filas preagregadas por métrica en lugar de decenas de miles. La actualización crea el nuevo rollup sin rellenarlo, por lo que la mejora se aplica a los datos escritos después de la actualización; las lecturas anteriores se leen del nivel de un minuto exactamente como antes, y los resultados son los mismos en ambos casos. Las horas que el rango corta a medias, incluida la hora en curso, también se leen del nivel de un minuto. El rollup se refresca en segundo plano cada minuto y se conserva durante la misma ventana de retención que el rollup de un minuto. El interruptor existente `DisableRollupReads` desactiva ambos niveles.

## zh-CN

- **新数据的长时间范围图表更快。** 按整数小时（1 小时、2 小时、6 小时、24 小时等）分桶的测量读取，现在改由建立在现有 1 分钟汇总之上的第二层 1 小时汇总提供，因此较长的图表每个指标只需读取几百行预聚合数据，而不再是数万行。升级时只创建新的汇总而不回填历史，所以加速作用于升级之后写入的数据；升级之前的读数仍像以前一样从 1 分钟层读取，两种方式的结果相同。范围中途截断的小时（包括仍在进行的当前小时）同样从 1 分钟层读取。该汇总每分钟在后台刷新一次，并沿用与 1 分钟汇总相同的保留期。现有的 `DisableRollupReads` 开关会同时关闭两层汇总读取。
