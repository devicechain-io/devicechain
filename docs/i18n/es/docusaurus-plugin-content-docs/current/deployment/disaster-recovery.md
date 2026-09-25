---
sidebar_position: 6
title: Recuperación ante desastres
---

# Recuperación ante desastres

Para restaurar una instancia de DeviceChain necesitas dos cosas: una copia de seguridad de sus
bases de datos y la **clave raíz del almacén de secretos** de la instancia. Casi todos los
procedimientos de copia de seguridad capturan la primera y omiten la segunda en silencio. Esta
página explica qué es la clave raíz, cómo conservar una copia recuperable de ella y cómo
reconstruir una instancia a partir de ambas.

## Dos copias de seguridad, no una {#two-tiers}

Los datos de una instancia residen en dos servidores de base de datos distintos. Respáldalos y
restáuralos como dos operaciones separadas, no como una:

| | **Datos de núcleo** (PostgreSQL) | **Datos de eventos** (TimescaleDB) |
|---|---|---|
| Qué | Inquilinos, identidades, dispositivos, perfiles, reglas de detección, cuadros de mando, conectores, último estado conocido — **y todos los secretos almacenados** | Mediciones, histórico de eventos, agregados |
| Tamaño | Megabytes; crece con la *configuración* de tu flota | El nivel de volumen; crece con el tiempo |
| Perderlos | La instancia no se puede reconstruir | Se pierde el histórico; la instancia sigue funcionando |
| Necesita la clave raíz | **Sí** | No |

Esta división no es una política impuesta sobre una única base de datos: es como la plataforma ya
almacena las cosas.

- `event-management` es el propietario del almacén de eventos —su esquema, sus hypertables, sus
  políticas de retención— y es el único servicio que escribe telemetría en él.
- Todos los demás servicios guardan sus propios datos en el servidor relacional.
- El único otro servicio que llega a TimescaleDB es `user-management`. Usa una conexión invitada
  que no crea ningún esquema ni ejecuta migraciones, y solo para borrar las filas de un inquilino
  purgado.

Ninguna escritura abarca ambos almacenes, así que no hay nada que mantener transaccionalmente
consistente entre ellos.

Hay algo que importa cuando las dos mitades tienen puntos de recuperación distintos. La fila del
inquilino en el lado relacional es lo que impulsa ese borrado, y se elimina una vez terminado. Así
que, si restauras los datos de eventos a un punto *anterior* a la purga de un inquilino junto a
unos datos de núcleo restaurados a un punto *posterior* a la liberación de su fila, recuperas una
telemetría que ya nada volverá a borrar.

Planifica en torno a dos consecuencias:

- **Calendarios distintos.** Ambos almacenes se archivan de la misma forma: una copia base más un
  flujo continuo del registro de escritura anticipada (WAL). Pero no quieren la misma cadencia de
  copias base ni la misma retención. Los datos de núcleo son pequeños y cambian cuando alguien
  cambia algo. Los datos de eventos son volumen, casi siempre de anexado, y ya están sujetos a una
  política de retención ([ciclo de vida de los datos](../concepts/architecture.md)). Conservar
  copias base de fragmentos que el reconciliador de ciclo de vida está a punto de eliminar es
  pagar dos veces por almacenar las mismas filas.
- **Objetivos de recuperación distintos.** Restaurar solo los datos de núcleo te devuelve una
  instancia *operativa*: los dispositivos se reconectan, las reglas de detección se ejecutan, los
  comandos se despachan y los secretos se descifran. Restaurar los datos de eventos rellena el
  histórico. Una instancia sin sus datos de eventos está degradada (widgets de histórico vacíos),
  no caída, de modo que las dos mitades pueden tener objetivos de tiempo de recuperación
  genuinamente distintos.

La clave raíz solo condiciona la mitad de núcleo. Los datos de eventos no contienen texto cifrado,
así que una restauración de TimescaleDB no necesita nada de esta página. Todo lo que sigue trata
de los datos de núcleo.

