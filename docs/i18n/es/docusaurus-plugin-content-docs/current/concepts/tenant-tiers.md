---
title: Niveles de inquilino y empaquetado
---

# Niveles de inquilino y empaquetado

Un **nivel de inquilino** (tenant tier) es la forma en que tú, como operador, empaquetas lo que recibe un inquilino. Los niveles los defines tú y les pones el nombre que coincida con lo que vendes: *oro / plata / bronce*, o cualquier otro. Un nivel responde a la pregunta "¿cuánto?": los [límites de gobernanza](./governance.md) que hereda un inquilino y los [modelos de IA](./ai-authoring.md) que puede usar. El nivel es una entidad de primera clase, y otras partes de la plataforma lo **leen** pero nunca lo redefinen.

Un nivel es una decisión de producto, no un control operativo. Los límites de tasa y el comportamiento ante la contención son diales que ajustas para mantener un sistema saludable. Un nivel es un paquete con nombre en el que está un cliente. En resumen: un dial de descarte (shed) se ajusta; un nivel se vende. Modelar el nivel como su propia entidad mantiene ese concepto de producto fuera de la maquinaria operativa de bajo nivel, donde de otro modo se reinventaría de forma incoherente.

:::note Estado
**Disponible hoy:** la entidad `TenantTier` en `user-management`, un registro de claves de configuración, la administración de niveles en el plano de administración de la instancia, la resolución de la configuración efectiva a través del nivel, el derecho de uso (entitlement) de modelos de IA (asignaciones y valores predeterminados por nivel), una matriz de concesión de **empaquetado de IA**, la presentación de niveles y el [descarte preferencial bajo contención](#preferential-shedding). **Planeado:** una señal de contención automática que eleve el nivel de descarte por sí sola.
:::

## Qué empaqueta un nivel

Un nivel es un paquete con nombre de configuraciones extraídas de un **registro de claves de configuración**: la lista de la plataforma de los diales que un nivel puede establecer. Hoy, dos consumidores leen el nivel de un inquilino.

- **Límites de gobernanza.** Las cuotas por inquilino se resuelven a través de su nivel: tasa de ingesta, tasa de salida, tasa de inferencia de IA, el [techo de comandos no entregados](./commands.md#held-command-ceiling) y los tres [límites de geocercas](./geofencing.md#what-a-boundary-may-be). Consulta [Gobernanza y cuotas](./governance.md). La regla de seguridad ante fallos se mantiene de principio a fin: un límite ausente o en cero se resuelve al **valor predeterminado de la plataforma, nunca a ilimitado**. Los techos se aplican por réplica; consulta [Los techos son por réplica](./governance.md#per-replica).
- **Derecho de uso de modelos de IA.** El nivel empaqueta qué [modelos de IA](./ai-authoring.md) puede usar un inquilino; un operador también puede conceder un modelo a un inquilino concreto además de los de su nivel. El modelo que un inquilino ejecuta para una función es una asignación `(tenant, function) → model` que recurre al **valor predeterminado del nivel**, si el nivel marca uno. Si ni el nivel ni una concesión por inquilino ponen un modelo en el menú del inquilino, el inquilino no tiene modelo: sin menú no hay modelo.

Muchos subsistemas leen un nivel, pero solo uno es su propietario. Por eso el patrón es siempre el mismo: los subsistemas **leen** el nivel, y ninguno almacena su propia copia de "a qué tiene derecho este inquilino".

## Descarte preferencial bajo contención {#preferential-shedding}

El nivel de un inquilino lleva una prioridad de descarte. Cuando la plataforma está bajo carga, esa prioridad determina qué inquilinos se degradan al final. Un operador también puede almacenar una prioridad de descarte en un inquilino concreto como anulación operativa por inquilino; si está definida, tiene precedencia sobre la del nivel.

Hoy un operador establece el piso de contención. Está planeada una señal de contención automática que eleve el nivel de descarte por sí sola.

## Los niveles son propiedad del operador, nunca configurables por el cliente

Un nivel, junto con la prioridad y los límites que lleva, es **configuración del operador**. Es:

- **Nunca configurable por el cliente.** Un inquilino no puede elevar sus propios límites ni cambiar su propio nivel.
- **Nunca una reclamación (claim) de token.** El nivel no está codificado en un JWT y no es una entrada de autorización. La plataforma lo resuelve del lado del servidor a partir del registro de inquilino del plano de control.

Hay una exención deliberada: **los tokens de identidad y los tokens de servicio no están vinculados a un nivel de autoridad.** Un nivel de autoridad es una idea distinta de un nivel de inquilino: indica si un permiso pertenece al plano de operador de toda la instancia o a un solo inquilino. Vincularlos colapsaría silenciosamente todos los límites de gobernanza por inquilino para esas rutas privilegiadas. Están exentos por diseño, no por omisión.

## Presentación de niveles {#presentation-a-shelf-not-a-ladder}

Los niveles llevan un **orden de visualización** y un **color**, para que puedas presentarlos de forma coherente: píldoras de colores, una lista reordenable por arrastre y una vista de detalle de nivel por pestañas. El orden de visualización es un estante, no una escalera. Dispone los niveles para su presentación y no es una jerarquía implícita con la que calcule ningún subsistema. El orden es cosmético; el derecho de uso proviene de lo que un nivel realmente empaqueta.

## Dónde vive en la consola

- **Niveles**: `/admin/tiers` (plano de administración). Crea, edita, colorea y reordena niveles; abre un nivel para ver sus configuraciones empaquetadas.
- **Empaquetado de IA**: la matriz entre niveles que indica qué modelos de IA puede usar cada nivel.
- **Por inquilino**: establece el nivel de un inquilino en su página de detalle de administración. Su modelo de IA por función también se establece allí, a partir del menú del inquilino: los modelos de su nivel más cualquier concesión por inquilino.

Las dos superficies de IA anteriores necesitan el [servicio opcional `ai-inference`](./ai-authoring.md), que se entrega en el perfil de despliegue `full`. Sin él, el nivel funciona con normalidad y los límites de gobernanza no se ven afectados. La matriz de empaquetado de IA y el menú de modelos por inquilino informan de que esta instancia no ejecuta el área.

## Por qué un nivel es su propia entidad {#a-packaging-concept-in-exactly-one-place}

Los niveles de inquilino son una capacidad familiar y esperada para cualquiera que empaquete una plataforma IoT multiinquilino: algo básico, hecho de forma limpia. Modelar un nivel como su propia entidad de primera clase significa que "a qué tiene derecho un inquilino" vive en **exactamente un lugar**, en lugar de estar disperso entre los servicios que lo consumen. La gobernanza lo lee, el derecho de uso de IA lo lee, y ninguno mantiene su propia copia.

## Véase también

- [Gobernanza y cuotas](./governance.md): los límites que suministra un nivel.
- [Autoría asistida por IA](./ai-authoring.md): el derecho de uso de modelos de IA que empaqueta un nivel.
- [Multitenencia](./multi-tenancy.md): cómo se modelan y se aíslan los inquilinos.
