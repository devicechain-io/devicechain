---
title: Ajustes del sistema
description: Los ajustes de toda la instancia que edita un operador, qué acepta cada uno y los límites a los que está sujeta toda escritura de ajustes.
---

# Ajustes del sistema

Un **ajuste del sistema** es un valor de toda la instancia que un operador establece una sola vez,
para todos los inquilinos. Hay cuatro, y están en **Configuración** dentro de la consola de
administración. Cada uno se sitúa *por debajo* de lo que un inquilino configure para sí mismo: un
inquilino que no configura nada obtiene el valor predeterminado de la instancia, y uno que establece
su propio valor nunca lo ve.

| Clave | Qué decide | Se cubre en |
| --- | --- | --- |
| `basemap.default` | Los teselados de mapa con los que arranca cada inquilino | [Mapas base](./basemaps.md) |
| `branding.default` | El título, el logotipo y la paleta de la instancia | [Marca blanca](./white-labeling.md) |
| `entity.token_masks` | La forma de cada token que acuña la consola | más abajo |
| `locale.default` | El idioma en el que se abre la consola | más abajo |

Leer un ajuste requiere `settings:read`, y escribir uno requiere `settings:write`. Ambas son
autoridades de nivel operador, y ninguna forma parte de ningún rol de inquilino. Cualquier usuario
con sesión iniciada puede leer dos cosas sin ninguna de las dos:

- la consulta `tokenMasks`, que sirve solo el mapa efectivo de máscaras de token, para que todo
  formulario de creación de la consola pueda acuñar un token
- la marca, el mapa base y el idioma *efectivos* de un inquilino, que el objeto del inquilino expone
  ya plegados sobre el valor predeterminado de la instancia

## A qué está sujeta toda escritura de ajustes {#settings-write-rules}

Tres reglas se aplican a las cuatro claves, en este orden:

1. **La clave debe ser una de las cuatro anteriores.** El vocabulario es cerrado. Escribir una clave
   no reconocida se rechaza en lugar de crear un ajuste, y se rechaza antes siquiera de mirar el
   valor. No hay forma de añadir una clave desde la API.
2. **El valor debe ocupar como máximo 64 KB.** Por encima de eso, la escritura se rechaza con un
   error que indica el límite en bytes (65.536). El límite acota el documento JSON entero, no un
   campo concreto dentro de él. Esto importa sobre todo en `branding.default`, donde un logotipo
   `data:` incrustado podría ser mucho mayor:
   - En un inquilino, el [registro de marca](./white-labeling.md) admite un logotipo incrustado de
     256 KB, porque se guarda como una columna con tipo y no como un ajuste.
   - En el nivel de instancia se aplica en su lugar el límite de 64 KB del documento, que equivale a
     unos 48 KB de imagen. En este nivel, el campo de logotipo de la consola pide una URL `https` y
     no ofrece subida de archivos, y con este límite una URL es la opción práctica.
3. **El valor debe ser JSON válido.**

Después, cada clave aplica su propia validación, que describen las páginas enlazadas en la tabla.

## Máscaras de token {#token-masks}

`entity.token_masks` decide el token que precarga cada formulario de creación de la consola. Toda
entidad se direcciona mediante un token. Escribir uno a mano para cada dispositivo nuevo es tedioso
y fácil de equivocar, así que la consola genera uno a partir de una plantilla y te deja editarlo
antes de guardar.

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

El valor predeterminado que se entrega es `{"default": "{slug}"}`.

Lo que produzca una máscara sigue teniendo que cumplir la [gramática de tokens](../reference/graphql-api.md#what-a-token-may-contain),
y eso hace imposibles algunas plantillas. Una máscara se rechaza si:

- está vacía
- usa un marcador desconocido. `dev-{sulg}` generaría en silencio `dev-` para todas las entidades,
  porque un marcador no reconocido no produce nada.
- no tiene ningún marcador. Todas las entidades recibirían el mismo token, así que la primera
  creación funciona y todas las siguientes colisionan.
- declara una anchura mayor de 128 caracteres, que nunca podría acuñar un token válido
- genera una muestra que no cumple la gramática de tokens. `my.device-{slug}` se rechaza por el
  punto, antes de que se cree ninguna entidad con ella.

Esa última comprobación es la razón por la que las máscaras se validan al guardarlas y no al crear
entidades. De lo contrario, el operador que guardó una máscara incorrecta no se enteraría, y sí lo
haría cada usuario de la consola que abriera un formulario de creación.

:::note Esto da forma a las sugerencias, no a las reglas
Una máscara decide lo que la consola *ofrece*. Un token escrito a mano, o enviado por una
integración a través de la API, solo está sujeto a la gramática de tokens. Las máscaras no se
imponen en la ruta de escritura, y cambiar una no afecta a las entidades que ya existen.
:::

## Idioma predeterminado {#locale-default}

`locale.default` decide el idioma en el que se abre la consola para las personas que no han elegido
uno por sí mismas. Su valor es una etiqueta de idioma [BCP-47](https://www.rfc-editor.org/info/bcp47)
dentro de una cadena JSON (`"en"`, `"es"`, `"pt-BR"`), o bien `null`, que es como se entrega.

`null` no significa «sin definir». Significa *ningún valor predeterminado para toda la instancia:
que decida el navegador de cada persona*. Por eso la consola, tal como se entrega, sigue a un
navegador en español desde el primer momento. Poner aquí una etiqueta anula el navegador para todo
el que no haya elegido un idioma, salvo que su inquilino defina un valor predeterminado propio.
Vaciar el campo en la consola vuelve a guardar `null`.

La consola elige el idioma entre cuatro niveles. Conoce el orden antes de configurar este ajuste,
porque es el único de los cuatro cuyo efecto puede anular un *usuario*:

1. el idioma que la persona eligió en el selector, que nada de aquí cambia
2. el valor predeterminado del propio inquilino, que un administrador del inquilino define en
   **Configuración → Idioma**. Para un inquilino que no lo ha definido, este ajuste ocupa su lugar.
3. los idiomas que pide el navegador de quien mira
4. inglés

Por tanto, una etiqueta aquí solo mueve a las personas que de otro modo caerían en los niveles 3 y 4:
quienes no han elegido idioma, en un inquilino sin valor predeterminado propio. Si defines una, los
compañeros que ya hayan usado el selector no verán cambiar su idioma. Es deliberado, y es el motivo
habitual por el que un cambio aquí «no funciona».

La etiqueta se comprueba por su forma, no por si esta versión incluye ese idioma:

- Una etiqueta desconocida pero bien formada se guarda, y no tiene efecto hasta que exista su
  catálogo. La consola te avisa cuando escribes una.
- La etiqueta debe guardarse en forma canónica (`es-MX`, no `es-mx`).
- Una cadena en blanco se rechaza. Usa `null` en su lugar.

Una etiqueta regional recurre a su idioma base, así que `es-MX` muestra español en una versión que
solo incluye `es`.
