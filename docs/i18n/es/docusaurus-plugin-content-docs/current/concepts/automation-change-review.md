---
sidebar_position: 4.1
title: Revisión de cambios de automatización
---

# Revisión de cambios de automatización {#automation-change-review}

Una revisión de cambios de automatización compara una regla de detección publicada con su borrador usando eventos de dispositivos registrados. Eliges un incidente que el cambio debe corregir y un periodo de funcionamiento normal que no debe alterar. Un perfil protegido exige una revisión aprobada antes de publicar. Sigue [Revisar un cambio de regla](../guides/reviewing-a-rule-change.md) para probarlo.

:::note Esta función está en versión preliminar
Los tipos y las operaciones de GraphQL pueden cambiar en una versión menor. La protección es opcional: ningún perfil queda protegido tras una actualización y nada cambia para los perfiles sin protección.
:::

## Qué compara una revisión {#what-a-review-compares}

Una revisión compara la versión activa (publicada) y el borrador de una regla de detección en un perfil de dispositivo, para un conjunto explícito de dispositivos. Debes proporcionar exactamente una ventana INCIDENT y una NORMAL.

La plataforma lee una sola vez los eventos resueltos registrados de cada ventana, incluidos los de todas las versiones del perfil registradas en ella. Su propio evaluador de detección se ejecuta dos veces sobre esa misma lista, una por versión de la regla. Ambas partes reciben entradas idénticas.

No se ejecuta nada: no se activa ninguna alarma, no se envía ningún comando y ningún webhook ni mensaje sale de la plataforma. La revisión registra las transiciones de alarma y las solicitudes de comandos o conectores como **intenciones**: lo que pediría la regla.

Cada ventana enumera los cambios por dispositivo:

| Tipo | Significado |
| --- | --- |
| `ADDED` | Solo lo hace el borrador. |
| `REMOVED` | Solo lo hace la regla activa. |
| `SHIFTED` | Ambas lo hacen, en momentos distintos. La tolerancia de emparejamiento es de 15 minutos por defecto; cámbiala con `shiftWindowSeconds`. |
| `CONTENT_CHANGED` | Ambas lo hacen, pero difiere el payload del comando, la severidad de la alarma o el resumen del cuerpo/configuración de la solicitud al conector. |

Los recuentos (`unchanged`, `added`, `removed`, `shifted`, `contentChanged`) están completos antes del truncamiento. La lista contiene como máximo 2.000 cambios por ventana; `changesTruncated` indica si hay más.

La cobertura muestra lecturas por métrica y versión del perfil, el estado de cada parte al inicio (`UNKNOWN`, `CLEAR`, `RAISED`), cuándo divergen por primera vez los comandos y las lecturas rechazadas por llegar tarde en cada parte.

Antes de cada ventana, el motor lee un historial previo para establecer el estado: el horizonte de la regla más cinco minutos, con un máximo de seis horas. El horizonte es de 30 minutos para una regla de umbral, su tiempo de permanencia para una regla de duración y su ventana para una regla de agregación.

## Tipos de cambio {#kinds-of-change}

La plataforma clasifica el borrador frente a la versión activa.

| `kind` | Qué cambió |
| --- | --- |
| `RULE_CHANGE` | Cambió exactamente la definición, el indicador de habilitación o el ámbito de grupo de una regla, sin cambiar nada que una regla lea o envíe. Esa regla se reproduce. |
| `NO_AUTOMATION_CHANGE` | No cambió ninguna regla ni nada que una regla lea o envíe. No se reproduce nada; el resultado es `NO_AUTOMATION_CHANGE` y puede aprobarse como `PASSED`. Por ejemplo, un cambio solo de descripción o del decodificador cuando no hay reglas habilitadas. |
| `UNREVIEWABLE` | Cambiaron dos o más reglas, o una métrica o comando que una regla lee o envía. El resultado es `INCONCLUSIVE`; `reasons` enumera cada cambio. Divide el trabajo en una regla por revisión. |

