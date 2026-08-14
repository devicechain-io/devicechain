---
sidebar_position: 3
title: Enviar un comando
---

# Enviar un comando

Esta guía cubre la mitad del operador en el envío bidireccional de comandos: emitir un
comando, distinguir uno aceptado de uno rechazado, y seguirlo hasta su desenlace. La mitad
del dispositivo — recibir un comando e informar qué ocurrió — está en
[Conectar un dispositivo](./connecting-a-device.md#responding-to-a-command). El ciclo de
vida por el que ambas mitades mueven un comando está en
[Comandos](../concepts/commands.md).

Emitir, leer y cancelar ocurren en el endpoint de `command-delivery`,
`https://<tu-host>/api/command-delivery/graphql`, con un token de acceso de inquilino.
Emitir y cancelar requieren la autoridad **`command:write`**; leer el historial requiere
**`command:read`**.

La única excepción es el primer paso de abajo. Averiguar qué acepta un dispositivo es una
consulta de `device-management` — otro endpoint,
`https://<tu-host>/api/device-management/graphql`, y otra autoridad, **`device:read`**.

## Averigua qué acepta el dispositivo

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
Un `PublishedCommand` lleva ambos. La compuerta de admisión compara contra **`commandKey`**,
y ese es el valor que pones en el campo de `createCommand` — que, confusamente, se llama
`name`. El `name` de la entrada del vocabulario es una etiqueta legible para personas y no
se compara con nada. Si envías la etiqueta, obtienes `COMMAND_NOT_IN_VOCABULARY` para un
comando que el dispositivo claramente admite.
:::

**Lee `constrained`, no la longitud de `commands`.** Cuando `constrained` es `false` la
lista está vacía y se acepta *cualquier* clave de comando — una lista vacía no significa
que el dispositivo no acepte nada, significa que su perfil no declara vocabulario. Cuando
`constrained` es `true`, la clave debe coincidir exactamente con una de las entradas
—**incluidas mayúsculas y minúsculas**— y la carga útil se valida contra el esquema de
parámetros de ese comando.

## Emítelo

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

`token` lo eliges tú y es como te refieres al comando después. `payload` y `metadata` son
**cadenas** JSON. `expiresAt` es opcional — consulta [Fija un TTL con el que puedas
vivir](#fija-un-ttl-con-el-que-puedas-vivir).

Volver a emitir con un token ya en uso no crea un segundo comando: se devuelve el original
sin cambios. Eso hace segura una reintentona tras un fallo de red, lo cual importa, porque
un comando es una actuación física y no quieres que una respuesta perdida reinicie un
dispositivo dos veces.

## Cuando se rechaza una admisión {#when-an-enqueue-is-refused}

:::danger Revisa `rejection`, no solo si hay errores
`createCommand` devuelve **exactamente uno** de `command` o `rejection`. Una admisión
rechazada es una respuesta GraphQL exitosa que lleva un `rejection` — **no** un error de
GraphQL. Un cliente que solo revisa el arreglo `errors` lee un rechazo como un éxito e
informa un comando que nunca se creó.
:::

Un rechazo es un veredicto decidido, no un fallo, y la distinción es deliberada: un rechazo
dice que la petición está mal y describe exactamente cómo, mientras que un error de GraphQL
dice que la plataforma no pudo responder en absoluto. Un llamador automático que no puede
distinguirlos reintenta un comando permanentemente inválido hasta agotar su límite de
reenvíos — lo cual se ve idéntico a una caída del servicio.

**Ramifica según `code`. Nunca según `reason`** — la razón es prosa para una persona y su
redacción puede cambiar.

| `code` | Significado | ¿Reintentar? |
|---|---|---|
| `HELD_CEILING_EXCEEDED` | El inquilino está en su límite de comandos **no entregados** — todo lo que sigue en `QUEUED` o `HELD`, no solo lo retenido para dispositivos ausentes. | **Sí** — se libera conforme esos comandos salen |
| `DEVICE_NOT_FOUND` | No hay ningún dispositivo con ese token en este inquilino. | No |
| `COMMAND_NOT_IN_VOCABULARY` | El perfil restringe los comandos y esta clave no es uno. Revisa mayúsculas y minúsculas. | No |
| `PAYLOAD_SCHEMA_VIOLATION` | La carga útil incumplió el esquema de parámetros del comando — parámetro desconocido, tipo incorrecto, fuera de rango, o falta uno requerido. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | La cadena no es JSON bien formado. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` no es una marca de tiempo RFC3339. | No |
| `COMMAND_REJECTED` | Llegó un rechazo sin clasificación. | No |

**La lista es abierta.** Trata un código que no reconozcas como un rechazo que no puedes
clasificar — nunca como un éxito.

Solo `HELD_CEILING_EXCEEDED` es temporal. Cualquier otro código describe una petición que
estará igual de mal la próxima vez, así que reintentarla desperdicia intentos y le oculta un
defecto real a quien podría corregirlo.

Un inquilino cuya flota está entera y presente igualmente puede alcanzar el techo: acota el
trabajo *no entregado*, y los comandos en cola cuentan mientras esperan el siguiente ciclo de
entrega. Consulta [Cuánta acumulación puede retener un
inquilino](../concepts/commands.md#held-command-ceiling).

## Síguelo hasta su desenlace

**No hay suscripción** para comandos — hay que consultar periódicamente. Obtén uno concreto
por token:

```graphql
query {
  commandsByToken(tokens: ["6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11"]) {
    token status sentTime respondedTime responsePayload error
  }
}
```

O busca, filtrando por un estado con `status` o por un conjunto con `statuses`:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    statuses: ["HELD", "SENT"]
  }) {
    results { token name status queuedTime }
    pagination { totalRecords }
  }
}
```

`statuses` es el campo al que recurrir cuando lo que te importa es un conjunto: «todo lo
que sigue en vuelo para este dispositivo» son `HELD` más `SENT`, los comandos retenidos
para él y los ya despachados sin respuesta. Una lista vacía se ignora en lugar de no
coincidir con nada.

Lo que significa cada estado terminal está en
[Comandos](../concepts/commands.md#command-lifecycle); el par que conviene interiorizar es
que **`EXPIRED` significa que nunca salió de la plataforma y `TIMEOUT` que sí salió** — una
racha del primero apunta al despacho, una del segundo apunta al dispositivo.

## Cancela uno

```graphql
mutation {
  cancelCommand(token: "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11") { token status }
}
```

Es legal desde cualquier estado no terminal — `QUEUED`, `HELD` o `SENT`. Cancelar un comando
retenido es el caso útil: se retuvo para un dispositivo ausente y puede anularse antes de
que ese dispositivo llegue a verlo, que es buena parte del sentido de retenerlo en lugar de
lanzarlo a ciegas. Registra `CANCELLED`.

**Cancelar un comando ya terminal no es un error.** Se devuelve sin cambios, con el estado
que hubiera alcanzado. Así que una cancelación que pierde la carrera contra una respuesta
parece una llamada exitosa que devolvió `SUCCESSFUL` — revisa el `status` que recibes en
lugar de suponer que la cancelación surtió efecto. Un token que no corresponde a ningún
comando **sí** es un error.

Cancelar no revoca un comando ya despachado: `SENT` significa que ya está en el dispositivo.
La cancelación compite con la entrega, y perder esa carrera es lo normal.

## Fija un TTL con el que puedas vivir {#fija-un-ttl-con-el-que-puedas-vivir}

Todo comando lleva uno. Pasa `expiresAt` para fijarlo, o se aplica el valor predeterminado
de la plataforma: **siete días**.

Siete días es mucho tiempo para enterarte de que un comando falló. Si tus dispositivos no
informan desenlaces, un comando permanece en `SENT` toda la semana antes de que `TIMEOUT`
registre lo que ya sospechabas. Fija tu propio `expiresAt` con lo que signifique «todavía
útil» para esa actuación — un reinicio que no ha ocurrido en diez minutos ya no va a ocurrir.

## Dos mutaciones que no son para ti

`markCommandSent` y `releaseHeldCommands` aparecen en este esquema pero están protegidas por
autoridades de **nivel de sistema** (`command:claim` y `command:wake`) que un token de acceso
de inquilino no lleva. Existen para transportes que son dueños de la conexión de un
dispositivo — un dispositivo LwM2M drenando su acumulación por la sesión que acaba de abrir,
o un broker informando que un dispositivo regresó — y llamarlas desde una aplicación
competiría con el barrido de entrega por el control de una actuación física.
