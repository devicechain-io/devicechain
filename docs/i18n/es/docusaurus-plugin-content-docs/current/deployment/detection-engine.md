---
sidebar_position: 7
title: Cómo operar el motor de detección
---

# Cómo operar el motor de detección

Las [reglas de detección](../concepts/event-processing.md) las evalúa un servicio que se comporta de
forma distinta al resto de la plataforma: mantiene estado vivo en memoria, se ejecuta como una
**única instancia** y evalúa sobre el **tiempo del evento** en lugar de sobre el reloj. Esta página
es el contrato del operador: qué se gana con eso, qué cuesta y cómo distinguir un motor sano de uno
atascado.

Si lo que busca es qué puede expresar una regla o cómo autorarla, empiece por
[Procesamiento de eventos y alarmas](../concepts/event-processing.md).

## Una sola instancia, a propósito

El motor de detección se ejecuta como **exactamente una réplica**, y el chart lo distribuye así, con
una estrategia de despliegue de tipo *recreate*.

Esto no es una limitación de escalado a la espera de que alguien la levante a la ligera. El motor
mantiene en memoria cada ventana abierta, cada temporizador en marcha y cada enclavamiento de flanco
ascendente, y los confirma como un único punto de control (checkpoint). Dos motores leyendo el mismo
flujo verían cada uno solo una parte de él, y cada uno confirmaría un estado construido a partir de
una vista parcial.

Tres cosas protegen ese invariante, y conviene saber que no son igual de fuertes:

1. **La estrategia de despliegue** impide que un despliegue solape la instancia antigua con la
   nueva. Solo cubre los despliegues.
2. **El chart se niega a renderizar** una configuración que pida más de una réplica junto a esa
   estrategia.
3. **El propio motor se niega a confirmar** un punto de control que quede por detrás de uno ya
   almacenado. Si dos motores llegan a ejecutarse brevemente —una expulsión, el drenaje de un nodo o
   un pod eliminado a mano programan un reemplazo de inmediato—, el que se quedó atrás se detiene en
   lugar de sobrescribir.

:::warning Un drenaje puede ejecutar dos motores durante unos segundos
Solo la vía del despliegue está cubierta por completo. En una expulsión o un drenaje de nodo, el
reemplazo se programa antes de que el original se haya detenido, así que durante unos segundos dos
motores pueden consumir el mismo flujo. La valla del punto de control lo contiene —el que pierde se
detiene—, pero es la razón para preferir un despliegue deliberado a drenar el nodo en el que
casualmente está el motor.
:::

Como hay una sola réplica, no hay presupuesto de interrupción de pods. Drenar su nodo detiene la
detección hasta
que el pod se reprograma.

## Qué cuesta un reinicio

Un reinicio es rutina, no un incidente. Al arrancar, el motor recarga su último punto de control y
reproduce el flujo desde esa posición, de modo que vuelve a derivar el estado que tenía.

| | Qué ocurre |
|---|---|
| **Eventos ya procesados y confirmados** | No se pierde nada. Solo se acusa recibo de los mensajes *después* de que se confirme el punto de control que los incluye, así que todo lo que no llegó a confirmarse se vuelve a entregar. |
| **Alarmas y comandos ya enviados** | Se vuelven a derivar y se reenvían, y luego se colapsan: una alarma es una actualización idempotente, y un comando lleva una clave que impide un segundo encolado. |
| **Webhooks salientes y publicaciones a conectores ya enviados** | Se vuelven a derivar y **se envían de nuevo**. Nada del lado de la plataforma los colapsa; vea la sección de entrega más abajo. |
| **Conteos de disparo de las reglas** | **Se cuentan de más.** Un reprocesamiento vuelve a incrementarlos. La hora del *último disparo* sí es correcta; trate el conteo como un mínimo, no como un total exacto. |
| **Ventanas abiertas, sostenimientos y temporizadores** | Se restauran desde el punto de control. Un sostenimiento que iba a medias sigue a medias. |
| **Reglas que usan un umbral dinámico (basado en atributos)** | Vea la advertencia de abajo. |

:::caution Umbrales dinámicos y reprocesamiento
Una regla cuyo umbral lee un **atributo de dispositivo** no es del todo segura ante
reprocesamiento. Al reprocesar, el atributo se lee con su valor *actual* y no con el que tenía en el
tiempo del evento original. Si el atributo cambió en el intervalo, puede perderse un disparo o
producirse uno de más. Las reglas con umbral **fijo** no se ven afectadas. Si depende del
comportamiento exacto del reprocesamiento —para auditar un borrado o reproducir un incidente—,
prefiera umbrales fijos.
:::

## Entrega: todo es al menos una vez

La vía de la telemetría es «al menos una vez» de extremo a extremo, así que planifique contando con
una repetición y no con exactamente una entrega.

