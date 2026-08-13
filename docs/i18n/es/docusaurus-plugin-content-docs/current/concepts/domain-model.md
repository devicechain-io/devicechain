---
title: Modelo de dominio
---

# Modelo de dominio

DeviceChain modela el mundo físico con un pequeño conjunto de conceptos componibles. La decisión definitoria es que el *contexto* del dispositivo se expresa como un **grafo de relaciones tipado (typed relationship graph)** en lugar de un registro de asignación fijo — lo que mantiene el modelo abierto a nuevos tipos de entidad con el tiempo.

## Entidades principales

- **Device (Dispositivo)** — la cosa que se conecta e informa; una instancia de un tipo de dispositivo.
- **Device Type (Tipo de dispositivo)** — la capa de **taxonomía/identidad**: el nombre de una clase de dispositivo, su apariencia (icono y colores) y su clasificación para agrupar y filtrar. Un tipo de dispositivo hace referencia como máximo a un perfil de dispositivo.
- **Device Profile (Perfil de dispositivo)** — el **contrato de capacidades**: un agregado distinto y **versionado** (borrador → publicar → revertir) que posee las definiciones de **métrica**, **comando** y **regla de detección** de una clase de dispositivo. Muchos tipos de dispositivo pueden compartir un mismo perfil — de modo que la configuración de capacidades se define una vez y se reutiliza — y un dispositivo resuelve sus capacidades a través de `device → type → profile`. Un tipo de dispositivo sin perfil es válido; simplemente clasifica y muestra sus dispositivos sin otorgar un contrato de capacidades tipado.
- **Asset (Activo)** — la cosa del mundo real que un dispositivo monitorea (categorizada como Device / Person / Hardware).
- **Area (Área)** — una ubicación espacial/organizacional, opcionalmente con límites poligonales y zonas; las áreas se anidan en jerarquías.
- **Customer (Cliente)** — un propietario organizacional; los clientes también se anidan en jerarquías.
- **Groups (Grupos)** — un **grupo de entidades** uniforme que agrupa cualquiera de las anteriores. La membresía es **estática** (una lista explícita de miembros) o **dinámica** — un selector guardado sobre los atributos de los miembros, resuelto en el momento de la lectura (ver [Facetas y grupos dinámicos](#facets-and-dynamic-groups)).

Cada una de estas entidades se direcciona de manera uniforme mediante un **tipo de entidad + id**, lo que permite que las relaciones, los grupos y la indexación de eventos operen de forma genérica sobre todas ellas.

## Relaciones {#relationships}

En lugar de vincular un dispositivo a una única asignación fija `(customer, area, asset)`, DeviceChain conecta las entidades con **relaciones tipadas y dirigidas**:

- Una relación tiene un **origen (source)**, un **destino (target)** y un **tipo de relación**.
- Un tipo de relación lleva un indicador **`Tracked`**.

El indicador `Tracked` es central. Cuando un dispositivo informa un evento, la plataforma registra **cada una** de las relaciones rastreadas del dispositivo como un **anclaje (anchor)** en ese evento (una entrada `(anchor_type, anchor_id)` en el conjunto de anclajes del evento). Un dispositivo puede tener **varias** relaciones rastreadas — un cliente *y* un área *y* un activo — y la lectura entonces se puede consultar por **cada una** de ellas: "cada lectura de temperatura del Edificio 7" y "…del cliente Acme" encuentran ambas esa lectura. Los anclajes se capturan en el momento de la escritura, de modo que el historial permanece intacto cuando un dispositivo se reasigna más tarde.

**La asignación organiza; no bloquea.** Un dispositivo que tiene credenciales pero aún no está asignado sigue informando telemetría — sus eventos se resuelven con un **anclaje nulo** en lugar de descartarse. Asignar el dispositivo más tarde le da a sus eventos posteriores un anclaje de cliente/área/activo. (Ver [Gestión de asignaciones de dispositivos](../guides/managing-assignments.md).)

## Atributos frente a eventos

DeviceChain distingue el **estado actual** del **historial**:

- Los **eventos** son el registro de series temporales de solo anexado (append-only) de todo lo que informa un dispositivo (mediciones, ubicaciones, alertas, invocaciones/respuestas de comandos, cambios de estado). Residen en hypertables de TimescaleDB.
- Los **atributos** son el estado actual de clave-valor de una entidad, en tres ámbitos:
  - `CLIENT` — informado por el dispositivo.
  - `SERVER` — metadatos exclusivos de la plataforma que el dispositivo nunca ve.
  - `SHARED` — definidos por la plataforma y legibles por el dispositivo (el canal para la configuración remota y los destinos OTA).

## Facetas y grupos dinámicos {#facets-and-dynamic-groups}

Los atributos también funcionan como **facetas de clasificación** — los ejes por los que se navegan y filtran las entidades. Un **registro de facetas** por inquilino declara qué claves de atributo (para una familia de entidades dada) son facetas, dando a la interfaz de navegación de la consola sus ejes y el autocompletado de valores; declara *qué claves* son facetas, no los valores (los valores permanecen como atributos en las propias entidades).

Un **grupo dinámico** convierte un filtro de facetas en una membresía guardada y autoactualizable. Su selector es una expresión booleana sobre los atributos de los miembros — por ejemplo `attr["climate"] == "arid" && attr["country"] == "US"` — escrita en [CEL](https://github.com/google/cel-go), el mismo lenguaje de expresiones que usa el motor de detección. La plataforma valida y limita el costo del selector cuando se guarda el grupo, y luego resuelve la membresía **en el momento de la lectura**, transformando la expresión en una consulta de base de datos indexada (nunca escaneando cada entidad), de modo que un grupo dinámico siempre refleja el estado actual de los atributos sin ninguna caché materializada que mantener sincronizada. Un grupo estático, en cambio, mantiene una lista explícita de miembros. La pantalla **Browse (Explorar)** de la consola compone un selector a partir de los ejes de facetas, previsualiza el recuento coincidente en vivo, y lo guarda como un grupo dinámico; la pantalla **Facets (Facetas)** administra el registro.

## Comandos y el contrato de capacidades {#commands-and-the-capability-contract}

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
a `QUEUED` cuando el dispositivo regresa.

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

## Identidad y credenciales

Un dispositivo tiene una **identidad estable** que todo lo demás referencia, mantenida separada de sus **credenciales** (el material que usa para autenticarse). Las credenciales son conectables (pluggable) — **token de acceso**, **MQTT-basic** (usuario + contraseña) y **certificado X.509** — de modo que un dispositivo puede rotar o mantener múltiples credenciales sin cambiar su identidad. El secreto de una credencial es de **solo escritura** — se envía cuando se registra la credencial y nunca se devuelve en una lectura —, pero eso solo cubre la contraseña de MQTT-basic: para un token de acceso o un certificado, el id de la credencial es en sí mismo la prueba de posesión, de modo que leer las credenciales de un dispositivo requiere la autoridad `device:write` y no `device:read`. Ver [Credenciales de dispositivo](../guides/device-credentials.md#reading-a-credential).

Un dispositivo también puede llevar un **`externalId`** opcional — una **clave de negocio** propiedad del cliente, como un VIN, número de serie, código GS1 o etiqueta de activo. Es distinto tanto de la identidad interna como de la credencial: es **opaco** (sin restricciones de formato), **único dentro de un inquilino** cuando está presente, y **nunca se usa para direccionamiento ni autenticación**. Su propósito es la búsqueda y la integración — hacer coincidir un dispositivo de DeviceChain con el identificador que tus otros sistemas ya usan para esa misma cosa física.

## Eventos

Cada evento registra el dispositivo que lo informa, el tipo de evento, las marcas de tiempo informadas por el dispositivo y recibidas por la plataforma, un id de correlación externo opcional (`alternateId`) para una ingesta idempotente, y el anclaje de relación resuelto descrito arriba (nulo cuando el dispositivo no está asignado). Las categorías de eventos incluyen mediciones, ubicaciones, alertas, invocaciones y respuestas de comandos, y cambios de estado.

Las mediciones son **autodescriptivas**: cuando una lectura coincide con una métrica definida en el perfil del dispositivo, la plataforma estampa la **unidad** y el **tipo de dato** de esa métrica directamente sobre la lectura persistida (y sobre la proyección de último estado conocido en vivo). Un consumidor que lee una medición obtiene su semántica — `22.4 °C`, un `DOUBLE` — sin una segunda consulta al perfil.
