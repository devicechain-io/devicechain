---
title: La consola de un vistazo
sidebar_position: 2
---

import GuideShot from '@site/src/components/GuideShot';

# La consola de un vistazo

Este capítulo es un breve recorrido por las partes de la consola que ves en todas las páginas:
la página de inicio, la barra lateral, la cabecera de página con su distintivo de inquilino, y el
mensaje que aparece cuando una parte de la consola no está disponible en tu instalación.

Es para cualquier persona que empiece con la consola. Léelo una vez y los capítulos siguientes
tendrán más sentido.

## La página de inicio

Después de iniciar sesión llegas a la página de inicio. Te da la bienvenida por tu nombre y
muestra dos recuentos: cuántos **Dispositivos** y cuántos **Tipos de dispositivo** tiene tu
inquilino. Selecciona cualquiera de los dos para abrir esa lista.

<GuideShot id="console-home" alt="La página de inicio de la consola. La barra lateral está a la izquierda. La cabecera dice Inicio, con el distintivo de inquilino 'acme (sim)' arriba a la derecha. La página se titula 'Bienvenido, Dana Okafor' y muestra dos tarjetas: 13 Dispositivos y 1 Tipos de dispositivo." />

La entrada **Inicio**, arriba del todo en la barra lateral, siempre te trae de vuelta aquí. No es
lo mismo que **Paneles**, que contiene los paneles que crea tu equipo.

## La barra lateral

La barra lateral de la izquierda es la forma de moverte por la consola. Enumera las áreas de tu
inquilino en un orden fijo.

<GuideShot id="console-sidebar" alt="La barra lateral de la consola. Bajo el logotipo de DeviceChain y las palabras Consola de gestión enumera Inicio y seis secciones plegables: Supervisión, Dispositivos, Conectar, Organización, Acceso y Configuración. El encabezado de cada sección lleva una flecha que indica que se abre y se cierra. El nombre y el correo de la persona que inició sesión están al pie." />

**Inicio** es una sola página. Cada una de las demás entradas es una **sección plegable** que agrupa
páginas relacionadas. Selecciona el encabezado de una sección para abrirla o cerrarla, y selecciona
una página dentro de ella para ir allí.

| Entrada | Qué contiene |
| --- | --- |
| **Inicio** | La página de inicio. |
| **Supervisión** | Los **Paneles** que crea tu equipo, las **Alarmas** que han generado tus reglas y las **Políticas de notificación**: a quién se avisa de las alarmas y las horas de silencio que retienen esos avisos. |
| **Dispositivos** | **Dispositivos**, **Tipos de dispositivo**, **Perfiles de dispositivo** (que contienen las reglas, métricas y comandos del perfil), **Grupos de dispositivos**, **Lotes de comandos**, **Ventanas de mantenimiento** y **Actualizaciones** (**Artefactos**, **Despliegues** y **Firmantes** para las actualizaciones remotas, OTA). |
| **Conectar** | Cómo llegan al inquilino los dispositivos y otros sistemas: **Perfiles de aprovisionamiento**, **Anclas de confianza**, **Integraciones** y **Conectores** (conexiones salientes con otros sistemas). |
| **Organización** | **Activos**, **Clientes** y **Áreas** (incluidas las **Geocercas**); **Facetas**, las claves con las que describes y filtras entidades; y **Explorar**, un generador de consultas para encontrar entidades por sus facetas. |
| **Acceso** | **Miembros**, **Cuentas de servicio**, **Roles**, **Seguridad de inicio de sesión** y **Auditoría**, el registro del inquilino de quién cambió qué. |
| **Configuración** | **Marca**, **Mapa** e **Idioma**. |

Solo ves las entradas que tu rol permite. Si tu rol no puede leer alarmas, por ejemplo,
**Alarmas** no aparece en tu barra lateral. **Actualizaciones** solo aparece cuando tu instalación ejecuta el servicio
de actualizaciones. Las secciones **Acceso** y **Configuración** son para los administradores del
inquilino.

Si tu acceso está limitado a algunos de los clientes del inquilino, la barra lateral contiene solo
**Inicio**, **Paneles**, **Alarmas** y **Dispositivos**. Consulta
[Qué ve un usuario de cliente](/user-guide/members-roles-and-customer-users#what-a-customer-user-sees).

Tu nombre y tu correo aparecen al pie de la barra lateral. Abren el menú de usuario, que se
describe en [Iniciar sesión](./signing-in.md#the-user-menu).

Para dejar más espacio a la página, usa el botón de la izquierda de la cabecera para contraer la
barra lateral en una franja estrecha de iconos. Vuelve a usarlo para expandirla.

## La cabecera de página y el distintivo de inquilino

Todas las páginas tienen una barra de cabecera. A la izquierda está el nombre de la sección en la
que estás. En una página de detalle, como la de un dispositivo, un enlace a la derecha te lleva
de vuelta a la lista.

En el extremo derecho está el distintivo de inquilino. Muestra en qué inquilino estás trabajando:
el nombre del inquilino y, debajo, su token.

<GuideShot id="console-tenant-chip" alt="El distintivo de inquilino: una pequeña tarjeta con un icono de edificio, el nombre del inquilino 'acme (sim)' y, debajo, el token 'sim-acme' en letra monoespaciada." />

Si tu inquilino ha subido un logotipo, el distintivo muestra el logotipo en lugar del icono de
edificio.
Si tu acceso está limitado a algunos de los clientes del inquilino, la segunda línea nombra esos
clientes y tu rol en lugar del token: por ejemplo **Acme · viewer**, el nombre del cliente y después
el token de tu rol.
Todo lo que ves y cambias en la consola pertenece a este inquilino. Para trabajar en
otro inquilino, cierra sesión y elígelo al volver a iniciarla.

## Cuando un área no está disponible

DeviceChain se compone de varios servicios, y quien lo instala decide cuáles ejecutar. Algunos son
opcionales, como el servicio de IA o el de conectores salientes. Si una página necesita un
servicio que tu instalación no ejecuta, la página muestra **No desplegado** en lugar de su
contenido habitual.

<GuideShot id="console-area-unavailable" alt="La página Proveedores de IA de la consola de administración con un estado vacío titulado 'No desplegado'. El texto explica que la instancia se desplegó sin el servicio ai-inference, por lo que la función no está disponible, que no hay ningún problema con la sesión y que un administrador decide qué servicios ejecuta cada instancia." />

El mensaje nombra el servicio que falta. No hay ningún problema con tu sesión ni con tu cuenta.
Si necesitas la función, pídesela a tu administrador indicando el nombre del servicio que aparece
en el mensaje.

La captura muestra una página de administración de la instancia, pero las páginas del inquilino se
comportan igual. Por ejemplo, **Conectores** muestra este mensaje cuando el servicio de conectores
salientes no está en ejecución.

## Relacionado

- [Arquitectura](../concepts/architecture.md): los servicios que componen DeviceChain
- [Multitenencia](../concepts/multi-tenancy.md)
- [Iniciar sesión](./signing-in.md)
