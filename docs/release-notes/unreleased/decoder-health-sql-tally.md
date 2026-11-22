---
section: whats-new
---

## en

- **Payload decoder health reads stay fast over long spans.** The decoder health tally (`payloadDecoderHealth`) is now summed in the database instead of loading every replica-minute row into device-management. A new migration adds two indexes on the decoder health table: one serves the per-profile read and the other the hourly trim of old rows. The answer is unchanged, including which last error is reported. The migration runs during the upgrade and briefly holds writes to that table while the indexes build.

## es

- **Las lecturas de salud del decodificador de payloads siguen siendo rápidas en periodos largos.** El recuento de salud del decodificador (`payloadDecoderHealth`) se suma ahora en la base de datos en lugar de cargar en device-management cada fila por réplica y minuto. Una nueva migración añade dos índices a la tabla de salud del decodificador: uno sirve la lectura por perfil y el otro la depuración horaria de las filas antiguas. La respuesta no cambia, incluido qué último error se informa. La migración se ejecuta durante la actualización y retiene brevemente las escrituras en esa tabla mientras se crean los índices.

## zh-CN

- **长时间跨度下，载荷解码器健康读取依然快速。** 解码器健康统计（`payloadDecoderHealth`）现在在数据库中求和，不再把每个副本每分钟的行都加载到 device-management。新的迁移为解码器健康表添加了两个索引：一个用于按配置文件的读取，另一个用于每小时清理旧行。返回结果不变，包括报告哪条最后错误。该迁移在升级期间运行，建索引时会短暂阻塞对该表的写入。
