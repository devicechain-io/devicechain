---
sidebar_position: 7
title: Comandar una flota
---

# Comandar una flota

Un **lote de comandos** emite un solo comando hacia muchos dispositivos como una única
operación registrada. Los dispositivos se nombran explícitamente o se resuelven a partir de
un grupo de entidades, y lo que recibes de vuelta es un registro persistente de lo que la
plataforma intentó hacer — a cuántos dispositivos resolvió el objetivo, cuántos se admitieron
realmente, y cuáles fueron rechazados y por qué.

Todo lo que hace un comando individual sigue ocurriendo por dispositivo: cada uno se valida
contra el contrato de capacidades de ese dispositivo, se retiene si el dispositivo está
ausente, se sigue por el mismo ciclo de vida y vence con el mismo TTL. Lee primero [Enviar un
comando](./sending-commands.md) — esta guía solo cubre lo que cambia cuando el objetivo es
una flota.

Un bucle de llamadas a `createCommand` puede comandar los mismos dispositivos. Lo que no
puede hacer es dejar un registro de lo que se intentó, fijar la membresía del grupo para que
una edición del selector a mitad del bucle no cambie el objetivo, ni anularse como una sola
operación.

Los lotes viven en el endpoint de `command-delivery`,
`https://<tu-host>/api/command-delivery/graphql`, con un token de acceso de inquilino.
Disparar y cancelar requieren **`command:write`**; leer los registros de lote requiere
**`command:read`**.

:::warning Un objetivo de grupo requiere además `device:read`
Resolver un grupo hasta sus miembros es una lectura del registro de dispositivos que la
plataforma realiza bajo su propia identidad, y la respuesta te llega a ti — la lista de
rechazos nombra tokens de dispositivo, y `resolved` revela el tamaño del grupo. Así que
apuntar a un grupo, leer el registro de un lote dirigido a un grupo y cancelarlo requieren
**`device:read`** además de la autoridad de comandos. Nombrar dispositivos explícitamente
solo necesita la autoridad de comandos, porque quien lo hace ya los conoce.
:::

## Nombra el objetivo: dispositivos, o un grupo

`deviceTokens` y `groupToken` son alternativas. Suministra **exactamente uno** — ambos o
ninguno se rechaza con `BATCH_TARGET_AMBIGUOUS` en lugar de resolverse por una regla de
precedencia, porque quien envió ambos no sabe qué flota acaba de actuar.

**Nombrar dispositivos.** Como máximo **10 000** tokens en una petición; más es
`BATCH_TOO_LARGE` y tienes que dividir la operación. El orden es significativo — un lote
admitido parcialmente admite en el orden que diste, así que pon primero los dispositivos que
más te importan. Un token nombrado dos veces se cuenta una sola vez.

**Nombrar un grupo.** El grupo debe agrupar **dispositivos**, y un grupo dinámico debe estar
**publicado** — un lote resuelve el selector publicado, nunca el borrador, porque una
actuación sobre una flota no debe seguir lo que alguien tecleó por última vez en el editor.
Pasa `groupVersion` para fijar una versión congelada concreta, u omítelo para la publicada
activa. Nombrar una versión para un grupo estático se rechaza en lugar de ignorarse, igual
que nombrar una sin grupo alguno. Un grupo que resuelve a más de 10 000 dispositivos es
`BATCH_TOO_LARGE` — el recorrido se rechaza en lugar de comandar los primeros 10 000 e
informar éxito.

