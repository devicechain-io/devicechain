---
title: Autoría Asistida por IA
---

# Autoría Asistida por IA

En DeviceChain puedes crear una regla de detección de tres maneras: un **formulario** tipado, un **lienzo de automatización** visual y, con el servicio de IA habilitado, una puerta **"Describir"** en lenguaje sencillo. Con la puerta Describir escribes *"levanta una alarma alta cuando la temperatura de un congelador se mantenga por encima de -15°C durante más de diez minutos"*, y la plataforma redacta una regla que puedes revisar, ajustar y publicar.

Las tres puertas convergen en el **mismo esquema de reglas estructurado** y pasan por el **mismo compilador**. La IA es una puerta de entrada más hacia un único backend determinista. Nunca es un segundo motor, y nunca forma parte de la ruta de eventos en vivo.

:::note Estado
**Disponible hoy:** un servicio `ai-inference` opcional (en el perfil de despliegue `full`); un registro de **proveedores de IA** registrados por el operador con manejadores de clave de solo escritura; la puerta **"Describir"** en lenguaje natural en la superficie de autoría de reglas del perfil de dispositivo, respaldada por la mutación `draftDetectionRuleFromText`; consentimiento de aceptación por inquilino; y limitación de tasa de IA por inquilino con métricas de gasto.

**Planeado:** un **presupuesto de gasto** de IA duradero por inquilino (un tope de costo estricto — la limitación de tasa y la observabilidad del gasto ya están disponibles hoy). Este repositorio es la fuente de verdad de lo que actualmente se construye.
:::

La mutación `draftDetectionRuleFromText` llama al servicio de IA y ejecuta un ciclo acotado de compilación y reparación. Reside en la API que posee el **compilador** de reglas, no en la que las almacena, porque redactar un borrador es una operación de tiempo de compilación. El borrador que devuelve lo guardas después por la API ordinaria de creación de reglas.

## Cómo se verifica una regla redactada {#ai-proposes-the-compiler-disposes}

Cada superficie de autoría produce una regla candidata. Después, el **compilador CEL** la analiza, verifica sus tipos y le aplica límites de costo antes de que pueda guardarse. Una regla mal formada, con tipos incorrectos o que supere el tope de costo de la plataforma es **rechazada al publicar, antes de llegar a ejecutarse**. La puerta de IA no es la excepción: el modelo propone una candidata, y el compilador la acepta o la rechaza exactamente igual que una regla dibujada a mano en el lienzo.

Este es el **límite de determinismo**, y es una línea firme:

- La IA (y el lienzo) se sitúan **únicamente** en el lado de la autoría. Te ayudan a escribir una regla.
- Lo que se ejecuta es la **regla compilada** — CEL determinista sobre el motor de streaming con clave. Por construcción, produce los mismos disparos cuando se reprocesan los eventos ([replay-correct](./event-processing.md)).
- **Ni el modelo ni el lienzo se sitúan jamás en la ruta de detección replay-correct.** Un reinicio vuelve a derivar disparos idénticos a partir de la regla compilada; el modelo que ayudó a redactarla no está en ningún punto de ese ciclo.

Cuando usas la puerta Describir, el servicio ejecuta un **ciclo acotado de compilación y reparación**. Redacta una candidata y la compila. Si el compilador la rechaza, el servicio le devuelve el error para un número limitado de intentos de reparación. Lo que recibes es una candidata que ya compila. Tú sigues revisándola y publicándola por tu cuenta; nada queda activado en tu nombre.

## Proveedores de IA {#ai-providers-are-operator-configuration}

La IA es configuración **registrada por el operador, con alcance de instancia**, no algo que aporte un inquilino. Un operador registra uno o más **proveedores de IA** en el plano de administración (`/admin/ai-providers`), cada uno con un tipo, un endpoint, un modelo y una **clave de API**.

La clave de API es un **manejador de secreto de solo escritura** ([almacén de secretos](./architecture.md)). Se sella al escribirla, se resuelve internamente en el servidor en el momento de la inferencia y **nunca se devuelve**. El lado de lectura de un proveedor solo expone si hay una clave configurada (`hasSecret`), nunca su valor. La vista de detalle del proveedor tiene las pestañas **Básico / Conexión / Prueba**, y una acción de **Prueba** verifica la conectividad sin exponer la clave.

El uso de modelos externos requiere **aceptación por inquilino**, y ante la duda se rechaza en lugar de recurrir a una alternativa. Un inquilino debe dar su consentimiento antes de que se ejecute cualquier inferencia externa en su nombre. Cualquier vacío en la cadena — sin consentimiento, sin proveedor, un proveedor deshabilitado o sin clave — se resuelve en "sin inferencia", nunca en un respaldo silencioso.

## Derecho de uso de modelos por nivel {#ai-is-a-tiered-entitlement}

El [**nivel de inquilino (tenant tier)**](./tenant-tiers.md) determina qué modelo ejecuta realmente un inquilino, y las reglas son deliberadamente estrictas:

- Un operador concede proveedores/modelos a **niveles** y (opcionalmente) a inquilinos individuales.
- El modelo en uso para una capacidad dada es una **asignación `(inquilino, función) → modelo`** que recae en el **valor predeterminado del nivel**.
- El servidor **nunca infiere** un valor predeterminado. Una concesión no es un predeterminado, y no existe un indicador de "hacer predeterminado". Si un nivel no empaqueta ningún modelo, el inquilino **no tiene modelo**: sin menú, no hay modelo.
- Una asignación que apunta **fuera** del menú vigente se resuelve en **NINGUNO**, nunca en una sustitución silenciosa.