## Por qué la clave raíz necesita su propio procedimiento {#root-key}

Todos los secretos que DeviceChain almacena se cifran en reposo con una clave de datos por secreto.
Eso incluye la clave que firma todos los tokens de inicio de sesión, las credenciales de los
conectores de salida, las contraseñas SMTP y las claves de proveedores de IA. Cada clave de datos
va a su vez envuelta por una única **clave raíz** de la instancia (la clave de cifrado de claves, o
KEK; véase [Arquitectura](../concepts/architecture.md)). Toda instancia almacena la clave de firma
de tokens, sea cual sea su perfil, así que toda instancia depende de su clave raíz: sin ella, nadie
puede iniciar sesión.

La clave raíz vive en el Secret de Kubernetes de la instancia, es decir, vive en **etcd**, y
ninguna copia de seguridad de bases de datos contiene etcd. Una copia de PostgreSQL archiva
PostgreSQL; una copia de TimescaleDB cubre TimescaleDB. Ninguna contiene un solo byte de la clave.

Esto produce un fallo que supera precisamente el simulacro que la mayoría de la gente hace:

- **Restaura las bases de datos en el mismo sitio**, en el mismo clúster, y todo funciona, porque
  etcd todavía tiene la clave. Este ensayo da una confianza falsa.
- **Restaura en un clúster nuevo** —el desastre real— y las filas cifradas se rehidratan
  perfectamente y la restauración informa de éxito, pero todos esos secretos quedan ilegibles para
  siempre. El clúster nuevo acuñó una clave raíz *distinta*, y la antigua no se puede derivar de
  nada de lo que aún conservas.

Antes esto solo aparecía más tarde, como un error de descifrado inexplicable mucho después de que
la copia de seguridad que podría haber ayudado ya hubiera rotado. Ahora lo detectan dos
comprobaciones, en momentos distintos y sobre pruebas distintas:

- **Antes de construir nada**, `dcctl bootstrap` le pregunta al almacén relacional qué contiene ya
  para la instancia. Una base de datos que está ahí sin que este clúster la haya construido solo
  puede haber sobrevivido al clúster que sí lo hizo, así que el bootstrap se detiene en lugar de
  acuñar una clave por encima de ella. Esta comprobación evita el error.
- **Al arrancar**, un servicio que almacena secretos comprueba su clave raíz contra sus propias
  filas almacenadas y se niega a servir si la clave no las abre. Esta comprobación detecta una
  clave equivocada en lugar de una ausente —una recuperación apuntada al artefacto incorrecto—,
  algo que ninguna lectura del almacén puede ver.

La comprobación al arrancar derriba toda la API, no solo las integraciones. `user-management`
almacena la clave de firma de tokens, así que es uno de los servicios que se niegan a arrancar.
Todos los demás servicios esperan las claves de firma de `user-management` antes de declararse
listos, así que ninguno sirve tampoco, y nadie puede iniciar sesión.

Ambas comprobaciones convierten el error en algo ruidoso e inmediato en lugar de lento y disperso.
Ninguna recupera nada. Si la clave se perdió, se perdió.

:::danger No hay recuperación posible tras perder la clave raíz
La clave son 256 bits de aleatoriedad y las claves de datos envueltas no se pueden descifrar por
fuerza bruta. Si la clave desaparece, los secretos desaparecen; un ticket de soporte no puede
recuperarlos. Eso incluye la clave de firma de tokens, así que una instancia cuya clave raíz se ha
perdido o es incorrecta **no arranca**: `user-management` se niega, y nada más llega a estar listo
sin él. Este es el único dato de DeviceChain sin segunda oportunidad, y por eso el depósito
descrito abajo está activado por defecto.
:::

## El artefacto de depósito

