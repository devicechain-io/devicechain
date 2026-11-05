---
title: Acceso de IA (MCP)
---

# Acceso de IA (MCP)

Puedes dejar que un asistente de IA — Claude Desktop y Claude Code, Cursor, VS Code — trabaje con un inquilino de DeviceChain en nombre de un usuario, a través de un servidor **Model Context Protocol (MCP)**. El cliente LLM se conecta, descubre un conjunto de herramientas y las invoca para responder preguntas sobre tu flota: *"¿qué dispositivos del Edificio 3 no han reportado en la última hora?"*, *"resume las alarmas de hoy para los activos de almacenamiento en frío"*, *"¿cuál es la última temperatura del termostato T-114?"*

El servidor se construye sobre un principio: **un agente de IA nunca puede hacer más que la persona que lo autorizó.** No es una puerta de enlace amplia y con permisos excesivos. Es una capa delgada, curada y de solo lectura sobre la API GraphQL existente de la plataforma, y porta el propio token con alcance de inquilino del usuario que inició sesión.

:::note Estado
**Disponible hoy (de solo lectura):** un servicio `mcp` opcional con once herramientas de lectura curadas, respaldado por un servidor de autorización OAuth 2.1 completo en `user-management`. **Planeado:** herramientas de escritura (enviar comando, reconocer/limpiar alarma) detrás de un alcance elevado y una confirmación humana obligatoria, y registro dinámico de clientes (RFC 7591). Este repositorio es la fuente de verdad de lo que se compila actualmente.
:::

El servidor de autorización admite el flujo de código de autorización con PKCE, metadatos RFC 8414, rotación de tokens de actualización y vinculación de audiencia RFC 8707. Hasta que llegue el registro dinámico de clientes, un administrador registra los clientes.

## Qué puede hacer un asistente

El servidor expone once herramientas de **lectura**. Cada una es una consulta contra la misma API GraphQL que usa la consola, ejecutada bajo el token del solicitante. Por eso una herramienta devuelve exactamente lo que ese usuario, en ese inquilino, tiene permitido ver, y nada más.

**Dispositivos**

- `list_devices` — lista dispositivos, con filtrado.
- `get_device` — los detalles de un dispositivo individual.
- `get_device_capabilities` — qué puede medir un dispositivo y los comandos publicados que acepta, con el esquema de parámetros de cada comando.

`list_devices` y `get_device` todavía no indican si un dispositivo está deshabilitado, y `list_devices` no puede filtrar por ello. Un dispositivo deshabilitado aparece exactamente igual que uno activo, así que con estas herramientas un asistente no puede distinguir un dispositivo en cuarentena de uno que funciona. Comprueba el estado del dispositivo en la consola, donde un dispositivo deshabilitado lleva el distintivo **Deshabilitado**; consulta [Deshabilitar y habilitar un dispositivo](../user-guide/devices.md).

**Estado en vivo y telemetría**

- `get_device_state` — el estado actual de último valor conocido del dispositivo, incluido si ese estado lo *reportó el transporte* o se *infirió del silencio*. La diferencia cambia lo que significa «no activo». Reportado significa que se sabe que el dispositivo está desconectado. Inferido significa solo que no ha llegado nada recientemente, que es también el aspecto que tiene un dispositivo sano con un intervalo de reporte lento.
- `get_latest_measurements` — el valor más reciente por medición.
- `query_measurements` — lecturas de series temporales sin procesar en un rango de tiempo.
- `aggregate_measurements` — agregados por intervalos (min/max/promedio y similares) en un rango. `startTime` e `intervalSeconds` son obligatorios, `endTime` toma por defecto la hora actual y una solicitud tiene un máximo de 10 000 intervalos y de 50 000 filas (los intervalos multiplicados por el número de nombres de medición devueltos).

**Posición**

