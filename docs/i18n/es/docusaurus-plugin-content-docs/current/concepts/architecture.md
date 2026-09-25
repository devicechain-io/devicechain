---
title: Arquitectura
---

# Arquitectura

DeviceChain es un conjunto de microservicios Go sin estado construidos sobre una biblioteca core compartida. Un operador de Kubernetes los coordina y NATS JetStream los conecta. Una única instancia sirve a todos los inquilinos (un modelo de microservicio compartido). El aislamiento entre inquilinos se aplica en las capas de mensajería y almacenamiento, no ejecutando pods separados por inquilino.

## Componentes {#components}

| Componente | Responsabilidad |
|---|---|
| `event-sources` | Transportes entrantes de dispositivos. Decodifica los mensajes en bruto (JSON hoy; Protobuf y decodificadores personalizados están planificados), aplica un límite de tasa de ingesta por inquilino y los publica en el pipeline. |
| `device-management` | Dispositivos, tipos de dispositivo y perfiles de dispositivo versionados, el grafo de relaciones tipado, el objeto de alarma y su ciclo de vida, y la resolución de eventos (adjuntar a cada evento el contexto del dispositivo y de la organización). |
| `event-processing` | Detección y acciones. Un núcleo de streaming, que da el mismo resultado cuando se reproducen los eventos, evalúa reglas de detección sobre los eventos resueltos: umbral, duración, repetición, tasa de cambio, ausencia, conectividad, agregado por ventana y correlación por área. Después despacha acciones automatizadas: levantar una alarma, enviar un comando y conectores salientes. La detección vive aquí. El objeto de alarma que levanta permanece en `device-management`, y la entrega por conectores se delega a `outbound-connectors`. |
| `event-management` | Persiste los eventos resueltos en TimescaleDB, aplica las políticas de ciclo de vida de datos (compresión, retención, rollups) y sirve consultas de series temporales sobre GraphQL. |
| `device-state` | La proyección en vivo del último estado conocido de cada dispositivo: la presencia y la lectura actual de cada medición. |
| `command-delivery` | Despacho de comandos persistente y bidireccional hacia los dispositivos, seguido mediante un ciclo de vida por comando. Envía a un dispositivo cada vez, o a una flota entera como un único lote registrado. |
| `dashboard-management` | Definiciones de panel versionadas (borrador, publicar, revertir), almacenadas de forma opaca y renderizadas por los paquetes de runtime en React del workspace de frontend de este repositorio. Exportar una definición es una función del lado de la consola, no una operación del servicio. |
| `notification-management` | Enruta las alarmas disparadas hacia las personas, con una política por inquilino sobre correo electrónico (SMTP) y webhook. Una política enruta las alarmas por severidad y puede volver a notificar, a un intervalo fijo, una alarma que sigue sin reconocerse ni despejarse. |
| `user-management` | Identidades globales, membresías por inquilino, el catálogo de roles, y la emisión y validación de JWT. |
| `sparkplug-ingest` _(opcional)_ | Una Host Application de Sparkplug B con estado. Se conecta *hacia fuera* con los brokers MQTT de cada inquilino, ejecuta la máquina de sesión de Sparkplug y alimenta este pipeline, incluida la presencia autoritativa de los dispositivos. Solo una réplica sirve a la vez, elegida mediante un lease con vallado. Consulta [Sparkplug B](./sparkplug.md). |
| `lwm2m-ingest` _(opcional)_ | Termina OMA LwM2M sobre CoAP/UDP con DTLS. Los dispositivos se conectan *hacia dentro* y se identifican por su identidad PSK de DTLS autenticada. El registro impulsa la presencia, los recursos observados se decodifican como mediciones, y las lecturas, escrituras, ejecuciones y la actualización de firmware pasan por las rutas de comandos y de actualización. Consulta [LwM2M](./lwm2m.md). |
| `ai-inference` _(opcional)_ | Redacta una regla de detección a partir de una descripción en lenguaje natural y la pasa por el *mismo* compilador que usan las demás superficies de autoría. El modelo propone una regla; el compilador decide si es válida. Nunca interviene en la ruta que evalúa las reglas. Consulta [Autoría con IA](./ai-authoring.md). |
| `outbound-connectors` _(opcional)_ | Entrega las acciones salientes a sistemas externos: una llamada HTTP/webhook y un `publish` a brokers de mensajes y colas en la nube (MQTT, Kafka, AWS SNS/SQS). Usa conectores versionados y con alcance de inquilino, cuyas credenciales se guardan en el almacén de secretos. Se ejecuta en su propio proceso, de modo que un sistema externo lento o con mal comportamiento no puede afectar al pipeline de detección. Consulta [Conectores salientes](./outbound-connectors.md). |
| `mcp` _(opcional)_ | Un servidor de solo lectura del Model Context Protocol que permite a los asistentes de IA operar un inquilino en nombre de un usuario. Es un servidor de recursos OAuth 2.1 ligero sobre la API de GraphQL que porta el propio token del llamador, con alcance de inquilino. No tiene token de servicio y solo ofrece herramientas de lectura curadas. Consulta [Acceso de IA (MCP)](./mcp.md). |
| `operator` | Un operador basado en controller-runtime que es dueño del recurso personalizado `Instance` y de su ciclo de vida. Hoy su bucle de reconciliación solo observa el recurso; la agregación del estado de disponibilidad de los Deployments renderizados es una ampliación planificada de ese bucle. La configuración no se recarga en caliente: un servicio lee su configuración una sola vez al arrancar, y un cambio se adopta reiniciando los pods. El chart de Helm renderiza las cargas de trabajo en sí. Los inquilinos son registros de base de datos del plano de control, no recursos reconciliados. |

