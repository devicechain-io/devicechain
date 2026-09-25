---
sidebar_position: 2
title: Conexión de un dispositivo
---

# Conexión de un dispositivo

Los dispositivos envían eventos a DeviceChain por **MQTT** o por **HTTP**. MQTT lo sirve directamente el servidor MQTT integrado de NATS en el puerto 1883, sin broker independiente. Ambos transportes alimentan el mismo pipeline de decodificación → resolución → persistencia, así que el cuerpo JSON del evento es idéntico en los dos.

:::note Estado
La ingesta por MQTT y por HTTP está disponible. Los dispositivos restringidos pueden conectarse en su lugar sobre CoAP/UDP con DTLS mediante la [ingesta LwM2M](../concepts/lwm2m.md), y las flotas heredadas (brownfield) mediante [Sparkplug B](../concepts/sparkplug.md). Un transporte WebSocket y el flujo completo de aprovisionamiento/reclamación de autoservicio siguen planificados.
:::

Las conexiones se protegen en el broker. Los listeners de MQTT/NATS son TLS, y un auth-callout de NATS autentica cada conexión y la vincula a los subjects de ese único dispositivo, de modo que un dispositivo solo puede publicar sus propios eventos y leer sus propios comandos. La autenticación del dispositivo también se aplica por evento mediante credencial. El modo de autenticación de dispositivo predeterminado es `required`, así que se espera una credencial tanto en la conexión como en el evento. Consulta [Credenciales de dispositivo](./device-credentials.md). LwM2M y Sparkplug B autentican en el handshake del transporte en lugar de por evento.

## Tres identificadores {#three-identifiers}

Conectar un dispositivo por MQTT significa acertar con tres identificadores distintos. Cumplen funciones diferentes y no son intercambiables, y a todos se les llama token o id en algún lugar de la consola.

| Identificador | Qué es | Dónde va |
| --- | --- | --- |
| **Token del dispositivo** | La identidad del dispositivo en el registro, p. ej. `sensor-001`. Lo eliges tú. | El campo `device` del cuerpo del evento y el segmento `{token}` del topic. Ambos deben coincidir: un evento que dice venir de un dispositivo distinto al de su topic se rechaza. |
| **Id de credencial** | Lo que el dispositivo presenta para demostrar que es él mismo. La consola lo etiqueta **Token de acceso** en una credencial `ACCESS_TOKEN` y **Usuario** en `MQTT_BASIC`. | Autentica dos veces: en la conexión MQTT y de nuevo por evento en el pipeline. El nombre de usuario MQTT es `{tenant}:{credentialId}`. |
| **Client id de MQTT** | `{instanceId}:{tenant}:{deviceToken}`. Es una clave de sesión, no una etiqueta. | La conexión MQTT. No aparece en ninguna parte de la consola. |

:::warning El nombre de usuario MQTT no es el id de credencial por sí solo
Es **`{tenant}:{credentialId}`**. Copiar el valor «Usuario» de la consola directamente en tu cliente es el fallo de conexión más común.
:::

