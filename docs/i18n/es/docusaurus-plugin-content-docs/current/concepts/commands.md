---
title: Comandos
---

# Comandos

El envío de comandos en DeviceChain es **bidireccional y persistente**. Un comando que
emites se valida contra el contrato de capacidades del dispositivo, se almacena, se
despacha solo cuando el dispositivo puede recibirlo realmente, y se rastrea hasta que el
dispositivo informa qué ocurrió o hasta que vence su tiempo de vida. Nada de esto se
envía y se olvida.

Para emitir uno, consulta [Enviar un comando](../guides/sending-commands.md).

## El contrato de capacidades {#commands-and-the-capability-contract}

Un perfil de dispositivo puede declarar los **comandos** que sus dispositivos aceptan, cada uno con un
esquema de parámetros tipado (nombre, tipo de dato, obligatorio, mín/máx, enum). Esas declaraciones son
lo que hace que el perfil sea un contrato y no una simple etiqueta.

Cuando se pone en cola un comando, se valida contra la versión **publicada** del perfil — no
el borrador. Hay tres resultados posibles:

- **El perfil no declara comandos.** Se acepta cualquier cosa. Declarar un vocabulario es
  opcional, así que un perfil que no ha adoptado uno sigue funcionando exactamente como antes.
- **El perfil declara comandos, y la clave coincide con uno.** El payload se valida
  contra el esquema de parámetros de ese comando: se rechazan los parámetros desconocidos, los tipos
  incorrectos, los valores fuera de rango y los parámetros obligatorios faltantes.
- **El perfil declara comandos, y la clave no coincide con ninguno.** Se rechaza — no se puede
  enviar a un dispositivo un comando que su contrato de capacidades no incluye.

Las claves de comando se comparan de forma **exacta**, incluyendo mayúsculas/minúsculas. Una clave con
mayúsculas/minúsculas incorrectas es una actuación mal referenciada, que es justo lo que esta validación
existe para evitar.

La validación lee deliberadamente el snapshot publicado. Una definición que has creado pero
aún no publicado no se ha comunicado a nada aguas abajo, así que aplicarla
rechazaría comandos que el dispositivo en realidad sí acepta. Publica el perfil para que un nuevo comando
entre en vigor.

El vocabulario publicado se puede leer, no solo aplicar: un dispositivo informa qué comandos
acepta actualmente, y la consola usa eso para ofrecerlos directamente — un selector de
comandos declarados y un formulario tipado construido a partir del esquema de parámetros del comando
seleccionado, en lugar de un cuadro de texto libre. Un perfil que no declara comandos igual obtiene el
formulario de texto libre, coincidiendo con lo que la plataforma aceptará. Los comandos que has creado
pero no publicado se muestran junto al selector como no disponibles, de modo que un comando faltante se
lee como "aún no publicado" en lugar de como una funcionalidad ausente.

## Ciclo de vida del comando {#command-lifecycle}

Un comando emitido se persiste y se rastrea, no es de tipo "disparar y olvidar" (fire-and-forget). Pasa por:

Estos estados significan que el comando aún no ha terminado:

- **`QUEUED`** — aceptado y validado, esperando su primera decisión de despacho. Es
  genuinamente transitorio: un comando no se queda aquí.
- **`HELD`** — la plataforma está reteniendo el comando deliberadamente porque sabe que el
  dispositivo está ausente. Aquí es donde se acumula el pendiente de una flota desconectada,
  y puede permanecer durante días. Un comando retenido cuenta como en tránsito: todavía se
  puede cancelar, y un TTL que vence sobre él registra `EXPIRED` y no `TIMEOUT`, porque el
  comando nunca llegó a salir. Vuelve a `QUEUED` cuando el dispositivo regresa.
