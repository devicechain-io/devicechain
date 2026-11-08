---
title: Políticas de notificación
sidebar_position: 18
---

# Políticas de notificación

Una **política de notificación** decide a quién se avisa de una alarma generada, por qué canal y
cuándo. Este capítulo muestra cómo ver las políticas en la consola y cómo darle **horas de
silencio** a una, para que deje de avisar a las personas por la noche y envíe lo que retuvo cuando termina
el periodo de silencio. Es para administradores que se ocupan del enrutamiento de guardias. Cómo se
comportan las horas de silencio, y cómo definirlas mediante la API, está en
[Horas de silencio](../guides/notification-channels.md#quiet-hours).

Las políticas de notificación están en **Supervisión → Políticas de notificación** en la barra lateral. La entrada
aparece si tu rol puede leer notificaciones. Para cambiar una política, tu rol también necesita
permiso para escribir notificaciones; sin él, la política se abre en solo lectura.

## La lista de políticas

La lista muestra el token de cada política, su nombre, si está habilitada, cuántas reglas de
enrutamiento tiene y sus horas de silencio. La columna **Horas de silencio** muestra la zona horaria
en la que se leen las horas de silencio de la política, o un guion si no tiene. Haz clic en una fila
para abrir la política.

## Crear y editar una política

Haz clic en **Nueva política de notificación**. El formulario pide:

- **Token**: el identificador de la política. No puede cambiar después de crearla.
- **Nombre** y **Descripción**.
- **Habilitado**: una política deshabilitada no envía nada y descarta las notificaciones que
  estuviera reteniendo por las horas de silencio.
- **Limitación (segundos)**: el tiempo mínimo entre notificaciones sobre la misma alarma. Déjalo en
  blanco para no limitar.
- **Escalar tras (segundos)** y **Escaladas máximas**: cuánto espera una alarma sin reconocer antes
  de notificarse de nuevo, y cuántas veces. Consulta
  [Limitación y escalada](../guides/notification-channels.md#throttling-and-escalation).
- **Horas de silencio**, descritas más abajo.

La consola no edita las **reglas de enrutamiento**. Una política que creas aquí empieza sin
ninguna, y una política que abres muestra sus reglas en solo lectura: cada severidad, el canal por
el que envía y sus destinatarios. Guardar nunca las modifica. Añade o cambia reglas mediante la API,
como se describe en [Configuración de canales de notificación](../guides/notification-channels.md#políticas).

Cuando editas una política, **Guardar cambios** envía solo los campos que cambiaste. La consola
también indica al servidor qué versión de la política estabas viendo. Si otra persona guardó la
política entretanto, tu guardado se rechaza con un mensaje que indica que la política fue modificada
por otro escritor; recarga la página y vuelve a hacer tu cambio.

## Horas de silencio

Marca **Retener notificaciones durante las horas de silencio** para dar a la política horas de
silencio. Mientras la política está dentro de ellas retiene sus notificaciones y las envía cuando
termina el periodo, salvo que la alarma haya sido reconocida o cerrada entretanto. Desmárcalo y
guarda para quitar las horas de silencio.

Cuando las horas de silencio están activadas, el formulario muestra:

- **Zona horaria**: la zona en la que se leen las horas de las franjas. Empieza siendo la zona de tu
  navegador. Las horas de las franjas son horas de reloj en esta zona, así que las horas de silencio
  siguen con ella el horario de verano.
- **Franjas de silencio**: una fila por periodo, cada una con un día de la semana, una hora de inicio
  y una duración en minutos. Usa **Añadir franja** para más, hasta 56. Una franja puede pasar de
  medianoche, y una de domingo continúa en lunes.
- **Severidad de excepción**: las alarmas con esta severidad o superior se envían de inmediato
  incluso durante las horas de silencio. Empieza siendo **Crítica** la primera vez que activas las
  horas de silencio. Elige **Ninguna** para retener todas las severidades.

Las franjas no pueden solaparse, y en conjunto no pueden cubrir la semana completa. El formulario
señala ambos casos mientras escribes y mantiene **Guardar cambios** deshabilitado hasta que se
corrijan; para silenciar una política por completo, deshabilítala. El servidor hace la comprobación
final al guardar.

Si una política tiene horas de silencio que la consola no puede leer, lo indica en lugar de mostrar
un horario vacío. Las horas de silencio siguen almacenadas y en vigor, y guardar otros cambios no
las toca.

Las horas de silencio no son [ventanas de mantenimiento](../guides/maintenance-windows.md). Una
ventana de mantenimiento pertenece a un tipo de dispositivo y retiene comandos disruptivos; las
horas de silencio pertenecen a una política de notificación y retienen sus avisos.

## Relacionado

- [Horas de silencio](../guides/notification-channels.md#quiet-hours), para saber qué se retiene y
  se libera, las zonas horarias, las reglas de validación y la métrica
- [Alarmas](./alarms.md), donde reconoces y cierras las alarmas de las que avisan las políticas
- [Configuración de canales de notificación](../guides/notification-channels.md)
