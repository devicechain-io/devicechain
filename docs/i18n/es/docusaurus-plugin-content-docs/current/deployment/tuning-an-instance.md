---
sidebar_position: 4.5
title: Ajustar una instancia
---

# Ajustar una instancia {#tuning-an-instance}

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
| `frontend.replicas`, `frontend.resources` | Los pods de la consola |
| `metrics.alerts`, `metrics.grafanaDashboards` | Reglas de alerta y paneles |
| `ingress.className`, `ingress.annotations`, `ingress.uploadMaxBodySize`, `networkPolicy.additionalAllowedCidrs`, `serviceAccount.annotations` | Cómo se publica el ingress y quién puede llegar a los pods |

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
