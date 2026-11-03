---
title: Por qué no aparecen los datos de mi dispositivo
---

# Por qué no aparecen los datos de mi dispositivo {#why}

Un dispositivo envía eventos y no aparece nada. Antes de leer el log de un pod, mire lo que la plataforma **rechazó**. Cuando descarta un evento, o no puede almacenarlo, guarda un registro breve de ello, y su tenant puede leerlo.

:::note Estado
Disponible. Abra **Fallos de ingesta** en la sección **Monitor** de la consola, o consulte `failedEvents` y `failedEventSummary` en la API GraphQL de event-management. Ambas requieren `device:write`, la autorización de los miembros que gestionan dispositivos y por tanto pueden actuar ante un rechazo, y no la base de solo lectura que tiene todo miembro. Tampoco se ofrecen a los miembros cuyo acceso se limita a algunos clientes: un rechazo suele nombrar un dispositivo que no existe, por lo que no pertenece a ningún cliente.
:::

## Qué está viendo {#what}

Cada fila es un evento rechazado, del más reciente al más antiguo:

| Columna | Significado |
| --- | --- |
| **Registrado** | Cuándo registró la plataforma el rechazo. Es el reloj de la plataforma, no el del dispositivo. |
| **Motivo** | Por qué. Vea la tabla siguiente. |
| **Token del dispositivo** | El token que **indicó el remitente**. Es lo que afirma el remitente, no una prueba de que el dispositivo exista: para un dispositivo desconocido no nombra nada de su registro. |
| **Lo que indicó la plataforma** | El texto de la propia plataforma, acortado a 512 bytes. |
| **Agrupados además** | Cuántos registros adicionales del mismo motivo y del mismo dispositivo se contaron en esta fila. Vea el [límite de inundación](#flood-bound). |

Las filas son un informe. **No son una cola**: no se reenvía nada, y los eventos originales no se almacenan en los datos de su tenant. Corrija la causa y los siguientes eventos del dispositivo se aceptan.

## Motivos y qué hacer {#reasons}

| Motivo | Qué ocurrió | Qué revisar |
| --- | --- | --- |
| **Dispositivo desconocido** (`DEVICE_NOT_FOUND`) | Ningún dispositivo tiene el token que indicó el remitente. | Registre el dispositivo o corrija el token que envía. Un token no es el nombre del dispositivo ni su credencial; vea [los tres identificadores](./connecting-a-device.md#three-identifiers). |
| **Credencial rechazada** (`UNAUTHENTICATED`) | La credencial que presentó el dispositivo faltaba o era incorrecta. | La [credencial](./device-credentials.md) del dispositivo: ¿se rotó, venció o nunca se configuró en el dispositivo? |
| **No es dispositivo de esta pasarela** (`NOT_GATEWAY_CHILD`) | Una pasarela publicó para un dispositivo que no le está asignado o que no existe. | La [asignación a la pasarela](./connecting-a-device.md) del dispositivo. |
| **Dispositivo deshabilitado** (`DEVICE_DISABLED`) | El dispositivo existe y ha sido deshabilitado. | Habilítelo, si no era lo previsto. |
| **Mensaje ilegible** (`INVALID`) | El mensaje no se pudo leer. | El formato de la carga que envía el dispositivo. |
| **Error de la plataforma** (`API_CALL_FAILED`) | No se pudo contactar un servicio de la plataforma que el evento necesitaba, o falló el almacenamiento del evento. | No se resuelve desde el dispositivo. Avise a su operador; el detalle está en los logs de la plataforma y no se le muestra. |
| **Alias de integración cambiado** (`INTEGRATION_ALIAS_MISMATCH`) | El evento de una integración nombró un dispositivo que ya no tiene el alias con el que se asoció. | El alias del dispositivo en la [integración](./inbound-integrations.md). |
| **Otro motivo** (`UNKNOWN`) | Un motivo que esta versión no nombra. | La fila lleva su código de motivo. Nunca se muestra como otro motivo. |

Si la tabla está vacía y los datos siguen sin aparecer, la plataforma no rechazó nada: revise la conexión del dispositivo, su [estado en vivo](../user-guide/devices.md) y si sus eventos llegan al broker.

## En la página de un dispositivo {#device-page}

Cuando se rechazaron eventos de un dispositivo en las últimas 24 horas, su página de detalle lo indica, con un enlace a los motivos de ese dispositivo.

## Desde la línea de comandos {#cli}

`dcctl events failed` lee los mismos registros como miembro del tenant, con el mismo requisito de `device:write`:

```bash
# los fallos del último día, contados por motivo, con los dispositivos que originan la mayoría
dcctl events failed --tenant acme --email ops@acme.example --password "$PASSWORD" --summary

# los rechazos de un dispositivo por un motivo, del más reciente al más antiguo, en JSON
dcctl events failed --tenant acme --email ops@acme.example --password "$PASSWORD" \
  --device sensor-17 --reason device-not-found --since 6h --output json
```

`--since` y `--until` aceptan una hora RFC 3339 o hace cuánto tiempo (`6h`). La plataforma lee como máximo 31 días atrás. Un listado se detiene en `--limit` registros (100 por defecto) e indica cuándo coincidieron más. `--summary` solo acepta `--since` (24 horas por defecto). Consulta la [referencia de comandos](../reference/dcctl.md#dcctl-events-failed). El comando no devuelve el evento original.

## El límite de inundación {#flood-bound}

Una flota con una sola credencial incorrecta puede ver rechazados miles de eventos por segundo. La lista sería inútil, y también el almacén que la respalda, si guardara cada uno. Por eso:

- se conservan como máximo **10 registros por hora** por dispositivo y motivo; cada registro adicional se **cuenta** en el más reciente de ellos (la columna **Agrupados además**);
- se detallan como máximo **1000 registros por hora** por motivo, entre todos los dispositivos. Un sondeo con tokens inventados queda acotado igual; el resto se cuenta en una sola fila marcada como más registros de ese motivo.

Los totales de los botones de motivo incluyen todo lo agrupado. Los contadores miden magnitud, no son una auditoría: los contadores se escriben aproximadamente una vez por segundo, así que durante una caída de la base de datos esperan en memoria y un reinicio de la plataforma antes de que se resuelva los pierde.

## Cuánto tiempo se conservan {#retention}

Siete días por omisión. Un operador lo cambia con `lifecycle.failedEventRetentionDays` en la configuración de event-management (`0` conserva los registros indefinidamente); es independiente de `lifecycle.retentionDays`, que rige la telemetría.

El evento original, cuando la plataforma archivó uno, permanece en el flujo de fallos de la plataforma solo hasta que el límite de tamaño de ese flujo lo descarta. Un registro cuyo original ya se descartó sigue en la lista e indica que el original ya no se conserva (`payloadStatus: EXPIRED` en la API). No es un error. Algunos registros nunca tuvieron un original: cuando la plataforma no pudo leer un mensaje, no conservó nada de él, y la consola indica «el original no se conservó» (`payloadStatus: NOT_RETAINED`). La API nunca devuelve el evento original.

## Al eliminar un tenant {#deletion}

Los registros se borran junto con el resto de los datos del tenant. Un tenant eliminado no deja ninguno.

## Para operadores {#operators}

`FailedEventsSustained` se dispara cuando un mismo motivo se rechaza a más de un registro por segundo durante 15 minutos. La serie que lo respalda cuenta todos los registros, incluidos los que el límite de inundación agrupó. Vea [Observabilidad](../deployment/observability.md) y la [referencia de métricas](../deployment/metrics-reference.md#event-management).