- Una **detección** puede producirse más de una vez y se colapsa por su identidad.
- La actualización de una **alarma** es idempotente: una repetición aterriza en la misma alarma.
- Un **comando** lleva una clave derivada del disparo, así que una repetición nunca encola un
  segundo comando.
- **Un webhook saliente o una publicación a un conector** es la única acción que un reintento puede
  duplicar de verdad. Cada solicitud lleva un encabezado `X-DC-Idempotency-Key` derivado del
  disparo, pero colapsar por él es tarea del **endpoint receptor**. Para destinos de cola y de
  broker la clave viaja como metadatos, sobre los que la mayoría de los brokers no puede actuar.
  **Diseñe los receptores de salida para que sean idempotentes.**

Cuando un sistema aguas abajo no está disponible, el mensaje se deja sin acusar recibo y se
reintenta con un temporizador, en lugar de martillear el destino. Tras cinco intentos de entrega,
repartidos en unos cuatro minutos en total:

- la solicitud de un **conector de salida** se envía a la **cola de mensajes no entregados**, de
  modo que se puede inspeccionar o reprocesar;
- una **detección** **se descarta con un error ruidoso**: en esa vía no hay cola de mensajes no
  entregados. Un *levantamiento* descartado no volverá a aparecer hasta que la condición se despeje
  y se vuelva a incumplir; una *resolución* descartada deja activa una alarma que debería haberse
  limpiado.

La alerta `ReactPoisonDropping` existe exactamente para ese caso y debe tratarse como urgente.

:::caution Una acción que falla se lleva por delante a las que van después
Las acciones de una regla se ejecutan en el orden en que están listadas, y una acción que falla
detiene al resto. En cada reintento, las acciones *anteriores* a ella se vuelven a ejecutar, y las
*posteriores* siguen sin haberse ejecutado nunca; así que, si el evento acaba descartándose, esas
acciones posteriores se pierden sin haber llegado a intentarse ni una sola vez.

**Ordene las acciones de una regla de modo que la importante vaya primero.** Si una regla levanta
una alarma y además llama a un webhook, poner la alarma primero significa que un endpoint inestable
no puede costarle la alarma.
:::

## Tiempos: qué significa «cuándo»

El motor trabaja sobre el **tiempo del evento** —la marca de tiempo de la lectura— y no sobre el
momento en que llegó el mensaje. De ahí se derivan dos ajustes.

**La tolerancia a eventos tardíos** es cuánto espera el motor a los eventos fuera de orden antes de
dar un instante por asentado. Súbala si su flota almacena lecturas y las sube por lotes, o si un
salto aguas arriba puede atascarse; el costo es que toda decisión basada en el tiempo se retrasa
otro tanto.

**Una marca de tiempo informada por el dispositivo se recorta** si está demasiado en el *futuro*
respecto al momento en que la plataforma la recibió, para que un dispositivo con el reloj mal puesto
no pueda arrastrar hacia adelante el sentido del tiempo de todo el motor. Las marcas de tiempo en el
pasado se tratan como retraso, no se recortan.

### ¿Con qué rapidez puede dispararse una regla de ausencia?

Una regla de «ausencia» o «silencio» no puede dispararse en el instante en que un dispositivo se
calla: no llega nada que active la evaluación. El mínimo es:

> el tiempo de espera de la regla **+** la tolerancia a eventos tardíos **+** el mayor entre el
> intervalo de comprobación de inactividad y el intervalo de punto de control **+** un tick

Con los valores predeterminados de fábrica, eso son aproximadamente **el tiempo de espera de la
regla más unos quince segundos**. Fije ese tiempo de espera al silencio que realmente le importa y
espere la detección poco después, no exactamente en ese instante.

Hay dos maneras en que se dispara una regla de ausencia, y solo una de ellas espera al broker. Si un
*evento posterior* mueve el sentido del tiempo del evento del motor más allá del plazo de la regla,
esta se dispara de inmediato, incluso mientras el motor va absorbiendo trabajo pendiente, que es el
comportamiento correcto ante reprocesamiento. La otra vía es el silencio genuino, en el que nunca
llegará ningún evento que la active; **esa** se dispara según el reloj de pared y solo una vez que
el broker confirma que no queda nada por procesar, de modo que el trabajo pendiente no puede hacer
que el motor declare silencioso a un dispositivo cuando lo que ocurre es que aún no ha leído sus
eventos.

## Una alarma levantada que no se limpia

Una alarma se limpia cuando se resuelve la **última** regla que contribuye a ella, no cualquiera de
ellas. Si varias reglas levantan sobre la misma alarma, todas deben resolverse.

