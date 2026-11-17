---
title: Alarmas
sidebar_position: 6
---

import GuideShot from '@site/src/components/GuideShot';

# Alarmas

Una alarma es lo que genera una regla de detección cuando algo requiere la atención de una persona.
La página **Alarmas** enumera todas las alarmas del inquilino y se actualiza a medida que las alarmas
se generan, cambian y se despejan. Este capítulo es para los operadores que vigilan y atienden
alarmas: leer la lista, filtrarla, reconocer y despejar alarmas y exportar la lista.

## La lista de alarmas

Elige **Supervisión → Alarmas** en la barra lateral. Cada fila es una alarma.

<GuideShot id="alarms-list" alt="La página Alarmas con los filtros Todos los estados, Todas las severidades y Cualquier reconocimiento y un botón Exportar CSV. Junto a la descripción hay un indicador verde En vivo. La tabla muestra alarmas bp-over-temp de severidad Mayor sobre la métrica temperature para los termostatos bp-therm-001 a bp-therm-012: tres están Activas con los botones Reconocer y Despejar, y el resto están Despejadas." />

Las columnas:

- **Severidad**: Crítica, Mayor, Menor, Advertencia o Indeterminada, según la regla que la generó.
- **Estado**: **Activa** mientras se cumple la condición, **Despejada** cuando se ha resuelto o
  alguien la ha despejado, y **Reconocida** para una alarma activa de la que alguien se ha hecho
  cargo.
- **Originador**: el dispositivo que generó la alarma.
- **Alarma**: la clave de la alarma, con la métrica debajo.
- **Valor**: la última lectura que vio la alarma.
- **Generada**: cuándo se generó la alarma.
- **Acciones**: **Reconocer** y **Despejar**, para una alarma activa.

El indicador **En vivo** junto a la descripción muestra que la página recibe los cambios a medida que
ocurren. Si la conexión se corta, indica que se está reconectando, y la página se pone al día cuando
vuelve. La página además se actualiza sola cada 30 segundos, para que un cambio perdido no se quede
sin reflejar.

Una alarma es siempre una sola fila. Si la condición deja de cumplirse y vuelve, la misma alarma se
reactiva en lugar de aparecer una fila nueva. Las reglas que comparten una clave de alarma también
pueden subir o bajar la severidad de una misma alarma. El
[ciclo de vida de las alarmas](../concepts/event-processing.md#the-alarm-lifecycle) explica ambas
cosas.

## Filtrar

Sobre la tabla hay tres filtros: estado (**Activa** o **Despejada**), severidad y reconocimiento
(**No reconocida** o **Reconocida**). Cada uno abre una lista corta en la que puedes buscar.

<GuideShot id="alarms-filter" alt="La página Alarmas con el filtro de estado abierto, que ofrece Activa y Despejada, sobre la lista de alarmas." />

Los filtros se combinan. Por ejemplo, elige **Activa** y **No reconocida** para ver lo que nadie ha
atendido todavía. Quita un filtro con la × que tiene dentro.

## Reconocer y despejar

**Reconocer** dice «me encargo yo». La alarma sigue activa, su estado pasa a **Reconocida** y la
plataforma registra quién la reconoció y cuándo. Reconocer también detiene las notificaciones
repetidas de esa alarma.

**Despejar** cierra la alarma a mano. La mayoría de las alarmas se despejan solas cuando la condición
de la regla deja de cumplirse, así que rara vez lo necesitarás, pero está ahí para una alarma que
sabes que está resuelta.

<GuideShot id="alarms-acknowledged" alt="La página Alarmas filtrada por Reconocida, con una sola alarma Mayor, bp-over-temp en bp-therm-011, en estado Reconocida y con un botón Despejar. Mostrando 1–1 de 1 resultado." />

Qué esperar:

- Una alarma reconocida conserva el botón **Despejar** pero pierde **Reconocer**.
- Si una alarma reconocida se despeja y su condición vuelve a cumplirse, la plataforma reabre la
  misma alarma sin su reconocimiento, así que hay que volver a reconocerla.
- Los botones solo aparecen si tu rol te permite modificar alarmas. Sin ese permiso, la página es de
  solo lectura.

## Exportar la lista

**Exportar CSV** guarda todas las alarmas que coinciden con los filtros actuales, no solo la página
que estás viendo, hasta 10.000 filas. Si coinciden más alarmas, la consola indica que la exportación
se detuvo en el límite, y acotas los filtros para obtener el resto. El archivo contiene, para cada
alarma, su token, severidad, estado, si está reconocida, originador, clave de alarma, métrica, valor
y las horas en que se generó y se despejó.

## Relacionado

- [Reglas de detección](./detection-rules.md), que generan estas alarmas
- [Procesamiento de eventos y alarmas](../concepts/event-processing.md#the-alarm-lifecycle): el ciclo de vida de las alarmas y la escalada
- [Configuración de canales de notificación](../guides/notification-channels.md), para recibir alarmas por correo o en un webhook
- [Políticas de notificación](./notification-policies.md), para retener los avisos de una política durante las horas de silencio
