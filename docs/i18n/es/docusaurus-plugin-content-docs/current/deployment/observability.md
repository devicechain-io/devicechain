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
La pila de monitoreo (kube-prometheus-stack a través de `dcctl install`) y el panel de operaciones
de event-processing están implementados y validados de extremo a extremo. Un panel de command-delivery
y sus reglas de alerta se incluyen junto a ellos. Los paneles para las demás áreas funcionales y el
trazado distribuido OTLP son mejoras planeadas a futuro.
:::

## Lo que expone cada servicio

Cada servicio de área funcional se instrumenta a sí mismo con métricas de cliente de Prometheus y
sirve las dos sondas estándar de Kubernetes:

- **`/healthz`** — vitalidad (liveness): ¿puede el proceso seguir haciendo su trabajo, o necesita un
  reinicio? Falla una vez que la conexión del servicio con el broker de mensajería se ha cerrado de
  forma definitiva, de modo que Kubernetes reinicia el pod. Un bucle que lee mensajes de un flujo y
  no puede leer durante dos minutos seguidos también termina el proceso, que Kubernetes reinicia
  después; antes de eso reintenta con una pausa creciente de hasta cinco segundos.
- **`/readyz`** — disponibilidad (readiness): ¿está listo para recibir tráfico? Un servicio que no está
  listo se mantiene fuera de rotación por su Service de Kubernetes (consulte
  [Despliegue y operador](./kubernetes-operator.md)).

Debido a que cada pod habla las mismas convenciones, la pila de monitoreo recolecta métricas de
toda la instancia de manera uniforme; no hay trabajo de integración por servicio.

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

## Iniciar sesión en Grafana

Grafana usa su propio **inicio de sesión de administrador**. Iniciar sesión en Grafana a
través del inicio de sesión único de DeviceChain no está disponible actualmente.

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
está haciendo?" se puede responder de un vistazo.

## Mensajes que un consumidor nunca leyó {#unread-loss}

