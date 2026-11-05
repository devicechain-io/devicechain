---
title: Iniciar sesión
sidebar_position: 1
---

import GuideShot from '@site/src/components/GuideShot';

# Iniciar sesión

Este capítulo explica cómo entrar en la consola y qué puedes cambiar de tu propia cuenta una vez
dentro: los pasos del inicio de sesión, el inicio de sesión único, la elección de inquilino, qué
pasa cuando una sesión expira y el menú de usuario.

Es para todas las personas que usan la consola. El superusuario de la instancia inicia sesión en la
consola de administración de la instancia, que no tiene páginas de inquilino; para usar la consola
de un inquilino necesitas una identidad de administrador de inquilino, que `dcctl sim create` crea
para un primer inquilino (o que un administrador crea con `createIdentity` y `addMembership` en la
API de administración). Tu cuenta la crea un administrador; no se puede
crear desde la página de inicio de sesión.

## Los pasos del inicio de sesión

La página de inicio de sesión te pide primero el correo electrónico, y solo después lo demás.

<GuideShot id="signin-identify" alt="La página de inicio de sesión de la consola. Bajo el logotipo de DeviceChain y la frase 'Inicia sesión en la consola de gestión' hay una tarjeta con un campo Correo electrónico, un botón Continuar y un botón Iniciar sesión con SSO. Debajo de la tarjeta hay un selector de idioma con English y Español." />

1. Escribe tu correo electrónico y elige **Continuar**.
2. La consola comprueba si tu organización configuró el inicio de sesión único para el dominio de
   tu correo:
   - Si no lo hizo, te pide la contraseña.
   - Si lo hizo, la página dice «Esta dirección inicia sesión mediante el inicio de sesión único
     de tu organización». Elige **Continuar con el inicio de sesión único** para ir a tu
     proveedor de identidad. Si tu cuenta solo tiene contraseña, elige
     **Usar una contraseña**.
3. Escribe tu contraseña y elige **Iniciar sesión**.

<GuideShot id="signin-password" alt="El segundo paso del inicio de sesión. El campo Correo electrónico muestra ahora la dirección escrita y, debajo, hay un campo Contraseña vacío, un botón Iniciar sesión, un botón Iniciar sesión con SSO y un enlace Usar otro correo electrónico." />

A partir del primer paso, tu correo sigue en pantalla pero ya no se puede editar. Para empezar de
nuevo con otra dirección, elige **Usar otro correo electrónico**.

La comprobación del paso 2 se refiere al **dominio** de tu correo, nunca a ti. Da la misma
respuesta exista o no una cuenta para esa dirección, así que no sirve para averiguar quién tiene
cuenta.

Si el correo o la contraseña no son correctos, la consola dice «Correo electrónico o contraseña no
válidos.», sin indicar cuál de los dos falló. Tras demasiados intentos fallidos, te pide que
esperes un momento antes de volver a intentarlo.

### Elegir el idioma

