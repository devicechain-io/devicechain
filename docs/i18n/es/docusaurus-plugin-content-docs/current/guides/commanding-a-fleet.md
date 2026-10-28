---
sidebar_position: 7
title: Comandar una flota
---

# Comandar una flota

Un **lote de comandos** envía un solo comando a muchos dispositivos como una única operación
registrada. Puedes nombrar los dispositivos o dejar que la plataforma los resuelva a partir de un
grupo de entidades. Lo que recibes de vuelta es un registro persistente de lo que la plataforma
intentó hacer: a cuántos dispositivos resolvió el objetivo, cuántos se encolaron realmente y cuáles
fueron rechazados y por qué.

Cada dispositivo se sigue tratando exactamente igual que con un comando individual. Su comando se
valida contra el contrato de capacidades de ese dispositivo, se retiene si el dispositivo está
ausente, se sigue por el mismo ciclo de vida y vence con el mismo TTL. Lee primero [Enviar un
comando](./sending-commands.md); esta guía solo cubre lo que cambia cuando el objetivo es una flota.

Un bucle de llamadas a `createCommand` puede comandar los mismos dispositivos. Lo que no puede hacer
es:

- dejar un registro de lo que se intentó;
- fijar la membresía del grupo, para que una edición del selector a mitad del bucle no cambie el
  objetivo;
- anularse como una sola operación.

Los lotes viven en el endpoint de `command-delivery`,
`https://<tu-host>/api/command-delivery/graphql`, y usan un token de acceso de inquilino. Disparar y
cancelar un lote requieren `command:write`. Leer los registros de lote requiere `command:read`.

:::warning Un objetivo de grupo requiere además `device:read`
Resolver un grupo hasta sus miembros lee el registro de dispositivos bajo la propia identidad de la
plataforma, y la respuesta te llega a ti: la lista de rechazos nombra tokens de dispositivo, y
`resolved` revela el tamaño del grupo. Por eso apuntar a un grupo, leer el registro de un lote
dirigido a un grupo y cancelarlo requieren cada uno **`device:read`** además de la autoridad de
comandos. Nombrar dispositivos explícitamente solo necesita la autoridad de comandos, porque quien lo
hace ya los conoce.
:::

Para lanzar y seguir un lote desde la consola web, consulta [Comandos a una flota](../user-guide/commanding-a-fleet.md) en la guía del usuario.

## Elige el objetivo: dispositivos o un grupo {#name-the-target-devices-or-a-group}

`deviceTokens` y `groupToken` son alternativas, y debes suministrar **exactamente uno**. Ambos o
ninguno se rechaza con `BATCH_TARGET_AMBIGUOUS` en lugar de resolverse por una regla de precedencia,
porque quien envió ambos no sabe qué flota acaba de actuar.

### Nombrar dispositivos {#naming-devices}

- Puedes nombrar como máximo 10 000 tokens en una petición. Más es `BATCH_TOO_LARGE`, y tienes que
  dividir la operación.
- El orden importa. Un lote admitido parcialmente admite los dispositivos en el orden que diste, así
  que pon primero los que más te importan.
- Un token nombrado dos veces se cuenta una sola vez.

### Nombrar un grupo {#naming-a-group}

- El grupo debe agrupar dispositivos.
- Un grupo dinámico debe estar publicado. Un lote resuelve el selector publicado, nunca el borrador,
  porque una actuación sobre una flota no debe seguir lo que alguien tecleó por última vez en el
  editor.
- Pasa `groupVersion` para fijar una versión congelada concreta, u omítelo para usar la publicada
  activa. Nombrar una versión para un grupo estático se rechaza en lugar de ignorarse, igual que
  nombrar una sin grupo alguno.
- Un grupo que resuelve a más de 10 000 dispositivos es `BATCH_TOO_LARGE`. La plataforma lo rechaza
  en lugar de comandar los primeros 10 000 e informar éxito.

