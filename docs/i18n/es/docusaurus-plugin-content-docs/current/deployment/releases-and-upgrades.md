---
sidebar_position: 4
title: Versiones y actualizaciones
---

# Versiones y actualizaciones

DeviceChain se distribuye como un conjunto de imágenes de contenedor precompiladas y versionadas,
más un chart de Helm. **No** necesita compilar nada para ejecutarlo: descargue una versión publicada,
instale el chart y actualice in situ con una actualización progresiva.

:::warning Hay versiones a las que no se puede actualizar

- Una instancia creada por la **`v0.16.0` o cualquier versión anterior** no puede actualizarse a una posterior, porque no registró ninguna declaración de lo que es la instancia. Consulte [Actualizar desde una versión anterior](#pre-declaration-recreate).
- La **`v0.19.0`, para una instancia cuyo almacén de eventos guarda más de 4 000 000 de filas aún sin comprimir**, o una tabla de más de 500 fragmentos cuyos eventos más antiguos no se pueden eliminar: su primer arranque reconstruye las claves del almacén de eventos sobre esas filas y se niega por encima de esos límites. Si actualiza desde la `v0.17.x` o la `v0.18.x`, lea antes [sus notas](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0190-upgrade).

Si está en cualquiera de ellos, lea la sección correspondiente antes de hacer nada más.
:::

:::caution Actualice pasando por la v0.23.0
Las versiones posteriores a `v0.23.0` escriben los eventos en una forma que `v0.22.0` y anteriores
no pueden leer. Si viene de `v0.22.0` o anterior, actualice primero cada instancia a `v0.23.0`, y
solo después a una versión posterior. Consulte
[El formato de los eventos en el bróker](#event-wire-format).
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
([Comprueba el número de filas](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0190-row-count)). Consulte las notas de la
versión a la que va a migrar antes de ejecutarlo:

```bash
dcctl install local --version <new-version>
dcctl upgrade local devicechain --version <new-version>
```

:::note Qué se ha medido
La actualización se ha ensayado a mano en un clúster local: una instancia instalada con `--ha` se
actualizó in situ con tráfico MQTT en marcha (cuatro dispositivos publicando con QoS 1, 20 mensajes por
segundo entre todos, con `infrastructure.nats.mqttFailClosed` desactivado, como lo está por defecto), su
broker se reinició dos veces y los dispositivos se reconectaron tras cada reinicio. En un ensayo, todos los
mensajes que el broker había confirmado seguían almacenados después (11 009 de 11 009). En otro se perdieron
11 de 11 262 mensajes confirmados, todos mientras se reiniciaba un servidor del broker y el stream de captura
MQTT cambiaba de líder. Con `mqttFailClosed` desactivado, espere una breve ventana de pérdida como esta en
cada reinicio del broker, con cualquier volumen de tráfico; vea
[MQTT: el bróker de la plataforma](../reference/transport-matrix.md#mqtt--the-platform-broker). Los
ensayos son manuales. La comprobación automática de actualización que condiciona cada versión se ejecuta
sin tráfico.

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

:::warning Actualice la plataforma antes que los agentes de borde
Un agente de borde en el [enlace ascendente de pasarela](./edge-services.md#gateway-identity) publica en el espacio de nombres de pasarela. Una plataforma cuyo `event-sources` no conoce ese espacio de nombres, con `infrastructure.nats.mqttFailClosed` activado, confirma esos mensajes en el broker y los descarta, y nada en el agente lo muestra. Termine de actualizar el clúster y cada instancia (todas las réplicas de `event-sources` y de `device-management`, y las concesiones del broker) antes de reiniciar cualquier agente en una versión que lo use. Un agente cuyo broker no se ha actualizado no puede suscribirse a sus avisos y no informa de su enlace ascendente como conectado.
:::

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

La confirmación MQTT a prueba de fallos se conserva del mismo modo: `--mqtt-fail-closed` en `dcctl bootstrap` o `dcctl upgrade` se registra en la instancia, y una actualización que omite el indicador conserva lo registrado, activado o desactivado. Consulta [MQTT con fail-closed](./observability.md#mqtt-fail-closed).

### Las credenciales aprovisionadas se conservan {#upgrade-provisioned-credentials}

Las credenciales que diste a `dcctl` con un archivo, las [claves precompartidas de LwM2M](./edge-services.md#lwm2m-identities) y las [contraseñas del broker de Sparkplug](./edge-services.md#sparkplug-sources), se guardan en Secrets que `dcctl` escribe él mismo, fuera de la versión de Helm. Una actualización no los toca y no lee nada secreto de la versión. La primera actualización de una instancia aprovisionada con claves LwM2M por una versión anterior las traslada a su propio Secret antes de que cambie la versión; las revisiones anteriores del historial de Helm siguen teniendo los valores antiguos, así que rota las claves después si eso te importa.

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

#### Se reparte el liderazgo del broker {#upgrade-broker-leadership}

Reiniciar de forma gradual los servidores del broker deja al reiniciado en último lugar sin liderar ningún stream
ni consumidor, mientras los demás lideran todo; con una carga medida, eso casi duplicó la latencia de confirmación
de publicación. Cuando los servicios ya se han reiniciado, una actualización que tocó el broker pide al líder de cada
stream y consumidor sobrecargado que ceda el liderazgo a una réplica al día, e imprime cuántos lidera cada servidor
antes y después. Los movimientos se hacen de uno en uno, y cada uno espera a que el nuevo líder esté al día antes del siguiente. El paso está acotado y es de mejor esfuerzo: si no puede terminar, avisa y la actualización sigue
siendo correcta.
Los streams con datos de dispositivo ya confirmados al dispositivo no se reequilibran deliberadamente nunca: los streams MQTT del broker, el stream de captura de dispositivos y todo stream en el que escribe un servicio de ingesta. Un cambio de líder en ellos ha perdido mensajes que se habían dicho seguros a los dispositivos, así que ninguna ganancia de latencia lo compensa y no hay opción para incluirlos. Su desequilibrio se resuelve en el siguiente reinicio del broker, o por sí solo. `dcctl broker rebalance <instance>` ejecuta el mismo paso a demanda, para un broker reiniciado de otro modo.

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

## Actualizar desde una versión anterior {#pre-declaration-recreate}

<a id="v090-baseline-squash"></a>
<a id="v0100-event-key"></a>

Una instancia inicializada con la **`v0.16.0`, o con cualquier versión anterior, no puede actualizarse a una versión posterior**. `dcctl upgrade` se niega en lugar de intentarlo, y esa negativa no cambia nada: la release de Helm, el operador y todas las filas quedan como estaban. Lo mismo vale para las dos versiones anteriores a las que no se podía llegar in situ, la `v0.9.0` (la cadena de migraciones de cada servicio sustituida por una única línea base congelada) y la `v0.10.0` (una clave primaria nueva en las tablas de eventos): una instancia tan antigua como para necesitar cualquiera de las dos queda por debajo de este límite.

`dcctl bootstrap` registra una **declaración**, un objeto de alcance de clúster que dice qué es la instancia: su perfil, su topología, cómo se expone y qué áreas funcionales ejecuta. `dcctl upgrade` la lee para saber qué desplegar. Las versiones hasta la `v0.16.0` incluida no escribían ninguna, así que no hay nada que leer, y la negativa lo dice:

```
instance "devicechain" IS in this cluster — named by the DeviceChain Helm releases in this
cluster — and it carries no declaration, so it was built by a release older than the one
that began recording them.
```

No hay ninguna capa de compatibilidad, y antes de la `v1.0.0` no la habrá: una declaración inventada a posteriori sería una conjetura aplicada sobre una instancia en funcionamiento. Si una versión posterior sube este límite, sus notas lo indican.

**Para pasar a una versión posterior, recree la instancia y el clúster que la contiene:**

```bash
# Exporte antes lo que necesite: esto descarta las bases de datos.
dcctl destroy local devicechain --without-state   # elimina la instancia; el clúster antiguo va después
kind delete cluster --name devicechain     # y el clúster que preparó la versión anterior
dcctl install local                        # prepara un clúster nuevo
dcctl bootstrap local devicechain
```

Para un clúster al que se accede con `--kube-context`, que `dcctl` nunca elimina, pase ese `--kube-context` también a `destroy`, recree el clúster con lo que lo creó y páselo además a `install` y `bootstrap`. `dcctl upgrade` imprime esta receta cuando se niega.

Ahora `dcctl` guarda su estado local en `~/.devicechain/instances/<instance>/`. Las versiones anteriores lo guardaban en `~/.devicechain/<instance>/`, que el nuevo `dcctl` no lee, no lista ni elimina, de modo que `destroy` deduce el clúster a partir del nombre de la instancia y necesita `--without-state`. Cuando la instancia ya no exista, elimine usted mismo ese directorio antiguo (`rm -rf ~/.devicechain/<instance>`; contiene el estado de la infraestructura en texto claro) y no toque `~/.devicechain/escrow/`: allí vive el artefacto de custodia de la clave raíz, que sigue abriendo las copias de seguridad de la base de datos de esa instancia ([Recuperación ante desastres](./disaster-recovery.md#after-destroy)).

:::caution Exporte primero: recrear descarta sus datos
La [protección de destrucción](#data-durability) protege las bases de datos frente a una
operación corriente de `helm`, no frente a un `dcctl destroy` deliberado. Si la instancia
contiene telemetría, definiciones de dispositivos o paneles que le importan, vuélquelos antes
de empezar.
:::

Cuando esté en una versión que registra una declaración, las actualizaciones normales in situ se reanudan.

## Versiones recientes {#recent-releases}

Las tres versiones menores más recientes se documentan por completo, de la más nueva a la más antigua.

### v0.26.0 — los dispositivos se registran solos y el front end pasa a Traefik {#v0260-upgrade}

`v0.26.0` es una actualización in situ desde `v0.25.2`: `dcctl install` para el clúster y después un `dcctl upgrade` por cada instancia, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además del nuevo `--version`. Si vienes de `v0.25.1` o `v0.25.0`, lee también las notas de las versiones intermedias.

```bash
dcctl install local --version v0.26.0
dcctl upgrade local devicechain --version v0.26.0 --dry-run
dcctl upgrade local devicechain --version v0.26.0
```

En un clúster con dos instancias del perfil default, añade `--max-connections 700` a este `dcctl install`.

La versión incluye el [autorregistro de dispositivos](../guides/device-self-registration.md), las [integraciones de entrada](../guides/inbound-integrations.md) para servidores de red LoRaWAN y webhooks, un canal de comandos opcional hacia nodos Sparkplug B, la [autenticación en dos pasos](#after-v0252-mfa) y el paso de ingress-nginx a Traefik. Elimina el filtro de credenciales en el cuerpo de MQTT. Lo que exige actuar, en el orden en que hacerlo:

- **Un clúster con dos instancias del perfil default necesita un presupuesto de conexiones de base de datos mayor antes de actualizar.** El presupuesto de un clúster nuevo es ahora `700`; un clúster instalado con el valor anterior conserva `600`, y la actualización de la segunda instancia puede rechazarse. Vuelve a ejecutar el `dcctl install` con el que se instaló el clúster añadiendo `--max-connections 700`: por ejemplo `dcctl install local --max-connections 700`, más los indicadores con los que se instaló, como `--kube-context`, `--ha`, `--compact` y `--database-node-selector`. Consulta [la nota de update-management más abajo](#v0260-updates) y [el presupuesto de conexiones](./bootstrap.md#connection-budget).
- **Elimina `functionalAreas.event-sources.config.capture.bodyCredential` si lo fijaste** y quita `--mqtt-body-credential` de los scripts. Consulta [se elimina el filtro de credenciales en el cuerpo](#after-v0252-body-credential).
- **Revisa todo lo que automatice perfiles de aprovisionamiento** y rota el secreto de cada perfil existente. Consulta [Perfiles de aprovisionamiento](#self-registration-profiles).
- **Los clientes de API que inician sesión deben tratar `MFA_CHALLENGE`.** Consulta [Autenticación en dos pasos](#after-v0252-mfa).
- **Si pasas una instalación existente a Traefik, comprueba el certificado del Gateway antes de retirar los objetos Ingress.** Una instancia actualizada a través del cambio de emisor puede quedarse con un certificado `devicechain-tls-gateway` que la autoridad certificadora propia de la instancia no emitió. Comprueba su emisor y vuelve a emitirlo si no es la CA de la instancia; la comprobación es un paso de [la guía de la migración](./ingress-cutover.md#ingress-cutover-steps), antes de retirar los objetos Ingress.
- **La pila de monitorización pasa a una nueva línea mayor** (Prometheus 3). Consulta [La pila de monitorización](#v0260-monitoring).
- **Avanza desde esta versión; no retrocedas.** La migración de device-management que sustituye por resúmenes los secretos de los perfiles de aprovisionamiento y de las reclamaciones de dispositivo no se puede deshacer, y el lector de captura pasa a un consumidor duradero nuevo (consulta [Mensajería](#v0260-messaging)).

Se ejecutan seis migraciones, una en el primer arranque del servicio que la posee, y cada una puede volver a ejecutarse si se interrumpe: en `device-management`, autorregistro de dispositivos, resúmenes de secretos de alta, integraciones y alias de dispositivo; en `user-management`, el segundo factor y la dimensión de gobernanza del autorregistro. Al arrancar, `device-management` también convierte en resumen con clave todo secreto de perfil de aprovisionamiento y de reclamación de dispositivo almacenado; una reclamación abierta durante la actualización sigue funcionando con el secreto que se entregó a su titular.

Hasta ahora un perfil de aprovisionamiento se podía crear y editar, pero nada lo leía: ningún dispositivo podía registrarse con uno. Así que nada que funcionara ayer deja de funcionar porque una flota lo usara. Lo que cambia es cómo se crean los perfiles y qué necesitan.

La versión también añade la [autenticación en dos pasos](#after-v0252-mfa), que cambia la forma en que los clientes de la API inician sesión en una cuenta que la activa.

#### Perfiles de aprovisionamiento {#self-registration-profiles}

- **El servidor genera el secreto de aprovisionamiento.** `createProvisioningProfile` ya no acepta `provisionSecret`. Devuelve `{ profile, issuedSecret }` donde antes devolvía el perfil, y `issuedSecret` se muestra **una sola vez**. Un script que selecciona `token` del resultado ahora debe seleccionar `profile { token }`. `updateProvisioningProfile` tampoco acepta ya `provisionSecret`. `rotateProvisioningProfileSecret(token)` es la única forma de reemplazar un secreto. Ambos cambios son incompatibles: regenera cualquier cliente construido contra el esquema anterior y revisa tus scripts. Las operaciones nuevas están en [Perfiles de aprovisionamiento](../reference/graphql-api.md#provisioning-profiles).
- **Los perfiles existentes se rechazan hasta que los rotes.** El nuevo endpoint no acepta un secreto elegido por un operador, porque solo acepta uno que generó el servidor. Todo perfil que exista en el momento de la actualización responde `401 PROVISIONING_FAILED` a un registro, y la consola lo marca como **Requiere rotación** (`secretIssued` es `false` en GraphQL). Rótalo, copia el secreto nuevo y entrégalo a la flota. Nada más del perfil cambia. **Esta actualización no se puede deshacer.** Los secretos de los perfiles de aprovisionamiento y de las reclamaciones de dispositivo se sustituyen por resúmenes en la base de datos, y esa migración se niega a revertirse (el texto en claro ya no existe, así que eliminar los resúmenes destruiría todos los secretos). Si debes volver a una versión anterior, restaura desde una copia de seguridad.
- **La clave de aprovisionamiento es única por inquilino, no en toda la instancia.** Dos inquilinos pueden usar ahora la misma clave. La restricción de base de datos que la hacía global a la instancia se sustituye por una por inquilino.
- **Un perfil puede tener un tope.** `maxDevices` es nuevo y opcional. Un perfil que lo deja sin definir sigue sin tope.

#### Dispositivos {#self-registration-devices}

- **Un perfil adopta un dispositivo existente solo si el dispositivo se reservó para él.** Con ambas estrategias, incluida `ALLOW_NEW`, un dispositivo que existe pero no se reservó para el perfil se rechaza con `409 DEVICE_NOT_EARMARKED`. Reserva con `provisioningProfileToken` en `createDevice` e `importDevices`, o con el **Perfil de aprovisionamiento esperado** de la consola. Los dispositivos existentes no tienen reserva, y una reserva solo se puede fijar al crear el dispositivo: `updateDevice` no tiene ese campo, y una fila de `importDevices` para un dispositivo existente cuya reserva difiere, incluido un dispositivo sin ninguna, es un `CONFLICT`. Por tanto, un perfil nunca puede adoptar un dispositivo creado antes de esta actualización. Ese dispositivo sigue funcionando con la credencial que ya tiene; para ponerlo bajo un perfil, crea un dispositivo nuevo reservado para él.
- **Un perfil no se puede eliminar mientras tenga credenciales emitidas**, y un tipo de dispositivo no se puede eliminar mientras lo use un perfil. Ambos responden `REFERENCE_VIOLATION`. Restablece o elimina antes los dispositivos.

#### Operación {#self-registration-operating}

- **El endpoint necesita el ingress de la API.** `POST /api/device-management/provisioning/{tenant}/register` se alcanza por la misma ruta `/api/<area>` que la API GraphQL. Una instancia con `ingress.enabled: false` no tiene camino público hasta él.
- **`dcctl bootstrap` y `dcctl upgrade` ahora generan `infrastructure.http.trustedProxies`** a partir del rango de pods del clúster, de modo que el límite por origen vea al dispositivo y no al controlador de ingress. La configuración de la instancia cambia, así que los servicios se reinician con el resto de la actualización progresiva. Revísalo si ejecutas tu propio controlador de ingress o estás detrás de un balanceador de carga: [Direcciones de cliente y proxies de confianza](./bootstrap.md#trusted-proxies). Sin él, todos los dispositivos comparten una sola asignación de peticiones por pod de ingress, que un desconocido puede agotar.
- **Configuración nueva**, toda opcional, bajo la clave `provisioning` de device-management: `readDeadline`, `maxInFlight`, `maxConcurrentRegistrations`, `sourceBurst` y `sourceEvery`. Los valores por defecto están en [los límites](../reference/device-protocol.md#self-registration-limits).
- **Métricas nuevas y tres alertas** (`ProvisioningAuthFailuresSustained`, `ProvisioningRateLimited`, `ProvisioningMisconfigured`) llegan con el chart. Están en silencio en una instancia que no usa el endpoint. Consulta [Autorregistro de dispositivos](./observability.md#self-registration-alerts).
- **Una nueva dimensión de gobernanza** limita los registros que puede hacer un inquilino: por defecto una ráfaga de 500 y 20 por segundo, fijada por nivel o por inquilino. Consulta [Gobernanza y cuotas](../concepts/governance.md).

#### El front end pasa de ingress-nginx a Traefik {#after-v0252-front-end}

Las instalaciones nuevas sirven su host con Traefik y rutas de la Gateway API, y `ingress-nginx` ya no se instala en un clúster que no lo tiene. **Un clúster existente conserva `ingress-nginx`, sirviendo, y recibe Traefik a su lado.** Nada cambia en el host hasta que lo muevas:

- `dcctl install` instala Traefik y las definiciones de la Gateway API a un lado de `ingress-nginx`, con un balanceador de carga y una dirección propios.
- `dcctl upgrade` da a cada instancia un Gateway y rutas, y mantiene sus objetos Ingress a su lado mientras `ingress-nginx` esté en el clúster. Los dos usan certificados distintos: los objetos Ingress conservan `devicechain-tls` y el Gateway usa `devicechain-tls-gateway`. Durante la ventana la actualización espera 30 segundos al certificado del Gateway y después avisa y continúa; `--drop-legacy-ingress` se niega hasta que esté `Ready`, y `--remove-legacy-ingress` comprueba todas las instancias.
- Mueves el DNS a la dirección de Traefik cuando quieras. Entonces `dcctl upgrade --drop-legacy-ingress` deja de generar los objetos Ingress, y `dcctl install --remove-legacy-ingress` quita `ingress-nginx`. La eliminación se rechaza mientras algún objeto Ingress nombre todavía su clase.
- `dcctl upgrade --ingress-controller nginx` devuelve una instancia a `ingress-nginx` mientras esté instalado. La elección queda registrada y las actualizaciones posteriores la conservan; `--ingress-controller auto` la borra.
- **Un emisor ACME que resuelve HTTP-01 a través de la clase de ingress `nginx` sigue funcionando durante la ventana, y falla en sus renovaciones cuando se quita `ingress-nginx`.** Déjalo como está hasta que el DNS apunte a Traefik, y entonces dale un solver `gatewayHTTPRoute` antes de `--drop-legacy-ingress` y `--remove-legacy-ingress`. Uno fijado antes falla. Un solver DNS-01 funciona en todo momento.
- En AWS el Classic Load Balancer oculta las direcciones de cliente. `dcctl install --aws-nlb traefik` da al Service de Traefik un Network Load Balancer (`nginx`, `all` y `none` son los otros valores); cambiar un Service sustituye su balanceador y cambia su nombre de host.
- Una instalación HA ejecuta dos réplicas de Traefik, repartidas entre nodos, con un PodDisruptionBudget que mantiene una en pie.
- En un clúster local no hay DNS que mover: el navegador llega al controlador que ocupe los puertos 80 y 443 del nodo. `ingress-nginx` los conserva hasta que `--remove-legacy-ingress` se los entrega a Traefik, así que ejecuta `--drop-legacy-ingress` y la eliminación seguidos: el host no responde entre ambos. Un clúster local nuevo tiene Traefik en los puertos desde el principio.

Todos los pasos, los comandos, los rechazos que puedes ver y el retroceso están en [Pasar una instalación existente a Traefik](./ingress-cutover.md). Los flags nuevos están en la [referencia de `dcctl`](../reference/dcctl.md).

#### Ingress-nginx conserva la dirección de cliente {#after-v0252-nginx-local}

Mientras `ingress-nginx` esté en el clúster, `dcctl install` fija ahora `externalTrafficPolicy: Local` en su Service LoadBalancer. Sin eso, todos los dispositivos de internet compartían un único cupo por nodo. Consulta [Direcciones de cliente y proxies de confianza](./bootstrap.md#trusted-proxies). El cambio tiene un coste en el momento en que se hace:

- El balanceador sigue enviando tráfico a los nodos que no ejecutan ningún pod del controlador hasta que su health check los marca como no sanos. **Espera que el tráfico hacia esos nodos se interrumpa hasta entonces (decenas de segundos).**
- Una instalación sin HA ejecuta una sola réplica del controlador, así que cada actualización del controlador es un corte breve. Una instalación HA ejecuta dos, repartidas entre nodos.

#### Las instancias autofirmadas conservan su certificado anterior {#after-v0252-self-signed}

Una instancia que sirve un certificado autofirmado lo emite ahora desde una autoridad certificadora que crea el chart en el namespace de la instancia, y los clientes pueden confiar en esa autoridad mediante `ca.crt` en el Secret del certificado ([TLS, flags e instalación en un portátil](./bootstrap.md#install-tls-and-flags)). Una instancia existente **conserva su certificado hoja anterior hasta que se renueve**. Elimina el Secret `devicechain-tls` para que se vuelva a emitir desde la autoridad de inmediato; los clientes deben entonces obtener de nuevo `ca.crt` de ese Secret.

Una instancia que ya pasó al Gateway sirve `devicechain-tls-gateway` para siempre, con un certificado hoja nuevo de la misma autoridad. Tras el paso, los clientes obtienen `ca.crt` de `devicechain-tls-gateway`.

#### Se elimina el filtro de credenciales en el cuerpo de MQTT {#after-v0252-body-credential}

Una credencial en el cuerpo de un mensaje en el broker MQTT de la plataforma se rechaza ahora siempre. No queda nada que configurar:

- **`capture.bodyCredential` se elimina, y `event-sources` se niega a arrancar mientras esté fijada**, con cualquier valor. El error nombra la clave y lo que hay que hacer. Si gestionas tú mismo los valores del chart, **elimina `functionalAreas.event-sources.config.capture.bodyCredential` antes de actualizar.**
- **`dcctl bootstrap` y `dcctl upgrade` ya no aceptan `--mqtt-body-credential`.** Un script que lo pase falla: quita el flag. Es un cambio incompatible de la línea de comandos.
- Una instancia gestionada por dcctl que estaba en `strip` queda en rechazo tras la actualización, y la actualización imprime un aviso. Los dispositivos que aún enviaban una credencial en el cuerpo dejan de recibirse. Vigílalos con `devicechain_eventsources_total_msg_credential_in_mqtt_body` y actualízalos como describe [Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT](#v0240-mqtt-body-credential).
- El filtrado propio del agente de borde sobre las credenciales de las cargas de los dispositivos (`body_credentials_stripped_total`) no cambia.

#### Se pueden perder mensajes MQTT durante la actualización {#after-v0252-mqtt-roll}

La actualización reinicia el broker de la plataforma, un servidor cada vez. En un ensayo del reinicio del broker de esta actualización con una compilación previa a la versión, en un clúster GKE de siete nodos (tres de servicios, tres de bases de datos y uno de generación de carga) instalado con `--ha`, cuyo broker ejecuta tres servidores, con dispositivos MQTT enviando QoS 1 a 300 mensajes por segundo todo el tiempo, **23 de 448.346 mensajes que el broker había confirmado no se almacenaron nunca**. Las pérdidas se produjeron en ráfagas alrededor de cada uno de los tres reinicios de servidor del broker. La entrega QoS 1 al menos una vez **no está garantizada durante un reinicio del broker**: un dispositivo al que se envió un PUBACK no reenviará. Las mediciones anteriores están en [Actualizaciones progresivas](#rolling-upgrades), el comportamiento del broker en [MQTT: el bróker de la plataforma](../reference/transport-matrix.md#mqtt--the-platform-broker), y el ajuste de cierre seguro que lo cambia es `--mqtt-fail-closed`.

#### Autenticación en dos pasos {#after-v0252-mfa}

Una cuenta ya puede activar la [autenticación en dos pasos](../guides/two-factor-authentication.md): un código de una aplicación de autenticación después de la contraseña, con diez códigos de recuperación de un solo uso. Está desactivada en todas las cuentas hasta que su dueño la active, así que no cambia nada para quien no hace nada. La consola, la aplicación de paneles y la página de autorización MCP piden el código, y los inquilinos y los niveles pueden exigirla. La base de datos de user-management añade tablas para el factor y sus códigos de recuperación mediante una migración automática.

- **`login` ahora puede fallar con `MFA_CHALLENGE`, así que los clientes de la API deben gestionarlo.** Para una cuenta con segundo factor, una contraseña correcta ya no devuelve un token: `login` falla con el código de error `MFA_CHALLENGE` y un desafío en `extensions`, y el cliente termina con el nuevo `verifyMfa(challenge, code)`. Un cliente que no lo conoce ve un inicio de sesión fallido con un mensaje legible, nunca un token. Las cuentas sin segundo factor inician sesión exactamente como antes. El SDK de C# lanza `MfaRequiredException` y añade `VerifyMfaAsync`; `dcctl` añade `--otp` en `dead-letters`, `presence` y `sim`. Consulta [Iniciar sesión con autenticación en dos pasos](../reference/graphql-api.md#two-factor-sign-in).
- **La fila de auditoría `login` cambió de significado.** Ahora se escribe cuando un inicio de sesión **se completa**, con una etiqueta `pwd`, `pwd+otp` o `pwd+recovery`. Para una cuenta con segundo factor, el paso de la contraseña es una nueva fila `mfa_challenged`, así que una consulta que contaba las filas `login` como contraseñas aceptadas ahora cuenta inicios de sesión completados. Las filas nuevas registran la configuración, los restablecimientos, los intentos fallidos y bloqueados y el uso de códigos de recuperación.
- **Configurar el factor, y desactivarlo, termina las sesiones de la cuenta.** Al configurarlo terminan todas las demás sesiones y la consola cambia la tuya por una nueva. Al desactivarlo terminan todas las sesiones, incluida la del llamante.
- **Un inquilino puede exigirla,** y un nivel puede fijar un mínimo con la clave de nivel `mfaRequired`. A los miembros sin factor se les envía a configurarlo, y las sesiones que ya están dentro terminan en su siguiente renovación. Los superusuarios, las cuentas de servicio y las sesiones de inicio de sesión único no están sujetos al ajuste de un inquilino.
- **Un nuevo comando break-glass, `dcctl instances break-glass mfa-reset`,** restablece a un operador que ha perdido su factor y no puede iniciar sesión. Necesita credenciales del clúster, siempre sustituye la contraseña e imprime la nueva una sola vez.
- **Dos alertas nuevas, `MfaIdentityLocked` y `MfaVerificationFailuresSustained`,** y cuatro métricas llegan con el chart. Permanecen en silencio mientras nadie tenga un segundo factor. El remedio para un bloqueo es restablecer la contraseña, no el factor. Consulta [Bloqueos del segundo factor](./observability.md#mfa-alerts).
- **Los nodos necesitan una sincronización horaria correcta.** Un código es válido durante su intervalo de 30 segundos y uno a cada lado.

#### Las actualizaciones over-the-air pasan al perfil default {#v0260-updates}

Las [actualizaciones over-the-air](../concepts/over-the-air-updates.md) forman parte del perfil `default` a
partir de esta versión, así que una instancia que ejecuta el sistema estándar incorpora el servicio
`update-management` al actualizarse. Nada más cambia en la actualización. Comprueba esto antes de empezar:

- **Puede que haya que subir antes el presupuesto de conexiones de la base de datos relacional.**
  `update-management` es un servicio más que usa la base de datos, así que una instancia de perfil `default`
  reserva ahora 320 conexiones, frente a 280. El presupuesto de un clúster nuevo es ahora `700`, que admite dos
  instancias de perfil `default`. **Un clúster instalado con el valor anterior de `600` lo conserva**, y si
  aloja dos instancias de perfil `default`, la actualización de la segunda puede rechazarse. Sube el presupuesto antes de actualizar volviendo a ejecutar el `dcctl install` con el que se instaló el clúster, añadiendo `--max-connections 700`: por ejemplo `dcctl install local --max-connections 700`, más los indicadores con los que se instaló, como `--kube-context`, `--ha`, `--compact` y `--database-node-selector`.
  Consulta [el presupuesto de conexiones](./bootstrap.md#connection-budget), que también explica lo que cuesta
  subirlo: las instancias de la base de datos se reinician una a una.
- **Si tu almacén de objetos es un volumen `ReadWriteOnce` en un clúster al que apuntaste `dcctl`, el servicio
  se ejecuta sin almacén.** Un volumen así lo puede montar un solo servicio y los demás ya lo ocupan, así que
  `dcctl` actualiza la instancia con `update-management` en `updates.objectStore: none` en lugar de rechazarla.
  La actualización lo indica. Declarar un artefacto y subir su contenido se rechazan con
  `OBJECT_STORE_NOT_CONFIGURED`, la consola muestra un aviso en las pantallas de Actualizaciones y
  `updateManagementInfo.objectStoreConfigured` es `false`. Una instancia no puede obtener un almacén al
  actualizar; para usar actualizaciones, vuelve a crearla con S3 o con un volumen `ReadWriteMany`. Consulta
  [Una instancia sin almacén de objetos](../concepts/over-the-air-updates.md#no-object-store). Un volumen
  compartible, S3 y un clúster que creó `dcctl` no se ven afectados.
- **No se puede asignar ningún artefacto hasta que se registre un firmante.** Por defecto solo se puede asignar
  un artefacto firmado por un firmante activo, y en caso contrario la asignación se rechaza con
  `UNVERIFIED_ARTIFACT_REFUSED`. Registrar un firmante requiere la autoridad `artifact:trust`. Sigue
  [Firmar actualizaciones](../guides/signing-updates.md).
- **La base de descarga ahora se deriva.** `updates.downloadBaseUrl` estaba sin definir hasta que la
  establecías, de modo que ningún dispositivo recibía un enlace de descarga. Si la omites, el chart ahora deriva
  `<origen>/api/update-management` del host del ingress de la instancia, con `https` si el TLS del ingress está
  activado. Un valor que definas sigue prevaleciendo, y una cadena vacía desactiva las descargas. Una instancia
  sin ingress no tiene origen del que derivarla, y la consola indica que las descargas no están configuradas.
- **`dcctl enable-area local <instancia> update-management` en una instancia `default` informa de que ya está activada.**
  Las instancias de los perfiles `telemetry` e `ingest-only` siguen sin incluirla.
- **Se llega a los mismos dispositivos que antes.** Solo un dispositivo conectado por MQTT puede recibir una
  actualización gestionada. Los dispositivos solo HTTP, LwM2M y Sparkplug no se alcanzan; consulta
  [Qué dispositivos se pueden actualizar](../concepts/over-the-air-updates.md#transports).

#### Más sobre las actualizaciones over-the-air {#v0260-updates-more}

Las actualizaciones quedan completas en esta versión más allá de entrar en el perfil default. Un [dispositivo de referencia actualizable](../guides/building-an-updatable-device.md) (ranuras A/B, un lanzador y reversión) se incluye en `examples/ota-device`. Cuatro alertas nuevas, `UpdateDownloadsNotConfigured`, `UpdateReportsMostlyRefused`, `UpdateRolloutStepsFailing` y `UpdateAttemptsGoingUnknown`, llegan con el chart y solo se generan en una instancia que tiene `update-management`; una instancia que no usa actualizaciones permanece en silencio. Consulta [el runbook de alertas](./observability.md).

#### Comandos Sparkplug B (opcional) {#v0260-sparkplug-commands}

DeviceChain ya puede enviar comandos a nodos y dispositivos Sparkplug: escribir una métrica, pedir a un nodo que rehaga su nacimiento (rebirth) o reiniciarlo, publicados en la sesión de Host Application que la plataforma ya mantiene con tu broker. Está desactivado de forma predeterminada y se activa por fuente con `commands.enabled` en los valores del chart (`dcctl` no admite fuentes Sparkplug, así que es un ajuste de valores de Helm). Sparkplug no tiene acuse de recibo de comandos: un comando se confirma cuando el nodo informa después del valor pedido, y uno que el nodo no informa termina como tiempo agotado, no como fallo. Las escrituras se limitan a las métricas que lista una fuente, los reinicios requieren un ajuste explícito y un cortacircuitos pausa una fuente cuyo broker rechaza la publicación. La credencial de broker de la fuente necesita permiso para publicar en los temas de comandos de nodo y de dispositivo. Tres alertas nuevas cubren comandos sin confirmar, un cortacircuitos abierto y fallos internos de comandos: `SparkplugCommandsUnconfirmed`, `SparkplugCommandBreakerOpen` y `SparkplugCommandResponsesFailing`.

No cambia nada en una instalación cuyas fuentes Sparkplug dejan `commands.enabled` desactivado. Cuando una fuente lo activa, el chart fija por sí mismo el indicador de capacidad de `command-delivery`; el renderizado falla si `command-delivery` no está desplegado, o si fijas `functionalAreas.command-delivery.config.sparkplugCommands` a mano.

#### Métricas calculadas {#v0260-calculated-metrics}

Las [métricas calculadas](../user-guide/calculated-metrics.md) siguen desactivadas: el techo de nivel que permite a un inquilino crearlas sigue siendo `0` de forma predeterminada, y esta versión no lo cambia. Cambian dos cosas a su alrededor. **Revertir un perfil de dispositivo a una versión anterior ahora cumple el mismo techo que publicar:** un inquilino cuyo techo se bajó después de publicar ya no puede reactivar una versión con más métricas calculadas de las que permite su nivel; la reversión se rechaza como `LIMIT_EXCEEDED`, y también cuando no se puede leer el techo. Revertir a una versión sin métricas calculadas nunca depende del techo. La consola ahora explica por qué una métrica calculada no puede combinar sus entradas. En `hack/` se incluye un arnés para medir el coste de las métricas calculadas; es para mantenedores y no forma parte de una instalación.

#### Integraciones de entrada y alias de dispositivo {#v0260-integrations}

Una [integración](../guides/inbound-integrations.md) es un emisor autenticado de datos de dispositivo por HTTP de un inquilino, como un servidor de red LoRaWAN, con los tipos ChirpStack v4, The Things Stack v3, JSON genérico y binario genérico. El chart publica exactamente una ruta para ella en el front end de la instancia, `POST /api/integrations/{tenant}/{integration}[/{messageType}]`, tanto por el Gateway como por los objetos Ingress. La ruta general de ingesta de dispositivos no se publica. Un inquilino puede tener cinco integraciones de forma predeterminada (la clave de nivel `integrationsPerTenant`, de 0 a 100). No se puede eliminar un tipo de dispositivo al que está acotada una integración. Los dispositivos se identifican con **alias**, y un uplink de un dispositivo que ya no tiene el alias con el que llegó se rechaza como `IntegrationAliasMismatch`, de modo que quitar o mover un alias surte efecto como mucho en cinco minutos, nunca en una hora. La consola incorpora una página de Integraciones, fragmentos de conexión, dispositivos sin registrar y alias de dispositivo.

#### La pila de monitorización {#v0260-monitoring}

`dcctl install` actualiza la pila de monitorización (kube-prometheus-stack 65.1.1 a 92.13.1): **Prometheus 2.54 a 3.15**, Grafana 11.2 a 13.2 y Prometheus Operator 0.77 a 0.94. Prometheus, Alertmanager, Grafana, kube-state-metrics y el operador se reinician una vez cada uno. Los CRD del operador se actualizan con un trabajo previo a la actualización que descarga `docker.io/busybox:1.37.0` y `registry.k8s.io/kubectl`, así que un clúster sin ruta a esos registros debe replicarlos. Grafana conserva su acceso de administrador, sus carpetas y sus paneles. En un clúster local Prometheus guarda sus datos solo en el disco del nodo, por lo que su historial empieza vacío. Quien tenga PromQL propio contra este Prometheus debe saber que es Prometheus 3: los selectores de rango son abiertos por la izquierda, `.` en una expresión regular coincide con un salto de línea y las etiquetas `le` de los histogramas clásicos se normalizan. Las reglas y paneles propios de DeviceChain se cargan sin errores de reglas.

Los paneles de Grafana del chart se completan: paneles de Ingesta, Mensajería, Persistencia y estado, Entrega y Plataforma, una vista general de la instancia que enlaza con cada uno y un estado de escalado a 0 en la vista general.

#### Mensajería y línea de comandos {#v0260-messaging}

- **El lector de captura de dispositivos pasa a un consumidor duradero nuevo,** `<instance>_event-sources_device-events-capture_unfiltered`, creado sin filtro en el broker. Un lector retrasado sobre un consumidor filtrado caía a unos 2.000 eventos por segundo y no se ponía al día. Durante la actualización progresiva un pod antiguo de `event-sources` conserva su propio consumidor. Para volver a `v0.25.x` hay que borrar antes el consumidor nuevo (`nats consumer rm <instance>_device-events-capture <instance>_event-sources_device-events-capture_unfiltered`), o su lector de captura se queda reintentando; los selectores de alertas y paneles ya coinciden con ambos nombres.
- **REACT despacha los eventos derivados de forma concurrente, por dispositivo,** donde un solo trabajador lo hacía en serie; un dispositivo colgado ya no cuesta a los demás de su carril sus intentos de entrega. No hay nada que configurar.
- **`dcctl bootstrap` y `dcctl upgrade` incorporan `--mqtt-fail-closed`** (y `--mqtt-fail-closed=false`) para activar y desactivar el acuse MQTT con cierre ante fallos; si se omite, se conserva la elección registrada. Además, cada `dcctl upgrade` rechaza ahora, antes de cambiar nada, una configuración cuya reserva de streams no cabe en el techo de almacenamiento de archivos del broker.
- **OpenTofu actúa sobre el clúster que `dcctl` resolvió.** Su proveedor recibe ahora el mismo kubeconfig y contexto que el cliente, y `dcctl` se niega a ejecutarse cuando alcanzan clústeres distintos o cuando hay una variable de entorno que anula el proveedor (`KUBE_HOST`, `KUBE_TOKEN` y similares).

#### Un broker que solicita certificados de dispositivo {#v0260-broker}

El broker es ahora la compilación propia de DeviceChain de `nats-server`, `dc-nats-server`: la `v2.15.1` original más un ajuste, aplicado al arrancar y en cada recarga, para que el listener MQTT solicite (sin exigir) un certificado de cliente TLS. Los dispositivos con token o contraseña no se ven afectados y todavía ningún dispositivo se autentica con certificado. La imagen se firma como las demás, se ejecuta como root igual que la imagen original y exporta `dc_broker_mqtt_client_auth_request`, `dc_broker_mqtt_tls` y `dc_broker_info` en el puerto 7778. La alerta nueva `BrokerMqttClientAuthLost` se dispara cuando un listener deja de solicitar certificados o cuando un pod del broker no es esta compilación. **Para volver a la imagen original,** fija `server_image_repository = "nats"` y `server_image_tag = "2.15.1-alpine"`; eso desactiva los dispositivos con certificado. Añade `dc-nats-server` a las imágenes que replicas.

`dcctl` fija el broker por el resumen (digest) que publicó la versión, de modo que una versión que no cambia nada del broker no lo reinicia. La primera actualización desde un `dcctl` anterior pasa la plantilla del pod de la etiqueta al resumen, lo que supone un reinicio progresivo. `dcctl version` imprime una línea `broker:`, con el resumen o `by tag`.

#### Problemas conocidos {#v0260-known-issues}

- **Tras un reinicio progresivo del broker, incluida esta actualización, el último servidor reiniciado no lidera ningún stream,** y el p99 del acuse de publicación se duplica aproximadamente hasta que el liderazgo se reequilibra.
- **Una rotación de certificado TLS hace que el broker se recargue y desconecta la mayoría de las sesiones MQTT.** Los dispositivos se reconectan solos. Una versión posterior lo corrige: el broker aplica el certificado renovado a las conexiones nuevas sin recargarse, así que las sesiones establecidas se mantienen. Un cambio en la configuración del broker sigue recargándolo, y una recarga sigue desconectando las sesiones de los dispositivos admitidos por el callout de autenticación hasta que la biblioteca `nats-server` del broker incluya la corrección de upstream.
- **Un despliegue del controlador de ingress pierde alrededor del 5 % de las solicitudes durante 28 a 40 segundos.** Hazlo en una ventana tranquila.

### v0.25.2 — la emisión de tokens de servicio sale del puerto público {#v0252-upgrade}

`v0.25.2` es un parche de seguridad de `v0.25.1`. No hay migración, y no cambia ninguna API, tema ni
permiso. La configuración de la instancia gana una clave, `infrastructure.userManagement.internalPort`,
que el chart fija en `8082`; déjala en ese valor, porque el Service, el pod y la política de red del chart
usan ese puerto fijo. Todo lo que describen las secciones de la [v0.25.1](#v0251-upgrade) y la
[v0.25.0](#v0250-upgrade) sigue siendo lo que estás ejecutando, incluidas sus limitaciones conocidas.

Actualiza el clúster y después cada instancia. Pasa a `dcctl install` los ajustes con los que se instaló
el clúster, como `--ha` y `--database-node-selector`, además de la nueva `--version`:

```bash
dcctl install local --version v0.25.2
dcctl upgrade local devicechain --version v0.25.2 --dry-run
dcctl upgrade local devicechain --version v0.25.2
```

**Si vienes de `v0.25.0` o de `v0.24.0`,** sigue las [notas de la v0.25.1](#v0251-upgrade) con
`--version v0.25.2`: se aplican sin cambios, y también lo que dicen sobre las actualizaciones de la v0.25.0.

#### Los endpoints internos salen del puerto público {#v0252-internal-endpoints}

`user-management` servía en su puerto público dos endpoints pensados solo para los servicios de la propia
plataforma: `/auth/service-token`, que intercambia el secreto de servicio compartido por un token de
servicio de corta duración, y `/auth/jwks`, el conjunto de claves con el que cada servicio valida los
tokens. Lo único que los mantenía fuera de internet era un fragmento de configuración (snippet) de nginx en
el ingress de la API del chart. **Por eso, una instalación que traía su propio controlador de ingress tenía
ambos accesibles desde fuera,** protegidos por el secreto de servicio compartido, que se compara en tiempo
constante.

A partir de esta versión, `user-management` sirve ambos en un puerto interno aparte, el `8082`, que el
chart publica solo en el Service interno del clúster. Ningún ingress, ruta ni balanceador de carga se
refiere a él, y el puerto público responde 404 a ambas rutas, haya lo que haya delante, así que **un
controlador propio no necesita ninguna regla en el borde para ocultarlos.** Ahora cada servicio obtiene el
conjunto de claves y emite su token en el puerto interno. Consulta
[Endpoints que nunca salen del clúster](../concepts/security-model.md#internal-endpoints).

- **El chart ya no fija ningún snippet de configuración de nginx.**
- **El controlador de ingress que instala `dcctl install` no cambia en esta versión** y sigue aceptando
  snippets, porque las instancias aún no actualizadas dependen de ellos. Una versión posterior los
  desactiva, tras una comprobación de `dcctl install` que se niega a continuar mientras algún Ingress de la
  clase del controlador siga llevando una anotación `*-snippet`.
- **Los verificadores de tokens externos no se ven afectados:** usan el `jwks_uri` de los metadatos del
  servidor de autorización, `/oauth/jwks`, que sigue siendo público.

#### Qué esperar {#v0252-during}

- **`user-management` y sus pares deben actualizarse juntos.** Un pod de una versión anterior pide el
  conjunto de claves y la emisión de tokens al puerto público, que ahora responde 404, y no sale de ello
  reintentando: sus emisiones fallan hasta que ese pod se sustituye por uno de esta versión.
  `dcctl upgrade` sustituye los pods de todos los servicios, así que esto dura lo que dura el despliegue;
  no dejes ningún área atrás en una imagen anterior.
- **Un pod anterior que se reinicia después de que `dcctl upgrade` haya escrito la nueva configuración no
  arranca.** Los binarios anteriores decodifican la configuración de la instancia de forma estricta y
  rechazan la nueva clave `internalPort`, así que ese pod entra en un bucle de reinicios hasta que se
  sustituye por uno de esta versión.
- **Si usas tu propio motor de políticas de red,** permite que los pods del namespace de la instancia
  lleguen al puerto `8082` de `user-management`, y nada más. La política de red propia del chart para ese
  puerto está desactivada por defecto, y las instalaciones con `dcctl` la ejecutan desactivada.
- **Los paneles de Grafana que incluye el chart** se abren con una tabla de las alertas activas de la
  instancia y enlazan a los demás paneles de la instancia, y un tercer panel, *Instance overview*, contiene
  por ahora solo esa tabla.

### v0.25.1 — las imágenes de las bases de datos y del almacén de respaldos, reconstruidas por avisos de seguridad actuales {#v0251-upgrade}

`v0.25.1` es un parche de `v0.25.0`. No hay migración, y no cambia ninguna API, tema, permiso, valor del
chart ni clave de configuración. Mueve las tres imágenes de terceros sobre las que el clúster y cada
instancia ejecutan sus bases de datos y su almacén de respaldos a compilaciones que ya no llevan los avisos
de seguridad que se enumeran abajo, y corrige lo que dos servicios informan a Prometheus y los paneles de
Grafana que el chart incluye para ellos (consulta [Las métricas y los paneles](#v0251-metrics)). Todo lo que describe la
[sección de la v0.25.0](#v0250-upgrade) sigue siendo lo que estás ejecutando.

Siguen vigentes las [limitaciones conocidas de la v0.25.0](#v0250-upgrade), incluida esta: con `mqttFailClosed` desactivado, el valor por defecto, también se pueden perder mensajes QoS 1 confirmados con carga sostenida y sin reinicio del broker, cuando `event-sources` se queda atrás y el stream de captura se llena. `devicechain_<area>_jetstream_consumer_unread_skipped_total` los cuenta y `JetStreamDurableLostUnread` alerta.

Actualiza el clúster y después cada instancia. Pasa a `dcctl install` los ajustes con los que se instaló
el clúster, como `--ha` y `--database-node-selector`, además de la nueva `--version`:

```bash
dcctl install local --version v0.25.1
dcctl upgrade local devicechain --version v0.25.1 --dry-run
dcctl upgrade local devicechain --version v0.25.1
```

**Si vienes de `v0.24.0`,** sigue las [notas de la v0.25.0](#v0250-upgrade) con `--version v0.25.1`: se
aplican sin cambios, incluido ejecutar `dcctl upgrade` dos veces. **Si vienes de `v0.25.0`,** basta con un
`dcctl upgrade` si se ejecutaron las dos actualizaciones de la v0.25.0. Si solo se ejecutó la primera, el
inicio de sesión compartido del bróker sigue en su sitio, y esta actualización lo retira, con el reinicio
del bróker que eso conlleva; su `--dry-run` y su resumen final lo indican (consulta
[Los inicios de sesión del bróker, en dos actualizaciones](#v0250-broker-logins)).

#### Qué se mueve {#v0251-images}

| Imagen | Ejecuta | De | A | Avisos que ya no lleva |
| --- | --- | --- | --- | --- |
| `ghcr.io/devicechain-io/postgresql-timescaledb` | el almacén de eventos de cada instancia | `17.10-ts2.28.3-r1` | `17.11-ts2.28.3-r1` | Perl `5.36.0-7+deb12u3`: CVE-2026-13221, CVE-2026-42496, CVE-2026-8376 (críticos), CVE-2026-42497, CVE-2026-48962, CVE-2026-57432, CVE-2026-57433 (altos). PCRE2 `10.42-1`: CVE-2026-103111, CVE-2026-86145, CVE-2026-89157, CVE-2026-89161 (altos) |
| `ghcr.io/cloudnative-pg/postgresql` | la base de datos relacional compartida del clúster (`dc-rdb`, en `dc-system`) | `17.10-standard-bookworm` | `17.11-standard-bookworm` | los mismos avisos de Perl y PCRE2 |
| `cgr.dev/chainguard/minio` | el almacén de respaldos dentro del clúster, cuando los respaldos van a él | resumen `sha256:32c504be…` | resumen `sha256:73297936…` | CVE-2026-111497 (decodificador Brotli, alto), CVE-2026-111943 (`crypto/x509` de Go, en el cliente `mc` incluido, alto) |

La imagen del almacén de eventos es una compilación propia de DeviceChain sobre la base PostgreSQL de
CloudNativePG, así que se mueve con esa base. PostgreSQL pasa de 17.10 a 17.11, una versión menor: los
ficheros de datos se conservan tal cual y no se vuelca ni se restaura nada. TimescaleDB sigue en 2.28.3.
Analizada con Trivy en busca de avisos altos y críticos que tengan corrección, ninguna de las imágenes
nuevas informa de ninguno.

#### Las métricas y los paneles {#v0251-metrics}

- **`event-processing` pone a cero sus indicadores exclusivos del líder cuando una réplica deja de serlo.**
  Una réplica que había perdido la concesión de detección seguía exportando el retraso, la demora y los
  tamaños de estado de su último mandato. El panel y las alertas `DetectConsumerBacklogHigh`,
  `DetectWatermarkLagHigh` y `DetectTenantOverStateBudget` toman la lectura más alta entre réplicas, así
  que esa lectura congelada podía mostrar un retraso que ninguna réplica tenía e impedir que una alerta se
  resolviera. Una réplica en espera informa ahora cero para ellos. El recuento de reglas cargadas no
  cambia, porque todas las réplicas cargan las reglas.
- **`command-delivery` exporta sus contadores de lotes de comandos a cero desde el arranque**, de modo que
  su panel muestra cero en lugar de "No data" antes del primer lote, y un `increase()` sobre ellos ve el
  primero.
- **Se corrigen los dos paneles que incluye el chart:** sus series coinciden con lo que exportan los
  servicios, los eventos poco frecuentes se muestran como recuentos, los gráficos que mezclaban unidades se
  separan y se añaden gráficos para el presupuesto de muestras retenidas.

#### Qué esperar {#v0251-during}

- **`dcctl install` reinicia una vez las instancias de la base de datos relacional**, sobre la imagen
  nueva: con `--ha`, primero las réplicas en espera y después una conmutación a una de ellas, así que las
  escrituras se detienen solo durante la conmutación. Una base de datos relacional de una sola instancia
  no está disponible hasta que esa instancia se ha reiniciado, y las escrituras hechas mientras tanto se
  reintentan. `dcctl install` espera hasta que cada instancia ejecuta la imagen nueva.
- **`dcctl install` reinicia una vez el pod del almacén de respaldos**, cuando el clúster guarda sus
  respaldos dentro del clúster. Los respaldos y el registro de escritura anticipada guardados se
  conservan. Mientras el pod se reinicia, el archivado se detiene y las bases de datos guardan el registro
  de escritura anticipada localmente. Un clúster que envía sus respaldos a tu propio almacén de objetos con
  `--backup-credentials-file` no ejecuta ningún almacén de respaldos, y ahí no cambia nada.
- **`dcctl upgrade` reinicia una vez el almacén de eventos de la instancia**, sobre la imagen nueva, en el
  paso de infraestructura antes de mover los servicios: con `--ha`, primero las réplicas en espera y
  después una conmutación; sin `--ha`, la única instancia se reinicia, y event-management no puede guardar
  eventos hasta que vuelve. Esos eventos esperan en el flujo de ingesta y se reintentan; uno se abandona
  solo tras unos cuatro minutos de reintentos fallidos. La actualización espera hasta que cada instancia
  del almacén ejecuta la imagen nueva antes de mover los servicios. Los ajustes del bróker de mensajes no
  cambian, así que sus servidores no se reinician.
- **Los servicios pasan a las imágenes `v0.25.1`** pod a pod, como en cualquier actualización. Su código
  es el mismo que el de `v0.25.0`, salvo los dos cambios de métricas de arriba.
- **Si replicas imágenes en un registro propio,** replica las tres referencias nuevas de arriba antes de
  actualizar, y las imágenes de DeviceChain `v0.25.1` como siempre.
- **Si fijaste `timescale_image` tú mismo** en el `terraform.tfvars` junto al estado de infraestructura de
  una instancia, la actualización conserva tu valor, y el almacén de eventos se queda en la imagen que
  nombra. Ponlo en `ghcr.io/devicechain-io/postgresql-timescaledb:17.11-ts2.28.3-r1`, o quita la línea,
  para moverlo.
- **Cambiar un ajuste de la base de datos en la misma ejecución se rechaza** en una base de datos de más
  de una instancia: el operador de la base de datos no acepta una imagen nueva y ajustes nuevos a la vez.
  Limita `dcctl install` a los ajustes que el clúster ya tiene (un `--max-connections` distinto, por
  ejemplo, es un cambio así), y cambia el ajuste en una ejecución aparte.

### v0.25.0 — cada área inicia sesión en el bróker con su propia identidad, y las versiones están firmadas {#v0250-upgrade}

`v0.25.0` es una actualización en su sitio desde `v0.24.0`: `dcctl install` para el clúster y después
`dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a
`dcctl install` los ajustes con los que se instaló el clúster, como `--ha` y `--database-node-selector`,
además de la nueva `--version`.
**A diferencia de la mayoría de las versiones, `dcctl upgrade` se ejecuta dos veces.** La primera ejecución da
a cada área un inicio de sesión propio en el bróker de mensajes y mantiene a su lado el inicio de sesión
compartido; la segunda retira el inicio de sesión compartido (consulta
[Los inicios de sesión del bróker, en dos actualizaciones](#v0250-broker-logins)). **No se puede alcanzar en
su sitio desde la `v0.22.0` ni versiones anteriores:** actualiza primero pasando por la `v0.23.0` y la
`v0.24.0` (consulta [Antes de actualizar](#v0250-before)). Sus tres migraciones añaden cinco tablas, una
columna y sus índices, y no reescriben ninguna fila existente (consulta
[Durante la actualización](#v0250-during)). **Desde ella se avanza, no se vuelve atrás** (consulta
[Volver atrás](#v0250-going-back)).

Los comandos, con la `v` en la versión y un ensayo primero:

```bash
dcctl install local --version v0.25.0
dcctl upgrade local devicechain --version v0.25.0 --dry-run
dcctl upgrade local devicechain --version v0.25.0
# when that has finished and every area is Ready, run it again to retire the shared broker login:
dcctl upgrade local devicechain --version v0.25.0
```

`--version` lleva la etiqueta con su `v`. En un clúster que no sea el local por defecto, pasa `--kube-context`
a `dcctl install` y a `dcctl upgrade`, y pasa `--registry` cuando las imágenes salgan de un registro distinto
del predeterminado. El `--dry-run` de cada ejecución indica cuál de las dos realizaría. Si la instancia se
inicializó sin custodia de la clave raíz, `dcctl upgrade` imprime una advertencia de custodia; es lo esperado
y no tiene relación con esta actualización. Un valor de `--mqtt-body-credential` indicado para la `v0.24.0`
se conserva en una actualización que omite la opción; la opción se eliminó en la `v0.26.0` (consulta la [`v0.24.0`](#v0240-mqtt-body-credential)).

Esta versión hace dos cosas en torno a la confianza. Dentro del clúster, **ningún servicio comparte ya una
credencial del bróker con otro**: cada área inicia sesión en el bróker de mensajes con su propia identidad,
limitada a lo que usa, y un único inicio de sesión de administración crea y modifica streams y buckets. Fuera
de él, **todas las imágenes y el chart están firmados**, de modo que puedes comprobar que lo que ejecutas lo
construyó el flujo de trabajo de versiones de DeviceChain. Además: el token de acceso de un dispositivo se
almacena como un digest con clave, el agente de borde es un gateway con una identidad propia, los tenants
pueden crear cuentas de servicio para sus integraciones, y las actualizaciones over-the-air se pueden desplegar
en un grupo de dispositivos. Qué cambia para alguien:

- **Ejecuta `dcctl upgrade` dos veces.** La primera actualización mantiene el inicio de sesión compartido
  `dc_service`; la segunda lo elimina. Consulta
  [Los inicios de sesión del bróker, en dos actualizaciones](#v0250-broker-logins).
- **El token de acceso de un dispositivo ya no se puede leer de vuelta.** Todo lo que leía `credentialId` de
  una credencial `ACCESS_TOKEN` y lo usaba como token recibe ahora una huella. Consulta
  [Los tokens de acceso de los dispositivos se almacenan como digests](#v0250-access-tokens).
- **Un agente de borde debe pasar a su identidad de gateway** antes de actualizarse; el antiguo enlace
  ascendente directo de dispositivo ha desaparecido. Consulta [El agente de borde es un gateway](#v0250-edge-gateway).
- **Una conexión del SDK de C# que el bróker rechaza lanza ahora `MqttConnectRefusedException`** en lugar de
  reintentarse como si la red estuviera caída. Consulta [El SDK de C#](#v0250-csharp-sdk).
- **Una instalación que ejecuta el chart sin `dcctl`** necesita el espacio de nombres de la instancia y el
  Secret del administrador del bróker antes de su primera instalación, o `brokerAdmin.enabled=false`. Consulta
  [Los inicios de sesión del bróker, en dos actualizaciones](#v0250-broker-logins).
- **El bróker se reinicia una vez en cada una de las dos actualizaciones,** un servidor a la vez con `--ha`.
  Consulta [Durante la actualización](#v0250-during).
- **No hay vuelta atrás a la `v0.24.0`.** Consulta [Volver atrás](#v0250-going-back).
- **Actualiza pasando por la `v0.23.0` y la `v0.24.0`** si vienes de la `v0.22.0` o anterior.
- **Tres migraciones:** dos tablas en `update-management`, tres en `user-management`, y una columna y un
  índice en `device-management`. Consulta [Durante la actualización](#v0250-during).
- **Las versiones están firmadas.** No hay nada que hacer; consulta [La primera versión firmada](#v0250-signing).
- **Una actualización revierte el ajuste que hayas hecho a mano, salvo que lo pases a `--values`.** Eso ya era
  así antes; esta versión añade la forma de conservarlo. Consulta
  [El ajuste del operador se conserva](#upgrade-tuning).

#### Antes de actualizar {#v0250-before}

1. **Si vienes de la `v0.22.0` o anterior, detente.** Actualiza a la `v0.23.0`, después a la `v0.24.0`,
   esperando cada vez a que todos los pods ejecuten la versión nueva, y solo entonces a la `v0.25.0`. El
   formato de cable compacto que la `v0.24.0` convirtió en el predeterminado es ilegible para la `v0.22.0` y
   anteriores (consulta [El formato de los eventos en el cable](#event-wire-format)), y saltarse una versión
   menor no es una ruta probada (consulta
   [Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)). Lee también las
   [notas de la `v0.24.0`](#v0240-upgrade). Una instancia construida por la `v0.16.0` o anterior sigue sin
   poder actualizarse; consulta [Instancias construidas por la v0.16.0 y anteriores](#pre-declaration-recreate).
2. **Averigua qué lee de vuelta el token de acceso de un dispositivo.** Un script, una herramienta de
   aprovisionamiento o una hoja de cálculo que obtenía el token de `deviceCredentials` o de
   `deviceCredentialsByToken` recibirá en su lugar una huella que empieza por `dctk1$`. Los tokens que están
   en los dispositivos siguen funcionando. Decide de dónde saldrá cada token que no registraste: fija un token
   nuevo en la credencial (consulta
   [Los tokens de acceso de los dispositivos se almacenan como digests](#v0250-access-tokens)).
3. **Si ejecutas el agente de borde, planifica su paso a gateway antes de actualizarlo.** Crea un dispositivo
   gateway con una credencial `MQTT_BASIC` para cada agente, y asígnale los dispositivos que el sitio le
   presenta. No reinicies un agente en esta versión hasta que la plataforma, bróker incluido, se haya
   actualizado. Consulta [El agente de borde es un gateway](#v0250-edge-gateway).
4. **Si ejecutas el chart sin `dcctl`, crea primero el espacio de nombres y el Secret del administrador,** o
   indica `brokerAdmin.enabled=false` y crea tú mismo los streams y los buckets con la imagen
   `dc-broker-admin`. Consulta [Qué aplica una actualización a la infraestructura](#upgrade-infrastructure).
   Una instalación que trae su propio bróker puede imprimir los permisos que necesita cada inicio de sesión
   con `dcctl broker permissions`.
5. **Si replicas las imágenes en tu propio registro, añade `dc-broker-admin`.** El job previo a la
   actualización la ejecuta, y la actualización falla antes de que ruede ningún servicio si no se puede
   descargar. También figura en la lista de [imágenes firmadas](./verifying-releases.md#what).
6. **Si ajustaste la instancia a mano** (número de réplicas, recursos, la configuración propia de un área),
   ponlo en un archivo de valores y pásalo como `--values` en la primera actualización. Una actualización
   reconstruye los ajustes de una instancia a partir de su declaración, así que todo lo fijado de otro modo se
   revierte. Consulta [Ajustar una instancia](./tuning-an-instance.md).
7. **Si un programa de dispositivo en C# o Unity se conecta al bróker MQTT de la plataforma,** lee
   [El SDK de C#](#v0250-csharp-sdk) antes de recompilarlo.

#### Durante la actualización {#v0250-during}

- **Se ejecutan tres migraciones, una por cada primer arranque del servicio al que pertenece.** Ninguna
  reescribe una fila, y cada una puede ejecutarse de nuevo si se interrumpe.
  - `device-management` añade la columna anulable `devices.gateway_id`, que registra el gateway tras el que
    está un dispositivo (está vacía para todos los dispositivos existentes), y un índice parcial sobre
    `(tenant_id, gateway_id)`. El índice se construye con un `CREATE INDEX` simple, que mantiene un bloqueo
    contra las escrituras en `devices` mientras se construye; cubre solo los dispositivos que tienen un
    gateway, así que está vacío en la primera ejecución.
  - `update-management` crea `update_rollouts` y `update_rollout_targets`, que contienen los despliegues en
    grupo. Ambas empiezan vacías.
  - `user-management` crea `iam_service_accounts`, `iam_service_account_roles` e
    `iam_service_account_secrets`, que contienen las cuentas de servicio y los digests de sus secretos. Las
    tres empiezan vacías.
- **`device-management` convierte cada token de acceso almacenado en su digest al arrancar.** Esto no es una
  migración: se ejecuta después de la cadena de migraciones en cada arranque, abarca todos los tenants y las
  filas eliminadas lógicamente, escribe cada fila solo si aún contiene el valor que leyó, y la segunda vez no
  encuentra nada que hacer. Un dispositivo conserva el token que tiene; la misma cadena autentica antes y
  después.
- **Mientras `device-management` se renueva, los dispositivos que se autentican con un token de acceso pueden
  no conectarse.** Cuando un pod de esta versión ha convertido las filas, un pod de la `v0.24.0` que aún está en
  la renovación busca un token presentado por su texto en claro y no encuentra nada, así que durante el
  solapamiento rechaza las conexiones con token de acceso y las comprobaciones por evento. Los dispositivos
  reintentan y llegan a un pod de esta versión. Un token que un pod de la versión anterior escribe durante el
  solapamiento se convierte en el siguiente arranque de un pod de esta versión. Los dispositivos que se
  autentican con `MQTT_BASIC` no se ven afectados.
- **El bróker se reinicia una vez en la primera actualización y otra en la segunda.** La primera añade los
  inicios de sesión al bróker; la segunda elimina el compartido. Con `--ha` cada uno se reinicia un servidor a
  la vez, cada uno Ready antes de que se detenga el siguiente; sin `--ha` se reinicia el único servidor y el
  bróker no está disponible mientras tanto, normalmente alrededor de un minuto. Los dispositivos y los
  servicios se vuelven a conectar por sí solos. **No reinicies a mano un servidor del bróker mientras se
  renueva.** Consulta [Qué aplica una actualización a la infraestructura](#upgrade-infrastructure).
- **Un job se ejecuta entre el bróker y los servicios.** El chart ejecuta `<instance>-broker-admin` como hook
  pre-install y pre-upgrade de Helm. Crea o repara cada stream y cada bucket clave-valor, incluido activar las
  lecturas directas del stream `resolved-events` que ahora usan las reproducciones de event-processing. Si
  falla, la actualización falla antes de que ruede ningún servicio, `dcctl` imprime el log del job y los
  servicios de la `v0.24.0` siguen ejecutándose. El job dispone por defecto de 540 segundos
  (`brokerAdmin.activeDeadlineSeconds`) y de un reintento (`brokerAdmin.backoffLimit`).
- **Un pod de la versión anterior que se reinicia mientras se ejecuta la actualización no arranca.** `dcctl`
  escribe la nueva configuración de la instancia justo antes del job, y esta lleva una clave (`areaLogins`) que
  la `v0.24.0` no declara, así que un pod de la `v0.24.0` que se reinicia después rechaza la configuración.
  Los pods que siguen en ejecución no se ven afectados. Deja que la actualización termine en lugar de
  reiniciar pods a mano; los pods que los sustituyen ejecutan esta versión.
- **Cada área espera su inicio de sesión.** Tras la primera actualización, un área se niega a arrancar si la
  instancia dice que existen inicios de sesión por área y falta su propio Secret, en lugar de recurrir al
  inicio de sesión compartido.

#### Volver atrás {#v0250-going-back}

**Avanza. No vuelvas a la `v0.24.0`, ni con `helm rollback` ni de ningún otro modo.** Si la actualización falla
a medias, corrige la causa y ejecuta `dcctl upgrade` otra vez con la misma `--version`. Volver atrás deja una
instancia que no funciona:

- **Se rechaza a todo dispositivo con token de acceso.** El token se almacena ahora como un digest, y la
  `v0.24.0` busca un token presentado por su texto en claro. Una reversión no restaura el texto en claro, y
  este no se guarda en ningún sitio.
- **Puede que el inicio de sesión compartido del bróker ya no exista.** Tras la segunda actualización el bróker
  ya no tiene `dc_service` y la configuración de la instancia ya no lo nombra, así que un servicio de la
  `v0.24.0` no tiene con qué conectarse. `dcctl` lo dice cuando retira el inicio de sesión: no se admite
  volver atrás a través de esta versión, vuelve a ejecutar el `dcctl upgrade` más reciente.
- **La configuración está escrita para esta versión.** `dcctl` escribe la configuración de la instancia en el
  Secret `dci-<instance>-config`, fuera de la release del chart, así que `helm rollback` restaura las cargas
  de trabajo de la release anterior y deja la configuración tal como la escribió esta versión. Esta lleva la
  clave `areaLogins`, que la `v0.24.0` no declara; los servicios rechazan al arrancar una clave de
  configuración desconocida.

Las tablas nuevas y la columna nueva no estorbarían: la `v0.24.0` lee la misma base de datos y las deja sin
usar.

#### Después de la actualización {#v0250-after}

- **Ejecuta `dcctl upgrade` una segunda vez,** cuando la primera haya terminado con todas las áreas Ready.
  Consulta [Los inicios de sesión del bróker, en dos actualizaciones](#v0250-broker-logins).
- **Comprueba las firmas de lo que instalaste,** si quieres. Consulta
  [La primera versión firmada](#v0250-signing).
- **Mantén activados `metrics.enabled` y `metrics.alerts`** (ambos lo están por defecto) para instalar las tres
  alertas nuevas de eliminación de tenants: `TenantPurgeStoreFailing`, `TenantPurgeStoreHolding` y
  `TenantPurgeCoordinatorFailing`. Consulta [Eliminación de inquilinos](./tenant-deletion.md#blocked-store).
- **Pasa cada agente de borde** cuando la plataforma haya terminado. Consulta
  [El agente de borde es un gateway](#v0250-edge-gateway).
- **Fija un token nuevo en toda credencial de token de acceso que no registraste.** Consulta
  [Los tokens de acceso de los dispositivos se almacenan como digests](#v0250-access-tokens).

##### Los inicios de sesión del bróker, en dos actualizaciones {#v0250-broker-logins}

Hasta ahora todos los servicios iniciaban sesión en el bróker de mensajes con un único inicio de sesión
compartido, de modo que cualquier pod podía presentar, y cualquier filtración de su configuración exponía, la
credencial de todos ellos. El mecanismo se describe en
[Qué aplica una actualización a la infraestructura](#upgrade-infrastructure), donde se tratan los inicios de
sesión por área, el job de administración y `dcctl broker reconcile`; qué significa para la actualización:

- **La primera actualización es la fase 1 de 2.** Crea un inicio de sesión para cada área que usa el bróker
  (`mcp` y `ai-inference` nunca abren una conexión con el bróker y no reciben ninguno) y uno para la
  administración del bróker, cada uno en un Secret llamado `dci-<instance>-nats-<area>` que solo monta esa
  área. El inicio de sesión compartido `dc_service` se mantiene, con los permisos que siempre tuvo, de modo
  que un pod que aún no se ha renovado conserva su conexión. Hasta la segunda actualización sigue estando en
  la configuración que lee cada pod, así que un pod aún puede presentarlo. El resumen final dice «This was
  phase 1 of 2: run `dcctl upgrade` once more to retire it.»
- **La segunda actualización lo retira.** Solo lo hace cuando la instancia de la que parte ya ejecuta todas
  las áreas con su propio inicio de sesión. Lo comprueba así:
  - la configuración en ejecución ya indica a las áreas que usen sus propios inicios de sesión;
  - el Secret de cada inicio de sesión existía antes de esta ejecución, de modo que ninguno se está creando
    ahora;
  - el bróker en ejecución ya tiene un inicio de sesión para cada área y para el administrador;
  - la versión en ejecución incluye el job de administración del bróker;
  - la última ejecución sobre la instancia terminó Ready; y
  - esta ejecución aplica el bróker, así que `--skip-infrastructure` nunca retira el inicio de sesión.

  Cuando se cumple todo, el bróker y la configuración de la instancia dejan de llevar el inicio de sesión
  compartido, con un reinicio más del bróker, y los servicios se renuevan una vez más sobre la configuración
  modificada. El resumen final dice «The shared message broker login is retired.» Cuando algo no se cumple,
  el inicio de sesión se mantiene y el resumen dice «The shared message broker login was NOT retired» y
  nombra la causa; resuélvela y ejecuta `dcctl upgrade` otra vez.
- **No pases `--skip-infrastructure` a ninguna de las dos ejecuciones.** No aplica nada al bróker, así que ni
  añade los inicios de sesión ni retira el compartido.
- **Una instancia nueva nunca tiene un inicio de sesión compartido.** `dcctl bootstrap` crea solo los inicios
  de sesión por área.
- **No se admite volver atrás a través de la segunda actualización.** Consulta
  [Volver atrás](#v0250-going-back).
- **Los servicios comprueban, y el administrador cambia.** Un servicio verifica cada stream y cada bucket que
  abre y se detiene con un error que nombra el stream y en qué difiere cuando falta uno o es incorrecto.
  Ejecuta `dcctl broker reconcile <instance>` para dejarlo como estaba, lo que también recupera un stream o un
  bucket eliminado a mano.
- **Dos comportamientos cambian con ello.** La vida de los buckets de caché queda fijada en cinco minutos,
  porque los crea el job y ya no se lo pregunta a cada servicio; los ajustes de tiempo de vida de la caché de
  `device-management` limitan ahora solo cuánto tiempo conserva una réplica una entrada en su propia memoria.
  Y un número de réplicas aumentado en la configuración de la instancia surte efecto cuando se ejecuta el job,
  en la siguiente actualización, y no en cada reinicio de pod.
- **Las instalaciones solo con el chart** necesitan el espacio de nombres y el Secret de administración en su
  sitio antes de la primera instalación, porque el job se ejecuta antes de que el chart cree nada. El Secret
  se llama `dci-<instance>-nats-broker-admin` y contiene `user` y `password`. `dcctl broker permissions`
  imprime los permisos para un bróker que ejecutas tú mismo.
- **Restaurar desde una copia de seguridad recibe inicios de sesión nuevos.** Consulta
  [Recuperación ante desastres](./disaster-recovery.md).

Consulta el [modelo de seguridad](../concepts/security-model.md) para saber qué protegen y qué no protegen
los inicios de sesión.

##### Los tokens de acceso de los dispositivos se almacenan como digests {#v0250-access-tokens}

El token de una credencial `ACCESS_TOKEN` es su id de credencial, y antes se almacenaba y se devolvía tal como
se enviaba, de modo que una filtración de la base de datos o de una copia de seguridad entregaba una
credencial que funcionaba para cada dispositivo con token. Ahora se almacena como un digest con clave del
mismo tipo que ya lo es la contraseña de `MQTT_BASIC`: un HMAC-SHA-256 sobre el tenant y el token, con una
clave derivada de la clave raíz de la instancia. La forma almacenada es `dctk1$<key id>$<digest>`.

- **Los dispositivos existentes conservan sus tokens.** Ningún dispositivo necesita reaprovisionarse.
- **El token se muestra una sola vez,** en la respuesta a la llamada que lo establece (crear, una
  actualización que lo rota, un reemplazo de dispositivo que genera uno) y en el aviso de la consola, y nunca
  más. Una lectura devuelve la huella, que identifica la credencial y no puede presentarse como el token.
- **Para obtener un token utilizable de una credencial cuyo token no registraste, fija uno nuevo:** **Set new
  token** en la consola, o `updateDeviceCredential` solo con `credentialId`. El token actual del dispositivo
  deja de abrir conexiones en cuanto lo haces. Consulta
  [Contraseñas y tokens que se muestran una vez, y su rotación](../guides/device-credentials.md#show-once).
- **`deviceCredentials(criteria: {credentialId})` acepta el token o la huella.**
- **Cambiar el tipo de una credencial a `ACCESS_TOKEN` o desde `ACCESS_TOKEN` debe nombrar `credentialId` en la
  misma solicitud,** porque el valor almacenado significa un digest con un tipo y un nombre de usuario con el
  otro. Una solicitud que no lo hace se rechaza como `INVALID_VALUE`. Un token de más de 256 bytes se rechaza
  como `LIMIT_EXCEEDED`.
- **Los fragmentos de conexión de la consola** muestran `<access-token>` para una credencial de token de
  acceso, igual que ya mostraban un marcador para una contraseña.
- **Una clave raíz distinta vuelve ajenos todos los digests.** Como ocurre con los digests de contraseña, una
  instancia restaurada junto a una clave raíz equivocada registra un error que indica cuántos digests
  almacenados no produjo su clave.

##### El agente de borde es un gateway {#v0250-edge-gateway}

El agente de borde inicia sesión en la plataforma como un dispositivo propio, un **gateway**, y publica los
eventos de cada dispositivo local en su propio espacio de nombres. La plataforma acepta un evento solo de un
dispositivo asignado a ese gateway. Este es el único modo de enlace ascendente: el antiguo modo de
dispositivo directo, en el que el agente reenviaba al bróker de la nube sin cambios, se ha eliminado.
Consulta [Identidad del gateway](./edge-services.md#gateway-identity) y
[el espacio de nombres del gateway](../reference/device-protocol.md#gateway-namespace).

- **La configuración de un agente debe cambiar.** `agentId` deja de ser una clave, y una que quede detiene el
  agente al arrancar, con un error que nombra `uplink.gatewayToken` como su sustituta. `uplink.tenant`,
  `uplink.gatewayToken`, `uplink.username` (con la forma `{tenant}:{credentialId}`, rechazada al cargar en
  caso contrario) y `uplink.passwordEnv` son obligatorias.
- **Crea el dispositivo gateway y asígnale sus dispositivos antes de que el agente vacíe su cola pendiente.**
  Los eventos guardados en el spool con el modo antiguo se reenvían en el espacio de nombres nuevo, y uno de un
  dispositivo que aún no está asignado se rechaza. El spool y su durable se limitan a `uplink.tenant` en su
  sitio; los eventos guardados de cualquier otro tenant no se reenvían y permanecen en el spool hasta que su
  búfer circular los desaloja.
- **Asigna dispositivos con `assignDeviceGateway`,** o con `gatewayToken` al crear uno, o desde la pestaña
  **Gateway** del dispositivo en la consola. Un dispositivo tiene como máximo un gateway, un gateway no puede
  estar a su vez tras otro gateway, y un gateway que aún tiene dispositivos no se puede eliminar. Requiere
  `device:write` y surte efecto en el siguiente evento del dispositivo.
- **Un evento rechazado no pasa en silencio.** La plataforma lo registra en el stream de eventos fallidos con
  el motivo `NotGatewayChild`, lo cuenta en `resolve_not_gateway_child_total` y envía un aviso al gateway; el
  agente lo registra en el log, lo cuenta y conserva el evento en `refused-events.jsonl` en su directorio de
  almacén.
- **Actualiza primero la plataforma.** Un `event-sources` de la `v0.24.0` que se ejecuta con
  `infrastructure.nats.mqttFailClosed` activado confirma los mensajes de gateway que no puede atribuir y los
  descarta, y el agente ve un éxito. Termina de actualizar todas las réplicas de `event-sources` y de
  `device-management` y el bróker antes de reiniciar ningún agente en esta versión. Un agente cuyo bróker no se
  ha actualizado no puede suscribirse a sus avisos y no informa `uplink_connected`. Consulta la advertencia en
  [Actualizaciones progresivas](#rolling-upgrades).
- **Los comandos a dispositivos tras un gateway aún no se enrutan.** El agente reenvía solo eventos.

##### El SDK de C# {#v0250-csharp-sdk}

El SDK no reconocía una conexión rechazada. La biblioteca MQTT informa de un CONNACK que no es un éxito
lanzando una excepción, y el SDK lo trataba como un fallo de transporte corriente, de modo que el bucle de
reconexión de una sesión seguía reintentando una credencial que el bróker había rechazado como si la red
estuviera caída. Ahora lanza `MqttConnectRefusedException` para un CONNACK que juzgó al cliente (versión de
protocolo no admitida, identificador de cliente no válido, nombre de usuario o contraseña incorrectos, no
autorizado, vetado, método de autenticación incorrecto), y el bucle de reconexión de una sesión se detiene
ante ella y declara la sesión ciega. Un CONNACK de «server unavailable» o «server busy», y un socket que
falla a mitad de la conexión, se siguen reintentando. `MqttConnectRefusedException` gana un constructor que
recibe la excepción subyacente, lo que es una adición a la API pública; no se elimina nada.

Qué cambia para un programa de dispositivo: uno que dependía de un reintento indefinido tiene ahora que
manejar el rechazo, por ejemplo tras revocarse o sustituirse su credencial, o tras fijarse de nuevo su token
de acceso.

##### La primera versión firmada {#v0250-signing}

Cada imagen de servicio, el operador, el agente de borde, el administrador del bróker y la consola, y el chart
de Helm, están firmados con cosign, sin clave, por digest. Cada imagen lleva además un SBOM SPDX firmado y una
procedencia de compilación SLSA, y el chart lleva procedencia. Una firma queda ligada al flujo de trabajo de
versiones y a la etiqueta, así que una de cualquier otro flujo de trabajo, repositorio o rama no se verifica
como una versión. La versión de GitHub no se publica hasta que `hack/verify-release-signatures.sh <tag>` ha
pasado contra lo que se subió.

- **Para comprobar lo que descargaste,** sigue [Verificar las versiones](./verifying-releases.md); el script
  necesita `cosign` 3 o posterior, `jq` y la CLI de GitHub, y ninguna cuenta ni clave nuestra.
- **Las versiones anteriores a la `v0.25.0` no llevan firma,** y verificar una falla con «no signatures found».
- **La imagen de la base de datos no está cubierta.** La imagen operand que ejecuta el almacén de eventos la
  publica un flujo de trabajo aparte y no está firmada en esta versión.
- **Los archivos de `dcctl` y del agente de borde** llevan procedencia de compilación, como desde la
  [`v0.18.0`](https://github.com/devicechain-io/devicechain/blob/v0.18.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0180-provenance).

No hay nada que hacer para una actualización. Consulta la
[política de seguridad](https://github.com/devicechain-io/devicechain/security/policy) para saber cómo
informar de un problema.

##### Cuentas de servicio {#v0250-service-accounts}

Un administrador de tenant puede crear **cuentas de servicio** en **Settings**, junto a **Members**, para
scripts e integraciones que no deban depender del token de actualización de una persona. Una cuenta tiene
exactamente los roles que se le dan (sin rol base de visor), opcionalmente limitada a algunos clientes o
grupos, y obtiene un token de acceso corriente válido durante cinco minutos con la concesión OAuth
`client_credentials`. Un secreto se muestra una vez, se almacena como un digest con clave y caduca (365 días
por defecto, 730 como máximo); rotar uno deja funcionando al antiguo durante un solapamiento que eliges, 24
horas por defecto. Un tenant puede tener 200 cuentas, y una cuenta no puede administrar el tenant. Consulta
[Cuentas de servicio](../user-guide/service-accounts.md). El cliente OAuth de un operador no puede usar esta
concesión, y las credenciales de una cuenta de servicio no autentican nada en las demás concesiones.

##### Despliegues en grupo de actualizaciones over-the-air {#v0250-rollouts}

`createUpdateRollout`, `startUpdateRollout`, `pauseUpdateRollout`, `resumeUpdateRollout` y
`abortUpdateRollout` asignan un artefacto a los miembros de un grupo de dispositivos, con nunca más de
`maxInFlight` dispositivos (de 1 a 1.000) con una actualización sin terminar, y **Updates → Rollouts** en la
consola los muestra. Iniciar un despliegue congela los miembros del grupo; un grupo de más de 10.000
dispositivos se rechaza. Un dispositivo que las reglas de asignación rechazan se omite con el código de esa
regla y no ocupa ningún hueco. Un umbral de fallos opcional pausa el despliegue por sí solo, y reanudarlo
entonces exige un umbral mayor. Abortar cancela los dispositivos que aún no han empezado y deja terminar a los
que están en curso. Los despliegues necesitan `artifact:write` para modificarse y `artifact:read` para verse, y
un miembro limitado a una parte del tenant es rechazado. Consulta
[Desplegar en un grupo](../concepts/over-the-air-updates.md#rollouts). Las métricas nuevas son
`update_rollout_dispatches_total`, `update_rollout_transitions_total` y
`update_rollout_step_failures_total`.

#### Qué más cambió {#v0250-what-changed}

##### Limitaciones conocidas {#v0250-known-limitations}

- **Tras la primera actualización, todos los pods pueden leer aún el inicio de sesión compartido.** Esa es la
  ventana entre las dos actualizaciones; ejecuta la segunda sin demora.
- **Dos permisos del bróker son más amplios por diseño:** `event-sources` conserva derechos de actualización
  sobre los almacenes MQTT propios del bróker, y `user-management` puede purgar los datos de un tenant
  eliminado en todos los streams, salvo en los consumidores en los que `event-sources` mantiene su durabilidad
  de mensajes confirmados.
- **Los comandos de un gateway no se enrutan,** un aviso a un gateway es de mejor esfuerzo, y el agente guarda
  una copia acotada (8.192 eventos y 16 MiB) de lo que reenvió para contrastarla con un aviso.
- **Los despliegues no tienen oleadas ni etapa canario,** ni programación, ni alerta para el proceso que impulsa los
  despliegues si se detiene.
- **El SBOM se genera a partir de la variante `amd64` de cada imagen.**
- **Con `mqttFailClosed` desactivado, que es el valor por defecto, se pueden perder mensajes QoS 1 que el
  bróker ya confirmó mientras el stream de captura MQTT cambia de líder, con cualquier carga,** y nada los
  cuenta (`JetStreamCaptureLeaderChanged` avisa de que hubo un cambio). También se pueden perder sin ningún cambio de líder, con
  carga sostenida: en una prueba de carga en una instalación por defecto, `event-sources` se quedó atrás leyendo el stream de captura,
  el stream se llenó hasta su límite y descartó sus mensajes sin leer más antiguos, y se perdieron 1.357.188 eventos confirmados,
  entre el 7 y el 13 % del tráfico MQTT. `devicechain_<area>_jetstream_consumer_unread_skipped_total` los cuenta y
  `JetStreamDurableLostUnread` alerta. Aún se investiga por qué `event-sources` se quedó atrás. Un reinicio progresivo del bróker
  reinicia todos los servidores, también el líder, y esta actualización reinicia el bróker dos veces, así que
  cuenta con una breve ventana de pérdida en cada reinicio del bróker salvo que fail-closed esté activado. En
  un ensayo de esta actualización con cuatro dispositivos que enviaban 20 mensajes QoS 1 por segundo entre
  todos, se perdieron 11 de 11 262 mensajes confirmados, todos durante los reinicios del bróker; en un ensayo
  anterior no se perdió ninguno. Para cerrar la ventana, activa `infrastructure.nats.mqttFailClosed`. `dcctl`
  no tiene una opción para ello en esta versión y `--values` no lo acepta, así que un valor puesto a mano en
  una instancia que gestiona `dcctl` lo sobrescribe el siguiente `dcctl upgrade`; una versión posterior añade
  `--mqtt-fail-closed` a `dcctl bootstrap` y `dcctl upgrade`, que registra la elección en la instancia
  (consulta [MQTT con fail-closed](./observability.md#mqtt-fail-closed)). La contrapartida: mientras el almacén de mensajes del bróker no
  tiene líder, una publicación QoS 1 no se confirma, así que el dispositivo debe volver a enviarla, y la
  instancia sostiene menos tráfico que con el ajuste desactivado. En un clúster de pruebas no perdió nada
  mientras daba abasto, a 6 000 y 9 000 eventos por segundo con cambios de líder repetidos; a 12 000 por
  segundo se quedó atrás, dos servidores del bróker se quedaron sin memoria y se perdieron mensajes
  confirmados. Dimensiona la instalación para mantenerte por debajo de su techo. Consulta
  [MQTT con fail-closed](./observability.md#mqtt-fail-closed).
- **Heredadas de la `v0.24.0`:** el relleno de una métrica calculada omite el historial comprimido, y una
  caída del bróker con una gran cola pendiente no está cubierta por `mqttFailClosed`.

##### También en esta versión {#v0250-also}

- **`dcctl bootstrap` y `dcctl upgrade` aceptan `--values`,** un archivo YAML de ajuste (número de réplicas,
  recursos, la configuración propia de un área) que se registra en la instancia y se vuelve a aplicar en cada
  actualización posterior. Una clave que posee `dcctl`, como una imagen, una credencial o un endpoint, se
  rechaza por su nombre antes de cambiar nada. `--reset-values` lo elimina, y `--dry-run` imprime qué
  cambiaría. Consulta [Ajustar una instancia](./tuning-an-instance.md).
- **`dcctl broker reconcile <instance>` y `dcctl broker permissions`** son nuevos; consulta más arriba.
- **El registro de auditoría de una entidad se guarda en la misma transacción que el cambio.** Antes se
  escribía después de que el cambio se confirmara, así que un fallo al escribir la auditoría le decía al
  llamante que el cambio había fallado aunque se había guardado, y lo dejaba sin registrar. Ahora se guardan
  ambos o ninguno, lo que significa que un fallo al escribir la auditoría hace fallar el cambio. Los eventos de
  inicio de sesión y de autenticación siguen siendo de mejor esfuerzo.
- **La eliminación de un tenant muestra qué la retiene.** `tenant_purge_store_blocked{store,reason}` cuenta las
  eliminaciones abiertas por sistema de almacenamiento, junto con `tenant_purge_store_rows_erased_total`,
  `tenant_purge_store_erase_duration_seconds`, `tenant_purge_completed_total`, `tenant_purge_failures_total`,
  `tenant_purge_duration_seconds` y `tenant_purge_clean_duration_seconds`, y tres alertas. Consulta
  [Eliminación de inquilinos](./tenant-deletion.md#blocked-store).
- **La eliminación de un tenant es más rápida en dos puntos.** El borrado de clave-valor ejecuta 32 purgas a la
  vez en lugar de una, y el borrado de objetos de S3 se hace por lotes, de hasta 1.000 claves por solicitud. En
  una prueba de loopback de un solo nodo, borrar 63.000 claves pasó de 28,9 s a 3,1 s. Un lote en el que
  cualquier clave no se llega a borrar hace fallar ahora la pasada, que se repite hasta que no queda nada.
- **Un escritor sigue una elección de líder hasta ponerse al día.** Un publicador que enruta al líder de un
  stream calculaba antes el nuevo líder una sola vez, y conservaba el destino antiguo si esa pasada fallaba.
  Ahora reintenta con una espera creciente, durante 30 segundos como máximo. Esto afecta solo al rendimiento.
- **Paneles.** Un panel puede mostrar una ventana fija, elegida con **Custom range** o abierta desde un enlace
  `?from=&to=`, en lugar del intervalo móvil (consulta
  [Intervalo personalizado](../user-guide/viewing-dashboards-in-dash.md#custom-range)). Un widget nuevo de
  tabla de historial lista el historial almacenado de un dispositivo como filas de promedios por intervalo, y
  un widget que tiene algo que entregar tiene un botón de descarga para CSV o PNG. Un widget sin nada que
  dibujar dice ahora cuál de cinco motivos es: ningún dispositivo elegido, no informado por este dispositivo,
  sin datos en este intervalo, sin acceso, o una fuente fallida. `/dash?rotate=tokA,tokB&every=60` recorre de
  dos a 24 paneles en una pantalla de pared, de 10 a 3.600 segundos cada uno (consulta
  [Rotar entre varios paneles](../user-guide/viewing-dashboards-in-dash.md#rotating-boards)). Todo ello es
  aditivo: un panel guardado se lee y se renderiza como antes.
- **Documentos de versionado y de seguridad.** [Política de versionado y actualización](./versioning-policy.md)
  dice qué es estable desde la `v1.0.0`, y el [modelo de seguridad](../concepts/security-model.md) describe
  los límites de confianza y qué hace cumplir cada uno. La sección de actualizaciones progresivas ya no afirma
  que no haya tiempo de inactividad.
- **Una comprobación de compatibilidad se ejecuta en cada cambio.** Cada esquema GraphQL servido, los comandos
  y las opciones de `dcctl`, el contrato de cable de dispositivo, la disposición de topics de dispositivo, la
  API pública del SDK de C# y las exportaciones de los paquetes npm se comparan con la última versión, y una
  ruptura debe declararse. Frente a la `v0.24.0` no se declara ninguna ruptura en esta versión.
- **El reproductor de demostración del showcase Sitepulse fija un token propio** en la credencial de cada
  dispositivo, porque ya no podía leer uno.

### v0.24.0 — métricas calculadas que puedes crear tú mismo, y una ruta de eventos que transporta más {#v0240-upgrade}

`v0.24.0` es una actualización en su sitio desde `v0.23.0`: `dcctl install` para el clúster y después
`dcctl upgrade` para cada instancia, como se describe [para la v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a
`dcctl install` los ajustes con los que se instaló el clúster, como `--ha` y `--database-node-selector`,
además de la nueva `--version`. **No se puede alcanzar en su sitio desde la `v0.22.0` ni versiones
anteriores: actualiza primero a la `v0.23.0`.** Sus cuatro migraciones añaden dos tablas y dos índices
y no reescriben ninguna fila existente (consulta [Durante la actualización](#v0240-during)). **Desde ella
se avanza, no se vuelve atrás:** la `v0.23.0` no puede arrancar con la configuración que escribe esta
versión (consulta [Volver atrás](#v0240-going-back)). Si vienes de
la `v0.22.0` o anterior, lee también las [notas de la `v0.23.0`](https://github.com/devicechain-io/devicechain/blob/v0.23.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0230-upgrade); se aplican a cualquier
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
     `devicechain_eventsources_capture_body_credential_strip` vale `1`. En una instalación con el chart (no con `dcctl`), indica
     en su lugar `functionalAreas.event-sources.config.capture.bodyCredential: strip`.
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
  conserva el valor. `strip` era transitorio y se elimina en la `v0.26.0`: desde esa versión una credencial en el cuerpo se
  rechaza siempre, la opción `--mqtt-body-credential` ya no existe y `event-sources` no arranca mientras siga indicado
  `capture.bodyCredential`. Elimina la clave antes de actualizar a la `v0.26.0`.

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
| `event-sources` `capture.bodyCredential` (nuevo, fijo, en `functionalAreas.event-sources.config`) | ninguno: se aceptaba una credencial en el cuerpo en el bróker MQTT de la plataforma | `refuse` | Eliminado en la `v0.26.0`: una credencial en el cuerpo se rechaza siempre. Consulta [Los dispositivos deben dejar de enviar la credencial en el cuerpo por MQTT](#v0240-mqtt-body-credential). |
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

## Versiones anteriores {#older-releases}

Cuando se publique una nueva versión menor, la versión detallada más antigua pasará a esta tabla; cada enlace de Detalles abre las notas completas de esa versión tal como estaban en su etiqueta.

| Versión | Resumen | Acción necesaria | Detalles |
| --- | --- | --- | --- |
| <a id="v0230-upgrade"></a>v0.23.0 | usuarios de cliente con acceso acotado, y una ingesta MQTT que pierde menos | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.23.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0230-upgrade) |
| <a id="v0220-upgrade"></a>v0.22.0 | paneles sobre una cuadrícula de celdas cuadradas, y una ruta de eventos que espera menos | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.22.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0220-upgrade) |
| <a id="v0211-upgrade"></a>v0.21.1 | un parche, nada que hacer | No | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.21.1/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0211-upgrade) |
| <a id="v0210-upgrade"></a>v0.21.0 | un dispositivo puede quedar excluido por completo, y las actualizaciones se firman | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.21.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0210-upgrade) |
| <a id="v0200-upgrade"></a>v0.20.0 | inicia sesión con tu propio proveedor de identidad; los widgets se distribuyen como paquetes | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.20.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0200-upgrade) |
| <a id="v0190-upgrade"></a>v0.19.0 | dimensionada a partir de lo medido a 6000 eventos por segundo; un stream lleno rechaza | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.19.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0190-upgrade) |
| <a id="v0180-upgrade"></a>v0.18.0 | lo que fallaba en silencio ahora lo dice, y la ingesta sigue el ritmo de su techo | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.18.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0180-upgrade) |
| <a id="v0170-upgrade"></a>v0.17.0 | las instancias dejan de ser dueñas del clúster en el que se ejecutan | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade) |
| <a id="v0160-upgrade"></a>v0.16.0 | los dispositivos deben nombrar el despacho que responden | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.16.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0160-upgrade) |
| <a id="v0150-upgrade"></a>v0.15.0 | las actualizaciones dejan de borrar lo que no envió | No | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.15.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0150-upgrade) |
| <a id="v0140-upgrade"></a>v0.14.0 | los paquetes contra los que compila | No | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.14.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0140-upgrade) |
| <a id="v0130-upgrade"></a>v0.13.0 | los límites de geocercas pasan a formar parte de su plan | No | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.13.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0130-upgrade) |
| <a id="v0121-upgrade"></a>v0.12.1 | un parche, nada que hacer | No | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.12.1/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0121-upgrade) |
| <a id="v0120-upgrade"></a>v0.12.0 | una actualización que cambia contratos | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.12.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0120-upgrade) |
| <a id="v0110-upgrade"></a>v0.11.0 | de nuevo una actualización normal | No | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v0.11.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0110-upgrade) |

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
