---
title: Enviar uplinks de ChirpStack a DeviceChain
description: Conecta una aplicación de ChirpStack v4 a DeviceChain con su integración HTTP y decodifica los uplinks con un códec de ChirpStack o con un decodificador de carga útil en DeviceChain.
---

# Enviar uplinks de ChirpStack a DeviceChain {#send-chirpstack-uplinks}

ChirpStack v4 reenvía lo que oyen sus pasarelas mediante una **integración HTTP** en una aplicación: publica cada evento en la URL que le indiques, en JSON. DeviceChain recibe esas peticiones con una [integración de entrada](./inbound-integrations.md) de tipo de remitente **ChirpStack v4**, encuentra el dispositivo por su DevEUI y guarda las lecturas como mediciones de ese dispositivo. A partir de ahí alimentan paneles, reglas de detección y alarmas como cualquier otra medición.

El tipo de remitente ChirpStack v4 lee el formato de evento propio de ChirpStack. No escribes ningún adaptador, y un decodificador nunca tiene que desenvolver el sobre de ChirpStack: la plataforma toma el DevEUI de `deviceInfo.devEui`, la hora de `time`, el objeto decodificado de `object`, la trama sin procesar de `data` y el puerto y el contador de `fPort` y `fCnt`.

Lo que queda por decidir es **dónde se decodifica la trama**:

| | Opción A: decodifica ChirpStack | Opción B: decodifica DeviceChain |
| --- | --- | --- |
| Dónde vive la disposición de los bytes | Un códec JavaScript en el perfil de dispositivo de ChirpStack | Un [decodificador de carga útil](./payload-decoders.md) en el perfil de dispositivo de DeviceChain |
| Qué guarda DeviceChain | Los números y booleanos del `object` de ChirpStack | Lo que produce el decodificador a partir de `data` |
| Versiones y pruebas | Las de ChirpStack | Se publica con el perfil, con vectores de prueba guardados que deben pasar antes de poder publicarlo |

