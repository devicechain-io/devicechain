---
sidebar_position: 2
title: Conexión de un dispositivo
---

# Conexión de un dispositivo

Los dispositivos se conectan a DeviceChain mediante **MQTT** (servido directamente por el servidor MQTT integrado de NATS en el puerto 1883 — sin broker independiente) o **HTTP**. Ambos transportes alimentan el mismo pipeline de decodificación → resolución → persistencia, por lo que el cuerpo del evento JSON es idéntico entre ambos.

:::note Estado
La ingesta por MQTT y HTTP están disponibles. **Las conexiones se protegen a nivel del broker:** los listeners de MQTT/NATS son **TLS**, y un auth-callout de NATS autentica cada conexión y la vincula a los subjects de ese único dispositivo, de modo que un dispositivo solo puede publicar sus propios eventos y leer sus propios comandos. La autenticación del dispositivo también se aplica **por evento** mediante credencial, y el modo de autenticación de dispositivo predeterminado es **`required`** — por lo que se espera una credencial tanto en la conexión como en el evento. Consulte [Credenciales de dispositivo](./device-credentials.md). Los dispositivos restringidos pueden conectarse en su lugar sobre CoAP/UDP con DTLS mediante la [ingesta LwM2M](../concepts/lwm2m.md), y las flotas heredadas mediante [Sparkplug B](../concepts/sparkplug.md); ambos autentican en el handshake del transporte en lugar de por evento. Aún están planificados un transporte WebSocket y el flujo completo de aprovisionamiento/reclamación de autoservicio.
:::

:::danger Tres identificadores, tres funciones — y a los tres se les llama «token» en algún sitio
Conectar un dispositivo por MQTT significa acertar con **tres identificadores distintos**. Cumplen
funciones diferentes, no son intercambiables, y a todos se les llama token o id en algún lugar de la
consola. Lea esto una vez y el resto de la página cobrará sentido.

**1. El token del dispositivo** — la identidad del dispositivo en el registro, p. ej. `sensor-001`.
Usted lo elige. Es el campo `device` del cuerpo del evento **y** el segmento `{token}` del topic, y
ambos deben coincidir: un evento que dice venir de un dispositivo distinto al de su topic se rechaza.

**2. El id de credencial** — lo que el dispositivo presenta para demostrar que es él mismo. La consola
lo etiqueta **Access token** en una credencial `ACCESS_TOKEN` y **Username** en `MQTT_BASIC`. Autentica
dos veces: en la conexión MQTT y de nuevo por evento en el pipeline.

  🔴 **El nombre de usuario MQTT no es el id de credencial por sí solo — es `{tenant}:{credentialId}`.**
  Copiar el valor «Username» de la consola directamente al cliente es el fallo de conexión más común.

