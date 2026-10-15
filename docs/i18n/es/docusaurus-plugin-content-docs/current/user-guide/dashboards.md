---
title: Paneles
sidebar_position: 8
---

import GuideShot from '@site/src/components/GuideShot';

# Paneles

Un panel organiza widgets en vivo (gráficos, indicadores, tarjetas, tablas de alarmas, botones de
comando, mapas) en una sola página. Los paneles pertenecen al inquilino y cada uno tiene versiones:
editas un borrador y después lo publicas como la versión que ven los demás.

Este capítulo es para quien crea y mantiene paneles. Cubre la lista de paneles, crear un panel desde
cero, desde una plantilla o desde un archivo exportado, verlo, editarlo en el lienzo, configurar
widgets, hacer una vista previa con datos sintéticos, exportarlo y publicar y revertir versiones. Las
capturas usan un inquilino de prueba cuyo panel *Widget Lab* contiene un ejemplar de cada widget
integrado. Para saber cómo funcionan los paneles por dentro, consulta [Paneles](../concepts/dashboards.md).

:::note Quién puede hacer qué
Cualquier miembro del inquilino puede abrir un panel. Crear, editar, publicar y eliminar uno requiere
el permiso de escritura de paneles. A un miembro sin ese permiso se le muestra la versión publicada,
nunca el borrador. Si un panel nunca se ha publicado, se le indica que pida a un autor que lo publique.
:::

## La lista de paneles

Elige **Paneles** en la barra lateral.

<GuideShot id="dashboards-list" alt="La página Paneles con los botones Importar panel y Nuevo panel. Hay dos paneles, Widget Lab — Stress y Widget Lab, cada uno con una descripción, la hora de actualización y las acciones Clonar y Eliminar." />

Cada fila muestra el nombre del panel, su descripción y cuándo se actualizó por última vez.
Selecciona una fila para abrirlo. **Clonar** hace una copia con un token nuevo, y **Eliminar** borra
el panel tras pedirte confirmación. La eliminación no se puede deshacer.

## Crear un panel

**Nuevo panel** abre un panel lateral con dos formas de empezar.

<GuideShot id="dashboards-new" alt="El panel lateral Nuevo panel con los campos Token, Nombre y Descripción y un botón Crear panel. Debajo, bajo O empieza desde una plantilla, se ofrecen tres plantillas: Resumen de la flota, Detalle de dispositivo y Panel de alarmas, cada una con una descripción de una línea." />

- **Desde cero.** Escribe un token, el identificador permanente del panel, o usa el botón junto al
  campo para generar uno. El token es el único campo obligatorio; el nombre y la descripción son
  opcionales. El campo marca un token que ya está en uso. Elige **Crear panel** y se abre el panel vacío.
- **Desde una plantilla.** Bajo **O empieza desde una plantilla**, elige **Resumen de la flota**
  (recuentos de alarmas por severidad, una tabla de alarmas y un dispositivo destacado), **Detalle de
  dispositivo** (últimos valores, un indicador, un gráfico de telemetría, la ubicación y las alarmas
  de un dispositivo) o **Panel de alarmas** (recuentos y tablas de alarmas de todo el inquilino). La
  plantilla se abre en la revisión de importación que se describe más abajo, donde la vinculas a tus
  propios dispositivos antes de crearla.

Los nombres de medición de las plantillas, como `temperature` y `humidity`, son ejemplos. Cámbialos
en el editor por los que informan tus dispositivos.

## Importar un panel

**Importar panel** crea un panel nuevo a partir del JSON exportado de otro panel o de otro inquilino.

<GuideShot id="dashboards-import" alt="El panel lateral Importar panel. Explica que el panel nuevo se guarda como borrador con un token nuevo y que no se sobrescribe ni se publica nada. Tiene un cuadro de texto JSON del panel, una indicación de que el panel puede ocupar hasta 1 MiB sin contar la sangría y los botones Elegir archivo y Revisar." />

