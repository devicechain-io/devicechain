---
sidebar_position: 2
title: Matriz de capacidades por transporte
---

# Matriz de capacidades por transporte

Qué hace realmente hoy cada transporte, por dirección, con las carencias nombradas.

:::info El código es la fuente de verdad
Esta página se mantiene a mano contra la implementación. No se genera, así que puede quedarse
atrás — y cuando ella y su instancia discrepen, **su instancia tiene razón**. Cada afirmación de
más abajo se leyó del servicio que la implementa, no de un documento de diseño, y todo lo que no
pudo establecerse así aparece marcado como tal en lugar de rellenado.

Si encuentra una celda que exagera lo que hace la plataforma, eso es un fallo de esta página y vale
la pena [comunicarlo](../getting-help.md).
:::

## Cómo leer esta página

**Tres estados, y ningún cuarto.** Una capacidad es una de estas:

| | | |
| --- | --- | --- |
| ● | **Completo** | Implementado, sin ninguna limitación específica de esta dirección. Las salvedades ordinarias que afectan al transporte entero están en sus notas. |
| ◐ | **Parcial** | Implementado, y **falta algo concreto**. La nota dice qué. Léala antes de diseñar apoyándose en esa fila. |
| ○ | **Ninguno** | No implementado. Cuando se trata de una decisión deliberada y no de trabajo pendiente, la nota lo dice — son dos cosas muy distintas frente a las que planificar. |
| — | **No aplica** | La dirección no tiene sentido para esa fila. **No** es un sinónimo de «ninguno», y nunca sustituye a «desconocido» ni a «planeado». |

**Tres direcciones.** Se nombran desde el punto de vista de la plataforma:

- **Lectura** — la plataforma pide un valor a un dispositivo y obtiene la respuesta en ese intercambio.
- **Escritura** — la plataforma fija un valor en un dispositivo, o le dice que actúe.
- **Suscripción** — el dispositivo envía lecturas sin que se le pidan cada vez.

Observe que `—` no aparece en absoluto en la tabla de transportes de dispositivo. Las tres
direcciones tienen sentido para todos ellos, así que **Ninguno** ahí es siempre una respuesta real a
una pregunta real, y no una no-pregunta.

## Transportes de dispositivo

Cómo llegan los dispositivos a la plataforma, y cómo llega la plataforma de vuelta.

