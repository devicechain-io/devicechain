---
sidebar_position: 1
title: API de GraphQL
---

# API de GraphQL

Todo servicio de DeviceChain que expone una API externa lo hace a través de **GraphQL**.

:::note Estado
Los esquemas evolucionan mientras DeviceChain está en pre-release. Los archivos de esquema
publicados son la referencia autoritativa — **la introspección está deshabilitada por defecto**
(ver [Explorar el esquema](#explorar-el-esquema)).
:::

## Descargar los esquemas

Todos los esquemas se publican aquí, generados a partir de los archivos que los servicios
analizan al arrancar:

| | |
|---|---|
| **Índice** | [`/schema/index.json`](pathname:///schema/index.json) — cada área, su plano de autenticación, su endpoint y su archivo de esquema |
| **Esquemas** | `/schema/<area>.graphql`, más `-admin` y `-settings` para las dos áreas que sirven esos planos |

Empieza por el índice. Nombra el plano de autenticación en el que reside cada esquema, algo que
los propios archivos de esquema no dicen — y una mutación de administración ofrecida a quien
desarrolla sobre un inquilino es una llamada que nunca podrá autorizar.

Se sirven como texto plano con CORS permisivo, así que pueden obtenerse directamente:

```bash
curl -s https://docs.devicechain.io/schema/index.json | jq '.areas[] | {area, endpoint}'
curl -s https://docs.devicechain.io/schema/device-management.graphql
```

## Endpoints

El ingress enruta `/api/<area>/graphql` a cada servicio de área funcional, quitando el prefijo para
que llegue al `/graphql` propio de ese servicio. Así que todos los endpoints siguientes son
`https://<tu-host>/api/<area>/graphql`:

| Área | Cubre |
|---|---|
| `user-management` | autenticación — `login`, `selectTenant`, `refresh` — y la vista de gobernanza del propio inquilino |
| `device-management` | dispositivos, tipos de dispositivo, perfiles, activos, áreas, clientes, grupos, relaciones, alarmas, credenciales, autoría de reglas de detección |
| `event-management` | consultas de eventos de series temporales — `events`, `locationEvents`, `measurementEvents`, `alertEvents`, `bucketedMeasurements` |
| `device-state` | último estado conocido en vivo — `latestMeasurements`, `latestLocation`, `deviceStates` — más `demoteAssertedPresence`, que devuelve los dispositivos afirmados de una fuente de eventos a presencia inferida |
| `command-delivery` | envío de comandos — `createCommand`, `cancelCommand`, lotes para toda la flota (`createCommandBatch`, `cancelCommandBatch`), historial de comandos |
| `event-processing` | validación de reglas de detección, vista previa de reproducción, salud de reglas |
| `dashboard-management` | CRUD y versionado de paneles |
| `outbound-connectors` | CRUD de conectores de salida por inquilino |
| `notification-management` | canales y políticas de notificación |
| `ai-inference` | una única llamada, `inferRuleCandidate`, que respalda la puerta de autoría de reglas en lenguaje natural — presente solo cuando el servicio opcional de inferencia está habilitado |

Otros tres endpoints residen en un **plano de token de identidad** separado, no en el plano de
inquilino, y están autorizados para el superusuario o el operador:

| Endpoint | Cubre |
|---|---|
| `/api/user-management/admin/graphql` | la API de administración de instancia — directorio de identidades, membresías, catálogo de roles, registro de inquilinos y niveles |
| `/api/user-management/settings/graphql` | ajustes de instancia |
| `/api/ai-inference/admin/graphql` | proveedores de inferencia registrados por el operador |

La autorización en los servicios del plano de datos está **basada en capacidades**: cada resolver
verifica una autoridad específica (por ejemplo, `device:write`) que lleva el token de inquilino del
llamador. Ten en cuenta que algunas autoridades no coinciden con la intuición — leer credenciales
de dispositivo requiere `device:write`, no `device:read`, y `latestLocation` requiere
`location:read` mientras que sus hermanas en `device-state` requieren `state:read`.
`demoteAssertedPresence` requiere `state:demote`, que no es ninguna de las dos y no lo tiene ningún
rol de forma predeterminada: es lo único fuera del canal de eventos que escribe la proyección de
estado en vivo, y una sola llamada alcanza los dispositivos de una fuente de eventos entera.

`sparkplug-ingest` y `lwm2m-ingest` no sirven GraphQL en absoluto y se mantienen deliberadamente
fuera del router `/api`. `event-sources` sí está enrutado, pero responde con un esquema marcador
de posición — la ingesta llega a él por los transportes del plano de dispositivo, no por esta API.

## Consultar eventos

event-management expone consultas de lectura sobre el historial de eventos persistido. Cada una
toma un criterio de búsqueda — dispositivo, tipos de evento, un rango de tiempo de ocurrencia, un
anclaje de relación (`{type, token}`) y paginación — y devuelve resultados paginados:

```graphql
query {
  measurementEvents(criteria: {
    pageNumber: 1, pageSize: 50,
    deviceToken: "sensor-001",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    anchor: { type: "customer", token: "acme-corp" }
  }) {
    results { deviceToken occurredTime name value }
    pagination { totalRecords }
  }
}
```

Las entidades se nombran mediante **token** en todo momento, incluido dentro del anclaje. Ambos
límites de tiempo son inclusivos, filtran por `occurredTime` (el instante en que el dispositivo
reportó, no el instante en que la plataforma lo almacenó), y los resultados vuelven del más
reciente al más antiguo. La paginación empieza en 1.

**`measurementEvents` no filtra por nombre de medición.** El criterio no tiene un campo `name`, así
que "solo las lecturas de temperatura de este dispositivo" no es directamente expresable — filtra
del lado del cliente sobre `results[].name`, o usa `bucketedMeasurements`, que sí toma un `name` y
devuelve intervalos temporales:

```graphql
query {
  bucketedMeasurements(criteria: {
    deviceToken: "sensor-001",
    name: "temperature",
    startTime: "2026-06-01T00:00:00Z",
    endTime: "2026-06-24T00:00:00Z",
    intervalSeconds: 300
  }) { bucketStart name avg min max sum count }
}
```

:::caution A `bucketedMeasurements` le faltan las lecturas rellenadas con mucho retraso
Una lectura por intervalos cuyo `intervalSeconds` es un múltiplo entero de 60 y que no lleva filtro
de ancla se sirve desde una preagregación, no desde las lecturas en bruto. Esa preagregación se
mantiene al día sobre una **ventana móvil de 30 días**, y todo lo anterior se materializó una sola
vez, cuando se creó la base de datos.

Así que una lectura **escrita ahora pero fechada más de 30 días en el pasado** — por su propio
`occurredTime`, que controla el dispositivo — cae entre ambas: demasiado antigua para la ventana de
refresco, demasiado tardía para la pasada única. `measurementEvents` la devuelve y el historial en
bruto está completo; `bucketedMeasurements` no la muestra, y ningún error lo advierte.

El límite es cuánto **hacia atrás** está fechada la lectura, no la antigüedad de los datos. Una
lectura rellenada con una hora, un día o tres semanas de retraso se recoge en un minuto y no tiene
problema. Esto solo alcanza a un dispositivo que estuvo almacenando más de un mes, o a uno cuyo
reloj se desvía otro tanto. Los intervalos de menos de un minuto y las lecturas acotadas por ancla
se sirven desde las lecturas en bruto y no se ven afectados.
:::

Todas las consultas de eventos están **acotadas por inquilino automáticamente** — los resultados se limitan al inquilino del llamador, y una consulta sin un inquilino resuelto se rechaza.

## Explorar el esquema

**La introspección está deshabilitada por defecto.** Un despliegue de producción que no configura
nada no expone ninguna superficie de introspección, así que apuntar un cliente de GraphQL a un
endpoint esperando que se autodocumente no funcionará — la consulta de introspección se rechaza.

Eso deja dos formas de leer el esquema.

**Los archivos de esquema publicados**, listados en [Descargar los
esquemas](#descargar-los-esquemas) más arriba. Esta es la vía confiable porque no necesita una
instancia en ejecución ni un token — lo que más importa cuando todavía estás evaluando
DeviceChain. Se generan desde `backend/services/<area>/graphql/` en cada build de la
documentación, así que no pueden divergir de los esquemas que los servicios analizan. (Las
fuentes versionadas también están ahí, si prefieres leerlas en su sitio; ten en cuenta que los
nombres de archivo no son uniformes: la mayoría de las áreas usan `schema.graphql`, pero
`user-management` usa `schema.gql`, `admin_schema.gql` y `settings_schema.gql`.)

**Introspección en una instancia de desarrollo.** Configura `DC_GRAPHQL_DEV_TOOLS=true` en el
servicio para habilitarla. Hazlo solo en una instancia de desarrollo; está deshabilitada por
defecto de forma deliberada. Cualquier valor que no se interprete como booleano se trata como
deshabilitado en lugar de adivinarse. Con ella habilitada, la consulta habitual funciona:

```graphql
query {
  __schema {
    types { name kind }
  }
}
```

Habilitar las herramientas de desarrollo sirve además un **explorador GraphiQL** en `/graphiql` en
cada servicio — a través del ingress eso es `/api/<área>/graphiql`, y en un port-forward directo
contra el pod es `/graphiql`. Antes de la `v0.12.0` la página cargaba y luego fallaba en cada
consulta que enviaba, porque apuntaba a una ruta que ningún servicio sirve; ahora envía al punto
final por el que se llegó a ella, así que funciona en las tres rutas (ingress, port-forward y el
proxy de desarrollo de la consola).

## Convenciones

- Las entidades se direccionan mediante un **token** legible por humanos, además de un id interno.
- Las consultas de listado toman una entrada de criterio de búsqueda con paginación.
- Las mutaciones siguen un patrón de nomenclatura `create* / update* / delete*`.

### Una actualización reemplaza el registro completo {#an-update-replaces-the-whole-record}

**El reemplazo completo es el comportamiento por defecto, y es un valor por defecto, no una
garantía** — varios campos se comportan de otra manera, y uno de ellos se comporta exactamente al
revés. Las excepciones están enumeradas [más abajo](#where-the-default-does-not-hold); lee esa tabla
antes de automatizar nada.

Donde rige el comportamiento por defecto, una `update*` toma **la misma entrada que su hermana
`create*`** — o, en el plano de administración, un `*UpdateRequest` propio que omite el token y por
lo demás significa lo mismo — y significa lo que eso implica: cada campo que envías se escribe, y
**cada campo que omites se borra**. La mutación devuelve la entidad y tiene éxito, así que un campo
que no querías limpiar desaparece sin nada que lo indique.

```graphql
# Renombrar un dispositivo así TAMBIÉN borra su externalId y sus metadatos.
# La entrada exige deviceTypeToken, así que un reemplazo completo tiene que reenviar
# el tipo ACTUAL del dispositivo; nombrar otro distinto lo recategoriza.
mutation {
  updateDevice(token: "sensor-001", request: {
    token: "sensor-001",
    deviceTypeToken: "cold-store-probe",
    name: "Sonda de cámara fría"
  }) { token }
}
```

Lee la entidad primero, cambia lo que quieras cambiar y envíala entera de vuelta:

```graphql
query {
  devicesByToken(tokens: ["sensor-001"]) {
    token name externalId metadata
    deviceType { token }
  }
}
```

Dos consecuencias que conviene prever. Como la escritura cubre todos los campos, **dos personas que
editan una misma entidad se sobrescriben mutuamente en todo el registro**, no solo donde coinciden
— salvo en `updateDashboard`, `updateConnector` y `updateAiProvider`, que toman un
`expectedUpdatedAt` opcional y rechazan la escritura si la marca de tiempo almacenada se ha movido
desde que leíste. Y como la entrada de actualización es la de creación, lleva el **token** — en la
mayoría de las entidades el resolutor localiza la fila por el token de la solicitud, así que no es
un canal de renombrado, y donde el token de una entidad es realmente fijo el servidor lo rechaza en
lugar de moverlo (una geocerca siempre; un perfil de dispositivo en cuanto está publicado o
adoptado por un tipo de dispositivo).

### Dónde no rige el comportamiento por defecto {#where-the-default-does-not-hold}

Todas las excepciones de la API que sirve esta versión. Cualquier cosa que no esté aquí borra un
campo omitido.

| Campo | Qué pasa al omitirlo |
| --- | --- |
| Todos los campos de `updateDeviceType` | **Se conserva.** La mutación entera es una actualización parcial — ver [más abajo](#updatedevicetype-is-a-partial-update) |
| `secret` en `updateNotificationChannel`, `updateConnector`, `updateAiProvider` | **Se conserva** — y una cadena vacía lo *borra*. Lo inverso de `updateDeviceType`; ver la advertencia de abajo |
| `config` en `updateTenantTier` | **Se conserva.** Limpiar la configuración de un nivel re-tarifica a todos sus inquilinos, así que no se llega ahí por omisión — envía `{}` para limpiarla |
| `selector` en `updateEntityGroup` | **Se conserva.** Un selector omitido *o vacío* deja en su sitio el que ya estaba compilado |
| `firstName` / `lastName` en `updateProfile` | **Se conserva.** Es la única `update*` con argumentos sueltos en vez de un `request`; una cadena vacía sí lo limpia |
| `credentialType` en `updateProvisioningProfile` | **Se restablece a `ACCESS_TOKEN`** — ni se conserva ni se limpia |
| `activeVersion` de un perfil de dispositivo o de un grupo de entidades | Nada: aquí no es escribible en absoluto, y solo se mueve al publicar y al revertir |
| `memberType` / `membershipMode` en `updateEntityGroup` | Nada al omitirlos, pero *enviar un valor distinto* se rechaza — ambos son inmutables |
| Las [anulaciones de gobernanza](../concepts/governance.md) de un inquilino en `updateTenant` | Se borran — y borrarlas aquí significa **heredar el valor por defecto de la plataforma**, nunca «ilimitado» |

:::danger Una cadena vacía no es una forma segura de decir «no toques esto»
Para los tres campos `secret` de solo escritura, **null conserva y `""` borra** — exactamente lo
inverso de `updateDeviceType`, donde null limpia. No puedes leer un secreto de vuelta, así que no
hay nada que reenviar; la respuesta de la API es que omitirlo lo conserva.

Esto importa porque el consejo de arriba — lee la entidad y envíala entera de vuelta — te empuja a
rellenar todos los campos. Hacerlo con un secreto que no querías tocar, enviando `secret: ""`,
borra la credencial almacenada y la mutación devuelve éxito. Un conector sin credencial empieza a
fallar la autenticación en cada envío saliente. **Deja el campo fuera.**
:::

:::note
Las actualizaciones parciales están llegando área por área, no todas a la vez. `updateDeviceType` es
la primera mutación convertida; el resto siguen siendo reemplazos completos, y la intención es
convertirlas antes de la 1.0.
:::

### `updateDeviceType` es una actualización parcial {#updatedevicetype-is-a-partial-update}

`updateDeviceType` no toma la entrada de creación. Toma su propio `DeviceTypeUpdateRequest`, en el
que cada campo es opcional en tres sentidos, no en dos:

| Qué envías para un campo | Qué le pasa al valor almacenado |
| --- | --- |
| Nada — el campo está ausente | Se deja intacto |
| Un `null` explícito | Se limpia |
| Un valor | Se asigna ese valor |

Así que un renombrado es solo un renombrado:

```graphql
# Cambia el nombre. La descripción, el icono, los colores, el fabricante, el modelo,
# los metadatos y el perfil adoptado quedan exactamente como estaban.
mutation {
  updateDeviceType(token: "cold-store-probe", request: { name: "Sonda de cámara fría" }) {
    token
    name
  }
}
```

`profileToken` es el campo donde más importa. Un tipo de dispositivo referencia como mucho un
[perfil de dispositivo](../concepts/domain-model.md), y cada dispositivo de ese tipo resuelve sus
comandos, sus métricas y su declaración de posición a través de ese perfil. Con la antigua forma de
reemplazo completo, omitir `profileToken` al renombrar un tipo **desvinculaba el perfil** — lo que
dejaba de declarar silenciosamente la posición de todos los dispositivos construidos sobre ese
tipo, con éxito, devolviendo el tipo renombrado y sin nada que lo indicara. Ahora omitirlo conserva
el perfil actual. Desvincularlo es algo que tienes que decir:

```graphql
mutation {
  updateDeviceType(token: "cold-store-probe", request: { profileToken: null }) {
    token
    profile { token }
  }
}
```

Un `profileToken` vacío o solo con espacios también lo desvincula. Uno desconocido se rechaza y toda
la actualización se anula antes de escribir nada.

Dos consecuencias más de que la entrada sea su propio tipo y no la de creación:

- **No hay campo `token`**, así que una actualización no puede mover el token de un tipo de
  dispositivo — no es que se niegue, es que no puede expresarlo. El token ya es el argumento de la
  propia mutación, y llevarlo otra vez en la carga útil solo creaba una segunda fuente discrepante
  para la misma identidad.
- **`metadata` se sigue reemplazando por completo** cuando lo envías, y se limpia con null. En el
  esquema es una cadena JSON opaca, no un mapa, así que no hay ninguna fusión por clave que elegir
  — la API nunca ha podido direccionar una clave individual.

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

### Qué puede contener un token {#what-a-token-may-contain}

Todo token de entidad — y todo id de inquilino — debe coincidir con:

```
^[A-Za-z0-9][A-Za-z0-9_-]*$
```

Letras (de cualquier caja), dígitos, guiones y guiones bajos, empezando por una letra o un dígito, y
un máximo de **128 caracteres**. Cualquier otra cosa se rechaza al escribir, tanto al crear como al
actualizar, antes de almacenar nada.

Es una regla de seguridad más que un estilo de la casa, y por eso es así de estrecha. Un token se
inserta en espacios de nombres de infraestructura: un id de inquilino pasa a ser un segmento de un
asunto de NATS que se recupera partiendo por `.`, y un token de dispositivo pasa a ser un segmento
de un tópico MQTT. Así que un `.` desplaza los segmentos del asunto, y `*`, `>`, `+` y `#` inyectan
comodines que coinciden **entre inquilinos**. Las mayúsculas se permiten deliberadamente, porque los
identificadores que emiten las máquinas — números de serie, VIN — suelen ir en mayúsculas.

Los identificadores a los que un integrador recurre primero son precisamente los que esto rechaza:
`sensor.001`, una dirección MAC `AA:BB:CC:DD:EE:FF`, `plant/line-2`, cualquier cosa con un espacio.
**Ponlos en `externalId`**, que es opaco, no tiene restricciones de formato y es único dentro de un
inquilino cuando está presente. Dale a la entidad un token que elijas tú y conserva junto a él el
identificador propio del dispositivo.

La consola acuña los tokens por ti a partir de una plantilla por tipo de entidad, así que allí esto
rara vez aparece; es en la API y en el aprovisionamiento por script donde muerde primero.

Se generarán páginas de referencia detalladas por tipo a partir de los esquemas a medida que se estabilicen.
