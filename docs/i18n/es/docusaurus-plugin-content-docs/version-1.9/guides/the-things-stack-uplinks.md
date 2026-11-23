---
title: Enviar uplinks de The Things Network / The Things Stack a DeviceChain
description: Conecta una aplicación de The Things Stack, en The Things Network o en tu propio despliegue, a DeviceChain con un webhook personalizado, y decodifica los uplinks con un formateador de carga útil o con un decodificador de carga útil en DeviceChain.
---

# Enviar uplinks de The Things Network / The Things Stack a DeviceChain {#send-the-things-stack-uplinks}

The Things Stack es el servidor de red que hay detrás de The Things Network, The Things Stack Cloud y los despliegues propios, y todos ellos reenvían los uplinks del mismo modo: mediante un **webhook** en una aplicación, que publica cada mensaje en JSON en la URL que le indiques. DeviceChain recibe esas peticiones con una [integración de entrada](./inbound-integrations.md) de tipo de remitente **The Things Stack v3**, encuentra el dispositivo por su DevEUI o por su id de dispositivo y guarda las lecturas como mediciones de ese dispositivo. A partir de ahí alimentan paneles, reglas de detección y alarmas como cualquier otra medición.

El tipo de remitente The Things Stack v3 lee el formato de mensaje propio de The Things Stack. No escribes ningún adaptador, y un decodificador nunca tiene que desenvolver el sobre de The Things Stack: la plataforma toma el dispositivo de `end_device_ids`, la hora de `received_at`, el objeto decodificado de `uplink_message.decoded_payload`, la trama sin procesar de `uplink_message.frm_payload` y el puerto y el contador de `f_port` y `f_cnt`.

Lo que queda por decidir es **dónde se decodifica la trama**:

| | Opción A: decodifica The Things Stack | Opción B: decodifica DeviceChain |
| --- | --- | --- |
| Dónde vive la disposición de los bytes | Un formateador de carga útil en la aplicación o en el dispositivo final | Un [decodificador de carga útil](./payload-decoders.md) en el perfil de dispositivo de DeviceChain |
| Qué guarda DeviceChain | Los números y booleanos de `decoded_payload` | Lo que produce el decodificador a partir de `frm_payload` |
| Versiones y pruebas | Las de The Things Stack | Se publica con el perfil, con vectores de prueba guardados que deben pasar antes de poder publicarlo |

