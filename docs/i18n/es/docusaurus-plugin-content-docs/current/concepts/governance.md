---
title: Gobernanza y cuotas
---

# Gobernanza y cuotas

DeviceChain ejecuta [un único conjunto compartido de servicios para todos los inquilinos](./multi-tenancy.md), y el aislamiento de inquilinos allí trata sobre la **corrección**: un inquilino nunca puede ver los datos de otro. La gobernanza es la otra mitad de esa apuesta: la **equidad**. Las cuotas por inquilino aseguran que la ráfaga, la tormenta de reconexiones o la regla mal configurada de un inquilino no puedan agotar la capacidad que todos los inquilinos comparten. El aislamiento de datos sin equidad de recursos igual permite que una flota degrade a todos los demás; la gobernanza cierra esa brecha.

Los límites se aplican **en los bordes**, antes de que el tráfico llegue a la infraestructura compartida:

- **Ingesta** — un límite de tasa por inquilino en el servicio event-sources, aplicado a medida que se decodifica el tráfico de dispositivos, antes de que se publique en el pipeline interno. El exceso de un inquilino que supera el límite se descarta en la puerta de entrada en lugar de acumularse en el stream compartido.
- **Egreso** — el volumen saliente de las [acciones REACT](./outbound-connectors.md#governance) tiene límite de tasa por inquilino en ambos extremos del salto: el motor de detección descarta las emisiones que exceden el presupuesto antes del despacho, y el servicio outbound-connectors admite tráfico de destino dentro de un presupuesto acotado.
- **Inferencia de IA** — el servicio de IA de suscripción voluntaria aplica un límite de tasa por inquilino y registra el gasto por inquilino, de modo que las sesiones de autoría de un inquilino no puedan monopolizar (ni hacer crecer silenciosamente) la vía de inferencia compartida.

Todos los puntos de aplicación resuelven límites a través de una **biblioteca de gobernanza** compartida en el núcleo de la plataforma — un único resolutor/obtenedor de límites por inquilino — de modo que cada dimensión responde de la misma manera a la pregunta "¿qué se le permite a este inquilino?".

## La regla de seguridad ante fallos

La propiedad de seguridad fundamental, enunciada con exactitud:

> Un límite ausente o en cero se resuelve al **valor por defecto de la plataforma** — nunca a ilimitado.

No existe estado de configuración, ni modo de fallo, en el que un inquilino quede sin gobernanza. Un inquilino sin límite explícito recibe el techo por defecto de la plataforma; un límite fijado en cero significa lo mismo, no "sin límite". Una gobernanza que falla en *abierto* — donde un error tipográfico o una fila ausente elimina silenciosamente un techo — es exactamente el fallo que este diseño prohíbe, y es la misma postura de fallo cerrado que adopta el [alcance de datos del inquilino](./multi-tenancy.md#isolation) en el lado de la corrección.

## Dónde viven los límites

Los límites de gobernanza son **configuración del operador y del inquilino**, no entrada del cliente:

- Se declaran en el **registro del plano de control** del inquilino y se editan a través de la consola de administración y la API del plano de control.
- **Nunca** son una afirmación (claim) de token. El JWT de quien llama identifica al inquilino; el servicio que aplica el límite luego resuelve los límites de ese inquilino desde la configuración. Nada que el cliente envíe — encabezados, claims, payloads — puede elevar su propio techo.

## Los niveles (tiers) suministran los techos

Los techos de gobernanza de un inquilino provienen de su **[nivel (tier)](./tenant-tiers.md)** — la entidad de empaquetado definida por el operador que responde "¿qué tipo de cliente es este?". El nivel es donde un operador empaqueta *cuánto*: los techos por defecto que hereda una clase de inquilinos. La resolución sigue una cascada de tres niveles:

**anulación por inquilino → configuración del nivel → valor por defecto de la plataforma**

Las anulaciones por inquilino son excepciones auditadas, no el mecanismo — el nivel lleva la respuesta empaquetada, y el valor por defecto de la plataforma es el piso que garantiza la regla de seguridad ante fallos. La misma forma de cascada gobierna el branding y el derecho de uso de modelos de IA, de modo que "¿en qué nivel está este inquilino?" responde una pregunta consistente en todos los subsistemas.

## Verlo funcionar

El volumen descartado se expone como una métrica operativa, de modo que un inquilino que ha alcanzado un techo — o una regla que ha empezado a emitir en exceso — es visible para un operador antes de convertirse en un ticket de soporte. La gobernanza está pensada para ser presión observable, no pérdida silenciosa.

:::note Estado
**Aplicado hoy:** limitación de tasa de ingesta por inquilino (event-sources), gobernanza de egreso saliente en ambos extremos del salto REACT (event-processing + outbound-connectors), y limitación de tasa de inferencia de IA por inquilino con observabilidad del gasto — todo a través del resolutor de gobernanza compartido del núcleo, todo sujeto a la regla de seguridad-ante-fallos del valor por defecto de la plataforma. **Planeado bajo el mismo modelo:** gobernanza de API/consultas por inquilino, límites de stream por inquilino en el bus interno, y un techo de expansión (fan-out) de relaciones.
:::

## Relacionado

- **[Multitenencia](./multi-tenancy.md)** — la mitad de la corrección: aislamiento de datos con fallo cerrado en la misma instancia compartida.
- **[Conectores salientes](./outbound-connectors.md#governance)** — cómo se aplica la gobernanza de egreso a los webhooks y las publicaciones a brokers.
- **[Niveles de inquilino](./tenant-tiers.md)** — la entidad de empaquetado que suministra los techos por defecto de un inquilino.