- **`SENT`** — publicado en el topic de comandos propio del dispositivo, esperando su
  respuesta. Léelo como *despachado hacia un dispositivo que la plataforma creía activo*, no
  como prueba de que el dispositivo lo tenga: un dispositivo que se cae entre la comprobación
  de presencia y la publicación acaba igualmente aquí. Un comando que permanece aquí varios
  minutos sin desenlace puede ser uno que ya nadie tiene en su poder — ver [Cuando la
  plataforma pierde el rastro de un comando](#stranded-commands).
- **`PARKED`** — se publicó, el transporte no encontró ninguna conexión viva con el
  dispositivo, y la plataforma sigue teniéndolo en su poder. Este es el estado del durmiente,
  y el comando se entrega en el próximo despertar del dispositivo. Igual que `HELD`, cuenta
  como en tránsito: todavía se puede cancelar, y un TTL que vence sobre él registra `EXPIRED`
  y no `TIMEOUT`, porque el comando nunca llegó a un dispositivo. Ver [Estar registrado no es
  lo mismo que ser alcanzable](#parked-commands).

Estos son terminales, y nada sale de un estado terminal:

- **`SUCCESSFUL`** / **`FAILED`** — el dispositivo informó el resultado.
- **`TIMEOUT`** — se despachó y el dispositivo nunca respondió.
- **`EXPIRED`** — su TTL transcurrió antes de que llegara a ningún dispositivo.
- **`CANCELLED`** — un operador o un inquilino lo canceló.

`EXPIRED` y `TIMEOUT` responden preguntas distintas, y confundir uno con el otro te hace
buscar en el lugar equivocado: `EXPIRED` significa que el comando nunca llegó a un
dispositivo, así que una racha de ellos indica que las entregas no están llegando;
`TIMEOUT` significa que uno sí salió y no volvió nada, lo que apunta al dispositivo.

### Comandos para un dispositivo ausente {#commands-to-a-device-that-is-away}

Sobre MQTT un comando solo llega a un dispositivo que esté conectado y suscrito en ese
preciso instante: el broker no lo retiene para que lo recoja más tarde. Por eso un comando
publicado hacia un dispositivo ausente simplemente se pierde, y la plataforma no tenía forma
de saberlo: registraba el comando como enviado, nada respondía y una semana después figuraba
como `TIMEOUT` — un registro permanente que culpaba a un dispositivo al que nunca se le
entregó el comando.

Por eso la plataforma comprueba antes de publicar. Cuando un transporte informa de que un
dispositivo no está conectado, sus comandos pasan a `HELD` en lugar de publicarse, y vuelven
a `QUEUED` cuando el dispositivo regresa — normalmente uno o dos segundos después de que se
reconecte, porque lo comunica directamente el transporte que posee la conexión. Una pasada
periódica vuelve a contrastar el conjunto retenido con lo que la plataforma cree actualmente
de cada dispositivo, de modo que el pendiente igual se libera si ese aviso se pierde. Ambas
vías dependen de que la plataforma se entere de que el dispositivo volvió: es el registro de
presencia al cambiar lo que libera la retención, y el aviso solo hace que ocurra antes.

Conviene conocer cuatro límites:

- **La comprobación necesita un transporte que informe de las conexiones.** Para un
  dispositivo cuyo transporte solo transporta datos, "sin eventos recientes" no es prueba de
  que el dispositivo no pueda recibir: un dispositivo que informa cada hora está en silencio
  59 de cada 60 minutos y es alcanzable durante todo ese tiempo. Esos comandos se despachan
  como antes.
- **Es una comprobación, no una cola.** Un dispositivo que se desconecta entre la
  comprobación y la publicación pierde el comando igualmente. Lo que se elimina es el caso
  que la plataforma sí podía prever. Un transporte que mantiene su propia cola para un
  dispositivo dormido se trata aparte — ver [Estar registrado no es lo mismo que ser
  alcanzable](#parked-commands).
- **Los comandos a dispositivos Sparkplug no se entregan en absoluto.** La ruta no está
  construida: los nodos Sparkplug viven en tu propia infraestructura MQTT y no en la de la
  plataforma, y nada une ambas. Un comando emitido a uno de ellos se registra como `FAILED`
  de inmediato, con ese motivo, en lugar de retenerse a la espera de un regreso que no
  serviría de nada.
- **Una retención sobrevive al transporte que la causó.** Solo el transporte que informó de
  la ausencia del dispositivo puede informar de su regreso, así que si ese transporte deja
  de ejecutarse la acumulación espera hasta que cada comando caduque por su cuenta.
  [Devolver el dispositivo a presencia
  inferida](../deployment/edge-services.md#demoting-a-device) la libera: la retención
  depende de que se haya informado de la ausencia del dispositivo, y de un dispositivo
  inferido no se informa.

Por eso una racha de `TIMEOUT` contra dispositivos que sabes que son intermitentes
sigue mereciendo leerse como algo que habla de cuándo estuvieron conectados y no de su
firmware — pero ahora debería ser una racha mucho más corta.

### Estar registrado no es lo mismo que ser alcanzable {#parked-commands}

La comprobación de presencia pregunta si un dispositivo está **registrado**. Para un
dispositivo que duerme por diseño — un dispositivo [LwM2M](./lwm2m.md) en modo cola — esa es
una pregunta distinta de si se le puede alcanzar ahora mismo. Está registrado, así que la
comprobación pasa y el comando se publica; el transporte no encuentra entonces ninguna
conexión viva y el comando no llega a ninguna parte.

Ese es el mismo defecto que el anterior, una capa más abajo, y solía terminar igual. El
comando se quedaba en `SENT`, que también significa «la plataforma se lo entregó al
dispositivo», de modo que un único estado cargaba con dos significados opuestos — y una
semana después el registro figuraba como `TIMEOUT`, culpando a un dispositivo al que nunca se
le entregó el comando.

Un comando así ahora registra **`PARKED`**: publicado, sin nadie que lo reciba, y todavía en
poder de la plataforma para entregarlo en el próximo despertar del dispositivo. De que la
plataforma siga teniéndolo se derivan tres consecuencias, y cada una es el motivo del cambio:

- **Un TTL vencido registra `EXPIRED`, no `TIMEOUT`.** El comando nunca llegó a un
  dispositivo, así que el registro lo dice en lugar de culpar al dispositivo por no responder.
- **Se puede cancelar** — por sí solo o como parte de una escritura de flota — porque la
  plataforma lo sigue teniendo en su poder, y cancelarlo evita que salga en el próximo
  despertar. Eso no garantiza que la operación nunca se ejecutara: la devolución puede ser el
  reintento de una publicación que sí llegó al dispositivo antes de que su conexión se
  cortara, así que interpreta una cancelación como «ya no se volverá a entregar», no como
  «nunca se llevó a cabo». Anular una escritura de flota reportaba estos comandos como ya
  enviados y por tanto irrecuperables, cuando la plataforma todavía los tenía en su poder.
- **Cuenta contra el techo de comandos no entregados del inquilino**, exactamente igual que
  `QUEUED` y `HELD`. Es trabajo que la plataforma sigue cargando.

### Cuando la plataforma pierde el rastro de un comando {#stranded-commands}

La devolución solo funciona mientras algo siga teniendo el comando en su poder. Un comando
también puede llegar a `SENT` y luego no ser alcanzado por nada en absoluto: el pod que lo
publicó muere antes de poder registrar el desenlace, un transporte se rinde tras agotar sus
reintentos, o una instancia funciona sin la pieza que realiza la devolución. Nada va mal en
el dispositivo, y nada va mal en el comando. Simplemente ya no tiene dueño, y `SENT` no tiene
ninguna salida que no exija uno — salvo el TTL, que registra `TIMEOUT`.

Esa es la misma etiqueta errónea que `PARKED` existe para eliminar, llegando por otro camino,
así que la plataforma la cierra de la misma manera. Un proceso en segundo plano busca comandos
que lleven en `SENT` sin desenlace más tiempo del que la plataforma podría haber seguido
trabajando en ellos — derivado del propio presupuesto de reintentos de la capa de mensajería,
unos cinco minutos y medio hoy, no una cifra elegida a mano. Los que puede rearmar con
seguridad pasan a `PARKED`, y se entregan en el próximo despertar del dispositivo como
cualquier otro comando aparcado.

Vale la pena conocer dos límites, porque ambos son deliberados:

- **Solo se aplica a dispositivos LwM2M.** `PARKED` afirma que un comando no llegó a nada, y
  para un dispositivo sobre MQTT simple la plataforma no puede decir eso honestamente: un
  comando MQTT se entrega en vivo a quien esté conectado en ese instante, así que un comando
  que parece no haber llegado a ninguna parte es indistinguible de uno que sí llegó y cuya
  respuesta se perdió. Para esos, el comportamiento no cambia.
- **Rearmar acepta que un comando pueda ejecutarse dos veces.** Un comando sin desenlace
  registrado no es lo mismo que un comando que nunca se llevó a cabo: el dispositivo pudo
  actuar y perderse el informe. Rearmar sigue siendo la mejor respuesta, porque la alternativa
  es un `TIMEOUT` garantizado sobre un comando que la plataforma realmente nunca entregó,
  escrito contra un dispositivo que no hizo nada mal. Interprétalo con la misma garantía de
  al-menos-una-vez que se aplica a una devolución: lee un comando rearmado como «se
  entregará», no como «no se había llevado a cabo».

### Cuánta acumulación puede retener un inquilino {#held-command-ceiling}

Una acumulación de comandos no entregados se drena de tres maneras y de ninguna otra: el
dispositivo vuelve o despierta, vence el TTL del comando y este registra `EXPIRED`, o alguien
anula un comando y este registra `CANCELLED`. Para una flota que permanece apagada y a la que
nadie toca, eso significa que la acumulación se queda ahí hasta el horizonte del TTL.
Por eso está acotada por inquilino, y la cota es un número real en todos los niveles —
**ningún ajuste significa «ilimitado»**. Una acumulación sin cota es un crecimiento del
almacenamiento durable provocado por el inquilino e invisible para el operador.

El límite se resuelve por una cascada: la anulación propia del inquilino si la tiene, si no
la de su nivel, si no el valor predeterminado de la plataforma: **10 000**. A un inquilino
que ya está en su límite se le rechaza el siguiente comando con el código
`HELD_CEILING_EXCEEDED`.

#### Una parte del techo está reservada para la entrega {#delivery-machinery-reserve}

No todo ese techo está a tu disposición. Una parte — el **20 % de forma predeterminada**, es
decir 2000 de los 10 000 de la plataforma — se guarda para la entrega de comandos de la
propia plataforma, y solo la plataforma dispone de ella. Todo lo que emite comandos en tu
nombre queda acotado por el resto: la consola, los SDK, `dcctl` y tus propias integraciones
por igual.

Esto existe por lo que puede provocar una escritura de flota. «Reinicia todas las bombas» es
una sola petición legítima capaz de llenar el techo entero de golpe, y a partir de ese momento
se rechaza cada comando que tus reglas de automatización intentan enviar — hasta que la
acumulación se drene, lo que para una flota apagada puede significar días. La reserva mantiene
en marcha tu automatización basada en alarmas mientras una escritura de flota está en vuelo.

Se aplica igual tanto si envías un comando como diez mil: un lote se admite hasta el mismo
límite que alcanzaría un bucle de comandos individuales, así que no hay forma de eludirla ni
ventaja en ninguna de las dos formas. Consulta [Un comando, muchos
dispositivos](#command-batches).

Un rechazo nombra el límite que realmente se aplicó y, cuando es la reserva la que te ha
acotado, indica cuánto se apartó — de modo que quien reciba un rechazo en 8000 frente a un
techo visible de 10 000 pueda distinguir ambas cifras. La reserva es un ajuste del operador,
no del inquilino: no se puede subir ni bajar por inquilino.

:::info Acota el trabajo no entregado, no solo el retenido
La cuenta abarca todo comando en **`QUEUED`, `HELD` o `PARKED`** — no solo los retenidos para
dispositivos ausentes. Un inquilino cuya flota está entera y presente igualmente puede ser
rechazado, únicamente por volumen de emisión en vuelo. Los comandos en cola se drenan en un
ciclo, así que su aporte en régimen estacionario es aproximadamente un ciclo de ritmo de
emisión: poco, pero no cero, y un inquilino que emite cerca de su techo a alta frecuencia lo
notará. La cota es sobre trabajo no entregado, y no entregado es no entregado.
:::

Ese rechazo es el **único temporal** que produce la ruta de admisión. Cualquier otro rechazo
describe una petición que estará igual de mal en el siguiente intento; este se resuelve solo
conforme esos comandos salen. Es el único código que vale la pena reintentar, y el resto vale
la pena mostrarlos a una persona. Consulta
[Enviar un comando](../guides/sending-commands.md#when-an-enqueue-is-refused).

Cancelar un comando registra `CANCELLED`. La cancelación y la expiración del TTL compartían
hasta hace poco el único valor `EXPIRED`, así que los comandos cancelados antes de ese
cambio siguen apareciendo como `EXPIRED`; ambos conviven en los datos históricos, y como
nada registró qué filas `EXPIRED` provenían de una cancelación, no es posible distinguirlas
a posteriori.

**Solo el dispositivo puede informar éxito.** `SUCCESSFUL` y un `FAILED` informado por el
dispositivo son los dos desenlaces que solo él puede producir; cualquier otro terminal lo
escribe la plataforma por su cuenta — `TIMEOUT`, `EXPIRED`, `CANCELLED`, o un `FAILED`
registrado porque el transporte no puede llevar el comando en absoluto. Informar el resultado
es la mitad del contrato que le corresponde al dispositivo — ver
[Responder a un comando](../guides/connecting-a-device.md#responding-to-a-command). Un
dispositivo que nunca responde deja sus comandos en `SENT` hasta que su TTL los convierte
en `TIMEOUT` — salvo en LwM2M, donde un comando que queda sin desenlace durante varios
minutos se rearma para el próximo despertar del dispositivo, como se describe en [Cuando la
plataforma pierde el rastro de un comando](#stranded-commands). Todo comando lleva un TTL — el que definas con `expiresAt`, o el
predeterminado de la plataforma de siete días —, así que define el tuyo si tus dispositivos
no informan resultados y una semana es más de lo que el comando sigue siendo útil.

Cada dispositivo recibe comandos en un topic acotado exclusivamente a ese dispositivo, y está autorizado
solo para ese topic — un dispositivo no puede observar comandos dirigidos a ningún otro dispositivo de
su inquilino.

## Un comando, muchos dispositivos {#command-batches}

Un **lote de comandos** difunde un solo comando a muchos dispositivos como una única
operación registrada. Los dispositivos se nombran explícitamente o se resuelven a partir de
un **grupo de entidades**, y todos reciben la misma clave de comando y la misma carga útil.
Para emitir uno, consulta [Comandar una flota](../guides/commanding-a-fleet.md).

Todo lo anterior sigue aplicándose por dispositivo: cada comando se valida contra el contrato
de capacidades de ese dispositivo, se retiene si el dispositivo está ausente, se sigue por el
mismo ciclo de vida y queda acotado por el mismo TTL. Un lote no cambia nada de lo que le
ocurre a un comando — cambia lo que la plataforma *recuerda* sobre la operación en conjunto.

Ese registro es justamente el punto. Un dispositivo rechazado no recibe fila de comando
alguna — no existe un estado que signifique «se quiso pero no se creó» — así que sin el
registro del lote un rechazo no dejaría rastro, y quien dispara una escritura de flota y
vuelve por la mañana no tendría nada que leer. El registro conserva a cuántos dispositivos
resolvió el objetivo, cuántos se admitieron realmente, y cuáles fueron rechazados y por qué.

Tres cosas lo distinguen de un bucle de comandos individuales, y ninguna es comodidad:

- **Un objetivo de grupo queda congelado en el momento del disparo.** El lote resuelve la
  membresía **publicada** del grupo — nunca un selector en borrador — y registra la versión
  del grupo contra la que resolvió. Editar el grupo después no puede cambiar lo que ya salió,
  y una auditoría todavía puede responder qué *significaba* el grupo cuando el lote se
  disparó.
- **Una difusión parcial es una decisión, no un valor por defecto.** En una flota real
  algunos dispositivos no podrán recibir el comando. Quien llama debe declarar si eso es
  aceptable: si no lo es, el lote entero se rechaza y **no se crea nada**, ni siquiera el
  registro, porque no ocurrió nada. Si lo es, los dispositivos que pueden recibir el comando
  lo reciben y el resto quedan registrados como rechazos.
- **Se puede anular como una sola operación.** Deshacer un bucle implica cancelar cada comando
  por separado, y conservar todos los tokens con los que se emitieron.

Las cifras del registro —cuántos dispositivos resolvieron, cuántos se admitieron— describen
**el momento en que el lote se disparó**, no el presente. Las filas de comando no son
inmortales, así que una cuenta en vivo podría derivar por debajo de la verdad del momento de
creación sin ningún rechazo que explique la diferencia. Las preguntas en presente («de los
5000 en cola, ¿cuántos han salido?») se responden buscando los comandos que creó el lote, no
releyendo el lote.

Los rechazos se almacenan de dos formas, y por la misma razón por la que una difusión grande
necesita ambas: una lista individual, acotada para que una escritura de flota no pueda
almacenar un blob sin límite, y totales completos por código que nunca se truncan. Los
totales son lo que hace que el registro se audite solo — los dispositivos resueltos siempre
equivalen a los admitidos más la suma de los recuentos de rechazo, incluso cuando la muestra
nombrada se queda corta.

### Cancelar un lote detiene lo que aún no ha salido {#cancelling-a-batch}

Cancelar un lote mueve sus comandos de `QUEUED`, `HELD` o `PARKED` a `CANCELLED`. Los
comandos que ya están `SENT` se dejan en paz, y los dispositivos que los recibieron actuarán
igualmente sobre ellos.

`SENT` es la línea, y se traza en un solo sitio: la plataforma anula lo que todavía tiene en
su poder y deja en paz lo que ya ha puesto en camino. Un comando `SENT` se despachó hacia
un dispositivo que se creía activo, así que cancelarlo no revoca nada — lo único que haría es
que la plataforma dejara de esperar la respuesta de ese dispositivo, sustituyendo un
desenlace real por un registro que dice que un operador lo anuló. Haz eso a escala de flota y
los dispositivos actúan, las respuestas se descartan, y el registro dice que la operación se
anuló.

Cancelar un comando **individual** traza exactamente la misma línea: `QUEUED`, `HELD` y
`PARKED` se cancelan, y un comando `SENT` se devuelve sin cambios en lugar de llevarse a
`CANCELLED`. Ver [Cancela uno](../guides/sending-commands.md#cancel-one).

Cancelar un lote nunca se rechaza. Un freno que se negara a actuar porque parte de la flota
ya se movió dejaría comandada al resto de la flota, que es el peor desenlace disponible — así
que siempre actúa e informa de lo que alcanzó. El registro del lote queda sellado con cuándo
se canceló y cuántos comandos alcanzó esa llamada, y el sello es de **primero que llega**:
una segunda cancelación no sobrescribe lo que registró la primera.
