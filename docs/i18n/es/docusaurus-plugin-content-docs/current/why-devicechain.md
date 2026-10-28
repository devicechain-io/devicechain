---
title: Por qué DeviceChain
---

# Por qué DeviceChain

DeviceChain es una plataforma IoT con licencia Apache 2.0 que ejecutas en Kubernetes. Los dispositivos se conectan por MQTT, HTTP, [Sparkplug B](./concepts/sparkplug.md) o [LwM2M](./concepts/lwm2m.md), o a través de un servidor de red LoRaWAN como ChirpStack. Las reglas generan alarmas y envían comandos de vuelta. Los paneles, las [actualizaciones por aire](./concepts/over-the-air-updates.md) y el [acceso multiinquilino para clientes](./user-guide/members-roles-and-customer-users.md#customer-users) vienen incluidos. No hay límite de dispositivos ni edición empresarial.

Esta página te ayuda a decidir si encaja contigo. Compara DeviceChain con tipos de solución, no con productos concretos, indica dónde cada tipo es la mejor opción y termina con lo que DeviceChain todavía no hace.

## Para quién es {#who-it-is-for}

**Quizá seas un ingeniero de plataforma que evalúa una plataforma IoT de código abierto.** Quieres ejecutarla tú mismo, en un clúster que controlas, y que tus datos se queden allí. Te gustaría que la licencia fuera la misma con 100 dispositivos o con 100 000, y quieres pruebas de cómo se comporta, no solo una lista de funciones. Empieza por [Arquitectura de un vistazo](./concepts/architecture-at-a-glance.md) y el [rendimiento medido](./deployment/bootstrap.md#measured-throughput).

**Quizá ya operes un servidor de red LoRaWAN o una planta con Sparkplug B.** Tu servidor de red decodifica las tramas de radio, o tus nodos de borde publican en un broker MQTT, y necesitas la capa superior: registro de dispositivos, reglas, alarmas, comandos, paneles y acceso para clientes. DeviceChain [recibe datos de un servidor de red](./guides/inbound-integrations.md) sin que el dispositivo se conecte a él, y se une a un entorno Sparkplug existente como [aplicación anfitriona](./concepts/sparkplug.md).

**Quizá construyas un producto conectado y necesites dar a cada uno de tus clientes una vista de sus propios equipos.** Eres un fabricante (OEM) o un integrador y quieres una sola plataforma para muchos clientes, con inicios de sesión que solo ven lo que les pertenece, una consola con [marca propia](./concepts/white-labeling.md) y actualizaciones que puedes desplegar en los dispositivos que entregas.

## Qué obtienes, comparado por tipo de solución {#comparison}

Las columnas son tipos de solución y cada celda es una generalización: un producto concreto de una categoría puede hacerlo mejor o peor. Léela como una forma de hacer las preguntas correctas a las opciones que realmente estás considerando.

| | Construirlo tú mismo con piezas (broker, base de datos de series temporales, motor de reglas, paneles) | Nubes IoT alojadas | Otras plataformas autoalojadas cuya edición de código abierto está limitada por un tope de dispositivos o un nivel empresarial | DeviceChain |
| --- | --- | --- | --- | --- |
| **Licencia** | Cada pieza tiene su propia licencia y tú te encargas de la integración. | Un servicio, normalmente medido por dispositivos o mensajes. | Núcleo de código abierto, con algunas funciones o escala reservadas a una edición de pago. | Apache 2.0, sin tope de dispositivos ni edición de pago. La alta disponibilidad, la multiinquilinidad y la entrega de comandos están en la plataforma abierta. |
| **Dónde se ejecuta** | Donde tú la pongas. | En la nube del proveedor. | En tu infraestructura o en la del proveedor. | En un clúster de Kubernetes que controlas, así que tus datos permanecen donde pongas el clúster. Consulta [Puesta en marcha](./deployment/bootstrap.md). |
| **Operarla** | Operas y actualizas cada pieza. | Lo hace el proveedor. | Lo haces tú. | Lo haces tú. Una CLI, `dcctl`, instala y actualiza la plataforma; consulta [Versiones y actualizaciones](./deployment/releases-and-upgrades.md). |
| **Multiinquilinidad y acceso de clientes** | Lo diseñas tú, normalmente tarde y con dificultad. | Normalmente separación a nivel de cuenta; las vistas por cliente las construyes tú. | A menudo existe, a veces en la edición de pago. | Una instancia sirve a muchos [inquilinos](./concepts/multi-tenancy.md), y un inquilino puede dar a sus propios clientes [inicios de sesión acotados](./user-guide/members-roles-and-customer-users.md#customer-users). |
| **Comandos de bucle cerrado** | Construyes la cola, los reintentos y el seguimiento del resultado. | Normalmente una función de comandos o trabajos, con distintos niveles de seguimiento del resultado. | Varía. | Un [comando](./concepts/commands.md#command-lifecycle) se persiste y se sigue hasta un [resultado](./concepts/commands.md#outcome). Si un dispositivo está ausente, sus comandos quedan retenidos, y una regla puede enviar uno. |
| **Reglas y alarmas** | Eliges y conectas un motor de reglas. | Normalmente un servicio de reglas, a menudo sin estado por mensaje. | Varía. | [Reglas de detección en flujo](./concepts/event-processing.md) que dan el mismo resultado cuando se reproducen los eventos, y [alarmas](./concepts/event-processing.md#the-alarm-lifecycle) con ciclo de vida y un camino hasta una persona. |
| **Actualizaciones por aire** | Construyes el almacén de artefactos, la firma y el seguimiento. | A menudo disponible como servicio gestionado. | Varía, y a veces es una función empresarial. | [Artefactos firmados, intentos con seguimiento y despliegues por grupo](./concepts/over-the-air-updates.md), para dispositivos con MQTT. Consulta los límites [más abajo](#not-yet). |
| **Pruebas sobre la operación** | Lo que mides tú mismo. | Términos de nivel de servicio, pero no puedes inspeccionar la plataforma. | Varía. | Una tabla de [rendimiento medido](./deployment/bootstrap.md#measured-throughput) que indica su carga de trabajo y sus salvedades, [versiones firmadas](./deployment/verifying-releases.md) que puedes verificar y [recuperación ante desastres](./deployment/disaster-recovery.md) con un procedimiento de restauración. El directorio [`hack/`](https://github.com/devicechain-io/devicechain/tree/main/hack) del repositorio contiene los bancos de validación de alta disponibilidad y recuperación, que se ejecutan a mano contra un clúster real. |

## Cuándo otra opción es mejor {#when-something-else-fits}

Cada tipo de solución es la respuesta correcta en algunas situaciones.

- **Constrúyelo tú mismo** cuando necesitas una o dos piezas, no una plataforma. Si un broker y una base de datos de series temporales ya cubren tu necesidad, o tus requisitos son tan inusuales que el modelo de una plataforma chocaría con ellos, las piezas te dan un control que una plataforma no te dará. El coste es que el registro de dispositivos, la multiinquilinidad, el seguimiento de comandos y las actualizaciones pasan a ser tu producto.
- **Una nube IoT alojada** cuando no quieres operar Kubernetes, cuando tus dispositivos y el resto de tus cargas de trabajo ya viven en una misma nube y quieres que se integren allí, o cuando necesitas hoy escala gestionada en muchas regiones. DeviceChain te da control y una licencia sin medición por dispositivo; también te da el trabajo de operación. [Ajuste de una instancia](./deployment/tuning-an-instance.md) muestra en qué consiste ese trabajo.
- **Otra plataforma autoalojada** cuando necesitas algo que DeviceChain todavía no tiene, como un controlador de protocolo industrial (véase más abajo), cuando te importa una trayectoria más larga o cuando quieres soporte comercial de un proveedor. DeviceChain es un proyecto joven, y la comunidad y el conjunto de integraciones a su alrededor son menores.

## Lo que todavía no hacemos {#not-yet}

Esta lista está aquí para que descubras estas cosas antes de adoptar DeviceChain, no después.

- **Controladores de buses de campo industriales.** No hay controladores BACnet, Modbus ni OPC-UA, y nada de lo que distribuye el proyecto los habla. La forma admitida es una pasarela local que habla el bus de campo y reenvía por MQTT o HTTP; la traducción del protocolo corre por tu cuenta. Consulta [No disponible](./reference/transport-matrix.md#no-disponible).
- **SNMP.** DeviceChain no recopila datos de agentes SNMP ni recibe traps SNMP. Usa un recolector que reenvíe por MQTT o HTTP.
- **Otros transportes.** La ingesta de dispositivos por WebSocket no está disponible, y CoAP llega a la plataforma solo a través de LwM2M. Solo el broker MQTT de la plataforma autentica un dispositivo mediante certificado de cliente; HTTP usa un token o una credencial básica y LwM2M usa claves precompartidas. Consulta la [matriz de capacidades de transporte](./reference/transport-matrix.md#no-disponible).
- **Comandos a dispositivos detrás de un servidor de red.** No hay enlace descendente a través de una [integración de entrada](./reference/transport-matrix.md#inbound-integrations). Un comando para un dispositivo así se acepta y después falla con `COMMAND_TRANSPORT_UNSUPPORTED`.
- **Una aplicación móvil.** DeviceChain tiene una consola web y paneles que se adaptan a la pantalla de un teléfono, además de bibliotecas cliente para [JavaScript](./reference/npm-packages.md) y, en el directorio [`sdks/`](https://github.com/devicechain-io/devicechain/tree/main/sdks) del repositorio, para .NET y Unity. No tiene una aplicación móvil nativa.
- **Límites de las actualizaciones por aire.** Los despliegues no tienen oleadas, promoción canario, programación ni ventanas de mantenimiento. Solo un dispositivo conectado por MQTT puede recibir una actualización gestionada: uno que solo habla HTTP no puede, los dispositivos LwM2M se actualizan con comandos ordinarios y Sparkplug no define actualización de firmware. Consulta [Qué dispositivos se pueden actualizar](./concepts/over-the-air-updates.md#transports).
- **Plataformas distintas de Kubernetes.** La plataforma se ejecuta en Kubernetes. Una instancia local corre en un clúster [kind](./quickstart/first-device.md), y una real en tu propio clúster.
- **Un gestor de claves externo.** Los secretos se cifran con una clave raíz de la instancia. El almacén está construido para que un gestor de claves pueda asumir esa tarea, pero hoy no se distribuye ninguno. Consulta [Gestión de secretos](./concepts/architecture.md#secret-handling).
- **Promesas de compatibilidad antes de la v1.0.0.** El versionado semántico, con una promesa sobre el protocolo de dispositivo, la API GraphQL y los paquetes npm, empieza en la v1.0.0. Las versiones anteriores pueden cambiar las API y los esquemas. Consulta la [política de versiones](./deployment/versioning-policy.md).

## Siguientes pasos {#next}

- [Inicio rápido: tu primer dispositivo](./quickstart/first-device.md) ejecuta una instancia en tu máquina.
- [Arquitectura de un vistazo](./concepts/architecture-at-a-glance.md) muestra cómo se mueve un mensaje por la plataforma, y [Arquitectura](./concepts/architecture.md) enumera cada componente.
- [Obtener ayuda](./getting-help.md) indica dónde hacer una pregunta o informar de una carencia en esta página.
