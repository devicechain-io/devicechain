---
title: Acceso de IA (MCP)
---

# Acceso de IA (MCP)

Los asistentes de IA — Claude Desktop y Claude Code, Cursor, VS Code — pueden operar un inquilino de DeviceChain en nombre de un usuario a través de un servidor **Model Context Protocol (MCP)**. Un cliente LLM se conecta, descubre un conjunto de herramientas y las invoca para responder preguntas sobre tu flota: *"¿qué dispositivos del Edificio 3 no han reportado en la última hora?"*, *"resume las alarmas de hoy para los activos de almacenamiento en frío"*, *"¿cuál es la última temperatura del termostato T-114?"*

El servidor MCP de DeviceChain se construye alrededor de un único principio: **un agente de IA nunca puede hacer más que la persona que lo autorizó.** En lugar de una puerta de enlace amplia y sobre-permisionada, es una capa delgada, curada y de solo lectura sobre la API GraphQL existente de la plataforma, que porta el propio token con alcance de inquilino del usuario autenticado.

:::note Estado
**Disponible hoy (de solo lectura):** un servicio `mcp` opcional que expone once herramientas de lectura curadas, respaldado por un servidor de autorización OAuth 2.1 completo en `user-management` (flujo de código de autorización con PKCE, metadatos RFC 8414, rotación de tokens de actualización, vinculación de audiencia RFC 8707). **Planeado:** herramientas de escritura (enviar comando, reconocer/limpiar alarma) detrás de un alcance elevado y una confirmación obligatoria con humano en el bucle; y registro dinámico de clientes (RFC 7591) — hoy los clientes son registrados por un administrador. Este repositorio es la fuente de verdad de lo que actualmente está implementado.
:::

## Qué puede hacer un asistente

El servidor expone once herramientas de **lectura**. Cada una es una consulta contra la misma API GraphQL que usa la consola, ejecutada bajo el token del solicitante — de modo que una herramienta devuelve exactamente lo que ese usuario, en ese inquilino, tiene permitido ver, y nada más.

**Dispositivos**

- `list_devices` — lista dispositivos, con filtrado.
- `get_device` — los detalles de un dispositivo individual.
- `get_device_capabilities` — qué puede medir un dispositivo, y los comandos **publicados** que acepta (con el esquema de parámetros de cada comando).

**Estado en vivo y telemetría**

- `get_device_state` — el estado actual de último valor conocido de un dispositivo, incluido si ese estado lo **reportó el transporte** o se **infirió del silencio**. La distinción cambia lo que significa «no activo»: reportado significa que se sabe que el dispositivo está desconectado; inferido significa solo que no ha llegado nada recientemente, que es también el aspecto que tiene un dispositivo sano con un intervalo de reporte largo.
- `get_latest_measurements` — el valor más reciente por medición.
- `query_measurements` — lecturas de series temporales sin procesar en un rango de tiempo.
- `aggregate_measurements` — agregados por intervalos (min/max/promedio y similares) en un rango.

**Posición**

- `query_locations` — las posiciones reportadas de un dispositivo en una ventana de tiempo opcional, paginadas y acotadas. Los resultados vienen **de más reciente a más antiguo**, así que el primero es la última posición conocida del dispositivo; pedir un único resultado sin ventana responde «¿dónde está ahora?». Cada posición lleva latitud y longitud y, cuando el receptor las reportó, elevación, precisión, velocidad y rumbo — un campo ausente no se reportó, y significa desconocido, nunca cero. Leer posiciones requiere el permiso de **ubicación** aparte, que deliberadamente no forma parte de la base de solo lectura de un visor, así que esta herramienta en concreto puede rechazarse para un solicitante al que el resto de herramientas de lectura le funcionan.

**Alarmas**

- `list_alarms` — alarmas, con filtrado por estado y entidad.
- `get_alarm` — los detalles de una sola alarma.

**Comandos**

- `list_commands` — los comandos emitidos a un dispositivo y su estado.

**No** existe una herramienta genérica de "ejecutar esta consulta GraphQL", y las lecturas sensibles — credenciales, el registro de auditoría, destinatarios de notificaciones, secretos de aprovisionamiento — quedan deliberadamente excluidas del conjunto de herramientas.

## El modelo de seguridad

MCP se está convirtiendo en una forma estándar de dar a los asistentes de IA capacidades reales, y el riesgo es que una implementación descuidada entregue a un agente una clave poderosa y de alcance amplio. El servidor de DeviceChain está diseñado para que eso estructuralmente no pueda ocurrir.

