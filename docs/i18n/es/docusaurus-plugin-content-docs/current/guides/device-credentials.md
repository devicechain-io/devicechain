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
| `ACCESS_TOKEN` | un token portador (bearer token) (el id de la credencial) | ninguno — poseer el id es la prueba |
| `MQTT_BASIC` | un usuario (el id de la credencial) + contraseña | la contraseña |
| `X509_CERTIFICATE` | un sujeto/huella digital de certificado (el id de la credencial) | ninguno — la posesión se prueba fuera de banda |

## Leer una credencial requiere `device:write` {#reading-a-credential}

Un secreto, cuando el tipo lo tiene, es de **solo escritura**. Solo `MQTT_BASIC` tiene uno: su contraseña. Envías la contraseña al registrar la credencial, y nunca se devuelve en una lectura. La consola nunca la muestra: la introduces en un campo enmascarado, y el campo se borra una vez creada la credencial. A partir de ese momento, la API devuelve `null` para ella.

Eso protege la contraseña de `MQTT_BASIC`, y nada más. `ACCESS_TOKEN` y `X509_CERTIFICATE` no almacenan ningún secreto que ocultar, porque el **`credentialId` es en sí mismo el portador**. La tabla anterior lo dice para el token de acceso, y la verificación por evento también acepta una credencial de certificado solo con su id. `credentialId` es un campo que se lee sin restricciones. Así que, sea cual sea el tipo, leer las credenciales de un dispositivo te da lo necesario para autenticarte como ese dispositivo.

Por eso, toda consulta que devuelve una credencial requiere **`device:write`**, no `device:read`. Un usuario de solo lectura no puede listar las credenciales de un dispositivo, y por eso la consola no le muestra la pestaña **Credenciales**. El requisito no le quita nada a quien tiene `device:write`: ya puede registrar una credencial para cualquier dispositivo del inquilino y suplantarlo. Lo que evita es que esa capacidad llegue a la base de solo lectura que recibe todo miembro habilitado del inquilino.

## Cómo presenta un dispositivo una credencial

Las credenciales viajan en el cuerpo del evento, sobre cualquier transporte (consulta [Conexión de un dispositivo](./connecting-a-device.md)):

```json
{
  "device": "sensor-001",
  "credentialType": "ACCESS_TOKEN",
  "credentialId": "5f989616-2a0d-4160-8ae1-da5fad2898b2",
  "eventType": "Measurement",
  "payload": { "entries": [ { "measurements": { "temperature": "21.5" } } ] }
}
```

`MQTT_BASIC` lleva además `"credentialSecret": "<password>"`.

La plataforma resuelve la credencial al dispositivo que la posee y la verifica. La verificación respeta la **expiración** de la credencial, y **deshabilitar** una credencial la revoca. El modo de autenticación de dispositivos de la instancia decide con qué rigor se aplica:

- `disabled` — se confía en el token `device` autoafirmado, y no se necesita credencial.
- `optional` — una credencial presentada es autoritativa; sin ella, se confía en el token del dispositivo.
- `required` — el evento se rechaza a menos que presente una credencial válida. **Este es el valor predeterminado.**

Cuando una credencial autentica, el dispositivo al que se resuelve es autoritativo. Un evento cuyo token `device` nombra a un dispositivo *distinto* se rechaza, de modo que un dispositivo autenticado no puede suplantar a otro.

## Dos capas: la conexión y el evento

La credencial del cuerpo del evento es la verificación **por evento**. Las **conexiones** MQTT y NATS también se autentican, en el propio broker:

- **TLS.** Los listeners de MQTT y NATS usan TLS. Un dispositivo se conecta por TLS con la CA de la instancia.
- **Auth-callout.** El broker pide a la plataforma que apruebe cada conexión nueva (un auth-callout de NATS). La plataforma autentica la conexión y la vincula a los subjects de ese único dispositivo, no a los de su inquilino. El dispositivo puede publicar sus propios eventos y leer sus propios comandos, y nada más. Un dispositivo `MQTT_BASIC` se conecta con el usuario MQTT `{tenant}:{credentialId}` y la contraseña de la credencial: la misma credencial que autentica sus eventos. Un dispositivo que no puede autenticarse ni siquiera puede conectarse.
- **Client id.** La conexión también debe presentar el client id de MQTT `{instanceId}:{tenant}:{deviceToken}`. Puede añadir `:` y un sufijo a elección del dispositivo, por ejemplo `{instanceId}:{tenant}:{deviceToken}:cmd`. El sufijo permite que una segunda sesión concurrente funcione sin expulsar a la primera, como una conexión que publica y otra suscrita a los comandos. Cualquier id que no empiece por el propio `{instanceId}:{tenant}:{deviceToken}` del dispositivo se rechaza. El broker archiva la sesión de un dispositivo bajo su client id, así que dejar que el dispositivo lo eligiera libremente permitiría que uno se apropiara de la sesión de otro.

Consulta [Conexión de un dispositivo](./connecting-a-device.md) para los detalles de transporte.

## Las conexiones fallidas repetidas se ralentizan {#connect-backoff}

Tras fallos repetidos, las conexiones MQTT que presentan usuario y contraseña se ralentizan, de modo que una contraseña no puede adivinarse al ritmo al que el broker acepta conexiones.

- **Qué cuenta:** los fallos seguidos de conexión con contraseña para un mismo usuario MQTT (`{tenant}:{credentialId}`). Un usuario desconocido cuenta exactamente igual que uno real, así que la respuesta nunca revela qué usuarios existen.
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
2. Elige el **tipo** de credencial y completa los campos de ese tipo: genera o pega un token de acceso, introduce un usuario y una contraseña para MQTT-basic, o introduce un id de certificado para X.509.
3. Para `MQTT_BASIC`, anota la contraseña antes de continuar. El campo se borra si la operación tiene éxito y la contraseña no se vuelve a mostrar.
4. Haz clic en **Agregar credencial**.

Para eliminar una credencial, usa su fila. El dispositivo ya no podrá autenticarse con ella.

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

Para `MQTT_BASIC`, pasa también `credentialValue: "<password>"` (solo escritura). Tanto registrar una credencial como listar las credenciales de un dispositivo requieren la autoridad `device:write`; consulta [Leer una credencial requiere `device:write`](#reading-a-credential).
