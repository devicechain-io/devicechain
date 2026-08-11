---
title: Niveles de inquilino y empaquetado
---

# Niveles de inquilino y empaquetado

Un **nivel de inquilino** (tenant tier) es la forma en que un operador empaqueta lo que recibe un inquilino. Es una entidad de primera clase, definida por el operador —*oro / plata / bronce*, o cualquier nombre que un operador elija y venda— que otras partes de la plataforma **leen** pero nunca redefinen. Un nivel responde a la pregunta **"¿cuánto?"**: los [límites de gobernanza](./governance.md) que hereda un inquilino y los [modelos de IA](./ai-authoring.md) que un inquilino puede usar.

La distinción rectora: **un dial de contención (shed) se ajusta; un nivel se vende.** Los controles operativos (límites de tasa, comportamiento ante contención) son cosas que se ajustan para mantener un sistema saludable. Un nivel es una decisión de producto: un paquete con nombre en el que está un cliente. Modelarlo como su propia entidad mantiene ese concepto de producto fuera de la maquinaria operativa de bajo nivel, donde de otro modo se reinventaría de forma incoherente.

:::note Estado
**Disponible hoy:** la entidad `TenantTier` en `user-management`; un registro de claves de configuración que define lo que empaqueta un nivel; la administración de niveles en el plano de administración de la instancia; la resolución de configuración efectiva a través del nivel; el modelo de derecho de uso (entitlement) de modelos de IA (asignaciones + valores predeterminados por nivel); una matriz de concesión de **empaquetado de IA**; la presentación de niveles (orden de visualización, color, reordenamiento por arrastre); y la **contención preferencial bajo carga** (shedding): el nivel de un inquilino lleva una prioridad de contención que determina quién se degrada al final cuando la plataforma está bajo carga, con la prioridad almacenada del nivel disponible como una anulación operativa por inquilino. **Planeado:** una señal de contención automática que eleve el nivel de contención por sí sola; hoy un operador establece el piso de contención.
:::

## Qué empaqueta un nivel

Un nivel es un paquete con nombre de configuraciones extraídas de un **registro de claves de configuración**: la lista de la plataforma de los diales que un nivel puede establecer. Dos consumidores leen hoy el nivel de un inquilino:

- **Límites de gobernanza.** Las cuotas por inquilino de un inquilino (tasa de ingesta, tasa de salida, tasa de inferencia de IA) se resuelven a través de su nivel. Consulte [Gobernanza y cuotas](./governance.md). La regla de seguridad ante fallos se mantiene de principio a fin: un límite ausente o en cero se resuelve al **valor predeterminado de la plataforma, nunca a ilimitado**.
- **Derecho de uso de modelos de IA.** Qué [modelos de IA](./ai-authoring.md) puede usar un inquilino se empaqueta en el nivel. El modelo que un inquilino ejecuta para una función es una asignación `(inquilino, función) → modelo` que recurre al **valor predeterminado del nivel**; si un nivel no empaqueta ningún modelo, el inquilino no tiene modelo. *Sin menú significa sin modelo.*

Debido a que un nivel es leído por muchos subsistemas pero es propiedad de uno solo, el patrón es siempre el mismo: los subsistemas **leen** el nivel; nunca almacenan su propia copia de "a qué tiene derecho este inquilino".

## Los niveles son propiedad del operador, nunca configurables por el cliente

Un nivel —y la prioridad y los límites que lleva— es **configuración del operador**. Es:

- **Nunca configurable por el cliente.** Un inquilino no puede elevar sus propios límites ni cambiar su propio nivel.
- **Nunca una reclamación (claim) de token.** El nivel no está codificado en un JWT y no es una entrada de autorización; se resuelve del lado del servidor a partir del registro de inquilino del plano de control.

Hay una exención deliberada: **los tokens de identidad y los tokens de servicio no están vinculados a un nivel de autoridad.** Vincularlos colapsaría silenciosamente todos los límites de gobernanza por inquilino para esas rutas privilegiadas, por lo que están exentos por diseño y no por omisión.

## Presentación: un estante, no una escalera

Los niveles llevan un **orden de visualización** y un **color** para que un operador pueda presentarlos de forma coherente (píldoras de colores, una lista reordenable por arrastre, una vista de detalle de nivel por pestañas). El orden de visualización es un **estante, no una escalera**: es la forma en que los niveles se disponen para su presentación, no una jerarquía implícita que ningún subsistema calcula. El orden es cosmético; el derecho de uso proviene de lo que un nivel realmente empaqueta.

## Dónde vive en la consola

- **Niveles** — `/admin/tiers` (plano de administración): crear, editar, colorear y reordenar niveles; abrir un nivel para ver sus configuraciones empaquetadas.
- **Empaquetado de IA** — la matriz entre niveles que asigna qué modelos de IA puede usar cada nivel.
- **Por inquilino** — el nivel de un inquilino se establece en su página de detalle de administración; su modelo de IA por función también se establece allí, a partir del menú derivado del nivel.

## Un concepto de empaquetado en exactamente un lugar

Los niveles de inquilino son una capacidad familiar y esperada para cualquiera que empaquete una plataforma IoT multiinquilino: algo dado por sentado, hecho de forma limpia. El punto de modelar un nivel como su propia entidad de primera clase es que el concepto de "a qué tiene derecho un inquilino" vive en **exactamente un lugar** en lugar de estar disperso entre los servicios que lo consumen: la gobernanza lo lee, el derecho de uso de IA lo lee, y ninguno mantiene su propia copia.

## Véase también

- [Gobernanza y cuotas](./governance.md) — los límites que suministra un nivel.
- [Autoría asistida por IA](./ai-authoring.md) — el derecho de uso de modelos de IA que empaqueta un nivel.
- [Multitenencia](./multi-tenancy.md) — cómo se modelan y se aíslan los inquilinos.
