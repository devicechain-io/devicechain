---
title: Auditoría
sidebar_position: 13
---

import GuideShot from '@site/src/components/GuideShot';

# Auditoría

La pantalla **Auditoría** es el diario de cambios en el registro del inquilino: quién cambió qué y
cuándo. La plataforma anota una entrada por cada creación, actualización y eliminación que hace en el
registro; la entrada se guarda junto con el cambio, así que si no se puede escribir, el cambio no se realiza y la llamada informa de un fallo. Los cambios en
conectores, paneles, reglas y actualizaciones no están en este diario. Las entradas nunca se editan.
Usa esta pantalla para responder preguntas como «¿quién eliminó ese dispositivo?» o «¿qué creó
anoche este script?». Es para los administradores de un inquilino y para quien revise la actividad de
un inquilino.

Abre **Auditoría** en la barra lateral. Solo aparece si tu rol concede el permiso `audit:read`. El
diario solo muestra las entradas de tu propio inquilino.

## Leer el diario

Las entradas más recientes aparecen primero, 25 por página. Cada fila muestra:

| Columna | Qué significa |
| --- | --- |
| Hora | Cuándo se hizo el cambio. |
| Actor | Quién lo hizo: la identidad con la sesión iniciada, normalmente una dirección de correo. |
| Operación | **Crear**, **Actualizar** o **Eliminar**. |
| Tabla | El tipo de registro que cambió, como `devices`, `assets`, `device_credentials` o `alarms`. |
| Objetivo | Una etiqueta del registro que cambió, normalmente su token o un ID. |
| Filas | Cuántos registros afectó el cambio. |

<GuideShot id="audit-journal" alt="La pantalla Auditoría filtrada por la operación Crear y un actor. Cada fila muestra la hora, la dirección de correo del actor, una insignia verde Crear, una tabla como device_replacements, device_credentials, devices, entity_relationships o assets, el objetivo y 1 fila." />

No todas las filas corresponden a un formulario que alguien guardó. Reconocer una alarma, por
ejemplo, es una actualización de la tabla `alarms`, hecha con la identidad de quien la reconoció.

## Filtrar

Arriba a la derecha hay dos filtros:

- El selector **Todas las operaciones** limita el diario a **Crear**, **Actualizar** o **Eliminar**.
  Vacíalo para volver a ver todas las operaciones.
- **Filtrar por actor…** encuentra cualquier actor que contenga lo que escribes, sin distinguir
  mayúsculas. Basta con escribir parte de una dirección.

<GuideShot id="audit-filter" alt="La pantalla Auditoría con el selector de operación abierto, que ofrece Crear, Actualizar y Eliminar. Debajo, el diario muestra filas de Actualizar sobre la tabla alarms." />

Los filtros se combinan, y cambiar cualquiera de ellos te devuelve a la primera página.

## Exportar

**Exportar CSV** descarga todas las entradas que coinciden con los filtros actuales, no solo la página
en pantalla. El archivo tiene las mismas columnas que la tabla, con la operación como `create`, `update` o
`delete` y la tabla con su nombre completo. Una exportación se detiene en 10.000
filas; si ocurre, la consola lo indica, y puedes acotar los filtros para exportar el resto. La
exportación muestra su progreso y se puede cancelar, y cambiar un filtro mientras se ejecuta la
abandona.

## Relacionado

- [Arquitectura](../concepts/architecture.md): cómo encajan los servicios y los datos de la
  plataforma.
- [Multitenencia](../concepts/multi-tenancy.md): cómo se mantienen separados los registros de un
  inquilino de los de otro.
