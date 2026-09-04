---
sidebar_position: 7
title: Cómo operar el motor de detección
---

# Cómo operar el motor de detección

Las [reglas de detección](../concepts/event-processing.md) las evalúa un servicio que se comporta de
forma distinta al resto de la plataforma: mantiene estado vivo en memoria, detecta desde
una **única instancia activa** y evalúa sobre el **tiempo del evento** en lugar de sobre el reloj.
Esta página es el contrato del operador: qué se gana con eso, qué cuesta y cómo distinguir un
motor sano de uno atascado.

Si lo que busca es qué puede expresar una regla o cómo autorarla, empiece por
[Procesamiento de eventos y alarmas](../concepts/event-processing.md).

## Un solo motor activo, a propósito

Exactamente un motor detecta a la vez. El chart distribuye una sola réplica con una estrategia de
despliegue de tipo *recreate*, que es la forma más simple de conseguirlo.

Esto no es una limitación de escalado a la espera de que alguien la levante a la ligera. El motor
mantiene en memoria cada ventana abierta, cada temporizador en marcha y cada enclavamiento de flanco
ascendente, y los confirma como un único punto de control (checkpoint). Dos motores leyendo el mismo
flujo verían cada uno solo una parte de él, y cada uno confirmaría un estado construido a partir de
una vista parcial.

Tres cosas protegen ese invariante, y conviene saber que no son igual de fuertes:

1. **La estrategia de despliegue** impide que un despliegue solape la instancia antigua con la
   nueva. Solo cubre los despliegues.
2. **Un arrendamiento (lease) de partición** decide qué réplica puede actuar. Una réplica lee,
   confirma mensajes, escribe puntos de control y publica únicamente mientras mantiene el
   arrendamiento, y se detiene en el momento en que deja de tenerlo.
3. **El propio motor se niega a confirmar** un punto de control que quede por detrás de uno ya
   almacenado. Si dos motores llegan a ejecutarse brevemente —una expulsión, el drenaje de un nodo o
   un pod eliminado a mano programan un reemplazo de inmediato—, el que se quedó atrás se detiene en
   lugar de sobrescribir.

:::warning Un drenaje puede ejecutar dos pods durante unos segundos
Solo la vía del despliegue está cubierta por completo por la estrategia. En una expulsión o un
drenaje de nodo, el reemplazo se programa antes de que el original se haya detenido. El
arrendamiento y la valla del punto de control lo contienen —el pod que no tiene el arrendamiento
deja de consumir, y un punto de control atrasado se rechaza—, pero es la razón para preferir un
despliegue deliberado a drenar el nodo en el que casualmente está el motor.
:::

### Ejecutar un standby en caliente

Puede ejecutar más de una réplica, y las réplicas adicionales son **standbys, no escritores**.
Establezca ambos valores:

```yaml
functionalAreas:
  event-processing:
    replicas: 2
    strategy: RollingUpdate
```

Subir `replicas` sin abandonar además la estrategia *recreate* hace fallar el renderizado, porque
esa estrategia detiene todos los pods antes de arrancar ninguno: el standby estaría caído justo
cuando hace falta.

Un standby está listo y sirve la API, pero no tiene el arrendamiento: no consume nada, no confirma
nada y toma la partición cuando el líder la libera o su arrendamiento expira. Lo que ahorra es el
arranque del pod y, en una expulsión, la espera a que se programe un reemplazo. Lo que **no** evita
es el coste de reinicio descrito en la siguiente sección: un standby no mantiene estado del motor
precargado, porque cargarlo significaría leer un punto de control que el líder todavía está
escribiendo.

Con una sola réplica no hay presupuesto de interrupción de pods, y drenar su nodo detiene la
detección hasta que el pod se reprograma. Un standby es la forma de evitarlo.

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
  modo que se puede inspeccionar;
- una **detección** cuyas acciones no se pudieron despachar **también se envía a esa cola**, con un
  error ruidoso.

:::caution Enviado a la cola es quedar registrado, no reintentado
Nada vuelve a ejecutar un mensaje de esa cola. El registro existe para que un fallo sea visible y
diagnosticable en lugar de silencioso; las consecuencias del propio fallo se mantienen igualmente.
Un *levantamiento* que no se despachó no volverá a aparecer hasta que la condición se despeje y se
vuelva a incumplir, y una *resolución* que no se despachó deja activa una alarma que debería
haberse limpiado. Trate un mensaje de esta cola como algo que investigar, no como algo que se
vaciará por sí solo.
:::

