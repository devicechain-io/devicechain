---
title: Autoría Asistida por IA
---

# Autoría Asistida por IA

Una regla de detección puede crearse de tres maneras en DeviceChain: un **formulario** tipado, un **lienzo de automatización** visual y, con el servicio de IA habilitado, una puerta de **"Describir"** en lenguaje sencillo. Escribes *"levanta una alarma alta cuando la temperatura de un congelador se mantenga por encima de -15°C durante más de diez minutos"* y la plataforma redacta una regla que puedes revisar, ajustar y publicar.

Las tres puertas convergen en el **mismo esquema de reglas estructurado** y pasan por el **mismo compilador**. Ese es todo el diseño: la IA es una puerta de entrada más hacia un único backend determinista — nunca un segundo motor, y nunca parte de la ruta de eventos en vivo.

:::note Estado
**Disponible hoy:** un servicio `ai-inference` opcional (en el perfil de despliegue `full`); un registro de **proveedores de IA** registrados por el operador con manejadores de clave de solo escritura; una puerta de **"Describir"** en lenguaje natural en la superficie de autoría de reglas del perfil de dispositivo — la mutación `draftDetectionRuleFromText`, que llama al servicio de IA y ejecuta un ciclo acotado de compilación y reparación (reside en la API que posee el **compilador** de reglas, no en la que las almacena: redactar un borrador es una operación de tiempo de compilación, y el borrador que devuelve se guarda después por la API ordinaria de creación de reglas); consentimiento de aceptación por inquilino; y limitación de tasa de IA por inquilino con métricas de gasto. **Planeado:** un **presupuesto de gasto** de IA duradero por inquilino (un tope de costo estricto — la limitación de tasa y la observabilidad de gasto ya están disponibles hoy). Este repositorio es la fuente de verdad de lo que actualmente se construye.
:::

## La IA propone, el compilador dispone

Cada superficie de autoría produce una regla candidata; el **compilador CEL** luego la analiza, verifica sus tipos y aplica límites de costo antes de que pueda guardarse — y una regla mal formada, con tipos incorrectos, o que supere el tope de costo de un inquilino es **rechazada al publicar, antes de que llegue a ejecutarse nunca**. La puerta de IA no es la excepción: el modelo *propone* una candidata, y el compilador *dispone* — acepta o rechaza — exactamente igual que con una regla dibujada a mano en el lienzo.

Este es el **límite de determinismo**, y es una línea firme:

- La IA (y el lienzo) se sitúan **únicamente** en el lado de la autoría. Te ayudan a escribir una regla.
- La **regla compilada** — CEL determinista sobre el motor de streaming con clave — es lo que se ejecuta, y es [correcta ante reprocesamiento (replay-correct)](./event-processing.md) por construcción.
- **Ni el modelo ni el lienzo se sitúan jamás en la ruta de detección replay-correct.** Un reinicio vuelve a derivar disparos idénticos a partir de la regla compilada; el modelo que ayudó a redactarla no está en ese ciclo en ningún momento.

Cuando usas la puerta Describir, el servicio ejecuta un **ciclo acotado de compilación y reparación**: redacta una candidata, la compila y — si el compilador la rechaza — retroalimenta el error para un número limitado de intentos de reparación. Lo que recibes es una candidata que ya compila. Tú sigues revisándola y publicándola por tu cuenta; nada queda activado en tu nombre.

## Los proveedores de IA son configuración del operador

La IA es configuración **registrada por el operador, con alcance de instancia** — no algo que un inquilino trae consigo. Un operador registra uno o más **proveedores de IA** en el plano de administración (`/admin/ai-providers`), cada uno con un tipo, un endpoint, un modelo y una **clave de API**.

La clave de API es un **manejador de secreto de solo escritura** ([almacén de secretos](./architecture.md)): se sella al escribirla, se resuelve internamente en el servidor en el momento de la inferencia y **nunca se devuelve** — el lado de lectura de un proveedor solo expone si una clave está configurada (`hasSecret`), nunca el valor. La vista de detalle del proveedor está organizada en pestañas **Básico / Conexión / Prueba**, y una acción de **Prueba** verifica la conectividad sin exponer la clave.

El uso de modelos externos es de **aceptación por inquilino y falla cerrado**: un inquilino debe dar su consentimiento antes de que se ejecute cualquier inferencia externa en su nombre, y la inferencia **falla cerrado** ante cualquier vacío en la cadena — sin consentimiento, sin proveedor, un proveedor deshabilitado o sin clave, todo se resuelve en "sin inferencia", nunca en un respaldo silencioso.

## La IA es un derecho de uso escalonado

Qué modelo ejecuta realmente un inquilino está gobernado por su [**nivel de inquilino (tenant tier)**](./tenant-tiers.md), y las reglas son deliberadamente estrictas:

