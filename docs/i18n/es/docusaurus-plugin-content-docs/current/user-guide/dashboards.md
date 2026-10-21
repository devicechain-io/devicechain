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

## Crear un panel a partir de un tipo de dispositivo {#from-a-device-type}

En la pestaña **Perfil** de un tipo de dispositivo, **Crear panel** genera un panel inicial a partir de
lo que declara el perfil, para llegar a un panel útil en un minuto en lugar de colocar widgets uno a
uno. El botón requiere el permiso de escritura de paneles y no se ofrece a un miembro limitado a una
parte del inquilino.

1. Elige **Crear panel**. El panel lateral lee el perfil y muestra una **Vista previa del diseño**.
2. Dale al panel un nombre y un token. Bajo la vista previa, las notas indican cuántas tarjetas,
   gráficos y botones tendrá y qué se dejó fuera.
3. Elige **Crear borrador y abrir el editor**. El panel se crea como borrador, nunca publicado, y se
   abre en el editor.

El panel usa solo los widgets que ya puedes añadir a mano:

- un **selector de dispositivo**, con la ranura apuntando a un dispositivo de este tipo cuando existe
  uno. El selector lista los dispositivos del inquilino, no solo los de este tipo, así que elige un
  dispositivo del tipo para el que creaste el panel. Una nota junto al selector en el panel lo indica;
- una **tarjeta de último valor** por cada medición declarada, en el orden del perfil y con su unidad.
  Una métrica calculada también recibe tarjeta, marcada **Calculada** en el título, y se lee como las demás;
- un **gráfico** de las primeras mediciones numéricas y una **tabla de alarmas** del dispositivo seleccionado;
- un **botón de comando** por cada comando declarado que es seguro enviar desde un panel. Los comandos
  declarados disruptivos o destructivos, o de impacto desconocido, no reciben botón; una nota en el
  panel indica cuántos se omitieron.

Un perfil puede declarar decenas de mediciones. El panel muestra las primeras doce como tarjetas y una
nota en el panel indica cuántas se omitieron; añade el resto en el editor. Un perfil que no declara
mediciones se rechaza con una explicación, porque no hay nada con qué construir.

El panel lateral lee la versión publicada activa del perfil, que es contra lo que la plataforma
comprueba lecturas y comandos, tenga o no el tipo dispositivos todavía. Solo un perfil que nunca se
publicó se lee de su borrador, y el panel lateral lo indica; una medición que nombre el borrador no se
acepta hasta publicar el perfil. Los perfiles no declaran bandas de
severidad, así que las tarjetas no las llevan; añádelas en el editor.

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

<GuideShot id="dashboard-view" alt="El panel Widget Lab en modo de visualización. La cabecera tiene los intervalos 15m, 1h (seleccionado), 6h, 24h y 7d, un botón Pausar, un indicador En vivo y los botones Vista previa, Exportar, Historial, Editar y Eliminar. Los widgets incluyen una etiqueta de título, una tarjeta de último valor y un indicador de temperatura de wl-sensor-01, un recuento de alarmas activas de wl-zone-01, una imagen, un gráfico de temperatura y humedad marcado 15m y una tabla de últimos valores con battery, humidity, pressure y temperature." />

En la parte superior:

- **15m**, **1h**, **6h**, **24h** y **7d** fijan el intervalo de tiempo que dibujan los gráficos, y
  **Pausar** congela el panel. Funcionan igual que en el visor /dash; consulta
  [Intervalo de tiempo y pausa](./viewing-dashboards-in-dash.md#time-range-and-pause).
- **En vivo** indica que la conexión en vivo del panel está activa; un dispositivo que simplemente
  no envía datos no lo cambia. Si se corta la conexión muestra **Reconectando**, y **Sin conexión**
  cuando lleva 20 segundos caída, o de inmediato si el propio navegador se queda sin conexión. Los
  widgets se reanudan solos cuando vuelve la conexión.
- **Vista previa** sustituye los datos en vivo por datos generados (consulta
  [Vista previa con datos sintéticos](#previewing-with-synthetic-data)).
- **Exportar** copia o descarga el panel (consulta [Exportar](#exporting)).
- **Historial** abre el historial de versiones (consulta [Versiones](#versions)).
- **Editar** pasa al editor.
- **Eliminar** borra el panel.

Los widgets con acciones, como reconocer una alarma o enviar un comando, también funcionan aquí. La
plataforma comprueba cada acción contra tus propios permisos.

### Cómo se ven los widgets {#what-the-widgets-look-like}

El panel *Widget Lab* del inquilino de prueba contiene un widget de cada tipo integrado. Más abajo en
la página:

<GuideShot id="dashboard-gauges" alt="Widgets indicadores de wl-sensor-01: un indicador lineal y un termómetro de temperatura, con sus bandas de umbral marcadas a lo largo de la escala, un indicador de nivel Tank level lleno hasta su porcentaje de humedad, un icono Battery con su carga, una luz de estado que indica Normal, barras de señal, una luz Powered que indica Apagado y, debajo, una brújula y dos anillos de progreso." />

El indicador lineal, el indicador de nivel, la batería, la luz de estado, las barras de señal, la
brújula y el anillo de progreso muestran cada uno una medición de un dispositivo. Los umbrales
marcan sus bandas a lo largo de la escala, como en el indicador lineal y el termómetro de arriba, y
una lectura en cualquier banda que no sea la normal toma el color de esa banda.

<GuideShot id="dashboard-stat-cards" alt="Ocho tarjetas de estadística de wl-sensor-01: Average temperature, Peak temperature (en rojo), Lowest temperature y Readings, cada una con su estadística y 1h; después Humidity total, Latest temperature con su lectura rotulada Último y el tiempo desde que llegó, Average, last 5 min con Promedio · 5m, y Peak (smallest)." />

Una tarjeta de estadística muestra una cifra sobre el intervalo de tiempo del panel, con la
estadística y el intervalo que abarca, por ejemplo «Máximo · 1h». Una tarjeta con su propio **Intervalo
de tiempo** lo conserva, como *Average, last 5 min* arriba.

<GuideShot id="dashboard-bar-pie" alt="Dos gráficos de barras con las últimas lecturas de temperatura, humedad y batería de wl-sensor-01: Latest readings, con barras verticales en el orden de la fuente de datos, y Largest first, con barras horizontales de mayor a menor. Debajo, dos gráficos circulares de las mismas lecturas." />

Los gráficos de barras y circulares comparan las últimas lecturas de un dispositivo. Un gráfico de
barras puede, en cambio, comparar una medición entre los dispositivos de un grupo (consulta **Una
barra por** más abajo). Dibuja columnas en un widget ancho y filas en uno alto.

<GuideShot id="dashboard-fleet-table" alt="La tabla de flota Sensors in the zone de wl-zone-01: una fila por dispositivo (wl-sensor-01 y wl-sensor-03) con Temperature (C), ordenada por esa columna, Humidity (%) y Battery (%), cada valor con su antigüedad. Debajo, una tabla de flota más pequeña y un gráfico de barras Temperature by sensor." />

La tabla de flota muestra una fila por dispositivo de un grupo y una columna por medición. Haz clic en
el encabezado de una columna para ordenar por ella. Sus ajustes se describen en
[Configurar un widget](#configurar-un-widget).

## Editar en el lienzo

**Editar** convierte el panel en un lienzo. Los botones pasan a ser **Deshacer**, **Rehacer**,
**Añadir widget**, **Pegar**, **Vista previa**, **Exportar**, **Historial**, **Guardar** y
**Hecho**, y aparece una barra de ajustes de diseño.

<GuideShot id="dashboard-edit" alt="El panel Widget Lab en modo de edición. La cabecera muestra un indicador En vivo, Deshacer y Rehacer, Añadir widget, Pegar, Vista previa, Exportar, Historial, Guardar desactivado y Hecho. Una barra de ajustes muestra Título Widget Lab, Columnas 12, Celda mínima 56, Separación 8, Margen 0, Tamaño Llenar área, Intervalo de tiempo 1h y Densidad Cómoda. Los widgets aparecen enmarcados en el lienzo, cada uno con su última lectura." />

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
- **Densidad** escala el texto y el espaciado de todos los widgets: **Compacta**, **Cómoda** (el
  valor predeterminado) o **Pantalla mural**, para una pantalla que se lee desde el otro lado de una
  sala. El visor /dash puede cambiarla para una pantalla concreta.

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

<GuideShot id="dashboard-add-widget" alt="El menú Añadir widget abierto en modo de edición, con su grupo Telemetría: gráfico de series temporales 6×4, tarjeta de último valor 3×2, indicador 3×3, tabla 6×3, indicador lineal 4×3, indicador de nivel 3×4, batería 3×2, luz de estado 2×2, barras de señal 2×2, brújula 4×4, anillo de progreso 3×3, tarjeta de estadística 3×3, gráfico de barras 6×4, gráfico circular 4×4 y tabla de flota 12×6. Cada entrada muestra su identificador y su versión, por ejemplo timeseries-chart · v1.0.0, y su tamaño en celdas." />

| Grupo | Widgets |
|---|---|
| Telemetría | Gráfico de series temporales, Tarjeta de último valor, Indicador, Tabla, Indicador lineal, Indicador de nivel, Batería, Luz de estado, Barras de señal, Brújula, Anillo de progreso, Tarjeta de estadística, Gráfico de barras, Gráfico circular, Tabla de flota |
| Estático | Etiqueta, Imagen |
| Alarmas | Tabla de alarmas, Contador de alarmas |
| Control | Botón de comando, Control deslizante de comando, Interruptor de comando |
| Navegación | Selector de entidad, que permite al espectador elegir qué dispositivo o entidad muestra el panel |
| Ubicación | Mapa, que dibuja sobre el mapa del inquilino (consulta [Configuración del inquilino](./tenant-settings.md)) |

Cada entrada muestra el tamaño, en celdas, con el que se añade el widget, por ejemplo *4×3*. Desplaza
el menú para ver los grupos que siguen a **Telemetría**. Elige un widget para añadirlo en el primer
hueco libre del lienzo y después configúralo.

### Configurar un widget

Selecciona un widget en el lienzo para abrir sus ajustes a la derecha. **Cuadrícula**, **Relativo
a…**, **Duplicar**, **Copiar** y **Eliminar** aparecen en una barra justo encima del widget (debajo
si no hay espacio encima), y los tiradores de sus bordes y esquinas cambian su tamaño.

<GuideShot id="dashboard-widget-config" alt="Modo de edición con la tarjeta de último valor Temperature seleccionada: los botones Cuadrícula, Relativo a…, Duplicar, Copiar y Eliminar en una barra justo encima del widget, y los tiradores de tamaño en sus bordes y esquinas. El panel de ajustes de la derecha muestra el título Temperature, una sección Contexto con Se vincula a en Con ámbito a un principal, Slot principal zone y Estrategia Selección manual, y el comienzo de una sección Fuente de datos." />

Los ajustes dependen del widget. Los más comunes:

- **Título** que se muestra en el widget.
- **Contexto**: a qué está vinculado el widget. **Contexto raíz** lo vincula directamente a un
  dispositivo o entidad. **Con ámbito a un principal** hace que siga el contexto de otro widget, por
  ejemplo un dispositivo dentro del edificio elegido en otra parte del panel. Con un principal,
  **Estrategia** decide si sigue al **Primer miembro** del principal o a un miembro que tú eliges
  (**Selección manual**).
- **Fuente de datos** elige el dispositivo o la entidad que lee el widget. En un widget que sigue a
  un principal, explica que el widget lo sigue. Solo la tabla de flota y un gráfico de barras con una
  barra por dispositivo pueden leer un grupo de dispositivos; cualquier otro widget de telemetría
  muestra un solo dispositivo y, vinculado a un grupo, lo indica en lugar de mezclar las lecturas de
  varios dispositivos.
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
  de mostrar un número que no pudo calcular. El número de lecturas de un intervalo sin ninguna es 0.
- **Orden** (gráfico de barras, gráfico circular): las mediciones en el orden en que las lista la
  fuente de datos, o de menor a mayor o de mayor a menor. Una medición que aún no ha informado va
  la última. El gráfico de barras dibuja columnas en un widget ancho y filas en uno alto.
- **Una barra por** (gráfico de barras): *Medición* (el valor predeterminado) dibuja una barra por cada medición de un dispositivo. *Dispositivo* dibuja una barra por cada dispositivo del grupo al que está vinculado el widget, para la **Medición** que elija, por lo que la fuente de datos debe ser un grupo de dispositivos. Un grupo demasiado grande se recorta a sus primeras barras en el orden elegido, y el gráfico indica «Mostrando N de M dispositivos».
- **Tabla de flota**: una fila por dispositivo del grupo al que está vinculado el widget y una columna por cada medición que selecciona la fuente de datos. **Columnas** da un encabezado, unidad y precisión por medición; **Umbrales** colorean las celdas de la columna de una medición (la banda normal conserva el color de la tabla); **Obsoleto tras** marca como obsoleta una lectura más antigua que esos segundos, y sin él la tabla solo muestra la antigüedad de cada lectura. **Mostrar antigüedad con retraso** muestra la antigüedad de una lectura solo cuando es al menos esos segundos más antigua que la lectura más reciente de la tabla (o está obsoleta), de modo que una flota que informa cada pocos segundos mantiene una línea por fila y el dispositivo que dejó de informar destaca; el pie del widget sigue mostrando la antigüedad de la lectura más reciente. Haga clic en el encabezado de una columna, o púlselo con el teclado, para ordenar; **Ordenar por** y **Mayor primero** fijan el orden inicial, y los dispositivos sin valor en la columna ordenada quedan siempre al final. **Dispositivos por página** pagina una flota larga. **Ranura de destino** convierte el nombre del dispositivo en un enlace que selecciona ese dispositivo en los widgets vinculados a la ranura. En un tablero estrecho la tabla pasa a una tarjeta por dispositivo. Una celda sin número explica por qué: *sin datos en el rango*, *no informa esta medición* (el dispositivo informa otras mediciones pero nunca esta) o *no se pudo leer*. La tabla lee los primeros 500 dispositivos de un grupo e indica «Mostrando 500 de N dispositivos» cuando el grupo es mayor. Sigue el rango de tiempo del tablero, y un tablero en pausa la detiene.

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

### Deshacer, duplicar y pegar {#undo-duplicate-and-paste}

**Deshacer** y **Rehacer**, los primeros botones del editor, retroceden y avanzan por tus cambios:
mover, redimensionar, añadir y eliminar widgets, su configuración y su origen de datos, los ajustes
de diseño y **Ajustar a los límites**. **Ctrl+Z** (**Cmd+Z** en un Mac) deshace, y
**Ctrl+Mayús+Z** o **Ctrl+Y** (**Cmd+Mayús+Z**) rehace. Un arrastre o un cambio de tamaño es un
solo paso por mucho que se mueva el puntero, y escribir en un campo es un solo paso hasta que sales
del campo. El editor guarda los últimos 100 pasos. Mientras escribes en un campo, estas teclas
deshacen y rehacen lo escrito en ese campo. Un lector de pantalla anuncia qué deshizo cada paso,
por ejemplo «Se deshizo: mover Nivel del tanque».

Deshacer solo vuelve a lo que el panel fue de verdad: puede devolver un problema de colocación que
**Ajustar a los límites** corrigió, pero nunca uno que el panel no tenía. Los pasos se conservan
hasta que sales del panel o restauras una versión. Guardar y publicar los mantienen, así que puedes
deshacer un cambio que acabas de guardar; el panel vuelve entonces a tener cambios sin guardar.

**Duplicar**, en el widget seleccionado, o **Ctrl+D** (**Cmd+D**), añade una copia con el mismo
tamaño, configuración y origen de datos en el primer hueco libre en el que cabe.

**Copiar**, en el widget seleccionado, o **Ctrl+C** (**Cmd+C**), lo copia. **Pegar**, en la
cabecera del editor, añade el último widget copiado en esta pestaña del navegador al panel que
editas, en el primer hueco libre, así que puedes copiar en un panel y pegar en otro. **Ctrl+V**
(**Cmd+V**) pega desde el portapapeles, que también llega a un panel abierto en otra pestaña. Un
tipo de widget que esta consola no tiene no se pega, y la consola lo nombra. Tampoco se pega un
widget copiado en una versión más reciente de DeviceChain: la consola lo indica; actualízala para
pegarlo.

En el mismo panel, una copia conserva su origen de datos. En otro panel solo lo conserva si ese
panel tiene un slot con el mismo nombre vinculado al mismo dispositivo o entidad: los slots se
numeran en cada panel, así que el mismo nombre en dos paneles suele indicar dos dispositivos
distintos. Si no, el widget se pega sin origen de datos y el editor lo indica, con el nombre del
widget y del slot al que estaba vinculado, hasta que eliges un origen de datos en su configuración.
Un widget copiado en otro inquilino nunca conserva su origen de datos, sea cual sea el panel: los
dispositivos pertenecen a su inquilino, así que se pega sin origen de datos y el editor dice por qué.
Un widget colocado en relación con otro se copia en la cuadrícula con su propio tamaño; elige
**Relativo a…** para volver a unirlo.

## Vista previa con datos sintéticos {#previewing-with-synthetic-data}

**Vista previa** sustituye los datos en vivo por valores generados, para que puedas comprobar
escalas, umbrales y diseño antes de que informe ningún dispositivo. Un selector junto al botón elige
la forma de los datos: *Sine wave* (onda sinusoidal), *Ramp* (rampa) o *Random walk* (paseo
aleatorio). Estos nombres aparecen en inglés también en la consola en español. La vista previa
funciona en los modos de visualización y de edición. Elige **Vista previa** de nuevo para volver a
los datos en vivo.

<GuideShot id="dashboard-preview" alt="El panel Widget Lab en modo de vista previa, con el selector de generador en Sine wave y el botón Vista previa resaltado. El gráfico muestra curvas sinusoidales suaves, el recuento de alarmas muestra 1 alarma de severidad mayor, las tarjetas y el indicador muestran valores generados y la tabla muestra un único valor generado." />

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

<GuideShot id="dashboard-history" alt="El panel Historial de versiones. Una sección Publicar borrador actual tiene un campo Etiqueta opcional, un campo de descripción, la nota de que al publicar la versión nueva pasa a ser la que ven los espectadores y un botón Publicar versión. Debajo, Versiones publicadas muestra la versión 2, marcada como Activa, y la versión 1, ambas publicadas por sam.rivera@widgetlab.example. La versión 1 tiene un botón Activar, y cada versión tiene un botón Revertir." />

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

## Compartir con clientes y grupos {#sharing-with-customers}

Un miembro cuyo acceso está limitado a algunos de tus clientes, o a grupos de dispositivos, ve un
panel solo cuando se comparte con uno de esos clientes o grupos y se publica. En la lista de paneles,
elige **Compartir** en un panel, elige los **Clientes** y los **Grupos de dispositivos** y elige
**Guardar uso compartido**. Las listas que guardas sustituyen a las anteriores, y unas listas vacías
no comparten el panel con nadie. Puedes compartir un panel antes de publicarlo; los miembros con los
que se comparte lo ven en cuanto se publica.

Solo se ofrecen los grupos usados como ámbito de acceso. Si falta un grupo, actívalo como ámbito de
acceso en la página del grupo, como se describe en
[Limitar un miembro a un grupo de dispositivos](/user-guide/members-roles-and-customer-users#group-scopes).
Un grupo que deja de ser un ámbito de acceso conserva las comparticiones que tenía, pero no se puede
volver a guardar hasta que lo sea de nuevo: al guardarlo se rechaza con un mensaje que dice que un
grupo elegido ya no es un ámbito de acceso.

{/* TODO(screenshot): dashboard-share, the Share drawer with customers and groups picked. */}

Vincula los widgets de un panel que compartes a dispositivos o a los clientes con los que lo
compartes. Un widget vinculado a un cliente, con **Relationship** establecido en `assigned` (la
relación que crea la pestaña **Assignment** de un dispositivo), muestra a cada usuario de cliente los
dispositivos de ese cliente, siempre que el cliente sea uno de los suyos; vinculado a cualquier otro
cliente, le muestra **No tiene acceso a estos datos**. Un widget vinculado a un activo, un área o un
grupo encuentra sus dispositivos a través de las relaciones, que un miembro con acceso limitado no
puede leer, así que en lugar de sus datos le muestra **Not available for customer users** (no
disponible para usuarios de cliente). En un panel compartido con un grupo, vincula los widgets a los
dispositivos del grupo.

Un programa hace lo mismo con la mutación `shareDashboard`, que requiere `dashboard:write`:

```graphql
mutation {
  shareDashboard(
    token: "plant-overview"
    anchors: [{ kind: CUSTOMER, token: "acme" }, { kind: GROUP, token: "north-wing" }]
  ) {
    token
    sharedWith { kind token }
  }
}
```

- La lista que envías sustituye a la anterior. Una lista vacía no comparte el panel con nadie.
- Todos los clientes deben existir, y todos los grupos deben existir y ser un ámbito de acceso. Si
  alguno no lo cumple, la solicitud falla con `NOT_FOUND` y nada cambia. Un grupo que existe pero no
  es un ámbito de acceso se responde exactamente igual que uno que no existe.
- Ese miembro solo ve en la lista los paneles **publicados** compartidos con sus clientes o grupos, y
  abre su versión publicada. Nunca ve un borrador ni el historial de versiones. Cualquier otro panel se
  responde como si no existiera.
- Compartir un panel no comparte datos. Cada widget sigue mostrando solo lo que el miembro puede ver.
- Un miembro con acceso limitado no puede compartir un panel ni ver con quién está compartido.
- Eliminar el panel elimina sus comparticiones. Eliminar un cliente o un grupo elimina las
  comparticiones que lo nombran.

## Relacionado

- [Paneles](../concepts/dashboards.md): widgets, fuentes de datos, versiones e incrustación
- [Mapas base](../concepts/basemaps.md), para los mosaicos del widget de mapa
