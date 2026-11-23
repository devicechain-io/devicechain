---
sidebar_position: 3.8
title: Revisar un cambio de regla
---

# Revisar un cambio de regla {#reviewing-a-rule-change}

Usa el episodio Night Shift de Towerpulse para comparar la versión publicada de una regla con dos borradores. El primero corrige el incidente, pero solicita refrigeración durante un calentamiento normal. El segundo debería pasar ambas ventanas. Consulta [Revisión de cambios de automatización](../concepts/automation-change-review.md) para conocer el modelo y la garantía de protección.

:::note Esta función está en versión preliminar
Los tipos y las operaciones de GraphQL pueden cambiar en una versión menor. La protección es opcional: ningún perfil queda protegido tras una actualización y nada cambia para los perfiles sin protección.
:::

## Antes de empezar {#before-you-start}

**Reserva unas 2 h 42 min de grabación desde que empieza el episodio, además del tiempo de preparación y revisión.** Este tutorial registra telemetría en vivo. No hay un modo compatible de avance rápido ni de carga de datos con fechas pasadas para esta revisión. Para una primera prueba más breve, consulta [Tu primer dispositivo](../quickstart/first-device.md); vuelve aquí para la comparación completa.

Necesitas una instancia de DeviceChain en ejecución, `dcctl` y dos personas con membresía en el tenant que `dcctl sim create` crea para el simulador. Quien edita y ejecuta necesita `device:write` y `event:read` en su propia sesión sin restricciones de ámbito. Quien aprueba necesita esos permisos, una membresía sin restricciones de ámbito y autenticación de dos factores configurada. Leer revisiones también exige `device:read` y `event:read`.

