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

**Todavía no:** a un dispositivo aún no se le entrega la firma de un artefacto, así que no puede
comprobarla por sí mismo; no hay despliegue por flota (asignación escalonada o por porcentaje a muchos dispositivos); no hay
pantallas de consola para las actualizaciones, así que se usan GraphQL y la subida por HTTP; y los
dispositivos que solo usan HTTP no pueden recibir actualizaciones, porque las asignaciones viajan
por el canal MQTT del dispositivo. Por la misma razón, los dispositivos [LwM2M](./lwm2m.md) y
[Sparkplug](./sparkplug.md) tampoco pueden recibirlas; el firmware de un dispositivo LwM2M se
impulsa en cambio con comandos ordinarios.
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
| `allowUnverifiedArtifacts` | `false` | Con `false`, solo se puede asignar un artefacto firmado por un [firmante](#signers) activo. Ponlo a `true` en una instancia de desarrollo o de pruebas para asignar también artefactos sin firmar. El valor se muestra en la consulta `updateManagementInfo`, de modo que se puede ver que una instancia lo tiene activado. |
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
asignación se rechaza (`UNVERIFIED_ARTIFACT_REFUSED`) salvo que el artefacto esté [firmado](#signers) por un
firmante activo o `allowUnverifiedArtifacts` esté activado. Un
[dispositivo deshabilitado](../guides/device-credentials.md#disabling-a-device) se rechaza con
`DEVICE_DISABLED`; mientras un dispositivo está deshabilitado, sus informes de actualización y
solicitudes de descarga se descartan y un enlace de descarga que ya tenía deja de funcionar, hasta
que se vuelve a habilitar, momento en que se le vuelve a ofrecer su asignación actual. Deshabilitar no cancela una actualización en curso: los
[plazos](#deadlines) siguen corriendo y pueden pasarla a `TIMED_OUT` o `UNKNOWN`; si el dispositivo
se vuelve a habilitar antes de que venza el plazo de la fase, su siguiente informe continúa la
actualización y un enlace de descarga que ya tenía vuelve a funcionar. Un dispositivo trabaja hacia
**un objetivo por componente**:

- asignar mientras la actualización anterior del componente sigue en curso se rechaza hasta que esa
  actualización se cancele;
- asignar de nuevo el mismo artefacto mientras esa actualización está en curso devuelve la asignación existente;
- asignar cuando la actualización anterior ya terminó la **sustituye**, y se conserva el historial.

`cancelUpdate` cancela al instante una actualización en cola. Antes de que empiece la instalación
registra la petición, y la actualización pasa a `CANCELLED` cuando el dispositivo confirma que ha
abandonado el trabajo. Una vez que el dispositivo ha empezado a instalar, la cancelación se
rechaza: una instalación no se puede revocar.

Leer el catálogo, las asignaciones y los firmantes requiere `artifact:read`. Cambiar el catálogo, asignar y
adjuntar una firma requieren `artifact:write`. Registrar o revocar un firmante requiere `artifact:trust`, que
está separado a propósito: poder publicar un binario no incluye decidir qué firma lo hace de confianza.

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

## Firmantes {#signers}

El **estado de confianza** de un artefacto indica si alguien de tu confianza responde por él. Es `UNVERIFIED`
hasta que al menos un **firmante** que hayas registrado lo firma, y `SIGNED` después. Se calcula cada vez que
se lee, a partir de las firmas registradas y del estado de los firmantes que las hicieron, así que
`artifacts` se puede filtrar por `trustStatus`.

Un firmante es una clave pública. `registerArtifactSigner(name, publicKeyPem)` recibe un único bloque PEM SPKI
`PUBLIC KEY` con una clave ECDSA P-256 o Ed25519; otros tipos de clave y curvas se rechazan. Registra solo la
mitad pública: la plataforma nunca necesita una clave privada, y si recibe una la rechaza
(`SIGNER_KEY_REFUSED`) sin repetirla, escribirla en los logs ni guardarla. El `keyId` del firmante es el SHA-256
en hexadecimal minúscula de la codificación DER de la clave, y una clave solo puede registrarse una vez por
inquilino.

Firmas el **digest SHA-256** del artefacto, los 32 bytes en bruto y no el texto hexadecimal, con la clave privada
que conservas tú: ECDSA P-256 produce una firma ASN.1 DER del digest, y Ed25519 firma los 32 bytes del digest.
`addArtifactSignature(artifactToken, signerToken, signature)` recibe la firma en base64 estándar y la comprueba
contra el digest que la plataforma guardó al declarar el artefacto. Una firma que no se verifica se rechaza
(`SIGNATURE_INVALID`), de modo que no se puede adjuntar una firma hecha sobre otros bytes, y un firmante
revocado se rechaza (`SIGNER_REVOKED`). Volver a firmar con el mismo firmante no cambia nada. La firma cubre
solo el digest: no indica para qué versión o componente es la imagen, así que la misma imagen declarada con otra
versión queda avalada por la misma firma.

`revokeArtifactSigner(token)` es definitivo. Las firmas del firmante siguen registradas pero dejan de contar,
así que todo artefacto que solo él avalaba pasa a `UNVERIFIED` de inmediato y ya no se puede asignar (salvo que
`allowUnverifiedArtifacts` esté activado); no se reescribe ningún artefacto. El
`soleSignedArtifactCount` de un firmante te dice de antemano a cuántos artefactos afectaría. Una asignación ya
hecha no se retira.

## Qué significa "verificado" {#verified}

`VERIFIED`, y la comprobación de tamaño y digest en la subida, establecen la **integridad**: los
bytes son los que se declararon. No dicen nada de quién los produjo; eso lo añade una [firma](#signers).
Hoy una asignación sigue comunicando `UNVERIFIED` al dispositivo, así que un dispositivo que necesite saber
que una imagen es auténtica debe comprobar una firma propia.

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