Consúltelos con `dcctl dead-letters list`, que se autentica como una identidad de operador:

```bash
dcctl dead-letters list --server <host> --email <usted> --password <secreto> \
  --tenant acme --since 2026-09-04T00:00:00Z
```

También están en el endpoint GraphQL de administración de la instancia como `deadLetters`,
protegido por la misma autoridad que el diario de auditoría. Los registros se conservan 30 días
de forma predeterminada —más que el flujo de mensajes subyacente, que es la razón de
almacenarlos— y la retención es configurable por despliegue.

La alerta `ReactPoisonDropping` existe exactamente para ese caso y debe tratarse como urgente.

:::caution Una acción que falla se lleva por delante a las que van después
Las acciones de una regla se ejecutan en el orden en que están listadas, y una acción que falla
detiene al resto. En cada reintento, las acciones *anteriores* a ella se vuelven a ejecutar, y las
*posteriores* siguen sin haberse ejecutado nunca; así que, si el evento acaba abandonándose, esas
acciones posteriores no llegaron a ocurrir en absoluto. El mensaje de la cola registra que la
detección se disparó y que sus acciones no; no las lleva a cabo.

**Ordene las acciones de una regla de modo que la importante vaya primero.** Si una regla levanta
una alarma y además llama a un webhook, poner la alarma primero significa que un endpoint inestable
no puede costarle la alarma.
:::

## Tiempos: qué significa «cuándo» {#timing-what-when-means}

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

**Qué ocurre pasada la tolerancia** depende del tipo de regla, y conviene saberlo antes de
dimensionar el ajuste:

- **Las reglas con ventana descartan la lectura**: los agregados de ventana fija, las reglas de
  sesión/hueco y los tipos deslizantes — repetición, agregados deslizantes y correlación. Una
  ventana es una afirmación sobre un intervalo de tiempo, y una lectura de fuera del intervalo que
  la regla cubre en ese momento no es evidencia sobre ese intervalo. Contarla permitiría que «tres
  lecturas por encima de 80 en diez segundos» se disparara con lecturas separadas por una hora.
- **Las reglas sin ventana la siguen evaluando** — umbral, duración, ventana de conteo y tasa. Cada
  una compara una lectura con la anterior o con un límite fijo, así que no hay intervalo del que una
  lectura tardía pueda quedar fuera.

En ambos casos la lectura se **almacena y grafica con normalidad**; esto afecta solo a la detección.

Dentro de la tolerancia no cambia nada: una lectura fuera de orden que aún cae dentro de la ventana
se incorpora con normalidad, que es para lo que existe la tolerancia. Como consecuencia la ventana
puede estirarse hasta la tolerancia — eso es lo que significa tolerar la llegada fuera de orden —
pero no más.

Los tipos deslizantes **cuentan lo que descartan**. `detect_late_samples_total` sube cada vez que
una lectura llega después de que haya pasado la ventana a la que pertenecía, de modo que una flota
cuyas reglas con ventana se han quedado calladas tiene algo que mirar en lugar de silencio; una
subida acumulada es la causa habitual. Los agregados de ventana fija y las reglas de sesión
descartan en silencio y no aparecen ahí.

Una propiedad hace que la tolerancia sea menos palanca de lo que parece: **la frontera es compartida
por toda la instancia**, no se lleva por dispositivo. Así que los dispositivos activos de una flota
la arrastran hasta aproximadamente «ahora» por mucho tiempo que uno silencioso haya estado fuera, y
subir la tolerancia para cubrir una subida acumulada de media hora significaría retrasar media hora
toda decisión basada en el tiempo, para todos los inquilinos. Donde ese intercambio no funcione, la
respuesta es acortar los lotes de subida o mantener las reglas con ventana fuera de esas métricas —
vea [conectar un dispositivo](../guides/connecting-a-device.md).

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

## Configuración {#configuration}

Todos estos ajustes son opcionales; los valores predeterminados de fábrica son adecuados para la
mayoría de los despliegues.

El motor de detección no tiene un ajuste propio de desfase de reloj. Cuánto puede adelantarse una
marca de tiempo informada por el dispositivo respecto al propio reloj de la plataforma se decide una
sola vez, al resolver el evento, y todos los consumidores —el historial almacenado, las proyecciones
en vivo, la detección y la reproducción— leen ese mismo valor ya acotado. Se configura en el área
device-management como `maxEventFutureSkewSeconds`, en segundos, **con 300 por defecto**. Una lectura
cuya hora informada adelanta al reloj del servidor más que eso se almacena en el techo, no se
rechaza.

