---
section: whats-new
---

## en

- **`dcctl bootstrap --sharding`.** A new flag chooses an instance's sharding level when you install it. The level is recorded on the instance, and an existing level cannot be changed, added or removed later. `off`, the default, renders exactly what dcctl rendered before the flag existed and records nothing. `scale` is refused as not yet available. Before you rely on `--sharding`, run `dcctl install` so the cluster's Instance definition records the field. If you leave the flag out and set a raw `shardGroups` block in `--values`, it works exactly as before, and the bootstrap summary and `dcctl upgrade` report that configuration as `custom`.

## es

- **`dcctl bootstrap --sharding`.** Un nuevo indicador elige el nivel de particionado de una instancia al instalarla. El nivel queda registrado en la instancia, y después no se puede cambiar, añadir ni quitar. `off`, el valor por defecto, genera exactamente lo mismo que dcctl generaba antes de que existiera el indicador y no registra nada. `scale` se rechaza porque aún no está disponible. Antes de usar `--sharding`, ejecute `dcctl install` para que la definición de Instance del clúster registre el campo. Si omite el indicador y define un bloque `shardGroups` propio en `--values`, funciona exactamente igual que antes, y el resumen de bootstrap y `dcctl upgrade` informan de esa configuración como `custom`.

## zh-CN

- **`dcctl bootstrap --sharding`。** 新标志用于在安装实例时选择其分片级别。该级别记录在实例上，之后不能更改、添加或移除。默认值 `off` 生成的内容与该标志出现之前 dcctl 生成的完全相同，且不记录任何内容。`scale` 会因尚不可用而被拒绝。在使用 `--sharding` 之前，请先运行 `dcctl install`，使集群的 Instance 定义能够记录该字段。如果不指定该标志，而在 `--values` 中设置自己的 `shardGroups` 块，其行为与以前完全相同，bootstrap 摘要和 `dcctl upgrade` 会将这种配置报告为 `custom`。
