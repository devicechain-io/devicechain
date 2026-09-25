---
title: Comandos
---

# Comandos

Los comandos en DeviceChain son **bidireccionales y persistentes**. Un comando que emites se
valida contra el contrato de capacidades del dispositivo y se almacena. Se despacha solo cuando el
dispositivo puede recibirlo realmente, y se rastrea hasta que el dispositivo informa qué ocurrió o
hasta que vence el tiempo de vida (TTL) del comando. Aquí nada se envía y se olvida.

Para emitir uno, consulta [Enviar un comando](../guides/sending-commands.md).

## El contrato de capacidades {#commands-and-the-capability-contract}

Un perfil de dispositivo puede declarar los **comandos** que aceptan sus dispositivos. Cada uno
tiene un esquema de parámetros tipado: nombre, tipo de dato, obligatorio, mín/máx y enum. Esas
declaraciones hacen que el perfil sea un contrato y no una simple etiqueta.

Cuando se pone en cola un comando, se valida contra la versión **publicada** del perfil, no contra
el borrador. Hay tres resultados posibles:

| El perfil… | Resultado |
| --- | --- |
| no declara comandos | Se acepta cualquier cosa. Declarar un vocabulario es opcional, así que un perfil que no ha adoptado uno sigue funcionando exactamente como antes. |
| declara comandos, y la clave coincide con uno | El payload se valida contra el esquema de parámetros de ese comando. Se rechazan los parámetros desconocidos, los tipos incorrectos, los valores fuera de rango y los parámetros obligatorios que faltan. Un comando que no declara parámetros acepta cualquier payload JSON bien formado. |
| declara comandos, y la clave no coincide con ninguno | Se rechaza. No se puede enviar a un dispositivo un comando que su contrato de capacidades no incluye. |

Las claves de comando se comparan de forma **exacta**, incluidas mayúsculas y minúsculas. Una clave
con las mayúsculas mal puestas es una actuación mal referenciada, que es justo lo que esta
validación existe para evitar.

La validación lee el snapshot publicado a propósito. Una definición que has creado pero no
publicado no se ha comunicado a nada aguas abajo, así que aplicarla rechazaría comandos que el
dispositivo en realidad acepta. Publica el perfil para que un comando nuevo entre en vigor.

También puedes leer el vocabulario publicado. La plataforma lista los comandos que un dispositivo
acepta actualmente, resueltos a partir de la versión publicada de su perfil mediante la misma
búsqueda que usa la comprobación al poner en cola. El propio dispositivo no informa nada aquí. La
consola usa esa lista para ofrecer los comandos directamente: un selector de comandos declarados y
un formulario tipado construido a partir del esquema de parámetros del comando seleccionado, en
lugar de un cuadro de texto libre. Un perfil que no declara comandos sigue obteniendo el formulario
de texto libre, en coherencia con lo que la plataforma aceptará.
Los comandos que has creado pero no publicado se muestran junto al selector como no disponibles, de
modo que un comando que falta se lee como «aún no publicado» y no como una funcionalidad ausente.

## Ciclo de vida del comando {#command-lifecycle}

Un comando emitido se persiste y se rastrea a través de un conjunto de estados.

Estos estados significan que el comando aún no ha terminado:

- **`QUEUED`** — aceptado y validado, a la espera de su primera decisión de despacho. Es realmente
  transitorio: un comando no se queda aquí.
- **`HELD`** — la plataforma retiene el comando deliberadamente porque sabe que el dispositivo está
  ausente. Aquí es donde se acumula el pendiente de una flota desconectada, y puede permanecer
  durante días. Un comando retenido cuenta como en tránsito: todavía puedes cancelarlo, y un TTL
  que vence sobre él registra `EXPIRED` y no `TIMEOUT`, porque el comando nunca llegó a salir.
  Vuelve a `QUEUED` cuando el dispositivo regresa.
