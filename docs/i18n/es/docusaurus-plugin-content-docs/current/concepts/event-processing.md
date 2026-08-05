---
sidebar_position: 5
title: Procesamiento de eventos y alarmas
---

# Procesamiento de eventos y alarmas

DeviceChain convierte la telemetría bruta de los dispositivos en señales significativas. Un servicio dedicado **event-processing** observa los eventos a medida que fluyen por el pipeline: una etapa **DETECT** evalúa reglas en streaming en tiempo real, y una etapa **REACT** ejecuta las respuestas automatizadas que declara cada disparo — levantando una **alarma** (una condición con estado que tiene un ciclo de vida, una severidad y una vía para notificar a una persona) o emitiendo un **comando** de vuelta al dispositivo.

El servicio está construido para ser **correcto ante reprocesamiento (replay-correct)**: evalúa sobre el tiempo del evento y persiste su estado, de modo que un reinicio vuelve a derivar disparos idénticos — ninguno se pierde, ninguno se duplica.

:::note Estado
**Disponible hoy:** el pipeline DETECT + REACT es el motor de detección en vivo de la plataforma. La detección cubre condiciones de **umbral**, **sostenida por duración**, **ocurrencia repetida**, **tasa de cambio**, **silencio/ausencia**, **conectividad**, **agregado en ventana** y **correlación de área/grupo** (con umbrales estáticos o determinados por atributos); las acciones REACT son **levantar alarma**, **enviar comando**, **llamar a un webhook** y **publicar a un conector**, con guardas por acción. Las reglas se autoran de tres maneras sobre un mismo esquema — un **generador de formularios tipado**, un **lienzo visual de automatización** y, con el servicio de IA habilitado, una puerta de **"Describir"** en lenguaje natural — todas validadas por el mismo compilador antes de publicar, y el lienzo puede **previsualizar un borrador contra historial reproducido** antes de que entre en vigor. El ciclo de vida de alarma de cuatro estados, los niveles de severidad con escalamiento en el mismo lugar, las suscripciones en vivo a alarmas y detecciones, y la notificación por correo electrónico/webhook con escalamiento, ya están todos implementados.
:::

## Dónde viven las reglas

Las reglas de detección se definen sobre un **[perfil de dispositivo](./domain-model.md)** — el contrato de capacidades versionado compartido por uno o más tipos de dispositivo. Como el perfil está versionado (borrador → publicar → revertir), un cambio en la lógica de detección de una flota se autora como borrador, se publica de forma atómica y se puede revertir si hace falta, exactamente igual que las definiciones de métricas y comandos del perfil. Todo dispositivo que resuelve a ese perfil adopta sus reglas automáticamente.

Una regla plantea una **condición** sobre la telemetría del perfil, declara su **severidad** y enumera las **acciones** a ejecutar cuando se dispara.

## Delimitar una regla a un grupo