- `query_locations` — las posiciones reportadas de un dispositivo en una ventana de tiempo opcional, paginadas y acotadas. Los resultados vienen de más reciente a más antiguo, así que el primero es la última posición conocida del dispositivo; pedir un único resultado sin ventana responde «¿dónde está ahora?». Cada posición lleva latitud y longitud. Cuando el receptor los reportó, también lleva elevación, precisión, velocidad y rumbo. Un campo ausente no se reportó, y significa desconocido, no cero. Leer posiciones requiere dos cosas que las demás herramientas no necesitan. El cliente también debe estar autorizado para el alcance aparte `location`; un cliente que quiera ambos pide `read-only location`. Además, el usuario debe tener el permiso **location**, que deliberadamente no forma parte de la base de solo lectura de un visor. Por eso esta herramienta en concreto puede rechazarse para un solicitante al que el resto de herramientas de lectura le funcionan.

**Alarmas**

- `list_alarms` — alarmas, con filtrado por estado y entidad.
- `get_alarm` — los detalles de una sola alarma.

**Comandos**

- `list_commands` — los comandos emitidos a un dispositivo y su estado.

No existe una herramienta genérica de "ejecutar esta consulta GraphQL". Las lecturas sensibles — credenciales, el registro de auditoría, destinatarios de notificaciones, secretos de aprovisionamiento — quedan deliberadamente fuera del conjunto de herramientas.

## El modelo de seguridad

MCP se está convirtiendo en una forma estándar de dar a los asistentes de IA capacidades reales. El riesgo es que una implementación descuidada entregue a un agente una clave poderosa y de alcance amplio. El servidor de DeviceChain está diseñado para que eso no pueda ocurrir.

- **Porta el token del usuario, nunca un token de servicio.** El servidor MCP no posee ninguna credencial privilegiada de plataforma. Cada llamada a una herramienta reenvía el JWT validado y con alcance de inquilino del *solicitante* al servicio GraphQL subyacente, de modo que el agente alcanza exactamente lo que alcanza el usuario. Dar a una IA una identidad de servicio propia le permitiría actuar entre inquilinos a petición de cualquiera, y eso es lo único que este diseño rechaza.
- **El inquilino se fija en el momento de la concesión, no se pasa como parámetro.** La autorización decide en qué inquilino puede actuar el token, y el token lleva ese inquilino. Ninguna herramienta recibe un argumento de "inquilino" que un agente pudiera cambiar.
- **Los tokens están vinculados a una audiencia.** Un token de acceso emitido para el servidor MCP nombra a ese servidor como su audiencia prevista (RFC 8707) y se rechaza en cualquier otro lugar. Un token acuñado para un recurso no puede reproducirse contra otro.
- **De solo lectura, y curado.** Todas las herramientas son consultas. No hay ruta de escritura, ni escotilla de escape de consulta genérica, ni exposición de objetos sensibles.
- **Cada llamada se autentica y se vuelve a verificar.** En cada solicitud, el servidor valida el token portador contra las claves públicas de `user-management` y hace cumplir un alcance de solo lectura. Después, el servicio GraphQL subyacente vuelve a aplicar de forma independiente las mismas verificaciones de inquilino y rol que recibe la consola.

Conecta un asistente y podrá *leer dispositivos, estado, mediciones y alarmas* de tu inquilino. No puede alcanzar otro inquilino, cambiar nada ni ejecutar una consulta arbitraria.

## Qué se registra {#audit}

Cada llamada a una herramienta escribe una línea en el registro del servicio `mcp`, sea cual sea su resultado. Las lecturas no son entradas del diario de auditoría, que registra cambios e inicios de sesión, así que estas líneas son el único registro de lo que leyó un asistente. Una línea tiene este aspecto:

```json
{"instance":"prod","area":"mcp","audit":"mcp_tool_call","schema":1,"tenant":"acme","actor":"ana@example.com","client_id":"claude-desktop","sudo":false,"tool":"query_measurements","args_sha256":"5f1c…","outcome":"ok","duration_ms":42,"time":"2026-10-16T09:12:03Z","message":"MCP tool call"}
```