Difundir un comando a muchos dispositivos forma parte de `command-delivery`, no de un servicio aparte. Consulta [Un comando, muchos dispositivos](./commands.md#command-batches). La programación sigue planificada; consulta el repositorio para conocer el estado actual.

## Columna vertebral de datos y mensajería {#the-data-and-messaging-backbone}

**NATS JetStream** es la columna vertebral única para tres cosas:

- la mensajería asíncrona
- el ingreso MQTT: los dispositivos se conectan al servidor MQTT integrado de NATS en el puerto 1883
- el almacenamiento en caché y los bloqueos de clave-valor

No hay un Kafka, un Redis ni un broker MQTT aparte.

**PostgreSQL** lo almacena todo, en dos bases de datos separadas con una misma forma:

- El almacén *relacional* guarda los datos de entidades: inquilinos, usuarios, dispositivos, relaciones.
- El almacén de *eventos* añade la extensión TimescaleDB. Mantiene los eventos de series temporales en hypertables con compresión y agregados continuos.

`event-management` es dueño del esquema del almacén de eventos. Es el único servicio que escribe eventos en él o los sirve desde él. El único otro servicio que se conecta a él es el coordinador de purga de inquilinos de `user-management`, y solo para borrar las filas de un inquilino eliminado. Por eso los dos almacenes pueden respaldarse, dimensionarse y restaurarse de forma independiente.

Ambos almacenes se ejecutan como clústeres PostgreSQL gestionados por un operador, así que la alta disponibilidad es un número de instancias y no una capa de almacenamiento distinta: una instancia de cada uno por defecto, tres de cada uno en una instalación de alta disponibilidad. Con réplicas, los dos se replican de forma distinta:

- El almacén relacional retiene una escritura hasta que una réplica la confirma.
- El almacén de eventos se replica igual mientras haya una réplica disponible, pero recurre a la replicación asíncrona cuando no la hay, porque los eventos aún no persistidos siguen conservados de forma duradera en la capa de mensajería y pueden reproducirse.

Ambos almacenes también archivan de forma continua, activado por defecto: un flujo del registro de escritura anticipada más copias base programadas, cada uno en su propio bucket separado. Así, cada uno puede restaurarse a cualquier momento dentro de una ventana de retención, y no solo al de anoche. Restaurar es una propiedad de *crear* un clúster, no una operación sobre uno en ejecución; consulta [Recuperación ante desastres](../deployment/disaster-recovery.md).

Los subjects tienen alcance por inquilino (`{instance}.{tenant}.{suffix}`), y los datos de eventos están particionados por inquilino en la base de datos. Así es como un conjunto compartido de servicios sirve de forma segura a muchos inquilinos.

## El pipeline de eventos {#the-event-pipeline}

```
device → MQTT/NATS → event-sources → (decoded event)
       → device-management → (resolved event: device + relationship context attached)
       → event-management → TimescaleDB
```

Durante la resolución, `device-management` busca las relaciones **rastreadas** del dispositivo y las adjunta al evento como dimensiones de índice. Así, las consultas posteriores como "todos los eventos del cliente X" no necesitan joins. Consulta el [Modelo de dominio](./domain-model.md).

## Modelo de despliegue

OpenTofu aprovisiona la infraestructura en dos capas:

1. Lo que comparten todas las instancias de un clúster (la base de datos relacional, ingress, TLS, monitorización), una sola vez, al instalar el clúster.
2. El broker (NATS) y el almacén de eventos (TimescaleDB) propios de cada instancia, al arrancar esa instancia.

Un chart de Helm renderiza las cargas de trabajo de la plataforma: un Deployment y un Service por cada área funcional habilitada. Seleccionas las áreas con un **perfil** de despliegue (`default` / `full` / `telemetry` / `ingest-only`) o con un conjunto explícito. Una compuerta de dependencias rechaza una selección inválida en el momento de la instalación.

El operador asume que la infraestructura ya existe y gestiona el ciclo de vida de `Instance` en lugar de crear las cargas de trabajo. Los inquilinos son registros de base de datos del plano de control, no recursos reconciliados. Esta separación mantiene el arranque del clúster fuera del código de la aplicación. Consulta [Despliegue](../deployment/kubernetes-operator.md).

## Configuración, salud y arranque

Cada servicio carga su configuración en un esquema tipado y se niega a arrancar si es incorrecta. Una clave desconocida o mal escrita, un tipo incorrecto o un valor inválido se rechaza al arrancar en lugar de ignorarse en silencio. Una configuración incorrecta se manifiesta de inmediato, no como un comportamiento erróneo más adelante.

Cada servicio expone dos endpoints HTTP para Kubernetes:

- **`/healthz`** (liveness) devuelve `200` mientras el proceso pueda seguir haciendo su trabajo. Devuelve `503` una vez que la conexión del servicio con el broker de mensajería se ha cerrado de forma permanente sin que el servicio lo pidiera; por ejemplo, después de que la credencial del broker cambie con el pod en ejecución. Kubernetes reinicia entonces el pod, y este vuelve a conectarse con la credencial que se le entrega.
- **`/readyz`** (readiness) devuelve `503` hasta que la autenticación del servicio esté activa, y después `200`. Vuelve a devolver `503` mientras el servicio se apaga, y siempre que `/healthz` lo haga.

Los servicios arrancan **no listos** y obtienen las claves de firma JWT de `user-management` en segundo plano. Mientras un servicio no está listo, se retira de los endpoints del Service y sus consumidores de mensajes permanecen en pausa. Por eso una breve interrupción de `user-management` degrada un servicio en lugar de hacerlo fallar, y ninguna solicitud ni mensaje se procesa nunca sin autenticación verificada.

## Manejo de secretos {#secret-handling}

Algunos valores nunca se almacenan en configuración en texto plano ni en una columna reversible:

- las credenciales de integraciones y proveedores, como una contraseña SMTP, un token bearer de webhook o la credencial de broker o de nube de un conector saliente
- la mitad privada de la clave que firma todos los tokens de inicio de sesión

Estos valores viven en un **almacén de secretos cifrado**. Cada valor se sella en reposo con su propia clave de datos AES-256-GCM, y esa clave de datos está envuelta por una clave de cifrado de claves (KEK). La KEK predeterminada es una clave raíz guardada en el Secret de Kubernetes existente de la instancia, así que obtienes cifrado en reposo sin infraestructura adicional.

El almacén está construido en torno a un proveedor de claves intercambiable, de modo que un gestor de claves externo puede encargarse del envoltorio sin cambiar cómo un consumidor almacena o resuelve un handle. La clave raíz de la instancia es el único proveedor que se incluye hoy.

Un consumidor almacena solo un **handle** opaco. El valor es de solo escritura a través de la API: se resuelve dentro del servidor en el momento de uso y nunca se devuelve en texto claro. Los cambios de secretos se auditan (quién, cuándo, qué handle, nunca el valor).

## Superficie de API

Las APIs de datos y de administración de la plataforma son GraphQL, que es introspectable y autodocumentado. Cada servicio que expone una API sirve su propio esquema; `user-management` y `ai-inference` sirven además esquemas administrativos separados. No hay gRPC, ni una API REST sobre el dominio que mantener junto a los esquemas.

La mayor parte del tráfico entre servicios es asíncrono sobre NATS. Cuando un servicio necesita una respuesta o una acción de otro servicio en el momento en que actúa, llama directamente al endpoint GraphQL de ese servicio con un token de servicio de corta duración. Comprobar que un dispositivo existe antes de encolar un comando es un ejemplo; que una regla de detección envíe un comando es otro.

Aparte de los endpoints `/healthz`, `/readyz` y `/metrics` que sirve cada servicio, y del explorador `/graphiql`, que solo se sirve cuando las herramientas de desarrollo están habilitadas, los endpoints HTTP que no son GraphQL son los que dicta un estándar, más tres específicos. Los estándar son:

- el documento JWKS que obtienen los validadores de tokens
- los endpoints del servidor de autorización OAuth 2.1 (`/oauth/authorize`, `/oauth/token`, `/oauth/userinfo`, `/oauth/jwks` y el documento de metadatos RFC 8414), que `user-management` sirve una vez configurada una URL de emisor
- el transporte JSON-RPC sobre HTTP que habla el [servidor MCP](./mcp.md) opcional, y el documento de metadatos del recurso protegido (RFC 9728) que publica

Los otros tres son:

- el endpoint HTTP de ingesta de eventos orientado a dispositivos (`POST /{instance}/{tenant}/events`), que `event-sources` sirve en su propio puerto; la configuración predeterminada incluye una fuente HTTP
- el endpoint de subida y descarga del logotipo del inquilino (`/branding/logo`) en `user-management`
- el endpoint donde los servicios obtienen su token de servicio de corta duración (`/auth/service-token`) en `user-management`