- **`SENT`** — publicado en el topic de comandos propio del dispositivo, a la espera de su
  respuesta. Léelo como *despachado hacia un dispositivo que la plataforma creía activo*, no como
  prueba de que el dispositivo lo tiene. Un dispositivo que se cae entre la comprobación de
  presencia y la publicación acaba igualmente aquí. Un comando que se queda aquí varios minutos sin
  desenlace puede ser uno que ya nadie tiene en su poder; consulta
  [Cuando la plataforma pierde el rastro de un comando](#stranded-commands).
- **`PARKED`** — se publicó, el transporte no encontró ninguna conexión viva con el dispositivo, y
  la plataforma sigue teniéndolo en su poder. Es el estado de un dispositivo dormido, y el comando
  se entrega en el próximo despertar del dispositivo. Igual que `HELD`, cuenta como en tránsito:
  todavía puedes cancelarlo, y un TTL que vence sobre él registra `EXPIRED` y no `TIMEOUT`, porque
  el comando nunca llegó a un dispositivo. Consulta
  [Estar registrado no es lo mismo que ser alcanzable](#parked-commands).

Estos estados son terminales. Nada sale de un estado terminal.

- **`SUCCESSFUL`** / **`FAILED`** — el dispositivo informó el resultado. La plataforma también
  registra `FAILED` cuando no puede completar un comando ella misma:
  - el transporte del dispositivo no puede llevar comandos en absoluto;
  - un dispositivo sí respondió, pero la plataforma no pudo registrar la respuesta tras todos los
    intentos;
  - la plataforma no pudo publicar el comando a su dispositivo y dejó de reintentarlo.

  En cada uno de estos casos, el campo `error` del comando indica cuál fue, así que nunca confundes
  un `FAILED` del lado de la plataforma con un fallo informado por el dispositivo.
- **`TIMEOUT`** — se despachó y el dispositivo nunca respondió.
- **`EXPIRED`** — su TTL transcurrió antes de que llegara a ningún dispositivo.
- **`CANCELLED`** — un operador o un inquilino lo anuló.

`EXPIRED` y `TIMEOUT` responden preguntas distintas, y confundirlos te hace buscar en el lugar
equivocado. `EXPIRED` significa que el comando nunca llegó a un dispositivo, así que una racha de
ellos indica que las entregas no están llegando. `TIMEOUT` significa que un comando sí salió y no
volvió nada, lo que apunta al dispositivo.

### Comandos para un dispositivo ausente {#commands-to-a-device-that-is-away}

Sobre MQTT, un comando solo llega a un dispositivo que esté conectado y suscrito en ese preciso
instante. El broker no lo retiene para que el dispositivo lo recoja más tarde. Sin una
comprobación, un comando publicado hacia un dispositivo ausente se perdería, y la plataforma no
tendría forma de saberlo: registraría el comando como enviado, nada respondería y una semana
después figuraría como `TIMEOUT`. Es un registro permanente que culpa a un dispositivo al que nunca
se le entregó el comando.

Por eso la plataforma comprueba antes de publicar. Cuando un transporte informa de que un
dispositivo no está conectado, sus comandos pasan a `HELD` en lugar de publicarse. Vuelven a
`QUEUED` cuando el dispositivo regresa — normalmente uno o dos segundos después de que se
reconecte, porque lo comunica directamente el transporte que posee la conexión.

Una pasada periódica también vuelve a contrastar el conjunto retenido con lo que la plataforma cree
actualmente de cada dispositivo, de modo que el pendiente se libera igualmente si ese aviso se
pierde. Ambas vías dependen de que la plataforma se entere de que el dispositivo ha vuelto. Es el
cambio en el registro de presencia lo que libera la retención; el aviso solo hace que ocurra antes.

Se aplican cuatro límites:

- **La comprobación necesita un transporte que informe de las conexiones.** Para un dispositivo
  cuyo transporte solo lleva datos, «sin eventos recientes» no es prueba de que el dispositivo no
  pueda recibir. Un dispositivo que informa cada hora está en silencio 59 de cada 60 minutos y es
  alcanzable todo el tiempo. Esos comandos se despachan como antes.
- **Es una comprobación, no una cola.** Un dispositivo que se desconecta entre la comprobación y la
  publicación pierde el comando igualmente. Lo que elimina la comprobación es el caso que la
  plataforma sí podía prever. Un transporte que mantiene su propia cola para un dispositivo dormido
  se trata aparte; consulta [Estar registrado no es lo mismo que ser alcanzable](#parked-commands).
- **Los comandos a dispositivos Sparkplug no se entregan en absoluto.** La ruta no está construida:
  los nodos Sparkplug viven en tu propia infraestructura MQTT y no en la de la plataforma, y nada
  une ambas. Un comando emitido a uno de ellos se registra como `FAILED` de inmediato, con ese
  motivo, en lugar de retenerse a la espera de un regreso que no serviría de nada.
- **Una retención sobrevive al transporte que la causó.** Solo el transporte que informó de la
  ausencia del dispositivo puede informar de su regreso. Si ese transporte deja de ejecutarse, el
  pendiente espera hasta que caduque cada comando por su cuenta. [Devolver el dispositivo a
  presencia inferida](../deployment/edge-services.md#demoting-a-device) lo libera, porque la
  retención depende de que se haya informado de la ausencia del dispositivo, y de un dispositivo
  inferido no se informa.

Una racha de `TIMEOUT` contra dispositivos que sabes que son intermitentes sigue leyéndose mejor
como algo que habla de cuándo estuvieron conectados que de su firmware — pero ahora debería ser una
racha mucho más corta.

### Estar registrado no es lo mismo que ser alcanzable {#parked-commands}

La comprobación de presencia pregunta si un dispositivo está **registrado**. Para un dispositivo
que duerme por diseño, como un dispositivo [LwM2M](./lwm2m.md) en modo cola, esa es una pregunta
distinta de si se le puede alcanzar ahora mismo. El dispositivo está registrado, así que la
comprobación pasa y el comando se publica. El transporte no encuentra entonces ninguna conexión
viva, y el comando no llega a ninguna parte.

Es el mismo problema que el anterior, una capa más abajo, y solía terminar igual. El comando se
quedaba en `SENT`, que también significa «la plataforma se lo entregó al dispositivo», de modo que
un único estado cargaba con dos significados opuestos. Una semana después el registro figuraba
como `TIMEOUT`, culpando a un dispositivo al que nunca se le entregó el comando.

Un comando así ahora registra **`PARKED`**: publicado, sin nadie que lo reciba, y todavía en poder
de la plataforma para entregarlo en el próximo despertar del dispositivo. Como la plataforma sigue
teniéndolo:

- **Un TTL vencido registra `EXPIRED`, no `TIMEOUT`.** El comando nunca llegó a un dispositivo, así
  que el registro lo dice en lugar de culpar al dispositivo por no responder.
- **Puedes cancelarlo**, por sí solo o como parte de una escritura de flota, y cancelarlo evita que
  salga en el próximo despertar. Eso no garantiza que la operación nunca se ejecutara. Una
  devolución puede ser el reintento de una publicación que sí llegó al dispositivo antes de que su
  conexión se cortara, así que interpreta una cancelación como «ya no se volverá a entregar», no
  como «nunca se llevó a cabo». Anular una escritura de flota antes informaba de los comandos
  aparcados como ya enviados y, por tanto, irrecuperables, cuando la plataforma todavía los tenía
  en su poder.
- **Cuenta contra el techo de comandos no entregados del inquilino**, exactamente igual que
  `QUEUED` y `HELD`. Es trabajo que la plataforma sigue cargando.

### Cuando la plataforma pierde el rastro de un comando {#stranded-commands}

La devolución solo funciona mientras algo siga teniendo el comando en su poder. Un comando también
puede llegar a `SENT` y luego no ser alcanzado por nada en absoluto:

- el pod que lo publicó muere antes de poder registrar el desenlace;
- un transporte se rinde tras agotar sus reintentos;
- una instancia funciona sin la pieza que realiza la devolución.

No hay nada mal en el dispositivo ni en el comando. El comando ya no tiene dueño, y `SENT` no tiene
ninguna salida que no lo necesite — salvo el TTL, que registra `TIMEOUT`.

Es la misma etiqueta errónea que `PARKED` existe para eliminar, llegando por otro camino, y la
plataforma la cierra de la misma manera. Una pasada en segundo plano busca comandos que lleven en
`SENT` sin desenlace más tiempo del que la plataforma podría haber seguido trabajando en ellos. Ese
umbral se deriva del propio presupuesto de reintentos de la capa de mensajería y del barrido de
entrega más lento que un operador puede configurar — unos diez minutos hoy, no una cifra elegida a
mano. Los comandos que la pasada puede rearmar con seguridad pasan a `PARKED`, y se entregan en el
próximo despertar del dispositivo como cualquier otro comando aparcado.

Dos límites son deliberados:

- **Solo se aplica a dispositivos LwM2M.** `PARKED` afirma que un comando no llegó a nada, y para
  un dispositivo sobre MQTT simple la plataforma no puede afirmarlo honestamente. Un comando MQTT se
  entrega en vivo a quien esté conectado en ese instante, así que un comando que parece no haber
  llegado a ninguna parte es indistinguible de uno que sí llegó y cuya respuesta se perdió. Para
  esos dispositivos, el comportamiento no cambia.
- **Rearmar acepta que un comando pueda ejecutarse dos veces cuando se perdió su respuesta.** Un
  comando sin desenlace registrado no es lo mismo que un comando que nunca se llevó a cabo: el
  dispositivo pudo actuar y perderse el informe. Rearmar sigue siendo la mejor respuesta, porque la
  alternativa es un `TIMEOUT` garantizado sobre un comando que la plataforma realmente nunca
  entregó, escrito contra un dispositivo que no hizo nada mal. Es la misma garantía de
  al-menos-una-vez que se aplica a una devolución: lee un comando rearmado como «se entregará», no
  como «no se había llevado a cabo».

Una *entrega* tardía es otra cosa, y no puede causar una segunda actuación. Tras una caída larga de
LwM2M, la entrega original de un comando rearmado todavía puede aparecer — reentregada después de
una conmutación por error, o recogida por primera vez cuando el adaptador vuelve a funcionar. Antes
de que un comando LwM2M llegue a un dispositivo, el adaptador confirma con la plataforma que la
entrega que tiene sigue siendo la actual del comando. Una entrega que la plataforma ya rearmó o
reenvió se descarta en lugar de llevarse a cabo. La confirmación mueve el `sentTime` del comando al
momento en que realmente se le envió al dispositivo.

### Cuánto pendiente puede retener un inquilino {#held-command-ceiling}

Un pendiente de comandos no entregados se drena de tres maneras y de ninguna otra:

- un dispositivo vuelve o despierta;
- vence el TTL de un comando y este registra `EXPIRED`;
- alguien anula un comando y este registra `CANCELLED`.

Para una flota que permanece apagada y a la que nadie toca, el pendiente se queda ahí hasta el
horizonte del TTL. Por eso está acotado por inquilino, y la cota es un número real en todos los
niveles: **ningún ajuste significa «ilimitado»**. Un pendiente sin cota sería un crecimiento del
almacenamiento durable que un inquilino puede provocar y un operador no puede ver.

El límite se resuelve por una cascada: la anulación propia del inquilino si la tiene, si no la de
su nivel, y si no el valor predeterminado de la plataforma, **10 000**. A un inquilino que ya está
en su límite se le rechaza el siguiente comando con el código `HELD_CEILING_EXCEEDED`.

:::note Acota el trabajo no entregado, no solo el retenido
La cuenta abarca todo comando en `QUEUED`, `HELD` o `PARKED`, no solo los retenidos por
dispositivos ausentes, así que un inquilino cuya flota está entera presente puede ser rechazado
igualmente, solo por volumen de emisión en vuelo.
Los comandos en cola se drenan en un ciclo, así que aportan aproximadamente un ciclo de ritmo de
emisión: poco, pero no cero, y un inquilino que emite cerca de su techo a alta frecuencia lo notará.
:::

#### Una parte del techo está reservada para la entrega {#delivery-machinery-reserve}

No todo el techo está a tu disposición. Una parte — el **20 % de forma predeterminada**, es decir,
2000 de los 10 000 de la plataforma — se guarda para la entrega de comandos de la propia
plataforma, y solo la plataforma dispone de ella. Todo lo que emite comandos en tu nombre queda
acotado por el resto: la consola, los SDK, `dcctl` y tus propias integraciones por igual.

La reserva existe por lo que puede provocar una escritura de flota. «Reinicia todas las bombas» es
una sola petición legítima capaz de llenar el techo entero de golpe. A partir de ese momento se
rechaza cada comando que tus reglas de automatización intentan enviar hasta que el pendiente se
drene, lo que para una flota desconectada puede significar días. La reserva mantiene en marcha tu
automatización basada en alarmas mientras una escritura de flota está en vuelo.

Se aplica igual tanto si envías un comando como diez mil. Un lote se admite hasta el mismo límite
que alcanzaría un bucle de comandos individuales, así que no hay forma de eludirla ni ventaja en
ninguna de las dos formas. Consulta [Un comando, muchos dispositivos](#command-batches).

Un rechazo nombra el límite que realmente se aplicó. Cuando es la reserva la que te ha acotado,
indica también cuánto se apartó, de modo que quien reciba un rechazo en 8000 frente a un techo
visible de 10 000 pueda distinguir ambas cifras. La reserva es un ajuste del operador, no del
inquilino; no se puede subir ni bajar por inquilino.

#### Reintentar un rechazo por techo {#retrying-a-ceiling-refusal}

`HELD_CEILING_EXCEEDED` es el **único rechazo temporal** que produce la ruta de puesta en cola.
Cualquier otro rechazo describe una petición que estará igual de mal en el siguiente intento; este
se resuelve solo conforme sale el pendiente. Es el único código que vale la pena reintentar, y los
demás vale la pena mostrarlos a una persona. Consulta
[Enviar un comando](../guides/sending-commands.md#when-an-enqueue-is-refused).

### Quién informa del desenlace {#who-reports-the-outcome}

Solo el dispositivo puede informar de un éxito. `SUCCESSFUL` y un `FAILED` informado por el
dispositivo son los dos desenlaces que solo el dispositivo puede producir. Cualquier otro estado
terminal lo escribe la plataforma por su cuenta:

- `TIMEOUT`, `EXPIRED` o `CANCELLED`;
- `FAILED` porque el transporte no puede llevar el comando en absoluto;
- `FAILED` porque llegó la respuesta de un dispositivo y no se pudo registrar contra el comando;
- `FAILED` porque la plataforma no pudo publicar el comando y dejó de reintentarlo.

Ninguno de los dos últimos es `TIMEOUT`, por la misma razón en direcciones opuestas: en uno el
dispositivo sí respondió, y en el otro nunca se le envió nada. `TIMEOUT` te llevaría a inspeccionar
hardware que funciona en ambos casos.

- **Cuando la respuesta se perdió**, el campo `error` del comando lo indica, y el resultado que
  informó el dispositivo no se puede recuperar. Si importa, vuelve a preguntarle al dispositivo.
- **Cuando el comando no se pudo publicar**, nada llegó al dispositivo, y puedes volver a emitir el
  comando una vez corregida la causa.

Informar del resultado es la mitad del contrato que le corresponde al dispositivo; consulta
[Responder a un comando](../guides/connecting-a-device.md#responding-to-a-command). Un dispositivo
que nunca responde deja sus comandos en `SENT` hasta que su TTL los convierte en `TIMEOUT`. La
excepción es LwM2M, donde un comando que queda sin desenlace durante varios minutos se rearma para
el próximo despertar del dispositivo, como se describe en
[Cuando la plataforma pierde el rastro de un comando](#stranded-commands).

Todo comando lleva un TTL: el que definas con `expiresAt`, o el predeterminado de la plataforma de
siete días. Define el tuyo si tus dispositivos no informan resultados y una semana es más de lo que
el comando sigue siendo útil.

Cancelar un comando registra `CANCELLED`. Hasta hace poco, la cancelación y la expiración del TTL
compartían el único valor `EXPIRED`, así que los comandos cancelados antes de ese cambio siguen
figurando como `EXPIRED`. Ambos aparecen en los datos históricos, y nada registró qué filas
`EXPIRED` provenían de una cancelación, así que no es posible distinguirlas a posteriori.

Cada dispositivo recibe comandos en un topic acotado exclusivamente a ese dispositivo, y está
autorizado solo para ese topic. Un dispositivo no puede observar comandos dirigidos a ningún otro
dispositivo de su inquilino.

## Un comando, muchos dispositivos {#command-batches}

Un **lote de comandos** difunde un solo comando a muchos dispositivos como una única operación
registrada. Nombras los dispositivos explícitamente o los resuelves a partir de un **grupo de
entidades**, y todos reciben la misma clave de comando y el mismo payload. Para emitir uno,
consulta [Comandar una flota](../guides/commanding-a-fleet.md).

Todo lo anterior sigue aplicándose por dispositivo. Cada comando se valida contra el contrato de
capacidades de ese dispositivo, se retiene si el dispositivo está ausente, se sigue por el mismo
ciclo de vida y queda acotado por el mismo TTL. Un lote no cambia nada de lo que le ocurre a un
comando; cambia lo que la plataforma *recuerda* sobre la operación en conjunto.

Ese registro es lo importante. Un dispositivo que la plataforma rechaza no recibe fila de comando
alguna — no existe un estado que signifique «se quiso pero no se creó». Sin un registro del lote,
un rechazo no dejaría rastro, y un operador que dispara una escritura de flota y vuelve por la
mañana no tendría nada que leer. El registro conserva a cuántos dispositivos resolvió el objetivo,
cuántos se pusieron realmente en cola, y cuáles fueron rechazados y por qué.

Tres cosas distinguen un lote de un bucle de comandos individuales, y ninguna es comodidad:

- **Un objetivo de grupo queda congelado en el momento del disparo.** El lote resuelve la membresía
  **publicada** del grupo, nunca un selector en borrador, y registra la versión del grupo contra la
  que resolvió. Editar el grupo después no puede cambiar lo que ya salió, y una auditoría todavía
  puede responder qué *significaba* el grupo cuando el lote se disparó.
- **Una difusión parcial es una decisión, no un valor por defecto.** En una flota real algunos
  dispositivos no pueden recibir el comando, y quien llama debe declarar si eso es aceptable. Si no
  lo es, el lote entero se rechaza y **no se crea nada**, ni siquiera el registro, porque no ocurrió
  nada. Si lo es, los dispositivos que pueden recibir el comando lo reciben y el resto quedan
  registrados como rechazos.
- **Se puede anular como una sola operación.** Deshacer un bucle implica cancelar cada comando por
  separado, y seguir conociendo todos los tokens con los que se emitieron.

Las cifras del registro — cuántos dispositivos se resolvieron, cuántos se admitieron — describen
**el momento en que se disparó el lote**, no el presente. Las filas de comando no son inmortales,
así que una cuenta en vivo podría quedar por debajo de la verdad del momento de creación sin ningún
rechazo que explique la diferencia. Responde las preguntas en presente («de los 5000 en cola,
¿cuántos han salido?») buscando los comandos que creó el lote, no releyendo el lote.

Los rechazos se almacenan de dos formas, porque una difusión grande necesita ambas:

- una lista individual, acotada para que una escritura de flota no pueda almacenar un blob sin
  límite;
- totales completos por código que nunca se truncan.

Los totales hacen que el registro se audite solo: los dispositivos resueltos siempre equivalen a
los admitidos más la suma de los recuentos de rechazo, incluso cuando la muestra nombrada se queda
corta.

### Cancelar un lote detiene lo que aún no ha salido {#cancelling-a-batch}

Cancelar un lote mueve sus comandos de `QUEUED`, `HELD` o `PARKED` a `CANCELLED`. Los comandos que
ya están en `SENT` se dejan en paz, y los dispositivos que los recibieron actuarán igualmente sobre
ellos.

`SENT` es la línea, y se traza en un solo sitio: la plataforma anula lo que todavía tiene en su
poder y deja en paz lo que ya ha puesto en camino. Un comando `SENT` se despachó hacia un
dispositivo que se creía activo, así que cancelarlo no revoca nada. Lo único que haría es que la
plataforma dejara de esperar la respuesta de ese dispositivo, sustituyendo un desenlace real por un
registro que dice que un operador lo anuló. A escala de flota, los dispositivos actuarían, las
respuestas se descartarían y el registro diría que la operación se anuló.

Cancelar un comando **individual** traza exactamente la misma línea: `QUEUED`, `HELD` y `PARKED`
se cancelan, y un comando `SENT` se devuelve sin cambios en lugar de llevarse a `CANCELLED`.
Consulta [Cancela uno](../guides/sending-commands.md#cancel-one).

Los dispositivos LwM2M tienen una parada más. El adaptador LwM2M confirma cada comando con la
plataforma inmediatamente antes de llevarlo a cabo. Un comando de un lote que se publicó pero aún
no había llegado a su dispositivo cuando se canceló el lote se detiene ahí y registra `CANCELLED`.
El resultado de la cancelación del lote todavía lo cuenta como ya enviado, porque eso era en el
momento de la cancelación.

Cancelar un lote nunca se rechaza. Un freno que se negara a actuar porque parte de la flota ya se
movió dejaría comandada al resto de la flota, que es el peor desenlace posible. Así que siempre
actúa e informa de lo que alcanzó. El registro del lote queda sellado con cuándo se canceló y
cuántos comandos alcanzó esa llamada. El sello es de primero que llega: una segunda cancelación no
sobrescribe lo que registró la primera.
