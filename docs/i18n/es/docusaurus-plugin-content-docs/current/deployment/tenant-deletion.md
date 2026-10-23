---
sidebar_position: 7
title: Eliminación de inquilinos
---

# Eliminación de inquilinos

Eliminar un inquilino es un ciclo de vida, no una única acción. La eliminación corta el acceso de
inmediato. Los datos del inquilino se recuperan después, en segundo plano, y su identificador no
vuelve a estar disponible hasta que eso termina.

Esta página trata de lo que sucede entre esos dos momentos: qué esperar, qué puede dejar una
eliminación abierta y qué conserva la plataforma deliberadamente.

## Qué ocurre, y cuándo {#timeline}

| | |
|---|---|
| **De inmediato** | Se revoca el inicio de sesión. El inquilino queda deshabilitado y marcado como en eliminación, y se registra el momento del corte. Las nuevas conexiones de dispositivos, la nueva ingesta y el nuevo envío de comandos se rechazan en aproximadamente un minuto. |
| **En uno o dos minutos** | Un coordinador en segundo plano empieza a recuperar los datos del inquilino de todos los sistemas de almacenamiento que los contienen, y continúa hasta que cada uno informa de que no conserva nada. |
| **Tras al menos 12 horas** | Se elimina el registro del inquilino y su identificador vuelve a estar disponible. |

La eliminación en sí es idempotente. Eliminar un inquilino que no existe, o uno que ya se está
eliminando, tiene éxito y no cambia nada, así que puedes volver a ejecutar un script de desmontaje
que falló a medias.

:::note Primero hay que quitar las membresías
Un inquilino que todavía tiene membresías de usuario se rechaza, porque quitarlas es lo que
realmente revoca el acceso de las personas a él. Quita las membresías y luego elimina el inquilino.
:::

## Por qué el identificador se retiene 12 horas {#token-hold}

Los datos del inquilino normalmente desaparecen en el primer minuto o dos. Lo que permanece
reservado es el **identificador**, y deben transcurrir dos esperas distintas antes de que la
plataforma dé la eliminación por terminada.

**Todos los sistemas de almacenamiento deben informar de que están limpios, y seguir haciéndolo.**
Una única respuesta limpia no basta: un barrido puede terminar justo cuando aterriza detrás de él
una escritura que ya estaba en curso. Por eso la plataforma exige un periodo de calma sostenido
(cinco minutos de forma predeterminada) y reinicia ese reloj siempre que una pasada todavía
encuentra algo que borrar.

**Ninguna conexión anterior a la eliminación debe poder seguir escribiendo.** Un dispositivo que ya
estaba conectado conserva su credencial del bróker hasta que esta caduca, hasta 12 horas. Sus
credenciales se han borrado, así que no puede volver a conectarse, pero la conexión existente
sobrevive. Liberar el identificador antes de ese momento permitiría que las escrituras de un
rezagado aterrizasen bajo un nombre con el que después podría crearse un inquilino *nuevo*, que las
heredaría. Evitar eso es el objetivo de todo el proceso, así que la espera coincide con la vida útil
de la credencial y se mide desde que el último sistema de almacenamiento terminó de borrar los datos
del inquilino, a partir de lo cual ya no puede emitirse ninguna credencial nueva para él.

Hasta que ambas esperas hayan pasado, el identificador sigue ocupado. Intentar crear un inquilino
con él devuelve un error que lo indica.

## Qué se borra {#erased}

Los datos de un inquilino no residen en un solo lugar, así que la recuperación pregunta a cada
sistema de almacenamiento por turno:

- la **base de datos principal**, en el esquema de cada área funcional que contiene;
- la **base de datos de telemetría**, donde reside el historial de eventos;
- el **bróker de mensajes**: los flujos del inquilino, además del propio estado de sesión de la
  pasarela MQTT, los mensajes en cola y las suscripciones;
- el **almacén clave-valor**: búsquedas y resoluciones en caché;
- el **motor de procesamiento de eventos en ejecución**: ventanas de detección abiertas,
  temporizadores en marcha y comprobaciones de ausencia armadas, que existen en memoria y a los que
  se pide que expulsen al inquilino en lugar de consultarlos;