Mientras haya alguna regla habilitada en cualquiera de las versiones, añadir o quitar cualquier métrica, cambiar cualquier cosa salvo sus etiquetas o cambiar el decodificador también cuenta como cambio de automatización, aunque ninguna regla nombre esa métrica. Un evento de medición se valida como un todo y el decodificador produce los valores que leen las reglas.

Las demás ediciones aparecen en `profileChanges` con `automation: false`. Al aprobar, también las aceptas. El servidor encuentra la regla modificada si omites `ruleToken`; si lo proporcionas, debe nombrar esa regla.

## Resultados {#outcomes}

`outcome` describe las pruebas; `state` describe el ciclo de vida de la revisión.

| Resultado | Significado |
| --- | --- |
| `PASSED` | Todas las expectativas del borrador son `PASS`, ambas ventanas tienen al menos una expectativa, ninguna está degradada y nada hace incompleta la comparación. Sin expectativas nunca se obtiene `PASSED`. |
| `FAILED` | Al menos una expectativa es `FAIL` y ninguna es `INCONCLUSIVE`. |
| `INCONCLUSIVE` | Cualquier otro caso. No se puede aprobar. |
| `NO_AUTOMATION_CHANGE` | No hay cambios de automatización que reproducir. Se puede aprobar. |

### Por qué la falta de conclusión se trata con rigor {#why-inconclusive-is-strict}

Una revisión que no puede demostrar que el borrador es seguro nunca pasa. Comprueba estas causas:

- Alguna regla no puede reproducirse con la telemetría registrada: `fidelity.status` es `UNSUPPORTED`, con motivos, y no se calcula ninguna diferencia.
- Una ventana está degradada: su historial previo o inicio ya no está retenido en el flujo, se alcanzó un límite de lectura, exploración o tiempo, hay registros corruptos o falló la evaluación. Consulta `degraded`.
- Un dispositivo revisado tiene lecturas tardías rechazadas, no tiene lecturas de una métrica que lee la regla o empieza con estado `UNKNOWN` porque no hay pruebas en el historial previo.
- Una expectativa de «nunca» empieza con su alarma ya activada.
- Las pruebas corresponden a un momento posterior a la divergencia de comandos. El comando del borrador cambiaría lo que haría después el dispositivo, por lo que las lecturas posteriores ya no describen el mundo del borrador.

`fidelity.label` muestra «replay of recorded inputs».

## Qué se puede reproducir {#what-can-be-replayed}

Se pueden reproducir reglas de umbral, duración y agregación con ventanas fijas consecutivas o deslizantes. Las acciones admitidas son activar alarma, enviar comando, llamada HTTP y publicar en un conector.

Lo siguiente produce `fidelity.status: UNSUPPORTED`, resultado `INCONCLUSIVE` y ninguna diferencia: reglas de tasa de variación, repetición, ausencia, correlación y conectividad; agregaciones con ventanas de sesión o recuento; reglas que leen atributos de dispositivo o posición de geocerca; correlación por anclas de relación; reglas con ámbito de grupo; métricas calculadas cuyas fórmulas leen constantes de dispositivo; y cualquier otro tipo de acción.

## Expectativas {#expectations}

Usa hasta diez expectativas con el vocabulario de las [pruebas de reglas](../guides/testing-your-rules.md). Asigna cada una a INCIDENT o NORMAL. La consola ofrece cuatro formas: activa una alarma (`raised`), no activa ninguna alarma (`neverRaised`), solicita un comando (`intent`, tipo `sendCommand`) y no solicita ningún comando (`neverIntent`, tipo `sendCommand`).

En GraphQL, `expect` es una cadena JSON de hasta 8.192 bytes. `within` cuenta desde el inicio de la ventana. Una expectativa de revisión no puede usar `count` ni `allowDegraded`, y cualquier dispositivo nombrado debe formar parte de la revisión.

