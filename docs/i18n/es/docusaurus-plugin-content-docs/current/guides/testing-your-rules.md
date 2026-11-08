---
sidebar_position: 3.7
title: Probar tus reglas
---

# Probar tus reglas {#testing-your-rules}

Una regla de detección que nadie ha probado es una suposición sobre lo que harán tus dispositivos. Esta guía muestra cómo escribir pruebas de reglas: describes los eventos que enviaría una flota, indicas qué deberían hacer las reglas al respecto y `dcctl test` comprueba la respuesta. La plataforma evalúa las reglas con su propio motor de detección, en tiempo de evento, así que una regla que espera siete días de silencio se prueba en una fracción de segundo, y nada de lo que hace toca tus dispositivos, alarmas o tráfico en producción.

:::note Esta función está en versión preliminar
Los formatos de escenario y de archivo de prueba llevan la versión `devicechain.io/ruletest/v1alpha1` y pueden cambiar de forma incompatible en una versión menor, con el cambio indicado en las notas de actualización de esa versión, hasta que se promuevan. Un archivo con una versión que el `dcctl` instalado no lee se rechaza, y también una clave que no conoce: una expectativa mal escrita que se ignorara pasaría.
:::

## Qué comprueba una prueba {#what-a-test-checks}

Una prueba se compone de dos archivos.

- Un **escenario** (`scenario.yaml`) dice qué ocurre: qué dispositivos existen, qué informa cada uno y cuándo, y qué falla entre el dispositivo y la plataforma.
- Un **archivo de prueba** (`*.test.yaml`) dice qué debería seguir: qué alarmas se activan o se despejan, y cuándo, cuáles nunca se activan y qué efecto secundario debería solicitar una regla.

La plataforma evalúa las reglas del escenario sobre los eventos que este genera y devuelve dos cosas. Las **transiciones** (edges) son los cambios de estado que derivaron las reglas, cada una activada o resuelta. Las **intenciones** (intents) son los efectos secundarios que esas transiciones provocarían: activar o despejar la alarma, el comando con su clave y argumentos, el webhook o el mensaje publicado con su cuerpo ya generado. Una intención es lo que la regla pide. No se ejecuta nada: no se activa ninguna alarma, no se encola ningún comando y ninguna solicitud sale de la plataforma.

Como la plataforma ejecuta las reglas, una prueba no puede discrepar de producción sobre lo que significa una regla. `dcctl` no guarda ninguna copia de la lógica de detección. Lo que guarda es la parte que no tiene lugar en un servidor: generar eventos a partir de una semilla y juzgar un resultado frente a lo que esperabas.

## Ejecuta tu primera prueba {#first-test}

Crea un directorio con dos archivos. El escenario describe una caldera que está bien durante media hora, se sobrecalienta y luego se enfría.

```yaml title="tests/scenario.yaml"
apiVersion: devicechain.io/ruletest/v1alpha1
kind: Scenario
profile: boiler            # el token del perfil de dispositivo cuyas reglas se prueban
start: 2026-03-01T00:00:00Z
duration: 2h
seed: 1
devices:
  - token: boiler-1
    streams:
      - metric: temperature
        every: 10m
        steps:
          - { at: 0s, value: 20 }
          - { at: 30m, value: 95 }
          - { at: 1h30m, value: 20 }
```

El archivo de prueba dice que las reglas publicadas deberían activar una alarma cuando la caldera se sobrecalienta, despejarla cuando se enfría y pedir que se arranque el ventilador.

```yaml title="tests/boiler.test.yaml"
apiVersion: devicechain.io/ruletest/v1alpha1
kind: RuleTest
expect:
  - name: raised when the boiler goes over 80
    raised: { device: boiler-1, after: 25m, within: 10m }
  - name: cleared once it cools
    cleared: { device: boiler-1, after: 85m, within: 10m }
  - name: the fan is started
    intent: { kind: sendCommand, command: set-fan, payload: { speed: 3 } }
```

Ejecútala como miembro del tenant. La identidad necesita `device:read`.

```bash
dcctl test ./tests --tenant acme --email you@example.com --password '<password>' --server localhost
```

