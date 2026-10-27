---
title: Actualizaciones over-the-air
---

# Actualizaciones over-the-air

El servicio **`update-management`** entrega imágenes de firmware, software y configuración
a los dispositivos. Mantiene un catálogo de **artefactos** por inquilino, te permite **asignar** un
artefacto a un dispositivo, envía al dispositivo lo que necesita para obtener la imagen y sigue cada
**intento de actualización** hasta que el dispositivo demuestra que ejecuta la nueva versión.

:::note Estado
**Disponible hoy:** el catálogo de artefactos con una subida en streaming verificada, la asignación
a un dispositivo cada vez, [despliegues](#rollouts) que asignan un artefacto a un grupo de dispositivos,
un número limitado de dispositivos a la vez, con pausa, reanudación, aborto y umbral de fallos, una descarga reanudable mediante una concesión de corta duración y el
seguimiento de cada intento con plazos. La consola tiene pantallas para los artefactos, los
firmantes y los despliegues, y una pestaña Actualizaciones en cada dispositivo para asignar una actualización y
seguirla; consulta [Actualizaciones](../user-guide/updates.md) en la guía de usuario. Un pequeño
dispositivo de referencia del repositorio sigue todo el lado del dispositivo del protocolo; consulta
[Construir un dispositivo actualizable](../guides/building-an-updatable-device.md).

**Todavía no:** los despliegues no tienen oleadas, promoción canario, programaciones ni ventanas de
mantenimiento. Solo un dispositivo conectado por MQTT puede recibir una actualización gestionada, porque las
asignaciones viajan por su canal MQTT. Los dispositivos que solo usan HTTP no pueden, los dispositivos
[LwM2M](./lwm2m.md) se actualizan con comandos ordinarios y [Sparkplug](./sparkplug.md) no tiene
actualización de firmware; consulta [Qué dispositivos se pueden actualizar](#transports).
:::

## Activarlo {#enabling}

`update-management` forma parte del perfil `default` (y por tanto de `full`); solo los perfiles
`telemetry` e `ingest-only` lo dejan fuera, y `enabledFunctionalAreas` puede nombrarlo allí. Depende de `device-management` y guarda el contenido de los artefactos
en el [almacén de objetos](./object-storage.md): sin almacén configurado, declarar un artefacto se
rechaza con `OBJECT_STORE_NOT_CONFIGURED`. En el backend S3 una subida se almacena en memoria, así
que allí los artefactos están limitados a 64 MiB sea cual sea el límite configurado.

### Antes de la primera actualización {#first-update}

Cuatro cosas tienen que cumplirse antes de que un dispositivo pueda recibir una actualización. Las dos
primeras son de la instancia, la tercera es una decisión que tomas una sola vez y la cuarta es el dispositivo.

1. **La instancia tiene un almacén de objetos.** Un volumen de sistema de archivos que más de un servicio pueda
   montar, o S3. Consulta [Una instancia sin almacén de objetos](#no-object-store).
2. **Se puede dar a los dispositivos un enlace de descarga.** El chart deriva la base del enlace del host del
   ingress de la instancia, así que esto se cumple en cualquier instancia con ingress; consulta
   `downloadBaseUrl` más abajo. La consola muestra un aviso en las pantallas de Actualizaciones cuando no es así.
3. **Hay un firmante registrado y el artefacto está firmado por él.** Por defecto
   (`allowUnverifiedArtifacts: false`) un artefacto que nadie ha firmado no se puede asignar: la asignación se
   rechaza con `UNVERIFIED_ARTIFACT_REFUSED`. Registrar un firmante requiere la autoridad `artifact:trust` y una
   clave pública que generas tú; sigue [Firmar actualizaciones](../guides/signing-updates.md). Activar
   `allowUnverifiedArtifacts` elimina el requisito y está pensado solo para instancias de desarrollo.
4. **El dispositivo habla el protocolo por MQTT** y tiene la clave pública del firmante. Consulta
   [Qué dispositivos se pueden actualizar](#transports) y
   [Construir un dispositivo actualizable](../guides/building-an-updatable-device.md).

Dos ajustes son decisiones deliberadas, no valores por defecto que dejar tal cual. Ambos están bajo
`functionalAreas.update-management.config.updates` en los values del chart:

| Ajuste | Por defecto | Significado |
| --- | --- | --- |
| `allowUnverifiedArtifacts` | `false` | Con `false`, solo se puede asignar un artefacto firmado por un [firmante](#signers) activo. Ponlo a `true` en una instancia de desarrollo o de pruebas para asignar también artefactos sin firmar. El valor se muestra en la consulta `updateManagementInfo`, de modo que se puede ver que una instancia lo tiene activado. |
| `downloadBaseUrl` | derivada del host del ingress | La URL pública por la que los dispositivos llegan al servicio, sin query ni fragmento. Si no la defines, el chart la fija en `<origen público>/api/update-management`, donde el origen es el host del ingress con `https` si el TLS del ingress está activado y `http` si no. Una clave que definas prevalece, y ponerla como cadena vacía desactiva las descargas. Sin ingress no hay origen del que derivarla, así que no se fija nada: no se puede dar a ningún dispositivo una concesión de descarga, y la petición del dispositivo se rechaza y se cuenta. Defínela en los values del chart; la superposición de ajuste de una Instance la rechaza, porque sigue al host de la instancia. Una base `http` en un host que no es loopback arranca, pero el servicio registra una advertencia, y un dispositivo rechaza una descarga `http` salvo que se le indique lo contrario. |

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

### Una instancia sin almacén de objetos {#no-object-store}

El contenido de los artefactos necesita un almacén de objetos, y el almacén se fija al crear la instancia
(consulta [el almacén de objetos](../deployment/bootstrap.md#object-store)). Hay un caso que requiere
cuidado. El perfil `default` incluye `update-management`. Cuando una instancia ejecuta el perfil `default` y su
almacén es un volumen de sistema de archivos `ReadWriteOnce` en un clúster que `dcctl` no creó, ese volumen lo
puede montar un solo servicio y otro ya lo ocupa. Por eso `dcctl` arranca `update-management` con
`updates.objectStore` en `none` en lugar de rechazar. Esto se aplica a un bootstrap nuevo y a una instancia
existente que incorpora el servicio al actualizar. El otro valor es `instance`, el predeterminado. `dcctl` fija `none` solo en ese
caso: un área que nombraste tú, el perfil `full`, un volumen compartible, S3 y un clúster que creó `dcctl` no
cambian. La superposición de ajuste no puede definir la clave, y una instalación solo con el chart puede usar
cualquiera de los dos valores; cualquier otro valor hace fallar el renderizado.

En ese modo el servicio funciona, y lo que no necesita contenido sigue funcionando: los firmantes, por ejemplo.
Declarar un artefacto y subir su contenido se rechazan con `OBJECT_STORE_NOT_CONFIGURED`, en lugar de
escribirse en un volumen que el servicio no tiene. `dcctl` lo indica al aplicar el cambio,
`updateManagementInfo` informa `objectStoreConfigured` como `false` y la consola muestra un aviso en las
pantallas de Actualizaciones.

Una instancia creada sin almacén no puede obtener uno al actualizar. Para usar actualizaciones en ella,
vuelve a crearla con `--object-store s3`, o con un almacén de sistema de archivos y
`--object-store-access-mode ReadWriteMany` sobre una clase de almacenamiento que lo admita.

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

**Una asignación lleva la firma con la que se hizo.** Cuando el artefacto está firmado, al asignarlo se
elige la firma más antigua cuyo firmante sigue activo y se **fija** a la asignación. El mensaje que recibe
el dispositivo lleva `trust: SIGNED` junto con esa firma (base64 estándar), el `keyId` del firmante y el
`algorithm` (`ECDSA_P256_SHA256` o `ED25519`), y el dispositivo los comprueba con las claves en las que
confía. `signedBy` en la asignación indica el firmante al que está fijada. Una asignación hecha sin ninguna
firma de un firmante activo (solo posible mientras `allowUnverifiedArtifacts` esté activado) no fija nada y
sigue siendo `UNVERIFIED` en el protocolo. La firma fijada no cambia: si el firmante se revoca después, una
actualización ya en curso conserva la firma con la que se asignó y cada reenvío de la asignación es idéntico,
mientras que una asignación *nueva* de ese artefacto se rechaza. Si la plataforma no puede leer la firma
fijada, no envía nada en lugar de enviar la asignación como `UNVERIFIED`.

De esto se derivan dos cosas para los operadores:

- **Un dispositivo debe tener la clave de todos los firmantes que podrían fijarse.** Gana la firma más
  antigua de un firmante activo, así que el firmante contra el que se le indica verificar depende de quién
  haya firmado el artefacto. Instala la clave de un firmante nuevo en tus dispositivos **antes** de revocar
  el anterior, y antes de que las firmas del anterior dejen de ser las más antiguas entre las activas.
- **Desactivar `allowUnverifiedArtifacts` impide que se envíen las asignaciones sin firmar**, también el
  reenvío cuando un dispositivo se sincroniza. El dispositivo conserva lo que ya tenía; asigna un artefacto
  firmado para hacerlo avanzar.

`cancelUpdate` cancela al instante una actualización en cola. Antes de que empiece la instalación
registra la petición, y la actualización pasa a `CANCELLED` cuando el dispositivo confirma que ha
abandonado el trabajo. Una vez que el dispositivo ha empezado a instalar, la cancelación se
rechaza: una instalación no se puede revocar.

Leer el catálogo, las asignaciones y los firmantes requiere `artifact:read`. Cambiar el catálogo, asignar y
adjuntar una firma requieren `artifact:write`. Registrar o revocar un firmante requiere `artifact:trust`, que
está separado a propósito: poder publicar un binario no incluye decidir qué firma lo hace de confianza.

## Desplegar en un grupo {#rollouts}

Un **despliegue** asigna un artefacto a los dispositivos de un [grupo de dispositivos](../user-guide/groups-facets-browse.md),
un número limitado a la vez, y se puede pausar, reanudar y abortar.

1. `createUpdateRollout(request)` lo declara como `DRAFT`: el artefacto (que debe estar `AVAILABLE` y
   poder asignarse, igual que con `assignArtifact`), el grupo, `maxInFlight` (de 1 a 1000) y un
   `failureThreshold` opcional. Todavía no se resuelve ni se envía nada.
2. `startUpdateRollout(token)` **fija los miembros del grupo** como dispositivos del despliegue y lo pasa
   a `RUNNING`. Un dispositivo que se una al grupo después no forma parte del despliegue, y uno que salga
   sigue siendo destino; inicia un despliegue nuevo para llegar a los recién llegados. Un grupo dinámico
   se resuelve desde su versión publicada, que el despliegue registra. Un grupo de más de 10.000
   dispositivos se rechaza (`ROLLOUT_TOO_LARGE`) en lugar de recortarse.
3. Después el servicio asigna el artefacto a los dispositivos en orden, sin dejar nunca que más de
   `maxInFlight` tengan una actualización sin terminar. Cada dispositivo pasa por las mismas reglas que
   `assignArtifact`: un dispositivo que no existe, está deshabilitado, tiene un perfil incompatible o
   está ocupado con otra actualización se **omite** con el código de ese rechazo (`DEVICE_NOT_FOUND`,
   `DEVICE_DISABLED`, `INCOMPATIBLE_PROFILE`, `UPDATE_IN_PROGRESS`) y no ocupa plaza. Un dispositivo que
   ya está trabajando en el mismo artefacto, o que ya se actualizó a él, se cuenta con la asignación que
   tiene en lugar de recibir la orden otra vez.
4. El despliegue queda `COMPLETED` cuando no hay ningún dispositivo esperando y ninguno tiene una
   actualización sin terminar.

El resultado de un dispositivo es siempre su propio intento de actualización: `SUCCEEDED` cuando llegó a
`UPDATED`, `FAILED` para `FAILED` o `TIMED_OUT`, y `UNKNOWN` mientras el resultado es desconocido. **Un
dispositivo `UNKNOWN` conserva su plaza** dentro de `maxInFlight` y cuenta como fallo, porque puede haber
instalado la actualización y no haber vuelto.

| Control | Qué hace | Qué no hace |
| --- | --- | --- |
| `pauseUpdateRollout` | Ningún dispositivo más recibe la orden de actualizar. | Los dispositivos que ya se están actualizando continúan. |
| `resumeUpdateRollout` | El envío continúa. | No pone a cero el recuento de fallos: tras una pausa por umbral se rechaza (`ROLLOUT_FAILURE_THRESHOLD_REACHED`) salvo que pases un `failureThreshold` más alto. |
| `abortUpdateRollout` | Los dispositivos que aún no recibieron la orden quedan `CANCELLED` y el despliegue termina para siempre. | Los dispositivos que ya se están actualizando terminan, y se siguen mostrando. Cancela cualquiera de ellos con `cancelUpdate`. |

Cuando el número de dispositivos fallidos y desconocidos alcanza `failureThreshold`, el despliegue se
pausa solo con el motivo `FAILURE_THRESHOLD`. También se pausa con `ARTIFACT_NOT_ASSIGNABLE` si el
artefacto se retira, o pierde la firma que exige esta instancia, mientras está en curso.

`updateRollout(token)` y `updateRollouts(criteria)` devuelven cada despliegue con su `progress` (recuentos
de dispositivos pendientes, en curso, correctos, fallidos, desconocidos, cancelados y omitidos), y
`updateRolloutTargets(token, criteria)` pagina sus dispositivos con su resultado, filtrables por
resultado. Los despliegues necesitan las mismas autoridades que las asignaciones individuales:
`artifact:read` para leerlos y `artifact:write` para crearlos y controlarlos. Un miembro limitado a
algunos clientes no puede ver ni controlar despliegues, porque un despliegue abarca dispositivos fuera
del alcance de ese miembro. Cada inicio, pausa, reanudación y aborto queda en el
[registro de auditoría](../user-guide/audit.md) a nombre de quien lo hizo.

Los despliegues siguen en marcha tras un reinicio y con varias réplicas del servicio: cada réplica hace
avanzar los despliegues en curso cada pocos segundos, y un despliegue lo hace avanzar una sola réplica a
la vez, así que el límite se respeta haya las réplicas que haya. Las oleadas, las programaciones y las
ventanas de mantenimiento para despliegues todavía no están disponibles.

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

Un dispositivo que rechaza una asignación por su firma informa `FAILED` con el código `UNKNOWN_SIGNER`, si no
confía en la clave del firmante o si exige una firma y la asignación es `UNVERIFIED`, o `SIGNATURE_INVALID`, si
la firma no se verifica. Comprueba una firma contra los bytes que tiene, así que informa de una firma incorrecta
o desconocida después de `DOWNLOADED`, cuando ya ha calculado su hash. Un dispositivo que exige imágenes
firmadas puede rechazar antes una asignación `UNVERIFIED`, justo después de `RECEIVED` y sin descargar nada, y el
dispositivo de referencia lo hace. Un dispositivo puede informar `FAILED` en cualquier etapa, así que ambas formas son válidas.

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
solo el digest: no indica para qué versión, componente o perfiles compatibles es la imagen, así que la misma imagen declarada con otra
versión queda avalada por la misma firma. El dispositivo tiene que hacer cumplir por sí mismo la versión y el
componente; consulta [En el dispositivo](#device-side).

`revokeArtifactSigner(token)` es definitivo. Las firmas del firmante siguen registradas pero dejan de contar,
así que todo artefacto que solo él avalaba pasa a `UNVERIFIED` de inmediato y ya no se puede asignar (salvo que
`allowUnverifiedArtifacts` esté activado); no se reescribe ningún artefacto. El
`soleSignedArtifactCount` de un firmante te dice de antemano a cuántos artefactos afectaría. Una asignación ya
hecha no se retira.

## Qué significa "verificado" {#verified}

`VERIFIED`, y la comprobación de tamaño y digest en la subida, establecen la **integridad**: los
bytes son los que se declararon. No dicen nada de quién los produjo; eso lo añade una [firma](#signers).
Una asignación de un artefacto firmado comunica `SIGNED` al dispositivo y le entrega la firma, de modo que un
dispositivo que tenga la clave pública del firmante puede comprobar que la imagen es auténtica antes de
instalarla. Ambos algoritmos firman los 32 bytes sin procesar del digest SHA-256, no el archivo.

## Qué dispositivos se pueden actualizar {#transports}

Una actualización gestionada es una conversación por el canal MQTT del dispositivo: la plataforma publica la
asignación y el enlace de descarga en `device-desired`, y el dispositivo responde en `device-reports`. Solo la
descarga en sí es HTTP. Eso decide qué dispositivos puede actualizar la plataforma.

| Dispositivo | Actualización gestionada | Qué hacer en su lugar |
| --- | --- | --- |
| Se conecta por MQTT | Sí | Sigue [Conectar un dispositivo](../guides/connecting-a-device.md#over-the-air-updates). |
| Solo usa HTTP | No. Puede enviar eventos por HTTP, pero nada puede avisarle de que existe una asignación y no tiene canal por el que informar. | Conéctalo también por MQTT, o actualízalo por tus propios medios. |
| [LwM2M](./lwm2m.md) | No. La plataforma no llega a los dispositivos LwM2M. | Impulsa el objeto estándar Firmware Update con comandos ordinarios, como en [Actualización de firmware con el objeto 5](./lwm2m.md#firmware-update-object-5). Ese procedimiento es una vía admitida, pero no es un trabajo gestionado: sin artefacto, sin retransmisión de la firma y sin seguimiento del intento. |
| Nodo perimetral [Sparkplug](./sparkplug.md) | No. Sparkplug no define actualización de firmware, y DeviceChain no inventa una. | Un nodo que deba actualizarse a través de DeviceChain se conecta también a DeviceChain como un dispositivo MQTT ordinario y usa este canal. Consulta [actualizar un nodo perimetral](./sparkplug.md#updating-an-edge-node). |

## En el dispositivo {#device-side}

Un dispositivo se entera de una asignación en su topic `device-desired`, pide una concesión de
descarga, obtiene la imagen por HTTP con peticiones `Range` reanudables e informa de cada etapa en
`device-reports`. Los mensajes están en [Conectar un dispositivo](../guides/connecting-a-device.md#over-the-air-updates).

La plataforma comprueba los bytes contra el digest cuando llegan y entrega al dispositivo una firma de ese
digest, y es el dispositivo quien decide. Como la firma cubre solo el digest, una imagen más antigua
correctamente firmada, o una construida para otro componente, sigue siendo una imagen firmada válida. La
plataforma no vincula la versión ni el componente a la firma, así que el dispositivo debe rechazar una versión
que no sea más reciente que la que ejecuta, y una imagen para un componente que no es el suyo. También debe no
ejecutar nada de la imagen antes de comprobar el digest y la firma, conservar las claves de confianza en local en
lugar de tomar una de un mensaje, e informar `RUNNING` solo con pruebas de la versión que realmente se ejecuta.
[Construir un dispositivo actualizable](../guides/building-an-updatable-device.md) recorre una implementación de
referencia que hace todo esto y muestra el orden de las comprobaciones.

## Seguimiento {#monitoring}

Las métricas del servicio, incluidos los veredictos de los informes, las concesiones de descarga y
los intentos que movieron los plazos, se enumeran en la
[referencia de métricas](../deployment/metrics-reference.md#update-management).

## Relacionado

- **[Construir un dispositivo actualizable](../guides/building-an-updatable-device.md)**: el lado del dispositivo, recorrido sobre un dispositivo de referencia.
- **[Firmar actualizaciones](../guides/signing-updates.md)**: crear claves y firmar artefactos.
- **[Almacenamiento de objetos](./object-storage.md)**: dónde se guarda el contenido de los artefactos.
- **[Configuración del dispositivo](../guides/connecting-a-device.md#reserved-device-topics)**: ajustes por dispositivo entregados por los mismos canales del dispositivo.
- **[API GraphQL](../reference/graphql-api.md#endpoints)**: los endpoints, incluidas las rutas de subida y descarga.
