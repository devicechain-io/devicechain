---
title: Tipos y perfiles de dispositivo
sidebar_position: 4
---

import GuideShot from '@site/src/components/GuideShot';

# Tipos y perfiles de dispositivo

Todo dispositivo pertenece a un **tipo de dispositivo**, y un tipo de dispositivo apunta a un
**perfil de dispositivo**. Los dos se reparten el trabajo:

- El **tipo de dispositivo** dice qué clase de cosa es un dispositivo: su nombre, su fabricante y
  modelo, cómo se ve en las listas y qué ventana de mantenimiento esperan sus comandos
  disruptivos.
- El **perfil de dispositivo** dice qué puede hacer el dispositivo: las mediciones que envía, los
  comandos que acepta, la configuración que recibe y las reglas que lo vigilan. Varios tipos de
  dispositivo pueden compartir un perfil.

Este capítulo trata los dos, incluida la publicación de una versión de perfil, la vuelta a una
versión anterior y la descarga de una versión como Thing Model. Es para los administradores del
inquilino y para quien define cómo se comporta una clase de dispositivos. Las reglas de detección
y su estado también viven en el perfil, pero tienen su propio capítulo.

Para las ideas detrás de estas pantallas, consulta [Modelo de dominio](../concepts/domain-model.md).

## Tipos de dispositivo

Abre **Dispositivos** en la barra lateral y luego **Tipos de dispositivo**. La lista muestra la
apariencia, el token, la descripción y la fecha de creación de cada tipo.
**Nuevo tipo de dispositivo** crea uno: dale un token, un nombre y, si quieres, una descripción y
una ventana de mantenimiento.

Selecciona un tipo para abrirlo. Su página tiene cuatro pestañas: **Básico**, **Identidad**,
**Apariencia** y **Perfil**.

### Básico

