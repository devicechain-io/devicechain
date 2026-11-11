---
sidebar_position: 4.6
title: Política de soporte a largo plazo
---

# Política de soporte a largo plazo {#long-term-support-policy}

DeviceChain designa **una línea de versiones a la vez** como línea de soporte a largo plazo (LTS). Una
línea LTS recibe **solo correcciones de seguridad y correcciones críticas**, como versiones de parche,
durante **18 meses** desde la fecha en que se designa. Esta página explica qué promete eso, cómo llega una
corrección a la línea y cómo se sale de ella. Complementa la
[política de versionado y actualización](./versioning-policy.md), que sigue rigiendo cada versión, sea LTS o no.

**Todavía no hay ninguna línea designada.** Cuando la haya, esta página la nombrará y el repositorio la
registrará en una única declaración que leen las herramientas de publicación y de backport. Hasta entonces,
cada versión sigue el [modelo de versionado](./releases-and-upgrades.md#versioning-model) habitual.

## Qué recibe una línea LTS {#what-it-receives}

| Recibe | No recibe |
| --- | --- |
| Correcciones de vulnerabilidades de seguridad. | Funcionalidades nuevas, campos u operaciones nuevos. |
| Correcciones de defectos críticos: pérdida o corrupción de datos, una plataforma que no arranca o no se actualiza, un fallo de autenticación o de aislamiento entre inquilinos. | Trabajo de rendimiento, refactorizaciones, actualizaciones de dependencias que no sean en sí una corrección de seguridad. |
| Versiones de parche (`1.4.1`, `1.4.2`) etiquetadas desde la rama propia de la línea. | Una versión menor nueva. Una versión menor nueva inicia una línea nueva en la rama principal. |

Una corrección se propone para la línea LTS solo cuando el cambio que la introdujo lleva la etiqueta
`security` o `critical`. No se propone nada más, y una propuesta nunca se fusiona automáticamente.

## Cómo llega una corrección a la línea {#how-fixes-arrive}

La línea LTS `X.Y` vive en una rama llamada `release-X.Y-lts`, creada a partir de la etiqueta `vX.Y.0`.

1. Una corrección se integra primero en la rama principal. No se acepta una corrección que exista solo en la rama LTS.
2. Si lleva la etiqueta `security` o `critical`, un bot abre una solicitud de cambios que la aplica con
   cherry-pick sobre la rama LTS. Si el commit no se aplica limpiamente, el bot lo indica en la solicitud
   original y un responsable lo porta a mano.
3. Esa solicitud ejecuta toda la matriz de CI contra la rama LTS, incluido el ensayo de actualización, y un
   responsable la revisa y la fusiona como cualquier otro cambio.
4. Las versiones de parche de la línea se etiquetan desde la rama LTS. El proceso de publicación rechaza una
   etiqueta de parche de una línea con rama creada en cualquier otro sitio, de modo que un parche nunca
   puede arrastrar cambios ajenos de la rama principal.
5. Una versión de parche de una línea anterior nunca mueve hacia atrás las etiquetas `latest` de las
   imágenes ni la etiqueta `latest` de npm. Sus paquetes npm se publican bajo una etiqueta propia, como `lts-1-4`.

## Salir de la línea {#moving-off}

Un operador en la línea LTS no tiene que avanzar de una versión menor en una versión menor. La ruta admitida
hacia una línea más nueva es una actualización directa que omite las versiones menores intermedias: se
ejecutan en orden todas las migraciones de todas las versiones omitidas, en un solo `dcctl upgrade`. Esto
se ensaya antes de recomendar una línea más nueva: se siembra una instancia con una entidad de cada tipo en
la versión LTS, se actualiza directamente a la candidata y se vuelve a leer cada fila sin cambios. El mismo
ensayo se ejecuta después contra un árbol con un defecto de migración plantado a propósito, y debe fallar;
un resultado correcto que nunca se ha visto fallar no es evidencia.

Lea las notas de actualización de cada versión que omita antes de saltar, ya que una obsolescencia anunciada
en una versión menor omitida se anuncia en las notas de esa versión.

## Lo que esta página no promete {#out-of-scope}

- Solo se admite una línea LTS a la vez. Las notas de la versión anuncian cuándo se designa una línea nueva
  y la fecha en que termina el soporte de la anterior.
- No hay compromiso más allá de 18 meses, ni una compilación validada FIPS.
- Una corrección de seguridad es una corrección de DeviceChain y de los componentes que distribuye. Una
  vulnerabilidad en un servicio que usted ejecuta junto a él queda fuera de esta página.
