---
sidebar_position: 8
title: Programar una flota
---

# Programar una flota

Una **campaña** envía comandos a los dispositivos de un grupo de entidades según un calendario y
conserva un registro de cada dispositivo al que lo intentó. El alumbrado público es el caso para el
que se construyó: atenuar cada luminaria en su propio ocaso, restaurarla en su propio amanecer,
omitir los días festivos y saber a la mañana siguiente qué luminarias no obedecieron y por qué.

Una campaña tiene tres partes:

- un **disparador** que indica cuándo se activa: una expresión cron, un calendario de fechas o el sol;
- uno o más **pasos**, cada uno un comando con una carga útil opcional y un retraso opcional tras el
  disparador;
- un **objetivo**, un grupo de entidades de dispositivos.

Cada vez que se activa el disparador, la plataforma planifica un **ciclo**. Un ciclo contiene un
**elemento** por dispositivo y por paso, y el elemento es la unidad de seguimiento y de reintento.
Internamente, cada grupo de elementos vencidos se convierte en un
[lote de comandos](./commanding-a-fleet.md) ordinario, de modo que cada dispositivo se valida contra
su contrato de capacidades, se retiene hasta su [ventana de mantenimiento](./maintenance-windows.md),
se sigue a lo largo del ciclo de vida del comando y caduca según su tiempo de vida, igual que
cualquier otro comando. Lee primero esas dos guías; esta solo cubre lo que añade la programación.

Las campañas viven en el punto de acceso `command-delivery`,
`https://<your-host>/api/command-delivery/graphql`. Crear, activar o desactivar y eliminar una
campaña, y reintentar sus fallos, requiere `command:write`. Leer requiere `command:read`. Toda
operación requiere además `device:read`, porque una campaña nombra un grupo y su seguimiento lista
los dispositivos del grupo. Un miembro limitado a una parte del tenant no puede usar campañas.

## Crear una campaña {#create-a-campaign}

```graphql
mutation {
  createCampaign(request: {
    token: "dusk-dim"
    name: "Dim at dusk"
    trigger: "{\"kind\":\"SOLAR\",\"zone\":\"Europe/Berlin\",\"event\":\"SUNSET\",\"offsetMinutes\":-15}"
    steps: [
      { type: "COMMAND", name: "setLevel", payload: "{\"level\":40}" }
      { type: "COMMAND", name: "setLevel", payload: "{\"level\":20}", delaySeconds: 21600 }
    ]
    groupToken: "city-streetlights"
  }) {
    campaign { token enabled plannedThrough lastError }
    rejection { code reason }
  }
}
```

Una definición que no puede aceptarse vuelve como `rejection`, no como error. Los códigos son
`CAMPAIGN_TRIGGER_INVALID`, `CAMPAIGN_STEPS_INVALID`, `CAMPAIGN_STEP_UNSUPPORTED`,
`CAMPAIGN_TARGET_INVALID`, `CAMPAIGN_FIELD_INVALID`, `TOKEN_IN_USE` y los códigos de carga útil y de
nombre de comando con los que se rechaza un comando individual.

| Campo | Significado |
| --- | --- |
| `token` | Único dentro del tenant. |
| `trigger` | Un documento JSON, descrito más abajo. |
| `steps` | De uno a diez pasos. Un paso es `COMMAND` con un `name`, una `payload` JSON opcional y `delaySeconds` de 0 a 86400 tras el disparador. |
| `groupToken` | El grupo de entidades objetivo. Un grupo dinámico puede seleccionar los dispositivos de un área. |
| `maxLatenessSeconds` | Cuánto puede retrasarse todavía el envío de un comando planificado, de 1 a 86400. Por defecto 900. |
| `expiresInSeconds` | El tiempo de vida de cada comando, de 1 a 604800, contado desde que se crea. Por defecto 3600. Es cuánto puede esperar un comando a un dispositivo inalcanzable o a una ventana de mantenimiento, así que puede durar más que `maxLatenessSeconds`, que limita cuándo envía el planificador, no cuándo recibe un dispositivo. Defínelo corto si una entrega tardía es peor que ninguna. |
| `enabled` | Si empieza activa. Por defecto true. |

