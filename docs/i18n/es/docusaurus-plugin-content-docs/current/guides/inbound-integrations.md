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
2. **Crea la integración.** En la consola, abre **Integraciones** en la barra lateral y elige **Nueva integración**. Elige el **Tipo de remitente**, los **Tipos de dispositivo** que puede alcanzar y, para un servidor de red, el **Identificador del dispositivo**. Activa **Guardar métricas del enlace** si quieres datos de señal. Elige **Crear integración** y copia el token bearer del diálogo. Se muestra una sola vez; si lo pierdes, rótalo. La página Integraciones necesita el permiso `device:read` para abrirse y `device:write` para modificar.
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
| `custom.<name>` | Cualquier identificador propio, para un remitente genérico: de 1 a 128 caracteres sin caracteres de control. El nombre son letras minúsculas, dígitos, guiones y guiones bajos, hasta 63 caracteres. | Tal como se escribe. Importan las mayúsculas. |

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

## ChirpStack v4 {#chirpstack}

ChirpStack envía los uplinks mediante una **integración HTTP** en una aplicación.

1. En la consola, crea una integración con tipo de remitente **ChirpStack v4**, con los tipos de dispositivo y, si los quieres, las métricas del enlace. Copia el token bearer.
2. En ChirpStack, abre la aplicación, añade la integración **HTTP** y rellénala:

   | Campo de ChirpStack | Valor |
   | --- | --- |
   | Payload encoding | **JSON**. Una integración Protobuf se rechaza con `415`, y el mensaje nombra este ajuste. |
   | Event endpoint URL | `https://<host>/api/integrations/{tenant}/{integration}`. ChirpStack añade `?event=up` por sí mismo. |
   | Headers | Nombre `Authorization`, valor `Bearer <bearer-token>`. |

