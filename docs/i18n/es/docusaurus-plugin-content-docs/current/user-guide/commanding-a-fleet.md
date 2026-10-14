---
title: Comandar una flota
sidebar_position: 7
---

import GuideShot from '@site/src/components/GuideShot';

# Comandar una flota

Un **lote de comandos** envía un mismo comando a muchos dispositivos como una única operación
registrada: fijar una nueva consigna en todos los termostatos de un edificio o reiniciar un grupo de
pasarelas. El lote guarda un registro de lo que se intentó, de qué dispositivos aceptaron el comando y
de cuáles se rechazaron y por qué. Puedes seguir sus comandos, cancelarlo y reintentar los
dispositivos que no tuvieron éxito.

Este capítulo es para los operadores que envían comandos en bloque. Da por hecho que conoces cómo
funciona un comando individual; consulta [Enviar un comando](../guides/sending-commands.md). Para
todas las reglas de los lotes, consulta [Comandar una flota](../guides/commanding-a-fleet.md) en las
guías.

## La lista de lotes

Elige **Dispositivos → Lotes de comandos** en la barra lateral. La lista muestra todos los lotes, del
más reciente al más antiguo.

<GuideShot id="batches-list" alt="La página Lotes de comandos con filtros de comando y de token de grupo, un filtro de destino y un botón Nuevo lote. Hay tres lotes de lista de dispositivos: night-setback, 0 de 2 aceptados, 2 rechazados, Cancelado; set-setpoint, 2 de 2, 0 rechazados, Cancelado; identify, 2 de 2, 0 rechazados." />

Las columnas:

- **Lanzado**: cuándo se envió el lote.
- **Comando**: la clave de comando que envió.
- **Destino**: **Lista de dispositivos** si se nombraron los dispositivos, o **Grupo** si se resolvió
  un grupo de dispositivos.
- **Aceptados / resueltos**: cuántos dispositivos aceptaron el comando, de cuántos resolvió el
  destino.
- **Rechazados**: cuántos dispositivos se rechazaron.
- **Cancelado**: una insignia cuando el lote se ha cancelado.

Filtra por clave de comando, por token de grupo o por tipo de destino. Selecciona una fila para abrir
el lote.

## Crear un lote

**Nuevo lote** abre un panel.

<GuideShot id="batches-new" alt="El panel Nuevo lote. El destino está en Lista de dispositivos. Debajo hay un selector de dispositivos, un cuadro para pegar tokens de dispositivo, un recuento que indica 0 dispositivos, un selector Tipar el comando contra vacío, que indica Ningún dispositivo: escribe la clave y el payload a mano, un campo Clave de comando y un campo Carga útil." />

1. **Destino.** Elige **Lista de dispositivos** para nombrar dispositivos, o **Grupo** para enviar a
   un grupo de dispositivos.
   - Con una lista, elige los dispositivos por nombre en **Dispositivos**, o pega sus tokens en
     **Pega tokens de dispositivo**, uno por línea o separados por comas. Pegar es la forma de nombrar
     una flota grande, hasta los 10.000 dispositivos que admite un lote. El orden importa: si solo se
     puede admitir una parte del lote, los dispositivos se admiten en el orden indicado. Un
     dispositivo nombrado dos veces recibe el comando una sola vez.
   - Con un grupo, elige el grupo. En un grupo dinámico puedes fijar una **Versión del grupo**, o
     dejar la versión publicada activa. El grupo se resuelve en sus dispositivos en el momento en que
     se lanza el lote.
2. **Comando.** Con una lista de dispositivos, el formulario sigue al primer dispositivo que nombraste y ofrece los
   comandos que publica su perfil, con un formulario tipado para los parámetros. Usa **Tipar el
   comando contra** para elegir otro dispositivo, o vacíalo, de modo que muestre *Ningún dispositivo: escribe la clave
   y el payload a mano*, para escribir tú la **Clave de comando** y la **Carga útil**. Con un grupo
   como destino, el selector empieza vacío. Al lanzar el lote, cada dispositivo de destino se
   comprueba contra su propio perfil, así que los dispositivos de otros perfiles pueden ser
   rechazados igualmente.
3. **Si algunos dispositivos no pueden recibirlo.** No hay valor por defecto; debes elegir:
   - enviar a los dispositivos que puedan recibirlo y registrar el resto como rechazos, o
   - rechazar el lote completo, de modo que no se envía ni se registra nada salvo que todos los
     dispositivos puedan recibir el comando.
4. Elige **Enviar lote**.

Si el lote se lanza, la consola abre su página de detalle. Si la plataforma rechaza el lote completo,
el panel explica por qué y confirma que no se creó nada, para que puedas corregir la petición y
lanzarlo de nuevo.