**3. El client id de MQTT** — `{instanceId}:{tenant}:{deviceToken}`. Este **no aparece en ninguna parte
de la consola**, y el broker rechaza cualquier otro valor, incluido el aleatorio que inventa su
biblioteca cliente cuando usted no lo fija. Es una clave de sesión, no una etiqueta; vea
[MQTT](#mqtt) más abajo para saber por qué debe derivarse así y cómo abrir más de una conexión por
dispositivo.

**Cómo se tuercen las cosas.** Una conexión rechazada se **cierra, no se responde** — su cliente
informa de un reset o un EOF inesperado, nunca de un fallo de autorización, y un dispositivo que
reconecta automáticamente entrará en bucle. Así que los tres errores se ven idénticos desde el
dispositivo. Si un dispositivo no consigue conectar, revise el client id, luego el prefijo
`{tenant}:` del nombre de usuario, y después la credencial en sí — en ese orden, porque ese es el
orden de frecuencia con que cada uno es la causa.

Y un cuarto, más adelante: el `token` del sobre de un **comando** identifica al *comando*, no al
dispositivo. Devolver el token del dispositivo en una respuesta de comando no coincide con nada y la
respuesta se descarta — vea [Respuesta a un comando](#responding-to-a-command).
:::

## El cuerpo del evento

Todo evento entrante — sobre cualquier transporte — es un objeto JSON:

```json
{
  "device": "sensor-001",
  "eventType": "Measurement",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

- `device` — el token estable del dispositivo.
- `eventType` — `Measurement`, `Location` o `Alert` (también `NewRelationship`).
- `credentialType` / `credentialId` — la credencial que presenta el dispositivo. `MQTT_BASIC` además incluye `credentialSecret`. Omita esto solo cuando el modo de autenticación de dispositivo de la instancia esté configurado como `disabled` u `optional`; el **valor predeterminado es `required`**, por lo que se espera una credencial.
- `payload` — la forma depende de `eventType`, y todas las formas son `{ "entries": [ … ] }`. Vea a continuación.

### Formas del payload

**Todo payload envuelve su contenido en un arreglo `entries`**, y todo valor numérico es una **cadena
de texto JSON**. Ambas reglas se aplican: un payload sin entradas, una entrada vacía, o un número
suelto donde se espera una cadena es **rechazado** — HTTP responde `400` y una publicación MQTT va a
la cola de mensajes fallidos en lugar de aceptarse en silencio.

**Una entrada es una lectura, tomada en un instante.** Una entrada puede llevar su propio
`occurredTime`, y ese es el instante con el que la lectura se almacena, se grafica, se evalúa y se
devuelve —de modo que un dispositivo que acumula lecturas mientras está sin conexión puede subir
una serie acumulada, hasta el tope por mensaje descrito más abajo, y conservar el historial que
realmente registró. Una entrada sin
`occurredTime` toma la del sobre. `occurredTime` es RFC 3339 (`2026-08-09T12:00:00.125Z`) donde
aparezca; un valor que no lo sea es **rechazado** indicando la entrada culpable, nunca sustituido en
silencio.

Hay un valor RFC 3339 válido que se rechaza igualmente: **`0001-01-01T00:00:00Z`**, que la
plataforma reserva para significar «no se informó ninguna hora». Un dispositivo que quiera decir la
época debe enviar `1970-01-01T00:00:00Z`. Como todo rechazo de marca de tiempo, es terminal y se
lleva por delante **el mensaje entero** — todas las lecturas hermanas del mismo lote —, así que
conviene descartarlo en el firmware antes que descubrirlo en una cola de mensajes fallidos.

### Cuánto puede llevar un mensaje

**Un mensaje en los transportes de esta página admite como máximo 1000 lecturas.** El techo pertenece
al evento JSON de dispositivo descrito arriba: MQTT y HTTP. Los dos transportes a los que la
introducción de esta página dirige las flotas restringidas y de brownfield **no** lo comparten:
[LwM2M](../concepts/lwm2m.md) acota un solo Notify en 256 muestras, y
[Sparkplug B](../concepts/sparkplug.md) no aplica techo por mensaje alguno (véase [lo que un operador
debe saber](../deployment/edge-services.md#sparkplug-lo-que-un-operador-debe-saber)).

Una lectura es un dato almacenado: en mediciones,
una *clave de métrica* —así que una entrada con doce métricas son doce lecturas—; en ubicaciones y
alertas, una entrada. Contar claves en lugar de entradas es deliberado: una sola entrada puede
llevar miles de métricas, y son las lecturas, no las entradas, las que se convierten en filas
almacenadas, actualizaciones de estado y evaluaciones de reglas.

Ese abanico es la razón de ser del tope. El limitador de ingesta por inquilino mide *mensajes*, y
cobra lo mismo por un mensaje de una lectura que por uno de cuarenta mil, de modo que sin esto un
solo mensaje es un coste ilimitado que asume toda la instancia. Un dispositivo con un backlog más
profundo lo sube en varios mensajes.

Por encima del tope, el mensaje se **rechaza entero**, nunca se recorta para que quepa: un lote
recortado en silencio se respondería con `202` y las lecturas ausentes serían indetectables desde
ambos extremos. No se almacena nada, y tampoco se pierde nada: el mensaje se enruta íntegro al flujo
de decodificación fallida.

Cómo se entera uno depende del transporte, y la diferencia importa:

- **HTTP** responde `400`, indicando el número de lecturas y el tope.
- **MQTT** no le dice nada al dispositivo. El broker confirma una publicación cuando la captura de
  forma duradera, es decir antes de decodificarla, así que un `PUBACK` no promete que el mensaje
  fuera aceptado: un rechazo posterior solo es visible para el operador.

Los operadores ven todos los rechazos en el contador `total_msg_too_many_readings`, y el tope es un
ajuste de operador (`maxReadingsPerMessage`) para una instancia cuya flota necesite realmente otro
valor. Bajarlo no reescribe el historial, pero sí se aplica a lo que siga en cola: los mensajes ya
capturados y aún sin decodificar se rechazan con el valor nuevo.

:::caution Un lote muy acumulado se almacena entero, pero la detección puede no verlo todo
«Una entrada es una lectura, tomada en un instante» habla de *almacenamiento*, y ahí se cumple sin
matices: cada lectura aterriza y se
grafica en su propio instante. La detección es otra cosa. El motor mantiene una única frontera para
toda la instancia y la avanza con la hora de cada mensaje, así que **un dispositivo que estuvo un
rato sin conexión y luego sube toda su serie de golpe puede hacer que sus lecturas más antiguas
lleguen por detrás de esa frontera** — y los dos tipos de regla con ventana, los agregados de
ventana fija y las reglas de sesión/hueco, descartan una lectura cuya ventana ya se cerró. Sin
contador, sin registro, sin alarma.

Las reglas de umbral, duración, repetición, ventana de conteo y tasa sí evalúan esas lecturas.

La tolerancia es [`watermarkLatenessSeconds`](../deployment/detection-engine.md) (5 segundos por
defecto), y subirla ayuda solo hasta cierto punto: la frontera es compartida, así que los
dispositivos activos la siguen empujando hacia adelante por mucho tiempo que el silencioso haya
estado fuera.

Si usa reglas con ventana sobre una flota que acumula, la forma fiable es **subir en lotes que
abarquen menos que la tolerancia de retraso**, o mantener las reglas con ventana fuera de las
métricas que reportan esos dispositivos. El almacenamiento, las gráficas y las [consultas de
eventos](../reference/graphql-api.md) no se ven afectados en ningún caso — las lecturas están todas.
:::

Una marca de tiempo informada no puede adelantarse demasiado al propio reloj de la plataforma. La que
lo haga se almacena en ese tope —la tolerancia es amplia para la deriva normal de reloj, así que esto
solo afecta a un dispositivo cuyo reloj está realmente mal. Ajuste el reloj en lugar de confiar en el
tope: una lectura almacenada en el tope es una lectura almacenada a la hora equivocada.

**`Measurement`** — una o más lecturas con nombre:

```json
"payload": { "entries": [ { "measurements": { "temperature": "21.5", "humidity": "48" } } ] }
```

**`Location`** — dónde está el dispositivo:

```json
"payload": {
  "entries": [
    {
      "latitude":  "33.74900000",
      "longitude": "-84.38800000",
      "elevation": "320.5",
      "accuracy":  "4.2",
      "speed":     "0.0",
      "heading":   "271.5"
    }
  ]
}
```

`latitude` y `longitude` son **obligatorios**; el resto son opcionales — envíe lo que el receptor
realmente conoce en lugar de un valor de relleno. Las unidades son fijas para toda la plataforma y
**no** se configuran por dispositivo:

| Campo | Unidad | Rango |
| --- | --- | --- |
| `latitude` / `longitude` | grados decimales WGS84 (EPSG:4326) | ±90 / ±180 |
| `elevation` | metros sobre el **elipsoide** WGS84 — no sobre el nivel medio del mar | — |
| `accuracy` | precisión horizontal, metros | 0 o mayor |
| `speed` | metros por segundo | 0 o mayor |
| `heading` | grados en sentido horario desde el norte verdadero | de 0 hasta 360 sin incluirlo |

:::caution La elevación es sobre el elipsoide, no sobre el nivel del mar
Un receptor que reporta altura sobre el nivel medio del mar debe convertirla antes de enviarla. Las
dos difieren en decenas de metros en terreno real — suficiente para situar una máquina del lado
equivocado de una geocerca — y como ambos valores parecen igual de plausibles, equivocarse produce
una posición incorrecta con toda seguridad en lugar de un error visible.
:::

Un valor fuera de su rango se rechaza como dato inválido en la primera entrega en lugar de
reintentarse. Es deliberado: el error más común es enviar grados escalados por 10⁷ (la convención que
usan algunas pilas GPS y LwM2M), y `337490000` no es una latitud en ninguna escala.

**`Alert`** — algo que el dispositivo quiere que vea una persona o una regla:

```json
"payload": { "entries": [ { "type": "overheat", "level": 5, "message": "coolant over limit", "source": "ecu" } ] }
```

`type` es **obligatorio** — es el clasificador por el que enrutan las políticas de notificación, las
reglas y los filtros de la consola, así que una alerta sin tipo es un registro sobre el que nada
puede actuar. `level`, `message` y `source` son opcionales.

## MQTT

Un topic de MQTT se asigna directamente a un subject de NATS, de modo que una publicación en `{instanceId}/{tenant}/devices/{token}/events` es consumida por `event-sources` como el subject `{instanceId}.{tenant}.devices.{token}.events`. Un dispositivo está autorizado a publicar únicamente en el topic de eventos de **sí mismo** y ningún otro, y el `{token}` en el topic debe coincidir con el `device` en el cuerpo — un evento que dice provenir de un dispositivo distinto es rechazado. El primer segmento es el **id de instancia** (el `instance.id` que desplegó, p. ej. `devicechain`): namespacea el plano de dispositivos para que las instancias que comparten un broker nunca se crucen, y una credencial de dispositivo solo está autorizada para el árbol de subjects de su propia instancia.

El listener es **TLS** y la conexión está **autenticada por el broker**: conéctese por TLS con la CA de la instancia y presente la credencial del dispositivo como nombre de usuario MQTT **`{tenant}:{credentialId}`** y contraseña.

La conexión también debe declarar **qué dispositivo es**: fije el **client id** de MQTT en `{instanceId}:{tenant}:{deviceToken}`. El broker rechaza cualquier otro valor — incluido el valor aleatorio que su biblioteca cliente inventa cuando usted no lo fija.

Ese requisito no es burocracia. Un client id de MQTT es la clave con la que un broker archiva la sesión de un dispositivo, y el protocolo establece que una conexión que presenta un id ya en uso *se apropia de esa sesión*: el dispositivo que la tenía queda desconectado y el recién llegado hereda sus suscripciones. Derivar el id de la identidad que el broker ya autenticó es lo que impide que un dispositivo — del tenant propio o de cualquier otro — expulse a otro, y es lo que permite encontrar y eliminar el estado de sesión de un tenant si alguna vez se elimina ese tenant.

**Si un dispositivo necesita más de una conexión, dé a cada una un sufijo:** `{instanceId}:{tenant}:{deviceToken}:pub`, `…:sub`, y así sucesivamente. Todo lo que vaya después del tercer `:` queda a su elección. Dos conexiones que comparten un mismo client id son dos clientes peleando por una sola sesión — se desconectarán mutuamente en bucle —, de modo que un dispositivo que publica en una conexión y se suscribe a comandos en otra necesita un sufijo distinto para cada una.

:::tip Diagnóstico de un client id rechazado
Una conexión rechazada se **cierra, no se responde**: el broker corta el socket en lugar de devolver un código MQTT de «no autorizado», por lo que su cliente informa un reinicio de conexión o un EOF inesperado en vez de un fallo de autorización. Un dispositivo que se reconecta automáticamente entrará en bucle. Si un dispositivo que antes se conectaba deja de hacerlo, revise su client id antes que su credencial.
:::

Publique el cuerpo del evento en el topic de eventos de su dispositivo:

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","credentialType":"MQTT_BASIC","credentialId":"<credentialId>","credentialSecret":"<credentialSecret>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

La credencial autentica la conexión (broker) y el evento (pipeline). El host TLS, el origen de la CA y la exposición del puerto dependen de cómo se despliegue la instancia — consulte [Despliegue](../deployment/kubernetes-operator.md).

### Calidad de servicio

**Publique la telemetría con QoS 0 salvo que tenga una razón específica para no hacerlo.** Eso es lo que hacen los ejemplos anteriores — `mosquitto_pub` lo usa por defecto.

QoS ≥ 1 tiene un costo real de almacenamiento en el servidor: el broker mantiene una **segunda copia** de cada mensaje QoS ≥ 1 en su propio almacén interno, además de la copia en el stream que lo sirve. Ese almacén comparte el mismo disco que todo lo demás que ejecuta la instancia. La plataforma le impone un techo para que no pueda consumir todo el volumen, lo que significa que un backlog sostenido de QoS ≥ 1 descarta sus mensajes no entregados **más antiguos** en lugar de tumbar la instancia.

QoS 1 es totalmente compatible. Úselo deliberadamente si sus dispositivos están en enlaces donde perder una publicación en tránsito importa más que el almacenamiento, y dimensione el volumen de JetStream del despliegue en consecuencia.

**Si usa QoS 1, establezca `altId` en sus eventos.** QoS 1 es *al menos una vez*, por lo que un acuse de recibo perdido hace que el dispositivo retransmita — y por defecto eso almacena el evento dos veces, contando la medición dos veces. Proporcionar un `altId` estable generado por el dispositivo es lo que activa la deduplicación de un evento:

```json
{"altId":"sensor-001-4417","device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}
```

Un evento reenviado que porta un `altId` ya visto se detecta y se omite. Sin uno, se inserta de nuevo. Esto aplica a cualquier ruta de al menos una vez, no solo a MQTT QoS 1 — es lo único que hace que un reintento sea seguro.

**QoS 2 se rechaza por defecto.** No aporta nada aquí que `altId` no le dé de forma más económica, y cuesta más: el broker retiene cada publicación QoS 2 hasta que llega su PUBREL, por lo que un dispositivo que inicia el handshake y nunca lo termina acumula estado del lado del servidor que nada recupera. En lugar de dejar eso abierto, el broker rechaza directamente las publicaciones QoS 2.

Tenga en cuenta *cómo* rechaza, porque no es sutil: el broker cierra la **conexión** en lugar de rechazar el mensaje individual, por lo que un firmware que publica en QoS 2 dentro de un bucle se reconectará en bucle. Un Will con QoS 2 se rechaza antes, en el CONNECT. Si ve un dispositivo reconectándose sin motivo aparente, verifique con qué QoS publica primero.

Publique con QoS 0, o QoS 1 con `altId`. Un operador que realmente necesite QoS 2 puede desactivar el rechazo con la variable de despliegue `nats_mqtt_reject_qos2_publish`; el búfer que lo llena permanece limitado de todos modos, por lo que el disco de la instancia queda protegido en cualquier caso.

## HTTP

`event-sources` también acepta eventos por HTTP en el puerto **8081**. El id de instancia y el inquilino se toman de la ruta `/{instanceId}/{tenant}/events` (siguiendo la convención del topic MQTT); el dispositivo y su credencial viajan en el cuerpo. `POST` devuelve **202 Accepted** una vez que el evento está en cola — o **429 Too Many Requests** si el inquilino supera su límite de tasa de ingesta (un limitador por inquilino con un techo predeterminado de la plataforma protege el pipeline compartido; la ruta MQTT descarta los mensajes que exceden el límite en su lugar):

```bash
curl -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

## Recepción de comandos

Un dispositivo recibe comandos en **su propio** topic:

```
{instanceId}/{tenant}/device-commands/{deviceToken}
```

Un dispositivo está autorizado a suscribirse a ese topic y a ningún otro — no puede ver comandos
dirigidos a ningún otro dispositivo, y no necesita filtrarlos. Suscríbase con la
misma credencial usada para publicar eventos:

```bash
mosquitto_sub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001:sub' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/device-commands/sensor-001"
```

Cada mensaje es un sobre (envelope) JSON:

```json
{
  "token": "6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11",
  "deviceToken": "sensor-001",
  "name": "reboot",
  "payload": {"delaySeconds": 5}
}
```

- **`token`** identifica **el comando**, no el dispositivo. Es lo que se devuelve en una
  respuesta, y es el único campo que correlaciona ambos.
- **`name`** es la clave del comando. Si el perfil del dispositivo declara un vocabulario de
  comandos, este es uno de sus comandos publicados y `payload` ya se ha validado contra
  el esquema de parámetros de ese comando — consulte
  [Comandos y el contrato de capacidades](../concepts/commands.md#commands-and-the-capability-contract).

## Respuesta a un comando {#responding-to-a-command}

Reporte el resultado publicando en el topic de respuestas de comando **propio** del dispositivo:

```
{instanceId}/{tenant}/command-responses/{deviceToken}
```

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/command-responses/sensor-001" \
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` debe ser el `token` del sobre de entrega** — el token del comando,
  no el del dispositivo. Este es el error más común: enviar aquí el token del dispositivo
  no coincide con ningún comando y la respuesta se descarta.
- **`success`** mueve el comando a `SUCCESSFUL` o `FAILED`.
- **`payload`** / **`error`** son cadenas opcionales, que se muestran en el historial de
  comandos de la consola y se devuelven a través de la API.

Al igual que los topics de eventos y de comandos, este es **por dispositivo**, y un dispositivo
está autorizado a publicar únicamente en el suyo. Tanto el inquilino como el dispositivo que
responde se toman del topic en lugar del cuerpo, de modo que un dispositivo solo puede responder
por **sus propios** comandos: una respuesta que nombre un comando perteneciente a otro
dispositivo se rechaza, no se registra.

:::caution El topic cambió
Este topic era antes de alcance de inquilino (`{instanceId}/{tenant}/command-responses`, sin
segmento de dispositivo). Un dispositivo que publique en el topic antiguo ahora es rechazado por
el broker, y sus respuestas nunca llegan a la plataforma — los comandos que responde quedan
pendientes hasta que pasan a `TIMEOUT`. Actualice el topic dondequiera que sus dispositivos lo
construyan.
:::

:::info Responder es lo que completa el ciclo de vida
Un comando que nunca se responde permanece en `SENT` hasta que su TTL lo convierte en
`TIMEOUT`. Sin una respuesta, la plataforma solo sabe que el comando fue despachado — no que
el dispositivo actuó sobre él. Si sus dispositivos no responden, configure un `expiresAt` al
emitir comandos para que alcancen un estado terminal en su propio plazo y no en el
predeterminado de siete días de la plataforma.
:::

## Qué sucede a continuación

1. **event-sources** decodifica el mensaje sin procesar.
2. **device-management** autentica el dispositivo mediante su credencial y resuelve el evento: **cada** una de las relaciones rastreadas del dispositivo (sus asignaciones a un cliente/área/activo) se registra como un anclaje, de modo que la lectura sea consultable por cada dimensión. Un dispositivo **sin asignar** igualmente reporta — su evento simplemente no lleva anclajes en lugar de ser descartado (consulte [Gestión de asignaciones de dispositivos](./managing-assignments.md)).
3. **event-management** persiste el evento resuelto en una hypertable de TimescaleDB, y **device-state** actualiza la última lectura y conectividad del dispositivo.

Consulte [Arquitectura → El pipeline de eventos](../concepts/architecture.md#the-event-pipeline).
