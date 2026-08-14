---
sidebar_position: 4
title: Gestión de asignaciones de dispositivos
---

# Gestión de asignaciones de dispositivos

Una **asignación** relaciona un dispositivo con un **cliente**, **área** o **activo** para que su telemetría lleve contexto organizativo. En DeviceChain, una asignación es simplemente una **relación rastreada** sobre el grafo de entidades uniforme — no existe un registro de asignación independiente.

:::note Estado
Disponible. Las asignaciones se gestionan desde la pestaña **Assignment** de la página de detalle del dispositivo en la consola, o mediante la API GraphQL de device-management.
:::

## La asignación organiza; no restringe el acceso

Un dispositivo se autentica con una **credencial**; la asignación solo **organiza** sus datos. Ambos son independientes:

- Un dispositivo que está registrado y provisto de credencial **reporta telemetría de inmediato**, incluso sin asignación. Sus eventos se resuelven con un **anclaje nulo** — igualmente se persisten y actualizan el estado en vivo del dispositivo; simplemente aún no se atribuyen a un cliente/área/activo.
- **Asignar** el dispositivo posteriormente da a sus eventos subsiguientes un **anclaje**, de modo que consultas como "cada lectura del Edificio 7" los encuentran.

Por lo tanto, los dispositivos sin asignar nunca se descartan silenciosamente — un cambio respecto al comportamiento anterior.

## Cada asignación es un anclaje

Un dispositivo puede tener **varias** asignaciones a la vez — un cliente *y* un área *y* un activo. Cuando el dispositivo reporta un evento, **cada** asignación se registra como un **anclaje** en ese evento. Así, la misma lectura es consultable por **cada** dimensión: aparece tanto bajo el cliente *como* bajo el área. No existe una asignación "primaria" — todas las asignaciones son iguales.

Concretamente, los anclajes de cada evento viven en un conjunto hermano `event_anchors` (una fila por asignación), y una consulta filtrada por anclaje ("eventos del área Y") coincide con los eventos cuyo conjunto contiene ese anclaje. Los anclajes se capturan **en el momento de la escritura**, de modo que el historial es estable: un dispositivo que más tarde cambia de área conserva el área en la que estaba cada evento antiguo cuando ocurrió.

## Asignar un dispositivo (consola)

1. Abra la página de detalle del dispositivo y seleccione la pestaña **Assignment**.
2. Elija un **tipo de destino** (Customer / Area / Asset) y seleccione la entidad **destino**.
3. Haga clic en **Assign**. Repita para agregar más asignaciones — el dispositivo puede asignarse a varios destinos a la vez.

Para desasignar, haga clic en **Unassign** en una fila. Esto detiene el anclaje de los eventos *futuros* del dispositivo a ese destino; los eventos ya registrados conservan sus anclajes.

## Asignar un dispositivo (GraphQL)

Una asignación es un borde (edge) de relación del tipo reservado **`assigned`** (un tipo *rastreado* incorporado, aprovisionado automáticamente por inquilino en su primer uso). Cree uno con la mutación masiva, direccionando el origen y el destino por `(type, token)`:

```graphql
mutation {
  createEntityRelationships(requests: [{
    token: "3f1c…",            # a fresh unique edge token (e.g. a UUID)
    sourceType: "device",
    source: "sensor-001",       # device token
    targetType: "customer",     # customer | area | asset
    target: "lucidworks",       # target entity token
    relationshipType: "assigned"
  }]) { id token }
}
```

Liste las asignaciones de un dispositivo consultando sus bordes rastreados del tipo `assigned`:

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

Elimine una con `removeEntityRelationships(tokens: ["<edge token>"])`. Las tres operaciones requieren la autoridad `device:write` (listar requiere `device:read`).

## Relación frente a asignación

La asignación es un uso del grafo de relaciones general. Las mismas mutaciones `createEntityRelationships` / `removeEntityRelationships` respaldan la **membresía de grupo** (el tipo reservado no rastreado `member`) y cualquier tipo de relación personalizado que defina. Lo que hace que una relación sea una *asignación que ancla eventos* es simplemente que su tipo sea **rastreado**. Consulte el [Modelo de dominio](../concepts/domain-model.md#relationships).
