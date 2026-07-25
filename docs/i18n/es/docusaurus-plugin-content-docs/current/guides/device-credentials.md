---
sidebar_position: 4
title: Credenciales de dispositivo
---

# Credenciales de dispositivo

La **identidad** de un dispositivo (su token estable) se mantiene separada de sus **credenciales** — el material que presenta para autenticarse (ADR-014). Un dispositivo puede tener varias credenciales y rotarlas sin cambiar su identidad.

:::note Estado
Disponible. Las credenciales se gestionan desde la pestaña **Credentials** de la página de detalle del dispositivo en la consola, o mediante la API GraphQL de device-management.
:::

## Tipos de credencial

| Tipo | El dispositivo presenta | Secreto almacenado |
| --- | --- | --- |
| `ACCESS_TOKEN` | un token portador (bearer token) (el id de la credencial) | ninguno — poseer el id es la prueba |
| `MQTT_BASIC` | un usuario (el id de la credencial) + contraseña | la contraseña |
| `X509_CERTIFICATE` | un sujeto/huella digital de certificado (el id de la credencial) | ninguno — la posesión se prueba fuera de banda |

## Los secretos son de solo escritura

Cuando un tipo lleva un secreto (la contraseña de `MQTT_BASIC`), ese secreto es de **solo escritura**: se envía cuando se registra la credencial y **nunca se devuelve en una lectura**. La consola lo muestra una sola vez, en el momento de la creación, y la API devuelve `null` para él en adelante. Un titular de `device:read` no puede exfiltrar secretos.

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
- `required` — se debe presentar una credencial válida o el evento se rechaza. **Este es el valor predeterminado** (ADR-025).

Cuando una credencial autentica, el dispositivo resuelto es autoritativo: un token `device` que nombra a un dispositivo *distinto* se rechaza, de modo que un dispositivo autenticado no puede suplantar a otro.

## Dos capas: la conexión y el evento

La credencial anterior es la verificación **por evento**. Además, las **conexiones** MQTT/NATS se autentican en el propio broker (ADR-025):

- Los listeners de MQTT y NATS son **TLS** — un dispositivo se conecta por TLS con la CA de la instancia.
- Un **auth-callout** de NATS autentica la conexión y la vincula a subjects por inquilino, de modo que un dispositivo solo puede publicar o suscribirse dentro de su propio inquilino. Para un dispositivo `MQTT_BASIC`, la conexión presenta el usuario MQTT **`{tenant}:{credentialId}`** y la contraseña de la credencial — la misma credencial que autentica sus eventos — de modo que un dispositivo que no puede autenticarse ni siquiera puede conectarse.

Ver [Conexión de un dispositivo](./connecting-a-device.md) para los detalles de transporte.

## Registrar una credencial (consola)

1. Abre la página de detalle del dispositivo y selecciona la pestaña **Credentials**.
2. Elige el **tipo** de credencial y completa los campos de ese tipo (genera o pega un token de acceso; introduce un usuario + contraseña para MQTT-basic; introduce un id de certificado para X.509).
3. Haz clic en **Add credential**. Para un tipo que lleva secreto, copia el secreto ahora — no se volverá a mostrar.

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

Para `MQTT_BASIC`, pasa también `credentialValue: "<password>"` (solo escritura). Registrar requiere la autoridad `device:write`; listar las credenciales de un dispositivo requiere `device:read`.
