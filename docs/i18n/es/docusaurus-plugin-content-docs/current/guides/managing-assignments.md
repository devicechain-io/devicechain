---
sidebar_position: 4
title: Gestión de asignaciones de dispositivos
---

# Gestión de asignaciones de dispositivos

Una **asignación** relaciona un dispositivo con un cliente, un área o un activo, para que su telemetría lleve contexto organizativo. En DeviceChain, una asignación es una **relación rastreada** sobre el grafo de entidades uniforme. No existe un registro de asignación independiente.

:::note Estado
Disponible. Gestiona las asignaciones desde la pestaña **Asignación** de la página de detalle del dispositivo en la consola, o mediante la API GraphQL de device-management.
:::

## La asignación organiza; no restringe el acceso {#assignment-organizes-it-does-not-gate}

Un dispositivo se autentica con una credencial. La asignación solo organiza sus datos, y ambas cosas son independientes:

- Un dispositivo registrado y con credencial reporta telemetría de inmediato, incluso sin asignación. Sus eventos se resuelven con un conjunto de anclajes vacío: igualmente se persisten y actualizan el estado en vivo del dispositivo, pero todavía no se atribuyen a un cliente, un área ni un activo.
- Si asignas el dispositivo más adelante, sus eventos posteriores reciben un anclaje, de modo que consultas como "cada lectura del Edificio 7" los encuentran.

Por lo tanto, los dispositivos sin asignar nunca se descartan silenciosamente. Esto supone un cambio respecto al comportamiento anterior.

## Cada asignación es un anclaje {#every-assignment-is-an-anchor}

Un dispositivo puede tener varias asignaciones a la vez: un cliente, un área y un activo. Cuando el dispositivo reporta un evento, cada asignación se registra como un **anclaje** en ese evento. Así, la misma lectura se puede consultar por cada dimensión; aparece bajo el cliente y bajo el área. Ninguna asignación es la principal: todas son iguales.

Los anclajes de cada evento viven en un conjunto hermano `event_anchors`, con una fila por asignación. Una consulta filtrada por anclaje ("eventos del área Y") coincide con los eventos cuyo conjunto contiene ese anclaje.

Los anclajes se capturan en el momento de la escritura, así que el historial es estable. Un dispositivo que más tarde cambia de área conserva, en cada evento antiguo, el área en la que estaba cuando ocurrió ese evento.

## Asignar un dispositivo (consola) {#assign-a-device-console}

1. Abre la página de detalle del dispositivo y selecciona la pestaña **Asignación**.
2. Elige un tipo de destino (Cliente / Área / Activo) y selecciona la entidad de destino.
3. Haz clic en **Asignar**.
4. Repite el proceso para añadir más asignaciones. El dispositivo puede asignarse a varios destinos a la vez.

Para desasignar, haz clic en **Desasignar** en una fila. Los eventos futuros del dispositivo dejan de anclarse a ese destino; los eventos ya registrados conservan sus anclajes.

## Asignar un dispositivo (GraphQL) {#assign-a-device-graphql}

Una asignación es un borde (edge) de relación del tipo reservado **`assigned`**. Se trata de un tipo rastreado incorporado, que se aprovisiona automáticamente por inquilino en su primer uso. Crea uno con la mutación masiva, direccionando el origen y el destino por `(type, token)`:

```graphql
mutation {
  createEntityRelationships(requests: [{
    token: "3f1c…",            # un token de borde único y nuevo (p. ej., un UUID)
    sourceType: "device",
    source: "sensor-001",       # token del dispositivo
    targetType: "customer",     # customer | area | asset
    target: "lucidworks",       # token de la entidad de destino
    relationshipType: "assigned"
  }]) { id token }
}
```

Para listar las asignaciones de un dispositivo, consulta sus bordes rastreados del tipo `assigned`:

```graphql
query {
  entityRelationships(criteria: {
    sourceType: "device", source: "sensor-001",
    relationshipType: "assigned", pageNumber: 1, pageSize: 100
  }) {
    results { id token targetType target { token } }
  }
}
```

Elimina una con `removeEntityRelationships(tokens: ["<edge token>"])`.

Crear y eliminar asignaciones requiere la autoridad `device:write`. Listarlas requiere `device:read`.

## Relación frente a asignación {#relationship-vs-assignment}

La asignación es un uso del grafo de relaciones general. Las mismas mutaciones `createEntityRelationships` y `removeEntityRelationships` respaldan la membresía de grupo (el tipo reservado no rastreado `member`) y cualquier tipo de relación personalizado que definas. Lo que convierte una relación en una asignación que ancla eventos es que su tipo sea **rastreado**. Consulta el [Modelo de dominio](../concepts/domain-model.md#relationships).
