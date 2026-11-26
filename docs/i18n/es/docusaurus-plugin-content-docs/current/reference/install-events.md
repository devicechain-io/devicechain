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
- **El código de salida es el veredicto del resumen:** `0` cuando la ejecución tuvo éxito o quedó planificada (`--dry-run`), `1` en otro caso, sea cual sea el código con el que terminó una herramienta que ejecutó la instalación. Una ejecución que falla de forma inesperada dentro de dcctl también termina su flujo con un resumen fallido y sale con `1`.
- Un valor de `--output` distinto de `text` o `json` se rechaza antes de ejecutar nada, igual que una línea de órdenes que no se puede analizar (un indicador desconocido, por ejemplo). Ninguno de los dos escribe un flujo.

`--output text`, el valor predeterminado, es la salida que `dcctl install` ha impreso siempre, y sale de la misma forma: `0` o `1`.

`--help` es ayuda, no una ejecución: con `--output json` se imprime en stderr, y stdout queda vacío.

## Preguntas {#prompts}

El flujo no es interactivo: **nunca lee stdin**.

- `--yes` responde la única confirmación que hace una instalación: si crear el clúster kind local cuando no existe. La resuelve sin preguntar, así que una ejecución con `--yes` no tiene eventos `prompt.requested` ni `prompt.resolved` para ella.
- Sin `--yes`, esa pregunta no puede responderse. La ejecución se rechaza en ese punto, antes de crear el clúster, con un evento `step.failed` cuyo `code` es `run.prompt-unavailable`, cuyo `remedy` es `install.rerun-with-yes` y cuyo `effect` es `none_observed`. Vuelve a ejecutarla con `--yes`, o instala en un clúster existente con `--kube-context`, que no pregunta nada.
- Una instalación nunca pide un secreto, y `--yes` nunca proporciona uno.
- Nada de lo que ejecuta la instalación recibe stdin tampoco. Un plugin de credenciales exec del kubeconfig se ejecuta de forma no interactiva, y uno que exige un terminal (`interactiveMode: Always`) falla en lugar de preguntar.

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

La segunda columna enumera todos los campos que puede llevar una carga útil de ese tipo. Un campo que no se aplica a un evento se omite.

