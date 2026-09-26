---
title: Procesamiento de eventos y alarmas
---

# Procesamiento de eventos y alarmas

DeviceChain convierte la telemetría bruta de los dispositivos en señales sobre las que puedes actuar. Un servicio dedicado, **event-processing**, observa los eventos a medida que fluyen por el pipeline. Su etapa de **detección** evalúa reglas en streaming en tiempo real. Su etapa de **acciones** ejecuta después las respuestas automatizadas que declara cada disparo: levantar una **alarma** (una condición con estado que tiene un ciclo de vida, una severidad y una vía para notificar a una persona) o emitir un **comando** de vuelta al dispositivo.

El servicio evalúa sobre el tiempo del evento y persiste su estado, de modo que un reinicio vuelve a derivar disparos idénticos: ninguno se pierde, ninguno se duplica.

:::note Estado
**Disponible hoy:** la detección y las acciones son el motor de detección en vivo de la plataforma, con los ocho [tipos de condición](#condition-types) (umbrales estáticos o determinados por atributos) y las cuatro [acciones](#automated-actions) con guardas por acción. Las reglas se autoran de tres maneras sobre un mismo esquema —generador de formularios, lienzo visual de automatización y, con el servicio de IA habilitado, una puerta "Describir" en lenguaje natural—, todas [validadas por el mismo compilador](#authoring--previewing-rules) antes de publicar, y el lienzo puede previsualizar un borrador contra historial reproducido. El [ciclo de vida de alarma](#the-alarm-lifecycle) de cuatro estados con escalamiento de severidad en el mismo lugar, las suscripciones en vivo a alarmas y detecciones, y la [notificación](#reaching-a-human) por correo electrónico/webhook con escalamiento ya están implementados.
:::

## Dónde viven las reglas

Defines las reglas de detección sobre un **[perfil de dispositivo](./domain-model.md)**, el contrato de capacidades versionado compartido por uno o más tipos de dispositivo. El perfil está versionado (borrador → publicar → revertir), así que cambias la lógica de detección de una flota igual que cambias sus definiciones de métricas y comandos: autoras un borrador, lo publicas de forma atómica y lo reviertes si hace falta. Todo dispositivo que resuelve a ese perfil adopta sus reglas automáticamente.

Una regla plantea una **condición** sobre la telemetría del perfil, declara su **severidad** y enumera las **acciones** a ejecutar cuando se dispara.

## Delimitar una regla a un grupo

Por defecto, una regla se aplica a **todos los dispositivos** que resuelven a su perfil. En su lugar, puedes **delimitar una regla a un [grupo dinámico](./domain-model.md#facets-and-dynamic-groups)**, para que se dispare solo para los dispositivos que son miembros en ese momento. Por ejemplo, puedes ejecutar una regla de calor más estricta únicamente en *dispositivos en zonas áridas*. La delimitación es opcional y se define por regla. Las reglas de ausencia y de correlación de área no se pueden delimitar, y publicar una delimitada se rechaza.

La membresía del grupo se registra en cada evento **en el momento en que se resuelve**. Por eso el motor ve exactamente qué reglas aplicaban en ese instante, incluso cuando reproduce historial para previsualizar o volver a derivar disparos. Cuando un dispositivo entra o sale del grupo, queda inscrito o excluido en su siguiente evento, sin editar ninguna regla ni reescanear nada.

## Tipos de condición {#condition-types}

La detección cubre condiciones de umbral, sostenida por duración, ocurrencia repetida, tasa de cambio, silencio/ausencia, conectividad, agregado en ventana y correlación de área/grupo.

| Condición | Se dispara cuando | Parámetros |
|---|---|---|
| **Umbral** | una lectura cruza una comparación (p. ej. `temperature > 80`) | la comparación + un valor umbral |
| **Duración** | la condición se sostiene continuamente durante al menos un tiempo definido (p. ej. `pressure low for 5 minutes`) | un tiempo de sostenimiento |
| **Repetición** | la condición ocurre un número de veces dentro de una ventana (p. ej. `3 faults in 10 minutes`) | un conteo de ocurrencias + una ventana |
| **Tasa de cambio** | una métrica cambia demasiado rápido entre lecturas consecutivas (p. ej. `temperature rising > 5°/s`) | la comparación + una marca opcional para normalizar el cambio a una tasa por segundo |
| **Ausencia / silencio** | un dispositivo deja de reportar: ningún evento en absoluto dentro de una ventana (una verificación de tipo "hombre muerto"); todo evento cuenta como latido, así que la regla no lleva condición | una ventana de silencio |
| **Conectividad** | un dispositivo reporta una desconexión autoritativa (levanta) y se reconecta (resuelve), para transportes que afirman presencia como [Sparkplug-B](./sparkplug.md) y [LwM2M](./lwm2m.md). *Se autora en el generador de formularios de la consola o a través de la API; el lienzo visual de automatización no ofrece el tipo (véase más abajo).* | ninguno: el borde de [presencia](./device-presence.md) es toda la señal |
| **Agregado en ventana** | un agregado sobre una ventana cruza una comparación (p. ej. `average > 50 over 10 minutes`) | la función (count/sum/avg/min/max), una ventana (tumbling, sliding, session o una ventana de conteo de N eventos), la comparación + valor |
| **Correlación de área** | suficientes dispositivos distintos en un área cumplen la condición en conjunto (p. ej. `≥ 3 devices in a zone report a fault within 5 minutes`) | el tipo de área/ancla, un conteo de dispositivos distintos + ventana |

La comparación de cada condición puede ser una hoja estructurada `metric · operator · value` o una **expresión CEL** avanzada sobre el evento. Ambas se verifican de tipos de forma estática y se limitan en costo cuando se publica el perfil, de modo que una regla mal formada o desbocada se rechaza antes de poder ejecutarse.

:::note Las ventanas tienen un techo
Todo lapso de tiempo que declara una regla está limitado a **24 horas** de forma predeterminada. Una regla que pida más se rechaza al publicar el perfil, y el error nombra el campo y el límite. Consulta [Límites de tiempo en las reglas](#time-limits-on-rules).
:::

### Límites de tiempo en las reglas {#time-limits-on-rules}

El techo predeterminado de 24 horas se aplica a todo lapso de tiempo que declara una regla: una ventana, un tiempo de sostenimiento, un tiempo de espera de silencio y un hueco de sesión.

El techo existe porque una regla con ventana guarda **un registro por lectura** durante toda la ventana, por dispositivo, en un motor compartido por todos los inquilinos. Una ventana de varios días sobre una flota que reporta cada pocos segundos es una gran cantidad de memoria retenida indefinidamente. Ese costo lo paga todo el mundo en la instancia, no solo el inquilino que autoró la regla.

Los tiempos de espera de silencio y los huecos de sesión también están limitados, aunque no retengan lecturas. Esas reglas rearman un temporizador cada vez que un dispositivo reporta, y los temporizadores sustituidos no se liberan hasta que vence su plazo. Por eso un tiempo de espera largo con reportes frecuentes se acumula de la misma manera.

Si necesitas un lapso mayor, un operador puede subir el límite de la instancia ([`maxRuleDurationSeconds`](../deployment/detection-engine.md#configuration)) tras dimensionar la memoria. Antes de pedirlo, considera si la pregunta trata en realidad de *retención* más que de *detección*. Una pregunta del tipo «comparar contra el mes pasado» suele responderse mejor consultando el historial almacenado que reteniendo un mes de lecturas en memoria.

### Las reglas de Conectividad y el lienzo de automatización

El generador de formularios autora y abre reglas de Conectividad. El tipo está en su selector y, como el borde de presencia es toda la señal, el formulario no ofrece condición ni parámetros para él.

El lienzo visual de automatización no tiene nodo de Conectividad. Rechaza abrir una regla de Conectividad sin más y te indica que ese tipo no puede mostrarse en el lienzo. Autora y edita una regla de Conectividad en el generador de formularios o a través de la API, no en el lienzo.

Si el formulario abre una regla almacenada que no puede contener por completo —un campo que no modela o un tipo que no conoce—, advierte que parte de la definición no se muestra y que guardar reemplazaría la original únicamente con lo que ves. Ese aviso es distinto del de «no se pudo leer» que aparece para una definición que no es JSON válido. Una regla de Conectividad se abre sin ninguno de los dos.

### Umbrales estáticos y dinámicos

Un umbral puede ser un **valor fijo** en la regla, o **dinámico**: el nombre de un **atributo** de dispositivo que la regla lee en el momento de la evaluación. Un umbral dinámico permite que una sola regla se adapte por dispositivo. El perfil define la regla una vez, y cada dispositivo lleva su propio límite como un atributo con alcance `SERVER` o `SHARED` (los valores establecidos por el servidor tienen prioridad). Cambia el atributo y el umbral efectivo cambia, sin editar la regla.

#### Umbrales dinámicos en una expresión CEL {#dynamic-thresholds-in-cel}

En una expresión CEL, las mediciones del evento son el mapa `m` y los atributos del dispositivo son el mapa `attr`, ambos de una clave a un número. Una clave está en `attr` solo mientras el dispositivo tiene un valor **numérico** para ella con alcance `SERVER` o `SHARED`. Falta cuando el atributo nunca se estableció, cuando se estableció con algo que no es un número, cuando se estableció con alcance `CLIENT`, y durante un breve tiempo después de establecerlo, hasta que el cambio llega al motor de detección.

Compruebe la presencia antes de leer un valor. Un umbral dinámico creado en el formulario se compila como `"tempLimit" in attr && "temp" in m && m["temp"] > attr["tempLimit"]`, que no se dispara para un dispositivo sin el atributo. El formulario no tiene valor de respaldo. Para recurrir a un límite fijo, escriba el respaldo como su propia comparación:

```
"temp" in m && ("tempLimit" in attr ? m["temp"] > attr["tempLimit"] : m["temp"] > 80.0)
```

Como esta expresión no puede ser verdadera para un evento sin `temp`, la regla solo mira los eventos que llevan `temp`. Un evento sin ella se omite: no resuelve una alarma de umbral y no interrumpe el tiempo de sostenimiento de una regla de duración.

Una condición de umbral o de duración que sería verdadera en todos los eventos de **todos** los dispositivos sin los atributos que lee, sea cual sea el contenido del evento, se rechaza al publicar el perfil. Por ejemplo, `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]` levantaría una alarma para cada uno de esos dispositivos, informara lo que informara, mientras le faltara el atributo. Una condición que sigue dependiendo de la lectura, como `!("tempLimit" in attr) && m["temp"] > 80.0`, se acepta. Tenga en cuenta que también se aplica a los dispositivos cuyo atributo tiene un tipo o un alcance incorrectos, no solo a los que nunca lo establecieron.

En una regla de repetición, de tasa de cambio, de agregado en ventana o de correlación de área, la condición es un filtro sobre qué eventos cuentan, así que allí se acepta un filtro como `!("maint" in attr)` («dispositivos que no están en mantenimiento»).

## Acciones automatizadas {#automated-actions}

Cuando una regla se dispara, se ejecutan sus acciones. Las acciones integradas son:

- **Levantar alarma**: abre (o escala) una alarma con estado para el dispositivo, descrita más abajo. Es el tipo con el que empieza una acción nueva, tanto en el generador de formularios como en el lienzo, y no necesita más objetivo que una severidad. Una regla sin acciones no levanta ninguna alarma; solo emite una detección a la que puedes suscribirte.
- **Enviar comando**: encola un comando de vuelta al dispositivo a través del pipeline persistente de comandos. El despacho es idempotente, de modo que un reprocesamiento o reintento nunca envía dos veces.
- **Llamar a un webhook** (`httpCall`): hace POST de un payload con forma CEL a un endpoint HTTP externo, con entrega endurecida (se rechazan las redirecciones, se eliminan los encabezados reservados) y autenticación opcional vía el almacén de secretos.
- **Publicar a un conector** (`publish`): entrega un payload con forma CEL a un **[conector saliente](./outbound-connectors.md)** que lo distribuye a un broker de mensajes o una cola en la nube (MQTT, Kafka, AWS SNS/SQS).

Las dos acciones salientes, `httpCall` y `publish`, se describen en **[Conectores salientes](./outbound-connectors.md)**. Las entrega un servicio separado, así que un sistema externo lento nunca ralentiza la detección.

Una regla puede llevar varias acciones, hasta un pequeño límite fijo. Una regla de correlación de área no lleva ninguna: su disparo pertenece a un área, no a un dispositivo, y toda acción apunta a un dispositivo. Cada acción puede estar **protegida por una guarda**, una condición sobre el disparo. Por ejemplo, una regla puede levantar una alarma en cada disparo pero enviar un comando solo cuando la lectura esté en una banda particular.

Un disparo es **activado por flanco**: un flanco ascendente cuando la condición empieza a sostenerse y un flanco descendente cuando deja de hacerlo. Por eso una alarma levantada en el flanco ascendente se limpia automáticamente en el flanco descendente. Tú autoras el levantamiento, y la limpieza queda implícita.

## Autoría y previsualización de reglas {#authoring--previewing-rules}

Autoras las reglas en la consola de tres maneras. Las tres usan el mismo esquema, y el **mismo compilador del lado del servidor** las valida todas antes de publicar:

- Un **generador de formularios**: un formulario tipado por tipo de condición, la vía más rápida para una sola regla. A medida que editas, muestra en línea la retroalimentación de tipos y costo del compilador, antes de publicar. Su selector de acciones solo ofrece levantar alarma y enviar comando. Las guardas y las acciones salientes se autoran en el lienzo; el formulario las muestra en modo de solo lectura y las conserva al guardar.
- Un **lienzo visual de automatización**: un grafo de nodos (fuente → condición → ramas opcionales → acciones) para flujos más ricos. El lienzo **compila a la misma regla** que produciría un formulario; es una superficie de autoría, no un segundo motor. Añade nodos de **rama** (enrutar un disparo a distintas acciones mediante una guarda) y nodos de **cómputo** (nombrar un valor derivado reutilizable y referenciarlo en una condición o guarda).
- Una puerta **"Describir"** en lenguaje natural: donde el servicio de IA está habilitado, describes la regla con palabras y recibes una candidata redactada para revisarla y publicarla. Se ofrece al crear una regla nueva, y produce una regla en el mismo esquema que producen las otras dos. Consulta [Autoría asistida por IA](./ai-authoring.md).

Lo más destacado del lienzo es la **previsualización contra historial**. Ejecutas una regla en *borrador* sobre el historial de eventos reproducido del perfil y ves los flancos de levantamiento/resolución que *habría* producido en una ventana elegida, sin publicar nada. Al seleccionar un disparo se superpone sobre el lienzo una **traza por nodo** que muestra el camino que tomó el evento: qué condición coincidió, qué rama tomó y qué acción se disparó. Edita y vuelve a previsualizar hasta que la regla haga lo que esperas, y luego publícala.

## El ciclo de vida de la alarma {#the-alarm-lifecycle}

Una alarma levantada es un **objeto con estado**, no un mensaje único. Su estado combina dos ejes en un **modelo de cuatro estados**:

- **Estado**: `ACTIVE` mientras la condición se sostiene, `CLEARED` una vez que se resuelve.
- **Reconocida (Acknowledged)**: si un operador ha tomado posesión de la alarma, con un registro de quién y cuándo.

Una alarma pasa por `ACTIVE/no reconocida` → `ACTIVE/reconocida` → `CLEARED`. Una condición intermitente reactiva la *misma* alarma en lugar de generar duplicados.

Una alarma nombra el dispositivo que la levantó. Consultas las alarmas a nivel de todo el inquilino con filtros (estado, severidad, reconocimiento, dispositivo de origen) en lugar de leerlas desde una entidad superior.

### Severidad y escalamiento

Cada alarma lleva una **severidad**: `CRITICAL`, `MAJOR`, `MINOR`, `WARNING` o `INDETERMINATE`. Una sola condición puede declarar reglas en varios niveles de severidad (por ejemplo `temp > 80 → MAJOR`, `temp > 100 → CRITICAL`). Cuando esas reglas levantan bajo la misma clave de alarma, el motor **escala en el mismo lugar una única alarma activa** hasta el nivel más alto que se cumple en ese momento, y la desescala a medida que las condiciones ceden, en lugar de abrir una alarma separada por nivel. Una acción de levantar alarma sin clave de alarma usa como clave la propia regla (sus tokens de perfil y de regla), así que las reglas que se dejan con ese valor por defecto abren alarmas separadas.

## Llegar a una persona {#reaching-a-human}

Una alarma levantada puede notificar a las personas a través del sistema de **notificaciones**. Una política por inquilino enruta las alarmas a canales de correo electrónico (SMTP) y webhook con enrutamiento por severidad: cada regla de la política asigna una severidad (o cualquiera) a un canal y a una lista de destinatarios.

**El escalamiento es por política, no por severidad.** La política fija un único intervalo y un único tope. Una alarma que no se reconoce ni se limpia se vuelve a notificar por los mismos canales según ese calendario hasta alcanzar el tope. Una alarma lleva un solo reloj y un solo nivel de escalamiento por muchas políticas que la abarquen, así que el intervalo más corto entre ellas marca el ritmo de todas. No puedes darle a una severidad una cadencia propia.

Las políticas admiten además limitación de tasa: un intervalo mínimo entre notificaciones de la misma alarma, para que una alarma que señaliza repetidamente no inunde un canal.

Cuando dos políticas enrutan al mismo canal y a una lista de destinatarios **idéntica**, la entrega duplicada se colapsa y la notificación se envía una sola vez. Los mismos destinatarios en otro orden, o con otras mayúsculas y minúsculas, se tratan como distintos, y se envían ambas.

Las credenciales de canal (la contraseña SMTP, un token portador de webhook) se guardan en el almacén de secretos cifrado de la plataforma. Se sellan en reposo con cifrado envolvente, son de solo escritura a través de la API y nunca se devuelven en texto claro.

Esta vía de máquina a humano se mantiene distinta de los **[conectores salientes](./outbound-connectors.md)** de máquina a máquina que distribuyen eventos a otros sistemas.

## Ver alarmas y la salud de las reglas {#seeing-alarms--rule-health}

Las alarmas aparecen en vivo en dos lugares sin cableado adicional:

- La vista **Alarmas** de la consola: una lista en vivo, a nivel de todo el inquilino, filtrable y reconocible en el mismo lugar.
- **Widgets de panel**: un widget de **tabla de alarmas** en vivo y un widget de **conteo de alarmas** (consulta [Paneles](./dashboards.md)), con **acciones de reconocer/limpiar** que el servidor autoriza contra los propios derechos del operador.

Ambos se alimentan de suscripciones en vivo, de modo que los cambios de estado aparecen a medida que ocurren.

El propio editor de un perfil también muestra la **salud de las reglas** (estado por regla, hora del último disparo y conteo de disparos) junto a un **feed en vivo** de detecciones a medida que ocurren. Puedes confirmar que una regla recién publicada se comporta bien antes de que llegue a levantar una alarma.

## Cómo se opera el servicio {#running-it}

El servicio que evalúa las reglas mantiene estado vivo en memoria y detecta desde una única instancia activa; las réplicas adicionales quedan en espera. Eso le da algunas propiedades operativas que conviene conocer antes de depender de él en producción: qué cuesta un reinicio, con qué rapidez puede dispararse una regla de silencio, por qué una alarma podría no limpiarse y cómo encontrar una regla que está fallando al evaluarse. **[Cómo operar el motor de detección](../deployment/detection-engine.md)** las cubre.
