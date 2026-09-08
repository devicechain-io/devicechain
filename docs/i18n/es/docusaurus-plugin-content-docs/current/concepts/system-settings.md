---
title: Ajustes del sistema
description: Los ajustes de toda la instancia que edita un operador, qué acepta cada uno y los límites a los que está sujeta toda escritura de ajustes.
---

# Ajustes del sistema

Un **ajuste del sistema** es un valor de toda la instancia que un operador establece una sola vez,
para todos los inquilinos. Hay cuatro, viven en **Ajustes** dentro de la consola de administración, y
cada uno se sitúa *por debajo* de lo que cada inquilino configure para sí mismo — un inquilino que
no configura nada obtiene el valor por defecto de la instancia, y uno que establece el suyo propio
nunca lo ve.

| Clave | Qué decide | Se cubre en |
| --- | --- | --- |
| `basemap.default` | Los teselados de mapa con los que arranca cada inquilino | [Mapas base](./basemaps.md) |
| `branding.default` | El título, el logotipo y la paleta de la instancia | [Marca blanca](./white-labeling.md) |
| `entity.token_masks` | La forma de cada token que acuña la consola | más abajo |
| `locale.default` | El idioma en el que se abre la consola | más abajo |

Leer un ajuste no requiere más autoridad que haber iniciado sesión. Escribir uno requiere
`settings:write`, que es una autoridad de nivel operador y no forma parte de ningún rol de
inquilino.

## A qué está sujeta toda escritura de ajustes {#settings-write-rules}

Tres reglas se aplican a las cuatro claves, en este orden:

1. **El valor debe ocupar menos de 64 KB.** Por encima de eso la escritura se rechaza indicando el
   recuento de bytes. Esto acota el documento JSON entero, no un campo concreto dentro de él — lo
   que importa sobre todo en `branding.default`, donde un logotipo `data:` incrustado podría ser
   mucho mayor. El [registro de marca](./white-labeling.md) admite un logotipo incrustado de 256 KB
   *en un inquilino*, donde se guarda como una columna con tipo y no como un ajuste; en el nivel de
   instancia se aplica en su lugar el límite de 64 KB del documento, que equivale a unos 48 KB de
   imagen. La consola le dirige a una URL `https` en este nivel exactamente por eso.
2. **El valor debe ser JSON válido.**
3. **La clave debe ser una de las cuatro anteriores.** El vocabulario es cerrado: escribir una clave
   no reconocida se rechaza en lugar de crear un ajuste. No hay forma de añadir uno desde la API.

Después cada clave aplica su propia validación, que describen las páginas enlazadas en la tabla.

## Máscaras de token {#token-masks}

`entity.token_masks` decide el token que precarga cada formulario de **creación** de la consola.
Toda entidad se direcciona mediante un token, y escribir uno a mano para cada dispositivo nuevo es
tedioso y fácil de equivocar, así que la consola genera uno a partir de una plantilla y le deja
editarlo antes de guardar.

El ajuste es un mapa de tipo de entidad a plantilla. La clave `default` se aplica a cualquier tipo
de entidad que no tenga entrada propia:

```json
{
  "default": "{slug}",
  "device": "dev-{alphanumeric-8}",
  "area": "area-{slug}"
}
```

Una plantilla es texto literal más marcadores:

| Marcador | Produce |
| --- | --- |
| `{slug}` | Un slug del nombre que se está escribiendo — así, llamar a un dispositivo «Cold Store Probe» sugiere `cold-store-probe` |
| `{uuid}` | Un UUID |
| `{alphanumeric-N}` | `N` letras y dígitos aleatorios |
| `{numeric-N}` | `N` dígitos aleatorios |

El valor por defecto que se entrega es `{"default": "{slug}"}`.

Lo que una máscara produzca sigue teniendo que satisfacer la [gramática de
tokens](../reference/graphql-api.md#what-a-token-may-contain), que es lo que hace imposibles
algunas plantillas. Una máscara se rechaza si:

- está **vacía**
- usa un **marcador desconocido** — `dev-{sulg}` generaría en silencio `dev-` para todas las
  entidades, porque un marcador no reconocido no produce nada
- **no tiene ningún marcador** — a cada entidad se le daría el token idéntico, así que la primera
  creación funciona y todas las siguientes colisionan
- declara una **anchura mayor de 128 caracteres**, que nunca podría acuñar un token válido
- genera una **muestra que no pasa la gramática de tokens** — `my.device-{slug}` se rechaza por el
  punto, antes de que se cree ninguna entidad con ella

Lo último es el motivo de validar aquí y no al crear: un operador que guardara una máscara mala no
se enteraría, y en cambio sí lo haría cada usuario de la consola que llegase a un formulario de
creación.

:::note Esto da forma a las sugerencias, no a las reglas
Una máscara decide lo que la consola *ofrece*. Un token escrito a mano, o enviado por una
integración a través de la API, solo está sujeto a la gramática de tokens — las máscaras no se
imponen en la ruta de escritura, y cambiar una no afecta a las entidades que ya existen.
:::

## Idioma predeterminado {#locale-default}

`locale.default` decide el idioma en el que se abre la consola para quienes no han elegido uno.
Su valor es una etiqueta de idioma [BCP-47](https://www.rfc-editor.org/info/bcp47) dentro de una
cadena JSON — `"en"`, `"es"`, `"pt-BR"` — o bien **`null`**, que es como se entrega.

`null` no significa «sin definir». Es el valor que significa *ningún valor predeterminado para toda
la instancia: que decida el navegador de cada persona*, y es la razón por la que la consola que se
entrega sigue a un navegador en español desde el primer momento. Poner aquí una etiqueta anula eso
para todo el que no haya elegido; vaciar el campo en la consola vuelve a guardar `null`.

Es el último de cuatro niveles, y conviene conocer el orden antes de configurarlo, porque este es
el único de los cuatro ajustes cuyo efecto puede anular un *usuario*:

1. el idioma que la persona eligió en el selector, que nada de aquí cambia
2. el valor predeterminado del propio inquilino, que un administrador del inquilino define en
   **Configuración → Idioma**
3. los idiomas que pide el navegador de quien mira
4. inglés

Así que una etiqueta aquí solo mueve a las personas de los niveles 3 y 4 — y si defines una, los
compañeros que ya hayan usado el selector no verán ningún cambio. Es deliberado, y es el motivo
habitual por el que un cambio aquí «no funciona».

La etiqueta se comprueba por su **forma**, no por si esta versión incluye ese idioma: una etiqueta
desconocida pero bien formada se guarda y simplemente no tiene efecto hasta que exista su catálogo.
La consola te avisa cuando escribes una. La etiqueta debe guardarse en forma canónica (`es-MX`, no
`es-mx`), y una cadena en blanco se rechaza — usa `null`.

Una etiqueta regional recurre a su idioma base, así que `es-MX` muestra español en una versión que
solo incluye `es`.
