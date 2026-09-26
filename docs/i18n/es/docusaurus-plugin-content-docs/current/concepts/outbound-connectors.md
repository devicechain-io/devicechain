---
title: Conectores de Salida
---

# Conectores de Salida

Los conectores de salida permiten que una [regla de detección](./event-processing.md) actúe sobre el mundo exterior. Cuando una regla se dispara, sus acciones pueden llamar a un webhook o publicar un mensaje en un broker o en una cola en la nube. Así es como DeviceChain distribuye los eventos procesados hacia los sistemas que ya operas: una herramienta de incidentes, un pipeline de datos, el bus de mensajes de otra aplicación. La detección es solo la mitad de la automatización; esta es la otra mitad.

Un servicio dedicado, **outbound-connectors**, se encarga de la entrega, y está separado del motor de detección a propósito. Un endpoint externo lento o con mal comportamiento puede acumular retraso en su propia entrega, pero nunca ralentiza la evaluación de reglas.

:::info El servicio es opcional
`outbound-connectors` se incluye en el [perfil de despliegue](../deployment/kubernetes-operator.md) `full`, no en `default`, así que una instancia levantada sin indicar un perfil no lo ejecuta. La consola sigue mostrando la sección **Conectores**; sus páginas explican que esta instancia no ejecuta el área, en lugar de fingir que la función no existe.

Todo lo que describe esta página se aplica una vez desplegada el área. Para añadirla, levanta la instancia con el perfil `full`.
:::

