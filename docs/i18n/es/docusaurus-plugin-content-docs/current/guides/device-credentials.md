---
sidebar_position: 5
title: Credenciales de dispositivo
---

# Credenciales de dispositivo

La **identidad** de un dispositivo (su token estable) se mantiene separada de sus **credenciales** — el material que presenta para autenticarse. Un dispositivo puede tener varias credenciales y rotarlas sin cambiar su identidad.

:::note Estado
Disponible. Las credenciales se gestionan desde la pestaña **Credentials** de la página de detalle del dispositivo en la consola, o mediante la API GraphQL de device-management.
:::

## Tipos de credencial

| Tipo | El dispositivo presenta | Secreto almacenado |
| --- | --- | --- |
| `ACCESS_TOKEN` | un token portador (bearer token) (el id de la credencial) | ninguno — poseer el id es la prueba |
| `MQTT_BASIC` | un usuario (el id de la credencial) + contraseña | la contraseña |
| `X509_CERTIFICATE` | un sujeto/huella digital de certificado (el id de la credencial) | ninguno — la posesión se prueba fuera de banda |

## Leer una credencial requiere `device:write` {#reading-a-credential}

Cuando un tipo lleva un secreto (la contraseña de `MQTT_BASIC`), ese secreto es de **solo escritura**: se envía cuando se registra la credencial y **nunca se devuelve en una lectura**. La consola nunca lo muestra — la contraseña se introduce en un campo enmascarado y se borra una vez creada la credencial —, y la API devuelve `null` para él en adelante.

Eso protege la contraseña de `MQTT_BASIC`, y nada más. `ACCESS_TOKEN` y `X509_CERTIFICATE` no almacenan ningún secreto que retener: el **`credentialId` es en sí mismo el portador** — así lo dice la tabla anterior para el token de acceso, y la verificación por evento también acepta una credencial de certificado solo con su id —, y `credentialId` es un campo que se lee sin más. Así que leer las credenciales de un dispositivo entrega todo lo necesario para autenticarse como ese dispositivo, sea cual sea el tipo.

Por eso toda consulta que devuelva una credencial está protegida por **`device:write`**, no por `device:read`: un usuario de solo lectura no puede listar las credenciales de un dispositivo, y por eso la consola no le muestra la pestaña **Credentials**. La restricción no le quita nada a quien tiene `device:write` — ya puede registrar una credencial para cualquier dispositivo del inquilino y suplantarlo. Lo que evita es que esa capacidad llegue a la base de solo lectura que recibe todo miembro habilitado del inquilino.

## Cómo presenta un dispositivo una credencial

Las credenciales viajan en el cuerpo del evento, sobre cualquier transporte (ver [Conexión de un dispositivo](./connecting-a-device.md)):

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

La plataforma resuelve la credencial al dispositivo que la posee y la verifica — respetando la **expiración** y la **revocación mediante deshabilitación** de la credencial. El modo de autenticación de dispositivos de una instancia rige la aplicación:

- `disabled` — se confía en el token `device` autoafirmado (no se necesita credencial).
- `optional` — una credencial presentada es autoritativa; sin una, se confía en el token del dispositivo.
- `required` — se debe presentar una credencial válida o el evento se rechaza. **Este es el valor predeterminado**.

Cuando una credencial autentica, el dispositivo resuelto es autoritativo: un token `device` que nombra a un dispositivo *distinto* se rechaza, de modo que un dispositivo autenticado no puede suplantar a otro.

## Dos capas: la conexión y el evento

La credencial anterior es la verificación **por evento**. Además, las **conexiones** MQTT/NATS se autentican en el propio broker:

- Los listeners de MQTT y NATS son **TLS** — un dispositivo se conecta por TLS con la CA de la instancia.
- Un **auth-callout** de NATS autentica la conexión y la vincula a los subjects de **ese único dispositivo** — no a los de su inquilino —, de modo que un dispositivo puede publicar sus propios eventos y leer sus propios comandos, y nada más. Para un dispositivo `MQTT_BASIC`, la conexión presenta el usuario MQTT **`{tenant}:{credentialId}`** y la contraseña de la credencial — la misma credencial que autentica sus eventos — de modo que un dispositivo que no puede autenticarse ni siquiera puede conectarse.
- La conexión también debe presentar el **client id** de MQTT `{instanceId}:{tenant}:{deviceToken}`, opcionalmente seguido de `:` y un sufijo elegido por el dispositivo (por ejemplo `{instanceId}:{tenant}:{deviceToken}:cmd`), de modo que una segunda sesión concurrente — una conexión publicando y otra suscrita a los comandos — no expulse a la primera. Cualquier id que no empiece por el propio `{instanceId}:{tenant}:{deviceToken}` del dispositivo es rechazado. El client id es la clave con la que el broker archiva la sesión de un dispositivo, así que dejarlo a elección del dispositivo permitiría que uno se apropiara de la sesión de otro.

