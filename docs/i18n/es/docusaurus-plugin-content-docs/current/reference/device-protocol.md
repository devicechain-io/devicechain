---
sidebar_position: 5
title: Referencia del protocolo de dispositivo
---

# Referencia del protocolo de dispositivo {#device-protocol-reference}

Esta página es la referencia de cada mensaje que un dispositivo intercambia con DeviceChain por MQTT y HTTP: el evento que envía el dispositivo, el comando que recibe y la respuesta que devuelve. Enumera cada campo, qué ocurre con un mensaje que la plataforma rechaza y dónde se puede ver ese rechazo. Para un recorrido guiado, empieza por [Conexión de un dispositivo](../guides/connecting-a-device.md).

Los dispositivos LwM2M y Sparkplug B no usan estos mensajes. Sus pasarelas de protocolo los traducen a la forma interna de la plataforma; consulta [LwM2M](../concepts/lwm2m.md) y [Sparkplug B](../concepts/sparkplug.md).

## Esquemas legibles por máquina {#schemas}

Cada mensaje de esta página se publica como un esquema JSON (draft 2020-12). Úsalos para validar lo que emite el firmware o para generar tipos:

| Esquema | Describe |
| --- | --- |
| [`device-event.schema.json`](pathname:///schema/device/device-event.schema.json) | El [sobre del evento](#device-event) que envía un dispositivo |
| [`measurement-payload.schema.json`](pathname:///schema/device/measurement-payload.schema.json) | El payload de un evento [`Measurement`](#measurement-payload) |
| [`location-payload.schema.json`](pathname:///schema/device/location-payload.schema.json) | El payload de un evento [`Location`](#location-payload) |
| [`alert-payload.schema.json`](pathname:///schema/device/alert-payload.schema.json) | El payload de un evento [`Alert`](#alert-payload) |
| [`new-relationship-payload.schema.json`](pathname:///schema/device/new-relationship-payload.schema.json) | El payload de un evento [`NewRelationship`](#new-relationship-payload) |
| [`command-delivery.schema.json`](pathname:///schema/device/command-delivery.schema.json) | El [comando](#command-delivery) que la plataforma entrega a un dispositivo |
| [`command-response.schema.json`](pathname:///schema/device/command-response.schema.json) | La [respuesta](#command-response) que devuelve un dispositivo |

La misma lista, con URL absolutas, está bajo `deviceProtocol` en [`/schema/index.json`](pathname:///schema/index.json). El esquema del sobre hace referencia a los esquemas de payload mediante URL relativas, así que un validador que lo cargue por URL los resuelve por sí solo.

Los esquemas se guardan en el repositorio junto al código que decodifica estos mensajes, y una prueba hace fallar la compilación de la plataforma si un campo se renombra, se añade o se elimina en un lado y no en el otro.

Lo que un esquema no puede decir:

- **Los miembros desconocidos se ignoran, no se rechazan.** Un campo opcional mal escrito (`altID` en lugar de `altId` funciona; `alt_id` no) se descarta sin aviso en vez de rechazarse. Un validador al que añadas localmente `additionalProperties: false` lo detecta en las pruebas.
- **"Obligatorio" describe lo que un dispositivo debe enviar, no todo lo que la plataforma rechaza en la entrada.** La mayoría de los campos obligatorios se hacen cumplir, como enumeran las [tablas de rechazos](#rejections). Estos no:
  - `device` puede faltar cuando una credencial autentica el evento; el evento se atribuye al dispositivo de la credencial.
  - Un evento `NewRelationship` sin `payload`, o sin `relationshipType`, `targetType` o `target`, se acepta (HTTP `202`): cada valor ausente se lee como un string vacío, y después el evento falla en la resolución y pasa a dead-letter con el motivo `ApiCallFailed`.
  - En una respuesta a comando, un `success` ausente se lee como `false` y cierra el comando como `FAILED`; un `commandToken` ausente no corresponde a ningún comando y pasa a dead-letter con el motivo `exhausted` tras sus reintentos.

## Versionado {#versioning}

Los mensajes no llevan ningún campo de versión. El contrato es el que describen los esquemas publicados con la documentación de tu versión, y un esquema cambia en la misma versión que el código que decodifica su mensaje. Antes de v1.0.0, una versión puede cambiar el contrato, así que compara los esquemas entre la versión que ejecutas y aquella a la que vas a pasar.

## Topics y endpoints {#topics}

El broker de la plataforma es el servidor MQTT integrado en NATS. Un topic MQTT y el subject NATS que lee la plataforma son el mismo nombre, con `/` escrito como `.`:

| Mensaje | Dirección | Topic MQTT | Subject NATS |
| --- | --- | --- | --- |
| [Evento](#device-event) | dispositivo → plataforma | `{instanceId}/{tenant}/devices/{deviceToken}/events` | `{instanceId}.{tenant}.devices.{deviceToken}.events` |
| [Entrega de comando](#command-delivery) | plataforma → dispositivo | `{instanceId}/{tenant}/device-commands/{deviceToken}` | `{instanceId}.{tenant}.device-commands.{deviceToken}` |
| [Respuesta a comando](#command-response) | dispositivo → plataforma | `{instanceId}/{tenant}/command-responses/{deviceToken}` | `{instanceId}.{tenant}.command-responses.{deviceToken}` |

- Un dispositivo solo está autorizado a publicar y suscribirse en los topics que llevan **su propio** token. No puede leer los comandos de otro dispositivo ni publicar como otro dispositivo.
- **Los dispositivos se conectan por MQTT, no por NATS directo.** Una credencial de dispositivo autoriza solo una conexión MQTT: un cliente NATS simple que presente una se rechaza al conectar. La columna de subjects es lo que ve un operador en las herramientas de NATS, no una segunda vía de entrada. La configuración de la conexión (el client id, el usuario `{tenant}:{credentialId}`, TLS) está en [Ajustes de conexión](../guides/connecting-a-device.md#connection-settings).
- Publica los eventos con QoS 0, o con QoS 1 y un [`altId`](#device-event). Las publicaciones QoS 2 se rechazan por defecto y el broker cierra la conexión; consulta [Calidad de servicio](../guides/connecting-a-device.md#quality-of-service).

Por **HTTP**, un dispositivo solo puede enviar eventos. Haz `POST` del mismo [cuerpo de evento](#device-event) a `/{instanceId}/{tenant}/events` en el servicio `event-sources` (puerto 8081). No hay canal de bajada por HTTP: un dispositivo que solo usa HTTP no puede recibir comandos.

## Evento de dispositivo {#device-event}

Un objeto JSON por mensaje MQTT o petición HTTP. Un mensaje es un evento.

| Campo | Tipo | Obligatorio | Significado | Ejemplo |
| --- | --- | --- | --- | --- |
| `device` | string | sí¹ | El token del dispositivo que envía el evento. En MQTT debe coincidir con el `{deviceToken}` del topic. Cuando una credencial autentica el evento, debe nombrar al dispositivo de esa credencial. | `"sensor-001"` |
| `eventType` | string | sí | `Measurement`, `Location`, `Alert` o `NewRelationship`, distinguiendo mayúsculas. Selecciona la forma del payload. | `"Measurement"` |
| `payload` | object | sí | El contenido del evento. Su forma depende de `eventType`; consulta [Payloads](#payloads). | `{"entries":[…]}` |
| `occurredTime` | string, RFC 3339 | no | Cuándo ocurrió el evento. Si se omite, el evento se fecha cuando la plataforma recibió el mensaje. | `"2026-08-09T12:00:00.125Z"` |
| `altId` | string | no | Una clave de idempotencia elegida por el dispositivo: un evento reentregado con el mismo `altId` **y** el mismo `occurredTime` del sobre se omite. Hoy la coincidencia es por tenant, no por dispositivo, lo cual es una limitación conocida; consulta [más abajo](#altid). | `"sensor-001-4417"` |
| `relationship` | string | no | Se acepta, se transporta por la cadena de proceso y **no se usa**. La plataforma registra en el evento todas las relaciones con seguimiento del dispositivo, diga lo que diga este campo. No dependas de él. | — |
| `credentialType` | string | ver abajo | `ACCESS_TOKEN` o `MQTT_BASIC`. Junto con `credentialId`, autentica el evento. | `"ACCESS_TOKEN"` |
| `credentialId` | string | ver abajo | Para `ACCESS_TOKEN`, el propio token. Para `MQTT_BASIC`, el nombre de usuario, sin el prefijo `{tenant}:` que usa la conexión MQTT. | `"5f98…98b2"` |
| `credentialSecret` | string | solo `MQTT_BASIC` | La contraseña de `MQTT_BASIC`. | |

¹ Envía `device` en cada evento. En sentido estricto, la plataforma tolera su ausencia cuando una credencial autentica el evento, y entonces atribuye el evento al dispositivo de la credencial.

**La credencial.** El modo de autenticación de dispositivos por defecto es `required`: un evento sin credencial se rechaza (consulta la [tabla de rechazos](#rejections)). Omite la credencial solo en una instancia configurada como `optional` o `disabled`. Una credencial solo se lee cuando están presentes `credentialType` y un `credentialId` no vacío. Consulta [Credenciales de dispositivo](../guides/device-credentials.md).

**Marcas de tiempo.** Todo `occurredTime`, en el sobre o en una entrada, es RFC 3339. La plataforma rechaza uno que no lo sea, uno igual a `0001-01-01T00:00:00Z` (reservado para indicar "no se informó ninguna hora") y uno anterior en más de **366 días** al momento en que la plataforma recibió el mensaje. Una hora muy por delante del reloj de la plataforma se almacena en un tope en lugar de rechazarse; consulta [Relojes adelantados](../guides/connecting-a-device.md#clocks-that-run-ahead).

### `altId` y duplicados {#altid}

La entrega al menos una vez (MQTT QoS 1, un reintento HTTP tras un `503`) puede entregar un evento dos veces. Sin `altId`, se almacenan las dos copias. Con él, la segunda se omite:

- La coincidencia es sobre `altId` **y** el `occurredTime` del sobre a la vez. El `occurredTime` de una entrada no cuenta.
- Envía un `occurredTime` en el sobre. Sin él, cada copia se fecha al llegar, las dos horas difieren y se almacenan ambas.
- Hoy la coincidencia se hace por tenant y no por dispositivo; consulta la limitación más abajo.
- Un segundo evento con el mismo `altId` y `occurredTime` se omite aunque su contenido sea distinto.

:::caution Limitación conocida
Hoy los duplicados se detectan por **tenant** sobre (`altId`, `occurredTime`), no por dispositivo, así que dos dispositivos que envían el mismo `altId` para el mismo instante colisionan, y uno de los eventos se omite. Es un defecto, y se está preparando una corrección. Hasta que llegue, haz que el valor sea único en toda la flota, por ejemplo anteponiéndole el token del dispositivo.
:::

### Payloads {#payloads}

Los payloads `Measurement`, `Location` y `Alert` envuelven su contenido en un array `entries`. Una entrada es una lectura en un instante, y cada entrada puede llevar su propio `occurredTime`; una entrada sin él toma el del sobre. Un payload sin entradas, o con su contenido colocado directamente bajo `payload`, se rechaza.

Un evento lleva **como máximo 256 lecturas**, en ambos transportes. Una lectura es una clave de medición, o una entrada de ubicación o de alerta. Un mensaje que supera el límite se rechaza entero, nunca se recorta.

#### Measurement {#measurement-payload}

| Campo | Tipo | Obligatorio | Significado | Ejemplo |
| --- | --- | --- | --- | --- |
| `entries` | array | sí | Las muestras. Al menos una. | |
| `entries[].measurements` | objeto de string → **string** | sí | Nombre de métrica a valor. Cada valor es un **string** JSON: `"21.5"`, no `21.5`. Un número sin comillas hace fallar todo el mensaje. Al menos una métrica. | `{"temperature":"21.5"}` |
| `entries[].occurredTime` | string, RFC 3339 | no | El instante de esta muestra. | |

#### Location {#location-payload}

Cada campo de ubicación es un **string** JSON, incluidos los numéricos. Cada uno debe interpretarse como un número finito dentro de su rango.

| Campo | Tipo | Obligatorio | Significado | Rango |
| --- | --- | --- | --- | --- |
| `entries` | array | sí | Las posiciones. Al menos una. | |
| `entries[].latitude` | string | sí | Grados decimales WGS84 | −90 a 90 |
| `entries[].longitude` | string | sí | Grados decimales WGS84 | −180 a 180 |
| `entries[].elevation` | string | no | Metros sobre el **elipsoide** WGS84, no sobre el nivel medio del mar | magnitud ≤ 99999999 |
| `entries[].accuracy` | string | no | Precisión horizontal, metros | 0 a 99999999 |
| `entries[].speed` | string | no | Metros por segundo | 0 a 99999999 |
| `entries[].heading` | string | no | Grados en sentido horario desde el norte verdadero | desde 0 hasta 360 sin incluirlo; 359.99995 o más se rechaza |
| `entries[].occurredTime` | string, RFC 3339 | no | El instante de esta posición | |

#### Alert {#alert-payload}

| Campo | Tipo | Obligatorio | Significado | Ejemplo |
| --- | --- | --- | --- | --- |
| `entries` | array | sí | Las alertas. Al menos una. | |
| `entries[].type` | string | sí | El clasificador por el que enrutan las políticas de notificación, las reglas y los filtros de la consola. Vacío se rechaza. | `"overheat"` |
| `entries[].level` | **integer** | no | Gravedad, un entero JSON sin comillas de 0 a 2147483647. Un nivel entre comillas hace fallar todo el mensaje. Si se omite, es 0. | `5` |
| `entries[].message` | string | no | Texto legible. | `"coolant over limit"` |
| `entries[].source` | string | no | Qué parte del dispositivo la generó. | `"ecu"` |
| `entries[].occurredTime` | string, RFC 3339 | no | El instante de esta alerta. | |

#### NewRelationship {#new-relationship-payload}

Crea una relación desde el dispositivo emisor hacia otra entidad del mismo tenant. Este payload no tiene array `entries`, y sus tres claves se leen por su nombre exacto, así que las mayúsculas importan. Una clave ausente no se rechaza al decodificar el mensaje: se lee como un string vacío, y el evento falla después en la resolución, como en los casos siguientes.

| Campo | Tipo | Obligatorio | Significado | Ejemplo |
| --- | --- | --- | --- | --- |
| `relationshipType` | string | sí | El token del tipo de relación: uno definido en el tenant, o uno de los tipos reservados de la plataforma, que se crean en su primer uso. | `"member"` |
| `targetType` | string | sí | `device`, `asset`, `area`, `customer` o `group`. | `"group"` |
| `target` | string | sí | El token de la entidad de destino. | `"building-7-sensors"` |

Una relación que la plataforma no puede crear (un tipo de destino desconocido, un destino que no existe, un tipo de relación desconocido) no se crea. Como cualquier evento que no se puede resolver, se reintenta y después pasa a dead-letter.

## Entrega de comando {#command-delivery}

Lo que un dispositivo recibe en su topic `device-commands`, una vez por cada despacho de un comando. La entrega es **solo en vivo**: un dispositivo que no está conectado y suscrito cuando se publica el comando no lo recibe, y la plataforma no se entera en ningún caso. Consulta [Recepción de comandos](../guides/connecting-a-device.md#receiving-commands).

| Campo | Tipo | Siempre presente | Significado | Ejemplo |
| --- | --- | --- | --- | --- |
| `token` | string | sí | El token **del comando**. Identifica el comando, no el dispositivo. Devuélvelo como `commandToken`. | `"6f1c0f8e-…"` |
| `deviceToken` | string | sí | El dispositivo al que va dirigido el comando: siempre el dispositivo en cuyo topic llegó. | `"sensor-001"` |
| `name` | string | sí | La clave del comando. Cuando el perfil del dispositivo declara un vocabulario de comandos, uno de sus comandos publicados. | `"reboot"` |
| `dispatchNonce` | string | sí | Nombra este despacho del comando. Es opaco: no lo interpretes. Repítelo en la respuesta. Si el mismo comando vuelve a llegar, responde con el nonce de la entrega más reciente. | `"0f6f4a2c-…"` |
| `payload` | cualquier valor JSON | no | Los parámetros del comando, tal como se emitieron. Ya validados contra el esquema de parámetros del comando cuando el perfil declara uno. Ausente cuando el comando se emitió sin parámetros. | `{"delaySeconds":5}` |

## Respuesta a comando {#command-response}

Lo que un dispositivo publica en su topic `command-responses` para cerrar un comando. La plataforma toma el dispositivo que responde del **topic**, nunca del cuerpo.

| Campo | Tipo | Obligatorio | Significado | Ejemplo |
| --- | --- | --- | --- | --- |
| `commandToken` | string | sí | El `token` de la entrega a la que se responde: el token del comando, no el del dispositivo. | `"6f1c0f8e-…"` |
| `dispatchNonce` | string | sí | El `dispatchNonce` de la entrega a la que se responde. | `"0f6f4a2c-…"` |
| `success` | boolean | sí | `true` cierra el comando como `SUCCESSFUL`; `false`, como `FAILED`. Si se omite, se lee como `false`. | `true` |
| `payload` | cualquier valor JSON | no | Los datos de resultado del comando, almacenados con el comando y devueltos por la API. Un string se almacena como su texto; un objeto, array, número o booleano se almacena como ese valor JSON, tal cual se envió, de modo que un dispositivo puede devolver datos estructurados directamente. Si se omite o es `null`, el comando no tiene payload de respuesta. | `"rebooting in 5s"` o `{"level":3}` |
| `error` | string | no | Por qué falló el comando. Solo se almacena cuando `success` es `false`; se ignora cuando es `true`. | `"actuator jammed"` |

```json
{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":false,"error":"actuator jammed"}
```

## Rechazos y errores {#rejections}

Lo que le ocurre a un mensaje que la plataforma no acepta depende de dónde se rechaza y del transporte. HTTP responde a la petición, así que el dispositivo se entera de un rechazo en la entrada. MQTT confirma una publicación (`PUBACK`) en cuanto el broker la ha capturado, **antes** de decodificarla, así que a un dispositivo MQTT nunca se le avisa: todo rechazo posterior al broker solo es visible para un operador.

### En la entrada {#rejections-at-the-door}

Lo comprueba `event-sources` al recibir y decodificar el mensaje.

| Condición | HTTP | MQTT | Dónde lo ve un operador |
| --- | --- | --- | --- |
| El id de instancia de la ruta o del topic no es el de esta instancia | `404` | El broker rechaza la publicación: el dispositivo no está autorizado en ese topic | — |
| El tenant de la ruta no es un token válido | `400` | El broker rechaza la publicación | — |
| El tenant supera su límite de ingesta, por mensajes o por lecturas | `429` con `Retry-After: 1` | Se confirma y después **se descarta** | `total_msg_rate_limited`, `total_msg_reading_limited` |
| El cuerpo no se puede leer o supera 1 MiB | `400` | — | — |
| El cuerpo no se decodifica: no es JSON, un `eventType` desconocido o exclusivo de la plataforma (`StateChange`, `CommandInvocation`, `CommandResponse`), un valor del tipo JSON equivocado, ninguna entrada, una entrada vacía, un campo de ubicación ausente o fuera de rango, una alerta sin `type` o con un `level` mayor que 2147483647 | `400`, con el motivo en el cuerpo | Se confirma y después se envía al stream `failed-decode` | `total_msg_failed_decode` |
| Una marca de tiempo que no es RFC 3339, es `0001-01-01T00:00:00Z` o tiene más de 366 días | `400`, nombrando el campo | Se envía a `failed-decode` | `total_msg_failed_decode`, `total_msg_invalid_event_time` |
| Más de 256 lecturas | `400`, indicando el número y el límite | Se envía a `failed-decode` | `total_msg_failed_decode`, `total_msg_too_many_readings` |
| El `device` del cuerpo no es el dispositivo del topic | — | Se envía a `failed-decode` | `total_msg_failed_decode` |
| La plataforma está aplicando [contrapresión](../deployment/observability.md#ingest-backpressure) | `503` **con** `Retry-After`: no se almacenó, envíalo de nuevo | No se rechaza: espera en el stream de captura; los mensajes capturados más antiguos solo se descartan si ese stream se llena | |
| El evento no se pudo entregar a la cadena de proceso | `503` **sin** `Retry-After`: puede haberse almacenado, así que reenviarlo lo almacena dos veces salvo que lleve un [`altId`](#altid) | — | |
| Aceptado | `202` | `PUBACK` (QoS 1) | |

Un `400` es definitivo: la misma petición recibe la misma respuesta. Un rechazo en la entrada rechaza el **mensaje entero**, incluidas todas las demás entradas que contenga.

### Después de la aceptación {#rejections-after-acceptance}

Un evento aceptado (`202`, o capturado por el broker) lo resuelve después `device-management`, que autentica el dispositivo y le asocia el evento. Estos rechazos son iguales en ambos transportes, y a ningún dispositivo se le avisa. El evento se reintenta y después se registra en el stream `failed-events` con uno de estos motivos:

| Motivo | Causa |
| --- | --- |
| `Unauthenticated` | Ninguna credencial mientras la autenticación de dispositivos es `required`; una credencial que no autentica; o un `device` en el cuerpo que no es el dispositivo de la credencial. |
| `DeviceNotFound` | No se usó ninguna credencial y el token `device` no está registrado en el tenant. |
| `Invalid` | El evento tiene más de 366 días, o no se pudo interpretar para su tipo. |
| `ApiCallFailed` | Falló una consulta o escritura que la resolución necesitaba, incluido un [`NewRelationship`](#new-relationship-payload) que la plataforma no pudo crear. |

Un evento rechazado se reintenta hasta su quinta entrega, de modo que si entretanto se registra el dispositivo o se recupera la base de datos, el evento se almacena igualmente. La excepción es un evento demasiado antiguo para almacenarse, que se registra en su primera entrega, ya que ningún reintento puede cambiar su antigüedad.

### Respuestas a comandos {#rejections-command-responses}

Una respuesta a comando nunca recibe contestación, en ningún transporte. Lo que le ocurre:

| Respuesta | Resultado |
| --- | --- |
| Corresponde a un comando del dispositivo, con el `dispatchNonce` de su despacho actual | El comando se cierra como `SUCCESSFUL` o `FAILED`. |
| Responde a un comando ya terminado | Se ignora; el comando conserva su resultado. |
| No es decodificable: no es JSON, o un campo tiene el tipo equivocado, como un `success` entre comillas | No se resuelve; se registra en el stream `dead-letters` con el motivo `unprocessable`, indicando el dispositivo que respondió. El comando sigue en `SENT` hasta que vence, salvo que el dispositivo vuelva a responder correctamente. |
| Sin `dispatchNonce` | No se cierra; se registra en el stream `dead-letters` con el motivo `unprocessable`. |
| Un `dispatchNonce` de un despacho que el comando ya dejó atrás | No se cierra; pasa a dead-letter con el motivo `unprocessable`. |
| Un `commandToken` que no corresponde a ningún comando (casi siempre el propio token del dispositivo enviado por error) | Se reintenta hasta la quinta entrega y después pasa a dead-letter con el motivo `exhausted`. |
| Un comando que pertenece a otro dispositivo | Se rechaza: se registra en el log y se cuenta, no se asocia al comando y no pasa a dead-letter. |

Un comando que nadie responde sigue en `SENT` hasta que su caducidad lo convierte en `TIMEOUT`. Consulta [Por qué el nonce es obligatorio](../guides/connecting-a-device.md#why-the-nonce-is-required).
