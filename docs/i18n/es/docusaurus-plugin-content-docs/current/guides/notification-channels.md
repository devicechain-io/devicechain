---
sidebar_position: 5
title: Configuración de Canales de Notificación
---

# Configuración de Canales de Notificación

Cuando la etapa REACT de una regla de detección levanta una **alarma**, el subsistema de notificaciones la lleva la última milla — hasta una **persona**. Una **política** por inquilino enruta las alarmas por severidad a **canales** configurados (correo electrónico por SMTP, o un webhook), con limitación de frecuencia (throttling) y **escalado** de alarmas no reconocidas. Esta ruta de máquina a humano está deliberadamente separada de los **[conectores de salida](../concepts/outbound-connectors.md)** de máquina a máquina: los conectores llevan payloads a *sistemas*; las notificaciones llevan alertas a *personas*, con destinatarios y enrutamiento por severidad. Consulta [Procesamiento de Eventos y Alarmas](../concepts/event-processing.md) para saber cómo se levantan las alarmas en primer lugar.

:::note Estado
Disponible. Los canales y las políticas se gestionan a través de la API GraphQL de notification-management. La lectura requiere la autoridad `notification:read`; crear o modificar cualquier cosa requiere `notification:write`.
:::

## Canales

Un **canal** es un endpoint de entrega configurado por el inquilino: una instancia de un **tipo** de canal con su configuración de conexión. Consulta `notificationChannelTypes` para ver los tipos que define la plataforma — hoy `smtp` y `webhook` se entregan con adaptadores funcionales (el indicador `available` de cada tipo dice si su adaptador ya está disponible).

Un canal divide su configuración en dos:

- **`config`** — la configuración de conexión no secreta, como un documento JSON (host/puerto/from de SMTP; URL/método/encabezados del webhook).
- **`secret`** — la credencial (la contraseña de SMTP, un token de autenticación del webhook). Se almacena en el **almacén de secretos** cifrado por sobre (envelope-encrypted) de la plataforma y es de **solo escritura**: la envías al crear, y nunca se devuelve al leer. El canal solo expone un booleano `hasSecret`.

En una **actualización**, un secreto `null` deja el secreto existente sin cambios (nunca necesitas reenviarlo), un valor no nulo lo reemplaza, y una cadena vacía lo borra.

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

Un canal webhook realiza un POST de la notificación renderizada a una URL. Créalo de la misma manera con `channelType: "webhook"` y una configuración que lleve la `url` (opcionalmente `method` y encabezados adicionales en `headers`). Su secreto se presenta como `Authorization: Bearer <secret>` de forma predeterminada; configura `authHeader`/`authScheme` en la configuración para usar en su lugar un encabezado personalizado.

## Políticas

Una **política** decide qué alarmas levantadas se entregan, a quién y a través de qué canales. Lleva un conjunto de **reglas** — cada una mapea una `severity` (`CRITICAL`, `MAJOR`, `MINOR`, `WARNING`, `INDETERMINATE`, o `"*"` para cualquiera) a un canal (nombrado por token) y un arreglo JSON de `recipients` que interpreta el adaptador (direcciones de correo para SMTP; puede estar vacío para un webhook).

Ten en cuenta que aquí la severidad va en **mayúsculas**, porque es la severidad de la *alarma*. La severidad de autoría de una regla de detección va en minúsculas (`major`), y se convierte a mayúsculas al levantar la alarma — así que una regla de notificación siempre coincide con la forma en mayúsculas.

:::caution Las políticas son de alcance para todo el inquilino
`deviceTypeToken` **todavía no se respeta**, y una política que lo establece se **rechaza al escribir**. El acotamiento necesita una búsqueda entre servicios que vaya del originador de la alarma a su tipo de dispositivo, y eso aún no ha llegado; hasta entonces el despachador omite una política acotada en lugar de aplicarla a todo el inquilino y notificar de más. Rechazar la escritura es deliberado — una política que aceptara el campo devolvería éxito y luego no entregaría nada. Deja `deviceTypeToken` sin establecer.
:::

Dos ajustes adicionales moldean la entrega:

- **`throttleSeconds`** — el intervalo mínimo entre notificaciones para la *misma* alarma, de modo que una condición intermitente (flapping) no inunde un canal (`null` = sin limitación de frecuencia).
- **`escalateAfterSeconds`** + **`maxEscalations`** — cuando está configurado (> 0), una alarma que permanece **no reconocida y sin despejar (uncleared)** durante ese tiempo desde su última notificación se vuelve a notificar, hasta el tope. El escalado se ejecuta en un planificador (scheduler) seguro para alta disponibilidad (HA-safe), y cada alarma tiene **un único reloj y nivel de escalado compartido**: si varias políticas de escalado coinciden, la ventana más corta fija la cadencia y cada tope cuenta contra el nivel compartido. Un `escalateAfterSeconds` con valor `null`/`0` deshabilita el escalado para la política.

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

En una actualización, las `rules` de la solicitud **reemplazan** el conjunto de reglas existente de la política. Nombrar un token de canal desconocido hace fallar toda la escritura.

## Verificar la ruta de extremo a extremo

1. **Crea un canal** (como arriba) y confirma `hasSecret: true` y `enabled: true` en el resultado.
2. **Crea una política** cuyas reglas mapeen las severidades que te interesan a ese canal.
3. **Levanta una alarma real** — dispara una regla de detección en un dispositivo de prueba (ver [Procesamiento de Eventos y Alarmas](../concepts/event-processing.md)) y confirma que llega la llamada de correo o de webhook.
4. **Inspecciona el estado de entrega** — el servicio mantiene un registro de solo lectura por alarma de lo que ha hecho. Consulta `notificationStatesByAlarmToken(alarmTokens: [...])` (o busca con `notificationStates`) y revisa `firstNotifiedAt`, `notifyCount` y — una vez que la alarma ha permanecido sin reconocer más allá de la ventana de escalado — `escalationLevel`.

Reconocer o despejar la alarma detiene el escalado adicional; el registro de estado guarda `acknowledgedAt`/`clearedAt` junto con el historial de notificaciones.
