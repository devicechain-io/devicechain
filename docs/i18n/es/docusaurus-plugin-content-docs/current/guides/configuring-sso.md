---
title: Configurar el inicio de sesión único
---

# Configurar el inicio de sesión único

Esta guía conecta un proveedor de identidad OpenID Connect para que las personas de un inquilino inicien sesión con la cuenta de su organización. Para saber cómo se comporta la función y dónde están sus límites, lee primero [Inicio de sesión único](../concepts/single-sign-on.md).

:::caution Las recetas por proveedor aún no están probadas en vivo
Las recetas para Microsoft Entra ID, Okta y Google Workspace que siguen están escritas a partir de la documentación de cada proveedor y de las afirmaciones que DeviceChain comprueba. **Aún no se han ejecutado contra inquilinos reales** de esos proveedores. El flujo de inicio de sesión en sí se prueba de extremo a extremo contra un proveedor OpenID Connect de sustitución. Espera tener que ajustar alguna etiqueta de la consola o el nombre de una afirmación, y cuéntanos lo que encuentres.
:::

## Antes de empezar {#before}

- **La instancia necesita una dirección pública.** El inicio de sesión único necesita `auth.issuerUrl` ([más abajo](#deployment)). Si está vacío, crear una conexión falla con "sso requires a public issuer URL".
- **Necesitas iniciar sesión como superusuario** en la consola (**Admin → Identity providers**) o en la API de administración. Una identidad cuyo rol de sistema tenga `user:write` también puede usar la API de administración, pero no las páginas de administración de la consola, que solo se abren para un superusuario.
- **En el proveedor** necesitas registrar DeviceChain como aplicación (un cliente "web" o "confidential" que use el flujo de código de autorización) y anotar su id de cliente y su secreto de cliente.
- **Decide el inquilino y el dominio.** Una conexión de inquilino sirve a los inquilinos que nombres y representa los dominios de correo que afirmes.

## Crear una conexión {#create}

1. En la consola, abre **Admin → Identity providers** y elige **New connection**. Dale un token (letras, dígitos y guiones), un nombre, el tipo (`tenant`), el **issuer** del proveedor, y el id y el secreto de cliente. El emisor y el tipo no se pueden cambiar después; un emisor distinto es una conexión distinta. Si el certificado del proveedor fue firmado por una autoridad de certificación privada, aporta aquí también su certificado ([detalles](#private-ca)).
2. DeviceChain obtiene el documento de descubrimiento del proveedor y lo retiene para su aprobación. La conexión empieza deshabilitada. La página muestra su callback URL, con la etiqueta **Callback address**: `<issuerUrl>/sso/callback/<token>`. Registra exactamente esa como URI de redirección en el proveedor.
3. Revisa los endpoints y elige **Approve endpoints**.
4. Añade los dominios de correo que representa el proveedor (por ejemplo `example.com`).
5. Elige los inquilinos a los que sirve.
6. Si las personas deben crearse en el primer inicio de sesión, activa el aprovisionamiento y elige los roles por defecto (`setIdpJit`). Añade asignaciones de grupo a rol si el proveedor envía grupos (`setIdpRoleMappings`). La consola aún no tiene controles para esto, así que usa la API de administración.
7. Elige **Enable**.
8. Opcionalmente, define el límite de sesión (12 horas por defecto; `setIdpSessionMaxAge` en la API de administración, ya que la consola aún no tiene un control para ello) y, cuando todos tengan una forma de entrar, activa **Require SSO** en el inquilino (**Admin → Tenants**). La consola pide confirmación antes, porque termina las sesiones con contraseña del inquilino.

Los mismos pasos están disponibles en la API GraphQL de administración: `createIdpConnection`, `approveIdpDiscovery`, `addIdpDomain`, `setIdpConnectionTenants`, `setIdpJit`, `setIdpRoleMappings`, `updateIdpConnection` (`enabled: true`), `setIdpSessionMaxAge` y `setTenantSsoEnforced`. Un superusuario también puede usar `setIdpRequiredClaims` para cambiar las afirmaciones que exige una conexión.

Una conexión de instancia (para tus propios operadores) se crea del mismo modo con el tipo `instance`, y además necesita `requiredAmr` (por ejemplo `["mfa"]`) o `mfaEnforcedByIdp: true`. Vincula a cada operador explícitamente con `linkIdentity`; nada los vincula automáticamente.

## Autenticación en dos pasos {#two-factor}

La [exigencia de dos pasos](./two-factor-authentication.md#require) de un inquilino obliga solo a los inicios de sesión **con contraseña**. Una persona que inicia sesión mediante el inicio de sesión único no la encuentra, y el ajuste del inquilino no se le aplica. Lo que rige un inicio de sesión SSO es la conexión: exige un método de autenticación en ella (`requiredAmr`, por ejemplo `["mfa"]`) para que el proveedor de identidad tenga que haber aplicado su propio segundo factor. Un inquilino que activa **Require SSO** ya no tiene inicios de sesión con contraseña a los que aplicar la exigencia.

## Cerrar sesión en el proveedor {#provider-sign-out}

Por defecto, cerrar sesión en la consola o en la aplicación de paneles termina solo la sesión de DeviceChain. La persona sigue con la sesión iniciada en el proveedor de identidad, así que su siguiente **Sign in with SSO** puede pasar directamente sin pedir contraseña. Una conexión también puede enviar a las personas a la página de cierre de sesión del propio proveedor cuando cierran sesión (cierre de sesión iniciado por el RP de OpenID Connect). Está **desactivado por defecto**, porque cerrar sesión en el proveedor suele terminar la sesión de la persona allí para todas las aplicaciones a las que sirve el proveedor.

1. Comprueba que el documento de descubrimiento aprobado muestra un **Sign-out endpoint**. Un proveedor que no publica ninguno no permite cerrar sesión de esta forma: el ajuste no tiene efecto, y cerrar sesión sigue terminando la sesión de DeviceChain.
2. En la pestaña **Settings** de la conexión, en **Sign out at the provider**, copia las dos **Post-logout redirect addresses** y regístralas en el proveedor como URI de redirección tras el cierre de sesión (post-logout). Son `<origen>/login` para la consola y `<origen>/dash/` para la aplicación de paneles, donde `<origen>` es el esquema y el host de la [dirección pública](#issuer-url) de la instancia.
3. Marca **Also sign people out at the provider**. En la API de administración es `updateIdpConnection` con `providerSignOut: true`.

Qué ocurre cuando alguien cierra sesión:

- **La sesión de DeviceChain siempre termina primero**, en el servidor, haga lo que haga el proveedor después. Solo entonces se envía el navegador al proveedor, que lo devuelve a la página de inicio de sesión.
- La solicitud lleva el id de cliente de la conexión y la dirección de retorno, y nada más. DeviceChain no guarda el token de ID del proveedor, así que no envía `id_token_hint`, y el proveedor puede pedir a la persona que lo confirme. Un proveedor que rechaza una solicitud de cierre de sesión sin `id_token_hint` no se puede usar con este ajuste; déjalo desactivado.
- El servidor construye la dirección de retorno a partir de la dirección pública de la instancia, y quien llama no puede indicarla. Si no está registrada en el proveedor, el proveedor muestra su propio error en lugar de devolver a la persona; la sesión de DeviceChain ha terminado de todos modos.
- Solo se aplica a los inicios de sesión a través de esa conexión mientras está habilitada y su documento de descubrimiento está aprobado. Un inicio de sesión con contraseña solo cierra la sesión de DeviceChain.

## Recetas por proveedor {#recipes}

En cada receta, reemplaza `<callback URL>` por la callback URL de la conexión.

### Microsoft Entra ID {#entra}

1. En el centro de administración de Entra, **App registrations → New registration**. Tipo de cuenta: **Accounts in this organizational directory only**. URI de redirección: plataforma **Web**, valor `<callback URL>`.
2. **Certificates & secrets → New client secret.** Copia el valor.
3. **Token configuration → Add optional claim → ID token**, y añade `email` y `xms_edov`. Microsoft no envía `email_verified`; DeviceChain confía en una dirección solo cuando `xms_edov` es verdadero, así que sin esta afirmación se rechaza todo inicio de sesión que no esté ya vinculado a una identidad (`refused_unverified_email`), y tampoco queda ninguna solicitud de vínculo.
4. Para la asignación de grupos: **Token configuration → Add groups claim**, elige **Groups assigned to the application**. La afirmación `groups` lleva entonces los ids de objeto de los grupos, así que asigna esos ids, no los nombres de grupo.
5. Crea la conexión con:
   - issuer `https://login.microsoftonline.com/<tenant GUID>/v2.0`, con el id de inquilino de tu directorio. `common`, `organizations` y `consumers` se rechazan.
   - afirmación requerida `tid` = el mismo GUID de inquilino. Es obligatoria para un emisor de Microsoft.
   - scopes `openid`, `email`, `profile`.
6. **Groups overage.** Un token puede listar como máximo unos 200 grupos; por encima de eso Entra envía un puntero en lugar de la lista (`hasgroups` o `_claim_names`). DeviceChain **rechaza el inicio de sesión** cuando una conexión asigna grupos (`refused_groups_overage`; se le dice a la persona que consulte a su administrador) en lugar de tratar a la persona como si no tuviera grupos. Evítalo asignando a la aplicación solo los grupos necesarios. Los hosts emisores de nubes soberanas se tratan como `login.microsoftonline.com`.
7. Para una conexión de instancia, Entra informa `mfa` en `amr` cuando se realizó la autenticación multifactor; exígelo.

### Okta {#okta}

1. En la consola de administración de Okta, **Applications → Create App Integration → OIDC - OpenID Connect → Web Application**. Sign-in redirect URI: `<callback URL>`. Asigna las personas o grupos que pueden usarla.
2. Copia el id de cliente y el secreto de cliente.
3. Issuer: la URL de tu organización de Okta para el servidor de autorización de la organización (`https://<org>.okta.com`), o un servidor de autorización personalizado (`https://<org>.okta.com/oauth2/<id>`). Debe ser exactamente igual al `issuer` del documento de descubrimiento de ese servidor.
4. Okta envía `email` y `email_verified`. Si los correos de tus usuarios de Okta no están verificados, los inicios de sesión se rechazan.
5. Para la asignación de grupos: añade el scope `groups` a los scopes de la conexión, y añade una **groups claim** (pestaña Sign On, o los Claims del servidor de autorización) con un filtro como "Starts with" o "Matches regex" para que solo se envíen los grupos que quieres. Asigna los nombres de los grupos. El `amr` de Okta incluye `mfa` cuando se usó autenticación multifactor.
6. Okta no tiene una afirmación que fije el inquilino como `tid`; cada organización tiene su propio emisor, que es lo que la fija.

### Google Workspace {#google}

1. En la consola de Google Cloud, **APIs & Services → OAuth consent screen**: tipo de usuario **Internal** (limita el inicio de sesión a tu Workspace). **Credentials → Create credentials → OAuth client ID → Web application**, URI de redirección autorizado `<callback URL>`.
2. Issuer `https://accounts.google.com`. Afirmación requerida `hd` = el dominio principal de tu Workspace (por ejemplo `example.com`). Es obligatoria: Google firma para todas las cuentas de Google, incluidas las personales, y solo las cuentas de Workspace llevan `hd`. El `hd` del token también debe ser igual al dominio de la dirección.
3. Añade el mismo dominio como dominio verificado de la conexión.
4. Los tokens de identidad de Google **no llevan grupos** ni `amr`. Usa los roles por defecto de la conexión, no la asignación de grupos, y no uses Google para una conexión de instancia salvo que definas `mfaEnforcedByIdp` e impongas la verificación en dos pasos en Workspace.

### Otros proveedores {#other}

Funciona cualquier proveedor OpenID Connect que sirva un documento de descubrimiento por https, firme los tokens de identidad con RS256, ES256 o PS256, y envíe `email` con `email_verified`. `none` y las firmas HMAC se rechazan. Define en la conexión los nombres de las afirmaciones de correo y de grupos si el proveedor usa otros.

## Despliegue y configuración {#deployment}

### La dirección pública {#issuer-url}

El `auth.issuerUrl` de user-management debe ser la dirección accesible externamente de su API, por ejemplo `https://dc.example.com/api/user-management`. El chart la deriva del ingress (`ingress.host`, con `ingress.tls.enabled`), así que una instalación normal no necesita nada. Defínela tú en `functionalAreas.user-management.config.auth.issuerUrl` para anularla, o como `""` para mantener desactivados el inicio de sesión único y el servidor de autorización OAuth. Una dirección http en un nombre de host real se rechaza al arrancar; sirve la instancia por https. Tu ingress debe enrutar `/api/user-management/sso/` a user-management (el ingress del chart lo hace).

### Llegar al proveedor {#egress}

Toda solicitud a un proveedor pasa por la protección de salida, que rechaza destinos privados, de loopback y de enlace local. Un proveedor público no necesita nada. Para un proveedor local (ADFS, Keycloak, Dex en tu red), permite su rango de direcciones en `instance.config.infrastructure.egress.allowedDestinations` con CIDR estrechos. La lista de permitidos es de toda la instancia y también se aplica a webhooks y conectores, así que prefiere un /32. El certificado del proveedor debe encadenar con las raíces del sistema de la imagen de user-management, o con un [paquete de CA privada](#private-ca) que aportes a esa conexión.

### Un proveedor con certificado de una CA privada {#private-ca}

Un proveedor local suele servir un certificado TLS firmado por la autoridad de certificación de tu propia organización, que las raíces del sistema no incluyen. Sin más, crear la conexión falla con «certificate signed by unknown authority». Aporta a la conexión el certificado de esa CA en lugar de debilitar nada en toda la instancia:

1. Obtén el certificado de la CA (y los intermedios, si los hay) como texto PEM: uno o más bloques `-----BEGIN CERTIFICATE-----`, hasta 64 KiB. Nunca una clave privada; una clave privada en el texto se rechaza y no se devuelve.
2. Al crear la conexión, pégalo en **Private CA certificates** o elige el archivo. En una conexión existente, usa **Settings → Private CA** y luego **Verify and save bundle**. En la API de administración es `caBundle` en `createIdpConnection`, o `setIdpCaBundle`.
3. Revisa los certificados listados: cada uno muestra su sujeto, su caducidad y su huella SHA-256. Compara la huella con la que publica tu CA. Un certificado que no indica ser una autoridad de certificación se acepta exactamente como ese certificado y aparece marcado **Not a CA**.

Qué hace y qué no:

- El paquete se aplica **solo a esa conexión**: el descubrimiento, el conjunto de claves y el intercambio de tokens confían en él, y ninguna otra conexión ni función. Se confía en él **además de** las raíces del sistema, así que un proveedor con certificado público sigue funcionando aunque haya un paquete.
- No cambia la protección de salida. La dirección del proveedor debe seguir siendo accesible según [la lista de permitidos](#egress); un certificado nunca hace accesible una dirección prohibida.
- El paquete se pega o se sube, nunca se descarga de una dirección, porque una raíz de confianza descargada la elige quien responda a la descarga.
- Al guardar un paquete nuevo, primero se accede con él a la dirección de descubrimiento del proveedor, y se rechaza (sin guardar nada) si el certificado no encadena con él. Si el proveedor indica entonces endpoints distintos de los que aprobaste, se retira la aprobación y debes revisarlos de nuevo; si indica los mismos, la aprobación se mantiene, de modo que rotar la CA no deja a nadie fuera.
- El certificado del proveedor debe seguir incluyendo su dirección (un nombre DNS o una IP que coincida con el emisor) en los nombres alternativos del sujeto, y estar dentro de su periodo de validez. Sustituye el paquete antes de que caduque un certificado de CA. **Remove bundle** (`setIdpCaBundle` con una cadena vacía) devuelve la conexión solo a las raíces del sistema.
- El paquete es un certificado público, así que se guarda con la conexión y no como secreto, y se incluye en una copia de seguridad de la base de datos.

### Detrás de un balanceador de carga {#load-balancer}

Conserva la dirección del cliente (`externalTrafficPolicy: Local` o protocolo proxy), o todos tus usuarios compartirán una única cuota de límite de frecuencia. Consulta [la página de conceptos](../concepts/single-sign-on.md#load-balancer).

### Alertas {#alerts}

| Alerta | Gravedad | Significado | Qué hacer |
| --- | --- | --- | --- |
| `SsoIdpUnreachable` | warning | Durante diez minutos user-management no ha podido llegar al endpoint de token ni a las claves de firma de un proveedor, así que se rechaza a quienes inician sesión por él. El inicio de sesión con contraseña no se ve afectado. | Comprueba que el proveedor esté activo y se resuelva desde el clúster, que la lista de permitidos de salida permita un rango local y, si el proveedor movió un endpoint, actualiza y aprueba la instantánea de descubrimiento. |
| `SsoUnverifiedIdentityRefused` | warning | Se rechazó un inicio de sesión aunque el proveedor lo avalaba: `refused_unverified_email`, `refused_unverified_domain`, `refused_required_claim` o `refused_system_role_identity` (la etiqueta `outcome`). Nadie inició sesión. | Normalmente es una conexión mal configurada o alguien que la sondea. Lee el diario de auditoría para ver la dirección afirmada y el registro de inicios de sesión del propio proveedor. |
| `SsoStateStoreFull` | critical | El almacén que guarda los inicios de sesión comenzados está lleno, así que no puede comenzar ningún inicio de sesión único por ningún proveedor hasta que caduquen los inicios de sesión comenzados, diez minutos después de que cese la avalancha. El inicio de sesión con contraseña no se ve afectado. | Probablemente alguien llama en bucle al inicio público del inicio de sesión: encuéntralo en los registros del ingress y bloquéalo aguas arriba. Si el volumen es legítimo, aumenta `instance.config.infrastructure.nats.kvStateMaxBytes`. |

Las métricas detrás de ellas son `devicechain_usermanagement_sso_logins_total{connection,outcome}` y `devicechain_usermanagement_sso_idp_fetch_failures_total{connection}`. Otros resultados que puedes ver en la primera: `state_invalid` (un inicio de sesión caducado, repetido o que no coincide), `idp_error`, `rate_limited`, `refused_link_required`, `refused_groups_overage`, `refused_token_invalid`, `refused_issuer_mismatch`.

## Solución de problemas {#troubleshooting}

| Síntoma | Causa probable |
| --- | --- |
| La consola pide una contraseña para una dirección que debería usar el inicio de sesión único, o **Iniciar sesión con SSO** dice "Single sign-on is not set up for that email address" | Ninguna conexión utilizable habla por el dominio: no está afirmado en ninguna conexión, o su conexión está deshabilitada o su descubrimiento no está aprobado. Un inicio de sesión por dirección responde igual en todos estos casos (la conexión aparece con el resultado `refused_connection_unavailable`), y la consola también pide una contraseña cuando la instancia no tiene inicio de sesión único, así que revisa **Admin → Identity providers**. |
| "Single sign-on is not available right now" | La instancia no está configurada para el inicio de sesión único (véase [Despliegue](#deployment)), o se iniciaron demasiados inicios de sesión desde la misma dirección: espera un momento y vuelve a intentarlo. |
| "Your account exists but is not linked" | La dirección ya tiene una identidad: aprueba su solicitud de vínculo. |
| Todos los inicios de sesión se rechazan como no verificados | El proveedor no envía un correo verificado (para Entra, añade `xms_edov`). |
| Rechazo tras un cambio del proveedor | El proveedor movió un endpoint: **Refresh from provider** y aprueba de nuevo. |
| "no enabled identity provider connection serves this tenant" | Conecta y habilita un proveedor antes de activar **Require SSO**. (código de error `SSO_ENFORCE_NO_CONNECTION`.) |
| "this is the last enabled identity provider connection" | Desactiva primero **Require SSO** en el inquilino, o habilita otra conexión que lo sirva, y luego deshabilita o elimina la conexión. (código de error `SSO_ENFORCED_TENANT_STRANDED`; la consola lo explica.) |
| Al vincular una cuenta: "El inicio de sesión único no está configurado para el dominio de tu correo electrónico" | Ninguna conexión de inquilino habilitada y aprobada tiene el dominio del correo de la persona. Añade el dominio a la conexión, o habilítala o apruébala. (`SSO_LINK_NO_CONNECTION`.) |
| Al vincular una cuenta: "vuelve a iniciar sesión" | El inicio de sesión con contraseña tiene más de 15 minutos, terminó o se hizo mediante inicio de sesión único. Inicia sesión con la contraseña y vuelve a vincular. (`SSO_LINK_SESSION_REQUIRED`, `SSO_LINK_PASSWORD_REQUIRED`, o `link_session_ended` al volver.) |
| Al vincular una cuenta: "un correo electrónico distinto" | La persona inició sesión en el proveedor como otra persona, o el proveedor informa otra dirección. (`link_email_mismatch` al volver.) |
| Al vincular una cuenta: "ya está vinculada" | La identidad ya está vinculada en esa conexión, o la cuenta del proveedor está vinculada a otra identidad: desvincúlala primero. (`SSO_LINK_ALREADY_LINKED`, o `link_already_linked` al volver.) |
| Al vincular una cuenta: "Pide a un superusuario que la vincule" | La identidad tiene un rol de sistema; vincúlala con `linkIdentity`. (`SSO_LINK_NOT_ALLOWED`.) |
