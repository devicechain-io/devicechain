---
sidebar_position: 10.6
title: Elegir un tamaño
---

# Elegir un tamaño {#choosing-a-size}

El tamaño de una instancia es una sola elección con nombre. `dcctl` convierte ese nombre en el
mismo conjunto de ajustes cada vez, de modo que una cifra medida en un clúster se puede
reproducir en otro, en lugar de reconstruirla a partir de una lista de entradas de superposición
y de exportaciones `TF_VAR_`.

Un tamaño es un **preajuste, no un dial**. Combina ajustes que ya existen, nunca cambia qué
servicios se ejecutan (eso sigue en el [perfil](./bootstrap.md)) y nunca activa un comportamiento:
los sobres de eventos y la confirmación MQTT a prueba de fallos quedan como los hayas puesto. Es
independiente de `--ha`, que decide cuántas copias se ejecutan, mientras que el tamaño decide lo
grande que es cada copia.

| Tamaño | Para | Estado |
| --- | --- | --- |
| `base` | Un portátil o un clúster pequeño. Es el valor por defecto: una instancia que no indica tamaño es `base` y genera exactamente lo que generaba antes de que existieran los tamaños. | El dimensionamiento estándar, descrito en [Dimensionamiento de servicios](./bootstrap.md#service-sizing). |
| `compact` | Un portátil con pocos recursos. Es un ajuste del clúster, elegido al instalar; `--compact` es lo mismo y se mantiene. | Descrito en [`--compact`](./bootstrap.md#--compact). |
| `perf-12k` | Un clúster sin `--ha`, grupos de plataforma de unas 40 vCPU, 12 000 eventos por segundo sostenidos con los sobres de eventos activados. | Medido: 12 000 eventos por segundo sostenidos sin incidencias en GKE (confirmación p99 por debajo de 4 ms), con todos los eventos contabilizados. Algunos valores se corrigen a partir de esa ejecución y aún no se han ejecutado tal cual; véase más abajo. |
| `perf-16k` | La misma configuración para 16 000 eventos por segundo sostenidos: un broker, una asignación del registro de escritura anticipada y un pod de event-processing más grandes. 20 000 eventos por segundo es un techo de rendimiento, no el objetivo. | Derivado de la misma ejecución en GKE, que mantuvo 16 000 eventos por segundo con exactitud; el dimensionamiento apunta a la latencia de confirmación de 50 ms que esa ejecución rozó (63 ms con el dimensionamiento de `perf-12k`). Aún no se ha ejecutado tal cual; véase más abajo. |
| `perf-20k` | Dimensionamiento para 20 000 eventos por segundo en un clúster sin particiones ni `--ha`, con sobres de eventos activados. | Derivado de la rampa en GKE; aún no se ha ejecutado tal cual. La latencia de confirmación no está validada. |

`perf-20k` es un objetivo de dimensionamiento de rendimiento. La ejecución medida almacenó exactamente 20 000 eventos por segundo, sin pérdidas ni duplicados, pero la confirmación p99 fue de 245 ms, por encima del objetivo de 50 ms. Este preajuste aumenta la CPU del broker en la misma configuración sin particiones; no promete ese objetivo de latencia.

## Almacenamiento del broker en GKE

Los brokers nuevos de GKE creados por `dcctl bootstrap` usan `premium-rwo` (`pd-ssd`) cuando está disponible la StorageClass SSD de GKE. Las escrituras síncronas de JetStream hacen que la latencia del disco sea importante, especialmente con tres réplicas bajo `--ha`; SSD evita depender del mínimo de IOPS de los discos balanced pequeños. Otras nubes y kind conservan la clase predeterminada del clúster. La entrada OpenTofu de instancia `nats_jetstream_storage_class` (entrada del módulo `jetstream_storage_class`) vale `""` por defecto y omite `config.jetstream.fileStore.pvc.storageClassName`. Para elegir otra clase, configúrala antes del primer apply.

La clase de un PVC existente y la plantilla de claims del StatefulSet son inmutables. `dcctl` deja esta entrada sin definir al actualizar y cuando bootstrap encuentra un broker o claim de JetStream existente. El módulo conserva su primera elección mediante `lifecycle.ignore_changes`, incluso si estaba sin definir, para que los nuevos valores predeterminados no la cambien. Los brokers GKE existentes conservan sus discos; este cambio no migra datos a SSD.

## Elegir uno {#choosing}

```bash
dcctl install <provider> --kube-context <context>
dcctl bootstrap <provider> <instance> --size perf-12k
```

`--size compact` en `dcctl install` equivale a `--compact`. Los tamaños `perf` se eligen por
instancia, con `dcctl bootstrap`. Un tamaño `perf` se rechaza en un clúster compacto.

El tamaño queda registrado en la instancia y `dcctl upgrade` lo vuelve a aplicar cada vez, de modo
que una actualización nunca cambia el tamaño de una instancia. Una versión que reajusta un tamaño
cambia las instancias registradas con él en su siguiente actualización, y sus notas de
actualización lo indican.

## Qué establece `perf-12k` {#perf-12k}

Cada fila es un pod. `--ha` decide el número de réplicas de `event-management` (dos) y el número de servidores del broker y de instancias de base de datos (tres de cada); los recuentos de réplicas de abajo para los demás servicios se mantienen, y el tamaño de cada copia sigue siendo el indicado.

<!-- size-table:perf-12k:areas -->
| Servicio | Réplicas | Límite de CPU | Límite de memoria | Solicitud de CPU |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `400m` |
| `device-state` | `2` | `3` | `512Mi` | `250m` |
| `event-management` | - | `4` | `1Gi` | `400m` |
| `event-processing` | - | `2` | `1Gi` | `500m` |
| `event-sources` | `3` | `3` | `256Mi` | `600m` |
<!-- /size-table:perf-12k:areas -->

Un guion significa que el tamaño no indica nada y se aplica el valor propio del chart.
`device-management` conserva su mínimo de memoria en lugar de un límite propio, porque un límite
escrito por un tamaño se saltaría ese mínimo. `event-management` mantiene una réplica salvo que
`--ha` decida otra cosa.

El broker, los volúmenes y la asignación del registro de escritura anticipada del almacén de eventos:

<!-- size-table:perf-12k:infra -->
| Ajuste | Valor |
| --- | --- |
| `streamDesignRate` | `12000` |
| `nats_cpu_request` | `4` |
| `nats_memory_request` | `4Gi` |
| `nats_memory_limit` | `8Gi` |
| `nats_jetstream_storage` | `28Gi` |
| `timescale_storage` | `160Gi` |
| `timescale_max_wal_size_mib` | `20480` |
| `postgres_max_wal_size_mib` | `4096` |
<!-- /size-table:perf-12k:infra -->

El volumen de JetStream se dimensiona para que quepa la reserva de la tasa de diseño; una
actualización nunca cambia el tamaño de un volumen que la instancia ya tiene, así que un tamaño
aplicado a una instancia existente conserva los volúmenes con los que se creó.

La última fila es del almacén relacional del clúster, no de la instancia. Un bootstrap no la pasa: fíjala con `dcctl install --relational-store-max-wal-mib 4096`, junto con un volumen que le deje espacio, por ejemplo `TF_VAR_postgres_storage=16Gi`. La elección se registra antes de aplicar y se conserva en instalaciones posteriores si se omite la opción. Un valor registrado se pasa explícitamente y prevalece sobre `TF_VAR_postgres_max_wal_size_mib`; `--relational-store-max-wal-mib=0` registra un restablecimiento al 1GB propio de PostgreSQL. Llega a la base de datos a través de la definición del clúster (el operador no permite `ALTER SYSTEM`). El cambio solo requiere una recarga y no reinicia ningún pod. El plan rechaza un valor superior a una cuarta parte de `postgres_storage`, porque el registro comparte el volumen de datos. Con el 1GB predeterminado, a esta tasa se fuerza un checkpoint por volumen de registro cada uno o dos minutos.

### Medido y derivado {#perf-12k-derived}

La configuración (las réplicas y los límites de CPU de arriba, y los 12 000 eventos por segundo) se midió en GKE con 40 vCPU de grupos de plataforma; el resto de las 48 vCPU de ese clúster era el generador de carga. Una ejecución posterior con la misma configuración mantuvo 12 000 eventos por segundo con todos los eventos contabilizados, una confirmación p99 de 3,7 ms, sin reinicios y sin ningún checkpoint solicitado por volumen de registro. De esa misma ejecución se leyeron tres valores que **aún no se han ejecutado tal cual**: la solicitud de 400m de `device-management`, el límite de memoria de 256Mi de `event-sources` (81 MiB residentes como máximo) y la asignación de 4096 MiB del almacén relacional. Las notas de actualización lo indicarán si una ronda posterior los cambia.

### Cuánto dura el volumen del almacén de eventos {#event-store-volume}

Un evento almacenado cuesta unos 756 bytes en el almacén de eventos, con índices y antes de comprimir (345 bytes en la tabla de eventos y 454 en la de mediciones). Con 12 000 eventos por segundo saturados son 32,7 GB por hora, así que el volumen de `160Gi` aguanta unas tres horas y media de ingesta continua tras la asignación del registro y el margen de trabajo. `perf-16k` establece `260Gi`, que aguantan unas cuatro horas a 16 000 eventos por segundo (43,6 GB por hora). Son volúmenes para ráfagas y pruebas de carga, no de retención. Los fragmentos anteriores a `lifecycle.compressAfterDays` (7 por defecto) se comprimen, así que para un uso sostenido configura el ciclo de vida de los datos y dimensiona el volumen en consecuencia: véase [Dimensionar el almacén de eventos](./tuning-an-instance.md#event-store-sizing). La alerta `DatabaseVolumeFillsWithinDay` salta cuando el volumen va camino de llenarse.

## Qué establece `perf-16k` {#perf-16k}

`perf-16k` es la configuración de `perf-12k` con elevados los parámetros que la rampa de 12 000 a 20 000 mostró como limitantes. Los recuentos de réplicas y los límites de CPU de los servicios no cambian; las solicitudes suben con el uso medido por pod.

<!-- size-table:perf-16k:areas -->
| Servicio | Réplicas | Límite de CPU | Límite de memoria | Solicitud de CPU |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `500m` |
| `device-state` | `2` | `3` | `512Mi` | `300m` |
| `event-management` | - | `4` | `1Gi` | `450m` |
| `event-processing` | - | `2` | `1536Mi` | `700m` |
| `event-sources` | `3` | `3` | `256Mi` | `750m` |
<!-- /size-table:perf-16k:areas -->

`event-processing` recibe 2 CPU y 1536Mi: con una CPU hubo limitación desde 16 000 eventos por segundo, y 1,5Gi con 2 CPU fue la única configuración cuyo retraso de detección se mantuvo por debajo de la marca de agua a 20 000.

El broker, los volúmenes y las asignaciones del registro de escritura anticipada:

<!-- size-table:perf-16k:infra -->
| Ajuste | Valor |
| --- | --- |
| `streamDesignRate` | `16000` |
| `nats_cpu_request` | `6` |
| `nats_memory_request` | `6Gi` |
| `nats_memory_limit` | `10Gi` |
| `nats_jetstream_storage` | `36Gi` |
| `timescale_storage` | `260Gi` |
| `timescale_max_wal_size_mib` | `40960` |
| `postgres_max_wal_size_mib` | `8192` |
<!-- /size-table:perf-16k:infra -->

La tasa de diseño de los flujos es 16000 y el volumen de JetStream 36Gi: la reserva para una interrupción de 15 minutos a esa tasa (unos 17,5 GiB, y 30 GiB con la confirmación a prueba de fallos activada) tiene que caber en él. El broker solicita 6 CPU porque usó 4,25 núcleos a 16 000 eventos por segundo y 5,2 a 20 000, más de los 4 que solicita `perf-12k`. Su límite de memoria es 10Gi (conjunto de trabajo de 3,6 GiB a 16 000 y 4,4 GiB a 20 000). La asignación del almacén de eventos está dimensionada para que los checkpoints sigan el reloj a 20 000 eventos por segundo. Como arriba, la última fila es del almacén relacional del clúster: indícala al instalar, con `TF_VAR_postgres_storage=32Gi` a su lado.

### Medido y derivado {#perf-16k-derived}

La ejecución medida usó el dimensionamiento de `perf-12k` y mantuvo 16 000 eventos por segundo con exactitud, con una confirmación p99 de 63 ms (el objetivo es 50 ms). Todos los valores de esta tabla se leen de esa ejecución y **aún no se han ejecutado tal cual**; la próxima ronda en GKE los confirmará, y las notas de actualización lo indicarán si cambian.

### 20 000 eventos por segundo {#perf-16k-ceiling}

Con la configuración de `perf-12k`, 20 000 eventos por segundo se almacenaron con exactitud (13,2 millones de eventos, ninguno perdido ni duplicado) sin perder ninguna conexión, con una confirmación p99 de 245 ms. Es un techo de rendimiento: la latencia de confirmación supera su objetivo y el límite es el único servidor del broker (5,2 núcleos en uso, pico de 5,5). El dimensionamiento de `perf-16k` lo soporta, pero no es una promesa sobre la latencia.

## Qué establece `perf-20k` {#perf-20k}

`perf-20k` es un objetivo de dimensionamiento de rendimiento. La ejecución medida almacenó exactamente 20 000 eventos por segundo, sin pérdidas ni duplicados, pero la confirmación p99 fue de 245 ms, por encima del objetivo de 50 ms. Este preajuste aumenta la CPU del broker en la misma configuración sin particiones; no promete ese objetivo de latencia.

Mantiene las réplicas y los límites de CPU de `perf-12k`. Las solicitudes siguen la misma regla: CPU medida por pod a la tasa de diseño, multiplicada por 1,2–1,5 y redondeada hacia arriba a 50m. Event-processing recibe 1536Mi porque ese caso mantuvo el retraso de detección bajo control a 20k.

<!-- size-table:perf-20k:areas -->
| Servicio | Réplicas | Límite de CPU | Límite de memoria | Solicitud de CPU |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `600m` |
| `device-state` | `2` | `3` | `512Mi` | `400m` |
| `event-management` | - | `4` | `1Gi` | `650m` |
| `event-processing` | - | `2` | `1536Mi` | `950m` |
| `event-sources` | `3` | `3` | `256Mi` | `900m` |
<!-- /size-table:perf-20k:areas -->

<!-- size-table:perf-20k:infra -->
| Ajuste | Valor |
| --- | --- |
| `streamDesignRate` | `20000` |
| `nats_cpu_request` | `6` |
| `nats_memory_request` | `6Gi` |
| `nats_memory_limit` | `10Gi` |
| `nats_jetstream_storage` | `40Gi` |
| `timescale_storage` | `260Gi` |
| `timescale_max_wal_size_mib` | `40960` |
| `postgres_max_wal_size_mib` | `8192` |
<!-- /size-table:perf-20k:infra -->

El broker único usó 5,2 núcleos (pico de 5,5) y 4,4Gi de memoria a 20k: se reservan 6 CPU y 6Gi, con un límite de memoria de 10Gi. El volumen JetStream de 40Gi admite la reserva de 15 minutos incluso con confirmaciones de cierre seguro activadas. Los 260Gi del almacén de eventos y los 40960 MiB de WAL proceden de la ejecución medida; es almacenamiento para ráfagas, no retención. Los 8192 MiB de WAL relacional se configuran por separado al instalar con `dcctl install --relational-store-max-wal-mib 8192` y `TF_VAR_postgres_storage=32Gi`. Estos valores aún no se han ejecutado tal cual.

### Lo que los tamaños `perf` no establecen {#perf-12k-not}

- **Los sobres de eventos.** Las cifras se midieron con ellos activados, y reducen la CPU en
  aproximadamente una quinta parte. Son un interruptor aparte, así que `dcctl bootstrap` te avisa
  cuando un tamaño `perf` se ejecuta con ellos desactivados.
- **`--ha`.** La tasa se midió con un solo servidor de broker. La combinación está permitida y
  `dcctl bootstrap` avisa de que no se ha medido.
- **Ajuste a los nodos con `--ha`.** Esta es una comprobación de `dcctl bootstrap`; upgrade no la ejecuta. Con `--ha`, un tamaño `perf` se rechaza cuando menos de tres nodos planificables tienen, cada uno, la solicitud del broker (6 CPU y 6Gi para `perf-16k` y `perf-20k`, 4 CPU y 4Gi para `perf-12k`) como CPU y memoria asignables. La comprobación se ejecuta antes de declarar la instancia, para que funcione una nueva ejecución con un `--size` menor. Cada nodo se juzga por separado: la CPU de un nodo y la memoria de otro no se suman. El mensaje indica el nodo más grande que vio y las soluciones: declarar las variables de entorno `TF_VAR_nats_cpu_request` / `TF_VAR_nats_memory_request`, o establecer `nats_cpu_request` / `nats_memory_request` en el archivo `terraform.tfvars` de la raíz de la instancia, que solo se lee una vez que esa raíz existe, en una nueva ejecución. En el primer bootstrap, usa las variables de entorno. Las solicitudes que declares tú mismo quedan sin comprobar por `dcctl`. Otras opciones son un `--size` menor o nodos más grandes. Solo compara la capacidad asignable, no lo que está libre ni lo que decidiría el planificador. También rechaza con `--dry-run`, pero si no se puede alcanzar el clúster solo emite un aviso.
- **La base de datos compartida y los nodos.** El volumen del almacén relacional, las solicitudes
  de CPU y memoria de las bases de datos, el volumen de monitorización y los grupos de nodos
  pertenecen al clúster y quedan como los dejó `dcctl install` y tu clúster. El `max_wal_size` del
  almacén relacional es un ajuste del mismo tipo: el tamaño publica el valor con el que se midió y
  lo indicas al instalar (véase arriba). El clúster medido usó tres grupos de nodos: `t2d-standard-8` para los servicios
  (dos nodos), `n2d-standard-8` para las bases de datos (dos nodos) y un `t2d-standard-8` solo para
  el broker, con el generador de carga en otro sitio.

## Qué valor prevalece {#precedence}

De menor a mayor prioridad:

1. los valores por defecto del chart y de las variables de OpenTofu;
2. el tamaño;
3. lo que `dcctl` calcula por sí mismo: los recuentos de réplicas de HA, el interruptor de los
   sobres, los servicios que se ejecutan y los volúmenes que la instancia ya tiene;
4. tu superposición `--values`;
5. una variable de OpenTofu que indiques en `TF_VAR_*`, `terraform.tfvars` o un archivo
   `*.auto.tfvars`.

Prevalece lo que tú indiques. `dcctl` no pasa en absoluto una variable de OpenTofu del tamaño
cuando ya la has indicado tú, porque de lo contrario OpenTofu tomaría el valor del tamaño en lugar
del tuyo. La línea que imprime `dcctl bootstrap` nombra el tamaño que aplicó.

## Cambiar el tamaño de una instancia en ejecución {#changing}

Una instancia conserva el tamaño con el que se hizo el bootstrap. Pasar una instancia existente a
otro tamaño no forma parte de esta versión; hasta entonces, indica los ajustes que quieras en una
superposición, que prevalece sobre cualquier tamaño. Sea cual sea el tamaño, `dcctl` no puede
ampliar un volumen de JetStream: consulta
[cuánto tiempo pueden esperar los flujos de eventos durante una interrupción](./observability.md#outage-runway)
para el procedimiento.
