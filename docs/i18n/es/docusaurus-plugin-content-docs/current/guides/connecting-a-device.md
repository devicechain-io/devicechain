---
sidebar_position: 2
title: Conexión de un dispositivo
---

# Conexión de un dispositivo

Los dispositivos se conectan a DeviceChain mediante **MQTT** (servido directamente por el servidor MQTT integrado de NATS en el puerto 1883 — sin broker independiente) o **HTTP**. Ambos transportes alimentan el mismo pipeline de decodificación → resolución → persistencia, por lo que el cuerpo del evento JSON es idéntico entre ambos.

:::note Estado
La ingesta por MQTT y HTTP están disponibles. **Las conexiones se protegen a nivel del broker:** los listeners de MQTT/NATS son **TLS**, y un auth-callout de NATS autentica cada conexión y la vincula a los subjects de ese único dispositivo, de modo que un dispositivo solo puede publicar sus propios eventos y leer sus propios comandos. La autenticación del dispositivo también se aplica **por evento** mediante credencial, y el modo de autenticación de dispositivo predeterminado es **`required`** — por lo que se espera una credencial tanto en la conexión como en el evento. Consulte [Credenciales de dispositivo](./device-credentials.md). Aún están planificados transportes adicionales (CoAP, WebSocket) y el flujo completo de aprovisionamiento/reclamación de autoservicio.
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
- `payload` — la forma depende de `eventType`; los valores de medición son cadenas de texto.

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
  [Comandos y el contrato de capacidades](../concepts/domain-model.md#commands-and-the-capability-contract).

## Respuesta a un comando {#responding-to-a-command}

Reporte el resultado publicando en el topic de respuestas de comando del inquilino:

```
{instanceId}/{tenant}/command-responses
```

```bash
mosquitto_pub \
  --cafile instance-ca.crt \
  -h <mqtt-host> -p 1883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t "devicechain/acme/command-responses" \
  -m '{"commandToken":"6f1c0f8e-6d1e-4a1a-9a3f-1f2b0d0a5c11","success":true,"payload":"rebooting in 5s"}'
```

- **`commandToken` debe ser el `token` del sobre de entrega** — el token del comando,
  no el del dispositivo. Este es el error más común: enviar aquí el token del dispositivo
  no coincide con ningún comando y la respuesta se descarta.
- **`success`** mueve el comando a `SUCCESSFUL` o `FAILED`.
- **`payload`** / **`error`** son cadenas opcionales, que se muestran en el historial de
  comandos de la consola y se devuelven a través de la API.

A diferencia del topic de eventos, este **no** es por dispositivo: cada dispositivo de un inquilino publica
en el mismo subject, y una respuesta identifica su comando mediante el token. El inquilino se toma
del topic en lugar del cuerpo, de modo que un dispositivo no puede responder por otro inquilino.

:::info Responder es lo que completa el ciclo de vida
Un comando que nunca se responde permanece en `SENT` hasta que expira. Sin una respuesta, la
plataforma solo sabe que el comando fue despachado — no que el dispositivo actuó sobre él. Si
sus dispositivos no responden, configure un `expiresAt` al emitir comandos para que alcancen un
estado terminal en lugar de quedar en tránsito indefinidamente.
:::

## Qué sucede a continuación

1. **event-sources** decodifica el mensaje sin procesar.
2. **device-management** autentica el dispositivo mediante su credencial y resuelve el evento: **cada** una de las relaciones rastreadas del dispositivo (sus asignaciones a un cliente/área/activo) se registra como un anclaje, de modo que la lectura sea consultable por cada dimensión. Un dispositivo **sin asignar** igualmente reporta — su evento simplemente no lleva anclajes en lugar de ser descartado (consulte [Gestión de asignaciones de dispositivos](./managing-assignments.md)).
3. **event-management** persiste el evento resuelto en una hypertable de TimescaleDB, y **device-state** actualiza la última lectura y conectividad del dispositivo.

Consulte [Arquitectura → El pipeline de eventos](../concepts/architecture.md#the-event-pipeline).
