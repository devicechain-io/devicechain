---
sidebar_position: 9
title: Escribir un widget personalizado
---

# Escribir un widget personalizado {#writing-a-custom-widget}

Un widget personalizado es un componente de React más un pequeño fragmento de datos simples que lo
describe. Escribes ambos con `@devicechain/widget-sdk`, los distribuyes en un paquete de npm y
registras el paquete en la aplicación que muestra los paneles. Los widgets integrados se declaran
con exactamente la misma interfaz, así que un widget personalizado obtiene el mismo enlace de
datos, la misma validación de opciones y el mismo manejo de errores.

Tu widget recibe datos ya resueltos. No autentica, no resuelve fuentes de datos, no hace sondeos ni
abre una conexión de telemetría. El anfitrión hace todo eso y entrega a tu componente una
instantánea.

Un paquete completo que puedes copiar está en el repositorio, en `examples/widget-tank-level`. Se
compila fuera del monorepo contra tarballs empaquetados del SDK.

## Instala el SDK {#install-the-sdk}

```bash
npm install @devicechain/widget-sdk @devicechain/dashboards react
```

En tu paquete de widgets, declara `@devicechain/widget-sdk`, `@devicechain/dashboards` y `react`
como **dependencias peer**, nunca como dependencias. El SDK define los contextos de React que lee un
widget (selección, zona horaria, raíz del tema), y un contexto se identifica por el objeto que lo
creó. Por eso el anfitrión y cada paquete de widgets deben resolver una sola copia del SDK.

La entrada raíz del SDK no contiene código de gráficos ni de mapas. Un widget que solo dibuja DOM
importa desde la raíz y no añade al empaquetado del anfitrión ni Apache ECharts, ni MapLibre, ni
los widgets integrados. Las utilidades de gráficos (`EChart`, `buildLineOption`,
`buildGaugeOption`, `buildCompassOption`, `useChartTheme`, `resolveChartTheme` y `severityFill`)
están en `@devicechain/widget-sdk/echarts`, y `echarts` es una
dependencia peer opcional que solo necesita un paquete que importe esa entrada.

## Declara un widget {#declare-a-widget}

```tsx
import { WidgetFrame, defineWidget, formatValue, pickSample } from '@devicechain/widget-sdk';

export const tankLevelWidget = defineWidget({
  descriptor: {
    id: 'acme/tank-level',
    version: '1.0.0',
    sdkApiVersion: 1,
    optionsVersion: 1,
    display: { title: 'Tank level', category: 'Telemetry' },
    channel: 'measurement',
    datasource: { mode: 'required', selectors: ['device', 'slot'] },
    binding: { window: { option: 'window', min: 1, default: 1 } },
    options: {
      unit: { kind: 'string', doc: 'Unit shown beside the level.', label: 'Unit' },
      window: { kind: 'number', integer: true, min: 1, doc: 'Samples kept.', label: 'Samples kept' },
    },
    defaults: { unit: '%', window: 1 },
    interactions: {},
  },
  implementation: {
    kind: 'trusted-react',
    component: ({ data }) => (
      <WidgetFrame title="Tank level">{formatValue(pickSample(data.latest, undefined)?.value, 1)}</WidgetFrame>
    ),
  },
});
```

`defineWidget` infiere el canal y el esquema de opciones a partir del literal. Un componente cuyos
datos no coinciden con el canal, valores por defecto que no coinciden con el esquema, un enlace que
nombra una opción inexistente o una interacción que el canal no puede transportar son errores de
compilación. El registro vuelve a comprobar las mismas reglas cuando el anfitrión lo construye, para
quienes eluden el compilador.

### El descriptor {#the-descriptor}

El descriptor son datos simples serializables como JSON. No contiene componentes, callbacks, URL de
importación ni tokens, así que el analizador de definiciones y los validadores pueden leerlo sin
cargar ni confiar en tu código.

