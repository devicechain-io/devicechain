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
| ○ | **Ninguno** | No implementado. Cuando se trata de una decisión deliberada y no de trabajo pendiente, la nota lo dice — son dos cosas muy distintas frente a las que planificar. En **Escritura**, la nota dice además qué le ocurre a un comando que emita de todos modos, porque no es lo mismo en todas las filas. |
| — | **No aplica** | La dirección no tiene sentido para esa fila. **No** es un sinónimo de «ninguno», y nunca sustituye a «desconocido» ni a «planeado». |

**La regla que hay detrás de ● y ◐, aplicada a todas las filas de esta página.** Una dirección es
**◐** siempre que la plataforma pueda perder, truncar o rechazar algo **sin decírselo a nadie** —
incluso cuando el transporte en conjunto sea el más completo de la página. **●** se reserva para
una dirección sin ese agujero. Esa es la única razón por la que la ingesta HTTP es `●` en
Suscripción y el bróker de la plataforma no lo es: al superar el límite de ingesta del inquilino,
HTTP responde `429` al publicador, mientras que la vía del bróker descarta el mensaje después de
que el dispositivo ya haya recibido su PUBACK, así que tampoco al publicador le llega nada.

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
| [MQTT](../guides/connecting-a-device.md#mqtt) (bróker de la plataforma) | ○ | ◐ | ◐ |
| [HTTP](../guides/connecting-a-device.md#http) | ○ | ○ | ● |
| MQTT (bróker externo) | ○ | ○ | ◐ |
| [Sparkplug B](../concepts/sparkplug.md) | ○ | ○ | ◐ |
| [LwM2M](../concepts/lwm2m.md) | ◐ | ◐ | ◐ |

### MQTT — el bróker de la plataforma

La vía por defecto, y la más completa. El bróker es el servidor MQTT integrado en NATS, así que no
hay ningún bróker aparte que operar.

- **Suscripción ◐** — el dispositivo publica en su propio topic de eventos y el bróker captura el
  mensaje de forma duradera antes de que ningún código de la plataforma lo vea. La autenticación son
  dos capas independientes: la conexión se autentica en el bróker y queda ligada a los subjects de
  ese único dispositivo, y el evento lleva una credencial que se comprueba de nuevo en el pipeline.
  El único agujero, y la razón de que esto no sea `●`: un mensaje que llega mientras el inquilino
  supera su **límite de tasa de ingesta** se confirma al bróker y se descarta. El dispositivo ya
  recibió su PUBACK cuando el bróker lo capturó, así que nada informa al publicador — en este
  transporte no hay un `429` que enviar. Una flota capaz de superar su límite a ráfagas debería
  dimensionarse contra él, y no confiar en una contrapresión que no existe.
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
  `400` ante un cuerpo que no puede decodificar o un inquilino sintácticamente inválido, `429` cuando
  el inquilino supera su límite de tasa de ingesta, y **`503` cuando el evento no pudo entregarse al
  stream**. `503` es el que hay que reintentar: es la plataforma diciendo, en el único transporte que
  puede decirlo, que sus datos no llegaron. Los demás estados son terminales para esa petición.
- **Escritura ○ / Lectura ○** — **no hay ningún canal descendente en absoluto.** Un dispositivo que
  llega a la plataforma solo por HTTP no puede recibir comandos. Más que una carencia a la espera de
  arreglo, es la forma de la integración: dé también una conexión MQTT a un dispositivo que deba
  recibir comandos.

  🔴 **Un comando emitido a un dispositivo solo-HTTP no se rechaza: caduca.** La plataforma no acuña
  ningún nombre de transporte para un dispositivo que llegó por HTTP (el origen proyectado es el id
  de la propia fuente de eventos, elegido por el operador), así que la compuerta que reconoce un
  transporte no entregable no puede reconocer este. El comando se acepta, se publica en el plano de
  dispositivo donde nadie está suscrito, se marca `SENT` y acaba en `TIMEOUT`. Sparkplug es la única
  fila de esta página en la que un `○` en esta columna produce un `FAILED` inmediato.
- El listener de ingesta termina HTTP en claro y no lleva autenticación de transporte propia — las
  credenciales del dispositivo viajan en el cuerpo del evento. El TLS, donde lo necesite, lo aporta
  lo que ponga por delante del servicio.

### MQTT — un bróker externo, propiedad del operador

La plataforma también puede actuar como cliente en un bróker que usted ya opere, para ingerir de él.

- **Suscripción ◐** — funciona, y faltan cuatro cosas, todas relevantes para cualquier cosa más allá
  de un laboratorio: la conexión es **en claro** (sin TLS), no presenta **ninguna credencial de
  bróker**, es **como mucho una vez en la práctica**, y un mensaje rechazado por exceder un límite se
  **descarta sin devolver nada al publicador**. Prefiera el bróker de la plataforma salvo que
  necesite específicamente leer de uno ya existente.

  Sobre ese tercer punto, el detalle importa si va a elegir un QoS en su propio bróker: la
  plataforma **se suscribe con QoS 1**, así que la pérdida no está en la suscripción. Está en que la
  sesión no es persistente y el traspaso a la decodificación vive en memoria, de modo que un mensaje
  que la plataforma ya tomó de su bróker y aún no ha publicado se pierde si el proceso se reinicia.
  Subir el QoS en su lado no cambia eso, y la plataforma no reclama durabilidad sobre un bróker que
  no le pertenece.
- **Escritura ○ / Lectura ○** — esta integración es solo de ingesta. Un comando emitido a un
  dispositivo que llega por esta vía se comporta exactamente igual que en HTTP, más arriba:
  publicado, `SENT` y luego `TIMEOUT`.

### Sparkplug B

Para flotas ya existentes que hablan Sparkplug con su propio bróker.

- **Suscripción ◐** — se decodifican NBIRTH/NDATA/DBIRTH/DDATA, incluidas las tablas de alias y el
  seguimiento de secuencia, y BIRTH/DEATH dirigen una presencia **autoritativa** en lugar de una
  presencia inferida por temporizador. Lo que impide que sea `●`: **solo las métricas numéricas se
  convierten en mediciones**. Una métrica booleana, de cadena, de bytes, DataSet o Template se omite
  al decodificar el payload, sin registrar nada y sin decir nada. Una flota cuyas señales
  interesantes sean booleanas —un indicador de marcha, un bit de fallo— mostrará un dispositivo
  autoritativamente en línea que no reporta nada.
- **Escritura ○ — deliberadamente fuera de alcance, no inacabado.** No hay salida de comandos
  Sparkplug (`DCMD`), y no es una carencia a la espera de trabajo: una flota Sparkplug reside en la
  infraestructura MQTT *del cliente*, así que nada tiende un puente entre el flujo de comandos de la
  plataforma y ella. Un comando emitido a un dispositivo Sparkplug acaba en `FAILED`, no entregable
  — con dos matices que conviene conocer antes de confiar en ello. Ocurre en el **barrido de
  entrega**, que corre cada 30 segundos, y no en el momento en que lo encola. Y exige que la
  compuerta de presencia esté **configurada**: esa compuerta necesita el secreto entre servicios y un
  endpoint de `device-state`, y sin cualquiera de los dos está apagada —lo registra al arrancar— y el
  comando se despacha como cualquier otro y acaba en `TIMEOUT`.
- **Lectura ○** — por la misma razón.

:::info Qué *sí* publica la plataforma en Sparkplug, y por qué una ACL debe permitirlo
No se publica ningún `DCMD`, nunca, y **ningún `NCMD` es alcanzable desde la API de comandos** — el
único `NCMD` que la plataforma emite es un `Node Control/Rebirth` interno, emitido por la máquina de
sesión de la propia Host Application para reparar un hueco de secuencia, con QoS 0 y sin retener.

La Host Application sí publica, en cambio, su propio mensaje **`STATE` en
`spBv1.0/STATE/{host_id}`, retenido y con QoS 1**: al conectarse, al detenerse limpiamente y como su
Last-Will si muere. Ese es el contrato de nacimiento/muerte de la Host Application de Sparkplug, y
los nodos de borde lo leen. **Si está escribiendo ACLs de bróker, conceda a su cliente permiso de
publicación en ese topic** — si lo deniega, la sesión de la Host Application queda rota desde la
primera conexión.
:::

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

- **Lectura ◐** — implementada como un comando de dispositivo, y el cuerpo de la respuesta vuelve.
  La limitación es el tope: un cuerpo de más de **8 KiB se trunca**, y la respuesta se sigue
  reportando como un éxito. «Limitado» es el modelo mental equivocado — no se rechaza nada, y nada
  marca el resultado como parcial, así que la lectura de un recurso grande vuelve con aspecto de
  estar completa.
- **Escritura ◐** — un **único recurso escalar** cada vez. No se admite escribir una instancia de
  objeto ni varios recursos en una sola operación, ni la actualización parcial. Los valores se
  limitan a 8 KiB.
- **Suscripción ◐** — Observe funciona, **solo se decodifican notificaciones SenML-JSON** y, de
  ellas, **solo los recursos numéricos se convierten en mediciones** — un objeto de naturaleza
  booleana no produce telemetría alguna, la misma limitación que tiene Sparkplug. Esto
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
| Read | ◐ | GET de CoAP; una respuesta de más de 8 KiB se trunca en silencio y se sigue reportando como correcta |
| Write | ◐ | Un único recurso escalar, solo reemplazo |
| Execute | ● | Con o sin argumentos |
| Observe | ◐ | Solo SenML-JSON; solo recursos numéricos; lista de objetos fija; 32 por registro |
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
| `publish` → MQTT | — | ● | — | QoS 0/1/2; usuario + secreto; **sin ajustes de TLS** — vea más abajo |
| `publish` → Kafka | — | ● | — | TLS; SASL `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `publish` → AWS SNS | — | ● | — | Solo credenciales estáticas por inquilino |
| `publish` → AWS SQS | — | ● | — | Solo credenciales estáticas por inquilino |
| `publish` → Google Pub/Sub | — | ○ | — | **Creable pero no despachable** — vea más abajo |

La ausencia de TLS en la fila de MQTT merece decirse con claridad junto a la de Kafka, que sí tiene
un conmutador `tls` real: la configuración del conector MQTT **no tiene campos de TLS de ningún
tipo**, y rechaza claves desconocidas, así que no hay nada que redactar. El TLS solo ocurre de forma
implícita, dando al bróker una URL `ssl://` — sin ninguna manera de aportar una CA, un certificado de
cliente o un ajuste de verificación.

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
| Protocolos de bus de campo industrial — OPC-UA, Modbus, BACnet | No disponibles como transportes de plataforma, y **nada de lo que publica el proyecto los habla.** La forma admitida es una pasarela local que hable el bus de campo en la red de planta y reenvíe por MQTT o HTTP; la traducción de protocolo la aporta usted. El proyecto sí publica `dc-edge-agent`, que hace la *otra* mitad de ese trabajo — termina localmente la vía MQTT del dispositivo, almacena de forma duradera durante un corte de WAN y reenvía **solo por MQTT** —, pero no habla ningún bus de campo. |
