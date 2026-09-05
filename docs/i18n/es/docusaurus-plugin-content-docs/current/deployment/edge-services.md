---
sidebar_position: 8
title: Cómo operar los servicios de borde
---

# Cómo operar los servicios de borde

Tres componentes de DeviceChain viven en el borde de la plataforma, y ninguno se comporta como los
servicios sin estado que los rodean. La [ingesta de Sparkplug-B](../concepts/sparkplug.md) y la
[ingesta LwM2M](../concepts/lwm2m.md) son **transportes que afirman presencia**: se les *informa*
cuándo un dispositivo se conecta y se desconecta, en lugar de deducirlo del silencio. El **agente de
borde** es un binario aparte que se ejecuta en un equipo dentro de una planta, almacena localmente
cuando el enlace con la nube se cae, y reenvía cuando vuelve.

Los tres se ejecutan como una **única instancia**, los tres mantienen estado vivo que ninguna base de
datos guarda, y lo que cuesta un reinicio es distinto en cada uno. Esta página es el contrato del
operador: cuántas instancias se ejecutan y por qué, qué pierde un relevo, qué garantiza la presencia y
qué no, y qué vigilar.

Si lo que busca es qué hace cada protocolo o cómo se corresponde un dispositivo con la plataforma,
empiece por [Sparkplug-B](../concepts/sparkplug.md), [LwM2M](../concepts/lwm2m.md) o
[Presencia de dispositivo](../concepts/device-presence.md).

Ambos servicios de ingesta son **opcionales** (opt-in). Ninguno está en el conjunto predeterminado de
áreas funcionales —se habilitan de forma deliberada— y cada uno depende de forma dura de la gestión de
dispositivos, que resuelve lo que ellos producen.

## Una sola instancia de cada uno, y por razones distintas

Conviene saber que son tres argumentos separados, no una misma política aplicada tres veces.

| Servicio | Por qué exactamente una | Qué haría una segunda |
|---|---|---|
| **Ingesta de Sparkplug** | Se une a su broker como **Host Application** de Sparkplug, y un entorno de Sparkplug tiene una sola. | Publicar un STATE de host contradictorio e ingerir cada mensaje dos veces. |
| **Ingesta LwM2M** | DTLS es una **sesión con estado sobre un único socket UDP enlazado**. | Una réplica en espera que también enlazara el socket recibiría —y descartaría— silenciosamente la parte de los datagramas que le tocara. El tráfico desaparece en lugar de fallar de forma ruidosa. |
| **Agente de borde** | Es dueño de un directorio de almacenamiento local y de una identidad en el enlace ascendente hacia la nube. | Dos sobre un mismo directorio chocan en los bloqueos de archivo; dos que comparten identidad se expulsan mutuamente del enlace en bucle. |

En los dos servicios de ingesta esto no solo está documentado: está impuesto. Cada uno toma un
**arrendamiento (lease) de propiedad** con una ventana de 30 segundos: un pod de reemplazo no conecta
ni enlaza nada hasta que lo tiene, de modo que la ventana en la que dos de ellos sirven queda
**acotada**: por la ventana del arrendamiento más un intervalo de renovación (unos diez segundos),
porque un líder que ha perdido el arrendamiento se autoexpulsa solo cuando su siguiente renovación lo
advierte, y aun entonces todavía tiene que deshacer su estado de broker o de DTLS. Está acotada, no
eliminada, y **nada valla las escrituras de un líder obsoleto en estas dos vías**: el arrendamiento
lleva su época, pero ninguna vía de ingesta rechaza en función de ella. El chart, por su parte, se
niega a renderizar cualquiera de las dos áreas con más de una réplica, y ese rechazo está ligado
**al área en sí**, no a la estrategia de despliegue con la que esté configurada: cambiar `strategy`
a `RollingUpdate` no lo esquiva.

:::warning Ningún servicio de ingesta recibe un presupuesto de interrupción de pods
El chart omite el presupuesto de interrupción para cualquier área que se ejecute con una sola réplica,
porque un presupuesto que exigiera un pod disponible bloquearía el drenaje de nodo por completo. Por
tanto, drenar el nodo en el que casualmente está un servicio de borde **detiene ese transporte** hasta
que el pod se reprograma y toma el arrendamiento. La recuperación es automática, pero no es inmediata:
prefiera un despliegue deliberado a drenar ese nodo.
:::

## Qué cuesta un relevo

Un reinicio o un cambio de liderazgo es rutina en ambos servicios, y los dos vuelven por sí solos.
Ahora bien, lo que recuperan no es lo mismo.

| | Ingesta de Sparkplug | Ingesta LwM2M |
|---|---|---|
| **Presencia** | **Se reconstruye.** El nuevo líder restablece la sesión, pide a los nodos de borde que se vuelvan a anunciar y reconcilia qué dispositivos están realmente vivos, de modo que no se pierde una desconexión ocurrida durante el relevo. | **Se reconstruye** a partir de la proyección durable y del tiempo de vida de registro de cada dispositivo, en lugar de sondear, así que un dispositivo dormido en modo cola no se marca falsamente como fuera de línea. |
| **Telemetría durante la ventana** | Se pierde. Un broker no retiene DATA para un host que no está conectado. | Los datagramas enviados durante la ventana se pierden; los mensajes CoAP confirmables los retransmite el dispositivo. |
| **Recursos observados** | No aplica. | **No se restablecen. Vea más abajo.** |
| **Tiempo de recuperación** | Lo que el pod de reemplazo necesite para planificarse y arrancar, más hasta los 30 segundos de la ventana del arrendamiento. | Lo mismo, más el enlace del socket. |

:::danger Las observaciones LwM2M se pierden en un relevo y nada las vuelve a crear
Este es el hecho operativo más sorprendente de esta página.