Cada expectativa tiene un veredicto de la regla activa (`baseline`) y otro del borrador (`verdict`): `PASS`, `FAIL` o `INCONCLUSIVE`. Lee `baselineReason`, `reason` y `evidence` para ver la explicación y las lecturas que la respaldan.

## Aprobaciones {#approvals}

Solo puedes aprobar una revisión `RAN` con resultado `PASSED` o `NO_AUTOMATION_CHANGE`. `approveAutomationReview(token, digest)` recibe el `reviewDigest` mostrado a quien aprueba, para que esa persona apruebe exactamente las pruebas almacenadas.

Usa tu propia sesión de usuario en el tenant, sin restricciones de ámbito, con `device:write` y `event:read`. Se rechazan tokens de agentes, tokens de servicio, cuentas de servicio y miembros limitados a parte del tenant. Debes haber verificado tu segundo factor dentro de la antigüedad máxima del perfil, 15 minutos por defecto. La consola ofrece la verificación adicional; los clientes API usan la [verificación adicional dentro de la sesión en la referencia GraphQL](../reference/graphql-api.md).

Por defecto, ni quien ejecutó la revisión ni ningún autor del borrador puede aprobar. Los autores son las personas que editaron el borrador del perfil, incluidas métricas, comandos y reglas, desde que se publicó la versión base, según el diario de auditoría. En caso de duda, la plataforma excluye a la persona. Una política puede permitir la autoaprobación.

Por defecto basta una aprobación; la política puede exigir dos personas distintas. Sin autoaprobación, ninguna puede ser la creadora ni autora del borrador. La aprobación registra la persona, los métodos de autenticación, la hora del segundo factor, la hora de aprobación y la revisión de la política.

La aprobación se evalúa con el token de la sesión propia de quien aprueba en ese momento. Cerrar la sesión después no la termina. Al publicar, cada aprobador debe conservar una membresía habilitada, sin restricciones de ámbito, con `device:write` y `event:read`; de lo contrario se rechaza con `APPROVER_NO_LONGER_QUALIFIES`.

El ciclo es `RAN` → `APPROVED` cuando aprueban suficientes personas → `CONSUMED` al publicar. `consumedVersion` identifica la versión. Puedes rechazar una revisión `RAN` o `APPROVED` con `rejectAutomationReview(token, note)`, sin verificación adicional. Las ejecuciones, aprobaciones y rechazos se registran con la persona como actor.

La aprobación dura 24 horas por defecto. Las lecturas y los filtros de estado muestran `EXPIRED` cuando vence el `expiresAt` almacenado o la duración de la política actual, incluido el instante exacto de caducidad. Al publicar se aplica la misma regla y se rechazan las pruebas caducadas con `REVIEW_EXPIRED`. La caducidad se calcula al consultar, sin tarea en segundo plano ni actualización del registro; las revisiones pendientes, rechazadas y consumidas conservan su estado.

Al aprobar también se comprueba que coincidan el borrador, la base y las pruebas almacenadas. `matchesCurrentDraft` indica si el borrador sigue siendo el revisado; la consola muestra un aviso cuando cambia. Comparte `/automation-reviews/<token>` con cualquiera que tenga permiso para leer revisiones.

## Perfiles protegidos {#protected-profiles}

La protección exige un perfil publicado. En un perfil protegido, `publishDeviceProfile` debe incluir `automationReviewToken` de una revisión aprobada de exactamente el borrador actual. Quien publica necesita `device:write` y no tiene que ser aprobador. Cada revisión se consume en una sola publicación. En perfiles sin protección, el token se ignora y se publica como antes.

Lee `automationReviewPolicy(profileToken)` para ver `effective`, `revision` y `pendingChange`. Usa `setAutomationReviewPolicy(profileToken, settings)` para cambiar `required`, `requiredApprovals`, `allowSelfApproval`, `approvalTtlSeconds` y `stepUpMaxAgeSeconds`; devuelve `applied` y `policy`.

