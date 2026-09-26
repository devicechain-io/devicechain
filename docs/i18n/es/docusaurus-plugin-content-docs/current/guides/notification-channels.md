---
sidebar_position: 6
title: Configuración de Canales de Notificación
---

# Configuración de Canales de Notificación

Las notificaciones llevan una **alarma** levantada la última milla, hasta una persona. Cuando las acciones de una regla de detección levantan una alarma, una **política** por inquilino la enruta por severidad a los **canales** que configures: correo electrónico por SMTP, o un webhook. Las políticas añaden limitación de frecuencia (throttling) y **escalado** de las alarmas no reconocidas.

Esta ruta de máquina a humano está deliberadamente separada de los **[conectores de salida](../concepts/outbound-connectors.md)** de máquina a máquina. Los conectores llevan payloads a *sistemas*; las notificaciones llevan alertas a *personas*, con destinatarios y enrutamiento por severidad. Para saber cómo se levantan las alarmas en primer lugar, consulta [Procesamiento de Eventos y Alarmas](../concepts/event-processing.md).

:::note Estado
Disponible. Gestionas los canales y las políticas a través de la API GraphQL de notification-management. La lectura requiere la autoridad `notification:read`; crear o modificar cualquier cosa requiere `notification:write`.
:::

## Canales

Un **canal** es un endpoint de entrega que configura tu inquilino: una instancia de un **tipo** de canal, más su configuración de conexión. Consulta `notificationChannelTypes` para ver los tipos que define la plataforma. Hoy `smtp` y `webhook` se entregan con adaptadores funcionales; el indicador `available` de cada tipo dice si su adaptador ya está disponible.

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

Si un canal llega a la entrega en ese estado de todos modos, por ejemplo uno guardado antes de que existiera `auth`, no se envía. La entrega se rechaza en su primer intento y no se reintenta, y el servicio de notificaciones registra el inquilino, el token del canal y el motivo. El rechazo se cuenta en `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`, sobre el que puedes crear una alerta.

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

Para un webhook entrante de Slack, usa `"auth":"none"` y omite `secret`.

## Políticas

Una **política** decide qué alarmas levantadas se entregan, a quién y a través de qué canales. Lleva un conjunto de **reglas**. Cada regla mapea:

- una `severity`: `CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, `INDETERMINATE`, o `"*"` para cualquiera;
- a un canal, nombrado por token;
- con un arreglo JSON de `recipients` que interpreta el adaptador: direcciones de correo para SMTP; puede estar vacío para un webhook.

Aquí la severidad va en **mayúsculas** porque es la severidad de la *alarma*. La severidad de autoría de una regla de detección va en minúsculas (`major`) y se convierte a mayúsculas al levantar la alarma, así que una regla de notificación siempre coincide con la forma en mayúsculas. Una regla cuya severidad no sea uno de esos valores (un `major` en minúsculas, por ejemplo) se **rechaza al escribir**, en lugar de almacenarse como una regla que nunca podría coincidir.

:::caution Las políticas son de alcance para todo el inquilino
`deviceTypeToken` **todavía no se respeta**, y una política que lo establece se rechaza al escribir. Deja `deviceTypeToken` sin establecer. Consulta [Acotamiento por tipo de dispositivo](#device-type-scoping) para saber por qué.
:::

### Acotamiento por tipo de dispositivo {#device-type-scoping}

Acotar una política a un tipo de dispositivo necesita una búsqueda entre servicios que vaya del originador de la alarma a su tipo de dispositivo, y eso aún no ha llegado. Hasta entonces, el despachador omite una política acotada en lugar de aplicarla a todo el inquilino y notificar de más. Rechazar la escritura es deliberado: una política que aceptara el campo devolvería éxito y luego no entregaría nada en absoluto.

### Limitación de frecuencia y escalado {#throttling-and-escalation}

Dos ajustes más moldean la entrega:

- **`throttleSeconds`** es el intervalo mínimo entre notificaciones para la *misma* alarma, de modo que una condición intermitente (flapping) no inunde un canal. `null` significa sin limitación de frecuencia.
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

## Verificar la ruta de extremo a extremo

1. **Crea un canal** (como arriba). Confirma `enabled: true` en el resultado, y que `hasSecret` es `true` para un canal SMTP con usuario o un webhook que declara `bearer` o `header`, y `false` para un webhook que declara `none`.
2. **Crea una política** cuyas reglas mapeen las severidades que te interesan a ese canal.
3. **Levanta una alarma real.** Dispara una regla de detección en un dispositivo de prueba (consulta [Procesamiento de Eventos y Alarmas](../concepts/event-processing.md)) y confirma que llega el correo o la llamada al webhook.
4. **Inspecciona el estado de entrega.** El servicio mantiene un registro de solo lectura por alarma de lo que ha hecho. Consulta `notificationStatesByAlarmToken(alarmTokens: [...])`, o busca con `notificationStates`. Revisa `firstNotifiedAt` y `notifyCount` y, una vez que la alarma ha permanecido sin reconocer más allá de la ventana de escalado, `escalationLevel`.

Reconocer o despejar la alarma detiene el escalado adicional. El registro de estado guarda `acknowledgedAt`/`clearedAt` junto con el historial de notificaciones.