- el **almacén de objetos**: activos subidos, como el logotipo de marca de un inquilino.

Una eliminación no puede darse por terminada hasta que todos informen de que están limpios. Un
sistema de almacenamiento inaccesible se reintenta en la siguiente pasada, no se omite.

### Lo que debes retirar tú {#operator-steps}

Algunos ajustes que nombran a un inquilino forman parte de la configuración de un servicio, no de
los datos del inquilino, así que una eliminación no los toca.

**Identidades de dispositivos LwM2M.** Cada identidad DTLS-PSK de LwM2M está vinculada a un
inquilino y a un identificador externo en la configuración de lwm2m-ingest. Después de eliminar un
inquilino, retira sus identidades de esa configuración: en una instancia instalada con `dcctl`,
quítalas del archivo indicado en `--lwm2m-identities` y vuelve a ejecutar `dcctl bootstrap` con él.
De lo contrario, los vínculos se mantienen y se aplican a cualquier inquilino que se cree más
adelante con el mismo identificador.

## Qué se conserva deliberadamente {#retained}

Algunos registros sobreviven a propósito, y ninguno contiene datos propios del inquilino:

- **Las personas.** Una cuenta de usuario pertenece a toda la instancia, no a un inquilino. Lo que
  se elimina es la membresía que la vinculaba a él.
- **Las definiciones a nivel de instancia**: roles, niveles, clientes OAuth, claves de firma y
  ajustes del sistema. Existen una vez por instalación y sobreviven a cualquier inquilino.
- **El registro de eliminación.** Cada eliminación escribe un registro duradero de qué se borró y
  cuándo, más una línea por sistema de almacenamiento. Deliberadamente no conserva el nombre ni los
  datos de contacto del inquilino; de lo contrario, la propia evidencia del borrado sería el último
  lugar donde vivirían los datos del cliente.
- **El diario de auditoría, con las identidades que contiene destruidas.** Consulta más abajo.