Cada flujo (stream) de JetStream tiene un límite. Cuando un flujo está lleno descarta sus mensajes
**más antiguos** para hacer sitio, de modo que la ingesta sigue funcionando. Un consumidor que aún no
había leído un mensaje cuando se descartó no lo leerá nunca. El broker no lo informa, así que cada
servicio lo mide para cada consumidor duradero que lee, y tres alertas vigilan el resultado:

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamStreamNearFull` | warning | Un flujo lleva 10 minutos por encima del 80% de su límite de bytes. Todavía no se ha perdido nada. Cubre los flujos de todos los servicios. | Busque un consumidor que se esté quedando atrás. Si el tráfico simplemente ha superado el flujo, aumente su límite. |
| `JetStreamDurableLostUnread` | critical | Un consumidor pasó por encima de mensajes que se eliminaron antes de que los leyera. Nunca se procesaron. | Si en ese momento se estaba eliminando un tenant, es lo esperado: la eliminación borró mensajes a los que el consumidor aún no había llegado. Si no, el flujo estaba lleno mientras este consumidor iba atrasado. O bien el límite es demasiado pequeño para el tráfico, o bien el consumidor es más lento que su productor. |
| `JetStreamDurableStalledBehindStream` | critical | Un consumidor lleva al menos dos minutos sin recibir ningún mensaje, y el flujo ya ha descartado mensajes por delante de él. Un consumidor que está leyendo, aunque sea despacio, no dispara esta alerta; sus pérdidas disparan `JetStreamDurableLostUnread`. | El servicio está en marcha, ya que es él quien lo informa, pero su consumidor no lee. Busque un procesamiento de mensajes bloqueado en una dependencia, como la base de datos, o pods esperando a estar listos. Si no se puede arreglar rápido, aumente el límite del flujo para que deje de descartar. |

Los límites son `streamMaxBytes` (los flujos de alto volumen), `streamMaxBytesCold` (los demás) y
`streamMaxMsgs`, bajo `instance.config.infrastructure.nats`. El volumen de JetStream se dimensiona a
partir de su suma, así que aumente el volumen junto con ellos (consulte
[Arrancar una instancia](./bootstrap.md)).

Las alertas leen dos series, que cada servicio exporta para cada consumidor duradero que lee:

- **`devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`** cuenta los
  mensajes que el consumidor pasó por encima sin leerlos. Es un límite inferior: una reentrega, o un
  mensaje eliminado por detrás del consumidor, hace que cuente menos, nunca más.
- **`devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`** es cuántos
  mensajes se han descartado por delante de un consumidor que no ha recibido ningún mensaje desde la
  muestra anterior (cada 30 segundos). Vale 0 mientras el consumidor lee, aunque vaya atrasado: esas
  pérdidas son del contador. Vuelve a 0 cuando el consumidor lee de nuevo, y el contador anterior
  toma el relevo.

Ambas existen con valor 0 desde que el servicio crea el lector del consumidor. Todas las réplicas de un servicio informan
del mismo consumidor y cuentan la misma pérdida, así que combínelas con `max`, no con `sum`. Reiniciar
un pod pone el contador a cero, así que léalo con `increase()` o `rate()`. Cada pod mide desde su
propia primera muestra, así que una pérdida que el consumidor pasa por encima mientras todos los pods
del servicio lector se reinician a la vez puede quedar sin contar. Un servicio sin pods en marcha no
informa ninguna de las dos series, así que ninguna alerta puede dispararse por él; la advertencia de
flujo casi lleno y sus alertas de salud de los pods cubren ese caso.

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
| `JetStreamDurableFallingBehind` | warning | Un consumidor ha tenido más de 10000 mensajes esperándole durante 15 minutos. Todo lo que ese servicio deriva del flujo va así de atrasado: en `device-state`, el estado en vivo de un dispositivo va por detrás de sus eventos almacenados. No cubre el consumidor de detección de `event-processing`, porque lo vigila `DetectConsumerBacklogHigh` y una toma de control lo vuelve a leer por diseño. | Compare el ritmo del consumidor con el del flujo. Si mantiene el paso pero no recupera, dele capacidad (en `device-state`, consulte [sus ajustes](#live-state-projection) y su base de datos). Si se ha detenido, `JetStreamDurableStalledBehindStream` y los registros del servicio indican por qué. Si el flujo se llena antes de que se ponga al día, se descartarán los mensajes a los que aún no ha llegado. |

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
| `ReplayCoveredDeliveriesExhausted` | Un consumidor que lee su stream desde su propio punto de control agotó intentos de entrega en los últimos 15 minutos, porque el punto de control lleva sin guardarse más tiempo del que el broker sigue reentregando. Todavía no se ha perdido nada. | Corrija lo que impide al servicio que indica la etiqueta `job` guardar su punto de control, normalmente su conexión a la base de datos. Mientras el servicio sigue en marcha, guarda lo que ha leído en cuanto el punto de control se guarda. Si se reinicia antes, vuelve a leer el stream desde el último punto de control guardado, y los eventos que el stream ya haya descartado no se pueden volver a leer, así que vigile también `JetStreamStreamNearFull`. |

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
| `ConnectorDispatchRateLimited` | warning | Durante 15 minutos, outbound-connectors ha descartado envíos por superar la tasa de salida de su inquilino. El motor de detección mide el mismo techo sobre la misma línea de tiempo y descarta las acciones que lo superan antes de enviarlas, así que estas se admitieron en un extremo y se rechazaron en el otro. Un inquilino por encima de su techo no dispara este aviso; dispara `ReactConnectorEgressShedding` en el motor de detección. | Compruebe que la tasa de salida por defecto de la plataforma (`outboundMessagesPerSecond` y `outboundBurst`) es la misma para event-processing y outbound-connectors, y si `TenantsMeteredAtPlatformDefault` está activo para alguno de los dos. Los envíos que fallan y se reintentan también se vuelven a medir en el extremo de los conectores, así que busque también un destino que falle. Eso también significa que un solo inquilino cuyo destino falla repetidamente mientras envía cerca de su cuota puede activar este aviso por sí solo, y por eso es un aviso de tipo warning y no critical. Los envíos descartados son mensajes no entregados con el motivo `rate_limited`. |

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
lo que supone una lectura de la base de datos relacional, después consulta el perfil y las
relaciones del dispositivo en el almacén clave-valor del bróker de mensajes, y entrega el evento
resuelto para que se publique. Las consultas se hacen una tras otra, así que un resolvedor pasa la
mayor parte de cada evento esperando respuestas, no usando CPU. Varios resolvedores trabajan a la
vez. Los eventos que llegan mientras todos están ocupados esperan delante de ellos, en orden: hasta
100 en la cola de entrega del pod y hasta 64 más en el último lote leído del flujo. El resto espera
en el flujo.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_devicemanagement_resolve_workers` | Cuántos resolvedores ejecuta el pod. |
| `devicechain_devicemanagement_resolve_inflight` | Cuántos de ellos están ocupados. Cuando se mantiene en `resolve_workers`, los eventos llegan más rápido de lo que el pod los resuelve y se acumulan delante de él. El recuento de pendientes del consumidor de entrada de `device-management` crece entonces (consulte [Un consumidor que se queda atrás](#consumer-backlog)). |
| `devicechain_devicemanagement_resolve_messages_total` | Eventos resueltos, por resultado. Mientras todos los resolvedores están ocupados, su ritmo es cuántos eventos por segundo puede resolver el pod. |

`resolve_duration_seconds` incluye el tiempo que un resolvedor espera para entregar su resultado.
Su intervalo más bajo es de 5 ms, así que un cuantil por debajo de eso es una estimación, no una
medida.

### Ajustarlo

| Ajuste (configuración de `device-management`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `resolution.workers` | `10` | Resolvedores que trabajan a la vez. Cada uno ocupa una conexión a la base de datos mientras autentica la credencial de un evento, lo que hace con cada evento que lleva una (todos los eventos, con la autenticación de dispositivos `required` por defecto). Por eso debe ser menor que el pool de conexiones del servicio (`rdbConfiguration.maxOpenConnections`, 20 si no se indica), que comparte con la API GraphQL, las comprobaciones de conexión MQTT y el consumidor que aplica las activaciones y resoluciones de alarmas. Se permite más de la mitad del pool, y se registra al arrancar. |

Súbalo cuando `resolve_inflight` se mantenga en `resolve_workers` mientras al pod le sobra CPU. Si
el pod está en su límite de CPU, más resolvedores no ayudan: dele más CPU (consulte
[Dimensionamiento de los servicios](./bootstrap.md#service-sizing)). Medido dentro del
proceso contra un bróker de tres servidores, con cada consulta tardando 750 µs, 5 resolvedores
resolvieron unos 1500 eventos por segundo y 10 unos 2900. Los resolvedores terminan los eventos
fuera del orden de llegada, por una fracción de segundo; la detección los aplica en el orden en que
llegan al flujo de eventos resueltos. Un valor fuera de rango impide que el servicio arranque, y el
error nombra el ajuste. El servicio registra el valor que usa al arrancar.

## Persistencia de eventos {#event-persistence}

`event-management` escribe los eventos por lotes. Cada escritor toma los eventos que ya lo esperan,
hasta un límite, y los confirma en una sola transacción. Un evento se reconoce solo después de que
la transacción que lo contiene se haya confirmado. Si se rechaza un evento de un lote, no se
conserva nada de esa transacción: el evento rechazado se vuelve a escribir por separado, y se
reintenta o se notifica exactamente como sin lotes. El resto del lote se vuelve a confirmar sin él.
Si la transacción falla por una causa que no se debe a ningún evento concreto, como una conexión
perdida con la base de datos, cada uno de sus eventos se vuelve a escribir por separado.

Con poco tráfico, un escritor encuentra un único evento esperando y lo confirma solo, así que el
procesamiento por lotes no añade retraso. Los lotes crecen solo cuando los eventos llegan más rápido
de lo que las confirmaciones individuales pueden absorber, que es cuando ayudan: en un almacén de
eventos replicado, la mayor parte de cada confirmación se va en esperar a la réplica, y un lote paga
esa espera una sola vez.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_eventmanagement_persist_batch_size` | Eventos por transacción confirmada. Si casi siempre es `1`, los escritores van al día. Lotes en el límite indican que trabajan a plena capacidad. |
| `devicechain_eventmanagement_persist_batch_fallbacks_total` | Transacciones de lote que no se confirmaron, tras lo cual sus eventos se volvieron a escribir. Un aumento ocasional es un evento rechazado. Un ritmo constante indica que algo rechaza escrituras una y otra vez, por ejemplo un inquilino eliminado cuyos dispositivos siguen enviando: cada lote que contiene sus eventos cuesta una transacción adicional, por muchos que contenga. Esos eventos aparecen en `persist_messages_total` como `failed` o `retry`. |
| `devicechain_eventmanagement_persist_inflight` | Eventos que tienen los escritores, incluidos los que esperan a que su lote se confirme. |

`persist_duration_seconds` mide cada evento desde que un escritor lo toma hasta que su lote se
confirma.

### Ajustarlo

| Ajuste (configuración de `event-management`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `persistence.writers` | `5` | Escritores en paralelo. Cada uno ocupa una conexión a la base de datos mientras escribe, así que debe ser menor que el pool de conexiones del servicio (`tsdbConfiguration.maxOpenConnections`, 20 si no se indica). Se permite más de la mitad del pool, y se registra al arrancar, porque entonces las lecturas compiten con los escritores por el resto. |
| `persistence.maxBatch` | `32` | Máximo de eventos confirmados en una transacción, de `1` a `64`. `1` desactiva los lotes. |
| `persistence.lingerMillis` | `0` | Cuánto espera un escritor a más eventos antes de confirmar un lote incompleto, hasta `1000`. `0` confirma lo que ya está esperando. |

Suba `maxBatch` antes que `writers`. En un almacén de eventos replicado, los lotes aumentan el
rendimiento mucho más que los escritores adicionales, y no usan conexiones extra. Un valor fuera de
rango impide que el servicio arranque, y el error nombra el ajuste. El servicio registra los
valores que usa al arrancar.

### Estado en vivo de los dispositivos {#live-state-projection}

`device-state` mantiene el estado en vivo de cada dispositivo (conectividad, actividad, últimas
lecturas y última posición) a partir del mismo flujo de eventos, y los fusiona de la misma manera:
cada escritor toma los eventos que ya lo esperan, hasta un límite, y los fusiona en una sola
transacción. Un evento se reconoce solo después de que esa transacción se confirme. Varios eventos
de un mismo dispositivo en un lote dejan exactamente lo que dejaría fusionarlos de uno en uno. Una
lectura o una posición sustituye a la almacenada solo si es estrictamente más reciente, así que una
más antigua o igual de antigua nunca la sobrescribe; las horas se comparan tal como las guarda la
base de datos, al microsegundo. Si se rechaza la parte de un lote que corresponde a un inquilino,
los eventos de ese inquilino se vuelven a fusionar de uno en uno, de modo que solo se reintenta un
evento que es rechazado por sí mismo, y los eventos de los demás inquilinos se confirman juntos sin
ellos. Si la transacción falla por una causa que no se debe a ningún inquilino, como una conexión
perdida con la base de datos, cada uno de sus eventos se vuelve a fusionar por separado.

| Métrica | Qué indica |
| --- | --- |
| `devicechain_devicestate_state_batch_size` | Eventos por transacción confirmada. Si casi siempre es `1`, los escritores van al día. |
| `devicechain_devicestate_state_batch_fallbacks_total` | Transacciones de lote que no se confirmaron, tras lo cual sus eventos se volvieron a fusionar. Un ritmo constante indica que las escrituras de un inquilino se rechazan una y otra vez, por ejemplo las de un inquilino eliminado cuyos dispositivos siguen enviando. |
| `devicechain_devicestate_state_inflight` | Eventos que tienen los escritores, incluidos los que esperan a que su lote se confirme. |

`state_duration_seconds` mide cada evento desde que un escritor lo toma hasta que su lote se
confirma.

| Ajuste (configuración de `device-state`) | Valor por defecto | Qué hace |
| --- | --- | --- |
| `projection.writers` | `5` | Escritores en paralelo; cada uno ocupa una conexión a la base de datos mientras fusiona. Debe ser menor que `rdbConfiguration.maxOpenConnections` (20 si no se indica). |
| `projection.maxBatch` | `32` | Máximo de eventos fusionados en una transacción, de `1` a `64`. `1` desactiva los lotes. |
| `projection.lingerMillis` | `0` | Cuánto espera un escritor a más eventos antes de fusionar un lote incompleto, hasta `1000`. `0` fusiona lo que ya está esperando. |

Deje `writers` en su valor por defecto salvo que los lotes vayan llenos y la base de datos tenga
margen. En una base de datos replicada, lo que sostiene el rendimiento son los lotes. Las fusiones de
un mismo dispositivo se esperan entre sí, así que cuando los dispositivos envían por turnos, más
escritores significan más lotes esperando a los mismos dispositivos, y a partir de unos pocos
escritores el rendimiento puede bajar en lugar de subir. El servicio registra los valores que usa al
arrancar. Si el
estado en vivo sigue quedándose atrás, `JetStreamDurableFallingBehind` salta para el consumidor de
`device-state` (consulte [Un consumidor que se queda atrás](#consumer-backlog)).

## Replicación {#replication}

Una instancia de alta disponibilidad necesita dos cosas: flujos de JetStream creados con el
número de réplicas para el que está configurada la instancia, y un clúster de NATS lo bastante
grande para alojarlos. Cualquiera de las dos puede fallar aunque la configuración parezca
correcta. Por eso cada servicio informa cada 30 segundos de lo que el bróker dice sobre cada flujo
y cada bucket KV que usa, y cinco avisos vigilan el resultado:

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamNotReplicatedAsConfigured` | warning | Durante 15 minutos, un flujo ha tenido menos réplicas de las configuradas para la instancia, según el servicio indicado por la etiqueta `job`. La instancia no es de alta disponibilidad para ese flujo. | Asegúrese de que el clúster de NATS tiene servidores suficientes para `instance.config.infrastructure.nats.streamReplicas`. El aumento de réplicas solo se ejecuta cuando un servicio arranca, así que, cuando el clúster sea lo bastante grande, reinicie los Deployments afectados. |
| `JetStreamReplicaPeersDegraded` | warning | Durante 20 minutos, un flujo ha tenido menos copias actualizadas y en línea que réplicas. Perder su líder puede perder datos o disponibilidad. La espera es larga porque las réplicas recién añadidas copian los datos del flujo antes de contar como actualizadas. | Revise el estado y la ubicación de los pods de NATS. Tres réplicas en pods que comparten un nodo no sobreviven a la pérdida de ese nodo. |
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

Eliminar una entrada tras un cambio (un dispositivo borrado, un perfil publicado) nunca se omite.
Espera hasta cinco segundos, porque solo el líder del bucket puede aceptarlo. Si aun así falla, el
servicio registra `A key-value cache eviction failed`, y la entrada antigua puede servirse hasta
que caduque, que es el tiempo de vida configurado de la caché.

- **`devicechain_devicemanagement_kv_cache_unavailable{cache}`**: 1 mientras el bucket se está
  omitiendo.
- **`devicechain_devicemanagement_kv_cache_failures_total{cache, op, reason}`**: operaciones que
  agotaron el tiempo (`reason="timeout"`) o fallaron (`reason="error"`).
- **`devicechain_devicemanagement_kv_cache_bypassed_total{cache, op}`**: búsquedas y escrituras que
  fueron a la base de datos en su lugar.
- **`devicechain_devicemanagement_kv_cache_request_duration_seconds{cache, op}`**: cuánto tardó
  cada operación. Una búsqueda o una escritura se corta a medio segundo, una eliminación a cinco
  segundos.

Por otra parte, resolver un evento que tarda más de cinco segundos, por la razón que sea, se
registra como advertencia (`Event resolution is slow`): la primera vez de inmediato y después como
máximo una línea cada 30 segundos, con cuántas hubo y la más lenta.

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
| event-sources | `presence_demote` |
| command-delivery | `command_sweep`, `hold_reconcile`, `stranded_reconcile` |

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
| `DatabaseVolumeFillingFast` | Un volumen del almacén de eventos está lleno en más de la mitad y, al ritmo de los últimos 15 minutos, se llenará en menos de una hora, durante 3 minutos. | Si solo sube el primario, es log sin enviar: arregla primero el archivado. Si suben todos los miembros, son datos: amplía el volumen o acorta la retención de datos. |
| `DatabaseVolumeAlmostFull` | Un volumen de base de datos está lleno en más de un 85 %, durante 15 minutos. | Amplíalo ya. |

Las dos alertas de ritmo existen porque los umbrales fijos son demasiado lentos para un volumen
que se llena en minutos, que es lo que la ingesta sostenida hace con él. Se comprobaron con una
prueba de rendimiento en la que el primario del almacén de eventos pasó del 45 % a lleno en unos
nueve minutos después de que se llenara el almacén de objetos: la alerta de ritmo se dispara unos
dos minutos antes de que el volumen se llene, mientras que la alerta del 85 %, que espera 15
minutos, solo se habría disparado después.

El almacén de objetos lo comparten todas las instancias del clúster, igual que la base de datos
relacional. El chart de cada instancia lleva estas alertas, así que una alerta sobre un volumen
compartido o sobre la base de datos compartida aparece una vez por instancia.

Para saber qué tamaño necesita el almacén interno, consulta
[El destino de respaldo predeterminado](./bootstrap.md#default-backup-destination).

## Relacionado

- **[Arrancar una instancia](./bootstrap.md#install)** — `dcctl install`, el comando que
  despliega la pila de monitoreo, y sus indicadores (flags).
- **[Despliegue y operador](./kubernetes-operator.md)** — cómo el chart genera
  las cargas de trabajo por servicio con sus sondas de salud.
