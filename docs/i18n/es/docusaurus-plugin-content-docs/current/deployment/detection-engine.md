---
sidebar_position: 8
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

Eso incluye las acciones que disparan las detecciones: un standby no despacha nada, de modo que el
límite de salida de un inquilino en el motor se consume una sola vez, en la réplica que detecta.
Cuando la partición cambia de réplica, la antigua y la nueva pueden despachar a la vez durante un
breve intervalo. Una actualización de alarma o una solicitud a un conector enviada por ambas se
almacena una sola vez en el bus de mensajes, y un comando lleva una clave que impide un segundo
encolado; eso sí, cada réplica consume su propio límite de salida en ese intervalo.

Con una sola réplica no hay presupuesto de interrupción de pods, y drenar su nodo detiene la
detección hasta que el pod se reprograma. Un standby es la forma de evitarlo.

## Qué cuesta un reinicio

Un reinicio es rutina, no un incidente. Al arrancar, el motor recarga su último punto de control y
reproduce el flujo desde esa posición, de modo que vuelve a derivar el estado que tenía.

Si el único pod del motor se detiene sin liberar su partición (una caída o una terminación por falta
de memoria), las acciones pendientes de despachar se reanudan cuando el reemplazo toma la partición,
hasta unos 35 segundos después. La detección se reanuda más tarde todavía: el reemplazo espera
primero un periodo de traspaso adicional, porque no puede distinguir un pod detenido de uno aislado
que sigue en marcha, y después reproduce el flujo como se describe más abajo. Un reinicio ordenado
libera la partición y se salta ambas esperas. Si el bróker está inaccesible un momento mientras el
pod se detiene, el pod sigue intentando liberar la partición hasta poco antes de que termine su
periodo de gracia. Una interrupción del bróker de más de 30 segundos termina la tenencia incluso de
un pod en marcha. Cuando el bróker vuelve, el motor toma de nuevo la partición, espera el periodo de
traspaso y reproduce el flujo, porque nada registra que se detuvo limpiamente.

| | Qué ocurre |
|---|---|
| **Eventos ya procesados y confirmados** | No se pierde nada. Solo se acusa recibo de los mensajes *después* de que se confirme el punto de control que los incluye, así que todo lo que no llegó a confirmarse se vuelve a entregar. |
| **Alarmas y comandos ya enviados** | Se vuelven a derivar y se reenvían, y luego se colapsan: una alarma es una actualización idempotente, y un comando lleva una clave que impide un segundo encolado. |
| **Webhooks salientes y publicaciones a conectores ya enviados** | También se colapsan. Una detección que se vuelve a derivar dentro de 30 minutos la reconoce el bus de mensajes y no se despacha otra vez, y una solicitud que el motor vuelve a enviar en unos diez minutos (una detección que estaba en curso cuando el pod se detuvo) se almacena una sola vez. Tras una interrupción más larga, una solicitud puede **enviarse de nuevo**; vea la sección de entrega más abajo. |
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
- **Un webhook saliente o una publicación a un conector** que el motor vuelve a enviar en unos diez
  minutos (cada reintento de una misma detección) lo reconoce el bus de mensajes y se almacena una
  sola vez. Pasado ese plazo, una solicitud todavía puede llegar dos veces a su destino: un
  reprocesamiento tras una interrupción larga, o el servicio de conectores reintentando una llamada
  cuya respuesta perdió. Cada solicitud lleva un encabezado `X-DC-Idempotency-Key` derivado del
  disparo, así que colapsar ese duplicado restante es tarea del **endpoint receptor**. Para destinos
  de cola y de broker la clave viaja como metadatos, sobre los que la mayoría de los brokers no
  puede actuar. **Diseñe los receptores de salida para que sean idempotentes.**

Cuando un sistema aguas abajo no está disponible, el mensaje se deja sin acusar recibo y se
reintenta con un temporizador, en lugar de martillear el destino. Tras cinco intentos de entrega,
repartidos en unos cuatro minutos en total:

- la solicitud de un **conector de salida** se envía a la **cola de mensajes no entregados**, de
  modo que se puede inspeccionar;
- una **detección** con acciones que aun así no se pudieron despachar **también se envía a esa
  cola**, con un error ruidoso. El detalle del mensaje nombra cada acción que falló, o que la
  tasa de salida rechazó, en el último intento, por tipo y clave de idempotencia. Las demás acciones de la detección se ejecutaron, o se
  omitieron deliberadamente (excluidas por su condición, no habilitadas o rechazadas por inválidas).

:::caution Enviado a la cola es quedar registrado, no reintentado
Nada vuelve a ejecutar un mensaje de esa cola. El registro existe para que un fallo sea visible y
diagnosticable en lugar de silencioso; las consecuencias del propio fallo se mantienen igualmente.
Un *levantamiento* que no se despachó no volverá a aparecer hasta que la condición se despeje y se
vuelva a incumplir, y una *resolución* que no se despachó deja activa una alarma que debería
haberse limpiado. Trate un mensaje de esta cola como algo que investigar, no como algo que se
vaciará por sí solo.
:::

Una acción de salida que la tasa de salida del inquilino rechaza se envía a la cola de mensajes no
entregados con motivo `shed`, dentro de un presupuesto. Ocurre de inmediato y no tras reintentos,
porque esperar no devuelve a un inquilino por debajo de su techo. La tasa se mide según el momento en
que la telemetría que desencadenó la acción llegó a la plataforma, de modo que un atraso que el motor
procesa tras un reinicio se cobra como ocurrió y no todo de golpe. Por encima de un presupuesto por
inquilino de aproximadamente un mensaje por segundo (60 de golpe) y diez por segundo en total, las
acciones descartadas se cuentan y se resumen en un mensaje por inquilino y minuto. Las detecciones
que el motor vuelve a publicar tras un reinicio las reconoce el bus de mensajes y se almacenan una
sola vez dentro de una ventana de 30 minutos, así que no se despachan ni se cobran dos veces.
Mientras una de las acciones de una detección sigue fallando, cada reintento vuelve a cobrar sus
acciones de webhook y de conector contra la tasa de salida del inquilino, aunque el bus de mensajes
almacene una sola vez la solicitud reenviada; así que un fallo sostenido, como un inquilino en su
límite de comandos retenidos, puede provocar descartes en otras reglas del mismo inquilino.

