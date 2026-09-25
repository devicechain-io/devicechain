---
sidebar_position: 3
title: Enviar un comando
---

# Enviar un comando

Esta guía cubre la mitad del operador en el envío bidireccional de comandos: emites un
comando, distingues uno aceptado de uno rechazado y lo sigues hasta su desenlace. La mitad
del dispositivo — recibir un comando e informar qué ocurrió — está en
[Conectar un dispositivo](./connecting-a-device.md#responding-to-a-command). El ciclo de vida
por el que ambas mitades mueven un comando está en [Comandos](../concepts/commands.md).

Emites, lees y cancelas comandos en el endpoint de `command-delivery`,
`https://<tu-host>/api/command-delivery/graphql`, con un token de acceso de inquilino. Emitir
y cancelar requieren la autoridad `command:write`. Leer el historial de comandos requiere
`command:read`.

El primer paso de abajo es la única excepción. Averiguar qué acepta un dispositivo es una
consulta de `device-management`. Usa otro endpoint,
`https://<tu-host>/api/device-management/graphql`, y otra autoridad, `device:read`.

## Averigua qué acepta el dispositivo {#find-out-what-the-device-accepts}

El vocabulario de comandos de un dispositivo proviene de su perfil, así que pregúntale a
`device-management` en lugar de adivinar:

```graphql
query {
  deviceCommandVocabulary(deviceToken: "sensor-001") {
    constrained
    commands { commandKey name description parameterSchema }
  }
}
```

:::warning `commandKey` es el identificador; `name` es una etiqueta
Un `PublishedCommand` lleva ambos. La comprobación que acepta o rechaza un comando compara
contra `commandKey`, y ese es el valor que pones en el campo de `createCommand` — que,
confusamente, se llama `name`. El `name` de la entrada del vocabulario es una etiqueta
visible y no se compara con nada. Si envías la etiqueta, obtienes `COMMAND_NOT_IN_VOCABULARY`
para un comando que el dispositivo claramente admite.
:::

Lee `constrained`, no la longitud de `commands`:

- **`constrained: false`** — la lista está vacía y se acepta *cualquier* clave de comando. La
  lista vacía no significa que el dispositivo no acepte nada; significa que su perfil no
  declara vocabulario.
- **`constrained: true`** — la clave debe coincidir exactamente con una de las entradas,
  incluidas mayúsculas y minúsculas. La carga útil se valida contra el esquema de parámetros
  de ese comando.

## Emite un comando {#issue-it}

```graphql
mutation {
  createCommand(request: {
    token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11",
    deviceToken: "sensor-001",
    name: "reboot",          # el commandKey, no el nombre visible
    payload: "{\"delaySeconds\":5}",
    expiresAt: "2026-08-15T00:00:00Z"
  }) {
    command { token status queuedTime }
    rejection { code reason }
  }
}
```

- `token` lo eliges tú. Lo usas para referirte al comando después.
- `payload` y `metadata` son **cadenas** JSON.
- `expiresAt` es opcional. Consulta [Fija un TTL con el que puedas
  vivir](#set-a-ttl-you-can-live-with).

Volver a emitir con un token ya en uso no crea un segundo comando; recibes el original sin
cambios. Eso hace seguro un reintento tras un fallo de red. Importa porque un comando es una
actuación física, y una respuesta perdida no debe reiniciar un dispositivo dos veces.

Esta repetición solo aplica a los comandos que **te pertenecen**. Si el token lo tiene un
comando que la plataforma acuñó para un lote, recibes `TOKEN_IN_USE` en su lugar. Entregarte
la actuación de otro dispositivo como si fuera tuya sería peor que decir que no.

## Cuando se rechaza una admisión {#when-an-enqueue-is-refused}

:::danger Revisa `rejection`, no solo si hay errores
`createCommand` devuelve **exactamente uno** de `command` o `rejection`. Una admisión
rechazada es una respuesta GraphQL exitosa que lleva un `rejection`, no un error de GraphQL.
Un cliente que solo revisa el arreglo `errors` lee un rechazo como un éxito e informa un
comando que nunca se creó.
:::

La distinción es deliberada. Un rechazo es un veredicto decidido: la petición está mal, y el
rechazo dice exactamente cómo. Un error de GraphQL significa que la plataforma no pudo
responder en absoluto. Un llamador automático que no puede distinguirlos reintenta un comando
permanentemente inválido hasta que su límite de reenvíos se agota, lo cual se ve idéntico a
una caída del servicio.

Ramifica según `code`, nunca según `reason`. La razón es prosa para una persona, y su
redacción puede cambiar.

| `code` | Significado | ¿Reintentar? |
|---|---|---|
| `HELD_CEILING_EXCEEDED` | El inquilino está en su límite de comandos **no entregados** — todo lo que sigue en `QUEUED`, `HELD` o `PARKED`, no solo lo retenido para dispositivos ausentes. | **Sí** — se libera conforme esos comandos salen |
| `DEVICE_NOT_FOUND` | No hay ningún dispositivo con ese token en este inquilino. | No |
| `COMMAND_NOT_IN_VOCABULARY` | El perfil restringe los comandos y esta clave no es uno de ellos. Revisa mayúsculas y minúsculas. | No |
| `PAYLOAD_SCHEMA_VIOLATION` | La carga útil incumplió el esquema de parámetros del comando — parámetro desconocido, tipo incorrecto, fuera de rango, o falta uno requerido. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | La cadena no es JSON válido. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` no es una marca de tiempo RFC3339. | No |
| `TOKEN_IN_USE` | El token lo tiene un comando que no te pertenece — en la práctica, uno que la plataforma acuñó para un lote. | No — elige otro token |
| `COMMAND_REJECTED` | Llegó un rechazo sin clasificación. | No |

La lista es abierta. Trata un código que no reconozcas como un rechazo que no puedes
clasificar, nunca como un éxito.

Solo `HELD_CEILING_EXCEEDED` es temporal. Cualquier otro código describe una petición que
estará igual de mal la próxima vez. Reintentarla desperdicia intentos y le oculta un defecto
real a quien podría corregirlo.

Un inquilino cuya flota está entera presente igualmente puede alcanzar el techo. El techo
acota el trabajo *no entregado*, y los comandos en cola cuentan mientras esperan el siguiente
ciclo de entrega. Consulta [Cuánta acumulación puede retener un
inquilino](../concepts/commands.md#held-command-ceiling).

## Síguelo hasta su desenlace {#follow-it-to-an-outcome}

**No hay suscripción** para comandos, así que consulta periódicamente. Obtén un comando
concreto por token:

```graphql
query {
  commandsByToken(tokens: ["6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11"]) {
    token status sentTime respondedTime responsePayload error
  }
}
```

O busca, filtrando por un estado con `status` o por un conjunto de estados con `statuses`:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    statuses: ["HELD", "PARKED", "SENT"]
  }) {
    results { token name status queuedTime }
    pagination { totalRecords }
  }
}
```

Usa `statuses` cuando te importa un conjunto. «Todo lo que sigue en vuelo para este
dispositivo» es:

- `HELD` — retenido porque el dispositivo está ausente.
- `PARKED` — publicado hacia un dispositivo que resultó no estar despierto.
- `SENT` — despachado y sin respuesta.

Una lista `statuses` vacía se ignora en lugar de no coincidir con nada.

Lo que te dice cada estado terminal está en
[Comandos](../concepts/commands.md#command-lifecycle). El par que conviene recordar: `EXPIRED`
significa que el comando nunca llegó a un dispositivo, y `TIMEOUT` significa que sí llegó. Una
racha de `EXPIRED` apunta al despacho; una racha de `TIMEOUT` apunta al dispositivo.

## Cancela un comando {#cancel-one}

```graphql
mutation {
  cancelCommand(token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11") { token status }
}
```

Cancelar es legal desde `QUEUED`, `HELD` y `PARKED` — los estados en los que la plataforma
todavía tiene el comando en su poder — y registra `CANCELLED`. Los casos útiles son un comando
retenido para un dispositivo ausente, o uno publicado hacia un dispositivo que resultó estar
dormido. Cualquiera de los dos puede anularse antes de que la plataforma lo entregue, que es
buena parte del sentido de retenerlo en lugar de lanzarlo a ciegas.

**Un comando `SENT` no se cancela.** Cancelar no revoca un comando ya despachado. Llevarlo a
`CANCELLED` no detendría ninguna actuación; solo haría que la plataforma descartara la
respuesta real del dispositivo cuando llegue. El dispositivo actuaría, la respuesta se
desvanecería y el registro diría que la operación se anuló. Por eso la llamada tiene éxito y
devuelve el comando sin cambios, todavía en `SENT`. La cancelación compite con la entrega, y
perder esa carrera es lo normal.

Cancelar un comando ya terminal tampoco es un error. Se devuelve sin cambios, con el estado
que hubiera alcanzado. Así que una cancelación que pierde la carrera contra una respuesta
parece una llamada exitosa que devolvió `SUCCESSFUL`.

En ambos casos, **revisa el `status` que recibes** en lugar de suponer que la cancelación
surtió efecto. Un token que no corresponde a ningún comando *sí* es un error.

`cancelCommandBatch` aplica exactamente este freno a una escritura de flota completa: se
cancelan los mismos estados, y se detiene en la misma línea, `SENT`. Consulta [Cancelar un
lote](../concepts/commands.md#cancelling-a-batch).

## Fija un TTL con el que puedas vivir {#set-a-ttl-you-can-live-with}

Todo comando lleva un TTL. Pasa `expiresAt` para fijarlo; si no, se aplica el valor
predeterminado de la plataforma: **siete días**.

Siete días es mucho tiempo para enterarte de que un comando falló. Si tus dispositivos no
informan desenlaces, un comando permanece en `SENT` toda la semana antes de que `TIMEOUT`
registre lo que ya sospechabas. Fija `expiresAt` con lo que signifique «todavía útil» para esa
actuación — un reinicio que no ha ocurrido en diez minutos ya no va a ocurrir.

## Comandar muchos dispositivos a la vez {#commanding-many-devices-at-once}

Todo lo anterior emite un comando para un dispositivo. Para enviar un solo comando a una flota
entera — nombrada explícitamente o resuelta a partir de un grupo de entidades — como una única
operación que puedes auditar y anular, consulta [Comandar una flota](./commanding-a-fleet.md).
Un comando de flota no es un bucle de esta mutación. Fija la membresía del grupo tal como
estaba en el momento de dispararse, registra qué dispositivos fueron rechazados y por qué, y se
cancela como una sola operación.

## Cinco operaciones que no son para ti {#operations-that-are-not-for-you}

`markCommandSent`, `confirmCommandDispatch`, `releaseHeldCommands` y `parkCommand` aparecen en
este esquema, pero están protegidas por autoridades de **nivel de sistema** que un token de
acceso de inquilino no lleva: `command:claim` para las dos primeras, y luego `command:wake` y
`command:park`. Existen para transportes que son dueños de la conexión de un dispositivo:

- un dispositivo LwM2M drenando su acumulación por la sesión que acaba de abrir
- un adaptador LwM2M confirmando que una entrega sigue vigente inmediatamente antes de llevarla
  a cabo
- un broker informando que un dispositivo regresó
- un transporte devolviendo un comando porque el dispositivo hacia el que se publicó resultó
  ser inalcanzable

Llamarlas desde una aplicación competiría con el propio proceso de entrega de la plataforma por
el control de una actuación física.

`drainableCommands` es la lectura que esos transportes hacen primero. Está protegida por
**`command:claim`**, la misma autoridad que `markCommandSent`, en lugar de una cuarta propia:
quien tiene derecho a reclamar los comandos de un dispositivo es precisamente quien tiene
derecho a averiguar cuáles hay para reclamar. Dado un token de dispositivo, devuelve los
comandos que siguen esperando a ese dispositivo — `HELD` y `PARKED`, menos todo lo que ya pasó
su horizonte de expiración — **del más antiguo al más reciente**, acotados por `limit`. Un
`limit` ausente o no positivo da 32, y 1000 es el techo.

El orden es la razón de ser de la consulta. La escritura de una actualización de firmware
tiene que llegar al dispositivo antes que su ejecución, así que una acumulación drenada en
cualquier otro orden no solo llega tarde — ejecuta el despliegue al revés.

`command:read` no abre esta consulta, y de todos modos una aplicación no la necesita. Para ver
qué tiene esperando un dispositivo, usa la [consulta `commands`](#follow-it-to-an-outcome) con
`statuses: ["HELD", "PARKED"]`.
