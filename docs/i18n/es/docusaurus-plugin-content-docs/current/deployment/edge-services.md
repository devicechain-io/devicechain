---
sidebar_position: 9
title: Cómo operar los servicios de borde
---

# Cómo operar los servicios de borde

Tres componentes de DeviceChain viven en el borde de la plataforma, y ninguno se comporta como los
servicios sin estado que los rodean:

- La [ingesta de Sparkplug-B](../concepts/sparkplug.md) y la [ingesta LwM2M](../concepts/lwm2m.md)
  son **transportes que afirman presencia**. Se les *informa* cuándo un dispositivo se conecta y se
  desconecta, en lugar de deducirlo del silencio.
- El **agente de borde** es un binario aparte que se ejecuta en un equipo dentro de una planta.
  Almacena localmente cuando el enlace con la nube se cae, y reenvía cuando vuelve.

Los tres se ejecutan como una sola instancia, y los tres mantienen estado vivo que ninguna base de
datos guarda. Lo que cuesta un reinicio es distinto en cada uno. Esta página explica cuántas
instancias se ejecutan y por qué, qué pierde un relevo, qué garantiza la presencia y qué no, y qué
vigilar.

Si lo que buscas es qué hace cada protocolo o cómo se corresponde un dispositivo con la plataforma,
empieza por [Sparkplug-B](../concepts/sparkplug.md), [LwM2M](../concepts/lwm2m.md) o
[Presencia de dispositivo](../concepts/device-presence.md).

Ambos servicios de ingesta son **opcionales** (opt-in). Ninguno está en el conjunto predeterminado de
áreas funcionales, así que los habilitas de forma deliberada. Cada uno depende de forma estricta de la
gestión de dispositivos, que resuelve lo que ellos producen.

## Por qué cada uno se ejecuta como una sola instancia {#one-instance-each-and-the-reasons-differ}

Cada componente se ejecuta como una sola instancia por su propia razón; no es una misma política
aplicada tres veces.

| Servicio | Por qué exactamente una | Qué haría una segunda |
|---|---|---|
| **Ingesta de Sparkplug** | Se une a tu broker como **Host Application** de Sparkplug, y un entorno de Sparkplug tiene una sola. | Publicar un STATE de host contradictorio e ingerir cada mensaje dos veces. |
| **Ingesta LwM2M** | DTLS es una **sesión con estado sobre un único socket UDP enlazado**. | Una réplica en espera que también enlazara el socket recibiría —y descartaría— en silencio la parte de los datagramas que le tocara. El tráfico desaparece en lugar de fallar de forma visible. |
| **Agente de borde** | Es dueño de un directorio de almacenamiento local y de una identidad en el enlace ascendente hacia la nube. | Dos sobre un mismo directorio chocan en los bloqueos de archivo; dos que comparten identidad se expulsan mutuamente del enlace en bucle. |

En los dos servicios de ingesta, la plataforma impone esto en lugar de limitarse a documentarlo:

- **Arrendamiento (lease) de propiedad.** Cada servicio toma un arrendamiento con una ventana de 30
  segundos. Un pod de reemplazo no conecta ni enlaza nada hasta que lo tiene.
- **Solapamiento acotado.** La ventana en la que dos pods sirven queda acotada por la ventana del
  arrendamiento más un intervalo de renovación (unos diez segundos). Un líder que ha perdido el
  arrendamiento se autoexpulsa solo cuando su siguiente renovación lo advierte, y aun entonces tiene
  que deshacer su estado de broker o de DTLS. El solapamiento está acotado, no eliminado.
- **Sin vallado de escrituras.** Nada valla las escrituras de un líder obsoleto en estas dos vías. El
  arrendamiento lleva su época, pero ninguna vía de ingesta rechaza en función de ella.
- **Rechazo del chart.** El chart se niega a renderizar cualquiera de las dos áreas con más de una
  réplica. El rechazo está ligado al área en sí, no a su estrategia de despliegue, así que cambiar
  `strategy` a `RollingUpdate` no lo esquiva.

Un pod que se termina *a sí mismo* libera el arrendamiento al salir, de modo que el reemplazo lo
adquiere en cuanto arranca. Un pod se termina a sí mismo cuando concluye que ya no puede servir; en la
ingesta LwM2M eso significa un turno de liderazgo que no pudo construir, o un transporte CoAP/DTLS que
dejó de leer. La espera de 30 segundos solo se aplica tras una pérdida **abrupta**, en la que nada tuvo
ocasión de liberar: un fallo de nodo, un `SIGKILL`, una muerte por falta de memoria.

:::warning Ningún servicio de ingesta recibe un presupuesto de interrupción de pods
El chart omite el presupuesto de interrupción para cualquier área con una sola réplica, porque un
presupuesto que exigiera un pod disponible bloquearía por completo el drenaje de un nodo. Por tanto,
drenar el nodo en el que está un servicio de borde **detiene ese transporte** hasta que el pod se
reprograma y toma el arrendamiento. La recuperación es automática pero no inmediata, así que prefiere
un despliegue deliberado a drenar ese nodo.
:::

## Qué cuesta un relevo

Un reinicio o un cambio de liderazgo es rutina en ambos servicios, y los dos vuelven por sí solos. Lo
que recuperan no es lo mismo.

| | Ingesta de Sparkplug | Ingesta LwM2M |
|---|---|---|
| **Presencia** | **Se reconstruye.** El nuevo líder restablece la sesión, pide a los nodos de borde que se vuelvan a anunciar y reconcilia qué dispositivos están realmente vivos, de modo que no se pierde una desconexión ocurrida durante el relevo. | **Se reconstruye** a partir de la proyección almacenada y del tiempo de vida de registro de cada dispositivo, en lugar de sondear, así que un dispositivo dormido en modo cola no se marca falsamente como fuera de línea. |
| **Telemetría durante la ventana** | Se pierde. Un broker no retiene DATA para un host que no está conectado. | Los datagramas enviados durante la ventana se pierden; los mensajes CoAP confirmables los retransmite el dispositivo. |
| **Recursos observados** | No aplica. | **No se restablecen. Consulta más abajo.** |
| **Tiempo de recuperación** | Lo que el pod de reemplazo necesite para planificarse y arrancar, más hasta los 30 segundos de la ventana del arrendamiento. | Lo mismo, más el enlace del socket. |

