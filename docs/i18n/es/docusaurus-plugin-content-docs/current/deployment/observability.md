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
  forma definitiva, de modo que Kubernetes reinicia el pod.
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

El registro de sentencias SQL es un interruptor aparte, por servicio: `sqlDebug` en la
configuración del almacén de datos de un servicio. La capa de base de datos lo escribe con su
propio registrador, así que `infrastructure.logging.level` ni lo activa ni lo suprime.

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

## Relacionado

- **[Arrancar una instancia](./bootstrap.md#install)** — `dcctl install`, el comando que
  despliega la pila de monitoreo, y sus indicadores (flags).
- **[Despliegue y operador](./kubernetes-operator.md)** — cómo el chart genera
  las cargas de trabajo por servicio con sus sondas de salud.
