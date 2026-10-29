---
sidebar_position: 10
title: Referencia de métricas
---

# Referencia de métricas

Cada servicio exporta métricas de Prometheus en su puerto de métricas, y la pila de monitoreo las
recoge sin configuración por servicio (consulta [Observabilidad y métricas](./observability.md)).
Esta página lista las métricas específicas de cada servicio que no se describen con una función en
otro lugar. Las que pertenecen a una función están en la página de esa función:
[contrapresión en la ingesta](./observability.md#ingest-backpressure),
[pasadas de mantenimiento](./observability.md#maintenance-passes), [cachés](./observability.md#kv-caches),
[el bucle de detección](./detection-engine.md#loop-phases) y los
[servicios de borde](./edge-services.md#what-to-watch).

Los nombres son `devicechain_`, después el nombre del servicio sin guiones, después el nombre de la
primera columna, de modo que `batch_refusals_total` en command-delivery es
`devicechain_commanddelivery_batch_refusals_total`. Un nombre entre llaves tras una métrica lista sus
etiquetas. Ninguna métrica de esta página está etiquetada por inquilino ni por dispositivo, así que
ninguna supone un riesgo de cardinalidad al recogerla. Los contadores terminan en `_total` y solo
suben, así que se leen como una tasa. Los indicadores (gauges) se leen tal cual.

Casi todo lo que sigue es diagnóstico: te permite saber qué hizo un servicio sin leer su log. Cuando
una métrica es de las que un operador debe atender, su fila dice qué hacer y, si la vigila una
alerta, se nombra.


## Entrega de comandos {#command-delivery}

Prefijo: `devicechain_commanddelivery_`.

Las tres [alertas de entrega de comandos](./observability.md#command-delivery-alerts) leen
`command_delivery_claims_stranded_total`, `command_delivery_presence_read_errors_total` y
`batch_refusals_total{bound="reserve"}`. El panel de Command Delivery representa los contadores de escritura de flota y los principales contadores de reclamación, retención y presencia; el resto de las series sirven para consultas ad hoc y para tus propios paneles. Es normal
que varias de ellas estén muy activas y no son fallos: una proporción estable de avisos rechazados
por `not_sole`, reclamaciones perdidas porque la pasada de barrido compitió con un aviso, y
retenciones colocadas mientras los dispositivos están fuera de línea.
`command_delivery_responses_refused_total` debería marcar cero. Una tasa creciente en
`command_delivery_stranded_observed_total` o `command_response_lost_unsettled_total` significa que
los comandos están caducando contra dispositivos que no hicieron nada mal.
(El `command_delivery_` duplicado en los nombres más antiguos es histórico y se mantiene, porque
renombrar una serie rompe todos los paneles que la leen.)

| Métrica | Significa |
| --- | --- |
| `batch_cancel_commands_total{disposition}` | Comandos examinados por una cancelación de lote, según lo que la cancelación pudo hacer con cada uno. Una proporción sostenida de already_sent es un freno que llega siempre tarde. |
| `batch_devices_total{disposition}` | Dispositivos a los que se resolvió un lote de comandos, según si el lote les encoló comandos. |
| `batch_enqueues_total{target_kind, outcome}` | Lotes de comandos decididos, según cómo se nombró el destino y el resultado. |
| `batch_refusals_total{code, bound}` | Rechazos de lote por dispositivo, por código y, en un rechazo por techo, por la cota que lo causó. bound=reserve significa que el dispositivo habría cabido en el techo propio del inquilino y lo rechazó solo la reserva de entrega. |
| `command_dead_letters_not_ours_total` | Mensajes no entregados leídos del flujo compartido que describen otro tipo de trabajo. Se confirman y se ignoran: todos los productores escriben en un único flujo. |
| `command_dead_letters_unreadable_total` | Mensajes no entregados sobre los que este consumidor no pudo actuar: sin inquilino interpretable, un cuerpo que no es un sobre, o un sobre que no nombra ningún comando. Se confirman y se cuentan en lugar de reintentarse, porque ninguna reentrega hace que un mensaje malformado se pueda interpretar. |
| `command_delivery_claims_lost_total{path}` | Despachos abandonados porque otro despachador reclamó antes el comando, por la vía de despacho que perdió. «sweep» es la pasada periódica, «nudge» es el despacho emitido cuando se encola un comando; que las dos compitan por una fila es normal y seguro (la reclamación es un compare-and-set), así que lee una tasa en una vía sin ninguna en la otra, no el total. |
| `command_delivery_claims_stranded_total` | Comandos que se quedaron leyéndose como SENT porque falló su publicación y también falló la liberación. En LwM2M el reconciliador de varados los rearma; en MQTT siguen caducando como TIMEOUT, culpando por error al dispositivo. |
| `command_delivery_dispatches_exhausted_total` | Comandos fallados porque la plataforma no pudo publicarlos a su dispositivo tantas veces como permite la cota configurada, así que dejó de reintentar. Cada fila registra FAILED con la plataforma como causa, en lugar de reintentar hasta su TTL y registrar después TIMEOUT contra un dispositivo al que nunca llegó. |
| `command_delivery_holds_placed_total` | Comandos retenidos del despacho porque el dispositivo está ausente de forma autoritativa. |
| `command_delivery_holds_released_total` | Comandos retenidos devueltos a la cola de despacho porque su dispositivo volvió. |
| `command_delivery_nudges_applied_total` | Avisos de despacho que encontraron exactamente un comando en cola y lo pasaron por las puertas de entrega. NO es un recuento de publicaciones: la puerta de presencia aún puede retener o fallar el comando, exactamente como en un ciclo de barrido. |
| `command_delivery_nudges_declined_total{reason}` | Avisos de despacho sobre los que el vaciado se negó a actuar, por motivo. Una tasa alta y estable de reason="not_sole" es normal (el aviso se retira siempre que un dispositivo tiene más de un comando en cola) y no es un fallo. |
| `command_delivery_nudges_dropped_total` | Avisos de despacho descartados porque la cola estaba llena. Una señal de LATENCIA, no una tasa de errores: el comando sale igualmente en el barrido de entrega, que es la red de seguridad bajo todo aviso. |
| `command_delivery_nudges_requested_total` | Avisos de despacho aceptados en la cola de despacho del momento de encolar, uno por cada comando creado mediante createCommand (un lote de flota no emite ninguno). |
| `command_delivery_presence_read_errors_total` | Pasadas de barrido que no pudieron leer la proyección de presencia; la puerta falla en ABIERTO, así que una tasa sostenida aquí significa que los comandos se están despachando sin comprobación. |
| `command_delivery_disabled_gate_errors_total` | Veces que no se pudo leer el registro de dispositivos deshabilitados para un lote de despacho (ese lote no se despacha) o no se pudo sincronizar con device-management. Una tasa sostenida significa que los comandos se retrasan, no que se pierden. |
| `command_delivery_disabled_gate_ready` | Medidor. `1` cuando este proceso ya listó los inquilinos y leyó los dispositivos deshabilitados de cada uno, `0` hasta entonces. Un inquilino cuyo registro nunca se llenó en ningún sitio no recibe despachos mientras vale `0`, así que un valor atascado en `0` con `command_delivery_disabled_gate_errors_total` en aumento es un reinicio durante una caída de user-management o device-management. |
| `command_delivery_disabled_gate_pending_tenants` | Medidor. Inquilinos cuyo registro de dispositivos deshabilitados este proceso aún no ha leído; sus comandos esperan. Un inquilino eliminado, o que ya no aparece en la lista, deja de contar. |
| `command_delivery_window_commands_undefined_total` | Comandos que esperaban una ventana de mantenimiento y fallaron, sin enviarse nunca, porque la versión del perfil en la que está ahora su tipo de dispositivo ya no los define. Cada uno termina en `FAILED` con resultado `NOT_DELIVERED`. |
| `command_delivery_claims_refused_disabled_total` | Despachos cuya reclamación se rechazó porque el dispositivo del comando se deshabilitó después de que el despacho leyera el registro de dispositivos deshabilitados. No se envía nada, el siguiente barrido retiene el comando y no se cuentan en `command_delivery_claims_lost_total`. |
| `command_delivery_responses_dead_lettered_total` | Respuestas de dispositivos a comandos escritas en el flujo de mensajes no entregados tras fallar todos los intentos de registrarlas, de modo que se pueda ver una respuesta que el dispositivo sí dio en lugar de dejar su comando pareciendo sin respuesta. |
| `command_delivery_responses_refused_total` | Respuestas de dispositivos rechazadas porque el dispositivo que las publica no es dueño del comando que nombran. Debería ser cero: o un dispositivo está respondiendo por otro, o el despacho dirigió un comando al dispositivo equivocado. |
| `command_delivery_stranded_observed_total` | Comandos encontrados en SENT sin resultado durante más tiempo del que la plataforma aún podía estar reintentándolos. |
| `command_response_lost_not_actionable_total` | Respuestas a comandos enviadas a mensajes no entregados que este consumidor dejó sin tocar porque su motivo dice que la plataforma declinó escribir la respuesta en lugar de intentarlo y fallar. Nada se resuelve: no hay un resultado perdido que registrar, y el comando nombrado aún puede estar vivo. Merece mirar una tasa creciente en un motivo que nadie esperaba, porque esta puerta está deliberadamente cerrada por defecto. |
| `command_response_lost_not_answerable_total` | Respuestas enviadas a mensajes no entregados cuyo comando no estaba en un estado que una respuesta pudiera resolver: ya había llegado a un resultado terminal por otra vía, o ha vuelto a estar vivo (redespachado o retenido) desde que se perdió la respuesta. En ninguno de los dos casos se escribe nada: un mensaje no entregado tardío no debe sobrescribir un resultado que realmente ocurrió, ni hacer fallar un comando que la plataforma aún pretende entregar. |
| `command_response_lost_settled_total` | Comandos llevados a un estado terminal porque la respuesta del dispositivo a ellos fue enviada a mensajes no entregados, de modo que un comando cuya respuesta la plataforma perdió deja de leerse como si siguiera en curso. |
| `command_response_lost_unsettled_total` | Respuestas enviadas a mensajes no entregados que agotaron todos los intentos de entrega sin que se escribiera la disposición de su comando. Esos comandos se leen como en curso hasta su TTL y luego caducan a TIMEOUT, culpando a un dispositivo que sí respondió. |

## Device management {#device-management}

Prefijo: `devicechain_devicemanagement_`.

`geofence_set_publish_failures_total` alimenta la alerta
[`GeoFenceSetPublishFailing`](./detection-engine.md#what-to-watch). Un aumento de
`credential_misconfigured_total` significa que se restauró una base de datos junto a una clave raíz
distinta de la que la selló, o que nunca se almacenó la credencial de un dispositivo; las
contraseñas incorrectas no se cuentan aquí. Los tres contadores de mensajes no entregados significan
que un cambio de estado llegó a la base de datos pero no al bus: léelos con `dcctl dead-letters`.

| Métrica | Significa |
| --- | --- |
| `alarm_event_dead_lettered_total` | Eventos de cambio de estado de alarma que no se pudieron publicar en el flujo de eventos de alarma y se escribieron en el flujo de mensajes no entregados. Cada uno es una transición de alarma que llegó a la base de datos pero no al bus, así que nadie recibió aviso de ella: queda visible para un operador y no solo registrada en el log. |
| `callout_in_flight` | Solicitudes de auth-callout de dispositivos que se están autorizando ahora mismo. |
| `callout_refused_busy_total` | Solicitudes de auth-callout de dispositivos rechazadas con la denegación genérica porque se alcanzó la cota de solicitudes en curso. |
| `config_reports_rejected_total{reason}` | Mensajes del [topic device-reports](../guides/connecting-a-device.md#reserved-device-topics) que device-management rechazó: `malformed` (no se pudo decodificar), `throttled` (un sync dentro del periodo de espera por dispositivo), `unattributable` (sin inquilino ni dispositivo en el topic), `unknown_device`, `unknown_revision`, `digest_mismatch`, `stale` (más antiguo que lo registrado) y `device_disabled` (el dispositivo está [deshabilitado](../guides/device-credentials.md#disabling-a-device)). Una tasa constante desde una flota es un fallo del lado del dispositivo. |
| `credential_misconfigured_total{path}` | Comprobaciones de credenciales de dispositivos rechazadas porque el secreto almacenado nunca puede coincidir: no hay ninguno almacenado, o su resumen se hizo con una clave distinta de la que deriva la clave raíz de esta instancia (lo más probable, una base de datos restaurada junto a la clave raíz equivocada). path="connect" es el auth callout de MQTT, path="event" la comprobación por evento. Las contraseñas incorrectas no se cuentan aquí. |
| `device_reports_dead_lettered_total` | Mensajes de dispositivo escritos en el flujo de mensajes fallidos porque no se pudieron entender o nombraban una revisión de configuración que el dispositivo no tiene. Léelos con `dcctl dead-letters`. |
| `geofence_set_publish_failures_total` | Manifiestos de conjuntos de geocercas que no se pudieron publicar: un error de serialización, un rechazo del broker o un fallo de transporte. Cada uno significa que no se avisó a event-processing de una edición de geocercas, así que la contención de ese inquilino conserva su conjunto de geocercas anterior hasta que una pasada de reconciliación lo repare. Una tasa sostenida distinta de cero significa que las ediciones de geocercas no están llegando al motor de detección. |
| `provisioning_in_flight` | Gauge. Autorregistros de dispositivo que esta réplica está procesando ahora, después de leer el cuerpo de la solicitud y antes de responder. Una solicitud que aún envía su cuerpo no se cuenta. Un valor fijo en `provisioning.maxInFlight` significa que las solicitudes nuevas se responden con `503`. |
| `provisioning_request_duration_seconds{result}` | Histograma. Tiempo en responder una solicitud de autorregistro, por resultado, desde que arranca el manejador (incluye un cuerpo lento). |
| `provisioning_requests_total{result}` | Solicitudes de autorregistro de dispositivo por resultado: `created`, `adopted`, `replayed`, `auth_failed` (todas las formas en que se puede rechazar la clave o el secreto de aprovisionamiento, contadas juntas a propósito), `invalid`, `read_timeout`, `rate_limited_source`, `rate_limited_tenant`, `busy`, `not_registered`, `not_earmarked`, `disabled`, `type_mismatch`, `token_collision`, `already_provisioned`, `limit_exceeded`, `unavailable`, `misconfigured`, `unsupported_type` y `error`. Todos los resultados existen desde el arranque. No hay etiqueta de tenant, porque antes de autenticarse una solicitud no tiene un tenant fiable. |
| `raise_alarm_dead_lettered_total` | Aristas de raise-alarm escritas en el flujo de mensajes no entregados tras fallar todos los intentos de aplicarlas, de modo que una alarma que debía haberse levantado o despejado queda visible en lugar de solo registrada en el log. |
| `resolve_device_disabled_total` | Eventos rechazados porque su dispositivo está [deshabilitado](../guides/device-credentials.md#disabling-a-device). Se descartan; no van a la cola de mensajes fallidos. |
| `resolve_event_time_bounded_total` | Horas de evento informadas rechazadas por adelantarse al reloj del servidor más de la tolerancia configurada, y sustituidas por la última hora que permite la tolerancia. |
| `resolve_not_gateway_child_total` | Eventos rechazados porque una [pasarela](./edge-services.md#gateway-identity) publicó por un dispositivo que no tiene asignado, que no existe, o porque la pasarela está deshabilitada. Cada uno se registra en el flujo `failed-events` con su carga (motivo `NotGatewayChild`), no se reintenta y se notifica a la pasarela en su topic de avisos. No lleva etiqueta de tenant ni de pasarela; la línea de registro, como máximo una por minuto por pasarela y dispositivo, los nombra. Un aumento suele significar que un dispositivo nunca se asignó a su pasarela, o se asignó a otra. |

## Event processing: detección {#event-processing-detect}

Prefijo: `devicechain_eventprocessing_`.

Las métricas que leen las alertas son `detect_is_leader`, `detect_live`, `detect_checkpoints_total`
(la alerta `DetectLeaderIsNotConsuming` lee las tres) y los contadores `detect_fence_*` que respaldan
las [alertas de geocercas](./detection-engine.md#what-to-watch). `detect_consumer_pending` es la
señal de retraso que vigila `DetectConsumerBacklogHigh`, y `detect_watermark_lag_seconds` es la que
hay detrás de `DetectWatermarkLagHigh`. `detect_loop_heartbeat_timestamp_seconds` es la propia señal
de vida del motor: si está obsoleta mientras `detect_is_leader` y `detect_live` marcan 1, el bucle
está bloqueado dentro de una llamada.

| Métrica | Significa |
| --- | --- |
| `detect_applied_stream_seq` | Mayor secuencia de flujo de JetStream capturada en la instantánea confirmada. |
| `detect_checkpoint_failures_total{stage}` | Puntos de control programados de DETECT que no se confirmaron, por etapa (publish, serialize, save). Una llamada de punto de control que supera su plazo cuenta contra la etapa en la que se quedó atascada. Un rechazo por escritor obsoleto no se cuenta aquí: eso termina el proceso. |
| `detect_checkpoint_save_seconds` | Tiempo que cada instantánea confirmada tardó en escribirse en la base de datos, como distribución: la segunda parte del coste de un punto de control, tras la serialización. |
| `detect_checkpoint_serialize_seconds` | Tiempo que cada instantánea confirmada tardó en serializar el estado del motor, como distribución: la primera parte del coste de un punto de control. |
| `detect_checkpoints_total` | Puntos de control de instantánea de DETECT confirmados. |
| `detect_consumer_ack_pending` | Mensajes entregados pero sin confirmar en el consumidor durable de eventos resueltos (trabajo en curso). |
| `detect_consumer_pending` | Mensajes sin entregar a la espera en el consumidor durable de eventos resueltos (la señal principal de retraso de DETECT). |
| `detect_derived_publish_ack_seconds` | Tiempo que cada evento derivado publicado esperó a que el broker lo confirmara, como distribución. Solo publicaciones correctas. No es la latencia de detección: una detección espera a su punto de control antes de publicarse. |
| `detect_derived_publish_pass_seconds` | Tiempo que cada punto de control dedicó a publicar sus detecciones, desde el primer envío hasta la última confirmación, como distribución. No incluye el tiempo que las detecciones esperaron a su punto de control. |
| `detect_derived_publish_outstanding` | Eventos derivados enviados que esperan ahora mismo la confirmación del broker (limitados por `derivedPublishing.maxInFlight`). Cero entre puntos de control. |
| `detect_derived_publish_outstanding_bytes` | Bytes de contenido de esos eventos (limitados por `derivedPublishing.maxBufferedBytes`, salvo que un único evento mayor se envía solo). |
| `detect_derived_publish_window_full_seconds_total` | Segundos que los puntos de control pasaron esperando una confirmación mientras otra detección estaba lista para enviarse pero el límite en vuelo o el presupuesto de bytes no tenían hueco. |
| `detect_derived_publish_lane_waits_total` | Detecciones que esperaron a que se confirmara una detección anterior del mismo dispositivo (o ancla de correlación) antes de poder enviarse, con `derivedPublishing.maxInFlight` por encima de 1. |
| `detect_derived_publish_failures_total{outcome}` | Publicaciones de eventos derivados sin confirmación positiva, por `outcome`: `unknown` (agotó el tiempo: el evento puede haberse guardado) o `failed` (el broker lo rechazó). Cada una aplaza su punto de control y se reintenta con el mismo evento e identidad, así que uno ya guardado no se guarda dos veces. |
| `detect_derived_publish_stale_outcomes_total` | Resultados de publicación rechazados porque no correspondían a ninguna publicación que el punto de control actual estuviera esperando. Siempre cero salvo que algo vaya mal. |
| `detect_derived_events_published_total` | Eventos de señal derivados publicados. |
| `detect_derived_events_rejected_total{reason}` | Detecciones descartadas antes de publicarse, por motivo (enumeración acotada). |
| `detect_events_applied_total` | Eventos resueltos alimentados al motor DETECT. |
| `detect_fact_persist_retries_total` | Reintentos de una escritura de proyección de hechos (reglas, censo, atributos, eliminaciones) que falló con un error no terminal. El hecho sigue sin confirmar y este consumidor queda bloqueado tras él hasta que un reintento se confirma. |
| `detect_fanout_events_total` | Eventos núcleo por regla producidos por la expansión de eventos resueltos. |
| `detect_fence_archive_skew_total` | Lecturas del archivo de geocercas que fallaron porque device-management no sirve las puertas de manifiestos: ejecuta una versión anterior a la entrega de manifiestos. Se repara solo cuando ese servicio avanza de versión. |
| `detect_fence_geometry_cache_evictions_total` | Entradas de la caché de geometrías compiladas descartadas para mantenerse dentro de la cota de vértices de la caché. |
| `detect_fence_geometry_cache_hits_total` | Consultas a la caché de geometrías compiladas servidas desde la caché, evitando tanto una lectura entre servicios como una recompilación. |
| `detect_fence_geometry_cache_misses_total` | Consultas a la caché de geometrías compiladas que tuvieron que obtener y compilar el documento. |
| `detect_fence_geometry_cache_vertices` | Total de vértices que contiene la caché de geometrías compiladas: la cantidad en la que se cuenta su cota. |
| `detect_fence_geometry_hash_mismatch_total` | Documentos de geometría de geocercas cuyo hash no coincidió con la dirección de contenido bajo la que se pidieron. Siempre es un defecto, nunca transitorio: el par sirvió la fila equivocada, algo recodificó el documento en tránsito, o el archivo está corrupto. |
| `detect_fence_geometry_unresolved_total` | Entradas de manifiesto de geocercas cuya geometría no se pudo obtener del archivo de device-management. Cada una deja esa geocerca informando que es irresoluble en lugar de responder, y la repara la siguiente pasada de reconciliación si la causa era transitoria. |
| `detect_idle_advances_total` | Avances por inactividad según el reloj de pared que produjeron al menos una detección. |
| `detect_idle_detections_total` | Detecciones producidas por avance por inactividad según el reloj de pared (ausencia/duración/sesión que se disparan por silencio). |
| `detect_is_leader` | 1 mientras esta réplica tiene el arrendamiento de la partición DETECT, desde que lo adquiere y no desde el final de la construcción del turno. |
| `detect_live` | 1 mientras esta réplica consume dentro de un turno de liderazgo que tiene; 0 mientras está en espera O mientras construye un turno que ya ha adquirido. |
| `detect_live_gap_fills_total{outcome}` | Veces que el consumo en vivo se encontró con un mensaje cuya secuencia de flujo superaba en más de uno la del motor y leyó del flujo el rango que faltaba, por resultado: filled (se leyó el rango y contenía mensajes), absent_only (el flujo no contenía nada de él: purgado o expulsado), failed (un intento de leer el rango no se completó; el consumo en vivo se detiene y el intento se repite en cada ciclo, así que una caída sostenida cuenta cada intento, no una sola vez). |
| `detect_live_gap_sequences_total{outcome}` | Secuencias de flujo dentro de los rellenos de huecos en vivo, por resultado: applied (un mensaje que el broker había contado como entregado pero que nunca llegó al bucle, ahora aplicado), absent (no está en el flujo: purgado o expulsado), skipped (se leyó de vuelta sin poder procesarse, y se registró como gestionado sin aplicar nada). Una tasa de applied distinta de cero significa que se están perdiendo entregas entre el broker y este bucle. |
| `detect_loop_heartbeat_timestamp_seconds` | Hora Unix de la última pasada del bucle de escritor único de DETECT, refrescada al menos una vez por ciclo mientras hay un turno activo. Obsoleta mientras detect_is_leader y detect_live marcan 1 significa que el bucle está bloqueado dentro de una llamada. |
| `detect_restore_seconds` | Tiempo para restaurar el estado del motor desde el almacén de instantáneas al arrancar. |
| `detect_rule_stat_write_failures_total` | Escrituras del contador de disparos de salud de reglas (la transacción de un inquilino) que fallaron o agotaron el tiempo y se descartaron, sin reintento. La detección no se ve afectada (los contadores se escriben en segundo plano), pero los recuentos de salud de reglas de la consola se quedan cortos mientras esto sube; los disparos perdidos están en `detect_rule_stat_updates_discarded_total` con el motivo `write_failed`, y la causa está en el registro del servicio. |
| `detect_rule_stat_write_seconds` | Tiempo que tardó cada escritura del contador de disparos de salud de reglas (la transacción de un inquilino, incluida la comprobación de eliminación de inquilino que hace primero) en el escritor en segundo plano, como distribución. No es tiempo del bucle de detección. Incluye las escrituras fallidas. |
| `detect_rule_stat_updates_admitted_total` | Detecciones publicadas añadidas a los contadores de disparos de salud de reglas, para escribirse en segundo plano. |
| `detect_rule_stat_updates_coalesced_total` | De ellas, las que se sumaron a una regla que ya esperaba escribirse, por lo que no cuestan una fila más. |
| `detect_rule_stat_updates_written_total` | Actualizaciones de disparos de salud de reglas confirmadas en la base de datos. |
| `detect_rule_stat_updates_discarded_total` | Actualizaciones de disparos de salud de reglas que nunca se escribirán, por `reason`: `full` (el presupuesto de pendientes estaba agotado), `overflow` (el recuento pendiente de una regla llegó a su tope), `closed` (llegó después de terminar el periodo de liderazgo del motor), `write_failed` (su escritura falló o agotó el tiempo), `purged` (su inquilino se eliminó antes), `shutdown` (seguía pendiente al terminar el periodo). La detección no se ve afectada. |
| `detect_rule_stat_admission_contended_total` | Adiciones a la salud de reglas que esperaron a que el escritor en segundo plano terminara un breve paso de contabilidad. Cada espera está acotada y nunca incluye una llamada a la base de datos. |
| `detect_rule_stat_pending_keys` | Reglas con actualizaciones de disparos aún sin escribir (acotadas por `ruleHealth.maxPendingKeys`). |
| `detect_rule_stat_pending_bytes` | Memoria contabilizada de las actualizaciones de disparos aún sin escribir (acotada por `ruleHealth.maxPendingBytes`). |
| `detect_rule_stat_oldest_pending_seconds` | Antigüedad de la actualización de disparo más antigua aún sin escribir: cuánto pueden ir retrasados los recuentos de la consola. |
| `detect_rule_stat_batch_rows` | Reglas en cada escritura de salud de reglas, como distribución. |
| `detect_rule_stat_batch_updates` | Actualizaciones de disparos en cada escritura de salud de reglas, como distribución: cuántas escrituras de una fila sustituye. |
| `detect_rules_active` | Reglas cargadas en el motor DETECT. |
| `detect_stale_absence_dropped_total` | **Activaciones** de ausencia descartadas al publicar porque el dispositivo salió del alcance de la regla: se eliminó, se deshabilitó, se cambió de tipo, o la versión de la regla fue reemplazada. También cuenta el cierre emparejado de una activación cuando esa activación se descartó en la misma pasada, y el cierre que una republicación retiene porque la nueva versión conserva la regla. El cierre de un dispositivo que salió del alcance no se descarta, así que la alarma que levantó se cierra. |
| `detect_superseded_frontier_dropped_total` | Detecciones de los tipos duración, sesión y agregado, que se disparan por el paso del tiempo, descartadas al publicar porque su versión de perfil ha sido reemplazada. |
| `detect_tenants_over_live_key_budget` | Inquilinos que superan actualmente el presupuesto de claves vivas por inquilino. |
| `detect_tenants_over_retained_sample_budget` | Inquilinos que superan actualmente el presupuesto de muestras retenidas por inquilino. |
| `detect_tenants_over_rule_budget` | Inquilinos que superan actualmente el presupuesto de número de reglas por inquilino. |
| `detect_watermark_lag_seconds` | Hora del reloj de pared menos la marca de agua del motor en el último punto de control. |

## Event processing: reacciones {#event-processing-react}

Prefijo: `devicechain_eventprocessing_`.

`react_events_poison_dropped_total` es la serie sobre la que se dispara la alerta
`ReactPoisonDropping`. `react_actions_dropped_total{reason="unknown_kind"}` debería ser siempre
cero: un valor distinto de cero es un defecto que investigar, no carga. `react_actions_not_enabled_total`
nunca debería mostrar `raiseAlarm` ni `clearAlarm`, porque ese sumidero siempre está conectado.

| Métrica | Significa |
| --- | --- |
| `react_actions_dispatched_total{action}` | Acciones REACT entregadas a su sumidero, por tipo de acción (incluye reproducciones idempotentes). |
| `react_actions_dropped_total{reason}` | Acciones REACT omitidas sin intentarse, por motivo. unknown_kind: la definición de la regla traía un tipo de acción que esta compilación no puede despachar (una definición falsificada o editada a mano; inalcanzable por la vía de autoría admitida), así que un valor distinto de cero es un defecto que investigar, no carga. |
| `react_actions_not_enabled_total{action}` | Acciones REACT reconocidas pero descartadas porque este despliegue no tiene sumidero para ellas, por tipo de acción: sendCommand sin command-delivery configurado, o httpCall/publish sin conectores de salida habilitados. El sumidero de alarmas siempre está conectado, así que raiseAlarm/clearAlarm nunca deberían aparecer aquí. |
| `react_connector_egress_shed_total{action}` | Intentos de despacho a conectores (`httpCall`, `publish`) descartados en origen por superar la tasa de salida del inquilino, por tipo de acción. Se cuenta por intento: una reentrega puede descartarse y luego admitir la misma acción, así que no es un recuento de acciones descartadas de forma permanente. |
| `react_connector_shed_dead_lettered_total{action}` | Acciones de conector (httpCall/publish) descartadas en origen y registradas como mensaje no entregado individual con motivo shed, por tipo de acción. |
| `react_derived_in_flight` | Eventos derivados que REACT ha tomado del flujo y aún no ha terminado: esperando al trabajador de su dispositivo o en despacho. Normalmente como mucho `react_workers`; más mientras hay entregas esperando más allá de su plazo de confirmación. |
| `react_dispatch_lag_seconds` | Cuánto retraso lleva REACT respecto del flujo de eventos derivados: la edad de cada evento, desde que el broker lo almacena hasta que empieza su despacho. Es el tiempo que la alarma, los comandos y las llamadas a conectores de una detección esperan a REACT. |
| `react_events_dead_lettered_total` | Eventos derivados escritos en el flujo de mensajes no entregados tras el tope de reentregas, para poder inspeccionar sus acciones en lugar de que desaparezcan. |
| `react_events_orphaned_total` | Eventos derivados cuya regla ya no estaba en la proyección (no se despachó nada). |
| `react_events_poison_dropped_total` | Eventos derivados descartados tras el tope de reentregas, porque su despacho seguía fallando. También se escriben en el flujo de mensajes no entregados, así que esto cuenta los mismos eventos que `react_events_dead_lettered_total`; se mantiene porque la alerta `ReactPoisonDropping` se dispara sobre él. |
| `react_workers` | Cuántos eventos derivados puede despachar REACT a la vez (`react.workers`). Los eventos de un mismo dispositivo se despachan uno tras otro, en el orden del flujo, salvo que un evento dejado para reentrega se despacha de nuevo después de los eventos que le siguen. |

## Event sources {#event-sources}

Prefijo: `devicechain_eventsources_`.

Las métricas de presencia pertenecen a la presencia MQTT afirmada por el broker que este servicio lee
del broker. `presence_tap_off` es la que conviene alertar: vale 1 cuando esa presencia no está en
marcha en una réplica, etiquetada con el motivo, y una flota MQTT de larga vida no emite avisos que
lo muestren. La pareja de canarios te dice si la presencia se está leyendo siquiera:
`presence_canary_missed_total` subiendo significa que no. `presence_reconcile_regressed_sessions`
por encima de cero de forma sostenida significa que las reparaciones no convergen. Los contadores
`total_msg_*` son por fuente.

| Métrica | Significa |
| --- | --- |
| `command_wake_dropped_total` | Despertares de comandos descartados porque la cola estaba llena; la pasada de reconciliación de command-delivery libera después sus comandos, así que es una señal de latencia y no una pérdida. |
| `command_wake_failed_total` | Despertares de comandos que no pudieron llegar a command-delivery; los cubre la pasada de reconciliación. |
| `command_wake_released_total` | Comandos retenidos devueltos a la cola de entrega porque su dispositivo se reconectó. |
| `command_wake_requested_total` | Dispositivos que regresan puestos en cola para un despertar de comandos. |
| `presence_advisories_skipped_total{reason}` | Avisos de conexión del broker que no produjeron ningún evento de presencia, por motivo. |
| `presence_canary_missed_total` | Sondas canario cuya propia conexión al broker NO se observó: la presencia no se está leyendo. |
| `presence_canary_observed_total` | Sondas canario cuya propia conexión al broker se observó de extremo a extremo. |
| `presence_events_failed_total` | Transiciones de presencia que no se pudieron escribir en el flujo de entrada. |
| `presence_events_refused_total` | Transiciones de presencia rechazadas por la puerta de admisión de ingesta (inquilino eliminado o techo del inquilino). |
| `presence_reconcile_regressed_sessions` | Dispositivos encontrados ACTIVOS con un id de sesión inferior al que mantiene la proyección, en la última pasada de reconciliación. Un valor sostenido distinto de cero significa que las reparaciones no convergen. |
| `presence_reconcile_repaired_total{direction}` | Reparaciones de presencia que la reconciliación emitió porque se perdió un aviso, y que el estado del dispositivo luego reflejó, por dirección. Una reparación se cuenta en la pasada siguiente a la que la emitió. |
| `presence_reconcile_unapplied_total{direction}` | Reparaciones de presencia que el estado del dispositivo aún no reflejaba en la siguiente pasada de reconciliación, por dirección. Se rechazaron o se perdieron, y se emiten de nuevo. Una tasa sostenida significa que las reparaciones no se aplican; una causa es un dispositivo eliminado mientras aún aparecía como conectado, cuya reparación se rechaza en cada pasada. |
| `presence_reconcile_runs_total{outcome}` | Pasadas de reconciliación de presencia, por resultado. |
| `presence_reconcile_withheld_disconnects_total` | Dispositivos que se habrían marcado fuera de línea si el inventario del broker hubiera sido demostrablemente completo. |
| `presence_released_total` | Dispositivos devueltos de presencia afirmada a presencia inferida porque esta fuente dejó de leer el broker. |
| `presence_sessions_regressed_total` | Transiciones de presencia observadas con un id de sesión inferior a la marca máxima de esta réplica (el reloj de un nodo del broker puede ir por detrás del de sus pares). Solo diagnóstico: que se aplique o no esa transición lo decide la proyección más adelante. |
| `presence_still_asserted` | Dispositivos que esta fuente aún tenía afirmados al comenzar la última pasada de liberación. El trabajo se vacía solo, así que un vaciado sano lleva esto a cero y lo mantiene ahí. |
| `total_http_connections_closed_before_request{source}` | Recuento de conexiones a un oyente HTTP de ingesta que se cerraron antes de entregar una solicitud: un cierre por tiempo de espera de cabeceras, pero igualmente un escaneo de puertos, una comprobación de salud TCP o un cliente que colgó. |
| `total_msg_decode_successful{source}` | Recuento total de mensajes decodificados correctamente. |
| `total_msg_failed_decode{source}` | Recuento total de mensajes que fallaron al decodificarse. |
| `total_msg_tenant_deleted{source}` | Recuento de mensajes entrantes rechazados porque su inquilino ha sido eliminado y sus datos se están recuperando. |

## Ingesta LwM2M {#lwm2m-ingest}

Prefijo: `devicechain_lwm2mingest_`.

Los indicadores de líder y de servicio se describen con los [servicios de borde](./edge-services.md#lwm2m-ingestion-metrics),
igual que las métricas que avisan. Las de abajo son el resto. Los contadores de comandos cuentan la
historia de un comando a través del adaptador: intentado, y después con éxito o fallado, con los
contadores de fuera de línea, aparcados y vaciados mostrando los comandos que esperaron a que un
dispositivo despertara. `command_park_errors_total` y `command_response_publish_failures_total` son
los dos que significan que un comando puede terminar como `TIMEOUT` aunque el dispositivo no tuviera
la culpa.

| Métrica | Significa |
| --- | --- |
| `bad_requests_total` | Solicitudes /rd malformadas (p. ej., una solicitud de elemento de registro sin ubicación). |
| `coap_requests_total{code}` | Solicitudes CoAP a nivel de transporte atendidas por la sonda de salud L0, por código de respuesta. Los resultados de registro en /rd se miden con los contadores de registros/actualizaciones/bajas/caducidades. |
| `command_drain_claim_errors_total` | Comandos acumulados NO despachados porque no se pudo establecer la propiedad con command-delivery (falla en cerrado; se reintenta enseguida, en orden). |
| `command_drain_claims_lost_total` | Comandos acumulados que otro despachador o el barrido de entrega reclamó antes, de modo que este vaciado no los despachó: una actuación duplicada evitada, no un fallo. |
| `command_drain_errors_total` | Lecturas de vaciado que fallaron; se reintentan tras una breve espera mientras el dispositivo sigue activo. |
| `command_park_errors_total` | Comandos no entregables que NO se pudieron devolver a command-delivery; se dejan sin confirmar para reintentarse en la reentrega, y quedan atascados en SENT hasta que uno tenga éxito. |
| `command_park_settled_total` | Devoluciones que no movieron ninguna fila porque el comando ya había sido respondido, cancelado, caducado o reclamado de nuevo: un resultado ya resuelto, no un fallo. |
| `command_park_skipped_total` | Comandos no entregables NO devueltos porque no hay ningún aparcador conectado o el sobre de entrega no llevaba nonce de despacho; siguen en SENT y esperan su TTL. |
| `command_response_publish_failures_total` | Resultados de comandos que no se pudieron publicar en command-responses tras los reintentos locales (la operación ya se ejecutó, así que el comando no se reentrega; caducará con TIMEOUT). |
| `commands_attempted_total{op}` | Comandos LwM2M despachados a un dispositivo activo (= con éxito + fallados), por operación. |
| `commands_drained_total` | Comandos acumulados (retenidos o aparcados) despachados a un dispositivo activo por un vaciado, el más antiguo primero. |
| `commands_poison_total` | Comandos descartados por no poder procesarse (sin inquilino interpretable en el subject, o un sobre que no se puede decodificar). |
| `commands_served_offline_total` | Comandos para un dispositivo que este adaptador sirve que no tenían conexión activa: aparcados en command-delivery y vaciados en el siguiente despertar del dispositivo, o EXPIRED en su TTL si nunca despierta. |
| `commands_succeeded_total{op}` | Comandos LwM2M que el dispositivo confirmó con un 2.xx, por operación. |
| `commands_tenant_deleted_total` | Comandos descartados con confirmación porque su inquilino ha sido eliminado y sus datos se están recuperando: la plataforma declinando actuar sobre el hardware de un cliente dado de baja. |
| `deregistrations_total` | Bajas explícitas de LwM2M (`DELETE /rd/{id}`). |
| `device_info_attribute_write_errors_total` | Atributos de información del dispositivo recopilados que no se pudieron escribir en device-management; se escriben de nuevo en la siguiente recopilación del dispositivo. |
| `device_info_attribute_writes_shed_total` | Lotes de atributos recopilados descartados porque el grupo de escritura de atributos estaba ocupado; se escriben en la siguiente recopilación del dispositivo. Una tasa constante indica que device-management tarda en responder. |
| `device_info_harvest_retries_total` | Recopilaciones de información del dispositivo reintentadas una vez porque el dispositivo no respondió. |
| `device_info_harvest_skipped_total` | Solicitudes de recopilación de información del dispositivo no ejecutadas: el dispositivo se recopiló en la última hora, la cola de su fragmento estaba llena, ya no era alcanzable, o está deshabilitado (o no se pudo comprobar si lo está). |
| `device_info_harvests_total{outcome}` | Lecturas de información del dispositivo ejecutadas cuando un dispositivo pasó a ser alcanzable, por resultado: `complete` (todos los recursos leídos), `partial` (faltan algunos), `empty` (respondió, pero no dio ninguno), `unanswered` (no respondió). |
| `devices_registered_total` | Dispositivos creados automáticamente en su primer registro LwM2M. |
| `handshakes_total` | Handshakes DTLS completados (cada uno es una nueva sesión LwM2M autenticada). |
| `measurements_emitted_total` | Muestras de medición individuales escritas de forma duradera desde LwM2M Observe/Notify. |
| `notifies_received_total` | Mensajes LwM2M Notify recibidos sobre una instancia de objeto observada. |
| `observe_terminal_notifications_total` | Notificaciones que terminaron una observación (RFC 7641, p. ej. 4.04 tras eliminarse la instancia observada). |
| `presence_dropped_total` | Transiciones de presencia descartadas: un dispositivo no registrado con una credencial sin registro automático, o un presupuesto de emisión duradera agotado. |
| `registrations_disabled_refused_total` | Registros rechazados (`4.03`) y registros terminados en la siguiente actualización (`4.04`) porque un operador ha deshabilitado el dispositivo. |
| `sessions_rejected_total` | Sesiones nuevas rechazadas porque se alcanzó el techo de sesiones activas (maxSessions). |
| `telemetry_devices_registered_total` | Dispositivos creados automáticamente en una primera muestra de telemetría (poco frecuente: los dispositivos LwM2M se crean en el registro /rd). |
| `telemetry_tenant_deleted_dropped_total` | Muestras de telemetría descartadas porque el inquilino ha sido eliminado y sus datos se están recuperando. |
| `telemetry_unknown_dropped_total` | Muestras de telemetría descartadas por un dispositivo no registrado (registro automático desactivado para la credencial). |

## Ingesta Sparkplug {#sparkplug-ingest}

Prefijo: `devicechain_sparkplugingest_`.

El resto de las métricas de Sparkplug, incluido el indicador de líder, se describen con los
[servicios de borde](./edge-services.md#sparkplug-ingestion-metrics). `samples_skipped_total` existe
porque un nodo que publica solo valores sin ninguna muestra numérica, de otro modo, se ve igual que
uno inactivo.

| Métrica | Significa |
| --- | --- |
| `devices_registered_total` | Dispositivos registrados automáticamente en el primer avistamiento de su identidad Sparkplug. |
| `samples_skipped_total{reason}` | Métricas de mensajes Sparkplug aceptados que no produjeron ninguna muestra, por motivo: non_numeric (un valor booleano, de texto, bytes, dataset o plantilla), null (is_null), unnamed (sin nombre y sin alias resoluble), untyped (un entero cuyo tipo de dato no declararon ni el mensaje ni su nacimiento y cuyo valor se lee distinto con y sin signo, por lo que no se adivina; el servicio registra una advertencia limitada en frecuencia con el tema y la métrica) u oversize (el mensaje traía más de 16.384 métricas, así que no se almacenó ninguna de sus lecturas; el servicio registra una advertencia con el tema). Un nodo que publica solo esas métricas, de otro modo, se ve igual que uno inactivo. |

## Notification management {#notification-management}

Prefijo: `devicechain_notificationmanagement_`.

Las notificaciones que fallan todos los intentos de entrega se escriben en el flujo de mensajes no
entregados, donde `dcctl dead-letters` las lista. Este contador te dice que nunca se enviaron avisos.

| Métrica | Significa |
| --- | --- |
| `notifications_dead_lettered_total` | Alarmas escritas en el flujo de mensajes no entregados tras fallar todos los intentos de entrega, para que un operador pueda ver qué avisos nunca se enviaron. |
| `quiet_hours_total{outcome}` | Lo que hicieron las horas de silencio con una notificación, según el resultado: held (retenida en lugar de enviada), released (enviada al terminar el periodo de silencio), dropped_resolved (descartada porque la alarma fue reconocida o cerrada), dropped_policy (descartada porque la política se deshabilitó o se eliminó), dropped_exhausted (descartada y registrada como mensaje fallido tras 5 envíos que no llegaron a ningún canal) o superseded (descartada porque la política avisó de un evento más reciente de la misma alarma). |

## User management {#user-management}

Prefijo: `devicechain_usermanagement_`.

Estos cuentan el consumidor de mensajes no entregados, que guarda un fallo del que un servicio se rindió en un almacén consultable para que sobreviva a la propia ventana de siete días del flujo. `dead_letters_unstored_total` es la que importa: un fallo que el almacén no pudo aceptar no queda registrado en ningún lugar, y `DeadLetterStoreLosing` se dispara sobre ella.

| Métrica | Significa |
| --- | --- |
| `dead_letters_stored_total` | Mensajes no entregados escritos en el almacén consultable, de modo que un fallo del que un consumidor se rindió sobrevive a la propia ventana de siete días del flujo. |
| `dead_letters_unstorable_total` | Mensajes no entregados que este consumidor no pudo interpretar: sin inquilino interpretable, o un cuerpo que no es un sobre. Se CONFIRMAN y se cuentan en lugar de reintentarse, porque ninguna reentrega hace que un mensaje malformado se pueda interpretar. |
| `dead_letters_unstored_total` | Mensajes no entregados que agotaron todos los intentos de entrega sin almacenarse: el almacén estuvo inalcanzable durante más tiempo del que duran los reintentos. El fallo que describían ya no queda registrado en ningún lugar, que es justo lo que este consumidor existe para evitar. |

Estas cuentan el coordinador de eliminación de inquilinos, que borra los datos de un inquilino eliminado de todos los sistemas de almacenamiento y después libera su identificador. Ninguna lleva etiqueta de inquilino. Consulta [Eliminación de inquilinos](./tenant-deletion.md#blocked-store) para saber cómo leerlas.

| Métrica | Significa |
| --- | --- |
| `tenant_purge_in_flight` | Eliminaciones de inquilino abiertas en este momento. |
| `tenant_purge_oldest_age_seconds` | Antigüedad de la eliminación abierta más antigua, desde su corte; 0 si no hay ninguna. `TenantPurgeStalled` la vigila. |
| `tenant_purge_store_blocked{store,reason}` | Eliminaciones abiertas que retiene cada sistema de almacenamiento, por almacén (`rdb`, `tsdb`, `broker`, `kv`, `detect`, `blob`, `other`) y motivo (`deferred`: aún hay datos o siguen llegando; `error`: no se pudo consultar al almacén). `TenantPurgeStoreFailing` y `TenantPurgeStoreHolding` la vigilan. |
| `tenant_purge_store_rows_erased_total{store}` | Filas, objetos, mensajes o claves que ha borrado cada almacén en eliminaciones de inquilino: la señal de avance de un almacén bloqueado. |
| `tenant_purge_store_erase_duration_seconds{store}` | Histograma de lo que tardó una llamada de borrado de un almacén. |
| `tenant_purge_completed_total` | Eliminaciones de inquilino completadas: todos los almacenes limpios, asentados y retenidos, barreras levantadas e identificador liberado. |
| `tenant_purge_erased_total` | Declaraciones de borrado de inquilino establecidas: todos los almacenes limpios y asentados, antes de que termine la retención del identificador. Una declaración retirada porque un almacén volvió a informar datos, y restablecida después, se cuenta de nuevo. |
| `tenant_purge_failures_total` | Veces que el coordinador no pudo llevar la eliminación de un inquilino a término en una pasada. Reintenta en la siguiente, así que cuenta intentos. `TenantPurgeCoordinatorFailing` la vigila. |
| `tenant_purge_duration_seconds` | Histograma de segundos desde la eliminación de un inquilino hasta la liberación de su identificador. Incluye la retención del identificador. |
| `tenant_purge_clean_duration_seconds` | Histograma de segundos desde la eliminación de un inquilino hasta que el último almacén informó de que estaba limpio. Excluye las esperas de asentamiento y retención posteriores: lo que tardó el borrado en sí. |

Estas métricas cuentan la [autenticación en dos pasos](../guides/two-factor-authentication.md). La primera y la última son las que vigilan las alertas `MfaVerificationFailuresSustained` y `MfaIdentityLocked`. Ninguna lleva una etiqueta de cuenta ni de inquilino.

| Métrica | Significado |
| --- | --- |
| `mfa_verifications_total{method,result}` | Códigos comprobados, por método (`totp`, `recovery`) y resultado: `ok`, `invalid` (incorrecto), `replay` (un código correcto ya usado), `locked` (rechazado por un bloqueo), `expired` (un desafío que no se pudo usar), `budget` (una petición con más comprobaciones de las permitidas), `rate_limited` y `unavailable` (no se pudo hacer la comprobación). |
| `mfa_challenges_total` | Inicios de sesión con contraseña retenidos a la espera de un código. |
| `mfa_enrollments_total{result}` | Eventos de configuración: `begun`, `confirmed`, `disabled` por la persona, `reset` por un operador. |
| `mfa_identity_locks_total` | Intentos que activaron un bloqueo global de una cuenta. Cualquier aumento significa que alguien está usando la contraseña de una cuenta y falla su segundo factor. La vigila `MfaIdentityLocked`. |

## Update management {#update-management}

Prefijo: `devicechain_updatemanagement_`. El servicio se ejecuta en los perfiles `default` y `full`; consulta [Actualizaciones over-the-air](../concepts/over-the-air-updates.md).

Si sube `update_download_grants_total{result="not_configured"}`, no hay una base de descarga definida y ningún dispositivo puede descargar: `updates.downloadBaseUrl` se puso como cadena vacía, o la instancia no tiene ingress del que derivarla. `update_attempts_expired_total` cuenta los dispositivos que callaron durante una actualización; uno en `UNKNOWN` espera el inventario del dispositivo o un `abandonUnknownUpdate` de un operador. `UpdateDownloadsNotConfigured`, `UpdateReportsMostlyRefused`, `UpdateRolloutStepsFailing` y `UpdateAttemptsGoingUnknown` los vigilan; consulta las [alertas de actualizaciones por aire](./observability.md#update-alerts).

| Métrica | Significado |
| --- | --- |
| `update_attempts_expired_total{state}` | Intentos de actualización que el barrido de plazos movió porque el dispositivo calló: `TIMED_OUT` antes de que se informara del inicio de la instalación, `UNKNOWN` durante la instalación o el reinicio. |
| `update_desired_publish_failures_total` | Asignaciones de actualización, concesiones de descarga y cancelaciones que no se pudieron poner en el topic desired de un dispositivo. El dispositivo recoge el estado en su siguiente sync. |
| `update_download_bytes_total` | Bytes de artefactos enviados a los dispositivos. |
| `update_download_grants_total{result}` | Concesiones de descarga pedidas por dispositivos, por resultado: `granted`, o el motivo de no concederla (`unknown_assignment`, `superseded`, `finished`, `cancel_requested`, `artifact_unavailable`, `not_configured`, `error`). |
| `update_downloads_total{result}` | Peticiones de descarga de artefactos, por resultado: `served`, `aborted` (el cliente se fue o el objeto no se pudo leer a mitad), o el motivo del rechazo antes de servir ningún byte (`capability_malformed`, `capability_invalid`, `capability_expired`, `stale`, `gone`, `range_not_satisfiable`, `busy`, `device_disabled`, `error`). Una tasa creciente de `capability_invalid` es alguien adivinando. |
| `update_reports_rejected_total{reason}` | Mensajes de dispositivo que update-management rechazó antes de aplicarlos: `malformed`, `unattributable`, `throttled` y `device_disabled` (el dispositivo está deshabilitado). |
| `update_rollout_dispatches_total{result}` | Dispositivos sobre los que decidió un despliegue: `dispatched` (se les asignó la actualización, o se contaron con una asignación del mismo artefacto que ya tenían) o `skipped` (rechazados por las reglas de asignación: no existe, deshabilitado, incompatible, ocupado con otra actualización). |
| `update_rollout_step_failures_total{stage}` | Fallos del controlador de despliegues que quedaron para la siguiente pasada: `step` (falló el paso de un despliegue: un error de base de datos, un documento de perfiles compatibles ilegible en el artefacto, o todas las consultas de dispositivos fallaron porque device-management es inalcanzable o no está configurado para update-management) o `list` (falló la consulta de los despliegues en curso, así que no se avanzó ninguno). Un aumento constante significa que los despliegues no avanzan. |
| `update_rollout_transitions_total{to}` | Despliegues que el servicio movió por sí solo: `completed`, `paused_failure_threshold` o `paused_artifact_not_assignable` (el artefacto se retiró o perdió la firma exigida). |
| `update_reports_total{verdict,kind}` | Informes de progreso e inventario de actualizaciones, por `kind` (`progress`, `inventory`) y por el veredicto de la máquina de estados: `APPLIED`, o el motivo de no aplicarlo (`DUPLICATE`, `STALE_ATTEMPT`, `DIGEST_MISMATCH`, `WRONG_COMPONENT`, `REGRESSION`, `UNCONFIRMED`, `NOT_CANCELLABLE`, `LATE`, `CONFLICT`, `INVALID_ATTEMPT`, `WRONG_KIND`, `UNKNOWN_ASSIGNMENT`). Una tasa creciente de no aplicados desde una flota es un fallo del lado del dispositivo; júzgala con `kind="progress"`, porque un informe de inventario de un dispositivo sin nada asignado recibe `UNKNOWN_ASSIGNMENT` o `UNCONFIRMED` por diseño. |

## Todos los servicios {#every-service}

Prefijo: `devicechain_<area>_`, donde `<area>` es el nombre del servicio sin guiones (`devicechain_devicemanagement_ready`). Todos los servicios exportan estas.

`safety_gate_enabled` es lo que lee la alerta [`SafetyGateDisabled`](./observability.md#safety-gates).
Los indicadores de JetStream informan del tamaño de cada flujo frente a sus límites configurados y de
la salud de sus réplicas; las alertas de replicación de [Replicación](./observability.md#replication)
se construyen sobre esta última.

| Métrica | Significa |
| --- | --- |
| `auth_gate_attempts_total` | Intentos de obtención de JWKS de la puerta de autenticación en segundo plano. |
| `auth_gate_failures_total` | Fallos de obtención de JWKS de la puerta de autenticación en segundo plano. |
| `ready` | 1 cuando el plano de datos está listo (autenticación activa), 0 en caso contrario. |
| `safety_gate_enabled{gate}` | 1 cuando una puerta de seguridad opcional está conectada en este servicio, 0 cuando está APAGADA porque falta su configuración. Una puerta APAGADA deja pasar trabajo que la puerta existe para rechazar (tenant_lifecycle: trabajo de un inquilino eliminado; presence: comandos a dispositivos ausentes; rule_validation: reglas de detección que no compilan al publicar el perfil). |
| `jetstream_peers_current{stream}` | Pares RAFT actualmente al día y en línea para este flujo o bucket KV, incluido el líder. Por debajo de jetstream_replicas_actual significa que el flujo está etiquetado como replicado pero no lo está. |
| `jetstream_replicas_actual{stream}` | Número de réplicas que JetStream informa para este flujo o bucket KV. |
| `jetstream_stream_limit_bytes{stream}` | Techo MaxBytes configurado para un flujo de JetStream. |
| `jetstream_stream_limit_messages{stream}` | Techo MaxMsgs configurado para un flujo de JetStream. |
| `jetstream_stream_used_bytes{stream}` | Bytes actuales almacenados en disco en un flujo de JetStream. |
| `jetstream_stream_used_messages{stream}` | Número actual de mensajes en un flujo de JetStream. |