- Un operador concede proveedores/modelos a **niveles**, y (opcionalmente) a inquilinos individuales.
- El modelo en uso para una capacidad dada es una asignación **`(inquilino, función) → modelo`** que recae en el **valor predeterminado del nivel** si no está definida.
- El servidor **nunca infiere** un valor predeterminado. Una concesión no es un predeterminado; no existe un indicador de "hacer predeterminado". Si un nivel no empaqueta ningún modelo, el inquilino **no tiene modelo** — *sin menú significa sin modelo*.
- Una asignación que apunta **fuera** del menú vigente se resuelve en **NINGUNO** — nunca en una sustitución silenciosa.

Los usuarios **no** eligen un modelo por tarea. La elección de modelo es configuración del operador, establecida una vez por función en la configuración del inquilino, no un parámetro de ninguna solicitud. La única función de IA en el vocabulario de disponibilidad general (GA) es la **redacción de reglas**; el mecanismo se generaliza a futuras funciones sin cambiar el contrato.

## Dónde vive en la consola

- **Puerta Describir** — en la superficie de autoría de reglas de detección del perfil de dispositivo, junto al constructor de formularios y el lienzo de automatización, y ofrecida al **crear** una regla nueva (una regla existente se edita en el formulario o en el lienzo). Escribe una descripción, revisa la regla redactada, publica.
- **Proveedores de IA** — `/admin/ai-providers` (plano de administración): registra proveedores, configura claves, prueba la conectividad.
- **Empaquetado de IA** — la matriz de concesión entre niveles que mapea qué modelos puede usar cada nivel.
- **Modelo por inquilino** — configurado en la página de detalle del inquilino, por función, desde el menú derivado del nivel.

## Límites y fronteras {#limits-and-boundaries}

### Lo que la IA nunca toca

- Nunca se ejecuta en la ruta [de detección/REACT](./event-processing.md) en vivo — esa ruta es CEL determinista, replay-correct, sin modelo.
- Nunca ve los datos de otro inquilino, y no es una puerta trasera privilegiada. (Distinto de la [superficie MCP](./mcp.md), donde un *agente* de IA opera la plataforma bajo el propio token con alcance de inquilino de un usuario.)
- Los datos comerciales del inquilino (nombres de dispositivos, valores de atributos) y los secretos no son del modelo para exponer; las claves permanecen de solo escritura en el almacén de secretos.
- **No escribe nada.** Una regla redactada se devuelve para que una persona la revise y la guarde por la vía de autoría habitual, bajo su propio token. La llamada de redacción no persiste nada por sí misma.

### No se admiten claves propias del inquilino (BYOK), y no se admitirán

Un inquilino no puede aportar su propia clave de proveedor. Los proveedores son **configuración del operador a nivel de instancia**: un operador los registra, custodia las claves y decide qué niveles e inquilinos pueden usar qué modelos. La única palanca por inquilino es el indicador de consentimiento para la inferencia externa, y tampoco es de autoservicio: un inquilino puede leerlo, pero solo un operador puede fijarlo.

Esto es una decisión, no una carencia. Un cliente que necesite ejecutarse con su propia clave y su propia cuenta necesita una instancia dedicada, que es donde acaba también cualquier otra petición de infraestructura por inquilino — y una clave por inquilino dentro de una instancia compartida sería la única pieza de ese aislamiento ofrecida sin el resto.

### Límites

- **Hoy se despacha un solo tipo de proveedor: Anthropic.** La entidad de proveedor está construida sobre una costura preparada para otros, y los demás tipos se rechazan en la escritura hasta que llegue su implementación, en lugar de aceptarse y quedar inertes.
- **El bucle de reparación está acotado.** Un candidato que el compilador rechaza se le devuelve, con el error del propio compilador, un número fijo y pequeño de veces, y después la petición falla. Nunca se relaja el compilador para que un borrador encaje.
- **Cada llamada de inquilino está limitada del lado del servidor** — tamaño del prompt, longitud de la salida, tiempo de espera y una tasa de peticiones por inquilino. Ninguno de estos valores lo aporta quien llama, y ninguno es ilimitado.
- **Una sola función.** El vocabulario de GA tiene exactamente una función de IA, la redacción de reglas, y es el servicio que llama quien la nombra — quien llama no puede elegir su propia función, porque elegir una función sería elegir un derecho de uso.

### Una excepción deliberada a la puerta de consentimiento

Un operador que prueba la conectividad de un proveedor desde la consola de administración llega al proveedor sin el indicador de consentimiento de ningún inquilino y **sin el límite de tasa por inquilino** — esa ruta resuelve el proveedor por token, así que no le aplica ninguna de las dos compuertas. La llamada es el prompt de un operador contra la configuración de un operador, ningún dato de inquilino cruza la frontera, y la clave debe resolverse igualmente. Toda ruta que transporte entrada de un inquilino pasa por ambas compuertas.

## Ver también

- [Procesamiento de Eventos y Alarmas](./event-processing.md) — el compilador y el motor contra los que redacta la IA.
- [Niveles y Empaquetado de Inquilinos](./tenant-tiers.md) — cómo se empaqueta el derecho de uso de modelos de IA.
- [Acceso de IA (MCP)](./mcp.md) — la superficie separada, de solo lectura, para agentes de IA que operan la plataforma.
