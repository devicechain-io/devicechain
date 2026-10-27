---
title: Autenticación en dos pasos
---

# Autenticación en dos pasos

La autenticación en dos pasos añade un segundo paso al inicio de sesión con contraseña: un código de seis dígitos de una aplicación de autenticación en el teléfono de la persona. Está integrada en DeviceChain, así que no necesita un proveedor de identidad ni ningún servicio adicional.

Esta guía tiene tres partes: [para las personas](#enrol) que quieren activarla en su propia cuenta, [para los administradores de un inquilino](#require) que quieren exigirla y [para los operadores](#operators) que la restablecen, se recuperan de un dispositivo perdido y vigilan los ataques contra ella. Para ver dónde encaja entre las demás protecciones de la plataforma, lee [el modelo de seguridad](../concepts/security-model.md#two-factor) antes o después.

## Qué cubre {#scope}

- **Un factor por cuenta, en todos los inquilinos.** Una cuenta (una identidad) es global: pertenece a la instancia, y sus pertenencias a inquilinos son independientes. Por eso la autenticación en dos pasos pertenece a la cuenta y se aplica en todos los inquilinos a los que la persona puede entrar.
- **Solo los inicios de sesión con contraseña.** Un inicio de sesión mediante el [inicio de sesión único](../concepts/single-sign-on.md) no la usa, porque el proveedor de identidad ya ha comprobado quién es la persona. Las cuentas de servicio y los dispositivos tampoco la usan nunca. Consulta [inicio de sesión único y dos pasos](#sso).
- **Una aplicación de autenticación y códigos de recuperación.** La aplicación genera un código de seis dígitos que cambia cada 30 segundos (el estándar de contraseñas de un solo uso basadas en el tiempo, RFC 6238, con SHA-1 y una ventana de un intervalo a cada lado del momento actual). Diez códigos de recuperación de un solo uso cubren la pérdida del teléfono.
- **Los relojes de los nodos deben estar en hora.** Un código solo es válido durante el intervalo de 30 segundos en que se generó, más uno a cada lado, así que un nodo de servicio cuyo reloj se haya desviado más de unos 30 segundos rechaza códigos correctos. Ejecuta NTP (o el servicio horario de la nube) en cada nodo.

## Actívala en tu cuenta {#enrol}

Abre el menú de usuario y elige **Seguridad de la cuenta**.

1. Elige **Configurar la autenticación en dos pasos** e introduce tu contraseña. La consola la pide de nuevo porque un token de sesión puede viajar, y configurar el factor exige algo más que un token.
2. Escanea el código QR con una aplicación de autenticación (sirve cualquiera que admita códigos basados en el tiempo), o escribe la clave que aparece en **¿No puedes escanear? Introduce esta clave**. El código QR se dibuja en tu navegador a partir de un secreto que solo se muestra ahora; DeviceChain lo guarda cifrado y nunca lo devuelve. La aplicación etiqueta la cuenta con el nombre de la instancia o, si no tiene nombre, con su nombre de host.
3. Introduce el código de seis dígitos que muestra la aplicación y elige **Activar**. Hasta que lo hagas, nada cambia en cómo inicias sesión. Una configuración sin confirmar caduca a los diez minutos, y cinco códigos incorrectos la cancelan.
4. La consola muestra diez **códigos de recuperación**, una sola vez. Cópialos o descárgalos y guárdalos en un lugar que no sea el teléfono. Cada uno inicia sesión una vez, en lugar de un código de la aplicación. Tienen este aspecto: `ABCD-EFGH-JKMN-PQRS`.

Al activarla **terminan todas las demás sesiones de tu cuenta**, estén donde estén, de modo que una sesión iniciada por alguien que solo tenía tu contraseña no sobreviva. La consola cambia tu sesión por una nueva, así que sigues trabajando sin volver a iniciar sesión.

La misma página muestra si está activada, cuándo se configuró y cuántos códigos de recuperación quedan. También permite:

- **Generar códigos de recuperación nuevos.** Sustituye los diez, estén usados o no: los anteriores dejan de funcionar. Exige tu contraseña y un código (de la aplicación o un código de recuperación sin usar). Cuando quedan pocos, la página lo avisa.
- **Desactivar los dos pasos.** Exige tu contraseña y un código, elimina el factor y los códigos y **termina todas las sesiones de tu cuenta, incluida esta**. Vuelves a iniciar sesión solo con la contraseña.

## Iniciar sesión {#sign-in}

Inicia sesión como siempre. Cuando la contraseña es correcta y la cuenta tiene un segundo factor, la consola pide un código en lugar de dejarte entrar:

- Introduce el código de seis dígitos de la aplicación. Un código sirve **una sola vez**. Si acabas de usar uno, espera a que la aplicación muestre el siguiente.
- O elige **Usar un código de recuperación** e introduce uno. Escríbelo como quieras: en mayúsculas o no, con o sin guiones o espacios. Se perdonan los caracteres parecidos: la letra O se lee como cero, y la letra I o la L como uno. Cada código de recuperación sirve una sola vez.

El paso del código caduca a los cinco minutos y admite cinco intentos. Después, vuelve a iniciar sesión. El mismo paso aparece allí donde hay un inicio de sesión con contraseña: la consola, la aplicación de paneles (`/dash`), la aceptación de una invitación y la página a la que te lleva la conexión [MCP](../concepts/mcp.md) de un agente de IA. La aplicación de paneles y la página MCP pueden pedir el código, pero no pueden configurarlo: te indican que la configures en **Seguridad de la cuenta** en la consola.

Un inicio de sesión con un código de recuperación queda registrado como tal. **Seguridad de la cuenta** te avisa cuando te quedan pocos códigos.

## Si pierdes el teléfono {#lost-device}

- **Aún tienes un código de recuperación.** Inicia sesión con él, abre **Seguridad de la cuenta**, desactiva los dos pasos y configúralos de nuevo en el teléfono nuevo. O genera un conjunto nuevo de códigos de recuperación si solo quieres estar seguro.
- **No tienes ningún código de recuperación.** Pide a un operador que restablezca tu segundo factor ([más abajo](#reset)). Un administrador del inquilino no puede hacerlo por ti, porque la cuenta la comparten todos los inquilinos. Tras el restablecimiento inicias sesión solo con la contraseña y puedes volver a configurar el factor.
- **Eres el único operador y no tienes ningún código de recuperación.** Consulta [break-glass](#break-glass).

## Exigirla en un inquilino {#require}

Un administrador del inquilino puede exigir un segundo factor a todos los que inician sesión en el inquilino con contraseña. Abre **Configuración → Seguridad** y elige **Exigir**. Hace falta el permiso para gestionar miembros, para todo el inquilino: un miembro limitado a algunos clientes no puede cambiarlo. Un operador puede cambiar el mismo ajuste desde **Admin → Tenants**, en la página del inquilino.

Qué hace exigirla:

- A un miembro cuyo inicio de sesión con contraseña no usó un segundo factor se le niega la entrada al inquilino. Si tiene un factor, la consola le pide que inicie sesión de nuevo e introduzca el código. Si no tiene ninguno, la consola lo envía a **Seguridad de la cuenta** para configurarlo primero.
- **Nadie queda bloqueado.** Un miembro siempre puede iniciar sesión y llegar a **Seguridad de la cuenta** sin segundo factor, y configurarlo solo requiere su inicio de sesión y su contraseña. Por eso activar la exigencia no deja fuera a nadie, y no hay un periodo de gracia que configurar.
- **Las sesiones que ya están dentro del inquilino terminan en su siguiente renovación.** Quien esté usando la consola en ese momento debe iniciar sesión de nuevo, y se le explica por qué.
- **Las cuentas de servicio no se ven afectadas**, porque nunca inician sesión con contraseña.
- **Los superusuarios no están sujetos a ella.** Un superusuario con una sesión solo de contraseña puede entrar en el inquilino, y cada entrada queda registrada como `mfa_enforcement_bypassed`. Los operadores pueden activar su propio segundo factor y deberían hacerlo.
- **No obliga a la consola de administración ni a la API de administración.** La exigencia del inquilino decide quién puede entrar en ese inquilino. Llegar al plano de administración de los operadores es otra cuestión, y nada de lo que configure un inquilino lo afecta.

### El mínimo por nivel {#tier-floor}

Un operador puede convertir la autenticación en dos pasos en un mínimo para todos los inquilinos de un nivel: abre el nivel (**Tenants → Tiers**) y, en su pestaña **Settings**, marca **Exigir autenticación en dos pasos**. Un inquilino de ese nivel debe usarla, sea cual sea su propio ajuste, y su administrador no puede desactivarla: la página **Seguridad** del inquilino indica que el nivel la exige. Deja la clave desactivada para que cada inquilino decida.

### Inicio de sesión único y dos pasos {#sso}

La exigencia del inquilino obliga solo a los inicios de sesión **con contraseña**. Una persona que inicia sesión mediante el inicio de sesión único se rige por los requisitos de su propia conexión: para una garantía mayor, exige un método de autenticación en la conexión (`requiredAmr`, por ejemplo `["mfa"]`), de modo que el proveedor tenga que haber aplicado su propio segundo factor. Consulta [Configurar el inicio de sesión único](./configuring-sso.md). Un inquilino que activa **Require SSO** ya no tiene inicios de sesión con contraseña a los que aplicar la exigencia.

## Línea de comandos y clientes de la API {#clients}

### dcctl {#dcctl}

`dcctl dead-letters`, `dcctl presence` y `dcctl sim` inician sesión como una cuenta en tu nombre, así que aceptan `--otp <código>` para una cuenta con segundo factor. También se acepta un código de recuperación. El código se usa solo para el primer inicio de sesión, y **un código sirve una sola vez**:

- Si el comando tiene que iniciar sesión de nuevo (su token caducó durante una ejecución larga), pide un código en el terminal, sin mostrarlo en pantalla.
- Sin terminal, se detiene y dice que lo ejecutes de nuevo con un `--otp` nuevo. Nunca ofrece el mismo código dos veces, porque el servidor lo rechazaría como repetición y un bucle de reintentos encima es la forma de bloquear una cuenta.

Para la automatización, o mantén un superusuario **sin** segundo factor y úsalo solo para scripts, o acepta las peticiones. Lo primero es la opción pragmática para un pipeline, y su precio es que la cuenta queda protegida solo por su contraseña, así que dale una contraseña larga y generada. `dcctl sim` lee la contraseña del superusuario inicial del Secret de Kubernetes de la instancia, así que un superusuario inicial con segundo factor también necesita `--otp` allí.

### GraphQL {#graphql-clients}

`login` ya no devuelve un token para una cuenta con segundo factor. Falla con el código de error `MFA_CHALLENGE`, que lleva un desafío, y el cliente termina con `verifyMfa`. Todo cliente que inicie sesión con `login` debe gestionarlo. La forma exacta está en [la referencia de la API GraphQL](../reference/graphql-api.md#two-factor-sign-in).

### SDK de C# {#csharp}

`LoginAsync` lanza `MfaRequiredException` para una cuenta así. Su `Challenge` se pasa a `VerifyMfaAsync` junto con el código:

```csharp
try
{
    await client.LoginAsync(email, password);
}
catch (MfaRequiredException mfa)
{
    // Pide a la persona el código de su aplicación de autenticación, o un código de recuperación.
    await client.VerifyMfaAsync(mfa.Challenge, code);
}
```

`MfaRequiredException` es una `GraphQlRequestException`, así que el código que ya captura ese tipo sigue capturándola. No llames a `LoginAsync` de nuevo para reintentar: una segunda llamada crea un segundo desafío. Un código incorrecto es una `GraphQlRequestException` y el desafío sigue siendo utilizable unos intentos más; un desafío caducado es uno con el código `MFA_CHALLENGE_EXPIRED`, y la persona inicia sesión de nuevo.

## Para operadores {#operators}

### Restablecer un segundo factor perdido {#reset}

Abre **Admin → Identities**, abre a la persona y, en la sección **Autenticación en dos pasos**, elige **Restablecer dos pasos**. Se eliminan el autenticador, los códigos de recuperación y el secreto guardado, y terminan todas las sesiones de la persona. Su siguiente inicio de sesión pide solo la contraseña, y puede volver a configurar la autenticación en dos pasos. La lista tiene una columna **Dos pasos** para ver quién la tiene.

- Hace falta el permiso para gestionar usuarios. Restablecer una identidad que tiene un **rol de sistema** exige un superusuario.
- **Si la cuenta ha tenido códigos incorrectos desde el último correcto, el restablecimiento pide una contraseña nueva y la establece en el mismo paso.** Los códigos incorrectos significan que alguien que conoce la contraseña ha estado adivinando el código, así que quitar solo el segundo factor le entregaría la cuenta. La consola muestra el campo adicional cuando el servidor lo exige; la API responde `MFA_RESET_NEEDS_PASSWORD` y no cambia nada hasta que envías `newPassword`.
- El restablecimiento queda registrado como `mfa_reset`, con el nombre de quien lo hizo.
- La llamada de la API es `resetIdentityMfa(email, newPassword)` en la API de administración.

Borrar la contraseña de una identidad (`clearPassword` en la API de administración, que la convierte en solo inicio de sesión único) también elimina su segundo factor, los códigos de recuperación y el secreto guardado, en el mismo paso. El factor protege solo los inicios de sesión con contraseña, así que sin contraseña no protegería nada y quedaría huérfano.

### Break-glass: el operador que no puede iniciar sesión {#break-glass}

Si el único superusuario tiene un segundo factor y ha perdido tanto el teléfono como los códigos de recuperación, nadie puede restablecerlo con la API de administración. `dcctl instances break-glass mfa-reset` lo hace desde dentro del clúster:

```bash
dcctl instances break-glass mfa-reset --instance <instance> --email superuser@devicechain.local
```

- **Necesita credenciales del clúster y nada más.** Ejecuta el binario del propio servicio dentro de un pod `user-management` listo (así que necesita permiso para hacer `exec` en los pods del namespace de la instancia) y no inicia sesión en nada.
- **Siempre sustituye la contraseña.** No hay un llamante que lo avale, así que quitar solo el factor entregaría la cuenta a quien tenga la contraseña anterior. dcctl genera una contraseña y la imprime **una sola vez**. Para scripts, o para elegirla tú, añade `--password-stdin` y se usa la primera línea de la entrada estándar (no se imprime nada; también necesita `--yes`).
- **Te pide que escribas el nombre de la instancia** para confirmar, o acepta `--yes`.
- **Termina todas las sesiones** de la cuenta y elimina el factor, los códigos y el secreto.
- **Rechaza una identidad sin rol de sistema.** A cualquier otra persona se la restablece con la API de administración, donde el registro nombra al operador que lo hizo. El restablecimiento break-glass queda registrado como `mfa_reset` por `break-glass:pod-exec`.
- **Para el superusuario inicial de la instancia** (`superuser@devicechain.local`), dcctl también actualiza la clave `password` del Secret `dci-<instance>-superuser` de la instancia, que leen `dcctl sim`, las sondas y una repetición de bootstrap. Si no puede, lo dice en voz alta, porque el restablecimiento ya se ha hecho y el Secret conserva la contraseña anterior: pasa la nueva a `dcctl sim` con `--admin-password` hasta que corrijas el Secret.

### Bloqueos, y por qué el remedio es la contraseña {#lockouts}

Cada intento de adivinar un código lo hace alguien que ya tiene la contraseña, así que un patrón de códigos incorrectos indica que la contraseña la conoce otra persona.

- **Cada código incorrecto se cuenta antes de comprobarse.** Tras **5** códigos incorrectos desde una misma dirección de origen, esa dirección queda bloqueada para el autenticador de la cuenta; tras **20** desde todas las direcciones juntas, el autenticador se bloquea para todos. Un bloqueo dura un minuto y se duplica con cada fallo adicional, hasta **15 minutos**.
- **Un intento bloqueado recibe exactamente la misma respuesta que un código incorrecto** ("invalid code"), así que la respuesta no dice nada a quien adivina, y no se evalúa ningún código mientras dura el bloqueo.
- **Los códigos de recuperación siguen funcionando durante un bloqueo.** Llevan 80 bits aleatorios y no se pueden adivinar, así que están exentos del bloqueo del autenticador. Eso evita que una persona con sus códigos de recuperación quede fuera.
- **Quien conoce la contraseña puede renovar un bloqueo indefinidamente**, con un código incorrecto cada vez que caduca. No hay bloqueo permanente, por diseño: uno permanente permitiría a cualquiera que conozca una dirección dejar fuera a su dueño para siempre. Es el mismo compromiso que [la espera entre intentos de inicio de sesión](../reference/graphql-api.md#sign-in-backoff).
- **El remedio es restablecer la contraseña** (`setPassword` en la API de administración, o **Set password** en la página de la identidad), **no el segundo factor.** Restablecer la contraseña rota la época de sesión de la cuenta, lo que también invalida todos los desafíos de inicio de sesión pendientes. Restablecer solo el factor mientras se conoce la contraseña entregaría la cuenta a quien está adivinando. Por esa razón el restablecimiento se niega a ejecutarse sin una contraseña nueva.
- Las direcciones de origen están en los registros del ingress. Un emisor que controle la cabecera de dirección reenviada puede obtener una asignación nueva por dirección, así que el límite global de 20 por cuenta es el límite real.

El riesgo residual se indica en [el modelo de seguridad](../concepts/security-model.md#two-factor): para quien ya conoce la contraseña y sigue adivinando al ritmo máximo, alrededor del 10 % al año.

### Alertas y métricas {#alerts}

El chart incluye dos alertas, que se disparan solo cuando las alertas de métricas están activadas:

| Alerta | Gravedad | Significado | Qué hacer |
| --- | --- | --- | --- |
| `MfaIdentityLocked` | critical | Se activó el bloqueo global de una cuenta: se comprobaron veinte códigos de autenticador incorrectos, cada uno tras una **contraseña correcta**. Alguien tiene la contraseña de esta cuenta y está adivinando su código. Se dispara con una sola ocurrencia. | Encuentra la cuenta en el registro de auditoría (`mfa_locked`, precedido de filas `mfa_failed` y `mfa_challenged`) y **restablece su contraseña**. No restablezcas primero el segundo factor. Las direcciones de origen están en los registros del ingress. |
| `MfaVerificationFailuresSustained` | warning | Durante quince minutos se han rechazado códigos incorrectos, repetidos o bloqueados a más de uno cada diez segundos en toda la instancia. O bien muchas personas se equivocan al escribir (un problema de reloj en los nodos, o aplicaciones desincronizadas) o alguien usa contraseñas que ha obtenido. | Comprueba que los relojes de los nodos estén sincronizados y busca filas `mfa_failed` agrupadas por cuenta y los registros del ingress. Una cuenta que se bloquea dispara `MfaIdentityLocked` por sí sola. |

Los contadores que las alimentan son `devicechain_usermanagement_mfa_verifications_total{method,result}` (`method` es `totp` o `recovery`; `result` es `ok`, `invalid`, `replay`, `locked`, `expired`, `budget`, `rate_limited` o `unavailable`), `mfa_challenges_total`, `mfa_enrollments_total{result}` (`begun`, `confirmed`, `disabled`, `reset`) y `mfa_identity_locks_total`. Consulta [la referencia de métricas](../deployment/metrics-reference.md#user-management).

### Qué registra el registro de auditoría {#audit}

El servicio de inicio de sesión registra `mfa_challenged` cuando una contraseña era correcta y hace falta un código, `mfa_failed` para un código comprobado y rechazado, `mfa_locked` para el intento que activó el bloqueo global, `mfa_enrolled`, `mfa_disabled`, `mfa_reset` (con quien lo hizo), `mfa_recovery_code_used`, `mfa_recovery_codes_regenerated`, `mfa_enforcement_bypassed` y `tenant_mfa_required_changed`. Un intento rechazado por un bloqueo se cuenta y no se escribe, de modo que quien tiene una contraseña no puede provocar escrituras en el registro. Ninguna fila contiene nunca un código, un secreto o un desafío.

**La fila `login` cambió de significado.** Ahora se escribe cuando un inicio de sesión **se completa**, no cuando se acepta la contraseña, e indica cómo: `pwd`, `pwd+otp` o `pwd+recovery`. Para una cuenta con segundo factor, el paso de la contraseña es la fila `mfa_challenged`, y la fila `login` aparece solo cuando se acepta el código.

## Solución de problemas {#troubleshooting}

| Síntoma | Causa probable |
| --- | --- |
| Se rechazan códigos correctos para todos | Los relojes de los nodos de servicio se han desviado más de unos 30 segundos. Corrige la sincronización horaria. |
| Se rechaza el código de una persona, los demás funcionan | El reloj del teléfono está desajustado (activa la hora automática), o el código ya se usó: un código sirve una sola vez, así que espera al siguiente. |
| "invalid code" incluso con un código nuevo | La cuenta puede estar bloqueada tras repetidos códigos incorrectos. Un intento bloqueado se ve exactamente igual que uno incorrecto. Usa un código de recuperación, espera hasta 15 minutos y consulta a un operador si el bloqueo vuelve. |
| "This sign-in timed out" | El desafío dura cinco minutos y cinco intentos. Inicia sesión de nuevo. Un cambio de contraseña también lo invalida. |
| A un miembro se le envía a **Seguridad de la cuenta** | Su inquilino exige la autenticación en dos pasos y no la ha configurado. |
| Todas las sesiones terminaron tras configurarla | Es lo previsto: activarla termina las demás sesiones de la cuenta. |
| **Seguridad de la cuenta** dice "Administrar la autenticación en dos pasos requiere un inicio de sesión reciente con contraseña. Inicia sesión de nuevo" | La página necesita un inicio de sesión con contraseña reciente, porque el token de identidad que usa dura 15 minutos. Cierra sesión, vuelve a iniciarla con tu contraseña y abre **Seguridad de la cuenta** de nuevo. |
