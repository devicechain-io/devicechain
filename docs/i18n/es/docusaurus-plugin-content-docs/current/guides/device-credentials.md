---
sidebar_position: 5
title: Credenciales de dispositivo
---

# Credenciales de dispositivo

La **identidad** de un dispositivo es su token estable. Sus **credenciales** son el material que presenta para autenticarse, y se mantienen separadas de esa identidad. Un dispositivo puede tener varias credenciales y rotarlas sin cambiar su identidad.

:::note Estado
Disponible. Gestionas las credenciales desde la pestaña **Credenciales** de la página de detalle del dispositivo en la consola, o mediante la API GraphQL de device-management.
:::

## Tipos de credencial

| Tipo | El dispositivo presenta | Secreto almacenado |
| --- | --- | --- |
| `ACCESS_TOKEN` | un token portador (bearer token) (el id de la credencial) | un resumen irreversible con clave del token |
| `MQTT_BASIC` | un usuario (el id de la credencial) + contraseña | un resumen irreversible con clave de la contraseña |

:::note Las credenciales X.509 no se aceptan
El tipo `X509_CERTIFICATE` no es compatible hasta que se publique la verificación de certificados. Crear o actualizar una credencial de ese tipo se rechaza (`UNSUPPORTED`), y una credencial de ese tipo que ya exista deja de autenticar. Usa `ACCESS_TOKEN` o `MQTT_BASIC`.
:::

## Leer una credencial requiere `device:write` {#reading-a-credential}

