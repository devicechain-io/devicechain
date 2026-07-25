---
slug: /
sidebar_position: 1
title: Introducción
---

# DeviceChain

DeviceChain es una **Plataforma de Habilitación de Aplicaciones IoT** moderna y nativa de la nube, construida en Go y React. Conecta, gestiona y procesa datos de flotas de dispositivos grandes y heterogéneas — ciclo de vida del dispositivo, ingesta de telemetría, comando y control, modelado organizacional y multitenencia — y expone todo a través de una API GraphQL y paneles embebibles con control de versiones.

Es una reconstrucción desde cero de la plataforma SiteWhere que conserva el modelo de dominio probado, reemplazando a la vez el pesado stack Java/Spring por microservicios eficientes y operativamente simples que se ejecutan en cualquier clúster de Kubernetes.

## Por qué DeviceChain

- **Microservicios nativos de Go** — arranque en menos de un segundo, huella de memoria pequeña, servicios de binario único.
- **Operador + CRDs** — un operador de Kubernetes con un recurso declarativo `DeviceChainInstance`, no scripts de shell; los inquilinos son registros de base de datos del plano de control gestionados a través de la consola de administración.
- **API GraphQL primero** — introspectable y autodocumentada; sin stubs de cliente generados.
- **Un stack ágil y totalmente de código abierto** — NATS JetStream es toda la columna vertebral de mensajería / MQTT / KV, JWT nativo gestiona la autenticación, TimescaleDB es el único almacén de datos, y OpenTofu aprovisiona la infraestructura. Dos dependencias para ejecutar localmente: **NATS + TimescaleDB**.
- **Un modelo de relaciones uniforme** — el contexto del dispositivo es un grafo de relaciones tipado en lugar de asignaciones rígidas, de modo que los nuevos tipos de entidad se componen sin agitación de esquema.
- **Paneles embebibles y versionados** — un diseño centrado en lienzo (canvas-first) (capas, imágenes de fondo, responsivo por punto de quiebre) con widgets integrados de Apache ECharts, suscripciones en vivo, versionado de borrador/publicación/reversión, y un modelo de enlace (binding) en tiempo de ejecución (una definición + un manifiesto de host → en vivo en cualquier dispositivo). Distribuido como paquetes npm para que cualquier aplicación pueda embeber el visor.
- **Autoalojado y sin medición por uso** — Apache-2.0 sin división open-core y sin precios por dispositivo. El inventario de dispositivos, el estado del gemelo (twin state), la entrega de comandos, los paneles, la multitenencia, la alta disponibilidad y el SSO son parte de la plataforma abierta, no de un nivel de pago — ejecútala dentro de tu propio entorno con propiedad total de los datos.

## Cómo está organizada la plataforma

DeviceChain es un conjunto de microservicios cooperantes sobre una biblioteca núcleo (core) compartida:

- **event-sources** — transportes de entrada conectables (MQTT hoy; HTTP, CoAP, WebSocket planeados) que decodifican los mensajes de dispositivo en bruto hacia el pipeline.
- **device-management** — dispositivos, tipos de dispositivo + perfiles de dispositivo versionados, el grafo de relaciones, el objeto de alarma y su ciclo de vida, y la resolución de eventos.
- **event-processing** — el pipeline de detección + acción sobre eventos resueltos: reglas de streaming replay-correct (umbral, duración, repetición, tasa de cambio, ausencia, agregado por ventana, correlación por área) y respuestas automatizadas (levantar alarma, enviar comando y conectores de salida). La detección vive aquí; el objeto de alarma que levanta permanece en device-management.
- **event-management** — persiste los eventos resueltos en TimescaleDB y sirve consultas de series temporales (incluyendo suscripciones en vivo a través de un puente graphql-ws).
- **device-state** — la proyección en vivo del último estado conocido por dispositivo (lectura actual por medición).
- **command-delivery** — despacho de comandos persistente y bidireccional hacia los dispositivos.
- **dashboard-management** — definiciones de panel versionadas (borrador, publicación/reversión, exportación) renderizadas por los paquetes de widgets embebibles.
- **notification-management** — enruta las alarmas disparadas hacia humanos por correo electrónico (SMTP) y webhook, con escalado por severidad.
- **outbound-connectors** — entrega las acciones de salida de REACT (webhook `httpCall`, y `publish` hacia MQTT/Kafka/AWS SNS/SQS) a sistemas externos a través de conectores versionados y autenticados por secreto.
- **ai-inference** _(opcional)_ — redacta una regla de detección a partir de una descripción en lenguaje natural y la entrega al mismo compilador que usan los humanos; la IA [propone y el compilador dispone](./concepts/ai-authoring.md), nunca en la ruta replay-correct.
- **user-management** — identidades globales, membresías por inquilino, el catálogo de roles, la emisión/validación de JWT, y la entidad de empaquetado de nivel de inquilino (tenant tier).
- **mcp** _(opcional)_ — un servidor de solo lectura de [Model Context Protocol](./concepts/mcp.md) que permite a los asistentes de IA (Claude, Cursor, VS Code) consultar un inquilino en nombre de un usuario, bajo el propio token del usuario.
- **operator (k8s)** — reconcilia los CRDs con la plataforma en ejecución.

Consulta [Arquitectura](./concepts/architecture.md) para ver cómo encajan estas piezas, el [Modelo de Dominio](./concepts/domain-model.md) para los conceptos centrales, y [Procesamiento de Eventos y Alarmas](./concepts/event-processing.md) para saber cómo la telemetría se convierte en señales accionables.

## Probarlo con datos simulados

DeviceChain incluye una herramienta de **simulación de dispositivos** (`dcctl sim`) para levantar datos de demostración realistas sin hardware físico. Aprovisiona la topología completa de un escenario — clientes, áreas, activos y dispositivos — y luego envía telemetría y alarmas en vivo hacia la plataforma a través del **mismo canal de dispositivo que usa un dispositivo real**, para que puedas explorar la consola, los paneles y las consultas contra una flota en movimiento. Una simulación se autentica como una identidad de un solo inquilino con alcance limitado, igual que cualquier otro cliente externo — no tiene acceso especial a la plataforma.

## Estado del proyecto

DeviceChain está en fase previa al lanzamiento y en desarrollo activo. Las páginas de esta documentación indican si una capacidad está **disponible**, **planeada** o **en diseño**. El [repositorio de GitHub](https://github.com/devicechain-io/devicechain) es la fuente de verdad de lo que actualmente se construye y se ejecuta.

## Licencia

Apache License 2.0.
