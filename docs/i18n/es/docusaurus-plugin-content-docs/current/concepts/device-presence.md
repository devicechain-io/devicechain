---
title: Presencia de dispositivo
---

# Presencia de dispositivo

DeviceChain mantiene una señal de **presencia** en vivo para cada dispositivo — si está actualmente en línea, y cuándo se conectó, desconectó o reportó actividad por última vez. La presencia forma parte del [último estado conocido](./architecture.md) de un dispositivo (la misma proyección que contiene sus mediciones más recientes), y aparece en la pestaña **Connectivity** del dispositivo en la consola.

Lo importante es entender *cómo* DeviceChain decide que un dispositivo está en línea, porque depende del transporte.

## Dos formas de conocer la presencia

Todo dispositivo lleva una **fuente de presencia** (presence source) que indica cómo se determina su estado en línea/fuera de línea:

- **Inferida** (predeterminada) — DeviceChain no tiene una señal explícita de conexión/desconexión del transporte, por lo que infiere la presencia a partir de la **actividad**. Un dispositivo se considera en línea mientras esté enviando datos; si permanece en silencio más tiempo que su **tiempo de espera de inactividad** (inactivity timeout), un barrido en segundo plano lo marca como fuera de línea. Este es el modelo correcto para transportes sin conexión (HTTP simple, CoAP).

- **Afirmada** (asserted) — el transporte le indica a DeviceChain *explícitamente* cuándo un dispositivo se conecta y se desconecta, por lo que la presencia es **autoritativa** en lugar de deducida. La primera vez que llega una señal de este tipo para un dispositivo, DeviceChain cambia ese dispositivo a la fuente afirmada y, a partir de entonces:
  - su estado en línea/fuera de línea se rige **únicamente** por señales explícitas de conexión/desconexión — un paquete de datos aislado nunca puede marcar como en línea a un dispositivo que la plataforma ha registrado como fuera de línea;
  - el barrido de inactividad lo deja en paz — un dispositivo afirmado que queda en silencio *no* se asume muerto, porque el silencio no es evidencia de muerte en un transporte cuyo cometido es justamente reportar la muerte de forma explícita. Mezclar ambos modos permitiría marcar como fuera de línea a un dispositivo que reporta en un intervalo largo mientras la plataforma ha sido informada de que está conectado.

Un dispositivo permanece **inferido** hasta que un transporte que afirma presencia produce una señal para él, por lo que nada cambia para los dispositivos existentes a menos que comiencen a llegar a través de un transporte que afirme presencia. Hoy tres transportes afirman presencia:

