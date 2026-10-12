---
sidebar_position: 10
title: Añadir paquetes de widgets
---

# Añadir paquetes de widgets a la consola y a /dash {#adding-widget-packages-to-the-console-and-dash}

Un paquete de widgets se empaqueta en la aplicación que lo muestra. Añadir uno es un cambio en
tiempo de compilación: añades la dependencia, incluyes sus definiciones en el registro de la
aplicación y vuelves a compilar. No hay instalador en tiempo de ejecución ni carga de archivos. Un
panel exportado es JSON portable y no lleva la implementación de un widget.

Un paquete registrado en una aplicación no está instalado en ninguna otra. La consola de edición y
cada aplicación que muestre un panel que use el widget deben incluir el mismo paquete, o el widget
aparece allí como marcador de posición. Consulta
[versiones y compatibilidad de widgets](./widget-versions-and-compatibility.md).

## En tu propia aplicación {#in-your-own-application}

Si incrustas paneles con `@devicechain/dashboards` y `@devicechain/widgets`, construye un registro a
partir de las definiciones integradas más las tuyas y entrega el mismo registro al analizador y al
renderizador:

```tsx
import { parseDashboardDefinition } from '@devicechain/dashboards';
import { createWidgetRegistry } from '@devicechain/widget-sdk';
import { builtinWidgetDefinitions, DashboardRenderer } from '@devicechain/widgets';
import { tankLevelWidgets } from '@acme/devicechain-tank-level';

const registry = createWidgetRegistry([...builtinWidgetDefinitions, ...tankLevelWidgets]);
const definition = parseDashboardDefinition(rawDefinition, { catalog: registry.catalog });

return <DashboardRenderer definition={definition} hub={hub} registry={registry} />;
```

Analizar una definición con un catálogo que no contiene tus widgets y luego renderizar con un
registro que sí los tiene no funciona: el analizador ya habrá convertido el widget en un marcador de
posición. Pasa el mismo registro a ambos.

`createWidgetRegistry` valida y sella el registro. Rechaza ids duplicados (incluido el intento de
reutilizar un id integrado), descriptores mal formados, un `sdkApiVersion` no admitido y valores por
defecto o enlaces no válidos, y los informa todos en un único error. No existe registro global, y dos
registros en una misma página son independientes. Un registro contiene una versión exacta de cada id.

## En la consola de DeviceChain y /dash {#in-the-devicechain-console-and-dash}

La consola, el visor `/dash` y el reproductor de grabaciones (board-replay) construyen su registro a
partir de un único módulo de composición compartido en el árbol de código fuente del frontend,
`frontend/apps/host-widgets`. Ninguno mantiene una lista propia, así que un paquete añadido allí
llega a todos a la vez: al analizador de definiciones, a los validadores de opciones, al
renderizador, al menú Añadir y al panel de ajustes del editor, a la vista previa sintética y al
reproductor de grabaciones.

Para instalar un paquete de widgets:

1. Añade el paquete a las dependencias de `frontend/apps/host-widgets/package.json` y ejecuta
   `npm install` en `frontend/`.
2. En `frontend/apps/host-widgets/src/extensions.ts`, importa las definiciones del paquete y
   enuméralas en `extensionWidgets`:

   ```ts
   import type { ErasedWidgetDefinition } from '@devicechain/widget-sdk';
   import { tankLevelWidgets } from '@acme/devicechain-tank-level';

   export const extensionWidgets: readonly ErasedWidgetDefinition[] = [...tankLevelWidgets];
   ```

3. Vuelve a compilar. `npm run build` en `frontend/` compila la consola, `/dash` y el reproductor de
   grabaciones. La consola y `/dash` se distribuyen juntas en la imagen de contenedor del frontend
   que se construye con `frontend/Dockerfile`; construye esa imagen y despliégala en lugar de la
   estándar (los valores `frontend.image` del chart de Helm). Si sirves el reproductor de
   grabaciones, despliega también su paquete recompilado.

Despliega todas las aplicaciones a la vez. Un panel creado en la consola con tu widget muestra un
marcador de posición en una compilación de `/dash` o del reproductor que no incluya el paquete.

Si la composición no es válida, por ejemplo porque dos paquetes reclaman el mismo id o un
descriptor está mal formado, el registro lanza un único error que enumera todos los fallos y la
aplicación falla al cargarse, de modo que un paquete roto aparece en la compilación que pruebas y no
widget a widget en producción.

Esta lista es código de confianza compilado en la aplicación. No es un instalador en tiempo de
ejecución, y un panel no puede añadirle nada.

## Publicar mientras un widget no está resuelto {#publishing-while-a-widget-is-unresolved}

Un widget cuyo paquete falta, o cuya versión de ajustes almacenada no puede leer el paquete
instalado, se analiza como un marcador de posición. En la consola aún puedes abrir el panel, mover y
redimensionar el marcador de posición y guardar el borrador; su JSON almacenado se conserva
exactamente. Publicar una versión nueva queda bloqueado mientras haya algún widget sin resolver, y el
panel del historial de versiones nombra cada uno y el motivo. Instala el paquete, o elimina el
widget, y después publica.

El bloqueo está en la consola. El servidor almacena las definiciones de paneles como JSON opaco y no
hace esta comprobación.

## Comprueba un paquete antes de distribuirlo {#check-a-package-before-you-ship-it}

Compílalo contra artefactos empaquetados, como recibe el SDK un consumidor, no contra el monorepo. El
script `hack/widget-package-proof.sh all` del repositorio hace esto con el paquete de ejemplo:
empaqueta el SDK, instala los tarballs en una copia fuera del repositorio, la compila y comprueba que
el paquete no tiene importaciones privadas ni del monorepo, que resuelve exactamente una copia del
SDK y que su entrada solo DOM no incluye código de ECharts, MapLibre ni de widgets integrados.
Después introduce una importación de gráficos en la entrada solo DOM y exige que la comprobación
falle, para detectar una comprobación que no ve. La CI lo ejecuta en cada cambio; necesita Node, npm,
un frontend instalado (`npm ci` en `frontend/`) y acceso al registro de npm. En Windows, ejecútalo con
Git Bash. Aplica las mismas comprobaciones a tu propio paquete.