| Campo | Significado |
| --- | --- |
| `audit` | Siempre `mcp_tool_call`. Selecciona los registros por este campo: no llevan `level`. |
| `schema` | La versión de este formato, `1`. Un cambio en los campos se anuncia en las notas de la versión con un número nuevo. |
| `tenant`, `actor` | El inquilino y el usuario que nombra el token de acceso. El actor es el correo electrónico del usuario. |
| `client_id` | El cliente OAuth registrado al que se emitió el token, que indica qué aplicación hizo la llamada. |
| `sudo` | `true` cuando el usuario es superusuario. |
| `tool` | La herramienta llamada. Una llamada a una herramienta que el servidor no tiene se registra como `(unregistered)`, y el nombre que envió el cliente no se copia. |
| `args_sha256` | El SHA-256 de los argumentos de la llamada, con las claves ordenadas y sin espacios, de modo que solicitudes idénticas comparten valor. Los argumentos nunca se escriben. |
| `outcome` | `ok`, `error` (la herramienta rechazó la llamada o falló, incluidos argumentos que no acepta y un rechazo porque el usuario no tiene el permiso) o `rejected` (una llamada a una herramienta que el servidor no tiene). Una solicitud tan mal formada que ni siquiera nombra una herramienta la rechaza la capa de protocolo antes de que llegue a ninguna herramienta, y no escribe ninguna línea. |
| `duration_ms` | Cuánto tardó la llamada. |

Algunas propiedades de estas líneas importan cuando dependes de ellas:

- **Se escriben en todos los niveles de registro.** Poner `infrastructure.logging.level` en `warn` o `error` no las detiene.
- **Están en los registros del operador, no en la página de Auditoría de la consola.** Un administrador del inquilino no puede verlas. Consérvalas recogiendo la salida de registro del servicio; cuánto tiempo se guardan depende de tu canal de registros.
- **Nombran a personas.** `actor` es una dirección de correo electrónico. Eliminar un inquilino no alcanza las líneas ya recogidas, así que elimina las líneas de un inquilino eliminado en tu almacén de registros si tus reglas de retención lo exigen.
- **El resumen sirve para relacionar llamadas, no para ocultarlas.** Los argumentos son tokens de dispositivo, filtros y rangos de tiempo. Quien conozca los dispositivos de un inquilino puede recalcular el resumen de una solicitud que adivine. Ningún argumento de herramienta lleva una credencial.
- **Una línea por llamada.** Una línea ocupa unos cientos de bytes, así que el volumen sigue al ritmo de llamadas. Listar las herramientas y abrir una sesión no escriben nada.

## Cómo se conecta un cliente

El servidor MCP es un **servidor de recursos OAuth 2.1**, y `user-management` es su **servidor de autorización**. Conectar un cliente es un flujo OAuth estándar, no un intercambio de claves a medida:

1. El cliente lee los requisitos del servidor en sus metadatos de recurso protegido (RFC 9728), y luego encuentra el servidor de autorización en los metadatos de *ese* servidor (RFC 8414). Cada documento vive en una ruta well-known construida insertando el segmento well-known **entre** el host y la ruta del identificador. En una instancia en `iot.example.com` son `/.well-known/oauth-protected-resource/api/mcp` y `/.well-known/oauth-authorization-server/api/user-management`.
2. El cliente lleva al usuario por el flujo de código de autorización con PKCE (`/oauth/authorize`). El usuario inicia sesión, elige el inquilino a conceder y da su consentimiento. Todo se renderiza en el servidor, sin secreto compartido. La página inicia sesión al usuario con contraseña o, cuando su organización ha configurado el inicio de sesión único, con **Sign in with SSO**: el usuario escribe su correo de trabajo, inicia sesión en su proveedor de identidad y vuelve a la misma solicitud de autorización, que el servidor conservó. El consentimiento siempre se muestra, incluso si el proveedor de identidad inició la sesión en silencio. Un inquilino que exige inicio de sesión único rechaza el formulario de contraseña, así que una organización que solo usa SSO conecta clientes MCP de esta forma, y la concesión termina cuando termina ese inicio de sesión (su límite de sesión, o un administrador que cierra la sesión de la persona en todas partes).
3. El cliente intercambia el código por un token de acceso con alcance de inquilino en `/oauth/token`, y lo renueva según sea necesario. Los tokens de actualización son de un solo uso y rotan. La concesión depende del inicio de sesión hecho en la página de autorización, que es independiente de cualquier inicio de sesión en la consola, así que cerrar sesión en la consola no la termina. Dura como máximo la vida de un token de actualización (7 días por defecto) desde ese inicio de sesión, se renueve o no; restablecer la contraseña del usuario, desactivarlo o eliminarlo la termina antes. En ambos casos la siguiente renovación se rechaza y el cliente debe autorizarse de nuevo.
4. El cliente invoca las herramientas MCP con ese token, y cada llamada se ejecuta bajo los propios permisos del usuario.

