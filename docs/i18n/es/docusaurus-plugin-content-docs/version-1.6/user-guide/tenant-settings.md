---
title: Configuración del inquilino
sidebar_position: 14
---

import GuideShot from '@site/src/components/GuideShot';

# Configuración del inquilino

El grupo **Configuración** de la barra lateral reúne tres páginas que determinan el aspecto de la
consola y el idioma en que se abre para todos los miembros de tu inquilino: **Marca**, **Mapa** e
**Idioma**. Este capítulo es para los administradores del inquilino.

Las tres páginas siguen el mismo patrón. Un campo que dejas vacío hereda un valor por defecto: el que
haya definido el operador de tu instancia o, si no ha definido ninguno, el integrado. Cada página
necesita su propio permiso. Sin él, la página no aparece en la barra lateral; si la abres por su
dirección, solo te indica qué permiso debes pedir.

## Marca

**Marca** personaliza la consola con la identidad de tu inquilino: el título, los colores y el
logotipo.

<GuideShot id="settings-branding" alt="La página Marca. Campos para el título de la aplicación, los colores Principal, Acento, Fondo de la barra lateral y Primer plano de la barra lateral (todos heredados), la altura máxima del logotipo en 28 y el Logotipo, con un cuadro de URL https y los botones Aplicar URL y Subir. A la derecha, una tarjeta de vista previa muestra el título DeviceChain, un botón principal y contenido de ejemplo. Arriba a la derecha está el botón Guardar marca." />

Los campos:

- **Título de la aplicación** se muestra en la pestaña del navegador y en la consola. Vacío hereda el
  valor por defecto, que es *DeviceChain* salvo que tu operador lo haya cambiado.
- **Principal** colorea los botones, los enlaces y los anillos de foco. **Acento** colorea los
  resaltados secundarios.
- **Fondo de la barra lateral** y **Primer plano de la barra lateral** colorean la barra lateral y el
  texto y los iconos que contiene. No cambian la página en sí, que mantiene su tema claro u oscuro.
- **Altura máxima del logotipo (px)** fija la altura del logotipo en la barra lateral, de 16 a 200.
  Vacío hereda el valor por defecto, que es 28 salvo que tu operador lo haya cambiado.

Escribe un color como código hexadecimal de seis dígitos, por ejemplo `#1f9fb7`, o usa la muestra
para elegirlo. Vacía un campo para volver a heredar. La tarjeta **Vista previa** muestra el título (o el logotipo, si hay
uno) y un botón principal a medida que los cambias. Si una combinación de colores se lee mal, la página te
avisa, pero te deja guardar. Elige **Guardar marca** para aplicar el título, los colores y la altura
del logotipo.

El **Logotipo** se gestiona aparte y se aplica de inmediato, sin **Guardar marca**:

- **Subir** un archivo PNG, JPEG o WebP de hasta 1 MB, o
- pegar la dirección `https://` de una imagen que alojes tú (aquí sirve SVG) y elegir **Aplicar
  URL**.

**Eliminar logotipo** lo quita. La página de inicio de sesión siempre muestra la marca estándar de
DeviceChain, porque no se sabe el inquilino hasta que alguien inicia sesión. Consulta
[Marca blanca e identidad de marca](../concepts/white-labeling.md) para ver cómo se combinan los
valores por defecto.

## Mapa

**Mapa** define los mosaicos de mapa sobre los que dibujan el editor de geocercas y los widgets de
mapa de los paneles de este inquilino, y dónde se abre una geocerca nueva. La barra superior de la
página muestra *Basemap*, en inglés también en la consola en español.

<GuideShot id="settings-basemap" alt="La página Mapa. Bajo Origen de mosaicos, una lista Proveedor en Personalizado…, un campo Plantilla de URL de mosaicos, un campo Atribución y una línea que indica que se hereda el valor por defecto de la instancia: https://tile.openstreetmap.org/{z}/{x}/{y}.png. Una nota avisa de que una clave de API en una URL de mosaicos no es secreta. Bajo Vista por defecto aparecen vacíos los campos Latitud, Longitud y Zoom." />

**Origen de mosaicos.** Déjalo vacío para heredar el valor por defecto de la instancia; la página
muestra cuál es. Para usar tu propio proveedor:

1. Elígelo en **Proveedor**. La consola rellena la **Plantilla de URL de mosaicos** y la
   **Atribución** que exige la licencia del proveedor. Algunos proveedores piden además una **Clave
   de API**. Para un proveedor que no está en la lista, elige **Personalizado…** y rellena tú ambos
   campos.
2. Revisa los dos campos. La URL de mosaicos debe empezar por `https://` y contener los marcadores
   `{z}`, `{x}` e `{y}` (o `{bbox-epsg-3857}` o `{quadkey}`).
3. Elige **Guardar mapa base**.

La URL de mosaicos y su atribución se guardan juntas o no se guardan, porque mostrar los mosaicos de
un proveedor sin su línea de crédito incumple su licencia.

:::warning Una clave en una URL de mosaicos no es secreta
El navegador descarga los mosaicos con la clave en la URL, así que cualquiera que use este inquilino
puede leerla. Protege la clave con las restricciones de referente HTTP de tu proveedor.
:::

**Vista por defecto.** **Latitud**, **Longitud** y **Zoom** definen dónde se abre el editor de
geocercas al empezar una geocerca nueva. Latitud y longitud van juntas: define las dos o ninguna. Una
geocerca que ya tiene forma se abre sobre esa forma, y los widgets de mapa de los paneles siempre se
ajustan a lo que muestran, así que ninguno de los dos usa esta vista.

Consulta [Mapas base](../concepts/basemaps.md) para elegir proveedor y conocer las reglas exactas.

## Idioma

**Idioma** define el idioma en que se abre la consola para los miembros de este inquilino que no han
elegido uno. La barra superior de la página muestra *Locale*, en inglés también en la consola en español.

<GuideShot id="settings-locale" alt="La página Idioma. Idioma predeterminado ofrece Heredar (seleccionado), English y Español. Una nota explica que es un valor por defecto, no una imposición, y debajo está el botón Guardar idioma." />

Elige **Heredar** para seguir el valor predeterminado que tu operador haya definido para toda la
instancia, o elige un idioma, y después **Guardar idioma**.

Es un valor por defecto, no una imposición. Quien haya elegido un idioma en el menú de usuario lo
conserva. La consola elige el idioma en este orden:

1. el idioma que eligió la persona;
2. el idioma predeterminado de este inquilino;
3. el idioma predeterminado de la instancia;
4. los idiomas que pide el navegador de la persona;
5. inglés.

Consulta [Idioma predeterminado](../concepts/system-settings.md#locale-default) para el ajuste de la
instancia.

## Relacionado

- [Marca blanca e identidad de marca](../concepts/white-labeling.md)
- [Mapas base](../concepts/basemaps.md)
- [Ajustes del sistema](../concepts/system-settings.md), para los valores por defecto de toda la instancia que define un operador