Ver [Conexión de un dispositivo](./connecting-a-device.md) para los detalles de transporte.

## Las conexiones fallidas repetidas se ralentizan {#connect-backoff}

Las conexiones MQTT que presentan usuario y contraseña se ralentizan tras fallos repetidos, de
modo que una contraseña no puede adivinarse al ritmo al que el broker acepta conexiones.

- **Qué cuenta:** los fallos seguidos de conexión con contraseña para un mismo usuario MQTT
  (`{tenant}:{credentialId}`). Un usuario desconocido cuenta exactamente igual que uno real, así
  que la respuesta nunca revela qué usuarios existen.
- **El calendario:** los primeros 10 fallos seguidos no se ralentizan. Tras el décimo, el
  siguiente intento con ese usuario espera 1 segundo, y cada fallo posterior duplica la espera,
  hasta 30 segundos.
- **Durante la espera se rechaza incluso la contraseña correcta.** El broker da el mismo rechazo
  que para una contraseña incorrecta. El dispositivo se conecta con normalidad cuando termina la
  espera.
- **Una conexión correcta pone la cuenta a cero.**
- **No se ralentizan:** las conexiones con token de acceso (una conexión sin contraseña) ni las
  credenciales que viajan en el cuerpo de los eventos. La verificación por evento no da ninguna
  respuesta al remitente, así que no sirve para adivinar.

:::warning Quien conozca el usuario de un dispositivo puede retrasar sus reconexiones

La cuenta se lleva por usuario, y el usuario no es secreto. Alguien que siga enviando
contraseñas incorrectas para el usuario de un dispositivo puede impedir que ese dispositivo se
reconecte mientras siga haciéndolo. Cada espera está limitada a 30 segundos, pero puede iniciar
la siguiente en cuanto termina la anterior. Un dispositivo que ya está conectado no se ve
afectado hasta que se reconecta. No publique los usuarios de los dispositivos y dé a cada
credencial `MQTT_BASIC` una contraseña robusta.

:::

Las cuentas se guardan en JetStream, de modo que todas las réplicas de device-management ven las
mismas:

- Si no se puede acceder a JetStream, **las conexiones con contraseña se rechazan** hasta que se
  pueda, porque una conexión que no se puede contar no se verifica. Esto incluye una breve
  ventana mientras cambia el líder de JetStream del bucket que guarda las cuentas, por ejemplo
  mientras se reinicia un nodo de NATS. Las conexiones con token de acceso siguen funcionando.
- El bucket que guarda las cuentas tiene un tamaño limitado. Cada conexión, correcta o no,
  conserva una entrada durante diez minutos. Si el bucket se llena, las conexiones siguen
  funcionando pero **dejan de ralentizarse**, y se dispara la alerta
  `DeviceCredentialAttemptStoreFull`. Eso ocurre cuando se envían conexiones para un número muy
  grande de usuarios distintos, o cuando una flota muy grande se reconecta a la vez. El bucket se
  vacía solo diez minutos después. Para darle más espacio, aumente
  `instance.config.infrastructure.nats.kvStateMaxBytes`.

La métrica `devicechain_devicemanagement_credential_checks_total` cuenta cada verificación de
conexión con contraseña por `outcome`: `throttled` para una conexión rechazada durante una
espera, `unavailable` cuando no se pudo acceder a las cuentas y `store_full` cuando el bucket
estaba lleno.

## Registrar una credencial (consola)

1. Abre la página de detalle del dispositivo y selecciona la pestaña **Credentials**.
2. Elige el **tipo** de credencial y completa los campos de ese tipo (genera o pega un token de acceso; introduce un usuario + contraseña para MQTT-basic; introduce un id de certificado para X.509).
3. Para `MQTT_BASIC`, anota la contraseña antes de continuar — el campo se borra al crear la credencial y la contraseña no se vuelve a mostrar. Después haz clic en **Add credential**.

Elimina una credencial desde su fila; el dispositivo ya no podrá autenticarse con ella.

## Registrar una credencial (GraphQL)

```graphql
mutation {
  createDeviceCredential(request: {
    token: "b2e1…",                 # a fresh unique credential token
    deviceToken: "sensor-001",
    credentialType: "ACCESS_TOKEN",
    credentialId: "5f989616-2a0d-4160-8ae1-da5fad2898b2",
    enabled: true
  }) { id token credentialType credentialId enabled }
}
```

Para `MQTT_BASIC`, pasa también `credentialValue: "<password>"` (solo escritura). Tanto registrar una credencial como listar las credenciales de un dispositivo requieren la autoridad `device:write` — ver [más arriba](#reading-a-credential).
