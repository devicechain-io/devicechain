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

- **Calendarios distintos.** Los datos de núcleo son pequeños y cambian cuando alguien
  cambia algo, así que piden copias completas frecuentes y baratas. Los datos de
  eventos son volumen, casi siempre de anexado, y ya están sujetos a una política de
  retención ([ADR-026](../concepts/architecture.md)) — respaldar fragmentos que el
  reconciliador de ciclo de vida está a punto de eliminar es almacenamiento
  desperdiciado.
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
[ADR-059](../concepts/architecture.md)).

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

El orden importa. Sembrar la clave **primero**, restaurar los datos **después**: una
instancia construida sobre la clave equivocada escribirá secretos nuevos bajo ella, y
esos se convierten en daño colateral cuando corrija la clave más tarde.

**1. Reconstruya la instancia con la clave depositada.**

```bash
dcctl bootstrap local mi-instancia --restore-root-key ~/backups/mi-instancia-rootkey.escrow
```

Se le pedirá la frase de contraseña del artefacto (o puede proporcionarla con
`--escrow-passphrase-file` / `DCCTL_ESCROW_PASSPHRASE`). La instancia nueva ejecuta
ahora la *misma* clave raíz que la antigua.

**2. Aquiete la instancia, restaure los datos de núcleo y vuelva a levantarla.** La
instancia reconstruida ya ha arrancado y ha creado sus propios esquemas vacíos, así que
restaurar por debajo de un servicio en marcha implica eliminar tablas que tiene
abiertas y competir con sus migraciones. Escale primero las cargas de trabajo a cero:

```bash
kubectl -n mi-instancia get deploy -o custom-columns=NAME:.metadata.name,R:.spec.replicas --no-headers
kubectl -n mi-instancia scale deploy --all --replicas=0
# ... restaure su copia de PostgreSQL en la base de datos de la instancia ...
kubectl -n mi-instancia scale deploy/<nombre> --replicas=<n>   # según los valores anotados arriba
kubectl -n mi-instancia wait --for=condition=available deploy --all --timeout=300s
```

Anote los recuentos de réplicas antes de escalar a cero en lugar de suponer `1`: un
área escalada volvería más pequeña de lo que era.

**3. Restaure los datos de eventos** por separado, en TimescaleDB, cuando convenga a su
objetivo de tiempo de recuperación. El paso 4 no depende de ello.

**4. Confirme que los secretos almacenados se descifran**: lea un objeto respaldado por
un secreto (un conector de salida, un canal de notificación) desde la consola o la API.
Una restauración que devuelve filas no es una prueba; un valor que se descifra sí lo es.

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

Consérvelo mientras conserve cualquier copia de seguridad de las bases de datos de esa
instancia. Es lo único que todavía puede leerlas. Elimínelo cuando esas copias hayan
desaparecido, y no antes.