Más allá de eso, la causa más común es un tipo de regla que **solo se reevalúa cuando llega un
evento**. Si un dispositivo levanta una alarma y luego se queda completamente en silencio, no hay
nada que observe el fin de la condición, y la alarma sigue activa. Una regla de **ocurrencia
repetida** tiene una versión más severa del mismo problema: no puede observar en absoluto el fin de
su condición a partir de tráfico que no coincide, así que solo la limpiará un nuevo evento que sí
coincida, o un cambio de alcance.

**El patrón previsto es emparejar una regla así con una regla de ausencia**, para que un dispositivo
que deja de reportar levante una señal distinta y accionable en lugar de dejar una obsoleta en pie.

Dos causas más que conviene revisar:

- **Un «limpiar» de un operador no elimina la condición subyacente.** Si la condición sigue siendo
  cierta, el siguiente evento reactiva la misma alarma. Limpiar es un acuse de que la ha visto, no
  una supresión.
- **Un dispositivo que sale del alcance de una regla y luego se queda en silencio** conserva su
  alarma levantada. Los cambios de alcance surten efecto en el siguiente evento del dispositivo, y
  un dispositivo en silencio no tiene siguiente evento.

## Una regla que no se dispara

Por orden de frecuencia con la que resulta ser la respuesta:

1. **El perfil nunca se publicó.** Las reglas se ejecutan sobre telemetría en vivo solo después de
   publicar la versión del perfil que las contiene. Una regla en borrador no dispara nada.
2. **El dispositivo no resuelve a ningún perfil.** Un dispositivo cuyo tipo no tiene perfil, o cuyo
   perfil nunca se ha publicado, no coincide con **ninguna regla**. No falla nada: los eventos
   simplemente no se evalúan contra nada.
3. **El nombre de la métrica no coincide con lo que envía el dispositivo.** Una condición que nombra
   una métrica que nunca aparece es válida y compila sin problemas; simplemente nunca llega a ser
   cierta. Revise los eventos recientes del dispositivo para ver la clave exacta.
4. **La regla está delimitada a un grupo en el que el dispositivo no está actualmente.** La
   membresía se registra en cada evento a medida que se resuelve, así que un dispositivo recién
   añadido se incorpora en su siguiente evento.
5. **Un umbral dinámico no tiene atributo definido en ese dispositivo.** La regla lee el atributo
   del propio dispositivo; un dispositivo que no lo tiene no dispara.
6. **La regla falla en el momento de la evaluación.** Este es el caso difícil; vea más abajo.
7. **La notificación de publicación se perdió.** Es raro, pero no deja rastro en ninguno de los
   sitios donde uno lo buscaría.

:::warning Una notificación de publicación perdida silencia un perfil sin dejar error en ninguna parte
Cuando se publica una versión de perfil, las reglas que contiene se entregan al motor de detección
como una notificación de un solo intento. Si el broker no está disponible justo en ese momento, **la
publicación en sí sigue teniendo éxito**: el perfil aparece como publicado, las reglas se ven en la
consola y nada queda marcado como fallido. El motor simplemente nunca las recibe, y nunca se
ejecutan.

Nada lo reintenta y no se dispara ninguna alerta. **La recuperación consiste en volver a publicar el
perfil**, lo que reenvía la notificación. Si las reglas de todo un perfil dejaron de dispararse a la
vez y el punto 1 de arriba no lo explica, compruebe si el broker sufrió una interrupción alrededor
del momento de la publicación y vuelva a publicar.
:::

:::caution Una regla que falla en cada evento se ve exactamente igual que una regla silenciosa
Cuando la expresión de una regla falla en el momento de la evaluación, el evento se omite. La salud
de las reglas sigue reportando la regla como **activa y con cero disparos**, lo cual es
indistinguible de una regla cuya condición simplemente nunca es cierta, y la métrica de plataforma
que cuenta esos errores no está desglosada por regla.

Si la alerta `DetectFanoutEvalErrors` está activa y no consigue saber qué regla es la responsable,
use la **previsualización del lienzo** en cada regla sospechosa: la previsualización es el único
sitio donde un error de evaluación se atribuye a la regla que lo causó.
:::

## Previsualizar antes de publicar

La previsualización reproduce historial real a través del mismo motor que ejecuta la plataforma, sin
publicar nada. Es la mejor herramienta disponible para comprobar una regla, y tiene límites que
explican casi todos los resultados sorprendentes:

- Arranca **en frío** al principio de la ventana. Un sostenimiento o una ventana que empezó antes es
  invisible, y una ventana de agregación que cruza el final nunca se cierra.
- **No resuelve ningún atributo de dispositivo**, así que una regla con umbral dinámico se
  previsualiza como que nunca se dispara.
- No aplica la **delimitación a un grupo**: una regla delimitada se previsualiza sobre todo el
  perfil.
- No puede armar la ausencia para un dispositivo que **nunca ha reportado**.

