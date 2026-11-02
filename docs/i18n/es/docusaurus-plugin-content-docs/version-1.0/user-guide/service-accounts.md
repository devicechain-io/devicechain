---
title: Cuentas de servicio
sidebar_position: 20
---

# Cuentas de servicio

Este capítulo es para los **administradores del inquilino** que conectan una integración —un
script, un proceso ETL, un servicio de backend— a DeviceChain. Una **cuenta de servicio** es una
cuenta no humana de tu inquilino. La integración presenta el **ID de cliente** y el **secreto de
cliente** de la cuenta al endpoint de tokens de OAuth y recibe un token de acceso de corta
duración, que envía a las API como cualquier otro. No interviene el inicio de sesión de ninguna
persona, así que la integración sigue funcionando cuando alguien se va, restablece su contraseña o
cambia de roles.

**Cuentas de servicio** está en el grupo **Acceso** de la barra lateral, junto a **Miembros**. Usa los
mismos permisos: `member:read` para listar las cuentas y `member:write` para crearlas o
cambiarlas. Un miembro limitado a una parte del inquilino (un usuario de cliente) no puede ver la
página.

## Qué puede hacer una cuenta {#what-an-account-can-do}

Una cuenta actúa con **exactamente las autoridades de sus roles**. A diferencia de un miembro, no
recibe además la base de solo lectura de visor: si la integración necesita leer dispositivos,
dale un rol que conceda `device:read` (el rol integrado **Viewer** lo hace).