| `kind` | Campos | Significado |
| --- | --- | --- |
| `run.started` | `target`, `dry_run`, `manifest` | `target` dice a dónde va la ejecución: su `mode` es `managed-kind` o `existing-context`, y su `context` lo nombra (un nombre de contexto es una etiqueta, no una identidad). `manifest` enumera las etapas que la ejecución puede realizar |
| `step.started` | `message`, `params`, `budget_ms` | `message` es un código, con `params` opcionales. `budget_ms` aparece cuando la etapa tiene una espera acotada por un presupuesto conocido |
| `step.progress` | `metric_id`, `mode`, `current`, `total`, `unit`, `source` | `mode` es `indeterminate` o `measured`, `total` es opcional y `unit` es `checks`, `resources`, `actions`, `deployments` o `replicas`. Un valor es una instantánea y puede bajar. Los valores se agrupan: consulta [Cómo leer el flujo](#reading-the-stream) |
| `step.finished` | `outcome`, `duration_ms` | `outcome` es `succeeded`, `unchanged`, `planned` (una ejecución de prueba) o `not_applicable` |
| `step.failed` | `code`, `retry`, `remedy`, `remedy_params`, `cancelled`, `effect`, `duration_ms` | `retry` es `transient`, `permanent` o `unknown`. `remedy` es un código opcional, con `remedy_params` opcionales. `effect` es `none_observed`, `may_have_changed` o `applied`. Nunca lleva el texto de un error |
| `preflight.observation` | `observation`, `section`, `verdict`, `text`, `highlight`, `fix`, `fails`, `warns` | Una fila del informe de comprobaciones previas, y `observation` dice qué tipo de fila es. `section`: empieza un grupo de comprobaciones, nombrado por el código `section`. `check`: una comprobación, con su `verdict` (`pass`, `info`, `warn`, `fail`, `unknown`, `not_needed` o `not_checked`), su `text`, un `highlight` opcional (un valor que la comprobación destaca, como el contexto de kube actual, mostrado después de `text`) y, para una advertencia o un fallo, un `fix`. `summary`: los recuentos finales `fails` y `warns` del informe |
| `log.line` | `level`, `source`, `code`, `text` | Una línea de la salida de la propia instalación, sin secuencias de escape de terminal. `code` aparece cuando la línea tiene uno |
| `prompt.requested` | `id`, `prompt`, `question`, `params` | `prompt` es `confirm`, `choice` o `secret`. `question` es un código, con `params` opcionales |
| `prompt.resolved` | `id`, `disposition` | Cómo terminó la pregunta, nunca la respuesta. Una instalación resuelve su única pregunta como `unavailable` cuando nadie pudo responder, o como `cancelled` cuando la ejecución se interrumpió mientras preguntaba |
| `run.cancelling` | `reason`, `active_step`, `waiting_for_tool`, `stop_budget_ms` | La ejecución fue interrumpida: por qué, la etapa en la que estaba, y si espera a que una herramienta se detenga y cuánto tiempo se le dio |
| `run.summary` | `outcome`, `install_record`, `readiness`, `claim`, `failed_step`, `partial_state`, `next`, `credentials`, `delivery_incomplete` | El veredicto. `outcome` es `succeeded`, `planned`, `failed` o `cancelled`. `partial_state` es un efecto, como arriba. `next` enumera órdenes para ejecutar a continuación. `credentials` nombra los secretos de Kubernetes que guardan las credenciales que creó la instalación (`namespace`, `name` y `key`), nunca sus valores. `delivery_incomplete` es `true` cuando no se pudo escribir un evento, y una ejecución así nunca informa de éxito |

## Cómo leer el flujo {#reading-the-stream}

- **La última línea es el veredicto.** Todo flujo termina con exactamente un `run.summary`. Un flujo que termina sin él, porque el proceso fue terminado por ejemplo, significa que el resultado es **desconocido**, nunca un éxito.
- **Solo `run.summary` dice si la instalación tuvo éxito.** Un recuento de progreso completo, una etapa alcanzada o un registro sin errores no son un éxito.
- **Cada etapa iniciada termina exactamente una vez**, con `step.finished` o `step.failed`. Un reintento es un intento nuevo.
- **Una interrupción se informa y luego se espera, dentro de un presupuesto.** `run.cancelling` llega en cuanto llega la interrupción. El `step.failed` de la etapa interrumpida (con `cancelled: true`) y el resumen llegan cuando el trabajo en curso se ha detenido, con una excepción: una orden de OpenTofu interrumpida que no se ve terminar dentro de su presupuesto de parada ordenada y un margen posterior. dcctl deja entonces de esperarla, la etapa falla con `code` `run.tool-abandoned` y `effect` `may_have_changed`, y el resumen llega mientras esa orden, o un plugin de proveedor que inició, puede seguir cambiando el clúster, incluso después de que dcctl haya terminado. dcctl no empieza otra ejecución en el mismo proceso hasta que esa orden haya terminado, pero un `dcctl install` nuevo no puede verla: antes de volver a ejecutar, asegúrate de que no sigue en marcha ningún proceso de OpenTofu de la ejecución interrumpida.
- **`effect` dice qué pudo dejar una etapa fallida.** Solo `none_observed` significa que no se cambió nada. Volver a ejecutar la misma orden hace converger lo que una instalación interrumpida o fallida dejó a medias.
- **Las líneas de registro informan; nunca deciden.** Están acotadas y, con mucha salida, las más antiguas pueden descartarse antes de que un lector lento las tome.
- **El progreso se agrupa.** Cuando una etapa informa del progreso más rápido de lo que el flujo se escribe o se lee, un valor más reciente de la misma métrica sustituye a uno aún no escrito, así que pueden omitirse valores intermedios. El último valor que una etapa informó antes de terminar siempre llega, antes de su `step.finished` o `step.failed`.
- **Los eventos de cualquier otro tipo nunca se descartan.**
- **Una orden rechazada antes de que su ejecución pudiera empezar** (indicadores en conflicto, por ejemplo) escribe una línea: un `run.summary` con `outcome` `failed`, `failed_step` `install.inputs` y `partial_state` `none_observed`.
- **Léelo con tolerancia.** Versiones posteriores pueden añadir tipos de evento y campos dentro de `dcctl.install.events/v1`; omite lo que no reconozcas. Un cambio que rompería a un lector recibe un nombre de esquema nuevo.

## Secretos {#secrets}

El flujo nunca lleva las opciones de la instalación, sus credenciales ni el contenido de sus archivos. La resolución de una pregunta indica cómo terminó, nunca la respuesta.

Como segunda línea de defensa, por si una herramienta repite alguno, estos valores se eliminan de cualquier texto que imprime dcctl y se sustituyen por `[redacted]`:

- los valores secretos que maneja la instalación, como la URL del webhook de alertas, la contraseña SMTP y las credenciales de copia de seguridad;
- las credenciales de tu kubeconfig: tokens, contraseñas, claves de cliente, la contraseña de una URL de servidor o de proxy (tal cual y con escape de URL), los valores del entorno de los plugins exec y de la configuración de los proveedores de autenticación, y los argumentos de un plugin exec que contienen una credencial (el valor de un indicador como `--client-secret` o `--token`, y un argumento con forma de clave).

Con `--output json`, eso abarca todos los eventos, el error que se imprime en stderr (incluidos los diagnósticos de una herramienta añadidos a él) y todo lo demás que llegue a la stdout o la stderr de dcctl mientras se ejecuta la instalación, incluida la stderr de un plugin de credenciales exec del kubeconfig. Esa stderr se redacta línea a línea, así que puede llegar algo después de que el plugin la escribiera.

Hay límites:

- Un valor de menos de 4 bytes no puede eliminarse sin eliminar texto normal con él. No se redacta, y la ejecución indica cuántos valores así encontró en una línea de registro `warning:`.
- Una credencial de tu kubeconfig solo se sustituye donde aparece como palabra completa, nunca dentro de una más larga, y nunca cambia lo que hace la ejecución. Con `AWS_PROFILE=prod`, `--profile prod` muestra `--profile [redacted]` y `production` queda intacto, y un usuario que la instalación no usa, con la contraseña `install`, no la detiene. Los secretos que maneja el propio dcctl se sustituyen dondequiera que aparezcan.
- Con `--output text`, solo se redacta el error que imprime dcctl. El resto es la salida que `dcctl install` ha impreso siempre, que puede mostrarte una credencial a propósito (el resumen de acceso de un bootstrap), y las herramientas que ejecuta, entre ellas kind y un plugin de credenciales exec del kubeconfig, escriben en el terminal como siempre.

Un kubeconfig que dcctl no puede leer, o con el que no puede crear un cliente para un contexto, se informa por el tipo de problema, el archivo y el nombre del contexto, nunca con el motivo del cliente de Kubernetes, que puede citar las credenciales y URL del archivo. Esto también se aplica a `--output text`.

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
