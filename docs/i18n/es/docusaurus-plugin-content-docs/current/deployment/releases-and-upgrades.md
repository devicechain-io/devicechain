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

:::warning Esta sección describe las versiones anteriores a la v1.0.0

`v1.0.0` es la primera versión estable, y la [política de versiones y actualización](./versioning-policy.md) se aplica desde ella. Hasta la **v1.0.0**, cualquier versión —incluida una versión de parche— puede cambiar APIs, esquemas o
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

Dé a `dcctl install` los mismos ajustes con los que se instaló el clúster, como `--ha` o `--compact`. Se niega a cambiarlos mientras una instancia se ejecuta en el clúster; consulte [Volver a ejecutar install](./bootstrap.md#re-running-install).

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

Las credenciales que diste a `dcctl` con un archivo, las [claves precompartidas de LwM2M](./edge-services.md#lwm2m-identities) y las [contraseñas del broker de Sparkplug](./edge-services.md#sparkplug-sources), se guardan en Secrets que `dcctl` escribe él mismo, fuera de la versión de Helm. Una actualización no los toca y no lee nada secreto de la versión. La primera actualización de una instancia aprovisionada con claves LwM2M por una versión anterior las traslada a su propio Secret antes de que cambie la versión; las revisiones anteriores del historial de Helm siguen teniendo los valores antiguos, así que rota las claves después si eso te importa. Los ajustes que pusieras a mano en el bloque `lwm2m-ingest` de la versión ya no se conservan entre actualizaciones (solo las identidades y su proyección); ponlos en un [archivo de valores](./tuning-an-instance.md) indicado con `--values`. Quitar una clave, o todo el Secret en una retirada, espera a que la release haya terminado bien, así que una actualización fallida nunca deja un pod que sigue en ejecución sin una clave que lee cuando su contenedor se reinicia; si la release termina bien y solo falla esa limpieza, `dcctl` avisa con el comando que la termina.

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
Antes de equilibrar, el stream que transporta cada evento resuelto se aparta del servidor que lidera la captura de dispositivos o el stream de entrada, que el equilibrado por sí solo podía dejar en un mismo servidor. El resumen termina indicando dónde se lideran esos streams: el de eventos resueltos aparece como "separated"; como "SHARED" cuando ninguna réplica al día está en un servidor libre (menos servidores que streams de este tipo, un servidor libre que no tiene réplica de él, como con cinco servidores y tres réplicas, o réplicas retrasadas); y como "NOT CONFIRMED" cuando el movimiento falló o se omitió. Los streams de captura y de entrada nunca se mueven, así que si un mismo servidor lidera ambos, la línea lo indica como información, y se resuelve en el siguiente reinicio del broker.

### Qué más comprueba una actualización {#upgrade-checks}

Dos cosas viajan con ella, porque un cambio de versión es lo que le ocurre de forma fiable a
una instancia en funcionamiento, y un calendario no.

**El certificado del bróker.** El bróker de mensajería sirve un certificado válido durante un
año, emitido por una autoridad que `dcctl` acuña en el arranque inicial y conserva en el
clúster. Una actualización vuelve a emitir ese certificado cuando está dentro de sus últimos 30
días, o cuando ya no cubre todos los nombres por los que los bróker se llaman entre sí —que es
lo que le hace escalar una instancia a un certificado que, por lo demás, sigue holgadamente en
vigor. La reemisión se hace bajo la **misma** autoridad, así que nada tiene que volver a
confiar en nada. Cuando solo cambia el certificado, el bróker **no** se reinicia: intercambia el
certificado en caliente, y la actualización espera, hasta cinco minutos, a que todos los bróker
informen del vencimiento del certificado nuevo. Si un bróker no ha cambiado en ese tiempo, la
actualización avisa y reinicia los bróker para que ninguno siga sirviendo el certificado
antiguo. Los bróker se reinician de inmediato cuando cambia algo más que el certificado: la
autoridad o el número de servidores. Si la actualización cambia además la imagen o la
configuración del bróker, ese cambio ya los reinicia. Fuera de esas condiciones la comprobación
se ejecuta y no hace nada.

Ese certificado lo acuña y lo renueva `dcctl`, no cert-manager, que emite los certificados web
de la instancia y no interviene en el del bróker. El certificado del bróker está en el Secret
`dc-nats-tls` del namespace de la instancia, y es `dcctl upgrade` quien lo reescribe. Un bróker
al que se entrega un certificado nuevo bajo la misma autoridad lo sirve a las conexiones nuevas
sin recargar su configuración, así que el cambio de certificado no corta las sesiones MQTT que
ya mantiene. Un reinicio que sí haga la actualización es un reinicio progresivo corriente, un
bróker cada vez, y reconecta los dispositivos como cualquier reinicio del bróker.

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

### Sobres de eventos {#event-envelopes}

Un sobre de eventos lleva varios eventos de un mismo tenant en un único mensaje del flujo
resolved-events (y, para los eventos tal como se recibieron, del flujo inbound-events), en lugar de
un mensaje por evento. Reduce el trabajo que hace el bróker por evento. Lo activan dos ajustes, y
**ambos están desactivados por defecto**: `infrastructure.messaging.envelopes.resolved`
(device-management escribe los eventos resueltos como sobres) e
`infrastructure.messaging.envelopes.inbound` (event-sources escribe los eventos de entrada como
sobres, esperando como máximo `inboundLingerMillis`, 5 por defecto y 10 como máximo, a un segundo
evento de un tenant con mucho tráfico). Se establecen en un archivo de valores bajo
`instance.config.infrastructure.messaging.envelopes`; consulte [Ajustar una instancia](./tuning-an-instance.md).
Leer no es un interruptor: desde `v1.1.0` todos los servicios leen sobres y mensajes de un solo
evento por igual, sean cuales sean los valores; los valores solo deciden si un servicio los
escribe. Todas las reglas siguientes valen para cualquiera de los dos y para ambos.

- **Cuándo es seguro activarlo.** Cuando todos los pods de la instancia ejecutan `v1.1.0` o
  posterior, y no antes. Un pod de una versión anterior no puede leer un sobre y lo aparta como
  fallido, y los eventos que contiene se pierden para ese pod. Actualice primero, espere a que todos
  los pods se hayan renovado y entonces establezca el valor.
- **Qué hace el motor de detección con un duplicado.** En casos raros, como una confirmación
  perdida durante un reintento, un evento llega dos veces al flujo. El almacenamiento de eventos y
  el estado de los dispositivos ya lo absorben. Con el valor activado, el motor de detección
  también recuerda la identidad de cada evento que aplicó durante los últimos diez minutos
  aproximadamente de tiempo del bróker (el intervalo de reentrega de la propia plataforma), omite
  una segunda copia y la cuenta en `detect_duplicate_events_skipped_total`. Una tasa sostenida
  distinta de cero significa que algo aguas arriba está almacenando eventos dos veces. Con el valor
  desactivado el motor de detección no guarda esa memoria y su coste y comportamiento no cambian.
  La identidad es la que usa el almacenamiento de eventos, de modo que dos lecturas idénticas de un
  mismo dispositivo en el mismo nanosegundo se cuentan una sola vez en ambos.
- **Qué cuesta.** Con unos 20 000 eventos por segundo la memoria guarda unos 12 millones de
  identidades, unos 540 MB (extrapolado), solo en el líder de detección (un suplente la construye
  al asumir el cargo). Medido, añadió unos 190 MB con 6 000 eventos por segundo y unos 300 MB con
  12 000, aproximadamente 45 a 55 bytes por identidad; con 5 000 eventos por segundo, calcule unos
  150 MB. Con un ajuste activado, dimensione `event-processing` como se describe en
  [Dimensionamiento de los servicios](./bootstrap.md#service-sizing). Por encima de unos 25 000 eventos por segundo
  se aplica un tope y la memoria cubre menos de diez minutos (unos 2,7 minutos con 100 000 por
  segundo). Tras un reinicio o un cambio de líder, el motor de detección vuelve a leer los últimos
  diez minutos del flujo antes de continuar, así que ese paso tarda más en un flujo con mucho
  tráfico; se informa en `detect_guard_prime_seconds` y `detect_guard_prime_events_total`, y
  `detect_guard_prime_partial_total` cuenta las veces que el flujo ya no llegaba tan atrás.
- **Qué no cubre.** Una copia que llega más de diez minutos después de la primera, por ejemplo
  cuando se recrea un consumidor de entrada y vuelve a entregar desde su inicio, el motor de
  detección la aplica una segunda vez, lo que puede contarla dos veces en una regla de conteo o de
  ventana.
- **Desactivarlo.** Establezca el valor en `false`. Los sobres que ya están en el flujo permanecen
  hasta que caducan o se consumen, y se siguen leyendo.
- **Pasar a una versión anterior a `v1.1.0`.** `dcctl upgrade` registra cuándo se activa el valor
  y, cuando se desactiva, registra la posición del mensaje más reciente de los flujos de entrada y
  resuelto. Pasar a una versión anterior a `v1.1.0` se rechaza mientras el valor esté activado,
  mientras alguno de los flujos conserve un mensaje anterior a la desactivación, mientras falte el
  registro de cuándo se desactivó (ejecute `dcctl upgrade` una vez más en la versión actual para
  crearlo) y cuando se encuentra un sobre en una muestra de los flujos (el mensaje retenido más
  antiguo y los 256 más recientes de cada uno, lo que también detecta un valor cambiado fuera de
  `dcctl`). También se niega cuando no puede comprobarlo o cuando el destino no es una etiqueta de
  versión. Una ejecución que no cambia de versión nunca se rechaza. `--allow-envelope-downgrade`
  omite el rechazo con una advertencia: un servicio de la versión anterior aparta un sobre como
  fallido y los eventos que contiene se pierden para ese servicio.

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

### v1.2.0 {#v120-upgrade}

`v1.2.0` es una actualización in situ, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Los valores de Helm que cambia esta versión no están cubiertos por la política de compatibilidad, y esta sección es donde una versión menor lo indica.

Qué requiere acción:

- **Tenga en cuenta que dos límites de memoria son más altos.** `device-management` tiene ahora un límite de 512Mi (antes 256Mi) y `event-processing` tiene un límite de 768Mi (antes 384Mi) mientras cualquiera de los ajustes de sobres de eventos esté activado. Nada cambia para `event-processing` con ambos ajustes desactivados. Las solicitudes de memoria no cambian, así que la planificación es la misma; cada límite es un techo que el pod puede usar. Consulte [Límites de memoria](#v120-memory).
- **Revise cualquier límite de memoria que haya fijado usted.** Un límite fijado en `device-management` o `event-processing` con `functionalAreas.<servicio>.resources.limits.memory` se usa tal cual, igual que un `resources.limits.memory` superior de nivel superior. Si el suyo está por debajo de las cifras de [Dimensionamiento de los servicios](./bootstrap.md#service-sizing) para su flota o su ritmo de eventos, súbalo.
- **Tenga en cuenta que `device-management` guarda más de la flota en memoria, y que su límite de memoria es 768Mi, no los 512Mi de arriba.** Las cachés por dispositivo (`inMemoryCache.perDeviceCacheMiB`) son de 80 MiB cada una, antes 24, porque 24 MiB solo guardaban de 8.000 a 10.000 dispositivos. Cada réplica usa ahora más memoria con una flota de ese tamaño o mayor. Si fija usted el límite de memoria de `device-management`, compárelo con la regla de [Dimensionamiento de los servicios](./bootstrap.md#service-sizing). Si fija usted `perDeviceCacheMiB`, su valor se usa tal cual. Consulte [Dimensionamiento de cachés y bróker](#v120-sizing-defaults).
- **Con sobres de eventos activados y 16.000 eventos por segundo o más, fije `event-processing` en unos 1Gi.** El mínimo de 768Mi no crece con el ritmo. Consulte [Dimensionamiento de cachés y bróker](#v120-sizing-defaults).
- **Aumente `nats_memory_limit` si el conjunto de trabajo de un servidor del bróker está en su límite blando,** que los 4Gi por defecto alcanzan desde unos 12.000 eventos por segundo. Un servidor en ese punto va lento, no se reinicia. Consulte [Dimensionamiento de cachés y bróker](#v120-sizing-defaults).
- **`dcctl bootstrap` ahora rechaza el estado local que pertenece a otro clúster.** Incluye `--dry-run`. Si reconstruyó un clúster fuera de `dcctl` y reutiliza un nombre de instancia, mueva antes `~/.devicechain/instances/<nombre>`; vea [los detalles](#v120-stale-state).
- **Compruebe si algo solicita tokens para un recurso que esta instancia no sirve.** Por defecto, esta versión cuenta esas solicitudes y las llamadas a la API que usan sus tokens, pero no rechaza ninguna. `v1.3` las rechaza por defecto. `infrastructure.tokenAudience.enforcement: observe` conserva el comportamiento de esta versión durante esa versión menor, y `v1.4` rechaza `observe` al arrancar y en el esquema de valores del chart. Consulte [Los tokens se comprueban contra su recurso](#v120-token-audience).
- **Haga que las solicitudes `client_credentials` que indican un `resource` indiquen también un `scope`.** Una solicitud así se cuenta y se registra en esta versión y sigue funcionando. Solo se rechaza en la próxima versión mayor. Consulte [Los tokens se comprueban contra su recurso](#v120-token-audience).
- **Si escribe usted mismo la configuración de la instancia** (`instance.existingSecret`), añada `infrastructure.tokenAudience`. Cuando el área MCP está desplegada y el documento no incluye su recurso, `dcctl upgrade` muestra un aviso con el bloque que hay que añadir, y se niega a continuar cuando `enforcement` es `enforce`. Consulte [Los tokens se comprueban contra su recurso](#v120-token-audience).
- **Sepa que un pod de la `v1.1` reiniciado durante la actualización progresiva no arrancará.** La configuración de la instancia ahora lleva `infrastructure.tokenAudience`, y los servicios de la `v1.1` rechazan al arrancar una clave que no conocen. Un pod de la `v1.1` que se reprograme, se expulse o se reinicie después de que cambie la configuración y antes de que la actualización lo sustituya no arrancará hasta que lo sustituya un pod de la `v1.2`; deje avanzar la actualización y no la detenga a medias. Los pods que ya se estaban ejecutando siguen funcionando. Consulte [Los tokens se comprueban contra su recurso](#v120-token-audience).
- **Si fija usted mismo la `resourceUrl` del servidor MCP, escriba su esquema y su host en minúsculas** antes de la aplicación obligatoria. Consulte [Los tokens se comprueban contra su recurso](#v120-token-audience).
- **Espere que los cambios de perfil y de tipo de dispositivo lleguen tarde a las réplicas actualizadas mientras dura el despliegue.** Consulte [Cambios de perfil durante una actualización progresiva](#v120-profile-cache).
- **Tenga en cuenta que los uplinks de los tipos de dispositivo con un decodificador de carga útil esperan en un flujo pequeño.** Consulte [Uplinks con decodificador e interrupciones de device-management](#v120-decoder-stream).
- **Atienda un nuevo motivo de evento fallido, `DECODE_FAILED`.** Un cliente que use un `switch` exhaustivo sobre `FailedEventReason` verá un valor más. Consulte [Un nuevo motivo de evento fallido](#v120-decode-failed).
- **Espere que un decodificador de carga útil que falla deje de llenar los eventos fallidos.** Para cada tipo de dispositivo, cada réplica de `device-management` registra los primeros 20 fallos de decodificación y después 10 por minuto; el resto se confirma (ack) y se cuenta como suprimido, sin registrarse. Hay dos alertas nuevas, `PayloadDecoderFailuresHigh` y `PayloadDecoderSuppressing`: añádalas a su enrutamiento de alertas. Consulte [Un decodificador de carga útil que falla tiene un presupuesto](#v120-decoder-budget).
- **Sepa que `DeviceState.inactivityAlarmTime` vuelve a ser lo que era antes de `v1.1.0`.** `v1.1.0` lo derivaba de `lastDisconnectTime`, de modo que informaba una hora para un dispositivo que pasó a desconectado por un informe de desconexión y nunca quedó en silencio. Vuelve a ser el instante en que el barrido de inactividad marcó el dispositivo como inactivo, y es nulo para un dispositivo que el barrido no ha marcado o que vuelve a estar activo. El campo sigue obsoleto, se elimina en la próxima versión mayor, y `lastDisconnectTime` sigue siendo el campo que hay que usar. El primer arranque de `v1.2.0` repara las filas que dejó `v1.1.0`: borra un valor residual en un dispositivo activo y rellena el valor desde `lastDisconnectTime` en un dispositivo inferido que está inactivo y no lo tiene. Un dispositivo que un pod de `v1.1.0` marque como inactivo mientras el despliegue sigue en curso responde el instante en que ese pod lo marcó, leído de `lastDisconnectTime`, como lo habría hecho `v1.0.0`. Una segunda migración repasa después cualquier fila que un escritor de `v1.1.0` mantuviera bloqueada mientras corría la primera, reintentando durante unos treinta segundos. Una fila que siga bloqueada después se registra y se deja, y el campo responde correctamente por ella al leerse.
- **Publique el decodificador de payload de un perfil solo cuando todas las réplicas de `device-management` ejecuten esta versión.** Una versión publicada mientras una réplica anterior sigue atendiendo puede quedarse sin su decodificador; consulte [Decodificadores de payload](#v120-payload-decoders).
- **Tenga en cuenta que las listas de revocación de certificados X.509 son nuevas y opcionales.** No hay que hacer nada para actualizar; lea [Listas de revocación](#v120-x509-crl) antes de subir una.
- **Compruebe el espacio libre del almacén de eventos antes de actualizar.** En una instancia existente, `dcctl upgrade` sube el `max_wal_size` del almacén de eventos a una quinta parte de su volumen (6,4 GiB con los 32Gi predeterminados, frente a 1 GiB), y el registro de escritura anticipada puede crecer ahora hasta ese tamaño, o más si el archivado se retrasa, en el volumen de datos. La actualización imprime un aviso cuando el valor sube y advierte si el volumen tiene menos espacio libre que el nuevo tamaño. Para conservar la asignación anterior, ejecute `dcctl upgrade --event-store-max-wal-mib 1024`. Consulte [Asignación del registro de escritura anticipada del almacén de eventos](#v120-event-store-wal).
- **Si grafica los agentes de borde con el tablero incluido, rásquelos con el nombre de job `dc-edge-agent`.** Los paneles de proceso del tablero (memoria, CPU, goroutines, archivos abiertos) ahora seleccionan ese job, de modo que otro job en la misma máquina ya no suma sus propias series de proceso a los paneles de un agente. Con otro nombre de job esos paneles aparecen vacíos. Véase [Tablero del agente de borde y el job de rascado](#v120-edge-board-job).
- **`dcctl sim create` ahora rechaza un nivel existente que no declara un límite que debía cumplir.** Con `--tier` que nombra un nivel que ya existe, `dcctl` solo sube los límites que están por debajo de lo que piden el escenario o `--tier-config`, y no puede ver un valor por defecto que el nivel hereda. Hasta ahora un límite que el nivel omitía se leía como 0 y se fijaba en el mínimo, lo que podía sustituir un valor mayor heredado; ahora el nivel se rechaza y se nombra la clave que falta. Añada la clave al nivel (la mutación `updateTenantTier` de la API de administración de user-management) o elija un nivel que la declare. Una ejecución de `dcctl sim create --tier-config` en la `v1.1` podía escribir esas claves en un nivel existente, como describen las notas de la `v1.1.0`; desde esta versión ya no lo hace. Un límite `null` en `--tier-config` también se rechaza; antes se leía como 0. Consulte [Otros cambios](#v120-other-changes).
- **Sepa que `dcctl upgrade` ahora puede fallar con `upgrade incomplete` después de que el broker se reinicie.** Cuando los servidores del broker están listos, la actualización vuelve a leer sus reclamaciones de volumen durante hasta dos minutos y falla si alguna tiene menos capacidad de la que solicita, porque el límite de almacenamiento de archivos del broker sigue a la solicitud. Termine o revierta el cambio de tamaño del volumen (`kubectl -n <namespace> describe pvc`) y ejecute `dcctl upgrade` de nuevo. Consulte [Otros cambios](#v120-other-changes).
- **Tenga en cuenta que revocar un certificado ahora termina las sesiones abiertas a las que afecta.** Revocar una autoridad certificadora puede reconectar de golpe toda una flota. Vea [Revocar un certificado termina las sesiones abiertas](#v120-x509-sessions).
- **Tenga en cuenta que `POST /{instanceId}/{tenant}/events` ahora puede responder `500`.** Un evento descartado al construir su mensaje (una credencial que presentó no se pudo sellar, o el evento no se pudo codificar) se respondía `202` y no se almacenaba en ningún sitio. Ahora se responde `500`, no se almacena, y un reintento falla igual; un cliente que reintentaba toda respuesta distinta de `202` debe dejar de reintentar un `500`. Consulte [HTTP deja de responder 202 a un evento descartado](#v120-http-build-dropped).

- **No hace falta hacer nada por la nueva opción `--size`.** `dcctl bootstrap --size perf-12k` aplica un dimensionamiento de alto rendimiento medido, y `dcctl install --size compact` equivale a `--compact`, que se mantiene. Una instancia que no indica tamaño genera exactamente lo que generaba antes. Consulte [Preajustes de tamaño](#v120-size-presets).
- **Sepa que `devicechain_eventprocessing_detect_pending_timers` ahora cuenta las claves armadas.** Antes contaba cada temporizador que una regla de ausencia o de sesión había insertado y que aún no vencía, y crecía con la tasa de eventos; el motor ahora guarda una entrada por dispositivo armado, de modo que el indicador sigue el número de dispositivos y la memoria de las reglas de ausencia y de sesión ya no crece con la tasa de eventos. El nombre y la alerta no cambian; un panel o un umbral de alerta que ajustó a los valores anteriores, más altos, puede bajarse.
- **Enrute sus alertas a una persona.** De fábrica Alertmanager descarta todas las alertas, y una instancia en ese estado ahora muestra `ALERTS NOT ROUTED` en `dcctl instances list` y en su condición `AlertsRouted`. Pase `--alert-webhook`, `--alert-email` o `--alert-config` a `dcctl install` o `dcctl upgrade`, y `--alert-watchdog-webhook` para enterarse de cuándo se detiene el propio sistema de alertas. Un clúster instalado con una versión anterior ejecuta antes `dcctl install` una vez. Consulte [Las alertas llegan a una persona](#v120-alert-routing).

#### Las partes de la detección se ejecutan en paralelo {#v120-detect-shards}

El ajuste `detectShards` de event-processing (1 por defecto, de 1 a 64) divide por clave el estado del motor de detección. En `v1.1.0` las partes se aplicaban una tras otra, de modo que un valor superior a 1 daba los mismos resultados y ninguna aceleración. A partir de esta versión cada parte se ejecuta en su propia goroutine, así que el trabajo propio del motor puede usar varios núcleos. Las detecciones, el reloj del tiempo del evento y el punto de control guardado son los mismos con cualquier valor, por lo que no hay nada que migrar y el ajuste puede cambiarse o revertirse en cualquier reinicio.

- **No hay que hacer nada si dejó `detectShards` en 1.** El motor es el mismo código de antes.
- **Si fijó `detectShards` por encima de 1**, el pod de event-processing puede usar ahora hasta ese número de núcleos para el motor, además del bucle único que lee y planifica los mensajes. Compruebe el límite de CPU del pod antes de contar con ello, y lea [Ejecutar el motor en más núcleos](./detection-engine.md#detect-shards) para ver si el motor es realmente su límite.
- **Hay dos alertas nuevas**: `DetectShardSaturated` (una parte es el límite de la velocidad de detección) y `DetectShardInvariantViolated` (las partes discreparon en un punto de control y este se rechazó). Se instalan con cualquier valor, pero solo pueden dispararse con `detectShards` superior a 1. Añádalas a su enrutado de alertas. Las acompaña un conjunto nuevo de métricas `detect_shard_*` y `detect_barrier_wait_seconds`; las dos sin etiqueta se exportan a 0 con 1.
- **La memoria crece con `detectShards`.** Cada parte retiene hasta 1024 mensajes en cola, así que un pod que ya funciona con un valor superior a 1 puede retener en memoria hasta `detectShards` veces esa cantidad mientras el bucle va por delante de la parte más lenta. Compruebe el límite de memoria del pod antes de contar con el nuevo ajuste.
- **Volver atrás** es un reinicio con el ajuste en el valor anterior. El punto de control es idéntico, así que no hay nada que deshacer.

#### Límites de memoria {#v120-memory}

Ambos límites estaban a un paso de lo que se midió que los servicios ocupan, en una prueba en Google Kubernetes Engine de hasta 16 000 eventos por segundo:

- **`device-management`** mantiene toda la flota de dispositivos en memoria en cada réplica. Su montón de Go fue de 154 MiB con 6 000 dispositivos y de 193 a 218 MiB con 12 000 a 16 000, frente a un límite de 256Mi cuyo límite blando (`GOMEMLIMIT`) es de 192 MiB. Una flota de unos 12 000 dispositivos alcanzaba por tanto el límite blando, y una mayor corría el riesgo de que el pod fuera terminado por superar su límite. El chart fija ahora el mínimo en 512Mi (límite blando de 384 MiB).
- **`event-processing` con sobres de eventos activados** guarda la protección contra duplicados descrita en [Sobres de eventos](#event-envelopes). Usó de 265 a 269 MiB con 4 500 a 6 000 eventos por segundo y 388 MiB con 12 000, con 353 MiB de montón de Go, frente a un límite de 384Mi con un límite blando de 288 MiB. Por encima de unos 8 000 eventos por segundo el pod habría quedado retenido en su límite blando, recolectando sin parar, o habría sido terminado. El chart fija ahora el mínimo en 768Mi (límite blando de 576 MiB) mientras `resolved` o `inbound` esté activado, y en 384Mi en caso contrario. La cifra anterior para la protección (unos 180 MB a 20 000 eventos por segundo) era de tres a cuatro veces demasiado baja; las cifras corregidas están en [Sobres de eventos](#event-envelopes).

Ambos son mínimos, aplicados solo cuando el límite que se aplicaría de otro modo es menor. Un límite fijado en el servicio, o un límite de nivel superior mayor, se usa tal cual, y el límite blando sigue al límite que se aplique. El nuevo valor `functionalAreas.<servicio>.memoryLimitFloorWithEnvelopes` contiene el mínimo con sobres. `dcctl` fija `eventEnvelopesEnabled` a partir de la configuración de instancia que escribe. Si instala el chart usted mismo con `instance.existingSecret`, el chart no puede leer esa configuración: fije `eventEnvelopesEnabled: true` cuando active cualquiera de los ajustes de sobres.

#### Dimensionamiento de cachés y bróker {#v120-sizing-defaults}

La ronda 30 en Google Kubernetes Engine (de 8.000 a 20.000 eventos por segundo, un servidor del bróker) encontró tres tamaños incluidos por defecto por debajo de la carga para la que están documentados.

- **Las cachés por dispositivo de `device-management` son de 80 MiB, no de 24.** Un dispositivo ocupa de 2,5 a 3,1 KB en la caché por token, así que 24 MiB guardaban de 8.000 a 10.000 dispositivos. Con 16.000 dispositivos la caché expulsó una entrada en el 73 % de las consultas, la CPU por evento pasó de 0,048 a 0,105 segundos de CPU por cada 1.000 eventos entre 8.000 y 16.000 dispositivos, y con 20.000 las lecturas pasaban al cubo clave-valor y agotaban el tiempo. `DeviceLookupCacheTooSmall` ya saltaba desde 12.000 dispositivos; no cambia. 80 MiB guardan 27.000 dispositivos por token, la flota de 20.000 dispositivos a la que apuntan las tablas de dimensionamiento con una cuarta parte de margen. Para otras flotas, fije `inMemoryCache.perDeviceCacheMiB` en 4 por cada 1.000 dispositivos que se resuelven por su token. La regla, con el límite de memoria que necesita, está en [Dimensionamiento de los servicios](./bootstrap.md#service-sizing) y en [Cachés que dejan de responder](./observability.md#kv-caches).
- **El mínimo de memoria de `device-management` es 768Mi (límite blando de 576 MiB), y sustituye a los 512Mi de [Límites de memoria](#v120-memory).** Con el límite mayor, todas las cachés llenas ocupan unos 370 MiB vivos con el servicio, lo que habría dejado al límite blando de 512Mi (384 MiB) en 1,04 veces eso, y deja a 768Mi en 1,5 veces. Las reglas del mínimo son las mismas: un límite que fije usted en el servicio, o un límite superior de nivel superior, se usa tal cual.
- **El mínimo de `event-processing` con sobres activados sigue siendo 768Mi, y no crece con el ritmo.** Su montón de Go fue de 441, 488, 511 y 516 MiB con 8.000, 12.000, 16.000 y 20.000 eventos por segundo (residente de 500 a 571 MiB), frente al límite blando de 576 MiB, así que desde unos 16.000 eventos por segundo sostenidos recolecta en el límite. Fije ahí unos 1Gi (`functionalAreas.event-processing.resources.limits.memory`).
- **El límite blando del límite de memoria de 4Gi del bróker se alcanza a unos 12.000 eventos por segundo.** El conjunto de trabajo de un servidor fue de 1,9, 3,0, 3,8 y 4,8 GiB con 8.000, 12.000, 16.000 y 20.000 eventos por segundo, frente a un límite blando de 3,2 GiB. Por encima, no lo matan: el servidor recolecta sin parar, y a 12.000 eventos por segundo el tiempo de confirmación en el percentil 99 fue de 101 ms con 4Gi y de 25 ms con 20Gi. Aumente `nats_memory_limit` cuando el conjunto de trabajo esté a menos del 10 % del 80 % del límite, y no solo después de un `OOMKilled`; cuente 0,35 GiB por cada 1.000 eventos por segundo para los que diseñe. Consulte [El intermediario de mensajes](./bootstrap.md#broker-sizing).

#### `dcctl bootstrap` rechaza el estado local que pertenece a otro clúster {#v120-stale-state}

`dcctl bootstrap` (y `dcctl bootstrap --dry-run`) ahora se niega cuando el estado local de ese nombre de instancia, en `~/.devicechain/instances/<nombre>`, se registró contra un clúster distinto del que el comando apunta. Antes se reescribía en silencio con la identidad del clúster nuevo y se planificaba sobre el estado de OpenTofu y las credenciales del broker del clúster anterior, lo que fallaba después con un mensaje sin relación (por ejemplo, la negativa a reducir un clúster de brokers que no existe). El rechazo nombra ambos clústeres e imprime el comando que lo resuelve. Si el clúster registrado ya no existe, mueva el directorio a otro lugar; contiene credenciales, así que conserve la copia en lugar de borrarla:

```bash
mv ~/.devicechain/instances/<nombre> ~/.devicechain/<nombre>.stale-state
```

Si el clúster registrado sigue en marcha, use otro nombre de instancia. Un registro de un `dcctl` anterior sin identidad de clúster no se rechaza. No cambió ningún comando ni opción de `dcctl`.
#### Los tokens se comprueban contra su recurso {#v120-token-audience}

Un cliente OAuth puede pedir un token vinculado a un recurso con el parámetro `resource` de las solicitudes de autorización y de token. Hasta ahora el servidor de autorización aceptaba cualquier valor y ninguna API miraba ese vínculo, así que un token vinculado a un recurso era aceptado por todas las API. Esta versión añade dos comprobaciones y **por defecto, en esta versión ambas solo cuentan y registran. No se rechaza nada**, salvo que usted establezca `enforcement: enforce`. [Audiencia del token](../concepts/security-model.md#token-audience) explica las reglas.

- **El servidor de autorización comprueba el recurso solicitado.** Los recursos permitidos son el servidor MCP de esta instancia, la API de cada área en `<origin>/api/<área>` y lo que figure en `infrastructure.tokenAudience.externalResources`. La comprobación se hace en el endpoint de autorización, en la concesión `client_credentials` y en cada renovación. Un valor que no está en la lista se cuenta en `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}`. También se cuentan, solo en el endpoint de autorización, un valor que no es una URL `https` absoluta (`http` solo para un host de loopback) y una solicitud con más de un `resource`. La concesión `client_credentials` ya rechaza esos dos casos.
- **Cada API comprueba la audiencia del token.** La API de un área acepta un token sin vínculo, vinculado a esa área o vinculado al servidor MCP. Cualquier otro se cuenta en `devicechain_<área>_token_audience_checks_total{verdict="would_reject"}`. Un token obtenido al iniciar sesión en la consola, la aplicación de paneles, el SDK de C# o `dcctl` no tiene vínculo, así que no le afecta. Las llamadas del servidor MCP a las API llevan el vínculo MCP y se aceptan.

Ambos contadores vienen con una alerta `info` (`TokenAudienceWouldReject` y `OAuthResourceWouldReject`) y con un aviso limitado en frecuencia en el registro del servicio, que indica el cliente OAuth y el inquilino. [Comprobaciones de la audiencia del token](./observability.md#token-audience-checks) explica cómo leerlos.

**`v1.3` los rechaza por defecto.** Una solicitud de autorización se devuelve a la dirección de redirección del cliente con `invalid_target`, una solicitud `client_credentials` responde `invalid_target`, una renovación o un código de autorización vinculados a un recurso que ya no se sirve responden `invalid_grant`, una solicitud cuyo `resource` difiere del de su código o token de renovación responde `invalid_target`, y una API responde `401`. Un token de renovación rechazado porque su recurso ya no se sirve queda gastado: volver a `observe` o retroceder de versión restablece la configuración pero no esa sesión, y el cliente debe autorizar de nuevo. Si alguno de los contadores se mueve, localice el cliente en la línea de registro y cambie el `resource` que envía: omítalo o use uno de los valores anteriores. Si un servicio suyo valida tokens de la plataforma con el conjunto de claves publicado y pide tokens vinculados a sí mismo, añada su identificador a `externalResources`. Las API de la plataforma nunca aceptan un token vinculado a un recurso externo; tampoco lo acepta el endpoint `userinfo` del servidor de autorización.

- **Para conservar el comportamiento de esta versión una versión menor más**, establezca `infrastructure.tokenAudience.enforcement: observe` antes de actualizar a `v1.3`. Esta versión ya lo acepta, y establecerlo antes evita que ningún pod aplique la comprobación durante el despliegue. `v1.4` rechaza ese valor: un servicio se detiene al arrancar con una indicación de qué hacer, y el esquema de valores del chart lo rechaza para que `helm upgrade` se detenga antes de reiniciar ningún pod.
- **Para activar la aplicación obligatoria antes**, establezca `enforcement: enforce` en esta versión, lo que reinicia los pods una vez. Cuando `v1.3` cambie el valor por defecto, los pods antiguos y los nuevos coexisten durante el despliegue, así que durante unos minutos un mismo token puede ser aceptado por una réplica y rechazado por otra. Establecerlo antes lo evita.

El chart y `dcctl` escriben `infrastructure.tokenAudience` por usted, a partir del host del ingress y de la dirección del servidor MCP. No establezca `areaBaseUrl` ni `mcpResources` en `instance.config`: el renderizado lo rechaza. Un ingress sin TLS en un host distinto de `localhost` no tiene recursos de área, igual que no tiene servidor de autorización. Las claves que puede establecer son `enforcement`, `unscopedClientCredentials` y `externalResources`; consulte [Configuración](../concepts/security-model.md#token-audience-config).

**El registro del servidor MCP.** `mcp` comprueba al arrancar que su `resourceUrl` figura en `infrastructure.tokenAudience.mcpResources`. Si no figura, `mcp` registra un Error en cada arranque e informa del medidor `devicechain_mcp_token_audience_registered` con valor 0, y las llamadas que reenvía se cuentan como rechazos que se habrían producido. Con `enforcement: enforce` se niega a arrancar.

- **En una instancia que escribe el chart** el chart siempre la incluye, así que el medidor vale 1. Las excepciones son una `functionalAreas.mcp.config.resourceUrl` explícita cuyo esquema o host contenga una letra mayúscula, y una que sea igual al identificador propio de un área de la plataforma, como `<origin>/api/device-management`. La lista solo admite esquema y host en minúsculas, y la comparación es byte a byte. Con observe el chart omite ese valor, con los síntomas anteriores, y con `enforce` hace fallar el renderizado. El propio `mcp` sigue aceptando el valor, así que cámbielo a una dirección en minúsculas que no sea la de un área antes de `v1.3`.
- **Si escribe usted mismo la configuración de la instancia** (`instance.existingSecret`), el chart no escribe el bloque. Cuando su documento no tiene bloque `tokenAudience`, o su `mcpResources` no incluye la `resourceUrl` del servidor MCP, el medidor se queda en 0 y el Error se repite en cada arranque mientras falte la entrada. Eso es permanente hasta que la añada. Cuando el área MCP está desplegada, `dcctl upgrade` muestra un aviso con el bloque que hay que añadir y, con `enforce`, se niega a continuar. Añada el bloque con `areaBaseUrl: <origin>/api` y `mcpResources: [<la resourceUrl del servidor MCP>]`, y actualice `instance.existingSecretChecksum`.

**Obsoleto: una solicitud `client_credentials` que indica un `resource` sin un `scope`.** Ese token lleva todas las autoridades de los roles de la cuenta de servicio. Envíe un `scope` junto con el recurso (`read-only`, opcionalmente con `location`). Un token con ámbito lleva solo las autoridades de lectura del ámbito, así que una integración que necesita escribir debe omitir el `resource`. La falta de ámbito nunca es motivo de rechazo en esta versión, así que la solicitud sigue funcionando. Cada una se cuenta en `devicechain_usermanagement_oauth_unscoped_client_credentials_total` y se registra con su cliente, y se rechaza con `invalid_scope` en la próxima versión mayor. Hasta entonces, `infrastructure.tokenAudience.unscopedClientCredentials: enforce` se rechaza al arrancar. Consulte [Cuentas de servicio](../user-guide/service-accounts.md#binding-a-token-to-a-resource).

**Volver atrás.** Los servicios de `v1.1` rechazan el bloque `tokenAudience` al arrancar. `dcctl` genera la configuración de `v1.1` sin él. Si escribe usted mismo la configuración, elimine el bloque y actualice `instance.existingSecretChecksum` antes de volver.

#### Un nuevo motivo de evento fallido {#v120-decode-failed}

`FailedEventReason` incorpora `DECODE_FAILED`: un decodificador de carga útil rechazó un uplink de una integración. El inicio del texto del error indica el motivo (por ejemplo `no_section`, `eval_error` o `invalid_output`). Se registra en la primera entrega y no se reintenta, porque el mismo uplink y el mismo decodificador dan la misma respuesta. La política de versiones permite un nuevo valor de enumeración en una versión menor; un cliente con un `switch` exhaustivo sobre los motivos debe contemplarlo, y un event-management anterior muestra el número como `UNKNOWN` junto con su `reasonCode`. `dcctl` acepta `DECODE_FAILED` en `--reason`.

#### Un decodificador de carga útil que falla tiene un presupuesto {#v120-decoder-budget}

Un decodificador incorrecto para un dispositivo falla en cada uplink de ese dispositivo, y cada fallo era un registro de eventos fallidos. En un tenant con mucho tráfico eso es una avalancha de registros en un flujo compartido por toda la plataforma. Desde esta versión, `device-management` somete cada tipo de dispositivo a un presupuesto antes de escribir el registro:

- **Registros.** En cada réplica, para cada tenant y tipo de dispositivo, los primeros 20 fallos de decodificación se registran en los eventos fallidos (motivo `DECODE_FAILED`, con el uplink archivado). Después se registran 10 más por minuto. El mensaje de origen de todos los demás fallos se confirma sin registro, y el fallo se cuenta como **suprimido**. Un fallo suprimido solo ocurre cuando ya se han escrito 20 registros de ese tipo de dispositivo, de modo que un decodificador roto nunca pasa inadvertido.
- **Muestras.** En cada réplica, para cada tenant y perfil, se conserva un uplink fallido cada 10 segundos, y un perfil conserva los 20 más recientes. El panel de pruebas puede cargar uno en la prueba del decodificador, que da el resultado que tuvo en vivo. Una muestra es de mejor esfuerzo: la que no se puede guardar (falla la escritura, o el JSON recibido no se puede volver a codificar) se cuenta en `resolve_decode_samples_dropped_total`, y su fallo se registra igualmente.
- **El presupuesto es por réplica,** de modo que el total de un tenant crece con el número de réplicas. No se puede configurar nada.
- **Cada uplink se cuenta una vez por entrega.** La salud del decodificador del perfil suma, por minuto, los uplinks decodificados, ignorados, fallidos (registrados) y suprimidos (no registrados). Los recuentos de `payloadDecoderHealth` son `Float` y no `Int`, porque un decodificador con mucho tráfico supera un entero de 32 bits en una semana; son números enteros, exactos hasta 2^53. Las consultas `payloadDecoderHealth` y `payloadDecoderSamples` de la API GraphQL de `device-management` son nuevas, aditivas y requieren `device:write`; los bytes de una muestra solo son accesibles mediante `testPayloadDecoder` con `sampleId`, con el mismo permiso. Las filas de salud se conservan 7 días.
- **Los recuentos pueden quedarse cortos tras una caída.** Los totales se escriben una vez por minuto, así que una caída pierde hasta un minuto de recuentos, nunca un registro de eventos fallidos. Tras una caída, o mientras el flujo de eventos fallidos no está disponible, un uplink reenviado puede contarse dos veces. Un fallo reenviado siempre se registra, y aun así consume un token del presupuesto.
- **Hay dos alertas nuevas.** `PayloadDecoderFailuresHigh` salta cuando más del 5 % de los uplinks decodificados fallan durante 15 minutos, y `PayloadDecoderSuppressing` salta cuando se suprimen más de 50 fallos en 10 minutos. No llevan etiqueta de tenant; el índice de eventos fallidos y la salud del decodificador del perfil indican el tenant y el perfil. Métricas nuevas: `resolve_decode_suppressed_total`, `resolve_decode_samples_dropped_total`, `resolve_decode_health_flush_failures_total`, `resolve_decode_health_lost_total` y `resolve_decode_unattributed_total`.
- **No hay que hacer nada para actualizar.** Las tablas las creó la migración anterior de esta versión y no se ejecuta ninguna migración nueva. Una réplica anterior a esta versión no escribe salud ni muestras y no aplica presupuesto a los fallos.

#### Uplinks con decodificador e interrupciones de device-management {#v120-decoder-stream}

Un uplink de un tipo de dispositivo que tiene un decodificador de carga útil viaja de event-sources a device-management por un flujo propio. Ese flujo es pequeño y de tamaño fijo (32 MiB), por lo que el volumen de JetStream predeterminado y la tabla de dimensionamiento publicada no cambian. Si device-management deja de leerlo, el flujo se llena en segundos con tasas altas y event-sources responde con un 503 a las solicitudes de esas integraciones. Un remitente que reintenta una entrega fallida entregará el uplink cuando el flujo se vacíe; uno que no reintenta, no. Los webhooks de The Things Stack solo reintentan una entrega fallida con su función opcional de reintentos de webhook (una función de Cloud Plus, en la que cualquier respuesta distinta de 2xx cuenta como intento fallido), y la integración HTTP de ChirpStack v4 no documenta ningún reintento, así que trate los uplinks que envíe durante una interrupción como no reenviados. Los uplinks ya aceptados (respondidos con 202) no se pierden. Los uplinks de tipos de dispositivo sin decodificador se atienden como antes y no se ven afectados. Ejecute más de una réplica de device-management, las suficientes para que reiniciar una no detenga la lectura, de modo que una interrupción dure poco.

#### Cambios de perfil durante una actualización progresiva {#v120-profile-cache}

`device-management` guarda ahora en caché la resolución del perfil de cada tipo de dispositivo bajo una clave nueva, de modo que una réplica de esta versión nunca lee una entrada escrita por una réplica anterior (las entradas antiguas no llevarían el decodificador de carga útil). Todo cambio hecho en una réplica de esta versión elimina la entrada bajo ambas claves. Un cambio hecho en una réplica **anterior** elimina solo la clave antigua, así que las réplicas actualizadas siguen sirviendo lo que tienen hasta que caduca.

- **Los cambios de perfil y de tipo de dispositivo hechos mientras se actualiza de forma progresiva pueden tardar hasta aproximadamente el doble del tiempo de vida de la caché en llegar a las réplicas actualizadas** (unos 10 minutos con los 300 segundos predeterminados de `metricDefCacheTtlSeconds` e `inMemoryCache.ttlSeconds`). Esto abarca una publicación, una reversión, un cambio de perfil de un tipo de dispositivo y un cambio de geovalla. Cuando todas las réplicas ejecutan esta versión, los cambios surten efecto de inmediato como antes.
- **Publique el decodificador de carga útil de un perfil solo cuando todas las réplicas de `device-management` ejecuten esta versión.** Una versión publicada desde una réplica anterior no contiene el decodificador.
- **La copia en proceso de esta caché es mayor.** Está limitada a 32 MiB por réplica (antes 4 MiB), porque una entrada con un decodificador de carga útil contiene sus programas compilados. Las entradas sin decodificador no cambian de tamaño. El límite es `inMemoryCache.profileResolutionCacheMiB`, y `inMemoryCache.profileResolutionTenantSharePercent` (25 por defecto) limita cuánto puede ocupar un solo inquilino, de modo que un inquilino con muchos decodificadores grandes queda limitado a su parte y expulsa primero sus propias entradas. Cuando la caché entera de una réplica está llena, cede una entrada primero el inquilino que más ocupa, así que un inquilino dentro de su cuota conserva sus entradas mientras otro mayor tenga algunas que ceder. La cuota de un inquilino es el espacio donde sus decodificadores grandes deben caber juntos: un decodificador máximo se contabiliza en unos 6 MiB, así que la cuota por defecto de 8 MiB admite uno, y dos en uso a la vez en el mismo inquilino se turnan, compilando su decodificador de nuevo en cada lectura. Dimensione la caché y la cuota según el número de decodificadores grandes que un inquilino mantiene en uso. `device-management` ahora se niega a arrancar si la cuota de un inquilino (`profileResolutionCacheMiB` x `profileResolutionTenantSharePercent`) es inferior a 7 MiB, que es lo que se contabiliza para el mayor decodificador que permiten los límites: una cuota menor no podría mantenerlo decodificado, y cada evento de su tipo de dispositivo lo compilaría de nuevo. Un decodificador menor necesita menos. Las réplicas exportan `payload_decoder_compiles_total`, y el chart avisa con `PayloadDecoderRecompileSustained` cuando una réplica compila más de una vez por segundo durante 15 minutos. Si fija usted mismo un límite de memoria, cuente los 28 MiB adicionales.

#### Decodificadores de payload {#v120-payload-decoders}

Un perfil de dispositivo puede llevar ahora un **decodificador de payload**: reglas, escritas como expresiones, que convierten una trama binaria, un cuerpo JSON genérico o el objeto decodificado de un servidor de red en mediciones. La nueva guía [Decodificadores de payload](../guides/payload-decoders.md) los describe, y la [referencia de funciones](../reference/decoder-functions.md) enumera las funciones de la biblioteca y los límites. No hay que hacer nada para actualizar, y un perfil sin decodificador se comporta exactamente como antes.

- **La creación y la prueba están en esta versión.** La página del perfil de la consola tiene una pestaña **Decodificador**. En la API GraphQL de `device-management`, `setDeviceProfileDecoder` y `clearDeviceProfileDecoder` escriben el decodificador del borrador del perfil, `testPayloadDecoder` ejecuta un decodificador sobre una entrada sin guardar nada y `payloadDecoderFunctions` enumera la biblioteca de funciones. `DeviceProfile.decoder` y `DeviceProfileVersion.decoder` son campos nuevos que admiten nulos. Todo es aditivo.
- **La decodificación de uplinks en vivo está en esta versión.** Cuando el dispositivo del que trata un uplink de una [integración de entrada](../guides/inbound-integrations.md) tiene un tipo de dispositivo cuya versión de perfil lleva un decodificador, `device-management` decodifica el uplink con él y no se guarda lo que decodificó el servidor de red. Event-sources busca y recuerda el decodificador de un tipo de dispositivo durante aproximadamente un minuto, así que hasta un minuto después de publicar uno, un uplink aún puede guardarse como lo decodificó el servidor de red. Un uplink de un tipo de dispositivo sin decodificador se guarda como antes. El uplink se carga a la asignación de ingesta del inquilino al aceptarse: como mínimo el máximo de lecturas del decodificador (más tres si la integración guarda métricas de enlace), más para un decodificador costoso de ejecutar, y nunca más de 256. Si un decodificador publicado entretanto produce más, las lecturas se guardan y `device-management` cuenta el exceso. Una decodificación fallida no guarda nada y se registra como un evento fallido con el motivo `DECODE_FAILED`; consulte [Un nuevo motivo de evento fallido](#v120-decode-failed). El endpoint de integración ahora también pregunta a `device-management` si el tipo del dispositivo tiene un decodificador, y responde `503` cuando no puede obtener la respuesta y no recuerda ninguna de los últimos 15 minutos, en lugar de guardar lecturas que un decodificador debía producir. Frente a un `device-management` que no conoce la pregunta, algo posible durante la actualización progresiva, el uplink se guarda como antes.
- **La publicación depende del decodificador.** Un perfil con decodificador se publica, o se vuelve a una versión suya, solo si el decodificador compila con las métricas de esa versión y pasan todos los vectores de prueba guardados. Un perfil sin decodificador no se ve afectado.
- **La migración es aditiva.** Añade una columna que admite nulos a los perfiles de dispositivo y dos tablas en las que `device-management` registra la salud del decodificador y las muestras de fallos (consulte [Un decodificador de carga útil que falla tiene un presupuesto](#v120-decoder-budget)). Una réplica anterior no las lee.
- **La puerta de prueba tiene un límite de frecuencia por réplica.** `testPayloadDecoder` permite 10 peticiones por segundo por inquilino, con una ráfaga de 20, en cada réplica de `device-management`, así que el total de un inquilino crece con el número de réplicas. Una petición por encima del límite se rechaza con `RATE_LIMITED`.
- **Durante una actualización progresiva, publique un decodificador solo cuando todas las réplicas de `device-management` ejecuten esta versión.** Una versión publicada mientras la petición la atiende una réplica anterior se congela sin su decodificador, porque esa réplica no conoce el campo. Terminada la actualización, compruebe que `DeviceProfileVersion.decoder` está presente en la versión que publicó y, si no lo está, publique de nuevo.

#### Listas de revocación X.509 {#v120-x509-crl}

Un tenant puede ahora guardar la lista de revocación (CRL) de una autoridad certificadora en un ancla de confianza, y cada ancla tiene un modo de revocación. Un ancla sin lista y sin revocación manual se comporta exactamente como antes.

- **Una CRL se sube por GraphQL o con un `PUT` en streaming**, y la plataforma comprueba su firma contra el ancla. El tamaño admitido es el ajuste fijo `deviceManagement.callout.revocation.maxCrlBytes` (2 MiB por defecto, hasta 32 MiB). **Dimensiónelo según el límite de memoria del pod.** Verificar una CRL la analiza entera, y una subida alcanza un pico de entre 20 y 30 veces el tamaño de la CRL en heap: una lista de 2 MiB cuesta de 40 a 65 MiB, y una de 7 MB con 300 000 entradas unos 190 MiB. `maxConcurrentWrites` (1 por defecto) es cuántas subidas procesa a la vez una réplica.
- **Una CRL más antigua nunca puede sustituir a una más nueva**, ni siquiera después de borrar la lista: se conserva el número de CRL más alto aceptado para cada emisor. **Borrar un ancla de confianza descarta esos números con ella**, junto con sus listas y sus entradas, de modo que un ancla recreada a partir de la misma CA empieza de nuevo y acepta una CRL que antes se rechazó. Un ancla solo puede borrarse cuando ninguna credencial ni perfil de aprovisionamiento depende de ella.
- **Borrar un ancla de confianza se rechaza mientras tenga revocaciones manuales.** Anúlelas primero (`unrevokeDeviceCertificate`); son decisiones suyas y nunca se descartan en silencio.
- **`REQUIRE_CURRENT` es opcional por ancla.** La plataforma se niega a cambiar un ancla a ese modo mientras no tenga ninguna lista vigente, o mientras un certificado registrado haya sido emitido por una CA que no la tiene, porque todos esos dispositivos serían rechazados.
- **Durante una actualización gradual**, las réplicas que aún no se han sustituido responden al callout de autorización del broker sin comprobar revocaciones, así que una revocación solo se aplica en todas partes cuando termina el despliegue. Volver a `v1.1.0` deja las tablas sin leer: las revocaciones dejan de aplicarse.

#### Revocar un certificado termina las sesiones abiertas {#v120-x509-sessions}

Revocar un certificado, a mano o subiendo una CRL que lo nombra, ahora también termina las sesiones abiertas de los dispositivos a los que afecta. Antes, una sesión ya abierta seguía hasta que caducaba su token (hasta 12 horas por defecto). No cambia nada en una instancia que no revoca nada.

- **Un certificado de dispositivo termina las sesiones de los dispositivos que lo tienen.** La revocación de una autoridad certificadora es más amplia: cuando revoca una CA entregando su certificado, o una CRL nombra un intermedio que se vio usar a una credencial registrada, se terminan las sesiones de **todos los dispositivos con una credencial de certificado bajo esa ancla de confianza**. Esos dispositivos se reconectan solos; aquellos cuya cadena sigue siendo de confianza se admiten de inmediato. Una llamada termina por sí misma las sesiones de hasta unos 5 000 dispositivos, así que una revocación a nivel de CA puede reconectar a ese número a la vez; el resto se termina en segundo plano. **En una instancia actualizada desde una versión anterior**, una credencial registrada antes de la actualización no tiene un intermedio registrado, así que una CRL que añade una entrada que no coincide con ninguna credencial almacenada también termina las sesiones de esas credenciales antiguas, por si la entrada es un intermedio que usan.
- **Las sesiones se terminan dos veces.** Una al confirmarse la revocación y otra unos 10 segundos después. La segunda pasada alcanza a un dispositivo que se estaba admitiendo en el instante de la revocación. Por eso una revocación a nivel de CA consulta dos veces a toda el ancla.
- **Revocar puede tardar contra un respondedor lento.** La llamada espera al servicio que termina las sesiones (`event-sources`) hasta unos 90 segundos. Si no puede confirmarlo, la revocación sigue en pie y las conexiones nuevas se rechazan; el resultado indica `sessionsEnded: false`, la plataforma sigue reintentando y repetir la revocación o la subida vuelve a preguntar.
- **Durante una actualización gradual**, los nuevos cierres de sesión llevan un motivo nuevo que `event-sources` debe entender. Hasta que `event-sources` se haya reemplazado, estos cierres quedan pendientes y se reintentan, de modo que una revocación hecha a mitad de la actualización termina las sesiones abiertas cuando `event-sources` se haya actualizado. Los pods de la versión anterior ni toman estos cierres pendientes ni los eliminan.

#### Asignación del registro de escritura anticipada del almacén de eventos {#v120-event-store-wal}

El `max_wal_size` del almacén de eventos es ahora una quinta parte de su volumen, nunca por debajo del 1 GiB propio de Postgres, y una actualización lo aplica a un almacén existente con el volumen que ya tiene. Con los 32Gi predeterminados son 6,4 GiB, donde una instancia actualizada tenía 1 GiB. `max_wal_size` es un objetivo de frecuencia de los puntos de control, no un límite ni una reserva: el registro puede superarlo cuando los puntos de control no dan abasto o el archivado de WAL se retrasa. Sin embargo, reside en el volumen de datos, así que un almacén que ya está casi lleno tiene menos margen que ese, y un atasco prolongado del archivado puede usar más que ese valor.

- **`dcctl upgrade` avisa cuando el valor sube.** El plan imprime una línea con el tamaño anterior y el nuevo. También lee el espacio libre del volumen del almacén de eventos e imprime una advertencia si es menor que el nuevo tamaño, o si no pudo leerlo. No se niega a continuar: un almacén con poco espacio libre puede estar sano al ritmo actual, la lectura del espacio libre es de mejor esfuerzo, y el remedio es una opción.
- **Para conservar una asignación menor**, pase `--event-store-max-wal-mib` a la actualización, por ejemplo `dcctl upgrade local devicechain --event-store-max-wal-mib 1024` para el 1 GiB anterior. `--dry-run` muestra antes el efecto. Las actualizaciones posteriores conservan el valor cuando se omite la opción, y `--event-store-max-wal-mib 0` vuelve al valor derivado. `--event-store-checkpoint-timeout` fija del mismo modo el intervalo entre puntos de control (`15min` por defecto; un valor vacío, `--event-store-checkpoint-timeout=""`, vuelve a él). `dcctl` registra ambos antes de aplicar el cambio, de modo que una actualización que falla a medias y se repite sin las opciones los conserva. Ambos se aplican por recarga, sin reiniciar el almacén de eventos. `dcctl` los registra en `zz-dcctl-event-store.auto.tfvars.json`, junto al estado de OpenTofu de la instancia, en la máquina que ejecuta la actualización; consulte [Ajuste del almacén de eventos](./tuning-an-instance.md).
- **Si gestiona usted mismo el OpenTofu de la instancia**, fije `timescale_max_wal_size_mib` y `timescale_checkpoint_timeout` como antes.

#### HTTP deja de responder 202 a un evento descartado {#v120-http-build-dropped}

El endpoint HTTP de eventos (`POST /{instanceId}/{tenant}/events`) respondía `202 Accepted` a un evento descartado mientras se construía su mensaje de entrada: una credencial presentada que no se pudo sellar, o un evento que no se pudo codificar. Al llamante se le decía que sus datos estaban a salvo cuando no se habían almacenado en ningún sitio. Desde esta versión el endpoint responde `500 Internal Server Error` a esos eventos, sin `Retry-After` y sin poner la causa en la respuesta.

- **No hace falta hacer nada si ningún cliente ve un `500`.** Los casos son una clave de instancia ausente o con fallo en el servicio, o un evento que no se puede codificar, y ninguno ocurre en el funcionamiento normal.
- **Un cliente que reintenta toda respuesta distinta de `202` no debe reintentar un `500` de este endpoint:** la misma petición falla igual. `503` (con o sin `Retry-After`) y `429` conservan su significado.
- **Un contador nuevo, `total_msg_build_dropped{source,cause}`,** cuenta estos descartes en todos los transportes, con `cause` igual a `no_tenant`, `no_transit_key`, `seal_failed` o `marshal_failed`. Se exporta a 0 para cada fuente configurada. Cualquier valor mayor que 0 es un evento que no se almacenó.
- Los endpoints de integración conservan su `202` documentado cuando se descarta un mensaje.

#### Preajustes de tamaño {#v120-size-presets}

`dcctl bootstrap` tiene una nueva opción `--size`, y `dcctl install` acepta `--size base` y `--size compact`. No cambia nada salvo que la indique: una instancia sin tamaño genera byte por byte lo que generaba antes de este cambio, y `--compact` funciona exactamente igual que antes.

- **`perf-12k` es el primer tamaño.** Establece réplicas, CPU y memoria de los cinco servicios más ocupados, las solicitudes y el límite de memoria del broker, los volúmenes de JetStream y del almacén de eventos y la asignación del registro de escritura anticipada del almacén de eventos, tal como se midieron en un clúster que sostiene 12 000 eventos por segundo con los sobres de eventos activados. Las cifras están en [Elegir un tamaño](./sizing.md#perf-12k).
- **Las instancias compactas existentes ganan `spec.size: "compact"` en su próxima escritura.** La declaración de una instancia creada con `--compact` se reescribe con el tamaño junto a `compact: true` la próxima vez que `dcctl` la escribe (una actualización lo hace); nada de la instancia cambia.
- **Queda registrado en la instancia** (`spec.size` en la declaración, vacío para base) y cada `dcctl upgrade` lo vuelve a aplicar. Una versión que reajusta un tamaño cambia las instancias registradas con él en su siguiente actualización, y lo indica aquí.
- **Prevalecen sus propios ajustes.** De menor a mayor prioridad: los valores por defecto del chart, el tamaño, lo que `dcctl` calcula por sí mismo, su superposición `--values` y, por último, una variable de OpenTofu que usted indique. Consulte [Qué valor prevalece](./sizing.md#precedence).
- **No activa nada más.** Un tamaño no activa los sobres de eventos ni la confirmación MQTT a prueba de fallos, y con `--ha` avisa de que la combinación no se ha medido.

#### Tablero del agente de borde y el job de rascado {#v120-edge-board-job}

Los paneles de proceso del tablero del agente de borde seleccionan `job="dc-edge-agent"` además del agente. `agent` es una etiqueta externa global del colector de la máquina, así que un colector que también rasca otra cosa en la máquina (node exporter, por ejemplo) sumaba la memoria, la CPU, las goroutines y los archivos abiertos de ese proceso a los paneles de todos los agentes. La configuración de rascado documentada ya usa `job_name: dc-edge-agent`, de modo que un tablero importado con ella no necesita cambios. Si dio otro nombre al job, cámbielo en el colector o edite las cinco consultas de proceso y de runtime del tablero (`process_*` y `go_goroutines`) con su nombre de job. Vuelva a importar el tablero desde `backend/edge/dc-edge-agent/dashboards/edge-agent.json` para aplicar el cambio.

#### Las alertas llegan a una persona {#v120-alert-routing}

Cada regla de alerta que incluye el chart lleva ahora un `runbook_url` que abre su entrada en la nueva [Guía de alertas](./alert-runbook.md). Nada más de una regla cambia: nombres, expresiones, duraciones `for` y etiquetas son los mismos.

`dcctl install` y `dcctl upgrade` aceptan `--alert-webhook`, `--alert-email` (con `--alert-smtp-host`, `--alert-smtp-from`, `--alert-smtp-user` y `--alert-smtp-password-file`), `--alert-config`, `--alert-watchdog-webhook` y `--clear-alert-routing`. Alertmanager lee ahora su configuración del Secret `dc-alertmanager-config` del namespace `monitoring`, que `dcctl install` escribe antes de aplicar la pila de monitoreo. Volver a ejecutar `dcctl install` sin ninguna opción `--alert-*` conserva el enrutamiento existente, y una primera instalación sin ellas escribe una configuración sin receptor, que es el comportamiento de las versiones anteriores. Una instancia indica si alguien escucha en su nueva condición `AlertsRouted` y en `dcctl instances list`; la condición nunca hace que la instancia deje de estar lista.

Vuelva a ejecutar `dcctl install` una vez en un clúster instalado con una versión anterior antes de usar las opciones con `dcctl upgrade`; en caso contrario `dcctl upgrade` las rechaza, porque el Alertmanager anterior no lee ningún Secret nuestro. Consulte [Enrutar las alertas a una persona](./observability.md#alert-routing).

#### Otros cambios en esta versión {#v120-other-changes}

Nada de esto requiere acción, salvo los dos elementos de `dcctl` de la lista anterior. Cada uno es una corrección o una adición a algo que ya se había publicado.

- **`dcctl sim create` y los niveles.** Un nivel indicado con `--tier` que ya existe debe declarar todos los límites que nombran el escenario o `--tier-config`, o `create` lo rechaza y nombra la clave: `dcctl` sube límites y no puede ver un valor por defecto heredado. Un nivel que `create` crea él mismo no se ve afectado. Un `null` en `--tier-config` se rechaza en lugar de leerse como 0.
- **`dcctl upgrade` y los volúmenes del broker.** Una reclamación cuyo único paso pendiente es la mitad del sistema de archivos de un cambio de tamaño, y cuyo pod de servidor del broker ya no existe, deja de rechazar la actualización para siempre: el StatefulSet que la actualización vuelve a crear termina el cambio de tamaño. Un cambio de tamaño en curso, una reclamación corta sin cambio de tamaño registrado, un fallo de cambio de tamaño en el nodo y un pod vivo siguen rechazándola. Tras el despliegue, la actualización comprueba las reclamaciones como se describe en la lista anterior. `dcctl upgrade --mqtt-exposure <modo>` sin `--mqtt-host` conserva el host que la instancia ya registra; si no hay ninguno registrado, se sigue rechazando.
- **`dcctl sim`.** `dcctl sim status`, `start` y `stop` aplican sangría al JSON de la API de control cuando la salida es un terminal; la salida canalizada no cambia. Las flotas simuladas pueden ejecutar la parte del dispositivo de una actualización por aire (`--ota`, con opciones para inyectar un hash incorrecto, una firma incorrecta, una descarga interrumpida y un dispositivo sin conexión).
- **`dcctl bootstrap` y `dcctl sim create` dicen más cuando la instancia no es la predeterminada.** La línea `GraphQL:` del resumen de bootstrap ahora lleva el mismo host y puerto que la línea `Console:`; antes omitía un puerto no predeterminado. Cuando `dcctl sim create` (o `sim destroy`) no puede leer la contraseña del superusuario porque `--instance` conserva su valor predeterminado, `devicechain`, ahora añade las instancias registradas en esta máquina y sugiere `--instance <nombre>`. Los indicadores, los valores predeterminados y la instancia sobre la que actúa cada comando no cambian.
- **`dcctl bootstrap` y `dcctl upgrade` imprimen una línea más** cuando la ruta de ingesta MQTT no está configurada para fallar cerrada, indicando que los eventos confirmados pueden descartarse bajo una sobrecarga sostenida y nombrando `--mqtt-fail-closed`. Nada cambia a menos que actúe.
- **`dcctl broker rebalance` mantiene en servidores distintos los dos flujos por evento más activos.** Tras reiniciar el broker, los líderes de `resolved-events` y de los flujos fijos de ingesta podían acabar en un mismo servidor, algo que un equilibrio por número de flujos no veía. El paso ahora mueve `resolved-events` a un servidor que no lidere ningún flujo por evento cuando hay uno disponible, y su resumen lo indica como separado, compartido o no confirmado. Se conservan los campos del resumen de la v1.0.
- **Almacén de eventos: historial anterior al alcance de la política de refresco.** Una lectura fechada más atrás de los últimos 30 días que refresca el rollup tras `bucketedMeasurements` (store-and-forward, carga retroactiva) se guardaba y la devolvía `measurementEvents`, pero nunca llegaba al rollup. Ahora un barrido cada hora la refresca, de modo que esa lectura aparece en las consultas agrupadas en aproximadamente una hora. No hay cambio de esquema.
- **Deshabilitar o eliminar un dispositivo termina sus sesiones del broker de forma fiable.** Antes, el fin de sesión era un único intento tras guardar el cambio; si el broker no lo confirmaba, solo repetir la llamada lo reintentaba. Ahora se encola con el cambio y se reintenta en segundo plano, como ya ocurría con la revocación de credenciales, y volver a habilitar el dispositivo retira un fin pendiente.
- **Fallos de encolado de comandos que significan «reintente».** Cuando la plataforma no puede decidir un encolado porque un servicio que necesita no está disponible (la validación, la consulta de propiedad, la pertenencia a un grupo, un calendario de ventanas de mantenimiento), el error GraphQL lleva `extensions.code: UNAVAILABLE`. El mensaje no cambia y sigue siendo un error, nunca un rechazo.
- **Detección: una alarma que se cierra por el desarme de un hombre muerto ya no queda activa** cuando un punto de control o el fin del mandato del motor llegaban sin un mensaje posterior. El Resolved pendiente ahora se publica antes.
- **Sobres de eventos (opcional): una ronda cuyo mensaje se expulsó del flujo se omite** y se cuenta (`envelope_rounds_evicted_total`, `resolve_envelope_rounds_evicted_total`), y un grupo de recorridos lleno ya no impide que un servicio se detenga.
- **Hay dos alertas nuevas**, `SparkplugIngestLeaderMetricAbsent` y `Lwm2mIngestLeaderMetricAbsent`: un servicio de ingesta de borde desplegado que no exportaba series de líder dejaba en silencio las alertas de recuento de líderes existentes. Añádalas a su enrutamiento de alertas. El chart incluye dos tableros de Grafana nuevos, para la ingesta Sparkplug y LwM2M, y el repositorio incluye un archivo de panel para el agente de borde; consulte [Tableros de Grafana](./grafana-boards.md).
- **La consola** consulta el estado de los comandos de un dispositivo mientras alguno no ha terminado, da formato a los valores de alarma como los paneles, aplica el tema a los controles de zoom del lienzo de automatización y encabeza la lista de perfiles de dispositivo con el nombre.
- **Paneles en un host con CSP estricta.** `configureStrictCsp` (desactivado por defecto, así que nada cambia) permite que el paquete de reproducción de tableros se ejecute con `style-src 'self'`; consulte [Escribir un widget personalizado](../guides/writing-a-custom-widget.md).
- **Infraestructura.** Con `nats_mqtt_capable_users` sin definir, el módulo OpenTofu del broker ya no escribe un `allowed_connection_types` nulo que el broker rechazaba; el reparador de certificados de las copias de seguridad del almacén de eventos vigila el espacio de nombres configurado del operador.
- **`dcctl upgrade` acepta `--yes` (`-y`), igual que `dcctl install` y `dcctl bootstrap`, y encuentra un escrow escrito con `--escrow-file`.** `--yes` da por respondidas con sí las preguntas; una actualización no hace ninguna, así que no cambia nada, y un script ya puede pasarlo a los tres comandos. `dcctl bootstrap --escrow-file` registra dónde se escribió el artefacto y la comprobación del escrow de la clave raíz de la actualización mira allí, de modo que ya no informa de «sin escrow» para una instancia cuyo artefacto está en una ruta personalizada. Para una instancia creada antes, pase `--escrow-file` una vez a la actualización; ella registra la ruta.

#### Cambios de esquema y cómo volver atrás {#v120-going-back}

Seis migraciones añaden lo siguiente. Toda columna nueva admite nulos, así que un servicio de `v1.1.0` que se ejecute con el esquema nuevo escribe filas que sigue pudiendo almacenar.

- **Revocación X.509** (`device-management`, migración `20261105100000`). Cuatro tablas nuevas: `device_revocation_issuers`, `device_revocation_lists`, `device_revoked_certificates` y `device_credential_chain_links`. Cuatro columnas nuevas: `revocation_mode`, `revocation_stale_grace` y `revocation_rows` en `device_trust_anchors`, y `not_before` en `pending_session_kicks`. Las anclas de confianza existentes dejan `revocation_mode` vacío, lo que se lee como `ENTRIES`: un certificado se rechaza solo cuando figura en la lista.
- **Decodificadores de carga útil** (`device-management`, migración `20261110120000`). Dos tablas nuevas, `profile_decoder_healths` y `profile_decoder_samples`, y una columna nueva, `decoder_declaration` en `device_profiles`.
- **Cola de cierres por revocación** (`device-management`, migración `20261111120000`). Una tabla nueva, `pending_revocation_kicks`, y una columna nueva que admite nulos, `kick_queued_at` en `device_revoked_certificates`. La tabla está separada de la cola de cierres de sesión existente para que una réplica de `v1.1.0` nunca lea ni elimine sus filas.
- **Almacenamiento de proyecciones** (`device-state`, migración `20261104120000`). No añade tablas ni columnas. Fija parámetros de almacenamiento (un factor de relleno de 75 y umbrales de autovacuum) en `device_states`, `latest_measurements` y `latest_locations`. Las filas que ya existen ganan el margen del factor de relleno solo a medida que se reescriben sus páginas, así que una instancia actualizada nota el cambio lentamente.
- **Tablas de ubicación** (`user-management`, migración `20261105120000`). Tres tablas nuevas: `iam_partition_config`, `iam_vpartition_assignments` y `iam_tenant_vpartition_overrides`. La migración no inserta filas; el primer arranque de `user-management` escribe la ubicación predeterminada (una fila en `iam_partition_config` y 64 filas en `iam_vpartition_assignments`) y crea el bucket `dc_partition_placement` en el broker de mensajes.
- **Reparación de la hora de alarma de inactividad** (`device-state`, migración `20261106120000`). No añade nada al esquema. Es una reparación solo de datos de `inactivity_alarm_time` en `device_states`, en lotes de 5.000, que restaura lo que la columna contenía antes de `v1.1.0`. No hay nada que deshacer al volver a `v1.1.0`.

Salvo esa ubicación predeterminada, las tablas nuevas empiezan vacías. **No hay que deshacer nada en una base de datos para volver a `v1.1.0`:** las tablas, columnas y parámetros de almacenamiento nuevos se quedan donde están y los servicios de `v1.1.0` los ignoran, con una excepción: el borrado de inquilinos, más abajo. Avance cuando pueda: una actualización fallida se arregla ejecutando `dcctl upgrade` de nuevo. Para el cambio de configuración que exige volver atrás, consulte [Los tokens se comprueban contra su recurso](#v120-token-audience).

**El borrado de un inquilino queda retenido mientras lo ejecuta una réplica de `user-management` de `v1.1.0`.** Una réplica de `v1.1.0` no conoce las tres tablas de colocación, y su purga relacional se niega a ejecutarse contra una tabla que no sabe clasificar. Mientras una de ellas ejecuta la purga, el almacén relacional informa de un error para el borrado, con una línea de registro y un fallo en el libro que nombran `iam_partition_config`, `iam_vpartition_assignments` o `iam_tenant_vpartition_overrides`, y `tenant_purge_store_blocked{store="rdb",reason="error"}` supera cero. El paso rechazado no borra nada de la base de datos y no se pierde nada; los demás almacenes sí se borran en la misma pasada. El borrado no puede terminar: el inquilino sigue en `purging`. Una réplica de esta versión lo retoma en su siguiente pasada y termina cuando se cumplen sus comprobaciones habituales. En una actualización progresiva ocurre solo cuando desaparece la última réplica de `v1.1.0`; tras volver a `v1.1.0` queda retenido hasta que vuelva a avanzar. Si puede, inicie los borrados cuando la actualización haya terminado.

### v1.1.0 — la ingesta cuesta menos por evento y los dispositivos fuera del clúster se conectan por MQTT {#v110-upgrade}

`v1.1.0` es una actualización in situ desde `v1.0.0`: `dcctl install` para el clúster y después un `dcctl upgrade` por cada instancia, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Durante la actualización se ejecutan dos migraciones añadidas (una en `event-management` y otra en `device-state`); cada una agrega una tabla y no reescribe ninguna fila existente. Los nuevos campos de `Instance` (consulta [Dispositivos fuera del clúster](#v110-mqtt-exposure)) se entregan con el operador, que `dcctl install` actualiza, así que ejecútalo antes de actualizar la instancia, como se indica arriba. Si vienes de `v0.27.0` o anterior, lee primero las [notas de `v1.0.0`](#v100-upgrade) y sigúelas con `--version v1.1.0`.

```bash
dcctl install local --version v1.1.0
dcctl upgrade local devicechain --version v1.1.0 --dry-run
dcctl upgrade local devicechain --version v1.1.0
```

Se aplica la [política de versiones y actualización](./versioning-policy.md). Esta versión amplía las interfaces cubiertas (nuevos comandos y banderas de `dcctl`, nuevas consultas GraphQL, nuevos campos de `Instance`) y no elimina ninguna. Un campo GraphQL queda [obsoleto](#v110-deprecations) y se mantiene hasta la próxima versión mayor.

Lo que exige actuar, en el orden en que hacerlo:

- **Ten presente que los flujos por evento ahora se dimensionan por un margen de caída.** `inbound-events`, `resolved-events` y el flujo de captura de dispositivos se dimensionan por minutos de eventos a una tasa de diseño, en lugar de 1 GiB fijo. Con los valores por defecto del chart los límites no cambian. Si defines `streamDesignRate`, la actualización los amplía in situ y el volumen de JetStream tiene que alojar el resultado; `dcctl` comprueba la reserva frente al volumen antes de escribir nada y nombra el déficit. Consulta [Los flujos se dimensionan por margen de caída](#v110-runway).
- **Reducir el límite de un flujo se rechaza salvo que lo pidas.** Si un archivo de valores tuyo fija un límite por debajo de lo que un flujo contiene ahora, el administrador del broker se detiene y nombra el flujo y el déficit. Consulta [la protección](#v110-shrink).
- **Revisa todo lo que inicie sesión en el listener MQTT con un inicio de sesión de servicio de la plataforma.** A partir de esta versión solo el inicio de sesión propio de `event-sources` puede autenticarse por MQTT; los demás inicios de sesión estáticos de la plataforma quedan limitados al protocolo nativo del broker. Los dispositivos no se ven afectados. Consulta [Notas de actualización](#v110-upgrade-notes).
- **Ten presente que una repetición de `dcctl bootstrap` ahora conserva la autoridad de certificación del broker.** Antes generaba una nueva. Si contabas con una repetición para rotarla, ya no ocurre. Consulta [Notas de actualización](#v110-upgrade-notes).
- **Ten presente que `projection.maxBatch` de `device-state` ahora vale 64 por defecto** (antes 32). Si lo defines tú, no cambia nada. Consulta [Notas de actualización](#v110-upgrade-notes).
- **Deja de usar `DeviceState.inactivityAlarmTime`** si algún cliente lo lee; usa `lastDisconnectTime`. Consulta [Obsolescencias](#v110-deprecations).
- **Los sobres de eventos y la exposición de MQTT están desactivados por defecto** y no exigen nada al actualizar. Para usarlos, consulta [Sobres de eventos](#v110-envelope) y [Dispositivos fuera del clúster](#v110-mqtt-exposure).
- **Actualiza el enrutamiento de alertas.** Hay cuatro alertas nuevas y dos cambiadas. Consulta [Alertas](#v110-alerts).
- **Actualiza los scripts que leen `kubectl get dci`.** Las columnas cambiaron. Consulta [La instancia informa de su estado observado](#v110-status).
- **Instala el SDK de .NET 8.0.427** si compilas tú mismo el SDK de C# o los proyectos de demostración. Consulta [Cambios menores](#v110-smaller).
- **Volver atrás.** Consulta [Volver atrás](#v110-going-back).

#### La ingesta consume menos CPU por evento {#v110-ingest}

Los escritores de eventos ahora llenan sus lotes. Cada uno de los diez escritores de un pod recogía antes su propio lote de un canal compartido, así que un lote contenía solo un puñado de eventos y la espera de acumulación casi no aportaba nada. Ahora la recogida se serializa entre los escritores de un pod: solo uno a la vez conserva el derecho de recoger, únicamente mientras recibe mensajes, y lo libera antes de la decodificación, la admisión del inquilino y la confirmación, de modo que esas etapas siguen siendo paralelas. Como resultado, tanto `event-management` como `device-state` agrupan mucho más. `device-state` además escribe sus upserts calientes (latido, bloqueo o creación, la escritura diferida combinada, la última medición y la última ubicación) como una sola sentencia por lote con un arreglo por columna, mediante un nuevo modo de upsert por arreglos de columnas en la capa de base de datos compartida. Ese modo toma el inquilino del contexto de la solicitud, exige que el destino del conflicto nombre la columna del inquilino y valida sus protecciones de actualización contra una gramática de lista permitida, de modo que ofrece el mismo aislamiento por inquilino que la inserción a la que acompaña.

Medido en Google Kubernetes Engine, en `us-east4-b`, con la misma forma de carga y el mismo arnés en ambas columnas: tres nodos de servicios (`t2d-standard-4`), tres nodos de base de datos (`n2d-standard-4`), un nodo generador de carga (`n2d-standard-4`) y el broker (el original, sin modificar) en su propio grupo de tres nodos privados `t2d-standard-4`, con taint para el broker, con discos de arranque persistentes estándar de 100 GB, `premium-rwo` como clase de almacenamiento predeterminada, una instancia instalada con `dcctl install --ha` y `dcctl bootstrap --no-tls --no-escrow`, los sobres de eventos desactivados y el acuse MQTT con fallo cerrado desactivado, y una carga mixta de 500 dispositivos HTTP a 5 eventos/s (2.500 eventos/s), 1.500 sesiones MQTT sobre TLS a 1 evento/s, 500 dispositivos de detección de fondo a 4 eventos/s (2.000 eventos/s) más sondas, y comandos (unos 6.200 eventos/s almacenados), en una ventana estable de 15 minutos. La primera columna no son las imágenes publicadas de `v1.0.0`: es la última compilación de la rama principal anterior a los cambios en los escritores. Con el broker en los mismos nodos, esa compilación midió lo mismo que la versión candidata de `v1.0.0`; la tabla usa la forma con grupo dedicado, donde la CPU del broker en esa ejecución es una cota superior por la colocación desigual de los líderes.

| Carga mixta, unos 6.200 eventos/s | Antes (última compilación anterior a los cambios en los escritores) | v1.1.0 |
| --- | --- | --- |
| CPU de la plataforma por 1.000 eventos | 2,60 CPU-s | 2,06 CPU-s (21 % menos) |
| CPU de la base de datos relacional por 1.000 eventos | 0,402 CPU-s | 0,241 CPU-s (40 % menos) |
| CPU del almacén de eventos (TimescaleDB) por 1.000 eventos | 0,377 CPU-s | 0,193 CPU-s (49 % menos) |
| CPU de `device-state` por 1.000 eventos | 0,154 CPU-s | 0,064 CPU-s (58 % menos) |
| Filas por `INSERT` en `events` | 8,4 (medido con el broker en los nodos de servicios) | 41,9 |
| Latencia de persistencia, mediana / percentil 99 | 21 ms / 167 ms | 17,6 ms / 107 ms |

La latencia de persistencia no aumentó. No se muestran las mediciones que no se repitieron con esta configuración, como una carga solo MQTT con 3.000 sesiones.

Con la misma forma de carga, con cada paso en su propio inquilino nuevo tras un calentamiento de 3 minutos y medido en una ventana estable de 11 minutos, MQTT sobre TLS a un evento por sesión y segundo se mantuvo por debajo de 50 ms en el percentil 99 del tiempo de confirmación hasta 5.250 eventos/s y lo superó a 6.000 (60,1 ms en la ejecución; 55,7 ms como media de los percentiles 99 de 10 segundos de la ventana estable). En ese punto un servidor del broker era el recurso más ocupado, mientras que los nodos de servicios y de base de datos tenían margen de sobra. Son ejecuciones únicas, y las cifras describen esta forma de carga, no una garantía de capacidad.

Otros dos cambios acortan la ruta de escritura del almacén de eventos. El identificador de cada evento ahora se calcula directamente en lugar de a través de un documento JSON; los identificadores no cambian ni un byte, así que un evento reentregado se sigue reconociendo. Y un lote se escribe primero con un `INSERT` simple, recurriendo a `ON CONFLICT DO NOTHING` solo cuando aparece un duplicado, lo que elimina el segundo registro de log que el árbitro escribe por cada fila. El WAL del almacén de eventos por evento es aproximadamente un 8 % menor en una medición aislada. El nuevo contador `devicechain_eventmanagement_persist_insert_fallbacks_total` muestra con qué frecuencia un lote recurre al respaldo; se espera que sea poco frecuente.

Ajustes y métricas: `projection.lingerMillis` de `device-state` ahora vale 10 por defecto (un `0` explícito sigue siendo `0`). Las nuevas métricas `persist_collect_full_total` y `persist_handoff_depth` en `event-management`, y `state_collect_full_total` y `state_handoff_depth` en `device-state`, muestran si los lotes se están llenando. `projection.maxBatch` de `device-state` ahora vale 64 por defecto (antes 32). Un lote recogido se divide por inquilino y la parte de cada inquilino es un upsert, de modo que con varios inquilinos en paralelo un tope de 32 dejaba solo una fracción de él en cada sentencia. A 4.500 eventos MQTT/s, 64 frente a 32 subió las filas por lote de `device_states` de 31,4 a 49,4, redujo la CPU de `device-state` un 10 % y la de la base de datos relacional un 15 %, y subió la mediana del tiempo de traspaso de `device-state` de 8,0 a 13,2 ms porque los lotes esperan a llenarse, con el percentil 99 sin cambios. Una carga con muchos inquilinos sigue viendo lotes más pequeños que una de un solo inquilino. Define `maxBatch` explícitamente para conservar 32.

#### Los flujos se dimensionan por margen de caída {#v110-runway}

Los flujos que transportan un mensaje por evento ingerido tenían un tope fijo de 1 GiB y 5 millones de mensajes. Ese tope es el margen de la plataforma durante una caída de un consumidor o de la base de datos, y el horizonte que pueden releer la vista previa de reglas y la reproducción de detección. A 20.000 eventos por segundo retenía `resolved-events` durante unos 2,8 minutos.

- **Dos valores nuevos del chart.** `instance.config.infrastructure.nats.streamDesignRate` es el pico de eventos por segundo que se espera que ingiera toda la instancia, sumando inquilinos. Es la tasa para la que se dimensiona el disco, no un límite de tasa. `instance.config.infrastructure.nats.streamRunwayMinutes` es cuántos minutos de eventos a esa tasa retienen los flujos (15 por defecto, entre 10 y 10080). Cada uno de los tres flujos recibe al menos tasa × minutos × 60 × su coste por evento (384 bytes para captura y entrada, 448 bytes para resueltos). `streamMaxBytes` sigue siendo el mínimo y `streamMaxMsgs` se eleva para que el número de mensajes no acorte el margen.
- **Los valores por defecto dejan los límites donde estaban.** Sin una tasa definida, los flujos se dimensionan como antes. El valor propio del chart para `streamDesignRate` es 0 (sin dimensionar por tiempo); el perfil estándar de `dcctl` aporta 1.000 eventos por segundo durante 15 minutos, que sigue cabiendo en el límite de 1 GiB, y `--compact` aporta 50 por segundo, que conserva 64 MiB. Los valores fuera de rango hacen fallar el arranque.
- **Para definirlos,** pon las dos claves bajo `instance.config.infrastructure.nats` en un archivo de valores y ejecuta `dcctl upgrade <provider> <instance> --values <file>`. Los flujos existentes toman los nuevos límites in situ. Un flujo dimensionado para 20.000 eventos por segundo durante 15 minutos necesita unos 33 GiB de volumen de JetStream; [cuánto pueden esperar los flujos de eventos durante una caída](./observability.md#outage-runway) tiene la tabla completa.
- **La comprobación previa de la reserva nombra el margen.** Cuando lo que no cabe en el volumen es el margen, `dcctl` falla antes de escribir nada y lo dice.

##### Reducir el límite de un flujo exige una bandera explícita {#v110-shrink}

Reducir el límite de un flujo por evento por debajo de lo que contiene hace que el broker descarte de inmediato sus mensajes más antiguos, incluidos los no leídos. El administrador del broker ahora lo rechaza, nombrando el flujo y el déficit. Cuando lo hagas a propósito, por ejemplo en una actualización que acorta deliberadamente el margen, indícalo una vez y quítalo después:

- valor del chart `brokerAdmin.allowCeilingShrink: true` (`false` por defecto),
- `dcctl broker reconcile --allow-ceiling-shrink`.

Elevar un límite, o reducirlo a algo bajo lo cual el flujo todavía cabe, no requiere nada.

#### Los dispositivos MQTT dentro de su límite no se descartan tras un bloqueo de la plataforma {#v110-rate-gate}

`event-sources` mide la ingesta de cada inquilino frente a su límite. Un mensaje que había esperado en el flujo de captura más de cinco segundos fijos se medía en el momento en que el dispositivo lo envió, y uno más reciente en el momento en que se leyó. Cuando el salto de captura a entrada se bloqueaba unos segundos, todo el tráfico en cola se medía en el instante en que terminaba el bloqueo, la ráfaga permitida del inquilino absorbía solo uno o dos segundos y el resto se descartaba aunque el inquilino estuviera dentro de su límite y el broker ya hubiera confirmado los mensajes al dispositivo.

- **El umbral es por inquilino.** Un mensaje cuenta como acumulado cuando su antigüedad supera el menor entre los cinco segundos anteriores y la ráfaga que el límite del inquilino puede absorber (ráfaga dividida por tasa), y entonces se mide en el momento de su envío. La decisión se toma una vez por mensaje y se entrega a las dos etapas de medición, de modo que la etapa de mensajes y la de lecturas no pueden encaminar un mismo mensaje a limitadores distintos.
- **Sin cambios.** El tráfico HTTP y otro tráfico no confiable, las integraciones y la fuente MQTT externa se miden en el momento de la lectura como antes. Un inquilino que realmente supera su límite sigue siendo limitado.
- **Nuevo contador y alerta.** `total_msg_shed_acknowledged{source}` cuenta los mensajes descartados después de que el broker los hubiera confirmado, solo para fuentes MQTT, y se exporta a 0. La alerta `MqttIngestShedAfterAcknowledgement` (advertencia) se activa con `increase(...[10m]) > 0` sin `for:`, de modo que una ráfaga que dura segundos y se detiene también se ve. Se mantiene diez minutos después de la última. Un inquilino dentro de su límite no descarta nada, así que en régimen estable la serie está en 0; un inquilino por encima de su límite la eleva, y debe hacerlo.

#### Alertas {#v110-alerts}

- **`JetStreamOutageRunwayShort`** (advertencia) es nueva. Lee con qué rapidez crece la acumulación sin leer de un consumidor que condiciona y se activa cuando, a ese ritmo, el flujo empezará a rechazar eventos nuevos en menos de veinte minutos (los rechaza al 90 % del flujo). La acumulación debe superar el 10 % y la condición debe mantenerse dos minutos. Una acumulación plana o que se vacía permanece en silencio. Su texto indica subir `streamDesignRate` junto con el volumen de JetStream cuando el tráfico ha superado la tasa de diseño.
- **`JetStreamUnreadBacklogNearFull`** ahora se activa al 75 % durante un minuto (antes al 80 % durante cinco minutos). Un flujo dimensionado para un margen de R minutos llega al 75 % en 0,75R y rechaza en 0,9R, así que el umbral anterior se activaba después del rechazo para cualquier margen inferior a unos 17 minutos.
- **`MqttIngestShedAfterAcknowledgement`** (advertencia) es nueva. Consulta [más arriba](#v110-rate-gate).
- **`BrokerCertificateExpiringSoon`** (advertencia, de 30 a 7 días) y **`BrokerCertificateExpiryCritical`** (crítica, menos de 7 días o caducado) son nuevas. Leen la caducidad que el listener TLS del broker está sirviendo en este momento (`dc_broker_mqtt_tls_cert_not_after_seconds`), de modo que una renovación que el broker nunca recogió sigue alertando.
- **`JetStreamReplicaPeersDegraded` lee pares en línea,** y el retraso tiene su propia alerta, **`JetStreamReplicaPeersLagging`**. La comparación anterior contaba igual a un seguidor momentáneamente rezagado que a uno ausente, así que bajo carga se rearmaba una y otra vez. Un nuevo indicador `jetstream_peers_online` cuenta el líder más cada par que no está fuera de línea.
- **`DeviceX509IssuerPinUnavailable` ya no pasa a pendiente en un reinicio del broker.** Un parpadeo de dos segundos de la cuenta de sistema del broker durante un reinicio dejaba la alerta pendiente unos dos minutos. Ahora la espera está en la expresión: la tasa de rechazos debe ser distinta de cero en cada una de las últimas diez evaluaciones de un minuto. Una caída real sigue activándola tras unos diez minutos.

#### La instancia informa de su estado observado {#v110-status}

El operador ahora escribe el estado **observado** en cada `Instance` y es su único escritor. Antes, `kubectl get dci` mostraba la intención de dcctl en la columna `Phase` y el `status` del objeto nunca se escribía.

```
NAME   PROVIDER   CLUSTER           PROFILE   REQUESTED   OPERATION   READY   RUNNING   AGE
prod   local      kind-devicechain  default   1.1.0       Ready       True    1.1.0     12d
```

- **Columnas cambiadas.** `Phase` pasa a llamarse `Operation`, `Version` pasa a llamarse `Requested` para no confundirla con la nueva columna `Running`, y se añaden `Ready` y `Running`.
- **Contenido del estado.** `status.observedGeneration`, las condiciones `WorkloadsAvailable` y `RolloutComplete` con sus motivos, `status.version.requested` y `.running`, y una entrada `status.areas[]` por área funcional (réplicas deseadas, listas y actualizadas, imagen). No se guarda nada por inquilino ni por dispositivo. El controlador vigila los Deployments de la instancia, así que el estado sigue a un despliegue. El operador gana `get`, `list` y `watch` sobre `apps/deployments`, y su caché de gestor se restringe a los Deployments etiquetados con la instancia. El CRD de Instance se entrega con el operador, no en el chart de Helm.
- **Caducidad del certificado del broker.** Una nueva condición `BrokerCertificateValid` informa de la hoja TLS del broker y de su autoridad: `CertificateValid`, `CertificateExpiringSoon` (30 días o menos, sigue en True), `CertificateExpired` (False, y deja `Ready` en False), y `CertificateNotPublished` o `CertificateUnreadable` (Unknown). Gana el peor de la hoja y la autoridad. La hoja se emite por un año y se renueva dentro de `dcctl upgrade`, de modo que una instancia que no se actualizaba durante unos once meses perdía el TLS del broker sin que nada lo dijera. `dcctl bootstrap` y `dcctl upgrade` publican las dos fechas `notAfter` como anotaciones en la Instance (`core.devicechain.io/broker-certificate-not-after` y `.../broker-ca-not-after`). Una instancia antigua se completa en su próxima actualización y, hasta entonces, la condición muestra `CertificateNotPublished`.

Consulta [Estado de la instancia](./kubernetes-operator.md#instance-status) y [Caducidad del certificado del broker](./kubernetes-operator.md#broker-certificate-expiry).

#### Los eventos rechazados se pueden leer {#v110-failed-events}

Los eventos que la plataforma rechaza van a un flujo, y hasta ahora nada lo leía: «por qué no aparecen los datos de mi dispositivo» solo podía responderse con los logs de los pods.

- **Un índice de eventos rechazados.** `event-management` lee el flujo de eventos fallidos en una nueva tabla `failed_event_records`, conservada siete días (`lifecycle.failedEventRetentionDays`). Una fila contiene el inquilino, la hora, el área de origen, el motivo, el token de dispositivo reclamado, el tipo de evento, un texto de error acotado y si la carga original sigue en el flujo; la carga no se copia. Se conservan como máximo diez filas por inquilino, motivo y dispositivo en una hora, y los registros adicionales se pliegan en un contador `suppressed` de la fila más reciente, de modo que una sonda con tokens de dispositivo inventados no puede inundar la tabla.
- **GraphQL del inquilino.** `failedEvents`, `failedEventSummary` y `failedEventCount`, para los miembros que tienen `device:write` (los llamadores limitados a un cliente son rechazados). Leen como máximo 31 días atrás. Nada reproduce ni reintenta un evento, y nada en la API devuelve la carga original.
- **Consola.** Una vista de fallos de ingesta con chips de motivo, un filtro de dispositivo y un rango de tiempo, y un aviso en la página del dispositivo cuando se rechazaron eventos de ese dispositivo en las últimas 24 horas.
- **Se registran las claves de medición descartadas.** Un dispositivo que envía `{"status_text":"ok","battery":50}` recibe confirmación, `battery` se almacena y `status_text` se descarta. El [contador de `v1.0.0`](#v100-measurement-drops) indica con qué frecuencia ocurre en toda la flota; ahora `device-state` registra qué dispositivo perdió qué clave y por qué, con un recuento, la primera y la última hora y el último valor (recortado a 64 bytes), con un máximo de 32 claves por dispositivo. Se sirve mediante la consulta `droppedMeasurements` y se muestra en la página del dispositivo. El recuento indica la frecuencia, no un total exacto. Los identificadores de evento no cambian.

#### La presencia se puede consultar {#v110-presence}

- **Una línea de tiempo de presencia.** Una nueva consulta GraphQL del inquilino, `stateChangeEvents`, en `event-management` devuelve los cambios de presencia de un dispositivo (conectado, desconectado, inactivo), del más reciente al más antiguo, por token de dispositivo e intervalo de tiempo, con paginación. Necesita `event:read` y se acota mediante los anclajes del evento como las demás consultas de eventos, de modo que quienes están acotados a un cliente solo ven sus propios dispositivos. La página del dispositivo en la consola la muestra como una línea de tiempo de presencia.
- **La página del dispositivo muestra más de la conexión.** La tarjeta de conectividad ahora muestra el último informe de presencia y la sesión de transporte de la que procede.
- **Una sola ventana de inactividad.** Un dispositivo con presencia inferida se marca como inactivo tras diez minutos sin informar, en toda la plataforma; nunca fue configurable por dispositivo. El barrido se ejecuta cada 60 segundos, así que ese dispositivo pasa a desconectado entre 10 y 11 minutos después de quedar en silencio. Un dispositivo con presencia afirmada sigue sus transiciones de presencia explícitas y queda fuera del barrido. `DeviceState.inactivityTimeout` informa siempre esa ventana (600 segundos). El campo `inactivityAlarmTime` está [obsoleto](#v110-deprecations).

#### Obsolescencias {#v110-deprecations}

**`DeviceState.inactivityAlarmTime` (GraphQL de device-state) está obsoleto.** Usa `lastDisconnectTime`. Para un dispositivo inferido que está inactivo en este momento, ambos son el mismo instante. El campo obsoleto ahora devuelve ese valor y devuelve nulo para cualquier otro dispositivo, de modo que ya no conserva un episodio de inactividad pasado cuando el dispositivo vuelve a estar activo. El campo se elimina en la próxima versión mayor. `DeviceState.inactivityTimeout` no cambia de forma y ahora informa siempre la ventana de toda la plataforma (600 segundos). En esta versión no hay nada más obsoleto.

#### Dispositivos fuera del clúster {#v110-mqtt-exposure}

Los dispositivos que no están en el clúster no tenían una forma admitida de llegar al broker MQTT de la plataforma en un clúster en la nube. Ahora el listener TLS del broker en el puerto TCP 8883 se puede exponer por instancia. **Está desactivado por defecto y una actualización no cambia nada hasta que lo pidas.** La página [Conectar dispositivos desde fuera del clúster](./connecting-from-outside.md) contiene el procedimiento completo: DNS, puertos y cortafuegos, el certificado en el que deben confiar tus dispositivos, cómo comprobarlo desde fuera y la resolución de problemas.

```bash
dcctl install   <provider> --mqtt-gateway                                   # una vez por clúster, solo en modo gateway
dcctl bootstrap <provider> <instance> --mqtt-exposure gateway|loadbalancer|none --mqtt-host mqtt.example.com
dcctl upgrade   <provider> <instance> --mqtt-exposure ... --mqtt-host ...   # o --mqtt-exposure none para cerrarla
dcctl upgrade   <provider> <instance> --broker-max-connections N          # límite opcional; 0 lo elimina
```

- **Dos modos.** `gateway` enruta TLS en modo passthrough por el Gateway de la instancia según el nombre de host al que marca el dispositivo, de modo que varias instancias comparten una dirección. `loadbalancer` crea un Service `LoadBalancer` para el 8883 directo a los pods del broker, para clústeres sin soporte de Gateway API o cuando quieres un balanceador propio.
- **TLS termina en el broker, nunca en el ingreso.** Ningún modo publica MQTT en texto plano (1883), y el chart se niega a exponer un broker cuyo propio TLS esté desactivado. Los dispositivos se autentican como siempre, por credencial o por [certificado](./device-certificates.md).
- **Se rechaza antes de escribir nada:** un host ausente o mal formado, un host que ya tiene otra instancia del clúster, el modo gateway cuando el controlador de ingreso no escucha en el 8883 o la instancia no se sirve por el front end Gateway, y un host que el certificado del broker no puede incluir.
- **Un límite de conexiones.** `--broker-max-connections` limita el número de conexiones que acepta el broker, dispositivos y clientes de la plataforma juntos, frente a una avalancha de conexiones en el puerto expuesto. No está definido por defecto, lo que conserva el límite propio del servidor.
- **Se registra en la `Instance`.** `spec.mqttExposure`, `spec.mqttHost` y `spec.brokerMaxConnections` son nuevos. Una actualización sin banderas conserva lo que registran.
- **Límite.** No hay protocolo PROXY, así que en modo gateway el broker ve la dirección del controlador de ingreso en lugar de la del dispositivo, y `callout.jitSourceTrusted` debe permanecer desactivado. En modo balanceador de carga actívalo solo detrás de un balanceador que conserve las direcciones de los clientes.

#### Notas de actualización {#v110-upgrade-notes}

- **Los inicios de sesión de servicio de la plataforma ya no pueden autenticarse por MQTT.** Todos los inicios de sesión estáticos de la plataforma quedan limitados al protocolo nativo del broker, salvo el propio de `event-sources`, cuya fuente MQTT y cuyo canario de presencia hablan MQTT. Así, una credencial de plataforma filtrada no se puede presentar a través de un puerto MQTT expuesto. Los dispositivos no se ven afectados porque se autentican mediante el callout de autenticación. Si algún script o herramienta tuya se conectaba al listener MQTT con un inicio de sesión de servicio de la plataforma, dale una credencial de dispositivo o usa el protocolo nativo.
- **Una repetición de `dcctl bootstrap` conserva la autoridad de certificación del broker.** Los dispositivos la fijan, así que una repetición ahora reutiliza la autoridad existente de la instancia y emite solo un nuevo certificado hoja, que incluye el host MQTT si hay uno definido. Antes, una repetición generaba una autoridad nueva. Si contabas con una repetición para rotar la autoridad, ya no lo hace. Una instancia nueva, y una instancia destruida y reconstruida, siguen recibiendo una autoridad nueva; eso incluye una restauración en un clúster reconstruido, porque la autoridad no está en ninguna copia de seguridad de base de datos ni en el depósito de la clave raíz, y los dispositivos que la fijan necesitan entonces el nuevo `instance-ca.crt`. Una autoridad que existe pero no se puede leer hace fallar la ejecución en lugar de reemplazarse.
- **Nuevos campos de `Instance`.** `spec.mqttExposure`, `spec.mqttHost` y `spec.brokerMaxConnections` se añaden a la definición del recurso `Instance`, que se entrega con el operador, no en el chart de Helm. `dcctl install` la trae, y por eso va antes de la actualización de la instancia.
- **`projection.maxBatch` de `device-state` vale 64 por defecto.** Consulta [La ingesta consume menos CPU por evento](#v110-ingest).
- **`dcctl install` sin `--mqtt-gateway` conserva el puerto 8883** que ya tiene el controlador de ingreso, y `--mqtt-gateway=false` se rechaza mientras alguna instancia declare exposición gateway.

#### `dcctl` {#v110-dcctl}

- **`dcctl attr set|get|unset <kind> <token> <key> [value]`** escribe valores de facetas desde un script, como hace la pestaña Facetas de la consola. Los tipos son dispositivo, activo, área y cliente. `get` imprime una tabla o `-o json`. Las escrituras van al ámbito compartido, el único que leen los selectores de facetas; el tipo del valor procede de la declaración de la faceta, y un `--type` que la contradiga se rechaza. Una clave no declarada necesita `--type`. Inicia sesión como miembro del inquilino (`--tenant`, `--email`, `--password`, `--otp`) y usa las llamadas GraphQL existentes de device-management, así que la autorización es la del servidor. No incluye la importación masiva por CSV ni el cambio de nombre de un valor.
- **`dcctl events failed`** lista los eventos rechazados del inquilino, del más reciente al más antiguo, con `--reason`, `--device`, `--since` y `--until` (RFC 3339 o una duración como `6h`), `--limit` (100 por defecto; la salida indica cuándo coincidieron más) y `-o table|json`. `--summary` imprime los recuentos por motivo y los dispositivos principales, por defecto de las últimas 24 horas. Inicia sesión como miembro del inquilino y necesita `device:write`.
- **`dcctl sim create --tier <token> --tier-config '<json>'`** crea el nivel que nombra una ejecución de carga si no existe, y solo lo sube si existe (nunca lo baja). Por ejemplo, `--tier rc-100k --tier-config '{"ingestReadingsPerSecond":100000}'`. `--tier-config` sin `--tier` se rechaza, y `--tier` solo sigue significando un nivel existente. Un nivel ausente sin `--tier-config` ahora falla con la solución explicada.
- **`dcctl sim create` y `dcctl sim destroy` envían el host de la instancia.** Cuando `--server` es una dirección (una IP o `localhost`) y no un nombre, marcan a esa dirección y envían el host registrado de la instancia como cabecera Host y nombre de servidor TLS, de modo que un listener del Gateway ligado a un nombre de host acepta la solicitud. Antes, el inicio de sesión fallaba con un 404 que no nombraba la causa. Una nueva bandera `--host-header` sustituye el host registrado, un `--server` DNS se deja como está y el simulador respeta la misma cabecera. Si no se puede leer la declaración de la instancia y falla el inicio de sesión, el error nombra la bandera.
- **Nuevas banderas para [dispositivos fuera del clúster](#v110-mqtt-exposure):** `dcctl install --mqtt-gateway`, y `--mqtt-exposure`, `--mqtt-host` y `--broker-max-connections` en `bootstrap` y `upgrade`.
- **`dcctl upgrade --allow-envelope-downgrade`** se describe en [Sobres de eventos](#v110-envelope).
- **`dcctl broker reconcile --allow-ceiling-shrink`** se describe [más arriba](#v110-shrink).
- **`dcctl broker rebalance` funciona después de un reinicio del broker.** No encontraba el inicio de sesión del administrador del broker y, una vez corregido eso, aun así solo lograba algunos de los traslados de líder, porque el administrador podía leer flujos pero no los consumidores que toca un traslado, y el rechazo se manifestaba como un tiempo de espera agotado. El administrador ahora puede leer esos consumidores; nada más de sus permisos cambia. `dcctl upgrade` ejecuta el mismo paso al final.
- **`dcctl install` repara un certificado atascado del plugin Barman Cloud.** En un clúster nuevo, cert-manager podía dejar sin emitir uno de los dos certificados del plugin tras una espera de una hora, de modo que el plugin se quedaba en `ContainerCreating` hasta que se agotaba la espera de Helm. Ahora la instalación vigila los dos certificados durante la aplicación de los requisitos previos y, si uno sigue sin estar listo tres minutos después de crearse, lo elimina y lo vuelve a crear.
- **`dcctl --values` admite los interruptores del perfilador:** `functionalAreas.<area>.profiler.enabled`, `.blockRate` y `.mutexFraction`. `profiler.address` no se admite deliberadamente, porque un bind no loopback expone perfiles sin autenticación; recógelos mediante un port-forward.

#### Interruptores de perfilado {#v110-profiling}

Todos están desactivados por defecto y están pensados para investigar el rendimiento. Los perfiles de bloqueo y de mutex siempre salían vacíos porque el muestreo nunca se activaba; con `profiler.blockRate` y `profiler.mutexFraction` definidos, los endpoints los sirven. Cualquiera de los dos sin `profiler.enabled` rechaza el arranque, y el chart lo rechaza al renderizar. La raíz de OpenTofu del clúster gana `postgres_statement_stats` para `pg_stat_statements` en la base de datos relacional, y la raíz de la instancia gana `nats_profiling_port` y `nats_profiling_block_rate` para el broker y `timescale_statement_stats` para `pg_stat_statements` en el almacén de eventos. El módulo de monitorización también ejecuta el node exporter en los grupos de nodos con taints (una toleration para cada taint), de modo que un grupo de broker dedicado ya no falta en la CPU de nodos y no activa `KubeDaemonSetRolloutStuck` ni `TargetDown`.

#### Cambios menores {#v110-smaller}

- **SDK de .NET 8.0.427.** El SDK fijado pasa de 8.0.425 a 8.0.427 (runtime 8.0.33) por un aviso publicado sobre el decodificador Brotli de .NET que llevaban los paquetes de runtime anteriores. Todos los proyectos .NET con bloqueo, incluidos el SDK de C#, su host AOT y los proyectos de demostración y simulador, se vuelven a bloquear con los paquetes 8.0.33; solo cambian versiones y hashes de contenido. Si compilas alguno tú mismo, instala antes el SDK 8.0.427.
- **El espacio de trabajo del frontend fija `minimatch` en 11.0.1 o posterior** por un aviso publicado, y se incluyen las actualizaciones rutinarias de Go, npm y de imágenes base de contenedores.
- **Documentación.** Nuevo: la página para conectar dispositivos desde fuera del clúster, la sección de estado de la instancia, la sección Sobres de eventos, la página del margen de caída, una sección «Desde la línea de comandos» en la guía de eventos fallidos y una sección de scripting para facetas. La página de presencia ahora indica la única ventana de inactividad de toda la plataforma. La página de rendimiento medido añade la fila de carga mixta de sesenta minutos para `v1.0.0`. El encabezado de las notas de versión ya no lleva la advertencia previa a la 1.0.
- **Un pulso de una sola muestra** tiene su correspondencia con flancos ascendentes fijada por una prueba; el comportamiento no cambia.

#### Sobres de eventos (opcionales) {#v110-envelope}

Un sobre de eventos lleva varios eventos de un mismo inquilino en un único mensaje de los flujos de eventos de entrada y resueltos, en lugar de un mensaje por evento, lo que reduce el trabajo que hace el broker por evento. Lo activan dos ajustes, y **ambos están desactivados por defecto**: `infrastructure.messaging.envelopes.resolved` e `infrastructure.messaging.envelopes.inbound`. Con ellos desactivados el formato del flujo no cambia y no hay nada que hacer. Todos los servicios de esta versión leen sobres y mensajes de un solo evento por igual, digan lo que digan los ajustes; los ajustes deciden únicamente si un servicio los escribe. Las reglas para activarlos, lo que cuesta la protección contra duplicados del motor de detección y la protección contra la vuelta atrás están en [Sobres de eventos](#event-envelopes).

- **Cuándo es seguro.** Una vez que todos los pods de la instancia ejecutan `v1.1.0` o posterior, y no antes: un pod de una versión anterior aparta un sobre como fallido. Actualiza primero, espera a que todos los pods se hayan reiniciado y después define los ajustes.
- **Sobres de entrada.** `event-sources` agrupa los eventos de un inquilino con una espera corta y predictiva: un inquilino tranquilo publica de inmediato en la forma de un solo evento, y uno ocupado espera como máximo `inboundLingerMillis` (5 ms por defecto, máximo 10) al siguiente evento. `HTTP /events` responde 202 solo después de que el broker haya confirmado el mensaje que contiene el evento. Después, `device-management` escribe un sobre resuelto por cada sobre de entrada.
- **El motor de detección omite duplicados.** Con los ajustes activados, el motor de detección recuerda la identidad de cada evento aplicado durante el intervalo de reentrega de la propia plataforma (unos diez minutos de tiempo del broker) y omite una segunda copia, contada en `detect_duplicate_events_skipped_total`. La memoria es de unos 190 MB a 6.000 eventos/s, 300 MB a 12.000 y unos 540 MB a 20.000 (extrapolado), solo en el líder de detección; el chart sube el límite de memoria de `event-processing` mientras un ajuste está activado (consulte [Dimensionamiento de los servicios](./bootstrap.md#service-sizing)). Una copia que llegue después de ese intervalo se aplica dos veces; [Sobres de eventos](#event-envelopes) indica el residuo.
- **Métricas nuevas:** `inbound_envelope_members{hop}` (eventos por sobre) e `inbound_envelope_linger_seconds{hop}`, los contadores de asentamiento y las series `detect_guard_*`.
- **Medido en un clúster local, aún no en uno en la nube.** En un clúster `kind` local con tres workers a 1.500 eventos/s (1.000 por MQTT, 500 por HTTP), con ambos ajustes activados y unos ocho eventos por sobre, los mensajes del broker por evento bajaron un 41 % (de 116,5 a 68,2), los bytes del broker por evento entre un 37 % y un 40 %, y la CPU total de la plataforma por evento un 12 % (de 7,48 a 6,60 CPU-s por 1.000 eventos). `device-management` usó un 31 % menos de CPU por evento, `device-state` un 24 % menos, el motor de detección un 47 % menos y el broker un 14 % menos. El almacén de eventos hizo más trabajo, no menos: su CPU por evento subió un 23 % y sus filas por `INSERT` bajaron de 16,1 a 12,9, porque recibe lotes más pequeños y numerosos a partir de los mensajes de sobre. La latencia de persistencia y de confirmación no mostró un cambio claro dentro del ruido del banco de pruebas. Toma las cifras como la dirección y el tamaño aproximado del efecto en una forma de carga, y mide tu propia carga antes de fiarte de ellas.
- **Desactivarlos y volver atrás.** Define ambos valores como `false`. Los sobres que ya están en los flujos permanecen hasta que caducan o se consumen. `dcctl upgrade` registra cuándo se activaron los ajustes y, cuando ambos están desactivados y el despliegue ha terminado, la posición del mensaje más reciente de cada flujo. Después se niega a pasar a una versión inferior a `v1.1.0` mientras un ajuste esté activado o mientras un flujo contenga todavía un mensaje anterior a su desactivación, y nombra cada ajuste que está activado. Para volver atrás, desactiva ambos ajustes, deja que los flujos superen la posición registrada y entonces vuelve. `--allow-envelope-downgrade` omite el rechazo con una advertencia, y un servicio de la versión anterior pierde entonces los eventos de cualquier sobre que encuentre. El administrador del broker tiene acceso de lectura a esos dos flujos para esta comprobación.

#### Volver atrás {#v110-going-back}

Dos migraciones añaden una tabla cada una (`failed_event_records` en el almacén de eventos y `dropped_measurements` en `device-state`). No hay nada que deshacer en una base de datos. Avanza siempre que puedas: una actualización fallida se arregla ejecutando `dcctl upgrade` de nuevo.

- **Sobres de eventos.** Si alguno de los dos ajustes llegó a activarse, desactiva ambos y deja que los flujos de eventos superen la posición que registró `dcctl` antes de volver a una versión inferior a `v1.1.0`. De lo contrario, `dcctl upgrade` rechaza el cambio, y una versión inferior a `v1.1.0` no puede leer un sobre. Consulta [Sobres de eventos](#v110-envelope).
- **MQTT expuesto.** Una versión inferior a `v1.1.0` no conoce los ajustes de exposición. Ejecuta antes `dcctl upgrade <provider> <instance> --mqtt-exposure none` para cerrar el puerto de forma deliberada.
- **Tamaños de los flujos.** Si vuelves a `v1.0.0` después de definir `streamDesignRate`, `v1.0.0` devuelve los flujos de eventos a sus propios límites fijos al arrancar, y un flujo que contenga más que eso descarta de inmediato sus mensajes más antiguos (`v1.0.0` no tiene protección contra la reducción). Antes de volver atrás, deja que los flujos de eventos se vacíen hasta que cada uno contenga bastante menos que su límite de `v1.0.0`, o conserva `v1.1.0`.

#### Problemas conocidos {#v110-known-issues}

- **La confirmación MQTT con cierre ante fallo está desactivada por defecto,** como en `v1.0.0`. El [transporte MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker) indica los límites medidos con ella desactivada y activada.
- **Un cambio en la configuración del broker sigue desconectando los dispositivos que admitió el callout de autenticación,** hasta que la biblioteca del broker incluya la corrección de origen. Una renovación de certificado no lo hace.

### v1.0.0 — la primera versión estable: una caída de la base de datos retiene los eventos y Traefik es el acceso por defecto {#v100-upgrade}

`v1.0.0` es la primera versión estable. Es una actualización in situ desde `v0.27.0`: `dcctl install` para el clúster y después un `dcctl upgrade` por cada instancia, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. No se ejecuta ninguna migración y no se reescribe ninguna fila, así que no hay un paso de datos más allá de la propia actualización. Si vienes de `v0.26.0` o anterior, lee primero las [notas de `v0.27.0`](#v0270-upgrade) y síguelas con `--version v1.0.0`. Una instancia creada con `v0.16.0` o anterior sigue [por debajo del límite](#pre-declaration-recreate) y se recrea, no se actualiza.

```bash
dcctl install local --version v1.0.0
dcctl upgrade local devicechain --version v1.0.0 --dry-run
dcctl upgrade local devicechain --version v1.0.0
```

Desde esta versión se aplica la [política de versiones y actualización](./versioning-policy.md): las interfaces que lista como cubiertas cambian de forma incompatible solo en una versión mayor. Controles en CI comparan cada interfaz cubierta (los formatos de cable de los dispositivos, los esquemas GraphQL, los comandos y banderas de `dcctl`, los paquetes npm y el SDK de C#) con la versión anterior, y una ruptura debe declararse en un archivo que el control lee.

Lo que exige actuar, en el orden en que hacerlo:

- **Cambia el nombre del filtro de alarmas `originator` por `originatorToken`.** En el esquema GraphQL de `device-management`, `AlarmSearchCriteria`, `AlarmCountCriteria` y `Subscription.alarmStream` recibían `originator`, mientras que `Alarm`, `AlarmEvent` y `AlarmState` ya devolvían `originatorToken`. Las tres entradas ahora reciben `originatorToken` y no hay alias: una solicitud que siga enviando `originator` se rechaza. La herramienta MCP `list_alarms` también recibe `originatorToken`. La consola y los paquetes de paneles ya están actualizados. Consulta [Los filtros de alarmas nombran al originador por su token](#v100-alarm-filter).
- **Si instalas el chart directamente con Helm (no con `dcctl`) y tu instancia la sirve ingress-nginx, define `ingress.controller=nginx` antes de actualizar el chart.** El valor por defecto del chart ahora es `gateway`. El nuevo valor por defecto genera un Gateway y sus rutas y elimina los objetos Ingress, y si el clúster no tiene un `GatewayClass` de Traefik, el host deja de responder. `dcctl` siempre indica por sí mismo el acceso, así que no le afecta. Consulta [Traefik es el acceso por defecto](#v100-front-end).
- **Si aplicas directamente la raíz de OpenTofu del clúster,** `enable_ingress_nginx` ahora vale `false` por defecto, de modo que un clúster nuevo no recibe ingress-nginx. Ponlo en `true` para conservar el controlador en un clúster que ya lo ejecuta. `dcctl install` lo hace por ti cuando encuentra ingress-nginx en el clúster. La raíz sigue negándose a quitar el controlador mientras quede un Ingress de su clase.
- **Espera que los eventos se retengan, no que se descarten, cuando una base de datos no puede aceptar escrituras.** Es un cambio de comportamiento para todo lo que vigilaba los destinos de mensajes fallidos durante una caída. Consulta [Una caída de la base de datos retiene los mensajes](#v100-store-hold) para ver las alertas que llegan con él.
- **Volver atrás.** Consulta [Volver atrás](#v100-going-back).

#### Una caída de la base de datos retiene los mensajes en lugar de descartarlos {#v100-store-hold}

En una prueba de carga con un volumen de 32 GiB para el almacén de eventos, el volumen se llenó cuando el almacén de eventos tenía unos 51 millones de registros. El primario entró en un bucle de reinicios con «No space left on device», y el arnés de carga contó 1.722.366 eventos que el broker había confirmado y la plataforma no había almacenado. La causa era un límite de entregas. Cuando el almacén de eventos rechazaba una escritura, `event-management` dejaba el mensaje sin confirmar y el broker lo reentregaba cada 60 segundos. En la quinta entrega, unos cuatro minutos después, publicaba el evento en un destino que nadie lee y lo confirmaba. Una caída en la que la base de datos respondía a cada sentencia de inmediato con un error descartaba toda la ventana en vuelo, una ventana cada cuatro minutos, mientras durara. Nada lo alertaba.

- **`event-management` retiene los eventos que el almacén rechaza.** Cuando el servidor dice que no puede aceptar escrituras (disco lleno, demasiadas conexiones, solo lectura, un error de E/S), o un error de conexión se confirma con una sonda `SELECT 1` que falla, el escritor mantiene el lote sin confirmar, avisa al broker de que sigue trabajando en él y vuelve a escribir solo cuando el almacén responde a una sonda. La acumulación espera sin leer en el flujo, donde la compuerta de contrapresión existente rechaza eventos nuevos antes de que el flujo descarte los no leídos. Un mensaje que hace fallar a su escritor mientras el almacén responde se aparta tras agotar su presupuesto de entregas, como antes. La nueva métrica es `devicechain_eventmanagement_persist_store_held_events`.
- **La misma retención cubre a los demás consumidores que descartaban en la última entrega.** En `device-management`: el resolvedor de eventos, `raise-alarm` y `device-reports`. En `command-delivery`: `command-responses`, `dead-letter-writeback` y `device-roster`. En `notification-management`: `alarm-notification`. En `update-management`: `device-reports` y `device-roster`. La proyección del estado en vivo de `device-state` se deja como está a propósito, porque puede reconstruirse desde el flujo; su consumidor de propiedad sí retiene. Cada consumidor que retiene informa `devicechain_<service>_store_held_messages{consumer}`.
- **Un aviso ni se repite ni se olvida.** Las escrituras que siguen a una notificación enviada (el registro de la notificación y quitar lo que retenían las horas de silencio) ahora se retienen en su sitio y se repiten cuando el almacén responde. Solo se reintenta la escritura; el envío no se repite. Un envío por webhook o SMTP no puede ser exactamente una vez: si el servicio se detiene entre el envío y la escritura, el registro se pierde y la pérdida se registra en el log.
- **Cada reinicio de un servicio que retiene consume una de las cinco entregas de un mensaje.** Una retención que sobrevive a varios reinicios aún puede llegar a la última entrega.
- **Cinco alertas nuevas.** `DatabaseVolumeFillsWithinDay` (advertencia) salta cuando un volumen del almacén de eventos con menos del 80 % libre estará lleno en 24 horas al ritmo de las últimas dos horas. `DatabaseVolumeCritical` (crítica) salta cuando cualquier volumen de base de datos supera el 95 % de uso. `EventStoreRefusingWrites` (crítica) salta cuando `event-management` retiene eventos que su base de datos rechazó. `EventPersistBacklogGrowing` (advertencia) salta cuando la acumulación supera 1.000 y ha crecido de forma sostenida durante diez minutos. `ConsumerHoldingForDatabase` (crítica, por servicio y consumidor) cubre los demás servicios. Las alertas existentes de volumen y de JetStream no cambian. Consulta [Cuando el almacén de eventos no puede aceptar escrituras](./observability.md#event-store-down) y [Cuando la base de datos de otro servicio no puede aceptar escrituras](./observability.md#service-store-down).
- **Tres métricas de persistencia nuevas** dan la latencia por fila almacenada: `persist_rows_total`, `persist_batch_rows` y `persist_commit_duration_seconds` (cubos de 1 ms a 2,5 s). `persist_duration_seconds` no cambia; es por mensaje e incluye la espera del lote, por lo que sube con el número de filas de una confirmación.
- **Guía de dimensionamiento.** Una fila del almacén de eventos midió unos 600 bytes sin comprimir. [Dimensionar el almacén de eventos](./tuning-an-instance.md#event-store-sizing) da el multiplicador que añaden las métricas calculadas, la retención y la compresión, y cómo ampliar el volumen en caliente.

La retención se probó de extremo a extremo en un clúster kind, con el almacén de eventos en solo lectura durante siete minutos bajo 200 eventos por segundo de MQTT QoS 1. Dos veces, cada uno de los 144.000 eventos se almacenó una sola vez y ninguno de los cuatro destinos de mensajes fallidos recibió un mensaje. La misma caída contra la compilación anterior a la retención perdió unos 37.500. Esa comparación se ejecuta como `hack/outage-rig.sh`. Una caída de fallo lento (conexiones que se cuelgan en lugar de fallar) frena a los escritores en vez de agotar las entregas, de modo que lo que perdía la compilación anterior dependía de su forma; la retención cubre ambos casos.

El límite de este cambio: un evento que el broker ha confirmado a un dispositivo MQTT pero que sigue en el flujo de captura puede expulsarse cuando ese flujo está lleno, salvo que la confirmación MQTT con cierre ante fallo esté activada. Consulta [Problemas conocidos](#v100-known-issues).

#### La detección es más rápida y un dispositivo recreado puede volver a generar alarmas {#v100-detection}

- **Una detección se publica en un tick.** Una detección disparada esperaba hasta el siguiente punto de control (10 segundos por defecto) antes de pasar a la etapa de reacción, así que en un flujo tranquilo la alarma de una regla y su `sendCommand` llegaban entre 0 y 10 segundos después de la lectura. La alarma y el comando siempre se escribían con milisegundos de diferencia. Ahora las detecciones pendientes se publican en cada tick. La publicación anticipada no confirma ni reconoce nada; tras una caída, las mismas detecciones se derivan de nuevo con la misma identidad y la ventana de duplicados del flujo las colapsa.
- **Un dispositivo eliminado y recreado con el mismo token vuelve a generar alarmas.** El motor conservaba el pestillo de alarma, los cursores, las ventanas y los temporizadores de un token después de eliminar el dispositivo, de modo que la primera lectura en incumplimiento del dispositivo recreado encontraba la regla ya activada y no generaba nada hasta que salía de la condición y volvía a entrar. Ahora el estado se descarta al eliminar el dispositivo y se reconcilia al inicio de cada mandato de liderazgo a partir del registro duradero, con lo que también se cubre una eliminación que estaba en cola cuando terminó un mandato.
- **Los recuentos de disparos de reglas son estadísticas, no un mínimo.** La página del motor de detección decía que un recuento inflado por una reproducción debía leerse como cota inferior. Puede leerse alto tras una reproducción, y los contadores de salud de las reglas también pueden quedarse cortos, así que ninguna cota se sostiene. Usa las alarmas y los comandos, colapsados por identidad, para una cuenta exacta. Además, una fila almacenada una vez no prueba una única acción física, porque la entrega a un dispositivo o a un receptor puede repetirse.

#### Las mediciones descartadas ahora se cuentan {#v100-measurement-drops}

Un dispositivo que publica un valor de texto junto a lecturas válidas, por ejemplo `status_text` con `"ok"` junto a `battery` con 50, recibe confirmación y las lecturas válidas se almacenan; la entrada de texto se descartaba con solo una advertencia en el log de `device-management`. El contador `resolve_measurement_dropped_total{reason}` ahora las cuenta, con los motivos `undeclared_non_numeric`, `undeclared_unstorable` y `declared_not_storable`. La advertencia se registra una vez por tenant, dispositivo, métrica y motivo, y dice que el resto del evento se almacenó. El contrato no cambia: el evento no se rechaza, y una métrica declarada con un valor de tipo incorrecto sigue enviándose a mensajes fallidos. Un valor verdadero/falso pasa a 1 o 0 solo para una métrica declarada booleana.

#### Los filtros de alarmas nombran al originador por su token {#v100-alarm-filter}

Toda otra referencia a un token en la superficie GraphQL se llama `...Token`, y `Alarm`, `AlarmEvent` y `AlarmState` exponen `originatorToken`, pero los filtros de alarmas recibían `originator`, así que un script que reutilizaba el nombre de un campo como nombre de filtro fallaba. Ahora los filtros coinciden:

| Entrada | Antes | Ahora |
| --- | --- | --- |
| `AlarmSearchCriteria` | `originator` | `originatorToken` |
| `AlarmCountCriteria` | `originator` | `originatorToken` |
| Argumento de `Subscription.alarmStream` | `originator` | `originatorToken` |

`originatorType` no cambia y sigue siendo obligatorio cuando se da un token. Las tres eliminaciones están declaradas en el archivo de rupturas del control de compatibilidad de GraphQL. Un cliente tipado regenerado a partir del esquema no compila hasta que use el nombre nuevo.

#### Traefik es el acceso por defecto; ingress-nginx queda obsoleto {#v100-front-end}

El proyecto Kubernetes ha retirado ingress-nginx. Un clúster nuevo ahora lo sirven solo Traefik y la Gateway API, y ingress-nginx queda obsoleto. Nada se mueve en silencio: `dcctl` indica el acceso de forma explícita, así que el valor por defecto del chart solo afecta a las instalaciones hechas directamente con Helm.

- **Un clúster que solo tiene ingress-nginx** sigue sirviendo sus instancias con él, y `dcctl upgrade` imprime un aviso de obsolescencia en cada ejecución.
- **Un clúster que tiene ambos controladores** no cambia respecto a `v0.26.0` y `v0.27.0`. Una instancia recibe un Gateway junto a sus objetos Ingress, mover el DNS te corresponde a ti, y `--drop-legacy-ingress` y `--remove-legacy-ingress` siguen protegidos.
- **Una elección registrada de `--ingress-controller nginx` se conserva** entre actualizaciones.
- **`dcctl install` sobre un clúster que tiene ingress-nginx lo conserva.** Pasa `enable_ingress_nginx=true` para que el nuevo valor por defecto no se lea como una eliminación.
- **La opción `nginx` se mantiene una versión menor más.** Aprovecha ese tiempo para [mover la instancia a Traefik](./ingress-cutover.md).

#### Instalar y ejecutar `dcctl` {#v100-dcctl}

Hallado al instalar desde la versión pública en una máquina Windows limpia con Docker Desktop.

- **`dcctl preflight` comprueba los puertos del host 80, 443 y 1883.** Un puerto que mantiene otro contenedor hace fallar la comprobación, antes de que `dcctl install` falle con el mensaje propio de Docker, y la comprobación nombra quién lo tiene. Un clúster que ya los tiene pasa la comprobación. Docker Desktop ya no se informa como inadecuado, y la ausencia de contexto actual antes de la primera instalación es informativa.
- **Preflight comprueba el disco en Windows y macOS.** Comprueba el espacio libre del volumen que contiene el estado de `dcctl` y la imagen de disco de Docker Desktop (advertencia por debajo de 40 GiB, fallo por debajo de 20 GiB) e imprime el uso de disco de Docker. El límite de la VM de Docker Desktop no puede leerse desde la línea de comandos, así que la salida pide confirmarlo.
- **La URL de la consola impresa y la dirección MQTT de `dcctl sim create` salen de los puertos que el contenedor del plano de control de kind publica realmente.** Cuando no pueden determinarse, se imprime el texto anterior.
- **Un `dcctl bootstrap` interrumpido a la fuerza puede volver a ejecutarse.** Una ejecución terminada a la fuerza en el paso de infraestructura dejaba un artefacto de custodia que necesitaba `--restore-root-key` para terminar. Ahora la repetición adopta el artefacto cuando nombra la instancia, el Secret de credenciales de la instancia está en el clúster y la frase de paso lo abre. Cualquier otro caso se rechaza como antes. Un rechazo por bloqueo obsoleto ahora dice cuando quien lo mantiene es un proceso de esta máquina que ya no se está ejecutando.
- **`dcctl install` sin terminal y sin `--yes`** ya no informa de que se rechazó la creación.
- **`dc-simulator` se adjunta a cada versión,** junto a `dcctl` y `dc-edge-agent`, para Linux, macOS y Windows en `amd64` y `arm64`, con sumas de verificación y procedencia. `dcctl sim run --simulator ./dc-simulator` ya no necesita un compilador de Go, y `dc-simulator -version` informa de la versión. `dcctl sim run` publica a través del listener de ingesta de dispositivos de `event-sources`, que una instalación estándar no expone fuera del clúster, así que mantén en ejecución `kubectl -n dci-devicechain port-forward svc/event-sources 8081:8081` mientras tanto (consulta el [paso 5 de la guía del primer dispositivo](../quickstart/first-device.md#5-open-a-path-to-the-ingest-endpoint)).

#### Paneles {#v100-dashboards}

- **El panel generado a partir del perfil de un tipo de dispositivo se corrige en cuatro aspectos.** Dibuja un gráfico por unidad en lugar de todas las mediciones en un solo eje (una medición sin unidad tiene su propio gráfico; como máximo cuatro gráficos y cuatro series por gráfico). Su selector de dispositivos ofrece solo dispositivos de ese tipo, mediante un nuevo filtro opcional `candidates.deviceType` en los huecos de dispositivo. El token del panel se rellena solo en lugar de bloquear Crear con el marcador. Y un botón de comando no muestra formulario ni botón Enviar a un espectador que carece de `command:write`; muestra el motivo y conserva el historial de solo lectura.
- **Un espectador limitado a un cliente ya no provoca llamadas rechazadas.** Abrir un panel hacía una llamada `anchorDevices` rechazada por cada ancla de área y de activo antes de reintentar sin tipo de relación. Ahora el alcance del espectador se lee del token de acceso y no se envía ningún tipo de relación. El servidor no cambia, así que nada de lo que puede ver un miembro con alcance se amplía.

#### Cambios menores {#v100-smaller}

- **`update-management` dice cómo configurar el almacén de objetos.** El rechazo `OBJECT_STORE_NOT_CONFIGURED` en `createArtifact` ahora nombra `dcctl bootstrap --object-store filesystem|s3` y el valor del chart `instance.config.infrastructure.blob.backend`.
- **Las actualizaciones por aire se documentan según lo validado.** Las actualizaciones gestionadas son solo por MQTT, los dispositivos LwM2M se actualizan con comandos normales, Sparkplug no define ninguna actualización de firmware, y las oleadas, los canarios y las programaciones no están en 1.0. Un arnés repetible ahora cubre una firma que no se verifica, una discrepancia de huella al subir, una descarga reanudada, una asignación sin conexión, una cancelación, un plazo y un despliegue en pausa.
- **El vocabulario de tipos de credencial queda congelado** por una prueba con control negativo, y se quitaron seis declaraciones de ruptura de compatibilidad caducadas.
- **Guías y ejemplos nuevos.** Un [recorrido de funciones](../quickstart/feature-tour.md) sigue todo el camino desde un tipo de dispositivo hasta un usuario cliente que solo ve su propio dispositivo, y se ejecuta para un segundo sitio cambiando un único archivo de parámetros. `examples/migration-pilot` es una evaluación práctica de migrar desde otra plataforma, enlazada desde la [guía de migración](../guides/migrating-from-another-platform.md). Otros ejemplos cubren un dispositivo que inicia sesión con un certificado X.509, los uplinks de ChirpStack y una regla que genera una alarma y envía un comando a un dispositivo. La guía de inicio rápido añade un paso para obtener `dcctl` e indica qué puertos del host deben estar libres y cómo recuperarse de un bootstrap interrumpido a la fuerza.
- **Las demostraciones Sitepulse y Towerpulse vuelven a mostrar las lecturas de la plataforma.** En `v0.27.0` sus tarjetas de dispositivo en vivo nunca mostraban lo que informaba la plataforma, porque el observador compartido creaba su flujo de mediciones pero nunca lo iniciaba; los dispositivos publicaban con normalidad. Vuelva a compilar los reproductores de las demostraciones con esta versión. `dcctl sim create --manifest towerpulse` ahora aprovisiona su propio nivel de demostración, con las métricas calculadas habilitadas solo para ese nivel; las métricas calculadas siguen desactivadas en todos los niveles predeterminados.
- **Las traducciones al español se siguen por actualidad** frente a las páginas en inglés, como ya se hacía con las chinas.

#### Volver atrás {#v100-going-back}

En esta versión no se ejecuta ninguna migración, así que no hay nada que deshacer en una base de datos. Avanza siempre que puedas: una actualización fallida se arregla ejecutando `dcctl upgrade` de nuevo. Si vuelves a `v0.27.0`, un cliente GraphQL que se cambió para enviar `originatorToken` debe revertirse con ella.

#### Problemas conocidos {#v100-known-issues}

- **La confirmación MQTT con cierre ante fallo está desactivada por defecto,** como de `v0.24.0` a `v0.27.0`. Con ella desactivada, un evento que el broker ha confirmado aún puede perderse si el flujo de captura se llena. El [transporte MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker) indica los límites medidos y las cotas de pérdida con ella desactivada y activada.
- **Un cambio en la configuración del broker sigue desconectando los dispositivos que admitió el callout de autenticación,** hasta que la biblioteca del broker incluya la corrección de origen. Una renovación de certificado no lo hace.
- **Dos rutas no las cubre la retención.** Un mensaje que el lector ya había leído por adelantado respecto a un escritor que retiene, y que supera la mitad del plazo de confirmación, se descarta sin confirmar y consume una entrega, y el registrador de mensajes que llegan a su última entrega puede escribir una carta para él antes de que un escritor lo retenga. Tampoco se cubre la republicación del registrador.

### v0.27.0 — los dispositivos inician sesión con certificado y los comandos Sparkplug funcionan {#v0270-upgrade}

`v0.27.0` es una actualización in situ desde `v0.26.0`: `dcctl install` para el clúster y después un `dcctl upgrade` por cada instancia, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además del nuevo `--version`. Si vienes de `v0.25.2` o anterior, lee primero las [notas de `v0.26.0`](#v0260-upgrade) y síguelas con `--version v0.27.0`.

```bash
dcctl install local --version v0.27.0
dcctl upgrade local devicechain --version v0.27.0 --dry-run
dcctl upgrade local devicechain --version v0.27.0
```

La versión permite que los dispositivos [se autentiquen con un certificado X.509](../guides/device-x509.md), hace utilizables los comandos Sparkplug B, renueva el certificado del broker sin desconectar las sesiones de los dispositivos y da a los usuarios de cliente paneles de áreas y activos. Lo que exige actuar, en el orden en que hacerlo:

- **Si instalas el chart sin `dcctl` y el callout de autenticación del broker está activado, dale el acceso de sistema del broker.** El callout ahora comprueba que una conexión con certificado la solicitó un servidor vivo del broker, y pide esa lista a la cuenta de sistema del broker. Sin `infrastructure.nats.auth.sysUser` y `sysPassword`, device-management se niega a arrancar. Defínelos, o define `callout.issuerPinDisabled: true` para funcionar sin la comprobación (el servicio registra una advertencia). Una instalación con `dcctl` ya tiene el acceso. Consulta [Anclar las solicitudes al broker](./device-certificates.md#issuer-pin).
- **Un `dcctl bootstrap` ejecutado de nuevo sobre una instancia con fuentes Sparkplug o identidades LwM2M debe decir qué hacer con ellas.** (En una instancia en ejecución, `dcctl bootstrap` ya se niega a reconstruirla; esto cubre el reintento de un bootstrap que no terminó.) Ahora se niega salvo que pases de nuevo el mismo archivo (`--sparkplug-source`, `--lwm2m-identities`) o las retires a propósito (`--withdraw-sparkplug-sources`, `--withdraw-lwm2m-identities`). Antes, repetir el bootstrap sin el indicador eliminaba las credenciales en silencio. `dcctl upgrade` las conserva, como antes.
- **Rota tus claves precompartidas de LwM2M después de actualizar.** Consulta [las claves de LwM2M salen de la release de Helm](#v0270-lwm2m).
- **Los ajustes que añadiste a mano al bloque `lwm2m-ingest` de la release ya no se conservan** en una actualización. Ponlos en un [archivo de valores](./tuning-an-instance.md) y pásalo con `--values`.
- **Si defines `extraSecrets` en los valores del chart, su suma de verificación cambió.** `checksum/extra-secrets` ahora cubre el nombre y los nombres de clave de cada Secret, nunca los valores. Cambiar solo un valor ya no reinicia los pods; reinícialos tú cuando cambie un valor.
- **Un cliente GraphQL tipado debe aceptar `null` en `ProvisioningProfileSecretResult.issuedSecret`.** Un perfil de aprovisionamiento que nombra un ancla de confianza no tiene secreto. Todo perfil que lo tiene lo sigue devolviendo una vez, como antes.
- **Avanza desde esta versión; no retrocedas.** Consulta [Volver atrás](#v0270-going-back).

Se ejecutan dos migraciones, una en el primer arranque del servicio que la posee, y cada una puede volver a ejecutarse si se interrumpe: en `device-management`, una tabla de anclas de confianza y columnas para el ancla en las credenciales y los perfiles de aprovisionamiento; en `user-management`, dos columnas anulables en el registro de purga del tenant que guardan cuándo se estableció que los datos estaban borrados y cuándo se comprobó por última vez. Ninguna reescribe una fila.

#### Los comandos Sparkplug B funcionan y `dcctl` acepta una fuente Sparkplug {#v0270-sparkplug}

En `v0.26.0` se rechazaba una clave de comando con un punto, y los tipos de comando Sparkplug (`sparkplug.write` y los comandos de rebirth y reboot) se nombran así, de modo que no se podía crear ningún comando Sparkplug. Ahora una clave puede llevar espacios de nombres con puntos, y cada segmento sigue la misma regla de antes. Los comandos Sparkplug siguen desactivados hasta que los activas para una fuente, como en `v0.26.0`.

- **Un comando rechazado dice por qué.** Un comando rechazado antes de llegar al nodo (`FAILED` o `NOT_DELIVERED`) ahora guarda como respuesta un motivo legible por máquina, por ejemplo `commands_not_enabled`, `node_offline`, `node_rebirthing`, `metric_not_writable` o `reboot_not_allowed`. La consola explica cada uno. Lo que hayas escrito que dependiera del texto del error anterior debe usar el código de motivo.
- **Un nodo que nació antes de que existiera su tipo de dispositivo recibe una petición de rebirth.** La plataforma recuerda el nodo y le pide un rebirth 10 segundos después del registro fallido, y luego con un intervalo que se duplica hasta un minuto, hasta que se registra. Dos métricas nuevas, `birth_recovery_pending` y `birth_recovery_rebirths_total`, lo muestran.
- **`dcctl bootstrap` y `dcctl upgrade` aceptan `--sparkplug-source <archivo>`.** El archivo lista las fuentes, la contraseña del broker queda en un Secret que `dcctl` escribe por sí mismo, y las actualizaciones posteriores conservan ambos. Un bootstrap con el indicador activa el área; una actualización con él se niega si el área no está en ejecución y nombra `dcctl enable-area`. Consulta [Dar a `dcctl` una fuente Sparkplug](./edge-services.md#sparkplug-sources). Una superposición de `--values` sigue sin poder definir una fuente.

#### Las claves de LwM2M salen de la release de Helm {#v0270-lwm2m}

Hasta ahora `dcctl` ponía cada clave precompartida de dispositivo LwM2M en los valores de la release de Helm, donde `helm get values` la mostraba y el historial de Helm la conservaba en cada revisión. Ahora `dcctl` escribe las claves en el Secret `dci-<instancia>-lwm2m-identities` por sí mismo, fuera de la release, y la release solo lleva el nombre del Secret y un sello que no se deriva de ninguna clave.

- **La primera actualización traslada las claves.** Las lee de la release una vez, escribe el Secret nuevo antes de cambiar nada más, elimina la entrada antigua de `extraSecrets` y ejecuta la actualización de Helm. Los dispositivos conservan las claves que tienen. Los pods que siguen en ejecución las mantienen en su entorno hasta que se reinician.
- **Rota las claves después.** Las revisiones que Helm registró antes de esta actualización (conserva hasta diez) siguen guardando los valores antiguos, igual que cualquier copia de seguridad de la release. Ejecuta `dcctl upgrade local <instancia> --lwm2m-identities <archivo>` con claves nuevas; un archivo pasado de nuevo reescribe el Secret y reinicia los pods. Hasta que las revisiones antiguas caduquen, restringe quién puede leer Secrets en el namespace donde Helm guarda sus releases (`default`).
- **`dcctl upgrade` incorpora `--lwm2m-identities` y `--withdraw-lwm2m-identities`,** y el Secret de Sparkplug funciona igual. Un Secret que `dcctl` no escribió nunca se toca.
- **Reducir o borrar espera a la release.** `dcctl` añade claves antes de la release y quita las que nada referencia solo después de que tenga éxito. Una actualización fallida deja las claves antiguas para los pods en ejecución, y la siguiente ejecución termina el trabajo. Si el último paso falla tras una release correcta, la ejecución avisa con el comando exacto y no falla.

#### Autenticación por certificado {#v0270-certificates}

Los dispositivos pueden autenticarse en el broker de la plataforma con un certificado de cliente X.509, ya sea fijado a un dispositivo que creaste o registrado en la primera conexión mediante un perfil de aprovisionamiento por certificado. No se confía en nada hasta que añades un ancla de confianza, así que una actualización no cambia el comportamiento de ningún dispositivo. Los dispositivos con token y contraseña se conectan como antes. Lee [Actualizar una instancia que ya está en marcha](./device-certificates.md#cutover) antes de añadir la primera ancla, y [Conectar con un certificado](../guides/device-x509.md) para los pasos.

- **La petición de certificado del broker ya no nombra un emisor aceptado.** Un cliente Go o el simulador retenía un certificado cuyo emisor no figuraba en la lista del broker; ahora el broker no envía lista, así que el dispositivo presenta el certificado que tiene.
- **Cada conexión con certificado se comprueba contra los servidores vivos del broker.** Consulta [Anclar las solicitudes al broker](./device-certificates.md#issuer-pin). La nueva alerta `DeviceX509IssuerPinUnavailable` avisa ante una interrupción sostenida, no ante un reinicio del broker.
- **Con el chart llegan tres alertas y un histograma de latencia.** `DeviceX509ChainFailuresSustained`, `DeviceX509JitRefusedSustained` y `DeviceTrustAnchorsExpiringSoon` permanecen en silencio en una instancia sin anclas de confianza. `callout_duration_seconds{path,result}` y la alerta de aviso `DeviceCalloutLatencyHigh` cubren todos los caminos del callout.

#### El certificado del broker se renueva sin desconectar dispositivos {#v0270-broker}

- **Un certificado TLS renovado ya no desconecta las sesiones MQTT.** El broker aplica un certificado cambiado bajo la misma autoridad a las conexiones nuevas sin recargar su configuración, así que un dispositivo conectado sigue conectado. En una prueba, 30 sesiones mantenidas tras el callout de autenticación sobrevivieron a una rotación y la conexión siguiente recibió el certificado nuevo.
- **`dcctl upgrade` renueva el certificado hoja del broker sin reiniciarlo** cuando el único motivo es la caducidad: la autoridad no cambia y el broker ya ejecuta el número de réplicas para el que se emite el certificado. Escribe el Secret y espera hasta cinco minutos a que cada pod del broker informe de la nueva caducidad. Si no puede confirmarse, avisa y reinicia el broker, de modo que un certificado antiguo nunca queda sirviéndose tras una renovación aparentemente correcta. Un cambio de autoridad, de nombres o de número de réplicas sigue reiniciándolo.
- **Un cambio en la configuración del broker sigue recargándolo,** y una recarga sigue desconectando las sesiones de los dispositivos admitidos por el callout, hasta que la biblioteca del broker incluya una corrección de upstream que aún no está en ninguna versión. `dcctl upgrade` reinicia el broker en toda actualización que cambie su imagen o su configuración.
- **La biblioteca del broker es nats-server 2.15.2,** una versión de seguridad; entre sus correcciones, el callout de autenticación ahora respeta los tipos de conexión que permite. `dc-nats-server` se construye sobre ella. La imagen de control para volver al broker upstream es `nats:2.15.2-alpine`; el ajuste que se usa es `server_image_tag = "2.15.2-alpine"`.
- **Tras una actualización que reinicia el broker, `dcctl` reequilibra el liderazgo.** El servidor reiniciado en último lugar solía no liderar ningún stream, y el p99 del acuse de publicación se duplicaba aproximadamente hasta que el liderazgo se movía. El último paso de `dcctl upgrade` ahora mueve los líderes de streams y consumidores para igualar los servidores e imprime los líderes por servidor antes y después. Es de mejor esfuerzo: un fallo es una advertencia, nunca un fallo de la actualización. [`dcctl broker rebalance <instancia>`](../reference/dcctl.md#dcctl-broker-rebalance) hace lo mismo a petición, y la nueva alerta `BrokerLeadershipImbalanced` salta cuando un servidor lidera menos de la mitad de la media.

#### Los despliegues de ingress conservan las solicitudes {#v0270-ingress}

Detrás de un balanceador de carga en la nube, los pods de Traefik e ingress-nginx ahora esperan antes de irse (`drain_seconds`, 40 por defecto) y un pod nuevo debe estar listo durante `min_ready_seconds` (15) antes de sustituir al siguiente, de modo que un despliegue ya no pierde solicitudes mientras la comprobación de salud del balanceador se pone al día. Los valores son variables de los módulos de OpenTofu de los controladores; 0 desactiva cada uno. En GKE, un bucle de una solicitud por segundo a través de un balanceador de carga en la nube durante despliegues de Traefik no registró solicitudes fallidas, mientras que la medición anterior sí las registró. Un clúster local, sin balanceador en la nube, no cambia. En un clúster en la nube, el primer `dcctl install` que lo aplique reinicia cada controlador una vez, con el drenaje más largo. Consulta [qué cambia en el borde](./ingress-cutover.md#ingress-cutover-differences).

#### Usuarios de cliente y la consola {#v0270-customers}

- **Un usuario de cliente ve las áreas y los activos en los que están sus dispositivos visibles.** La regla está en el almacén, junto al predicado del tenant: un área o activo aparece mientras un dispositivo que el solicitante puede ver tiene una relación seguida con él, y `anchorDevices` lista los dispositivos que hay en él. Los nombres de áreas y activos son visibles para quien pueda ver un dispositivo en ellos; sus demás campos se ocultan. Nada del propio grafo de relaciones se abre.
- **Las alarmas y el historial de eventos pueden filtrarse por ancla.** `alarms`, `alarmCountsBySeverity`, `alarmCountsByDeviceType` y la suscripción `alarmStream` aceptan un `anchor` opcional (un cliente, un área o un activo), y la respuesta se limita al alcance del solicitante. Una alarma permanece bajo el área en la que estaba su dispositivo cuando se generó. Un ancla de cliente fuera del alcance del solicitante se rechaza. No hace falta migración.
- **La consola oculta lo que el servidor rechaza.** A un usuario de cliente no se le ofrecen acciones de nuevo dispositivo, importación, cancelar comando ni escritura de paneles, y una tarjeta de estadística con la última lectura se rellena con el valor almacenado más reciente en lugar de esperar al siguiente.

#### El borrado de un tenant indica cuándo se han borrado los datos {#v0270-tenant-erasure}

El registro de borrado de un tenant incorpora `erased`, `erasedAt` y `lastVerifiedAt`. La afirmación solo se hace cuando todos los almacenes llevan limpios durante la ventana de asentamiento, se retira si algún almacén retrocede, y un libro vacío se lee como «aún se está comprobando», nunca como borrado. `dcctl tenant-deletion status <token> [--wait] [--until erased|complete]` termina con código distinto de cero hasta que los datos están borrados. La métrica `tenant_purge_erased_total` cuenta las afirmaciones. Consulta [Saber que los datos están borrados](./tenant-deletion.md#erased-signal) y [Cuánto tarda borrar un tenant grande](./tenant-deletion.md#measured) para la medición y lo que no cubre.

#### El acuse MQTT con cierre ante fallo sigue siendo opcional {#v0270-fail-closed}

El acuse MQTT con cierre ante fallo (`--mqtt-fail-closed`) sigue desactivado salvo que lo actives, como en `v0.26.0`, y sigue siendo una opción hasta la 1.0. El [transporte MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker) ahora indica sus límites medidos y las cotas de pérdida con él desactivado y activado. No cambia nada para una instancia que no lo define.

#### Volver atrás {#v0270-going-back}

Avanza desde esta versión; no retrocedas. Las dos migraciones nuevas añaden una tabla y columnas que `v0.26.0` no conoce, y una instancia con LwM2M guarda sus claves en un Secret que `v0.26.0` no lee. Una actualización fallida se corrige ejecutando `dcctl upgrade` de nuevo.

#### Problemas conocidos {#v0270-known-issues}

- **Un cambio en la configuración del broker sigue desconectando a los dispositivos admitidos por el callout de autenticación,** hasta que la biblioteca del broker incluya la corrección de upstream. Una renovación de certificado no lo hace.

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

- **Tras un reinicio progresivo del broker, incluida esta actualización, el último servidor reiniciado no lidera ningún stream,** y el p99 del acuse de publicación se duplica aproximadamente hasta que el liderazgo se reequilibra. **Corregido en `v0.27.0`:** `dcctl upgrade` reequilibra el liderazgo tras reiniciar el broker; consulta [el certificado del broker se renueva sin desconectar dispositivos](#v0270-broker).
- **Una rotación de certificado TLS hace que el broker se recargue y desconecta la mayoría de las sesiones MQTT.** Los dispositivos se reconectan solos. **Corregido en `v0.27.0`:** el broker aplica el certificado renovado a las conexiones nuevas sin recargarse, así que las sesiones establecidas se mantienen. Un cambio en la configuración del broker sigue recargándolo, y una recarga sigue desconectando las sesiones de los dispositivos admitidos por el callout de autenticación hasta que la biblioteca `nats-server` del broker incluya la corrección de upstream.
- **Un despliegue del controlador de ingress pierde alrededor del 5 % de las solicitudes durante 28 a 40 segundos.** Hazlo en una ventana tranquila. **Corregido en `v0.27.0`:** ambos controladores drenan antes de que un pod se vaya y esperan antes de retirar uno antiguo, así que un despliegue ya no pierde solicitudes detrás de un balanceador de carga en la nube; consulta [qué cambia en el borde](./ingress-cutover.md#ingress-cutover-differences).

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
