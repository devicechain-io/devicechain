---
sidebar_position: 5
title: Observabilidad y métricas
---

# Observabilidad y métricas

DeviceChain se distribuye con observabilidad integrada, no añadida después: cada servicio
está instrumentado con **métricas de Prometheus** y sondas de salud estándar de Kubernetes, y
`dcctl install` despliega una pila completa de **Prometheus + Grafana + Alertmanager**
en el clúster, para todas sus instancias, de modo que una instalación nueva se puede observar desde su primer minuto,
sin necesidad de armar un proyecto de monitoreo aparte.

:::note Estado
La pila de monitoreo (kube-prometheus-stack a través de `dcctl install`) y sus paneles de Grafana
están implementados y validados de extremo a extremo: un resumen de la instancia y siete paneles por etapa,
descritos en [Paneles de Grafana](./grafana-boards.md), con reglas de alerta incluidas. El trazado
distribuido OTLP es una mejora planeada a futuro.
:::

## Lo que expone cada servicio

Cada servicio de área funcional se instrumenta a sí mismo con métricas de cliente de Prometheus y
sirve las dos sondas estándar de Kubernetes:

- **`/healthz`** — vitalidad (liveness): ¿puede el proceso seguir haciendo su trabajo, o necesita un
  reinicio? Falla una vez que la conexión del servicio con el broker de mensajería se ha cerrado de
  forma definitiva, de modo que Kubernetes reinicia el pod. Un bucle que lee mensajes de un flujo
  también termina el proceso cuando sus lecturas siguen fallando durante dos minutos sin ninguna
  respuesta del broker de mensajería entre medias, y Kubernetes lo reinicia después. Hasta entonces
  reintenta con una pausa creciente de hasta cinco segundos. Un bucle sobre un flujo tranquilo
  comprueba cada pocos segundos que el broker sigue respondiendo, y una reconexión al broker también
  cuenta como respuesta, así que dos fallos separados por minutos cuentan como dos y no como uno
  largo, y una caída larga del broker no reinicia el servicio cuando el broker vuelve. Un broker
  que simplemente no es alcanzable no cuenta como lecturas fallidas: las lecturas lo esperan, el
  servicio sigue reconectando y continúa cuando el broker vuelve. Consulte
  [Un contenedor que se reinició](#container-restarts).
- **`/readyz`** — disponibilidad (readiness): ¿está listo para recibir tráfico? Un servicio que no está
  listo se mantiene fuera de rotación por su Service de Kubernetes (consulte
  [Despliegue y operador](./kubernetes-operator.md)).

Debido a que cada pod habla las mismas convenciones, la pila de monitoreo recolecta métricas de
toda la instancia de manera uniforme; no hay trabajo de integración por servicio.

### Resultados de los mensajes {#message-results}

La mayoría de los servicios que consumen un flujo cuentan cada mensaje que terminan de tratar en un
contador llamado `devicechain_<area>_<loop>_messages_total`, por ejemplo
`devicechain_eventmanagement_persist_messages_total`. Su etiqueta `result` indica qué fue del
mensaje:

| Resultado | Qué fue del mensaje |
| --- | --- |
| `ok` | Se trató. |
| `invalid` | Se descartó al llegar: no se pudo leer, no llevaba inquilino o se rechazó por lo que contiene. |
| `failed` | Se abandonó y se registró, como carta muerta o en el flujo `failed-events`. |
| `dropped` | Se abandonó tras su última entrega y deliberadamente no se registró, porque lo que habría actualizado se puede reconstruir. |
| `retry` | Su tratamiento falló por una causa que puede desaparecer, así que se dejó para que se entregue de nuevo. |
| `erased` | Su inquilino se ha eliminado y la base de datos del servicio rechazó la escritura. Se liquidó en la misma entrega en que llegó y no se registró en ningún sitio. Consulte [Eliminación de inquilinos](./tenant-deletion.md#database-writes). |

Todos los resultados se exportan a cero desde que arranca el servicio, así que una alerta sobre el
aumento de uno de ellos ve el primer mensaje contado, no solo el segundo. La única excepción es un
mensaje contado antes de que Prometheus haya leído el pod por primera vez: la serie se ve por primera
vez en 1, lo que no es un aumento.

Cada servicio puede servir además perfiles del runtime de Go en un listener propio. Está
desactivado por defecto; consulta [Perfilar un servicio](#profiling).

## Registros {#logs}

Cada servicio escribe registros JSON estructurados en stderr, un objeto por línea. Cada línea
lleva la `instance` y el `area` de los que procede, y `tenant` cuando el pod atiende a un solo
tenant, de modo que un pipeline de registros puede filtrar por ellos sin analizar los mensajes.

### El nivel de registro

Cuánto registra un servicio se fija una sola vez para toda la instancia, con
`infrastructure.logging.level` en la configuración de la instancia. Acepta exactamente uno de
estos valores, en minúsculas:

| Nivel | Lo que se obtiene |
| --- | --- |
| `trace` | Todo, incluidos los diagnósticos más detallados. |
| `debug` | Líneas de diagnóstico, algunas escritas una vez por mensaje en la ruta de ingesta. |
| `info` | **El valor por defecto.** Arranque, apagado, configuración y eventos relevantes. |
| `warn` | Solo advertencias y errores. |
| `error` | Solo errores. |

Cualquier otro valor, incluidos `INFO`, un número o un valor con un espacio, se rechaza: el
servicio no arranca, y su registro nombra la clave y los valores aceptados. No existe ningún
nivel que desactive el registro de errores.

`debug` y `trace` sirven para diagnosticar un problema, no para operar. En la ruta de ingesta
escriben una línea por cada mensaje de dispositivo, así que a ritmos de producción multiplican
el volumen que tiene que transportar su pipeline de registros.

Cada servicio arranca en `info` y cambia al nivel configurado en cuanto ha leído la
configuración de la instancia, al principio del arranque. Registra una línea que indica a qué
nivel ha cambiado, escrita antes del cambio para que aparezca incluso con `warn` o `error`.

El nivel forma parte de la configuración montada, no es algo que un servicio en ejecución
recargue. Cambiarlo cambia la suma de comprobación de la configuración, y los pods se
reinician con el nuevo valor.

**Quién puede cambiarlo depende de cómo se instaló la instancia:**

- **Instalada con `dcctl bootstrap`:** la instancia se ejecuta en `info`, y `dcctl` aún no
  tiene ninguna opción para cambiar el nivel. No edite a mano el Secret de configuración para
  sortearlo: `dcctl` escribe ese Secret por sí mismo y lo reemplaza en su siguiente ejecución,
  y una edición hecha fuera de `dcctl` tampoco reinicia los pods.
- **Instalada directamente desde el chart de Helm:** fíjelo junto con sus demás valores, por
  ejemplo `--set instance.config.infrastructure.logging.level=debug`.
- **Instalación del chart con `instance.existingSecret`:** añada `logging.level` bajo
  `infrastructure` en el documento que usted proporciona, y actualice
  `instance.existingSecretChecksum` con la suma de comprobación del nuevo documento. Esa suma
  es lo que reinicia los pods; sin ella, el nuevo nivel no se aplica.

:::note La salida de depuración estaba activada por defecto
Antes de que el nivel de registro fuera configurable, todos los servicios registraban en
`debug` lo pidiera alguien o no. Si compara registros de una instancia actualizada a través de
ese cambio, las líneas por mensaje de la ruta de ingesta, las confirmaciones de lectura y
escritura del broker y diagnósticos similares ya no aparecen en el nivel por defecto. No se ha
perdido nada: fije `debug` para volver a verlos.
:::

### Líneas que se escriben en todos los niveles {#audit-lines}

Un tipo de línea ignora el nivel: el registro que escribe el servicio `mcp` por cada llamada de herramienta de IA. No lleva campo `level` y se escribe también con `warn` y `error`, porque bajar la verbosidad no es una decisión de dejar de auditar. Selecciónalo por su campo `audit`; su formato se describe en [Qué se registra](../concepts/mcp.md#audit).

### Lo que nunca se registra

El documento de configuración propio de un servicio nunca se escribe en el registro, en ningún
nivel. En su lugar, el servicio registra un hash corto del documento (`config_sha256`, los
primeros 16 caracteres hexadecimales de su SHA-256). Para comprobar qué configuración está
ejecutando un pod, calcule el hash de la entrada de ese servicio en el ConfigMap de
configuración renderizado y compare ambos.

### Mensajes de la base de datos

La actividad de la base de datos se registra con el mismo registrador que todo lo demás:
líneas JSON con los campos `instance` y `area` del servicio, filtradas por el
`infrastructure.logging.level` configurado. Una sentencia que falla se registra en `error`
(`database statement failed`, con el mensaje de la base de datos en `error`), y una que tarda
más de 200 ms en `warn` (`slow database statement`). Cada línea lleva la sentencia (`sql`),
las filas afectadas (`rows`), su duración en milisegundos (`elapsed_ms`) y el código que la
emitió (`caller`). Una consulta que no encuentra filas no ha fallado, así que nunca se registra
como un fallo; solo se registra si es lenta, o con `sqlDebug` activado.

El registro de todas las sentencias es un interruptor aparte, por servicio: `sqlDebug` en la
configuración del almacén de datos de un servicio. Sus líneas se escriben en `info`, así que un
nivel `warn` o `error` las oculta.

El campo `sql` muestra los marcadores de una sentencia (`$1`, `$2`, …), nunca los valores
asociados a ellos, en cualquier nivel y con `sqlDebug` activado. El mensaje de error de la
propia base de datos se registra tal cual, y algunos citan el valor que rechazaron (por
ejemplo, `invalid input syntax for type uuid: "…"`).

## La pila de monitoreo

[`dcctl install`](./bootstrap.md#install) aprovisiona el monitoreo como uno de sus módulos
de OpenTofu integrados, **activado por defecto** (`--no-monitoring` lo omite, y también
`--compact`): la misma capa que aprovisiona la base de datos relacional, cert-manager e
ingress también levanta
[kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack)
(Prometheus, Grafana y Alertmanager). Se instala una vez por clúster, y observa a todas las
instancias arrancadas en ese clúster:

- **Recolección entre espacios de nombres (cross-namespace)** — Prometheus se ejecuta en su propio espacio de nombres y recolecta métricas de
  los servicios de cada instancia a través de los espacios de nombres, de modo que una sola pila observa todo el
  despliegue.
- **Los paneles se distribuyen con la plataforma** — los paneles de Grafana viven en el chart de Helm
  (`deploy/helm/devicechain/dashboards/`) y son importados automáticamente por el sidecar de
  paneles de Grafana. Un panel nuevo es un cambio de chart, no una importación manual.
- **Una carpeta por instancia** — cada instancia tiene su propia carpeta de Grafana,
  `devicechain-<instancia>`, con su propia copia de cada panel. Cada copia está acotada a
  esa instancia y lleva su id en el título; no hay selector de instancia que ajustar. Los
  archivos de `dashboards/` son plantillas que el chart genera por instancia, no paneles
  para importar a mano. La carpeta proviene de una anotación
  `grafana_folder: devicechain-<instancia>` en cada ConfigMap de panel, y solo un sidecar
  configurado para leerla organiza los paneles en carpetas: el sidecar debe ejecutarse con
  `FOLDER_ANNOTATION=grafana_folder` y su proveedor de paneles debe tener
  `foldersFromFilesStructure: true`. La pila que despliega `dcctl install` fija ambos. Si
  instaló con `--no-monitoring` y apunta su propio sidecar de Grafana a estos ConfigMaps
  sin ellos, la anotación se ignora y los paneles de todas las instancias quedan planos en
  un mismo lugar: siguen siendo paneles separados, cada uno acotado a su propia instancia,
  solo que sin carpetas.
- **Las instancias de un clúster mantienen sus paneles separados** — una vez que todas las
  instancias de un clúster usan un chart con carpetas por instancia, eliminar o actualizar
  una deja intactos los paneles de las demás. Hasta entonces, las instancias que aún usan un
  chart anterior comparten un panel por cada tablero fuera de cualquier carpeta de
  instancia, y actualizar cualquier instancia elimina ese archivo compartido: el panel de
  una instancia anterior falta hasta que el sidecar de paneles de Grafana vuelve a
  explorar. Un clúster cuya pila de monitoreo es anterior a esta organización muestra los
  paneles de todas las instancias fuera de sus carpetas hasta que se vuelva a ejecutar
  `dcctl install`.
- **Los enlaces antiguos dejan de funcionar** — los antiguos ids compartidos de los paneles
  (`dc-event-processing-ops`, `dc-command-delivery-ops`) no los usa ninguna instancia
  actualizada, así que los marcadores que apuntan a ellos dejan de funcionar una vez que
  todas las instancias se actualizan.
- **Destruir una instancia deja una carpeta vacía** — sus paneles se eliminan, pero su
  carpeta `devicechain-<instancia>`, ya vacía, permanece en Grafana; elimínela a mano.

### Actualizar la pila

Volver a ejecutar `dcctl install` actualiza la pila de monitoreo a la versión que fija este lanzamiento. Helm nunca
actualiza las definiciones de recursos personalizados de un chart, así que la pila
ejecuta primero el Job de actualización de CRD del propio chart: un Job de corta duración en el
espacio de nombres `monitoring` aplica del lado del servidor las CRD de Prometheus Operator antes de
que arranque el nuevo operador, y no hay nada que aplicar a mano. El Job descarga dos imágenes
(`busybox` y `registry.k8s.io/kubectl`); un clúster sin ruta a esos registros debe replicarlas antes.

Pasar de la pila de versiones anteriores a esta cruza una versión mayor de Prometheus (de 2 a 3) y
una de Grafana (de 11 a 13). Prometheus, Alertmanager y Grafana se reinician una vez; el inicio de
sesión de administrador de Grafana, su Service `ClusterIP` y las carpetas de paneles por instancia no
cambian. Prometheus conserva sus métricas en un clúster con volumen persistente y arranca vacío en un
clúster local, que las guarda solo en el disco del nodo.

## Iniciar sesión en Grafana

Grafana usa su propio **inicio de sesión de administrador**. Grafana no está integrado con el
inicio de sesión de DeviceChain: ni una contraseña de DeviceChain ni un proveedor de identidad
conectado (consulta [Inicio de sesión único](../concepts/single-sign-on.md)) te permiten
iniciar sesión en él.

Las métricas son a nivel de instancia y entre inquilinos, por lo que Grafana es una superficie
de *operador*, no algo a lo que los usuarios de inquilinos puedan acceder. Los inquilinos ven
sus propios datos a través de la consola y los paneles, nunca a través de Grafana.

### Acceder a Grafana

La pila que despliega `dcctl install` no publica ninguna ruta de ingress para Grafana; su
Service es `ClusterIP`. Haga un port-forward y abra `http://localhost:3000/`:

```bash
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80
```

### La contraseña de administrador

Inicie sesión como `admin`. La contraseña la genera `dcctl install` y se guarda en el Secret
`dc-grafana-admin` del espacio de nombres `monitoring`, clave `admin-password`:

```bash
kubectl -n monitoring get secret dc-grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d
```

El informe de la instalación imprime estos mismos dos comandos. Grafana se ejecuta **una vez
por clúster**, así que este es el inicio de sesión *del clúster*, compartido por todas las
instancias que hay en él, no una contraseña por instancia. Rotarla deja fuera a los operadores
de todas las instancias de ese clúster, no solo a los de la suya.

### Rotarla

Volver a ejecutar `dcctl install` **conserva** la contraseña: el Secret se vuelve a leer y se
reutiliza, y solo se genera una nueva cuando el Secret no existe. Editar el Secret a mano
tampoco la rota: Grafana lee la contraseña del Secret como variable de entorno al arrancar,
un Secret modificado no reinicia nada, y Grafana sigue aceptando la contraseña con la que
arrancó mientras el Secret nombra una que nunca ha visto. Para rotarla a propósito, ejecute
los tres pasos:

```bash
kubectl -n monitoring delete secret dc-grafana-admin
dcctl install <los flags con los que se instaló el clúster>   # un Secret ausente se genera de nuevo
kubectl -n monitoring rollout restart deployment/kube-prometheus-stack-grafana
```

:::caution
No omita el reinicio. Tras los dos primeros pasos el Secret contiene una contraseña nueva y
Grafana sigue aceptando la anterior, así que la rotación parece hecha y no lo está, y la
siguiente persona que lea el Secret no podrá iniciar sesión. El reinicio es lo que la aplica,
y funciona porque el Grafana desplegado no conserva una base de datos persistente.
:::

## El panel de operaciones de event-processing

El motor DETECT/REACT (consulte [Procesamiento de eventos](../concepts/event-processing.md))
es el componente que un operador más necesita vigilar, y se distribuye con un panel de Grafana
dedicado y alertas. El motor emite métricas orientadas al operador, incluido
un **indicador de retraso del consumidor (consumer-lag gauge)**: cuánto se ha retrasado la detección respecto al
flujo de eventos resueltos, y **recuentos de disparo de reglas**, de modo que "¿el motor de alarmas está al día, y qué
está haciendo?" se puede responder de un vistazo. El panel se describe, junto con el resto del conjunto,
en [Paneles de Grafana](./grafana-boards.md#event-processing-board).

## Mensajes que un consumidor nunca leyó {#unread-loss}

Cada flujo (stream) de JetStream tiene un límite. Cuando un flujo está lleno descarta sus mensajes
**más antiguos** para hacer sitio, de modo que la ingesta sigue funcionando. Un consumidor que aún no
había leído un mensaje cuando se descartó no lo leerá nunca. Los dos flujos de ingesta cuya pérdida
serían datos de dispositivos rechazan eventos nuevos antes de que eso le ocurra al consumidor que no
puede perderlos (consulte [Contrapresión en la ruta de ingesta](#ingest-backpressure)). Todos los
demás flujos, y todos los demás consumidores, se cubren aquí. El broker no informa de esta pérdida,
así que cada servicio la mide para cada consumidor duradero que lee, y estas alertas vigilan el
resultado:

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamDurableUnreadNearFull` | warning | Un consumidor lleva 5 minutos sin haber leído más del 80% de lo que cabe en su flujo. Todavía no se ha perdido nada. Los mensajes que ya leyó no cuentan, así que un flujo lleno de historial ya procesado no la dispara. Cuando la cola sin leer llega al límite, el flujo descarta los mensajes más antiguos y este consumidor nunca procesará los que no había alcanzado. Para el consumidor de detección de `event-processing` mide el consumidor, no el punto de control desde el que la detección vuelve a leer; el punto de control nunca va por detrás de lo que el consumidor ha confirmado, así que el número es al menos lo que la detección podría perder. | Averigüe por qué el consumidor va lento: los registros de su servicio, su base de datos, `JetStreamDurableFallingBehind`. Si el tráfico ha superado el flujo, aumente su límite y, con él, el volumen de JetStream. Los dos consumidores que frenan la ingesta los cubre `JetStreamUnreadBacklogNearFull` (consulte [Contrapresión en la ruta de ingesta](#ingest-backpressure)). |
| `JetStreamCaptureBacklogWillOverflow` | critical | El stream de captura, que guarda los eventos de los dispositivos MQTT hasta que `event-sources` los reenvía, se llenará en menos de diez minutos al ritmo al que creció su atraso sin reenviar en los últimos tres, y ese atraso es al menos el 10% del límite. El broker ya confirmó cada uno de esos eventos a su dispositivo, así que nada puede rechazarlos: una vez lleno, el stream descarta los más antiguos sin reenviar (`JetStreamDurableLostUnread`) y los dispositivos nunca se enteran. Salta minutos antes que `JetStreamDurableUnreadNearFull`, porque un atraso rápido puede pasar de vacío a descartar en menos de sus 5 minutos. | Los dispositivos publican más rápido de lo que `event-sources` reenvía. Si `event-sources` no está ocupado en CPU, el límite es su ventana de publicación: `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}` crece cerca de un segundo por segundo. Añada réplicas de `event-sources`, cada una con su propia ventana: `capture.publishWindow` ya vale por defecto su máximo, 1024, así que súbalo solo si se bajó. Si salta `JetStreamIngestBackpressureEngaged`, el consumidor lento está más abajo. Para ganar tiempo, suba `streamMaxBytes` junto con el volumen de JetStream, y también `streamMaxMsgs` si el stream se llena antes por número de mensajes (cargas pequeñas); el stream de captura toma esos límites completos y `event-sources` los aplica al reiniciarse. |
| `JetStreamCaptureBacklogGrowing` | warning | El aviso temprano de `JetStreamCaptureBacklogWillOverflow`. El stream de captura, que guarda los eventos de los dispositivos MQTT hasta que `event-sources` los reenvía, se llenará en menos de veinte minutos al ritmo al que creció su atraso sin reenviar en los últimos cinco, y ese atraso es al menos el 10% del límite. El broker ya confirmó cada uno de esos eventos a su dispositivo, así que nada puede rechazarlos: una vez lleno, el stream descarta los más antiguos sin reenviar (`JetStreamDurableLostUnread`) y los dispositivos nunca se enteran. En una prueba de carga la alerta crítica saltó dos minutos antes de que empezara la pérdida; esta deja tiempo para añadir capacidad antes. | Los dispositivos publican más rápido de lo que `event-sources` reenvía. Añada ya réplicas de `event-sources`, cada una con su propia ventana de publicación, y revise su CPU y `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}`. Si salta `JetStreamIngestBackpressureEngaged`, el consumidor lento está más abajo. Para ganar tiempo, suba `streamMaxBytes` junto con el volumen de JetStream. |
| `JetStreamCaptureLeaderChanged` | warning | El stream de captura cambió de líder en los últimos 15 minutos mientras llegaban eventos (`devicechain_*_jetstream_stream_leader_changes_total`, contado a partir de la información del stream que el broker da cada 30 segundos, junto con `devicechain_eventsources_total_inbound_messages` habiéndose movido en el mismo periodo). Mientras el stream no tiene líder, algo que dura varios segundos, la pasarela MQTT del broker sigue confirmando a los dispositivos las publicaciones QoS 1 que le llegan y no las guarda en ningún sitio, y nada las cuenta: **los eventos MQTT publicados durante el cambio pueden haberse confirmado y perdido**, y `JetStreamDurableLostUnread` no salta para esta pérdida. En una prueba de 12 000 eventos por segundo, un cambio así perdió unos cinco segundos de eventos. El ajuste por defecto tiene una segunda forma de perder eventos confirmados, que esta alerta no cubre y que no necesita ningún cambio de líder: si `event-sources` se queda atrás con carga sostenida hasta que el stream de captura alcanza su límite, el stream descarta sus mensajes sin leer más antiguos, y `JetStreamDurableLostUnread` lo informa (a partir de `devicechain_<area>_jetstream_consumer_unread_skipped_total`). No se detecta un cambio más corto que una muestra que vuelve al mismo servidor. Un reinicio progresivo del broker, incluidos los que hace una actualización, también mueve líderes y puede perder eventos de la misma forma con cualquier volumen de tráfico; como salta en reinicios rutinarios, es una advertencia. Con `infrastructure.nats.mqttFailClosed` activado, un evento QoS 1 lo guarda el almacén de mensajes del broker y no se pierde así; solo se pierden los eventos QoS 0. Vea [MQTT: el bróker de la plataforma](../reference/transport-matrix.md#mqtt--the-platform-broker) para la limitación. | Busque en los logs del broker `Leader falling behind, stepping down`: es un líder cuyo nodo está sobrecargado. Revise la CPU de los nodos del broker y si el líder del stream de captura comparte nodo con el líder de inbound-events (`nats stream cluster step-down --preferred` lo mueve). Los eventos perdidos no se pueden recuperar; la hora del cambio acota la ventana que revisar en los registros de sus propios dispositivos. Programe los reinicios planificados del broker con poco tráfico. |
| `JetStreamReaderForeignDropped` | warning | El lector de captura de dispositivos descartó, en los últimos 15 minutos, mensajes guardados bajo un subject que no lee (`devicechain_*_jetstream_reader_foreign_dropped_total`). El broker le entrega todo el stream de captura, y el lector se queda solo con los subjects en los que los dispositivos publican telemetría. Una publicación de un dispositivo nunca llega ahí bajo otro subject, así que un mensaje descartado entró en el stream por otra vía, muy probablemente un consumidor push de otro stream cuyo subject de entrega es un subject de telemetría de dispositivo. No se reenvió nada y no falta ningún evento de dispositivo. Busque el consumidor push (`nats consumer ls` en cada stream) y la credencial que lo creó. event-sources registra el subject y la secuencia del primer mensaje que descarta. |
| `JetStreamStreamNearFull` | info | Un flujo que guarda registros para un operador, en lugar de mensajes que procesa un servicio, lleva 10 minutos por encima del 80% de su límite (en bytes o en mensajes). Son los registros de mensajes que fallaron (`failed-decode`, `failed-events`, `connector-dispatch.dead`), `max-deliveries`, y `dead-letters` mientras `user-management`, que guarda sus cartas muertas, no informe de que lo lee. Nada procesa lo que contienen, así que cuando se llenan descartan registros que nadie ha mirado. Los flujos que leen los servicios no se cubren: conservan una semana de historial que normalmente está cerca del límite, y las alertas de esta tabla vigilan a sus consumidores. | Busque qué lo está llenando: un decodificador que rechaza los mensajes de un dispositivo, eventos que no se pueden resolver o guardar, un conector cuyo destino rechaza todos los envíos. En `dead-letters`, compruebe que `user-management` está en marcha. Aumente el límite solo si hay que conservar los registros más tiempo. La configuración predeterminada de Alertmanager de kube-prometheus-stack suprime las alertas `info`, así que enrútela explícitamente si la quiere recibir. |
| `JetStreamDurableLostUnread` | critical | Un consumidor pasó por encima de mensajes que se eliminaron antes de que los leyera. Nunca se procesaron. | Si en ese momento se estaba eliminando un tenant, es lo esperado: la eliminación borró mensajes a los que el consumidor aún no había llegado. Si no, el flujo estaba lleno mientras este consumidor iba atrasado. O bien el límite es demasiado pequeño para el tráfico, o bien el consumidor es más lento que su productor. |
| `JetStreamDurableStalledBehindStream` | critical | Un consumidor lleva al menos dos minutos sin recibir ningún mensaje, y el flujo ya ha descartado mensajes por delante de él. Un consumidor que está leyendo, aunque sea despacio, no dispara esta alerta; sus pérdidas disparan `JetStreamDurableLostUnread`. | El servicio está en marcha, ya que es él quien lo informa, pero su consumidor no lee. Busque un procesamiento de mensajes bloqueado en una dependencia, como la base de datos, o pods esperando a estar listos. Si no se puede arreglar rápido, aumente el límite del flujo para que deje de descartar. |

Los límites son `streamMaxBytes` (los flujos de alto volumen), `streamMaxBytesCold` (los demás) y
`streamMaxMsgs`, bajo `instance.config.infrastructure.nats`. El volumen de JetStream se dimensiona a
partir de su suma, así que aumente el volumen junto con ellos (consulte
[Arrancar una instancia](./bootstrap.md)).

Las alertas de acumulación (`JetStreamCaptureBacklogWillOverflow`, `JetStreamCaptureBacklogGrowing`,
`JetStreamDurableFallingBehind` y `JetStreamDurableUnreadNearFull`) leen la acumulación de cada consumidor
del servicio que lo lee y, si ese servicio no informa de nada, de las métricas del propio bróker de
mensajes (`nats_consumer_num_pending`, `nats_consumer_num_ack_pending` y los límites `nats_stream_*`,
del sidecar de métricas del bróker). Por eso un servicio lector caído, que es la forma habitual en que un
flujo se acumula, no las silencia. Las cifras del bróker se limitan a los flujos de la propia instancia y
solo el líder del consumidor informa de un recuento, así que las alertas toman el valor más alto entre los
pods del bróker. Si su Prometheus no recoge las métricas del bróker (el monitor de bróker del chart sí lo
hace), la alternativa queda vacía y estas alertas necesitan que el servicio lector esté en marcha, como antes.

Las alertas leen estas series. Cada servicio exporta las tres primeras para cada consumidor duradero
que lee:

- **`devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`** cuenta los
  mensajes que el consumidor pasó por encima sin leerlos. Es un límite inferior: una reentrega, o un
  mensaje eliminado por detrás del consumidor, hace que cuente menos, nunca más.
- **`devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`** es cuántos
  mensajes se han descartado por delante de un consumidor que no ha recibido ningún mensaje desde la
  muestra anterior (cada 30 segundos). Vale 0 mientras el consumidor lee, aunque vaya atrasado: esas
  pérdidas son del contador. Vuelve a 0 cuando el consumidor lee de nuevo, y el contador anterior
  toma el relevo.
- **`devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}`** es la cola sin leer del
  consumidor (pendientes más sin confirmar) dividida entre lo que cabe en el flujo, en mensajes o en
  bytes, según qué límite sea más estricto. No se exporta para los dos consumidores que frenan la
  ingesta; los servicios que escriben en sus flujos exportan el mismo número como
  `jetstream_backpressure_unread_ratio`. No aparece hasta la primera muestra ni mientras no se
  puede medir.

Cada servicio exporta además **`devicechain_<area>_jetstream_stream_sink{stream}`** para cada flujo
que escribe o lee: 1 para un flujo que guarda registros para un operador, 0 para cualquier otro.
`JetStreamStreamNearFull` solo lee el llenado de un flujo donde vale 1.

Las dos primeras existen con valor 0 desde que el servicio crea el lector del consumidor; la
proporción aparece con la primera muestra. Todas las réplicas de un servicio informan
del mismo consumidor y cuentan la misma pérdida, así que combínelas con `max`, no con `sum`. Reiniciar
un pod pone el contador a cero, así que léalo con `increase()` o `rate()`. Cada pod mide desde su
propia primera muestra, así que una pérdida que el consumidor pasa por encima mientras todos los pods
del servicio lector se reinician a la vez puede quedar sin contar. Un servicio sin pods en marcha no
informa ninguna de estas series, así que ninguna alerta por consumidor puede dispararse por él; sus
alertas de salud de los pods cubren ese caso.

## Un consumidor que se queda atrás {#consumer-backlog}

Un consumidor puede no perder nada y aun así ir muy atrasado: todo lo que su servicio deriva del
flujo va entonces igual de desfasado. Cada servicio informa, para cada consumidor duradero que lee,
de cuántos mensajes le esperan, con una muestra cada 30 segundos:

- **`devicechain_<area>_jetstream_consumer_pending_messages{stream, durable}`**: mensajes del flujo
  que todavía no se le han entregado al consumidor.
- **`devicechain_<area>_jetstream_consumer_ack_pending_messages{stream, durable}`**: mensajes que
  se le han entregado y aún no ha confirmado.

Ambas series aparecen con la primera muestra tras arrancar el servicio, no antes, y vuelven a
desaparecer mientras no se puede leer el consumidor. Una serie ausente significa «no medido», nunca
«no hay nada esperando». Todas las réplicas informan del mismo consumidor, así que combínelas con
`max`. Un atraso que crece y se reduce es normal durante las ráfagas. El que se mantiene es el que
vigila la alerta. Sus 15 minutos sobreviven al reinicio de un pod mientras otra réplica siga
informando. Con una sola réplica, un reinicio retira la serie hasta la primera muestra del pod nuevo
y los 15 minutos vuelven a empezar, así que un pod que se reinicia una y otra vez con atraso puede no
dispararla nunca: vigile también su número de reinicios.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamDurableFallingBehind` | warning | Un consumidor ha tenido más de 10000 mensajes esperándole durante 15 minutos. Todo lo que ese servicio deriva del flujo va así de atrasado: en `device-state`, el estado en vivo de un dispositivo va por detrás de sus eventos almacenados. No cubre el consumidor de detección de `event-processing`, porque lo vigila `DetectConsumerBacklogHigh` y una toma de control lo vuelve a leer por diseño. | Compare el ritmo del consumidor con el del flujo. Si mantiene el paso pero no recupera, dele capacidad (en `device-state`, consulte [sus ajustes](#live-state-projection) y su base de datos). Si se ha detenido, `JetStreamDurableStalledBehindStream` y los registros del servicio indican por qué. Si el flujo se llena antes de que se ponga al día, se descartarán los mensajes a los que aún no ha llegado, salvo para los dos consumidores que frenan la ingesta (consulte [Contrapresión en la ruta de ingesta](#ingest-backpressure)), donde antes se rechazan los eventos nuevos. |

## Contrapresión en la ruta de ingesta {#ingest-backpressure}

Dos flujos llevan eventos que la plataforma no puede perder antes de procesarlos:
`inbound-events` (eventos pendientes de resolver) y `resolved-events` (eventos pendientes de
almacenar). Cada uno tiene un consumidor cuyos eventos sin leer se perderían si el flujo los
descartara: el de `device-management` en `inbound-events` y el de `event-management` en
`resolved-events`. En estos dos flujos la plataforma rechaza eventos nuevos en lugar de descartar
los que ese consumidor aún no ha leído.

- Cuando la cola **sin leer** de ese consumidor alcanza el **90%** de lo que cabe en el flujo, los
  servicios que escriben en él empiezan a rechazar eventos nuevos. Vuelven a aceptarlos cuando la
  cola baja del **80%**. Para este umbral solo cuenta la cola sin leer: los eventos ya procesados
  que el flujo conserva durante una semana no cuentan.
- Un flujo lleno descarta primero sus eventos más antiguos, así que los eventos ya procesados que
  conserva por delante de los no leídos del consumidor son lo que se agota antes de que se pierda
  un evento sin leer. Los servicios también rechazan cuando ese historial procesado se descartaría
  entero en **30 segundos** al ritmo al que el flujo lo está descartando. Lo que dura no depende del
  tamaño de los eventos, así que una ráfaga de eventos grandes sobre un historial de eventos
  pequeños se rechaza aquí aunque la cola sin leer esté muy por debajo del 90%. Vuelven a aceptar
  eventos cuando ese historial duraría **un minuto**, o el consumidor lo ha leído todo. Un flujo
  lleno cuyo consumidor mantiene el ritmo no rechaza nada: lo que el flujo descarta se sustituye
  por eventos que el consumidor ya ha terminado. La excepción es un flujo lleno que contiene menos
  de un minuto aproximadamente de su propio tráfico: un consumidor que se retrasa unos segundos
  puede hacer que rechace, y entonces solo vuelve a aceptar cuando ese consumidor lo ha leído todo.
  Es más probable con límites de flujo pequeños, como en una instalación creada con `--compact`, y
  eventos grandes que llegan deprisa.
- Un evento que el consumidor recibió pero no ha confirmado cuenta como no leído, también uno que
  espera a entregarse de nuevo tras un fallo. Mientras un evento así está cerca del principio de un
  flujo lleno, el flujo puede rechazar eventos nuevos hasta que se confirme o se abandone, en lugar
  de descartarlo.
- Cada servicio mide el flujo cada 5 segundos, y también en cuanto él mismo ha escrito alrededor de
  una milésima parte del límite del flujo desde su última medición, como mucho cada 100 ms.
- Un servicio que no puede medir la cola durante 30 segundos trata el flujo como lleno y también
  rechaza.
- `device-management` deja de leer `inbound-events` mientras `resolved-events` rechaza, y
  `event-sources` deja de leer el flujo de captura MQTT mientras `inbound-events` rechaza. La cola
  espera en el flujo anterior, y ningún mensaje gasta sus intentos de entrega.
- El rechazo afecta a **todos los inquilinos**, porque los flujos son compartidos. Antes de
  consultar la compuerta compartida, cada inquilino se limita a su propio techo de ingesta, contado
  en lecturas. Con el valor por defecto de 1000 lecturas por segundo, los dispositivos de un solo
  inquilino no pueden enviar por sí mismos más de lo que se midió que almacena una instalación de
  alta disponibilidad por defecto, en el clúster descrito en
  [qué permite el valor por defecto](../concepts/governance.md#ingest-default), con una réplica de
  `event-sources`. Varios inquilinos juntos sí pueden, igual que un inquilino cuyo nivel eleva su
  techo, cualquier inquilino en un clúster más pequeño y un cliente que publica por HTTP con muchos
  nombres de inquilino, porque un nombre se mide antes de comprobar su credencial. Entonces la
  compuerta rechaza a todos.
- El broker sigue descartando el mensaje más antiguo cuando un flujo está lleno, así que aún se
  puede perder un evento sin leer en tres casos. Los servicios que escriben en el flujo pueden
  gastar, entre dos de sus mediciones, más historial procesado del que se vio descartar al flujo en
  los 30 segundos anteriores. Un flujo puede llegar a su límite por primera vez casi sin historial
  procesado, antes de que se haya medido ningún ritmo de descarte. Y las transiciones de conexión
  y desconexión se admiten mientras el flujo rechaza (ver más abajo). Las alertas de
  [Mensajes que un consumidor nunca leyó](#unread-loss) lo informan.
- Eliminar un inquilino quita sus eventos de los dos flujos. Eso libera espacio, así que por sí
  solo no hace que el flujo rechace, con una excepción: un inquilino cuyos eventos son los más
  antiguos que conserva un flujo lleno, y tan pocos que el flujo sigue lleno sin ellos. Quitarlos
  parece lo mismo que un flujo que descarta su historial deprisa, así que el flujo puede rechazar
  hasta que el consumidor se ponga al día. No se pierde nada.

Qué hace cada transporte mientras el flujo rechaza:

| Transporte | Qué ve el dispositivo |
| --- | --- |
| HTTP | `503` con `Retry-After: 10`, después de que la solicitud haya pasado el techo de su inquilino (un inquilino por encima recibe `429`). El evento no se almacenó. Reinténtelo. |
| MQTT (el broker de la plataforma) | Nada. El broker confirmó el mensaje antes de que la plataforma pudiera rechazarlo. El mensaje espera en el flujo de captura, que descarta sus mensajes más antiguos cuando se llena (`JetStreamDurableLostUnread`). Con `infrastructure.nats.mqttFailClosed` activado, un mensaje QoS 1 espera en cambio en el almacén de mensajes del broker, que rechaza las publicaciones nuevas cuando está lleno: el dispositivo no recibe PUBACK y vuelve a enviar el mensaje (consulta [MQTT a prueba de fallos](#mqtt-fail-closed)). |
| Broker MQTT externo | Nada. El mensaje ya estaba confirmado. Se descarta y se cuenta en `devicechain_eventsources_total_msg_backpressured{source}` (un mensaje por encima del techo de su inquilino se cuenta en `devicechain_eventsources_total_msg_rate_limited`). |
| Sparkplug | Las lecturas se descartan sin reintentar y se cuentan en `devicechain_sparkplugingest_ingest_failures_total`. |
| LwM2M | Las notificaciones se descartan y se cuentan en `devicechain_lwm2mingest_notify_ingest_dropped_total`. La siguiente notificación sustituye a la perdida. |

Las transiciones de conexión y desconexión (del broker, los nacimientos y muertes de Sparkplug, los
registros de LwM2M) se siguen aceptando mientras el flujo rechaza, porque nada volvería a enviar una
transición rechazada. Ocupan el 10% del flujo que queda por encima del umbral de rechazo. La
compuerta de contrapresión no limita cuántas admite (el techo del inquilino sigue aplicándose a la
toma del broker): los dispositivos deciden con qué frecuencia se conectan y desconectan,
así que una flota que se reconecta en bucle puede llenar ese margen, y entonces el broker descarta
los eventos más antiguos, incluidos los no leídos, como hacía antes de esta versión. Solo estos dos consumidores frenan
la ingesta. Un `device-state` o un `event-processing` lentos no lo hacen. Sus pérdidas sin leer las
siguen informando las alertas anteriores. La posición real de `event-processing` es su propio punto
de control, que la compuerta no ve. `ReplayCoveredDeliveriesExhausted` lo vigila.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamUnreadBacklogNearFull` | warning | Un consumidor que controla la compuerta lleva 5 minutos con más del 80% de su flujo sin leer. Al 90% el flujo rechaza eventos nuevos para todos los inquilinos: los dispositivos HTTP reciben `503` con `Retry-After`, los eventos de los dispositivos MQTT esperan en el flujo de captura, y las lecturas de Sparkplug y LwM2M, y los eventos de un broker MQTT externo, se descartan y se cuentan. | Averigüe por qué el consumidor va lento: los registros de su servicio, su base de datos, `JetStreamDurableFallingBehind`. Si el tráfico ha superado el flujo, aumente su límite y, con él, el volumen de JetStream. |
| `JetStreamIngestBackpressureEngaged` | critical | Un flujo lleva un minuto rechazando eventos nuevos, para todos los inquilinos: la cola sin leer de un consumidor está cerca del límite, o el flujo lleno descarta el historial procesado por delante de ella tan deprisa que se agotaría en 30 segundos, o un servicio que escribe en él lleva 30 segundos sin poder medir esa cola. | `JetStreamUnreadBacklogNearFull` indica qué consumidor va atrasado. La causa más probable es que el servicio de ese consumidor no esté funcionando: escalado a cero réplicas o en un bucle de reinicios. Un servicio desplegado que no funciona sigue frenando la ingesta, a propósito. Si `JetStreamUnreadBacklogNearFull` no se dispara, es el segundo caso: `jetstream_backpressure_history_runway_seconds` lo muestra, y el registro del servicio que escribe indica el consumidor y el ritmo. El rechazo se levanta solo cuando la cola de ese consumidor baja del 80% y, si la causa era el historial, cuando ese historial duraría un minuto o el consumidor lo ha leído todo. Si ninguno de los dos indica un consumidor, puede que el servicio que escribe no esté llegando al broker: consulta [Una conexión con el broker que murió en silencio](#broker-connection-dead). |

Los servicios que escriben en los dos flujos exportan estas series:

- **`devicechain_<area>_jetstream_backpressure_unread_ratio{stream, durable}`**: la cola sin leer
  del consumidor (pendientes más sin confirmar) dividida entre lo que cabe en el flujo, en mensajes
  o en bytes, según qué límite sea más estricto. No aparece mientras no se puede medir. Combine los
  pods con `max`.
- **`devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`**: cuánto
  durarían los eventos procesados que quedan por delante de los no leídos del consumidor al ritmo
  al que el flujo lleno los ha estado descartando. `+Inf` mientras el flujo no está en su límite,
  no descarta ninguno, o el consumidor lo ha leído todo. El flujo empieza a rechazar por debajo de
  30 y vuelve a aceptar a partir de 60. No aparece mientras no se puede medir. Combine los pods con
  `min`.
- **`devicechain_<area>_jetstream_backpressure_engaged{stream}`**: 1 mientras el servicio rechaza,
  también mientras no puede medir la cola. Se lee cuando Prometheus hace el scrape, así que no
  puede mostrar 0 cuando el servicio de hecho está rechazando.
- **`devicechain_<area>_jetstream_publish_refused_total{stream}`**: mensajes que el servicio no
  publicó porque el flujo estaba rechazando.

## Una conexión con el broker que murió en silencio {#broker-connection-dead}

Un servidor del broker cuya máquina se detiene o se reinicia de golpe, o que una partición de red
deja aislado, no cierra nada: las conexiones de sus clientes siguen abiertas y simplemente dejan de
recibir respuesta. Cada servicio envía un ping a su servidor del broker cada 10 segundos y da una
conexión por perdida tras tres intervalos sin respuesta (30 segundos), o en cuanto una escritura
lleva 10 segundos sin avanzar, así que una conexión muerta se detecta en 40 segundos como mucho. Un
servidor que deja de leer durante 10 segundos mientras el servicio le escribe se abandona de la
misma forma. Después el servicio se reconecta, normalmente en pocos segundos. Mientras está aislado
no puede publicar ni recibir: en `event-sources`, la ingesta HTTP se rechaza (`503`) o agota su
tiempo, y la [compuerta de contrapresión](#ingest-backpressure) se declara activada porque no
puede medir la cola.

| Alerta | Gravedad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `BrokerConnectionDiedSilently` | warning | Un servicio abandonó una conexión con el broker que había dejado de responder, en los últimos 15 minutos, y se reconectó. | Comprueba si se perdió o se reinició un nodo: `kubectl get nodes`, y los eventos de máquinas virtuales de tu proveedor de nube, porque Kubernetes no registra nada de una máquina que se reinicia dentro del periodo de gracia del nodo (entre 40 y 50 segundos, aproximadamente). Ejecuta `dcctl ha verify`. Si se repite para un mismo servicio sin ningún evento de nodo, el problema está en la red entre ese servicio y el broker. |

- **`devicechain_<area>_nats_connection_dead_total{detected_by}`**: conexiones que este servicio
  abandonó. `detected_by="ping"` significa que el servidor dejó sus pings sin respuesta;
  `detected_by="write"`, que una escritura llevaba 10 segundos sin avanzar. Combina los pods con
  `sum`.
- **`devicechain_<area>_nats_connection_dead_last_timestamp_seconds`**: cuándo abandonó el servicio
  la última, en segundos Unix, o 0 si no lo ha hecho desde que arrancó. La alerta lee esta, así que
  se dispara aunque Prometheus estuviera en el nodo perdido y no viera el momento en que cambió el
  contador.

El registro del servicio dice lo mismo e indica el servidor: `Disconnected from NATS: the server
left 2 pings sent 10s apart unanswered…` o `Disconnected from NATS: a write to the server made no
progress for 10s…`, y después `Reconnected to NATS`. La segunda conexión con el broker que mantiene
`event-sources`, la que observa las conexiones de los dispositivos al broker, tiene el mismo límite
pero no se cuenta: su registro dice `A write to the NATS server made no progress` o `Lost the NATS
system-account connection`.

## Una fuente MQTT externa que nadie lee {#external-mqtt-owner}

Una fuente que lee de un bróker MQTT que tú operas la lee un solo pod de `event-sources` a la vez;
los demás quedan a la espera y siguen listos, porque siguen atendiendo todo lo demás (consulta la
[Matriz de transportes](../reference/transport-matrix.md#external-mqtt-broker)). Así que una
fuente que nadie lee no es un pod caído: es un pod que tomó la fuente y no pudo llegar a tu
bróker, y lo vuelve a intentar cada 15 segundos, o pods que no pueden acordar cuál la lee porque
no llegan al bróker de mensajería de la plataforma.

- **`devicechain_eventsources_external_mqtt_owner{source}`**: 1 en el pod que lee la fuente, 0 en
  todos los demás. Cada pod lo exporta para cada fuente externa desde que arranca, así que combina
  los pods con `sum`: 1 es lo sano.
- **`devicechain_eventsources_total_msg_not_owner{source}`**: mensajes que a un pod le seguían
  llegando después de perder la fuente, descartados en lugar de almacenados, porque el pod que la
  tomó recibe los mismos mensajes.
- **`devicechain_eventsources_total_msg_publish_failed{source}`**: eventos de la fuente que se
  leyeron de tu bróker y se decodificaron, pero no se almacenaron porque falló publicarlos en el
  stream de eventos de la plataforma. Esa fuente no reintenta, así que cada uno se pierde. El
  registro del pod tiene una línea por cada uno, con la fuente y el tenant. Una tasa mayor que 0
  significa que el bróker propio de la plataforma rechazaba o no respondía; mira ahí, no en tu
  bróker.

La fuente publica con una ventana de eventos esperando a la vez la confirmación del bróker de la
plataforma, de modo que no queda limitada a una confirmación a la vez por cada worker de
decodificación, que en un bróker replicado (una confirmación tarda unos 11 ms) dejaba a una fuente
cerca de 440 eventos por segundo para todo el clúster, porque un solo pod lee cada fuente.

| Ajuste (configuración de `event-sources`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `externalMqtt.publishWindow` | `128` | Cuántos eventos mantiene una fuente MQTT externa esperando a la vez la confirmación del bróker de la plataforma, de 1 a 1024. Un valor fuera de ese rango, o un 0 explícito, detiene el servicio al arrancar; nunca se ajusta. La publicación no puede ir más rápido que este número dividido por lo que tarda una confirmación. Es un ajuste fijo, leído al arrancar. **También es cuánto más puede perder un pod.** Este transporte es como mucho una vez: a tu bróker se le dice que un mensaje llegó en cuanto la plataforma lo ha puesto en cola para decodificar, y un pod que se detiene de golpe pierde lo que había tomado y aún no almacenó, que son hasta 105 mensajes en cola o en decodificación, más hasta este número esperando confirmación. Un pod que se detiene limpiamente, o que cede la fuente a otro pod, espera (hasta 8 segundos) a que se almacenen primero. Vigila `devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="external-mqtt"}`: una tasa cercana a 1 significa que la ventana es lo que limita la publicación. |

`ExternalMqttSourceNotReadByOnePod` (aviso) salta cuando esa suma lleva dos minutos sin valer 1.
En 0, nadie lee la fuente y lo que tu bróker entrega mientras tanto se pierde; el registro del
último pod que tomó la fuente dice por qué. Una instalación sin fuentes externas no exporta esa
serie, y la alerta no salta.

`ExternalMqttSourceLosingEvents` (aviso) salta cuando una fuente lleva diez minutos perdiendo
eventos por publicaciones fallidas (`devicechain_eventsources_total_msg_publish_failed` en aumento).
Esos eventos ya se leyeron de tu bróker y se han perdido, así que revisa el bróker de mensajería de
la plataforma, no el tuyo; el registro de event-sources indica la fuente y el tenant de cada uno. Una
ráfaga aislada, como lo que estaba en vuelo cuando una parada dejó de esperar, se despeja sola y no
la dispara.

## Mensajes retenidos más allá de su ventana de confirmación {#held-past-ack-wait}

El broker da a un servicio una ventana fija para confirmar cada mensaje que le entrega. Un
mensaje que sigue sin confirmar cuando la ventana se cierra se entrega de nuevo, y el servicio
lo trata como si fuera nuevo. Para los dos servicios cuyo trabajo es un envío lento hacia fuera
de la plataforma —las notificaciones de alarmas y los conectores de salida— eso puede
significar una segunda notificación o una segunda llamada a un webhook. Ambos leen solo tantos
mensajes como trabajadores tienen libres para empezarlos, así que nada espera en una cola
mientras corre la ventana, y cada envío se corta con margen antes de que la ventana se cierre.
La alerta siguiente informa de los casos que aun así se producen.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `ReaderHeldMessagePastAckWait` | warning | Un manejador retuvo un mensaje más allá de su ventana de confirmación, así que se volvió a entregar. `stage=worker`: un envío tardó demasiado, así que el mensaje pudo enviarse dos veces. `stage=buffer`: un mensaje se descartó antes de entregarse, y se procesó su nueva entrega en su lugar. | Para `stage=worker`, busque un destino lento o que no responde detrás del servicio que indica la etiqueta `durable`. Para `stage=buffer`, el servicio no está al día con su stream. |

El aviso lee `devicechain_<area>_reader_held_past_ack_wait_total{durable, stage}`. El contador
existe con valor 0 desde que se crea el lector, así que `increase()` ve la primera vez que ocurre en
un pod. Cada pod cuenta solo los mensajes que retuvo él, así que combine los pods con `sum`, no con
`max`.

## Lectura anticipada {#fetch-ahead}

De forma predeterminada, un servicio pide 256 mensajes al broker y pide los 256 siguientes mientras
todavía procesa los que tiene, de modo que el tiempo de cada petición se solapa con el trabajo en
lugar de sumarse a él. Está activado para mantener pequeña la cola del motor de detección, no para
aumentar el rendimiento: con él, el número de eventos en espera delante de la detección se mantuvo
en cientos donde sin él llegaba a miles, sin coste de CPU, y la velocidad que sostiene la
plataforma no cambió. Los ajustes de `infrastructure.nats.fetch` lo cambian. Se leen al arrancar el
servicio, así que un cambio requiere un reinicio, y no modifican ningún consumidor del broker.

| Ajuste | Valor predeterminado | Qué hace |
| --- | --- | --- |
| `batch` | 256 | Cuántos mensajes pide cada petición, de 1 a 256 (cualquier otro valor se rechaza al arrancar). Un lote mayor reparte el tiempo de la petición entre más mensajes. Es también lo máximo que una conexión caída puede hacer perder de una sola petición, y el motor de detección vuelve a leer ese intervalo del flujo antes de continuar. |
| `ahead` | activado | Pide el lote siguiente mientras se entrega el actual, de modo que la petición se solapa con el trabajo. Con `ahead: false` se pide un lote cada vez. Los mensajes se siguen procesando en el orden en que llegan. |
| `aheadHoldBudgetMillis` | 1000 | Un lote pedido por adelantado espera mientras se procesa el anterior, y su ventana de confirmación ya está corriendo. Si los últimos lotes tardan más que este valor en entregarse, el servicio deja de pedir por adelantado y vuelve a una petición cada vez. Como máximo una décima parte de la ventana de confirmación; de lo contrario el servicio se niega a arrancar. |

Los servicios que leen solo tantos mensajes como trabajadores libres tienen (las notificaciones
de alarmas y los conectores salientes) ignoran estos ajustes. Cuando un servicio se detiene, un
lote que había pedido por adelantado no se confirma, de modo que el broker se lo entrega al
siguiente lector cuando se cierra su ventana.

## Mantener el tráfico en un solo servidor del broker {#leader-affinity}

En un broker en clúster, cada stream, y cada consumidor que lo lee, tiene como líder a uno de
los servidores NATS. Un servicio conectado a otro servidor paga un salto adicional entre
servidores por todo lo que hace con ellos: cada publicación se reenvía al líder del stream y su
confirmación se reenvía de vuelta, y cada entrega y cada confirmación cruzan entre el líder del
consumidor y el servidor del lector. En un clúster de tres servidores bajo carga, estos mensajes
reenviados eran alrededor de una cuarta parte de todo lo que manejaba el broker, y el trabajo del
broker crece con el número de mensajes, no con su tamaño. Los dos ajustes de
`infrastructure.nats.affinity` los eliminan. `publishToLeader` está activado por defecto y
`consumerLeaders` desactivado; ambos se leen al arrancar el servicio. Ninguno cambia qué se
guarda, en qué orden ni qué se confirma.

| Ajuste | Por defecto | Qué hace |
| --- | --- | --- |
| `consumerLeaders` | desactivado | Cuando el líder de un consumidor ha estado en un servidor distinto al de su lector durante tres muestras seguidas (alrededor de un minuto y medio), el servicio pide al broker que lo mueva al servidor del lector. No lo pide mientras el broker no está sano: mientras un stream o un consumidor no tiene líder o tiene una réplica atrasada, ni en los dos minutos siguientes a una reconexión del servicio. Mueve un mismo consumidor como mucho una vez por minuto, y deja el líder donde está cuando las réplicas del servicio están repartidas entre servidores sin mayoría en ninguno. Lo pide una réplica, y las demás no. |
| `publishToLeader` | activado | Los servicios que mantienen muchas publicaciones en curso (los eventos resueltos que publica `device-management` y los eventos de dispositivo que reenvía `event-sources`) abren una segunda conexión al servidor que lidera el stream. Cuando el broker elige un nuevo líder se pasan a él, después de que se confirmen las publicaciones que ya estaban en curso. Si esa conexión no se puede abrir, o se pierde, publican por su conexión habitual, como con el ajuste desactivado. |

Ponga `publishToLeader: false` para desactivar la mitad que publica, o `consumerLeaders: true`
para activar la otra. En un clúster de tres servidores bajo carga, publicar al líder redujo los
mensajes que el broker maneja por evento de unos 11 a 9 o 10, redujo entre un tercio y casi la
mitad la mediana de lo que `device-management` espera a que el broker confirme un evento
resuelto, y elevó en torno a un 6%
lo máximo que resolvía una réplica de `device-management`. Durante una elección forzada de líder,
un reinicio escalonado y un escalado siguió publicando y se pasó al nuevo líder. Mover el líder de
un consumidor detiene brevemente sus entregas, y en el mismo clúster funcionó como se describe pero
no resolvió más eventos, así que `consumerLeaders` sigue desactivado. En un broker de un solo
servidor ninguno tiene nada que hacer.

- **`devicechain_<area>_jetstream_consumer_leader_local{stream, durable}`**: 1 mientras el líder
  del consumidor es el servidor al que está conectado este servicio, 0 mientras es otro. Ausente
  mientras no se puede leer el consumidor. Cuando varias réplicas leen un consumidor desde
  servidores distintos, las que están en minoría marcan 0, y es lo esperado.
- **`devicechain_<area>_jetstream_publish_leader_local{suffix, writer}`**: 1 mientras todos esos
  escritores de ese nombre en el stream publican por una conexión a su líder, 0 mientras alguno
  publica a través de otro servidor. `writer` es el mismo nombre que llevan las métricas de la
  ventana de publicación.
- **`devicechain_<area>_jetstream_consumer_leader_moves_total{stream, durable, result}`**:
  movimientos que este servicio pidió al broker. `result="moved"` es un movimiento hecho;
  `result="failed"` es uno que el broker rechazó o no respondió, y que no cambió nada.

Los dos indicadores se exportan también con los ajustes desactivados, de modo que desactivar un
ajuste muestra lo que estaba haciendo.

## Mensajes que agotaron sus intentos de entrega {#max-delivery-records}

Tras cinco entregas sin confirmar, el broker deja de entregar un mensaje. Publica un aviso la
siguiente vez que se lee del consumidor después de que venza la ventana de confirmación de la última
entrega, así que, para un servicio caído, el aviso espera hasta que el servicio vuelve a funcionar. Un
stream de la plataforma, `max-deliveries`, captura esos avisos, y cada servicio convierte los
suyos en entradas de la cola de mensajes no entregados, con el motivo `no-outcome` (consúltelas
con `dcctl dead-letters list`). El stream es una cola de trabajo: un aviso registrado se borra,
así que en una instancia sana está vacío. El contador
`devicechain_<área>_max_delivery_records_total{stream,outcome}` dice qué se hizo con cada aviso.

Hay un consumidor que es una excepción, y está declarado como tal: el motor de detección de
`event-processing` lee `resolved-events` desde su propio punto de control guardado. Confirma un
evento solo cuando un punto de control lo cubre, y tras un reinicio vuelve a leer el stream desde
el último punto de control, así que un evento que agotó sus intentos de entrega no se ha perdido.
Cuando su punto de control no se puede guardar (normalmente porque su base de datos no responde)
durante más tiempo del que el broker sigue reentregando, todos los eventos de ese intervalo agotan
sus intentos. Esos avisos no se convierten en mensajes no entregados, que informarían de cientos de
pérdidas que no ocurrieron. Se cuentan con `outcome="replay-covered"`, y la alerta siguiente
informa de ellos. Los demás servicios que leen `resolved-events` no tienen ese punto de control, y
sus avisos se registran como mensajes no entregados como siempre.

| Alerta | Qué significa | Qué hacer |
| --- | --- | --- |
| `MaxDeliveryRecordsWaiting` | Hay avisos de mensajes que agotaron sus intentos esperando desde hace 15 minutos sin convertirse en registros. | Compruebe que todos los servicios están en marcha: uno caído registra tarde. Si el aviso persiste con todo sano, nombra un consumidor que ya ningún servicio lee (un lector retirado en una actualización); no se registrará y puede borrarse del stream. |
| `ReplayCoveredDeliveriesExhausted` | Un consumidor que lee su stream desde su propio punto de control agotó intentos de entrega en los últimos 15 minutos, porque el punto de control lleva sin guardarse más tiempo del que el broker sigue reentregando. Todavía no se ha perdido nada. | Corrija lo que impide al servicio que indica la etiqueta `job` guardar su punto de control, normalmente su conexión a la base de datos. Mientras el servicio sigue en marcha, guarda lo que ha leído en cuanto el punto de control se guarda. Si se reinicia antes, vuelve a leer el stream desde el último punto de control guardado, y los eventos que el stream ya haya descartado no se pueden volver a leer, así que vigile también `JetStreamDurableUnreadNearFull`. |

## Un contenedor que se reinició {#container-restarts}

Un servicio de DeviceChain termina por sí mismo, con un estado de salida distinto de cero, cuando
ya no puede hacer su trabajo: cuando las lecturas de uno de sus bucles en el broker de mensajería
llevan dos minutos fallando sin ninguna respuesta entre medias, o cuando una de sus partes informa
de que el proceso no puede continuar. Su sonda de vitalidad también falla, y Kubernetes lo
reinicia, cuando su conexión con el broker se ha cerrado de forma definitiva. El pod suele volver a
estar listo en segundos. Los mensajes que el contenedor había leído del broker sin confirmar se
entregan de nuevo, así que un reinicio no los pierde. Las peticiones que un dispositivo o un cliente
tenía en curso con el contenedor reiniciado fallan y hay que reintentarlas, y cada reinicio alarga
la interrupción que lo provocó.

| Alerta | Qué significa | Qué hacer |
| --- | --- | --- |
| `InstanceContainerRestarted` (warning) | Kubernetes reinició en los últimos 15 minutos el contenedor que indican las etiquetas `pod` y `container`. Se dispara una vez por contenedor y se despeja 15 minutos después del último reinicio de ese contenedor. | Lea el registro del contenedor que terminó: `kubectl logs --previous -n <namespace> <pod>`. La última línea de error dice por qué, normalmente tras "A component has declared this process unfit to continue". Un bucle de lectura que se rindió nombra su flujo y su último error del broker. Si varios servicios se reiniciaron a la vez, mire primero el broker: compruebe que sus servidores están en marcha y ejecute `dcctl ha verify`. Si el contenedor reiniciado es un servidor del broker (`dc-nats-*`), lea también su registro anterior; sus streams tardan unos minutos en volver a estar al día. Si el último estado del contenedor es `OOMKilled`, aumente su límite de memoria. Los reinicios en los primeros minutos de una instalación o una actualización son de esperar. |
| `InstanceAreaUnavailable` (critical) | Un deployment de esta instancia quiere al menos un pod y lleva diez minutos sin ninguno disponible, así que esa área no está sirviendo. Cuando todos los pods de un área desaparecen, el área deja de informar de su propia disponibilidad y de sus métricas, por lo que los paneles de disponibilidad y de objetivos de recogida del resumen no la muestran; esta alerta y el panel "Areas with no pod available" sí. Un área reducida a cero a propósito no quiere pods y no cuenta. | Ejecute `kubectl describe deployment <deployment> -n <namespace>` y `kubectl get pods -n <namespace>`. Muestran una descarga de imagen fallida, un pod que no se puede planificar por falta de CPU o memoria, una sonda de disponibilidad fallida o una caída antes de estar listo (`kubectl logs --previous`). Si el área se redujo a cero por error, vuelva a escalarla. Lee `kube_deployment_spec_replicas` y `kube_deployment_status_replicas_available` de kube-state-metrics, así que sin él nunca se dispara. Las áreas arrancan en orden durante una instalación o una actualización, por eso espera diez minutos. |

La alerta lee `kube_pod_container_status_restarts_total` de kube-state-metrics, que instala la pila
de monitoreo. Si usa su propio Prometheus sin kube-state-metrics, nunca se dispara. Un pod que se
sustituye en lugar de reiniciarse, por ejemplo durante una actualización o tras
`kubectl delete pod`, empieza una cuenta nueva y no la dispara. Un pod que se reinicia una y otra
vez lo notifica también la propia `KubePodCrashLooping` de la pila de monitoreo, a los 15 minutos.

La alerta también notifica un reinicio ocurrido mientras kube-state-metrics se estaba moviendo, por
ejemplo tras perder su nodo, siempre que kube-state-metrics vuelva antes de 15 minutos. Tras una
caída así se despeja 15 minutos después de la última vez que kube-state-metrics informó antes de la
caída. Un reinicio durante una caída más larga de kube-state-metrics no se notifica.

## La solicitud de certificados de dispositivo del broker {#broker-client-certificates}

El broker de mensajes es la compilación propia de DeviceChain del servidor NATS: el servidor
original, sin cambios, con un añadido. Su listener MQTT pide a cada dispositivo un certificado de
cliente TLS sin exigirlo, de modo que un dispositivo que se autentica con certificado demuestra que
posee la clave del certificado durante el handshake TLS, y un dispositivo que usa un token o una
contraseña se conecta exactamente como lo haría con cualquier servidor NATS. El broker vuelve a
aplicar ese ajuste cada vez que recarga su configuración, y comprueba su propio listener al
arrancar, después de cada recarga y una vez por minuto.

| Alerta | Qué significa | Qué hacer |
| --- | --- | --- |
| `BrokerMqttClientAuthLost` (warning) | Durante diez minutos, el servidor del broker indicado por la etiqueta `pod` ha servido MQTT sobre TLS sin pedir certificados de cliente, o no ha ejecutado la imagen del broker de DeviceChain (el sidecar de métricas informa del servidor, pero el servidor no publica ninguna de sus propias métricas). Los dispositivos que se autentican con certificado no pueden conectarse; los dispositivos con token o contraseña no se ven afectados. | Compruebe la imagen del servidor: `kubectl get pod -n <namespace> <pod> -o jsonpath='{.spec.containers[0].image}'`. Si es la imagen original `nats`, la instancia volvió a ella, y los dispositivos con certificado seguirán rechazados hasta que se restaure la imagen de DeviceChain. Si es la imagen de DeviceChain, lea el log del servidor en torno a su última recarga de configuración y busque la línea que dice que el listener no está pidiendo certificados de cliente, o que la comprobación no pudo completarse; después reinicie el pod e informe del log. |

El broker exporta estas series en su propio puerto de métricas (`dc-metrics`, 7778):
`dc_broker_mqtt_client_auth_request` vale 1 mientras el listener pide certificados,
`dc_broker_mqtt_tls` vale 1 mientras termina TLS, y `dc_broker_info` indica la versión del
servidor NATS en la que se basa la compilación. Un broker sin TLS en MQTT no puede pedir
certificados y no dispara la alerta. En una instancia sin el sidecar de métricas (el preset
compacto) solo se detecta el primero de los dos estados anteriores.

## Inquilinos medidos con el valor por defecto de la plataforma {#tenant-ceilings}

Cada servicio que aplica un techo por inquilino lee el techo de cada inquilino desde
user-management. Mientras no tiene respuesta, mide al inquilino con el valor por defecto de la
plataforma, y el endpoint de ingesta HTTP da a los nombres de inquilino que no puede confirmar un
conjunto acotado de asignaciones. [Gobernanza](../concepts/governance.md#unresolved-ceilings)
explica ambos casos.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `TenantsMeteredAtPlatformDefault` | warning | Durante 15 minutos, el servicio indicado por la etiqueta `job` ha seguido midiendo a los inquilinos con su valor por defecto de la plataforma porque user-management no era accesible o fallaba. Un inquilino cuyo techo está por encima del valor por defecto se descarta antes de tiempo, y uno cuyo techo está por debajo se admite por encima de su techo. | Compruebe que user-management está en ejecución y que el servicio puede contactar con él. |
| `RateLimiterOverflowInUse` | warning | Durante 10 minutos, la ingesta HTTP ha admitido peticiones para nombres de inquilino que no pudo confirmar a través de la única asignación que todos comparten. Llegan muchos nombres sin confirmar, lo que suele indicar peticiones que nombran inquilinos inventados. | Revise quién envía peticiones de ingesta HTTP. Consulte [nombres de inquilino que no se pueden confirmar](../concepts/governance.md#unconfirmed-tenants). |
| `ReactShedLettersOverBudget` | warning | Durante 10 minutos, el motor de detección ha descartado acciones de salida más rápido de lo que las registra una a una, así que el exceso se resume en un mensaje no entregado por inquilino y minuto. Un inquilino supera con creces su techo de salida. | Busque el inquilino en `dcctl dead-letters` (motivo `shed`) y revise sus reglas, o suba su techo de salida si el tráfico es legítimo. Consulte [la gobernanza de salida](../concepts/outbound-connectors.md#governance). |
| `RateMeteringClockFallback` | warning | Durante una hora, el servicio indicado por la etiqueta `job` ha medido acciones de salida según la hora del bróker o de llegada porque no llevaban hora de desencadenamiento, así que una puesta al día tras un reinicio puede volver a descartarse como una inundación. Una hora de desencadenamiento posterior a la hora del bróker del mensaje que la lleva también se mide según esa hora del bróker, pero se cuenta con el origen `capped` y no dispara este aviso: indica que los relojes del pod y del bróker no coinciden, no que falte la hora. | Compruebe que event-processing y outbound-connectors ejecutan la misma versión. |
| `ConnectorDispatchRateLimited` | warning | Durante 15 minutos, outbound-connectors ha descartado envíos por superar la tasa de salida de su inquilino. El motor de detección mide el mismo techo sobre la misma línea de tiempo y descarta las acciones que lo superan antes de enviarlas, así que estas se admitieron en un extremo y se rechazaron en el otro. Un inquilino por encima de su techo no dispara este aviso; dispara `ReactConnectorEgressShedding` en el motor de detección. | Compruebe que la tasa de salida por defecto de la plataforma (`outboundCallsPerSecond` y `outboundBurst`) es la misma para event-processing y outbound-connectors, y si `TenantsMeteredAtPlatformDefault` está activo para alguno de los dos. Los envíos que fallan y se reintentan también se vuelven a medir en el extremo de los conectores, así que busque también un destino que falle. Eso también significa que un solo inquilino cuyo destino falla repetidamente mientras envía cerca de su cuota puede activar este aviso por sí solo, y por eso es un aviso de tipo warning y no critical. Los envíos descartados son mensajes no entregados con el motivo `rate_limited`. |

Las series detrás de estos avisos:

- `TenantsMeteredAtPlatformDefault` lee
  `devicechain_<area>_governance_unresolved_admissions_total{dimension, cause}` con
  `cause="unreachable"`.
- `RateLimiterOverflowInUse` lee `devicechain_eventsources_ratelimit_overflow_admissions_total`.
- `ReactShedLettersOverBudget` lee
  `devicechain_eventprocessing_react_connector_shed_unlettered_total{action}`.
- `RateMeteringClockFallback` lee `devicechain_<area>_rate_clock_fallback_total{source}`, donde
  `source` es `append`, `capped` o `now`. El aviso ignora `capped`.
- `ConnectorDispatchRateLimited` lee el resultado `rate_limited` de
  `devicechain_outboundconnectors_connector_dispatch_total`.

Solo el resultado `rate_limited` de `devicechain_outboundconnectors_connector_dispatch_total`
genera un aviso. Sus otros resultados de fallo son la configuración de un solo inquilino, como un
webhook que falla o un destino al que la plataforma se niega a conectarse. Esos envíos ya se
registran como mensajes no entregados y aparecen en `dcctl dead-letters`, y no avisan al operador.

## Resolución de eventos {#event-resolution}

`device-management` resuelve cada evento entrante antes de que nada lo almacene o lo evalúe. Un
resolvedor (cada uno de los trabajadores que resuelven eventos) autentica la credencial del evento,
lo que supone una lectura de la base de datos relacional, después consulta el perfil, las
relaciones y el alcance de grupos del dispositivo en el almacén clave-valor del bróker de
mensajes, y entrega el evento resuelto para que se publique. Esas tres consultas se hacen a la
vez. Lo que el almacén clave-valor no puede responder se lee de la base de datos una consulta
tras otra, así que un resolvedor sigue ocupando como máximo una conexión a la base de datos. Un
resolvedor pasa la mayor parte de cada evento esperando respuestas, no usando CPU. Varios resolvedores trabajan a la
vez. Los eventos que llegan mientras todos están ocupados esperan delante de ellos, en orden: hasta
100 en la cola de entrega del pod y, detrás de ellos, los lotes que cada lector de entrada ha leído
del flujo (un lector salvo que `resolution.inboundReaders` indique otra cosa): hasta 256 en el
lote que se está entregando y, con la [lectura anticipada](#fetch-ahead) activada, hasta 256 más
en el lote pedido por adelantado. El resto espera
en el flujo.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_devicemanagement_resolve_workers` | Cuántos resolvedores ejecuta el pod. |
| `devicechain_devicemanagement_resolve_inflight` | Cuántos de ellos están ocupados. Cuando se mantiene en `resolve_workers`, los eventos llegan más rápido de lo que el pod los resuelve y se acumulan delante de él. El recuento de pendientes del consumidor de entrada de `device-management` crece entonces (consulte [Un consumidor que se queda atrás](#consumer-backlog)). |
| `devicechain_devicemanagement_resolve_messages_total` | Eventos resueltos, por resultado. Mientras todos los resolvedores están ocupados, su ritmo es cuántos eventos por segundo puede resolver el pod. |
| `devicechain_devicemanagement_resolve_not_gateway_child_total` | Eventos rechazados porque una pasarela publicó por un dispositivo que no tiene asignado. Consulta [la referencia de métricas](./metrics-reference.md) e [identidad de pasarela](./edge-services.md#gateway-identity). |

`resolve_duration_seconds` incluye el tiempo que un resolvedor espera para entregar su resultado.
Su intervalo más bajo es de 5 ms, así que un cuantil por debajo de eso es una estimación, no una
medida.

### Ajustarlo

| Ajuste (configuración de `device-management`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `resolution.workers` | `10` | Resolvedores que trabajan a la vez. Cada uno ocupa una conexión a la base de datos mientras lee de ella la credencial de un evento, lo que hace siempre que la credencial no se haya verificado en esa réplica dentro de `inMemoryCache.ttlSeconds` (consulte [Cachés que dejan de responder](#kv-caches)). Por eso debe ser menor que el pool de conexiones del servicio (`rdbConfiguration.maxOpenConnections`, 20 si no se indica), que comparte con la API GraphQL, las comprobaciones de conexión MQTT y el consumidor que aplica las activaciones y resoluciones de alarmas. Se permite más de la mitad del pool, y se registra al arrancar. Las consultas de un resolvedor al almacén clave-valor se hacen a la vez, pero sus lecturas de la base de datos siguen haciéndose de una en una, así que nunca ocupa más de una conexión. |
| `resolution.publishWindow` | `512` | Cuántos eventos resueltos (y fallidos) mantiene un pod esperando la confirmación del broker a la vez, de 1 a 1024. Un valor fuera de ese rango detiene el servicio al arrancar; nunca se ajusta. La publicación no puede ir más rápido que este número dividido por lo que tarda una confirmación, y en un broker replicado una confirmación tarda de 8 a 11 ms aproximadamente, así que 128 dejaba a un pod cerca de 11.000 eventos por segundo y 512 no. Súbalo cuando `devicechain_<area>_jetstream_publish_window_full_seconds_total` crezca cerca de un segundo por segundo. Una ventana más profunda deja más eventos de origen sin confirmar en el pod: si el pod muere se reentregan tras la espera de confirmación del consumidor y se publican de nuevo con el mismo id de mensaje, que el stream reconoce (consulte `jetstream_publish_duplicates_total`). Cada evento de la ventana se guarda en memoria, junto con su origen, hasta que se confirma. Es un ajuste fijo, leído al arrancar. |
| `resolution.inboundReaders` | `1` | Cuántas lecturas mantiene un pod en curso a la vez sobre el consumidor de eventos de entrada, de 1 a 4. Un valor fuera de ese rango detiene el servicio al arrancar; nunca se ajusta. Cada lector pide al broker su lote siguiente mientras se resuelve el que tiene, así que un lector entrega como máximo un lote por viaje de ida y vuelta al broker: en un clúster de tres nodos con carga, un solo lector dejaba a un pod cerca de 10.000 eventos por segundo con sus resolvedores casi siempre libres. Los lectores comparten un mismo consumidor, así que cada evento va exactamente a uno de ellos, y comparten los resolvedores, las cachés y la publicación del pod, así que un lector cuesta mucho menos que una réplica. Cada lector mantiene hasta dos lotes sin confirmar (véase arriba), que un reinicio deja para que el broker los reentregue tras la espera de confirmación. Con más de un lector, una acumulación se entrega a razón de un lote por lector a la vez, así que mientras el consumidor se pone al día, eventos separados hasta un lote aproximadamente pueden llegar al flujo de eventos resueltos en el orden inverso, como ocurre con varias réplicas. En una flota lenta un lote puede abarcar más tiempo que el retraso permitido de la detección, y las reglas de detección que comparan una lectura con la anterior, o que cuentan lecturas en el orden en que llegan, notan ese desorden. Súbalo solo cuando los resolvedores estén casi siempre libres y el consumidor de entrada de `device-management` se siga quedando atrás: por debajo de unos 10.000 eventos por segundo por pod la lectura no es lo que lo limita, y cuando el límite es la CPU del nodo, otro lector no ayuda. Es un ajuste fijo, leído al arrancar. |
| `inMemoryCache.ttlSeconds` | `300` | Cuánto tiempo guarda una réplica un valor en su propia memoria, de 1 a 3600 segundos. Un cambio descarta el valor en todas las réplicas a la vez, así que esto solo acota un mensaje de descarte que se perdió. El tiempo de vida de la propia caché de clave-valor lo limita (véase más abajo). Consulte [Cachés que dejan de responder](#kv-caches). |
| `deviceCacheTtlSeconds`, `relationshipCacheTtlSeconds`, `metricDefCacheTtlSeconds`, `membershipCacheTtlSeconds` | `300` | El tiempo de vida de cada caché de clave-valor en el broker, de 1 a 3600 segundos. |
| `inMemoryCache.perDeviceCacheEntries` | `131072` | Cuántas entradas, como máximo, guarda cada réplica en memoria de cada una de las tres cachés que se guardan por dispositivo: un dispositivo por su token, sus relaciones seguidas y sus pertenencias a grupos. Consulte [Cachés que dejan de responder](#kv-caches). |
| `inMemoryCache.perDeviceCacheMiB` | `24` | Cuánta memoria, en MiB, ocupa como máximo cada una de esas tres cachés en cada réplica. Suba con él el límite de memoria del servicio. Consulte [Cachés que dejan de responder](#kv-caches). |
| `inMemoryCache.credentialCacheMiB` | `32` | Cuánta memoria, en MiB, ocupa como máximo la caché de credenciales de dispositivo en cada réplica, de 1 a 256. En el valor por defecto caben unos 29.000 dispositivos con credencial; 60.000 necesitan unos 70. Suba con él el límite de memoria del servicio. Consulte [Cachés que dejan de responder](#kv-caches). |

Súbalo cuando `resolve_inflight` se mantenga en `resolve_workers` mientras al pod le sobra CPU. Si
el pod está en su límite de CPU, más resolvedores no ayudan: dele más CPU (consulte
[Dimensionamiento de los servicios](./bootstrap.md#service-sizing)). Medido dentro del
proceso contra un bróker de tres servidores, con cada consulta tardando 750 µs y hechas una tras
otra, 5 resolvedores resolvieron unos 1500 eventos por segundo y 10 unos 2900. Los resolvedores terminan los eventos
fuera del orden de llegada, por una fracción de segundo; la detección los aplica en el orden en que
llegan al flujo de eventos resueltos. Un valor fuera de rango impide que el servicio arranque, y el
error nombra el ajuste. El servicio registra el valor que usa al arrancar.

### Ventana de publicación de ingesta {#ingest-publish-window}

`event-sources` publica cada evento decodificado en el broker y mantiene varias de esas publicaciones esperando la confirmación del broker a la vez, igual que `device-management`.

| Ajuste (config de `event-sources`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `capture.publishWindow` | `1024` | Cuántos eventos mantiene un pod esperando la confirmación del broker a la vez, de 1 a 1024. Un valor fuera de ese rango, incluido 0, detiene el servicio al arrancar; nunca se ajusta. La ingesta no puede ir más rápido que este número dividido por lo que tarda una confirmación. En un broker replicado sin carga una confirmación tarda de 8 a 11 ms aproximadamente, pero en uno cargado se midieron unos 70 ms, donde 512 dejaba a un pod cerca de 7.100 eventos por segundo mientras los dispositivos MQTT publicaban más rápido y el stream de captura se llenaba. Por encima de 1024, añada réplicas. Súbalo cuando `devicechain_eventsources_jetstream_publish_window_full_seconds_total{writer="capture"}` crezca cerca de un segundo por segundo. Cada evento de la ventana se guarda en memoria, junto con el mensaje de dispositivo del que viene, hasta que se confirma, así que la memoria crece en la ventana por el tamaño de un evento y un mensaje de origen: unos pocos MiB con 1024 para cargas habituales. Si el pod muere, esos mensajes de origen se reentregan tras la espera de confirmación del consumidor y se publican de nuevo con el mismo id de mensaje, que el stream reconoce (consulte `jetstream_publish_duplicates_total`). Es un ajuste fijo, leído al arrancar. |
| `brokerPresence.publishWindow` | `128` | Cuántas transiciones de presencia mantiene un pod esperando la confirmación del broker a la vez, de 1 a 1024. Un valor fuera de ese rango, incluido 0, detiene el servicio al arrancar; nunca se ajusta. El lector de presencia del broker esperaba cada confirmación antes de tomar la siguiente conexión o desconexión, así que procesaba unas 88 transiciones por segundo y por asunto (unos 11 ms cada una en un broker replicado) y la reconexión de 10.000 dispositivos tardaba unos dos minutos en una réplica; con 128 procesa unas 11.000 por segundo. La reconexión de N dispositivos tarda unas N veces el tiempo de una confirmación dividido por este número, repartida entre las réplicas: alrededor de un segundo para 10.000 dispositivos con 128. Vigila `devicechain_eventsources_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="presence"}`: una tasa cercana a 1 significa que esta ventana es lo que limita el lector. El broker no confirma ni reentrega los avisos de conexión, así que también es un límite de pérdida: si el pod muere, se pierden hasta tantas transiciones como hubiera aceptado y aún no confirmado, junto con los avisos que aún esperaban ser leídos, y la reconciliación las recupera en su siguiente pasada (véase más abajo). Una transición que no se puede escribir nunca se descarta en silencio: se registra y se cuenta en `devicechain_eventsources_presence_events_failed_total`, y un dispositivo cuya conexión falló no despierta sus comandos retenidos. Es un ajuste fijo, leído al arrancar. |

Una transición perdida así la repara la siguiente pasada de reconciliación, que compara las conexiones vivas del broker con el estado almacenado. La pasada se ejecuta cada `brokerPresence.reconcileSeconds` (300 por defecto) en cada réplica, y una vez al arrancar. Así que, en el peor caso, un dispositivo cuya conexión se perdió aparece **desconectado mientras está conectado** durante hasta un intervalo (cinco minutos por defecto) más lo que tarde la pasada, y uno cuya desconexión se perdió aparece conectado durante lo mismo. La primera pasada de un pod de reemplazo se ejecuta en cuanto arranca. El valor por defecto es 128 y no el 512 de los escritores de ingesta porque la presencia llega en ráfagas tan grandes como la flota, no como un flujo constante: 128 ya vacía la reconexión de 10.000 dispositivos en alrededor de un segundo, y una ventana mayor aumentaría lo que un pod que muere a mitad de ráfaga deja a la reconciliación sin hacer la reconexión apreciablemente más rápida.

`BrokerPresenceLosingTransitions` (aviso) salta cuando lleva diez minutos fallando la escritura de transiciones de presencia en el stream de eventos (`devicechain_eventsources_presence_events_failed_total` en aumento). Cada una se pierde hasta la siguiente pasada de reconciliación, así que mientras tanto hay dispositivos que pueden aparecer desconectados estando conectados. Revisa el bróker de mensajería de la plataforma, y el registro de event-sources en busca de `Failed to emit a broker presence transition`, que nombra cada dispositivo. Una transición que llega mientras el lector se detiene no se cuenta, así que una actualización progresiva no la dispara, y una ráfaga aislada se despeja sola.

`MqttGatewayStreamUnbounded` (aviso) salta cuando un stream de la pasarela MQTT del broker (`$MQTT_msgs`, `$MQTT_out` o `$MQTT_qos2in`) lleva diez minutos sin límite de tamaño y `event-sources` no ha podido ponerlo (`devicechain_eventsources_mqtt_gateway_stream_unbounded` en 1). El broker crea estos streams sin límite cuando se conecta el primer dispositivo MQTT, que en una instancia nueva ocurre mucho después de que arrancara `event-sources`; por eso cada pod los revisa cada 30 segundos y pone el límite en cuanto existen, y `devicechain_eventsources_mqtt_gateway_stream_bounded_late_total` cuenta cada vez que lo hizo. La alerta indica que no se pudo poner el límite, casi siempre porque al broker no le queda almacenamiento para reservarlo. Un stream sin límite puede llenar el almacenamiento del broker y tumbar la instancia: libere almacenamiento en el broker y busque en el registro de `event-sources` `Could not bound the MQTT gateway`.

`IntegrationAuthFailuresSustained` (aviso) salta cuando más de 30 solicitudes en cada uno de los últimos diez minutos se rechazaron en la ruta de integraciones entrantes por un token desconocido o incorrecto, o porque se agotó el presupuesto de fallos de la propia integración (`devicechain_eventsources_integration_requests_total` con `result` de `unauthorized` o `bad_auth_limited`), y lleva así diez minutos. Un servidor de red o un webhook que conservó el token antiguo tras rotarlo se ve exactamente igual, y también alguien que conoce la URL y prueba tokens. Si rotó el token, actualícelo en el remitente; si no, la hora del último fallo de autenticación de la integración indica cuál se está probando. Una integración que usted desactivó se rechaza como `disabled` y no se cuenta aquí.

`IntegrationShedAccepted` (aviso) salta cuando, en los últimos diez minutos, se descartaron envíos que llegaban por integraciones entrantes porque un inquilino supera su límite de ingesta (`devicechain_eventsources_integration_requests_total` con `result="shed_accepted"`, o `devicechain_eventsources_integration_uplinks_total` con `result="shed"`). Estas rutas responden 202 y no 429, porque un rechazo pierde el mensaje igualmente y algunos servidores de red suspenden un webhook rechazado, con lo que se perdería tráfico que estaba dentro del límite. Por eso el remitente cree que los datos llegaron y no fue así. Suba el límite de ingesta del inquilino o reduzca lo que envía la integración.

`IntegrationUndecodedHigh` (aviso) salta cuando más de la mitad de al menos 20 envíos de los últimos 30 minutos llegaron por integraciones entrantes sin nada que almacenar (`devicechain_eventsources_integration_uplinks_total` con `result="undecoded"`), y lleva así 30 minutos. La causa habitual es un perfil de dispositivo LoRaWAN del servidor de red sin códec de carga útil: el servidor reenvía la trama en bruto y la plataforma no tiene valores decodificados que leer. Configure el códec en el servidor de red; la última muestra sin decodificar de la integración muestra una trama que no pudo leer.

### Confirmación MQTT a prueba de fallos {#mqtt-fail-closed}

`MqttFailClosedDurableMissing` (crítica) y `MqttFailClosedStoreNotAsDeclared` (crítica) solo pueden saltar en una instancia con `infrastructure.nats.mqttFailClosed` activado. El ajuste está desactivado por defecto. Con él activado, un PUBACK de QoS 1 del bróker significa que la plataforma guarda el mensaje (consulta [calidad de servicio](../guides/connecting-a-device.md#quality-of-service) para lo que ve un dispositivo). `event-sources` declara el almacén de mensajes MQTT del propio bróker (`$MQTT_msgs`) para que, lleno, rechace una publicación en lugar de descartar otra que ya confirmó, mantiene en ese almacén un consumidor propio por cada tipo de publicación de dispositivo (eventos, respuestas a comandos e informes de dispositivo) y reenvía lo que leen esos consumidores. La copia de la misma publicación que llega directamente a un flujo de la plataforma se omite, así que cada publicación llega una vez; una publicación QoS 0 nunca se almacena y sigue llegando a la plataforma por el stream de captura. Cada pod comprueba el almacén y esos consumidores cada 30 segundos, vuelve a crear lo que falte y restablece lo que se haya cambiado. Un almacén que tiene que volver a crear no acepta ninguna publicación de dispositivo hasta que existen sus consumidores, así que en ese momento los dispositivos reciben un rechazo en lugar de una confirmación hacia un almacén que nadie lee. Durante una actualización progresiva que activa o desactiva el ajuste, el almacén se queda como lo quieren los pods con el ajuste anterior hasta que se detiene el último de ellos, y cambia después; al desactivar el ajuste, los consumidores se conservan hasta que se han leído por completo, así que no se pierde nada de lo que guardan. Una publicación hecha mientras el almacén cambia de manos puede llegar dos veces, nunca ninguna. El ajuste se rechaza al arrancar en un bróker que también sirve a otra instancia, porque todas las instancias de un bróker comparten el almacén.

Para activarlo, pasa `--mqtt-fail-closed` a `dcctl bootstrap <provider> <instance>`, o a `dcctl upgrade <provider> <instance>` si la instancia ya está en marcha; `--mqtt-fail-closed=false` lo desactiva de nuevo. `dcctl` registra la elección en la instancia, así que cada `dcctl upgrade` posterior la conserva sin repetir el indicador, y una instancia a la que se le dijo que siguiera desactivada sigue desactivada. Un valor puesto a mano en la configuración de la instancia no dura, porque `dcctl` vuelve a componer ese documento en cada actualización, y `--values` no acepta este ajuste. La contrapartida es la de arriba: mientras el almacén de mensajes del bróker no tiene líder, una publicación QoS 1 se rechaza en lugar de confirmarse, y la instancia sostiene menos tráfico, así que dimensiónala para mantenerte por debajo de ese techo. La garantía se enuncia para un bróker replicado, por lo que conviene instalar el clúster con `--ha`; `dcctl` lo advierte cuando activas el ajuste en un clúster instalado sin él. El ajuste necesita las áreas `event-sources` y `device-management`, que todo perfil incluye, y `dcctl` lo rechaza si al conjunto de áreas le falta alguna. Antes de cambiar nada, `dcctl upgrade` también comprueba que los techos de los streams que reserva la nueva configuración caben en el límite de almacenamiento de archivos de JetStream del bróker (una instancia conserva el volumen con el que se construyó), y lo rechaza mostrando las cifras si no caben; `--mqtt-fail-closed=false` es el camino de vuelta. La elección pertenece a la declaración de la instancia, así que `dcctl destroy` seguido de `dcctl bootstrap` vuelve a empezar desde el valor por defecto.

`MqttFailClosedDurableMissing` salta en cuanto una comprobación encuentra que falta uno de esos consumidores (`devicechain_eventsources_mqtt_failclosed_durable_missing_total` en aumento), y mientras un pod informe de uno que no pudo crear (`devicechain_eventsources_mqtt_failclosed_durable_present` a 0). Mientras falta un consumidor, el bróker confirma las publicaciones de dispositivo de su tipo sin guardarlas, así que cada una se pierde aunque al dispositivo se le dijo que había llegado. Una actualización progresiva no elimina ningún consumidor, así que no hace saltar esta alerta. Averigua qué borró el consumidor y busca en el registro de event-sources `platform durable on the MQTT message store`. `MqttFailClosedStoreNotAsDeclared` salta cuando una comprobación tuvo que restablecer un ajuste del almacén (`devicechain_eventsources_mqtt_failclosed_store_repaired_total`, por ajuste) o vio otra instancia en el bróker (`devicechain_eventsources_mqtt_failclosed_foreign_instances` por encima de 0). Un ajuste cambiado se restablece en 30 segundos, pero mientras duró, un almacén lleno pudo descartar mensajes confirmados o rechazar todas las publicaciones. Un límite de tamaño o un número de réplicas cambiado en la configuración no es un cambio que esta alerta notifique. Otra instancia en el bróker comparte el almacén y su límite de tamaño, así que da a cada instancia su propio bróker o desactiva el ajuste.

Dos mecanismos evitan que un almacén lleno se convierta en una avalancha de reconexiones. Mientras el almacén supera el 80 % de su límite de tamaño, `device-management` rechaza toda conexión MQTT nueva de dispositivo, antes de comprobar ninguna credencial, hasta que el almacén vuelve a estar por debajo del 60 %. Los dispositivos ya conectados siguen publicando, y la plataforma va vaciando el almacén. A un dispositivo rechazado se le dice que su conexión no está autorizada (código de retorno 5 de CONNACK de MQTT), porque la pasarela MQTT del bróker no puede enviar el código 3 (servidor no disponible) en este caso, así que el firmware del dispositivo debe seguir reintentando tras el código 5 en lugar de rendirse. Y cuando el bróker cierra la conexión de un dispositivo porque el almacén rechazó o no confirmó una de sus publicaciones (el motivo de la desconexión es `Protocol Violation`), el dispositivo no se da por desconectado durante 30 segundos. Un dispositivo que se reconecta en ese tiempo nunca aparece desconectado, así que la entrega de comandos no retiene sus comandos y las reglas de desconexión no saltan. Uno que no se reconecta se da por desconectado al terminar los 30 segundos, con la hora a la que se desconectó. `devicechain_eventsources_presence_disconnects_deferred_total` cuenta las desconexiones retenidas así. Una pasada de reconciliación que se ejecuta mientras un dispositivo está entre un rechazo y su reconexión todavía puede darlo por desconectado hasta que se reconecte.

`MqttStoreBackoffEngaged` (aviso) salta cuando `device-management` lleva dos minutos rechazando conexiones MQTT nuevas de dispositivo porque el almacén está casi lleno (`devicechain_devicemanagement_mqtt_store_backoff_engaged` a 1). `devicechain_devicemanagement_mqtt_store_fill_ratio` indica cuánto está lleno el almacén, y `devicechain_devicemanagement_callout_refused_store_full_total` cuenta las conexiones rechazadas. Mientras salta no se pierde nada, pero se está dejando a dispositivos fuera de la plataforma, porque la plataforma lee las publicaciones de los dispositivos más despacio de lo que los dispositivos las envían. `devicechain_eventsources_mqtt_failclosed_durable_pending{durable}` y `devicechain_eventsources_mqtt_failclosed_durable_ack_pending{durable}` muestran qué lector va retrasado: los mensajes de cada consumidor que aún no se le han entregado, y los que se le entregaron y aún no confirmó, cada 30 segundos. El consumidor de eventos lee el almacén entero y descarta lo que no es un evento, así que su recuento de pendientes incluye todos los tipos; los consumidores de respuestas a comandos y de informes de dispositivo cuentan solo los suyos. No aparecen mientras un consumidor no se puede leer, ni mientras su recuento no es fiable: el stream o el consumidor no tiene líder, una réplica está fuera de línea, o el consumidor no indica nada pendiente mientras el almacén aún guarda mensajes para él, que es lo que puede informar un servidor NATS cuya copia del almacén va retrasada tras un cambio de líder o un reinicio. Revisa `event-sources` y lo que lee el stream de eventos de entrada o, para respuestas a comandos e informes de dispositivo, `command-delivery` y `device-management`.

Limitaciones conocidas del ajuste:

- La garantía se da para un bróker replicado (`streamReplicas` 3). Con una sola réplica, la caída de un nodo puede perder mensajes que el bróker había confirmado y aún no había escrito en disco.
- Un consumidor borrado a mano en el bróker se vuelve a crear en 30 segundos. Hasta entonces, y durante unos segundos más mientras los pods vuelven a consultar el almacén, las publicaciones de su tipo se confirman y no se guardan, y por eso `MqttFailClosedDurableMissing` es crítica.
- Un mensaje por encima del techo de ingesta de su inquilino se sigue confirmando y descartando, igual que con el ajuste desactivado.
- El bróker MQTT propio del agente de borde no está cubierto. Su enlace de subida a la plataforma es una publicación QoS 1 como la de cualquier otro dispositivo, y sí está cubierto.
- El stream de captura sigue recibiendo una copia de cada publicación QoS 1, que su lector omite. Si se llena bajo sobrecarga, `JetStreamDurableLostUnread` sobre él cuenta también esas copias omitidas, así que exagera lo perdido: solo se pierden las publicaciones QoS 0 que contiene, y las QoS 1 las guarda el almacén. Por el mismo motivo, con el ajuste activado `JetStreamCaptureLeaderChanged` no pierde nada de QoS 1.
- Los pods juzgan la concesión de los demás con su propio reloj. Relojes separados más de unos 30 segundos pueden hacer que se pasen el almacén de unos a otros, lo que cuesta duplicados y no pierde nada.
- No está cubierta la caída del bróker mientras el almacén acumula un gran atraso. En una prueba de carga a 9000 eventos QoS 1 por segundo, antes de que el lector del almacén se hiciera más rápido, el almacén llegó a unos 1,5 millones de mensajes sin leer, dos de los tres servidores NATS se quedaron sin memoria y se reiniciaron, y alrededor de un tercio de los eventos confirmados nunca llegó al almacén de eventos. No se ha determinado dónde se perdieron. Mientras no se determine, no cuente con el ajuste ante caídas del bróker, y trate un almacén que crece (`devicechain_eventsources_mqtt_failclosed_durable_pending`) como algo que hay que corregir, no como algo que el almacén absorberá.
- El almacén le cuesta al bróker más por publicación que el stream de captura. Un almacén que rechaza en lugar de expulsar comprueba su límite en cada publicación, con un coste que crece con el número de dispositivos que publican a la vez, así que los PUBACK son más lentos con el ajuste activado: en una prueba a 6000 eventos por segundo, 336 ms en el percentil 99 frente a 35 ms con el ajuste desactivado.
- El mecanismo de espera mide el tamaño del almacén en bytes y solo rechaza conexiones nuevas: los dispositivos ya conectados siguen publicando. Un atraso muy por debajo del 80 % del límite puede ser grande igualmente.
- Un atraso en el propio almacén ocupa poca memoria del bróker, decenas de MiB por servidor con el almacén lleno. La memoria de cada servidor NATS se va sobre todo en las ventanas de detección de duplicados, y a 10 000 eventos por segundo sostenidos funciona cerca de su límite blando de memoria, con el ajuste activado o desactivado. Un servidor al que le falta CPU también acumula trabajo en memoria, así que si alguna vez el sistema mata un servidor por falta de memoria (OOM), aumente `nats_memory_limit`.

## Persistencia de eventos {#event-persistence}

`event-management` escribe los eventos por lotes. Cada escritor toma los eventos que ya lo esperan,
hasta un límite, y los confirma en una sola transacción, escribiendo cada tabla una vez por cada
inquilino del lote en lugar de una vez por cada evento. Los eventos de conexión y desconexión se
siguen escribiendo uno a uno dentro del lote. Un evento se reconoce solo después de que la
transacción que lo contiene se haya confirmado. Si se rechaza un evento de un lote, no se conserva
nada de esa transacción: el evento rechazado se vuelve a escribir por separado, y se reintenta o se
notifica exactamente como sin lotes. El resto del lote se vuelve a confirmar sin él. Un evento
rechazado porque su inquilino se ha eliminado es la excepción: el rechazo se aplica a todos los
eventos de ese inquilino, así que ninguno se vuelve a escribir. Se reconocen enseguida, no se
notifican en ningún sitio y se cuentan como `erased` (consulte
[Eliminación de inquilinos](./tenant-deletion.md#database-writes)). Cuando la base
de datos rechaza una fila de una sentencia que lleva varios eventos, no indica de qué evento venía,
así que primero el lote se vuelve a escribir en una transacción nueva, evento a evento, para
encontrarlo. Si la transacción falla por una causa que no se debe a ningún evento concreto, como una
conexión perdida con la base de datos, cada uno de sus eventos se vuelve a escribir por separado.

Un escritor que encuentra menos eventos esperando que un lote completo espera hasta 10
milisegundos a que lleguen más antes de confirmar (`persistence.lingerMillis`). Un escritor que
encuentra eventos ya esperando los toma enseguida, así que con cola acumulada la espera no cuesta
nada. La espera ahorra confirmaciones solo cuando todos los escritores están ocupados: un evento que
llega va a un escritor libre antes que a uno que espera para llenar su lote, así que por debajo de
unos pocos cientos de eventos por segundo por réplica cada evento se sigue confirmando solo, hasta
10 milisegundos más tarde que sin la espera. Los lotes ayudan sobre todo con carga: en un almacén de
eventos replicado, la mayor parte de cada confirmación se va en esperar a la réplica, y un lote paga
esa espera una sola vez.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_eventmanagement_persist_batch_size` | Eventos por transacción confirmada. Lotes pequeños indican que los escritores van al día. Lotes que crecen hacia el límite indican que los escritores están ocupados. Con 10 escritores compartiendo un mismo flujo, los lotes rara vez llegan al límite aunque el almacenamiento vaya retrasado, así que léala junto a la cola del consumidor. |
| `devicechain_eventmanagement_persist_batch_fallbacks_total` | Transacciones de lote que no se confirmaron, tras lo cual sus eventos se volvieron a escribir. Un aumento ocasional es un evento rechazado, o dos cuando la base de datos rechazó una fila de una sentencia que llevaba varios eventos, porque encontrar el evento requiere un segundo intento. Un ritmo constante indica que algo rechaza escrituras una y otra vez, por ejemplo un inquilino eliminado cuyos dispositivos siguen enviando: cada lote que contiene sus eventos cuesta una transacción adicional, por muchos que contenga. Esos eventos aparecen en `persist_messages_total` como `erased`. |
| `devicechain_eventmanagement_persist_inflight` | Eventos que tienen los escritores, incluidos los que esperan a que su lote se confirme. |

`persist_duration_seconds` mide cada evento desde que un escritor lo toma hasta que su lote se
confirma, incluido el tiempo, hasta `persistence.lingerMillis`, que espera a que el lote se llene,
así que con poca carga su mediana queda unos 10 milisegundos por encima que con la espera
desactivada.

### Ajustarlo

| Ajuste (configuración de `event-management`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `persistence.writers` | `10` | Escritores en paralelo. Cada uno ocupa una conexión a la base de datos mientras escribe, así que debe ser menor que el pool de conexiones del servicio (`tsdbConfiguration.maxOpenConnections`, 20 si no se indica). Se permite más de la mitad del pool, y se registra al arrancar, porque entonces las lecturas compiten con los escritores por el resto. |
| `persistence.maxBatch` | `64` | Máximo de eventos confirmados en una transacción, de `1` a `64`. `1` desactiva los lotes. |
| `persistence.lingerMillis` | `10` | Cuánto espera un escritor con un lote incompleto a más eventos antes de confirmarlo, hasta `1000`. Un escritor que encuentra eventos esperando no espera. `0` desactiva la espera y confirma lo que ya está esperando. |

Los valores por defecto son el lote más grande y la mitad del pool de conexiones por defecto. Si la
cola del consumidor de `event-management` sigue creciendo, el almacenamiento va retrasado, sea cual
sea el tamaño de los lotes (ver [Un consumidor que se queda atrás](#consumer-backlog)). Añadir
escritores no lo arregla con seguridad: reparten los mismos eventos en lotes más pequeños, y cada
confirmación cuesta CPU al almacén de eventos. En el clúster de tres nodos de las
[mediciones en que se basan estos valores](./bootstrap.md#measured-throughput), el almacenamiento
dejó de crecer cerca de 6000 eventos por segundo con lotes de unos 21 eventos de media ahí y de 28
a 30 por encima, por debajo del límite, mientras dos de los tres nodos, uno de ellos el del almacén de eventos, estaban al
86-95% de CPU; no se aisló cuál de esas dos cosas limitó el ritmo. En una medición anterior, dos
réplicas de 20 escritores cada una redujeron los lotes a unos 3 eventos, la base de datos del
almacén de eventos usó más de 4 núcleos, y el conjunto almacenó menos que una réplica de 10. Los
escritores son por réplica. Un valor fuera de rango impide que el servicio arranque, y el error
nombra el ajuste. El servicio registra los valores que usa al arrancar.

La espera de 10 milisegundos se midió en la prueba de rendimiento de la versión, en el clúster en
la nube de seis nodos de [Rendimiento medido](./bootstrap.md#measured-throughput). A 3000 eventos
por segundo, las transacciones por evento almacenado bajaron un 37% (de 0,126 a 0,080), el tiempo
mediano para almacenar un evento subió unos 5 milisegundos y el 1% más lento bajó un 18%; esa
comparación también cambió las claves del almacén de eventos y la compresión de su archivo, las solicitudes de CPU de los servicios y su colocación, los escritores de `device-state` y el límite de CPU de la detección, y las solicitudes y el límite de memoria de los servidores NATS, así que
no aísla la espera. En una ejecución de cinco minutos con 6800 eventos por segundo ofrecidos, el
camino que confirma un evento cada vez se llevó el 4,8% de la CPU de `event-management`, con 0,050
transacciones por evento, frente al 24% en la prueba anterior, con la compilación previa a la espera
y en otros nodos de servicios. Si cuesta más latencia de la que ahorra, indique
`persistence.lingerMillis: 0`.

### Relleno de métricas calculadas {#calculated-backfill}

El [relleno](../user-guide/calculated-metrics.md#backfilling-history) de un inquilino completa
métricas calculadas en el historial almacenado. Se ejecuta en `event-management`: un único
trabajador para toda la instancia, que toma un trabajo cada vez por turnos entre inquilinos, a un
ritmo acotado. Sin trabajos cuesta un intento de bloqueo y dos consultas pequeñas cada diez
segundos. Un relleno solo escribe filas de mediciones. No publica nada en el broker y nunca llega
al estado en vivo ni a la detección.

| Ajuste (configuración de `event-management`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `calculatedBackfill.enabled` | sin definir | Tres estados. **Sin definir**: el trabajador se ejecuta cuando el pool de conexiones tiene sitio para él junto a los escritores de persistencia (`persistence.writers` + 1 + 4 conexiones libres deben caber en `tsdbConfiguration.maxOpenConnections`) y se queda apagado, con una advertencia al arrancar que nombra los ajustes, cuando no. **`true`**: pides el trabajador, así que un pool que no puede alojarlo detiene el servicio al arrancar. **`false`**: el trabajador no se ejecuta. Iniciar un relleno se rechaza entonces como no disponible, con el motivo. Ese motivo lo leen los usuarios de los inquilinos, así que solo dice que el trabajador está apagado o que el pool no tiene sitio para él; los ajustes y los tamaños del pool están en la advertencia de arranque, no en el motivo. La consola consulta `calculatedMetricBackfillAvailability` al abrir la página (sin sondeo) y desactiva el control desde el principio con ese mismo motivo, así que un cambio de este ajuste se ve en la consola la próxima vez que se abra la página; un trabajo que estaba en curso sigue en curso hasta que se vuelva a activar el trabajador o se cancele el trabajo. |
| `calculatedBackfill.rowsPerSecond` | `20000` | El máximo de filas que el trabajador lee más escribe por segundo, entre todos los trabajos, de `1` a `200000`. Este valor por defecto es provisional y no se ha medido en una instancia con carga: en una instancia ocupada empieza bajo y vigila la CPU del almacén de eventos. |

El trabajador mantiene una conexión del pool durante toda su vida. El intervalo de un trabajo (90
días), el tamaño de lote y la regla de un trabajo activo por inquilino son fijos.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_eventmanagement_calc_backfill_rows_written_total` | Filas de mediciones insertadas. |
| `devicechain_eventmanagement_calc_backfill_samples_total` | Lecturas evaluadas. |
| `devicechain_eventmanagement_calc_backfill_skipped_total{reason}` | Valores no calculados, por el motivo de omisión de la fórmula, más `ambiguous`. |
| `devicechain_eventmanagement_calc_backfill_windows_skipped_total{why}` | Tramos sin tocar: `compressed`, o `retention` (al alcance de la política de retención). |
| `devicechain_eventmanagement_calc_backfill_jobs_finished_total{status}` | Trabajos terminados, por estado. |
| `devicechain_eventmanagement_calc_backfill_oldest_active_job_age_seconds` | Antigüedad del trabajo en cola o en curso más antiguo; `0` si no hay. Un trabajo atascado la hace crecer sin límite, así que conviene alertar sobre ella. |
| `devicechain_eventmanagement_calc_backfill_batch_seconds`, `..._rollup_refresh_seconds` | Tiempo para procesar un lote y para refrescar el agregado de mediciones sobre un tramo. |
| `devicechain_eventmanagement_calc_backfill_rollup_refresh_conflicts_total` | Refrescos que coincidieron con el propio trabajo de política del agregado y se reintentaron. |

Cada toma de un trabajo, final de porción y estado final también se registra una vez, con el
inquilino, el token del trabajo, el perfil, la versión, los recuentos y cualquier fallo.

#### Detener un relleno {#calculated-backfill-stop}

Para detener **todos** los rellenos en cola y en curso, conéctese a la base de datos del almacén de
eventos y ejecute:

```sql
UPDATE "event-management".calculated_backfills
   SET cancel_requested_at = now()
 WHERE status IN ('QUEUED', 'RUNNING');
```

Añada `AND tenant_id = '<tenant>'` para detener solo los de un inquilino. El trabajador lo advierte
en su siguiente confirmación y cada trabajo termina como `CANCELLED` en un lote. Las filas ya
escritas se conservan, y un trabajo en cola que ningún trabajador ha tomado termina igual cuando el
trabajador se ejecuta de nuevo. Para detener el propio trabajador, defina
`calculatedBackfill.enabled: false` y reinicie `event-management`.

### Estado en vivo de los dispositivos {#live-state-projection}

`device-state` mantiene el estado en vivo de cada dispositivo (conectividad, actividad, últimas
lecturas y última posición) a partir del mismo flujo de eventos, y los fusiona de la misma manera:
cada escritor toma los eventos que ya lo esperan, hasta un límite, y los fusiona en una sola
transacción. Dentro de esa transacción, el estado de todos los dispositivos que ya tienen uno se
escribe en una sola sentencia por cada inquilino del lote, en lugar de una sentencia por
dispositivo; un dispositivo que aparece por primera vez se crea por separado. Un evento se reconoce
solo después de que esa transacción se confirme. Varios eventos de un mismo dispositivo en un lote
dejan exactamente lo que dejaría fusionarlos de uno en uno. Una lectura o una posición sustituye a
la almacenada solo si es estrictamente más reciente, así que una más antigua o igual de antigua
nunca la sobrescribe; las horas se comparan tal como las guarda la base de datos, al microsegundo.
Si se rechaza la parte de un lote que corresponde a un inquilino, los eventos de ese inquilino se
vuelven a fusionar de uno en uno, de modo que solo se reintenta un evento que es rechazado por sí
mismo, y los eventos de los demás inquilinos se confirman juntos sin ellos. Si la transacción falla
por una causa que no se debe a ningún inquilino, como una conexión perdida con la base de datos,
cada uno de sus eventos se vuelve a fusionar por separado.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_devicestate_state_batch_size` | Eventos por transacción confirmada. Si casi siempre es `1`, los escritores van al día. |
| `devicechain_devicestate_state_batch_fallbacks_total` | Transacciones de lote que no se confirmaron, tras lo cual sus eventos se volvieron a fusionar. Un ritmo constante indica que las escrituras de un inquilino se rechazan una y otra vez, por ejemplo las de un inquilino eliminado cuyos dispositivos siguen enviando. |
| `devicechain_devicestate_state_write_conflict_retries_total` | Escrituras de un solo evento que se repitieron en el momento tras abortarlas PostgreSQL como víctima de un interbloqueo o por un fallo de serialización. Un ritmo que sigue subiendo indica que el barrido de inactividad y los escritores colisionan a menudo; un evento cuyos reintentos se pierden todos queda para reentrega y se cuenta como `retry` en los resultados de mensajes. |
| `devicechain_devicestate_state_inflight` | Eventos que tienen los escritores, incluidos los que esperan a que su lote se confirme. |

`state_duration_seconds` mide cada evento desde que un escritor lo toma hasta que su lote se
confirma.

| Ajuste (configuración de `device-state`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `projection.writers` | `10` | Escritores en paralelo; cada uno ocupa una conexión a la base de datos mientras fusiona. Debe ser menor que `rdbConfiguration.maxOpenConnections` (20 si no se indica). |
| `projection.maxBatch` | `32` | Máximo de eventos fusionados en una transacción, de `1` a `64`. `1` desactiva los lotes. |
| `projection.lingerMillis` | `0` | Cuánto espera un escritor a más eventos antes de fusionar un lote incompleto, hasta `1000`. `0` fusiona lo que ya está esperando. |

En una base de datos replicada, lo que sostiene el rendimiento son los lotes. Las fusiones de un
mismo dispositivo se esperan entre sí, así que en una flota pequeña cuyos dispositivos envían por
turnos, más escritores pueden significar más lotes esperando a los mismos dispositivos. En un
clúster en la nube de tres nodos con unos 1700 a 1900 dispositivos, 5 escritores se quedaron atrás a
partir de unos 6800 eventos por segundo. Los 10 escritores se eligieron en una ejecución que también
aumentó la solicitud de CPU del servicio y `projection.maxBatch` a 64, así que no se separó la parte
de cada cambio.
El número de escritores por defecto es 10, la mitad del pool de conexiones por defecto, y
`projection.maxBatch` sigue en 32 por defecto: en esa ejecución los lotes promediaron unos 15, así
que 32 no limitó en promedio. Auméntelo más solo si los lotes van llenos y la base de
datos tiene margen. El servicio registra los valores que usa al arrancar. Si el
estado en vivo sigue quedándose atrás, `JetStreamDurableFallingBehind` salta para el consumidor de
`device-state` (consulte [Un consumidor que se queda atrás](#consumer-backlog)).

Un evento que `device-state` no logra aplicar en su último intento de entrega se confirma y se
descarta en lugar de registrarse en ningún sitio, porque el estado en vivo se puede reconstruir a
partir del historial de eventos. Se cuenta como `dropped` en los
[resultados de los mensajes](#message-results).
`DeviceStateDroppingEvents` (warning) salta en cuanto se cuenta uno, y se desactiva 15 minutos
después del último. Cada evento descartado dejó desactualizada la conectividad, una lectura o la
última posición de su dispositivo hasta que llegue un evento más reciente de ese dispositivo; el
historial de eventos almacenado no se ve afectado. La causa habitual es que la base de datos del
servicio rechaza o falla las escrituras durante más tiempo que la ventana de reentrega. Revise la
base de datos y, en el registro de `device-state`, `Giving up on`, que indica la escritura y el
dispositivo.

## Replicación {#replication}

Una instancia de alta disponibilidad necesita dos cosas: flujos de JetStream creados con el
número de réplicas para el que está configurada la instancia, y un clúster de NATS lo bastante
grande para alojarlos. Cualquiera de las dos puede fallar aunque la configuración parezca
correcta. Por eso cada servicio informa cada 30 segundos de lo que el bróker dice sobre cada flujo
y cada bucket KV que usa, y cinco avisos vigilan el resultado:

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamNotReplicatedAsConfigured` | warning | Durante 15 minutos, un flujo ha tenido menos réplicas de las configuradas para la instancia, según el servicio indicado por la etiqueta `job`. La instancia no es de alta disponibilidad para ese flujo. | Asegúrese de que el clúster de NATS tiene servidores suficientes para `instance.config.infrastructure.nats.streamReplicas`. El aumento de réplicas solo se ejecuta cuando un servicio arranca, así que, cuando el clúster sea lo bastante grande, reinicie los Deployments afectados. |
| `JetStreamReplicaPeersDegraded` | warning | Durante 20 minutos, un flujo ha tenido menos copias actualizadas y en línea que réplicas. Perder su líder puede perder datos o disponibilidad. La espera es larga porque las réplicas recién añadidas copian los datos del flujo antes de contar como actualizadas, y porque con tasas de eventos altas y sostenidas una copia puede quedarse brevemente atrás; consulte más abajo. | Revise el estado y la ubicación de los pods de NATS. Tres réplicas en pods que comparten un nodo no sobreviven a la pérdida de ese nodo. |
| `JetStreamLeaseBucketNotReplicated` | critical | En una instancia configurada para más de una réplica, el bucket que decide qué pod puede escribir tiene menos de tres réplicas. Perder su servidor impide que cualquier réplica en espera tome el relevo. | Igual que para `JetStreamNotReplicatedAsConfigured`. No confíe en la conmutación por error mientras esté activo. |
| `JetStreamClusterUnused` | warning | El bróker está en clúster, pero todos los flujos están configurados para una réplica, así que la instancia ejecuta varios servidores de NATS y no sobrevive a la pérdida de ninguno. | Ajuste `streamReplicas` al clúster (`dcctl install --ha` fija ambos), o reduzca el clúster de NATS si lo que quería era un solo servidor. |
| `JetStreamReplicationUnobserved` | warning | Durante 15 minutos, un pod en ejecución no ha podido leer el estado de replicación de un flujo que antes sí podía leer. Mientras está activo, los avisos anteriores no pueden juzgar ese flujo para ese pod, así que su replicación es desconocida, no correcta. | Si se activa para todos los flujos a la vez, el bróker o JetStream no está disponible: revise los pods de NATS y los registros de conexión del servicio. Si es un solo flujo, lo más probable es que ese flujo haya perdido su líder: inspecciónelo con `nats stream info`. |

`JetStreamReplicationUnobserved` tiene límites que conviene conocer:

- Se activa para **todos los flujos en todos los pods** durante una caída del bróker. Es
  deliberado: el aviso es correcto, y ningún otro aviso del chart informa de que el bróker no es
  accesible. Agrupe sus notificaciones por nombre de aviso si son demasiadas.
- Vigila cada pod por separado, así que una réplica que pierde un flujo lo activa aunque otra
  réplica del mismo servicio todavía pueda leerlo. Un pod sustituido, y una versión que deja de
  usar un flujo, no lo activan.
- Solo ve un flujo que el pod ha leído al menos una vez. Un pod que no ha podido leer un flujo
  desde que arrancó no lo activa, y eso incluye un pod de sustitución creado después de que
  empezara el problema. Un contenedor reiniciado en el mismo pod (tras un fallo, una terminación
  por falta de memoria o una sonda de actividad fallida) conserva el nombre del pod, así que lo que
  leyó el proceso anterior sigue contando y el aviso se sigue activando.
- Se resuelve seis horas después de la última lectura del flujo por el pod, aunque el flujo siga
  sin poder leerse. Una notificación de resolución no demuestra la recuperación.
- Un pod que no está en ejecución no exporta nada, así que no puede activarlo. Los avisos de
  salud de los pods cubren ese caso.

Con tasas de eventos altas y sostenidas, `JetStreamReplicaPeersDegraded` puede pasar a pendiente y
volver a despejarse sin que nada vaya mal. Una copia de uno de los flujos con más tráfico se queda
un momento por detrás de su líder, cuenta como no actualizada hasta que lo alcanza, y después lo
alcanza. En dos pruebas de 10 minutos a 6000 eventos por segundo con un bróker de tres servidores,
el flujo con más tráfico tuvo una copia no actualizada en aproximadamente una de cada cinco
comprobaciones, y nunca durante más de unos segundos seguidos, y el aviso pasó a pendiente y se
despejó repetidas veces. Solo se activa cuando un flujo lleva 20 minutos sin todas sus copias
actualizadas en cada comprobación, así que un retraso de este tipo lo despeja en lugar de activarlo.
Para comprobar la replicación, ejecute `dcctl ha verify`: sigue comprobando durante hasta 90
segundos (`--settle`) antes de informar de un fallo, así que una copia que va brevemente por detrás
no lo hace fallar, y pasó al ejecutarlo después de cada una de esas pruebas.

Los avisos leen estas series, que exporta cada servicio que usa JetStream:

- **`devicechain_<area>_jetstream_replicas_desired{stream}`**, **`_replicas_actual{stream}`** y
  **`_peers_current{stream}`**: el número de réplicas configurado, el que informa el bróker y las
  copias actualizadas y en línea. Un servicio que no puede leer un flujo elimina las tres para ese
  flujo en lugar de seguir informando de sus últimos valores.
- **`devicechain_<area>_jetstream_broker_clustered`**: 1 cuando el bróker conectado está en
  clúster y 0 en otro caso, también mientras el servicio está desconectado. Existe mientras el pod
  está en ejecución.

Ningún aviso lee la serie siguiente, pero es la que hay que mirar cuando publicar va lento:

- **`devicechain_<area>_jetstream_publish_duration_seconds{suffix, mode}`**: cuánto tardó cada
  publicación en un flujo de JetStream, desde que se envía hasta que el servicio actúa sobre la
  confirmación del bróker o sobre su fallo, según el flujo al que se envió. `mode="sync"` es una
  publicación que el servicio esperó sola. `mode="pipelined"` es una de varias en curso a la vez
  (los eventos resueltos que publica `device-management`, y los eventos de dispositivo que
  `event-sources` reenvía de lo que los dispositivos publican por MQTT en el bróker de la
  plataforma), y su tiempo incluye además la espera a
  que se resuelvan todas las publicaciones anteriores, y a la pausa que el servicio hace tras una
  fallida, así que los dos modos no se comparan directamente. Una publicación `mode="sync"` que el
  bróker nunca respondió se cuenta en el límite de 5 segundos, así que para ese modo la cuenta por
  encima del bucket `le="5"` son las publicaciones que llegaron a él. Una publicación
  `mode="pipelined"` puede contarse por encima de 5 segundos sin haber llegado al límite.
- **`devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix, writer}`**: segundos en que un
  escritor en pipeline no pudo enviar porque toda su ventana esperaba confirmaciones. Una tasa
  cercana a 1 significa que la ventana, y no el broker, limita la publicación: suba
  `resolution.publishWindow` (`device-management`) o `capture.publishWindow` (`event-sources`).
  Una tasa cercana a 0 significa que la ventana tiene espacio. `writer` indica qué escritor fue,
  porque varios comparten un mismo sufijo: `resolution` (`device-management`), `capture`,
  `external-mqtt` y `presence` (`event-sources`) y `sparkplug` (`sparkplug-ingest`). Todas las
  fuentes MQTT externas cuentan bajo `external-mqtt`. Una consulta que no nombra `writer` puede usar
  `sum by (suffix)` para leer el total.
- **`devicechain_<area>_jetstream_publish_window_full_total{suffix, writer}`**: cuántos envíos encontraron
  la ventana llena y tuvieron que esperar. Los segundos anteriores divididos por esta cifra son la
  espera media.
- **`devicechain_<area>_jetstream_publish_duplicates_total{suffix, writer}`**: publicaciones que el broker
  confirmó como copias de un mensaje que ya tenía con el mismo id de mensaje. El mensaje se
  guardó una sola vez. Tras un reinicio del pod o una confirmación perdida, que esta cifra suba es
  el stream absorbiendo la republicación, no un fallo.

## Cachés que dejan de responder {#kv-caches}

`device-management` guarda las búsquedas que repite para cada evento (un dispositivo por su
token, las relaciones seguidas del dispositivo, el perfil publicado de su tipo y las pertenencias
a grupos) en buckets de clave-valor de NATS. Cada búsqueda espera como máximo medio segundo. Un
bucket que no responde a tiempo, o por el que no responde ningún servidor, se omite durante cinco
segundos: sus búsquedas van directamente a la base de datos, que contiene los mismos datos, y
después se vuelve a intentar una búsqueda. Solo cuando esa búsqueda obtiene respuesta el bucket
deja de omitirse. El servicio registra una advertencia cuando un bucket se omite por primera vez
(`A key-value cache stopped answering`) y una línea cuando vuelve a responder (`A key-value cache
is answering again`), con cuánto tiempo pasó y cuántas búsquedas y escrituras fueron a la base de
datos mientras tanto. Un error con el que responde el bucket, como un bucket lleno que rechaza una
escritura, se cuenta pero no hace que se omita.

La causa habitual es un servidor NATS que se ha caído de la red sin cerrar sus conexiones. Todas
las réplicas de un bucket responden lecturas, así que hasta que los demás servidores notan el
silencio, lo que tarda entre un minuto y un minuto y medio, parte de las lecturas se envía al
servidor que ya no está. Durante ese tiempo los eventos se siguen resolviendo, a costa de más
lecturas de la base de datos.

Cada réplica de `device-management` también guarda en memoria lo que leyó de un bucket, o escribió
en él, durante hasta cinco minutos (`inMemoryCache.ttlSeconds`, y nunca más que el tiempo de vida
de la caché), y responde desde ahí sin preguntar a NATS, incluso mientras el bucket se está
omitiendo. El tiempo cuenta desde que se leyó el valor, no desde la última vez que se usó. Las
tres cachés que se guardan por dispositivo (un dispositivo por su token, sus relaciones seguidas y
sus pertenencias a grupos) guardan cada una hasta 131.072 entradas o 24 MiB por réplica, según
`inMemoryCache.perDeviceCacheEntries` e `inMemoryCache.perDeviceCacheMiB`. Eso son unos 87.000
dispositivos sin ninguna relación seguida, o unos 26.000 con una. Las cachés de perfil y de alcance
de grupos, que se guardan por tipo de dispositivo y por inquilino, guardan 4096 entradas o 4 MiB.
Cada una descarta primero la entrada usada hace más tiempo cuando está llena. Al guardar una
entrada nueva también descarta las caducadas desde su extremo de uso más antiguo, y se detiene en
la primera que no ha caducado. Una búsqueda que NATS informó como ausente nunca se guarda.

**Un cambio llega a todas las réplicas a la vez, y el tiempo solo cubre un mensaje perdido.** Un
cambio que deja incorrecto un valor guardado (un dispositivo actualizado o borrado, una relación
añadida o quitada, un perfil publicado o revertido, cambios en los miembros de un grupo, un
inquilino borrado) quita el valor del bucket y luego envía un mensaje en NATS que hace que cada
réplica de `device-management` descarte su copia. Una réplica que pierde la conexión con NATS y la
recupera descarta todo lo que guarda, porque puede haberse perdido mensajes. Así que una réplica
sirve un valor cambiado durante más de un instante solo si se perdió un mensaje mientras estaba
conectada, por ejemplo porque iba demasiado atrasada para recibirlo, y entonces solo hasta que
se agote el tiempo. Lo mismo ocurre con una lectura que estaba en curso al producirse el cambio y
guardó lo que había leído antes de él. Los mensajes se cuentan en `cache_eviction_broadcasts_total`.

**Credenciales de dispositivo.** Cada réplica guarda también en memoria una credencial de
dispositivo que acaba de verificar, con su dispositivo, durante cinco minutos desde que la leyó.
Nunca se guarda en un bucket de clave-valor, porque contiene la contraseña de la credencial. Cada
réplica guarda hasta `inMemoryCache.credentialCacheMiB` de ellas (32 MiB por defecto). Cada
comprobación de una credencial guardada compara su contraseña y su expiración igual que la de la
almacenada, y una credencial que no se pudo verificar nunca se guarda. Un cambio en una credencial
o en su dispositivo descarta la copia en la réplica que lo hace, y un mensaje en NATS avisa a las
demás réplicas para que descarten la suya. Así que una revocación, o un dispositivo borrado a
través de otra réplica, surte efecto en el siguiente evento en todas, y cuando caduque la copia si
ese mensaje se pierde. Las conexiones MQTT siempre leen la base de datos.

**Dimensionar para la flota.** Un valor se guarda en memoria cinco minutos desde que se leyó, así
que un dispositivo que informa al menos con esa frecuencia se responde desde memoria en todos sus
eventos salvo el primero. Un dispositivo que informa con menos frecuencia nunca se responde desde
memoria, y cada uno de sus eventos cuesta una lectura del bucket de clave-valor y otra de la base
de datos para su credencial. Dimensione cada réplica para toda la flota activa, no para la flota dividida entre las
réplicas: el flujo de entrada es un único consumidor duradero compartido por todas las réplicas, así
que los eventos no se reparten entre réplicas por dispositivo, y cuando el número de réplicas por el
intervalo de informe de un dispositivo es menor que el tiempo, cada réplica ve casi todos los
dispositivos. Una caché menor que la flota la recorre en ciclo y casi no responde nada. Por ejemplo,
60.000 dispositivos que informan cada 10 s, cada uno con una relación seguida, necesitan en cada
réplica un `inMemoryCache.credentialCacheMiB` de unos 80 (una entrada ocupa unos 1,1 KB) y un
`inMemoryCache.perDeviceCacheMiB` de unos 64 (un conjunto de relaciones ocupa unos 950 bytes), y un
límite de memoria de 768Mi para alojarlos. La señal es
`kv_cache_local_lookups_total{cache="relationships-by-source", result="miss"}` cerca del ritmo de
eventos, mientras `kv_cache_local_entries` de esa caché se mantiene muy por debajo de
`kv_cache_local_max_entries`. Una flota demasiado grande para la caché muestra en cambio
`kv_cache_local_evictions_total{reason="capacity"}` creciendo cerca del ritmo de eventos, con
`kv_cache_local_entries` en `kv_cache_local_max_entries` o `kv_cache_local_bytes` en
`kv_cache_local_max_bytes`; en las credenciales, las mismas señales están en
`credential_cache_evictions_total{reason="capacity"}`. El chart avisa de esto con
`DeviceCredentialCacheTooSmall` y `DeviceLookupCacheTooSmall` (aviso): una réplica cuyas expulsiones
por capacidad llevan 15 minutos por encima del 5% de sus consultas. Cuando salte una, suba el límite
(`inMemoryCache.credentialCacheMiB` o `inMemoryCache.perDeviceCacheMiB`), y con él el límite
de memoria: con los valores por defecto las seis cachés en memoria guardan como máximo 112 MiB.
`device-management` se ejecuta con `GOGC=400` y un límite blando de memoria (`GOMEMLIMIT`) al 75 %
de su límite de memoria, de modo que el recolector deja crecer el heap hasta unas cinco veces lo
que guarda, y trabaja más al acercarse a ese límite blando; donde no hay `GOMEMLIMIT`, el heap
puede crecer hasta aproximadamente el doble de lo que guarda antes de recolectarse. Para resolver una flota más rápido también puede añadir resolvedores
(`resolution.workers`, dentro del pool de conexiones) o réplicas de `device-management`.

Eliminar una entrada tras un cambio (un dispositivo borrado, un perfil publicado) nunca se omite.
Espera hasta cinco segundos, porque solo el líder del bucket puede aceptarlo. Si aun así falla, el
servicio registra `A key-value cache eviction failed`, y la entrada antigua puede servirse hasta
que caduque, que es el tiempo de vida configurado de la caché, o el tiempo en memoria en las
réplicas que ya la tenían, el que sea menor.

- **`devicechain_devicemanagement_kv_cache_unavailable{cache}`**: 1 mientras el bucket se está
  omitiendo.
- **`devicechain_devicemanagement_kv_cache_failures_total{cache, op, reason}`**: operaciones que
  agotaron el tiempo (`reason="timeout"`) o fallaron (`reason="error"`). Las consultas de un evento
  se hacen a la vez, así que cuando un bucket deja de responder, varias pueden agotar el tiempo
  juntas antes de que se omita, y cada una cuenta aquí.
- **`devicechain_devicemanagement_kv_cache_bypassed_total{cache, op}`**: búsquedas y escrituras que
  fueron a la base de datos en su lugar.
- **`devicechain_devicemanagement_kv_cache_request_duration_seconds{cache, op}`**: cuánto tardó
  cada operación en el bucket. Una búsqueda o una escritura se corta a medio segundo, una
  eliminación a cinco segundos. Una búsqueda respondida desde memoria nunca llega al bucket, así
  que `op="get"` cuenta solo las búsquedas que la memoria no pudo responder.
- **`devicechain_devicemanagement_kv_cache_local_lookups_total{cache, result}`**: búsquedas
  respondidas desde memoria (`result="hit"`) o pasadas al bucket (`result="miss"`).
- **`devicechain_devicemanagement_kv_cache_local_evictions_total{cache, reason}`**: entradas
  descartadas de la memoria porque habían superado su tiempo (`reason="expired"`), porque la caché
  estaba llena (`reason="capacity"`) o porque la entrada se eliminó tras un cambio
  (`reason="deleted"`).
- **`devicechain_devicemanagement_kv_cache_local_entries{cache}`** y
  **`devicechain_devicemanagement_kv_cache_local_bytes{cache}`**: cuántas entradas, y
  aproximadamente cuántos bytes, guarda una réplica en memoria para la caché. Las entradas
  caducadas cuentan hasta que una búsqueda las encuentra, la caché las descarta desde su extremo
  de uso más antiguo al guardar una entrada nueva, o la caché necesita el espacio.
- **`devicechain_devicemanagement_kv_cache_local_max_entries{cache}`** y
  **`devicechain_devicemanagement_kv_cache_local_max_bytes{cache}`**: cuántas entradas, y cuántos
  bytes, guarda como máximo la caché en memoria antes de descartar la usada hace más tiempo.

Una caché creada sin la copia en memoria no tiene ninguna de las seis series `kv_cache_local_`.
Hoy todas las cachés de `device-management` la tienen.

La copia de las credenciales tiene series propias:

- **`devicechain_devicemanagement_credential_cache_lookups_total{result}`**: comprobaciones de
  credenciales respondidas desde memoria (`result="hit"`) o pasadas a la base de datos
  (`result="miss"`).
- **`devicechain_devicemanagement_credential_cache_evictions_total{reason}`**: credenciales
  descartadas de la memoria porque habían superado su tiempo (`reason="expired"`), porque la copia
  estaba llena (`reason="capacity"`) o porque la credencial o su dispositivo cambiaron, en esta
  réplica o en otra (`reason="revoked"`).
- **`devicechain_devicemanagement_credential_cache_entries`**,
  **`devicechain_devicemanagement_credential_cache_bytes`**,
  **`devicechain_devicemanagement_credential_cache_max_entries`** y
  **`devicechain_devicemanagement_credential_cache_max_bytes`**: lo que guarda una réplica, y lo
  máximo que guarda.
- **`devicechain_devicemanagement_cache_eviction_broadcasts_total{cache, result}`**: los mensajes
  que avisan a las demás réplicas para que descarten una copia, enviados (`result="published"`),
  no enviados (`result="publish_failed"`), recibidos (`result="received"`) o recibidos y
  descartados por ilegibles (`result="malformed"`). Un `publish_failed` constante significa que los
  cambios llegan a las demás réplicas solo cuando caducan sus copias.

Por otra parte, resolver un evento que tarda más de cinco segundos, por la razón que sea, se
registra como advertencia (`Event resolution is slow`): la primera vez de inmediato y después como
máximo una línea cada 30 segundos, con cuántas hubo y la más lenta.

## Llamadas de inferencia de IA {#ai-inference}

Donde la creación asistida por IA está habilitada (consulte
[Autoría Asistida por IA](../concepts/ai-authoring.md)), el servicio `ai-inference` cuenta lo que
sus inquilinos le piden. Ambas series están acotadas: sus etiquetas toman un conjunto fijo de
valores, nunca un inquilino, un modelo o un proveedor, así que no crecen con el número de inquilinos.

- **`devicechain_aiinference_ai_inference_calls_total{outcome}`**: solicitudes de inferencia de los
  inquilinos, según cómo terminó cada una.
- **`devicechain_aiinference_ai_inference_tokens_total{direction}`**: tokens que el proveedor informó
  haber usado, por `direction` (`input` u `output`). Es la señal de gasto: ninguna otra métrica de la
  plataforma cuenta tokens. Es un contador para vigilar, no un libro contable, y nada se hace cumplir
  a partir de él.

| Resultado | Significado |
| --- | --- |
| `served` | El proveedor respondió y se devolvió un candidato. |
| `rate_limited` | El inquilino superó su límite de tasa de inferencia y la llamada se rechazó antes de llegar a ningún proveedor. Un aumento sostenido indica que un inquilino está por encima de su cuota. |
| `consent_required` | El inquilino no ha aceptado el enrutamiento externo de IA, así que nada salió de la plataforma. Es lo esperado para un inquilino sin consentimiento concedido. |
| `unavailable` | La plataforma no está configurada para atender la llamada: sin modelo asignado, proveedor deshabilitado, falta una clave. El registro del servidor indica cuál. |
| `provider_error` | Se llegó al proveedor y este falló la llamada (error de transporte, un estado de error, una respuesta vacía). La dependencia externa no funciona bien. |
| `timed_out` | Se llegó al proveedor y no respondió dentro de `inferenceTimeoutMs` (40 segundos por defecto, 120 como máximo). El autor ve un tiempo agotado en lugar de un fallo genérico. |

Lea `timed_out` y `provider_error` por separado. Un `provider_error` creciente indica que el
proveedor está fallando. Un `timed_out` creciente con `provider_error` estable indica que el modelo
en uso es más lento de lo que permite el tiempo límite: elija un modelo más rápido o aumente
`inferenceTimeoutMs`, que la configuración de `ai-inference` del chart expone y que impide arrancar
si supera su máximo.

## Pasadas de mantenimiento {#maintenance-passes}

Varios servicios ejecutan una tarea de mantenimiento con un temporizador: un barrido, un
reconciliador o un planificador. Cada una exporta tres series con el nombre de su propio servicio,
`devicechain_<area>_<task>_…`:

- **`<task>_passes_total{outcome}`**: pasadas realizadas, por resultado.
- **`<task>_pass_duration_seconds`**: cuánto tardó una pasada. Una pasada `skipped` no se cronometra.
- **`<task>_last_success_timestamp_seconds`**: la hora Unix de la última pasada que hizo su trabajo
  (`complete` o `partial`). Vale NaN hasta la primera pasada así, de modo que una regla como
  `time() - X > umbral` no se dispara en un pod que acaba de arrancar.

| Servicio | Tareas |
| --- | --- |
| user-management | `dead_letter_sweep`, `tenant_purge` |
| notification-management | `retention_sweep`, `escalation_scheduler` |
| event-management | `anchor_sweep` |
| device-state | `inactivity_sweep` |
| event-sources | `presence_demote`, `presence_reconcile`, `presence_canary` |
| device-management | `ownership_drain` |
| command-delivery | `command_sweep`, `hold_reconcile`, `stranded_reconcile` |
| event-processing | `detect_tick`, `rule_health_flush`, `shed_summary` |
| lwm2m-ingest | `tenant_sweep` |
| sparkplug-ingest | `presence_probe` |

Tres de ellas solo se ejecutan mientras algo las impulsa: `detect_tick` y `tenant_sweep` de
lwm2m-ingest en la réplica que tiene el liderazgo, y `presence_probe` una vez por cada sesión
Sparkplug conectada, con todas las sesiones en una sola serie. En una réplica en espera, o
mientras sparkplug-ingest no tiene sesión, la hora del último éxito sigue en NaN o conserva el
valor de su último mandato. Aplique `max by (job)` entre réplicas antes de compararla con `time()`.

Por ejemplo, el coordinador de eliminación de inquilinos de user-management exporta
`devicechain_usermanagement_tenant_purge_passes_total`.

| Resultado | Significado |
| --- | --- |
| `complete` | La pasada se ejecutó y terminó su trabajo. |
| `partial` | La pasada se ejecutó e hizo parte de su trabajo, por ejemplo algunos inquilinos pero no otros. |
| `failed` | La pasada no pudo hacer su trabajo. |
| `skipped` | Otra réplica tiene el bloqueo de la tarea, así que esta no se ejecutó, como corresponde. No es un fallo, y no mueve la hora del último éxito. |
| `cancelled` | La pasada se interrumpió porque el servicio se estaba deteniendo. No es un fallo. |

Genere avisos sobre `failed` y sobre una hora de último éxito que ha dejado de avanzar, no sobre
`skipped` ni `cancelled`: un servicio con más de una réplica omite la pasada en todas las réplicas
menos una, y cada despliegue puede cancelar una pasada. Los barridos de user-management,
notification-management y event-management empiezan cada pasada en un momento aleatorio dentro de
un 10 % a cada lado de su intervalo, para que las réplicas que arrancaron juntas no lleguen a la
base de datos todas a la vez.

Una tarea puede completar sus pasadas sin fallos mientras el trabajo para el que existe está
atascado. La eliminación de inquilinos es el caso para el que el chart tiene avisos propios; vea
[Eliminación de inquilinos](./tenant-deletion.md#stalled-alert).

## Respaldos que dejan de enviarse {#backup-archiving}

Los respaldos de una base de datos fallan en silencio. El archivado se ejecuta junto a las
escrituras, no en su camino, así que un archivo del log de escritura anticipada que no puede
llegar a su destino no ralentiza nada ni hace fallar ninguna comprobación de salud. Pero
PostgreSQL conserva cada segmento que no ha enviado, en el propio volumen de la base de datos, y
solo los guarda el primario: las réplicas no ayudan. Cuando ese volumen se llena, PostgreSQL se
detiene, y el operador de la base de datos no hace failover ante un disco lleno. La cadena
habitual es: se llena el almacén de objetos de los respaldos, falla el archivado y después se
llena el volumen del primario.

Las alertas del chart para cada eslabón de esa cadena:

| Alerta | Se dispara cuando | Qué hacer |
| --- | --- | --- |
| `PostgresWALArchivingFailing` | El último intento de archivado falló más recientemente que el último que tuvo éxito, durante 5 minutos. | Comprueba que el almacén de objetos es accesible, tiene espacio y acepta las credenciales. |
| `PostgresWALArchiveBacklog` | Una base de datos tiene más de 32 segmentos de log terminados (512 MiB) esperando a enviarse, durante 5 minutos. Incluye un archivador lento o bloqueado que no registra ningún fallo. | Si también se dispara la alerta de fallo, arregla el destino. Si no, revisa los logs del sidecar de respaldos y el espacio libre del almacén, y amplía el volumen de la base de datos si la cola sigue creciendo. |
| `BackupDestinationFillingFast` | El almacén de objetos interno está lleno en más de un 65 % y, al ritmo de los últimos 10 minutos, se llenará en menos de una hora. | Averigua qué escribe: una instancia que ingiere más rápido de lo previsto para el almacén, respaldos de instancias que ya no existen o una programación de respaldos base que se detuvo, de modo que nada se poda. Amplía el almacén, elimina lo que no pertenezca a ninguna instancia en marcha o saca los respaldos del clúster. |
| `BackupDestinationAlmostFull` | El almacén de objetos interno está lleno en más de un 85 %, durante 15 minutos. | Lo mismo, con menos tiempo. |
| `PostgresHasNeverArchivedWAL` | Una base de datos nunca ha enviado un segmento de log, durante una hora. El primer respaldo base fuerza un cambio de segmento, así que un clúster que funciona sale de este estado a los pocos minutos de crearse; una hora en él significa que el archivado nunca ha funcionado aquí, no que la base de datos esté tranquila. No registra ningún fallo, así que la alerta de archivado fallido no puede verlo: un archivador que se bloquea en lugar de dar error se ve exactamente así. | Revisa los logs del sidecar de respaldos y que el almacén de objetos es accesible. |
| `PostgresWALArchivingNotConfigured` | Una base de datos tiene el archivado activado y ningún plugin de respaldo detrás, durante 15 minutos. Cada segmento de log se «archiva» con un comando que lo descarta e informa de éxito, así que nada más te avisa: el contador de archivados sube, no se registra ningún fallo, el disco no crece y todas las demás alertas de respaldo siguen en verde. **La base de datos no se puede restaurar a ningún punto en el tiempo.** | Comprueba que el Cluster sigue teniendo su entrada barman-cloud en `spec.plugins` y que existe su ObjectStore. |
| `DatabaseVolumeFillingFast` | Un volumen del almacén de eventos está lleno en más de la mitad y, al ritmo de los últimos 15 minutos, se llenará en menos de una hora, durante 3 minutos. | Si solo sube el primario, es log sin enviar: arregla primero el archivado. Si suben todos los miembros, son datos: amplía el volumen o acorta la retención de datos. |
| `DatabaseVolumeFillingUp` | Un volumen de base de datos está lleno en más de un 75 %, durante una hora. El almacenamiento de la base de datos aquí no se recupera por sí solo: varias tablas son de solo anexado por diseño y nada las poda (el diario de auditoría y los historiales de versiones de perfiles de dispositivo, grupos de entidades, paneles, conectores y conjuntos de geocercas, además del archivo de geometrías de geocercas), porque un evento almacenado nombra la versión contra la que se resolvió y una reversión vuelve a una versión almacenada. Los ajustes de retención no se aplican a ellas; esos podan el almacén de series temporales. | Amplía el volumen mientras haya margen para trabajar y averigua qué está produciendo versiones. En el primario relacional, cuenta las filas por inquilino en una tabla de historial, por ejemplo `SELECT tenant_id, count(*) FROM "device-management".geo_fence_set_versions GROUP BY tenant_id ORDER BY 2 DESC LIMIT 10;`. Lo habitual es un inquilino que reescribe en bucle. Para un volumen del almacén de eventos, revisa los ajustes de retención del ciclo de vida de los datos; para un volumen `-wal`, la causa está en las alertas de archivado de arriba. |
| `DatabaseVolumeAlmostFull` | Un volumen de base de datos está lleno en más de un 85 %, durante 15 minutos. | Amplíalo ya. |

Las dos alertas de ritmo existen porque los umbrales fijos son demasiado lentos para un volumen
que se llena en minutos, que es lo que la ingesta sostenida hace con él. Se comprobaron con una
prueba de rendimiento en la que el primario del almacén de eventos pasó del 45 % a lleno en unos
nueve minutos después de que se llenara el almacén de objetos: la alerta de ritmo se dispara unos
dos minutos antes de que el volumen se llene, mientras que la alerta del 85 %, que espera 15
minutos, solo se habría disparado después.

El almacén de objetos lo comparten todas las instancias del clúster, igual que la base de datos
relacional. El chart de cada instancia lleva estas alertas, así que una alerta sobre un volumen
compartido o sobre la base de datos compartida aparece una vez por instancia. El almacén
predeterminado está dimensionado para una instancia con ingesta continua: con más, puede llenarse
antes que cualquier almacén de eventos, y estas alertas son el aviso.

Para saber qué tamaño necesita el almacén interno, consulta
[Tamaño del almacén de objetos de respaldo](./bootstrap.md#backup-store-size).

### Respaldos base como instantáneas de volumen {#snapshot-backup-alerts}

En un clúster instalado con
[`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups), el respaldo base diario es una
instantánea de volumen y el del almacén de respaldos es semanal. `PostgresNoRecentBaseBackup`, que
vigila los respaldos base del almacén de respaldos, espera entonces 8,5 días en lugar de 36 horas,
y se generan tres alertas más:

| Alerta | Se dispara cuando | Qué hacer |
| --- | --- | --- |
| `PostgresNoRecentSnapshotBackup` | Una base de datos no ha completado ninguna instantánea en 36 horas, o su programación de instantáneas no ha completado nunca ninguna, durante una hora. | Lee los eventos del ScheduledBackup `<cluster>-snapshot` y de sus Backups más recientes. Una VolumeSnapshotClass que se borró, o que pertenece a otro controlador de almacenamiento distinto del de los volúmenes de las bases de datos, hace fallar todas las instantáneas. Mientras tanto, la recuperación a un punto en el tiempo sigue funcionando desde el respaldo base semanal. |
| `DatabaseSnapshotPruningStalled` | El operador de DeviceChain lleva más de una hora sin terminar de podar una programación de instantáneas, durante 30 minutos. | Comprueba que el operador está en marcha, en la versión con la que se instaló el clúster, y lee los eventos del ScheduledBackup. Si el operador está en marcha y su log no muestra ningún error `snapshot retention pass failed`, pero el ScheduledBackup nunca ha llevado la anotación `devicechain.io/snapshot-retention-checked-at`, y sus eventos no muestran ningún aviso `SnapshotRetentionIgnored` (una programación cuyo método no es `volumeSnapshot`), el operador no lo está seleccionando: solo actúa sobre ScheduledBackups con la etiqueta `app.kubernetes.io/component: database-snapshot-backup` que llevan `devicechain.io/snapshot-retention`, en un namespace que creó DeviceChain. Hasta que vuelva a funcionar, se conservan todas las instantáneas, cada una una copia completa de la base de datos. |
| `DatabaseSnapshotBackupsUnobserved` | Los respaldos como instantáneas están activados, pero la monitorización no ve ninguna programación de instantáneas, durante una hora. | Comprueba que existen los ScheduledBackup de instantáneas, y que el kube-state-metrics de la pila de monitorización lee los Backups y ScheduledBackups de CloudNativePG. Hasta entonces, ninguna de las dos alertas anteriores puede dispararse. |

Estas alertas leen los objetos Backup y ScheduledBackup a través de kube-state-metrics, no el
indicador de respaldos del propio CloudNativePG, que solo se establece en el primario de cada base
de datos.

## El plano de control de la base de datos {#database-control-plane}

Una base de datos puede estar sana y aun así no poder hacer failover. El operador de PostgreSQL
(CloudNativePG) y su plugin de respaldos deciden qué instancia pasa a ser el primario. Un Cluster
cuyo plugin el operador no puede alcanzar deja de reconciliarse, así que nunca se promueve: el
primario puede perderse estando sano, hay réplicas completamente al día esperando y nada elige una.
Todos los pods de instancia siguen en `Running` todo el tiempo, y ninguna de las alertas de respaldo
de arriba se dispara, porque nada de PostgreSQL va mal. También existe el fallo contrario: un Cluster
que informa `Ready` mientras todos los servicios están bloqueados fuera de él. Estas cinco alertas
cubren ambos casos y los huecos de monitorización que los esconderían. Se generan cuando se le indica
al chart el namespace del operador de bases de datos.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `CNPGClusterNotReady` | critical | Un Cluster de PostgreSQL ha informado `Ready=False` durante 15 minutos. **Esto bloquea el failover.** Un Cluster pasa por `Ready=False` durante un switchover, un reinicio progresivo y su primer arranque, pero una recuperación tarda segundos, así que un cuarto de hora no es una operación en curso. Una fase de «Cluster cannot proceed to reconciliation» apunta al operador o al plugin de respaldos, no a PostgreSQL. | Lee el `.status.phase` y las condiciones del Cluster (la alerta nombra el Cluster y el motivo). Si la fase menciona plugins, revisa `CNPGControlPlaneUnavailable`. |
| `CNPGControlPlaneUnavailable` | critical | Un Deployment del namespace del operador de bases de datos lleva 10 minutos sin réplicas disponibles. Con el operador caído, ningún Cluster puede hacer failover, escalar ni reconfigurarse, y las bases de datos siguen sirviendo sin gestión. Con el plugin de respaldos caído, el operador no puede reconciliar ningún Cluster que lo referencie, así que se aplica el mismo bloqueo de failover. | Restaura el Deployment que nombra la alerta. Espera que `CNPGClusterNotReady` la siga en un cuarto de hora si no lo haces. Reiniciar kube-state-metrics reinicia el temporizador de esta alerta, así que una caída puede notificarse más tarde de lo que empezó. |
| `CNPGClusterUnusable` | critical | Durante 5 minutos, al menos una instancia de un Cluster no puede ser consultada por su propio recolector de métricas, mientras que los pods y la condición `Ready` del Cluster pueden parecer sanos. La alerta no puede decir qué instancia. Si es el primario, los clientes se ven afectados, y la causa más probable es el **agotamiento de conexiones**: cada servicio con base de datos abre un pool de hasta 20 contra el primario, una actualización progresiva duplica brevemente los pods, y entonces los servicios fallan con «remaining connection slots are reserved for roles with the SUPERUSER attribute». Si es una standby, los clientes no se ven afectados y una réplica está fallando. | Como superusuario, que sigue funcionando mientras esto se dispara, compara `SELECT count(*) FROM pg_stat_activity` con `SHOW max_connections` en cada instancia. Cambiar `max_connections` es en sí un reinicio progresivo, que la espera de 5 minutos absorbe. |
| `CNPGCollectorAbsent` | warning | Durante 10 minutos nada ha informado esa serie del recolector en un namespace de bases de datos, así que no se sabe si las instancias pueden responder, y `CNPGClusterUnusable` no puede dispararse porque lee la misma serie. | Comprueba que existe el PodMonitor de CloudNativePG para el Cluster y que selecciona sus pods, que Prometheus puede leer el namespace, y que los pods de instancia están en marcha y sirven el puerto 9187. |
| `CNPGClusterStateUnobserved` | warning | Durante 30 minutos nada ha informado las condiciones de los Cluster en un namespace de bases de datos, así que se desconoce la salud del failover, y `CNPGClusterNotReady` no puede dispararse. Es un hueco de monitorización, pero el que esconde un bloqueo de failover. | Comprueba que kube-state-metrics está en marcha, que se inició con una configuración de custom-resource-state (el módulo de monitorización de OpenTofu la establece cuando CloudNativePG está activado), y que su rol puede listar `postgresql.cnpg.io/clusters`. Un permiso ausente solo se muestra como una línea `forbidden` en su log. |

## Entrega de comandos {#command-delivery-alerts}

Tres alertas cubren los estados en los que la entrega de comandos informa algo falso mientras nadie
mira. El servicio exporta muchos más contadores, listados en la
[referencia de métricas](./metrics-reference.md#command-delivery), y un panel de Command Delivery
los representa. Los umbrales están razonados en lugar de medidos, y cada uno se comprueba en ambas
direcciones con pruebas unitarias.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `CommandClaimsStranded` | critical | En los últimos 30 minutos, durante 5 minutos, al menos un comando se quedó leyéndose como `SENT` después de que fallaran tanto su publicación como la liberación de vuelta a `QUEUED`. Nunca se entregó. En LwM2M el reconciliador de varados lo rearma en unos diez minutos y se entrega cuando el dispositivo despierta; en MQTT nada lo reintenta, y caduca como `TIMEOUT`, lo que registra el fallo contra el dispositivo aunque el dispositivo nunca recibió nada. | Trata cada `TIMEOUT` de MQTT notificado en esta ventana como sin atribuir, y no envíes ingenieros a inspeccionar los dispositivos nombrados. Busca una caída del broker que coincidiera con una caída de la base de datos: hacen falta ambas para varar una reclamación. En LwM2M, `stranded_recovered_total{disposition="PARKED"}` confirma el rearme, mientras que `CANCELLED` significa que el lote del comando se había cancelado y la fila se limpió. |
| `CommandPresenceReadErrors` | warning | Durante 15 minutos, la pasada de despacho no ha podido leer la proyección de presencia. La puerta de presencia, que retiene los comandos de los dispositivos ausentes, **falla en abierto** por diseño, así que los comandos se están despachando sin la comprobación. Nada más lo muestra: el despacho parece normal y el contador de retenciones cae a cero, lo que se lee como «ningún dispositivo está ausente» y no como «no se está comprobando nada». | Revisa el servicio device-state y la salud de la proyección de presencia. Hasta que se arregle, un panel de retenciones tranquilo no tranquiliza. |
| `CommandBatchReserveRefusals` | warning | Durante 30 minutos, se han rechazado escrituras de flota que habrían cabido en el techo de comandos sin entregar del propio inquilino, y solo las rechazó el margen que la maquinaria de entrega reserva para sí misma. Nada está roto y se avisó a cada llamante; el techo y la reserva no coinciden sobre lo que es una escritura de flota normal. | Sube el techo del inquilino o baja la reserva, de forma deliberada. Los contadores no llevan etiqueta de inquilino, así que el panel muestra qué códigos y qué cota pero no qué inquilino. Encuentra el inquilino en los lotes de comandos registrados: un lote que se expandió parcialmente registra un rechazo `HELD_CEILING_EXCEEDED` por dispositivo, mientras que uno rechazado entero no registra nada. |

## Actualizaciones por aire {#update-alerts}

Cuatro alertas cubren los estados en los que una actualización no llega y nadie lo está mirando: el
dispositivo simplemente no informa, y la primera persona en notarlo es quien pregunta por qué una
flota sigue en la versión anterior. Solo existen en una instancia que ejecuta el área
update-management. Los contadores que las alimentan están en la
[referencia de métricas](./metrics-reference.md#update-management). Ninguno lleva etiqueta de
inquilino, así que toda alerta es de la instancia entera; las pantallas de Actualizaciones de la
consola muestran qué inquilino y qué despliegue. Los umbrales están razonados, no medidos, y cada
uno se comprueba en ambas direcciones con pruebas unitarias.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `UpdateDownloadsNotConfigured` | warning | Durante 15 minutos, los dispositivos han pedido una descarga y todas las solicitudes se rechazaron porque `updates.downloadBaseUrl` no está configurada. Ninguna actualización asignada puede descargarse, y nada más lo muestra: las asignaciones se crean y luego no avanzan hasta su plazo. | Define `functionalAreas.update-management.config.updates.downloadBaseUrl` con la URL pública por la que los dispositivos llegan al servicio. Si no se define se deriva del host del ingress, así que solo queda vacía si se fijó una cadena vacía o la instancia no tiene ingress. Las asignaciones continúan en el siguiente reintento de cada dispositivo. Si las descargas están desactivadas a propósito, cancela las asignaciones: un dispositivo sigue preguntando mientras haya una vigente. |
| `UpdateReportsMostlyRefused` | warning | Durante 15 minutos, con al menos 20 informes en la ventana, más de una cuarta parte de los informes de progreso de los dispositivos se rechazaron por un motivo distinto de ser duplicados, obsoletos o tardíos: un digest que no es el del objetivo, el componente equivocado, una secuencia que retrocede, un informe en conflicto. Los informes de inventario no cuentan, porque a un dispositivo que se reconecta sin nada asignado se le responde `UNKNOWN_ASSIGNMENT` o `UNCONFIRMED` por diseño. Es un error del lado del dispositivo, normalmente una compilación que informa mal en toda una flota. | Lee `update_reports_total` por `verdict` para ver qué rechazo domina y luego el registro de update-management, que nombra el dispositivo y el intento. Pausa el despliegue que alimenta esa flota mientras investigas. |
| `UpdateRolloutStepsFailing` | warning | Durante 15 minutos, el controlador de despliegues ha estado fallando pasos y dejándolos para la siguiente pasada. Los despliegues de grupo figuran como en curso pero no despachan a dispositivos nuevos, y la pausa por umbral de fallos no ayuda porque cuenta fallos de dispositivos. | Las causas son un error de base de datos durante un paso, un documento de perfiles compatibles ilegible en el artefacto, o que device-management sea inalcanzable o no esté configurado para update-management. La etiqueta `stage` de `update_rollout_step_failures_total` distingue un paso fallido (`step`) de una consulta fallida de los despliegues en curso (`list`); el registro de update-management nombra el paso que falla. |
| `UpdateAttemptsGoingUnknown` | warning | En los últimos 30 minutos, cinco o más intentos pasaron a `UNKNOWN` porque el dispositivo dejó de informar durante la instalación o el reinicio. La actualización puede haberse aplicado o no. Unos pocos son normales; una ráfaga indica que los dispositivos fallan a la vez, a menudo por una imagen defectuosa. El umbral de cinco es un recuento absoluto, no una proporción de la flota; edita la regla según el tamaño de tu flota. | Pausa el despliegue. Comprueba si los dispositivos afectados vuelven a informar, ya que su inventario resuelve el intento; para los que nunca lo hagan, decide con `abandonUnknownUpdate`. |

## Autorregistro de dispositivos {#self-registration-alerts}

Tres alertas cubren el endpoint por el que los dispositivos se registran solos
(`POST /api/device-management/provisioning/{tenant}/register`). Los contadores en que se basan están
en la [referencia de métricas](./metrics-reference.md#device-management). Los umbrales están razonados,
no medidos, y cada uno se comprueba en ambos sentidos con pruebas unitarias.

| Alerta | Gravedad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `ProvisioningAuthFailuresSustained` | aviso | Durante 15 minutos se ha rechazado más de una solicitud de autorregistro por segundo porque la clave o el secreto de aprovisionamiento no eran válidos. Todas las causas se cuentan juntas y se responden igual, a propósito: un tenant desconocido, una clave desconocida, un secreto incorrecto y un perfil deshabilitado o caducado. Una tasa sostenida es una flota que se registra con un secreto incorrecto o rotado, o alguien que sondea el endpoint. | Revisa el perfil de aprovisionamiento que debería usar la flota: habilitado, sin caducar, secreto vigente (rótalo si se filtró). Mira los orígenes en el registro del ingress. Para un sondeo no hace falta nada más: el secreto son 256 bits aleatorios y no se puede adivinar. |
| `ProvisioningRateLimited` | aviso | Durante 15 minutos se han rechazado solicitudes de autorregistro con `429` a más de una cada diez segundos. La etiqueta `result` indica qué límite: `rate_limited_source` es una dirección de cliente que agotó su cupo, `rate_limited_tenant` son los registros por segundo del tenant en su techo de gobernanza. Los dispositivos esperan el `Retry-After` que se les indica y reintentan. | Para `rate_limited_source`, comprueba que `infrastructure.http.trustedProxies` nombra al controlador de ingress y que este conserva la dirección del cliente. Sin ello, todos los dispositivos comparten un único cupo. Para `rate_limited_tenant`, sube el techo de aprovisionamiento del tenant o reparte la tirada de fábrica. |
| `ProvisioningMisconfigured` | aviso | En los últimos 10 minutos se rechazó al menos una solicitud por un perfil de aprovisionamiento que no puede funcionar: se borró su tipo de dispositivo, su estrategia no es una que la plataforma conozca, o emite un tipo de credencial que esta compilación no admite. Todos los dispositivos que usan ese perfil se rechazan hasta que se corrija. | Localiza el perfil en el registro del servicio (el token se registra con nivel `ERROR`) y corrígelo o recréalo. |

## Bloqueos del segundo factor {#mfa-alerts}

Dos alertas vigilan la [autenticación en dos pasos](../guides/two-factor-authentication.md). Un código solo lo adivina alguien que ya superó el paso de la contraseña, así que estas alertas tratan de contraseñas, no de códigos. Los contadores que las alimentan están en la [referencia de métricas](./metrics-reference.md#user-management).

| Alerta | Gravedad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `MfaIdentityLocked` | critical | El autenticador de una cuenta se bloqueó tras veinte códigos incorrectos, cada uno después de una contraseña correcta. Alguien tiene esa contraseña y está adivinando. Se dispara con una sola ocurrencia. | Encuentra la cuenta en el registro de auditoría (`mfa_locked`, tras filas `mfa_failed` y `mfa_challenged`) y **restablece su contraseña**; no restablezcas primero su segundo factor, porque entregaría la cuenta a quien está adivinando. Las direcciones de origen están en los registros del ingress. Consulta los [bloqueos](../guides/two-factor-authentication.md#lockouts). |
| `MfaVerificationFailuresSustained` | warning | Durante quince minutos se han rechazado códigos incorrectos, repetidos o bloqueados a más de uno cada diez segundos. O bien muchas personas se equivocan al escribir en gran número (un problema de reloj en los nodos, o aplicaciones desincronizadas) o alguien usa contraseñas que ha obtenido. | Comprueba que los relojes de los nodos estén sincronizados y busca filas `mfa_failed` agrupadas por cuenta y los registros del ingress. |

## Puertas de seguridad que se apagaron solas {#safety-gates}

Algunos servicios llevan una puerta de seguridad que depende de otro servicio, y cada una arranca
**apagada**, con un único aviso al inicio, cuando falta su configuración. Es una concesión deliberada
de disponibilidad: el plano de dispositivos no debe depender de forma rígida de esos servicios.
También es invisible, porque el trabajo sigue fluyendo y nada da error mientras se reanuda aquello
que la puerta existe para impedir. Una instalación con el chart siempre aporta la configuración, así
que una puerta apagada indica una instalación mal configurada o editada a mano.

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `SafetyGateDisabled` | warning | Durante 5 minutos, un servicio nombrado por la etiqueta `job` ha funcionado con la puerta nombrada por la etiqueta `gate` apagada. Basta una réplica que arrancó sin su configuración, porque ya está apagada para su parte del tráfico. Con `tenant_lifecycle` apagada, se admite trabajo de inquilinos eliminados. Con `presence` apagada, los comandos a dispositivos ausentes se publican y se descartan. Con `rule_validation` apagada, las reglas de detección que no compilan pueden publicarse y se omiten al cargarse. | El servicio registró un aviso al inicio nombrando lo que falta: el secreto del servicio, o la dirección del servicio que consulta la puerta. Restáuralo y reinicia el servicio. |

## Perfilar un servicio {#profiling}

Las métricas muestran cuánto trabajo hace un servicio y cuánto tarda. No muestran en qué se va el
tiempo dentro de él. Para eso, un servicio puede servir los perfiles del runtime de Go: un perfil
de CPU, los perfiles de heap y de asignaciones, un volcado de goroutines y una traza de ejecución.
Los perfiles se sirven en un listener propio del servicio, separado de su puerto HTTP.

### Por qué está desactivado

Los perfiles exponen el interior del servicio, incluidos nombres de funciones, pilas de llamadas y
dónde asigna memoria. Un perfil de CPU o una traza también consumen CPU mientras duran. El listener
es una herramienta de medición, no una fuente de monitoreo, así que permanece desactivado hasta que
lo actives para el servicio que quieras medir.

Cuando está activado, escucha por defecto en la dirección de loopback del pod, `127.0.0.1:6060`.
El listener nunca es un puerto del contenedor, un puerto del Service ni una ruta del ingress. No
tiene autenticación propia. Solo puede alcanzarlo quien tenga permiso del clúster para hacer
port-forward al pod, así que el acceso lo controlan los permisos del clúster, no DeviceChain.

### Activarlo

Se configura por servicio bajo `functionalAreas`. Solo se reinician los pods de ese servicio:

```yaml
functionalAreas:
  device-management:
    profiler:
      enabled: true
```

- **Instalada directamente con el chart de Helm:** añade el bloque a tus valores y ejecuta
  `helm upgrade` como de costumbre.
- **Instalada con `dcctl bootstrap`:** `dcctl` no tiene una opción para esto. Cambia la release
  con `helm upgrade`, usando la versión del chart que ya ejecuta la instancia. `helm list -n default`
  muestra esa versión, y la release se llama `dc-<instance>`:

  ```bash
  helm get values dc-<instance> -n default -o yaml > dc-values.yaml
  # añade a dc-values.yaml el bloque profiler de arriba
  helm upgrade dc-<instance> oci://ghcr.io/devicechain-io/charts/devicechain \
    --version <chart-version> -n default -f dc-values.yaml
  ```

  Esto cambia solo la configuración de los pods, no el Secret de configuración de la instancia
  que gestiona `dcctl`. El siguiente `dcctl upgrade` o `dcctl bootstrap` recalcula la release y
  vuelve a desactivar el listener.

La configuración se rechaza al instalar si el servicio no está desplegado, porque no haría nada.
También se rechaza si la dirección usa un puerto que el pod del servicio ya sirve. Una vez en
marcha, el pod registra `Profiling listener is ON` con la dirección.

### Capturar un perfil

Reenvía un puerto local al pod y apunta a él las herramientas de Go. Necesitas una instalación de
Go en tu propia máquina; las imágenes de los servicios no la incluyen.

```bash
kubectl -n dci-<instance> port-forward deploy/device-management 6060:6060

# un perfil de CPU de 30 segundos, abierto en el navegador
go tool pprof -http=:8081 'http://127.0.0.1:6060/debug/pprof/profile?seconds=30'

# el heap, guardado para abrirlo después
curl -o heap.pb.gz http://127.0.0.1:6060/debug/pprof/heap

# una traza de ejecución de 5 segundos
curl -o trace.out 'http://127.0.0.1:6060/debug/pprof/trace?seconds=5'
go tool trace trace.out
```

Un port-forward a un Deployment llega a **uno** de sus pods. Cuando un servicio ejecuta más de una
réplica, reenvía al pod que quieres por su nombre (`kubectl port-forward pod/<name> …`).

| Ruta bajo `/debug/pprof/` | Qué es |
| --- | --- |
| `profile?seconds=N` | Perfil de CPU, de 1 a 60 segundos (30 por defecto) |
| `trace?seconds=N` | Traza de ejecución, de 1 a 60 segundos (1 por defecto) |
| `heap` | Heap vivo, muestreado (`?gc=1` ejecuta antes una recolección) |
| `allocs` | Todas las asignaciones desde que arrancó el proceso, muestreadas |
| `goroutine` | La pila de cada goroutine (`?debug=2` para la forma de texto completa) |
| `threadcreate` | Pilas que crearon hilos del sistema operativo |

`?debug=1` devuelve un perfil instantáneo como texto en lugar del formato binario.

### Qué no sirve, y por qué

- **`block` y `mutex` responden 404.** Su muestreo está desactivado en estos servicios, así que
  estarían siempre vacíos, y un perfil vacío se leería como "sin contención" cuando no se midió
  nada.
- **Se rechaza un perfil de heap o de asignaciones sobre una ventana de tiempo (`?seconds=` en
  ellos).** Toma dos instantáneas y compáralas con `go tool pprof -base first.pb.gz second.pb.gz`.
- **`cmdline` y `symbol` no se sirven.** Los perfiles llevan sus propios símbolos.
- **Un perfil de CPU y una traza a la vez.** Una segunda petición de cualquiera de ellos responde
  409 hasta que termina la primera.
- **Un perfil en curso cuando el servicio se apaga se abandona**, para que el apagado no lo espere.
  Un perfil de CPU responde 503. A una traza se le corta la conexión, así que tu herramienta
  informa de un error en lugar de guardar una traza que parece completa.
- **Una descarga que deja de leerse se corta.** Un perfil debe llegarte en los 10 segundos
  siguientes al tiempo que pediste (`?seconds=` más 10). A un cliente que se detiene más tiempo,
  como un `kubectl port-forward` atascado o un `curl` suspendido, se le cierra la conexión, así
  que no puede mantener la traza en marcha. Al apagarse, se cierra cualquier conexión que siga
  abierta un segundo después de que el listener empiece a detenerse.
- **Cada perfil se sirve en una conexión propia.** La respuesta cierra su conexión, así que una
  herramienta que reutiliza conexiones abre una nueva para la siguiente petición.

### Otra dirección

`profiler.address` fija dónde escucha el listener. Debe ser una dirección IP y un puerto, por
ejemplo `0.0.0.0:6060` para un perfilador que recoge datos desde otros pods. El servicio se niega a
arrancar con un nombre de host, sin puerto o con su propio puerto 8080.

:::warning Cualquier dirección que no sea loopback queda abierta a la red del clúster
El listener no tiene autenticación, y el chart no genera ninguna network policy que limite quién
puede conectarse a un servicio. Así que en una dirección que no sea loopback, cualquier cosa que
alcance la IP del pod puede leer sus perfiles, salvo que añadas tu propia network policy de
entrada. El servicio registra un aviso cuando arranca en una dirección así.
:::

### Desactivarlo

Pon `enabled: false` o quita el bloque, y actualiza la release igual que al activarlo. Solo se
reinician los pods de ese servicio.

## Relacionado

- **[Arrancar una instancia](./bootstrap.md#install)** — `dcctl install`, el comando que
  despliega la pila de monitoreo, y sus indicadores (flags).
- **[Despliegue y operador](./kubernetes-operator.md)** — cómo el chart genera
  las cargas de trabajo por servicio con sus sondas de salud.
