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
| `perf-12k` | Un clúster sin `--ha`, unas 48 vCPU, 12 000 eventos por segundo sostenidos con los sobres de eventos activados. | Medido: 12 000 eventos por segundo sostenidos sin incidencias en GKE, con todos los eventos contabilizados. |

Un tamaño para una tasa mayor se añade solo después de que una medición demuestre esa tasa sin
incidencias. El nombre es la afirmación, así que un tamaño nunca se publica como objetivo.

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

Cada fila es un pod. Con `--ha`, los recuentos de abajo se multiplican por lo que decida HA, y el
tamaño de cada copia sigue siendo el indicado.

<!-- size-table:perf-12k:areas -->
| Servicio | Réplicas | Límite de CPU | Límite de memoria | Solicitud de CPU |
| --- | --- | --- | --- | --- |
| `device-management` | `2` | `3` | - | `500m` |
| `device-state` | `2` | `3` | `512Mi` | `250m` |
| `event-management` | - | `4` | `1Gi` | `400m` |
| `event-processing` | - | `2` | `1Gi` | `500m` |
| `event-sources` | `3` | `3` | `512Mi` | `600m` |
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
<!-- /size-table:perf-12k:infra -->

El volumen de JetStream se dimensiona para que quepa la reserva de la tasa de diseño; una
actualización nunca cambia el tamaño de un volumen que la instancia ya tiene, así que un tamaño
aplicado a una instancia existente conserva los volúmenes con los que se creó.

### Lo que `perf-12k` no establece {#perf-12k-not}

- **Los sobres de eventos.** Las cifras se midieron con ellos activados, y reducen la CPU en
  aproximadamente una quinta parte. Son un interruptor aparte, así que `dcctl bootstrap` te avisa
  cuando un tamaño `perf` se ejecuta con ellos desactivados.
- **`--ha`.** La tasa se midió con un solo servidor de broker. La combinación está permitida y
  `dcctl bootstrap` avisa de que no se ha medido.
- **La base de datos compartida y los nodos.** El volumen del almacén relacional y su
  `max_wal_size`, las solicitudes de CPU y memoria de las bases de datos, el volumen de
  monitorización y los grupos de nodos pertenecen al clúster y quedan como los dejó `dcctl install`
  y tu clúster. El clúster medido usó tres grupos de nodos: `t2d-standard-8` para los servicios
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
