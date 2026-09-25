---
slug: /
sidebar_position: 1
title: Introducción
---

# DeviceChain

DeviceChain es una **Plataforma de Habilitación de Aplicaciones IoT** nativa de la nube, construida en Go y React. Conecta, gestiona y procesa datos de flotas de dispositivos grandes y heterogéneas. Cubre el ciclo de vida del dispositivo, la ingesta de telemetría, el comando y control, el modelado organizacional y la multitenencia, y expone todo ello a través de una API GraphQL y de paneles embebibles con control de versiones.

DeviceChain es una reconstrucción desde cero de la plataforma SiteWhere. Conserva el modelo de dominio probado de SiteWhere y reemplaza el pesado stack Java/Spring por microservicios eficientes y operativamente simples que se ejecutan en cualquier clúster de Kubernetes.

## Por qué DeviceChain

- **Microservicios nativos de Go.** Los servicios arrancan en menos de un segundo, usan poca memoria y se distribuyen como binarios únicos.
- **Operador y CRDs.** DeviceChain usa un operador de Kubernetes con un recurso declarativo `Instance`, no scripts de shell. Los inquilinos son registros de base de datos del plano de control que gestionas desde la consola de administración.
- **API GraphQL primero.** La API es introspectable y autodocumentada, así que no necesitas stubs de cliente generados.
- **Un stack ligero y totalmente de código abierto.** NATS JetStream es toda la columna vertebral de mensajería, MQTT y KV. JWT nativo se encarga de la autenticación, PostgreSQL es el único almacén de datos (una base de datos relacional para los datos de entidades y una segunda base de datos con la extensión TimescaleDB para los eventos de series temporales), y OpenTofu aprovisiona la infraestructura. Para ejecutarlo localmente necesitas dos dependencias: **NATS + TimescaleDB**.
- **Un modelo de relaciones uniforme.** El contexto del dispositivo es un grafo de relaciones tipado en lugar de asignaciones rígidas, de modo que los nuevos tipos de entidad se componen sin cambios constantes de esquema.
- **Paneles embebibles y versionados.** Los paneles usan un diseño centrado en el lienzo (capas, imágenes de fondo, adaptable por punto de quiebre) con widgets integrados de Apache ECharts y suscripciones en vivo. Tienen versionado de borrador/publicación/reversión y un modelo de enlace (binding) en tiempo de ejecución: una definición más un manifiesto de host se pone en vivo sobre cualquier dispositivo. El visor se distribuye como paquetes npm, así que cualquier aplicación puede embeberlo.
- **Autoalojado y sin medición por uso.** DeviceChain es Apache-2.0, sin división open-core y sin precios por dispositivo. El inventario de dispositivos, el estado del gemelo (twin state), la entrega de comandos, los paneles, la multitenencia, la alta disponibilidad y el servidor de autorización OAuth 2.1 forman parte de la plataforma abierta, no de un nivel de pago. Lo ejecutas dentro de tu propio entorno, con propiedad total de los datos.

## Cómo está organizada la plataforma

DeviceChain es un conjunto de microservicios cooperantes sobre una biblioteca núcleo (core) compartida:

| Servicio | Qué hace |
| --- | --- |
| **event-sources** | Transportes de entrada conectables (MQTT y HTTP hoy; WebSocket planeado) que decodifican los mensajes en bruto de los dispositivos hacia el pipeline. |
| **sparkplug-ingest** _(opcional)_ | Una aplicación host de [Eclipse Sparkplug B](./concepts/sparkplug.md). Se une a tu entorno MQTT Sparkplug existente, alimenta el mismo pipeline y afirma la presencia autoritativa del dispositivo a partir del intercambio de nacimiento/muerte. |
| **lwm2m-ingest** _(opcional)_ | Un servidor [OMA LwM2M](./concepts/lwm2m.md) que termina CoAP/UDP sobre DTLS. Autentica cada dispositivo mediante su identidad de clave precompartida y afirma la presencia a partir del ciclo de vida de registro. |
| **device-management** | Dispositivos, tipos de dispositivo y perfiles de dispositivo versionados, el grafo de relaciones, el objeto de alarma y su ciclo de vida, y la resolución de eventos. |
| **event-processing** | El pipeline de detección y acción sobre eventos resueltos. Ejecuta reglas de streaming (umbral, duración, repetición, tasa de cambio, ausencia, agregado por ventana, correlación por área) que dan el mismo resultado cuando los eventos se reproducen, y respuestas automatizadas (levantar alarma, enviar comando y conectores de salida). La detección vive aquí; el objeto de alarma que levanta permanece en device-management. |
| **event-management** | Persiste los eventos resueltos en TimescaleDB y sirve consultas de series temporales, incluidas suscripciones en vivo a través de un puente graphql-ws. |
| **device-state** | La proyección en vivo del último estado conocido por dispositivo (la lectura actual por medición). |
| **command-delivery** | Despacho de comandos persistente y bidireccional hacia los dispositivos. |
| **dashboard-management** | Definiciones de panel versionadas (borrador, publicación/reversión, exportación), renderizadas por los paquetes de widgets embebibles. |
| **notification-management** | Enruta las alarmas disparadas hacia personas por correo electrónico (SMTP) y webhook, con enrutamiento por severidad y escalado de las alarmas que siguen sin reconocer y sin despejar. |
| **outbound-connectors** | Entrega las acciones de salida del pipeline (webhook `httpCall`, y `publish` hacia MQTT/Kafka/AWS SNS/SQS) a sistemas externos a través de conectores versionados y autenticados por secreto. |
| **ai-inference** _(opcional)_ | Redacta una regla de detección a partir de una descripción en lenguaje natural y la entrega al mismo compilador que usan las personas. La IA solo propone; el compilador decide qué es válido. La IA nunca se ejecuta en la detección en vivo, así que una regla da el mismo resultado cuando los eventos se reproducen. Consulta [Autoría asistida por IA](./concepts/ai-authoring.md). |
| **user-management** | Identidades globales, membresías por inquilino, el catálogo de roles, la emisión y validación de JWT, y los niveles de inquilino (tenant tiers: el empaquetado que un operador de la plataforma define y asigna a los inquilinos). |
| **mcp** _(opcional)_ | Un servidor de solo lectura de [Model Context Protocol](./concepts/mcp.md) que permite a los asistentes de IA (Claude, Cursor, VS Code) consultar un inquilino en nombre de un usuario, bajo el propio token del usuario. |
| **operator (k8s)** | Es dueño del recurso personalizado `Instance`. Hoy su bucle de reconciliación solo observa el recurso; el chart de Helm renderiza las cargas de trabajo de la plataforma. |

Consulta [Arquitectura](./concepts/architecture.md) para ver cómo encajan estas piezas, el [Modelo de Dominio](./concepts/domain-model.md) para los conceptos centrales, y [Procesamiento de Eventos y Alarmas](./concepts/event-processing.md) para saber cómo la telemetría se convierte en señales accionables.

## Datos simulados {#trying-it-with-simulated-data}

Puedes explorar DeviceChain sin hardware físico con su herramienta de **simulación de dispositivos**, `dcctl sim`. Aprovisiona la topología completa de un escenario (clientes, áreas, activos y dispositivos) y luego envía telemetría y alarmas en vivo hacia la plataforma a través del **mismo canal de dispositivo que usa un dispositivo real**. Así puedes explorar la consola, los paneles y las consultas contra una flota en movimiento.

Una simulación se autentica como una identidad de un solo inquilino con alcance limitado, igual que cualquier otro cliente externo. No tiene ningún acceso especial a la plataforma.

## Estado del proyecto

DeviceChain está en fase previa al lanzamiento y en desarrollo activo. Las páginas de esta documentación indican si una capacidad está **disponible**, **planeada** o **en diseño**. El [repositorio de GitHub](https://github.com/devicechain-io/devicechain) es la fuente de verdad de lo que actualmente se construye y se ejecuta.

## Licencia

Apache License 2.0.
