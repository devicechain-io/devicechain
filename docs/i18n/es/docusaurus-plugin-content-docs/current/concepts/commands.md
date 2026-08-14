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
  respuesta.

Estos son terminales, y nada sale de un estado terminal:

- **`SUCCESSFUL`** / **`FAILED`** — el dispositivo informó el resultado.
- **`TIMEOUT`** — se despachó y el dispositivo nunca respondió.
- **`EXPIRED`** — su TTL transcurrió antes de que llegara a salir.
- **`CANCELLED`** — un operador o un inquilino lo canceló.

`EXPIRED` y `TIMEOUT` responden preguntas distintas, y confundir uno con el otro te hace
buscar en el lugar equivocado: `EXPIRED` significa que el comando nunca salió de la
plataforma, así que una racha de ellos indica que las entregas no se están intentando;
`TIMEOUT` significa que sí salió y no volvió nada, lo que apunta al dispositivo.

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
periódica recoge lo que ese aviso no alcance, de modo que el pendiente de un dispositivo que
regresa siempre acaba liberándose aunque la plataforma nunca llegue a enterarse de la
reconexión.

Conviene conocer tres límites:

- **La comprobación necesita un transporte que informe de las conexiones.** Para un
  dispositivo cuyo transporte solo transporta datos, "sin eventos recientes" no es prueba de
  que el dispositivo no pueda recibir: un dispositivo que informa cada hora está en silencio
  59 de cada 60 minutos y es alcanzable durante todo ese tiempo. Esos comandos se despachan
  como antes.
- **Es una comprobación, no una cola.** Un dispositivo que se desconecta entre la
  comprobación y la publicación pierde el comando igualmente. Lo que se elimina es el caso
  que la plataforma sí podía prever.
- **Los comandos a dispositivos Sparkplug no se entregan en absoluto.** La ruta no está
  construida: los nodos Sparkplug viven en tu propia infraestructura MQTT y no en la de la
  plataforma, y nada une ambas. Un comando emitido a uno de ellos se registra como `FAILED`
  de inmediato, con ese motivo, en lugar de retenerse a la espera de un regreso que no
  serviría de nada.

Por eso una racha de `TIMEOUT` contra dispositivos que usted sabe que son intermitentes
sigue mereciendo leerse como algo que habla de cuándo estuvieron conectados y no de su
firmware — pero ahora debería ser una racha mucho más corta.

Cancelar un comando registra `CANCELLED`. La cancelación y la expiración del TTL compartían
hasta hace poco el único valor `EXPIRED`, así que los comandos cancelados antes de ese
cambio siguen apareciendo como `EXPIRED`; ambos conviven en los datos históricos, y como
nada registró qué filas `EXPIRED` provenían de una cancelación, no es posible distinguirlas
a posteriori.

**Un comando solo alcanza un resultado terminal si el dispositivo responde.** Informar el resultado
es la mitad del contrato que le corresponde al dispositivo — ver
[Responder a un comando](../guides/connecting-a-device.md#responding-to-a-command). Un
dispositivo que nunca responde deja sus comandos en `SENT` hasta que su TTL los convierte
en `TIMEOUT`. Todo comando lleva un TTL — el que definas con `expiresAt`, o el
predeterminado de la plataforma de siete días —, así que define el tuyo si tus dispositivos
no informan resultados y una semana es más de lo que el comando sigue siendo útil.

Cada dispositivo recibe comandos en un topic acotado exclusivamente a ese dispositivo, y está autorizado
solo para ese topic — un dispositivo no puede observar comandos dirigidos a ningún otro dispositivo de
su inquilino.
