---
title: Paneles
---

# Paneles

DeviceChain incluye un sistema de paneles (dashboards) embebible y con control de versiones para visualizar datos de dispositivos en vivo. Un panel es un **recurso con alcance de inquilino** creado en la consola y renderizado a partir de una definición JSON portable — la misma definición se renderiza en la consola, en el visor de referencia independiente, o en cualquier aplicación React que instale los paquetes de runtime desde npm.

:::note Estado
Disponible: el editor de lienzo (canvas), el conjunto de widgets integrado (widgets de telemetría, alarma y comando/control), suscripciones en vivo, **acciones de widget** (reconocer/limpiar alarma, enviar comando — autorizadas por el servidor), versionado (publicar / revertir), vista previa sintética, slots con nombre + manifiestos de vinculación (binding manifests), y exportación — además del visor de referencia independiente `/dash` y los paquetes de runtime publicados en npm. Planeado: selectores de fuente de datos más completos (recorrido del grafo de relaciones, drill-down), edición de diseño por punto de quiebre (breakpoint), y widgets adicionales.
:::

## El lienzo

Un panel distribuye widgets en una **cuadrícula CSS fluida**: una cuadrícula de columnas de alta resolución (los widgets se colocan por extensión de columna/fila, no por píxeles fijos), orden en z / capas, un desplazamiento en píxeles opcional por widget para ajuste fino o superposición, y una imagen o color de fondo opcional. Como las columnas son fraccionarias, un panel **llena el ancho de cualquier contenedor en el que se monte** — un panel lateral, un marco de ancho fijo, o una página completa — y un control de dimensionamiento en el momento del montaje (`fill`, ancho fijo, o alto fijo) permite que el host elija. El ajuste a la cuadrícula (snap-to-grid) es inherente a la cuadrícula, y como los widgets pueden compartir celdas y apilarse por z, aún pueden superponerse (por ejemplo, tarjetas sobre una imagen de plano de planta). El formato de definición y el renderizador incluyen una caja **por punto de quiebre** para cada widget, de modo que un panel puede organizarse de forma distinta en diferentes tamaños de pantalla — pero el editor de lienzo solo escribe el punto de quiebre base, así que un panel creado en la consola tiene hoy exactamente un diseño.

## Widgets

