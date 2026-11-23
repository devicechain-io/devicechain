---
title: Activos, clientes y áreas
sidebar_position: 9
---

import GuideShot from '@site/src/components/GuideShot';

# Activos, clientes y áreas

Los dispositivos miden cosas. En el registro describes esas cosas y cómo se organizan: los
**activos** que vigila un dispositivo, los **clientes** a los que atiendes y las **áreas** por las que
se reparte tu flota. Este capítulo es para los usuarios y administradores de un inquilino que montan
y mantienen esa estructura.

Las tres familias funcionan igual: si aprendes una, conoces las tres. Cada familia tiene tres
pantallas en **Organización**, en la barra lateral:

| Familia | Instancias | Plantillas | Colecciones |
| --- | --- | --- | --- |
| Activos | Activos | Tipos de activo | Grupos de activos |
| Clientes | Clientes | Tipos de cliente | Grupos de clientes |
| Áreas | Áreas | Tipos de área | Grupos de áreas |

Un **tipo** clasifica instancias, por ejemplo «HVAC Unit» para activos o «Building» para áreas. Un
**grupo** es una colección con nombre de instancias de una misma familia. El menú Áreas incluye
también Geocercas, que tienen su propio capítulo, [Geocercas](./geofences.md).

Para ver cómo se relacionan estas entidades con los dispositivos y entre sí, consulta el
[modelo de dominio](../concepts/domain-model.md).

## Listas

Cada lista muestra las instancias de la familia por páginas, con **Anterior** y **Siguiente** bajo la
tabla. Para activos, clientes y áreas, las columnas son Token, Nombre, Tipo y Creado. Haz clic en una
fila para abrirla.

<GuideShot id="assets-list" alt="La lista Activos: cinco activos, tres unidades HVAC y dos de sus componentes, cada uno con su token, su nombre, el tipo HVAC Unit y una fecha de creación. Arriba a la derecha está el botón Nuevo activo." />

El **token** es el identificador de la instancia. Otras pantallas, las reglas y la API se refieren a
ella por su token, así que nunca cambia una vez creada. El **nombre** es la etiqueta que leen las
personas, y puedes cambiarlo cuando quieras.

<GuideShot id="customers-list" alt="La lista Clientes con un cliente, Acme Properties, de tipo Facility Owner." />

<GuideShot id="areas-list" alt="La lista Áreas con tres edificios, todos del tipo de área Building. El menú Áreas desplegado muestra Áreas, Tipos de área, Grupos de áreas y Geocercas." />

## Crear una instancia

Haz clic en **Nuevo activo** (o **Nuevo cliente**, **Nueva área**). Se abre un panel con los mismos
cuatro campos para todas las familias:

- **Tipo de activo** (o tipo de cliente, tipo de área): la plantilla a la que pertenece la instancia.
  Si todavía no hay ninguna, el selector te pide que crees primero un tipo.
- **Token**: el identificador. El botón de al lado genera un token a partir del patrón de tokens que
  el operador ha definido para este tipo de entidad, que se muestra bajo el campo. También puedes escribir el tuyo. Un token puede llevar
  letras, dígitos, guiones y guiones bajos, y debe empezar por una letra o un dígito.
- **Nombre** y **Descripción**: texto libre.

<GuideShot id="assets-new" alt="El panel Nuevo activo, con un selector de tipo de activo, un campo Token con el patrón {slug} y un botón para generar el token, los campos Nombre y Descripción, y un botón Crear activo." />

Haz clic en **Crear activo** para guardar. El botón sigue desactivado hasta que eliges un tipo y
escribes un token.

## La página de detalle

Abre una instancia para ver su página de detalle. La cabecera muestra el nombre, el token (con un
botón para copiarlo) y el tipo. **Eliminar** está arriba a la derecha; pide confirmación, y la
eliminación no se puede deshacer. Si algo depende todavía de la instancia, la consola muestra el
rechazo del servidor.

Eliminar un cliente también retira su token: no puedes crear otro cliente con el mismo token en este
tenant. Las alarmas, los comandos, las asignaciones de actualización, los paneles compartidos y el
historial de eventos nombran al cliente por su token y permanecen después de eliminarlo, así que un
cliente nuevo con ese token los heredaría. Elige otro token para el cliente nuevo.

La pestaña **Básico** contiene el mismo formulario que usaste para crear la instancia. Puedes cambiar
el tipo, el nombre y la descripción y hacer clic en **Guardar cambios**. El token se muestra, pero no
se puede editar.