Endurecer la protección se aplica al instante: activarla, exigir más aprobaciones, impedir la autoaprobación o acortar la duración o la antigüedad del segundo factor. Relajarla espera como `policy.pendingChange` con `applied: false`. Una segunda persona aprueba mediante `approveAutomationReviewPolicyChange(token)`, con su propia sesión sin restricciones de ámbito, `device:write` y un segundo factor reciente. Quien propone nunca puede aprobar su propio cambio.

Si la política actual permite autoaprobación, la relajación también entra en vigor por sí sola 24 horas después, en `effectiveAt`, aplicada por la siguiente operación protegida, salvo que una segunda persona la apruebe antes. En ambos casos puedes cancelarla con `cancelAutomationReviewPolicyChange(token)`. El perfil sigue protegido mientras el cambio esté pendiente.

La pestaña **Change review** incluye **Publish protection**, **Edit protection**, la aprobación de cambios pendientes, **Cancel policy change** y **Break-glass record**. La pestaña **Versions** ofrece el selector **Approved review** para el borrador actual.

### La garantía {#the-guarantee}

En un perfil protegido, las reglas que ejecutan los dispositivos y los tipos de dispositivo vinculados al perfil solo cambian mediante una publicación revisada y aprobada o una acción de emergencia registrada. Esto cubre publicar, revertir, redirigir un tipo de dispositivo hacia o desde el perfil, sacar un dispositivo de un tipo vinculado y eliminar el perfil.

Las versiones publicadas de un perfil protegido no pueden cambiarse. Solo se eliminan junto con el perfil; ninguna operación elimina una versión por separado. Eliminar un perfil protegido se rechaza, por lo que su historial de versiones se conserva mientras la protección esté activa.

La base de datos lo exige a todos los clientes, incluidos los pods de servicio de la versión anterior durante una actualización progresiva. Las instrucciones que cambiarían lo que ejecutan los dispositivos de un perfil protegido se rechazan en vez de aplicarse.

### Qué no cubre la protección {#what-protection-does-not-cover}

La protección no cubre crear, habilitar, deshabilitar o eliminar dispositivos; añadir dispositivos a un tipo vinculado; crear un tipo nuevo vinculado al perfil o eliminar uno vacío; atributos y metadatos de dispositivos; ni pertenencia a grupos de entidades. Por eso las reglas protegidas no pueden depender de atributos, constantes, grupos, anclas ni geocercas.

## Elegibilidad {#eligibility}

Para activar la protección, todas las reglas habilitadas de la versión activa deben ser elegibles. El borrador publicado con protección también debe superar la comprobación. Ninguna regla habilitada puede leer atributos de dispositivo, leer una métrica calculada cuya fórmula lea constantes de dispositivo, tener ámbito de grupo, correlacionar dispositivos mediante anclas de relación ni leer posición de geocerca.

Esas entradas pueden cambiar el comportamiento sin publicar, por lo que la telemetría registrada no respalda la garantía. La comprobación es conservadora: mencionar una entrada así, o que la comprobación no pueda leer una regla, cuenta como usarla. La protección no elegible se rechaza con `PROTECTION_INELIGIBLE` y motivos.

## Acciones de emergencia {#break-glass}

Tres operaciones exigen `breakGlassReason` en perfiles protegidos: `rollbackDeviceProfile`, `updateDeviceType` al redirigir un tipo hacia o desde el perfil, y `updateDevice` al sacar un dispositivo de un tipo vinculado.

Usa la sesión propia de una persona con `device:write`, un motivo no vacío de hasta 512 caracteres y un segundo factor verificado dentro de la antigüedad máxima del perfil. La consola reintenta mediante **Change a protected profile**, solicitando el motivo y después un segundo factor si hace falta.

La plataforma registra acción (`ROLLBACK`, `REPOINT_TYPE`, `MOVE_DEVICE`), destino, motivo, actor y hora en la misma transacción, y lo anota en el diario. Consulta `automationReviewBreakGlass(profileToken)` para ver hasta los 200 registros más recientes, o abre **Break-glass record**.

