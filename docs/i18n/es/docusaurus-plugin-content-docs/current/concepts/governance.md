---
title: Gobernanza y cuotas
---

# Gobernanza y cuotas

DeviceChain ejecuta [un único conjunto compartido de servicios para todos los inquilinos](./multi-tenancy.md), y el aislamiento de inquilinos allí trata sobre la **corrección**: un inquilino nunca puede ver los datos de otro. La gobernanza es la otra mitad de esa apuesta: la **equidad**. Las cuotas por inquilino aseguran que la ráfaga, la tormenta de reconexiones o la regla mal configurada de un inquilino no puedan agotar la capacidad que todos los inquilinos comparten. El aislamiento de datos sin equidad de recursos igual permite que una flota degrade a todos los demás; la gobernanza cierra esa brecha.

Los límites se aplican **en los bordes**, antes de que el tráfico llegue a la infraestructura compartida:

- **Ingesta** — un límite de tasa por inquilino en el servicio event-sources, aplicado a medida que se decodifica el tráfico de dispositivos, antes de que se publique en el pipeline interno. El exceso de un inquilino que supera el límite se descarta en la puerta de entrada en lugar de acumularse en el stream compartido.
- **Egreso** — el volumen saliente de las [acciones REACT](./outbound-connectors.md#governance) tiene límite de tasa por inquilino en ambos extremos del salto: el motor de detección descarta las emisiones que exceden el presupuesto antes del despacho, y el servicio outbound-connectors admite tráfico de destino dentro de un presupuesto acotado. Ambos extremos miden una acción según el momento en que llegó a la plataforma la telemetría que la desencadenó, de modo que un atraso procesado tras un reinicio se cobra como ocurrió.
- **Inferencia de IA** — el servicio de IA de suscripción voluntaria aplica un límite de tasa por inquilino, de modo que las sesiones de autoría de un inquilino no puedan monopolizar la vía de inferencia compartida. El gasto de inferencia es observable — los tokens de entrada y salida que reporta el proveedor se contabilizan como métricas de toda la instancia que un operador puede vigilar y sobre las que puede alertar — pero no se registra por inquilino, y no se aplica ningún presupuesto sobre él: el techo de tasa es lo único que acota a un inquilino aquí.
- **Comandos no entregados** — un techo por inquilino sobre cuántos comandos pueden estar esperando a salir a la vez, aplicado en el momento de emitir el comando. Es el único límite de esta lista que **rechaza** en lugar de descartar: los tres anteriores descartan el exceso de tráfico de un inquilino, mientras que este devuelve un rechazo que quien llama puede ver y reintentar — un comando es una actuación física, así que descartarlo en silencio no es una opción. Consulte [cuánta acumulación puede retener un inquilino](./commands.md#held-command-ceiling).

Todos los puntos de aplicación resuelven límites a través de una **biblioteca de gobernanza** compartida en el núcleo de la plataforma — un único resolutor/obtenedor de límites por inquilino — de modo que cada dimensión responde de la misma manera a la pregunta "¿qué se le permite a este inquilino?".

## La regla de seguridad ante fallos

La propiedad de seguridad fundamental, enunciada con exactitud:

> Un límite ausente o en cero se resuelve al **valor por defecto de la plataforma** — nunca a ilimitado.

No existe estado de configuración, ni modo de fallo, en el que un inquilino quede sin gobernanza. Un inquilino sin límite explícito recibe el techo por defecto de la plataforma; un límite fijado en cero significa lo mismo, no "sin límite". Una gobernanza que falla en *abierto* — donde un error tipográfico o una fila ausente elimina silenciosamente un techo — es exactamente el fallo que este diseño prohíbe, y es la misma postura de fallo cerrado que adopta el [alcance de datos del inquilino](./multi-tenancy.md#isolation) en el lado de la corrección.

### Antes de conocer el techo de un inquilino {#unresolved-ceilings}

Un servicio obtiene los techos de un inquilino del plano de control y los guarda en caché. Hasta que los ha leído (la primera vez que ve a un inquilino después de arrancar, o mientras el plano de control no sea alcanzable), mide a ese inquilino con su **valor por defecto de la plataforma**. Ese valor sigue siendo un techo, nunca ilimitado, pero para un inquilino cuyo nivel fija uno más bajo admite más de lo que el nivel permite hasta que la lectura tiene éxito. Un inquilino cuyos techos ya se leyeron conserva sus últimos valores conocidos durante una caída.

Cada servicio que aplica límites cuenta el tráfico que admitió de esta forma en `devicechain_<service>_governance_unresolved_admissions_total`, etiquetado por dimensión y causa:

- `unreachable`: no se pudo consultar al plano de control;
- `unknown-tenant`: respondió que no existe tal inquilino;
- `pending`: todavía no hay respuesta.

La alerta `TenantsMeteredAtPlatformDefault` se dispara solo cuando `unreachable` sigue subiendo durante 15 minutos.

### Nombres de inquilino que no se pueden confirmar {#unconfirmed-tenants}

El endpoint de ingesta HTTP toma el inquilino de la ruta de la petición, antes de comprobar cualquier credencial de dispositivo. Por eso la ingesta HTTP tiene una asignación propia para cada inquilino, separada de la que consume el tráfico MQTT, NATS y de presencia del broker de ese inquilino, y las peticiones HTTP que nombran a un inquilino no pueden agotar el tráfico de sus dispositivos. Cualquiera que pueda llegar al puerto HTTP y conozca el nombre de un inquilino sí puede agotar la asignación HTTP de ese inquilino, porque la credencial del dispositivo solo se comprueba después de admitir la petición. Dentro de la asignación HTTP, un nombre de inquilino obtiene una asignación propia solo si el plano de control lo ha confirmado, o de un conjunto fijo de 1024. Pasado ese conjunto, todos esos nombres comparten una única asignación con el valor por defecto de la plataforma, y se dispara la alerta `RateLimiterOverflowInUse`. El tráfico MQTT, NATS y LwM2M procede de un origen autenticado o en el que el operador decidió confiar: el broker de la plataforma autentica cada dispositivo, LwM2M comprueba la clave del dispositivo, y un broker MQTT externo es de confianza porque el operador lo configuró. Ese tráfico siempre obtiene su propia asignación.

El conjunto acota la memoria del servicio, no el total admitido entre nombres inventados: entre ellos se puede admitir hasta 1024 veces el valor por defecto de la plataforma.

## Dónde viven los límites

Los límites de gobernanza son **configuración del operador y del inquilino**, no entrada del cliente:

- Se declaran en el **registro del plano de control** del inquilino y se editan a través de la consola de administración y la API del plano de control.
- **Nunca** son una afirmación (claim) de token. El JWT de quien llama identifica al inquilino; el servicio que aplica el límite luego resuelve los límites de ese inquilino desde la configuración. Nada que el cliente envíe — encabezados, claims, payloads — puede elevar su propio techo.

## Los niveles (tiers) suministran los techos

Los techos de gobernanza de un inquilino provienen de su **[nivel (tier)](./tenant-tiers.md)** — la entidad de empaquetado definida por el operador que responde "¿qué tipo de cliente es este?". El nivel es donde un operador empaqueta *cuánto*: los techos por defecto que hereda una clase de inquilinos. La resolución sigue una cascada de tres niveles:

**anulación por inquilino → configuración del nivel → valor por defecto de la plataforma**

Las anulaciones por inquilino son excepciones auditadas, no el mecanismo — el nivel lleva la respuesta empaquetada, y el valor por defecto de la plataforma es el piso que garantiza la regla de seguridad ante fallos. La misma cascada gobierna el derecho de uso de modelos de IA — una asignación de modelo por inquilino, y luego el modelo que el nivel del inquilino marca como su valor por defecto — de modo que "¿en qué nivel está este inquilino?" responde una pregunta consistente en los subsistemas de gobernanza y de IA. El branding del inquilino también sigue una cascada, pero sin ningún escalón de nivel: la anulación del inquilino, luego la configuración de sistema `branding.default` del operador, y luego el valor por defecto de fábrica.

## Los techos son por réplica {#per-replica}

Cada techo de tasa de esta página lo aplica por separado cada copia en ejecución del servicio que lo impone, sin coordinación entre copias. Si ejecuta dos réplicas de `event-sources`, `outbound-connectors` o `ai-inference` y se reparten el tráfico de un inquilino, ese inquilino puede ser admitido hasta al doble de su techo, y N réplicas permiten hasta N veces. La instalación por defecto ejecuta una réplica de cada uno, y ahí el techo es exacto. Hay dos techos que no se multiplican: el techo de comandos no entregados es un recuento que se lleva en la base de datos, y el techo de salida del motor de detección se consume solo en la réplica que detecta. Si escala un servicio, fije los techos del nivel para el número de réplicas que ejecuta.

### Cuándo la ingesta puede admitir a un inquilino por encima de su techo {#ingest-above-ceiling}

Dentro de una réplica de `event-sources`, el techo de ingesta de un inquilino se aplica por separado a cada una de tres asignaciones, no al inquilino en su conjunto:

- **Tráfico en vivo**: lo que los dispositivos del inquilino envían ahora por MQTT y NATS, incluida la presencia del broker.
- **Atraso (backlog)**: mensajes que el broker de la plataforma guardó mientras `event-sources` estaba caído o retrasado, medidos según cuándo se enviaron a medida que se drenan. Un inquilino que drena un atraso tras una caída mientras también envía en vivo puede ser admitido hasta al doble de su techo hasta que el drenaje se pone al día.
- **Ingesta HTTP**: se mide por separado, así que un inquilino que envía por HTTP y por MQTT a la vez puede ser admitido hasta su techo en cada uno.

En el peor caso, un inquilino puede ser admitido al triple de su techo en cada réplica, multiplicado por el número de réplicas como se indica arriba.

## Verlo funcionar

El volumen descartado se expone como una métrica operativa, de modo que un inquilino que ha alcanzado un techo — o una regla que ha empezado a emitir en exceso — es visible para un operador antes de convertirse en un ticket de soporte. La gobernanza está pensada para ser presión observable, no pérdida silenciosa.

:::note Estado
**Aplicado hoy:** limitación de tasa de ingesta por inquilino (event-sources), gobernanza de egreso saliente en ambos extremos del salto REACT (event-processing + outbound-connectors), y limitación de tasa de inferencia de IA por inquilino con observabilidad del gasto — todo a través del resolutor de gobernanza compartido del núcleo, todo sujeto a la regla de seguridad-ante-fallos del valor por defecto de la plataforma. También aplicado, con la misma cascada y la misma regla de seguridad ante fallos pero rechazando en lugar de descartar: el techo de comandos no entregados por inquilino (command-delivery). **Planeado bajo el mismo modelo:** gobernanza de API/consultas por inquilino, límites de stream por inquilino en el bus interno, y un techo de expansión (fan-out) de relaciones.
:::

## Relacionado

- **[Multitenencia](./multi-tenancy.md)** — la mitad de la corrección: aislamiento de datos con fallo cerrado en la misma instancia compartida.
- **[Conectores salientes](./outbound-connectors.md#governance)** — cómo se aplica la gobernanza de egreso a los webhooks y las publicaciones a brokers.
- **[Niveles de inquilino](./tenant-tiers.md)** — la entidad de empaquetado que suministra los techos por defecto de un inquilino.
