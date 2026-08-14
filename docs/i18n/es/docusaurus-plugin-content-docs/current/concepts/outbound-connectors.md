---
title: Conectores de Salida
---

# Conectores de Salida

La detección es solo la mitad de la automatización — la otra mitad es **actuar sobre el mundo exterior**. Cuando se dispara una [regla de detección](./event-processing.md), sus acciones REACT pueden alcanzar más allá de la plataforma: llamar a un webhook, o publicar un mensaje en un broker o una cola en la nube. Estos **conectores de salida** son la forma en que DeviceChain distribuye los eventos procesados hacia los sistemas que ya operas — una herramienta de incidentes, un pipeline de datos, el bus de mensajes de otra aplicación.

La entrega de salida la gestiona un servicio dedicado, **outbound-connectors**, mantenido deliberadamente separado del motor de detección: un endpoint externo lento o con mal comportamiento puede acumular retrasos en su propia entrega sin llegar nunca a ralentizar la evaluación de reglas.

:::info El servicio es opcional
`outbound-connectors` se entrega en el perfil de despliegue [`full`](../deployment/kubernetes-operator.md), no en `default` — así que una instancia levantada sin nombrar un perfil no lo ejecuta. La consola sigue mostrando la sección **Conectores**, y sus páginas explican que esta instancia no ejecuta el área en lugar de fingir que la función no existe.

Todo lo de esta página se aplica una vez desplegada el área. Para añadirla, levante la instancia con el perfil `full`.
:::

:::note Estado
**Disponible hoy:** la acción de webhook `httpCall`, y una acción `publish` que entrega a **MQTT**, **Apache Kafka**, **AWS SNS** y **AWS SQS** a través de un conector versionado con alcance de inquilino, con credenciales almacenadas en el almacén de secretos cifrado. Ambas acciones de salida se pueden configurar como **nodos de acción en el lienzo de automatización**. Un conector `gcp_pubsub` se puede crear a través de la API pero todavía no se puede despachar — un `publish` hacia uno de ellos se envía a la cola de mensajes no entregados (dead letter) como no soportado, y la consola no ofrece ese tipo. Están planeados objetivos `publish` adicionales (RabbitMQ, Azure, NATS, Redis, Slack, Splunk) bajo el mismo modelo — este repositorio es la fuente de verdad de lo que actualmente se construye.
:::

## Las dos acciones de salida

