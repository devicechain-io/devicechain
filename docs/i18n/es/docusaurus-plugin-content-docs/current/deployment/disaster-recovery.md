---
sidebar_position: 5
title: Recuperación ante desastres
---

# Recuperación ante desastres

Restaurar una instancia de DeviceChain requiere **dos** cosas: una copia de seguridad
de sus bases de datos y la **clave raíz del almacén de secretos** de la instancia.
Casi todos los procedimientos de copia de seguridad capturan la primera y omiten la
segunda en silencio.

Esta página trata de la segunda.

## Dos copias de seguridad, no una {#two-tiers}

Los datos de una instancia residen en **dos servidores de base de datos distintos**,
y conviene respaldarlos y restaurarlos como dos operaciones separadas, no como una:

| | **Datos de núcleo** (PostgreSQL) | **Datos de eventos** (TimescaleDB) |
|---|---|---|
| Qué | Tenants, identidades, dispositivos, perfiles, reglas de detección, cuadros de mando, conectores, último estado conocido — **y todos los secretos almacenados** | Mediciones, histórico de eventos, agregados |
| Tamaño | Megabytes; crece con la *configuración* de su flota | El volumen grande; crece con el tiempo |
| Perderlos | La instancia no se puede reconstruir | Se pierde el histórico; la instancia sigue funcionando |
| Necesita la clave raíz | **Sí** | No |

No es una política impuesta sobre una única base de datos: es como la plataforma ya
almacena las cosas. `event-management` es el único servicio que habla con TimescaleDB,
y no habla con ningún otro almacén; todos los demás servicios viven íntegramente en el
servidor relacional. No hay escrituras cruzadas que mantener consistentes entre ambos.

Dos consecuencias que conviene planificar:

- **Calendarios distintos.** Ambos almacenes se archivan de la misma forma —una copia
  base más un flujo continuo del registro de escritura anticipada (WAL)—, pero no
  quieren la misma cadencia de copias base ni la misma retención. Los datos de núcleo
  son pequeños y cambian cuando alguien cambia algo. Los datos de eventos son volumen,
  casi siempre de anexado, y ya están sujetos a una política de retención
  ([ciclo de vida de los datos](../concepts/architecture.md)) — conservar copias base de
  fragmentos que el reconciliador de ciclo de vida está a punto de eliminar es pagar
  dos veces por almacenar las mismas filas.
- **Objetivos de recuperación distintos.** Restaurar solo los datos de núcleo le
  devuelve una instancia *operativa*: los dispositivos se reconectan, las reglas de
  detección se ejecutan, los comandos se despachan, los secretos se descifran.
  Restaurar los datos de eventos rellena el histórico. Una instancia sin sus datos de
  eventos está degradada —widgets de histórico vacíos—, no caída, de modo que ambas
  mitades pueden tener objetivos de tiempo de recuperación genuinamente distintos.

**La clave raíz solo condiciona la mitad de núcleo.** Los datos de eventos no
contienen texto cifrado, así que una restauración de TimescaleDB no necesita nada de
esta página. Todo lo que sigue trata de los datos de núcleo.

## Por qué la clave raíz necesita su propio procedimiento

Todos los secretos que DeviceChain almacena por usted —credenciales de conectores de
salida, contraseñas SMTP, claves de proveedores de IA— se cifran en reposo con una
clave de datos por secreto, y cada una de esas claves de datos va envuelta por una
única **clave raíz** de la instancia (la KEK; véase
[Arquitectura](../concepts/architecture.md)).

Esa clave raíz vive en el Secret de Kubernetes de la instancia, es decir, vive en
**etcd**, y ninguna copia de seguridad de bases de datos contiene etcd. Una copia de
PostgreSQL archiva PostgreSQL; una copia de TimescaleDB cubre TimescaleDB. Ninguna
contiene un solo byte de la clave.

La consecuencia es un fallo que supera precisamente el simulacro que la mayoría de la
gente hace:

- **Restaurar las bases de datos en el mismo sitio** —en el mismo clúster— y todo
  funciona, porque etcd todavía tiene la clave. Este es el ensayo que da una confianza
  falsa.
- **Restaurar en un clúster nuevo** —el desastre real— y las filas cifradas se
  rehidratan perfectamente, la restauración informa de éxito, y todos esos secretos
  quedan ilegibles para siempre. El clúster nuevo acuñó una clave raíz *distinta*, y
  la antigua no se puede derivar de nada de lo que aún conserva.

El fallo no aparece durante la restauración. Aparece después, como un error de
descifrado inexplicable, normalmente mucho tiempo después de que la copia de seguridad
que podría haber ayudado ya haya rotado.

