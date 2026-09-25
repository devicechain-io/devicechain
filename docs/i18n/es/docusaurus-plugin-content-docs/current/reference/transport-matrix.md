---
sidebar_position: 2
title: Matriz de capacidades por transporte
---

# Matriz de capacidades por transporte

Esta página muestra qué hace hoy cada transporte, en cada dirección, y nombra las carencias.

:::info El código es la fuente de verdad
Esta página se mantiene a mano contra la implementación, no se genera, así que puede quedarse atrás.
Cuando ella y tu instancia discrepen, tu instancia tiene razón. Cada afirmación de más abajo se leyó
del servicio que la implementa, no de un documento de diseño; todo lo que no pudo establecerse así
aparece marcado como tal en lugar de rellenado. Una celda que exagera lo que hace la plataforma es un
fallo de esta página; por favor, [comunícalo](../getting-help.md).
:::

## Cómo leer esta página

Cada capacidad está en uno de tres estados, y no hay un cuarto:

| | | |
| --- | --- | --- |
| ● | **Completo** | Implementado, sin ninguna limitación específica de esta dirección. Las salvedades ordinarias que afectan al transporte entero están en sus notas. |
| ◐ | **Parcial** | Implementado, y falta algo concreto. La nota dice qué; léela antes de diseñar apoyándote en esa fila. |
| ○ | **Ninguno** | No implementado. Cuando se trata de una decisión deliberada y no de trabajo pendiente, la nota lo dice: son dos cosas muy distintas frente a las que planificar. En **Escritura**, la nota dice además qué le ocurre a un comando que emitas de todos modos, porque no es lo mismo en todas las filas. |
| — | **No aplica** | La dirección no tiene sentido para esa fila. No es un sinónimo de «ninguno», y nunca sustituye a «desconocido» ni a «planeado». |

Una sola regla separa ● de ◐, y se aplica a todas las filas de esta página. Una dirección es ◐
siempre que la plataforma pueda perder, truncar o rechazar algo **sin decírselo a nadie**, incluso
cuando el transporte en conjunto sea el más completo de la página. ● se reserva para una dirección sin
ese agujero. Esa es la única razón por la que la ingesta HTTP es `●` en Suscripción y el bróker de la
plataforma no lo es: al superar el límite de ingesta del inquilino, HTTP responde `429` al publicador,
mientras que la vía del bróker descarta el mensaje después de que el dispositivo ya haya recibido su
PUBACK, y al publicador no le llega nada.

Las direcciones se nombran desde el punto de vista de la plataforma:

- **Lectura**: la plataforma pide un valor a un dispositivo y obtiene la respuesta en ese intercambio.
- **Escritura**: la plataforma fija un valor en un dispositivo, o le dice que actúe.
- **Suscripción**: el dispositivo envía lecturas sin que se le pidan cada vez.

`—` nunca aparece en la tabla de transportes de dispositivo. Las tres direcciones tienen sentido para
todos los transportes de dispositivo, así que **Ninguno** ahí es siempre una respuesta real a una
pregunta real.

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

- **Suscripción ◐**: el dispositivo publica en su propio topic de eventos, y el bróker captura el
  mensaje de forma duradera antes de que ningún código de la plataforma lo vea. La autenticación
  tiene dos capas independientes: la conexión se autentica en el bróker y queda ligada a los
  subjects de ese único dispositivo, y el evento lleva una credencial que se comprueba de nuevo en
  el pipeline.

  Hay un agujero, y es la razón de que esto no sea `●`. Un mensaje que llega mientras el inquilino
  supera su **límite de tasa de ingesta** se confirma al bróker y se descarta. El dispositivo ya
  recibió su PUBACK cuando el bróker lo capturó, así que nada informa al publicador; en este
  transporte no hay un `429` que enviar. Si tu flota puede superar su límite a ráfagas,
  dimensiónala contra ese límite en lugar de confiar en una contrapresión que no existe.