**Básico** contiene el nombre y la descripción del tipo, y su **Ventana de mantenimiento**. Los
comandos disruptivos y destructivos para los dispositivos de este tipo esperan a que se abra esa
ventana. Déjala vacía para enviarlos de inmediato. Las ventanas de mantenimiento se explican en
[Comandos](../concepts/commands.md#maintenance-windows).

### Identidad

**Identidad** registra el **Fabricante** y el **Modelo** del dispositivo físico.

<GuideShot id="device-type-identity" alt="La página del tipo de dispositivo Building Pulse Thermostat, token bp-thermostat, con las pestañas Básico, Identidad, Apariencia y Perfil. La pestaña Identidad está abierta. Su explicación dice que el fabricante y el modelo son facetas de descubrimiento para filtrar y agrupar. Debajo están los campos Fabricante y Modelo vacíos, con los ejemplos Acme Corp y TS-100, y un botón Guardar identidad desactivado." />

Los dos sirven para filtrar y agrupar dispositivos, así que los campos sugieren valores que ya se
usan. Elige un valor existente siempre que puedas, para mantenerlos coherentes, o escribe uno
nuevo. Después elige **Guardar identidad**.

### Apariencia

**Apariencia** define cómo se dibuja el tipo allí donde aparece, por ejemplo en la lista de
dispositivos y en la página de cada dispositivo.

<GuideShot id="device-type-appearance" alt="La pestaña Apariencia. Una Vista previa muestra la etiqueta del tipo, Building Pulse Thermostat. Debajo hay tres selectores de color, Fondo, Texto y Borde, cada uno con un guion al lado porque no tiene color asignado, y una cuadrícula de Icono con 36 iconos, ninguno seleccionado. Una indicación explica que se elige un icono o se vuelve a hacer clic en el seleccionado para quitarlo. Al final hay un botón Guardar apariencia." />

Elige un color de **Fondo**, de **Texto** y de **Borde**, y un **Icono**. La **Vista previa**
muestra el resultado sobre la marcha. Para quitar el icono, vuelve a seleccionarlo. Elige
**Guardar apariencia** cuando termines.

### Perfil

**Perfil** muestra qué perfil de dispositivo usa el tipo.

<GuideShot id="device-type-profile" alt="La pestaña Perfil. Su explicación dice que un perfil es el contrato de capacidades reutilizable (métricas, comandos y reglas de alarma) que adopta el tipo, y que aquí se elige cuál usa. Una tarjeta muestra el perfil vinculado, Building Pulse Thermostat Profile, token bp-thermostat-profile, de categoría sensor, con los botones Cambiar y Desvincular y un enlace Editar este perfil." />

- **Editar este perfil** abre el perfil, donde lo modificas.
- **Cambiar** te permite vincular otro perfil.
- **Desvincular** quita el perfil del tipo. El perfil en sí no se elimina.

Cuando varios tipos de dispositivo comparten un perfil, la tarjeta lo indica. Un cambio en él
afecta a todos.

Un tipo sin perfil está permitido, pero tiene **capacidad limitada**: sus dispositivos se siguen
listando y mostrando, pero no envían métricas tipadas, no aceptan comandos y no generan alarmas.
Para corregirlo, vincula un perfil existente o elige **Crear un perfil para este tipo**.

## Perfiles de dispositivo

Abre **Dispositivos** en la barra lateral y luego **Perfiles de dispositivo**. La lista muestra el
token, la categoría, cuántos tipos de dispositivo lo usan, la descripción y la fecha de creación de cada perfil.
**Nuevo perfil de dispositivo** crea uno, con un token, un nombre, una **Categoría** (una clase
funcional como termostato o contador) y una descripción.

Selecciona un perfil para abrirlo. Sus pestañas son **Básico**, **Métricas**, **Comandos**,
**Configuración**, **Reglas de detección**, **Estado de reglas** y **Versiones**. Las reglas de
detección y su estado tienen su propio capítulo.

### Borradores y versiones

Un perfil tiene versiones. Todo lo que cambias en sus pestañas modifica un **borrador**. Los
dispositivos no ven el borrador: usan la **versión activa** del perfil, una copia congelada que se
crea al **publicar**. Así, un cambio en una métrica, un comando o una clave de configuración solo
llega a los dispositivos cuando publicas el perfil. Consulta
[Publicar una versión](#publishing-a-version).

### Métricas

**Métricas** enumera las mediciones tipadas que envía un dispositivo de este perfil.

<GuideShot id="profile-metrics" alt="La página del perfil Building Pulse Thermostat Profile, token bp-thermostat-profile, con las pestañas Básico, Métricas, Comandos, Configuración, Reglas de detección, Estado de reglas y Versiones. La pestaña Métricas está abierta con un botón Nueva métrica. Una tabla enumera co2 (DOUBLE, ppm), setpoint (DOUBLE, C), humidity (DOUBLE, %) y temperature (DOUBLE, C), cada una con botones para editar y eliminar." />

Elige **Nueva métrica** para añadir una, o edita una fila para cambiarla. Una métrica tiene:

- una **Clave de la métrica**: el nombre que llevan las mediciones del dispositivo, como
  `temperature`;
- un token y un **Tipo de datos**;
- una **Unidad**, escrita en UCUM, como `Cel`;
- un **Mín.** y un **Máx.** opcionales, que se muestran como rango;
- un nombre y una descripción.

Una métrica también puede ser **calculada** a partir de una fórmula sobre las demás, en lugar de ser enviada
por el dispositivo. Consulta [Métricas calculadas](calculated-metrics.md).

### Comandos

**Comandos** enumera los comandos que acepta un dispositivo de este perfil. Son los comandos que se
ofrecen en la [pestaña Comandos](./devices.md#commands) de un dispositivo.

<GuideShot id="profile-commands" alt="La pestaña Comandos. Un recuadro arriba ofrece añadir los comandos estándar de LwM2M para dispositivos LwM2M. Debajo, un botón Nuevo comando y una tabla con las columnas Clave, Nombre, Impacto, Parámetros y Acciones que enumera identify (Flash the display, Normal, declarado) y set-setpoint (Set temperature setpoint, Normal, declarado)." />

Elige **Nuevo comando** para añadir uno.

<GuideShot id="profile-command-new" alt="El panel Nuevo comando. Tiene un campo Clave del comando con el ejemplo set_point, un campo Token con un botón para regenerarlo, los campos Nombre y Descripción, un selector Impacto con el valor Normal y una indicación de que los comandos disruptivos y destructivos esperan la ventana de mantenimiento del tipo, un cuadro Esquema de parámetros con un ejemplo de arreglo JSON y un botón Crear comando." />

- **Clave del comando**: el comando que acepta el dispositivo, como `set_point`.
- **Token**, **Nombre** y **Descripción**.
- **Impacto**: **Normal**, **Disruptivo** o **Destructivo**. Los comandos disruptivos y
  destructivos esperan la ventana de mantenimiento del tipo de dispositivo. Los normales nunca se
  retienen.
- **Esquema de parámetros** (opcional): un arreglo JSON que describe los parámetros del comando,
  cada uno con un `name` y un `dataType`. Déjalo vacío para un comando sin argumentos. La consola
  construye el formulario de parámetros de la pestaña Comandos de un dispositivo a partir de este
  esquema.

Para dispositivos LwM2M, **Añadir comandos estándar de LwM2M** declara **Reiniciar** y
**Leer información del dispositivo** como comandos con nombre, para que puedas enviarlos sin
escribir la ruta de un objeto. Los comandos ya declarados se dejan como están.

Cómo se comprueban los comandos con el perfil, y qué significa el impacto, se explica en
[Comandos](../concepts/commands.md#commands-and-the-capability-contract).

### Configuración

**Configuración** declara qué claves de atributos compartidos recibe como configuración un
dispositivo de este perfil.

<GuideShot id="profile-configuration" alt="La pestaña Configuración. Su explicación dice que un dispositivo solo ve las claves declaradas, que sin ninguna declarada ve un documento vacío, y que los cambios modifican el borrador y llegan a los dispositivos al publicar el perfil. Arriba a la derecha hay un botón Declarar clave. Una tabla enumera setpoint (DOUBLE), schedule (STRING) y reportIntervalSeconds (LONG) con sus descripciones, cada una con botones para editar y eliminar." />

Un dispositivo solo ve las claves declaradas. Si no hay ninguna, ve un documento vacío. Elige
**Declarar clave** para añadir una: indica la **Clave de atributo**, su **Tipo de valor**
(**Cadena**, **Entero (long)**, **Decimal (double)**, **Booleano** o **JSON**) y una descripción.

Los valores se fijan en cada dispositivo, como atributos compartidos. La
[pestaña Configuración](./devices.md#configuration) de un dispositivo muestra el resultado.

### Publicar una versión {#publishing-a-version}

**Versiones** enumera todas las versiones publicadas del perfil, de la más reciente a la más
antigua, e indica cuál está activa.

<GuideShot id="profile-versions" alt="La pestaña Versiones. Dice 'Versión activa: 2' y explica que publicar toma una instantánea del borrador en una nueva versión y que los dispositivos resuelven la versión activa. Arriba a la derecha hay un botón Publicar. Una tabla con las columnas Versión, Etiqueta, Publicada, Por y Acciones enumera la versión 2 (activa, 'Remote setpoint') y la versión 1 ('Building Pulse Thermostat Profile'), ambas publicadas por dana.okafor@acme-properties.example. Cada fila tiene un botón de descarga Thing Model de WoT y un botón Revertir; el Revertir de la versión activa está atenuado." />

Para publicar tu borrador, elige **Publicar**.

<GuideShot id="profile-version-publish" alt="El panel Publicar perfil, con la descripción de que congela el borrador actual en una nueva versión y la convierte en la versión que resuelven los dispositivos. Tiene un campo Etiqueta opcional con el ejemplo v1.0, un campo Descripción opcional y un botón Publicar." />

Ponle a la versión una **Etiqueta** y una **Descripción** si quieres; las dos son opcionales.
Después elige **Publicar**. El borrador se congela en una versión nueva, que pasa a ser la versión
activa de inmediato. La versión guarda todo lo que hay en el borrador, incluidas sus métricas,
comandos, claves de configuración y reglas de detección. Si varios tipos de dispositivo usan el perfil, la pestaña **Versiones** te
avisa de que publicar cambia lo que usan todos ellos.

Hasta que un perfil se publica por primera vez, sus dispositivos no resuelven nada de él, y la
pestaña dice «Aún no publicada». Publícalo al menos una vez.

### Revertir

Si una versión nueva da problemas, puedes volver a activar una anterior. Elige **Revertir** en la
fila de esa versión y confirma.

<GuideShot id="profile-version-rollback" alt="Un cuadro de confirmación titulado 'Revertir' sobre la pestaña Versiones. Pregunta si hacer que la versión 1 sea la versión activa que resuelven los dispositivos y aclara que el borrador no se modifica, con los botones Cancelar y Revertir." />

Revertir solo cambia qué versión está activa. No toca tu borrador, así que los cambios en los que
estabas trabajando siguen ahí. El botón **Revertir** de la versión activa está atenuado, porque ya
está activa.

Cada publicación y cada reversión también queda registrada en el registro de activaciones del perfil:
qué versión pasó a estar activa, cuándo y quién lo hizo. La columna **Publicada** de la pestaña
Versiones muestra cuándo se publicó una versión, que no es cuándo estuvo activa si después se
revirtió a ella, así que usa el registro para saber qué versión estaba activa en un momento dado. El
registro se lee con el campo `activations` de un perfil de dispositivo en la API GraphQL, de la más
reciente a la más antigua. Empieza cuando actualizas a la versión que lo introdujo, así que un perfil
cuya última publicación o reversión fue anterior no tiene entrada para ese cambio. Interpreta una
entrada ausente como desconocida, no como inactiva.

### Descargar un Thing Model

Cada fila de versión tiene un botón **Thing Model de WoT**. Descarga esa versión como un Thing
Model de W3C Web of Things: un archivo JSON-LD estándar que describe las métricas del perfil como
propiedades y sus comandos como acciones. Puedes entregarlo a herramientas que entiendan Web of
Things. El archivo se nombra con el token del perfil y la versión, por ejemplo
`bp-thermostat-profile.v2.tm.jsonld`.

Si una versión usa algo que un Thing Model no puede expresar, la descarga falla con un mensaje que
nombra la versión. Qué contiene la exportación, y cuándo se rechaza, se describe en
[Exportar un perfil como Thing Model de W3C](../concepts/domain-model.md#wot-thing-model).

## Relacionado

- [Dispositivos](./devices.md)
- [Modelo de dominio](../concepts/domain-model.md)
- [Comandos](../concepts/commands.md)
- [Enviar un comando](../guides/sending-commands.md)
- [Conexión de un dispositivo](../guides/connecting-a-device.md)
