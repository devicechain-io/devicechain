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
| **Id de credencial** | Lo que el dispositivo presenta para demostrar que es él mismo. La consola lo etiqueta **Token de acceso** en una credencial `ACCESS_TOKEN` y **Usuario** en `MQTT_BASIC`. | Por MQTT autentica la conexión, y el cuerpo del evento no lleva ninguna. Por HTTP autentica cada evento, desde el cuerpo. El nombre de usuario MQTT es `{tenant}:{credentialId}`. |
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
- `credentialType` / `credentialId` — la credencial que presenta el dispositivo **por HTTP**. `MQTT_BASIC` además lleva `credentialSecret`. Omítelos solo cuando el modo de autenticación de dispositivo de la instancia esté configurado como `disabled` u `optional`. El valor predeterminado es `required`, así que se espera que un evento HTTP lleve una credencial. **Por MQTT, omite los tres**: el broker ya autenticó al dispositivo al conectarse, y un evento en el broker MQTT de la plataforma que lleve cualquiera de ellos se rechaza (consulta la [Referencia del protocolo de dispositivos](../reference/device-protocol.md#rejections-at-the-door)).
- `payload` — su forma depende de `eventType`, y todas las formas son `{ "entries": [ … ] }`. Consulta a continuación.

Cada campo, con su tipo y si es obligatorio, incluidos los opcionales `altId` y `relationship`, está en la [Referencia del protocolo de dispositivo](../reference/device-protocol.md#device-event), junto con esquemas JSON con los que puedes validar lo que emite el firmware.

### Formas del payload {#payload-shapes}

Todo payload envuelve su contenido en un arreglo `entries`, y la forma fija el tipo JSON de cada valor:

- Los valores de medición y todos los campos de `Location` son **cadenas JSON** (`"21.5"`, no `21.5`).
- El `level` de una alerta es un **entero JSON sin comillas** de 0 a 2147483647.

Ambas reglas se aplican. Un payload sin entradas, una entrada vacía, un valor del tipo JSON equivocado (un número sin comillas donde se espera una cadena, o un `level` de alerta entre comillas) o un `level` de alerta fuera de ese rango se rechaza en lugar de aceptarse en silencio: HTTP responde `400` y una publicación MQTT va a la cola de mensajes fallidos.

Una entrada es una lectura, tomada en un instante. Una entrada puede llevar su propio `occurredTime`. Ese es el instante con el que la lectura se almacena, se grafica, se evalúa y se devuelve, de modo que un dispositivo que acumula lecturas mientras está sin conexión puede subir una serie acumulada (hasta el límite descrito más abajo) y conservar el historial que realmente registró.

- Una entrada sin `occurredTime` toma la del sobre.
- Un sobre sin `occurredTime` se fecha en el momento en que la plataforma recibió el mensaje. Un mensaje que esperó en la plataforma durante una caída conserva la hora en que llegó, no la hora en que se procesó.
- `occurredTime` es RFC 3339 (`2026-08-09T12:00:00.125Z`) dondequiera que aparezca. Un valor que no lo sea se rechaza indicando la entrada culpable, nunca se sustituye en silencio.
- Un `occurredTime` anterior en más de **366 días** al momento en que la plataforma recibió el mensaje se rechaza, tanto en el sobre como en cualquier entrada. Una subida acumulada debe llegar a la plataforma dentro de ese plazo; una lectura más antigua no se almacena en ningún sitio.

Hay un valor RFC 3339 válido que se rechaza igualmente: **`0001-01-01T00:00:00Z`**, que la plataforma reserva para significar «no se informó ninguna hora». Un dispositivo sin reloj debe omitir `occurredTime`, no enviar un valor de relleno: la época, `1970-01-01T00:00:00Z`, también se rechaza, por tener más de 366 días. Como todo rechazo de marca de tiempo, es terminal y se lleva por delante **el mensaje entero**, incluidas todas las lecturas hermanas del mismo lote. Descártalo en el firmware en lugar de descubrirlo en una cola de mensajes fallidos.

### Cuánto puede llevar un mensaje {#how-much-one-message-may-carry}

Un evento lleva **como máximo 256 lecturas**, en todos los transportes. El límite es fijo y no es un ajuste. En los transportes de esta página (el evento JSON de dispositivo en MQTT y HTTP) un mensaje es un evento, así que un mensaje por encima del límite se rechaza. Las pasarelas de protocolo lo dividen en su lugar: un Notify de [LwM2M](../concepts/lwm2m.md) o un mensaje de [Sparkplug B](../concepts/sparkplug.md) con más lecturas se convierte en varios eventos consecutivos de como máximo 256 (consulta [lo que un operador debe saber](../deployment/edge-services.md#sparkplug-what-an-operator-must-know)).

Una lectura es un dato almacenado. En mediciones, es una *clave de métrica*, así que una entrada con doce métricas son doce lecturas. En ubicaciones y alertas, es una entrada. El límite cuenta claves en lugar de entradas porque una sola entrada puede llevar miles de métricas, y son las lecturas, no las entradas, las que se convierten en filas almacenadas, actualizaciones de estado y evaluaciones de reglas.

Ese abanico es la razón de ser del límite. El techo de ingesta por inquilino cuenta lecturas a lo largo del tiempo, así que limita cuántas envía un inquilino por segundo, y un mensaje no puede costar más que la ráfaga del nivel. Pero la ráfaga es un ajuste del nivel: un nivel cuya ráfaga se eleve a cuarenta mil podría enviar un mensaje de cuarenta mil. Sin el límite, lo que un mensaje le cuesta a toda la instancia lo fijaría la mayor ráfaga que permita cualquier nivel. Un dispositivo con un backlog más profundo lo sube en varios mensajes.

Por encima del límite, el mensaje se **rechaza entero**, nunca se recorta para que quepa. Un lote recortado en silencio se respondería con `202`, y las lecturas ausentes serían indetectables desde ambos extremos. No se almacena nada. Para el diagnóstico, el mensaje se enruta al flujo de decodificación fallida sin la credencial que llevara y con su cuerpo recortado a los primeros 16 KiB.

Cómo se entera el dispositivo depende del transporte:

- **HTTP** responde `400`, indicando el número de lecturas y el límite.
- **MQTT** no le dice nada al dispositivo. El broker confirma una publicación cuando la captura de forma duradera, lo que ocurre antes de decodificar el mensaje. Por tanto, un `PUBACK` no promete que el mensaje se aceptara, y un rechazo posterior solo es visible para el operador.

Los operadores ven todos los rechazos en el contador `total_msg_too_many_readings`. El límite no es configurable. Una instancia actualizada desde una versión que admitía más rechaza con el límite nuevo los mensajes ya capturados y aún sin decodificar.

:::caution Un lote muy acumulado se almacena entero, pero la detección puede no verlo todo
El almacenamiento guarda cada lectura en su propio instante, siempre que el mensaje llegue dentro de los [366 días](#payload-shapes) siguientes a su lectura más antigua; un mensaje con una lectura anterior se rechaza entero. La detección es otra cosa: un dispositivo que estuvo sin conexión y luego sube toda su serie de golpe puede ver cómo las reglas con ventana de tiempo o con tiempo de sostenimiento descartan sus lecturas más antiguas, sin registro ni alarma. Consulta [Subidas acumuladas y reglas con ventana](#buffered-uploads-and-windowed-rules).
:::

#### Subidas acumuladas y reglas con ventana {#buffered-uploads-and-windowed-rules}

El motor de detección mantiene una única frontera para toda la instancia y la avanza con la hora propia de cada mensaje. Un dispositivo que estuvo un rato sin conexión y luego sube toda su serie de golpe puede hacer que sus lecturas más antiguas lleguen por detrás de esa frontera. Las reglas con ventana de tiempo descartan una lectura cuya ventana ya ha quedado atrás de la frontera: los agregados de ventana fija, las reglas de sesión/hueco y los tipos deslizantes (repetición, agregados deslizantes y correlación). Las reglas de duración descartan una lectura que cumple su condición y queda más atrás de la frontera que su tiempo de sostenimiento, y colocan el resto según su propia hora. Ningún registro ni ninguna alarma deja constancia del descarte.

Los tipos deslizantes y las reglas de duración cuentan lo que descartan en la métrica `detect_late_samples_total`. Los agregados de ventana fija y las reglas de sesión/hueco descartan en silencio y no aparecen en ella.

Las reglas de umbral, ventana de conteo y tasa sí evalúan esas lecturas.

La tolerancia es [`watermarkLatenessSeconds`](../deployment/detection-engine.md) (5 segundos por defecto). Subirla ayuda solo hasta cierto punto: la frontera es compartida, así que los dispositivos activos la siguen empujando hacia adelante por mucho tiempo que el silencioso haya estado fuera.

Si usas reglas con ventana sobre una flota que acumula lecturas, **sube en lotes que abarquen menos que la tolerancia de retraso**, o mantén las reglas con ventana fuera de las métricas que reportan esos dispositivos. El almacenamiento, las gráficas y las [consultas de eventos](../reference/graphql-api.md) no se ven afectados en ningún caso; las lecturas están todas.

### Relojes adelantados {#clocks-that-run-ahead}

Una marca de tiempo informada no puede adelantarse demasiado al propio reloj de la plataforma. La que lo haga se almacena en el tope. La tolerancia es amplia para la deriva normal de reloj, así que esto solo afecta a un dispositivo cuyo reloj está realmente mal. Ajusta el reloj en lugar de confiar en el tope: una lectura almacenada en el tope es una lectura almacenada a la hora equivocada. Un reloj muy atrasado se trata de otra forma: una lectura con más de 366 días se rechaza, no se mueve (consulta [Formas del payload](#payload-shapes)).

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

:::tip Deja que la consola lo escriba
La pestaña **Enviar datos** de la página de un dispositivo en la consola construye este comando, y el de HTTP más abajo, para ese dispositivo: su token, su credencial y lecturas de ejemplo para las métricas que declara su perfil publicado. Consulta [Enviar datos](../user-guide/devices.md#send-data).
:::

Publica el cuerpo del evento en el topic de eventos de tu dispositivo:

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/devices/sensor-001/events" \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

La credencial autentica la conexión. El broker ya sabe de qué dispositivo viene cada publicación, así que el evento no lleva credencial; uno que la lleve se rechaza. El host TLS, el origen de la CA y la exposición del puerto dependen de cómo se despliegue la instancia; consulta [Despliegue](../deployment/kubernetes-operator.md).

El broker habla TLS, así que `--cafile` necesita el certificado de la CA de la instancia. En una instalación kind local está en el Secret `dc-nats-tls` del namespace de la instancia, bajo la clave `ca.crt`. Guárdalo como `instance-ca.crt` con un comando (sustituye el id de tu instancia tras el prefijo `dci-`):

```bash
kubectl -n dci-devicechain get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > instance-ca.crt
```

En un clúster kind local el broker está en `localhost:1883`, pero su certificado no se emite para `localhost` ni `host.docker.internal`, así que la verificación falla por el nombre de host. Solo para pruebas locales, añade `--insecure` a `mosquitto_pub` para omitir la comprobación del nombre de host:

```bash
mosquitto_pub --cafile instance-ca.crt --insecure -h localhost -p 1883 ...
```

:::warning Solo para pruebas locales
`--insecure` desactiva la verificación del nombre de host, así que la conexión ya no demuestra que llegó a tu broker. No lo uses nunca en producción; conéctate a un nombre de host que el certificado cubra.
:::

### Calidad de servicio {#quality-of-service}

Publica la telemetría con **QoS 0** salvo que tengas una razón concreta para no hacerlo. Los ejemplos anteriores lo hacen, porque `mosquitto_pub` lo usa por defecto.

QoS ≥ 1 tiene un coste real de almacenamiento en el servidor. El broker mantiene una segunda copia de cada mensaje QoS ≥ 1 en su propio almacén interno, además de la copia en el stream que lo sirve, y ese almacén comparte el mismo disco que todo lo demás que ejecuta la instancia. Lo que promete un PUBACK, y lo que hace un backlog, depende de un ajuste de la instancia, `infrastructure.nats.mqttFailClosed`:

- **Desactivado (por defecto).** La plataforma le impone un tope al almacén para que no pueda consumir todo el volumen. Un PUBACK significa que el broker aceptó el mensaje, no que la plataforma vaya a conservarlo: un backlog sostenido descarta sus mensajes no entregados **más antiguos** en lugar de tumbar la instancia, y al dispositivo no se le avisa.
- **Activado.** Un PUBACK significa que la plataforma guarda el mensaje. Esto cubre eventos, respuestas a comandos e informes de dispositivo. Cuando la plataforma se retrasa y el almacén se llena, **rechaza las publicaciones nuevas** en lugar de descartar las antiguas. El broker no envía PUBACK y cierra la conexión, así que el dispositivo conserva el mensaje y lo vuelve a enviar al reconectarse: se le hace esperar, y no se pierden datos. Mientras el almacén está lleno por encima del 80 %, se rechazan las conexiones nuevas de dispositivos hasta que baja del 60 %. El rechazo es el código de retorno 5 de CONNACK de MQTT (no autorizado), así que el firmware debe seguir reintentando tras el código 5 en lugar de rendirse. Un dispositivo cuya conexión se cerró así y que se reconecta en menos de 30 segundos no se da por desconectado. Una excepción: un mensaje que supera el límite de ingesta de su tenant se sigue confirmando y descartando, como con el ajuste desactivado.

QoS 0 no se ve afectado por el ajuste. El broker nunca almacena una publicación QoS 0, así que nada la frena, y sigue siendo de mejor esfuerzo en ambos casos.

QoS 1 es totalmente compatible. Úsalo deliberadamente si tus dispositivos están en enlaces donde perder una publicación en tránsito importa más que el almacenamiento, y dimensiona el volumen de JetStream del despliegue en consecuencia. Con el ajuste activado, el almacén toma el techo de los streams calientes (`streamMaxBytes`), que se reserva en el volumen como el de cualquier otro stream.

El ajuste solo se acepta en un broker que sirve a esta única instancia, porque el broker mantiene un solo almacén para todas las instancias que sirve; una instancia en un broker compartido se niega a arrancar con él activado. La garantía se da para un broker replicado (tres réplicas). Con una sola réplica, la caída de un nodo puede perder mensajes que el broker había confirmado y aún no había escrito en disco. Una publicación hecha mientras se activa o desactiva el ajuste, o mientras se mueve el líder del almacén, puede llegar dos veces, nunca ninguna, lo que es un motivo más para establecer `altId`. El broker propio del agente de borde no está cubierto.

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
- Devuelve **429 Too Many Requests** si el inquilino supera su techo de ingesta HTTP, contado en lecturas, así que un mensaje que lleva muchas lecturas cuesta más que uno que lleva una sola. La ruta MQTT descarta los mensajes que exceden el límite en lugar de responder.
- Devuelve **400 Bad Request** cuando el cuerpo no puede leerse o decodificarse, o el inquilino de la
  ruta no es un token válido. Enviar de nuevo la misma petición recibe la misma respuesta.
- Devuelve **503 Service Unavailable** cuando el evento no se aceptó. Con una cabecera
  `Retry-After`, la plataforma está rechazando eventos nuevos
  ([contrapresión](../deployment/observability.md#ingest-backpressure)) y el evento no se
  almacenó: espera esos segundos y vuelve a enviarlo. Sin ella, la publicación falló y puede que el
  evento se haya almacenado igualmente, así que un reenvío lo almacena dos veces salvo que lleve
  `altId` y `occurredTime` (consulta [Calidad de servicio](#quality-of-service)). Reinténtalo en
  ambos casos. El techo propio del inquilino se comprueba primero, así que un inquilino por encima
  recibe `429`, no este `503`. La [matriz de transportes](../reference/transport-matrix.md#http)
  tiene la lista completa.

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

## Recepción de comandos {#receiving-commands}

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
- **`payload`** es opcional y puede ser cualquier valor JSON (una cadena se almacena como su texto; un objeto o un arreglo, como ese JSON); **`error`** es una cadena opcional. Ambos se muestran en el historial de comandos de la consola y se devuelven a través de la API.

Todas las formas en que una respuesta puede no liquidar su comando están en la [Referencia del protocolo de dispositivo](../reference/device-protocol.md#rejections-command-responses).

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

## Topics de entrega de configuración {#reserved-device-topics}

Un dispositivo recibe su configuración, y le dice a la plataforma qué hizo con ella, mediante dos topics por dispositivo:

```
{instanceId}/{tenant}/device-desired/{deviceToken}    (de la plataforma al dispositivo; un dispositivo puede suscribirse al suyo)
{instanceId}/{tenant}/device-reports/{deviceToken}    (del dispositivo a la plataforma; un dispositivo puede publicar en el suyo)
```

Igual que con los comandos, un dispositivo solo está autorizado para el topic que lleva su propio token de dispositivo, y únicamente en el sentido indicado. La plataforma toma el tenant y el dispositivo del topic, nunca del mensaje, así que un mensaje no debe llevar la identidad de un dispositivo (si la lleva, se rechaza) y un dispositivo solo puede informar por sí mismo.

La configuración se construye a partir de los atributos `SHARED` del dispositivo, pero solo con las claves que su perfil declara como configuración visible para el dispositivo: decláralas en el editor de perfiles de la consola (o con `setDeviceProfileConfigurationDeclaration`), y llegan a los dispositivos cuando se publica el perfil. Una clave compartida que la declaración no nombra nunca se envía, y una clave declarada cuyo valor almacenado no encaja con su tipo declarado se omite. Una escritura de atributo compartido que haría la configuración de un dispositivo mayor de 64 KiB se rechaza. La página del dispositivo en la consola muestra la última revisión, lo último que informó el dispositivo y si la revisión sigue pendiente; la consulta `deviceConfiguration` devuelve lo mismo.

Cada vez que cambia la configuración del dispositivo, la plataforma publica una revisión en `device-desired`:

```json
{"v":1,"kind":"config.revision","revision":3,"digest":"sha256:9e39b20b9def308341baa751db8c2570bfc5b562e1d5420f9e6506510e0328da","document":"{\"sampleIntervalSeconds\":30}"}
```

`document` es una **cadena** JSON que contiene la configuración. Decodifica el valor de la cadena JSON, calcula SHA-256 sobre sus bytes UTF-8 y compara `sha256:` seguido del hash en hexadecimal en minúsculas con `digest`; si difieren, no apliques nada. No analices el documento ni lo vuelvas a serializar antes de calcular el hash, porque el digest cubre los bytes que envió la plataforma.

Las revisiones pueden llegar a un dispositivo fuera de orden, así que una revisión menor puede llegar después de una mayor. Ignora un `config.revision` cuya revisión sea menor que la que tienes y, si es la que ya tienes, informa `APPLIED` de nuevo en lugar de aplicarla dos veces.

El dispositivo responde en `device-reports`. Cada mensaje es JSON, de como máximo 4 KiB, y se rechazan los campos desconocidos.

- **`sync`**, que se envía cada vez que el dispositivo se conecta, indica qué revisión tiene (`0` si ninguna): `{"v":1,"kind":"sync","config":{"haveRevision":3}}`. Suscríbete a `device-desired` **antes** de enviar el sync: no se retiene nada, así que una respuesta publicada antes de que te suscribas se pierde. La plataforma responde con la revisión actual, la tenga ya el dispositivo o no, y no responde nada cuando el dispositivo no tiene configuración. No cuentes con que llegue después un mensaje publicado mientras el dispositivo estaba desconectado: sincronizar es como un dispositivo se pone al día. Sincroniza en cada conexión y también periódicamente (cada pocas horas, por ejemplo), porque una revisión perdida entre el momento en que la plataforma la confirma y el momento en que la envía solo se repara con un sync. Los sync enviados a los pocos segundos del anterior se confirman y se ignoran.
- **`config.status`** informa de lo que el dispositivo hizo con una revisión: `{"v":1,"kind":"config.status","revision":3,"digest":"sha256:…","status":"APPLIED"}`. El estado es `RECEIVED`, `APPLIED` o `REJECTED`. Un informe `REJECTED` debe incluir `errors`, una lista de hasta 16 cadenas de hasta 256 bytes cada una que explique el motivo.

La plataforma registra un `config.status` solo cuando el dispositivo tiene esa revisión y el digest coincide con ella; el dispositivo deja de figurar como pendiente de esa revisión cuando ha informado `APPLIED` para ella. Los informes se procesan al menos una vez, así que repetir uno es seguro, y un informe antiguo nunca sobrescribe uno más reciente. Un mensaje que no se decodifica, que nombra una revisión que no se envió al dispositivo o que lleva un digest que no es el de esa revisión se rechaza y se registra en el flujo de mensajes fallidos (dead-letter) en lugar de descartarse. Los dispositivos que solo hablan HTTP no se atienden por este canal.

### Restaurar o copiar una configuración {#restore-copy-configuration}

La configuración de un dispositivo puede volver a una revisión anterior, o hacerse igual a la de otro dispositivo: por ejemplo, para deshacer un cambio erróneo o para dar a una unidad de reemplazo la configuración de la unidad que reemplazó. Ambas cosas se hacen desde la pestaña **Configuración** de la página del dispositivo, por un operador con `device:write`, o con las mutaciones `restoreDeviceConfiguration` y `copyDeviceConfiguration`.

- **Restaurar** toma una de las revisiones del propio dispositivo del historial de revisiones. **Copiar de otro dispositivo** toma la revisión más reciente de otro dispositivo del mismo inquilino.
- La consola muestra primero una vista previa: cada clave, su valor actual, el valor que tendrá y si se añade, se cambia o se elimina. Nada cambia hasta que eliges **Aplicar**.
- Al aplicar, los atributos `SHARED` del dispositivo para sus claves declaradas toman los valores de esa revisión. Una clave declarada que la revisión no contiene se elimina. Se crea una revisión **nueva** y se envía al dispositivo como de costumbre. Las revisiones anteriores nunca se modifican. Si el dispositivo ya tiene esa configuración, no se crea nada.
- Todo el cambio se rechaza, sin escribir nada, si el perfil del dispositivo no declara una clave que contiene la revisión (`CONFIGURATION_KEY_UNDECLARED`), si un valor no encaja en el tipo declarado ahora para su clave (`CONFIGURATION_VALUE_TYPE_MISMATCH`), o si el resultado supera el límite de tamaño (`CONFIGURATION_TOO_LARGE`). Por eso, restaurar una revisión antigua después de que haya cambiado la declaración del perfil puede rechazarse. Un dispositivo de otro inquilino, una revisión desconocida o un dispositivo de origen sin revisión dan `NOT_FOUND`.
- Si otro cambio en la configuración del dispositivo llega mientras se aplica la restauración o la copia, se rechaza con `CONFIGURATION_CONFLICT` y no se escribe nada; es seguro volver a intentarlo.
- Una copia aplica la revisión de origen que mostró la vista previa de la consola, aunque la configuración del dispositivo de origen cambie antes de elegir **Aplicar**. Con GraphQL, pasa esa revisión como `fromRevision`; sin ella, `copyDeviceConfiguration` copia la revisión más reciente del origen.

```graphql
mutation {
  restoreDeviceConfiguration(deviceToken: "pump-7", revision: 3) {
    desired { revision document }
  }
}
```

## Actualizaciones over-the-air {#over-the-air-updates}

En una instancia que ejecuta el servicio de actualizaciones (consulta [Actualizaciones over-the-air](../concepts/over-the-air-updates.md)), los mismos dos topics transportan actualizaciones de firmware y software. Las reglas anteriores también se aplican aquí: cada mensaje lleva `"v":1` y un `kind`, los campos desconocidos o repetidos se rechazan, y un mensaje no debe llevar una identidad de dispositivo. Los mensajes de actualización, en cualquier sentido, ocupan como máximo 4 KiB.

Cuando se asigna un artefacto al dispositivo, la plataforma publica en `device-desired`:

```json
{"v":1,"kind":"update.assignment","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","trust":"UNVERIFIED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,"requiresReboot":true}}
```

`component` nombra la parte del dispositivo que se actualiza; un dispositivo trabaja como máximo en una asignación por componente. `requiresReboot` es true para un artefacto de firmware y false para los de software y configuración. `trust` es `UNVERIFIED` o `SIGNED`. `UNVERIFIED` significa que la plataforma ha comprobado que los bytes son los que se declararon, no quién los produjo; no lleva campos de firma, y si los lleva se rechaza. `SIGNED` añade tres campos, todos obligatorios, como se muestra a continuación, y el dispositivo **debe** verificarlos antes de informar `VERIFIED`: la plataforma transmite la firma, el dispositivo decide si confía en ella.

```json
{"v":1,"kind":"update.assignment","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","trust":"SIGNED","artifact":{"version":"1.4.2","digest":"sha256:…","size":8388608,"requiresReboot":true},"signature":"MEQCIHGf…","keyId":"f3ef3f7c…","algorithm":"ECDSA_P256_SHA256"}
```

- `signature` es la firma separada, en base64 con el alfabeto estándar.
- `keyId` son 64 caracteres hexadecimales en minúscula: el SHA-256 de la clave pública del firmante en DER `SubjectPublicKeyInfo`. El dispositivo la busca entre las claves públicas en las que confía.
- `algorithm` es `ECDSA_P256_SHA256` (una firma ASN.1 DER, la forma que produce `cosign sign-blob`) o `ED25519`. Cualquier otro valor se rechaza.
- Ambos algoritmos firman el **resumen SHA-256 en bruto de 32 bytes** del artefacto, no su texto hexadecimal ni la cadena `sha256:`. Ed25519 firma esos 32 bytes directamente como mensaje. Calcula el resumen a partir de los bytes descargados, nunca a partir de `artifact.digest`, y comprueba antes que es igual a `artifact.digest`.

Para producir una firma a mano (`file` es el artefacto, `k.pem` la clave privada):

```bash
# keyId: SHA-256 de la clave pública en DER SubjectPublicKeyInfo
openssl pkey -in k.pem -pubout -outform DER | openssl dgst -sha256

# ECDSA_P256_SHA256 (clave P-256): firma SHA-256(file)
openssl dgst -sha256 -sign k.pem -binary file | base64 | tr -d '\n'
cosign sign-blob --key k.key file          # la misma firma, en base64

# ED25519: firma los 32 bytes del resumen
openssl dgst -sha256 -binary file > digest
openssl pkeyutl -sign -rawin -inkey k.pem -in digest | base64 | tr -d '\n'
```

Para Ed25519, `cosign` y `openssl pkeyutl -sign -rawin -in file` firman el **archivo**, no su resumen, así que un dispositivo rechazaría sus firmas: usa la forma con `digest` de arriba. ECDSA es el algoritmo con el que `cosign sign-blob` es compatible.

Si la firma no se verifica, falla la actualización con `FAILED` y `result.code` `SIGNATURE_INVALID`. Si `keyId` nombra una clave en la que el dispositivo no confía, usa `UNKNOWN_SIGNER`; usa el mismo código para una asignación `UNVERIFIED` cuando el dispositivo exige imágenes firmadas. Ambos se informan cuando termina la descarga (después de `DOWNLOADED`, como una discrepancia de resumen), y ninguno cambia las etapas anteriores. La firma cubre la imagen, no la versión, así que un dispositivo que deba rechazar degradaciones tiene que imponerlo por su cuenta.

Para obtener la imagen, pide una concesión de descarga en `device-reports`:

```json
{"v":1,"kind":"download.request","assignmentId":"asg-91xq"}
```

La plataforma responde en `device-desired` con una URL que funciona, sin ninguna otra credencial, para este único artefacto hasta `expiresAt` (como máximo quince minutos):

```json
{"v":1,"kind":"download.grant","assignmentId":"asg-91xq","url":"https://…/api/update-management/updates/download/…?cap=…","expiresAt":"2026-10-12T10:15:00Z"}
```

Descárgala con un `GET` HTTP. El endpoint responde a peticiones `Range`, así que una descarga interrumpida se reanuda desde los bytes que ya tienes; cuando la concesión caduca, envía de nuevo el mismo `download.request` para obtener otra. Si la asignación ya no es la vigente, la respuesta es un `update.cancel` en lugar de una concesión.

Informa de cada etapa en `device-reports` con `update.progress`:

```json
{"v":1,"kind":"update.progress","attemptId":"att-7f3k","assignmentId":"asg-91xq","component":"firmware","artifactDigest":"sha256:…","seq":3,"stage":"DOWNLOADING","progress":{"bytes":4194304,"total":8388608},"bootId":"b-20261012-0815"}
```

- `attemptId`, `assignmentId`, `component` y `artifactDigest` repiten los de la asignación; un informe que nombra otro intento u otro digest no se aplica.
- `seq` empieza en 1 y aumenta con cada informe del intento. Enviar de nuevo el mismo informe es inocuo; un informe distinto con un `seq` ya usado es un conflicto y no se aplica.
- `stage` es uno de `RECEIVED`, `DOWNLOADING`, `DOWNLOADED`, `VERIFIED`, `INSTALLING`, `REBOOTING`, `RUNNING`, `FAILED` y `ABANDONED`. Puedes saltarte una etapa cuyo informe se perdió, pero nunca volver a una anterior.
- `progress` (los bytes que tienes y el total, si se conoce) solo se admite con `DOWNLOADING`.
- Informa de `VERIFIED` solo cuando el SHA-256 de los bytes que tienes coincida con `artifact.digest` y, en una asignación `SIGNED`, la firma se verifique.
- `FAILED` debe llevar `result`: `{"code":"FLASH_WRITE","detail":"…"}`, un código de hasta 64 letras mayúsculas, dígitos y guiones bajos, y un detalle de hasta 512 bytes. Los códigos `SIGNATURE_INVALID` y `UNKNOWN_SIGNER` significan que el dispositivo rechazó la firma de la imagen, como se describe arriba.
- `RUNNING` debe llevar `running`: `{"version":"1.4.2"}`, opcionalmente con el `digest` de lo que se ejecuta. Envíalo en cuanto la nueva versión esté en ejecución. Un informe `RUNNING` que nombra cualquier otra versión no se aplica: si el dispositivo no está en la objetivo, informa `FAILED`.
- `bootId` es obligatorio en cada informe: un identificador del arranque actual, de hasta 128 caracteres imprimibles, que cambia cada vez que el dispositivo arranca. Cuando `requiresReboot` es true, la plataforma acepta `RUNNING` como prueba de la actualización solo desde un arranque distinto de aquel en el que el dispositivo la instaló.

`update.cancel` (`{"v":1,"kind":"update.cancel","attemptId":"att-7f3k","assignmentId":"asg-91xq"}`) pide al dispositivo que deje de trabajar en esa asignación. Si no ha empezado a instalar, informa de la `stage` `ABANDONED`; una vez iniciada la instalación, la actualización no se puede revocar.

Después de arrancar, un dispositivo informa de lo que ejecuta cada componente con `update.inventory`, que no nombra ningún intento:

```json
{"v":1,"kind":"update.inventory","component":"firmware","running":{"version":"1.4.2"},"bootId":"b-20261012-0821"}
```

Así es como se resuelve una actualización que la plataforma marcó como `UNKNOWN` porque el dispositivo calló mientras instalaba o reiniciaba.

El `sync` que un dispositivo envía en cada conexión (consulta [Topics de entrega de configuración](#reserved-device-topics)) también hace que la plataforma reenvíe cada actualización en la que el dispositivo todavía debería estar trabajando, y una cancelación para cualquiera cuya cancelación el dispositivo no haya confirmado. Las asignaciones no se retienen, así que suscríbete a `device-desired` antes de enviar el sync.

## Qué sucede a continuación

1. **event-sources** decodifica el mensaje sin procesar.
2. **device-management** autentica el dispositivo mediante su credencial y resuelve el evento. Cada una de las relaciones rastreadas del dispositivo (sus asignaciones a un cliente/área/activo) se registra como un anclaje, de modo que la lectura se puede consultar por cada dimensión. Un dispositivo **sin asignar** sigue informando: su evento no lleva anclajes en lugar de descartarse (consulta [Gestión de asignaciones de dispositivos](./managing-assignments.md)).
3. **event-management** persiste el evento resuelto en una hypertable de TimescaleDB, y **device-state** actualiza la última lectura y la conectividad del dispositivo.

Consulta [Arquitectura → El pipeline de eventos](../concepts/architecture.md#the-event-pipeline).