- **Porta el token del usuario — nunca un token de servicio.** El servidor MCP no posee ninguna credencial privilegiada de plataforma. Cada llamada a una herramienta reenvía el JWT validado y con alcance de inquilino del *solicitante* al servicio GraphQL subyacente, de modo que el alcance del agente es exactamente el alcance del usuario. (Entregarle a una IA una identidad de servicio crearía un "diputado confundido" (confused deputy) que podría actuar entre inquilinos — lo único que este diseño rechaza.)
- **El inquilino se fija en el momento de la concesión, no se pasa como parámetro.** En qué inquilino puede actuar el token se decide durante la autorización, y luego queda incorporado en el token. Ninguna herramienta recibe un argumento de "inquilino" que un agente pudiera cambiar.
- **Los tokens están vinculados a una audiencia.** Un token de acceso emitido para el servidor MCP está sellado con ese servidor como su audiencia prevista (RFC 8707) y se rechaza en cualquier otro lugar — un token acuñado para un recurso no puede reproducirse contra otro.
- **De solo lectura, y curado.** Todo el conjunto de herramientas son consultas. No hay ruta de escritura, ni escotilla de escape de consulta genérica, ni exposición de objetos sensibles.
- **Cada llamada se autentica y se vuelve a verificar.** El servidor valida el token portador contra las claves públicas de `user-management` en cada solicitud y hace cumplir un alcance de solo lectura; el servicio GraphQL subyacente vuelve a aplicar de forma independiente las mismas verificaciones de inquilino y rol que obtiene la consola.

El resultado: conecta un asistente, y podrá *leer dispositivos, estado, mediciones y alarmas* de tu inquilino — y físicamente no puede alcanzar otro inquilino, mutar nada, ni ejecutar una consulta arbitraria.

## Cómo se conecta un cliente

El servidor MCP es un **servidor de recursos OAuth 2.1**, y `user-management` es su **servidor de autorización** — de modo que conectar un cliente es un flujo OAuth estándar, no un intercambio de claves a medida:

1. El cliente descubre los requisitos del servidor a partir de sus metadatos de recurso protegido (RFC 9728), y luego encuentra el servidor de autorización a partir de *sus* metadatos (RFC 8414). Ambos documentos viven en una ruta well-known construida insertando el segmento well-known **entre** el host y la ruta del identificador — así que en una instancia en `iot.example.com` son `/.well-known/oauth-protected-resource/api/mcp` y `/.well-known/oauth-authorization-server/api/user-management`.
2. El usuario pasa por el **flujo de código de autorización con PKCE** (`/oauth/authorize`): inicia sesión, elige el inquilino a conceder, y da su consentimiento — todo renderizado por el servidor, sin secreto compartido.
3. El cliente intercambia el código por un token de acceso con alcance de inquilino en `/oauth/token`, y lo renueva según sea necesario (los tokens de actualización son de un solo uso y rotan).
4. El cliente invoca las herramientas MCP con ese token; cada llamada se ejecuta bajo los propios permisos del usuario.

Los clientes son **registrados por un administrador** (a través de la API de administración) en lugar de autorregistrarse, de modo que un operador controla qué aplicaciones pueden solicitar acceso y con qué URIs de redirección.

### A dónde apuntar un cliente {#where-to-point-a-client}

Hay una sola URL que configurar, y es el host público de la instancia más `/api/mcp`:

```
https://<host-de-tu-instancia>/api/mcp
```

Esa única cadena es tres cosas a la vez, y por eso es la única que necesitas:

- el **endpoint** al que el cliente envía por POST sus peticiones MCP;
- el **identificador de recurso** que el cliente manda como parámetro `resource` al pedir un token, y con el que el token queda sellado como audiencia;
- el **punto de partida del descubrimiento** — todo lo demás se deriva de él.

No hay que introducir nada más a mano. Un cliente que recibe el `401` de ese endpoint lee la ubicación de los metadatos en la cabecera `WWW-Authenticate` de la respuesta, la sigue, y a partir de ese documento averigua dónde está el servidor de autorización y le pide *sus* propios metadatos. El descubrimiento son esas tres peticiones, y puedes recorrerlas a mano antes de apuntar ningún cliente:

```bash
# 1. El endpoint responde 401 y nombra su documento de metadatos.
curl -i -X POST https://<host-de-tu-instancia>/api/mcp

# 2. Ese documento nombra el servidor de autorización.
curl https://<host-de-tu-instancia>/.well-known/oauth-protected-resource/api/mcp

# 3. El servidor de autorización describe dónde iniciar sesión y obtener un token.
curl https://<host-de-tu-instancia>/.well-known/oauth-authorization-server/api/user-management
```

