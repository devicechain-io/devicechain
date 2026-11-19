---
section: before-upgrade
---

## en

- **Alarm suppression windows are available.** Declare a one-shot or recurring window over devices, device types, groups, areas, assets, customers or the whole tenant. Alarms are still raised, recorded and visible, marked as suppressed; pages are held until the window ends, then sent if the alarm is still active and unacknowledged. Narrow device windows need `alarm:write`; broader windows also need `notification:write`. During the upgrade, alarms under a window may still page; rely on suppression once device-management and notification-management have finished rolling out.

## es

- **Ya están disponibles las ventanas de supresión de alarmas.** Declare una ventana única o recurrente para dispositivos, tipos de dispositivo, grupos, áreas, activos, clientes o todo el inquilino. Las alarmas se siguen generando, registrando y mostrando, marcadas como suprimidas; los avisos se retienen hasta que termina la ventana y se envían si la alarma sigue activa y sin reconocer. Las ventanas limitadas a dispositivos necesitan `alarm:write`; las más amplias también necesitan `notification:write`. Durante la actualización, las alarmas bajo una ventana aún pueden enviar avisos; confíe en la supresión cuando device-management y notification-management hayan terminado de desplegarse.

## zh-CN

- **告警抑制窗口现已可用。** 可为设备、设备类型、组、区域、资产、客户或整个租户声明一次性或周期性窗口。告警仍会触发、记录并显示，同时标记为已抑制；通知暂存至窗口结束，届时若告警仍处于活动状态且尚未确认，则发送通知。小范围设备窗口需要 `alarm:write`；更大范围的窗口还需要 `notification:write`。升级期间，窗口内的告警仍可能发送通知；请在 device-management 和 notification-management 均完成滚动部署后再依赖抑制功能。
