---
sidebar_position: 10.5
title: Ajustar una instancia
---

# Ajustar una instancia {#tuning-an-instance}

:::note Las cifras de dimensionamiento suponen la confirmación a prueba de fallos desactivada
Las cifras de [Rendimiento medido](./bootstrap.md#measured-throughput) se tomaron con la [confirmación MQTT a prueba de fallos](./observability.md#mqtt-fail-closed) desactivada, el valor por defecto. Sin ella, una sobrecarga MQTT sostenida que supere el margen del flujo de captura descarta los eventos confirmados más antiguos (una [carencia aceptada](../concepts/security-model.md#accepted-gaps)). Si eso te importa, actívala con `--mqtt-fail-closed` y dimensiona por debajo de su techo más bajo.
:::

Ajustas una instancia (número de réplicas, CPU y memoria, la configuración propia de un área) con
un archivo de valores. Se lo pasas a `dcctl bootstrap` o a `dcctl upgrade` con `--values`, y
`dcctl` **lo registra en la instancia**, de modo que cada `dcctl upgrade` posterior lo aplica de
nuevo sin que se lo pidas. Sin ese registro, una actualización reconstruye los ajustes de la
instancia a partir de su declaración y tu ajuste se revierte.

```yaml title="tuning.yaml"
functionalAreas:
  device-management:
    replicas: 3
    resources:
      limits:
        cpu: "3"
  event-sources:
    config:
      capture:
        publishWindow: 512
```

```bash
# En el bootstrap
dcctl bootstrap local my-instance --values tuning.yaml

# En una instancia en marcha
dcctl upgrade local my-instance --values tuning.yaml

# Cada actualización posterior lo sigue aplicando; no hay nada que repetir
dcctl upgrade local my-instance --version <new-version>
```

## Cómo se combina la superposición con lo que fija dcctl {#precedence}

Tres capas, cada una sobre la anterior:

1. Los valores por defecto del chart.
2. Los valores que `dcctl` calcula a partir de la declaración de la instancia: imágenes,
   credenciales, endpoints, el perfil y las áreas, y el dimensionamiento que implica un
   preajuste como `--compact` o `--ha`.
3. Tu superposición.

Los mapas se fusionan clave por clave, así que solo indicas las claves que quieres cambiar. Una
lista o un valor simple sustituye lo que había debajo. Repite `--values` para apilar varios
archivos; los últimos prevalecen.

La superposición se aplica a los mismos valores a partir de los cuales el chart renderiza la
configuración de la instancia, de modo que un ajuste como `shutdownDrainSeconds` llega al
documento de configuración además de a los pods.

## Qué puedes ajustar {#tunable}

Una superposición solo puede fijar los valores siguientes. Todo lo demás se rechaza.

| Valores | Qué controlan |
| --- | --- |
| `replicas`, `rollingUpdate`, `terminationGracePeriodSeconds`, `nodeLossTolerationSeconds`, `shutdownDrainSeconds`, `podDisruptionBudget`, `startupProbe`, `livenessProbe` | Escala y despliegue de todas las áreas |
| `resources`, `useMeasuredRequests`, `goMemLimitPercent`, `gogc`, `goMemLimit` | CPU y memoria, y el comportamiento de memoria de Go, de todas las áreas |
| `functionalAreas.<area>.replicas`, `.strategy`, `.resources`, `.measuredRequests`, `.goMemLimitPercent`, `.memoryLimitFloor`, `.gogc`, `.goMemLimit`, `.avoidEventStorePrimary`, `.eventPathSpread` | Lo mismo, para un área |
| `functionalAreas.<area>.config` | La configuración propia de esa área. Una clave desconocida detiene el servicio al arrancar |
| `functionalAreas.<area>.profiler.enabled`, `.blockRate`, `.mutexFraction` | El listener de profiling de diagnóstico, desactivado por defecto (véase [Observabilidad](./observability.md)). Su `address` no es ajustable |
| `frontend.replicas`, `frontend.resources` | Los pods de la consola |
| `metrics.alerts`, `metrics.grafanaDashboards` | Reglas de alerta y paneles |
| `ingress.className`, `ingress.annotations`, `ingress.uploadMaxBodySize`, `ingress.gateway.className`, `ingress.gateway.annotations`, `networkPolicy.additionalAllowedCidrs`, `serviceAccount.annotations` | Cómo se publica el ingress y quién puede llegar a los pods |
| `mqttExposure.loadBalancer` | Los ajustes de nube del balanceador de carga de MQTT, como un balanceador interno y los rangos de origen permitidos |
| `instance.config.infrastructure.nats.streamDesignRate`, `.streamRunwayMinutes`, `.streamMaxBytes`, `.streamMaxBytesCold`, `.streamMaxMsgs` | El margen de interrupción para el que se dimensionan los streams de eventos y sus techos de bytes y mensajes. La reserva se comprueba con el volumen del broker antes de escribir nada |
| `instance.config.infrastructure.messaging.envelopes.resolved`, `.inbound`, `.inboundLingerMillis` | Si los eventos se escriben como envelopes, desactivado por defecto |
| `brokerAdmin.allowCeilingShrink` | Permite que una actualización baje un techo de stream por debajo de lo que contiene. Fíjalo para esa actualización y quítalo después |

`functionalAreas.event-management.replicas` no es ajustable aunque el resto de esa área sí lo
sea: lo fija `--ha`, y la reserva de conexiones del almacén de eventos se dimensiona según ese
número.

También se rechazan algunos valores dentro de un subárbol ajustable, porque son interruptores de
seguridad o cableado y no capacidad:

- En la `config` de un área: `deviceAuthMode` (device-management), el bloque `auth`
  (user-management), `resourceUrl` e `issuerUrl` (mcp), `updates.allowUnverifiedArtifacts` y
  `updates.downloadBaseURL` (update-management), `eventSources` (event-sources), `sources`
  (sparkplug-ingest), la `tsdbConfiguration` de cualquier área y `rdbConfiguration.sqlDebug`. Las
  claves se comparan sin distinguir mayúsculas de minúsculas, igual que las leen los servicios.
- En `ingress.annotations`: toda anotación que el chart ya fija en sus ingress (tu valor
  reemplazaría el del chart), cualquier anotación `*-snippet`, `cert-manager.io/*`, y
  `ssl-redirect` o `force-ssl-redirect`.

`serviceAccount.annotations` se acepta porque es donde se vincula una identidad de nube (workload
identity, o un rol IAM para cuentas de servicio) para el almacén de objetos s3. Todos los pods de
servicio de la instancia comparten esa cuenta de servicio, así que todos tienen el rol que se
vincule ahí: no le concedas más de lo que necesita el bucket.

Se rechaza todo lo que `dcctl` posee: imágenes y versiones, credenciales y la clave raíz del
almacén de secretos, endpoints del broker y de las bases de datos, el host y el TLS del ingress,
el perfil y el conjunto de áreas (lo cambia `dcctl enable-area`), los ajustes del almacén de
objetos, los contextos de seguridad, y los ajustes de LwM2M que
`dcctl` traslada por sí mismo de una versión a la siguiente. La lista es una lista de permitidos a
propósito: un valor que el chart incorpore en una versión posterior sigue rechazado hasta que se
añada aquí, en lugar de volverse ajustable por accidente.

Un rechazo nombra cada clave infractora y el archivo del que viene, antes de cambiar nada:

```text
--values tuning.yaml sets values dcctl does not let an overlay set, so nothing has been changed:
  image.tag: this is not an operator-tunable value: dcctl or the chart owns it (images, credentials, endpoints, exposure, the area set, security contexts)
```

La superposición también se valida contra el esquema de valores del chart y se renderiza junto
con los demás valores de la instancia antes de aplicar nada. Un valor de tipo incorrecto, una
petición por encima de su límite o una combinación que el chart rechaza detiene la ejecución sin
cambiar nada.

## Dimensionar el almacén de eventos {#event-store-sizing}

El almacén de eventos es el único volumen cuyo crecimiento lo marca el tráfico, así que es el que
hay que dimensionar a propósito. Tres cifras lo deciden.

- **Bytes por fila almacenada.** Una fila de medición cuesta unos 600 bytes en disco, índices
  incluidos, antes de comprimir: un volumen de 32Gi contenía unos 51 millones de filas cuando se
  llenó en una prueba a 10 000 lecturas por segundo. Una medición anterior dio cerca de 1 KB con el
  registro de escritura anticipada; dimensione con la cifra mayor hasta que haya medido la suya; la fórmula siguiente toma el tamaño de fila como variable.
  El almacén de eventos comprime los fragmentos anteriores a `lifecycle.compressAfterDays` (7 por
  defecto) en la configuración de `event-management`, sin pérdida, pero no hace nada con la semana
  más reciente: dimensione el volumen para al menos esa cantidad de datos sin comprimir. La cifra
  de 600 bytes es la no comprimida, y la razón de compresión depende de sus datos, así que mídala
  antes de contar con ella.
- **Filas por lectura.** Cada [métrica calculada](../user-guide/calculated-metrics.md) que se aplica
  a un perfil almacena una fila más por lectura. Cada fórmula añade una fila por lectura, así que un perfil con N métricas calculadas almacena
  (1 + N) filas por lectura, de modo que N fórmulas multiplican el volumen hasta por 1 + N. El techo del nivel `calculatedMetricsPerProfile` es 0 (desactivado) por defecto y como máximo 32; es un techo, y el coste depende del uso.
- **Retención.** `lifecycle.retentionDays` es 0 por defecto, es decir, conserva los datos para
  siempre. Una instancia pensada para funcionar indefinidamente debería fijarlo; entonces el volumen
  solo tiene que contener la ventana más la semana sin comprimir.

Como regla, la ventana sin comprimir necesita lecturas por segundo × (1 + métricas calculadas) × bytes por fila × 86 400 segundos × días, dejando libre una cuarta parte del volumen para el registro de
escritura anticipada, el mantenimiento y el crecimiento. A los 600 bytes medidos y 10 000 lecturas por segundo son unos 520 GB al día, así que los siete días sin comprimir por defecto son unos 3,6 TB. Añada lo que
necesiten el historial comprimido y la ventana de retención.

Tres alertas vigilan el volumen, en el orden en que conviene enterarse: `DatabaseVolumeFillsWithinDay`
(va camino de llenarse en 24 horas), `DatabaseVolumeFillingFast` (en una hora) y
`DatabaseVolumeCritical` (95 % lleno). Si la base de datos deja de aceptar escrituras de todos
modos, salta `EventStoreRefusingWrites` y `event-management` retiene los eventos que no pudo
almacenar en lugar de descartarlos (vea [Cuando el almacén de eventos no puede aceptar
escrituras](./observability.md#event-store-down)).

### Puntos de control, tamaño de fragmento y compresión con tasas altas {#event-store-checkpoints}

Tres ajustes deciden cómo se comporta el almacén de eventos cuando la tasa supera unos pocos miles
de lecturas por segundo.

- **Puntos de control.** El primer cambio en cada página tras un punto de control escribe la página
  completa de 8 KB en el registro de escritura anticipada, de modo que unos puntos de control
  demasiado frecuentes multiplican el registro. El almacén de eventos registra imágenes de página
  comprimidas (`wal_compression = lz4`) y fija `checkpoint_timeout` en 15 minutos y `max_wal_size`
  en una quinta parte del volumen (6,4 GiB en los 32Gi predeterminados), de modo que un almacén que
  recibe unos pocos miles de lecturas por segundo hace los puntos de control por reloj y no cada
  vez que se llena el registro. Ambos se aplican por recarga, sin reinicio. El coste es la
  recuperación tras una caída: después de una parada no limpia, la base de datos reproduce el
  registro escrito desde su último punto de control, hasta unos 30 minutos, porque un punto de control se reparte durante la mayor parte de su intervalo. Al dimensionar el almacén
  para una tasa mayor, suba `timescale_max_wal_size_mib` y baje `timescale_checkpoint_timeout`
  juntos: para que los puntos de control sigan el reloj, `max_wal_size` debe ser al menos el triple
  del registro escrito por intervalo de punto de control. El registro vive en el volumen de datos,
  así que esa cifra tiene que caber en él.
  **Regla de dimensionamiento.** Postgres solicita un punto de control, antes de que venza el
  temporizador, cuando el registro escrito desde el anterior alcanza aproximadamente
  `max_wal_size` / (1 + `checkpoint_completion_target`), es decir, `max_wal_size` dividido entre
  1,9. Para que los puntos de control del almacén de eventos sigan el reloj, fije `max_wal_size` en
  al menos 1,9 × `checkpoint_timeout` (900 segundos por defecto) × la tasa de registro en bytes por
  segundo. Mida la tasa en su propio almacén (`wal_bytes` de `pg_stat_wal`, en una ventana);
  depende del ancho de fila y de los índices. Ejemplo de una prueba de rendimiento en GKE: con el límite predeterminado
  de 13 GiB, el almacén de eventos hizo puntos de control por reloj a 8.000 eventos MQTT por segundo
  (9,4 MB/s de registro), pero a 12.000 por segundo (15,3 MB/s) solicitó un punto de control en lugar
  de esperar al temporizador, porque 15 MB/s escriben unos 7 GB en menos de ocho minutos. A 15,3 MB/s
  la regla da unos 26 GiB, y unos 45 GiB a 20.000 por segundo. El registro vive en el volumen de
  datos, así que dimensione el volumen para ese límite además de para los datos. El triple indicado
  arriba es el margen que conviene mantener sobre este mínimo.
  Estos ajustes afinan solo el almacén de eventos. La base de datos relacional conserva sus valores
  predeterminados (`max_wal_size` de 1 GB y `checkpoint_timeout` de cinco minutos); en la misma
  prueba solicitó de 3 a 6 puntos de control en cada ventana de 7,5 minutos con 4,5 a 7 MB/s de
  registro, algo esperable en ella y no una señal de que el almacén de eventos esté mal ajustado.
  Una actualización lo aplica también a un almacén existente, con el tamaño actual de su volumen.
  Si ese volumen ya está casi lleno, fije `timescale_max_wal_size_mib` en 1024 para conservar el
  1 GiB anterior.
- **Intervalo de fragmento.** `lifecycle.chunkIntervalHours` es 24 por defecto y solo afecta a los
  fragmentos creados después de cambiarlo. Los índices de un fragmento deben caber en memoria
  mientras se escribe en él, y los de los fragmentos activos en aproximadamente una cuarta parte
  de la RAM de la base de datos. A 1.000 lecturas por segundo un fragmento diario ocupa decenas de
  gigabytes y el valor predeterminado es correcto. A 10.000 o más, use de 1 a 6 horas.
- **Compresión tras N días.** `lifecycle.compressAfterDays` es 7 por defecto, lo que deja una
  semana de datos sin comprimir. A 10.000 lecturas por segundo son varios terabytes de datos
  calientes. Si no consulta datos en bruto de más de uno o dos días, fíjelo en 1 y dimensione el
  volumen en consecuencia.

### Ampliar el volumen en línea {#event-store-expand}

En una StorageClass que permita ampliar volúmenes, suba el tamaño en el Cluster del almacén de
eventos. El operador de la base de datos redimensiona la reclamación de cada instancia en su sitio,
sin reiniciar:

```bash
kubectl -n <instance namespace> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"storage":{"size":"48Gi"}}}'
kubectl -n <instance namespace> get pvc -w
```

Si el pod de la base de datos ya estaba en bucle de reinicios
por el disco lleno, elimine ese pod cuando su reclamación haya crecido para que reinicie sobre el
sistema de archivos mayor. Entonces se almacenan los eventos retenidos y la acumulación detrás de
ellos, y las alertas se resuelven al subir la fracción libre. Fije también `timescale_storage` al nuevo tamaño en la configuración OpenTofu de la instalación. El Cluster se aplica desde ese valor: la fusión de tres vías de Helm conserva un tamaño modificado a mano solo mientras el valor configurado no cambie, un valor configurado menor hace que el operador de la base de datos rechace la reducción y falle una aplicación directa (la propia `dcctl upgrade` conserva el tamaño actual del volumen y nunca lo reduce), y una reconstrucción de recuperación ante desastres vuelve al tamaño configurado.

### Cuando el cambio de tamaño no llega a los volúmenes {#event-store-expand-pvc}

El operador de la base de datos aplica un nuevo `spec.storage.size` a las reclamaciones solo mientras
reconcilia el Cluster, y un Cluster en mal estado puede no hacerlo. En el incidente de volumen lleno
que motiva estas notas, ambas réplicas en espera entraban en bucle de fallos con `No space left on
device`, y modificar el Cluster no cambió las reclamaciones en unos tres minutos; modificar las
reclamaciones directamente sí. Si `kubectl get pvc` sigue mostrando la capacidad anterior unos
minutos después de modificar el Cluster, amplíe usted las reclamaciones. Lo mismo vale para la base de
datos relacional (Cluster `dc-rdb`, reclamaciones `dc-rdb-N`), que vigilan `DatabaseVolumeFillingUp`,
`DatabaseVolumeAlmostFull` y `DatabaseVolumeCritical` además del almacén de eventos.

1. Compruebe que la StorageClass permite la expansión. Sin ella el cambio se rechaza y las únicas
   salidas son las descritas arriba:

   ```bash
   kubectl -n <espacio de nombres de la instancia> get pvc dc-tsdb-1 -o jsonpath='{.spec.storageClassName}{"\n"}'
   kubectl get storageclass <esa clase> -o jsonpath='{.allowVolumeExpansion}{"\n"}'   # debe imprimir true
   ```

2. Modifique todas las reclamaciones del Cluster al mismo tamaño: la de datos de cada miembro y,
   si el Cluster tiene un volumen aparte para el registro de escritura anticipada, también su
   reclamación `-wal` (`dc-tsdb-1`, `dc-tsdb-2`, `dc-tsdb-3`, y `dc-tsdb-1-wal`, etc.). Una
   reclamación solo puede crecer:

   ```bash
   for pvc in dc-tsdb-1 dc-tsdb-2 dc-tsdb-3; do
     kubectl -n <espacio de nombres de la instancia> patch pvc "$pvc" --type merge \
       -p '{"spec":{"resources":{"requests":{"storage":"48Gi"}}}}'
   done
   ```

3. Mantenga `spec.storage.size` del Cluster y el ajuste `timescale_storage` en el mismo tamaño, como
   se describió antes, para que el operador y la siguiente aplicación coincidan con lo que hizo.

Verifique antes de darlo por terminado:

- `kubectl -n <espacio de nombres de la instancia> get pvc` muestra la nueva capacidad en `CAPACITY`
  para todos los miembros. Una reclamación que queda con la condición `FileSystemResizePending`
  amplía su sistema de archivos al reiniciarse su pod: elimine el pod.
- `df -h` dentro del contenedor de la base de datos (`kubectl -n <espacio de nombres de la
  instancia> exec dc-tsdb-1 -c postgres -- df -h /var/lib/postgresql/data`) informa del sistema de
  archivos mayor.
- Las alertas se resuelven al subir la fracción libre: primero `DatabaseVolumeCritical` y
  `DatabaseVolumeAlmostFull`, luego `DatabaseVolumeFillingFast` y `DatabaseVolumeFillsWithinDay`.
  `CNPGClusterNotReady` y `EventStoreRefusingWrites` se resuelven cuando las réplicas en espera han
  reiniciado y `event-management` ha almacenado lo que retenía; `JetStreamDurableFallingBehind` se
  resuelve cuando se ha drenado el retraso en `resolved-events`. En el incidente, el retraso se drenó
  en unos 70 segundos una vez ampliado el volumen y cada evento quedó almacenado exactamente una vez.

## Réplicas y el presupuesto de conexiones de la base de datos {#connections}

El usuario de base de datos de cada instancia está limitado a las conexiones que sus servicios
pueden abrir: un pool completo por cada pod, duplicado para cubrir un despliegue. Una
superposición que sube las réplicas de un área que usa la base de datos relacional, o el pool de
esa área (`functionalAreas.<area>.config.rdbConfiguration.maxOpenConnections`), sube ese límite, y
`dcctl` pide a la base de datos compartida el límite mayor antes de cambiar nada. Si el
presupuesto de conexiones del clúster no puede darlo, la ejecución se detiene sin cambiar nada:
reduce la superposición, o sube el presupuesto volviendo a ejecutar el `dcctl install` con el que se instaló el clúster, añadiendo `--max-connections` con un valor mayor (por ejemplo `dcctl install local --max-connections 1000`, más `--kube-context`, `--ha` o `--compact` si se instaló con ellos). Quitar la
superposición devuelve las conexiones.

## Cambiar o quitar la superposición {#changing}

`--values` en `dcctl upgrade` **sustituye** la superposición registrada por completo; no se
fusiona con ella. Para añadir una clave, ponla en el archivo junto a las que ya estaban. Para
quitar la superposición y devolver la instancia a los valores que calcula `dcctl`:

```bash
dcctl upgrade local my-instance --reset-values
```

`--values` y `--reset-values` no se pueden combinar. `dcctl enable-area` y una actualización sin
ninguno de los dos indicadores conservan la superposición registrada tal como está.

## Ver antes qué cambiaría {#dry-run}

`--dry-run` imprime la diferencia entre la superposición registrada y la que dejaría la ejecución:

```text
  Overlay: changes to the recorded tuning:
    ~ functionalAreas.device-management.replicas: 3 -> 4
    + gogc: 400
    - functionalAreas.device-state.replicas: 2
[dry-run] would record the values overlay in the instance declaration
```

`+` es añadido, `-` eliminado y `~` modificado.

## Dónde se guarda la superposición {#where}

La superposición se guarda en la declaración de la instancia, dentro del clúster, como la
anotación `core.devicechain.io/values-overlay`. Cualquier máquina que llegue al clúster actualiza
con ella; no se guarda nada en un archivo de la máquina que hizo el bootstrap. Destruir la
instancia la elimina.

Quien pueda leer la declaración de la instancia puede leer la superposición. No puede fijar
credenciales, pero el `config` de un área es de formato libre, así que no pongas secretos en él.

## Cuando una actualización rechaza la superposición registrada {#stale}

Una versión nueva puede renombrar un valor o endurecer lo que acepta. Una actualización comprueba
la superposición registrada contra la versión a la que se mueve, antes de cambiar nada, y se
detiene nombrando la clave si ya no encaja. Corrígela con `--values <archivo-corregido>` o quítala
con `--reset-values`.

## Qué hace aplicarla {#rollout}

Una superposición se aplica a través de la misma release que cualquier otro ajuste, así que un
cambio que altera la configuración de un pod o el `config` de un área reinicia los pods de esa
área; los pods en ejecución no lo vuelven a leer. Los ajustes que mueven carga entre servicios,
como el número de réplicas en la ruta de eventos, conviene ensayarlos con `--dry-run` y aplicarlos
cuando la instancia está tranquila.