`dcctl bootstrap` escribe un **artefacto de depósito cifrado**: un pequeño archivo de texto que
contiene la clave raíz, sellada con una frase de contraseña que tú eliges.

```
~/.devicechain/escrow/<instance>-rootkey.escrow
```

El archivo es autodescriptivo. Alguien que nunca haya visto uno puede abrirlo dentro de años y
saber qué es, qué protege, qué ocurre si se pierde y el comando exacto para recuperar, sin
necesidad de esta página.

Importan dos propiedades:

- **No se guarda junto a la instancia.** Deliberadamente *no* vive en
  `~/.devicechain/instances/<instance>/`, porque [`dcctl destroy`](#after-destroy) elimina ese
  directorio. `dcctl` rechaza una ruta `--escrow-file` que esté dentro de él.
- **Lleva una huella de la clave en claro.** Eso te permite responder «¿sigue siendo este el
  depósito correcto?» *sin* la frase de contraseña. Consulta [verificación](#verify).

### Elegir una frase de contraseña

El bootstrap toma la frase de contraseña de la primera de estas fuentes que encuentre:

| Fuente | Cuándo usarla |
|--------|---------------|
| `--escrow-passphrase-file <path>` | Automatización con un gestor de secretos; se elimina el salto de línea final. |
| `DCCTL_ESCROW_PASSPHRASE` | CI e instalaciones automatizadas. Definida pero vacía es un error, no un respaldo. |
| Solicitud interactiva | Una persona en un terminal. Se pide dos veces, para detectar un error de tecleo ahora y no durante una recuperación. |

Si no hay ninguna disponible y no hay terminal donde preguntar, el bootstrap falla. Es deliberado:
la alternativa es producir en silencio una instancia cuyos secretos mueren con su clúster.

:::warning Guarda el archivo y la frase de contraseña por separado, y fuera del clúster
Ambos en el mismo sitio están a un compromiso de no ser ninguna protección. Ambos en el clúster
están a un desastre de no ser ninguna copia de seguridad.
:::

### Desactivarlo

Para una instancia realmente desechable —una ejecución de CI, una demostración, un experimento
local— pasa `--no-escrow`. `--dev` lo implica.

```bash
dcctl bootstrap local scratch --dev            # sin depósito, desechable por construcción
dcctl bootstrap local scratch --yes --no-escrow
```

El resumen del bootstrap lo indica entonces en rojo. No lo uses para nada cuyos secretos vayas a
echar de menos.

## Recuperar una instancia {#recover}

La recuperación **construye** un clúster nuevo y una instancia nueva sobre él. Deliberadamente no
hay un paso de «restaurar sobre la instancia en marcha»: restaurar por debajo de servicios que ya
han creado sus propios esquemas implica eliminar tablas que tienen abiertas y competir con sus
migraciones. Recupera reconstruyendo.

Recuperas las dos bases de datos con dos comandos, en este orden, porque los dos almacenes
pertenecen a cosas distintas:

- La base de datos relacional se instala una vez por clúster y contiene los datos de **todas** las
  instancias, así que la recupera `dcctl install`.
- El almacén de eventos pertenece a una instancia, así que lo recupera `dcctl bootstrap`.

Antes de empezar, decide el nombre de la instancia: una recuperación tiene que usar el nombre que
tenía la instancia. Consulta
[Recupera con el nombre propio de la instancia](#recover-under-the-instances-own-name).

**1. Recupera la base de datos relacional compartida** al preparar el clúster.

```bash
dcctl install local --restore-rdb-from dc-rdb
```

`--restore-rdb-from` nombra la carpeta dentro del bucket de respaldo. Para un almacén que nunca se
ha restaurado es `dc-rdb`, ya que el archivo histórico se escribe bajo el nombre propio del
clúster.

Añade `--restore-rdb-at`, una marca de tiempo RFC 3339 estrictamente anterior al daño, para el otro
tipo de desastre: los datos se destruyeron correctamente, por una migración errónea o un borrado
equivocado, y quieres el estado justo anterior.

Pasa el mismo `--backup-credentials-file` que usó la instalación original, para que el clúster
nuevo lea el archivo histórico que escribió el anterior.

La opción solo surte efecto cuando se *crea* el almacén relacional. Apuntada a un clúster que ya
tiene uno, no mueve ningún dato en lugar de funcionar a medias, y `dcctl` lo dice antes de aplicar
nada. Recupera instalando en un clúster cuyo almacén relacional no esté presente.

`dcctl` elige dónde archiva el almacén recuperado *después*, y no hay opción para ello. Una base de
datos recuperada que siguiera archivando en la ruta que leyó se detendría en su propia comprobación
de seguridad y se quedaría colgada al arrancar. `dcctl` le da una ruta propia y la conserva en
todas las ejecuciones posteriores.

:::warning Las filas vuelven; las claves no
Una copia de seguridad de base de datos no contiene ninguna clave raíz. Cada secreto del almacén
recuperado sigue sellado con la clave de la instancia que lo escribió, y esa clave solo vivía en el
clúster que acabas de perder. Reconstruye cada instancia con `--restore-root-key` en el paso 2.
:::

**2. Reconstruye la instancia con su clave raíz**, y con sus datos de eventos.

```bash
dcctl bootstrap local my-instance \
  --restore-root-key ~/backups/my-instance-rootkey.escrow \
  --restore-tsdb-from dc-tsdb-my-instance-1a2b3c4d
```

La clave raíz del almacén de secretos de la instancia se siembra desde el artefacto de depósito en
lugar de acuñarse. La instancia conserva la clave con la que se cifraron sus secretos, así que las
filas recuperadas en el paso 1 pueden descifrarse. Se te pedirá la frase de contraseña del
artefacto, o puedes proporcionarla con `--escrow-passphrase-file` / `DCCTL_ESCROW_PASSPHRASE`.

`dcctl bootstrap` se niega a hacerlo de otra manera; es la primera de las dos comprobaciones
descritas [arriba](#root-key). Antes de escribir nada, le pregunta al almacén recuperado qué
contiene ya bajo el nombre de esa instancia, y una ejecución sin `--restore-root-key` se detiene y
nombra la opción.

Esa negativa es la última línea de defensa, no la primera. Solo puede hablar por una instancia
cuyo artefacto de depósito siga existiendo. Una instancia arrancada con `--no-escrow` no tiene
ninguna segunda copia de su clave en ninguna parte, y nada puede volver a abrir esas filas.

`--restore-tsdb-from` es opcional e independiente. El almacén de eventos mantiene su propia línea
de tiempo a propósito: rebobinar la telemetría hasta ayer no significa que el plano de control deba
rebobinarse con ella, y el paso 3 no depende de ello. Admite `--restore-tsdb-at` para un punto en
el tiempo, de la misma forma. El almacén de eventos de cada instancia archiva bajo una ruta propia,
así que lee la ruta del archivo histórico en vez de adivinarla. `dc-tsdb` a secas es el nombre al
estilo relacional y no estará allí.

Una restauración es una de las pocas cosas que sí pueden ejecutarse contra una instancia que ya
existe. La recuperación es justamente la situación en la que una ejecución se interrumpe y hay que
reintentarla. Una guarda más precisa lo hace seguro: permite el reintento solo cuando el artefacto
de depósito lleva la clave sobre la que la instancia ya está funcionando.

**3. Confirma que la clave raíz es la del depósito** con `dcctl secrets escrow verify` (consulta
[Verificar el depósito](#verify)).

Poder iniciar sesión es la primera señal de que la clave es la correcta: `user-management` no
arranca hasta que la clave raíz abre su clave de firma de tokens sellada. Leer de vuelta un objeto
respaldado por un secreto —un conector de salida, un canal de notificación— es la comprobación más
fuerte. Está disponible en cuanto el paso 1 ha recuperado el almacén donde vive ese objeto. Una
restauración que devuelve filas no es una prueba; un valor que se descifra sí lo es.

**4. Si restauraste datos de eventos, revisa la maquinaria, no el número de filas.** Un almacén de
eventos recuperado puede conservar todas las filas y aun así haber dejado de ser, en silencio, una
base de datos de series temporales. Las tablas están ahí y las consultas responden; lo que falta es
el trabajo en segundo plano. Ese almacén responde consultas perfectamente durante el tiempo que
tarde el disco en llenarse.

Abre una shell en el primario del almacén de eventos. Bajo el operador CloudNativePG, `psql` no
necesita contraseña allí. El comando usa dos nombres construidos a partir del id de instancia, y no
son la misma cadena: el namespace es `dci-` más el id de instancia, mientras que la base de datos
es el id de instancia a secas, sin prefijo.

```bash
kubectl -n dci-<instance-id> exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d <instance-id>
```

Después hazle dos preguntas. Primero, ¿siguen siendo hypertables las tablas de eventos?

```sql
SELECT hypertable_schema, hypertable_name FROM timescaledb_information.hypertables;
```

Tus tablas de eventos viven en el esquema `event-management` y todas deberían aparecer. Una tabla
que volvió como tabla ordinaria es el fallo que esto detecta. Es invisible en un volcado de
esquema, donde una tabla normal y una hypertable se ven idénticas. (No busques
`measurement_rollups`. Es un agregado continuo, y la hypertable que lo respalda es interna, así que
su ausencia de esta lista es normal.)

Segundo, y esta es la que importa: ¿está realmente ejecutándose el planificador de trabajos _en
este clúster_? Ejecuta esto, espera un minuto o dos y ejecútalo de nuevo:

```sql
SELECT job_id, proc_name, total_runs, last_run_status
  FROM timescaledb_information.job_stats ORDER BY job_id;
```

**`total_runs` tiene que avanzar.** Esa es toda la comprobación. `total_runs` es un contador, así
que verlo avanzar observa trabajo ocurriendo en el clúster que tienes delante.

:::danger No juzgues esto por `next_start` ni por un indicador de programado
Un almacén recuperado cuyo planificador nunca arrancó sigue mostrando cada trabajo como programado,
con un `next_start` futuro perfectamente plausible, y se queda así para siempre. Parece sano
**porque los datos se restauraron, no porque algo vaya a ejecutarse nunca.**
:::

`next_start` es el campo obvio al que recurrir, y aquí no puede decirte nada. La tabla que lo
respalda es una tabla ordinaria, así que una restauración física la devuelve con los valores del
clúster *antiguo*.

### Comprueba que cada base de datos terminó de recuperarse {#check-recovery-finished}

Que el bootstrap termine no significa que la base de datos esté lista. `dcctl` informa de éxito en
cuanto las cargas de trabajo están arriba, lo que puede ocurrir antes de que la base de datos en
recuperación haya terminado de reproducir su archivo histórico. Una recuperación que no puede
alcanzar su archivo se queda esperando en lugar de fallar. Así que comprueba la propia base de
datos antes de dar por buena una restauración:

```bash
kubectl get clusters.postgresql.cnpg.io --all-namespaces
```

La base de datos relacional (`dc-rdb`) está en `dc-system`; el almacén de eventos (`dc-tsdb`) está
en el namespace propio de la instancia.

Buscas `Cluster in healthy state`. Un clúster atascado en `Setting up primary` no se ha recuperado.
Lo más habitual es que el archivo histórico sea inalcanzable, o que `--restore-rdb-from` /
`--restore-tsdb-from` indique una ruta que no existe en el bucket.

### Recupera con el nombre propio de la instancia {#recover-under-the-instances-own-name}

Una recuperación tiene que usar el nombre que tenía la instancia. La base de datos relacional lleva
el nombre de la instancia, así que la restauración del paso 1 la devuelve con el nombre *antiguo*,
propiedad del login de esa misma instancia.

Un bootstrap ejecutado con `--restore-root-key` bajo un nombre *distinto* consulta el almacén y no
encuentra allí ninguna base de datos suya. En un clúster que tampoco contiene una instancia a medio
construir con ese nombre, se detiene antes de escribir nada:

```text
--restore-root-key recovers the key that opens instance "<new-name>"'s stored secrets, and
there is nothing here for it to open: the relational store holds no database for "<new-name>" ...
```

Se niega porque la alternativa es una instancia *vacía* sellada con una clave recuperada, que desde
fuera parece una recuperación exitosa y no contiene ninguno de los datos por los que volviste. El
aviso amarillo `note: ... was escrowed for instance "<old-name>"` impreso antes en la misma
ejecución es la advertencia, no la negativa.

Para superarla, repite el paso 2 con el nombre original: el que `dcctl secrets escrow show` muestra
como `Instance:` para el artefacto, y el que lleva la base de datos recuperada.

:::warning Decide el nombre antes de empezar
La negativa llega *después* de que la restauración relacional ya se haya ejecutado, así que decide
el nombre antes de empezar, no durante el incidente.
:::

El artefacto sí registra el nombre para el que se escribió, y ese nombre está autenticado: no se
puede editar sin invalidar el archivo. Renombrar una instancia es una migración, no una
restauración, y hoy la plataforma no lo hace.

## Verifica el depósito antes de necesitarlo {#verify}

Un depósito que ya no corresponde a la clave en uso es indistinguible de uno bueno hasta el día en
que lo usas. Compruébalo un martes cualquiera:

```bash
dcctl secrets escrow verify ~/backups/my-instance-rootkey.escrow --instance my-instance
```

Esto compara la huella del artefacto con la clave que la instancia **realmente está usando**. No
necesita frase de contraseña y termina con un código distinto de cero si no coinciden, así que
encaja en un cron o en una puerta de CI. Una discrepancia significa que la instancia no tiene un
depósito utilizable, casi siempre porque se volvió a hacer bootstrap después de escribir el
archivo.

Para ver qué es un artefacto sin abrirlo:

```bash
dcctl secrets escrow show ~/backups/my-instance-rootkey.escrow
```

:::warning Lo que `verify` no demuestra
Demuestra que el artefacto nombra la clave correcta. No demuestra que el artefacto todavía *se
abra*; eso requiere la frase de contraseña. Ensaya una recuperación real periódicamente: una
comprobación de huella es un detector de humo, no un simulacro de incendio.
:::

## Credenciales durante el bootstrap y la actualización {#credentials-and-the-two-commands-that-touch-them}

Dos comandos tocan las credenciales de una instancia. `dcctl bootstrap` las crea; `dcctl upgrade`
las conserva.

`dcctl bootstrap` **acuña** todas las credenciales que tiene una instancia, porque ninguna de ellas
existe todavía. Por eso se niega a ejecutarse contra una instancia que ya está viva: no hay ningún
orden en el que entregarle credenciales nuevas a una instancia en funcionamiento sea seguro.

- Reescribir la clave raíz vuelve permanentemente ilegible todo secreto almacenado.
- Reescribir las credenciales del broker es recuperable pero disruptivo. El broker y los servicios
  se actualizan por mecanismos distintos y en momentos distintos, así que unas credenciales nuevas
  abren una ventana en la que un lado rechaza al otro, y los pods que arrancan dentro de ella no
  llegan a conectarse al broker.

`dcctl upgrade` es el comando que actúa sobre una instancia viva, y **no acuña nada**. Vuelve a
leer y conserva todos estos:

- la clave raíz del almacén de secretos;
- la autoridad y los inicios de sesión del broker (la semilla del emisor de callout y las
  contraseñas de servicio y de sistema);
- las contraseñas de las bases de datos (la del propietario compartido, la del aprovisionador, la
  del login propio de la instancia y la del propietario del almacén de eventos);
- el secreto de autenticación entre servicios;
- cuando el monitoreo y las copias de seguridad dentro del clúster están habilitados, la contraseña
  de administrador de Grafana y las credenciales del almacén de objetos.

Un cambio de versión no puede convertirse en un cambio de credenciales.

### Terminar un bootstrap que falló a mitad de camino {#resuming-a-bootstrap}

La negativa se basa en el **documento de configuración** de la instancia, que se escribe cerca del
final de la ejecución. Todo lo que se quede antes de eso es una instancia a medio construir y no
una instancia viva, y volver a ejecutar el bootstrap es la manera admitida de terminarla.

El broker es la razón por la que esa ventana tiene que quedar abierta. Se configura varios pasos
antes que la instancia en sí. Un bootstrap que falle en ese intervalo deja un broker en
funcionamiento que ninguna ejecución posterior podría reconocer solo a partir del clúster: allí
únicamente quedan una clave pública y dos hashes de contraseña, y ninguno de ellos puede
convertirse de vuelta en las credenciales que necesitan los servicios.

Por eso `dcctl` también registra las credenciales del broker en la máquina desde la que lo
ejecutas, en `~/.devicechain/instances/<instance>/`, antes de configurar el broker con ellas. Una
ejecución posterior las reutiliza desde ahí cuando todavía no hay una instancia a la que preguntar.
El archivo solo es legible por ti, y `dcctl destroy` lo elimina junto con el resto del estado local
de la instancia.

### El depósito se reconcilia en cada actualización {#escrow-reconcile}

Comprobar un depósito no necesita frase de contraseña. El artefacto registra una huella de la clave
que protege, así que compararla con la clave que la instancia está usando no abre nada. Por eso
`dcctl upgrade` lo hace cada vez, sin pedirte nada:

| Artefacto de depósito | Qué hace la actualización |
|---|---|
| Coincide con la clave en uso | Lo confirma y lo deja intacto. |
| **No** coincide | Avisa con claridad y lo nombra por lo que muy probablemente es: un depósito que pertenece a una instancia anterior con el mismo nombre. Restaurar desde él recuperaría un clúster incapaz de leer sus propios secretos. |
| No existe | Escribe uno, si pasaste `--escrow-passphrase-file` (o definiste `DCCTL_ESCROW_PASSPHRASE`). Así es como una instancia creada inicialmente con `--no-escrow` obtiene un depósito más tarde. Si no hay ninguna frase de contraseña disponible, avisa en su lugar y no la pide: una actualización se ejecuta desatendida y, a estas alturas, ya ha movido la instancia. |

Ninguno de esos desenlaces hace fallar la actualización. Un problema de depósito trata de un
desastre futuro, mientras que la actualización que tienes delante trata de la instancia en
funcionamiento. Y un operador que no puede actualizar rodeará la comprobación en lugar de
arreglarla.

## Después de `dcctl destroy` {#after-destroy}

`dcctl destroy` elimina la instancia —su release de Helm, su broker NATS y su almacén de eventos,
su base de datos y su login de base de datos, su namespace— y su estado local. **No** elimina el
artefacto de depósito, que vive fuera de ese directorio por diseño. Destroy nombra el artefacto al
terminar.

Destroy nunca elimina el clúster, ni los requisitos previos que `dcctl install` dejó en él: la base
de datos relacional que usan las demás instancias y el almacén de objetos de las copias de
seguridad siguen donde están.

Conserva el artefacto de depósito mientras conserves cualquier copia de seguridad de las bases de
datos de esa instancia. Es lo único que todavía puede leerlas. Elimínalo cuando esas copias hayan
desaparecido, y no antes.
