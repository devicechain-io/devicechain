---
section: whats-new
---

## en

- **A tenant over its outbound rate no longer floods the dead-letter record.** `outbound-connectors` now budgets the dead letters it writes for rate-shed and terminally undeliverable dispatches, per tenant and across all tenants, as REACT already does for the actions its own gate sheds. A dispatch over that budget is still acknowledged and counted on `devicechain_outboundconnectors_connector_dispatch_total` with its usual outcome, so the `ConnectorDispatchRateLimited` alert reads the same; it is also counted on the new `devicechain_outboundconnectors_connector_dispatch_unlettered_total`, and each tenant gets one summary letter per minute in place of the individual ones. Dispatches dead-lettered after their last delivery attempt are recorded individually, as before.

## es

- **Un tenant por encima de su tasa de salida ya no inunda el registro de mensajes fallidos.** `outbound-connectors` limita ahora los mensajes fallidos que escribe para los despachos descartados por tasa y para los que no se pueden entregar de forma definitiva, por tenant y entre todos los tenants, como REACT ya hace con las acciones que descarta su propia puerta. Un despacho por encima de ese límite se sigue confirmando y contando en `devicechain_outboundconnectors_connector_dispatch_total` con su resultado habitual, de modo que la alerta `ConnectorDispatchRateLimited` lee lo mismo; además se cuenta en la nueva `devicechain_outboundconnectors_connector_dispatch_unlettered_total`, y cada tenant recibe un mensaje de resumen por minuto en lugar de los individuales. Los despachos enviados a mensajes fallidos tras su último intento de entrega se siguen registrando uno a uno, como antes.

## zh-CN

- **超出出站速率的租户不再淹没死信记录。** `outbound-connectors` 现在会对因速率被丢弃以及终止性无法投递的调度所写入的死信设定预算，按租户和全部租户分别限制，与 REACT 对其自身闸门丢弃的动作所做的一样。超出该预算的调度仍会被确认，并以原有结果计入 `devicechain_outboundconnectors_connector_dispatch_total`，因此 `ConnectorDispatchRateLimited` 告警读取的内容不变；它还会计入新的 `devicechain_outboundconnectors_connector_dispatch_unlettered_total`，每个租户每分钟获得一条汇总死信，取代逐条死信。在最后一次投递尝试后进入死信的调度仍会像以前一样逐条记录。
