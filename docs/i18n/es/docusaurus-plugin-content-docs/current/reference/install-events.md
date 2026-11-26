---
sidebar_position: 4
title: Flujo de eventos de la instalación
---

# Flujo de eventos de la instalación {#install-event-stream}

`dcctl install --output json` (o `-o json`) ejecuta la instalación exactamente como siempre, y la informa como un flujo de eventos en lugar de texto: un objeto JSON por línea en stdout, en el orden en que ocurrieron. Está pensado para la automatización, como un trabajo de CI, una herramienta de aprovisionamiento o un script envoltorio, que necesita saber a qué etapa llegó una instalación, qué hizo cada etapa y cómo terminó la ejecución, sin analizar la salida pensada para personas.

El esquema del flujo es `dcctl.install.events/v1`, y cada línea lo lleva.

## Cómo ejecutarlo {#running-it}

```bash
dcctl install local --kube-context prod --yes -o json > install.ndjson
echo "exit status: $?"
```

- **Stdout contiene eventos y nada más.** Cada byte forma parte de una línea, y cada línea es un objeto JSON completo. No hay cabecera, ni color, ni secuencias de escape de terminal. El texto de progreso de la propia instalación llega dentro del flujo como eventos `log.line`.
- **Stderr conserva el motivo legible.** Cuando una ejecución falla, la misma frase que imprime la salida de texto se imprime en stderr, como siempre. Los avisos que se imprimen antes de que empiece la ejecución también van allí.
- **El código de salida no cambia:** `0` cuando la ejecución tuvo éxito o quedó planificada (`--dry-run`), `1` en otro caso.
- Un valor de `--output` distinto de `text` o `json` se rechaza antes de ejecutar nada, igual que una línea de órdenes que no se puede analizar (un indicador desconocido, por ejemplo). Ninguno de los dos escribe un flujo.

`--output text`, el valor predeterminado, es la salida que `dcctl install` ha impreso siempre.

## Preguntas {#prompts}

El flujo no es interactivo: **nunca lee stdin**.

- `--yes` responde la única confirmación que hace una instalación: si crear el clúster kind local cuando no existe.
- Sin `--yes`, esa pregunta no puede responderse. La ejecución se rechaza en ese punto, antes de crear el clúster, con un evento `step.failed` cuyo `code` es `run.prompt-unavailable`, cuyo `remedy` es `install.rerun-with-yes` y cuyo `effect` es `none_observed`. Vuelve a ejecutarla con `--yes`, o instala en un clúster existente con `--kube-context`, que no pregunta nada.
- Una instalación nunca pide un secreto, y `--yes` nunca proporciona uno.

## El sobre {#envelope}

Cada línea es un sobre alrededor de una carga útil:

| Campo | Significado |
| --- | --- |
| `schema` | `dcctl.install.events/v1` |
| `run_id` | Aleatorio en cada invocación; el mismo en todas las líneas de una ejecución |
| `seq` | Empieza en 1 y sube de uno en uno por línea, sin huecos |
| `time` | Cuándo ocurrió el evento, en UTC, RFC 3339 |
| `elapsed_ms` | Milisegundos desde que empezó la ejecución, de un reloj monótono |
| `operation` | `install` |
| `kind` | Qué carga útil es (ver más abajo) |
| `step_id` | La etapa a la que se refiere el evento, cuando se refiere a una |
| `parent_id` | La etapa padre, para una etapa anidada en otra |
| `attempt` | El intento de la etapa, desde 1, cuando el evento se refiere a una |
| `payload` | El evento en sí |

Las etapas de una instalación, en orden, son `install.inputs`, `install.preflight`, `install.cluster`, `install.claim`, `install.inspect`, `install.credentials`, `install.prepare`, `install.write-intent`, `install.operator`, `install.prerequisites`, `install.database-login`, `install.record`, `install.rdb-ready` e `install.summary`. `run.started` las enumera en su manifiesto. Una etapa que no aplica a una ejecución también empieza y termina, como `not_applicable`.

## Tipos de evento {#event-kinds}

| `kind` | Carga útil |
| --- | --- |
| `run.started` | `target` (`mode` es `managed-kind` o `existing-context`, y `context` lo nombra; un nombre de contexto es una etiqueta, no una identidad), `dry_run`, y `manifest`, las etapas que la ejecución puede realizar |
| `step.started` | `message` (un código), `params` opcionales, y `budget_ms` cuando la etapa tiene una espera acotada por un presupuesto conocido |
| `step.progress` | `metric_id`, `mode` (`indeterminate` o `measured`), `current`, `total` opcional, `unit` (`checks`, `resources`, `actions`, `deployments` o `replicas`) y `source`. Un valor es una instantánea y puede bajar |
| `step.finished` | `outcome`: `succeeded`, `unchanged`, `planned` (una ejecución de prueba) o `not_applicable`; y `duration_ms` |
| `step.failed` | `code`, `retry` (`transient`, `permanent` o `unknown`), `remedy` opcional, `cancelled`, `effect` (`none_observed`, `may_have_changed` o `applied`) y `duration_ms`. Nunca lleva el texto de un error |
| `preflight.observation` | Una línea del informe de comprobaciones previas: una `section`, un `check` con su `verdict` (`pass`, `info`, `warn`, `fail`, `unknown`, `not_needed` o `not_checked`), `text` y `fix`, o el `summary` final con sus `fails` y `warns` |
| `log.line` | `level`, `source` y `text`: una línea de la salida de la propia instalación, sin secuencias de escape de terminal |
| `prompt.requested` | `id`, `prompt` (`confirm`, `choice` o `secret`) y `question` (un código) |
| `prompt.resolved` | Solo `id` y `disposition` (`auto_confirmed` para `--yes`, `unavailable` cuando nadie pudo responder). Nunca lleva la respuesta |
| `run.cancelling` | La ejecución fue interrumpida: `reason`, la etapa activa (`active_step`), y si espera a una herramienta (`waiting_for_tool`) y durante cuánto tiempo (`stop_budget_ms`) |
| `run.summary` | El veredicto: `outcome` (`succeeded`, `planned`, `failed` o `cancelled`), `install_record`, `readiness`, `claim`, `failed_step`, `partial_state` (un efecto, como arriba) y órdenes `next` opcionales |

