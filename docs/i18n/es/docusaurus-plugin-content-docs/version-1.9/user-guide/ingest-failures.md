---
title: Fallos de ingesta
sidebar_position: 7
---

# Fallos de ingesta {#ingest-failures}

La página **Fallos de ingesta** lista los eventos que la plataforma rechazó o no pudo almacenar, del más reciente al más antiguo. Ábrela cuando un dispositivo dice que envía y no aparece nada: la página te dice si la plataforma rechazó los eventos y por qué. Este capítulo es para los miembros que gestionan dispositivos.

Es un informe, no una cola. No se reenvía nada y los eventos originales no se guardan en los datos de tu tenant. Corrige la causa y los siguientes eventos del dispositivo se aceptan.

## Quién puede verla {#who}

La página requiere la autorización `device:write`, la que tienen los miembros que gestionan dispositivos y por tanto pueden actuar ante un rechazo. No forma parte de la base de solo lectura que tiene todo miembro y no se ofrece a un miembro cuyo acceso está limitado a algunos clientes. Un miembro sin la autorización que abre la página ve una nota que lo indica, y la página no pide nada a la plataforma.

## Abrir la página {#open}

Elige **Monitor → Fallos de ingesta** en la barra lateral. La página también puede abrirse ya filtrada: la página de detalle de un dispositivo enlaza aquí con ese dispositivo cuando se rechazaron eventos suyos en las últimas 24 horas, y la pestaña **Decodificador** de un perfil de dispositivo enlaza con el motivo **Fallo del decodificador**.

## Leer una fila {#rows}

Cada fila es un evento rechazado.

| Columna | Significado |
| --- | --- |
| **Registrado** | Cuándo registró la plataforma el rechazo. Es el reloj de la plataforma, no el del dispositivo. |
| **Motivo** | Por qué se rechazó el evento. Una fila con el motivo **Otro motivo** muestra además su código de motivo. |
| **Token del dispositivo** | El token que nombró el emisor. Es lo que afirma el emisor, no una prueba de que el dispositivo exista. Una fila que dice **Más registros de este motivo** representa los registros que exceden el límite de avalancha, más abajo. |
| **Lo que indicó la plataforma** | El texto de la propia plataforma, abreviado. Debajo, **El evento original ya no se conserva; solo queda este resumen.** o **El original no se conservó.** indica qué ocurrió con el evento en sí. |
| **Agrupados además** | **+N más**: cuántos registros adicionales del mismo motivo y del mismo dispositivo se contaron en esta fila. |

## Motivos {#reasons}

Las etiquetas sobre la tabla son los motivos. Cada una muestra cuántos registros de ese motivo contó la plataforma en el intervalo elegido, y una ayuda emergente explica qué significa. Elige una etiqueta para ver solo ese motivo; vuelve a elegirla, o elige **Todos los motivos**, para quitar el filtro. Con un motivo elegido, la página también nombra los dispositivos más afectados.

| Motivo | Qué ocurrió |
| --- | --- |
| **Dispositivo desconocido** | Ningún dispositivo tiene el token que nombró el emisor. Registra el dispositivo o corrige el token que envía. |
| **Credencial rechazada** | La credencial que presentó el dispositivo faltaba o era incorrecta. Revisa la credencial del dispositivo. |
| **No es dispositivo de esta pasarela** | Una pasarela publicó para un dispositivo que no tiene asignado o que no existe. |
| **Dispositivo deshabilitado** | El dispositivo existe y se ha deshabilitado. |
| **Mensaje inválido** | No se pudo leer el mensaje, o se rechazó un valor suyo. El texto del error indica cuál. |
| **Error de la plataforma** | No se pudo alcanzar un servicio de la plataforma que el evento necesitaba, o falló el almacenamiento del evento. No puedes corregirlo desde el dispositivo; avisa a tu operador. |
| **Alias de integración cambiado** | El evento de una integración nombró un dispositivo que ya no tiene el alias con el que se asoció. |
| **Fallo del decodificador** | Un decodificador de payload no pudo convertir un uplink en lecturas. El comienzo del error indica por qué. Abre el perfil del tipo de dispositivo, pestaña **Decodificador**, para ver los fallos conservados y probarlos. |
| **Otro motivo** | Un motivo para el que esta versión de la consola no tiene texto. La fila lleva su código de motivo. |

Para saber qué revisar en cada motivo, consulta [Por qué no aparecen los datos de mi dispositivo](../guides/why-isnt-my-data-showing-up.md#reasons).

## Acotar la lista {#filters}

- **Intervalo**: **Última hora**, **Últimas 24 horas** (el predeterminado) o **Últimos 7 días**. La plataforma conserva los registros durante una ventana de retención que fija un operador, siete días por defecto, así que un intervalo más largo muestra simplemente lo que aún se conserva.
- **Motivo**: las etiquetas descritas arriba.
- **Dispositivo**: escribe un token de dispositivo en **Filtrar por token de dispositivo…** para ver solo los rechazos de ese dispositivo.

La lista muestra 25 filas por página. Si nada coincide, la página lo indica y añade que un dispositivo cuyos datos faltan por otra causa no aparece aquí: por ejemplo, los eventos de un dispositivo deshabilitado se descartan y se cuentan, no se listan.

## Por qué una fila puede representar muchos eventos {#flood-bound}

Una flota con una credencial equivocada puede sufrir miles de rechazos por segundo, y una lista que guardara cada uno sería inútil. Por eso la plataforma conserva como máximo 10 registros por hora por dispositivo y motivo, y cuenta el resto en el más reciente (**+N más**). Detalla como máximo 1.000 registros por hora por motivo entre todos los dispositivos; el resto se cuenta en una sola fila marcada **Más registros de este motivo**. Las cifras de las etiquetas de motivo incluyen todo lo agrupado.

## Desde la línea de comandos, y las dead letters {#cli}

`dcctl events failed` lee los mismos registros como miembro del tenant, con el mismo requisito `device:write`, y `--summary` los cuenta por motivo como hacen las etiquetas. Consulta la [referencia de comandos](../reference/dcctl.md#dcctl-events-failed).

Estos registros no son dead letters. Un fallo de ingesta es un evento rechazado en la entrada, y el registro es tuyo para leerlo. Una dead letter es un mensaje del que un servicio de la plataforma desistió después de aceptarlo y se conserva para los operadores; se lee con `dcctl dead-letters` y no aparece aquí.

## Relacionado {#related}

- [Por qué no aparecen los datos de mi dispositivo](../guides/why-isnt-my-data-showing-up.md): qué revisar en cada motivo, la retención y la alerta que reciben los operadores.
- [Dispositivos](./devices.md): la página de cada dispositivo, que enlaza aquí.
- [La consola de un vistazo](./console-at-a-glance.md): dónde está **Fallos de ingesta** en la barra lateral.
