---
title: Geocercas
sidebar_position: 10
---

import GuideShot from '@site/src/components/GuideShot';

# Geocercas

Una **geocerca** es un límite dibujado sobre un mapa. Las reglas de detección comprueban contra ella
las posiciones que informan tus dispositivos, por ejemplo para generar una alarma cuando un
rastreador sale de un recinto. Este capítulo muestra cómo dibujar y editar geocercas en la consola,
y cómo ver la forma que tenía una geocerca en el pasado. Es para los usuarios y administradores de un
inquilino que mantienen las geocercas. Para las reglas que las usan, consulta
[usar una geocerca en una regla](../concepts/geofencing.md#using-a-fence-in-a-rule).

Las geocercas están en **Organización → Áreas → Geocercas**, en la barra lateral.

## La lista de geocercas

La lista muestra el token, el nombre, el número de vértices y la fecha de creación de cada geocerca.
Haz clic en una fila para abrirla.

<GuideShot id="geofences-list" alt="La lista Geocercas con una geocerca, acme-campus, llamada Acme campus perimeter, con 4 vértices. Arriba a la derecha está el botón Nueva geocerca." />

## Dibujar una geocerca nueva

Haz clic en **Nueva geocerca**. El panel pide:

- **Token**: el identificador con el que las reglas nombran la geocerca, por ejemplo en
  `geo.inFence("acme-campus")`. El botón de al lado genera uno a partir del patrón de tokens de tu
  instancia. El token no puede cambiar una vez creada la geocerca.
- **Nombre** y **Descripción**: texto libre que puedes cambiar cuando quieras.
- **Límite**: la forma, dibujada sobre el mapa.

<GuideShot id="geofences-new" alt="El panel Nueva geocerca: los campos Token, Nombre y Descripción sobre un mapa de Límite de Europa y África. Bajo el mapa, un contador indica 0 de 511 vértices, con los botones Deshacer el último punto y Borrar todos los puntos, y una sección Mapa base plegada." />

Para dibujar el límite:

1. Acerca y desplaza el mapa hasta tu recinto.
2. Haz clic en el mapa para colocar cada vértice.
3. Si quieres, vuelve a hacer clic en el primer vértice para cerrar la figura; así los clics en el
   mapa dejan de añadir vértices.
4. Haz clic en **Crear geocerca**.

Mientras dibujas, el contador bajo el mapa indica cuántos vértices has colocado y el máximo
permitido. **Deshacer el último punto** quita el último vértice, y **Borrar todos los puntos** empieza
de nuevo.

La consola comprueba la figura sobre la marcha. Un límite necesita al menos tres vértices, y dos
vértices no pueden estar en el mismo lugar; la consola no guarda una figura que incumpla cualquiera
de las dos reglas. También avisa si las aristas se cruzan, porque una figura con aristas que se
cruzan no tiene un interior definido. Esa comprobación es una aproximación, así que no impide
guardar: la respuesta del servidor es la que decide. Consulta las
[reglas de los límites](../concepts/geofencing.md#what-a-boundary-may-be) para más detalle.

### El mapa bajo la geocerca

El mapa muestra el mapa base de tu inquilino, que un administrador del inquilino configura una sola
vez para todos en **Configuración → Mapa**. La sección **Mapa base** bajo el mapa te permite probar
otro proveedor de teselas: indica una **URL de teselas** y su **Atribución**. Esa preferencia se
recuerda solo en tu navegador: no se comparte con otros usuarios ni se guarda en el servidor. La URL
de teselas y la atribución van juntas: una URL sin línea de créditos se ignora y se muestra el mapa
base del inquilino.

Si no hay ninguna fuente de teselas configurada, el mapa recurre a un mapa del mundo incorporado,
solo con continentes y fronteras. El trazado sigue funcionando y las coordenadas que colocas son
exactas. Consulta [Mapas base](../concepts/basemaps.md).

## Editar una geocerca

Abre una geocerca para ver su página de detalle. La pestaña **Básico** muestra el token (de solo
lectura), el nombre, la descripción y el límite sobre el mapa.

<GuideShot id="geofence-detail" alt="La página de detalle de la geocerca Acme campus perimeter, en la pestaña Básico. El token de solo lectura, el nombre y la descripción están sobre un callejero del centro de Chicago, con el contorno de la geocerca, sus vértices superiores y los tiradores de sus aristas dibujados encima." />

En el mapa puedes:

- arrastrar un vértice para moverlo,
- pulsar Alt y hacer clic en un vértice para eliminarlo, o
- hacer clic en el tirador pequeño en mitad de una arista para añadir allí un vértice.

Haz clic en **Guardar cambios** para aplicar lo que has editado. **Eliminar**, arriba a la derecha,
borra la geocerca tras confirmarlo. La eliminación no se puede deshacer. Una regla que siga
nombrando el token de una geocerca eliminada ya no puede evaluarla e informa de errores de
evaluación; consulta [geocercas desconocidas o ausentes](../concepts/geofencing.md#unknown-fence).

Si una geocerca usa una figura que este editor no puede dibujar, la consola lo indica. La geocerca
se conserva intacta y se sigue aplicando; lo único que no está disponible es editarla aquí. Déjala
como está en lugar de volver a crearla.

## Historial

Cada vez que cambia el conjunto de geocercas (se crea una, se modifica su límite o se elimina), la
plataforma congela la forma de todas las geocercas en una nueva **versión del conjunto de
geocercas**. Renombrar una geocerca o editar su descripción no crea una versión, porque las formas
siguen siendo las mismas.

Los eventos de ubicación llevan la versión vigente cuando llegaron. Así, cuando reproduces una regla
sobre eventos pasados, se evalúan con las geocercas tal como eran entonces.

La pestaña **Historial** muestra esta geocerca en cualquier versión. Usa **Anterior** y **Siguiente**,
o escribe un número de versión. **Actual** aparece cuando estás viendo la última versión. La forma
almacenada en la versión elegida se dibuja con trazo continuo, y la forma actual de la geocerca con
trazo discontinuo detrás, para que puedas compararlas.

<GuideShot id="geofence-history" alt="La pestaña Historial de la geocerca Acme campus perimeter. El control Versión del conjunto de geocercas muestra 1, con los botones Anterior y Siguiente y la etiqueta Actual. El límite cuadrado se dibuja sobre el mapa del mundo incorporado, continuo para la versión 1 y discontinuo para la forma actual, con la leyenda debajo." />

El mapa del Historial usa siempre el mapa del mundo incorporado, sin teselas, así que solo se ve la
forma.

Si la geocerca no existía en la versión que eliges, la pestaña lo indica: se creó después, o se
eliminó una geocerca con este token y el token se reutilizó.

## Permisos

Leer geocercas y su historial requiere `device:read`, y eliminar una requiere `device:write`. Crear o
modificar una geocerca requiere además `location:read`, porque dibujar una geocerca es hacer una
pregunta sobre dónde están los dispositivos. Consulta [permisos](../concepts/geofencing.md#permissions).

## Relacionado

- [Geocercas](../concepts/geofencing.md): reglas de los límites, contención esférica, versiones del
  conjunto de geocercas y uso de una geocerca en una regla.
- [Mapas base](../concepts/basemaps.md): de dónde salen las teselas del mapa.
- [Activos, clientes y áreas](./assets-customers-areas.md)
