---
title: Ajustes del sistema
description: Los ajustes de toda la instancia que edita un operador, qué acepta cada uno y los límites a los que está sujeta toda escritura de ajustes.
---

# Ajustes del sistema

Un **ajuste del sistema** es un valor de toda la instancia que un operador establece una sola vez,
para todos los inquilinos. Hay tres, viven en **Ajustes** dentro de la consola de administración, y
cada uno se sitúa *por debajo* de lo que cada inquilino configure para sí mismo — un inquilino que
no configura nada obtiene el valor por defecto de la instancia, y uno que establece el suyo propio
nunca lo ve.

| Clave | Qué decide | Se cubre en |
| --- | --- | --- |
| `basemap.default` | Los teselados de mapa con los que arranca cada inquilino | [Mapas base](./basemaps.md) |
| `branding.default` | El título, el logotipo y la paleta de la instancia | [Marca blanca](./white-labeling.md) |
| `entity.token_masks` | La forma de cada token que acuña la consola | más abajo |

Leer un ajuste no requiere más autoridad que haber iniciado sesión. Escribir uno requiere
`settings:write`, que es una autoridad de nivel operador y no forma parte de ningún rol de
inquilino.

## A qué está sujeta toda escritura de ajustes {#settings-write-rules}

Tres reglas se aplican a las tres claves, en este orden:

1. **El valor debe ocupar menos de 64 KB.** Por encima de eso la escritura se rechaza indicando el
   recuento de bytes. Esto acota el documento JSON entero, no un campo concreto dentro de él — lo
   que importa sobre todo en `branding.default`, donde un logotipo `data:` incrustado podría ser
   mucho mayor. El [registro de marca](./white-labeling.md) admite un logotipo incrustado de 256 KB
   *en un inquilino*, donde se guarda como una columna con tipo y no como un ajuste; en el nivel de
   instancia se aplica en su lugar el límite de 64 KB del documento, que equivale a unos 48 KB de
   imagen. La consola le dirige a una URL `https` en este nivel exactamente por eso.
2. **El valor debe ser JSON válido.**
3. **La clave debe ser una de las tres anteriores.** El vocabulario es cerrado: escribir una clave
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