| Transporte | Lectura | Escritura | Suscripción |
| --- | :---: | :---: | :---: |
| [MQTT](../guides/connecting-a-device.md#mqtt) (bróker de la plataforma) | ○ | ◐ | ● |
| [HTTP](../guides/connecting-a-device.md#http) | ○ | ○ | ● |
| MQTT (bróker externo) | ○ | ○ | ◐ |
| [Sparkplug B](../concepts/sparkplug.md) | ○ | ○ | ● |
| [LwM2M](../concepts/lwm2m.md) | ● | ◐ | ◐ |

### MQTT — el bróker de la plataforma

La vía por defecto, y la más completa. El bróker es el servidor MQTT integrado en NATS, así que no
hay ningún bróker aparte que operar.

- **Suscripción ●** — el dispositivo publica en su propio topic de eventos y el bróker captura el
  mensaje de forma duradera antes de que ningún código de la plataforma lo vea. La autenticación son
  dos capas independientes: la conexión se autentica en el bróker y queda ligada a los subjects de
  ese único dispositivo, y el evento lleva una credencial que se comprueba de nuevo en el pipeline.
- **Escritura ◐** — los comandos se entregan, y la limitación merece planificarse: la entrega es
  **solo en vivo y sin confirmación**. Una publicación alcanza a un dispositivo que esté conectado y
  suscrito en ese instante; el bróker no la retiene para uno que no lo esté, y nada informa a la
  plataforma de si el dispositivo la recibió. Deliberadamente no existe un estado `DELIVERED`, porque
  confirmar la entrega por separado de la respuesta exigiría un acuse de recibo que este transporte
  no proporciona. Un comando se completa cuando **el dispositivo lo responde** — vea
  [respuesta a un comando](../guides/connecting-a-device.md#responding-to-a-command).
- **Lectura ○** — no hay ninguna primitiva de petición/respuesta iniciada por la plataforma. Puede
  expresar una lectura como un comando cuya respuesta lleve el valor, pero eso es un vocabulario que
  usted define en el perfil del dispositivo, no algo que aporte el transporte.

### HTTP

Un endpoint `POST` para el mismo cuerpo de evento JSON. Sencillo, y de un solo sentido.

- **Suscripción ●** — `POST /{instanceId}/{tenant}/events` devuelve `202` una vez encolado el evento,
  `400` ante un cuerpo que no puede decodificar, y `429` cuando el inquilino supera su límite de tasa
  de ingesta.
- **Escritura ○ / Lectura ○** — **no hay ningún canal descendente en absoluto.** Un dispositivo que
  llega a la plataforma solo por HTTP no puede recibir comandos. Más que una carencia a la espera de
  arreglo, es la forma de la integración: dé también una conexión MQTT a un dispositivo que deba
  recibir comandos.
- El listener de ingesta termina HTTP en claro y no lleva autenticación de transporte propia — las
  credenciales del dispositivo viajan en el cuerpo del evento. El TLS, donde lo necesite, lo aporta
  lo que ponga por delante del servicio.

### MQTT — un bróker externo, propiedad del operador

La plataforma también puede actuar como cliente en un bróker que usted ya opere, para ingerir de él.

- **Suscripción ◐** — funciona, y faltan cuatro cosas, todas relevantes para cualquier cosa más allá
  de un laboratorio: la conexión es **en claro** (sin TLS), no presenta **ninguna credencial de
  bróker**, es **como mucho una vez** por decisión (la plataforma no reclama durabilidad sobre un
  bróker que no le pertenece), y un mensaje rechazado por exceder un límite se **descarta sin devolver
  nada al publicador**. Prefiera el bróker de la plataforma salvo que necesite específicamente leer
  de uno ya existente.
- **Escritura ○ / Lectura ○** — esta integración es solo de ingesta.

### Sparkplug B

Para flotas ya existentes que hablan Sparkplug con su propio bróker.

- **Suscripción ●** — se decodifican NBIRTH/NDATA/DBIRTH/DDATA, incluidas las tablas de alias y el
  seguimiento de secuencia, y BIRTH/DEATH dirigen una presencia **autoritativa** en lugar de una
  presencia inferida por temporizador.
- **Escritura ○ — deliberadamente fuera de alcance, no inacabado.** No hay salida de comandos
  Sparkplug (`DCMD`), y no es una carencia a la espera de trabajo: una flota Sparkplug reside en la
  infraestructura MQTT *del cliente*, así que nada tiende un puente entre el flujo de comandos de la
  plataforma y ella. Un comando emitido a un dispositivo Sparkplug se marca de inmediato como no
  entregable, en lugar de quedar pendiente hasta caducar. El único mensaje Sparkplug que la
  plataforma llega a publicar es un `Node Control/Rebirth` interno con el que repara su propio estado
  de sesión; no es alcanzable desde la API de comandos.
- **Lectura ○** — por la misma razón.

:::caution En Sparkplug la identidad del dispositivo se establece en el bróker, no por dispositivo
La identidad de un dispositivo Sparkplug se deriva del topic en el que publicó. Eso significa que el
modo de autenticación de dispositivo por evento **no** impide que un publicador de su bróker envíe
bajo la identidad de otro dispositivo **dentro del mismo inquilino**. El cruce entre inquilinos está
cerrado —la pertenencia a inquilino la fija la conexión de bróker por la que llegó un mensaje, nunca
nada del mensaje— pero la separación dentro de un inquilino se aplica en *su* bróker, con credenciales
por cliente y permisos de topic. Dimensione eso antes de apuntar un bróker compartido a un inquilino.
:::

### LwM2M

Para dispositivos con recursos limitados sobre CoAP/UDP con DTLS.

- **Lectura ●** — implementada como un comando de dispositivo; el cuerpo de la respuesta vuelve,
  limitado a 8 KiB.
- **Escritura ◐** — un **único recurso escalar** cada vez. No se admite escribir una instancia de
  objeto ni varios recursos en una sola operación, ni la actualización parcial. Los valores se
  limitan a 8 KiB.
- **Suscripción ◐** — Observe funciona, y **solo se decodifican notificaciones SenML-JSON**. Esto
  tiene una consecuencia que conviene dimensionar de antemano: un cliente conforme **solo LwM2M 1.0**
  no puede producir SenML, así que rechaza correctamente el Observe. Ese dispositivo sigue
  registrándose, dirige la presencia y acepta comandos, pero no reporta **ninguna telemetría**.
  Decodificar el formato TLV más antiguo es la tarea que cierra esto. Los objetos observados se
  restringen a una lista de permitidos incorporada que no es configurable, con un tope de 32
  observaciones por registro, y las observaciones **no sobreviven a un relevo de líder** — la
  presencia se reconstruye, la telemetría se restablece solo a medida que se renueva el registro de
  cada dispositivo.
- Los comandos a un dispositivo dormido se **retienen de forma duradera y se drenan** cuando vuelve a
  aparecer, que es el único lugar donde la plataforma retiene un comando en vez de exigir que el
  dispositivo esté en vivo.

#### Operaciones LwM2M en detalle

| Operación | | Notas |
| --- | :---: | --- |
| Read | ● | GET de CoAP |
| Write | ◐ | Un único recurso escalar, solo reemplazo |
| Execute | ● | Con o sin argumentos |
| Observe | ◐ | Solo SenML-JSON; lista de objetos fija; 32 por registro |
| Discover | ○ | No implementado |
| Create | ○ | No implementado |
| Delete | ○ | No implementado |
| Write-Attributes | ○ | No implementado — las bandas de notificación no se pueden fijar desde la plataforma |
| Bootstrap | ○ | No implementado; un servidor Bootstrap está planeado |

La autenticación de dispositivo es por dispositivo, en el handshake DTLS, con claves precompartidas.
Las credenciales X.509 y de clave pública en bruto están planeadas.

## Conectores de salida

A dónde envía datos la plataforma cuando dispara una regla. No llevan direcciones de dispositivo —un
conector es un sumidero de un solo sentido—, así que **Lectura** y **Suscripción** son `—` y no
`Ninguno`.

| Conector | Lectura | Escritura | Suscripción | Notas |
| --- | :---: | :---: | :---: | --- |
| Webhook `httpCall` | — | ● | — | Solo `POST`; se rechaza cualquier otro método |
| `publish` → MQTT | — | ● | — | QoS 0/1/2; TLS; usuario + secreto |
| `publish` → Kafka | — | ● | — | TLS; SASL `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `publish` → AWS SNS | — | ● | — | Solo credenciales estáticas por inquilino |
| `publish` → AWS SQS | — | ● | — | Solo credenciales estáticas por inquilino |
| `publish` → Google Pub/Sub | — | ○ | — | **Creable pero no despachable** — vea más abajo |

Los dos conectores de AWS exigen deliberadamente una clave de acceso estática y **no** recurren a la
identidad IAM ambiental del pod en el que se ejecutan. Tomar prestada la identidad de nube de la
propia plataforma para hacer la llamada de un inquilino es justamente la confusión que esa separación
existe para impedir.

:::warning Un conector de Google Pub/Sub se puede crear y nunca enviará nada
`gcp_pubsub` es un tipo de conector válido: la API lo acepta, y el conector se guarda y se publica
como cualquier otro. **No tiene generador de salida**, así que todo despacho hacia él falla de forma
terminal y acaba en la cola de mensajes muertos — reconocido pero no ejecutable, nunca descartado en
silencio.

La razón de que se retenga en lugar de despacharse: la salida Pub/Sub de Bento se autentica mediante
Application Default Credentials —la identidad de todo el proceso— sin ningún campo de credencial por
conector, de modo que no podría inyectarse la credencial de un inquilino sin que todos compartieran
una sola identidad. Se despachará cuando exista una forma de dar a cada conector la suya.
:::

Aparte de los conectores, los [canales de notificación](../guides/notification-channels.md) llegan a
personas y no a sistemas, por **SMTP** y **webhook**.

## No disponible

Se nombra explícitamente porque, desde fuera de la plataforma, «ausente de la lista de arriba» y
«se preguntó y la respuesta fue no» son indistinguibles — y solo una de las dos merece la espera.

| | |
| --- | --- |
| Ingesta de dispositivo por WebSocket | No disponible. Figura como planeada en la [introducción](../intro.md). |
| CoAP fuera de LwM2M | No disponible. CoAP llega a la plataforma a través de [LwM2M](../concepts/lwm2m.md) y no de otro modo. |
| NATS en bruto como transporte de dispositivo | No disponible. Una credencial de dispositivo autoriza una conexión MQTT, y no existe un cliente de dispositivo nativo de NATS. |
| Salida de comandos Sparkplug (`DCMD`) | No disponible, y deliberadamente fuera de alcance en lugar de pendiente — vea [más arriba](#sparkplug-b). |
| Protocolos de bus de campo industrial — OPC-UA, Modbus, BACnet | No disponibles como transportes de plataforma. La forma admitida es una pasarela local que los hable en la red de planta y reenvíe por MQTT o HTTP. |
