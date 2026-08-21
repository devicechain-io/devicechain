---
sidebar_position: 3
title: Versiones y actualizaciones
---

# Versiones y actualizaciones

DeviceChain se distribuye como un conjunto de imágenes de contenedor precompiladas y versionadas,
más un chart de Helm. **No** necesita compilar nada para ejecutarlo: descargue una versión publicada,
instale el chart y actualice in situ sin tiempo de inactividad.

:::warning No se puede actualizar a la v0.9.0 ni a la v0.10.0
Hasta ahora, dos versiones exigen recrear la instancia en lugar de actualizarla:

- La **`v0.9.0`** reemplazó la cadena de migraciones de cada servicio por una única línea base
  congelada, por lo que una base de datos `v0.8.x` falla con `already exists` al encontrarla.
  Consulte [La compactación de la línea base de la v0.9.0](#v090-baseline-squash).
- La **`v0.10.0`** cambió la clave primaria de las tablas de eventos para corregir un defecto que
  descartaba telemetría de forma silenciosa. Consulte
  [El cambio de clave de eventos de la v0.10.0](#v0100-event-key).

Si está en cualquiera de las versiones anteriores, lea la sección correspondiente más abajo antes
de hacer nada más.
:::

:::caution Actualizar a la v0.12.0 requiere algunos cambios previos
La `v0.12.0` se actualiza en el sitio, pero cambia el tema en el que un dispositivo responde
a un comando, mueve un permiso y cambia varias cosas cuya forma se mantuvo igual. Un
`helm upgrade` informará éxito en cualquier caso. Lea
[v0.12.0: una actualización que cambia contratos](#v0120-upgrade) antes de empezar.
:::

## Modelo de versionado

Cada versión es una única etiqueta git de versión semántica (`vX.Y.Z`). Ese único número cubre
**todo en conjunto**: cada imagen de servicio, el operador, el chart de Helm y la CLI
`dcctl` se publican todos con la misma versión. No hay desfase de versión por servicio
del que preocuparse: un despliegue es un único número coherente.

Mantenerlo así requiere dos comandos en lugar de uno, porque el operador no forma parte del
chart: consulte [Actualizaciones sin tiempo de inactividad](#zero-downtime-upgrades) para el
procedimiento.

- Las **versiones estables** son `vX.Y.Z` (por ejemplo, `v1.2.0`). La etiqueta `:latest` sigue a la
  versión estable más reciente.
- Las **versiones preliminares** son `vX.Y.Z-rc.N` (por ejemplo, `v1.2.0-rc.1`). Estas nunca mueven `:latest`.

## Estabilidad previa a 1.0 {#pre-10-stability}

:::warning DeviceChain es previo a 1.0

Hasta la **v1.0.0**, cualquier versión —incluida una versión de parche— puede cambiar APIs, esquemas o
comportamiento sin una capa de compatibilidad. Esto es deliberado: mientras el modelo de datos aún se
está asentando, preferimos un cambio limpio antes que cargar con una capa de compatibilidad que
tendríamos que mantener para siempre.

**Cada cambio disruptivo se indica al principio de las notas de esa versión. Léalas antes de
actualizar.** Son la lista autorizada; el número de versión por sí solo no le indica si
una versión es segura para su despliegue.

:::

En concreto, antes de la v1.0.0 debe esperar que una versión pueda:

- **endurecer la validación**, de modo que una solicitud que antes tenía éxito ahora sea rechazada, por lo general
  porque se estaba aceptando silenciosamente o descartando silenciosamente
- **cambiar o eliminar un campo GraphQL**, en lugar de marcarlo obsoleto durante un ciclo
- **alterar el esquema de la base de datos** de formas que una reversión no deshará
- **reemplazar por completo la línea base de migraciones**, lo que elimina por entero la ruta de
  actualización en lugar de limitarse a hacerla unidireccional. Cuando eso ocurre, las notas de la versión lo
  indican al principio, y la única vía es recrear la instancia. La `v0.9.0` y la `v0.10.0` son versiones de este tipo

La propiedad de "actualizar in situ sin tiempo de inactividad" descrita arriba describe la *mecánica* de una
actualización progresiva. No es una promesa de que sus llamadas a la API existentes conserven el mismo significado
a través de un incremento de versión previo a 1.0.

Una vez que se publique la v1.0.0, esta sección se reemplaza por una promesa de compatibilidad de versionado
semántico normal: cambios disruptivos solo en una versión mayor.

Debido a que las versiones son frecuentes antes de la disponibilidad general (GA), la versión **menor** marca un hito
(una funcionalidad o subsistema significativo que se lanza) y la versión de **parche** lleva el ritmo continuo
de correcciones y endurecimiento. Una versión de parche no es automáticamente una actualización de bajo riesgo
durante este período; nuevamente, las notas de la versión son las que se lo indican.

## Imágenes

Las imágenes se publican en el Registro de Contenedores de GitHub público bajo
`ghcr.io/devicechain-io`, por ejemplo, `ghcr.io/devicechain-io/device-management`. Son
multiarquitectura (`linux/amd64` y `linux/arm64`) y se construyen sobre una base distroless sin
privilegios de root, por lo que se ejecutan como un usuario sin privilegios, sin shell y con una superficie de ataque mínima.

Debido a que el registro es público, no se requieren credenciales para descargar las imágenes publicadas.

## Instalación de una versión específica

Fije la etiqueta de imagen a la versión que desea:

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

El chart de Helm en sí también se publica como un artefacto OCI, por lo que puede instalarlo sin una
copia local del repositorio:

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version 1.2.0 \
  --set instance.id=devicechain \
  --set image.tag=v1.2.0
```

El chart también está publicado en
[Artifact Hub](https://artifacthub.io/packages/helm/devicechain/devicechain), que muestra
cada versión publicada junto con sus valores predeterminados y sus plantillas renderizadas.

## Actualizaciones sin tiempo de inactividad {#zero-downtime-upgrades}

Actualizar a una nueva versión son **dos comandos**, y el chart y los servicios están diseñados
para hacer avanzar a los clientes sin perder tráfico. Hasta ahora hay tres versiones que son
excepciones, todas documentadas más abajo: la transición a la ingesta duradera, que sigue siendo
una actualización corriente pero tiene un efecto secundario visible, y la **`v0.9.0` y la
`v0.10.0`, a las que no se puede actualizar en absoluto**. Consulte las notas de la versión a la
que va a migrar antes de ejecutarlos:

```bash
# 1. Los servicios. Traslade los valores de la versión actual y cambie únicamente
#    la versión. Este archivo contiene los secretos de su instancia: elimínelo al terminar.
helm get values dc -n default -o yaml > dc-values.yaml

helm upgrade dc deploy/helm/devicechain \
  -n default \
  -f dc-values.yaml \
  --set image.tag=v1.3.0

rm dc-values.yaml

# 2. El operador. No forma parte del chart, así que `helm upgrade` no puede moverlo.
dcctl upgrade local devicechain --version v1.3.0
```

:::warning Ambos pasos, siempre: el segundo no es opcional
El operador (sus CRD, su RBAC y su controlador) **no lo instala el chart de Helm**. `dcctl` lo
aplica a partir de manifiestos incrustados en el propio binario, de modo que `helm upgrade` no
tiene forma de alcanzarlo, y una actualización que se detenga tras el paso 1 deja su instancia
ejecutando los servicios nuevos contra el controlador con el que se arrancó por primera vez:
indefinidamente y sin ningún error que se lo indique.

`dcctl upgrade` no toca nada más. No ejecuta la actualización de Helm, no aplica la pila de
infraestructura y **no genera, lee ni rota ninguna credencial**, por lo que es seguro en una
instancia en funcionamiento. Úselo en lugar de volver a ejecutar `dcctl bootstrap`, que sí rota
todas las credenciales generadas.

Pase la misma versión a ambos pasos. Ejecute `dcctl upgrade` con `--dry-run` primero si quiere
ver exactamente qué objetos movería.
:::

:::warning Traslade los valores: `--set image.tag=…` por sí solo no funcionará
No todos los valores de su instancia se escriben a mano. `dcctl bootstrap` genera varios y los
guarda en la versión desplegada; entre ellos, la clave raíz de la instancia, el secreto de
autenticación entre servicios, la credencial de servicio de NATS y la semilla del emisor de
callout, y la CA del bróker.

La regla de Helm es la trampa. Una actualización que **no** pasa ningún valor reutiliza los que ya
están en la versión desplegada. Pero en cuanto pasa *cualquier* valor —incluido el único `--set`
que cambia la versión, que es justamente el objetivo de una actualización— Helm parte de los
valores predeterminados del chart y todo lo que generó `dcctl bootstrap` desaparece.

Cuando eso ocurre no se corrompe nada, porque el chart se niega a renderizar sin la clave raíz:

```
Error: UPGRADE FAILED: execution error at (devicechain/templates/instance-config.yaml:27:4): instance.config.infrastructure.secrets.rootKey is required: area "notification-management" owns an envelope-encrypted secret store and cannot form its KEK without it, so it would crash-loop. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically.
```

La solución es el paso `helm get values` de arriba. `--reuse-values` también funciona, pero conserva
en silencio entradas obsoletas cuando los valores predeterminados del chart cambian entre versiones,
así que es preferible volcar los valores y pasarlos con `-f`, donde puede verlos.
:::

Lo que hace que el despliegue sea seguro:

- **Aumentar antes de terminar.** Cada Deployment usa una estrategia `RollingUpdate` con
  `maxUnavailable: 0` y `maxSurge: 1`, de modo que un pod nuevo debe pasar su sonda de disponibilidad
  `/readyz` **antes** de que se elimine un pod antiguo. La capacidad nunca disminuye durante el despliegue.
- **Apagado ordenado / drenaje de conexiones.** Cuando se le pide a un pod que termine, primero
  informa "no listo" (de modo que el Service deje de enrutarle nuevas solicitudes), espera una breve
  ventana de drenaje para que ese cambio se propague, y solo entonces termina el trabajo en curso y
  se apaga. Configure la ventana con `shutdownDrainSeconds` (por defecto `5`), mantenida de forma segura
  por debajo de `terminationGracePeriodSeconds` (por defecto `30`).
- **Migraciones de esquema coordinadas.** Los servicios ejecutan migraciones de base de datos bajo un bloqueo
  a nivel de base de datos, de modo que cuando varias réplicas se inician a la vez, exactamente una aplica las
  migraciones y el resto espera; sin condiciones de carrera, sin DDL duplicado.

:::tip Ejecute al menos dos réplicas en producción
Para lograr un verdadero cero tiempo de inactividad, ejecute `replicas: 2` (o más) para cada área, de modo que el despliegue siempre tenga
un pod activo sirviendo tráfico. Una sola réplica igualmente tiene una breve brecha mientras se reemplaza su único pod.
Configúrelo globalmente con `--set replicas=2`, o por área bajo
`functionalAreas.<area>.replicas`. Un `PodDisruptionBudget` se genera automáticamente para cualquier
área con más de una réplica, de modo que los drenajes de nodo no puedan expulsar a todas las réplicas a la vez.
:::

### La compactación de la línea base de la v0.9.0 {#v090-baseline-squash}

La `v0.9.0` es la **primera** de las dos versiones a las que no se puede llegar con `helm upgrade`
(la otra es la [`v0.10.0`](#v0100-event-key)).

Antes de ella, el esquema de cada servicio se construía mediante una cadena de migraciones aplicadas en orden.
La `v0.9.0` reemplaza todas esas cadenas por una **única línea base congelada**: una migración por servicio que
crea el esquema completo tal y como está. Una base de datos creada por `v0.8.x` ya aplicó la cadena antigua, así
que cuando se encuentra con la línea base intenta crear tablas que ya existen y falla con `already exists`. El
fallo es evidente y ocurre en el arranque; no corrompe nada.

No hay ruta de migración y, antes de la `v1.0.0`, no la habrá. Mantener una capa de compatibilidad para una forma
de esquema que todavía se está asentando es precisamente el coste que este proyecto ha decidido no asumir
mientras todas las instalaciones siguen siendo tempranas.

**Para pasar a la `v0.9.0`, recree la instancia:**

```bash
# Exporte antes lo que necesite: esto descarta las bases de datos.
dcctl destroy local devicechain
dcctl bootstrap local devicechain
```

:::caution Exporte primero: recrear descarta sus datos
La [protección de destrucción](#data-durability) protege las bases de datos frente a una operación normal de
`helm`, no frente a un `dcctl destroy` deliberado. Si la instancia contiene telemetría, definiciones de
dispositivos o paneles que le importan, expórtelos antes de empezar. No existe una ruta in situ que los
conserve a través de esta versión.
:::

Normalmente, un cambio de esquema **añade** una nueva migración sobre la línea base, lo que es una
actualización in situ corriente. Esa es la regla, y se cumple en casi todas las versiones.

:::note Esta sección prometió una vez que no volvería a ocurrir
Decía que la compactación describía «una única versión, no una nueva política». Después, la
`v0.10.0` también necesitó recrear la instancia, por un motivo distinto. La versión honesta de la
regla es: añadir migraciones es lo normal y, antes de la `v1.0.0`, una versión todavía puede exigir
recrear la instancia cuando un defecto no se puede corregir de otra forma. **Toda versión que lo
exija lo indicará en sus notas y aquí.** Consulte ambas antes de actualizar, en lugar de deducirlo
del número de versión.
:::

### El cambio de clave de eventos de la v0.10.0 {#v0100-event-key}

La `v0.10.0` es la segunda versión a la que **no se puede llegar con `helm upgrade`**, por un motivo
distinto al de la compactación.

Un evento se identificaba por la combinación de su inquilino, dispositivo, tipo y marca de tiempo.
Esa combinación no es única: un dispositivo que muestrea dos sensores y publica cada uno como su
propio mensaje bajo una misma marca de tiempo produce dos eventos realmente distintos que a la base
de datos le parecen idénticos. El segundo se descartaba de forma silenciosa: sus lecturas quedaban
guardadas contra el registro del primero y, una vez descartado, ya nunca podía reconocerse como
repetido, de modo que cada reintento posterior de ese mensaje añadía otra copia de sus lecturas.

Cualquier dispositivo que marque el tiempo en segundos enteros podía provocarlo emitiendo dos veces
en un mismo segundo, y el SDK de .NET publicado lo hacía así hasta esta versión.

La `v0.10.0` otorga a cada evento, a cada lectura y a cada registro de relación una identidad
derivada de su propio contenido, y convierte esa identidad en la clave. Almacenar la telemetría
correctamente implica cambiar la clave primaria de las tablas más grandes del sistema, y esas tablas
están comprimidas: un motor de base de datos no altera una clave sobre datos comprimidos in situ. No
existe ninguna ruta de actualización que conserve las filas existentes.

**Para migrar a la `v0.10.0`, recree la instancia:**

```bash
# Exporte antes lo que necesite: esto descarta las bases de datos.
dcctl destroy local devicechain
dcctl bootstrap local devicechain
```

Se aplica la misma advertencia que más arriba: recrear la instancia descarta su telemetría, las
definiciones de dispositivos y los paneles. Exporte lo que necesite antes de empezar.

La acompañan dos cambios en cómo la API informa del tiempo, y ninguno requiere acción alguna:

- Las marcas de tiempo se devuelven ahora con la precisión con la que se registraron; antes se
  redondeaban hacia abajo al segundo entero al salir, por lo que dos lecturas separadas por 200
  milisegundos volvían pareciendo simultáneas. Las marcas de tiempo de segundo entero no cambian.
- Las peticiones que usan el valor `updatedAt` de un registro para evitar sobrescribir la edición de
  otra persona se comprueban ahora con esa misma precisión. Antes, dos ediciones dentro de un mismo
  segundo podían superar ambas la comprobación, y la posterior sobrescribía en silencio un cambio
  que nunca había visto.

### v0.11.0: de nuevo una actualización normal {#v0110-upgrade}

La `v0.11.0` es la primera versión desde la `v0.8.5` a la que se puede llegar con `helm upgrade`.
Su cambio de esquema **añade** tres migraciones en lugar de reemplazar una línea base, por lo que
una base de datos `v0.10.0` existente se traslada con sus filas intactas en vez de tener que
recrearse.

Lo que llega a la base de datos:

- dos tablas nuevas que registran el progreso y el historial de la eliminación de un inquilino, y
- dos columnas en la tabla de inquilinos que siguen su estado de ciclo de vida.

Todos los inquilinos que ya existen quedan en el estado activo normal al añadirse la columna, así
que nada cambia en una instancia en funcionamiento hasta que elimine realmente un inquilino.

:::note Qué se probó y qué no
Antes de la publicación se ejecutaron dos comprobaciones, y conviene mantenerlas separadas porque
midieron cosas distintas.

**Las migraciones, contra una base de datos con datos dentro.** Se construyó un esquema `v0.10.0`,
se llenó con filas representativas y se trasladó hacia adelante. Todas esas filas llegaron
idénticas byte a byte, y el esquema resultante es idéntico al de una instalación nueva de
`v0.11.0`, para todas las áreas funcionales, no solo para la que cambió.

**La actualización en sí, sobre una instancia en funcionamiento.** Se construyó una instancia
`v0.10.0` a partir de las imágenes `v0.10.0` publicadas, se le dieron inquilinos e identidades
reales y luego se actualizó con el comando de arriba. Todos los servicios se desplegaron, el
recuento de filas de las 67 tablas no varió salvo por las nuevas entradas de migración y los
registros de auditoría que escribieron, el inicio de sesión siguió funcionando para una cuenta
creada bajo `v0.10.0`, y la nueva API de eliminación de inquilinos respondió en la instancia
actualizada.

Cuatro límites, indicados con claridad:

- **Solo se verificaron las bases de datos.** El estado del bróker (JetStream), el almacenamiento
  de objetos y el estado clave-valor no están cubiertos por ninguna de las dos comprobaciones.
- **Solo PostgreSQL 16.** Las instalaciones nuevas se verifican en ambas versiones principales
  admitidas; la ruta de actualización en sí se midió en la 16.
- **La comparación fila a fila proviene de la primera comprobación, no de la segunda.** La
  instancia en funcionamiento se verificó por *recuentos* de filas, que no detectarían una fila
  modificada en el sitio en lugar de eliminada.
- **La consola web se dejó con su imagen `v0.10.0`** durante la segunda comprobación, así que la
  consola `v0.11.0` no se ejercitó contra una instancia actualizada.
:::

### v0.12.0: una actualización que cambia contratos {#v0120-upgrade}

Se puede llegar a la `v0.12.0` con `helm upgrade`. Su cambio de esquema **añade**
migraciones en lugar de reemplazar una línea base, así que una base de datos `v0.11.0`
existente se conserva con sus filas intactas, y esto se midió sobre una instancia en
ejecución en lugar de razonarse.

Lo que sí cambia son los **contratos**: el tema MQTT en el que un dispositivo responde a un
comando, unas cuantas operaciones GraphQL y el significado de varias cosas cuya forma no
cambió en absoluto. Nada de eso se ve en un `helm upgrade` que informa éxito, así que lea
esta sección antes de ejecutarlo.

#### Haga esto antes de actualizar

**1. Actualice todo dispositivo que responda a comandos.** El tema en el que un dispositivo
publica la respuesta a un comando ahora está acotado a ese dispositivo:

```
# antes
{instanceId}/{tenant}/command-responses
# ahora
{instanceId}/{tenant}/command-responses/{deviceToken}
```

Las credenciales que se emiten a un dispositivo ya no permiten el tema anterior, así que a
un dispositivo sin actualizar se le rechazarán las respuestas en el broker: seguirá
recibiendo los comandos y actuando sobre ellos, pero la plataforma nunca registrará que lo
hizo, y todos acabarán leyéndose como caducados por tiempo.

El motivo del cambio es que el tema anterior permitía que **cualquier** dispositivo del
inquilino publicara una respuesta nombrando **cualquier** comando, incluido uno emitido a
otro dispositivo. Nada en la respuesta decía quién la enviaba, así que nada podía
distinguirlo. El token del dispositivo forma ahora parte del tema, que forma parte de lo
que el broker firma, de modo que un dispositivo solo puede responder por sí mismo.

Actualice primero los dispositivos si puede. Las respuestas enviadas en el tema anterior
durante la transición se rechazan, no se encolan, y el pequeño número de respuestas en
vuelo justo en el momento de la actualización se descarta en lugar de entregarse.

**2. Renombre una fuente de eventos cuyo id sea exactamente `lwm2m`.** Ese es el valor bajo
el que el servicio LwM2M archiva la presencia de sus propios dispositivos, y los registros
de presencia se comparan por igualdad exacta, así que su fuente y ese servicio se
sobrescriben mutuamente las filas. `event-sources` ahora se niega a arrancar con ese id, lo
que detiene toda la ingesta de la instancia.

Un id que solo *se lee* como un transporte, como `sparkplug:plant-a` o `lwm2m:site-a`, ahora
arranca con una advertencia en lugar de negarse. Renómbrelos cuando le venga bien. En ambos
casos tenga en cuenta la trampa al renombrar: la presencia ya registrada bajo el id anterior
no se traslada, y nada la rellena después.

**3. Compruebe quién lee el historial de ubicaciones.** Las consultas que devuelven
posiciones de dispositivos ahora requieren el permiso `location:read` en lugar de
`event:read`. Ese permiso no está en la base de solo lectura que recibe un visor, así que
una cuenta que podía leer el historial de posiciones en la `v0.11.0` no puede en la
`v0.12.0`. Concédalo explícitamente a los roles que lo necesiten.

Si ha registrado un cliente OAuth para acceso de IA, necesita la misma atención: leer
posiciones ahora requiere un alcance `location` aparte, junto a `read-only`, así que añádalo
a los alcances registrados del cliente y vuelva a autorizarlo. Hasta que lo haga, la
herramienta `query_locations` de ese cliente se rechaza mientras sus demás herramientas de
lectura siguen funcionando. La separación es deliberada: es lo que permite autorizar a un
asistente a observar una flota mientras se le oculta dónde ha estado.

**4. Busque estas operaciones GraphQL** en cualquier cosa que haya escrito contra la API:

| Operación | Qué cambió |
| --- | --- |
| `createCommand` | Devuelve `CreateCommandResult!` en lugar de `Command!`. El comando está ahora bajo un campo `command`, junto a un campo `rejection` que explica un rechazo. |
| `updateDeviceType` | Su argumento `request` es ahora un `DeviceTypeUpdateRequest!` obligatorio, y con él cambió la semántica: esto es una **actualización parcial**. Un campo omitido ahora CONSERVA su valor almacenado en lugar de borrarlo, y un null explícito lo limpia. Así que un cliente que limpiaba un campo dejándolo fuera ahora tiene que enviarle null — y, en el otro sentido, renombrar un tipo ya no desvincula el perfil a través del cual sus dispositivos resuelven sus capacidades. `token` también ha desaparecido de la entrada, así que una actualización ya no puede mover el token de un tipo. Los campos no reconocidos dentro de la solicitud se rechazan en lugar de ignorarse. |
| `assertedActiveDeviceStates` | Sustituida por `assertedDeviceStates`, que toma `activeOnly` y pagina mediante `afterId` y `pageSize`. |
| `deviceCredentials`, `deviceCredentialsById`, `deviceCredentialsByToken` | Ahora requieren `device:write`. Para un tipo de credencial el identificador legible *es* el token portador, así que `device:read` — que tiene todo miembro habilitado — bastaba para abrir una sesión en el broker como cualquier dispositivo del inquilino. |
| `locationEvents` | Ahora requiere `location:read`, como arriba. |
| Cualquier consulta `...ById(ids: [])` | Una lista de ids vacía ahora no devuelve nada. Antes devolvía la tabla entera, sin paginar. |

#### Cambios sin cambio de firma

Estos son los que un cliente no puede detectar mirando el esquema.

**Actualizar un perfil de dispositivo borra su declaración de ubicación.** Un perfil puede
ahora declarar que sus dispositivos informan de su posición, y `updateDeviceProfile`
reemplaza el perfil completo. Un cliente escrito contra la `v0.11.0` no envía el campo
nuevo, así que actualizar un perfil por cualquier motivo — renombrarlo, editar su
descripción — deja de declarar la posición para todos los dispositivos que lo usan, en
silencio. El único síntoma es que las superficies de mapa se quedan vacías. Envíe el campo,
o vuelva a establecer la declaración después de cualquier actualización hecha desde un
cliente antiguo.

**Las reglas de detección con ventana ya no cuentan lecturas acumuladas de fuera de su
ventana.** Las reglas de repetición, de agregado deslizante y de correlación incorporaban una
lectura de cualquier punto del pasado, lo que permitía que una regla que dice «tres lecturas
en diez segundos» se disparara con lecturas separadas por una hora — el detonante habitual
era un dispositivo subiendo su búfer acumulado. Ahora esas reglas descartan una lectura que
llega después de que haya pasado la ventana a la que pertenecía, igual que ya hacían los
agregados de ventana fija y las reglas de sesión. Espere **menos** alarmas de esos tipos de
regla en cualquier flota que suba por lotes, y consulte `detect_late_samples_total` para ver
cuánto se está descartando. Las lecturas se almacenan y grafican exactamente igual que antes;
esto afecta solo a la detección. Véase [ejecutar el motor de
detección](./detection-engine.md#timing-what-when-means).

**Cancelar un comando registra `CANCELLED`.** Antes registraba `EXPIRED`, que compartía con
un comando que simplemente agotó su tiempo. Si se bifurca sobre `EXPIRED` para detectar su
propia cancelación, ya no estará ahí.

**Los comandos pueden quedarse ahora en `HELD` o `PARKED`.** Un comando dirigido a un
dispositivo que la plataforma sabe ausente se retiene en lugar de publicarse, y uno que se
despachó a un dispositivo que resultó inalcanzable se aparca. Ambos están esperando, no
terminados, y ambos son nuevos: el código que trate cualquier cosa distinta de `QUEUED` o
`SENT` como terminal se equivocará. El conjunto completo es ahora `QUEUED`, `HELD`, `SENT`,
`PARKED`, `SUCCESSFUL`, `FAILED`, `TIMEOUT`, `EXPIRED`, `CANCELLED`.

**Una lectura se almacena en el instante en que se tomó.** Cuando un mensaje transporta
muchas muestras, cada una con su propia marca de tiempo — toda carga de Sparkplug y LwM2M lo
hace, y también cualquier dispositivo que almacene mientras está sin conexión — esas
muestras se almacenaban en el instante en que llegaba el mensaje. Ahora se almacenan en el
suyo propio. Un dispositivo que sube una hora de lecturas almacenadas las escribe a lo largo
de esa hora en lugar de en el momento de la subida, de modo que el historial, las gráficas,
la retención y la detección las ven donde realmente corresponden.

**Una fuente de presencia que deja de ejecutarse ahora devuelve sus dispositivos.** Un dispositivo
marcado como `ASSERTED` conservaba indefinidamente la presencia que tuviera por última vez: el
barrido de inactividad omite los dispositivos afirmados y un evento de datos no puede cambiarlos, así
que un dispositivo que estaba conectado cuando su fuente desapareció figuraba conectado para siempre,
y uno que estaba fuera de línea tenía sus comandos retenidos para siempre. La presencia MQTT afirmada
por el broker ahora libera los dispositivos que afirmó cuando se la desactiva deliberadamente, o
cuando falta su credencial de cuenta de sistema de NATS, devolviéndolos a `INFERRED` sin afirmar nada
sobre la conectividad. En una instancia donde eso aplique, espere un evento de cambio de estado por
dispositivo, a ritmo pausado, contado bajo `presence_events_total{state="demoted"}`, y espere que
esos dispositivos vuelvan a quedar bajo el barrido de inactividad de diez minutos. Sparkplug y LwM2M
no tienen liberación automática: `dcctl presence demote` y la nueva mutación `demoteAssertedPresence`
de `device-state` lo hacen a mano, para cualquier fuente. La mutación necesita un permiso nuevo,
`state:demote`, que ningún rol tiene de forma predeterminada. Un medidor nuevo,
`presence_tap_off{reason}`, informa de si la presencia afirmada por el broker está funcionando
siquiera — algo que nada reportaba antes, porque desde fuera una flota en silencio y una toma que
nunca arrancó son idénticas. Vea [Devolver un dispositivo a presencia
inferida](./edge-services.md#demoting-a-device).

#### Entradas que antes se aceptaban y ahora no

- Una política de notificación que lleve `deviceTypeToken`. Acotar una política a un tipo de
  dispositivo no está implementado; la escritura antes tenía éxito y luego no entregaba nada.
- Una regla de notificación cuya `severity` no sea uno de los niveles en mayúsculas o `*`.
  Una severidad en minúsculas antes se escribía, se releía sin cambios y nunca coincidía con
  ninguna alarma.
- Un `occurredTime` de `0001-01-01T00:00:00Z`. Es una marca de tiempo válida, y la
  plataforma la reserva para significar que no se informó ninguna hora.

#### Configuración

Una clave se movió. `maxEventFutureSkewSeconds` limitaba cuánto puede adelantarse una marca
de tiempo informada por el dispositivo respecto al reloj de la plataforma; era un ajuste de
`event-processing` y ahora es de `device-management`, porque la hora del evento se decide
ahora en un único lugar, tanto para la detección en vivo como para la reproducción.

Una configuración que siga estableciéndola bajo `event-processing` **arranca con
normalidad** y registra una advertencia que nombra la nueva ubicación. El valor anterior no
se aplica: establézcalo bajo `device-management` si lo había cambiado respecto al valor
predeterminado de 300 segundos.

No se eliminó nada de los valores del chart, así que un archivo de valores `v0.11.0` se
aplica sin cambios.

### La transición única a la ingesta duradera

La versión que introduce la **ingesta MQTT duradera** cambia la forma en que `event-sources` recibe
la telemetría de dispositivos: en lugar de suscribirse al broker como cliente MQTT, consume un
flujo de captura duradero que el broker escribe antes de confirmar la recepción al dispositivo. Esto
es lo que evita que se pierda telemetría cuando `event-sources` está caído.

Cruzar esa versión una vez es un `helm upgrade` normal, pero espere una **breve ventana de
telemetría duplicada** y planifique para ello:

- Durante el despliegue, el pod saliente sigue ingiriendo por MQTT mientras el pod
  entrante ya ha comenzado a consumir el flujo de captura, de modo que los mensajes publicados en ese solapamiento son
  ingeridos por ambos. La ventana está acotada por cuánto tiempo coexisten los dos pods: el arranque del
  pod entrante más el drenaje del pod saliente.
- Los eventos que llevan **tanto** un `altId` **como** una `occurredTime` provista por el dispositivo no se ven afectados:
  la clave de deduplicación del lado de escritura es `(tenant, altId, occurredTime)`, de modo que esos duplicados se colapsan. Un
  evento con un `altId` pero sin `occurredTime` **no** se colapsa: el decodificador estampa la
  hora actual cuando el dispositivo omite una, y las dos copias se decodifican en pods diferentes en
  instantes diferentes, por lo que obtienen marcas de tiempo diferentes y terminan como dos filas. La telemetría sin
  `altId` no se deduplica en absoluto.
- El solapamiento se prefiere deliberadamente. El orden alternativo —detener el pod antiguo
  antes de que exista el flujo de captura— pierde cada mensaje que el broker confirma en la brecha,
  y esa pérdida es silenciosa: al dispositivo se le informa que el mensaje fue aceptado y nunca se
  almacena. Una lectura duplicada es visible y corregible; una faltante no es ninguna de las dos cosas.

:::danger No configure `event-sources` como `Recreate`
`strategy: Recreate` en `event-sources` produce exactamente el orden con pérdida descrito arriba, porque
termina el pod antiguo antes de que el nuevo cree el flujo de captura. El chart se niega
a renderizar esta configuración en lugar de dejar que descarte telemetría silenciosamente. `event-sources`
no es un servicio de escritor único y no gana nada con `Recreate`; una vez realizada la transición puede ejecutar
múltiples réplicas, algo que la ruta de cliente MQTT que reemplaza no podía hacer.
:::

## Durabilidad de los datos {#data-durability}

La capa de base de datos es intencionalmente **independiente del ciclo de vida** de la aplicación. Ambas
bases de datos se aprovisionan como infraestructura separada con una protección contra destrucción, de modo
que actualizar, reinstalar o desinstalar la *aplicación* nunca las toca. Ese es el caso habitual y es seguro.

:::caution Quitar la base de datos de la configuración de infraestructura es un acto distinto
La protección resguarda cada base de datos mientras está *dentro* de la configuración de infraestructura. No
resguarda una que se haya sacado *fuera* de ella: un recurso eliminado de la configuración deja de estar
cubierto por las reglas que esa configuración declara, y el plan de eliminación se ejecutará con éxito. Los
clústeres de base de datos además son dueños de sus volúmenes, así que eliminar uno se lleva sus datos consigo
en lugar de dejar un volumen desasociado.

No edites la base de datos para sacarla de la configuración de infraestructura como forma de reemplazarla.

Actualizar una instancia creada antes de que las bases de datos pasaran al operador es el
único caso en que esto aparece, y se rechaza en tiempo de planificación en lugar de dejarse
al azar. Vuelca primero ambas bases de datos y vuelve a ejecutar el arranque con
`--allow-legacy-db-removal`, que afirma que te has ocupado de los datos y no verifica nada.
Para una instancia local, `dcctl destroy` seguido de un arranque nuevo es más simple y
descarta los datos de forma deliberada.
:::

Esto es durabilidad de los volúmenes en ejecución; no es un sustituto de las copias de seguridad programadas y la
recuperación a un punto en el tiempo, que se aprovisionan con la infraestructura de producción. Consulte
[Despliegue y operador](./kubernetes-operator.md) para saber cómo se separan las capas de infraestructura y aplicación.