Las dos usan la misma integración y la misma configuración de ChirpStack. Los pasos son iguales hasta el [paso 5](#5-decoding), donde eliges una.

## Antes de empezar {#before-you-start}

- Un servidor ChirpStack v4 con una aplicación, un perfil de dispositivo y dispositivos que ya se han unido a la red. Necesitas el DevEUI de cada dispositivo.
- Una instancia de DeviceChain que ChirpStack pueda alcanzar por HTTPS. A un ChirpStack que gestionas tú se le puede dar la CA de tu instancia; uno alojado por terceros necesita que la instancia tenga un certificado de una CA pública.
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

- **Tipo de remitente**: **ChirpStack v4**. No se puede cambiar después.
- **Token**: el nombre de la integración en la URL, como `chirpstack-eu868`. No se puede cambiar después.
- **Tipos de dispositivo**: `Room sensor`.
- **Identificador del dispositivo**: **DevEUI**.
- **Guardar métricas del enlace**: actívalo si quieres guardar junto a las lecturas de cada uplink la intensidad de señal, la relación señal-ruido y el número de pasarelas, como `lorawan.rssi`, `lorawan.snr` y `lorawan.gateways`.

Elige **Crear integración**. El diálogo **Integración creada** muestra el **token de portador** una sola vez. Cópialo ahora: solo se guarda como un resumen y no se puede volver a mostrar. Si lo pierdes, **Rotar token** emite uno nuevo y el anterior deja de funcionar de inmediato.

Con GraphQL, `createIntegration` devuelve el token en `issuedToken`:

```graphql
mutation {
  createIntegration(request: {
    token: "chirpstack-eu868"
    name: "ChirpStack EU868"
    type: "CHIRPSTACK_V4"
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

La dirección de la integración es:

```text
https://<host>/api/integrations/<tenant>/<integration>
```

`<host>` es el host en el que abres la consola, `<tenant>` es el token de tu inquilino e `<integration>` es el token de la integración. La página de la integración en la consola muestra la URL completa en **Conectar el remitente**, con un botón de copia.

## 3. Añade la integración HTTP en ChirpStack {#3-chirpstack-http}

En la interfaz web de ChirpStack, abre la aplicación, ve a **Integrations** y añade la integración **HTTP**:

| Ajuste de ChirpStack | Valor |
| --- | --- |
| Payload encoding | **JSON**. DeviceChain rechaza la codificación Protobuf con `415`. |
| Event endpoint URL(s) | `https://<host>/api/integrations/<tenant>/<integration>`, sin nada detrás. ChirpStack añade por sí mismo `?event=up` (u otro nombre de evento) a cada petición. |
| Headers | Una cabecera: nombre `Authorization`, valor `Bearer <bearer-token>`. |

Guárdala. A partir de ese momento ChirpStack publica en DeviceChain cada evento de los dispositivos de la aplicación. Solo se guardan los eventos `up`. `join`, `status`, `log` y los demás eventos se responden con `204` y se ignoran, y `ack` y `txack` solo se leen cuando el operador ha activado el [downlink LoRaWAN](./inbound-integrations.md#lorawan-downlink), así que puedes dejar todos los eventos activados.

El token se envía solo en la cabecera. No lo pongas en la URL, donde quedaría escrito en los registros de acceso.

## 4. Registra los dispositivos por DevEUI {#4-devices}

DeviceChain encuentra el dispositivo al que se refiere un uplink mediante un **alias** de dispositivo que contiene su DevEUI. Crea un dispositivo del tipo del paso 1 para cada sensor, con el alias `lorawan.deveui`. En la consola, usa el campo **Alias** del formulario del dispositivo: **Añadir alias**, **DevEUI LoRaWAN** y los 16 dígitos hexadecimales. Da igual mayúsculas o minúsculas. Con GraphQL:

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

También puedes dejar que los sensores te lo digan. Un uplink cuyo DevEUI no tiene ningún dispositivo de los tipos de la integración no se guarda, y su DevEUI aparece en la página de la integración en **Dispositivos no registrados vistos**. **Crear un dispositivo con este alias** abre el formulario del dispositivo con el DevEUI ya rellenado. Consulta [Dispositivos no registrados](./inbound-integrations.md#unregistered).

## 5. Elige dónde se decodifica la trama {#5-decoding}

### Opción A: ChirpStack decodifica con un códec {#option-a-codec}

En ChirpStack, abre el perfil de dispositivo que usan tus sensores, ve a su pestaña **Codec**, elige funciones JavaScript como códec de carga útil y escribe una función `decodeUplink`. ChirpStack la ejecuta en cada uplink y envía lo que devuelve en `data` como el `object` del evento de uplink. Para el sensor de sala, cuya trama de seis bytes en el puerto 2 contiene una temperatura con signo en décimas de grado, una humedad en pasos de medio punto porcentual, la batería en milivoltios y el contacto de puerta en el bit 0 del último byte:

```js
function decodeUplink(input) {
  var b = input.bytes;
  if (input.fPort !== 2 || b.length < 6) {
    throw new Error("unexpected frame on port " + input.fPort);
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

En el lado de DeviceChain no hay nada más que hacer: deja el perfil de dispositivo **sin** decodificador. DeviceChain aplana `object` en lecturas:

- cada número es una lectura, con su clave; un valor anidado toma como clave su ruta unida por puntos (`{"battery": {"mv": 3300}}` es `battery.mv`);
- `true` y `false` se guardan como `1` y `0`;
- las cadenas y `null` se omiten, así que un códec que devuelve `"status": "ok"` no guarda nada para ese campo.

Nombra las claves que devuelve el códec como las métricas que declaraste, o declara las métricas con las claves que devuelve el códec. [Qué se guarda](./inbound-integrations.md#what-is-stored) tiene las reglas completas.

Un uplink cuyo evento no trae `object`, o uno sin ningún número ni booleano, se acusa y no guarda nada. La integración lo cuenta como **sin decodificar** y conserva una muestra de su trama: así se ve un uplink de un perfil de dispositivo sin códec.

:::tip Reformar la salida de un códec
Si el códec de un fabricante devuelve claves o unidades distintas de las de tu perfil (`temp_f` en Fahrenheit, `bat.mv`), no tienes que cambiar el códec. Un decodificador de DeviceChain sobre la entrada **Objeto decodificado** puede renombrarlas y convertirlas; consulta [Reformar un objeto decodificado](./payload-decoders.md#example-tts).
:::

### Opción B: DeviceChain decodifica la trama sin procesar {#option-b-decoder}

Da al perfil de dispositivo un [decodificador de carga útil](./payload-decoders.md) y ChirpStack no necesita códec. Cada evento `up` trae la trama sin procesar en `data`, y la integración entrega al decodificador:

| Nombre en el decodificador | Del evento de ChirpStack |
| --- | --- |
| `frame` | `data`, los bytes de la trama |
| `fPort` | `fPort` |
| `fCnt` | `fCnt` |
| `decoded` | `object`, cuando ChirpStack tiene un códec |
| `link` | La recepción más fuerte de `rxInfo` y el número de pasarelas, cuando **Guardar métricas del enlace** está activado |

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

Prueba el decodificador antes de publicar. En el panel **Prueba** elige **Trama**, pega `00D75A0CE401` y pon `fPort` a 2, o elige **Cuerpo de webhook**, selecciona la integración y pega un evento completo tal como lo publica ChirpStack. Después **publica** el perfil. Un decodificador recién publicado tarda hasta un minuto aproximadamente en aplicarse.

Cuando el tipo de dispositivo tiene un decodificador, es el decodificador el que decide qué se guarda. Si ChirpStack también tiene un códec, su `object` no se guarda; solo está disponible para el decodificador como `decoded`. Una decodificación que falla no guarda nada de ese uplink y queda registrada como un evento fallido con el motivo `DECODE_FAILED`; la pestaña **Decodificador** del perfil lo muestra en **Estado** y **Fallos recientes**. [Rechazos del decodificador](./payload-decoders.md#refusal-reasons) enumera los motivos.

Un decodificador de DeviceChain no es JavaScript. Es una lista de expresiones tipadas sin bucles, así que el códec JavaScript de un fabricante no se puede pegar tal cual; sus lecturas de bytes se reescriben como filas del decodificador. [Decodificadores de payload](./payload-decoders.md) y la [referencia de funciones](../reference/decoder-functions.md) describen el lenguaje.

## 6. Comprueba que llegan los uplinks {#6-check}

Puedes probar la integración antes de configurar ChirpStack, con una petición con la misma forma que la que envía ChirpStack. Esta lleva un `object` decodificado (opción A) y la trama sin procesar en `data` (opción B), así que sirve para las dos; el `deduplicationId` debe ser un UUID, y uno nuevo cada vez, o el uplink se guarda una sola vez:

```bash
curl -i -X POST "https://<host>/api/integrations/<tenant>/chirpstack-eu868?event=up" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "deduplicationId": "'"$(uuidgen)"'",
    "deviceInfo": { "devEui": "70b3d57ed0050004" },
    "fPort": 2,
    "fCnt": 1,
    "data": "ANdaDOQB",
    "object": { "temperature": 21.5, "humidity": 45, "battery": 3300, "door": true }
  }'
```

`202 Accepted` significa que la petición se leyó y que el token es válido; no significa que se haya guardado nada. Después mira en DeviceChain:

- La página del dispositivo muestra las lecturas: `temperature` 21.5, `humidity` 45, `battery` 3300 y `door` 1.
- La página de la integración muestra **Token rechazado** si una petición falló en la autenticación, un recuento **sin decodificar** de los uplinks sin nada que guardar y **Dispositivos no registrados vistos** con los DevEUI que no tiene ningún dispositivo del ámbito.
- Con la opción B, la pestaña **Decodificador** del perfil muestra cuántos uplinks se decodificaron y cuántos fallaron.

En cuanto ChirpStack empieza a enviar, los uplinks reales aparecen del mismo modo. La página del dispositivo en ChirpStack muestra los eventos `up` que reenvió.

## Solución de problemas {#troubleshooting}

| Lo que ves | Causa probable |
| --- | --- |
| `401` | El token de portador es incorrecto o se rotó, o el token del inquilino o de la integración en la URL es incorrecto. La integración muestra **Token rechazado**. |
| `403` | La integración está desactivada. Vuelve a activar **Aceptando datos**. |
| `415` | La codificación de carga útil de la integración HTTP es Protobuf. Ponla en **JSON**. |
| `400` | El cuerpo de la respuesta nombra el campo: un DevEUI que no tiene 16 dígitos hexadecimales, un `deduplicationId` que no es un UUID, un `time` de hace más de 366 días o una petición sin `?event=` (una URL de endpoint que no termina donde ChirpStack puede añadirlo). |
| `202`, pero no se guarda nada | Ningún dispositivo de los tipos de la integración tiene el DevEUI (mira **Dispositivos no registrados vistos**), o el uplink no tenía nada que guardar (mira el recuento sin decodificar), o, con la opción B, la decodificación falló (mira la pestaña **Decodificador**). |
| Las lecturas llegan sin unidad | No hay ninguna métrica declarada en el perfil para esa clave, o el perfil no se ha publicado desde que se declaró. |
| ChirpStack no consigue conectar | ChirpStack no confía en el certificado de la instancia. |

[Por qué no aparecen los datos de mi dispositivo](./why-isnt-my-data-showing-up.md) recorre el registro que guarda la plataforma de lo que rechazó, y [Qué se le responde al remitente](./inbound-integrations.md#responses) enumera todas las respuestas.

## Siguientes pasos {#next-steps}

- Convierte lecturas en alarmas: [De un uplink LoRaWAN a una alarma](./lorawan-uplink-to-alarm.md) lleva el mismo sensor hasta una regla de detección, una alarma y un comando.
- Envía comandos de vuelta a través de ChirpStack: [downlink LoRaWAN](./inbound-integrations.md#lorawan-downlink).
- Entiende los duplicados y los reintentos: [Duplicados y reintentos](./inbound-integrations.md#duplicates). La clave es el `deduplicationId` de ChirpStack, así que una petición reintentada se guarda una sola vez.
- Protege el token: [Qué puede hacer un token de integración](./inbound-integrations.md#trust).

## Véase también {#see-also}

La documentación de ChirpStack para los ajustes de su lado (en inglés):

- [HTTP integration](https://www.chirpstack.io/docs/chirpstack/integrations/http.html): el endpoint, el parámetro de consulta `event` y la codificación JSON.
- [Event types](https://www.chirpstack.io/docs/chirpstack/integrations/events.html): el evento `up` y sus campos.
- [Device profiles](https://www.chirpstack.io/docs/chirpstack/use/device-profiles.html): la pestaña **Codec** y las funciones de códec JavaScript.
