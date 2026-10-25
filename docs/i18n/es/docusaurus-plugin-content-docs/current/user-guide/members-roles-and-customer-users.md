---
title: Miembros, roles y usuarios de cliente
sidebar_position: 19
---

# Miembros, roles y usuarios de cliente

Este capítulo es para los **administradores del inquilino**. Trata de las personas que pertenecen a
tu inquilino, los roles que tienen y los accesos que das a tus propios clientes para que puedan
vigilar sus equipos sin ver los de nadie más, tanto si los limitas a clientes como a grupos de
dispositivos.

Dos páginas del grupo **Configuración** de la barra lateral se encargan de esto: **Miembros** y
**Roles**. Cada una necesita su propio permiso. **Miembros** lista a las personas con `member:read`
y las cambia con `member:write`; **Roles** lista los roles con `role:read` y los cambia con
`role:write`. El rol integrado de administrador del inquilino tiene los cuatro. Sin un permiso, su
página no aparece en tu barra lateral.

## Miembros {#members}

**Miembros** lista a todas las personas que pertenecen al inquilino, ordenadas por dirección de
correo. Para cada persona muestra:

- su nombre, si lo ha definido, y su dirección de correo,
- los **Roles** que tiene,
- lo que **Puede ver**: **Todo el inquilino**, o los clientes y grupos de dispositivos a los que
  está limitada,
- su estado: **Habilitado**, **Deshabilitado**, o **Invitado** mientras una invitación espera a ser
  aceptada. Una membresía que procede del proveedor de identidad de tu inquilino se marca además
  como **Proveedor de identidad**.

{/* TODO(screenshot): members-list, the Members page with staff and customer users. */}

El menú al final de una fila contiene las acciones para esa persona. Tu propia fila no tiene
ninguna: nadie cambia aquí su propia membresía, así que tiene que hacerlo otro administrador. Una
membresía marcada como **Proveedor de identidad** tampoco tiene ninguna, porque la gestiona el
proveedor de identidad y se cambia allí.

### Invitar a un miembro {#inviting-a-member}

Elige **Invitar miembro** y rellena:

- **Correo electrónico**, la dirección con la que la persona inicia sesión.
- **Roles**, lo que puede hacer. Solo puedes dar roles cuyas autoridades tengas tú.
- **Puede ver**: **Todo el inquilino**, o **Clientes o grupos concretos**. Después elige
  **Clientes**, **Grupos de dispositivos** o ambos: hasta 16 en total. Un miembro limitado a
  clientes o grupos es un *usuario de cliente*, que se describe [más abajo](#customer-users). Solo
  se ofrecen los grupos usados como ámbito de acceso; si falta un grupo, actívalo en la página del
  grupo, como se describe en [Limitar un miembro a un grupo de dispositivos](#group-scopes).

Vuelve a elegir **Invitar miembro** para crear la invitación. La consola muestra entonces un
**Enlace de invitación**. Cópialo y envíaselo tú a la persona: DeviceChain no lo envía por correo.

{/* TODO(screenshot): member-invite, the invitation drawer showing the one-time link. */}

El enlace se muestra **una sola vez**. El servidor no guarda ninguna copia que pueda volver a
mostrarte, así que si el enlace se pierde, vuelve a emitir la invitación (más abajo), lo que deja sin
efecto el enlace anterior. Un enlace es válido durante 7 días. Hasta que la persona acepta, su
membresía sigue deshabilitada y se marca como **Invitado**.

La respuesta es la misma tanto si la persona ya tiene una cuenta de DeviceChain como si no, así que
invitar a alguien no te revela si tiene una.

### Aceptar una invitación {#accepting-an-invitation}

El enlace de invitación abre la página **Unirse a un inquilino**. La persona puede:

- elegir **Cuenta nueva** y definir una contraseña, o
- elegir **Ya tengo una cuenta** e iniciar sesión con la cuenta a la que se envió la invitación, con
  su contraseña o con inicio de sesión único.

Si tu inquilino exige inicio de sesión único, la opción de contraseña se rechaza y la página pide a
la persona que inicie sesión con SSO. Una vez aceptada la invitación, la persona entra en el
inquilino. Si pertenece a más de un inquilino, elige cuál abrir.

Un enlace que no se puede usar (caducó, se revocó, ya se usó o se envió a otra cuenta) recibe una
única respuesta, "Esta invitación no se puede usar", sea cual sea el motivo. Pide uno nuevo.

### Cambiar el acceso de un miembro {#changing-access}

Desde el menú de una fila:

- **Editar acceso** cambia los **Roles** del miembro y lo que **Puede ver**. Si cambias ambos en un
  mismo guardado, se guardan juntos: si se rechaza uno, no cambia ninguno.
- **Deshabilitar** deja al miembro fuera de este inquilino sin quitarlo, y **Habilitar** le vuelve a
  dar entrada.
- **Quitar** saca al miembro del inquilino. Su cuenta se conserva y se le puede volver a invitar.
- Para un miembro que sigue marcado como **Invitado**, **Volver a emitir la invitación** muestra un
  enlace nuevo y deja sin efecto el anterior, y **Revocar invitación** deja sin efecto el enlace sin
  emitir otro.

Un cambio surte efecto la próxima vez que se renueva la sesión del miembro: en 15 minutos como
máximo, o en 5 minutos para un usuario de cliente. Eso también vale para retirar acceso. El miembro
no tiene que cerrar sesión, y sus sesiones en otros inquilinos no se ven afectadas.

Algunos cambios se rechazan, con un mensaje que explica el motivo:

- No puedes dar una autoridad que no tienes, ni cambiar a un miembro que tiene una que tú no
  tienes.
- No puedes cambiar tu propia membresía.
- No puedes dejar el inquilino sin un miembro habilitado que vea todo el inquilino y pueda gestionar
  los miembros.
- Una membresía gestionada por el proveedor de identidad no se puede cambiar aquí.

### Invitaciones {#invitations}

Debajo de la lista de miembros, **Invitaciones** lista todas las invitaciones emitidas en el
inquilino, de la más reciente a la más antigua, con su estado (**En espera**, **Aceptada**,
**Caducada** o **Revocada**), cuándo se emitió y cuándo caduca.

## Roles {#roles}

**Roles** lista los roles que puedes dar a tus miembros. **Definido por** indica de dónde procede
cada uno:

- Los roles de **Plataforma** los define el operador de tu instancia, como los integrados de
  lector y de administrador del inquilino. Puedes asignarlos, y abrir uno para ver sus autoridades,
  pero solo el operador puede cambiarlos.
- Los roles de **Este inquilino** son tuyos. Solo los tiene tu inquilino.

{/* TODO(screenshot): roles-list, the Roles page with platform and tenant roles. */}

Elige **Nuevo rol** para definir uno. Dale un token (no se puede cambiar después), un nombre y una
descripción, y elige sus **Autoridades** en la lista de casillas. La lista agrupa las autoridades
por el área sobre la que actúan, así que `device:read` y `device:write` aparecen juntas bajo
`device`. `*` concede todas las autoridades del inquilino, incluidas las que se añadan en versiones
posteriores.

{/* TODO(screenshot): role-detail, a tenant role with the grouped authority checklist. */}

Las autoridades que no tienes aparecen atenuadas: nadie puede conceder más de lo que tiene. Un rol
solo puede llevar autoridades del inquilino, así que nunca se ofrecen permisos de nivel de
instancia.

Cuando cambias un rol, todos los que lo tienen reciben el cambio en la siguiente renovación de su
sesión, como se describe en [Cambiar el acceso de un miembro](#changing-access). **Eliminar rol** se
rechaza mientras algún miembro tenga todavía el rol; quítaselo primero.

## Usuarios de cliente {#customer-users}

Un usuario de cliente es un miembro cuyo acceso está limitado a uno o varios de tus clientes, a uno o
varios grupos de dispositivos, o a ambos. Úsalo para dar a un cliente tuyo su propio acceso: ve sus
propios equipos y nada de los de nadie más. Limítalo a un cliente cuando los dispositivos están
asignados a él, o a un grupo cuando los dispositivos que debe ver son los que cumplen un filtro sobre
sus atributos, como todos los dispositivos de una región.

### Qué ve un usuario de cliente {#what-a-customer-user-sees}

La barra lateral de un usuario de cliente contiene solo **Inicio**, **Paneles**, **Alarmas** y
**Dispositivos**, y en ellas:

- **Dispositivos** lista los dispositivos asignados ahora a sus clientes y los que están ahora en sus
  grupos. Un dispositivo que pasa a otro cliente, o que sale del grupo, sale de su lista de
  inmediato, sin que nadie edite al usuario.
- La página de un dispositivo muestra su estado en vivo, las últimas lecturas, los eventos, los
  comandos y, si tu instalación lo ejecuta, sus actualizaciones. Los eventos y las alarmas son los
  registrados mientras el dispositivo pertenecía a uno de sus clientes, o estaba en uno de sus
  grupos: un cliente que recibe un dispositivo no ve su historial anterior, y un cliente que lo cede
  conserva el historial del tiempo en que fue suyo.
- **Alarmas** lista las alarmas generadas en esos dispositivos mientras pertenecían a sus clientes o
  estaban en sus grupos. Un usuario de cliente puede reconocerlas y borrarlas si su rol lo permite.
- **Paneles** lista los paneles publicados compartidos con sus clientes o grupos y abre su versión
  publicada. Nunca ve un borrador.

Todo lo demás, desde los perfiles de dispositivo y las reglas hasta los conectores, la auditoría y
la configuración, no aparece en su barra lateral, y abrir una de esas páginas por su dirección lo
lleva de vuelta a la página de inicio. El servidor le rechaza esas operaciones diga lo que diga su
rol, así que un usuario de cliente no puede cambiar tu configuración ni siquiera con un rol que se
lo permitiría al personal. Tampoco puede ver la asignación, las facetas ni la configuración
almacenada de un dispositivo.

El distintivo de inquilino, arriba a la derecha, muestra a qué está limitado y su rol, por ejemplo
**Acme · viewer** o **Acme, Ala norte · viewer** (los nombres de sus clientes y grupos y después el
token del rol).

Para que un usuario de cliente pueda enviar comandos, dale un rol que incluya `command:write`.
Entonces puede enviar un comando a los dispositivos que puede ver, y se le rechaza para cualquier
otro.

### Desde la página de un cliente {#from-a-customers-page}

La página de un cliente en la consola tiene dos pestañas para esto:

- **Dispositivos** lista los dispositivos asignados al cliente: los que ven sus usuarios. Asigna un
  dispositivo desde la pestaña **Asignación** del propio dispositivo.
- **Usuarios** lista los miembros limitados a este cliente. **Invitar usuario** abre la invitación
  con **Puede ver** ya fijado en este cliente. La pestaña solo se muestra a los administradores que
  pueden leer los miembros.

{/* TODO(screenshot): customer-users, a customer's Users tab. */}

### Limitar un miembro a un grupo de dispositivos {#group-scopes}

Un miembro se puede limitar a un grupo de dispositivos en lugar de a clientes, o además de a ellos.
Antes, el grupo tiene que ser un **ámbito de acceso**, lo que se fija en el propio grupo:

1. El grupo debe ser un grupo **dinámico** de dispositivos: uno cuyos miembros elige un filtro sobre
   sus atributos. Guarda uno desde la pantalla **Explorar**, como se describe en
   [Grupos, facetas y Explorar](/user-guide/groups-facets-browse).
2. Abre el grupo en **Grupos de dispositivos** y elige su pestaña **Ámbito de acceso**.
3. Un grupo dinámico que nunca se ha publicado muestra **Publicar filtro**. Elígelo para publicar el
   filtro del grupo como su primera versión. Un ámbito de acceso sigue la versión publicada del
   grupo, así que el filtro tiene que publicarse antes de que el grupo pueda serlo. Si otra persona
   publica el grupo mientras tienes la pestaña abierta, **Publicar filtro** no publica una segunda
   versión encima de la suya: la pestaña indica que el grupo cambió y muestra su estado actual.
4. Marca **Usar como ámbito de acceso**. Hace falta `device:write`.

{/* TODO(screenshot): group-access-scope, a device group's Access scope tab with the box ticked. */}

Cuando un grupo no puede ser un ámbito de acceso, la pestaña dice por qué: solo puede serlo un grupo
de dispositivos, y solo uno dinámico. Un inquilino puede tener como máximo 32 grupos de ámbito de
acceso; marcar la casilla para uno más se rechaza, y la pestaña lo dice.

Un miembro limitado al grupo ve entonces sus dispositivos, y solo mientras están en él. La
pertenencia sigue los atributos de los dispositivos, así que un dispositivo cuyos atributos lo meten
en el grupo aparece para el miembro sin que nadie lo edite, y uno que sale desaparece. Lo que ve
registrado sobre un dispositivo (sus eventos, alarmas, comandos y actualizaciones) es lo registrado
mientras estaba en el grupo, la misma regla que se aplica a un cliente.

Desmarcar **Usar como ámbito de acceso** pide confirmación. Los miembros limitados al grupo dejan de
ver sus dispositivos, y el grupo deja de ofrecerse en la página **Miembros** y al compartir un panel.
Se conservan sus membresías y los paneles compartidos con el grupo: ese miembro puede seguir
abriendo esos paneles, pero no le muestran ninguno de los dispositivos del grupo. Volver a marcar la
casilla le devuelve lo que veía.

### Compartir paneles con clientes y grupos {#sharing-dashboards}

Un usuario de cliente ve un panel solo cuando se comparte con uno de sus clientes o grupos y se
publica. En la lista de **Paneles**, elige **Compartir** en un panel y elige los clientes y los
grupos. Consulta [Compartir con clientes y grupos](/user-guide/dashboards#sharing-with-customers).

Un panel compartido muestra a cada usuario de cliente solo sus propios datos. Vincula sus widgets a
dispositivos, o al cliente con la relación `assigned`: un widget así muestra a los usuarios del
cliente los dispositivos asignados a él. Un widget vinculado a un activo, un área o un grupo lee el
grafo de relaciones para encontrar sus dispositivos, y los usuarios de cliente no tienen acceso a
él, así que les muestra **Not available for customer users** (no disponible para usuarios de
cliente).

### Lo que todavía no se cubre {#not-covered-yet}

- Un widget de panel todavía no puede mostrar los dispositivos del grupo de un miembro como un
  conjunto. En un panel compartido con un grupo, vincula los widgets a sus dispositivos.
- Después de la primera versión de un grupo, publicar una versión nueva, que cambia lo que ven sus
  miembros, se hace con la API (`publishEntityGroup`), no desde la consola.
- Los activos y las áreas asignados a un cliente no se muestran a sus usuarios.
- Un cliente no puede gestionar sus propios usuarios. Tus administradores los invitan y los cambian.

## Relacionado

- [Activos, clientes y áreas](/user-guide/assets-customers-areas)
- [Paneles](/user-guide/dashboards#sharing-with-customers)
- [Ver paneles en /dash](/user-guide/viewing-dashboards-in-dash)
- [API de GraphQL: cuando tu acceso se limita a algunos clientes](/reference/graphql-api#limited-access)
- [Gestión de asignaciones](/guides/managing-assignments): vincular dispositivos a clientes.
- [Cuentas de servicio](/user-guide/service-accounts): cuentas no humanas para integraciones.