:::note Estado
**Disponible hoy:** la acción de webhook `httpCall`, y una acción `publish` que entrega a MQTT, Apache Kafka, AWS SNS y AWS SQS a través de un conector versionado con alcance de inquilino, cuyas credenciales se guardan en el almacén de secretos cifrado. Ambas se pueden configurar como nodos de acción en el lienzo de automatización. Un conector `gcp_pubsub` se puede crear a través de la API, pero todavía no se puede despachar (consulta [más abajo](#publish--send-to-a-connector)).

**Planificado:** más destinos de `publish` (RabbitMQ, Azure, NATS, Redis, Slack, Splunk) bajo el mismo modelo. Este repositorio es la fuente de verdad de lo que se construye actualmente.
:::

## Las dos acciones de salida

Ambas son [acciones](./event-processing.md#automated-actions) que creas en el **lienzo de automatización**, junto a *levantar alarma* y *enviar comando*. Cada una puede estar **protegida (guarded)** por una condición sobre el disparo.

El selector de acciones del generador de formularios solo ofrece *levantar alarma* y *enviar comando*. Si una regla ya lleva una acción de salida, el formulario muestra esa acción en modo de solo lectura y la conserva, de modo que cambiar entre el lienzo y el formulario nunca la descarta.

### `httpCall` — llamar a un webhook

`httpCall` envía una solicitud HTTP directamente a un endpoint que tú especificas. El cuerpo de la solicitud se moldea con una **expresión CEL** sobre el disparo, de modo que envías exactamente los campos que el receptor espera. La URL, el método, los encabezados y la plantilla del cuerpo viven en la propia acción, así que un webhook puntual no necesita ninguna configuración aparte.

La autenticación es opcional y usa un **manejador de secreto**. El token se guarda en el **almacén de secretos** y se presenta en el momento del envío como un encabezado `Authorization: Bearer <token>`. El nombre del encabezado y el esquema no son configurables, así que por esta vía no puedes autenticarte ante un receptor que espera un encabezado de clave de API personalizado. Si la acción nombra un manejador pero no hay ningún secreto guardado con él, la llamada nunca se envía sin su credencial: se registra una vez en la cola de mensajes no entregados con el resultado `invalid` y no se reintenta. Nada la vuelve a reproducir, así que la llamada de ese disparo no se hace; corrige el manejador de secreto de la acción para que los disparos posteriores se autentiquen.

La entrega de webhooks está reforzada de estas maneras:

- Se niega a seguir redirecciones, de modo que un endpoint externo no puede desviar la solicitud a otro sitio con un `3xx`.
- Solo admite destinos `http`/`https` y rechaza las credenciales incrustadas en la URL.
- Elimina los encabezados reservados y de plataforma, de modo que un encabezado suministrado por el inquilino no puede falsificar el encabezado de autenticación ni la identidad interna del servicio.
- Valida el nombre y el valor de cada encabezado contra la gramática del protocolo, que prohíbe los CR/LF de los que depende la inyección de encabezados.
- Cuando hay un secreto adjunto, no repite el cuerpo de la respuesta en los registros (logs), de modo que un endpoint hostil no puede reflejar en ellos la credencial.

### `publish` — enviar a un conector {#publish--send-to-a-connector}

Para brokers de mensajes y colas en la nube, el destino es un **conector** reutilizable (ver abajo) en lugar de configuración en línea. Eliges un conector registrado y moldeas el payload del mensaje en CEL. El conector lleva el destino y su credencial sellada. Configuras un conector y le asignas su credencial una sola vez, lo reutilizas en tantas reglas como quieras, y la credencial nunca aparece en una regla.

Una única acción `publish` genérica cubre todos los tipos de broker y de cola; el **tipo del conector** selecciona el transporte. Los tipos admitidos hoy son `mqtt`, `kafka`, `aws_sns` y `aws_sqs`.

Un conector `gcp_pubsub` se puede crear a través de la API, pero todavía no se puede despachar: un `publish` hacia uno de ellos se envía a la cola de mensajes no entregados (dead letter) como no soportado. La consola no ofrece ese tipo.

## Los conectores son recursos versionados

Un conector es un **recurso con alcance de inquilino** con el mismo ciclo de vida que un [perfil de dispositivo](./domain-model.md) o un [panel](./dashboards.md). Editas un **borrador**, **publicas** una versión inmutable y **reviertes (roll back)** a una anterior si un cambio se comporta mal. Un conector contiene:

- un **tipo** (`mqtt`, `kafka`, `aws_sns`, `aws_sqs`)
- la **configuración de destino** para ese tipo: direcciones de broker, topic, cola o ARN, y opciones como QoS o TLS
- una **credencial** opcional, referenciada por manejador. El valor se escribe en el almacén de secretos y **nunca se devuelve en texto claro**, exactamente igual que el secreto de un canal de notificación.

Como los conectores son de nivel de inquilino, un inquilino nunca ve ni envía a través de los conectores de otro.

## Cómo funciona la entrega

Cuando se dispara una acción `publish` (o `httpCall`) protegida, el motor de detección no realiza la llamada saliente por sí mismo. Entrega una **solicitud de despacho (dispatch request)**, es decir, la acción resuelta más una clave de idempotencia, al servicio outbound-connectors a través del bus de mensajes interno, y vuelve a la detección. La solicitud de despacho es **duradera**: si el servicio de conectores se reinicia, la solicitud sobrevive y se entrega al recuperarse.

Dos propiedades mantienen esto seguro:

- **Disparar y olvidar (fire-and-forget), con forma exacta.** Una acción de salida no bloquea la regla esperando una respuesta. Los payloads se moldean únicamente con CEL, sin scripting arbitrario en la ruta de entrega, de modo que lo que una regla puede enviar está acotado y es revisable.
- **Una clave de idempotencia en cada despacho.** Cada despacho lleva una **clave de idempotencia** direccionada por contenido y derivada del disparo: la misma detección y la misma acción producen la misma clave en cada reentrega y en cada reprocesamiento (replay). La entrega es al-menos-una-vez (at-least-once). Cuando el motor de detección vuelve a entregar el mismo despacho en unos diez minutos (un reintento, o una detección que estaba en curso durante un reinicio), el bus de mensajes lo almacena una sola vez; un despacho que el servicio de conectores tiene que reintentar, o uno reprocesado tras una interrupción larga, todavía puede enviarse dos veces. La clave se reenvía para que el receptor pueda reconocer y descartar el duplicado: como el encabezado `X-DC-Idempotency-Key` en un webhook, como un encabezado de registro `idempotency_key` en Kafka, y como un atributo de mensaje `idempotency_key` en SNS y SQS. Una publicación MQTT no lleva clave, porque MQTT 3.1.1 no tiene dónde ponerla.

## A dónde puede enviar un conector {#destinations}

Cada conexión que hace un conector se comprueba en el momento en que se establece, contra la dirección a la que realmente resolvió el destino. Un destino que resuelve a una dirección de **loopback, privada, NAT de operador (carrier-grade NAT), de enlace local (link-local) o de metadatos de nube se rechaza**, sea cual sea el nombre que se le dio. La comprobación funciona igual para webhooks, MQTT, Kafka, SNS y SQS:

- **El rechazo es definitivo.** Un despacho rechazado se envía a la cola de mensajes no entregados (dead letter) con el resultado `blocked` y no se reintenta, porque esperar no convierte una dirección en pública. Un destino que simplemente está caído es distinto: eso es un fallo ordinario, y se reintenta.
- **Las URL de broker MQTT** deben usar `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://` o `wss://`, con un puerto explícito y un broker por entrada. Cualquier otro esquema, incluido `unix://`, se rechaza al guardar el conector, y otra vez si se despacha un conector almacenado que lo use.
- **Un despacho es `blocked` en cuanto se rechaza cualquier dirección que intenta**, y una dirección solo se juzga cuando se intenta. Con una lista de brokers, el resultado puede depender por tanto de a cuál llega primero el cliente. Un cliente MQTT que se conecta a un primer broker permitido entrega, y solo encuentra uno rechazado cuando el primero está caído. Un cliente Kafka elige su primera semilla al azar. Incluye en la lista solo destinos permitidos.
- **Las direcciones de Kafka** son `host:port`. También se comprueba cada broker que el clúster **anuncia** en sus metadatos, no solo las direcciones que configuraste.
- **Los endpoints personalizados de SNS y SQS** se comprueban como cualquier otro destino. Sin uno, el conector habla con el endpoint regional de AWS, que también se comprueba.
- **No se consulta el entorno del propio servicio.** No usa las variables de proxy (`HTTPS_PROXY`, `ALL_PROXY`, …), las variables `AWS_*`, los archivos de configuración de AWS ni la identidad de nube del pod. Un conector llega exactamente al destino que nombra, con la credencial que lleva.

Para que los conectores lleguen a un destino privado, un operador incluye cada dirección como su propio `/32` en `instance.config.infrastructure.egress.allowedDestinations`. Son destinos privados, por ejemplo, un broker dentro del clúster, **Amazon MSK**, **Amazon MQ**, y SNS/SQS a través de un **endpoint de interfaz de VPC con DNS privado**, que hace que incluso los nombres regionales predeterminados resuelvan a direcciones privadas. Un endpoint de interfaz tiene una dirección por zona de disponibilidad, y cada una necesita su propia entrada. Una autorización se aplica a **todos los inquilinos y a todos los conectores y webhooks**, no solo a aquel para el que se añadió.

Un resultado `blocked` solo le dice a un inquilino que el destino resolvió a una dirección rechazada, lo mismo que dice el rechazo de un webhook. La dirección rechazada queda registrada en el dead letter, que los operadores pueden leer y los inquilinos no.

## Gobernanza {#governance}

Toda acción de salida está sujeta a **gobernanza por inquilino**, porque una llamada externa es más costosa que una llamada dentro del proceso, y más fácil de convertir en una inundación autoinfligida.

- **Tasa limitada por inquilino en ambos extremos del salto.** El motor de detección descarta (sheds) las emisiones que exceden el presupuesto antes de despacharlas, y el servicio de conectores admite tráfico de destino dentro de un presupuesto acotado.
- **Nunca ilimitado.** Un inquilino sin límite configurado recae en un valor predeterminado de plataforma que nunca es ilimitado.
- **Medido según el momento de llegada.** El motor de detección y el servicio de conectores miden ambos las acciones de salida de un inquilino según el momento en que la telemetría que las desencadenó llegó a la plataforma. Por eso, un atraso de detecciones que se procesa tras un reinicio o una conmutación por error ni se confunde con una inundación ni se frena al techo del inquilino.
- **Descartado, no reintentado.** Una acción que sigue por encima del presupuesto se registra como un dead letter con motivo `shed`: se registra, no se reintenta. Por encima de un presupuesto por inquilino de aproximadamente una carta por segundo, las acciones descartadas se cuentan y se resumen en una carta por inquilino y minuto.

Ambos extremos aplican el techo por réplica de su servicio; consulta [Los techos son por réplica](./governance.md#per-replica).

## Aislamiento y dependencias

El servicio outbound-connectors se ejecuta en su **propio proceso**, separado de event-processing. Ese límite es deliberado:

- Un SDK de nube o un cliente de broker que se cuelga, falla o filtra memoria afecta solo a la entrega de conectores, nunca a la detección.
- Las bibliotecas cliente de broker y de nube que respaldan `publish` están enlazadas **únicamente** a este servicio. El motor de detección, que debe dar el mismo resultado cuando se reprocesan los eventos, se mantiene liviano, con una superficie de dependencias pequeña.
- El servicio resuelve las credenciales del conector por sí mismo; el motor de detección nunca las posee.

## Relacionado

- **[Procesamiento de Eventos y Alarmas](./event-processing.md)**: donde se crean las reglas y sus acciones.
- **[Arquitectura](./architecture.md)**: dónde se ubica outbound-connectors entre los servicios.
- Las credenciales se guardan en el **almacén de secretos** cifrado descrito en [Manejo de secretos](./architecture.md#secret-handling).
