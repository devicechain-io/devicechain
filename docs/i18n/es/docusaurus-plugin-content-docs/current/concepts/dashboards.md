---
title: Paneles
---

# Paneles

DeviceChain incluye un sistema de paneles (dashboards) embebible y con control de versiones para visualizar datos de dispositivos en vivo. Un panel es un **recurso con alcance de inquilino** que creas en la consola. Se renderiza a partir de una definición JSON portable, y esa misma definición se renderiza en la consola, en el visor de referencia independiente o en cualquier aplicación React que instale los paquetes de runtime desde npm.

:::note Estado
Disponible: el editor de lienzo (canvas), con deshacer y rehacer, duplicar, y copiar y pegar entre paneles; el conjunto de widgets integrado (widgets de telemetría, alarma y comando/control), un intervalo de tiempo del panel con pausa, las suscripciones en vivo que se recuperan tras una interrupción, las acciones de widget (reconocer/limpiar alarma, enviar comando — autorizadas por el servidor), el versionado (publicar / revertir), la vista previa sintética, los slots con nombre y los manifiestos de vinculación (binding manifests), la exportación, la importación, la clonación y las plantillas iniciales, el visor de referencia independiente `/dash` y los paquetes de runtime publicados en npm.

Planeado: selectores de fuente de datos más completos (recorrido del grafo de relaciones, drill-down), edición del diseño por punto de quiebre (breakpoint) y widgets adicionales.
:::

## El lienzo

Un panel distribuye los widgets en una cuadrícula de **celdas cuadradas**. La cuadrícula ofrece:

- colocación por celdas: cada widget ocupa un bloque de celdas completas, columnas por filas, y cada tipo de widget puede fijar el bloque más pequeño y el más grande que ocupa;
- ningún solapamiento: dos widgets de la cuadrícula nunca comparten una celda, y al mover o redimensionar uno en el editor los demás se desplazan hacia abajo para dejarle sitio;
- widgets colocados en **relación** con otro, anclados a un punto de este y que se mueven con él, como una insignia sobre un mapa o un pie bajo un gráfico. Son los únicos widgets que quedan encima de otros;
- una imagen o un color de fondo opcional.

Un panel que no define su propia cuadrícula obtiene 12 columnas, un tamaño mínimo de celda de 56 px y un espaciado (gutter) de 8 px.

El panel mide el contenedor en el que lo montas (un panel lateral, un marco de ancho fijo o una página completa), no la pantalla. Mientras las columnas diseñadas caben con el tamaño mínimo de celda del lienzo, el diseño se dibuja tal como se creó y las celdas se estiran para llenar el ancho, de modo que una pantalla mural obtiene celdas más grandes y no más celdas. En un contenedor más estrecho, el panel usa tantas columnas como quepan y coloca los widgets en ellas en orden de lectura, cada uno dentro de los tamaños que admite. Por debajo de 600 px de ancho, como en un teléfono, cada widget ocupa su propia fila, de modo que el panel se lee como una sola columna. Un control de dimensionamiento en el momento del montaje (`fill`, ancho fijo o alto fijo) permite que el host elija.