Un **valor negativo se rechaza al arrancar**, y conviene saber qué habría significado: desactivar el
límite por completo. Un evento fechado años en el futuro fija entonces la hora de última actividad
del dispositivo — todas las proyecciones de aquí conservan solo el valor estrictamente más nuevo —,
así que su barrido de inactividad no vuelve a dispararse y el dispositivo nunca puede darse por
fuera de línea. No hay forma admitida de desactivar el límite; suba el número si los relojes de una
flota se desvían de verdad.

`watermarkLatenessSeconds`, más abajo, es otro ajuste para otra dirección: el desfase acota cuánto
puede adelantarse una marca de tiempo, y el retraso acota cuánto espera el motor a una que llega
*por detrás*.

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `watermarkLatenessSeconds` | 5 | Cuánto esperar a los eventos fuera de orden antes de dar un instante por asentado. **Súbalo** si los eventos llegan por lotes o si un salto aguas arriba puede atascarse; es la principal defensa frente a una falsa alarma de ausencia. |
| `idleAdvanceGuardSeconds` | 5 | Cuánto tiempo debe estar el motor sin actividad antes de disparar una regla según el reloj de pared. Un valor negativo desactiva esa vía: las reglas de ausencia solo se disparan entonces cuando un *evento posterior* mueve el tiempo del evento más allá de su plazo, de modo que un dispositivo que se queda en silencio y sigue en silencio nunca levanta ninguna. |
| `checkpointEvents` | 1000 | Máximo de eventos procesados entre puntos de control. |
| `checkpointIntervalSeconds` | 10 | Tiempo máximo entre puntos de control, para que un flujo tranquilo también confirme. |
| `maxRuleDurationSeconds` | 86400 | Lapso de tiempo máximo que puede declarar una regla: ventana, retención, tiempo de espera de silencio o hueco de sesión. **Se aplica**: una regla más larga se rechaza al publicar. Vea más abajo. |
| `maxRulesPerTenant` | 500 | Techo de reglas por inquilino. **Se mide y se reporta, no se aplica**; vea más abajo. |
| `maxLiveKeysPerTenant` | 1000000 | Techo por inquilino de ventanas y temporizadores vivos. También se mide, no se aplica. |
| `maxRetainedSamplesPerTenant` | 5000000 | Techo por inquilino de lecturas retenidas dentro de las ventanas abiertas. También se mide, no se aplica. |
| `outboundMessagesPerSecond` | 100 | Tasa por inquilino a la que se despachan las acciones de conector de salida. |
| `outboundBurst` | 200 | Margen de ráfaga para lo anterior. |

### El techo de duración de regla sí se aplica

`maxRuleDurationSeconds` es el único límite de esta página que **rechaza trabajo** en lugar de
limitarse a informar sobre él. Una regla que declare una ventana, retención, tiempo de espera o
hueco más largos se rechaza al publicar el perfil, con un error que nombra el campo y el límite, y
ese mismo techo se vuelve a aplicar cuando el motor carga una regla publicada, de modo que ambos
puntos nunca pueden discrepar sobre qué es ejecutable.

Existe porque una regla con ventana retiene **un registro por lectura** durante toda la ventana, y
por dispositivo. Esa memoria queda comprometida mientras viva la regla, en un proceso compartido por
todos los inquilinos, y es el único coste que no se puede observar a posteriori para luego
contenerlo: cuando la métrica se mueve, la memoria ya está reservada. Subir este límite aumenta esa
exposición para toda la instancia, así que dimensiónelo antes de cambiarlo: aproximadamente
*frecuencia de reporte × ventana × dispositivos × 32 bytes*, sumado sobre las reglas que usan
ventanas largas.

Los tiempos de espera de silencio y los huecos de sesión están acotados por la misma razón, aunque
no retengan lecturas. Esas reglas insertan una entrada nueva en el montículo de temporizadores del
motor cada vez que un dispositivo reporta, y la entrada sustituida no se descarta hasta que vence su
plazo: un tiempo de espera de tres días sobre un dispositivo que reporta cada diez segundos acumula
unas 26.000 entradas pendientes *para ese único dispositivo*. Un tiempo de espera largo cuesta
memoria en proporción directa a su longitud, igual que una ventana larga.

:::danger Una regla por encima del techo NO se ejecuta: se rechaza al arrancar, no se preserva
El techo se aplica cuando el motor **carga** una regla, no solo cuando se publica una. Una regla
cuya ventana supere el techo vigente falla al compilar durante la carga y **se omite**: no se
ejecuta, y la única evidencia es una línea de error en el registro del motor. No se dispara ninguna
alerta ni se mueve ninguna métrica —una regla omitida no retiene estado que medir.

