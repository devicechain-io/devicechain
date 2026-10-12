---
title: Inicio de sesión único
---

# Inicio de sesión único

Las personas pueden iniciar sesión en DeviceChain a través del proveedor de identidad de su propia organización (Microsoft Entra ID, Okta, Google Workspace o cualquier proveedor OpenID Connect) en lugar de una contraseña de DeviceChain. Un administrador conecta el proveedor una sola vez; las personas que representa inician sesión con la cuenta que ya tienen, y el proveedor, no DeviceChain, decide quiénes son y si su cuenta sigue activa.

Esta página explica cómo se comporta la función y dónde están sus límites. Para configurarla, consulta [Configurar el inicio de sesión único](../guides/configuring-sso.md).

:::note Estado
El inicio de sesión único habla **OpenID Connect**. SAML no es compatible. Las recetas para Microsoft Entra ID, Okta y Google Workspace siguen el comportamiento documentado de cada proveedor, pero **aún no se han probado contra inquilinos reales de esos proveedores**; el flujo se prueba de extremo a extremo contra un proveedor OpenID Connect de sustitución.
:::

## Conexiones {#connections}

Una **conexión** es un proveedor de identidad registrado en la instancia. Solo un superusuario crea, modifica o elimina conexiones, mediante **Admin → Identity providers** en la consola o la API de administración. Hay dos tipos:

| | Conexión de inquilino | Conexión de instancia |
| --- | --- | --- |
| De quién es el proveedor | Las personas de un cliente o una organización | El personal propio del operador |
| A quién sirve | Uno o más inquilinos que eliges | Ningún inquilino: inicia sesión de operadores en la instancia |
| Crea identidades en el primer inicio de sesión | Sí, si activas el aprovisionamiento | Nunca |
| Llega a una identidad | Por coincidencia de correo tras aprobar un vínculo, o por aprovisionamiento | Solo mediante un vínculo explícito que hace un superusuario |
| Puede llegar a una identidad con un rol de sistema | **Nunca** | Sí: es su propósito |
| Entra en un inquilino | Solo en los inquilinos a los que sirve | Solo como superusuario, lo cual queda registrado en el diario de auditoría |

Una conexión de inquilino es estructuralmente incapaz de iniciar sesión a nadie como operador. Diga lo que diga su proveedor, la sesión que produce no lleva roles de sistema ni autoridad de administración, y se rechaza un inicio de sesión que se resuelva en una identidad con un rol de sistema. Una conexión de instancia debe exigir un método multifactor (`requiredAmr`, por ejemplo `mfa`) o llevar tu declaración registrada (`mfaEnforcedByIdp`) de que el proveedor lo impone. La contraseña del superusuario sembrado se mantiene como acceso de emergencia.

Una conexión se crea **deshabilitada**. Pasa a ser utilizable cuando una persona ha aprobado su instantánea de descubrimiento y la ha habilitado.

## Dominios verificados {#domains}

Una conexión representa un conjunto de dominios de correo, cada uno afirmado por un superusuario y registrado con su nombre. Cuando alguien escribe su correo en la página de inicio de sesión de la consola, el dominio exacto selecciona la conexión. Un dominio pertenece a una sola conexión, y DeviceChain rechaza los sufijos públicos (`com`, `co.uk`) y los dominios de correo de consumo conocidos (`gmail.com`, `outlook.com` y similares), porque una afirmación sobre ellos dirigiría a desconocidos hacia tu proveedor.

Un primer inicio de sesión que aún no esté vinculado a una identidad solo se acepta si el proveedor avala la dirección (`email_verified`; para Microsoft Entra ID, la afirmación `xms_edov`) **y** la dirección está en uno de los dominios de la conexión. No existe ningún ajuste que confíe en una dirección que el proveedor no verificó.

Los emisores multiinquilino compartidos se rechazan salvo que la conexión esté fijada a tu organización:

- Un emisor de Microsoft debe nombrar un id de inquilino, y la conexión debe exigir que la afirmación `tid` sea igual a él. `common`, `organizations` y `consumers` se rechazan de plano.
- Una conexión de Google debe exigir la afirmación `hd` (tu dominio de Workspace), y el `hd` del token debe coincidir con el dominio de la dirección. Sin ello, cualquier cuenta de Google, incluidas las personales, podría iniciar sesión.

## Aprobar el documento de descubrimiento {#discovery}

Cuando se crea una conexión, DeviceChain obtiene el documento de descubrimiento del proveedor y conserva una **instantánea** de sus endpoints (autorización, token, claves de firma, cierre de sesión). Un superusuario revisa la instantánea y la aprueba (**Approve endpoints**) por su huella. Los inicios de sesión usan solo los endpoints aprobados y nunca obtienen el descubrimiento en vivo. Si el proveedor mueve después un endpoint, **Refresh from provider** reemplaza la instantánea, retira la aprobación, y la conexión rechaza inicios de sesión hasta que una persona apruebe la nueva. Solo el *contenido* del conjunto de claves de firma se obtiene al iniciar sesión, porque la rotación de claves es rutinaria.