Como un miembro, una cuenta se puede limitar a **clientes o grupos concretos**. Sus tokens ven
entonces solo los dispositivos de esos clientes y grupos y sus datos, exactamente como un
[usuario de cliente](./members-roles-and-customer-users.md#customer-users).

Las reglas que protegen la administración delegada también se aplican aquí:

- Solo puedes dar a una cuenta roles cuyas autoridades tengas tú.
- Solo puedes cambiar, rotar, deshabilitar o eliminar una cuenta si tienes todas las autoridades
  que ella tiene. Si no, la consola rechaza el cambio y lo indica.
- Una cuenta no puede administrar el inquilino: no puede gestionar miembros, roles ni otras
  cuentas de servicio, digan lo que digan sus roles.

Un inquilino puede tener hasta 200 cuentas de servicio.

## Crear una cuenta {#creating-an-account}

Elige **Nueva cuenta de servicio** y rellena:

- **Token**, un identificador único dentro del inquilino (letras, dígitos, guiones y guiones
  bajos). Da nombre a la cuenta en el registro de auditoría y no se puede cambiar después.
- **Nombre** y **Descripción**, opcionales.
- **Roles**, lo que la cuenta puede hacer.
- **Puede ver**: **Todo el inquilino** o **Clientes o grupos concretos**.

Elige **Crear cuenta de servicio**. La consola muestra entonces el **ID de cliente**, el
**Secreto de cliente** y el **Endpoint de tokens**.

{/* TODO(screenshot): service-account-secret, the one-time client credentials panel. */}

El secreto se muestra **una sola vez**. DeviceChain guarda un resumen con clave, nunca el secreto
en sí, así que nada puede volver a mostrártelo. Cópialo ahora en el almacén de secretos de tu
integración. Si se pierde, rota el secreto (más abajo).

Todos los secretos caducan. Un secreto nuevo es válido durante 365 días salvo que se indique otra
cosa a la API (de 1 a 730 días). La columna **Secretos** muestra cuántos secretos funcionan, sus
últimos cuatro caracteres y cuándo caduca el siguiente.

## Obtener un token {#obtaining-a-token}

La integración solicita un token con la concesión OAuth `client_credentials` en el endpoint de
tokens que muestra la consola (la URL del emisor de la instancia seguida de `/oauth/token`). Se
autentica con HTTP Basic:

```bash
curl -s -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials \
  https://dc.example.com/api/user-management/oauth/token
```

o, para un cliente que no puede poner una cabecera `Authorization`, con las credenciales como
campos de formulario:

```bash
curl -s \
  -d grant_type=client_credentials \
  --data-urlencode "client_id=$CLIENT_ID" \
  --data-urlencode "client_secret=$CLIENT_SECRET" \
  https://dc.example.com/api/user-management/oauth/token
```

La respuesta es un objeto JSON con un `access_token` y su `expires_in`. Envía el token como token
Bearer a cualquier API del inquilino:

```bash
curl -s -X POST https://dc.example.com/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"query($c:DeviceSearchCriteria!){devices(criteria:$c){results{token}}}",
       "variables":{"c":{"pageNumber":1,"pageSize":10}}}'
```

Lo que conviene saber del token:

- **Dura 5 minutos y no hay token de actualización.** Solicita uno nuevo cuando caduque; cada
  solicitud vuelve a leer la cuenta, así que un cambio que hagas llega a la integración con su
  siguiente token.
- **Nombra el inquilino de la cuenta** y ningún otro. Una solicitud no puede pedir otro inquilino.
- Dos campos de formulario opcionales lo acotan. `scope` limita el token al techo de un ámbito de
  OAuth (por ejemplo, `read-only`). `resource` lo vincula a un servidor de recursos por su
  dirección (un URI `https` absoluto): el servidor MCP, por ejemplo, solo acepta un token
  vinculado a su propia dirección.
- El endpoint de tokens tiene límite de frecuencia. Cada cuenta puede obtener aproximadamente un
  token cada 30 segundos, tras una ráfaga de 10, y cada dirección de origen puede hacer
  aproximadamente un intento cada 3 segundos, tras una ráfaga de 20. A un cliente que pide
  demasiado se le responde `429` (`slow_down`) con una cabecera `Retry-After` en segundos.
  Reutiliza un token hasta que esté a punto de caducar en lugar de pedir uno por llamada.

Todo intento fallido —un ID de cliente desconocido, un secreto incorrecto o caducado, una cuenta
deshabilitada— recibe la misma respuesta, `401 invalid_client`, así que quien llama no puede saber
cuál fue. A una cuenta de un inquilino deshabilitado o en proceso de eliminación se le responde
`400 invalid_grant`.

El requisito de inicio de sesión único del propio inquilino se aplica a las personas, no a las
cuentas de servicio.

## Rotar un secreto {#rotating-a-secret}

Elige **Rotar secreto** en el menú de la fila. Indica **Solapamiento (horas)**, cuánto tiempo
sigue funcionando el secreto actual (de 0 a 168, 24 por defecto), y confirma. La consola muestra
el secreto nuevo una vez.

Durante el solapamiento funcionan los dos secretos, así que puedes desplegar el nuevo sin
interrupción. Cuando termina el solapamiento, el secreto antiguo deja de funcionar. Una cuenta
nunca tiene más de dos secretos que funcionen: rotar de nuevo durante un solapamiento detiene el
más antiguo de inmediato. Un solapamiento de 0 detiene el secreto actual al instante, que es lo
que hay que hacer cuando un secreto se ha filtrado.

## Deshabilitar y eliminar {#disabling-and-deleting}

**Deshabilitar** impide que la cuenta obtenga tokens nuevos, tras confirmarlo; **Habilitar** se
lo vuelve a permitir. **Eliminar** borra la cuenta y todos sus secretos. Su token se puede reutilizar para una
cuenta nueva, que recibe un ID de cliente nuevo, así que un secreto antiguo nunca funciona con
ella.

Ninguna de las dos acciones recupera un token ya emitido: sigue siendo válido hasta que caduca, en
5 minutos como mucho. Lo mismo ocurre con un cambio de los roles o del alcance de la cuenta.

Un rol que tiene una cuenta de servicio no se puede eliminar hasta que ninguna cuenta (ni ningún
miembro) lo tenga.

## El registro de auditoría {#the-audit-log}

Lo que hace una cuenta se registra con su propio nombre, `service-account:<token>`, así que el
[registro de auditoría](./audit.md) nunca atribuye a una persona los cambios de una integración.
Cada token que obtiene se registra como un evento `service_account_token`. Crear, cambiar y
eliminar cuentas se registra con el nombre del administrador que lo hizo. Los intentos fallidos no
se registran.

## La API {#the-api}

La página es una vista sobre la API de user-management del inquilino. Por ejemplo, para crear una
cuenta:

```graphql
mutation {
  createServiceAccount(request: { token: "ci-bot", name: "CI", roleTokens: ["viewer"] }) {
    clientId
    clientSecret
    tokenEndpoint
  }
}
```

`serviceAccounts`, `serviceAccount`, `setServiceAccountAccess`, `setServiceAccountEnabled`,
`rotateServiceAccountSecret` y `deleteServiceAccount` hacen el resto; la
[referencia de la API GraphQL](../reference/graphql-api.md) describe cada una.
