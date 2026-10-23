---
sidebar_position: 4
title: Versiones y actualizaciones
---

# Versiones y actualizaciones

DeviceChain se distribuye como un conjunto de imágenes de contenedor precompiladas y versionadas,
más un chart de Helm. **No** necesita compilar nada para ejecutarlo: descargue una versión publicada,
instale el chart y actualice in situ con una actualización progresiva.

:::warning Hay versiones a las que no se puede actualizar
Hasta ahora, cuatro puntos de la historia pueden exigir recrear la instancia en lugar de actualizarla:

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
- La **`v0.19.0`, para una instancia cuyo almacén de eventos guarda más de 4 000 000 de filas aún
  sin comprimir**, o una tabla de más de 500 fragmentos cuyos eventos más antiguos no se pueden
  eliminar: su primer arranque reconstruye las claves del almacén de eventos sobre esas filas y se
  niega por encima de esos límites. Consulte
  [Comprueba el número de filas](#v0190-row-count).

Si está en cualquiera de ellos, lea la sección correspondiente más abajo antes de hacer nada más.
:::

:::caution Actualice pasando por la v0.23.0
Las versiones posteriores a `v0.23.0` escriben los eventos en una forma que `v0.22.0` y anteriores
no pueden leer. Si viene de `v0.22.0` o anterior, actualice primero cada instancia a `v0.23.0`, y
solo después a una versión posterior. Consulte
[El formato de los eventos en el bróker](#event-wire-format).
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
instancia. Consulte [Actualizaciones progresivas](#rolling-upgrades) para el
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
- **negarse a una actualización in situ por encima de un límite de tamaño de los datos**: la
  `v0.19.0` rechaza un almacén de eventos con más de 4 000 000 de filas aún sin comprimir, o una
  tabla de más de 500 fragmentos, y lo indica al principio de sus notas

La propiedad de "actualizar in situ con una actualización progresiva" descrita arriba describe la *mecánica* de una
actualización progresiva. No es una promesa de que sus llamadas a la API existentes conserven el mismo significado
a través de un incremento de versión previo a 1.0.

A partir de la v1.0.0, esta sección se reemplaza por una promesa de compatibilidad de versionado
semántico: cambios disruptivos solo en una versión mayor. Consulte
[Política de versionado y actualización](./versioning-policy.md).

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

<a id="zero-downtime-upgrades"></a>

## Actualizaciones progresivas {#rolling-upgrades}

Actualizar consiste en **dos comandos** —uno para el clúster y luego uno por cada instancia
que haya en él—, y el chart y los servicios están diseñados para avanzar pod por pod, de modo que un
área con más de una réplica sigue sirviendo mientras se actualiza. Hay cinco excepciones, todas documentadas más abajo: la transición a la
ingesta duradera, que sigue siendo una actualización corriente pero tiene un efecto secundario
visible; la **`v0.9.0`, la `v0.10.0` y cualquier instancia creada por la `v0.16.0` o una
versión anterior, a las que no se puede actualizar en absoluto**; y la **`v0.19.0` para una
instancia cuyo almacén de eventos guarda más de 4 000 000 de filas aún sin comprimir**, o una
tabla de más de 500 fragmentos cuyos eventos más antiguos no se pueden eliminar
([Comprueba el número de filas](#v0190-row-count)). Consulte las notas de la
versión a la que va a migrar antes de ejecutarlo:

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

:::note Qué se ha medido
La actualización se ha ensayado a mano en un clúster local: una instancia instalada con `--ha` se
actualizó in situ con tráfico MQTT en marcha, su broker se reinició dos veces, los dispositivos se
reconectaron tras cada reinicio y todos los mensajes que el broker había confirmado seguían almacenados
después. Ese ensayo es manual. La comprobación automática de actualización que condiciona cada versión
se ejecuta sin tráfico.

No se ha medido una actualización bajo carga sostenida de producción en un clúster de tamaño de producción,
así que DeviceChain no promete una actualización sin interrupción. Prevea una breve interrupción donde se
reemplace un pod: un área con una sola réplica y cada servidor del broker. Sin `--ha`, el único servidor del
broker se reinicia y el broker no está disponible mientras tanto. Un dispositivo que envía mientras está
desconectado debe reintentar.
:::

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

1. **la infraestructura de la instancia**: su bróker de mensajería (NATS) y su almacén de
   eventos, aplicados desde la configuración de OpenTofu que trae esta versión, de modo que un
   cambio en cualquiera de los dos llega a las instancias existentes y no solo a las nuevas.
   Consulte [Qué aplica una actualización a la infraestructura](#upgrade-infrastructure);
2. **el documento de configuración** del que cada servicio lee sus credenciales y sus
   endpoints, recompuesto a partir del chart de esta versión y escrito por `dcctl`, que es su
   dueño;
3. **la versión desplegada de Helm** que ejecuta los servicios, que los hace avanzar a las
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
con el número anterior. Para añadir un área funcional a una instancia en marcha, usa
[`dcctl enable-area`](./bootstrap.md#enable-area), que la registra en esa declaración.

Una cosa queda además deliberadamente fuera de este comando: no toca los datos de las bases de
datos más allá de dejar que los servicios ejecuten sus propias migraciones, y nunca cambia el
tamaño de un volumen.
:::

### El ajuste del operador se conserva {#upgrade-tuning}

Una actualización no cambia la forma de una instancia, pero sí conserva el ajuste que le diste. Un archivo de valores pasado a `dcctl bootstrap --values` o a `dcctl upgrade --values` se registra en la instancia y lo aplica de nuevo cada actualización posterior; `--reset-values` lo elimina. Consulta [Ajustar una instancia](./tuning-an-instance.md).

### Qué aplica una actualización a la infraestructura {#upgrade-infrastructure}

Antes de mover los servicios, `dcctl upgrade` aplica la configuración de OpenTofu de la
instancia, la misma que aplicó `dcctl bootstrap` al crearla. Esa configuración contiene los
servidores NATS de la instancia y su almacén de eventos: la imagen, los ajustes y la ubicación
de la base de datos, y su configuración de respaldo y archivado. La actualización planifica
primero y muestra lo que va a cambiar; con `--dry-run` muestra el plan y se detiene. Necesita
`tofu` en el `PATH`, como un bootstrap, y preparar la configuración consulta el registro de
proveedores (o su réplica) por sus proveedores; una ejecución en seco también lo hace, en el
directorio de trabajo local en el que planifica.

Conserva aquello con lo que funciona la instancia. Conserva todas las credenciales, como se
indica arriba. El volumen de JetStream y el del almacén de eventos conservan su tamaño. Si esta
versión crea otro tamaño para las instancias nuevas, la actualización lo dice, y
[Volumen del almacén de eventos](./bootstrap.md#event-store-volume) muestra cómo ampliar uno
usted mismo. El almacén de eventos conserva la ruta bajo la que archiva, y un almacén restaurado
conserva el archivo desde el que se restauró.

Se niega, sin cambiar nada, cuando:

- **Esta máquina no tiene el estado de la instancia.** El estado de la configuración vive en
  `~/.devicechain/instances/<instancia>/` en la máquina que la creó. Ejecute allí la
  actualización, o copie antes ese directorio. Contiene credenciales, así que manténgalo privado
  (modo `0700`).
- **El plan borraría o reemplazaría algo que gestiona la configuración**, como el bróker o el
  almacén de eventos. El rechazo dice qué. Las notas de una versión así explican cómo pasar a
  ella. Esto juzga los recursos que gestiona la configuración; un objeto que desaparece dentro de
  uno de ellos se muestra como un cambio de ese recurso.
- **El plan acortaría la ventana de recuperación del almacén de eventos o dejaría de declarar un
  lector de analítica** que nada de lo que lee esta actualización declara. Son valores puestos a
  mano, y aplicar sin ellos podaría respaldos o dejaría sin gestionar la contraseña de un lector.
  Declárelos como se describe abajo y vuelva a ejecutar la actualización.
- **El plan desactivaría los respaldos del almacén de eventos.** Ninguna declaración lo deja
  pasar: `dcctl` decide si los respaldos están activos según cómo se instaló el clúster.
  `--skip-infrastructure` mueve los servicios y deja el almacén de eventos como está.
- **El bróker o el almacén de eventos no está sano antes de empezar**, por ejemplo un servidor
  que no está listo o un almacén a mitad de una conmutación por error. Recupérelo primero. La
  única excepción es un bróker cuyo último despliegue gradual no terminó, con algunos servidores
  ya en los ajustes nuevos y uno de ellos sin estar listo. Solo una aplicación puede cambiar los
  ajustes hacia los que avanza, así que la actualización aplica sobre él, lo indica junto al plan
  y espera a que los ajustes nuevos terminen de desplegarse. Un bróker al que le falta un servidor
  con los ajustes que ya ejecutan todos, por ejemplo tras perder un nodo, se sigue rechazando.
- **El plan cambia la imagen del almacén de eventos y sus ajustes a la vez** en un almacén de más
  de una instancia, algo que rechaza el operador de la base de datos. El rechazo indica el orden
  en que aplicarlos.

`--skip-infrastructure` mueve solo los servicios, como hacían las actualizaciones anteriores a
esta versión, y al terminar dice lo que dejó sin aplicar.

**Valores que usted mismo dio a la configuración.** Una actualización aplica la configuración con
los valores de `dcctl`, como hace un bootstrap. Un valor se conserva cuando está declarado en un
archivo `terraform.tfvars` o `*.auto.tfvars` junto al estado de la instancia, en
`~/.devicechain/instances/<instancia>/infra/instance/`, o en una variable de entorno
`TF_VAR_<nombre>`: OpenTofu lee ambos en cada aplicación que ejecuta `dcctl`. Un valor pasado a
mano con `-var` no se conserva. Entre los valores que se indica fijar así están
`backup_retention_tsdb`, `timescale_analytics_readers` (consulte
[Acceso SQL y BI](../guides/sql-and-bi-access.md)) y los del bróker `nats_cpu_request`,
`nats_memory_request` y `nats_memory_limit`. Una solicitud o un límite del bróker que la
actualización bajaría se muestra como aviso junto al plan.

Esto solo funciona para una variable que `dcctl` no pasa por sí mismo, porque OpenTofu deja que un
`-var` en la línea de órdenes prevalezca sobre esos archivos y sobre `TF_VAR_`. `dcctl` pasa, en
cada aplicación:

- **en una instancia instalada con `--compact`**, las solicitudes del bróker `nats_cpu_request` y
  `nats_memory_request`, y los tamaños de sus volúmenes. Fijar esas solicitudes en
  `terraform.tfvars` no tiene efecto en una instancia compacta. `nats_memory_limit` no está entre
  ellas.
- **según cómo se instaló el clúster**, si los respaldos de base de datos están activos
  (`enable_database_backups`), la clase de instantánea (`backup_snapshot_class`) y la ubicación de
  las bases de datos en los nodos. Cámbielos con `dcctl install`, no en ese archivo.

Los avisos y rechazos de la actualización indican en qué caso está cada valor.

Lo que ve una instancia en marcha:

- **Los servidores NATS se reinician cuando cambian sus ajustes.** Con `--ha` se reinician de
  uno en uno; cada uno está Ready antes de que se detenga el siguiente. Sin `--ha` se reinicia el único servidor y el
  bróker no está disponible mientras tanto, normalmente alrededor de un minuto. Los servicios y
  los dispositivos se reconectan solos; las publicaciones hechas entretanto se rechazan. Si el
  nodo no tiene sitio para las nuevas solicitudes del servidor, este queda en `Pending`: con
  `--ha` los otros dos siguen atendiendo, pero sin `--ha` el bróker sigue **caído** hasta que haya
  sitio, y la actualización falla tras hasta 15 minutos diciéndolo.
- **La devolución de llamada de autenticación de dispositivos va cifrada y sus claves cambian de
  sitio.** La contraseña de un dispositivo cruzaba el bróker en claro hacia cualquier servicio que
  escuchara. `dcctl upgrade` crea un Secret llamado `dci-<instancia>-callout` con la semilla de
  firma de la devolución de llamada y una nueva semilla de cifrado, montado solo en
  `device-management`; la configuración de la instancia conserva únicamente sus mitades públicas. La
  primera actualización que lo hace aplica el bróker en dos pasos para que los dispositivos sigan
  conectando: primero se despliegan los servicios con un respondedor que lee solicitudes cifradas y
  sin cifrar, y solo después se vuelve a aplicar el bróker con el cifrado activado. Ese segundo paso
  es un reinicio progresivo más de los servidores NATS. Los dispositivos conectados al servidor que
  se reinicia se reconectan solos, en segundos con `--ha`; sin `--ha` el bróker no está disponible
  el minuto habitual. Si el segundo paso falla, los dispositivos siguen conectando, y ejecutar
  `dcctl upgrade` de nuevo lo termina.
- **Todos inician sesión una vez más, y el estado de inicio de sesión pasa a una cuenta propia.** Los
  tokens de actualización, los códigos de autorización OAuth, los registros de inicio de sesión único
  y la limitación de intentos vivían en la cuenta NATS que comparten todos los servicios, donde
  cualquier credencial de servicio podía leerlos o copiarlos a un stream propio. `dcctl upgrade` crea
  una credencial nueva y un Secret llamado `dci-<instancia>-identity` que guarda su contraseña y se
  monta solo en `user-management`. El bróker recibe una cuenta `IDENTITY` aparte, a la que solo esa
  credencial puede entrar y que no comparte nada con la otra cuenta. Los buckets antiguos se
  **eliminan, no se copian**: sus tokens eran legibles por todos los servicios, así que trasladarlos
  mantendría viva la exposición. Todas las sesiones anteriores a la actualización terminan: las
  personas vuelven a iniciar sesión en la consola y los paneles, y los clientes MCP y OAuth vuelven a
  autorizar. Los tokens de acceso ya emitidos siguen funcionando hasta que caducan, 15 minutos por
  defecto. Mientras `user-management` se actualiza, un inicio de sesión o una renovación de token que
  llegue a un pod que aún ejecuta la versión anterior puede fallar con un error durante unos segundos;
  al reintentarlo funciona. Nada se pierde en silencio: `user-management` registra los buckets que eliminó la primera
  vez que arranca, y `dcctl upgrade` lo indica al terminar. El bróker se reinicia una vez, servidor a
  servidor con `--ha`, para añadir la cuenta. `user-management` se niega a arrancar si la instancia
  dice que la cuenta existe y falta su contraseña, en lugar de recurrir a la cuenta compartida.
- **Cada área recibe una credencial propia para el bróker.** Hasta ahora todos los servicios entraban
  al bróker de mensajes con una sola credencial compartida, de modo que cualquier pod podía presentar,
  y cualquier fuga de su configuración exponía, la credencial de todos. `dcctl upgrade` crea una
  credencial por cada área que usa el bróker, y otra para la administración del bróker, y guarda cada
  una en un Secret llamado `dci-<instancia>-nats-<área>` que se monta solo en el pod de esa área.
  `mcp` y `ai-inference` nunca abren una conexión con el bróker y no montan ninguna. Cada credencial
  queda limitada a lo que usa su área, y a nada más: los streams en los que escribe, los consumidores
  con los que lee (con nombre exacto, así que un área no puede reescribir, tomar mensajes de ni
  confirmar los de otra), los buckets clave-valor que abre y
  sus propios subjects de respuesta. Una credencial ya no puede publicar en los streams de otra área,
  escribir en la caché de otra ni suscribirse a las respuestas de otra. Los permisos se generan a
  partir de las mismas declaraciones contra las que se comprueban los servicios, así que no pueden
  quedar por detrás de lo que un servicio hace. La credencial compartida sigue en su sitio junto a
  las nuevas durante esta actualización, con los permisos que siempre tuvo, de modo que los servicios que aún no se han
  reiniciado siguen conectándose; hasta la siguiente actualización
  sigue en la configuración que lee cada pod, así que un pod aún puede presentarla, y esto todavía no es una frontera frente a un
  servicio comprometido. Ejecute `dcctl upgrade` una vez más para retirarla. Cuando cada área ya usa su propia credencial y la actualización anterior terminó, esa
  actualización elimina la credencial compartida del bróker y de la configuración de la instancia, con un reinicio más del bróker, y su
  resumen final indica qué fase se ejecutó (o por qué no retiró la credencial). Una instancia nueva nunca tiene una credencial
  compartida. Una vez retirada, no se admite volver a una versión anterior: ejecute de nuevo el `dcctl upgrade` más reciente. Dos permisos son
  más amplios a propósito: event-sources conserva permisos de actualización sobre los almacenes MQTT
  del bróker, y user-management puede purgar los datos de un tenant eliminado en todos los streams, salvo los
  consumidores con los que event-sources conserva la durabilidad de los mensajes confirmados.
  Una instalación que ejecuta el chart sin dcctl y aporta su propio bróker puede imprimir los mismos
  permisos con `dcctl broker permissions`. El bróker se
  reinicia una vez, servidor a servidor con `--ha`, para añadir los usuarios. Un área se niega a
  arrancar si la instancia dice que las credenciales existen y falta la suya, en lugar de recurrir a la
  compartida. Las credenciales existentes nunca se reemplazan: una actualización crea solo las que faltan.
- **Los streams y buckets los crea ahora una única credencial de administración, antes de que los servicios
  se actualicen.** La configuración de un stream decide qué se almacena bajo qué subject, de modo que dejar que
  cada servicio creara y cambiara los streams que usa permitía que cualquiera de ellos remodelara un stream para
  capturar o falsificar mensajes. El chart ejecuta ahora un job, `<instancia>-broker-admin`, antes de cada instalación y
  actualización (un hook de Helm pre-install y pre-upgrade). Entra con la credencial de administración del
  bróker, que ningún Deployment monta, y hace lo que antes hacía cada servicio por su cuenta al arrancar:
  crea lo que falta, restablece los subjects, la ventana de duplicados, la política de descarte, los límites de tamaño y el
  número de réplicas de lo que existe y, para el único stream de captura que una versión anterior creó con otra
  retención, espera a que su lector lo vacíe antes de recrearlo como se declara. Esa espera puede tardar unos
  minutos. Un servicio ya no crea ni cambia nada: comprueba que cada stream y bucket que abre coincide con lo
  que declara su versión, y se niega a arrancar, indicando el stream y en qué difiere, cuando falta alguno o es
  incorrecto. Si el job falla, la actualización falla antes de que se actualice ningún servicio, y `dcctl upgrade`
  muestra el log del job; los servicios de la versión anterior siguen funcionando. `dcctl broker reconcile
  <instancia>` ejecuta de nuevo el mismo job, que es también la forma de recuperar un stream o bucket borrado a mano.
  Las credenciales de área ya no tienen derecho a crear, actualizar ni borrar un stream o bucket, con dos
  excepciones que se mantienen a propósito: event-sources conserva sus derechos sobre los almacenes MQTT propios
  del bróker, y user-management conserva el derecho a borrar las copias antiguas de los buckets de inicio de sesión.
  Las reproducciones del stream resolved-events que hace event-processing (su puesta al día tras un reinicio, el
  relleno de huecos en vivo y la vista previa de reglas) leen ahora el stream por secuencia, lo que no crea ningún consumidor en
  el bróker, así que ningún área tiene derechos sobre los consumidores de otra área en un stream de mensajes; el job de
  administración activa para ello las lecturas directas del stream. Con esto cambian dos comportamientos. La vida útil de los buckets de caché queda fijada en cinco minutos, porque
  los crea el job y ya no se la pide a cada servicio; los ajustes de tiempo de vida de la caché de
  device-management limitan solo cuánto tiempo conserva una réplica una entrada en su propia memoria. Y un
  número de réplicas aumentado en la configuración de la instancia surte efecto cuando se ejecuta el job, en la
  siguiente actualización, y no en cada reinicio de pod. Un pod de la versión anterior que se reinicia después de
  actualizar el bróker pero antes de que el job se haya ejecutado es rechazado cuando intenta reconfigurar un stream; avance a la
  versión nueva y arrancará. Una instalación que ejecuta el chart sin dcctl necesita tener listos el namespace y el Secret de
  la credencial de administración antes de la primera instalación, porque el job se ejecuta antes de que el chart cree nada;
  puede desactivar el job con `brokerAdmin.enabled=false` y crear los streams y buckets por su cuenta con la imagen
  `dc-broker-admin`.
- **El almacén de eventos se reinicia cuando cambian sus pods**, por ejemplo por una imagen o una
  ubicación nuevas. Un cambio solo de ajustes se recarga sin reinicio. Con `--ha` se reinician
  primero las réplicas en espera y después el rol primario pasa a una de ellas. Sin `--ha` se
  reinicia la única instancia, y event-management no puede almacenar eventos hasta que vuelva.
  Esos eventos esperan en el stream de ingesta y se reintentan, y uno solo se abandona tras unos
  cuatro minutos de reintentos fallidos.
- **Los dos pueden reiniciarse a la vez.** Se aplican juntos, y la actualización espera a que
  ambos estén sanos antes de mover los servicios.

Una versión que cambie la imagen del almacén de eventos llega ahora por este paso a las
instancias existentes: sus instancias se reinician con ella. Las notas de una versión así dicen
qué más necesita.

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
Para seguir sirviendo durante un despliegue, ejecute `replicas: 2` (o más) para cada área que
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

### El formato de los eventos en el bróker {#event-wire-format}

Cada evento que envía un dispositivo cruza el bróker dos veces como mensaje en un flujo retenido:
una vez tal como se recibió, y otra resuelto contra su dispositivo.
`infrastructure.messaging.wireFormat` en la configuración de la instancia define cómo se codifican
esos mensajes:

| Valor | Codificación |
| --- | --- |
| `compact` | **El valor por defecto desde `v0.24.0`.** Cada instante como un recuento de nanosegundos y el sello de cada credencial de dispositivo en binario. Alrededor de un tercio más pequeño por mensaje, lo que significa menos disco, red y retención del bróker por evento. |
| `legacy` | Cada instante como texto y cada sello en su forma de texto, byte a byte lo que escribían `v0.22.0` y anteriores. `v0.23.0` lo escribe por defecto. |

Cualquier otro valor, incluido `Compact`, se rechaza: el servicio no arranca y su registro nombra
la clave. Forma parte de la configuración montada, como el
[nivel de registro](./observability.md#el-nivel-de-registro), y se establece de la misma forma.

- **Todas las versiones desde `v0.23.0` leen ambos.** Los mensajes escritos en la forma `legacy`
  antes de una actualización siguen en los flujos retenidos y se leen como antes, incluida la
  vista previa de repetición de una regla de detección sobre una ventana que empezó antes de la
  actualización.
- **Actualice pasando por `v0.23.0`, no directamente desde `v0.22.0` o anteriores.** `v0.22.0` y
  anteriores solo leen `legacy`: un servicio en una de ellas descarta un mensaje `compact`, o lo
  aparta como fallido, así que una actualización gradual desde una de ellas directamente a una
  versión que escribe `compact` pierde los eventos escritos mientras los pods antiguos y nuevos
  funcionan a la vez. Actualice primero cada instancia a `v0.23.0`, espere a que todos los pods la
  ejecuten y solo entonces actualice a la versión posterior. En una instalación con el chart puede,
  en su lugar, establecer `legacy` para la actualización y quitarlo cuando todos los pods se hayan
  renovado. `dcctl` no tiene opción para establecerlo.
- **Volver atrás a `v0.23.0`** no requiere nada en cuanto al formato: lee los mensajes `compact`
  que escribió una versión posterior.
- **Volver atrás a `v0.22.0` o anterior** requiere establecer `legacy` primero, y que los
  consumidores hayan vaciado de los flujos todo mensaje `compact`.

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
del número de versión. La `v0.19.0` es la más reciente, por el tamaño de los datos y no por su
forma: una instancia cuyo almacén de eventos guarda más de 4 000 000 de filas aún sin comprimir,
o una tabla de más de 500 fragmentos cuyos eventos más antiguos no se pueden eliminar
([Comprueba el número de filas](#v0190-row-count)).
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

### v0.18.0 — lo que fallaba en silencio ahora lo dice, y la ingesta sigue el ritmo de su techo {#v0180-upgrade}

`v0.18.0` es una actualización en el sitio desde `v0.17.0`: `dcctl install` para el clúster y
después `dcctl upgrade` para cada instancia que contiene, como se describe [para
v0.17.0](#v0170-upgrade). Una instancia creada con `v0.16.0` o anterior sigue teniendo que
[destruirse y crearse de nuevo](#pre-declaration-recreate).

Buena parte de lo que cambia hace visible un fallo que antes pasaba desapercibido: un mensaje
perdido ahora se cuenta o se registra como mensaje no entregado, un servicio que no puede hacer su
trabajo se reinicia en lugar de seguir listo, y un ajuste que la plataforma no puede respetar
detiene el servicio al arrancar. Además, los eventos se persisten y se fusionan en el estado en
vivo por lotes, y se resuelven varios a la vez, de modo que una instalación por defecto sigue el ritmo de los 1000 mensajes por segundo que
se permiten a cada inquilino.

**Quién tiene que hacer algo:**

- **Todos:** cada usuario vuelve a iniciar sesión una vez, y los clientes OAuth, incluidos los
  asistentes de IA conectados por MCP, vuelven a autorizarse. No hay nada que preparar para ello.
- **Toda instancia creada antes de esta versión:** revise la contraseña del superusuario después
  de actualizar. La actualización no la cambia, y puede seguir siendo el antiguo valor por defecto
  publicado.
- **Quien tenga canales de notificación webhook:** cada uno necesita una clave `auth` antes de la
  actualización, o deja de entregar.
- **Quien fije en sus valores una clave que esta versión rechaza**, fije `resources` para un solo
  servicio, haya dimensionado por su cuenta el volumen de JetStream, restrinja de qué registros
  pueden descargar los nodos o permita que los conectores de los inquilinos lleguen a direcciones
  privadas: vea [Antes de actualizar](#v0180-before).
- **Quien llame a la API de GraphQL desde su propio código o scripts:** lea [API y
  errores](#v0180-api). El campo `message` de las alarmas desaparece, varios rechazos llevan ahora
  un código, y el servidor rechaza algunas peticiones que antes aceptaba.
- **Quien vigile el recuento de reinicios de los pods o busque en los registros de los servicios:**
  un servicio cuya conexión con el broker se cierra definitivamente, o cuyas lecturas de mensajes
  fallan durante dos minutos, ahora se reinicia en lugar de seguir listo
  ([Mensajería](#v0180-messaging)), y los servicios registran ahora en `info`, así que desaparecen
  líneas de depuración que quizá buscaba ([detalles](#v0180-log-level)).
- **Quien enrute o silencie alertas por su nombre:** una alerta cambia de nombre y se añaden
  varias; vea [Alertas añadidas y renombradas](#v0180-alerts).

Todo lo demás está en [Qué cambia](#v0180-what-changed), por área. Cada apartado indica si requiere
algo de usted; la mayoría no.

#### Antes de actualizar {#v0180-before}

Hágalo en este orden. Cada punto enlaza con el apartado que tiene los detalles.

1. **Dé a cada canal de notificación webhook una clave `auth`.** Un canal guardado sin ella deja de
   entregar con la actualización, incluido uno que hoy funciona. La versión actual acepta la clave
   y la ignora, así que añadirla antes no deja ninguna interrupción. En cada inquilino, localice los
   canales webhook cuyo `config` no tiene `auth`:

   ```graphql
   query {
     notificationChannels(criteria: {pageNumber: 1, pageSize: 100, channelType: "webhook"}) {
       results { token config hasSecret enabled }
       pagination { totalRecords }
     }
   }
   ```

   Si `totalRecords` es mayor que 100, lea también el siguiente `pageNumber`. Qué valor usar está en
   [Los canales de notificación webhook deben indicar cómo se autentican](#v0180-webhook-auth).
2. **Elimine o cambie la configuración que los servicios nuevos rechazan.** Cada una de estas
   impide que un servicio arranque, y el error nombra el ajuste.
   - `dispatchBacklog` en `functionalAreas.outbound-connectors.config`: elimínelo
     ([detalles](#v0180-dispatch-backlog)).
   - `auth.superuserPassword` en `functionalAreas.user-management.config`: elimínelo
     ([detalles](#v0180-superuser)).
   - `checkpointIntervalSeconds` por encima de 30 para `event-processing`: redúzcalo
     ([detalles](#v0180-checkpoint-interval)).
   - Un pool de conexiones igual o menor que un nuevo número de trabajadores. `event-management`
     rechaza un `tsdbConfiguration.maxOpenConnections` de 5 o menos, y `device-state` un
     `rdbConfiguration.maxOpenConnections` de 5 o menos, salvo que también fije
     `persistence.writers` (event-management) o `projection.writers` (device-state) por debajo
     ([detalles](#v0180-batched-persistence)). `device-management` rechaza un
     `rdbConfiguration.maxOpenConnections` de 10 o menos salvo que fije `resolution.workers` por
     debajo ([detalles](#v0180-resolution-workers)).
3. **Una instalación solo con el chart de los perfiles `telemetry` o `ingest-only`: fije una clave
   raíz de la instancia.** El chart ahora se niega a renderizar cualquier perfil sin ella. Una
   instancia creada con `dcctl bootstrap` ya tiene una ([detalles](#v0180-root-key)).
4. **Si sus valores fijan `resources` para un solo servicio, revise los pods renderizados.** Los
   `resources` de un servicio se fusionan ahora clave a clave sobre los `resources` de nivel
   superior, en lugar de sustituirlos, y `device-management` y `event-management` reciben un límite
   de CPU propio de 2 núcleos. Por tanto, un límite de CPU de nivel superior por encima de 2 núcleos
   baja esos dos servicios a 2 núcleos, y un `ResourceQuota` o un `LimitRange` de namespace que
   haya añadido puede rechazarlos ([detalles](#v0180-cpu-limits)).
5. **Si dimensionó por su cuenta el volumen de JetStream, compruebe su espacio libre.** La reserva
   crece 128 MiB por los recuentos de inicio de sesión, 128 MiB por los recuentos de conexiones
   MQTT y 64 MiB por el nuevo bucket de caché de device-management hasta que borre los dos que
   sustituye (16, 16 y 4 MiB en el preajuste compacto). Si no hay espacio suficiente,
   device-management no arranca ([detalles](#v0180-profile-cache-bucket)). Una instancia instalada
   con `--compact` conserva su volumen de JetStream de 2Gi: `dcctl upgrade` no vuelve a aplicar la
   infraestructura de una instancia, así que el volumen no se redimensiona, y los buckets nuevos
   siguen cabiendo en él. Solo una instancia creada por el `dcctl bootstrap` de esta versión recibe
   el nuevo volumen de 3Gi del preajuste compacto. No borre el StatefulSet `dc-nats` para
   redimensionarlo: ningún comando de dcctl lo vuelve a crear en una instancia existente
   ([detalles](#v0180-mqtt-connect-backoff)).
6. **Asegúrese de que sus nodos pueden descargar de `cgr.dev`.** El almacén de respaldos dentro del
   clúster usa ahora `cgr.dev/chainguard/minio`, fijado por digest; permítalo en sus reglas de
   salida o replíquelo con ese digest ([detalles](#v0180-backup-store-image)). Si ejecuta `dcctl`
   donde no se puede alcanzar el registro de proveedores de OpenTofu, haga que él o un espejo de
   proveedores sea alcanzable: cada `dcctl install`, `dcctl bootstrap` y `dcctl destroy` le pide
   ahora las versiones fijadas de los proveedores ([detalles](#v0180-dcctl-install-rerun)).
7. **Si un conector de un inquilino publica en una dirección privada, permítala.** Los conectores
   MQTT, Kafka, SNS y SQS ya no pueden llegar a direcciones de loopback, privadas, de NAT de
   operador, de enlace local o de metadatos de la nube, incluidos los brokers de Amazon MSK, Amazon
   MQ y SNS o SQS a través de un endpoint de interfaz con DNS privado. Permita cada dirección como
   su propio `/32` en `instance.config.infrastructure.egress.allowedDestinations`. Revise a la vez
   los esquemas de las URL MQTT y las ACL de Kafka por id de cliente
   ([detalles](#v0180-connector-egress)).
8. **Fije `outboundMessagesPerSecond` y `outboundBurst` con el mismo valor para `event-processing`
   y `outbound-connectors`.** Si no, el nuevo aviso `ConnectorDispatchRateLimited` se dispara cada
   vez que un inquilino medido con el valor por defecto de la plataforma envía más rápido que el
   menor de los dos ([detalles](#v0180-new-warnings)).
9. **Actualice sus propios clientes de GraphQL.** Quite `message` de las selecciones de alarmas y
   actualice `@devicechain/dashboards` y `@devicechain/widgets` junto con la plataforma
   ([detalles](#v0180-alarm-message)). Llame a `tenantDeletions` con su nuevo argumento de criterios
   ([detalles](#v0180-tenant-deletions)). Los demás cambios de la API, enumerados en [API y
   errores](#v0180-api), solo requieren atención si su código depende del comportamiento anterior.
10. **Corrija las condiciones CEL que son verdaderas para todo dispositivo sin un atributo.** Una
    condición de umbral o de duración como `!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]`
    deja de ejecutarse con la actualización. La forma corregida también es válida en la versión
    actual ([detalles](#v0180-cel-attribute-conditions)).
11. **Revise las ACL de su broker si ejecuta una fuente MQTT sobre su propio broker o una fuente
    Sparkplug.** Una suscripción que el broker rechaza detiene ahora todo el servicio
    `event-sources` en el caso de una fuente MQTT externa
    ([detalles](#v0180-external-mqtt-resubscribe)), y mantiene fuera de línea una fuente Sparkplug
    ([detalles](#v0180-sparkplug-refused-group)).
12. **Actualice las rutas de alertas, los silencios y los paneles que nombran lo que cambió.**
    `EventProcessingStreamNearFull` pasa a llamarse `JetStreamStreamNearFull`
    ([detalles](#v0180-unread-loss)). `JetStreamLeaseBucketNotReplicated` tiene un resumen nuevo
    ([detalles](#v0180-new-warnings)). Las tres alertas de mensajes no entregados pasan a su propio
    grupo de reglas ([detalles](#v0180-dead-letter-rule-group)). Cinco series de pérdidas de
    mensajes no entregados se sustituyen por un nombre por servicio
    ([detalles](#v0180-dead-letter-lost)).

#### Durante la actualización {#v0180-during}

- **Todos cierran sesión una vez.** Las sesiones de la consola, del panel y de los SDK terminan en
  su siguiente renovación, en 15 minutos salvo que haya cambiado la duración del token de acceso, y
  todos los tokens de acceso y de actualización emitidos antes de la actualización dejan de
  funcionar cuando termina el despliegue. Los clientes OAuth, incluidos los clientes MCP, tienen que
  volver a autorizarse ([detalles](#v0180-sessions)).
- **Durante unos segundos, las peticiones pueden rechazarse con `401 invalid or expired token`**, y
  un inicio de sesión con `invalid or expired token`, incluso para un token emitido momentos antes.
  Dura hasta que se detiene el último pod de user-management de la versión anterior. Después,
  volver a iniciar sesión y reintentar funciona ([detalles](#v0180-sessions)).
- **Varias cosas se reinician una vez.** `device-management` y `event-management` se reinician pod a
  pod por sus nuevos límites de CPU ([detalles](#v0180-cpu-limits)). `dcctl install` reinicia las
  instancias del almacén relacional, con una breve interrupción de escritura con `--ha` y una más
  larga con una sola instancia ([detalles](#database-primary-failover-in-seconds)), y reinicia el
  almacén de objetos de respaldos con su nueva imagen, lo que pausa el archivado mientras se
  descarga ([detalles](#v0180-backup-store-image)).
- **Las alarmas pueden retrasarse o perderse si el despliegue se atasca.** Un pod de
  `device-management` de la versión anterior no puede guardar una alarma nueva; la alarma se
  reintenta aproximadamente una vez por minuto y normalmente la guarda un pod actualizado. Mantenga
  el despliegue corto. Una pestaña de la consola abierta antes de la actualización recibe un error
  en las listas de alarmas hasta que se recarga ([detalles](#v0180-alarm-message)).
- **Los comandos LwM2M emitidos a mitad del despliegue pueden retrasarse varios minutos**, mientras
  `lwm2m-ingest` y `command-delivery` están en versiones distintas ([detalles](#v0180-lwm2m-confirm)).
- **Publicar o revertir un perfil, o editar una geocerca, puede tardar hasta un TTL de caché en
  llegar a todas las réplicas de `device-management`** mientras conviven las dos versiones
  ([detalles](#v0180-profile-cache-bucket)).
- **Algunas cosas pueden registrarse dos veces.** Un abandono puede registrarse como mensaje no
  entregado una vez por un pod de la versión anterior y otra por el aviso del broker
  ([detalles](#v0180-no-outcome-dead-letters)). Un evento sin `occurredTime` que seguía sin
  confirmar puede guardarse dos veces ([detalles](#v0180-processed-time)).
- **Una alerta de mensajes no entregados que estaba pendiente o activa vuelve a empezar** cuando su
  regla pasa al grupo nuevo ([detalles](#v0180-dead-letter-rule-group)).

#### Después de actualizar {#v0180-after}

Nada de esto es necesario para que la plataforma funcione. Cada punto limpia algo que la
actualización deja atrás.

- **Revise la contraseña del superusuario.** En una instancia sin contraseña generada, la
  actualización imprime un aviso al terminar. Si nunca cambió la contraseña de ese superusuario,
  sigue siendo `devicechain`: inicie sesión y cámbiela ([detalles](#v0180-superuser)).
- **Borre los dos buckets de caché que `device-management` ya no usa.** Cada uno conserva su
  reserva hasta que lo borra. Con la CLI `nats` y un inicio de sesión que pueda administrar
  JetStream en la cuenta de la plataforma:

  ```bash
  nats stream rm KV_<instance>_device-management_metric-defs-by-type
  nats stream rm KV_<instance>_device-management_profile-scope-by-type
  ```

  ([detalles](#v0180-profile-cache-bucket))
- **Dé a un almacén de eventos anterior los nuevos ajustes de parada.** `dcctl upgrade` no vuelve a
  aplicar las bases de datos de una instancia, así que una instancia creada antes de esta versión
  conserva los antiguos en su almacén de eventos hasta que lo parchea
  ([detalles](#database-primary-failover-in-seconds)).
- **Despeje las alarmas que levantó una condición CEL que ahora se rechaza.** La regla ya no se
  ejecuta, así que nada las resuelve ([detalles](#v0180-cel-attribute-conditions)).
- **Busque alarmas que no se levantaron durante el despliegue** con
  `dcctl dead-letters list --kind detection-action --source device-management`
  ([detalles](#v0180-alarm-message)).
- **Vuelva a enviar toda contraseña de dispositivo que deba empezar o terminar con un espacio.** Los
  valores guardados antes de esta versión se guardaron recortados
  ([detalles](#v0180-credential-values)).
- **Dé un secreto real a un perfil de aprovisionamiento guardado con un secreto vacío** con
  `updateProvisioningProfile` ([detalles](#v0180-provisioning-secret)).
- **Busque niveles de alerta guardados mayores que 2147483647**, que ahora convierten en error el
  listado de alertas que los incluye ([detalles](#v0180-int-range)).
- **Siga protegiendo las copias de seguridad tomadas antes de la actualización.** Contienen las
  claves de firma de tokens que esta actualización retira ([detalles](#v0180-sessions)).

#### Qué cambia {#v0180-what-changed}

El resto de esta sección describe los cambios que un operador o quien llama a la API puede notar,
agrupados por área.

#### Ingesta y resolución {#v0180-ingest}

##### La ingesta HTTP tiene su propia asignación {#v0180-http-allowance}

Las peticiones de ingesta HTTP se miden ahora contra una asignación por inquilino propia. Antes de
esta versión consumían la misma asignación que el tráfico MQTT, NATS y de presencia del broker del
inquilino. HTTP toma el inquilino de la ruta de la petición y comprueba la credencial del
dispositivo solo después de admitir la petición, así que cualquiera que pudiera llegar al puerto
8081 y conociera el nombre de un inquilino podía agotar esa asignación y hacer que se descartara la
telemetría MQTT del inquilino, incluidos mensajes que el broker ya había confirmado al dispositivo.
Ahora quien haga eso solo puede agotar la asignación HTTP del inquilino.

Esto cambia lo que significa el techo de ingesta de un nivel. En cada réplica de `event-sources` el
techo ya se aplicaba por separado al tráfico en vivo de los dispositivos y a un atraso que se drena
tras una caída, y HTTP es ahora una tercera asignación junto a ellas. En el peor caso, un inquilino puede
ser admitido hasta al triple de su techo por réplica ([Cuándo la ingesta puede admitir a un
inquilino por encima de su techo](../concepts/governance.md#ingest-above-ceiling)). Si dimensiona
los techos de los niveles para facturar o por capacidad, téngalo en cuenta.

Exponga el puerto 8081 solo detrás de controles de red, como una NetworkPolicy o un ingress que
autentique a quien llama. El chart no lo enruta por su ingress, pero por defecto cualquier pod del
clúster puede llegar a él.

##### Los nombres de inquilino inventados en la ingesta HTTP ya no hacen crecer la memoria, y dos alertas nuevas {#v0180-unconfirmed-tenants}

El endpoint de ingesta HTTP creaba una asignación de frecuencia separada para cada nombre de
inquilino que llegaba en la ruta de una petición, estuviera confirmado o no, así que un flujo de
nombres inventados podía hacer crecer la memoria de `event-sources` sin límite. Los nombres que el
plano de control no ha confirmado comparten ahora un conjunto fijo de 1024 asignaciones y, pasado
ese conjunto, una única asignación compartida con el valor por defecto de la plataforma. Los
inquilinos que llegan por MQTT, NATS o LwM2M no se ven afectados ([Nombres de inquilino que no se
pueden confirmar](../concepts/governance.md#unconfirmed-tenants)).

Se añaden dos avisos:

- `RateLimiterOverflowInUse` se dispara cuando la asignación compartida está en uso.
- `TenantsMeteredAtPlatformDefault` se dispara cuando un servicio lleva 15 minutos sin poder leer
  los techos de los inquilinos desde user-management y los está midiendo con su valor por defecto de
  la plataforma ([Antes de conocer el techo de un
  inquilino](../concepts/governance.md#unresolved-ceilings)).

Cada servicio exporta ahora
`…_governance_unresolved_admissions_total{dimension,cause}`, y `event-sources` exporta además
`…_ratelimit_overflow_admissions_total`.

Además, la asignación de un inquilino se mide ahora con un único reloj que nunca retrocede. Un
servicio que vaciaba un atraso según la hora de envío de cada mensaje podía admitir antes más que
el techo cuando esas horas retrocedían (un cambio de líder del broker entre servidores cuyos relojes
no coinciden) o cuando el techo de un inquilino cambiaba a mitad del
vaciado; esos mensajes se cargan ahora en la última hora que la asignación ya ha visto, lo que puede
descartar algo más pero nunca admite más.

##### `processedTime` ahora significa cuándo la plataforma recibió un evento {#v0180-processed-time}

El `processedTime` de un evento (en GraphQL, y la columna `processed_time` de la vista analítica
`events`) es ahora la hora en que la plataforma **recibió** el evento. Para MQTT en el broker de la
plataforma, es el momento en que el broker guardó el mensaje. Antes de esta versión era el momento
en que `event-sources` lo decodificaba. Normalmente los dos difieren en milisegundos, pero tras una
caída de `event-sources` diferían en toda la duración de la caída.

Un evento enviado sin `occurredTime` también se fecha ahora cuando se recibió, así que las
lecturas que esperaron en la plataforma durante una caída conservan la hora en que llegaron y no
la hora en que se procesaron. De ahí se sigue:

- **Las reglas de detección con ventana pueden perder esas lecturas tras una caída.** Las lecturas
  que llegan por LwM2M o Sparkplug mantienen la frontera del motor de detección en la hora actual
  mientras `event-sources` está caído. Cuando `event-sources` se pone al día, las lecturas a las que
  asigna una hora de antes de la caída llegan tarde a las reglas de repetición, de agregado
  deslizante y de correlación: se almacenan y se grafican, pero no cuentan en esas ventanas, y
  `detect_late_samples_total` sube.
  Las lecturas que llevan su propio `occurredTime` siempre se han comportado así. Vea [qué
  significa «cuándo» para el motor de detección](./detection-engine.md#timing-what-when-means).
- **Los eventos decodificados a ambos lados de la actualización pueden guardarse dos veces.** El id
  de un evento se deriva de su contenido, incluido su `occurredTime`. Un mensaje sin `occurredTime`
  que se guardó antes de la actualización y se entrega de nuevo después recibe ahora otra hora, y
  por tanto otro id. Solo puede ocurrir con mensajes que seguían esperando confirmación mientras se
  desplegaba la actualización.
- Las filas escritas antes de la actualización no cambian.
- Los agregados de mediciones colocan un atraso en los intervalos de cuando se recibió. Los
  agregados se refrescan 30 días hacia atrás, así que nada de una caída más corta queda fuera.
- Las acciones de salida se miden según cuándo su telemetría llegó a la plataforma. Para la
  telemetría que esperó durante una caída de `event-sources`, eso es ahora cuándo llegó y no cuándo
  se decodificó. Para un inquilino cuyo único tráfico esperó en el broker, la puesta al día ya no se
  cobra como una sola ráfaga. Un inquilino que además envió telemetría por LwM2M o Sparkplug durante
  la caída ya llevó su medidor de salida al presente, así que su atraso se sigue cobrando junto,
  como antes.

##### Los eventos que los dispositivos envían por MQTT se reenvían varios a la vez {#v0180-mqtt-forwarding}

No hay que hacer nada en la actualización.

- **`event-sources` mantiene hasta 128 publicaciones en inbound-events esperando al bróker a la
  vez** para los eventos que los dispositivos envían por MQTT al bróker de la plataforma, en lugar
  de cinco como máximo. El mensaje de un dispositivo se sigue confirmando solo después de que el
  evento que llevaba se haya guardado, y uno cuya publicación falla se sigue dejando para volver a
  entregarse. Los eventos enviados por HTTP, y a través de un bróker MQTT externo que usted haya
  configurado, se publican como antes.
- **Cuando publicar en inbound-events sigue fallando, `event-sources` frena** igual que
  `device-management`: tras una publicación fallida espera medio segundo, duplicándolo hasta dos
  segundos, y hasta que una publicación tiene éxito envía una cada vez.
- **Pueden volver a entregarse más mensajes de dispositivos cuando `event-sources` se detiene de
  golpe:** hasta los 128 que esperaban al bróker, además de los que ya retenía antes. Cada uno lleva
  el mismo identificador de detección de duplicados que antes, así que un evento que ya se guardó no
  se guarda dos veces.
- **Un mensaje de dispositivo que falló en todas sus entregas se sigue enviando a failed-decode,
  como mucho cuatro a la vez.** Uno que llega mientras se envían cuatro se deja para que el bróker
  lo termine, y se registra como mensaje no entregado en lugar de en
  failed-decode.
- **Los eventos de un dispositivo pueden llegar a inbound-events ligeramente desordenados, como
  ya podían:** cinco decodificadores procesan a la vez los mensajes capturados, y cada réplica
  publica.
- **`devicechain_eventsources_jetstream_publish_duration_seconds` añade una serie
  `mode="pipelined"` para `suffix="inbound-events"`.** [Observabilidad](./observability.md)
  describe los modos.

##### Una fuente MQTT sobre su propio broker vuelve a suscribirse tras una reconexión {#v0180-external-mqtt-resubscribe}

Una fuente de eventos MQTT que lee de un broker que usted gestiona se suscribía una sola vez, al
arrancar. El cliente se reconecta por su cuenta con una sesión limpia, así que tras un reinicio del
broker o un corte de red el broker no tenía ninguna suscripción para ella: la fuente seguía
conectada, no informaba de ningún problema y no ingería nada hasta que se reiniciaba el pod. Ahora
vuelve a suscribirse en cada conexión. La fuente de pasarela propia de la instalación por defecto
lee del flujo de la plataforma y no estaba afectada.

- **Un broker que rechaza la suscripción tras una reconexión detiene ahora todo el servicio
  `event-sources`**, y también uno que nunca la confirma. Kubernetes reinicia el pod, y un broker
  que sigue rechazándola al arrancar lo detiene de nuevo, como ya ocurría. Por tanto, un cambio de
  ACL en su broker se ve como un bucle de fallos, y la ingesta HTTP y la del broker de la plataforma
  caen con él. Compruebe antes de actualizar que la credencial de la fuente puede seguir
  suscribiéndose a sus temas. Vea [Matriz de transportes](../reference/transport-matrix.md).
- Una conexión que se cae mientras se envía la suscripción se deja a la reconexión del cliente.
- **Cuando la presencia del broker no puede recuperarse, `event-sources` se detiene ahora de forma
  ordenada.** Antes salía de inmediato, sin retirar su disponibilidad, sin detener cada fuente, el
  servidor GraphQL ni vaciar la conexión con el broker. Ahora hace todo eso antes de salir, y el pod
  se reinicia como antes ([Presencia de dispositivos](../concepts/device-presence.md)).

##### Una fuente Sparkplug con un grupo rechazado sigue fuera de línea {#v0180-sparkplug-refused-group}

Si el broker acepta la conexión de una fuente Sparkplug pero rechaza su suscripción a
cualquiera de los grupos de la fuente (lo más habitual, porque la credencial de la fuente no puede
leer ese grupo), la fuente ya no se anuncia en línea. No ingiere ninguno de sus grupos, se
desconecta y reintenta con una espera creciente de hasta 30 segundos hasta que se conceden todos
los grupos. Por tanto, un solo grupo rechazado detiene toda esa fuente hasta que se corrija la ACL
del broker. Ocurre lo mismo si el broker no confirma el propio anuncio en línea. Antes de desconectarse, la
fuente publica su estado fuera de línea, para que un anuncio en línea que el broker guardó sin
confirmarlo no se quede vigente.

Antes, la fuente se anunciaba en línea con el grupo ausente. Los nodos edge de ese grupo volcaban
entonces sus datos almacenados en una suscripción que no existía, y la fuente marcaba después sus
dispositivos como desconectados por quedarse en silencio. Un contador nuevo,
`devicechain_sparkplugingest_subscribe_failures_total`, cuenta las sesiones abandonadas: alerte
ante cualquier aumento. La línea de log nombra el grupo rechazado. Si vigila el estado del host
Sparkplug, una fuente con un grupo rechazado aparece ahora fuera de línea en lugar de en línea.
[Servicios edge](./edge-services.md) tiene los detalles.

##### Los eventos resueltos se publican varios a la vez {#v0180-resolved-publish}

No hay que hacer nada en la actualización.

- **`device-management` mantiene hasta 128 publicaciones de eventos resueltos esperando al bróker a
  la vez,** en lugar de esperar a cada una antes de enviar la siguiente, así que la resolución de un
  pod ya no queda limitada a un viaje de ida y vuelta de publicación cada vez. Un evento de entrada
  se sigue confirmando solo después de que se hayan almacenado todos los eventos resueltos que
  produjo, y uno cuya publicación falla se sigue dejando para su reentrega.
- **Un evento resuelto se almacena una sola vez cuando su evento de entrada se reentrega.** Cada
  publicación resuelta lleva un identificador de detección de duplicados derivado de su evento de
  entrada, así que cuando una publicación se almacenó pero se perdió su confirmación, la copia
  publicada en la reentrega la descarta el bróker en lugar de almacenarla dos veces. El bróker
  conserva cada identificador durante dos minutos, lo que cuesta memoria de NATS por cada evento
  resuelto publicado en ese intervalo, unos 100 bytes cada uno según la medición en proceso.
- **Un evento fallido se confirma solo después de que su registro se almacene en el flujo
  failed-events.** Antes, el evento de entrada se confirmaba cuando su registro se entregaba para
  publicarlo, así que un registro que no se llegaba a publicar se perdía. Ahora el evento de entrada
  se reentrega o, en su última entrega, se registra como mensaje no entregado. El registro
  lleva el mismo tipo de identificador de detección de duplicados, así que una reentrega no registra
  el fallo dos veces.
- **Cuando publicar en resolved-events sigue fallando, `device-management` va más despacio** en
  lugar de hacer fallar toda su cola de entrada a máxima velocidad: tras una publicación fallida
  espera medio segundo, duplicándose hasta dos segundos, y hasta que una publicación tiene éxito
  envía una cada vez. Las publicaciones que fallaron juntas, como todas las que están en curso
  cuando se cae la conexión con el bróker, comparten una sola espera.
- **Los eventos resueltos de un dispositivo pueden llegar al flujo ligeramente desordenados,** y ya
  podían antes: cada réplica publica, y una actualización progresiva ejecuta dos pods a la vez. El
  ajuste [`watermarkLatenessSeconds`](./detection-engine.md) de la detección lo tolera. Un evento
  cuya publicación falló se vuelve a publicar al menos 60 segundos después, más allá del valor por
  defecto.
- **Un histograma nuevo, `devicechain_<area>_jetstream_publish_duration_seconds{suffix, mode}`,**
  mide cada publicación en JetStream. [Observabilidad](./observability.md) lo describe.

##### device-management resuelve más eventos a la vez {#v0180-resolution-workers}

`device-management` resolvía los eventos entrantes con cinco resolvedores, un número fijado en el
código. Resolver un evento es sobre todo esperar: a que la base de datos autentique su credencial y
después a que el almacén clave-valor del bróker de mensajes devuelva su perfil y sus relaciones, una
consulta tras otra. Así que cinco resolvedores limitaban cuántos eventos por segundo podía resolver
un pod mientras la mayor parte de su CPU seguía libre. En un clúster de pruebas el pod resolvía unos
1600 eventos por segundo con 1,5 de sus 4 núcleos, y los eventos por encima de ese ritmo esperaban
en el pod, unos 140 a la vez, y después en el flujo, con la detección quedándose atrás con ellos.
Ahora ejecuta 10 por defecto, y el número es configurable. Medido dentro del proceso contra un
bróker de tres servidores, con cada consulta tardando 750 µs, 5 resolvedores resolvieron unos 1500
eventos por segundo y 10 unos 2900. Consulte [Resolución de eventos](./observability.md#event-resolution).

- **El ajuste nuevo es `resolution.workers`** (por defecto `10`). Debe ser menor que el pool de
  conexiones del servicio (`rdbConfiguration.maxOpenConnections`, 20 si no se indica). Un valor
  fuera de rango impide que el servicio arranque, y el error nombra el ajuste. El valor por defecto
  solo se rechaza si fija `maxOpenConnections` para `device-management` en 10 o menos: fije
  `resolution.workers` por debajo antes de actualizar.
- **device-management ocupa más conexiones a la base de datos mientras resuelve eventos.** Cada
  resolvedor ocupa una mientras autentica la credencial de un evento, lo que con la autenticación de
  dispositivos `required` por defecto ocurre con cada evento. Con todos los resolvedores ocupados son
  ahora hasta 10 conexiones en lugar de 5, del pool que también usan la API GraphQL, las
  comprobaciones de conexión MQTT y el consumidor que aplica las activaciones y resoluciones de
  alarmas. Si fija `maxOpenConnections` por debajo de 20, compruebe que lo
  que queda les basta. Se permite más de la mitad del pool, y se registra al arrancar.
- **Un pod en su límite de CPU no gana nada con más resolvedores.** Esto solo sube el ritmo donde al
  pod le sobra CPU.
- **Una métrica nueva, `resolve_workers`,** indica cuántos resolvedores ejecuta el pod, y
  `resolve_inflight` puede llegar ahora a 10. `resolve_inflight` mantenido en `resolve_workers`
  significa que los eventos llegan más rápido de lo que el pod los resuelve.
- **Para volver a una versión anterior:** un `device-management` anterior rechaza una configuración
  que fija `resolution.workers`, como rechaza cualquier ajuste que no conoce. Quite el ajuste antes
  de volver atrás.

##### device-management usa un solo bucket de caché por tipo de dispositivo en lugar de dos {#v0180-profile-cache-bucket}

Las definiciones de métricas y el alcance de reglas de un tipo de dispositivo que se guardan en
caché son ahora un único bucket clave-valor, `<instance>_device-management_profile-resolution-by-type`,
de modo que cada evento de medición lee el perfil publicado de su tipo de dispositivo una vez en
lugar de tres, y un evento ya no puede validarse contra una versión del perfil y etiquetarse con
otra. No hay nada que hacer salvo que dimensione usted mismo el volumen de JetStream o gestione los
buckets a mano.

- **La actualización añade un bucket de caché a la reserva de JetStream** (64 MiB por defecto,
  4 MiB en el preajuste compacto). device-management crea el bucket nuevo al arrancar, y los dos que
  reemplaza conservan su reserva hasta que los borre (vea el punto siguiente). Si el volumen de
  JetStream tiene menos espacio libre que un bucket de caché, la creación del bucket nuevo falla
  por falta de almacenamiento y device-management no arranca. Una vez borrados los dos buckets
  antiguos, la reserva queda un bucket de caché por debajo de la de antes de la actualización, que
  es también lo que reserva una instalación nueva.
- **Una instancia actualizada conserva los dos buckets que este reemplaza**:
  `<instance>_device-management_metric-defs-by-type` y
  `<instance>_device-management_profile-scope-by-type`. Nada escribe en ellos tras la
  actualización, y sus entradas caducan dentro del TTL de caché con el que se crearon los buckets
  (60 segundos salvo que se cambiara `metricDefCacheTtlSeconds` antes de esta actualización), pero
  cada uno sigue reservando su techo hasta que lo borre. Borrarlos requiere la CLI `nats` con un
  inicio de sesión que pueda administrar JetStream en la cuenta de la plataforma; `dcctl` no tiene
  un comando para ello: `nats stream rm KV_<instance>_device-management_metric-defs-by-type` y
  `nats stream rm KV_<instance>_device-management_profile-scope-by-type`. Dejarlos solo cuesta esa
  reserva. Borrar un inquilino los sigue limpiando durante una versión más.
- **Mientras la actualización está en curso**, un perfil publicado o revertido, o una edición de
  geocercas, puede tardar hasta un TTL de caché en llegar a todas las réplicas de
  device-management: una réplica de la versión anterior limpia solo los buckets antiguos, y una de
  esta solo el bucket nuevo. Si una edición de geocercas se pierde así, los eventos de ubicación se
  marcan con el conjunto de geocercas anterior durante hasta ese TTL.

##### device-management sigue resolviendo eventos cuando un servidor NATS se cae de la red {#v0180-nats-server-drop}

No hay nada que hacer en la actualización.

- **Una búsqueda en una de las cachés de clave-valor de `device-management` espera como máximo
  medio segundo,** en lugar de los cinco segundos que se permiten a una petición NATS. Cuando un
  servidor NATS se caía de la red sin cerrar sus conexiones, parte de estas búsquedas se le
  enviaba y cada una esperaba los cinco segundos completos, así que la resolución de eventos bajaba
  a unos pocos eventos por segundo durante cerca de un minuto, sin registrar nada.
- **Una caché que agota el tiempo, o por la que no responde ningún servidor, se omite durante
  cinco segundos,** y sus búsquedas van a la base de datos. `device-management` registra una
  advertencia cuando eso empieza y una línea cuando la caché vuelve a responder. Consulte
  [Cachés que dejan de responder](./observability.md#kv-caches) para las cuatro métricas nuevas.
- **Eliminar una entrada de la caché tras un cambio nunca se omite,** y una eliminación que falla
  ahora se registra (`A key-value cache eviction failed`); antes, un fallo pasaba en silencio.
- **Resolver un evento que tarda más de cinco segundos ahora se registra como advertencia**
  (`Event resolution is slow`), como máximo una vez cada 30 segundos.

#### Detección {#v0180-detection}

##### Una condición de umbral o de duración que es verdadera para todo dispositivo sin un atributo ahora se rechaza {#v0180-cel-attribute-conditions}

**Reglas de detección.** Una condición de umbral o de duración escrita en CEL que sería verdadera en
todos los eventos de todos los dispositivos a los que les faltan los atributos que lee, sea cual sea
el contenido del evento, ahora se rechaza al publicar el perfil. La forma habitual es una prueba de
presencia negada unida con `||`, por ejemplo
`!("tempLimit" in attr) || m["temp"] > attr["tempLimit"]`, o una prueba de presencia negada sola.
Una regla así levantaba una alarma para cada dispositivo sin el atributo, informara lo que
informara, y la mantenía levantada mientras le faltara el atributo. Eso incluye los dispositivos
cuyo atributo se estableció con algo que no es un número o con alcance `CLIENT`, no solo los que
nunca lo establecieron.

Qué verá:

- Publicar un perfil que contenga una regla así falla, y el error nombra la condición y el motivo.
- Una regla con esta forma publicada antes de la actualización, incluida una publicada mientras la
  actualización se está desplegando, **deja de ejecutarse** con la actualización. El estado de la
  regla la muestra como `COMPILE_ERROR` con el mismo motivo, y el registro de `event-processing`
  anota una línea que empieza por `Published detection rule failed to compile; skipping`.
- **Las alarmas que esa regla ya había levantado siguen activas hasta que las limpie.** La regla ya
  no se ejecuta, así que nada las resuelve.
- Revertir un perfil a una versión publicada antes de la actualización trae de vuelta esa regla como
  `COMPILE_ERROR`. Publique una versión corregida en su lugar.

No se ven afectados los umbrales dinámicos creados en el formulario o en el lienzo; las condiciones
CEL que siguen evaluando el evento, como `!("tempLimit" in attr) && m["temp"] > 80.0`; ni una
condición usada como filtro en una regla de repetición, de tasa de cambio, de agregado en ventana o
de correlación de área, como `!("maint" in attr)`.

Para corregir una regla rechazada, compruebe el atributo en positivo o escriba el respaldo como su
propia comparación
(`"temp" in m && ("tempLimit" in attr ? m["temp"] > attr["tempLimit"] : m["temp"] > 80.0)`) y vuelva a
publicar el perfil. Consulte
[Umbrales dinámicos en una expresión CEL](../concepts/event-processing.md#dynamic-thresholds-in-cel).

También se corrige la documentación de la previsualización. La previsualización no resuelve
atributos de dispositivo, así que un respaldo CEL se previsualiza con su respaldo en todos los
dispositivos; no se previsualiza como que nunca se dispara.

##### Las reglas de duración colocan las lecturas tardías según su propia hora {#v0180-duration-late-readings}

Una regla de duración («temperatura por encima de 80 durante 10 minutos») ahora coloca cada lectura
según la hora en que se tomó, no según el orden en que llegó, y descarta una lectura que cumple su
condición pero queda más atrás de la frontera del motor de detección que el tiempo de sostenimiento
de la regla. Antes de esta versión, una lectura tardía se aplicaba como si fuera la más reciente:

- **Una lectura tardía que no cumplía la condición cancelaba un sostenimiento que lecturas más
  recientes seguían respaldando.** Un dispositivo que sube lecturas acumuladas podía retrasar una
  alarma de duración hasta un sostenimiento completo cada vez, o impedir que se elevara mientras la
  condición se cumplía todo el tiempo. Ahora se ignora una lectura tardía anterior a la racha, y una
  que muestra que la condición se interrumpió a mitad reinicia la racha desde la lectura más reciente
  que la cumplía.
- **Una lectura tardía que cumplía la condición podía abrir una racha por encima de una interrupción
  que el motor ya había visto**, y elevar una alarma que las lecturas no respaldaban. Ahora se ignora.
- **Una lectura de mucho antes de la frontera podía abrir una racha cuyo sostenimiento ya había
  pasado**, y elevar la alarma con el siguiente evento. Ahora se descarta y se cuenta en
  `detect_late_samples_total`, cuya descripción ahora nombra las reglas de duración junto a los
  tipos deslizantes.

Una lectura que no cumple la condición sigue terminando una alarma de duración elevada por tarde que
llegue, salvo que sea anterior a la alarma: una lectura tardía de antes de que se elevara la alarma,
que llega después, no la retira, y se cuenta en `detect_late_samples_total` cuando se tomó dentro de
la racha que elevó la alarma (una anterior a toda la racha se ignora). Como ahora las lecturas
se colocan donde corresponde, una alarma de duración puede elevarse más pronto o más tarde que antes, según
el orden en que llegaron sus lecturas; ya no se eleva sobre una racha que las lecturas muestran
interrumpida. Como hasta ahora, un episodio solo es seguro que eleve la alarma si dura su tiempo de
sostenimiento más la tolerancia de retraso. Consulte [qué significa «cuándo» para el motor de
detección](./detection-engine.md#timing-what-when-means).

Como la frontera es compartida por toda la instancia, un dispositivo cuyas marcas de tiempo van
sistemáticamente por detrás del resto de la flota más que el tiempo de sostenimiento de una regla de
duración más la tolerancia de retraso, por un reloj lento o un camino lento, nunca eleva esa regla: cada lectura suya que cumple
la condición se descarta y se cuenta como tardía. Antes de esta versión esas lecturas se aplicaban.

La previsualización del lienzo se ejecuta sin tolerancia de retraso, así que descarta una lectura
tardía sin margen. Ahora indica cuántas lecturas apartó por tardías, para las reglas de duración y
los tipos deslizantes.

**Lo que cuesta.** Para distinguir una lectura tardía de una interrupción, una regla de duración
ahora guarda un pequeño registro y un temporizador de caducidad por cada dispositivo que envía la
métrica de la regla sin cumplir su condición, hasta un tiempo de sostenimiento después de la lectura
más reciente de ese tipo. Antes, un dispositivo así no ocupaba nada. Para un dispositivo que reporta
al menos una vez por sostenimiento, el registro es por tanto permanente mientras siga reportando:

- **Dos claves vivas por dispositivo y regla de duración**, que cuentan en el techo de claves vivas
  por inquilino, que se mide y no se impone. Un inquilino con 100 000 dispositivos que reportan bajo
  cinco reglas de duración alcanza solo con esto el `maxLiveKeysPerTenant` por defecto de 1 000 000,
  y entonces se dispara el aviso `DetectTenantOverStateBudget`. Suba `maxLiveKeysPerTenant` si esa
  es su flota.
- **Unos 290 bytes de punto de control de detección por dispositivo y regla de duración**
  (`detect_snapshot_bytes`), medidos en proceso con un identificador de regla de 26 caracteres y
  tokens de dispositivo de 19 caracteres. Crece con la longitud de ambos.
- **La frontera avanza en cada intervalo de inactividad.** Esos temporizadores de caducidad son
  trabajo pendiente, así que una instancia con una regla de duración y algún dispositivo que reporta
  su métrica avanza la frontera y guarda un punto de control con el flujo en calma, en lugar de
  quedarse en reposo.

Un dispositivo con la alarma elevada también conserva su racha hasta que la condición cesa, cuando
antes la soltaba al elevarse la alarma.

Un punto de control escrito antes de la actualización se restaura sin cambios, incluida cualquier
alarma de duración elevada en ese momento. Volver después a la versión anterior no convierte los
registros nuevos en alarmas: solo lee del punto de control las rachas abiertas e ignora el resto. No hay que hacer nada en
la actualización.

##### Una acción que falla ya no detiene las demás acciones de la regla {#v0180-failing-action}

No hay que hacer nada en la actualización. Lea esto si alguna regla tiene más de una acción.

Antes de esta versión, las acciones de una regla se ejecutaban en el orden en que estaban listadas,
y la primera que fallaba detenía al resto. En cada reintento las acciones anteriores se volvían a
ejecutar y las posteriores no llegaban a ejecutarse nunca, así que un comando que no se podía
encolar durante unos minutos (por ejemplo, porque el inquilino estaba en su límite de comandos
retenidos) hacía que nunca se levantara una alarma listada después de él. Ahora cada acción se
intenta en cada entrega, pase lo que pase con las demás.

- **Una regla ya no necesita listar primero su acción más importante.** Las reglas que se
  reordenaron para sortear esto pueden quedarse como están.
- **Una regla que dependía del comportamiento anterior para ejecutar una acción solo si otra
  anterior tenía éxito deja de tenerlo:** cada acción se ejecuta con independencia de las demás.
- **Un reintento del motor de detección ya no envía por segunda vez un webhook ni una publicación a
  un conector, dentro de unos diez minutos desde el primer intento.** Las actualizaciones de alarma
  y las solicitudes a conectores que se vuelven a enviar en ese plazo las reconoce el bus de
  mensajes y se almacenan una sola vez. Los dos flujos afectados se reconfiguran automáticamente al
  arrancar la nueva versión; recordar cada solicitud durante diez minutos cuesta memoria de NATS en
  proporción a la frecuencia con que se disparan las reglas. Pasado ese plazo, y cuando el propio
  servicio de conectores reintenta una llamada, una solicitud todavía puede llegar dos veces a su
  destino.
- **Mientras una de las acciones de una detección sigue fallando, cada reintento vuelve a cobrar sus
  acciones de webhook y de conector contra la tasa de salida del inquilino,** aunque la solicitud
  reenviada se almacene una sola vez. Un fallo sostenido, como un inquilino en su límite de comandos
  retenidos, puede provocar así descartes en las demás reglas del inquilino.
- **Una detección que acaba en la cola de mensajes no entregados tras sus reintentos ahora nombra,
  en el detalle del mensaje, cada acción que falló en el último intento,** por tipo y clave de
  idempotencia, como `sendCommand/failed/<clave>` (y `httpCall/shed/<clave>` para una que la tasa de
  salida rechazó), la misma forma que usan los mensajes de descarte. El resumen y la descripción de
  la alerta `ReactPoisonDropping` se reformulan en consecuencia.
- **El motor de detección ahora obtiene del bus de mensajes una detección cada vez,** así que una
  detección nunca queda esperando detrás de otra lenta el tiempo suficiente para entregarse dos
  veces, y un intento contra un servicio que no responde termina cuando termina su entrega en lugar
  de prolongarse. Cada acción recibe su parte de ese tiempo, así que los comandos a un servicio que
  no responde no pueden agotarlo antes de que se active una alarma listada detrás de ellos.

##### Las acciones de salida ya no se pierden cuando el motor de detección se pone al día {#v0180-catch-up-metering}

Tras un reinicio, un despliegue o una conmutación por error, el motor de detección procesa la
telemetría que llegó mientras estaba caído. Las acciones de webhook y de conector de salida de ese
atraso se cobraban antes contra la tasa de salida del inquilino como si hubieran ocurrido todas a la
vez, así que la mayoría se descartaban y solo quedaba una métrica como registro. Ahora se miden,
tanto donde se desencadenan como en el servicio de conectores, según el momento en que la telemetría
llegó a la plataforma. Un inquilino dentro de su límite no pierde nada en una puesta al día ni se ve
frenado por ella.

Una acción que sigue por encima del límite se registra como mensaje no entregado con motivo `shed` y
tipo `detection-action`, hasta aproximadamente un mensaje por segundo por inquilino (60 de golpe) y
diez por segundo en total. Por encima de eso, las acciones se cuentan y se resumen en un mensaje por
inquilino y minuto. Cuatro ajustes de `event-processing` regulan el presupuesto:
`shedLetterPerSecond`, `shedLetterBurst`, `shedLetterGlobalPerSecond` y `shedLetterGlobalBurst`. Se
añaden dos avisos: `ReactShedLettersOverBudget` y `RateMeteringClockFallback`.

Las detecciones que el motor vuelve a publicar tras un reinicio las reconoce ahora el bus de mensajes
y se almacenan una sola vez dentro de una ventana de 30 minutos, así que los suscriptores del flujo de
eventos derivados ven menos duplicados. Cada evento derivado lleva ahora un campo `triggeredAt`. Si
filtra los mensajes no entregados por motivo, espere mensajes `shed` de tipo `detection-action`. No
hace falta hacer nada en la actualización.

##### Con una reserva en caliente, solo la réplica que detecta despacha acciones {#v0180-warm-standby}

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

##### `checkpointIntervalSeconds` tiene un máximo de 30, y algunos fallos silenciosos ahora avisan {#v0180-checkpoint-interval}

`event-processing` ahora se niega a arrancar cuando `checkpointIntervalSeconds` es mayor que 30. El
motor de detección confirma su entrada solo cuando guarda un punto de control, así que un intervalo
cercano o superior a la ventana de confirmación de 60 segundos del bróker retenía los mensajes de
un flujo tranquilo hasta que el bróker los volvía a entregar y, tras cinco ventanas, los contaba
como entregas agotadas, lo que dispara `ReplayCoveredDeliveriesExhausted` con un motor sano. 30
deja margen para el propio punto de control. El límite es un rechazo al
arrancar y no una comprobación del chart, así que `helm upgrade` con un valor mayor tiene éxito y
después el pod no arranca. Si sus valores lo fijan más alto, redúzcalo antes de actualizar. El valor
predeterminado (10) no se ve afectado.

Algunos fallos que eran silenciosos con el nivel de registro predeterminado ahora registran un
aviso:

- un muestreo fallido del tamaño y la replicación de un flujo o de un bucket KV, o de la pérdida sin
  leer de un consumidor;
- un muestreo fallido del retraso del consumidor del motor de detección;
- una autenticación de dispositivo en el bróker que falló por un motivo distinto de la propia
  credencial del dispositivo: el almacén de credenciales falla, o una credencial almacenada no
  tiene secreto y por tanto nunca puede autenticar.

Un dispositivo que presenta una credencial incorrecta, desconocida, caducada o revocada sigue
registrándose solo en depuración. Durante una caída del bróker, los avisos de muestreo se repiten en
cada pasada, aproximadamente cada 30 segundos por flujo; un muestreo interrumpido por un apagado se
queda en depuración. Durante una caída del almacén de credenciales (la base de datos), el aviso de
autenticación se repite una vez por cada intento de conexión de un dispositivo, así que una flota
que se reconecta durante la caída registra un aviso por intento.

##### Los cambios perdidos de reglas, dispositivos y atributos se reparan solos {#v0180-fact-repair}

Publicar o revertir un perfil de dispositivo, crear un dispositivo o cambiar su tipo, apuntar un tipo
de dispositivo a otro perfil, y establecer un atributo de umbral avisan al motor de detección una
sola vez. Antes de esta versión un aviso perdido nunca se reintentaba: una versión de perfil recién
publicada **no ejecutaba ninguna regla**, un dispositivo que nunca informaba no se vigilaba por
silencio y un umbral dinámico conservaba su valor anterior, sin nada marcado como fallido y sin
alerta. El remedio documentado era volver a publicar el perfil.

`event-processing` compara ahora su copia de las reglas publicadas, las versiones activas de perfil,
los dispositivos y los atributos de umbral de cada inquilino con `device-management` cuando asume la
detección y cada cinco minutos después, y corrige lo que difiera. Un cambio perdido se repara
en unos siete minutos como máximo, y un dispositivo o atributo eliminado en unos diez. Ya no hace falta
volver a publicar tras una interrupción del broker.

Lo que verá:

- **La actualización añade dos columnas a la base de datos de `device-management`**: cuándo pasó a
  estar activa la versión activa de cada perfil, y cuándo empezó la pertenencia de cada dispositivo
  a su perfil actual. Ambas admiten nulos y se rellenan con el uso, así que la migración es rápida
  con cualquier tamaño de flota y una réplica de la versión anterior sigue funcionando durante el
  despliegue. Un perfil cuya versión activa se eligió antes de la actualización se trata como
  activo desde que se publicó esa versión o, si se revirtió a ella, desde justo después de publicarse
  la versión más reciente.
- **Tres avisos nuevos**: `DeviceFactPublishFailing` (las notificaciones no se están enviando),
  `DetectFactsRepaired` (el motor corrigió algo de lo que no se le había avisado) y
  `DetectFactReconcileFailing` (la propia comparación está fallando). `DetectFactsRepaired` cuenta
  solo una pérdida real —un cambio cuyo aviso simplemente sigue en camino se deja para la siguiente
  comparación—, así que tras la actualización solo se dispara si de verdad se habían perdido avisos
  antes.
- `device-management` exporta `fact_publish_failures_total`, y `event-processing` exporta
  `detect_fact_reconcile_repairs_total` y `detect_fact_reconcile_failures_total`.
- Una reversión lleva ahora el momento en que se hizo tal como lo guarda `device-management`. Cada
  publicación o reversión de un perfil queda marcada después de la anterior, aunque las réplicas que
  las hicieron tengan relojes que no coinciden. La excepción son dos cambios del mismo perfil hechos
  en el mismo instante: cualquiera de los dos puede quedarse con el momento anterior, y el motor de
  detección acaba igualmente en la versión que guarda `device-management`.
- `event-processing` llama ahora también a `user-management` para listar los inquilinos y a
  `device-management` para leer reglas, dispositivos y atributos, con el mismo secreto de servicio
  que ya usa para las geocercas. Si el secreto de servicio o alguna de las dos direcciones no está
  configurado, la comparación queda desactivada y el servicio registra un aviso al arrancar, como
  ocurre con la evaluación de geocercas.

##### El lienzo de automatización permite crear reglas de Conectividad y no guarda sobre una regla que no puede mostrar por completo {#v0180-canvas}

No hace falta hacer nada en la actualización.

- **El lienzo tiene un nodo de Conectividad,** así que una regla de «el dispositivo se desconectó»
  puede construirse allí además de en el generador de formularios.
- **Al abrir una regla existente, el lienzo comprueba que guardar conservaría la regla completa.**
  Antes, una regla de un tipo que el lienzo no podía mostrar se abría como un lienzo vacío sin
  ninguna explicación, y una regla con un campo o un tipo de acción que el lienzo no modela se abría
  sin él. Guardar desde el lienzo reemplazaba entonces la regla almacenada por la reducida. Ahora el
  lienzo explica lo que no puede mostrar y desactiva el guardado para esa regla; edítela a través de
  la API.
- **Una regla construida en el lienzo cuya definición se modificó a través de la API se vuelve a
  disponer a partir de esa definición** cuando el lienzo la abre, en lugar de a partir de su
  disposición guardada anterior, así que guardar desde el lienzo ya no deshace el cambio. El lienzo
  indica cuándo lo ha hecho. La excepción es un lienzo guardado que ya no compila, que no puede
  compararse con la regla: se abre como estaba, y su nota advierte que guardarlo deshace cualquier
  cambio de ese tipo.
- **El lienzo conserva una plantilla de clave de alarma** definida a través de la API. La muestra en
  modo de solo lectura y la guarda sin cambios.
- **Guardar desde el lienzo ya no borra el nombre ni la descripción de una regla** cuando su
  definición no los contiene. Solo se envían cuando los edita en el lienzo.

#### Comandos {#v0180-commands}

##### Las respuestas a comandos que no se pudieron registrar vuelven a registrarse como mensajes no entregados {#v0180-command-responses}

En `v0.16.0` y `v0.17.0`, command-delivery no podía escribir ni un solo mensaje no entregado. Cada
uno que intentaba se rechazaba antes de escribirse, se contaba como perdido, y la respuesta del
dispositivo desaparecía. Por eso `DeadLetterWriteLost` podía dispararse con el bróker en buen
estado. Ese caso está corregido, y cambia lo que ocurre con los comandos afectados:

- Una respuesta que no se pudo registrar tras todos los intentos aparece como mensaje no entregado,
  y su comando pasa ahora a `FAILED`, con un error que dice que el dispositivo respondió y la
  respuesta se perdió. Antes, ese comando seguía en curso hasta que otra cosa lo resolvía.
- Una respuesta que no nombraba ningún despacho, o nombraba uno que su comando ya había dejado
  atrás, aparece como mensaje no entregado y no resuelve nada. El comando queda como estaba.

##### Los comandos LwM2M se confirman inmediatamente antes de llegar al dispositivo {#v0180-lwm2m-confirm}

Un comando entregado a un dispositivo LwM2M ahora se confirma con command-delivery inmediatamente
antes de que el adaptador lo lleve a cabo. Una entrega que la plataforma ya rearmó o reenvió se
descarta en lugar de llegar al dispositivo una segunda vez. Una entrega así puede aparecer tarde
tras una caída o una conmutación por error, y antes de esta versión podía actuar sobre un
dispositivo al que una entrega posterior ya había hecho actuar. Si no se puede contactar con
command-delivery, los comandos LwM2M esperan y se reintentan. Nunca se envían sin confirmar.

Lo que cambia y puede ver:

- **Cancelar un lote ahora también detiene los comandos LwM2M que se publicaron pero aún no habían
  llegado a su dispositivo.** Registran `CANCELLED`. El resultado de la cancelación todavía los
  cuenta como ya enviados, porque eso eran cuando se ejecutó la cancelación.
- **Para un comando LwM2M, `sentTime` ahora registra cuándo se le envió realmente al
  dispositivo.** Puede ser posterior a cuando el comando se publicó por primera vez.
- **`lwm2m-ingest` se niega a arrancar sin `infrastructure.commandDelivery`** siempre que tenga
  identidades de dispositivo que atender. El chart siempre lo define, así que solo afecta a una
  configuración hecha a mano.
- **Métricas.** `lwm2m-ingest` añade `devicechain_lwm2mingest_commands_stale_dispatch_total` (entregas descartadas porque
  la plataforma ya había avanzado: una actuación duplicada evitada, no un fallo) y
  `devicechain_lwm2mingest_command_live_claim_errors_total` (comandos no llevados a cabo porque command-delivery no pudo
  confirmarlos). Se elimina `devicechain_lwm2mingest_command_drain_dedup_total`.
- **Durante la propia actualización,** un `lwm2m-ingest` nuevo no puede confirmar comandos con un
  command-delivery que todavía está en la versión anterior. Un comando LwM2M emitido en esa
  ventana puede retrasarse varios minutos. Si agota sus reintentos antes de que ambos servicios
  estén actualizados, se rearma y se entrega en el siguiente despertar del dispositivo.

No hay que hacer nada durante la actualización.

##### Un dispositivo LwM2M lento ya no retrasa los comandos de los demás {#v0180-lwm2m-slow-device}

Cuando un dispositivo LwM2M tarda en responder y sus comandos se acumulan, los siguientes comandos
para él ahora se apartan en command-delivery y se entregan en orden momentos después. Antes de esta
versión detenían todo el adaptador: los comandos de todos los demás dispositivos LwM2M esperaban
detrás del lento. Los comandos para un dispositivo sin conexión se apartan de la misma manera, sin
ocupar el espacio que necesitan los dispositivos conectados.

Lo que cambia y puede ver:

- **Un dispositivo conectado recibe el resto de una acumulación larga sin reconectarse.** Sus
  comandos pendientes se entregaban de 32 en 32 por despertar, y el resto esperaba a que el
  dispositivo despertara de nuevo, cosa que un dispositivo que permanece conectado nunca hace.
  Ahora se entregan unos pocos cada vez, alternando con los comandos de otros dispositivos, hasta
  vaciar la acumulación.
- **Los comandos de un dispositivo siguen llegando en el orden en que se enviaron.** Esto ahora
  también se cumple cuando un comando no pudo confirmarse con command-delivery y se reintenta: los
  comandos posteriores lo esperan. Dos casos todavía pueden entregar un comando después de los que
  se enviaron detrás de él, y ambos requieren que command-delivery esté fallando. Uno es una
  interrupción que dura más que los reintentos del comando. El otro es una confirmación que
  command-delivery registró pero cuya respuesta nunca llegó a `lwm2m-ingest`, por ejemplo porque la
  petición agotó su tiempo de espera. En ambos casos el comando queda enviado pero sin ejecutar
  hasta que la plataforma lo detecta como varado, y entonces se entrega en la siguiente conexión
  del dispositivo, después de los comandos posteriores, o caduca.
- **Tras una conmutación por error, los comandos enviados mientras los dispositivos se reconectan
  se entregan un
  momento después.** Los comandos pendientes de cada dispositivo se entregan antes que cualquiera
  nuevo, así que durante ese breve intervalo los nuevos también se apartan. Es de esperar un breve
  aumento del tráfico hacia command-delivery tras una conmutación por error.
- **Un comando apartado de esta forma muestra el estado `PARKED`,** aunque su dispositivo esté
  conectado. Antes de esta versión `PARKED` solo significaba que el dispositivo no tenía conexión
  activa. Vea [Comandos](../concepts/commands.md).
- **Métricas.** `lwm2m-ingest` añade `devicechain_lwm2mingest_commands_overflow_parked_total`, con
  una etiqueta `reason` (`full`, `offline`, `bind`, `unconfirmed`),
  `devicechain_lwm2mingest_command_overflow_blocked_total` (el adaptador esperó porque command-delivery
  estaba lento) y `devicechain_lwm2mingest_command_drain_turns_total`. Se elimina
  `devicechain_lwm2mingest_command_drain_dropped_total`: la petición de un dispositivo de sus
  comandos pendientes ya no se descarta cuando el adaptador está ocupado.

No hay que hacer nada durante la actualización.

#### Conectores y notificaciones {#v0180-connectors}

##### Los conectores ya no pueden llegar a direcciones privadas, y el servicio de conectores tiene clientes nuevos {#v0180-connector-egress}

Los conectores de MQTT, Kafka, SNS y SQS reciben ahora la misma comprobación en el momento de
conectar que ya tenían los webhooks y los relés de correo. Se rechaza un destino que resuelve a una
dirección de loopback, privada, NAT de operador (carrier-grade NAT), de enlace local o de metadatos
de nube. El rechazo es **definitivo**: el despacho acaba en la cola de mensajes no entregados como
`blocked` y no se reintenta. En Kafka esto cubre cada bróker que anuncia el clúster, no solo las
direcciones que usted configuró. La comprobación se hace en el propio servicio, así que ya no
depende de `networkPolicy.enabled` ni de que su clúster la aplique.

Qué revisar antes de actualizar:

- **Los destinos en direcciones privadas dejan de recibir.** Esto incluye:
  - un bróker dentro del clúster o en una red emparejada;
  - **los brókers de Amazon MSK**, que son privados por defecto;
  - **Amazon MQ** usado para MQTT;
  - **SNS y SQS alcanzados a través de un endpoint de interfaz de VPC con DNS privado activado**.
    Con DNS privado, incluso los nombres predeterminados `sns.<región>.amazonaws.com` /
    `sqs.<región>.amazonaws.com` resuelven a direcciones privadas, así que también se ven afectados
    los conectores sin endpoint personalizado.

  Permita cada dirección como su propio `/32` en
  `instance.config.infrastructure.egress.allowedDestinations`. Un endpoint de interfaz tiene una
  dirección por zona de disponibilidad, y cada una necesita su propia entrada. Una autorización se
  aplica a todos los inquilinos y a todas las rutas de conectores y webhooks, no solo a la que usted
  tiene en mente.
- **Las URL de MQTT deben usar `tcp://`, `mqtt://`, `ssl://`, `tls://`, `mqtts://`, `ws://` o
  `wss://`**, con un puerto explícito y un bróker por entrada. `tcps://`, `mqtt+ssl://` y `unix://`
  ya no se aceptan. Se rechazan al guardar un conector, y un conector almacenado que use uno acaba
  en la cola de mensajes no entregados como `invalid` cuando se dispara. Las direcciones de Kafka
  deben ser `host:puerto`.
- **Las variables de entorno de proxy** (`HTTPS_PROXY`, `ALL_PROXY`) ya no las usan los conectores,
  y tampoco las variables `AWS_*` ni los archivos de configuración de AWS del pod.
- **El cliente de Kafka cambió.**
  - El identificador de cliente predeterminado es ahora `devicechain` (antes era `bento`). Fije
    `clientId` en el conector si sus brókers aplican ACL o cuotas por identificador de cliente.
  - Los registros sin clave se reparten ahora con particionado pegajoso (sticky partitioning).
  - La versión del protocolo se negocia con el bróker en lugar de ser fija.
  - La entrega no cambia: confirmación del líder, sin productor idempotente.
- **Los mensajes de SQS se envían de uno en uno** (`SendMessage`), no por lotes. El permiso de IAM
  es el mismo, `sqs:SendMessage`.

Dos cosas mejoran como efecto secundario. Un bróker de Kafka que está inaccesible brevemente ahora
se reintenta en lugar de acabar en la cola de mensajes no entregados como `invalid`. Y el binario
del servicio de conectores, que es la mayor parte de su imagen, ocupa alrededor de un tercio de lo
que ocupaba.

##### El servicio de conectores ya no acepta dispatchBacklog {#v0180-dispatch-backlog}

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

##### Los canales de notificación webhook deben indicar cómo se autentican {#v0180-webhook-auth}

La configuración de un canal webhook necesita ahora una clave `auth`: `none`, `bearer` o `header`.
Antes, el canal enviaba `Authorization: Bearer <secret>` cuando había un secreto guardado y
**ninguna credencial** cuando no lo había, así que un canal cuyo secreto faltaba o se había borrado
seguía publicando sin autenticación, y nada lo indicaba. [Configuración de canales de
notificación](../guides/notification-channels.md) tiene los detalles.

- **Un canal webhook guardado antes de esta versión no tiene `auth` y deja de entregar con la
  actualización**, incluido uno que hoy funciona. Cada entrega a él se rechaza en el primer intento
  y no se reintenta, para cada alarma y cada escalado que se dirija a él, y la alarma no se vuelve
  a entregar para ese canal. El servicio de notificaciones registra el inquilino, el token del
  canal y el motivo, y lo cuenta en
  `devicechain_notificationmanagement_deliveries_refused_total{reason="credential"}`.
- **Localice los canales que hay que corregir antes de actualizar.** En cada inquilino, ejecute
  `notificationChannels(criteria: {pageNumber: 1, pageSize: 100, channelType: "webhook"}) { results { token config hasSecret enabled } pagination { totalRecords } }`
  y busque un `config` sin `auth`. Si `totalRecords` es mayor que 100, repita con el siguiente
  `pageNumber` hasta haberlos leído todos.
- **Añada `auth` antes de actualizar.** La versión actual acepta la clave y la ignora, así que no
  hay interrupción. Use `bearer` para un canal con secreto (`hasSecret: true`) y `none` para uno
  sin secreto que no debe tenerlo, como un webhook entrante de Slack. Un canal que fijaba
  `authHeader` necesita `header`. Uno que solo fijaba `authScheme` (por ejemplo `Token`) necesita
  `"auth":"header","authHeader":"Authorization"`. Con `bearer` o `none`, quite `authHeader` y
  `authScheme`: esta versión los rechaza ahí en lugar de ignorarlos. Con `none`, envíe
  `secret: null` en la misma actualización si el canal tiene secreto.
- **Un canal cuyo `auth` y secreto no concuerdan se rechaza al guardarlo.** Esto incluye `bearer`
  o `header` sin secreto, `none` con secreto, y borrar el secreto de un canal `bearer` o `header`.
  Rotar el secreto de un canal que aún no tiene `auth` también se rechaza, hasta que se añada `auth`
  en la misma solicitud. Una actualización que solo renombra, describe o desactiva un canal no se
  comprueba, así que puede desactivar un canal roto sin arreglarlo antes; activarlo sí se comprueba.
- **Un canal SMTP con usuario y sin secreto** se rechaza ahora antes de que la plataforma conecte
  con el servidor de correo, y no se reintenta. Antes conectaba, desistía sin autenticarse y
  reintentaba en cada intento.
- **Una acción `httpCall` cuyo manejador de secreto no tiene ningún secreto guardado** se registra
  una vez en la cola de mensajes no entregados con el resultado `invalid` y no se reintenta. Antes
  se reintentaba hasta el límite de reentregas y se registraba como agotada, así que un secreto
  guardado durante ese intervalo aún podía dejar pasar la llamada. Nada vuelve a reproducir un
  mensaje no entregado, así que la llamada de ese disparo no se hace; corrija el manejador de
  secreto de la acción para que los disparos posteriores se autentiquen. La llamada nunca se envía
  sin su credencial.

##### Se puede rechazar el guardado de una política de notificación hecho desde una copia obsoleta {#v0180-policy-precondition}

`updateNotificationPolicy` acepta un `expectedUpdatedAt` opcional. Envíe el `updatedAt` que leyó
por última vez, o el que devolvió la actualización anterior, y un guardado hecho desde una copia
obsoleta se rechaza, sin escribir nada, si alguien ha cambiado la política desde entonces, sus
reglas incluidas. El rechazo dice `notification policy was modified by another writer; reload and
try again` y no lleva `extensions.code`. La respuesta de la actualización vuelve a leer ahora
`updatedAt` de la base de datos, para que pueda enviarse como la siguiente precondición. Si omite el
argumento, gana la última escritura, como antes. La consola no tiene editor de políticas de
notificación; solo quien llame a la API puede enviarlo. Vea [Qué
mutaciones son actualizaciones parciales](../reference/graphql-api.md#which-mutations-are-partial-updates).

#### Persistencia y estado {#v0180-persistence}

##### Los eventos se persisten por lotes {#v0180-batched-persistence}

`event-management` confirma ahora juntos, en una sola transacción, los eventos que esperan a un
escritor, en lugar de usar una transacción por evento. En un almacén de eventos replicado cada
confirmación espera a una réplica, y era esa espera, no el trabajo de la base de datos, lo que
limitaba la velocidad a la que se almacenaban los eventos. Un lote la paga una sola vez. No cambia
qué eventos se almacenan, y un evento se sigue reconociendo solo después de haberse almacenado. Un
evento rechazado se vuelve a escribir por separado, así que se reintenta o se notifica exactamente
como antes, y el resto de su lote se confirma sin él.

Hay tres ajustes nuevos, todos opcionales: `persistence.writers` (por defecto `5`, el número que
antes era fijo), `persistence.maxBatch` (por defecto `32`; `1` vuelve a una transacción por evento)
y `persistence.lingerMillis` (por defecto `0`). `device-state` incorpora `projection.writers` (por
defecto `5`). Consulte [Persistencia de eventos](./observability.md#event-persistence).

- **El número de escritores debe ser menor que el pool de conexiones del servicio.** Un valor fuera
  de rango impide que el servicio arranque, y el error nombra el ajuste. El valor por defecto de 5
  solo se rechaza si ha fijado `tsdbConfiguration.maxOpenConnections` para `event-management`, o
  `rdbConfiguration.maxOpenConnections` para `device-state`, en 5 o menos. Antes de actualizar,
  fije el número de escritores por debajo del pool, o aumente el pool como máximo hasta su valor por
  defecto de 20: los límites de conexiones a la base de datos de la plataforma están dimensionados
  para ese valor.
- **`device-state` también fusiona por lotes.** Consulte [El estado en vivo se fusiona por lotes](#v0180-live-state-batches).
- **Si representa las métricas de persistencia:** `persist_inflight` puede superar ahora el número
  de escritores, porque cuenta los eventos que esperan a que su lote se confirme, y
  `persist_duration_seconds` incluye ahora esa espera. Hay dos métricas nuevas,
  `persist_batch_size` y `persist_batch_fallbacks_total`.

##### El estado en vivo se fusiona por lotes, y un consumidor que se queda atrás genera un aviso {#v0180-live-state-batches}

`device-state` fusionaba cada evento en el estado en vivo de un dispositivo (conectividad,
actividad, últimas lecturas y última posición) en dos transacciones propias. En una base de datos
replicada cada confirmación espera a la réplica, así que el estado en vivo se quedaba atrás siempre
que los eventos llegaban más rápido de lo que podía confirmarlos de uno en uno, y tras un ritmo alto
y sostenido podía ir más de una hora por detrás de los eventos almacenados sin que nada lo
indicara. Ahora fusiona en una sola transacción los eventos que esperan a un escritor, igual que
`event-management` los persiste. Medido dentro del proceso contra TimescaleDB con una réplica
síncrona, cinco escritores fusionaron unos 3500 eventos por segundo, frente a unos 85 antes.

- **Lo que acaba guardando el estado en vivo de un dispositivo no cambia,** con una excepción. Una
  lectura o una posición sustituye a la almacenada solo si es estrictamente más reciente, y las
  horas se comparan ahora tal como las guarda la base de datos, al microsegundo. Antes, una lectura
  que llegaba dentro del mismo microsegundo que la almacenada podía sustituirla aunque fuera más
  antigua. Ahora se conserva la que se almacenó primero.
- **Un evento se sigue reconociendo solo después de haberse almacenado.** Si se rechaza la parte
  de un lote que corresponde a un inquilino, los eventos de ese inquilino se vuelven a fusionar de
  uno en uno, así que solo un evento que es rechazado por sí mismo se reintenta o se descarta como
  antes, y los eventos de los demás inquilinos se confirman juntos.
- **Hay dos ajustes nuevos, ambos opcionales:** `projection.maxBatch` (por defecto `32`; `1` vuelve
  a un evento cada vez) y `projection.lingerMillis` (por defecto `0`), con los mismos rangos que los
  de `event-management`. Consulte [Estado en vivo de los dispositivos](./observability.md#live-state-projection).
- **Si representa las métricas de `device-state`:** `state_inflight` puede superar ahora el número
  de escritores, porque cuenta los eventos que esperan a que su lote se confirme, y
  `state_duration_seconds` incluye ahora esa espera. Hay dos métricas nuevas, `state_batch_size` y
  `state_batch_fallbacks_total`.
- **Cada servicio informa ahora de cuántos mensajes esperan a cada consumidor que lee,** como
  `jetstream_consumer_pending_messages` y `jetstream_consumer_ack_pending_messages`. Un aviso nuevo,
  `JetStreamDurableFallingBehind`, salta cuando un consumidor ha tenido más de 10000 mensajes
  esperándole durante 15 minutos. El consumidor de detección de `event-processing` queda a cargo de
  `DetectConsumerBacklogHigh`, que no cambia. Consulte
  [Un consumidor que se queda atrás](./observability.md#consumer-backlog).

#### Bases de datos y conmutación {#v0180-databases}

##### La primaria de una base de datos conmuta en segundos {#database-primary-failover-in-seconds}

Eliminar el pod de la primaria de una base de datos, drenar su nodo o desplegar un cambio en ella
retenía la conmutación por error durante tres minutos: la primaria esperaba a que todos los
clientes se desconectaran, y los servicios de la plataforma nunca lo hacen. Ahora concede cinco
segundos a los clientes y después se detiene, de modo que con `--ha` una réplica en espera toma
el relevo en bastante menos de un minuto. Vea
[Cuando se detiene la primaria de una base de datos](./bootstrap.md#ha-database-failover).

- **Una base de datos de varias instancias renueva ahora su primaria mediante un traspaso.** Las
  versiones anteriores reiniciaban la primaria en su sitio y la esperaban, sin promover ninguna
  réplica, lo que convertía cada uno de esos despliegues en una interrupción completa de las
  escrituras.
- **Una instancia de base de datos que se está deteniendo se detiene de forma forzosa a los dos
  minutos**, en lugar de a los treinta. Un pod de base de datos que podía quedarse en
  `Terminating` hasta media hora después de haberse detenido su base de datos, dejando al clúster
  con una réplica de menos, ahora se elimina en dos minutos como máximo.
- **Si el almacén de copias de seguridad es inaccesible cuando se detiene una instancia**, esta
  ya no espera hasta treinta minutos a que se archive su último registro de escritura
  anticipada: espera como máximo dos. Los datos confirmados no se ven afectados, pero el archivo
  puede tener un hueco, como se describe en la página enlazada arriba.

**Las instancias del almacén relacional se reinician una vez cuando `dcctl install` actualiza el
clúster.** Con `--ha` primero se reinician las réplicas en espera y después el papel de primaria
se traspasa a una de ellas, lo que supone una breve interrupción de las escrituras. Una
instalación de una sola instancia reinicia su única instancia en su sitio, y el almacén
relacional no está disponible hasta que termina el reinicio. Las escrituras de ese intervalo se
reintentan.

**El almacén de eventos de una instancia existente conserva la configuración anterior.**
`dcctl upgrade` no vuelve a aplicar las bases de datos de una instancia, así que solo las
instancias creadas con esta versión reciben la nueva configuración en su almacén de eventos.
Para dar la misma configuración al almacén de eventos de una instancia existente, aplique un
parche a su clúster de base de datos con UNO de estos comandos. Esto reinicia sus instancias
una vez, como se describe arriba:

```bash
# con --ha: el ajuste de traspaso va en el MISMO parche que los tiempos
kubectl -n dci-<instance> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"primaryUpdateMethod":"switchover","smartShutdownTimeout":5,"stopDelay":120,"switchoverDelay":120}}'
# una instalación de una sola instancia
kubectl -n dci-<instance> patch cluster dc-tsdb --type merge \
  -p '{"spec":{"smartShutdownTimeout":5,"stopDelay":120,"switchoverDelay":120}}'
```

Con `--ha`, no divida el primer comando en dos parches aplicando primero los tiempos. Cambiar
`stopDelay` inicia el reinicio de inmediato y, si el ajuste de traspaso aún no está aplicado, la
primaria se reinicia en su sitio sin promover ninguna réplica, que es la interrupción larga que
esta versión elimina.

**Con `--ha`, un cambio de imagen de la base de datos y un cambio de parámetros de la base de
datos deben aplicarse por separado.** Con el ajuste de traspaso aplicado, el operador de la base
de datos rechaza una actualización que cambia a la vez la imagen de la base de datos y cualquier
parámetro de la base de datos. Por tanto, se rechaza una sola ejecución de `dcctl install` que
pase a una versión con una nueva imagen de base de datos y cambie además `--max-connections`.
Ejecute primero `dcctl install` sin el cambio de `--max-connections` y después otra vez con él.

:::caution El reinicio que aplica esta configuración aún tiene el antiguo límite de treinta minutos
El límite de dos minutos pertenece a cada pod de base de datos, así que llega con el reinicio que
sustituye el pod, y los pods que se sustituyen todavía tienen treinta minutos. Si un pod de base
de datos se queda en `Terminating` más de dos minutos durante ese reinicio, y su registro muestra
`failed waiting for all runnables to end within grace period of 30s`, su base de datos ya se ha
detenido y el pod no va a terminar por sí solo. Elimínelo, y el operador lo vuelve a crear:

```bash
# los pods del almacén relacional son dc-rdb-<n> en dc-system,
# los del almacén de eventos son dc-tsdb-<n> en dci-<instance>
kubectl -n dc-system delete pod dc-rdb-1 --grace-period=0 --force
```

Hágalo solo cuando el nodo del pod esté `Ready` y haya leído esa línea en su registro. Un pod que
está en `Terminating` porque su nodo es inaccesible es un caso distinto: su base de datos puede
seguir en ejecución, y no debe eliminarse a la fuerza. Consulte
[perder un nodo](./bootstrap.md#ha-node-loss).
:::

##### Una transacción de base de datos inactiva durante un minuto se termina {#v0180-idle-transactions}

No hay que hacer nada en la actualización.

Cada servicio pide ahora a la base de datos que termine cualquiera de sus transacciones que lleve 60
segundos inactiva. Esto cubre un pod que se congela o pierde la red en mitad de una transacción.
Antes, esa transacción seguía abierta, con sus bloqueos, hasta que se detectaba que la conexión
estaba muerta, lo que con la configuración predeterminada del sistema operativo puede tardar horas,
y aún podía confirmarse al volver la red. Eso podía escribir datos de un inquilino cuya eliminación
ya se había dado por completada. Cuando se alcanza el límite, la base de datos registra
`terminating connection due to idle-in-transaction timeout` y revierte la transacción. La solicitud
a la que pertenecía falla con un error, y el trabajo que un servicio toma de un flujo se le vuelve a
entregar. Vea [Rechazo de escrituras en la base de datos](./tenant-deletion.md#database-writes).

##### Qué ocurre al perder un nodo y al recuperarlo queda documentado {#v0180-node-loss}

El comportamiento no cambia. Con `--ha`, [perder un nodo](./bootstrap.md#ha-node-loss) describe
ahora, en orden, lo que ve un operador: con qué rapidez se recuperan el broker, los servicios y
las bases de datos, que el procesamiento de eventos puede detenerse durante aproximadamente un
minuto, por qué
los pods desalojados del nodo perdido se quedan en `Terminating` y no deben
eliminarse a la fuerza mientras el nodo sea inaccesible, y que la **vuelta** de un nodo es en sí
misma una interrupción breve. Un servidor del broker que quedó aislado vuelve tras haber
celebrado elecciones por su cuenta, y los demás servidores vuelven a elegir a sus líderes cuando
se reincorpora: cuente con unos segundos de "temporalmente no disponible" en JetStream alrededor
de 45 segundos después de que vuelva el nodo. No se pierde nada. Planifique la vuelta de un nodo
como planifica su pérdida.

El desalojo a los 30 segundos de los pods de los servicios cuando se pierde un nodo no cambia y
es deliberado, y ahora se comprueba en cada pod que renderiza el chart, incluida la consola.

#### Mensajería {#v0180-messaging}

##### Un servicio cuya conexión con el broker se cierra definitivamente ahora se reinicia {#v0180-broker-connection-closed}

El pod de cada servicio falla ahora su comprobación de actividad (`/healthz`), y con ella la de
disponibilidad, cuando su conexión con el broker de mensajería se cierra definitivamente sin que el
servicio lo haya pedido: por ejemplo, cuando el broker deja de aceptar una credencial que se revocó
o se rotó bajo el pod en ejecución, o envía un error que el cliente no sabe interpretar. Kubernetes
reinicia el pod, y el pod nuevo vuelve a leer su credencial. Antes de esta versión el pod seguía
activo y listo, registraba una línea de error y no hacía nada más. Un cierre que el propio servicio
pidió, como en una parada ordenada, nunca lo activa.

- **Un cambio de credencial puede verse como reinicios.** Si el broker corta la conexión de un pod
  antes de que el propio pod se sustituya, por ejemplo durante una rotación de credenciales, ese pod
  se reinicia una vez.
- **Una parada ordenada espera ahora a que se vacíe la conexión con el broker**, dentro del
  presupuesto de terminación del pod, de modo que los mensajes ya recibidos se procesan antes de que
  el pod salga. Una parada puede tardar más que antes, hasta ese presupuesto.

`event-sources` tiene además una conexión propia para la presencia del broker, y ahora ocurre lo
mismo con ella. Por esa conexión lee del broker los eventos de conexión MQTT, autenticada con la
credencial de la cuenta de sistema. Si el broker la cerraba definitivamente, el pod seguía activo y
listo con la presencia del broker congelada en silencio: no se afirmaba ni se liberaba nada, y nada
lo reiniciaba.

Ahora el pod falla su comprobación de actividad (liveness) y Kubernetes lo reinicia. Una credencial
rechazada llega a todas las réplicas a la vez, así que **se reinician todos los pods de
`event-sources`**, y la ingesta HTTP no está disponible mientras tanto; la telemetría MQTT la guarda
el broker y se procesa cuando vuelven. Si los pods reiniciados siguen sin poder iniciar sesión, la
presencia del broker se apaga con el motivo `broker_unreachable` y los dispositivos afirmados vuelven
a la presencia inferida, como cuando no se puede alcanzar el broker al arrancar ([Presencia de
dispositivos](../concepts/device-presence.md)).


##### Los bucles de lectura que siguen fallando ahora reinician su servicio {#v0180-read-loops}

Un bucle que lee mensajes de un flujo reintentaba una lectura fallida una vez por segundo mientras
siguiera fallando, mientras su pod se declaraba listo y no consumía nada. Cada uno de esos bucles
reintenta ahora con una pausa creciente, de hasta 5 segundos, y cuando las lecturas llevan dos
minutos fallando sin interrupción termina el proceso con un código de salida distinto de cero, de
modo que Kubernetes reinicia el pod. Una sola lectura correcta vuelve a empezar los dos minutos, que
bastan para superar una conmutación o un reinicio del broker. El reinicio vuelve a conectarse al
broker, vuelve a crear el consumidor y vuelve a leer la credencial montada.

Los bucles que lo incorporan en esta versión son:

- el despacho de conectores de `outbound-connectors`;
- el almacén de mensajes no entregados de `user-management`;
- la reescritura de mensajes no entregados de `command-delivery`;
- siete en `event-processing`: eventos resueltos, cambios de reglas y de atributos, la lista de
  dispositivos y sus eliminaciones, los conjuntos de geocercas y el despachador de acciones;
- el despachador de comandos de `lwm2m-ingest`.

Lo que verá es un recuento de reinicios donde antes había un pod listo que no consumía nada.

##### Un consumidor que se queda atrás de un flujo lleno ahora genera una alerta {#v0180-unread-loss}

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
`JetStreamDurableStalledBehindStream`. Eliminar un inquilino puede disparar la primera: la
eliminación
borra los mensajes de ese inquilino, incluidos los que un consumidor aún no había alcanzado.
Consulte
[Mensajes que un consumidor nunca leyó](./observability.md#unread-loss) para saber qué significa cada
alerta y qué hacer.

La advertencia de flujo casi lleno **cambia de nombre**, de `EventProcessingStreamNearFull` a
**`JetStreamStreamNearFull`**, y ahora cubre los flujos de todos los servicios, no solo los de
event-processing. El umbral (80% del límite de bytes durante 10 minutos) no cambia. Si una ruta o un
silencio de Alertmanager nombra la alerta antigua, cámbielo al nombre nuevo.

##### Los mensajes abandonados en su último intento ahora se registran {#v0180-no-outcome-dead-letters}

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
  mensajes no entregados o la reescritura de comandos agotó sus intentos, y que ahora puede caducar
  en
  el stream sin haberse almacenado (su último intento pudo almacenarlo y perder solo el acuse de
  recibo).

Durante la actualización escalonada, un abandono puede registrarse dos veces: una por un pod de la
versión anterior y otra a partir del aviso del broker. Es el mismo fallo; no se perdió nada.

##### Los contadores de pérdidas de mensajes no entregados son una métrica por servicio {#v0180-dead-letter-lost}

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

El contador cuenta también un mensaje no entregado que el servicio **rechazó** por estar mal
formado, lo que es un defecto de ese servicio y no un problema del bróker. La línea de error `LOST` del pod indica cuál
de los dos casos ocurrió.

#### Seguridad e inicio de sesión {#v0180-security}

##### Restablecer una contraseña termina las sesiones, y todos vuelven a iniciar sesión una vez {#v0180-sessions}

Dos cambios terminan todas las sesiones con la actualización.

**Cada usuario tiene ahora un valor de sesión**, y todo token que se puede canjear por otro nuevo
lo lleva: los tokens de actualización, el token de inicio de sesión que la consola guarda antes de
elegir un inquilino y los códigos de autorización de OAuth. Restablecer la contraseña de un usuario,
desactivarlo o eliminarlo cambia ese valor, y un token que lleva el anterior se rechaza. Antes de
esta versión, restablecer una contraseña dejaba funcionando todos los tokens de actualización ya
emitidos, de modo que uno robado seguía renovándose mientras se usara.

**La clave que firma todos los tokens de acceso y de actualización se sella ahora bajo la clave
raíz de la instancia.** Antes se guardaba sin cifrar en la base de datos de user-management, así
que cualquiera que pudiera leer esa base de datos, una copia de seguridad de ella o su archivo del
registro de escritura anticipada podía firmar tokens que todos los servicios aceptan. Ahora se sella
como cualquier otra credencial almacenada, y solo la clave en uso tiene mitad privada. Cuando una
clave se retira por rotación, su mitad privada se borra y solo se conserva la pública, de modo que
los tokens que firmó siguen verificándose hasta que caducan. La actualización **borra todas las
claves de firma que tenía la instancia**, en lugar de sellarlas, porque cada una ya ha estado sin
cifrar en todas las copias de seguridad tomadas hasta ahora. user-management genera una clave nueva
al arrancar.

Lo que verá en la actualización:

- **Todos los usuarios vuelven a iniciar sesión.** Los tokens emitidos antes de la actualización no
  llevan valor de sesión, así que no se pueden renovar, y cuando termina el despliegue tampoco
  validan. Las sesiones de la consola, del panel y de los SDK terminan en su siguiente renovación,
  dentro de los 15 minutos posteriores a la actualización salvo que haya cambiado la duración del
  token de acceso. También terminan las sesiones en una aplicación de paneles embebida. Una consola
  que en ese momento esté en el selector de inquilinos o en las páginas de administración puede
  mostrar un error al elegir un inquilino, en lugar de volver a la página de inicio de sesión.
  Cerrar sesión y volver a entrar lo resuelve.
- **Los clientes OAuth, incluidos los asistentes de IA conectados por MCP, deben autorizarse de
  nuevo.** Sus tokens de actualización se rechazan con `invalid_grant`.
- **La clave antigua es de confianza hasta que termina el despliegue.** Hasta que se detiene el
  último pod antiguo de user-management, sigue firmando tokens con la clave antigua y publicándola
  para que los demás servicios verifiquen con ella. `helm upgrade` y `dcctl upgrade` sustituyen
  todos los pods, así que la clave antigua deja de ser de confianza cuando terminan. Mientras ese
  pod antiguo se detiene, una petición hecha en los primeros segundos del despliegue puede
  rechazarse con `401 invalid or expired token`, y un inicio de sesión con
  `invalid or expired token`, incluso para un token emitido momentos antes; cuando el pod antiguo
  de user-management se ha detenido, volver a iniciar sesión y reintentar funciona.
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

Las copias de seguridad y el registro de escritura anticipada archivado **antes** de la
actualización siguen conteniendo las claves antiguas. Esas claves ya no son de confianza en ninguna
parte una vez terminado el despliegue, pero conviene seguir protegiendo esos archivos como las
credenciales que fueron.

##### Una nueva clave de firma se acepta en aproximadamente un segundo {#v0180-signing-key-trust}

Cuando user-management empieza a firmar tokens con una clave nueva, como ocurre en esta
actualización, cada uno de los demás servicios conoce esa clave la primera vez que ve un token
firmado con ella, volviendo a obtener el conjunto de claves que publica user-management. Las
versiones anteriores permitían a cada pod de servicio esa obtención como mucho una vez cada 30
segundos. Si respondía un pod de user-management que aún publicaba el conjunto de claves anterior,
como hace el pod antiguo mientras una actualización progresiva está en curso, ese servicio
rechazaba todos los tokens firmados con la clave nueva como `invalid or expired token` hasta que
pasaban los 30 segundos. Un inicio de sesión justo después de una actualización podía fallar así.

- **Un servicio vuelve ahora a obtener las claves en cuanto pasa un segundo desde que terminó la
  obtención anterior.** Las solicitudes que llegan mientras una obtención está en curso esperan a
  que termine en lugar de rechazarse.
- **Los tokens que nombran una clave que user-management nunca publicó** le siguen costando como
  mucho una obtención por segundo desde cada pod de servicio.
- **Una obtención que no encuentra la clave ahora se registra.** El servicio registra `JWKS
  refetched on an unknown kid, and the fetched set does not hold it.` con el identificador de la
  clave, lo que distingue un token rechazado durante un cambio de clave de un token que
  simplemente no es válido.

No hay nada que configurar.

##### La clave raíz de la instancia es ahora obligatoria en todos los perfiles {#v0180-root-key}

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

##### El superusuario ya no tiene contraseña por defecto {#v0180-superuser}

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

Si sus valores fijan `auth.superuserPassword` para user-management
(`functionalAreas.user-management.config.auth.superuserPassword`), elimínelo antes de actualizar.
user-management se niega ahora a arrancar con él, y el error nombra la clave. La contraseña inicial
solo llega del Secret `dci-<instance>-superuser`, o del que nombra `instance.superuserSecret`.

##### El inicio de sesión tiene límite de frecuencia, y las solicitudes de GraphQL llevan menos campos {#v0180-sign-in-limits}

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
- **La reserva de JetStream crece en 128 MiB** (16 MiB en el preajuste compacto), por el bucket que
  guarda los recuentos de inicio de sesión. En el preajuste compacto los buckets de caché bajan de
  8 a 4
  MiB cada uno para hacer sitio. Si dimensionó usted mismo el volumen de JetStream cerca de la reserva,
  compruebe que tiene espacio.
- **Una alerta nueva, `CredentialAttemptStoreFull`,** se dispara si ese bucket se llena. El inicio de
  sesión sigue funcionando cuando está lleno, pero los fallos repetidos ya no se ralentizan. [Espera
  entre intentos de inicio de sesión](../reference/graphql-api.md#sign-in-backoff) explica qué hacer.

[Límites de las solicitudes](../reference/graphql-api.md#request-limits) y [espera entre intentos de
inicio de sesión](../reference/graphql-api.md#sign-in-backoff) tienen los detalles.

##### Una renovación de sesión sobrevive a una caída breve {#v0180-session-refresh}

Renovar una sesión gastaba antes el token de actualización antes de volver a
comprobar la sesión. Un error de la base de datos o del broker durante esa comprobación terminaba
entonces la sesión: la renovación fallaba como «invalid or expired token» y el token ya no se
podía usar. Ahora la comprobación va primero. Un error del almacén deja el token válido y devuelve
un error que se puede reintentar («the session could not be refreshed right now; try again»), y el
endpoint de tokens de OAuth devuelve `server_error` sin el texto del error subyacente. Una renovación
rechazada porque la sesión terminó, la membresía se eliminó o se desactivó, o el inquilino niega el
acceso sigue gastando el token.

Solo se beneficia un cliente que reintenta con el mismo token de actualización. Un cliente OAuth,
como
un agente de IA que se conecta por MCP, recibe ahora `server_error` en lugar de `invalid_grant`
durante esa caída, así que puede reintentar en lugar de pedir al usuario que lo autorice de nuevo. La
biblioteca cliente de Go que usan el simulador, las pruebas de carga y `dcctl` ya no pierde su token
de renovación por la caída, pero sigue recurriendo a un inicio de sesión con contraseña cuando falla
una renovación, como antes. La consola sigue cerrando la sesión del usuario ante cualquier fallo de
renovación. No hay que hacer nada en la actualización.

##### Las conexiones MQTT fallidas repetidas con contraseña se ralentizan {#v0180-mqtt-connect-backoff}

No hay que hacer nada salvo que un dispositivo se conecte en bucle con una contraseña MQTT
incorrecta, o que haya dimensionado por su cuenta el volumen de JetStream. [Las conexiones fallidas
repetidas se ralentizan](../guides/device-credentials.md#connect-backoff) tiene los detalles.

- **Tras 10 conexiones fallidas seguidas para un mismo usuario MQTT, el siguiente intento espera 1
  segundo,** duplicándose hasta 30 segundos. Una conexión hecha durante la espera se rechaza como
  una contraseña incorrecta, aunque la contraseña sea correcta. Una conexión correcta pone la cuenta
  a cero. Las conexiones con token de acceso y las credenciales en el cuerpo de los eventos no se
  ven afectadas.
- **Alguien que conozca el usuario MQTT de un dispositivo puede impedir que ese dispositivo se
  reconecte** mientras siga enviando contraseñas incorrectas para él. Los dispositivos que ya están
  conectados no se ven afectados hasta que se reconectan.
- **Las conexiones con contraseña necesitan ahora JetStream.** Si no se puede acceder al almacén
  que guarda los recuentos, las conexiones con contraseña se rechazan, también brevemente mientras
  cambia el líder de JetStream de ese almacén, por ejemplo mientras se reinicia un nodo de NATS.
- Durante una caída de la base de datos, un dispositivo cuyas conexiones con contraseña siguen
  fallando se ralentiza del mismo modo, así que tras sus primeros 10 intentos sus conexiones
  rechazadas se registran en depuración en lugar de como un aviso cada una. Un almacén de recuentos
  inaccesible se registra como un aviso por minuto.
- **La reserva de JetStream crece 128 MiB** (16 MiB en el preajuste compacto) para el bucket nuevo
  que guarda los recuentos.
- **El volumen de JetStream del preajuste compacto crece de 2Gi a 3Gi para las instancias creadas
  por esta versión:** el almacén crece de 1 GiB a 2 GiB. Una instancia compacta existente conserva
  su volumen de 2Gi, porque `dcctl upgrade` no vuelve a aplicar la infraestructura, y el bucket
  nuevo sigue cabiendo en él.
- **Una alerta nueva, `DeviceCredentialAttemptStoreFull`** (aviso), se dispara cuando ese bucket
  se llena. Las conexiones siguen funcionando, pero sin la ralentización. Una ola de reconexiones
  muy grande puede llenarlo igual que un ataque.

##### Los valores de las credenciales se guardan exactamente como se envían {#v0180-credential-values}

- **El `credentialValue` de una credencial de dispositivo ya no se recorta.** Las versiones
  anteriores quitaban los espacios iniciales y finales al guardar el valor, pero comparaban sin
  recortar la contraseña que presentaba el dispositivo, así que una contraseña MQTT que empezaba o
  terminaba con un espacio nunca podía autenticarse. Ahora el valor se guarda exactamente como se
  envía. Solo un valor vacío, o un `null` explícito al actualizar, no guarda ninguna contraseña.
- **También cambia la dirección contraria.** Un valor pegado con un salto de línea o un espacio al
  final se guardaba sin él, así que se aceptaba a un dispositivo que presentaba la contraseña sin
  él. Ahora se guarda con él, y ese dispositivo se rechaza hasta que se vuelva a enviar el valor
  sin el salto de línea.
- **La actualización no cambia los valores guardados antes de esta versión.** Se guardaron
  recortados, y los espacios no se pueden recuperar. Si la contraseña configurada en un
  dispositivo lleva espacios alrededor, vuelva a enviar el valor con `updateDeviceCredential`.
- **Un nivel sin color ahora devuelve `color: null`** en la API de administración, donde antes
  devolvía `""`. Enviar `""` o `null` lo sigue limpiando, y la actualización convierte a null todos
  los colores vacíos guardados. Los espacios alrededor se recortan ahora antes de comprobar el
  color, así que `" amber "` se acepta como `amber` donde antes se rechazaba. La consola no
  necesita nada; el código propio que compare `color` con `""` debe comprobar null.
- **`firstName` y `lastName` se recortan como el resto del texto visible**, en `createIdentity` y
  en `updateProfile`, y un nombre limpiado se guarda como null. Las lecturas ya devolvían `null`
  para un nombre vacío, y lo siguen haciendo: la actualización convierte a null todos los nombres
  vacíos guardados. Hace lo mismo con el `endpoint` vacío de un proveedor de IA, que también se
  leía ya como `null`. Un nombre que una versión anterior guardó con espacios alrededor los
  conserva hasta que una actualización nombra ese campo, que entonces lo recorta.
- **Un número guardado demasiado grande para un `Int` de GraphQL es ahora un error en lugar de un
  número equivocado.** Los campos `throttleSeconds`, `escalateAfterSeconds` y `maxEscalations` de
  una política de notificación, y las anulaciones de ráfaga, prioridad de descarte, comandos
  retenidos y geocercas de un inquilino en la API de administración, convertían ese valor en uno
  negativo. Los valores escritos a través de la API siempre caben, así que esto afecta solo a un
  valor escrito en la base de datos por otra vía. Una actualización de una política de
  notificación que no nombra uno de esos tres campos tampoco lo reescribe ya.

##### Ya no se puede crear un perfil de aprovisionamiento con un secreto en blanco {#v0180-provisioning-secret}

`createProvisioningProfile` rechaza ahora un `provisionKey` o un `provisionSecret` vacío o formado
solo por espacios. Un perfil con un secreto vacío coincidiría con un dispositivo que también enviara
un secreto vacío. Ninguna instancia quedó expuesta a ello, porque la vía por la que un dispositivo
presentaría un secreto de aprovisionamiento todavía no se ha publicado; la comprobación cierra el
hueco antes de que lo haga. `updateProvisioningProfile` ya rechazaba un valor en blanco. Un
perfil que ya esté guardado con un secreto vacío no coincide ahora con ningún secreto presentado,
vacío o no: asígnele uno real con `updateProvisioningProfile`.

#### API y errores {#v0180-api}

##### Los WebSocket de GraphQL solo llevan suscripciones y se cierran cuando caduca su token {#v0180-websocket}

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

##### Los documentos de GraphQL deben usar los comentarios y las cadenas propios de GraphQL {#v0180-graphql-syntax}

No hay que hacer nada en la actualización salvo que su propio código o sus scripts escriban
documentos de GraphQL a mano. La consola, la aplicación de paneles, los SDK, `dcctl` y el servidor
MCP nunca envían nada de lo siguiente.

- **Un documento escrito con comentarios `//` o `/* */`, cadenas entre comillas invertidas o
  caracteres entre comillas simples ahora se rechaza** con un error de sintaxis, y no se ejecuta
  nada de él. Las versiones anteriores los aceptaban, aunque no son GraphQL. Use `#` para los
  comentarios y `"` para las cadenas.
- **También se rechaza una cadena de bloque cuyo `"""` de cierre sigue directamente a una barra
  invertida**, como en `\"""`. Ese escape es GraphQL válido, pero las versiones anteriores nunca lo
  leyeron como lo define la especificación: cerraban la cadena en esas tres comillas y leían el resto
  del documento a partir de ahí. Envíe ese texto en una variable.
- **Se rechaza una cadena seguida directamente de una comilla, como `"x""y"`.** GraphQL lo lee como
  dos cadenas contiguas, que nunca son un valor válido; las versiones anteriores lo leían en cambio
  como el comienzo de una cadena de bloque. Solo `"""` abre una cadena de bloque, y la cadena vacía
  `""` no se ve afectada.
- **Por un WebSocket de GraphQL, una suscripción que el servidor no puede leer recibe ahora el error
  de sintaxis**, no el mensaje que dice que solo se aceptan suscripciones. Un documento que nombra
  una operación que no contiene también recibe su propio error. Ambos son errores solo de esa
  operación; la conexión sigue abierta.

[Límites de las solicitudes](../reference/graphql-api.md#request-limits) tiene los detalles.

##### Se elimina el campo `message` de las alarmas {#v0180-alarm-message}

Las alarmas tenían un campo `message` que nada rellenaba nunca: siempre era nulo. Se elimina en
todos los sitios donde aparecía:

- **GraphQL:** desaparecen `Alarm.message` y `AlarmEvent.message` (la suscripción `alarmStream`).
  Una consulta o suscripción que siga seleccionando `message` ahora se rechaza con
  `Cannot query field "message"`. Quítelo de sus propios documentos antes de actualizar.
- **`@devicechain/dashboards` y `@devicechain/widgets`:** `AlarmRow` ya no tiene `message`, así que
  el código que lee `AlarmRow.message` ya no compila. El widget de tabla de alarmas ya no muestra
  una descripción emergente sobre la clave de la alarma, y la vista previa del editor de paneles ya
  no muestra mensajes de alarma inventados. Las versiones de estos paquetes anteriores a esta
  versión siguen seleccionando `message`, así que el servidor actualizado rechaza su lista de
  alarmas y los widgets de alarmas dejan de cargar: actualice los paquetes junto con la plataforma.
- **Notificaciones:** los correos de alarma ya no tienen una línea `Message` y las cargas de los
  webhooks ya no tienen una clave `message`. Ninguna de las dos apareció nunca, porque el valor
  siempre estaba vacío.
- **MCP:** `list_alarms` y `get_alarm` ya no devuelven `message`.
- **Base de datos:** la columna vacía `message` se elimina de la tabla de alarmas cuando arranca
  device-management.

Mientras se despliega la actualización:

- Una pestaña de la consola abierta antes de la actualización, y un pod de la consola, de paneles o
  de MCP que siga en la versión anterior, dan un error en las listas de alarmas hasta que se recarga
  la pestaña o se sustituye el pod.
- Un pod de `device-management` que siga en la versión anterior no puede guardar una alarma nueva.
  La alarma se reintenta aproximadamente una vez por minuto y normalmente la guarda un pod ya
  actualizado. Si los pods de la versión anterior siguen en marcha más de unos cinco minutos, por
  ejemplo porque un pod nuevo nunca llega a estar listo, la alarma se abandona y se registra como
  mensaje no entregado, y solo se genera cuando su condición desaparece y vuelve a darse.
  Mantenga corto el despliegue y, después, revise
  `dcctl dead-letters list --kind detection-action --source device-management` para ver las
  alarmas que no se generaron.
- Las listas de alarmas, y los reconocimientos y borrados de alarmas, que atiende un pod de
  `device-management` que siga en la versión anterior pueden fallar una vez por conexión a la base
  de datos. Repetir la petición funciona.

##### Un duplicado responde ahora con el código `CONFLICT` {#v0180-conflict}

No hay que hacer nada salvo que su propio código o sus scripts reconozcan un duplicado leyendo el
mensaje de error. [Un valor que debe ser único](../reference/graphql-api.md#unique-values) tiene los
detalles.

- **Una creación, actualización o renombrado que repite un valor que debe ser único** lleva ahora
  `extensions.code` igual a `CONFLICT`. Decida por el código. Significa que la escritura chocó con
  un valor único, que no siempre es uno que usted envió: dos publicaciones del mismo registro que
  compiten por el siguiente número de versión también chocan, y entonces un reintento funciona.
- **Se sustituye el texto propio de la base de datos.** Donde un mensaje terminaba en
  `duplicate key value violates unique constraint "…" (SQLSTATE 23505)` o `UNIQUE constraint failed:
  …`, ahora termina en `the request conflicts with an existing record: a value that must be unique is
  already in use`, que no nombra ningún índice ni columna de la base de datos.
- **Los rechazos que ya tenían su propia frase la conservan y ganan el código:** renombrar a un
  token que ya está en uso, añadir una segunda membresía en el mismo inquilino y declarar una clave
  de comando que el perfil ya tiene.
- **`dcctl sim create` reconoce por el código un inquilino, una identidad o una membresía que ya
  existen,** así que volver a ejecutarlo con el mismo nombre termina. Antes, una nueva ejecución se
  detenía en el paso de la membresía. Use un `dcctl` de esta versión con una instancia de esta
  versión: una instancia anterior no envía el código, y este `dcctl` informa entonces del duplicado
  como un error.
- **Estos no son duplicados y no llevan `CONFLICT`:** crear un inquilino con el token reservado de
  un inquilino eliminado, y un guardado rechazado porque el registro cambió desde que se leyó.

##### Las referencias y los valores rechazados responden con un código {#v0180-reference-codes}

No hay que hacer nada salvo que su propio código o sus scripts reconozcan estos rechazos leyendo el
mensaje de error. [Una referencia o un valor que se rechaza](../reference/graphql-api.md#reference-and-invalid-values)
tiene los detalles.

- **Una eliminación rechazada porque otros registros todavía se refieren al registro** lleva ahora
  `extensions.code` igual a `REFERENCE_VIOLATION`: un perfil de dispositivo, un tipo de dispositivo
  u otro tipo todavía en uso, un grupo de entidades al que una regla de detección todavía se
  limita, un nivel de inquilino en el que todavía hay inquilinos, un inquilino que todavía tiene
  membresías, un proveedor de IA que todavía tiene concesiones y un canal de notificación que una
  regla de política todavía nombra. El mensaje no cambia.
- **Se sustituye el texto propio de la base de datos.** Donde un mensaje terminaba en `violates
  foreign key constraint "…" (SQLSTATE 23503)`, ahora termina en `the request refers to a record
  that does not exist, or removes one that other records still refer to` y lleva
  `REFERENCE_VIOLATION`. Un valor que la base de datos rechaza por cualquier otro motivo de
  integridad, como un valor obligatorio que falta, se responde igual con `the request contains a
  value this record does not allow` e `INVALID_VALUE`. Ninguna de las dos frases nombra una tabla,
  una columna ni una restricción, ni repite un valor que usted envió.
- **Ninguno de los dos es `CONFLICT`,** así que ni `dcctl` ni el código que trata `CONFLICT` como
  «ya existe» continúan como si nada. Un rechazo en el que intervienen a la vez un duplicado y uno
  de estos lleva ahora el código nuevo en lugar de `CONFLICT`.
- **El servidor registra cada uno de estos rechazos de la base de datos como una advertencia** que
  nombra la restricción, la tabla y la columna, porque normalmente responde antes la propia
  comprobación del servicio. El detalle, que puede repetir los valores enviados, no se registra.

##### Los niveles de alerta mayores que 2147483647 se rechazan, y cuatro números más ya no se desbordan {#v0180-int-range}

El `level` de una alerta se aceptaba hasta 4294967295, pero se sirve como un `Int` de GraphQL, que
llega hasta 2147483647, así que un nivel mayor se leía como un número negativo. Cuatro campos
tenían el mismo fallo: el número guardado se recortaba a 32 bits al salir, así que un valor mayor
que 2147483647 se convertía en un número negativo verosímil. Cada uno es ahora un error, como los
campos enumerados en [Los valores de las credenciales se guardan exactamente como se
envían](#v0180-credential-values).

- **Una alerta con un `level` mayor que 2147483647 se rechaza ahora al llegar.** El mensaje del
  dispositivo se rechaza como datos erróneos, igual que una alerta sin `type`: HTTP responde `400`
  y una publicación MQTT se registra como mensaje no entregado. No hay nada que cambiar salvo que un
  dispositivo envíe esos niveles.
- **Una alerta ya guardada con un nivel así convierte en error el listado de alertas que la
  incluye.** `level` no puede ser nulo, así que toda la respuesta de `alertEvents` es nula con el
  error, en lugar de faltar una sola alerta. Esto se aplica a las filas guardadas antes de la
  actualización y a las que se guarden mientras sigan en ejecución pods de `event-sources` de la
  versión anterior. La fila permanece hasta que la retención la elimina. Para saber si tiene
  alguna, ejecute esto en la base de datos de event-management:
  `SELECT tenant_id, device_token, count(*) FROM "event-management".alert_events WHERE level > 2147483647 GROUP BY 1, 2;`
- **El `classifier` de una última medición** es el id de la definición de métrica a la que se
  asoció la lectura. Cuando esos ids superan 2147483647, `classifier` es un error solo en ese
  campo: se lee como nulo y el `value`, la `unit` y la hora de la lectura se siguen devolviendo.
  La consola y las herramientas MCP no leen este campo. (El mismo id es una cadena en los eventos
  de medición guardados, donde no tiene límite).
- **El `inactivityTimeout` del estado de un dispositivo** y **el `logoMaxHeight` de la marca de un
  inquilino** solo se escriben con valores que caben, así que esto afecta solo a un valor escrito
  en la base de datos por otra vía. Un `inactivityTimeout` demasiado grande convierte en error todo
  el listado de estados de dispositivo que lo selecciona. Un `logoMaxHeight` demasiado grande hace
  que falle la petición de la consola para el inquilino, así que la consola ya no puede cargar la
  marca, la configuración regional predeterminada ni la configuración de mapa de ese inquilino, y
  puede que el editor de marca no se abra. Para resolverlo, vuelva a fijar la altura del logotipo
  con `setTenantBranding` a través de la API, enviando también el título y los colores: esa
  mutación los reemplaza juntos, así que uno que se omita se borra.

##### `tenantDeletions` en la API de administración está paginada {#v0180-tenant-deletions}

`tenantDeletions` en `/api/user-management/admin/graphql` recibe ahora un argumento de criterios y
devuelve una página, como `auditEvents` y `deadLetters` en la API de administración. Una llamada con
la forma anterior se rechaza.

```graphql
# antes
tenantDeletions(completed: false, limit: 50, offset: 0) { token epoch completedAt }

# ahora
tenantDeletions(criteria: {pageNumber: 1, pageSize: 50, completed: false}) {
  results { token epoch completedAt }
  pagination { totalRecords }
}
```

`pageNumber` y `pageSize` son obligatorios. Un tamaño de página menor que 1 lee 100 registros y uno
mayor que 1000 lee 1000, como en las demás listas de la plataforma. Antes, omitir `limit` leía todo
el historial de eliminaciones. `completed` sigue siendo opcional. Vea [Eliminación de
inquilinos](./tenant-deletion.md#stalled-alert).

##### Las entradas de auditoría nombran la fila que cambiaron {#v0180-audit-rows}

Veinte mutaciones auditadas escribían entradas en el diario de auditoría con la clave y la etiqueta
de fila vacías: cambios de versión de perfiles, grupos y tipos de activo, reclamaciones de
dispositivos, paneles, conectores, proveedores de inferencia y cambios de estado de comandos. Ahora
registran la clave primaria de la fila afectada, y once de ellas también su etiqueta; los cambios de
estado de comandos registran solo la clave. Las entradas escritas antes de la actualización se
quedan como estaban. No hay que hacer nada.

#### Observabilidad {#v0180-observability}

##### Alertas añadidas y renombradas {#v0180-alerts}

Si enruta o silencia alertas por su nombre, estas son las alertas nuevas del chart y la que cambia
de nombre. Cada una enlaza con el apartado que la explica.

| Alerta | Gravedad | Qué indica | Detalles |
| --- | --- | --- | --- |
| `JetStreamStreamNearFull` | warning | Antes `EventProcessingStreamNearFull`; ahora cubre los flujos de todos los servicios | [detalles](#v0180-unread-loss) |
| `JetStreamDurableLostUnread` | critical | Un consumidor perdió mensajes que no había leído | [detalles](#v0180-unread-loss) |
| `JetStreamDurableStalledBehindStream` | critical | Un consumidor detenido está perdiendo mensajes por delante de él | [detalles](#v0180-unread-loss) |
| `JetStreamDurableFallingBehind` | warning | Un consumidor lleva 15 minutos con más de 10000 mensajes esperando | [detalles](#v0180-live-state-batches) |
| `JetStreamReplicationUnobserved` | warning | Un pod no puede leer el estado de replicación de un flujo | [detalles](#v0180-new-warnings) |
| `ReaderHeldMessagePastAckWait` | warning | Un mensaje se retuvo más allá de su ventana de confirmación y se volvió a entregar | [detalles](#v0180-dispatch-backlog) |
| `MaxDeliveryRecordsWaiting` | warning | No se están registrando los avisos de mensajes que agotaron sus intentos | [detalles](#v0180-no-outcome-dead-letters) |
| `ReplayCoveredDeliveriesExhausted` | warning | El punto de control del motor de detección lleva demasiado tiempo sin guardarse | [detalles](#v0180-no-outcome-dead-letters) |
| `RateLimiterOverflowInUse` | warning | La ingesta HTTP comparte una sola asignación entre nombres de inquilino no confirmados | [detalles](#v0180-unconfirmed-tenants) |
| `TenantsMeteredAtPlatformDefault` | warning | Un servicio no puede leer los techos de los inquilinos y los mide con el valor por defecto | [detalles](#v0180-unconfirmed-tenants) |
| `ReactShedLettersOverBudget` | warning | Las acciones descartadas se resumen en lugar de registrarse una a una | [detalles](#v0180-catch-up-metering) |
| `RateMeteringClockFallback` | warning | Las acciones de salida se miden sin su hora de disparo | [detalles](#v0180-catch-up-metering) |
| `ConnectorDispatchRateLimited` | warning | El servicio de conectores descarta envíos que el motor de detección admitió | [detalles](#v0180-new-warnings) |
| `CredentialAttemptStoreFull` | critical | El bucket que cuenta los inicios de sesión fallidos está lleno | [detalles](#v0180-sign-in-limits) |
| `DeviceCredentialAttemptStoreFull` | warning | El bucket que cuenta las conexiones MQTT fallidas con contraseña está lleno | [detalles](#v0180-mqtt-connect-backoff) |
| `DeviceFactPublishFailing` | warning | Fallan las notificaciones de cambios de reglas, dispositivos y atributos | [detalles](#v0180-fact-repair) |
| `DetectFactsRepaired` | warning | El motor de detección corrigió un cambio del que no se le había avisado | [detalles](#v0180-fact-repair) |
| `DetectFactReconcileFailing` | warning | Falla la comparación del motor de detección con device-management | [detalles](#v0180-fact-repair) |
| `TenantPurgeStalled` | warning | Una eliminación de inquilino no avanza | [detalles](#v0180-tenant-purge-alerts) |
| `TenantPurgeVisibilityLost` | warning | Nada informa de si las eliminaciones de inquilinos avanzan | [detalles](#v0180-tenant-purge-alerts) |

`ReactPoisonDropping`, `DeadLetterStoreLosing` y `DeadLetterWriteLost` conservan su nombre y pasan
a su propio grupo de reglas ([detalles](#v0180-dead-letter-rule-group)).

##### Los servicios registran en `info` por defecto, y el nivel se puede fijar {#v0180-log-level}

Todos los servicios registraban en `debug`, se quisiera o no. Ahora registran en `info` por
defecto, y una clave nueva de la configuración de la instancia, `infrastructure.logging.level`, fija
el nivel para toda la instancia. Vea [El nivel de registro](./observability.md#logs).

- **Acepta exactamente `trace`, `debug`, `info`, `warn` o `error`, en minúsculas.** Cualquier otro
  valor, incluidos `INFO` o un número, impide que los servicios arranquen, y el registro nombra la
  clave y los valores aceptados.
- **Con el nivel por defecto desaparecen líneas que quizá buscaba**: las líneas por mensaje de la
  ruta de ingesta, las confirmaciones de lectura y escritura del broker y diagnósticos similares.
  Fije `debug` para volver a verlas. `debug` y `trace` escriben una línea por cada mensaje de
  dispositivo, así que sirven para diagnosticar un problema, no para funcionar.
- **El documento de configuración de un servicio ya no se escribe en el registro** al arrancar. En
  su lugar, el servicio registra `config_sha256`, los primeros 16 caracteres hexadecimales del
  SHA-256 del documento.
- **Una instancia instalada con `dcctl bootstrap` funciona en `info`**, y `dcctl` todavía no tiene
  ninguna opción para cambiar el nivel. Una instalación con el chart lo fija con sus demás valores,
  por ejemplo `--set instance.config.infrastructure.logging.level=debug`. Con
  `instance.existingSecret`, añádalo al documento que proporciona y actualice
  `instance.existingSecretChecksum`.

##### Los mensajes de la base de datos son líneas de registro estructuradas, y una consulta que no encuentra nada ya no se registra como un fallo {#v0180-database-log-lines}

Las versiones anteriores imprimían los mensajes de la base de datos en un formato propio: texto de
varias líneas y en color, escrito fuera del registro JSON del servicio, sin los campos `instance`
ni `area`, fuera cual fuera el valor de `infrastructure.logging.level`. Cada servicio que escribe
datos de inquilinos imprimía uno de esos bloques, con el texto `record not found`, por cada
transacción de base de datos que los escribía; en `event-management` era uno por cada evento
guardado. Ese bloque era la respuesta normal a una comprobación previa a cada escritura, no un
error, y con un ritmo de eventos alto ocupaba la mayor parte del registro y ocultaba fallos reales.

- **Los mensajes de la base de datos son ahora líneas de registro JSON**, en `error` cuando una
  sentencia falló (`database statement failed`) y en `warn` cuando tardó más de 200 ms
  (`slow database statement`), con los campos `sql`, `rows`, `elapsed_ms` y `caller`. Una consulta
  que no encuentra filas ya no se registra como un fallo. Eso incluye la comprobación previa a cada
  escritura.
- **El campo `sql` ya no muestra los valores con los que se envió una sentencia.** Muestra sus
  marcadores (`$1`, `$2`, …). Las versiones anteriores incluían los valores, también en la línea de
  una escritura fallida. El mensaje de error de la propia base de datos se sigue registrando tal
  cual, y algunos citan el valor que rechazaron.
- **`sqlDebug` sigue ahora el nivel de registro.** Sus líneas por sentencia se escriben en `info`,
  así que una instancia con nivel `warn` o `error` ya no las muestra.
- **Un servicio que no puede alcanzar su propia base de datos al arrancar registra cada intento
  fallido en `error`**, como `failed to initialize database, got error …`. Las versiones anteriores
  lo imprimían como texto fuera del registro JSON.

Si buscaba el texto anterior (por ejemplo `record not found` o `SLOW SQL`), busque en su lugar el
campo `message`. No hay nada que configurar.

##### Dos avisos nuevos: un flujo que no se puede leer, y descartes de conectores que el motor de detección admitió {#v0180-new-warnings}

- **`JetStreamReplicationUnobserved`** (warning, grupo `jetstream-replication`) se dispara cuando
  un pod en ejecución lleva 15 minutos sin poder leer el estado de replicación de un flujo que
  antes sí podía leer. Hasta ahora, un servicio que no podía leer un flujo dejaba de informar de
  él, y los demás avisos de replicación se quedaban en silencio para ese flujo en lugar de
  indicarlo. También se dispara para todos los flujos en todos los pods durante una caída del
  bróker, y es deliberado: nada más en el chart informa de una. Se resuelve seis horas después de
  la última lectura del flujo, tanto si se puede volver a leer como si no. Consulte
  [Replicación](./observability.md#replication).
- **`ConnectorDispatchRateLimited`** (warning, grupo `governance`) se dispara cuando
  outbound-connectors lleva 15 minutos descartando envíos por superar la tasa de salida de su
  inquilino. El motor de detección ya descarta las acciones que superan la cuota antes de
  enviarlas, así que esto significa que los dos servicios no coinciden en el techo (lo más
  habitual es que sus valores por defecto de la plataforma sean distintos) o que se reintentan
  envíos que fallan y se vuelven a medir. Antes de actualizar, compruebe que
  `outboundMessagesPerSecond` y `outboundBurst` tienen el mismo valor para event-processing y
  outbound-connectors. Si no, se dispara cada vez que un inquilino medido con el valor por defecto
  de la plataforma envía más rápido que el menor de los dos. Consulte
  [Inquilinos medidos con el valor por defecto de la plataforma](./observability.md#tenant-ceilings).
- **`JetStreamLeaseBucketNotReplicated` tiene un resumen nuevo**, "The partition-lease bucket is
  not replicated". Su nombre, etiquetas y severidad no cambian. Actualice las rutas o los
  silencios que filtren por el texto del resumen anterior.

##### Las alertas de mensajes no entregados pasan a su propio grupo de reglas {#v0180-dead-letter-rule-group}

Las tres alertas de mensajes no entregados, `ReactPoisonDropping`, `DeadLetterStoreLosing` y
`DeadLetterWriteLost`, se distribuyen ahora en su propio PrometheusRule `dead-letter` (grupo
`devicechain.dead-letter`) en lugar de en `event-processing`. Sus nombres, etiquetas, severidades,
umbrales y descripciones no cambian, así que las rutas y los silencios que filtran por el nombre de
la alerta o por sus etiquetas siguen funcionando.

- **Una alerta pendiente o disparada durante la actualización vuelve a empezar.** Prometheus ve una
  regla nueva, así que la alerta se resuelve y vuelve cuando ha pasado su espera `for` (5 o 10
  minutos), si la condición se mantiene.
- **Si selecciona objetos PrometheusRule por nombre**, o busca grupos de reglas en la interfaz de
  Prometheus, añada `dead-letter`.
- **`DeadLetterStoreLosing` y `DeadLetterWriteLost` ya no terminan en `or vector(0)`.** Una
  expresión que no devuelve nada y una que devuelve una comparación falsa dejan la alerta en el
  mismo estado, así que la cláusula no cambiaba nada. Ambas alertas se disparan y se resuelven
  exactamente igual que antes.

##### Dos avisos para una eliminación de inquilino que no avanza {#v0180-tenant-purge-alerts}

Una eliminación de inquilino que no puede terminar deja sanas las métricas de pasada del propio
coordinador, así que el chart genera ahora alertas directamente sobre ella, en un grupo de reglas
nuevo, `devicechain.tenant-purge` (PrometheusRule `tenant-purge`):

- **`TenantPurgeStalled`** (warning) se dispara cuando la eliminación abierta más antigua lleva
  abierta más del doble de la retención de identificador configurada (24 horas con la retención por
  defecto) durante 15 minutos.
- **`TenantPurgeVisibilityLost`** (warning) se dispara cuando los indicadores de eliminación de
  user-management llevan 15 minutos sin recogerse, de modo que la primera alerta no podría
  dispararse.

user-management exporta los dos indicadores que leen,
`devicechain_usermanagement_tenant_purge_in_flight` y
`devicechain_usermanagement_tenant_purge_oldest_age_seconds`. Un `tokenHoldSeconds` de `0` se lee
como el valor por defecto, igual que lo lee el servicio. Vea [Cómo te
enteras](./tenant-deletion.md#stalled-alert).

##### Las tareas de mantenimiento informan de cada pasada, y los barridos ya no se ejecutan con un intervalo exacto {#v0180-maintenance-passes}

Diez tareas de mantenimiento, los barridos, reconciliadores y planificadores que se ejecutan con un
temporizador, exportan ahora tres series cada una: `devicechain_<area>_<task>_passes_total{outcome}`,
`…_pass_duration_seconds` y `…_last_success_timestamp_seconds`. El resultado es `complete`,
`partial`, `failed`, `skipped` o `cancelled`. `skipped` (otra réplica tiene el bloqueo de la tarea)
y `cancelled` (el servicio se estaba deteniendo) no son fallos, y una pasada omitida no mueve la
hora del último éxito. [Pasadas de mantenimiento](./observability.md#maintenance-passes) enumera las
tareas.

Cinco de ellas, el barrido de mensajes no entregados y el coordinador de eliminación de inquilinos
de user-management, el barrido de retención y el planificador de escalados de
notification-management, y el barrido de anclas de event-management, empiezan ahora cada pasada en
un momento aleatorio dentro de un 10 % a cada lado de su intervalo, en lugar de exactamente en él,
de modo que las réplicas que arrancaron juntas ya no llegan a la base de datos en el mismo
instante. No hay nada que configurar.

#### Despliegue y dimensionamiento {#v0180-deployment}

##### device-management y event-management pueden usar más CPU {#v0180-cpu-limits}

`device-management` y `event-management` pueden usar ahora hasta 2 núcleos de CPU cada uno; los
demás servicios de backend mantienen el límite de 500m. Con 500m, `device-management` resolvía como
máximo unos 720 eventos por segundo en un clúster de pruebas de cuatro nodos, por debajo de los 1000
mensajes por segundo que cada inquilino tiene permitidos por defecto, así que un inquilino que
enviaba a ese ritmo acumulaba un retraso que crecía mientras siguiera enviando. `event-management`
necesita alrededor de medio núcleo a ese ritmo, todo su límite anterior, así que habría sido el
siguiente límite. Consulte [Dimensionamiento de los servicios](./bootstrap.md#service-sizing).

La actualización reinicia esos dos servicios una vez, pod a pod. Las solicitudes no cambian, así que
ningún pod necesita más espacio en un nodo, y las instancias `--compact` conservan sus solicitudes
reducidas.

- **El bloque `functionalAreas.<servicio>.resources` de un servicio se combina ahora clave a clave
  sobre los `resources` de nivel superior,** en lugar de sustituirlos. El `frontend.resources` de la
  consola no cambia. Revise los pods generados antes de actualizar si sus valores definen
  `resources` para un solo servicio:
  - Si definía solo `requests` para un servicio, no tenía límites y ahora recibe los de nivel
    superior. Para que un servicio siga sin límite, quite ese límite de los `resources` de nivel
    superior y defínalo en los servicios que lo necesiten.
  - Si definía solo `limits` para un servicio, Kubernetes le daba solicitudes iguales a esos
    límites; ahora recibe las solicitudes de nivel superior (100m y 128Mi, o 25m y 64Mi con
    `--compact`), que reservan menos en el nodo y pueden cambiar la clase QoS del pod. Defina las
    `requests` de ese servicio para conservar su reserva.
  - Si su bloque para `device-management` o `event-management` no define `limits.cpu`, ese servicio
    recibe ahora el nuevo límite de 2 núcleos.
  - Una solicitud por encima del límite combinado se rechaza al generar el chart, indicando el
    servicio; por ejemplo, un servicio que define solo `requests.memory: 512Mi` frente al límite de
    256Mi de nivel superior. Defina también su límite.
  - Se rechaza cualquier clave distinta de `requests`, `limits` o `claims` bajo los `resources` de
    un servicio.
- **Un `resources.limits.cpu` de nivel superior por encima de 2 núcleos ya no llega a
  `device-management` ni a `event-management`,** porque su nuevo límite de 2 núcleos es propio y la
  clave propia de un servicio prevalece sobre la de nivel superior. Si sus valores suben solo el
  límite de CPU de nivel superior, por ejemplo a 4, la actualización BAJA estos dos servicios a 2
  núcleos. Defina `functionalAreas.device-management.resources.limits.cpu` y
  `functionalAreas.event-management.resources.limits.cpu` para conservar lo que tenían. Por la misma
  razón, un `resources.requests.cpu` de nivel superior por encima de 2 núcleos, que antes se
  generaba, ahora se rechaza, indicando como límite superado el propio del servicio; suba también
  ese límite.
- **Un `ResourceQuota` de espacio de nombres sobre `limits.cpu`, o un `LimitRange` con un `max` de
  CPU, puede rechazar los dos pods** ahora que sus límites son más altos. Nada de lo que instala
  DeviceChain crea ninguno de los dos; revise los que haya añadido.
- **Para conservar los límites anteriores,** defina
  `functionalAreas.device-management.resources.limits.cpu` y
  `functionalAreas.event-management.resources.limits.cpu` en `500m`. Eso restaura el techo anterior
  de unos 720 eventos por segundo.

##### El almacén de respaldos dentro del clúster se descarga de una imagen mantenida {#v0180-backup-store-image}

El almacén de objetos que `dcctl install` ejecuta en `dc-system` para guardar los respaldos de las
bases de datos descargaba su imagen de MinIO de `quay.io/minio/minio`. Esas imágenes ya no se
publican: el registro rechaza una descarga anónima, de modo que en una máquina que no tuviera ya la
imagen en caché, `dcctl install` se detenía con el almacén en `ImagePullBackOff`. Los clústeres
que ya tenían la imagen en caché siguieron funcionando, pero solo mientras el pod del almacén
permaneciera en un nodo que la tuviera: un pod trasladado a otro nodo —por un drenaje, un desalojo
o un nodo sustituido— no podía arrancar, y el archivado se detenía hasta que pudiera. Actualizar
elimina esa exposición.

Esta versión descarga `cgr.dev/chainguard/minio`, fijada por digest: una compilación de una
bifurcación mantenida del mismo servidor MinIO, que sigue bajo licencia AGPL-3.0. Lee tal cual los
datos que escribió el servidor anterior.

- **Antes de actualizar, asegúrese de que sus nodos pueden descargar de `cgr.dev`**: permítalo en
  sus reglas de salida, o replique `cgr.dev/chainguard/minio` con el digest que fija esta versión.
  Si la descarga falla, el almacén sigue caído y las bases de datos conservan localmente su registro
  de escritura anticipada hasta que vuelva.
- **Una instalación nueva vuelve a funcionar** con el destino de respaldo predeterminado.
- **En un clúster existente, `dcctl install` reinicia una vez el pod del almacén** con la nueva
  imagen. Los respaldos y el registro de escritura anticipada guardados se conservan. Mientras el
  pod se
  reinicia, el archivado se detiene y las bases de datos retienen el registro localmente, durante el
  tiempo que la nueva imagen tarde en descargarse y arrancar.
- **Esto no se deshace instalando una versión anterior.** El `dcctl install` de una versión
  anterior detendría el almacén y después no podría descargar la imagen que indica, y el archivado
  se detendría hasta volver a ejecutar el `dcctl install` de esta versión.
- **Si `dcctl install` falló por esto en una versión anterior**, vuelva a ejecutar el
  `dcctl install` de esta versión: sustituye el almacén que dejó aquel intento (consulte [Un `dcctl install` fallido puede volver a
  ejecutarse](#v0180-dcctl-install-rerun)).

No hay nada que configurar. Si dirige los respaldos a su propio almacén de objetos con
`--backup-credentials-file`, no cambia nada para usted.

##### Un `dcctl install` fallido puede volver a ejecutarse {#v0180-dcctl-install-rerun}

Si `dcctl install` fallaba mientras arrancaba el almacén de objetos de los respaldos, por ejemplo
porque no se podía descargar su imagen, o se interrumpía en ese punto, todas las ejecuciones
posteriores fallaban también, antes de cambiar nada, con `Unexpected Identity Change` sobre
`kubernetes_deployment_v1`. Ocurría con Terraform 1.12 o posterior, y la única salida era borrar el
clúster y su directorio en `~/.devicechain/clusters/`. Ahora una nueva ejecución elimina el almacén
a medio crear, lo crea de nuevo y espera a que esté listo. Un clúster que ya estaba bloqueado así se
recupera del mismo modo, sin ningún paso manual, una vez corregida la causa: ejecute de nuevo el
mismo `dcctl install`. Si la causa sigue ahí, la nueva ejecución falla del mismo modo en lugar de
dar el clúster por instalado.

- **`dcctl install` comprueba ahora que el almacén de objetos de los respaldos ha terminado su
  despliegue** antes de dar el clúster por instalado. Un cambio en el almacén que agotaba su tiempo,
  como una imagen nueva que no se podía descargar, lo aceptaba la ejecución siguiente, porque el
  cambio fallido ya había quedado registrado. Esa ejecución espera ahora hasta cinco minutos al
  almacén y después falla indicándolo.
- **El proveedor de Kubernetes pasa de 2.38.0 a 3.2.1.** La corrección está en esa versión. El
  primer comando de dcctl que aplica o destruye infraestructura en cada clúster lo descarga. No
  cambia nada en el clúster.
- **Los proveedores de infraestructura quedan fijados a versiones exactas** (Kubernetes 3.2.1,
  Helm 2.17.0), y dcctl lleva el `.terraform.lock.hcl` de cada raíz a ellas en cada ejecución.
  Antes, cada clúster conservaba las versiones que resolvió su primera instalación. Cada
  ejecución que aplica o destruye infraestructura, incluida `dcctl destroy`, consulta ahora esas
  versiones al registro de proveedores,
  así que el registro, o un espejo de proveedores que haya configurado, debe ser accesible. El
  proveedor TLS, que no se usaba, ya no se declara.
- **Un dcctl anterior no puede operar un clúster sobre el que se ha ejecutado esta versión.** Su
  `init` rechaza el archivo de bloqueo, que ahora indica versiones de proveedor que su
  configuración no admite.
- **Si ejecuta la configuración de OpenTofu directamente** en lugar de a través de dcctl, ejecute
  `tofu init -upgrade` una vez en cada raíz: un `init` normal rechaza un archivo de bloqueo con las
  versiones anteriores.

##### Los archivos de la versión llevan una procedencia de compilación firmada {#v0180-provenance}

Desde `v0.18.0`, cada versión publica `devicechain_<version>_provenance.sigstore.json` (la versión
sin su `v`, p. ej. `devicechain_0.18.0_provenance.sigstore.json`) junto a los
archivos de `dcctl` y `dc-edge-agent`: un registro firmado de la ejecución del flujo de trabajo que
los compiló. Para comprobar un archivo que ha descargado:

```bash
gh attestation verify <archive> --repo devicechain-io/devicechain
# o sin conexión, contra el paquete de la versión
gh attestation verify <archive> --repo devicechain-io/devicechain \
  --bundle devicechain_0.18.0_provenance.sigstore.json
```

No hay que hacer nada.

### v0.24.0 — métricas calculadas que puedes crear tú mismo, y una ruta de eventos que transporta más {#v0240-upgrade}

`v0.24.0` es una actualización en su sitio desde `v0.23.0`: `dcctl install` para el clúster y después
`dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](#v0170-upgrade). Pasa a
`dcctl install` los ajustes con los que se instaló el clúster, como `--ha` y `--database-node-selector`,
además de la nueva `--version`. **No se puede alcanzar en su sitio desde la `v0.22.0` ni versiones
anteriores: actualiza primero a la `v0.23.0`.** Sus cuatro migraciones añaden dos tablas y dos índices
y no reescriben ninguna fila existente (consulta [Durante la actualización](#v0240-during)). **Desde ella
se avanza, no se vuelve atrás:** la `v0.23.0` no puede arrancar con la configuración que escribe esta
versión (consulta [Volver atrás](#v0240-going-back)). Si vienes de
la `v0.22.0` o anterior, lee también las [notas de la `v0.23.0`](#v0230-upgrade); se aplican a cualquier
actualización que cruce esa versión.

Los comandos, con la `v` en la versión y un ensayo primero:

```bash
dcctl install local --version v0.24.0
dcctl upgrade local devicechain --version v0.24.0 --dry-run
dcctl upgrade local devicechain --version v0.24.0
```

`--version` lleva la etiqueta con su `v`. En un clúster que no sea el local por defecto, pasa
`--kube-context` a `dcctl install` y a `dcctl upgrade`, y pasa `--registry` cuando las imágenes salgan de
un registro distinto del predeterminado. Si algún dispositivo MQTT sigue enviando una credencial en el
cuerpo de sus eventos, añade `--mqtt-body-credential=strip` al último comando: consulta
[Antes de actualizar](#v0240-before).
`dcctl upgrade --dry-run` no imprime el valor de `--mqtt-body-credential`, así que confirma que surtió efecto
tras la actualización (consulta el procedimiento en
[Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT](#v0240-mqtt-body-credential)). Si la
instancia se inicializó sin custodia de la clave raíz, `dcctl upgrade` imprime una advertencia de custodia; es lo
esperado y no tiene relación con esta actualización.

Esta versión convierte las métricas calculadas en algo que creas y rellenas tú mismo: un editor de
fórmulas con comprobación, constantes por dispositivo, fórmulas que leen otras métricas calculadas,
vistas previas y un **relleno** que calcula una métrica para las lecturas almacenadas antes de que
existiera. Lo demás trata de una ruta de eventos que transporta más: los eventos se escriben por defecto
en una forma más pequeña, las detecciones se publican en paralelo y los cambios de alarma se aplican
varios a la vez. También cierra dos sitios donde un secreto estaba al alcance de todos los servicios de la
plataforma: la credencial de un dispositivo en el cuerpo de un evento MQTT y el estado de inicio de
sesión. Qué cambia para alguien:

- **Los dispositivos deben dejar de enviar una credencial en el cuerpo de un evento publicado por el
  bróker MQTT de la plataforma.** Ese evento se rechaza ahora. Para que una flota siga informando mientras
  se actualiza su firmware, actualiza con `--mqtt-body-credential=strip`. Consulta
  [Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT](#v0240-mqtt-body-credential).
- **`IDeviceEventCarrier` del SDK de C# tiene un miembro abstracto nuevo,** `CarriesBodyCredential`; un
  portador propio deja de compilar hasta que lo declara. Consulta la misma sección.
- **Todos vuelven a iniciar sesión una vez, y el bróker se reinicia dos veces.** El estado de inicio de
  sesión pasa a una cuenta propia del bróker y no se traslada, y el callout de autenticación de
  dispositivos se cifra. Consulta [Los inicios de sesión del bróker se acotan](#v0240-broker-logins).
- **Actualiza pasando por la `v0.23.0`.** Los eventos se escriben ahora por defecto en la forma
  `compact`, que la `v0.22.0` y anteriores no pueden leer. Consulta
  [El formato de los eventos en el cable](#event-wire-format).
- **El bróker pasa a NATS 2.15.1** en el primero de esos reinicios. Consulta
  [El bróker pasa a NATS 2.15](#v0240-nats).
- **No hay vuelta atrás a la `v0.23.0`.** Avanza: un `helm rollback` deja servicios que no pueden
  arrancar. Consulta [Volver atrás](#v0240-going-back).
- **Cuatro migraciones:** una tabla para los trabajos de relleno en `event-management`, una tabla para el
  registro de activaciones de perfil y un índice en `device-management`, y un índice en
  `command-delivery`. Consulta [Durante la actualización](#v0240-during).
- **`device` es un nombre reservado en las fórmulas de métricas calculadas.** Consulta
  [Métricas calculadas](#v0240-calculated-metrics).
- **Cambia el id de un valor calculado para una lectura JSON que no envía su propia hora.** Consulta
  [Métricas calculadas](#v0240-calculated-metrics).
- **Se rechaza una medición numérica que sea NaN, infinita o demasiado grande.** Consulta
  [Mediciones](#v0240-measurements).
- **Se mueven algunos valores por defecto:** el motor de detección publica 32 detecciones a la vez,
  `device-management` aplica los cambios de alarma en varios carriles, el endpoint MCP limita el tamaño
  de un lote y a cada llamante, y el tipo de máquina del grupo de servicios del módulo de referencia
  de GKE es otro. Consulta [Valores por defecto que cambiaron](#v0240-defaults).

#### Antes de actualizar {#v0240-before}

1. **Si vienes de la `v0.22.0` o anterior, detente y actualiza primero a la `v0.23.0`.** La `v0.22.0` y
   las anteriores leen solo la forma de cable `legacy`. Un pod de una de ellas descarta un mensaje
   `compact`, o lo aparta como fallido en `device-management`, así que una actualización progresiva
   directa a la `v0.24.0` pierde los eventos escritos mientras conviven pods viejos y nuevos. Actualiza
   cada instancia a la `v0.23.0`, espera a que todos los pods la ejecuten y después actualiza a la
   `v0.24.0`. En una instalación con el chart (no con `dcctl`) puedes en cambio indicar
   `infrastructure.messaging.wireFormat: legacy` durante la actualización y quitarlo cuando todos los pods
   se hayan renovado. `dcctl` no tiene ninguna opción para indicarlo.
2. **Si algún dispositivo MQTT envía una credencial en el cuerpo de sus eventos, actualiza con
   `--mqtt-body-credential=strip`.** Con `deviceAuthMode: required` son todos los dispositivos MQTT,
   porque la `v0.23.0` rechaza un evento MQTT que no lleva credencial en el cuerpo. Por eso el firmware no
   se puede actualizar primero: un dispositivo actualizado antes de la actualización ve rechazados sus
   eventos hasta que esta termina. Ejecuta
   `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=strip` y sigue los pasos de
   [Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT](#v0240-mqtt-body-credential).
   **No indiques `deviceAuthMode: optional` para salvar el intervalo.** Hace que la instancia acepte un
   evento HTTP sin credencial, solo con el token del dispositivo.
3. **Si un `terraform.tfvars` junto al estado de infraestructura de la instancia fija
   `nats_chart_version`, súbelo a `2.15.1` o elimínalo.** Un chart de NATS anterior a `2.15.0` se rechaza
   al elaborar el plan.
4. **No cambies la replicación del bróker (`streamReplicas`, `nats_cluster_replicas`) en la misma
   actualización.** Mientras los servidores se reinician, unos ejecutan `2.14.4` y otros `2.15.1`, y un
   cambio de réplicas o de ubicación iniciado en ese intervalo puede quedarse atascado. Hazlo antes o
   después. Consulta [El bróker pasa a NATS 2.15](#v0240-nats).
5. **Si una fórmula publicada de una métrica calculada lee como identificador simple una métrica medida
   cuya clave es `device`, reescríbela** como `double(m["device"])` (o la conversión de su tipo) antes de
   actualizar. Tras la actualización esa fórmula ya no compila y no produce valor en ninguna lectura. Las
   métricas calculadas están desactivadas hasta que el `calculatedMetricsPerProfile` de un nivel supera 0,
   así que esto solo afecta a los niveles en los que ya lo hayas subido.
6. **Si instalaste tú mismo el módulo `nats`, mantén `jetstream.limits.default_max_consumers` en
   `-1`.** NATS 2.15, si no, limita cada stream a 1.000 consumidores, y la suscripción MQTT QoS 1 o 2
   número 1.001 se rechaza mientras su dispositivo sigue conectado.

#### Durante la actualización {#v0240-during}

- **Se ejecutan cuatro migraciones, en el primer arranque del servicio al que pertenece cada una.**
  Ninguna escribe ni reescribe filas, y cada una se puede volver a ejecutar si se interrumpe.
  - `event-management` crea la tabla `calculated_backfills` y sus índices.
  - `device-management` crea `device_profile_activations`, el registro de qué versión de un perfil pasó a
    estar activa y cuándo (empieza vacío: no se reconstruye nada de las activaciones anteriores a la
    actualización), y un índice sobre `devices (tenant_id, device_type_id)` que atiende las pestañas por
    tipo de dispositivo de la tabla de alarmas.
  - `command-delivery` crea `idx_commands_device_time`, que atiende las marcas de los gráficos que
    muestran los comandos de un dispositivo en su eje de tiempo.

  Cada índice se construye con un `CREATE INDEX` simple, que mantiene un bloqueo contra escrituras en la
  tabla mientras se ejecuta; en una tabla con millones de filas, arranca ese servicio en un momento
  tranquilo.
- **El bróker se reinicia, y en la primera actualización que añade el callout cifrado se reinicia dos
  veces.** El primer reinicio lleva el paso a NATS 2.15.1 y la nueva cuenta de inicio de sesión; el
  segundo activa el cifrado del callout cuando los servicios ya se han renovado a un respondedor que
  puede leerlo (una instancia sin callout de autenticación de dispositivos no tiene nada que cifrar y se
  reinicia una vez). Con `--ha`, de un servidor en otro, cada uno Ready antes de que se detenga el siguiente; sin `--ha` el único servidor se reinicia
  y el bróker no está disponible durante aproximadamente un minuto cada vez. Consulta
  [Los inicios de sesión del bróker se acotan](#v0240-broker-logins). Los dispositivos se vuelven a conectar solos, y una sesión MQTT persistente
  conserva sus suscripciones y los mensajes QoS 1 retenidos para ella. Los dispositivos se vuelven a conectar
  sin perder mensajes solo con `--ha`. En `kind` con `--ha` (dos reinicios
  progresivos del bróker), la actualización tardó de 4 a 5 minutos en total: unos 90 segundos el primero, unos
  40 segundos sin actividad y unos 80 segundos el segundo. **No reinicies a mano un servidor del bróker mientras avanza.** Consulta [El bróker pasa a NATS 2.15](#v0240-nats).
- **Los servicios empiezan a escribir eventos `compact` a medida que se renuevan.** Los pods de la
  `v0.23.0` leen ambas formas, así que una actualización progresiva entre las dos versiones es segura.
- **Revocar la autoridad de un dispositivo durante la renovación puede fallar, o dejar una sesión
  abierta.** Deshabilitar o eliminar un dispositivo, o cambiar una credencial, termina ahora también las
  sesiones MQTT abiertas del dispositivo pidiéndoselo a `event-sources`, y solo responde un pod de
  `event-sources` de esta versión. Mientras no haya ninguno en marcha, el cambio se guarda y la llamada
  devuelve un error («the change was saved, but the device's live broker sessions could not be
  confirmed ended ... repeat the operation»). Un cambio que atiende un pod de `device-management` de la
  versión anterior se guarda y no termina ninguna sesión, como antes de la actualización. Repite esos
  cambios cuando termine la renovación; repetirlos es seguro y vuelve a preguntar.
- **Un pod de la versión anterior que se reinicia durante la renovación no vuelve.** Antes de que se
  muevan los servicios, `dcctl` escribe la configuración de la instancia para esta versión, y la
  `v0.23.0` la rechaza al arrancar (consulta [Volver atrás](#v0240-going-back)). Un pod de la `v0.23.0`
  desalojado, terminado por falta de memoria o que pierde su nodo en ese intervalo sigue caído hasta que
  la nueva versión lo sustituye, así que deja que la actualización termine en lugar de reiniciar pods a
  mano.
- **Un inicio de sesión o una renovación de token que llegue a un pod de `user-management` de la versión
  anterior puede fallar durante unos segundos** mientras se traslada el estado de inicio de sesión. Sale
  bien al reintentarlo.
- **Un valor derivado almacenado por un pod de la versión anterior y otro almacenado por un pod de la
  nueva pueden llevar ids distintos** para una lectura cuyo instante tiene dígitos por debajo del
  microsegundo. Consulta [Métricas calculadas](#v0240-calculated-metrics).

#### Volver atrás {#v0240-going-back}

**Avanza. No vuelvas a la `v0.23.0`, ni con `helm rollback` ni de ninguna otra forma.** Si la
actualización falla a medias, corrige la causa y vuelve a ejecutar `dcctl upgrade` con la misma
`--version`. Volver atrás deja una instancia que no funciona:

- **Los servicios de la `v0.23.0` no pueden arrancar.** `dcctl` escribe la configuración de la instancia
  en el Secret `dci-<instance>-config`, fuera de la release del chart, así que `helm rollback` restaura
  las cargas de trabajo de la release anterior y deja la configuración tal como la escribió esta
  versión. Esa configuración lleva claves que la `v0.23.0` no conoce, como `identityAccount`,
  `calloutIssuerPublic` y `calloutXKeyPublic`, y la `v0.23.0` rechaza al arrancar cualquier clave de su
  configuración que no conozca. Todos los servicios que arranca fallan, y la semilla de firma del
  callout ya ni siquiera está en esa configuración.
- **Los dispositivos no pueden conectarse.** El bróker no forma parte de la release del chart, así que
  sigue en NATS 2.15 y sigue cifrando su callout de autenticación de dispositivos. Las semillas del
  callout están en un Secret que la `v0.23.0` no lee, así que no hay ningún respondedor que pueda
  atender las peticiones del bróker, y todos los dispositivos se rechazan.
- **El bróker tampoco puede volver atrás.** NATS solo admite volver de 2.15 a 2.14.7 o posterior, y la
  versión anterior ejecuta 2.14.4.
- **No se restaura el estado de inicio de sesión.** Los buckets de inicio de sesión de la versión
  anterior se eliminaron, así que todos tendrían que volver a iniciar sesión.

Las tablas y los índices nuevos no estorbarían: la `v0.23.0` lee la misma base de datos y los deja sin
usar. Volver a la `v0.22.0` o anterior exige indicar antes `legacy` y que se hayan vaciado de los
streams todos los mensajes `compact` (consulta [El formato de los eventos en el cable](#event-wire-format)).

#### Después de la actualización {#v0240-after}

- **No hace falta nada.** Todos los ajustes nuevos tienen un valor por defecto.
- **El paso del bróker es irreversible.** NATS solo admite volver de 2.15 a 2.14.7 o posterior, y la
  versión anterior ejecuta 2.14.4.
- **Mantén `metrics.enabled` y `metrics.alerts` activados** (lo están por defecto) para instalar la nueva
  alerta `MqttGatewayStreamUnbounded`.
- **Localiza los dispositivos que aún envían una credencial en el cuerpo.**
  `devicechain_eventsources_total_msg_credential_in_mqtt_body` cuenta los eventos rechazados por origen y, con
  `strip`, los que se eliminaron. Actualiza el firmware o el SDK de cada uno y rota la credencial de todo
  dispositivo que se haya contado (consulta la sección siguiente).

##### Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT {#v0240-mqtt-body-credential}

Un dispositivo en el bróker MQTT de la plataforma se autentica al conectarse, mediante el callout de
autenticación de dispositivos. El contrato publicado hacía además que repitiera su credencial en el
cuerpo de cada evento, y el bróker escribía ese cuerpo en sus propios streams antes de que se ejecutara
ningún código de la plataforma, de modo que una contraseña o un token de acceso en texto claro quedaba en
un stream legible por todos los servicios de la plataforma, por cualquiera con acceso al volumen y por sus
instantáneas. Ahora la credencial en el cuerpo se rechaza:

- **Se rechaza un evento publicado por el bróker MQTT de la plataforma cuyo cuerpo lleve
  `credentialType`, `credentialId` o `credentialSecret`,** salvo que hayas indicado
  `capture.bodyCredential: strip` para la actualización (más abajo). Se cuenta en
  `devicechain_eventsources_total_msg_credential_in_mqtt_body`, se aparta en el stream failed-decode con la credencial censurada y
  no se reenvía nunca. El bróker ya ha confirmado el mensaje, así que el rechazo lo ves tú y no el
  dispositivo: un dispositivo que sigue enviando el cuerpo antiguo se queda en silencio mientras cree que
  publica.
- **Se acepta un evento sin credencial** y se atribuye al dispositivo que se conectó. Con
  `deviceAuthMode: required` se acepta para todos los tipos de evento.
- **La ingesta HTTP y un bróker MQTT que conectas como origen no cambian.** Siguen exigiendo la
  credencial en el cuerpo.
- **Trata como expuesto a un dispositivo que fue rechazado.** El texto claro existió en el bróker durante
  los instantes previos a la confirmación del mensaje, en el almacén del gateway MQTT mientras había una
  suscripción interesada y en bloques de archivo sin recuperar hasta la compactación. Rota la credencial
  de todo dispositivo que se haya contado.
- **Actualiza con `strip`, actualiza los SDK y el firmware y pasa después a `refuse`.** La `v0.23.0`
  rechaza un evento MQTT que no lleva credencial en el cuerpo, y esta versión rechaza uno que sí la lleva,
  así que una flota no puede actualizar su firmware antes de la actualización. El puente es
  `capture.bodyCredential: strip`, que acepta un evento del bróker MQTT de la plataforma con la credencial
  eliminada. En este orden:
  1. Actualiza con `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=strip`.
     Los eventos siguen fluyendo y se cuenta cada uno que llevaba una credencial. El `--dry-run` del comando
     no imprime el valor de este indicador; para confirmar que la actualización lo tomó, busca la advertencia de
     arranque de `event-sources` que dice que el ajuste es transitorio, o comprueba que
     `devicechain_eventsources_capture_body_credential_strip` vale `1`.
  2. Despliega el SDK o el firmware que deja de enviar la credencial en el cuerpo.
  3. Vigila `devicechain_eventsources_total_msg_credential_in_mqtt_body` hasta que deje de subir durante
     más tiempo que el intervalo de envío de tu dispositivo más lento. **Recopila la lista de dispositivos
     sobre la marcha.** Una advertencia en el registro de `event-sources` indica el inquilino y el dispositivo
     de cada uno que sigue enviando una credencial (una vez por hora para cada dispositivo, en cada pod de
     `event-sources`); copia esas parejas inquilino-dispositivo del registro durante esta fase, porque es el
     único lugar donde se nombran.
  4. Ejecuta `dcctl upgrade local devicechain --version v0.24.0 --mqtt-body-credential=refuse`. A partir de
     aquí el registro de `event-sources` no nombra ningún dispositivo y la métrica no tiene etiqueta de
     dispositivo. El único registro por dispositivo que queda es el stream de decodificaciones fallidas, que
     conserva el nombre del dispositivo con la credencial censurada.
  5. Rota la credencial de todo dispositivo de la lista que recopilaste en el paso 3 (y de los que el stream de
     decodificaciones fallidas nombre después del paso 4).

  `strip` no elimina la exposición: el bróker guardó el cuerpo antes de que se aplicara cualquiera de los
  dos ajustes, así que `strip` y `refuse` dejan el mismo texto claro en reposo, y el paso 5 hace falta con
  ambos. `strip` se aplica solo al bróker MQTT de la plataforma. Un `dcctl upgrade` posterior sin la opción
  conserva el valor. Es transitorio: está previsto eliminarlo en la `v0.26.0`, que no dejará arrancar
  `event-sources` mientras siga indicado. En una instalación con el chart (no con `dcctl`) indica
  `functionalAreas.event-sources.config.capture.bodyCredential`.

  Mientras la actualización se despliega, un pod de `event-sources` de esta versión puede quitar la
  credencial de un evento antes de que `device-management` se haya actualizado. Un `device-management` de
  la `v0.23.0` rechaza ese evento, porque ya no lleva credencial, y lo deja para que se entregue de nuevo un
  minuto después, hasta cinco entregas en total; un `device-management` de esta versión lo acepta. Así que
  no se pierde nada si `device-management` termina de actualizarse en unos cuatro minutos desde el primer
  pod nuevo de `event-sources`, como ocurre en una actualización normal. Un evento que siga rechazado en su
  quinta entrega queda apartado en el flujo de eventos fallidos como no autenticado, sin la credencial.
- **Qué cambiar en un dispositivo.** El SDK de C# (y el paquete de Unity, que se apoya en él) deja de
  enviar la credencial por MQTT en esta versión, y la sigue enviando por HTTP. El JSON Schema del sobre
  marca ahora los tres miembros como solo HTTP. Un `IDeviceEventCarrier` propio debe declarar el nuevo
  miembro `CarriesBodyCredential` (`true` para un portador que va por HTTP, `false` para el MQTT de la
  plataforma); es un error de compilación, a propósito, en lugar de un valor por defecto. El firmware que
  escribe el cuerpo a mano debe quitar los tres miembros. El agente de borde reenvía los cuerpos sin
  cambios, y su enlace ascendente a un bróker de la plataforma que autentica dispositivos no es
  compatible en esta versión.
- **Una sesión ya no sobrevive a su credencial.** El token de la conexión caduca ahora con la credencial
  (lo que ocurra antes entre 12 horas y la caducidad de la propia credencial), y revocar una credencial,
  rotar su secreto, deshabilitar o eliminar un dispositivo o sustituirlo termina las sesiones MQTT
  abiertas del dispositivo. El dispositivo se vuelve a conectar y se autentica de nuevo con la credencial
  que presenta, que la base de datos rechaza si se revocó. Terminan todas las sesiones del dispositivo,
  así que revocar una de dos credenciales corta un momento la sesión de la otra. Si la plataforma no puede
  confirmar que las sesiones terminaron, el cambio se guarda igualmente y la llamada devuelve un error
  que lo indica; repítela.
- **Rota los tokens de los dispositivos de token de acceso cuyos mensajes se rechazaron o pasaron a
  mensajes fallidos.** Los registros de failed-decode y de dead-letter conservaban el `credentialId` de un
  token de acceso, que en ese tipo de credencial es el propio token (en MQTT_BASIC es el nombre de inicio
  de sesión). Ahora se censuran, pero nada reescribe un registro escrito por una versión anterior: esos
  streams caducan a los 7 días, la tabla de dead-letters la limpia su barrido de retención y una copia de
  seguridad de la base de datos los conserva hasta que caduca.
- **`event-sources` necesita el inicio de sesión de la cuenta de sistema del bróker para arrancar**
  siempre que el bróker ejecute el callout de autenticación de dispositivos (`dcctl bootstrap` lo crea), y
  ese inicio de sesión queda limitado a listar y terminar conexiones. `dcctl upgrade` rechaza una
  instancia que tiene el callout y no tiene inicio de sesión de sistema, en lugar de dejar a
  `event-sources` en un bucle de fallos.
- **El inicio de sesión de servicio compartido ya no puede publicar en el subject de eventos de un
  dispositivo.** Quien lo tuviera podía antes falsificar un evento de cualquier dispositivo con una sola
  publicación. No es una frontera frente a quien posee el inicio de sesión, que sigue administrando los
  streams de los servicios.

Consulta [Conexión de un dispositivo](../guides/connecting-a-device.md),
[Credenciales de dispositivo](../guides/device-credentials.md) y el
[referencia del protocolo de dispositivo](../reference/device-protocol.md).

##### Los inicios de sesión del bróker se acotan {#v0240-broker-logins}

Dos secretos que estaban al alcance de todos los servicios están ahora al alcance de uno solo. El
mecanismo y los reinicios se describen en
[Qué aplica una actualización a la infraestructura](#upgrade-infrastructure); qué cambia para alguien:

- **El callout de autenticación de dispositivos se cifra.** La contraseña de un dispositivo cruzaba el
  bróker en claro hacia cualquier servicio que escuchara. `dcctl upgrade` crea el Secret
  `dci-<instance>-callout` (la semilla de firma y una nueva semilla de cifrado), montado solo en
  `device-management`. La configuración de la instancia conserva las mitades públicas. Una instalación
  solo con el chart debe crear ese Secret por su cuenta.
- **El estado de inicio de sesión pasa a una cuenta propia del bróker, y todos vuelven a iniciar sesión
  una vez.** Los tokens de actualización, los códigos de autorización OAuth, los registros de inicio de
  sesión único y el limitador de inicios de sesión pasan a una cuenta `IDENTITY` a la que solo puede
  entrar `user-management`, con la contraseña en el Secret `dci-<instance>-identity`. Las copias de la
  versión anterior se **eliminan, no se trasladan**, así que termina toda sesión anterior a la
  actualización: la gente inicia sesión de nuevo en la consola y en los paneles, y los clientes MCP y OAuth
  vuelven a autorizar. Un token de acceso ya emitido funciona hasta que caduca, 15 minutos por defecto.
  Una instalación solo con el chart debe añadir la cuenta a su propio bróker, o no cambia nada.
- **El bróker se reinicia dos veces en la primera actualización que hace ambas cosas**, como se describe
  más arriba. Los dispositivos se vuelven a conectar en segundos con `--ha` y en aproximadamente un minuto
  sin él. Si falla el segundo paso, los dispositivos siguen conectándose; ejecuta `dcctl upgrade` otra vez
  para terminarlo.
- **La semilla de firma se traslada, no se sustituye.** La semilla de firma del callout estaba en la
  configuración de la instancia, y `dcctl upgrade` lleva esa misma semilla al nuevo Secret. `dcctl`
  mantiene la configuración fuera de los valores de Helm, así que su historial de releases no guarda
  ninguna copia. Una instalación con el chart que pasa la configuración en línea sigue teniendo la
  semilla en las releases de Helm guardadas antes de la actualización; elimina esos Secrets de release si
  eso te importa.
- **Una instancia restaurada recibe un inicio de sesión nuevo para el estado de inicio de sesión,** y ese
  estado no se restaura desde una copia de seguridad.
- **En el clúster, esos Secrets son la única copia de las semillas.** La máquina que ejecutó
  `dcctl bootstrap` también las guarda en `~/.devicechain/instances/<instance>/`. No hay verbo de
  rotación, y editar
  cualquiera de los dos Secrets fuera de `dcctl` no está admitido. Quien pueda leer Secrets en el
  espacio de nombres de la instancia puede seguir leyéndolos.

##### El bróker pasa a NATS 2.15 {#v0240-nats}

El bróker de mensajería de la instancia pasa de NATS `2.14.4` a `2.15.1` (el chart oficial `nats`,
misma versión). `dcctl upgrade` lo aplica en su primera aplicación del bróker, la que también añade la
cuenta de inicio de sesión, así que el paso no cuesta un reinicio propio: con `--ha` sus servidores se
reinician de uno en uno, cada uno Ready antes de que se detenga el siguiente; sin `--ha` el único
servidor se reinicia y el bróker no está disponible durante aproximadamente un minuto. El segundo
reinicio, que activa el cifrado del callout, llega después de los servicios (consulta
[Durante la actualización](#v0240-during)). Los
dispositivos se vuelven a conectar solos, y una sesión MQTT persistente conserva sus suscripciones y
los mensajes QoS 1 retenidos para ella mientras el dispositivo no estaba.

- **No cambies la replicación del bróker en la misma actualización.** Mientras los servidores se
  reinician, unos ejecutan `2.14.4` y otros `2.15.1`. NATS 2.15 cambió la forma en que un stream o
  un consumidor cambia su número de réplicas o su ubicación, y solo NATS `2.14.7` y posteriores
  entienden el nuevo mecanismo. Esta actualización parte de `2.14.4`, así que un cambio de réplicas
  o de ubicación iniciado mientras algún servidor todavía lo ejecuta puede quedarse atascado, no
  solo retrasado. Los streams se pueden leer y escribir durante todo el proceso, y crearlos o
  eliminarlos no se ve afectado. Sube `streamReplicas` o `nats_cluster_replicas` antes o después de
  esta actualización, no durante.
- **No reinicies a mano un servidor del bróker mientras avanza la actualización.** La nueva
  configuración llega a todos los servidores a la vez, pero cada servidor pasa a `2.15.1` solo
  cuando le toca. Hasta entonces sigue funcionando con la configuración con la que arrancó, y si se
  reinicia en ese intervalo (a mano, o porque falla su nodo) no puede arrancar con la nueva
  configuración y queda caído hasta que la actualización lo reemplaza. Con `--ha` eso deja al
  clúster con un servidor menos durante el resto del proceso, y los streams replicados dejan de
  aceptar escrituras mientras el siguiente servidor se reinicia.
- **Se rechaza un chart de NATS anterior a `2.15.0`.** La configuración del bróker está escrita ahora
  para NATS 2.15 y un servidor 2.14 la rechaza al arrancar, así que un `nats_chart_version` inferior
  a `2.15.0` hace fallar el plan en lugar de dejar el bróker en un bucle de reinicios. Si un
  `terraform.tfvars` junto al estado de infraestructura de la instancia fija `nats_chart_version`,
  súbelo a `2.15.1` o elimínalo antes de actualizar. Considera el paso a 2.15 como irreversible: NATS
  solo admite volver de 2.15 a 2.14.7 o posterior, y la versión anterior ejecuta 2.14.4.
- **Se levanta el límite de 1.000 consumidores que NATS 2.15 añade a cada stream.** Cada
  suscripción MQTT con QoS 1 o 2 es un consumidor en el stream de mensajes propio del bróker, así que
  con el nuevo valor por defecto se rechazaría la suscripción número 1.001 de ese tipo en toda la
  flota mientras su dispositivo seguía conectado, y ese dispositivo nunca recibiría un comando. Se
  cuentan suscripciones, no dispositivos: un dispositivo con dos suscripciones QoS 1 usa dos. Si
  instalas tú mismo el módulo `nats`, mantén `jetstream.limits.default_max_consumers` en `-1`.

Qué mejora:

- **Las publicaciones QoS 1 de una misma conexión MQTT ya no se esperan unas a otras.** El bróker
  almacena ahora hasta 1.024 mensajes QoS 1 de una conexión a la vez, en lugar de uno en uno. En un
  clúster de prueba de tres servidores, una conexión que enviaba 5.000 mensajes pasó de unos 1.100 a
  unos 20.000 confirmados por segundo.
- **Recargar la configuración del bróker ya no detiene la entrega QoS 1 y 2 a los dispositivos ya
  conectados.** Con TLS en el listener MQTT, que es el valor por defecto, cada recarga volvía a
  aplicar los ajustes MQTT y dejaba cada sesión conectada sin poder recibir mensajes QoS 1 o 2,
  comandos incluidos, hasta que se reconectaba. Una renovación de certificado podía provocar una.
- **Correcciones en el inicio de sesión de dispositivos y los permisos**, entre ellas cómo decide el
  bróker qué inicios de sesión omiten la comprobación de dispositivo, y un rechazo de inicio de
  sesión que ahora se informa como error de autenticación.
- **El bróker local del agente de borde ya no rechaza la suscripción QoS 1 o 2 número 1.001 de un
  sitio.** `dc-edge-agent` integra NATS 2.15, y desde `v0.20.0` hasta `v0.23.0` su listener MQTT
  local tenía el mismo límite de 1.000 consumidores: respondía a la suscripción QoS 1 o 2 número
  1.001 de un sitio con un rechazo mientras el dispositivo seguía conectado. El límite se levanta
  también allí. Actualice el agente en cualquier sitio cuyos dispositivos mantengan entre todos más
  de 1.000 suscripciones de ese tipo en su listener.

##### Métricas calculadas {#v0240-calculated-metrics}

Las métricas calculadas siguen desactivadas hasta que el `calculatedMetricsPerProfile` de un nivel supera
0 (se introdujo en la `v0.23.0`). Novedades de esta versión, allí donde estén activadas:

- **Creación desde la consola.** La pestaña Métricas del perfil de dispositivo tiene un selector **Tipo**
  (Medida o Calculada) y un editor de **Fórmula** con comprobación. El borrador se compila con el mismo
  compilador que usan la puerta de publicación y el resolvedor, y muestra su coste frente al techo de 100.
  La [guía de usuario](../user-guide/calculated-metrics.md) le dedica un capítulo.
- **Constantes numéricas por dispositivo.** `device.calibration` o `device["ct-ratio"]` leen un número de
  los metadatos del dispositivo. Solo cuenta un número JSON; una cadena, un booleano, null, un objeto o
  una lista está presente pero no es utilizable y nunca se convierte. **`device` es ahora un nombre
  reservado**: una métrica medida con la clave `device` se lee como `m["device"]`, con su conversión de
  tipo. Una fórmula publicada que leía esa métrica como identificador simple ya no compila: no produce
  valor en ninguna lectura, se cuenta como `eval_error` en
  `devicechain_devicemanagement_calcmetric_skipped_total` y se informa en el registro de
  `device-management`. Un valor que el dispositivo envíe bajo la clave de esa métrica calculada se sigue
  descartando.
- **Encadenamiento.** Una fórmula puede leer otra métrica calculada, hasta cuatro niveles. Un anillo
  (`a -> b -> a`) o una cadena de más de cuatro se rechaza al publicar, indicando la cadena.
- **Vistas previas.** `previewRule` acepta las métricas del perfil en borrador y vuelve a derivar los
  valores calculados con ese borrador, y un panel **Pruébalo con el historial reciente** de la consola
  evalúa una fórmula en borrador sobre las lecturas almacenadas de un dispositivo.
- **Relleno.** Un trabajo que calcula una métrica calculada para las lecturas almacenadas antes de que se
  publicara su versión y escribe los valores que faltan. Consulta [Relleno del historial](#v0240-backfill).
- **Las métricas de dispositivos Sparkplug y LwM2M ahora se calculan.** Un dispositivo que envía una
  métrica por entrada (Sparkplug, LwM2M o un lote JSON) omitía toda fórmula de varias entradas como
  `missing_input`, porque cada entrada era una lectura por separado. Ahora una lectura es cada medición
  de un mismo mensaje que lleva exactamente el mismo instante, de modo que `power = voltage * current`
  se calcula cuando ambas comparten instante. Sparkplug y LwM2M sellan al milisegundo, así que un nodo que
  sella cada métrica por separado (valores con unos milisegundos de diferencia), o que solo informa de lo
  que cambió, sigue sin obtener valor en una fórmula de varias entradas. Si una métrica aparece dos veces
  en un instante, la fórmula lee la primera. Las lecturas LwM2M de un mismo paquete SenML comparten ahora
  un único instante de recepción. La guía de usuario describe
  [cómo se forma una lectura](../user-guide/calculated-metrics.md#what-a-calculated-metric-is).
- **Dos muestras calculadas del mismo nombre en el mismo instante conservan una sola fila almacenada**, la
  primera escrita.
- **El id de un valor derivado sigue ahora a su instante en UTC con precisión de microsegundo.** Las
  lecturas cuyo instante es exacto al microsegundo (todas las de Sparkplug y LwM2M, y las lecturas JSON
  que envían su propia hora con hasta seis decimales) conservan los mismos ids. Una lectura con dígitos
  por debajo del microsegundo recibe un id derivado nuevo, lo que incluye toda lectura JSON que no envía
  su propia hora, porque la plataforma le pone la hora de recepción con nanosegundos. Si esa misma lectura
  se almacena de nuevo tras la actualización, porque el almacenamiento de eventos la recibió de nuevo (aún no
  estaba confirmada cuando los pods se renovaron) o el dispositivo reenvió una lectura anterior, su valor derivado se almacena como una segunda fila. Las
  filas sin procesar no se ven afectadas y las filas antiguas no se reescriben.
- **Un mensaje que no lleva ninguna de las entradas de una fórmula deja de contarse como omisión.** Una
  fórmula de batería registraba una omisión `missing_input` por cada mensaje de temperatura de un
  dispositivo que envía ambas magnitudes en mensajes separados (unas 3.600 por hora con un mensaje por
  segundo). Ese mensaje simplemente ya no se evalúa, y un mensaje que lleva algunas de las entradas pero
  no todas sigue contando como `missing_input`. Tras la actualización,
  `devicechain_devicemanagement_calcmetric_skipped_total` con `missing_input` y las líneas de registro
  correspondientes bajan, así que un panel o una alerta basados en ello leerán menos. No cambia ningún
  valor almacenado.
- **Las fórmulas que leen una constante de dispositivo ahora se calculan con lecturas en vivo de
  dispositivos que se autentican con una credencial** (inicio de sesión MQTT o token de acceso, por
  cualquier transporte). Se omitían como `missing_input` en cada una de esas lecturas, mientras que la
  vista previa de la consola, que toma las constantes de la petición, funcionaba. Una constante que
  edites llega a la siguiente lectura.
- **En la consola,** la pestaña Versiones del perfil muestra, para la versión activa, si cada métrica
  calculada está calculándose, no se está calculando o no tiene lecturas de entrada, en las últimas 24
  horas o 7 días, contado a partir de las lecturas almacenadas; el relleno se deshabilita de antemano, con
  el motivo, cuando la instancia no puede ejecutarlo (`calculatedMetricBackfillAvailability`); y
  `calculatedMetricPlan` marca una métrica cuyo nombre también usa la versión activa de otro perfil
  (`nameSharedWithOtherProfiles`).
- **Un perfil de dispositivo registra qué versión pasó a estar activa y cuándo.**
  `DeviceProfile.activations` lista las publicaciones y los retrocesos, de la más reciente a la más
  antigua. El registro empieza en la actualización; un perfil publicado antes no tiene entrada hasta su
  próxima publicación o retroceso, y una entrada ausente significa desconocido, no inactivo.

##### Relleno del historial {#v0240-backfill}

Un relleno añade una métrica calculada al historial almacenado antes de que se publicara la versión que
la define, de modo que los gráficos y las exportaciones del pasado también la muestren. Está disponible
allí donde hay métricas calculadas y no necesita nada para activarse salvo una versión publicada de un
perfil que tenga una métrica calculada.

- **Iniciar y cancelar un relleno exige `device:write`; leer y listar los trabajos exige `event:read`.**
  Un llamante limitado a clientes es rechazado por las cuatro operaciones. La consola lo inicia desde la
  pestaña Versiones del perfil y lo ofrece una vez cuando publicas una versión que añade una métrica
  calculada.
- **Un trabajo tiene límites.** Como máximo 90 días por trabajo, que terminen como muy tarde ahora, sobre
  una versión publicada de un perfil, para hasta 1.000 dispositivos nombrados o todos los del perfil. Un
  intervalo más antiguo de lo que permite la retención se rechaza en lugar de acortarse. Hay un trabajo
  activo (en cola o en ejecución) por tenant.
- **Solo añade valores y es silencioso.** Un valor almacenado nunca cambia. Un valor rellenado aparece en
  paneles, historial y exportaciones; no cambia los últimos valores de un dispositivo y las reglas de
  detección nunca lo ven.
- **Una lectura que contiene una misma entrada dos veces con valores distintos se omite como
  `ambiguous`**, mientras que el cálculo en vivo lee la primera. El trabajo cuenta cada omisión por motivo.
- **El historial comprimido se omite** en esta versión (el almacén de eventos comprime por defecto los
  fragmentos de más de 7 días), y el trabajo cuenta lo que omitió.
- **Un trabajador por instancia ejecuta los trabajos, a un ritmo acotado.** `calculatedBackfill.enabled`
  en la configuración de `event-management` tiene tres estados. Sin definir, el valor por defecto, el
  trabajador se ejecuta cuando el pool de conexiones tiene sitio para él junto a los escritores de
  persistencia y se queda apagado, con un aviso al arrancar que nombra los ajustes, cuando no lo tiene,
  de modo que una instancia afinada antes de esta versión arranca como lo hacía. `true` detiene el
  servicio al arrancar si el pool no puede alojar al trabajador. `false` apaga el trabajador, y entonces
  iniciar un relleno se rechaza como no disponible, con el motivo. `calculatedBackfill.rowsPerSecond` es
  20000 por defecto, de 1 a 200000, y ese valor es provisional. Consulta
  [Relleno de métricas calculadas](./observability.md#calculated-backfill), que también lista las métricas
  y el SQL que cancela un trabajo atascado, y [Rellenar el historial](../user-guide/calculated-metrics.md#backfilling-history)
  en la guía de usuario.

##### Mediciones: se rechazan NaN, infinito y valores enormes {#v0240-measurements}

Una medición numérica que sea NaN, infinita o de 1.000.000.000.000 o más en magnitud se rechaza ahora al
resolver el evento.

- Para una métrica **declarada en el perfil**, se rechaza todo el evento como no válido. Antes, un valor de
  1e12 o más ya se perdía del historial (la columna lo rechazaba); ahora además se mantiene fuera del
  estado en vivo y de la evaluación de reglas, y NaN ya no se cuela por los valores Mín. y Máx. de una
  métrica.
- Para una **clave no declarada**, solo se descarta ese valor, con un aviso en el registro de
  `device-management`. Antes, fallaba el almacenamiento de todo el evento.
- Un valor NaN o infinito ya no hace que el almacenamiento de eventos reintente el mensaje durante unos
  cinco minutos (cinco entregas) y lo informe después como un fallo de un componente posterior.

##### Valores por defecto que cambiaron {#v0240-defaults}

| Ajuste | Antes | Ahora | Restaurar el comportamiento anterior |
| --- | --- | --- | --- |
| `infrastructure.messaging.wireFormat` | `legacy` | `compact` | `legacy` (solo en instalación con el chart; consulta [Antes de actualizar](#v0240-before)) |
| `event-processing` `derivedPublishing.maxInFlight` (nuevo) | una detección cada vez | 32 | Indica `1`. Sigue habiendo como máximo una detección pendiente por dispositivo, así que las detecciones de cada dispositivo llegan en orden. Volver a publicar sin duplicados se mantiene dentro de la ventana de duplicados de 30 minutos del stream de eventos derivados. |
| `device-management` `raiseAlarm.workers` (nuevo, fijo, se lee al arrancar) | un cambio cada vez | `max(1, min(8, pool - resolution.workers - 4))`, es decir, 6 con el pool por defecto de 20 | Indica `1` para la ruta en línea anterior. Los cambios de una misma alarma se aplican siempre en orden. Un valor explícito se valida contra el pool de conexiones y nunca se ajusta. |
| `mcp` `maxBatchEntries`, `callsPerSecond`, `callsBurst` (en `functionalAreas.mcp.config`) | sin límite | 16, 20 y 40 | Súbelos; `callsBurst` no puede ser menor que `maxBatchEntries`. |
| `event-sources` `capture.bodyCredential` (nuevo, fijo, en `functionalAreas.event-sources.config`) | ninguno: se aceptaba una credencial en el cuerpo en el bróker MQTT de la plataforma | `refuse` | Indica `strip` solo para la actualización (transitorio, está previsto eliminarlo en la `v0.26.0`). Consulta [Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT](#v0240-mqtt-body-credential). |
| `services_machine_type` del módulo de referencia de GKE (`deploy/gke`) | `n2-custom-4-8192` | `t2d-standard-4` | Indícalo explícitamente. Cambiar el tipo de máquina de un grupo existente recrea sus nodos. |

- **Detección:** las detecciones de distintos dispositivos se publican en paralelo, hasta 32 a la vez, en
  lugar de esperar cada confirmación. En un clúster de pruebas de GKE con una carga de disparos intensa de unas
  2.500 detecciones por segundo ofrecidas, una ventana de 32 publicó 2.441 detecciones por segundo frente a
  528 con una ventana de 1, y el retraso de la marca de agua fue de 5,1 s en lugar de 139 s. El
  presupuesto de bytes `derivedPublishing.maxBufferedBytes` cuenta ahora todas las cargas preparadas que
  aún no se han confirmado. Consulta [la página del motor de detección](./detection-engine.md#configuration).
- **Alarmas:** `device-management` aplicaba cada cambio de raise-alarm en su bucle de lectura, unos cien
  por segundo en una base de datos local, por inactivo que estuviera el servicio. Ahora los aplica en
  `raiseAlarm.workers` carriles, y los cambios de una alarma van al mismo carril. En una prueba local con
  8 carriles fueron entre cuatro y siete veces más. El valor por defecto deja libres cuatro conexiones del
  pool junto a los resolvedores; ambos grupos comparten el pool de conexiones, así que dimensiónalos
  juntos. Consulta [Qué tan rápido se aplican las alarmas](./detection-engine.md#how-fast-alarms-are-applied).
- **MCP:** un lote que supera el tope se rechaza entero (HTTP 400, petición no válida de JSON-RPC, no se
  ejecuta nada), y un llamante que supera la tasa recibe HTTP 429 con `Retry-After`. Un lote de N cuenta
  como N mensajes, y el llamante es el sujeto del token verificado (tenant y correo). Consulta
  [los límites del servidor MCP](../concepts/mcp.md#limits-and-boundaries).
- **GKE:** en una instalación HA 3+3, los nodos de servicios T2D persistieron un 18 % más de eventos por
  segundo (unos 17.200 frente a 14.500 y 14.700, con 18.000 ofrecidos por HTTP), y el p99 de la
  confirmación MQTT QoS 1 a 9.000 eventos por segundo fue de 38 ms frente a 578 y 171 ms. La alternativa
  `n2d-custom-4-8192` equivale a la forma N2 dentro del ruido y es aproximadamente un 13 % más barata por
  nodo. `T2D_CPUS` es una cuota propia, y no todas las zonas ofrecen T2D (`us-east4-a`, `-b` y `-c` sí). El
  grupo de bases de datos sigue siendo `n2-standard-4`.

##### Confirmación MQTT con fallo cerrado {#v0240-mqtt-failclosed}

`infrastructure.nats.mqttFailClosed` lo acepta ahora la configuración (la `v0.23.0` lo rechazaba) y
**sigue desactivado por defecto**: nada cambia a menos que lo actives. Con él activado, un PUBACK QoS 1
significa que la plataforma tiene el mensaje. El almacén de mensajes MQTT del propio bróker (`$MQTT_msgs`)
se declara acotado y con descarte de lo nuevo, de modo que un almacén lleno rechaza una publicación QoS 1
(el dispositivo no recibe PUBACK y la reenvía) en lugar de descartar una que ya había confirmado.
`event-sources` reenvía desde el almacén, y el almacén se traspasa entre pods de forma segura durante una
actualización progresiva. Un almacén casi lleno aparta a los dispositivos: `device-management` rechaza las
conexiones MQTT nuevas en cuanto llega al 80 % y hasta que baja del 60 %, lo que un dispositivo
ve como CONNACK con código 5 (no autorizado), así que el firmware debe seguir reintentando ante él. El
ajuste se rechaza en un bróker que sirve también a otra instancia.

Límites, descritos en las páginas de [observabilidad](./observability.md#mqtt-fail-closed) y de la
[matriz de transportes](../reference/transport-matrix.md): QoS 0 nunca se almacena; un fallo del bróker con
una acumulación grande no está cubierto; los PUBACK son más lentos con él activado (p99 de 336 ms a 6.000
eventos por segundo en una prueba, frente a 35 ms con él desactivado); y el margen de memoria cerca de
10.000 eventos por segundo es escaso en ambos modos, así que sube `nats_memory_limit` si un servidor se
detiene por falta de memoria.

##### Los streams del gateway MQTT quedan acotados en una instalación nueva {#v0240-gateway-streams}

En una instalación nueva de la `v0.22.0` o la `v0.23.0`, los streams `$MQTT_msgs`, `$MQTT_out` y
`$MQTT_qos2in` del gateway MQTT los creaba el bróker sin límite de tamaño cuando se conectaba el primer
dispositivo MQTT, y seguían sin límite hasta que `event-sources` se reiniciaba. Un `$MQTT_msgs` sin límite
puede llenar el volumen del bróker. Ahora cada pod de `event-sources` comprueba estos streams cada 30
segundos y fija el techo, así que en una instancia existente en ejecución quedan acotados en cuanto
arrancan los pods nuevos. Una alerta nueva, `MqttGatewayStreamUnbounded` (aviso, 10 minutos), se dispara
cuando no se pudo fijar el techo, normalmente porque el bróker no tiene almacenamiento libre para
reservarlo. Métricas nuevas: `devicechain_eventsources_mqtt_gateway_stream_unbounded{stream}` y
`devicechain_eventsources_mqtt_gateway_stream_bounded_late_total{stream}`.

#### Qué más cambió {#v0240-what-changed}

##### Limitaciones conocidas {#v0240-known-limitations}

- **Heredada de la `v0.23.0`:** con `mqttFailClosed` desactivado, un cambio de líder del stream de captura
  de MQTT con mucha carga puede perder eventos QoS 1 ya confirmados sin contarlos
  (`JetStreamCaptureLeaderChanged` avisa). El techo documentado de MQTT es de unos 6.000 eventos por
  segundo en tres nodos de 4 vCPU, limitado por el bróker.
- **Un fallo del bróker con una acumulación grande no lo cubre `mqttFailClosed`.** En una prueba de carga
  de 9.000 eventos QoS 1 por segundo, antes de acelerar el lector del almacén, dos de los tres servidores
  NATS se quedaron sin memoria y cerca de un tercio de los eventos confirmados no llegó nunca al almacén
  de eventos. No se ha establecido dónde se perdieron. El ajuste es opcional y está desactivado por
  defecto.
- **El relleno omite el historial comprimido,** y su ritmo por defecto (`rowsPerSecond`) no se ha medido
  en una instancia con carga.

##### También en esta versión {#v0240-also}

- **`dcctl upgrade` comprueba el montaje del almacén de objetos antes de registrar la versión nueva.** Un
  almacén ReadWriteOnce que montaría un segundo pod se rechaza antes de registrar la versión, como ya
  hacía `--enable-area`. El mensaje «recorded binding» ya no nombra dos veces el contexto.
- **Una acción de regla `sendCommand` puede acotar cuánto espera su comando al dispositivo.** El
  `expiresAfter` opcional admite una duración de `1m` a `168h`; sin él, el comando espera los 7 días por
  defecto como antes. Un valor fuera de rango, o que no se puede leer, se rechaza al guardar o publicar la
  regla, y una regla almacenada con un valor no válido se descarta (se cuenta con el motivo
  `invalid_action`) en lugar de enviarse sin límite.
- **Paneles y widgets.** La tabla de alarmas puede agrupar sus alarmas en pestañas por tipo de
  dispositivo, cada una con su propio recuento y su mayor severidad; la tarjeta de estadística puede
  mostrar una minigráfica de tendencia; una nueva tarjeta de recuento de flota cuenta los dispositivos de
  un grupo, o los que requieren atención, y deja fuera los dispositivos cuyo perfil no declara la
  medición; una tarjeta de estado del dispositivo muestra si un dispositivo está en línea, con cuándo se
  vio por última vez, y un anillo de conexión divide un grupo en en línea, sin conexión y sin estado
  todavía (ambos leen el estado de los dispositivos que conoce la plataforma y requieren permiso para
  leer el estado de los dispositivos); el control deslizante de comandos tiene
  un modo de pasos; un clic en un marcador del mapa selecciona el dispositivo, y un enlace puede abrir un
  panel con un dispositivo enlazado; un desglose de severidad de alarmas muestra las alarmas activas por
  severidad; el gráfico de series temporales puede marcar alarmas y comandos en su eje de tiempo, limitado
  a las alarmas sobre la medición que dibuja con la opción `markAlarmsScope` (un gráfico nuevo empieza
  limitado; un gráfico guardado sin la opción marca todas las alarmas del dispositivo), y dibujar una
  línea por dispositivo de un grupo con `groupBy: device` (5 líneas por defecto, 12 como máximo); el editor de widgets ofrece las métricas de un dispositivo y marca las calculadas; y
  **Create dashboard** en la pestaña Perfil de un tipo de dispositivo crea un borrador de panel a partir
  de lo que declara el perfil. Todo es aditivo: un panel guardado se lee y se dibuja como antes. Consulta
  la [guía de usuario](../user-guide/dashboards.md).
- **Consola: los formularios ya no guardan sobre la entidad equivocada.** Tras cambiar de registro,
  inquilino, nivel, rol o dispositivo, un formulario podía conservar los datos de la entidad anterior y
  guardarlos sobre la nueva. Ahora se separan los datos que se muestran de los datos sobre los que se
  actúa, en todas las consultas de la consola.
- **GraphQL.** La mutación de relleno de posición `_empty` de `event-management` desaparece, porque el servicio ya
  tiene una mutación real (iniciar y cancelar un relleno). Un cliente que llamara a `_empty` recibe un
  error de campo desconocido.
- **Panel de detección.** Dos paneles de latencia (p50 y p99 de la escritura de salud de las reglas y de
  la confirmación de publicación derivada) se suman al panel de escritura de salud de las reglas.
- **Una tarea que falla una y otra vez ya no llena el registro.** La línea «Maintenance pass failed» de
  una tarea periódica, como el tick de detección que se ejecuta cada segundo, se registra una vez al
  empezar una racha de fallos, después como mucho una vez por minuto con un recuento de las líneas
  retenidas, y una vez cuando la tarea se recupera. Cada origen Sparkplug lleva su propia racha, así que
  un origen que falla no se esconde tras uno sano. Siete bucles que no registraban duración, resultado ni último éxito de sus pasadas
  ahora lo hacen, en las series existentes `devicechain_<area>_<task>_passes_total`.
- **El acceso a streams y buckets se declara por servicio.** Cada servicio solo puede abrir los streams
  y buckets clave-valor que tiene declarados para leer o escribir, y rechaza la llamada, nombrando el
  stream y la solución, si pide otro. Un arranque del bróker abandonado por quien lo llamó (apagado) ya
  no gasta aproximadamente un minuto por stream reintentando.
- **Salud de las reglas.** Los contadores de disparos de la salud de las reglas se agrupan fuera del bucle
  de detección (unos 2,6 ms ahorrados por detección publicada en PostgreSQL), y el panel de detección
  gana un panel para `detect_rule_stat_write_failures_total` y la nueva
  `detect_rule_stat_admission_contended_total`.
- **Propiedad de dispositivos.** Las horas de los hechos de propiedad las da el reloj de la base de datos,
  y un dispositivo que vuelve a ponerse en cola una y otra vez conserva su lugar en la cola, así que ya
  no puede quedar relegado indefinidamente.
- **Un consumidor duradero de fallo cerrado que se vuelve a crear empieza en la primera secuencia del
  almacén,** de modo que un consumidor eliminado no deja mensajes confirmados sin leer (solo con
  `mqttFailClosed` activado). La ventana de duplicados del stream de destino absorbe una repetición y, más
  allá de ella, se almacena de nuevo.
- **Usuarios de cliente.** Un miembro limitado a algunos clientes vuelve a ver su lista de Actualizaciones,
  y la pestaña Usuarios del cliente, con más de 10.000 miembros, muestra los primeros 10.000 con un aviso
  en lugar de un error.
- **Go 1.27.3.** La cadena de herramientas pasa a Go 1.27.3, que incluye la corrección de un aviso de
  `crypto/x509`.
- **Sustituir la clave raíz de la instancia significa recrear la instancia.** No hay verbo de rotación;
  consulta [Recuperación ante desastres](./disaster-recovery.md#replace-root-key-losses).
- **Compilar desde el código fuente.** El SDK de C# se compila ahora con el SDK de .NET fijado en
  `global.json` (8.0.425), así que una restauración bloqueada ya no se rompe cuando una imagen de runner
  cambia de parche.
- **Documentación.** Un capítulo de la guía de usuario sobre métricas calculadas (en inglés, español y
  chino), y las opciones de custodia de la guía de inicio rápido, la extracción de la CA de MQTT y las
  pruebas locales con TLS.

### v0.23.0 — usuarios de cliente con acceso acotado, y una ingesta MQTT que pierde menos {#v0230-upgrade}

`v0.23.0` es una actualización en su sitio desde `v0.22.0`: `dcctl install` para el clúster y después
`dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](#v0170-upgrade). Pasa a
`dcctl install` los ajustes con los que se instaló el clúster, como `--ha` y `--database-node-selector`,
además de la nueva `--version`: se niega a cambiar los ajustes de un clúster mientras una instancia se
ejecuta en él. A diferencia de la [`v0.22.0`](#v0220-upgrade), **sí ejecuta migraciones**: siete servicios añaden columnas,
índices o tablas, y `user-management` sustituye el índice único de los tokens de rol por dos, uno para
el catálogo del operador y otro para los roles propios de cada tenant. Ninguna reescribe filas existentes, los únicos rellenos registran los tokens de los
clientes y grupos que ya existen, y cada una se puede volver a ejecutar sin riesgo si se interrumpe.
Si vienes de la `v0.21.x` o anterior, lee primero las [notas de la `v0.22.0`](#v0220-upgrade); se
aplican a cualquier actualización que cruce esa versión. Una instancia creada por la `v0.16.0` o
anterior sigue teniendo que [destruirse y crearse de nuevo](#pre-declaration-recreate).

Esta versión añade los **usuarios de cliente**: miembros de tu tenant limitados a algunos de tus
clientes o a grupos de dispositivos, que ven solo ese equipo, con roles que define tu tenant e
invitaciones que envías tú mismo. Casi todo lo demás trata de no perder eventos y de no activar la
alarma equivocada: el stream que guarda los eventos de los dispositivos MQTT hasta que `event-sources`
los reenvía se vacía con el doble de profundidad y tiene un espacio que puedes dimensionar, una alarma
de umbral sigue la lectura más reciente del dispositivo, y un cambio de propietario de un dispositivo
llega a device-state aunque un pod muera en el peor momento. Qué cambia para alguien:

- **Crece el techo del stream de captura de MQTT.** Estaba limitado a 256 MiB y ahora toma el techo
  completo de los streams calientes, de modo que un volumen de JetStream dimensionado con poco margen
  según la cifra anterior puede dejar a `event-sources` sin poder arrancar. Consulta
  [Crece el techo del stream de captura](#v0230-capture).
- **Un bróker en clúster (`--ha`) reinicia sus servidores de uno en uno, una vez**, para activar la
  compresión entre ellos. Consulta [Compresión de rutas del bróker](#v0230-route-compression).
- **Las métricas calculadas se calculan**, pero siguen desactivadas hasta que subas el
  `calculatedMetricsPerProfile` de un nivel por encima de 0. No lo hagas durante el despliegue.
  Consulta [Métricas calculadas](#v0230-calculated-metrics).
- **Las alarmas de umbral siguen la lectura más reciente del dispositivo**, no la última en llegar.
  Consulta [Alarmas de umbral y lecturas tardías](#v0230-threshold).
- **Dos permisos nuevos, y `role:read` y `role:write` ahora actúan también dentro de un tenant.**
  Consulta [Usuarios de cliente, roles acotados y los permisos que los sustentan](#v0230-customer-users).
- **El token de un cliente o de un grupo eliminado ya no se puede reutilizar**, y a un miembro limitado
  a algunos clientes se le rechazan, en lugar de responderle, las operaciones que no se pueden limitar.
  Consulta [Usuarios de cliente, roles acotados y los permisos que los sustentan](#v0230-customer-users).
- **Dos identificadores de mensaje de la ruta de eventos cambiaron de formato**, lo que abre una
  pequeña ventana para un duplicado durante el despliegue. Consulta
  [Ids de evento y cabecera de correlación](#v0230-ids).
- **La próxima versión cambia cómo se escriben los eventos en el cable**, y solo se puede alcanzar
  pasando por esta. Consulta [Actualiza por esta versión antes de la siguiente](#v0230-next-wire).
- **Se mueven algunos valores por defecto:** la consola no puede incrustarse en otros sitios, la
  ventana de publicación del stream de captura es 1024 y los escritores publican en el líder de su
  stream. Consulta [Valores por defecto que cambiaron](#v0230-defaults).
- **Algunos límites medidos en un clúster de prueba** afectan a las instalaciones con mucho MQTT.
  Consulta [Limitaciones conocidas](#v0230-known-limitations).

#### Antes de actualizar {#v0230-before}

1. **Si subiste `streamMaxBytes`, o dimensionaste o limitaste tú mismo el volumen de JetStream,
   comprueba primero el margen.** La reserva del stream de captura crece en `streamMaxBytes` menos
   256 MiB, es decir, 768 MiB con el valor por defecto, y un stream nuevo, `device-ownership`,
   reserva 128 MiB. Consulta [Crece el techo del stream de captura](#v0230-capture).
2. **Si una aplicación o un script elimina un cliente o un grupo y vuelve a crear otro con el mismo
   token**, planifica elegir un token nuevo. Consulta
   [Usuarios de cliente, roles acotados y los permisos que los sustentan](#v0230-customer-users).
3. **Si dependes de reglas de umbral**, lee [Alarmas de umbral y lecturas tardías](#v0230-threshold)
   para que una alarma que ahora se activa no sea una sorpresa.
4. **Prevé dejar el `calculatedMetricsPerProfile` de cada nivel en 0 hasta que termine el
   despliegue.** La `v0.22.0` no tiene ese ajuste, así que cada nivel empieza en 0 y no hay nada que
   cambiar antes de actualizar. No lo subas, ni publiques una métrica calculada, hasta que todas las
   réplicas de `device-management` y `event-management` ejecuten esta versión. Consulta
   [Métricas calculadas](#v0230-calculated-metrics).

#### Durante la actualización {#v0230-during}

- **Las migraciones se ejecutan en el primer arranque** en `user-management` (un ámbito en cada
  membresía, un tenant en cada rol, invitaciones), `device-management` (los clientes de cada alarma, el
  ámbito de acceso de los grupos, los tokens retirados de clientes y grupos, una cola de cambios de
  propietario, una fórmula en la definición de una métrica), `command-delivery`, `device-state`,
  `update-management`, `dashboard-management` (paneles compartidos) y `notification-management` (horas
  de silencio, el ciclo de una alarma). Las columnas nuevas están vacías en todas las filas existentes,
  algo intencionado que se describe [más abajo](#v0230-customer-users). Los índices nuevos se crean sin
  `CONCURRENTLY`, así que las escrituras en la tabla que se indexa esperan a que termine, un tiempo
  breve salvo que la tabla sea muy grande.
- **En un bróker en clúster, los servidores del bróker se reinician de uno en uno, una vez**, porque la
  nueva compresión por defecto forma parte de la configuración del bróker.
- **`event-sources` sube en su sitio el techo del stream de captura** en su primer arranque. No se
  recrea ningún stream ni se pierde ningún mensaje.

Volver a `v0.22.0` tras la actualización lee la misma base de datos: los cambios son columnas,
índices y tablas nuevos, y la división del índice de los tokens de rol. No lo hagas mientras algún
miembro esté limitado a clientes o a grupos, ni mientras un tenant tenga roles propios definidos.
`v0.22.0` no lee ni el ámbito de una membresía ni el tenant de un rol, así que ese miembro dejaría de
estar limitado, y los roles propios de un tenant le parecerían roles del catálogo del operador.
Deshabilita o elimina antes esos miembros, y borra los roles que definieron tus tenants. Volver atrás
`event-management` cuando ya se usan métricas calculadas puede almacenar dos veces un mensaje
reentregado (consulta [Métricas calculadas](#v0230-calculated-metrics)).

#### Después de actualizar {#v0230-after}

- **No se requiere nada.** Todos los ajustes nuevos tienen valor por defecto, y ningún miembro tiene un
  ámbito hasta que se lo das.
- **Comprueba el margen del stream de captura** si no lo hiciste antes, y mantén activados
  `metrics.enabled` y `metrics.alerts` (lo están por defecto) para que se instale
  `JetStreamCaptureBacklogWillOverflow`.
- **Las alarmas, los comandos y las asignaciones de actualización anteriores a la actualización no son
  visibles para un miembro limitado a clientes o a grupos.** El servidor registra sus clientes a partir de esta
  versión. El estado en vivo de un dispositivo se registra la próxima vez que informa o cambia de
  propietario.

##### Crece el techo del stream de captura {#v0230-capture}

Los eventos de los dispositivos MQTT esperan en un stream de captura hasta que `event-sources` los
reenvía. El bróker ya confirmó cada uno a su dispositivo, así que cuando el stream se llena descarta los
eventos sin reenviar más antiguos y los dispositivos nunca se enteran. En una prueba de carga a 12.000
eventos por segundo se perdió así el 7,2 % de los eventos confirmados: el stream tenía 256 MiB y la
plataforma seguía el ritmo de unos 7.100 por segundo.

- **El vaciado tiene el doble de profundidad.** `capture.publishWindow` es 1024, antes 512. En una prueba
  comparativa que retrasa solo las confirmaciones, la tasa de reenvío para un retraso dado se duplicó.
  Es un ajuste fijo de `event-sources`, de 1 a 1024; un valor fuera de ese rango detiene el servicio.
- **El stream toma el techo caliente completo.** Estaba limitado a 256 MiB, o al techo caliente si era
  menor, y ningún ajuste podía subir ese límite. Ahora sigue a `streamMaxBytes` como los demás streams
  calientes: 1 GiB por defecto y 64 MiB en una instancia `--compact`, donde no cambia nada. Con los
  valores por defecto la reserva de disco de JetStream pasa de 10.696 MiB a 11.592 MiB, de los 14 GiB
  que permite el volumen por defecto: 768 MiB para el stream de captura y 128 MiB para el nuevo stream
  `device-ownership`.
- **Si subiste `streamMaxBytes` y dimensionaste el volumen con poco margen, la reserva crece en ese
  valor menos 256 MiB.** JetStream reserva el techo de un stream al crearlo o subirlo. Una reserva por
  encima del límite del bróker hace que `event-sources` se detenga al arrancar con `insufficient
  storage resources available`, y sigue deteniéndose hasta que añadas espacio (consulta
  [Arrancar una instancia](./bootstrap.md)) o bajes el techo.
- **Un stream editado para descartar los mensajes nuevos se restaura.** Un stream de captura lleno que
  rechaza mensajes nuevos no llega a ningún dispositivo, porque el bróker confirma la publicación antes,
  de modo que se perderían los eventos más recientes y nada los contaría. Cada stream declarado vuelve a
  descartar los más antiguos, y el servicio lo registra en el log.
- **Una alerta crítica nueva, `JetStreamCaptureBacklogWillOverflow`,** salta cuando el atraso de la
  captura ocupa al menos una décima parte del stream y lo llenará en diez minutos al ritmo al que
  creció en los últimos tres, durante un minuto. Se instala con las demás alertas cuando
  `metrics.enabled` y `metrics.alerts` están activados. Consulta [Observabilidad](./observability.md).

##### Compresión de rutas del bróker {#v0230-route-compression}

Los servidores de un bróker en clúster se copian entre sí cada evento almacenado. `nats_route_compression`
comprime ese tráfico, y ahora es `s2_fast` por defecto en un bróker en clúster (`--ha`, o
`nats_cluster_replicas` mayor que 1). Un bróker de un solo servidor no se ve afectado. En un clúster de
tres nodos redujo el tráfico entre servidores un 60 % por evento; comprimir costó a cada servidor entre
el 2,6 y el 2,9 % de su CPU, y aun así la CPU total del bróker por evento bajó cerca de un 4 %. `dcctl upgrade` reinicia los servidores del
bróker de uno en uno, una vez, para activarla. Un valor que hayas fijado tú, `off` incluido, se
conserva. Consulta [Compresión entre los servidores del bróker](./bootstrap.md#route-compression).

##### Alarmas de umbral y lecturas tardías {#v0230-threshold}

Una alarma de umbral seguía la última lectura en llegar. Una lectura que la plataforma recibía tarde
podía activar una alarma que lecturas más recientes ya habían despejado, o despejar una que esas
lecturas mantenían activa. Ahora:

- **Mientras una alarma está activa, una lectura hasta diez minutos más antigua que la más reciente
  del dispositivo se descarta y se cuenta**, de modo que no puede despejar la alarma. Una lectura más
  antigua que eso se evalúa, así que un reloj de dispositivo atrasado no puede mantener activa una
  alarma.
- **Mientras no hay alarma activa, una lectura coincidente más atrasada que la tolerancia de retraso se
  descarta solo si se puso en cola para detección más de 30 segundos después de que la plataforma la
  recibiera**, como ocurre con una publicación reintentada, o si se desconoce alguna de las dos horas.
  La tolerancia se mide desde la lectura más reciente que ha visto el motor. Cualquier otra lectura
  coincidente activa la alarma, así que un dispositivo con el reloj lento, o que sube lecturas guardadas durante una caída,
  ahora activa una alarma con ellas a medida que llegan.
- **Las reglas de ventana de recuento y de tasa sitúan una lectura por su propia hora**, como las reglas
  de duración. Activan y despejan con un margen de una tolerancia de retraso respecto a la lectura más
  reciente que ha visto el motor, y la última lectura antes de un periodo de silencio se decide con el reloj de pared cuando el
  motor lleva un rato inactivo.
- **Las reglas de ventana deslizante leen su ventana según la lectura más reciente del dispositivo**, de
  modo que una lectura tardía cuenta solo junto a las lecturas que estaban dentro de una misma ventana
  con ella.

`devicechain_eventprocessing_detect_late_samples_total` cuenta estos descartes. Consulta
[la página del motor de detección](./detection-engine.md).

##### Usuarios de cliente, roles acotados y los permisos que los sustentan {#v0230-customer-users}

Ahora un miembro puede limitarse a algunos de tus clientes, a grupos dinámicos de dispositivos, o a
ambos (hasta 16 en total), y ve solo los dispositivos, alarmas, eventos, comandos y paneles que les
pertenecen. Los administradores del tenant invitan a miembros, definen roles y comparten paneles desde
la consola; el capítulo completo es
[Miembros, roles y usuarios de cliente](../user-guide/members-roles-and-customer-users.md). Lo que
importa al actualizar:

- **Hay dos permisos nuevos.** `member:read` lista miembros e invitaciones, y `member:write` invita,
  asigna roles y ámbito, habilita, deshabilita y elimina. Son permisos de tenant y no están en la base de
  solo lectura. El rol integrado de administrador del tenant tiene `*` y, por tanto, ambos.
- **`role:read` y `role:write` ahora actúan en dos planos.** En el plano de administración de la
  instancia siguen llegando al catálogo de roles del operador. En un rol de tenant, ahora permiten al
  miembro listar y definir los roles que ese tenant posee.
- **Un cambio en el acceso de un miembro surte efecto en su siguiente renovación**, dentro de la vida
  del token de acceso (15 minutos por defecto) o en 5 minutos para un miembro limitado a clientes o
  grupos. No cierra la sesión de la persona en sus demás tenants.
- **Las operaciones que no se pueden limitar se rechazan** a un miembro limitado, con `FORBIDDEN` y
  `extensions.reason` con el valor `SCOPE_UNSUPPORTED`, diga lo que diga su rol. Las operaciones que sí
  se pueden limitar responden solo con lo que el miembro posee, y un registro fuera de ese conjunto se
  responde como `NOT_FOUND`. Consulta
  [Cuando tu acceso se limita a algunos clientes](../reference/graphql-api.md#limited-access).
- **El token de un cliente o de un grupo eliminado se retira en su tenant.** `createCustomer` o
  `createEntityGroup` con ese token responde `INVALID_VALUE` con un mensaje que explica por qué, y solo
  eliminar el tenant entero lo libera. Un cliente que sigue existiendo con ese token sigue respondiendo
  `CONFLICT`. Los registros que nombran a un cliente solo por su token mostrarían, si no, el historial
  del cliente anterior a uno nuevo. Los tokens de clientes y grupos eliminados antes de esta versión no
  se retiran.
- **Los cambios de propietario de un dispositivo siempre llegan a device-state.** El cambio se confirma
  junto con un anuncio en cola que se reintenta hasta que el bróker lo confirma, mientras que antes se
  publicaba después de la confirmación. Un pod que moría entre ambos pasos dejaba a los miembros del
  propietario anterior viendo el estado en vivo del dispositivo hasta su siguiente evento, que para un
  dispositivo en silencio nunca llegaba.
- **Los paneles se pueden compartir con clientes y grupos.** Un usuario de cliente ve solo los paneles
  publicados que se comparten con él. Un widget vinculado a un área o a un activo lee el grafo de
  relaciones, al que no tienen acceso, y muestra **Not available for customer users**.

##### Ids de evento y cabecera de correlación {#v0230-ids}

El id que usa el bróker para descartar una publicación duplicada, y la cabecera que lleva un id de
correlación entre servicios, son más cortos: la cabecera es `Dc-Cid` (antes `Dc-Correlation-Id`) y los ids
usan base 36. Un id nuevo nunca puede coincidir con uno antiguo. Una actualización progresiva tiene a la
vez pods de ambas versiones, así que un evento cuya confirmación de publicación se perdió, o cuyo pod se
detuvo sin vaciarse, mientras duraba el despliegue puede entregarse dos veces. La telemetría almacenada
no se duplica, porque la clave de un evento es un resumen de su contenido, pero una regla de recuento, de
repetición o de agregado deslizante puede contar ese evento dos veces. La correlación en los logs no se
conserva para los mensajes en vuelo durante la actualización. La exposición termina 10 minutos después de
que desaparezca el último pod antiguo.

##### Actualiza por esta versión antes de la siguiente {#v0230-next-wire}

Los eventos cruzan dos streams retenidos como mensajes protobuf, y esta versión puede leer una forma más
pequeña: los instantes como un recuento de nanosegundos y los sellos de las credenciales de dispositivo
en binario, cerca de un tercio más pequeños por mensaje. **Sigue escribiendo la forma antigua por
defecto**, de modo que un pod de la versión anterior puede leerlo todo. La próxima versión escribe la
forma pequeña por defecto, y un pod anterior a esta versión descartaría esos mensajes o los enviaría a la
cola de mensajes fallidos. Actualiza cada instancia a `v0.23.0` primero, y solo después a la siguiente
versión. `infrastructure.messaging.wireFormat` (`legacy` o `compact`, exactamente así escritos) elige la
forma. Fija `compact` solo cuando todos los pods ejecuten esta versión, y vuelve a fijar `legacy` antes
de volver a una versión anterior.

##### Valores por defecto que cambiaron {#v0230-defaults}

- **La consola no puede incrustarse en otro sitio.** Sus páginas se sirven con
  `Content-Security-Policy: frame-ancestors 'self'` y `X-Frame-Options: SAMEORIGIN`. El visor `/dash`
  sigue pudiendo incrustarse, porque usarlo en una pantalla de pared es un uso admitido. Una página de otro
  origen que incruste la propia consola deja de mostrarla.
- **`capture.publishWindow` es 1024** (antes 512).
- **Los escritores publican en el líder de su stream.** `infrastructure.nats.affinity.publishToLeader`
  está activado por defecto: los escritores en canalización de `device-management`, `event-sources` y
  `sparkplug-ingest` abren una segunda conexión con el servidor que lidera su stream, y vuelven a la
  habitual si falla. En un clúster de tres nodos, el techo de un solo pod de
  `device-management` subió cerca de un 6 %. Ponlo en `false` para desactivarlo.

##### Métricas calculadas {#v0230-calculated-metrics}

La definición de una métrica puede llevar una fórmula sobre las demás métricas del perfil, y
`testMetricFormula` previsualiza una. Una fórmula publicada se calcula ahora al resolver cada lectura y
se almacena como una medida ordinaria, así que el historial, el agregado por hora, el último estado, la
detección y la transmisión en vivo la reciben. Un valor que un dispositivo envía con la clave de una
métrica calculada se descarta en favor del calculado, y una fórmula que no puede producir un valor omite
solo esa métrica para esa lectura.

**Las métricas calculadas siguen desactivadas mientras el `calculatedMetricsPerProfile` de cada nivel
sea 0**, que es el valor por defecto; la `v0.22.0` no tenía ese ajuste. Publicar un perfil con una
métrica calculada se rechaza con `LIMIT_EXCEEDED` hasta que el nivel de su tenant lo permita, así que la
actualización no tiene efecto hasta entonces. Antes de actualizar, comprueba tus niveles. Si alguno es
mayor que 0, ponlo a 0, o no publiques ninguna métrica calculada, hasta que todas las réplicas de
`device-management` y `event-management` ejecuten esta versión, y después espera al menos
`metricDefCacheTtlSeconds` (300 segundos por defecto) tras detenerse el último pod antiguo de
`device-management`. Publicar durante un despliegue mixto puede almacenar para siempre un valor enviado
por el dispositivo con la clave de una métrica calculada, y los mensajes reentregados entre réplicas
antiguas y nuevas de `event-management` pueden almacenarse dos veces. Volver atrás `event-management`
cuando ya se usan métricas calculadas conlleva el mismo riesgo de duplicados.

##### Alertas y métricas nuevas {#v0230-observability}

- `JetStreamCaptureBacklogWillOverflow` (crítica), descrita arriba;
- `JetStreamCaptureBacklogGrowing` (aviso), su aviso temprano: la misma comprobación del atraso con un
  horizonte de veinte minutos;
- `JetStreamCaptureLeaderChanged` (aviso): el stream de captura cambió de líder mientras llegaban
  eventos, así que los eventos MQTT publicados durante el cambio pueden haberse confirmado y perdido
  (consulta [Limitaciones conocidas](#v0230-known-limitations));
- `DeviceStateDroppingEvents` (aviso): device-state descartó un evento tras su último intento de entrega,
  lo que deja obsoleta la conectividad de un dispositivo, una lectura o su posición hasta que llega un
  evento más reciente;
- `devicechain_notificationmanagement_quiet_hours_total{outcome}` para las notificaciones retenidas,
  liberadas o descartadas por las horas de silencio;
- `devicechain_devicemanagement_calcmetric_evaluations_total`,
  `devicechain_devicemanagement_calcmetric_skipped_total{reason}` y
  `devicechain_devicemanagement_calcmetric_device_key_dropped_total` para las métricas calculadas;
- `devicechain_commanddelivery_command_delivery_window_commands_undefined_total` para los comandos
  que fallan porque su perfil ya no los define;
- `devicechain_sparkplugingest_presence_probes_total` y
  `devicechain_sparkplugingest_presence_probe_disconnects_total` para los sondeos de actividad del Host;
- `jetstream_consumer_leader_local`, `jetstream_publish_leader_local` y
  `jetstream_consumer_leader_moves_total` para la ubicación de los líderes, y
  `jetstream_stream_leader_changes_total` para un cambio del líder de un stream.

Cada resultado de los contadores de procesador compartidos se exporta ahora a cero, de modo que el primer
descarte tras el primer scrape de un pod es visible para una alerta. Ninguna métrica cambió de nombre ni
se eliminó. `jetstream_publish_window_full_seconds_total`, `jetstream_publish_window_full_total` y
`jetstream_publish_duplicates_total` ganaron una etiqueta `writer`, que divide sus series; una consulta
que suma sobre ella da lo mismo que antes.

#### Qué más cambió {#v0230-what-changed}

##### Limitaciones conocidas {#v0230-known-limitations}

Se midieron en un único clúster de prueba: tres nodos de servicios de 4 vCPU y 8 GiB cada uno, TLS
activado en el bróker, el formato de cable por defecto y ejecuciones de 300 a 600 segundos. Describen ese
clúster, no son una garantía para el tuyo.

- **Las publicaciones MQTT QoS 1 pueden confirmarse y después perderse, sin contarse, mientras el stream
  de captura cambia de líder con mucha carga.** A 12.000 eventos por segundo, el líder del stream cedió
  una vez, y los eventos publicados durante los cinco segundos en que el stream no tuvo líder se
  perdieron después de que los dispositivos recibieran su confirmación. Ningún contador registra los
  eventos perdidos. `JetStreamCaptureLeaderChanged` avisa de que hubo un cambio de líder mientras
  llegaban eventos, y su hora acota la ventana que revisar en los registros de tus propios
  dispositivos. Hay trabajo en curso para eliminarlo.
- **La tasa MQTT sostenida que ese clúster pudo seguir fue de unos 6.000 eventos por segundo**, limitada
  por el bróker. A 12.000 por segundo el atraso creció; por encima del techo del stream de captura se
  descartan los eventos más antiguos, y esa pérdida se cuenta y genera una alerta. La ingesta HTTP en el
  mismo clúster llegó a unos 14.000 por segundo.
- **La memoria del bróker puede acercarse a su límite con mucha carga MQTT.** Cada servidor usó entre 3,0
  y 3,2 GB de su `GOMEMLIMIT` de 3.276 MiB. Para una instalación con mucho MQTT, sube `nats_memory_limit`
  (4Gi por defecto, se fija en el `terraform.tfvars` de la instancia y se aplica con `dcctl upgrade`;
  `GOMEMLIMIT` lo sigue al 80 %). Consulta
  [El intermediario de mensajes](./bootstrap.md#broker-sizing).

##### También en esta versión {#v0230-also}

- **Horas de silencio en una política de notificación.** Franjas semanales en una zona horaria IANA
  retienen las notificaciones por debajo de una gravedad que eliges y las envían cuando termina el
  periodo de silencio. Consulta [Horas de silencio](../guides/notification-channels.md#quiet-hours).
- **Una alarma que se vuelve a activar escala de nuevo.** Una alarma que se despejó y se volvió a activar
  conservaba el acuse de recibo del primer ciclo, así que su segundo ciclo nunca escalaba.
- **Los paneles recuperan lo que se perdió durante una caída de la transmisión en vivo.** Tras una caída de
  la conexión, un gráfico de líneas vinculado a un dispositivo, una tabla de flota, un gráfico de barras
  con una barra por dispositivo y una tarjeta de estadística recuperan las lecturas enviadas durante la
  caída, de modo que un gráfico ya no traza una línea recta sobre el hueco. Un gráfico de varios
  dispositivos muestra un corte en su lugar. Esto elimina casi toda la limitación conocida de la
  `v0.22.0`.
- **Una pestaña Enviar datos** en la página del dispositivo escribe un comando `mosquitto_pub` o `curl`
  con la primera lectura del dispositivo, con marcadores para lo que la consola no puede saber, como el
  id de la instancia, los hosts y una contraseña.
- **`dcctl enable-area`** activa un área funcional en una instancia en marcha. No hay ningún comando para
  desactivarla.
- **Sparkplug.** Un Host conectado vuelve a sondear la actividad de sus dispositivos asertados cada
  `presence.probeSeconds` (1800 por defecto), de modo que un dispositivo cuyo certificado de defunción
  nunca llegó deja de figurar en línea. De un mensaje con más de 16.384 métricas no se almacena
  ninguna lectura.
- **MCP.** Cada llamada a una herramienta escribe una línea de log estructurada,
  `audit=mcp_tool_call`, con el tenant, el actor, `client_id`, la herramienta y el resultado.
- **Comandos.** Un comando retenido por una ventana de mantenimiento sigue retenido cuando un cambio de
  perfil lo vuelve ordinario, y un comando que espera una ventana y que su perfil ya no define falla
  con un motivo, sin enviarse nunca.
- **Las migraciones del almacén de eventos** encuentran los chunks comprimidos de una forma que
  TimescaleDB 2.29 y posteriores pueden planificar.
- **Varios lectores de entrada por pod de `device-management`:** `resolution.inboundReaders`, de 1 a 4,
  vale 1 por defecto.
- **Métricas calculadas**, desactivadas por defecto; consulta
  [Métricas calculadas](#v0230-calculated-metrics).
- **Los brókeres en clúster** también pueden mover los líderes de los consumidores junto a sus lectores:
  `infrastructure.nats.affinity.consumerLeaders`, desactivado por defecto.
- **Una republicación que compite con su propio original** se reintenta, y ya no se cuenta como una
  publicación fallida ni como un evento perdido.
- **Los paneles cargan más rápido.** El motor de gráficos, los descriptores de widgets y los catálogos se
  cargan cuando se necesitan por primera vez, de modo que la primera descarga es unos 173 kB (gzip) más
  pequeña.
- **La consola** ya no renueva su token dos veces justo antes de que caduque, y un miembro sin
  `device:write` ve de solo lectura la pestaña Básico de un dispositivo.

### v0.22.0 — paneles sobre una cuadrícula de celdas cuadradas, y una ruta de eventos que espera menos {#v0220-upgrade}

`v0.22.0` es una actualización en su sitio desde `v0.21.0` o `v0.21.1`: `dcctl install` para el
clúster y después `dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](#v0170-upgrade).
No añade ninguna migración: ningún servicio cambia el esquema de su base de datos. Incluye las
cuatro correcciones que publicó la [`v0.21.1`](#v0211-upgrade), así que si vienes de `v0.21.0`, lee
también esa sección por la única comprobación que te pide. Si vienes de `v0.20.0` o una versión
anterior, lee además las [notas de la `v0.21.0`](#v0210-upgrade). Una instancia creada por la
`v0.16.0` o anterior sigue teniendo que [destruirse y crearse de nuevo](#pre-declaration-recreate).

La mayor parte de esta versión son paneles: una cuadrícula de celdas cuadradas que se adapta a la
pantalla, un editor que se ajusta a las celdas, un rango de tiempo del panel con pausa y trece
widgets nuevos, entre ellos una tabla de flota con una fila por dispositivo. El resto hace que la
ruta de eventos espere menos: varios servicios mantienen más publicaciones en vuelo, device-state
hace menos trabajo de base de datos por evento y la plataforma lee 256 mensajes por petición. Qué
cambia para alguien:

- **Los paneles se guardan en un formato nuevo.** Los paneles anteriores se convierten al cargarse,
  de forma aproximada en cuanto a la disposición. De la configuración de un widget no se pierde
  nada salvo el número de muestras de un gráfico de serie temporal. Consulta
  [Los paneles se guardan en un formato nuevo](#v0220-dashboard-format).
- **Los paquetes npm `@devicechain/dashboards` y `@devicechain/widgets` tienen cambios
  incompatibles**, y un paquete `0.21` dibuja mal, sin ningún error, un panel guardado por la
  `v0.22.0`. Consulta [Si desarrollas con los paquetes npm](#v0220-packages).
- **Un widget de un solo dispositivo vinculado a un grupo de dispositivos ahora rechaza el grupo**
  en lugar de mostrar el dispositivo que informó el último. Consulta
  [Un widget de un solo dispositivo rechaza un grupo](#v0220-group-refusal).
- **Cambian tres valores por defecto:** el lote de lectura (de 128 a 256), la petición de CPU de
  device-management (de 800m a 450m) y cuántos eventos mantienen en vuelo MQTT externo, Sparkplug y
  la presencia, lo que también aumenta cuántos puede perder un pod que muere de repente en esas
  rutas de como mucho una entrega. Consulta [Valores por defecto que cambiaron](#v0220-defaults).
- **`dcctl upgrade`, `destroy` y `sim` rechazan un `--kube-context` que nombra un clúster distinto
  del registrado para la instancia.** Antes sustituía el registro sin avisar. Consulta
  [dcctl rechaza un `--kube-context` en conflicto](#v0220-kube-context).
- **Los comandos retenidos por una ventana de mantenimiento siguen la ventana actual del tipo de
  dispositivo**, y quitar una ventana los libera de inmediato. Consulta
  [Los comandos retenidos siguen la ventana de mantenimiento actual](#v0220-window-follow).

#### Antes de actualizar {#v0220-before}

1. **Si una aplicación tuya dibuja paneles con los paquetes npm, prevé pasarla a `0.22.0` antes de
   que alguien guarde un panel en la instancia actualizada.** Los paneles guardados hoy se siguen
   dibujando bien con `0.21`; un panel guardado o publicado después de la actualización, no.
   Consulta [Si desarrollas con los paquetes npm](#v0220-packages).
2. **Si lees fuentes MQTT externas o Sparkplug y la pérdida repentina de un pod debe perder tan
   poco como antes**, reduce sus ventanas de publicación antes de actualizar. Consulta
   [Más publicaciones en vuelo](#v0220-publish-windows).
3. **Si tu `terraform.tfvars` define `nats_memory_limit` por debajo de 4Gi, súbelo a 4Gi o quita la
   línea.** La actualización conserva tu valor, y con 2Gi un servidor solo sostiene unos 3.000
   eventos por segundo. Consulta
   [El broker recuerda los eventos reentregados durante 10 minutos](#v0220-dedup).

#### Durante la actualización {#v0220-during}

- **No se ejecuta ninguna migración**, así que la base de datos queda intacta y los pods se
  renuevan como en cualquier actualización.
- **La consola y `/dash` leen el nuevo formato de panel en cuanto se renuevan**, porque se
  distribuyen con la versión. Nada convierte un panel guardado durante la actualización; cada uno
  se convierte la próxima vez que se carga.
- **Los pods de device-management se vuelven a programar con la petición de CPU más baja**, y el
  límite de memoria de cada servidor NATS sube de 2Gi a 4Gi. Son los dos cambios en valores de
  recursos renderizados. Consulta
  [El broker recuerda los eventos reentregados durante 10 minutos](#v0220-dedup).
- **Hasta que event-processing también se haya renovado, un cambio de conexión de un dispositivo
  deshabilitado puede generar o cerrar una alarma de conectividad.** device-management ahora
  transmite las conexiones y desconexiones de un dispositivo deshabilitado, y un pod de
  event-processing que sigue en la `v0.21.x` no sabe que debe ignorarlas. `dcctl upgrade` renueva
  los servicios juntos, así que esto solo dura lo que dura el despliegue.

Volver a `v0.21.x` después de la actualización lee la misma base de datos. Los paneles son la
excepción que hay que mirar: un panel guardado o publicado con la `v0.22.0` está en el formato
nuevo, que la `v0.21.x` dibuja con cada widget de una celda de ancho y una de alto, y sin los widgets
nuevos. No edites ese panel en la consola anterior, porque al guardarlo allí se escribe esa
disposición.

#### Después de actualizar {#v0220-after}

- **Revisa una vez cada panel que te importe.** Ábrelo en el editor: un aviso indica que se
  convirtió y nombra cualquier gráfico al que se le quitó el número de muestras. Comprueba la
  disposición, guarda y, si el panel está publicado, publica una versión nueva.
- **Busca los widgets que ahora rechazan un grupo** y cámbialos por la tabla de flota o por un
  gráfico de barras con una barra por dispositivo.
- **No hace falta nada más.** Todos los ajustes nuevos tienen valores por defecto.

##### El broker recuerda los eventos reentregados durante 10 minutos {#v0220-dedup}

`resolved-events` y `failed-events` ahora descartan un evento publicado de nuevo durante 10
minutos, frente a los 2 minutos por defecto del broker. Antes, un evento cuyo origen se reentregaba
tras dos publicaciones fallidas podía guardarse dos veces, y la detección podía contarlo dos veces.
La ventana del flujo de eventos entrantes pasa de 30 a 10 minutos. Sigue cubriendo todas las
reentregas mientras un consumidor está en marcha, y un pod de `event-sources` o
`device-management` que vuelve en unos 8 minutos. Si un pod está caído más tiempo, o un broker
guarda una publicación, pierde su confirmación y después falla publicaciones durante más de unos
8 minutos, los eventos en vuelo se guardan dos veces. Ejecuta `event-sources` y
`device-management` con 2 o más réplicas para evitar el caso del pod. El límite de memoria de cada
servidor NATS pasa de 2Gi a 4Gi (`GOMEMLIMIT` de 1638MiB a 3276MiB), porque cada servidor guarda
cada id dentro de estas ventanas: unos 2,6 GiB con 10.000 eventos por segundo sostenidos. La
petición de 768Mi no cambia, así que ningún servidor se coloca de otra forma. `dcctl upgrade`
aplica primero el broker y reinicia sus servidores, de uno en uno con `--ha`. Después, cada
servicio cambia las ventanas de los flujos existentes en su sitio al arrancar por primera vez;
no se recrea ningún flujo y no se pierde ningún mensaje. **Si tu `terraform.tfvars` define
`nats_memory_limit` por debajo de 4Gi, la actualización conserva tu valor.** Con 2Gi un servidor
solo puede sostener unos 3.000 eventos por segundo antes de que las tablas de ids lo dejen sin
margen. Súbelo a 4Gi, o quita la línea, antes de actualizar.

#### Qué cambió {#v0220-what-changed}

##### Los paneles se guardan en un formato nuevo {#v0220-dashboard-format}

La definición de un panel pasa de `schemaVersion` 1 a 3 en dos pasos, y un panel sin
`schemaVersion` se lee por su forma. Todos los lugares que cargan una definición la convierten: el
editor, `/dash`, la clonación, la importación, la comparación de versiones y la reversión. Una
carga nunca vuelve a escribir la definición guardada.

**La cuadrícula (formato 2).** La cuadrícula anterior tenía columnas fluidas (24 por defecto) y
filas de altura fija (40 px). La nueva tiene celdas cuadradas, medidas en celdas en ambas
direcciones, y dos widgets de la cuadrícula no pueden solaparse. No existe una correspondencia
exacta entre ambas, así que la conversión es una aproximación:

- las líneas de columna se escalan al nuevo número de columnas y se redondean, de modo que los
  widgets que se tocaban en una línea se siguen tocando;
- las líneas de fila se convierten a un ancho de referencia de 1366 px;
- el espacio anterior pasa a ser el canal (gutter);
- `z` y el `offset` en píxeles se descartan, porque la cuadrícula no tiene capas;
- lo que se solapa tras el redondeo, o se solapaba mediante `z`, se desplaza hacia abajo hasta que
  cabe, y cada widget queda debajo de los widgets que tenía encima.

Se conservan todos los widgets, con su tipo, su vínculo y sus opciones, incluido un widget de un
tipo que esta compilación no conoce.

**Ventanas de los gráficos (formato 3).** Un `timeseries-chart` guardaba un número de muestras,
`options.window` (300 por defecto). Ahora dibuja un intervalo de tiempo: `windowSeconds` o, si no
indica ninguno, el rango de tiempo del panel. Un número de muestras no tiene un intervalo honesto,
porque depende de la frecuencia con la que informa el dispositivo, así que la conversión descarta
`window` y el gráfico sigue el rango de tiempo del panel. Es una hora salvo que el panel indique
otra, que es la hora que ya cargaba cada gráfico, así que lo que muestra un gráfico al cargarse no
cambia. Un gráfico que debía ser más corto o más largo necesita un intervalo fijado a mano. Los
demás widgets conservan su `window`.

**Qué ven los autores.** En el editor, un panel convertido muestra un aviso que pide revisar la
disposición y guardar para conservarla en el formato nuevo. Cuando hay gráficos que perdieron el
número de muestras, también los nombra por su título. Guardar está habilitado aunque no haya otra
edición, y al guardar se escribe el formato nuevo y desaparece el aviso. Una versión publicada es
inmutable, así que sigue en el formato anterior y se convierte cada vez que se ve, hasta que
publiques una versión nueva. Un archivo de panel exportado desde la `v0.21.x` se importa y se
convierte de la misma forma.

##### Si desarrollas con los paquetes npm {#v0220-packages}

`@devicechain/dashboards`, `@devicechain/widgets`, `@devicechain/widget-sdk`, `@devicechain/client`
y `@devicechain/brand` se publican como `0.22.0`.

**Pasa a `0.22.0` antes de que se guarden paneles en el formato nuevo.** El analizador de `0.21`
ignora `schemaVersion` y lee una ubicación nueva como una caja sin extensión, así que un panel
guardado por la `v0.22.0` se dibuja con cada widget de una celda de ancho y una de alto. No muestra
ningún error. Los paneles guardados antes de la actualización se siguen dibujando bien con `0.21`
hasta que se vuelven a guardar.

**Desde `0.22.0`, un panel guardado por una versión más reciente se rechaza en lugar de dibujarse
mal.** `parseDashboardDefinition` lanza un `DashboardDefinitionError` con `code: 'schemaTooNew'` y
la versión encontrada en `found`. Comprueba `code` en lugar de `instanceof`, que falla cuando una
aplicación empaqueta dos copias del paquete. `/dash`, board-replay y, en la consola, la página de
detalle, la reversión, la importación y la clonación muestran un mensaje que pide actualizar en
lugar del panel. Esto solo protege a los visores `0.22.0` y posteriores de los paneles guardados
por una versión futura: un visor `0.21` sigue dibujando mal un panel de la `v0.22.0`, así que
actualiza primero los paquetes.

**`@devicechain/dashboards`** cambia el modelo de disposición:

- se eliminan `WidgetBox`, `resolveWidgetBox`, `baseBox`, `setWidgetBox`, `bringToFront`,
  `gridBoxToPx` y `pxToGridBox`. Los sustituyen `Placement`, `resolvePlacement`, `basePlacement`,
  `setWidgetPlacement`, `gridPlacementToPx` y `pxToGridPlacement`; no hay capas, así que nada
  sustituye a `bringToFront`;
- `WidgetLayout` asocia un punto de corte a un `Placement`, `Breakpoints` asocia un nombre a un
  `BreakpointSpec`, `CanvasGrid` sustituye `gap` y `rowHeight` por `minCellSize`, `gutter` y
  `padding`, y `GridGeometry` sustituye sus tamaños de columna y fila por `cell` y `gutter`;
- `SYNTHETIC_GENERATORS` solo enumera los identificadores de los generadores. Aporta tus propias
  etiquetas; los identificadores guardados no cambian;
- `parseDashboardDefinition` ahora rechaza widgets de la cuadrícula que se solapan. Una definición
  anterior se convierte antes de esa comprobación.

**`@devicechain/widgets`:**

- `severityColor(severity)` pasa a ser `severityColor(colors, severity)`;
- la opción `window` de `timeseries-chart` se sustituye por `windowSeconds`, y la versión de
  opciones del gráfico pasa de 1 a 2.

**`@devicechain/widget-sdk`** no elimina ninguna exportación y añade muchas: tokens de tamaño y
densidad del panel, texto ajustado a la caja, antigüedad del valor y el indicador de conexión,
niveles y colores de severidad, umbrales y el rango de tiempo del panel (`useWidgetTimeRange`,
`MAX_SPAN_SAMPLES`, `withinSpan`), además de nuevos constructores de gráficos en `/echarts`.
`useElementSize` gana un parámetro opcional `step` y `WidgetFrame` las propiedades opcionales
`flush` y `sampleTime`. Tres cambios que una compilación de TypeScript puede notar:

- `ChartTheme` tiene un campo obligatorio `severity`. Solo importa si construyes un tema a mano en
  lugar de llamar a `useChartTheme` o `resolveChartTheme`;
- las uniones `WidgetChannel` (ahora con `aggregate`), `WidgetId`, `BuiltinWidgetId`, `ThemeVar` y
  `OptionSpec` ganaron miembros, así que un `switch` exhaustivo sobre una de ellas necesita un caso
  nuevo;
- `formatValue` sin precisión ahora redondea a 3 decimales, o a 4 cifras significativas por
  debajo de 1, donde antes imprimía el número sin redondear.

`SDK_API_VERSION` no cambia, y el widget de terceros de ejemplo del repositorio compila contra
`0.22.0` sin cambios. La sección `Upgrading from 0.21` del README de cada paquete, en
[`@devicechain/dashboards`](https://www.npmjs.com/package/@devicechain/dashboards), [`@devicechain/widgets`](https://www.npmjs.com/package/@devicechain/widgets) y
[`@devicechain/widget-sdk`](https://www.npmjs.com/package/@devicechain/widget-sdk), enumera los cambios con sus sustitutos.
**`@devicechain/client`** solo añade: `createSessionTokens` y
`update-management` como área. **`@devicechain/brand`** no cambia.

##### Un widget de un solo dispositivo rechaza un grupo {#v0220-group-refusal}

El flujo de mediciones indexaba sus últimos valores por nombre de medición. Vinculado a un grupo de
dispositivos, cada dispositivo que informaba `temperature` sobrescribía a los demás, así que una
tarjeta, un indicador, un gráfico o una tabla mostraba el dispositivo que había hablado el último
como si fuera la respuesta. Esos widgets ahora rechazan un grupo con una frase que remite a la
tabla de flota o al modo de una barra por dispositivo del gráfico de barras. Esto cubre la tarjeta
de último valor, los indicadores, el gráfico de serie temporal, la tabla, el gráfico circular, la
tarjeta de estadística y un gráfico de barras en su modo por medición. El editor solo ofrece un
grupo a la tabla de flota, al gráfico de barras, a los widgets de alarmas y al mapa.

##### dcctl rechaza un `--kube-context` en conflicto {#v0220-kube-context}

`dcctl upgrade` con un `--kube-context` que nombra el clúster registrado de la propia instancia
ahora renderiza exactamente la misma instancia que sin la opción. Antes descartaba el ajuste de
colocación del almacenamiento de blobs local, y el chart rechazaba el renderizado.

Un `--kube-context` que nombra un contexto **distinto** del registrado para la instancia ahora lo
rechazan `dcctl upgrade`, `dcctl destroy` y `dcctl sim`. Antes sustituía el registro sin avisar.
Quita la opción para usar el clúster registrado. Si lo que está mal es el registro, porque el
clúster se reconstruyó o se renombró su contexto, elimina
`~/.devicechain/instances/<instance>/instance.json` y vuelve a ejecutar con `--kube-context`.

##### El estado de conexión de un dispositivo deshabilitado sigue actualizándose {#v0220-disabled-presence}

Las conexiones, desconexiones y degradaciones de un dispositivo deshabilitado ahora llegan a
device-state y al historial de eventos, así que su estado de conexión sigue siendo cierto mientras
está deshabilitado. La detección las sigue ignorando, de modo que no salta ninguna alarma para un
dispositivo deshabilitado, y sus mediciones, ubicaciones y comandos se siguen rechazando. Un nodo
de borde Sparkplug puede hacer que un dispositivo hijo deshabilitado aparezca conectado, porque la
presencia que el nodo informa de sus dispositivos es de confianza por diseño. Un contador nuevo,
`devicechain_eventsources_presence_reconcile_unapplied_total`, cuenta las reparaciones de la reconciliación que se enviaron pero no se vieron
aplicadas.

##### Los comandos retenidos siguen la ventana de mantenimiento actual {#v0220-window-follow}

Un comando retenido por una ventana de mantenimiento ahora sigue la ventana **actual** de su tipo
de dispositivo. Si cambias un tipo de la ventana A a la ventana B, los comandos que ya esperaban
pasan a la próxima apertura de B. Si **quitas** la ventana de un tipo, sus comandos en espera se
**liberan de inmediato**. Las notas de la `v0.21.0` decían que seguían retenidos; eso ya no es así.
Los comandos de un dispositivo deshabilitado siguen retenidos en cualquier caso. Antes de quitar
una ventana, comprueba qué está esperando en ella.

También se corrige: los comandos en cola de un dispositivo deshabilitado ya no mantienen al emisor
LwM2M sondeando en un bucle continuo, y un comando cuya confirmación se rechaza porque su ventana
acaba de cerrarse ahora queda aparcado para la próxima apertura en lugar de quedarse en `SENT`.
Una reclamación rechazada porque el dispositivo está deshabilitado se cuenta en
`devicechain_commanddelivery_command_delivery_claims_refused_disabled_total`.

##### Valores por defecto que cambiaron {#v0220-defaults}

###### Más publicaciones en vuelo {#v0220-publish-windows}

En un broker replicado una confirmación tarda entre 8 y 11 ms, y una ruta que espera cada una antes
de enviar la siguiente queda limitada por ello. Cinco rutas mantienen ahora una ventana de
publicaciones en vuelo, cada una con su ajuste. Cada ajuste se lee al arrancar; un valor fuera de 1
a 1024, o un `0` explícito, detiene el servicio.

| Ajuste | Servicio | Por defecto | Antes |
| --- | --- | --- | --- |
| `resolution.publishWindow` | `device-management` | 512 | 128, fijo |
| `capture.publishWindow` | `event-sources` | 512 | 128, fijo |
| `externalMqtt.publishWindow` | `event-sources` | 128 por fuente | 5 por fuente, unos 440 eventos por segundo |
| `brokerPresence.publishWindow` | `event-sources` | 128 | una por sujeto, unas 88 transiciones por segundo |
| `ingest.publishWindow` | `sparkplug-ingest` | 32 por conexión | una, unos 88 mensajes por segundo |

Las dos primeras leen de flujos que reentregan, así que una ventana más profunda solo cuesta
memoria. Las tres últimas entregan como mucho una vez: el broker externo, o el aviso de presencia,
no se repite. Un pod que se detiene limpiamente, o que pasa una fuente a otro pod, espera hasta 8
segundos lo que tiene en vuelo. **Un pod que muere de repente pierde hasta una ventana además de lo
que perdía antes**: hasta 128 eventos por fuente MQTT externa (antes 5), 32 mensajes por conexión
Sparkplug (antes 1) y 128 transiciones de presencia, que repara la siguiente pasada de
reconciliación. Si eso te importa más que el ritmo, baja la ventana. Los ajustes se describen en
[Observabilidad](observability.md) y [Servicios de borde](edge-services.md).

###### El lote de lectura es 256 {#v0220-fetch-batch}

Cuando `infrastructure.nats.fetch.batch` no está definido, un servicio ahora lee 256 mensajes por
petición en lugar de 128. Un valor que definas sigue mandando. En mediciones en GKE,
device-management, que estaba limitado por el viaje de ida y vuelta de cada petición, resolvió un
10 a 12 % más de eventos por segundo con el lote mayor. La ventana de confirmación de cada mensaje
sigue empezando cuando se entrega, así que un lector que procesa los mensajes de uno en uno tiene
ahora unos 234 ms por mensaje, no 469 ms, antes de que se reentregue el último de un lote completo.
Un mensaje reentregado se procesa de nuevo, no se pierde. Si ejecutas un lector así de lento,
define `batch: 128`. Consulta [Lectura anticipada](observability.md#fetch-ahead).

###### device-management pide menos CPU {#v0220-dm-cpu}

`functionalAreas.device-management.measuredRequests.cpu` baja de 800m a 450m, que es el uso medido
de device-management a 6.000 eventos por segundo en esta versión. El límite de CPU sigue en 2
núcleos. Un valor que definas en tus propios valores se conserva.

##### device-state hace menos trabajo por evento {#v0220-device-state}

Un lote de eventos de datos simples ahora escribe todas sus filas de dispositivo en un único upsert
en lugar de una lectura con bloqueo y una reescritura por lote. La cola entre el lector de
device-state y sus escritores se dimensiona a partir del grupo de escritores (640 con los valores
por defecto, antes 100), de modo que los escritores siguen ocupados cuando el servicio va
retrasado. Los escritores también toman los bloqueos de fila en un único orden, lo que elimina un
interbloqueo poco frecuente entre un escritor que crea filas de dispositivo y otro que reclama la
presencia de los mismos dispositivos. A 6.000 eventos por segundo, device-state usó un 21 % menos
de CPU por evento que en la `v0.21.0`; parte de ello es la corrección de lectura de la base de
datos que ya publicó la `v0.21.1`.

##### Medido en GKE {#v0220-performance}

Con la misma forma de clúster de GKE que la ronda de mediciones anterior, una compilación con los
cambios de device-state, device-management y event-sources de esta versión sostuvo 10.000 eventos
por segundo durante 10 minutos en todas las etapas, con la ubicación de los servicios fijada, y
almacenó exactamente una vez cada uno de los 6 millones de eventos, comprobado evento por evento
(consulta [Rendimiento medido](./bootstrap.md#measured-throughput)). Es el mismo ritmo sostenido que antes, no uno mayor: con la ingesta ahora
más rápida, el lado de lectura de device-management pasó a ser la primera etapa en retrasarse, a
unos 10.300 eventos por segundo con una sola réplica. El lote de lectura de 256 es el cambio
dirigido a ello, y dio un 12 % más en una ejecución de 3 minutos. En los cinco servicios de la ruta
de eventos, la CPU a 10.000 eventos por segundo bajó un 6 % respecto a esa ronda, que se ejecutó
antes de la corrección de lectura de la base de datos de la `v0.21.1`.

##### Alertas y métricas nuevas {#v0220-observability}

Alertas nuevas, con `metrics.alerts` activado:

- `ExternalMqttSourceLosingEvents`: los eventos de una fuente MQTT externa no se pudieron publicar
  durante 10 minutos, así que se pierden;
- `BrokerPresenceLosingTransitions`: las transiciones de presencia no llegaron al flujo de eventos
  durante 10 minutos;
- `DeviceCredentialCacheTooSmall` y `DeviceLookupCacheTooSmall`: más del 5 % de las búsquedas de
  device-management expulsaron otra entrada de una caché llena durante 15 minutos;
- `DetectRuleStatWriteFailing`: no se pueden escribir los contadores de disparos de la salud de las
  reglas. La detección no se ve afectada.

Métricas nuevas: `devicechain_<area>_jetstream_publish_window_full_seconds_total`,
`devicechain_<area>_jetstream_publish_window_full_total` y
`devicechain_<area>_jetstream_publish_duplicates_total` para las ventanas de publicación
anteriores, `devicechain_eventsources_total_msg_publish_failed{source}` para MQTT externo y
`devicechain_eventprocessing_detect_rule_stat_write_failures_total` y `devicechain_eventsources_presence_reconcile_unapplied_total`. Ninguna métrica se renombró
ni se eliminó.

##### Correcciones de una revisión de extremo a extremo {#v0220-review-fixes}

- **Los widgets en vivo se recuperan tras una caída de cualquier duración.** Antes, tras unos 40
  segundos sin red o sin servidor, mostraban "Datos no disponibles" hasta recargar la página, y un
  flujo que el servidor terminaba podía dejar de actualizarse mientras el indicador seguía diciendo
  en vivo. Un widget cuyo código no se pudo descargar ahora se puede reintentar con
  **Restablecer widget**.
- **Los datos del panel muestran lo que dicen.** Un slot que se apunta a otro dispositivo con el
  panel en pausa muestra el dispositivo nuevo. Las barras con una barra por dispositivo siguen
  actualizándose. Una tarjeta de estadística sin dispositivo pide elegir un dispositivo en lugar de
  mostrar "0 lecturas". Una celda de la tabla de flota de un dispositivo que informa poco nunca
  muestra la media de un intervalo como su última lectura, y una celda que supera el límite de
  lectura dice "no cargado".

##### Limitación conocida: el historial tras una reconexión {#v0220-known-limitation}

Cuando un panel se reconecta, el historial que se perdió no se vuelve a leer. Un gráfico de líneas
traza una línea recta sobre la caída, y las cifras de flota y agregadas omiten las lecturas
enviadas durante ella. Corregido en la próxima versión: al reconectarse, un panel vuelve a leer la
caída desde el historial almacenado (consulta [tras una reconexión](../concepts/dashboards.md#after-a-reconnect)).

##### También en esta versión {#v0220-also}

- **Paneles.** Las celdas siguen siendo cuadradas y el número de columnas sigue al contenedor, así
  que un teléfono muestra un widget por fila y una pantalla de pared obtiene celdas más grandes. El
  editor se ajusta a las celdas, aparta a los vecinos, funciona con el teclado, puede anclar un
  widget a otro y muestra los problemas de disposición en cada widget; un error de disposición
  bloquea la publicación hasta que **Ajustar a los límites** lo resuelve. También tiene deshacer y
  rehacer, duplicar, y copiar y pegar entre paneles; un widget pegado donde su vínculo de
  dispositivo no existe, como el panel de otro inquilino, recibe un slot nuevo sin vincular y no
  conserva ninguna referencia a dispositivos. Un panel tiene un rango
  de tiempo (de 15 minutos a 7 días) y una pausa; los comandos se siguen enviando durante la pausa.
  Widgets nuevos: indicadores lineal y de nivel, batería, luz de estado, barras de señal, brújula,
  anillo de progreso, control deslizante y conmutador de comandos, tarjeta de estadística,
  gráficos de barras y circular, y la tabla de flota. Los widgets ganan umbrales, unidades y
  precisión, muestran la antigüedad de su valor y dicen por qué están vacíos. `/dash` mantiene viva
  su sesión y tiene un modo quiosco para pantallas de pared.
- **Actualizaciones por aire en la consola:** páginas de Artefactos y Firmantes, y una pestaña de
  Actualizaciones en la página del dispositivo. GraphQL añade `Artifact.signatures`.
- **Límites de nivel.** El formulario de nivel define el techo de comandos retenidos, la prioridad
  de descarte y los tres límites de geocercas, y `AdminTenant.effectiveScalarSettings` informa de
  dónde procede cada valor.
- **La guía de usuario de la consola** tiene 17 capítulos en inglés, español y chino.
- **La tabla de alarmas parte las claves de alarma y los originadores por los guiones**
  (`engine-` / `overheat`) en lugar de en mitad de una palabra.
- **board-replay ya no incluye el polyfill de precarga de módulos de Vite.** En Safari anterior a
  17 y Firefox anterior a 115, los fragmentos compartidos de un widget que se carga al usarse por
  primera vez llegan un viaje de ida y vuelta más tarde. Nada más cambia.

### v0.21.1: un parche, nada que hacer {#v0211-upgrade}

`v0.21.1` es una actualización directa desde `v0.21.0`: `dcctl install` para el clúster y después
`dcctl upgrade` para cada instancia, como en la [v0.21.0](#v0210-upgrade). No añade ninguna
migración, por lo que la base de datos queda intacta, y no cambia ninguna API, tema, permiso,
valor del chart ni clave de configuración. Todo lo que describe la sección de la v0.21.0 que sigue
es exactamente lo que estás ejecutando. Si vienes de `v0.20.0` o una versión anterior, sigue
primero las [notas de la v0.21.0](#v0210-upgrade); se aplican sin cambios.

Cuatro correcciones:

- **Leer de la base de datos vuelve a costar lo que costaba antes de la v0.21.0.** La v0.21.0 fue
  la primera versión compilada con la cadena de herramientas Go 1.27, y con ella el controlador de
  PostgreSQL hacía una inspección de tipos adicional por cada columna de cada fila que leía. Los
  servicios que leen mucho, device-state sobre todo, gastaban por ello bastante más CPU en la
  ingesta. Las filas vuelven a leerse como en `v0.20.0`, y el rendimiento de ingesta de
  device-state queda restablecido. En una prueba de rendimiento centrada en lecturas, la misma
  lectura pasó de 85–95 ms a 31–35 ms.
- **La vista Rule Health cuenta los disparos en PostgreSQL.** Hasta ahora mostraba cero disparos y
  ninguna hora de último disparo para todas las reglas, porque PostgreSQL rechazaba la sentencia
  que registra un disparo. La detección no se vio afectada: las reglas generaron y cerraron
  alarmas con normalidad en todo momento. Los recuentos empiezan con la actualización; los
  disparos anteriores nunca se registraron.
- **Crear una identidad con `enabled: false` crea una identidad deshabilitada.** Antes se guardaba
  habilitada. La misma corrección cubre los demás registros que llevan un indicador de
  habilitación. Una identidad creada deshabilitada con `v0.21.0` o una versión anterior sigue
  habilitada: revisa las que quisiste crear deshabilitadas y deshabilítalas.
- **Detener la fuente de eventos HTTP espera a que sus conexiones terminen de cerrarse**, de modo
  que una fuente detenida ya no actualiza sus métricas después de haberse detenido.

### v0.21.0 — un dispositivo puede quedar excluido por completo, y las actualizaciones se firman {#v0210-upgrade}

`v0.21.0` es una actualización en su sitio desde `v0.20.0`: `dcctl install` para el clúster y
después `dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](#v0170-upgrade). Si
vienes de `v0.19.0` o una versión anterior, lee también las [notas de `v0.20.0`](#v0200-upgrade): sus
recuentos de filas y de fragmentos y sus pasos se aplican también a esta actualización. Una instancia
creada por la `v0.16.0` o una versión anterior sigue teniendo que
[destruirse y crearse de nuevo](#pre-declaration-recreate).

La mayor parte de esta versión es capacidad nueva que no cambia nada hasta que la usas: un
dispositivo puede deshabilitarse por completo, los artefactos de actualización pueden firmarse, una
flota puede importarse desde un archivo CSV, los ajustes preestablecidos de alertas y una
comprobación de notificación se suman al formulario de reglas de detección, y una conexión de inicio
de sesión único puede confiar en una autoridad de certificación privada. Lo que cambia para alguien:
un dispositivo creado para entender solo asignaciones de actualización sin firmar rechaza una
firmada, un artefacto solo puede asignarse cuando lo ha firmado un firmante activo, se rechaza un
Factory Reset de LwM2M en bruto, un dispositivo deshabilitado queda excluido de la configuración, las
actualizaciones y el registro LwM2M, con sus comandos en cola retenidos, y los archivos compilados de
la consola pasan a una ruta nueva. Todos los binarios se compilan ahora con Go 1.27.2.

#### Antes de actualizar {#v0210-before}

1. **Dispositivos que reciben actualizaciones por aire: despliega primero un actualizador que
   verifique firmas.** Una asignación puede llevar ahora `trust: "SIGNED"` con una firma, un id de
   clave y un algoritmo, y un dispositivo que solo entiende `UNVERIFIED` la rechaza. Nada firma una
   asignación hasta que un inquilino registra un firmante y firma un artefacto, así que despliega el
   actualizador verificador antes de hacerlo. Consulta
   [Las asignaciones de actualización pueden firmarse](#v0210-signed-updates).
2. **Decide cómo se confía en los artefactos de actualización.** Con `updates.allowUnverifiedArtifacts`
   en `false`, solo puede asignarse un artefacto firmado por un firmante activo de su inquilino. Si
   activas la opción para asignar artefactos sin firmar, sigue aplicándose. Consulta
   [Las asignaciones de actualización pueden firmarse](#v0210-signed-updates).
3. **Deja de enviar un Factory Reset de LwM2M en bruto.** Un comando que ejecuta `/3/0/5` se rechaza
   al encolarse, también desde una regla o un lote. Consulta
   [Se rechaza un Factory Reset de LwM2M en bruto](#v0210-factory-reset).
4. **Comprueba qué depende de la ruta estática de la consola.** Sus archivos compilados se sirven
   ahora desde `/_app/` en lugar de `/assets/`. Consulta
   [Los archivos compilados de la consola cambian de ruta](#v0210-console-app-path).

#### Durante la actualización {#v0210-during}

- **Actualiza `device-management` y `command-delivery` a la vez.** `command-delivery` conserva su
  propio registro de qué dispositivos están deshabilitados y lo rellena desde `device-management`.
  Los comandos de un inquilino no se despachan hasta la primera sincronización correcta de ese
  inquilino, así que un pod nuevo de `command-delivery` que aún no puede llegar a un pod de
  `device-management` con la nueva consulta los retiene. No se pierden, y salen en cuanto la
  sincronización tiene éxito.
- **Cuatro áreas aplican una migración** en su primer arranque: `device-management` añade a la tabla
  de dispositivos una columna `disabled_at` anulable y un índice parcial, la tabla de ventanas de
  mantenimiento, una columna de ventana en los tipos de dispositivo y una columna de impacto en las
  definiciones de comando; `command-delivery` crea dos tablas pequeñas y añade columnas de
  resultado, de ventana y de reintento a sus comandos y lotes; `update-management` crea el registro
  de firmantes; y `user-management` añade una columna de paquete de CA a los proveedores de
  identidad. Cada una puede volver a ejecutarse por separado, y ninguna requiere recrear. Consulta
  [Tablas y columnas nuevas](#v0210-migrations).
- **La migración de `command-delivery` reescribe filas existentes.** Rellena la nueva columna
  `outcome` de cada comando que ya terminó, con un solo `UPDATE` sobre la tabla de comandos. Un
  comando correcto queda registrado como satisfactorio, uno caducado o cancelado como no entregado,
  y uno fallido o agotado por tiempo como desconocido, porque la fila antigua no dice cuál de ellos
  fue. Con una tabla de comandos grande, el primer arranque de `command-delivery` tarda más, en
  proporción al número de comandos terminados, y PostgreSQL escribe una versión nueva de cada una de
  esas filas, así que cuenta con un volumen de registro de escritura anticipada y de filas muertas
  para autovacuum a la misma escala. La sentencia solo escribe las filas cuyo `outcome` sigue vacío,
  así que volver a ejecutarla nunca sobrescribe un valor. La sonda de arranque de un pod concede
  unos 150 segundos por defecto (`startupProbe` en los valores del chart); si tu tabla es lo
  bastante grande para que el relleno tarde más, sube `startupProbe.failureThreshold` para la
  actualización, o el contenedor se reinicia antes de que termine la migración y la vuelve a
  empezar. Las demás migraciones de esta versión añaden tablas, columnas e índices y no escriben
  ninguna fila.
- **`update-management` empieza a leer el censo de dispositivos** solo a partir de hechos nuevos, así
  que no necesita ponerse al día ni ningún paso del operador.

Volver a `v0.20.0` tras la actualización conserva las columnas y tablas nuevas, que `v0.20.0`
ignora. No conoce los firmantes, así que `allowUnverifiedArtifacts` por sí solo decide lo que puede
asignar.

#### Después de actualizar {#v0210-after}

- **Recarga la consola una vez.** Un navegador que conserva el `index.html` anterior pide las rutas
  de archivo antiguas hasta que se recarga. `index.html` se sirve con `no-cache`, así que basta una
  navegación.
- **Da a los roles personalizados la nueva autoridad si gestionan firmantes.** Los administradores de
  inquilino tienen `artifact:trust`; un rol que hayas definido no. Consulta
  [Las asignaciones de actualización pueden firmarse](#v0210-signed-updates).
- **No se requiere nada más.** Deshabilitar un dispositivo, firmar, importar, los ajustes
  preestablecidos y el paquete de CA empiezan todos sin usarse.

#### Qué cambia {#v0210-what-changed}

##### Un dispositivo deshabilitado queda excluido por completo, y sus comandos en cola se retienen {#v0210-disabled-device}

Un dispositivo puede deshabilitarse por completo en lugar de buscar y revocar cada credencial, con
`updateDevice(enabled: false)` o la acción Deshabilitar de la página del dispositivo. Mientras está
deshabilitado:

- sus eventos se rechazan en la resolución, sus conexiones al broker se rechazan y los comandos
  nuevos para él se rechazan con `DEVICE_DISABLED`;
- se rechazan su sincronización de configuración y sus informes de estado, así como sus informes de
  progreso de actualización y de inventario, sus concesiones de descarga y sus descargas, y un
  enlace de descarga emitido antes de deshabilitarlo responde 403; `assignArtifact` lo rechaza con
  `DEVICE_DISABLED`;
- un registro LwM2M responde 4.03 Forbidden, y un dispositivo registrado se da por terminado en su
  siguiente Update para que se registre de nuevo y sea rechazado;
- sus reglas de ausencia se desarman, de modo que no se genera ni se mantiene para él ninguna alarma
  de «dispositivo en silencio»;
- los comandos ya en cola quedan **retenidos**: ni se envían ni se cancelan. Un comando retenido
  sigue caducando por su propio TTL. Habilitar el dispositivo los libera, los más antiguos primero, y
  eliminar un dispositivo deshabilitado los cancela. `deviceCommandHold` y `cancelDeviceCommands` los
  muestran y los borran.

Habilitar el dispositivo envía una vez su última configuración y vuelve a ofrecer sus asignaciones de
actualización vigentes. Una sesión ya abierta sigue abierta hasta la siguiente comprobación de la
conexión. Consulta [Deshabilitar un dispositivo](../guides/device-credentials.md#disabling-a-device).

Si hoy deshabilitas dispositivos y cuentas con que sus comandos en cola salgan, ahora esperan.

##### Las asignaciones de actualización pueden firmarse {#v0210-signed-updates}

Un inquilino puede registrar las claves públicas en las que confía para firmar artefactos de
actualización: ECDSA P-256 o Ed25519, como un único PEM `PUBLIC KEY`. Una firma sobre el resumen
SHA-256 de un artefacto se verifica al añadirla, y un artefacto cuenta como **firmado** mientras al
menos un firmante activo del mismo inquilino lo haya firmado. Revocar un firmante, que es terminal,
devuelve sus artefactos a sin verificar sin tocarlos. Se rechaza cualquier cosa que contenga una
clave privada.

Qué cambia con esto:

- **La nueva autoridad `artifact:trust`** registra y revoca firmantes. La tienen los administradores
  de inquilino y ningún rol que hayas definido; `artifact:write` añade una firma.
- **Asignar requiere un artefacto firmado**, salvo que `updates.allowUnverifiedArtifacts` sea `true`.
  Esa opción no cambia y sigue siendo `false` por defecto; lo que cambia es que un artefacto firmado
  ya no la necesita.
- **El contrato del dispositivo crece.** Un `update.assignment` para un artefacto firmado lleva
  `trust: "SIGNED"`, una `signature` (base64 estándar), un `keyId` (el SHA-256 en hexadecimal
  minúsculo de la clave pública codificada en DER) y un `algorithm` (`ECDSA_P256_SHA256` o
  `ED25519`); ambos firman el resumen en bruto de 32 bytes. `"v":1` no cambia y una asignación sin
  firmar es idéntica byte a byte a la de antes. Un dispositivo que solo entiende `UNVERIFIED` rechaza
  una asignación `SIGNED`, lo que falla de forma cerrada. Un dispositivo informa `UNKNOWN_SIGNER` o
  `SIGNATURE_INVALID` bajo `FAILED` en la etapa de descarga completada.
- **Una firma no ata la versión.** Firmar el resumen dice quién produjo los bytes, no de qué versión
  son; consulta [Actualizaciones por aire](../concepts/over-the-air-updates.md).

Cuando se asigna un artefacto, la firma más antigua de un firmante activo se **fija** a la
asignación (`UpdateAssignment.signedBy` nombra al firmante), y esa firma es la que recibe el
dispositivo. De ello se derivan dos cosas:

- **Un dispositivo debe tener la clave de cada firmante que pueda fijarse.** Pon la clave de un
  firmante nuevo en tus dispositivos antes de revocar el anterior. Revocar un firmante después de
  fijar la firma no cambia un mensaje ya enviado.
- **Desactivar `allowUnverifiedArtifacts` detiene el envío de asignaciones sin firma**, incluido
  un reenvío cuando un dispositivo sincroniza. Una asignación hecha en `v0.20.0` no tiene firma,
  así que con el ajuste desactivado su dispositivo conserva lo que tiene hasta que asignes un
  artefacto firmado. Una asignación cuya firma fijada no se puede leer no se envía; nunca se envía
  como `UNVERIFIED`.

Para registrar un firmante y firmar un artefacto, sigue [Firmar actualizaciones](../guides/signing-updates.md).

##### Se rechaza un Factory Reset de LwM2M en bruto {#v0210-factory-reset}

Un `lwm2m.execute` sobre el recurso 5 del objeto Device (`/3/0/5`) se rechaza al encolarse con
`COMMAND_DESTRUCTIVE_REFUSED`, tanto para un comando individual como para un lote y para un comando
enviado por una regla de detección, en cualquier grafía de la ruta, y de nuevo antes de que llegue al
dispositivo. Las reglas de detección tratan el código como permanente y no reintentan. Reboot
(`/3/0/4`), los demás execute y las lecturas no se ven afectados. Está prevista una operación con
nombre, confirmada y para un solo dispositivo, para Factory Reset; hasta entonces la ruta en bruto
permanece cerrada. El ejecutor también rechaza cualquier carga de lectura, escritura o ejecución
cuya clave de ruta aparezca más de una vez.

##### Los lotes pueden reintentarse por resultado, y una petición LwM2M sin respuesta es un TIMEOUT {#v0210-batch-retry}

Cada comando que termina lleva ahora un `outcome` almacenado, escrito por la operación que lo terminó:
`SUCCEEDED`, `NOT_DELIVERED` (la plataforma puede demostrar que nunca llegó al dispositivo), `REJECTED`
(el dispositivo dijo que no lo hizo) o `UNKNOWN` (puede haber llegado al dispositivo y haber actuado). Una
migración añade la columna y la rellena para los comandos ya terminados: `SUCCESSFUL` pasa a `SUCCEEDED`,
`EXPIRED` y `CANCELLED` pasan a `NOT_DELIVERED`, y cualquier otro comando terminado, incluido uno que
falló o agotó el tiempo, pasa a `UNKNOWN`. Los `commands` pueden buscarse por `outcomes`.

`retryCommandBatch` emite un lote nuevo, enlazado en ambos sentidos con el original (`retryOf`, `retries`),
para los dispositivos que indiques y que no tuvieron éxito. El comando se copia del original, nunca se
reescribe. Los dispositivos con resultado `UNKNOWN` se rechazan con `RETRY_UNKNOWN_NOT_ACKNOWLEDGED` salvo
que la petición fije `includeUnknown`, de modo que un comando anterior a la actualización que falló o
agotó el tiempo necesita ese indicador para reintentarse. Un dispositivo que ya tiene un comando vivo o
con éxito en cualquier lote de la familia del reintento se rechaza, para que no se envíe un comando dos
veces porque haya dos reintentos abiertos. Requiere `command:write`. La página del lote en la consola
tiene una acción **Reintentar…**. Consulta
[Comandar una flota](../guides/commanding-a-fleet.md#retry-the-devices-that-did-not-succeed).

**Cambio de comportamiento.** Una petición LwM2M que no recibe respuesta del dispositivo se notifica
ahora como `TIMEOUT`, igual que en los demás transportes. Si lees el estado de un comando LwM2M para
decidir qué hacer a continuación, espera `TIMEOUT` ahí.

##### Los comandos disruptivos pueden esperar a una ventana de mantenimiento {#v0210-maintenance-windows}

A un tipo de dispositivo se le puede asignar una **ventana de mantenimiento** semanal recurrente: una zona
horaria y de 1 a 56 franjas semanales, que se gestionan en la API de `device-management` (`device:write`).
Cada definición de comando gana un `impact` de `NORMAL`, `DISRUPTIVE` o `DESTRUCTIVE`; una definición que ya
existe se lee como `NORMAL`, y `lwm2m.reboot` se crea ahora como `DISRUPTIVE`. Nada se retiene hasta que se
asigna una ventana a un tipo de dispositivo y un comando de ese tipo es `DISRUPTIVE` o `DESTRUCTIVE`.

Entonces, para un dispositivo de ese tipo, un comando así encolado mientras la ventana está cerrada se acepta
y **espera**, aún `QUEUED`, hasta la siguiente apertura. Uno encolado con la ventana abierta queda ligado a esa
apertura: si no se ha enviado cuando la ventana se cierra, espera a la siguiente en lugar de salir cuando el
dispositivo vuelva. Esto vale para un comando suelto, un lote y un comando enviado por una regla de detección,
y en cada ruta que envía un comando o confirma uno en vuelo. Un comando cuyo `expiresAt` cae antes de la
siguiente apertura se rechaza con `WINDOW_AFTER_EXPIRY`, en un lote como rechazo por dispositivo. Un lote
informa de cuántos de sus comandos están en `waiting`.

Dos comportamientos que conviene conocer. Si un dispositivo confirma un comando en vuelo después de que la
ventana se haya cerrado, la confirmación se rechaza; el comando se aparca y pasa a la siguiente apertura. Y
desasignar una ventana de un tipo de dispositivo no libera los comandos ya marcados con ella.

Para configurar una, consulta [Ventanas de mantenimiento](../guides/maintenance-windows.md).

##### Los archivos compilados de la consola pasan a `/_app/` {#v0210-console-app-path}

Un enlace a la página de un recurso (`/assets/<token>`) devolvía 404 al cargar o refrescar, porque el
JavaScript y el CSS compilados de la consola estaban bajo `/assets/` y el servidor web respondía 404
a todo lo que allí no tenía. Los archivos compilados están ahora bajo `/_app/`, con la misma caché
inmutable de un año, y la página del recurso carga desde un enlace directo. La aplicación de paneles
bajo `/dash` no cambia. Si usas un proxy, una caché o una lista de permitidos para las rutas de la
consola, permite `/_app/`.

##### Un registro inexistente se responde con `NOT_FOUND` {#v0210-not-found}

Una petición que nombra un rol, nivel de inquilino, identidad, membresía, inquilino, cliente OAuth,
proveedor de identidad o solicitud de vínculo de SSO que no existe, o un dispositivo que `claimDevice`
no encuentra, responde ahora con `extensions.code` `NOT_FOUND`, como dice la
[referencia de GraphQL](../reference/graphql-api.md). Antes solo llevaba un mensaje como
`role not found` y ningún código. Los mensajes no cambian, así que un cliente que comparaba el
mensaje sigue funcionando; un cliente que trataba la ausencia de código como «otro error» debe
manejar el código.

##### Go 1.27.2 acepta horas de evento escritas con escapes JSON {#v0210-go127}

Todos los binarios se compilan con Go 1.27.2. Un dispositivo que escribía la hora de un evento con
escapes JSON en la cadena, como `"2026-10-13T12:00:00Z"` para `...Z`, veía su evento enviado a la
cola de mensajes fallidos al decodificarlo con Go 1.26. Es JSON correcto, y ahora se acepta y se
guarda. No hay que hacer nada.

##### Tablas y columnas nuevas {#v0210-migrations}

| Área | Cambio |
| --- | --- |
| `device-management` | `devices.disabled_at timestamptz` (nulo significa habilitado) y el índice parcial `idx_devices_disabled`; la tabla `maintenance_windows`, `device_types.maintenance_window_id` y `command_definitions.impact` |
| `command-delivery` | `device_gates` y `device_gate_syncs`, con sus índices únicos y parciales; `commands.outcome` (rellenado para los comandos terminados), `command_batches.retry_of` y un índice parcial sobre él; `commands.dispatch_after`, `dispatch_until` y `window_token` con un índice parcial, y `command_batches.waiting` |
| `update-management` | `artifact_signers` y `artifact_signatures` (solo claves públicas y firmas); la confianza de un artefacto se deriva ahora de ellas, y la antigua columna de confianza guardada se conserva, sin escribirse más, de modo que una actualización gradual es segura; cada asignación gana además un `signature_id` nulo, la firma a la que está fijada |
| `user-management` | `ca_bundle` en los proveedores de identidad |

##### El inicio de sesión único puede confiar en una autoridad de certificación privada {#v0210-sso-ca}

Una conexión de proveedor de identidad puede llevar su propio paquete de CA, fijado al crearla o con
`setIdpCaBundle` (una cadena vacía lo borra; ambos necesitan `user:write` en la API de administración
de la instancia). Las llamadas a ese proveedor confían en las raíces del sistema más el paquete, solo
para esa conexión, y siguen pasando por la protección de salida. El descubrimiento debe tener éxito
con el paquete nuevo antes de guardarlo. Consulta
[Configurar el inicio de sesión único](../guides/configuring-sso.md). No cambia nada para una
conexión sin paquete.

##### Otro comportamiento que conviene conocer {#v0210-other}

- **Importación de flota.** `importDevices` admite hasta 1000 filas y responde cada una como
  `CREATED`, `EXISTS`, `CONFLICT`, `INVALID` o `REFUSED`; solo crea, así que ejecutar un archivo de
  nuevo es seguro, y una ejecución en seco no escribe nada. Necesita `device:write`.
- **Configuración de dispositivo.** `restoreDeviceConfiguration` y `copyDeviceConfiguration` vuelven
  a aplicar una revisión anterior o la configuración de otro dispositivo mediante los atributos
  compartidos declarados del dispositivo, generando una revisión nueva. Necesitan `device:write` y
  añaden los códigos `CONFIGURATION_KEY_UNDECLARED` y `CONFIGURATION_CONFLICT`.
- **Exportación de Thing Model.** Una versión publicada de un perfil está disponible como Thing Model
  del W3C Web of Things mediante `DeviceProfileVersion.thingModel`.
- **LwM2M.** `lwm2m.reboot` y `lwm2m.readDeviceInfo` son operaciones con nombre, y el fabricante, el
  modelo, el número de serie, la versión de firmware y el nivel de batería de un dispositivo se leen
  cuando se registra (como máximo una vez por hora) y se conservan como atributos de cliente.
- **Reglas de detección.** El formulario de la consola ofrece ajustes preestablecidos para un
  dispositivo en silencio, un valor mantenido por encima de un límite y una batería baja, y muestra si
  la severidad de la regla llega a alguien. Una alarma de ausencia se borra cuando su dispositivo se
  deshabilita, se elimina o cambia de tipo.
- **Mensajería.** Dos réplicas que migran a la vez la retención de un flujo ya no hacen que una se
  niegue a arrancar.
- **Reintento de lotes.** `retryCommandBatch`, el `outcome` de cada comando terminado y la búsqueda por `outcomes` se describen en [Los lotes pueden reintentarse por resultado](#v0210-batch-retry).
- **Ventanas de mantenimiento.** Las ventanas, `impact` y la retención se describen en [Los comandos disruptivos pueden esperar a una ventana de mantenimiento](#v0210-maintenance-windows).

### v0.20.0 — inicia sesión con tu propio proveedor de identidad; los widgets se distribuyen como paquetes {#v0200-upgrade}

`v0.20.0` es una actualización en su sitio desde `v0.19.0`: `dcctl install` para el clúster y
después `dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](#v0170-upgrade). Si
vienes de `v0.18.0`, lee también las [notas de `v0.19.0`](#v0190-upgrade): sus recuentos de filas y
de fragmentos y sus pasos se aplican también a esta actualización. Una instancia creada por la
`v0.16.0` o una versión anterior sigue teniendo que [destruirse y crearse de nuevo](#pre-declaration-recreate).

La mayor parte de esta versión es capacidad nueva que no cambia nada hasta que la usas: las personas
pueden iniciar sesión mediante tu propio proveedor de identidad OpenID Connect, los paneles pueden
mostrar widgets de tus propios paquetes, los dispositivos pueden recibir configuración y las
actualizaciones por aire llegan como una primera entrega. Lo que cambia para todos: cada usuario
inicia sesión de nuevo una vez, los techos de tasa se renombran según lo que cuentan, se rechazan
las credenciales de dispositivo X.509, la contraseña de un dispositivo se conserva solo como un
resumen irreversible, los paneles publicados antes de la actualización tienen que publicarse de
nuevo y muchas entradas de la API pasan a tener un límite. Los servicios que procesan cada evento
se ejecutan con un recolector de basura ajustado, y `device-management` conserva sus búsquedas
durante minutos en lugar de segundos.

#### Antes de actualizar {#v0200-before}

1. **Renombra las claves de los techos de tasa** en cualquier `config` de servicio que fijes:
   `ingestRateLimit.messagesPerSecond` pasa a ser `ingestRateLimit.readingsPerSecond` (`event-sources`,
   `lwm2m-ingest`, `sparkplug-ingest`) y `outboundMessagesPerSecond` pasa a ser
   `outboundCallsPerSecond` (`event-processing`, `outbound-connectors`). Un servicio que aún encuentra
   una clave anterior se niega a arrancar. Los scripts que leen o fijan techos de inquilinos mediante
   la API también necesitan los nuevos nombres de campo. Consulta
   [Los techos de tasa se renombran](#v0200-rate-key-rename).
2. **Saca los dispositivos de las credenciales X.509.** Una credencial de tipo `X509_CERTIFICATE`
   deja de autenticar con la actualización. Da antes a cada uno de esos dispositivos una credencial
   `ACCESS_TOKEN` o `MQTT_BASIC`. Consulta
   [Ya no se aceptan credenciales de dispositivo X.509](#v0200-x509).
3. **En un almacén de objetos S3, concede a la credencial `s3:ListBucket`** sobre el bucket, o el
   borrado de un inquilino eliminado no termina. Consulta
   [Eliminar un inquilino ahora borra todo lo guardado bajo su prefijo del almacén de objetos](#v0200-object-prefix-erase).
4. **En un almacén de eventos grande, plantéate construir antes dos índices.** El primer arranque de
   `event-management` construye dos índices sobre los eventos base aún sin comprimir, y se niega, sin
   cambiar nada, cuando hay más de 4 000 000 de filas que indexar o una tabla tiene más de 500
   fragmentos. Ambas secciones dan las sentencias que construyen el índice a mano, antes o después de
   la actualización: [La lista de eventos de un dispositivo lee solo las filas de su inquilino](#tenant-device-index)
   y [Dos dispositivos pueden enviar el mismo `altId` en el mismo instante](#device-alt-id-key).
5. **Si fijas `gogc` o `goMemLimitPercent` en el nivel superior de los valores del chart**, los cinco
   servicios que procesan cada evento fijan ahora los suyos, que prevalecen. Consulta
   [Los servicios de la ruta de eventos se ejecutan con un recolector de basura ajustado](#v0200-gogc).
6. **Comprueba la memoria de `device-management` para una flota grande.** Sus búsquedas se conservan
   ahora durante 5 minutos, y cada réplica necesita sitio para toda la flota activa. Consulta
   [`device-management` conserva sus búsquedas durante minutos](#v0200-resolve-caches).
7. **Detén cualquier otra cosa que lea el flujo `device-events-capture`**, como `nats stream view`.
   Ahora solo admite el consumidor que posee `event-sources`. Consulta
   [El flujo de entrada de dispositivos elimina cada mensaje una vez reenviado](#v0200-capture-stream).
8. **Vuelve a crear toda identidad cuya dirección de correo tenga un dominio no ASCII**
   (`user@bücher.example`), o no sea un `local@dominio` válido. Tras la actualización no puede
   iniciar sesión hasta que un administrador la vuelva a crear. Elimínala, créala de nuevo con el
   dominio en su forma `xn--` (`user@xn--bcher-kva.example`) y restaura sus membresías. Consulta
   [El dominio de un correo ahora tiene una sola grafía](#v0200-email-idna).
9. **Revisa tus clientes de la API y los paquetes con los que compilas** frente a
   [Peticiones que ahora se rechazan](#v0200-input-bounds),
   [Paneles: publicar pone en vigor una versión](#v0200-dashboard-publishing) y
   [Los paquetes publicados](#v0200-packages).

#### Durante la actualización {#v0200-during}

- **Las contraseñas de dispositivo se convierten.** El primer arranque del nuevo `device-management`
  sustituye cada contraseña `MQTT_BASIC` guardada por un resumen irreversible. Hasta que todos los
  pods de `device-management` ejecutan la nueva versión, un pod anterior no encuentra contraseña para
  una credencial convertida, así que rechaza las conexiones MQTT de ese dispositivo y las
  comprobaciones de contraseña de sus eventos; el dispositivo reintenta y llega a un pod nuevo. Una
  contraseña establecida en cualquiera de las dos versiones durante el despliegue es la que perdura.
- **Actualiza `event-sources` y `device-management` a la vez.** Mientras ejecutan versiones
  distintas, se pueden rechazar los eventos que llevan una contraseña de dispositivo o un token de
  acceso en el cuerpo. Consulta
  [Eventos que llevan una contraseña de dispositivo o un token de acceso en el cuerpo](#v0200-body-password).
- **Evita modificar inquilinos o niveles hasta que termine el despliegue.** Un pod de ingesta o de
  salida anterior mide a todos los inquilinos con el valor predeterminado de la plataforma, y un pod
  de `user-management` anterior hace fallar las lecturas y escrituras de inquilinos que atiende,
  hasta que se sustituye. Consulta [Los techos de tasa se renombran](#v0200-rate-key-rename).
- **`event-management` construye dos índices** en su primer arranque; las escrituras de eventos base
  esperan mientras se construye cada uno, y las lecturas no.
- **`event-sources` reenvía lo que contiene el flujo de entrada de dispositivos y después lo crea de
  nuevo.** Si el flujo no queda vacío en cinco minutos, `event-sources` se detiene con un error y no
  elimina nada.
- **La detección pasa a un nuevo consumidor de los eventos resueltos.** Consulta
  [La detección lee sus eventos mediante un nuevo consumidor](#v0200-ack-floor).

Volver a `v0.19.0` tras la actualización deja sin poder autenticarse a todos los dispositivos que
usan una contraseña `MQTT_BASIC`, porque `v0.19.0` no sabe leer un resumen; si lo haces, establece
contraseñas nuevas y cárgalas en esos dispositivos. Los cambios de índices y de flujo anteriores
indican cada uno lo que exige volver atrás.

#### Después de actualizar {#v0200-after}

- **Todos inician sesión de nuevo una vez**, y los clientes OAuth, como los agentes MCP, se
  autorizan de nuevo. Consulta
  [Cerrar sesión ahora termina la sesión en el servidor](#v0200-server-side-logout).
- **Publica de nuevo cada panel que deban ver los espectadores.** Hasta entonces, a los espectadores
  se les indica que no está publicado. Consulta
  [Paneles: publicar pone en vigor una versión](#v0200-dashboard-publishing).
- **Pueden dispararse alertas nuevas.** `SafetyGateDisabled` se dispara por una compuerta de
  seguridad opcional sin la que se ejecuta un servicio. Las demás se enumeran en
  [Alertas nuevas](#v0200-alerts).
- **Una instancia creada con bootstrap antes de esta versión no tiene almacén de objetos, y la
  actualización no le añade uno.** Los logotipos de inquilino y los artefactos de actualización lo
  necesitan. Consulta
  [`dcctl bootstrap` puede dar a una instancia un almacén de objetos](#v0200-object-store).

#### Qué cambia {#v0200-what-changed}

##### Cerrar sesión ahora termina la sesión en el servidor, y todos inician sesión de nuevo una vez {#v0200-server-side-logout}

Cerrar sesión en la consola o en la aplicación de paneles incrustada solo borraba el navegador: el token de actualización seguía siendo válido hasta 7 días, y el token de inicio de sesión que conserva la consola aún podía iniciar una sesión nueva durante hasta 15 minutos. Una nueva mutación `logout` termina el inicio de sesión en el servidor. Desde ese momento se rechazan los intercambios de ese inicio de sesión: sus tokens de actualización ya no se renuevan, en ningún inquilino, su token de inicio de sesión ya no puede elegir un inquilino, y una concesión OAuth autorizada durante ese inicio de sesión ya no se renueva. La página de autorización de MCP inicia sesión por su cuenta, así que cerrar sesión en la consola no termina una concesión hecha allí; termina cuando caduca ese inicio de sesión (7 días por defecto). Los demás inicios de sesión de la misma cuenta no se ven afectados. Los tokens de acceso y los tokens de inicio de sesión ya emitidos, también en la API de administración, no se pueden revocar y siguen siendo válidos hasta que caducan, en un plazo de 15 minutos. Si no se puede contactar con el servidor al cerrar sesión, la aplicación cierra la sesión localmente igualmente e informa de que no se pudo terminar la sesión del servidor.

Cada inicio de sesión es ahora un registro que dura lo mismo que un token de actualización (7 días por defecto), de modo que una sesión que se renueva continuamente termina 7 días después del inicio de sesión y el usuario vuelve a iniciar sesión. Eso incluye una concesión OAuth, como la de un agente MCP: termina 7 días después del inicio de sesión en la página de autorización, y el cliente se autoriza de nuevo.

Los tokens emitidos antes de la actualización no tienen registro de inicio de sesión y se rechazan, por lo que **todos inician sesión de nuevo una vez**: las sesiones de la consola, de la aplicación de paneles, del SDK y de OAuth terminan en su siguiente renovación, dentro de los 15 minutos posteriores a la actualización salvo que hayas cambiado la vida del token de acceso. Los clientes OAuth, como los agentes MCP, se autorizan de nuevo. No hay que hacer nada más.

##### Los eventos que llevan una contraseña de dispositivo o un token de acceso en el cuerpo se ven afectados mientras conviven versiones {#v0200-body-password}

Desde esta versión, `event-sources` y `device-management` se pasan en otra forma la contraseña de
dispositivo o el token de acceso que llega en el cuerpo de un evento, y cada uno necesita la versión
nueva para leer la del otro. Mientras conviven versiones antiguas y nuevas de ambos durante una
actualización progresiva:

- Los eventos que llevan en el cuerpo una contraseña de dispositivo (`MQTT_BASIC`), por HTTP o MQTT, se
  rechazan como no autenticados y no se reproducen.
- Los eventos que llevan en el cuerpo un token de acceso (`ACCESS_TOKEN`) se rechazan con
  `deviceAuthMode: required`. Con `deviceAuthMode: optional` se aceptan con el token de dispositivo que
  nombran, sin comprobar su credencial.
- Las conexiones MQTT no se ven afectadas.

Actualiza `event-sources` y `device-management` a la vez, o acepta la breve pérdida de esos eventos.

##### La contraseña de un dispositivo `MQTT_BASIC` se muestra una vez y no puede volver a mostrarse {#v0200-device-password-show-once}

La plataforma conserva ahora solo un resumen irreversible con clave de la contraseña de un dispositivo `MQTT_BASIC`, no la contraseña. La API nunca devolvía la contraseña en una lectura, así que ningún resultado de consulta cambia. Lo que cambia es que nadie puede recuperarla tampoco de la plataforma: la consola muestra una contraseña una sola vez, justo después de agregar la credencial o de establecer una nueva, con un botón de copiar, y después solo indica que hay una contraseña establecida. La fila de la credencial incorpora la acción **Establecer nueva contraseña**, y el formulario ofrece **Generar contraseña segura**.

Las contraseñas existentes se convierten en resúmenes la primera vez que device-management arranca con la nueva versión, y los dispositivos siguen autenticándose con la contraseña que ya usan. Una contraseña que no anotaste no puede mostrarse después; establece una nueva y cárgala en el dispositivo. Consulta [Contraseñas que se muestran una vez y su rotación](../guides/device-credentials.md#show-once).

No hay nada que hacer.

##### La lista de eventos de un dispositivo lee solo las filas de su inquilino {#tenant-device-index}

El índice que sirve la lista de eventos de un dispositivo empieza ahora por el inquilino. En
`v0.19.0`, el total de la lista, y la lista de un dispositivo filtrada por tipo de evento, recorrían
todas las filas aún sin comprimir con el token de ese dispositivo, incluidas las de un dispositivo con
el mismo token en otro inquilino, así que un `gateway-1` con mucho tráfico en un inquilino ralentizaba
la lista de `gateway-1` en otro. Ahora recorren solo las filas del propio dispositivo, y una consulta
SQL o de BI sobre los eventos de un dispositivo gana lo mismo. Cada evento base almacenado sigue
actualizando el mismo número de índices: el nuevo sustituye al anterior, y es algo más ancho porque
incluye el inquilino.

- **Durante la actualización.** El primer arranque del nuevo `event-management` construye el índice
  sobre los eventos base aún sin comprimir (por defecto, aproximadamente la última semana) y después
  elimina el índice al que sustituye. Mientras construye, las escrituras de eventos base esperan y las
  lecturas no; la construcción está acotada a 40 segundos una vez bloqueada la tabla. Se niega, sin
  cambiar nada, cuando hay más de 4 000 000 de filas que indexar o la tabla tiene más de 500
  fragmentos, y se detiene de forma definitiva (hasta que el índice exista) si la construcción agota su
  tiempo. Ninguno de estos casos obliga a recrear la instancia: el error incluye las sentencias que
  construyen el índice a mano. Mientras tanto, el `event-management` anterior sigue almacenando
  eventos.
- **Para construirlo a mano, en un momento tranquilo.** Ejecuta esto en el primario del almacén de
  eventos y reinicia `event-management`, que encuentra el índice y solo elimina el que sustituye. La
  primera sentencia elimina un índice inacabado del mismo nombre que deja una construcción
  interrumpida, y no hace nada si no lo hay:

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_time;
  CREATE INDEX idx_events_tenant_device_time
    ON "event-management".events (tenant_id, device_token, occurred_time DESC)
    WITH (timescaledb.transaction_per_chunk);
  ```

  La construcción avanza fragmento a fragmento, así que una escritura espera solo mientras se indexa
  el fragmento al que va dirigida: unos 3 microsegundos por cada evento base de ese fragmento aún sin
  comprimir. Mientras se indexa un fragmento, el `event-management` anterior y el nuevo esperan para
  almacenar eventos en él, y un evento retenido más allá de la espera de confirmación del broker, de
  60 segundos, se entrega de nuevo, lo cual es seguro. En una instancia con mucho tráfico, elige un
  momento tranquilo.
- **Para evitar el rechazo, construye el índice antes de actualizar.** En una instancia `v0.19.0` con
  más de `4000000` eventos base sin comprimir, ejecuta primero las dos sentencias anteriores;
  `v0.19.0` funciona con ellas, y la actualización entonces solo elimina el índice anterior. Para
  contar los eventos base, usa la consulta de [Comprueba el número de filas](#v0190-row-count) con
  `hypertable_name IN ('events')`.
- Volver a `v0.19.0` conserva el nuevo índice, y `v0.19.0` funciona con él.

##### Dos dispositivos pueden enviar el mismo `altId` en el mismo instante y se guardan ambos eventos {#device-alt-id-key}

El `altId` de un evento es el identificador de mensaje del propio emisor, y el lado de escritura lo
usaba, junto con el `occurredTime` del evento, para descartar una copia reentregada. Esa clave era
por inquilino, así que cuando dos dispositivos del mismo inquilino enviaban el mismo `altId` en el
mismo instante, el segundo evento se tomaba por una reentrega del primero y se descartaba sin aviso.
Ahora la clave incluye el dispositivo: se guardan ambos eventos, y un dispositivo que vuelve a
enviar el mismo `altId` en el mismo instante sigue siendo deduplicado. Si dependía de que el `altId`
de un dispositivo suprimiera el de otro, eso ya no ocurre; haga usted mismo que el `altId` sea único
entre sus dispositivos si lo necesita.

- **En la actualización.** El primer arranque del nuevo `event-management` construye el nuevo índice
  único sobre los eventos base que aún no están comprimidos y después elimina el índice por inquilino
  que reemplaza; no hay ningún momento sin una protección de deduplicación. Mientras se construye,
  las escrituras de eventos base esperan y las lecturas no; la construcción está acotada a 40
  segundos una vez bloqueada la tabla. Se rehúsa, sin cambiar nada, cuando hay más de 4.000.000 de
  filas por indexar o la tabla tiene más de 500 fragmentos, y un arranque que no consigue bloquear la
  tabla o terminar a tiempo no cambia nada y el siguiente arranque lo intenta de nuevo. Nada de esto
  requiere recrear: el error incluye las sentencias que construyen el índice a mano. El
  `event-management` anterior sigue guardando eventos mientras tanto.
- **Para construirlo a mano, en un momento tranquilo.** Ejecute esto en el primario del almacén de
  eventos y reinicie `event-management`, que encuentra el índice y solo elimina el que reemplaza. La
  primera sentencia elimina un índice inacabado del mismo nombre que deja una construcción
  interrumpida, y no hace nada si no hay ninguno:

  ```sql
  DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_alt_id;
  CREATE UNIQUE INDEX idx_events_tenant_device_alt_id
    ON "event-management".events (tenant_id, device_token, alt_id, occurred_time)
    WHERE alt_id IS NOT NULL;
  ```

  Un índice único se construye de una sola vez, de modo que las escrituras de eventos base esperan
  durante toda la construcción y las lecturas no. Un evento retenido más que la espera de confirmación de 60
  segundos del broker se entrega de nuevo, lo cual es seguro. En una instancia ocupada, elija un
  momento tranquilo.
- **Para evitar el rechazo, construya el índice antes de actualizar.** En una instancia `v0.19.0`
  con más de `4000000` eventos base sin comprimir, ejecute primero las dos sentencias anteriores;
  `v0.19.0` funciona con ellas y la actualización solo elimina el índice antiguo. Para contar los
  eventos base, use la consulta de [Comprobar el número de filas](#v0190-row-count) con
  `hypertable_name IN ('events')`.
- Volver a `v0.19.0` conserva el nuevo índice y `v0.19.0` funciona con él: los eventos reentregados
  siguen deduplicándose, pero esa versión sigue comprobando por inquilino, así que vuelve a
  descartar el evento del segundo dispositivo.

##### El flujo de entrada de dispositivos elimina cada mensaje una vez reenviado {#v0200-capture-stream}

El flujo que guarda cada publicación de un dispositivo hasta que `event-sources` la reenvía
(`device-events-capture`) ahora elimina un mensaje cuando `event-sources` lo confirma. En `v0.19.0`
conservaba cada mensaje hasta siete días después de reenviarlo. Una publicación que aún no se ha
reenviado se conserva exactamente como antes, así que una caída de `event-sources` sigue sin perder
nada.

- **En la actualización.** El primer arranque del nuevo `event-sources` reenvía lo que el flujo
  existente aún contiene, espera a que quede vacío y entonces elimina el flujo y lo crea de nuevo
  con la nueva retención. Los números de secuencia saltan por delante de los del flujo anterior, así que ninguno se reutiliza. Una
  publicación de dispositivo que llegue en el instante entre la comprobación de vacío y la
  recreación puede perderse después de que el broker la haya confirmado. Si el flujo no queda vacío
  en cinco minutos, `event-sources` se detiene con un error que nombra el flujo y no elimina nada;
  vuelva a iniciarlo cuando pueda reenviar.
- **Nada más puede leer este flujo.** Admite un solo consumidor, el que posee `event-sources`, así
  que `nats stream view` y cualquier otro consumidor sobre `device-events-capture` dejan de
  funcionar. Para ver lo que envían los dispositivos, lea el flujo `inbound-events`, que contiene
  los eventos decodificados.
- Volver a `v0.19.0` exige eliminar antes el flujo, con `event-sources` detenido, porque `v0.19.0`
  se niega a arrancar contra un flujo con la nueva retención; lo crea de nuevo en su siguiente
  arranque. Lo que aún no se haya reenviado cuando lo elimine se pierde.

##### Un evento entrante cuyo registro de fallo no se puede codificar ya no se guarda como un registro vacío {#v0200-failed-record-encode}

Cuando device-management no podía codificar el registro de un evento entrante que había fallado,
guardaba de todos modos un registro vacío y confirmaba el evento. El registro se leía como un fallo
sin motivo ni texto, y el fallo real no quedaba registrado en ningún sitio. Ahora no publica nada
para ese evento, lo confirma y cuenta la pérdida en `dead_letter_lost_total`, por lo que se dispara
`DeadLetterWriteLost` y la línea `LOST` del registro del pod indica de qué evento se trata. Ningún
evento que pueda enviar un dispositivo llega hoy hasta ahí; es una ruta de defecto, y la alerta es
la forma de enterarse de uno. Por eso `DeadLetterWriteLost` tiene una cuarta causa, indicada en
[la tabla de alertas](./detection-engine.md#what-to-watch).

No hay que hacer nada.

##### Un servicio que no alcanza el broker de mensajería al arrancar se detiene al cabo de un minuto {#v0200-broker-unreachable-at-start}

Cada servicio espera al broker de mensajería (NATS) al arrancar. En `v0.19.0` esperaba 30 segundos
y luego arrancaba de todos modos. Los servicios que crean un bucket al arrancar se detenían entonces
con un error que culpaba a la versión del broker (`key-value requires at least server version
2.6.2`), y los demás se declaraban listos sin poder enviar ni recibir nada. Ahora un servicio espera
hasta un minuto y, si el broker no ha respondido para entonces, se detiene con un error que indica
la dirección del broker y el último error de conexión, y Kubernetes lo reinicia. Un broker que
arranca dentro de ese minuto se trata igual que antes.

- **Un pod que se reinicia con `the NATS broker was not reachable before the startup connection
  wait ran out`** no pudo conectarse al broker en la dirección de la configuración de la instancia.
  Compruebe que los pods del broker están en ejecución y listos, y lea el error de conexión al final
  del mensaje: un nombre que no se resuelve, una conexión rechazada y un certificado o credencial
  rechazados apuntan a sitios distintos.
- **Un broker ausente durante más tiempo cuesta reinicios, no un servicio que se queda caído.** Cada
  reinicio vuelve a intentarlo, con la espera entre reinicios habitual de Kubernetes, y el servicio
  arranca en cuanto el broker responde.

No hay que hacer nada.

##### `dcctl install --dry-run` aplica los rechazos de reinstalación que aplica la instalación {#v0200-dry-run}

En un clúster que responde, una ejecución en seco ahora lee el registro de instalación, pregunta
qué instancias se ejecutan en el clúster cuando cambian los ajustes, y falla con el mismo mensaje que la instalación cuando
esta se rechazaría por lo que el clúster ya contiene: una reinstalación desde una máquina sin
estado del clúster, ajustes cambiados mientras hay instancias en ejecución, o un
`--backup-snapshot-class` que no puede funcionar. Antes imprimía un plan y salía con 0. Una
ejecución en seco contra un clúster que aún no existe, o al que no llega, sigue imprimiendo su plan
e indica que esas comprobaciones no se hicieron. Una ejecución en seco no comprueba todo lo que la
instalación rechaza; por ejemplo, un clúster cuyo almacén relacional fue creado por una versión
anterior se sigue rechazando solo cuando la instalación se ejecuta.

No hay nada que hacer.

##### `dcctl install` y `dcctl bootstrap` esperan a que se hayan unido todas las instancias de la base de datos {#v0200-wait-database-instances}

Antes, `dcctl install` volvía en cuanto el primario de la base de datos relacional aceptaba una
conexión, y `dcctl bootstrap` solo esperaba a los servicios, de modo que cualquiera de los dos
podía informar de éxito, y bootstrap imprimir que ambas bases de datos estaban replicadas, mientras
una réplica aún se estaba creando. Ahora install se registra, libera el bloqueo del clúster y espera
hasta 15 minutos a que se hayan unido todas las instancias de la base de datos relacional. Bootstrap
espera a los servicios y después, hasta 15 minutos, a todas las instancias del almacén de eventos de
la instancia. Si alguna no se ha unido a tiempo, el comando termina con un error que nombra la base
de datos y cuántas de sus instancias están listas. Ejecute de nuevo el mismo comando para seguir
esperando (en install, eso vuelve a aplicar los requisitos previos y rechaza los bootstraps mientras
se ejecuta, así que si solo quiere seguir la base de datos obsérvela con `kubectl`); un bootstrap
que terminó así no se deshace, y la instalación ya está registrada, así que mientras tanto se
pueden hacer bootstrap de instancias. Una instalación sana añade unos segundos.

No hace falta nada más.

##### Los techos de tasa se renombran según lo que cuentan {#v0200-rate-key-rename}

El techo de ingesta cuenta lecturas desde hace tiempo, y el techo de salida cuenta llamadas de
conector, pero ambos seguían nombrados por mensajes. Se renombran en todas partes, sin conservar la
grafía anterior:

| Antes | Ahora |
| --- | --- |
| `ingestRateLimit.messagesPerSecond` (configuración de `event-sources`, `lwm2m-ingest`, `sparkplug-ingest`) | `ingestRateLimit.readingsPerSecond` |
| `outboundMessagesPerSecond` (configuración de `event-processing`, `outbound-connectors`) | `outboundCallsPerSecond` |
| `ingestMessagesPerSecond` (campo GraphQL, clave de configuración de nivel) | `ingestReadingsPerSecond` |
| `outboundMessagesPerSecond` (campo GraphQL, clave de configuración de nivel) | `outboundCallsPerSecond` |

- **Antes de actualizar, renombre las claves de configuración de los servicios.** Si sus valores
  fijan alguna de las claves anteriores en el `config` de un servicio, cámbiela por el nombre
  nuevo. Un servicio que aún encuentra una clave anterior se niega a arrancar, y su error indica la
  clave que debe usar. No recurre al valor predeterminado de la plataforma, porque eso sustituiría
  en silencio el techo que usted fijó.
- **Clientes de la API.** Se renombran los campos de `tenantGovernance`, del tipo de inquilino de
  la API de administración y de sus entradas de creación y actualización. Una petición que
  selecciona o envía un nombre anterior ahora falla, así que actualice cualquier script o
  integración que gestione techos de inquilinos. Una configuración de nivel que usa una clave
  anterior se rechaza, y su error enumera las claves aceptadas.
- **Los datos guardados se convierten solos.** La actualización renombra las columnas de
  sobrescritura del inquilino y cambia las claves de cada configuración de nivel guardada, incluidos
  los niveles de serie y los eliminados, con los mismos valores.
- **Durante el despliegue.** Hasta que todos los pods ejecutan la nueva versión, un pod anterior y
  un `user-management` nuevo no se entienden. Un pod de ingesta o de salida anterior pide los campos
  anteriores de `tenantGovernance`, la petición falla y ese pod mide a todos los inquilinos con el
  valor predeterminado de la plataforma; `TenantsMeteredAtPlatformDefault` puede dispararse por
  él. Un pod de `user-management` anterior sigue usando los nombres de columna anteriores una vez
  que el pod nuevo los ha renombrado, así que las lecturas y escrituras de inquilinos que atiende
  fallan hasta que se sustituye. Ambas cosas terminan al acabar el despliegue; evite modificar
  inquilinos o niveles hasta entonces.

##### Eliminar un inquilino ahora borra todo lo guardado bajo su prefijo del almacén de objetos {#v0200-object-prefix-erase}

El borrado de un inquilino eliminado solo quitaba los objetos que nombraba una fila de
user-management. Ahora también quita todos los objetos bajo el prefijo de ese inquilino en el
almacén de objetos, sea cual sea el servicio que los guardó, de modo que los artefactos de
actualización y cualquier objeto cuya referencia se perdió se borran con el inquilino.

- **En el backend S3, la credencial ahora necesita `s3:ListBucket` sobre el bucket**, además de
  `s3:GetObject`, `s3:PutObject` y `s3:DeleteObject`. Sin ello, el paso de objetos del borrado de
  un inquilino falla y se reintenta en cada pasada, así que el borrado no termina. Concédelo antes
  de actualizar. El backend filesystem no necesita nada.

##### El token de un inquilino eliminado se retiene hasta que todo lo emitido para él haya caducado, y un inquilino nuevo con ese token empieza limpio {#v0200-token-hold}

El token de un inquilino eliminado se liberaba un tiempo fijo después de empezar la eliminación. La
retención (`tokenHoldSeconds`, 12 horas por defecto) ahora se cuenta desde que el último sistema de
almacenamiento terminó de borrar los datos del inquilino, más un minuto de margen, de modo que una
credencial emitida para el inquilino eliminado haya caducado antes de que el token pueda reutilizarse.
Por eso una eliminación retiene su token algo más que antes. Tres cambios lo respaldan:

- **No se emite ninguna credencial de dispositivo una vez iniciado el borrado.** Desde ese momento se
  rechaza la conexión de un dispositivo de ese inquilino.
- **Los servicios LwM2M y Sparkplug olvidan una búsqueda de dispositivo en caché después de una hora**,
  y un registro LwM2M de un inquilino eliminado se termina en lugar de prorrogarse.
- **Se rechaza un token de actualización emitido en un segundo anterior a la creación del inquilino**, para cualquier
  llamante. Cuando se elimina un inquilino y se crea otro con el mismo token, quienes tenían la sesión
  iniciada en el primero deben volver a iniciar sesión.

Las entradas de auditoría conservadas de un inquilino eliminado ahora se archivan bajo la eliminación y
no bajo el token del inquilino, así que el diario del inquilino nuevo empieza vacío. El tipo
`TenantDeletion` de la API de administración incorpora `retainedAuditTenant`, que es el filtro `tenant`
de `auditEvents` que lee esas entradas; en la consola, **Administración → Auditoría** muestra cada
eliminación completada como `<token> (eliminado el <fecha>)` en su filtro de inquilino. No hay nada que
hacer. Consulta [Eliminación de un inquilino](./tenant-deletion.md#audit).

##### Un registro que no existe se responde con `NOT_FOUND` {#v0200-not-found}

Una petición que nombra un registro que no existe llegaba al cliente como un `record not found` sin
`extensions.code` en la mayoría de los servicios. Ahora lleva `extensions.code` con el valor
`NOT_FOUND`, y el mensaje no cambia. Un rechazo que ya tenía su propio código para un registro
inexistente, como `ARTIFACT_NOT_FOUND`, lo conserva. En esta versión las API de user-management todavía
responden algunos casos (un inquilino, un rol, una identidad o una pertenencia que no existe) con un
mensaje como `tenant not found` y sin código. Un cliente que distinguía un registro
inexistente de otros fallos leyendo el mensaje puede pasar a usar el código. Consulta la
[referencia de GraphQL](../reference/graphql-api.md#not-found).

##### El inicio de sesión único es nuevo y no te pide nada {#v0200-sso}

Las personas ahora pueden iniciar sesión mediante un proveedor de identidad externo OpenID Connect, y un inquilino puede exigirlo. Nada se activa hasta que un superusuario crea una conexión, así que una instancia actualizada se comporta como antes. No hay nada que migrar y el inicio de sesión con contraseña sigue funcionando. El nuevo inicio de sesión que provoca el [cierre de sesión en el servidor](#v0200-server-side-logout) ya está descrito.

- **Configurar un proveedor** se describe en [Configurar el inicio de sesión único](../guides/configuring-sso.md), y su comportamiento en [Inicio de sesión único](../concepts/single-sign-on.md).
- **La pantalla de inicio de sesión empieza solo con la dirección de correo.** A una dirección cuyo dominio tiene una conexión habilitada y aprobada se le ofrece **Continuar con el inicio de sesión único**, con **Usar una contraseña** al lado; cualquier otra dirección pasa al campo de contraseña. `beginSso` llamado con un correo responde `null` para un dominio sin conexión y para uno cuya conexión está deshabilitada o pendiente de aprobación. Una conexión indicada por token sigue rechazando con «single sign-on is not available». El inicio de sesión con contraseña nunca se elimina, así que una cuenta que solo tiene contraseña no queda aislada.
- **Cada persona vincula su propia cuenta.** Quien inició sesión con contraseña puede vincular una cuenta de inicio de sesión único desde el menú de usuario. `beginSsoLink` no recibe un token de conexión: el servidor usa la conexión para la que un superusuario verificó el dominio del correo de la persona. Cada rechazo lleva un código (`SSO_LINK_SESSION_REQUIRED`, `SSO_LINK_PASSWORD_REQUIRED`, `SSO_LINK_NOT_ALLOWED`, `SSO_LINK_NO_CONNECTION`, `SSO_LINK_ALREADY_LINKED`), y un rechazo en el retorno desde el proveedor indica qué comprobación falló.
- **Una conexión también puede cerrar la sesión en el proveedor de identidad.** Está desactivado hasta que un administrador lo activa, porque cerrar sesión en el proveedor termina la sesión allí para todas las aplicaciones que atiende. La actualización añade el ajuste (`providerSignOut`, desactivado por defecto) a cada conexión; nada cambia hasta que se establece. `logout` acepta un `returnTo` opcional (`CONSOLE` o `DASHBOARD`) y responde `SignOut { providerSignOutUrl }`, que es `null` cuando no queda nada más por hacer. Las direcciones que hay que registrar en el proveedor están en la conexión (`signOutReturnUrls`). Consulta [Cerrar sesión en el proveedor](../guides/configuring-sso.md#provider-sign-out).
- **Terminar una sesión no recupera los tokens ya emitidos.** Tras un cierre de sesión, una revocación, una desvinculación o una conexión deshabilitada, los tokens de acceso y de inicio de sesión ya emitidos siguen funcionando hasta que caducan, 15 minutos por defecto.

##### El inicio de sesión único necesita la dirección pública de la instancia, así que el chart ahora la deriva {#v0200-sso-issuer}

Iniciar sesión mediante un proveedor de identidad externo OpenID Connect, y el servidor de
autorización OAuth que usan los clientes MCP, necesitan conocer la dirección en la que se accede a
esta instancia, porque un inicio de sesión tiene que volver a algún sitio. El `auth.issuerUrl` de
user-management estaba vacío salvo que lo establecieras. El chart ahora lo deriva del ingress, como
`https://<host>/api/user-management`, cuando la instancia se sirve por https o en un host de
loopback (`localhost`, `127.0.0.1`, `::1`), y tu propio valor siempre prevalece. De ello se sigue:

- **El servidor de autorización OAuth se activa** en esas instancias. En una instalación por defecto
  los clientes MCP no podían obtener un token, porque estaba desactivado hasta que establecías este
  valor. Para mantenerlo desactivado, y con él el inicio de sesión único, establece
  `functionalAreas.user-management.config.auth.issuerUrl` en `""`.
- **Los tokens emitidos tras la actualización llevan esa dirección como emisor**, en lugar del
  identificador por instancia. Los validadores de tokens de los servicios no comprueban ni el emisor
  ni la audiencia, así que los tokens emitidos antes de la actualización siguen funcionando hasta que
  caducan. No hay nada que hacer.
- **Una instancia solo http en un nombre de host real no recibe emisor**, porque user-management
  rechaza un emisor http en cualquier host que no sea de loopback. El servidor de autorización y el
  inicio de sesión único siguen desactivados, y `helm install` y `dcctl bootstrap` lo indican. Sirve
  la instancia por https para activarlos.

##### Alertas del inicio de sesión único {#v0200-sso-alerts}

Tres alertas informan de los fallos del inicio de sesión único. Si enrutas o silencias alertas por
nombre, estas son nuevas.

| Alerta | Severidad | Qué informa | Qué hacer |
| --- | --- | --- | --- |
| `SsoIdpUnreachable` | warning | Durante diez minutos, user-management no ha podido alcanzar el endpoint de tokens ni las claves de firma de un proveedor de identidad configurado, así que se rechaza a quienes inician sesión a través de él. El inicio de sesión con contraseña no se ve afectado. | Comprueba que el proveedor está activo y que su dirección se resuelve desde el clúster, que la lista de destinos permitidos de salida (`instance.config.infrastructure.egress`) permite el rango de un proveedor local y, si el proveedor movió un endpoint, actualiza y aprueba la instantánea de descubrimiento de la conexión. |
| `SsoUnverifiedIdentityRefused` | warning | Se rechazó un inicio de sesión aunque el proveedor lo avalaba, con el resultado en la etiqueta `outcome`: `refused_unverified_email`, `refused_unverified_domain`, `refused_required_claim` o `refused_system_role_identity`. No se inició sesión a nadie. | Suele ser una conexión mal configurada o alguien que la está sondeando. Busca en el diario de auditoría la dirección que afirmó el proveedor, y en el registro de inicios de sesión del propio proveedor. |
| `SsoStateStoreFull` | critical | El almacén que guarda los inicios de sesión comenzados está lleno, así que user-management se niega a comenzar cualquier inicio de sesión único con cualquier proveedor hasta que las entradas caduquen, diez minutos después. El inicio de sesión con contraseña no se ve afectado. | Casi seguro que alguien llama en bucle al inicio público de sesión: localiza el tráfico en los registros del ingress y bloquéalo aguas arriba. Si el volumen es legítimo, aumenta `instance.config.infrastructure.nats.kvStateMaxBytes`. |

##### El dominio de un correo ahora tiene una sola grafía, y una dirección almacenada con dominio no ASCII deja de coincidir {#v0200-email-idna}

Una dirección de correo ahora se almacena, se busca y se compara en una única forma canónica: la parte local recortada y en minúsculas como antes, y el dominio convertido a ASCII en minúsculas (la forma `xn--` para un nombre internacionalizado) y sin punto final. Por tanto, `user@bücher.example` y `user@xn--bcher-kva.example` son la misma identidad en el inicio de sesión con contraseña, en el inicio de sesión único y en una solicitud de vinculación, y el dominio que reclama una conexión se compara en la misma forma. Se rechaza una dirección que no sea exactamente `local@dominio`, o cuyo dominio no sea un nombre de host válido.

- **Una dirección con dominio ASCII no cambia.** No tienes que hacer nada.
- **Una identidad almacenada con un dominio no ASCII (`user@bücher.example`) conserva la forma antigua, y se rechaza hasta que un administrador la vuelva a crear.** No puede iniciar sesión con contraseña ni mediante inicio de sesión único, y una sesión que ya tenga abierta termina en su siguiente intercambio; al seleccionar un tenant o renovar se informa entonces el código `IDENTITY_EMAIL_NOT_CANONICAL`. Las acciones de administración siguen encontrándola por la dirección tal como está almacenada, así que puedes deshabilitarla, terminar sus sesiones, desvincularla y eliminarla. Para restaurar el acceso, desvincula y elimina la identidad antigua, vuelve a crearla (con cualquiera de las dos grafías del dominio: se almacena en la forma `xn--`) y restaura sus membresías y vínculos. Hacerlo antes de actualizar, con el dominio en su forma `xn--`, evita la interrupción: consulta [Antes de actualizar](#v0200-before). Lo mismo vale para una identidad cuya dirección almacenada no sea un `local@dominio` válido.

##### Ya no se aceptan credenciales de dispositivo X.509 {#v0200-x509}

El tipo de credencial `X509_CERTIFICATE` no se acepta hasta que se admita la verificación de
certificados. Crear una credencial de ese tipo, cambiar una a ese tipo y sustituir un dispositivo
por uno que la tenga se rechazan con `UNSUPPORTED`, y no se escribe nada. La consola ya no lo
ofrece. Una credencial de ese tipo guardada antes de la actualización deja de autenticar, y nada la
convierte: antes de actualizar, da a cada uno de esos dispositivos una credencial `ACCESS_TOKEN` o
`MQTT_BASIC`. Consulta [Credenciales de dispositivo](../guides/device-credentials.md).

##### Una contraseña `MQTT_BASIC` debe tener al menos 16 bytes {#v0200-mqtt-password-length}

Crear una credencial `MQTT_BASIC`, o establecer su contraseña, rechaza ahora una contraseña de menos
de 16 bytes, o de más de 4096, contados tal como se envían. Una contraseña corta se rechaza con
`INVALID_VALUE`. La contraseña de un dispositivo se aprovisiona en lugar de recordarse, así que el
mínimo está pensado para un secreto generado; **Generar contraseña segura** en la consola genera
uno. Las contraseñas más cortas existentes siguen funcionando hasta que se establecen de nuevo.
Cambiar una credencial a `MQTT_BASIC` debe establecer su contraseña en la misma petición.

##### Paneles: publicar pone en vigor una versión {#v0200-dashboard-publishing}

Publicar un panel hace ahora que la versión que crea sea la que se sirve a los espectadores, y
`activateDashboardVersion` vuelve a poner en vigor una versión anterior; no hay forma de despublicar.
Los espectadores leen la versión en vigor con `publishedDashboard`, y `/dash` la abre por su token.
El borrador es para los autores del panel.

- **Los paneles publicados antes de la actualización no están en vigor.** Nada registraba qué
  versión servían, así que a los espectadores se les indica que el panel no está publicado
  (`NOT_PUBLISHED`) hasta que un autor lo publica de nuevo.
- **`publishDashboard` devuelve un `DashboardPublication`**, con la nueva `version` y el `dashboard`.
- **Leer la `definition` de un panel, o el cuerpo de una versión con `dashboardVersion`, necesita
  `dashboard:write`.** Quien solo tiene `dashboard:read` lee `publishedDashboard`.
- **La búsqueda `dashboards` devuelve `DashboardSummary`**, que no tiene `definition`.
- **`dashboardVersions` se pagina**, de la más reciente a la más antigua: 100 versiones salvo que
  pases `limit` (como máximo 1000) y `offset`, y sin el cuerpo de cada versión.
- `rollbackDashboard` acepta un `expectedUpdatedAt` opcional y rechaza una reversión obsoleta.
- El filtro de nombre de la búsqueda `dashboards` trata `%`, `_` y `\` como sí mismos, no como
  comodines.
- Modificar un panel de un inquilino que se está eliminando se rechaza con `TENANT_DELETED`;
  eliminarlo no.
- La consola no publica una versión mientras un widget de ella esté sin resolver. Consulta
  [Publicar mientras un widget no está resuelto](../guides/adding-widget-packages.md#publishing-while-a-widget-is-unresolved).

##### La agregación de mediciones necesita una hora de inicio, y está acotada {#v0200-aggregation-bounds}

`bucketedMeasurements` exige ahora `startTime`, y sin él se rechaza con `INVALID_VALUE`; `endTime`
es por defecto el momento actual. Una lectura se rechaza con `LIMIT_EXCEEDED`, en lugar de
truncarse, cuando produciría más de 10 000 intervalos, o cuando sus intervalos multiplicados por sus
nombres de medición superan 50 000. Sin un filtro `name`, los nombres contados son los nombres
distintos que coinciden con la lectura. La herramienta MCP `aggregate_measurements` tiene la misma
exigencia y los mismos límites.

##### Peticiones que ahora se rechazan {#v0200-input-bounds}

Entradas que se aceptaban de cualquier tamaño se rechazan ahora por encima de estos límites, con
`LIMIT_EXCEEDED` salvo que se indique otro código. Un valor exactamente en el límite se acepta. La
[referencia de GraphQL](../reference/graphql-api.md#input-validation) los enumera.

| Qué | Límite |
| --- | --- |
| Claves en una búsqueda por token, id o id externo, en todos los servicios, y los tokens de un `removeEntityRelationships` | 1000 |
| Una entrada JSON: metadatos, propiedades, configuración, una carga útil, destinatarios y similares | 64 KiB |
| La carga útil o los metadatos de un comando, al ponerlo en cola o en lote | 64 KiB (`PAYLOAD_TOO_LARGE`, `METADATA_TOO_LARGE`) |
| El nombre de un comando | 128 bytes (`COMMAND_NAME_TOO_LONG`) |
| Una clave de atributo, y un valor de atributo | 128 bytes, y 64 KiB |
| La definición de una regla de detección, y su grafo de autoría | 256 KiB cada uno |
| Relaciones rastreadas de un dispositivo | 256 |
| Definiciones de métricas, definiciones de comandos y reglas de detección de un perfil | 1000 de cada una |
| Relaciones creadas por un `createEntityRelationships` | 1000 |
| Versiones devueltas por `deviceProfileVersions`, `entityGroupVersions`, `assetTypeVersions` y `connectorVersions` | las 1000 más recientes, salvo que se paginen con sus nuevos `limit` y `offset` |
| Destinatarios de una regla de notificación | 100 |
| Los `redirectUris` de un cliente OAuth, y sus `scopes` | 16 entradas cada uno, de como máximo 2048 y 64 caracteres |
| Niveles de inquilino, y los tokens de un `reorderTenantTiers` | 100 |
| Una contraseña establecida mediante la API de administración | 72 bytes |
| Roles, autoridades o tokens de rol en una petición de administración | 100 |
| Ejecutar una consulta o mutación GraphQL | 60 segundos |
| Recibir el cuerpo de una petición, en GraphQL y en los endpoints de OAuth y MCP | 30 segundos; a un cuerpo detenido se responde `400` y se cierra la conexión |

También se rechazan tres valores al crear o actualizar un registro, que antes se guardaban y
fallaban más tarde o no hacían nada:

- un conector de tipo `gcp_pubsub`, que aún no tiene cliente de entrega (`UNSUPPORTED`); uno
  guardado antes sigue enviándose a mensajes fallidos cuando se despacha;
- `params` de un proveedor de IA que fijen algo (`UNSUPPORTED`), ya que ningún cliente de proveedor
  los aplica; se aceptan vacío, null y `{}`;
- las cabeceras personalizadas de un canal de notificación webhook que la entrega rechazaría: un
  nombre no válido, caracteres de control en un valor, una cabecera reservada `Authorization` o
  `X-DC-*`, o un duplicado.

##### Los servicios de la ruta de eventos se ejecutan con un recolector de basura ajustado {#v0200-gogc}

`event-sources`, `event-management`, `device-management`, `device-state` y `event-processing` se
ejecutan ahora con `GOGC=400` y un límite de memoria blando (`GOMEMLIMIT`) al 75% de su límite de
memoria, y el límite de memoria de `event-processing` es de al menos 384Mi. En Google Kubernetes
Engine a 6000 eventos por segundo, frente a una ejecución de control en la misma sesión, la CPU por
evento bajó un 18% en `device-management` y `event-management`, un 15% en `device-state`, un 4% en
`event-sources` y un 5% en `event-processing`.

Son valores del chart, `functionalAreas.<area>.gogc` y `functionalAreas.<area>.goMemLimitPercent`,
y un nuevo `gogc` de nivel superior (por defecto `0`, desactivado) junto al `goMemLimitPercent` de
nivel superior ya existente. El valor propio de un área prevalece sobre el de nivel superior, así
que un `0` de nivel superior ya no desactiva el límite blando para estos cinco: fija el valor propio
del área en `0` para desactivar cualquiera de los dos en esa área.

##### `device-management` conserva sus búsquedas durante minutos, y un cambio llega a todas las réplicas {#v0200-resolve-caches}

`device-management` busca la credencial, el dispositivo, las relaciones rastreadas, el perfil y las
pertenencias a grupos de cada evento, y los conservaba en la memoria de cada réplica durante 5
segundos, así que un dispositivo que informaba con menos frecuencia se buscaba de nuevo cada vez.
Ahora se conservan durante 5 minutos por defecto:

| Clave de configuración de `device-management` | Valor por defecto | Rango |
| --- | --- | --- |
| `inMemoryCache.ttlSeconds` (nueva) | 300 | 1-3600 |
| `inMemoryCache.credentialCacheMiB` (nueva; era un valor fijo de 16 MiB) | 32 | 1-256 |
| `deviceCacheTtlSeconds`, `relationshipCacheTtlSeconds`, `metricDefCacheTtlSeconds`, `membershipCacheTtlSeconds` (eran 60) | 300 | 1-3600 |

Un valor fuera de su rango impide que el servicio arranque. Un cambio descarta ahora la copia
guardada en todas las réplicas, no solo en la que lo hizo; una réplica cuya conexión con el bróker
se restablece descarta todo lo que conserva, y el borrado de un inquilino eliminado descarta las
copias de ese inquilino en todas las réplicas. Actualizar un dispositivo, incluido cambiar su tipo,
y crear una relación, en una llamada o en varias, al canjear o reabrir una reclamación, o al cambiar
un tipo de relación, no descartaban las copias que modificaban, y ahora sí lo hacen.

- **Dimensiona cada réplica para toda la flota activa**, no para la flota dividida entre las
  réplicas: cada réplica ve casi todos los dispositivos. Para 60 000 dispositivos con una relación
  rastreada cada uno, eso son unos 80 para `inMemoryCache.credentialCacheMiB`, 64 para
  `inMemoryCache.perDeviceCacheMiB` y un límite de memoria de 512Mi. Un límite menor que la flota
  hace que la caché no responda casi nada.
- **Una revocación está acotada por los mismos 5 minutos.** Una réplica que pierde el mensaje que le
  indica descartar una credencial revocada, sin perder su conexión con el bróker, puede seguir
  autenticando los eventos de esa credencial hasta que caduque su copia. Las conexiones siempre se
  comprueban contra la base de datos. Consulta
  [Cuánto tarda en aplicarse una revocación](../guides/device-credentials.md#revocation-timing).

##### Los lectores usan la lectura anticipada por defecto {#v0200-fetch-ahead}

Un servicio que lee un stream pide ahora su siguiente lote mientras entrega el actual, y pide 128
mensajes cada vez en lugar de 64. Según lo medido en Google Kubernetes Engine, esto mantuvo la cola
pendiente del motor de detección bajo carga intensa en unos cientos de mensajes en lugar de decenas
de miles; el techo de rendimiento y la CPU por evento de cada servicio fueron los mismos con ello
desactivado. Fija `instance.config.infrastructure.nats.fetch.ahead: false` para desactivarlo.
Consulta [Lectura anticipada](./observability.md#fetch-ahead).

##### La detección lee sus eventos mediante un nuevo consumidor {#v0200-ack-floor}

`event-processing` confirma ahora los eventos resueltos una vez por punto de control, mediante un
nuevo consumidor duradero cuyo nombre termina en `_ackfloor`
(`<instance>_event-processing_resolved-events_ackfloor`). En su primer arranque, el nuevo consumidor
empieza justo después del último evento que cubre el estado guardado, y el primer líder de la nueva
versión elimina el consumidor anterior. No hay nada que migrar. Un panel o una alerta propios que
seleccionen el consumidor de eventos resueltos de `event-processing` por su etiqueta `durable`
necesitan el nuevo nombre; las alertas incluidas coinciden con ambos. Volver a `v0.19.0` hace que la
detección lea una vez más los eventos retenidos del stream, y omite los que ya ha visto.

La detección en vivo también vuelve a leer un tramo de eventos que no se le entregó, en lugar de
saltárselo, y dos alertas lo notifican (`DetectLiveGapFillFailing`, `DetectLiveDeliveriesRecovered`).
Un bucle de detección que deja de avanzar dispara `DetectLoopStalled`, y cada punto de control se
ejecuta ahora con un plazo cuyo mínimo es el nuevo `checkpointTimeoutSeconds` (por defecto 10, como
máximo 120).

##### El tiempo de espera del borrador con IA {#v0200-inference-timeout}

El techo por defecto de una llamada de `ai-inference` a un proveedor de modelos baja de 60 a 40
segundos (`inferenceTimeoutMs`, como máximo 120000). Redactar una regla a partir de una descripción
espera ahora ese techo, con un presupuesto total de 50 segundos, y un modelo que no responde a
tiempo se notifica como tal y no como un proveedor no disponible. Si dependías del valor por
defecto de 60 segundos, fija `inferenceTimeoutMs` tú mismo.

##### `dcctl bootstrap` puede dar a una instancia un almacén de objetos {#v0200-object-store}

Los logotipos de inquilino y los artefactos de actualización se guardan en un almacén de objetos, y
una instancia creada por `dcctl` no tenía ninguno, así que ambos se rechazaban con
`OBJECT_STORE_NOT_CONFIGURED`. `dcctl bootstrap` acepta ahora
`--object-store none|filesystem|s3`, con opciones para el volumen o el bucket. Sin la opción, una
instancia en un clúster que crea `dcctl` recibe un almacén filesystem de 5Gi, y una en un clúster
indicado con `--kube-context` no recibe ninguno. La elección queda registrada y `dcctl upgrade` la
conserva, y rechaza un cambio que dejaría huérfanos los objetos guardados. **`dcctl upgrade` no
añade un almacén a una instancia creada sin él**: antes de `v1.0.0`, destrúyela y vuelve a crearla
con bootstrap con la opción. Consulta [El almacén de objetos](./bootstrap.md#object-store).

##### Los dispositivos pueden recibir configuración {#v0200-device-configuration}

Una versión de perfil de dispositivo declara cuáles de los atributos compartidos de sus dispositivos
puede ver un dispositivo, en la pestaña **Configuración** del editor de perfiles o con
`setDeviceProfileConfigurationDeclaration`. Escribir un atributo compartido declarado crea una
revisión de configuración numerada, que se envía al dispositivo por su propio topic de
configuración; el dispositivo informa `RECEIVED`, `APPLIED` o `REJECTED`, y pide la revisión actual
con un `sync`, que es también como le llega un perfil recién publicado. La página del dispositivo en
la consola muestra la declaración, la revisión actual y lo último que informó el dispositivo, y
`deviceConfiguration` y `deviceConfigurationRevisions` los leen. Escribir un atributo declarado se
rechaza con `CONFIGURATION_TOO_LARGE` cuando el documento superaría 64 KiB, y con
`CONFIGURATION_VALUE_TYPE_MISMATCH` cuando el valor no encaja con el tipo declarado. Por ahora, solo
dispositivos MQTT. Consulta
[Topics de entrega de configuración](../guides/connecting-a-device.md#reserved-device-topics).

##### Actualizaciones por aire: una primera entrega {#v0200-updates}

El nuevo servicio `update-management` mantiene un catálogo por inquilino de imágenes de firmware,
software y configuración, con una subida comprobada contra su tamaño declarado y su resumen
SHA-256. Un artefacto se asigna a un dispositivo cada vez; al dispositivo se le avisa por su topic
de configuración, descarga la imagen con una concesión de corta duración que puede reanudar, y cada
intento se sigue hasta la versión en ejecución. Es opcional: se ejecuta en el perfil `full`, o con
`--enable-area update-management`. Necesita un almacén de objetos, y:

- **No se puede asignar ningún artefacto hasta que fijes `updates.allowUnverifiedArtifacts: true`**,
  porque nada autentica aún quién produjo un artefacto.
- **Los dispositivos no reciben concesión de descarga hasta que se fija `updates.downloadBaseUrl`.**
- **Mantén la cadena de consulta de la ruta de descarga fuera de los registros de acceso.** El chart
  la sirve en un ingress propio con el registro de acceso desactivado; detrás de otro proxy, haz lo
  mismo.
- Aún no hay despliegues a toda la flota, y solo se pueden actualizar dispositivos MQTT.

##### Los paquetes publicados {#v0200-packages}

- **`@devicechain/widget-sdk` es nuevo.** Define un widget (`defineWidget`) y construye un registro
  de ellos (`createWidgetRegistry`); consulta [Escribir un widget personalizado](../guides/writing-a-custom-widget.md).
  `@devicechain/widgets` lo declara como dependencia par, así que instálalo junto a `@devicechain/widgets`.
- **`@devicechain/widgets` ya no exporta `WIDGET_REGISTRY`, `ALARM_WIDGET_REGISTRY`,
  `CONTROL_WIDGET_REGISTRY`, `SELECTION_WIDGET_REGISTRY` ni `LOCATION_WIDGET_REGISTRY`.** Busca un
  widget mediante un registro, como el que construye `createBuiltinWidgetRegistry`.
- **`echarts` es una dependencia par de `@devicechain/widgets`, no una dependencia:** añade `echarts`
  (`^6.0.0`) a tu aplicación. Las utilidades de gráficos están en `@devicechain/widget-sdk/echarts`.
- **Suben los rangos de las dependencias par:** `react` y `react-dom` a `^19.3.0`, `maplibre-gl` a `^6.11.2`.
- **Se eliminan `DashboardHubConfig.bindings` y `DashboardHub.setBindings` de
  `@devicechain/dashboards`.** Pasa los enlaces a `DashboardRenderer` y usa `useDashboardHub`.
  `fetchWidgetHistory` y `widgetSubjectLabel` aceptan un widget cuya fuente de datos ya está resuelta.
- **`parseDashboardDefinition(raw, { catalog })`** acepta el catálogo de widgets contra el que
  comprobar. Un widget de un tipo desconocido, o uno que el catálogo no sabe leer, se conserva como
  marcador (`UnknownWidgetInstance`, parte de la unión `WidgetInstance`) y se vuelve a escribir
  exactamente como estaba guardado, donde antes hacía fallar toda la definición. Se rechaza una
  fuente de datos guardada de tipo `unbound`.
- **El SDK de .NET** abre las suscripciones por sí mismo y ya no pasa por el proxy HTTP del sistema.
  Un dispositivo puede optar por procesar comandos de forma concurrente (`MaxConcurrentCommands`),
  cada comando lleva su orden de llegada (`DeviceCommand.Sequence`), y una respuesta terminada
  durante una reconexión se envía después de ella en lugar de descartarse.

##### Alertas nuevas {#v0200-alerts}

Si enrutas o silencias alertas por nombre, estas son nuevas, además de las
[alertas del inicio de sesión único](#v0200-sso-alerts):

| Alerta | Qué informa |
| --- | --- |
| `SafetyGateDisabled` | Un servicio se ejecuta sin una de sus compuertas de seguridad opcionales: la compuerta del ciclo de vida del inquilino, la presencia de comandos o la validación de las reglas de detección al publicar. |
| `DetectLoopStalled` | El líder de detección lleva minutos sin avanzar. |
| `DetectLiveGapFillFailing`, `DetectLiveDeliveriesRecovered` | La detección en vivo no pudo volver a leer, o sí volvió a leer, eventos que no se le entregaron. |
| `SparkplugIngestLeaderCountNotOne`, `Lwm2mIngestLeaderCountNotOne`, `Lwm2mIngestLeaderNotServing` | Un servicio de ingesta perimetral no tiene exactamente un líder, o su líder no está sirviendo. Un `sparkplug-ingest` sin fuentes no tiene líder y la dispara. |
| `SparkplugIngestDroppingData`, `SparkplugIngestSubscriptionRefused`, `SparkplugIngestBrokerUnreachable`, `Lwm2mIngestDroppingNotifications` | La ingesta Sparkplug o LwM2M está perdiendo datos o no puede alcanzar su bróker. |
| `CNPGCollectorAbsent` | Prometheus ha dejado de recoger las métricas de una base de datos. |

`dcctl install` también deja de recoger métricas, y de alertar, de los componentes del plano de
control que no ejecuta un clúster gestionado como Google Kubernetes Engine, así que sus alertas
`...Down` ya no se disparan allí.

##### Otros comportamientos que conviene conocer {#v0200-other}

- **Sparkplug.** Un nacimiento de nodo que sustituye a una sesión en línea marca ahora como
  desconectados los dispositivos hijos de la sesión anterior; antes seguían en línea.
- **Notificaciones.** Un evento de alarma de un tipo que `notification-management` no conoce se
  reintenta y después se envía a mensajes fallidos, en lugar de descartarse.
- **Los mensajes fallidos y las decodificaciones fallidas** conservan una carga útil con sus campos
  de credencial sustituidos por `[redacted]`. Un cuerpo que no es JSON se conserva como su longitud
  y su SHA-256.
- **Las respuestas a comandos** pueden llevar cualquier valor JSON como `payload`, no solo una
  cadena.
- **Los identificadores de correlación** que genera la plataforma tienen 22 caracteres en lugar de
  un UUID de 36; trátalos como cadenas opacas.
- **Las descripciones de GraphQL** provienen solo de las cadenas `"""` de los esquemas, y ahora cada
  elemento tiene una; la introspección ya no muestra comentarios `#`.
- **`dcctl install` y `dcctl bootstrap` con `--kube-context`** ya no necesitan Docker ni `kind`, y
  comprueban la versión de Kubernetes del contexto al que se dirigen. Un contexto indicado que falta
  en el kubeconfig hace fallar la comprobación previa.
- **La detección puede dividir su estado** en `detectShards` (1-64, por defecto 1). Las particiones
  siguen ejecutándose una tras otra, así que más de una aún no acelera la detección.

### v0.19.0 — dimensionada a partir de lo medido a 6000 eventos por segundo; un stream lleno rechaza {#v0190-upgrade}

`v0.19.0` es una actualización en su sitio desde `v0.18.0`: `dcctl install` para el clúster y
después `dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](#v0170-upgrade).
**Cuenta tu almacén de eventos antes de ejecutar el `dcctl install` de esta versión.** Una
instancia cuyo almacén de eventos guarda más de
4 000 000 de filas aún sin comprimir no puede recibirla en su sitio y hay que recrearla, lo que
descarta sus datos, y la mayoría de las instancias con tráfico real superan ese límite
([Comprueba el número de filas](#v0190-row-count)). También se rechaza una tabla con más de 500
fragmentos, salvo que antes elimines sus eventos más antiguos
([Cuenta los fragmentos](#v0190-chunk-count)). Una instancia creada por la `v0.16.0` o una versión
anterior sigue teniendo que [destruirse y crearse de nuevo](#pre-declaration-recreate).

La mayor parte de esta versión trata de seguir el ritmo. Los servicios que procesan cada evento
solicitan ahora la CPU que se midió que usan, se reparten entre los nodos, almacenan y fusionan los
eventos en menos sentencias y responden desde memoria las búsquedas repetidas; con `--ha` sin
`--compact`, `event-management` se ejecuta en dos pods. En Google Kubernetes Engine, una instalación HA
predeterminada aceptó 6000 eventos por segundo durante 10 minutos, dos veces, y almacenó
exactamente una vez cada evento aceptado, comprobado evento por evento ([Rendimiento](#v0190-performance)). Dos caminos de fallo dejan
de ser silenciosos: un stream de ingesta lleno rechaza ahora los eventos nuevos en lugar de
descartar los no leídos, y las alertas de respaldo avisan antes de que el archivado pueda detener
una base de datos. Y `dcctl upgrade` aplica ahora los ajustes del bróker de mensajería y del almacén
de eventos de una instancia, algo que antes no hacía nunca, así que una instancia actualizada recibe
el mismo bróker y el mismo almacén de eventos que una nueva.

**Quién tiene que hacer algo:**

- **Toda instancia con tráfico real:** cuenta las filas sin comprimir y los fragmentos del almacén
  de eventos antes de actualizar. Por encima de 4 000 000 de filas hay que recrear la instancia, y
  recrearla descarta sus datos ([Comprueba el número de filas](#v0190-row-count)).
- **Todo clúster:** vuelve a ejecutar `dcctl install` con esta versión antes de cualquier
  `bootstrap`, `upgrade` o `destroy`. Este `dcctl` rechaza un registro de instalación escrito por
  uno anterior. Actualiza también todas las copias de `dcctl` que uses: una anterior puede dejar
  marcada como sin terminar una instancia creada por esta versión, y un `dcctl bootstrap` sin más
  se ejecutaría entonces sobre ella
  ([Terminar un arranque inicial fallido](#v0190-bootstrap-resume)).
- **Toda instancia instalada sin `--compact`:** los nodos necesitan sitio para unas solicitudes de
  CPU mayores y para las primeras solicitudes del bróker de mensajería ([Haz sitio](#v0190-room)).
- **Quien tenga dispositivos que envían más de 256 lecturas en un mensaje:** esos mensajes se
  rechazan ahora ([256 lecturas](#v0190-reading-limit)).
- **Todo inquilino que envíe más de 1000 lecturas por segundo**, incluida una flota que pone varias
  lecturas en un mensaje, y **todo inquilino LwM2M**: el techo de ingesta cuenta ahora lecturas, y
  las que lo superan se rechazan con `429` por HTTP y se descartan, sin avisar al dispositivo, en
  todos los demás transportes ([Lecturas, no mensajes](#v0190-ingest-readings)).
- **Quien tenga dispositivos que envían lecturas fechadas más de 366 días atrás**, o que informan
  horas cercanas a 1970 porque no tienen el reloj configurado: esas lecturas se rechazan ahora
  ([Lecturas antiguas](#v0190-event-age-limit)).
- **Quien haya configurado `backup_retention`, `tsdbConfiguration.maxOpenConnections`,
  `rdbConfiguration.maxOpenConnections`, `maxReadingsPerMessage` o los límites de conexión de los
  lectores de analítica:** consulta [Antes de actualizar](#v0190-before).
- **Quien dependa de que revocar una credencial de dispositivo surta efecto al instante:** una
  revocación puede tardar ahora hasta cinco segundos en una réplica de `device-management`
  ([Comprobación de credenciales](#v0190-credential-cache)).
- **Quien llame a la API de ingesta HTTP:** reintenta ante un `503` con `Retry-After`
  ([Contrapresión](#v0190-backpressure)).
- **Quien tenga una fuente de eventos que lee de su propio bróker MQTT:** la fuente se conecta con
  un identificador de cliente nuevo, que el bróker tiene que permitir, y puede leerse dos veces
  mientras avanza la actualización ([Tu propio bróker MQTT](#v0190-mqtt-client-id)).
- **Quien enrute o silencie alertas por nombre o por severidad:** se añaden alertas, y
  `JetStreamStreamNearFull` pasa a `info` ([Alertas](#v0190-alerts)).
- **Quien instale el chart con sus propios valores:** varios valores de recursos de nivel superior
  ya no llegan a los servicios de la ruta de eventos ([Tus propios valores del chart](#v0190-chart-values)).

Todo lo demás está en [Qué cambia](#v0190-what-changed), por áreas.

#### Antes de actualizar {#v0190-before}

Hazlo en este orden.

##### 1. Comprueba el número de filas {#v0190-row-count}

El primer arranque del nuevo `event-management` reconstruye las claves del almacén de eventos sobre
todas las filas de cinco tablas que aún no están comprimidas: por defecto, más o menos la última
semana. **Una instancia con más de 4 000 000 de esas filas no se puede actualizar en su sitio.** Un
evento base es una fila, más una por cada lectura, ubicación o alerta que lleva, más una por cada
anclaje de relación. Con una lectura y un anclaje por evento, 4 millones de filas son una semana a
una media de unos 2 eventos por segundo, así que la mayoría de las instancias con tráfico real
superan el límite.

Abre `psql` en el almacén de eventos (el espacio de nombres es `dci-` más el id de la instancia; la
base de datos es el id de la instancia solo). El recuento solo lee, así que sirve cualquiera de los
pods del almacén; si `dc-tsdb-1` no existe, `kubectl -n dci-<instance-id> get pods` lista los demás:

```bash
kubectl -n dci-<instance-id> exec -it dc-tsdb-1 -c postgres -- psql -U postgres -d <instance-id>
```

Después ejecuta el recuento que hace la actualización. Lee todas las filas sin comprimir, así que en
un almacén grande tarda un rato:

```sql
DO $$
DECLARE c record; n bigint := 0; k bigint;
BEGIN
  FOR c IN SELECT format('%I.%I', chunk_schema, chunk_name) AS chunk
           FROM timescaledb_information.chunks
           WHERE hypertable_schema = 'event-management'
             AND hypertable_name IN ('events', 'measurement_events', 'location_events',
                                     'alert_events', 'event_anchors')
  LOOP
    EXECUTE 'SELECT count(*) FROM ONLY ' || c.chunk INTO k;
    n := n + k;
  END LOOP;
  RAISE NOTICE 'rows to rebuild: %', n;
END $$;
```

Si imprime más de `4000000`, no actualices esta instancia en su sitio: saca de ella lo que
necesites y después recréala con `dcctl destroy` y `dcctl bootstrap` en la nueva versión. Cuenta
mientras el clúster sigue en la `v0.18.0`, antes del `dcctl install` de esta versión, para que una
instancia por encima del límite aún se pueda leer y destruir con el `dcctl` que la creó.

:::caution Recrear descarta los datos de la instancia
`dcctl destroy` elimina las bases de datos de la instancia: todos los inquilinos, dispositivos,
definiciones de dispositivo, paneles y usuarios, y todos sus eventos. DeviceChain no tiene ninguna
herramienta de exportación. Saca lo que necesites antes de destruirla. Los eventos, lecturas,
ubicaciones y alertas de un inquilino se pueden leer mediante el
[acceso por SQL y BI](../guides/sql-and-bi-access.md). `pg_dump` de PostgreSQL puede copiar las dos
bases de datos de la instancia, cada una con el id de la instancia como nombre: una en el almacén de
eventos, en el espacio de nombres de la propia instancia, y otra en la base de datos relacional
compartida (`dc-rdb`, en `dc-system`), que guarda los datos de todas las instancias, así que vuelca
solo esa base de datos. Escribe el volcado en tu propia máquina, por ejemplo
`kubectl -n dc-system exec dc-rdb-1 -c postgres -- pg_dump -U postgres -Fc <instance-id> > rdb.dump`,
y lo mismo con `-n dci-<instance-id>` y `dc-tsdb-1` para el almacén de eventos. Los secretos del
volcado siguen sellados con la clave raíz de la instancia. Un volcado es una copia para leer, o
para cargar en una base de datos tuya: ningún `dcctl` lo carga en la instancia nueva.

Conserva los respaldos, para poder volver atrás. Destruye la instancia con el `dcctl` de la
`v0.18.0` antes de ejecutar el `dcctl install` de esta versión: deja en su sitio los respaldos
internos. Después de la instalación, usa el `dcctl destroy --keep-backups` de esta versión; sin esa
opción borra el archivo del almacén de eventos de la instancia. En ambos casos, lo que se conserva
es el archivo del **almacén de eventos**, junto al archivo relacional del clúster, que destroy no
borra nunca. En el mismo clúster, el archivo del almacén de eventos solo devuelve el historial de
eventos de la instancia, bajo un plano de control vacío, sin inquilinos, dispositivos, usuarios ni
secretos. Recuperar la instancia entera en la `v0.18.0` significa recuperarla en un clúster nuevo, a
partir del archivo relacional en un momento anterior al destroy (`--restore-rdb-at`), lo que
rebobina todas las instancias de ese clúster, con el respaldo en custodia de la clave raíz de la
instancia y el archivo de su almacén de eventos, como describe
[Recuperar una instancia](./disaster-recovery.md#recover). Restaurar el archivo del almacén de
eventos en la `v0.19.0` no es un camino hacia delante: devuelve las mismas filas, y la
actualización las vuelve a rechazar.

El archivo conservado del almacén de eventos guarda los eventos de todos los inquilinos que tuvo la
instancia, incluidos los inquilinos eliminados dentro de su ventana de recuperación, y nada lo poda
una vez que la instancia ya no existe. Elimínalo cuando ya no lo necesites, como muestra
[Qué pasa con los respaldos de la instancia](./bootstrap.md#destroy-backups).

Antes de ejecutar `dcctl bootstrap` con el mismo nombre, aparta el artefacto de custodia de la clave
raíz de la instancia (`~/.devicechain/escrow/<instance>-rootkey.escrow`) y consérvalo: bootstrap no
lo sobrescribe, y es la única clave de los respaldos relacionales tomados antes del destroy.

No se ha probado ningún camino en su sitio para una instancia por encima del límite.
:::

##### 2. Cuenta los fragmentos {#v0190-chunk-count}

La reconstrucción, y la [eliminación de índices](#v0190-event-store-keys), también rechazan una
tabla con más de **500 fragmentos**. Cada una bloquea todos los fragmentos de la tabla que cambia, y
500 es la mitad de lo máximo que esta versión ha medido que se reconstruye dentro de un arranque, lo
que deja margen para un almacenamiento más lento. Por encima, `event-management` se niega antes de
bloquear nada, en lugar de arriesgarse a una reconstrucción que no puede terminar y que después
bloquea todos los arranques siguientes. Un fragmento cubre un intervalo de fragmento de una tabla:
un día por defecto (`lifecycle.chunkIntervalHours`), así que con el valor por defecto 500 fragmentos
son un año y cuatro meses de historial, aproximadamente, con la retención desactivada. Un intervalo
más corto llega antes a 500, y también un dispositivo que envió lecturas fechadas muy en el pasado.
Cuéntalos, en cualquiera de los pods del almacén de eventos:

```sql
SELECT hypertable_name, count(*) AS chunks, min(range_start) AS oldest, max(range_end) AS newest
FROM timescaledb_information.chunks WHERE hypertable_schema = 'event-management'
GROUP BY 1 ORDER BY 1;
```

Si una tabla tiene más de 500 y puedes prescindir de sus eventos más antiguos, elimínalos antes de
actualizar. **Esto borra para siempre los eventos de todos los inquilinos en esos fragmentos.**

1. **Detén lo que los envió.** Si un dispositivo sigue enviando lecturas fechadas muy en el pasado,
   revoca antes su credencial: la versión anterior las acepta, y volverían a crear los fragmentos.
2. **Elige una fecha de corte.** Los fragmentos que terminan en ella o antes se eliminan enteros; el
   fragmento que la abarca se conserva. La fecha en que se instaló la instancia elimina solo eventos
   fechados antes de que la instancia existiera. La misma fecha de corte se aplica a las seis tablas
   de eventos, para que ningún evento quede sin sus lecturas.
3. **Ejecuta la eliminación en el primario del almacén de eventos.**
   `kubectl -n dci-<instance-id> get pods -l cnpg.io/instanceRole=primary` lista los primarios; usa
   el que empieza por `dc-tsdb-`, abre `psql` en él como arriba y ejecuta la sentencia de abajo con tu
   fecha de corte en lugar de `2024-01-01`. Ejecútala sola, no dentro de `BEGIN`: elimina como mucho
   100 fragmentos de una tabla cada vez y confirma tras cada lote, así que la base de datos nunca
   tiene que bloquearlos todos a la vez.

```sql
DO $$
DECLARE
  cutoff constant timestamptz := TIMESTAMPTZ '2024-01-01';
  tbl text;
  step timestamptz;
BEGIN
  FOREACH tbl IN ARRAY ARRAY['events', 'measurement_events', 'location_events',
                             'alert_events', 'event_anchors', 'state_change_events'] LOOP
    LOOP
      SELECT max(range_end) INTO step FROM (
        SELECT range_end FROM timescaledb_information.chunks
        WHERE hypertable_schema = 'event-management' AND hypertable_name = tbl
          AND range_end <= cutoff
        ORDER BY range_end LIMIT 100) oldest;
      EXIT WHEN step IS NULL;
      PERFORM drop_chunks(format('%I.%I', 'event-management', tbl)::regclass, older_than => step);
      COMMIT;
    END LOOP;
  END LOOP;
END $$;
```

Después vuelve a contar los fragmentos y actualiza. Si no puedes prescindir de esos eventos, recrea
la instancia como en [Comprueba el número de filas](#v0190-row-count).

##### 3. Ejecuta la actualización donde se creó la instancia {#v0190-where}

`dcctl upgrade` aplica ahora la configuración de OpenTofu de la instancia, así que necesita su
estado: ejecútala en la máquina que creó la instancia, o copia antes allí
`~/.devicechain/instances/<instancia>/` y mantenlo privado (modo `0700`), porque contiene
credenciales. También necesita `tofu` en el `PATH`, con OpenTofu 1.9 o posterior (o Terraform 1.9 o
posterior). Sin el estado, la actualización se niega y no cambia nada. `--skip-infrastructure`
mueve solo los servicios, como antes, y dice lo que dejó sin aplicar.

##### 4. Haz sitio {#v0190-room}

Una vez actualizada, una instancia instalada sin `--compact` solicita más CPU, y la actualización
progresiva necesita aún más mientras los pods antiguos y los nuevos se ejecutan a la vez:

| | CPU solicitada de más una vez actualizada | CPU libre que la actualización progresiva de los servicios necesita a la vez |
| --- | --- | --- |
| `--ha` | 4,45 para los servicios, más 500m y 768Mi de memoria en cada uno de los tres nodos que ejecutan un servidor NATS | 4,85, además de la de los servidores NATS: 4,55 para los pods que arrancan junto a los antiguos (`device-management`, los dos pods de `event-management`, `device-state` y `event-sources`) y 0,3 para `event-processing`, cuyo pod nuevo arranca después de que se detenga el antiguo |
| sin `--ha` | 3,55 para los servicios, más 500m y 768Mi para el único servidor NATS | 3,95 (3,65 y 0,3), además de la del servidor NATS |
| `--compact` | nada para los servicios; 25m y 64Mi en cada nodo que ejecuta un servidor NATS | 0,1, como antes (25m por cada pod que arranca junto al antiguo), además de la de los servidores NATS |

Estas cifras son para una instancia que ejecuta los cinco servicios. Las nuevas solicitudes de los
servicios son `device-management` 800m, `event-management` 900m (dos pods con `--ha`, lo que añade
también los 128Mi de memoria del segundo pod), `device-state` 950m, `event-sources` 1 núcleo y
`event-processing` 400m; en `v0.18.0` cada uno solicitaba 100m. Cada pod nuevo necesita su solicitud
entera libre en un solo nodo. Cada servidor NATS solicita ahora 500m de CPU y 768Mi de memoria y
tiene un límite de 2Gi de memoria; antes no solicitaba nada. Compara la CPU y la memoria asignables
libres de los nodos (`kubectl describe nodes`, "Allocated resources") con
[Dimensionamiento de los servicios](./bootstrap.md#service-sizing). Un clúster kind en un portátil
es el que más probablemente no las tenga.

Un pod que no se puede colocar queda en `Pending`, y la actualización falla tras esperar (hasta 15
minutos para el bróker), con la instancia actualizada a medias: los servicios cuyos pods nuevos
arrancaron están en la nueva versión, y el resto sigue en la antigua. Un servidor NATS que queda en
`Pending` sin `--ha` deja el bróker caído hasta que haya sitio. Haz sitio y vuelve a ejecutar
`dcctl upgrade` para terminar. Una instancia creada con `dcctl` no tiene forma de conservar las
solicitudes antiguas; el remedio es la capacidad, o volver a crearla con `--compact`. Las instancias
instaladas con `--compact` conservan sus solicitudes de 25m y 64Mi, y sus servidores NATS solicitan
lo mismo.

Un `ResourceQuota` o `LimitRange` en el espacio de nombres de la instancia puede rechazar las
solicitudes mayores, los límites de 2 núcleos de `event-sources` y `device-state`, o el límite de 1
núcleo de `event-processing`.

##### 5. Comprueba si hay una cuota sobre la ubicación entre espacios de nombres {#v0190-quota}

Los pods de base de datos llevan ahora una preferencia de ubicación que tiene en cuenta otros
espacios de nombres ([Primarias de las bases de datos](#v0190-primary-spread)). Un `ResourceQuota`
con el ámbito `CrossNamespacePodAffinity` rechaza esos pods, sea preferencia o no: las instancias
reiniciadas de la base de datos relacional en `dc-system`, y el almacén de eventos de cada instancia
en su propio espacio de nombres. Nada de lo que instala DeviceChain crea uno. Para listar los que
existan:

```bash
kubectl get resourcequota -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,SCOPES:.spec.scopeSelector
```

Una lista vacía no lo zanja. La configuración de admisión de cuotas del servidor de API puede
nombrar `CrossNamespacePodAffinity` en `limitedResources`, y entonces esos pods se rechazan en todo
espacio de nombres que **no** tenga una cuota con ese ámbito que los admita. Esa configuración vive
en el plano de control, así que pregunta a quien administre el clúster; si está activa, da a
`dc-system` y al espacio de nombres de cada instancia una cuota con ese ámbito antes de actualizar.
Un pod del almacén de eventos rechazado deja el almacén con una instancia menos o, sin `--ha`, caído.

##### 6. Comprueba los ajustes que ahora impiden que un servicio arranque {#v0190-settings}

- Si fijaste `tsdbConfiguration.maxOpenConnections` de `event-management`, o
  `rdbConfiguration.maxOpenConnections` de `device-state`, en `10` o menos y no fijaste
  `persistence.writers` ni `projection.writers`, el pod nuevo no arranca, y nombra el ajuste y el
  tamaño del pool. Fija los escritores por debajo de tu pool (el valor por defecto anterior era
  `5`), o quita el ajuste del pool para usar el valor por defecto de 20. Los pools de 11 a 19
  arrancan y registran que más de la mitad del pool se da a los escritores.
- **Lectores de analítica** (instancias `--ha`): sus valores de `connection_limit` pueden sumar
  ahora como máximo 17 de las conexiones del almacén de eventos, no 57, porque la plataforma reserva
  80 para dos pods de `event-management` en lugar de 40. Por encima de 17 la actualización se
  detiene antes de cambiar nada. Si subiste `timescale_analytics_reserved_connections` para ejecutar
  por tu cuenta un segundo pod de `event-management`, devuélvelo a 40: ahora es la reserva de cada
  pod. Consulta [Límite de conexiones](../guides/sql-and-bi-access.md#connection-cap).
- **`maxReadingsPerMessage`** queda retirado: el servicio lo ignora con un aviso. Consulta
  [256 lecturas](#v0190-reading-limit) para localizar los dispositivos que rechazaría.

##### 7. Comprueba lo que envían tus dispositivos {#v0190-device-traffic}

- **Más de 256 lecturas en un mensaje** se rechazan. Localiza esos dispositivos como indica
  [256 lecturas](#v0190-reading-limit).
- **Más de 1000 lecturas por segundo de un inquilino** superan ahora el techo predeterminado, que
  cuenta lecturas en lugar de mensajes. Localiza esos inquilinos y eleva primero la tasa de ingesta
  de su nivel, como indica [Lecturas, no mensajes](#v0190-ingest-readings). Las lecturas por encima
  del techo se descartan, no se retrasan: HTTP responde `429`, los dispositivos MQTT, LwM2M y
  Sparkplug no reciben aviso, y un mensaje MQTT descartado así ya estaba confirmado.
- **Las lecturas fechadas más de 366 días antes de llegar** se rechazan. Un dispositivo sin reloj
  debe omitir `occurredTime`. Los mensajes que ya esperan en la plataforma se juzgan según la hora en
  que llegaron; el almacén local de un agente de borde no tiene límite de antigüedad propio
  ([Lecturas antiguas](#v0190-event-age-limit)).

##### 8. Cambia el nombre de `backup_retention` {#v0190-rename-retention}

Si lo configuraste: `backup_retention_rdb` en la configuración del clúster, `backup_retention_tsdb`
en la de la instancia. Un `-var backup_retention=…` se rechaza; una línea en un `.tfvars` solo
produce un aviso, y se usa el valor predeterminado en lugar del tuyo; una variable
`TF_VAR_backup_retention` se ignora sin ningún aviso.

##### 9. Comprueba el margen del almacén de respaldos {#v0190-headroom}

La base de datos relacional conserva ahora 30 días de respaldos en lugar de 7
([Ventanas de recuperación](#v0190-backup-retention)), y un almacén existente conserva su tamaño,
así que un almacén creado antes de esta versión sigue teniendo 20 GiB salvo que lo hayas ampliado.
Amplíalo primero si tiene poco margen, como muestra
[Tamaño del almacén de objetos de respaldo](./bootstrap.md#backup-store-size).

##### 10. Conserva los valores de OpenTofu que fijaste tú mismo {#v0190-tfvars}

La actualización conserva un valor declarado en un `terraform.tfvars` junto al estado de la
instancia (`~/.devicechain/instances/<instancia>/infra/instance/`) o en `TF_VAR_<nombre>`, y no uno
que pasaste con `-var`. Se niega antes que acortar la ventana de recuperación del almacén de eventos
o dejar de declarar un lector de analítica, y avisa de una solicitud o un límite del bróker que
bajaría. Pasa esos valores a ese archivo primero. Consulta
[Valores que usted mismo dio a la configuración](#upgrade-infrastructure).

##### 11. Si sustituiste la imagen de base de datos del almacén de eventos {#v0190-image}

Comprueba que puede usar `lz4` para la
[compresión del registro de escritura anticipada](#v0190-wal); una instancia que se reinicie en una
imagen que no puede puede no arrancar. Actualiza solo si cada línea imprime `t`:

```bash
pods=$(kubectl -n dci-<instance> get pods -l cnpg.io/cluster=dc-tsdb,cnpg.io/podRole=instance -o name)
for p in $pods; do
  kubectl -n dci-<instance> exec "$p" -c postgres -- psql -U postgres -tAc \
    "SELECT 'lz4' = ANY (enumvals) FROM pg_settings WHERE name = 'wal_compression'"
done
```

##### 12. Si una fuente de eventos lee de tu propio bróker MQTT {#v0190-mqtt-client-id}

Cada fuente así se conecta ahora como `devicechain:<instancia>:<fuente>:<pod>` en lugar de
`devicechain` ([Su propio identificador de cliente](#v0190-external-mqtt-client-id)). Si la ACL, la
lista de permitidos o la regla de identificadores de tu bróker nombra `devicechain`, permite los
identificadores que empiezan por `devicechain:`. El identificador además supera los 23 caracteres,
el límite más corto que MQTT exige aceptar a un bróker. Un bróker que rechaza el nuevo
identificador hace que el pod nuevo no arranque, con el motivo del bróker en su registro, y la
actualización se detiene con los pods viejos aún en marcha y leyendo la fuente como antes.

Para no almacenar mensajes dos veces mientras avanza la actualización (consulta
[Durante la actualización](#v0190-during)), prevé pausar a los publicadores de tu bróker mientras
dura el despliegue.

#### Durante la actualización {#v0190-during}

**`dcctl install`** da a la base de datos relacional compartida una ventana de recuperación de 30
días, zstd para su registro archivado y la etiqueta de ubicación, y reinicia una vez sus instancias:
con `--ha`, primero las réplicas en espera y después un traspaso a una de ellas, en otro nodo. Una
base de datos relacional de una sola instancia no está disponible hasta que termina de reiniciarse;
las escrituras hechas mientras tanto se reintentan.

**`dcctl upgrade`** aplica después los servidores NATS y el almacén de eventos de la instancia antes
de mover los servicios ([detalles](#v0190-upgrade-infrastructure)):

- Los servidores NATS se reinician por sus nuevas solicitudes y su límite, de uno en uno con
  `--ha`. Sin `--ha` el bróker no está disponible mientras se reinicia su único servidor,
  normalmente alrededor de un minuto. Con `--ha`, la ingesta HTTP puede responder `503` brevemente
  mientras se sustituye un servidor; reinténtalo como con cualquier `503`
  ([calidad de servicio](../guides/connecting-a-device.md#quality-of-service)).
- Sin `--ha`, la detección se detiene mientras se reinicia el bróker, durante ese minuto más hasta
  25 segundos, y después reproduce el flujo desde su último punto de control
  ([La partición de la detección](#v0190-detect-lease-release)).
- En una instancia creada con `v0.18.0`, las instancias del almacén de eventos pueden reiniciarse
  una vez por la preferencia de ubicación: primero las réplicas en espera y después un cambio de
  primaria con `--ha`. Sin `--ha`, los eventos no se almacenan hasta que vuelve la única instancia,
  y mientras tanto esperan en el stream de ingesta. Su compresión del registro de escritura
  anticipada y el archivado con zstd no necesitan reinicio.
- El bróker y el almacén de eventos pueden reiniciarse a la vez; la actualización espera a ambos.

**Mientras se despliega `event-sources`, una fuente sobre tu propio bróker MQTT puede leerse dos
veces.** Hasta que se detiene el último pod de la versión anterior, ese pod sigue leyendo la fuente
con el identificador viejo, junto al pod nuevo que tiene la fuente, y cada uno almacena lo que
recibe. Los mensajes que llegan en esa ventana pueden almacenarse dos veces. Ocurre una sola vez, y
lo mismo pasa al volver a la versión anterior; pausar a los publicadores de tu bróker mientras dura
el despliegue lo evita. Los arrendamientos que deja una versión revertida caducan solos en 30
segundos.

**El nuevo `event-management`, en su primer arranque,** elimina dieciséis índices y reconstruye las
claves del almacén de eventos, tabla a tabla
([Las claves del almacén de eventos](#v0190-event-store-keys)). Mientras trabaja en una tabla, las
lecturas y escrituras de esa tabla esperan: unos segundos en el caso habitual, y como máximo 45
segundos por tabla para la reconstrucción de la clave. El `event-management` anterior sigue
recibiendo y almacenando eventos; un lote retenido el tiempo suficiente se entrega de nuevo y se
almacena una sola vez. Con `--ha`, los pods se
renuevan de uno en uno, y el primer pod nuevo en arrancar hace el trabajo antes de estar listo. Un
segundo pod nuevo que arranque mientras ese trabajo sigue en curso lo espera y puede ser reiniciado
una vez por su comprobación de arranque; el que arranca después no tiene nada que esperar. Ambos
casos son lo esperado. Si el trabajo se detiene (una tabla ocupada, el minuto agotado, demasiadas
filas, más de 500 fragmentos, una tabla que tarda más de 40 segundos), `event-management` se detiene
con un error que nombra la tabla y qué hacer, todos los demás servicios ejecutan la nueva versión, y
el `event-management` anterior sigue almacenando eventos. Consulta
[Si el cambio del almacén de eventos se detiene](#v0190-event-store-keys-stops).

##### Si el cambio del almacén de eventos se detiene {#v0190-event-store-keys-stops}

- **Demasiadas filas:** `event-management` se detiene antes de cambiar nada, y explica el motivo.
  `dcctl upgrade` informa de que no terminó de desplegarse; recrea la instancia como en
  [Comprueba el número de filas](#v0190-row-count).
- **Una tabla con más de 500 fragmentos:** `event-management` se detiene antes de bloquear nada; su
  registro nombra la tabla, su número de fragmentos y la consulta de
  [Cuenta los fragmentos](#v0190-chunk-count). Ese arranque no cambia ni marca nada (su registro
  lista lo que ya hicieron arranques anteriores): una vez eliminados los fragmentos, el siguiente
  arranque sigue adelante.
- **Una tabla ocupada, o el minuto agotado:** cada intento de bloqueo desiste como máximo a los 5
  segundos, y una tabla ocupada se reintenta cada 2 segundos durante un minuto como máximo.
  `event-management` se detiene entonces con un error que nombra la tabla, lista lo que ya está
  hecho e incluye una consulta que lista las sesiones que la retienen; continúa desde ahí al
  reiniciarse. Si se sigue deteniendo, busca consultas SQL o de BI de larga duración, el borrado de
  un inquilino o el trabajo de compresión o de retención del almacén.
- **La reconstrucción de la clave de una tabla tarda más de 40 segundos** una vez bloqueada: se
  deshace, la tabla conserva su clave anterior, y `event-management` se detiene de inmediato en cada
  arranque posterior sin tocarla, así que la ingesta no se atasca en cada reinicio. Recrea la
  instancia o, para intentarlo una vez más (por ejemplo, en un almacenamiento más rápido), ejecuta la
  sentencia `COMMENT ON INDEX` que da el error. Si la tabla guarda eventos fechados muy en el pasado,
  eliminarlos como en [Cuenta los fragmentos](#v0190-chunk-count) acorta la reconstrucción; después
  ejecuta la sentencia `COMMENT ON INDEX`.
- **Sin espacio para bloqueos:** eliminar un índice o reconstruir una clave bloquea cada fragmento
  de su tabla; el error lo indica y nombra el ajuste que hay que aumentar.

#### Después de actualizar {#v0190-after}

- **Comprueba dónde están las dos primarias de las bases de datos.** Los traspasos de arriba pueden
  dejar las dos en un mismo nodo.
  [Dónde se ejecutan las primarias de las bases de datos](./bootstrap.md#ha-database-primaries)
  explica cómo comprobarlo y cómo mover una.
- **Vigila el almacén de respaldos durante tres semanas.** Nada sale de la nueva ventana de 30 días
  de la base de datos relacional hasta que tiene 30 días, así que su parte del almacén crece durante
  unas tres semanas, hacia unas cuatro veces el registro que conserva hoy más unos 23 respaldos base
  nocturnos más, y después se estabiliza.
- **Vigila las lecturas rechazadas en el techo de cada inquilino:**
  `devicechain_eventsources_total_readings_rate_limited`,
  `devicechain_lwm2mingest_ingest_samples_shed_total` y
  `devicechain_sparkplugingest_ingest_samples_shed_total`. Un inquilino que descarta ahí está
  enviando más lecturas de las que permite su nivel. Eleva la tasa de ingesta de su nivel, o haz que
  sus dispositivos envíen menos.
- **Una instancia existente conserva los tamaños de sus volúmenes.** La actualización indica que
  esta versión crea un almacén de eventos de 32Gi; para ampliar uno de 8Gi, consulta
  [Volumen del almacén de eventos](./bootstrap.md#event-store-volume). Un almacén de respaldos
  existente también conserva su tamaño.

Volver a `v0.18.0` no necesita ningún cambio de datos: guarda y lee eventos con las claves nuevas y
sin los índices eliminados, y devuelve `event-management` a un pod y el techo de ingesta a contar
mensajes. Una lectura es más lenta allí: `v0.18.0` lista los eventos registrados contra un anclaje de
relación recorriendo los eventos del inquilino en el rango de tiempo pedido, comprimidos incluidos.
Una configuración de `device-management` que fije `inMemoryCache.perDeviceCacheEntries` o
`inMemoryCache.perDeviceCacheMiB` tiene que quitarlos antes: `v0.18.0` no arranca con ninguno de los
dos.

#### Qué cambia {#v0190-what-changed}

##### Ingesta {#v0190-ingest}

###### Un stream de ingesta lleno rechaza eventos nuevos en lugar de descartar los no leídos {#v0190-backpressure}

Cuando un consumidor se atrasaba tanto que su cola sin leer llenaba `inbound-events` o
`resolved-events`, el stream descartaba sus eventos más antiguos para hacer sitio, y eran eventos
que nadie había procesado todavía. Al dispositivo ya se le había dicho que se aceptaron.

Ahora, cuando la cola sin leer de `device-management` en `inbound-events`, o la de
`event-management` en `resolved-events`, alcanza el 90% de lo que cabe en el stream, la plataforma
deja de aceptar eventos nuevos hasta que la cola baja del 80%. Los dos streams conservan su semana
de eventos ya procesados, y ese historial no cuenta para el 90%: solo cuentan los eventos sin
leer. Un stream lleno también rechaza antes de descartar ese historial tan deprisa que alcance
eventos sin leer; consulta
[Un stream lleno también rechaza antes de que los eventos grandes expulsen a los no leídos](#v0190-ingest-history-runway).

- La ingesta **HTTP** responde `503` con `Retry-After: 10` mientras rechaza. Reintenta ante un
  `503`. Un `503` sin `Retry-After` sigue significando que la publicación en sí falló. Una solicitud
  por encima del techo de su propio inquilino recibe antes un `429`; solo una que esté dentro de él
  puede recibir el `503` ([Lecturas, no mensajes](#v0190-ingest-readings)).
- Los dispositivos **MQTT** ya recibieron el acuse del bróker. Sus mensajes esperan en el stream de
  captura hasta que se reanuda la ingesta.
- Las lecturas de **Sparkplug y LwM2M**, y los mensajes de un bróker MQTT externo, se descartan y se
  cuentan, porque esos protocolos no dan a la plataforma forma de hacer que el dispositivo
  reintente. Las transiciones de conexión y desconexión se siguen aceptando. La compuerta de
  contrapresión no limita cuántas admite (el techo del inquilino sigue aplicándose a la toma del
  broker), así que una flota que se reconecta en bucle aún puede llevar el stream a su techo, donde
  descarta sus eventos más antiguos como antes.
- El rechazo afecta a **todos los inquilinos**, porque los streams son compartidos. Un
  `device-state` o un `event-processing` lentos no lo provocan.
- El simulador y el arnés de carga cuentan un `503` con `Retry-After` como rechazo, no como
  fallo. Consulta [la entrada sobre los informes de las pruebas de carga](#v0190-loadtest-refusals)
  para ver cómo lo informan.

Consulta [Contrapresión en la ruta de ingesta](./observability.md#ingest-backpressure). Ningún
stream se reconfigura, y un servicio que siga en la versión anterior conserva su comportamiento
anterior hasta que se actualice.

###### Un stream lleno también rechaza antes de que los eventos grandes expulsen a los no leídos {#v0190-ingest-history-runway}

El rechazo de arriba valoraba cada evento sin leer al tamaño medio del stream, porque el bróker
informa de los bytes totales de un stream y no de los de los eventos que un consumidor no ha leído.
En un stream lleno eso es la parte sin leer del NÚMERO de eventos. Una ráfaga de eventos grandes
sobre un historial de eventos pequeños ya procesados podía, por tanto, llenar `inbound-events` o
`resolved-events` mientras la cola sin leer marcaba más o menos la mitad del límite, y el stream
descartaba entonces eventos que nadie había procesado. Medir después de cada publicación no lo
habría evitado.

- **El stream también rechaza cuando se le agota el historial procesado.** Un stream lleno descarta
  primero sus eventos más antiguos, así que los eventos ya procesados que aún conserva por delante
  de los no leídos son lo que se agota antes de que se pierda uno sin leer. Los servicios que
  escriben en el stream rechazan ahora eventos nuevos cuando ese historial se descartaría entero en
  30 segundos al ritmo al que el stream lo está descartando, y vuelven a aceptarlos cuando duraría
  un minuto, o el consumidor lo ha leído todo. Las dos magnitudes se calculan a partir de números
  de secuencia que informa el bróker, así que la regla no depende del tamaño de los eventos. Un
  stream lleno cuyo consumidor mantiene el ritmo no rechaza nada. Eliminar un inquilino tampoco hace
  por sí solo que un stream rechace, salvo que sus eventos sean los más antiguos que conserva un
  stream lleno: entonces el stream puede rechazar, sin perder nada, hasta que el consumidor se ponga
  al día. Consulta [Contrapresión en la ruta de ingesta](./observability.md#ingest-backpressure) para
  ese caso y para un stream lleno que contiene menos de un minuto de su propio tráfico.
- **Un evento que el consumidor recibió pero no ha confirmado cuenta como no leído**, también uno
  que espera a entregarse de nuevo tras un fallo. Mientras un evento así está cerca del principio de
  un stream lleno, el stream puede rechazar eventos nuevos hasta que se confirme o se abandone.
- **Cada servicio mide también el stream en cuanto él mismo ha escrito alrededor de una milésima
  parte del límite del stream** desde su última medición, como mucho cada 100 ms, además de cada
  5 segundos.
- Una serie nueva,
  `devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`, muestra
  cuánto duraría ese historial. `JetStreamIngestBackpressureEngaged` también se dispara por este
  rechazo, y su descripción explica ahora cómo distinguir las dos causas.
- Aún se puede perder un evento sin leer cuando los servicios que escriben en un stream gastan,
  entre dos de sus mediciones, más historial procesado del que se vio descartar al stream en los
  30 segundos anteriores, o cuando un stream llega a su límite por primera vez casi sin historial
  procesado.

Ningún stream se reconfigura y no se añade ningún ajuste.

###### El techo de ingesta de un inquilino cuenta lecturas, y HTTP lo comprueba antes de la compuerta de contrapresión compartida {#v0190-ingest-readings}

El techo de ingesta por inquilino cuenta ahora **lecturas** en todos los transportes de dispositivo.
Una lectura es un valor almacenado: una clave de una entrada de medición, una ubicación o una
alerta. Antes, HTTP y MQTT cobraban una unidad por mensaje, llevara las lecturas que llevara. Un
inquilino con el valor por defecto de 1000 por segundo podía enviar hasta 256 000 lecturas por
segundo, mucho más de lo que una instalación puede almacenar, y la compuerta de contrapresión
acababa rechazando los eventos de todos los inquilinos.

- **HTTP y MQTT:** un mensaje se cobra una vez antes de decodificarlo, como antes, y después una vez
  por cada lectura que lleva. En HTTP, un inquilino por encima de su techo recibe `429`. En MQTT, el
  mensaje se confirma y se descarta, como ya ocurría con un mensaje por encima del techo. Ese
  mensaje se cuenta en el nuevo `devicechain_eventsources_total_msg_reading_limited` y sus lecturas
  en el nuevo `devicechain_eventsources_total_readings_rate_limited`. Se recibió, así que también
  se cuenta en `devicechain_eventsources_total_inbound_messages`, y no en
  `devicechain_eventsources_total_msg_rate_limited`, que sigue contando solo los mensajes
  descartados antes de decodificar.
- **HTTP comprueba primero el techo del propio inquilino.** Una solicitud por encima del techo de su
  inquilino recibe `429` aunque la plataforma esté rechazando eventos. Solo una solicitud dentro del
  techo de su inquilino puede recibir el `503` de contrapresión. El cuerpo de la solicitud se lee y
  se decodifica ahora antes de comprobar la contrapresión, y una solicitud rechazada por
  contrapresión se cuenta ahora como recibida.
- **Los brokers MQTT externos** también comprueban el techo del inquilino antes de la compuerta de
  contrapresión, así que un mensaje por encima del techo de su inquilino mientras la plataforma
  rechaza se cuenta en `devicechain_eventsources_total_msg_rate_limited` en lugar de en
  `devicechain_eventsources_total_msg_backpressured`.
- **LwM2M** ya cobraba lecturas, a 25 veces el techo. Ahora las cobra al propio techo, así que el
  presupuesto de lecturas LwM2M de un inquilino es **25 veces menor** que antes.
- **Sparkplug tiene ahora un techo de ingesta por inquilino**, contado en lecturas:
  `ingestRateLimit.messagesPerSecond` (por defecto 1000) e `ingestRateLimit.burst` (por defecto
  2000). Las lecturas de cada mensaje DATA que lo superan se descartan y se cuentan en
  `devicechain_sparkplugingest_ingest_samples_shed_total`. Los nacimientos, las muertes y el ritmo
  de mensajes no se miden. Consulta
  [Qué acota la ingesta de Sparkplug](./edge-services.md#unbounded-sparkplug-ingest).
- **El valor por defecto sigue siendo 1000 por segundo con una ráfaga de 2000, ahora en lecturas.**
  Una flota que envía una lectura por mensaje no nota ningún cambio. Una flota que agrupa varias
  lecturas en un mensaje queda limitada a 1000 lecturas por segundo por inquilino. Si ese tráfico es
  legítimo, eleva la tasa de ingesta del nivel del inquilino. La consola muestra ahora la tasa de
  ingesta en lecturas por segundo.

Con el valor por defecto, los dispositivos de un inquilino se admiten como máximo a 5000 lecturas
por segundo, incluso con un atraso vaciándose y ambos servicios de borde en marcha, lo que queda por
debajo de las 6000 por segundo que se midió que almacena una instalación de alta disponibilidad por
defecto, con `event-management` en dos pods, en el clúster descrito en
[qué permite el valor por defecto](../concepts/governance.md#ingest-default). En un clúster más
pequeño, o con más de una réplica de `event-sources`, baja el valor por defecto.

Para localizar antes de actualizar los inquilinos que envían más de 1000 lecturas por segundo:

- Para dispositivos JSON por MQTT y HTTP, cuenta las mediciones almacenadas por segundo del
  inquilino durante una hora de mucha actividad.
- Para LwM2M, compara la tasa actual de lecturas del inquilino con su techo, no con 25 veces su
  techo.
- Para Sparkplug, `rate(devicechain_sparkplugingest_measurements_emitted_total[5m])` son las
  lecturas de los inquilinos de las fuentes de ese pod, ya que cada fuente pertenece a un inquilino.
- Un [agente de borde](./edge-services.md) vuelve a publicar las lecturas que tenía en búfer tan
  rápido como puede tras una reconexión. Un sitio que agrupa varias lecturas en un mensaje puede
  superar el techo de su inquilino mientras se pone al día.

Volver a la versión anterior restaura el cobro por mensaje. No cambian datos almacenados ni el
esquema, y no se renombra ninguna clave de configuración.

###### Un evento lleva como máximo 256 lecturas, y las pasarelas dividen los mensajes mayores {#v0190-reading-limit}

**Un evento lleva ahora como máximo 256 lecturas, en todos los transportes, y el límite no es
configurable.** Una lectura es un valor de métrica de una medición, o una entrada de ubicación o de
alerta. Antes, el evento JSON de dispositivo en HTTP y MQTT admitía hasta 1000 por defecto, y un
operador podía subir o bajar ese valor.

Para localizar antes de actualizar los dispositivos afectados, establece
`maxReadingsPerMessage: 256` en la configuración de event-sources de `v0.18.0` y vigila
`total_msg_too_many_readings`: cada mensaje que cuente es uno que esta versión rechaza. Cambia esos
dispositivos para que envíen como máximo 256 lecturas por mensaje.

- **HTTP y MQTT:** un mensaje con más de 256 lecturas se rechaza entero, nunca se recorta. HTTP
  responde `400`, indicando el número de lecturas y el límite. En MQTT no se avisa al dispositivo,
  porque el bróker confirma antes de decodificar; el rechazo se cuenta en
  `total_msg_too_many_readings` y el mensaje va al stream de decodificación fallida. Los mensajes
  capturados antes de la actualización y decodificados después, incluidos los que sigan en la cola
  de un agente de borde, se evalúan con el límite nuevo.
- **`maxReadingsPerMessage` queda retirado.** El servicio arranca, registra un aviso y lo ignora;
  un valor por debajo de 256 tampoco se respeta ya. Elimina la clave.
- **Sparkplug B:** un mensaje con más de 256 valores de métrica se convertía en un solo evento.
  Ahora se convierte en eventos consecutivos de como máximo 256, cada valor con su propia marca de
  tiempo. Las consultas que cuentan *eventos* verán más; las lecturas almacenadas son las mismas. Las
  reglas ven cada evento por separado, así que una regla de tiempo de retención o de ausencia puede
  dispararse ahora entre dos eventos de un mismo mensaje ancho.
- **LwM2M:** un Notify con más de 256 valores numéricos conservaba los 256 primeros y descartaba el
  resto. Ahora se almacena como varios eventos, y el presupuesto de muestras del inquilino se cobra
  evento a evento; los eventos que no puede admitir se cuentan en `ingest_samples_shed_total`. Se
  elimina `notify_samples_truncated_total`: quítala de cualquier panel o alerta.

###### Se almacena un evento que ya estaba en cola con miles de lecturas {#v0190-large-events}

Un evento con más lecturas de las que caben en una sola sentencia de la base de datos (más de
unas 5950 mediciones, 5450 ubicaciones o 6550 alertas, o 9350 anclajes de relación) no se podía
almacenar nunca. El controlador de la base de datos rechazaba la sentencia, `event-management`
reintentaba el evento hasta agotar sus entregas y después lo registraba en el stream
`failed-events` como un fallo posterior, no como un problema del propio evento. Antes del
[límite de 256 lecturas](#v0190-reading-limit), un evento así podía venir de un mensaje Sparkplug
con miles de métricas, o de un transporte JSON cuyo `maxReadingsPerMessage` se hubiera subido por
encima de su valor predeterminado de 1000, y puede que alguno siga esperando en un stream al
actualizar. Ahora el almacén de eventos escribe un evento grande en tantas sentencias como necesite,
dentro de la misma transacción, así que se almacena entero o no se almacena, como cualquier otro, y
una reentrega suya no añade nada. El mismo camino de reintentos y fallo posterior lo seguía un
evento de cambio de estado cuyo identificador de sesión es demasiado grande para la columna de 64
bits con signo de la base de datos; ese evento se registra ahora como inválido en su primera
entrega.

###### Se rechaza una lectura fechada más de 366 días antes de llegar {#v0190-event-age-limit}

El `occurredTime` de una lectura, en el sobre o en cualquier entrada, puede ser ahora como mucho 366
días anterior al momento en que la plataforma la recibió. Uno más antiguo se rechaza, nunca se mueve
a una hora posterior:

- **HTTP y MQTT:** se rechaza el mensaje entero, como en cualquier otro rechazo de marca de tiempo.
  HTTP responde `400`, y una publicación MQTT se envía a la cola de mensajes fallidos. Ambos se
  cuentan en `total_msg_invalid_event_time` en `event-sources`.
- **Sparkplug y LwM2M:** solo se descartan las lecturas demasiado antiguas, y el resto del mensaje
  se almacena. El mensaje de una pasarela lo compone la pasarela, no lo envía un dispositivo como una
  unidad, y la marca de tiempo de una métrica de Sparkplug es cuándo cambió su valor por última vez,
  así que un valor sin cambios durante más de 366 días se descarta de cada nacimiento hasta que
  cambie. Un nodo sin reloj configurado, que informa horas cercanas a 1970, ve descartadas todas sus
  lecturas. Se cuentan en `samples_too_old_dropped_total` en `sparkplug-ingest` y en
  `telemetry_too_old_dropped_total` en `lwm2m-ingest`, y el servicio registra un aviso que nombra el
  dispositivo.
- Cualquier otro evento fechado tan atrás se envía a la cola de mensajes fallidos con el motivo
  `Invalid` en su primera entrega, y se cuenta en `resolve_event_time_too_old_total` en
  `device-management`.

Antes se aceptaba cualquier hora pasada, y el almacén de eventos guarda una partición (fragmento) por
intervalo de fragmento, un día por defecto. Un dispositivo podía crear una partición por cada día
hasta el año 1, y toda operación posterior que recorre cada partición, como una actualización que
reconstruye una clave, se volvía más lenta con ellas. [Cuenta los fragmentos](#v0190-chunk-count)
explica qué hace la actualización de esta versión con una tabla que ya tiene demasiados.

- Un dispositivo sin reloj debe omitir `occurredTime`; la plataforma fecha entonces la lectura al
  llegar. La época, `1970-01-01T00:00:00Z`, se rechaza.
- Los mensajes que ya esperan en la plataforma al actualizar se juzgan según la hora en que llegaron,
  así que la nueva versión rechaza uno fechado más de 366 días antes de esa hora.
- El almacén local del agente de borde no tiene límite de antigüedad propio; consulta
  [el almacén local](./edge-services.md#the-spool).
- El límite es fijo. No hay ningún ajuste para él.

###### Una fuente MQTT sobre tu propio bróker se conecta con su propio identificador de cliente, desde un solo pod a la vez {#v0190-external-mqtt-client-id}

Una fuente de eventos que lee de un bróker MQTT que tú operas se conectaba con el identificador de
cliente `devicechain`, fuera cual fuera la instancia, la fuente o el pod. Un bróker mantiene una
sesión por identificador de cliente y corta la conexión más antigua cuando llega una segunda. Así
que, cada vez que dos conexiones usaban ese identificador, cada una le quitaba la sesión a la otra
en bucle, y los mensajes que llegaban mientras cualquiera de las dos se reconectaba se perdían sin
contarse. Ocurría con dos pods de `event-sources`, en cada actualización progresiva (el pod nuevo
arranca antes de que se detenga el viejo), con dos fuentes así sobre un mismo bróker y con dos
instancias que leían un mismo bróker. La fuente de pasarela de la instalación por defecto lee el
flujo de la plataforma y no se veía afectada.

Ahora cada fuente se conecta como `devicechain:<instancia>:<fuente>:<pod>`, y solo un pod lee una
fuente dada a la vez. Los pods acuerdan cuál mediante un arrendamiento guardado en el bróker de
mensajería de la plataforma. Los demás no conectan nada para esa fuente hasta que se libera.
Consulta la [Matriz de transportes](../reference/transport-matrix.md#external-mqtt-broker). Lo que
hay que permitir antes en tu bróker está en el [paso 12](#v0190-mqtt-client-id), y la única doble
lectura durante el despliegue, en [Durante la actualización](#v0190-during).

- Un pod que se detiene cede la fuente a otro pod en unos dos segundos. Tras una pérdida abrupta
  (un fallo de nodo, un `SIGKILL`, una terminación por falta de memoria), la fuente queda sin leer
  unos 30 segundos más lo que tarde en reconectarse. La fuente sigue siendo como mucho una vez: tu
  bróker no conserva lo que entrega en ninguno de esos huecos.
- Un pod que toma una fuente y no puede llegar a tu bróker la libera y vuelve a intentarlo cada 15
  segundos, en lugar de detener el servicio. Un bróker que rechaza la suscripción sigue deteniendo
  el servicio.
- Ejecutar más pods de `event-sources` ya no significa más lectores de una fuente externa. Antes
  tampoco añadía capacidad, porque los pods se quitaban una misma sesión. Ahora añade pods en
  espera.
- Nuevo indicador: `devicechain_eventsources_external_mqtt_owner{source}`, que vale 1 en el pod que
  lee la fuente y 0 en los demás, y un nuevo contador,
  `devicechain_eventsources_total_msg_not_owner{source}`, para los mensajes que un pod descarta
  porque acaba de perder la fuente. Nueva alerta: `ExternalMqttSourceNotReadByOnePod` (aviso),
  cuando una fuente lleva dos minutos sin que la lea ningún pod, o leída por más de uno. Consulta
  [Una fuente MQTT externa que nadie lee](./observability.md#external-mqtt-owner).

##### Resolución {#v0190-resolution}

###### device-management responde las búsquedas repetidas desde memoria {#v0190-local-cache}

- **Cada réplica de `device-management` guarda en memoria lo que leyó de sus cachés de clave-valor
  durante hasta cinco segundos**, y responde desde ahí las búsquedas repetidas del mismo
  dispositivo, tipo de dispositivo o inquilino. Un tiempo de vida de caché menor de cinco segundos
  también lo acorta.
- **Un cambio puede tardar hasta cinco segundos más en llegar a los eventos que resuelven las demás
  réplicas.** Un dispositivo borrado, o vuelto a crear con el mismo token, puede seguir
  resolviéndose a través de su registro anterior en otra réplica durante esos segundos, y una regla
  cuyo alcance de grupo acaba de cambiar puede evaluarse allí con el alcance anterior. Un flanco de
  alarma para un dispositivo recién borrado se sigue descartando de inmediato en todas las réplicas.
- **Hasta 131 072 entradas o 24 MiB por réplica** para cada una de las tres cachés que se guardan
  por dispositivo (un dispositivo por token, sus relaciones seguidas, sus pertenencias a grupos);
  4096 entradas o 4 MiB para las cachés por tipo de dispositivo y por inquilino.
  `inMemoryCache.perDeviceCacheEntries` e `inMemoryCache.perDeviceCacheMiB` cambian el límite; sube
  con ellos el límite de memoria del servicio.
- **Un dispositivo que informa con menos frecuencia que cada cinco segundos no se responde desde
  memoria**, por grande que sea la caché.
- **Las búsquedas de perfil, relaciones y alcance de grupos de un evento se hacen a la vez**, y
  también sus búsquedas de pertenencia a grupos, así que un evento que no encuentra nada en memoria
  espera una sola ida y vuelta a NATS en lugar de tres. La base de datos se sigue leyendo una
  consulta tras otra, así que `resolution.workers` sigue contando las conexiones como antes.
- **Métricas nuevas** cuentan las búsquedas respondidas desde memoria, las entradas descartadas, el
  tamaño y los límites (`kv_cache_local_max_entries`, `kv_cache_local_max_bytes`);
  `kv_cache_request_duration_seconds{op="get"}` cuenta ahora solo las búsquedas que la memoria no
  pudo responder. Consulta [Cachés que dejan de responder](./observability.md#kv-caches).

###### device-management comprueba desde memoria una credencial de dispositivo repetida {#v0190-credential-cache}

- **Cada réplica guarda en memoria, durante hasta cinco segundos, una credencial de dispositivo que
  acaba de verificar**, y comprueba los siguientes eventos del dispositivo contra esa copia en lugar
  de leer la base de datos. La copia se comprueba exactamente igual que la credencial almacenada: la
  contraseña de `MQTT_BASIC` se sigue comparando en cada evento, y una expiración sigue surtiendo
  efecto en su momento. Una credencial que no se pudo verificar nunca se guarda.
- **Una revocación puede tardar ahora hasta cinco segundos en una réplica que no recibió el
  aviso.** Deshabilitar, borrar, reasignar o cambiar de cualquier otra forma una credencial, y
  reemplazar, editar o borrar su dispositivo, descartan la copia en la réplica que hace el cambio
  antes de responder, y avisan a las demás réplicas para que descarten la suya. Si ese aviso se
  pierde (una réplica que se reconecta a NATS, o réplicas que aún ejecutan la versión anterior
  durante la actualización), la copia de esa réplica caduca en cinco segundos como máximo. Hasta
  ahora una revocación surtía efecto en el siguiente evento en todas las réplicas. Consulta
  [Cuánto tarda en aplicarse una revocación](../guides/device-credentials.md#revocation-timing).
- **Las conexiones MQTT siguen leyendo la base de datos cada vez**, así que una credencial revocada
  no puede abrir una conexión nueva en ninguna réplica.
- **Como máximo 65 536 credenciales o 16 MiB por réplica**, un límite fijo; con los valores por
  defecto las cachés en memoria del servicio guardan como máximo 96 MiB en lugar de 80. Métricas
  nuevas cuentan las comprobaciones respondidas desde la copia, las credenciales descartadas, su
  tamaño y los avisos de descarte enviados y recibidos.

##### Almacenamiento {#v0190-storage}

###### Las claves del almacén de eventos empiezan por el tiempo, y desaparecen dieciséis índices {#v0190-event-store-keys}

Las claves que impiden almacenar un evento dos veces empiezan ahora por el inquilino y el instante
del evento, en lugar del inquilino y un resumen criptográfico del evento, así que la clave de un
evento nuevo queda junto a la anterior y la base de datos reescribe muchas menos páginas de índice.
Se eliminan dieciséis índices: doce que repetían otro índice o que ninguna consulta leía, y los
índices por inquilino y tiempo de los eventos base, las mediciones, las ubicaciones y las alertas,
a los que ahora responden las claves nuevas. Cada fila almacenada actualiza menos índices que en
`v0.18.0`: una fila de evento base, dos en lugar de cinco (tres en lugar de seis con un id
alternativo); una fila de medición, tres en lugar de cinco; una fila de ubicación o de alerta, uno
en lugar de cuatro; una fila de anclaje de relación, dos en lugar de cuatro; y una fila de cambio de
presencia, uno en lugar de cuatro. Un evento de medición con una lectura y sin anclajes actualiza
cinco índices en lugar de diez.

Si `event-management` se negó a arrancar porque una tabla tiene más de 500 fragmentos, el
procedimiento de `drop_chunks` por lotes al que remite su registro está en
[Cuenta los fragmentos](#v0190-chunk-count).

- Los eventos se siguen almacenando una sola vez, y toda lectura que sirve `event-management`
  sigue usando un índice.
- **La lista de eventos de un dispositivo trabaja más sobre los datos recientes.** El total que
  acompaña a la lista, y una lista filtrada por tipo de evento, recorren ahora todas las filas del
  dispositivo que aún no están comprimidas (la última semana, por defecto), incluidas las de un
  dispositivo con el mismo token en otro inquilino. Cuando el almacén tiene pocos tokens de
  dispositivo distintos (en nuestras pruebas, 40 en lugar de 100), la primera página de un
  dispositivo que lleva un tiempo en silencio mientras los demás siguen enviando puede tardar
  bastante más.
- **Acceso por SQL y BI.** Una consulta sobre los eventos, lecturas, ubicaciones, alertas o anclajes
  de un inquilino que filtra solo por tiempo usa un índice. Una consulta solo por tiempo sobre
  `analytics.state_change_events` lee todas las filas de tu inquilino en cada fragmento sin
  comprimir que toca; añade `device_token` para usar un índice. Para unir `analytics.event_anchors`
  con `analytics.events`, une por `event_id` y por `occurred_time`. Consulta
  [Notas prácticas](../guides/sql-and-bi-access.md#practical-notes).

###### Los lotes se escriben en menos sentencias {#v0190-batch-writes}

- **`event-management` escribe cada lote con una sentencia por tabla para cada inquilino que
  contiene**, dentro de la misma transacción, en lugar de varias por evento. Lo que se almacena no
  cambia, un evento se sigue reconociendo solo después de que su lote se confirme, y un evento
  reentregado no añade nada. Cuando la base de datos rechaza una fila de una sentencia que lleva
  varios eventos, el lote se vuelve a escribir evento a evento para encontrarlo;
  `persist_batch_fallbacks_total` lo cuenta. Los eventos de conexión y desconexión se siguen
  escribiendo uno a uno. Consulta [Persistencia de eventos](./observability.md#event-persistence).
- **`device-state` escribe el estado de todos los dispositivos que ya tienen uno en una sola
  sentencia por cada inquilino del lote.** Un dispositivo que aparece por primera vez se sigue
  creando por separado, y `updatedAt` sigue avanzando cada vez que se escribe su estado.
- **Cada conexión del pool a la base de datos sigue abierta entre usos**, hasta el tamaño del pool;
  antes solo lo hacía la mitad, y un servicio que usaba más de la mitad se reconectaba en cada
  consulta. Una conexión se sigue cerrando una hora después de abrirse. La base de datos puede
  mostrar más conexiones inactivas de cada servicio después de un periodo de mucha carga; con la
  configuración predeterminada lo cubre el
  [presupuesto de conexiones](./bootstrap.md#connection-budget), pero si ejecutas un servicio con
  `replicas` por encima de 1 o subiste su `maxOpenConnections`, comprueba el límite de conexiones de
  la instancia. `maxIdleConnections` se sigue respetando cuando se fija.

###### Nuevos valores por defecto del flujo de eventos {#v0190-pipeline-defaults}

| Ajuste | `v0.18.0` | `v0.19.0` |
| --- | --- | --- |
| `event-management` `persistence.writers` | 5 | 10 |
| `event-management` `persistence.maxBatch` | 32 | 64 |
| `event-management` `persistence.lingerMillis` | 0 | 10 |
| `device-state` `projection.writers` | 5 | 10 |
| Límite de CPU de `event-processing` | 500m | 1 núcleo |
| Pods de `event-management` con `--ha` (no con `--compact --ha`) | 1 | 2 |

Una instalación que fija alguno de estos valores conserva el suyo; `persistence.lingerMillis: 0`
sigue significando sin espera. Un escritor que encuentra menos eventos que un lote completo espera
ahora hasta 10 milisegundos a que lleguen más, así que por debajo de unos pocos cientos de eventos
por segundo por pod cada evento se confirma hasta 10 milisegundos más tarde que antes y
`persist_duration_seconds` sube más o menos eso; con cola acumulada no cambia nada. En la prueba de
rendimiento de la versión a 3000 eventos por segundo, las transacciones por evento almacenado
bajaron un 37% (de 0,126 a 0,080), el tiempo mediano para almacenar un evento subió unos 5
milisegundos y el 1% más lento bajó un 18%. Esa comparación también cambió las claves del almacén
de eventos y la compresión de su archivo, las solicitudes de CPU de los servicios y su colocación, los escritores de `device-state` y el límite de CPU de la detección, y las solicitudes y el límite de memoria de los servidores NATS, así que no aísla la espera. En una ejecución de cinco
minutos con 6800 eventos por segundo ofrecidos, el camino que confirma un evento cada vez se llevó
el 4,8% de la CPU de `event-management`, con 0,050 transacciones por evento, frente al 24% en la
prueba anterior, con la compilación previa a la espera y en otros nodos de servicios. Las
instalaciones con `--compact` reciben los mismos valores salvo el segundo pod; sus solicitudes no
cambian.

**Dos pods de `event-management` con `--ha`.** En un grupo de servicios de tres nodos, uno de ellos
ejecuta también el servidor NATS que lidera el stream de eventos entrantes. En la prueba de
rendimiento de la versión, el planificador puso allí el único pod de `event-management` junto con
`device-state`; ese nodo funcionó al 94-95% de CPU y, con un solo pod, el almacenamiento bajó a 6592
eventos por segundo con 6800 ofrecidos, la primera etapa en quedarse atrás. Un segundo pod permite
almacenar usando la CPU de otro nodo. La cifra de [Rendimiento](#v0190-performance) se midió con dos pods. Los pods prefieren nodos distintos, pero es una preferencia, no
una garantía. Cada uno llena sus propios lotes, así que el almacén de eventos confirma más o menos
el doble de transacciones por evento; su nodo se mantuvo por debajo del 70% de CPU. Si instalas el
chart por tu cuenta, su valor predeterminado sigue siendo una réplica: establece
`functionalAreas.event-management.replicas: 2` y, en un almacén de eventos creado con el OpenTofu
de este repositorio, establece `event_management_replicas = 2`, que reserva las conexiones del
segundo pod.

##### Dimensionamiento y ubicación {#v0190-sizing}

###### Los servicios de la ruta de eventos solicitan lo que usan y se reparten entre los nodos {#v0190-service-sizing}

Los cinco servicios que procesan cada evento solicitan la CPU que se midió que usan a 6000 eventos
por segundo (los valores están en [Haz sitio](#v0190-room)); antes, con 100m cada uno, solicitaban
mucho menos que eso, y el planificador, que ubica los pods según sus solicitudes, juntaba a los más
ocupados. `event-sources` y `device-state` pueden usar ahora hasta 2 núcleos, como
`device-management` y `event-management`. Los cinco prefieren nodos distintos (con tres nodos,
normalmente no más de dos por nodo); `functionalAreas.<servicio>.eventPathSpread: false` lo
desactiva para un servicio. `device-management`, `event-management` y `event-sources` prefieren
además un nodo que no ejecute el primario del almacén de eventos. Una preferencia solo se aplica
cuando se planifica un pod: tras una conmutación por error, un pod que ya se ejecuta en el nodo del
nuevo primario se queda hasta que se vuelva a planificar.

El almacén de eventos de una instancia nueva recibe un volumen de 32Gi en lugar de 8Gi. Sus
servidores NATS solicitan 500m de CPU y 768Mi de memoria y tienen un límite de 2Gi; antes no
solicitaban nada, lo que los convertía en los primeros pods desalojados cuando un nodo se quedaba
sin memoria. El límite de 2Gi se dimensionó con ingesta sostenida; no se midió un servidor
poniéndose al día tras un reinicio, así que si uno queda `OOMKilled` durante la actualización, sube
`nats_memory_limit` en el `terraform.tfvars` de la instancia y vuelve a ejecutar `dcctl upgrade`.
Consulta [Dimensionamiento de los servicios](./bootstrap.md#service-sizing) y
[El intermediario de mensajes](./bootstrap.md#broker-sizing).

###### Un servicio que ejecuta más de un pod mantiene sus pods en nodos distintos {#v0190-own-pods-apart}

Con más de una réplica, cada uno de los cinco servicios de la ruta de eventos (`device-management`,
`event-management`, `device-state`, `event-sources` y `event-processing`) prefiere un nodo que no
ejecute ya otro de sus propios pods. Un servicio con [el reparto de la ruta de
eventos](#v0190-service-sizing) deja de recibir el reparto predeterminado del clúster, y esto le
devuelve la mitad que mantiene las réplicas de un servicio en nodos distintos; la otra mitad, zonas
distintas, no se devuelve. Es una preferencia que el planificador pondera junto con las demás del
servicio (menos servicios de la ruta de eventos por nodo y, para `device-management`,
`event-sources` y `event-management`, sin la primaria del almacén de eventos), no una garantía.

- Con `--ha`, se aplica a los dos pods de `event-management`.
- **Si instalas el chart por tu cuenta**, se aplica a cualquiera de los cinco que ejecutes con
  `replicas` por encima de uno, y figura en la antiafinidad del pod, junto a la preferencia por
  evitar la primaria del almacén de eventos en los tres servicios que la tienen. Un servicio con
  `eventPathSpread: false` conserva en su lugar el reparto predeterminado del clúster, que también
  prefiere zonas distintas, y no recibe esta preferencia.

###### Tus propios valores del chart {#v0190-chart-values}

Si instalas el chart tú mismo:

- Un `resources.requests.cpu` de nivel superior ya no llega a `device-management`,
  `event-management`, `device-state`, `event-sources` ni `event-processing`: su solicitud medida
  prevalece. Define las suyas en `functionalAreas.<servicio>.resources.requests`, o establece
  `useMeasuredRequests: false` para que las solicitudes de nivel superior vuelvan a aplicarse a
  todos los servicios. Con `useMeasuredRequests: false`, una solicitud de nivel superior por encima
  de 1 núcleo hace fallar ahora la generación para `event-processing`, porque supera el límite
  propio de ese servicio.
- Un `resources.limits.cpu` de nivel superior por encima de 2 núcleos baja ahora `event-sources` y
  `device-state` a 2, y ya no llega a `event-processing`, que tiene siempre 1 núcleo salvo que se
  defina en `functionalAreas.event-processing.resources.limits`.
- Un límite de CPU propio de un servicio por debajo de su solicitud medida se rechaza ahora,
  nombrando `measuredRequests`.
- Los valores de nivel superior del chart y el bloque de cada servicio en `functionalAreas`
  rechazan ahora una clave que el chart no lee, así que una clave mal escrita hace fallar la
  generación.
- `eventPathSpread` está activado en los cinco servicios; activarlo en otro servicio lo reparte
  junto con ellos.
- Un servicio que ejecutes con `replicas` por encima de uno prefiere nodos que no ejecuten otro de
  sus propios pods ([Pods propios separados](#v0190-own-pods-apart)).

###### Las primarias de las bases de datos prefieren nodos distintos {#v0190-primary-spread}

Cada base de datos prefiere un nodo que no ejecute la primaria de otra base de datos de
DeviceChain. En las pruebas, con tres nodos de 8 vCPU, el nodo que ejecutaba las dos primarias
estaba al 94-98% de CPU mientras los otros dos estaban al 45-51%. Es una preferencia: un clúster
más pequeño sigue planificando todas las instancias de base de datos. Actúa cuando se planifica un
pod de base de datos, así que una conmutación por error o un traspaso todavía pueden dejar las dos
primarias en un mismo nodo; consulta
[Dónde se ejecutan las primarias de las bases de datos](./bootstrap.md#ha-database-primaries). Una
cuota que limita la ubicación entre espacios de nombres puede rechazar estos pods: consulta
[el paso 5](#v0190-quota).

###### Las bases de datos pueden ejecutarse en los nodos que elijas {#v0190-database-placement}

`dcctl install` admite `--database-node-selector` y `--database-toleration`. Colocan el almacén
relacional compartido y el almacén de eventos de cada instancia en nodos con una etiqueta, incluidos
nodos con un taint que mantiene fuera otras cargas. `dcctl bootstrap` no tiene esos flags: cada
instancia sigue a la instalación. Antes de instalar nada, install rechaza una ubicación con menos
nodos utilizables que instancias tiene una base de datos, y cada bootstrap vuelve a comprobarlo.
NATS, los servicios y el almacén de objetos de respaldo no se colocan. Añadir una ubicación a un
clúster que ya ejecuta instancias se rechaza; sin ninguna en marcha, mueve el almacén relacional,
cuyos volúmenes quizá no puedan seguirlo. Consulta
[Ubicación de las bases de datos](./bootstrap.md#database-placement).

##### Respaldos {#v0190-backups}

###### `dcctl destroy` elimina los respaldos internos de una instancia {#v0190-destroy-backups}

Cuando el espacio de nombres de la instancia ya no existe, destroy borra todo lo que hay bajo la
ruta a la que archivaba su almacén de eventos en el almacén de objetos propio del clúster, y
comprueba que la ruta ha quedado vacía. Lee y muestra esa ruta antes de cambiar nada. Los respaldos
en un almacén de objetos que tú proporcionaste no se borran nunca: destroy indica dónde están. Pasa
`--keep-backups` para conservar también los respaldos internos, y pásalo sin falta antes de
reconstruir una instancia a partir de sus propios respaldos con `--restore-tsdb-from`, porque un
destroy sin esa opción borra el archivo que lee esa restauración. Si no se puede acceder al almacén,
destroy termina igualmente e indica qué dejó. Un archivo que destroy no escribió en esta ejecución
(uno que dejó una versión anterior, o uno conservado con `--keep-backups` y del que después se
restauró) se lista, no se elimina;
[Qué pasa con los respaldos de la instancia](./bootstrap.md#destroy-backups) explica cómo
eliminarlo. Destroy ya no muestra la lista **Changes to Outputs** de OpenTofu, cuyos valores eran
los predeterminados de la configuración y no los de la instancia.

###### Los respaldos que dejan de enviarse avisan antes {#v0190-backup-alerts}

`PostgresWALArchiveBacklog` se dispara cuando una base de datos retiene registro de escritura
anticipada sin enviar, también cuando el archivador es lento o está bloqueado.
`BackupDestinationFillingFast` y `DatabaseVolumeFillingFast` se disparan según lo rápido que se
llena el almacén de respaldos o un volumen del almacén de eventos. Consulta
[Respaldos que dejan de enviarse](./observability.md#backup-archiving).

###### El almacén de respaldos se dimensiona para que el almacén de eventos se llene primero {#v0190-backup-store-size}

El almacén de respaldos interno de un clúster nuevo tiene **160 GiB** en lugar de 20 GiB, y
**20 GiB** en lugar de 8 GiB con `--compact` cuando se mantiene TLS. Con 20 GiB, una ingesta
sostenida llenaba el almacén tras entre 12 y 16 millones de eventos, mucho antes que un almacén de
eventos de 32 GiB; entonces el archivado se detenía y el primario del almacén de eventos llenaba su
propio volumen. El dimensionamiento supone hasta unos 1,9 KB de archivo por evento para las dos
bases de datos, medidos antes de que esta versión comprimiera el registro de escritura anticipada,
cambiara las claves del almacén de eventos y archivara con zstd, así que peca por exceso: en la
prueba de rendimiento de la versión, el archivo del almacén de eventos ocupó por sí solo unos
0,47 KB por evento almacenado. **Un clúster existente conserva el tamaño de su almacén**: el volumen
se dimensiona solo al crearlo, y `backup_object_store_storage` en un almacén existente no hace nada.
Para dar el nuevo tamaño a un clúster existente, amplía el volumen en una StorageClass que lo
permita; consulta [Tamaño del almacén de objetos de respaldo](./bootstrap.md#backup-store-size).

###### Cada base de datos conserva su propia ventana de recuperación: 30 días para los datos de núcleo, 7 para los de eventos {#v0190-backup-retention}

`backup_retention` se sustituye por `backup_retention_rdb` en la configuración del clúster (por
defecto `30d`) y `backup_retention_tsdb` en la de la instancia (por defecto `7d`). La base de datos
relacional, que guarda inquilinos, usuarios, dispositivos, reglas, secretos y el último estado
conocido, se puede recuperar ahora a cualquier punto de los últimos 30 días en lugar de 7, y los
datos de núcleo de un inquilino eliminado siguen siendo restaurables durante 30 días
([Qué se conserva deliberadamente](./tenant-deletion.md#retained)). Una ventana es un número entero
y `d`, `w` o `m`; cualquier otra forma se rechaza antes de la aplicación. Cuando una ventana de
retención limita el almacén de eventos, el ritmo de ingesta sostenido que llena un almacén de 20 GiB
baja de unos 19 a unos 13 eventos por segundo, y el del almacén de 160 GiB de unos 150 a unos 100.
Esos ritmos salen del coste por evento medido antes de la compresión del registro, las claves nuevas
y zstd de esta versión, así que en ese aspecto pecan por defecto. También usan la parte del registro
de la base de datos relacional medida una sola vez, aproximadamente un 14 % para una flota pequeña
que informa con frecuencia; para flotas más grandes o más lentas probablemente es mayor, y si el
registro relacional fuera todo, la cifra de 160 GiB sería de unos 35. Para conservar 7 días,
establece `backup_retention_rdb = "7d"` si aplicas tú mismo la configuración de OpenTofu;
`dcctl install` no tiene ninguna opción para ello. Consulta
[Ventanas de recuperación](./bootstrap.md#backup-retention).

###### Los respaldos base de las bases de datos pueden ser instantáneas de volumen {#v0190-snapshot-backups}

En un clúster cuyo controlador de almacenamiento toma instantáneas de volumen CSI, `dcctl install
--backup-snapshot-class <class>` toma el respaldo base diario de cada base de datos como una
instantánea de volumen en lugar de una copia completa en el almacén de respaldos. No cambia nada si
no pasas la opción. Consulta
[Respaldos base como instantáneas de volumen](./bootstrap.md#snapshot-base-backups).

- El archivado del registro no cambia, y un respaldo base completo sigue yendo al almacén de
  respaldos cada domingo a las 04:00. Toda restauración lee el almacén, no las instantáneas, así que
  puede reproducir hasta una semana de registro.
- Las instantáneas se guardan en tu proveedor de nube y sobreviven al clúster.
  [Respaldos base como instantáneas de volumen](./bootstrap.md#snapshot-base-backups) explica qué
  comprobar antes y después de borrar una.
- La clase tiene que existir, tener `deletionPolicy: Delete` y pertenecer al controlador que
  aprovisiona los volúmenes de las bases de datos; install y cada bootstrap comprueban las tres
  cosas primero. GKE y AKS incluyen un controlador de instantáneas; en EKS, instala antes el
  complemento del controlador de instantáneas.
- El operador de DeviceChain poda las instantáneas antiguas cada diez minutos, y conserva todas las
  que están dentro de la ventana de recuperación y la más reciente anterior a ella. Su ClusterRole
  gana, en todos los espacios de nombres: `get` sobre namespaces; `get`, `list` y `delete` sobre los
  Backups de CloudNativePG; `get`, `list` y `patch` sobre los ScheduledBackups de CloudNativePG; y
  `create` y `patch` sobre los Events de `events.k8s.io`. Solo actúa en espacios de nombres que creó
  DeviceChain, sobre las programaciones que genera su propia configuración, y registra cada pasada
  en la anotación `devicechain.io/snapshot-retention-checked-at`.
- El almacén de respaldos guarda hasta una semana más de registro de cada base de datos, así que se
  llena antes: con las ventanas y el almacén predeterminados, a unos 60 eventos por segundo de
  ingesta sostenida en lugar de unos 100 (de la misma medición, anterior a la compresión, así que
  conservadora).
- `PostgresNoRecentSnapshotBackup`, `DatabaseSnapshotPruningStalled` y
  `DatabaseSnapshotBackupsUnobserved` son nuevas, y en un clúster así `PostgresNoRecentBaseBackup`
  espera 8,5 días en lugar de 36 horas.
- El ajuste pertenece al clúster; cambiarlo se rechaza mientras haya instancias en él.

###### El registro de escritura anticipada se comprime, y se archiva con zstd {#v0190-wal}

Los almacenes de eventos nuevos y los actualizados comprimen las imágenes de página de su registro
de escritura anticipada (`wal_compression = lz4`), algo que se recarga sin reinicio. Las dos bases de
datos archivan ahora su registro con zstd en lugar de gzip; los respaldos base siguen en gzip. Las
restauraciones y la caducidad de los respaldos leen ambos formatos, según el nombre de cada segmento
(`.gz` o `.zst`), así que un archivo que cambia de compresión a mitad se restaura como antes; la
prueba de actualización de la versión restauró un archivo así y recuperó todos los eventos. En la
prueba de rendimiento de la versión a 3000 eventos por segundo, el cambio de claves de esta versión
y el archivado con zstd redujeron juntos un 22% el registro por evento almacenado del almacén de
eventos y un 53% la CPU de su archivador, frente a la compilación de desarrollo anterior a ellos.
Consulta [Volumen del almacén de eventos](./bootstrap.md#event-store-volume).

##### Operaciones {#v0190-operations}

###### `dcctl upgrade` aplica los ajustes del bróker de mensajería y del almacén de eventos de una instancia {#v0190-upgrade-infrastructure}

`dcctl upgrade` aplica ahora los servidores NATS y el almacén de eventos de la instancia desde la
configuración de OpenTofu que trae esta versión, antes de mover los servicios. Hasta ahora solo los
aplicaba `dcctl bootstrap`, así que una instancia actualizada conservaba sus ajustes antiguos del
bróker y del almacén de eventos sin decirlo. Esta actualización da a una instancia creada con
`v0.18.0` las solicitudes y el límite de NATS, el registro de escritura anticipada comprimido, el
archivado con zstd y la preferencia de ubicación de las primarias; en una instancia así, las
instancias del almacén de eventos pueden reiniciarse una vez por la preferencia de ubicación. Los
tamaños de los volúmenes se conservan. Consulta
[Qué aplica una actualización a la infraestructura](#upgrade-infrastructure). La configuración de
OpenTofu de la instancia declara ahora que necesita OpenTofu o Terraform 1.9 o posterior.

###### Volver a ejecutar `dcctl bootstrap` termina una instancia cuyo primer arranque inicial falló al final {#v0190-bootstrap-resume}

Un arranque inicial que fallaba después de escribir el documento de configuración de la
instancia (mientras instalaba el chart, o mientras esperaba a que los servicios estuvieran
listos) dejaba una instancia sobre la que `dcctl bootstrap` se negaba a volver a ejecutarse,
como si ya estuviera en marcha.

Ahora, cuando `dcctl bootstrap` declara una instancia que está construyendo, deja anotado en la
declaración que el primer arranque inicial no ha terminado (la anotación
`core.devicechain.io/bootstrap-unfinished`). Un arranque inicial o una actualización que termina
con éxito la retira, en la misma escritura que registra la instancia como `Ready`. Hasta
entonces, volver a ejecutar el mismo `dcctl bootstrap` termina la instancia. Reutiliza la clave
raíz, las credenciales del bróker y las contraseñas de las bases de datos que dejó en el clúster
la ejecución anterior, y muestra la contraseña generada del superusuario, a la que la ejecución
fallida nunca llegó. Una vez retirada la anotación, una nueva ejecución se rechaza como antes.

- Si una ejecución termina con éxito pero no puede retirar la anotación (la escritura se rechaza,
  o el bloqueo del clúster se reclamó antes), sale con un error que indica que vuelvas a ejecutar
  el mismo comando, en lugar de informar de un éxito sobre una instancia sobre la que un arranque
  inicial posterior se ejecutaría.
- Una instancia cuyo primer arranque inicial empezó con una versión anterior no tiene esa
  anotación, así que se trata como en marcha, igual que antes; la negativa nombra `dcctl upgrade` y
  `dcctl destroy`. Consulta
  [Terminar un bootstrap que falló a mitad de camino](./disaster-recovery.md#resuming-a-bootstrap).
- Actualiza todas las copias de `dcctl` que uses antes de ejecutarlas sobre una instancia
  construida con esta versión. Un `dcctl` anterior no retira la anotación, así que una
  actualización posterior con él que falle puede dejarla en vigor sobre una instancia en marcha,
  y un `dcctl bootstrap` sin más se ejecutaría entonces sobre esa instancia.

###### La detección reintenta liberar su partición cuando el bróker no responde {#v0190-detect-lease-release}

Cuando el pod del motor de detección se detiene, libera su partición para que el siguiente pod
empiece a detectar enseguida. Hasta ahora, si el bróker no respondía a esa liberación, por ejemplo
porque un servidor del bróker se estaba reiniciando en ese momento, el pod se rendía tras un
intento. El siguiente pod esperaba entonces a que la partición expirara, hasta 30 segundos, y un
periodo de traspaso adicional de 20 segundos antes de detectar nada. Ahora el pod reintenta la
liberación hasta que el bróker responde, mientras se lo permita su tiempo de apagado, y reserva lo
suficiente para un tiempo de espera más del bróker y para terminar de detenerse. Una renovación o
una liberación cuya respuesta se perdió mientras un servidor del bróker se reiniciaba tampoco le
cuesta ya la partición al motor. El reintento lo hace el pod que se detiene, así que tiene efecto a
partir de la actualización siguiente a la que instala esta versión: durante esa actualización, el
pod que se sustituye todavía ejecuta la versión anterior y hace un solo intento.

Lo que todavía pausa la detección:

- **Sin `--ha`, una actualización que reinicia el bróker detiene la detección durante todo el
  reinicio**, normalmente alrededor de un minuto (consulta [la entrada del bróker y del almacén de
  eventos](#v0190-upgrade-infrastructure)). El motor no puede conservar su partición durante una
  interrupción del bróker de más de 30 segundos, y nada registra que se detuvo limpiamente, así
  que, cuando el bróker vuelve, espera el periodo de traspaso de 20 segundos, más hasta 5 segundos
  antes de reintentar, y después reproduce el flujo desde su último punto de control, como tras
  cualquier reinicio. El despliegue gradual de los servicios que sigue mueve luego la partición al
  pod nuevo, que reproduce el flujo una vez más pero no espera.
- **Un pod que se detiene mientras el bróker sigue inaccesible más allá de su apagado** todavía deja
  que su partición expire, y el siguiente pod espera hasta 30 segundos más el periodo de traspaso.

Con `--ha` el bróker sigue disponible mientras sus servidores se reinician de uno en uno. El motor
solo pierde su partición si sus renovaciones fallan durante 30 segundos.

###### Alertas {#v0190-alerts}

- **Nuevas:** `JetStreamUnreadBacklogNearFull` (warning) y `JetStreamIngestBackpressureEngaged`
  (critical) para la [contrapresión](#v0190-backpressure), que también se dispara por
  [el rechazo por historial](#v0190-ingest-history-runway); `JetStreamDurableUnreadNearFull`
  (warning), cuando un consumidor lleva 5 minutos sin haber leído más del 80% de lo que cabe en su
  stream; las tres [alertas de respaldo](#v0190-backup-alerts); las tres
  [alertas de instantáneas](#v0190-snapshot-backups); `ExternalMqttSourceNotReadByOnePod`
  (warning) para [una fuente MQTT sobre tu propio bróker](#v0190-external-mqtt-client-id);
  `BrokerConnectionDiedSilently` (warning) para
  [una conexión con el bróker que murió sin cerrarse](#v0190-broker-liveness); y
  `InstanceContainerRestarted` (warning) para
  [un contenedor que Kubernetes reinició](#v0190-read-loop-restarts).
- **Cambia:** `JetStreamStreamNearFull` pasa a `info`, solo se dispara para streams que guardan
  registros para un operador (`failed-decode`, `failed-events`, `connector-dispatch.dead`,
  `max-deliveries`, y `dead-letters` mientras `user-management` no informe de que lo lee), y tiene en
  cuenta el límite de mensajes de un stream además del de bytes. La configuración predeterminada de
  Alertmanager de kube-prometheus-stack no entrega las alertas `info`. Consulta
  [Mensajes que un consumidor nunca leyó](./observability.md#unread-loss).
- **Series nuevas:** `devicechain_<area>_jetstream_consumer_unread_ratio{stream, durable}` y
  `devicechain_<area>_jetstream_stream_sink{stream}`, más las que nombran los apartados de arriba,
  entre ellas, con sus nombres completos:
  - `devicechain_<area>_jetstream_backpressure_history_runway_seconds{stream, durable}`
    ([el rechazo por historial](#v0190-ingest-history-runway));
  - `devicechain_eventsources_total_msg_reading_limited` y
    `devicechain_eventsources_total_readings_rate_limited`
    ([Lecturas, no mensajes](#v0190-ingest-readings));
  - `devicechain_sparkplugingest_samples_too_old_dropped_total`,
    `devicechain_lwm2mingest_telemetry_too_old_dropped_total` y
    `devicechain_devicemanagement_resolve_event_time_too_old_total`
    ([Lecturas antiguas](#v0190-event-age-limit));
  - `devicechain_eventsources_external_mqtt_owner{source}` y
    `devicechain_eventsources_total_msg_not_owner{source}`
    ([Identificador de cliente MQTT](#v0190-external-mqtt-client-id));
  - `devicechain_<area>_nats_connection_dead_total{detected_by}` y
    `devicechain_<area>_nats_connection_dead_last_timestamp_seconds`
    ([Un servidor del bróker que dejó de responder](#v0190-broker-liveness)).

  **Se elimina:** `devicechain_lwm2mingest_notify_samples_truncated_total`.

###### Cualquier servicio puede servir perfiles del runtime de Go, desactivado por defecto {#v0190-profiling}

`functionalAreas.<service>.profiler.enabled: true` sirve perfiles de CPU, memoria, asignaciones,
goroutines y traza de ejecución de ese servicio, en un listener propio en la dirección de loopback
del pod, al que se accede con `kubectl port-forward`. Nunca es un puerto del contenedor, un puerto
del Service ni una ruta del ingress. Solo se reinician los pods de ese servicio. Consulta
[Perfilar un servicio](./observability.md#profiling).

###### Una configuración de Google Kubernetes Engine (nueva) {#v0190-gke}

`deploy/gke` es nuevo: una configuración de OpenTofu que crea un clúster de GKE para DeviceChain,
con un grupo `database` con taint de tres nodos de 4 vCPU y 16 GB y un grupo `services` de tres
nodos de 4 vCPU y 8 GB, todos arrancando desde un disco persistente estándar. Instala con
`--database-node-selector` y `--database-toleration` para colocar las bases de datos en el grupo
`database`. Una instalación `--ha` predeterminada con una instancia cabe en la cuota de SSD de un
proyecto nuevo de Google Cloud; para más instancias, o para un clúster creado de otra forma en
cualquier nube, comprueba antes la cuota de disco: [Prerrequisitos](./bootstrap.md#prerequisites)
indica los tamaños de los volúmenes y la guía de `deploy/gke`, la cuota que hay que solicitar.

###### Los informes de las pruebas de carga cuentan los eventos rechazados {#v0190-loadtest-refusals}

El simulador y el arnés de las pruebas de carga cuentan ahora por separado las dos formas en que la
ingesta rechaza un evento:

- `shed` es un `429`, rechazado en el techo de ingesta del inquilino.
- `backpressured` es un `503` con `Retry-After`, rechazado porque la plataforma va retrasada. Este
  rechazo afecta a todos los inquilinos.

Un `503` sin `Retry-After` se sigue contando como `failed`. Dónde aparecen los recuentos:

- El `GET /status` del simulador muestra `emitted`, `shed`, `backpressured` y `failed`.
- La sección `drive` de los informes L1, de monitorización, de detección, de comandos, de
  presencia y de lotes muestra `accepted`, `shed`, `backpressured` y `failed`.
- El informe de contención muestra los cuatro para cada inquilino de prueba.
- El informe de autocomprobación sigue mostrando solo `accepted`.

Qué cambia:

- Antes, la mayoría de los informes de las pruebas de carga mostraban siempre `shed 0`, por muchos
  eventos que rechazara la ingesta. Ahora muestran el recuento real.
- El `stats.shed` del simulador cuenta ahora solo los `429`. Antes incluía los `503`.
- La prueba de contención falla una ejecución en la que la contrapresión rechazó al inquilino que
  nunca se descarta, o a cualquiera de los dos inquilinos cuando no hay un suelo de contención.
  Esa ejecución no dice nada sobre la prioridad de descarte. Antes, la prueba la informaba como un
  descarte del inquilino que nunca se descarta. Con un suelo, también podía aprobar una ejecución
  en la que el suelo no rechazó nada.

No hay nada que hacer al actualizar.

###### Los servicios detectan en 40 segundos un servidor del bróker que dejó de responder {#v0190-broker-liveness}

Cuando la máquina de un servidor del bróker se detenía de golpe (un nodo reiniciado o que perdió la
alimentación), los servicios conectados a ese servidor podían tardar hasta seis minutos en darse
cuenta, porque nada cerraba sus conexiones. Si uno de ellos era `event-sources`, la ingesta HTTP se
detenía durante ese tiempo, y si la máquina volvía pronto Kubernetes no registraba nada. En las
pruebas, un reinicio forzado del nodo que tenía la conexión de `event-sources` con el bróker detuvo
la ingesta durante unos cinco minutos y medio.

- Ahora cada servicio envía un ping a su servidor del bróker cada 10 segundos y da la conexión por
  perdida tras tres intervalos sin respuesta, o cuando una escritura lleva 10 segundos sin avanzar,
  así que una conexión muerta se detecta en 40 segundos como mucho; después el servicio se reconecta
  a un servidor que responde. La segunda conexión con el bróker que mantiene `event-sources`, para
  la presencia de los dispositivos, tiene el mismo límite.
- Nuevo contador `devicechain_<area>_nats_connection_dead_total{detected_by}` y nuevo indicador
  `devicechain_<area>_nats_connection_dead_last_timestamp_seconds`, y una nueva alerta,
  `BrokerConnectionDiedSilently` (warning). Consulta
  [Una conexión con el broker que murió en silencio](./observability.md#broker-connection-dead).
- El procedimiento de `JetStreamIngestBackpressureEngaged` cubre ahora un servicio que escribe y no
  llega al bróker, y la descripción de la [pérdida de un nodo](./bootstrap.md#ha-node-loss) indica
  cuánto tarda en reconectarse un servicio conectado al servidor perdido.

No hay nada que hacer al actualizar.

###### Una perturbación del bróker minutos después de otra ya no reinicia servicios, y un reinicio dispara una alerta {#v0190-read-loop-restarts}

Un servicio termina por sí mismo, para que Kubernetes lo reinicie, cuando uno de sus bucles de
lectura de mensajes sigue fallando (consulta
[Los bucles de lectura que siguen fallando ahora reinician su servicio](#v0180-read-loops)). Un
bucle sobre un flujo tranquilo, que puede pasar mucho tiempo sin recibir un mensaje, contaba esos
dos minutos desde su primer fallo y solo volvía a empezar cuando llegaba un mensaje. Dos
perturbaciones del bróker separadas por minutos u horas, como el reinicio de un servidor del bróker
y un cambio posterior del líder de un consumidor, contaban por tanto como un único fallo de más de
dos minutos, y el servicio terminaba en la segunda. Detener a la vez dos de los tres servidores del
bróker reiniciaba así varios servicios juntos, entre ellos `event-sources`, lo que alargaba el
corte de la ingesta.

- Ahora un bucle vuelve a empezar la cuenta siempre que el bróker le haya respondido desde su
  último fallo: una entrega, la comprobación que hace un bucle tranquilo cada pocos segundos de que
  su consumidor sigue existiendo, una nueva vinculación a su consumidor o una reconexión. Un bucle
  cuyas lecturas siguen fallando durante dos minutos sin ninguna respuesta del bróker entre medias
  sigue terminando el proceso, como antes.
- Una caída del bróker de más de dos minutos ya no reinicia todos los servicios en el momento en
  que el bróker vuelve. La reconexión cuenta como respuesta del bróker.
- Una lectura vacía no cuenta como respuesta, porque el cliente informa de una incluso mientras
  está desconectado. Un segundo fallo pocos segundos después de que vuelva el bróker todavía puede
  contarse junto con el primero.
- Un fallo que se repite mientras el bróker responde entre medias, como un líder de consumidor que
  cambia cada minuto, se reintenta y se registra cada vez, pero ya no reinicia el servicio.
- Nueva alerta: `InstanceContainerRestarted` (aviso), por cada contenedor del namespace de la
  instancia que Kubernetes haya reiniciado en los últimos 15 minutos. Hasta ahora nada informaba de
  un reinicio aislado: `KubePodCrashLooping`, de la pila de monitoreo, solo se dispara para un pod
  que sigue reiniciándose a los 15 minutos. La alerta lee kube-state-metrics, que instala la pila
  de monitoreo incluida. Consulta [Un contenedor que se reinició](./observability.md#container-restarts).

No hay nada que hacer al actualizar.

###### Los informes de las pruebas de carga comprueban cada evento, no solo los totales {#v0190-loadtest-identity}

Las pruebas de carga L1 y de contención comprueban ahora cada evento que enviaron, no solo que el
número de eventos almacenados sea igual al de aceptados. Unos totales iguales pueden ocultar un
evento perdido compensado por un duplicado o por un evento que no debería estar.

Cada evento se identifica por su dispositivo y por el `occurredTime` con el que lo envió el
simulador, que ahora lo escribe al microsegundo, la precisión que guarda el almacén de eventos. Tras
el intervalo de asentamiento de la ejecución, el arnés vuelve a leer los eventos almacenados de cada
dispositivo con la consulta `events` del inquilino y comprueba que:

- cada evento aceptado está almacenado exactamente una vez;
- ningún evento rechazado está almacenado: un `429`, un `503` con `Retry-After` u otro `4xx`;
- un evento de resultado desconocido está almacenado como mucho una vez: un tiempo de espera
  agotado o una conexión cortada, un `503` sin `Retry-After` u otro `5xx`;
- no hay nada más almacenado en la ventana de la ejecución.

Dónde aparece:

- El informe L1 tiene una sección `identity` con `missing`, `duplicateKeys`, `unexpected`,
  `refusedStored`, `ambiguousStored` y `ambiguousAbsent`, hasta diez eventos de ejemplo de cada
  uno, cuánto tiempo después de la ejecución miró la comprobación por última vez
  (`observedUntilAfterDriveSeconds`) y un veredicto `reconciled`. Una nueva comprobación,
  `ingest-identity`, falla la ejecución cuando alguno de los tres primeros no es cero, o cuando las
  pruebas no permiten decidir, por ejemplo porque seguían llegando eventos mientras se leían. Una
  petición de relectura que falla se reintenta dos veces, con dos segundos entre intentos; si sigue
  fallando, `ingest-identity` falla como no concluyente con el error anotado en el informe, y el
  informe se escribe igualmente. Un informe sin esta sección ya no se aprueba.
- El informe de contención tiene la misma sección para cada inquilino de prueba, comprobada como
  `gold-identity` y `shed-identity`.
- La autocomprobación ahora también elimina un evento almacenado e inserta un duplicado de otro, de
  modo que los totales vuelven a coincidir, y solo se aprueba si la comprobación de los totales
  sigue aprobándose y la comprobación de identidad nombra los dos eventos.
- La comprobación `ingest-completeness` sigue comparando los totales, y su mensaje dice ahora que es
  lo único que compara.

Una copia reentregada de un evento se almacena al menos un minuto después de la primera: el broker
reenvía un evento 60 segundos después de una entrega cuyo acuse de recibo se perdió, la copia aún
tiene que procesarse, y una copia cuyo propio acuse se pierde se reenvía de nuevo. Un minuto es, por
tanto, lo mínimo que puede tardar una copia, no lo máximo. Una ejecución cuyo resultado se vaya a
publicar debe asentarse con margen por encima de ese tiempo, por ejemplo `--quiesce-settle 90s`.

El simulador no envía `altId`. Un `altId` haría que el almacén de eventos descartara una segunda
copia antes de almacenarla, lo que ocultaría los duplicados que busca esta comprobación y cambiaría
el trabajo que se mide.

No hay nada que hacer al actualizar.

#### Rendimiento {#v0190-performance}

En Google Kubernetes Engine, en tres nodos de base de datos de 4 vCPU y 16 GB y tres nodos de
servicios de 4 vCPU y 8 GB, una instalación HA predeterminada aceptó 6000 eventos por segundo
durante 10 minutos, dos veces, y almacenó exactamente una vez cada evento aceptado. Se comprobó evento por evento: ninguno faltante, ninguno
almacenado dos veces, ninguno inesperado.

La resolución, el almacenamiento y el estado en vivo de los dispositivos siguieron cada uno el ritmo
durante esos 10 minutos, y la cola se había vaciado en los 3 segundos siguientes al fin de la
carga. La detección, que esa comprobación no cubre, siguió el ritmo a 6000 (cola máxima por debajo
de 1000) y se quedó atrás a partir de 7600 ofrecidos. No se afirma ningún ritmo sostenido por encima
de 6000. Consulta [Rendimiento medido](./bootstrap.md#measured-throughput).

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
