---
sidebar_position: 3.7
title: Integraciones de entrada
---

# Integraciones de entrada {#inbound-integrations}

La mayoría de los dispositivos se conectan a DeviceChain por sí mismos, por MQTT o HTTP. Algunos nunca lo hacen: un sensor LoRaWAN habla con una pasarela, y un **servidor de red** como ChirpStack o The Things Stack decodifica lo que oyó la pasarela y lo reenvía por HTTPS. Una **integración de entrada** es el extremo receptor de ese reenvío. Da al remitente una URL y un token bearer, encuentra el dispositivo al que se refiere cada mensaje y guarda las lecturas como mediciones normales. A partir de ahí se comportan como cualquier otra medición: tienen unidades, alimentan paneles, activan reglas de detección y generan alarmas.

El mismo mecanismo sirve para cualquier otro sistema capaz de enviar un `POST` HTTP, con un cuerpo JSON o binario, y tú indicas dónde está el identificador del dispositivo. Esta página cubre [ChirpStack v4](#chirpstack), [The Things Stack v3](#the-things-stack), [JSON genérico](#generic-json) y [binario genérico](#generic-binary), y después qué se guarda, cómo se tratan los duplicados, los límites y lo que la versión 1 no hace.

## Cómo encaja todo {#concept}

Una integración es un recurso del inquilino con estos ajustes:

| Ajuste | Significado |
| --- | --- |
| Token | El nombre de la integración en el inquilino. Es el último segmento de la URL y lo eliges tú (`chirpstack-eu868`). No se puede cambiar. |
| Tipo de remitente | `CHIRPSTACK_V4`, `TTS_V3`, `GENERIC_JSON` o `GENERIC_BINARY`. Fija cómo se lee el cuerpo y no se puede cambiar. El cuerpo nunca se adivina por su forma. |
| Tipos de dispositivo | El **alcance**: solo se alcanzan por la integración los dispositivos de estos tipos, diga lo que diga el remitente. Al menos uno, como máximo 50. |
| Identificador del dispositivo | Cómo nombra el remitente a un dispositivo: por `DEV_EUI` (el valor por defecto), por `DEVICE_ID` (solo The Things Stack) o, en un remitente genérico, por un esquema de alias que tú nombras (`ALIAS:custom.serial`). |
| Ajustes genéricos | Solo en remitentes genéricos: dónde está el identificador, dónde está el arreglo de uplinks y dónde está la hora del propio remitente. |
| Guardar métricas del enlace | Desactivado por defecto. Guarda la intensidad de señal, la relación señal/ruido y el número de pasarelas junto a cada uplink. |
| Aceptando datos | Desactiva una integración para rechazar su tráfico de inmediato sin eliminarla. |
| Token bearer | Lo genera el servidor (256 bits aleatorios), se **muestra una sola vez** al crear la integración o rotar su token, y se almacena solo como un resumen con clave. |

Cada mensaje pasa por los mismos pasos:

1. La plataforma comprueba el token bearer contra la integración que nombra la URL, antes de leer el cuerpo.
2. Lee el cuerpo según el tipo de remitente de la integración y extrae el identificador del dispositivo.
3. Busca el dispositivo que lleva ese identificador como [alias](#aliases). Un dispositivo fuera del alcance de la integración, o ningún dispositivo, significa que el mensaje no se guarda.
4. Convierte los valores decodificados en lecturas y los guarda como mediciones de ese dispositivo.

Cuántas integraciones puede tener un inquilino es un ajuste del [nivel](../concepts/tenant-tiers.md), **Integraciones por inquilino**. El valor por defecto de la plataforma es 5, un nivel puede fijar hasta 100 y 0 significa ninguna. Crear una por encima del techo se rechaza.

## La URL {#url}

Cada integración se alcanza en una única dirección pública:

```text
POST https://<host>/api/integrations/{tenant}/{integration}[/{messageType}]
Authorization: Bearer <bearer-token>
```

- `<host>` es la dirección de tu instancia, la misma en la que abres la consola. Sustitúyela; el id de la instancia no forma parte de la URL.
- `{tenant}` es el token del inquilino e `{integration}` es el token de la integración.
- `{messageType}` es opcional. Solo lo usa The Things Stack, porque envía cada tipo de mensaje a una ruta propia.
- Solo se acepta `POST`. Cualquier otro método recibe `405`.
- El token va en la cabecera `Authorization` y en ningún otro sitio. Un token en la URL quedaría escrito en los registros de acceso.

La consola muestra la URL, la cabecera y los ajustes que necesita el remitente cuando creas la integración, en el diálogo que muestra el token una sola vez, y de nuevo en la página de la integración, en **Conectar el remitente**.

:::warning Un servidor de red ajeno debe confiar en tu certificado
Un servidor de red alojado por un tercero no confiará en una instancia que sirve un certificado autofirmado. Dale al host un certificado de una CA pública (`ingress.tls.clusterIssuer`) antes de conectar uno alojado. A un servidor de red que ejecutas tú mismo le puedes dar tu CA.
:::

Si operas la instancia, también hay que indicar a `event-sources` en qué proxies que tiene delante debe creer para conocer la dirección de un cliente (`infrastructure.http.trustedProxies`); de lo contrario, todos los remitentes comparten una sola asignación de peticiones. `dcctl` lo configura; consulta [Direcciones de cliente y proxies de confianza](../deployment/bootstrap.md#trusted-proxies).

## Antes de conectar un remitente {#before-you-start}

1. **Ten un tipo de dispositivo con un perfil.** Los dispositivos de este tipo son los que la integración puede alcanzar. Declara una [métrica](../user-guide/device-types-and-profiles.md#métricas) para cada valor decodificado del que quieras unidades o validación, con la misma clave que produce el objeto decodificado. Las claves que no declares se guardan igualmente, sin unidad.
2. **Crea la integración.** En la consola, abre **Conectar → Integraciones** en la barra lateral y elige **Nueva integración**. Elige el **Tipo de remitente**, los **Tipos de dispositivo** que puede alcanzar y, para un servidor de red, el **Identificador del dispositivo**. Activa **Guardar métricas del enlace** si quieres datos de señal. Elige **Crear integración** y copia el token bearer del diálogo. Se muestra una sola vez; si lo pierdes, rótalo. La página Integraciones necesita el permiso `device:read` para abrirse y `device:write` para modificar.
3. **Añade los dispositivos** con sus alias, como en la sección siguiente. Puedes hacerlo antes o después de que el remitente empiece a enviar: los mensajes de un dispositivo que aún no existe se listan para que puedas [crearlo a partir de lo visto](#unregistered).

Con GraphQL, `createIntegration` devuelve la integración y el token juntos:

```graphql
mutation {
  createIntegration(request: {
    token: "chirpstack-eu868"
    name: "ChirpStack EU868"
    type: "CHIRPSTACK_V4"
    enabled: true
    deviceTypeTokens: ["lora-sensor"]
    linkMetrics: true
  }) {
    integration { token type identity }
    issuedToken
  }
}
```

`rotateIntegrationToken(token)` reemplaza el token y devuelve el nuevo una sola vez. El token anterior deja de funcionar de inmediato, sin solapamiento, así que actualiza el remitente enseguida. `updateIntegration` cambia todo salvo el token y el tipo. `deleteIntegration` elimina la integración; los alias de los dispositivos permanecen.

## Identifica los dispositivos con alias {#aliases}

Un mensaje nombra un dispositivo como lo conoce el remitente, no como lo conoce DeviceChain. Un **alias** de dispositivo registra ese nombre en el dispositivo. Un alias es un esquema y un valor:

| Esquema | El valor | Se guarda como |
| --- | --- | --- |
| `lorawan.deveui` | El DevEUI de LoRaWAN: exactamente 16 dígitos hexadecimales. | En mayúsculas. `70b3d57ed0012345` y `70B3D57ED0012345` son el mismo alias. |
| `tts.device-id` | El id que tiene el dispositivo en The Things Stack: de 1 a 36 letras, dígitos y guiones, sin empezar ni terminar con guion. | En minúsculas. |
| `custom.<name>` | Cualquier identificador propio, para un remitente genérico: de 1 a 128 caracteres sin caracteres de control. El nombre empieza por una letra minúscula o un dígito y sigue con letras minúsculas, dígitos, guiones y guiones bajos, hasta 63 caracteres en total. | Tal como se escribe. Importan las mayúsculas. |

Un dispositivo tiene como máximo 8 alias y un valor por esquema, y un esquema y un valor solo pueden pertenecer a un dispositivo del inquilino. Escribir un DevEUI en minúsculas, o con espacios alrededor, es válido: se normaliza al guardarlo y cuando llega un mensaje. Todo lo que no sean 16 dígitos hexadecimales se rechaza.

Establece un alias:

- en la consola, en el campo **Alias** del formulario del dispositivo: elige **Añadir alias**, escoge **DevEUI de LoRaWAN**, **Id de dispositivo del servidor de red** o **Esquema propio** y escribe el valor;
- con GraphQL, con `aliases: [{scheme: "lorawan.deveui", value: "70B3D57ED0012345"}]` en `createDevice` o `updateDevice`. En una actualización, la lista que envías reemplaza los alias del dispositivo, y una lista vacía los elimina;
- en `importDevices`, en el mismo campo `aliases`.

```graphql
mutation {
  createDevice(request: {
    token: "boiler-room-3"
    deviceTypeToken: "lora-sensor"
    aliases: [{ scheme: "lorawan.deveui", value: "70b3d57ed0012345" }]
  }) { token aliases { scheme value } }
}
```

Para encontrar un dispositivo por alias, da a `devices` el criterio `alias: {scheme: "lorawan.deveui", value: "..."}`.

El **Identificador del dispositivo** de la integración decide qué esquema se busca: `DEV_EUI` lee `lorawan.deveui`, `DEVICE_ID` lee `tts.device-id` y `ALIAS:custom.serial` lee `custom.serial`. Un dispositivo que solo tiene otro esquema no se encuentra.

**Cambiar o quitar el identificador de un dispositivo.** Pasar un identificador (un DevEUI, un id de dispositivo) a otro dispositivo, o quitarlo, surte efecto en unos cinco minutos. Hasta entonces el endpoint de integración puede seguir nombrando al dispositivo anterior para ese identificador, pero la plataforma comprueba en el evento que el dispositivo aún tiene el identificador, así que esos uplinks se rechazan y se conservan en el almacén de mensajes fallidos para inspeccionarlos (motivo `IntegrationAliasMismatch`, contado por `devicechain_devicemanagement_resolve_integration_alias_mismatch_total`). Nunca se guardan en ningún dispositivo y no se pueden reproducir. Cuando el cambio se ha propagado, los uplinks llegan al dispositivo que tiene ahora el identificador. Un dispositivo que crees para un identificador que ya estaba llegando empieza a recibir en menos de un minuto.

## ChirpStack v4 {#chirpstack}

ChirpStack envía los uplinks mediante una **integración HTTP** en una aplicación.

1. En la consola, crea una integración con tipo de remitente **ChirpStack v4**, con los tipos de dispositivo y, si los quieres, las métricas del enlace. Copia el token bearer.
2. En ChirpStack, abre la aplicación, añade la integración **HTTP** y rellénala:

   | Campo de ChirpStack | Valor |
   | --- | --- |
   | Payload encoding | **JSON**. Una integración Protobuf se rechaza con `415`, y el mensaje nombra este ajuste. |
   | Event endpoint URL(s) | `https://<host>/api/integrations/{tenant}/{integration}`. ChirpStack añade `?event=up` por sí mismo. |
   | Headers | Nombre `Authorization`, valor `Bearer <bearer-token>`. |

3. **Configura un códec en el perfil de dispositivo de ChirpStack** (un códec de payload en JavaScript). La plataforma guarda los valores que decodifica el servidor de red; no decodifica la trama cruda. Sin códec, ChirpStack envía la trama cruda y ningún objeto decodificado, y el uplink se [cuenta como no decodificado](#undecoded) en lugar de guardarse. Como alternativa, da al tipo de dispositivo un [decodificador de payload](./payload-decoders.md), que lee la trama cruda en DeviceChain.
4. Añade los dispositivos en DeviceChain con su DevEUI como alias `lorawan.deveui`.

Los uplinks se guardan como lecturas. ChirpStack envía además los eventos `ack` y `txack` a la misma URL: una instalación con el [downlink LoRaWAN](#lorawan-downlink) activado los lee para cerrar los [downlinks confirmados](#lorawan-confirmed); sin él se responden con `204` y se ignoran sin leerlos, como los demás eventos (join, status), sea cual sea su contenido. El `deduplicationId` del uplink debe ser un UUID, como siempre envía ChirpStack; cualquier otra cosa se rechaza con `400`. La hora del mensaje es el `time` que informa ChirpStack, o la hora a la que lo recibió la plataforma cuando el campo falta.

## The Things Stack v3 {#the-things-stack}

The Things Stack envía los uplinks mediante un **webhook** en una aplicación.

1. En la consola, crea una integración con tipo de remitente **The Things Stack v3**. En **Identificador del dispositivo** elige **DevEUI**, o **Id de dispositivo** si prefieres hacer coincidir el id del dispositivo de The Things Stack. Copia el token bearer.
2. En The Things Stack, abre la aplicación, añade un **Custom webhook**, fija el formato en **JSON** y rellénalo:

   | Campo de The Things Stack | Valor |
   | --- | --- |
   | Base URL | `https://<host>/api/integrations/{tenant}/{integration}` |
   | Additional headers | Nombre `Authorization`, valor `Bearer <bearer-token>`. |
   | Enabled event types | Uplink message, activado, con la ruta `/uplink-message`. La plataforma también lee las rutas `/up`, `/uplink` y `/uplinks`. |

3. **Configura un payload formatter** en la aplicación o en el dispositivo (el LoRaWAN Device Repository ofrece uno para muchos sensores). La plataforma guarda lo que produce el formatter; no decodifica la trama cruda. Sin uno, el uplink se [cuenta como no decodificado](#undecoded). Como alternativa, da al tipo de dispositivo un [decodificador de payload](./payload-decoders.md), que lee la trama cruda en DeviceChain.
4. Añade los dispositivos en DeviceChain con su DevEUI como alias `lorawan.deveui`, o con su id de dispositivo como alias `tts.device-id` si elegiste **Id de dispositivo**.

Un mensaje enviado a cualquier otra ruta (un join accept, una localización resuelta) se responde con `204` y se ignora, igual que un evento de downlink que no pertenece a un downlink enviado por la plataforma (véase [downlink de The Things Stack](#lorawan-downlink-tts)), de modo que puedes activar otros tipos de mensaje sin cambiar nada en DeviceChain, aunque no hay motivo para hacerlo. La hora del mensaje es el `received_at` del uplink, que debe estar presente y no tener más de 366 días. La clave de deduplicación es el dispositivo, el contador de tramas y esa hora juntos, porque el contador se reinicia cuando un dispositivo vuelve a unirse.

## JSON genérico {#generic-json}

Usa una integración JSON genérica para cualquier sistema que pueda enviar JSON: la nube de un fabricante de pasarelas, un middleware, un script. Tú indicas a la plataforma dónde encontrar cada pieza. Al crear la integración elige el tipo de remitente **JSON genérico**, fija **Identificador del dispositivo** en el esquema de alias en el que están los identificadores (`ALIAS:custom.serial`, por ejemplo) y completa los ajustes. Con GraphQL son el campo `generic`, un documento JSON dentro de una cadena:

| Ajuste | Significado |
| --- | --- |
| `idFrom` | Obligatorio. Dónde está el identificador del dispositivo: `pointer:/device/id` (un [puntero JSON](https://datatracker.ietf.org/doc/html/rfc6901) dentro de cada elemento), `header:X-Device-Id` (una cabecera de la petición) o `query:device` (un parámetro de consulta). Un identificador de cabecera o de consulta se aplica a todos los elementos de la petición. |
| `itemsPointer` | Opcional. Un puntero a un arreglo del cuerpo; cada elemento es un uplink. Sin él, el cuerpo es un uplink, o un arreglo simple de uplinks. |
| `timePointer` | Opcional. Un puntero a la hora que registró el remitente, como cadena RFC 3339 o como número de época en segundos o milisegundos. Sin él se usa la hora de recepción. |
| `framePointer`, `frameEncoding` | Opcionales. Dónde está una trama cruda codificada (`HEX` o `BASE64`). Solo se toma una muestra, para ayudarte a ver qué contenía un uplink no decodificado. |

Cada clave de un elemento, salvo el identificador y la hora, es una lectura candidata. Por ejemplo, con `idFrom` en `pointer:/serial`, `itemsPointer` en `/readings` y `timePointer` en `/ts`:

```bash
curl -X POST https://dc.example.com/api/integrations/acme/vendor-cloud \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"readings": [
        {"serial": "A-100", "ts": "2026-10-27T09:00:00Z", "temperature": 21.4, "battery": {"voltage": 3.1}},
        {"serial": "A-101", "ts": "2026-10-27T09:00:00Z", "temperature": 19.8, "door": false}
      ]}'
```

Un arreglo lleva como máximo 100 elementos. Un cuerpo que no se puede leer, o un elemento sin identificador, se rechaza entero con `400` y no se guarda nada. Los elementos de dispositivos que el inquilino no tiene se omiten uno a uno y se listan como [no registrados](#unregistered); el resto se guarda.

:::warning Un reintento de un remitente genérico se guarda dos veces si no le das una hora del remitente
Sin `timePointer`, la plataforma marca un elemento con la hora a la que lo recibió, de modo que el mismo elemento enviado otra vez parece uno nuevo. Fija `timePointer` en un campo que el remitente rellene una sola vez por lectura, y un reintento llevará la misma hora y se guardará una vez. Consulta [Duplicados y reintentos](#duplicates).
:::

## Binario genérico {#generic-binary}

Una integración binaria genérica acepta un cuerpo de bytes crudos, con el identificador en una cabecera o en un parámetro de consulta (`idFrom` es `header:` o `query:`; un cuerpo binario no tiene campos a los que apuntar). Sin un [decodificador de payload](./payload-decoders.md) en el tipo del dispositivo, la plataforma no puede leer esos bytes, así que cada uplink de este tipo se acepta, se [cuenta como no decodificado](#undecoded) y sus primeros 512 bytes se guardan como muestra en la integración. Úsala para ver qué envía un remitente antes de que exista un decodificador. Con un decodificador en el tipo del dispositivo, los bytes se decodifican en lecturas.

## Qué se guarda {#what-is-stored}

El objeto decodificado que lleva cada mensaje (el `object` de ChirpStack, el `decoded_payload` de The Things Stack, un elemento genérico) se aplana en mediciones del dispositivo. Un mensaje se convierte en un evento con estas lecturas:

- Cada número es una lectura, con una clave formada por su ruta unida con puntos: `{"temperature": 21.4, "battery": {"voltage": 3.1}, "sensors": [{"value": 7}]}` guarda `temperature`, `battery.voltage` y `sensors.0.value`.
- Un booleano se guarda como `1` si es verdadero y `0` si es falso.
- Una cadena o un `null` se omiten. También se omite cualquier valor anidado a más de 32 niveles y cualquier ruta que no sea una clave de medición válida (letras, dígitos, `_`, `.` y `-`, como máximo 128 caracteres, sin empezar con `.` ni `-`). Nada se renombra para que encaje.
- Declara una métrica con la misma clave para dar a una lectura su unidad y su validación. Una lectura sin métrica declarada se guarda sin unidad.
- La hora del mensaje es la del remitente cuando la da, y la hora de recepción de la plataforma en caso contrario.
- Un mensaje que se aplana en más de 256 lecturas se rechaza entero con `400`, sin recortarlo.

Con **Guardar métricas del enlace** activado, se guardan tres lecturas más con cada uplink a partir del informe de recepción del servidor de red: `lorawan.rssi` y `lorawan.snr` de la recepción más fuerte, y `lorawan.gateways`, el número de pasarelas que oyeron el uplink. Estos nombres pertenecen a la plataforma; un valor decodificado con la misma clave se omite.

### Uplinks sin nada que guardar {#undecoded}

Un uplink que no lleva objeto decodificado, o que no tiene ningún valor numérico ni booleano, se confirma pero no guarda nada. La integración lo cuenta como **no decodificado**, y su página muestra el recuento, cuándo llegó el último y una muestra de la trama cruda en hexadecimal. La causa habitual es un perfil de dispositivo sin códec en el servidor de red. La alerta `IntegrationUndecodedHigh` avisa a un operador cuando la mayor parte del tráfico es así; consulta [Observabilidad](../deployment/observability.md).

## Duplicados y reintentos {#duplicates}

Un servidor de red que no recibe respuesta a tiempo envía el mensaje otra vez, y una pasarela puede entregar un uplink dos veces. Cada mensaje lleva una clave que la plataforma guarda con el evento, y un evento cuya clave ya está guardada para ese dispositivo y esa hora no se vuelve a guardar.

| Remitente | La clave |
| --- | --- |
| ChirpStack v4 | El `deduplicationId`. |
| The Things Stack v3 | El DevEUI del dispositivo (su id de dispositivo, cuando la integración identifica los dispositivos por `DEVICE_ID` y el mensaje no trae DevEUI), el contador de tramas y `received_at`. |
| Genérico, con `timePointer` | Un resumen del dispositivo y del elemento, más la hora del remitente. Un reintento lleva el mismo elemento y la misma hora, así que se guarda una vez. |
| Genérico, sin `timePointer` | Un resumen del dispositivo y del elemento, más la hora de recepción. Un reintento es una clave nueva, así que **se guarda dos veces**. |

La plataforma también descarta, antes de procesarlo, un repetido que lleva la misma clave dentro de los diez minutos posteriores al primero; un remitente genérico sin `timePointer` obtiene una clave nueva en cada envío, así que esto no lo cubre. Una clave tiene como máximo 192 bytes; una más larga se rechaza con `400`. Ninguna regla fusiona dos uplinks distintos que casualmente tengan los mismos valores.

## Dispositivos no registrados {#unregistered}

Cuando un mensaje nombra un identificador que no lleva ningún dispositivo dentro del alcance de la integración, la plataforma no lo guarda. En su lugar registra el identificador en la integración, para que veas qué dispositivos te están hablando sin que los hayas añadido. Abre la integración en la consola y mira en **Dispositivos no registrados vistos**: cada fila muestra el esquema, el identificador, cuántas veces se vio y cuándo. **Crear un dispositivo con este alias** abre el formulario del dispositivo con el alias rellenado, y también el tipo cuando la integración tiene en su alcance exactamente un tipo de dispositivo. Con GraphQL, la lista es `unregisteredIntegrationDevices(criteria)`.

- La lista guarda como máximo 1.000 identificadores por integración. Pasado ese punto, los nuevos se cuentan y no se listan.
- Un avistamiento se registra como máximo una vez por minuto para cada identificador en cada réplica de `event-sources`, así que el recuento mide actividad y no es un número exacto de mensajes.
- Un identificador que ya lleva un dispositivo fuera del alcance de la integración no se lista, de modo que la lista nunca muestra un valor que un dispositivo ya tiene.
- **Vaciar lista** (`clearUnregisteredIntegrationDevices(integration)`) la vacía. Es una lista de observaciones, así que un identificador que el remitente sigue enviando vuelve a aparecer.

## Qué se le dice al remitente {#responses}

Un servidor de red solo ve el código de estado, y algunos suspenden un webhook al que se rechaza con demasiada frecuencia. La ruta está construida con eso en mente.

| Respuesta | Significado |
| --- | --- |
| `202` | Aceptado. Incluye los mensajes de dispositivos no registrados, que se omiten, y los mensajes [descartados](#limits) porque el inquilino superó su asignación de ingesta. |
| `204` | Un tipo de mensaje que no es un uplink (join, status, confirmación). Confirmado e ignorado. |
| `400` | El cuerpo no es lo que espera el tipo de remitente de la integración: JSON no válido, un identificador ausente o mal formado, un `deduplicationId` que no es un UUID, una hora de más de 366 días, más de 100 elementos o más de 256 lecturas. No se guarda nada de la petición. |
| `401` | Falta el token, es incorrecto o pertenece a otra integración. Un token incorrecto y una integración que no existe son indistinguibles a propósito. |
| `403` | La integración está desactivada. No se cuenta como un inicio de sesión fallido. |
| `405` | El método no es `POST`. |
| `413` | El cuerpo supera los 256 KiB. |
| `415` | ChirpStack está enviando Protobuf. Cambia su codificación de payload a JSON. |
| `429` | Demasiados inicios de sesión fallidos para esta integración, demasiadas peticiones desde una misma dirección cuyo token la plataforma no acaba de verificar o, en una réplica, demasiadas peticiones para integraciones que nunca ha verificado. Reintenta pasado el intervalo de `Retry-After`. |
| `503` | La plataforma no puede comprobar el token ni recibir el mensaje ahora, o está aplicando contrapresión. Reintenta. Una cabecera `Retry-After` significa que la plataforma aplica contrapresión: un uplink suelto no se guardó, pero en un arreglo genérico los elementos anteriores al rechazo pueden haberse guardado, así que da a un remitente genérico un `timePointer` para que el reintento sea seguro. |

Un cambio en una integración es rápido pero no instantáneo en el endpoint, que recuerda lo que averiguó sobre una integración hasta un minuto (hasta 15 minutos mientras device-management no está disponible). Después de rotar un token o desactivar una integración, puede seguir respondiendo `202` hasta un minuto a un remitente que aún use el token anterior, pero nada de lo enviado en ese minuto se guarda. Después de volver a activar una integración, puede seguir respondiendo `403` hasta un minuto.

## Límites {#limits}

| Límite | Valor |
| --- | --- |
| Cuerpo de la petición | 256 KiB |
| Objeto decodificado, o un elemento genérico | 64 KiB, anidado como máximo 32 niveles |
| Elementos en un arreglo genérico | 100 |
| Lecturas en un uplink | 256 |
| Antigüedad de una hora que informa el remitente | 366 días |
| Integraciones por inquilino | 5 por defecto, fijadas por el nivel, como máximo 100 |
| Alias en un dispositivo | 8, un valor por esquema |
| Identificadores no registrados listados por integración | 1.000 |

**La asignación de ingesta del inquilino.** El tráfico de integraciones se mide en una asignación propia, con el techo de ingesta del inquilino, contada en lecturas como en cualquier otro transporte, aparte de las asignaciones que consumen la ingesta MQTT y HTTP. Las asignaciones son independientes, así que un inquilino que envía por varias vías a la vez puede ser admitido por encima de un techo en total, en cada réplica de `event-sources`; consulta [Cuándo la ingesta puede admitir a un inquilino por encima de su techo](../concepts/governance.md#ingest-above-ceiling). Una petición con un arreglo de N uplinks se cobra N veces, así que un arreglo grande no compra una tarifa más barata.

**Por encima de la asignación, la respuesta es `202`, no `429`.** Un rechazo perdería el mensaje de todos modos, y algunos servidores de red suspenden un webhook al que se rechaza, lo que haría perder tráfico que estaba dentro de la asignación. La consecuencia es que el remitente cree que los datos llegaron y no fue así. La alerta `IntegrationShedAccepted` avisa a un operador cuando esto ocurre; sube el techo del inquilino o reduce lo que envía la integración.

**Límites al tráfico sin autenticar.** Quien conozca la URL de una integración no puede usarla para gastar la asignación del inquilino ni el tráfico bueno de una integración: los tokens incorrectos se limitan por integración (una ráfaga de 30 y luego 1 por segundo), las peticiones cuyo token la plataforma no ha verificado en el último minuto se limitan por dirección de origen (una ráfaga de 60 y luego 5 por segundo) y, para nombres de integración que nunca ha verificado, por réplica (una ráfaga de 100 y luego 20 por segundo). El front end `ingress-nginx` limita además cada dirección a 200 peticiones por segundo y responde `503` por encima; limita por cliente solo cuando el controlador está configurado para confiar en la dirección reenviada del balanceador, y el front end de la Gateway API no tiene ese límite. El tráfico constante de un servidor de red con un token válido le cuesta a su dirección aproximadamente una petición por minuto frente a los límites de la plataforma. La alerta `IntegrationAuthFailuresSustained` salta cuando los fallos se mantienen diez minutos, que es el aspecto de un remitente que sigue usando un token rotado.

## Comandos a dispositivos de integración {#commands}

Un dispositivo que informa a través de una integración no tiene una conexión con DeviceChain por la que enviarle un comando. Salvo que el [downlink LoRaWAN](#lorawan-downlink) esté activado en tu instalación, un comando a un dispositivo así **se acepta cuando lo emites** y luego **falla en el siguiente barrido de entrega** con `COMMAND_TRANSPORT_UNSUPPORTED`, porque la plataforma reconoce el transporte y no tiene forma de entregar por él. No espera en la cola hasta caducar.

Conviene conocer dos casos:

- Un dispositivo que además se conecta por MQTT se juzga por el último transporte por el que informó. Mientras su último informe fue a través de la integración, sus comandos fallan; cuando informa por MQTT, se entregan.
- Un dispositivo que nunca ha informado no tiene transporte registrado, así que sus comandos se despachan como de costumbre, y caducan sin respuesta, hasta que informa por primera vez.

Para un dispositivo cuyo servidor de red no tiene API de downlink, dale también una conexión MQTT. Consulta la [matriz de transportes](../reference/transport-matrix.md).

## Downlink LoRaWAN {#lorawan-downlink}

Una instalación que ejecuta los conectores de salida con el downlink activado (`functionalAreas.outbound-connectors.config.lpwanDownlink.enabled: true`; el chart de Helm hace entonces que la entrega de comandos despache estos comandos, y rechaza una copia de esa marca puesta a mano) envía comandos poniendo downlinks en la cola del servidor vinculado. Se admiten ChirpStack v4, The Things Stack v3 y webhooks HTTP configurables. A continuación se describe ChirpStack; los demás servidores tienen subsecciones más abajo. Nada cambia en una instalación que lo deja desactivado.

**1. Vincula la integración a su servidor de red.** Crea y publica un conector de tipo `lorawan_downlink`:

```json
{
  "networkServer": "chirpstack_v4",
  "baseUrl": "https://chirpstack.example.com",
  "integration": "my-integration-token"
}
```

`integration` es el token de la integración de entrada cuyos dispositivos atiende este servidor. El **secreto** del conector es una clave de API de ChirpStack con permiso para encolar downlinks (la API REST de ChirpStack v4, servida por su componente `chirpstack-rest-api`); se guarda cifrado y no se vuelve a mostrar. Solo un conector publicado puede nombrar una integración: con ninguno, o con dos, un comando se rechaza en lugar de enviarse. Como todo conector, la llamada a `baseUrl` pasa por la política de salida de la plataforma, así que un servidor en una dirección privada necesita una autorización del operador.

**2. Define el comando.** Sin codificador, da al perfil del tipo de dispositivo un comando con estos parámetros de payload en bruto. Para enviar valores tipados, configura un [codificador](#lorawan-command-encoder):

| Parámetro | Significado |
| --- | --- |
| `fPort` | El puerto de aplicación LoRaWAN, de 1 a 223. Obligatorio. |
| `hex` o `base64` | La carga útil de la aplicación, en hexadecimal o en base64 estándar con relleno. Indica exactamente uno. Como máximo 242 bytes. |
| `confirmed` | Opcional, por defecto `false`. `true` pide al dispositivo que acuse la trama y retiene el comando hasta que lo haga (véase [Downlinks confirmados](#lorawan-confirmed)). |

Cualquier otro nombre de parámetro se rechaza.

**3. Envíalo.** El dispositivo debe tener un [alias](#aliases) `lorawan.deveui`: el DevEUI se lee del dispositivo, nunca del comando. El comando sale por la integración por la que el dispositivo **informó por última vez**.

**Qué significa el resultado.** En un downlink sin confirmar, `SUCCEEDED` significa que el servidor de red **encoló** el downlink (el resultado incluye `{"stage":"queued","queueItemId":...}`). No significa que el dispositivo lo haya recibido: un dispositivo de clase A solo oye los downlinks después de su siguiente transmisión, y un downlink sin confirmar no recibe acuse. En un [downlink confirmado](#lorawan-confirmed), `SUCCEEDED` significa que el dispositivo lo acusó. Los demás resultados:

| Qué ocurrió | Resultado |
| --- | --- |
| Un parámetro no es válido, el dispositivo no tiene DevEUI, la integración no tiene vínculo (o tiene dos), el comando caducó, o el inquilino superó su techo de llamadas de salida | `FAILED`, no entregado. Es seguro volver a enviarlo. |
| El servidor de red lo rechazó (un 4xx: clave de API incorrecta, dispositivo desconocido, carga demasiado grande para la velocidad de datos actual del dispositivo) o no se pudo alcanzar antes de enviar nada | `FAILED`, no entregado. El motivo nombra la respuesta del servidor. |
| La petición se envió y el servidor falló o no respondió (un 5xx, un tiempo de espera) | El resultado es **desconocido**: el downlink puede estar en cola, y enviarlo de nuevo puede enviarlo dos veces. |

Consulta la métrica `lpwan_downlinks_total` por resultado para ver dónde se detienen los comandos.

### Downlinks confirmados {#lorawan-confirmed}

Indica `"confirmed": true` en el comando y el servidor de red pide al dispositivo que acuse la trama. El comando **no** se responde cuando el downlink se encola: permanece en `SENT` hasta que el servidor de red informa de lo ocurrido, y entonces se cierra con esa evidencia:

| Qué informa el servidor de red | Comando |
| --- | --- |
| El dispositivo acusó la trama (`ack`, acusada) | `SUCCEEDED`. El resultado incluye `{"stage":"acknowledged",...}`. |
| El servidor de red dejó de esperar el acuse (`ack`, no acusada) | `TIMEOUT`, resultado desconocido. El dispositivo pudo oír la trama y perderse su acuse, así que enviarlo de nuevo puede actuar dos veces. |
| Una pasarela aceptó la trama para transmitirla (`txack`) | Se registra y el comando sigue esperando. Que una pasarela envíe una trama no es que el dispositivo la reciba. |
| Nada antes del plazo | `TIMEOUT`, resultado desconocido. El plazo es la caducidad del propio comando, o 24 horas si no tiene. |

ChirpStack solo informa de un acuse si la integración HTTP de la aplicación está configurada para enviar el evento `ack`, y debe ser la misma integración que registraste en DeviceChain (la URL y el token de arriba): el informe se lee con el token de la propia integración, de modo que solo puede cerrar comandos enviados por esa integración, a dispositivos registrados en ella. Activa también el evento `txack` si quieres que se registre la transmisión; es opcional.

Un informe que no corresponde a ningún downlink en espera nunca se asocia por suposición. Se descarta, se registra en el log y se cuenta en `lpwan_ack_reports_total` (`unmatched` cuando nada espera ese elemento de la cola, `mismatch` cuando nombra otra integración u otro dispositivo); ambos merecen una alerta. Todo downlink produce un `txack`, confirmado o no, así que uno para un downlink que no está en espera se cuenta como `tx_untracked` y es normal. Durante una actualización progresiva, un pod de la versión anterior sigue rechazando un comando confirmado como no entregado, lo que es seguro volver a enviar.

Consulta `lpwan_ack_reports_total` por resultado: `acknowledged`, `timed_out`, `transmitted` y `deadline` son los resultados de arriba. Un `unmatched` de vez en cuando no es un fallo: si un informe se responde pero se pierde el acuse del mensaje en el broker, el informe llega de nuevo y no encuentra nada en espera (una réplica que lo cerró hace un momento lo cuenta como `redelivered`). Alerta sobre una tasa sostenida de `unmatched` o sobre cualquier `mismatch`.

Un `txack` de un downlink que solo llevaba comandos MAC no tiene elemento de cola y se responde con `204` y se cuenta (`integration_downlink_reports_total`, resultado `untracked`); un `ack` sin elemento de cola se rechaza con `400`. Los informes tienen un límite propio, separado de la telemetría: una ráfaga de 100 por inquilino y 20 por segundo sostenidos. El límite se lleva por réplica de `event-sources`, así que el techo efectivo de un inquilino es esa tasa multiplicada por el número de réplicas. Más allá, la petición se responde con `202` y se cuenta (resultado `rate_limited`), y el comando que habría cerrado se cierra en su plazo, de modo que la avalancha de un inquilino no desplaza las confirmaciones de otro.

**Actualizar.** Los downlinks confirmados necesitan la nueva versión tanto de los conectores de salida como de las fuentes de eventos. Mientras los conectores de salida son nuevos y las fuentes de eventos siguen siendo antiguas, los pods antiguos responden `204` a los informes del servidor de red y los descartan, así que un downlink confirmado enviado en esa ventana se cierra como `TIMEOUT`, resultado desconocido, en su plazo. El chart los actualiza en la misma operación; para evitar la ventana, retén los comandos confirmados mientras dure.

### The Things Stack v3 {#lorawan-downlink-tts}

Un vínculo con The Things Stack es un conector `lorawan_downlink` con `"networkServer": "tts_v3"` y la aplicación a la que pertenecen los dispositivos:

```json
{
  "networkServer": "tts_v3",
  "baseUrl": "https://eu1.cloud.thethings.network",
  "integration": "my-integration-token",
  "applicationId": "my-application"
}
```

El **secreto** del conector es una clave de API con derecho a escribir el tráfico de downlink de esa aplicación. El dispositivo debe tener un [alias](#aliases) `tts.device-id`, que es el nombre por el que The Things Stack lo direcciona, en lugar de un DevEUI; se lee del dispositivo, nunca del comando. Los parámetros del comando son los mismos que para ChirpStack, y el downlink se añade al final de la cola del dispositivo.

En un downlink **sin confirmar**, `SUCCEEDED` vuelve a significar que el servidor de red lo aceptó. En uno **confirmado**, la plataforma da a cada downlink un identificador propio (The Things Stack no devuelve ninguno), enviado como un correlation id que empieza por `dc:`, y cierra el comando con los eventos que The Things Stack publica para ese downlink. En el webhook de la aplicación, activa los eventos de downlink y da a cada uno su propia ruta bajo la URL de la integración, con una ruta que **empiece por `downlink`**: por ejemplo `/downlink-ack`, `/downlink-nack`, `/downlink-sent` y `/downlink-failed`.

| Qué informa The Things Stack | Comando |
| --- | --- |
| `downlink_ack`: el dispositivo acusó la trama | `SUCCEEDED`. |
| `downlink_nack`: el dispositivo no la acusó | `TIMEOUT`, resultado desconocido. El dispositivo pudo oír la trama y perderse su acuse, así que enviarlo de nuevo puede actuar dos veces. Véase la nota sobre reintentos más abajo. |
| `downlink_sent`: una pasarela transmitió la trama | Se registra y el comando sigue esperando. |
| `downlink_failed`, y el propio servidor de red nombra un descarte antes de transmitir (un downlink confirmado puesto en cola para un dispositivo multicast), sin reintento ni transmisión informada antes | `FAILED`, no entregado. Es seguro volver a enviarlo. |
| Cualquier otro `downlink_failed` | `TIMEOUT`, resultado desconocido. La trama pudo transmitirse: véase abajo. |
| Nada antes del plazo | `TIMEOUT`, resultado desconocido. |

**Reintentos tras un nack.** Tras un `downlink_nack`, The Things Stack vuelve a poner en cola por sí mismo un downlink confirmado (hasta 8 veces más por defecto), y cada reintento es un downlink nuevo sobre el que el dispositivo puede actuar. DeviceChain cierra el comando en el primer nack, así que el dispositivo aún puede ejecutarlo después, y el acuse de un reintento llega con el comando ya cerrado y se descarta como sin correspondencia. Para mantener pequeña esa ventana, la plataforma establece `confirmed_retry.max_attempts` en 1 en cada downlink confirmado que envía (el valor más bajo que acepta The Things Stack), de modo que hay como máximo un reintento.

**Por qué solo algunos `downlink_failed` son «no entregado».** El servidor de red emite `downlink_failed` cuando descarta un downlink en cola que nunca programó (los casos de la tabla) y cuando una pasarela informa de que no pudo transmitir. El Application Server también lo emite, cuando renuncia a reintentar un downlink confirmado que **sí** se transmitió (se agotan los reintentos o falla volver a ponerlo en cola). Como el evento no dice cuál de los dos es, DeviceChain responde `FAILED` solo para los errores propios del servidor de red previos a la transmisión, y nunca para un downlink informado como transmitido o reintentado; cualquier otro `downlink_failed` es `TIMEOUT`. Eso incluye una sesión desconocida, una carga demasiado grande, un downlink caducado, un fallo de programación en la pasarela (hora absoluta o rutas fijas no válidas) y el error de transmisión propio de una pasarela: si una llamada de programación a una pasarela falla de forma ambigua, el servidor de red vuelve a poner el elemento en cola sin contar un reintento ni informar de transmisión, de modo que un descarte posterior por cualquiera de esos motivos no se distingue del de un elemento que nunca se intentó. Solo el caso multicast, una propiedad estática del dispositivo, es seguro.

Un informe se asocia a su comando solo por el correlation id `dc:` que puso la plataforma, y solo cuando nombra la aplicación y el dispositivo a los que se envió el downlink (y el dispositivo que resolvió la propia DeviceChain coincide), por la integración por la que se envió; los identificadores de traza propios del servidor de red no se usan nunca. Un downlink puesto en cola a mano en la consola de The Things Stack no lleva ese id, y sus eventos se ignoran. Las discrepancias y los informes sin correspondencia se cuentan y descartan como se describió arriba. Un webhook enviado a la URL base sin ruta no nombra ningún tipo de mensaje, así que sus informes se tratan como telemetría y pueden descartarse cuando el inquilino supera su techo de mensajes; da a cada tipo su propia ruta.

**Activarlo en una instalación en marcha.** El downlink se activa con una sola actualización de Helm, y mientras los pods se reinician, los pods de entrega de comandos que aún no se han reiniciado siguen haciendo fallar de inmediato un comando a un dispositivo de integración, como antes de que existiera el downlink, con `COMMAND_TRANSPORT_UNSUPPORTED`. No se envía nada por ese comando y es seguro emitirlo de nuevo cuando termine la actualización. Para evitar esa ventana, pausa los comandos a dispositivos de integración mientras dure la actualización. Un comando emitido en los minutos previos al primer arranque del adaptador se recoge igualmente cuando este arranca.

### Cualquier otro servidor de red (webhook) {#lorawan-downlink-webhook}

Para un servidor de red sin cliente nativo, publica un conector `lorawan_downlink` con `networkServer: "webhook"`. El dispositivo necesita un alias `lorawan.deveui`; la plataforma obtiene su DevEUI del dispositivo. Usa los mismos parámetros de comando o el codificador tipado descritos arriba. Exactamente un enlace publicado debe nombrar la integración entrante.

| Configuración | Significado |
| --- | --- |
| `networkServer` | Obligatorio: `webhook`. |
| `baseUrl` | Raíz de API HTTP(S) obligatoria con host literal; admite un prefijo de ruta. Sin consulta, fragmento ni información de usuario. |
| `integration` | Token obligatorio de la integración entrante. |
| `urlTemplate` | Ruta obligatoria, con consulta opcional, que empieza por `/`; máximo 1024 bytes. |
| `method` | `POST` (predeterminado), `PUT` o `PATCH`. |
| `contentType` | `application/json` (predeterminado), `text/plain` o `application/x-www-form-urlencoded`. |
| `bodyTemplate` | Cuerpo opcional, máximo 8192 bytes; puede estar vacío. Los cuerpos JSON deben producir JSON válido. |
| `headers` | Hasta 16 cabeceras personalizadas, cada valor de hasta 512 bytes. Los nombres son tokens HTTP literales; los valores admiten marcadores. |
| `authHeader` | Nombre de la cabecera de credencial; por defecto `Authorization`. |
| `authPrefix` | Por defecto `"Bearer "`; `""` explícito envía el secreto sin prefijo. Máximo 32 caracteres, sin caracteres de control. |
| Secreto del conector | Credencial de API obligatoria, cifrada y de solo escritura. El valor de la cabecera es `authPrefix + secret`. |

Se rechaza `applicationId` para webhook. Los campos específicos de webhook se rechazan para los tipos nativos de servidor. Los nombres de cabecera deben ser tokens HTTP válidos. Las cabeceras personalizadas no pueden definir `Host`, `Content-Length`, `Transfer-Encoding`, `Connection`, `Content-Type`, `Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie` ni el `authHeader` configurado (sin distinguir mayúsculas). `authHeader` no puede ser una cabecera de delimitación, tipo de contenido, conexión o cookies. Los valores de cabecera no pueden contener caracteres de control. Usa `contentType` para elegir el formato del cuerpo.

| Marcador | Valor generado |
| --- | --- |
| `{{devEui}}` | 16 dígitos hexadecimales en mayúsculas. |
| `{{fPort}}` | Puerto de aplicación decimal. |
| `{{payloadBase64}}` | Payload en base64 estándar con relleno. |
| `{{payloadHex}}` | Payload hexadecimal en minúsculas. |
| `{{confirmed}}` | `true` o `false`. |
| `{{correlationId}}` | `dc:` generado por la plataforma seguido de 32 dígitos hexadecimales en minúsculas. |

Solo se aceptan estos nombres exactos. Se rechazan espacios dentro del marcador, marcadores desconocidos o incompletos y `{{` literal; las llaves sueltas son literales para JSON. Los valores en rutas URL se escapan como segmentos de ruta y los de consulta como componentes de consulta; los valores del cuerpo y las cabeceras se insertan sin escape. La sustitución se hace en una sola pasada. La URL o el cuerpo debe contener `{{devEui}}`.

Por ejemplo, con el secreto del conector establecido a la credencial de API:

```json
{
  "networkServer": "webhook",
  "baseUrl": "https://ns.example.com/api",
  "integration": "my-integration-token",
  "urlTemplate": "/devices/{{devEui}}/downlinks",
  "method": "POST",
  "contentType": "application/json",
  "bodyTemplate": "{\"devEui\":\"{{devEui}}\",\"fPort\":{{fPort}},\"data\":\"{{payloadBase64}}\",\"confirmed\":{{confirmed}},\"ref\":\"{{correlationId}}\"}",
  "headers": {
    "X-Correlation-Id": "{{correlationId}}"
  },
  "authHeader": "Authorization",
  "authPrefix": "Bearer "
}
```

Un comando `{"fPort":10,"hex":"0a0b","confirmed":false}` para el DevEUI `0011223344556677` se envía a `https://ns.example.com/api/devices/0011223344556677/downlinks` con `data: "Cgs="`, `fPort: 10`, `confirmed: false` y la referencia generada en el cuerpo y la cabecera. Adapta los nombres de campo a la API del servidor.

La llamada usa la política de salida; los destinos privados necesitan una autorización del operador. Nunca se siguen redirecciones ni se usan las variables de entorno del proxy. Las plantillas no pueden cambiar el esquema o host base, añadir información de usuario o fragmentos, ni usar `..` o barras inversas. La credencial se establece al final y nunca aparece en errores; solo se muestra el estado HTTP, nunca el texto de respuesta del servidor.

| Resultado del envío | Resultado del comando |
| --- | --- |
| HTTP 2xx | Aceptado. Sin confirmar: `SUCCEEDED`, etapa `queued`; confirmado: permanece `SENT` esperando un ack. La aceptación en cola no demuestra la entrega. |
| Configuración/parámetros inválidos, falta de DevEUI, rechazo de salida, fallo DNS/conexión antes de escribir u otros HTTP 4xx | `FAILED`, no entregado (`UNSENT`); es seguro volver a enviarlo. |
| HTTP 3xx, 408, 499, 5xx o fallo ambiguo después de escribir | `TIMEOUT`, resultado desconocido (`UNANSWERED`); reenviar puede ejecutar dos veces. |

**Los downlinks confirmados requieren `{{correlationId}}` en la URL, el cuerpo o un valor de cabecera.** En caso contrario, el comando se rechaza antes del despacho. El servidor de red o su middleware debe conservar esa referencia y enviar un acuse de vuelta. El plazo pendiente es la caducidad del comando, limitado por defecto a 24 horas.

**Contrato del ack.** Envía un POST a la [URL de integración JSON genérica](#generic-json) autenticada, añadiendo `ack` o un segmento de ruta que empiece por `downlink` (por ejemplo `/downlink-ack` o `/downlink-nack`; la comparación ignora mayúsculas y `-`, `_`, `/`). Usa `Content-Type: application/json` y el token bearer de la integración. `idFrom` identifica al dispositivo igual que en los uplinks: `header:`, `query:` o `pointer:` dentro de este único objeto ack. Si `idFrom` es `pointer:/devEui`, añade `"devEui":"0011223344556677"` a este cuerpo:

```json
{"correlationId":"dc:0123456789abcdef0123456789abcdef","status":"acknowledged"}
```

`correlationId` es obligatorio, empieza por `dc:`, contiene hexadecimal en minúsculas tras el prefijo y tiene como máximo 100 caracteres. `status` es obligatorio y distingue mayúsculas. Las rutas ack de integraciones binarias genéricas se ignoran. El cuerpo es un único objeto JSON sujeto a los límites normales de solicitud y elemento; no se aplica la configuración de arreglos de uplinks.

| Ack `status` | Resultado del comando |
| --- | --- |
| `acknowledged` | `SUCCEEDED`, etapa `acknowledged`. |
| `timeout` | `TIMEOUT`, resultado desconocido; el acuse puede haberse perdido. |
| `failed` | `TIMEOUT`, resultado desconocido; este informe no demuestra que la trama nunca se transmitió. |
| Ningún informe antes del plazo | `TIMEOUT`, resultado desconocido. |

El informe debe coincidir en id de correlación, integración y token de dispositivo resuelto por la plataforma. Los informes de otro dispositivo se descartan y cuentan como `mismatch`; los elementos pendientes inexistentes o ya cerrados se descartan y cuentan como `unmatched` en `lpwan_downlink_reports_total`, como arriba. Las rutas ack usan el limitador de informes de downlink y no se descartan con la telemetría. La URL base de integración sin ruta no nombra ningún tipo ack.


### Codificar comandos a partir de parámetros tipados {#lorawan-command-encoder}

Una definición de comando puede incluir una cadena JSON `encoder` opcional. Publica
el perfil para activarla. Se aplica a ChirpStack v4, The Things Stack v3 y los enlaces webhook.
El codificador vigente al despachar el comando es el que se aplica; una nueva
publicación se aplica a los comandos despachados después de ella, incluidos los que
ya estaban en cola. La definición publicada se consulta de nuevo en cada despacho,
sin conservar el codificador en caché entre publicaciones. Si la consulta falla o
devuelve una respuesta incompleta, se reintenta: el comando queda pendiente y no se
envía nada. Un problema definitivo de la definición (codificador inválido,
definiciones de comando duplicadas o comando ausente de un vocabulario publicado
restringido) rechaza el comando como **no enviado**.
Este documento concatena constantes, números escalados, booleanos y bits:

```json
{
  "fPort": 10,
  "confirmed": false,
  "fields": [
    {"const": "01ff"},
    {"param": "level", "type": "u16be", "scale": 0.1, "offset": 0},
    {"param": "enabled", "type": "bool"},
    {"bits": {"type": "u8", "fields": [
      {"param": "mode", "shift": 0, "width": 3},
      {"param": "armed", "shift": 3, "width": 1}
    ]}}
  ]
}
```

`fPort` es obligatorio, de 1 a 223. `confirmed` vale false por defecto y mantiene
el requisito del canal de acuses indicado arriba. Se admiten 1–32 campos ordenados
y un total de 1–242 bytes. Las constantes son hexadecimales de longitud par.
Los tipos numéricos son `u8`, `i8`, `u16be`, `u16le`, `i16be`, `i16le`, `u32be`,
`u32le`, `i32be` e `i32le`. `bool` genera `00` o `01`. Los bits usan un tipo entero,
deben caber sin solaparse y dejan a cero las posiciones no especificadas.

Para un termostato, declara `setpoint` como `DOUBLE` y `mode` como `INT` en
`parameterSchema`, y usa este codificador:

```json
{
  "fPort": 12,
  "fields": [
    {"param": "setpoint", "type": "i16be", "scale": 0.1},
    {"bits": {"type": "u8", "fields": [
      {"param": "mode", "shift": 0, "width": 3}
    ]}}
  ]
}
```

El payload `{"setpoint":21.5,"mode":3}` produce `00d703` en hexadecimal, o `ANcD`
en base64, en el puerto 12. El cálculo invierte el decodificador:
`raw = (value - offset) / scale`. La escala vale 1 por defecto y su magnitud debe estar entre `1e-9` y `1e9`.
El desplazamiento vale 0 por defecto y su magnitud no puede superar `1e9`; ambos deben
ser finitos. Las combinaciones de escala y desplazamiento que dejan muy poca precisión
`float64` para la tolerancia de `1e-6` del entero raw se rechazan al guardar la
definición, aunque ambos valores cumplan sus límites individuales. Aumenta la
magnitud de la escala o reduce la del desplazamiento para corregirlo. El rango del
valor raw se comprueba antes de redondear, con una tolerancia de `1e-6` en los límites
para errores de coma flotante. Con escala 1 y
desplazamiento 0 se exige un entero exacto; otras transformaciones admiten una
tolerancia de `1e-6`. La magnitud de entrada no puede superar `1e15`, y las magnitudes
de entrada y raw deben ser menores que `2^53`. `215.4` con escala 1 se rechaza. NaN, infinito,
valores fuera de rango, tipos JSON incorrectos o parámetros ausentes rechazan el
comando como **no enviado**, antes de llamar a la red. Los valores predeterminados
del esquema son sugerencias de edición y nunca se insertan.

Cada parámetro del codificador debe ser único y estar declarado con un tipo escalar
compatible: `INT` o `DOUBLE` para números, `BOOLEAN` para booleanos y valores enteros
dentro de su anchura para bits (anchura 1 también admite `BOOLEAN`). Se rechazan las claves de payload no utilizadas y las claves JSON duplicadas.
Las claves desconocidas se rechazan. Un codificador requiere un esquema de
parámetros no vacío, que no puede declarar
`fPort`, `hex`, `base64` ni `confirmed`: el codificador controla puerto y confirmación.
Borrar `encoder` restaura el contrato raw. Una definición sin codificador conserva
sin cambios el contrato `fPort` más `hex` o `base64`.

## Qué puede hacer un token de integración {#trust}

Trata el token bearer como la contraseña de toda una flota. **Un token de integración habla en nombre de todos los dispositivos de sus tipos en alcance que tengan un alias.** La plataforma comprueba que el token pertenece a la integración y que el tipo del dispositivo está dentro del alcance de la integración, y lo vuelve a hacer cuando se procesa el evento, pero no puede saber si el remitente es sincero sobre de qué dispositivo viene un mensaje. Quien tenga el token puede enviar lecturas como cualquier dispositivo con alias dentro del alcance.

Por eso:

- Limita el alcance de una integración a los tipos de dispositivo que necesita, y usa una integración por servidor de red o por flota en lugar de una para todo.
- Rota el token si pudo quedar expuesto, y cuando se marche alguien que lo conocía.
- El token se almacena solo como un resumen con clave y no se vuelve a mostrar, ni en la consola ni por GraphQL. Un administrador tampoco puede leerlo.
- Los eventos de integración se marcan con la integración por la que llegaron, algo que un dispositivo no puede imitar: un dispositivo no puede enviar por MQTT ni por HTTP un evento que afirme haber llegado por una integración.
- La página Integraciones muestra cuándo falló por última vez la autenticación de una petición y marca un token rechazado.

Consulta el [modelo de seguridad](../concepts/security-model.md).

## No incluido en la versión 1 {#not-supported}

- **Payloads firmados.** No hay comprobación de HMAC ni de firma en una petición. El token bearer es la única credencial, y el transporte debe ser TLS.
- **Mensajes join, status y de ubicación.** Se confirman y se ignoran.
- **Creación automática de dispositivos.** Un identificador desconocido se lista, no se crea. Añades el dispositivo tú mismo, desde la lista.
- **Decodificar un cuerpo binario genérico sin decodificador.** Se acepta y se cuenta, no se decodifica, salvo que el tipo del dispositivo tenga un [decodificador de payload](./payload-decoders.md).

## Solución de problemas {#troubleshooting}

| Lo que ves | Causa probable |
| --- | --- |
| El remitente informa `401` | El token es incorrecto o se rotó; el token de integración de la URL no coincide con la integración a la que pertenece el token bearer; o el segmento del inquilino es incorrecto. La integración muestra **Token rechazado**. |
| El remitente informa `403` | La integración está desactivada (**Aceptando datos**). |
| El remitente informa `415` | La codificación de payload de ChirpStack es Protobuf. Cámbiala a JSON. |
| El remitente informa `400` | Lee el cuerpo de la respuesta; nombra el campo. Causas habituales: un DevEUI que no tiene 16 dígitos hexadecimales, un `deduplicationId` que no es un UUID y una hora de más de 366 días. |
| El remitente informa `202` pero no se guarda nada | El dispositivo no tiene un alias que coincida, o su tipo está fuera del alcance de la integración: mira en **Dispositivos no registrados vistos**. O el uplink no tenía nada que guardar: mira el recuento de no decodificados. O el inquilino superó su asignación: consulta `IntegrationShedAccepted`. |
| **Uplinks sin nada que leer** sigue creciendo | El perfil de dispositivo del servidor de red no tiene códec ni payload formatter, o su salida solo contiene cadenas. |
| Los valores se guardan sin unidad | No hay ninguna [métrica](../user-guide/device-types-and-profiles.md#métricas) declarada para esa clave en el perfil del dispositivo. La clave es la ruta unida con puntos (`battery.voltage`). |
| Un servidor de red alojado no consigue conectar | La instancia sirve un certificado autofirmado. Usa un certificado de una CA pública. |
| Todos los remitentes comparten un mismo límite de peticiones | Los proxies que hay delante de `event-sources` no son de confianza. Consulta [Direcciones de cliente y proxies de confianza](../deployment/bootstrap.md#trusted-proxies). |

## Relacionado {#related}

- [De un uplink LoRaWAN a una alarma](./lorawan-uplink-to-alarm.md): un recorrido desde un uplink binario, pasando por una integración y un decodificador, hasta una regla de detección y una alarma.
- [Decodificadores de payload](./payload-decoders.md): convierten una trama cruda o el objeto decodificado de un servidor de red en mediciones.
- [Dispositivos](../user-guide/devices.md): el formulario del dispositivo, con su campo **Alias**.
- [Conexión de un dispositivo](./connecting-a-device.md): las vías MQTT y HTTP que usa un dispositivo por sí mismo.
- [Matriz de capacidades por transporte](../reference/transport-matrix.md): qué admite y qué no admite cada vía de entrada.
- [Referencia de la API GraphQL](../reference/graphql-api.md#integrations): las operaciones de integración.
