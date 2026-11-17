---
section: before-upgrade
---

## en

- **Geofence alarms need `location:read`.** An alarm raised by a rule that tests geofence containment is now read only by a caller that also holds `location:read`, as `detectionStream` already withholds that rule's detections: the alarm's raise and clear times are the device entering and leaving the fence. Without it, the alarm queries, the alarm counts, `alarmDispositions` and the `alarmStream` subscription leave the alarm out, and acknowledging, clearing or labelling it answers as for an unknown alarm. Other alarms are unchanged. The upgrade marks the existing alarms of such rules, judging a rule by whether its definition mentions `inFence`. The read-only baseline and tokens minted through the read-only OAuth scope do not include `location:read`, so a role or token that monitors or acknowledges geofence alarms needs it added. Notifications sent to configured channels are not affected.

## es

- **Las alarmas de geocerca requieren `location:read`.** Una alarma generada por una regla que comprueba la pertenencia a una geocerca ahora solo la lee quien además tiene `location:read`, igual que `detectionStream` ya omite las detecciones de esa regla: las horas de activación y de borrado de la alarma son la entrada y la salida del dispositivo de la geocerca. Sin ese permiso, las consultas de alarmas, los recuentos de alarmas, `alarmDispositions` y la suscripción `alarmStream` omiten la alarma, y reconocerla, borrarla o etiquetarla responde como ante una alarma desconocida. Las demás alarmas no cambian. La actualización marca las alarmas existentes de esas reglas, juzgando una regla por si su definición menciona `inFence`. El conjunto básico de solo lectura y los tokens emitidos mediante el ámbito OAuth de solo lectura no incluyen `location:read`, así que un rol o token que supervise o reconozca alarmas de geocerca necesita que se le añada. Las notificaciones enviadas a los canales configurados no se ven afectadas.

## zh-CN

- **地理围栏告警需要 `location:read`。** 由检测地理围栏包含关系的规则触发的告警，现在只有同时拥有 `location:read` 的调用方才能读取，与 `detectionStream` 已略去该规则检测结果的做法一致：告警的触发和清除时间就是设备进入和离开围栏的时间。没有该权限时，告警查询、告警计数、`alarmDispositions` 和 `alarmStream` 订阅都会略去该告警，对其确认、清除或标注的响应与未知告警相同。其他告警不变。升级会标记此类规则已有的告警，依据是规则定义中是否提及 `inFence`。只读基线权限以及通过只读 OAuth 范围签发的令牌都不包含 `location:read`，因此监控或确认地理围栏告警的角色或令牌需要添加该权限。发送到已配置渠道的通知不受影响。
