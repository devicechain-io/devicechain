---
title: Grupos, facetas y Explorar
sidebar_position: 11
---

import GuideShot from '@site/src/components/GuideShot';

# Grupos, facetas y Explorar

Cuando una flota crece, necesitas formas de quedarte con los dispositivos (o activos, áreas,
clientes) que te interesan: las unidades de la azotea, los sensores de la planta 3. DeviceChain te
da tres herramientas para ello, y este capítulo las cubre todas:

- **Grupos**: colecciones con nombre de una familia, ya sea una lista que mantienes a mano o un
  filtro guardado.
- **Facetas**: claves de clasificación, como `zone` o `floor`, que declaras una vez y luego asignas a
  cada entidad.
- **Explorar**: una pantalla que filtra una familia por sus facetas, muestra las coincidencias en vivo
  y guarda el filtro como un **grupo dinámico** cuyos miembros se mantienen al día solos.

Es para los usuarios y administradores de un inquilino que organizan una flota. Para el modelo que
hay detrás, consulta [facetas y grupos dinámicos](../concepts/domain-model.md#facets-and-dynamic-groups).

## Grupos

Cada familia tiene su propia pantalla de grupos: **Dispositivos → Grupos de dispositivos**,
**Organización → Activos → Grupos de activos**, **Organización → Clientes → Grupos de clientes** y **Organización → Áreas → Grupos de áreas**. Un
grupo solo contiene miembros de una familia. La lista muestra el token, el nombre, la descripción y la
fecha de creación de cada grupo.

<GuideShot id="device-groups-list" alt="La lista Grupos de dispositivos, bajo el menú Dispositivos, con un grupo, rooftop-units, llamado Rooftop units y descrito como todos los termostatos de la azotea. Arriba a la derecha está el botón Nuevo grupo de dispositivos." />

Hay dos clases de grupo:

- Un grupo **estático** contiene una lista explícita de miembros. Créalo con **Nuevo grupo de
  dispositivos** (o el botón equivalente de la familia), ábrelo y usa su pestaña **Miembros** para
  añadir y quitar miembros.
- Un grupo **dinámico** contiene un filtro guardado en lugar de una lista. Sus miembros se calculan
  cada vez que se lee el grupo, así que siempre reflejan los valores de faceta actuales de las
  entidades. Se crea en la pantalla [Explorar](#browse). Los grupos dinámicos también aparecen en la
  lista de grupos de la familia; el grupo `rooftop-units` de arriba es uno de ellos. Para ver los
  miembros actuales de un grupo dinámico, usa su botón **Miembros** en **Grupos dinámicos guardados**, en la pantalla Explorar, que los
  calcula a partir del filtro.
  La pestaña **Miembros** de la página del propio grupo gestiona una lista mantenida a mano, así que
  no muestra los miembros que coinciden con el filtro.

## Facetas

Una **faceta** es una clave de atributo que declaras como eje de clasificación para una familia, como
`zone` para los dispositivos. Declarar una faceta hace dos cosas: la ofrece como filtro en la pantalla
Explorar y le da un campo de valor en la pestaña **Facetas** de cada entidad.

Los valores en sí viven en las entidades, no en esta pantalla. Una faceta declarada que no tiene
valor en ninguna entidad no coincide con nada.

Abre **Organización → Facetas** en la barra lateral. Los chips de arriba filtran la lista por familia de miembros:
**Todas**, **Dispositivo**, **Activo**, **Área** o **Cliente**. Cada fila muestra la familia, la clave,
el tipo de valor, los valores sugeridos y la etiqueta.

<GuideShot id="facets-list" alt="La pantalla Facetas, filtrada por Todas. Dos facetas de dispositivo: zone, de tipo STRING, con los valores lobby, rooftop, offices, amenities, technical y circulation y la etiqueta Zone; y floor, de tipo LONG, sin lista de valores y con la etiqueta Floor. Cada fila tiene la acción Eliminar." />

### Declarar una faceta

Declarar o eliminar una faceta, y guardar un grupo dinámico, requieren el permiso `device:write`;
sin él, esos botones no aparecen.

Haz clic en **Nueva faceta**. El panel pide:

- **Familia de miembros**: Dispositivo, Activo, Área o Cliente.
- **Clave de atributo**: la clave bajo la que se guardan los valores, por ejemplo `clima`.
- **Tipo de valor**: `STRING`, `LONG`, `DOUBLE`, `BOOLEAN` o `JSON`. Los valores que se introduzcan
  para la faceta se comprueban contra él.
- **Valores**: opcional. Una lista separada por comas de valores sugeridos, que se ofrecen mientras
  escribes. Déjalo en blanco para valores libres.
- **Etiqueta**: opcional. El nombre que se muestra para la faceta; por defecto es la clave.

<GuideShot id="facets-new" alt="El panel Nueva faceta con los campos Familia de miembros en Dispositivo, Clave de atributo, Tipo de valor en STRING, Valores con la sugerencia árido, templado, tropical, y Etiqueta, seguidos del botón Guardar faceta." />

Haz clic en **Guardar faceta**. No hay una pantalla de edición aparte: para cambiar el tipo, los
valores o la etiqueta de una faceta, vuelve a declararla con la misma familia y la misma clave, y la
nueva declaración sustituye a la anterior.

**Eliminar** solo quita la declaración. Los valores guardados en las entidades no se tocan, y
cualquier grupo dinámico guardado que use la faceta sigue funcionando.

### Asignar valores de faceta

Para dar un valor de faceta a una entidad, ábrela y ve a su pestaña **Facetas**. Cada faceta declarada
para esa familia aparece con su valor actual, o **Sin valor**. Escribe un valor y haz clic en
**Guardar**, o haz clic en **Borrar** para quitarlo. Si hay grupos dinámicos que dependen de la
faceta, la pestaña te dice en qué grupos entraría o de cuáles saldría la entidad con el cambio. La
pestaña se describe en [Activos, clientes y áreas](./assets-customers-areas.md#facets); los
dispositivos tienen la misma pestaña.

## Explorar {#browse}

**Explorar** convierte las facetas en un filtro. Elige una familia con los chips de arriba
(**Dispositivo**, **Activo**, **Área** o **Cliente**). El panel **Ejes de facetas** lista todas las
facetas declaradas para esa familia, cada una con un operador:

- **— (ignorar)**: la faceta no interviene en el filtro. Es el valor por defecto.
- **es** y **no es**: comparan con un valor. En una faceta de texto con valores sugeridos, **es** te
  deja elegir varios valores, y la entidad coincide con cualquiera de ellos.
- **tiene algún valor**: la entidad tiene algún valor para la faceta. Es el único operador para una
  faceta `JSON`.
- En las facetas `LONG` y `DOUBLE` se ofrecen además las comparaciones `<`, `≤`, `>` y `≥`.

A medida que defines condiciones, el panel **Coincidencias** muestra el **Selector** que ha compuesto
la consola, el número de miembros que coinciden y una muestra de ellos. El recuento se actualiza
cuando dejas de escribir.

<GuideShot id="browse-filter" alt="La pantalla Explorar con la familia Dispositivo seleccionada. En Ejes de facetas, Zone está en es rooftop y Floor se ignora. El panel Coincidencias muestra el selector attr zone igual a rooftop, 2 miembros coinciden, y lista bp-therm-002 y bp-therm-003, con el botón Guardar como grupo. Debajo, Grupos dinámicos guardados lista rooftop-units con la insignia dinámico, su selector y un botón Miembros." />

El selector se muestra tal como se guardará. Está escrito en CEL, el mismo lenguaje de expresiones que
usan las reglas de detección.

### Guardar un grupo dinámico

Cuando el filtro coincide con lo que buscas, haz clic en **Guardar como grupo**. El panel vuelve a
mostrar el selector y pide un **Token** (el botón de al lado genera uno) y un **Nombre** opcional. Haz
clic en **Guardar grupo**.

<GuideShot id="browse-save-group" alt="El panel Guardar como grupo dinámico, con el selector attr zone igual a offices, un campo Token con un botón para generarlo, un campo Nombre opcional y el botón Guardar grupo." />

La plataforma vuelve a comprobar el selector al guardar y rechaza uno que no puede ejecutar de forma
eficiente. Una vez guardado, el grupo aparece en **Grupos dinámicos guardados** de esa familia, con su
token, su nombre y su selector. Haz clic en **Miembros** para ver quién pertenece a él ahora mismo; la
lista se calcula a partir del selector cada vez que la abres.

## Relacionado

- [Facetas y grupos dinámicos](../concepts/domain-model.md#facets-and-dynamic-groups)
- [Activos, clientes y áreas](./assets-customers-areas.md)
- [Comandar una flota](../guides/commanding-a-fleet.md): enviar un comando a todos los dispositivos
  de un grupo.
