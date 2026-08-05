---
sidebar_position: 1
title: Arquitectura
---

# Arquitectura

DeviceChain es un conjunto de microservicios Go sin estado sobre una biblioteca core compartida, coordinados por un operador de Kubernetes y conectados mediante NATS JetStream. Una única instancia sirve a todos los inquilinos (un modelo de microservicio compartido), con el aislamiento de inquilinos aplicado en las capas de mensajería y almacenamiento en lugar de ejecutar pods separados por inquilino.

## Componentes {#components}

| Componente | Responsabilidad |
|---|---|
| **event-sources** | Transportes de dispositivos entrantes. Decodifica los mensajes en bruto (JSON hoy; Protobuf y decodificadores personalizados están planificados), aplica un límite de tasa de ingesta por inquilino, y los publica en el pipeline. |
| **device-management** | Dispositivos, tipos de dispositivo + perfiles de dispositivo versionados, el grafo de relaciones tipado, el objeto de alarma y su ciclo de vida, y la resolución de eventos (adjuntando contexto de dispositivo y organizacional a cada evento). |
| **event-processing** | El pipeline DETECT + REACT: un núcleo de streaming replay-correct evalúa reglas de detección sobre eventos resueltos (umbral, duración, repetición, tasa de cambio, ausencia, conectividad, agregado por ventana, correlación por área) y despacha acciones automatizadas (levantar alarma, enviar comando, y conectores salientes). La detección vive aquí; el objeto de alarma que levanta permanece en device-management, y la entrega del conector se delega a outbound-connectors. |
| **event-management** | Persiste los eventos resueltos en TimescaleDB, aplica las políticas de ciclo de vida de datos (compresión / retención / rollups), y sirve consultas de series temporales sobre GraphQL. |
| **device-state** | La proyección en vivo del último estado conocido por dispositivo — presencia y lectura actual por medición. |
| **command-delivery** | Despacho de comandos persistente y bidireccional hacia los dispositivos, seguido a través de un ciclo de vida por comando. |
| **dashboard-management** | Definiciones de panel versionadas (borrador, publicar / revertir, exportar), renderizadas por los paquetes de widgets embebibles. |
| **notification-management** | Enruta las alarmas disparadas hacia los humanos — política por inquilino sobre correo electrónico (SMTP) y webhook, con escalamiento por severidad. |
| **user-management** | Identidades globales, membresías por inquilino, el catálogo de roles, y la emisión/validación de JWT. |
| **sparkplug-ingest** _(opcional)_ | Una Host Application de Sparkplug B con estado que conecta *hacia fuera* con los brokers MQTT de cada inquilino, ejecuta la máquina de sesión de Sparkplug y alimenta este mismo pipeline — incluida la presencia autoritativa de dispositivos. Solo una réplica sirve a la vez, elegida mediante un lease con vallado. Vea [Sparkplug B](./sparkplug.md). |
| **lwm2m-ingest** _(opcional)_ | Termina OMA LwM2M sobre CoAP/UDP con DTLS. Los dispositivos conectan *hacia dentro* y se identifican por su identidad PSK de DTLS autenticada; el registro impulsa la presencia, los recursos observados se decodifican como mediciones, y las lecturas/escrituras/ejecuciones más la actualización de firmware cubren los planos de comandos y de actualización. Vea [LwM2M](./lwm2m.md). |
| **ai-inference** _(opcional)_ | Redacta una regla de detección a partir de una descripción en lenguaje natural y la pasa por el *mismo* compilador que usan las demás superficies de autoría, de modo que el modelo propone y el compilador decide. Nunca interviene en la ruta que evalúa las reglas. Vea [Autoría con IA](./ai-authoring.md). |
| **outbound-connectors** | Entrega las acciones salientes de REACT a sistemas externos — una llamada HTTP/webhook y un `publish` a brokers de mensajes y colas en la nube (MQTT, Kafka, AWS SNS/SQS) — mediante conectores versionados y con alcance de inquilino, con credenciales almacenadas en el almacén de secretos. Se ejecuta en su propio proceso para que un sistema externo lento o con mal comportamiento no pueda afectar el pipeline de detección. Vea [Conectores salientes](./outbound-connectors.md). |
| **mcp** _(opcional)_ | Un servidor de solo lectura del Model Context Protocol que permite a los asistentes de IA operar un inquilino en nombre de un usuario. Un servidor de recursos OAuth 2.1 ligero sobre la API de GraphQL que porta el propio token con alcance de inquilino del llamador — sin token de servicio, solo herramientas de lectura curadas. Vea [Acceso de IA (MCP)](./mcp.md). |
| **operator** | Un operador basado en controller-runtime que gestiona el ciclo de vida de `DeviceChainInstance` (agregación de estado, recarga en caliente de configuración). Las cargas de trabajo en sí son renderizadas por el chart de Helm; los inquilinos son registros de base de datos del plano de control, no recursos reconciliados. |

Están planificados servicios adicionales — operaciones por lotes y programación. Consulte el repositorio para el estado actual.

## La columna vertebral de datos y mensajería