Cuando la previsualización se trunca —porque la ventana quedó fuera de la retención o porque se
alcanzó un límite de escaneo—, se lo indica en lugar de devolver en silencio un resultado corto. Lea
ese aviso antes de concluir que una regla no se dispara.

## Configuración

Todos estos ajustes son opcionales; los valores predeterminados de fábrica son adecuados para la
mayoría de los despliegues.

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `watermarkLatenessSeconds` | 5 | Cuánto esperar a los eventos fuera de orden antes de dar un instante por asentado. **Súbalo** si los eventos llegan por lotes o si un salto aguas arriba puede atascarse; es la principal defensa frente a una falsa alarma de ausencia. |
| `maxEventFutureSkewSeconds` | 300 | Cuánto puede adelantarse una marca de tiempo informada por el dispositivo respecto al propio reloj de la plataforma antes de que se recorte. |
| `idleAdvanceGuardSeconds` | 5 | Cuánto tiempo debe estar el motor sin actividad antes de disparar una regla según el reloj de pared. Un valor negativo desactiva esa vía: las reglas de ausencia solo se disparan entonces cuando un *evento posterior* mueve el tiempo del evento más allá de su plazo, de modo que un dispositivo que se queda en silencio y sigue en silencio nunca levanta ninguna. |
| `checkpointEvents` | 1000 | Máximo de eventos procesados entre puntos de control. |
| `checkpointIntervalSeconds` | 10 | Tiempo máximo entre puntos de control, para que un flujo tranquilo también confirme. |
| `maxRulesPerTenant` | 500 | Techo de reglas por inquilino. **Se mide y se reporta, no se aplica**; vea más abajo. |
| `maxLiveKeysPerTenant` | 1000000 | Techo por inquilino de ventanas y temporizadores vivos. También se mide, no se aplica. |
| `outboundMessagesPerSecond` | 100 | Tasa por inquilino a la que se despachan las acciones de conector de salida. |
| `outboundBurst` | 200 | Margen de ráfaga para lo anterior. |

:::note Los presupuestos de estado se miden, no se aplican
Los dos techos por inquilino levantan una métrica y una línea de registro cuando un inquilino los
supera. **Nada detiene al inquilino.** Un solo inquilino que autore reglas patológicas puede agotar
la memoria del motor compartido. Vigile `DetectTenantOverStateBudget` y actúe en consecuencia: la
alerta *es* la aplicación del límite.
:::

## Qué vigilar

| Señal | Significa |
|---|---|
| `DetectCheckpointsStalledWithBacklog` | **La alerta más importante de esta página.** Los puntos de control se han detenido mientras hay trabajo esperando. O el motor se ha parado tras perder una carrera de cerebro dividido (split-brain), o su base de datos no está disponible. No se está detectando nada. |
| `DetectConsumerBacklogHigh` | El motor va con retraso. Mientras lo esté, la detección de ausencias queda suprimida. |
| `DetectWatermarkLagHigh` | El sentido del tiempo del evento del motor se está quedando atrás respecto al tiempo real. |
| `DetectFanoutEvalErrors` | Una o más reglas publicadas están fallando al evaluarse. Vea la advertencia de arriba. |
| `ReactPoisonDropping` | Se están descartando acciones tras agotar sus reintentos: se están perdiendo alarmas o comandos. Trátelo como urgente. |
| `ReactConnectorEgressShedding` | El despacho de salida supera el límite de tasa del inquilino y se está descartando. |
| `DetectTenantOverStateBudget` | Un inquilino ha superado un techo que no se aplica. |

:::warning Un motor detenido sigue informando que está sano
Si el motor se detiene tras perder una carrera de cerebro dividido, sus endpoints de salud siguen
informando que está listo. El pod se ve bien y la detección se ha parado. Hoy
`DetectCheckpointsStalledWithBacklog` es la señal que detecta esto, y se dispara con retraso: no se
fíe solo de la salud del pod para saber si la detección está funcionando.
:::

El estado por regla, la hora del último disparo y el conteo de disparos están disponibles en la
consola, en la pestaña **Salud de las reglas** del perfil de dispositivo, junto a un feed en vivo de
las detecciones a medida que ocurren.

## Eliminar un inquilino

El motor de detección mantiene el estado del inquilino como un punto de control opaco que ninguna
consulta puede interpretar, así que una [eliminación de inquilino](./tenant-deletion.md) le pide
directamente al motor que lo expulse y espera a que el motor confirme que la expulsión ha quedado
**grabada en un punto de control**, no meramente aplicada en memoria. Por tanto, una instancia que
ejecuta detección debe tener el motor accesible para que una eliminación pueda completarse; si el
motor está detenido o inaccesible, la eliminación queda abierta en lugar de completarse sobre datos
que siguen ahí.