Por defecto, una regla se aplica a **todos los dispositivos** que resuelven a su perfil. Una regla también puede **delimitarse a un [grupo dinámico](./domain-model.md#facets-and-dynamic-groups)** para que se dispare solo para los dispositivos que son miembros en ese momento — por ejemplo, ejecutar una regla de calor más estricta únicamente en *dispositivos en zonas áridas*. La delimitación es opcional y se define por regla.

La membresía del grupo se registra en cada evento **en el momento en que se resuelve**, de modo que el motor ve exactamente qué reglas aplicaban en ese instante — incluso cuando reproduce historial para previsualizar o volver a derivar disparos. Cuando un dispositivo entra o sale del grupo, queda inscrito o excluido en su siguiente evento, sin editar ninguna regla ni reescanear nada.

## Tipos de condición {#condition-types}

| Condición | Se dispara cuando | Parámetros |
|---|---|---|
| **Umbral** | una lectura cruza una comparación (p. ej. `temperature > 80`) | la comparación + un valor umbral |
| **Duración** | la condición se sostiene continuamente durante al menos un tiempo definido (p. ej. `pressure low for 5 minutes`) | un tiempo de sostenimiento |
| **Repetición** | la condición ocurre un número de veces dentro de una ventana (p. ej. `3 faults in 10 minutes`) | un conteo de ocurrencias + una ventana |
| **Tasa de cambio** | una métrica cambia demasiado rápido entre lecturas consecutivas (p. ej. `temperature rising > 5°/s`) | la comparación + una marca opcional para normalizar el cambio a una tasa por segundo |
| **Ausencia / silencio** | un dispositivo deja de reportar — ningún evento calificado dentro de una ventana (una verificación de tipo "hombre muerto") | una ventana de silencio |
| **Conectividad** | un dispositivo reporta una desconexión autoritativa (levanta) y se reconecta (resuelve) — para transportes que afirman presencia como [Sparkplug-B](./sparkplug.md) y [LwM2M](./lwm2m.md). *Hoy se define a través de la API — ninguna de las superficies de autoría de la consola la ofrece todavía.* | ninguno — el borde de [presencia](./device-presence.md) es toda la señal |
| **Agregado en ventana** | un agregado sobre una ventana cruza una comparación (p. ej. `average > 50 over 10 minutes`) | la función (count/sum/avg/min/max), una ventana (tumbling, sliding o session), la comparación + valor |
| **Correlación de área** | suficientes dispositivos distintos en un área cumplen la condición en conjunto (p. ej. `≥ 3 devices in a zone report a fault within 5 minutes`) | el tipo de área/ancla, un conteo de dispositivos distintos + ventana |

La comparación de cada condición puede ser una hoja estructurada `metric · operator · value` o una **expresión CEL** avanzada sobre el evento; ambas se verifican de tipos de forma estática y se limitan en costo cuando se publica el perfil, de modo que una regla mal formada o desbocada se rechaza antes de poder ejecutarse.

### Umbrales estáticos y dinámicos

Un umbral puede ser un **valor fijo** en la regla, o **dinámico** — el nombre de un **atributo** de dispositivo que la regla lee en el momento de la evaluación. Un umbral dinámico permite que una sola regla se adapte por dispositivo: el perfil define la regla una vez, y cada dispositivo lleva su propio límite como un atributo con alcance `SERVER` o `SHARED` (los valores establecidos por el servidor tienen prioridad). Cambiar el atributo cambia el umbral efectivo sin editar la regla.

## Acciones automatizadas {#automated-actions}

Cuando una regla se dispara, se ejecutan sus acciones **REACT**. Las acciones integradas son:

- **Levantar alarma** — abre (o escala) una alarma con estado para el dispositivo, descrita más abajo. Esta es la acción por defecto y no necesita más objetivo que una severidad.
- **Enviar comando** — encola un comando de vuelta al dispositivo a través del pipeline persistente de comandos (el despacho es idempotente, de modo que un reprocesamiento o reintento nunca envía dos veces).
- **Llamar a un webhook** (`httpCall`) — hace POST de un payload con forma CEL a un endpoint HTTP externo, con entrega endurecida (se rechazan las redirecciones, se eliminan los encabezados reservados) y autenticación opcional vía el almacén de secretos.
- **Publicar a un conector** (`publish`) — entrega un payload con forma CEL a un **[conector saliente](./outbound-connectors.md)** que lo distribuye a un broker de mensajes o cola en la nube (MQTT, Kafka, AWS SNS/SQS).

Las dos últimas — las acciones salientes — se describen en **[Conectores salientes](./outbound-connectors.md)**; se entregan mediante un servicio separado para que un sistema externo lento nunca ralentice la detección.

Una regla puede llevar varias acciones (hasta un pequeño límite fijo), y cada acción puede estar **protegida por una guarda** que es una condición sobre el disparo — así, por ejemplo, una regla puede levantar una alarma en cada disparo pero enviar un comando solo cuando la lectura esté en una banda particular. Como un disparo es **activado por flanco** (un flanco ascendente cuando la condición empieza a sostenerse, un flanco descendente cuando deja de hacerlo), una alarma levantada en el flanco ascendente se limpia automáticamente en el flanco descendente — usted autora el levantamiento, y la limpieza queda implícita.

## Autoría y previsualización de reglas

Las reglas se autoran en la consola de tres maneras, todas sobre el mismo esquema y todas validadas por el **mismo compilador del lado del servidor** antes de publicar:

- Un **generador de formularios** — un formulario tipado por tipo de condición, la vía más rápida para una sola regla. A medida que edita, muestra en línea la retroalimentación de tipos y costo del compilador, antes de publicar.
- Un **lienzo visual de automatización** — un grafo de nodos (fuente → condición → ramas opcionales → acciones) para flujos más ricos. El lienzo **compila a la misma regla** que produciría un formulario; es una superficie de autoría, no un segundo motor. Añade nodos de **rama** (enrutar un disparo a distintas acciones mediante una guarda) y nodos de **cómputo** (nombrar un valor derivado reutilizable y referenciarlo en una condición o guarda).
- Una puerta de **"Describir"** en lenguaje natural — donde el servicio de IA está habilitado, describa la regla con palabras y reciba una candidata redactada para que la revise y publique. Se ofrece al crear una regla nueva, y produce una regla en el mismo esquema que producen las otras dos puertas. Vea [Autoría Asistida por IA](./ai-authoring.md).

Lo distintivo del lienzo es la **previsualización contra historial**: ejecute una regla en *borrador* sobre el historial de eventos reproducido del perfil y vea los flancos de levantamiento/resolución que *habría* producido en una ventana elegida — sin publicar nada. Al seleccionar un disparo se superpone una **traza por nodo** sobre el lienzo, mostrando el camino que tomó ese evento (qué condición coincidió, qué rama tomó, qué acción se disparó). Puede editar y volver a previsualizar hasta que la regla haga lo que espera, y luego publicarla.

## El ciclo de vida de la alarma

Una alarma levantada es un **objeto con estado**, no un mensaje único. Su estado es una combinación de dos ejes — un **modelo de cuatro estados**:

- **Estado** — `ACTIVE` mientras la condición se sostiene, `CLEARED` una vez que se resuelve.
- **Reconocida (Acknowledged)** — si un operador ha tomado posesión de la alarma (con un registro de quién y cuándo).

Así, una alarma pasa por `ACTIVE/no reconocida` → `ACTIVE/reconocida` → `CLEARED`, y una condición intermitente reactiva la *misma* alarma en lugar de generar duplicados.

Una alarma nombra el dispositivo que la levantó, y las alarmas se consultan a nivel de todo el inquilino con filtros (estado, severidad, reconocimiento, dispositivo de origen) en lugar de leerse desde una entidad superior.

### Severidad y escalamiento

Cada alarma lleva una **severidad** — `CRITICAL`, `MAJOR`, `MINOR`, `WARNING` o `INDETERMINATE`. Una sola condición puede declarar reglas en varios niveles de severidad (por ejemplo `temp > 80 → MAJOR`, `temp > 100 → CRITICAL`); el motor **escala en el mismo lugar una única alarma activa** hasta el nivel más alto que se cumple en ese momento, y la desescala a medida que las condiciones ceden — en lugar de abrir una alarma separada por nivel.

## Llegar a una persona

Una alarma levantada puede notificar a las personas a través del sistema de **notificaciones**. Una política por inquilino enruta las alarmas a canales de **correo electrónico (SMTP)** y **webhook**, con **escalamiento** por severidad — una alarma que no se reconoce ni se limpia se **vuelve a notificar por los mismos canales** según el calendario de la política, hasta un tope — y **limitación de tasa**, un intervalo mínimo entre notificaciones de la misma alarma para que una alarma que señaliza repetidamente no inunde un canal. Cuando dos políticas enrutan al mismo canal y a los mismos destinatarios, la entrega duplicada se colapsa y la notificación se envía una sola vez. Las credenciales de canal (la contraseña SMTP, un token portador de webhook) se guardan en el **almacén de secretos cifrado** de la plataforma — sellado en reposo con cifrado envolvente, de escritura únicamente a través de la API, y nunca devuelto en texto claro. Esta vía de máquina a humano se mantiene distinta de los **[conectores salientes](./outbound-connectors.md)** de máquina a máquina que distribuyen eventos a otros sistemas.

## Ver alarmas y la salud de las reglas

Las alarmas aparecen en vivo en dos lugares sin cableado adicional:

- La vista **Alarmas** de la consola — una lista en vivo, a nivel de todo el inquilino, filtrable y reconocible en el mismo lugar.
- **Widgets de panel** — un widget de **tabla de alarmas** en vivo y un widget de **conteo de alarmas** (ver [Paneles](./dashboards.md)), incluyendo **acciones de reconocer/limpiar** que el servidor autoriza contra los propios derechos del operador.

Ambos se alimentan de suscripciones en vivo, de modo que los cambios de estado aparecen a medida que ocurren.

El propio editor de un perfil también muestra la **salud de las reglas** — estado por regla, hora del último disparo y conteo de disparos — junto a un **feed en vivo** de detecciones a medida que ocurren, para que pueda confirmar que una regla recién publicada se está comportando bien antes de que llegue a levantar una alarma.

## Cómo se opera

El servicio que evalúa las reglas mantiene estado vivo en memoria y se ejecuta como una única
instancia, lo que le da algunas propiedades operativas que conviene conocer antes de depender de él
en producción: qué cuesta un reinicio, con qué rapidez puede dispararse una regla de silencio, por
qué una alarma podría no limpiarse y cómo encontrar una regla que está fallando al evaluarse. Todo
ello se cubre en **[Cómo operar el motor de detección](../deployment/detection-engine.md)**.