El selector de idioma bajo la tarjeta de inicio de sesión fija el idioma de la consola:
**English** o **Español**. Tu elección se recuerda en este navegador y se aplica al momento.
Puedes volver a cambiarla desde el [menú de usuario](#the-user-menu).

## Inicio de sesión único

Si tu organización inicia sesión a través de un proveedor de identidad, puedes elegir
**Iniciar sesión con SSO** en cualquiera de los dos pasos. Escribe antes tu correo: la consola lo
necesita para encontrar el proveedor de tu organización. Se te envía al proveedor y vuelves a la
consola con la sesión iniciada.

Si el inicio de sesión único no está configurado para tu dirección, la consola te lo dice y
muestra el campo de contraseña.

Cómo funciona el inicio de sesión único, y cómo lo configura un administrador, se explica en
[Inicio de sesión único](../concepts/single-sign-on.md) y en
[Configurar el inicio de sesión único](../guides/configuring-sso.md).

## Autenticación en dos pasos {#two-factor}

Si tu cuenta tiene autenticación en dos pasos, introducir la contraseña correcta no basta. La consola pide a continuación un **código de verificación**:

- Introduce el código de seis dígitos de tu aplicación de autenticación. Un código sirve una sola vez, así que si acabas de usar uno, espera al siguiente.
- O elige **Usar un código de recuperación** e introduce uno de los códigos que guardaste al configurarla. Sirve una sola vez.

Si el paso caduca (cinco minutos, cinco intentos), vuelves a la contraseña. Para activarla, abre el menú de usuario y elige **Seguridad de la cuenta**; los pasos están en [Autenticación en dos pasos](/guides/two-factor-authentication#enrol). Si tu organización la exige y no la has configurado, la consola te lleva allí después de iniciar sesión.

## Elegir un inquilino

Un inquilino es el espacio de trabajo que contiene los dispositivos, reglas y paneles de tu
organización. Tu cuenta puede pertenecer a más de uno.

- Si perteneces a **un** inquilino, entras directamente en él.
- Si perteneces a **varios**, la consola muestra «Elige un inquilino para continuar» con un botón
  por inquilino. Elige aquel en el que quieres trabajar. **Volver** te devuelve al formulario de
  inicio de sesión.
- Un inquilino que exige el inicio de sesión único sigue en la lista, marcado como «Requiere
  inicio de sesión único», pero no puedes entrar en él tras iniciar sesión con contraseña. Usa
  **Iniciar sesión con SSO**.
- Si tu cuenta aún no pertenece a ningún inquilino, la consola te lo indica. Pide a un
  administrador que te agregue a uno.

Los operadores de la instancia (superusuarios) no eligen inquilino al iniciar sesión: llegan a la
consola de administración, que se describe en
[Administración de la instancia](./administering-the-instance.md).

## Cuando tu sesión expira

Una sesión no dura para siempre. Si estás ausente el tiempo suficiente para que caduque, la
consola te devuelve a la página de inicio de sesión y te explica por qué.

<GuideShot id="signin-session-expired" alt="La página de inicio de sesión con un aviso sobre la tarjeta que dice 'Tu sesión expiró. Inicia sesión de nuevo.' El campo Correo electrónico está vacío." />

Vuelve a iniciar sesión para seguir. Si tu inquilino empezó a exigir el inicio de sesión único
mientras tenías una sesión con contraseña, el aviso lo dice y te pide usar
**Iniciar sesión con SSO**.

## El menú de usuario {#the-user-menu}

Tu nombre y tu correo aparecen al pie de la barra lateral. Selecciónalos para abrir el menú de
usuario.

<GuideShot id="user-menu" alt="La página de inicio de la consola con el menú de usuario abierto abajo a la izquierda. El menú muestra el nombre y el correo de la persona, un control de tema de tres posiciones con iconos de sol, luna y monitor, un control de idioma con English y Español, y las opciones Editar perfil, Vincular cuenta de inicio de sesión único y Cerrar sesión." />

Desde el menú de usuario puedes:

- **Cambiar el tema.** Los tres iconos son **Claro**, **Oscuro** y **Sistema**. Sistema sigue la
  configuración de tu equipo.
- **Cambiar el idioma**, igual que en la página de inicio de sesión.
- **Editar tu perfil** (ver más abajo).
- **Vincular tu cuenta al inicio de sesión único.** **Vincular cuenta de inicio de sesión único**
  conecta tu cuenta con el proveedor de identidad de tu organización, para que a partir de
  entonces puedas iniciar sesión a través de él. Necesita un inicio de sesión reciente con
  contraseña, así que no se ofrece si entraste mediante el inicio de sesión único. Consulta
  [vínculos](../concepts/single-sign-on.md#linking).
- **Cerrar sesión.**

Los operadores de la instancia ven además **Consola de administración** y, si la instancia lo
ofrece, **Métricas**.

### Editar tu perfil

**Editar perfil** abre un panel donde puedes cambiar el nombre con el que te muestra la consola.

<GuideShot id="user-profile-drawer" alt="El panel Editar perfil abierto a la derecha de la pantalla, con la descripción 'Actualiza tu nombre visible.' Muestra un campo Correo electrónico atenuado con la nota 'Tu identidad de inicio de sesión; no se puede cambiar.', los campos Nombre y Apellido y un botón Guardar cambios." />

Cambia tu **Nombre** y tu **Apellido** y elige **Guardar cambios**. Tu correo es la identidad con
la que inicias sesión, así que se muestra pero no se puede cambiar aquí.

## Relacionado

- [Inicio de sesión único](../concepts/single-sign-on.md)
- [Configurar el inicio de sesión único](../guides/configuring-sso.md)
- [Multitenencia](../concepts/multi-tenancy.md)
