---
title: Arquitectura de un vistazo
---

# Arquitectura de un vistazo

Esta página es la versión corta. Muestra el camino que sigue un evento desde un dispositivo hasta un panel, una alarma y un comando de vuelta al dispositivo. [Arquitectura](./architecture.md) es la versión larga: describe cada componente, la columna vertebral de datos y mensajería, el modelo de despliegue y el almacén de secretos.

![Los dispositivos se conectan a los servicios de ingesta, que entregan los eventos a device-management para su resolución. Los eventos resueltos van a event-management para almacenarse, a device-state para el estado en vivo y a event-processing para la detección y la reacción. Las reacciones generan alarmas, notifican a las personas, envían comandos de vuelta a los dispositivos y llaman a conectores de salida. La consola, los paneles, la API GraphQL y MCP leen los resultados.](/img/architecture-overview.svg)

## La forma general {#shape}

- **Los dispositivos llegan a la plataforma a través de un servicio de ingesta.** El broker de la plataforma es el servidor MQTT integrado en NATS. `event-sources` atiende MQTT y HTTP, `sparkplug-ingest` y `lwm2m-ingest` son servicios opcionales para esos protocolos, y un servidor de red LoRaWAN publica en una [integración de entrada](../guides/inbound-integrations.md).
- **Cada evento se resuelve una sola vez, cerca del principio.** `device-management` identifica el dispositivo y adjunta su contexto al evento, de modo que todo lo que viene después lee un evento autodescriptivo y no necesita uniones.
- **Tres consumidores leen de forma independiente los eventos resueltos.** `event-management` los almacena, `device-state` mantiene el último estado conocido de cada dispositivo y `event-processing` evalúa reglas. Un consumidor lento no frena a los demás, porque NATS JetStream se interpone entre ellos.
- **Las reacciones pasan por los servicios dueños de lo que se modifica.** Una alarma es un objeto de `device-management`, un comando es un registro de `command-delivery` y una llamada de salida la entrega `outbound-connectors`, en su propio proceso.
- **Las personas y las herramientas leen a través de GraphQL.** La consola, los paneles, tu propio código y el [servidor MCP](./mcp.md) opcional usan las mismas API GraphQL de cada servicio con el token del propio llamante.
- **Una instancia sirve a muchos inquilinos.** Los asuntos del bus y las filas de la base de datos llevan el inquilino; consulta [Multiinquilinidad](./multi-tenancy.md).

## Cómo fluye un mensaje {#how-a-message-flows}

Sigue una lectura, un nivel de combustible, desde un dispositivo hasta un panel, y luego mira qué hace una regla con ella.

1. **El dispositivo publica.** Se conecta al broker de la plataforma con su credencial y publica un evento JSON en su propio tema, `{instance}/{tenant}/devices/{token}/events`. El broker le permite publicar en ese tema y en ningún otro. Consulta [Conectar un dispositivo](../guides/connecting-a-device.md).
2. **La ingesta lo decodifica.** `event-sources` toma el mensaje, comprueba que el dispositivo del cuerpo es el del tema, aplica el límite de ingesta del inquilino y publica el evento decodificado en la canalización. Un mensaje que no puede decodificar va a un flujo `failed-decode`.
3. **El evento se resuelve.** `device-management` autentica el dispositivo y busca su [tipo y perfil de dispositivo](./domain-model.md), sus asignaciones y sus relaciones rastreadas, hasta el cliente, el área y el activo. Lo escribe en el evento como dimensiones de índice y publica el evento resuelto.
4. **El evento se almacena.** `event-management` lo escribe en TimescaleDB, por lotes, particionado por inquilino. Allí se aplican las políticas de ciclo de vida (compresión, retención, agregados).
5. **El estado en vivo se actualiza.** `device-state` consume el mismo evento resuelto y actualiza el valor actual del dispositivo para esa medición, con la unidad y el tipo de su perfil.
6. **Un panel se actualiza.** Un widget de panel tiene abierta una suscripción GraphQL contra los servicios que guardan los datos. `event-management` empuja la nueva lectura por su puente WebSocket y el widget se redibuja. Consulta [Paneles](./dashboards.md).
7. **Una regla lo evalúa.** `event-processing` consume el mismo evento resuelto y lo pasa por sus reglas de detección. Cuando el nivel de combustible baja de un umbral, la regla se dispara en el flanco ascendente. Consulta [Procesamiento de eventos y alarmas](./event-processing.md).
8. **El disparo actúa.** Se ejecutan las acciones de la regla. Una genera una alarma en `device-management`, un objeto con ciclo de vida que se cierra solo en el flanco descendente. Otra encola un comando. Otras pueden entregar una carga a `outbound-connectors`, que la envía a un webhook, un broker o una cola en la nube.
9. **Una persona se entera.** `notification-management` enruta la alarma por correo o webhook según la política del inquilino, y puede volver a notificar mientras siga sin reconocer.
10. **El comando hace el viaje de ida y vuelta.** `command-delivery` persiste el comando, comprueba que el dispositivo está presente y lo publica en el tema de comandos del propio dispositivo. El dispositivo actúa y publica su resultado en su propio tema de respuesta de comandos, y `command-delivery` registra el resultado. El comando termina entonces como un éxito, un rechazo del dispositivo o uno de los estados que indican que nunca llegó; consulta [Comandos](./commands.md#command-lifecycle). Si el dispositivo estaba ausente, el comando quedó retenido hasta que volvió.

Los pasos se ejecutan en servicios separados conectados por NATS JetStream, así que cada uno puede escalarse y reiniciarse por su cuenta. [Observabilidad](../deployment/observability.md) describe las métricas y alertas que muestran dónde se retiene un evento.

## Siguientes pasos {#where-next}

- [Arquitectura](./architecture.md), para cada componente y cómo se despliega la plataforma.
- [Modelo de dominio](./domain-model.md), para dispositivos, perfiles, relaciones y eventos.
- [Matriz de capacidades de transporte](../reference/transport-matrix.md), para ver qué hace cada forma de conectarse en cada dirección.
- [Rendimiento medido](../deployment/bootstrap.md#measured-throughput), para lo que se ha medido, con qué carga y en qué clúster.