Un administrador registra los clientes a través de la API de administración; los clientes no se registran a sí mismos. Así, un operador controla qué aplicaciones pueden solicitar acceso y con qué URIs de redirección.

### A dónde apuntar un cliente {#where-to-point-a-client}

Configuras una sola URL: el host público de la instancia más `/api/mcp`.

```
https://<your-instance-host>/api/mcp
```

Esa única cadena cumple tres funciones, y por eso es la única que necesitas:

- el **endpoint** al que el cliente envía por POST sus peticiones MCP;
- el **identificador de recurso** que el cliente manda como parámetro `resource` al pedir un token, y que el token lleva como su audiencia;
- el **punto de partida del descubrimiento**, del que se deriva todo lo demás.

No introduces nada más a mano. Cuando el endpoint responde `401`, el cliente lee la ubicación de los metadatos en la cabecera `WWW-Authenticate` de la respuesta y la sigue. Ese documento le dice dónde está el servidor de autorización, y el cliente pide entonces al servidor de autorización sus propios metadatos. El descubrimiento son esas tres peticiones, y puedes recorrerlas a mano antes de apuntar ningún cliente:

```bash
# 1. El endpoint responde 401 y nombra su documento de metadatos.
curl -i -X POST https://<your-instance-host>/api/mcp

# 2. Ese documento nombra el servidor de autorización.
curl https://<your-instance-host>/.well-known/oauth-protected-resource/api/mcp

# 3. El servidor de autorización describe dónde iniciar sesión y obtener un token.
curl https://<your-instance-host>/.well-known/oauth-authorization-server/api/user-management
```

El segmento well-known va **entre** el host y el resto de la ruta, no después. Parece raro al principio, pero es la ubicación que los estándares definen para un identificador que lleva una ruta, así que es la que un cliente construye por su cuenta. Para el segundo documento, la forma que parece más intuitiva, `https://<host>/api/mcp/.well-known/oauth-protected-resource`, sirve lo mismo, para los clientes que construyen la ruta así.

