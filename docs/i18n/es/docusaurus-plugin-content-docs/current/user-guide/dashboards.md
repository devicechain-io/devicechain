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

Elige **Supervisión → Paneles** en la barra lateral.

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
  **Intervalo personalizado** muestra en su lugar un intervalo fijo de fecha a fecha; consulta
  [Intervalo personalizado](./viewing-dashboards-in-dash.md#custom-range).
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

### Elegir un dispositivo y compartir un enlace {#sharing-a-link}

Cuando un panel tiene un slot que siguen varios widgets (un **Target slot** en un mapa, una tabla de
flota o una tabla de alarmas, o un selector de entidad), elegir un dispositivo redirige los widgets
vinculados a ese slot. En un mapa, haz clic en un marcador, o llega a él con el tabulador y pulsa
Intro, para elegir ese dispositivo; define el **Target slot** del mapa en el editor para activarlo.
Sin él, un marcador es solo una marca.

La dirección del navegador siempre reproduce lo que estás viendo. Al elegir un dispositivo se le
añade `bind.<slot>=<token del dispositivo>`, por ejemplo:

```text
/dashboards/pump-station?bind.machine=hauler-7
```

Copia la dirección y envíala, y quien la abra verá el panel con ese dispositivo elegido. El `<slot>`
es el nombre del slot tal como aparece en el editor, y el valor es el token del dispositivo. Un
enlace puede fijar varios slots repitiendo el parámetro.

Un enlace se comprueba con los permisos de quien lo abre, nunca con los de quien lo envió. Si el
dispositivo no existe, o existe pero queda fuera de lo que puedes ver, la página lo indica en lugar
de mostrar el panel y ofrece **Abrir el panel sin él**, que abre el panel con su dispositivo
predeterminado. Ambos casos se leen igual a propósito: la plataforma no confirma que exista un
dispositivo que no puedes ver. Un enlace que nombra un slot que el panel no tiene, o algo que no es
un token de dispositivo, se rechaza del mismo modo. También se rechaza un enlace que nombra el mismo slot dos veces, y uno que nombra un slot cuyo
dispositivo sigue a otro slot, porque el panel mostraría el dispositivo que deduce y no el del enlace. Si no se pudo completar la
comprobación, por ejemplo porque se cortó la conexión, puedes **Reintentar**.

Elegir un dispositivo reemplaza la dirección en lugar de añadir una entrada al historial, así que
**Atrás** sale del panel en vez de recorrer cada dispositivo en el que hiciste clic. El mismo enlace
funciona en el visor /dash; consulta
[Enlazar a un panel con un dispositivo elegido](/user-guide/viewing-dashboards-in-dash#linking-to-a-device).

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
de tiempo** lo conserva, como *Average, last 5 min* arriba. Activa **Mostrar tendencia** y la tarjeta dibuja bajo la cifra la tendencia de la medición durante el mismo intervalo, de modo que «¿está subiendo?» se responde en la propia tarjeta. La línea es siempre del color neutro, sea cual sea la banda de la cifra, y se omite cuando la tarjeta es demasiado pequeña para mantener legible la cifra.

<GuideShot id="dashboard-bar-pie" alt="Dos gráficos de barras con las últimas lecturas de temperatura, humedad y batería de wl-sensor-01: Latest readings, con barras verticales en el orden de la fuente de datos, y Largest first, con barras horizontales de mayor a menor. Debajo, dos gráficos circulares de las mismas lecturas." />

Los gráficos de barras y circulares comparan las últimas lecturas de un dispositivo. Un gráfico de
barras puede, en cambio, comparar una medición entre los dispositivos de un grupo (consulta **Una
barra por** más abajo). Dibuja columnas en un widget ancho y filas en uno alto.

<GuideShot id="dashboard-fleet-table" alt="La tabla de flota Sensors in the zone de wl-zone-01: una fila por dispositivo (wl-sensor-01 y wl-sensor-03) con Temperature (C), ordenada por esa columna, Humidity (%) y Battery (%), cada valor con su antigüedad. Debajo, una tabla de flota más pequeña y un gráfico de barras Temperature by sensor." />

La tabla de flota muestra una fila por dispositivo de un grupo y una columna por medición. Haz clic en
el encabezado de una columna para ordenar por ella. Sus ajustes se describen en
[Configurar un widget](#configurar-un-widget).

Una tarjeta de recuento de flota pone una cifra sobre esa tabla: cuántos dispositivos de un grupo requieren atención (*3 de 21*) o, sencillamente, cuántos dispositivos tiene el grupo. Sus ajustes se describen en [Configurar un widget](#configurar-un-widget).

Dos widgets responden a la primera pregunta sobre una flota: si está conectada. Una tarjeta de estado del dispositivo muestra un dispositivo como *En línea* o *Sin conexión*, con la hora a la que se vio por última vez, y un anillo de conexión divide un grupo según cuántos de sus dispositivos están en línea, sin conexión o todavía sin estado. Leen el estado de los dispositivos que conoce la plataforma, no sus lecturas.

Una tabla de historial lista el historial almacenado de un dispositivo como filas: una fila por intervalo de tiempo dentro del intervalo de tiempo del tablero y una columna por medición. Cada cifra es el **promedio** de las lecturas de ese intervalo, no una lectura, y la tabla lo dice bajo las filas (*prom. por 5m*); la hora es el inicio del intervalo. Úsela para leer los números que hay detrás de un gráfico. Su configuración se describe en [Configurar un widget](#configurar-un-widget).

### Descargar los datos de un widget {#downloading-a-widgets-data}

Un widget con datos que entregar muestra un botón de descarga a la derecha de su barra de título. Ábralo para elegir **Descargar CSV** (la tabla de historial, el gráfico de series temporales y la tabla de flota) o **Descargar imagen (PNG)** (los gráficos dibujados en un lienzo: el gráfico de series temporales, el indicador y la brújula).

- El archivo contiene lo que el widget está mostrando, no una consulta nueva: el mismo intervalo de tiempo y el mismo intervalo de agrupación. El nombre del archivo lo dice. Una tabla de historial de la última hora, en intervalos de 5 minutos, se guarda como `Pump-3-history-pump-3-avg-per-5m-20261022T1300Z-20261022T1400Z.csv`: el título, el dispositivo, el intervalo y la ventana que cubre, en UTC.
- El archivo de la tabla de historial contiene todas las filas, no solo la página en pantalla. El del gráfico de series temporales tiene una fila por punto, con una columna **Base** que indica si un punto es un promedio de intervalo (*prom. por 1m*) o una lectura que llegó en vivo. El de la tabla de flota tiene una fila por dispositivo, en el orden en pantalla, con su última lectura y la hora en que se tomó; una celda sin lectura queda vacía.
- Las unidades van en los encabezados. Las horas son ISO 8601 en UTC, por ejemplo `2026-10-22T14:00:00.000Z`. Los valores son números, redondeados como los muestra el widget.
- El CSV es UTF-8 con marca de orden de bytes, de modo que Excel lee bien unidades como °C. Un programa que lea los bytes sin procesar ve esa marca al inicio del primer encabezado.
- El texto que una hoja de cálculo podría leer como fórmula, un nombre de dispositivo o de medición que empiece por `=`, `+`, `-` o `@`, se escribe con un apóstrofo delante, de modo que abrir el archivo nunca ejecuta nada.
- El botón pertenece a la barra de título. Un widget sin título ni línea de asunto, y un tablero que oculta los encabezados de los widgets, no tienen botón.

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
en relación con otro se mueve con él. Un widget sin nada que dibujar
dice por qué, en un estado atenuado y no con una tarjeta en blanco ni un guion suelto:

- **Sin dispositivo elegido**: el espacio del widget no tiene dispositivo vinculado. Elija uno en el selector del panel o vincule el espacio.
- **Este dispositivo no lo informa**: el perfil activo del dispositivo no declara la medición que pide el widget, así que nunca la informará. La solución está en el widget (elegir otra medición) o en el perfil, no en esperar.
- **Sin datos en este rango**: el perfil declara la medición, pero no llegó nada en el rango de tiempo mostrado. Si se conoce una lectura anterior al rango, el widget añade *Último valor 21,4 °C · hace 3 días*.
- **Sin acceso**: la plataforma rechazó la lectura porque su acceso se limita a una parte del inquilino.
- **Datos no disponibles**: el origen de datos no respondió. El mensaje nombra el fallo.

Estos estados son deliberadamente discretos. Usan el color de texto atenuado, sin símbolo de advertencia ni color de severidad, porque en un panel ese estilo significa una alarma. Un widget pequeño muestra solo el título; uno mayor añade la explicación y el último valor. Pase el cursor sobre el estado para leer la frase completa. Mientras el panel carga, un widget que aún no tiene lectura queda en blanco un momento antes de decir por qué, de modo que una lectura en camino nunca se anuncia como ausente.

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
| Telemetría | Gráfico de series temporales, Tarjeta de último valor, Indicador, Tabla, Indicador lineal, Indicador de nivel, Batería, Luz de estado, Barras de señal, Brújula, Anillo de progreso, Tarjeta de estadística, Gráfico de barras, Gráfico circular, Tabla de flota, Recuento de flota, Estado del dispositivo, Anillo de conexión, Tabla de historial |
| Estático | Etiqueta, Imagen |
| Alarmas | Tabla de alarmas, Contador de alarmas, Severidad de alarmas |
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
  un principal, explica que el widget lo sigue. Solo la tabla de flota, el recuento de flota, el anillo de conexión, un gráfico de barras con una
  barra por dispositivo y un gráfico de series temporales con una línea por dispositivo pueden leer un grupo de dispositivos; cualquier otro widget de telemetría
  muestra un solo dispositivo y, vinculado a un grupo, lo indica en lugar de mezclar las lecturas de
  varios dispositivos.
- **Medición** y opciones de presentación como unidades, rango y precisión.
- **Intervalo de tiempo** (gráfico de series temporales): cuánto historial dibuja el gráfico, en
  segundos, de 60 a 604800. Si lo dejas vacío, el gráfico sigue el intervalo de tiempo del panel,
  que los espectadores pueden cambiar. Si lo indicas, el gráfico conserva ese intervalo sea cual
  sea el que elija un espectador.
- **Mín.** y **Máx.** (gráfico de series temporales): los extremos del eje de valores. Si los dejas
  vacíos, el gráfico elige su propio rango, que empieza en cero aunque las lecturas nunca se acerquen.
  Fíjalos para encuadrar las lecturas que importan: un motor a 92 °C en un eje de 70 a 130 queda
  donde se ve su línea de sobrecalentamiento por encima, no como una línea plana pegada al borde
  superior. Un extremo nunca recorta: una lectura o un umbral más allá de él amplía el eje, así que
  nada de lo dibujado se sale del gráfico.
- **Marcar alarmas** y **Marcar comandos** (gráfico de series temporales): ambas desactivadas por
  defecto. **Marcar alarmas** dibuja las alarmas del dispositivo del gráfico sobre las lecturas: una
  banda tenue desde que se activó la alarma hasta que se despejó (hasta el borde derecho mientras
  sigue activa), con una franja fina en el borde superior de la banda y una línea en la
  activación, del color de su severidad. Una alarma sin severidad
  propia y la línea donde se despejó se dibujan en el color neutro, porque en un panel el color
  significa severidad y nada más. **Marcar comandos** dibuja una línea punteada neutra donde se
  envió cada comando al dispositivo, con el nombre del comando y, cuando se conoce, su resultado
  (correcto, fallido, sin respuesta a tiempo, etc.). Pasa el cursor o toca un marcador para ver los
  mismos detalles en texto. Debajo del gráfico, **Eventos en este intervalo** abre una lista de
  todos los marcadores, accesible con el teclado. Los marcadores siguen el intervalo de tiempo del
  panel y la pausa, y leen solo el dispositivo del propio gráfico, para un intervalo de cualquier
  antigüedad. Se dibujan como máximo 30 eventos, los más recientes; el gráfico indica exactamente
  cuántos más caen en el intervalo («N más sin mostrar»). Una alarma que se despejó y volvió a
  activarse se dibuja solo desde su última activación, porque la plataforma guarda un registro por
  alarma. **Alarmas a marcar** (gráfico de series temporales) elige qué alarmas se dibujan. *Solo las
  mediciones de este gráfico* es lo predeterminado en un gráfico nuevo: dibuja las alarmas sobre una
  medición que el gráfico representa, de modo que una alarma de presión de neumáticos no tiñe un
  gráfico de temperatura del motor. Una alarma sobre otra medición, o que la plataforma no puede
  vincular a una medición, no se dibuja; aparece bajo el gráfico como **Otras alarmas de este
  dispositivo**. *Todas las alarmas del dispositivo* las dibuja todas, como los gráficos guardados
  antes de existir esta opción. En una grabación reproducida, los
  marcadores salen de la grabación si contiene alarmas o comandos, y el gráfico indica que no están
  en la grabación para el canal que no los tiene.
- **Estadística** (tarjeta de estadística): qué cifra del intervalo mostrar: promedio (por defecto),
  mínimo, máximo, suma, número de lecturas o la última lectura. La tarjeta escribe bajo la cifra la
  estadística y el intervalo que abarca, por ejemplo «Máximo · 1h». El mismo ajuste **Intervalo de
  tiempo** fija la tarjeta a un intervalo propio. La tarjeta dice por qué no tiene cifra (la fuente
  de datos no puede calcular agregados, la lectura falló o no hubo lecturas en el intervalo) en lugar
  de mostrar un número que no pudo calcular. El número de lecturas de un intervalo sin ninguna es 0.
- **Mostrar tendencia** (tarjeta de estadística): desactivado por defecto. Activado, la tarjeta dibuja la tendencia de la medición durante su intervalo en una línea fina y neutra bajo la cifra, con un punto por cada cubo del gráfico del intervalo, para la estadística que muestra la tarjeta (la tarjeta del último valor dibuja los promedios de los cubos). Un hueco en las lecturas es un hueco en la línea, un solo punto es un punto, y un intervalo sin lecturas no dibuja nada. La cifra es el mismo número con o sin ella.
- **Orden** (gráfico de barras, gráfico circular): las mediciones en el orden en que las lista la
  fuente de datos, o de menor a mayor o de mayor a menor. Una medición que aún no ha informado va
  la última. El gráfico de barras dibuja columnas en un widget ancho y filas en uno alto.
- **Una barra por** (gráfico de barras): *Medición* (el valor predeterminado) dibuja una barra por cada medición de un dispositivo. *Dispositivo* dibuja una barra por cada dispositivo del grupo al que está vinculado el widget, para la **Medición** que elija, por lo que la fuente de datos debe ser un grupo de dispositivos. Un grupo demasiado grande se recorta a sus primeras barras en el orden elegido, y el gráfico indica «Mostrando N de M dispositivos».
- **Una línea por** (gráfico de series temporales): *Medición* (el valor predeterminado) dibuja una línea por cada medición de un dispositivo. *Dispositivo* dibuja una línea por cada dispositivo del grupo al que está vinculado el widget, para la **Medición** que elija, durante el intervalo de tiempo del panel (o el **Intervalo de tiempo** propio del gráfico), por lo que la fuente de datos debe ser un grupo de dispositivos. Las líneas conservan el orden del grupo y cada dispositivo mantiene su color al llegar lecturas. El historial almacenado se dibuja como el **promedio de cada intervalo de tiempo**, un punto por intervalo, y el gráfico lo indica (por ejemplo «prom. por 6m»); las lecturas que llegan en vivo después se dibujan tal cual. Las líneas de umbral son valores de referencia dibujados en el gráfico: un punto de historial es un promedio por intervalo, no una lectura, y el gráfico lo advierte junto a las líneas. En este modo no se dibujan marcadores de alarmas ni de comandos.
- **Máximo de líneas** (gráfico de series temporales, una línea por dispositivo): el máximo de dispositivos dibujados, 5 por defecto y como máximo 12; los colores del gráfico se repiten tras cinco. Cada línea cuesta una lectura de historial y la plataforma lee un dispositivo a la vez, así que manténgalo bajo. Un grupo mayor que el límite muestra sus primeros dispositivos y el gráfico indica «Mostrando N de M dispositivos», de modo que un grupo nunca se recorta en silencio.
- **Agrupar por** (tabla de alarmas): *Tipo de dispositivo* muestra una tira de pestañas sobre la tabla, una pestaña **Todas** y después una pestaña por cada tipo de dispositivo que tiene una alarma activa en el alcance del widget, con el nombre del tipo de dispositivo. La insignia de una pestaña cuenta sus alarmas **activas** y toma el color de la peor gravedad entre ellas; una pestaña sin alarmas activas no tiene insignia, y una pestaña cuya peor alarma es indeterminada se mantiene neutra. Los recuentos salen de una sola lectura para toda la tabla, y las filas de una pestaña solo se leen cuando la elige. En **Todas** la tabla añade una columna **Tipo de dispositivo**, que la pestaña de cada tipo omite. La pestaña elegida se mantiene al actualizarse los datos. La tira se desplaza horizontalmente en una pantalla estrecha; use las flechas izquierda y derecha, **Inicio** y **Fin** para moverse entre pestañas, y **Intro** o **Espacio** para elegir una. Los recuentos son exactos, tenga el alcance las alarmas que tenga. Un usuario limitado a algunos clientes solo ve pestañas para los dispositivos que puede ver, y una alarma de un dispositivo que ya no puede ver aparece solo en **Todas**. **Pestañas de tipos de dispositivo** (tabla de alarmas, con la agrupación *Tipo de dispositivo*) enumera los tokens de tipo de dispositivo que se mostrarán como pestañas, en ese orden, en lugar de todos los tipos con alguna alarma; un tipo que no figura aparece solo en **Todas**.
- **Severidad de alarmas**: qué tan grave es la situación, de un vistazo. Una barra apilada muestra las alarmas activas del widget divididas por severidad y, debajo, se enumera cada severidad, de la más grave a la menos grave, con su recuento: *Crítica*, *Mayor*, *Menor*, *Advertencia* e *Indeterminada*. Una severidad sin alarmas permanece en la lista, atenuada, de modo que la lista nunca cambia de forma cuando aparecen o desaparecen alarmas, y no tiene segmento en la barra. Una severidad con alarmas toma su color de severidad; *Indeterminada* se mantiene neutra y, sin ninguna alarma, la barra es una pista vacía. La lista es texto real («Crítica: 2»), de modo que un lector de pantalla lee los recuentos y el color nunca es la única señal; la barra en sí es decorativa. **Estado** (activas por defecto) y **Reconocida** acotan qué alarmas se cuentan. Sin fuente de datos, el widget cuenta todas las alarmas que puede ver, las origine lo que las origine (un dispositivo, un activo, un área, un cliente o un grupo), de modo que su total coincide con el de un contador de alarmas; vinculado a un dispositivo, un ancla o un grupo, cuenta las alarmas de los dispositivos que cubre. **Tipo de dispositivo** cuenta solo las alarmas de dispositivos de ese tipo, por su token, y el pie lo indica (*5 alarmas en dispositivos de tipo pump*). Los recuentos son exactos, haya las alarmas que haya, y a un usuario limitado a algunos clientes solo se le cuentan las alarmas que puede ver. Una fuente de datos que no puede contar alarmas por severidad lo indica en lugar de mostrar ceros. En una reproducción del tablero, el widget cuenta las alarmas grabadas; una grabación no incluye tipos de dispositivo, así que un widget configurado con uno indica que no se puede mostrar. El widget no filtra una tabla de alarmas vinculada al hacer clic en una severidad.
- **Tabla de flota**: una fila por dispositivo del grupo al que está vinculado el widget y una columna por cada medición que selecciona la fuente de datos. **Columnas** da un encabezado, unidad y precisión por medición; **Umbrales** colorean las celdas de la columna de una medición (la banda normal conserva el color de la tabla); **Obsoleto tras** marca como obsoleta una lectura más antigua que esos segundos, y sin él la tabla solo muestra la antigüedad de cada lectura. **Mostrar antigüedad con retraso** muestra la antigüedad de una lectura solo cuando es al menos esos segundos más antigua que la lectura más reciente de la tabla (o está obsoleta), de modo que una flota que informa cada pocos segundos mantiene una línea por fila y el dispositivo que dejó de informar destaca; el pie del widget sigue mostrando la antigüedad de la lectura más reciente. Haga clic en el encabezado de una columna, o púlselo con el teclado, para ordenar; **Ordenar por** y **Mayor primero** fijan el orden inicial, y los dispositivos sin valor en la columna ordenada quedan siempre al final. **Dispositivos por página** pagina una flota larga. **Ranura de destino** convierte el nombre del dispositivo en un enlace que selecciona ese dispositivo en los widgets vinculados a la ranura. En un tablero estrecho la tabla pasa a una tarjeta por dispositivo. Una celda sin número explica por qué: *sin datos en el rango*, *no informa esta medición* (el perfil publicado del dispositivo no declara esta medición) o *no se pudo leer*. La tabla lee los primeros 500 dispositivos de un grupo e indica «Mostrando 500 de N dispositivos» cuando el grupo es mayor. Sigue el rango de tiempo del tablero, y un tablero en pausa la detiene.

- **Recuento de flota**: una cifra para un grupo de dispositivos, vinculada como la tabla de flota. **Contar** elige qué cuenta. *Dispositivos que requieren atención* (por defecto) cuenta los dispositivos cuya última lectura de la **Medición** está en una banda anormal de los **Umbrales** de la tarjeta y la muestra como *3 de 21*, con el nombre de la banda cuando todos los dispositivos señalados están en la misma banda con etiqueta (una banda llamada *Low* se lee «Low») y, si no, «requieren atención». La cifra toma el color de la peor banda entre los señalados; sin ninguno señalado, o con una flota en su banda normal, conserva el color de texto habitual, de modo que una flota sana se ve tranquila. Un dispositivo sin lectura de la medición no está sano ni señalado: se cuenta aparte, como «2 sin informar» cuando no envió nada en el intervalo (también un sensor cuya sonda dejó de funcionar mientras sus otras lecturas siguen llegando) y como «1 sin leer» cuando sus lecturas no se pudieron cargar. Un dispositivo cuyo perfil publicado no declara la medición (una trituradora en un recuento de combustible) queda fuera de la pregunta: no entra en el *de 21* y se indica como «1 no informa esta medición». Eso se decide por el perfil, no por lo que el dispositivo ha enviado, y un dispositivo cuyo perfil no se puede leer, o no declara ninguna medición, se cuenta como sin informar en lugar de quedar fuera. Mientras las lecturas del grupo aún se cargan, la tarjeta no muestra ninguna cifra en lugar de una parcial. *Dispositivos* muestra cuántos dispositivos tiene el grupo, informen lo que informen. **Intervalo de tiempo** funciona como en la tabla de flota; **Obsoleto tras** se aplica a la lectura más reciente del grupo, como en las demás tarjetas. Un grupo mayor de los 500 dispositivos leídos indica «Mostrando 500 de N dispositivos», y el recuento de atención abarca los dispositivos mostrados.
- **Estado del dispositivo**: si un dispositivo está conectado. La tarjeta dice *En línea* o *Sin conexión* y, debajo, cuándo se vio el dispositivo por última vez (la hora de su evento más reciente). En línea conserva el color de texto habitual; solo *Sin conexión* toma un color, y la palabra lo indica, así que el color nunca es la única señal. Un dispositivo que nunca ha enviado nada no tiene estado todavía y se muestra como *Sin estado todavía* y *Nunca visto* en un color atenuado, no como una caída. El pie del widget indica la antigüedad del estado, y **Obsoleto tras** marca la tarjeta como obsoleta cuando el estado lleva más de esos segundos sin leerse, lo que solo ocurre si la lectura sigue fallando; un dispositivo que simplemente está callado no queda obsoleto, porque si está en línea es el propio estado. Sin él se muestra la antigüedad y nada se marca como obsoleto. Vinculada a un grupo, lo indica en lugar de mostrar un dispositivo de varios. El estado se lee cada 15 segundos y se queda quieto mientras el tablero está en pausa. Mostrarlo requiere permiso para leer el estado de los dispositivos; sin él, la tarjeta dice que no tiene acceso a estos datos.
- **Anillo de conexión**: cuántos dispositivos de un grupo están conectados. Un anillo divide el grupo en *En línea*, *Sin conexión* y, solo cuando algún dispositivo nunca ha enviado nada, *Sin estado todavía* (un tramo pálido), con el número de dispositivos en línea en el centro (*18 de 21 en línea*) y cada cifra listada bajo el anillo. El tramo en línea es un gris discreto; solo el tramo sin conexión toma un color, de modo que un grupo sin nada desconectado no dibuja ningún color, y un cero se lista igualmente. Mientras los estados del grupo se cargan, el widget no muestra anillo en lugar de uno parcial, y un dispositivo cuyo estado no se pudo leer se anota aparte («1 sin leer») y se deja fuera del anillo. Lee los primeros 500 dispositivos de un grupo e indica «Mostrando 500 de N dispositivos» cuando el grupo es mayor. **Obsoleto tras** marca el anillo como obsoleto cuando el estado del grupo lleva más de esos segundos sin leerse.
- **Tabla de historial**: el historial almacenado de un dispositivo como tabla. Tiene una fila por intervalo de tiempo, las más recientes primero, y una columna por medición, cada celda con el promedio de las lecturas de esa medición en el intervalo. Sigue el intervalo de tiempo del tablero, o su propio **Intervalo de tiempo** si lo establece, y el intervalo de agrupación es el ancho que usaría el gráfico de series temporales para ese lapso (1 minuto para una hora, 6 minutos para un día), indicado bajo las filas como *prom. por 6m*. La hora es el inicio del intervalo, y una medición sin lecturas en un intervalo muestra un guion, no el último valor. No hay cola en vivo: la tabla vuelve a leer cuando termina cada intervalo, y la fila más reciente es un intervalo que aún se está llenando. **Columnas** elige qué mediciones listar, en orden, con encabezado, unidad y precisión para cada una; sin ellas lista las mediciones de la fuente de datos, o todas las que el dispositivo informó en el intervalo. **Más antiguas primero** invierte el orden y **Filas por página** fija la longitud de la página (de 5 a 100, 10 por defecto). **Obsoleto tras** marca la tabla como obsoleta cuando su historial lleva más de esos segundos sin leerse, lo que solo ocurre si la lectura sigue fallando; la tabla sigue mostrando lo último que leyó. Vinculada a un grupo, lo indica, porque una tabla del historial de un dispositivo mezclaría las lecturas del grupo. Quien no tenga permiso para leer el historial ve una nota de que no tiene acceso a estos datos. Su descarga se describe en [Descargar los datos de un widget](#downloading-a-widgets-data).

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
