---
sidebar_position: 6
title: Eliminación de inquilinos
---

# Eliminación de inquilinos

Eliminar un inquilino es un **ciclo de vida, no una única acción**. La eliminación corta
el acceso de inmediato; la recuperación de los datos del inquilino ocurre después, en
segundo plano, y su identificador no vuelve a estar disponible hasta que eso termina.

Esta página trata de lo que sucede entre esos dos momentos: qué debe esperar un operador,
qué puede dejar una eliminación abierta y qué conserva la plataforma deliberadamente.

## Qué ocurre, y cuándo {#timeline}

| | |
|---|---|
| **De inmediato** | Se revoca el inicio de sesión. El inquilino queda deshabilitado y marcado como en eliminación, y se registra el momento del corte. Las nuevas conexiones de dispositivos, la nueva ingesta y el nuevo envío de comandos se rechazan en aproximadamente un minuto. |
| **En uno o dos minutos** | Un coordinador en segundo plano empieza a recuperar los datos del inquilino de todos los sistemas de almacenamiento que los contienen, y continúa hasta que cada uno informa de que no conserva nada. |
| **Tras al menos 12 horas** | Se elimina el registro del inquilino y su identificador vuelve a estar disponible. |

La eliminación en sí es **idempotente**: eliminar un inquilino que no existe, o uno que ya
se está eliminando, tiene éxito y no cambia nada. Un script de desmontaje que falló a
medias puede volver a ejecutarse sin más.

:::note Primero hay que quitar las membresías
Un inquilino que todavía tiene membresías de usuario se **rechaza**, porque quitarlas es
lo que realmente revoca el acceso de las personas a él. Quite las membresías y luego
elimine el inquilino.
:::

## Por qué el identificador se retiene 12 horas {#token-hold}

Esta es la parte que más suele sorprender, así que conviene ser preciso: los **datos** del
inquilino normalmente desaparecen en el primer minuto o dos. Lo que permanece reservado es
el **identificador**.

Deben transcurrir dos esperas distintas antes de que la plataforma dé una eliminación por
terminada.

**Todos los sistemas de almacenamiento deben informar de que están limpios, y seguir
haciéndolo.** Una única respuesta limpia no es la garantía que aparenta: un barrido puede
terminar justo cuando aterriza una escritura que ya estaba en curso. Por eso la plataforma
exige un periodo de calma sostenido (cinco minutos de forma predeterminada) y reinicia ese
reloj siempre que una pasada todavía encuentra algo que borrar.

**Ninguna conexión anterior a la eliminación puede seguir escribiendo.** Un dispositivo ya
conectado conserva su credencial del bróker hasta que esta caduca, hasta 12 horas. Sus
credenciales se han borrado, así que no puede volver a conectarse, pero la conexión
existente sobrevive. Liberar el identificador antes de que eso transcurra permitiría que
las escrituras de un rezagado aterrizasen bajo un nombre con el que después podría crearse
un inquilino **nuevo**, que las heredaría. Ese es exactamente el problema que todo este
proceso existe para evitar, así que la espera se mide desde el momento de la eliminación y
coincide con la vida útil de la credencial.

Hasta que ambas hayan pasado, el identificador sigue ocupado, e intentar crear un
inquilino con él devuelve un error que lo indica.

## Qué se borra {#erased}

Los datos de un inquilino no residen en un solo lugar, así que la recuperación pregunta a
cada sistema de almacenamiento por turno:

- la **base de datos principal**, en el esquema de cada área funcional que contiene;
- la **base de datos de telemetría**, donde reside el historial de eventos;
- el **bróker de mensajes**: los flujos del inquilino, además del propio estado de sesión
  de la pasarela MQTT, los mensajes en cola y las suscripciones;
- el **almacén clave-valor**: búsquedas y resoluciones en caché;
- el **motor de procesamiento de eventos en ejecución**: ventanas de detección abiertas,
  temporizadores en marcha y comprobaciones de ausencia armadas, que existen en memoria y a
  los que se pide que expulsen al inquilino en lugar de consultarlos;
- el **almacén de objetos**: activos subidos, como el logotipo de marca de un inquilino.

Una eliminación no puede darse por terminada hasta que **todos** informen de que están
limpios. Un sistema de almacenamiento inaccesible se reintenta en la siguiente pasada, no
se omite.

## Qué se conserva deliberadamente {#retained}

Algunos registros sobreviven a propósito, y ninguno contiene datos propios del inquilino:

- **Las personas.** Una cuenta de usuario pertenece a toda la instancia, no a un
  inquilino. Lo que se elimina es la membresía que la vinculaba a él.
- **Las definiciones a nivel de instancia** —roles, niveles, clientes OAuth, claves de
  firma y ajustes del sistema— que existen una vez por instalación y sobreviven a
  cualquier inquilino.
