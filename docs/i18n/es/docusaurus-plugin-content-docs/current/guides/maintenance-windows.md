---
sidebar_position: 8
title: Ventanas de mantenimiento
---

# Ventanas de mantenimiento

Una ventana de mantenimiento evita que los comandos disruptivos, como un reinicio, se inicien en el
momento equivocado. Esta guía explica cómo configurar una y ver cómo un comando espera a que se
abra. El razonamiento de fondo está en
[Ventanas de mantenimiento](../concepts/commands.md#maintenance-windows).

Las ventanas se gestionan en la API GraphQL de gestión de dispositivos, en
`https://<your-host>/api/device-management/graphql`. Los comandos se emiten como se describe en
[Enviar un comando](./sending-commands.md), contra la API de entrega de comandos.

## Crear una ventana {#create-a-window}

Una ventana es una zona horaria más de 1 a 56 franjas semanales. `slots` es una **cadena** JSON.
Cada franja tiene un `day` (`MONDAY` a `SUNDAY`), un `start` (`HH:MM` de 24 horas, hora de reloj en
esa zona) y una `durationMinutes` (de 1 a 10080, tiempo transcurrido).

```graphql
mutation {
  createMaintenanceWindow(request: {
    token: "weekend-night",
    name: "Weekend night",
    timezone: "Europe/Berlin",
    slots: "[{\"day\":\"SATURDAY\",\"start\":\"22:00\",\"durationMinutes\":240}]"
  }) {
    token timezone slots
  }
}
```

`Local` y los nombres de zona desconocidos se rechazan. Una franja puede cruzar la medianoche, así que
el ejemplo anterior está abierto de las 22:00 a las 02:00 en la noche del sábado al domingo. Para
comprobar qué hará una ventana, pregunta cuándo se abre:

```graphql
query {
  maintenanceWindowOpening(token: "weekend-night", at: "2026-11-02T12:00:00Z") {
    found openNow dispatchAfter closes
  }
}
```

`dispatchAfter` es el instante por el que preguntaste si la ventana está abierta; en caso contrario,
la próxima apertura. Omite `at` para usar el momento actual.

## Asignarla a un tipo de dispositivo {#assign-it-to-a-device-type}

Un tipo de dispositivo tiene como máximo una ventana. Todos los dispositivos de ese tipo la siguen.

```graphql
mutation {
  updateDeviceType(token: "pump-controller", request: {
    maintenanceWindowToken: "weekend-night"
  }) {
    token
    maintenanceWindow { token }
  }
}
```

Envía `null` o un valor vacío para desvincularla. Un tipo de dispositivo sin ventana envía todos los
comandos de inmediato, como antes.

## Marcar un comando como DISRUPTIVE {#mark-a-command-disruptive}

Solo esperan los comandos declarados con impacto `DISRUPTIVE` o `DESTRUCTIVE`. Defínelo en la
definición del comando y después **publica el perfil**; la compuerta lee el vocabulario publicado.

```graphql
mutation {
  createCommandDefinition(request: {
    token: "pump-reboot",
    deviceProfileToken: "pump-profile",
    commandKey: "reboot",
    impact: "DISRUPTIVE"
  }) { token impact }
}
```

`impact` es `NORMAL` si se omite. **Add LwM2M standard commands**, en la consola, crea
`lwm2m.reboot` como `DISRUPTIVE`. Las definiciones declaradas antes no cambian. Un comando cuya clave
no esté declarada con alguno de estos impactos, incluido un `lwm2m.execute` sin procesar, nunca se
retiene.

## Enviar un comando y verlo esperar {#send-and-watch-it-wait}

Envía el comando como de costumbre. Si la ventana está cerrada, el comando se acepta y espera. Si la
ventana está abierta, el comando se envía de inmediato, pero solo hasta que la ventana se cierre.

```graphql
mutation {
  createCommand(request: {
    token: "reboot-pump-7",
    deviceToken: "pump-007",
    name: "reboot"
  }) {
    command { token status dispatchAfter maintenanceWindowToken }
    rejection { code reason }
  }
}
```

El comando permanece en `QUEUED`, `dispatchAfter` es la próxima apertura y `maintenanceWindowToken`
es `weekend-night`. Cuando la ventana se abre, se envía como cualquier otro comando, también a un
dispositivo dormido que despierte más tarde. Los comandos en espera cuentan para el techo de comandos
sin entregar del inquilino y pueden cancelarse, uno a uno o como lote, como cualquier comando en
cola. El campo `waiting` del registro de un lote indica cuántos miembros quedaron retenidos para una
ventana.

Para ver qué ocurriría sin enviar nada, consulta `commandDispatchSchedule` con los tokens de los
dispositivos, la clave del comando y un `expiresAt` opcional.

La ventana controla solo el inicio. Un comando ya iniciado dentro de la ventana termina cuando
termine.

## Cuando la ventana se abre demasiado tarde: WINDOW_AFTER_EXPIRY {#window-after-expiry}

Un comando caduca a los siete días salvo que indiques `expiresAt`. Si la próxima apertura es posterior,
el encolado se rechaza con el código `WINDOW_AFTER_EXPIRY`, porque aceptarlo solo acabaría en
`EXPIRED`. En un lote es un rechazo por dispositivo, y se rechaza todo el lote salvo que se indique
`allowPartial`. Tal como está, no tiene sentido reintentarlo. Para solucionarlo, haz una de estas
cosas:

- indica un `expiresAt` posterior, de modo que el comando sobreviva a la espera;
- añade franjas a la ventana o adelanta la próxima, para que se abra antes de que el comando caduque.

## Editar o eliminar una ventana {#edit-or-delete-a-window}

`updateMaintenanceWindow` es una actualización parcial: omite un campo para dejarlo como está.

```graphql
mutation {
  updateMaintenanceWindow(token: "weekend-night", request: {
    slots: "[{\"day\":\"SATURDAY\",\"start\":\"23:00\",\"durationMinutes\":180}]"
  }) { token slots }
}
```

Los comandos que ya esperan siguen el nuevo calendario en aproximadamente un minuto, porque la
entrega de comandos lo consulta periódicamente en lugar de recibir un aviso. No esperes que el cambio
sea instantáneo.

`deleteMaintenanceWindow` se rechaza mientras algún tipo de dispositivo use la ventana; desvincúlala
primero. Los comandos que ya esperan siguen a su tipo de dispositivo: desvincular la ventana de un
tipo los libera, y pasar el tipo a otra ventana los mueve a la siguiente apertura de esa ventana, en
ese mismo minuto. Los comandos liberados se envían de inmediato, fuera de cualquier ventana: para
detenerlos en lugar de liberarlos, cancélalos (o su lote) antes de desvincular la ventana. Si un dispositivo
está desconectado durante toda una apertura, su comando pasa a la siguiente apertura en lugar de
enviarse cuando el dispositivo vuelva.

## Límites {#limits}

- Una ventana pertenece a un tipo de dispositivo. No hay ventana por grupo, por dispositivo ni
  predeterminada del inquilino, ni ventanas de bloqueo o festivos.
- El control de las actualizaciones por aire no está construido.
- Si no se puede alcanzar la gestión de dispositivos al encolar, el comando se rechaza en lugar de
  enviarse sin control.

El contador `command_delivery_window_restamps_total` cuenta los comandos cuya retención se movió por
una edición, una eliminación, una ventana que se cerró de nuevo antes de que volviera el
dispositivo, o porque su tipo de dispositivo pasó a otra ventana o se quedó sin ventana.
