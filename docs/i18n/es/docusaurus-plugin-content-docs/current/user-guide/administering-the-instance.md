---
title: Administración de la instancia
sidebar_position: 15
---

import GuideShot from '@site/src/components/GuideShot';

# Administración de la instancia

Este capítulo es para los **operadores de la instancia**: los superusuarios que gestionan una
instalación de DeviceChain para todos los que la usan. Si trabajas dentro de un inquilino, no verás
estas pantallas y puedes saltarte el capítulo.

En la consola de administración creas y eliminas inquilinos, decides con qué empaquetado cuenta cada
uno, gestionas a las personas que inician sesión y los roles que tienen, conectas proveedores de
identidad externos, revisas lo que se ha cambiado en toda la instancia y fijas los valores
predeterminados para toda la instancia.

## Abrir la consola de administración

Un superusuario que inicia sesión llega directamente a la consola de administración, sin elegir
inquilino. Desde la consola del inquilino, el menú de usuario también ofrece **Consola de
administración** ([el menú de usuario](./signing-in.md#the-user-menu)). La consola de administración
solo se abre para un superusuario; a cualquier otra persona se la devuelve a la página de inicio de
sesión.

La barra lateral muestra **Consola de administración** bajo el logotipo, y una insignia **Admin**
aparece a la derecha de la cabecera. Sus entradas, bajo **Plataforma**, son:

- **Inquilinos**, con **Lista**, **Niveles** y **Eliminaciones**.
- **Identidades**, con **Lista**, **Roles** y **Proveedores de identidad**.
- **Proveedores de IA**, **Auditoría** y **Configuración**.

## Inquilinos

**Inquilinos → Lista** es el registro de inquilinos de la instancia. Un inquilino es un registro que
la plataforma guarda, no algo que aprovisiona: crear uno surte efecto al instante, sin servidores ni
servicios nuevos.

<GuideShot id="admin-tenants" alt="La lista de inquilinos con tres filas. harbor-view-pilot, llamado Harbor View pilot, está en el nivel pilot y muestra una insignia Deshabilitado y una insignia roja de eliminación en curso desde el 14/10/2026. sim-acme y sim-widgets están en el nivel silver y habilitados. La columna de configuración está vacía en las tres. Arriba a la derecha hay un botón Nuevo inquilino." />

Cada fila muestra el token y el nombre del inquilino, su nivel como una etiqueta de color, su estado y
cualquier configuración libre. Un inquilino que se está eliminando conserva su fila, marcada como en
eliminación con la fecha en que empezó, hasta que la eliminación termina.

### Crear un inquilino

Elige **Nuevo inquilino**. El formulario pide:

- **Token**: el identificador del inquilino en toda la plataforma. Se genera a partir del nombre con
  la [máscara de token](../concepts/system-settings.md#token-masks) de la instancia, que aparece bajo
  el campo, y el botón de al lado genera otro. No se puede cambiar después.
- **Nombre**: un nombre para mostrar.
- **Nivel**: el empaquetado al que se sujeta el inquilino (consulta [Niveles](#tiers)). Todo
  inquilino tiene uno.
- **Configuración (JSON)**: un objeto JSON libre y opcional.
- Los límites propios del inquilino, descritos más abajo en [Ajustes](#a-tenants-settings).
  Déjalos en blanco para heredarlos.

Después elige **Crear inquilino**.

<GuideShot id="admin-tenant-new" alt="El formulario Nuevo inquilino. El campo Token muestra la máscara {slug} con un botón para regenerarla y la pista del patrón. Debajo están Nombre, un selector de nivel vacío, Configuración (JSON), la tasa y la ráfaga de ingesta, la tasa y la ráfaga de salida, todas con el valor predeterminado, y la casilla de enrutamiento externo de IA sin marcar." />

### La página de un inquilino

Abre un inquilino para editarlo. Su página muestra el nombre con el token al lado, las insignias de
nivel y de estado, y dos acciones: **Deshabilitar** (o **Habilitar**) y **Eliminar**. Un inquilino
deshabilitado no admite a nadie hasta que lo vuelvas a habilitar.

Sobre las pestañas, el panel **Inicio de sesión único** indica si las personas del inquilino deben
iniciar sesión mediante inicio de sesión único. **Exigir SSO** te pide confirmación, porque las
sesiones con contraseña del inquilino terminan en su siguiente renovación. Se rechaza si ninguna
conexión de proveedor de identidad habilitada atiende al inquilino. Los superusuarios nunca están
sujetos a ello, así que una caída del proveedor de identidad no puede dejarte fuera. Consulta
[Exigir el inicio de sesión único en un inquilino](../concepts/single-sign-on.md#require-sso).

La pestaña **Básico** edita el nombre, el nivel y la configuración. El token se muestra, pero no se
puede cambiar. Un cambio de nivel se aplica en menos de un minuto.

<GuideShot id="admin-tenant-basic" alt="La página del inquilino acme (sim), con token sim-acme, insignias de nivel silver y Habilitado, y botones Deshabilitar y Eliminar. El panel Inicio de sesión único explica el ajuste, indica que no se exige y ofrece el botón Exigir SSO. Debajo están las pestañas Básico, Ajustes, Ajustes efectivos y Modelos de IA, con Básico abierta: el token sim-acme en gris, el nombre acme (sim), el nivel Silver y un campo de configuración JSON vacío." />

La página del inquilino también tiene un panel **Autenticación en dos pasos** con **Exigir dos pasos** (o **Dejar de exigir**). Exige un segundo factor a los inicios de sesión con contraseña del inquilino, y el botón no está disponible cuando el nivel del inquilino ya lo exige. Los superusuarios están exentos, y el inicio de sesión único sigue los requisitos de su propia conexión. Consulta [Autenticación en dos pasos](/guides/two-factor-authentication#require).

### Los ajustes de un inquilino {#a-tenants-settings}

La pestaña **Ajustes** contiene los límites de este inquilino concreto, cuando deben diferir de los de
su nivel. Un campo que dejes en blanco se hereda: primero el valor del nivel y, si no hay, el valor
predeterminado de la plataforma. Los campos son la tasa y la ráfaga de ingesta, la
tasa y la ráfaga de salida de los conectores de salida, el **Enrutamiento externo de IA** (el
consentimiento del inquilino para enviar descripciones de reglas a un modelo de IA externo,
desactivado de forma predeterminada), la tasa y la ráfaga de redacción de IA, el **Límite de comandos
retenidos**, la **Prioridad de reducción** y tres límites de geocercas: **Número de geocercas**,
**Puntos por geocerca** y **Presupuesto de puntos de geocercas**. Cada campo lleva una breve
explicación en la consola. **Guardar cambios** guarda a la vez las pestañas Básico y Ajustes.

<GuideShot id="admin-tenant-settings" alt="La pestaña Ajustes de acme (sim): la tasa y la ráfaga de ingesta y la tasa y la ráfaga de salida, todas con el valor predeterminado; la casilla de enrutamiento externo de IA sin marcar; la tasa y la ráfaga de redacción de IA; y el principio del límite de comandos retenidos." />

La pestaña **Ajustes efectivos** es de solo lectura. Muestra el resultado para las tasas y ráfagas de
ingesta, de salida y de redacción de IA: el valor del nivel, la anulación de este inquilino y el valor
vigente, con una insignia que indica de dónde procede. El límite de comandos retenidos, la prioridad
de reducción y los límites de geocercas se resuelven igual, pero esta pestaña no los muestra. Un ajuste recurre primero a su propia anulación, luego al nivel y después al valor
predeterminado de la plataforma, que es un límite real, nunca «ilimitado». Cuando el valor procede
del propio valor predeterminado de un servicio, la consola muestra **Establecido por el servicio**. El
nombre del nivel en la explicación enlaza con el nivel.

<GuideShot id="admin-tenant-effective" alt="La pestaña Ajustes efectivos de acme (sim). La explicación nombra el nivel Silver como enlace. Una tabla lista la tasa y la ráfaga de ingesta, la tasa y la ráfaga de salida y la tasa y la ráfaga de redacción de IA; las columnas de nivel y de anulación muestran un guion, y cada valor efectivo indica que lo establece el servicio, con una insignia de valor predeterminado de la plataforma." />

La pestaña **Modelos de IA** elige qué modelo de IA usa el inquilino para cada función de IA.
Necesita el servicio opcional de IA; en una instancia sin él, la pestaña indica que el área no está
desplegada. Para saber cómo funcionan los límites y los derechos de uso, consulta
[Niveles de inquilino y empaquetado](../concepts/tenant-tiers.md) y
[Gobernanza y cuotas](../concepts/governance.md).

### Eliminar un inquilino

**Eliminar** abre una confirmación. Léela: la eliminación no se puede deshacer. El acceso al
inquilino se revoca al instante, y sus dispositivos, paneles e historial se borran después en segundo
plano, lo que lleva al menos 12 horas. El token del inquilino queda reservado hasta que termina.

<GuideShot id="admin-tenant-delete-confirm" alt="La confirmación Eliminar inquilino sobre la página de acme (sim). Explica que el acceso se revoca de inmediato y que no se puede deshacer, que los dispositivos, paneles e historial del inquilino se borran después en segundo plano en al menos 12 horas, y que el token sim-acme queda reservado hasta entonces. Botones Cancelar y Eliminar." />

Mientras dura una eliminación, la página del inquilino pierde los botones **Deshabilitar** y
**Eliminar** y el panel de inicio de sesión único, y gana una pestaña **Eliminación**. La pestaña
empieza con una línea de estado que indica qué espera la eliminación. Por orden:

1. **Esperando a** uno o varios sistemas de almacenamiento que aún guardan datos del inquilino.
2. Todos los sistemas de almacenamiento informan que están limpios, y la eliminación espera un poco
   más para confirmar que no llega nada más.
3. La recuperación de datos ha terminado, y el token queda retenido antes de volver a estar
   disponible: unas 12 horas de forma predeterminada.
4. Todas las comprobaciones han pasado, y la eliminación se completa en la próxima pasada.

Debajo del estado, una tabla tiene una fila por cada sistema de almacenamiento
en el que el inquilino tenía datos, con su estado:

- **Limpio**: no queda nada del inquilino ahí.
- **Retiene datos**: todavía hay datos, y seguirán ahí hasta que alguien cambie algo. Cuando la
  consola conoce la solución, indica qué hacer bajo la fila.
- **Reintentando**: un intento falló, y la siguiente pasada lo vuelve a intentar sola.

Cada fila muestra también cuántos registros se borraron y cuándo se comprobó por última vez. La
columna **Detalle** recoge las notas del propio sistema de almacenamiento. Algunas son explicaciones
internas largas, en inglés, de lo que un sistema deja a propósito sin tocar, como registros de inicio
de sesión de vida corta; una nota en una fila **Limpio** es una aclaración, no un problema que
resolver. La pestaña se actualiza sola mientras la eliminación está en curso, y si aparece vacía justo
después de eliminar, es que la primera comprobación aún no se ha hecho; empieza en menos de un minuto.

En la versión en español de la consola, la línea de estado muestra la unidad de tiempo en inglés
(«unas 12 hours»).

<GuideShot id="admin-tenant-deletion" alt="La página de Harbor View pilot, con token harbor-view-pilot, insignias pilot, Deshabilitado y de eliminación en curso, y sin botones de acción. La pestaña Eliminación está abierta. Su línea de estado dice que la recuperación de datos ha terminado y que el token volverá a estar disponible en unas «12 hours», con la unidad en inglés. Una tabla lista los sistemas de almacenamiento, empezando por blob, broker, detect y kv, todos en estado Limpio, con 0 borrados y la hora de la última comprobación. La fila kv lleva una nota técnica larga en la columna Detalle." />

### La lista de eliminaciones

**Inquilinos → Eliminaciones** lista todas las eliminaciones de inquilinos de la instancia, de la más
reciente a la más antigua. Usa los botones **Todas**, **En curso** y **Completadas** para filtrarla.
Cada fila muestra el token, cuándo se solicitó la eliminación, su estado con el mismo resumen de una
línea que la pestaña Eliminación del inquilino, el número de registros borrados y cuántos sistemas de
almacenamiento están limpios o aún retienen datos.

<GuideShot id="admin-deletions" alt="La página Eliminaciones de inquilinos con los botones de filtro Todas, En curso y Completadas. Una fila: harbor-view-pilot, solicitada el 14/10/2026, en curso, con la nota de que la recuperación de datos ha terminado y el token volverá a estar disponible en unas 12 horas, 0 borrados y 6 sistemas, todos limpios." />

Esta lista es el registro permanente. Cuando una eliminación termina, la fila y la página del
inquilino desaparecen, así que una eliminación terminada solo aparece aquí. Ábrela cuando alguien te
pida demostrar que se borraron los datos de un cliente. Para el calendario completo, consulta
[Eliminación de inquilinos](../deployment/tenant-deletion.md).

## Niveles {#tiers}

Un nivel es el empaquetado que se le vende a un inquilino: los límites predeterminados a los que se
sujeta todo inquilino de ese nivel y qué modelos de IA pueden usar esos inquilinos. **Inquilinos →
Niveles** los lista, cada uno con su etiqueta de color, su nombre, su descripción y cuántos inquilinos
lo tienen.

<GuideShot id="admin-tiers" alt="La lista de niveles con cuatro filas, cada una con un asa para arrastrar: bronze (Bronze, empaquetado de entrada: el primero en reducirse bajo contención y un conjunto reducido de modelos de IA, entre otras cosas), gold (Gold, empaquetado premium: el último en reducirse bajo contención y el conjunto más amplio de modelos de IA, entre otras cosas), pilot (Pilot, para evaluaciones con plazo: un límite de ingesta pequeño y el primero en reducirse bajo carga) y silver (Silver, el empaquetado estándar), este último con 2 inquilinos y el resto con 0. Cada etiqueta de nivel tiene su propio color. Arriba a la derecha hay un botón Nuevo nivel." />

Arrastra una fila por su asa para cambiar el orden en que se listan los niveles. El orden es solo de
presentación: no es una clasificación, y nada se calcula a partir de él. **Nuevo nivel** crea uno.
Definir un nivel no cambia nada por sí solo; surte efecto cuando se crean inquilinos en él o se pasan
a él.

La página de un nivel tiene tres pestañas. **Básico** contiene el token, que no se puede cambiar, el
nombre, una descripción para los operadores y el **Color** de su etiqueta. El color es solo de
presentación.

<GuideShot id="admin-tier-basic" alt="La página del nivel Pilot, con token pilot, una etiqueta pilot, 0 inquilinos y un botón Eliminar. La pestaña Básico muestra el token pilot en gris, el nombre Pilot, la descripción del nivel, una fila de muestras de color con una verde azulada seleccionada junto a una etiqueta de vista previa, y un botón para guardar los cambios." />

**Ajustes** contiene los **Límites máximos** del nivel: las tasas y ráfagas de ingesta, de salida y de
redacción de IA. Deja un campo en blanco para usar el valor predeterminado de la plataforma. Un
inquilino concreto puede seguir teniendo su propia anulación en su pestaña Ajustes. Si hay inquilinos
en el nivel, guardar cambia sus límites en menos de un minuto, sin reiniciar nada.

Un nivel también puede llevar un límite de comandos retenidos, una prioridad de reducción y los tres
límites de geocercas, que los inquilinos del nivel heredan de la misma forma. La consola no tiene
campos para ellos en un nivel: se fijan mediante la API de administración, y guardar esta pestaña los
deja como están. El valor propio de un inquilino en su pestaña Ajustes sigue teniendo prioridad sobre
el del nivel.

<GuideShot id="admin-tier-settings" alt="La pestaña Ajustes del nivel Pilot, bajo el encabezado de límites máximos: tasa de ingesta 50, ráfaga de ingesta 100, tasa de salida 5, ráfaga de salida 10, y la tasa y la ráfaga de redacción de IA con el valor predeterminado de la plataforma. Un botón para guardar los cambios." />

**Modelos de IA** elige qué modelos de IA concede el nivel, y necesita el servicio opcional de IA. Un
nivel no se puede eliminar mientras algún inquilino lo tenga. Consulta
[Niveles de inquilino y empaquetado](../concepts/tenant-tiers.md).

La pestaña **Settings** de un nivel también tiene **Exigir autenticación en dos pasos**. Si se marca, todos los inquilinos del nivel deben usar un segundo factor en los inicios de sesión con contraseña, y un inquilino no puede desactivarlo. Consulta [Autenticación en dos pasos](/guides/two-factor-authentication#tier-floor).

## Identidades

**Identidades → Lista** es el directorio de personas de la instancia. Una persona es una identidad,
identificada por su dirección de correo, que puede ser miembro de muchos inquilinos. La lista muestra
el correo, el nombre, el estado, los roles de sistema y en cuántos inquilinos está cada identidad.

<GuideShot id="admin-identities" alt="La lista de identidades: dana.okafor, jonas.weber y maria.chen de acme-properties.example, habilitadas; lee.park de acme-properties.example, deshabilitada; superuser@devicechain.local, sin nombre, habilitada, con el rol de sistema superuser y 0 inquilinos; y sam.rivera@widgetlab.example, habilitada. Cada una de las personas pertenece a 1 inquilino. Arriba a la derecha hay un botón Nueva identidad." />

**Nueva identidad** pide un correo electrónico, una contraseña, el nombre y el apellido, y los
**Roles de sistema** que correspondan. Los roles de sistema dan acceso a la consola de administración
y a su API; déjalos vacíos para una persona normal. Las membresías en inquilinos se añaden cuando la
identidad ya existe.

<GuideShot id="admin-identity-new" alt="El formulario Nueva identidad, descrito como una identidad global identificada por su correo a la que se añaden membresías después de crearla. Campos: correo electrónico, contraseña, nombre, apellido y un selector de roles de sistema, con los botones Crear identidad (desactivado hasta rellenar el formulario) y Cancelar." />

La página de una identidad tiene **Deshabilitar** y **Eliminar** arriba. Una identidad deshabilitada
no puede iniciar sesión, y sus sesiones terminan en su siguiente renovación. Eliminarla borra la identidad y todas sus
membresías.

- **Acceso** fija los **Roles de sistema** de la identidad (y después **Guardar**) y sustituye su
  contraseña con **Establecer contraseña**.
- **Membresías** lista los inquilinos a los que pertenece, cada uno con su estado y sus roles de
  inquilino. Cambia los roles y elige **Guardar roles**, o **Deshabilitar** o **Quitar** la membresía.
  Una membresía deshabilitada impide a la persona entrar en ese inquilino concreto. Para añadir una,
  elige un inquilino y unos roles en **Añadir a inquilino** y pulsa **Añadir membresía**.

<GuideShot id="admin-identity-detail" alt="La página de Maria Chen, maria.chen@acme-properties.example, habilitada, con botones Deshabilitar y Eliminar. El panel Acceso tiene un selector de roles de sistema con Guardar y un campo para establecer la contraseña. El panel Membresías muestra sim-acme, habilitada, con el rol Facilities viewer y los botones Guardar roles, Deshabilitar y Quitar. Debajo, los selectores para añadir a un inquilino con roles y el botón Añadir membresía." />

La lista tiene una columna **Dos pasos**. En la página de una identidad, la sección **Autenticación en dos pasos** muestra si está activada y tiene **Restablecer dos pasos** para una persona que perdió el teléfono: elimina el autenticador y los códigos de recuperación y termina todas las sesiones. Si la identidad ha tenido códigos incorrectos recientemente, es posible que otra persona conozca su contraseña, así que el restablecimiento también pide una contraseña nueva. Consulta [Autenticación en dos pasos](/guides/two-factor-authentication#reset) y, para un operador que no puede iniciar sesión, [break-glass](/guides/two-factor-authentication#break-glass).

## Roles

**Identidades → Roles** es el catálogo de roles de toda la instancia. Un rol de **sistema** da acceso
a la consola de administración y a su API; un rol de **inquilino** determina lo que un miembro puede
hacer dentro de un inquilino. Cada rol lista sus permisos. `*` lo concede todo dentro del ámbito del
rol.

<GuideShot id="admin-roles" alt="La lista de roles. superuser es un rol de sistema con el permiso *. facilities-viewer, Facilities viewer, es un rol de inquilino con device:read, state:read, event:read, alarm:read, alarm:write, dashboard:read y location:read. tenant-admin, Tenant Administrator, es un rol de inquilino con *. viewer, Viewer, es un rol de inquilino con device:read, event:read, state:read, command:read, alarm:read y dashboard:read. Arriba a la derecha hay un botón Nuevo rol." />

Abre un rol para cambiar su nombre, su descripción y sus **Permisos**, y después guarda los cambios.
El ámbito y el token quedan fijados una vez que el rol existe. **Nuevo rol** crea uno; **Eliminar**
quita un rol a todas las personas que lo tienen.

Los administradores de cada inquilino asignan estos roles de inquilino a sus miembros, y también
pueden definir roles propios, que solo existen en ese inquilino y solo llevan permisos del inquilino.
Gestionan sus miembros, invitaciones y roles por sí mismos, sin la consola de administración;
consulta [Miembros, roles y usuarios de cliente](/user-guide/members-roles-and-customer-users).

<GuideShot id="admin-role-detail" alt="El rol de inquilino facilities-viewer. El ámbito muestra inquilino seleccionado y sistema no disponible. El token facilities-viewer en gris, el nombre Facilities viewer y una descripción que dice que lee dispositivos, estado en vivo, telemetría y alarmas, y reconoce alarmas. Los permisos son device:read, state:read, event:read, alarm:read, alarm:write, dashboard:read y location:read. Un botón para guardar los cambios y otro Eliminar." />

Los nombres y descripciones de los roles son datos que escribió quien los creó, así que aparecen tal
como se escribieron.

## Proveedores de identidad

**Identidades → Proveedores de identidad** conecta proveedores OpenID Connect externos, como Google
Workspace o Microsoft Entra ID, con los que las personas inician sesión. La lista muestra el nombre,
el tipo, el emisor, los dominios de correo que representa, si sus endpoints están aprobados y el
estado de cada conexión. Una conexión se crea deshabilitada, y no puede habilitarse hasta que alguien
apruebe los endpoints del proveedor.

<GuideShot id="admin-identity-providers" alt="La página Proveedores de identidad con una conexión: Acme Google Workspace, de tipo inquilino, emisor https://accounts.google.com, dominio acme-properties.example, endpoints aprobados y estado deshabilitado. Debajo, el panel Solicitudes de vinculación explica los inicios de sesión rechazados e indica que ninguno espera una decisión. Arriba a la derecha hay un botón Nueva conexión." />

Debajo de la lista, **Solicitudes de vinculación** reúne los inicios de sesión rechazados porque
coincidían con una identidad existente que no tiene vínculo con ese proveedor. Una solicitud no
concede nada hasta que la **apruebas**; **Rechazar** la borra.

**Nueva conexión** abre un formulario. El **Tipo** decide para quién es la conexión:

- **Inquilino**: un proveedor para las personas de uno o varios inquilinos. Sus inicios de sesión solo
  pueden entrar en los inquilinos que atiende, y nunca llegan a una identidad con un rol de sistema.
- **Instancia**: el proveedor de tus propios operadores. Solo se vincula a identidades mediante una
  acción explícita, y debe exigir un método de autenticación, como `mfa`, o quedar confirmado como un
  proveedor que impone el inicio de sesión multifactor. No atiende a ningún inquilino, y solo entra
  en un inquilino como superusuario.

El formulario también pide un token, un nombre, el **Emisor** del proveedor, el **ID de cliente** y el
**Secreto de cliente** que te dio, y, de forma opcional, los ámbitos, el claim de correo y los métodos
de autenticación y claims que debe llevar todo inicio de sesión. El token y el emisor no se pueden
cambiar después. El secreto de cliente es de solo escritura: no se vuelve a mostrar. Si el proveedor
está dentro de tu red y su certificado TLS lo firmó tu propia autoridad de certificación, pega ese
certificado de CA (PEM) en **Certificados de CA privada**, o elige el archivo.

<GuideShot id="admin-identity-provider-new" alt="El panel Nuevo proveedor de identidad. El tipo tiene Inquilino seleccionado, con Instancia como alternativa, y explica que los inicios de sesión de un proveedor de inquilino solo pueden entrar en sus inquilinos. Campos: token, nombre, emisor, ID de cliente, secreto de cliente (de solo escritura), ámbitos y claim de correo, cada uno con una breve explicación." />

La página de una conexión tiene cuatro pestañas, con **Habilitar** (o **Deshabilitar**) y
**Eliminar** arriba.

**Endpoints** muestra la instantánea de descubrimiento: los endpoints de autorización, de token, de
claves de firma y de cierre de sesión del proveedor tal como se obtuvieron por última vez, con una
huella y la aprobación. Los inicios de sesión solo usan los endpoints aprobados aquí. Mientras no
estén aprobados, la pestaña ofrece **Aprobar endpoints**, y **Habilitar** no está disponible.
**Actualizar desde el proveedor** los vuelve a obtener; si cambiaron, la aprobación se retira, los
inicios de sesión con la conexión se rechazan, y tienes que revisar y aprobar los nuevos endpoints
para reanudarlos.

<GuideShot id="admin-identity-provider-discovery" alt="La conexión Acme Google Workspace, con token acme-google, deshabilitada y de tipo inquilino, con botones Habilitar y Eliminar. La pestaña Endpoints muestra la instantánea de descubrimiento: los endpoints de autorización, de token y de claves de firma de Google, ninguno de cierre de sesión, la fecha de obtención, una huella y la aprobación de superuser@devicechain.local con su fecha. Un botón Actualizar desde el proveedor." />

**Configuración** edita la conexión. Su sección **General** muestra el emisor, que es fijo, y la
**Dirección de callback**, que registras en el proveedor como URI de redirección; después vienen el
nombre, el ID de cliente, los ámbitos, el claim de correo y los métodos de autenticación exigidos.
Otras secciones gestionan el secreto de cliente, los claims exigidos, la CA privada y si al cerrar
sesión también se cierra la sesión en el proveedor.

La sección **CA privada** es para un proveedor dentro de tu red cuyo certificado TLS firmó tu propia
autoridad de certificación. Los certificados que añadas solo se confían para este proveedor, además de
las raíces de confianza del sistema, y solo se aceptan certificados, nunca una clave privada.
**Verificar y guardar el paquete** primero contacta con el proveedor usando el paquete nuevo, y no
guarda nada si su certificado no encadena con él. Si después el proveedor indica endpoints distintos,
la aprobación se retira y los revisas de nuevo. **Quitar el paquete** vuelve a usar solo las raíces
del sistema.

<GuideShot id="admin-identity-provider-settings" alt="La pestaña Configuración de Acme Google Workspace. En General: el emisor https://accounts.google.com, fijado al crear la conexión; la dirección de callback http://localhost/api/user-management/sso/callback/acme-google con un botón para copiarla; el nombre Acme Google Workspace; un ID de cliente terminado en apps.googleusercontent.com; los ámbitos openid email profile; el claim de correo email; y el principio de los métodos de autenticación exigidos." />

**Inquilinos** indica exactamente en qué inquilinos pueden entrar los inicios de sesión de esta
conexión, y en ningún otro. Elige **Guardar inquilinos** después de cambiar la lista.

<GuideShot id="admin-identity-provider-tenants" alt="La pestaña Inquilinos de Acme Google Workspace. Bajo Inquilinos atendidos, el selector contiene acme (sim). El botón Guardar inquilinos está en gris porque no ha cambiado nada." />

**Dominios** lista los dominios de correo que representa esta conexión, quién afirmó cada uno y
cuándo. Añade uno en **Añadir un dominio**, o **Quítalo**. Los proveedores de correo gratuito y los
sufijos públicos se rechazan, y un dominio pertenece a una sola conexión.

<GuideShot id="admin-identity-provider-domains" alt="La pestaña Dominios de Acme Google Workspace. Bajo Dominios verificados, una fila: acme-properties.example, afirmado por superuser@devicechain.local con su fecha, y un botón Quitar. Debajo, un campo para añadir un dominio y un botón Añadir dominio." />

La conexión de estas capturas está aprobada pero sigue deshabilitada, así que todavía nadie puede
iniciar sesión con ella. Para la configuración completa, paso a paso, consulta
[Configurar el inicio de sesión único](../guides/configuring-sso.md), y para su funcionamiento,
[Inicio de sesión único](../concepts/single-sign-on.md).

## Proveedores de IA

**Proveedores de IA** registra los modelos de IA que la instancia puede usar para redactar reglas de
detección en lenguaje natural. Cada proveedor tiene un tipo, un modelo, una clave de API y, cuando
hace falta, un endpoint. La clave es de solo escritura: no se vuelve a mostrar. La página de un
proveedor tiene las pestañas **Básico**, **Conexión** y **Prueba**, y **Probar proveedor** ejecuta una
instrucción con él para comprobar la conexión. Registrar un proveedor no lo ofrece a nadie: lo
concedes a un nivel, en **Empaquetado**, para que aparezca en el menú de esos inquilinos.

Las pantallas de proveedores de IA necesitan el servicio opcional de IA. La instancia de estas
capturas no lo ejecuta, así que la página indica que el área no está desplegada:

<GuideShot id="console-area-unavailable" alt="La página Proveedores de IA de la consola de administración con un estado vacío titulado 'No desplegado'. El texto explica que la instancia se desplegó sin el servicio ai-inference, por lo que la función no está disponible, que no hay ningún problema con la sesión y que un administrador decide qué servicios ejecuta cada instancia." />

Consulta [Proveedores de IA](../concepts/ai-authoring.md#ai-providers-are-operator-configuration).

## Auditoría de la instancia

**Auditoría** es el diario de toda la instancia. Registra los inicios de sesión, los inicios de sesión
fallidos y las renovaciones de sesión (la categoría **Autenticación**), y los cambios en identidades,
roles, inquilinos y proveedores de identidad (la categoría **Mutación**), en todos los inquilinos.
Cada fila muestra la hora, la categoría, el actor, la operación, el inquilino y el objetivo.

Filtra con **Todos los inquilinos** (también aparece un inquilino cuya eliminación ya terminó, con la
fecha, para que su diario siga siendo accesible), **Todas las categorías** y **Filtrar por actor…**. **Exportar CSV** descarga todo lo que
coincide, igual que la exportación de la [Auditoría](./audit.md#exportar) del inquilino.

<GuideShot id="admin-audit" alt="La página Auditoría filtrada por la categoría Mutación y el actor superuser@devicechain.local. Cada fila es una mutación de superuser@devicechain.local: una actualización de iam_identities lee.park@acme-properties.example, creaciones en iam_memberships e iam_membership_tenant_roles, una creación y una actualización de iam_tenants harbor-view-pilot, y creaciones y una actualización de la conexión de proveedor de identidad acme-google y sus inquilinos y dominios. La columna Inquilino muestra un guion. Arriba a la derecha hay un botón Exportar CSV." />

La captura está filtrada a los cambios del propio operador. Sin filtros, el diario son sobre todo
inicios de sesión.

## Configuración

**Configuración** contiene los ajustes de toda la instancia, cada uno en su pestaña. Los valores
predeterminados viven en el código de la plataforma y solo se guardan tus anulaciones, así
que cada pestaña muestra una insignia **Predeterminado** o **Anulado**. **Guardar anulación** guarda tu
valor; **Restablecer al valor predeterminado** lo elimina.

**Marca** fija el título de la aplicación, los colores, la altura del logotipo y el logotipo
predeterminados que recibe cada inquilino si no fija los suyos, con una vista previa en vivo. Aquí el
logotipo es una dirección https, y se guarda con el resto mediante **Guardar anulación**.
Consulta [Marca blanca e identidad de marca](../concepts/white-labeling.md).

<GuideShot id="admin-settings" alt="La página Ajustes del sistema en la pestaña Marca, con las pestañas Máscaras de token, Marca, Mapa e Idioma. El ajuste branding.default lleva la insignia Predeterminado. Campos: el título de la aplicación DeviceChain; los colores primario, de acento, de fondo y de primer plano de la barra lateral, todos heredados; la altura máxima del logotipo, 28; y un campo de logotipo con el marcador https://, cuyo texto de ayuda describe una subida o una dirección https. Una tarjeta de vista previa muestra DeviceChain, un botón principal y texto de ejemplo. Botones Guardar anulación y Restablecer al valor predeterminado." />

**Máscaras de token** fija cómo genera la consola los tokens de los registros nuevos. Una máscara es
texto literal más marcadores: `{slug}` (a partir del nombre del registro), `{uuid}`,
`{alphanumeric-N}` y `{numeric-N}`. La entrada `default` se aplica a cualquier tipo sin máscara
propia, y cada fila muestra un token de ejemplo. **Añadir una máscara** añade una fila para un tipo de
entidad. Consulta [Máscaras de token](../concepts/system-settings.md#token-masks).

<GuideShot id="admin-settings-token-masks" alt="La pestaña Máscaras de token. El ajuste entity.token_masks lleva la insignia Predeterminado. Una explicación de los marcadores y una fila: el tipo de entidad default, la máscara {slug} y el ejemplo sample-name, con un icono para borrarla. Botones Añadir una máscara, Guardar anulación y Restablecer al valor predeterminado." />

**Mapa** fija el mapa base con el que empieza cada inquilino ([Mapas base](../concepts/basemaps.md)),
e **Idioma** fija el idioma en que se abre la consola cuando un inquilino no ha fijado ninguno
([Idioma predeterminado](../concepts/system-settings.md#locale-default)). Los administradores de un
inquilino pueden anular la marca, el mapa y el idioma para su propio inquilino; consulta
[Configuración del inquilino](./tenant-settings.md).

## Relacionado

- [Multitenencia](../concepts/multi-tenancy.md): cómo se mantienen separados los inquilinos en una
  misma instalación compartida.
- [Niveles de inquilino y empaquetado](../concepts/tenant-tiers.md) y
  [Gobernanza y cuotas](../concepts/governance.md): qué empaqueta un nivel y cómo se resuelven los
  límites.
- [Eliminación de inquilinos](../deployment/tenant-deletion.md): qué borra una eliminación, qué
  conserva y cuánto tarda.
- [Inicio de sesión único](../concepts/single-sign-on.md) y
  [Configurar el inicio de sesión único](../guides/configuring-sso.md).
- [Ajustes del sistema](../concepts/system-settings.md): los valores predeterminados de la instancia.
