---
sidebar_position: 3.5
title: Importar dispositivos desde CSV
---

# Importar dispositivos desde CSV {#importing-devices}

Lo habitual es dar de alta una flota a partir de un archivo de inventario: un número de serie, un nombre, un tipo de dispositivo y, a veces, una credencial emitida en fábrica. **Importar** lee ese archivo y crea los dispositivos que contiene. Es la vía para un inventario arbitrario; para «N dispositivos de prueba numerados» usa **Creación masiva**, que genera dispositivos a partir de plantillas.

:::note Estado
Disponible en la consola (**Dispositivos → Importar**) y, mediante la API GraphQL de device-management, como `importDevices`.
:::

La importación solo **crea** y se puede **ejecutar de nuevo sin riesgo**. La clave es el token del dispositivo: una fila cuyo dispositivo ya existe exactamente como la fila lo describe se informa como `EXISTS`, una fila cuyo dispositivo existe de forma distinta se informa como `CONFLICT`, y un dispositivo existente nunca se modifica. Cada fila es su propia transacción, de modo que una fila errónea nunca detiene a las demás.

## Importar en la consola {#console}

1. Abre **Dispositivos** y elige **Importar**.
2. Elige cómo se tratan las credenciales ([modos](#credential-modes) más abajo).
3. Elige un archivo CSV o pega las filas. El archivo necesita una fila de encabezado.
4. Revisa la asignación de columnas. Las columnas cuyo encabezado coincide con un campo se asignan solas, incluidos los encabezados que escribe **Exportar CSV** de la propia consola, de modo que una lista exportada se puede importar tal cual. Si el archivo no tiene columna de tipo, elige un tipo de dispositivo para todo el archivo.
5. Elige **Comprobar el archivo**. Es una ejecución de prueba: el servidor decide cada fila exactamente como lo hará la importación real y no escribe nada. Los resultados indican el resultado de cada fila y se pueden filtrar por resultado.
6. Elige **Importar N dispositivos** (N es el número de filas que, según la comprobación, se crearían). Las filas se envían en lotes de 1000, uno tras otro, con indicador de progreso; **Detener tras este lote** termina la ejecución limpiamente entre lotes.
7. Al terminar, descarga los **resultados** (el resultado de cada fila) o las **filas fallidas**, corrígelas e importa ese archivo. Los dispositivos ya creados se informan como `EXISTS` y no se tocan.

Un archivo puede tener hasta 10 000 filas y 5 MiB. Un archivo mayor se rechaza antes de leerlo; divídelo e importa las partes.

### Lectura del archivo {#reading-the-file}

- El archivo debe estar en UTF-8. Un archivo guardado con otra codificación se rechaza con un mensaje, en lugar de leerse con caracteres deformados; en una hoja de cálculo, guárdalo como **CSV UTF-8**. Una marca de orden de bytes inicial se ignora.
- Las comas, los puntos y coma y las tabulaciones se reconocen a partir de la primera línea, de modo que un archivo guardado por una hoja de cálculo con coma decimal se importa tal cual.
- Los valores pueden ir entre comillas, pueden contener el delimitador, comillas dobles y saltos de línea, y las líneas pueden terminar en `LF`, `CRLF` o `CR`. Las líneas en blanco se omiten.
- Nada del archivo se evalúa. Una celda que empieza por `=`, `+`, `-` o `@` se guarda como el texto que es. (La exportación de la consola antepone un apóstrofo a esas celdas para que una hoja de cálculo no las trate como fórmulas; la importación lo quita de nuevo, así que un viaje de ida y vuelta devuelve los valores originales.)
- En la consola se recortan los espacios de los extremos de todos los valores salvo el secreto de la credencial, que se envía exactamente como está escrito.

## Columnas {#columns}

| Columna | Obligatoria | Significado |
| --- | --- | --- |
| `token` | Sí | El token del dispositivo: letras, dígitos, guiones y guiones bajos, comenzando por una letra o un dígito, hasta 128 caracteres. Es la clave con la que se reconoce una nueva ejecución. |
| `deviceTypeToken` | Sí, por fila o para todo el archivo | El token de un tipo de dispositivo existente. Un tipo desconocido deja la fila en `INVALID`. |
| `externalId` | No | Tu identificador de negocio, hasta 256 caracteres, único en el tenant. Se comprueba para detectar conflictos y nunca es la clave. |
| `name` | No | Hasta 128 caracteres. |
| `description` | No | Hasta 1024 caracteres. |
| `metadata` | No | Un documento JSON, hasta 64 KiB. Se compara por valor en una nueva ejecución, así que el orden de las claves y los espacios no importan. |
| `provisioningProfileToken` | No | El token de un [perfil de aprovisionamiento](./device-self-registration.md#earmarking) para el que reservar el dispositivo, de modo que ese perfil pueda adoptarlo cuando el dispositivo se registre por sí mismo. Debe existir y ser del tipo del dispositivo, o la fila es `INVALID`. Un dispositivo que ya existe con otra reserva hace que la fila sea un `CONFLICT`. |
| `credentialType` | Solo credenciales de fábrica | `ACCESS_TOKEN` o `MQTT_BASIC`. |
| `credentialId` | Solo credenciales de fábrica | Lo que presenta el dispositivo: el propio token de acceso o el nombre de usuario MQTT. |
| `credentialValue` | Solo `MQTT_BASIC` | La contraseña, de al menos 16 bytes y como máximo 4096. Solo escritura. |

Los dispositivos siempre se crean **habilitados**; no hay columna para deshabilitar un dispositivo al importar. Deshabilítalo después desde su página de detalle.

## Modos de credenciales {#credential-modes}

| Modo | Qué hace | Secretos en el archivo |
| --- | --- | --- |
| **Ninguna** (`NONE`) | Crea solo los dispositivos. Las credenciales se añaden después, desde la pestaña **Credenciales** del dispositivo o por GraphQL. | No |
| **Credenciales de fábrica** (`FACTORY_CREDENTIALS`) | Crea cada dispositivo junto con la credencial que su fábrica proporcionó, en una sola transacción. Una fila nunca termina con un dispositivo sin credencial, ni al revés. | Sí: el archivo ya existe en tu fábrica; la importación lo consume y no crea ninguno |

No existe un modo que genere credenciales y te entregue un archivo de secretos. DeviceChain no crea archivos que contengan secretos.

Las columnas de credenciales se rechazan fuera del modo de credenciales de fábrica, de modo que un secreto no pueda colarse en un archivo por descuido.

Un modo preaprovisionado, para flotas que se aprovisionan solas, llegará con el aprovisionamiento de autoservicio.

### Cómo se tratan los secretos {#secrets}

`credentialValue` es de **solo escritura**. Se almacena únicamente como un resumen con clave, exactamente igual que al registrar una credencial por separado, y no aparece en ningún resultado, mensaje, línea de registro ni archivo de resultados. El archivo de **filas fallidas** nunca contiene una columna de secretos, así que un archivo corregido necesita que añadas de nuevo los secretos desde tu propia fuente. En la consola los secretos permanecen en la página solo mientras dura la importación y se borran al terminar. El archivo en sí sigue siendo responsabilidad tuya.

En una nueva ejecución, una fila cuyo dispositivo e id de credencial ya existen es `EXISTS`; el secreto **no se compara**, así que una contraseña distinta en una nueva ejecución no cambia nada. Para rotar una contraseña usa la actualización de la propia credencial.

## Resultados {#outcomes}

| Resultado | Significado |
| --- | --- |
| `CREATED` | Se creó el dispositivo (y su credencial). En una ejecución de prueba: se crearía. |
| `EXISTS` | El dispositivo ya existe exactamente como lo describe la fila. Es un éxito; no se escribió nada. |
| `CONFLICT` | El token existe con otro `deviceTypeToken`, `externalId`, `name`, `description` o `metadata`; o el id externo o el id de credencial pertenece a otro dispositivo; o el dispositivo existe sin la credencial que nombra la fila. No se escribió nada. |
| `INVALID` | La fila está mal formada o nombra algo que no existe: un token incorrecto, un tipo de dispositivo desconocido, un valor que supera su límite, JSON no válido, un token, id externo o id de credencial repetido en el mismo archivo, o un problema con la credencial. |
| `REFUSED` | La fila está bien formada y una regla la rechazó: un tipo de credencial no admitido. |

Todo resultado distinto de `CREATED` y `EXISTS` incluye un `code` y un `message` que nombran el campo. Un mensaje nunca contiene el secreto de una credencial; puede repetir un token que enviaste, como el token de un tipo de dispositivo. Una importación nunca añade una credencial a un dispositivo que ya existe; usa para eso la creación de credenciales.

## Por GraphQL {#graphql}

```graphql
mutation Import($rows: [DeviceImportRow!]!) {
  importDevices(rows: $rows, mode: NONE, dryRun: true) {
    created exists conflict invalid refused
    rows { row token outcome code message }
  }
}
```

```json
{ "rows": [
  { "token": "sensor-001", "deviceTypeToken": "thermostat", "name": "Boiler room" },
  { "token": "sensor-002", "deviceTypeToken": "thermostat", "externalId": "SN-20413" }
] }
```

- Una llamada admite **como máximo 1000 filas**; más hace fallar toda la llamada con `LIMIT_EXCEEDED`. Envía un archivo mayor como varias llamadas, una tras otra.
- `row` es la posición de la fila en la llamada, empezando en 1.
- `dryRun: true` decide cada fila con el mismo código que la llamada real y no escribe nada, así que una ejecución de prueba limpia es una vista previa fiel.
- Un fallo que no es de una fila (que la base de datos deje de estar disponible) hace fallar la llamada con un error. Las filas creadas antes son reales; envía de nuevo las mismas filas y se informan como `EXISTS`.
- Requiere la autoridad `device:write`, igual que crear un dispositivo.
