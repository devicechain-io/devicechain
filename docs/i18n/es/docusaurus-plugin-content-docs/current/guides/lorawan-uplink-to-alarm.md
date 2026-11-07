---
sidebar_position: 3.9
title: De un uplink LoRaWAN a una alarma
---

# De un uplink LoRaWAN a una alarma {#lorawan-uplink-to-alarm}

Al terminar esta página, un uplink LoRaWAN binario habrá atravesado la integración de un servidor de red, un decodificador que escribiste y publicaste lo habrá convertido en lecturas, habrá activado una regla de detección y habrá levantado una alarma que se despeja sola. No necesitas gateway ni sensor: el simulador hace de servidor de red y envía las mismas solicitudes que enviaría un ChirpStack o un The Things Stack reales. Los comandos son los que usarías con hardware real, y [la última sección](#real-network-server) explica qué cambia.

Calcula unos cuarenta y cinco minutos, la mayor parte para la instalación.

:::note Esta página requiere la v1.2
La decodificación de uplinks con un decodificador que publicas llegó en la v1.2, junto con las opciones de `dc-simulator` que se usan aquí (`--lorawan-binary` y `--lorawan-print-decoder`). En la v1.1 o anteriores, un uplink que no trae un objeto decodificado se confirma y no almacena nada, así que esta página no funciona a partir del [paso 3](#3-the-decoder). Comprueba la versión de la instancia antes de empezar.
:::

El camino que sigue el uplink:

```text
 dc-simulator                         DeviceChain
┌──────────────────────┐  HTTPS POST  ┌─────────────────────────────────────────────────────────────┐
│ plays ChirpStack or  │─────────────▶│ inbound integration ──▶ decoder on the device profile       │
│ The Things Stack     │  raw frame,  │   finds the device        six bytes ──▶ temperature,        │
│                      │  no decoded  │   by its DevEUI           humidity, battery.voltage, door   │
└──────────────────────┘  object      │                                   │                         │
                                      │                                   ▼                         │
                                      │ alarm room-too-warm ◀── detection rule  temperature > 25   │
                                      └─────────────────────────────────────────────────────────────┘
```

Un servidor de red con un códec o un payload formatter entrega a DeviceChain un objeto que ya decodificó, y la integración guarda sus números tal cual. Aquí el servidor de red no tiene códec para el sensor, así que reenvía los bytes crudos y el decodificador del perfil de dispositivo los lee. La disposición de bytes del dispositivo queda entonces en un solo lugar, que versionas, pruebas y reviertes junto con el resto del perfil.

## Qué necesitas {#requirements}

- Una instancia en marcha de la v1.2 o posterior en un portátil, instalada como en [Tu primer dispositivo](../quickstart/first-device.md): `dcctl install local --compact` y luego `dcctl bootstrap local devicechain --host localhost --no-tls --no-escrow`. La instancia se llama `devicechain`, que es lo que `dcctl` espera cuando omites `--instance`.
- `curl` y `jq`.
- `dc-simulator`, adjunto a cada versión desde la v1.2 junto a `dcctl`. Ponlo en tu `PATH` y compruébalo con `dc-simulator --lorawan-print-decoder`, que imprime un documento JSON.

Los comandos llaman a la instancia en `http://localhost`. Si tu consola está en otro puerto, define la dirección una sola vez y dale a `dcctl sim create` el mismo host y puerto con `--server`:

```bash
export DC_URL=http://localhost
```

Todo esto está también en [`examples/lorawan-uplink-to-alarm`](https://github.com/devicechain-io/devicechain/tree/main/examples/lorawan-uplink-to-alarm) como scripts, y su `run.sh` hace todo el recorrido con un solo comando.

## 1. Obtener un token de tenant {#1-get-a-tenant-token}

`dcctl sim create` crea un tenant y una identidad limitada a él, y escribe la contraseña de la identidad en un archivo de handshake. Úsalo para obtener un tenant donde trabajar:

```bash
dcctl sim create lora
```

El tenant es `sim-lora` y la identidad es `lora@sim.devicechain.local`.

La autenticación lleva dos llamadas: un inicio de sesión que devuelve un token de identidad y después el intercambio de ese token por uno limitado al tenant. La función de abajo envía una solicitud GraphQL a uno de los servicios de la instancia. Defínela una vez, porque todos los pasos posteriores la usan, y luego obtén el token:

```bash
gql() { # SERVICE TOKEN QUERY [VARIABLES-JSON]
  local v=${4:-'{}'}
  curl -sS -X POST "$DC_URL/api/$1/graphql" \
    -H 'Content-Type: application/json' ${2:+-H "Authorization: Bearer $2"} \
    -d "$(jq -nc --arg q "$3" --argjson v "$v" '{query:$q,variables:$v}')" | jq -c .
}

PW=$(jq -r .simPassword ~/.devicechain/sims/lora.json)
IDT=$(gql user-management '' 'mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}' \
  "$(jq -nc --arg p "$PW" '{e:"lora@sim.devicechain.local",p:$p}')" | jq -r .data.login.identityToken)
export DC_TOKEN=$(gql user-management '' 'mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}' \
  "$(jq -nc --arg t "$IDT" '{t:$t,n:"sim-lora"}')" | jq -r .data.selectTenant.accessToken)
```

El token dura 15 minutos. Cuando una llamada posterior falle con un error de autenticación, vuelve a ejecutar las últimas cuatro líneas.

## 2. Declarar lo que mide el sensor {#2-the-profile}

Un **tipo de dispositivo** apunta a un **perfil de dispositivo**, y el perfil declara las mediciones, sus unidades, el decodificador y las reglas. Crea el perfil y una métrica por cada lectura que envía el sensor:

```bash
gql device-management "$DC_TOKEN" 'mutation{createDeviceProfile(request:{token:"room-sensor",name:"Room sensor",category:"sensor"}){token}}'

metric() { # KEY TYPE UNIT
  gql device-management "$DC_TOKEN" 'mutation($r:MetricDefinitionCreateRequest){createMetricDefinition(request:$r){metricKey}}' \
    "$(jq -nc --arg k "$1" --arg t "$2" --arg u "$3" \
      '{r:({token:("room-sensor-"+($k|gsub("[.]";"-"))),deviceProfileToken:"room-sensor",metricKey:$k,name:$k,dataType:$t} + (if $u=="" then {} else {unit:$u} end))}')"
}
metric temperature DOUBLE Cel
metric humidity DOUBLE %
metric battery.voltage DOUBLE V
metric door BOOLEAN ''
```

```text
{"data":{"createDeviceProfile":{"token":"room-sensor"}}}
{"data":{"createMetricDefinition":{"metricKey":"temperature"}}}
{"data":{"createMetricDefinition":{"metricKey":"humidity"}}}
{"data":{"createMetricDefinition":{"metricKey":"battery.voltage"}}}
{"data":{"createMetricDefinition":{"metricKey":"door"}}}
```

Un decodificador solo puede escribir en métricas que el perfil declara como medidas, así que van primero. Las unidades `Cel` y `V` son códigos UCUM.

## 3. El decodificador {#3-the-decoder}

El sensor simulado envía seis bytes por el puerto LoRaWAN 2:

| Byte | Significado |
| --- | --- |
| 0 | Versión de la disposición, siempre `1` |
| 1 a 2 | Temperatura en décimas de grado, con signo, big-endian |
| 3 | Humedad en porcentaje |
| 4 | Tensión de la batería en décimas de voltio |
| 5 | El bit 0 es el contacto de la puerta |

El simulador imprime el documento de decodificador que lee esta disposición. Guárdalo en un archivo:

```bash
dc-simulator --lorawan-print-decoder | tee decoder.json
```

```json
{
  "kind": "CEL",
  "envVersion": 1,
  "input": "FRAME",
  "sections": [
    {
      "name": "telemetry",
      "when": "fPort == 2 && u8(frame, 0) == 1",
      "outputs": [
        {"key": "temperature", "expr": "double(i16be(frame, 1)) / 10.0"},
        {"key": "humidity", "expr": "double(u8(frame, 3))"},
        {"key": "battery.voltage", "expr": "double(u8(frame, 4)) / 10.0"},
        {"key": "door", "expr": "bit(frame, 5, 0)"}
      ]
    }
  ],
  "vectors": [
    {
      "name": "door open",
      "input": {"frameHex": "0100fa2d1f01", "fPort": 2},
      "expect": {"temperature": 25, "humidity": 45, "battery.voltage": 3.1, "door": 1}
    }
  ]
}
```

Léelo desde arriba:

- `"input": "FRAME"` hace que el decodificador lea los bytes crudos del uplink, con el nombre `frame`.
- Una **sección** se aplica cuando se cumple su condición `when`. Esta exige el puerto 2 y un primer byte igual a `1`, de modo que una trama con una versión de disposición para la que no has escrito sección se rechaza en lugar de leerse mal. La primera sección cuya condición se cumple es la que se ejecuta.
- Cada **salida** nombra una métrica y la expresión que calcula su valor. `i16be(frame, 1)` lee un número de 16 bits con signo en big-endian en el byte 1, y `bit(frame, 5, 0)` lee el bit menos significativo del byte 5.
- Un **vector** es un caso de prueba que viaja con el decodificador: esta trama debe producir exactamente estas lecturas.

La guía de [decodificadores de payload](./payload-decoders.md) describe cada parte. Antes de guardar el decodificador, ejecútalo. `testPayloadDecoder` evalúa el documento sobre una trama con el evaluador propio de la plataforma y no almacena nada:

```bash
gql device-management "$DC_TOKEN" 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok section readings{key value} vectors{name ok}}}' \
  "$(jq -nc --rawfile d decoder.json '{i:{profileToken:"room-sensor",document:$d,frameHex:"0100fa2d1f01",fPort:2}}')" \
  | jq '.data.testPayloadDecoder | {ok, section, readings: (.readings | map({(.key): .value}) | add), vectors}'
```

```json
{
  "ok": true,
  "section": "telemetry",
  "readings": {
    "temperature": "25",
    "humidity": "45",
    "battery.voltage": "3.1",
    "door": "1"
  },
  "vectors": [
    {
      "name": "door open",
      "ok": true
    }
  ]
}
```

La trama `0100fa2d1f01` es la versión 1, `00fa` = 250 décimas = 25 grados, 45 por ciento de humedad, `1f` = 31 décimas = 3,1 voltios y el bit de la puerta activo. Ahora acorta la trama para ver qué aspecto tiene un rechazo. Es la misma llamada con una trama de cuatro bytes y otra parte del resultado:

```bash
gql device-management "$DC_TOKEN" 'query($i:PayloadDecoderTestInput!){testPayloadDecoder(input:$i){ok error{reason path message offset}}}' \
  "$(jq -nc --rawfile d decoder.json '{i:{profileToken:"room-sensor",document:$d,frameHex:"0100fa2d",fPort:2}}')" \
  | jq -c .data.testPayloadDecoder
```

```json
{"ok":false,"error":{"reason":"eval_error","path":"sections[0].outputs[2].expr","message":"u8: reads 1 byte(s) at offset 4 of a 4-byte frame (offset 4)","offset":4}}
```

El motivo nombra la expresión que falló y el desplazamiento al que se pasó la lectura. Un uplink en vivo que falla así se rechaza por completo, y el [paso 10](#10-when-an-uplink-does-not-decode) muestra dónde se registra.

Guarda el decodificador en el borrador del perfil:

```bash
gql device-management "$DC_TOKEN" 'mutation($t:String!,$d:String!){setDeviceProfileDecoder(profileToken:$t,document:$d){token}}' \
  "$(jq -nc --rawfile d decoder.json '{t:"room-sensor",d:$d}')"
```

```text
{"data":{"setDeviceProfileDecoder":{"token":"room-sensor"}}}
```

En la consola, el mismo documento va en **Devices → Device Profiles →** tu perfil **→ Decoder**, donde la tabla de campos escribe estas expresiones por ti.

## 4. La regla y la publicación {#4-the-rule}

Una **regla de detección** vigila las lecturas de un perfil y actúa cuando una condición empieza a cumplirse. Esta levanta una alarma de advertencia cuando `temperature` supera 25. Guárdala como `rule.json`:

```json
{
  "name": "Room too warm",
  "description": "Above 25 degrees: raise a warning alarm. The alarm clears by itself when the reading drops back to 25 or below.",
  "type": "threshold",
  "severity": "warning",
  "when": { "metric": "temperature", "op": "gt", "threshold": 25 },
  "actions": [
    { "type": "raiseAlarm", "raiseAlarm": { "alarmKey": "room-too-warm" } }
  ]
}
```

Una regla se activa por flanco: la alarma se levanta cuando la condición empieza a cumplirse y se despeja cuando deja de cumplirse, y nadie escribe el despeje. La regla lee las lecturas que produjo el decodificador, no los bytes, así que es la misma regla sea cual sea el remitente o el decodificador de esas lecturas. La regla se añade al perfil como un documento JSON dentro de una cadena:

```bash
gql device-management "$DC_TOKEN" 'mutation($r:DetectionRuleCreateRequest!){createDetectionRule(request:$r){token}}' \
  "$(jq -nc --rawfile d rule.json '{r:{token:"room-too-warm",deviceProfileToken:"room-sensor",name:"Room too warm",enabled:true,definition:$d}}')"
```

```text
{"data":{"createDetectionRule":{"token":"room-too-warm"}}}
```

El decodificador y la regla siguen en el borrador. Ninguno llega a un uplink en vivo hasta que **publicas** el perfil, lo que congela el borrador como una versión:

```bash
gql device-management "$DC_TOKEN" 'mutation{publishDeviceProfile(token:"room-sensor",label:"decoder and rule"){version}}'
```

```text
{"data":{"publishDeviceProfile":{"version":1}}}
```

La publicación es donde la plataforma comprueba el perfil. Compila el decodificador contra las métricas que se publican, ejecuta los vectores guardados del decodificador y compila la regla. Rechaza la publicación nombrando la primera expresión o el primer vector que falla, de modo que un decodificador cuyo propio caso de prueba no pasa nunca entra en producción.

## 5. Un dispositivo, conocido por su DevEUI {#5-a-device}

Un servidor de red LoRaWAN nombra un dispositivo por su DevEUI. DeviceChain encuentra el dispositivo que lleva el mismo valor como **alias**. Crea un tipo de dispositivo para el perfil y un dispositivo con el alias:

```bash
gql device-management "$DC_TOKEN" 'mutation{createDeviceType(request:{token:"room-sensor",name:"Room sensor",profileToken:"room-sensor"}){token}}'

gql device-management "$DC_TOKEN" 'mutation{createDevice(request:{token:"room-4",name:"Room 4",deviceTypeToken:"room-sensor",aliases:[{scheme:"lorawan.deveui",value:"70B3D57ED0050004"}]}){token aliases{scheme value}}}'
```

```text
{"data":{"createDeviceType":{"token":"room-sensor"}}}
{"data":{"createDevice":{"token":"room-4","aliases":[{"scheme":"lorawan.deveui","value":"70B3D57ED0050004"}]}}}
```

El DevEUI `70B3D57ED0050004` es uno cuya temperatura simulada sube de 22,4 a 26,9 grados a lo largo de diez uplinks y luego vuelve a empezar. Eso es lo que hace que la alarma se levante y se despeje en menos de un minuto.

## 6. La integración {#6-the-integration}

Una **integración de entrada** es la dirección a la que publica un servidor de red. Está limitada a tipos de dispositivo, de modo que quien publique en ella solo alcanza dispositivos de esos tipos. Crea una para ChirpStack v4 y conserva el token portador que devuelve:

```bash
export INTEGRATION_TOKEN=$(gql device-management "$DC_TOKEN" 'mutation{createIntegration(request:{token:"chirpstack",name:"ChirpStack",type:"CHIRPSTACK_V4",enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}){integration{token type identity} issuedToken}}' \
  | jq -r .data.createIntegration.issuedToken)
```

El token se muestra una sola vez y solo se guarda como un resumen con clave. Este comando lo guarda en una variable de shell; guárdalo en un lugar seguro si lo vas a necesitar de nuevo. Si lo pierdes, `rotateIntegrationToken` emite otro. `linkMetrics:true` guarda la intensidad de señal, la relación señal-ruido y el número de gateways de cada uplink junto a sus lecturas.

La integración ya es accesible en `$DC_URL/api/integrations/sim-lora/chirpstack`: el tenant y después el token de la integración. Consulta [la URL](./inbound-integrations.md#url).

## 7. Enviar uplinks {#7-send-uplinks}

Ejecuta el simulador como un ChirpStack sin códec para el sensor: doce uplinks, con dos segundos entre ellos.

```bash
DC_SIM_INTEGRATION_TOKEN="$INTEGRATION_TOKEN" dc-simulator \
  --lorawan-ns chirpstack --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/chirpstack" \
  --deveui 70B3D57ED0050004 --uplinks 12 --emit-interval 2s
```

```json
{"ns":"chirpstack","sent":12,"resent":0,"statuses":{"202":12},"errors":0,"runId":"1793997768570354300"}
```

`--lorawan-binary` es lo que convierte esto en un servidor de red sin códec: cada solicitud lleva solo la trama cruda (`data`, en base64) y el puerto, sin objeto decodificado. El token entra por el entorno para que no aparezca en la lista de procesos. La línea de resumen es la respuesta de la plataforma, y las doce fueron `202 Accepted`. Si tu instancia enruta por un nombre de host que no se resuelve desde tu equipo, añade `--integration-host <host>`.

:::note Un decodificador recién publicado tarda hasta un minuto en aplicarse
`event-sources` recuerda durante aproximadamente un minuto qué tipos de dispositivo tienen decodificador. Un uplink enviado dentro de esa ventana todavía puede almacenarse como lo decodificó el servidor de red, y sin objeto decodificado eso significa almacenado como nada. Si recorriste los pasos anteriores con prisa, espera un minuto y vuelve a enviar.
:::

## 8. Ver el resultado {#8-see-the-result}

Dale unos segundos después de que el simulador termine. Las últimas lecturas del dispositivo, con las unidades que declara el perfil:

```bash
gql device-state "$DC_TOKEN" '{latestMeasurements(deviceToken:"room-4"){name value unit}}' \
  | jq -r '.data.latestMeasurements[] | "\(.name) = \(.value) \(.unit // "")"'
```

```text
battery.voltage = 3.1 V
door = 1 
humidity = 57 %
lorawan.gateways = 2 
lorawan.rssi = -80 
lorawan.snr = 8.5 
temperature = 22.9 Cel
```

Cuatro de las lecturas son del decodificador. Las tres `lorawan.` son las métricas de enlace que añade la integración. El último uplink, el número doce, marca 22,9: la temperatura del sensor superó 25 y volvió a bajar, y la alarma la siguió:

```bash
gql device-management "$DC_TOKEN" 'query($c:AlarmSearchCriteria!){alarms(criteria:$c){results{alarmKey severity state raisedTime clearedTime lastValue}}}' \
  '{"c":{"pageNumber":1,"pageSize":5,"originatorType":"device","originatorToken":"room-4"}}' \
  | jq '.data.alarms.results'
```

```json
[
  {
    "alarmKey": "room-too-warm",
    "severity": "WARNING",
    "state": "CLEARED",
    "raisedTime": "2026-11-06T20:43:01.144383Z",
    "clearedTime": "2026-11-06T20:43:09.16384Z",
    "lastValue": 25.4
  }
]
```

El uplink siete marcó 25,4 grados, el primero por encima de 25, y eso levantó la alarma. El uplink once marcó 22,4 y la despejó, cuatro uplinks y ocho segundos después. Nada en la regla dice cómo despejarla. En la consola, la alarma está en **Monitor → Alarms** y las lecturas están en la página del dispositivo.

Si no ves ninguna alarma, vuelve a enviar los doce uplinks pasado un minuto y lee [Por qué no aparecen los datos de mi dispositivo](./why-isnt-my-data-showing-up.md).

## 9. Lo mismo con The Things Stack {#9-the-things-stack}

The Things Stack envía la misma información con otra forma: la trama es `frm_payload`, el puerto es `f_port` y el tipo de mensaje es el último segmento de la ruta. Añade una segunda integración para él, limitada al mismo tipo de dispositivo, y ejecuta el simulador como un The Things Stack:

```bash
export TTS_TOKEN=$(gql device-management "$DC_TOKEN" 'mutation{createIntegration(request:{token:"tts",name:"The Things Stack",type:"TTS_V3",enabled:true,deviceTypeTokens:["room-sensor"],identity:"DEV_EUI",linkMetrics:true}){integration{token} issuedToken}}' \
  | jq -r .data.createIntegration.issuedToken)

DC_SIM_INTEGRATION_TOKEN="$TTS_TOKEN" dc-simulator \
  --lorawan-ns tts --lorawan-binary \
  --integration-url "$DC_URL/api/integrations/sim-lora/tts" \
  --deveui 70B3D57ED0050004 --uplinks 3 --emit-interval 1s
```

```json
{"ns":"tts","sent":3,"resent":0,"statuses":{"202":3},"errors":0,"runId":"1793997852808847000"}
```

El dispositivo, el decodificador y la regla son los de la primera ejecución. El decodificador lee `frame` y `fPort`, que aportan ambos remitentes, así que no cambió. Solo cambió la integración, y la integración es donde se entiende la forma del remitente.

## 10. Cuando un uplink no se decodifica {#10-when-an-uplink-does-not-decode}

Una decodificación que falla no almacena ninguna de las lecturas del uplink y se registra como un evento fallido con el motivo `DECODE_FAILED`. Envía uno a propósito: los mismos seis bytes, por el puerto 9, que la sección del decodificador no acepta. El simulador usa siempre el puerto 2, así que este se envía a mano, como lo enviaría ChirpStack:

```bash
curl -sS -i -X POST "$DC_URL/api/integrations/sim-lora/chirpstack?event=up" \
  -H "Authorization: Bearer $INTEGRATION_TOKEN" -H 'Content-Type: application/json' \
  -d '{"deduplicationId":"3f0c2f1e-6b1d-4a54-9a43-6a1f0f6f5e01","time":"'"$(date -u +%Y-%m-%dT%H:%M:%SZ)"'",
       "deviceInfo":{"devEui":"70b3d57ed0050004"},"fCnt":99,"fPort":9,"data":"AQD6LR8B"}' | head -1
```

```text
HTTP/1.1 202 Accepted
```

Al servidor de red se le dice `202`, porque la integración aceptó el mensaje. El rechazo es un registro de tu lado. Léelo con `dcctl events failed`, con el tenant, la identidad y su contraseña. Añade `--server` con tu host y puerto si la instancia no está en `localhost`:

```bash
dcctl events failed -t sim-lora --email lora@sim.devicechain.local --password "$PW"
```

```text
WHEN                         REASON         DEVICE  SOURCE             FOLDED  PAYLOAD    ERROR
2026-11-06T20:43:50.708786Z  DECODE_FAILED  room-4  device-management  0       AVAILABLE  decode_failed: no_section: no section matched this uplink

1 of 1 record(s).
```

El inicio del texto del error es el motivo de [la tabla de rechazos](./payload-decoders.md#refusal-reasons): aquí `no_section`, porque el puerto 9 no coincidió con ninguna sección. Una trama demasiado corta (`eval_error`) y un valor fuera de los límites de una métrica (`invalid_output`) tienen cada uno el suyo. La columna PAYLOAD indica si la plataforma conserva todavía el mensaje original. Los mismos registros están en **Monitor → Ingest failures** en la consola. El `deduplicationId` está fijo en este comando; para enviar otro fallo, cambia sus últimos dígitos, porque una repetición dentro de diez minutos se descarta antes de procesarse.

## El servidor de red real {#real-network-server}

Sustituye el simulador por ChirpStack o The Things Stack y el lado de DeviceChain queda como lo construiste. Lo que cambia está en el servidor de red:

- **ChirpStack v4.** En la aplicación, añade la integración **HTTP** con codificación de payload **JSON**, el endpoint de eventos `https://<host>/api/integrations/<tenant>/chirpstack` y la cabecera `Authorization: Bearer <token>`. El perfil de dispositivo no necesita códec: con un decodificador publicado, el uplink lo decodifica este, y lo que decodificó el servidor de red no se almacena.
- **The Things Stack v3.** En la aplicación, añade un **Custom webhook** con el formato **JSON**, la URL base `https://<host>/api/integrations/<tenant>/tts`, la cabecera `Authorization: Bearer <token>` y la ruta de mensaje de uplink `/uplink-message`. La aplicación no necesita payload formatter, por la misma razón.
- **Dispositivos.** Da a cada dispositivo en DeviceChain su DevEUI como alias `lorawan.deveui`, o su id de dispositivo como alias `tts.device-id` cuando la integración identifica los dispositivos por `DEVICE_ID`. Un DevEUI que ningún dispositivo lleva aparece en **Unregistered devices seen** de la integración, y desde esa lista puedes crear el dispositivo.
- **Un certificado público.** Un servidor de red alojado no confiará en un certificado autofirmado, así que la instancia necesita un certificado de una CA pública antes de que uno pueda publicar en ella. A un servidor de red que ejecutas tú se le puede dar tu CA.

El otro cambio son las tramas del propio sensor. El decodificador de esta guía lee la disposición del sensor simulado. Para un sensor real, escribe un decodificador a partir de su hoja de datos, pruébalo con tramas reales en `testPayloadDecoder` y guarda un vector por cada disposición que envíe, de modo que la publicación demuestre que el decodificador sigue leyéndolas. Los ajustes campo por campo de ambos remitentes están en [Integraciones de entrada](./inbound-integrations.md#chirpstack), y un decodificador sobre el objeto que un servidor de red ya decodificó es un ejemplo desarrollado en [Decodificadores de payload](./payload-decoders.md#example-tts).

## Limpieza {#clean-up}

La regla, el dispositivo y las integraciones pertenecen al tenant `sim-lora`. Para eliminar el tenant y su identidad:

```bash
dcctl sim destroy lora
```

Si has terminado con la instancia, `dcctl destroy local devicechain` la elimina.

## Relacionado {#related}

- [Decodificadores de payload](./payload-decoders.md): cada parte de un decodificador, la biblioteca de funciones, el panel de pruebas y los rechazos.
- [Integraciones de entrada](./inbound-integrations.md): los remitentes, los alias, lo que se almacena y los límites.
- [Reglas de detección](../user-guide/detection-rules.md) y [Alarmas](../user-guide/alarms.md): cómo escribir reglas y trabajar las alarmas que levantan.
- [Por qué no aparecen los datos de mi dispositivo](./why-isnt-my-data-showing-up.md): el registro de la plataforma de lo que rechazó.
