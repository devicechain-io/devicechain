---
sidebar_position: 4
title: Paneles
---

# Paneles

DeviceChain incluye un sistema de paneles (dashboards) embebible y con control de versiones para visualizar datos de dispositivos en vivo. Un panel es un **recurso con alcance de inquilino** creado en la consola y renderizado a partir de una definición JSON portable — la misma definición puede embeberse en cualquier aplicación React o abrirse en el visor de referencia independiente.

:::note Estado
Disponible: el editor de lienzo (canvas), el conjunto de widgets integrado (widgets de telemetría, alarma y comando/control), suscripciones en vivo, **acciones de widget** (reconocer/limpiar alarma, enviar comando — autorizadas por el servidor), versionado (publicar / revertir), vista previa sintética, slots con nombre + manifiestos de vinculación (binding manifests), y exportación — además del visor de referencia independiente `/dash`. Planeado: publicar los paquetes de runtime en el registro público de npm (hoy se compilan dentro del repositorio), selectores de fuente de datos más completos (recorrido del grafo de relaciones, drill-down), edición de diseño por punto de quiebre (breakpoint), y widgets adicionales.
:::

## El lienzo

Un panel distribuye widgets en una **cuadrícula CSS fluida**: una cuadrícula de columnas de alta resolución (los widgets se colocan por extensión de columna/fila, no por píxeles fijos), orden en z / capas, un desplazamiento en píxeles opcional por widget para ajuste fino o superposición, y una imagen o color de fondo opcional. Como las columnas son fraccionarias, un panel **llena el ancho de cualquier contenedor en el que se monte** — un panel lateral, un marco de ancho fijo, o una página completa — y un control de dimensionamiento en el momento del montaje (`fill`, ancho fijo, o alto fijo) permite que el host elija. El ajuste a la cuadrícula (snap-to-grid) es inherente a la cuadrícula, y como los widgets pueden compartir celdas y apilarse por z, aún pueden superponerse (por ejemplo, tarjetas sobre una imagen de plano de planta). Los diseños son **por punto de quiebre**, de modo que un panel puede organizarse de forma distinta en diferentes tamaños de pantalla.

## Widgets

Los widgets integrados abarcan tres canales — **telemetría**, **alarma** y **control** — y se renderizan sobre [Apache ECharts](https://echarts.apache.org/):

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

Los widgets se tematizan con propiedades personalizadas de CSS, de modo que una aplicación embebedora controla su apariencia sin modificar el código del widget.

Los widgets también pueden portar **acciones** — reconocer o limpiar una alarma, o enviar un comando — que el servidor autoriza contra los propios derechos con alcance de inquilino del solicitante (por ejemplo, una acción que requiere `alarm:write` queda inerte para un visor de solo lectura).

## Fuentes de datos

Un widget no embebe una consulta — embebe un **selector** tipado que el runtime resuelve:

- **`device`** — un solo dispositivo por token.
- **`anchor`** — telemetría con alcance a una entidad organizacional (un cliente, área o activo), agregada mediante una consulta del lado del servidor sobre los eventos anclados a esa entidad.

Los selectores se resuelven a través del SDK de cliente contra la API GraphQL, de modo que la resolución es **en vivo** — un dispositivo recién asignado a un área aparece en el panel de esa área sin editarlo — y **verificada por permisos**, porque usa el propio acceso de API autenticado y con alcance de inquilino del solicitante. Los valores en vivo llegan mediante **suscripciones GraphQL**, multiplexadas de modo que un panel con muchos widgets abre un flujo por dispositivo en lugar de uno por widget.

## Creación, versionado y vista previa

Los paneles se crean en la **consola**:

- Un **editor de lienzo** de arrastrar y redimensionar con selectores reales de dispositivo / ancla.
- **Versionado** — la definición en vivo es un **borrador** mutable; **publicar** la captura como una versión inmutable, y puedes **revertir** a cualquier versión anterior (lo que la vuelve a convertir en borrador en el mismo lugar). El historial es una lista de instantáneas publicadas, no un diff.
- **Vista previa sintética** — sustituye los datos en vivo por un generador del lado del cliente (seno / rampa / paseo aleatorio) para validar el diseño, las escalas y los umbrales antes de que ningún dispositivo haya reportado.
- **Exportación** — descarga o copia una definición para compartirla o embeberla en otro lugar.

## Incrustación: definiciones, slots y manifiestos de vinculación

Una definición de panel es portable y **reutilizable como plantilla**. En lugar de codificar de forma fija qué dispositivo lee cada widget, los widgets se vinculan a **slots con nombre**; un host suministra un **manifiesto de vinculación** (binding manifest) en el momento del montaje que mapea cada slot a un dispositivo o ancla concreto. Así, **una definición + dos manifiestos → dos paneles en vivo** para dos dispositivos distintos, sin ningún cambio en la definición misma.

El runtime está estructurado como paquetes en capas:

| Paquete | Rol |
|---|---|
| `@devicechain/client` | el SDK de TypeScript — autenticación, operaciones GraphQL, suscripciones en vivo |
| `@devicechain/widgets` | los componentes de widget en React (entra la fuente de datos, salen los píxeles) |
| `@devicechain/dashboards` | el `DashboardHub` (posee la conexión, resuelve selectores, multiplexa suscripciones) y el renderizador |

Cualquier aplicación React embebe un panel en vivo construyendo un hub con un resolvedor y un manifiesto de vinculación, y renderizando la definición. La aplicación independiente **`/dash`** es el embebedor externo de referencia: tiene su propio inicio de sesión, acepta una definición exportada más un manifiesto de vinculación, y la renderiza en modo **solo vista** — el ejemplo de referencia para embeber paneles de DeviceChain en una aplicación separada.

Consulta también la vista general de [Arquitectura](./architecture.md) y la [referencia de la API GraphQL](../reference/graphql-api.md).