:::danger Las observaciones LwM2M se pierden en un relevo y nada las vuelve a crear
Un reinicio, un despliegue o un cambio de liderazgo pierde todas las observaciones LwM2M. La presencia
vuelve; la telemetría no, hasta que cada dispositivo se vuelve a registrar. Con el valor
predeterminado, eso son **hasta un día de silencio de un dispositivo sano** que figura en línea todo
ese tiempo. Consulta [Observaciones perdidas](#lost-observations).
:::

### Observaciones perdidas {#lost-observations}

Este es el hecho operativo más sorprendente de esta página.

DeviceChain le pide a un dispositivo LwM2M que **Observe** sus recursos, para que el dispositivo envíe
las lecturas por su cuenta. Esas observaciones viven únicamente mientras dura el proceso que las
estableció. Un reinicio, un despliegue o un cambio de liderazgo **las pierde todas, y nada las vuelve
a crear.**

La presencia vuelve. La telemetría no. Un dispositivo vuelve a reportar solo cuando se **vuelve a
registrar**. Eso es comportamiento propio del dispositivo, en su propio calendario, sin más límite que
su tiempo de vida de registro. Con el valor predeterminado de `86400` segundos, eso son **hasta un día
de silencio de un dispositivo perfectamente sano**, con el dispositivo en línea en la consola todo ese
tiempo.

Si no puedes tolerarlo, baja `maxLifetimeSeconds` (consulta [Ajustes de LwM2M](#lwm2m-settings)).
Nunca lo bajes por debajo del mayor tiempo de vida que pidan realmente tus dispositivos, o quedarán
expirados como muertos en cada relevo.

## Qué garantiza la presencia

Todo dispositivo lleva una **fuente de presencia**, `INFERRED` o `ASSERTED`. Las reglas que la rigen
son más estrictas de lo que parecen.

**Solo la fuente puede devolver un dispositivo.** Un dispositivo pasa a afirmado la primera vez que un
transporte autoritativo habla por él. Ningún tiempo de espera, ningún evento de datos y ninguna
cantidad de silencio lo devuelven a inferido. Lo único que lo hace es una **degradación**: una
afirmación de la *fuente* de que ya no habla por el dispositivo, nunca una afirmación sobre el
dispositivo en sí. Consulta [Devolver un dispositivo a presencia inferida](#demoting-a-device). Un
dispositivo que antes llegaba por Sparkplug o LwM2M y ahora llega por MQTT simple conserva su fuente
afirmada, y sigue exento del barrido de inactividad.

**El orden lo fija una identidad de sesión generada por la plataforma, nunca nada que envíe el
dispositivo.** La plataforma sella cada par de conexión/desconexión con un marcador de sesión que
genera ella misma. El número de secuencia de nacimiento/muerte de Sparkplug se lee solo para emparejar
una muerte con el nacimiento al que pertenece. Nunca se compara por magnitud, porque desborda y vuelve
a empezar. El identificador de registro de LwM2M nunca se usa como identidad de sesión. Como
resultado, un mensaje retrasado o repetido de una sesión anterior no puede derribar una sesión viva,
en ninguno de los dos transportes.

Los marcadores se generan con el reloj del nodo del broker que aceptó la conexión. En un clúster de
varios nodos, por tanto, **no** está garantizado que el marcador de una sesión nueva quede por encima
del anterior: un nodo cuyo reloj va por detrás de sus pares genera uno menor. La plataforma concilia
ese caso en lugar de darlo por imposible. Un dispositivo que se encuentra activo en una sesión que
queda por debajo de la almacenada se vuelve a archivar en la sesión en la que realmente está, de modo
que su desconexión posterior se sigue reconociendo.

**Un dispositivo no puede afirmar su propia presencia.** Un evento de conexión/desconexión enviado por
la vía habitual de payload orientada al dispositivo se rechaza de plano, no simplemente se ignora.
Solo los propios transportes los producen. Un dispositivo afirmado está exento del barrido de
inactividad, así que un dispositivo capaz de declararse conectado podría fijarse en línea de forma
permanente.

**El tiempo de espera inferido es de diez minutos y no es ajustable.** Los dispositivos sin un
transporte que afirme presencia se marcan fuera de línea tras diez minutos de silencio, con una
revisión cada minuto. Hoy no hay anulación por dispositivo ni ajuste alguno para ello. Es además el
tiempo de espera bajo el que vuelve a quedar un dispositivo degradado, que es buena parte del sentido
de degradarlo.

## Dispositivos atascados en línea {#a-device-that-reads-online-and-is-not}

Entiende este modo de fallo antes de apoyar en la presencia cualquier cosa que despierte a una
persona.

**Un dispositivo afirmado que muere sin decirlo puede figurar en línea indefinidamente.** El barrido
de inactividad omite deliberadamente a los dispositivos afirmados, porque en un transporte que afirma
presencia el silencio no es prueba de muerte. En estos dos transportes **no hay nada más que tenga un
tiempo de espera, un watchdog ni un barredor.** Dos cosas lo limpian, y ninguna es un tiempo de
espera:

1. **Una señal nueva del propio transporte del dispositivo.** Los dispositivos afirmados por el broker
   MQTT propio de DeviceChain obtienen una sin que el dispositivo haga nada: allí una pasada de
   reparación compara periódicamente la lista de conexiones vivas del broker con lo que cree la
   plataforma, y corrige la diferencia. Consulta [Presencia de dispositivo](../concepts/device-presence.md).
   Sparkplug y LwM2M no tienen tal pasada, así que la señal tiene que venir del dispositivo.
2. **Una [degradación](#demoting-a-device).** Es la respuesta cuando la primera no va a llegar nunca,
   porque la fuente que tendría que producirla ya no está. Funciona en los tres transportes que
   afirman presencia.

Las formas concretas en que un dispositivo se queda atascado en línea:

- **Un certificado de muerte de Sparkplug perdido.** Si el broker nunca entrega el DEATH del nodo, el
  dispositivo sigue en línea hasta la siguiente reconciliación. La reconciliación se ejecuta **solo
  cuando el host se reconecta al broker**. Un host que permanece conectado de forma estable y nunca
  vuelve a saber de ese nodo no la vuelve a ejecutar jamás.
- **Un nodo que se vuelve a anunciar.** Cuando un nodo de borde inicia una sesión nueva, los
  dispositivos hijos de su sesión anterior se reemplazan junto con ella. Un dispositivo hijo que no se
  vuelva a anunciar bajo la nueva sesión sigue mostrándose conectado, sin nada que lo corrija hasta la
  siguiente reconciliación.
- **Un registro LwM2M largo.** Un dispositivo que desaparece se marca fuera de línea cuando caduca su
  tiempo de vida de registro. Con el valor predeterminado, eso son **86400 segundos**, un día entero.
- **Un transporte afirmante eliminado.** Da de baja la fuente de Sparkplug o la credencial LwM2M por
  la que llegaba un dispositivo, y nada volverá a producir una señal para él. Queda varado en su último
  estado afirmado hasta que alguien lo [libere](#demoting-a-device). Para eso existe la operación, ya
  que una fuente que ya no está no le va a contar nada más a la plataforma.

**La única palanca en estos dos transportes es `maxLifetimeSeconds`, y solo se aplica a LwM2M.** El
tiempo de vida de cada registro se recorta hasta ese valor como máximo, de modo que acota directamente
cuánto tiempo puede figurar en línea un dispositivo LwM2M muerto. Ponerlo en, digamos, 3600 lo limita
a una hora. Debe quedar por encima del mayor tiempo de vida que pida realmente tu flota. La vía MQTT
tiene su propia cota: el intervalo de la pasada de reparación, `brokerPresence.reconcileSeconds`,
cinco minutos de forma predeterminada.

La vía de Sparkplug no tiene palanca equivalente. Si que un dispositivo Sparkplug figure en línea
tiene peso operativo para ti, combina la señal de conectividad con una
[regla de ausencia](../concepts/event-processing.md) basada en tiempo de espera, que se dispara con el
silencio independientemente de lo que diga la presencia.

### Devolver un dispositivo a presencia inferida {#demoting-a-device}

Una **degradación** es la única transición de `ASSERTED` de vuelta a `INFERRED`. Es una afirmación de
la fuente, no sobre el dispositivo: la fuente renuncia a su custodia. No afirma nada sobre la
conectividad. Si el dispositivo figura en línea, cuándo se conectó por última vez, cuándo se
desconectó por última vez y cuándo reportó por última vez quedan exactamente como estaban.

Lo que cambia es quién puede corregir esos valores. Un dispositivo afirmado suprime los dos mecanismos
de reparación de la plataforma. Liberarlo se lo devuelve a ambos, lo que repara los dos sentidos de la
congelación a la vez:

- **Congelado en línea.** El dispositivo vuelve a ser visible para el barrido de inactividad, y se
  marca fuera de línea diez minutos después de su última actividad real.
- **Congelado fuera de línea.** El dispositivo deja de tener sus comandos retenidos. La retención
  depende de que el dispositivo esté afirmado *y* no activo, así que a un dispositivo inferido se le
  despacha. La pasada periódica que revisa el conjunto retenido libera la acumulación en un par de
  minutos, y el dispositivo vuelve a figurar en línea con su siguiente lectura. Arregla este sentido
  pronto: los comandos retenidos cuentan contra un
  [techo por inquilino](../concepts/commands.md#held-command-ceiling), así que los dispositivos
  atascados fuera de línea por una fuente que se fue pueden acabar rechazando encolados de los
  dispositivos sanos que tienen al lado.

Una degradación ocurre de una de dos formas: una fuente libera sus propios dispositivos, o un operador
los libera a mano.

#### Liberación automática cuando se apaga una fuente {#a-source-releases-its-own-devices-when-it-is-switched-off}

La presencia MQTT afirmada por el broker depende de una *toma de presencia*: una conexión que
`event-sources` establece con la cuenta de sistema del broker para recibir sus avisos de conexión y
desconexión. Cuando la toma se niega a arrancar por una de estas razones, `event-sources` recorre los
dispositivos que aún tiene afirmados y los libera:

- se **desactivó deliberadamente**;
- **falta la credencial de la cuenta de sistema de NATS**;
- **no se puede alcanzar el broker**.

Son tres de las seis razones por las que la toma puede no arrancar, y la línea es deliberada. Las dos
primeras son configuración. Todas las réplicas de la instancia leen los mismos valores y llegan a la
misma conclusión, así que la liberación es la instancia hablando, no una réplica adivinando.

La tercera es evidencia de otro tipo. La toma da treinta segundos a su conexión para alcanzar el
broker antes de darlo por inalcanzable, así que esta razón significa medio minuto sin conexión con la
cuenta de sistema, no un intento fallido: un broker caído, o una credencial que rechaza. La pasarela
MQTT vive en ese mismo broker, así que mientras esté inalcanzable tampoco hay ningún dispositivo
conectado por ella. La liberación no es una suposición sobre la flota; es la única lectura compatible
con que el broker no esté.

Es además la única de las tres cuya verdad puede cambiar con el pod en marcha, así que es la única que
sigue preguntando. Cada pasada de liberación vuelve a conectar primero con la cuenta de sistema, la
primera pasada incluida. **Si el broker responde, no se libera nada**: el servicio termina y el pod se
reinicia. El reemplazo conecta con el broker con normalidad y ejecuta su toma. Un broker que vuelve
aparece por tanto como un reinicio de pod, no como una flota de dispositivos liberados. Sin esa
comprobación, la liberación continuaría: la pasada recorre lo que esté afirmado *ahora* según el
intervalo de reconciliación. Con pares que siguen afirmando, las dos se turnarían sobre cada fila
indefinidamente, y en el hueco entre ambas el barrido de inactividad marcaría fuera de línea a los
dispositivos conectados pero silenciosos.

Estas **no** liberan:

- una suscripción que falla sobre una conexión que sí alcanzó el broker. Es mala suerte de esa réplica
  en concreto; sus pares pueden estar leyendo los avisos perfectamente.
- las dos razones que significan que esta instancia no tiene ninguna toma que ejecutar: ninguna fuente
  apuntando al broker de la plataforma, y ninguna configuración de llamadas entre servicios.

Las seis razones ponen `presence_tap_off{reason}` de todos modos, que es cómo sabes cuál tienes.

Con la toma ya en marcha, una conexión que el broker cierra definitivamente es motivo para reiniciar,
no para apagar la toma. Si el broker deja de aceptar la credencial de la cuenta de sistema, todos los
pods de `event-sources` fallan su comprobación de actividad (liveness) más o menos a la vez y
Kubernetes los reinicia. La ingesta HTTP no está disponible mientras se reinician, y la telemetría MQTT
espera en el broker. Cada pod reiniciado conecta con su credencial montada. Si el broker la sigue
rechazando, la toma se apaga con el motivo `broker_unreachable` y se aplica la liberación descrita
arriba.

Conoce tres propiedades de la liberación automática antes de depender de ella:

- **Una credencial ausente y un broker inalcanzable esperan dos minutos primero.** Un arranque inicial
  genera esa credencial y renueva el broker en la misma ejecución que pone en marcha los servicios,
  así que cualquiera de los dos puede ser una carrera con esa ejecución y no una condición permanente.
  Un `enabled: false` escrito es inequívoco, y actúa de inmediato. Para el broker, la espera es además
  una **comprobación** (consulta más arriba). Para la credencial no puede serlo, porque la
  configuración se lee una sola vez al arrancar y un cambio renueva el pod.
- **Necesita una fuente de pasarela y configuración de llamadas entre servicios propias**: algo bajo
  lo que emitir, y una manera de enumerar inquilinos y leer la proyección. Sin ellas no se ejecuta en
  absoluto. Registra que no lo hizo y apunta a la liberación manual, que entonces es la única vía.
- **Va a ritmo controlado y se vacía sola.** Las liberaciones salen a 25 dispositivos por segundo, y un
  dispositivo liberado abandona el conjunto que se recorre, así que una pasada interrumpida se reanuda
  sin coste en lugar de empezar de nuevo. `presence_still_asserted` es cuánto queda; una liberación
  sana lo lleva a cero y lo deja ahí.

Nada libera automáticamente los dispositivos de Sparkplug ni de LwM2M. Esas fuentes desaparecen porque
un operador las retiró, no porque cambiara una bandera, así que es un operador quien los libera.

#### Liberación manual {#an-operator-releases-them-by-hand}

`dcctl presence demote` recorre los dispositivos afirmados de una fuente en un inquilino y libera cada
uno:

```
dcctl presence demote --tenant acme --source sparkplug:plant-a \
  --email ops@acme.example --password "$DC_PASSWORD" \
  --reason "plant-a gateway decommissioned"
```

| Opción | |
|---|---|
| `--tenant` | Obligatoria. Una degradación actúa sobre un inquilino. |
| `--email` / `--password` | Obligatorias. La identidad con la que se autoriza la degradación. Debe ser miembro del inquilino, o un superusuario. |
| `--server` | El host de la instancia para las llamadas a la API, `localhost` de forma predeterminada. Añade `--tls` para HTTPS. |
| `--source` | Obligatoria, y nunca se infiere: el radio de acción es una fuente de eventos entera. Pásala exactamente como la reporta el estado del dispositivo: el identificador configurado de la propia fuente para MQTT y HTTP (`mqtt1`, `http1`), `sparkplug:{hostId}` para Sparkplug, `lwm2m` para LwM2M. |
| `--device` | Repetible. Acota a dispositivos concretos *dentro* de la fuente; omítela para liberar la fuente entera. |
| `--reason` | Obligatoria. Se registra con cada evento que emite la ejecución: el único rastro de una escritura de presencia sobre toda una flota. |
| `--page` | Dispositivos por llamada, `200` de forma predeterminada. |
| `--dry-run` | Informa de lo que se liberaría, y no libera nada. |
| `--yes` | Omite la confirmación que pide una ejecución real. Sin un terminal en el que confirmar, una ejecución real se rechaza salvo que la pases. |

Una fuente que nadie usa no es un error; sencillamente no coincide con nada. Así que una primera
página que no coincide con ningún dispositivo es mucho más probable que sea un `--source` mal escrito
que un trabajo terminado, y el comando lo dice en lugar de informar de éxito.

La misma operación está disponible en la API como la mutación `demoteAssertedPresence` de
`device-state`. Requiere el permiso `state:demote`. Ese permiso es de escritura, y no forma parte de la
base de solo lectura que recibe todo miembro. El rol `tenant-admin` que se crea de serie tiene todos los
permisos del inquilino, así que incluye este; concédelo explícitamente a cualquier otro rol que lo
necesite.

#### Contabilización de las liberaciones {#a-release-is-metered-like-any-other-presence-event}

Una liberación pasa por el mismo [techo de ingesta](../concepts/governance.md) por inquilino que una
conexión o una desconexión. A un inquilino que ya está en su techo se le puede rechazar la
*reparación* junto con la rotación que causa la presión, contada en `presence_events_refused_total`. No
se pierde nada: una liberación rechazada deja el dispositivo afirmado, así que la siguiente pasada lo
vuelve a encontrar. La reparación llega no antes de lo que permita el techo.

## Pertenencia a inquilino en ambos transportes

**La pertenencia a inquilino la fija la conexión en ambos transportes, y nunca se lee de contenido
suministrado por el dispositivo.** Es la propiedad más fuerte de esta parte de la plataforma.

- **Sparkplug.** Todo mensaje se atribuye al inquilino configurado para la **conexión de broker por la
  que llegó**. El Group ID de Sparkplug que va en el topic es una etiqueta propia del cliente, no es
  globalmente única y cualquier publicador puede fijarla, así que nunca nombra a un inquilino. La
  configuración rechaza dos inquilinos sobre un mismo endpoint de broker, porque entonces el Group ID
  sería lo único que los separaría.
- **LwM2M.** Cada dispositivo queda ligado a su inquilino por la **identidad de clave precompartida
  (PSK) DTLS autenticada** que presentó en el handshake. El nombre de endpoint que el dispositivo
  afirma en su propio payload de registro nunca se usa para identificarlo. Una identidad no
  aprovisionada falla el handshake, y el rechazo no devuelve la identidad como eco, de modo que un
  sondeo no puede enumerar credenciales válidas comparando respuestas de error.

:::caution En Sparkplug, la autenticación del dispositivo es a nivel de broker
Con `deviceAuthMode: required`, se confía en ambos transportes para la identidad del dispositivo sin
una segunda credencial por evento. En LwM2M esa identidad está ligada a la PSK, así que es por
dispositivo. En Sparkplug sale del topic, así que la autenticación de dispositivo obligatoria *no*
impide que un publicador envíe como otro dispositivo **dentro del mismo inquilino**. Consulta
[Autenticación de dispositivos en Sparkplug](#device-authentication-on-sparkplug).
:::

### Autenticación de dispositivos en Sparkplug {#device-authentication-on-sparkplug}

Ambos transportes marcan su tráfico como autenticado por el transporte. Eso es lo que permite a la
plataforma confiar en una identidad de dispositivo con `deviceAuthMode: required` sin una segunda
credencial por evento. En LwM2M esa identidad está ligada a la PSK autenticada, así que es realmente
por dispositivo.

En Sparkplug la identidad se deriva del topic, así que la autenticación es tan fina como **la conexión
de broker** y no más. Activar la autenticación de dispositivo obligatoria *no* impide que un
publicador en el broker de un inquilino envíe bajo la identidad de otro dispositivo dentro de ese
mismo inquilino. El cruce entre inquilinos está cerrado en ambas vías: un publicador nunca puede
alcanzar a otro inquilino. Si la identidad de dispositivo dentro del inquilino te importa, impónla
con credenciales por cliente y permisos de topic **en tu propio broker**, que es donde vive realmente
esa frontera.

### Identificadores de dispositivo en los transportes de borde {#device-identifiers}

:::danger Ninguno de los tres identificadores es el que escribiste
Todo dispositivo en un transporte de borde lleva tres identificadores con tres funciones distintas, y
los fallos por confundirlos son silenciosos o engañosos. Un dispositivo aprovisionado automáticamente
llega **sin nombre**, y la consola no puede buscarlo. Lee esta sección antes de aprovisionar.
:::

Dos de los identificadores parecen nombres y el tercero es generado, por eso es fácil confundirlos.

| Identificador | Su función | Sparkplug | LwM2M |
|---|---|---|---|
| **1. Ancla de pertenencia a inquilino** | Decide a qué inquilino pertenecen los datos. Nunca nada que venga en el mensaje; tratado más arriba. | La **conexión de broker**. | La **identidad PSK autenticada**. |
| **2. Clave de resolución del dispositivo** | Decide *qué dispositivo*. Es el **id externo** del dispositivo, y el dispositivo no lo elige. | La cadena `group/node[/device]` del topic, p. ej. `plant-a/line-3/press-1`. | El id externo que escribiste **junto a la identidad PSK en la configuración del servicio**, no el nombre de endpoint (`ep`) que envía el dispositivo. |
| **3. Token del dispositivo** | Lo que la consola, la API y todos los eventos usan realmente. Es **generado**, no elegido. | `sp-…`, p. ej. `sp-plant-a-line-3-press-1-9f2c1a8b4d3e`. | `lw-…`. |

En LwM2M, `ep` se registra en el log y por lo demás se ignora. Un dispositivo cuyo firmware envía
`ep=urn:imei:35…` nunca será emparejado por él, y nada te lo indicará.

El token es generado porque el id externo contiene con frecuencia `/`, `.`, espacios o caracteres no
ASCII, nada de lo cual puede contener un token. Así, `plant-a/line-3/press-1` se convierte en algo
como `sp-plant-a-line-3-press-1-9f2c1a8b4d3e`. El sufijo desambigua dos ids externos que de otro modo
se reducirían a la misma cadena.

La consecuencia práctica es peor de lo que parece: un dispositivo aprovisionado automáticamente llega
**sin nombre alguno**. El registro lleva solo el token, el id externo y el tipo de dispositivo. Por
eso la lista de dispositivos de la consola lo muestra bajo su token generado `sp-…` / `lw-…`, con un
`—` en la columna Nombre. No hay nada ahí por lo que reconocerlo.

Tampoco puedes buscarlo. La lista de dispositivos de la consola **no tiene caja de búsqueda**. Es un
listado paginado sin más de Estado, Token, Nombre, Tipo, Descripción y Creado, sin columna de id
externo. La búsqueda de dispositivos de la API solo admite número de página, tamaño de página y tipo
de dispositivo. Para encontrar el dispositivo:

- **En la consola**, localízalo por su token. El token generado incrusta el id externo
  (`plant-a/line-3/press-1` → `sp-plant-a-line-3-press-1-…`), así que paginar la lista y leer la
  columna del token es toda la técnica.
- **Por la API**, usa `devicesByExternalId`, que recibe ids externos exactos y devuelve los
  dispositivos. Es una consulta de coincidencia exacta, no una búsqueda: sin prefijos ni subcadenas.
  Nada en la consola la invoca, así que esta vía es solo de API.

Cuando un dispositivo no aparezca en absoluto, revisa el identificador 2 antes de sospechar del
transporte. En LwM2M en particular, una identidad PSK equivocada falla en el handshake DTLS, antes del
registro, y el rechazo deliberadamente no te dice nada.

## Operación de LwM2M {#lwm2m-what-an-operator-must-know}

**Solo se decodifica telemetría en SenML-JSON.** Las notificaciones en cualquier otro formato de
contenido se cuentan y se descartan. La consecuencia práctica no es evidente a partir del estándar:

:::warning Un cliente conforme solo con LwM2M 1.0 obtiene presencia y comandos, pero ninguna telemetría
SenML llegó con LwM2M 1.1. Un dispositivo que solo habla 1.0 se registra, mantiene su sesión, impulsa
la presencia y acepta comandos Read/Write/Execute, y **nunca produce una sola medición**. Nada falla de
forma visible. Revisa **`observe_establish_refused_total`**, no
`notify_unknown_content_format_total`. Consulta [Clientes solo LwM2M 1.0](#lwm2m-10-only-clients).
:::

### Clientes solo LwM2M 1.0 {#lwm2m-10-only-clients}

Un dispositivo que solo habla LwM2M 1.0 se registrará, mantendrá su sesión, impulsará la presencia
correctamente y aceptará comandos Read/Write/Execute, y nunca producirá una sola medición. Nada falla
de forma visible; las lecturas sencillamente no aparecen nunca.

La métrica que hay que revisar es **`observe_establish_refused_total`**. DeviceChain pide SenML-JSON
en el propio Observe, así que un cliente conforme solo con 1.0 rechaza el Observe con
`4.06 Not Acceptable` y luego no envía notificación alguna. Ese rechazo se cuenta aquí, y es la causa
dominante de este contador. `notify_unknown_content_format_total` se queda en **cero** para ese
dispositivo, porque cuenta el *otro* caso: un dispositivo que sí notifica, en un formato de contenido
que este adaptador no puede decodificar.

### Límites de las observaciones {#observation-limits}

**Las observaciones están acotadas, y las cotas no son configurables.** DeviceChain establece una
observación por *instancia* de objeto, solo para objetos dentro de un rango IPSO fijo, y como máximo
**32 por registro**. La lista de objetos permitidos es una **propiedad fija de la compilación; ningún
ajuste añade nada a ella.** Si tu flota reporta un recurso fuera de ese rango, ese recurso no se
observará y ninguna configuración lo cambiará. Vigila `observation_overflow_total` para detectar
dispositivos que superen el tope por registro.

### Reciclado de sesiones {#session-reaping}

**Las sesiones no se reciclan de forma predeterminada.** `idleTimeoutSeconds` es `0` de forma
predeterminada, es decir, nunca, que es lo correcto para dispositivos siempre conectados. En una flota
en **modo cola**, fíjalo cómodamente por encima del intervalo de despertar esperado. Si es demasiado
bajo, las claves de sesión de un dispositivo dormido se desalojan sin que lo sepa, lo que fuerza el
rehandshake completo que el Connection ID de DTLS existe para evitar.

### Exponer el puerto LwM2M {#exposing-the-lwm2m-port}

:::danger Nada expone el puerto LwM2M fuera del clúster
El puerto CoAP/DTLS orientado al dispositivo es **UDP 5684**, y **ni el chart ni los módulos de
infraestructura lo exponen más allá del clúster.** Tal como se instala, una flota LwM2M real no puede
alcanzar el servicio. Tienes que proporcionar la vía UDP por tu cuenta.
:::

Todos los servicios son internos al clúster, no hay afinidad de sesión configurada en ninguna parte, y
el controlador de ingress que se distribuye maneja solo HTTP.

La exposición externa es explícitamente una decisión del operador, y **no se distribuye ninguna
implementación de ella**. Proporciona la vía UDP por tu cuenta (un servicio `LoadBalancer` o
`NodePort`, o un proxy UDP externo). Debe ser una vía que mantenga todos los datagramas de una sesión
yendo al único pod que sirve.

### Ajustes de LwM2M {#lwm2m-settings}

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `listen.port` | `5684` | El puerto UDP que enlaza el servidor CoAP/DTLS. |
| `security.connectionIdLength` | `8` | Longitud en bytes del Connection ID de DTLS. **Mantenlo distinto de cero** en flotas celulares o itinerantes: es lo que permite que una sesión sobreviva a un cambio de dirección. `0` lo desactiva y fuerza un rehandshake en cada reasignación. |
| `security.idleTimeoutSeconds` | `0` | Recicla una sesión sin tráfico tras ese tiempo. `0` no recicla nunca. Fíjalo por encima del intervalo de despertar en una flota en modo cola. |
| `security.handshakeTimeoutSeconds` | `10` | Acota un handshake DTLS, para que uno atascado no pueda inmovilizar recursos. |
| `security.maxSessions` | `100000` | Techo de la tabla de sesiones vivas. Un handshake por encima del techo se rechaza y se cuenta, nunca se admite en silencio. |
| `maxLifetimeSeconds` | `86400` | El techo hasta el que se recorta el tiempo de vida de todo registro. **Es la palanca que acota cuánto tiempo figura en línea un dispositivo muerto.** Debe quedar por encima del mayor tiempo de vida que pidan tus dispositivos. |
| `ingestRateLimit.messagesPerSecond` | `1000` | Techo de ingesta sostenida por inquilino. Sin definir o con un valor no positivo, recae en este predeterminado, nunca en ilimitado. |
| `ingestRateLimit.burst` | `2000` | Margen de ráfaga para lo anterior. |
| `downlink.timeoutSeconds` | `10` | Acota un intercambio de comando con un dispositivo. Al expirar, el comando se reporta como fallido en lugar de quedar colgado. Súbelo para dispositivos celulares lentos que duermen. |
| `downlink.concurrency` | `16` | Paralelismo de comandos entre dispositivos. Los comandos de un mismo dispositivo se ejecutan siempre en orden, sea cual sea este valor. |

## Operación de Sparkplug {#sparkplug-what-an-operator-must-know}

**Cada fuente es una conexión saliente independiente.** Una fuente nombra un broker, un inquilino y
los grupos a los que suscribirse. Un broker inalcanzable se reintenta en su propio bucle con espera
creciente. Degrada **esa única fuente**, no el pod ni la fuente de ningún otro inquilino. Para esto,
vigila `connect_failures_total` en lugar de la salud del pod.

**Un solo grupo rechazado detiene toda esa fuente hasta que se corrija la ACL del broker.** Una fuente
anuncia un único estado en línea/fuera de línea para todos sus grupos, así que no puede estar en línea
para unos y fuera de línea para otros. El broker puede aceptar la conexión pero rechazar la
suscripción a un grupo, lo más habitual porque la credencial de la fuente no puede leerlo. Entonces la
fuente:

1. no se anuncia en línea;
2. no ingiere ninguno de sus grupos;
3. se desconecta, y reintenta en el mismo bucle con espera creciente, hasta 30 segundos entre intentos.

Anunciarse en línea con un grupo ausente sería peor. Los nodos de borde de ese grupo volcarían sus
datos almacenados en una suscripción que no existe, y la fuente marcaría después sus dispositivos como
desconectados por quedarse en silencio. Antes de desconectarse, la fuente publica ella misma su estado
fuera de línea, porque una desconexión limpia no activa su Last Will. Un anuncio en línea que el broker
guardó pero nunca confirmó se reemplaza, por tanto, en lugar de quedarse vigente. Vigila
**`subscribe_failures_total`**: cualquier aumento significa que una fuente está caída, y la línea de
log nombra el grupo rechazado.

**La reconexión la gestiona la plataforma, no la biblioteca cliente de MQTT.** Cada reconexión abre
una sesión realmente nueva con una marca de tiempo nueva. Sparkplug exige que el nacimiento del host y
su certificado de muerte lleven la misma marca de tiempo, para que un nodo de borde pueda rechazar una
muerte retrasada de una sesión anterior. Es también la razón por la que todas las réplicas comparten
un mismo client id: lo que expulsa a un host zombi es el propio desalojo por client id duplicado del
broker.

:::caution En la vía de Sparkplug no se acota ni la tasa de mensajes ni el tamaño de un mensaje
La ingesta de Sparkplug **no aplica ningún techo de ingesta por inquilino y no descarta nada**, ni
tiene techo de lecturas por mensaje. Un nodo de borde desbocado en un broker configurado no se limita
en la puerta. Acótalo en el broker, mediante los grupos a los que te suscribes, y mediante el número
de métricas por publicación en el nodo de borde. Consulta
[Ingesta de Sparkplug sin límite](#unbounded-sparkplug-ingest).
:::

### Ingesta de Sparkplug sin límite {#unbounded-sparkplug-ingest}

A diferencia de LwM2M y de las vías de ingesta de dispositivo habituales, la ingesta de Sparkplug **no
aplica ningún techo de ingesta por inquilino y no descarta nada**. El razonamiento es que su
exposición es un broker al que elegiste conectarte deliberadamente, y no un endpoint abierto. La
consecuencia es tuya: un nodo de borde desbocado en un broker configurado no se limita en la puerta.
Acótalo en el broker, o mediante los grupos a los que te suscribes.

**Son dos límites distintos, y aquí no se aplica ninguno.** El límite de tasa anterior mide
*mensajes*. El [techo de lecturas por mensaje](../guides/connecting-a-device.md#how-much-one-message-may-carry)
acota lo que un mensaje puede costar una vez admitido. Un DDATA de Sparkplug que lleve miles de
métricas es un solo mensaje, y se convierte en una lectura almacenada por métrica: cada una, su propia
fila, actualización de estado y evaluación de reglas en el motor de detección que comparten todos los
inquilinos. Acota el número de métricas por publicación en el nodo de borde, del mismo modo y por la
misma razón por la que acotas su tasa.

La [puerta del ciclo de vida del inquilino](./tenant-deletion.md) sigue aplicándose. El tráfico de un
inquilino en eliminación se rechaza en esta vía como en cualquier otra, y se cuenta en
`tenant_deleted_dropped_total`.

### Identidades desconocidas {#unknown-identities}

**Las identidades desconocidas son una elección.** Con el registro automático activado, una identidad
de Sparkplug sin dispositivo correspondiente crea uno. Con él desactivado, su telemetría se descarta y
se cuenta en `unknown_device_dropped_total`. Revisa esa métrica cuando un nodo de borde esté
publicando y no aparezca nada.

## El agente de borde

El agente de borde **no es una cuarta vía de ingesta**. Se ejecuta en una planta, presenta un endpoint
MQTT corriente a los dispositivos locales, almacena en disco local lo que publican y lo vuelve a
publicar sobre los mismos topics de dispositivo que la nube ya ingiere. Nada del lado de la plataforma
sabe que hubo un agente de por medio, y por eso no hay configuración específica del agente en ningún
servicio de la nube.

**No es un área funcional del chart.** No aparece en ninguna lista de áreas ni en ningún perfil de
despliegue, y no se puede habilitar como se habilita un servicio. Se distribuye como binarios
estáticos y una imagen de contenedor, y lo despliegas tú: una unidad de systemd en una pasarela de
planta, un contenedor o un manifiesto de Kubernetes escrito a mano en el borde.

### El almacén local {#the-spool}

**El almacén local es un anillo que descarta lo más antiguo.** Es un búfer durable en disco, de
`1 GiB` de forma predeterminada. Cuando se llena, descarta los eventos sin reenviar **más antiguos**
para admitir los nuevos, nunca los más recientes.

Esa dirección es deliberada. A un dispositivo se le acusa recibo en el momento en que publica, desde
la propia persistencia del agente. Descartar lo más nuevo tiraría justo aquello que el agente acaba de
prometer conservar, y te dejaría un búfer obsoleto al final de una interrupción en lugar de uno
actual.

Cada descarte se cuenta, como la primera secuencia del propio almacén menos el número de eventos que
este agente ha reenviado y confirmado. El segundo operando es contabilidad de entrega, y está
*persistido*, que es lo que permite que el conteo sobreviva a un reinicio en lugar de volver a cero.

Hay un caso que no queda cubierto. Cuando ese conteo persistido falta —un primer arranque, o un
almacén cuyo archivo de progreso se eliminó— se inicializa a partir de la primera secuencia actual del
almacén. Todo lo ya desalojado se da entonces por contabilizado, y un reinicio en ese estado sí borra
la evidencia. Mantén intacto el directorio del almacén entre reinicios si el conteo de descartes te
importa.

:::caution El colapso de duplicados al reconectar solo cubre payloads JSON
Cuando el enlace ascendente vuelve, el agente reenvía todo lo que había almacenado. Para los
**payloads de objeto JSON** estampa una identidad y un tiempo de evento estables ante reenvíos, de modo
que un mensaje ya entregado se funde con el existente en la comprobación de unicidad de la nube y lo
ves una sola vez.

**Cualquier otra forma de payload se reenvía tal cual y es «al menos una vez»**, y una reconexión tras
un enlace inestable puede entregarlo dos veces. Si usas un decodificador que no sea JSON detrás de un
agente de borde, haz que tu tratamiento de las lecturas tolere una repetición.
:::

### Antes de desplegar un agente {#before-you-deploy-an-agent}

- **El listener MQTT local está abierto salvo que configures una credencial.** Define `local.username`
  y `local.passwordEnv` para exigirla. Dejarlo abierto es una postura válida de LAN de confianza, y el
  agente lo anuncia con una advertencia bien visible al arrancar para que la elección siga siendo
  visible. En cualquier caso es un control de acceso de red, no una identidad por dispositivo, y sobre
  MQTT en texto plano el secreto cruza la LAN en claro.
- **El endpoint de métricas y salud se enlaza solo a loopback.** Por diseño, el puerto MQTT de
  dispositivo es la única superficie del agente expuesta a la LAN. Para hacer scraping del agente desde
  otro sitio, necesitas algo en el propio equipo que lo retransmita.

### Ajustes del agente de borde

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `instanceId` | — | Obligatorio. La instancia de nube hacia la que reenvía este agente. Las publicaciones vistas para otra instancia no se reenvían, y se cuentan en `instance_mismatched_total`. |
| `agentId` | — | Obligatorio. La identidad de este agente en el enlace ascendente. **Debe ser única**: dos agentes que la comparten se desconectan mutuamente en bucle. |
| `local.listenPort` | `1883` | El puerto MQTT al que se conectan los dispositivos de la planta. |
| `local.storeDir` | — | Obligatorio. El directorio que contiene el almacén durable. Un agente por directorio. |
| `local.spoolMaxBytes` | `1 GiB` | Presupuesto del almacén. Más allá, se descartan los eventos más antiguos. El mínimo es 16 MiB. |
| `local.metricsPort` | `9090` | Puerto de métricas y salud, solo en loopback. Un `0` explícito desactiva el endpoint. |
| `uplink.brokerUrl` | — | Obligatorio. El endpoint MQTT de la nube al que reenviar. |
| `uplink.connectTimeoutSeconds` | `30` | Acota un intento de conexión del enlace ascendente. |
| `uplink.backoffMinSeconds` / `uplink.backoffMaxSeconds` | `1` / `60` | Cotas de la espera creciente de reconexión mientras el enlace está caído. |

## Qué vigilar

:::danger No se distribuye ninguna alerta ni ningún panel para nada de esto
Las reglas de alerta y los paneles de Grafana que se distribuyen cubren otras partes de la plataforma,
como la detección, la entrega de comandos, la mensajería, las bases de datos y la replicación.
**Ninguna métrica de las tablas de abajo tiene alerta ni panel.** Todo lo de abajo se emite y se
recoge, y nada te avisará de ello hasta que escribas la regla tú mismo. Empieza por la
[alerta de «sin líder»](#no-leader-alert).
:::

Todas las métricas llevan el prefijo `devicechain_` y el segmento de su propio servicio:
`devicechain_sparkplugingest_`, `devicechain_lwm2mingest_`, `devicechain_edge_`. Ninguna está
etiquetada por dispositivo ni por inquilino, así que ninguna supone un riesgo de cardinalidad al
recogerla.

### La alerta de «sin líder» {#no-leader-alert}

**La primera alerta que hay que escribir es una alerta de «sin líder»**, en cada servicio de ingesta:

> `sum(devicechain_lwm2mingest_is_leader) != 1 or absent(devicechain_lwm2mingest_is_leader)`
>
> `sum(devicechain_sparkplugingest_is_leader) != 1 or absent(devicechain_sparkplugingest_is_leader)`

Cero significa que nadie está sirviendo ese transporte, y que todos los dispositivos que dependen de él
son inalcanzables en silencio. Cualquier valor distinto de uno merece despertar a alguien. Es la señal
con más peso de toda esta superficie, y hoy nada te informa de ella.

La mitad con `absent()` hace falta, pero no por un pod sin fuentes. Ambos servicios registran su
indicador `is_leader` de forma incondicional al inicializarse, antes de comprobar si tienen algo que
servir. Un pod de Sparkplug que se ejecute sin fuentes publica por tanto la serie con valor **0**
durante toda la vida del pod, y `!= 1` se dispara por sí solo. `absent()` cubre el caso en que no hay
ninguna serie que sumar: ninguna réplica arrancó, o ninguna se está recogiendo. `!= 1` sobre un
resultado vacío está vacío a su vez, lo que es una alerta muda, no una que se dispara. Ese caso afecta
por igual a ambos transportes, y por eso las dos expresiones llevan el emparejamiento.

Los indicadores `is_leader` de ambos servicios suben cuando la réplica **adquiere** el arrendamiento,
no cuando termina de construir su turno de liderazgo. Un relevo normal, por tanto, no se lee como «sin
líder» mientras el nuevo líder reconstruye su estado. Lo que sí se esconde en esa ventana es un líder
**atascado** en la construcción, y en LwM2M un segundo indicador lo identifica: consulta `is_serving`
más abajo.

### Métricas de la ingesta de Sparkplug {#sparkplug-ingestion-metrics}

Prefijo: `devicechain_sparkplugingest_`.

| Señal | Significa |
|---|---|
| `is_leader` | 1 en el pod que sirve, 0 en el resto. **Alerta si la suma no es 1.** |
| `connect_failures_total` | Un broker configurado no es alcanzable. Que suba significa que una fuente está caída mientras el pod parece sano. |
| `subscribe_failures_total` | El broker aceptó la conexión pero rechazó (o nunca confirmó) la suscripción a un grupo, lo más probable porque la credencial de la fuente no puede leerlo. La fuente sigue fuera de línea, no ingiere ninguno de sus grupos y reintenta. **Alerta ante cualquier aumento.** |
| `messages_total` | Tráfico Sparkplug entrante. Una línea plana con una flota viva es el síntoma de una suscripción perdida o de una fuente muerta. |
| `presence_emitted_total` | Señales de conexión/desconexión producidas. |
| `rebirth_requests_total` | Nodos a los que se pide que se vuelvan a anunciar. Que suba de forma sostenida significa que un nodo no consigue resincronizarse. |
| `rebirth_enqueued_total` / `rebirth_dropped_total` | Rebirths que pidió la máquina de sesión, y los que su cola de publicación estaba demasiado llena para aceptar. Un descarte es una señal de latencia más que un fallo (la petición se vuelve a hacer en la siguiente ventana del nodo), pero una tasa de descarte sostenida significa que los rebirths salen más despacio de lo que se piden. Léelo junto a `rebirth_requests_total`, que solo cuenta lo que llegó al cable y por tanto está limitado por el publicador y no por la demanda: **descartes mientras `rebirth_requests_total` sube hasta un techo estable** es un fan-out que supera a un publicador por lo demás sano; **descartes mientras está plano** son las propias publicaciones atascándose, lo que apunta a la conexión con el broker. |
| `unknown_device_dropped_total` | Tráfico de identidades sin dispositivo, con el registro automático desactivado. |
| `decode_errors_total` / `ingest_failures_total` | Payloads malformados, y fallos al publicar hacia adelante. |
| `tenant_deleted_dropped_total` | Tráfico rechazado porque su inquilino está siendo eliminado. |

### Métricas de la ingesta LwM2M {#lwm2m-ingestion-metrics}

Prefijo: `devicechain_lwm2mingest_`.

| Señal | Significa |
|---|---|
| `is_leader` | 1 en el pod que tiene el arrendamiento, desde el momento en que lo adquiere. **Alerta si la suma no es 1.** |
| `is_serving` | 1 cuando el bucle de lectura CoAP/DTLS de ese pod ya está en marcha. Léelo **junto a** `is_leader`: la pareja es lo único que distingue a un líder que todavía reconstruye su tabla de registros de un líder atascado en esa reconstrucción, y todas las demás señales del pod —readiness, liveness, `is_leader`— están en verde en ambos casos. `is_leader == 1 and is_serving == 0` sostenido durante más de lo que tarda un relevo merece una alerta propia. |
| `active_registrations` / `active_sessions` / `active_observations` | La flota viva tal como la ve el servicio. **Vigila `active_observations` a través de un reinicio**: es como ves la pérdida de observaciones descrita más arriba, y como ves su recuperación. |
| `registrations_total` / `registration_updates_total` | Dispositivos que llegan y que mantienen vivas sus sesiones. |
| `registration_expiries_total` | Registros que caducaron en lugar de darse de baja: dispositivos que se desvanecieron. |
| `handshake_failures_total` / `auth_errors_total` | Dispositivos que fallan DTLS, e identidades no aprovisionadas. |
| `observe_establish_refused_total` | Un Observe que el dispositivo rechazó o que falló por otra causa. **El síntoma del cliente solo 1.0**: ese cliente responde al Observe SenML con `4.06` y nunca notifica. |
| `notify_unknown_content_format_total` | Telemetría que llega en un formato que no se decodifica: un dispositivo que *sí* notifica, de forma indescifrable. Cero para un cliente solo 1.0. |
| `notify_decode_failures_total` / `notify_samples_truncated_total` | Payloads malformados o demasiado grandes. |
| `notify_records_non_numeric_total` / `notify_records_non_finite_total` / `notify_records_unnamed_total` | Lecturas que traía un Notify y que no produjeron ninguna medición. **Que no sean numéricas es normal**: una lectura IPSO booleana o de texto es un dispositivo funcionando bien, y este contador es lo que distingue ese caso del de un dispositivo que se ha quedado callado, que desde aquí se ve igual. Los otros dos son fallos de firmware: un valor que resolvió a infinito o NaN, y una lectura sin ruta de recurso. |
| `observation_overflow_total` | Un registro que supera el tope de 32 observaciones. Algunos de sus recursos no se observan. |
| `ingest_messages_shed_total` / `ingest_samples_shed_total` | Un inquilino por encima de su techo de ingesta. |
| `shadows_reconstructed_total` | Presencia reconstruida tras un cambio de liderazgo. Un pico es la huella de un relevo. |
| `commands_failed_total` / `commands_not_served_total` | Comandos descendentes que no llegaron a destino. |
| `command_live_claim_errors_total` | Comandos **no llevados a cabo** porque command-delivery no pudo confirmarlos. Cada comando se confirma con command-delivery inmediatamente antes de llegar al dispositivo, y sin esa confirmación nunca se envía. Una tasa sostenida significa que ningún comando LwM2M está llegando a su dispositivo: **es la señal sobre la que alertar.** Los comandos se reintentan, no se pierden. |
| `commands_stale_dispatch_total` | Entregas descartadas porque la plataforma ya había rearmado o reenviado el comando. **No es un fallo**: cada una es una actuación duplicada que no ocurrió. Es de esperar que suba tras una caída o un relevo. |
| `commands_overflow_parked_total{reason}` | Comandos apartados en command-delivery en lugar de enviarse de inmediato, y entregados en orden momentos después. `full`: la cola del dispositivo estaba llena, así que el dispositivo tarda en responder. `offline`: no tenía conexión activa. `bind`: acababa de conectarse y sus comandos pendientes aún no se habían entregado; **es de esperar un pico tras un relevo**, cuando todos los dispositivos se reconectan a la vez. `unconfirmed`: no se pudo confirmar un comando anterior a él. |
| `command_overflow_blocked_total` | Veces que el adaptador tuvo que esperar porque no podía apartar los comandos lo bastante rápido. Solo sube mientras command-delivery está lento o inalcanzable, y entonces todos los comandos LwM2M esperan. |

### Métricas del agente de borde {#edge-agent-metrics}

Prefijo: `devicechain_edge_`.

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

## Límites de la validación {#what-is-not-validated}

Dos límites en cómo se validan estos servicios:

- **Ningún banco de pruebas distribuido ejercita una flota Sparkplug real.** Nada en el proyecto
  conduce un nodo de borde o un broker de terceros de extremo a extremo. El comportamiento de
  Sparkplug está cubierto por pruebas contra la propia implementación de la plataforma.
- **La vía LwM2M es la mejor validada de las dos.** Se ejercita contra una pila cliente LwM2M
  independiente de terceros, leyendo sus veredictos desde el lado del servidor, de modo que un cliente
  que se comporte mal no puede fabricar un aprobado. Esa suite se ejecuta de forma programada y es
  orientativa, no una puerta de publicación.

El agente de borde está cubierto por sus propias pruebas y no tiene validación de despliegue en
clúster. Pilota un primer despliegue de agente en una planta antes de que se convierta en una flota.
