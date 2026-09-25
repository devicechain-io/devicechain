---
title: Gobernanza y cuotas
---

# Gobernanza y cuotas

DeviceChain ejecuta [un único conjunto compartido de servicios para todos los inquilinos](./multi-tenancy.md). El aislamiento de inquilinos en esa instancia compartida trata sobre la **corrección**: un inquilino nunca puede ver los datos de otro. La gobernanza trata sobre la **equidad**. Las cuotas por inquilino impiden que la ráfaga, la tormenta de reconexiones o la regla mal configurada de un inquilino agoten la capacidad que comparten todos. Aislar los datos sin repartir los recursos con equidad seguiría permitiendo que una flota degradara a todos los demás, y la gobernanza cierra esa brecha.

Los límites se aplican en los bordes, antes de que el tráfico llegue a la infraestructura compartida:

- **Ingesta.** El servicio event-sources aplica un límite de tasa por inquilino a medida que se decodifica el tráfico de dispositivos, antes de publicarlo en el pipeline interno. El exceso de un inquilino que supera el límite se descarta en la puerta de entrada en lugar de acumularse en el stream compartido.
- **Egreso.** El volumen saliente de las [acciones](./outbound-connectors.md#governance) tiene un límite de tasa por inquilino en ambos extremos del salto. El motor de detección descarta las emisiones que exceden el presupuesto antes del despacho, y el servicio outbound-connectors admite tráfico hacia los destinos dentro de un presupuesto acotado. Ambos extremos miden una acción según el momento en que llegó a la plataforma la telemetría que la desencadenó, de modo que un atraso procesado tras un reinicio se cobra como ocurrió.
- **Inferencia de IA.** El servicio de IA opcional aplica un límite de tasa por inquilino, para que las sesiones de autoría de un inquilino no puedan monopolizar la vía de inferencia compartida. Puedes observar el gasto de inferencia: los tokens de entrada y salida que reporta el proveedor se contabilizan como métricas de toda la instancia que un operador puede vigilar y sobre las que puede alertar. El gasto no se registra por inquilino y no se aplica ningún presupuesto sobre él. El techo de tasa es lo único que acota a un inquilino aquí.
- **Comandos no entregados.** Un techo por inquilino limita cuántos comandos pueden estar esperando a salir a la vez, y se aplica al encolar el comando. Es el único límite de esta lista que **rechaza** en lugar de descartar. Los tres anteriores descartan el exceso de tráfico de un inquilino; este devuelve un rechazo que quien llama puede ver y reintentar, porque un comando es una actuación física y descartarlo en silencio no es una opción. Consulta [cuánta acumulación puede retener un inquilino](./commands.md#held-command-ceiling).

Todos los puntos de aplicación resuelven los límites a través de una única biblioteca de gobernanza compartida en el núcleo de la plataforma, un único obtenedor y resolutor de límites por inquilino. Así, cada dimensión responde de la misma manera a la pregunta "¿qué se le permite a este inquilino?".

## La regla de seguridad ante fallos

Esta es la propiedad de seguridad de la que depende el resto de la gobernanza:

> Un límite ausente o en cero se resuelve al **valor por defecto de la plataforma**, nunca a ilimitado.

Ningún estado de configuración ni modo de fallo deja a un inquilino sin gobernanza. Un inquilino sin límite explícito recibe el techo por defecto de la plataforma, y un límite fijado en cero significa lo mismo, no "sin límite". El diseño prohíbe una gobernanza que falle en abierto, donde un error tipográfico o una fila ausente elimina silenciosamente un techo. El [alcance de datos del inquilino](./multi-tenancy.md#isolation) adopta la misma postura en el lado de la corrección: rechaza una consulta cuando falta el inquilino.

### Antes de conocer el techo de un inquilino {#unresolved-ceilings}

Un servicio obtiene los techos de un inquilino del plano de control y los guarda en caché. Hasta que los ha leído, mide a ese inquilino con su **valor por defecto de la plataforma**. Eso ocurre la primera vez que ve a un inquilino después de arrancar, y mientras el plano de control no sea alcanzable. El valor por defecto sigue siendo un techo, nunca ilimitado. Pero para un inquilino cuyo nivel fija uno más bajo, admite más de lo que el nivel permite hasta que la lectura tiene éxito. Un inquilino cuyos techos ya se leyeron conserva sus últimos valores conocidos durante una caída.

Cada servicio que aplica límites cuenta el tráfico que admitió de esta forma en `devicechain_<service>_governance_unresolved_admissions_total`, etiquetado por dimensión y causa:

- `unreachable`: no se pudo consultar al plano de control;
- `unknown-tenant`: respondió que no existe tal inquilino;
- `pending`: todavía no hay respuesta.

La alerta `TenantsMeteredAtPlatformDefault` se dispara solo cuando `unreachable` sigue subiendo durante 15 minutos.

### Nombres de inquilino que no se pueden confirmar {#unconfirmed-tenants}

El endpoint de ingesta HTTP toma el inquilino de la ruta de la petición, antes de comprobar cualquier credencial de dispositivo. Por eso la ingesta HTTP tiene una asignación propia para cada inquilino, separada de la que consume el tráfico MQTT, NATS y de presencia del broker de ese inquilino. Las peticiones HTTP que nombran a un inquilino no pueden agotar el tráfico de sus dispositivos.

Aun así, cualquiera que pueda llegar al puerto HTTP y conozca el nombre de un inquilino puede agotar la asignación HTTP de ese inquilino, porque la credencial del dispositivo solo se comprueba después de admitir la petición. Dentro de la asignación HTTP, un nombre de inquilino obtiene una asignación propia solo si el plano de control lo ha confirmado, o de un conjunto fijo de 1024. Pasado ese conjunto, todos esos nombres comparten una única asignación con el valor por defecto de la plataforma, y se dispara la alerta `RateLimiterOverflowInUse`.

El conjunto acota la memoria del servicio, no el total admitido entre nombres inventados: entre ellos se puede admitir hasta 1024 veces el valor por defecto de la plataforma.

El tráfico MQTT, NATS y LwM2M procede de un origen autenticado o en el que el operador decidió confiar, y siempre obtiene su propia asignación:

- el broker de la plataforma autentica cada dispositivo;
- LwM2M comprueba la clave del dispositivo;
- un origen de broker MQTT externo es de confianza porque el operador lo configuró.

## Dónde viven los límites

Los límites de gobernanza son configuración del operador y del inquilino, no entrada del cliente:

- Se declaran en el **registro del plano de control** del inquilino y se editan a través de la consola de administración y la API del plano de control.
- **Nunca son un claim del token.** El JWT de quien llama identifica al inquilino, y luego el servicio que aplica el límite resuelve los límites de ese inquilino desde la configuración. Nada que envíe un cliente (encabezados, claims, payloads) puede elevar su propio techo.

## Los niveles suministran los techos

Los techos de gobernanza de un inquilino provienen de su **[nivel (tier)](./tenant-tiers.md)**, la entidad de empaquetado definida por el operador que responde "¿qué tipo de cliente es este?". El nivel es donde un operador empaqueta el *cuánto*: los techos por defecto que hereda una clase de inquilinos. La resolución sigue una cascada de tres niveles:

**anulación por inquilino → configuración del nivel → valor por defecto de la plataforma**

Las anulaciones por inquilino son excepciones auditadas, no el mecanismo principal. El nivel lleva la respuesta empaquetada, y el valor por defecto de la plataforma es el piso que garantiza la regla de seguridad ante fallos.

La misma cascada gobierna el derecho de uso de modelos de IA: una asignación de modelo por inquilino y, después, el modelo que el nivel del inquilino marca como su valor por defecto. Así, "¿en qué nivel está este inquilino?" responde una misma pregunta coherente en gobernanza y en IA. El branding del inquilino también sigue una cascada, pero sin escalón de nivel: la anulación del inquilino, luego la configuración de sistema `branding.default` del operador y, por último, el valor por defecto de fábrica.

## Los techos son por réplica {#per-replica}

Cada copia en ejecución de un servicio aplica por su cuenta todos los techos de tasa de esta página, sin coordinación entre copias. Si ejecutas dos réplicas de `event-sources`, `outbound-connectors` o `ai-inference` y se reparten el tráfico de un inquilino, ese inquilino puede ser admitido hasta al doble de su techo, y N réplicas permiten hasta N veces. La instalación por defecto ejecuta una réplica de cada uno, y ahí el techo es exacto.

Hay dos techos que no se multiplican:

- el techo de comandos no entregados es un recuento que se lleva en la base de datos;
- el techo de salida del motor de detección se cobra solo en la réplica que detecta.

Si escalas un servicio horizontalmente, fija los techos del nivel según el número de réplicas que ejecutas.

### Cuándo la ingesta puede admitir a un inquilino por encima de su techo {#ingest-above-ceiling}

Dentro de una réplica de `event-sources`, el techo de ingesta de un inquilino se aplica por separado a cada una de tres asignaciones, no al inquilino en su conjunto:

- **Tráfico en vivo**: lo que los dispositivos del inquilino envían ahora por MQTT y NATS, incluida la presencia del broker.
- **Atraso (backlog)**: mensajes que el broker de la plataforma guardó mientras `event-sources` estaba caído o retrasado, medidos según cuándo se enviaron a medida que se drenan. Un inquilino que drena un atraso tras una caída mientras también envía en vivo puede ser admitido hasta al doble de su techo hasta que el drenaje se pone al día.
- **Ingesta HTTP**: se mide por separado, así que un inquilino que envía por HTTP y por MQTT a la vez puede ser admitido hasta su techo en cada uno.

En el peor caso, un inquilino puede ser admitido al triple de su techo en cada réplica, multiplicado por el número de réplicas como se indica arriba.

## Observar la gobernanza {#seeing-it-work}

El volumen descartado se expone como una métrica operativa. Un operador ve a un inquilino que ha alcanzado un techo, o una regla que ha empezado a emitir en exceso, antes de que se convierta en un ticket de soporte. La gobernanza está pensada para mostrarse como presión observable, no como pérdida silenciosa.

:::note Estado
**Aplicado hoy:** limitación de tasa de ingesta por inquilino (event-sources), gobernanza de egreso en ambos extremos del salto de la acción (event-processing y outbound-connectors), limitación de tasa de inferencia de IA por inquilino con observabilidad del gasto, y el techo de comandos no entregados por inquilino (command-delivery). Todos siguen la regla de seguridad ante fallos descrita arriba.

**Planeado bajo el mismo modelo:** gobernanza de API/consultas por inquilino, límites de stream por inquilino en el bus interno y un techo de expansión (fan-out) de relaciones.
:::

## Relacionado

- **[Multitenencia](./multi-tenancy.md)**: la mitad de la corrección, un aislamiento de datos que rechaza las consultas sin alcance en la misma instancia compartida.
- **[Conectores salientes](./outbound-connectors.md#governance)**: cómo se aplica la gobernanza de egreso a los webhooks y las publicaciones a brokers.
- **[Niveles de inquilino](./tenant-tiers.md)**: la entidad de empaquetado que suministra los techos por defecto de un inquilino.