Toda solicitud a un proveedor pasa por la protección de salida de la plataforma, así que un proveedor en una dirección privada, de loopback o de enlace local se rechaza salvo que el operador haya permitido su rango (consulta [Configurar el inicio de sesión único](../guides/configuring-sso.md#deployment)).

## Iniciar sesión {#sign-in}

La consola y la aplicación de paneles muestran **Sign in with SSO** junto al formulario de contraseña. El flujo es el código de autorización de OpenID Connect con PKCE:

1. La persona escribe su correo y elige **Sign in with SSO**. El navegador conserva un secreto aleatorio y envía su hash.
2. DeviceChain registra el inicio de sesión (válido diez minutos, de un solo uso) y envía el navegador al proveedor.
3. El proveedor autentica a la persona y vuelve a `<issuerUrl>/sso/callback/<connection>`.
4. DeviceChain comprueba la respuesta: que el estado y la conexión coincidan, la firma, el emisor, la audiencia, la caducidad y el nonce del token, las afirmaciones y métodos que exige la conexión, y las reglas de identidad que siguen.
5. Entrega al navegador un código de un minuto y de un solo uso. El navegador lo canjea con el secreto del paso 1, así que solo el navegador que empezó el inicio de sesión puede terminarlo, y recibe el mismo tipo de token de identidad que da un inicio de sesión con contraseña. Después la persona elige un inquilino como de costumbre.

La página que recibe el código (`/login/sso/complete` para la consola, `/dash/sso/complete` para la aplicación de dashboards) lo quita de la barra de direcciones de inmediato y se sirve con `Referrer-Policy: no-referrer`, así que nada de lo que carga puede llevar la dirección. Al terminar el inicio de sesión, o si la persona vuelve al formulario de inicio de sesión, la aplicación se carga de nuevo en lugar de seguir en esa página, de modo que el resto de la sesión envía un `Referer` normal; el mapa base predeterminado de OpenStreetMap lo exige. Si tu propio proxy está delante de la consola, limita cualquier cabecera que elimine el Referer a esas dos rutas exactas.

Cuando se rechaza un inicio de sesión, la persona ve un mensaje genérico ("Single sign-on did not allow this account in", "your account is not linked yet") y nunca qué comprobación falló. (Vincular la propia cuenta es la excepción: la persona ya inició sesión y cada comprobación trata de su propia cuenta, así que un rechazo de vínculo dice qué comprobación falló; consulta [vínculos](#linking).) El resultado exacto se cuenta en la métrica `devicechain_usermanagement_sso_logins_total` y los rechazos se escriben en el diario de auditoría con la dirección que afirmó el proveedor.

## Identidades, vínculos y aprovisionamiento {#linking}

DeviceChain nunca vincula una cuenta externa a una identidad existente porque las direcciones de correo se parezcan. Un vínculo se hace de una de cuatro formas, y todas se auditan:

- **Aprovisionamiento (just-in-time).** En una conexión de inquilino con el aprovisionamiento activado, un inicio de sesión cuya dirección **no tiene ninguna identidad** crea una (sin contraseña) y la vincula. Es la única escritura automática.
- **Un superusuario aprueba una solicitud de vínculo.** Un inicio de sesión que no está vinculado ni se aprovisiona (su dirección ya tiene una identidad, o la conexión no aprovisiona) se rechaza, y aparece una **solicitud de vínculo** en **Identity providers → Link requests**. El superusuario la aprueba o la rechaza. Si la identidad ya tiene membresías en los inquilinos de la conexión que ninguna conexión gestiona, la aprobación exige una elección: `convert` hace que la conexión las gestione a partir de entonces, `keep_manual` las deja como están. Una solicitud marcada como *subject changed* significa que la identidad ya está vinculada en esa conexión con una cuenta distinta, que es el aspecto de una dirección de correo reciclada; trátala con sospecha.
- **Un superusuario vincula directamente** (`linkIdentity`, `unlinkIdentity` en la API de administración). Una conexión de instancia solo la vincula un superusuario: de esta forma, o aprobando una solicitud de vínculo.
- **La persona vincula su propia cuenta.** Con la sesión iniciada con contraseña, puede iniciar `beginSsoLink`. Nadie indica una conexión: DeviceChain usa la conexión de inquilino cuyo dominio verificado es el dominio del correo de la propia identidad (la misma búsqueda que usa un inicio de sesión por correo), y debe estar habilitada y aprobada. El proveedor debe informar la dirección de correo de la propia identidad. En la consola, elige **Vincular cuenta de inicio de sesión único** en el menú de usuario y continúa al proveedor; la misma acción está disponible en la API. El inicio de sesión con contraseña que necesita dura 15 minutos, así que pasado ese tiempo la consola pide volver a iniciar sesión primero. Cada rechazo dice qué comprobación falló: antes del proveedor, `beginSsoLink` falla con un `extensions.code` (`SSO_LINK_NO_CONNECTION`, `SSO_LINK_ALREADY_LINKED`, `SSO_LINK_SESSION_REQUIRED`, `SSO_LINK_PASSWORD_REQUIRED`, `SSO_LINK_NOT_ALLOWED`); después, la dirección de retorno lleva `error=link_email_mismatch`, `link_unverified_email`, `link_session_ended`, `link_already_linked` o `link_not_allowed`.

Una conexión de inquilino rechaza una identidad que tenga cualquier rol de sistema, incluso si el vínculo se hizo antes de conceder el rol. Una identidad vinculada a una conexión no puede tomar una segunda cuenta en esa misma conexión.

La dirección de correo de una identidad nunca se actualiza a partir de las afirmaciones de un proveedor.

## Asignación de grupos a roles {#role-mapping}

En una conexión de inquilino, cada inicio de sesión recalcula las membresías **gestionadas** de la persona en los inquilinos de la conexión: los roles por defecto de la conexión, más el rol de cada asignación cuyo grupo aparece en la afirmación de grupos del token. Si no hay ningún rol, la membresía gestionada se deshabilita; si falta una, se crea. Las membresías que un administrador hizo a mano nunca se tocan, ni tampoco las gestionadas por otra conexión.

Los límites son deliberados:

- **Solo roles de inquilino.** Una asignación nunca puede nombrar un rol de sistema. Una asignación, o un rol por defecto, que conlleve toda la autoridad de un inquilino necesita un reconocimiento explícito y se audita.
- **Se aplica al iniciar sesión.** Un cambio en el proveedor llega a DeviceChain en el siguiente inicio de sesión de la persona, o cuando su sesión alcance su límite (más abajo).
- **Los grupos deben llegar en el token.** Si la conexión asigna grupos y el proveedor informa de que una persona tiene demasiados grupos para listarlos (el "groups overage" de Microsoft Entra ID), el inicio de sesión se **rechaza** en lugar de tratarse como "sin grupos", lo cual deshabilitaría el acceso de la persona. Se corrige en el proveedor enviando solo los grupos que la aplicación necesita. También se rechaza una afirmación de grupos que no sea ni una cadena ni una lista de cadenas, o que liste más de 1.000 grupos.
- **Eliminar una conexión deshabilita, nunca elimina,** las membresías que gestionaba, de modo que un rol que concedió el proveedor nunca se convierte en silencio en una concesión manual.

## Exigir el inicio de sesión único en un inquilino {#require-sso}

Un superusuario puede activar **Require SSO** en un inquilino (**Admin → Tenants → el inquilino**, o `setTenantSsoEnforced`). Entonces:

- Una sesión con contraseña no puede entrar en ese inquilino. La consola explica por qué y lleva a la persona a **Sign in with SSO**; las contraseñas en la página de autorización de MCP se rechazan del mismo modo.
- Las sesiones con contraseña que ya estaban dentro del inquilino terminan en su siguiente renovación.
- **Los superusuarios están exentos**, y cada una de sus entradas se audita, de modo que una caída del proveedor de identidad nunca deja fuera al operador.
- **El rechazo del inquilino varado.** Activarlo requiere al menos una conexión *habilitada* que sirva al inquilino. Y a la inversa: no puedes deshabilitar ni eliminar la última conexión habilitada que sirve a un inquilino que exige inicio de sesión único, ni quitar ese inquilino de ella. DeviceChain lo rechaza en lugar de readmitir contraseñas en silencio. Desactiva primero el requisito, o habilita otra conexión que sirva al inquilino, y luego haz el cambio.

Exigir el inicio de sesión único no elimina la contraseña de nadie. Para que una persona concreta use solo SSO, un administrador usa `clearPassword`, que se rechaza salvo que la identidad esté vinculada a una conexión habilitada por la que pueda iniciar sesión.

## Límite de sesión, cierre de sesión y revocación {#sessions}

Cada conexión tiene un **límite de sesión**: cuánto puede durar un inicio de sesión a través de ella, contado desde el momento en que DeviceChain lo aceptó (no desde la hora de autenticación del propio proveedor) y nunca ampliado por una renovación. Por defecto son 12 horas; el rango va de 15 minutos a 7 días, y el límite superior es menor si la vida útil del token de actualización del despliegue es más corta. Cuando pasa el límite, la sesión termina en su siguiente intercambio y la persona inicia sesión de nuevo. Se aplica a las sesiones ya abiertas.

- **Cerrar sesión** desde la consola o los paneles termina la sesión de DeviceChain en el servidor. No cierra la sesión de la persona en el proveedor de identidad.
- **Revocar** (`revokeSessions` en la API de administración; la consola aún no tiene un botón para ello) termina todos los inicios de sesión de una identidad en todos los dispositivos, con contraseña y SSO, incluidas las concesiones de MCP.
- **Deshabilitar o eliminar una conexión**, o desvincular una identidad, termina las sesiones que dependían de ella en su siguiente intercambio.

### Ventanas residuales {#residual-windows}

Terminar una sesión detiene la *renovación*; no puede recuperar un token ya emitido. Tras un cierre de sesión, una revocación, una desvinculación o la deshabilitación de una conexión, **el token de acceso y el token de identidad ya emitidos siguen funcionando hasta que caducan: 15 minutos por defecto**, también en la API de administración. Dar de baja a alguien en el proveedor sin ninguna acción en DeviceChain solo llega a DeviceChain en su siguiente inicio de sesión o cuando el límite de sesión termina la sesión, así que el máximo es el límite (12 horas por defecto) más hasta 15 minutos. En esta versión no hay aprovisionamiento SCIM ni cierre de sesión por canal secundario. Si esa ventana es demasiado larga para una población, acorta el límite de la conexión, y usa `revokeSessions` para una salida urgente.

## MCP y agentes de IA {#mcp}

La página de autorización OAuth que usan los clientes MCP ofrece **Sign in with SSO** junto al formulario de contraseña. La persona inicia sesión en su proveedor y vuelve a la misma solicitud de autorización, que DeviceChain conservó, y ve la **pantalla de consentimiento: el consentimiento nunca se omite**, incluso si el proveedor la inició en silencio. La concesión pertenece a ese inicio de sesión y termina con él (su límite, o una revocación). Un inquilino que exige inicio de sesión único rechaza el formulario de contraseña de la página, así que una organización que solo usa SSO sigue conectando clientes MCP. Consulta [Acceso de IA (MCP)](./mcp.md).

## La dirección pública (`issuerUrl`) {#issuer-url}

Todas las partes del flujo de inicio de sesión vuelven a la dirección pública de esta instancia, que es el `auth.issuerUrl` de user-management. El chart la **deriva** del ingress como `<scheme>://<host>/api/user-management` cuando la instancia se sirve por https o en un host de loopback, y un valor explícito siempre gana. Sin emisor no hay inicio de sesión único ni servidor de autorización OAuth: crear una conexión falla con "sso requires a public issuer URL". El URI de redirección que registras en el proveedor es `<issuerUrl>/sso/callback/<connection token>`, que se muestra en la página de la conexión como su **Callback address**.

### Detrás de un balanceador de carga {#load-balancer}

El inicio público del inicio de sesión tiene un límite de frecuencia **por dirección de origen**, y esa dirección es la última entrada de `X-Forwarded-For`, o el par de la conexión cuando no hay ninguna. Si tu balanceador de carga o ingress reemplaza la dirección de cada cliente por la suya (un balanceador de carga en la nube, en su modo por defecto, aplica NAT de origen al tráfico), todos tus usuarios comparten una única cuota de cinco inicios y luego uno cada dos segundos, y los usuarios legítimos son rechazados en las horas punta. Conserva la dirección del cliente: `externalTrafficPolicy: Local` en el Service del controlador de ingress, o el protocolo proxy del balanceador de carga con el ingress configurado para leerlo. Un límite global y una cota del almacén de estado de inicios de sesión se mantienen sea cual sea el origen, y cuando el almacén se llena, iniciar un inicio de sesión falla cerrado (consulta la alerta `SsoStateStoreFull` en [Configurar el inicio de sesión único](../guides/configuring-sso.md#alerts)) mientras el inicio de sesión con contraseña continúa.

## Qué se audita {#audit}

El diario de auditoría registra la creación, actualización y eliminación de conexiones, los cambios de secretos, la adición y eliminación de dominios, la aprobación del descubrimiento, el vínculo y la desvinculación, las aprobaciones de vínculos, cada vez que un superusuario omite el requisito de inicio de sesión único de un inquilino, las entradas en inquilinos de una conexión de instancia, y cada rechazo con la dirección que afirmó el proveedor. Los tokens y códigos en bruto nunca se registran.

## Ver también

- [Configurar el inicio de sesión único](../guides/configuring-sso.md): configuración, recetas por proveedor, ajustes y alertas.
- [Multitenencia](./multi-tenancy.md): inquilinos, membresías y roles.
- [Acceso de IA (MCP)](./mcp.md): cómo se autorizan los clientes MCP.