Los usuarios **no** eligen un modelo por tarea. La elección de modelo es configuración del operador, establecida una vez por función en la configuración del inquilino, no un parámetro de ninguna solicitud. La única función de IA en el vocabulario de disponibilidad general (GA) es la **redacción de reglas**; el mecanismo se generaliza a futuras funciones sin cambiar el contrato.

## En la consola {#where-it-lives-in-the-console}

- **Puerta Describir** — en la superficie de autoría de reglas de detección del perfil de dispositivo, junto al constructor de formularios y el lienzo de automatización. Se ofrece al **crear** una regla nueva; una regla existente la editas en el formulario o en el lienzo. Escribe una descripción, revisa la regla redactada y publícala.
- **Proveedores de IA** — `/admin/ai-providers` (plano de administración): registra proveedores, configura claves y prueba la conectividad.
- **Empaquetado de IA** — la matriz de concesiones entre niveles que define qué modelos puede usar cada nivel.
- **Modelo por inquilino** — se configura en la página de detalle del inquilino, por función, a partir del menú derivado del nivel.

## Límites y fronteras {#limits-and-boundaries}

### Lo que la IA nunca toca

- Nunca se ejecuta en la ruta en vivo de [detección y acciones](./event-processing.md). Esa ruta es CEL determinista, replay-correct y sin modelo.
- Nunca ve los datos de otro inquilino, y no es una puerta trasera privilegiada. Esto es distinto de la [superficie MCP](./mcp.md), donde un *agente* de IA opera la plataforma bajo el propio token con alcance de inquilino de un usuario.
- Los datos de negocio del inquilino (nombres de dispositivos, valores de atributos) y los secretos no son del modelo para exponer; las claves permanecen de solo escritura en el almacén de secretos.
- **No escribe nada.** Una regla redactada se devuelve para que una persona la revise y la guarde por la vía de autoría habitual, bajo su propio token. La llamada de redacción no persiste nada por sí misma.

### No se admiten claves propias del inquilino (BYOK) {#bring-your-own-key-is-not-supported-and-will-not-be}

Un inquilino no puede aportar su propia clave de proveedor, y esto no va a cambiar. Los proveedores son **configuración del operador a nivel de instancia**: un operador los registra, custodia las claves y decide qué niveles e inquilinos pueden usar qué modelos. La única palanca por inquilino es el indicador de consentimiento para la inferencia externa, y tampoco es de autoservicio: un inquilino puede leerlo, pero solo un operador puede fijarlo.

Esto es una decisión, no una carencia. Un cliente que necesite ejecutarse con su propia clave y su propia cuenta necesita una instancia dedicada, que es donde acaba también cualquier otra petición de infraestructura por inquilino. Una clave por inquilino dentro de una instancia compartida sería la única pieza de ese aislamiento ofrecida sin el resto.

### Cotas

- **Hoy se incluyen dos tipos de proveedor: `anthropic` y `openai-compatible`.**
  - `anthropic` enruta a la API Claude de Anthropic. Su endpoint es una sobrescritura opcional de la URL base incorporada.
  - `openai-compatible` enruta a cualquier endpoint que hable la API de chat-completions de OpenAI — vLLM, Ollama, DeepSeek, el servidor de llama.cpp o una pasarela delante de ellos. Este tipo se define por su dirección y no por un fabricante, así que su endpoint es **obligatorio**. Un proveedor escrito sin él se rechaza en lugar de almacenarse inutilizable.
  - Ambos tipos cuentan como **externos** para la compuerta de consentimiento. El mismo protocolo de red sirve a un pod de vLLM dentro del clúster y a una API pública, y el tipo por sí solo no puede distinguirlos, así que un modelo `openai-compatible` autoalojado sigue necesitando la aceptación del inquilino.
  - La entidad de proveedor está diseñada para admitir otros tipos, pero estos se rechazan al escribir hasta que llegue su implementación, en lugar de aceptarse y quedar inertes.
- **El ciclo de reparación está acotado.** Una candidata que el compilador rechaza se le devuelve al modelo, con el propio error del compilador, un número fijo y pequeño de veces. Si para entonces ninguna candidata compila, el borrador vuelve sin éxito, con los motivos del compilador y el último intento del modelo para que veas qué probó. El ciclo nunca relaja el compilador para que un borrador encaje.
- **Cada llamada de inquilino está limitada del lado del servidor**: tamaño del prompt, longitud de la salida, tiempo de espera y una tasa de peticiones por inquilino. Ninguno de estos valores lo aporta quien llama, y ninguno es ilimitado.
- **Una sola función.** El vocabulario de GA tiene exactamente una función de IA, la redacción de reglas, y es el servicio que llama quien la nombra. Quien llama no puede elegir su propia función, porque elegir una función sería elegir un derecho de uso.

### Excepción: pruebas de conectividad de proveedores {#one-deliberate-exception-to-the-consent-gate}

Cuando un operador prueba la conectividad de un proveedor desde la consola de administración, la llamada llega al proveedor sin el indicador de consentimiento de ningún inquilino y **sin el límite de tasa por inquilino**. Esa ruta resuelve el proveedor por token, así que no le aplica ninguna de las dos compuertas. La excepción es deliberada: la llamada es el prompt de un operador contra la configuración de un operador, ningún dato de inquilino cruza la frontera, y la clave debe resolverse igualmente. Toda ruta que transporte entrada de un inquilino pasa por ambas compuertas.

## Ver también

- [Procesamiento de Eventos y Alarmas](./event-processing.md) — el compilador y el motor contra los que redacta la IA.
- [Niveles y Empaquetado de Inquilinos](./tenant-tiers.md) — cómo se empaqueta el derecho de uso de modelos de IA.
- [Acceso de IA (MCP)](./mcp.md) — la superficie separada, de solo lectura, para agentes de IA que operan la plataforma.