El tipo de paso `ARTIFACT` forma parte del vocabulario pero esta versión no lo ejecuta; se rechaza
con `CAMPAIGN_STEP_UNSUPPORTED` en lugar de ignorarse. Para desplegar una actualización según un
calendario, usa un [despliegue gradual](../concepts/over-the-air-updates.md).

## Disparadores {#triggers}

Todo disparador necesita una zona horaria IANA en `zone`. Una zona vacía, o `Local`, se rechaza: un
calendario que siguiera al host se movería cuando se mueva el pod.

### Cron

```json
{"kind":"CRON","zone":"America/New_York","cron":"30 18 * * *"}
```

Cinco campos: minuto, hora, día del mes, mes y día de la semana. Cada uno admite `*`, un número,
`a-b`, `*/n`, `a-b/n` y listas separadas por comas. El día de la semana va de 0 a 7, con 0 y 7 como
domingo. Cuando ambos campos de día están restringidos, un día coincide si lo hace cualquiera de
los dos. Se rechazan los nombres, `?`, `L` y `@daily`.

### Calendario

```json
{"kind":"CALENDAR","zone":"Europe/London","dates":["2026-12-24","2026-12-31"],"times":["07:00","17:30"]}
```

Se activa a cada hora en cada fecha indicada, en la zona.

### Solar

```json
{"kind":"SOLAR","zone":"Europe/Berlin","event":"SUNSET","offsetMinutes":-15}
```

Se activa en `SUNSET` o `SUNRISE` más `offsetMinutes` (negativo es antes), **calculado por separado
para cada dispositivo a partir de su propia ubicación instalada**. El mismo disparador se activa por
tanto en un instante distinto para cada luminaria, y las luces de una ciudad se atenúan en una ola
que sigue al sol.

La ubicación instalada es la posición instalada del dispositivo, registrada con la mutación
`setInstalledPosition` o con las columnas de posición instalada de una importación de flota:

```graphql
mutation {
  setInstalledPosition(family: DEVICE, token: "lamp-0001",
    position: { latitude: 52.52, longitude: 13.40, source: SURVEY }) { latitude longitude }
}
```

Defínela al poner en servicio la luminaria. Deliberadamente no es la última posición notificada: una
luminaria no se mueve, y una cuyo receptor falla no debe atenuarse en un ocaso equivocado. El cálculo
es preciso a unos dos minutos entre aproximadamente 60 grados norte y sur.

Un evento solar no involucra ninguna zona horaria, porque el sol no la conoce. Aquí la zona solo
indica a qué calendario civil pertenecen el día y los festivos.

Dos tipos de dispositivo se registran como **omitidos** en lugar de adivinarse:

- un dispositivo sin posición instalada se omite como `NO_SITE`;
- un dispositivo donde el sol no sale o no se pone ese día (dentro de los círculos polares) se omite
  como `POLAR_DAY` o `POLAR_NIGHT`.

Si la campaña tiene un disparador solar y no pueden leerse en absoluto las ubicaciones de los
dispositivos, la planificación falla y el motivo se muestra en `lastError` de la campaña; no se
envía nada.

### Festivos

Cualquier disparador puede llevar `holidays`, una lista de fechas locales en la zona en las que no se
activa:

```json
{"kind":"SOLAR","zone":"Europe/Berlin","event":"SUNSET","holidays":["2026-12-24","2026-12-25"]}
```

Un festivo suprime una activación. Nunca la traslada a otro día.

### Horario de verano {#daylight-saving}

Una hora de cron o de calendario es hora de reloj en la zona. En los dos días al año en que eso es
ambiguo:

- una hora dentro del salto de primavera se activa en el instante en que el reloj salta, una vez;
- una hora que ocurre dos veces en la hora del retroceso otoñal se activa solo en su primera
  ocurrencia.

## Ventanas de mantenimiento {#maintenance-windows}

Una campaña no tiene una ventana propia. Cada comando que crea pasa por la misma puerta que cualquier
otro comando: un comando disruptivo a un dispositivo cuyo tipo tiene una
[ventana de mantenimiento](./maintenance-windows.md) se retiene hasta que la ventana se abre, y se
rechaza de inmediato si la ventana se abre solo después de que el comando caducaría. Un comando
retenido cuenta como `waiting` en los recuentos del ciclo. Define `expiresInSeconds` lo bastante
largo para cubrir la espera que quieras permitir.

