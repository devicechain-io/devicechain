---
title: Presencia de dispositivo
---

# Presencia de dispositivo

DeviceChain mantiene una señal de **presencia** en vivo para cada dispositivo: si está en línea ahora mismo, y cuándo se conectó, se desconectó o reportó actividad por última vez. La presencia forma parte del [último estado conocido](./architecture.md) de un dispositivo, la misma proyección que contiene sus mediciones más recientes. La ves en la pestaña **Conectividad** del dispositivo en la consola.

Cómo decide DeviceChain que un dispositivo está en línea depende del transporte, así que esta página empieza por ahí.

## Dos formas de conocer la presencia {#two-ways-presence-is-known}

Todo dispositivo lleva una **fuente de presencia** (presence source) que indica cómo se determina su estado en línea/fuera de línea.

**Inferida** es la opción predeterminada. El transporte no le da a DeviceChain ninguna señal explícita de conexión o desconexión, así que la presencia se infiere a partir de la actividad. Un dispositivo se considera en línea mientras está enviando datos. Si permanece en silencio más tiempo que su **tiempo de espera de inactividad** (inactivity timeout), un barrido en segundo plano lo marca como fuera de línea. Es el modelo adecuado para transportes sin conexión, como HTTP simple y CoAP.

**Afirmada** (asserted) significa que el transporte le indica a DeviceChain explícitamente cuándo un dispositivo se conecta y se desconecta, de modo que la presencia es autoritativa en lugar de deducida. La primera vez que llega una señal de este tipo para un dispositivo, DeviceChain cambia ese dispositivo a la fuente afirmada. A partir de entonces:

- Su estado en línea/fuera de línea se rige únicamente por señales explícitas de conexión/desconexión. Un paquete de datos suelto nunca puede marcar como en línea a un dispositivo después de que la plataforma haya sido informada de que está fuera de línea.
- El barrido de inactividad lo deja en paz. Un dispositivo afirmado que queda en silencio no se da por muerto, porque en un transporte cuyo cometido es reportar la muerte de forma explícita, el silencio no es evidencia de muerte. Mezclar ambos modos permitiría marcar como fuera de línea a un dispositivo que reporta con un intervalo largo mientras la plataforma ha sido informada de que está conectado.

Un dispositivo sigue siendo inferido hasta que un transporte que afirma presencia produce una señal para él. Los dispositivos existentes no se ven afectados, salvo que empiecen a llegar por un transporte que afirme presencia.

### Transportes que afirman presencia {#transports-that-assert-presence}

Hoy tres transportes afirman presencia:

- **MQTT simple**, para dispositivos conectados al propio broker de DeviceChain. El broker ya sabe el momento en que una conexión se abre y se cierra, y DeviceChain lo lee directamente. Consulta [MQTT en el broker de la plataforma](#mqtt-on-the-platform-broker).
- **[Sparkplug-B](./sparkplug.md)**, cuyos mensajes BIRTH y DEATH son exactamente estas señales explícitas de conexión/desconexión.
- **[LwM2M](./lwm2m.md)**, cuyo ciclo de vida de registro hace lo mismo: registro, actualización periódica y baja de registro (o un tiempo de vida vencido).

### Un dispositivo afirmado no tiene red de seguridad por inactividad {#no-inactivity-backstop}

Omitir el barrido de inactividad es deliberado, y tiene una consecuencia que debes tener prevista: **un dispositivo afirmado no tiene red de seguridad por inactividad.** Su señal de desconexión solo puede venir del transporte. Si esa señal nunca llega, el dispositivo sigue apareciendo en línea sin nada que lo corrija. Dos ejemplos:

- Un certificado de muerte (death) de Sparkplug perdido junto con la conexión.
- Un dispositivo LwM2M cuyo tiempo de vida de registro aún no ha vencido. El tiempo de vida predeterminado del propio LwM2M es de 86400 segundos, un día completo.

Qué vigilar, y cómo acotar esa ventana, se explica en [Cómo operar los servicios de borde](../deployment/edge-services.md). Si el transporte que habría reportado la desconexión ha desaparecido para siempre y no solo está en silencio, [devuelve el dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device) para que vuelva a ser posible corregirlo.

### MQTT en el broker de la plataforma {#mqtt-on-the-platform-broker}

La presencia MQTT no requiere ninguna cooperación del dispositivo. No tiene que publicar un mensaje de nacimiento, definir un last will ni anunciarse de ninguna manera.

Lo que hay que equipar es la instancia, no el dispositivo. La parte de `event-sources` que lee las conexiones del broker, la **toma del broker** (broker tap, o simplemente «la toma» en lo que sigue), necesita dos cosas:

- una credencial de cuenta de sistema de NATS
- una fuente de eventos que apunte al propio broker de esta instancia

Hasta que la instancia tenga ambas, la toma permanece apagada y los dispositivos MQTT siguen siendo inferidos. `dcctl bootstrap` acuña esa credencial y configura esa fuente, así que una instancia levantada de ese modo afirma la presencia MQTT sin trabajo adicional. Un `helm install` a secas deja la credencial vacía y no lo hace. [Confirmar que la toma del broker está en marcha](#confirming-the-tap) te dice cuál de los dos casos tienes.

Una instancia que *sí tuvo* toma y luego pierde esa credencial es un caso distinto de una que nunca la tuvo. Los dispositivos que ya había afirmado se quedarían congelados en lo último que reportaron, así que la instancia los devuelve a presencia inferida. Consulta [devolver un dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device).

Dos detalles más importan antes de construir sobre la presencia MQTT:

- **Sigue la conexión principal del dispositivo.** Un dispositivo puede abrir conexiones adicionales añadiendo su propio sufijo a su identificador de cliente, como en el flujo de trabajo de dos terminales `mosquitto_sub` / `mosquitto_pub`. La presencia ignora deliberadamente esas conexiones adicionales y sigue la sesión principal, de modo que cerrar una conexión secundaria nunca hace que un dispositivo conectado aparezca como fuera de línea. Un dispositivo que solo se conecta con un identificador de cliente con sufijo no se afirma en absoluto y sigue siendo inferido.
- **Cubre los dispositivos del broker de esta instancia.** DeviceChain no puede observar las conexiones de un broker que operes tú mismo, así que un dispositivo que llega a DeviceChain a través de uno sigue siendo inferido.

### Reparar desconexiones MQTT perdidas {#repairing-missed-mqtt-disconnects}

Para los dispositivos MQTT, DeviceChain cierra por sí mismo la brecha de la falta de red de seguridad. Hay un caso en el que el broker no puede avisarte de que un dispositivo se ha ido: cuando el broker se reinicia, las conexiones que mantenía desaparecen y no se anuncia ninguna desconexión para ellas.

Por eso DeviceChain compara periódicamente la lista de conexiones vivas del broker con lo que cree, y corrige la diferencia en ambos sentidos:

- dispositivos que no sabía que estaban conectados
- dispositivos que cree conectados y que el broker no mantiene

Los dispositivos que se reconectan tras un reinicio del broker se corrigen con su propia reconexión. El resto se corrige en una comparación posterior, una que pueda dar cuenta de todo el clúster, como se describe a continuación y en la [advertencia sobre reducir el clúster](#resizing-the-broker-cluster).

La comparación se niega a marcar nada como fuera de línea salvo que pueda dar cuenta de **todos** los nodos del clúster del broker. Si un nodo va lento o está inaccesible, sus dispositivos faltan de la lista y parecen iguales que los dispositivos que realmente se han ido. Marcar por error como fuera de línea a un dispositivo vivo es el error más dañino, porque todo lo que depende de la presencia actúa en consecuencia: el dispositivo aparece fuera de línea en su pestaña Conectividad, y una [regla de Conectividad](./event-processing.md#condition-types) genera una alarma de desconexión para un dispositivo que era alcanzable todo el tiempo. En esa situación DeviceChain sigue marcando como en línea los dispositivos recién vistos, y espera a la siguiente pasada para decidir los que están fuera de línea.

### Dónde se muestra la fuente de presencia {#where-the-presence-source-shows}

La fuente de presencia se muestra allí donde cambia el significado de una lectura:

- **Consola.** La pestaña Conectividad nombra la fuente: *Reportado por el transporte* o *Inferido a partir de la actividad*. También distingue un dispositivo que el transporte reportó como **Desconectado** de otro que simplemente está **Fuera de línea**. Fuera de línea significa que no ha llegado nada recientemente, que es también exactamente el aspecto que tiene un dispositivo sano con un intervalo de reporte largo.
- **[MCP](./mcp.md).** La herramienta `get_device_state` devuelve `presenceSource` junto al estado, y le indica al asistente que no reporte como caído un dispositivo inferido inactivo.
- **API.** `presenceSource` es un campo del tipo `DeviceState` de `device-state`, que devuelve `ASSERTED` o `INFERRED`.

## Por qué importa la distinción {#why-the-distinction-matters}

La presencia inferida es cómoda pero lenta y ambigua. «Fuera de línea» solo significa «no ha hablado recientemente», lo que tarda en detectar una desconexión real y es ciego para dispositivos que reportan con un intervalo largo. La presencia afirmada es inmediata e inequívoca: una desconexión es una desconexión en el instante en que el transporte la reporta. Eso es lo que quieres para cualquier cosa sobre la que vayas a alarmar o actuar.

Como el modo es una marca explícita por dispositivo, un dispositivo en un transporte sin conexión conserva su comportamiento habitual de tiempo de espera, un dispositivo en un transporte consciente de la presencia obtiene la señal autoritativa, y ambos nunca interfieren entre sí.

:::note Estado
La presencia de dispositivo, tanto inferida como afirmada, está disponible. Tres transportes afirman presencia: MQTT simple en el propio broker de DeviceChain (solo una vez que la instancia tiene la credencial de cuenta de sistema descrita más arriba), [Sparkplug-B](./sparkplug.md) y [LwM2M](./lwm2m.md). Las reglas de detección pueden dispararse sobre flancos de conexión/desconexión; consulta [Reglas sobre la presencia](#rules-on-presence).
:::

## Reglas sobre la presencia {#rules-on-presence}

Una regla de detección puede dispararse directamente sobre un flanco de conexión/desconexión. La [condición de Conectividad](./event-processing.md#condition-types) genera una alarma en el instante en que llega una desconexión autoritativa, y la resuelve al reconectar. No hay tiempo de espera que ajustar.

- **Formulario de reglas.** El formulario de reglas de la consola la ofrece como el tipo **Conectividad**. No hay condición que redactar, porque el propio flanco de presencia es la señal. El formulario abre una regla de Conectividad existente como su propio tipo. Si una definición almacenada contiene algo que el formulario no puede modelar, el formulario te lo advierte antes de guardar en lugar de reemplazarla en silencio.
- **Lienzo de automatización.** El lienzo es la única superficie de autoría que todavía omite ese tipo. Se niega a abrir una regla de Conectividad y nombra el tipo no soportado.

La condición de Conectividad complementa la regla de Ausencia basada en tiempo de espera (muerte autoritativa frente a silencio inferido), y ambas están pensadas para usarse en conjunto.

Una desconexión autoritativa también actualiza el estado en vivo del dispositivo, de modo que la pestaña Conectividad muestra el dispositivo fuera de línea en el instante en que el transporte lo reporta.

## Cómo se opera la presencia {#running-it}

La presencia vale lo que vale la señal que hay detrás. Los dos transportes de borde que la afirman se ejecutan cada uno como una única réplica propietaria, lo que le da a la presencia algunas propiedades operativas que conviene entender antes de alarmar sobre ella: qué cuesta un relevo, por qué un dispositivo afirmado puede quedarse atascado en línea, y cómo acotarlo. [Cómo operar los servicios de borde](../deployment/edge-services.md) las cubre.

Las secciones siguientes cubren propiedades específicas de la presencia MQTT afirmada por el broker.

### Confirmar que la toma del broker está en marcha {#confirming-the-tap}

Leer conexiones del propio broker de DeviceChain necesita cuatro cosas. Si falta alguna, la toma **se niega a arrancar**. Registra el motivo y pone `presence_tap_off{reason}` para indicar cuál es. Los dispositivos MQTT que nunca fueron afirmados siguen siendo inferidos, lo que se ve exactamente igual que una instancia que nunca tuvo presencia afirmada, porque funcionalmente lo es. Los cuatro requisitos:

- `brokerPresence.enabled` no está puesto a `false`.
- Hay una credencial de cuenta de sistema de NATS configurada. `dcctl bootstrap` acuña una; que falte es la razón habitual de que una instancia montada a mano no tenga toma.
- Al menos una fuente de eventos apunta al propio broker de la plataforma. Sin ninguna, no hay avisos de conexión que leer.
- Las llamadas entre servicios están configuradas. Sin ellas la toma funcionaría sin ruta de reparación, así que se queda apagada deliberadamente en lugar de funcionar a medias: un dispositivo cuya desconexión el broker nunca anunció aparecería como conectado para siempre.

#### Cuando una toma que estaba en marcha se apaga {#when-a-running-tap-turns-off}

En una instancia que nunca tuvo toma, eso es todo. Una instancia que sí la tuvo tiene un segundo problema: los dispositivos ya marcados como afirmados conservan la presencia que tuvieran por última vez, porque un dispositivo afirmado está exento del barrido de inactividad y un evento de datos no puede cambiarlo.

Por los dos primeros motivos de arriba, un `enabled: false` escrito y una credencial de cuenta de sistema ausente, `event-sources` devuelve por sí mismo esos dispositivos a presencia inferida. Ambos son configuración que todas las réplicas leen igual, y eso es lo que hace seguro automatizar la liberación de una flota entera a partir de ellos.

**Un broker que la toma no consigue alcanzar también los libera**, por una razón más fuerte que la configuración. La pasarela MQTT por la que se conectan los dispositivos vive en ese mismo broker, así que mientras esté inalcanzable tampoco hay ningún dispositivo conectado a través de ella. La toma da treinta segundos a la conexión para establecerse antes de decidir. El disparador es, por tanto, medio minuto sin conexión con la cuenta de sistema, no un único intento fallido, tiempo suficiente para no confundir un broker que se está reiniciando junto a los servicios con uno que se ha ido.

**Perder esa conexión después de que la toma haya arrancado reinicia `event-sources`.** Si el broker cierra definitivamente la conexión de la toma una vez que esta está en marcha (por ejemplo, porque ya no acepta la credencial de la cuenta de sistema), `event-sources` falla su comprobación de actividad (liveness) y Kubernetes reinicia el pod. Una credencial rechazada llega a todas las réplicas a la vez, así que se reinician todos los pods de `event-sources`. La ingesta HTTP no está disponible mientras tanto; la telemetría MQTT la sigue guardando el broker y se procesa cuando vuelven. Si el pod reiniciado sigue sin poder iniciar sesión, la toma se apaga con el motivo `broker_unreachable`, libera los dispositivos que afirmaba como se describe aquí y sigue comprobando. Por eso ese motivo cubre también un broker que está en marcha pero rechaza la credencial.

Para este motivo en concreto, la espera de dos minutos antes de una liberación (consulta [devolver un dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device)) es una nueva comprobación, no un retraso, y esa diferencia impide que la liberación sobreviva a la caída que la provocó. Antes de cada pasada, la primera incluida, el servicio vuelve a conectarse a la cuenta de sistema. Si el broker responde, no se libera nada: el servicio termina y el pod se reinicia con una toma que arranca con normalidad. Así, un broker que vuelve produce un reinicio de pod, no una flota de dispositivos liberados. Las otras dos vías de liberación no pueden funcionar así, y tampoco lo necesitan, porque ambas son configuración que se lee una sola vez al arrancar. Un `enabled: false` escrito no tiene espera de dos minutos y empieza a liberar en unos treinta segundos, un retraso aleatorio que evita que las réplicas empiecen a la vez. Para una credencial ausente, lo que espera la espera de dos minutos es el pod de reemplazo que despliega un cambio de configuración.

En los tres casos restantes no se libera nada automáticamente:

- ninguna fuente apunta al broker de la plataforma
- no hay configuración de llamadas entre servicios
- una suscripción falla sobre una conexión que *sí* alcanzó el broker

`dcctl presence demote` es la salida en esos casos. Tanto la liberación automática como el comando manual se describen en [devolver un dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device).

#### Señales de que la toma no está en marcha {#signals-that-the-tap-is-not-running}

Dos señales te indican que la toma no está en marcha, y cubren fallos distintos.

`presence_tap_off{reason}` es la directa. Se pone a 1 en cada vía por la que la toma se niega a arrancar, con la etiqueta nombrando cuál. Responde a una pregunta que una flota en silencio deja sin respuesta de otro modo: una flota MQTT de larga vida legítimamente no emite avisos de conexión ni desconexión durante días, así que nada en el flujo ordinario de eventos de presencia distingue una instancia que está afirmando presencia de otra que en silencio nunca llegó a arrancar.

No cubre una toma que arrancó y luego dejó de funcionar, porque en ese caso nada se niega a arrancar. **`presence_canary_missed_total` cubre ese caso, y es el contador sobre el que alarmar.** El servicio abre su propia conexión MQTT una vez por minuto exclusivamente para que una toma que funciona tenga algo que observar. `presence_canary_observed_total` sube con una toma sana, y `presence_canary_missed_total` sube cuando la cadena está rota.

El canario funciona con su propio calendario, independiente de la pasada de reparación descrita más abajo. Esa separación es lo que hace fiable al contador: un instrumento que solo pudiera informar mientras aquello que vigila estuviera sano se quedaría callado justo en el momento en que importa.

Lee los demás contadores de presencia como tráfico, no como salud. `presence_events_total` está legítimamente plano en una flota en silencio. También está legítimamente *no* plano en una toma que se acaba de apagar, porque liberar los dispositivos que esa toma tenía afirmados emite un evento por dispositivo bajo `presence_events_total{state="demoted"}`. Ninguna de las dos formas dice nada sobre si la presencia se está leyendo.

### Cómo ajustar la toma {#broker-presence-settings}

La toma se distribuye con valores predeterminados que funcionan, y la mayoría de las instancias nunca los cambian. Los ajustes viven bajo la configuración `brokerPresence` del área `event-sources`.

| Ajuste | Predeterminado | Qué hace |
|---|---|---|
| `enabled` | activada si no se define | Ejecuta la toma. Ponlo en `false` para desactivar deliberadamente la presencia MQTT afirmada por el broker, por ejemplo en una instancia cuyo broker se comparte con algo que no admite un suscriptor de cuenta de sistema. Los dispositivos MQTT pasan entonces a presencia inferida, y los que la toma ya hubiera afirmado se liberan de vuelta a ella, a ritmo pausado, en los minutos siguientes. |
| `reconcileSeconds` | `300` | Cada cuánto se compara la lista de conexiones vivas del broker con la de la plataforma, en ambos sentidos. **Esto no es una red de seguridad.** Un reinicio ordenado del broker no anuncia desconexión alguna, así que esta pasada es lo único que llega a corregir esos dispositivos, y un dispositivo afirmado no tiene detrás ningún barrido de inactividad. Bájalo para reparar antes, a cambio de un inventario de todo el clúster más una lectura por inquilino en cada pasada. |
| `canarySeconds` | `60` | Cada cuánto el servicio abre su propia conexión MQTT para demostrar que la toma sigue viva. Es el calendario contra el que cuenta `presence_canary_missed_total`. |
| `canaryDeadlineSeconds` | `15` | Acota una sola sonda. Si es demasiado ajustado, informa de fallos que la toma no tiene. |
| `inventoryGatherSeconds` | `5` | Cuánto tiempo recoge una pasada las respuestas del clúster del broker. Si es demasiado corto, un nodo que solo va lento se lee como ausente, lo que retiene todas las desconexiones de esa pasada. |

Un valor no positivo en cualquiera de los cuatro intervalos recae en el predeterminado de arriba, no en cero.

### Una pasada de reparación que se queda sin tiempo lo dice {#reconcile-pass-timeout}

Cada pasada de reparación recorre todos los inquilinos de la instancia y lee los dispositivos afirmados de cada uno, así que en una instancia grande la pasada es larga. Está acotada: una pasada que no puede terminar dentro de su presupuesto se detiene, informa `presence_reconcile_runs_total{outcome="timeout"}` y registra cuántos inquilinos cubrió.

La siguiente pasada continúa entonces en el primer inquilino al que no llegó, en lugar de empezar otra vez desde el principio. Sin eso, una flota cuya pasada nunca cupiera en el presupuesto repararía los mismos primeros inquilinos en cada intento y nunca alcanzaría al resto: no tarde, nunca.

- Resultados `timeout` ocasionales significan que las reparaciones van con retraso, y cada inquilino sigue teniendo su turno.
- Una racha sostenida de ellos significa que la instancia necesita más margen. Los dispositivos al final de la rotación son aquellos cuyas desconexiones perdidas quedan sin corregir durante más tiempo.

`presence_reconcile_runs_total` lleva un resultado por pasada:

| Resultado | Qué significa |
| --- | --- |
| `complete` | se recorrieron todos los inquilinos contra un clúster de brokers íntegramente contabilizado |
| `partial` | la pasada se ejecutó, pero no respondieron todos los nodos del broker — solo se marcaron dispositivos **en línea**, nunca fuera de línea |
| `timeout` | la pasada agotó su presupuesto; los inquilinos a los que no llegó van primero la próxima vez |
| `failed` | la pasada no pudo leer nada — ni el inventario del broker, ni la lista de inquilinos, ni **el estado de presencia de ningún inquilino**. La reconciliación no hizo absolutamente nada |
| `cancelled` | el servicio se estaba deteniendo a mitad de pasada. No es un fallo |

Alarma sobre `failed` junto con el canario. Que falle la lectura del estado de un solo inquilino se tolera, y los demás inquilinos siguen teniendo su pasada. Que fallen todas las lecturas significa que las reparaciones se han detenido por completo, que es como se ve desde aquí una caída de device-state.

### Las transiciones de presencia se contabilizan contra el techo de ingesta del inquilino {#presence-and-the-ingest-ceiling}

Las transiciones de conexión y desconexión pasan por el mismo [límite de ingesta](./governance.md) por inquilino que la telemetría. **Se rechazan cuando un inquilino está en su techo**, y se cuentan en `presence_events_refused_total`.

Es deliberado. La rotación de conexiones la controla por completo el dispositivo y, por lo demás, es gratuita: un dispositivo reconectándose en bucle sería un amplificador de escrituras no medido que el limitador de ingesta nunca ve.

Conviene tener prevista la consecuencia. Un inquilino apretado contra su techo tiene dispositivos cuyo estado en línea/fuera de línea es incorrecto, y sigue siéndolo hasta que una pasada de reconciliación posterior lo repara (por defecto, hasta cinco minutos). Todo lo que depende de la presencia también es incorrecto durante esa ventana, incluidas las reglas de Conectividad y la liberación de comandos retenidos para un dispositivo fuera de línea.

**Una degradación pasa por la misma puerta.** A un inquilino apretado contra su techo se le puede rechazar la reparación junto con la rotación que causa la presión. No se pierde nada, porque una liberación rechazada deja el dispositivo afirmado y la siguiente pasada lo vuelve a encontrar, pero la reparación no llega antes de lo que el techo permite.

Esto se aplica a la toma MQTT del broker de la plataforma. La ingesta de Sparkplug no aplica ningún techo por inquilino y no descarta nada, y LwM2M usa su propio límite, configurado por separado.

### Reducir el clúster del broker exige reiniciar `event-sources` {#resizing-the-broker-cluster}

La comparación de reparación se niega a marcar nada como fuera de línea si no puede dar cuenta de todos los nodos del clúster del broker. Decide qué significa «todos los nodos» a partir del clúster más grande que ha visto jamás. Esa marca solo sube, y eso es lo que impide que una partición de red provoque desconexiones falsas masivas: un broker aislado por rutas se declara a sí mismo como el clúster entero y, de otro modo, cumpliría su propia comprobación.

El coste es un caso que el diseño no puede distinguir: **reducir el clúster del broker a propósito**. Responden menos nodos que el máximo recordado, así que toda pasada posterior se trata como incompleta y nunca se hace ninguna reparación de desconexión, no solo hasta la pasada siguiente sino durante toda la vida del proceso. Los dispositivos huérfanos del nodo retirado aparecen en línea indefinidamente y, como un dispositivo afirmado no tiene red de seguridad por inactividad, ningún tiempo de espera los corrige.

**Tras reducir el clúster de NATS, reinicia `event-sources`.** Si hacía falta y no lo hiciste, `presence_reconcile_withheld_disconnects_total` sube sin estabilizarse. Ampliar el clúster no requiere nada.

Si el reinicio tiene que esperar, los dispositivos huérfanos no tienen por qué hacerlo. Ejecuta `dcctl presence demote` sobre esa fuente para devolverlos a presencia inferida, donde el barrido de inactividad de diez minutos puede marcarlos fuera de línea con su propia evidencia. Consulta [devolver un dispositivo a presencia inferida](../deployment/edge-services.md#demoting-a-device). La degradación repara los dispositivos que ya están mal; el reinicio sigue siendo lo que impide que los siguientes se estropeen.
