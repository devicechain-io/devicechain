---
section: whats-new
---

## en

- **A device asking for an update download in a loop is now throttled.** `update-management` answers a device's `download.request` up to five times at once and then once a second, per device, as it already answers a `sync` at most once every five seconds. A request over that budget is acknowledged without an answer and counted on `devicechain_updatemanagement_update_reports_rejected_total{reason="throttled"}`, so one looping device no longer delays every other tenant's update reports. A device that asks again after a restart or a failed download is still answered.

## es

- **Un dispositivo que pide en bucle la descarga de una actualización ahora se limita.** `update-management` responde al `download.request` de un dispositivo hasta cinco veces seguidas y después una vez por segundo, por dispositivo, igual que ya responde a un `sync` como máximo una vez cada cinco segundos. Una petición que supera ese límite se confirma sin respuesta y se cuenta en `devicechain_updatemanagement_update_reports_rejected_total{reason="throttled"}`, así que un dispositivo en bucle ya no retrasa los informes de actualización de los demás tenants. Un dispositivo que vuelve a pedir tras un reinicio o una descarga fallida sigue recibiendo respuesta.

## zh-CN

- **循环请求更新下载的设备现在会被限流。** `update-management` 对每台设备的 `download.request` 最多连续应答五次，此后每秒一次，就像它对 `sync` 已经最多每五秒应答一次。超出该预算的请求会被确认但不予应答，并计入 `devicechain_updatemanagement_update_reports_rejected_total{reason="throttled"}`，因此单台循环请求的设备不再拖慢其他租户的更新报告。设备在重启或下载失败后再次请求时仍会得到应答。
