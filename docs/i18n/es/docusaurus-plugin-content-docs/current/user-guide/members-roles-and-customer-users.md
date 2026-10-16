---
title: Miembros, roles y usuarios de cliente
sidebar_position: 19
---

# Miembros, roles y usuarios de cliente

Este capítulo es para los **administradores del inquilino**. Trata de las personas que pertenecen a
tu inquilino, los roles que tienen y los accesos que das a tus propios clientes para que puedan
vigilar sus equipos sin ver los de nadie más.

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
- lo que **Puede ver**: **Todo el inquilino**, o los clientes a los que está limitada,
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
- **Puede ver**: **Todo el inquilino**, o **Clientes concretos** y después hasta 16 clientes. Un
  miembro limitado a clientes es un *usuario de cliente*, que se describe [más abajo](#customer-users).

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

- **Editar acceso** cambia los **Roles** del miembro y lo que **Puede ver**.
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

Un usuario de cliente es un miembro cuyo acceso está limitado a uno o varios de tus clientes. Úsalo
para dar a un cliente tuyo su propio acceso: ve sus propios equipos y nada de los de nadie más.

### Qué ve un usuario de cliente {#what-a-customer-user-sees}

La barra lateral de un usuario de cliente contiene solo **Panel**, **Paneles**, **Alarmas** y
**Dispositivos**, y en ellas:

- **Dispositivos** lista los dispositivos asignados ahora a sus clientes. Un dispositivo que pasa a
  otro cliente sale de su lista de inmediato, sin que nadie edite al usuario.
- La página de un dispositivo muestra su estado en vivo, las últimas lecturas, los eventos, los
  comandos y, si tu instalación lo ejecuta, sus actualizaciones. Los eventos y las alarmas son los
  registrados mientras el dispositivo pertenecía a uno de sus clientes: un cliente que recibe un
  dispositivo no ve su historial anterior, y un cliente que lo cede conserva el historial del tiempo
  en que fue suyo.
- **Alarmas** lista las alarmas generadas en esos dispositivos mientras pertenecían a sus clientes.
  Un usuario de cliente puede reconocerlas y borrarlas si su rol lo permite.
- **Paneles** lista los paneles publicados compartidos con sus clientes y abre su versión
  publicada. Nunca ve un borrador.

Todo lo demás, desde los perfiles de dispositivo y las reglas hasta los conectores, la auditoría y
la configuración, no aparece en su barra lateral, y abrir una de esas páginas por su dirección lo
lleva de vuelta a la página de inicio. El servidor le rechaza esas operaciones diga lo que diga su
rol, así que un usuario de cliente no puede cambiar tu configuración ni siquiera con un rol que se
lo permitiría al personal. Tampoco puede ver la asignación, las facetas ni la configuración
almacenada de un dispositivo.

El distintivo de inquilino, arriba a la derecha, muestra a qué está limitado y su rol, por ejemplo
**Acme · viewer** (el nombre del cliente y después el token del rol).

Para que un usuario de cliente pueda enviar comandos, dale un rol que incluya `command:write`.
Entonces puede enviar un comando a los dispositivos de sus propios clientes, y se le rechaza para
cualquier otro.

### Desde la página de un cliente {#from-a-customers-page}

La página de un cliente en la consola tiene dos pestañas para esto:

- **Dispositivos** lista los dispositivos asignados al cliente: los que ven sus usuarios. Asigna un
  dispositivo desde la pestaña **Asignación** del propio dispositivo.
- **Usuarios** lista los miembros limitados a este cliente. **Invitar usuario** abre la invitación
  con **Puede ver** ya fijado en este cliente. La pestaña solo se muestra a los administradores que
  pueden leer los miembros.

{/* TODO(screenshot): customer-users, a customer's Users tab. */}

### Compartir paneles con clientes {#sharing-dashboards}

Un usuario de cliente ve un panel solo cuando se comparte con uno de sus clientes y se publica. En
la lista de **Paneles**, elige **Compartir** en un panel y elige los clientes. Consulta
[Compartir con clientes](/user-guide/dashboards#sharing-with-customers).

Un panel compartido muestra a cada usuario de cliente solo sus propios datos. Vincula sus widgets a
dispositivos, o al cliente con la relación `assigned`: un widget así muestra a los usuarios del
cliente los dispositivos asignados a él. Un widget vinculado a un activo o un área lee el grafo de
relaciones para encontrar sus dispositivos, y los usuarios de cliente no tienen acceso a él, así que
les muestra **Not available for customer users** (no disponible para usuarios de cliente).

### Lo que todavía no se cubre {#not-covered-yet}

- Todavía no se puede limitar un miembro a un grupo de dispositivos en lugar de a clientes.
- Los activos y las áreas asignados a un cliente no se muestran a sus usuarios.
- Un cliente no puede gestionar sus propios usuarios. Tus administradores los invitan y los cambian.

## Relacionado

- [Activos, clientes y áreas](/user-guide/assets-customers-areas)
- [Paneles](/user-guide/dashboards#sharing-with-customers)
- [Ver paneles en /dash](/user-guide/viewing-dashboards-in-dash)
- [API de GraphQL: cuando tu acceso se limita a algunos clientes](/reference/graphql-api#limited-access)
- [Gestión de asignaciones](/guides/managing-assignments): vincular dispositivos a clientes.