## Seguir un lote

La página de detalle de un lote tiene tres partes: un **Resumen**, los **Rechazos** y los **Comandos**
que creó el lote. Las insignias bajo el nombre muestran el tipo de destino y si se permitía el envío
parcial (**Envío parcial permitido**) o no (**Todo o nada**).

En la consola en español, la barra superior de las páginas de lotes y el enlace de vuelta aparecen
en inglés (*Command Batches*).

<GuideShot id="batch-detail-completed" alt="La página de detalle del lote identify, batch-identify-rooftop, con las insignias Lista de dispositivos y Todo o nada y los botones Reintentar… y Cancelar lote. El Resumen muestra la hora de lanzamiento, Resueltos 2, Aceptados 2, Rechazados 0, la línea resueltos = aceptados + rechazados — 2 = 2 + 0, que 0 de 2 esperaban una ventana de mantenimiento, y la carga útil. La sección Rechazos indica que todos los dispositivos fueron aceptados. Debajo empieza la sección Comandos." />

El **Resumen** registra el momento en que se lanzó el lote: a cuántos dispositivos resolvió el
destino, cuántos aceptaron y cuántos se rechazaron. Estas cifras no cambian después y siempre
cuadran: resueltos = aceptados + rechazados. El resumen también indica cuántos comandos aceptados
esperaban una ventana de mantenimiento (consulta
[Ventanas de mantenimiento](#maintenance-windows) más abajo).

Para saber dónde están los comandos **ahora**, mira **Comandos**. Enumera el comando de cada
dispositivo en el orden en que se admitieron los dispositivos, con su estado, su resultado, cuándo
entró en cola y su respuesta. Fíltralo por estado y usa **Exportar CSV** para guardar todos los
comandos que coincidan con el filtro, hasta 10.000 filas.

## Rechazos

Cuando algunos dispositivos no pudieron recibir el comando, la sección **Rechazos** los agrupa por
motivo.

<GuideShot id="batch-detail-refusals" alt="La página de detalle del lote night-setback, con las insignias Lista de dispositivos, Envío parcial permitido y Cancelado — 0 detenidos y un botón Reintentar…. El Resumen muestra Resueltos 2, Aceptados 0, Rechazados 2. La sección Rechazos indica que 2 dispositivos fueron rechazados y no recibieron nada, y muestra el código COMMAND_NOT_IN_VOCABULARY con 2 dispositivos rechazados: bp-therm-002 y bp-therm-003, cada uno con el motivo, en inglés, de que el dispositivo no acepta el comando night-setback." />

Cada grupo muestra el código de rechazo, cuántos dispositivos abarca y los propios dispositivos, con
el motivo de cada uno. En el ejemplo, el perfil de los termostatos no declara un comando
`night-setback`, así que ambos dispositivos se rechazaron con `COMMAND_NOT_IN_VOCABULARY`. El motivo
lo redacta el servidor, por eso aparece en inglés. El registro nombra como mucho 100 dispositivos por
código; cuando se rechazaron más, la consola indica cuántos muestra del total exacto. Qué significa
cada código se explica en [Cuando se rechaza una admisión](../guides/sending-commands.md#when-an-enqueue-is-refused).

## Cancelar un lote

**Cancelar lote** cancela el lote. Detiene todos los comandos que la plataforma aún retiene para el
lote. No puede recuperar un comando que ya se entregó a un dispositivo: ese dispositivo lo ejecutará
igualmente.

<GuideShot id="batch-cancel-confirm" alt="Un diálogo de confirmación titulado ¿Cancelar «identify»?. Indica que la plataforma no retiene ahora ningún comando de este lote, así que puede que no se detenga nada, y que los comandos ya entregados a sus dispositivos no se pueden recuperar. Los botones son Dejarlo en marcha y Cancelar lote." />

Antes de confirmar, el diálogo te dice cuántos comandos del lote retiene todavía la plataforma, para
que sepas cuánto puede detener la cancelación. Elige **Cancelar lote** para continuar, o **Dejarlo en
marcha**. Después, la consola informa de lo ocurrido: cuántos comandos se detuvieron, cuántos ya
habían llegado a los dispositivos y cuántos ya habían terminado.

Un lote cancelado queda marcado con el momento de la cancelación y el número de comandos que detuvo.

<GuideShot id="batch-detail-cancelled" alt="La página de detalle del lote set-setpoint, batch-setpoint-cancelled, con las insignias Lista de dispositivos, Envío parcial permitido y Cancelado — 2 detenidos y un botón Reintentar…. El Resumen muestra Resueltos 2, Aceptados 2, Rechazados 0 y una línea que indica que se canceló y que eso detuvo 2 comandos que la plataforma aún retenía." />

La insignia y el resumen conservan la hora y el recuento de la primera cancelación. Si la plataforma
todavía retiene comandos de un lote cancelado, el botón pasa a **Cancelar de nuevo**, y cancelar otra
vez detiene lo que queda sin reescribir ese registro. Cuando ya no queda nada que detener, el botón
desaparece.

## Reintentar los dispositivos que no tuvieron éxito

**Reintentar…** vuelve a enviar el comando del lote, con la misma carga útil, a los dispositivos que
no tuvieron éxito. Crea un lote nuevo vinculado a este y deja este lote como está. Abre una sección **Reintentar dispositivos no satisfactorios** en la parte superior de la página del lote.
Ninguna captura muestra todavía esa sección.

La sección agrupa los dispositivos sin éxito según lo que les ocurrió:

- **Nunca entregado**: la plataforma puede demostrar que el comando nunca llegó al dispositivo, así
  que volver a enviarlo no puede hacer que actúe dos veces. Seleccionado de entrada.
- **Rechazado por el dispositivo**: el dispositivo recibió el comando y respondió que no lo ejecutó.
  Seleccionado de entrada.
- **Rechazado al lanzar**: el dispositivo se rechazó cuando se lanzó el lote. Solo se ofrece cuando el
  registro del lote nombra todos los dispositivos rechazados con ese código (guarda como mucho 100 por código), pero no se selecciona, porque el motivo puede seguir
  vigente.
- **Resultado desconocido**: el comando se envió y no llegó respuesta, así que el dispositivo puede
  haber actuado ya. No se selecciona y permanece bloqueado hasta que marques la casilla que confirma
  que volver a enviarlo puede hacer que esos dispositivos actúen dos veces.

Los comandos que siguen en curso se cuentan pero no se pueden seleccionar. Elige los dispositivos y
después el botón de reintento, que indica cuántos dispositivos elegiste (por ejemplo **Reintentar 2
dispositivos**). El lote nuevo se abre al crearse. Su página muestra **Reintento del lote:** con un
enlace al original, y el original muestra **Reintentado por:** con un enlace a cada reintento. El
reintento pasa por las mismas comprobaciones que un lote nuevo, así que un dispositivo que ahora no
puede recibir el comando aparece como rechazo en el lote nuevo. Consulta
[Reintenta los dispositivos que no tuvieron éxito](../guides/commanding-a-fleet.md#retry-the-devices-that-did-not-succeed)
para las reglas completas.

## Ventanas de mantenimiento {#maintenance-windows}

Algunos comandos, como un reinicio, solo deben empezar cuando se puede interrumpir el dispositivo.
Una **ventana de mantenimiento** es un horario semanal, en una zona horaria, que retiene esos
comandos hasta que se abre.

Intervienen tres ajustes:

- La ventana se define en **Dispositivos → Ventanas de mantenimiento**, como una zona horaria y una o
  varias franjas semanales.
- A un tipo de dispositivo se le asigna una ventana en su campo **Ventana de mantenimiento**. Todos
  los dispositivos de ese tipo la siguen.
- Una definición de comando del perfil de dispositivo tiene un **Impacto**: Normal, Disruptivo o
  Destructivo. Solo los comandos disruptivos y destructivos esperan a la ventana. Los normales nunca
  se retienen.

Cuando un lote envía un comando disruptivo o destructivo a dispositivos cuya ventana está cerrada, esos comandos se
aceptan y esperan. El resumen del lote registra cuántos esperaban cuando se lanzó, y cada comando en
espera de la lista **Comandos** muestra *Esperando la ventana de mantenimiento hasta* la hora en que
se abre. Un comando en espera se puede cancelar como cualquier otro comando en cola. Si la ventana no
fuera a abrirse antes de que el comando caduque, el dispositivo se rechaza con `WINDOW_AFTER_EXPIRY`.

Ninguna captura muestra todavía las páginas de ventanas de mantenimiento. Consulta
[Ventanas de mantenimiento](../guides/maintenance-windows.md) para configurar una, y
[la página de conceptos](../concepts/commands.md#maintenance-windows) para saber cómo funcionan.

## Relacionado

- [Comandar una flota](../guides/commanding-a-fleet.md): destinos, códigos de rechazo, reintento y cancelación en detalle
- [Ventanas de mantenimiento](../guides/maintenance-windows.md)
- [Enviar un comando](../guides/sending-commands.md)
- [Comandos](../concepts/commands.md): el ciclo de vida de los comandos y sus resultados
