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

Una solicitud HTTP directa a un endpoint que especificas. El cuerpo de la solicitud se moldea con una **expresión CEL** sobre el disparo, de modo que envías exactamente los campos que el receptor espera. Todo lo que la acción necesita — URL, método, encabezados, plantilla del cuerpo — vive en la propia acción, así que un webhook puntual no necesita ninguna configuración aparte. La autenticación opcional es un **manejador de secreto**: el token se almacena en el **almacén de secretos** y se presenta en el momento del envío como un encabezado `Authorization: Bearer <token>` — el nombre del encabezado y el esquema no son configurables, así que un receptor que espera un encabezado de clave de API personalizado no puede autenticarse de esta manera.

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

## A dónde puede enviar un conector {#destinations}

Cada conexión que hace un conector se comprueba en el momento en que se establece, sobre la dirección
a la que realmente resolvió el destino. Un destino que resuelve a una dirección de **loopback,
privada, NAT de operador (carrier-grade NAT), de enlace local (link-local) o de metadatos de nube se
rechaza**, sea cual sea el nombre que se le dio. La comprobación se aplica igual a webhooks, MQTT,
Kafka, SNS y SQS:

- **El rechazo es definitivo.** Un despacho rechazado acaba en la cola de mensajes muertos con el
  resultado `blocked` y no se reintenta, porque esperar no convierte una dirección en pública. Un
  destino que simplemente está caído es distinto: es un fallo ordinario y se reintenta.
- **Las URL de bróker MQTT** deben usar `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://`
  o `wss://`, con un puerto explícito y un bróker por entrada. Cualquier otro esquema — incluido
  `unix://` — se rechaza al guardar el conector, y otra vez si se despacha un conector almacenado que
  lo use.
- **Un despacho es `blocked` en cuanto se rechaza cualquier dirección que intenta** — y una dirección
  solo se juzga cuando se intenta. Con una lista de brókeres, el resultado puede depender de a cuál
  llega primero el cliente: un cliente MQTT que se conecta a un primer bróker permitido entrega, y solo
  encuentra uno rechazado cuando el primero está caído; un cliente Kafka elige su primera semilla al
  azar. Liste solo destinos permitidos.
- **Las direcciones de Kafka** son `host:puerto`. También se comprueba cada bróker que el clúster
  **anuncia** en sus metadatos, no solo las direcciones que configuró.
- **Los endpoints personalizados de SNS y SQS** se comprueban como cualquier otro destino. Sin uno,
  el conector habla con el endpoint regional de AWS, que también se comprueba.
- **No se consulta el entorno del propio servicio.** No se usan las variables de proxy
  (`HTTPS_PROXY`, `ALL_PROXY`, …), ni las variables `AWS_*`, ni los archivos de configuración de AWS,
  ni la identidad de nube del pod. Un conector llega exactamente al destino que nombra, con la
  credencial que lleva.

Para que los conectores lleguen a un destino privado — un bróker dentro del clúster, **Amazon MSK**,
**Amazon MQ**, o SNS/SQS a través de un **endpoint de interfaz de VPC con DNS privado** (que hace que
incluso los nombres regionales predeterminados resuelvan a direcciones privadas) — un operador lista
cada dirección como su propio `/32` en `instance.config.infrastructure.egress.allowedDestinations`.
Un endpoint de interfaz tiene una dirección por zona de disponibilidad, y cada una necesita su propia
entrada. Una autorización se aplica a **todos los inquilinos y a todos los conectores y webhooks**, no
solo a aquel para el que se añadió.

Un resultado `blocked` solo le dice a un inquilino que el destino resolvió a una dirección rechazada —
lo mismo que dice el rechazo de un webhook. La dirección rechazada queda registrada en el mensaje
muerto, que los operadores pueden leer y los inquilinos no.

## Gobernanza {#governance}

Toda acción de salida está sujeta a **gobernanza por inquilino**, porque una llamada externa es más costosa — y más fácil de convertir en una inundación autoinfligida — que una llamada dentro del proceso. El volumen de salida se limita en tasa por inquilino en ambos extremos del salto: REACT descarta (sheds) las emisiones que exceden el presupuesto antes de despacharlas, y el servicio de conectores admite tráfico de destino dentro de un presupuesto acotado. Un inquilino sin límite configurado recae en un valor predeterminado de plataforma que **nunca es ilimitado**. REACT y el servicio de conectores miden las acciones de salida de un inquilino según el momento en que la telemetría que las desencadenó llegó a la plataforma, de modo que un atraso de detecciones que se procesa tras un reinicio o una conmutación por error no se confunde con una inundación ni se frena al techo del inquilino. Una acción que sigue por encima del presupuesto se registra como una carta muerta con motivo `shed`: se registra, no se reintenta. Por encima de un presupuesto por inquilino de aproximadamente una carta por segundo, las acciones descartadas se cuentan y se resumen en una carta por inquilino y minuto. Ambos extremos aplican el techo por réplica de su servicio; consulte [Los techos son por réplica](./governance.md#per-replica).

## Aislamiento y dependencias

El servicio outbound-connectors se ejecuta en su **propio proceso**, separado de event-processing. Ese límite es deliberado:

- Un SDK de nube o un cliente de broker que se cuelga, falla o filtra memoria afecta solo a la entrega de conectores — nunca a la detección.
- Las bibliotecas cliente de broker/nube que respaldan `publish` están enlazadas **únicamente** a este servicio, de modo que el motor de detección replay-correct se mantiene liviano y su superficie de dependencias pequeña.
- El servicio resuelve las credenciales del conector por sí mismo; el motor de detección nunca las posee.

## Relacionado

- **[Procesamiento de Eventos y Alarmas](./event-processing.md)** — donde se crean las reglas y sus acciones REACT.
- **[Arquitectura](./architecture.md)** — dónde se ubica outbound-connectors entre los servicios.
- Las credenciales se mantienen en el **almacén de secretos** cifrado descrito en [Manejo de secretos](./architecture.md#secret-handling).