3. **Configura un códec en el perfil de dispositivo de ChirpStack** (un códec de payload en JavaScript). La plataforma guarda los valores que decodifica el servidor de red; no decodifica la trama cruda. Sin códec, ChirpStack envía la trama cruda y ningún objeto decodificado, y el uplink se [cuenta como no decodificado](#undecoded) en lugar de guardarse.
4. Añade los dispositivos en DeviceChain con su DevEUI como alias `lorawan.deveui`.

Solo se leen los uplinks. ChirpStack envía otros eventos (join, status, confirmaciones) a la misma URL con otro valor de `event`, y se responden con `204` y se ignoran. El `deduplicationId` del uplink debe ser un UUID, como siempre envía ChirpStack; cualquier otra cosa se rechaza con `400`. La hora del mensaje es el `time` que informa ChirpStack, o la hora a la que lo recibió la plataforma cuando el campo falta.

## The Things Stack v3 {#the-things-stack}

The Things Stack envía los uplinks mediante un **webhook** en una aplicación.

1. En la consola, crea una integración con tipo de remitente **The Things Stack v3**. En **Identificador del dispositivo** elige **DevEUI**, o **Id de dispositivo** si prefieres hacer coincidir el id del dispositivo de The Things Stack. Copia el token bearer.
2. En The Things Stack, abre la aplicación, añade un **Custom webhook**, fija el formato en **JSON** y rellénalo:

   | Campo de The Things Stack | Valor |
   | --- | --- |
   | Base URL | `https://<host>/api/integrations/{tenant}/{integration}` |
   | Additional header | Nombre `Authorization`, valor `Bearer <bearer-token>`. |
   | Uplink message | Activado, con la ruta `/uplink-message`. La plataforma también lee las rutas `/up`, `/uplink` y `/uplinks`. |

3. **Configura un payload formatter** en la aplicación o en el dispositivo (el repositorio de modelos de dispositivo ofrece uno para muchos sensores). La plataforma guarda lo que produce el formatter; no decodifica la trama cruda. Sin uno, el uplink se [cuenta como no decodificado](#undecoded).
4. Añade los dispositivos en DeviceChain con su DevEUI como alias `lorawan.deveui`, o con su id de dispositivo como alias `tts.device-id` si elegiste **Id de dispositivo**.

Un mensaje enviado a cualquier otra ruta (un join accept, una confirmación de downlink) se responde con `204` y se ignora, de modo que puedes activar otros tipos de mensaje sin cambiar nada en DeviceChain, aunque no hay motivo para hacerlo. La hora del mensaje es el `received_at` del uplink, que debe estar presente y no tener más de 366 días. La clave de deduplicación es el dispositivo, el contador de tramas y esa hora juntos, porque el contador se reinicia cuando un dispositivo vuelve a unirse.

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

Una integración binaria genérica acepta un cuerpo de bytes crudos, con el identificador en una cabecera o en un parámetro de consulta (`idFrom` es `header:` o `query:`; un cuerpo binario no tiene campos a los que apuntar). En la versión 1 la plataforma no tiene decodificador para esos bytes, así que cada uplink de este tipo se acepta, se [cuenta como no decodificado](#undecoded) y sus primeros 512 bytes se guardan como muestra en la integración. Úsala para ver qué envía un remitente antes de que exista un decodificador, no para guardar lecturas.

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
| The Things Stack v3 | El DevEUI del dispositivo, el contador de tramas y `received_at`. |
| Genérico, con `timePointer` | Un resumen del dispositivo y del elemento, más la hora del remitente. Un reintento lleva el mismo elemento y la misma hora, así que se guarda una vez. |
| Genérico, sin `timePointer` | Un resumen del dispositivo y del elemento, más la hora de recepción. Un reintento es una clave nueva, así que **se guarda dos veces**. |

La plataforma también descarta, antes de procesarla, una repetición exacta de un mensaje que llega dentro de los diez minutos posteriores al primero. Ninguna regla fusiona dos uplinks distintos que casualmente tengan los mismos valores.

## Dispositivos no registrados {#unregistered}

Cuando un mensaje nombra un identificador que no lleva ningún dispositivo dentro del alcance de la integración, la plataforma no lo guarda. En su lugar registra el identificador en la integración, para que veas qué dispositivos te están hablando sin que los hayas añadido. Abre la integración en la consola y mira en **Dispositivos no registrados vistos**: cada fila muestra el esquema, el identificador, cuántas veces se vio y cuándo. **Crear un dispositivo con este alias** abre el formulario del dispositivo con el alias y el tipo rellenados. Con GraphQL, la lista es `unregisteredIntegrationDevices(criteria)`.

- La lista guarda como máximo 1.000 identificadores por integración. Pasado ese punto, los nuevos se cuentan y no se listan.
- Un avistamiento se registra como máximo una vez por minuto para cada identificador, así que el recuento mide actividad y no es un número exacto de mensajes.
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
| `429` | Demasiados inicios de sesión fallidos para esta integración, o demasiadas peticiones sin autenticar desde una misma dirección. Reintenta pasado el intervalo de `Retry-After`. |
| `503` | La plataforma no puede comprobar el token ni recibir el mensaje ahora, o está aplicando contrapresión. Reintenta; una cabecera `Retry-After` significa que el mensaje con certeza no se guardó. |

Un cambio en una integración es rápido pero no instantáneo en el endpoint, que recuerda lo que averiguó sobre una integración hasta un minuto. Después de rotar un token o desactivar una integración, puede seguir respondiendo `202` hasta un minuto a un remitente que aún use el token anterior, pero nada de lo enviado en ese minuto se guarda. Después de volver a activar una integración, puede seguir respondiendo `403` hasta un minuto.

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

**Límites al tráfico sin autenticar.** Quien conozca la URL de una integración no puede usarla para gastar la asignación del inquilino ni el tráfico bueno de una integración: los tokens incorrectos se limitan por integración (una ráfaga de 30 y luego 1 por segundo), las peticiones que obligan a la plataforma a consultar una integración desconocida se limitan por dirección de origen (una ráfaga de 60 y luego 5 por segundo) y por réplica, y un front end `ingress-nginx` limita cada dirección a 200 peticiones por segundo. El tráfico constante de un servidor de red con un token válido no le cuesta nada a su dirección frente a estos límites. La alerta `IntegrationAuthFailuresSustained` salta cuando los fallos se mantienen diez minutos, que es el aspecto de un remitente que sigue usando un token rotado.

## Comandos a dispositivos de integración {#commands}

Un dispositivo que informa a través de una integración no tiene una conexión con DeviceChain por la que enviarle un comando, y la versión 1 no tiene downlink hacia la API de un servidor de red. Un comando a un dispositivo así **se acepta cuando lo emites** y luego **falla en el siguiente barrido de entrega** con `COMMAND_TRANSPORT_UNSUPPORTED`, porque la plataforma reconoce el transporte y no tiene forma de entregar por él. No espera en la cola hasta caducar.

Conviene conocer dos casos:

- Un dispositivo que además se conecta por MQTT se juzga por el último transporte por el que informó. Mientras su último informe fue a través de la integración, sus comandos fallan; cuando informa por MQTT, se entregan.
- Un dispositivo que nunca ha informado no tiene transporte registrado, así que su primer comando se despacha como de costumbre y caduca sin respuesta.

Para un dispositivo que debe recibir comandos, dale también una conexión MQTT. Consulta la [matriz de transportes](../reference/transport-matrix.md).

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

- **Decodificadores escritos por ti.** La plataforma guarda lo que decodificó el servidor de red. Aún no hay un lugar en DeviceChain donde escribir tu propio decodificador, así que el códec tiene que ejecutarse en ChirpStack o The Things Stack, o en tu remitente genérico.
- **Downlink.** No se pueden enviar comandos a un dispositivo a través de un servidor de red. Consulta [Comandos a dispositivos de integración](#commands).
- **Payloads firmados.** No hay comprobación de HMAC ni de firma en una petición. El token bearer es la única credencial, y el transporte debe ser TLS.
- **Mensajes join, status y de ubicación.** Se confirman y se ignoran.
- **Creación automática de dispositivos.** Un identificador desconocido se lista, no se crea. Añades el dispositivo tú mismo, desde la lista.
- **Decodificar un cuerpo binario genérico.** Se acepta y se cuenta, no se decodifica.

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

- [Dispositivos](../user-guide/devices.md): el formulario del dispositivo, con su campo **Alias**.
- [Conexión de un dispositivo](./connecting-a-device.md): las vías MQTT y HTTP que usa un dispositivo por sí mismo.
- [Matriz de capacidades por transporte](../reference/transport-matrix.md): qué admite y qué no admite cada vía de entrada.
- [Referencia de la API GraphQL](../reference/graphql-api.md#integrations): las operaciones de integración.
