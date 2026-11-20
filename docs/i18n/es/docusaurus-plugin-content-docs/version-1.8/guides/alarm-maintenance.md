---
sidebar_position: 9
title: Mantenimiento de alarmas
---

# Mantenimiento de alarmas

Una ventana de supresión retiene avisos durante el mantenimiento previsto. Las alarmas se siguen generando y mostrando, con una marca de supresión. La detección, la gravedad y los contribuyentes siguen funcionando. Es independiente de las [ventanas de mantenimiento de comandos](./maintenance-windows.md).

Gestione las ventanas en la API GraphQL de device-management: `https://<your-host>/api/device-management/graphql`.

## Elegir la ventana {#choose-the-window}

Indique un token y un motivo obligatorio. Los destinos pueden ser `DEVICE`, `DEVICE_TYPE`, `ENTITY_GROUP`, `AREA`, `ASSET`, `CUSTOMER` o `TENANT`; todos salvo `TENANT` llevan token. Basta con que coincida un destino. Una lista `alarmKeys` vacía cubre todas las claves; si contiene valores, solo cubre esas claves. `bypassSeverity` permite avisar normalmente a partir de esa gravedad.

Use `ONE_SHOT` con `startTime` y `endTime`, o `RECURRING` con una zona IANA en `timezone` y franjas semanales en `slots` (una cadena JSON), con límites opcionales `validFrom` y `validUntil`. Consulte `openings` para previsualizar los intervalos. Incluyen el inicio y excluyen el final; si coinciden varias ventanas, se usa el final más tardío.

```graphql
mutation {
  createAlarmSuppressionWindow(request: {
    token: "pump-service"
    reason: "Inspección de la bomba"
    targets: [{type: "DEVICE", token: "pump-007"}]
    kind: ONE_SHOT
    startTime: "2026-12-01T10:00:00Z"
    endTime: "2026-12-01T12:00:00Z"
    alarmKeys: ["temperature"]
    bypassSeverity: "CRITICAL"
  }) { token status openings { start end } }
}
```

Se permiten hasta 20 destinos y 20 claves por ventana. `ONE_SHOT` dura como máximo 31 días. El inquilino puede tener hasta 500 ventanas vigentes, incluidas las programadas que aún no han terminado ni caducado.

## Permisos y ámbito {#permissions-and-scope}

Leer requiere `alarm:read`. Crear o editar una ventana limitada (solo destinos `DEVICE`, `ONE_SHOT`, hasta 24 horas) requiere `alarm:write`. Las más amplias también requieren `notification:write`. Una edición debe cumplir los requisitos tanto de la ventana anterior como del resultado.

`endAlarmSuppressionWindow` solo requiere `alarm:write` y que la ventana sea visible. Repetir el fin conserva la primera hora. `deleteAlarmSuppressionWindow` requiere los permisos necesarios para crearla. Los miembros con ámbito limitado solo ven y gestionan ventanas totalmente dentro de su ámbito; no pueden cambiar destinos. Los cambios de propiedad actualizan la visibilidad y las ventanas creadas con ámbito limitado siguen restringidas al ámbito original del creador.

## Seguir la liberación {#follow-the-release}

Al terminar la ventana, el aviso se envía en unas pocas pasadas del planificador, según la cola, si la alarma sigue activa y sin reconocer. Si ese ciclo aún no ha enviado avisos, reconocer o resolver la alarma durante la ventana no envía nada, tampoco un aviso de resolución. Los avisos de resolución a destinos que ya recibieron un aviso de ese ciclo siguen la política existente. Las horas de silencio de la política pueden prolongar la espera. Finalizar, ampliar o borrar una ventana surte efecto en la siguiente comprobación. El aviso liberado explica la retención y el escalamiento comienza desde el envío.

Observe [`suppression_total` y `deferral_oldest_due_seconds`](../deployment/metrics-reference.md#notification-management). El contador registra retenciones, comprobaciones, liberaciones y fallos de consulta. El indicador mide trabajo vencido y vuelve a cero cuando ningún inquilino visitado tiene filas pendientes vencidas. Una consulta fallida o no compatible permite enviar avisos.

La marca **generada durante mantenimiento** registra la ventana declarada cuando se procesó ese ciclo, evaluada en la hora del evento. Permanece en el historial al terminar. Use `raisedDuringSuppression` en los criterios de búsqueda para filtrar ciclos marcados o sin marcar; es un filtro histórico, no de supresión actual.

Durante una actualización gradual, las alarmas bajo una ventana aún pueden enviar avisos. Confíe en la supresión cuando device-management y notification-management hayan terminado de desplegarse.