## Qué ocurre si hay retraso o caída {#late-and-down}

El planificador se ejecuta cada 15 segundos en cada réplica, sin líder, y un dispositivo recibe el
comando una sola vez sin importar cuántas réplicas estén en marcha o reiniciándose: la reclamación de
cada elemento es una comparación y asignación en la base de datos, y el lote que crea lleva un token
derivado de sus elementos, de modo que una réplica que termina el trabajo de otra caída repite el
lote en lugar de duplicarlo.

Nunca se envía un comando pasado su **límite de retraso**. Un elemento que vence y no se reclama
dentro de `maxLatenessSeconds` (una caída, o la campaña estaba desactivada) se marca `SKIPPED` con
el motivo `MISSED`. Un comando de atenuación enviado a medianoche es peor que ninguno, así que una
campaña nunca se pone al día.

Desactivar una campaña detiene la planificación y el envío. Eliminarla cancela todo lo planificado y
aún no enviado, como `SKIPPED` con el motivo `CANCELLED`; los comandos ya creados no se tocan.

El [tope de comandos sin entregar](./commanding-a-fleet.md) del tenant sigue aplicándose. Si una flota
muy grande vence en el mismo instante, los dispositivos que superan el tope se registran como
`REFUSED` con `HELD_CEILING_EXCEEDED` y pueden reintentarse.

## Seguir un ciclo {#follow-a-cycle}

```graphql
query {
  campaignCycles(campaignToken: "dusk-dim", limit: 3) {
    key day resolved
    counts { pending dispatched refused skipped succeeded inFlight waiting failed unknown }
  }
}
```

`counts` responde a la pregunta de la mañana siguiente. Los primeros cinco números cuentan elementos
según el estado de la campaña: `pending`, `claimed`, `dispatched`, `refused`, `skipped`. El resto
cuenta los comandos detrás de los elementos enviados: `succeeded`, `inFlight` (de los cuales
`waiting` por una ventana), `failed` (terminaron sin éxito y es seguro reenviarlos) y `unknown`
(pueden haber actuado ya).

Para ver los dispositivos, recorre los elementos, opcionalmente por estado:

```graphql
query {
  campaignElements(campaignToken: "dusk-dim", cycleKey: "d2026-06-21", status: "SKIPPED", limit: 200) {
    id deviceToken step dueAt status reason commandStatus commandOutcome
  }
}
```

Pasa el último `id` como `afterId` para la página siguiente.

## Reintentar lo fallido {#retry-from-failed}

```graphql
mutation {
  retryCampaignFailed(campaignToken: "dusk-dim", cycleKey: "d2026-06-21", includeUnknown: false) {
    batches retried held
  }
}
```

Esto entrega los elementos sin éxito del ciclo al [reintento de lotes](./commanding-a-fleet.md) de la
plataforma, de modo que se aplican sus salvaguardas: a un dispositivo cuyo comando sigue en curso, o
que ya tuvo éxito, nunca se le envía una segunda copia, y a uno cuyo resultado es desconocido solo se
le envía si pasas `includeUnknown: true`, tras confirmar que actuar dos veces es inocuo. También se
reintentan los elementos que el lote rechazó. `held` cuenta los elementos que se dejan intactos
porque reenviar no es seguro.

Un elemento se reintenta una vez. Si el propio reintento falla, reintenta ese lote desde la
[API de lotes](./commanding-a-fleet.md).

## Límites {#limits}

- Un ciclo planifica como máximo 100 000 dispositivos.
- Los dispositivos de un ciclo son los miembros del grupo en el momento en que se planifica el ciclo, hasta 36 horas antes de que se active. Un dispositivo añadido después al grupo se incorpora al ciclo siguiente.
- Los ciclos terminados y sus elementos se conservan durante 30 días.
- Un objetivo es un grupo de entidades. No se admite apuntar directamente a un área; usa un grupo
  dinámico que seleccione los dispositivos del área.
- Un paso es un comando. Esta versión no ejecuta pasos de actualización.