:::info Un objetivo de grupo queda congelado en el momento del disparo
El registro guarda la versión del grupo contra la que se resolvió el conjunto objetivo, así
que una auditoría puede responder qué *significaba* el grupo cuando el lote se disparó,
incluso después de que alguien edite el selector. Editar un grupo dinámico luego no cambia
nada de lo que ya salió. La versión guardada es nula para un grupo estático, que nunca se
versiona, y para un lote por lista de dispositivos. Consulta [Facetas y grupos
dinámicos](../concepts/domain-model.md#facets-and-dynamic-groups).
:::

## Decide qué significa una difusión parcial {#decide-what-a-partial-fan-out-means}

En una flota real algunos dispositivos no podrán recibir el comando — uno no está en el
registro, el perfil de otro no declara el comando, un tercero no cabe bajo el techo del
inquilino. `allowPartial` es donde dices qué debe ocurrir entonces:

- **`false`** — si *algún* dispositivo no puede recibir el comando, el lote entero se rechaza
  y **no se crea nada**, ni siquiera el registro del lote. No hay nada que registrar, porque
  no ocurrió nada. El rechazo nombra los dispositivos responsables.
- **`true`** — mejor esfuerzo. Los dispositivos que pueden recibir el comando lo reciben; el
  resto no obtiene fila de comando alguna y aparece en la lista de rechazos del registro.

:::warning `allowPartial` no tiene valor por defecto — tienes que enviarlo
Es un booleano no nulo sin valor predeterminado, así que una petición que lo omite es
inválida. Eso es deliberado para un campo que decide si una actuación física puede alcanzar a
una parte de la flota pero no a toda: declaras tu intención en lugar de heredarla de un
esquema que quizá no has leído.
:::

La bandera tiene un solo significado para todos los motivos de rechazo. No es una tolerancia
solo para problemas de capacidad — aceptarla también acepta que un dispositivo cuyo perfil
rechaza el comando quede fuera en silencio.

## Dispáralo

```graphql
mutation {
  createCommandBatch(request: {
    token: "nightly-reboot-2026-08-14",
    name: "reboot",              # el commandKey, no el nombre visible
    payload: "{\"delaySeconds\":5}",
    groupToken: "pumps-arid-us",
    allowPartial: true
  }) {
    batch {
      token targetKind groupToken groupVersion
      resolved accepted
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
    rejection {
      code reason resolved
      refusals { deviceToken code reason }
      refusalCounts { code count }
    }
  }
}
```

`name` es el **`commandKey`** del vocabulario del dispositivo, exactamente igual que en
`createCommand` — consulta [Averigua qué acepta el
dispositivo](./sending-commands.md#find-out-what-the-device-accepts). Cada dispositivo
objetivo recibe la misma clave y la misma carga útil, que es lo que hace asequible validar
una escritura de flota.

`expiresAt` fija el TTL de todos los comandos que crea el lote, o se aplica a todos ellos el
valor predeterminado de la plataforma: siete días. `metadata` se registra en el lote; no se
copia a los comandos individuales.

:::danger Revisa `rejection`, no solo si hay errores
`createCommandBatch` devuelve **exactamente uno** de `batch` o `rejection`. Un lote rechazado
es una respuesta GraphQL exitosa que lleva un `rejection` — no un error de GraphQL. Un error
de GraphQL en lugar de cualquiera de los dos significa que el lote no se pudo *decidir* en
absoluto, y no se creó nada, así que el token queda sin gastar y la petición se puede
reintentar sin más.
:::

### El token es una clave de idempotencia

`token` lo eliges tú y nombra la operación completa después. Volver a emitir un token que ya
nombra un lote devuelve **ese lote, sin cambios** — nunca se rellena con más dispositivos,
porque admitir más bajo el mismo token haría de `accepted` una cifra móvil y del registro
algo no auditable. Por eso un reintento tras un fallo de red es seguro, lo cual importa aquí
más que para un comando individual: la petición de la que dudas puede haber reiniciado diez
mil bombas.

No existe un rechazo `TOKEN_IN_USE` para un lote. Un token ya en uso no es un conflicto; es
una repetición.

## Cuando se rechaza un lote {#when-a-batch-is-refused}

**Ramifica según `code`. Nunca según `reason`** — la razón es prosa para una persona y su
redacción puede cambiar.

| `code` | Significado | ¿Reintentar? |
|---|---|---|
| `BATCH_PARTIAL_REFUSED` | Al menos un dispositivo no puede recibir el comando y `allowPartial` está desactivado. **No se creó nada.** | **Lee los rechazos** — el código propio de cada dispositivo dice si seguirá siendo rechazado la próxima vez |
| `HELD_CEILING_EXCEEDED` | El lote necesita más espacio del que el inquilino tiene para comandos **no entregados**. | **Sí** — se libera conforme se drena la acumulación |
| `BATCH_TARGET_AMBIGUOUS` | Se dieron ambos objetivos, o ninguno, o un `groupVersion` sin grupo. | No |
| `BATCH_TOO_LARGE` | Más dispositivos de los que un lote puede comandar — nombrados explícitamente, o resueltos del grupo. | No — divide la operación o acota el grupo |
| `BATCH_GROUP_UNUSABLE` | El grupo no existe, agrupa algo que no son dispositivos, nunca se publicó, o la versión nombrada no existe. El código propio del servicio de grupos viaja en la razón. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | La cadena no es JSON válido. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` no es una marca de tiempo RFC3339. | No |

**La lista es abierta.** Trata un código que no reconozcas como un rechazo que no puedes
clasificar — nunca como un éxito.

`BATCH_PARTIAL_REFUSED` es el único código que no puede responder por sí solo a la pregunta
del reintento, y por eso los dispositivos culpables viajan con él: un dispositivo que falta
en el vocabulario de comandos necesita un cambio de perfil, mientras que uno rechazado por
falta de espacio tendrá éxito cuando se drene la acumulación. Un solo código no puede decir
ambas cosas, así que no dice ninguna y delega en la lista.

:::info En un rechazo, `resolved` puede ser nulo — y nulo no es cero
`null` significa que nunca se estableció un conjunto objetivo: el rechazo ocurrió antes de
resolver nada. `0` significa un objetivo que genuinamente resolvió a ningún dispositivo, lo
cual es un lote real y exitoso, no un rechazo.
:::

La lista `refusals` del rechazo se rellena para exactamente un código,
`BATCH_PARTIAL_REFUSED`, y está vacía para todos los demás — incluido
`HELD_CEILING_EXCEEDED`. Esa asimetría es deliberada. Un rechazo parcial lo causan
dispositivos concretos, así que nombrarlos es lo que te evita bisecar una flota a mano. Un
rechazo por techo lo causa la acumulación del inquilino: ningún dispositivo de la petición
tiene la culpa, nada cambiaría si intercambiaras sus miembros, y una lista ahí invitaría a
arreglar dispositivos que están bien. Qué hacer al respecto está en `reason`.

## Lee el registro

```graphql
query {
  commandBatchesByToken(tokens: ["nightly-reboot-2026-08-14"]) {
    token name targetKind groupToken groupVersion allowPartial
    resolved accepted
    refusals { deviceToken code reason }
    refusalCounts { code count }
  }
}
```

O busca, por clave de comando, por grupo, o por `targetKind` (`DEVICE_LIST` o `GROUP`):

```graphql
query {
  commandBatches(criteria: {
    pageNumber: 1, pageSize: 25,
    groupToken: "pumps-arid-us"
  }) {
    results { token name resolved accepted createdAt }
    pagination { totalRecords }
  }
}
```

:::warning `resolved` y `accepted` describen el momento del disparo, no el presente
Son hechos almacenados, no cuentas en vivo. Las filas de comando no son inmortales —pueden
borrarse de forma lógica, o eliminarse junto con un inquilino— así que derivar `accepted` de
una consulta en vivo dejaría que derivase por debajo de la verdad del momento de creación sin
ningún rechazo que explique la diferencia. Para el estado de entrega en presente, busca los
comandos.
:::

### `refusals` es una muestra; `refusalCounts` es completo

`refusals` conserva como máximo **100 entradas por código**, así que un lote disparado contra
un grupo grande rechaza más dispositivos de los que el registro nombra. `refusalCounts` es el
total completo por código y nunca se trunca, que es lo que hace que el registro se audite
solo:

```
resolved = accepted + la suma de refusalCounts
```

Esa identidad siempre se cumple. La muestra puede quedarse corta, y comparar su longitud
contra los recuentos es cómo sabes que se acotó.

El `code` por dispositivo es el mismo vocabulario abierto que usa el rechazo de una admisión
individual — `DEVICE_NOT_FOUND`, `COMMAND_NOT_IN_VOCABULARY`, `PAYLOAD_SCHEMA_VIOLATION`
transmitido desde el perfil del dispositivo, y `HELD_CEILING_EXCEEDED` para los dispositivos
que no cupieron en el espacio restante del inquilino. Consulta [Cuando se rechaza una
admisión](./sending-commands.md#when-an-enqueue-is-refused) para saber qué significa cada
uno.

## Sigue los comandos que creó

El registro del lote deliberadamente no se mueve. Para preguntar qué está *haciendo* la
escritura de flota —«de los 5000 en cola, ¿cuántos han salido?»— busca los comandos con
`batchToken`:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    batchToken: "nightly-reboot-2026-08-14",
    statuses: ["QUEUED", "HELD"]
  }) {
    results { token deviceToken status queuedTime }
    pagination { totalRecords }
  }
}
```

Los tokens de los comandos individuales los genera la plataforma —elegiste el token del lote,
no los suyos— así que `batchToken` es como los encuentras, en lugar de construir un token tú
mismo.

## Anúlalo entero

```graphql
mutation {
  cancelCommandBatch(token: "nightly-reboot-2026-08-14") {
    cancelled
    alreadySent
    alreadyFinished
    matched
  }
}
```

`cancelled` es la cifra autoritativa: ese número de comandos pasó de `QUEUED` o `HELD` a
`CANCELLED` y no se entregará. Los `alreadySent` ya se habían entregado a sus dispositivos, y
**esos dispositivos actuarán igualmente sobre ellos**. Los `alreadyFinished` ya habían
alcanzado un estado terminal — `SUCCESSFUL`, `FAILED`, `TIMEOUT`, `EXPIRED` o `CANCELLED`.

:::warning Cancela solo `QUEUED` y `HELD` — `cancelCommand` también cancela `SENT`
No son el mismo freno, y vale la pena interiorizar la asimetría. `SENT` significa que el
comando ya está en el dispositivo: nada de lo que haga la plataforma ahora puede revocarlo,
así que «cancelarlo» no detiene ninguna actuación — solo hace que la plataforma deje de
esperar la respuesta del dispositivo, lo que convierte un desenlace real en un registro que
dice que el operador lo anuló. Un freno para toda la flota no debería hacerle eso a miles de
comandos que ya están fuera. `cancelCommand` se estrechará para que coincida con esto en un
cambio posterior. Hasta entonces, un bucle de `cancelCommand` sobre los comandos de un lote
**no** es la misma operación que esta.
:::

**Nunca se rechaza.** Un freno que se negara a actuar porque parte de la flota ya se había
movido dejaría comandado al resto de la flota, que es el peor desenlace disponible. Así que
un lote cuyos comandos ya se enviaron todos es una llamada exitosa que informa
`cancelled: 0` — lee las cifras en lugar de suponer que la llamada no hizo nada. Un token que
no corresponde a ningún lote **sí** es un error de GraphQL.

Cancelar necesita **`command:write`**, y un lote dirigido a un grupo necesita además
**`device:read`**, por la misma razón que dispararlo.

:::info `matched` es una cuenta en vivo, y las cuatro cifras no tienen por qué cuadrar
`matched` es cuántas de las filas de comando del lote estaban vivas en ese momento, no
cuántas creó. Las filas eliminadas desde entonces —por una purga, o por un borrado— no están
ahí para coincidir, así que un `matched` por debajo del `accepted` del lote es normal y no
dice nada sobre la cancelación.

`matched` también puede *superar* a `cancelled + alreadySent + alreadyFinished`. Un comando
cuya entrega falló puede volver a la cola entre la cancelación y el recuento, y ese comando
queda fuera de los tres grupos en lugar de contarse en `alreadyFinished` — informar de un
comando vivo como si hubiera terminado es justo lo que este vocabulario existe para evitar.
Cancela otra vez y quedará atrapado. Es raro y se corrige solo: una vez registrada una
cancelación, una entrega fallida retira el comando en lugar de volver a encolarlo.
:::

El registro del lote queda sellado con `cancelledAt` y `cancelledCount`, de modo que la
cancelación es tan auditable como lo fue la difusión. `cancelledCount` es lo que alcanzó esa
llamada, y el sello es de **primero que llega**: una segunda cancelación no sobrescribe lo
que registró la primera.

## Lo que un lote no cambia

Un lote queda acotado exactamente por los mismos límites que alcanzaría un bucle de comandos
individuales. Se admite contra el [techo del inquilino para comandos no
entregados](../concepts/commands.md#held-command-ceiling), menos la [parte reservada para la
entrega de la propia plataforma](../concepts/commands.md#delivery-machinery-reserve) — así
que no hay forma de eludir ninguno de los dos, ni ventaja de una forma sobre la otra.

Qué significa esto en la práctica: con `allowPartial` activado, una difusión grande puede
admitirse solo parcialmente porque el inquilino está cerca de su techo, y los dispositivos que
no cupieron vuelven como rechazos `HELD_CEILING_EXCEEDED` por dispositivo. Con él desactivado,
el lote entero se rechaza con ese código y no se crea nada. En ambos casos es una condición
temporal y no un defecto en la petición: una vez drenada la acumulación, un token **nuevo**
comandará al resto. Repetir el token original no puede, porque una repetición devuelve el lote
que ya tienes.