Un secreto es de **solo escritura**, y ambos tipos lo tienen: la contraseña de una credencial `MQTT_BASIC` y el token de una credencial `ACCESS_TOKEN`. Lo envías al registrar la credencial, y la plataforma conserva solo un resumen irreversible con clave de él, nunca el secreto en sí. La API devuelve `null` para `credentialValue` en toda lectura. Un token de acceso es el `credentialId`, y la API lo devuelve exactamente **una vez**: en la respuesta a la llamada que lo establece (registrar la credencial, una actualización que lo rota o un reemplazo de dispositivo que lo genera). Toda lectura posterior devuelve en su lugar una huella, un valor que empieza por `dctk1$`, que identifica la credencial y no puede presentarse como el token. La consola muestra una contraseña o un token **una sola vez**, justo después de establecerlos, y nunca más. Consulta [Contraseñas y tokens que se muestran una vez, y su rotación](#show-once).

Así que, sea cual sea el tipo, leer las credenciales de un dispositivo no te da lo necesario para autenticarte como ese dispositivo. Aun así puedes encontrar una credencial por su token: `deviceCredentials` acepta el token, o la huella, como `credentialId` en sus criterios, y la plataforma calcula el resumen del token que escribes antes de buscar coincidencias.

Toda consulta que devuelve una credencial sigue requiriendo **`device:write`**, no `device:read`, porque el listado nombra los usuarios, las caducidades y el estado de un dispositivo. Un usuario de solo lectura no puede listar las credenciales de un dispositivo, y por eso la consola no le muestra la pestaña **Credenciales**. El requisito no le quita nada a quien tiene `device:write`, que ya puede registrar una credencial para cualquier dispositivo del inquilino. Lo que hace es mantener el listado fuera de la base de solo lectura que recibe todo miembro habilitado del inquilino.

## Cómo presenta un dispositivo una credencial

Por HTTP, la credencial viaja en el cuerpo del evento (consulta [Conexión de un dispositivo](./connecting-a-device.md)). Por MQTT autentica la conexión y el cuerpo del evento no lleva ninguna: un evento en el broker MQTT de la plataforma que incluya `credentialType`, `credentialId` o `credentialSecret` se rechaza. La forma HTTP es:

```json
{
  "device": "sensor-001",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "eventType": "Measurement",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

`MQTT_BASIC` lleva además `"credentialSecret": "<password>"`. Son campos solo de HTTP.

La plataforma resuelve la credencial al dispositivo que la posee y la verifica. La verificación respeta la **expiración** de la credencial, y **deshabilitar** una credencial la revoca. El modo de autenticación de dispositivos de la instancia decide con qué rigor se aplica:

- `disabled` — se confía en el token `device` autoafirmado, y no se necesita credencial.
- `optional` — una credencial presentada es autoritativa; sin ella, se confía en el token del dispositivo.
- `required` — el evento se rechaza a menos que presente una credencial válida. **Este es el valor predeterminado.**

Cuando una credencial autentica, el dispositivo al que se resuelve es autoritativo. Un evento cuyo token `device` nombra a un dispositivo *distinto* se rechaza, de modo que un dispositivo autenticado no puede suplantar a otro.

### Cuánto tarda en aplicarse una revocación {#revocation-timing}

Cada réplica de `device-management` guarda en memoria, durante hasta cinco minutos por defecto (`inMemoryCache.ttlSeconds`, de 1 a 3600 segundos), una credencial que acaba de verificar, para comprobar los siguientes eventos del dispositivo sin volver a leer la base de datos. La copia se comprueba exactamente igual que la credencial almacenada: la contraseña de una credencial `MQTT_BASIC` se compara en cada evento, y una expiración surte efecto en su momento. Una credencial que no se pudo verificar nunca se guarda, así que una credencial corregida funciona en el siguiente evento del dispositivo.

Cuando deshabilitas, borras o cambias una credencial, o reemplazas, editas o borras su dispositivo, la réplica que hace el cambio descarta su copia antes de responderte. Después avisa a las demás réplicas para que descarten la suya. Así que una revocación normalmente surte efecto en el siguiente evento del dispositivo. Una réplica cuya conexión con el broker se restablece tras una caída descarta todas las copias que conserva, ya que puede haberse perdido uno de esos avisos. Si el aviso se pierde sin una desconexión, o algunas réplicas aún ejecutan una versión anterior durante una actualización, la credencial revocada todavía puede autenticar eventos en esa réplica hasta que caduque su copia: hasta cinco minutos después del cambio por defecto. Reduce `inMemoryCache.ttlSeconds` si eso te parece demasiado; cada evento cuya credencial no se conserve en memoria lee entonces la base de datos.

Las conexiones no se ven afectadas. Cada conexión MQTT, con contraseña o con token de acceso, se comprueba contra la base de datos, así que una credencial revocada no puede abrir una conexión nueva en ninguna réplica. Cuando se borran los datos de un inquilino eliminado, todas las réplicas descartan las copias de ese inquilino.

Lo que presenta un dispositivo tampoco se guarda en claro mientras recorre la plataforma. La contraseña o el token de acceso que llega con un evento se sella antes de que el evento se ponga en el flujo de eventos interno de la plataforma, y a la copia de un mensaje que se guarda para diagnóstico, como uno que no se pudo decodificar, se le quitan las credenciales.

## Dos capas: la conexión y el evento

La credencial del cuerpo del evento es la verificación **por evento**. Las **conexiones** MQTT y NATS también se autentican, en el propio broker:

- **TLS.** Los listeners de MQTT y NATS usan TLS. Un dispositivo se conecta por TLS con la CA de la instancia.
- **Auth-callout.** El broker pide a la plataforma que apruebe cada conexión nueva (un auth-callout de NATS). La plataforma autentica la conexión y la vincula a los subjects de ese único dispositivo, no a los de su inquilino. El dispositivo puede publicar sus propios eventos y leer sus propios comandos, y nada más. Un dispositivo `MQTT_BASIC` se conecta con el usuario MQTT `{tenant}:{credentialId}` y la contraseña de la credencial: la misma credencial que autentica sus eventos. Un dispositivo que no puede autenticarse ni siquiera puede conectarse.
- **Client id.** La conexión también debe presentar el client id de MQTT `{instanceId}:{tenant}:{deviceToken}`. Puede añadir `:` y un sufijo a elección del dispositivo, por ejemplo `{instanceId}:{tenant}:{deviceToken}:cmd`. El sufijo permite que una segunda sesión concurrente funcione sin expulsar a la primera, como una conexión que publica y otra suscrita a los comandos. Cualquier id que no empiece por el propio `{instanceId}:{tenant}:{deviceToken}` del dispositivo se rechaza. El broker archiva la sesión de un dispositivo bajo su client id, así que dejar que el dispositivo lo eligiera libremente permitiría que uno se apropiara de la sesión de otro.

Consulta [Conexión de un dispositivo](./connecting-a-device.md) para los detalles de transporte.

## Deshabilitar un dispositivo {#disabling-a-device}

Deshabilitar un dispositivo lo deja fuera sin eliminarlo ni tocar sus credenciales. Úsalo para poner en cuarentena un dispositivo comprometido o que está inundando la plataforma, y vuelve a habilitarlo cuando esté corregido.

Mientras un dispositivo está deshabilitado:

- **Sus eventos se descartan.** Se rechazan al llegar, sea cual sea la credencial o el transporte por el que llegaron, y se cuentan en `devicechain_devicemanagement_resolve_device_disabled_total`. No se almacenan ni van a la cola de mensajes fallidos (dead-letter), y no se recuperan cuando el dispositivo vuelve a habilitarse. Su estado de conexión es la excepción, porque lo informa la plataforma y no el dispositivo: cuando termina una conexión que el dispositivo ya tenía, el dispositivo aparece como desconectado, y el cambio se conserva en su historial de eventos como cualquier otro cambio de conexión. Eso no genera ninguna alarma. Un dispositivo Sparkplug también puede aparecer como conectado mientras está deshabilitado, porque su nodo de borde informa de su conexión en su nombre.
- **No puede abrir una conexión nueva.** El broker rechaza la conexión con la misma respuesta que una contraseña incorrecta, y el registro de un dispositivo LwM2M se rechaza con `4.03 Forbidden`. Una conexión que el dispositivo ya tenía sigue abierta hasta que termina, pero todo lo que publica por ella se descarta o se rechaza: sus eventos, sus solicitudes e informes de configuración, y sus informes de actualización y solicitudes de descarga. La plataforma también deja de leer su información de dispositivo por LwM2M.
- **No se le envía configuración ni actualizaciones nuevas, y se pone al día al habilitarlo.** Una revisión de configuración creada mientras el dispositivo está deshabilitado se conserva pero no se envía, y no se le ofrece ninguna actualización. Al habilitarlo se le envía su última revisión de configuración y se le vuelve a ofrecer su actualización actual, de modo que normalmente no tiene que esperar a su próximo reinicio o sincronización. Como se le rechazó mientras estaba deshabilitado, una sincronización que hiciera entonces no se respondió; nada se lo comunica al dispositivo.
- **Los comandos que se le envían se rechazan** con `DEVICE_DISABLED`. Un comando de flota lo incluye entre los dispositivos rechazados. Consulta [Cuando se rechaza una admisión](./sending-commands.md#when-an-enqueue-is-refused). Los comandos encolados para él **antes** de deshabilitarlo quedan **retenidos**: ni se envían ni se cancelan. Salen, del más antiguo al más reciente, cuando lo vuelves a habilitar, o caducan a su propia hora de vencimiento mientras esperan. La pestaña **Comandos** del dispositivo en la consola muestra cuántos están retenidos, con la acción **Cancelar todos**; consulta [Comandos retenidos para un dispositivo deshabilitado](./sending-commands.md#held-while-disabled). La retención surte efecto en cuestión de instantes tras deshabilitar, así que un comando publicado en ese instante todavía llega al dispositivo, y un comando ya enviado está en el dispositivo y no se puede recuperar.
- **No genera alarmas de silencio.** Una regla que genera una alarma cuando un dispositivo deja de informar ignora un dispositivo deshabilitado, y una alarma de silencio que ya había levantado se **cierra** cuando el dispositivo se deshabilita. Cuando se vuelve a habilitar, la regla espera una ventana de silencio completa desde ese momento antes de poder considerarlo silencioso.
- **Todo lo demás se conserva**: sus datos, credenciales, configuración, relaciones y pertenencia a grupos.

El cambio se aplica en todas las réplicas con el siguiente evento del dispositivo: es un cambio del dispositivo, así que descarta las copias en caché igual que una [revocación](#revocation-timing).

En la consola, abre el dispositivo y elige **Deshabilitar** (o **Habilitar**); un dispositivo deshabilitado muestra una insignia **Deshabilitado** en su página y en la lista de dispositivos. Mediante GraphQL, establece `enabled` en una actualización parcial. Se rechaza `null`, y deshabilitar un dispositivo que ya está deshabilitado conserva el momento en que se deshabilitó por primera vez, que informa `disabledAt`:

```graphql
mutation {
  updateDevice(token: "pump-7", request: { enabled: false }) {
    token
    enabled
    disabledAt
  }
}
```

También se puede crear un dispositivo ya deshabilitado, con `enabled: false` en `createDevice`. El cambio queda registrado en el registro de auditoría como una actualización del dispositivo.

## Las conexiones fallidas repetidas se ralentizan {#connect-backoff}

Tras fallos repetidos, las conexiones MQTT que presentan usuario y contraseña se ralentizan, de modo que una contraseña no puede adivinarse al ritmo al que el broker acepta conexiones.

- **Qué cuenta:** los fallos seguidos de conexión con contraseña para un mismo usuario MQTT (`{tenant}:{credentialId}`). Un usuario desconocido cuenta exactamente igual que uno real y recibe el mismo rechazo, así que la respuesta nunca revela qué usuarios existen. El tiempo que tarda un rechazo es parecido pero no idéntico: comprobar un usuario que existe lee una fila de la base de datos que un usuario desconocido no lee. Esa fila contiene solo lo que la comprobación necesita, así que la diferencia no crece con lo que el dispositivo guarda en sus metadatos, su nombre o su descripción.
- **El calendario:** los primeros 10 fallos seguidos no se ralentizan. Tras el décimo, el siguiente intento con ese usuario espera 1 segundo. Cada fallo posterior duplica la espera, hasta 30 segundos.
- **Durante la espera se rechaza incluso la contraseña correcta.** El broker da el mismo rechazo que para una contraseña incorrecta. El dispositivo se conecta con normalidad cuando termina la espera.
- **Una conexión correcta pone la cuenta a cero.**
- **No se ralentizan:** las conexiones con token de acceso (una conexión sin contraseña) ni las credenciales que viajan en el cuerpo de los eventos. La verificación por evento no da ninguna respuesta al remitente, así que no sirve para adivinar.

:::warning Quien conozca el usuario de un dispositivo puede retrasar sus reconexiones
La cuenta se lleva por usuario, y el usuario no es secreto. Alguien que siga enviando contraseñas incorrectas para él puede impedir que ese dispositivo se reconecte mientras siga haciéndolo: cada espera está limitada a 30 segundos, pero puede iniciar la siguiente en cuanto termina la anterior. Un dispositivo que ya está conectado no se ve afectado hasta que se reconecta. No publiques los usuarios de los dispositivos, y da a cada credencial `MQTT_BASIC` una contraseña robusta.
:::

Las cuentas se guardan en JetStream, el almacenamiento persistente del broker, de modo que todas las réplicas de device-management ven las mismas.

- **Si no se puede acceder a JetStream, las conexiones con contraseña se rechazan** hasta que se pueda, porque una conexión que no se puede contar no se verifica. Esto incluye una breve ventana mientras cambia el líder de JetStream del bucket que guarda las cuentas, por ejemplo mientras se reinicia un nodo de NATS. Las conexiones con token de acceso siguen funcionando.
- **El bucket que guarda las cuentas tiene un tamaño limitado.** Cada conexión, correcta o no, conserva una entrada durante diez minutos. Si el bucket se llena, las conexiones siguen funcionando pero **dejan de ralentizarse**, y se dispara la alerta `DeviceCredentialAttemptStoreFull`. Eso ocurre cuando llegan conexiones para un número muy grande de usuarios distintos, o cuando una flota muy grande se reconecta a la vez. El bucket se vacía solo diez minutos después. Para darle más espacio, aumenta `instance.config.infrastructure.nats.kvStateMaxBytes`.

La métrica `devicechain_devicemanagement_credential_checks_total` cuenta cada verificación de conexión con contraseña por `outcome`. Entre los resultados están:

- `throttled` — una conexión rechazada durante una espera.
- `unavailable` — no se pudo acceder a las cuentas.
- `store_full` — el bucket estaba lleno.

## Registrar una credencial (consola)

1. Abre la página de detalle del dispositivo y selecciona la pestaña **Credenciales**.
2. Elige el **tipo** de credencial y completa los campos de ese tipo: genera o pega un token de acceso, o introduce un usuario y una contraseña para MQTT-basic. Para la contraseña, escribe una o haz clic en **Generar contraseña segura**.
3. Haz clic en **Agregar credencial**.
4. Aparece un aviso con la contraseña (en `MQTT_BASIC`) o el token de acceso (en `ACCESS_TOKEN`) y un botón para copiarlo. **Cópialo ahora**: es la única vez que se muestra. Haz clic en **Listo** cuando la tengas. Salir de la página, recargarla o abrir otro dispositivo también borra el aviso.

Para eliminar una credencial, usa su fila. El dispositivo ya no podrá autenticarse con ella.

## Contraseñas y tokens que se muestran una vez, y su rotación {#show-once}

Como la plataforma conserva solo un resumen de una contraseña `MQTT_BASIC` y del token de un `ACCESS_TOKEN`, nadie puede volver a leer ninguno de los dos, tampoco un administrador. La consola se comporta en consecuencia:

- **La contraseña o el token se muestra una vez**, en un aviso con un botón de copiar, al agregar la credencial o al establecer uno nuevo. Después, la fila de la credencial solo indica que hay una contraseña o un token de acceso establecido. Ninguna pantalla muestra un secreto almacenado ni su resumen, y ningún formulario se rellena con uno.
- **Una contraseña o un token perdido no se puede recuperar.** Establece uno nuevo.
- **Para rotar una contraseña**, haz clic en **Establecer nueva contraseña** en la fila de la credencial, escribe una o haz clic en **Generar contraseña segura**, y haz clic en **Guardar contraseña**. La contraseña nueva se muestra una vez, como se indicó. La contraseña actual del dispositivo deja de abrir conexiones nuevas en cuanto guardas; en los eventos deja de funcionar como una revocación, en cuestión de minutos de forma predeterminada (consulta [Cuánto tarda en aplicarse una revocación](#revocation-timing)). Ten lista la nueva para cargarla en el dispositivo. Los demás campos de la credencial no cambian.
- **Para rotar un token de acceso**, haz clic en **Establecer nuevo token** en la fila de la credencial y confirma. La consola genera un token nuevo y lo muestra una vez, como se indicó. El token actual del dispositivo deja de abrir conexiones nuevas en cuanto confirmas, y en los eventos deja de funcionar como una revocación. Por GraphQL, llama a `updateDeviceCredential` solo con `credentialId`.
- **Las contraseñas generadas** son 24 bytes aleatorios de la fuente aleatoria segura del navegador, escritos en base64 seguro para URL (32 caracteres).
- **Longitud.** La plataforma rechaza una contraseña `MQTT_BASIC` de menos de 16 bytes. La consola avisa mientras escribes, contando bytes UTF-8 y no caracteres (un carácter fuera de ASCII ocupa de dos a cuatro), y aun así envía lo escrito, de modo que si la plataforma discrepa verás su propio rechazo.

El **usuario** de `MQTT_BASIC` es un identificador, no un secreto, y sigue listado con un botón de copiar.

## Registrar una credencial (GraphQL)

```graphql
mutation {
  createDeviceCredential(request: {
    token: "b2e1…",                 # un token de credencial nuevo y único
    deviceToken: "sensor-001",
    credentialType: "ACCESS_TOKEN",
    credentialId: "5f989616-2a0d-4160-8ae1-da5fad2898b2",
    enabled: true
  }) { id token credentialType credentialId enabled }
}
```

En un `ACCESS_TOKEN`, el `credentialId` de la respuesta es el token que enviaste, y es la última vez que una respuesta lo contiene. Un token tiene como máximo 256 bytes y se almacena solo como un resumen con clave, así que se comprueba exactamente como lo envías, byte a byte. Para sustituir un token, llama a `updateDeviceCredential` solo con `credentialId`: el token anterior deja de funcionar al instante y la respuesta trae el nuevo. Cambiar una credencial existente a `ACCESS_TOKEN`, o desde `ACCESS_TOKEN`, exige `credentialId` en la misma solicitud (si no, `INVALID_VALUE`), porque el id almacenado significa un token en un tipo y un usuario en el otro.

Para `MQTT_BASIC`, pasa también `credentialValue: "<password>"` (solo escritura). La contraseña se comprueba exactamente como la envías, espacios iniciales o finales incluidos, así que debe coincidir byte a byte con lo que presenta el dispositivo; un valor vacío no guarda ninguna contraseña. La contraseña debe tener al menos 16 bytes (y como máximo 4096); una más corta se rechaza con `extensions.code` `INVALID_VALUE`, tanto al registrar una credencial como al cambiar su contraseña. Una contraseña establecida antes de existir este mínimo sigue funcionando hasta que la cambies. Cambiar una credencial existente a `MQTT_BASIC` exige `credentialValue` en la misma solicitud. Para cambiarla después, llama a `updateDeviceCredential` solo con `credentialValue`: los demás campos no cambian y la contraseña anterior deja de funcionar al instante. Ninguna de las dos llamadas devuelve la contraseña. Tanto registrar una credencial como listar las credenciales de un dispositivo requieren la autoridad `device:write`; consulta [Leer una credencial requiere `device:write`](#reading-a-credential).