El informe nombra cada expectativa y, en un fallo, dice qué se encontró en su lugar:

```text
evaluated tests/scenario.yaml: 13 events, 2 edges, 2 intents in 41ms
PASS  tests/boiler.test.yaml: raised when the boiler goes over 80
PASS  tests/boiler.test.yaml: cleared once it cools
FAIL  tests/boiler.test.yaml: the fan is started
        expected sendCommand, command "set-fan", payload {"speed":3} from any rule on any device to be emitted at least once, anywhere in the scenario; 0 matched; the intents emitted were: raiseAlarm "overheat" (rule hot, device boiler-1); clearAlarm "overheat" (rule hot, device boiler-1)
2 passed, 1 failed, 0 errored
```

El código de salida es `0` cuando todas las expectativas se cumplieron y distinto de cero cuando una falló, cuando no se pudo leer un archivo o cuando la plataforma rechazó el escenario. Un directorio sin ningún archivo `*.test.yaml` es un error, porque una ejecución que no comprueba nada no debe pasar. Para conservar el resultado para un sistema de CI, añade `--junit report.xml`.

## Qué reglas se prueban {#which-rules}

Por defecto un escenario evalúa las reglas **publicadas** del perfil: las que están activas ahora. Para probar una regla antes de publicarla, evalúa un **borrador**. Indica una definición de regla (el JSON con el que se almacena una regla) o un grafo de lienzo, como un archivo junto al escenario:

```yaml title="tests/scenario.yaml"
profile: boiler
ruleSet: DRAFT
draft:
  ruleDefinition: hot.rule.json      # o: graph: hot.canvas.json
```

Un borrador se evalúa sobre los eventos que genera el escenario, así que el perfil no tiene que estar publicado. Un grafo de lienzo puede contener varias reglas de detección; cada una se evalúa y se nombra en el resultado por el id de su nodo de condición. Un borrador `ruleDefinition` se llama `draft`.

Una prueba nombra una regla con `rule:` en una expectativa. Para una regla publicada es el token de la regla.

## Escribir un escenario {#scenario}

| Campo | Significado |
| --- | --- |
| `apiVersion`, `kind` | `devicechain.io/ruletest/v1alpha1` y `Scenario`. Obligatorios. |
| `profile` | El perfil de dispositivo cuyas reglas se evalúan. Obligatorio. |
| `ruleSet` | `PUBLISHED` (el valor por defecto) o `DRAFT`. |
| `draft` | Para `DRAFT`: exactamente uno de `ruleDefinition` y `graph`, una ruta relativa al escenario. |
| `start` | El instante en que empieza el escenario, como hora RFC 3339. Obligatorio: un valor por defecto de «ahora» haría distinta cada ejecución. |
| `duration` | Cuánto dura, por ejemplo `90m` o `7d`. Obligatorio. Tras el último evento el reloj del motor avanza hasta `start + duration`, que es lo que decide una regla que espera silencio. |
| `seed` | Siembra toda decisión aleatoria. La misma semilla genera siempre los mismos eventos. |
| `devices` | Los dispositivos simulados: un `token` y sus `streams`. |
| `faults` | Lo que falla de camino a la plataforma. |

Las duraciones admiten las unidades de Go más `d` (24 horas) y `w` (siete días): `90m`, `36h`, `7d`, `1d12h`. Un número solo se rechaza.

### Series {#streams}

Una serie (stream) es la serie de una métrica en un dispositivo. Indica la métrica, un intervalo `every` y exactamente un comportamiento. `from` y `until`, desplazamientos desde `start`, acotan la serie: una que termina antes del final del escenario es la forma de que un dispositivo se quede en silencio.

| Comportamiento | Ejemplo | Produce |
| --- | --- | --- |
| `constant` | `constant: 21.5` | El mismo valor en cada muestra. |
| `randomWalk` | `randomWalk: { start: 20, step: 0.5, min: 15, max: 30 }` | Un paseo que se mueve una cantidad uniforme en `[-step, +step]` en cada muestra y se mantiene dentro de `[min, max]`. |
| `steps` | `steps: [{ at: 0s, value: 20 }, { at: 1h, value: 95 }]` | Un valor que se mantiene desde cada paso hasta el siguiente. El primer paso debe estar en `0s`. |
| `playback` | `playback: { file: recorded.csv }` | Lecturas de un archivo grabado. |