## Cómo leer el flujo {#reading-the-stream}

- **La última línea es el veredicto.** Todo flujo termina con exactamente un `run.summary`. Un flujo que termina sin él, porque el proceso fue terminado por ejemplo, significa que el resultado es **desconocido**, nunca un éxito.
- **Solo `run.summary` dice si la instalación tuvo éxito.** Un recuento de progreso completo, una etapa alcanzada o un registro sin errores no son un éxito.
- **Cada etapa iniciada termina exactamente una vez**, con `step.finished` o `step.failed`. Un reintento es un intento nuevo.
- **Una interrupción se informa y luego se espera.** `run.cancelling` llega en cuanto llega la interrupción; el `step.failed` de la etapa interrumpida (con `cancelled: true`) y el resumen llegan solo cuando el trabajo en curso se ha detenido.
- **`effect` dice qué pudo dejar una etapa fallida.** Solo `none_observed` significa que no se cambió nada. Volver a ejecutar la misma orden hace converger lo que una instalación interrumpida o fallida dejó a medias.
- **Las líneas de registro informan; nunca deciden.** Están acotadas y, con mucha salida, las más antiguas pueden descartarse antes de que un lector lento las tome. Los eventos de cualquier otro tipo nunca se descartan.
- **Una orden rechazada antes de que su ejecución pudiera empezar** (indicadores en conflicto, por ejemplo) escribe una línea: un `run.summary` con `outcome` `failed`, `failed_step` `install.inputs` y `partial_state` `none_observed`.
- **Léelo con tolerancia.** Versiones posteriores pueden añadir tipos de evento y campos dentro de `dcctl.install.events/v1`; omite lo que no reconozcas. Un cambio que rompería a un lector recibe un nombre de esquema nuevo.

## Secretos {#secrets}

El flujo nunca lleva las opciones de la instalación, sus credenciales ni el contenido de sus archivos. Los valores secretos que maneja la instalación, como la URL del webhook de alertas, la contraseña SMTP y las credenciales de copia de seguridad, también se eliminan de cualquier texto que llegue al flujo, sustituidos por `[redacted]`, por si una herramienta repite alguno. La resolución de una pregunta indica cómo terminó, nunca la respuesta.

## Ejemplo {#example}

Una instalación sin clúster kind y sin `--yes`, rechazada en la confirmación (manifiesto abreviado):

```json
{"schema":"dcctl.install.events/v1","run_id":"…","seq":1,"time":"2026-11-26T12:00:00Z","elapsed_ms":0,"operation":"install","kind":"run.started","payload":{"target":{"mode":"managed-kind","context":"kind-devicechain"},"dry_run":false,"manifest":[{"id":"install.inputs"}]}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":6,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"step.started","step_id":"install.cluster","attempt":1,"payload":{"message":"install.cluster"}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":7,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"prompt.requested","step_id":"install.cluster","payload":{"id":"prompt-1","prompt":"confirm","question":"install.create-kind-cluster","params":[{"name":"cluster","value":"devicechain"}]}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":8,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"prompt.resolved","step_id":"install.cluster","payload":{"id":"prompt-1","disposition":"unavailable"}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":9,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"step.failed","step_id":"install.cluster","attempt":1,"payload":{"code":"run.prompt-unavailable","retry":"permanent","remedy":"install.rerun-with-yes","cancelled":false,"effect":"none_observed","duration_ms":0}}
{"schema":"dcctl.install.events/v1","run_id":"…","seq":10,"time":"2026-11-26T12:00:00Z","elapsed_ms":4,"operation":"install","kind":"run.summary","payload":{"outcome":"failed","install_record":"not_read","readiness":"not_checked","claim":"not_claimed","failed_step":"install.cluster","partial_state":"none_observed"}}
```

Para seguir una ejecución mientras ocurre y detenerse en su veredicto:

```bash
dcctl install local --yes -o json | jq -c 'select(.kind == "step.started" or .kind == "run.summary")'
```

Todos los indicadores de `dcctl install` están en la [referencia de órdenes](./dcctl.md#dcctl-install).