Las tres peticiones van sin autenticar: el descubrimiento es público por diseño y no devuelve ningún secreto. La petición 3 solo responde una vez que el servidor de autorización está activado; consulta [más abajo](#limits-and-boundaries) por qué es un paso aparte.

### Una sola réplica {#run-exactly-one-replica}

El servidor MCP mantiene la sesión de protocolo de cada cliente en memoria, en el pod que la creó. Las sesiones no se comparten entre pods y no hay afinidad de sesión. Con una segunda réplica, aproximadamente la mitad de las peticiones de cada cliente llegan a un pod que nunca ha oído hablar de la sesión, y se rechazan. El fallo es intermitente y su mensaje no menciona el escalado, así que parece un error del cliente.

:::warning Ejecuta exactamente una réplica
Instalar el área `mcp` con más de una réplica se rechaza de plano, en lugar de dejar que falle de forma intermitente en tiempo de ejecución.
:::

## Límites y fronteras {#limits-and-boundaries}

Algunos límites son decisiones y otros son cotas de la implementación actual. Cada grupo de abajo indica cuál.

**Deliberado, y sin previsión de cambio:**

- **Sin escrituras.** Enviar un comando o reconocer una alarma a través de MCP está planeado, pero solo detrás de un alcance elevado *y* una confirmación humana explícita. Un asistente nunca accionará un dispositivo en silencio.
- **Sin acceso entre inquilinos.** El token tiene alcance limitado a un inquilino, elegido por el usuario en el momento de la concesión. La pertenencia a un inquilino nunca es un parámetro de una herramienta, así que no hay ningún argumento que un agente pueda variar para alcanzar otro inquilino.
- **Sin consultas arbitrarias.** Solo el conjunto de herramientas curado es alcanzable; no existe `run_graphql`.
- **Ninguna credencial de servicio en ninguna ruta de código que ejecute.** Esto es más fuerte que «no usa un token de servicio»: ninguna ruta de código del servidor MCP echa mano de una credencial propia, así que un agente no tiene ninguna autoridad prestada que explotar. Toda lectura hacia abajo sale bajo el token del propio solicitante, y un agente sin permiso para leer algo recibe el mismo rechazo que recibiría una persona. (Su pod monta la configuración de instancia igual que la de cualquier otro servicio. Eso es fontanería de despliegue, no algo que el servidor use.)
- **Los payloads de los comandos no se devuelven.** `list_commands` te da el nombre, el estado y las marcas de tiempo de un comando. Lo que se envió al dispositivo queda fuera del contexto del agente.

**Cotas con las que te toparás antes que con cualquier otra cosa:**

- Los resultados se paginan de 25 en 25 por defecto y 100 como máximo, y una consulta de varios dispositivos admite como mucho 50 tokens por llamada. Un agente que examine una flota grande la recorre por páginas.
- El servidor lee como máximo 8 MiB de cualquier respuesta descendente. Eso acota lo que obtiene de las propias APIs de la plataforma; no es un límite que anuncie al agente.
- Una sesión caduca por inactividad a los 30 minutos.
- Un lote JSON-RPC lleva como máximo 16 mensajes, y cada llamador (el usuario del token) puede enviar 20 mensajes por segundo, con una ráfaga de 40; un lote de N cuenta como N. Un lote que supera el tope se rechaza completo con un error JSON-RPC (nada de lo que contiene se ejecuta), y un llamador que supera la tasa recibe HTTP 429 con una cabecera `Retry-After`. Para cambiarlos, defina `maxBatchEntries`, `callsPerSecond` y `callsBurst` en `functionalAreas.mcp.config`; `callsBurst` no puede ser menor que `maxBatchEntries`, y un valor no válido detiene el servicio al arrancar.

**Habilitar el servicio no basta para poder usarlo.** Hay dos interruptores independientes. Activar solo el primero es la forma habitual de acabar con un servidor que responde pero al que no se puede llegar:

1. El área funcional `mcp` no forma parte de un despliegue predeterminado; un operador la habilita explícitamente.
2. El servidor de autorización en `user-management` permanece apagado hasta que se configura una URL de emisor. Hasta entonces, `mcp` arranca y sirve sus metadatos, pero **ningún cliente puede obtener un token**. Es un interruptor aparte a propósito: fijar el emisor cambia un claim de todos los tokens que emite la instancia, no solo de los que usa MCP.

## Relacionado

- **[Multitenencia](./multi-tenancy.md)** — cómo se hace cumplir el aislamiento de inquilinos, en lo que se apoya el token MCP.
- **[Arquitectura](./architecture.md)** — dónde se ubica el servicio `mcp`, y el modelo de [manejo de secretos](./architecture.md#secret-handling) para credenciales.
- **[API GraphQL](../reference/graphql-api.md)** — la API que respaldan las herramientas MCP.