El broker rechaza cualquier client id que no sea `{instanceId}:{tenant}:{deviceToken}` o ese valor más un `:sufijo`. Eso incluye el id aleatorio que inventa tu biblioteca cliente cuando no lo fijas. El sufijo es la forma de que un dispositivo abra dos conexiones; consulta [MQTT](#mqtt).

Los tres errores se ven idénticos desde el dispositivo. Una conexión rechazada recibe una única respuesta genérica: el broker devuelve el código de retorno 5 del CONNACK de MQTT (*no autorizado*) y después cierra la conexión. El código es genérico a propósito: es el mismo tanto si lo incorrecto era el client id, el prefijo `{tenant}:` o la credencial, así que el rechazo nunca dice qué comprobación falló. Tu cliente informa de «no autorizado» o, si nunca lee el CONNACK, solo del reset o EOF inesperado que sigue. Un dispositivo que se reconecta automáticamente entrará en bucle.

Si un dispositivo no consigue conectar, revisa en este orden:

1. El client id. Es el único valor que la consola nunca te muestra, así que es el que has tenido que construir tú.
2. El prefijo `{tenant}:` del nombre de usuario.
3. La credencial en sí.

Más adelante aparece un cuarto identificador: el `token` del sobre de un **comando** identifica al *comando*, no al dispositivo. Devolver el token del dispositivo en una respuesta de comando no coincide con nada. La respuesta no cierra ningún comando y acaba en el flujo de mensajes descartados de la plataforma, donde un operador puede verla, mientras el comando sigue pendiente. Consulta [Respuesta a un comando](#responding-to-a-command).

## El cuerpo del evento

Todo evento entrante, sobre cualquier transporte, es un objeto JSON:

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
- `credentialType` / `credentialId` — la credencial que presenta el dispositivo. `MQTT_BASIC` además lleva `credentialSecret`. Omítelos solo cuando el modo de autenticación de dispositivo de la instancia esté configurado como `disabled` u `optional`. El valor predeterminado es `required`, así que se espera una credencial.
- `payload` — su forma depende de `eventType`, y todas las formas son `{ "entries": [ … ] }`. Consulta a continuación.

### Formas del payload

Todo payload envuelve su contenido en un arreglo `entries`, y la forma fija el tipo JSON de cada valor:

- Los valores de medición y todos los campos de `Location` son **cadenas JSON** (`"21.5"`, no `21.5`).
- El `level` de una alerta es un **entero JSON sin comillas**.

Ambas reglas se aplican. Un payload sin entradas, una entrada vacía o un valor del tipo JSON equivocado (un número sin comillas donde se espera una cadena, o un `level` de alerta entre comillas) se rechaza en lugar de aceptarse en silencio: HTTP responde `400` y una publicación MQTT va a la cola de mensajes fallidos.

Una entrada es una lectura, tomada en un instante. Una entrada puede llevar su propio `occurredTime`. Ese es el instante con el que la lectura se almacena, se grafica, se evalúa y se devuelve, de modo que un dispositivo que acumula lecturas mientras está sin conexión puede subir una serie acumulada (hasta el tope por mensaje descrito más abajo) y conservar el historial que realmente registró.

- Una entrada sin `occurredTime` toma la del sobre.
- Un sobre sin `occurredTime` se fecha en el momento en que la plataforma recibió el mensaje. Un mensaje que esperó en la plataforma durante una caída conserva la hora en que llegó, no la hora en que se procesó.
- `occurredTime` es RFC 3339 (`2026-08-09T12:00:00.125Z`) dondequiera que aparezca. Un valor que no lo sea se rechaza indicando la entrada culpable, nunca se sustituye en silencio.

Hay un valor RFC 3339 válido que se rechaza igualmente: **`0001-01-01T00:00:00Z`**, que la plataforma reserva para significar «no se informó ninguna hora». Un dispositivo que quiera indicar la época debe enviar `1970-01-01T00:00:00Z`. Como todo rechazo de marca de tiempo, es terminal y se lleva por delante **el mensaje entero**, incluidas todas las lecturas hermanas del mismo lote. Descártalo en el firmware en lugar de descubrirlo en una cola de mensajes fallidos.

### Cuánto puede llevar un mensaje {#how-much-one-message-may-carry}

Un mensaje en los transportes de esta página admite **como máximo 1000 lecturas**. El tope pertenece al evento JSON de dispositivo descrito arriba, en MQTT y HTTP. Los transportes a los que la introducción dirige las flotas restringidas y heredadas no lo comparten: [LwM2M](../concepts/lwm2m.md) acota un solo Notify en 256 muestras, y [Sparkplug B](../concepts/sparkplug.md) no aplica ningún tope por mensaje (consulta [lo que un operador debe saber](../deployment/edge-services.md#sparkplug-what-an-operator-must-know)).

Una lectura es un dato almacenado. En mediciones, es una *clave de métrica*, así que una entrada con doce métricas son doce lecturas. En ubicaciones y alertas, es una entrada. El tope cuenta claves en lugar de entradas porque una sola entrada puede llevar miles de métricas, y son las lecturas, no las entradas, las que se convierten en filas almacenadas, actualizaciones de estado y evaluaciones de reglas.

Ese abanico es la razón de ser del tope. El limitador de ingesta por inquilino mide *mensajes*, y cobra lo mismo por un mensaje de una lectura que por uno de cuarenta mil. Sin el tope, un solo mensaje sería un coste ilimitado que comparte toda la instancia. Un dispositivo con un backlog más profundo lo sube en varios mensajes.

Por encima del tope, el mensaje se **rechaza entero**, nunca se recorta para que quepa. Un lote recortado en silencio se respondería con `202`, y las lecturas ausentes serían indetectables desde ambos extremos. No se almacena nada y no se pierde nada: el mensaje se enruta íntegro al flujo de decodificación fallida.

Cómo se entera el dispositivo depende del transporte:

- **HTTP** responde `400`, indicando el número de lecturas y el tope.
- **MQTT** no le dice nada al dispositivo. El broker confirma una publicación cuando la captura de forma duradera, lo que ocurre antes de decodificar el mensaje. Por tanto, un `PUBACK` no promete que el mensaje se aceptara, y un rechazo posterior solo es visible para el operador.

Los operadores ven todos los rechazos en el contador `total_msg_too_many_readings`. El tope es un ajuste de operador (`maxReadingsPerMessage`) para una instancia cuya flota necesite realmente otro valor. Bajarlo no reescribe el historial, pero sí se aplica a lo que siga en cola: los mensajes ya capturados y aún sin decodificar se rechazan con el valor nuevo.

:::caution Un lote muy acumulado se almacena entero, pero la detección puede no verlo todo
El almacenamiento guarda cada lectura en su propio instante, sin matices. La detección es otra cosa: un dispositivo que estuvo sin conexión y luego sube toda su serie de golpe puede ver cómo las reglas con ventana de tiempo descartan sus lecturas más antiguas, sin registro ni alarma. Consulta [Subidas acumuladas y reglas con ventana](#buffered-uploads-and-windowed-rules).
:::

#### Subidas acumuladas y reglas con ventana {#buffered-uploads-and-windowed-rules}

El motor de detección mantiene una única frontera para toda la instancia y la avanza con la hora propia de cada mensaje. Un dispositivo que estuvo un rato sin conexión y luego sube toda su serie de golpe puede hacer que sus lecturas más antiguas lleguen por detrás de esa frontera. Las reglas con ventana de tiempo descartan una lectura cuya ventana ya ha quedado atrás de la frontera: los agregados de ventana fija, las reglas de sesión/hueco y los tipos deslizantes (repetición, agregados deslizantes y correlación). Ningún registro ni ninguna alarma deja constancia del descarte.

Los tipos deslizantes cuentan lo que descartan en la métrica `detect_late_samples_total`. Los agregados de ventana fija y las reglas de sesión/hueco descartan en silencio y no aparecen en ella.

Las reglas de umbral, duración, ventana de conteo y tasa sí evalúan esas lecturas.

La tolerancia es [`watermarkLatenessSeconds`](../deployment/detection-engine.md) (5 segundos por defecto). Subirla ayuda solo hasta cierto punto: la frontera es compartida, así que los dispositivos activos la siguen empujando hacia adelante por mucho tiempo que el silencioso haya estado fuera.

Si usas reglas con ventana sobre una flota que acumula lecturas, **sube en lotes que abarquen menos que la tolerancia de retraso**, o mantén las reglas con ventana fuera de las métricas que reportan esos dispositivos. El almacenamiento, las gráficas y las [consultas de eventos](../reference/graphql-api.md) no se ven afectados en ningún caso; las lecturas están todas.

### Relojes adelantados {#clocks-that-run-ahead}

Una marca de tiempo informada no puede adelantarse demasiado al propio reloj de la plataforma. La que lo haga se almacena en el tope. La tolerancia es amplia para la deriva normal de reloj, así que esto solo afecta a un dispositivo cuyo reloj está realmente mal. Ajusta el reloj en lugar de confiar en el tope: una lectura almacenada en el tope es una lectura almacenada a la hora equivocada.

### Measurement {#measurement}

**`Measurement`** — una o más lecturas con nombre:

```json
"payload": { "entries": [ { "measurements": { "temperature": "21.5", "humidity": "48" } } ] }
```

### Location {#location}

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

`latitude` y `longitude` son obligatorios; el resto son opcionales. Envía lo que el receptor realmente conoce en lugar de un valor de relleno. Las unidades son fijas para toda la plataforma y no se configuran por dispositivo:

| Campo | Unidad | Rango |
| --- | --- | --- |
| `latitude` / `longitude` | grados decimales WGS84 (EPSG:4326) | ±90 / ±180 |
| `elevation` | metros sobre el **elipsoide** WGS84 — no sobre el nivel medio del mar | — |
| `accuracy` | precisión horizontal, metros | 0 o mayor |
| `speed` | metros por segundo | 0 o mayor |
| `heading` | grados en sentido horario desde el norte verdadero | de 0 hasta 360 sin incluirlo |

:::caution La elevación es sobre el elipsoide, no sobre el nivel del mar
Un receptor que reporta altura sobre el nivel medio del mar debe convertirla antes de enviarla. Las dos difieren en decenas de metros en terreno real, suficiente para situar una máquina al lado equivocado de una geocerca. Ambos valores parecen igual de plausibles, así que equivocarse produce una posición incorrecta que parece fiable en lugar de un error visible.
:::

Un valor fuera de su rango se rechaza como dato inválido en la primera entrega en lugar de reintentarse. El error más común es enviar grados escalados por 10⁷ (la convención que usan algunas pilas GPS y LwM2M), y `337490000` no es una latitud en ninguna escala.

### Alert {#alert}

**`Alert`** — algo que el dispositivo quiere que vea una persona o una regla:

```json
"payload": { "entries": [ { "type": "overheat", "level": 5, "message": "coolant over limit", "source": "ecu" } ] }
```

`type` es obligatorio. Es el clasificador por el que enrutan las políticas de notificación, las reglas y los filtros de la consola, así que una alerta sin tipo es un registro sobre el que nada puede actuar. `level`, `message` y `source` son opcionales.

## MQTT

Un topic de MQTT se asigna directamente a un subject de NATS. Una publicación en `{instanceId}/{tenant}/devices/{token}/events` la consume `event-sources` como el subject `{instanceId}.{tenant}.devices.{token}.events`.

- Un dispositivo está autorizado a publicar en **su propio** topic de eventos y en ningún otro.
- El `{token}` del topic debe coincidir con el `device` del cuerpo. Un evento que dice provenir de un dispositivo distinto se rechaza.
- El primer segmento es el **id de instancia** (el `instance.id` que desplegaste, p. ej. `devicechain`). Aísla el plano de dispositivos en su propio espacio de nombres para que las instancias que comparten un broker nunca se crucen, y una credencial de dispositivo solo está autorizada para el árbol de subjects de su propia instancia.

### Ajustes de conexión {#connection-settings}

El listener es TLS y la conexión está autenticada por el broker. Conéctate por TLS con la CA de la instancia y presenta la credencial del dispositivo como nombre de usuario MQTT **`{tenant}:{credentialId}`** y contraseña.

Fija el **client id** de MQTT en `{instanceId}:{tenant}:{deviceToken}` para que la conexión declare qué dispositivo es. El broker rechaza cualquier otra cosa, incluido el valor aleatorio que inventa tu biblioteca cliente cuando no lo fijas. La única excepción es un `:sufijo` tras el token del dispositivo, descrito más abajo.

El client id no es un mero trámite. Un client id de MQTT es la clave con la que un broker archiva la sesión de un dispositivo, y el protocolo establece que una conexión que presenta un id ya en uso *se apropia de esa sesión*: el dispositivo que la tenía queda desconectado y la nueva conexión hereda sus suscripciones. Derivar el id de la identidad que el broker ya autenticó impide que un dispositivo, de tu inquilino o de cualquier otro, expulse a otro. También es lo que permite encontrar y eliminar el estado de sesión de un inquilino si alguna vez se elimina ese inquilino.

Si un dispositivo necesita más de una conexión, da a cada una un sufijo: `{instanceId}:{tenant}:{deviceToken}:pub`, `…:sub`, y así sucesivamente. Todo lo que vaya después del tercer `:` queda a tu elección. Dos conexiones que comparten un mismo client id son dos clientes peleando por una sola sesión, y se desconectarán mutuamente en bucle. Un dispositivo que publica en una conexión y se suscribe a comandos en otra necesita un sufijo distinto para cada una.

:::tip Diagnóstico de un client id rechazado
Un client id incorrecto, un prefijo `{tenant}:` ausente y una credencial incorrecta producen el mismo código de retorno 5 del CONNACK, así que si un dispositivo que antes se conectaba deja de hacerlo, revisa su client id antes que su credencial (consulta [Tres identificadores](#three-identifiers)). Un dispositivo que insiste con una contraseña MQTT incorrecta además se ralentiza: tras 10 fallos seguidos sus conexiones se rechazan, incluso con la contraseña correcta, durante hasta 30 segundos cada vez (consulta [Las conexiones fallidas repetidas se ralentizan](./device-credentials.md#connect-backoff)). Cuando hayas corregido su contraseña, espera medio minuto antes de concluir que la corrección no ha funcionado.
:::

### Publicación de un evento {#publishing-an-event}

Publica el cuerpo del evento en el topic de eventos de tu dispositivo:

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","credentialType":"MQTT_BASIC","credentialId":"<credentialId>","credentialSecret":"<credentialSecret>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

La credencial autentica la conexión (broker) y el evento (pipeline). El host TLS, el origen de la CA y la exposición del puerto dependen de cómo se despliegue la instancia; consulta [Despliegue](../deployment/kubernetes-operator.md).

### Calidad de servicio

Publica la telemetría con **QoS 0** salvo que tengas una razón concreta para no hacerlo. Los ejemplos anteriores lo hacen, porque `mosquitto_pub` lo usa por defecto.

QoS ≥ 1 tiene un coste real de almacenamiento en el servidor. El broker mantiene una segunda copia de cada mensaje QoS ≥ 1 en su propio almacén interno, además de la copia en el stream que lo sirve, y ese almacén comparte el mismo disco que todo lo demás que ejecuta la instancia. La plataforma le impone un tope para que no pueda consumir todo el volumen, lo que significa que un backlog sostenido de QoS ≥ 1 descarta sus mensajes no entregados **más antiguos** en lugar de tumbar la instancia.

QoS 1 es totalmente compatible. Úsalo deliberadamente si tus dispositivos están en enlaces donde perder una publicación en tránsito importa más que el almacenamiento, y dimensiona el volumen de JetStream del despliegue en consecuencia.

Si usas QoS 1, **establece `altId` y `occurredTime` en tus eventos**. QoS 1 es *al menos una vez*, así que un acuse de recibo perdido hace que el dispositivo retransmita, y por defecto eso almacena el evento dos veces y cuenta la medición por duplicado. Un `altId` estable generado por el dispositivo activa la deduplicación de un evento. La coincidencia se busca por el `altId` y el `occurredTime` del sobre juntos, así que envía ambos en el sobre. Un `occurredTime` en una entrada no cuenta para la coincidencia:

```json
{"altId":"sensor-001-4417","occurredTime":"2026-08-09T12:00:00.125Z","device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}
```

Un evento reenviado que lleva un `altId` y un `occurredTime` ya vistos se detecta y se omite. Sin `altId`, se inserta de nuevo. Un sobre con `altId` pero sin `occurredTime` propio se fecha al llegar, así que una copia que el dispositivo vuelve a enviar recibe una hora distinta, no coincide con la primera y también se almacena de nuevo. Esto se aplica a cualquier ruta de al menos una vez, no solo a MQTT QoS 1; es lo único que hace que un reintento sea seguro.

**QoS 2 se rechaza por defecto.** No aporta nada aquí que `altId` no te dé de forma más económica, y cuesta más: el broker retiene cada publicación QoS 2 hasta que llega su PUBREL, así que un dispositivo que inicia el handshake y nunca lo termina acumula estado en el servidor que nada recupera. En lugar de dejar eso abierto, el broker rechaza directamente las publicaciones QoS 2.

El rechazo no es sutil. El broker cierra la **conexión** en lugar de rechazar el mensaje individual, así que un firmware que publica con QoS 2 en bucle se reconectará en bucle. Un Will con QoS 2 se rechaza antes, en el CONNECT. Si un dispositivo se reconecta sin motivo aparente, revisa primero con qué QoS publica.

Publica con QoS 0, o con QoS 1 con `altId` y `occurredTime`. Un operador que realmente necesite QoS 2 puede desactivar el rechazo con la variable de despliegue `nats_mqtt_reject_qos2_publish`. El búfer que llena sigue limitado en cualquier caso, así que el disco de la instancia queda protegido igualmente.

## HTTP

`event-sources` también acepta eventos por HTTP en el puerto **8081**. El id de instancia y el inquilino se toman de la ruta `/{instanceId}/{tenant}/events`, siguiendo la convención del topic MQTT; el dispositivo y su credencial viajan en el cuerpo.

- `POST` devuelve **202 Accepted** una vez que el evento está en cola.
- Devuelve **429 Too Many Requests** si el inquilino supera su límite de tasa de ingesta HTTP. La ruta MQTT descarta los mensajes que exceden el límite en lugar de responder.

La ingesta HTTP tiene una asignación por inquilino propia, separada de la que consume el tráfico MQTT del inquilino, así que las peticiones HTTP que nombran a un inquilino no pueden agotar su telemetría MQTT (consulta [nombres de inquilino que no se pueden confirmar](../concepts/governance.md#unconfirmed-tenants)).

```bash
curl -X POST http://localhost:8081/devicechain/acme/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001","eventType":"Measurement","credentialType":"ACCESS_TOKEN","credentialId":"<token>","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

:::warning Expón el puerto 8081 solo detrás de controles de red
La ingesta HTTP no tiene autenticación de transporte: la credencial del dispositivo va en el cuerpo de la petición y se comprueba después de admitirla. Por tanto, cualquiera que pueda llegar al puerto 8081 y conozca el nombre de un inquilino puede consumir la asignación HTTP de ese inquilino. El ingress del chart no enruta este puerto y, por defecto, cualquier pod del clúster puede llegar a él. Ponlo detrás de una NetworkPolicy, o de un ingress o gateway que autentique a quien llama, antes de depender de él.
:::

### Límites de tiempo en una petición

El listener de ingesta limita cuánto puede tardar una petición. Un dispositivo dispone de **5 segundos** para enviar las cabeceras de la petición y de **60 segundos** para enviar la petición completa, cabeceras y cuerpo. Ambos son configurables por instancia, en los ajustes `httpIngest` del área `event-sources`.

Una petición que supera cualquiera de los dos límites ve su **conexión cerrada**. El servidor la cierra antes de que el evento exista, así que no hay respuesta, ni evento, ni nada en el pipeline con lo que rastrearla. El síntoma parece una inestabilidad intermitente del lado del dispositivo que solo afecta a los dispositivos más lentos. En un enlace restringido (NB-IoT, 2G, satélite), donde tardar varios segundos en completar una petición es lo normal, eleva los límites en lugar de dejar que esos dispositivos fallen en silencio. La métrica `total_http_connections_closed_before_request` cuenta las conexiones que nunca llegaron a entregar una petición, que es lo que deja tras de sí un dispositivo así.

## Recepción de comandos

Un dispositivo recibe comandos en **su propio** topic:

```
{instanceId}/{tenant}/device-commands/{deviceToken}
```

Un dispositivo está autorizado a suscribirse a ese topic y a ningún otro. No puede ver comandos dirigidos a ningún otro dispositivo, y no necesita filtrarlos. Suscríbete con la misma credencial que usas para publicar eventos:

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
  "payload": {"delaySeconds": 5},
  "dispatchNonce": "0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94"
}
```

- **`token`** identifica el comando, no el dispositivo. Es lo que devuelves en una respuesta, y es el único campo que correlaciona ambos.
- **`name`** es la clave del comando. Si el perfil del dispositivo declara un vocabulario de comandos, este es uno de sus comandos publicados, y `payload` ya se ha validado contra el esquema de parámetros de ese comando. Consulta [Comandos y el contrato de capacidades](../concepts/commands.md#commands-and-the-capability-contract).
- **`dispatchNonce`** nombra esta entrega del comando. Es opaco, así que nada en el dispositivo debería leerlo ni interpretarlo, y debe devolverse en la respuesta. Consérvalo junto al comando hasta responder. Si el mismo comando vuelve a llegar, responde con el nonce de la entrega **más reciente** y no con el que guardaste primero.

## Respuesta a un comando {#responding-to-a-command}

Informa del resultado publicando en el topic de respuestas de comando **propio** del dispositivo:

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
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","dispatchNonce":"0f6f4a2c-9b71-4d0e-8a5b-3c2d1e0f7a94","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` debe ser el `token` del sobre de entrega**, el token del comando, no el del dispositivo. Enviar aquí el token del dispositivo es el error más común. No coincide con ningún comando, así que la respuesta no cierra nada: se vuelve a entregar hasta el tope de entregas del broker (cinco intentos) y después queda registrada en el flujo de mensajes descartados con el motivo `exhausted`, visible para un operador, mientras el comando sigue pendiente.
- **`dispatchNonce` debe ser el `dispatchNonce` del sobre de entrega que estás respondiendo.** Es obligatorio. Una respuesta que lo omita, o que cite el nonce de una entrega anterior del mismo comando, no cierra el comando. Consulta [Por qué el nonce es obligatorio](#why-the-nonce-is-required).
- **`success`** mueve el comando a `SUCCESSFUL` o `FAILED`.
- **`payload`** / **`error`** son cadenas opcionales, que se muestran en el historial de comandos de la consola y se devuelven a través de la API.

Al igual que los topics de eventos y de comandos, este es por dispositivo, y un dispositivo está autorizado a publicar únicamente en el suyo. Tanto el inquilino como el dispositivo que responde se toman del topic en lugar del cuerpo, de modo que un dispositivo solo puede responder por **sus propios** comandos. Una respuesta que nombre un comando perteneciente a otro dispositivo se rechaza, no se registra.

:::caution El topic cambió
Este topic era antes de alcance de inquilino (`{instanceId}/{tenant}/command-responses`, sin segmento de dispositivo). El broker ahora rechaza a un dispositivo que publique en el topic antiguo, y sus respuestas nunca llegan a la plataforma, así que los comandos que responde quedan pendientes hasta que pasan a `TIMEOUT`. Actualiza el topic dondequiera que tus dispositivos lo construyan.
:::

:::info Responder es lo que completa el ciclo de vida
Un comando que nunca se responde permanece en `SENT` hasta que su TTL lo convierte en `TIMEOUT`. Sin una respuesta, la plataforma solo sabe que el comando se despachó, no que el dispositivo actuó sobre él. Si tus dispositivos no responden, configura un `expiresAt` al emitir comandos para que alcancen un estado terminal en tu propio plazo y no en el predeterminado de siete días de la plataforma.
:::

### Por qué el nonce es obligatorio {#why-the-nonce-is-required}

Devolver `dispatchNonce` le indica a la plataforma que tu respuesta corresponde a **esta** entrega del comando. Importa porque un comando puede publicarse legítimamente más de una vez. Si una publicación informa de un error, la plataforma no puede distinguir un mensaje que se perdió de uno cuyo acuse de recibo se perdió, así que vuelve a encolar el comando para enviarlo de nuevo. Sin el nonce, una respuesta a la primera entrega que llegue después de enviarse la segunda cerraría el comando antes de que el dispositivo hubiera ejecutado esa segunda entrega.

Para un dispositivo que construyes tú, esto tiene dos consecuencias:

- Una respuesta **sin** `dispatchNonce` se rechaza. El comando no se cierra, y la respuesta queda registrada en el flujo de mensajes descartados de la plataforma en lugar de perderse, de modo que el rechazo es visible y no silencioso.
- Una respuesta que cita un `dispatchNonce` del que el comando ya **se ha movido** se rechaza igual. Responde con el nonce de la entrega que realmente estás contestando.

Guarda el nonce junto al comando mientras lo ejecutas, y sobrescríbelo si el mismo comando se entrega de nuevo antes de que respondas.

## Qué sucede a continuación

1. **event-sources** decodifica el mensaje sin procesar.
2. **device-management** autentica el dispositivo mediante su credencial y resuelve el evento. Cada una de las relaciones rastreadas del dispositivo (sus asignaciones a un cliente/área/activo) se registra como un anclaje, de modo que la lectura se puede consultar por cada dimensión. Un dispositivo **sin asignar** sigue informando: su evento no lleva anclajes en lugar de descartarse (consulta [Gestión de asignaciones de dispositivos](./managing-assignments.md)).
3. **event-management** persiste el evento resuelto en una hypertable de TimescaleDB, y **device-state** actualiza la última lectura y la conectividad del dispositivo.

Consulta [Arquitectura → El pipeline de eventos](../concepts/architecture.md#the-event-pipeline).
