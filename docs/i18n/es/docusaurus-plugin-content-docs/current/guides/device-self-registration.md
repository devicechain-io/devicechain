---
sidebar_position: 3.6
title: Autorregistro de dispositivos
---

# Autorregistro de dispositivos {#device-self-registration}

Crear cada dispositivo a mano, o importar un archivo con ellos, funciona cuando conoces la flota de antemano. El **autorregistro** es para el otro caso: una flota que sale de una línea de producción con un secreto compartido grabado, donde cada unidad se presenta sola la primera vez que se enciende. El dispositivo envía su número de serie por HTTPS, inventa su propia credencial y recibe su token de dispositivo y su id de cliente MQTT. No hace falta introducir nada del dispositivo de antemano, salvo que quieras hacerlo.

Esta página es la del operador: qué configurar, cómo poner en marcha una flota y qué hacer cuando algo sale mal. La petición exacta, las respuestas y los códigos de error, que necesitan quienes escriben firmware, están en la [referencia del protocolo de dispositivo](../reference/device-protocol.md#self-registration).

## Cómo encaja todo {#concept}

Un **perfil de aprovisionamiento** es el punto de entrada compartido de la flota. Contiene:

| Campo | Significado |
| --- | --- |
| Clave de aprovisionamiento | Un nombre que presenta la flota, único en el inquilino. No es secreta. |
| Secreto de aprovisionamiento | Lo genera el servidor (256 bits aleatorios), se **muestra una sola vez** al crear o rotar el perfil y se almacena solo como un resumen (digest). Grábalo en el firmware de la flota. |
| Estrategia | `ALLOW_NEW` crea un dispositivo que la plataforma no conocía y también adopta uno que reservaste. `CHECK_PRE_PROVISIONED` registra solo dispositivos que ya existen y se reservaron para el perfil. |
| Tipo de dispositivo | El tipo de todos los dispositivos que registra el perfil, y el único tipo que adoptará. |
| Máximo de dispositivos | Tope opcional de cuántos dispositivos registra el perfil. Sin él, el perfil no tiene tope. |
| Habilitado, caduca el | Un perfil deshabilitado o caducado rechaza de inmediato todos los registros. |

El dispositivo guarda **su propia credencial**: genera 32 bytes aleatorios, los codifica en base64url, los conserva y los envía en la petición de registro. La plataforma almacena un resumen, así que nadie, ni siquiera un administrador, puede leerla de vuelta. La credencial es un token de acceso: el dispositivo se conecta por MQTT con el nombre de usuario `{tenant}:{credential}` y sin contraseña, y con el id de cliente que indica la respuesta.

## Configurar un perfil {#setup}

1. **Crea el perfil** en la consola, en **Conectar → Perfiles de aprovisionamiento**, o con `createProvisioningProfile`. Elige el tipo de dispositivo, la estrategia, un tope opcional y, si la flota se entrega por lotes, una caducidad.
2. **Copia el secreto** del diálogo que aparece a continuación. Se muestra una sola vez. Si lo pierdes, rótalo; el anterior deja de funcionar al instante.
3. **Comprueba que el ingress de la API está habilitado.** El endpoint se sirve en la misma ruta `/api/device-management` que la API GraphQL. Con `ingress.enabled: false` la instancia no tiene camino público hasta él.
4. **Revisa la configuración de la dirección del cliente** si ejecutas tu propio controlador de ingress o un balanceador de carga de la nube: [Direcciones de cliente y proxies de confianza](../deployment/bootstrap.md#trusted-proxies). Un error ahí no impide el registro, pero todos los dispositivos pasan a compartir una sola asignación de peticiones.

### Rotación necesaria {#rotate-required}

Un perfil creado antes de que el servidor generara secretos tiene un secreto elegido por un operador. Se **rechaza** (`401 PROVISIONING_FAILED`, como cualquier secreto incorrecto) hasta que lo rotes, y la consola lo marca como **Requiere rotación**. Rotar emite un secreto nuevo, que se muestra una sola vez, y el anterior deja de funcionar al instante. Actualiza el firmware y después los dispositivos se registran con el nuevo.

## Reservar {#earmarking}

Una **reserva** (earmark) dice «este dispositivo existente puede ser adoptado por este perfil». Un perfil adopta un dispositivo solo cuando se reservó para él, con cualquiera de las dos estrategias. Es deliberado: sin ello, cualquiera que tuviera el secreto de la flota y el número de serie de un dispositivo creado por un operador podría apropiarse de él.

Reserva un dispositivo al crearlo:

- en la consola, con el campo **Perfil de aprovisionamiento esperado** del formulario del dispositivo;
- en una importación CSV, con la columna `provisioningProfileToken` ([columnas](./importing-devices.md#columns));
- por GraphQL, con `provisioningProfileToken` en `createDevice` o `importDevices`.

Una reserva solo se puede fijar al crear el dispositivo. Nada la fija en un dispositivo existente, así que un dispositivo creado antes de configurar el perfil no puede ser adoptado por él. El perfil debe existir en el inquilino y ser del tipo del dispositivo. La página de detalle de un dispositivo muestra cómo llegó a la plataforma y ofrece **Restablecer aprovisionamiento**; la lista de dispositivos se puede filtrar por el perfil que emitió su credencial o por el perfil para el que está reservado (`provisioningProfile` y `expectedProvisioningProfile` en `DeviceSearchCriteria`).

Eliminar un perfil no elimina las reservas: los dispositivos vuelven a no tener ninguna y, desde entonces, se rechazan (`DEVICE_NOT_EARMARKED`).

## Lo que ve un dispositivo {#what-a-device-sees}

Con un perfil cuya clave es `acme-sensors-2026` y un dispositivo con número de serie `SN-000123`, el firmware hace el equivalente de:

```bash
# una vez, en el dispositivo: crea la credencial y guárdala ANTES de enviar nada
openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n' > /data/credential

curl -sS https://dc.example.com/api/device-management/provisioning/acme/register \
  -H 'Content-Type: application/json' \
  -d '{
    "provisionKey": "acme-sensors-2026",
    "provisionSecret": "'"$PROVISION_SECRET"'",
    "externalId": "SN-000123",
    "credentialSecret": "'"$(cat /data/credential)"'"
  }'
```

```json
{"outcome":"CREATED","tenant":"acme","deviceToken":"pv-SN-000123-06e08144e3e8","externalId":"SN-000123","credentialType":"ACCESS_TOKEN","mqtt":{"clientId":"<instanceId>:acme:pv-SN-000123-06e08144e3e8"}}
```

Puedes probar todo el flujo sin escribir firmware. [`dcctl device register`](../reference/dcctl.md#dcctl-device-register) hace de dispositivo: genera la credencial, la guarda en el archivo indicado por `--material-file` **antes** de enviar nada, sigue las reglas de reintento e imprime el token del dispositivo y el id de cliente. Ejecútalo de nuevo y informa `REPLAYED`.

```bash
dcctl device register --server dc.example.com --tenant acme \
  --key acme-sensors-2026 --secret-file ./provision.secret \
  --external-id SN-000123 --material-file ./SN-000123.credential
```

Dos reglas importan más que el resto:

- **Guarda la credencial antes del primer envío.** La plataforma conserva solo un resumen. Un dispositivo que pierde su credencial no puede recuperarla registrándose de nuevo: presenta material nuevo para un dispositivo que ya tiene una credencial y se rechaza con `409 ALREADY_PROVISIONED`. Entonces un operador usa **Restablecer aprovisionamiento** para que pueda registrarse otra vez.
- **Reintenta con la misma credencial y espera.** Un registro cuya respuesta se perdió se reconoce como repetición (`200 REPLAYED`) y se puede repetir sin riesgo. Con `429` y `503`, espera el `Retry-After` más un jitter aleatorio; no bombardees. El [contrato de reintentos](../reference/device-protocol.md#self-registration-retry) enumera qué respuestas reintentar y cuáles requieren a una persona.

## Rotación y solapamiento {#rotation}

`rotateProvisioningProfileSecret` (el **Rotar secreto** de la consola) es un corte brusco: el secreto anterior deja de funcionar en la siguiente petición, sin solapamiento. Los dispositivos que ya se registraron no se ven afectados, porque se conectan con su propia credencial, no con el secreto de la flota.

Para una rotación que necesite solapamiento, por ejemplo mientras una fábrica sigue grabando el secreto anterior, crea un **segundo perfil** con su propia clave y secreto, y fija la caducidad del perfil antiguo.

## Si el secreto se filtra {#compromise}

El secreto es el ancla de confianza completa del primer contacto: quien lo tiene puede registrar un dispositivo que aún no se ha registrado. Qué significa eso depende de la estrategia. Con `ALLOW_NEW`, cualquier número de serie que nadie haya registrado todavía. Con `CHECK_PRE_PROVISIONED`, solo los números de serie que reservaste y que aún no se han registrado. El tope `maxDevices` limita ambos casos.

Para recuperarte:

1. **Rota el secreto**, para que el filtrado deje de funcionar.
2. **Revoca los dispositivos que emitió el perfil** si algunos no son tuyos (`revokeProvisionedDevices`, el **Revocar dispositivos** de la consola). Sus credenciales se deshabilitan y se cierran sus conexiones activas.
3. **Averigua qué se registró y cuándo.** Filtra la lista de dispositivos por el perfil (`provisioningProfile`) y mira la hora de creación de cada uno. Los registros también están en el [diario de auditoría](../user-guide/audit.md) como `device.provision.created` y `device.provision.adopted`, atribuidos a `provisioning-profile:<token>`. Una repetición se cuenta pero no se registra en el diario.
4. **Restablece los dispositivos que sí son tuyos** (`resetDeviceProvisioning`) para que puedan registrarse de nuevo con el secreto nuevo. Un dispositivo revocado sigue rechazado hasta que lo hagas.

## Deshabilita un dispositivo, no lo elimines {#disable-dont-delete}

Deshabilita una unidad que quieras mantener fuera. No la elimines. El número de serie de un dispositivo eliminado vuelve a ser desconocido, así que con `ALLOW_NEW` la siguiente unidad que lo presente se registra como un dispositivo nuevo.

## Eliminar un perfil y sus dispositivos {#deleting}

`deleteProvisioningProfile` se rechaza mientras algún dispositivo conserve una credencial que emitió el perfil, incluidas las revocadas: *«revoke the profile's devices first, then reset their provisioning or delete them»*. Así el registro del origen de la credencial de un dispositivo nunca apunta a la nada. Asimismo, un tipo de dispositivo no se puede eliminar mientras lo use un perfil de aprovisionamiento.

Bajar `maxDevices` por debajo del número ya registrado solo rechaza nuevos registros; no elimina nada.

## Operación {#operating}

- **Límites.** El registro tiene limitación de tasa por dirección de origen, por inquilino y por réplica, y cada réplica atiende como máximo 64 peticiones a la vez. Los valores y lo que responde cada uno están en [los límites](../reference/device-protocol.md#self-registration-limits). El techo por inquilino forma parte de la [gobernanza](../concepts/governance.md) del inquilino y se puede fijar en su nivel o por inquilino (`provisioningRegistrationsPerSecond` y `provisioningBurst`).
- **Métricas y alertas.** `provisioning_requests_total` cuenta cada petición por resultado, incluida la avalancha de `auth_failed` que produce un sondeo. Tres alertas la vigilan: [`ProvisioningAuthFailuresSustained`, `ProvisioningRateLimited` y `ProvisioningMisconfigured`](../deployment/observability.md#self-registration-alerts). Los contadores están en la [referencia de métricas](../deployment/metrics-reference.md).
- **Es solo HTTPS.** Un dispositivo no puede registrarse conectándose por MQTT.
- **No se emite MQTT_BASIC.** El autorregistro emite solo una credencial `ACCESS_TOKEN`. Un perfil cuyo tipo de credencial esta versión no puede emitir se rechaza con `501`.