Pide la aprobación a otra persona: por defecto, ni quien ejecuta ni los autores del borrador pueden aprobar. Para iniciar sesión en el simulador con un segundo factor, `dcctl sim` acepta `--otp`; consulta [autenticación de dos factores con dcctl](../guides/two-factor-authentication.md#dcctl).

## Registra el episodio Night Shift {#record-night-shift}

### Inicia la grabación {#start-recording}

Crea el tenant y el acceso del simulador:

```bash
dcctl sim create nightshift --manifest towerpulse --episode night-shift
```

Ejecuta el simulador en su propia terminal y mantenlo en primer plano:

```bash
dcctl sim run nightshift
```

### Mientras se graba el episodio {#while-recording}

Deja el simulador en ejecución. Explora los cinco dispositivos y su telemetría en la consola, prepara el borrador A descrito más abajo sin publicarlo y pide a la segunda persona que compruebe su membresía y la autenticación de dos factores. Lee las expectativas de revisión y los resultados esperados mientras se acumula el historial. Espera hasta unas 2 h 42 min después del inicio del episodio para ejecutar la revisión.

### Obtén las ventanas de revisión {#get-review-windows}

En otra terminal, obtén las horas de las ventanas del episodio:

```bash
dcctl sim windows nightshift
```

La salida tiene dos líneas separadas por tabuladores: rol, inicio y fin en RFC3339 UTC. Este es el formato, con marcadores:

```text
INCIDENT	<start RFC3339>	<end RFC3339>
NORMAL	<start RFC3339>	<end RFC3339>
```

Antes de iniciar el episodio, el comando te pide que ejecutes primero el simulador. El instante inicial se registra cuando empieza el episodio. INCIDENT va del minuto 45 al 95 desde ese instante; NORMAL, del 105 al 160. Mostrar las ventanas no significa que la grabación haya terminado: ejecuta la revisión solo cuando hayan pasado al menos dos minutos desde el final de NORMAL. Cambiar `--emit-interval` modifica la frecuencia de muestreo, no el tiempo transcurrido del episodio.

Los primeros 45 minutos permiten estabilizar el episodio. Durante INCIDENT, `tp-vav-tower-f1-south` se calienta y la regla incluida no lo detecta. Durante NORMAL, `tp-vav-tower-f3-north` tiene un breve calentamiento normal que no debe activar la refrigeración; `tp-vav-tower-f5-west` alberga una reunión como control.

Los cinco dispositivos son `tp-vav-tower-f1-north`, `tp-vav-tower-f1-south`, `tp-vav-tower-f3-north`, `tp-vav-tower-f3-south` y `tp-vav-tower-f5-west`. Consulta la referencia de [create](../reference/dcctl.md#dcctl-sim-create), [run](../reference/dcctl.md#dcctl-sim-run) y [windows](../reference/dcctl.md#dcctl-sim-windows).

## Cambia la regla: borrador A {#draft-a}

Abre las reglas de detección del borrador del perfil `tp-vav-profile`. Edita `tp-rule-zone-hot`, «Towerpulse zone hot while occupied». La regla publicada comprueba una media deslizante de cinco minutos de `temp_c` mayor que 26 mientras `occupancy > 0`. Activa `zone-hot` con severidad `major` y solicita `boost-cooling` con payload `{"minutes":15}`.

Para el borrador A, baja el umbral a 25.5 y conserva la ventana de cinco minutos. Cambia solo esta regla.

## Ejecuta una revisión {#run-a-review}

### En la consola {#run-in-console}

Abre **Change review** → **Run review**. Introduce el inicio y fin de ambas ventanas en hora local, convirtiendo las horas UTC de `dcctl sim windows`. Introduce los cinco tokens de dispositivo separados por comas o saltos de línea. **Let the server choose the rule** deja `ruleToken` vacío.

Añade estas dos expectativas. Elige activar una alarma para INCIDENT y no solicitar ningún comando para NORMAL. El formulario admite hasta diez expectativas en cuatro formas: activa una alarma, no activa ninguna alarma, solicita un comando y no solicita ningún comando.

```json
{"raised":{"rule":"tp-rule-zone-hot","device":"tp-vav-tower-f1-south","within":"30m"}}
```

```json
{"neverIntent":{"kind":"sendCommand","command":"boost-cooling","device":"tp-vav-tower-f3-north"}}
```

`within` empieza al inicio de su ventana. No uses `count` ni `allowDegraded`. Elige **Run review**.

### Mediante GraphQL {#run-over-graphql}

Envía las operaciones a `https://<your-host>/api/device-management/graphql` con tu token de acceso del tenant en `Authorization: Bearer`. Esta operación recibe la entrada como variable para que uses las horas reales registradas y tu propio `requestId` sin copiar una salida de ejemplo.

```graphql
mutation RunReview($input: RunAutomationReviewInput!) {
  runAutomationReview(input: $input) {
    token kind state outcome reviewDigest matchesCurrentDraft
    reasons profileChanges { area key change automation }
    fidelity { status reasons label }
    expectations {
      index name window baseline verdict baselineReason reason
      evidence { device at value spanSeconds }
    }
    windowResults {
      role start end leadInStart degraded changesTruncated
      counts { unchanged added removed shifted contentChanged }
      devices {
        device readings { key count } versions { key count }
        stateBase stateDraft divergence lateBase lateDraft
      }
      changes {
        kind device subject baseAt draftAt shiftSeconds contentChanged
        baseContent { severity payload configDigest bodyDigest }
        draftContent { severity payload configDigest bodyDigest }
        afterDivergence
      }
    }
  }
}
```

Pon `tp-vav-profile` en `input.profileToken`, los cinco dispositivos en `deviceTokens` y dos objetos con `role`, `start` y `end` en `windows`. Usa `INCIDENT` y `NORMAL` y las horas RFC3339 reales. Cada ventana debe ser positiva, durar como máximo 24 horas y terminar al menos dos minutos antes de ejecutar.

Pon en `expectations` dos objetos con `window` y `expect`: `expect` es el JSON correspondiente de arriba codificado como cadena JSON, no un objeto. `name` es opcional. Omite `ruleToken` para que lo seleccione el servidor o usa `tp-rule-zone-hot`. `shiftWindowSeconds` es opcional y equivale a 15 minutos por defecto.

Usa un `requestId` de entre 1 y 128 bytes imprimibles. Reintenta la misma entrada con el mismo ID para recuperar la misma revisión; usa un ID nuevo si cambia la entrada.

## Lee el resultado {#read-the-result}

Se espera que el borrador A sea `FAILED`. En INCIDENT, `baseline` falla y `verdict` pasa. En NORMAL, el borrador falla: solicitaría `boost-cooling` en `tp-vav-tower-f3-north` durante el calentamiento. El motivo nombra el comando, `{"minutes":15}` y el instante en que divergen los comandos. Son resultados esperados, no una salida real de ejemplo.

Lee `expectations` desde la versión activa (`baseline`) al borrador (`verdict`), y después los motivos y pruebas. En `windowResults`, compara `ADDED`, `REMOVED`, `SHIFTED` y `CONTENT_CHANGED` por dispositivo. Los recuentos están completos aunque `changesTruncated` indique que la lista supera los 2.000 cambios.

Comprueba la cobertura por métrica y versión del perfil, `stateBase`, `stateDraft`, `lateBase`, `lateDraft` y `divergence`. Lee `fidelity` y `degraded` antes de confiar en un resultado sin actividad. Las reglas no compatibles, el historial incompleto, los límites, el tiempo disponible, los registros corruptos, los errores de evaluación, las lecturas tardías rechazadas, la falta de lecturas, el estado inicial desconocido, una alarma ya activada para una expectativa de «nunca» o pruebas posteriores a la divergencia pueden producir `INCONCLUSIVE`. No se puede aprobar. Consulta [por qué se trata con rigor](../concepts/automation-change-review.md#why-inconclusive-is-strict).

## Prueba el borrador B {#draft-b}

Mantén el umbral en 25.5 y aumenta la ventana deslizante a 15 minutos. Ejecuta otra revisión con los mismos dispositivos, ventanas y expectativas y un `requestId` nuevo. Se espera `PASSED`: detecta la zona caliente sin pedir refrigeración durante el calentamiento normal.

Comprueba las pruebas reales. Para pasar, todas las expectativas del borrador deben pasar, cada ventana debe tener al menos una expectativa y la comparación debe estar completa, sin ventanas degradadas. Editar el borrador después de ejecutar deja obsoleta esa revisión; `matchesCurrentDraft` y el aviso de la consola lo indican.

## Aprueba la revisión {#approve-the-review}

Elige **Copy link** y dáselo a la segunda persona. La página es `/automation-reviews/<token>`. Esa persona la abre en su propia sesión, lee las pruebas y elige **Approve**. Debe ser distinta de quien ejecutó y de todas las personas que editaron el borrador, salvo que la política permita autoaprobación.

Quien aprueba necesita `device:write`, `event:read`, membresía sin restricciones de ámbito y un segundo factor verificado recientemente: en los últimos 15 minutos por defecto. La consola ofrece verificación adicional. Para clientes API, usa `stepUp(refreshToken, code)` de la API del tenant, descrito en la [referencia GraphQL](../reference/graphql-api.md).

```graphql
mutation ApproveReview($token: String!, $digest: String!) {
  approveAutomationReview(token: $token, digest: $digest) {
    token state requiredApprovals approvals { principal approvedAt policyRevision }
    approvedAt expiresAt
  }
}
```

Proporciona el `token` de la revisión y el `reviewDigest` exacto que esa persona examinó. Solo pueden aprobarse revisiones `RAN` con `PASSED` o `NO_AUTOMATION_CHANGE`. Si se exigen dos aprobaciones, deben aprobar dos personas distintas que cumplan los requisitos. **Reject** rechaza una revisión pendiente o aprobada, con nota opcional y sin verificación adicional.

La aprobación dura 24 horas por defecto. Comprueba `expiresAt`; una revisión aprobada caducada se muestra como `EXPIRED` al consultar y no cumple el requisito para publicar. Cerrar sesión no termina la aprobación, pero al publicar se comprueban de nuevo la membresía habilitada y los permisos de cada aprobador.

## Protege el perfil {#protect-the-profile}

En **Change review** → **Publish protection** → **Edit protection**, activa **Require an approved review to publish**. Configura **Required approvals**, **Allow self-approval**, **Approval lifetime (hours)** y **Second-factor maximum age (minutes)**, y elige **Save protection**. Los valores predeterminados son una aprobación, sin autoaprobación, 24 horas y 15 minutos. La protección exige un perfil publicado y reglas activas habilitadas elegibles.

También puedes usar la API:

```graphql
mutation ProtectProfile($profileToken: String!, $settings: AutomationReviewPolicyInput!) {
  setAutomationReviewPolicy(profileToken: $profileToken, settings: $settings) {
    applied
    policy {
      effective { required requiredApprovals allowSelfApproval approvalTtlSeconds stepUpMaxAgeSeconds }
      revision pendingChange { token effectiveAt }
    }
  }
}
```

Usa `required: true`. `requiredApprovals` es 1 o 2, `approvalTtlSeconds` va de 900 a 604800 y `stepUpMaxAgeSeconds` de 60 a 3600. Usa tu propia sesión sin restricciones de ámbito con `device:write`.

Endurecer se aplica al instante. Relajar espera a la aprobación de una segunda persona con segundo factor reciente; quien propone no puede aprobar. Si la política actual permite autoaprobación, el cambio también entra en vigor por sí solo tras 24 horas, aplicado por la siguiente operación protegida, salvo que una segunda persona lo apruebe antes. Usa **Cancel policy change** para cancelarlo. El perfil sigue protegido mientras esté pendiente. Si el endurecimiento hace insuficientes las aprobaciones existentes, obtén otra revisión y aprobaciones antes de publicar.

## Publica con la revisión {#publish-with-the-review}

Abre **Versions**, selecciona la revisión aprobada del borrador actual en **Approved review** y publica. Quien publica necesita `device:write` y no tiene que ser aprobador.

```graphql
mutation PublishReviewedProfile($token: String!, $automationReviewToken: String!) {
  publishDeviceProfile(token: $token, automationReviewToken: $automationReviewToken) {
    version automationReviewToken
    automationReview { token approvals { principal } }
  }
}
```

Usa el token del perfil y el de la revisión aprobada. Publicar consume esa revisión una sola vez. Un borrador o base distintos, pruebas caducadas o aprobaciones que ya no cumplan la política actual impiden publicar. En un perfil sin protección, `automationReviewToken` se ignora.

## Rastrea una versión hasta su revisión {#trace-a-version}

`DeviceProfileVersion.automationReviewToken` identifica la revisión consumida. `DeviceProfileVersion.automationReview` contiene las aprobaciones, expectativas, `inputDigest` y resultado. `consumedVersion` de la revisión apunta a la versión publicada.

```graphql
query ReadReview($token: String!) {
  automationReview(token: $token) {
    token profileToken baselineVersion state outcome consumedVersion
    reviewDigest inputDigest resultDigest
    approvals { principal approvedAt policyRevision }
    expectationInputs { window name expect }
    result
  }
}
```

Léelo con `device:read` y `event:read`, sin restricciones de ámbito. Las ejecuciones, aprobaciones y rechazos también están en el diario de auditoría.

## Usa una acción de emergencia {#break-glass}

Para revertir un perfil protegido, proporciona un motivo no vacío de hasta 512 caracteres desde tu propia sesión con `device:write` y un segundo factor verificado dentro de la antigüedad máxima del perfil.

```graphql
mutation RollBackProtectedProfile($token: String!, $version: Int!, $reason: String!) {
  rollbackDeviceProfile(token: $token, version: $version, breakGlassReason: $reason) {
    token
  }
}
```

En la consola, **Change a protected profile** pide el motivo y después un segundo factor si hace falta. Los mismos requisitos se aplican al redirigir un tipo de dispositivo hacia o desde un perfil protegido (`updateDeviceType`) y al sacar un dispositivo de un tipo vinculado (`updateDevice`). Cada operación acepta `breakGlassReason`.

Lee **Break-glass record** o `automationReviewBreakGlass(profileToken)` con `device:read`, sin restricciones de ámbito. Registra acción, destino, motivo, actor y hora. Una reversión hace fallar las aprobaciones pendientes con `REVIEW_STALE_BASELINE_CHANGED`; ejecuta y aprueba otra revisión. Se rechaza revertir a reglas no elegibles.

## Qué puede fallar {#what-can-go-wrong}

Los rechazos en tiempo de ejecución no almacenan una revisión. Una revisión `INCONCLUSIVE` almacena pruebas que no pudieron demostrar que el cambio es seguro.

| Rechazo | Qué hacer |
| --- | --- |
| `REVIEW_REFUSED` | Lee el rechazo y corrige la entrada, como una regla no válida. |
| `REVIEW_UNAVAILABLE` | Restablece el servicio de comparación o completa la actualización; publicar con protección también exige poder comprobar los requisitos de los aprobadores. |
| `NO_BASELINE`, `PROFILE_NOT_PUBLISHED` | Publica el perfil antes de revisar o activar la protección. |
| `RATE_LIMITED` | Espera a que haya menos de 60 revisiones en la última hora. |
| `CONFLICT` | Usa un `requestId` nuevo para entradas distintas. |
| `NOT_APPROVABLE`, `REVIEW_NOT_PENDING` | Comprueba resultado y estado; solo puedes aprobar revisiones `RAN` elegibles. |
| `SECOND_FACTOR_REQUIRED`, `STEP_UP_REQUIRED` | Usa tu propia sesión con segundo factor o completa la verificación adicional. |
| `APPROVER_IS_AN_AGENT`, `SERVICE_TOKEN_REFUSED`, `NOT_A_PERSON`, `SCOPED_GRANT_NOT_SUPPORTED` | Usa la sesión propia de una persona en el tenant, sin restricciones de ámbito. |
| `SELF_APPROVAL_REFUSED` | Pídeselo a otra persona que cumpla los requisitos. Quien propone un cambio de política nunca puede aprobarlo. |
| `CONCURRENT_APPROVAL` | Recarga después de la otra aprobación. |
| `REVIEW_STALE_DRAFT_CHANGED`, `REVIEW_STALE_BASELINE_CHANGED` | Ejecuta y aprueba otra revisión del borrador y la base actuales. |
| `DIGEST_MISMATCH` | Recarga y comprueba las pruebas y el resumen exactos; las pruebas almacenadas deben verificarse. |
| `REVIEW_REQUIRED`, `REVIEW_NOT_FOUND`, `REVIEW_WRONG_PROFILE` | Selecciona una revisión aprobada del borrador actual de este perfil. |
| `REVIEW_NOT_APPROVED`, `REVIEW_ALREADY_USED`, `REVIEW_EXPIRED` | Obtén una revisión aprobada vigente y sin usar. |
| `REVIEW_STALE_POLICY_CHANGED` | Obtén aprobaciones que cumplan la política de protección actual. |
| `APPROVER_NO_LONGER_QUALIFIES` | Obtén aprobación de personas que sigan cumpliendo los requisitos al publicar. |
| `PROTECTION_INELIGIBLE` | Lee los motivos y elimina las dependencias de reglas no elegibles. |
| `INVALID_POLICY` | Usa los rangos admitidos de aprobaciones, duración y segundo factor. |
| `BREAK_GLASS_REASON_REQUIRED` | Proporciona un motivo no vacío de hasta 512 caracteres. |
| `PROFILE_PROTECTED` | Quita la protección antes de eliminar; relajar espera aprobación o el plazo. |

## Páginas relacionadas {#related-pages}

- [Revisión de cambios de automatización](../concepts/automation-change-review.md)
- [Probar tus reglas](../guides/testing-your-rules.md)
- [Autenticación de dos factores](../guides/two-factor-authentication.md)
- [API GraphQL](../reference/graphql-api.md)