- **NATS JetStream** es la columna vertebral única para la mensajería asíncrona, el ingreso MQTT (los dispositivos se conectan al servidor MQTT integrado de NATS en el puerto 1883), y el almacenamiento en caché / bloqueo de clave-valor. No hay un Kafka, Redis o broker MQTT separados.
- **PostgreSQL** lo almacena todo, en dos bases de datos separadas con una sola forma. El almacén *relacional* guarda los datos de entidades —inquilinos, usuarios, dispositivos, relaciones— y el almacén de *eventos* añade la extensión TimescaleDB, manteniendo los eventos de series temporales en hypertables con compresión y agregados continuos. `event-management` es el único servicio que habla con el almacén de eventos, y no habla con ningún otro, y por eso ambos pueden respaldarse, dimensionarse y restaurarse de forma independiente.
- Ambos se ejecutan como clústeres PostgreSQL replicados gestionados por un operador, así que la alta disponibilidad es un número de instancias y no una capa de almacenamiento distinta. El almacén relacional retiene una escritura hasta que una réplica la confirma; el de eventos recurre en su lugar a la replicación asíncrona, porque los eventos aún no persistidos siguen conservados de forma duradera en la capa de mensajería y pueden reproducirse.
- Ambos archivan además de forma continua — un flujo del registro de escritura anticipada más copias base programadas, cada uno en su propio bucket separado — de modo que cada uno puede restaurarse a cualquier momento dentro de una ventana de retención, y no solo al de anoche. Esto está activado por defecto. Restaurar es una propiedad de *crear* un clúster, no una operación sobre uno en ejecución; vea [Recuperación ante desastres](../deployment/disaster-recovery.md).

Los subjects tienen alcance por inquilino (`{instance}.{tenant}.{suffix}`) y los datos de eventos están particionados por inquilino en la base de datos, que es cómo un conjunto compartido de servicios sirve de forma segura a muchos inquilinos.

## El pipeline de eventos {#the-event-pipeline}

```
device → MQTT/NATS → event-sources → (decoded event)
       → device-management → (resolved event: device + relationship context attached)
       → event-management → TimescaleDB
```

Durante la resolución, device-management busca las relaciones **rastreadas** del dispositivo y las adjunta al evento como dimensiones de índice, de modo que las consultas posteriores como "todos los eventos del cliente X" no necesitan joins. Vea el [Modelo de dominio](./domain-model.md).

## Modelo de despliegue

La infraestructura (NATS, TimescaleDB, ingress, TLS) es aprovisionada por **OpenTofu** en el momento de creación del clúster. Un **chart de Helm** renderiza las cargas de trabajo de la plataforma — un Deployment + Service por área funcional habilitada, seleccionada mediante un **perfil** de despliegue (`default` / `full` / `telemetry` / `ingest-only`) o un conjunto explícito, con una compuerta de dependencias que rechaza una selección inválida en el momento de instalación. El **operador** asume que la infraestructura ya existe y gestiona el ciclo de vida de `DeviceChainInstance` en lugar de estampar cargas de trabajo (los inquilinos son registros de base de datos del plano de control, no recursos reconciliados). Esta separación mantiene el arranque del clúster fuera del código de la aplicación. Vea [Despliegue](../deployment/kubernetes-operator.md).

## Configuración, salud, y arranque

Cada servicio carga su configuración en un esquema tipado y **falla de forma cerrada**: una clave desconocida o mal escrita, un tipo incorrecto, o un valor inválido se rechaza en el arranque en lugar de ignorarse silenciosamente, de modo que una configuración incorrecta se manifiesta de inmediato en lugar de aparecer como un comportamiento erróneo más tarde.

Cada servicio expone dos endpoints HTTP para Kubernetes:

- **`/healthz`** (liveness) — devuelve `200` mientras el proceso esté en ejecución.
- **`/readyz`** (readiness) — devuelve `503` hasta que la autenticación del servicio esté activa, luego `200`.

Los servicios inician en un estado **no listo** y obtienen las claves de firma JWT desde `user-management` en segundo plano. Mientras no está listo, un servicio se retira de los endpoints de Service y sus consumidores de mensajes permanecen pausados — de modo que una breve interrupción de `user-management` degrada a un servicio en lugar de hacerlo fallar, y ninguna solicitud o mensaje se procesa jamás sin autenticación verificada.

## Manejo de secretos {#secret-handling}

Las credenciales de integración y proveedores — una contraseña SMTP, un token bearer de webhook, la credencial de broker o nube de un conector saliente — nunca se almacenan en configuración de texto plano ni en una columna reversible. Viven en un **almacén de secretos encriptado**: cada valor se sella en reposo con una clave de datos AES-256-GCM por secreto, envuelta por una clave de encriptación de claves (KEK), donde la KEK predeterminada es una clave raíz en el Secret de Kubernetes existente de la instancia — encriptación en reposo sin infraestructura adicional, y KMS en la nube / HashiCorp Vault son alternativas de reemplazo directo para despliegues regulados. Un consumidor almacena solo un **handle** opaco; el valor es de **solo escritura sobre la API** y se resuelve internamente en el servidor en el momento de uso, nunca se devuelve en texto claro. Las mutaciones de secretos son auditadas (quién, cuándo, qué handle — nunca el valor).

## Superficie de API

Todas las APIs externas son **GraphQL** (un esquema por servicio), lo cual es introspectable y autodocumentado. La comunicación interna entre servicios es asíncrona sobre NATS. No hay gRPC ni superficie REST que mantener.