Las dos usan la misma integración y el mismo webhook. Los pasos son iguales hasta el [paso 5](#5-decoding), donde eliges una.

## Antes de empezar {#before-you-start}

- Una aplicación de The Things Stack con dispositivos finales que ya se han unido a la red. Necesitas el DevEUI de cada dispositivo, o su id de dispositivo final si prefieres identificarlos por él.
- Una instancia de DeviceChain con un certificado de una CA pública. The Things Network y The Things Stack Cloud no publican en una instancia con un certificado autofirmado; a un despliegue que gestionas tú se le puede dar en su lugar la CA de tu instancia.
- Un miembro del inquilino con el permiso `device:write`. Los pasos de consola usan la consola del inquilino; cada paso tiene también su forma en GraphQL, que se envía a `https://<host>/api/device-management/graphql` con un token de acceso del inquilino en la cabecera `Authorization: Bearer` ([cómo obtener uno](./lorawan-uplink-to-alarm.md#1-get-a-tenant-token)).

## 1. Un tipo de dispositivo para los sensores {#1-device-type}

La integración solo puede alcanzar dispositivos de los tipos de dispositivo a los que la limitas, así que empieza por un tipo de dispositivo y su perfil. Declara una [métrica](../user-guide/device-types-and-profiles.md#metrics) por cada valor que informa el sensor, con la clave que tendrá el valor decodificado: la métrica da a la lectura su unidad y su validación. Esta guía usa un sensor de sala que informa una temperatura, una humedad, la tensión de la batería en milivoltios y un contacto de puerta:

| Clave de la métrica | Tipo de datos | Unidad |
| --- | --- | --- |
| `temperature` | Double | `Cel` |
| `humidity` | Double | `%` |
| `battery` | Integer | `mV` |
| `door` | Boolean | |

En la consola, crea el perfil y sus métricas como se describe en [Tipos y perfiles de dispositivo](../user-guide/device-types-and-profiles.md), crea un tipo de dispositivo que use el perfil y **publica** el perfil. Con GraphQL, envíalo en dos peticiones, porque una mutación puede seleccionar como máximo [cinco campos raíz](../reference/graphql-api.md#request-limits) y con un sexto se rechaza la petición entera:

```graphql
mutation {
  createDeviceProfile(request: { token: "room-sensor", name: "Room sensor", category: "sensor" }) { token }
  temperature: createMetricDefinition(request: { token: "room-sensor-temperature", deviceProfileToken: "room-sensor", metricKey: "temperature", name: "Temperature", dataType: "DOUBLE", unit: "Cel" }) { metricKey }
  humidity: createMetricDefinition(request: { token: "room-sensor-humidity", deviceProfileToken: "room-sensor", metricKey: "humidity", name: "Humidity", dataType: "DOUBLE", unit: "%" }) { metricKey }
  battery: createMetricDefinition(request: { token: "room-sensor-battery", deviceProfileToken: "room-sensor", metricKey: "battery", name: "Battery", dataType: "INT", unit: "mV" }) { metricKey }
  door: createMetricDefinition(request: { token: "room-sensor-door", deviceProfileToken: "room-sensor", metricKey: "door", name: "Door", dataType: "BOOLEAN" }) { metricKey }
}
```

```graphql
mutation {
  createDeviceType(request: { token: "room-sensor", name: "Room sensor", profileToken: "room-sensor" }) { token }
}
```

Si eliges la [opción B](#option-b-decoder), añade el decodificador antes de publicar; si no, publica ahora:

```graphql
mutation { publishDeviceProfile(token: "room-sensor", label: "metrics") { version } }
```

Una clave decodificada sin métrica se guarda igualmente, sin unidad.

## 2. Crea la integración {#2-integration}

En la consola, abre **Conectar → Integraciones** y elige **Nueva integración**:

- **Tipo de remitente**: **The Things Stack v3**. No se puede cambiar después.
- **Token**: el nombre de la integración en la URL, como `ttn-eu1`. No se puede cambiar después.
- **Tipos de dispositivo**: `Room sensor`.
- **Identificador del dispositivo**: **DevEUI**, o **Id de dispositivo** para identificar los dispositivos por su id de dispositivo final en The Things Stack. Elige **Id de dispositivo** para los dispositivos registrados sin DevEUI: con **DevEUI**, un mensaje que no trae ninguno se rechaza con `400`.
- **Guardar métricas del enlace**: actívalo si quieres guardar junto a las lecturas de cada uplink la intensidad de señal, la relación señal-ruido y el número de pasarelas, como `lorawan.rssi`, `lorawan.snr` y `lorawan.gateways`.

Elige **Crear integración**. El diálogo **Integración creada** muestra el **token de portador** una sola vez. Cópialo ahora: solo se guarda como un resumen y no se puede volver a mostrar. Si lo pierdes, **Rotar token** emite uno nuevo y el anterior deja de funcionar de inmediato.

Con GraphQL, `createIntegration` devuelve el token en `issuedToken`:

```graphql
mutation {
  createIntegration(request: {
    token: "ttn-eu1"
    name: "The Things Network EU1"
    type: "TTS_V3"
    enabled: true
    deviceTypeTokens: ["room-sensor"]
    identity: "DEV_EUI"
    linkMetrics: true
  }) {
    integration { token type identity }
    issuedToken
  }
}
```

Para identificar por id de dispositivo, usa `identity: "DEVICE_ID"`. La dirección de la integración es:

```text
https://<host>/api/integrations/<tenant>/<integration>
```

`<host>` es el host en el que abres la consola, `<tenant>` es el token de tu inquilino e `<integration>` es el token de la integración. La página de la integración en la consola muestra la URL completa en **Conectar el remitente**, con un botón de copia.

## 3. Añade un webhook en The Things Stack {#3-webhook}

En la consola de The Things Stack, abre la aplicación, elige **Integrations → Webhooks**, luego **Add webhook**, y selecciona **Custom webhook**:

| Ajuste de The Things Stack | Valor |
| --- | --- |
| Webhook ID | Cualquier nombre, como `devicechain`. |
| Webhook format | **JSON**. |
| Base URL | `https://<host>/api/integrations/<tenant>/<integration>` |
| Additional headers | Una cabecera: nombre `Authorization`, valor `Bearer <bearer-token>`. |
| Enabled event types | **Uplink message**, activado, con la ruta `/uplink-message`. |

The Things Stack añade a la URL base la ruta de cada tipo de evento, así que los uplinks se publican en `.../<integration>/uplink-message`. DeviceChain lee un uplink en las rutas `/uplink-message`, `/uplink`, `/uplinks` y `/up`, y en la propia URL base cuando la ruta se deja vacía. Cualquier otra ruta, como un join accept o una resolución de ubicación, se responde con `204` y se ignora, salvo que traiga un informe sobre un downlink que envió DeviceChain (consulta [Downlink de The Things Stack](./inbound-integrations.md#lorawan-downlink-tts)). No hace falta activarlas.

Deja vacío **Downlink API key**; DeviceChain no lo usa. El token va solo en la cabecera. No lo pongas en la URL, donde quedaría escrito en los registros de acceso.

## 4. Registra los dispositivos {#4-devices}

DeviceChain encuentra el dispositivo al que se refiere un uplink mediante un **alias** de dispositivo. Crea un dispositivo del tipo del paso 1 para cada sensor, con el alias que lee el **Identificador del dispositivo** de la integración:

| Identificador del dispositivo | Esquema del alias | Valor |
| --- | --- | --- |
| DevEUI | `lorawan.deveui` (**DevEUI LoRaWAN** en la consola) | Los 16 dígitos hexadecimales. Da igual mayúsculas o minúsculas. |
| Id de dispositivo | `tts.device-id` (**Id de dispositivo del servidor de red** en la consola) | El id del dispositivo final, como `room-4-sensor`. |

En la consola, usa el campo **Alias** del formulario del dispositivo: **Añadir alias**, elige el esquema y escribe el valor. Con GraphQL:

```graphql
mutation {
  createDevice(request: {
    token: "room-4"
    name: "Room 4"
    deviceTypeToken: "room-sensor"
    aliases: [{ scheme: "lorawan.deveui", value: "70B3D57ED0050004" }]
  }) { token aliases { scheme value } }
}
```

También puedes dejar que los sensores te lo digan. Un uplink cuyo identificador no tiene ningún dispositivo de los tipos de la integración no se guarda, y el identificador aparece en la página de la integración en **Dispositivos no registrados vistos**. **Crear un dispositivo con este alias** abre el formulario del dispositivo con el alias ya rellenado. Consulta [Dispositivos no registrados](./inbound-integrations.md#unregistered).

## 5. Elige dónde se decodifica la trama {#5-decoding}

### Opción A: The Things Stack decodifica con un formateador de carga útil {#option-a-formatter}

En The Things Stack, define un **formateador de carga útil** de uplink en la aplicación, o en un dispositivo final, cuyo formateador prevalece sobre el de la aplicación. Muchos dispositivos tienen un formateador ya hecho en el LoRaWAN Device Repository. Para un formateador JavaScript propio, escribe una función `decodeUplink`; The Things Stack envía lo que devuelve en `data` como el `decoded_payload` del uplink. Para el sensor de sala, cuya trama de seis bytes en el puerto 2 contiene una temperatura con signo en décimas de grado, una humedad en pasos de medio punto porcentual, la batería en milivoltios y el contacto de puerta en el bit 0 del último byte:

```js
function decodeUplink(input) {
  var b = input.bytes;
  if (input.fPort !== 2 || b.length < 6) {
    return { errors: ["unexpected frame on port " + input.fPort] };
  }
  var t = (b[0] << 8) | b[1];
  if (t & 0x8000) t -= 0x10000;
  return {
    data: {
      temperature: t / 10,
      humidity: b[2] / 2,
      battery: (b[3] << 8) | b[4],
      door: (b[5] & 1) === 1
    }
  };
}
```

En el lado de DeviceChain no hay nada más que hacer: deja el perfil de dispositivo **sin** decodificador. DeviceChain aplana `decoded_payload` en lecturas:

- cada número es una lectura, con su clave; un valor anidado toma como clave su ruta unida por puntos (`{"battery": {"mv": 3300}}` es `battery.mv`);
- `true` y `false` se guardan como `1` y `0`;
- las cadenas y `null` se omiten, así que un formateador que devuelve `"label": "north"` no guarda nada para ese campo.

Nombra las claves que devuelve el formateador como las métricas que declaraste, o declara las métricas con las claves que devuelve el formateador. Un formateador del Device Repository decide sus propias claves; el **Live data** del uplink en la consola de The Things Stack las muestra. [Qué se guarda](./inbound-integrations.md#what-is-stored) tiene las reglas completas.

Un uplink sin `decoded_payload`, o uno sin ningún número ni booleano, se acusa y no guarda nada. La integración lo cuenta como **sin decodificar** y conserva una muestra de su trama: así se ve un uplink sin formateador de carga útil.

:::tip Reformar la salida de un formateador
Si un formateador devuelve claves o unidades distintas de las de tu perfil (`temp_f` en Fahrenheit, `bat.mv`), no tienes que cambiar el formateador. Un decodificador de DeviceChain sobre la entrada **Objeto decodificado** puede renombrarlas y convertirlas; [Reformar un objeto decodificado](./payload-decoders.md#example-tts) es exactamente ese caso.
:::

### Opción B: DeviceChain decodifica la trama sin procesar {#option-b-decoder}

Da al perfil de dispositivo un [decodificador de carga útil](./payload-decoders.md) y The Things Stack no necesita formateador. Cada uplink trae la trama sin procesar en `frm_payload`, y la integración entrega al decodificador:

| Nombre en el decodificador | Del mensaje de The Things Stack |
| --- | --- |
| `frame` | `uplink_message.frm_payload`, los bytes de la trama |
| `fPort` | `uplink_message.f_port` |
| `fCnt` | `uplink_message.f_cnt` |
| `decoded` | `uplink_message.decoded_payload`, cuando se ejecutó un formateador |
| `link` | La recepción más fuerte de `rx_metadata` y el número de pasarelas, cuando **Guardar métricas del enlace** está activado |

En la consola, abre el borrador del perfil, ve a la pestaña **Decodificador**, pon la entrada en **Trama binaria** y añade una sección para el **fPort** 2 con cuatro filas. Este es el documento que guarda, y el mismo documento se puede fijar con GraphQL mediante `setDeviceProfileDecoder(profileToken: "room-sensor", document: ...)`:

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "uplink",
      "when": "fPort == 2",
      "outputs": [
        { "key": "temperature", "expr": "double(i16be(frame, 0)) / 10.0" },
        { "key": "humidity", "expr": "double(u8(frame, 2)) / 2.0" },
        { "key": "battery", "expr": "u16be(frame, 3)" },
        { "key": "door", "expr": "bit(frame, 5, 0)" }
      ]
    },
    { "name": "other ports", "ignore": true }
  ],
  "vectors": [
    {
      "name": "room sensor",
      "input": { "frameHex": "00D75A0CE401", "fPort": 2 },
      "expect": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": 1 }
    }
  ]
}
```

La segunda sección acepta los uplinks de cualquier otro puerto y no guarda nada para ellos; sin ella, fallarían al decodificar con `no_section`. El vector es una prueba guardada: la publicación lo ejecuta y la rechaza si el decodificador no produce exactamente esas lecturas.

Prueba el decodificador antes de publicar. En el panel **Prueba** elige **Trama**, pega `00D75A0CE401` y pon `fPort` a 2, o elige **Cuerpo de webhook**, selecciona la integración, pega un mensaje completo tal como lo publica The Things Stack y pon el tipo de mensaje en `uplink-message`. Después **publica** el perfil. Un decodificador recién publicado tarda hasta un minuto aproximadamente en aplicarse.

Cuando el tipo de dispositivo tiene un decodificador, es el decodificador el que decide qué se guarda. Si también se ejecutó un formateador, su `decoded_payload` no se guarda; solo está disponible para el decodificador como `decoded`. Una decodificación que falla no guarda nada de ese uplink y queda registrada como un evento fallido con el motivo `DECODE_FAILED`; la pestaña **Decodificador** del perfil lo muestra en **Estado** y **Fallos recientes**. [Rechazos del decodificador](./payload-decoders.md#refusal-reasons) enumera los motivos.

Un decodificador de DeviceChain no es JavaScript. Es una lista de expresiones tipadas sin bucles, así que un formateador JavaScript no se puede pegar tal cual; sus lecturas de bytes se reescriben como filas del decodificador. [Decodificadores de payload](./payload-decoders.md) y la [referencia de funciones](../reference/decoder-functions.md) describen el lenguaje.

## 6. Comprueba que llegan los uplinks {#6-check}

Puedes probar la integración antes de configurar el webhook, con una petición con la misma forma que la que envía The Things Stack. Esta lleva un `decoded_payload` (opción A) y la trama sin procesar en `frm_payload` (opción B), así que sirve para las dos. `received_at` es obligatorio y no puede tener más de 366 días, así que el comando pone la hora actual:

```bash
curl -i -X POST "https://<host>/api/integrations/<tenant>/ttn-eu1/uplink-message" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "end_device_ids": {
      "device_id": "room-4-sensor",
      "application_ids": { "application_id": "my-application" },
      "dev_eui": "70B3D57ED0050004"
    },
    "received_at": "'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
    "uplink_message": {
      "f_port": 2,
      "f_cnt": 1,
      "frm_payload": "ANdaDOQB",
      "decoded_payload": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": true }
    }
  }'
```

`202 Accepted` significa que la petición se leyó y que el token es válido; no significa que se haya guardado nada. Después mira en DeviceChain:

- La página del dispositivo muestra las lecturas: `temperature` 21.5, `humidity` 45, `battery` 3300 y `door` 1.
- La página de la integración muestra **Token rechazado** si una petición falló en la autenticación, un recuento **sin decodificar** de los uplinks sin nada que guardar y **Dispositivos no registrados vistos** con los identificadores que no tiene ningún dispositivo del ámbito.
- Con la opción B, la pestaña **Decodificador** del perfil muestra cuántos uplinks se decodificaron y cuántos fallaron.

En cuanto el webhook está activado, los uplinks reales aparecen del mismo modo. El **Live data** de la aplicación en la consola de The Things Stack muestra los uplinks que reenvió.

## Solución de problemas {#troubleshooting}

| Lo que ves | Causa probable |
| --- | --- |
| `401` | El token de portador es incorrecto o se rotó, o el token del inquilino o de la integración en la URL base es incorrecto. La integración muestra **Token rechazado**. |
| `403` | La integración está desactivada. Vuelve a activar **Aceptando datos**. |
| `400` | El cuerpo de la respuesta nombra el campo: falta el DevEUI con **DevEUI** (elige **Id de dispositivo**), un id de dispositivo o un DevEUI con un formato incorrecto, falta `received_at` o tiene más de 366 días, o el cuerpo no es JSON (pon el formato del webhook en **JSON**). |
| `204`, y no se guarda nada | El uplink se publicó en una ruta que DeviceChain no lee como uplink. Usa `/uplink-message`. |
| `202`, pero no se guarda nada | Ningún dispositivo de los tipos de la integración tiene el identificador (mira **Dispositivos no registrados vistos**), o el uplink no tenía nada que guardar (mira el recuento sin decodificar), o, con la opción B, la decodificación falló (mira la pestaña **Decodificador**). |
| Las lecturas llegan sin unidad | No hay ninguna métrica declarada en el perfil para esa clave, o el perfil no se ha publicado desde que se declaró. |
| El webhook no consigue conectar | The Things Stack no confía en el certificado de la instancia. |

[Por qué no aparecen los datos de mi dispositivo](./why-isnt-my-data-showing-up.md) recorre el registro que guarda la plataforma de lo que rechazó, y [Qué se le responde al remitente](./inbound-integrations.md#responses) enumera todas las respuestas.

## Siguientes pasos {#next-steps}

- Convierte lecturas en alarmas: [De un uplink LoRaWAN a una alarma](./lorawan-uplink-to-alarm.md) lleva el mismo sensor hasta una regla de detección, una alarma y un comando.
- Envía comandos de vuelta a través de The Things Stack: [Downlink de The Things Stack](./inbound-integrations.md#lorawan-downlink-tts).
- Entiende los duplicados y los reintentos: [Duplicados y reintentos](./inbound-integrations.md#duplicates). Una petición reintentada trae el mismo DevEUI, el mismo contador de tramas y el mismo `received_at`, así que se guarda una sola vez.
- Protege el token: [Qué puede hacer un token de integración](./inbound-integrations.md#trust).

## Véase también {#see-also}

La documentación de The Things Stack para los ajustes de su lado (en inglés):

- [Creating webhooks](https://www.thethingsindustries.com/docs/integrations/webhooks/creating-webhooks/): el formulario del webhook personalizado, la URL base y las rutas de los tipos de evento.
- [Data formats](https://www.thethingsindustries.com/docs/integrations/data-formats/): el mensaje de uplink y sus campos.
- [Payload formatters](https://www.thethingsindustries.com/docs/integrations/payload-formatters/): los formateadores de la aplicación y del dispositivo final, y la función JavaScript `decodeUplink`.