- **Escritura ◐**: los comandos se entregan, pero la entrega es **solo en vivo y sin
  confirmación**. Una publicación alcanza a un dispositivo que esté conectado y suscrito en ese
  instante. El bróker no la retiene para uno que no lo esté, y nada informa a la plataforma de si
  el dispositivo la recibió. Deliberadamente no existe un estado `DELIVERED`: confirmar la entrega
  por separado de la respuesta exigiría un acuse de recibo que este transporte no proporciona. Un
  comando se completa cuando el dispositivo lo responde; consulta
  [respuesta a un comando](../guides/connecting-a-device.md#responding-to-a-command).
- **Lectura ○**: no hay ninguna primitiva de petición/respuesta iniciada por la plataforma. Puedes
  expresar una lectura como un comando cuya respuesta lleve el valor, pero eso es un vocabulario
  que tú defines en el perfil del dispositivo, no algo que aporte el transporte.

### HTTP

Un endpoint `POST` para el mismo cuerpo de evento JSON. Sencillo, y de un solo sentido.

- **Suscripción ●**: `POST /{instanceId}/{tenant}/events` devuelve:
  - `202` una vez encolado el evento;
  - `400` ante un cuerpo que no puede decodificar o un inquilino sintácticamente inválido;
  - `429` cuando el inquilino supera su límite de tasa de ingesta;
  - **`503` cuando el evento no pudo entregarse al stream.**

  Reintenta ante un `503`: es la plataforma diciéndote, en el único transporte que puede hacerlo,
  que tus datos no llegaron. Un `429` también significa que el evento no se aceptó; lleva una
  cabecera `Retry-After`, así que espera y reintenta. `202` y `400` son terminales para esa
  petición.
- **Escritura ○ / Lectura ○**: **no hay ningún canal descendente en absoluto.** Un dispositivo que
  llega a la plataforma solo por HTTP no puede recibir comandos. Más que una carencia a la espera
  de arreglo, es la forma de la integración: da también una conexión MQTT a un dispositivo que deba
  recibir comandos.

  Un comando emitido a un dispositivo solo HTTP **no se rechaza: caduca**. La plataforma no acuña
  ningún nombre de transporte para un dispositivo que llegó por HTTP (el origen registrado del
  dispositivo es el id que el operador dio a la fuente de eventos), así que la compuerta que
  reconoce un transporte no entregable no puede reconocer este. El comando se acepta, se publica
  donde ningún dispositivo está suscrito para recibirlo, se marca `SENT` y acaba en `TIMEOUT`.
  Sparkplug es la única fila de esta página en la que un `○` en esta columna produce un `FAILED`
  inmediato.
- El listener de ingesta termina HTTP en claro y no tiene autenticación de transporte propia; las
  credenciales del dispositivo viajan en el cuerpo del evento. Donde necesites TLS, lo aporta lo
  que pongas por delante del servicio.

### MQTT — un bróker externo, propiedad del operador

La plataforma también puede actuar como cliente en un bróker que ya operes, para ingerir de él.

- **Suscripción ◐**: funciona, pero faltan cuatro cosas, y todas importan más allá de un
  laboratorio:
  - la conexión es en claro (sin TLS);
  - no presenta ninguna credencial de bróker;
  - en la práctica es como mucho una vez;
  - un mensaje rechazado por exceder un límite se descarta, sin devolver nada al publicador.

  Prefiere el bróker de la plataforma salvo que necesites específicamente leer de uno ya existente.

  Lo de como mucho una vez importa si vas a elegir un QoS en tu propio bróker. La plataforma se
  suscribe con QoS 1, así que la pérdida no está en la suscripción. La sesión no es persistente y
  el traspaso a la decodificación vive en memoria, de modo que un mensaje que la plataforma ya tomó
  de tu bróker y aún no ha publicado se pierde si el proceso se reinicia. Subir el QoS en tu lado
  no cambia eso, y la plataforma no reclama durabilidad sobre un bróker que no le pertenece.

  Si tu bróker se reinicia o la conexión se cae, la plataforma se reconecta y vuelve a suscribirse
  por sí sola. Un bróker que rechaza la suscripción, ya sea al arrancar o tras una reconexión (por
  ejemplo, porque un cambio de ACL deniega el topic), detiene **todo el servicio event-sources** con
  un error en lugar de dejarlo conectado sin ingerir nada. Eso incluye la ingesta desde el bróker de
  la plataforma y por HTTP, no solo esta fuente: el servicio se reinicia y sigue fallando hasta que
  el bróker vuelva a conceder la suscripción.
- **Escritura ○ / Lectura ○**: esta integración es solo de ingesta. Un comando emitido a un
  dispositivo que llega por esta vía se comporta exactamente igual que en HTTP, más arriba:
  publicado, `SENT` y luego `TIMEOUT`.

### Sparkplug B

Para flotas ya existentes que hablan Sparkplug con su propio bróker.

- **Suscripción ◐**: se decodifican NBIRTH/NDATA/DBIRTH/DDATA, incluidas las tablas de alias y el
  seguimiento de secuencia, y BIRTH/DEATH dirigen una presencia autoritativa en lugar de una
  presencia inferida por temporizador. Lo que impide que sea `●`: **solo las métricas numéricas se
  convierten en mediciones**. Una métrica booleana, de cadena, de bytes, DataSet o Template se omite
  al decodificar el payload, sin registrar nada y sin decir nada. Si las señales interesantes de tu
  flota son booleanas (un indicador de marcha, un bit de fallo), verás un dispositivo
  autoritativamente en línea que no reporta nada.
- **Escritura ○: deliberadamente fuera de alcance, no inacabado.** No hay salida de comandos
  Sparkplug (`DCMD`), y ninguna espera trabajo pendiente: una flota Sparkplug reside en la
  infraestructura MQTT *del cliente*, así que nada tiende un puente entre el flujo de comandos de la
  plataforma y ella. Un comando emitido a un dispositivo Sparkplug acaba en `FAILED`, no entregable,
  con dos matices:
  - **El veredicto suele ser rápido, pero no es sincrónico con el encolado.** Encolar un comando
    desencadena un intento de despacho inmediato para ese dispositivo. Cuando es el único comando
    encolado del dispositivo, la compuerta de presencia lo marca como fallido en el acto. Si el
    dispositivo ya tiene otros comandos encolados, ese intento se retira y el veredicto llega en el
    siguiente barrido de entrega, que corre cada 30 segundos por defecto (configurable entre 5 y
    300).
  - **Exige que la compuerta de presencia esté configurada.** La compuerta de presencia es la
    comprobación que retiene o marca como fallido un comando según lo que el transporte del
    dispositivo informa sobre él. Necesita el secreto entre servicios y un endpoint de
    `device-state`. Sin cualquiera de los dos está apagada (lo registra al arrancar), y el comando
    se despacha como cualquier otro y acaba en `TIMEOUT`.
- **Lectura ○**: por la misma razón.

Lo que la plataforma *sí* publica en Sparkplug: ningún `DCMD`, nunca, y ningún `NCMD` alcanzable
desde la API de comandos. El único `NCMD` que la plataforma emite es un `Node Control/Rebirth`
interno, emitido por el seguimiento de sesión de la propia Host Application para reparar un hueco de
secuencia, con QoS 0 y sin retener.

La Host Application sí publica su propio mensaje `STATE` en `spBv1.0/STATE/{host_id}`, retenido y
con QoS 1: al conectarse, al detenerse limpiamente y como su Last-Will si muere. Ese es el contrato
de nacimiento/muerte de la Host Application de Sparkplug, y los nodos de borde lo leen.

:::warning Las ACL del bróker deben permitir la publicación de `STATE` de la plataforma
Si escribes ACL de bróker, concede al cliente de la plataforma permiso de publicación en
`spBv1.0/STATE/{host_id}`. Si lo deniegas, la sesión de la Host Application queda **rota desde la
primera conexión**.
:::

:::caution En Sparkplug la identidad del dispositivo se establece en el bróker, no por dispositivo
La identidad de un dispositivo Sparkplug proviene del topic en el que publicó, así que el modo de
autenticación de dispositivo por evento **no** impide que un publicador de tu bróker envíe bajo la
identidad de otro dispositivo dentro del mismo inquilino. Entre inquilinos esto no puede ocurrir: la
pertenencia a inquilino la fija la conexión de bróker por la que llegó un mensaje, nunca nada del
mensaje. La separación dentro de un inquilino se aplica en *tu* bróker, con credenciales por cliente
y permisos de topic. Dimensiona eso antes de apuntar un bróker compartido a un inquilino.
:::

### LwM2M

Para dispositivos con recursos limitados sobre CoAP/UDP con DTLS.

- **Lectura ◐**: implementada como un comando de dispositivo, y el cuerpo de la respuesta vuelve.
  La limitación es que un cuerpo de más de **8 KiB se trunca** y la respuesta se sigue reportando
  como un éxito. «Limitado» es el modelo mental equivocado: no se rechaza nada y nada marca el
  resultado como parcial, así que la lectura de un recurso grande vuelve con aspecto de estar
  completa.
- **Escritura ◐**: un **único recurso escalar** cada vez. No se admite escribir una instancia de
  objeto, escribir varios recursos en una sola operación ni la actualización parcial. Los valores se
  limitan a 8 KiB.
- **Suscripción ◐**: Observe funciona, pero **solo se decodifican notificaciones SenML-JSON** y, de
  ellas, **solo los recursos numéricos se convierten en mediciones**. Un objeto de naturaleza
  booleana no produce telemetría alguna, la misma limitación que tiene Sparkplug.

  Dimensiona esto de antemano: un cliente conforme **solo LwM2M 1.0** no puede producir SenML, así
  que rechaza correctamente el Observe. Ese dispositivo sigue registrándose, dirige la presencia y
  acepta comandos, pero no reporta **ninguna telemetría**. Decodificar el formato TLV más antiguo es
  la tarea pendiente que cierra esto.

  Los objetos observados se restringen a una lista de permitidos incorporada que no es
  configurable, con un tope de 32 observaciones por registro. Las observaciones **no sobreviven a
  un relevo de líder**: la presencia se reconstruye, y la telemetría se restablece solo a medida que
  se renueva el registro de cada dispositivo.
- Los comandos a un dispositivo dormido se retienen de forma duradera y se drenan cuando vuelve a
  aparecer, registrados como `PARKED`. Es el único lugar donde un comando que el transporte ya
  intentó entregar se conserva para el dispositivo en vez de perderse. No es la única retención de
  la plataforma: para todos los transportes (MQTT incluido, cuando es el propio bróker quien informa
  la presencia del dispositivo), un comando cuyo dispositivo el transporte afirma ausente se retiene
  como `HELD` antes de publicarse, y se libera cuando vuelve la presencia. Consulta
  [comandos para un dispositivo ausente](../concepts/commands.md#commands-to-a-device-that-is-away).

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
| Write-Attributes | ○ | No implementado: las bandas de notificación no se pueden fijar desde la plataforma |
| Bootstrap | ○ | No implementado; un servidor Bootstrap está planeado |

La autenticación de dispositivo es por dispositivo, en el handshake DTLS, con claves precompartidas.
Las credenciales X.509 y de clave pública en bruto están planeadas.

## Conectores de salida

A dónde envía datos la plataforma cuando dispara una regla. Un conector es un sumidero de un solo
sentido sin direcciones de dispositivo, así que **Lectura** y **Suscripción** son `—` y no
`Ninguno`.

| Conector | Lectura | Escritura | Suscripción | Notas |
| --- | :---: | :---: | :---: | --- |
| Webhook `httpCall` | — | ● | — | Solo `POST`; se rechaza cualquier otro método |
| `publish` → MQTT | — | ● | — | QoS 0/1/2; usuario + secreto; URL `tcp`, `mqtt`, `ssl`, `tls`, `mqtts`, `ws`, `wss`; **sin ajustes de TLS** (consulta más abajo) |
| `publish` → Kafka | — | ● | — | TLS; SASL `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `publish` → AWS SNS | — | ● | — | Solo credenciales estáticas por inquilino |
| `publish` → AWS SQS | — | ● | — | Solo credenciales estáticas por inquilino |
| `publish` → Google Pub/Sub | — | ○ | — | **Creable pero no despachable** (consulta más abajo) |

A diferencia de Kafka, que tiene un conmutador `tls` real, la configuración del conector MQTT **no
tiene campos de TLS de ningún tipo**, y rechaza claves desconocidas, así que no hay nada que
configurar. El TLS solo ocurre de forma implícita, cuando das al bróker una URL `ssl://`, `tls://`,
`mqtts://` o `wss://`. La conexión se verifica entonces contra el almacén de confianza público y el
host que nombra la URL, sin ninguna manera de aportar una CA, un certificado de cliente o un ajuste
de verificación.

Los dos conectores de AWS exigen deliberadamente una clave de acceso estática y **no** recurren a la
identidad IAM ambiental del pod en el que se ejecutan. Esa separación existe para que la identidad de
nube de la propia plataforma nunca se tome prestada para hacer la llamada de un inquilino.

Cada destino de un conector se comprueba al establecer la conexión, y uno que resuelve a una
dirección privada o de metadatos de nube se rechaza salvo que un operador haya permitido
explícitamente esa dirección; consulta
[a dónde puede enviar un conector](../concepts/outbound-connectors.md#destinations).

:::warning Un conector de Google Pub/Sub se puede crear y nunca enviará nada
`gcp_pubsub` es un tipo de conector válido: la API lo acepta, y el conector se guarda y se publica
como cualquier otro. **No tiene implementación de entrega** en esta versión, así que todo despacho
hacia él falla de forma terminal y acaba en la cola de mensajes muertos: reconocido pero no
ejecutable, nunca descartado en silencio. Cuando se publique, se autenticará con una credencial
guardada en el conector, como hacen los conectores de AWS, nunca con la identidad del pod.
:::

Aparte de los conectores, los [canales de notificación](../guides/notification-channels.md) llegan a
personas y no a sistemas, por SMTP y webhook.

## No disponible

Se nombran explícitamente porque, desde fuera de la plataforma, «ausente de la lista de arriba» y
«se pidió y la respuesta fue no» son indistinguibles, y solo uno de los dos merece la espera.

| | |
| --- | --- |
| Ingesta de dispositivo por WebSocket | No disponible. Figura como planeada en la [introducción](../intro.md). |
| CoAP fuera de LwM2M | No disponible. CoAP llega a la plataforma a través de [LwM2M](../concepts/lwm2m.md) y no de otro modo. |
| NATS en bruto como transporte de dispositivo | No disponible. Una credencial de dispositivo autoriza una conexión MQTT, y no existe un cliente de dispositivo nativo de NATS. |
| Salida de comandos Sparkplug (`DCMD`) | No disponible, y deliberadamente fuera de alcance en lugar de pendiente; consulta [más arriba](#sparkplug-b). |
| Protocolos de bus de campo industrial: OPC-UA, Modbus, BACnet | No disponibles como transportes de plataforma, y **nada de lo que publica el proyecto los habla.** La forma admitida es una pasarela local que hable el bus de campo en la red de planta y reenvíe por MQTT o HTTP; la traducción de protocolo la aportas tú. El proyecto sí publica `dc-edge-agent`, que hace la *otra* mitad de ese trabajo: termina localmente la vía MQTT del dispositivo, almacena de forma duradera durante un corte de WAN y reenvía **solo por MQTT**. No habla ningún bus de campo. |