- **MQTT simple**, para dispositivos conectados al propio broker de DeviceChain. No se requiere ninguna cooperación del dispositivo — no tiene que publicar un mensaje de nacimiento, definir un last will ni anunciarse de ninguna manera. El broker ya sabe el momento exacto en que una conexión se abre y se cierra, y DeviceChain lo lee directamente. Lo que hay que equipar para ello es la *instancia*, no el dispositivo: leer esas conexiones necesita una credencial de cuenta de sistema de NATS y una fuente de eventos que apunte al propio broker de esta instancia, y hasta que tenga ambas la toma permanece apagada y los dispositivos MQTT siguen siendo inferidos. Una instancia que *sí tuvo* toma y luego pierde esa credencial es un caso distinto de una que nunca la tuvo: los dispositivos que ya había afirmado se quedarían congelados en lo último que reportaron, así que en su lugar se los devuelve a presencia inferida. Vea [Devolver un dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device). `dcctl bootstrap` acuña esa credencial y configura esa fuente, así que una instancia levantada de ese modo afirma la presencia MQTT sin trabajo adicional; un `helm install` a secas deja la credencial vacía y no lo hace. [Confirmar que la toma del broker está realmente en marcha](#confirming-the-tap), más abajo, es cómo se distingue un caso del otro.
- **[Sparkplug-B](./sparkplug.md)**, cuyos mensajes BIRTH y DEATH son exactamente estas señales explícitas de conexión/desconexión.
- **[LwM2M](./lwm2m.md)**, cuyo ciclo de vida de registro — registro, actualización periódica y baja de registro (o un tiempo de vida vencido) — hace lo mismo.

Dos detalles del caso MQTT conviene conocerlos antes de construir sobre él:

- **Sigue la conexión principal del dispositivo.** Un dispositivo puede abrir conexiones adicionales añadiendo su propio sufijo al identificador de cliente (el flujo de trabajo de dos terminales `mosquitto_sub` / `mosquitto_pub`). Esas conexiones adicionales se ignoran deliberadamente para la presencia: el estado del dispositivo sigue su sesión principal, de modo que cerrar una conexión secundaria nunca hace que un dispositivo conectado se lea como fuera de línea. Un dispositivo que *solo* se conecta con un identificador de cliente con sufijo no se afirma en absoluto y permanece inferido.
- **Cubre los dispositivos del broker de esta instancia.** Un dispositivo que llega a DeviceChain a través de un broker que usted mismo opera no es algo cuyas conexiones DeviceChain pueda observar, así que permanece inferido.

La consecuencia de esa omisión es deliberada, y conviene entenderla antes de depender de ella: **un dispositivo afirmado no tiene una red de seguridad por inactividad.** Su señal de desconexión solo puede venir del transporte, así que si esa señal nunca llega — un certificado de muerte (death) de Sparkplug perdido junto con la conexión, o un dispositivo LwM2M cuyo tiempo de vida de registro aún no ha vencido (el valor predeterminado del propio LwM2M es de 86400 segundos, un día completo) — el dispositivo sigue apareciendo en línea sin nada que lo corrija. Qué vigilar, y cómo acotar esa ventana, está en **[Cómo operar los servicios de borde](../deployment/edge-services.md)**. Si el transporte que lo habría dicho se ha ido para siempre y no solo está callado, [devolver el dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device) es lo que vuelve a poner una corrección a su alcance.

Para los dispositivos MQTT, DeviceChain cierra esa brecha por sí mismo en lugar de dejársela a usted. Hay un caso en el que el broker no puede avisar de que un dispositivo se ha ido: cuando el propio broker se reinicia, las conexiones que mantenía simplemente desaparecen y nunca se anuncia una desconexión para ellas. Por eso DeviceChain compara periódicamente la lista de conexiones vivas del broker con lo que cree, y corrige la diferencia en ambos sentidos — dispositivos que no sabía que estaban conectados, y dispositivos que cree conectados y que el broker no mantiene. Los dispositivos que se reconectan tras un reinicio del broker se corrigen con su propia reconexión; el resto se corrige en una comparación posterior — una que pueda dar cuenta de todo el clúster, según el párrafo siguiente y la [advertencia sobre reducir el clúster](#resizing-the-broker-cluster).

Esa comparación se niega deliberadamente a marcar nada como fuera de línea salvo que pueda dar cuenta de **todos** los nodos del clúster del broker. Si un nodo está lento o inaccesible, sus dispositivos faltan de la lista y son indistinguibles de dispositivos que realmente se han ido — y marcar erróneamente como fuera de línea a un dispositivo vivo es el error más dañino, porque todo lo que depende de la presencia actúa en consecuencia: el dispositivo aparece fuera de línea en su pestaña Connectivity, y una [regla de Connectivity](./event-processing.md#condition-types) genera una alarma de desconexión para un dispositivo que era alcanzable todo el tiempo. En esa situación DeviceChain sigue marcando como en línea los dispositivos recién vistos y simplemente espera a la siguiente pasada para decidir sobre los que faltan.

La *fuente* de presencia se expone allí donde cambia el significado de una lectura. La pestaña Connectivity de la consola la nombra —*Reportado por el transporte* o *Inferido a partir de la actividad*— y distingue un dispositivo que el transporte reportó como **Desconectado** de otro que simplemente está **Fuera de línea**, es decir, que no ha llegado nada recientemente, que es también exactamente el aspecto que tiene un dispositivo sano con un intervalo de reporte largo. La herramienta `get_device_state` de [MCP](./mcp.md) devuelve `presenceSource` junto al estado y le indica al asistente que no reporte como caído un dispositivo inactivo inferido. Y puede leerse mediante la API: `presenceSource` es un campo del tipo `DeviceState` de `device-state` y devuelve `ASSERTED` o `INFERRED`.

## Por qué importa la distinción

La presencia inferida es conveniente pero lenta y ambigua: "fuera de línea" solo significa "no ha hablado recientemente", lo cual es lento para detectar una desconexión real y ciego para dispositivos que reportan en un intervalo largo. La presencia afirmada es inmediata e inequívoca — una desconexión es una desconexión en el instante en que el transporte la reporta — que es lo que se desea para cualquier cosa sobre la que se vaya a alarmar o actuar.

Mantener los dos modos como una marca explícita por dispositivo significa que un dispositivo en un transporte sin conexión conserva su comportamiento de tiempo de espera habitual, mientras que un dispositivo en un transporte consciente de la presencia obtiene la señal autoritativa, y ambos nunca interfieren entre sí.

:::note Estado
La presencia de dispositivo — tanto inferida como afirmada — está disponible, con tres transportes que afirman presencia: MQTT simple sobre el propio broker de DeviceChain (que solo afirma una vez que la instancia tiene la credencial de cuenta de sistema descrita más arriba), [Sparkplug-B](./sparkplug.md) y [LwM2M](./lwm2m.md). Una **regla de detección puede dispararse directamente sobre un borde de conexión/desconexión**: la [condición de Connectivity](./event-processing.md#condition-types) genera una alarma en el instante en que llega una desconexión autoritativa y la resuelve al reconectar — sin tiempo de espera que ajustar. El motor ya la evalúa hoy, pero ninguna superficie de autoría de la consola la ofrece todavía — el generador de formularios y el lienzo de automatización omiten ambos ese tipo de condición, de modo que una regla de conectividad se define enviando la regla directamente a la API. **No abra ninguna en el editor de formulario de la consola**: no reconoce el tipo, así que lee la regla como una regla de umbral y al guardar reemplaza la definición original, sin aviso alguno. (El lienzo la rechaza correctamente, nombrando el tipo no soportado.) Complementa la regla de Absence basada en tiempo de espera (muerte autoritativa frente a silencio inferido), y ambas están pensadas para usarse en conjunto. Una desconexión autoritativa también actualiza el estado en vivo del dispositivo, de modo que la pestaña Connectivity muestra el dispositivo fuera de línea en el instante en que el transporte lo reporta.
:::

## Cómo se opera

La presencia vale lo que vale la señal que hay detrás, y los dos transportes de borde que la afirman
se ejecutan cada uno como una única réplica propietaria — lo que le da a la presencia algunas
propiedades operativas que conviene conocer antes de alarmar sobre ella: qué cuesta un relevo, por
qué un dispositivo afirmado puede quedarse mostrando en línea, y cómo acotar eso. Todo ello se
cubre en **[Cómo operar los servicios de borde](../deployment/edge-services.md)**.

Otras tres propiedades pertenecen específicamente a la presencia MQTT afirmada por el broker.

### Confirmar que la toma del broker está realmente en marcha {#confirming-the-tap}

Leer conexiones del broker propio de DeviceChain necesita cuatro cosas, y si falta alguna la toma
**se niega a arrancar**. Registra el motivo, pone `presence_tap_off{reason}` para decir cuál es, y
los dispositivos MQTT que nunca fueron afirmados siguen siendo inferidos — lo que se ve exactamente
igual que una instancia que nunca tuvo presencia afirmada, porque funcionalmente lo es. Las cuatro:

- que `brokerPresence.enabled` no esté puesto a `false`
- que haya una credencial de cuenta de sistema de NATS configurada (`dcctl bootstrap` acuña una;
  esta es la razón habitual de que una instancia montada a mano no tenga toma)
- que al menos una fuente de eventos apunte al broker propio de la plataforma — sin ninguna, no hay
  avisos de conexión que leer
- que las llamadas entre servicios estén configuradas. Sin ellas la toma funcionaría sin ruta de
  reparación, así que se queda apagada deliberadamente en lugar de funcionar a medias: un
  dispositivo cuya desconexión el broker nunca anunció se leería como conectado para siempre

Eso es toda la historia en una instancia que nunca tuvo toma. Una que **sí la tuvo** tiene un segundo
problema: los dispositivos ya marcados como afirmados conservan la presencia que tuvieran por última
vez, porque un dispositivo afirmado está exento del barrido de inactividad y un evento de datos no
puede cambiarlo. Así que, por los dos primeros motivos de la lista —un `enabled: false` escrito y una
credencial de cuenta de sistema ausente—, `event-sources` devuelve por sí mismo esos dispositivos a
presencia inferida. Ambos son configuración que todas las réplicas leen igual, que es lo que hace
seguro automatizar la liberación de una flota entera a partir de ellos.

**Un broker que la toma no consigue alcanzar también los libera**, y por una razón más fuerte que la
configuración: la pasarela MQTT por la que se conectan los dispositivos vive en ese mismo broker, así
que mientras esté inalcanzable no hay ningún dispositivo conectado por ella. La toma da treinta
segundos a la conexión para establecerse antes de decidir, así que esto es medio minuto sin conexión
con la cuenta de sistema, no un intento fallido —tiempo suficiente para no confundir un broker que se
está reiniciando junto a los servicios con uno que se ha ido—.

**Para este motivo en concreto, la espera de dos minutos es una comprobación, no un retraso**, y esa
diferencia es lo que impide que la liberación sobreviva a la caída que la provocó. Antes de cada
pasada —la primera incluida— el servicio vuelve a marcar contra la cuenta de sistema. Si el broker
responde, no se libera nada: el servicio **termina, y el pod se reinicia** con una toma que arranca
con normalidad. Así que un broker que vuelve produce un reinicio de pod, no una flota de dispositivos
liberados. Las otras dos vías de liberación no pueden funcionar así, y tampoco lo necesitan: ambas
son configuración que se lee una sola vez al arrancar, de modo que lo que su ventana espera es el
*pod de reemplazo* que despliega un cambio de configuración.

Por las tres razones restantes no se libera nada automáticamente: ninguna fuente apuntando al broker
de la plataforma, ninguna configuración de llamadas entre servicios, y una suscripción que falla sobre
una conexión que *sí* alcanzó el broker. `dcctl presence demote` es la puerta en esos casos. Ambas
vías están descritas en [Devolver un dispositivo a presencia
inferida](../deployment/edge-services.md#demoting-a-device).

**Dos señales le dicen que la toma no está en marcha, y cubren fallos distintos.**

`presence_tap_off{reason}` es la directa. Se pone a 1 en cada vía por la que la toma se niega a
arrancar, con la etiqueta nombrando cuál, y responde a una pregunta que una flota en silencio deja
sin respuesta de otro modo: una flota MQTT de larga vida legítimamente no emite avisos de conexión ni
desconexión durante días, así que nada en el flujo ordinario de eventos de presencia distingue una
instancia que está afirmando presencia de otra que sencillamente nunca la arrancó.

No cubre una toma que arrancó y luego dejó de funcionar, porque en ese caso nada se niega a arrancar.
**Para eso está `presence_canary_missed_total`, y es el contador sobre el que alarmar.** El servicio
abre su propia conexión MQTT una vez por minuto exclusivamente para que una toma que funciona tenga
algo que observar: `presence_canary_observed_total` sube con una toma sana, y
`presence_canary_missed_total` sube cuando la cadena está rota.

Lea los contadores de presencia como tráfico, no como salud. `presence_events_total` está
legítimamente plano en una flota en silencio — y legítimamente *no* plano en una toma que se acaba de
apagar, porque liberar los dispositivos que esa toma tenía afirmados emite un evento por dispositivo
bajo `presence_events_total{state="demoted"}`. Ninguna de las dos formas dice nada sobre si la
presencia se está leyendo.

El canario funciona con su propio calendario, independiente de la pasada de reparación descrita más
abajo. Esa separación es lo que hace fiable al contador: un instrumento que solo pudiera informar
mientras aquello que vigila estuviera sano se quedaría callado justo en el momento que importa.

### Cómo ajustar la toma {#broker-presence-settings}

La toma se distribuye con valores predeterminados que funcionan, y la mayoría de las instancias nunca
los cambian. Viven bajo la configuración `brokerPresence` del área `event-sources`.

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `enabled` | activada si no se define | Ejecuta la toma. Póngalo en `false` para desactivar deliberadamente la presencia MQTT afirmada por el broker — por ejemplo, en una instancia cuyo broker se comparte con algo que no admite un suscriptor de cuenta de sistema. Los dispositivos MQTT pasan entonces a presencia inferida, y los que la toma ya hubiera afirmado se liberan de vuelta a ella, a ritmo pausado, en los minutos siguientes. |
| `reconcileSeconds` | `300` | Cada cuánto se compara la lista de conexiones vivas del broker con la de la plataforma, en ambos sentidos. **Esto no es una red de seguridad.** Un reinicio ordenado del broker no anuncia desconexión alguna, así que esta pasada es lo único que corrige jamás a esos dispositivos, y un dispositivo afirmado no tiene detrás ningún barrido de inactividad. Bájelo para reparar antes, a cambio de un inventario de todo el clúster más una lectura por inquilino en cada pasada. |
| `canarySeconds` | `60` | Cada cuánto el servicio abre su propia conexión MQTT para demostrar que la toma sigue viva. Es el calendario contra el que cuenta `presence_canary_missed_total`. |
| `canaryDeadlineSeconds` | `15` | Acota una sola sonda. Si es demasiado estrecho, informa de fallos que la toma no tiene. |
| `inventoryGatherSeconds` | `5` | Cuánto tiempo recoge una pasada las respuestas del clúster del broker. Si es demasiado corto, un nodo que solo va lento se lee como ausente, lo que retiene todas las desconexiones de esa pasada. |

Un valor no positivo en cualquiera de los cuatro intervalos recae en el predeterminado de arriba, no
en cero.

### Una pasada de reparación que se queda sin tiempo lo dice {#reconcile-pass-timeout}

Cada pasada de reparación recorre todos los inquilinos de la instancia y lee los dispositivos
declarados de cada uno, así que en una instancia grande la pasada es larga. Está acotada: una
pasada que no pueda terminar dentro de su presupuesto se detiene, informa
`presence_reconcile_runs_total{outcome="timeout"}` y registra cuántos inquilinos cubrió.

**La siguiente pasada continúa en el inquilino al que no llegó**, en lugar de empezar otra vez desde
el principio. Sin eso, una flota cuya pasada nunca cupiera en el presupuesto repararía los mismos
primeros inquilinos en cada intento y nunca alcanzaría al resto — no tarde, nunca. Resultados
`timeout` ocasionales significan que las reparaciones van con retraso y que cada inquilino sigue
teniendo su turno; una racha *sostenida* significa que la instancia necesita más margen, y los
dispositivos al final de la rotación son aquellos cuyas desconexiones perdidas quedan sin corregir
durante más tiempo.

`presence_reconcile_runs_total` lleva un resultado por pasada, y conviene distinguirlos:

| Resultado | Qué significa |
| --- | --- |
| `complete` | se recorrieron todos los inquilinos contra un clúster de brokers íntegramente contabilizado |
| `partial` | la pasada se ejecutó, pero no respondieron todos los nodos del broker — solo se marcaron dispositivos **en línea**, nunca fuera de línea |
| `timeout` | la pasada agotó su presupuesto; los inquilinos a los que no llegó van primero la próxima vez |
| `failed` | la pasada no pudo leer nada — ni el inventario del broker, ni la lista de inquilinos, ni **el estado de presencia de ningún inquilino**. No reparó absolutamente nada |
| `cancelled` | el servicio se estaba deteniendo a mitad de pasada. No es un fallo |

`failed` es el resultado sobre el que alarmar junto con la sonda. Que falle la lectura de un solo
inquilino se tolera — los demás siguen teniendo su pasada — pero que fallen *todas* significa que
las reparaciones se han detenido por completo, que es como se ve desde aquí una caída de
device-state.

### Las transiciones de presencia se contabilizan contra el techo de ingesta del inquilino {#presence-and-the-ingest-ceiling}

Las transiciones de conexión y desconexión pasan por el mismo [límite de ingesta](./governance.md)
por inquilino que la telemetría, y **se rechazan cuando un inquilino está en su techo** — contadas
en `presence_events_refused_total`.

**Una degradación pasa por la misma puerta**, y eso conviene preverlo por sí solo: a un inquilino
apretado contra su techo se le puede rechazar la *reparación* junto con la rotación que causa la
presión. No se pierde nada —una liberación rechazada deja el dispositivo afirmado, así que la
siguiente pasada lo vuelve a encontrar—, pero la reparación llega no antes de lo que el techo
permita.

Es deliberado, no un descuido. La rotación de conexiones la controla enteramente el dispositivo y
por lo demás es gratis: un dispositivo reconectándose en bucle sería un amplificador de escrituras
no medido que el limitador de ingesta nunca ve. Pero la consecuencia conviene preverla — un
inquilino apretado contra su techo tiene dispositivos cuyo estado en línea/fuera de línea es
incorrecto, y sigue siéndolo hasta que una pasada de reconciliación posterior lo repare (por
defecto, hasta cinco minutos). Todo lo que dependa de la presencia también es incorrecto durante esa
ventana, incluidas las reglas de conectividad y la liberación de comandos retenidos para un
dispositivo fuera de línea.

Aplica a la toma MQTT del broker de la plataforma. La ingesta de Sparkplug no aplica techo por
inquilino y no descarta nada, y LwM2M usa su propio límite configurado por separado.

### Reducir el clúster del broker exige reiniciar `event-sources` {#resizing-the-broker-cluster}

La comparación anterior se niega a marcar nada fuera de línea si no puede dar cuenta de todos los
nodos del clúster del broker. Decide qué es «todos los nodos» a partir del clúster más grande que
ha visto jamás — una marca que solo sube, que es lo que impide que una partición de red provoque
desconexiones falsas masivas (un broker aislado por rutas se declara a sí mismo como todo el
clúster y si no cumpliría su propia comprobación).

El coste de ese diseño es un caso que no puede distinguir: **reducir el clúster del broker a
propósito**. Responden menos nodos que el máximo recordado, así que toda pasada posterior se trata
como incompleta y no se hace ninguna reparación de desconexión — no hasta la pasada siguiente, sino
durante toda la vida del proceso. Los dispositivos huérfanos del nodo retirado se leen en línea
indefinidamente, y como un dispositivo afirmado no tiene red de seguridad por inactividad, ningún
tiempo de espera los corrige.

**Tras reducir el clúster de NATS, reinicie `event-sources`.** La señal de que hacía falta y no se
hizo es `presence_reconcile_withheld_disconnects_total` subiendo sin estabilizarse. Ampliar el
clúster no requiere nada.

Si el reinicio tiene que esperar, los dispositivos huérfanos no. `dcctl presence demote` sobre esa
fuente los devuelve a presencia inferida, donde el barrido de inactividad de diez minutos puede
marcarlos fuera de línea con su propia evidencia — vea [Devolver un dispositivo a presencia
inferida](../deployment/edge-services.md#demoting-a-device). Repara los dispositivos que ya están
mal; el reinicio sigue siendo lo que impide que los siguientes se estropeen.