<GuideShot id="asset-detail-basic" alt="La página de detalle de HVAC Unit 1, en la pestaña Básico: tipo de activo HVAC Unit, el token de solo lectura con la nota de que el id del activo no puede cambiar, Nombre, Descripción y el botón Guardar cambios. Junto a Básico están las pestañas Facetas, Jerarquía, Propiedades y Dispositivos." />

Todos los activos, clientes y áreas tienen además una pestaña **Facetas**, que se describe
[más abajo](#facets). Los activos tienen tres pestañas más: Jerarquía, Propiedades y Dispositivos.
Los clientes tienen dos: Dispositivos y Usuarios.

### Jerarquía (activos)

Los activos forman un árbol. Una bomba puede colgar de la sala de máquinas a la que pertenece, y un
motor de ventilador de su unidad HVAC. La pestaña **Jerarquía** muestra dónde está el activo y qué
tiene justo debajo.

- **Ubicación en la jerarquía** muestra la ruta por encima del activo, o indica que está en la cima.
- Para mover el activo, elige un **Activo superior** y haz clic en **Colocar bajo un superior**. Esto
  sustituye su activo superior actual, y sus propios activos dependientes se mueven con él.
- **Desvincular** saca el activo de debajo de su superior, de modo que pasa a estar en la cima.
- **Directamente debajo** lista los activos que dependen de este. Haz clic en uno para abrirlo.

<GuideShot id="asset-detail-hierarchy" alt="La pestaña Jerarquía de HVAC Unit 1. Indica que el activo está en la cima de la jerarquía, ofrece un selector de activo superior con los botones Colocar bajo un superior y Desvincular, y lista dos activos directamente debajo: el ventilador de impulsión y el compresor." />

### Propiedades (activos)

Un tipo de activo puede publicar un **contrato de propiedades**: una lista de valores con nombre y
tipo que sus activos deben llevar, como un fabricante o una capacidad nominal. En la pestaña
**Propiedades** del activo rellenas esos valores. Si el tipo del activo no ha publicado un contrato,
la pestaña lo indica y el activo no lleva propiedades. El contrato se prepara en el tipo de activo,
como se explica en [Tipos de activo](#asset-types).

### Dispositivos (activos)

La pestaña **Dispositivos** lista los dispositivos asignados a este activo: es la vista inversa de la
asignación de un dispositivo. Haz clic en un token para abrir el dispositivo. Para asignar un
dispositivo a un activo, consulta [gestión de asignaciones](../guides/managing-assignments.md).

<GuideShot id="asset-detail-devices" alt="La pestaña Dispositivos de HVAC Unit 1, con un único dispositivo, bp-therm-001." />

### Dispositivos y Usuarios (clientes) {#customer-devices-and-users}

La pestaña **Dispositivos** de un cliente lista los dispositivos asignados a él, igual que la de un
activo. Son los dispositivos que ven los propios usuarios del cliente.

La pestaña **Usuarios** lista los miembros cuyo acceso está limitado a este cliente: los accesos
propios del cliente. **Invitar usuario** invita a alguien con su acceso ya limitado a este cliente.
La pestaña solo se muestra si puedes leer los miembros del inquilino. Los usuarios de cliente se
describen en
[Miembros, roles y usuarios de cliente](/user-guide/members-roles-and-customer-users#customer-users).

{/* TODO(screenshot): customer-detail-users, a customer's Users tab. */}

### Facetas {#facets}

La pestaña **Facetas** guarda los **valores de faceta** de la instancia: los valores de clasificación
por los que filtras en la pantalla Explorar, como la zona climática en la que está un edificio. El valor de
una faceta vive en la propia instancia. La pantalla Facetas solo declara qué claves son facetas, así
que una faceta declarada sin valor aquí no coincide con nada.

Si todavía no hay ninguna faceta declarada para la familia, la pestaña lo indica y te remite a la
pantalla Facetas. Debajo, **Otros atributos** lista todo lo demás que lleva la instancia. Los valores
informados por un dispositivo (CLIENT) o establecidos por la plataforma (SERVER) se muestran solo
como referencia, y un filtro de facetas no los lee. **Añadir atributo** guarda una clave nueva; para
filtrar por ella, declárala en la pantalla Facetas.

<GuideShot id="customer-detail-facets" alt="La pestaña Facetas del cliente Acme Properties. En Valores de faceta, una nota indica que aún no hay facetas declaradas para esta familia. En Otros atributos, un botón Añadir atributo y la nota No hay otros atributos." />

<GuideShot id="area-detail-facets" alt="La pestaña Facetas del área Building 1, con la misma disposición: no hay facetas declaradas para la familia de áreas ni otros atributos." />

Editar valores de atributos requiere el permiso `device:write`, para todas las familias. Las
facetas, Explorar y los grupos dinámicos se tratan en
[Grupos, facetas y Explorar](./groups-facets-browse.md).

## Tipos

Los tipos son plantillas que clasifican instancias. Cada lista de tipos muestra la columna Apariencia
(la insignia con la que se dibuja el tipo), el token, una descripción y la fecha de creación.

<GuideShot id="area-types-list" alt="La lista Tipos de área con un tipo, Building, de token bp-building. Arriba a la derecha está el botón Nuevo tipo de área." />

La página de detalle de un tipo tiene una pestaña **Básico** para su nombre y descripción, y una
pestaña **Apariencia** para el icono y los colores de su insignia. La insignia aparece allí donde se
muestra una instancia de ese tipo.

### Tipos de activo: contratos de propiedades y versiones {#asset-types}

Los tipos de activo tienen dos pestañas más, porque un tipo de activo puede declarar qué llevan sus
activos.

La pestaña **Propiedades** contiene el borrador del **Contrato de propiedades**: un arreglo JSON de
descriptores de propiedad. Cada uno tiene un `name` y un `dataType` de `DOUBLE`, `INT`, `BOOLEAN` o
`STRING`, más `description`, `unit`, `required`, `default`, `minValue`, `maxValue` y `enum` opcionales.
Los activos del tipo se validan contra él: una propiedad no declarada, un valor del tipo equivocado
o una obligatoria ausente se rechazan.

Dos cosas que conviene saber:

- Dejar el campo vacío **retira** el contrato. No es lo mismo que una lista vacía `[]`, que declara
  que los activos de este tipo no llevan nada.
- Guardar solo cambia el borrador. Los activos se validan contra la versión **publicada** hasta que
  vuelvas a publicar.

<GuideShot id="asset-type-properties" alt="La pestaña Propiedades del tipo de activo HVAC Unit. Una nota indica que los activos de este tipo se validan contra la versión publicada 1 y que las ediciones solo tienen efecto al volver a publicar. El campo Contrato de propiedades contiene un arreglo JSON que declara manufacturer, ratedCapacityKw, installedYear y serviceTier." />

La pestaña **Versiones** muestra la versión activa y todas las versiones publicadas, con su etiqueta,
cuándo se publicó y quién la publicó.

- **Publicar** congela el borrador actual en una nueva versión y la convierte en la versión con la que
  se validan los activos. Puedes darle a la versión una etiqueta y una descripción.
- **Revertir** convierte una versión anterior en la activa. El borrador no cambia. Las propiedades ya
  almacenadas no se revalidan ni se reescriben, así que un activo que ya no encaja queda como está
  hasta que alguien vuelva a escribir sus propiedades.

Publicar, revertir y editar un contrato de propiedades también requieren `device:write`.

<GuideShot id="asset-type-versions" alt="La pestaña Versiones del tipo de activo HVAC Unit. Versión activa: 1. Una fila: la versión 1, marcada como activa, con la etiqueta Nameplate properties, su fecha de publicación, quién la publicó y la acción Revertir. Arriba a la derecha está el botón Publicar." />

## Grupos

Un grupo reúne instancias de una familia: los Grupos de activos contienen activos; los de
clientes, clientes, y los de áreas, áreas. Las páginas de lista y de detalle de un grupo
funcionan como las demás. Su página de detalle tiene una pestaña **Miembros** donde añades instancias
al grupo y las quitas.

Los grupos también pueden ser **dinámicos**: en lugar de una lista que mantienes a mano, el grupo es
un filtro guardado cuyos miembros se calculan cada vez que se lee. Se crean en la pantalla Explorar.
Consulta [Grupos, facetas y Explorar](./groups-facets-browse.md).

Eliminar un grupo retira su token, igual que eliminar un cliente: no puedes crear otro grupo con el
mismo token en este tenant. El acceso de un miembro puede limitarse a un grupo, y los registros
escritos mientras un dispositivo estaba en él nombran al grupo por su token, así que un grupo nuevo
con ese token los heredaría. Elige otro token para el grupo nuevo.

## Relacionado

- [Modelo de dominio](../concepts/domain-model.md): activos, la jerarquía de activos, grupos y
  atributos.
- [Gestión de asignaciones](../guides/managing-assignments.md): vincular dispositivos a activos,
  áreas y clientes.
- [Grupos, facetas y Explorar](./groups-facets-browse.md)
- [Geocercas](./geofences.md)
- [Miembros, roles y usuarios de cliente](/user-guide/members-roles-and-customer-users)