Una reversión mueve la base. Las aprobaciones pendientes fallan con `REVIEW_STALE_BASELINE_CHANGED`; ejecuta y aprueba otra revisión. Se rechaza una reversión de emergencia a una versión con reglas no elegibles.

## Eliminar un perfil {#deleting-a-profile}

Eliminar un perfil protegido se rechaza con `PROFILE_PROTECTED`. Quita primero la protección; esa relajación espera a una segunda persona o al plazo. Eliminar un perfil sin protección elimina su política, los cambios de política pendientes y las revisiones sin consumir, y conserva las revisiones consumidas y los registros de emergencia como historial.

## Límites {#limits}

| Elemento | Límite o valor predeterminado |
| --- | --- |
| Dispositivos | Entre 1 y 50 distintos. |
| Ventanas | Exactamente una INCIDENT y una NORMAL, cada una con duración positiva. Cuando se reproduce un cambio de regla, cada ventana dura como máximo 24 horas y termina al menos dos minutos antes de ejecutar. |
| Historial previo automático | Hasta seis horas. |
| Expectativas | Hasta 10; cada una de hasta 8.192 bytes. |
| Cambios enumerados | Hasta 2.000 por ventana; los recuentos están completos. |
| Ejecuciones | Hasta 60 por tenant en la última hora. |
| `requestId` | Entre 1 y 128 bytes imprimibles. Repetir entradas idénticas devuelve la misma revisión sin contar otra ejecución; entradas distintas con el mismo ID dan `CONFLICT`. |
| Aprobaciones exigidas | 1 por defecto; 1 o 2. |
| Autoaprobación | Desactivada por defecto. |
| Duración de la aprobación | 86.400 segundos por defecto; entre 900 y 604.800 segundos (15 minutos–7 días). |
| Antigüedad del segundo factor | 900 segundos por defecto; entre 60 y 3.600 segundos. |
| Nota de rechazo | Hasta 512 bytes. |
| Motivo de emergencia | No vacío; hasta 512 caracteres. |

## Permisos necesarios {#required-permissions}

Una sesión sin restricciones de ámbito corresponde a una membresía que no está limitada a parte del tenant.

| Acción | Requisitos |
| --- | --- |
| Ejecutar | `device:write` + `event:read`; sesión propia de una persona sin restricciones de ámbito. |
| Leer (`automationReview`, `automationReviews`, enlace compartido) | `device:read` + `event:read`, sin restricciones de ámbito. Las revisiones contienen lecturas de dispositivos. |
| Aprobar | `device:write` + `event:read`; sesión propia sin restricciones de ámbito, segundo factor reciente, no ser creador ni autor salvo que la política permita autoaprobación. |
| Rechazar | `device:write` + `event:read`; sesión propia sin restricciones de ámbito. |
| Leer protección o registro de emergencia | `device:read`, sin restricciones de ámbito. |
| Cambiar protección o cancelar un cambio pendiente | `device:write`; sesión propia sin restricciones de ámbito. |
| Aprobar una relajación pendiente | `device:write`; sesión propia sin restricciones de ámbito, segundo factor reciente, no ser proponente. |
| Publicar un perfil protegido | `device:write` y una revisión aprobada. |
| Acción de emergencia | `device:write`; sesión propia, motivo, segundo factor reciente. |

## Actualizar {#upgrading}

En una actualización progresiva, actualiza user-management antes de device-management: publicar con protección devuelve `REVIEW_UNAVAILABLE` mientras user-management sea anterior. Activa la protección solo después del despliegue. Si event-processing es anterior a device-management, ejecutar una revisión devuelve `REVIEW_UNAVAILABLE` hasta actualizar event-processing.

## Páginas relacionadas {#related-pages}

- [Revisar un cambio de regla](../guides/reviewing-a-rule-change.md)
- [Probar tus reglas](../guides/testing-your-rules.md)
- [Procesamiento de eventos](../concepts/event-processing.md)
- [Autenticación de dos factores](../guides/two-factor-authentication.md)
- [API GraphQL](../reference/graphql-api.md)
