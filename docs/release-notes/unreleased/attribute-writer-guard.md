---
section: before-upgrade
---

## en

- **The database now refuses device attribute writes that skip the writer protocol.** The device-management database carries a trigger on its attribute table that rejects, with SQLSTATE `DCAP1`, any write to a device's `SHARED` attribute that is not made inside a transaction that set `devicechain.attr_protocol` to `1`. Every v1.9 pod already does, so a normal upgrade from v1.9 is unaffected. Across a skipped minor (an upgrade from v1.8 or older straight to this release, which is not supported), a pod that has not been upgraded yet fails its device `SHARED` attribute writes with `DCAP1` until the rolling update finishes, then works again once every pod runs this release; nothing is written wrongly in between. Other attribute scopes and other owners (assets, areas, customers) are not affected. SQL run by hand against a device's shared attributes must go through the API instead, or set the marker for its own transaction with `SELECT set_config('devicechain.attr_protocol', '1', true)`. `fleetOperationReadiness` now reports `enforcement: ENFORCED` on an upgraded instance.

## es

- **La base de datos ahora rechaza las escrituras de atributos de dispositivo que omiten el protocolo de escritura.** La base de datos de device-management lleva un disparador en su tabla de atributos que rechaza, con SQLSTATE `DCAP1`, cualquier escritura de un atributo `SHARED` de un dispositivo que no se haga dentro de una transacción que haya establecido `devicechain.attr_protocol` en `1`. Todos los pods de v1.9 ya lo hacen, por lo que una actualización normal desde v1.9 no se ve afectada. Al saltarse una versión menor (una actualización desde v1.8 o anterior directamente a esta versión, que no está admitida), un pod que aún no se ha actualizado ve fallar con `DCAP1` sus escrituras de atributos `SHARED` de dispositivo hasta que termina la actualización progresiva, y vuelve a funcionar cuando todos los pods ejecutan esta versión; entretanto no se escribe nada incorrecto. Los demás ámbitos de atributos y los demás propietarios (activos, áreas, clientes) no se ven afectados. El SQL ejecutado a mano sobre los atributos compartidos de un dispositivo debe pasar por la API, o bien establecer el marcador para su propia transacción con `SELECT set_config('devicechain.attr_protocol', '1', true)`. `fleetOperationReadiness` ahora informa `enforcement: ENFORCED` en una instancia actualizada.

## zh-CN

- **数据库现在会拒绝绕过写入协议的设备属性写入。** device-management 数据库的属性表上带有一个触发器：任何对设备 `SHARED` 属性的写入，只要不是在已将 `devicechain.attr_protocol` 设为 `1` 的事务内完成，就会被以 SQLSTATE `DCAP1` 拒绝。所有 v1.9 的 Pod 本来就这样做，因此从 v1.9 正常升级不受影响。若跳过了次要版本（从 v1.8 或更早版本直接升级到本版本，这是不受支持的），尚未升级的 Pod 在滚动更新完成之前，其设备 `SHARED` 属性写入会以 `DCAP1` 失败，所有 Pod 都运行本版本后即恢复正常；期间不会写入任何错误数据。其他属性作用域和其他所有者（资产、区域、客户）不受影响。对设备共享属性手工执行的 SQL 必须改为通过 API，或在自己的事务中用 `SELECT set_config('devicechain.attr_protocol', '1', true)` 设置该标记。升级后的实例上，`fleetOperationReadiness` 现在会报告 `enforcement: ENFORCED`。