Las rutas well-known resultan extrañas la primera vez: el segmento well-known va **entre** el host y el resto de la ruta, no después. Esa es la ubicación que los estándares definen para un identificador que lleva una ruta, así que es la que un cliente construye por su cuenta. Para el segundo documento, la forma que parece más intuitiva, `https://<host>/api/mcp/.well-known/oauth-protected-resource`, sirve lo mismo, para los clientes que la construyen así.

Las tres peticiones son sin autenticar — el descubrimiento es público por diseño y no devuelve ningún secreto. La petición 3 solo responde una vez que el servidor de autorización está encendido; abajo se explica [por qué](#limits-and-boundaries) es un paso aparte.

:::caution Ejecuta exactamente una réplica
El servidor MCP mantiene la sesión de protocolo de cada cliente en memoria, en el pod que la creó. Las sesiones no se comparten entre pods y no hay afinidad de sesión, así que una segunda réplica significa que aproximadamente la mitad de las peticiones de cada cliente llegan a un pod que nunca ha oído hablar de su sesión y son rechazadas. El fallo es intermitente y su mensaje no menciona el escalado, así que se lee como un error del cliente. Instalar esta área con más de una réplica se rechaza de plano en lugar de dejarlo para que se descubra.
:::

## Límites y fronteras {#limits-and-boundaries}

Algunos de estos puntos son trabajo pendiente y otros son decisiones. Vale la pena distinguirlos, así que cada uno dice cuál es.

**Deliberado, y sin previsión de cambio:**

- **Sin escrituras.** Enviar un comando o reconocer una alarma a través de MCP está planeado, pero solo detrás de un alcance elevado *y* una confirmación humana explícita — un asistente nunca accionará un dispositivo en silencio.
- **Sin acceso entre inquilinos.** El token tiene alcance limitado a un inquilino, elegido por el usuario en el momento de la concesión. La pertenencia a un inquilino nunca es un parámetro de una herramienta, así que no hay ningún argumento que un agente pueda variar para cruzar esa frontera.
- **Sin consultas arbitrarias.** Solo el conjunto de herramientas curado es alcanzable; no existe `run_graphql`.
- **Ninguna credencial de servicio está cableada en ninguna ruta de código que ejecute.** Esto es más fuerte que «no usa un token de servicio»: no hay en el servidor MCP ninguna ruta de código que eche mano de una credencial propia, así que no hay nada que un ataque de diputado confundido pueda tomar prestado. Toda lectura hacia abajo sale bajo el token del propio llamante, y un agente sin permiso para leer algo recibe el mismo rechazo que recibiría una persona. (Su pod monta la configuración de instancia igual que la de cualquier otro servicio — eso es fontanería de despliegue, no algo que el servidor use.)
- **Los payloads de los comandos no se devuelven.** `list_commands` entrega el nombre, el estado y las marcas de tiempo de un comando; lo que se envió al dispositivo queda fuera del contexto del agente.

**Límites que alcanzarás antes que cualquier otro:**

- Los resultados se paginan de 25 en 25 por defecto y 100 como máximo, y una consulta de varios dispositivos admite como mucho 50 tokens por llamada. Un agente que recorra una flota grande la recorre por páginas.
- Una respuesta descendente que el servidor vaya a leer está limitada a 8 MiB — es un límite sobre lo que obtiene de las propias APIs de la plataforma, no un límite que anuncie al agente. Una sesión caduca por inactividad a los 30 minutos.

**Habilitar el servicio no basta para poder usarlo.** Hay dos interruptores independientes, y activar solo el primero es la forma habitual de acabar con un servidor que responde y al que no se puede llegar:

1. El área funcional `mcp` no forma parte de un despliegue predeterminado; un operador la habilita explícitamente.
2. El servidor de autorización en `user-management` está a su vez apagado hasta que se configura una URL de emisor. Hasta entonces, `mcp` arranca y sirve sus metadatos, y **ningún cliente puede obtener un token**. Es un interruptor aparte a propósito: fijar el emisor cambia un claim de **todos** los tokens que emite la instancia, no solo los que usa MCP.

## Relacionado

- **[Multitenencia](./multi-tenancy.md)** — cómo se hace cumplir el aislamiento de inquilinos, sobre lo cual se apoya el token MCP.
- **[Arquitectura](./architecture.md)** — dónde se ubica el servicio `mcp`, y el modelo de [manejo de secretos](./architecture.md#secret-handling) para credenciales.
- **[API GraphQL](../reference/graphql-api.md)** — la API que respaldan las herramientas MCP.