:::danger No hay recuperación posible tras perder la clave raíz
La clave son 256 bits de aleatoriedad y las claves de datos envueltas no son
descifrables por fuerza bruta. Si la clave desaparece, los secretos desaparecen: un
ticket de soporte no puede recuperarlos. Este es el único dato de DeviceChain sin
segunda oportunidad, y por eso el depósito descrito abajo está activado por defecto.
:::

## El artefacto de depósito

`dcctl bootstrap` escribe un **artefacto de depósito cifrado**: un pequeño archivo de
texto que contiene la clave raíz, sellada con una frase de contraseña que usted elige:

```
~/.devicechain/escrow/<instancia>-rootkey.escrow
```

Es un archivo de texto autodescriptivo. Si alguien lo abre dentro de años sin haber
visto uno nunca, el propio archivo explica qué es, qué protege, qué ocurre si se
pierde y el comando exacto de recuperación, sin necesidad de esta página.

Dos propiedades conviene conocer:

- **No se guarda junto a la instancia.** Deliberadamente *no* vive en
  `~/.devicechain/<instancia>/`, porque [`dcctl destroy`](#after-destroy) elimina ese
  directorio. `dcctl` rechaza una ruta `--escrow-file` que esté dentro de él.
- **Lleva una huella de la clave en claro.** Eso es lo que permite responder «¿sigue
  siendo este el depósito correcto?» *sin* la frase de contraseña; véase
  [verificación](#verify).

### Elegir una frase de contraseña

El bootstrap toma la frase de contraseña de la primera de estas fuentes que encuentre:

| Fuente | Cuándo usarla |
|--------|---------------|
| `--escrow-passphrase-file <ruta>` | Automatización con un gestor de secretos; se elimina el salto de línea final. |
| `DCCTL_ESCROW_PASSPHRASE` | CI e instalaciones automatizadas. Definida pero vacía es un error, no un respaldo. |
| Solicitud interactiva | Una persona en un terminal. Se pide dos veces, para detectar un error de tecleo ahora y no durante una recuperación. |

Si no hay ninguna disponible y no hay terminal donde preguntar, **el bootstrap
falla**. Es deliberado: la alternativa es producir en silencio una instancia cuyos
secretos mueren con su clúster.

:::caution Guarde el archivo y la frase de contraseña por separado, y fuera del clúster
Ambos en el mismo sitio están a un compromiso de no ser ninguna protección, y ambos en
el clúster están a un desastre de no ser ninguna copia de seguridad.
:::

### Desactivarlo

Para una instancia realmente desechable —una ejecución de CI, una demostración, un
experimento local— use `--no-escrow`. `--dev` lo implica.

```bash
dcctl bootstrap local scratch --dev            # sin depósito, desechable por construcción
dcctl bootstrap local scratch --yes --no-escrow
```

El resumen del bootstrap lo indica entonces en rojo. No lo use para nada cuyos
secretos vaya a echar de menos.

## Recuperar una instancia {#recover}

La recuperación es **un solo comando**, y es un comando que construye una instancia
nueva. La base de datos se recupera desde su archivo histórico en el momento en que se
crea el clúster, antes de que ningún servicio se conecte a ella, sembrada con la clave
raíz de su artefacto de depósito.

Por eso aquí no hay un paso de «restaurar sobre la instancia en marcha». No existe una
forma soportada de hacerlo, deliberadamente: restaurar por debajo de servicios que ya
han creado sus propios esquemas implica eliminar tablas que tienen abiertas y competir
con sus migraciones. **Recupere reconstruyendo.**

**1. Reconstruya la instancia recuperando la base de datos y la clave a la vez.**

```bash
dcctl bootstrap local mi-instancia \
  --restore-root-key ~/backups/mi-instancia-rootkey.escrow \
  --restore-rdb-from dc-rdb
```

`--restore-rdb-from` indica la **ruta del archivo histórico dentro de su bucket de
copias**: el `serverName` bajo el que escribía la instancia antigua, `dc-rdb` salvo que
lo haya cambiado. Se le pedirá la frase de contraseña del artefacto (o puede
proporcionarla con `--escrow-passphrase-file` / `DCCTL_ESCROW_PASSPHRASE`).

Dos cosas que este comando no le permitirá hacer:

- **Recuperar datos sin la clave.** `--restore-rdb-from` por sí solo se rechaza.
  Rehidrataría todas las filas y acuñaría una clave raíz *nueva*, de modo que la
  restauración informaría de éxito y todos los secretos almacenados quedarían
  ilegibles para siempre: el único fallo que es invisible en el momento en que ocurre.
- **Recuperar sobre una instancia en uso.** La opción solo surte efecto cuando se
  *crea* el clúster de base de datos. Volver a ejecutarla contra una instancia que ya
  existe no hace absolutamente nada, en lugar de funcionar a medias.

La instancia recuperada empieza de inmediato a archivar bajo una ruta **nueva**, propia,
de modo que no puede sobrescribir el archivo histórico del que acaba de nacer. El
resumen del bootstrap imprime el nombre que eligió.

**2. Retroceda a un punto en el tiempo**, si el desastre fue que los datos se
destruyeron *correctamente*: una migración defectuosa, un borrado masivo por error.
Añada `--restore-rdb-at` con una marca de tiempo RFC 3339 estrictamente anterior al
daño:

```bash
dcctl bootstrap local mi-instancia \
  --restore-root-key ~/backups/mi-instancia-rootkey.escrow \
  --restore-rdb-from dc-rdb \
  --restore-rdb-at 2026-03-14T09:15:00Z
```

**3. Restaure los datos de eventos** por separado con `--restore-tsdb-from` (y
opcionalmente `--restore-tsdb-at`), cuando convenga a su objetivo de tiempo de
recuperación. Los dos almacenes mantienen líneas de tiempo independientes a propósito:
rebobinar la telemetría hasta ayer no significa que el plano de control deba rebobinarse
con ella. El paso 4 no depende de esto.

**4. Confirme que los secretos almacenados se descifran**: lea un objeto respaldado por
un secreto (un conector de salida, un canal de notificación) desde la consola o la API.
Una restauración que devuelve filas no es una prueba; un valor que se descifra sí lo es.

**5. Si restauró datos de eventos, revise la maquinaria y no el número de filas.** Un
almacén de eventos recuperado puede conservar todas las filas y haber dejado de ser en
silencio una base de datos de series temporales: las tablas están ahí, las consultas
responden, y lo que falta es el trabajo en segundo plano. Ese almacén responderá
consultas perfectamente el tiempo que tarde el disco en llenarse.

Abra una sesión en el primario del almacén de eventos; bajo el operador, `psql` no
necesita contraseña allí:

```bash
kubectl -n dc-system exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d devicechain
```

Hágale dos preguntas. **Primero: ¿siguen siendo hypertables las tablas de eventos?**

```sql
SELECT hypertable_schema, hypertable_name FROM timescaledb_information.hypertables;
```

Sus tablas de eventos viven en el esquema `event-management` y todas deberían aparecer.
Una tabla que volvió como tabla ordinaria es el fallo que esto detecta, y es invisible
en un volcado de esquema: una tabla normal y una hypertable se ven idénticas ahí. (No
busque `measurement_rollups`. Es un agregado continuo, y la hypertable que lo respalda
es interna, así que su ausencia de esta lista es normal.)

**Segundo — y este es el que importa — ¿está realmente ejecutándose el planificador de
trabajos _en este clúster_?** Ejecute esto, espere un minuto o dos, y ejecútelo de nuevo:

```sql
SELECT job_id, proc_name, total_runs, last_run_status
  FROM timescaledb_information.job_stats ORDER BY job_id;
```

**`total_runs` tiene que MOVERSE.** Esa es toda la comprobación.

:::danger No juzgue esto por `next_start` ni por un indicador de "programado"
Es el campo obvio al que recurrir y aquí no puede decirle nada. La tabla que respalda a
`next_start` es una tabla ordinaria, así que una restauración física la devuelve con los
valores del clúster *antiguo*. Un almacén recuperado cuyo planificador nunca arrancó
muestra cada trabajo como programado y con un `next_start` futuro perfectamente
plausible, y se queda así para siempre. Parece sano **porque los datos se restauraron,
no porque algo vaya a ejecutarse.** `total_runs` es un contador, así que verlo avanzar
observa trabajo ocurriendo en el clúster que tiene delante.
:::

:::caution Que el bootstrap termine no significa que la base de datos esté lista
`dcctl` informa de éxito en cuanto las cargas de trabajo están arriba, lo que puede
ocurrir antes de que la base de datos en recuperación haya terminado de reproducir su
archivo histórico. Si una recuperación no puede alcanzar su archivo, se queda esperando
en lugar de fallar, así que compruebe la propia base de datos antes de dar por buena una
restauración:

```bash
kubectl -n dc-system get clusters.postgresql.cnpg.io
```

Debe ver `Cluster in healthy state`. Un clúster atascado en `Setting up primary` no se
ha recuperado: lo más habitual es que el archivo histórico sea inalcanzable, o que
`--restore-rdb-from` indique una ruta que no existe en el bucket.
:::

:::note Restaurar con otro nombre de instancia
Está perfectamente soportado: el artefacto registra el nombre para el que se escribió y
`dcctl` señala la discrepancia en lugar de rechazarla. El nombre registrado está
autenticado, así que no se puede editar sin invalidar el archivo.
:::

## Verificar el depósito antes de necesitarlo {#verify}

Un depósito que ya no corresponde a la clave en uso es indistinguible de uno bueno
hasta el día en que se usa. Compruébelo un martes cualquiera:

```bash
dcctl secrets escrow verify ~/backups/mi-instancia-rootkey.escrow --instance mi-instancia
```

Esto compara la huella del artefacto con la clave que la instancia **realmente
ejecuta**, no necesita frase de contraseña y termina con código distinto de cero si no
coinciden, de modo que encaja en un cron o en una puerta de CI. Una discrepancia
significa que la instancia no tiene un depósito utilizable, casi siempre porque se
volvió a hacer bootstrap después de escribir el archivo.

Para ver qué es un artefacto sin abrirlo:

```bash
dcctl secrets escrow show ~/backups/mi-instancia-rootkey.escrow
```

:::caution Lo que `verify` no demuestra
Demuestra que el artefacto nombra la clave correcta. No demuestra que el artefacto
todavía *se abra*: eso requiere la frase de contraseña. Ensaye una recuperación real
periódicamente; una comprobación de huella es un detector de humo, no un simulacro de
incendio.
:::

## Volver a ejecutar bootstrap sobre una instancia en uso

`dcctl bootstrap` es idempotente, y volver a ejecutarlo sobre una instancia existente
**reutiliza todas las credenciales que esa instancia ya está usando** en lugar de
acuñar nuevas. Eso abarca la clave raíz del almacén de secretos, la contraseña de
servicio de NATS, la clave del emisor del callout y el secreto de autenticación entre
servicios. Si no puede determinar si la instancia existe, se detiene en vez de suponer:
acuñar sería la respuesta destructiva.

El bróker es un caso especial, porque se configura varios pasos antes que la instancia
en sí. Un bootstrap que falle en ese intervalo deja un bróker en funcionamiento que
ninguna ejecución posterior podría reconocer solo a partir del clúster: allí únicamente
quedan una clave pública y dos hashes de contraseña, y ninguno de ellos puede
convertirse de vuelta en las credenciales que necesitan los servicios. Por eso las
credenciales del bróker también se registran en la máquina desde la que ejecutas
`dcctl`, en `~/.devicechain/<instancia>/`, antes de configurar el bróker con ellas; y
una nueva ejecución las reutiliza desde ahí cuando todavía no hay una instancia a la
que preguntar. El archivo solo es legible por ti, y `dcctl destroy` lo elimina junto
con el resto del estado local de la instancia.

Por qué importa difiere según la credencial. Reescribir la clave raíz vuelve
permanentemente ilegible todo secreto almacenado. Reescribir las credenciales del
bróker es recuperable pero disruptivo: el bróker y los servicios se actualizan por
mecanismos distintos y en momentos distintos, así que unas credenciales nuevas abren
una ventana en la que un lado rechaza al otro, y los pods que arrancan dentro de ella
no llegan a conectarse al bróker.

:::note Una excepción
El secreto de cliente OAuth de Grafana (`--grafana-sso`) se sigue reacuñando en cada
ejecución, porque su texto en claro vive en la configuración de Grafana y no en la
configuración de la instancia. Ambas mitades se reescriben en la misma ejecución, así
que el efecto es una breve ventana de inicios de sesión fallidos en Grafana durante el
despliegue.
:::

En una nueva ejecución también reconcilia el depósito:

- el artefacto coincide con la clave en uso → confirmado, se deja intacto;
- el artefacto **no** coincide → la ejecución se detiene y lo señala como huérfano;
- no hay artefacto → se escribe uno, de modo que una instancia creada inicialmente con
  `--no-escrow` puede obtener un depósito más tarde.

## Después de `dcctl destroy` {#after-destroy}

`dcctl destroy` elimina el clúster y el estado local de la instancia, pero **no** el
artefacto de depósito, que vive fuera de ese directorio por diseño y que destroy
nombra al terminar.

Hay un caso en el que elimina menos: una instancia arrancada dentro de un clúster que
creaste tú mismo (con `--kube-context`) deja ese clúster **en marcha**. La instancia se
desinstala de él y su estado local —incluidos los vecinos del depósito— se limpia, pero
el clúster no es de dcctl para borrarlo. Lo dice, y nombra el clúster, al terminar.
`dcctl instances list` muestra qué instancias están en ese estado.

Consérvelo mientras conserve cualquier copia de seguridad de las bases de datos de esa
instancia. Es lo único que todavía puede leerlas. Elimínelo cuando esas copias hayan
desaparecido, y no antes.