El registro guarda la versión del grupo contra la que se resolvió el conjunto objetivo. Así, una
auditoría puede responder qué *significaba* el grupo cuando se disparó el lote, incluso después de
que alguien edite el selector. La versión guardada es nula para un grupo estático, que nunca se
versiona, y para un lote por lista de dispositivos.

:::note Un objetivo de grupo queda congelado en el momento del disparo
Editar un grupo dinámico después de disparar un lote no cambia nada de lo que ya salió. Consulta
[Facetas y grupos dinámicos](../concepts/domain-model.md#facets-and-dynamic-groups).
:::

## Decide qué significa una difusión parcial {#decide-what-a-partial-fan-out-means}

En una flota real, algunos dispositivos no podrán recibir el comando: uno no está en el registro, el
perfil de otro no declara el comando, un tercero no cabe bajo el techo del inquilino. `allowPartial`
dice qué ocurre entonces:

| `allowPartial` | Si algún dispositivo no puede recibir el comando |
|---|---|
| `false` | El lote entero se rechaza y **no se crea nada**, ni siquiera el registro del lote: no ocurrió nada, así que no hay nada que registrar. El rechazo nombra los dispositivos responsables. |
| `true` | Mejor esfuerzo. Los dispositivos que pueden recibir el comando lo reciben. El resto no obtiene fila de comando alguna y aparece en la lista de rechazos del registro. |

La bandera tiene un solo significado para todos los motivos de rechazo. No es una tolerancia solo
para problemas de capacidad: activarla también acepta que un dispositivo cuyo perfil rechaza el
comando quede fuera en silencio.

:::warning `allowPartial` no tiene valor por defecto — tienes que enviarlo
Es un booleano no nulo sin valor predeterminado, así que una petición que lo omite es inválida. Este
campo decide si una actuación física puede alcanzar a una parte de la flota pero no a toda, así que
declaras tu intención en lugar de heredarla de un esquema que quizá no has leído.
:::

## Dispara el lote {#fire-it}

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

`name` es el `commandKey` del vocabulario del dispositivo, exactamente igual que en `createCommand`;
consulta [`commandKey` es el identificador](./sending-commands.md#find-out-what-the-device-accepts).
Cada dispositivo objetivo recibe la misma clave y la misma carga útil, que es lo que hace asequible
validar una escritura de flota en primer lugar.

`expiresAt` fija el TTL de todos los comandos que crea el lote. Sin él, se aplica a todos el valor
predeterminado de la plataforma: siete días. `metadata` se registra en el registro del lote; no se
copia a los comandos individuales.

:::danger Revisa `rejection`, no solo si hay errores
`createCommandBatch` devuelve **exactamente uno** de `batch` o `rejection`. Un lote rechazado es una
respuesta GraphQL exitosa que lleva un `rejection`, no un error de GraphQL. Un error de GraphQL en
lugar de cualquiera de los dos significa que el lote no se pudo *decidir* en absoluto: no se creó
nada, el token queda sin gastar y puedes reintentar la petición.
:::

### El token es una clave de idempotencia

`token` lo eliges tú, y después nombra la operación completa. Volver a emitir un token que ya nombra
un lote devuelve **ese lote, sin cambios**. Nunca se rellena con más dispositivos, porque admitir más
bajo el mismo token haría de `accepted` una cifra móvil y del registro algo no auditable.

Por eso un reintento tras un fallo de red es seguro. Eso importa aquí más que para un comando
individual: la petición de la que dudas puede haber reiniciado diez mil bombas.

No existe un rechazo `TOKEN_IN_USE` para un lote. Un token ya en uso no es un conflicto; es una
repetición.

## Cuando se rechaza un lote {#when-a-batch-is-refused}

Ramifica según `code`, **nunca según `reason`**. La razón es prosa para una persona, y su redacción
puede cambiar.

| `code` | Significado | ¿Reintentar? |
|---|---|---|
| `BATCH_PARTIAL_REFUSED` | Al menos un dispositivo no puede recibir el comando y `allowPartial` está desactivado. No se creó nada. | Lee los rechazos: el código propio de cada dispositivo dice si seguirá siendo rechazado la próxima vez |
| `HELD_CEILING_EXCEEDED` | El lote necesita más espacio del que el inquilino tiene para comandos no entregados. | Sí; se libera conforme se drena la acumulación |
| `BATCH_TARGET_AMBIGUOUS` | Se dieron ambos objetivos, o ninguno, o un `groupVersion` sin grupo. | No |
| `BATCH_TOO_LARGE` | Más dispositivos de los que un lote puede comandar, nombrados explícitamente o resueltos del grupo. | No; divide la operación o acota el grupo |
| `BATCH_GROUP_UNUSABLE` | El grupo no existe, agrupa algo que no son dispositivos, nunca se publicó o la versión nombrada no existe (o se nombró una versión para un grupo estático). El código propio del servicio de grupos viaja en la razón. | No |
| `PAYLOAD_NOT_JSON` / `METADATA_NOT_JSON` | La cadena no es JSON válido. | No |
| `PAYLOAD_TOO_LARGE` / `METADATA_TOO_LARGE` | La cadena de carga útil o de metadatos supera los 64 KiB. | No |
| `COMMAND_NAME_TOO_LONG` | El nombre del comando supera los 128 bytes. | No |
| `EXPIRES_AT_INVALID` | `expiresAt` no es una marca de tiempo RFC3339. | No |

La lista es abierta. Trata un código que no reconozcas como un rechazo que no puedes clasificar,
**nunca como un éxito**.

`BATCH_PARTIAL_REFUSED` es el único código que no puede responder por sí solo a la pregunta del
reintento, y por eso los dispositivos culpables viajan con él. Un dispositivo que falta en el
vocabulario de comandos necesita un cambio de perfil, mientras que uno rechazado por falta de espacio
tendrá éxito cuando se drene la acumulación. Un solo código no puede decir ambas cosas, así que no
dice ninguna y delega en la lista.

La lista `refusals` del rechazo se rellena para exactamente un código, `BATCH_PARTIAL_REFUSED`, y
está vacía para todos los demás, incluido `HELD_CEILING_EXCEEDED`. La asimetría es deliberada:

- Un rechazo parcial lo causan dispositivos concretos, así que nombrarlos te evita bisecar una flota a
  mano.
- Un rechazo por techo lo causa la acumulación del inquilino. Ningún dispositivo de la petición tiene
  la culpa, y nada cambiaría si intercambiaras sus miembros; una lista ahí invitaría a arreglar
  dispositivos que están bien. Qué hacer al respecto está en `reason`.

:::note En un rechazo, `resolved` puede ser nulo — y nulo no es cero
`null` significa que nunca se estableció un conjunto objetivo: el rechazo ocurrió antes de resolver
nada. `0` significa un objetivo que realmente resolvió a ningún dispositivo, lo cual es un lote real
y exitoso, no un rechazo.
:::

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

También puedes buscar por clave de comando, por grupo o por `targetKind` (`DEVICE_LIST` o `GROUP`):

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
Son hechos almacenados, no cuentas en vivo. Para el estado de entrega en presente, busca los comandos
(consulta [Sigue los comandos que creó](#follow-the-commands-it-created)).
:::

Las filas de comando no son inmortales: pueden borrarse de forma lógica, o eliminarse junto con un
inquilino. Derivar `accepted` de una consulta en vivo dejaría que bajara de la verdad del momento de
creación sin ningún rechazo que explicara la diferencia, y por eso el registro lo almacena.

### `refusals` es una muestra; `refusalCounts` es completo

`refusals` conserva como máximo **100 entradas por código**, así que un lote disparado contra un
grupo grande rechaza más dispositivos de los que el registro nombra. `refusalCounts` es el total
completo por código y nunca se trunca, lo que hace que el registro se audite solo:

```
resolved = accepted + la suma de refusalCounts
```

Esa identidad siempre se cumple. La muestra puede quedarse corta; compara su longitud con los
recuentos para saber si se acotó.

El `code` por dispositivo usa el mismo vocabulario abierto que el rechazo de una admisión individual:

- `DEVICE_NOT_FOUND`
- `DEVICE_DISABLED`
- `COMMAND_NOT_IN_VOCABULARY`
- `PAYLOAD_SCHEMA_VIOLATION`, transmitido desde el perfil del dispositivo
- `HELD_CEILING_EXCEEDED`, para los dispositivos que no cupieron en el espacio restante del inquilino
- `WINDOW_AFTER_EXPIRY`, para los dispositivos cuya [ventana de mantenimiento](./maintenance-windows.md#window-after-expiry) se abre después de que caduque el comando

Consulta [Cuando se rechaza una admisión](./sending-commands.md#when-an-enqueue-is-refused) para
saber qué significa cada uno.

## Sigue los comandos que creó {#follow-the-commands-it-created}

El registro del lote deliberadamente no se mueve. Para preguntar qué está *haciendo* la escritura de
flota («de los 5000 en cola, ¿cuántos han salido?»), busca los comandos con `batchToken`:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 50,
    batchToken: "nightly-reboot-2026-08-14",
    statuses: ["QUEUED", "HELD", "PARKED"]
  }) {
    results { token deviceToken status queuedTime }
    pagination { totalRecords }
  }
}
```

La plataforma genera los tokens de los comandos individuales; elegiste el token del lote, no los
suyos. Por eso los encuentras con `batchToken` en lugar de construir un token tú mismo.

El vínculo también funciona en sentido contrario. Una fila de comando lleva `batchToken` como campo
legible, así que quien tenga un solo comando (del historial de un dispositivo, o de una respuesta que
llegó sin contexto) puede preguntar qué escritura de flota lo creó:

```graphql
query {
  commands(criteria: { pageNumber: 1, pageSize: 20, deviceToken: "gw-4471" }) {
    results { token name status batchToken }
  }
}
```

`batchToken` es nulo para un comando emitido de uno en uno, y es el único campo que distingue los dos
casos. Un lote envía la misma clave de comando, con la misma carga útil, que el dispositivo habría
recibido individualmente, así que nada más en la fila difiere.

Esta dirección importa porque una sola fila de comando no puede mostrarte la parte interesante de una
escritura de flota: los dispositivos que *rechazó*. No se les dio ningún comando, así que no aparecen
en el historial de ningún dispositivo. Solo el registro del lote sabe que fueron seleccionados.

En la consola, la página de detalle de un lote muestra estos comandos y tiene un botón **Exportar CSV**.
Exporta todos los comandos que cumplen el filtro de estado vigente, no solo la página visible, hasta
10 000 filas; si el lote es mayor, la consola lo indica y acotas el filtro para obtener el resto.

## Reintenta los dispositivos que no tuvieron éxito {#retry-the-devices-that-did-not-succeed}

Una escritura de flota que falla en parte no debería obligarle a reconstruir a mano la lista de
dispositivos. Cada comando que termina lleva un `outcome`, registrado por la escritura que lo
terminó, y el reintento se selecciona por ese resultado y no leyendo el texto del error:

| `outcome` | Qué ocurrió | Enviarlo de nuevo |
|---|---|---|
| `SUCCEEDED` | El dispositivo informó éxito. | No se puede reintentar. |
| `NOT_DELIVERED` | La plataforma puede demostrar que el comando nunca llegó al dispositivo: caducó o se canceló mientras la plataforma aún lo tenía, el transporte no tiene vía de comandos o la plataforma dejó de intentar publicarlo. | No puede actuar dos veces. |
| `REJECTED` | El dispositivo recibió el comando y respondió que no lo ejecutó. | Volver a pedirlo, de forma deliberada. |
| `UNKNOWN` | El comando puede haber llegado al dispositivo y haber actuado: se envió y nunca se respondió (`TIMEOUT`), o el dispositivo respondió y la plataforma perdió la respuesta. | Puede actuar dos veces. Solo con reconocimiento explícito. |

Un comando que no ha terminado no tiene resultado. El `status` por sí solo no lo da, porque `FAILED`
abarca nunca entregado, rechazado y desconocido. Busque por resultado:

```graphql
query {
  commands(criteria: {
    pageNumber: 1, pageSize: 100,
    batchToken: "nightly-reboot-2026-08-14",
    outcomes: ["NOT_DELIVERED", "REJECTED"]
  }) {
    results { deviceToken status outcome error }
  }
}
```

`retryCommandBatch` vuelve a emitir el comando del lote original a los dispositivos que nombre, como un
**lote nuevo** vinculado al original:

```graphql
mutation {
  retryCommandBatch(request: {
    token: "nightly-reboot-2026-08-14-retry-1",
    of: "nightly-reboot-2026-08-14",
    deviceTokens: ["gw-4471", "gw-4472"],
    includeUnknown: false,
    allowPartial: true
  }) {
    batch { token retryOf resolved accepted }
    rejection { code reason refusals { deviceToken code reason } }
  }
}
```

- **El comando no va en la solicitud.** La plataforma copia el nombre y la carga del original, de modo
  que un reintento no puede convertirse en otro comando.
- **Cada dispositivo debe ser miembro del lote original** y no haber tenido éxito: `NOT_DELIVERED` o
  `REJECTED`. Un dispositivo cuyo comando tuvo éxito o sigue en curso se rechaza
  (`RETRY_NOT_ELIGIBLE`), uno que no estaba en el lote se rechaza (`RETRY_NOT_A_MEMBER`), y también se
  rechaza uno que ya tiene un comando activo o exitoso en otro lote de la misma familia de reintentos
  (el original, cualquier reintento suyo y cualquier reintento de estos), de modo que un dispositivo
  nunca recibe el comando dos veces por haber dos reintentos abiertos a la vez.
- **`UNKNOWN` exige `includeUnknown: true`.** Sin él, esos dispositivos se rechazan con
  `RETRY_UNKNOWN_NOT_ACKNOWLEDGED`. Actívelo solo tras confirmar que actuar dos veces es inocuo.
- **Los dispositivos que el original rechazó** solo se pueden reintentar cuando el registro del lote
  conserva ese rechazo. El registro guarda como máximo 100 dispositivos rechazados por código
  (`refusalCounts` es exacto), así que un dispositivo de un conjunto mayor se rechaza con
  `RETRY_NOT_A_MEMBER`.
- **Un reintento pasa por las mismas comprobaciones que un lote nuevo**: el vocabulario actual de
  comandos del dispositivo, un dispositivo deshabilitado, un comando que la plataforma se niega a
  enviar en bruto, el techo de comandos retenidos y `allowPartial`. Reintentar no es una forma de
  eludir ninguna.
- **La idempotencia funciona en ambos sentidos.** El reintento tiene su propio `token`; reenviarlo
  devuelve el reintento y no envía nada más. Enviar el token *original* a `createCommandBatch` sigue
  devolviendo el lote original y no ejecuta nada. Un token que ya nombra un lote que no es un
  reintento de este se rechaza (`RETRY_TOKEN_IN_USE`).
- **El vínculo está en el lote**: `retryOf` nombra el lote que un reintento vuelve a intentar, y
  `retries` en el original lista los lotes que lo reintentaron. Un reintento de un reintento nombra al
  reintento.

Reintentar requiere `command:write`, y un original dirigido a un grupo requiere además `device:read`.

En la consola, la página de detalle de un lote tiene la acción **Reintentar…**. Lista los dispositivos
no satisfactorios en grupos: *nunca entregado* y *rechazado por el dispositivo* vienen seleccionados,
*rechazado al lanzar* se ofrece (cuando el registro lo conserva) pero no se selecciona, y *resultado
desconocido* no se selecciona y permanece bloqueado hasta que marque un reconocimiento de que esos
dispositivos pueden haber actuado ya. Los comandos aún en curso se cuentan y no se pueden seleccionar.
El lote nuevo se abre al crearse, y ambos lotes se enlazan entre sí.

## Cancela un lote {#call-the-whole-thing-off}

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

| Campo | Significado |
|---|---|
| `cancelled` | La cifra autoritativa. Ese número de comandos pasó de `QUEUED`, `HELD` o `PARKED` a `CANCELLED` y no se entregará. |
| `alreadySent` | Comandos ya despachados a sus dispositivos. Esos dispositivos **actuarán igualmente sobre ellos**. |
| `alreadyFinished` | Comandos que ya habían alcanzado un estado terminal: `SUCCESSFUL`, `FAILED`, `TIMEOUT`, `EXPIRED` o `CANCELLED`. |
| `matched` | Cuántas de las filas de comando del lote estaban vivas en ese momento (consulta [más abajo](#matched-and-the-other-counts)). |

Este es el mismo freno que `cancelCommand` aplica a un comando individual: ambos cancelan `QUEUED`,
`HELD` y `PARKED`, y ninguno toca `SENT`. Por qué `SENT` es la línea se explica en [Cancelar un
lote](../concepts/commands.md#cancelling-a-batch).

Cancelar **nunca se rechaza**. Un freno que se negara a actuar porque parte de la flota ya se había
movido dejaría comandado al resto de la flota, que es el peor desenlace posible. Así que un lote cuyos
comandos ya se enviaron todos es una llamada exitosa que informa `cancelled: 0`. Lee las cifras en
lugar de suponer que la llamada no hizo nada. Un token que no corresponde a ningún lote *sí* es un
error de GraphQL.

Cancelar necesita `command:write`, y un lote dirigido a un grupo necesita además `device:read`, por
la misma razón que dispararlo.

El propio registro del lote queda sellado con `cancelledAt` y `cancelledCount`, de modo que la
cancelación es tan auditable como lo fue la difusión. `cancelledCount` es lo que alcanzó esa llamada.
El sello es de **primero que llega**: una segunda cancelación no sobrescribe lo que registró la
primera.

### `matched` y las demás cifras {#matched-and-the-other-counts}

`matched` es una cuenta en vivo, y las cuatro cifras no tienen por qué cuadrar.

`matched` cuenta las filas de comando del lote que estaban vivas en ese momento, no cuántas creó el
lote. Las filas eliminadas desde entonces (por una purga, o por un borrado) no están ahí para
coincidir. Así que un `matched` por debajo del `accepted` del lote es normal y no dice nada sobre la
cancelación.

`matched` también puede *superar* a `cancelled + alreadySent + alreadyFinished`. Un comando cuya
entrega falló puede volver a la cola entre la cancelación y el recuento. Ese comando queda fuera de
los tres grupos en lugar de contarse en `alreadyFinished`, porque informar de un comando vivo como si
hubiera terminado es justo lo que este vocabulario existe para evitar. Cancela otra vez y quedará
atrapado.

Lo que hace que esto sea raro es el propio sello de cancelación: una vez confirmada una cancelación,
una entrega fallida retira el comando en lugar de devolverlo a la cola. La excepción es un comando
liberado en el mismo instante que la cancelación, que sigue vivo dentro de un lote ya anulado. El
remedio es cancelar otra vez, no esperar.

## Límites que un lote comparte con los comandos individuales {#what-a-batch-does-not-change}

Un lote queda acotado exactamente por los mismos límites que alcanzaría un bucle de comandos
individuales. Se admite contra el [techo del inquilino para comandos no
entregados](../concepts/commands.md#held-command-ceiling), menos la [parte reservada para la entrega
de la propia plataforma](../concepts/commands.md#delivery-machinery-reserve). No hay forma de eludir
ninguno de los dos, ni ventaja de una forma sobre la otra.

En la práctica, cuando el inquilino está cerca de su techo:

- Con `allowPartial` activado, una difusión grande puede admitirse solo parcialmente. Los
  dispositivos que no cupieron vuelven como rechazos `HELD_CEILING_EXCEEDED` por dispositivo.
- Con él desactivado, el lote entero se rechaza con ese código y no se crea nada.

En ambos casos es una condición temporal y no un defecto en la petición. Una vez drenada la
acumulación, un token **nuevo** comandará al resto. Repetir el token original no puede, porque una
repetición devuelve el lote que ya tienes.
