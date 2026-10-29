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

Si no puedes tolerarlo, acorta el tiempo de vida de registro que solicitan tus dispositivos, a costa
de actualizaciones de registro más frecuentes. Bajar `maxLifetimeSeconds` **no** acorta este apagón:
al dispositivo nunca se le comunica el recorte y sigue actualizando según su propio calendario. Ese
ajuste acota cuánto tiempo figura en línea un dispositivo muerto (consulta
[Ajustes de LwM2M](#lwm2m-settings)), y debe quedar por encima del mayor tiempo de vida que pidan
realmente tus dispositivos, o quedarán expirados como muertos en cada relevo.

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

**A un dispositivo afirmado que muere sin decirlo no lo atrapa un tiempo de espera.** El barrido
de inactividad omite deliberadamente a los dispositivos afirmados, porque en un transporte que afirma
presencia el silencio no es prueba de muerte. Lo que sí atrapa a ese dispositivo difiere según el
transporte, y cada uno tiene una cota que puedes fijar:

1. **Una señal nueva del propio transporte del dispositivo, o una pregunta que no contesta.** Los
   dispositivos afirmados por el broker MQTT propio de DeviceChain obtienen una sin que el dispositivo
   haga nada: una pasada de reparación compara periódicamente la lista de conexiones vivas del broker
   con lo que cree la plataforma, y corrige la diferencia. Consulta
   [Presencia de dispositivo](../concepts/device-presence.md). Los dispositivos Sparkplug también la
   obtienen: un host conectado pide periódicamente a cada nodo con un dispositivo afirmado que se ha
   quedado callado que se vuelva a anunciar, y marca fuera de línea al dispositivo que no contesta. Los
   dispositivos LwM2M se marcan fuera de línea cuando su tiempo de vida de registro caduca sin
   renovación.
2. **Una [degradación](#demoting-a-device).** Es la respuesta cuando la primera no va a llegar nunca,
   porque la fuente que tendría que producirla ya no está. Funciona en los tres transportes que
   afirman presencia.

Cómo acota cada transporte una muerte de la que nunca le hablaron:

- **Un certificado de muerte de Sparkplug perdido.** Si el broker nunca entrega el DEATH del nodo, el
  dispositivo sigue en línea hasta el siguiente sondeo de vida. Un sondeo se ejecuta cuando el host se
  conecta al broker, y después cada `presence.probeSeconds` (treinta minutos de forma predeterminada)
  mientras dure la sesión. Cada sondeo periódico pide un rebirth al nodo de cada dispositivo afirmado
  que no ha producido nada en el último intervalo, espera `presence.probeWindowSeconds` (diez segundos
  de forma predeterminada) y marca fuera de línea al dispositivo que sigue sin producir nada. A un dispositivo del que se ha sabido dentro del intervalo
  nunca se le pregunta ni se le juzga, y cualquier mensaje de un dispositivo cuenta también para su
  nodo, así que los dispositivos que informan con más frecuencia que el intervalo no cuestan nada.
  Una muerte silenciosa se nota, por tanto, en unos dos intervalos (alrededor de una hora con el valor
  predeterminado), con pruebas (preguntado, sin respuesta) y no por silencio. Vigila `presence_probes_total{trigger="periodic"}`: en un host conectado tiene que
  subir; plano significa que el sondeo no se está ejecutando.

  **El sondeo tiene un coste en una flota callada.** Sparkplug informa por excepción, así que un
  dispositivo sano puede no decir nada durante horas con toda legitimidad. Entonces a su nodo se le
  pide un rebirth cada dos intervalos: el nodo vuelve a enviar todos los valores de métricas que tiene,
  los suyos y los de todos sus dispositivos, que se almacenan como lecturas (los births no cuentan
  contra el límite de ingesta del tenant), y cada dispositivo registra una conexión nueva. No se les
  pregunta a todos los nodos a la vez: el intervalo se divide en franjas de al menos dos ventanas de
  respuesta, y a cada nodo se le pregunta siempre en la misma franja, elegida a partir de su id de
  grupo y de nodo, así que los rebirths de una flota callada llegan repartidos a lo largo del
  intervalo. Cada franja sigue esperando su ventana completa para sus propios nodos antes de que
  empiece la siguiente. Si el coste sigue siendo demasiado para tu flota, sube
  `presence.probeSeconds`; la cota de detección crece con él. Un nodo que tarda más que la ventana de
  respuesta en volver a anunciar un dispositivo hace que ese dispositivo se marque fuera de línea
  hasta que llegue su birth, y vuelve a estar en línea por sí solo cuando llega; sube
  `presence.probeWindowSeconds` para nodos grandes en enlaces lentos.
- **Un nodo que se vuelve a anunciar.** Cuando un nodo de borde inicia una sesión nueva, los
  dispositivos hijos de su sesión anterior se reemplazan junto con ella. Un dispositivo hijo que no se
  vuelva a anunciar bajo la nueva sesión sigue mostrándose conectado hasta el siguiente sondeo
  periódico, que pide un rebirth a su nodo y lo marca fuera de línea cuando sigue sin aparecer.
- **Un registro LwM2M largo.** Un dispositivo que desaparece se marca fuera de línea cuando caduca su
  tiempo de vida de registro. Con el valor predeterminado, eso son **86400 segundos**, un día entero.
  `maxLifetimeSeconds` recorta el tiempo de vida de cada registro hasta ese valor como máximo, de modo
  que acota directamente cuánto tiempo puede figurar en línea un dispositivo LwM2M muerto; ponerlo en,
  digamos, 3600 lo limita a una hora. Debe quedar por encima del mayor tiempo de vida que pida
  realmente tu flota.
- **Un transporte afirmante eliminado.** Da de baja la fuente de Sparkplug o la credencial LwM2M por
  la que llegaba un dispositivo, y nada volverá a producir una señal para él, y ningún sondeo se
  ejecuta para una fuente que ya no está configurada. Queda varado en su último estado afirmado hasta
  que alguien lo [libere](#demoting-a-device). Para eso existe la operación, ya que una fuente que ya
  no está no le va a contar nada más a la plataforma.

Las tres cotas, una junto a otra: `brokerPresence.reconcileSeconds` para el broker de la plataforma,
`presence.probeSeconds` para Sparkplug y `maxLifetimeSeconds` para LwM2M. La primera es de cinco
minutos de forma predeterminada y la segunda de treinta, porque preguntar a un nodo Sparkplug cuesta un
rebirth completo; la tercera, de un día, porque es el tiempo de vida de registro
del propio dispositivo, no un ajuste de la plataforma. Si que un dispositivo figure en línea tiene
peso operativo para ti, puedes además combinar la señal de conectividad con una
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

```bash
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

### Dar identidades LwM2M a `dcctl` {#lwm2m-identities}

Una credencial DTLS-PSK es un par: la identidad que presenta un dispositivo y la clave precompartida que la demuestra. `dcctl` toma los pares de un archivo JSON, cada uno vinculado a un inquilino y a un identificador externo:

```json
[
  {
    "identity": "opaque-handle-1",
    "psk": "<clave en base64, de al menos 16 bytes>",
    "tenant": "acme",
    "externalId": "plant-a/sensor-1",
    "deviceTypeToken": "sensor",
    "autoRegister": true
  }
]
```

```bash
dcctl bootstrap local acme --lwm2m-identities identities.json
dcctl upgrade   local acme --lwm2m-identities identities.json
```

Un bootstrap con el archivo también habilita `lwm2m-ingest`; una actualización necesita que el área ya esté habilitada. El archivo se comprueba antes de que nada toque el clúster (una clave corta, un inquilino o un identificador externo ausentes, un `autoRegister` sin tipo de dispositivo, una identidad repetida).

**Dónde viven las claves.** Nunca son un valor de Helm, así que no están en el registro de la versión que imprime `helm get values`, y una clave rotada no queda en una revisión antigua. `dcctl` escribe él mismo el Secret `dci-<instancia>-lwm2m-identities`, antes de la versión y fuera de su manifiesto, y el pod proyecta cada clave como una variable de entorno que nombra el `pskEnv` de la identidad. La versión solo lleva el nombre del Secret y una marca, derivada de la versión del Secret, que reinicia el pod cuando cambia una clave.

Las identidades **se conservan en cada `dcctl upgrade` posterior**, que no lee nada secreto de la versión. Una ejecución que recibe de nuevo el archivo las reemplaza: reescribe el Secret en su sitio (una rotación) y reinicia el pod. Quitarlas es explícito: `dcctl upgrade ... --withdraw-lwm2m-identities` elimina las identidades y el Secret. Un `dcctl bootstrap` repetido sobre una instancia que tiene identidades, sin recibir ninguna, se rechaza salvo que indique `--withdraw-lwm2m-identities`, porque un bootstrap compone la instancia a partir de sus indicadores y de otro modo borraría la clave de cada dispositivo. Un Secret con ese nombre que `dcctl` no escribió nunca se toca.

:::warning Instancias aprovisionadas antes de que las claves salieran de la versión
Una instancia creada por un `dcctl` anterior guarda sus claves en los valores de la versión de Helm y en un Secret que renderizó el chart, `dci-<instancia>-lwm2m-psk`. Su primer `dcctl upgrade` con esta versión las traslada: copia las claves al nuevo Secret **antes** de que cambie la versión, luego actualiza, y Helm elimina el Secret antiguo porque ya no está en el manifiesto. Los pods nuevos arrancan con el Secret nuevo, que ya existe. Los pods que siguen en marcha conservan sus claves en su entorno, pero solo hasta que se reinicie un contenedor, así que ejecuta la actualización cuando un reinicio de lwm2m-ingest sea aceptable.

Helm conserva hasta diez revisiones anteriores de la versión, y esas revisiones siguen teniendo los valores antiguos; `dcctl` no reescribe el historial de Helm. Si importa que esas claves queden retiradas, Un `helm rollback` a una revisión anterior al traslado vuelve a renderizar el Secret antiguo con las claves de esa revisión, así que rota también tras cualquier rollback así. Para retirar las claves, rótalas después de la actualización ejecutando `dcctl upgrade ... --lwm2m-identities` con un archivo de claves nuevas (y vuelve a aprovisionar los dispositivos), y restringe quién puede leer Secrets en el espacio de nombres `default` hasta que las revisiones antiguas hayan caducado.
:::

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
| `listen.port` | `5684` | El puerto UDP que enlaza el servidor CoAP/DTLS. Déjalo en `5684`: el puerto UDP y el Service del chart no siguen un cambio, así que los handshakes desaparecerían en silencio, y `--values` lo rechaza. |
| `security.connectionIdLength` | `8` | Longitud en bytes del Connection ID de DTLS. **Mantenlo distinto de cero** en flotas celulares o itinerantes: es lo que permite que una sesión sobreviva a un cambio de dirección. `0` lo desactiva y fuerza un rehandshake en cada reasignación. |
| `security.idleTimeoutSeconds` | `0` | Recicla una sesión sin tráfico tras ese tiempo. `0` no recicla nunca. Fíjalo por encima del intervalo de despertar en una flota en modo cola. |
| `security.handshakeTimeoutSeconds` | `10` | Acota un handshake DTLS, para que uno atascado no pueda inmovilizar recursos. |
| `security.maxSessions` | `100000` | Techo de la tabla de sesiones vivas. Un handshake por encima del techo se rechaza y se cuenta, nunca se admite en silencio. |
| `maxLifetimeSeconds` | `86400` | El techo hasta el que se recorta el tiempo de vida de todo registro. **Es la palanca que acota cuánto tiempo figura en línea un dispositivo muerto.** Debe quedar por encima del mayor tiempo de vida que pidan tus dispositivos. |
| `ingestRateLimit.readingsPerSecond` | `1000` | Techo de ingesta sostenida por inquilino, contado en lecturas (muestras decodificadas) y en mensajes Notify antes de decodificar. Sin definir o con un valor no positivo, recae en este predeterminado, nunca en ilimitado. |
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

**Un nodo cuyos nacimientos no se pudieron registrar recibe la petición de volver a nacer.** Cuando un nacimiento no se puede registrar (normalmente porque el tipo de dispositivo de la fuente aún no existe), el nodo se recuerda y se le envía un `NCMD` `Node Control/Rebirth` a intervalos crecientes, hasta que se registra. Vigila **`birth_recovery_pending`**: un valor que se mantiene por encima de cero es un nodo, o una fuente entera, que no se registra, y la línea de log de nivel error nombra el tipo de dispositivo. Créalo y el indicador baja a cero por sí solo.

**La reconexión la gestiona la plataforma, no la biblioteca cliente de MQTT.** Cada reconexión abre
una sesión realmente nueva con una marca de tiempo nueva. Sparkplug exige que el nacimiento del host y
su certificado de muerte lleven la misma marca de tiempo, para que un nodo de borde pueda rechazar una
muerte retrasada de una sesión anterior. Es también la razón por la que todas las réplicas comparten
un mismo client id: lo que expulsa a un host zombi es el propio desalojo por client id duplicado del
broker.

:::caution La vía de Sparkplug no acota la tasa de mensajes ni el tamaño total de un mensaje
Sparkplug cobra las lecturas de cada mensaje DATA contra el techo de ingesta del inquilino, y descarta
lo que lo supera. No mide la tasa de mensajes, los nacimientos ni las muertes, y el tamaño total de un
mensaje solo lo acota el broker. Consulta [Qué acota la ingesta de Sparkplug](#unbounded-sparkplug-ingest).
:::

### Entregar a `dcctl` una fuente Sparkplug {#sparkplug-sources}

Una fuente Sparkplug es conexión y credencial, así que una superposición `--values` la rechaza. Entrégala a `dcctl` como archivo, en `bootstrap` (que además habilita el área `sparkplug-ingest`) o en `upgrade` (el área ya debe estar en marcha; `dcctl enable-area` la activa):

```yaml
# sources.yaml
- tenant: acme
  hostId: devicechain
  broker:
    url: ssl://broker.acme.example:8883
    username: devicechain
    password: the-broker-password      # o passwordFile: /ruta/al/archivo
  groups: [plant-a]
  autoRegister: true
  deviceTypeToken: sparkplug-pump      # créalo antes de que la fuente conecte, o consulta la nota de abajo
  commands:
    enabled: true
    hostRole: primary
    writableMetrics: ["Outputs/*"]
```

```bash
dcctl bootstrap local acme --enable-area sparkplug-ingest --sparkplug-source sources.yaml
dcctl upgrade   local acme --sparkplug-source sources.yaml
```

El archivo se comprueba antes de tocar el clúster (falta el inquilino, una URL de broker sin host, un `autoRegister` sin tipo de dispositivo, dos inquilinos en un broker, una clave desconocida).

**Dónde vive la contraseña.** Nunca es un valor de Helm, así que no está en el registro de la release que imprime `helm get values`, y una contraseña rotada no queda en una revisión antigua. `dcctl` escribe él mismo el Secret `dci-<instancia>-sparkplug-broker`, antes de la release y fuera de su manifiesto, y el pod lo proyecta como variable de entorno que nombra el `passwordEnv` de la fuente. La release solo lleva el nombre del Secret y una marca, con sal por instancia y derivada de la versión del Secret y no de su contenido, que reinicia el pod cuando cambia la contraseña. `dcctl` no imprime ni la contraseña ni el contenido del archivo. Para que la contraseña no esté en el archivo que entregas a `dcctl`, usa `passwordFile: /ruta` (se lee una vez), o gestiona tú mismo el Secret y escribe `passwordSecretRef: {name: mi-secret, key: password}` en el bloque broker; `dcctl` entonces ni lo lee ni lo escribe.

Las fuentes **se conservan en cada `dcctl upgrade` posterior** (la actualización no lee nada secreto de la release), y una ejecución que recibe el archivo de nuevo las sustituye; una contraseña rotada reemplaza el Secret en su sitio y reinicia el pod. Retirarlas es explícito: `dcctl upgrade ... --withdraw-sparkplug-sources` elimina las fuentes y el Secret. Un `dcctl bootstrap` repetido sobre una instancia que tiene fuentes, sin recibir ninguna, se rechaza hasta que pases `--sparkplug-source` o `--withdraw-sparkplug-sources`. Las claves son las de la lista `sources` del chart, descritas más abajo.

:::note Un tipo de dispositivo que aún no existe
Un nodo se registra con el `deviceTypeToken` de la fuente. Si el tipo se crea después de que la fuente conecte, la plataforma pide que vuelvan a nacer los nodos que no pudo registrar, hasta que se registren; consulta [un nodo que nace antes de que exista su tipo de dispositivo](../concepts/sparkplug.md#birth-recovery).
:::

### Comandos Sparkplug {#sparkplug-commands}

El canal descendente de comandos descrito en [Comandos Sparkplug](../concepts/sparkplug.md#commands) es **opcional y está desactivado por defecto**. Se configura por fuente: en el bloque `commands` del archivo que entregas a `dcctl` ([arriba](#sparkplug-sources)) o, si gestionas tú mismo la release de Helm, en los valores del chart para `functionalAreas.sparkplug-ingest.config.sources`, como aquí.

```yaml
functionalAreas:
  sparkplug-ingest:
    config:
      sources:
        - tenant: acme
          hostId: devicechain
          broker:
            url: ssl://broker.acme.example:8883
            username: devicechain
            passwordEnv: SPARKPLUG_ACME_PW
          groups: [plant-a]
          commands:
            enabled: true
            hostRole: primary            # obligatorio: primary o secondary
            writableMetrics:             # vacío (el valor por defecto) no permite escrituras
              - "Outputs/*"
            allowReboot: false
```

| Ajuste | Por defecto | Significado |
| --- | --- | --- |
| `enabled` | `false` | Activa el canal descendente para esta fuente |
| `hostRole` | ninguno, obligatorio | `primary` o `secondary`; consulta [host primario y secundario](../concepts/sparkplug.md#primary-and-secondary-host) |
| `writableMetrics` | vacío | Métricas a las que puede apuntar `sparkplug.write`: nombres exactos o un prefijo terminado en `/*`. Vacío **no** permite escrituras. `bdSeq` y las métricas `Node Control/` y `Device Control/` no se pueden incluir |
| `allowReboot` | `false` | Permite que `sparkplug.reboot` llegue a los nodos de esta fuente |
| `confirmWindowSeconds` | `120` | Cuánto se espera un informe tras una escritura (5 a 900). Auméntalo para nodos con enlaces lentos |
| `rebootWindowSeconds` | `600` | Cuánto se espera el nuevo nacimiento del nodo tras un reinicio, y el tiempo mínimo entre dos reinicios de un nodo (30 a 3600) |
| `clockSkewSeconds` | `30` | Cuánto antes de la publicación puede estar la marca de tiempo del propio nodo y aun así contar (0 a 300) |
| `publishPerSecond`, `burst` | `50`, `200` | Máximo de comandos por segundo que esta fuente pone en el cable, y la ráfaga |

Reglas que el servicio aplica al arrancar, de modo que un error lo detiene con un mensaje en lugar de enviar algo no deseado:

- **Una fuente por bróker.** Una fuente con comandos habilitados debe ser la única fuente Sparkplug de su endpoint de bróker, para que un nodo nunca reciba el mismo comando dos veces. Fusiona los grupos en una sola fuente.
- **`hostRole` es obligatorio** cuando los comandos están habilitados.
- **`command-delivery` debe estar desplegado.** El chart fija por sí mismo el ajuste `sparkplugCommands` de command-delivery a partir de las fuentes, y se niega a renderizar cuando una fuente habilita comandos y `command-delivery` no está habilitado, o cuando la dirección interna de `command-delivery` está vacía. Ese ajuste no se puede fijar a mano.

Tras un reinicio gradual con el ajuste aplicado, declara los comandos en el perfil del dispositivo (**Añadir comandos estándar de Sparkplug** en la consola) y publícalo. Los comandos que llegan a una fuente que no los habilita fallan al instante como no entregados.

#### La ACL del bróker {#sparkplug-commands-acl}

La credencial de la fuente necesita permiso para **publicar** comandos, además del acceso de lectura y la publicación de `STATE` que ya necesita. Para Mosquitto, en el archivo de ACL, para el `username` de la fuente y sus grupos:

```
user devicechain
topic read spBv1.0/plant-a/#
topic write spBv1.0/STATE/devicechain
topic write spBv1.0/plant-a/NCMD/+
topic write spBv1.0/plant-a/DCMD/+/+
```

Otros bróker usan su propia sintaxis para la misma regla: publicar en `spBv1.0/<group>/NCMD/<node>` y `spBv1.0/<group>/DCMD/<node>/<device>`. Un bróker que descarta en silencio una publicación rechazada deja los comandos en `TIMEOUT`; uno que desconecta al cliente ante una publicación rechazada abre el cortacircuitos, que lanza `SparkplugCommandBreakerOpen`. Si todos los comandos acaban sin confirmar, se dispara `SparkplugCommandsUnconfirmed`, y lo primero que hay que revisar es el permiso de publicación de la credencial.

### Qué acota la ingesta de Sparkplug {#unbounded-sparkplug-ingest}

**Las lecturas se cobran contra el techo de ingesta del inquilino.** Cada métrica numérica de un
mensaje NDATA o DDATA es una lectura, que se cobra contra el mismo techo por inquilino, contado en
lecturas, que usan todos los demás transportes de dispositivo (consulta
[qué permite el valor por defecto](../concepts/governance.md#ingest-default)). Se cobra una vez por
mensaje, antes de almacenarlo, y un reintento dentro del manejador no se vuelve a cobrar. Las lecturas
por encima del techo se descartan y se cuentan en `ingest_samples_shed_total`. Un Host de sesión
limpia no recibe reentregas del broker, así que una lectura descartada se pierde, como ocurre en
cualquier transporte con una lectura por encima de su techo.

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `ingestRateLimit.readingsPerSecond` | `1000` | Techo de ingesta sostenida por inquilino, contado en lecturas. El nivel del inquilino lo sustituye. Sin definir o con un valor no positivo, recae en este predeterminado, nunca en ilimitado. |
| `ingestRateLimit.burst` | `2000` | Ráfaga permitida para lo anterior, en lecturas. |

**Qué no se mide.** La tasa de mensajes no se mide, porque el Host debe observar cada mensaje para
mantener correctas sus sesiones. Los valores de métricas de un nacimiento no se cobran: Sparkplug
informa por excepción, así que un nacimiento es el único lugar donde aparece una métrica que cambia
poco, y un renacimiento tras una reconexión o una conmutación por error reenvía a la vez el conjunto
completo de métricas de cada nodo. Los nacimientos los acota la sesión: una vez por secuencia de
nacimiento, y con los renacimientos solicitados espaciados. Tampoco se miden las transiciones de
presencia que afirman nacimientos y muertes. Un nodo de borde desbocado aún puede enviar mensajes tan
rápido como el broker los entregue. Acótalo en el broker, o mediante los grupos a los que te suscribes.

**Las lecturas también se descartan mientras la plataforma aplica contrapresión.** Mientras la
plataforma rechaza eventos nuevos (consulta
[Contrapresión en la ruta de ingesta](./observability.md#ingest-backpressure)), las lecturas de un
mensaje que aún no se han almacenado se descartan en el acto, sin el reintento dentro del manejador,
y se cuentan en `ingest_failures_total`. Eso incluye los valores de métricas de un nacimiento, que el
techo nunca descarta: una métrica que cambia poco y se pierde así falta hasta que vuelve a cambiar o
el nodo renace. Las transiciones de conexión y desconexión que afirman nacimientos y muertes se
siguen aceptando.

**La publicación es por ventana, y la ventana también es un límite de pérdida.** Cada conexión lee
sus mensajes de uno en uno, en orden, porque el Host debe ver cada mensaje para mantener correctas
sus sesiones. No espera a que la plataforma almacene un mensaje antes de leer el siguiente: mantiene
una ventana de mensajes esperando la confirmación del broker de la plataforma. Antes esperaba por
cada uno, así que una conexión no podía superar un mensaje por confirmación, unos 88 por segundo en
un broker replicado, donde una confirmación tarda unos 11 ms, y todas las fuentes de Sparkplug se
ejecutan en el único pod líder. Una publicación que falla se intenta de nuevo, hasta tres intentos
en total, con medio segundo entre ellos; después el mensaje se descarta y se cuenta en
`ingest_failures_total`, como siempre. Una parada, o el traspaso a otro líder, espera (hasta 8
segundos) a que se almacene lo que está en vuelo antes de liberar el arrendamiento.

| Ajuste | Valor por defecto | Qué hace |
|---|---|---|
| `ingest.publishWindow` | `32` | Cuántos mensajes mantiene una conexión de Sparkplug esperando a la vez la confirmación del broker de la plataforma, de 1 a 1024. Un valor fuera de ese rango, o un 0 explícito, detiene el servicio al arrancar; nunca se ajusta. La publicación no puede ir más rápido que este número dividido por lo que tarda una confirmación (unos 2.800 mensajes por segundo por conexión con 32). Es un ajuste fijo, leído al arrancar, igual para todas las fuentes. **También es cuánto más puede perder un pod.** El Host es un cliente de sesión limpia que confirma cada mensaje a tu broker en cuanto lo ha tomado, y un broker no reentrega a un host que se reinició, así que un pod que se detiene de golpe pierde lo que había tomado y aún no almacenó: hasta este número de mensajes por conexión, donde antes era uno, más, solo mientras las publicaciones fallan, las que esperan un nuevo intento (como mucho, más o menos otra ventana). Un nuevo líder recupera la presencia y los valores actuales de las métricas de cada nodo mediante rebirths, no las lecturas perdidas entre medias. El almacenamiento y reenvío del nodo de borde cubre el tiempo en que el Host está desconectado, no los mensajes ya entregados a un Host que luego murió. Vigila `devicechain_<area>_jetstream_publish_window_full_seconds_total{suffix="inbound-events", writer="sparkplug"}`: una tasa cercana a 1 significa que la ventana es lo que limita la publicación. |
| `presence.probeSeconds` | `1800` | Cada cuánto un Host conectado vuelve a comprobar la vida de sus dispositivos afirmados. Una vez por intervalo, al nodo de cada dispositivo que la plataforma tiene por conectado y que no ha producido nada en el último intervalo se le pide un rebirth; un dispositivo que sigue callado tras la ventana de respuesta (`presence.probeWindowSeconds`) se marca fuera de línea. Los nodos se reparten a lo largo del intervalo, cada uno en su propio punto fijo, en lugar de preguntarse a todos a la vez. A un dispositivo del que se ha sabido dentro del intervalo nunca se le pregunta ni se le juzga, y los mensajes de un dispositivo cuentan también para su nodo, así que solo se le pregunta a un nodo con un dispositivo callado, como mucho cada dos intervalos. Cada pregunta hace que ese nodo vuelva a enviar todos los valores de métricas que tiene y registra una conexión nueva para cada uno de sus dispositivos; consulta la nota sobre el coste en [dispositivos atascados en línea](#a-device-that-reads-online-and-is-not). Esto es lo que atrapa un certificado de muerte que el broker nunca entregó en un Host que permanece conectado; consulta [dispositivos atascados en línea](#a-device-that-reads-online-and-is-not). Sin valor o 0 significa 1800. Un valor escrito por debajo de 60 detiene el servicio al arrancar; nunca se ajusta. No hay interruptor de apagado: una flota para la que un rebirth periódico resulta caro lo fija largo. Es un ajuste fijo, leído al arrancar, igual para todas las fuentes. |
| `presence.probeWindowSeconds` | `10` | Cuánto espera un sondeo a que conteste un nodo al que pidió un rebirth, con su propio birth y el de cada dispositivo, antes de marcar fuera de línea a los dispositivos de los que no ha sabido nada. Se aplica también al sondeo que se ejecuta al conectar. Un nodo más lento hace que esos dispositivos se marquen fuera de línea hasta que lleguen sus births, momento en que vuelven a estar en línea por sí solos, así que súbelo para nodos grandes en enlaces lentos. Sin valor o 0 significa 10. Un valor escrito por debajo de 5, o que no quede por debajo de `presence.probeSeconds`, detiene el servicio al arrancar; nunca se ajusta. Es un ajuste fijo, leído al arrancar, igual para todas las fuentes. |

**El límite de lecturas se aplica dividiendo.** El
[límite de lecturas por evento](../guides/connecting-a-device.md#how-much-one-message-may-carry) se
aplica en esta vía dividiendo el mensaje, no rechazándolo. Un DDATA que lleve miles de métricas se
convierte en varios eventos, y sigue siendo una lectura almacenada por métrica: cada una, su propia
fila, actualización de estado y evaluación de reglas en el motor de detección que comparten todos los
inquilinos. La división acota cada evento, y el techo acota las lecturas por segundo. Un mensaje
también está acotado, a 16.384 métricas: de un mensaje que lleve más no se almacena ninguna lectura,
sus métricas se cuentan en `samples_skipped_total{reason="oversize"}` y el servicio registra una
advertencia que nombra el tema. El mensaje sigue contando para la sesión, así que un birth por encima
del límite sigue conectando el nodo y declarando sus alias, y los DATA posteriores del nodo se leen
como siempre. Acota el número de métricas por publicación en el nodo de borde.

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
MQTT corriente a los dispositivos locales, almacena en disco local lo que publican y lo vuelve a publicar en la nube como una [pasarela](#gateway-identity): un dispositivo corriente con identidad propia, al que la plataforma solo permite hablar por los dispositivos que le has asignado. No hay ningún servicio específico del agente en la plataforma ni configuración específica del agente en los servicios de la nube.

**No es un área funcional del chart.** No aparece en ninguna lista de áreas ni en ningún perfil de
despliegue, y no se puede habilitar como se habilita un servicio. Se distribuye como binarios
estáticos y una imagen de contenedor, y lo despliegas tú: una unidad de systemd en una pasarela de
planta, un contenedor o un manifiesto de Kubernetes escrito a mano en el borde.

**Los binarios se adjuntan a cada [versión de GitHub](https://github.com/devicechain-io/devicechain/releases)**
como `dc-edge-agent_<version>_<os>_<arch>.tar.gz`, para Linux y macOS en `amd64` y `arm64`. Sus
resúmenes SHA-256 están en `dc-edge-agent_checksums.txt`, en la misma versión, así que en Linux
`sha256sum --ignore-missing -c dc-edge-agent_checksums.txt` comprueba lo que descargaste. Linux es el
destino en el borde; las compilaciones de macOS sirven para probar el agente en una estación de
trabajo. No hay compilación para Windows.

### Identidad de pasarela {#gateway-identity}

El agente es una **pasarela**: un dispositivo corriente de la plataforma que representa a otros dispositivos. Inicia sesión como sí mismo, con su propia credencial de dispositivo, y publica los eventos de cada dispositivo local en su propio espacio de nombres, `{instanceId}/{tenant}/gateways/{gateway}/devices/{device}/events` (consulta [el espacio de nombres de pasarela](../reference/device-protocol.md#gateway-namespace)). La plataforma acepta un evento solo para un dispositivo **asignado a esa pasarela**. Es el único modo de enlace ascendente.

**Cómo configurar una:**

1. Crea el dispositivo pasarela, con cualquier tipo de dispositivo, y dale una [credencial](../guides/device-credentials.md) `MQTT_BASIC`. El token del dispositivo es el `uplink.gatewayToken` del agente. El id de la credencial va en `uplink.username` como `{tenant}:{credentialId}`, y su secreto en la variable de entorno que indica `uplink.passwordEnv`.
2. Crea los dispositivos que representa la planta y asigna cada uno a la pasarela, con `assignDeviceGateway` o con `gatewayToken` al crearlo. Un dispositivo tiene como máximo una pasarela, y una pasarela no puede tener a su vez una pasarela. Asignar requiere el mismo permiso que gestionar credenciales de dispositivo.
3. Arranca el agente. Los dispositivos locales siguen publicando en él sus propios topics, sin cambios.

El agente inicia sesión con la credencial `MQTT_BASIC` anterior. Todavía no tiene un ajuste de certificado de cliente, así que hoy una pasarela no puede autenticarse por [certificado](../concepts/device-certificates.md).

Una asignación surte efecto con el siguiente evento del dispositivo, en segundos y sin reconexión. Quitarla funciona igual, y el resto de dispositivos de la pasarela siguen fluyendo. Deshabilitar la pasarela, o revocar su credencial, termina su sesión.

**Qué hace el agente en el enlace ascendente:**

- Se conecta con el client id MQTT simple del dispositivo pasarela, de modo que el broker afirma la presencia de la propia pasarela como con cualquier dispositivo. No hay un client id por agente: dos agentes que usan un mismo dispositivo pasarela se arrebatan la sesión.
- Construye cada topic del enlace ascendente solo a partir de un token de dispositivo hijo, así que no puede publicar fuera de su propio espacio de nombres. El broker no puede rechazar una publicación fuera de una concesión (MQTT 3.1.1 no tiene forma de hacerlo), así que la confirmaría y la descartaría. Que no pueda construir un topic así es lo que elimina ese fallo.
- Solo captura de sus dispositivos el tenant configurado. Una publicación que nombra otro tenant se cuenta en `tenant_mismatched_total` y nunca se almacena.
- Elimina `credentialType`, `credentialId` y `credentialSecret` de los cuerpos de los eventos antes de reenviarlos, y los cuenta en `body_credentials_stripped_total`. La plataforma rechaza un evento que lleve una en un topic autenticado por el broker. Cambia el dispositivo para que deje de enviarlas.
- Mantiene hasta **64 publicaciones en vuelo** y confirma cada evento almacenado con su propio acuse. Un agente que esperara cada acuse antes de enviar el siguiente no podría superar un evento por viaje de ida y vuelta, unos 20 por segundo con 50 ms, y una planta ocupada nunca se pondría al día tras una interrupción. Los eventos reenviados conservan la hora en que se almacenaron por primera vez, y la nube sigue plegando los duplicados.
- Se suscribe al topic de avisos de la pasarela en cada conexión. Si el broker rechaza esa suscripción, el enlace ascendente permanece caído y el registro dice por qué.

**Cuando se rechaza un evento.** La plataforma rechaza un evento de un dispositivo que no está asignado a la pasarela, o que no existe. Lo registra con su carga en el flujo `failed-events` con el motivo `NotGatewayChild`, lo cuenta en `resolve_not_gateway_child_total` y envía a la pasarela un *aviso*. El broker ya confirmó la publicación, así que nada más se lo diría al agente. Ante un aviso, el agente registra una advertencia con el nombre del dispositivo (como máximo una vez por dispositivo cada 30 segundos), lo cuenta en `refused_total{reason="not_gateway_child"}` y escribe el evento en `refused-events.jsonl`, en el directorio del almacén. Ese archivo es privado para el usuario del agente, contiene un objeto JSON por línea y se rota una vez a los 16 MiB.

Hay tres límites. Un aviso es de mejor esfuerzo: una pasarela que estaba desconectada cuando se envió no lo recibe, así que en lo que puedes confiar es en el registro y el contador de la plataforma. El agente conserva una copia acotada de lo que reenvió (los últimos 8192 eventos, y como máximo 16 MiB) para emparejarla con un aviso, de modo que un aviso que llega cuando la copia ya se descartó se cuenta en `refused_unmatched_total` y se registra, pero no se conserva. Y el archivo contiene la carga tal como se reenvió, con las credenciales ya eliminadas. Recuperar un evento rechazado tras corregir la asignación es una reproducción manual.

El agente también rechaza, sin enviarlo, un evento que la plataforma solo podría rechazar: el de un dispositivo local que usa el token de la propia pasarela (`reason="gateway_self"`) o un token que la plataforma no puede almacenar (`reason="invalid_device_token"`). Se escriben en el mismo archivo.

:::warning Actualiza la plataforma antes de pasar un agente al enlace ascendente de pasarela
Termina primero la actualización de la plataforma: todas las réplicas de `event-sources` y de `device-management`, y las concesiones del broker. Un `event-sources` anterior con `infrastructure.nats.mqttFailClosed` activado confirma los mensajes de pasarela que no puede atribuir y los descarta, de modo que el agente ve éxito mientras no se almacena nada. Un agente cuyo broker no se ha actualizado no puede suscribirse a sus avisos y no informa del enlace ascendente como conectado (`uplink_connected` se queda en 0).
:::

**De qué es de fiar una pasarela.** Una pasarela habla por los dispositivos que tiene asignados. Una pasarela comprometida puede falsificar eventos solo de los dispositivos que tiene asignados, y solo hasta que se la deshabilita o se revoca su credencial. Lo que afirma sobre *qué cliente local* envió un evento es tan fiable como la autenticación local de la planta.

Los comandos a dispositivos tras una pasarela aún no se enrutan, y el agente solo reenvía eventos.

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

**El almacén limita bytes, no antigüedad.** Un evento que espera en él conserva la hora que informó
el dispositivo (a un evento JSON que no informó ninguna se le estampa la hora en que el agente lo
guardó), así que llega a la plataforma tan antiguo como la interrupción que esperó, y más si el
dispositivo ya lo había acumulado antes. La plataforma rechaza una lectura con más de 366 días de
antigüedad respecto al momento en que llega, así que un evento retenido más tiempo se pierde al
llegar. El agente no avisa de ello; la señal es `spool_oldest_age_seconds`. Una alerta sobre ella por
encima de 335 días (28.944.000 segundos) deja un mes para restablecer el enlace ascendente.

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
| `local.listenPort` | `1883` | El puerto MQTT al que se conectan los dispositivos de la planta. |
| `local.storeDir` | — | Obligatorio. El directorio que contiene el almacén durable. Un agente por directorio. |
| `local.spoolMaxBytes` | `1 GiB` | Presupuesto del almacén. Más allá, se descartan los eventos más antiguos. El mínimo es 16 MiB. |
| `local.metricsPort` | `9090` | Puerto de métricas y salud, solo en loopback. Un `0` explícito desactiva el endpoint. |
| `uplink.brokerUrl` | — | Obligatorio. El endpoint MQTT de la nube al que reenviar. |
| `uplink.tenant` | — | Obligatorio. El tenant al que pertenece el dispositivo pasarela. Forma parte de cada topic del enlace ascendente, y es el único tenant que el agente toma de sus dispositivos. |
| `uplink.gatewayToken` | — | Obligatorio. El token del dispositivo pasarela con el que inicia sesión este agente. **Un agente por dispositivo pasarela**: dos que lo compartan se arrebatan la sesión en bucle. |
| `uplink.username` | — | Obligatorio. `{tenant}:{credentialId}`, la credencial del dispositivo pasarela. Un nombre sin el prefijo del tenant se rechaza al arrancar el agente. |
| `uplink.passwordEnv` | — | Obligatorio. La variable de entorno que contiene el secreto de la credencial. |
| `uplink.connectTimeoutSeconds` | `30` | Acota un intento de conexión del enlace ascendente. |
| `uplink.backoffMinSeconds` / `uplink.backoffMaxSeconds` | `1` / `60` | Cotas de la espera creciente de reconexión mientras el enlace está caído. |

## Qué vigilar {#what-to-watch}

:::note Se distribuyen diez alertas para estos dos servicios
El chart distribuye diez reglas de alerta para sparkplug-ingest y lwm2m-ingest, descritas en
[Alertas que se distribuyen](#edge-alerts): si cada transporte se está leyendo siquiera, un líder de
LwM2M atascado las maneras en que los dos servicios pierden datos y el canal descendente de comandos de Sparkplug. **Cualquier otra métrica de las
tablas de abajo no tiene alerta ni panel.** Se emite y se recoge, y nada te avisará de ella hasta que
escribas la regla tú mismo. El agente de borde no tiene ninguna alerta distribuida.
:::

Todas las métricas llevan el prefijo `devicechain_` y el segmento de su propio servicio:
`devicechain_sparkplugingest_`, `devicechain_lwm2mingest_`, `devicechain_edge_`. Ninguna está
etiquetada por dispositivo. Solo `command_breaker_state` lleva las etiquetas `tenant` y `source`, acotadas
por las fuentes que configuras, así que ninguna supone un riesgo de cardinalidad al recogerla.

### Alertas que se distribuyen {#edge-alerts}

| Alerta | Severidad | Qué significa | Qué hacer |
| --- | --- | --- | --- |
| `Lwm2mIngestLeaderCountNotOne` | critical | Durante 2 minutos, el número de réplicas de lwm2m-ingest que informan de que tienen el arrendamiento de liderazgo no ha sido exactamente 1. Con 0, nadie está leyendo el transporte CoAP/DTLS y todos los dispositivos LwM2M son inalcanzables, aunque todos los pods parezcan sanos. Con más de 1, dos réplicas sirven a la vez. | Lee los logs de lwm2m-ingest para ver por qué ninguna réplica adquirió el arrendamiento; lo habitual es que el broker de mensajes de la plataforma no sea alcanzable. |
| `Lwm2mIngestLeaderNotServing` | critical | Durante 5 minutos, un pod ha tenido el arrendamiento de LwM2M mientras su bucle de lectura CoAP/DTLS no está en marcha (`is_leader` es 1 e `is_serving` es 0). Está atascado construyendo su turno, normalmente leyendo de device-state los dispositivos afirmados como activos, y como conserva el arrendamiento ninguna standby puede tomar el relevo. No se está sirviendo a ningún dispositivo LwM2M. | Lee el log de ese pod alrededor de `Acquired LwM2M leadership`, y reinicia el pod para que una standby pueda adquirir el arrendamiento. |
| `Lwm2mIngestDroppingNotifications` | warning | En los últimos 10 minutos, durante 10 minutos, lwm2m-ingest ha descartado las lecturas de mensajes Notify porque la ingesta los falló o los rechazó, contrapresión incluida. No hay reintento en la ruta de Notify, así que un dispositivo que informa pocas veces pierde su lectura. | Revisa el broker de mensajes, la cola del flujo de eventos entrantes (consulta [Contrapresión en la ruta de ingesta](./observability.md#ingest-backpressure)) y el log de lwm2m-ingest. Se cuenta en `notify_ingest_dropped_total`. |
| `SparkplugIngestLeaderCountNotOne` | critical | Durante 2 minutos, el número de réplicas de sparkplug-ingest que informan de que tienen el arrendamiento no ha sido exactamente 1. Con 0, no se está leyendo ninguna fuente Sparkplug. Con más de 1, dos réplicas conectan las mismas fuentes. Un pod desplegado sin fuentes configuradas nunca toma el arrendamiento y marca 0 de forma permanente. | Configura las fuentes del pod o deja el servicio fuera del despliegue. Si no, lee los logs de sparkplug-ingest para ver por qué ninguna réplica adquirió el arrendamiento. |
| `SparkplugIngestBrokerUnreachable` | warning | En los últimos 10 minutos, durante 10 minutos, sparkplug-ingest no ha logrado conectar con un broker Sparkplug configurado. La preparación deliberadamente no depende de que el broker sea alcanzable, así que el pod parece sano mientras esa fuente no ingiere nada. | Lo provocan un host incorrecto, una credencial rechazada o una caída del broker. El log de sparkplug-ingest nombra la fuente y el motivo. Se cuenta en `connect_failures_total`. |
| `SparkplugIngestSubscriptionRefused` | warning | En los últimos 15 minutos, durante 5 minutos, una fuente Sparkplug ha sido abandonada y reintentada porque su broker aceptó la conexión pero rechazó, o nunca confirmó, la suscripción a un grupo. Mientras se rechace algún grupo, la fuente no ingiere ninguno de sus grupos y nunca anuncia ONLINE. | Compara las reglas de acceso del broker para la credencial de la fuente con los grupos configurados para ella; lo más probable es que la credencial no pueda leer alguno. Se cuenta en `subscribe_failures_total`. |
| `SparkplugIngestDroppingData` | warning | En los últimos 10 minutos, durante 10 minutos, sparkplug-ingest ha abandonado mensajes que aceptó: sus lecturas o transiciones de presencia se descartaron tras agotarse el presupuesto de reintentos, en el acto bajo contrapresión, o al apagarse. Un Host Sparkplug no recibe reentrega del broker, así que cada uno se pierde. | Revisa device-management y el broker de mensajes, y el log de sparkplug-ingest en busca de `Dropping Sparkplug samples`. Se cuenta en `ingest_failures_total`. |
| `SparkplugCommandsUnconfirmed` | warning | Durante 15 minutos, más del 90 % de los comandos Sparkplug publicados en los últimos 30 minutos (al menos 20) terminaron sin que el nodo informara del valor solicitado. Sparkplug no tiene confirmación de comandos, así que así se ve una vía de comandos que no funciona. Los comandos se registran como TIMEOUT, no como fallidos. | Comprueba que la credencial de la fuente puede publicar en los topics `NCMD` y `DCMD`, que `commands.hostRole` corresponde a cómo están configurados los nodos (un nodo puede ignorar a un host que no es su primario) y si `commands.confirmWindowSeconds` es menor que el retraso con que informan los nodos. Se cuenta en `commands_total{result="unconfirmed"}`. |
| `SparkplugCommandBreakerOpen` | warning | En los últimos 20 minutos, el cortacircuitos de comandos de una fuente se abrió. Se abre cuando el broker del cliente cierra la sesión del Host justo después de publicar un comando, lo que casi siempre significa que la credencial no puede publicar en `spBv1.0/<group>/NCMD/+` ni en `spBv1.0/<group>/DCMD/+/+`. Mientras está abierto, los comandos a esa fuente fallan al instante como no entregados; la telemetría de la fuente no se ve afectada. | Concede el permiso de publicación en el broker. La alerta se silencia 20 minutos después de la última apertura del cortacircuitos; no espera a que se cierre. Tras su pausa (5 minutos, que se duplica hasta 1 hora en cada reapertura) queda semiabierto, con valor 2 en `command_breaker_state`, y solo se cierra cuando se publica el siguiente comando sin desconexión, así que una fuente sin comandos en espera se queda en 2 sin alertar. La alerta indica el tenant y la fuente. |
| `SparkplugCommandResponsesFailing` | warning | Durante 5 minutos, sparkplug-ingest ha contado comandos que no pudo llevar a término, por `result`: `claim_failed`, `park_failed` y `response_failed` significan que no se pudo contactar con command-delivery o que rechazó la llamada; `kv_failed` significa que no se pudo escribir el almacén de comandos pendientes de confirmación, así que el comando no se publicó; `publish_failed` significa que una publicación pudo llegar o no al nodo. | Revisa command-delivery y el broker de mensajes, y el log de sparkplug-ingest. Los rechazos y los comandos sin confirmar son resultados, no fallos, y no cuentan aquí. Se cuenta en `commands_total`. |

### Las alertas de líder {#no-leader-alert}

Las dos alertas `LeaderCountNotOne` son la señal con más peso de toda esta superficie: cero significa
que nadie está sirviendo ese transporte, y que todos los dispositivos que dependen de él son
inalcanzables en silencio. Se disparan con una suma distinta de uno, y deliberadamente **no llevan
rama `absent()`**. Un servicio que no está desplegado no exporta nada, y a las reglas del chart no se
les dice qué servicios ejecuta una instalación. Un servicio desplegado sin ningún pod en marcha
tampoco exporta nada; eso es un pod que falla ruidosamente por sí mismo, mediante reinicios y
preparación. Si quieres que te avisen cuando la serie no existe en absoluto, añade tú el emparejamiento:

> `sum(devicechain_lwm2mingest_is_leader) != 1 or absent(devicechain_lwm2mingest_is_leader)`
>
> `sum(devicechain_sparkplugingest_is_leader) != 1 or absent(devicechain_sparkplugingest_is_leader)`

Un pod de Sparkplug que se ejecuta sin fuentes registra su indicador `is_leader` al inicializarse,
antes de comprobar si tiene algo que servir, y después nunca adquiere el arrendamiento. Publica la
serie con valor **0** durante toda la vida del pod, y `SparkplugIngestLeaderCountNotOne` se dispara
por sí sola con ella. Es deliberado: un servicio de transporte sin nada que servir es una
configuración que arreglar, o un servicio que dejar fuera del despliegue.

Los indicadores `is_leader` de ambos servicios suben cuando la réplica **adquiere** el arrendamiento,
no cuando termina de construir su turno de liderazgo. Un relevo normal, por tanto, no se lee como «sin
líder» mientras el nuevo líder reconstruye su estado. Lo que sí se esconde en esa ventana es un líder
**atascado** en la construcción, y en LwM2M un segundo indicador lo identifica: consulta `is_serving`
más abajo, y `Lwm2mIngestLeaderNotServing` más arriba.

### Métricas de la ingesta de Sparkplug {#sparkplug-ingestion-metrics}

Prefijo: `devicechain_sparkplugingest_`.

| Señal | Significa |
|---|---|
| `is_leader` | 1 en el pod que sirve, 0 en el resto. `SparkplugIngestLeaderCountNotOne` se dispara cuando la suma no es 1. |
| `connect_failures_total` | Un broker configurado no es alcanzable. Que suba significa que una fuente está caída mientras el pod parece sano. Alimenta `SparkplugIngestBrokerUnreachable`. |
| `subscribe_failures_total` | El broker aceptó la conexión pero rechazó (o nunca confirmó) la suscripción a un grupo, lo más probable porque la credencial de la fuente no puede leerlo. La fuente sigue fuera de línea, no ingiere ninguno de sus grupos y reintenta. `SparkplugIngestSubscriptionRefused` se dispara ante un aumento. |
| `birth_recovery_pending` | Nodos cuyos nacimientos no se pudieron registrar (casi siempre porque el `deviceTypeToken` de la fuente nombra un tipo de dispositivo que aún no existe) y a los que se pide que vuelvan a nacer. Un valor distinto de cero que se mantiene es un nodo, o una fuente, que no se registra. |
| `birth_recovery_rebirths_total` | Comandos de rebirth enviados a esos nodos, a intervalos crecientes hasta un minuto. Una serie que crece junto a un `birth_recovery_pending` distinto de cero es un registro que sigue fallando. |
| `messages_total` | Tráfico Sparkplug entrante. Una línea plana con una flota viva es el síntoma de una suscripción perdida o de una fuente muerta. |
| `presence_emitted_total` | Señales de conexión/desconexión producidas. |
| `rebirth_requests_total` | Nodos a los que se pide que se vuelvan a anunciar. Que suba de forma sostenida significa que un nodo no consigue resincronizarse. Los rebirths del sondeo de vida también cuentan aquí, así que en una flota con dispositivos callados espera un goteo constante repartido a lo largo de cada `presence.probeSeconds`. |
| `presence_probes_total` | Sondeos de vida que llegaron a un veredicto, por `trigger`: `connect` (la reconciliación que se ejecuta una vez por sesión con el broker) o `periodic` (uno por franja de la rotación que visita cada nodo una vez por `presence.probeSeconds` en una sesión estable). Un sondeo pide un rebirth a los nodos de los dispositivos afirmados callados que le tocan y marca fuera de línea a los dispositivos que no contestan; una franja que no encuentra ninguno callado también cuenta. **En un Host conectado la serie periódica tiene que subir.** Plana significa que el sondeo no se está ejecutando, y una flota callada y un sondeo que nunca corre son por lo demás indistinguibles. |
| `presence_probe_disconnects_total` | Dispositivos afirmados que un sondeo de vida marcó fuera de línea por no contestar a un rebirth dentro de la ventana, por el mismo `trigger`. Cercano a cero en una flota sana. Una tasa `periodic` sostenida significa que se están perdiendo certificados de muerte entre los nodos y el Host. |
| `rebirth_enqueued_total` / `rebirth_dropped_total` | Rebirths que pidió la máquina de sesión, y los que su cola de publicación estaba demasiado llena para aceptar. Un descarte es una señal de latencia más que un fallo (la petición se vuelve a hacer en la siguiente ventana del nodo), pero una tasa de descarte sostenida significa que los rebirths salen más despacio de lo que se piden. Léelo junto a `rebirth_requests_total`, que solo cuenta lo que llegó al cable y por tanto está limitado por el publicador y no por la demanda: **descartes mientras `rebirth_requests_total` sube hasta un techo estable** es un fan-out que supera a un publicador por lo demás sano; **descartes mientras está plano** son las propias publicaciones atascándose, lo que apunta a la conexión con el broker. |
| `unknown_device_dropped_total` | Tráfico de identidades sin dispositivo, con el registro automático desactivado. |
| `decode_errors_total` | Payloads malformados. |
| `ingest_failures_total` | Mensajes aceptados cuyas lecturas, o las transiciones de conexión y desconexión que afirman sus nacimientos y muertes, se descartaron, más los lotes de desconexiones que la reconciliación de presencia declaró y no pudo almacenar: las lecturas en el acto mientras la plataforma aplica contrapresión, y unas u otras cuando se agotó el reintento (device-management o el broker no respondían, o el broker de la plataforma no confirmó la publicación), cuando la cola de publicaciones a la espera de un nuevo intento estaba llena, o mientras la conexión se cerraba. Un mensaje dividido en varios eventos puede haber almacenado algunos de sus eventos. Un Host de sesión limpia no recibe reentregas, así que cada uno se pierde. Alimenta `SparkplugIngestDroppingData`. |
| `ingest_samples_shed_total` | Lecturas DATA descartadas porque un inquilino supera su techo de ingesta. Un inquilino que descarta aquí envía más lecturas de las que permite su nivel. |
| `tenant_deleted_dropped_total` | Tráfico rechazado porque su inquilino está siendo eliminado. |
| `samples_too_old_dropped_total` | Lecturas descartadas porque su marca de tiempo es anterior en más de 366 días al momento en que el host las recibió; el resto de su mensaje se almacena. Una métrica cuyo valor no ha cambiado en ese tiempo se descarta de cada nacimiento, y un nodo sin reloj configurado (horas cercanas a 1970) ve descartadas todas sus lecturas. El servicio registra un aviso que nombra el dispositivo. |
| `commands_total` | Comandos que gestionó el canal descendente, por `result`. Resultados: `published`, `confirmed`, `confirmed_unchanged`, `unconfirmed`, `superseded`, `rearmed`, `parked`, `drained`; rechazos: `refused_disabled`, `refused_breaker`, `refused_expired`, `refused_payload`, `refused_not_allowed`, `refused_metric`, `refused_datatype`, `refused_reboot_gate`; que no son de este servicio: `not_served`, `poison`, `claim_lost`, `tenant_gone`; fallos: `claim_failed`, `park_failed`, `publish_failed`, `kv_failed`, `response_failed`. Es cero hasta que una fuente habilita los comandos. Alimenta `SparkplugCommandsUnconfirmed` (sin confirmar sobre publicados) y `SparkplugCommandResponsesFailing` (los cinco fallos). |
| `commands_awaiting` | Comandos publicados a la espera de que el nodo informe del valor solicitado. |
| `command_confirm_seconds` | Cuánto tardaron los comandos confirmados desde la publicación hasta el informe del nodo. Los comandos sin confirmar no se observan aquí. |
| `command_breaker_state` | Por `tenant` y `source`: 0 cerrado, 1 abierto (los comandos fallan al instante), 2 semiabierto (se deja pasar un comando). Alimenta `SparkplugCommandBreakerOpen`. |
| `command_breaker_trips_total` | Veces que se ha abierto un cortacircuitos. Una tasa aquí con `SparkplugCommandBreakerOpen` en silencio indica un broker que cerró la sesión una vez. |

### Métricas de la ingesta LwM2M {#lwm2m-ingestion-metrics}

Prefijo: `devicechain_lwm2mingest_`.

| Señal | Significa |
|---|---|
| `is_leader` | 1 en el pod que tiene el arrendamiento, desde el momento en que lo adquiere. `Lwm2mIngestLeaderCountNotOne` se dispara cuando la suma no es 1. |
| `is_serving` | 1 cuando el bucle de lectura CoAP/DTLS de ese pod ya está en marcha. Léelo **junto a** `is_leader`: la pareja es lo único que distingue a un líder que todavía reconstruye su tabla de registros de un líder atascado en esa reconstrucción, y todas las demás señales del pod —readiness, liveness, `is_leader`— están en verde en ambos casos. `is_leader == 1 and is_serving == 0` sostenido durante más de lo que tarda un relevo es sobre lo que alerta `Lwm2mIngestLeaderNotServing`. |
| `active_registrations` / `active_sessions` / `active_observations` | La flota viva tal como la ve el servicio. **Vigila `active_observations` a través de un reinicio**: es como ves la pérdida de observaciones descrita más arriba, y como ves su recuperación. |
| `registrations_total` / `registration_updates_total` | Dispositivos que llegan y que mantienen vivas sus sesiones. |
| `registration_expiries_total` | Registros que caducaron en lugar de darse de baja: dispositivos que se desvanecieron. |
| `handshake_failures_total` / `auth_errors_total` | Dispositivos que fallan DTLS, e identidades no aprovisionadas. |
| `observe_establish_refused_total` | Un Observe que el dispositivo rechazó o que falló por otra causa. **El síntoma del cliente solo 1.0**: ese cliente responde al Observe SenML con `4.06` y nunca notifica. |
| `notify_unknown_content_format_total` | Telemetría que llega en un formato que no se decodifica: un dispositivo que *sí* notifica, de forma indescifrable. Cero para un cliente solo 1.0. |
| `notify_decode_failures_total` | Payloads malformados. |
| `notify_records_non_numeric_total` / `notify_records_non_finite_total` / `notify_records_unnamed_total` | Lecturas que traía un Notify y que no produjeron ninguna medición. **Que no sean numéricas es normal**: una lectura IPSO booleana o de texto es un dispositivo funcionando bien, y este contador es lo que distingue ese caso del de un dispositivo que se ha quedado callado, que desde aquí se ve igual. Los otros dos son fallos de firmware: un valor que resolvió a infinito o NaN, y una lectura sin ruta de recurso. |
| `observation_overflow_total` | Un registro que supera el tope de 32 observaciones. Algunos de sus recursos no se observan. |
| `telemetry_too_old_dropped_total` | Lecturas descartadas porque su hora es anterior en más de 366 días al momento en que el adaptador las recibió; el resto de su Notify se almacena. El servicio registra un aviso que nombra el dispositivo. |
| `ingest_messages_shed_total` / `ingest_samples_shed_total` | Un inquilino por encima de su techo de ingesta, en mensajes Notify y en lecturas. |
| `notify_ingest_dropped_total` | Mensajes Notify cuyas lecturas se descartaron porque la plataforma las rechazó o no pudo recibirlas, incluido mientras aplica contrapresión. No hay reintento; el siguiente Notify sustituye lo perdido. Un Notify dividido en varios eventos puede haber almacenado sus primeros eventos. Alimenta `Lwm2mIngestDroppingNotifications`. |
| `shadows_reconstructed_total` | Presencia reconstruida tras un cambio de liderazgo. Un pico es la huella de un relevo. |
| `commands_failed_total` / `commands_not_served_total` | Comandos descendentes que no llegaron a destino. |
| `command_live_claim_errors_total` | Comandos **no llevados a cabo** porque command-delivery no pudo confirmarlos. Cada comando se confirma con command-delivery inmediatamente antes de llegar al dispositivo, y sin esa confirmación nunca se envía. Una tasa sostenida significa que ningún comando LwM2M está llegando a su dispositivo: **es la señal sobre la que alertar.** Los comandos se reintentan, no se pierden. |
| `commands_stale_dispatch_total` | Entregas descartadas porque la plataforma ya había rearmado o reenviado el comando. **No es un fallo**: cada una es una actuación duplicada que no ocurrió. Es de esperar que suba tras una caída o un relevo. |
| `commands_overflow_parked_total{reason}` | Comandos apartados en command-delivery en lugar de enviarse de inmediato, y entregados en orden momentos después. `full`: la cola del dispositivo estaba llena, así que el dispositivo tarda en responder. `offline`: no tenía conexión activa. `bind`: acababa de conectarse y sus comandos pendientes aún no se habían entregado; **es de esperar un pico tras un relevo**, cuando todos los dispositivos se reconectan a la vez. `unconfirmed`: no se pudo confirmar un comando anterior a él. |
| `command_overflow_blocked_total` | Veces que el adaptador tuvo que esperar porque no podía apartar los comandos lo bastante rápido. Solo sube mientras command-delivery está lento o inalcanzable, y entonces todos los comandos LwM2M esperan. |
| `command_drain_turns_total` | Turnos de vaciado ejecutados, cada uno de los cuales entrega como máximo 4 de los comandos apartados de un dispositivo. Una tasa que sube mientras `commands_overflow_parked_total` se mantiene plana significa que las colas pendientes se están vaciando. |

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
| `tenant_mismatched_total` | Publicaciones de dispositivo que nombran un tenant distinto de `uplink.tenant`. No se almacenan ni se reenvían. |
| `body_credentials_stripped_total` | Miembros de credencial eliminados de los cuerpos de eventos antes de reenviarlos. El dispositivo debería dejar de enviarlos. |
| `refused_total{reason}` | **Eventos rechazados para esta pasarela.** `not_gateway_child`: la plataforma rechazó un evento porque su dispositivo no está asignado a la pasarela o no existe. `gateway_self` e `invalid_device_token`: el agente no quiso enviarlo. `other`: un motivo que esta versión del agente no conoce. Cualquier incremento requiere atención; consulta [identidad de pasarela](#gateway-identity). |
| `refused_kept_total` / `refused_unmatched_total` | Eventos rechazados escritos en `refused-events.jsonl` / rechazos cuyo evento ya no se conservaba en el equipo (el registro `failed-events` de la plataforma lo tiene). |
| `uplink_inflight` | Publicaciones a la espera de acuse, como máximo 64. Mantenerse en 64 mientras se vacía un atraso es normal. |
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