Un panel también tiene una **densidad**: `compact`, `comfortable` (la predeterminada) o `wall`. Escala todos los tamaños de texto y de espaciado, los espaciados entre celdas y la celda más pequeña a la que puede encogerse una columna (por 0,9, 1 y 1,5), de modo que `wall` dibuja un panel pensado para leerse desde el otro lado de una sala. El autor la fija en el editor y un espectador puede anularla (consulta el [parámetro `density`](#session-refresh-and-kiosk-mode) de `/dash`).

El formato de definición y el renderizador incluyen una colocación por punto de quiebre (breakpoint) para cada widget, elegida según el ancho del contenedor del panel, y un diseño de punto de quiebre definido así se dibuja en lugar de la reorganización automática. Sin embargo, el editor de lienzo solo escribe el punto de quiebre base, así que hoy un panel creado en la consola tiene un solo diseño, que se reorganiza automáticamente en contenedores más estrechos.

## Widgets

Los widgets integrados abarcan seis canales: telemetría, estático (sin datos), alarma, control, selección y ubicación. El gráfico de series temporales, el medidor y la brújula se renderizan sobre [Apache ECharts](https://echarts.apache.org/), y el mapa sobre [MapLibre GL](https://maplibre.org/). El resto son DOM simple.

Los widgets de nivel e indicadores (indicador lineal, indicador de nivel, batería, luz de estado, barras de señal) nunca suponen una escala: un indicador sin mín. ni máx., o sin datos, lo dice en lugar de dibujar un relleno verosímil. Un valor fuera de la escala fija el relleno en el extremo y marca la lectura con ▲ o ▼. Solo colorean por banda de umbral cuando el valor está en una banda anómala; una banda `normal` conserva el color neutro del widget. La brújula y el anillo de progreso siguen las mismas reglas: un rumbo fuera de 0–360 se pliega sobre el dial (370° se lee 10°), y un anillo de progreso sin mín. ni máx. lo dice. El medidor, la brújula y el anillo de progreso muestran su lectura como texto de la página, no como parte del dibujo del gráfico.

La tarjeta de estadística muestra una cifra de una medición en un intervalo de tiempo: su promedio, mínimo, máximo, suma, el número de lecturas o su última lectura. La plataforma calcula la cifra con todas las lecturas del intervalo, no con los promedios que dibuja un gráfico, de modo que un máximo es la lectura más alta y no el promedio más alto. La tarjeta sigue el intervalo de tiempo del panel salvo que le des uno propio, indica junto a la cifra qué intervalo abarca («Máximo · 1h») y deja de actualizarse cuando el panel está en pausa. El intervalo empieza y termina en un minuto entero, así que la cifra nunca incluye lecturas anteriores al intervalo. Una tarjeta en vivo vuelve a leer una vez por minuto en un intervalo de una hora, con menos frecuencia en intervalos más largos (cada 42 minutos en una semana) y no mientras la pestaña del navegador está oculta. Una tarjeta configurada con la última lectura muestra la antigüedad de esa lectura. Se colorea por franja de umbral como los widgets de nivel. Cuando no se puede calcular la cifra (la fuente de datos no puede, la lectura falló o el intervalo no tiene lecturas), la tarjeta lo dice y no muestra ningún número; el número de lecturas es la excepción, y un intervalo sin ninguna muestra 0. El gráfico de barras y el gráfico circular dibujan la última lectura de cada medición seleccionada, una barra o un sector por medición; un gráfico circular son proporciones de un total, así que rechaza los valores negativos y los ceros en lugar de dibujar otra cosa. Ambos se dibujan como texto y formas de la página, por lo que sus etiquetas son texto legible y no parte de un lienzo. El valor de una barra se escribe junto a ella cuando cabe, la leyenda del gráfico circular da cada valor y su proporción, y todos los valores están también en la descripción del gráfico para lectores de pantalla.

El control deslizante y el interruptor de comando muestran lo último que se envió al dispositivo, con el estado de entrega de ese comando (pendiente, entregado o fallido), nunca el estado actual del dispositivo. El control deslizante envía una sola vez al soltarlo, o con Intro desde el teclado, no mientras se arrastra; un valor que no se le pidió enviar (Escape, o un arrastre abandonado) se descarta. El interruptor envía lo contrario del último valor enviado solo mientras ese comando está pendiente o entregado. Si no se ha enviado nada, si el último comando falló o si no figura entre los comandos recientes, el interruptor no adivina: ofrece Encendido y Apagado y envía el que usted elija. Ninguno de los dos vuelve a enviar mientras su comando anterior aún se está aceptando. Si el comando tiene otro parámetro obligatorio sin valor por defecto, o el parámetro elegido no existe o es de otro tipo, el control lo dice y no envía nada. Un visor sin `command:write` los ve en solo lectura. El control deslizante también tiene un modo **paso a paso** (`mode` en stepper): − y + cambian el valor según el paso declarado del parámetro, dentro de su mínimo y máximo declarados, y no se envía nada hasta pulsar Enviar (o Intro). Escape descarta el valor elegido.

| Widget | Canal | Muestra |
|---|---|---|
| **Gráfico de series temporales** | telemetría | una o más series de mediciones a lo largo del intervalo de tiempo del panel, o de un intervalo propio; opcionalmente marcado con las alarmas del dispositivo (una banda desde la activación hasta el despeje, con el color de su severidad) y los comandos que se le enviaron |
| **Medidor (gauge)** | telemetría | un único valor más reciente frente a un rango / umbrales |
| **Tarjeta de último valor** | telemetría | una sola lectura actual con su marca de tiempo |
| **Tabla** | telemetría | la última lectura de cada medición de un dispositivo |
| **Indicador lineal** | telemetría | un único valor más reciente como barra (horizontal o vertical) o termómetro, sobre una escala mín./máx. obligatoria |
| **Indicador de nivel** | telemetría | un depósito lleno hasta el valor más reciente entre un mín. y un máx. obligatorios, leído como valor o como porcentaje |
| **Batería** | telemetría | un icono de batería lleno hasta el valor más reciente, sobre una escala de 0 a 100 % salvo que se fijen mín. y máx. |
| **Luz de estado** | telemetría | una lámpara coloreada por la banda de umbral del valor, o encendida y apagada en un valor de encendido, con un texto |
| **Barras de señal** | telemetría | una barra por umbral, encendida por cada umbral que el valor más reciente ha alcanzado |
| **Brújula** | telemetría | un rumbo (dirección del viento, demora) como aguja sobre una vuelta completa, con los grados y el más cercano de los ocho puntos cardinales |
| **Anillo de progreso** | telemetría | un anillo lleno entre un mín. y un máx. obligatorios, leído como porcentaje o como valor |
| **Tarjeta de estadística** | telemetría | el promedio, mínimo, máximo, suma, número de lecturas o última lectura de una medición en el intervalo de tiempo del panel (o en uno propio) |
| **Gráfico de barras** | telemetría | la última lectura de cada medición seleccionada como una barra, en el orden de la fuente de datos o por valor; columnas en un widget ancho, filas en uno alto |
| **Gráfico circular** | telemetría | la proporción de cada medición seleccionada sobre el total de sus últimas lecturas, con una leyenda de nombres, valores y porcentajes |
| **Tabla de flota** | telemetría | una fila por dispositivo de un grupo y una columna por medición; cada celda muestra la última lectura de ese dispositivo con su antigüedad, coloreada según los umbrales de la columna; se puede ordenar y paginar, y el nombre del dispositivo lo selecciona en los widgets enlazados. Un gráfico de barras configurado como *una barra por dispositivo* dibuja las mismas lecturas por dispositivo como barras |
| **Etiqueta** | estático | texto estático |
| **Imagen** | estático | una imagen estática (p. ej., un plano de planta detrás de otros widgets) |
| **Tabla de alarmas** | alarma | alarmas en vivo de un dispositivo o ancla |
| **Conteo de alarmas** | alarma | un recuento agregado de alarmas abiertas |
| **Severidad de alarmas** | alarma | las alarmas activas desglosadas por severidad: una barra apilada sobre una leyenda con el recuento de cada severidad, para todas las alarmas del alcance o para los dispositivos de un tipo |
| **Comando / control** | control | un formulario de parámetros tipado que despacha un comando y muestra en vivo su ciclo de vida de entrega |
| **Control deslizante de comando** | control | un control deslizante para un parámetro numérico de un comando, que se envía al soltarlo; su rango proviene de la definición del comando |
| **Interruptor de comando** | control | un interruptor para un parámetro booleano de un comando, un comando por clic |
| **Selector de entidad** | selección | un selector que reapunta un slot con nombre, de modo que quien lo ve elige qué entidad muestra el panel — o un widget dentro de él |
| **Mapa** | ubicación | la última posición conocida de los dispositivos vinculados |

La mayoría de los widgets de telemetría admiten **umbrales**, una **unidad** y una **precisión**. Un umbral nombra una severidad (`normal`, `warning`, `minor`, `major` o `critical`), nunca un color, de modo que el tema decide cómo se ve cada banda en modo claro y oscuro; solo las bandas anómalas tienen color. Sin precisión, un valor se redondea a 3 decimales, o a 4 dígitos significativos por debajo de 1.

Un widget que muestra lecturas indica la antigüedad de su valor (por ejemplo, "hace 12 s"). Si se rellena su ajuste **Obsoleto tras (segundos)**, un valor más antiguo que eso se atenúa y se marca como obsoleto; no se supone ningún valor predeterminado, porque cualquier número fijo marcaría como obsoletos dispositivos sanos que informan con poca frecuencia. Un widget sin nada que mostrar explica por qué (ningún dispositivo elegido, ningún dato en el intervalo, un grupo que no puede mostrar) en lugar de dibujar un marco vacío.

Los widgets se tematizan con propiedades personalizadas de CSS, de modo que una aplicación embebedora controla su apariencia sin modificar el código de los widgets.

## Intervalo de tiempo y pausa {#time-range-and-pause}

Un panel tiene un **intervalo de tiempo**: el lapso, hacia atrás desde ahora, que dibujan sus gráficos y sobre el que calculan sus tarjetas de estadística. El espectador elige 15 minutos, 1 hora, 6 horas, 24 horas o 7 días en la cabecera del panel. El autor puede guardar otro valor predeterminado en el editor; un panel que no guarda ninguno usa una hora. Un widget puede fijar un lapso propio, de un minuto a una semana, que la elección del espectador no cambia; un gráfico de series temporales fijado en cinco minutos sigue siendo una franja de cinco minutos.

**Pausa** congela lo que el panel muestra en ese instante: los gráficos, las tarjetas, los medidores, las tablas, los widgets de alarma y el mapa conservan los valores en pantalla, y al reanudar vuelven los valores en vivo. Una pausa solo congela la visualización. Los comandos, y el reconocimiento y la limpieza de alarmas, se siguen enviando al momento y su estado se sigue actualizando, como indica el aviso de pausa ("los comandos se siguen enviando").

Un gráfico lee su historial en intervalos (buckets) que limitan una lectura a 240 puntos como máximo: un minuto para 15 minutos y una hora, 2 minutos para 6 horas, 6 minutos para 24 horas y 42 minutos para 7 días. Un gráfico conserva como máximo 2.000 muestras en vivo; cuando un lapso largo a una frecuencia alta lo supera, las más antiguas se descartan primero y el gráfico indica desde cuándo cubre.

### Teselas del mapa y acceso a la ubicación

:::note Las teselas del mapa vienen del inquilino, no del widget
Qué proveedor de teselas usar es una decisión a nivel de inquilino, no de cada widget. Consulta [Mapas base](./basemaps.md).
:::

El widget de mapa renderiza las posiciones de los dispositivos sobre el **mapa base** del inquilino. Una instancia nueva ya viene con uno configurado, así que un widget de mapa dibuja teselas sin que nadie tenga que prepararlo. Las opciones `tileUrl` y `attribution` del propio widget anulan el mapa base para ese panel concreto, lo que resulta útil para probar un proveedor antes de adoptarlo en todo el inquilino.

Si ningún nivel tiene una fuente de teselas —un operador definió el valor por defecto de la instancia como `{}` y el inquilino no definió nada—, el widget sigue dibujando un mapa real, sobre un mapa base del mundo incorporado con continentes y fronteras de dominio público. Un panel plano con posiciones relativas queda solo para el caso en que el propio motor de mapas no pueda cargarse ni iniciarse, por ejemplo detrás de un proxy que lo bloquea o en un navegador sin WebGL utilizable.

Leer posiciones también requiere la autoridad `location:read`. **No** la otorga la base de solo lectura que recibe cada miembro; consulta [ubicación de dispositivos](../guides/connecting-a-device.md). A quien no la tenga se le indica, en lugar de mostrarle un mapa vacío.

### Acciones y permisos

Los widgets pueden portar **acciones**: reconocer o limpiar una alarma, o enviar un comando. El servidor autoriza cada acción contra los propios derechos con alcance de inquilino de quien la invoca. Por ejemplo, una acción que requiere `alarm:write` queda inerte para un visor de solo lectura.

Abrir un panel requiere `dashboard:read`, que posee todo miembro habilitado de un inquilino. Forma parte de la base de solo lectura, junto con leer dispositivos, eventos, estado, comandos y alarmas. Un panel es una disposición guardada de datos que esas autoridades ya alcanzan, así que un miembro que puede leer los datos puede abrir su vista.

`dashboard:write` controla crear, actualizar, publicar, revertir y eliminar, y sigue concediéndose por rol.

## Fuentes de datos

Un widget no embebe una consulta. Embebe un **selector** tipado que el runtime resuelve:

- **`device`** — un solo dispositivo, por token.
- **`anchor`** — telemetría con alcance a una entidad organizacional (un cliente, un área o un activo), designada mediante una relación rastreada. El runtime la expande del lado del cliente a los dispositivos relacionados en ese momento con esa entidad y transmite las muestras sin procesar de cada miembro: un flujo por dispositivo, hasta 500 miembros. Solo la tabla de flota y un gráfico de barras configurado con una barra por dispositivo leen un ancla, porque mantienen separadas las lecturas de cada dispositivo. Cualquier otro widget de telemetría muestra un solo dispositivo y, vinculado a un ancla, lo indica en lugar de mezclar las lecturas de sus dispositivos bajo un mismo nombre de medición. Agregar los eventos del ancla en una sola serie del lado del servidor está reservado. El campo `aggregation` de un selector se almacena y se conserva en el ida y vuelta, pero todavía no se lee.
- **`slot`** — un marcador con nombre que el host resuelve en el momento del montaje a partir de su manifiesto de vinculación (consulta [Incrustación](#embedding-definitions-slots-and-binding-manifests) más abajo). Esto es lo que la consola escribe hoy: al cargar un panel, reescribe los selectores concretos `device` y `anchor` como slots, de modo que un panel creado es por defecto una plantilla reutilizable.

Otros dos tipos, `devices` y `relatedTraversal`, están reservados para que una definición almacenada siga siendo compatible hacia adelante. El runtime los rechaza hasta que se implementen.

Los selectores se resuelven a través del SDK de cliente contra la API GraphQL. Eso hace que la resolución sea:

- **actual** — un dispositivo recién asignado a un área aparece en el panel de esa área sin editarlo, la próxima vez que se abre el panel (un panel ya abierto conserva los miembros que resolvió hasta que se recarga);
- **verificada por permisos** — usa el propio acceso autenticado a la API, con alcance de inquilino, de quien lo solicita.

Cómo llegan los valores en vivo depende del canal:

| Canal | Cómo llegan los valores |
|---|---|
| telemetría | una suscripción GraphQL, multiplexada para que un panel con muchos widgets abra un flujo por dispositivo en lugar de uno por widget |
| alarma | relee una consulta, disparada por un flujo de alarmas en vivo y respaldada por un sondeo cada 30 segundos |
| control | solo por sondeo, porque command-delivery no expone ninguna suscripción |

Los widgets de alarma y de control mantienen cada uno su propio flujo y temporizador. Solo el canal de telemetría está multiplexado.

La conexión de una suscripción se cierra cuando caduca el token de acceso con el que se abrió (consulta
[Suscripciones por WebSocket](../reference/graphql-api.md#subscriptions-over-websocket)). El visor
independiente `/dash` renueva su token de acceso poco antes de que caduque, así que un panel que se deja en una pantalla sigue recibiendo datos en vivo. Si la renovación es rechazada, el visor lo indica (consulta [Renovación de sesión](#session-refresh-and-kiosk-mode)) en lugar de quedarse congelado.

### Estado de la conexión y recuperación {#connection-state-and-recovery}

La cabecera del panel muestra si sus datos en vivo están llegando: **en vivo**, **reconectando**
mientras se reintenta una conexión caída, y **sin conexión** cuando lleva 20 segundos caída o el
propio navegador se ha quedado sin conexión. Un dispositivo silencioso con una conexión sana sigue
mostrándose en vivo; la antigüedad de un valor la indica el indicador de frescura, no esta insignia.

Los widgets en vivo se recuperan de una interrupción de cualquier duración sin recargar la página.
Una conexión caída se reintenta mientras el panel esté abierto, y un flujo que el servidor termina
se vuelve a abrir tras una espera que se duplica hasta 30 segundos. Un widget cuyo código no se pudo
descargar muestra el fallo en su propio marco, y **Reset widget** lo vuelve a descargar.

#### Tras una reconexión {#after-a-reconnect}

Las lecturas enviadas mientras la conexión de un panel está caída nunca llegan por el flujo en
vivo, así que cuando un flujo se recupera (volvió la red o el servidor, o el servidor terminó el
flujo y el panel lo abrió de nuevo) cada widget las lee del historial almacenado:

- un **gráfico de líneas** vuelve a leer su historial y lo usa en lugar de lo que dibujó antes de
  la interrupción. Hasta que esa lectura llega, o si falla, el gráfico corta su línea durante la
  interrupción en lugar de trazar una línea recta desde la última lectura anterior hasta la primera
  posterior. Una lectura fallida se reintenta cada 30 segundos. En un panel en pausa la lectura
  espera hasta que lo reanudes. Un gráfico que no está vinculado a un único dispositivo no tiene
  historial almacenado que leer, así que su corte se mantiene;
- una **tabla de flota** o un **gráfico de barras con una barra por dispositivo** vuelve a leer los
  últimos valores de cada dispositivo, de inmediato y otra vez justo después del siguiente minuto
  completo, para incluir una lectura enviada en el último minuto de la interrupción. Un dispositivo
  cuya lectura falla conserva el valor que mostraba;
- una **tarjeta de estadística** vuelve a leer su cifra de inmediato en lugar de esperar a su
  siguiente actualización programada.

## Creación, versionado y vista previa {#authoring-versioning-and-preview}

Creas los paneles en la **consola**:

- **Editor de lienzo** — arrastrar y redimensionar, con selectores reales de dispositivo / ancla. Los widgets se ajustan a las celdas, y mover o redimensionar uno desplaza a sus vecinos hacia abajo. Un widget con el foco se mueve una celda con las teclas de flecha; Mayús y las teclas de flecha redimensionan un widget de cuadrícula una celda y desplazan un widget relativo un cuarto de celda. La **Colocación** de un widget es **Cuadrícula** o **Relativo a…** otro widget, anclado a uno de sus nueve puntos. El editor también fija la densidad del panel y su intervalo de tiempo predeterminado.
- **Comprobaciones de diseño** — cada widget muestra sobre sí mismo un problema de diseño: un tamaño fuera de lo que admite el widget, un widget que sobrepasa el borde derecho de la cuadrícula, un widget relativo que queda fuera del lienzo, un widget que no declara límites de tamaño o un título que repite su línea de asunto. Los tres primeros son errores y bloquean la publicación; **Ajustar a los límites** ajusta cada widget a los tamaños que admite y devuelve cada widget al lienzo. Los demás son consejos.
- **Deshacer, rehacer, duplicar, copiar y pegar** — deshacer con Ctrl+Z (Cmd+Z en un Mac) y rehacer con Ctrl+Mayús+Z o Ctrl+Y (Cmd+Mayús+Z), hasta 100 pasos; escribir en un mismo campo cuenta como un paso. El historial sobrevive a un guardado y a una publicación, así que puedes deshacer una edición que acabas de guardar, y una reversión lo borra. Ctrl+D (Cmd+D) duplica el widget seleccionado. Copiar y pegar usan el copiar y pegar del propio navegador, y un botón **Pegar** pega el último widget copiado en la pestaña, también en otro panel. Un widget pegado o duplicado se coloca en el primer espacio libre con su propio tamaño, y un widget relativo se pega en la cuadrícula. Conserva su fuente de datos solo cuando el panel de destino tiene el mismo slot vinculado de la misma forma; si no, se pega sin ella y el editor explica por qué. Un widget copiado en otro inquilino nunca lleva consigo un dispositivo ni una entidad. Ninguno de estos atajos actúa mientras escribes en un campo o mientras hay un diálogo o un menú abierto.
- **Versionado** — la definición de trabajo es un **borrador** mutable. **Publicar** la captura como una versión inmutable y hace que esa versión esté **activa**: es la que ven los espectadores. Puedes volver a activar una versión anterior sin tocar el borrador (**activar**), o **revertir** a una versión anterior, lo que sobrescribe el borrador con ella. El historial es una lista de instantáneas publicadas, no un diff.
- **Vista previa sintética** — sustituye los datos en vivo por un generador del lado del cliente (seno / rampa / paseo aleatorio) para validar el diseño, las escalas y los umbrales antes de que ningún dispositivo haya reportado.
- **Exportación** — descarga o copia una definición para compartirla o embeberla en otro lugar.
- **Importar, clonar y plantillas iniciales** — crea un panel nuevo a partir de una definición exportada (pegada o subida; se importa cualquier panel que el servidor pueda guardar, ya que el límite de 1 MiB se aplica a la definición sin su sangría), de un panel existente (**Clonar**, en la lista y en el menú de exportación del editor) o de una de las plantillas iniciales que ofrece **Nuevo panel** (un resumen de la flota, el detalle de un dispositivo y un panel de alarmas). Las tres pasan por la misma comprobación que aplica el editor al abrir un panel guardado, y siempre crean un panel **nuevo** con el token que elijas: un panel existente nunca se sobrescribe. El resultado es un **borrador**; publicar sigue siendo un paso explícito.
  - Un widget que esta consola no puede resolver (por ejemplo, de un paquete que no está instalado aquí) se conserva como marcador con su definición guardada intacta. La importación lo lista, y la publicación sigue bloqueada mientras quede algún marcador.
  - Un dispositivo, cliente, área o activo al que la definición está vinculada pero que este tenant no tiene se muestra antes de guardar nada. Para cada uno puedes elegir un sustituto de este tenant o dejar el slot sin vincular y vincularlo después. Los vínculos que sí existen se conservan tal cual. Una plantilla, o una definición exportada con **Copiar como plantilla**, no lleva vínculos, así que te pide elegir un dispositivo para cada slot de dispositivo.
  - Cada sitio externo del que la definición carga contenido (una imagen, teselas de mapa, un fondo del lienzo) se lista antes de guardar nada, porque el navegador contacta con esos sitios cada vez que se abre el panel.
  - Las plantillas iniciales usan nombres de medición de ejemplo como `temperature` y `humidity`; cámbialos en el editor por los que reportan tus dispositivos.

El borrador es para los autores. Leer la definición de un borrador, o la de una versión publicada, requiere `dashboard:write`. Un miembro que solo tiene `dashboard:read` ve el nombre y la descripción del panel y qué versión está activa, y recibe la definición de la versión activa mediante la consulta `publishedDashboard`. Un panel que nunca se ha publicado no sirve nada a ese miembro, y la respuesta lo dice en lugar de devolver un panel en blanco.

Publicar o activar una versión no cambia el `updatedAt` del borrador, de modo que guardar el borrador con la referencia que ya tienes sigue funcionando. La lista de versiones solo lleva el número de cada versión, su etiqueta y descripción opcionales, y quién la publicó y cuándo.

Un panel registra el formato con el que se guardó. Un visor o una consola más antiguos que ese formato se niegan a abrir el panel e indican que hay que actualizar, en lugar de dibujarlo mal; una aplicación que llama a `parseDashboardDefinition` recibe un `DashboardDefinitionError` cuyo `code` es `'schemaTooNew'` (compruebe `code` en lugar de `instanceof`, que falla cuando una aplicación empaqueta dos copias del paquete). Los paneles guardados en un formato anterior siguen abriéndose y se convierten al cargarse, de forma aproximada en cuanto al diseño. En el editor, un panel convertido muestra un aviso que pide revisar el diseño y guardar; al guardar se escribe el formato nuevo. Un gráfico de series temporales que conservaba un número de muestras de una versión anterior ahora sigue el intervalo de tiempo del panel, y el aviso lo nombra para que puedas fijar un lapso donde deba ser distinto.

## Incrustación: definiciones, slots y manifiestos de vinculación {#embedding-definitions-slots-and-binding-manifests}

Una definición de panel es portable y reutilizable como plantilla. En lugar de codificar de forma fija qué dispositivo lee cada widget, los widgets se vinculan a **slots con nombre**. En el momento del montaje, un host suministra un **manifiesto de vinculación** que asigna cada slot a un dispositivo o ancla concretos. Así, una definición más dos manifiestos dan dos paneles en vivo para dos dispositivos distintos, sin ningún cambio en la propia definición.

El runtime está estructurado como paquetes en capas:

| Paquete | Rol |
|---|---|
| `@devicechain/client` | el SDK de TypeScript — autenticación, operaciones GraphQL, suscripciones en vivo |
| `@devicechain/widgets` | los componentes de widget en React (entra la fuente de datos, salen los píxeles) y el renderizador que los distribuye |
| `@devicechain/dashboards` | el `DashboardHub` (posee la conexión, resuelve selectores, multiplexa las suscripciones de telemetría) y los tipos de definición, selector, slot y manifiesto de vinculación |

Una aplicación React embebe un panel en vivo construyendo un único hub con un resolvedor, y renderizando después la definición con un manifiesto de vinculación. El renderizador resuelve cada slot a través del manifiesto, así que reapuntar un slot, por ejemplo cuando un espectador profundiza en una alarma, vuelve a suscribir solo los widgets vinculados a ese slot; el resto del panel conserva sus valores en vivo. La consola y la aplicación independiente `/dash` siguen ambas este camino dentro de este repositorio, compilando contra los mismos artefactos que descarga un consumidor externo. Los paquetes están publicados en npm, así que una aplicación externa los instala de la misma manera. Consulta [Paquetes de npm](../reference/npm-packages.md) para la línea de instalación, la política de versiones y dist-tags, y la única pieza de cableado del host que necesita el widget de mapa.

### El visor de referencia `/dash`

La aplicación independiente **`/dash`** es el embebedor externo de referencia. Tiene su propio inicio de sesión. Indícale el token de un panel y obtiene la versión de ese panel que está activa en ese momento; o pega una definición exportada, que no necesita acceso al servicio y es la vía de un embebedor externo o de una vista previa sin conexión. En ambos casos puedes añadir un manifiesto de vinculación, y renderiza el resultado.

En cuanto a la creación, es solo de visualización: no hay editor ni guardado, y solo pide la versión activa, nunca el borrador. Aun así, las acciones de widget siguen disponibles. Un visor con `alarm:write` o `command:write` puede reconocer y limpiar alarmas y despachar comandos a dispositivos reales desde el panel que renderiza, y el servidor hace valer esos derechos en cualquier caso.

#### Renovación de sesión y modo quiosco {#session-refresh-and-kiosk-mode}

`/dash` renueva su token de acceso antes de que caduque, usando el token de renovación que recibió al iniciar sesión. Un corte de red o un error del servidor no termina la sesión: el visor sigue reintentando, con pausas cada vez mayores, hasta que el token de acceso actual caduca de verdad. Si el servidor rechaza la renovación (la sesión se cerró en el servidor o el token de renovación caducó), o el corte dura más que el token de acceso, el panel se sustituye por un aviso a pantalla completa, **Tu sesión ha terminado**, con un botón **Iniciar sesión de nuevo**, de modo que una pantalla nunca se queda congelada con valores obsoletos. Al iniciar sesión de nuevo vuelve al mismo panel si la dirección incluye el parámetro `dashboard`.

La dirección puede llevar parámetros, de modo que un marcador o una pantalla de pared abran el panel correcto en el modo correcto:

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark&density=wall
```

| Parámetro | Valores | Efecto |
| --- | --- | --- |
| `dashboard` | un token de panel | Carga la versión activa de ese panel justo después de iniciar sesión. El parámetro se conserva al iniciar sesión, también con inicio de sesión único. |
| `kiosk` | `1` (o `true`) | Oculta la cabecera y sus controles. En su lugar, una insignia de conexión y un botón de pantalla completa flotan sobre las esquinas del panel. En este modo no hay control para cerrar sesión. |
| `chrome` | `0` (o `false`) | Con `kiosk`, oculta también los controles flotantes, para una captura de pantalla o una página que enmarca el panel por sí misma. La insignia de conexión sigue apareciendo mientras el panel se reconecta o está sin conexión, de modo que un panel que dejó de recibir datos nunca parece estar en vivo. |
| `theme` | `dark`, `light` | Fija el tema de color. Fuera del modo quiosco, un interruptor en la cabecera lo cambia y recuerda la elección; el parámetro `theme` nunca se recuerda. |
| `density` | `compact`, `comfortable`, `wall` | Sustituye la densidad con la que se diseñó el panel. Escala todos los tamaños de texto y de espaciado, los márgenes entre celdas y la celda más pequeña a la que puede reducirse una columna, así que `wall` dibuja más grande y, en una pantalla estrecha, con menos columnas, y `compact` dibuja más pequeño. Sin el parámetro se aplica la densidad del propio panel, y el parámetro nunca se guarda en el panel ni se recuerda. |
| `range` | `15m`, `1h`, `6h`, `24h`, `7d` | Abre el panel con ese intervalo de tiempo en lugar del predeterminado del panel. El visor puede seguir cambiándolo. |
| `paused` | `1` | Abre el panel en pausa en el momento en que se carga. Cualquier otro valor lo abre en vivo. |

Un parámetro con un valor no reconocido se ignora y se escribe un aviso en la consola del navegador; el panel se carga igualmente con ese ajuste en su valor por defecto.

Consulta también la vista general de [Arquitectura](./architecture.md) y la [referencia de la API GraphQL](../reference/graphql-api.md).