**Los respaldos son la excepción a esa frase.** Un respaldo es una copia de una base de datos
entera, así que los datos de un inquilino eliminado permanecen en el archivo de respaldos hasta que
salen de la [ventana de recuperación](./bootstrap.md#backup-retention) de esa base de datos: sus
datos de núcleo durante 30 días por defecto (`backup_retention_rdb`), y sus datos de eventos
durante 7 (`backup_retention_tsdb`). Hasta entonces, una restauración a un punto anterior a la
eliminación los recupera. Los respaldos enviados a un almacén de objetos que hayas proporcionado
están sujetos además a las reglas de ciclo de vida de ese almacén. Con
[respaldos base como instantáneas de volumen](./bootstrap.md#snapshot-base-backups), las
instantáneas en tu proveedor de nube se podan a la misma ventana mientras el clúster está en
marcha; si el clúster se borra antes de haberlas borrado, se quedan en el proveedor, con esos
datos, hasta que las borres allí.

### El diario de auditoría {#audit}

Cada cambio en una entidad queda registrado en un diario de auditoría: cuándo ocurrió, qué tabla,
qué operación, cuántas filas y quién lo hizo. El diario se conserva a través de una eliminación,
porque es el registro de que la eliminación ocurrió. Barrerlo destruiría la evidencia del propio
borrado.

Lo que no sobrevive es la identidad de nadie. Dos campos pueden nombrar a una persona:

- el usuario que actúa, que en un inicio de sesión humano es su dirección de correo;
- la etiqueta de la fila afectada, que puede ser un correo o un nombre que tú mismo elegiste para un
  cliente, dispositivo o activo.

**Ambos se vacían para un inquilino eliminado, de forma permanente.** Se escriben en blanco en lugar
de cifrarse o resumirse con un hash. Una dirección de correo es corta y adivinable, así que
cualquiera con una lista de direcciones podría emparejar un hash con una de ellas, lo que sería un
borrado solo de nombre.

Tras una eliminación, una entrada del diario de ese inquilino sigue mostrando la forma de lo que
pasó («tres dispositivos eliminados a las 14:02») sin nombrar a nadie. Eso es la conservación
funcionando, no datos que faltan.

Las entradas conservadas se archivan bajo la eliminación y no bajo el token del inquilino, así que un
inquilino nuevo creado más tarde con el mismo token empieza con un diario vacío. El campo
`retainedAuditTenant` del registro de eliminación es el valor bajo el que se archivan: pásalo como
filtro `tenant` de la consulta `auditEvents` de la API de administración para leer las entradas de
esa eliminación en el diario administrativo.

En la consola, abre **Administración → Auditoría** y usa el filtro de inquilino. Muestra cada eliminación
completada como `<token> (eliminado el <fecha>)`, y al elegir una se ven las entradas conservadas de esa
eliminación.

Un inquilino creado con el token de uno eliminado tampoco hereda las sesiones de ese inquilino. Un token
de actualización (refresh token) emitido en un segundo anterior a la creación del inquilino nuevo se rechaza, para
cualquier llamante, así que quien tenía la sesión iniciada en el inquilino anterior debe volver a iniciar
sesión.

:::warning Los registros de inicio de sesión no pertenecen a ningún inquilino, y no se alcanzan
El historial de inicios de sesión de un antiguo miembro sobrevive a la eliminación de un inquilino.
Si tus obligaciones alcanzan a los registros de inicio de sesión, gestiónalos mediante la retención
de la base de datos y de los registros, no mediante la eliminación de inquilinos.
:::

Iniciar sesión ocurre en dos pasos: primero te autenticas como persona y después eliges un
inquilino. El primer paso se registra, tanto los aciertos como los fallos, con la dirección de
correo y sin inquilino asociado, porque aún no se ha elegido ninguno. Ninguna eliminación de
inquilino puede alcanzar registros sin inquilino. Lo que sí cubre una eliminación es todo lo que un
miembro hizo *dentro* del inquilino.

Dos límites menores, para que consten:

- Un antiguo miembro que intente iniciar sesión en el inquilino *después* de que su eliminación se
  complete crea un registro nuevo que lo nombra, bajo un identificador que para entonces puede
  pertenecer a otra persona.
- Una entrada del diario sobre el propio perfil de una persona sigue registrando qué cuenta se
  modificó. Las cuentas no pertenecen a un inquilino y no se eliminan con él, así que un operador
  con acceso a ambos todavía puede relacionarlos. Lo que se destruye es el nombre en el diario, no
  la existencia de la cuenta a la que se refería.

## Qué puede dejar una eliminación abierta {#stalled}

Una eliminación que no termina casi siempre se debe a uno de estos motivos, y cada uno se identifica
en los registros de la plataforma:

- **El procesamiento de eventos no está en marcha.** El estado del motor en vivo solo puede borrarlo
  el proceso que lo contiene. Si ese servicio está escalado a cero, la eliminación espera, y hace
  bien, porque los datos siguen realmente ahí. Arrancar el servicio lo resuelve en la siguiente
  pasada.
- **No hay almacenamiento de objetos configurado, pero el inquilino subió algo.** La referencia al
  objeto se conoce, pero el objeto en sí no es accesible. Configura el backend de almacenamiento que
  lo contiene, o elimina el objeto por otra vía.
- **Un sistema de almacenamiento está inaccesible.** Se trata como «vuelve a intentarlo», no como un
  fallo. La eliminación se reanuda por sí sola cuando el sistema vuelve.

Ninguno de estos casos pierde la eliminación. La lista de trabajo es el propio registro del
inquilino, así que un coordinador detenido, una réplica reprogramada y un sistema caído durante una
semana convergen todos en la siguiente pasada.

### Cómo te enteras {#stalled-alert}

Una eliminación estancada no hace que nada más parezca averiado, y por eso necesita su propia
alerta. El coordinador visita al inquilino en cada pasada, no encuentra nada que pueda notificar
como error y la pasada termina bien. Por eso las [métricas de tareas
programadas](./observability.md#maintenance-passes) que te dirían que el coordinador se ha parado
siguen sanas todo el tiempo; responden a otra pregunta.

`TenantPurgeStalled` responde a esta. Se dispara cuando la eliminación abierta más antigua lleva
abierta más del doble de la retención de identificador configurada, bastante más allá del punto en
que ya han transcurrido todas las esperas obligatorias. La consulta `tenantDeletions` de la API de
administración indica entonces qué inquilino y qué sistema de almacenamiento siguen pendientes.
Está paginada como las demás listas de la API de administración; para leer las eliminaciones
abiertas:

```graphql
query {
  tenantDeletions(criteria: {pageNumber: 1, pageSize: 50, completed: false}) {
    results { token epoch awaiting blockedBy stores { store complete retaining } }
    pagination { totalRecords }
  }
}
```

Su contrapeso, `TenantPurgeVisibilityLost`, se dispara cuando esas cifras dejan de recogerse por
completo. Sin él, un servicio user-management que no se estuviera consultando se vería exactamente
igual que uno sin ninguna eliminación abierta.

### Qué sistema de almacenamiento retiene una eliminación {#blocked-store}

`TenantPurgeStalled` dice que una eliminación está atascada; estas series dicen dónde. El servicio
user-management las exporta sin etiqueta de inquilino, porque una serie con el nombre de un inquilino
sobreviviría al borrado que describe:

- `devicechain_usermanagement_tenant_purge_store_blocked{store,reason}`: número de eliminaciones
  abiertas que cada sistema de almacenamiento retiene en este momento. `store` es `rdb` (datos
  relacionales), `tsdb` (telemetría), `broker` (flujos), `kv` (buckets clave-valor), `detect` (estado
  de detección), `blob` (almacenamiento de objetos) u `other`. `reason` es `deferred` (el almacén
  respondió que aún hay datos o que siguen llegando) o `error` (no se pudo consultar al almacén). Se
  exportan todos los pares y valen 0 cuando nada está bloqueado.
- `devicechain_usermanagement_tenant_purge_store_rows_erased_total{store}`: filas, objetos, mensajes
  o claves que ha borrado cada almacén. Un almacén bloqueado cuyo contador sigue subiendo está
  procesando un inquilino grande; si el contador está plano, no avanza.
- `devicechain_usermanagement_tenant_purge_store_erase_duration_seconds{store}`: lo que tardó cada
  llamada de borrado, que muestra a qué almacén se va el tiempo.
- `devicechain_usermanagement_tenant_purge_completed_total`, `tenant_purge_failures_total`,
  `tenant_purge_duration_seconds` y `tenant_purge_clean_duration_seconds`: eliminaciones completadas;
  veces que el coordinador no pudo llevar una eliminación a término en una pasada (reintenta en la
  siguiente); el tiempo desde la eliminación hasta la liberación del identificador; y el tiempo hasta
  que el último almacén informó de que estaba limpio. La última excluye las esperas de asentamiento y
  de retención del identificador, así que mide el borrado en sí.

Tres alertas las usan. `TenantPurgeStoreFailing` se dispara cuando un almacén lleva una hora
devolviendo un error. `TenantPurgeStoreHolding` se dispara cuando un almacén ha informado de datos
aún presentes durante tanto tiempo como la retención del identificador.
`TenantPurgeCoordinatorFailing` se dispara cuando el propio coordinador no consigue llevar una
eliminación a término en una pasada. Las dos primeras llevan el almacén en su etiqueta `store`; la
línea de registro «Tenant purge store did not report clean» y la consulta `tenantDeletions` indican
el motivo.

## Configuración {#configuration}

Estos ajustes residen bajo `tenantPurge` en el servicio user-management. En los tres, `0` significa
«usar el valor predeterminado».

| Ajuste | Predeterminado | Significado |
|---|---|---|
| `intervalSeconds` | `60` | Cada cuánto se ejecuta el coordinador. Un **valor negativo lo deshabilita** por completo. |
| `settleSeconds` | `300` | Cuánto tiempo debe cada sistema de almacenamiento seguir informando de que está limpio. Debe ser mayor que 140. |
| `tokenHoldSeconds` | `43200` | Cuánto tiempo permanece reservado un identificador eliminado, medido desde que el último sistema de almacenamiento terminó de borrar los datos del inquilino. |

Deshabilitar el coordinador es una palanca operativa admitida, por ejemplo durante una ventana de
mantenimiento en la que nada debería estar borrando filas. Es seguro: las eliminaciones pendientes
siguen pendientes en lugar de perderse, y no se libera ningún identificador mientras está
desactivado.

**Las dos esperas no se pueden deshabilitar.** Un valor negativo para cualquiera de ellas se rechaza
al cargar la configuración, en lugar de aplicarse. Desactivar el periodo de calma significaría
declarar unos datos borrados sin haber comprobado nunca que ya no están. Desactivar la retención del
identificador liberaría un nombre mientras una sesión preexistente todavía podría escribir bajo él.
Bajar `tokenHoldSeconds` es una decisión real, ya que el nombre de un inquilino eliminado no está
disponible durante ese tiempo, pero es una decisión sobre corrección, no sobre pulcritud.

## Durante una eliminación {#during}

**El tráfico de dispositivos se detiene en aproximadamente un minuto.** Las nuevas conexiones, la
ingesta en todos los transportes y el envío de comandos se rechazan en cuanto se detecta la
eliminación. A un dispositivo rechazado se le responde exactamente igual que a uno que ha superado
su límite de tasa, y este espera y reintenta.

**Los dispositivos ya conectados no se desconectan.** Conservan su credencial del bróker hasta que
caduca, hasta 12 horas, y no pueden volver a conectarse cuando eso ocurre. Esta es la razón de la
[retención del identificador](#token-hold).

**Si el servicio user-management está inaccesible, estos rechazos dejan de aplicarse** hasta que
vuelve. Es deliberado: de lo contrario, user-management sería una dependencia estricta de la
conectividad de los dispositivos para todos los inquilinos de la instancia. Los rechazos detienen el
tráfico pronto para que la recuperación no persiga datos que siguen llegando. El borrado en sí no
depende de ellos.

### Rechazo de escrituras en la base de datos {#database-writes}

La escritura en las bases de datos se rechaza por separado, y ese rechazo no depende de ningún otro
servicio. Desde el momento en que la recuperación alcanza por primera vez la base de datos principal
y la base de datos de telemetría, cada una rechaza toda escritura del inquilino eliminado, llegue
por la API, por un flujo que consume un servicio o por un trabajo en segundo plano propio. La
comprobación se ejecuta dentro de la misma transacción de base de datos que la escritura que
rechaza.

Esto es lo que convierte la garantía en un borrado y no en una limpieza: las filas recuperadas no
pueden volver para quedarse, aunque haya fallado todo lo que debía detener el tráfico antes. Una
escritura tardía se barre de nuevo, y la eliminación no se completa hasta que las escrituras han
cesado. El rechazo se levanta únicamente cuando la eliminación se completa, que es también cuando se
libera el identificador, de modo que un inquilino nuevo creado con ese identificador escribe con
normalidad desde su primera petición.

Una escritura rechazada no se reintenta. Un servicio que se encuentra con el rechazo al tratar un
mensaje de un flujo reconoce el mensaje en la misma entrega en que llegó y no registra nada sobre
él: ni una carta muerta ni un registro en `failed-events`, porque cualquiera de los dos sería una
copia nueva de los datos que se están borrando. Cuenta el mensaje como `erased` (consulte
[Resultados de los mensajes](./observability.md#message-results)). Cuando un servicio escribe en una
sola transacción mensajes de varios inquilinos, el rechazo liquida a la vez todos los mensajes del
inquilino eliminado que contiene, y los mensajes de los demás inquilinos se escriben sin ellos. Una
base de datos que no puede decir si el inquilino se ha eliminado no es un rechazo: esos mensajes se
reintentan como cualquier otra escritura fallida.

El trabajo de un inquilino eliminado también puede fallar antes de llegar a una escritura, por
ejemplo porque la eliminación ya ha borrado el dispositivo o el comando al que se refiere. Tampoco
se escribe una carta muerta por ello, siempre que el servicio pueda saber por user-management que el
inquilino se ha eliminado. Si no puede, la carta se escribe, y la que se escribe antes de que
termine la eliminación se borra junto con los demás registros del inquilino.

El rechazo se comprueba en la primera escritura de una transacción para un inquilino, así que una
transacción que ya había escrito para el inquilino cuando empezó la eliminación todavía puede
confirmarse, incluidas las escrituras que haga después. La eliminación las recoge siempre que la
transacción se confirme antes de que termine el periodo de calma. Las bases de datos lo garantizan
para una transacción cuyo servicio deja de comunicarse con ellas, por ejemplo porque su pod se ha
congelado o ha perdido la red: una transacción inactiva durante 60 segundos se termina y se revierte,
muy por debajo del periodo de calma más corto permitido. La base de datos registra
`terminating connection due to idle-in-transaction timeout` y no se conserva nada de lo que escribió
la transacción. La solicitud a la que pertenecía falla con un error, y el trabajo que un servicio
toma de un flujo se le vuelve a entregar.

El rechazo cubre las dos bases de datos, que es donde viven los registros de un inquilino. El
bróker, el almacén clave-valor y el almacén de objetos se recuperan en las mismas pasadas, pero no
tienen un rechazo equivalente. Un mensaje o un objeto que llegue tarde ahí se recoge en una pasada
posterior en lugar de rechazarse, y por eso la eliminación tampoco se da por terminada hasta que
todos ellos se han mantenido limpios durante el periodo de calma.

### Conectores de salida y notificaciones {#connectors-and-notifications}

Los conectores de salida están cubiertos por el mismo rechazo, y es ahí donde más importa. En todos
los demás casos, un mensaje admitido un instante demasiado tarde son datos que permanecen en la
plataforma hasta que la recuperación llega a ellos. Un conector de salida envía los datos de un
inquilino a un sistema que tú controlas pero la plataforma no (un endpoint de webhook, un bróker
MQTT, un tema de Kafka, una cola de SNS o SQS), y una vez enviados, ninguna pasada posterior puede
recuperarlos. Por eso un despacho de conector para un inquilino eliminado se rechaza y se descarta,
en lugar de conservarse para su inspección.

Las notificaciones también se detienen. Las alarmas de un inquilino eliminado ya no envían correo ni
disparan webhooks de notificación, y las alarmas abiertas dejan de escalar. Esto importa porque no
requiere tráfico de dispositivos: el escalado vuelve a avisar de las alarmas abiertas sin confirmar
mediante un temporizador. Sin este rechazo, se seguiría avisando a los destinatarios de guardia de
un inquilino eliminado sobre alarmas de un inquilino que ya no existe.

:::warning Los rechazos no son instantáneos
Una acción que ya estaba en curso cuando llega la eliminación se completará, y los rechazos empiezan
a aplicarse dentro del minuto siguiente a la eliminación, no al instante. Si una eliminación debe
garantizar que *nada* más llegue a un sistema externo o a un destinatario, deshabilita los
conectores y las políticas de notificación del inquilino antes de eliminarlo.
:::

## Qué puedes ver hoy {#visibility}

Un inquilino en proceso de eliminación tiene una pestaña **Eliminación** en su página de detalle en
la consola de administración. Responde a la pregunta que realmente se hace un operador, *¿ha
terminado y, si no, por qué no?*, con:

- una línea de estado en lenguaje claro;
- lo que esté bloqueando el proceso;
- una fila por sistema de almacenamiento que indica si ese sistema está limpio, si todavía retiene
  datos o si está reintentando tras un fallo.

Esos tres estados de fila se mantienen separados a propósito. Los datos que aún se retienen no se
liberarán hasta que alguien cambie algo, mientras que un fallo se resuelve por sí solo en el
siguiente barrido.

Algunos sistemas añaden una nota breve a una línea limpia, indicando lo que *decidieron no* revisar:
por ejemplo, una base de datos de telemetría que no existe en esta instancia, o las cachés internas
que una eliminación exime deliberadamente. Una nota es una aclaración sobre «limpio», no un problema
sobre el que haya que actuar.

Las eliminaciones completadas están en **Administración → Eliminaciones**, no en la página de un
inquilino. Al completarse una eliminación se elimina el inquilino, así que una eliminación terminada
ya no tiene página de inquilino donde aparecer. Esa lista, a nivel de instancia, es el registro
duradero: el identificador, cuándo se solicitó, cuándo se completó, cuánto se borró y qué informó
cada sistema de almacenamiento. Ábrela cuando alguien te pida demostrar que los datos de un cliente
se borraron.

Hay dos acciones que deliberadamente no ofrece:

- No hay **reintento**. Cada barrido ya reintenta, así que un botón daría a entender que no lo hace.
- No hay **forzar la finalización**. Es la única acción que podría dejar registrado un borrado que
  no ocurrió.