| Campo | Significado |
| --- | --- |
| `id` | Un id con espacio de nombres, `scope/name`, como `acme/tank-level`, en letras minúsculas, dígitos y guiones. Los ids sin barra están reservados para los widgets integrados. En esta versión la propiedad del espacio de nombres es una convención entre operadores; no demuestra quién publicó un paquete. |
| `version` | La versión de este widget, como `MAJOR.MINOR.PATCH`. Es procedencia. Consulta [versiones y compatibilidad de widgets](./widget-versions-and-compatibility.md). |
| `sdkApiVersion` | La generación del contrato del SDK para la que se escribió el widget. Actualmente `1`. |
| `optionsVersion` | La forma de las opciones almacenadas. Increméntala solo cuando las opciones almacenadas dejen de significar lo mismo. |
| `display` | `title` y, opcionalmente, `description` y `category`, para el menú Añadir de un anfitrión. `titleId` y `categoryId` son ids de mensaje opcionales, descritos en [etiquetas y textos de ayuda](#labels-and-help-text). |
| `channel` | Qué datos consume el widget. Ver abajo. |
| `datasource` | `{ mode: 'none' }`, o `mode: 'optional'` o `'required'` con los `selectors` que un autor puede elegir: `device`, `anchor` o `slot`. Un widget del canal `control` apunta a un único dispositivo, así que solo ofrece `device` y `slot`. |
| `binding` | Cómo se suscribe el anfitrión, escrito como constantes y referencias a tus propias claves de opción. |
| `options` | El esquema de opciones. |
| `constraints` | Reglas opcionales que abarcan más de una opción, como `greater-than`. |
| `defaults` | Las opciones iniciales de un widget recién añadido. |
| `interactions` | Los callbacks del anfitrión que el widget solicita. |
| `freshness` | Opcional, `true` para activarlo. Solo en el canal `measurement`: el marco muestra la antigüedad de la muestra más reciente del widget. Si el widget declara además una opción numérica `staleAfterSeconds`, un valor más antiguo que ese umbral se atenúa y se marca como obsoleto. Sin esa opción solo se muestra la antigüedad y nunca se marca nada como obsoleto. Un widget que muestra un único valor pasa la hora de esa muestra como `<WidgetFrame sampleTime={sample?.occurredTime ?? null}>`, para que la antigüedad sea la del valor en pantalla y no la de la última medición recibida. |
| `layout` | Límites de tamaño opcionales, en celdas de la cuadrícula. Consulta [Tamaño](#size). |

### Canales y política de enlace {#channels-and-binding-policy}

Un widget declara un canal:

| Canal | El componente recibe | Política de enlace |
| --- | --- | --- |
| `measurement` | `latest` por nombre de medición, una ventana móvil `samples` y un `error`; `members` en un widget de flota | `window`: cuántas muestras conservar. `windowSeconds`: hace que el widget dibuje en su lugar un lapso de tiempo (consulta [el intervalo de tiempo del panel](#the-boards-time-range)). `members`: lo convierte en un widget de flota (ver más abajo) |
| `aggregate` | `aggregates` (el recuento, el mínimo, el máximo, la suma y la media de cada medición en el lapso, o por qué no se pudieron calcular) y la lectura en vivo más reciente de cada medición | `windowSeconds`: el lapso que cubren las cifras |
| `alarm` | Las filas de alarma actuales, un recuento total e indicadores de carga y error | `pageSize` y, opcionalmente, los filtros `state`, `severity` y `acknowledged` |
| `control` | El historial de comandos de un dispositivo destino | `pageSize` |
| `location` | Muestras de ubicación de la entidad enlazada | ninguna |
| `selection` | Sin datos. El widget gobierna un slot a partir de la búsqueda de candidatos del anfitrión. | ninguna |
| `none` | Sin datos. Un widget estático. | ninguna |

Un valor de enlace es una constante, `{ const: 25 }`, o una referencia a una clave de opción,
`{ option: 'window', min: 1, default: 1 }`. La referencia se comprueba contra el tipo de la opción al
compilar. No puedes aportar una cadena GraphQL ni una función de suscripción: el transporte es del
anfitrión. Los canales `selection` y `none` no abren ningún canal de datos y deben declarar
`datasource: { mode: 'none' }`.

Un widget `measurement` que declara `members` lee su fuente de datos dispositivo a dispositivo:
enlazado a un grupo, `data.members` lista cada dispositivo con su propia lectura más reciente por
medición, de modo que dos dispositivos que informan del mismo nombre se mantienen separados.
`{ const: true }` activa el modo siempre, y una referencia a una opción con `equals` lo activa para un
valor de una opción enum, como hace el modo *una barra por dispositivo* del gráfico de barras. Un
widget `measurement` o `aggregate` sin ese modo muestra un solo dispositivo: ofrece solo `device` y
`slot` como selectores, porque el anfitrión rechaza un grupo que le llegue, con una frase que remite
a la tabla de flota, en lugar de mostrar el dispositivo que informó en último lugar.

Cuando un slot no tiene dispositivo, o un dispositivo enlazado se elimina, el anfitrión muestra su
estado estándar de sin enlazar o no disponible y no entrega a tu componente los datos de la entidad
anterior.

## Opciones {#options}

Una opción tiene un `kind` (`string`, `number`, `boolean`, `enum` o `list`) y una frase `doc`. Un número puede
declarar `min`, `max` e `integer`. Un enum declara sus `values`. Una cadena puede declarar
`format: 'json'`, que exige que el valor se pueda analizar como JSON. `required` marca una opción cuya
ausencia deja al widget sin nada que mostrar; una cadena vacía cuenta como ausente. El anfitrión
valida las opciones almacenadas contra el esquema, y la consola genera su formulario de ajustes a
partir de él.

Una opción `list` almacena un arreglo de objetos pequeños, como los umbrales de un widget. Declara un
objeto `item` cuyos campos son especificaciones `string`, `number`, `boolean` o `enum` (un campo puede
ser `required`, y una lista no se puede anidar) un `maxItems` opcional y `ordered: true` cuando el orden de las entradas significa algo (el formulario de ajustes ofrece entonces subir y bajar cada fila). Se rechaza una entrada
almacenada con un campo que `item` no declara, y el formulario de ajustes muestra una fila por entrada
con un control por campo. La opción integrada `thresholds`, que llevan la mayoría de los widgets de
telemetría integrados, es una de ellas: cada entrada nombra una severidad (`normal`, `warning`,
`minor`, `major` o `critical`), nunca un color, de modo que el tema decide su aspecto en claro y en
oscuro. Solo los niveles anómalos tienen color: un valor en una banda `normal` se ve exactamente igual
que sin umbrales, de modo que una lectura sana nunca se colorea.

`optThresholds`, `bandAt` y `thresholdBands` leen esa misma opción en tu propio widget, y
`useSeverityColors` da el color del tema para cada nivel anómalo.

Lee las opciones de forma defensiva con los ayudantes del SDK `optString`, `optNumber` y
`optBoolean`. Ante un valor editado a mano del tipo equivocado no lanzan
una excepción: `optString` y `optNumber` devuelven `undefined`, y `optBoolean` devuelve `false`
para cualquier cosa que no sea un `true` almacenado.

### Etiquetas y textos de ayuda {#labels-and-help-text}

Da a cada opción un `label` portable y, cuando una frase ayude, un `help`. Un anfitrión que nunca ha
oído hablar de tu widget muestra ese texto tal cual, de modo que tus opciones se entienden sin
ninguna traducción en el anfitrión. Cada texto también puede llevar un id de mensaje que el
anfitrión busca primero en sus propias traducciones:

| Texto | Texto portable | Id de mensaje opcional |
| --- | --- | --- |
| Etiqueta de la opción | `label` | `labelId` |
| Ayuda de la opción | `help` | `helpId` |
| Opciones de un enum | `valueLabels`, por valor | `valueLabelIds`, por valor |
| La entrada sin valor de un enum | `placeholder` | `placeholderId` |
| Título y categoría del widget | `display.title`, `display.category` | `display.titleId`, `display.categoryId` |

La consola usa el id de mensaje cuando sus propias traducciones lo definen y, si no, tu texto
portable. Una etiqueta sin ninguno de los dos recurre a la clave de la opción hecha legible
(`maxRows` se muestra como «Max rows»), y una opción de enum sin ninguno muestra su valor tal cual.
Un id de mensaje que el anfitrión no define no es un error, así que tu paquete funciona en un
anfitrión que nunca lo ha visto.

```tsx
options: {
  show: {
    kind: 'enum',
    values: ['bar', 'number'],
    doc: 'Whether the card draws the fill bar under the number.',
    label: 'Show',
    valueLabels: { bar: 'Number and fill bar', number: 'Number only' },
    placeholder: 'Number and fill bar',
  },
},
```

### Controles de edición especializados {#specialized-authoring-controls}

Una opción puede nombrar un flujo de edición especializado en su campo `control`. No tiene relación
con el canal de datos `control`. Sin él, la opción recibe el campo genérico de su tipo: un cuadro de
texto, un cuadro numérico, una casilla o una lista desplegable.

El vocabulario es abierto. Cualquier cadena no vacía se registra, y un anfitrión que no implementa
el control nombrado muestra en su lugar el campo genérico de esa opción. Así, un paquete sigue
siendo instalable en un anfitrión más antiguo que los controles que nombra. Nombra tus propios
controles en tu espacio de nombres, como `acme/capacity-presets`. Los nombres sin prefijo
pertenecen al anfitrión.

La consola implementa los controles enumerados en `HOST_OPTION_CONTROLS`, exportado por
`@devicechain/dashboards`:

| Control | Flujo |
| --- | --- |
| `command-vocabulary` | Elige uno de los comandos del dispositivo enlazado y escribe juntos su clave, su etiqueta y sus parámetros. |
| `slot-target` | Elige uno de los slots del panel. |
| `device-type` | Elige un tipo de dispositivo del catálogo del inquilino y guarda su token. En una opción de texto o en un campo de texto de un elemento de lista. |
| `basemap` | Marca la fuente de teselas ráster del mapa y su atribución. |

Un control que la consola no implementa, incluido cualquier control con espacio de nombres, recibe
el campo genérico. Para dar a tus opciones una experiencia de edición propia, distribuye un editor.

### Un editor personalizado {#a-custom-editor}

`implementation.editor` es un componente de React opcional. Cuando un widget tiene uno, la consola
lo muestra en lugar de los campos de opción generados. Los campos de la fuente de datos y los avisos
de la consola siguen siendo de la consola. El editor recibe `WidgetEditorProps`, exportado por
`@devicechain/widget-sdk`:

| Prop | Significado |
| --- | --- |
| `options` | Las opciones almacenadas del widget, de solo lectura. |
| `setOptions(patch)` | La única forma de cambiarlas. Varias claves en una llamada se aplican juntas. |
| `services` | Servicios del anfitrión, pasados de forma explícita. Ver abajo. |
| `descriptor` | El descriptor de tu registro, para las etiquetas, los límites y los valores de enum de sus opciones. |

`setOptions` se comprueba contra tu esquema de opciones antes de aplicarse. Una clave que el esquema
no declara, o un valor del tipo equivocado, lanza una excepción en el momento en que el editor
comete el error, en lugar de almacenarse sin que nada la lea. La consola captura ese error y lo muestra junto a tu editor, y no se escribe nada. `undefined` y una cadena vacía
eliminan una clave, igual que `false` en un booleano. Un número fuera de los límites declarados se
ajusta a ellos.

`services` (`WidgetEditorServices`) contiene:

| Servicio | Significado |
| --- | --- |
| `slots` | Los slots del panel, por nombre. Vacío en un panel sin slots. |
| `deviceToken` | El token del dispositivo al que el widget está enlazado directamente, cuando lo está. |
| `commandChoices(deviceToken)` | Los comandos que acepta un dispositivo. Opcional: solo se proporciona cuando tu descriptor declara el control `command-vocabulary` en una opción, y un anfitrión sin vocabulario de comandos lo omite. |

Un editor no recibe hub, ni cliente, ni token de acceso, y no debe importar nada de la aplicación
anfitriona. Un servicio que el anfitrión no puede proporcionar está ausente, y tu editor debe
tolerarlo. Si el editor lanza una excepción al mostrarse, la consola lo sustituye por un mensaje de
error y un botón para reintentar, y se conservan los demás cambios del autor en el panel.

```tsx
implementation: {
  kind: 'trusted-react',
  component: TankLevelCard,
  editor: function TankLevelCardEditor({ options, setOptions, descriptor }) {
    return (
      <label>
        {descriptor.options.unit?.label ?? 'unit'}
        <input value={options.unit ?? ''} onChange={(e) => setOptions({ unit: e.target.value })} />
      </label>
    );
  },
},
```

Dentro de `defineWidget`, `options` y `setOptions` se tipan a partir de tu esquema de opciones, así
que una clave mal escrita también es un error de compilación. El widget de tarjeta del paquete de
ejemplo tiene un editor completo.

## Tamaño {#size}

Un widget declara qué tan grande puede ser en un bloque opcional `layout` de su descriptor. Los tamaños se
cuentan en celdas de la cuadrícula (columnas a lo ancho, filas a lo alto), no en píxeles:

```ts
layout: {
  defaultSpan: { cols: 4, rows: 3 }, // el tamaño de un widget recién añadido
  minSpan: { cols: 3, rows: 2 },     // el mínimo al que se puede redimensionar (por defecto 1 x 1)
  maxSpan: { cols: 8, rows: 6 },     // el máximo (por defecto: sin límite)
  resize: 'both',                    // 'both' (por defecto), 'horizontal', 'vertical' o 'none'
},
```

Cada valor es un número entero positivo, y `minSpan` no puede superar a `defaultSpan`, que a su vez no
puede superar a `maxSpan`. Un modo `resize` distinto de `both` mantiene el otro eje en `defaultSpan`:
`'horizontal'` fija las filas, `'vertical'` las columnas y `'none'` ambos. Un `minSpan` o `maxSpan` que
declares debe coincidir con `defaultSpan` en un eje fijo. El registro rechaza un bloque que incumpla estas
reglas cuando el anfitrión lo construye.

El editor añade un widget nuevo con `defaultSpan`. `lintLayout`, de `@devicechain/dashboards`, informa
como error de un panel que coloque el widget fuera de sus límites. Elige un `minSpan` que sea la caja más
pequeña en la que tu widget siga siendo legible. Un widget sin bloque `layout` se añade con 4 x 2 celdas y
sin límites, y `lintLayout` indica que no declaró ninguno.

Las celdas son cuadradas, y un panel nunca muestra una celda más pequeña que su tamaño mínimo de
celda (56 píxeles por defecto; en una pantalla más estrecha el panel se reorganiza). Por eso conviene
calcular `minSpan` con esa celda mínima: con la separación por defecto de 8 píxeles, 2 celdas son 120
píxeles, 3 son 184 y 4 son 248. En una pantalla más ancha las celdas crecen y tu widget solo se hace
más grande. Las versiones anteriores a la cuadrícula de celdas cuadradas contaban celdas en una
cuadrícula de 24 columnas estrechas y filas de 40 píxeles; un paquete que aún declare esos números
sigue funcionando, pero sus tamaños son aproximadamente el doble de anchos de lo que deberían, y
`lintLayout` informa de cualquier panel que lo coloque fuera de ellos. Divide las columnas entre dos y
revisa las filas.

El texto sigue la densidad del panel. `WidgetFrame` añade el relleno y configura el área de contenido del
widget como un contenedor de tamaño, de modo que dentro de él las unidades de contenedor CSS (`cqw`, `cqh`)
miden el espacio que el widget tiene de verdad. Toma los tamaños de texto de la escala tipográfica
(`fontVar('md')` y los demás pasos de `xs` a `display`) y el espaciado de `spaceVar(1)` a `spaceVar(4)` en
lugar de escribir valores en píxeles: ambos se multiplican por la densidad del panel (compacta, cómoda o
mural), que define quien crea el panel y puede cambiar quien lo ve. Para una lectura grande,
`fitValueFontCss(chars)` devuelve el tamaño de fuente CSS para un número de caracteres. Un canvas no puede
leer variables CSS: llama a `useWidgetScale()` desde un componente dibujado dentro del marco (o pasa al
marco una función como hijo) para obtener `sizeClass` (`compact`, `regular` o `large`), el tamaño del
contenido en píxeles y `px('font-md')` con el valor en píxeles de un token. El marco oculta el subtítulo y
después el título cuando el widget se vuelve bajo; el título sigue disponible para las tecnologías de
asistencia.

## El intervalo de tiempo del panel {#the-boards-time-range}

Un panel tiene un intervalo de tiempo que el espectador puede cambiar y pausar (consulta
[Paneles](../concepts/dashboards.md#time-range-and-pause)). Un widget `measurement` que declara
`binding.windowSeconds` dibuja un lapso de tiempo en lugar de un número de muestras: el lapso que fija
la opción referenciada, o el intervalo del panel cuando la opción no está definida. El anfitrión
precarga el historial del lapso y conserva como máximo `MAX_SPAN_SAMPLES` (2.000) muestras en vivo;
cuando tuvo que descartar muestras antiguas que seguían dentro del lapso, `data.truncatedBefore`
indica desde cuándo cubre realmente la ventana, y tu widget debería decirlo en lugar de etiquetar una
ventana corta con el lapso largo.

`useWidgetTimeRange()` devuelve el intervalo: `spanSeconds`, `paused` y, mientras está en pausa,
`pausedAt`. Es `undefined` cuando el widget se renderiza fuera de un panel, así que trata ese caso en
lugar de suponer una hora. `withinSpan(samples, spanSeconds, end)` descarta las muestras más antiguas
que el lapso. Mientras el panel está en pausa, el anfitrión mantiene todos los canales de datos salvo
`control` en la instantánea que hay en pantalla, así que tu widget no necesita lógica de pausa propia.

`formatValue(value)` sin precisión muestra como máximo 3 decimales, o 4 dígitos significativos por
debajo de 1; pasa una precisión para fijar el número de decimales.

## Interacciones y modelo de confianza {#interactions-and-the-trust-model}

`interactions` enumera los callbacks del anfitrión que usa el widget: `alarmActions` (reconocer y
cerrar; solo canal de alarmas), `commandSend` (solo canal de control), `selection` y `candidates`.
El anfitrión proporciona únicamente los callbacks que declaraste, limitados además a lo que el
anfitrión admite y a lo que el espectador tiene permitido. Un widget sin callback se comporta como
de solo lectura. El servidor sigue siendo la autoridad: una acción que el espectador no puede
realizar es rechazada por el servidor, y el rechazo llega a tu componente.

**Un paquete de widgets es código de confianza.** Se empaqueta en la aplicación anfitriona en tiempo
de compilación y se ejecuta en la página del anfitrión con los privilegios del anfitrión. Las
declaraciones de un descriptor dan forma a la API; no son una frontera de seguridad y no detienen
código malicioso en un paquete. El SDK no pasa a tu componente el token de acceso, el cliente ni el
hub sin procesar, pero nada impide que código de la misma página los alcance. Instala solo paquetes
que hayas revisado, de editores en los que confíes. El anfitrión envuelve cada widget en un límite de
error, de modo que un widget que lanza una excepción muestra un error en su propio marco y sus
vecinos siguen mostrándose. Eso no contiene un bucle infinito ni código hostil.

## A continuación {#next}

- [Añade el paquete a la consola y a la aplicación de paneles](./adding-widget-packages.md).
- [Cómo los widgets almacenados siguen siendo compatibles entre actualizaciones de paquetes](./widget-versions-and-compatibility.md).