Los widgets integrados abarcan cinco canales — **telemetría**, **alarma**, **control**, **selección** y **ubicación**. El gráfico de series temporales y el medidor se renderizan sobre [Apache ECharts](https://echarts.apache.org/), el mapa sobre [MapLibre GL](https://maplibre.org/); el resto son DOM simple:

| Widget | Canal | Muestra |
|---|---|---|
| **Gráfico de series temporales** | telemetría | una o más series de mediciones a lo largo de una ventana de tiempo |
| **Medidor (gauge)** | telemetría | el último valor único frente a un rango / umbrales |
| **Tarjeta de último valor** | telemetría | una sola lectura actual con su marca de tiempo |
| **Tabla** | telemetría | filas recientes para un dispositivo o ancla |
| **Etiqueta** | telemetría | texto estático |
| **Imagen** | telemetría | una imagen estática (p. ej. un plano de planta detrás de otros widgets) |
| **Tabla de alarmas** | alarma | alarmas en vivo para un dispositivo o ancla |
| **Conteo de alarmas** | alarma | un recuento acumulado de alarmas abiertas |
| **Comando / control** | control | un formulario de parámetros tipado que despacha un comando y muestra su ciclo de vida de entrega en vivo |
| **Selector de entidad** | selección | un selector que reapunta un slot con nombre, de modo que un visor elige qué entidad muestra el panel — o un widget dentro de él |
| **Mapa** | ubicación | la última posición conocida de los dispositivos vinculados |

:::note Las teselas del mapa vienen del inquilino, no del widget
El widget de mapa renderiza las posiciones de los dispositivos sobre el **mapa base** del inquilino, que en una instancia nueva ya viene configurado, de modo que un widget de mapa dibuja teselas sin que nadie tenga que prepararlo. Las opciones `tileUrl` y `attribution` del propio widget son una anulación para ese panel concreto, útil para probar un proveedor antes de aplicarlo a todo el inquilino.

Qué proveedor conviene usar realmente es una decisión a nivel de inquilino, no por widget: vea [Mapas base](./basemaps.md).

Si ningún nivel tiene una fuente de teselas —un operador definió el valor por defecto de la instancia como `{}` y el inquilino no definió nada— el widget sigue dibujando un mapa real, sobre un mapa base del mundo incorporado con continentes y fronteras de dominio público. Un panel liso con las posiciones relativas queda reservado únicamente para el caso en que ni siquiera se pueda cargar el motor de mapas, por ejemplo detrás de un proxy que lo bloquea.

Leer posiciones también requiere la autoridad `location:read`, que **no** otorga la base de solo lectura que recibe cada miembro — vea [ubicación de dispositivos](../guides/connecting-a-device.md). A un visor que no la tenga se le indica, en lugar de mostrarle un mapa vacío.
:::

Los widgets se tematizan con propiedades personalizadas de CSS, de modo que una aplicación embebedora controla su apariencia sin modificar el código del widget.

Los widgets también pueden portar **acciones** — reconocer o limpiar una alarma, o enviar un comando — que el servidor autoriza contra los propios derechos con alcance de inquilino del solicitante (por ejemplo, una acción que requiere `alarm:write` queda inerte para un visor de solo lectura).

Abrir un panel requiere **`dashboard:read`**, que *no* forma parte de la línea base de solo lectura que recibe todo miembro habilitado de un inquilino — de modo que un miembro sin rol asignado puede ver dispositivos, eventos, estado, comandos y alarmas, pero no puede listar ni abrir un panel hasta que un rol se lo conceda. `dashboard:write` controla crear, actualizar, publicar, revertir y eliminar.

## Fuentes de datos

Un widget no embebe una consulta — embebe un **selector** tipado que el runtime resuelve:

- **`device`** — un solo dispositivo por token.
- **`anchor`** — telemetría con alcance a una entidad organizacional (un cliente, área o activo), agregada mediante una consulta del lado del servidor sobre los eventos anclados a esa entidad.
- **`slot`** — un **marcador con nombre** que el host resuelve en el momento del montaje a partir de su manifiesto de vinculación (ver *Incrustación* más abajo). Esto es lo que la consola escribe hoy: reescribe los selectores concretos `device` y `anchor` como slots cuando carga un panel, de modo que un panel creado es una plantilla reutilizable por defecto.

Otros dos tipos (`devices`, `relatedTraversal`) están reservados para que una definición almacenada siga siendo compatible hacia adelante; el runtime los rechaza hasta que se implementen.

Los selectores se resuelven a través del SDK de cliente contra la API GraphQL, de modo que la resolución es **en vivo** — un dispositivo recién asignado a un área aparece en el panel de esa área sin editarlo — y **verificada por permisos**, porque usa el propio acceso de API autenticado y con alcance de inquilino del solicitante. Cómo llegan los valores en vivo depende del canal: los widgets de telemetría leen una **suscripción GraphQL**, multiplexada de modo que un panel con muchos widgets abre un flujo por dispositivo en lugar de uno por widget; los widgets de alarma releen una consulta, disparada por un flujo de alarmas en vivo y respaldada por un sondeo cada 30 segundos; los widgets de control funcionan solo por sondeo, porque command-delivery no expone ninguna suscripción. Los widgets de alarma y de control mantienen cada uno su propio flujo y temporizador — solo el canal de telemetría está multiplexado.

## Creación, versionado y vista previa

Los paneles se crean en la **consola**:

- Un **editor de lienzo** de arrastrar y redimensionar con selectores reales de dispositivo / ancla.
- **Versionado** — la definición en vivo es un **borrador** mutable; **publicar** la captura como una versión inmutable, y puedes **revertir** a cualquier versión anterior (lo que la vuelve a convertir en borrador en el mismo lugar). El historial es una lista de instantáneas publicadas, no un diff. La definición de una versión publicada no se puede leer por sí sola: la lista de versiones solo lleva su número, su etiqueta y descripción opcionales, y quién la publicó y cuándo — revertir, que es una escritura, es la única forma de recuperar su contenido.
- **Vista previa sintética** — sustituye los datos en vivo por un generador del lado del cliente (seno / rampa / paseo aleatorio) para validar el diseño, las escalas y los umbrales antes de que ningún dispositivo haya reportado.
- **Exportación** — descarga o copia una definición para compartirla o embeberla en otro lugar.

## Incrustación: definiciones, slots y manifiestos de vinculación

Una definición de panel es portable y **reutilizable como plantilla**. En lugar de codificar de forma fija qué dispositivo lee cada widget, los widgets se vinculan a **slots con nombre**; un host suministra un **manifiesto de vinculación** (binding manifest) en el momento del montaje que mapea cada slot a un dispositivo o ancla concreto. Así, **una definición + dos manifiestos → dos paneles en vivo** para dos dispositivos distintos, sin ningún cambio en la definición misma.

El runtime está estructurado como paquetes en capas:

| Paquete | Rol |
|---|---|
| `@devicechain/client` | el SDK de TypeScript — autenticación, operaciones GraphQL, suscripciones en vivo |
| `@devicechain/widgets` | los componentes de widget en React (entra la fuente de datos, salen los píxeles) y el renderizador que los distribuye |
| `@devicechain/dashboards` | el `DashboardHub` (posee la conexión, resuelve selectores, multiplexa las suscripciones de telemetría) y los tipos de definición, selector, slot y manifiesto de vinculación |

Una aplicación React embebe un panel en vivo construyendo un hub con un resolvedor y un manifiesto de vinculación, y renderizando la definición. Ese camino está resuelto de extremo a extremo dentro de este repositorio — la consola y la aplicación independiente `/dash` lo recorren, compilando contra los mismos artefactos que descarga un consumidor externo — y los paquetes están publicados en npm, así que una aplicación externa los instala de la misma manera. Ver [Paquetes de npm](../reference/npm-packages.md) para la línea de instalación, la política de versiones y dist-tags, y la única pieza de cableado del anfitrión que necesita el widget de mapa. La aplicación independiente **`/dash`** es el embebedor externo de referencia: tiene su propio inicio de sesión, acepta una definición exportada más un manifiesto de vinculación, y la renderiza. Es solo de vista en cuanto a la **creación** — no hay editor, no hay guardado, y nunca obtiene un panel desde el servicio — pero las acciones de widget siguen disponibles: un visor con `alarm:write` o `command:write` puede reconocer y limpiar alarmas y despachar comandos a dispositivos reales desde el panel que renderiza, y el servidor hace valer esos derechos en cualquier caso.

Consulta también la vista general de [Arquitectura](./architecture.md) y la [referencia de la API GraphQL](../reference/graphql-api.md).
