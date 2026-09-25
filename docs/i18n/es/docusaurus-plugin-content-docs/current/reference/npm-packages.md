---
sidebar_position: 3
title: Paquetes de npm
---

# Paquetes de npm

El runtime de paneles se publica en npm. Una aplicación React fuera de este repositorio puede
instalarlo y renderizar paneles de DeviceChain en vivo.

:::note Estado
Estos paquetes son **previos a 1.0**: los tipos, las props y las exportaciones pueden cambiar entre
versiones, así que lee las notas de la versión antes de actualizar. Son exclusivamente ESM y están
construidos para un bundler; consulta [Qué asumen estos paquetes](#qué-asumen-estos-paquetes).
:::

## Qué se publica

| Paquete | Qué es |
|---|---|
| [`@devicechain/client`](https://www.npmjs.com/package/@devicechain/client) | El SDK de TypeScript: gestión de tokens de autenticación, GraphQL sobre fetch, decodificación de JWT y suscripciones en vivo. Agnóstico del framework; sin React. |
| [`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards) | El `DashboardHub`, que es dueño de la conexión, resuelve selectores y multiplexa las suscripciones de telemetría. También los tipos de definición, selector, slot y manifiesto de vinculación. |
| [`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) | Los widgets de React y el renderizador que dispone el panel. |
| [`@devicechain/brand`](https://www.npmjs.com/package/@devicechain/brand) | Los tokens de marca y las hojas de estilo generadas. Opcional: los widgets se tematizan enteramente con propiedades personalizadas de CSS y no requieren nada de esto. |

```bash
npm install @devicechain/widgets @devicechain/dashboards @devicechain/client \
            graphql react react-dom
```

`@devicechain/dashboards` y `@devicechain/client` son **dependencias peer** de los widgets, fijadas a
exactamente la misma versión. npm 7 y posteriores instalan las peers por ti, así que esa línea de
instalación es todo lo que necesitas.

La fijación es deliberada: impide que npm anide en silencio una segunda copia del SDK bajo los
widgets. Dos copias serían peor que un error. El runtime distingue «no tienes permiso para ver esto»
de «se cayó la conexión» por la identidad de la clase de error que captura, y un error lanzado por
una copia no es una instancia de la clase de la otra. Un rechazo por permisos se leería entonces como
una caída del servicio.

## Versiones y dist-tags

Los cuatro paquetes se publican juntos, en una sola versión, desde la misma release que corta la
plataforma. La versión `0.14.0` de los widgets va con la versión `0.14.0` del SDK; no hay un
versionado independiente por paquete que tengas que razonar.

| Tag | Apunta a |
|---|---|
| `latest` | La release **estable** más reciente. Es lo que resuelve un `npm install` sin más. |
| `next` | La **prerelease** más reciente (`-rc.1` y compañía). Actívala explícitamente con `npm install @devicechain/widgets@next`. |

Toda versión publicada por el workflow de release lleva
[procedencia (provenance) de npm](https://docs.npmjs.com/generating-provenance-statements): una
declaración firmada y verificable públicamente de qué repositorio, workflow y commit la construyeron.
npmjs.com la muestra en la página del paquete. Todas las versiones desde `0.14.0` en adelante la
llevan.

La excepción son las versiones de arranque `0.14.0-0`. Se publicaron a mano para crear los paquetes
en primer lugar, y son anteriores al workflow que las firma.

## Renderizar un panel

```tsx
import { DashboardRenderer } from '@devicechain/widgets';
import { DashboardHub, parseDashboardDefinition } from '@devicechain/dashboards';

const hub = new DashboardHub({ resolver, authorities: user.scopes });
const definition = parseDashboardDefinition(json);

<DashboardRenderer definition={definition} hub={hub} actions={hub} />;
```

Omite `actions` para un montaje estrictamente de solo lectura, y los widgets que actúan se renderizan
inertes:

- La tabla de alarmas omite por completo sus controles de reconocer y despejar.
- El widget de comandos mantiene en pantalla su formulario de parámetros y el botón Send, pero
  deshabilitados, con un aviso de que el visor no tiene permiso para emitir comandos.

Esa es toda la activación explícita, y es una segunda capa por encima de las comprobaciones del
servidor. Un visor que nunca recibe `actions` no puede escribir desde el panel, contenga lo que
contenga, y el servidor hace valer `alarm:write` y `command:write` en cualquier caso.

Consulta [Paneles](../concepts/dashboards.md) para saber qué es una definición, cómo los slots y los
manifiestos de vinculación permiten que una definición sirva a muchos dispositivos, y cómo los usa el
visor independiente `/dash`.

## Renderizar un mapa {#map-host-wiring}

El widget de mapa necesita una cosa de ti, y si la omites el mapa falla en silencio.

MapLibre analiza las teselas vectoriales en un web worker, y deriva la URL de ese worker en tiempo de
ejecución a partir de la URL de su propio módulo. Es una cadena calculada que ningún bundler puede
rastrear, así que:

1. Ningún bundler emite el archivo del worker.
2. El navegador responde 404.
3. `new Worker()` no lanza excepción.
4. El mapa se renderiza como una caja vacía sin nada en la consola.

Solo tu bundler puede emitir ese archivo, y por eso la URL tiene que venir de ti.

### Vite {#vite}

En Vite es un solo import:

```tsx
import { MapRuntimeProvider } from '@devicechain/widgets';
import { viteMapRuntime } from '@devicechain/widgets/vite';

<MapRuntimeProvider runtime={viteMapRuntime}>
  <DashboardRenderer definition={definition} hub={hub} />
</MapRuntimeProvider>;
```

### Otros bundlers {#other-bundlers}

En cualquier otro bundler suministras la URL tú mismo. Hay un requisito, y es toda la dificultad:
MapLibre carga esa URL como un **module worker**, así que lo que sirvas ahí tiene que ser un módulo
sin imports pendientes de resolver. `maplibre-gl/dist/maplibre-gl-worker.mjs` no lo es por sí solo:
su primera línea importa un archivo hermano, `maplibre-gl-shared.mjs`.

:::danger No apuntes a una copia suelta del archivo del worker
En particular, no uses `new URL('maplibre-gl/dist/maplibre-gl-worker.mjs', import.meta.url)`.
Webpack copia ese único archivo como un activo y nunca emite el hermano, así que el worker muere en
su propia primera línea. Obtienes un 200 en la URL del worker, un canvas en pantalla, marcadores
colocados, una compilación que terminó con 0 y nada en la consola, y ningún mapa en el mapa.
:::

Hay dos recetas que funcionan. En **webpack**, dale al worker su propio entry, para que webpack
empaquete dentro de él lo que importa:

```js
entry: {
  main: './src/index.tsx',
  'maplibre-worker': 'maplibre-gl/dist/maplibre-gl-worker.mjs',
},
output: {
  filename: (data) =>
    data.chunk.name === 'maplibre-worker' ? 'maplibre-worker.js' : '[name].[contenthash].js',
},
```

Luego apunta al nombre de archivo que elegiste:

```tsx
const runtime = { workerUrl: '/maplibre-worker.js' };
```

O, en **cualquier bundler**, copia tú mismo los dos archivos a un mismo directorio servido:

```tsx
//   maplibre-gl/dist/maplibre-gl-worker.mjs  ->  public/vendor/
//   maplibre-gl/dist/maplibre-gl-shared.mjs  ->  public/vendor/
const runtime = {
  workerUrl: '/vendor/maplibre-gl-worker.mjs',
  loadStyles: () => import('maplibre-gl/dist/maplibre-gl.css'),
};
```

`loadStyles` es opcional; puedes importar tú mismo la hoja de estilos de MapLibre de forma
anticipada. Suministrarla aquí mantiene los 83 KB (10,7 KB comprimidos) en el chunk perezoso del
mapa, de modo que un visor que nunca abre un mapa no descarga nada de eso.

Un widget de mapa sin proveedor por encima renderiza un aviso visible, no un canvas en blanco. Es
deliberado, para que un anfitrión sin cablear pueda ver qué falta en lugar de tener que adivinarlo.

### `maplibre-gl` es una dependencia peer

Decláralo en tu propia aplicación, junto a los widgets:

```bash
npm install maplibre-gl
```

Hay dos razones:

- **Propiedad.** La URL del worker la emites tú, así que la librería también tiene que ser tuya.
- **Desfase de versiones.** Si tu aplicación suministrara el worker mientras los widgets resolvieran
  MapLibre desde una copia propia, un worker construido con una versión gobernaría un hilo principal
  de otra. El resultado es el mismo mapa en blanco, por otro camino. Una dependencia peer garantiza
  una única copia compartida.

Si tu aplicación no lo declara, el hoisting de npm deja que el import se resuelva de todos modos.
Seguirá funcionando hasta que alguien instale con el modo estricto de pnpm o una disposición aislada
similar, donde una dependencia que nadie declaró es una dependencia que nadie obtiene.

## Teselas de mapa base

`TenantBasemapProvider` suministra la fuente de teselas, y es opcional: sin proveedor, el mapa recae
en una vista simple. La cascada de resolución vive en `@devicechain/client`, de modo que toda
superficie de DeviceChain resuelve del mismo modo una anulación por usuario sobre el valor por
defecto del inquilino. Consulta [Mapas base](../concepts/basemaps.md).

## Tematización

Todo color, radio y tipografía que usan los widgets viene de una propiedad personalizada de CSS, así
que los reestilizas fijando variables en cualquier elemento ancestro. No hay Tailwind, ni hoja de
estilos global, ni opinión alguna sobre el armazón de tu aplicación. Los valores de DeviceChain se
publican como `@devicechain/brand`; nada los exige.

## Qué asumen estos paquetes

| | |
|---|---|
| **Formato de módulo** | Solo ESM. No hay build de CommonJS. |
| **React** | 19. |
| **Bundler** | Vite o webpack. |
| **Resolución de TypeScript** | Las declaraciones emitidas usan especificadores sin extensión, que resuelven bajo `moduleResolution: "bundler"` y no bajo `node16`/`nodenext`. |

Esas son las combinaciones soportadas, y la línea entre «soportado» y «probablemente funcione» está
trazada a propósito. Se prueban en un navegador real contra tres aplicaciones construidas fuera de
este repositorio a partir de los tarballs empaquetados:

- una sobre Vite,
- una sobre webpack,
- una sobre la receta de copiar el worker de más arriba, también construida con webpack.

Cada una renderiza el mapa y se comprueba por las teselas que realmente pidió y los marcadores que
realmente colocó. «Se renderizó» no basta, porque es exactamente la afirmación que supera el modo de
fallo del widget de mapa.

Nada de eso ejercita consumidores de Rollup ni de esbuild, Next.js ni React Server Components,
consumidores de CommonJS, ni la resolución `node16`/`nodenext`. Rollup y esbuild consumen el mismo
ESM plano y se espera que funcionen, pero quedan del lado de «probablemente funcione» de la línea. El
resto está sin probar, no se sabe que esté roto. Esta página lo dirá cuando eso cambie.

## Compilar contra el workspace {#building-against-the-workspace-instead}

Todo lo anterior describe instalar desde el registro. Dentro de este repositorio los paquetes son
además la fuente de verdad: la consola y el visor `/dash` compilan ambos contra el mismo `dist` que
descarga un consumidor, no contra las fuentes TypeScript, así que una rotura en el artefacto
publicado rompe también esas aplicaciones. Si estás trabajando en DeviceChain mismo, consulta la
[guía de desarrollo local](../guides/local-development.md).
