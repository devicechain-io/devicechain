---
sidebar_position: 6
title: Configuración de Canales de Notificación
---

# Configuración de Canales de Notificación

Las notificaciones llevan una **alarma** levantada la última milla, hasta una persona. Cuando las acciones de una regla de detección levantan una alarma, una **política** por inquilino la enruta por severidad a los **canales** que configures: correo electrónico por SMTP, un webhook, o un canal nativo de Slack, Microsoft Teams o PagerDuty. Las políticas añaden limitación de frecuencia (throttling) y **escalado** de las alarmas no reconocidas.

Esta ruta de máquina a humano está deliberadamente separada de los **[conectores de salida](../concepts/outbound-connectors.md)** de máquina a máquina. Los conectores llevan payloads a *sistemas*; las notificaciones llevan alertas a *personas*, con destinatarios y enrutamiento por severidad. Para saber cómo se levantan las alarmas en primer lugar, consulta [Procesamiento de Eventos y Alarmas](../concepts/event-processing.md).

Para gestionar las políticas y las horas de silencio desde la consola web, consulta [Políticas de notificación](../user-guide/notification-policies.md) en la guía del usuario.

:::note Estado
Disponible. Gestionas los canales y las políticas a través de la API GraphQL de notification-management. La lectura requiere la autoridad `notification:read`; crear o modificar cualquier cosa requiere `notification:write`.
:::

## Canales

Un **canal** es un endpoint de entrega que configura tu inquilino: una instancia de un **tipo** de canal, más su configuración de conexión. Consulta `notificationChannelTypes` para ver los tipos que define la plataforma. Hoy `smtp`, `webhook`, `slack`, `teams` y `pagerduty` se entregan con adaptadores funcionales; el indicador `available` de cada tipo dice si su adaptador ya está disponible.

Un canal divide su configuración en dos:

- **`config`**: la configuración de conexión no secreta, como un documento JSON (host/puerto/from de SMTP; URL/método/encabezados del webhook y cómo se autentica).
- **`secret`**: la credencial, como la contraseña de SMTP o un token de autenticación del webhook. Se almacena en el **almacén de secretos** cifrado por sobre (envelope-encrypted) de la plataforma y es de **solo escritura**. La envías al crear, y nunca se devuelve al leer; el canal solo expone un booleano `hasSecret`.

En una **actualización**, el campo `secret` se comporta así:

- **Omítelo** para dejar el secreto existente sin cambios. Nunca necesitas reenviarlo.
- Envía un valor no nulo para reemplazarlo.
- Envía `null` o una cadena vacía para borrarlo.
- En un canal webhook cuya configuración declara `bearer` o `header`, borrar el secreto se rechaza. Cambia `auth` a `none` en la misma solicitud si quieres que el endpoint sea anónimo.

Si tu cliente enlaza una variable por campo, una variable no suministrada llega como un `null` explícito y **borra el secreto**. En un canal webhook que declara `bearer` o `header`, ese mismo `null` hace fallar la actualización entera, incluso una que solo pretendía renombrar el canal. Envía la solicitud completa como una sola variable y deja fuera la clave `secret`.

### Crear un canal SMTP

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-email",
    name: "Operations email",
    channelType: "smtp",
    config: "{\"host\":\"smtp.example.com\",\"port\":587,\"from\":\"alerts@example.com\",\"username\":\"alerts\",\"security\":\"starttls\"}",
    secret: "<smtp password>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

Un canal SMTP que define `username` debe tener un secreto. Una entrega a uno que no lo tiene se
rechaza en su primer intento, antes de que la plataforma se conecte al servidor de correo, y no se
reintenta; se cuenta con `reason="credential"` (ver más abajo). Esto se comprueba en la entrega, no
al guardar el canal.

### Crear un canal webhook

Un canal webhook realiza un POST de la notificación renderizada a una URL. Créalo de la misma manera, con `channelType: "webhook"` y una configuración que lleve la `url`, un modo `auth` y, opcionalmente, `method` y encabezados adicionales en `headers`. El único `method` aceptado es `POST`, que también es el predeterminado; cualquier otro método se rechaza al guardar el canal.

`auth` es obligatorio e indica cómo se autentica el canal:

| `auth` | Qué se envía | `secret` |
| --- | --- | --- |
| `none` | Ningún encabezado de credencial. Úsalo cuando la propia URL lleva la credencial, como en un webhook entrante de Slack. | No debe configurarse |
| `bearer` | `Authorization: Bearer <secret>` | Obligatorio |
| `header` | El secreto en el encabezado indicado por `authHeader`, precedido por `authScheme` y un espacio si lo configuras. Por ejemplo, `"authHeader":"X-API-Key"` envía el token tal cual, y `"authHeader":"Authorization","authScheme":"Token"` envía `Authorization: Token <secret>`. | Obligatorio |

`authHeader` y `authScheme` solo se leen con `header`. Con `none` o `bearer`, omítelos: un canal que los configura se rechaza en lugar de ignorarlos en silencio.

Un canal cuyo `auth` y `secret` no concuerdan se rechaza al guardarlo, no cuando se dispara una alarma. Eso incluye que falte `auth`, `bearer` o `header` sin secreto, y `none` con secreto. Para convertir un canal `bearer` en anónimo, envía `auth` `none` y `secret: null` en la misma actualización. Una actualización que solo renombra, describe o desactiva un canal no se comprueba, así que siempre puedes desactivar un canal mal configurado; activarlo sí se comprueba.

Si un canal llega a la entrega en ese estado de todos modos, por ejemplo uno guardado antes de que existiera `auth`, no se envía. La entrega se rechaza en su primer intento y no se reintenta, y el servicio de notificaciones registra el inquilino, el token del canal y el motivo. El rechazo se cuenta en `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`, sobre el que puedes crear una alerta. El mismo contador lleva `reason="egress"` para un canal webhook o SMTP cuyo destino está en una dirección a la que el tráfico saliente no puede llegar, como una dirección privada, de loopback o de metadatos de la nube. También lleva `reason="no_adapter"`, que se cuenta cuando se omite un canal de un tipo para el que esta versión no tiene adaptador de entrega, de modo que nadie recibe notificación por él. Los rechazos `credential` y `egress` son definitivos: la entrega no se reintenta.

