---
title: Paneles
---

# Paneles

DeviceChain incluye un sistema de paneles (dashboards) embebible y con control de versiones para visualizar datos de dispositivos en vivo. Un panel es un **recurso con alcance de inquilino** que creas en la consola. Se renderiza a partir de una definición JSON portable, y esa misma definición se renderiza en la consola, en el visor de referencia independiente o en cualquier aplicación React que instale los paquetes de runtime desde npm.

:::note Estado
Disponible: el editor de lienzo (canvas), el conjunto de widgets integrado (widgets de telemetría, alarma y comando/control), las suscripciones en vivo, las acciones de widget (reconocer/limpiar alarma, enviar comando — autorizadas por el servidor), el versionado (publicar / revertir), la vista previa sintética, los slots con nombre y los manifiestos de vinculación (binding manifests), la exportación, la importación, la clonación y las plantillas iniciales, el visor de referencia independiente `/dash` y los paquetes de runtime publicados en npm.

Planeado: selectores de fuente de datos más completos (recorrido del grafo de relaciones, drill-down), edición del diseño por punto de quiebre (breakpoint) y widgets adicionales.
:::

## El lienzo

Un panel distribuye los widgets en una **cuadrícula CSS fluida**. La cuadrícula ofrece:

- una cuadrícula de columnas de alta resolución: colocas los widgets por extensión de columna/fila, no por píxeles fijos;
- orden en z y capas;
- un desplazamiento en píxeles opcional por widget, para ajustes finos o superposiciones;
- una imagen o un color de fondo opcional.

Como las columnas son fraccionarias, un panel llena el ancho de cualquier contenedor en el que lo montes: un panel lateral, un marco de ancho fijo o una página completa. Un control de dimensionamiento en el momento del montaje (`fill`, ancho fijo o alto fijo) permite que el host elija.

El ajuste a la cuadrícula (snap-to-grid) es inherente a la cuadrícula. Aun así, los widgets pueden superponerse, porque pueden compartir celdas y apilarse por z; por ejemplo, tarjetas sobre la imagen de un plano de planta.

El formato de definición y el renderizador incluyen una caja por punto de quiebre para cada widget, de modo que un panel puede organizarse de forma distinta según el tamaño de pantalla. Sin embargo, el editor de lienzo solo escribe el punto de quiebre base, así que hoy un panel creado en la consola tiene exactamente un diseño.

## Widgets

Los widgets integrados abarcan seis canales: telemetría, estático (sin datos), alarma, control, selección y ubicación. El gráfico de series temporales y el medidor se renderizan sobre [Apache ECharts](https://echarts.apache.org/), y el mapa sobre [MapLibre GL](https://maplibre.org/). El resto son DOM simple.

| Widget | Canal | Muestra |
|---|---|---|
| **Gráfico de series temporales** | telemetría | una o más series de mediciones a lo largo de una ventana de tiempo |
| **Medidor (gauge)** | telemetría | un único valor más reciente frente a un rango / umbrales |
| **Tarjeta de último valor** | telemetría | una sola lectura actual con su marca de tiempo |
| **Tabla** | telemetría | filas recientes de un dispositivo o ancla |
| **Etiqueta** | estático | texto estático |
| **Imagen** | estático | una imagen estática (p. ej., un plano de planta detrás de otros widgets) |
| **Tabla de alarmas** | alarma | alarmas en vivo de un dispositivo o ancla |
| **Conteo de alarmas** | alarma | un recuento agregado de alarmas abiertas |
| **Comando / control** | control | un formulario de parámetros tipado que despacha un comando y muestra en vivo su ciclo de vida de entrega |
| **Selector de entidad** | selección | un selector que reapunta un slot con nombre, de modo que quien lo ve elige qué entidad muestra el panel — o un widget dentro de él |
| **Mapa** | ubicación | la última posición conocida de los dispositivos vinculados |

Los widgets se tematizan con propiedades personalizadas de CSS, de modo que una aplicación embebedora controla su apariencia sin modificar el código de los widgets.

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
- **`anchor`** — telemetría con alcance a una entidad organizacional (un cliente, un área o un activo), designada mediante una relación rastreada. El runtime la expande del lado del cliente a los dispositivos relacionados en ese momento con esa entidad y transmite las muestras sin procesar de cada miembro: un flujo por dispositivo, hasta 500 miembros. Agregar los eventos del ancla en una sola serie del lado del servidor está reservado. El campo `aggregation` de un selector se almacena y se conserva en el ida y vuelta, pero todavía no se lee.
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

## Creación, versionado y vista previa

Creas los paneles en la **consola**:

- **Editor de lienzo** — arrastrar y redimensionar, con selectores reales de dispositivo / ancla.
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

`/dash` renueva su token de acceso antes de que caduque, usando el token de renovación que recibió al iniciar sesión. Si la renovación falla (la sesión se cerró en el servidor, el token de renovación caducó o el servicio no responde), el panel se sustituye por un aviso a pantalla completa, **Tu sesión ha terminado**, con un botón **Iniciar sesión de nuevo**, de modo que una pantalla nunca se queda congelada con valores obsoletos. Al iniciar sesión de nuevo vuelve al mismo panel si la dirección incluye el parámetro `dashboard`.

La dirección puede llevar parámetros, de modo que un marcador o una pantalla de pared abran el panel correcto en el modo correcto:

```text
/dash/?dashboard=<token>&kiosk=1&theme=dark&density=wall
```

| Parámetro | Valores | Efecto |
| --- | --- | --- |
| `dashboard` | un token de panel | Carga la versión activa de ese panel justo después de iniciar sesión. El parámetro se conserva al iniciar sesión, también con inicio de sesión único. |
| `kiosk` | `1` (o `true`) | Oculta la cabecera y sus controles y ofrece un botón de pantalla completa. En este modo no hay control para cerrar sesión. |
| `theme` | `dark`, `light` | Fija el tema de color. Fuera del modo quiosco, un interruptor en la cabecera lo cambia y recuerda la elección; el parámetro `theme` nunca se recuerda. |
| `density` | `compact`, `comfortable`, `wall` | Reservado para la anulación de la densidad del lienzo. El valor se valida y se acepta, pero todavía no cambia el renderizado. |

Un parámetro con un valor no reconocido se ignora y se escribe un aviso en la consola del navegador; el panel se carga igualmente con ese ajuste en su valor por defecto.

Consulta también la vista general de [Arquitectura](./architecture.md) y la [referencia de la API GraphQL](../reference/graphql-api.md).