Ambas son [acciones REACT](./event-processing.md#automated-actions), que se autoran en el **lienzo de automatización** junto a *levantar alarma* y *enviar comando*, y cada una puede estar **protegida (guarded)** por una condición sobre el disparo. El selector de acciones del generador de formularios solo ofrece *levantar alarma* y *enviar comando*; una regla que ya lleva una acción de salida se abre en el formulario con esa acción mostrada en modo de solo lectura y preservada, de modo que cambiar de superficie nunca la descarta.

### `httpCall` — llamar a un webhook

Una solicitud HTTP directa a un endpoint que especificas. El cuerpo de la solicitud se moldea con una **expresión CEL** sobre el disparo, de modo que envías exactamente los campos que el receptor espera. Todo lo que la acción necesita — URL, método, encabezados, plantilla del cuerpo — vive en la propia acción, así que un webhook puntual no necesita ninguna configuración aparte. La autenticación opcional (un token bearer, un encabezado de clave de API) se almacena en el **almacén de secretos** y se adjunta en el momento del envío.

La entrega de webhooks está **reforzada** de maneras concretas: se niega a seguir redirecciones (de modo que un endpoint externo no puede desviar la solicitud con un `3xx` hacia otro sitio), solo admite destinos `http`/`https` y rechaza las credenciales incrustadas en la URL, elimina los encabezados reservados y de plataforma (de modo que un encabezado suministrado por el inquilino no puede falsificar el encabezado de autenticación ni la identidad interna del servicio), valida el nombre y el valor de cada encabezado contra la gramática del protocolo — que prohíbe los CR/LF de los que depende la inyección de encabezados — y, cuando hay un secreto adjunto, no repite el cuerpo de la respuesta en los registros (logs), de modo que un endpoint hostil no puede reflejar la credencial en ellos.

### `publish` — enviar a un conector

Para brokers de mensajes y colas en la nube, el destino es un **conector** reutilizable (ver abajo) en lugar de configuración en línea. Eliges un conector registrado y moldeas el payload del mensaje en CEL; el conector lleva consigo el destino y su credencial sellada. Un conector — configurado y provisto de credenciales una sola vez — se reutiliza en tantas reglas como quieras, y la credencial nunca aparece en una regla.

Una única acción `publish` genérica cubre todos los tipos de broker/cola: el **tipo del conector** selecciona el transporte. Los tipos admitidos hoy son `mqtt`, `kafka`, `aws_sns` y `aws_sqs`.

## Los conectores son recursos versionados

Un conector es un **recurso con alcance de inquilino** con el mismo ciclo de vida que un [perfil de dispositivo](./domain-model.md) o un [panel](./dashboards.md): editas un **borrador**, **publicas** una versión inmutable y **reviertes (roll back)** a una anterior si un cambio se comporta mal. Un conector contiene:

- un **tipo** (`mqtt`, `kafka`, `aws_sns`, `aws_sqs`),
- la **configuración de destino** para ese tipo (direcciones de broker, topic/cola/ARN, y opciones como QoS o TLS), y
- una **credencial** opcional, referenciada por manejador — el valor se escribe en el almacén de secretos y **nunca se devuelve en texto claro**, exactamente igual que el secreto de un canal de notificación.

Debido a que los conectores tienen alcance de inquilino, un inquilino nunca ve ni envía a través de los conectores de otro.

## Cómo funciona la entrega

Cuando se dispara una acción `publish` (o `httpCall`) protegida, REACT no realiza la llamada saliente por sí mismo. Entrega una **solicitud de despacho (dispatch request)** — la acción resuelta más una clave de idempotencia — al servicio outbound-connectors a través del bus de mensajes interno, y retoma la detección. La solicitud de despacho es **duradera**: si el servicio de conectores se reinicia, la solicitud sobrevive y se entrega al recuperarse.

Dos propiedades mantienen esto seguro:

- **Disparar y olvidar (fire-and-forget), con forma exacta.** Una acción de salida no bloquea la regla esperando una respuesta. Los payloads se moldean únicamente con CEL — no hay scripting arbitrario en la ruta de entrega — de modo que lo que una regla puede enviar está acotado y es revisable.
- **Idempotente por construcción.** Cada despacho lleva una **clave de idempotencia** direccionada por contenido derivada del disparo, de modo que si una detección se reprocesa (replayed) o una entrega se reintenta, el receptor puede reconocer y descartar el duplicado — una reentrega nunca significa un doble envío.

## Gobernanza {#governance}

Toda acción de salida está sujeta a **gobernanza por inquilino**, porque una llamada externa es más costosa — y más fácil de convertir en una inundación autoinfligida — que una llamada dentro del proceso. El volumen de salida se limita en tasa por inquilino en ambos extremos del salto: REACT descarta (sheds) las emisiones que exceden el presupuesto antes de despacharlas, y el servicio de conectores admite tráfico de destino dentro de un presupuesto acotado. Un inquilino sin límite configurado recae en un valor predeterminado de plataforma que **nunca es ilimitado**. El volumen descartado se expone como una métrica operativa para que un operador pueda detectar una regla que ha comenzado a sobreemitir.

## Aislamiento y dependencias

El servicio outbound-connectors se ejecuta en su **propio proceso**, separado de event-processing. Ese límite es deliberado:

- Un SDK de nube o un cliente de broker que se cuelga, falla o filtra memoria afecta solo a la entrega de conectores — nunca a la detección.
- Las bibliotecas cliente de broker/nube que respaldan `publish` están enlazadas **únicamente** a este servicio, de modo que el motor de detección replay-correct se mantiene liviano y su superficie de dependencias pequeña.
- El servicio resuelve las credenciales del conector por sí mismo; el motor de detección nunca las posee.

## Relacionado

- **[Procesamiento de Eventos y Alarmas](./event-processing.md)** — donde se crean las reglas y sus acciones REACT.
- **[Arquitectura](./architecture.md)** — dónde se ubica outbound-connectors entre los servicios.
- Las credenciales se mantienen en el **almacén de secretos** cifrado descrito en [Manejo de secretos](./architecture.md#secret-handling).
