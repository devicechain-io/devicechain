---
sidebar_position: 5.5
title: Paneles de Grafana
---

# Paneles de Grafana {#grafana-boards}

La [pila de monitoreo](./observability.md#la-pila-de-monitoreo) que despliega `dcctl install`
incluye ocho paneles de Grafana por instancia: un **resumen** que responde a "¿está todo en
marcha?" y siete **paneles por etapa**, uno por cada etapa que atraviesa un mensaje desde un
dispositivo hasta una notificación entregada, más la plataforma que los sostiene. Cada panel es
el punto de partida para una pregunta, y cada uno enlaza con el siguiente que abriría.

Todo lo que muestran los paneles se construye con las métricas que los servicios ya exportan
(consulte la [referencia de métricas](./metrics-reference.md)) y con las series estándar de
Kubernetes, de la base de datos y del broker de mensajes que recoge la pila de monitoreo. No hay
que activar nada para tenerlas.

## Dónde encontrar los paneles {#finding-the-boards}

[Abra Grafana](./observability.md#acceder-a-grafana) y busque la carpeta
`devicechain-<instancia>`. Cada instancia tiene su propia carpeta y su propia copia de cada panel,
con el título `DeviceChain — <nombre> [<instancia>]`. Un panel solo lee los datos de su propia
instancia: no hay selector de instancia, y los paneles de una instancia no pueden mostrar los de
otra.

Todos los paneles tienen en la parte superior un menú **DeviceChain boards** que lista los paneles
de esta instancia, y el resumen termina con una fila de enlaces a los paneles por etapa. Lo único
que se elige es el **origen de datos**, que es el Prometheus instalado por la pila.

Todos los paneles empiezan con la misma tabla **Firing alerts**. Lista las alertas que se están
disparando ahora mismo para esta instancia y que pertenecen a la etapa del panel, de modo que un
panel por etapa está en silencio cuando su etapa está bien. Una tabla vacía significa que no se
dispara ninguna. La tabla lee las reglas de Prometheus de la instancia, así que sigue vacía si esas
reglas no están instaladas. El resumen lista todas las alertas, por lo que una alerta que ningún
panel por etapa recoge sigue siendo visible allí.

## Cómo leer un panel {#reading-a-panel}

Cada panel tiene una descripción: pase el cursor sobre la pequeña `i` de su esquina. Indica qué
cuenta el panel, cómo es un valor normal y qué hacer ante uno anómalo. Las convenciones siguientes
valen para todos los paneles.

- **Los indicadores se colorean según lo que significan.** Verde es sano, amarillo significa
  "mire pronto" y rojo significa "actúe". Un indicador cuyo nombre dice "over the range" o
  "selected range" cuenta lo ocurrido en el intervalo de tiempo elegido, así que amplíe el
  intervalo para mirar más atrás; uno sin esa indicación muestra el valor actual.
- **Un indicador que debe mantenerse en cero muestra `0`, no "No data".** Cuando un recuento no
  tiene nada que contar, el panel sustituye cero a propósito. Un panel que muestra "No data" le
  está diciendo que la serie aún no se ha reportado, lo cual es distinto de que no haya pasado
  nada.

### Paneles de retraso acumulado y qué muestran cuando un lector está caído {#backlog-panels}

Un **retraso acumulado** (backlog) es el número de mensajes de un flujo que aún no se han
entregado a un lector. Es la señal de alerta temprana más útil de la plataforma, porque un lector
más lento que sus productores se retrasa mucho antes de que algo falle. Consulte
[Un consumidor que se queda atrás](./observability.md#consumer-backlog) para saber qué significa
el retraso y cómo se mide.

Cada servicio lector reporta su propio retraso, pero un servicio detenido no puede reportar nada.
Un lector que se cayó, se está reiniciando o se escaló a cero es justo cuando más se quiere el
número, y un panel que se queda en blanco en ese momento no sirve. Por eso los paneles de retraso
tienen **dos fuentes, por orden**:

1. **El reporte del propio lector**, mientras el servicio lector está en marcha.
2. **El recuento del propio broker de mensajes** para ese mismo lector, tomado de las métricas del
   broker, cuando el lector no está reportando.

Un panel que puede recurrir a esta alternativa lo dice en su descripción (*"While the reading
service is down this shows the message broker's own count"*). Ambas fuentes cuentan los mismos
mensajes, así que el panel mantiene una línea continua cuando una releva a la otra.

Diecinueve paneles tienen la alternativa del broker:

- **Messaging:** *Unread backlog per reader*, *Handed out, not yet acknowledged* y
  *Share of stream capacity left unread*.
- **Ingest:** *Backlog against its limit*, *Backlog and stream fill over time*, *Arriving vs
  backlog growth* (su línea de crecimiento del retraso) y *Capture backlog in messages* (su
  primera línea).
- **Event processing:** *Consumer lag (undelivered)*, *In-flight (delivered-unacked)*,
  *Consumer backlog*, *Derived-event backlog* y *Consumer unread backlog (share of stream
  capacity)*.
- **Delivery:** *Waiting to be sent*, *Dispatch backlog*, *Notification backlog* y
  *Update report backlog*.
- **Persistence:** *Events waiting to be stored* y *Updates waiting to be applied*.
- **Overview:** el indicador *Largest backlog share*.

*Messages waiting to be redelivered*, en Messaging, es siempre el recuento del propio broker, así
que no tiene una fuente del lector a la que recurrir.

Conviene conocer dos límites:

- **La alternativa necesita las métricas del broker.** La pila de monitoreo recoge las métricas del
  broker, así que en una instalación por defecto la alternativa funciona. Si usa su propio
  Prometheus sin recoger las del broker, un panel de retraso se queda en blanco cuando su lector se
  detiene, como antes.
- **Los contadores de pérdida no tienen alternativa.** Los indicadores y líneas que cuentan los
  mensajes que el flujo *descartó antes de que el lector los leyera* los cuenta el propio lector:
  en Ingest, *Messages lost before being read, over the range* y *…, any consumer* y la segunda
  línea de *Capture backlog in messages* (la brecha sin leer); en Messaging, *Messages lost unread
  (range)* y *Messages lost unread /s*; y *Messages lost* del resumen. Mientras el lector está
  caído, estos indicadores marcan 0, y eso no demuestra que no se haya perdido nada. Los mensajes
  descartados durante la interrupción se cuentan solo cuando el lector vuelve, como cota inferior
  y con la hora de su regreso. Durante una interrupción, juzgue el riesgo con *Share of stream
  capacity left unread*, que sigue funcionando: al 100 % el flujo empieza a descartar mensajes que
  el lector no ha visto. Consulte [Mensajes que un consumidor nunca leyó](./observability.md#unread-loss).

Una serie ausente significa "no medido", nunca "nada esperando". Si un panel de retraso está vacío, ninguna de las dos fuentes ha reportado: compruebe que el
broker se está recolectando (Messaging, *Broker servers up*) y que existe el durable del lector.

## El resumen {#overview-board}

<!-- screenshots: G13 -->

**Úselo para responder de un vistazo a "¿está todo en marcha y se está perdiendo algo?".** Empiece
aquí y siga el enlace al panel por etapa de lo que no esté en verde.

| Grupo | Qué muestra |
| --- | --- |
| Is everything up? | Indicadores de áreas no listas, destinos de recolección caídos, réplicas ausentes, reinicios de contenedores, mensajes perdidos y la mayor cuota de retraso entre todos los lectores. |
| How much is flowing | Eventos a través del pipeline por etapa y trabajo que no tuvo éxito, uno junto al otro para leer una caída frente a la otra. |
| Failures that should stay at zero | Mensajes rechazados en la entrada, mensajes fallidos, comandos abandonados o que no se pueden entregar, y eventos de reglas alimentados frente a acciones de reglas enviadas. Cualquier indicador distinto de cero merece una mirada. |
| Replicas and restarts | Réplicas disponibles por deployment, reinicios de contenedores por pod y los dos indicadores de áreas que se describen a continuación. |
| Stage boards | Enlaces a los siete paneles por etapa. |

Alertas que lo respaldan: todas. Las más importantes para este panel son
[`InstanceAreaUnavailable`](./observability.md#container-restarts) e
[`InstanceContainerRestarted`](./observability.md#container-restarts).

### Áreas caídas y áreas escaladas a 0 {#scaled-to-zero}

Un área cuyos pods han desaparecido todos deja de reportar nada propio: su disponibilidad, sus
métricas, su destino de recolección. Los indicadores *Areas not ready* y *Scrape targets down*
pueden entonces marcar cero mientras el área no está en marcha en absoluto, porque una serie
ausente no es un cero. Dos indicadores leen en cambio los deployments de Kubernetes, que siguen
diciendo qué se quiere y qué hay:

| Indicador | Cuenta | Color | Alerta |
| --- | --- | --- | --- |
| *Areas with no pod available* | Áreas y consola que **quieren** al menos un pod y no tienen ninguno disponible. | Rojo desde 1. | `InstanceAreaUnavailable`, tras diez minutos. |
| *Areas scaled to 0* | Áreas y consola configuradas para ejecutar **cero** pods. | Azul desde 1. | Ninguna. |

La diferencia es la intención. Un área en cero se redujo a mano
(`kubectl scale … --replicas=0`), por ejemplo en pausa por mantenimiento, así que se muestra como
un estado propio y no como una caída, y nunca genera alertas. Los dos indicadores nunca cuentan la misma
área. Si un área aparece en *Areas scaled to 0* y usted no la escaló, vuelva a escalarla: no está
sirviendo, y nada le avisará de ello. Los indicadores cuentan las áreas y la consola, que también
es un deployment de la instancia; el pod de depuración del broker de mensajes queda fuera de ambos,
y de la alerta, porque no es un área.

Un área que el perfil de la instancia no ejecuta no tiene ningún deployment, así que no aparece en
ninguno de los dos indicadores.

## Ingest {#ingest-board}

<!-- screenshots: G13 -->

**Úselo cuando los eventos no llegan, se rechazan o la ruta MQTT está bajo presión.** Cubre la
puerta de entrada: lo que aceptan los transportes, lo que rechazan y el retraso de mensajes MQTT
a la espera de ser reenviados.

| Grupo | Qué muestra |
| --- | --- |
| Backlog of MQTT messages waiting to be forwarded | El retraso del flujo de captura frente a su límite, su ocupación, los mensajes perdidos antes de ser leídos y los cambios de líder. Sus paneles de retraso tienen la [alternativa del broker](#backlog-panels), salvo la línea de brecha sin leer de *Capture backlog in messages*. Por encima del 10 % y subiendo, añada réplicas de `event-sources`; al 100 % se descartan los mensajes sin leer más antiguos. |
| Refused or lost at the door | Mensajes rechazados para proteger datos sin leer, retenidos por límites de tasa, rechazados por mal formados, rechazados por un tenant eliminado o por una credencial en el cuerpo MQTT, y publicaciones externas fallidas. |
| Protection of the MQTT message store | Consumidores requeridos presentes, ajustes del almacén restituidos, flujos de pasarela sin límite de tamaño y conexiones rechazadas porque el almacén está lleno u ocupado. |
| Device presence and external sources | Cambios de conexión y desconexión no registrados, fuentes MQTT externas sin exactamente un lector y la caché de inicio de sesión de dispositivos. |
| Device self-registration | Dispositivos registrados, rechazos por motivo y tiempo de respuesta. |
| Sparkplug ingest, LwM2M ingest | Solo cuando se ejecutan esos servicios opcionales: lectores activos o estado de servicio, fallos y tráfico por protocolo. |

Alertas que lo respaldan: las alertas de MQTT y del flujo de captura descritas en
[Contrapresión en la ruta de ingesta](./observability.md#ingest-backpressure) y
[Mensajes que un consumidor nunca leyó](./observability.md#unread-loss), las de fuentes externas en
[Una fuente MQTT externa que nadie lee](./observability.md#external-mqtt-owner), las de registro en
[Autorregistro de dispositivos](./observability.md#self-registration-alerts) y las de caché de
credenciales y limitador de tasa, y las de ingesta Sparkplug y LwM2M.

## Messaging {#messaging-board}

<!-- screenshots: G13 -->

**Úselo cuando algo va lento o acumula retraso y aún no sabe dónde.** El broker de mensajes
conecta todas las etapas, así que este panel muestra la infraestructura compartida: los
servidores del broker, los flujos que guardan, quién los lee y cuánto se retrasa, y si las copias
están sincronizadas.

| Grupo | Qué muestra |
| --- | --- |
| Message broker servers | Servidores en marcha, quién lidera el clúster, clientes descartados por lentos, fallos de solicitudes, disco, CPU, memoria y conexiones. |
| Streams | Flujos por encima del 80 % de ocupación, flujos que rechazan eventos nuevos, ocupación por tamaño y por número de mensajes, y los flujos mayores en disco. |
| Readers of streams | Retraso sin leer por lector, mensajes entregados y sin confirmar, cuota de capacidad del flujo sin leer, mensajes perdidos sin leer y mensajes retenidos más allá de su confirmación. La [alternativa de retraso](#backlog-panels) se aplica a los tres primeros. |
| Publishing and backpressure | Eventos rechazados por contrapresión, tiempo de publicación y la proporción de tiempo que los publicadores esperan al broker. |
| Copies and leaders | Flujos con menos copias de las configuradas o con copias desactualizadas, cambios de líder de flujo y qué servidores lideraron el almacén de la pasarela MQTT. |
| Failed deliveries and dead letters | Mensajes que agotaron sus intentos de entrega, dead letters guardados, no guardados o perdidos, y conexiones con el broker que murieron en silencio. |
| Key-value buckets and caches | Lecturas de caché atendidas desde memoria, memoria usada, expulsiones, fallos y tiempo de operación. |

Alertas que lo respaldan: las alertas [`JetStream*`](./observability.md#consumer-backlog),
[`BrokerConnectionDiedSilently`](./observability.md#broker-connection-dead),
[`ReaderHeldMessagePastAckWait`](./observability.md#held-past-ack-wait),
[`MaxDeliveryRecordsWaiting`](./observability.md#max-delivery-records), 
[`ReplayCoveredDeliveriesExhausted`](./observability.md#ingest-backpressure),
[`BrokerPresenceLosingTransitions`](./observability.md#event-resolution), las alertas de
[replicación](./observability.md#replication) y las de dead letters.

## Persistence and state {#persistence-board}

<!-- screenshots: G13 -->

**Úselo cuando los eventos se aceptan pero no aparecen, o el estado vivo de un dispositivo está
desactualizado.** Sigue un evento hasta el almacén de eventos y el estado vivo del dispositivo, y
después examina las bases de datos que hay debajo.

| Grupo | Qué muestra |
| --- | --- |
| Event storage | Eventos almacenados por segundo, eventos no almacenados, reintentos, escrituras en curso, tiempo de almacenamiento de un evento, eventos por transacción y eventos a la espera de ser almacenados (con la [alternativa de retraso](#backlog-panels)). |
| Live device state | El mismo conjunto para la proyección del estado de dispositivo: actualizaciones por segundo, perdidas, reintentadas, tiempo por actualización y actualizaciones a la espera de aplicarse (con la alternativa). |
| Background jobs in the event store | Tiempo desde la última reparación del historial, reconstrucciones de métricas calculadas, y tiempo y colisiones de la actualización de agregados. |
| Databases | Estado del servidor y del clúster, réplicas en streaming, conexiones, transacción abierta más larga, commits y rollbacks, tamaño, retraso de réplicas y qué pod es el primario. |
| Write-ahead log and backups | Segmentos de log a la espera de enviarse, fallos de envío, tiempo desde el último segmento enviado y antigüedad de la copia base más reciente. |
| Disk space | El disco de base de datos y el de copias más llenos, el menor tiempo hasta que se llene el disco del almacén de eventos y las tablas de historial mayores. |
| Tenant removal | Eliminaciones en curso, la eliminación abierta más antigua, finalizaciones y fallos, y los almacenes que retienen una eliminación. |

La base de datos relacional y el almacén de objetos de copias de seguridad viven en un namespace
compartido por todas las instancias del clúster. Esos paneles lo indican en sus descripciones y
muestran la base de datos compartida, no solo la de esta instancia.

Alertas que lo respaldan: [`DeviceStateDroppingEvents`](./observability.md#live-state-projection),
las alertas de [archivado de copias](./observability.md#backup-archiving) y del
[plano de control de la base de datos](./observability.md#database-control-plane), las alertas de
volumen `Database*`, `BackupDestination*` y `TenantPurge*`.

## Event processing {#event-processing-board}

<!-- screenshots: G13 -->

**Úselo cuando las reglas no se disparan, las alarmas llegan tarde o las reacciones no se
ejecutan.** Vigila el motor de detección y reacción descrito en
[Procesamiento de eventos](../concepts/event-processing.md).

| Grupo | Qué muestra |
| --- | --- |
| Pipeline health | Retraso del consumidor, mensajes en vuelo, retraso de marca de agua, reglas activas, eventos aplicados y checkpoints por segundo. El retraso del consumidor, los mensajes en vuelo y los paneles de retraso del consumidor tienen la [alternativa de retraso](#backlog-panels). |
| DETECT engine | Rendimiento de fan-out, detecciones rechazadas antes de publicar, errores de evaluación y las compuertas de seguridad de descarte de versión. |
| REACT dispatcher | Acciones despachadas, fallos de despacho, retraso de eventos derivados (con la alternativa), concurrencia y retraso de despacho (p50 y p99). |
| State budget | Entradas de estado con clave vivas, y los tenants que superan su presupuesto de reglas, de claves vivas o de muestras retenidas. |
| Snapshot and restore | Coste de checkpoint, tamaño de snapshot y coste de restauración al arrancar. |
| JetStream streams | Ocupación del flujo y proporción sin leer de la capacidad del flujo para el consumidor. |
| Geofence archive | Geometría de geocercas sin resolver o inconsistente, y la tasa de aciertos, ocupación y expulsiones de la caché de geometría. |
| Rule health and derived publish | Actualizaciones de salud de reglas descartadas, retraso y latencia de escritura, y la proporción de tiempo dedicada a publicar eventos derivados. |

Alertas que lo respaldan: las alertas `Detect*` y `React*`, `GeoFenceSetPublishFailing` y
`DeviceFactPublishFailing`. Consulte [Cómo operar el motor de detección](./detection-engine.md)
para saber cómo se ejecuta y se escala el consumidor del motor.

## Command delivery {#command-delivery-board}

<!-- screenshots: G13 -->

**Úselo cuando los comandos no llegan a los dispositivos o se rechazan escrituras masivas.**

| Grupo | Qué muestra |
| --- | --- |
| Fleet writes | Lotes creados, repetidos y rechazados por segundo, dispositivos aceptados, excluidos de un lote parcial, y rechazos por código y por límite (techo o reserva). |
| Dispatch | Reclamaciones varadas, reclamaciones perdidas y comandos no entregables. |
| Holds | Retenciones colocadas y liberadas, y errores de lectura de presencia. |

Alertas que lo respaldan: todas las alertas `Command*`, descritas en
[Entrega de comandos](./observability.md#command-delivery-alerts).

## Delivery {#delivery-board}

<!-- screenshots: G13 -->

**Úselo cuando ocurrió una alarma y nadie fue avisado, o cuando un conector o una actualización
está atascado.** Es el último tramo, desde la reacción de una regla hasta una persona o un sistema
externo. Los grupos de conectores y de actualizaciones de software aparecen para los servicios
opcionales que los ejecutan.

| Grupo | Qué muestra |
| --- | --- |
| Outbound connectors | Enviados, reintentando, abandonados, destinos bloqueados y despachos perdidos, por resultado, tipo de conector y motivo de fallo, más el retraso de despacho. |
| Hand-off from automation rules | Acciones entregadas a cada destino, acciones que no se pudieron ejecutar, acciones retenidas por superar la cuota y resultados de reglas apartados tras reintentos. |
| Alarm notifications | Gestionadas, reintentando y nunca entregadas, rechazos, retenciones por horas de silencio, pasadas de escalado, tiempo de gestión y el retraso de notificaciones. |
| Software updates | Informes de dispositivos por veredicto, descargas y concesiones por resultado, avance del despliegue, intentos que dejaron de responder y el retraso de informes de actualización. |

Los cuatro paneles de retraso usan la [alternativa del broker](#backlog-panels).

Alertas que lo respaldan: `ConnectorDispatchRateLimited`, `ReactConnectorEgressShedding` y
`ReactShedLettersOverBudget`. Los grupos de notificaciones y de actualizaciones de software no
tienen alerta propia; léalos en este panel.

## Platform {#platform-board}

<!-- screenshots: G13 -->

**Úselo cuando un servicio parece poco sano y necesita saber si es el servicio o el nodo que tiene
debajo.** Es el único panel que trata de los procesos y no del flujo de mensajes.

| Grupo | Qué muestra |
| --- | --- |
| Service health | Réplicas ausentes, pods no listos, reinicios de contenedores y contenedores detenidos por última vez por falta de memoria, con las réplicas disponibles frente a las deseadas. |
| CPU and memory | Memoria frente a su límite, CPU frente a su solicitud y limitación de CPU. |
| Service runtime | Goroutines, heap, memoria residente, ejecuciones y pausas del recolector de basura, y manejadores de archivo abiertos frente al límite. |
| Sign-in | Comprobaciones de credenciales por resultado, inicios de sesión limitados, rechazos de inicio de sesión único, fallos al obtener datos del proveedor de identidad y obtenciones de claves de token. |
| Tenant deletion | La misma vista de eliminación que en Persistence, desde el lado de la plataforma. |
| Limits and safety gates | Compuertas de seguridad desactivadas, tenants medidos con el valor por defecto de la plataforma, asignación compartida en uso, acciones salientes descartadas, y los techos alcanzados por el tráfico de dispositivos y por las llamadas salientes. |
| AI inference | Llamadas de inferencia por resultado y tokens de modelo usados. |

Alertas que lo respaldan: [`InstanceContainerRestarted`](./observability.md#container-restarts),
las alertas `Sso*` y de almacén de credenciales, `TenantPurge*`,
[`SafetyGateDisabled`](./observability.md#safety-gates) y
[`TenantsMeteredAtPlatformDefault`](./observability.md#tenant-ceilings), `RateLimiterOverflowInUse`,
`RateMeteringClockFallback`, `ReactShedLettersOverBudget` y `ConnectorDispatchRateLimited`.

## Lo que los paneles no cubren {#not-covered}

- **El operador de Kubernetes** se ejecuta en su propio namespace. Nada recolecta sus métricas y
  ningún panel lo muestra, ni siquiera los paneles de réplicas, que solo leen el namespace de esta
  instancia.
- **La tasa de solicitudes HTTP y GraphQL, los errores y la latencia** por servicio no se exportan,
  así que ningún panel los grafica.
- **La antigüedad de un evento de extremo a extremo**, desde la ingesta hasta su almacenamiento o
  detección, no se mide. Los paneles de retraso son un sustituto: un retraso que crece es una
  demora que crece. La medida real más cercana de antigüedad es `detect_watermark_lag_seconds`,
  que Event processing muestra como *Watermark lag*: cuánto se retrasa la marca de agua del motor
  de detección respecto al reloj.
- **Los agentes de borde** se ejecutan en hardware del cliente y no se recolectan.

## Relacionado {#related}

- [Observabilidad y métricas](./observability.md): qué expone cada servicio y la entrada de
  runbook de cada alerta mencionada arriba.
- [Referencia de métricas](./metrics-reference.md): todas las series por servicio.
- [Ajustar una instancia](./tuning-an-instance.md): qué cambiar cuando un panel muestra que se
  alcanza un límite.