DeviceChain le pide a un dispositivo LwM2M que **Observe** sus recursos para que él mismo envíe las
lecturas. Esas observaciones viven únicamente mientras dura el proceso que las estableció. Un
reinicio, un despliegue o un cambio de liderazgo **pierde todas y cada una de ellas, y nada las vuelve
a crear.**

La presencia vuelve. La telemetría no. Un dispositivo vuelve a reportar solo cuando se **vuelve a
registrar**, que es su propio comportamiento, en su propio calendario, sin más límite que su tiempo de
vida de registro. Con el valor predeterminado de fábrica de `86400` segundos, eso son **hasta un día
de silencio de un dispositivo perfectamente sano**, y con el dispositivo en línea en la consola
durante todo ese tiempo.

Si no puede tolerarlo, baje `maxLifetimeSeconds` (vea los ajustes de LwM2M más abajo), pero nunca por
debajo del mayor tiempo de vida que pidan realmente sus dispositivos, o quedarán expirados como
muertos en cada relevo.
:::

## Qué garantiza la presencia

Todo dispositivo lleva una **fuente de presencia** —`INFERRED` o `ASSERTED`— y las reglas que la rigen
son más estrechas de lo que parecen.

**Solo la fuente puede devolver un dispositivo.** Un dispositivo pasa a afirmado la primera vez que un
transporte autoritativo habla por él, y ningún tiempo de espera, ningún evento de datos y ninguna
cantidad de silencio lo devuelven a inferido. Lo único que lo hace es una **degradación**: una
afirmación de la *fuente* de que ya no habla por ese dispositivo, nunca una afirmación sobre el
dispositivo en sí. Vea [Devolver un dispositivo a presencia inferida](#demoting-a-device). Un
dispositivo que antes llegaba por Sparkplug o LwM2M y ahora llega por MQTT simple conserva su fuente
afirmada, y sigue exento del barrido de inactividad.

**El orden lo fija una identidad de sesión acuñada por la plataforma, nunca nada que envíe el
dispositivo.** Cada par de conexión/desconexión se sella con un marcador de sesión que genera la
plataforma. El número de secuencia de nacimiento/muerte de Sparkplug se lee solo para emparejar una
muerte con el nacimiento al que pertenece —nunca se compara por magnitud, porque desborda y vuelve a
empezar— y el identificador de registro de LwM2M nunca se usa como identidad de sesión. La
consecuencia para usted es que un mensaje retrasado o repetido de una sesión anterior no puede
derribar una sesión viva, en ninguno de los dos transportes.

Los marcadores se acuñan con el reloj del nodo del broker que aceptó la conexión, así que en un
clúster de varios nodos **no** está garantizado que el marcador de una sesión nueva quede por encima
del anterior: un nodo cuyo reloj va por detrás de sus pares acuña uno menor. La plataforma concilia
ese caso en lugar de darlo por imposible: un dispositivo que se encuentra activo en una sesión que
queda por debajo de la almacenada se vuelve a archivar en la sesión en la que realmente está, de modo
que su desconexión posterior se sigue reconociendo.

**Un dispositivo no puede afirmar su propia presencia.** Un evento de conexión/desconexión enviado por
la vía habitual de carga útil orientada al dispositivo se rechaza de plano, no simplemente se ignora.
Solo los propios transportes los producen. Esto importa porque un dispositivo afirmado está exento del
barrido de inactividad: un dispositivo capaz de declararse conectado podría fijarse en línea de forma
permanente.

**El tiempo de espera inferido es de diez minutos y no es ajustable.** Los dispositivos sin un
transporte que afirme presencia se marcan fuera de línea tras diez minutos de silencio, con una
revisión cada minuto. Hoy no hay ni anulación por dispositivo ni ajuste alguno, así que no lo busque.
Es además el tiempo de espera bajo el que vuelve a quedar un dispositivo degradado, que es buena
parte del sentido de degradarlo.

## Un dispositivo que figura en línea y no lo está

Este es el modo de fallo que hay que entender antes de apoyar en la presencia cualquier cosa que
despierte a una persona.

**Un dispositivo afirmado que muere sin decirlo puede figurar en línea indefinidamente.** El barrido
de inactividad omite deliberadamente a los dispositivos afirmados —el sentido mismo de un transporte
que afirma presencia es que el silencio no es prueba de muerte— y en estos dos transportes **no hay
nada más que tenga un tiempo de espera, un perro guardián ni un barredor.** Dos cosas lo limpian, y
ninguna de ellas es un tiempo de espera.

La primera es una señal nueva del propio transporte del dispositivo. Los dispositivos afirmados por el
broker propio de DeviceChain obtienen una sin que el dispositivo haga nada: allí una pasada de
reparación compara periódicamente la lista de conexiones vivas del broker con lo que la plataforma
cree y corrige la diferencia. Vea [Presencia de dispositivo](../concepts/device-presence.md). En
Sparkplug y LwM2M no existe tal pasada, así que la señal tiene que venir del dispositivo.

La segunda es una [degradación](#demoting-a-device), y es la respuesta cuando la primera no va a
llegar nunca, porque la fuente que tendría que producirla ya no está. Funciona en los tres
transportes que afirman presencia.

Las formas concretas en que ocurre:

- **Un certificado de muerte de Sparkplug perdido.** Si el broker nunca entrega el DEATH del nodo, el
  dispositivo sigue en línea hasta la siguiente reconciliación, y la reconciliación se ejecuta **solo
  cuando el host se reconecta al broker**. Un host que permanece conectado de forma estable y
  sencillamente nunca vuelve a saber de ese nodo no la vuelve a ejecutar jamás.
- **Un nodo que se vuelve a anunciar.** Cuando un nodo de borde da a luz una sesión nueva, los
  dispositivos hijos de su sesión anterior se reemplazan junto con ella. Un dispositivo hijo que no se
  vuelva a anunciar bajo la nueva sesión queda mostrándose conectado, sin nada que lo corrija hasta la
  siguiente reconciliación.
- **Un registro LwM2M que simplemente es largo.** Un dispositivo que desaparece se marca fuera de
  línea cuando caduca su tiempo de vida de registro; con el valor predeterminado eso son **86400
  segundos**, un día entero.
- **Un dispositivo cuyo transporte afirmante se elimina.** Dé de baja la fuente de Sparkplug o la
  credencial LwM2M por la que llegaba un dispositivo y nunca volverá a producirse una señal para él.
  Queda varado en su último estado afirmado hasta que alguien lo [libere](#demoting-a-device), que es
  exactamente para lo que existe esa operación: una fuente que ya no está no va a contarle nada más a
  la plataforma.

**La única palanca en estos dos transportes es `maxLifetimeSeconds`**, y solo se aplica a LwM2M. El tiempo de vida
de cada registro se recorta hasta ese valor como máximo, de modo que acota directamente cuánto tiempo
puede figurar en línea un dispositivo LwM2M muerto. Ponerlo en, digamos, 3600 lo limita a una hora. La
restricción es la de arriba: debe quedar por encima del mayor tiempo de vida que pida realmente su
flota. La vía MQTT tiene su propia cota: el intervalo de la pasada de reparación,
`brokerPresence.reconcileSeconds`, cinco minutos de forma predeterminada.

En la vía de Sparkplug no hay palanca equivalente. Si que un dispositivo Sparkplug figure en línea
tiene peso operativo para usted, empareje la señal de conectividad con una
[regla de ausencia](../concepts/event-processing.md) basada en tiempo de espera, que se dispara con el
silencio independientemente de lo que diga la presencia.

### Devolver un dispositivo a presencia inferida {#demoting-a-device}

Una **degradación** es la única transición de `ASSERTED` de vuelta a `INFERRED`. Es una afirmación de
la fuente, no sobre el dispositivo: dice que la fuente renuncia a su custodia, y no afirma
absolutamente nada sobre la conectividad. Si el dispositivo figura en línea, cuándo se conectó por
última vez, cuándo se desconectó y cuándo reportó por última vez quedan exactamente como estaban.

Lo que cambia es quién puede corregir esos valores. Un dispositivo afirmado suprime los dos mecanismos
de reparación de la plataforma; liberarlo se lo devuelve a ambos, lo que repara los dos sentidos de la
congelación a la vez:

- Un dispositivo congelado **en línea** vuelve a ser visible para el barrido de inactividad, y se
  marca fuera de línea diez minutos después de su última actividad real.
- Un dispositivo congelado **fuera de línea** deja de tener sus comandos retenidos. La retención
  depende de que el dispositivo esté afirmado *y* no activo, así que a un dispositivo inferido se le
  despacha; la pasada periódica que revisa el conjunto retenido libera la acumulación en un par de
  minutos, y el propio dispositivo vuelve a figurar en línea con su siguiente lectura. Este es el
  sentido que conviene arreglar pronto, porque los comandos retenidos cuentan contra un
  [techo por inquilino](../concepts/commands.md#held-command-ceiling): los dispositivos atascados
  fuera de línea por una fuente que se fue pueden acabar rechazando encolados de los dispositivos
  sanos que tienen al lado.

Hay dos formas de que ocurra.

#### Una fuente libera sus propios dispositivos cuando se apaga

Cuando la presencia MQTT afirmada por el broker se niega a arrancar porque se **desactivó
deliberadamente**, porque **falta la credencial de cuenta de sistema de NATS** o porque **no se
consigue alcanzar el broker**, `event-sources` recorre los dispositivos que aún tiene afirmados y los
libera.

Son tres de las seis razones por las que la toma puede no arrancar, y la línea es deliberada. Las dos
primeras son configuración, así que todas las réplicas de la instancia leen los mismos valores y
llegan a la misma conclusión — la liberación es la instancia hablando, no una réplica adivinando.

La tercera es evidencia de otro tipo, y conviene decir qué significa en realidad. La toma da treinta
segundos a su conexión para alcanzar el broker antes de darlo por inalcanzable, así que esta razón es
medio minuto sin conexión con la cuenta de sistema, no un intento fallido: un broker caído, o una
credencial que el broker rechaza. La pasarela MQTT vive en ese mismo broker, así que mientras esté
inalcanzable no hay ningún dispositivo conectado por ella — la liberación no es una suposición sobre
la flota, es la única lectura compatible con que el broker no esté.

Es además la única de las tres cuya verdad puede cambiar con el pod ya en marcha, así que es la única
que sigue preguntando. **Cada pasada de liberación vuelve a marcar primero contra la cuenta de
sistema, la primera incluida. Si el broker responde, no se libera nada: el servicio termina y el pod
se reinicia**, y el reemplazo marca contra el broker con normalidad y ejecuta su toma. Un broker que
vuelve aparece por tanto como un reinicio de pod, no como una flota de dispositivos liberados. Sin
eso la liberación seguiría sin más: la pasada recorre lo que esté afirmado *ahora* según el intervalo
de reconciliación, así que con pares que siguen afirmando, las dos se turnarían sobre cada fila
indefinidamente, y en el hueco entre ambas el barrido de inactividad marcaría fuera de línea a los
dispositivos conectados pero silenciosos.

Lo que **no** libera es una suscripción que falla sobre una conexión que sí alcanzó el broker —esa sí
es mala suerte de esa réplica en concreto, sus pares pueden estar leyendo los avisos perfectamente— y
las dos razones que significan que esta instancia no tiene toma que ejecutar: ninguna fuente apuntando
al broker de la plataforma, y ninguna configuración de llamadas entre servicios. Las seis ponen
`presence_tap_off{reason}` de todos modos, que es cómo se distingue cuál se tiene.

Tres propiedades de la liberación automática conviene conocerlas antes de depender de ella:

- **Una credencial ausente y un broker inalcanzable esperan dos minutos primero.** Un arranque inicial
  acuña esa credencial y renueva el broker en la misma ejecución que pone en marcha los servicios, así
  que cualquiera de los dos puede ser simplemente una carrera con esa ejecución y no una condición
  permanente. Un `enabled: false` escrito es inequívoco, y actúa de inmediato. Para el broker la
  espera es además una **comprobación** —véase más arriba—; para la credencial no puede serlo, porque
  la configuración se lee una sola vez al arrancar y un cambio renueva el pod.
- **Necesita una fuente de pasarela y configuración de llamadas entre servicios propias**: algo bajo
  lo que emitir, y una manera de enumerar inquilinos y leer la proyección. Sin eso no se ejecuta en
  absoluto. Registra que no lo hizo y apunta a la puerta manual, que entonces es la única.
- **Va a ritmo pausado y se vacía sola.** Las liberaciones salen a 25 dispositivos por segundo, y un
  dispositivo liberado abandona el conjunto que se recorre, así que una pasada interrumpida se reanuda
  gratis en lugar de empezar de nuevo. `presence_still_asserted` es cuánto queda; una liberación sana
  lo lleva a cero y lo deja ahí.

Nada libera automáticamente los dispositivos de Sparkplug ni de LwM2M. Esas fuentes desaparecen porque
un operador las retiró, no porque cambiara una bandera, así que un operador es quien las libera.

#### Un operador los libera a mano

`dcctl presence demote` recorre los dispositivos afirmados de una fuente en un inquilino y libera cada
uno:

```
dcctl presence demote --tenant acme --source sparkplug:plant-a \
  --reason "plant-a gateway decommissioned"
```

| Opción | |
|---|---|
| `--tenant` | Obligatoria. Una degradación actúa sobre un inquilino. |
| `--source` | Obligatoria, y nunca se infiere: el radio de acción es una fuente de eventos entera. Pásela exactamente como la reporta el estado del dispositivo: el identificador configurado de la propia fuente para MQTT y HTTP (`mqtt1`, `http1`), `sparkplug:{hostId}` para Sparkplug, `lwm2m` para LwM2M. |
| `--device` | Repetible. Acota a dispositivos concretos *dentro* de la fuente; omítala para liberar la fuente entera. |
| `--reason` | Obligatoria. Se registra con cada evento que emite la ejecución: el único rastro de una escritura de presencia sobre toda una flota. |
| `--page` | Dispositivos por llamada, `200` de forma predeterminada. |
| `--dry-run` | Informa de lo que se liberaría, y no libera nada. |

Una fuente que nadie usa no es un error: sencillamente no coincide con nada. Así que una primera
página que no coincide con ningún dispositivo es mucho más probable que sea un `--source` mal escrito
que un trabajo terminado, y el comando lo dice en lugar de informar de éxito.

La misma operación está disponible en la API como la mutación `demoteAssertedPresence` de
`device-state`. Requiere el permiso `state:demote`, que es de escritura, no forma parte de la base de
solo lectura que recibe todo miembro, y no lo tiene ningún rol de forma predeterminada: concédalo
explícitamente al rol que lo necesite.

#### Una liberación se contabiliza como cualquier otro evento de presencia

Pasa por el mismo [techo de ingesta](../concepts/governance.md) por inquilino que una conexión o una
desconexión, así que a un inquilino ya en su techo se le puede rechazar la *reparación* junto con la
rotación que causa la presión — contadas en `presence_events_refused_total`. No se pierde nada: una
liberación rechazada deja el dispositivo afirmado, así que la siguiente pasada lo vuelve a encontrar.
La reparación simplemente llega no antes de lo que el techo permita.

## Pertenencia a inquilino en ambos transportes

**La pertenencia a inquilino la fija la conexión en ambos transportes, y nunca se lee de contenido
suministrado por el dispositivo.** Es la propiedad más fuerte de esta parte de la plataforma y se
cumple en las dos vías.

- **Sparkplug.** Todo mensaje se atribuye al inquilino configurado para la **conexión de broker por la
  que llegó**. El Group ID de Sparkplug que va en el topic es una etiqueta propia del cliente —ni es
  globalmente única ni deja de ser fijable por cualquier publicador—, así que nunca nombra a un
  inquilino. La configuración rechaza dos inquilinos sobre un mismo endpoint de broker, porque
  entonces el Group ID sería lo único que los separaría.
- **LwM2M.** Cada dispositivo queda ligado a su inquilino por la **identidad de clave precompartida
  (PSK) DTLS autenticada** que presentó en el handshake. El nombre de endpoint que el dispositivo
  afirma en su propia carga útil de registro nunca se usa para identificarlo. Una identidad no
  aprovisionada falla el handshake, y el rechazo no devuelve la identidad como eco, de modo que un
  sondeo no puede enumerar credenciales válidas comparando respuestas de error.

:::caution En Sparkplug, la autenticación del dispositivo es a nivel de broker
Ambos transportes marcan su tráfico como autenticado por el transporte, que es lo que permite a la
plataforma confiar en una identidad de dispositivo bajo `deviceAuthMode: required` sin una segunda
credencial por evento. En LwM2M esa identidad está ligada a la PSK autenticada, así que es realmente
por dispositivo.

**En Sparkplug se deriva del topic, así que la autenticación es tan fina como la conexión de broker y
no más.** Activar la autenticación de dispositivo obligatoria *no* impide que un publicador en el
broker de un inquilino envíe bajo la identidad de otro dispositivo **dentro de ese mismo inquilino**.
El cruce entre inquilinos está cerrado en ambas vías —un publicador nunca puede alcanzar a otro
inquilino—, pero si la identidad de dispositivo dentro del inquilino le importa, impóngala con
credenciales por cliente y permisos de topic **en su propio broker**, que es donde vive realmente esa
frontera.
:::

:::danger Tres identificadores, tres funciones — y en estos transportes ninguno es el que usted escribió
Todo dispositivo en un transporte de borde lleva **tres** identificadores que cumplen tres funciones
distintas. Es fácil confundirlos porque dos parecen nombres y el tercero es generado. Aclárelos antes
de aprovisionar, porque los fallos son todos silenciosos o engañosos.

**1. El ancla de pertenencia a inquilino** — lo que decide a qué inquilino pertenecen los datos. En
Sparkplug es la **conexión de broker**; en LwM2M es la **identidad PSK autenticada**. Nunca nada que
venga en el mensaje. Tratado más arriba.

**2. La clave de resolución del dispositivo** — lo que decide *qué dispositivo*. En ambos transportes
de borde es el **id externo** del dispositivo, y no es algo que el dispositivo elija:

- En Sparkplug es la cadena `group/node[/device]` del topic, p. ej. `plant-a/line-3/press-1`.
- En LwM2M es el id externo que usted escribió **junto a la identidad PSK en la configuración del
  servicio** — no el nombre de endpoint (`ep`) que envía el dispositivo. `ep` se registra en el log y
  por lo demás se ignora. Un dispositivo cuyo firmware envía `ep=urn:imei:35…` nunca será emparejado
  por él, y nada se lo indicará.

**3. El token del dispositivo** — lo que la consola, la API y todos los eventos usan realmente. Es
**generado**, no elegido: el id externo contiene con frecuencia `/`, `.`, espacios o caracteres no
ASCII, nada de lo cual puede contener un token. Así, `plant-a/line-3/press-1` se convierte en algo
como `sp-plant-a-line-3-press-1-9f2c1a8b4d3e` (`lw-…` en LwM2M). El sufijo desambigua dos ids
externos que de otro modo se reducirían a la misma cadena.

**La consecuencia práctica, y es peor de lo que parece:** un dispositivo aprovisionado
automáticamente llega **sin nombre alguno**. El registro lleva solo el token, el id externo y el tipo
de dispositivo, así que la lista de dispositivos de la consola lo muestra bajo su token generado
`sp-…` / `lw-…` con un `—` en la columna Nombre. No hay nada ahí por lo que reconocerlo.

Y no puede buscarlo. **La lista de dispositivos de la consola no tiene caja de búsqueda** —es un
listado paginado sin más de Estado, Token, Nombre, Tipo, Descripción y Creado, sin columna de id
externo— y la búsqueda de dispositivos de la API solo admite número de página, tamaño de página y
tipo de dispositivo. Así que:

- **En la consola**, localícelo por su token. El token generado incrusta el id externo
  (`plant-a/line-3/press-1` → `sp-plant-a-line-3-press-1-…`), así que paginar la lista y leer la
  columna del token es toda la técnica.
- **Por la API**, use `devicesByExternalId`, que recibe ids externos exactos y devuelve los
  dispositivos. Es una consulta de coincidencia exacta, no una búsqueda: sin prefijos ni subcadenas.
  Nada en la consola la invoca, así que esta vía es solo de API.

Cuando un dispositivo no aparezca en absoluto, revise el identificador 2 antes de sospechar del
transporte — en LwM2M en particular, una identidad PSK equivocada falla en el handshake DTLS, antes
del registro, y el rechazo deliberadamente no le dice nada.
:::

## LwM2M: lo que un operador debe saber

**Solo se decodifica telemetría en SenML-JSON.** Las notificaciones en cualquier otro formato de
contenido se cuentan y se descartan. La consecuencia práctica no es evidente a partir del estándar:

:::warning Un cliente conforme solo con LwM2M 1.0 obtiene presencia y comandos, pero ninguna telemetría
SenML llegó con LwM2M 1.1. Un dispositivo que solo habla 1.0 se registrará, mantendrá su sesión,
impulsará la presencia correctamente y aceptará comandos Read/Write/Execute, y **nunca producirá una
sola medición**. Nada falla de forma ruidosa: las lecturas sencillamente no aparecen nunca.

La métrica que hay que revisar es **`observe_establish_refused_total`**. DeviceChain pide SenML-JSON
en el propio Observe, así que un cliente conforme solo con 1.0 rechaza el Observe con
`4.06 Not Acceptable` y luego no envía notificación alguna, lo cual se cuenta aquí y es la causa
dominante de este contador. `notify_unknown_content_format_total` se queda en **cero** para ese
dispositivo, porque cuenta el *otro* caso: un dispositivo que sí notifica, en un formato de contenido
que este adaptador no puede decodificar.
:::

**Las observaciones están acotadas y las cotas no son configurables.** DeviceChain establece una
observación por *instancia* de objeto, solo para objetos dentro de un rango IPSO fijo, y como máximo
**32 por registro**. La lista de objetos permitidos es una **propiedad fija de la compilación: no hay
ajuste alguno que añada a ella.** Si su flota reporta un recurso fuera de ese rango, ese recurso no se
observará y ninguna configuración lo cambiará. Vigile `observation_overflow_total` para los
dispositivos que superen el tope por registro.

**Las sesiones no se reciclan de forma predeterminada.** `idleTimeoutSeconds` es `0` por defecto, es
decir, nunca, que es lo correcto para dispositivos siempre conectados. Una flota en **modo cola**
debería fijarlo cómodamente por encima del intervalo de despertar esperado: si es demasiado bajo, las
claves de sesión de un durmiente se desalojan por debajo de él y se fuerza el rehandshake completo que
el Connection ID de DTLS existe precisamente para evitar.

:::danger Nada expone el puerto LwM2M fuera del clúster
El puerto CoAP/DTLS orientado al dispositivo es **UDP 5684**, y **ni el chart ni los módulos de
infraestructura lo exponen más allá del clúster.** Todos los servicios son internos al clúster, no hay
afinidad de sesión configurada en ninguna parte, y el controlador de ingress que se distribuye maneja
solo HTTP.

La exposición externa es explícitamente una decisión del operador, y **no hay implementación alguna de
ella distribuida**, así que una flota LwM2M real no puede alcanzar el servicio tal y como se instala.
Usted debe proporcionar la vía UDP por su cuenta (un servicio `LoadBalancer` o `NodePort`, o un proxy
UDP externo), y debe ser una vía que mantenga todos los datagramas de una sesión yendo al único pod
que sirve.
:::

### Ajustes de LwM2M

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `listen.port` | `5684` | El puerto UDP que enlaza el servidor CoAP/DTLS. |
| `security.connectionIdLength` | `8` | Longitud en bytes del Connection ID de DTLS. **Manténgalo distinto de cero** en flotas celulares o itinerantes: es lo que permite que una sesión sobreviva a un cambio de dirección. `0` lo desactiva y fuerza un rehandshake en cada reasignación. |
| `security.idleTimeoutSeconds` | `0` | Recicla una sesión sin tráfico tras ese tiempo. `0` no recicla nunca. Fíjelo por encima del intervalo de despertar en una flota en modo cola. |
| `security.handshakeTimeoutSeconds` | `10` | Acota un handshake DTLS, para que uno atascado no pueda inmovilizar recursos. |
| `security.maxSessions` | `100000` | Techo de la tabla de sesiones vivas. Un handshake por encima del techo se rechaza y se cuenta, nunca se admite en silencio. |
| `maxLifetimeSeconds` | `86400` | El techo hasta el que se recorta el tiempo de vida de todo registro. **Es la palanca que acota cuánto tiempo figura en línea un dispositivo muerto.** Debe quedar por encima del mayor tiempo de vida que pidan sus dispositivos. |
| `ingestRateLimit.messagesPerSecond` | `1000` | Techo de ingesta sostenida por inquilino. Sin definir o con un valor no positivo, recae en este predeterminado, nunca en ilimitado. |
| `ingestRateLimit.burst` | `2000` | Margen de ráfaga para lo anterior. |
| `downlink.timeoutSeconds` | `10` | Acota un intercambio de comando con un dispositivo. Al expirar, el comando se reporta como fallido en lugar de quedar colgado. Súbalo para durmientes celulares lentos. |
| `downlink.concurrency` | `16` | Paralelismo de comandos entre dispositivos. Los comandos de un mismo dispositivo se ejecutan siempre en orden, sea cual sea este valor. |

## Sparkplug: lo que un operador debe saber

**Cada fuente es una conexión saliente independiente.** Una fuente nombra un broker, un inquilino y
los grupos a los que suscribirse. Un broker inalcanzable se reintenta en su propio bucle con espera
creciente: **degrada esa única fuente, no el pod ni la fuente de ningún otro inquilino.** Para esto,
vigile `connect_failures_total` en lugar de la salud del pod.

**La reconexión la gestiona deliberadamente la plataforma y no la biblioteca cliente de MQTT.** Cada
reconexión abre una sesión genuinamente nueva con una marca de tiempo nueva, porque Sparkplug exige
que el nacimiento del host y su certificado de muerte lleven la misma marca de tiempo para que un nodo
de borde pueda rechazar una muerte retrasada de una sesión anterior. Es también la razón por la que
todas las réplicas comparten un mismo client id: lo que expulsa a un host zombi es el propio
desalojo por identificador duplicado del broker.

:::caution En la vía de Sparkplug no se acota ni la tasa de mensajes ni el tamaño de un mensaje
A diferencia de LwM2M y de las vías de ingesta de dispositivo habituales, la ingesta de Sparkplug **no
aplica ningún techo de ingesta por inquilino y no descarta nada**. El razonamiento es que su exposición
es un broker al que usted eligió conectarse deliberadamente, y no un endpoint abierto; pero la
consecuencia es suya: un nodo de borde desbocado en un broker configurado no se limita en la puerta.
Acótelo en el broker, o mediante los grupos a los que se suscribe.

**Son dos límites distintos, y aquí no se aplica ninguno.** El límite de tasa anterior mide
*mensajes*; el [techo de lecturas por mensaje](../guides/connecting-a-device.md#cuánto-puede-llevar-un-mensaje)
acota lo que un mensaje puede costar una vez admitido. Un DDATA de Sparkplug que lleve miles de
métricas es un solo mensaje y se convierte en una lectura almacenada por métrica: cada una, su propia
fila, actualización de estado y evaluación de reglas en la goroutine de detección compartida. Acote
el número de métricas por publicación en el nodo de borde, del mismo modo y por la misma razón por la
que acota su tasa.

La [puerta del ciclo de vida del inquilino](./tenant-deletion.md) sí sigue aplicándose: el tráfico de
un inquilino en eliminación se rechaza en esta vía como en cualquier otra, y se cuenta en
`tenant_deleted_dropped_total`.
:::

**Las identidades desconocidas son una elección.** Con el registro automático activado, una identidad
de Sparkplug sin dispositivo correspondiente crea uno. Con él desactivado, su telemetría se descarta y
se cuenta en `unknown_device_dropped_total`, que es la métrica que hay que revisar cuando un nodo de
borde está publicando y no aparece nada.

## El agente de borde

El agente de borde **no es una cuarta vía de ingesta**. Se ejecuta en una planta, presenta un endpoint
MQTT corriente a los dispositivos locales, almacena en disco local lo que publican y lo vuelve a
publicar sobre los mismos topics de dispositivo que la nube ya ingiere. Nada del lado de la plataforma
sabe que hubo un agente de por medio, y por eso no hay configuración con forma de agente en ningún
servicio de la nube.

**No es un área funcional del chart.** No aparece en ninguna lista de áreas ni en ningún perfil de
despliegue, y no se puede habilitar como se habilita un servicio. Se distribuye como binarios
estáticos y una imagen de contenedor, y usted lo despliega por su cuenta: una unidad de systemd en una
pasarela de planta, un contenedor o un manifiesto de Kubernetes escrito a mano en el borde.

**El almacenamiento local es un anillo que descarta lo más antiguo.** El almacén local es un búfer
durable en disco, de `1 GiB` de forma predeterminada. Cuando se llena, descarta los eventos sin
reenviar **más antiguos** para admitir los nuevos, nunca los más recientes. Esa dirección es
deliberada: a un dispositivo se le acusa recibo en el momento en que publica, desde la propia
persistencia del agente, así que descartar lo más nuevo tiraría justo aquello que el agente acaba de
prometer conservar y le dejaría un búfer obsoleto al final de una interrupción en lugar de uno
actual. Cada descarte se cuenta, como la primera secuencia del propio almacén menos el número de
eventos que este agente ha reenviado y confirmado: el segundo operando es contabilidad de entrega, y
está *persistido*, que es lo que permite que el conteo sobreviva a un reinicio en lugar de volver a
cero. Hay un caso que no queda cubierto: cuando ese conteo persistido falta —un primer arranque, o un
almacén cuyo archivo de progreso se eliminó— se siembra a partir de la primera secuencia actual del
almacén, así que todo lo ya desalojado se da por contabilizado y un reinicio en ese estado sí borra la
evidencia. Mantenga intacto el directorio del almacén entre reinicios si el conteo de descartes le
importa.

:::caution El colapso de duplicados al reconectar solo cubre cargas útiles JSON
Cuando el enlace ascendente vuelve, el agente reenvía todo lo que había almacenado. Para las **cargas
útiles de objeto JSON** estampa una identidad y un tiempo de evento estables ante reprocesamiento, de
modo que un mensaje ya entregado se funde con el existente en la comprobación de unicidad de la nube y
usted lo ve una sola vez.

**Cualquier otra forma de carga útil se reenvía tal cual y es «al menos una vez».** Una reconexión
tras un enlace inestable puede entregarlas dos veces de verdad. Si usa un decodificador que no sea
JSON detrás de un agente de borde, haga que lo que hace con esas lecturas tolere una repetición.
:::

Dos cosas más que conviene saber antes de desplegar uno:

- **El listener MQTT local está abierto salvo que configure una credencial.** Defina `local.username`
  y `local.passwordEnv` para exigirla. Dejarlo abierto es una postura válida de LAN de confianza, y el
  agente la anuncia con una advertencia ruidosa al arrancar para que la elección siga siendo visible;
  pero en cualquier caso es un control de acceso de red, no una identidad por dispositivo, y sobre
  MQTT en texto plano el secreto cruza la LAN en claro.
- **El endpoint de métricas y salud se enlaza solo a loopback.** El puerto MQTT de dispositivo es la
  única superficie del agente expuesta a la LAN, por diseño. Para raspar el agente desde otro sitio
  necesita algo en el propio equipo que lo retransmita.

### Ajustes del agente de borde

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `instanceId` | — | La instancia de nube hacia la que reenvía este agente. Las publicaciones vistas para otra instancia no se reenvían, y se cuentan en `instance_mismatched_total`. |
| `agentId` | — | La identidad de este agente en el enlace ascendente. **Debe ser única**: dos agentes que la comparten se desconectan mutuamente en bucle. |
| `local.listenPort` | `1883` | El puerto MQTT al que se conectan los dispositivos de la planta. |
| `local.storeDir` | — | Obligatorio. El directorio que contiene el almacén durable. Un agente por directorio. |
| `local.spoolMaxBytes` | `1 GiB` | Presupuesto del almacén. Más allá, se descartan los eventos más antiguos. El mínimo es 16 MiB. |
| `local.metricsPort` | `9090` | Puerto de métricas y salud, solo en loopback. Un `0` explícito desactiva el endpoint. |
| `uplink.brokerUrl` | — | Obligatorio. El endpoint MQTT de la nube al que reenviar. |
| `uplink.connectTimeoutSeconds` | `30` | Acota un intento de conexión del enlace ascendente. |
| `uplink.backoffMinSeconds` / `uplink.backoffMaxSeconds` | `1` / `60` | Cotas de la espera creciente de reconexión mientras el enlace está caído. |

## Qué vigilar

:::danger No se distribuye ninguna alerta ni ningún panel para nada de esto
Las reglas de alerta y el panel de Grafana que se distribuyen cubren el motor de detección, las bases
de datos y la replicación. **Ni una sola métrica de borde tiene alerta ni panel.** Todo lo que aparece
en las tablas de abajo se emite y se raspa; nada le avisará de ello hasta que usted escriba la regla.

**La primera alerta que hay que escribir es una alerta de «sin líder»**, en cada servicio de ingesta:

> `sum(devicechain_lwm2mingest_is_leader) != 1`
>
> `sum(devicechain_sparkplugingest_is_leader) != 1 or absent(devicechain_sparkplugingest_is_leader)`

Cero significa que nadie está sirviendo ese transporte y que todos los dispositivos que dependen de él
son inalcanzables en silencio. Cualquier valor distinto de uno merece despertar a alguien. Es la señal
con más peso de toda esta superficie y es la única de la que hoy nada le informa.

La mitad del `absent()` no es decorativa. El indicador de Sparkplug solo se registra cuando hay al
menos una fuente configurada, así que un pod que se ejecute sin fuentes no publica la serie **en
absoluto**, y un `!= 1` sobre un resultado vacío está vacío a su vez: una alerta muda, no una alerta
que se dispara. Y ese es justo el caso para el que existe la alerta. LwM2M registra su indicador de
forma incondicional, así que solo la expresión de Sparkplug necesita el emparejamiento.
:::

Todas las métricas llevan el prefijo `devicechain_` y el segmento de su propio servicio:
`devicechain_sparkplugingest_`, `devicechain_lwm2mingest_`, `devicechain_edge_`. Ninguna está
etiquetada por dispositivo ni por inquilino, así que ninguna supone un riesgo de cardinalidad al
rasparla.

**Ingesta de Sparkplug** (`devicechain_sparkplugingest_`):

| Señal | Significa |
|---|---|
| `is_leader` | 1 en el pod que sirve, 0 en el resto. **Alerte si la suma no es 1.** |
| `connect_failures_total` | Un broker configurado no es alcanzable. Que suba significa que una fuente está caída mientras el pod parece sano. |
| `messages_total` | Tráfico Sparkplug entrante. Una línea plana con una flota viva es el síntoma de una suscripción perdida o de una fuente muerta. |
| `presence_emitted_total` | Señales de conexión/desconexión producidas. |
| `rebirth_requests_total` | Nodos a los que se pide que se vuelvan a anunciar. Que suba de forma sostenida significa que un nodo no consigue resincronizarse. |
| `unknown_device_dropped_total` | Tráfico de identidades sin dispositivo, con el registro automático desactivado. |
| `decode_errors_total` / `ingest_failures_total` | Cargas útiles malformadas, y fallos al publicar hacia adelante. |
| `tenant_deleted_dropped_total` | Tráfico rechazado porque su inquilino está siendo eliminado. |

**Ingesta LwM2M** (`devicechain_lwm2mingest_`):

| Señal | Significa |
|---|---|
| `is_leader` | Como arriba. **Alerte si la suma no es 1.** |
| `active_registrations` / `active_sessions` / `active_observations` | La flota viva tal como la ve el servicio. **Vigile `active_observations` a través de un reinicio**: es como se ve la pérdida de observaciones descrita más arriba, y como se ve su recuperación. |
| `registrations_total` / `registration_updates_total` | Dispositivos que llegan y que mantienen vivas sus sesiones. |
| `registration_expiries_total` | Registros que caducaron en lugar de darse de baja: dispositivos que se desvanecieron. |
| `handshake_failures_total` / `auth_errors_total` | Dispositivos que fallan DTLS, e identidades no aprovisionadas. |
| `observe_establish_refused_total` | Un Observe que el dispositivo rechazó o que falló por otra causa. **El síntoma del cliente solo 1.0**: ese cliente responde al Observe SenML con `4.06` y nunca notifica. |
| `notify_unknown_content_format_total` | Telemetría que llega en un formato que no se decodifica — un dispositivo que *sí* notifica, de forma indescifrable. Cero para un cliente solo 1.0. |
| `notify_decode_failures_total` / `notify_samples_truncated_total` | Cargas útiles malformadas o demasiado grandes. |
| `notify_records_non_numeric_total` / `notify_records_non_finite_total` / `notify_records_unnamed_total` | Lecturas que traía un Notify y que no produjeron ninguna medición. **Que no sean numéricas es normal**: una lectura IPSO booleana o de texto es un dispositivo funcionando bien, y este contador es lo que distingue ese caso del de un dispositivo que se ha quedado callado, que desde aquí se ve igual. Los otros dos son fallos de firmware: un valor que resolvió a infinito o NaN, y una lectura sin ruta de recurso. |
| `observation_overflow_total` | Un registro que supera el tope de 32 observaciones. Algunos de sus recursos no se observan. |
| `ingest_messages_shed_total` / `ingest_samples_shed_total` | Un inquilino por encima de su techo de ingesta. |
| `shadows_reconstructed_total` | Presencia reconstruida tras un cambio de liderazgo. Un pico es la huella de un relevo. |
| `commands_failed_total` / `commands_not_served_total` | Comandos descendentes que no llegaron a destino. |

**Agente de borde** (`devicechain_edge_`):

| Señal | Significa |
|---|---|
| `uplink_connected` | 0 significa que la planta está almacenando. |
| `spool_oldest_age_seconds` | **La señal principal de trabajo pendiente.** Cuánto retraso lleva el agente, en tiempo de reloj. |
| `spool_used_bytes` / `spool_limit_bytes` | Cuán cerca está el almacén de empezar a descartar. |
| `dropped_total` | **Se han perdido datos.** Eventos antiguos desalojados para hacer sitio. Cualquier incremento es pérdida real. |
| `forward_errors_total` | Intentos de reenvío fallidos; el evento queda almacenado para volver a entregarse. |
| `received_total` / `forwarded_total` | Rendimiento de entrada y de salida. |
| `malformed_total` | Eventos descartados como no reenviables, en lugar de bloquear la cola que va detrás. |
| `local_auth_enabled` | 0 significa que el listener MQTT de la planta no exige credencial. |

## Qué no está validado

Dos límites honestos, para que pueda ponderarlos:

- **Ningún banco de pruebas distribuido ejercita una flota Sparkplug real.** Nada en el proyecto
  conduce un nodo de borde o un broker de terceros de extremo a extremo. El comportamiento de
  Sparkplug está cubierto por pruebas contra la propia implementación de la plataforma.
- **La vía LwM2M es la mejor validada de las dos.** Se ejercita contra una pila cliente LwM2M
  independiente de terceros, leyendo sus veredictos desde el lado del servidor, de modo que un cliente
  que se comporte mal no puede fabricar un aprobado. Esa suite se ejecuta de forma programada y es
  orientativa, no una puerta de publicación.

El agente de borde está cubierto por sus propias pruebas y no tiene validación de despliegue en
clúster. Trate un primer despliegue de agente como algo que hay que pilotar en una planta antes de que
sea una flota.
