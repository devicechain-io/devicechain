---
sidebar_position: 17
title: Mapas base
---

# Mapas base

Todas las superficies con mapa de DeviceChain —el editor de geocercas, el widget de mapa de los paneles y un panel incrustado a través del visor independiente— dibujan sus posiciones sobre un **mapa base**: mosaicos ráster obtenidos del proveedor que elijas.

Una instancia nueva dibuja mapas desde el primer momento: el valor por defecto es la capa de mosaicos estándar de [OpenStreetMap](https://www.openstreetmap.org/), que no necesita cuenta. Nada se adopta en silencio —el valor por defecto tiene nombre, es visible en **Configuración** y se sustituye con una sola edición— y todo nivel que pueda definir un origen de mosaicos debe aportar la línea de crédito que exige la licencia de ese proveedor.

Si es el proveedor adecuado para ti es otra cuestión, y conviene planteársela antes de pasar a producción. Consulta [Elegir un proveedor](#choosing-a-provider).

## El mapa base pertenece al inquilino {#the-basemap-belongs-to-the-tenant}

El mapa base se configura **por inquilino**, en la consola bajo **Configuración → Mapa**, por alguien que tenga la autoridad `basemap:write`.

Elige un proveedor de la lista y DeviceChain rellena su plantilla de mosaicos y la línea de crédito que exige su licencia. Ambas siguen siendo editables debajo, así que un proveedor que no esté en la lista —un servidor de mosaicos interno, por ejemplo— sigue siendo cuestión de escribir los dos campos directamente; consulta [Elegir un proveedor](#choosing-a-provider).

Esa ubicación es lo importante. Una URL de mosaicos suele llevar una clave de API, y que la clave pertenezca al inquilino significa que su propia cuenta de mapas se factura, se limita, se restringe y se revoca por separado, sin tocar a nadie más de la instancia. Un inquilino que agote su cuota no puede dejar sin mapas a otro, y un inquilino que ya tenga contrato con un proveedor puede usarlo.

`basemap:write` está deliberadamente **separada de `branding:write`**, aunque ambas definen el aspecto de la consola de un inquilino. Unirlas haría que cada concesión implicara la otra en ambos sentidos: quien cambia el logotipo podría leer la clave del mapa, y quien configura los mapas podría rediseñar la consola.

## De dónde procede un valor {#where-a-value-comes-from}

Tres niveles, del más específico al más general:

| Nivel | Lo define | Dónde |
| --- | --- | --- |
| Anulación por superficie | Quien edite esa superficie | Las opciones del propio widget de mapa; los campos de mapa base del editor de geocercas, recordados en tu navegador |
| **Inquilino** | Un administrador del inquilino (`basemap:write`) | Consola → **Configuración** → **Mapa** |
| Valor por defecto de la instancia | Un operador (`settings:write`) | Consola de administración → **Ajustes** → `basemap.default` |

Cada nivel rellena lo que el anterior deja en blanco, de modo que una implantación de un solo inquilino puede definir el valor una vez a nivel de instancia y no volver a pensar en ello.

Los niveles por superficie no desaparecen: son la forma de probar un proveedor en un solo panel, o en tu propio navegador, antes de aplicarlo a todo el mundo.

### El origen de mosaicos se mueve como un único valor {#the-tile-source-moves-as-one-value}

Una URL de mosaicos y la atribución que exige su licencia son **un único valor, no dos**. Se validan juntas —ninguna puede guardarse sin la otra— y se heredan juntas.

Esa segunda parte es la que conviene conocer. Si tu inquilino define su propia URL de mosaicos y deja la atribución en blanco, **no** conserva en silencio la línea de crédito del valor por defecto de la instancia: mostrar los mosaicos de un proveedor bajo el crédito de otro es una infracción de licencia, así que la cascada no fabricará una. En los niveles de inquilino y de instancia el guardado se rechaza antes de llegar a eso.

Los niveles **por superficie** se definen en el navegador y nunca pasan por esa validación, así que aplican la misma regla en el punto de uso: una opción de widget o un campo del editor de geocercas que indique una URL de mosaicos sin línea de crédito se **ignora por completo**, y el mapa recurre al mapa base del inquilino, que sí está acreditado. El editor de geocercas lo indica cuando ocurre. Una anulación a medias se descarta en lugar de aplicarse a medias, porque la alternativa es dibujar los mosaicos de un proveedor sin ningún crédito.

La vista inicial (`centerLat` / `centerLon` / `zoom`) no tiene esa restricción y se hereda campo a campo, de modo que un inquilino puede cambiar el zoom sin volver a declarar un proveedor para conservarlo.

### La vista inicial es un recurso alternativo, nunca una anulación {#the-starting-view-is-a-fallback}

El centro y el zoom se aplican solo cuando un mapa **no tiene nada propio a lo que ajustarse**. Una geocerca que ya tiene forma se abre sobre esa forma; un widget de mapa con marcadores se ajusta a sus marcadores. Editar una geocerca en Roma desde un inquilino centrado en Atlanta se abre sobre Roma.

## Qué aspecto debe tener una URL de mosaicos {#what-a-tile-url-must-look-like}

El guardado falla de forma cerrada, y cada regla rechaza un valor que si no fallaría en silencio más adelante:

- **Solo `https`.** Una consola servida por HTTPS bloquea los mosaicos obtenidos por HTTP como contenido mixto, así que un origen `http://` queda almacenado pero no puede representarse. Si tienes un servidor de mosaicos interno en HTTP simple, ponlo detrás de TLS.
- **Debe ser una plantilla.** La URL necesita `{z}`, `{x}` e `{y}` —o `{bbox-epsg-3857}`, o `{quadkey}`—. Sin un marcador, todos los mosaicos del mapa solicitan la misma imagen, que es la forma de los dos errores de copiado más habituales: la URL de un único mosaico y la URL de un JSON de estilo.
- **Solo se admiten los marcadores que el renderizador conoce** —`{prefix}`, `{z}`, `{x}`, `{y}`, `{ratio}`, `{bbox-epsg-3857}` y `{quadkey}`—. Cualquier otra cosa entre llaves se envía al proveedor como texto literal. Esto detecta la copia más habitual de todas: una URL escrita para Leaflet, que lleva un marcador de subdominio `{s}` que el renderizador de DeviceChain no sustituye. Sustitúyelo por un único subdominio —`a.tile.example.com` en lugar de `{s}.tile.example.com`—, que además es lo que recomienda la práctica actual.
- **La atribución es obligatoria, y su marcado es limitado**: texto plano más enlaces escritos exactamente como `<a href="https://…">texto</a>`. Se permiten enlaces porque varias licencias de proveedores exigen que el crédito enlace a su página de derechos de autor; todo lo demás se rechaza.

Hoy solo se admiten mosaicos **ráster**. No se acepta una URL de estilo vectorial.

## Elegir un proveedor {#choosing-a-provider}

El valor por defecto te da un mapa que funciona desde el primer día. No es automáticamente la respuesta correcta para un despliegue en producción, y el factor decisivo suele ser **quién se espera que sirva tu tráfico**.

La lista **Proveedor** incluye un conjunto de proveedores cuya plantilla de mosaicos y cuya línea de crédito obligatoria se han contrastado con la documentación del propio proveedor. Al elegir uno se rellenan ambos campos; cuando un proveedor necesita una clave de API, esta tiene su propio campo y se compone dentro de la URL por ti, de modo que la clave puede rotarse después sin volver a pegar la plantilla.

Dos cosas que la lista deliberadamente no hace:

- **No describe los términos de nadie.** Cada entrada enlaza a la página de términos y precios del propio proveedor. Si un nivel es gratuito, si necesita cuenta o si tiene un límite de uso son afirmaciones que pueden cambiar en la web de otra persona sin que nos enteremos, así que la lista apunta a la fuente en lugar de resumirla. Léela antes de depender de un proveedor.
- **No es exhaustiva, y eso es un listón deliberado, no una lista de pendientes.** Un proveedor solo aparece cuando su línea de crédito obligatoria está publicada por el propio proveedor. Una entrada con una línea de crédito *incorrecta* es peor que una entrada ausente: enviaría una infracción de licencia ya rellenada y con apariencia de fiable, justo en el lugar donde el usuario tiene derecho a suponer que lo hemos hecho bien. Si falta tu proveedor, elige **Personalizado…** e introduce los dos campos tú mismo.

Elegir **Personalizado…** nunca altera lo que ya hay en los campos: significa «esto lo escribo yo», que es exactamente cuando sobrescribir sería más destructivo.

Los servidores de mosaicos de OpenStreetMap los gestiona una organización sin ánimo de lucro y se financian con donaciones. Su [política de uso de mosaicos](https://operations.osmfoundation.org/policies/tiles/) detalla lo que te piden, y DeviceChain está construido para cumplirla: los mosaicos se obtienen solo a medida que navegas, nunca se descargan por adelantado ni se archivan, y la línea de crédito siempre se muestra. Dos cosas siguen siendo responsabilidad tuya:

- **No pongas una `Referrer-Policy` restrictiva delante de la consola.** La política pide a los clientes de navegador un `Referer` válido, y eliminarlo puede hacer que bloqueen una instancia sin aviso previo y sin más síntoma local que un mapa que ha dejado de dibujarse.
- **Lee la política antes de crecer.** Se reservan el derecho de bloquear el acceso sin previo aviso cuando el uso degrada el servicio: algo razonable viniendo de una infraestructura donada, y muy inoportuno de descubrir durante una demostración a un cliente.

Si los mapas son importantes para tu operación, apunta un inquilino —o el valor por defecto de la instancia— a un proveedor con el que tengas una relación. Ese es el caso para el que existe el nivel por inquilino, y la sección siguiente sobre claves de API es la que conviene leer a continuación.

:::tip Sin fuente de teselas obtienes un mundo esquemático, no uno en blanco
Si un operador define el valor por defecto de la instancia como `{}` y un inquilino no define nada, no hay ningún origen de mosaicos. Ten en cuenta que **Restablecer el valor por defecto** hace lo contrario —restaura el proveedor que viene de fábrica—, así que desactivar los mapas es un `{}` explícito, no un restablecimiento.

Las superficies con mapa recurren entonces a un **mapa base incorporado**: contornos de tierra y fronteras de países de Natural Earth, de dominio público, compilados dentro de la propia aplicación. No solicita nada a ningún servidor externo —todo lo que necesita lo sirve el propio DeviceChain—, y eso es justo lo que lo convierte en la respuesta correcta tanto para una instalación aislada de la red como para un operador que ha desactivado los proveedores a propósito.

Es esquemático, y lo es con honestidad: continentes y fronteras, nada con detalle de calle. Por eso se lee como «configura un proveedor» y no como «esto está roto». Todo lo demás sigue funcionando igual que sobre mosaicos: dibujar una geocerca funciona y las coordenadas que colocas siguen siendo exactas, porque la proyección es la misma que usa un mapa con mosaicos.
:::

## La clave de API de la URL de mosaicos no es un secreto {#the-api-key-is-not-a-secret}

:::warning Es visible para cualquiera que use el inquilino
Si la URL de mosaicos de tu proveedor lleva una clave de API, **esa clave llega al navegador**: tiene que hacerlo, porque el navegador es quien obtiene los mosaicos. Se almacena como configuración normal, no en el almacén de secretos, porque un valor que el cliente debe leer no puede ocultarse al cliente. Su propio campo **Clave de API** existe para colocar la clave en el lugar correcto de la plantilla, no para protegerla.

Protégela como esperan los proveedores de mapas: con **restricciones de referente HTTP** (y, cuando existan, cuotas por clave y restricciones de API) en la consola del propio proveedor, limitadas al nombre de host desde el que se sirve tu consola. Ese es el control que de verdad limita el abuso de una clave así. Trata la rotación como algo rutinario y usa una clave distinta por inquilino, para que revocar una no afecte a nadie más.
:::

## Paneles incrustados {#embedded-dashboards}

El visor de paneles independiente inicia sesión como su propio usuario y lee el mismo mapa base del inquilino, así que un panel incrustado allí se dibuja sobre los mismos mosaicos que en la consola. Si un panel incrustado muestra un mapa base distinto del de la consola —y de forma especialmente reveladora, el mundo esquemático incorporado donde esperabas mosaicos—, comprueba que el visor inició sesión **como miembro del mismo inquilino**. El mapa base sigue al inquilino, no al panel.
