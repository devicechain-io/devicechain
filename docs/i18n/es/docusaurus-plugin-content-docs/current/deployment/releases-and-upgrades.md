---
sidebar_position: 4
title: Versiones y actualizaciones
---

# Versiones y actualizaciones

DeviceChain se distribuye como un conjunto de imágenes de contenedor precompiladas y versionadas,
más un chart de Helm. **No** necesita compilar nada para ejecutarlo: descargue una versión publicada,
instale el chart y actualice in situ sin tiempo de inactividad.

:::warning Hay versiones a las que no se puede actualizar
Hasta ahora, tres puntos de la historia exigen recrear la instancia en lugar de actualizarla:

- La **`v0.9.0`** reemplazó la cadena de migraciones de cada servicio por una única línea base
  congelada, por lo que una base de datos `v0.8.x` falla con `already exists` al encontrarla.
  Consulte [La compactación de la línea base de la v0.9.0](#v090-baseline-squash).
- La **`v0.10.0`** cambió la clave primaria de las tablas de eventos para corregir un defecto que
  descartaba telemetría de forma silenciosa. Consulte
  [El cambio de clave de eventos de la v0.10.0](#v0100-event-key).
- **Cualquier instancia creada por la `v0.16.0` o una versión anterior**, que no registró
  ninguna declaración de lo que es la instancia: el registro que una actualización lee ahora
  para saber qué desplegar. Consulte
  [Instancias creadas por la v0.16.0 y anteriores](#pre-declaration-recreate).

Si está en cualquiera de ellos, lea la sección correspondiente más abajo antes de hacer nada más.
:::

:::caution Cruzar la v0.12.0 requiere algunos cambios previos
La `v0.12.0` se actualiza en el sitio, pero cambia el tema en el que un dispositivo responde
a un comando, mueve un permiso y cambia varias cosas cuya forma se mantuvo igual. Un
La actualización informará éxito en cualquier caso. Lea
[v0.12.0: una actualización que cambia contratos](#v0120-upgrade) antes de empezar.

Esto se aplica a **cualquier** actualización que cruce la `v0.12.0`, no solo a la que se
detiene ahí: pasar de la `v0.11.0` directamente a un parche posterior no omite esos cambios.
:::

## Modelo de versionado

Cada versión es una única etiqueta git de versión semántica (`vX.Y.Z`). Ese único número cubre
**todo en conjunto**: cada imagen de servicio, el operador, el chart de Helm y la CLI
`dcctl` se publican todos con la misma versión. No hay desfase de versión por servicio
del que preocuparse: un despliegue es un único número coherente.

Dos comandos mueven todo ello, y cuál mueve qué se decide por el **tiempo de vida** de cada
cosa. El operador es un solo controlador por clúster, compartido por todas las instancias que
haya en él, así que `dcctl install` lo mueve junto con el resto de los requisitos previos del
clúster. El documento de configuración, la versión desplegada del chart y las imágenes de los
servicios pertenecen a una instancia, así que `dcctl upgrade` mueve esos, instancia por
instancia. Consulte [Actualizaciones sin tiempo de inactividad](#zero-downtime-upgrades) para el
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
- **dejar de admitir la actualización desde una instancia más antigua** por un motivo que no
  tiene nada que ver con el esquema: la versión posterior a la `v0.16.0` lee un registro de lo
  que es una instancia que las versiones anteriores nunca escribieron, y se niega en lugar de
  inventárselo

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

`DC_ROOT_KEY`, más abajo, es la clave raíz del almacén de secretos de la instancia:
la requieren todos los perfiles, se genera una sola vez con `openssl rand -base64 32` y se
pasa sin cambios en cada instalación y actualización. Consulte
[Desplegando con Helm](./kubernetes-operator.md#desplegando-con-helm) para saber por qué.

Sustituya `<version>` por una etiqueta realmente publicada —la
[página de versiones](https://github.com/devicechain-io/devicechain/releases) las lista, y un
valor no publicado falla al descargar la imagen, no en el momento de la instalación—.

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

El chart de Helm en sí también se publica como un artefacto OCI, por lo que puede instalarlo sin una
copia local del repositorio. La versión del chart es la versión publicada sin la `v` inicial
—`--version 0.16.0` instala la versión `v0.16.0`—, así que no hay un número aparte que buscar;
`helm show chart oci://ghcr.io/devicechain-io/charts/devicechain` imprime la última, y
`--version` rechaza cualquier valor que nunca se haya publicado:

```bash
helm install dc oci://ghcr.io/devicechain-io/charts/devicechain \
  --version <chart-version> \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

El chart también está publicado en
[Artifact Hub](https://artifacthub.io/packages/helm/devicechain/devicechain), que muestra
cada versión publicada junto con sus valores predeterminados y sus plantillas renderizadas.

### Actualizar una instalación hecha solo con el chart {#chart-only-upgrade}

Una instancia instalada con `helm install` en lugar de `dcctl bootstrap` se actualiza con
`helm upgrade`, y conserva una trampa que la ruta de `dcctl` no tiene.

`dcctl upgrade` no se aplica a ella. Ese comando vuelve a leer del clúster la declaración de
una instancia y su documento de configuración, y una instalación hecha solo con el chart no
tiene ninguno de los dos.

El release de abajo se llama `dc` porque ese es el nombre que eligió el `helm install` de más
arriba. Una instancia instalada con `dcctl bootstrap` lleva un release con el nombre de la
instancia —`devicechain` se instala como `dc-devicechain`—, así que cualquier comando `helm`
dirigido a una de esas necesita ese nombre en su lugar.

```bash
helm get values dc -n default -o yaml > dc-values.yaml

helm upgrade dc deploy/helm/devicechain \
  -n default \
  -f dc-values.yaml \
  --set image.tag=<new-version>

rm dc-values.yaml   # este archivo contiene los secretos de su instancia
```

:::warning Traslade los valores: `--set image.tag=…` por sí solo no funcionará
La regla de Helm es la trampa. Una actualización que **no** pasa ningún valor reutiliza los que
ya están en la versión desplegada. Pero en cuanto pasa *cualquier* valor —incluido el único
`--set` que cambia la versión, que es justamente el objetivo de una actualización— Helm parte
de los valores predeterminados del chart y todo lo que usted fijó al instalar desaparece. Eso
incluye la clave raíz de la instancia, sin la cual los secretos almacenados de una instancia en
funcionamiento no se pueden leer.

Cuando eso ocurre no se corrompe nada, porque el chart se niega a renderizar sin la clave raíz:

```
Error: UPGRADE FAILED: execution error at (devicechain/templates/instance-config.yaml:21:4): instance.config.infrastructure.secrets.rootKey is required: every instance seals its token-signing key under it, along with any integration credentials it stores, and user-management cannot start without it. Set it to a base64 256-bit key (openssl rand -base64 32); dcctl bootstrap mints one automatically.
```

`--reuse-values` también funciona, pero conserva en silencio entradas obsoletas cuando los
valores predeterminados del chart cambian entre versiones, así que es preferible volcar los
valores y pasarlos con `-f`, donde puede verlos.
:::

:::caution Si la configuración de instancia procede de un Secret que usted gestiona
Una instalación que monta su configuración de instancia desde un Secret que el chart no
escribe —fijado con `instance.existingSecret`, el patrón que producen External Secrets y
sealed-secrets— tiene ahora que cumplir cuatro condiciones, y `helm upgrade` **hace fallar el
renderizado** en lugar de continuar cuando alguna no se cumple. Cuando eso ocurre, nada cambia
en la release; el rechazo es todo el efecto.

- **El Secret debe llamarse `dci-<instance.id>-config`**, en el namespace de la instancia.
  Cualquier otro nombre se rechaza, porque `dcctl` vuelve a leer la configuración exactamente
  por ese nombre para decidir si una instancia ya existe, y trata un Secret ausente como una
  instalación nueva: una nueva ejecución acuñaría una nueva clave raíz y nuevas credenciales de
  base de datos y de broker sobre una instancia en funcionamiento. Si su Secret está bajo otro
  nombre, créelo de nuevo bajo este (el `target.name` de External Secrets, el `metadata.name`
  de un sealed secret) antes de actualizar.
- **`instance.existingSecretChecksum` es obligatorio**: el sha256 del documento bajo la clave
  `instance` del Secret, como 64 caracteres hexadecimales en minúsculas. El chart no puede leer
  su Secret, así que esto es lo que reinicia los pods cuando la configuración cambia; sin él,
  una credencial rotada se aplicaría sin problemas, no reiniciaría nada e informaría éxito.
  Vuelva a calcularlo cada vez que el documento cambie:

  ```bash
  kubectl get secret dci-<instance.id>-config -n dci-<instance.id> \
    -o jsonpath='{.data.instance}' | base64 -d | sha256sum | cut -d' ' -f1
  ```

- **`networkPolicy.externalConfigPorts` es obligatorio mientras `networkPolicy.enabled` esté
  activado**, con las claves `nats` y `rdb` repitiendo los puertos del broker y de la base de
  datos que nombra su documento. De lo contrario el chart los tomaría de sus propios valores
  predeterminados, y un puerto que no coincida bloquea en silencio la salida de los propios
  servicios, lo que se presenta como una caída del broker o de la base de datos.
- **`metrics.natsBrokerHost` es obligatorio mientras `metrics.natsPodMonitor` esté activado**:
  el nombre de host del broker tal como lo nombra su documento (el nombre corto del Service
  para un broker en el namespace de esta instancia, `<service>.<namespace>` para uno en otro
  lugar). Decide qué namespace vigila el PodMonitor; un valor predeterminado que no coincida no
  monitoriza nada. O bien fije `metrics.natsPodMonitor=false`.

Cada error de `helm` nombra el valor que necesita y por qué. Las dos transformaciones que el
chart aplica normalmente al escribir el documento —el bloque `infrastructure.shutdown` y la
eliminación de `infrastructure.aiInference` cuando esa área no está desplegada— siguen
correspondiéndole a usted reproducirlas, como hasta ahora.
:::

## Actualizaciones sin tiempo de inactividad {#zero-downtime-upgrades}

Actualizar consiste en **dos comandos** —uno para el clúster y luego uno por cada instancia
que haya en él—, y el chart y los servicios están diseñados para hacer avanzar a los clientes sin
perder tráfico. Hay cuatro excepciones, todas documentadas más abajo: la transición a la
ingesta duradera, que sigue siendo una actualización corriente pero tiene un efecto secundario
visible, y la **`v0.9.0`, la `v0.10.0` y cualquier instancia creada por la `v0.16.0` o una
versión anterior, a las que no se puede actualizar en absoluto**. Consulte las notas de la
versión a la que va a migrar antes de ejecutarlo:

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

Una versión de DeviceChain es un único número que abarca las imágenes de los servicios, el
chart, el operador y `dcctl`. Los dos comandos se lo reparten según a qué pertenece cada cosa:

**`dcctl install` mueve el clúster.** El operador —su namespace, sus CRD, su RBAC y su
controlador— se aplica a partir de manifiestos incrustados en `dcctl`. No forma parte del
chart de Helm, así que nada dentro del chart puede alcanzarlo. Se aplica el flujo renderizado
completo y no solo la imagen del controlador, porque los CRD van en él: un esquema que se
quedara en la versión con la que se arrancó la instancia descartaría en silencio cualquier
campo que añadiera una versión posterior. Este comando mueve además el resto de los requisitos
previos compartidos del clúster; consulte
[Instalar el clúster](./bootstrap.md#install).

**`dcctl upgrade` mueve una instancia**, y no mueve nada de lo compartido:

1. **el documento de configuración** del que cada servicio lee sus credenciales y sus
   endpoints, recompuesto a partir del chart de esta versión y escrito por `dcctl`, que es su
   dueño;
2. **la versión desplegada de Helm** que ejecuta los servicios, que los hace avanzar a las
   imágenes nuevas y espera a que cada área termine.

**El orden importa, y la actualización lo comprueba.** El operador es lo que define la
declaración de la instancia, así que tiene que estar en la versión nueva antes de mover una
instancia a ella. `dcctl upgrade` lee el operador que lleva el clúster y **rechaza** una
instancia cuyo clúster no tenga operador alguno, o cuyo operador sea identificablemente el de
otra versión, nombrando el comando de instalación que hay que ejecutar primero. No aplica el
operador él mismo: en un clúster con varias instancias eso movería el controlador de todas
las demás como efecto secundario de actualizar una, en silencio.

Hay un caso que deja pasar con un aviso en lugar de rechazarlo. `dcctl install` deja
constancia de qué versión instaló las definiciones; un operador puesto en el clúster **a
mano** no lleva esa constancia, y `dcctl` no puede distinguir una instalación manual
deliberada de una que un `dcctl` más antiguo sobrescribió. En lugar de pasar por encima de
una decisión que no puede ver, imprime una nota con el comando de instalación y continúa. Si
usted no instaló el operador a mano, tome esa nota como el rechazo que habría sido y ejecute
`dcctl install` antes de seguir.

Ejecute cualquiera de los dos con `--dry-run` primero si quiere ver qué movería. `dcctl
upgrade` toma el clúster de destino del propio registro de la instancia en lugar de
adivinarlo, y dice cuál es.

:::tip Lee todas las credenciales y no acuña ninguna
`dcctl upgrade` conserva aquello sobre lo que la instancia está funcionando: las contraseñas
propietarias de las bases de datos, la autoridad y los inicios de sesión del bróker, el secreto
entre servicios, la clave raíz del almacén de secretos y —cuando el clúster los ejecuta— la
contraseña de administrador del panel de monitoreo y la credencial del almacén de objetos de
respaldo interno del clúster. Un cambio de versión no puede convertirse en un cambio de
credenciales.

Esto está verificado, no solo afirmado. Se comprobó la actualización de una instancia en
funcionamiento comparando un resumen criptográfico (digest) de cada una de esas credenciales
antes y después, y lo único que había cambiado era la etiqueta de imagen: en todos los
servicios, en la consola y en el operador.
:::

:::warning Una actualización no sirve para rotar credenciales
Como las conserva por diseño, no rota nada. Si necesita cambiar una credencial, una
actualización no lo hará — y para varias de ellas hoy no existe un procedimiento admitido.
:::

:::note Mueve una versión, no la forma de una instancia
El perfil, la topología y las áreas funcionales habilitadas provienen de la propia declaración
de la instancia —lo que `dcctl bootstrap` registró en el clúster—, no de flags escritos aquí.
Cambiar lo que una instancia *es* es una pregunta distinta con respuestas distintas: subir el
número de réplicas, por ejemplo, no vuelve a replicar los streams de mensajería que se crearon
con el número anterior.

Otras dos cosas quedan deliberadamente fuera de este comando. No ejecuta la aplicación de
infraestructura, porque una de las entradas de esa aplicación no se puede recuperar del
clúster: los nombres de endpoint y de bucket de un destino de respaldo externo, que provienen
del archivo que usted entregó a `dcctl install --backup-credentials-file`. Y no toca las bases
de datos más allá de dejar que los servicios ejecuten sus propias migraciones.
:::

### Qué más comprueba una actualización {#upgrade-checks}

Dos cosas viajan con ella, porque un cambio de versión es lo que le ocurre de forma fiable a
una instancia en funcionamiento, y un calendario no.

**El certificado del bróker.** El bróker de mensajería sirve un certificado válido durante un
año, emitido por una autoridad que `dcctl` acuña en el arranque inicial y conserva en el
clúster. Una actualización vuelve a emitir ese certificado cuando está dentro de sus últimos 30
días, o cuando ya no cubre todos los nombres por los que los bróker se llaman entre sí —que es
lo que le hace escalar una instancia a un certificado que, por lo demás, sigue holgadamente en
vigor. La reemisión se hace bajo la **misma** autoridad, así que nada tiene que volver a
confiar en nada, y el bróker se reinicia para que sirva de verdad el certificado nuevo en lugar
de conservar el antiguo hasta que algo ajeno lo reinicie. Fuera de esas condiciones la
comprobación se ejecuta y no hace nada.

Una instancia arrancada antes de que `dcctl` conservara esa autoridad no puede tener su
certificado reemitido en sitio. La actualización lo indica y continúa en lugar de fallar;
recrear la instancia es lo que acuña una autoridad y un certificado nuevos.

**El depósito (escrow) de la clave raíz.** Cada actualización comprueba que el artefacto de
depósito de esta instancia siga protegiendo la clave sobre la que la instancia está realmente
funcionando. Esa comprobación **no necesita contraseña**: el artefacto registra una huella de
la clave que protege, así que compararla con la que está en uso no abre nada.

| Qué encuentra | Qué hace |
|---|---|
| El artefacto protege la clave en uso | Lo indica y lo deja intacto |
| El artefacto protege una clave **distinta** | Avisa con claridad. Lo más habitual es que pertenezca a una instancia anterior con el mismo nombre, y restaurar desde él recuperaría un clúster incapaz de leer sus propios secretos |
| No hay artefacto | Escribe uno, si usted pasó `--escrow-passphrase-file` (o fijó `DCCTL_ESCROW_PASSPHRASE`). Si no, avisa de que la única copia de la clave raíz está dentro del clúster |

Así es como una instancia creada al principio con `--no-escrow` obtiene un depósito más tarde.
Ninguno de esos desenlaces hace fallar la actualización: un problema de depósito trata de un
desastre futuro y la actualización que tiene delante trata de la instancia en funcionamiento, y
un operador que no puede actualizar rodeará la comprobación en lugar de arreglarla.

:::note La mitad de la instancia solía ser un `helm upgrade`
El procedimiento era: volcar a un archivo los valores de la versión desplegada actual con `helm
get values`, volver a pasarlos con `-f` junto a la nueva etiqueta de imagen, borrar el archivo
porque contenía sus secretos, y después mover el operador por separado.

Ese baile existía únicamente porque la versión desplegada de Helm era donde vivían las
credenciales generadas de la instancia, y Helm parte de los valores predeterminados del chart
en cuanto se le pasa cualquier valor —de modo que una actualización que no las trasladara a
mano las perdía. Ahora `dcctl` es el dueño del documento de configuración, la versión desplegada
ya no contiene esas credenciales, y el paso que le decía que escribiera sus secretos en un
archivo simplemente desaparece.

El hueco que aquella forma dejaba abierto era una actualización que se detenía tras la mitad
de `helm`: dejaba los servicios nuevos ejecutándose contra el controlador con el que se arrancó
la instancia por primera vez, indefinidamente y sin ningún error que lo indicara. Eso es lo que
cierra el rechazo descrito más arriba: los dos comandos siguen siendo dos, porque el operador
pertenece al clúster y la versión desplegada pertenece a la instancia, pero `dcctl upgrade`
lee ahora qué operador lleva el clúster y lo dice: rechaza cuando puede distinguirlos, y avisa
cuando no puede.
:::

Lo que hace que el despliegue sea seguro:

- **Aumentar antes de terminar.** Los Deployments usan por defecto una estrategia `RollingUpdate` con
  `maxUnavailable: 0` y `maxSurge: 1`, de modo que un pod nuevo debe pasar su sonda de disponibilidad
  `/readyz` **antes** de que se elimine un pod antiguo, y la capacidad nunca disminuye durante el
  despliegue. Cuatro áreas se distribuyen en cambio con `strategy: Recreate` y una sola réplica, porque
  solo uno de sus pods puede servir a la vez: `event-processing` (el motor de reglas es un escritor
  único), `mcp` (la sesión de un cliente vive en el pod que la abrió), `sparkplug-ingest` (un Sparkplug
  Host por pod) y `lwm2m-ingest` (un socket CoAP/UDP por pod). En ellas, todos los pods antiguos se
  detienen antes de que arranque el nuevo, así que un despliegue tiene una breve brecha por diseño; y
  el chart rechaza `Recreate` con más de una réplica.
- **Apagado ordenado / drenaje de conexiones.** Cuando se le pide a un pod que termine, primero
  informa "no listo" (de modo que el Service deje de enrutarle nuevas solicitudes), espera una breve
  ventana de drenaje para que ese cambio se propague, y solo entonces termina el trabajo en curso y
  se apaga. Configure la ventana con `shutdownDrainSeconds` (por defecto `5`), mantenida de forma segura
  por debajo de `terminationGracePeriodSeconds` (por defecto `30`). Ambos forman un único presupuesto
  y los servicios lo verifican: el drenaje puede ocupar como máximo **la mitad** del período de gracia,
  porque la ventana solo espera — terminar las solicitudes en curso, drenar los consumidores del bróker
  y cerrar el pool de base de datos ocurren después de ella, y el kubelet envía SIGKILL cuando el
  período de gracia expira, haya terminado eso o no. Una ventana mayor se rechaza al arrancar el
  servicio (y en `dcctl bootstrap`, antes de instalar nada), en lugar de descubrirse cuando un pod ya
  se está apagando. Ponga `shutdownDrainSeconds: 0` para omitir el drenaje por completo, lo que encaja
  con una ejecución de una sola instancia sin ningún Service del que retirarse.
- **Migraciones de esquema coordinadas.** Los servicios ejecutan migraciones de base de datos bajo un bloqueo
  a nivel de base de datos, de modo que cuando varias réplicas se inician a la vez, exactamente una aplica las
  migraciones y el resto espera; sin condiciones de carrera, sin DDL duplicado.

:::tip Ejecute al menos dos réplicas en producción
Para lograr un verdadero cero tiempo de inactividad, ejecute `replicas: 2` (o más) para cada área que
pueda servir desde más de un pod, de modo que el despliegue siempre tenga un pod activo sirviendo tráfico.
Una sola réplica igualmente tiene una breve brecha mientras se reemplaza su único pod.
Configúrelo globalmente con `--set replicas=2`, o por área bajo
`functionalAreas.<area>.replicas`. Las cuatro áreas de un solo pod mencionadas arriba son la excepción:
`mcp`, `sparkplug-ingest` y `lwm2m-ingest` rechazan más de una réplica con cualquier estrategia, y
`event-processing` acepta una segunda réplica solo como reserva en caliente, con `strategy: RollingUpdate`
configurada junto a ella; en cualquier otro caso el renderizado falla y explica por qué. Un
`PodDisruptionBudget` se genera automáticamente para cualquier área con más de una réplica, de modo que
los drenajes de nodo no puedan expulsar a todas las réplicas a la vez.

Los techos de tasa los aplica cada réplica por separado, de modo que dos réplicas de `event-sources`,
`outbound-connectors` o `ai-inference` pueden admitir hasta el doble del techo de un inquilino. Consulte
[Gobernanza](../concepts/governance.md#per-replica).
:::

### La compactación de la línea base de la v0.9.0 {#v090-baseline-squash}

La `v0.9.0` es la **primera** de las dos versiones a las que no se puede llegar actualizando en sitio
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
dcctl destroy local devicechain --without-state   # elimina la instancia; el clúster antiguo va después
kind delete cluster --name devicechain     # y el clúster que preparó la versión anterior
dcctl install local                        # prepara un clúster nuevo
dcctl bootstrap local devicechain
```

Con el `dcctl` actual se recrea también el clúster, no solo la instancia: consulte
[por qué](#pre-declaration-recreate).

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

La `v0.10.0` es la segunda versión a la que **no se puede llegar actualizando en sitio**, por un motivo
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
dcctl destroy local devicechain --without-state   # elimina la instancia; el clúster antiguo va después
kind delete cluster --name devicechain     # y el clúster que preparó la versión anterior
dcctl install local                        # prepara un clúster nuevo
dcctl bootstrap local devicechain
```

Con el `dcctl` actual se recrea también el clúster, no solo la instancia: consulte
[por qué](#pre-declaration-recreate).

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

La `v0.11.0` es la primera versión desde la `v0.8.5` a la que se puede llegar en sitio.
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

Se puede llegar a la `v0.12.0` en sitio. Su cambio de esquema **añade**
migraciones en lugar de reemplazar una línea base, así que una base de datos `v0.11.0`
existente se conserva con sus filas intactas, y esto se midió sobre una instancia en
ejecución en lugar de razonarse.

Lo que sí cambia son los **contratos**: el tema MQTT en el que un dispositivo responde a un
comando, unas cuantas operaciones GraphQL y el significado de varias cosas cuya forma no
cambió en absoluto. Nada de eso se ve en una actualización que informa éxito, así que lea
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
la actualización reinicia todos los servicios y no solo aquellos cuya imagen se movió. Es una
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

:::note[Esto ya no aplica]
`updateDeviceProfile` ha pasado desde entonces a ser una [actualización
parcial](../reference/graphql-api.md#which-mutations-are-partial-updates): una petición que no
dice nada sobre la declaración ahora la deja como está, y limpiarla requiere un `null` explícito.
El consejo de arriba es lo que hay que hacer en una instancia `v0.12.x` o `v0.13.x`; en una actual
no hay nada que arrastrar. Renombrar un perfil es una [mutación
propia](../reference/graphql-api.md#renaming-a-record).
:::

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
infraestructura, no de la versión desplegada, así que ninguno se materializa durante la actualización
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

La `v0.12.1` es una actualización en sitio corriente desde la `v0.12.0`. No añade ninguna migración, por
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

`v0.13.0` es una actualización en sitio corriente, y no cambia ningún tema, permiso ni clave de
configuración.

Sí cambia la base de datos, de forma aditiva: crea una tabla para las formas de las geocercas,
añade tres columnas anulables al registro del inquilino y reescribe una sola vez, en su sitio, el
historial de geocercas almacenado para adaptarlo al nuevo formato. No se elimina nada y no hay
que recrear nada.

Ese último paso es el que conviene conocer si ya utiliza geocercas. A partir de `v0.13.0` la
forma de una geocerca se almacena una sola vez y se referencia por su contenido, en lugar de
copiarse en cada versión de su conjunto de geocercas, y la actualización reescribe el historial
que ya tiene para que lo referencie del mismo modo. Sus geocercas y su historial se conservan sin
cambios; lo que cambia es cómo se almacenan. El paso se puede volver a ejecutar sin riesgo y no
hace nada en una instancia que ya lo haya aplicado.

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

### v0.14.0 — los paquetes contra los que compila {#v0140-upgrade}

`v0.14.0` es una actualización en sitio corriente desde `v0.13.x`. No añade ninguna migración, así que la base
de datos queda intacta, y no cambia ninguna API, tema, permiso ni clave de configuración. **Si
solo ejecuta la plataforma, no hay nada que hacer.**

Lo que cambió está a su alrededor: los artefactos contra los que compila y la CLI con la que la
ejecuta.

**El runtime web está publicado.** `@devicechain/client`, `@devicechain/dashboards`,
`@devicechain/widgets` y `@devicechain/brand` están en npm, así que embeber un panel o un widget en
su propia aplicación es una instalación y ya no una compilación contra nuestro árbol de fuentes.
Los cuatro se publican juntos en una misma versión y están fijados entre sí. Ver
[Paquetes de npm](../reference/npm-packages.md) para la línea de instalación y la política de
dist-tags.

**Si estaba compilando nuestros widgets desde el árbol de fuentes, hay un cambio que le
corresponde hacer.** `maplibre-gl` ahora es una dependencia peer de `@devicechain/widgets`: su
aplicación proporciona la biblioteca, la URL de su worker y su hoja de estilos, en lugar de que el
paquete de widgets las decida por usted. Eso es lo que hace que el paquete funcione bajo un
empaquetador que no controlamos — pero significa que un widget de mapa sin cableado del anfitrión
por encima ahora muestra un aviso explícito en lugar de un lienzo en blanco, que es el síntoma que
debe esperar si actualiza sin hacerlo. El cableado es corto y está descrito en
[Renderizar un mapa](../reference/npm-packages.md#map-host-wiring). En el servidor no cambia nada.

**El SDK cliente para .NET y Unity está publicado** en nuget.org como `DeviceChain.Sdk`.

**`dcctl` ya puede decirle qué ha arrancado, y apagarlo todo.**

```bash
# cada instancia, el clúster en el que vive y si ese clúster sigue existiendo
dcctl instances list

# destruirlas todas
dcctl destroy --all
```

🔴 **Esto cierra un defecto sobre el que conviene actuar, no solo conocerlo.** Hasta ahora nada
registraba en qué clúster se había arrancado una instancia: se derivaba del nombre de la instancia
al crearla y se volvía a derivar al destruirla. Esa derivación es incorrecta para cualquier
instancia arrancada con `--kube-context`, y el fallo era silencioso en la peor dirección: `dcctl
destroy` pedía al proveedor que borrara un clúster que no existía, lo cual tiene éxito sin decir
nada, eliminaba el estado local e informaba de que la instancia había sido destruida mientras su
clúster real seguía funcionando. **Si alguna vez arrancó con `--kube-context` y después destruyó
esa instancia, es probable que su clúster siga en pie.** `dcctl instances list` no puede
mostrárselas — la destrucción eliminó el registro local, que es justamente el problema — así que
pregunte directamente al proveedor (para el proveedor local, `kind get clusters`) y borre lo que
reconozca.

A partir de esta versión el clúster se anota al arrancar y se relee al destruir, y la línea final
dice cuál de tres cosas ocurrió: se borró el clúster registrado, el clúster ya no estaba y solo se
limpió el estado local, o el registro no era fiable y no se tocó nada. Ninguna de ellas es la
frase antigua impresa sobre un clúster que sigue funcionando.

Las instancias creadas antes de esta versión no tienen ese registro y aparecen como `no record —
destroy will guess the cluster`. La destrucción sigue funcionando sobre ellas recurriendo a la
derivación antigua, así que la advertencia anterior sigue aplicando a ellas y solo a ellas.

:::note `dcctl destroy` ya no elimina clústeres
En las versiones actuales `dcctl destroy` elimina solo una instancia —su release de Helm, su broker
NATS y su almacén de eventos, su base de datos y su login, su namespace y su estado local— y nunca elimina un clúster ni los requisitos
previos que dejó `dcctl install`. Por tanto, `dcctl destroy --all` elimina todas las instancias y
deja todos los clústeres en marcha. Para eliminar un clúster local, usa
`kind delete cluster --name <name>`. Consulta [Eliminar una instancia](./bootstrap.md#destroy).
:::

### v0.15.0 — las actualizaciones dejan de borrar lo que no envió {#v0150-upgrade}

`v0.15.0` es una actualización en sitio corriente desde `v0.14.x`. Las migraciones nuevas se
ejecutan solas al arrancar los servicios, no hay nada que recrear y ningún dato debe moverse a
mano.

Los cambios incompatibles están en la **API** y en el **acceso de red saliente**, no en la
actualización en sí. Si administra la plataforma y la maneja desde la consola, aquí no hay nada
que deba hacer. Las secciones siguientes son para quienes llaman a la API directamente, quienes
envían notificaciones a través de algo dentro de su propia red, quienes ejecutan el servidor MCP,
o quienes han personalizado la configuración de `event-sources`.

#### Las actualizaciones ya no reemplazan el registro completo

Este es el cambio que afecta a más gente, y es la razón por la que esta versión está marcada como
incompatible.

Antes, una actualización reemplazaba el registro: **cualquier campo que omitiera se borraba.**
Ahora un campo que no menciona se deja exactamente como estaba, y borrar un valor requiere un
`null` explícito.

La petición en sí tiene una forma nueva que ya no lleva el nombre del propio registro —
actualizar y renombrar son operaciones distintas, y ahora existen mutaciones `rename…` dedicadas
para los cuatro tipos que lo necesitan. Por tanto, una aplicación que llame a la API directamente
debe **quitar el nombre de sus peticiones de actualización y regenerar su código cliente.**

**Una petición con la forma antigua se rechaza de plano**, con un error que nombra el campo
que ya no se acepta. No se aplica a medias y no falla en silencio: se entera en la primera
llamada, y no a partir de un registro que ha perdido la mitad de su contenido.

:::caution El único caso que cambia en silencio
Una aplicación que borraba un valor **omitiendo el campo** ahora conserva el valor anterior. Nada
da error; la actualización simplemente hace menos de lo que hacía. Si su código se apoya en la
omisión para borrar un campo, envíe un `null` explícito en su lugar.

Tenga en cuenta que no todos los campos aceptan `null`: algunos son obligatorios y lo rechazan con
un error específico. Son campos que nunca podrían borrarse legítimamente.
:::

:::danger Un caso delicado que conviene conocer
Si construye una petición de actualización enlazando una **variable distinta por campo**, una
variable que no suministre llega como **null explícito** en lugar de como campo ausente — y null
explícito significa *borra esto*. En el campo `rules` de una política de notificación eso vacía
todo el conjunto de reglas y devuelve éxito. Enlace el objeto de petición completo como una sola
variable, o incluya únicamente los campos que realmente quiere cambiar.
:::

#### El id de un evento almacenado ha cambiado

El `id` de un evento es ahora el identificador propio del evento, en lugar de un valor compuesto a
partir del token del dispositivo, el tipo de evento y la marca de tiempo. **Cualquier id que haya
guardado de una versión anterior ya no coincidirá con nada.**

La forma anterior tampoco era única: un dispositivo que reportaba dos medidas en el mismo instante
producía **el mismo id para ambas**, de modo que cualquier cliente con una caché normalizada
basada en él estaba fusionando esas lecturas en una sola sin avisar. Si guardó ids, vuelva a
leerlos; si los usaba como clave, esto es tanto una corrección como una ruptura.

#### Las conexiones salientes a direcciones privadas ahora se rechazan

Los webhooks de notificación, los **relés SMTP** y las llamadas HTTP de los conectores ya no
pueden alcanzar direcciones de loopback, privadas, de NAT de operador, de enlace local ni de
metadatos de nube. La comprobación ocurre en el momento de conectar, y un rechazo es **definitivo:
no se reintenta.**

Esto está activo por omisión y no hay ningún interruptor para desactivarlo.

:::caution Si su relé de correo vive dentro del clúster, el correo de alarmas dejará de salir
Este es el fallo con más probabilidad de sorprenderle, porque nada en él se parece a un cambio de
política de red: las notificaciones simplemente dejan de llegar, y el fallo queda registrado como
permanente en lugar de pendiente. Autorice las direcciones concretas que utiliza:

```yaml
instance:
  config:
    infrastructure:
      egress:
        allowedDestinations:
          - 10.96.0.25/32      # el relé SMTP dentro del clúster
```

Indique cada destino como su propio `/32`. Los destinos en la internet pública no se ven afectados
y no necesitan entrada.
:::

#### Si ejecuta el servidor MCP

Dos cambios requieren acción, y uno de ellos impide que el servicio arranque:

- **Una URL de recurso con una barra final ahora se rechaza al arrancar.** Un identificador se
  compara de forma exacta, así que una barra final significaba que los tokens quedaban ligados a
  una dirección que nunca terminaba de coincidir. Antes se aceptaba y luego fallaba de forma
  silenciosa; ahora falla de forma visible al arrancar. Quite la barra.
- **Los metadatos de recurso protegido han cambiado de ubicación**, a la que define la
  especificación, con el segmento well-known entre el host y la ruta. El chart los enruta por
  usted. **Si usted mismo termina el ingress, añada una ruta** para el prefijo `/.well-known/` que
  no reescriba la ruta.

#### Se han eliminado dos claves de configuración, y se comportan de forma distinta

- **`debug`, dentro de una entrada de `eventSources`.** La configuración se valida de forma
  estricta, así que dejarla **impide que `event-sources` arranque**, con un error que nombra el
  campo. Elimínela.
- **`inboundEventBatching` y sus `maxBatchSize` / `batchTimeoutMs`.** Esta queda retirada, no
  rechazada: se descarta al cargar la configuración con una advertencia, así que el servicio
  arranca con normalidad. Elimínela cuando le venga bien.

La diferencia no es arbitraria: una clave retirada es una que todavía podemos reconocer por su
nombre, de modo que podemos descartarla por usted. Una clave anidada dentro de una entrada de una
lista no lo es, y por eso la primera tiene que detener el servicio.

#### También en esta versión

Los comandos se despachan ahora en el momento en que se encolan, en lugar de esperar al siguiente
barrido, y el intervalo de ese barrido es configurable si quiere cambiar cada cuánto se ejecuta la
red de seguridad. Los mensajes no entregados (dead letters) se pueden leer y consultar, no solo contar. Hay una vista de
informes a la que puede apuntar una herramienta de BI. Los activos incorporan jerarquía
padre/hijo y un contrato de propiedades documentado, los dispositivos incorporan una operación de
sustitución, las alarmas incorporan reconocimiento masivo, y un inquilino puede elegir el idioma con
el que abre su consola.

Los paquetes npm publicados y el SDK para .NET/Unity no llevan cambios de código en esta versión.
Ahora bien, si su propio código envía mutaciones de actualización a través de ellos, ese código sí
tendrá que regenerarlo usted.

### v0.16.0 — los dispositivos deben nombrar el despacho que responden {#v0160-upgrade}

`v0.16.0` es una actualización en sitio corriente desde `v0.15.x`. Una migración nueva se
ejecuta sola al arrancar `command-delivery`: añade una columna con un valor por defecto, rellena
las filas existentes en la misma sentencia y no requiere nada de usted.

Hay **una comprobación previa que conviene hacer antes de actualizar** y un cambio incompatible
que afecta a los dispositivos, no a quienes llaman a la API. Más allá de eso, esta versión trata
sobre todo de servicios que se niegan a arrancar ante configuraciones que antes se aceptaban y se
ignoraban en silencio — lo cual es más seguro, y también puede detener un pod que llevaba meses
funcionando sin problemas.

#### Antes de actualizar: revise si sus puertos de escucha colisionan

`event-sources` ejecuta más de un servidor HTTP en un mismo proceso — GraphQL en su propio puerto,
más cada fuente de eventos HTTP que tenga configurada. Hasta ahora, que dos de ellos coincidieran
en el mismo puerto mataba un transporte de ingesta **en silencio**: el servidor perdedor moría
dentro de una gorutina y nunca se volvía a mencionar.

Ahora los enlaces son síncronos y un fallo es fatal, de modo que una colisión que llevaba meses
rota **hace que el despliegue entre en ciclo de reinicios.** Ese es el comportamiento correcto y
es también el cambio que con más probabilidad le sorprenderá, porque hoy nada le avisa de ello.

Compare el `port` de cada fuente con los demás y con el puerto de GraphQL, y compruebe que la
entrada `extraPorts` del chart coincide con el `port` de cada fuente. Un `port: "0"` de cara al
dispositivo también se rechaza ahora.

#### Todo dispositivo que responda a un comando debe devolver el nonce del despacho

Este es el único cambio incompatible de protocolo de la versión, y **la población a la que afecta
son los dispositivos construidos fuera de este repositorio** — firmware, pasarelas, cualquier cosa
que hable directamente el protocolo de comandos.

El sobre de entrega lleva un `dispatchNonce`. Un dispositivo que responda a ese comando debe ahora
devolver el mismo valor en el sobre de su respuesta. Una respuesta que lo omita, o que nombre un
despacho que el comando ya ha abandonado, se **rechaza y se registra como carta muerta** en lugar
de resolver el comando.

El motivo es un defecto real, no una cuestión de orden: el mismo comando puede publicarse
legítimamente más de una vez — devuelto a la cola y despachado de nuevo — y sin un nonce no hay
forma de saber a qué despacho pertenece una respuesta. Una respuesta a un despacho ya superado
estaba resolviendo el más reciente con el resultado del anterior.

:::caution Cómo saber si esto es seguro para su flota
La plataforma no puede enumerar dispositivos construidos en otro lugar, así que los cuenta por
usted. Después de actualizar, vigile:

- **`devicechain_commanddelivery_command_delivery_responses_without_nonce_total`** — respuestas que
  no nombraron ningún despacho. Sobre todo dispositivos sin actualizar, y en una flota donde todos
  hablan el contrato actual debería ser **cero**. Cuenta cualquier respuesta sin nonce, así que una
  respuesta duplicada o tardía a un comando ya resuelto también acaba aquí.
- **`devicechain_commanddelivery_command_delivery_responses_stale_nonce_total`** — respuestas que
  nombran un despacho que el comando ya abandonó. La lectura habitual es que se están publicando
  comandos más de una vez, que es el defecto que este cambio corrige; un dispositivo que reenvía una
  entrada antigua de su bandeja de salida también lo produce.

(El `command_delivery` duplicado no es una errata: la serie lleva como prefijos el espacio de
nombres de la plataforma y el área funcional, así que el nombre de arriba es el que se pega en una
consulta.)

Una respuesta rechazada **no se descarta.** Se escribe como carta muerta, porque el informe del
dispositivo sobre lo que hizo no existe en ningún otro sitio — así puede consultar esas respuestas
mientras resuelve el primer contador.
:::

Si sus dispositivos usan el **SDK de .NET/Unity**, actualizar el SDK es toda la solución: lleva el
nonce por usted en ambos sentidos. El adaptador de bajada LwM2M y el simulador de dispositivos se
actualizaron en el mismo cambio. El agente de borde no se ve afectado: envía telemetría y no
recibe comandos.

Junto a ellos llega un contador relacionado:
`devicechain_commanddelivery_command_delivery_responses_not_answerable_total` cuenta una respuesta
que nombró el despacho en el que está su comando y aun así no pudo resolverlo. Ningún estado de
comando del vocabulario actual produce eso, así que debería marcar **cero**; existe para detectar
que se añada más adelante un estado sin que nadie haya decidido si una respuesta puede resolverlo.

#### Ahora los servicios se niegan a arrancar ante cosas que antes aceptaban

Cada uno de estos es una corrección que falla de forma cerrada, y cada uno puede detener un pod que
antes funcionaba:

| Qué | La condición que ahora se rechaza |
| --- | --- |
| Almacén de secretos | una clave raíz de instancia bien formada pero **incorrecta** — antes arrancaba y fallaba en el primer secreto que se le pedía |
| Configuración de instancia | una **clave mal escrita** — antes se descartaba en silencio y se aplicaba el valor por defecto |
| Configuración de instancia | `DC_SHUTDOWN_DRAIN_SECONDS` todavía definida — la variable de entorno ya no existe; el valor es `infrastructure.shutdown.drainSeconds` |
| Configuración de instancia | una ventana de drenaje mayor que **la mitad** del `terminationGracePeriodSeconds` del pod |
| Puertos de escucha | dos servidores en un mismo puerto, o un `port: "0"` de cara al dispositivo |
| Cualquier servidor HTTP | un puerto ya en uso — antes se registraba desde dentro de una gorutina mientras el servicio informaba de un arranque correcto |

La de la clave mal escrita merece un momento. Una errata en `maxSubscriptionMessageBytes` redujo a
la mitad el techo real de trama sin registrar nada — la clave se descartaba y se aplicaba el valor
por defecto, lo que se parece exactamente a una configuración que funciona. La decodificación
estricta hace que se entere al arrancar.

#### Métricas: once series nuevas, ninguna renombrada ni eliminada

Todas las series que existían en v0.15.0 conservan su nombre exacto — nada se renombró y nada
desapareció, ni siquiera con el cambio que dio a cada servicio su propio registro de métricas.

Lo nuevo: cinco contadores en `command-delivery` (los dos del nonce de arriba, más despachos
agotados, respuestas que el estado del comando no pudo aceptar, y respuestas perdidas porque no se
pudo escribir una carta muerta), dos contadores de cartas muertas de alarma en `device-management`,
un contador de cierre temprano en `event-sources`, `is_serving` en `lwm2m-ingest`, y los dos
contadores de renacimiento de Sparkplug de más abajo.

:::caution `is_leader` cambia de significado en `lwm2m-ingest`, y la documentación recomienda alertar sobre él
El indicador se levanta ahora cuando la réplica **adquiere** el arrendamiento, en lugar de después
de terminar de construir su mandato. Construir un mandato tarda hasta 30 segundos por cada
inquilino asignado, así que una réplica que acababa de ganar una conmutación por error informaba
antes `is_leader=0` durante hasta 30 segundos por inquilino mientras sí tenía el arrendamiento.

Si sigue la alerta `sum(devicechain_lwm2mingest_is_leader) != 1` que recomienda la guía de
despliegue, esa ventana de falsa ausencia de líder desaparece. El nuevo indicador
**`devicechain_lwm2mingest_is_serving`** es lo que ahora distingue «líder, todavía construyendo su
mandato» de «líder y sirviendo» — el estado que un solo indicador no podía expresar. `is_leader == 1`
con `is_serving == 0` de forma sostenida es un líder atascado en su construcción. Tenga en cuenta
que el chart no incluye ninguna regla de alerta para ninguno de los dos: esto es orientación para
que la escriba usted, no una regla que hereda.
:::

**`sparkplug-ingest` añade `rebirth_enqueued_total` y `rebirth_dropped_total`.** Una cola de
renacimiento saturada era antes indistinguible de una inactiva en todas las series que el servicio
exportaba, porque `rebirth_requests_total` cuenta publicaciones exitosas — así que la saturación
hacía que *dejara de subir*. Lea el par nuevo en conjunto: descartes que suben mientras las
solicitudes se mantienen en un techo es un abanico de salida que supera a un publicador sano;
descartes que suben mientras las solicitudes siguen planas apunta a la conexión con el broker.

#### Otros comportamientos que conviene conocer

- **Un comando que la plataforma no puede publicar ahora falla.** Antes alternaba entre en cola y
  enviado en cada barrido hasta que su TTL vencía días después, y entonces registraba un tiempo de
  espera agotado — lo que dice que un dispositivo no respondió, cuando nunca se había despachado
  nada. Ahora se detiene en un límite (20 intentos por defecto, unos diez minutos con la cadencia
  de barrido por defecto de 30 segundos) y registra el fallo nombrando a la plataforma. El límite
  puede cambiarlo en `functionalAreas.command-delivery.config.maxDispatchFailures`.
- **El `reason` de una carta muerta por un destino de conector bloqueado es ahora `unprocessable`**
  en lugar de `exhausted`. Actualice cualquier alerta o consulta guardada que use el valor
  anterior; los registros existentes se leen igual que antes.
- **Un cambio de estado de alarma que no se pudo publicar ahora se registra como carta muerta y se
  cuenta**, y `alarm_event_dead_letter_lost_total` se suma a la alerta `DeadLetterWriteLost`.
- **Un mensaje entrante que no se puede decodificar ya no se archiva entero.** El registro apunta
  al original por asunto y número de secuencia del flujo.
- **Las suscripciones GraphQL se cierran limpiamente al apagar** con una trama `1001`, y una trama
  entrante tiene ahora un tope — `infrastructure.graphql.maxSubscriptionMessageBytes`, 4 MiB por
  defecto. Es el único valor nuevo del chart en esta versión, y tiene valor por defecto.
- **El refresco de gobernanza tiene un límite de ritmo** — 50 consultas/s con una ráfaga de 100, por
  dimensión gobernada, y una caché negativa de 10 segundos tras una consulta fallida. Un resolutor
  mantiene al día a unos 3000 inquilinos sin llegar nunca al límite. Por encima, un inquilino ya
  resuelto sigue sirviendo su **último valor conocido** en lugar de caer al valor por defecto de la
  plataforma; solo un inquilino que nunca se ha resuelto recibe el valor por defecto.
- **Aparte**, un techo de ingesta por defecto de la plataforma de `0` se eleva ahora a 100
  mensajes/s con una ráfaga de 200, en lugar de no admitir nada. Es un eje distinto del ritmo de
  consultas anterior.
- **El apagado tiene un límite** derivado del periodo de gracia, menos la ventana de drenaje y un
  margen de dos segundos, y respeta la cancelación en todo momento. **Los bucles de lectura de los consumidores** aplican
  espera creciente y luego hacen fallar el proceso, en lugar de girar en vacío o reintentar
  indefinidamente.
- **Dos detalles del chart con los que es fácil tropezar.**
  `instance.config.infrastructure.metrics.httpPort` está **retirada** — un documento que aún la lleve
  registra un aviso nombrando la clave y arranca con normalidad, y el chart ya no la escribe. Y
  `instance.config.infrastructure.shutdown` la **escribe ahora el chart** por usted a partir de
  `shutdownDrainSeconds` y `terminationGracePeriodSeconds` de nivel superior; definir ese bloque a
  mano hace que `helm` falle al renderizar, en lugar de discrepar en silencio con la especificación
  del pod. Si aporta la configuración de instancia mediante `instance.existingSecret`, ese bloque le
  corresponde añadirlo a usted.
- **Un pod que se termina a sí mismo libera su arrendamiento de liderazgo al salir.** En
  `lwm2m-ingest` se han corregido las dos rutas que salían mientras aún lo tenían. La espera de 30
  segundos antes de que un sustituto pueda tomar el relevo es ahora lo que sigue a una pérdida
  **abrupta** — un fallo de nodo, un `kill -9` — no lo que sigue a un pod que decide detenerse.

#### Los paquetes publicados

El **SDK de .NET/Unity incorpora el cambio del nonce de comando** descrito arriba; actualizarlo es
la forma de que un dispositivo construido sobre él siga respondiendo a comandos.

`@devicechain/client`, `@devicechain/dashboards`, `@devicechain/widgets` y `@devicechain/brand` no
tienen cambios de código en esta versión. Una cosa que conviene saber si instala
`@devicechain/widgets` por su cuenta: su **rango de dependencia par `maplibre-gl` pasa de `^6.6.0`
a `^6.7.0`**. Si fija maplibre-gl en 6.6.x verá un aviso de dependencia par no satisfecha, o un
fallo de instalación con un gestor de paquetes que las exija estrictamente. Nada más cambió en los
paquetes.

### v0.17.0 — las instancias dejan de ser dueñas del clúster en el que se ejecutan {#v0170-upgrade}

🔴 **No hay actualización en sitio a la `v0.17.0`.** Toda instancia creada por la `v0.16.0` o una
versión anterior hay que destruirla y volver a crearla: `dcctl upgrade` se niega e imprime la receta
en lugar de hacer algo a medias. [La siguiente sección](#pre-declaration-recreate) es la que hay que
leer, e indica qué exportar antes: no existe ningún camino que conserve su telemetría, sus
definiciones de dispositivo ni sus paneles a través de esta versión.

Lo que sigue es lo que cambia para usted una vez que esté en ella.

#### `dcctl bootstrap` son ahora dos comandos

`dcctl install <provider>` prepara un **clúster**, una sola vez. `dcctl bootstrap <provider>
<instance>` crea una **instancia** sobre un clúster ya preparado, tantas veces como instancias
quiera.

```bash
dcctl install local
dcctl bootstrap local my-instance
```

Todo lo que dimensiona o da forma al clúster se trasladó a `install` y queda registrado ahí, así que
`--ha`, `--compact`, `--no-monitoring`, `--no-cnpg` y `--max-connections` se fijan **una vez** y
todas las instancias del clúster los siguen. Un bootstrap ya no tiene banderas para ellos. `dcctl
bootstrap` se niega en un clúster donde `install` no ha terminado, y la negativa nombra el comando
que hay que ejecutar.

`dcctl destroy` desmonta ahora la infraestructura propia de una instancia y **deja el clúster en
pie**. `--keep-cluster` desapareció, porque describe lo que destroy hace siempre.

#### Una actualización futura son dos comandos, y el primero es el del clúster

El operador se movió con la separación. Es **un solo controlador por clúster**, compartido por todas
las instancias que haya en él, así que `dcctl install` es lo que lo coloca y lo que lo mueve:

```bash
dcctl install local --version <new-version>
dcctl upgrade local <instance> --version <new-version>
```

`dcctl upgrade` ya no aplica el operador. **Lee** el que lleva el clúster y rechaza una instancia
cuyo clúster no tenga operador, o tenga uno identificablemente de otra versión, nombrando el comando
de instalación que hay que ejecutar primero. La razón importa si ejecuta varias instancias en un
clúster: una actualización que aplicara el operador ella misma lo movía para **todas** las instancias
del clúster, en silencio, como efecto secundario de actualizar una.

Hay un caso que deja pasar con un aviso. Un operador que usted instaló **a mano** no lleva constancia
de qué versión lo puso, y `dcctl` no puede distinguir eso de un operador que un `dcctl` más antiguo
sobrescribió, así que imprime una nota con el comando de instalación y continúa. Si usted no lo
instaló a mano, tome esa nota como el rechazo que habría sido.

#### Un clúster aloja ahora tantas instancias como usted cree

Cada instancia recibe un namespace propio —**`dci-<instance>`**, no el id de la instancia a secas—
con su propio bróker, su propio almacén de eventos y su propio login y base de datos en el almacén
relacional compartido. Dos cosas del clúster siguen pudiendo pertenecer a una sola instancia, y un
bootstrap se niega en lugar de colisionar: el **host de ingress** y el NodePort MQTT local.

El prefijo es la razón de que el namespace no sea simplemente el id de su instancia: una instancia ya
no puede recibir un nombre que colisione con un namespace que usa el propio clúster.

#### Si concede a `dcctl` un RBAC explícito

En un clúster que administra otra persona, `dcctl` necesita verbos que antes no necesitaba: **`list`,
`patch` y `delete`** sobre `instances.core.devicechain.io` junto a `get`, `create` y `update`, además
de **`list` sobre secrets** en `dc-system`. Cada bootstrap y cada actualización preguntan ahora al
clúster qué instancias aloja ya y qué han reclamado, y esa pregunta es un list. Una cuenta que solo
tenga el conjunto documentado hasta ahora se rechaza a mitad de un bootstrap.

#### Dos cosas que se movieron y una que desapareció

- **El inicio de sesión único de Grafana a través de DeviceChain desapareció.** A Grafana se llega
  haciendo port-forward de su Service —no hay ruta de ingress— y se entra con una credencial de
  administración por clúster que está en el Secret `dc-grafana-admin`. Consulte
  [Observabilidad](./observability.md).
- **El estado local de `dcctl` se movió.** Los registros por instancia están bajo
  `~/.devicechain/instances/<instance>/`, y un directorio nuevo por clúster,
  `~/.devicechain/clusters/<cluster-uid>/`, guarda el estado de infraestructura del propio clúster.
  Ese directorio se indexa por la identidad del clúster y no por su nombre, y es la **única** copia de
  ese estado: ningún respaldo lo contiene. Un clúster instalado solo puede reinstalarse desde la
  máquina que lo guarda. Consulte [Instalar el clúster](./bootstrap.md#install).
- **Un bootstrap pregunta al almacén relacional qué contiene ya** antes de acuñar una clave raíz. Una
  base de datos que esté ahí con el nombre de la instancia solo puede haber sobrevivido al clúster
  que la creó, así que el bootstrap se detiene en lugar de acuñar una clave que no podría descifrar
  las filas que ya están ahí.

### Instancias creadas por la v0.16.0 y anteriores {#pre-declaration-recreate}

Una instancia arrancada por la **`v0.16.0`, o por cualquier versión anterior, no se puede
actualizar a la versión posterior a la `v0.16.0`**. `dcctl upgrade` se niega en lugar de
intentarlo.

`dcctl bootstrap` registra ahora una **declaración**: un objeto con ámbito de clúster que dice
lo que la instancia *es* — su perfil, su topología, cómo está expuesta y qué áreas funcionales
ejecuta. `dcctl upgrade` lee esa declaración para saber qué desplegar, que es lo que le
permite mover una versión sin tener que indicarle de nuevo la forma de la instancia. Las versiones hasta la `v0.16.0` incluida no escribieron ese registro, así que no
hay nada que la actualización pueda leer.

Y lo dice, en lugar de tratar su instancia como un nombre que no existe:

```
instance "devicechain" IS in this cluster — named by the DeviceChain Helm releases in this
cluster — and it carries no declaration, so it was built by a release older than the one
that began recording them.
```

No hay capa de compatibilidad, y antes de la `v1.0.0` no la habrá. Aquello con lo que se
configuró la instancia antigua nunca se escribió en una forma que esta versión pueda leer, de
modo que una declaración inventada a posteriori sería una conjetura aplicada sobre una
instancia en funcionamiento.

**Para migrar a esta versión, recree la instancia, y también el clúster que la aloja.** Las
versiones que construyeron estas instancias no tenían `dcctl install`: instalaban los
requisitos previos compartidos del clúster como parte de la instancia, y no registraban
ninguna instalación. Por eso `dcctl destroy` se niega a ejecutar `tofu destroy` sobre el
estado que escribieron esas versiones y necesita `--without-state`, lo que aun así deja atrás esos requisitos previos;
`dcctl bootstrap` rechaza un clúster sin registro de instalación y `dcctl install` chocaría
con lo que dejó la versión anterior. Parta de un clúster nuevo entre medias:

```bash
# Exporte antes lo que necesite: esto descarta las bases de datos.
dcctl destroy local devicechain --without-state   # elimina la instancia; el clúster antiguo va después
kind delete cluster --name devicechain     # y el clúster que preparó la versión anterior
dcctl install local                        # prepara un clúster nuevo
dcctl bootstrap local devicechain
```

Para un clúster al que se llega con `--kube-context`, que `dcctl` nunca elimina, pase también
ese `--kube-context` en la línea de `destroy` —el motivo está en la nota siguiente—; después
borre y recree el clúster con lo que lo creó, y pase el mismo `--kube-context` a `install` y
`bootstrap`. `dcctl upgrade` imprime esta misma receta cuando se niega.

:::note El `dcctl` de esta versión no lee el estado local de la versión anterior
`dcctl` guarda ahora lo que sabe de una instancia en `~/.devicechain/instances/<instance>/`.
Las versiones hasta la `v0.16.0` incluida lo guardaban un nivel más arriba, en
`~/.devicechain/<instance>/`, y **el nuevo `dcctl` no lee, no lista ni elimina ese
directorio**: deliberadamente no hay migración, porque `dcctl` no puede distinguir un
directorio de instancia antiguo de uno que usted haya creado por su cuenta. De ello se siguen
tres cosas para la receta anterior:

- **`dcctl instances list` no muestra ninguna de sus instancias antiguas.** En una máquina que
  solo tenga instancias construidas por la `v0.16.0` o una versión anterior imprime `No
  DeviceChain instances on this machine (nothing under ~/.devicechain/instances).` No se ha
  perdido nada; las instancias siguen en sus clústeres, y `helm list -A` sigue mostrando sus
  releases.
- **`destroy` adivina el clúster.** El registro de clúster que escribió la versión anterior
  está en el directorio que el nuevo `dcctl` no lee, así que `destroy` imprime
  `No record of which cluster instance "<instance>" lives in — GUESSING cluster … from its
  name` y lo deriva del nombre de la instancia. Esa conjetura es correcta para una instancia
  local con el nombre que usa la receta, y **errónea para una arrancada con `--kube-context`**,
  que es el motivo por el que `--kube-context` va también en la línea de `destroy`.
  `--without-state` sigue siendo necesario: el estado de infraestructura también está en el
  directorio en el que `dcctl` ya no mira.
- **El directorio antiguo se queda en disco.** `destroy` elimina
  `~/.devicechain/instances/<instance>/` —que para una instancia así no contiene nada— y deja
  `~/.devicechain/<instance>/` donde estaba, con su `infra/terraform.tfstate` (la contraseña
  del superusuario de la base de datos y la clave privada TLS del broker, en claro) y su
  `broker-credentials.json`. Una vez que la instancia haya desaparecido, elimínelo usted mismo:

  ```bash
  rm -rf ~/.devicechain/<instance>
  ```

  **No** toque `~/.devicechain/escrow/`. El artefacto de depósito de la clave raíz siempre ha
  vivido ahí, fuera de cualquier directorio de instancia, y sigue abriendo las copias de
  seguridad de la base de datos de esa instancia; consulte
  [Recuperación ante desastres](./disaster-recovery.md#after-destroy).
:::

:::caution Exporte primero: recrear descarta sus datos
La [protección de destrucción](#data-durability) protege las bases de datos frente a una
operación corriente de `helm`, no frente a un `dcctl destroy` deliberado. Si la instancia
contiene telemetría, definiciones de dispositivos o paneles que le importan, vuélquelos antes
de empezar.
:::

:::tip La actualización rechazada no cambia nada
`dcctl upgrade` lee la instancia antes de escribir nada, así que el rechazo ocurre antes del
primer cambio: la versión de Helm se queda en la revisión en la que estaba, el operador sigue
ejecutando la imagen que ejecutaba y todas las filas siguen donde estaban. Ejecutarlo para ver
qué dice no cuesta nada. Ambas mitades —el rechazo y que la instancia quede intacta después—
se comprueban contra un clúster real en cada versión.
:::

Una vez que esté en una versión que registra una declaración, las actualizaciones in situ
corrientes se reanudan. `dcctl instances list` muestra qué hay declarado y en qué clúster.

### Próxima versión {#next-upgrade}

La actualización cierra la sesión de todos los usuarios una vez, a partir de entonces restablecer
la contraseña de un usuario, desactivarlo o eliminarlo termina sus sesiones, y la clave raíz de la
instancia pasa a ser obligatoria en todos los perfiles. Eso son las tres primeras secciones de
abajo. Las tres siguientes solo importan si vigila usted mismo las métricas de mensajes no
entregados, depende de respuestas a comandos que no se pudieron registrar o abre conexiones
WebSocket de GraphQL desde su propio código. Además, en toda instancia creada antes de
esta versión hay que revisar la contraseña del superusuario (vea «El superusuario ya no tiene
contraseña por defecto» más abajo). La sección sobre el inicio de sesión importa si llama a la API de GraphQL desde su
propio código o scripts, o si dimensiona usted mismo el volumen de JetStream: el inicio de sesión
ahora tiene límite de frecuencia, y una solicitud de GraphQL tiene un límite de campos raíz y de
comprobaciones de contraseña. Si enruta o silencia alertas por su nombre, lea «Un consumidor que se
queda atrás de un flujo lleno ahora genera una alerta»: `EventProcessingStreamNearFull` cambia de
nombre. Si sus valores de outbound-connectors fijan `dispatchBacklog`, elimínelo antes de
actualizar: el servicio ahora se niega a arrancar con él (vea «El servicio de conectores ya no
acepta dispatchBacklog» más abajo). Si filtra los mensajes no entregados por tipo o por motivo, o
tiene alertas sobre su stream, lea «Los mensajes abandonados en su último intento ahora se
registran» más abajo: añade tres tipos, un motivo, un stream y dos alertas. Si ejecuta una reserva
en caliente de `event-processing`, lea «Con una reserva en caliente, solo la réplica que detecta
despacha acciones».

#### Todos los usuarios cierran sesión una vez, y restablecer una contraseña ahora termina sesiones

Cada usuario tiene ahora un **valor de sesión**, y todo token que se puede canjear por otro nuevo lo
lleva: los tokens de actualización, el token de inicio de sesión que la consola guarda antes de
elegir un inquilino y los códigos de autorización de OAuth. Restablecer la contraseña de un usuario,
desactivarlo o eliminarlo cambia ese valor, y un token que lleva el anterior se rechaza. Antes de
esta versión, restablecer una contraseña dejaba funcionando todos los tokens de actualización ya
emitidos, de modo que uno robado seguía renovándose mientras se usara.

Lo que verá en la actualización:

- **Todas las sesiones de la consola, del panel y de los SDK se cierran una vez.** Los tokens
  emitidos antes de la actualización no llevan valor de sesión, así que no se pueden renovar. Una
  sesión termina en su siguiente renovación, dentro de los 15 minutos posteriores a la actualización
  salvo que haya cambiado la duración del token de acceso, y el usuario vuelve a iniciar sesión. Una
  consola que en ese momento esté en el selector de inquilinos o en las páginas de administración
  puede mostrar un error al elegir un inquilino, en lugar de volver a la página de inicio de sesión.
  Cerrar sesión y volver a entrar lo resuelve.
- **Los clientes OAuth, incluidos los asistentes de IA conectados por MCP, deben autorizarse de
  nuevo.** Sus tokens de actualización se rechazan con `invalid_grant`.
- Un usuario creado por un servicio que aún no se había reemplazado mientras avanzaba la
  actualización no tiene valor de sesión y no puede iniciar sesión. El inicio de sesión falla como
  lo haría con una contraseña incorrecta, y el registro de user-management nombra al usuario y la
  causa. Que un administrador restablezca la contraseña de ese usuario lo corrige.

Lo que cambia a partir de entonces:

- **Restablecer la contraseña de un usuario, desactivarlo o eliminarlo termina todas sus
  sesiones.** Sus tokens de actualización dejan de funcionar en el siguiente uso, y un token de
  inicio de sesión o un código de autorización emitido antes del cambio ya no se puede canjear por
  una sesión nueva. Reactivar un usuario desactivado no recupera las sesiones anteriores.
- **Los tokens ya emitidos para uso directo no se revocan.** Un token de acceso, y el token de inicio
  de sesión en la API de administración, siguen funcionando hasta que caducan: 15 minutos, salvo que
  haya cambiado la duración del token de acceso. Eso incluye el token de inicio de sesión de un
  administrador.
- **Eliminar un usuario y crear otro con el mismo correo empieza de cero.** El usuario nuevo no
  hereda ninguna sesión que conservara el anterior.
- Cambiar los roles o las membresías de un usuario no cierra su sesión. Surte efecto en su siguiente
  renovación, como antes.

#### Todos vuelven a iniciar sesión una vez

La clave que firma todos los tokens de acceso y de actualización se guardaba sin cifrar en la base
de datos de user-management. Cualquiera que pudiera leer esa base de datos, una copia de seguridad
de ella o su archivo del registro de escritura anticipada podía firmar tokens que todos los
servicios aceptan. Ahora se sella bajo la clave raíz de la instancia, como cualquier otra
credencial almacenada, y solo la clave en uso tiene mitad privada. Cuando una clave se retira por
rotación, su mitad privada se borra y solo se conserva la pública, de modo que los tokens que firmó
siguen verificándose hasta que caducan.

La actualización **borra todas las claves de firma que tenía la instancia**, en lugar de sellarlas,
porque cada una ya ha estado sin cifrar en todas las copias de seguridad tomadas hasta ahora.
user-management genera una clave nueva al arrancar. Como consecuencia:

- **Todos los usuarios vuelven a iniciar sesión.** Los tokens de acceso y de actualización emitidos
  antes de la actualización dejan de validar.
- **Los clientes OAuth, incluidos los clientes MCP, tienen que volver a autorizarse.** Sus tokens de
  actualización también dejan de funcionar.
- **Las sesiones en una aplicación de paneles embebida terminan**, y sus usuarios vuelven a iniciar
  sesión.

Esto surte efecto cuando termina el despliegue, no en el momento en que empieza la actualización.
Hasta que se detiene el último pod antiguo de user-management, sigue firmando tokens con la clave
antigua y publicándola para que los demás servicios verifiquen con ella. `helm upgrade` y
`dcctl upgrade` sustituyen todos los pods, así que la clave antigua deja de ser de confianza cuando
terminan.

Las copias de seguridad y el registro de escritura anticipada archivado **antes** de la
actualización siguen conteniendo las claves antiguas. Esas claves ya no son de confianza en ninguna
parte una vez terminado el despliegue, pero conviene seguir protegiendo esos archivos como las
credenciales que fueron.

#### La clave raíz de la instancia es ahora obligatoria en todos los perfiles

Antes, la clave raíz solo la necesitaban los perfiles que almacenan credenciales de integración, así
que el chart permitía renderizar los perfiles `telemetry` e `ingest-only` sin ella. Ahora toda
instancia sella su clave de firma con la clave raíz, así que el chart **falla el renderizado** en
cualquier perfil cuando no hay clave. Una instancia construida con `dcctl bootstrap` ya tiene una y
no necesita nada. Una instalación `telemetry` o `ingest-only` hecha solo con el chart necesita una
clave antes de poder actualizarse. Genérela con `openssl rand -base64 32`, pásela como
`instance.config.infrastructure.secrets.rootKey` y consérvela: hay que pasar el mismo valor en
cada actualización posterior.

La clave raíz decide ahora también si alguien puede iniciar sesión. Con una clave equivocada o
perdida, user-management se niega a arrancar, igual que todo servicio que almacena credenciales de
integración. Los demás servicios se quedan entonces sin estar listos, porque no pueden obtener las
claves que necesitan para validar un token. Cae toda la API, no solo las integraciones. Consulte
[Recuperación ante desastres](./disaster-recovery.md#root-key) para saber qué hacer.

Borrar una credencial almacenada ahora también la elimina por completo de la base de datos. Antes,
la fila sellada se quedaba en la tabla, marcada como borrada. Las credenciales borradas **antes** de
esta versión se quedan así: no se pueden usar, pero sus filas selladas siguen en la tabla y en las
copias de seguridad.

#### Los contadores de pérdidas son una métrica por servicio

Un servicio que abandona un mensaje y luego no puede registrarlo como mensaje no entregado cuenta
ahora esa pérdida en **`dead_letter_lost_total`** bajo su propio subsistema, con el mismo nombre en
todos los servicios. Estas cinco series desaparecen:

- `devicechain_eventprocessing_react_events_dead_letter_lost_total`
- `devicechain_notificationmanagement_notifications_dead_letter_lost_total`
- `devicechain_commanddelivery_command_delivery_responses_dead_letter_lost_total`
- `devicechain_devicemanagement_raise_alarm_dead_letter_lost_total`
- `devicechain_devicemanagement_alarm_event_dead_letter_lost_total`

Estas cinco las reemplazan:

- `devicechain_eventprocessing_dead_letter_lost_total`
- `devicechain_notificationmanagement_dead_letter_lost_total`
- `devicechain_commanddelivery_dead_letter_lost_total`
- `devicechain_devicemanagement_dead_letter_lost_total`, un único contador para las dos rutas de
  device-management
- `devicechain_outboundconnectors_dead_letter_lost_total`, que es **nueva**. Un envío de conector
  saliente cuya copia no se pudo escribir en su entrega final solo se contaba antes como
  `connector_dispatch_total{outcome="dead_write_failed"}`, que ninguna alerta leía. Se sigue
  contando ahí, y ahora también aquí.

La alerta `DeadLetterWriteLost` las selecciona por nombre en lugar de enumerarlas, así que ahora
cubre también los conectores salientes. Si sus propios paneles o reglas nombran las series
antiguas, cámbielos. El selector `{__name__=~"devicechain_[a-z0-9]+_dead_letter_lost_total"}`
cubre todos los servicios. Use `[a-z0-9]+` y no `.+`: mientras avanza la actualización, los pods
que aún no se han reemplazado siguen exportando los dos nombres antiguos de device-management, y
`.+` coincide con ambos.

El contador cuenta también una carta que el servicio **rechazó** por estar mal formada, lo que es
un defecto de ese servicio y no un problema del bróker. La línea de error `LOST` del pod indica cuál
de los dos casos ocurrió.

#### Las respuestas a comandos que no se pudieron registrar vuelven a guardarse

En `v0.16.0` y `v0.17.0`, command-delivery no podía escribir ni un solo mensaje no entregado. Cada
uno que intentaba se rechazaba antes de escribirse, se contaba como perdido, y la respuesta del
dispositivo desaparecía. Por eso `DeadLetterWriteLost` podía dispararse con el bróker en buen
estado. Ese caso está corregido, y cambia lo que ocurre con los comandos afectados:

- Una respuesta que no se pudo registrar tras todos los intentos aparece como mensaje no entregado,
  y su comando pasa ahora a `FAILED`, con un error que dice que el dispositivo respondió y la
  respuesta se perdió. Antes, ese comando seguía en curso hasta que otra cosa lo resolvía.
- Una respuesta que no nombraba ningún despacho, o nombraba uno que su comando ya había dejado
  atrás, aparece como mensaje no entregado y no resuelve nada. El comando queda como estaba.

#### Los WebSocket de GraphQL solo llevan suscripciones y se cierran cuando caduca su token

El WebSocket que un servicio acepta en su endpoint GraphQL cambia de tres maneras:

- **Solo ejecuta suscripciones.** Una consulta o mutación enviada por él se rechaza con un error que
  indica usar HTTP, y no se ejecuta nada. Antes se ejecutaban ambas, con el token que la conexión
  presentó al abrirse. Envíe las consultas y mutaciones como peticiones HTTP.
- **Se cierra con el código `4401` cuando caduca el token de acceso con el que se autenticó.** Antes,
  una conexión seguía abierta, y sus suscripciones seguían emitiendo, mientras el cliente respondiera
  a los pings. Para mantener un flujo, abra una conexión nueva con un token nuevo y vuelva a
  suscribirse.
  - `@devicechain/client` lo hace por usted. Cuando una conexión que había establecido se cierra con
    `4401`, se reconecta una vez con un token recién obtenido, vuelve a suscribirse e informa de la
    reconexión a su receptor como `connected(true)`.
  - El SDK de .NET lanza el cierre desde `SubscribeAsync` como una excepción que indica el código.
    Vuelva a suscribirse para continuar; la conexión nueva toma un token nuevo de la sesión.
  - El visor de paneles independiente no renueva su token, así que sus widgets en vivo se detienen
    cuando el token caduca. Vuelva a iniciar sesión.
- **Un servicio sin suscripciones ya no acepta ningún WebSocket.** La petición de upgrade se rechaza
  con HTTP 400. Antes, la conexión se abría y cada operación enviada por ella fallaba.

#### El superusuario ya no tiene contraseña por defecto

Las versiones anteriores creaban el superusuario de cada instancia, `superuser@devicechain.local`,
con la misma contraseña publicada, `devicechain`. Ahora cada instancia nueva recibe una contraseña
generada para ella: `dcctl bootstrap` la imprime una sola vez y la guarda en el Secret
`dci-<instance>-superuser` del namespace de la instancia. Ya no hay valor por defecto. Si
user-management arranca con la tabla de identidades vacía y sin contraseña en ese Secret, se niega a
crear el superusuario.

**La actualización no cambia la contraseña de un superusuario existente.** No puede saber si usted
la cambió, así que no la toca, e imprime un aviso al terminar para cualquier instancia que no tenga
contraseña generada. Si nunca cambió la contraseña en una instancia así, sigue siendo `devicechain`.
Inicie sesión y cámbiela, o vuelva a crear la instancia para que se le genere una.

`dcctl sim` y las herramientas de simulacro tampoco dan ya por supuesta la contraseña antigua.
`dcctl sim` lee la generada del Secret de la instancia, y acepta `--admin-password` o
`$DC_ADMIN_PASSWORD` para una instancia que no lo tiene.

#### El inicio de sesión tiene límite de frecuencia, y las solicitudes de GraphQL llevan menos campos

No hay que hacer nada en la actualización salvo que su propio código o sus scripts hagan alguna de
las cosas siguientes. La consola, la aplicación de paneles, los SDK y `dcctl` ya respetan todos los
límites.

- **Una operación puede seleccionar como mucho 5 campos de primer nivel en una mutación y 20 en una
  consulta.** Los alias cuentan, y también los campos a los que se llega a través de fragmentos. Una
  solicitud que supera el límite no ejecuta nada y recibe un error con el código
  `TOO_MANY_ROOT_FIELDS`. Divida esa solicitud, o aumente `DC_GRAPHQL_MAX_MUTATION_ROOT_FIELDS` /
  `DC_GRAPHQL_MAX_QUERY_ROOT_FIELDS` para ese servicio.
- **En una solicitud se puede comprobar una contraseña.** Otro `login` en la misma solicitud no se
  evalúa y recibe el código `TOO_MANY_CREDENTIAL_CHECKS`. Inicie sesión una vez por solicitud.
- **Los inicios de sesión fallidos repetidos sobre una dirección de correo se ralentizan.** Tras cinco
  fallos seguidos, el siguiente intento sobre esa dirección espera 1 segundo, duplicándose hasta 5
  minutos. Un intento hecho durante la espera recibe el código `THROTTLED` con `retryAfterSeconds`,
  no «credenciales no válidas». Un inicio de sesión que el servidor no puede contar recibe
  `UNAVAILABLE`. Si su código inicia sesión, trate ambos como errores propios y no como una
  contraseña incorrecta. Los secretos de cliente OAuth no se ralentizan.
- **La reserva de JetStream crece en 128 MiB** (16 MiB en el preset compacto), por el bucket que
  guarda las cuentas de inicio de sesión. En el preset compacto los buckets de caché bajan de 8 a 4
  MiB cada uno para hacer sitio. Si dimensionó usted mismo el volumen de JetStream cerca de la reserva,
  compruebe que tiene espacio.
- **Una alerta nueva, `CredentialAttemptStoreFull`,** se dispara si ese bucket se llena. El inicio de
  sesión sigue funcionando cuando está lleno, pero los fallos repetidos ya no se ralentizan. [Espera
  entre intentos de inicio de sesión](../reference/graphql-api.md#sign-in-backoff) explica qué hacer.

[Límites de las solicitudes](../reference/graphql-api.md#request-limits) y [espera entre intentos de
inicio de sesión](../reference/graphql-api.md#sign-in-backoff) tienen los detalles.

#### Un consumidor que se queda atrás de un flujo lleno ahora genera una alerta

Un flujo (stream) de JetStream lleno descarta sus mensajes más antiguos. Antes de esta versión, un
consumidor que aún no había leído esos mensajes los perdía sin que ninguna métrica ni alerta lo
indicara. Ahora cada servicio lo mide para cada consumidor duradero que lee y exporta dos series
nuevas:

- `devicechain_<area>_jetstream_consumer_unread_skipped_total{stream, durable}`: mensajes que el
  consumidor pasó por encima sin leerlos.
- `devicechain_<area>_jetstream_consumer_unread_gap_messages{stream, durable}`: mensajes descartados
  por delante de un consumidor que ha dejado de leer (uno que no ha recibido ningún mensaje desde la
  muestra anterior; un consumidor que lee pero va atrasado vale 0 aquí).

Dos alertas críticas nuevas las leen: `JetStreamDurableLostUnread` y
`JetStreamDurableStalledBehindStream`. Eliminar un tenant puede disparar la primera: la eliminación
borra los mensajes de ese tenant, incluidos los que un consumidor aún no había alcanzado. Consulte
[Mensajes que un consumidor nunca leyó](./observability.md#unread-loss) para saber qué significa cada
alerta y qué hacer.

La advertencia de flujo casi lleno **cambia de nombre**, de `EventProcessingStreamNearFull` a
**`JetStreamStreamNearFull`**, y ahora cubre los flujos de todos los servicios, no solo los de
event-processing. El umbral (80% del límite de bytes durante 10 minutos) no cambia. Si una ruta o un
silencio de Alertmanager nombra la alerta antigua, cámbielo al nombre nuevo.

#### El servicio de conectores ya no acepta dispatchBacklog

Si sus valores fijan `dispatchBacklog` en `functionalAreas.outbound-connectors.config`, elimínelo
**antes** de actualizar. El servicio se niega a arrancar con él, y el error nombra la clave.

El ajuste dimensionaba un búfer entre el lector del servicio y sus trabajadores de envío, y ese búfer
ya no existe. El lector ahora solo obtiene tantos envíos como trabajadores libres hay para empezarlos
(`maxConcurrentSends`), así que un envío ya no espera dentro del proceso mientras corre la ventana de
confirmación del broker. El servicio de notificaciones lee las alarmas de la misma forma, una por
despachador.

Esto corrige un duplicado. Antes, una ráfaga de alarmas o de envíos de conectores en cola detrás de
un canal lento podía quedarse en el servicio más tiempo que esa ventana. El broker entonces volvía a
entregar los mismos mensajes mientras las primeras copias seguían esperando, y se enviaban ambas
copias: una segunda notificación por una sola alarma, o una segunda llamada al mismo webhook. Además,
ahora cada envío se corta con margen antes de que la ventana se cierre.

Una alerta nueva, `ReaderHeldMessagePastAckWait`, se dispara si alguno de los dos servicios aún
retiene un mensaje más allá de la ventana. [Mensajes retenidos más allá de su ventana de
confirmación](./observability.md#held-past-ack-wait) explica qué significa cada caso.

#### Los mensajes abandonados en su último intento ahora se registran

Hasta ahora, solo llegaba a la lista de mensajes no entregados un mensaje que un servicio abandonaba
después de procesarlo. Un mensaje cuyos cinco intentos de entrega se agotaban **sin** ningún
resultado (un pod detenido a mitad del procesamiento, o un manejador que se pasó de su ventana de
confirmación) no dejaba rastro, porque nada llegaba al código que escribe el registro. El broker sí
lo advierte, y ahora cada servicio también registra esos casos a partir del propio aviso del broker.

Qué cambia para usted:

- **Tres tipos nuevos**, `event`, `command` y `control-fact`, para los mensajes de los streams de
  eventos de dispositivos, de comandos y del plano de control. El tipo de un registro queda fijado
  ahora por el stream por el que llegó el mensaje. `dcctl dead-letters list --kind` los ofrece todos.
- **Un motivo nuevo, `no-outcome`.** Nunca liquida un comando: el último intento pudo haber hecho su
  trabajo y perder solo su acuse de recibo. En los streams de mucho volumen (eventos de
  dispositivos, comandos y acciones de detección) el registro no lleva copia del mensaje; su detalle
  indica dónde está el original hasta que el stream lo descarte por antigüedad. Lo mismo vale para
  un mensaje demasiado grande para copiarse, y el registro de una petición de conector apunta al
  stream propio de mensajes no entregados del servicio de conectores, que guarda la petición
  completa.
- **Un stream nuevo, `max-deliveries`,** que crea cada servicio que lee de un stream. Reserva 8 MiB
  con el dimensionamiento por defecto y cabe en el volumen de JetStream existente; no hace falta
  redimensionar nada. Está vacío en régimen normal, y una alerta nueva, `MaxDeliveryRecordsWaiting`,
  se dispara si hay avisos esperando sin registrar ([Mensajes que agotaron sus intentos de
  entrega](./observability.md#max-delivery-records)).
- **Los abandonos del motor de detección se cuentan, no se registran.** `event-processing` lee
  `resolved-events` desde su propio punto de control guardado y vuelve a leer el stream tras un
  reinicio, así que un evento que agotó ahí sus intentos no se ha perdido. Cuando el motor no puede
  guardar su punto de control durante más tiempo del que el broker sigue reentregando (normalmente,
  una caída de la base de datos), todos los eventos de ese intervalo agotan sus intentos, y un
  registro por cada uno informaría de pérdidas que no ocurrieron. En su lugar se cuentan con
  `outcome="replay-covered"`, y una alerta nueva de nivel warning,
  `ReplayCoveredDeliveriesExhausted`, informa de ellos. Los demás servicios que leen
  `resolved-events` siguen registrando los suyos.
- **El stream de mensajes no entregados gana una ventana de duplicados de 30 minutos,** aplicada en
  el sitio durante la actualización, y también el stream propio de mensajes no entregados del
  servicio de conectores. Es lo que hace que un abandono registrado a la vez por un servicio y por el
  aviso del broker quede una sola vez.
- **`DeadLetterWriteLost` tiene una tercera causa:** un mensaje de esa cola en el que el almacén de
  mensajes no entregados o la reconciliación de comandos agotó sus intentos, y que ahora puede caducar en
  el stream sin haberse almacenado (su último intento pudo almacenarlo y perder solo el acuse de
  recibo).

Durante la actualización escalonada, un abandono puede registrarse dos veces: una por un pod de la
versión anterior y otra a partir del aviso del broker. Es el mismo fallo; no se perdió nada.

#### Con una reserva en caliente, solo la réplica que detecta despacha acciones

En un despliegue de `event-processing` con una reserva en caliente, la reserva se quedaba con una
parte de las acciones de detección (comandos, alarmas y llamadas a conectores) y cargaba las llamadas
a conectores contra su propia copia del techo de salida de cada inquilino, de modo que un inquilino
podía llegar hasta el doble de ese techo. Ahora las acciones solo las despacha la réplica que tiene la
partición de detección. Cuando la partición cambia de réplica, ambas pueden despachar durante unos
cinco segundos como máximo, y una llamada a un conector hecha dos veces en ese intervalo llega dos
veces a su destino.

Con una réplica (el valor por defecto) nada cambia, salvo cuando el pod se detiene sin un apagado
ordenado (una caída o una terminación por falta de memoria). Las acciones pendientes de despachar se
reanudan entonces cuando el reemplazo toma la partición, hasta unos 35 segundos después, en lugar de
en cuanto arranca el reemplazo. La detección se reanuda tras una espera de traspaso adicional y la
reproducción, igual que antes de esta versión.

Los techos de tasa de `event-sources`, `outbound-connectors` y `ai-inference` los aplica cada réplica
por separado. Esto ahora está documentado en [Gobernanza](../concepts/governance.md#per-replica), y
importa si ejecuta más de una réplica de esos servicios.

### La transición única a la ingesta duradera

La versión que introduce la **ingesta MQTT duradera** cambia la forma en que `event-sources` recibe
la telemetría de dispositivos: en lugar de suscribirse al broker como cliente MQTT, consume un
flujo de captura duradero que el broker escribe antes de confirmar la recepción al dispositivo. Esto
es lo que evita que se pierda telemetría cuando `event-sources` está caído.

Cruzar esa versión una vez es una actualización en sitio corriente, pero espere una **breve ventana de
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
al azar. Vuelca primero ambas bases de datos y vuelve a ejecutar `dcctl install` con
`--allow-legacy-db-removal` para la base de datos relacional, y el arranque con él para el
almacén de eventos, lo que afirma que te has ocupado de los datos y no verifica nada.
Para una instancia local, recrearla es más simple y descarta los datos de forma
deliberada: destrúyela, recrea el clúster y después instala y arranca, como se describe en
[Instancias creadas por la v0.16.0 y anteriores](#pre-declaration-recreate).
:::

Esto es durabilidad de los volúmenes en ejecución; no es un sustituto de las copias de seguridad programadas y la
recuperación a un punto en el tiempo, que se aprovisionan con la infraestructura de producción. Consulte
[Despliegue y operador](./kubernetes-operator.md) para saber cómo se separan las capas de infraestructura y aplicación.