```graphql
mutation {
  createNotificationChannel(request: {
    token: "oncall-hook",
    name: "On-call webhook",
    channelType: "webhook",
    config: "{\"url\":\"https://hooks.example.com/alarms\",\"auth\":\"bearer\"}",
    secret: "<token>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

Para un webhook entrante de Slack, usa `"auth":"none"` y omite `secret`. El [canal de Slack](#create-a-slack-channel) nativo de abajo da formato al mensaje por ti y es la mejor opción.

El cuerpo JSON de un canal webhook incluye el `eventType` de la alarma (`RAISED`, `ESCALATED`, `DEESCALATED`, `CLEARED`, `ACKNOWLEDGED`, o `RENOTIFIED` para una renotificación temporizada), `alarmToken`, `alarmKey`, `severity`, `state`, el `originatorType` y el `originatorId` del originador, y las horas. Cuando se conoce el dispositivo de la alarma, incluye además `deviceToken`, `deviceName` y `deviceTypeToken`; una clave se omite cuando su valor se desconoce (por ejemplo, un dispositivo sin nombre) y nunca se envía vacía. Una renotificación temporizada no las incluye.

### Canales nativos de Slack, Teams y PagerDuty {#native-channels}

Los tres tipos nativos usan el formato propio de cada servicio en lugar de un cuerpo JSON genérico. Comparten tres reglas:

- **La credencial es el `secret`.** En Slack y Teams es la URL del webhook (que ya es una credencial, así que se almacena cifrada y nunca se devuelve, y no va en `config`); en PagerDuty es la clave de enrutamiento de Events API v2. No hay ajuste `auth`. Un canal nativo sin secreto se rechaza al guardarlo, y una entrega a uno que no lo tiene se rechaza en el primer intento y no se reintenta; se cuenta con `reason="credential"`.
- **`config` es opcional y estricto.** Una clave desconocida se rechaza al guardar el canal, de modo que un ajuste mal escrito no se ignora en silencio. `linkBaseUrl` es la dirección de tu consola; si se establece, el mensaje incluye un botón o enlace **Abrir alarmas** a `<linkBaseUrl>/alarms`.
- **La entrega pasa por la misma protección de salida que un webhook.** Un destino en una dirección privada, de bucle local o de metadatos de nube se rechaza y no se reintenta (`reason="egress"`), y las excepciones de salida de la instancia se aplican igual que en un webhook. Los `recipients` de una regla se ignoran: el webhook o la clave de enrutamiento deciden adónde va el mensaje.

Cada mensaje indica la alarma, su severidad, su estado, el dispositivo de origen, la métrica y el valor que la levantó, y cuándo se levantó. Mientras una alarma está abierta y sin reconocer, una indicación dice que hay que reconocerla en la consola para detener más notificaciones; una alarma cerrada o reconocida omite la indicación.

### Crear un canal de Slack {#create-a-slack-channel}

Un canal de Slack publica un mensaje Block Kit en la URL de un webhook entrante de Slack: un encabezado, una cuadrícula con los campos de la alarma, la indicación de reconocimiento y un botón opcional **Abrir alarmas**. La barra de color del mensaje sigue la severidad (rojo para `CRITICAL`, naranja para `MAJOR`, ámbar para `MINOR` y `WARNING`, gris para `INDETERMINATE`), y pasa a verde cuando la alarma se cierra y a azul cuando se reconoce. El texto de la alarma se escapa para que no pueda inyectar una mención o un enlace de Slack.

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-slack",
    name: "Slack de operaciones",
    channelType: "slack",
    config: "{\"linkBaseUrl\":\"https://console.example.com\"}",
    secret: "https://hooks.slack.com/services/T000/B000/XXXXXXXX",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

El secreto debe ser una URL `https`.

### Crear un canal de Microsoft Teams {#create-a-microsoft-teams-channel}

Un canal de Teams publica una Adaptive Card en la URL del webhook de un flujo de **Workflows** (Power Automate) de Teams que usa el desencadenador *Cuando se recibe una solicitud de webhook de Teams* y publica la tarjeta en un chat o canal. Los webhooks de conector de Office 365, ya retirados, no son compatibles. La tarjeta lleva los mismos datos que el mensaje de Slack, con el título coloreado según la severidad, y una acción **Abrir alarmas** si se establece `linkBaseUrl`.

```graphql
mutation {
  createNotificationChannel(request: {
    token: "ops-teams",
    channelType: "teams",
    secret: "https://prod-00.westus.logic.azure.com:443/workflows/...",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

### Crear un canal de PagerDuty {#create-a-pagerduty-channel}

Un canal de PagerDuty envía eventos de Events API v2 y asigna el ciclo de vida de la alarma al del incidente. Cada evento lleva `dedup_key` con el token de la alarma, de modo que toda la vida de una alarma es un solo incidente:

| Evento de la alarma | Acción de PagerDuty |
| --- | --- |
| levantada, escalada, desescalada o renotificada por el escalado de una política | `trigger` (una repetición sobre un incidente abierto lo actualiza en lugar de abrir otro) |
| reconocida | `acknowledge` |
| cerrada | `resolve` |

La severidad de la alarma se asigna a la de PagerDuty: `CRITICAL` a `critical`, `MAJOR` a `error`, `MINOR` y `WARNING` a `warning`, cualquier otra a `info`. Para que la política resuelva incidentes, enruta las transiciones de cierre y reconocimiento por el mismo canal que el levantamiento.

`config` admite `region` (`us`, el valor por defecto, o `eu`, que selecciona `events.eu.pagerduty.com`), `source` (el origen del evento; `devicechain` por defecto) y `linkBaseUrl`.

```graphql
mutation {
  createNotificationChannel(request: {
    token: "oncall-pagerduty",
    channelType: "pagerduty",
    config: "{\"region\":\"eu\",\"source\":\"plant-3\"}",
    secret: "<routing key>",
    enabled: true
  }) { token channelType hasSecret enabled }
}
```

### En la consola {#channels-in-the-console}

**Canales de notificación**, junto a **Políticas de notificación** en la barra lateral, lista, crea, edita y elimina canales de todos los tipos con un formulario para cada uno. El campo de credencial es de solo escritura: en un canal existente indica que hay una credencial almacenada, y dejarlo vacío la conserva.

Un canal webhook también puede **firmar** sus entregas para que el receptor pueda verificarlas. Está desactivado salvo que lo pidas, añadiendo `"signing":true` al `config` del canal una vez que le hayas generado un secreto de firma. Un canal configurado para firmar que no tiene secreto se rechaza, no se envía sin firma, y se cuenta con `reason="signing"`. Consulta [Firma de Webhooks de Salida](signed-webhooks.md).

Los mensajes de chat y el resumen del webhook nombran el dispositivo por su nombre (o por su token si no tiene nombre) e indican su tipo de dispositivo. Cuando el canal tiene `linkBaseUrl` y la alarma indica su dispositivo, el mensaje incluye además un botón o enlace **Open device** a `<linkBaseUrl>/devices/<token del dispositivo>`; sin `linkBaseUrl`, o para una alarma que no indica su dispositivo, no hay enlace al dispositivo.

## Políticas

Una **política** decide qué alarmas levantadas se entregan, a quién y a través de qué canales. Lleva un conjunto de **reglas**. Cada regla mapea:

- una `severity`: `CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, `INDETERMINATE`, o `"*"` para cualquiera;
- a un canal, nombrado por token;
- con un arreglo JSON de `recipients` que interpreta el adaptador: direcciones de correo para SMTP; puede estar vacío para un webhook.

Aquí la severidad va en **mayúsculas** porque es la severidad de la *alarma*. La severidad de autoría de una regla de detección va en minúsculas (`major`) y se convierte a mayúsculas al levantar la alarma, así que una regla de notificación siempre coincide con la forma en mayúsculas. Una regla cuya severidad no sea uno de esos valores (un `major` en minúsculas, por ejemplo) se **rechaza al escribir**, en lugar de almacenarse como una regla que nunca podría coincidir.

:::caution Las políticas son de alcance para todo el inquilino
`deviceTypeToken` **todavía no se respeta**, y una política que lo establece se rechaza al escribir. Deja `deviceTypeToken` sin establecer. Consulta [Acotamiento por tipo de dispositivo](#device-type-scoping) para saber por qué.
:::

### Notificar cuando una alarma se resuelve {#notify-on-clear}

De forma predeterminada, una política avisa a sus canales solo cuando una alarma se genera o escala. Establece `notifyOnClear: true` en una política para avisarles también cuando una alarma que la política notificó **se resuelve** (`clear`). El aviso va a los canales y destinatarios que realmente entregaron un aviso de esa alarma, entre los que la política enruta, sea cual sea la severidad de la alarma al resolverse (una alarma generada como `CRITICAL`, bajada a `MAJOR` y luego resuelta se resuelve en el canal que la notificó). Su `eventType` es `CLEARED`, que PagerDuty traduce en un resolve. Está desactivado de forma predeterminada, así que una política anterior a la opción se comporta como antes.

```graphql
mutation {
  updateNotificationPolicy(token: "ops", request: { notifyOnClear: true }) { token notifyOnClear }
}
```

Una resolución es informativa, así que se trata de forma distinta a un aviso:

- Va solo a los destinos que entregaron un aviso en ese ciclo. Una política que nunca notificó la alarma (su notificación se limitó por frecuencia, se retuvo por las horas de silencio, o la política se creó después de la generación) no envía resolución.
- Se envía una vez por ciclo, después de registrar la resolución, así que ninguna escalada de "sigue sin resolverse" la sigue. Si ningún canal la acepta, se reintenta; cuando algún canal la ha aceptado, no se repite. Una resolución que se procesa antes de que se haya registrado el aviso de su alarma se envía justo después de ese aviso.
- **No** se limita por frecuencia, no cuenta como notificación y no inicia la escalada.
- Una resolución que ocurre durante las horas de silencio de una política se **descarta**, no se retiene: un "resuelta" liberado horas después comunicaría una noticia vieja.
- `notifyOnClear` no se puede borrar con `null`; envía `true` o `false`.

### Acotamiento por tipo de dispositivo {#device-type-scoping}

Acotar una política a un tipo de dispositivo necesita una búsqueda entre servicios que vaya del originador de la alarma a su tipo de dispositivo, y eso aún no ha llegado. Hasta entonces, el despachador omite una política acotada en lugar de aplicarla a todo el inquilino y notificar de más. Rechazar la escritura es deliberado: una política que aceptara el campo devolvería éxito y luego no entregaría nada en absoluto.

### Limitación de frecuencia y escalado {#throttling-and-escalation}

Dos ajustes más moldean la entrega:

- **`throttleSeconds`** es el intervalo mínimo entre notificaciones para la *misma* alarma, de modo que una condición intermitente (flapping) no inunde un canal. Un cambio que escala la severidad de la alarma no se limita. `null` significa sin limitación de frecuencia.
- **`escalateAfterSeconds`** y **`maxEscalations`**: cuando están configurados (> 0), una alarma que permanece **no reconocida y sin despejar** durante ese tiempo desde su última notificación se vuelve a notificar, hasta el tope. Si `maxEscalations` no está configurado o vale `0`, se aplica un tope predeterminado de 5 para todo el servicio. Un `escalateAfterSeconds` con valor `null`/`0` deshabilita el escalado para la política.

El escalado es seguro de ejecutar en varias réplicas: cada nivel de escalado se reclama antes de enviarse, así que exactamente una réplica lo entrega. Cada alarma tiene **un único reloj y nivel de escalado compartidos**. Si coinciden varias políticas con escalado, la ventana más corta fija la cadencia y cada tope cuenta contra el nivel compartido.

```graphql
mutation {
  createNotificationPolicy(request: {
    token: "default-routing",
    name: "Default alarm routing",
    throttleSeconds: 300,
    escalateAfterSeconds: 900,
    maxEscalations: 3,
    enabled: true,
    rules: [
      { severity: "CRITICAL", channelToken: "oncall-hook", recipients: "[]" },
      { severity: "*", channelToken: "ops-email", recipients: "[\"ops@example.com\"]" }
    ]
  }) { token enabled rules { severity channel { token } } }
}
```

En una actualización, las `rules` de la solicitud **reemplazan** el conjunto de reglas existente de la política. Omite `rules` para dejar intactas las reglas almacenadas; enviar `null` o `[]` deja la política sin reglas. Igual que con el `secret` de un canal, un cliente que enlaza una variable por campo envía unas `rules` no suministradas como `null` y vacía el conjunto de reglas, así que envía la solicitud completa como una sola variable. Nombrar un token de canal desconocido hace fallar toda la escritura.

Dos personas que editan la misma política pueden pisarse: un campo que cambian ambas conserva solo el último guardado y, como una lista `rules` reemplaza el conjunto entero, el segundo guardado descarta todas las reglas que escribió el primero. Para evitarlo, pasa `expectedUpdatedAt` con el `updatedAt` de la política tal como la leíste (o tal como lo devolvió tu actualización anterior). Si la política ha cambiado desde entonces, la actualización se rechaza con «notification policy was modified by another writer; reload and try again» y no se escribe nada. Vuelve a cargarla, aplica tu cambio de nuevo y envíala otra vez.

### Horas de silencio {#quiet-hours}

Las **horas de silencio** evitan que una política avise a las personas a deshoras, por ejemplo por la noche. Mientras una política está dentro de sus horas de silencio **retiene** sus notificaciones en lugar de enviarlas, y las envía cuando termina el periodo de silencio. Cada política tiene sus propias horas de silencio, de modo que una política del turno de noche y otra del equipo de día pueden seguir horarios distintos.

Las horas de silencio no son [ventanas de mantenimiento](./maintenance-windows.md). Una ventana de mantenimiento pertenece a un tipo de dispositivo e indica cuándo pueden *empezar* los comandos disruptivos. Las horas de silencio pertenecen a una política de notificación e indican cuándo esa política *retiene sus avisos*. Usan el mismo formato de franjas y las mismas reglas de zona horaria y horario de verano, pero por lo demás no guardan relación, y ninguna afecta a la otra.

Una política obtiene sus horas de silencio de tres campos. Valen `null` en una política sin horas de silencio.

| Campo | Significado |
| --- | --- |
| `quietHoursTimezone` | La zona horaria IANA en la que se leen las franjas, como `Europe/Berlin`. `Local` y los nombres desconocidos se rechazan. Siempre se usa la zona de la propia política, nunca la del servidor. |
| `quietHoursSlots` | Una **cadena** JSON con de 1 a 56 franjas semanales, en el mismo documento que una ventana de mantenimiento. Cada franja tiene un `day` (`MONDAY` a `SUNDAY`), un `start` (`HH:MM` de 24 horas, hora de reloj en la zona) y una `durationMinutes` (de 1 a 10080, tiempo transcurrido). |
| `quietHoursBypassSeverity` | `CRITICAL`, `MAJOR`, `MINOR`, `WARNING` o `INDETERMINATE`, o `null`. Las alarmas con esta severidad o superior se envían incluso durante las horas de silencio. `null` retiene todas las severidades. Se permite, sin efecto, en una política sin horas de silencio. |

Define `quietHoursTimezone` y `quietHoursSlots` juntos, o deja ambos en `null`.

```graphql
mutation {
  createNotificationPolicy(request: {
    token: "night-shift",
    name: "Night shift pages",
    enabled: true,
    quietHoursTimezone: "Europe/Berlin",
    quietHoursSlots: "[{\"day\":\"MONDAY\",\"start\":\"22:00\",\"durationMinutes\":480},{\"day\":\"TUESDAY\",\"start\":\"22:00\",\"durationMinutes\":480}]",
    quietHoursBypassSeverity: "CRITICAL",
    rules: [
      { severity: "*", channelToken: "ops-email", recipients: "[\"ops@example.com\"]" }
    ]
  }) { token quietHoursTimezone quietHoursSlots quietHoursBypassSeverity }
}
```

Esta política está en silencio de 22:00 a 06:00 las noches del lunes y del martes, en hora de Berlín. Una alarma `CRITICAL` en ese periodo se envía igualmente de inmediato. Cualquier otra alarma espera hasta las 06:00.

`updateNotificationPolicy` es una actualización parcial, y los tres campos de horas de silencio admiten tres estados: omítelo para conservarlo, envía `null` para borrarlo, envía un valor para establecerlo. El servidor comprueba los campos en conjunto tras aplicar la actualización, así que para **quitar** las horas de silencio, borra `quietHoursTimezone` y `quietHoursSlots` en la misma solicitud (y también `quietHoursBypassSeverity`, si quieres eliminarla).

```graphql
mutation {
  updateNotificationPolicy(
    token: "night-shift",
    request: { quietHoursTimezone: null, quietHoursSlots: null }
  ) { token quietHoursTimezone quietHoursSlots }
}
```

#### Qué ocurre durante las horas de silencio {#quiet-hours-behavior}

- **Retenida y luego enviada.** Cuando una alarma generada o escalada coincide con una política que está dentro de sus horas de silencio, y la severidad de la alarma es inferior a la severidad de excepción (o no hay excepción), la notificación se **retiene**, no se descarta. La política la envía por sus reglas cuando termina el periodo de silencio. El planificador de escaladas libera las notificaciones retenidas en su pasada, que se ejecuta una vez por minuto por defecto, así que un envío puede retrasarse hasta una pasada. Un envío interrumpido por un reinicio del servicio de notificaciones no se pierde: se envía unos minutos después. En casos poco frecuentes, eso significa que el mismo aviso se envía dos veces.
- **Reconocer o cerrar la descarta.** Una notificación retenida se descarta en lugar de enviarse si la alarma fue reconocida o cerrada después del evento retenido.
- **Una notificación retenida por alarma y política.** Si la misma política retiene un evento posterior, generado o escalado, de la misma alarma, el posterior sustituye al anterior. El aviso de la mañana describe la alarma tal como quedó por última vez, y una alarma que oscila durante la noche cuesta un aviso, no uno por oscilación.
- **La severidad de excepción se envía de inmediato.** Una alarma con la severidad de excepción o superior se envía al instante, incluso dentro de las horas de silencio. Una alarma cuya severidad no es uno de los niveles conocidos se envía siempre.
- **Primero la limitación.** Una notificación que la limitación de la política suprime se suprime, no se retiene.
- **La escalada espera.** Mientras una política está en horas de silencio, sus renotificaciones por escalada esperan para las alarmas por debajo de la severidad de excepción. Se reanudan cuando termina el periodo: una alarma que sigue sin reconocer y sin cerrar se vuelve a notificar en la primera pasada posterior. Las horas de silencio no consumen niveles de escalada.
- **Cada política es independiente.** Una política sin horas de silencio, o que no está en silencio en ese momento, sigue avisando de inmediato por la misma alarma. Una notificación retenida se envía igualmente al final del periodo aunque otra política ya haya avisado a alguien, porque los destinatarios pueden ser distintos.
- **Un aviso nuevo responde al retenido.** Cuando la propia política avisa por la alarma, porque terminaron sus horas de silencio o porque la alarma alcanzó la severidad de excepción, lo que retenía para esa alarma se descarta en lugar de enviarse después.
- **Las ediciones reevalúan lo retenido.** Cambiar las horas de silencio o la severidad de excepción de una política, o habilitarla o deshabilitarla, se aplica en la siguiente pasada. Una notificación retenida se envía si la política ya no la retiene, o sigue retenida hasta el nuevo final si todavía la retiene. Una política deshabilitada o eliminada descarta lo que retenía.
- **Un envío fallido se reintenta.** Un envío que no llega a ningún canal, porque todas las entregas fallaron, se reintenta al cabo de 1, 2, 4 y 8 minutos, 5 intentos en total. Después se descarta, se cuenta y se registra como mensaje fallido (dead letter), igual que una alarma que no llegó a nadie cuando se generó.

#### Zonas horarias y horario de verano {#quiet-hours-time-zones}

Los franjas son horas de reloj en la `quietHoursTimezone` de la política, así que las horas de silencio siguen el horario de verano con la zona. Una franja cuyo inicio cae en un salto hacia delante, una hora que no existe, empieza al final del salto. Una franja cuyo inicio cae en un solapamiento de retroceso, una hora que ocurre dos veces, empieza en la primera aparición. La duración de una franja es tiempo transcurrido, por lo que una franja larga que atraviesa un cambio de hora termina una hora antes o después en el reloj.

Una franja puede pasar de medianoche. Una franja que pasa del final del domingo continúa en lunes.

#### Qué rechaza el servidor {#quiet-hours-validation}

Cada infracción se responde con `extensions.code` `INVALID_VALUE`, y no se escribe nada.

- `quietHoursTimezone` y `quietHoursSlots` no se definen juntos.
- La zona es `Local` o un nombre desconocido.
- Una franja tiene un `day` desconocido, un `start` que no es `HH:MM` de 24 horas o una `durationMinutes` fuera de 1 a 10080, o la lista no tiene de 1 a 56 franjas.
- Dos franjas se **solapan** en la semana tal como está escrita. Las franjas que solo se tocan son válidas, y una franja de domingo que pasa de medianoche se comprueba contra el lunes.
- Las franjas en conjunto **cubren la semana completa** (10080 minutos). Para silenciar una política por completo, deshabilítala.

#### Cómo observarlo {#quiet-hours-metric}

El contador `devicechain_notificationmanagement_quiet_hours_total` cuenta lo que hicieron las horas de silencio, con la etiqueta `outcome`:

| `outcome` | Se cuenta cuando |
| --- | --- |
| `held` | Una notificación se retuvo en lugar de enviarse. |
| `released` | Una notificación retenida se envió al terminar su periodo. |
| `dropped_resolved` | Una notificación retenida se descartó porque la alarma fue reconocida o cerrada. |
| `dropped_policy` | Una notificación retenida se descartó porque su política se deshabilitó o se eliminó. |
| `dropped_exhausted` | Una notificación retenida se descartó tras 5 envíos que no llegaron a ningún canal. También se registra como mensaje fallido (dead letter). |
| `superseded` | Una notificación retenida se descartó porque la política avisó de un evento más reciente de la misma alarma. |

En la consola, las horas de silencio se editan en la página **Supervisión → Políticas de notificación**; consulta [Políticas de notificación](../user-guide/notification-policies.md).

## Registro de entregas {#delivery-ledger}

Cada intento de enviar la notificación de una alarma por un canal queda registrado en el **registro de entregas** del inquilino, de modo que «¿se avisó realmente de esta alarma, a quién y, si no, por qué?» tiene respuesta. Consúltalo con `notificationAttempts` (requiere `notification:read`):

```graphql
query {
  notificationAttempts(criteria: { pageNumber: 1, pageSize: 20, alarmToken: "<token de la alarma>" }) {
    results {
      attemptedAt trigger policyToken channelToken recipients
      status attempt maxAttempts latencyMillis errorCode errorMessage
    }
  }
}
```

Cada registro es un intento, del más reciente al más antiguo. Un canal con tres intentos permitidos que falla dos veces y luego entrega deja tres registros: `FAILED` (intento 1), `FAILED` (intento 2) y `SENT` (intento 3). Filtra por `alarmToken`, `channelToken` o `status`, y por tiempo con `since` y `until` (RFC 3339).

| `status` | Significado |
| --- | --- |
| `SENT` | El canal aceptó la notificación. |
| `FAILED` | Un fallo que un reintento podría resolver: un tiempo de espera agotado, un código de error, una conexión rechazada. |
| `REFUSED` | Rechazado de forma definitiva, así que no hubo reintento: el destino es uno al que el tráfico saliente no puede llegar (`EGRESS_BLOCKED`), o la configuración de credenciales del canal se rechaza (`CREDENTIAL_REFUSED`). |
| `NOT_ATTEMPTED` | Planificado pero nunca intentado: no se pudo leer el secreto de entrega del canal (`SECRET_UNAVAILABLE`), o el tiempo disponible para las entregas de la alarma se agotó antes de llegar a este canal (`BUDGET_SPENT`). |

`trigger` indica qué causó la entrega: `ALARM` (un cambio de estado de la alarma), `ESCALATION` (se volvió a notificar una alarma no reconocida) o `QUIET_HOURS_RELEASE` (se envió una notificación retenida por las horas de silencio al terminar el periodo). Versiones posteriores pueden añadir valores de `status`, `trigger` y `errorCode`, así que trata uno que no reconozcas como informativo.

`errorMessage` es el texto de error del proveedor con cada URL sustituida por `<url>`, porque la URL de un webhook de Slack o Teams es en sí una credencial, y el registro lo puede leer cualquiera con `notification:read`. El mensaje en sí no se guarda: `subjectHash` (un SHA-256 de la línea de asunto) muestra si dos intentos llevaron el mismo.

Cada canal informa además de `deliveryHealth { lastSuccessAt lastFailureAt consecutiveFailures }`, leído del registro. `consecutiveFailures` cuenta los intentos fallidos y rechazados desde la última entrega correcta del canal. Los registros `NOT_ATTEMPTED` no cuentan contra el canal, porque describen algo que la plataforma no hizo, y tampoco un registro `FAILED` con `errorCode: BUDGET_SPENT`: el tiempo disponible para todas las entregas de la alarma se agotó mientras ese intento estaba en curso, así que fue la plataforma la que lo cortó. `deliveryHealth` es null para quien no tiene `notification:read`, por ejemplo un canal que una mutación devuelve a quien puede escribir pero no leer.

Dos propiedades a tener en cuenta:

- **El registro nunca retrasa una notificación.** Los registros se escriben después del intento y fuera de la ruta de entrega. Si la base de datos no puede aceptar un registro, este se descarta y se cuenta en `devicechain_notificationmanagement_ledger_writes_total{outcome="failed"}` (o `outcome="dropped"` cuando ya hay demasiadas escrituras en espera). Un registro demuestra que hubo un intento; la ausencia de un registro no demuestra que no lo hubo.
- **Los destinatarios son datos personales.** Los registros se eliminan pasados 7 días (la misma ventana que el estado de notificación resuelto), o pasado `AttemptRetentionSeconds` en la configuración de notification-management. También se borran con el inquilino cuando este se elimina.
- **Calcula el tamaño antes de ampliar la retención.** El registro guarda un registro por intento: aproximadamente las transiciones de alarma notificadas, por los canales a los que llega cada una, por los intentos por canal, más los escalados. Un inquilino que avisa 10.000 veces al día por 3 canales mantiene unos 210.000 registros con los 7 días predeterminados, y cada registro lleva su lista de destinatarios.

## Verificar la ruta de extremo a extremo

1. **Crea un canal** (como arriba). Confirma `enabled: true` en el resultado, y que `hasSecret` es `true` para un canal SMTP con usuario o un webhook que declara `bearer` o `header`, y `false` para un webhook que declara `none`.
2. **Crea una política** cuyas reglas mapeen las severidades que te interesan a ese canal.
3. **Levanta una alarma real.** Dispara una regla de detección en un dispositivo de prueba (consulta [Procesamiento de Eventos y Alarmas](../concepts/event-processing.md)) y confirma que llega el correo o la llamada al webhook.
4. **Inspecciona el estado de entrega.** El servicio mantiene un registro de solo lectura por alarma de lo que ha hecho. Consulta `notificationStatesByAlarmToken(alarmTokens: [...])`, o busca con `notificationStates`. Revisa `firstNotifiedAt` y `notifyCount` y, una vez que la alarma ha permanecido sin reconocer más allá de la ventana de escalado, `escalationLevel`.
5. **Lee el registro de entregas.** `notificationAttempts(criteria: {pageNumber: 1, pageSize: 20, alarmToken: "..."})` lista cada intento de la alarma con su resultado. Un registro `FAILED` o `REFUSED` lleva `errorCode` y el `errorMessage` del proveedor; consulta [Registro de entregas](#delivery-ledger).

Reconocer o despejar la alarma detiene el escalado adicional. El registro de estado guarda `acknowledgedAt`/`clearedAt` junto con el historial de notificaciones. Si la alarma se despeja y su condición vuelve a darse, la plataforma reabre la misma alarma y su registro de estado empieza de nuevo para el nuevo ciclo: el escalado vuelve a funcionar hasta que ese ciclo se reconozca o se despeje.
