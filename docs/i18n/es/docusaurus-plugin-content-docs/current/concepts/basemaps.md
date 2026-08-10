---
sidebar_position: 17
title: Mapas base
---

# Mapas base

Todas las superficies con mapa de DeviceChain —el editor de geocercas, el widget de mapa de los paneles y un panel incrustado a través del visor independiente— dibujan sus posiciones sobre un **mapa base**: mosaicos ráster obtenidos del proveedor que elijas.

DeviceChain no incluye **ningún proveedor de mosaicos por defecto**, y eso es una decisión, no un olvido. Los mosaicos de mapa llevan condiciones de licencia, políticas de uso y, a menudo, una factura, y la plataforma no puede aceptar ninguna de esas cosas en tu nombre. Una instancia que nadie ha configurado muestra todas las posiciones que se le pidieron, sobre un panel liso, y explica por qué no hay mapa detrás.

## El mapa base pertenece al inquilino {#the-basemap-belongs-to-the-tenant}

El mapa base se configura **por inquilino**, en la consola bajo **Mapa base**, por alguien que tenga la autoridad `basemap:write`.

Esa ubicación es lo importante. Una URL de mosaicos suele llevar una clave de API, y que la clave pertenezca al inquilino significa que su propia cuenta de mapas se factura, se limita, se restringe y se revoca por separado, sin tocar a nadie más de la instancia. Un inquilino que agote su cuota no puede dejar sin mapas a otro, y un inquilino que ya tenga contrato con un proveedor puede usarlo.

`basemap:write` está deliberadamente **separada de `branding:write`**, aunque ambas definen el aspecto de la consola de un inquilino. Unirlas haría que cada concesión implicara la otra en ambos sentidos: quien cambia el logotipo podría leer la clave del mapa, y quien configura los mapas podría rediseñar la consola.

## De dónde procede un valor {#where-a-value-comes-from}

Tres niveles, del más específico al más general:

| Nivel | Lo define | Dónde |
| --- | --- | --- |
| Anulación por superficie | Quien edite esa superficie | Las opciones del propio widget de mapa; los campos de mapa base del editor de geocercas, recordados en tu navegador |
| **Inquilino** | Un administrador del inquilino (`basemap:write`) | Consola → **Mapa base** |
| Valor por defecto de la instancia | Un operador (`settings:write`) | Consola de administración → **Ajustes** → `basemap.default` |

Cada nivel rellena lo que el anterior deja en blanco, de modo que una implantación de un solo inquilino puede definir el valor una vez a nivel de instancia y no volver a pensar en ello.

Los niveles por superficie no desaparecen: son la forma de probar un proveedor en un solo panel, o en tu propio navegador, antes de aplicarlo a todo el mundo.

### El origen de mosaicos se mueve como un único valor {#the-tile-source-moves-as-one-value}

Una URL de mosaicos y la atribución que exige su licencia son **un único valor, no dos**. Se validan juntas —ninguna puede guardarse sin la otra— y se heredan juntas.

Esa segunda parte es la que conviene conocer. Si tu inquilino define su propia URL de mosaicos y deja la atribución en blanco, **no** conserva en silencio la línea de crédito del valor por defecto de la instancia: se resuelve como «sin atribución», y de hecho el guardado se rechaza antes de llegar a eso. Mostrar los mosaicos de un proveedor bajo el crédito de otro es una infracción de licencia, así que la cascada no fabricará una.

La vista inicial (`centerLat` / `centerLon` / `zoom`) no tiene esa restricción y se hereda campo a campo, de modo que un inquilino puede cambiar el zoom sin volver a declarar un proveedor para conservarlo.

### La vista inicial es un recurso alternativo, nunca una anulación {#the-starting-view-is-a-fallback}

El centro y el zoom se aplican solo cuando un mapa **no tiene nada propio a lo que ajustarse**. Una geocerca que ya tiene forma se abre sobre esa forma; un widget de mapa con marcadores se ajusta a sus marcadores. Editar una geocerca en Roma desde un inquilino centrado en Atlanta se abre sobre Roma.

## Qué aspecto debe tener una URL de mosaicos {#what-a-tile-url-must-look-like}

El guardado falla de forma cerrada, y cada regla rechaza un valor que si no fallaría en silencio más adelante:

- **Solo `https`.** Una consola servida por HTTPS bloquea los mosaicos obtenidos por HTTP como contenido mixto, así que un origen `http://` queda almacenado pero no puede representarse. Si tienes un servidor de mosaicos interno en HTTP simple, ponlo detrás de TLS.
- **Debe ser una plantilla.** La URL necesita `{z}`, `{x}` e `{y}` —o `{bbox-epsg-3857}`, o `{quadkey}`—. Sin un marcador, todos los mosaicos del mapa solicitan la misma imagen, que es la forma de los dos errores de copiado más habituales: la URL de un único mosaico y la URL de un JSON de estilo.
- **La atribución es obligatoria, y su marcado es limitado**: texto plano más enlaces escritos exactamente como `<a href="https://…">texto</a>`. Se permiten enlaces porque varias licencias de proveedores exigen que el crédito enlace a su página de derechos de autor; todo lo demás se rechaza.

Hoy solo se admiten mosaicos **ráster**. No se acepta una URL de estilo vectorial.

## La clave de API de la URL de mosaicos no es un secreto {#the-api-key-is-not-a-secret}

:::warning Es visible para cualquiera que use el inquilino
Si la URL de mosaicos de tu proveedor lleva una clave de API, **esa clave llega al navegador**: tiene que hacerlo, porque el navegador es quien obtiene los mosaicos. Se almacena como configuración normal, no en el almacén de secretos, porque un valor que el cliente debe leer no puede ocultarse al cliente.

Protégela como esperan los proveedores de mapas: con **restricciones de referente HTTP** (y, cuando existan, cuotas por clave y restricciones de API) en la consola del propio proveedor, limitadas al nombre de host desde el que se sirve tu consola. Ese es el control que de verdad limita el abuso de una clave así. Trata la rotación como algo rutinario y usa una clave distinta por inquilino, para que revocar una no afecte a nadie más.
:::

## Paneles incrustados {#embedded-dashboards}

El visor de paneles independiente inicia sesión como su propio usuario y lee el mismo mapa base del inquilino, así que un panel incrustado allí se dibuja sobre los mismos mosaicos que en la consola. Si un mapa aparece en blanco al incrustarlo pero funciona en la consola, comprueba que el visor inició sesión **como miembro del mismo inquilino**: el mapa base sigue al inquilino, no al panel.