Un mensaje que un servicio abandona en su último intento (un pod detenido a mitad del
procesamiento, o un manejador que se pasó de su ventana) también queda registrado, con el motivo
`no-outcome`. Ese registro nunca liquida un comando: el último intento pudo haber hecho su trabajo
y perder solo su acuse de recibo. En los flujos de mucho volumen (eventos de
dispositivos, comandos y acciones de detección), el registro apunta al original en lugar de
copiarlo, e indica dónde encontrarlo hasta que el flujo lo descarte por antigüedad; lo mismo ocurre
con un registro cuyo mensaje es demasiado grande para copiarse. El registro de una petición de
conector apunta al flujo propio de mensajes no entregados del servicio de conectores, que guarda la
petición completa. Lo escribe el servicio dueño del mensaje la próxima vez que lee del flujo, así que,
mientras un servicio está caído, sus registros llegan tarde, pero llegan.

La lectura de los eventos resueltos que hace el propio motor es la excepción. Confirma un evento
solo después de que un punto de control lo incluya, y en cada arranque vuelve a leer el flujo desde
su último punto de control, así que un evento que agotó sus intentos mientras el punto de control no
se podía guardar no se pierde ni se envía a la cola de mensajes no entregados. En su lugar se
cuenta, y la alerta `ReplayCoveredDeliveriesExhausted` informa de ello ([Mensajes que agotaron sus
intentos de entrega](./observability.md#max-delivery-records)).

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

:::note Cada acción se despacha por separado
Las acciones de una regla no dependen unas de otras. Cada acción se intenta en cada entrega, así que
una que sigue fallando (un comando para un dispositivo cuyo inquilino está en su límite de comandos
retenidos, un webhook cuyo endpoint está caído) no retiene la alarma ni las demás acciones de la
regla. Lo mismo vale para un servicio que no responde en absoluto: cada intento se limita al tiempo
que queda antes de que el mensaje se vuelva a entregar, y cada acción recibe su parte de lo que
queda, así que las acciones listadas primero no pueden agotar el tiempo de las que van detrás. Las
acciones que ya tuvieron éxito se vuelven a enviar en cada reintento y se colapsan como se describe
arriba. No hay forma de condicionar una acción al éxito de otra.
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
- **Las reglas de duración descartan una lectura que cumple la condición y llega más atrás de la
  frontera que su tiempo de sostenimiento.** Una lectura así solo podría cambiar un sostenimiento
  que el motor ya decidió. Una lectura dentro de ese intervalo se coloca según su propia hora, no
  según cuándo llegó. Una lectura tardía que muestra que la condición se interrumpió a mitad de una
  racha reinicia la racha desde la lectura más reciente que la cumplía, y una lectura tardía que
  cumple la condición nunca reabre una racha por encima de una interrupción que el motor ya vio, así
  que una lectura tardía no puede elevar una alarma de duración que las lecturas no respaldan. Una
  lectura que no cumple la condición termina una alarma de duración elevada por tarde que llegue,
  salvo que sea anterior a la alarma: una lectura tardía de antes de que se elevara la alarma, que
  llega después, no la retira.
- **Las reglas de umbral descartan la lectura que las haría retroceder.** Mientras una alarma de
  umbral está elevada, una lectura más antigua que la más reciente que ha enviado el dispositivo se
  descarta y se cuenta, tanto si está dentro de la tolerancia como si queda muy por detrás, como una
  publicación reintentada un minuto tarde, así que una lectura más antigua no puede retirar una
  alarma que una lectura más reciente mantiene elevada. Mientras no hay alarma, una lectura que
  cumple la condición solo se descarta y se cuenta si queda más atrás que la tolerancia y además
  quedó en cola para la detección más de 30 segundos después de que la plataforma la recibiera,
  como ocurre con una publicación reintentada (no cuenta el tiempo que el propio motor de detección
  tarda en leer la cola, por ejemplo durante un reinicio), así que una publicación reintentada no puede elevar una
  alarma que lecturas más recientes habían retirado. Cualquier otra lectura que cumple la condición
  se evalúa, así que un dispositivo cuyas lecturas tienen una fecha más atrasada que la tolerancia
  respecto al resto, como uno cuyo reloj va retrasado o uno que sube lecturas guardadas durante un
  corte, eleva una alarma de umbral con ellas según llegan. Las lecturas que llegan en orden de marca
  de tiempo se evalúan en cuanto llegan, así que esto no añade retraso. De ello se siguen dos
  contrapartidas. Una lectura que cumple la condición y llega fuera de orden sigue elevando la
  alarma, aunque una lectura más reciente ya la hubiera retirado, cuando está dentro de la tolerancia
  o llega a la plataforma de nuevo, como una lectura que el dispositivo envía por segunda vez; la
  siguiente lectura del dispositivo la vuelve a retirar. Y una lectura que la plataforma retuvo más
  de 30 segundos antes de ponerla en cola para la detección, durante una acumulación en la ingesta o
  en la resolución por ejemplo, se trata como una
  publicación reintentada: si cumple la condición y queda más atrás que la tolerancia, se descarta y
  se cuenta. La lectura más reciente del dispositivo solo se
  recuerda mientras su alarma está elevada, y solo se descarta una lectura hasta diez minutos más
  antigua que ella: una más antigua se evalúa según llega, así que un dispositivo cuyo reloj se
  atrasa más de diez minutos, por ejemplo al corregir su zona horaria, no mantiene su alarma elevada
  hasta que su reloj se pone al día.
- **Las reglas de ventana de conteo y de tasa colocan la lectura según su propia hora**, como hacen
  las reglas de duración. Una tasa es el cambio entre lecturas consecutivas y una ventana de conteo
  es cada N lecturas consecutivas, así que una lectura que llega fuera de orden se coloca donde dice
  su marca de tiempo, y la regla evalúa una lectura solo cuando la frontera la ha sobrepasado — por
  eso estos dos tipos elevan y retiran sus alarmas una tolerancia de retraso por detrás de la
  lectura más reciente. Cuando no llega ninguna lectura más reciente — la última antes de que la
  flota se quede en silencio —, se decide según el reloj de pared una vez que el motor ha estado
  inactivo, igual que se dispara una regla de ausencia, de modo que con los valores
  predeterminados tarda unos quince segundos desde su llegada; con `idleAdvanceGuardSeconds`
  negativo espera al siguiente evento. Una lectura más antigua que la más reciente que la regla ya ha asentado se
  descarta: el par o la ventana a la que pertenecía ya se decidió. Una lectura que no cumple la
  condición sigue terminando una alarma de tasa elevada por tarde que llegue, salvo que sea
  anterior a la alarma.

En todos los casos la lectura se **almacena y grafica con normalidad**; esto afecta solo a la detección.

Dentro de la tolerancia, una lectura fuera de orden se incorpora con normalidad, que es para lo que
existe la tolerancia. Los tipos deslizantes leen su ventana tal como está en la **lectura más
reciente** que ha enviado el dispositivo (en la correlación, el avistamiento más reciente del área),
sea cual sea el orden en que llegaron las lecturas. Por eso una lectura tardía solo cuenta junto a
lecturas que estaban dentro de una misma ventana con ella en ese momento, y una alarma que eleva o
retira se registra en esa lectura más reciente, nunca antes de un cambio de alarma que la regla ya
haya notificado. Tras una ráfaga de lecturas fuera de orden, la alarma queda elevada o retirada
según lo que indican las lecturas en orden de marca de tiempo. El momento exacto del cambio dentro
de la ráfaga es aproximado. En una agregación deslizante hay un caso que espera: cuando una lectura
tardía retiraría una alarma que elevó una lectura más reciente, la retirada se retiene hasta que
hayan podido llegar las lecturas de hasta una tolerancia más recientes. Si alguna vuelve a superar
el umbral, la alarma sigue elevada en lugar de retirarse y volver a elevarse un instante después; si
no, se retira, registrada en la lectura más reciente. Una lectura tardía demasiado antigua para esa ventana ya no influye en
la alarma, y no se cuenta como descartada.

Los tipos deslizantes, las reglas de duración, las reglas de umbral y las reglas de ventana de
conteo y de tasa **cuentan lo que descartan**, una vez por cada regla que descarta una lectura.
`detect_late_samples_total` sube cada vez que una lectura llega después de que haya pasado la
ventana a la que pertenecía, cuando una regla de duración descarta una lectura que cumple la
condición y queda más atrás de la frontera que su tiempo de sostenimiento, cuando una regla de
ventana de conteo o de tasa descarta una lectura más antigua que otra que ya ha asentado, cuando
una regla de umbral descarta una lectura más antigua que la más reciente de un dispositivo en alarma,
o una lectura reintentada que cumple la condición y queda demasiado atrás de un dispositivo sin
alarma, y cuando
una lectura que no cumple la condición de una regla de duración llega después de que se elevara su
alarma pero se tomó dentro de la racha que la elevó, antes de la elevación. Una lectura
tardía anterior a toda la racha se ignora y no se cuenta: la racha empezó después de ella. Así, una
flota cuyas reglas se han quedado calladas tiene algo que mirar en lugar de silencio; una subida acumulada
es la causa habitual. Los agregados de ventana fija y las reglas de sesión descartan en silencio y
no aparecen ahí.

Una regla de duración eleva su alarma cuando la frontera pasa el final del sostenimiento, y la
frontera va por detrás de la lectura más reciente en la tolerancia de retraso. Una lectura que
muestra que la condición cesó y llega antes de ese momento termina la racha sin elevar la alarma,
aunque todas las lecturas lleguen en orden y la condición se haya cumplido durante todo el tiempo de
sostenimiento. Por eso un episodio solo es seguro que eleve la alarma si dura su tiempo de
sostenimiento más la tolerancia de retraso.

Una propiedad hace que la tolerancia sea menos palanca de lo que parece: **la frontera es compartida
por toda la instancia**, no se lleva por dispositivo. Así que los dispositivos activos de una flota
la arrastran hasta aproximadamente «ahora» por mucho tiempo que uno silencioso haya estado fuera, y
subir la tolerancia para cubrir una subida acumulada de media hora significaría retrasar media hora
toda decisión basada en el tiempo, para todos los inquilinos. Donde ese intercambio no funcione, la
respuesta es acortar los lotes de subida o mantener las reglas con ventana fuera de esas métricas —
vea [conectar un dispositivo](../guides/connecting-a-device.md).

La frontera compartida también afecta a los **relojes de los dispositivos**. Un dispositivo cuyas
marcas de tiempo van sistemáticamente por detrás del resto de la flota más que el tiempo de
sostenimiento de una regla de duración más la tolerancia de retraso, ya sea por un reloj lento o por
un camino lento hasta la plataforma, nunca eleva esa regla: cada lectura suya que cumple la condición se descarta por tardía
y se cuenta en `detect_late_samples_total`. Corrija el reloj del dispositivo o dé a la regla un
tiempo de sostenimiento mayor que el retraso.

Lo mismo se aplica a las lecturas que esperaron **dentro de la plataforma**. Mientras
`event-sources` está caído, el broker de la plataforma sigue guardando lo que los dispositivos
publican por MQTT, y `event-sources` procesa ese atraso cuando vuelve. Cada lectura conserva su
propia hora: la que informó el dispositivo o, para una lectura enviada sin `occurredTime`, el
momento en que el broker la recibió. Mientras tanto solo siguen llegando los transportes que no
pasan por `event-sources`: LwM2M y Sparkplug lo hacen y mantienen la frontera en «ahora», mientras
que la ingesta HTTP la sirve el propio `event-sources` y cae con él. Tras una caída más larga
que la ventana de una regla, el atraso llega por tanto tarde a los tipos deslizantes, y a las reglas de
duración cuando es más antiguo que su tiempo de sostenimiento, igual que una subida acumulada: se
almacena y se grafica con normalidad, esas reglas no lo usan, y `detect_late_samples_total` sube.

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
repetida** no tiene este problema mientras el dispositivo sigue reportando: una lectura que no
coincide pero que trae su métrica sigue haciendo envejecer las coincidencias anteriores fuera de la
ventana deslizante, y la alarma se limpia cuando el conteo baja de N. Las reglas de **ventana de
conteo** y de **sesión** tienen una versión más severa del problema: una ventana de conteo se cuenta
en eventos que coinciden y una sesión solo la abre uno de ellos, así que ninguna puede observar en
absoluto el fin de su condición a partir de tráfico que no coincide —una alarma levantada sigue en
pie hasta que se complete la siguiente ventana o se cierre la siguiente sesión con la condición ya
no cumplida, lo que puede no ocurrir nunca.

**El patrón previsto es emparejar una regla así con una regla de ausencia**, para que un dispositivo
que deja de reportar levante una señal distinta y accionable en lugar de dejar una obsoleta en pie.

Cuatro causas más que conviene revisar:

- **Un «limpiar» de un operador no elimina la condición subyacente.** Si la condición sigue siendo
  cierta, el siguiente evento reactiva la misma alarma. Limpiar es un acuse de que la ha visto, no
  una supresión.
- **Un dispositivo que sale del alcance de una regla y luego se queda en silencio** conserva su
  alarma levantada. Los cambios de alcance surten efecto en el siguiente evento del dispositivo (en todas las réplicas de `device-management`; una réplica que no recibió el aviso del cambio se pone al día cuando caduca su copia en caché, en cinco minutos como máximo por defecto), y un dispositivo en
  silencio no tiene siguiente evento.
- **La regla que la levantó ya no se ejecuta.** Una regla que se omite al cargarse —la pestaña
  **Rule Health** del perfil la muestra como un error de compilación— no se evalúa, así que nada
  resuelve la alarma que levantó antes. Limpie la alarma a mano cuando haya corregido o retirado la
  regla.
- **Una alarma de silencio (ausencia) que sigue levantada después de que cambió el dispositivo o la
  regla.** A diferencia de los tipos de regla anteriores, una alarma de ausencia se cierra sola cuando
  su dispositivo sale del alcance de la regla: el dispositivo se deshabilita, se elimina o pasa a un
  tipo de dispositivo con un perfil distinto, o una republicación retira la regla. Una republicación que
  **conserva** la regla deja la alarma levantada, con su acuse de recibo, en lugar de cerrarla y
  volver a levantarla cuando termina la nueva ventana de silencio.

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
   añadido se incorpora en su siguiente evento (en todas las réplicas de `device-management`; una réplica que no recibió el aviso del cambio se pone al día cuando caduca su copia en caché, en cinco minutos como máximo por defecto).
5. **Un umbral dinámico no tiene atributo definido en ese dispositivo.** Un umbral creado en el
   formulario lee el atributo del propio dispositivo, y un dispositivo sin un valor numérico
   `SERVER` o `SHARED` para él no dispara. Un valor que no es un número, o uno establecido con
   alcance `CLIENT`, cuenta como no definido. Una expresión CEL con un
   [respaldo](../concepts/event-processing.md#dynamic-thresholds-in-cel) dispara según su respaldo.
6. **La regla falla en el momento de la evaluación.** Este es el caso difícil; vea más abajo.
7. **Un cambio todavía no había llegado al motor.** Una publicación, una reversión, o un cambio de
   dispositivo o de atributo del que no se avisó al motor se recoge en su siguiente comparación con
   device-management (vea más abajo).
8. **La regla dejó de compilar tras una actualización.** Una actualización puede rechazar una regla
   que una versión anterior aceptaba. Esa regla se omite cuando el motor la carga, y la pestaña
   **Rule Health** del perfil la muestra como un error de compilación con el motivo. Las notas de la
   versión enumeran cada uno de esos cambios.

:::note Una notificación de cambio perdida se repara, no se pierde
Publicar o revertir un perfil, crear un dispositivo o cambiar su tipo, y establecer un atributo de
umbral avisan al motor de detección una sola vez. Si ese aviso se pierde —el broker no estaba
disponible en ese momento, o device-management se reinició justo después del cambio—, el cambio en
sí se mantiene. Además, el motor compara su copia de las reglas publicadas, las versiones activas de
perfil, los dispositivos y los atributos de umbral de cada inquilino con device-management cuando
asume la detección y cada cinco minutos después, y corrige lo que difiera. Por tanto, un aviso
perdido retrasa un cambio unos minutos —no más de unos siete, y un dispositivo o atributo
eliminado no más de unos diez— en lugar de perderlo. No hace falta volver a publicar.

`DetectFactsRepaired` indica que se hizo una corrección. `DeviceFactPublishFailing` indica que los
avisos no se están enviando. `DetectFactReconcileFailing` indica que la propia comparación está
fallando; en ese caso, un cambio perdido sigue perdido hasta que la comparación funcione.
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
- **No resuelve ningún atributo de dispositivo**: cada dispositivo se previsualiza como si no
  tuviera ninguno. Por eso un umbral dinámico creado en el formulario se previsualiza como que nunca
  se dispara, y una expresión CEL con
  [respaldo](../concepts/event-processing.md#dynamic-thresholds-in-cel) previsualiza su respaldo en
  todos los dispositivos, incluidos los que sí tienen el atributo.
- No aplica la **delimitación a un grupo**: una regla delimitada se previsualiza sobre todo el
  perfil.
- No puede armar la ausencia para un dispositivo que **nunca ha reportado**.
- Se ejecuta **sin tolerancia de retraso**, así que no usa una lectura que llega más atrás del resto
  del historial reproducido que una ventana deslizante o el tiempo de sostenimiento de una regla de
  duración.

Cuando la previsualización se trunca —porque la ventana quedó fuera de la retención o porque se
alcanzó un límite de escaneo— o aparta lecturas por tardías, se lo indica en lugar de devolver en silencio un resultado corto. Lea
ese aviso antes de concluir que una regla no se dispara.

## Configuración {#configuration}

Todos estos ajustes son opcionales; los valores predeterminados de fábrica son adecuados para la
mayoría de los despliegues.

El motor de detección no tiene un ajuste propio de desfase de reloj. Cuánto puede adelantarse una
marca de tiempo informada por el dispositivo respecto al propio reloj de la plataforma se decide una
sola vez, al resolver el evento, y todos los consumidores —el historial almacenado, las proyecciones
en vivo, la detección y la reproducción— leen ese mismo valor ya acotado. Se configura en el área
device-management como `maxEventFutureSkewSeconds`, en segundos, **con 300 por defecto**. Una lectura
cuya hora informada adelanta al momento en que la plataforma la recibió más que eso se almacena en
el techo, no se rechaza.

La otra dirección es fija, no configurable: una lectura fechada más de **366 días** antes de que la
plataforma la recibiera se rechaza, y no se almacena ni se evalúa en ningún sitio. Las dos
direcciones se tratan de forma distinta a propósito. Un reloj adelantado congelaría el estado
compartido en vivo, así que su hora se limita; una hora muy en el pasado crearía almacenamiento para
un periodo que nadie registró, así que la lectura se rechaza en vez de moverse. Consulta
[hasta cuándo puede fecharse una lectura](../guides/connecting-a-device.md#payload-shapes).

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
| `watermarkLatenessSeconds` | 5 | Cuánto esperar a los eventos fuera de orden antes de dar un instante por asentado. **Súbalo** si los eventos llegan por lotes o si un salto aguas arriba puede atascarse; es la principal defensa frente a una falsa alarma de ausencia. También tolera el pequeño desorden entre los eventos de un mismo dispositivo que introduce la resolución, y que crece con el número de réplicas de `device-management`: una regla con ventana sigue contando un evento que llega dentro de este margen, las reglas de duración, de ventana de conteo y de tasa lo colocan según su propia hora, y una regla de umbral en alarma descarta una lectura más antigua que la más reciente del dispositivo, mientras que una sin alarma descarta una lectura que cumple la condición y llega más tarde que este margen solo si se reintentó o se retuvo por el camino. No cubre un evento cuya publicación falló y se reintentó, que llega al menos 60 segundos tarde. |
| `idleAdvanceGuardSeconds` | 5 | Cuánto tiempo debe estar el motor sin actividad antes de disparar una regla según el reloj de pared. Un valor negativo desactiva esa vía: las reglas de ausencia solo se disparan entonces cuando un *evento posterior* mueve el tiempo del evento más allá de su plazo, de modo que un dispositivo que se queda en silencio y sigue en silencio nunca levanta ninguna. Del mismo modo, las reglas de ventana de conteo y de tasa deciden la última lectura antes de un periodo de silencio solo cuando llega un evento posterior. |
| `detectShards` | 1 | En cuántas partes se divide el estado del motor, según la serie de cada regla (un dispositivo, o el ancla en una regla de correlación), de 1 a 64 (cualquier otro valor se rechaza al arrancar). Las detecciones, el reloj del tiempo del evento y el punto de control guardado son idénticos con cualquier valor, así que puede cambiarse en cualquier reinicio, al alza o a la baja, sin nada que migrar. En esta versión las partes se procesan una tras otra, por lo que un valor superior a 1 todavía no acelera la detección: déjelo en 1 salvo que esté midiendo. El valor en vigor es la métrica `detect_shards`. |
| `checkpointEvents` | 1000 | Máximo de eventos procesados entre puntos de control. |
| `checkpointIntervalSeconds` | 10 | Tiempo máximo entre puntos de control, para que un flujo tranquilo también confirme. **Como máximo 30**: un punto de control es lo que confirma el flujo, así que un intervalo cercano o superior a la ventana de confirmación de 60 segundos del broker hace que los mensajes de un flujo tranquilo se vuelvan a entregar. 30 deja margen para el propio punto de control, y el servicio rechaza al arrancar cualquier valor mayor. |
| `checkpointTimeoutSeconds` | 10 | Cuánto puede tardar una llamada de un punto de control (publicar detecciones, guardar la instantánea) antes de abandonarla y contar el punto de control como fallido, en lugar de bloquear la detección con una base de datos o un bróker que ha dejado de responder. Máximo 120. El límite del guardado de la instantánea crece solo, hasta cuatro veces el último guardado completado (con tope de dos minutos), de modo que un motor grande sobre una base de datos lenta no se queda fallando; súbalo solo si el primer guardado ya es más lento que este valor. |
| `maxRuleDurationSeconds` | 86400 | Lapso de tiempo máximo que puede declarar una regla: ventana, retención, tiempo de espera de silencio o hueco de sesión. **Se aplica**: una regla más larga se rechaza al publicar. Vea más abajo. |
| `maxRulesPerTenant` | 500 | Techo de reglas por inquilino. **Se mide y se reporta, no se aplica**; vea más abajo. |
| `maxLiveKeysPerTenant` | 1000000 | Techo por inquilino de ventanas y temporizadores vivos. También se mide, no se aplica. |
| `maxRetainedSamplesPerTenant` | 5000000 | Techo por inquilino de lecturas retenidas dentro de las ventanas abiertas. También se mide, no se aplica. |
| `outboundCallsPerSecond` | 100 | Tasa por inquilino a la que se despachan las acciones de conector de salida, medida según el momento en que la telemetría que las desencadenó llegó a la plataforma. |
| `outboundBurst` | 200 | Margen de ráfaga para lo anterior. |
| `shedLetterPerSecond` | 1 | Tasa por inquilino a la que las acciones de salida descartadas se registran como mensajes no entregados individuales. |
| `shedLetterBurst` | 60 | Margen de ráfaga para lo anterior. |
| `shedLetterGlobalPerSecond` | 10 | Lo mismo, para todos los inquilinos. Las acciones descartadas por encima de cualquiera de los dos presupuestos se cuentan y se resumen en un mensaje no entregado por inquilino y minuto. |
| `shedLetterGlobalBurst` | 100 | Margen de ráfaga para lo anterior. |
| `ruleHealth.flushIntervalMillis` | 1000 | Cada cuánto se escriben en la base de datos los contadores de disparos por regla, de 50 a 60000. Es aproximadamente cuánto pueden ir retrasados los recuentos de disparos y la hora del último disparo en la consola; la detección nunca lo espera. |
| `ruleHealth.maxPendingKeys` | 4096 | Máximo de reglas que pueden tener disparos pendientes de escribir a la vez, de 1 a 1048576. Un disparo de una regla más allá de este límite no se cuenta (véase más abajo). |
| `ruleHealth.maxPendingBytes` | 4194304 | Presupuesto de memoria para esos disparos pendientes, de 65536 a 268435456 (256 MiB). |
| `ruleHealth.maxBatchRows` | 256 | Máximo de reglas que lleva una escritura de un inquilino, de 1 a 4096; un inquilino con más pendientes se escribe en varias transacciones. |
| `ruleHealth.writeTimeoutMillis` | 2000 | Cuánto puede tardar la escritura de un inquilino, de 100 a 60000. Una escritura que falla o agota el tiempo se descarta, no se reintenta. |
| `derivedPublishing.maxInFlight` | 32 | Cuántas detecciones puede tener a la vez un punto de control esperando la confirmación del bus de mensajes, de 1 a 1024. Con 1, cada una se confirma antes de enviar la siguiente. Por encima de 1, las detecciones de dispositivos distintos se publican en paralelo, nunca más de una por dispositivo a la vez. Véase más abajo. |
| `derivedPublishing.maxBufferedBytes` | 4194304 | Bytes que pueden ocupar juntas esas detecciones en espera, de 65536 a 268435456 (256 MiB). Una sola detección más grande se publica sola. |

Cada ajuste de `ruleHealth` y de `derivedPublishing` es opcional; si se indica, debe estar dentro de
su rango (incluido el cero) o el servicio no arranca.

### Los recuentos de salud de reglas son aproximados

La vista de salud de reglas de la consola muestra cuántas veces ha disparado cada regla y cuándo lo
hizo por última vez. Esos contadores no se escriben al publicar cada detección: el motor suma cada
detección publicada a un total en memoria por regla y escribe los totales en segundo plano, una
transacción por inquilino, cada `flushIntervalMillis`. Una regla muy activa cuesta así una escritura
de fila por intervalo en lugar de una por detección, y una base de datos lenta o caída nunca frena la
detección. El único caso en que se espera una escritura es el borrado de un inquilino: la purga espera
a que se resuelva la escritura de ese inquilino que ya está en curso, como máximo
`writeTimeoutMillis` más un segundo.

Lo que eso significa para las cifras:

- **Van con retraso.** Con una base de datos sana, un disparo suele aparecer en el intervalo de
  escritura más una escritura. Con sobrecarga sostenida, o mientras la base de datos falla, no se
  promete ninguna frescura.
- **Pueden quedarse cortas.** Una escritura que falla o agota el tiempo se descarta en lugar de
  reintentarse, porque puede haberse confirmado y un reintento contaría sus disparos dos veces. También
  se descartan disparos cuando el presupuesto de pendientes (`maxPendingKeys`, `maxPendingBytes`) está
  lleno, cuando se elimina un inquilino y cuando el motor se detiene o pierde su liderazgo con disparos
  pendientes (una parada ordenada hace antes una última escritura acotada). Cada disparo descartado se
  cuenta, por motivo, en `devicechain_eventprocessing_detect_rule_stat_updates_discarded_total`.
- **Pueden pasarse.** Tras un reinicio el motor vuelve a publicar las detecciones desde su último punto
  de control, y esas cuentan otra vez.
- **Solo cuentan las detecciones publicadas.** Un disparo se cuenta solo después de que el broker haya
  confirmado su evento derivado; una detección descartada o que aún espera publicarse no cuenta.

El último flanco disparado es el más reciente que vieron los contadores, no el estado actual de la
alarma de la regla. Para eso, use la propia alarma o el flujo de detecciones.

Contabilidad de los dos presupuestos: una entrada pendiente es una regla con disparos aún sin escribir,
esté donde esté (acumulando disparos, apartada para la siguiente escritura o dentro de una escritura
en curso), y su tamaño se calcula como 160 bytes más la longitud de su id de regla, más 320 bytes y el
token del inquilino para la primera entrada de un inquilino. Eso sobrestima lo que realmente ocupa, de
modo que el presupuesto de bytes es un límite estricto.

### Publicar varias detecciones a la vez

Un punto de control publica en el bus de mensajes todas las detecciones que cubre, y guarda el
estado del motor y confirma los eventos que las originaron solo cuando todas han sido confirmadas.
Con `derivedPublishing.maxInFlight` en 1, eso es una detección cada vez, así
que un punto de control con muchas detecciones espera otras tantas confirmaciones una tras otra.
Donde una confirmación es lenta, por ejemplo en un stream replicado cuyos servidores están lejos
entre sí, esa espera es lo que limita la rapidez con que se entregan las detecciones.

El valor por defecto, 32, permite que un punto de control espere varias confirmaciones a la vez. Con
una carga intensa en disparos, de unos 8.000 eventos por segundo que ofrecen unas 2.500 detecciones
por segundo, una ventana de 32 se mantuvo al día (7.658 eventos y 2.441 detecciones por segundo, con
la marca de agua unos 5 segundos atrás), mientras que una ventana de 1 fue quedando cada vez más
atrás (1.656 eventos y 528 detecciones por segundo, con la marca de agua más de dos minutos atrás).
Pon `maxInFlight` en 1 para publicar estrictamente una detección cada vez. Lo que no cambia con
ningún valor:

- **Las detecciones de cada dispositivo llegan en orden.** La siguiente detección de un dispositivo,
  de cualquiera de sus reglas, se envía solo cuando la anterior ha sido confirmada, así que nunca hay
  más de una por dispositivo en espera. Las detecciones de una regla de correlación cuentan como de
  su ancla. Las de dispositivos distintos pueden llegar en otro orden que antes; nada posterior
  depende de ese orden, porque cada alarma pertenece a un solo dispositivo.
- **Nada se confirma antes que sus detecciones.** El punto de control se guarda solo cuando todas
  las detecciones que cubre han sido confirmadas, exactamente igual que con 1.
- **Un fallo se reintenta sin duplicados.** Cuando una publicación falla o agota el tiempo, no se
  envían más, el punto de control no se guarda, y el siguiente intento envía la misma detección con
  la misma identidad. El bus de mensajes reconoce una copia que ya guardó, así que una publicación que
  agotó el tiempo pero sí llegó se guarda una vez, siempre que el reintento llegue dentro de la ventana de duplicados del flujo de
  eventos derivados (30 minutos). Es una ventana de deduplicación acotada, no una garantía permanente
  de entrega exactamente una vez: un reintento posterior, tras una interrupción larga, puede
  guardarse por segunda vez y se colapsa aguas abajo por la identidad de la detección. Las
  detecciones ya confirmadas no se reenvían.
- **Los recuentos de salud de reglas se registran en el mismo orden**, sea cual sea el orden en que
  vuelvan las confirmaciones.

Solo ayuda cuando la fase `publish` es una parte grande del tiempo del bucle (véase
[en qué se va el tiempo del bucle](#loop-phases)) y las detecciones se reparten entre muchos
dispositivos. Un único dispositivo muy activo no gana nada, porque sus detecciones se siguen enviando
de una en una. `devicechain_eventprocessing_detect_derived_publish_window_full_seconds_total` es el
tiempo que los puntos de control pasaron esperando con otra detección lista para enviar; si se acerca
al tiempo en `publish`, una ventana más ancha puede ayudar. Deje el valor por defecto salvo que esté
midiendo.

### El techo de duración de regla sí se aplica

`maxRuleDurationSeconds` es el único ajuste de la tabla anterior que **rechaza una regla** en lugar de
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
ejecuta. La evidencia es una línea de error en el registro del motor y un estado **Error de
compilación**, con el diagnóstico del compilador, en la pestaña **Rule Health** del perfil, que
recompila cada regla publicada bajo el techo vigente. No se dispara ninguna alerta ni se mueve
ninguna métrica —una regla omitida no retiene estado que medir.

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

### El techo de coste de las expresiones es fijo

Cada expresión CEL de una regla de detección se somete a una comprobación de coste cuando se
publica el perfil: la condición, la guarda de una acción, una plantilla de contenido y una
plantilla de clave de alarma. Una regla cuya expresión tenga un coste estimado en el peor caso
superior a **100** se rechaza, con un error que indica la estimación y el techo. El motor vuelve a
aplicar el mismo techo cuando carga una regla publicada. Los selectores de grupos dinámicos se
comprueban contra el mismo valor al guardar un grupo.

El techo es el mismo para todos los inquilinos, y no hay ningún ajuste para subirlo, ni para un
inquilino ni para la instancia. Acota cuánto trabajo puede costar una sola lectura al motor único
que comparten todos los inquilinos. Una expresión que recorre las mediciones de una lectura
(`m.all(...)`, `m.exists(...)`) es la forma habitual de alcanzarlo; nombre en su lugar las
mediciones que necesita.

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

## Qué vigilar {#what-to-watch}

| Señal | Significa |
|---|---|
| `DetectCheckpointsStalledWithBacklog` | **La alerta más importante de esta página.** Los puntos de control se han detenido mientras hay trabajo esperando: el motor no puede alcanzar su base de datos o el bróker, o su bucle está atascado. No se está detectando nada. |
| `DetectLoopStalled` | El bucle de detección de la réplica que posee la partición lleva más de dos minutos sin avanzar, aunque la réplica sigue declarándose activa. El bucle está atascado dentro de una llamada, normalmente a una conexión de base de datos o de bróker que ha dejado de responder. Todas las demás métricas de esta página se leen en ese mismo bucle, así que permanecen congeladas en su último valor y parecen sanas. No se está detectando nada; reinicie el pod indicado en la alerta. Una réplica en espera nunca la dispara. |
| `DetectConsumerBacklogHigh` | El motor va con retraso. Mientras lo esté, queda suprimida la detección de ausencias **por silencio**; un evento posterior sigue disparando una ausencia vencida, como se explica arriba. |
| `DetectWatermarkLagHigh` | El sentido del tiempo del evento del motor se está quedando atrás respecto al tiempo real. |
| `DetectFanoutEvalErrors` | Una o más reglas publicadas están fallando al evaluarse. Vea la advertencia de arriba. |
| `ReactPoisonDropping` | Algunas acciones de una detección fallaron en todos los intentos de entrega; normalmente command-delivery o el bus de mensajes estuvo sin servicio varios minutos. Las demás acciones se intentaron en cada entrega; cada mensaje de la cola nombra las que fallaron. Nada las reprocesa. Trátelo como urgente. |
| `DeadLetterWriteLost` | Algo se abandonó **y** no se pudo escribir en el flujo de mensajes no entregados. Revise el bróker y el registro del servicio: una carta que el propio servicio se negó a escribir también termina aquí, igual que un evento entrante cuyo registro de fallo device-management no pudo codificar (el evento se confirma y nada de lo publicado dice que falló), igual que un mensaje de esa cola en el que el almacén de mensajes no entregados o la reconciliación de comandos agotó sus intentos. |
| `DeadLetterStoreLosing` | Los mensajes llegaron al flujo pero no se pudieron escribir en el almacén, así que caducarán sin quedar registrados. Revise la base de datos del operador. |
| `ReactConnectorEgressShedding` | Un inquilino supera su tasa de salida en la línea de tiempo en que su telemetría llegó a la plataforma, y sus acciones de salida se están descartando. Cada una se envía a la cola de mensajes no entregados con motivo `shed`, dentro de un presupuesto; léalas con `dcctl dead-letters`. Una puesta al día tras un reinicio no causa esta alerta. |
| `ReactShedLettersOverBudget` | Un inquilino descarta acciones de salida más rápido de lo que se registran una a una, así que el exceso se resume en un mensaje no entregado por inquilino y minuto. |
| `RateMeteringClockFallback` | Durante una hora, las acciones de salida se han medido según la hora del bróker o de llegada porque no llevaban hora de desencadenamiento, así que una puesta al día puede volver a descartarse como una inundación. Compruebe que event-processing y outbound-connectors ejecutan la misma versión. Una hora de desencadenamiento posterior a la hora del bróker de su mensaje se cuenta aparte, con el origen `capped`, y no dispara este aviso: es un desfase de reloj entre el pod y el bróker, no una hora que falte. |
| `DetectFactReconcileFailing` | El motor no puede comparar sus reglas, versiones de perfil, dispositivos y umbrales con device-management. Mientras falla no se elimina nada, pero un cambio perdido sigue perdido hasta que se recupere. |
| `DetectRuleStatWriteFailing` | No se pueden escribir los contadores de salud de reglas. La detección no se ve afectada, pero los recuentos de disparos por regla de la consola se quedan cortos: cada escritura fallida se descarta, no se reintenta, y los disparos que llevaba se cuentan en `devicechain_eventprocessing_detect_rule_stat_updates_discarded_total`. El error está en el registro de event-processing; revise su conexión a la base de datos. |
| `DetectLiveGapFillFailing` | La detección en vivo se ha detenido porque no se puede leer una parte del flujo de eventos. El motor no salta eventos que no ha visto, así que no aplica nada nuevo hasta que el broker responda; mientras tanto crece el retraso. Los cambios de reglas y de tenants se siguen aplicando y no se pierde nada. Revise el broker y la conexión del motor con él. Si pasados diez minutos sigue sin poder leerse el mismo rango, el motor reinicia su periodo de detección y lo vuelve a leer desde el flujo. Si el flujo sigue sin poder leerse, la reconstrucción también falla y, tras cinco reconstrucciones fallidas, el pod se reinicia: una caída de JetStream de más de diez minutos, con el arrendamiento intacto, termina en un reinicio del pod. |
| `DetectLiveDeliveriesRecovered` | Eventos que el broker entregó nunca llegaron al motor, y este los leyó del flujo y los aplicó en orden. La detección es correcta. Si se repite, las conexiones con el broker se están cayendo o dos motores consumen a la vez. |
| `DetectFactsRepaired` | El motor corrigió un estado del que no se le había avisado. La detección vuelve a ser correcta; si se repite, se están perdiendo avisos. |
| `DeviceFactPublishFailing` | device-management no puede enviar avisos de cambio. Los cambios siguen llegando al motor, con unos minutos de retraso. |
| `DetectTenantOverStateBudget` | Un inquilino ha superado un techo que no se aplica: su número de reglas, sus ventanas y temporizadores vivos, o las lecturas que retienen sus ventanas abiertas. |
| `DetectLeaderIsNotConsuming` | Una réplica es dueña de la partición pero no está dentro de un turno activo y no ha confirmado ningún punto de control en diez minutos. Está atascada construyendo un turno, lo que retiene la partición frente a todas las standby mientras no se detecta nada. Una reproducción larga no la dispara, porque una reproducción va confirmando puntos de control. Lea el log de la réplica alrededor de la construcción de su turno y reinicie ese pod para que una standby pueda tomar el relevo. |
| `GeoFenceSetPublishFailing` | device-management no pudo publicar un manifiesto de conjunto de geocercas: un error de serialización, un rechazo del broker o un fallo de transporte. La edición de la geocerca en sí tuvo éxito y es duradera; lo que falló es avisar al motor, así que la contención conserva el conjunto de geocercas anterior hasta que la pasada de reconciliación de cinco minutos lo repare. Una tasa sostenida significa que cada edición de geocercas tarda minutos en surtir efecto. Si empezó tras un cambio de configuración del broker, compruebe que `infrastructure.nats.streamMaxMsgSize` no esté fijado por debajo de lo que necesita un manifiesto; device-management registra al arrancar un aviso que nombra ambos tamaños. |
| `DetectFenceArchiveSkew` | El motor pidió a device-management un manifiesto de conjunto de geocercas y se le dijo que la consulta no existe, así que los dos servicios ejecutan versiones a ambos lados del cambio de entrega de manifiestos. Es lo esperado durante un momento mientras avanza una actualización, porque los dos Deployments no se esperan entre sí. Si se sostiene, device-management va por detrás, lo más probable es que se haya revertido por su cuenta, y las ediciones de geocercas no llegan al motor hasta que avance de nuevo. |
| `DetectFenceGeometryUnresolved` | Un conjunto de geocercas nombra geocercas cuya geometría el motor no pudo obtener del archivo de device-management. Esas geocercas se instalan llevando su error, así que la contención las informa como irresolubles en lugar de responder, lo que también se ve en `DetectFanoutEvalErrors`. Se repara en la siguiente pasada de reconciliación si la causa era transitoria; si se sostiene, la lectura del archivo está fallando, así que revise la salud de device-management y su conexión con el token de servicio. El inquilino y la geocerca se nombran en el log de errores del motor, nunca en el contador. |
| `DetectFenceGeometryHashMismatch` | **Crítica.** Un documento de geometría no tuvo el hash de la dirección bajo la que se pidió. Una dirección es el SHA-256 del documento almacenado bajo ella, así que esto nunca es transitorio ni benigno: device-management sirvió la fila equivocada, algo recodificó el documento en tránsito, o el archivo está corrupto. Las geocercas afectadas se rechazan en lugar de compilarse, así que la contención se degrada en lugar de responder mal. Investigue antes de reiniciar nada; un reinicio oculta el síntoma y no cambia nada. |

:::note Un motor que pierde una carrera de cerebro dividido termina
Si alguna vez dos motores actúan a la vez como escritor, aquel cuyo punto de control se rechaza por
obsoleto deja de detectar, informa que no está listo y termina con un estado distinto de cero para
que se lo reemplace. No sigue en marcha aparentando estar sano. Lo que deja tras de sí es un reinicio
de un pod de event-processing, cuyas últimas líneas de registro indican que el punto de control se
rechazó por obsoleto. Si ninguna réplica tiene la partición durante dos minutos, se dispara
`DetectHasNoLeader`.
:::

El estado por regla, la hora del último disparo y el conteo de disparos están disponibles en la
consola, en la pestaña **Salud de las reglas** del perfil de dispositivo, junto a un feed en vivo de
las detecciones a medida que ocurren.

### En qué se va el tiempo del bucle de detección {#loop-phases}

Cuando el motor va con retraso (`DetectConsumerBacklogHigh`), lo que decide qué hacer es saber en qué
gasta el tiempo el bucle. `devicechain_eventprocessing_detect_loop_seconds_total` lo responde: los
segundos que el bucle ha pasado en cada fase, con la etiqueta `phase`. Las fases se reparten el tiempo
del bucle, así que la tasa de una es su parte del tiempo real, y que las fases distintas de
`fetch_wait` sumen 1 significa que el bucle no tiene tiempo libre. (`parked` y `probe` son espera, no
trabajo, pero es tiempo que el bucle no pudo dedicar a mensajes.)

| `phase` | El bucle está |
|---|---|
| `fetch_wait` | ocioso, esperando el siguiente mensaje o tic. |
| `decode` | leyendo un mensaje: la comprobación de secuencia, el inquilino y la carga. |
| `plan` | decidiendo qué reglas alimenta una lectura y evaluando sus condiciones. |
| `apply` | ejecutando el motor de detección con el resultado. |
| `publish` | entregando al broker las detecciones de un punto de control. |
| `save` | escribiendo en la base de datos el estado del motor en un punto de control. |
| `ack` | confirmando los mensajes que un punto de control dejó a salvo. |
| `gapfill` | leyendo un rango de eventos que el broker dio por entregados y el motor nunca recibió. |
| `control` | todo lo que ocurre entre mensajes: actualizaciones de reglas, dispositivos y umbrales, eliminaciones de inquilinos y el mantenimiento periódico. |
| `parked` | esperando con los mensajes en vivo desactivados, ya sea tras un avance de reloj que no se pudo confirmar o tras un hueco en el flujo de eventos que no se pudo leer. Es el motor negándose a trabajar hasta que un reintento tenga éxito. |
| `probe` | esperando al broker su recuento de pendientes, que el motor pide para decidir si está al día y para informar de su retraso. |

Un retraso con la mayor parte del tiempo en `fetch_wait` no es el límite del motor: espera a que lleguen
mensajes, y más capacidad del motor no ayudará. El tiempo en `parked` o `probe` es la misma clase de
espera: a un reintento o al broker, no al motor. Si la mayor parte está en `plan` y `apply`, el límite es el propio
motor. Si está en `publish`, `save` y `ack`, el coste son los puntos de control, y
`devicechain_eventprocessing_detect_checkpoint_duration_seconds` muestra cuánto tarda cada uno.
(`devicechain_eventprocessing_detect_checkpoint_seconds` conserva solo el último.) Dos pares de
distribuciones dicen qué parte del punto de control es. El tiempo en `publish` es sobre todo una
espera por detección: la confirmación del evento derivado por el broker
(`devicechain_eventprocessing_detect_derived_publish_ack_seconds`). Los contadores de salud de reglas
los escribe un proceso aparte en segundo plano, así que su tiempo de base de datos no está en ninguna
de estas fases; es `devicechain_eventprocessing_detect_rule_stat_write_seconds`, una observación por
escritura de inquilino. Con `derivedPublishing.maxInFlight` por encima de 1 las esperas de
confirmación se solapan, así que `publish` es menor que su suma;
`devicechain_eventprocessing_detect_derived_publish_pass_seconds` es la publicación completa de cada
punto de control. El tiempo
en `save` es la serialización del estado del motor
(`devicechain_eventprocessing_detect_checkpoint_serialize_seconds`) y después su escritura en la base
de datos (`devicechain_eventprocessing_detect_checkpoint_save_seconds`).

```promql
sum by (phase) (rate(devicechain_eventprocessing_detect_loop_seconds_total[5m]))
```

## Eliminar un inquilino

El motor de detección mantiene el estado del inquilino como un punto de control opaco que ninguna
consulta puede interpretar, así que una [eliminación de inquilino](./tenant-deletion.md) le pide
directamente al motor que lo expulse y espera a que el motor confirme que la expulsión ha quedado
**grabada en un punto de control**, no meramente aplicada en memoria. Por tanto, una instancia que
ejecuta detección debe tener el motor accesible para que una eliminación pueda completarse; si el
motor está detenido o inaccesible, la eliminación queda abierta en lugar de completarse sobre datos
que siguen ahí.
