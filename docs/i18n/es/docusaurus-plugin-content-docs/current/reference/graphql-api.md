---
sidebar_position: 1
title: API de GraphQL
---

# API de GraphQL

Todo servicio de DeviceChain que expone una API externa lo hace a través de **GraphQL**. El esquema es introspectable, por lo que la API se autodocumenta — apunta cualquier cliente de GraphQL (GraphiQL, Apollo, Insomnia) al endpoint de un servicio para explorarlo.

:::note Estado
Los esquemas de device-management y event-management son los más desarrollados; user-management está en expansión. Los esquemas evolucionan mientras DeviceChain está en pre-release — trata el resultado de la introspección en vivo como autoritativo.
:::

## Endpoints

Cada servicio expone su propio endpoint de GraphQL (las rutas exactas dependen del entorno y se exponen a través del ingress configurado por tu despliegue):

| Servicio | Cubre |
|---|---|
| device-management | dispositivos, perfiles, activos, áreas, clientes, grupos, relaciones |
| event-management | consultas de eventos de series temporales — `events`, `locationEvents`, `measurementEvents`, `alertEvents` |
| user-management | autenticación — `login`, `selectTenant`, `refresh` |

`user-management` también expone una **API de administración de instancia** separada (un endpoint distinto, autenticado con un token de identidad y autorizado para el superusuario) que gestiona el directorio global de identidades, las membresías por inquilino, el catálogo de roles y el registro de inquilinos. La autorización en los servicios del plano de datos está **basada en capacidades**: cada resolver verifica una autoridad específica (por ejemplo, `device:write`) que lleva el token de inquilino del llamador.

## Consultar eventos

event-management expone consultas de lectura sobre el historial de eventos persistido. Cada una toma un criterio de búsqueda — dispositivo, tipos de evento, un rango de tiempo de ocurrencia, un anclaje de relación (`{type, id}`) y paginación — y devuelve resultados paginados:

```graphql
query {
  measurementEvents(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceId: "42",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    anchor: { type: "customer", id: "7" }
  }) {
    results { deviceId occurredTime name value }
    pagination { totalRecords }
  }
}
```

Todas las consultas de eventos están **acotadas por inquilino automáticamente** — los resultados se limitan al inquilino del llamador, y una consulta sin un inquilino resuelto se rechaza.

## Explorar el esquema

Dado que la API es introspectable, la referencia más confiable es el propio esquema:

```graphql
query {
  __schema {
    types { name kind }
  }
}
```

## Convenciones

- Las entidades se direccionan mediante un **token** legible por humanos, además de un id interno.
- Las consultas de listado toman una entrada de criterio de búsqueda con paginación.
- Las mutaciones siguen un patrón de nomenclatura `create* / update* / delete*`.

## Validación de entrada

**Un campo de entrada que el esquema no define se rechaza.** Enviar un campo no declarado
hace fallar toda la solicitud con un error que nombra el campo infractor, y sugiere el
campo declarado que probablemente quisiste decir:

```json
{
  "errors": [{
    "message": "Variable \"request\" has invalid value.\nField \"deviceProfileToken\" is not defined by type \"DeviceTypeCreateRequest\". Did you mean \"profileToken\"?"
  }]
}
```

Esto se cumple tanto si el valor se escribe como un literal en la consulta como si se suministra mediante
una variable.

Importa más que una simple verificación de errores tipográficos. Un campo descartado silenciosamente es indistinguible de uno
que sí se aplicó: la mutación devuelve éxito, y obtienes una entidad parcialmente configurada
sin nada que indique que faltó un valor. Rechazar es lo que hace que una respuesta de éxito
signifique que se entendió toda la entrada.

Se generarán páginas de referencia detalladas por tipo a partir de los esquemas a medida que se estabilicen.