1. Pega el JSON en **JSON del panel**, o usa **Elegir archivo** para cargar un archivo `.json`.
2. Elige **Revisar**. La consola comprueba la definición y enumera lo que conviene que mires antes de
   guardarla:
   - **Vínculos por revisar**: huecos que aún no están vinculados, o que nombran un dispositivo,
     cliente, área o activo que no existe en este inquilino. Elige uno de este inquilino para cada
     uno, o deja el selector en *Dejar sin vincular* y vincúlalo más tarde.
   - widgets que esta consola no puede mostrar, que se conservan como marcadores y bloquean la
     publicación hasta que se instalen o se quiten;
   - opciones de widget que hay que corregir;
   - sitios externos de los que el panel carga imágenes o mosaicos de mapa.
3. Da un token al panel nuevo y elige **Crear borrador**.

Una importación nunca sobrescribe un panel existente, y nunca se publica por ti. **Clonar** y las
plantillas usan la misma revisión.

## Ver un panel

Al abrir un panel se muestra con datos en vivo.

<GuideShot id="dashboard-view" alt="El panel Widget Lab en modo de visualización, con los botones Vista previa, Exportar, Historial, Editar y Eliminar. Los widgets incluyen una etiqueta de título, una tarjeta de último valor y un indicador de temperatura de wl-sensor-01, un recuento de alarmas activas de wl-zone-01, una imagen, un gráfico de temperatura y humedad y una tabla de últimos valores con battery, humidity, pressure y temperature." />

Los botones de la parte superior:

- **Vista previa** sustituye los datos en vivo por datos generados (consulta
  [Vista previa con datos sintéticos](#previewing-with-synthetic-data)).
- **Exportar** copia o descarga el panel (consulta [Exportar](#exporting)).
- **Historial** abre el historial de versiones (consulta [Versiones](#versions)).
- **Editar** pasa al editor.
- **Eliminar** borra el panel.

Los widgets con acciones, como reconocer una alarma o enviar un comando, también funcionan aquí. La
plataforma comprueba cada acción contra tus propios permisos.

## Editar en el lienzo

**Editar** convierte el panel en un lienzo. Los botones pasan a ser **Añadir widget**, **Vista
previa**, **Exportar**, **Historial**, **Guardar** y **Hecho**, y aparece una barra de ajustes de
diseño.

<GuideShot id="dashboard-edit" alt="El panel Widget Lab en modo de edición. La cabecera muestra Añadir widget, Vista previa, Exportar, Historial, Guardar desactivado y Hecho. Una barra de ajustes muestra Título Widget Lab, Columnas 12, Celda mínima 56, Separación 8, Margen 0 y Tamaño Llenar área. Los widgets aparecen enmarcados en el lienzo; algunos muestran un guion o el texto de que aún no hay datos." />

- **Título** es el nombre del panel.
- **Columnas**, **Celda mínima**, **Separación** y **Margen** definen la cuadrícula a la que se
  ajustan los widgets. Las celdas son cuadradas y se estiran para llenar el ancho; **Celda mínima**
  es el tamaño más pequeño que puede tener una celda, en píxeles. **Separación** es el espacio
  entre celdas y **Margen**, el borde alrededor del panel.
- **Tamaño** decide cómo ocupa el panel la página: **Llenar área**, **Ancho fijo** o **Alto fijo**.
- **Intervalo de tiempo** es el intervalo que dibujan los gráficos del panel al abrirlo: 15m, 1h, 6h,
  24h o 7d. Una hora es el valor predeterminado. Los espectadores pueden elegir otro desde la cabecera
  y pausar el panel; consulta
  [Visualización de paneles en /dash](/user-guide/viewing-dashboards-in-dash#time-range-and-pause).

Arrastra un widget para moverlo. Un contorno discontinuo muestra las celdas que ocupará al soltarlo,
y queda exactamente ahí. Un widget que esté en su camino pasa al hueco libre más cercano, encima o
debajo, de modo que soltar un widget sobre otro los intercambia. Nada más se mueve: un hueco que
dejaste a propósito sigue abierto. Los widgets de la cuadrícula nunca se solapan, y un widget nuevo
ocupa el primer hueco libre en el que cabe. Selecciona un widget y arrastra un tirador de su borde o esquina para cambiar su
tamaño. Mientras arrastras, el tamaño se muestra como columnas por filas, por ejemplo *2×3*, y el
widget se detiene en el tamaño mínimo y máximo que admite. Un widget que solo puede cambiar de
tamaño en una dirección tiene tiradores únicamente en ese lado. Pulsa **Esc** mientras arrastras
para cancelar. Con un widget enfocado, las flechas lo mueven una celda y **Mayús** con las flechas
cambia su tamaño. Un widget fuera de los tamaños que admite, o que sobrepasa el borde derecho,
muestra una nota que lo indica. El panel mide el espacio que se le da, no la pantalla. Mientras las columnas
caben con **Celda mínima** o más, muestra tu diseño tal como lo dispusiste y las celdas se estiran
para llenar el ancho, de modo que una pantalla grande obtiene celdas más grandes y no más celdas.
En un espacio más estrecho usa tantas columnas como quepan y reparte los widgets en ellas en orden
de lectura, de arriba abajo y de izquierda a derecha. Por debajo de 600 px de ancho, como en un
teléfono, cada widget ocupa su propia fila a todo el ancho, de modo que el panel se lee como una
sola columna. Cada widget conserva su tamaño dentro de los límites que admite, y un widget colocado
en relación con otro se mueve con él. Un widget que todavía no ha
recibido datos muestra un guion o *No data yet* (este texto aparece en inglés también en la consola
en español).

Un panel guardado por una versión anterior se convierte a la cuadrícula actual al abrirlo. La
conversión es aproximada, así que el editor muestra un aviso que pide revisar el diseño. Al guardar,
el panel queda en el formato actual y el aviso desaparece.

Un gráfico de series temporales guardado por una versión anterior conservaba un número de muestras
en lugar de un intervalo de tiempo. Un número de muestras no tiene intervalo si no se sabe con qué
frecuencia informa el dispositivo, así que el gráfico no se convierte a un intervalo adivinado: sigue
el intervalo de tiempo del panel, y el editor nombra los gráficos afectados. Indica un **Intervalo de
tiempo** en cualquier gráfico que deba ser distinto y guarda.

**Guardar** almacena los cambios en el borrador. Está atenuado hasta que hay algo que guardar.
**Hecho** vuelve a la vista; los cambios sin guardar se mantienen y **Guardar** sigue disponible allí.
Si cierras la pestaña o usas el enlace **Paneles** para volver con cambios sin guardar, la consola te
pregunta antes de descartarlos. Elegir otra página en la barra lateral no pregunta, así que guarda
antes. Guardar
solo cambia el borrador. Los espectadores no ven nada nuevo hasta que publicas una versión.

### Añadir un widget

**Añadir widget** abre el menú de widgets, agrupados según lo que hace cada uno.

<GuideShot id="dashboard-add-widget" alt="El menú Añadir widget abierto en modo de edición, agrupado en Telemetría, Estático, Alarmas, Control, Navegación y Ubicación. Cada entrada muestra su identificador y su versión, por ejemplo timeseries-chart · v1.0.0." />

| Grupo | Widgets |
|---|---|
| Telemetría | Gráfico de series temporales, Tarjeta de último valor, Indicador, Tabla, Indicador lineal, Indicador de nivel, Batería, Luz de estado, Barras de señal, Brújula, Anillo de progreso, Tarjeta de estadística, Gráfico de barras, Gráfico circular |
| Estático | Etiqueta, Imagen |
| Alarmas | Tabla de alarmas, Contador de alarmas |
| Control | Botón de comando, Control deslizante de comando, Interruptor de comando |
| Navegación | Selector de entidad, que permite al espectador elegir qué dispositivo o entidad muestra el panel |
| Ubicación | Mapa, que dibuja sobre el mapa del inquilino (consulta [Configuración del inquilino](./tenant-settings.md)) |

Cada entrada muestra el tamaño, en celdas, con el que se añade el widget, por ejemplo *4×3*. Elige
uno para añadirlo en el primer hueco libre del lienzo y después configúralo.

### Configurar un widget

Selecciona un widget en el lienzo para abrir sus ajustes a la derecha. **Cuadrícula**, **Relativo
a…** y **Eliminar** aparecen sobre el propio widget.

<GuideShot id="dashboard-widget-config" alt="Modo de edición con la tarjeta de último valor Temperature seleccionada, con los botones Al frente y Eliminar sobre el widget. El panel de ajustes de la derecha muestra el título Temperature, una sección Contexto con Se vincula a en Con ámbito a un principal, Slot principal zone y Estrategia Selección manual, una sección Fuente de datos que explica que deriva del contexto principal, slot sensor, y un campo Medición." />

Los ajustes dependen del widget. Los más comunes:

- **Título** que se muestra en el widget.
- **Contexto**: a qué está vinculado el widget. **Contexto raíz** lo vincula directamente a un
  dispositivo o entidad. **Con ámbito a un principal** hace que siga el contexto de otro widget, por
  ejemplo un dispositivo dentro del edificio elegido en otra parte del panel. Con un principal,
  **Estrategia** decide si sigue al **Primer miembro** del principal o a un miembro que tú eliges
  (**Selección manual**).
- **Fuente de datos** elige el dispositivo o la entidad que lee el widget. En un widget que sigue a
  un principal, explica que el widget lo sigue.
- **Medición** y opciones de presentación como unidades, rango y precisión.
- **Intervalo de tiempo** (gráfico de series temporales): cuánto historial dibuja el gráfico, en
  segundos, de 60 a 604800. Si lo dejas vacío, el gráfico sigue el intervalo de tiempo del panel,
  que los espectadores pueden cambiar. Si lo indicas, el gráfico conserva ese intervalo sea cual
  sea el que elija un espectador.
- **Estadística** (tarjeta de estadística): qué cifra del intervalo mostrar: promedio (por defecto),
  mínimo, máximo, suma, número de lecturas o la última lectura. La tarjeta escribe bajo la cifra la
  estadística y el intervalo que abarca, por ejemplo «Máximo · 1h». El mismo ajuste **Intervalo de
  tiempo** fija la tarjeta a un intervalo propio. La tarjeta dice por qué no tiene cifra (la fuente
  de datos no puede calcular agregados, la lectura falló o no hubo lecturas en el intervalo) en lugar
  de mostrar un número que no pudo calcular.
- **Orden** (gráfico de barras, gráfico circular): las mediciones en el orden en que las lista la
  fuente de datos, o de menor a mayor o de mayor a menor. Una medición que aún no ha informado va
  la última. El gráfico de barras dibuja columnas en un widget ancho y filas en uno alto.

Los problemas con las opciones de un widget, como una opción que no lee, una obligatoria vacía o un
valor fuera de rango, se enumeran en **Historial de versiones**. **Publicar versión** sigue
desactivado hasta corregirlos.

Los problemas de colocación también mantienen desactivado **Publicar versión**: un widget fuera de
los tamaños que admite, que sobrepasa el borde derecho o que, colocado en relación con otro, queda
fuera del panel. El editor no los produce por sí solo; llegan con un panel importado, una versión
anterior de un widget o un panel escrito mediante la API. **Ajustar a los límites**, en la cabecera
del editor mientras el panel tenga alguno y en **Historial de versiones**, ajusta cada widget a los
tamaños que admite y devuelve al panel cualquier widget que quede fuera. Un widget que un tamaño
mayor pase a cubrir baja; los demás se quedan donde están. Revisa el resultado y guarda.

### Colocar un widget en relación con otro

Un widget normalmente está en la cuadrícula. También puede colocarse **Relativo a…** otro, por
ejemplo una insignia sobre un mapa o un pie bajo un gráfico. Selecciona el widget, elige
**Relativo a…** y haz clic en el widget del que debe depender, o lleva el foco a él y pulsa
**Intro**. Aparece un selector con nueve puntos
de anclaje; elige la esquina, el borde o el centro del anfitrión al que se une.

Un widget relativo conserva su tamaño, se mueve cuando se mueve su anfitrión, puede solaparlo y se
dibuja sobre la cuadrícula. Una línea discontinua lo une a su anfitrión. Arrástralo para alejarlo
del anclaje en celdas completas, o mantén **Mayús** para moverlo en cuartos de celda. Elige
**Cuadrícula** para devolverlo a la cuadrícula, en el primer hueco libre. Un widget relativo no
puede ser a su vez anfitrión.

## Vista previa con datos sintéticos {#previewing-with-synthetic-data}

**Vista previa** sustituye los datos en vivo por valores generados, para que puedas comprobar
escalas, umbrales y diseño antes de que informe ningún dispositivo. Un selector junto al botón elige
la forma de los datos: *Sine wave* (onda sinusoidal), *Ramp* (rampa) o *Random walk* (paseo
aleatorio). Estos nombres aparecen en inglés también en la consola en español. La vista previa
funciona en los modos de visualización y de edición. Elige **Vista previa** de nuevo para volver a
los datos en vivo.

<GuideShot id="dashboard-preview" alt="El panel Widget Lab en modo de vista previa, con el selector de generador en Sine wave y el botón Vista previa resaltado. El gráfico muestra curvas sinusoidales suaves, el recuento de alarmas muestra 1 alarma de severidad mayor y las tarjetas y el indicador muestran valores generados." />

Los datos generados se quedan en tu navegador y nunca se guardan.

## Exportar {#exporting}

**Exportar** ofrece cuatro opciones.

<GuideShot id="dashboard-export" alt="El menú Exportar abierto en el panel Widget Lab, con las opciones Descargar JSON, Copiar JSON, Copiar como plantilla (sin vincular) y Clonar como panel nuevo…." />

- **Descargar JSON** guarda el panel como un archivo.
- **Copiar JSON** lo copia al portapapeles.
- **Copiar como plantilla (sin vincular)** lo copia sin sus vínculos a dispositivos y entidades, para
  que quien lo importe elija los suyos.
- **Clonar como panel nuevo…** abre la revisión de importación con este panel, para crear una copia
  con un token nuevo.

## Versiones {#versions}

**Historial** abre el historial de versiones. El borrador de un panel es lo que editas; una
**versión** es una copia congelada del borrador que se muestra a los espectadores.

<GuideShot id="dashboard-history" alt="El panel Historial de versiones. Una sección Publicar borrador actual tiene un campo Etiqueta opcional, un campo de descripción, la nota de que al publicar la versión nueva pasa a ser la que ven los espectadores y un botón Publicar versión. Debajo, Versiones publicadas muestra la versión 1, marcada como Activa, publicada por dana.okafor@acme-properties.example, con un botón Revertir." />

- **Publicar borrador actual.** Añade una etiqueta opcional, como el nombre de una entrega, y una
  descripción, y elige **Publicar versión**. La versión nueva queda activa de inmediato. Guarda antes
  tus cambios: la consola publica el borrador guardado.
- **Versiones publicadas** enumera cada versión con cuándo y quién la publicó. La que está en uso
  aparece marcada como **Activa**.
- **Activar** sirve a los espectadores una versión anterior sin tocar tu borrador.
- **Revertir** sustituye el borrador por una versión anterior. Se pierde todo lo del borrador que no
  hubieras publicado, y la consola te pide confirmación.

El historial solo crece. Publicar, activar una versión y revertir dejan las versiones anteriores en
su sitio.

## Relacionado

- [Paneles](../concepts/dashboards.md): widgets, fuentes de datos, versiones e incrustación
- [Mapas base](../concepts/basemaps.md), para los mosaicos del widget de mapa
