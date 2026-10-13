---
title: Actualizaciones over-the-air
---

# Actualizaciones over-the-air

El servicio opcional **`update-management`** entrega imágenes de firmware, software y configuración
a los dispositivos. Mantiene un catálogo de **artefactos** por inquilino, te permite **asignar** un
artefacto a un dispositivo, envía al dispositivo lo que necesita para obtener la imagen y sigue cada
**intento de actualización** hasta que el dispositivo demuestra que ejecuta la nueva versión.

:::note Estado
**Disponible hoy:** el catálogo de artefactos con una subida en streaming verificada, la asignación
a un dispositivo cada vez, una descarga reanudable mediante una concesión de corta duración y el
seguimiento de cada intento con plazos.

**Todavía no:** nada autentica **quién produjo** un artefacto (la plataforma solo comprueba que los
bytes son los que se declararon), así que asignar uno debe permitirse de forma explícita; no hay
despliegue por flota (asignación escalonada o por porcentaje a muchos dispositivos); y los
dispositivos que solo usan HTTP no pueden recibir actualizaciones, porque las asignaciones viajan
por el canal MQTT del dispositivo.
:::

## Activarlo {#enabling}

`update-management` no forma parte del perfil `default`. Instala con `--profile full`, o nómbralo
en `enabledFunctionalAreas`. Depende de `device-management` y guarda el contenido de los artefactos
en el [almacén de objetos](./object-storage.md): sin almacén configurado, declarar un artefacto se
rechaza con `OBJECT_STORE_NOT_CONFIGURED`. En el backend S3 una subida se almacena en memoria, así
que allí los artefactos están limitados a 64 MiB sea cual sea el límite configurado.

Dos ajustes son decisiones deliberadas, no valores por defecto que dejar tal cual. Ambos están bajo
`functionalAreas.update-management.config.updates` en los values del chart:

| Ajuste | Por defecto | Significado |
| --- | --- | --- |
| `allowUnverifiedArtifacts` | `false` | Hoy todo artefacto está sin verificar, así que con `false` **no se puede asignar ningún artefacto**. Ponlo a `true` en una instancia de desarrollo o de pruebas. El valor se muestra en la consulta `updateManagementInfo`, de modo que se puede ver que una instancia lo tiene activado. |
| `downloadBaseUrl` | sin definir | La URL pública por la que los dispositivos llegan al servicio, por ejemplo `https://<ingress host>/api/update-management`, sin query ni fragmento. Sin ella no se puede dar a ningún dispositivo una concesión de descarga: la petición del dispositivo se rechaza y se cuenta. |

Los demás ajustes acotan el servicio:

| Ajuste | Por defecto | Significado |
| --- | --- | --- |
| `maxArtifactBytes` | 512 MiB | El artefacto individual más grande. No puede superar `maxTenantArtifactBytes`. |
| `maxTenantArtifactBytes` | 2 GiB | Lo máximo que pueden ocupar en total los artefactos de un inquilino. |
| `maxConcurrentUploads` | 4 | Subidas que una réplica procesa a la vez. |
| `maxConcurrentDownloads` | 16 | Descargas que una réplica sirve a la vez. |
| `capabilityTtlSeconds` | 900 | Cuánto dura una concesión de descarga. Quince minutos es también el máximo; un valor mayor se rechaza al arrancar. |
| `sweepIntervalSeconds` | 30 | Cada cuánto busca la plataforma intentos cuyo dispositivo ha callado. |
| `deadlines.*` | consulta [más abajo](#deadlines) | Cuánto espera la plataforma en cada fase de un intento. |

Un valor negativo, una clave desconocida, o una `downloadBaseUrl` relativa, con credenciales o con
query se rechazan al arrancar.

## Artefactos {#artifacts}

Un artefacto se declara primero, sin contenido: `createArtifact` recibe un token, un **tipo**
(`FIRMWARE`, `SOFTWARE` o `CONFIGURATION`), un nombre, una etiqueta de versión, el **componente** del
dispositivo que actualiza (por ejemplo `firmware` o `app/agent`), el tamaño exacto en bytes, el
digest SHA-256 y los perfiles de dispositivo con los que es **compatible** (al menos uno; cada uno
debe existir). Nombre, componente y versión juntos son únicos dentro del inquilino. El tamaño
declarado se comprueba contra ambos límites antes de guardar nada.

El artefacto empieza en `UPLOADING`. Su contenido se envía después con un `PUT` HTTP a
`/api/update-management/updates/artifacts/{token}/content`, autenticado con un token de acceso del
inquilino que tenga `artifact:write`; la [referencia de la API GraphQL](../reference/graphql-api.md#endpoints)
explica los ajustes de ingress que necesita esa ruta. La subida se procesa en streaming y se juzga
por lo que realmente llegó: un cuerpo más largo o más corto que el tamaño declarado, o cuyo hash no
coincide con el digest, se descarta y el artefacto sigue en `UPLOADING`. Solo un contenido que
coincide deja el artefacto **`AVAILABLE`**, y a partir de ahí es inmutable. `retireArtifact` lo
retira de nuevas asignaciones; su registro y su contenido se conservan.

## Asignar una actualización {#assigning}

`assignArtifact(deviceToken, artifactToken)` indica a un dispositivo que instale un artefacto
`AVAILABLE`. El perfil del dispositivo debe estar en la lista de compatibles del artefacto, y la
asignación se rechaza mientras `allowUnverifiedArtifacts` esté desactivado. Un
[dispositivo deshabilitado](../guides/device-credentials.md#disabling-a-device) se rechaza con
`DEVICE_DISABLED`; mientras un dispositivo está deshabilitado, sus informes de actualización y
solicitudes de descarga se descartan y un enlace de descarga que ya tenía deja de funcionar, hasta
que se vuelve a habilitar. Un dispositivo trabaja hacia **un objetivo por componente**:

- asignar mientras la actualización anterior del componente sigue en curso se rechaza hasta que esa
  actualización se cancele;
- asignar de nuevo el mismo artefacto mientras esa actualización está en curso devuelve la asignación existente;
- asignar cuando la actualización anterior ya terminó la **sustituye**, y se conserva el historial.

`cancelUpdate` cancela al instante una actualización en cola. Antes de que empiece la instalación
registra la petición, y la actualización pasa a `CANCELLED` cuando el dispositivo confirma que ha
abandonado el trabajo. Una vez que el dispositivo ha empezado a instalar, la cancelación se
rechaza: una instalación no se puede revocar.

Leer el catálogo y las asignaciones requiere `artifact:read`; todo cambio requiere `artifact:write`.

## Cómo avanza un intento {#states}

Cada asignación lleva un **intento**, cuyo estado la plataforma deriva de lo que informa el
dispositivo:

| Estado | Significado |
| --- | --- |
| `QUEUED` | Creado; el dispositivo aún no ha informado. |
| `INITIATED` | El dispositivo confirmó la asignación. |
| `DOWNLOADING` | El dispositivo está descargando el artefacto, con los bytes y el total si los informa. |
| `DOWNLOADED` | El dispositivo tiene todos los bytes. |
| `VERIFIED` | El hash de los bytes que tiene el dispositivo coincide con el digest del artefacto. |
| `UPDATING` | El dispositivo está instalando. |
| `REBOOTING` | El dispositivo se está reiniciando en la nueva versión. |
| `UPDATED` | El dispositivo ejecuta la versión objetivo. |
| `FAILED` | El dispositivo informó de un fallo o, en una actualización `UNKNOWN`, su inventario mostró que había reiniciado con una versión distinta de la objetivo. |
| `TIMED_OUT` | Pasó un plazo antes de que se informara del inicio de la instalación. |
| `UNKNOWN` | Pasó un plazo durante la instalación o el reinicio. |
| `CANCELLED` | La actualización se canceló antes de empezar la instalación, o un operador abandonó una actualización `UNKNOWN`. |

Los informes solo hacen avanzar un intento; un informe de una etapa anterior se ignora. **`UPDATED`
solo se alcanza con pruebas**: el dispositivo informa de que la versión objetivo está en ejecución
y, cuando el artefacto necesita un reinicio (todo artefacto `FIRMWARE` lo necesita; los `SOFTWARE` y `CONFIGURATION`, no), desde un arranque distinto de aquel en el que la
instaló. Una confirmación de recepción, o el propio "éxito" de un instalador, no es una prueba.

### Plazos {#deadlines}

Los plazos se miden desde el último informe que la plataforma aceptó, así que un dispositivo lento
que sigue informando no agota su plazo. Cada uno es una clave bajo `updates.deadlines`, en segundos:

| Clave | Cubre | Por defecto | Cuando vence |
| --- | --- | --- | --- |
| `acknowledgeSeconds` | `QUEUED`, `INITIATED` | 1 día | `TIMED_OUT` |
| `downloadSeconds` | `DOWNLOADING`, `DOWNLOADED` | 6 horas | `TIMED_OUT` |
| `verifySeconds` | `VERIFIED` | 30 minutos | `TIMED_OUT` |
| `installSeconds` | `UPDATING` | 1 hora | `UNKNOWN` |
| `confirmSeconds` | `REBOOTING` | 1 hora | `UNKNOWN` |

El silencio mientras se instala o se reinicia es **`UNKNOWN`**, nunca fallo ni éxito: el
dispositivo puede estar en cualquiera de las dos versiones. Un intento `UNKNOWN` solo sale de ese
estado con pruebas. El siguiente informe de inventario del dispositivo dice qué ejecuta, y el
intento pasa a `UPDATED`, o a `FAILED` si el dispositivo se ha reiniciado y no está en la versión
objetivo. Hasta entonces se rechaza una nueva asignación para ese componente. Si el dispositivo
nunca va a informar, `abandonUnknownUpdate(assignmentToken, reason)` cierra la actualización como
`CANCELLED`, guarda tu motivo, registra el cambio a tu nombre, avisa al dispositivo y libera el
componente. No averigua qué ejecuta el dispositivo, así que compruébalo antes de dar por buena
cualquiera de las dos versiones.

## Qué significa "verificado" {#verified}

`VERIFIED`, y la comprobación de tamaño y digest en la subida, establecen la **integridad**: los
bytes son los que se declararon. No dicen nada de quién los produjo. El estado de confianza de todo
artefacto es `UNVERIFIED`, igual que la confianza que cada asignación comunica al dispositivo, así
que un dispositivo que necesite saber que una imagen es auténtica debe comprobar una firma propia.

## En el dispositivo {#device-side}

Un dispositivo se entera de una asignación en su topic `device-desired`, pide una concesión de
descarga, obtiene la imagen por HTTP con peticiones `Range` reanudables e informa de cada etapa en
`device-reports`. Los mensajes están en [Conectar un dispositivo](../guides/connecting-a-device.md#over-the-air-updates).

## Seguimiento {#monitoring}

Las métricas del servicio, incluidos los veredictos de los informes, las concesiones de descarga y
los intentos que movieron los plazos, se enumeran en la
[referencia de métricas](../deployment/metrics-reference.md#update-management).

## Relacionado

- **[Almacenamiento de objetos](./object-storage.md)**: dónde se guarda el contenido de los artefactos.
- **[Configuración del dispositivo](../guides/connecting-a-device.md#reserved-device-topics)**: ajustes por dispositivo entregados por los mismos canales del dispositivo.
- **[API GraphQL](../reference/graphql-api.md#endpoints)**: los endpoints, incluidas las rutas de subida y descarga.
