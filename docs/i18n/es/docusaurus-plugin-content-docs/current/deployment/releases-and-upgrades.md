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

:::caution Cruzar la v0.12.0 requiere algunos cambios previos
La `v0.12.0` se actualiza en el sitio, pero cambia el tema en el que un dispositivo responde
a un comando, mueve un permiso y cambia varias cosas cuya forma se mantuvo igual. Un
`helm upgrade` informará éxito en cualquier caso. Lea
[v0.12.0: una actualización que cambia contratos](#v0120-upgrade) antes de empezar.

Esto se aplica a **cualquier** actualización que cruce la `v0.12.0`, no solo a la que se
detiene ahí: pasar de la `v0.11.0` directamente a un parche posterior no omite esos cambios.
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

Ese mismo permiso pasa a controlar además la **vista previa de una regla que comprueba la
contención en una geocerca**. Una vista previa de ese tipo devuelve, por dispositivo, cuándo
entró en una región y cuándo salió — una lectura de posición, se pida como se pida — así que
`previewRule` requiere `location:read` además del `device:read` que toma toda vista previa. A
un autor de reglas que en la `v0.11.0` podía previsualizar cualquier borrador se le rechazan
los borradores con contención hasta que se le conceda ese permiso. Las vistas previas que no
comprueban contención no se ven afectadas.

**Y compruebe quién lo lee a través de un asistente de IA.** El servidor MCP incorporó una
herramienta `query_locations` que devuelve las posiciones reportadas de un dispositivo, y llegar
a ella exige **dos** concesiones que se mantienen separadas de forma deliberada. La autorización
del agente debe incluir un alcance OAuth nuevo, `location`, *y* la persona que lo autorizó debe
tener un rol que conceda `location:read`. Ninguna de las dos basta por sí sola: el alcance es un
techo sobre lo que un token puede portar, no una concesión de nada.

Un agente autorizado solo con `read-only` no puede leer posiciones, por mucho que tenga su
usuario. Ese es justamente el sentido de un alcance aparte en lugar de un `read-only` más
amplio: la pantalla de consentimiento le muestra a la persona la cadena de alcance en crudo, así
que meter la posición dentro de `read-only` habría significado una autorización idéntica antes y
después que ahora incluye dónde han estado los dispositivos — y, con bastante frecuencia, dónde
han estado las personas que los llevan. Mantenerlo separado hace que conceder observabilidad a
un agente no sea el mismo acto que concederle el historial de ubicaciones, y permite a un
usuario permitir lo uno reteniendo lo otro.

Un cliente MCP que ya tenga registrado seguirá funcionando y seguirá recibiendo un rechazo en
las posiciones hasta que su petición de autorización pida `read-only location` y el usuario lo
vuelva a autorizar. La base del visor no cambia: `location:read` sigue sin ser algo que un
miembro reciba de forma predeterminada. Vea [Acceso de IA (MCP)](../concepts/mcp.md).

**4. Busque estas operaciones GraphQL** en cualquier cosa que haya escrito contra la API:

| Operación | Qué cambió |
| --- | --- |
| `createCommand` | Devuelve `CreateCommandResult!` en lugar de `Command!`. El comando está ahora bajo un campo `command`, junto a un campo `rejection` que explica un rechazo. |
| `updateDeviceType` | Su argumento `request` es ahora un `DeviceTypeUpdateRequest!` obligatorio, y con él cambió la semántica: esto es una **actualización parcial**. Un campo omitido ahora CONSERVA su valor almacenado en lugar de borrarlo, y un null explícito lo limpia. Así que un cliente que limpiaba un campo dejándolo fuera ahora tiene que enviarle null — y, en el otro sentido, renombrar un tipo ya no desvincula el perfil a través del cual sus dispositivos resuelven sus capacidades. Un cliente escrito contra el comportamiento anterior de registro completo — uno que lee el tipo y devuelve todos los campos — sigue funcionando y sigue escribiendo lo que envía. `token` también ha desaparecido de la entrada, así que una actualización ya no puede mover el token de un tipo. Los campos no reconocidos dentro de la solicitud se rechazan en lugar de ignorarse. |
| `assertedActiveDeviceStates` | Sustituida por `assertedDeviceStates`, que toma `activeOnly` y pagina mediante `afterId` y `pageSize`. |
| `deviceCredentials`, `deviceCredentialsById`, `deviceCredentialsByToken` | Ahora requieren `device:write`. Para un tipo de credencial el identificador legible *es* el token portador, así que `device:read` — que tiene todo miembro habilitado — bastaba para abrir una sesión en el broker como cualquier dispositivo del inquilino. |
| `locationEvents` | Ahora requiere `location:read`, como arriba. |
| `geoFenceSetSnapshot`, `currentGeoFenceSet` | Su campo `fences` ahora está paginado: toma un argumento `pagination` obligatorio y devuelve `results` junto a un registro `pagination`, en lugar de una lista simple. Lea páginas hasta que `pageEnd` alcance `totalRecords`. Un conjunto de geocercas en los límites documentados es mayor de lo que puede transportar una sola respuesta, así que la forma de lista no podía devolverse en absoluto para los inquilinos con más probabilidad de pedirla. |
| Cualquier consulta `...ById(ids: [])` | Una lista de ids vacía ahora no devuelve nada. Antes devolvía la tabla entera, sin paginar. |

**5. Deje de rellenar los ids con ceros a la izquierda.** Un argumento `id` se interpreta
ahora como un número decimal y nada más. Antes se interpretaba deduciendo la base del propio
literal, así que un `"017"` rellenado con ceros — exactamente lo que envía un cliente que
formatea los ids a un ancho fijo — se leía como **octal** y resolvía a la fila 15: la entidad
equivocada, devuelta con éxito y sin ningún error que lo delatara. `"0x2"`, `"0b101"` y `"1_0"`
se aceptaban de la misma manera. Las cuatro formas se rechazan ahora de plano. Envíe `"17"`.

**6. Cuente con que todos los pods de servicio se reinicien, una vez.** El documento de
configuración de instancia que se entrega a los servicios tiene ahora eliminada la coordenada
de cualquier área funcional que este despliegue no habilitó. En un despliegue sin
`ai-inference` — es decir, todos los perfiles salvo `full` — eso cambia los bytes del documento
y por tanto la anotación de suma de verificación que reinicia los pods, así que el
`helm upgrade` reinicia todos los servicios y no solo aquellos cuya imagen se movió. Es una
actualización progresiva normal y no requiere nada de usted; figura aquí para que un reinicio
completo no se lea como un síntoma.

El motivo de eliminar la coordenada es que un nombre de host de un servicio que nadie desplegó
era peor que ningún nombre de host: la superficie de autoría de reglas construía su puerta de
lenguaje natural «Describe» contra él, fallaba al resolver el nombre e informaba de que el
inquilino no había consentido el enrutamiento externo de IA — culpando a un ajuste del
inquilino por un servicio que el operador nunca instaló. Ahora dice que la función no está
habilitada en este despliegue, que es la verdad.

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

**La geometría de una geocerca se valida de forma más estricta y se almacena tal como queda
escrita, no tal como se envía.** Tres cambios, todos en el momento de crear o actualizar una
geocerca:

- Una posición debe ser exactamente `[longitud, latitud]`. Antes se aceptaba e ignoraba una
  tercera ordenada o posteriores.
- El documento de geometría solo puede llevar las claves que la plataforma lee — `kind` y
  `geometry` en el nivel superior, `type` y `coordinates` dentro. Cualquier otra clave antes
  se almacenaba y nunca se consultaba.
- Las coordenadas se reescriben en notación decimal simple antes de almacenarse. Una
  coordenada enviada como `1e-300` se devuelve como su expansión decimal completa. No se
  redondea ningún valor y ninguna geocerca cambia de forma, pero un documento leído de vuelta
  no es idéntico byte a byte al enviado.

Una geocerca también se rechaza ahora si su forma almacenada supera 32 KiB. Eso es
aproximadamente el doble del tamaño de una geocerca que use todos los vértices que la
plataforma permite, así que la geometría ordinaria no se ve afectada; lo que se rechaza es un
documento cuyo tamaño proviene de la notación y no de la forma. La consola siempre ha escrito
las posiciones en la forma aceptada, así que las geocercas dibujadas en la consola no se ven
afectadas. Las geocercas ya almacenadas **no** se reescriben y siguen funcionando, pero una
que incumpla alguna regla anterior será rechazada la próxima vez que se guarde.

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

**Una lectura reentregada ya no duplica sus filas.** La identidad de un evento de medición se
deriva de un resumen de su propio contenido, y esa identidad es lo que hace inofensiva una
reentrega. Para una lectura que lleva más de una métrica sobre un transporte JSON, el resumen se
calculaba sobre un orden que inventaba la plataforma y no sobre el que envió el dispositivo, así
que la misma lectura resolvía a una identidad distinta aproximadamente cuatro de cada cinco
veces. Cuando la plataforma reentregaba uno de esos mensajes — cosa que hace de forma rutinaria,
ante una publicación sin confirmar o un fallo de escritura transitorio — el duplicado no se
reconocía: las filas de medición se escribían una segunda vez y los resúmenes horarios las
contaban dos veces. Las lecturas de una sola métrica, y las que llegan por Sparkplug o LwM2M,
nunca se vieron afectadas. La corrección es solo hacia adelante: los duplicados ya escritos antes
de la actualización se quedan donde están, y sus resúmenes siguen inflados. Si tiene gráficas que
se veían demasiado altas en dispositivos de varias métricas, este es el motivo, y a partir de la
actualización se leerán correctamente.

**Toda lista paginada devuelve ahora las filas en un orden declarado.** De los 37 puntos finales
de lista de la plataforma, 31 no nombraban ningún orden, lo que deja a una lectura paginada libre
de entregar la misma fila en dos páginas y no mostrar nunca otra — un defecto real que ya se
había reportado dos veces como una pantalla que se reordenaba bajo un operador. Cada lista ordena
ahora por una clave total y sin ambigüedad. Si tiene código que dependía del orden incidental que
una consulta concreta devolvía por casualidad, verá ahora uno estable, que puede no ser el mismo.
Un orden se eligió deliberadamente en lugar de mecánicamente: las credenciales de dispositivo se
listan con la de mayor margen restante primero, porque una lectura sin acotar de ellas alimenta
la reutilización de credenciales, y ordenar por id habría devuelto la credencial más próxima a
caducar.

**Un comando respondido en texto plano registra ahora su respuesta.** Un dispositivo que
respondía a un comando con algo que no es JSON — `acknowledged`, una palabra de estado suelta —
hacía fallar la escritura con un error de tipo de la base de datos y dejaba el comando en `SENT`,
reintentando la misma escritura condenada una vez por minuto durante toda la vida de la fila. El
comando acababa caducando por tiempo contra un dispositivo que lo había respondido
correctamente. Una respuesta así se almacena ahora, sin pérdida, como una cadena JSON. Los
valores que suministra un **cliente de la API** no cambian: esos deben seguir siendo JSON válido,
porque a un cliente que envía JSON mal formado hay que decírselo.

**Los comandos a dispositivos Sparkplug fallan ahora de inmediato en lugar de perderse.** La
plataforma no tiene ruta de comandos hacia un dispositivo Sparkplug — esos nodos viven en tu
propia infraestructura MQTT y nada tiende un puente entre ambas — y la comprobación que debía
rechazar uno de esos comandos se comparaba contra un valor que ningún dispositivo lleva jamás,
así que no coincidía con nada y todos esos comandos se aceptaban y luego se iban en silencio a
ninguna parte. Ahora se registran `FAILED` de inmediato con ese motivo, y se cuentan bajo
`command_delivery_undeliverable_total`. Espere que los comandos que antes se quedaban hasta su
TTL y registraban `TIMEOUT` aparezcan ahora como fallos inmediatos. Vea
[Comandos](../concepts/commands.md).

**Un comando cuyo rastro perdió la plataforma se rearma en lugar de culpar al dispositivo.** Un
comando podía llegar a `SENT` y luego no ser alcanzable por nada — el pod que lo publicó muere
antes de registrar el desenlace — y `SENT` no tenía más salida que el TTL, que registraba
`TIMEOUT` contra un dispositivo al que nunca se le envió nada. Una pasada en segundo plano
encuentra ahora esos casos y los rearma a `PARKED`, de modo que se entregan en el siguiente
despertar del dispositivo. `command_delivery_stranded_recovered_total` lleva una etiqueta
`{disposition}` que dice dónde acabó cada uno. **Esto aplica solo a dispositivos LwM2M**: en MQTT
plano, un comando que parece no haber llegado a nada no se distingue de uno que llegó y cuya
respuesta se perdió, así que ahí el comportamiento no cambia y
`command_delivery_stranded_skipped_total{reason="transport"}` mostrará un ritmo constante que no
es un fallo. Vea [cuando la plataforma pierde el rastro de un
comando](../concepts/commands.md#stranded-commands).

**Una acción de regla que la plataforma nunca podrá entregar se descarta en lugar de
reintentarse.** Cuando una acción REACT se rechaza por un motivo que ningún reintento puede
cambiar — un `sendCommand` dirigido a un dispositivo que ya no existe, o a un comando fuera del
vocabulario publicado de ese dispositivo — antes se reintentaba hasta el límite de reentregas y
luego se contaba como envenenada, lo que ponía un error de autoría en el mismo estante que un
fallo de infraestructura. Ahora se descarta al primer rechazo de ese tipo y se cuenta bajo
`react_actions_permanently_rejected_total`, etiquetada por tipo de acción. Un ritmo sostenido en
ese contador significa que una regla apunta a algo que sus dispositivos no pueden aceptar; el
contador de envenenadas que antes inflaba significa ahora lo que dice.

**Una respuesta truncada entre servicios se cuenta.** Los servicios leen las respuestas de los
demás hasta un tope fijo de 1 MiB, y una respuesta mayor se cortaba en silencio. Ahora la cuenta
`devicechain_svcclient_responses_truncated_total`, etiquetada por par. La lectura debería ser
plana en cero; una distinta de cero significa que algún servicio está actuando sobre una
respuesta parcial, algo que conviene saber antes de que el síntoma llegue a una pantalla.

#### Entradas que antes se aceptaban y ahora no

- Una política de notificación que lleve `deviceTypeToken`. Acotar una política a un tipo de
  dispositivo no está implementado; la escritura antes tenía éxito y luego no entregaba nada.
- Una regla de notificación cuya `severity` no sea uno de los niveles en mayúsculas o `*`.
  Una severidad en minúsculas antes se escribía, se releía sin cambios y nunca coincidía con
  ninguna alarma.
- Un `occurredTime` de `0001-01-01T00:00:00Z`. Es una marca de tiempo válida, y la
  plataforma la reserva para significar que no se informó ninguna hora.
- Un encolado que llevaría a un inquilino por encima de su **techo de comandos retenidos**. Los
  comandos retenidos para un dispositivo ausente se acumulan sin freno natural — la acumulación
  de una flota dormida puede quedarse días — y antes nada la acotaba. El límite se resuelve desde
  la anulación propia del inquilino, si no la de su nivel, si no un valor predeterminado de
  plataforma de 10 000, y no hay ningún valor, en ningún nivel, que signifique ilimitado. El
  rechazo lleva el código `HELD_CEILING_EXCEEDED` y es el único temporal que produce la puerta de
  encolado: se libera a medida que esos dispositivos vuelven. Un cliente que trate todo rechazo
  como permanente debería tratarlo como caso aparte. Vea [cuánta acumulación puede retener un
  inquilino](../concepts/commands.md#held-command-ceiling).
- Un encolado que llevaría a un inquilino por encima de la parte de ese techo **reservada para la
  entrega**. Una parte del límite — el 20 % de forma predeterminada — se guarda para la entrega de
  comandos de la propia plataforma, de modo que una sola escritura de flota no pueda consumirlo
  todo y dejar rechazado cada `sendCommand` automatizado de ese inquilino hasta que la acumulación
  drene. Todo lo que emite comandos en su nombre queda acotado por el resto: la consola, los SDK,
  `dcctl` y sus propias integraciones por igual. La consecuencia práctica es que un lote grande
  que antes se habría admitido entero puede ahora rechazarse en parte; cuando el lote pudo
  desplegarse parcialmente, su registro dice qué dispositivos no cupieron. Vea [una parte del techo
  está reservada para la entrega](../concepts/commands.md#delivery-machinery-reserve).

#### Arranque inicial y la CLI

Estos llegan a una instancia a través de `dcctl bootstrap` y de la aplicación de la
infraestructura, no de `helm upgrade`, así que ninguno se materializa durante la actualización
descrita arriba. Figuran aquí porque cada uno es un cambio en lo que sale mal.

**Un cambio en la configuración del bróker reinicia ahora el bróker.** `nats-server` no puede
recargar en caliente su bloque de callout de autorización ni sus límites de JetStream, y su
negativa es total: abandona la recarga entera, incluido todo cambio no relacionado que viajara
en la misma aplicación. Visto desde fuera eso era la peor clase de nada: la aplicación informaba
éxito, el ConfigMap mostraba los valores nuevos, y el bróker en ejecución seguía con la
configuración con la que arrancó, con la única evidencia en una línea dentro del propio registro
del bróker. Los servicios fallaban entonces al autenticarse contra un ConfigMap que demostraba
que sus credenciales eran correctas. El StatefulSet del bróker lleva ahora en su plantilla de pod
un hash de su configuración renderizada, de modo que el servidor siempre arranca con el archivo
que se le dio. El coste es que los cambios de configuración del bróker reinician ahora esos pods,
donde antes solo lo hacía un cambio de chart o de imagen: presupueste unos 50–70 segundos por
pod, lo que en un bróker de un solo servidor es una interrupción total breve y en tres es un
reinicio continuo.

**Las versiones de los charts de terceros están fijadas.** `ingress-nginx` y `cert-manager` se
instalaban con lo último que hubiera publicado su repositorio, lo que convertía al repositorio de
charts en una dependencia de la *planificación* además de la aplicación: cuando su host de
artefactos devolvió un 503, el plan falló con un error que no nombraba ni el chart ni la red, y
costó dos arranques fallidos hasta dar con la causa. Quedan fijados en `4.15.1` y `v1.21.1`
respectivamente — las versiones que ejecuta el clúster con el que se hicieron las pruebas. Si
contaba con recoger una más nueva automáticamente, ahora la actualiza deliberadamente.

**Un `dcctl` compilado por usted tiene ahora una etiqueta de imagen predeterminada utilizable.**
`make -C backend/cli build` producía un binario cuya etiqueta de imagen predeterminada salía del
archivo `VERSION` del repositorio — un valor que ninguna versión establece y bajo el que nunca se
publicó ninguna imagen. Todas las cargas de trabajo acababan en `ImagePullBackOff`, varios minutos
dentro de un arranque que había informado progreso sano todo el camino. Un `dcctl` compilado
localmente usa ahora `dev` de forma predeterminada, que el guardián de versiones no publicadas
reconoce y rechaza pronto con un mensaje legible, en vez de tarde con uno que no lo es. Un `dcctl`
publicado nunca estuvo afectado: su etiqueta viene de la propia versión.

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

:::caution Detenga el tráfico de dispositivos durante esta única actualización, o asuma un posible reinicio del motor de detección
El límite se movió, así que mientras dura este despliegue **ninguna de las dos partes lo está
aplicando**. En `v0.11.0` solo el motor de detección limitaba una hora informada por el
dispositivo; en `v0.12.0` solo lo hace la resolución de eventos. Los dos servicios se
despliegan como Deployments independientes, así que existe una ventana en la que un
`event-processing` de `v0.11.0` ya ha sido sustituido mientras un `device-management` de
`v0.11.0` sigue publicando, y un evento que cruce en esa ventana no lo comprueba ninguno.

Lo que cuesta si llega uno con una marca de tiempo desmesuradamente futura: la detección
mantiene una única frontera temporal para toda la instancia, así que ese único evento la
adelanta y todos los temporizadores pendientes de todos los inquilinos se disparan a la vez.
Recuperarse implica reiniciar la instantánea del motor.

**Es un límite de una sola actualización, no una debilidad permanente**: una vez que ambos
servicios están en `v0.12.0` concuerdan de forma definitiva, y una instancia que se destruye
y se recrea nunca queda expuesta. Si va a actualizar en caliente con dispositivos enviando
datos, detenga el tráfico de dispositivos durante el despliegue, o prepárese para reiniciar
la instantánea de detección después.
:::

**Un servicio que rechaza su propia configuración sale ahora con un estado distinto de cero.**
Antes registraba «refusing to start» y terminaba con estado 0, así que el pod informaba
`Completed` — exactamente lo que informa un apagado ordenado, e indistinguible de uno a simple
vista. Esos pods entrarán ahora en `CrashLoopBackOff`. No ha cambiado nada sobre qué
configuraciones se rechazan; lo que cambió es que el rechazo se ve ahora en `kubectl get pods`,
en un contador de reinicios y para cualquier cosa que alerte sobre ellos. Un servicio que no
consigue apagarse limpiamente se informa igual, por el mismo motivo. Si tiene una alerta que
trata un pod de servicio en `Completed` como benigno, esta es la versión en la que el fallo
subyacente empieza a llegarle.

### v0.12.1: un parche, nada que hacer {#v0121-upgrade}

La `v0.12.1` es un `helm upgrade` sencillo desde la `v0.12.0`. No añade ninguna migración, por
lo que la base de datos queda intacta, y no cambia ninguna API, tema, permiso ni clave de
configuración: todo lo que describe la sección de la v0.12.0 anterior sigue siendo exactamente
lo que usted está ejecutando.

Vale la pena conocer dos correcciones:

- **Los colores de estado de la consola web** ahora cumplen el contraste WCAG AA en los temas
  claro y oscuro. Las insignias `pending` y `online` fallaban en ambos temas, y el texto de
  error fallaba en el tema oscuro. Lo que los colores *significan* no ha cambiado, pero las
  insignias rellenas son visiblemente más oscuras, porque esa es la única forma de que las
  letras blancas sobre ellas resulten legibles.
- **El monitor de inactividad** ya no lee en memoria todos los dispositivos de todos los
  inquilinos en cada pasada, ni emite una ida y vuelta a la base de datos por cada dispositivo
  que marca; decide y escribe en una sola sentencia. Los dispositivos pasan a inactivos según
  exactamente el mismo calendario que antes —esto es un cambio de coste, no de comportamiento—
  y se nota sobre todo en flotas grandes y en los momentos justo después de que una fuente de
  presencia devuelva sus dispositivos.

### v0.13.0 — los límites de geocercas pasan a formar parte de su plan {#v0130-upgrade}

`v0.13.0` es un `helm upgrade` normal, y no cambia ningún tema, permiso ni clave de
configuración.

Sí cambia la base de datos, de forma aditiva: crea una tabla para las formas de las geocercas,
añade tres columnas anulables al registro del inquilino y reescribe una sola vez, en su sitio, el
historial de geocercas almacenado para adaptarlo al nuevo formato. No se elimina nada y no hay
que recrear nada. Ese último paso es el que conviene conocer si ya utiliza geocercas: antes de
que existiera, una instancia actualizada conservaba todas sus geocercas, pero el motor de
detección ya no podía resolver las formas a las que apuntaban, así que las reglas de geocercas
dejaban de coincidir hasta que alguien editaba una geocerca. La actualización ahora repara ese
historial mientras se ejecuta. Se puede volver a ejecutar sin riesgo y no hace nada en una
instancia que ya la haya aplicado.

Lo que cambia es que los dos límites de geocercas que antes eran fijos para todos — 512
posiciones en una geocerca, 100 geocercas por inquilino — ahora son **ajustes de su plan**, junto
a un tercero: un límite sobre el total de posiciones de todo su conjunto de geocercas. Los tres
conservan sus valores anteriores de forma predeterminada, así que **un inquilino al que nunca se
le hayan cambiado queda medido exactamente donde estaba** y no tiene que hacer nada.

Dos cosas que conviene saber antes de actualizar:

- **El límite del conjunto completo es nuevo, y su valor predeterminado es el que los otros dos
  ya implicaban**: 51.200 posiciones, que son 100 geocercas de 512. Así que un inquilino que use
  geocercas exactamente hasta los límites documentados queda *en* el nuevo límite, nunca por
  encima. El total cuenta formas **distintas**, así que dos geocercas dibujadas de forma
  idéntica cuestan una.
- **Un cambio solo se rechaza cuando hace un número mayor.** Si más adelante un operador baja
  uno de sus límites por debajo de lo que ya tiene, conserva todas sus geocercas. Editar el
  nombre o la descripción de una geocerca, y eliminar una geocerca, siempre siguen funcionando:
  la comprobación es sobre el crecimiento, no sobre el tamaño. Esto es lo que evita que un
  cambio de plan deje varadas geocercas que eran válidas cuando se dibujaron. Hacer una geocerca
  más pequeña casi siempre funciona también; la excepción es que el total del conjunto cuenta
  formas *distintas*, así que editar una de varias geocercas dibujadas igual la separa del resto
  y puede subir el total aunque esa geocerca se haya encogido.

Una consecuencia que hay que prever: como eliminar una geocerca baja el total almacenado, un
inquilino que esté por encima de un límite y elimine una geocerca no podrá volver a crearla.
Para mover una geocerca a otro token, **cree primero la nueva y elimine después la antigua**, lo
que requiere un hueco libre de geocerca durante el momento en que ambas existen.

Los operadores que empaqueten planes deben saber que estos tienen topes reales, porque no todos se
gastan solo en el inquilino: el total del conjunto es una porción de una caché de geometría de la
que tiran todos los inquilinos de la instancia, y el número de geocercas acota un anuncio que tiene
que caber en un solo mensaje del broker. Los rechazos
nombran tanto el número como el ajuste que hay que subir, y una métrica
`geofence_cap_refusals_total` los cuenta según qué límite rechazó.

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
