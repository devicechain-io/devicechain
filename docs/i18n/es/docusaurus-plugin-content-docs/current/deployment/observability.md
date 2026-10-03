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
había leído un mensaje cuando se descartó no lo leerá nunca. Los dos flujos de ingesta cuya pérdida
serían datos de dispositivos rechazan eventos nuevos antes de que eso le ocurra al consumidor que no
puede perderlos (consulte [Contrapresión en la ruta de ingesta](#ingest-backpressure)). Todos los
demás flujos, y todos los demás consumidores, se cubren aquí. El broker no informa de esta pérdida,
así que cada servicio la mide para cada consumidor duradero que lee, y estas alertas vigilan el
resultado:

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `JetStreamDurableUnreadNearFull` | warning | Un consumidor lleva 5 minutos sin haber leído más del 80% de lo que cabe en su flujo. Todavía no se ha perdido nada. Los mensajes que ya leyó no cuentan, así que un flujo lleno de historial ya procesado no la dispara. Cuando la cola sin leer llega al límite, el flujo descarta los mensajes más antiguos y este consumidor nunca procesará los que no había alcanzado. Para el consumidor de detección de `event-processing` mide el consumidor, no el punto de control desde el que la detección vuelve a leer; el punto de control nunca va por detrás de lo que el consumidor ha confirmado, así que el número es al menos lo que la detección podría perder. | Averigüe por qué el consumidor va lento: los registros de su servicio, su base de datos, `JetStreamDurableFallingBehind`. Si el tráfico ha superado el flujo, aumente su límite y, con él, el volumen de JetStream. Los dos consumidores que frenan la ingesta los cubre `JetStreamUnreadBacklogNearFull` (consulte [Contrapresión en la ruta de ingesta](#ingest-backpressure)). |
| `JetStreamStreamNearFull` | info | Un flujo que guarda registros para un operador, en lugar de mensajes que procesa un servicio, lleva 10 minutos por encima del 80% de su límite (en bytes o en mensajes). Son los registros de mensajes que fallaron (`failed-decode`, `failed-events`, `connector-dispatch.dead`), `max-deliveries`, y `dead-letters` mientras `user-management`, que guarda sus cartas muertas, no informe de que lo lee. Nada procesa lo que contienen, así que cuando se llenan descartan registros que nadie ha mirado. Los flujos que leen los servicios no se cubren: conservan una semana de historial que normalmente está cerca del límite, y las alertas de esta tabla vigilan a sus consumidores. | Busque qué lo está llenando: un decodificador que rechaza los mensajes de un dispositivo, eventos que no se pueden resolver o guardar, un conector cuyo destino rechaza todos los envíos. En `dead-letters`, compruebe que `user-management` está en marcha. Aumente el límite solo si hay que conservar los registros más tiempo. La configuración predeterminada de Alertmanager de kube-prometheus-stack suprime las alertas `info`, así que enrútela explícitamente si la quiere recibir. |
| `JetStreamDurableLostUnread` | critical | Un consumidor pasó por encima de mensajes que se eliminaron antes de que los leyera. Nunca se procesaron. | Si en ese momento se estaba eliminando un tenant, es lo esperado: la eliminación borró mensajes a los que el consumidor aún no había llegado. Si no, el flujo estaba lleno mientras este consumidor iba atrasado. O bien el límite es demasiado pequeño para el tráfico, o bien el consumidor es más lento que su productor. |
| `JetStreamDurableStalledBehindStream` | critical | Un consumidor lleva al menos dos minutos sin recibir ningún mensaje, y el flujo ya ha descartado mensajes por delante de él. Un consumidor que está leyendo, aunque sea despacio, no dispara esta alerta; sus pérdidas disparan `JetStreamDurableLostUnread`. | El servicio está en marcha, ya que es él quien lo informa, pero su consumidor no lee. Busque un procesamiento de mensajes bloqueado en una dependencia, como la base de datos, o pods esperando a estar listos. Si no se puede arreglar rápido, aumente el límite del flujo para que deje de descartar. |

Los límites son `streamMaxBytes` (los flujos de alto volumen), `streamMaxBytesCold` (los demás) y
`streamMaxMsgs`, bajo `instance.config.infrastructure.nats`. El volumen de JetStream se dimensiona a
partir de su suma, así que aumente el volumen junto con ellos (consulte
[Arrancar una instancia](./bootstrap.md)).

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
| MQTT (el broker de la plataforma) | Nada. El broker confirmó el mensaje antes de que la plataforma pudiera rechazarlo. El mensaje espera en el flujo de captura, que descarta sus mensajes más antiguos cuando se llena (`JetStreamDurableLostUnread`). |
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
| `JetStreamIngestBackpressureEngaged` | critical | Un flujo lleva un minuto rechazando eventos nuevos, para todos los inquilinos: la cola sin leer de un consumidor está cerca del límite, o el flujo lleno descarta el historial procesado por delante de ella tan deprisa que se agotaría en 30 segundos. | `JetStreamUnreadBacklogNearFull` indica qué consumidor va atrasado. La causa más probable es que el servicio de ese consumidor no esté funcionando: escalado a cero réplicas o en un bucle de reinicios. Un servicio desplegado que no funciona sigue frenando la ingesta, a propósito. Si `JetStreamUnreadBacklogNearFull` no se dispara, es el segundo caso: `jetstream_backpressure_history_runway_seconds` lo muestra, y el registro del servicio que escribe indica el consumidor y el ritmo. El rechazo se levanta solo cuando la cola de ese consumidor baja del 80% y, si la causa era el historial, cuando ese historial duraría un minuto o el consumidor lo ha leído todo. |

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

`ExternalMqttSourceNotReadByOnePod` (aviso) salta cuando esa suma lleva dos minutos sin valer 1.
En 0, nadie lee la fuente y lo que tu bróker entrega mientras tanto se pierde; el registro del
último pod que tomó la fuente dice por qué. Una instalación sin fuentes externas no exporta esa
serie, y la alerta no salta.

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
| `ReplayCoveredDeliveriesExhausted` | Un consumidor que lee su stream desde su propio punto de control agotó intentos de entrega en los últimos 15 minutos, porque el punto de control lleva sin guardarse más tiempo del que el broker sigue reentregando. Todavía no se ha perdido nada. | Corrija lo que impide al servicio que indica la etiqueta `job` guardar su punto de control, normalmente su conexión a la base de datos. Mientras el servicio sigue en marcha, guarda lo que ha leído en cuanto el punto de control se guarda. Si se reinicia antes, vuelve a leer el stream desde el último punto de control guardado, y los eventos que el stream ya haya descartado no se pueden volver a leer, así que vigile también `JetStreamDurableUnreadNearFull`. |

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
lo que supone una lectura de la base de datos relacional, después consulta el perfil, las
relaciones y el alcance de grupos del dispositivo en el almacén clave-valor del bróker de
mensajes, y entrega el evento resuelto para que se publique. Esas tres consultas se hacen a la
vez. Lo que el almacén clave-valor no puede responder se lee de la base de datos una consulta
tras otra, así que un resolvedor sigue ocupando como máximo una conexión a la base de datos. Un
resolvedor pasa la mayor parte de cada evento esperando respuestas, no usando CPU. Varios resolvedores trabajan a la
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
| `resolution.workers` | `10` | Resolvedores que trabajan a la vez. Cada uno ocupa una conexión a la base de datos mientras lee de ella la credencial de un evento, lo que hace siempre que la credencial no se haya verificado en esa réplica en los últimos cinco segundos (consulte [Cachés que dejan de responder](#kv-caches)). Por eso debe ser menor que el pool de conexiones del servicio (`rdbConfiguration.maxOpenConnections`, 20 si no se indica), que comparte con la API GraphQL, las comprobaciones de conexión MQTT y el consumidor que aplica las activaciones y resoluciones de alarmas. Se permite más de la mitad del pool, y se registra al arrancar. Las consultas de un resolvedor al almacén clave-valor se hacen a la vez, pero sus lecturas de la base de datos siguen haciéndose de una en una, así que nunca ocupa más de una conexión. |
| `inMemoryCache.perDeviceCacheEntries` | `131072` | Cuántas entradas, como máximo, guarda cada réplica en memoria de cada una de las tres cachés que se guardan por dispositivo: un dispositivo por su token, sus relaciones seguidas y sus pertenencias a grupos. Consulte [Cachés que dejan de responder](#kv-caches). |
| `inMemoryCache.perDeviceCacheMiB` | `24` | Cuánta memoria, en MiB, ocupa como máximo cada una de esas tres cachés en cada réplica. Suba con él el límite de memoria del servicio. Consulte [Cachés que dejan de responder](#kv-caches). |

Súbalo cuando `resolve_inflight` se mantenga en `resolve_workers` mientras al pod le sobra CPU. Si
el pod está en su límite de CPU, más resolvedores no ayudan: dele más CPU (consulte
[Dimensionamiento de los servicios](./bootstrap.md#service-sizing)). Medido dentro del
proceso contra un bróker de tres servidores, con cada consulta tardando 750 µs y hechas una tras
otra, 5 resolvedores resolvieron unos 1500 eventos por segundo y 10 unos 2900. Los resolvedores terminan los eventos
fuera del orden de llegada, por una fracción de segundo; la detección los aplica en el orden en que
llegan al flujo de eventos resueltos. Un valor fuera de rango impide que el servicio arranque, y el
error nombra el ajuste. El servicio registra el valor que usa al arrancar.

## Persistencia de eventos {#event-persistence}

`event-management` escribe los eventos por lotes. Cada escritor toma los eventos que ya lo esperan,
hasta un límite, y los confirma en una sola transacción, escribiendo cada tabla una vez por cada
inquilino del lote en lugar de una vez por cada evento. Los eventos de conexión y desconexión se
siguen escribiendo uno a uno dentro del lote. Un evento se reconoce solo después de que la
transacción que lo contiene se haya confirmado. Si se rechaza un evento de un lote, no se conserva
nada de esa transacción: el evento rechazado se vuelve a escribir por separado, y se reintenta o se
notifica exactamente como sin lotes. El resto del lote se vuelve a confirmar sin él. Cuando la base
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
| `devicechain_eventmanagement_persist_batch_fallbacks_total` | Transacciones de lote que no se confirmaron, tras lo cual sus eventos se volvieron a escribir. Un aumento ocasional es un evento rechazado, o dos cuando la base de datos rechazó una fila de una sentencia que llevaba varios eventos, porque encontrar el evento requiere un segundo intento. Un ritmo constante indica que algo rechaza escrituras una y otra vez, por ejemplo un inquilino eliminado cuyos dispositivos siguen enviando: cada lote que contiene sus eventos cuesta una transacción adicional, por muchos que contenga. Esos eventos aparecen en `persist_messages_total` como `failed` o `retry`. |
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

Cada réplica de `device-management` también guarda en memoria lo que leyó de un bucket, o escribió
en él, durante hasta cinco segundos (menos si el tiempo de vida de la caché es menor), y responde
desde ahí sin preguntar a NATS, incluso mientras el bucket se está omitiendo. Los cinco segundos
cuentan desde que se leyó el valor, no desde la última vez que se usó. Las tres cachés que se
guardan por dispositivo (un dispositivo por su token, sus relaciones seguidas y sus pertenencias a
grupos) guardan cada una hasta 131.072 entradas o 24 MiB por réplica, según
`inMemoryCache.perDeviceCacheEntries` e `inMemoryCache.perDeviceCacheMiB`. Eso son unos 87.000
dispositivos sin ninguna relación seguida, o unos 26.000 con una. Las cachés de perfil y de alcance
de grupos, que se guardan por tipo de dispositivo y por inquilino, guardan 4096 entradas o 4 MiB.
Cada una descarta primero la entrada usada hace más tiempo cuando está llena. Al guardar una
entrada nueva también descarta las caducadas desde su extremo de uso más antiguo, y se detiene en
la primera que no ha caducado. Una búsqueda que NATS informó
como ausente nunca se guarda. Un cambio llega a los eventos que resuelven las demás
réplicas hasta cinco segundos más tarde de lo que llegaría solo a través del bucket. Hasta
entonces otra réplica puede, por ejemplo, seguir resolviendo un dispositivo borrado, o vuelto a
crear con el mismo token, a través de su registro anterior, o evaluar una regla cuyo alcance de
grupo acaba de cambiar con el alcance anterior. Los eventos que presentan una credencial de
dispositivo toman su dispositivo de la copia de la credencial que describe el párrafo siguiente.

**Credenciales de dispositivo.** Cada réplica guarda también en memoria una credencial de
dispositivo que acaba de verificar, con su dispositivo, durante cinco segundos desde que la leyó.
Nunca se guarda en un bucket de clave-valor, porque contiene la contraseña de la credencial. Cada
réplica guarda hasta 65.536 credenciales o 16 MiB, que son fijos. Cada comprobación de una
credencial guardada compara su contraseña y su expiración igual que la de la almacenada, y una
credencial que no se pudo verificar nunca se guarda. Un cambio en una credencial o en su
dispositivo descarta la copia en la réplica que lo hace, y un mensaje en NATS avisa a las demás
réplicas para que descarten la suya. Así que una revocación, o un dispositivo borrado a través de
otra réplica, normalmente surte efecto en el siguiente evento en todas, y en cinco segundos como
máximo si ese mensaje se pierde. Las conexiones MQTT siempre leen la base de datos. Una flota que
informa con menos frecuencia que cada cinco segundos lee su credencial de la base de datos en cada
evento, como antes.

**Flotas que informan con menos frecuencia que cada cinco segundos.** Un valor se guarda en memoria
cinco segundos desde que se leyó, por grande que sea la caché. Así que un dispositivo que informa
con menos frecuencia nunca se responde desde memoria, y cada uno de sus eventos cuesta una lectura
del bucket de clave-valor. En un clúster GKE de tres nodos esa lectura tardó unos 1,5 ms. Con los
10 resolvedores por defecto, cada uno dedicando ese tiempo a cada uno de esos eventos, una flota
así se resuelve más despacio que una cuyos dispositivos informan cada pocos segundos. Para
resolverla más rápido, añada resolvedores (`resolution.workers`, dentro del pool de conexiones) o
réplicas de `device-management`. La señal es
`kv_cache_local_lookups_total{cache="relationships-by-source", result="miss"}` cerca del ritmo de
eventos, mientras `kv_cache_local_entries` de esa caché se mantiene muy por debajo de
`kv_cache_local_max_entries`. Una flota demasiado grande para la caché muestra en cambio
`kv_cache_local_evictions_total{reason="capacity"}` creciendo cerca del ritmo de eventos, con
`kv_cache_local_entries` en `kv_cache_local_max_entries` o `kv_cache_local_bytes` en
`kv_cache_local_max_bytes`. Entonces suba el límite, y con él el límite de memoria: con los valores
por defecto las seis cachés en memoria, incluidos los 16 MiB fijos de las credenciales, guardan
como máximo 96 MiB, y sin `GOMEMLIMIT` el heap puede crecer
hasta aproximadamente el doble de lo que guarda antes de recolectarse.

Eliminar una entrada tras un cambio (un dispositivo borrado, un perfil publicado) nunca se omite.
Espera hasta cinco segundos, porque solo el líder del bucket puede aceptarlo. Si aun así falla, el
servicio registra `A key-value cache eviction failed`, y la entrada antigua puede servirse hasta
que caduque, que es el tiempo de vida configurado de la caché, más hasta cinco segundos en las
réplicas que ya la tenían en memoria.

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
  descartadas de la memoria porque tenían cinco segundos (`reason="expired"`), porque la caché
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
  descartadas de la memoria porque tenían cinco segundos (`reason="expired"`), porque la copia
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
  cambios llegan a las demás réplicas solo cuando caducan sus copias, en cinco segundos como máximo.

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