Hay dos situaciones que producen esto, y ambas son silenciosas:

- **Bajar `maxRuleDurationSeconds`.** Las reglas publicadas bajo el límite anterior, más alto, dejan
  de funcionar en el siguiente reinicio. No se preservan.
- **Actualizar a una versión que introduce el techo.** Cualquier regla publicada cuando el límite no
  existía —por ejemplo, un agregado de siete días— se rechaza la primera vez que arranca el motor
  actualizado.

Antes de bajar el límite o de actualizar, inventaríe las reglas publicadas con lapsos superiores al
nuevo valor y acórtelas o retírelas de forma deliberada. Después, revise el registro del motor en
busca de `failed to compile; skipping` y confirme en la pestaña **Rule Health** del perfil que se
están ejecutando las reglas que espera.
:::

:::note Los presupuestos de estado por inquilino se miden, no se aplican
Los tres techos por inquilino —reglas, claves vivas y lecturas retenidas— levantan una métrica y una
línea de registro cuando un inquilino los supera. **Nada detiene al inquilino.** Un solo inquilino
que autore reglas patológicas puede agotar la memoria del motor compartido. Vigile
`DetectTenantOverStateBudget` y actúe en consecuencia: la alerta *es* la aplicación del límite.

Vigile **las tres** dimensiones de memoria. Fallan en direcciones distintas y ninguna implica a las
otras, así que cualquiera de ellas leída por separado puede parecer sana mientras el motor se llena:

| Métrica | Cuenta | Se mueve cuando |
|---|---|---|
| `devicechain_eventprocessing_detect_live_keys` | ventanas y temporizadores abiertos | muchos dispositivos repartidos sobre muchas reglas |
| `devicechain_eventprocessing_detect_retained_samples` | lecturas retenidas dentro de las ventanas abiertas | una regla de ventana larga sobre un dispositivo con mucho tráfico: **una** clave viva y cientos de miles de lecturas |
| `devicechain_eventprocessing_detect_pending_timers` | entradas en el montículo de temporizadores | un tiempo de espera de silencio o un hueco de sesión largos con reportes frecuentes: de nuevo una sola clave viva, y ninguna lectura retenida |

La segunda y la tercera existen porque la primera queda plana precisamente en los casos que agotan
la memoria más rápido. Solo las dos primeras tienen techos por inquilino; el montículo de
temporizadores se reporta como un total de toda la instancia, porque atribuirlo a un inquilino
exigiría recorrerlo entero en cada punto de control.
:::

## Qué vigilar

| Señal | Significa |
|---|---|
| `DetectCheckpointsStalledWithBacklog` | **La alerta más importante de esta página.** Los puntos de control se han detenido mientras hay trabajo esperando. O el motor se ha parado tras perder una carrera de cerebro dividido (split-brain), o su base de datos no está disponible. No se está detectando nada. |
| `DetectConsumerBacklogHigh` | El motor va con retraso. Mientras lo esté, queda suprimida la detección de ausencias **por silencio**; un evento posterior sigue disparando una ausencia vencida, como se explica arriba. |
| `DetectWatermarkLagHigh` | El sentido del tiempo del evento del motor se está quedando atrás respecto al tiempo real. |
| `DetectFanoutEvalErrors` | Una o más reglas publicadas están fallando al evaluarse. Vea la advertencia de arriba. |
| `ReactPoisonDropping` | No se están despachando acciones tras agotar sus reintentos: las alarmas y los comandos no están ocurriendo. Las detecciones se envían a la cola de mensajes no entregados para que pueda ver cuáles, pero nada las reprocesa. Trátelo como urgente. |
| `DeadLetterWriteLost` | Algo se abandonó **y** no se pudo escribir en el flujo de mensajes no entregados. Revise el bróker. |
| `DeadLetterStoreLosing` | Los mensajes llegaron al flujo pero no se pudieron escribir en el almacén, así que caducarán sin quedar registrados. Revise la base de datos del operador. |
| `ReactConnectorEgressShedding` | El despacho de salida supera el límite de tasa del inquilino y se está descartando. |
| `DetectTenantOverStateBudget` | Un inquilino ha superado un techo que no se aplica: su número de reglas, sus ventanas y temporizadores vivos, o las lecturas que retienen sus ventanas abiertas. |

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