- **El registro de eliminación.** Cada eliminación escribe un registro duradero de qué se
  borró y cuándo, más una línea por sistema de almacenamiento. Deliberadamente **no**
  conserva el nombre ni los datos de contacto del inquilino: un registro que los guardara
  dejaría la propia evidencia del borrado como el último lugar donde vivieron los datos
  del cliente.

## Qué puede dejar una eliminación abierta {#stalled}

Una eliminación que no termina casi siempre se debe a uno de estos motivos, y cada uno se
identifica en los registros de la plataforma:

**El procesamiento de eventos no está en marcha.** El estado del motor en vivo solo puede
borrarlo el proceso que lo contiene. Si ese servicio está escalado a cero, la eliminación
espera, y hace bien: los datos siguen realmente ahí. Arrancar el servicio lo resuelve en la
siguiente pasada.

**No hay almacenamiento de objetos configurado, pero el inquilino subió algo.** La
referencia al objeto se conoce y el objeto en sí no es accesible. Configure el backend de
almacenamiento que lo contiene, o elimine el objeto por otra vía.

**Un sistema de almacenamiento está inaccesible.** Se trata como «vuelve a intentarlo», no
como un fallo. La eliminación se reanuda por sí sola cuando el sistema vuelve.

Ninguno de estos casos pierde la eliminación. La lista de trabajo es el propio registro del
inquilino, así que un coordinador detenido, una réplica reprogramada y un sistema caído
durante una semana convergen todos en la siguiente pasada.

## Configuración {#configuration}

Estos ajustes residen bajo `tenantPurge` en el servicio user-management. `0` significa
«usar el valor predeterminado» en los tres casos.

| Ajuste | Predeterminado | Significado |
|---|---|---|
| `intervalSeconds` | `60` | Cada cuánto se ejecuta el coordinador. Un **valor negativo lo deshabilita** por completo. |
| `settleSeconds` | `300` | Cuánto tiempo debe cada sistema de almacenamiento seguir informando de que está limpio. Debe ser mayor que 140. |
| `tokenHoldSeconds` | `43200` | Cuánto tiempo permanece reservado un identificador eliminado, medido desde la eliminación. |

**Deshabilitar el coordinador es una palanca operativa admitida**: una ventana de
mantenimiento en la que nada debería estar borrando filas. Es seguro: las eliminaciones
pendientes siguen pendientes en lugar de perderse, y no se libera ningún identificador
mientras está desactivado.

**Las dos esperas no se pueden deshabilitar.** Un valor negativo para cualquiera de ellas
se rechaza al cargar la configuración, en lugar de aplicarse. Desactivar el periodo de
calma significaría declarar unos datos borrados sin haber comprobado nunca que lo están;
desactivar la retención del identificador liberaría un nombre mientras una sesión
preexistente todavía podría escribir bajo él. Bajar `tokenHoldSeconds` es una decisión
real —el nombre de un inquilino eliminado no está disponible durante ese tiempo— pero es
una decisión sobre corrección, no sobre pulcritud.

## Durante una eliminación {#during}

**El tráfico de dispositivos se detiene en aproximadamente un minuto.** Las nuevas
conexiones, la ingesta en todos los transportes y el envío de comandos se rechazan en
cuanto se toma la eliminación. A un dispositivo rechazado se le responde exactamente igual
que a uno que ha superado su límite de tasa, y este espera y reintenta.

**Los dispositivos ya conectados no se desconectan.** Conservan su credencial del bróker
hasta que caduca —hasta 12 horas— y no pueden volver a conectarse cuando eso ocurre. Esta
es la razón de la retención del identificador descrita arriba.

**Si el servicio user-management está inaccesible, estos rechazos dejan de aplicarse**
hasta que vuelve. Es deliberado: la alternativa convertiría a user-management en una
dependencia estricta de la conectividad de los dispositivos para todos los inquilinos de la
instancia. Los rechazos existen para detener el tráfico pronto, de modo que la recuperación
no persiga datos que siguen llegando; el borrado en sí no depende de ellos.

**Los conectores de salida aún no están cubiertos.** Entre la eliminación y la pasada que
borra las reglas de automatización de un inquilino, esas reglas todavía pueden ejecutar sus
acciones de salida. Si la eliminación de un inquilino debe garantizar que no se envía nada
más a un sistema externo, deshabilite sus conectores antes de eliminarlo.

## Qué se puede ver hoy {#visibility}

La consola de administración muestra si un inquilino está activo o en eliminación, y cuándo
se solicitó la eliminación.

**El detalle por sistema todavía no está expuesto.** La plataforma registra, para cada
eliminación, qué sistemas de almacenamiento han informado de que están limpios, qué
conserva todavía alguno de ellos y por qué, y cuánto se borró, pero ese registro solo puede
leerse actualmente en la base de datos y en los registros del servicio. Está previsto
exponerlo.