Un archivo de reproducción es CSV con fila de encabezado, o NDJSON (`.ndjson` o `.jsonl`). Cada fila tiene un `at` (una hora RFC 3339 o un desplazamiento desde `start`, como `90m`) y un `value`, y puede nombrar una `metric`; una fila que no la nombra toma la de la serie. El archivo se nombra de forma relativa al escenario y no puede salir de su directorio.

```csv title="tests/recorded.csv"
at,value
0m,21.4
10m,21.9
2026-03-01T00:20:00Z,34.0
```

Las lecturas de un mismo dispositivo en el mismo instante se entregan como un solo evento, que es lo que envía un dispositivo que informa varias métricas en una muestra.

### Fallos {#faults}

Un fallo cambia cómo llegan los eventos a la plataforma, no lo que midió el dispositivo. Un fallo que no indica `device` se aplica a todos los dispositivos.

| Fallo | Ejemplo | Efecto |
| --- | --- | --- |
| `gap` | `gap: { device: boiler-1, from: 1h, for: 30m }` | Las muestras de la ventana se pierden para siempre. `metric` lo limita a una métrica. |
| `offline` | `offline: { device: boiler-1, from: 1h, for: 30m }` | El dispositivo sigue muestreando sin conexión y sube lo almacenado al reconectar: las muestras conservan su hora y llegan juntas al final de la ventana, detrás de lecturas que ocurrieron después. |
| `duplicate` | `duplicate: { probability: 0.1 }` | Cada evento se entrega una segunda vez con esa probabilidad. |
| `reorder` | `reorder: { probability: 0.2, maxDelay: 10m }` | Cada evento se retrasa un tiempo aleatorio de hasta `maxDelay` con esa probabilidad, de modo que llega detrás de eventos posteriores. |

La diferencia entre `gap` y `offline` importa: un hueco prueba qué hace una regla cuando faltan datos, y una ventana sin conexión prueba qué hace cuando los datos llegan tarde.

Los eventos se entregan al motor en el orden en que se entregan. El motor trata un evento tardío como lo hace en producción y, cuando tiene que descartar una lectura que llegó demasiado por detrás de las demás, el resultado lo dice y una expectativa fallida repite la nota.

## Escribir una prueba {#test-file}

Un archivo de prueba tiene un `apiVersion` y `kind: RuleTest`, un `name` opcional, un `scenario` opcional (una ruta relativa al archivo; por defecto `scenario.yaml` en el mismo directorio) y una lista de bloques `expect`. Cada bloque nombra exactamente uno de:

| Bloque | Se cumple cuando |
| --- | --- |
| `raised` | Al menos una transición activada coincidente cae en la ventana. |
| `cleared` | Al menos una transición resuelta coincidente cae en la ventana. |
| `neverRaised` | Ninguna transición activada coincidente cae en la ventana. |
| `intent` | Al menos un efecto secundario coincidente se solicita en la ventana. |
| `neverIntent` | Ningún efecto secundario coincidente se solicita en la ventana. |

Todo bloque admite un `name` opcional, que lo etiqueta en el informe y en JUnit. Todo esto acota lo que cuenta como coincidencia, y un campo omitido coincide con cualquier cosa:

- `rule` y `device` seleccionan la regla y el dispositivo.
- `after` y `within` dan la ventana como desplazamientos desde el `start` del escenario: de `after` a `after + within`, inclusive en ambos extremos. Omite `within` para llegar hasta el final del escenario.
- `count` (en `raised`, `cleared`, `intent`) exige exactamente ese número de coincidencias en lugar de al menos una.

Un bloque `intent` también puede comparar lo que dice el efecto secundario:

| Campo | Coincide con |
| --- | --- |
| `kind` | `raiseAlarm`, `clearAlarm`, `sendCommand`, `httpCall` o `publish`. |
| `edge` | `raised` o `resolved`: la transición que lo causó. |
| `command`, `payload` | La clave de un comando y su objeto de argumentos. El payload se compara como JSON, así que el orden de las claves y los espacios no importan, y un comando sin argumentos equivale a `{}`. |
| `alarmKey` | La clave bajo la que se activa o se despeja una alarma. |
| `connector`, `url`, `body`, `bodyContains` | El conector por el que se publica un mensaje, el endpoint al que llama un webhook y el cuerpo que generó la plantilla de la regla, exacto o por un fragmento. |

Un comando, un webhook y un mensaje publicado se solicitan solo en una transición activada; una alarma se despeja en la transición resuelta de una regla que la activa.

## Pruebas que pueden fallar {#tests-that-can-fail}

Una prueba no vale nada hasta que la has visto fallar. Cuando una prueba pasa, introduce a propósito un defecto en la regla y comprueba que la expectativa que lo nombra se pone en rojo: cambia un umbral, escribe mal la clave de un comando, invierte una comparación. Si un defecto cambia la regla y ninguna expectativa lo nota, la prueba no comprueba lo que creías. El conjunto de defectos plantados con el que se prueba la propia plataforma usa exactamente este método: un escenario, seis expectativas con nombre y cinco defectos deliberados, cada uno de los cuales debe hacer fallar las expectativas que lo nombran y ninguna otra.

Las ventanas son donde las pruebas suelen ser demasiado laxas. `raised: { device: boiler-1 }` pasa si la caldera activó en cualquier momento, incluido el equivocado. Da a la ventana un inicio y un final siempre que el momento importe.

## Ejecútalo en CI {#ci}

`dcctl test` necesita una identidad y una instancia en marcha, y escribe JUnit XML, que lee cualquier sistema de CI.

```bash
dcctl test ./tests \
  --tenant acme --email ci@example.com --password "$DC_PASSWORD" \
  --server platform.example.com --tls \
  --junit report.xml
```

Da a la identidad lo mínimo que necesita: `device:read` en el tenant. Si la cuenta usa autenticación de dos factores, consulta `--otp` en la [referencia de comandos](../reference/dcctl.md#dcctl-test).

## Qué no modela {#limits}

Una evaluación es exacta con las reglas y honesta sobre lo que deja fuera.

- **Atributos de dispositivo.** Un umbral que lee un atributo de dispositivo nunca coincide, porque aquí ningún dispositivo tiene atributos. Es la misma aproximación que hace la vista previa con historial.
- **Reglas con alcance de grupo.** Una regla publicada con alcance a un grupo de entidades se omite y se nombra como advertencia, porque un evento generado no lleva pertenencias a grupos. Un borrador no puede tener alcance de grupo.
- **Reglas de geovalla.** Una regla que prueba la pertenencia a una geovalla se rechaza, porque los eventos no llevan posición.
- **Comandos y conectores.** Una intención es lo que la regla pide. Si el comando existe en el dispositivo, o el conector existe en el tenant, se comprueba donde se envía la solicitud, no aquí.
- **Límites.** Una evaluación admite como máximo 250 000 eventos y, por defecto, un cuerpo de solicitud de 4 MiB; un escenario que supere cualquiera de los dos se rechaza antes de evaluar nada, con los ajustes que lo hicieron grande. Comparte el límite de concurrencia por tenant de la vista previa con historial, así que un tenant no puede ejecutar evaluaciones ilimitadas a la vez.

## Determinismo {#determinism}

Los mismos archivos de escenario producen siempre los mismos eventos, y los mismos eventos producen siempre el mismo resultado. Toda decisión aleatoria sale de un generador sembrado por el `seed` del escenario y la identidad de lo que genera, de modo que añadir un dispositivo a un escenario no cambia lo que informa otro. La evaluación no lee ningún reloj: la caducidad de un comando se mide desde el instante de la transición que lo causó, no desde la hora en que ejecutaste la prueba.

Esa es la propiedad que hace que un fallo merezca atención. Una prueba que falla vuelve a fallar, del mismo modo, hasta que se arregla la regla o la expectativa, y un arreglo se demuestra con la misma ejecución en verde.
