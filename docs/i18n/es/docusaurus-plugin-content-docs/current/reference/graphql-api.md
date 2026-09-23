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
fuentes versionadas también están ahí, si prefieres leerlas en su sitio. Todas son archivos
`.graphql`, nombrados según el endpoint que las sirve: `schema.graphql` para la API de inquilino,
más `admin_schema.graphql` y `settings_schema.graphql` en las áreas que además sirven una API con
token de identidad.)

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

### Cuánto del registro escribe una actualización {#an-update-replaces-the-whole-record}

**Toda mutación `update*` es una actualización parcial, y solo hay un contrato.** Cada una toma un
`*UpdateRequest` propio — nunca la entrada de su hermana `create*` — y cada una distingue tres
estados en lugar de dos. Los **campos** concretos sí pueden desviarse: una referencia obligatoria
que se niega a limpiarse, un secreto de solo escritura, un campo que no está en la entrada de
actualización en absoluto. Están enumerados [más abajo](#where-the-default-does-not-hold), y esa
tabla es la lista completa. Léela antes de automatizar nada.

Los tres estados:

| Qué envías para un campo | Qué le ocurre al valor almacenado |
| --- | --- |
| Nada — el campo está ausente | Se deja tal cual |
| Un `null` explícito | Se limpia |
| Un valor | Se establece a ese valor |

Así, un renombrado es solo un renombrado:

```graphql
# Cambia el nombre. La descripción, el externalId, los metadatos y el tipo del
# dispositivo quedan exactamente como estaban, porque no se menciona ninguno.
mutation {
  updateDevice(token: "sensor-001", request: { name: "Sonda de cámara fría" }) {
    token
    name
  }
}
```

**Envía solo lo que quieras cambiar.** Leer el registro primero y reenviarlo entero es el hábito que
enseña una API de reemplazo completo, y aquí es el equivocado: da más trabajo, amplía la ventana en
la que pisas una edición concurrente y, en un campo `secret` de solo escritura, es directamente
destructivo — consulta [el aviso de más abajo](#where-the-default-does-not-hold).

La concurrencia es lo único que una actualización parcial reduce sin eliminar: dos escritores que
tocan campos distintos ya no se pisan, pero dos que tocan el mismo campo sí. `updateDashboard`,
`updateConnector` y `updateAiProvider` aceptan un `expectedUpdatedAt` opcional y rechazan la
escritura si la marca de tiempo almacenada se ha movido desde que la leíste. Envía el `updatedAt`
que leíste por última vez; omítelo para que gane la última escritura.

#### El argumento `token` nombra el registro {#the-token-argument-names-the-record}

Toda `update*` nombra el registro mediante un **argumento**, nunca mediante la petición, y **ese
argumento es lo que decide qué registro se escribe.** En todas salvo dos ese argumento es
`token: String!`; `updateOauthClient` toma `clientId: String!` en su lugar, y `updateProfile` no toma
ningún localizador, porque el registro que edita es la identidad con la sesión iniciada. Lo que hace
el token de la *petición* — donde todavía existe uno — depende de la mutación, y la diferencia es
real, así que se enumera en lugar de disimularse.

Antes había una tercera respuesta: un token de la petición que debía **coincidir** con el argumento,
rechazado cuando no coincidía y leído como «sin especificar» cuando venía vacío. Sus dos últimas
mutaciones se han convertido, así que la fila que la nombraba desaparece en lugar de quedarse vacía.

| El token de la petición | Qué mutaciones | Un token que **no coincide** | Un token **vacío** |
| --- | --- | --- | --- |
| **No existe** | toda [actualización parcial](#which-mutations-are-partial-updates) | *no representable* — la entrada no tiene campo `token`, así que el esquema lo rechaza | — |

Había una segunda: un token de petición que **nombraba el nuevo token del registro**, que es como
se renombraba un perfil, un conector, un proveedor y un canal de notificación. Las cuatro tienen ya
una [mutación de renombrado propia](#renaming-a-record), así que esa fila también ha desaparecido —
y con ella la última entrada de actualización de la plataforma que llevaba un token. La única fila
de arriba es ahora la respuesta completa.

#### Renombrar un registro {#renaming-a-record}

Cuatro registros se renombraban de la misma manera: enviando un token distinto dentro de la
petición de una actualización de reemplazo completo. Cada uno tiene ahora **su propia mutación**,
donde el nuevo token solo puede significar una cosa:

```graphql
renameDeviceProfile(token: String!, newToken: String!): DeviceProfile!
renameConnector(token: String!, newToken: String!): Connector!
renameAiProvider(token: String!, newToken: String!): AiProvider!
renameNotificationChannel(token: String!, newToken: String!): NotificationChannel!
```

Las cuatro siguen un mismo contrato. Un `newToken` **en blanco** — vacío o solo espacios — se
rechaza, porque dejaría un registro vivo sin nada que lo nombre. Renombrar un registro al token que
**ya tiene** es un éxito idempotente que devuelve el registro, así que reintentar tras un fallo
parcial es seguro. Un token que **ya tiene otro registro de esa clase** se rechaza por su nombre,
en lugar de aflorar como una violación de restricción. Y la autoridad es la misma que exige la
actualización correspondiente: renombrar es editar el registro, no un acto de otra naturaleza.

Cada uno de estos renombrados siempre fue intencionado, porque lo que depende del registro se
indexa por su id interno y no por su token: el secreto de entrega de un canal y el id de canal que
guardan las reglas de una política, la credencial de un conector, la clave de API de un proveedor
junto con sus concesiones por nivel y la asignación de modelo de cada inquilino. Un renombrado no
deja huérfano a ninguno.

Dos cosas sí se mueven con un renombrado, y conviene revisarlas antes de lanzar uno. Una regla
REACT nombra su conector **por token**, así que las reglas que apunten a un conector renombrado hay
que reapuntarlas. Y `renameDeviceProfile` rechaza el renombrado por completo una vez que el perfil
ha sido **publicado o adoptado** por un tipo de dispositivo, porque a partir de ahí las reglas
publicadas y los inventarios de dispositivos lo nombran por token.

`updateNotificationPolicy` no necesitó tal mutación: nada se indexa por el token de una política,
así que una política se mueve creando la nueva y borrando la antigua.

**El token de un geocerco es inmutable, y ahora la regla vive en la primera fila.**
`updateGeoFence` reconciliaba dos tokens y rechazaba una discrepancia; su entrada ya no lleva
ninguno, así que no existe petición que pida un renombrado. El motivo no ha cambiado: las reglas de
detección nombran los geocercos por token dentro de expresiones compiladas que este servicio no
puede reescribir, así que un renombrado dejaría a todas ellas nombrando nada mientras la mutación
devuelve éxito. Si necesitas un geocerco con otro token, **crea primero el nuevo y borra después el
antiguo** — hacerlo al revés puede hacerte perder el margen de posiciones que tienes heredado y
dejar el geocerco sin poder recrearse.

En lo que coinciden todas las filas es en que un token de petición ya no puede **dejar en blanco** un
registro, y nunca puede hacer que la mutación escriba un registro distinto del que nombra `token:`.

:::note[Esto ha cambiado]
Antes de esta versión el comportamiento no era ni uniforme ni seguro, y ambos fallos devolvían éxito.

La mayoría de las mutaciones `update*` localizaban el registro por el token de la **petición** e
ignoraban el argumento por completo, así que una petición que nombraba una entidad en `token:` y otra
en `request.token` actualizaba en silencio la segunda y la devolvía. Las demás respetaban el
argumento pero luego escribían el token de la petición sobre el almacenado — así que la petición
seguía moviendo el registro, y un token de petición **vacío**, que `token: String!` permite (`""` es
una cadena no nula perfectamente válida), dejaba en blanco el token del registro y una fila viva sin
forma de direccionarla.

Si tienes un cliente que dependía de que la petición nombrase el registro, ahora recibe un error en
lugar de escribir la fila equivocada. Si tienes uno que envía `token: ""` en una actualización, ahora
recibe un error en ambos casos — rechazado por las mutaciones de renombrado, y rechazado por el
esquema en una actualización parcial, cuya entrada no tiene campo `token` donde enviarlo — donde
antes destruía la identidad del registro. Antes se *ignoraba* en un tercer grupo de mutaciones, que
es lo que la regla «debe coincidir» hacía con un token vacío; todas se han convertido.
:::

### Dónde no rige el comportamiento por defecto {#where-the-default-does-not-hold}

Todas las excepciones a nivel de campo que sirve esta versión. Lo que no se nombre aquí sigue los
tres estados de arriba: ausente lo deja tal cual, `null` lo limpia, un valor lo establece.

| Campo | Qué ocurre al omitirlo |
| --- | --- |
| `secret` en `updateNotificationChannel`, `updateConnector`, `updateAiProvider` | **Se conserva.** Un valor lo rota; `null` — o una cadena vacía — lo borra. No puedes leer un secreto de vuelta, así que omitirlo es como se dice «deja la credencial como está» |
| `config` en `updateTenantTier` | **Se conserva.** Limpiar los ajustes de un nivel recalcula el precio de cada inquilino en él, así que no se alcanza por omisión — envía `null` o `{}` para limpiarlo |
| `selector` en `updateEntityGroup` | **Se conserva** al omitirlo. A diferencia de la mayoría de campos de una actualización parcial, no se puede *limpiar*: `null` se rechaza, porque un grupo dinámico sin selector no coincide con nada y no se puede reparar. A un grupo estático se le rechaza un selector sin más |
| `definition` en `updateDashboard` | **Se conserva** al omitirlo, que es como se renombra un panel sin reenviar su documento. Igual que `selector` arriba, no puede *limpiarse*: un `null` se rechaza, porque un panel sin definición no es nada. Una definición malformada rechaza la actualización completa, así que un renombrado enviado con ella tampoco se aplica |
| `firstName` / `lastName` en `updateProfile` | **Se conservan.** Una cadena vacía limpia, y `null` significa lo mismo: son las columnas del nombre visible, donde «vacío» es un valor que una persona puede tener legítimamente y no una ausencia |
| `credentialType` en `updateProvisioningProfile` | **No está en la entrada de actualización.** Hoy el aprovisionamiento solo puede emitir un tipo de credencial, así que el campo únicamente repetiría lo almacenado. Antes cualquier actualización que lo omitiera lo *restablecía* a `ACCESS_TOKEN` |
| `activeVersion` en un perfil de dispositivo o un grupo de entidades | Nada: aquí no es escribible en absoluto, y solo se mueve con publicar y revertir |
| `memberType` / `membershipMode` en `updateEntityGroup` | **No están en la entrada de actualización.** Ambos son identidad, así que un cambio no es representable en lugar de rechazarse |
| Las [anulaciones de gobernanza](../concepts/governance.md) de un inquilino en `updateTenant` | **Se conservan.** Enviar `null` elimina la anulación, lo que significa **heredar el nivel y luego el valor por defecto de la plataforma**: nunca cero, y nunca «ilimitado» |

:::danger Una cadena vacía no es una forma segura de decir «deja esto como está»
En todos los campos `secret` de solo escritura, **`""` borra la credencial almacenada.** No puedes
leer un secreto de vuelta, así que no hay nada que reenviar; la respuesta de la API es que omitirlo
lo conserva.

Esto importa porque «leer el registro, cambiar una cosa y reenviarlo todo» es el hábito que enseña
una API de reemplazo completo, y los clientes escritos contra una lo siguen haciendo. Rellenar todos
los campos significa enviar `secret: ""` para una credencial que nunca quisiste tocar — lo que la
borra, y la mutación devuelve éxito. Un conector sin credencial empieza a fallar la autenticación en
cada envío saliente. **Deja el campo fuera.**

`null` también borra la credencial. Eso no es una excepción sino el significado habitual de un null
en la plataforma: un null limpia el campo que nombra. La **inversión** que estos campos llevaban —
donde null conservaba y solo `""` borraba — ha desaparecido.
:::

### Qué mutaciones son actualizaciones parciales {#which-mutations-are-partial-updates}

**Todas.** La conversión llegó por áreas y ya está completa, así que esta sección deja de ser un
listado de qué mutaciones son seguras: es el registro de lo que cambió en cada área, conservado
porque un cliente escrito contra el comportamiento antiguo necesita saberlo. En device-management,
**todas las `update*`** toman un `*UpdateRequest` propio:

`updateDeviceType` · `updateDevice` · `updateAssetType` · `updateAsset` · `updateCustomerType` ·
`updateCustomer` · `updateAreaType` · `updateArea` · `updateMetricDefinition` ·
`updateCommandDefinition` · `updateDetectionRule` · `updateGeoFence` · `updateEntityGroup` ·
`updateDeviceCredential` · `updateProvisioningProfile` · `updateEntityRelationshipType` ·
`updateDeviceProfile`

Los conectores salientes y la inferencia de IA han convertido su única actualización cada uno:
`updateConnector` y `updateAiProvider`. Todos los canales de renombrado que llevaban los tokens de
petición de esas áreas se han trasladado a una [mutación de renombrado
propia](#renaming-a-record) en lugar de eliminarse.

En notification-management se han convertido **ambas** mutaciones `update*`:
`updateNotificationChannel` y `updateNotificationPolicy`. Dos cosas de la política conviene saberlas
antes de enviar una:

- **`rules` es opcional, y omitirlo deja ahora el conjunto de reglas exactamente como está** — las
  mismas filas, no una copia reconstruida. Antes era obligatorio y cada actualización reemplazaba el
  conjunto entero, así que una edición que solo cambiaba un nombre destruía y recreaba cada regla; y
  una edición que dejaba `rules` fuera vaciaba la política y devolvía éxito. El reemplazo completo
  sigue disponible: envía la lista. Enviar `null` **o** `[]` vacía el conjunto de reglas — para una
  lista son la misma petición escrita de dos formas.
- **`deviceTypeToken` no está en la entrada de actualización.** Un valor no vacío se rechaza al
  escribir (el despachador omite una política acotada a un tipo de dispositivo, así que aceptarla
  devolvería éxito sobre una política que no entrega nada), lo que dejaba al campo sin ninguna
  petición aceptable más allá de una operación nula. Sigue en la entrada de creación, donde el
  rechazo se explica solo.

En dashboard-management, **`updateDashboard`** toma un `DashboardUpdateRequest` y no lleva token
alguno. Su única particularidad es `definition`: el campo es anulable para poder *omitirse* — así se
renombra un panel sin reenviar su documento entero —, pero un `null` explícito sobre él se
**rechaza**, porque un panel sin definición no es nada. Conserva su precondición opcional
`expectedUpdatedAt`, y una actualización que no nombre ningún campo no escribe nada (ni siquiera
`updatedAt`), aunque una precondición obsoleta sobre ella sigue siendo un conflicto.

En user-management, **todas las `update*`** toman ya una petición propia:

`updateRole` · `updateTenant` · `updateTenantTier` · `updateOauthClient` · `updateProfile`

Los conectores salientes y la inferencia de IA han convertido su única actualización cada uno:
**`updateConnector`** y **`updateAiProvider`**. Ambas conservan un `expectedUpdatedAt` opcional, y
en ellas `type`/`config` y `kind`/`endpoint` respectivamente se validan como un **par** contra los
valores que el registro va a tener — así que nombrar uno del par vuelve a comprobar el otro
almacenado, y un cambio que dejaría el registro inutilizable se rechaza al escribir y no en el
primer uso.

**Esa es toda la superficie de actualización.** Ninguna mutación `update*` toma en ningún sitio la
entrada de su hermana `create*`, así que no queda ninguna que el lector deba contrastar con una
lista — y esta página ya no lleva ninguna. Versiones anteriores sí, dos veces: primero un listado de
las áreas sin convertir (equivocado cada vez que aterrizaba una) y después una regla que decía que
la firma era la autoridad porque coexistían dos contratos. Ambas se escribieron para no desviarse y
ambas se desviaron, del mismo modo: su premisa caducó. Lo que las sustituye son los
[tres estados](#an-update-replaces-the-whole-record) y las
[excepciones a nivel de campo](#where-the-default-does-not-hold), que son una afirmación sobre toda
la API en lugar de una partición de ella.

:::caution[Consulta el esquema para saber qué declara cada entrada]
Un solo contrato no significa que toda entrada acepte todo campo. Lo que una actualización *puede*
expresar es lo que declara su `*UpdateRequest`, y algunos campos están ausentes a propósito —
`deviceTypeToken` en `updateNotificationPolicy`, `memberType` en `updateEntityGroup`,
`credentialType` en `updateProvisioningProfile` — porque ninguna petición para ellos se aceptaría.
Otros aceptan un valor pero rechazan un `null`.

El [esquema que descargaste](#descargar-los-esquemas) es la autoridad para lo primero; la
[tabla de excepciones](#where-the-default-does-not-hold) lo es para lo segundo. Ninguna de las dos
es una pregunta sobre qué contrato rige la mutación, porque solo hay uno.
:::

:::note[Esto cambió en user-management]
`updateRole`, `updateTenant`, `updateTenantTier` y `updateOauthClient` escribían todos los campos que
declaraba su entrada, así que una petición que solo nombraba `name` dejaba el resto en blanco y
devolvía el registro vaciado. `updateTenant` es la primera que conviene revisar: omitir una anulación
de gobernanza la **borraba**, así que renombrar un inquilino eliminaba todos los techos que un
operador hubiera fijado. Ahora omitir una la deja tal cual, y solo un `null` explícito la elimina.

`tierToken` en `updateTenant` pasó a ser **opcional**. Omitirlo mantiene al inquilino en su nivel
actual; un `null` explícito se rechaza, porque todo inquilino tiene un nivel.

`authorities`, `redirectUris` y `scopes` pasaron a ser **listas anulables** (`[String!]`, no
`[String!]!`), así que ya tienen estado ausente. Omitir una la deja tal cual; enviar una lista la
sustituye entera; `null` y `[]` significan ambos «vacía». Las autoridades de un rol **sí** se pueden
vaciar, porque un rol que no concede nada es algo que puedes crear. Las URI de redirección y los
ámbitos de un cliente OAuth **no** — una lista de redirección vacía no coincide con nada, así que el
cliente jamás podría completar una autorización.

`updateProfile` toma ahora `request: ProfileUpdateRequest!` en lugar de argumentos sueltos
`firstName` / `lastName`. Su comportamiento no cambia: escribe solo los nombres que envías, y `""`
sigue limpiando uno.
:::

#### Campos que conviene conocer en las mutaciones convertidas {#two-fields-on-converted-mutations}

- **Una referencia obligatoria no se puede limpiar.** El `assetTypeToken` de `updateAsset`, y sus
  equivalentes en dispositivos, clientes y áreas, reapuntan la entidad cuando envías uno y la dejan
  tal cual cuando no — pero un `null` explícito se **rechaza**, porque «sin tipo» no es un estado en
  el que esas entidades puedan estar. Un token desconocido también se rechaza, y el rechazo es
  total: no se escribe nada.
- **El `profileToken` de `updateDeviceType` es la única referencia que *sí* se puede limpiar**,
  porque un tipo de dispositivo sin [perfil de dispositivo](../concepts/domain-model.md) es algo
  real. Bajo la antigua forma de reemplazo completo, omitirlo al renombrar un tipo **desvinculaba el
  perfil** — lo que dejaba sin declarar la posición de cada dispositivo construido sobre ese tipo,
  con éxito. Omitirlo ahora conserva el perfil actual; `null`, o un token vacío, lo desvincula.

- **Un campo obligatorio tampoco se puede limpiar, aunque no sea una referencia.** El `dataType` de
  una métrica, el `credentialType` y el `enabled` de una credencial, la `definition` y el `enabled`
  de una regla, la `geometry` de un geocerco, la `provisionKey` y el `provisionSecret` de un perfil
  de aprovisionamiento: envía un valor para cambiarlo, omítelo para dejarlo tal cual, y un `null`
  explícito se **rechaza**. Merece mencionarse aparte del caso de las referencias porque el fallo
  que evita es invisible: convertir `enabled: null` en `false` desactivaría una credencial o
  aparcaría una regla y devolvería éxito, y `false` es un valor que podrías haber enviado a
  propósito.
- **Omitir un secreto ahora lo conserva.** El `credentialValue` de `updateDeviceCredential` y el
  `provisionSecret` de `updateProvisioningProfile` quedaban en blanco con cualquier actualización
  que no los repitiera — lo que dejaba sin conexión a un dispositivo, o a toda una flota que se
  autorregistra, en su siguiente conexión, con un `200` en la edición que lo rompió.

`metadata` se reemplaza por completo en ambos contratos cuando lo envías, y se limpia con `null` en
una actualización parcial. Es una cadena JSON opaca en el esquema, no un mapa, así que no hay
fusión clave a clave que elegir — la API nunca ha podido direccionar una clave individual.

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

## Límites de las solicitudes {#request-limits}

Todos los endpoints de GraphQL rechazan una solicitud demasiado grande o que hace demasiado trabajo,
antes de ejecutar nada de ella. Los límites son los mismos en todos los servicios, y cada uno puede
cambiarse por servicio con la variable de entorno indicada. Un valor ausente, que no es un número o
menor que 1 vuelve al valor por defecto: ninguno puede desactivarse.

| Límite | Por defecto | Variable | Qué se rechaza |
| --- | --- | --- | --- |
| Cuerpo de la solicitud | 4 MiB | `DC_GRAPHQL_MAX_BODY_BYTES` | Todo el cuerpo HTTP, variables incluidas. Se responde con HTTP 400. |
| Longitud de la consulta | 100.000 bytes | `DC_GRAPHQL_MAX_QUERY_LENGTH` | La cadena de la consulta en sí. |
| Profundidad de anidamiento | 15 | `DC_GRAPHQL_MAX_DEPTH` | Selecciones anidadas más allá de esta profundidad. |
| Campos raíz por consulta | 20 | `DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS` | Una operación de consulta que selecciona más campos de primer nivel que este número. |
| Campos raíz por mutación | 5 | `DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS` | Una operación de mutación que selecciona más campos de primer nivel que este número. |
| Comprobaciones de credenciales por petición | 1 | `DC_GRAPHQL_MAX_CREDENTIAL_CHECKS` | Las comprobaciones de contraseña posteriores a la primera en una misma petición (ver [más abajo](#credential-checks-per-request)). |

Salvo con el límite del cuerpo, una solicitud rechazada recibe HTTP 200 con una sola entrada en
`errors`, sin `data` y sin haber ejecutado nada. El rechazo por campos raíz lleva
`extensions.code` con el valor `TOO_MANY_ROOT_FIELDS`:

```json
{
  "errors": [{
    "message": "mutation (anonymous) selects 6 root fields; the maximum is 5",
    "extensions": { "code": "TOO_MANY_ROOT_FIELDS" }
  }]
}
```

**Los campos raíz se cuentan por clave de respuesta**, así que cada alias cuenta como un campo
propio, y los campos a los que se llega a través de un fragmento cuentan como si estuvieran escritos
directamente. Repetir la misma clave cuenta como un solo campo. `@skip` e `@include` no se evalúan,
así que un campo condicional cuenta tanto si se ejecuta como si no. Se cuentan todas las operaciones
del documento, no solo la que selecciona `operationName`, y la regla se aplica tanto por WebSocket
como por HTTP.

El límite de mutaciones es el estricto porque los campos de una mutación se ejecutan uno tras otro:
sin él, una sola solicitud podría llevar cientos de copias con alias de una mutación costosa. La
consola, la aplicación de paneles, los SDK, `dcctl` y el servidor MCP envían un solo campo de
mutación por solicitud y como mucho dos campos de consulta. El límite se aplica solo a los campos de primer nivel; los alias
de un campo anidado no se cuentan.

### Comprobaciones de credenciales por petición {#credential-checks-per-request}

En una petición se puede comprobar como mucho una contraseña, se escriba como se escriba. El primer
`login` de una petición se evalúa con normalidad. Cualquier otro `login` en la misma petición, como
otro alias, no se evalúa: no se comprueba la contraseña, no se busca nada y no se registra nada en
el registro de auditoría. Recibe su propio error en lugar de un veredicto sobre la contraseña:

```json
{
  "errors": [{
    "message": "this request has already made its credential checks; send one sign-in per request",
    "path": ["a2"],
    "extensions": { "code": "TOO_MANY_CREDENTIAL_CHECKS" }
  }]
}
```

No depende de cómo esté escrito el documento, así que se mantiene incluso para un documento que
supere el límite de campos raíz. El rechazo ocurre antes de mirar la dirección de correo, así que es
el mismo exista o no una cuenta. Todos los clientes que incluye DeviceChain envían un solo inicio de
sesión por petición, así que a ninguno le afecta. `DC_GRAPHQL_MAX_CREDENTIAL_CHECKS` aumenta el
número por servicio; como los demás límites, no se puede desactivar. Los rechazos se cuentan en
`devicechain_usermanagement_credential_checks_total` con `outcome="request_budget"`.

### Espera entre intentos de inicio de sesión {#sign-in-backoff}

Los inicios de sesión con contraseña fallidos hacen más lentos los siguientes intentos sobre la misma
dirección de correo. Esto se aplica a `login` y al formulario de inicio de sesión de OAuth.

Los cinco primeros intentos fallidos sobre una dirección se evalúan de inmediato. A partir de ahí, la
dirección espera 1 segundo antes de que se evalúe su siguiente intento, luego 2, luego 4,
duplicándose hasta 5 minutos. Un inicio de sesión correcto reinicia la cuenta, y también 10 minutos
sin actividad después del último intento evaluado.

La cuenta pertenece a la dirección escrita, exista o no una cuenta con ella, así que la espera no
revela qué direcciones están registradas. La comparten todas las réplicas del servicio.

:::warning Quien conozca una dirección puede dejar fuera a su dueño

La cuenta se lleva por dirección, no por dirección y ubicación de red, para que un atacante no
obtenga un margen nuevo repartiendo los intentos entre muchas máquinas. El precio es que cualquiera
que conozca una dirección de correo puede seguir enviando contraseñas incorrectas para ella. Mientras
lo haga, cada turno de evaluación es suyo, y al dueño se le rechaza por espera incluso con la
contraseña correcta. No es un bloqueo permanente: termina cuando el atacante se detiene, y tras una
espera de como mucho 5 minutos el dueño puede volver a iniciar sesión. La métrica
`devicechain_usermanagement_credential_checks_total`, con `outcome="throttled"`, muestra cuándo se
está reteniendo una cuenta de esta forma.

:::

**Los secretos de cliente OAuth no se ralentizan.** Los secretos de cliente creados por la API de
administración son 256 bits aleatorios, que ningún número de intentos encontrará, y un ID de cliente
es público: aparece en cada URL de autorización. Una espera sobre los secretos de cliente no los
protegería, y permitiría a cualquiera retener en la espera a un cliente confidencial, y con él cada
inicio de sesión que pasa por ese cliente. Un cliente que se siembre desde la configuración debe tener
un secreto igual de fuerte. Los clientes públicos no tienen secreto.

Un intento hecho durante la espera no se evalúa en absoluto: no se comprueba la contraseña y no se
registra nada en el registro de auditoría. Se informa como un error propio y no como una contraseña
incorrecta, porque la contraseña bien podría ser correcta:

```json
{
  "errors": [{
    "message": "too many failed sign-in attempts; try again in 8 seconds",
    "path": ["login"],
    "extensions": { "code": "THROTTLED", "retryAfterSeconds": 8 }
  }]
}
```

Si el servicio no puede llegar al almacén que guarda estas cuentas, se niega a comprobar contraseñas
en lugar de comprobarlas sin contar. El error de `login` lleva entonces `extensions.code` con el
valor `UNAVAILABLE`. Trátalo como una caída del servicio, no como una credencial rechazada. El
endpoint de tokens de OAuth no usa el almacén, así que la autenticación de clientes sigue
funcionando.

El almacén tiene un tamaño fijo, y cada dirección que se prueba ocupa un lugar en él durante 10
minutos, exista o no una cuenta con ella. Quien envíe inicios de sesión para suficientes direcciones
distintas puede llenarlo. Cuando está lleno, el inicio de sesión sigue funcionando: las contraseñas se
siguen comprobando y respondiendo con normalidad, pero los nuevos fallos no se cuentan, así que las
direcciones que no estaban ya esperando no se ralentizan hasta que caduquen entradas antiguas. Una
dirección que ya está esperando sigue esperando, pero solo hasta que termine esa espera, que dura
como mucho 5 minutos. A partir de ahí sus fallos tampoco se cuentan, así que una cuenta que esté
siendo atacada mientras el almacén está lleno no queda protegida por la espera. Es deliberado. Rechazar todos los inicios de sesión
permitiría a cualquiera que pueda llenar el almacén dejar fuera de la instancia a todos los usuarios.
Los intentos de adivinar siguen limitados por el tope de campos por petición y por el coste de cada
comprobación de contraseña.

Cada intento comprobado así se cuenta en `devicechain_usermanagement_credential_checks_total` con
`outcome="store_full"`. Si las reglas de alerta del chart están habilitadas, la alerta
`CredentialAttemptStoreFull` se dispara cuando hay alguno. El registro de user-management también muestra una advertencia, como mucho una vez por
minuto, mientras dure. Cuando se dispare la alerta, lo más probable es que alguien esté probando
muchas direcciones. Averigua de dónde viene el tráfico de inicio de sesión y bloquéalo antes de que
llegue. Si el tráfico es legítimo, aumenta `instance.config.infrastructure.nats.kvStateMaxBytes`.
Ese tamaño se aplica a todos los buckets de estado, así que comprueba que el volumen de JetStream
tiene espacio para el aumento.

Se generarán páginas de referencia detalladas por tipo a partir de los esquemas a medida que se estabilicen.
