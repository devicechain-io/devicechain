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
| Mensajes de [configuración](../guides/connecting-a-device.md#reserved-device-topics) y de [actualización](../guides/connecting-a-device.md#over-the-air-updates) | plataforma → dispositivo | `{instanceId}/{tenant}/device-desired/{deviceToken}` | `{instanceId}.{tenant}.device-desired.{deviceToken}` |
| Informes de configuración y de actualización | dispositivo → plataforma | `{instanceId}/{tenant}/device-reports/{deviceToken}` | `{instanceId}.{tenant}.device-reports.{deviceToken}` |

- Un dispositivo solo está autorizado a publicar y suscribirse en los topics que llevan **su propio** token. No puede leer los comandos de otro dispositivo ni publicar como otro dispositivo.
- **Los dispositivos se conectan por MQTT, no por NATS directo.** Una credencial de dispositivo autoriza solo una conexión MQTT: un cliente NATS simple que presente una se rechaza al conectar. La columna de subjects es lo que ve un operador en las herramientas de NATS, no una segunda vía de entrada. La configuración de la conexión (el client id, el usuario `{tenant}:{credentialId}`, TLS) está en [Ajustes de conexión](../guides/connecting-a-device.md#connection-settings).
- Publica los eventos con QoS 0, o con QoS 1 y un [`altId`](#device-event). Las publicaciones QoS 2 se rechazan por defecto y el broker cierra la conexión; consulta [Calidad de servicio](../guides/connecting-a-device.md#quality-of-service).

Un dispositivo que todavía no tiene credencial llega a la plataforma por HTTPS para [registrarse por sí mismo](#self-registration), en `POST /api/device-management/provisioning/{tenant}/register`. Es un endpoint distinto de la ingesta de eventos, servido por `device-management`.

Por **HTTP**, un dispositivo solo puede enviar eventos. Haz `POST` del mismo [cuerpo de evento](#device-event) a `/{instanceId}/{tenant}/events` en el servicio `event-sources` (puerto 8081). No hay canal de bajada por HTTP: un dispositivo que solo usa HTTP no puede recibir comandos, configuración ni asignaciones de actualización.

### El espacio de nombres de pasarela {#gateway-namespace}

Una pasarela (el [agente de borde](../deployment/edge-services.md#gateway-identity) es una) es un dispositivo que publica por otros dispositivos. Lo hace en su propio espacio de nombres:

| Mensaje | Dirección | Topic MQTT |
| --- | --- | --- |
| [Evento](#device-event) de un dispositivo tras la pasarela | pasarela → plataforma | `{instanceId}/{tenant}/gateways/{gatewayToken}/devices/{deviceToken}/events` |
| Aviso de que se rechazó un evento | plataforma → pasarela | `{instanceId}/{tenant}/gateways/{gatewayToken}/notices` |

- Una pasarela está autorizada a publicar solo bajo su token **propio**, con cualquier token hijo tras `devices/`, y a suscribirse solo a sus propios avisos. No puede publicar en el espacio de nombres de otra pasarela ni de otro tenant, ni en el topic propio de un hijo. La concesión tiene el mismo tamaño sea cual sea el número de dispositivos que representa la pasarela, y la tiene cada dispositivo, así que asignar un primer dispositivo a una pasarela no requiere reconexión.
- La plataforma acepta un evento solo si el dispositivo nombrado en el topic existe, está **asignado a esa pasarela** y la pasarela está habilitada. En caso contrario rechaza el evento con el motivo `NotGatewayChild` ([más abajo](#rejections-after-acceptance)) y envía un aviso a la pasarela. El broker confirma la publicación en ambos casos, porque MQTT 3.1.1 no puede rechazar una.
- La identidad es el topic más la asignación. El cuerpo no lleva ninguna: se rechaza un evento de este espacio de nombres que lleve `credentialType`, `credentialId` o `credentialSecret`, y también un `device` del cuerpo que nombre a otro dispositivo. El evento se atribuye al hijo, y el evento resuelto lleva además el token de la pasarela.
- Un aviso es un objeto JSON, `{"altId": "...", "device": "...", "reason": "not_gateway_child"}`. No lleva carga y no se almacena, así que una pasarela que estaba desconectada cuando se envió no lo recibe. El registro de un rechazo es el flujo `failed-events`.
- La concesión también cubre `responses` y `reports` bajo el espacio de nombres y la suscripción a `commands` y `desired`, pero nada los lee ni los envía todavía. Publica solo **eventos**.

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
| `credentialType` | string | HTTP: ver abajo. MQTT: nunca | `ACCESS_TOKEN` o `MQTT_BASIC`. Junto con `credentialId`, autentica el evento. | `"ACCESS_TOKEN"` |
| `credentialId` | string | HTTP: ver abajo. MQTT: nunca | Para `ACCESS_TOKEN`, el propio token. Para `MQTT_BASIC`, el nombre de usuario, sin el prefijo `{tenant}:` que usa la conexión MQTT. | `"5f98…98b2"` |
| `credentialSecret` | string | HTTP, solo `MQTT_BASIC`. MQTT: nunca | La contraseña de `MQTT_BASIC`. | |

¹ Envía `device` en cada evento. En sentido estricto, la plataforma tolera su ausencia cuando una credencial autentica el evento, y entonces atribuye el evento al dispositivo de la credencial.

**La credencial, por MQTT.** No la envíes. En el broker MQTT de la plataforma el dispositivo se autentica al conectarse y el topic nombra al dispositivo, así que `credentialType`, `credentialId` y `credentialSecret` deben estar ausentes: un evento que lleve cualquiera de ellos se rechaza (consulta la [tabla de rechazos](#rejections-at-the-door)). Considera expuesta la credencial de un dispositivo que lo haya hecho y rótala. Un broker que operas tú y conectas como fuente MQTT externa no autentica dispositivos para la plataforma, así que sus eventos siguen llevando la credencial, como por HTTP.

**La credencial, por HTTP.** El modo de autenticación de dispositivos por defecto es `required`: un evento sin credencial se rechaza (consulta la [tabla de rechazos](#rejections)). Omite la credencial solo en una instancia configurada como `optional` o `disabled`. Una credencial solo se lee cuando están presentes `credentialType` y un `credentialId` no vacío. Consulta [Credenciales de dispositivo](../guides/device-credentials.md).

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
| `reach` | string | no | Para una pasarela o adaptador de protocolo que responde en nombre de un dispositivo; un dispositivo que informa su propio resultado lo omite. Solo se lee cuando `success` es `false`. `"UNANSWERED"`: el comando se puso en el canal y no volvió nada, así que se resuelve como `TIMEOUT`. `"UNSENT"`: el adaptador lo rechazó antes de que llegara al dispositivo, así que se resuelve como `FAILED` y se registra como nunca entregado. Omitido, un fallo es la respuesta del propio dispositivo. Un valor no reconocido se trata como `"UNANSWERED"`. | `"UNANSWERED"` |

```json
{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":false,"error":"actuator jammed"}
```

## Autorregistro {#self-registration}

Un dispositivo que todavía no tiene credencial puede registrarse por sí mismo, por HTTPS, con una **clave y un secreto de aprovisionamiento** compartidos por toda la flota, tomados de un [perfil de aprovisionamiento](../guides/device-self-registration.md). El dispositivo inventa su propia credencial, la envía una sola vez y la plataforma responde con el token del dispositivo y su id de cliente MQTT. La plataforma conserva solo un resumen (digest) de la credencial, y ninguna respuesta contiene un secreto.

```text
POST /api/device-management/provisioning/{tenant}/register
```

- **Solo HTTPS.** La petición lleva el secreto de aprovisionamiento en el cuerpo. No existe el registro por MQTT: un dispositivo no puede registrarse conectándose.
- **Necesita el ingress de la API de la instancia**, que enruta `/api/<area>` a los servicios y elimina el prefijo. Con `ingress.enabled: false` la ruta no tiene ningún camino público. [`dcctl device register`](./dcctl.md#dcctl-device-register) usa el mismo host `--server` que los demás comandos.
- **No hay cabecera `Authorization`.** La clave y el secreto de aprovisionamiento del cuerpo son la autenticación.

### Petición {#self-registration-request}

`Content-Type: application/json`, de **8 KiB** como máximo. Un campo que el esquema no define se rechaza, de modo que un campo mal escrito falla en lugar de registrar el dispositivo sin él.

```json
{
  "provisionKey": "acme-sensors-2026",
  "provisionSecret": "<shown once when the profile was created or rotated>",
  "externalId": "SN-000123",
  "credentialSecret": "<the device's own credential, 22 to 256 base64url characters>",
  "name": "optional",
  "metadata": { "optional": "object" }
}
```

| Campo | Obligatorio | Reglas |
| --- | :---: | --- |
| `provisionKey`, `provisionSecret` | sí | De 1 a 256 caracteres cada uno. El secreto es el que el servidor generó para el perfil. |
| `externalId` | sí | El número de serie u otro id del dispositivo, de 1 a 256 caracteres. Es como la plataforma encuentra el dispositivo. |
| `credentialSecret` | sí | El material de credencial propio del dispositivo, que pasa a ser su token de acceso. **base64url** (letras, dígitos, `-` y `_`, sin relleno), **de 22 a 256 caracteres**. Genera 32 bytes aleatorios y codifícalos (43 caracteres). |
| `name` | no | Como máximo 128 caracteres. Solo se usa cuando esta petición crea el dispositivo. |
| `metadata` | no | Un objeto JSON. Solo se usa cuando esta petición crea el dispositivo. |

**Genera la credencial en el dispositivo y guárdala antes de enviar la petición.** La plataforma no puede devolverla. Así, una petición cuya respuesta se perdió se puede repetir sin riesgo con el mismo material: se reconoce como una repetición y se responde `200`, en lugar de rechazarse como un segundo dispositivo.

### Respuesta {#self-registration-response}

`201` cuando la petición creó el dispositivo, `200` cuando adoptó uno existente o reconoció una repetición. El cuerpo es el mismo en los tres casos y no contiene nada secreto:

```json
{
  "outcome": "CREATED",
  "tenant": "acme",
  "deviceToken": "pv-SN-000123-06e08144e3e8",
  "externalId": "SN-000123",
  "credentialType": "ACCESS_TOKEN",
  "mqtt": { "clientId": "<instanceId>:acme:pv-SN-000123-06e08144e3e8" }
}
```

| `outcome` | Estado | Significado |
| --- | :---: | --- |
| `CREATED` | `201` | El dispositivo no existía. El perfil (estrategia `ALLOW_NEW`) lo creó, del tipo de dispositivo del perfil y reservado para el perfil, con la credencial. El token del dispositivo se deriva del id externo y empieza por `pv-`. |
| `ADOPTED` | `200` | El dispositivo ya existía, estaba reservado para este perfil y no tenía credencial. Se le dio esta. |
| `REPLAYED` | `200` | El dispositivo ya tiene la credencial de este perfil y la petición presentó el mismo material. No se escribió nada. |

Conéctate con el id de cliente de la respuesta y el nombre de usuario MQTT `{tenant}:{credentialSecret}` sin contraseña, como en [Ajustes de conexión](../guides/connecting-a-device.md#connection-settings). La respuesta no repite la credencial.

### Qué decide la respuesta {#self-registration-decision}

Todo rechazo se decide antes de escribir nada. Un dispositivo que la plataforma ya conoce se juzga con estos pasos, en orden, y el primero que coincide es la respuesta:

| Paso | Condición | Respuesta |
| :---: | --- | --- |
| 1 | El dispositivo está deshabilitado. | `403 DEVICE_DISABLED` |
| 2 | Su tipo de dispositivo no es el del perfil. | `409 DEVICE_TYPE_MISMATCH` |
| 3 | No está reservado para este perfil. | `409 DEVICE_NOT_EARMARKED` |
| 4 | La credencial de este perfil en él se deshabilitó o ha caducado. | `409 ALREADY_PROVISIONED`, con el mensaje «the device's credential was revoked; an operator must reset its provisioning» |
| 5 | Tiene la credencial vigente de este perfil y el material coincide. | `200 REPLAYED` |
| 6 | Tiene la credencial vigente de este perfil y el material difiere, o tiene cualquier otra credencial. | `409 ALREADY_PROVISIONED` |
| 7 | No tiene credencial y el perfil ya ha registrado tantos dispositivos como su `maxDevices`. | `422 LIMIT_EXCEEDED` |
| 8 | En cualquier otro caso. | `200 ADOPTED` |

Un dispositivo que la plataforma no conoce (ningún dispositivo activo tiene este `externalId`) se crea con la estrategia `ALLOW_NEW` (`422 LIMIT_EXCEEDED` en el tope) y se rechaza con `404 DEVICE_NOT_REGISTERED` con `CHECK_PRE_PROVISIONED`. Ninguna estrategia adopta nunca un dispositivo que no se reservó para el perfil: un dispositivo creado por un operador solo lo toma un perfil al que alguien lo apuntó, y eso es [reservar](../guides/device-self-registration.md#earmarking).

### Errores {#self-registration-errors}

Un error responde `{ "code": "...", "message": "..." }`. El conjunto de códigos es cerrado. Un `429`, y un `503` por falta de capacidad, llevan además `Retry-After`, en segundos. Un mensaje nunca contiene un secreto y nunca dice qué parte de la autenticación falló.

| Estado | `code` | Cuándo |
| :---: | --- | --- |
| `400` | `INVALID_VALUE` | El inquilino de la ruta no es un token válido; el cuerpo no es un único objeto JSON o lleva un campo no definido; un campo está fuera de rango (el mensaje nombra el campo, nunca su valor). |
| `401` | `PROVISIONING_FAILED` | La clave o el secreto de aprovisionamiento no son válidos. **Es una sola respuesta para todas las causas**: un inquilino desconocido, una clave desconocida, un secreto incorrecto, un perfil deshabilitado o caducado, un perfil cuyo secreto el servidor nunca generó (consulta [rótalo](../guides/device-self-registration.md#rotate-required)) y un inquilino que se está eliminando. |
| `403` | `DEVICE_DISABLED` | Paso 1 de arriba. |
| `404` | `DEVICE_NOT_REGISTERED` | Un dispositivo desconocido con `CHECK_PRE_PROVISIONED`. |
| `408` | `INVALID_VALUE` | El cuerpo no llegó dentro del plazo de lectura (10 segundos por defecto). Se cierra la conexión. |
| `409` | `DEVICE_NOT_EARMARKED`, `DEVICE_TYPE_MISMATCH`, `ALREADY_PROVISIONED` | Pasos 3, 2, y 4 o 6 de arriba. |
| `409` | `DEVICE_TOKEN_COLLISION` | El token de dispositivo derivado de este id externo lo tiene otro dispositivo. Debe resolverlo un operador. |
| `413` | `INVALID_VALUE` | El cuerpo supera los 8 KiB. |
| `422` | `LIMIT_EXCEEDED` | El perfil ha registrado tantos dispositivos como su `maxDevices`. |
| `429` | `RATE_LIMITED` | Se agotó un límite: la asignación de la dirección de origen o los registros por segundo del inquilino. `Retry-After` indica cuánto esperar. |
| `500` | `PROVISIONING_MISCONFIGURED` | El perfil no puede registrar nada: se eliminó su tipo de dispositivo, o su estrategia no es una que la plataforma conozca. Un operador debe corregir el perfil. |
| `500` | `PROVISIONING_UNAVAILABLE` | Un fallo interno. El registro no se completó; reintenta más tarde. |
| `501` | `CREDENTIAL_TYPE_UNSUPPORTED` | El perfil emite un tipo de credencial que esta versión no puede emitir. Terminal; nunca recurre a otro tipo. |
| `503` | `PROVISIONING_UNAVAILABLE` | La réplica está ocupada (con `Retry-After`), o la instancia no tiene claves con las que comprobar un secreto de aprovisionamiento (sin él). |

### Los límites {#self-registration-limits}

Los límites impiden que quien no se ha autenticado haga que el servicio gaste más de lo que vale la petición, y evitan que una tanda de fábrica ahogue al resto del inquilino. En el orden en que se aplican:

1. **Un plazo para el cuerpo.** El cuerpo debe llegar en 10 segundos (`provisioning.readDeadline`, como máximo 1 minuto). Una petición que aún envía su cuerpo no retiene nada salvo su propia conexión.
2. **Un tope de tamaño y el análisis.** 8 KiB, y el cuerpo se lee y se valida antes de que la petición cuente para nada de lo siguiente.
3. **Un límite de peticiones simultáneas**: 64 por réplica (`provisioning.maxInFlight`); después, `503` con `Retry-After`.
4. **Una asignación por origen**: una ráfaga de 60 peticiones y después una por segundo, por dirección de cliente y por réplica (`provisioning.sourceBurst`, `provisioning.sourceEvery`). Los clientes IPv6 se cuentan por /64. Nada exime a un origen que ya tuvo éxito antes. La dirección es la del cliente real solo cuando se indica a los servicios en qué proxies confiar: consulta [Direcciones de cliente y proxies de confianza](../deployment/bootstrap.md#trusted-proxies).
5. **Un techo por inquilino**, que se cobra solo una vez aceptados la clave y el secreto de aprovisionamiento: por defecto una ráfaga de 500 y 20 registros por segundo, resuelto mediante el [nivel y las anulaciones](../concepts/tenant-tiers.md) del inquilino como los demás techos de [gobernanza](../concepts/governance.md). Un ajuste ausente o inalcanzable significa el valor por defecto de la plataforma, nunca ilimitado.
6. **Un tope por perfil**: `maxDevices`, fijado por el operador. Sin él, el perfil no tiene tope.

No hay límite para adivinar el secreto de aprovisionamiento, porque no se puede adivinar: son 256 bits aleatorios, comparados en tiempo constante, y una clave desconocida cuesta el mismo trabajo que un secreto incorrecto. En su lugar, una avalancha sostenida de `401` activa una [alerta](../deployment/observability.md#self-registration-alerts).

### El contrato de reintentos {#self-registration-retry}

Guarda la credencial antes del primer envío y presenta **la misma credencial en cada reintento**.

| Respuesta | Qué hacer |
| --- | --- |
| `429`, o `503` con `Retry-After` | Espera `Retry-After` más un añadido aleatorio del 0 al 100 % de ese valor y reintenta. |
| Un error de red, un tiempo de espera agotado, `408`, `502`, `504`, `503` sin `Retry-After`, o `500` con `PROVISIONING_UNAVAILABLE` | Retrocede de forma exponencial de 1 segundo a 5 minutos, con jitter completo, y reintenta. |
| `400`, `413` | Detente. La petición es incorrecta; es un defecto del firmware. |
| `401`, `403`, `404`, `409`, `422`, `501`, `500` con `PROVISIONING_MISCONFIGURED` | Detente e informa. Reintentar no puede cambiar la respuesta; tiene que hacerlo un operador. |

El jitter importa: una flota que se enciende a la vez y reintenta al unísono vuelve a llegar a la vez. [`dcctl device register`](./dcctl.md#dcctl-device-register) implementa este contrato y es la referencia para quienes escriben firmware.

## Rechazos y errores {#rejections}

Lo que le ocurre a un mensaje que la plataforma no acepta depende de dónde se rechaza y del transporte. HTTP responde a la petición, así que el dispositivo se entera de un rechazo en la entrada. MQTT confirma una publicación (`PUBACK`) en cuanto el broker la ha capturado, **antes** de decodificarla, así que a un dispositivo MQTT nunca se le avisa: todo rechazo posterior al broker solo es visible para un operador. Con el ajuste por defecto, una publicación QoS 1 confirmada mientras el stream de captura cambia de líder también se puede perder, tanto mientras cambia de líder como, con carga sostenida, cuando la ingesta se queda atrás y el stream se llena; vea [Calidad de servicio](../guides/connecting-a-device.md#quality-of-service).

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
| El cuerpo lleva `credentialType`, `credentialId` o `credentialSecret`, en el broker MQTT de la plataforma | — | Se envía a `failed-decode` (redactado) | `total_msg_failed_decode`, `total_msg_credential_in_mqtt_body` |
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
| `NotGatewayChild` | Una pasarela publicó por un dispositivo que no tiene asignado, que no existe, o la pasarela está deshabilitada. No se reintenta: se registra en la primera entrega. |

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
