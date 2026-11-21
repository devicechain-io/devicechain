---
section: whats-new
---

## en

- **Sharded event-processing startup.** Starting a sharded instance no longer attempts to delete an unsharded legacy consumer on a nonzero group stream, eliminating the resulting broker permission warning. Group zero still cleans up its legacy consumer during an in-place upgrade. Broker permissions are unchanged.

## es

- **Inicio del procesamiento de eventos con particiones.** Al iniciar una instancia con particiones, ya no se intenta eliminar un consumidor heredado sin particiones en el flujo de un grupo distinto de cero, lo que elimina la advertencia de permisos del broker. El grupo cero sigue eliminando su consumidor heredado durante una actualización en la instancia existente. Los permisos del broker no cambian.

## zh-CN

- **分片事件处理启动。** 启动分片实例时，不再尝试从非零分组的数据流中删除未分片的旧版消费者，因此不会再产生相应的消息代理权限警告。原地升级时，零号分组仍会清理其旧版消费者。消息代理权限保持不变。
