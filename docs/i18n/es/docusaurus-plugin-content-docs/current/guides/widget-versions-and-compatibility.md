---
sidebar_position: 11
title: Versiones y compatibilidad de widgets
---

# Versiones y compatibilidad de widgets {#widget-versions-and-compatibility}

Un panel guardado registra qué implementación de widget creó cada widget. Cuando actualizas un
paquete de widgets, DeviceChain decide para cada widget almacenado si el paquete nuevo todavía puede
renderizarlo. Esta página explica esa decisión y qué hacer ante cada resultado.

## Qué se almacena {#what-is-stored}

Un widget de un paquete registrado lleva una referencia `widget` junto a su `type`:

```json
{
  "id": "tank-1",
  "type": "acme/tank-level",
  "widget": { "version": "1.2.0", "optionsVersion": 1, "sdkApiVersion": 1 },
  "layout": { "base": { "col": 0, "colSpan": 8, "row": 0, "rowSpan": 4, "z": 0 } },
  "datasource": { "kind": "slot", "slot": "tank", "measurements": ["level"] },
  "options": { "unit": "%" }
}
```

La consola registra la referencia cuando un autor añade un widget, copiando `version`,
`optionsVersion` y `sdkApiVersion` del registro desde el que se añadió. Esto también se aplica a los
widgets integrados: cada uno lleva su propia versión de widget, empezando por `1.0.0`, que no es la
versión de la plataforma y solo cambia cuando su renderizado cambia de una forma que merece
señalarse. Los widgets integrados guardados antes de que existieran las referencias no tienen
ninguna y siguen renderizándose.

## La regla de compatibilidad {#the-compatibility-rule}

Un widget almacenado es compatible con un paquete instalado cuando coinciden su `id`, su
`optionsVersion` y su `sdkApiVersion`. La `version` es procedencia y no decide la compatibilidad.

Una referencia guardada antes de que la consola registrara `sdkApiVersion` solo tiene `version` y
`optionsVersion`. Se asocia por `id` y `optionsVersion` y sigue renderizándose; la próxima vez que un
autor reemplace el widget, la nueva referencia registrará los tres valores.

| Widget almacenado frente al paquete instalado | Resultado |
| --- | --- |
| Mismo `id`, `optionsVersion` y `sdkApiVersion`, misma `version` | Se renderiza. Es el único caso en que el widget lo renderiza exactamente la versión que lo creó. |
| Mismo `id`, `optionsVersion` y `sdkApiVersion`, `version` distinta | Se renderiza. El panel de ajustes del widget en la consola muestra un aviso no bloqueante con ambas versiones, para que un cambio de aspecto no sorprenda. |
| `optionsVersion` distinta | Marcador de posición, con el motivo. Las opciones almacenadas significan algo distinto para el paquete instalado. |
| `sdkApiVersion` distinta | Marcador de posición, con el motivo. |
| Paquete no instalado en esta aplicación | Marcador de posición: el paquete no está instalado en este anfitrión. |
| `id` personalizado sin referencia `widget` | Marcador de posición: el widget instalado no puede asociarse a él. |
| Una referencia `widget` mal formada, en cualquier widget | Marcador de posición: la información de versión almacenada no es válida. |

Un marcador de posición ocupa su lugar en el lienzo y conserva el JSON original del widget, incluida
la referencia de versión y cualquier campo que no reconozca. No abre ninguna suscripción de datos ni
ofrece acciones. Cargar el panel, mover el marcador de posición y guardar un borrador conservan sus
datos, de modo que instalar más tarde el paquete que falta recupera el widget sin pérdida. La
consola se niega a publicar una nueva versión del panel mientras algún widget sea un marcador de
posición; consulta
[publicar mientras un widget no está resuelto](./adding-widget-packages.md#publishing-while-a-widget-is-unresolved).

Si construyes tu propio anfitrión, `@devicechain/dashboards` exporta las mismas decisiones:
`unresolvedWidgets(definition)` enumera los marcadores de posición de una definición analizada con
sus motivos, y `widgetVersionNotice(id, reference, descriptor)` devuelve el aviso de procedencia, o
`undefined` cuando las versiones coinciden.

## Actualizar un paquete {#upgrading-a-package}

Elige el incremento de versión según lo que cambió:

- **Una corrección de errores o un cambio visual que no altera lo que significan las opciones
  almacenadas:** incrementa `version` (`1.2.0` a `1.2.1`). Los paneles existentes siguen
  renderizándose, y un autor que abre uno de estos widgets en la consola ve el aviso de versión.
- **Un cambio en el significado o la forma de las opciones almacenadas:** incrementa
  `optionsVersion` y también `version`. Los widgets existentes pasan a ser marcadores de posición
  hasta que un autor los reemplace, porque en esta versión no hay migración automática de opciones.
  Es preferible añadir una opción nueva con valor por defecto que cambiar una existente, para no
  necesitar este incremento.
- **Un cambio a una nueva generación del contrato del SDK:** cambia `sdkApiVersion`, y un anfitrión
  solo acepta registros de una generación que admite. Los widgets cuya referencia almacenada
  registra la generación anterior pasan a ser marcadores de posición, con el motivo, hasta que un
  autor los reemplace.

Actualizar un paquete nunca reescribe un panel ni una versión ya publicada. Para llevar un widget a
una nueva `optionsVersion`, un autor lo reemplaza, edita sus ajustes y publica una nueva versión del
panel. Una versión publicada creada con `1.2.0` que ahora renderiza `1.2.1` no es una reproducción
exacta; la reproducción exacta requiere la misma `version`. Un anfitrión instala una sola versión de
cada id.

## Paquetes ausentes en un panel publicado {#missing-packages-in-a-published-dashboard}

Como la implementación no forma parte del panel, una aplicación visora a la que le falta un paquete
muestra el marcador de posición, no una página de error, y el resto del panel se renderiza. Mantén la
consola y todos los visores con el mismo conjunto de paquetes de widgets.
