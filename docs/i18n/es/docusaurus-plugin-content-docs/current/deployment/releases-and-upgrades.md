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

Ejecuta ambos con el `dcctl` de la versión a la que vas a pasar: un `dcctl` instala y actualiza solo a su propia versión, y `dcctl upgrade` se niega si la instalación del clúster la hizo uno más antiguo. Da a `dcctl install` los mismos ajustes con los que se instaló el clúster, como `--ha` o `--compact`. Se niega a cambiarlos mientras una instancia se ejecuta en el clúster; consulta [Volver a ejecutar install](./bootstrap.md#re-running-install).

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

Hay un caso que la comprobación del operador deja pasar con un aviso en lugar de rechazarlo. `dcctl install`
marca con una identidad las definiciones que aplica; un operador puesto en el clúster **a
mano** no la lleva, y `dcctl` no puede distinguir una instalación manual deliberada de una que
un `dcctl` más antiguo sobrescribió. En lugar de pasar por encima de una decisión que no puede
ver, esa comprobación imprime una nota con el comando de instalación y continúa. Si no
instalaste el operador a mano, toma esa nota como el rechazo que habría sido y ejecuta
`dcctl install` antes de seguir. La comprobación de instalación de más abajo es independiente
y sigue aplicándose: un clúster en el que `dcctl install` nunca terminó no tiene registro de
instalación, y su actualización se rechaza hasta que se ejecute `dcctl install`.

**La actualización también comprueba que la instalación se ejecutó, y solo avanza a la versión de su propio dcctl.** Las definiciones de un operador pueden seguir iguales entre versiones mientras su controlador cambia, así que la comprobación del operador por sí sola no detecta que se omitió `dcctl install`. Por eso `dcctl upgrade` lee además el registro de instalación del clúster, que nombra el `dcctl` que terminó por última vez `dcctl install`, y **se niega** cuando ese es anterior al `dcctl` que hace la actualización, cuando una instalación empezó y no terminó, o cuando el clúster no tiene un registro utilizable. El rechazo nombra el comando de instalación que hay que ejecutar antes y no cambia nada. Repetir la actualización con la versión que se instaló no se rechaza, ni tampoco un clúster instalado por un `dcctl` más nuevo. Un `dcctl` compilado desde el código fuente no se compara. Desde esta versión, un `dcctl upgrade` normal con una versión de parche más nueva de `dcctl` también necesita `dcctl install` antes.

Un `--version` explícito se rige por la misma regla, en ambos sentidos, en `dcctl upgrade` y en `dcctl install`. Se rechaza una versión **anterior** a la del `dcctl` que se ejecuta, porque su chart y su configuración de infraestructura son los de su propia versión y se planificarían alrededor de imágenes que nunca los han visto; para retroceder, cuando las notas de esa versión lo permitan, usa el `dcctl` de la versión anterior. También se rechaza una versión **más nueva** que la del `dcctl` que se ejecuta, y instalarla con este `dcctl` no ayudaría: el registro de instalación nombra el `dcctl` que ejecutó la instalación, no el `--version` que recibió. Obtén el `dcctl` de la versión más nueva, ejecuta `dcctl install` con él y después actualiza. No se ven afectados `--version` igual a la versión del `dcctl` en ejecución, ni una etiqueta de un registro propio (`--registry`). Una actualización sin `--version` solo se rechaza cuando la instancia está declarada en una versión más nueva que la del `dcctl` en ejecución. Ninguno de los dos rechazos tiene una forma de omitirse.

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

Las tres versiones menores más recientes (`v1.5`, `v1.4` y `v1.3`) se documentan completas, de la más nueva a la más antigua. También se conservan más abajo las notas detalladas de `v0.27.0` a `v0.24.0`.

### v1.5.0 — cambios de automatización revisados y aprobaciones de agentes vinculadas al inicio de sesión {#v150-upgrade}

`v1.5.0` es una actualización in situ desde `v1.4.0`: ejecuta `dcctl install` para el clúster y después un `dcctl upgrade` para cada instancia, como se describe en [Actualizaciones graduales](#rolling-upgrades). Usa **el `dcctl` de la nueva versión para ambos comandos** y pasa a `dcctl install` los ajustes del clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Se ejecutan cuatro migraciones añadidas: tres en `device-management` y una en `user-management`. Añaden la barrera de desactivación del agente y la bandeja de salida de efectos, las revisiones de automatización y su exigencia, y la columna de generación de actuación. Consulta [Cambios de esquema y vuelta atrás](#v150-going-back). Si vienes de `v1.3.0` o anterior, lee primero las [notas de `v1.4.0`](#v140-upgrade).

```bash
dcctl install local --version v1.5.0
dcctl upgrade local devicechain --version v1.5.0 --dry-run
dcctl upgrade local devicechain --version v1.5.0
```

Se aplica la [política de versiones y actualizaciones](./versioning-policy.md). Esta versión añade interfaces cubiertas y no elimina ninguna. La revisión de cambios de automatización, las herramientas de propuestas de agentes, el acceso a aprobaciones y la verificación adicional dentro de la sesión están en el [nivel Preview](./versioning-policy.md#preview-interfaces), donde aún pueden cambiar en una versión menor. Los valores de Helm no están cubiertos por la política de compatibilidad; las nuevas opciones están desactivadas por defecto.

Novedades:

- **Los cambios de automatización pueden exigir una revisión aprobada (Preview).** Compara un borrador con el historial registrado antes de publicarlo. Consulta [Revisión de cambios de automatización (preview)](#v150-automation-review).
- **Las aprobaciones de agentes se vinculan a un inicio de sesión (Preview),** con herramientas MCP de propuestas, verificación adicional dentro de la sesión y una barrera local de desactivación. Consulta [Acciones de agentes (preview)](#v150-agent-actions).
- **`dcctl` solo permite avanzar** y recuerda las opciones de uplinks MQTT y WAL relacional. Consulta [dcctl solo permite avanzar](#v150-dcctl).
- **La configuración rechaza valores inválidos y las entradas de la API tienen límites.** Consulta [La configuración falla de forma segura](#v150-configuration), [Límites de entrada de GraphQL](#v150-input-limits) y [Comprobaciones más estrictas en operaciones existentes](#v150-tightenings).
- **La aceptación de una cola LoRaWAN ya no afirma que el dispositivo respondió.** Consulta [LoRaWAN y comandos](#v150-lorawan).
- **El trazado sigue un evento entre servicios,** desactivado por defecto. Consulta [Observabilidad](#v150-observability).
- **Las actualizaciones parciales conservan las ediciones simultáneas.** Consulta [Ediciones simultáneas conservadas](#v150-concurrent-edits) y [Otros cambios](#v150-other-changes).

#### Antes de actualizar {#v150-before-you-upgrade}

Lo que requiere una acción, en el orden en que debes hacerlo:

- **Nota de actualización:** Planifica avanzar o restaurar una copia de seguridad. `dcctl upgrade` y una repetición de bootstrap rechazan una versión anterior a la declarada en la instancia; `dcctl install` rechaza un `dcctl` anterior al último instalador del clúster. No hay excepción. Usa la herramienta de la versión de destino, instala con los ajustes del clúster y después actualiza cada instancia.
- **Nota de actualización:** Corrige los valores que versiones anteriores aceptaban antes de reiniciar. El esquema del chart y la validación de overlays de `dcctl` rechazan al renderizar los días de claves de firma fuera de rango, los límites de ingesta negativos, los ajustes negativos de entrega de comandos, los segundos que desbordan y los tokens LwM2M mal formados. Los servicios también los rechazan al arrancar. Revisa `functionalAreas.user-management.config.auth.signingKeyMaxAgeDays` y `signingKeyRetentionDays`: el rango permitido es 0–106751. `-1` ya no desactiva la rotación; usa `0` para la edad máxima (retención `0` toma el valor por defecto de 8 días). En `event-sources`, `lwm2m-ingest` y `sparkplug-ingest`, elimina los valores negativos de `ingestRateLimit.readingsPerSecond` o `ingestRateLimit.burst`. En `command-delivery`, elimina los valores negativos de `defaultCommandTtlSeconds`, `heldCommandCeiling`, `sweepIntervalSeconds`, `maxDispatchFailures` o `deliveryMachineryReserve`. Cero o ausencia mantienen sus valores por defecto.
- **Nota de actualización:** Comprueba que los ajustes en segundos no superen 9223372036: `updates.sweepIntervalSeconds`, `updates.deadlines.*`, `deliverySeconds`, `stateRetentionSeconds`, `attemptRetentionSeconds`, `retentionSweepSeconds` y `escalationSweepSeconds`. Corrige los `deviceTypeToken` de LwM2M para usar letras, dígitos, guiones y guiones bajos, empezando por letra o dígito, incluso con `autoRegister` desactivado; pasa el archivo corregido con `--lwm2m-identities`, que también lo valida. Estas comprobaciones también se aplican al renderizar. Los valores por defecto de `dcctl` y del chart no renderizan la mayoría de estos ajustes; afectan a valores aportados por el operador, no a una instalación por defecto.
- **Nota de actualización:** Revisa la configuración escrita a mano por los rechazos que solo ocurren al arrancar: `maxOpenConnections` o `maxIdleConnections` negativos, puertos de endpoint superiores a 65535, `terminationGracePeriodSeconds` negativo, contenido tras el documento de configuración (incluso `]` o `}`) y un `aheadHoldBudgetMillis` que desborde. Corrígelos antes de reiniciar. Las instalaciones con `dcctl` y el chart no suelen producir estos documentos inválidos.
- **Nota de actualización:** Divide o desactiva reglas antes de publicar un perfil con más de 1.000 reglas de detección habilitadas. Esa publicación ahora falla con `LIMIT_EXCEEDED`; `validateDetectionRules` también rechaza más de 1.000 reglas antes de compilar ninguna. Revisa los clientes de la API frente a los [límites de entrada](#v150-input-limits) y las [comprobaciones de escritura](#v150-tightenings).
- **Nota de actualización:** Completa el despliegue antes de activar acciones de agentes (Preview). Las aprobaciones registradas antes de esta versión ya no cuentan: vuelve a aprobar. Quienes iniciaron sesión antes de actualizar deben renovar su token o volver a iniciar sesión para que su aprobación cuente. Un despliegue mixto falla de forma segura: un device-management nuevo que llama a user-management antiguo rechaza con `POLICY_UNAVAILABLE`, y user-management nuevo no cuenta aprobaciones sin vínculo de device-management antiguo. Si durante el despliegue vuelves a habilitar un inquilino deshabilitado por un pod actualizado, hazlo mediante un pod actualizado; un pod antiguo no avanza la generación y las acciones siguen rechazadas hasta hacerlo.
- **Nota de actualización:** Protege perfiles para la revisión de automatización solo después del despliegue. Las publicaciones protegidas fallan con `REVIEW_UNAVAILABLE` hasta que user-management esté en `v1.5.0`; ejecutar una revisión también necesita event-processing en `v1.5.0`. `dcctl` no ordena estas áreas por ti. Ningún perfil queda protegido al actualizar.
- **Nota de actualización:** Activa las nuevas opciones solo después del despliegue, cada una en su propia actualización: `infrastructure.tracing`, `agentActuation.enabled` del área MCP y `--mqtt-device-uplinks` si los uplinks aún no están activados. Un pod `v1.4.0` que reinicia rechaza una clave de configuración de instancia o área que no conoce. El trazado desactivado se omite del documento, por lo que el valor por defecto es seguro. Los uplinks MQTT ya estaban disponibles en `v1.4.0`; la nueva opción registra la elección.
- **Nota de actualización:** Si un id de instancia escrito a mano no cumple la gramática del chart (letras minúsculas, dígitos y guiones interiores, hasta 50 caracteres), planifica recrearlo con un id válido: los nombres derivados de broker o base de datos pueden colisionar. El arranque sigue funcionando con una advertencia y el indicador de cada área (por ejemplo `devicechain_devicemanagement_instance_id_noncanonical`) en 1. Las instalaciones con `dcctl` y el chart ya cumplen.

Nuevos valores de Helm y ajustes de `dcctl`, todos desactivados o sin cambios por defecto:

| Valor | Por defecto | Qué hace |
| --- | --- | --- |
| `instance.config.infrastructure.tracing.enabled` | `false` | Activa el trazado OpenTelemetry; el bloque desactivado se omite del documento de instancia. |
| `instance.config.infrastructure.tracing.protocol` / `endpoint` / `insecure` | `grpc` / `""` / `false` | Exportación OTLP por `grpc` o `http`, endpoint del colector y seguridad de transporte. |
| `instance.config.infrastructure.tracing.samplingRatio` | ausente; `0.1` al activarlo | Muestreo basado en el padre; se permite `0` explícito. |
| `agentActuation.enabled` bajo `functionalAreas.mcp.config` | ausente (`false`) | Registra las herramientas MCP de propuestas; siguen aplicándose el interruptor de instancia de user-management y la política del inquilino. |
| `--mqtt-device-uplinks` / `spec.mqttDeviceUplinks` | ausente; desactivado sin una elección previa | Registra la elección de uplinks MQTT; `=false` los desactiva, omitirlo mantiene la elección registrada. |
| `dcctl install --relational-store-max-wal-mib` | omitirlo mantiene el registro; sin registro conserva el comportamiento anterior | Registra `max_wal_size` relacional; `0` restaura el valor de PostgreSQL, en otro caso al menos 64 MiB. |

#### dcctl solo permite avanzar {#v150-dcctl}

No hay vuelta a una versión anterior. Upgrade y las repeticiones de bootstrap rechazan una versión anterior a la declarada en la instancia, e install rechaza una herramienta anterior al último instalador del clúster, sin excepción. Si falla una actualización, repítela con la herramienta nueva, avanza a una versión corregida o restaura una copia de seguridad. Los alias del registro publicado reciben ahora el mismo tratamiento del prefijo de versión y del digest fijado del broker que el nombre canónico.

`--mqtt-device-uplinks` registra `spec.mqttDeviceUplinks` y se conserva en repeticiones y actualizaciones sin la opción. Un overlay nuevo que contradice una elección registrada se rechaza antes de escribir si omites la opción; un overlay antiguo heredado se permite y prevalece la elección registrada. Un CRD antiguo que elimina el campo se rechaza antes de aplicar.

`dcctl install --relational-store-max-wal-mib` persiste el objetivo WAL relacional antes de aplicar, incluso si falla la aplicación y se repite. Una vez registrado prevalece sobre `TF_VAR_postgres_max_wal_size_mib`; omitirlo lo mantiene. Esto corrige la necesidad anterior de repetir el ajuste en cada instalación. Nunca se deduce de `--size` y puede cambiarse mientras hay instancias en ejecución.

Un bootstrap HA con tamaño de rendimiento, incluida la simulación, rechaza antes de crear nada cuando menos de tres nodos planificables pueden alojar cada uno las peticiones de CPU y memoria de un broker. Elige un tamaño menor, añade nodos mayores o establece `nats_cpu_request` / `nats_memory_request` explícitamente (omite la comprobación de ese recurso). Upgrade no cambia. `--lwm2m-identities` valida ahora la gramática del token de tipo de dispositivo antes de aprovisionar.

#### La configuración falla de forma segura {#v150-configuration}

Los valores inválidos de [Antes de actualizar](#v150-before-you-upgrade) se rechazan nombrando la clave en lugar de convertirse silenciosamente en valores por defecto, desactivar la rotación, desbordar una duración o fallar al arrancar. Cero y ausencia mantienen los valores documentados. El esquema del chart comprueba la escritura documentada de las claves; los documentos escritos a mano siguen necesitando validación al arrancar. La configuración debe terminar tras su único documento, sin sufijos ignorados.

#### Límites de entrada de GraphQL {#v150-input-limits}

- Las búsquedas de nombres de dashboards de más de 256 bytes se rechazan con `LIMIT_EXCEEDED`.
- El filtro `actor` del registro de auditoría de más de 256 bytes se rechaza con `LIMIT_EXCEEDED` en los planos de inquilino y administrador.
- `calculatedMetricBackfills(statuses:)` rechaza más de cinco entradas con `LIMIT_EXCEEDED`; ignora las repetidas en una lista aceptada.
- Las búsquedas por subcadena de dispositivos y tipos de dispositivo rechazan `nameContains` de más de 256 bytes UTF-8, medidos antes de recortar espacios, con `LIMIT_EXCEEDED`.
- Las búsquedas de comandos rechazan listas `statuses` u `outcomes` de más de 1.000 entradas, incluidos duplicados, con `LIMIT_EXCEEDED`.
- Las páginas de eventos y las agregaciones de mediciones rechazan más de 1.000 entradas `eventTypes`, incluidos duplicados, con `LIMIT_EXCEEDED`.
- `facetValues` rechaza más de 1.000 valores distintos con `LIMIT_EXCEEDED`, sin devolver una lista parcial.
- Una conexión WebSocket GraphQL permite hasta 1.000 operaciones de suscripción simultáneas. Un `subscribe` adicional recibe `LIMIT_EXCEEDED`; la conexión permanece abierta.

#### Comprobaciones más estrictas en operaciones existentes {#v150-tightenings}

- **Dashboards.** `createDashboard` rechaza al llegar a 10.000 dashboards activos por inquilino con `DASHBOARD_LIMIT`. Los existentes siguen utilizables, incluso sobre el límite; elimina los que no uses para crear más. La publicación se detiene en 1.000 versiones por dashboard con `DASHBOARD_VERSION_LIMIT`; el historial, el puntero publicado y el borrador siguen utilizables.
- **Selectores de borradores de dashboard.** Guardar una fuente `kind: "device"` exige `deviceToken` de tipo cadena y `measurements` como matriz de cadenas, incluidas ambas claves. Las entradas manuales mal formadas se rechazan al guardar el borrador; los dashboards almacenados siguen leyéndose, publicándose, restaurándose y renderizándose.
- **Políticas de notificación.** Cada conjunto aportado acepta hasta 100 reglas (`LIMIT_EXCEEDED`). Las políticas mayores existentes siguen funcionando; una actualización que omite `rules` las deja intactas.
- **Campañas.** Crear una campaña rechaza JSON de disparador con datos posteriores en lugar de descartarlos silenciosamente.

#### Revisión de cambios de automatización (preview) {#v150-automation-review}

La pestaña **Change review** de un perfil compara un cambio de regla del borrador con la versión activa en ventanas registradas de incidente y funcionamiento normal. El motor ejecuta ambas versiones sobre los mismos eventos sin efectos secundarios y compara transiciones de alarmas y solicitudes de reacción, incluido su contenido. Las pruebas incompletas o no compatibles dan `INCONCLUSIVE`, nunca una aprobación. Los límites son 50 dispositivos, dos ventanas de hasta 24 horas cada una, hasta seis horas de contexto previo, diez expectativas y 2.000 cambios enumerados por ventana. La simulación Night Shift aporta un escenario registrado: `dcctl sim create --episode night-shift` y `dcctl sim windows`.

Las revisiones guardan los digests de entrada, límites del stream, expectativas y resultado, pueden compartirse y requieren `event:read`. Aprobar exige un segundo factor reciente; los autores del borrador quedan excluidos y la autoaprobación sigue la política del perfil. Las publicaciones y vueltas atrás se registran ahora en el diario de auditoría.

La protección es opcional en un perfil publicado y apto. Puede exigir una o dos aprobaciones. Una publicación protegida debe aportar `automationReviewToken` y consume una revisión aprobada una sola vez, comprobando el borrador congelado, la referencia, las pruebas, la caducidad, la política actual y la habilitación actual de los aprobadores. Sin revisión devuelve `REVIEW_REQUIRED`; si no puede comprobar la habilitación, `REVIEW_UNAVAILABLE`. Las versiones publicadas enlazan a su revisión y pruebas. Los perfiles sin protección conservan su vía de publicación.

Una vuelta atrás protegida, cambiar un tipo de dispositivo hacia o desde un perfil protegido, o sacar un dispositivo de sus tipos vinculados exige un motivo de emergencia registrado y un segundo factor reciente. Una vuelta atrás cambia la referencia e invalida las aprobaciones pendientes. Los perfiles protegidos no pueden eliminarse. Los triggers de PostgreSQL exigen estas condiciones incluso para pods anteriores: una escritura antigua se rechaza en lugar de eludir la protección. Endurecer se aplica inmediatamente; relajar exige una segunda persona o, cuando se permite autoaprobar, esperar 24 horas. Protege perfiles solo después del despliegue.

#### Acciones de agentes (preview) {#v150-agent-actions}

MCP añade `propose_alarm_action`, `get_proposal` y `list_my_proposals`, registradas solo con `agentActuation.enabled` de MCP activado y el ámbito de actuación requerido del llamante. Las solicitudes reenvían el propio token bearer del llamante. Las personas aprueban en la consola; no hay herramienta MCP de aprobación ni ejecución. `agentApprovalAccess` da acceso a la bandeja a aprobadores aptos sin `agent:admin`; el interruptor de desactivación sigue exigiendo `agent:admin`. `stepUp(refreshToken, code)` dentro de la sesión renueva `mfa_time` con un código del autenticador, con alternativa de volver a iniciar sesión si no está disponible o para SSO.

Un inquilino deshabilitado o en purga rechaza decisiones de propuesta y ejecución con `OPERATOR_SUSPENDED`. Deshabilitar o purgar también publica una desactivación; volver a habilitar avanza la generación de actuación. La barrera local de device-management serializa la ejecución con las desactivaciones consumidas y rechaza un flujo ausente o desactualizado con `FENCE_UNAVAILABLE`. Los efectos de alarmas entran en una bandeja de salida transaccional, por lo que un efecto confirmado se vuelve a intentar publicar. La publicación de desactivación puede fallar después de confirmar el cambio de política o inquilino: repite la acción para publicarla de nuevo.

Los datos de autorización de la base de datos provienen de una sola instantánea de lectura; los registros de inicio de sesión se leen una vez antes. La ejecución pide la decisión final tras todo el trabajo de base de datos, justo antes de confirmar, con un plazo de cuatro segundos y sin SQL posterior. Un rechazo o respuesta tardía revierte todo; terminar una sesión inactiva o perder la conexión después de preguntar se traduce en `POLICY_UNAVAILABLE`. Una revocación confirmada después de la instantánea de decisión aún puede ser adelantada durante cinco segundos más el tiempo de confirmación. El límite solo rige después de actualizar todos los pods device-management y user-management.

**Las aprobaciones se vinculan al inicio de sesión del aprobador.** Cerrar sesión, restablecer contraseña o segundo factor, o deshabilitar a la persona termina esa aprobación; vuelve a aprobar desde otro inicio de sesión. Las aprobaciones anteriores sin inicio de sesión ya no cuentan. Los tokens de acceso de consola llevan ahora `sid`, que rellena también la columna de sesión del diario de auditoría para cambios de consola. Activa acciones de agentes solo tras todo el despliegue. Consulta [Propuestas y aprobaciones](../concepts/mcp.md#agent-proposals).

#### LoRaWAN y comandos {#v150-lorawan}

Un downlink no confirmado que el servidor de red solo encola sigue terminando `SUCCESSFUL`, pero no registra `deliveredTime` ni `deliveryEvidence: DEVICE_RESPONSE`: ningún dispositivo respondió. Las confirmaciones siguen registrando `DEVICE_RESPONSE`. La guía de comandos explica qué componente resuelve cada resultado. La guía LoRaWAN envía ahora un comando confirmado al dispositivo, y `dc-simulator` puede simular la cola de downlinks y el flujo de confirmaciones ChirpStack con `--lorawan-downlink-listen` y `--lorawan-ack`.

#### Observabilidad {#v150-observability}

El trazado OpenTelemetry opcional sigue un evento por ingesta, publicación y consumo NATS, resolución de device-management, almacenamiento y event-processing. La exportación usa OTLP por gRPC o HTTP. Los spans GraphQL no registran documentos, variables ni tokens; los de ingesta no registran credenciales ni tokens de dispositivos. En el borde de ingesta, el `traceparent` del llamante se enlaza a una nueva raíz local, nunca se adopta: se descarta `tracestate` remoto y se ignora la marca de muestreo remota. Los spans internos continúan el contexto de la plataforma. El trazado desactivado no instala proveedor, goroutines ni exportador y no cambia los bytes de los mensajes. Las publicaciones de eventos fallidos y los reintentos separados empiezan una traza nueva.

Un id de instancia no canónico genera una advertencia y establece el indicador de cada área (por ejemplo `devicechain_devicemanagement_instance_id_noncanonical`) en 1 sin rechazar el arranque; consulta [Antes de actualizar](#v150-before-you-upgrade).

#### Ediciones simultáneas conservadas {#v150-concurrent-edits}

Las actualizaciones parciales escriben solo los campos nombrados para clientes, canales de notificación, borradores de perfil de dispositivo, tipos de activo, activos, ventanas de mantenimiento, definiciones de métricas, tipos de dispositivo, borradores de reglas, definiciones de comandos, tipos de área, tipos de cliente, tipos de relación, credenciales, dispositivos y geocercas. Los campos omitidos ya no revierten una edición simultánea; una actualización vacía no escribe nada. Un renombrado de área obsoleto ya no restaura un vínculo de cerca eliminado simultáneamente. Una edición de credencial que sobrescribiría una rotación simultánea de contraseña se rechaza con un error para reintentar.

#### Otros cambios {#v150-other-changes}

- **Apagado y relevo.** Event-sources persiste los recuentos pendientes de uplinks sin decodificar y espera una desconexión en curso tras el antirrebote. LwM2M espera al barrido de inquilinos eliminados antes del relevo. Messaging termina las expulsiones de caché de seguimiento antes de desconectar. Las actualizaciones de gobernanza se cancelan y se esperan. Las notificaciones cancelan entregas bloqueadas dentro del plazo de apagado y evitan reenviar una ya hecha. Command-delivery cancela un aviso de despacho antes de reclamar, dejando el comando en cola. Quien espera autenticación de integración deja de esperar si el llamante cancela; la consulta compartida tiene un plazo propio de diez segundos.
- **Liderazgo del operador.** El operador acepta `--leader-elect`; el manifiesto distribuido lo activa.
- **Acceso analítico.** La reconciliación converge miembros directos y anidados de grupos sin depender del prefijo del lector. Un superusuario con nombre en conflicto se omite, registra y cuenta; un conflicto con el usuario del servicio o propietario del esquema impide arrancar. Las vistas de otro propietario se diagnostican antes de reparar o cambiar permisos.
- **Consola y widgets.** Los encabezados de telemetría, tablas de alarmas y formularios e historiales de comandos siguen el idioma del lector. La tabla de alarmas puede desplazarse con teclado. Las URL y atribución de mapas personales de geocercas se aplican juntas; una pareja incompleta conserva el mapa anterior. La consola avisa antes de que una declaración de faceta cambie el tipo de valor de un eje existente.
- **Clientes y ediciones existentes.** Las actualizaciones obsoletas de conectores devuelven `STALE_WRITE`; la consola conserva lo no guardado y ofrece recargar y reintentar. Los clientes OAuth antiguos `dcsa_` siguen siendo clientes normales y mantenibles, nunca cuentas de servicio; registra un sustituto antes de borrar uno.
- **Correcciones operativas.** Un gateway no puede reiniciar mientras siga viva su parada anterior agotada. Restablecer una instantánea compara secuencia y marca de agua, conservando el checkpoint más nuevo del sucesor. Un `CONNECTED` rechazado ya no avanza la última actividad. Una entrega mixta es parcial, no completa, y un fallo grave conserva precedencia. Los rechazos de precisión del codificador nombran el parámetro. Solo el rechazo exacto de campo desconocido del propio par se reconoce como desfase de versión del archivo de cercas. Una lista cuya consulta de recuento falla devuelve ese error en lugar de una página con un total inventado.
- **Charts y documentación.** Los charts candidatos se marcan como versiones preliminares en Artifact Hub. Los contadores muestran cero mientras su área funciona y las colas incluyen mensajes en curso. La guía CRL aclara que las cargas streaming superiores a 2 MiB requieren aumentar `deviceManagement.callout.revocation.maxCrlBytes` (hasta 32 MiB). La referencia de decodificadores corrige `base64.decode` a un máximo de 5467 bytes de texto, incluidos saltos de línea; el comportamiento no cambia.

#### Cambios de esquema y vuelta atrás {#v150-going-back}

Se añaden exactamente cuatro migraciones, todas aditivas. Las tablas nuevas empiezan vacías; se conservan los datos existentes.

- **Barrera de desactivación del agente y bandeja de salida de efectos** (`device-management`, `20261110000100`): añade `agent_actuation_fences` y `agent_effect_outboxes`.
- **Revisiones de automatización** (`device-management`, `20261111150000`): añade `automation_reviews`.
- **Exigencia de revisión de automatización** (`device-management`, `20261126120000`): añade tres tablas para políticas de protección, cambios pendientes y registros de emergencia, una columna nullable `device_profile_versions.automation_review_token`, una función y seis triggers de fila `BEFORE`. La migración no protege ningún perfil; las barreras también se aplican a pods anteriores al activar la protección.
- **Generación de actuación** (`user-management`, `20261110130000`): añade `iam_agent_policies.actuation_generation`.

**La actualización solo permite avanzar; no hay vuelta a `v1.4.0`; `dcctl` ahora la rechaza.** Repite una actualización fallida con la herramienta `v1.5.0`, avanza a una versión corregida o restaura una copia de seguridad. Consulta la [política de versiones y actualizaciones](./versioning-policy.md).

#### Problemas conocidos {#v150-known-issues}

- **Un downlink LoRaWAN confirmado sin informe del servidor de red termina `TIMEOUT` al vencer el plazo,** con entrega desconocida. Los comandos no confirmados registrados antes de actualizar conservan sus pruebas antiguas.
- **Un escalado que ganó su reclamación antes de marcarse un cierre aún puede entregar después;** los destinos registrados tras reclamarse el cierre no reciben cierre.
- **Solo se ejecutan propuestas de alarmas ACK/CLEAR.** Las demás no se ejecutan aunque se aprueben. El interruptor de desactivación no revierte una acción ejecutada.
- **Quitar y volver a conceder un rol de aprobador dentro de un mismo inicio de sesión abierto revive su aprobación.** El vínculo de sesión no detecta ese cambio intermedio de rol.
- **Una relajación de protección de revisión cuyo plazo de 24 horas venció se aplica en la siguiente operación protegida,** no mediante un temporizador de fondo.
- **La aptitud para proteger revisiones usa búsquedas de texto conservadoras,** sin analizar reglas; identificadores o claves parecidos a dependencias dinámicas excluidas pueden impedir activarla.
- **El relay de efectos del agente entrega al menos una vez.** La deduplicación del broker cubre 120 segundos; los eventos de alarmas pueden llegar desordenados y una fila que falla 100 veces queda para intervención del operador.

### v1.4.0 — downlinks LoRaWAN confirmados, la carga útil propia de un dispositivo por MQTT y notificaciones que nombran el dispositivo {#v140-upgrade}

`v1.4.0` es una actualización in situ desde `v1.3.0`: `dcctl install` para el clúster y después un `dcctl upgrade` por cada instancia, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/docs/deployment/releases-and-upgrades.md#v0170-upgrade). Usa **el `dcctl` de la nueva versión para ambos comandos**, y pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Durante la actualización se ejecutan diez migraciones añadidas (dos en `device-management`, cuatro en `notification-management`, dos en `outbound-connectors` y dos en `user-management`); agregan tablas y columnas. La migración de la marca de tiempo de notificación al despejar también fija `notify_on_clear_since = updated_at` donde `notify_on_clear` es true y `notify_on_clear_since IS NULL`. Consulta [Cambios de esquema y vuelta atrás](#v140-going-back). Si vienes de `v1.2.0` o anterior, lee primero las [notas de `v1.3.0`](#v130-upgrade) y síguelas con `--version v1.4.0`.

```bash
dcctl install local --version v1.4.0
dcctl upgrade local devicechain --version v1.4.0 --dry-run
dcctl upgrade local devicechain --version v1.4.0
```

Se aplica la [política de versiones y actualización](./versioning-policy.md). Esta versión amplía las interfaces cubiertas (nuevos campos, operaciones y valores de enumeración GraphQL, un nuevo tema MQTT, nuevas banderas de `dcctl` y un nuevo `--size`) y no elimina ninguna. Las nuevas operaciones de propuesta y aprobación de agentes están en el [nivel de vista previa](./versioning-policy.md#preview-interfaces), donde todavía pueden cambiar en una versión menor; las notas de la versión indicarán entonces qué cambió. Los valores de Helm que añade esta versión no están cubiertos por la política de compatibilidad, y cada uno está desactivado, o conserva el comportamiento de `v1.3.0`, por defecto.

Novedades:

- **`dcctl` rechaza las actualizaciones en el orden equivocado.** Consulta [Se exige el orden de actualización](#v140-ordering).
- **Las audiencias de tokens ya no pueden fijarse en `observe`,** como anunciaban las notas de `v1.3.0`. Consulta [Las audiencias de tokens ya no pueden fijarse en observe](#v140-token-audience).
- **Una política de notificación puede avisar cuando se despeja una alarma,** y las notificaciones de alarma nombran el dispositivo y pueden enlazar con él. Consulta [Notificaciones que nombran el dispositivo](#v140-notifications).
- **Un dispositivo puede publicar su propia carga útil por su conexión MQTT** para el decodificador de su perfil, y el fragmento de conexión de la consola puede mostrarlo. Requiere activación. Consulta [La carga útil propia de un dispositivo por MQTT](#v140-mqtt-uplinks).
- **Los comandos LoRaWAN se pueden confirmar,** en ChirpStack v4 y The Things Stack v3, y una definición de comando puede llevar un codificador declarativo de carga útil. Consulta [Downlinks LoRaWAN confirmados](#v140-lorawan).
- **La medición de ingesta puede compensar el retraso causado por una pausa de la plataforma.** Requiere activación. Consulta [Medición de ingesta con crédito por retraso](#v140-ingest-metering).
- **Un tamaño `perf-16k`,** correcciones de `perf-12k` y un ajuste relacional de `max_wal_size`. Consulta [Dimensionamiento](#v140-sizing).
- **Acciones de agentes, en vista previa.** Un agente de IA puede proponer reconocer o despejar alarmas, y las personas aprueban desde sus propias sesiones. La aprobación final ejecuta un ACK o CLEAR de alarma. Consulta [Acciones de agentes (vista previa)](#v140-agent-actions).
- **Una forma admitida de acceder desde un clúster local a un servicio de tu propia máquina:** `--dev-allow-egress`. Consulta [Tráfico saliente en desarrollo local](#v140-dev-egress).
- **La página de paneles de Grafana tiene capturas de los ocho paneles,** y los paneles cuentan los mensajes en tránsito. Consulta [Otros cambios en esta versión](#v140-other-changes).

#### Antes de actualizar {#v140-before-you-upgrade}

Lo que exige actuar, en el orden en que hacerlo:

- **Elimina `infrastructure.tokenAudience.enforcement: observe` antes de actualizar,** si tu instancia lo fija en algún sitio. Un servicio se niega a arrancar con este valor y `dcctl upgrade` se niega antes de cambiar nada. Consulta [Las audiencias de tokens ya no pueden fijarse en observe](#v140-token-audience).
- **Ejecuta siempre `dcctl install` con el `dcctl` de la nueva versión antes de `dcctl upgrade`.** `dcctl upgrade` lee ahora el registro de instalación del clúster y se niega, antes de escribir nada, si el `dcctl` que terminó por última vez `dcctl install` es anterior al que actualiza, si una instalación comenzó y nunca terminó o si falta el registro. Esto incluye una versión de parche: un `dcctl upgrade` sin banderas con una versión de parche más reciente de `dcctl` necesita primero un `dcctl install` con ese `dcctl`. Un `dcctl upgrade` sin banderas, que vuelve a aplicar la versión declarada de la instancia, también se rechaza si esa versión es más reciente que el `dcctl` que ejecutas. Consulta [Se exige el orden de actualización](#v140-ordering).
- **Un `--version` explícito debe ser la versión de tu propio `dcctl`,** tanto para `install` como para `upgrade`. Para pasar a otra versión, usa el `dcctl` de esa versión. No afecta a una etiqueta de registro personalizado, a `--build` ni a `enable-area`.
- **Activa las nuevas funciones opcionales solo cuando termine el despliegue, y cada una en una actualización propia.** Cada una está desactivada por defecto, y un pod de `v1.3.0` que sigue en marcha no la conoce:
  - Fija `infrastructure.nats.mqttDeviceUplinks` en una actualización propia, una vez instalada `v1.4.0`. Consulta [La carga útil propia de un dispositivo por MQTT](#v140-mqtt-uplinks) para saber qué hace un pod anterior con ella: uno que se reinicia mientras está activo se detiene al arrancar.
  - Fija `agentActuation.enabled` en una actualización aparte, una vez terminado el despliegue de `v1.4.0`. Consulta [Acciones de agentes (vista previa)](#v140-agent-actions).
  - Fija `eventSources.ingestMetering: lagCredit` solo cuando todos los pods de `event-sources` sean `v1.4.0`. Consulta [Medición de ingesta con crédito por retraso](#v140-ingest-metering).
  - Activa el downlink LoRaWAN (`functionalAreas.outbound-connectors.config.lpwanDownlink.enabled`) solo tras el despliegue, si aún no está activo: un pod `v1.3.0` de `event-sources` que se reinicie mientras está activo se detiene ante la clave de configuración que `v1.4.0` deriva de él.
  - Usa `--dev-allow-egress` solo tras el despliegue: una entrada añade `infrastructure.egress.devAllowedDestinations` a la configuración de la instancia, que un pod `v1.3.0` que se reinicie rechaza. Consulta [Salida para desarrollo local](#v140-dev-egress).
- **Si el downlink LoRaWAN ya está activado** (`functionalAreas.outbound-connectors.config.lpwanDownlink.enabled`), un pod de `event-sources` de `v1.3.0` que se reinicia durante el despliegue se detiene con `unknown field "lpwanDownlink"`. `v1.4.0` añade esa clave a la configuración que recibe `event-sources` (la deriva el chart; no se fija a mano), y un pod de `v1.3.0` rechaza una clave que no conoce. Un pod que no se reinicia no se ve afectado, y el despliegue sustituye uno detenido por un pod de `v1.4.0`, que arranca. Con el downlink desactivado, como está por defecto, nada cambia para `event-sources`.
- **Comprueba `linkBaseUrl` en los canales webhook genérico, Slack, Teams y PagerDuty.** Ahora se valida al guardar el canal (se rechaza una consulta o un fragmento), y un valor no válido almacenado omite los enlaces al entregar tras la actualización, pero la notificación se sigue enviando. Consulta [Notificaciones que nombran el dispositivo](#v140-notifications).
- **Cambia los identificadores OAuth que empiecen por `dcsa_` (sin distinguir mayúsculas), incluida la configuración precargada.** El prefijo identifica cuentas de servicio y puede colisionar con la lista de clientes de la política de agentes. Consulta [Comprobaciones más estrictas de operaciones existentes](#v140-tightenings).
- **Si una instancia está registrada con `--size perf-12k`, espera dos cambios en las especificaciones de los pods en su próximo `dcctl upgrade`:** `device-management` solicita 400m de CPU (antes 500m), y `event-sources` tiene un límite de memoria de 256Mi (antes 512Mi). Los pods se reinician como en cualquier despliegue. Consulta [Dimensionamiento](#v140-sizing).
- **En una primera conexión admitida sin conocer el estado OCSP, espera que `devicechain_devicemanagement_x509_ocsp_unavailable_allowed_total` suba uno, en lugar de dos.** La primera conexión de un dispositivo ejecutaba el paso OCSP dos veces y contaba ambas; la serie cuenta conexiones, como dice su texto de ayuda, y ahora lo hace. Las decisiones de admisión y rechazo, el nombre y las etiquetas no cambian. Vuelve a ajustar la referencia de un panel o una alerta que hayas calibrado con el ritmo duplicado.
- **Revisa los scripts frente a las comprobaciones más estrictas siguientes,** incluidos los comandos LoRaWAN en bruto y los tiempos de espera de consulta del decodificador HTTP.

Valores de Helm y ajustes de `dcctl` nuevos, cada uno desactivado o sin cambios por defecto:

| Valor | Por defecto | Función |
| --- | --- | --- |
| `eventSources.ingestMetering` | `legacy` | `lagCredit` selecciona el control con crédito por retraso. Se renderiza como variable de entorno en los pods de `event-sources`. |
| `instance.config.infrastructure.nats.mqttDeviceUplinks` | `false` | Permite a cada dispositivo publicar en su propio tema de uplink y añade el subject de uplink al stream de captura. Se configura con `dcctl --values`. |
| `agentActuation.enabled` bajo `functionalAreas.user-management.config` | `false` | Interruptor de instancia para acciones de agentes, el primero de tres. Es una clave del documento de configuración, no un valor por defecto de `values.yaml`; ausente significa desactivado. |
| `postgres_max_wal_size_mib` (variable OpenTofu de la raíz del clúster) | `0` | `max_wal_size` del almacén relacional; `0` conserva el valor por defecto de PostgreSQL de 1 GB. |

#### Se exige el orden de actualización {#v140-ordering}

Un `dcctl install --version` o `dcctl upgrade --version` explícito ahora rechaza una versión publicada distinta, anterior o posterior, a la del `dcctl` en ejecución. Una actualización sin banderas conserva la versión declarada, pero rechaza una declaración posterior a la herramienta. Usa el `dcctl` de la versión de destino, ejecuta `dcctl install` con los ajustes del clúster y después actualiza cada instancia. El binario y la versión que instala deben coincidir para que la herramienta conozca sus migraciones y su orden.

`dcctl upgrade` también exige un registro de instalación completada por un `dcctl` al menos tan reciente como él mismo. Un registro ausente o ilegible, una instalación sin terminar o una instalación completada más antigua se rechazan antes de escribir. Los scripts que antes omitían la instalación deben ejecutarla primero, incluso para un parche.

Esta versión también admite `-i` / `--instance` en los comandos que hablan con la API de la plataforma (`events failed`, `attr`, `dead-letters`, `presence`, `product`, `tenant-deletion`, `test` y `device register`). Sin `--server`, `--instance` usa el host y el ajuste TLS de la declaración; un `--server` o `--tls` explícito tiene prioridad.

#### Comprobaciones más estrictas de operaciones existentes {#v140-tightenings}

- **Identificadores de clientes OAuth.** Al guardar y en la configuración precargada se rechaza ahora el prefijo `dcsa_`, sin distinguir mayúsculas. Cambia esos identificadores: el prefijo identifica cuentas de servicio, incluidas las de agentes, y podría confundirse con una o colisionar con la lista de clientes de la política.
- **Enlaces de notificaciones.** Guardar un canal webhook genérico, Slack, Teams o PagerDuty rechaza ahora un `linkBaseUrl` con consulta o fragmento. Elimínalos de la URL base: los enlaces se construyen añadiendo una ruta. Una URL base no válida almacenada omite los enlaces al entregar; la notificación se sigue enviando.
- **Envío LoRaWAN.** Cada envío lee ahora el vocabulario de comandos publicado y el codificador, incluidos los comandos en bruto sin codificador. Un fallo de consulta deja la entrega pendiente de reintento; un comando definitivamente ausente de un vocabulario restringido se rechaza como no enviado. Mantén publicada la definición y restablece la consulta. Un comando pendiente usa el codificador vigente al enviarse, nunca uno almacenado al ponerse en cola.
- **Consulta del decodificador HTTP del dispositivo.** La cancelación del solicitante deja ahora de esperar la consulta. La consulta compartida al backend continúa para llenar la caché, pero tiene un límite propio de 10 segundos para no ejecutarse indefinidamente. Los scripts pueden ver una cancelación anterior o un fallo de consulta reintentable; reintenta este último en vez de interpretarlo como ausencia de decodificador.

#### Las audiencias de tokens ya no pueden fijarse en observe {#v140-token-audience}

Las notas de `v1.3.0` lo anunciaban. `infrastructure.tokenAudience.enforcement: observe`, aceptado en `v1.2.0` y `v1.3.0` para desactivar la aplicación obligatoria, se rechaza:

- Un servicio se detiene al arrancar, indicando la clave, si la configuración de la instancia la fija.
- El esquema de valores del chart lo rechaza, tanto en los valores de Helm como en un archivo de superposición de valores de `dcctl`.
- `dcctl upgrade` se niega antes de cambiar nada si la superposición registrada en la instancia, los valores con los que se instala la versión o el documento de `instance.existingSecret` aún lo fijan.

Elimina la clave (sin definir significa `enforce`) o fíjala en `enforce`, y después actualiza. En una instancia gestionada por `dcctl` cuya superposición registrada la fija, ejecuta `dcctl upgrade` con `--reset-values`, o con `--values <file without the key>`; una ejecución sin ninguno de los dos se rechaza, indicando esos dos comandos. Si proporcionas el documento tú mismo (`instance.existingSecret`), elimina la clave de tu documento antes de ejecutar `helm upgrade`, porque los servicios la rechazan al arrancar y un `helm upgrade` corriente no puede mirar dentro de tu Secret; actualiza también `instance.existingSecretChecksum`. Antes de actualizar, comprueba que `{__name__=~"devicechain_[a-z0-9]+_token_audience_checks_total", verdict="would_reject"}` y `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}` no muestren nuevos incrementos durante un período de tráfico representativo en la instancia `v1.3`, o los clientes que representan serán rechazados. La serie `would_reject` y las alertas `TokenAudienceWouldReject` y `OAuthResourceWouldReject` se mantienen por compatibilidad y ya no pueden activarse. `unscopedClientCredentials` no cambia.

#### Notificaciones que nombran el dispositivo {#v140-notifications}

- **`notifyOnClear`.** Una política puede notificar cuando se despeja una alarma que anunció; la opción está desactivada por defecto. El aviso solo va a canales y destinatarios que entregaron una notificación en ese ciclo. Las horas de silencio descartan el aviso de despeje en lugar de retenerlo. Tras reclamarlo se entrega como máximo una vez: una caída entre reclamar y enviar puede perderlo. Los reintentos de recogida fallida esperan dos minutos y terminan 30 minutos después del despeje.
- **Las notificaciones nombran el dispositivo.** Los payloads webhook llevan `deviceToken`, `deviceName` y `deviceTypeToken` cuando se conocen. Los resúmenes de Slack, Teams, PagerDuty y correo muestran el nombre del dispositivo; el `text` del webhook genérico nombra el dispositivo por su token, y el nombre que escribe el dispositivo solo está en el campo estructurado `deviceName`, de modo que un nombre no puede inyectar marcado en una herramienta de chat que muestre `text`. Para publicar en Slack, usa el canal nativo de Slack en lugar de un webhook genérico. Las renotificaciones temporizadas y los despejes tardíos usan el origen almacenado; las filas antiguas sin él omiten esos campos. Se incluyen `originatorType` y `originatorId` cuando se conocen.
- **Enlaces.** Los canales webhook genérico, Slack, Teams y PagerDuty pueden fijar `linkBaseUrl`. Los payloads webhook genéricos añaden `alarmUrl` y, si se conoce el dispositivo, `deviceUrl`; los mensajes nativos pueden enlazar al dispositivo. Consulta [Comprobaciones más estrictas de operaciones existentes](#v140-tightenings) antes de guardar un canal existente.
- **Texto.** Se escapan los caracteres de control de Slack (`&`, `<`, `>`) y se neutralizan las menciones y enlaces de Teams en el texto escrito por dispositivos o inquilinos. Los nombres de los resúmenes se reducen a una línea y a 120 caracteres; los payloads estructurados conservan el nombre almacenado. Los asuntos de correo no ASCII se codifican según RFC 2047; los ASCII no cambian.

#### La carga útil propia de un dispositivo por MQTT {#v140-mqtt-uplinks}

Un dispositivo puede publicar sus propios bytes en `{instance}/{tenant}/devices/{device}/uplink` y el decodificador del perfil de su tipo de dispositivo los convierte en eventos, la misma capacidad que ofrecía la ruta HTTP `uplink` en `v1.3.0`. El dispositivo se autentica al conectar y su concesión firmada nombra solo su propio tema de uplink, así que una carga útil nunca puede decodificarse como si fuera de otro dispositivo. Una carga útil en `events` sigue sin ser leída nunca por un decodificador.

Requiere activación: con `infrastructure.nats.mqttDeviceUplinks` desactivado, como está por defecto, la concesión es exactamente la que emitían las versiones anteriores y el broker rechaza publicar en el tema. Los errores terminales de procesamiento de la carga útil se registran como decodificaciones fallidas con su causa y se cuentan en la nueva `devicechain_eventsources_device_uplink_messages_total{result}`; los descartes por límite de tasa se confirman por separado, y los de la etapa de mensajes no pasan por este contador. También hay una nueva consulta del plano de servicio, `brokerAssertedDeviceDecoder`.

**Actívalo en una actualización propia, una vez instalada `v1.4.0`.** Activarlo también hace que el stream de captura declare el subject de uplink. `mqttDeviceUplinks` es una clave del documento de configuración compartido de la instancia, así que mientras está activado, un pod de `v1.3.0` de cualquier área que arranque o se reinicie se detiene al arrancar con `unknown field "mqttDeviceUplinks"`, antes de cualquier comprobación del stream. Un pod de `v1.3.0` que no se reinicia sigue en marcha, con estos efectos: un pod de `event-sources` que ya está en marcha confirma y descarta un subject ajeno a sus dos, así que una carga útil en bruto que le llegue se pierde; y un pod de `device-management` rechaza el uplink que no puede ubicar, que se vuelve a entregar. Con él desactivado, el stream de captura es exactamente el que declara `v1.3.0`, así que no afecta a un pod que se reinicie a mitad del despliegue. Las concesiones de publicación existentes persisten hasta la reconexión o la caducidad del token de sesión, pero desactivar el ajuste elimina los uplinks de la captura durante la reconciliación, por lo que seguir publicando puede perder cargas útiles.

Con `dcctl`, fíjalo en un archivo de superposición `--values`: `instance.config.infrastructure.nats.mqttDeviceUplinks: true`; las actualizaciones posteriores lo conservan. Consulta la [guía de decodificadores de carga útil](../guides/payload-decoders.md) y la [Referencia del protocolo de dispositivos](../reference/device-protocol.md#raw-payload). La pestaña **Enviar datos** de un dispositivo en la consola ofrece ahora una opción **Carga útil en bruto** junto al JSON de eventos, con el `curl` de la ruta HTTP y el `mosquitto_pub` del tema. No puede ver si el operador ha activado alguno de los dos, así que cada comando indica el ajuste que necesita.

#### Downlinks LoRaWAN confirmados {#v140-lorawan}

- **ChirpStack v4.** Un comando con `"confirmed": true` permanece en `SENT` hasta que el servidor de red lo resuelve, en lugar de responderse al poner el downlink en cola. Los eventos `ack` y `txack` de ChirpStack llegan a la URL de integración entrante ya configurada para los uplinks. Una confirmación resuelve el comando como `SUCCESSFUL`; un tiempo de espera de confirmación agotado sin confirmación, o no recibir nada antes del plazo, lo resuelve como `TIMEOUT` con entrega desconocida, nunca «no entregado», porque el dispositivo puede haberlo recibido. El plazo es el primero entre la caducidad del comando y las 24 horas desde su puesta en cola. Un evento que no puede asociarse a un downlink pendiente por el identificador del elemento de cola, la integración y el DevEUI se descarta y se cuenta en `devicechain_outboundconnectors_lpwan_ack_reports_total`, nunca se asocia por conjetura.
- **The Things Stack v3.** El downlink también llega a dispositivos detrás de un servidor de red The Things Stack v3. Crea un conector `lorawan_downlink` con `networkServer: "tts_v3"` y el identificador de la aplicación; el dispositivo necesita un alias `tts.device-id`. Los downlinks confirmados se resuelven a partir de los eventos webhook `downlink_ack`, `downlink_nack`, `downlink_sent` y `downlink_failed`, que debes activar en el webhook de la aplicación con rutas que empiecen por `downlink` (por ejemplo, `/downlink-ack`). Un fallo solo da un resultado definitivo de no entregado si demuestra que la trama nunca se transmitió; los fallos inciertos terminan en `TIMEOUT` con entrega desconocida.
- **Un codificador de carga útil en una definición de comando.** Una definición de comando puede llevar un `encoder` declarativo opcional que convierte los parámetros tipados del comando en la carga útil de la aplicación, el reflejo de los decodificadores de uplink: un `fPort`, un `confirmed` estático opcional y campos ordenados (bytes constantes, enteros de ancho fijo de 8, 16 o 32 bits en cualquiera de los dos órdenes de bytes con `scale` y `offset`, booleanos y campos de bits empaquetados). Se valida al crear o actualizar la definición y al publicar el perfil, así que un codificador incorrecto falla de forma explícita e indica el comando. Se permiten entradas fraccionarias cuando scale/offset produce un entero dentro del rango y de la tolerancia de coma flotante del codificador. Los valores fuera de esos límites, los valores ausentes y los tipos incorrectos hacen que se rechace el comando como no enviado. Una definición sin codificador conserva el contrato de hex o base64 en bruto. Un comando pendiente usa el codificador publicado vigente al enviarse, también tras una nueva publicación; cada envío vuelve a consultar el vocabulario. Un fallo de consulta aplaza la entrega para reintentar, en lugar de usar un codificador obsoleto. Consulta la [guía de integraciones entrantes](../guides/inbound-integrations.md).

La función necesita `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled`, como en `v1.3.0`. No envíes downlinks confirmados hasta que termine el despliegue, para que todos los pods de `outbound-connectors` lean el nuevo stream de confirmaciones. Un pod de `v1.3.0` que sigue en marcha rechaza `"confirmed": true` y responde al comando como no enviado, así que no puede dejar un comando atascado; un pod que se detiene entre poner el downlink en cola y registrarlo deja el comando en `SENT` hasta que el barrido de caducidad lo resuelve como `TIMEOUT`. Para ChirpStack, los downlinks confirmados necesitan que la integración HTTP envíe `ack` (y, opcionalmente, `txack`) a la URL de integración; para The Things Stack, activa los eventos de downlink anteriores. Un informe del servidor de red que `outbound-connectors` no logra procesar en ningún intento de entrega no se registra como mensaje fallido (dead letter): su downlink se resuelve en el plazo, como se describe arriba.

#### Medición de ingesta con crédito por retraso {#v140-ingest-metering}

Tras una pausa breve de la plataforma (una pausa del broker de mensajes, un lector detenido, una etapa de decodificación lenta), el tráfico en cola de un inquilino que se mantuvo dentro de su techo se lee en una ráfaga, y la parte que no cabía en la cuota disponible se descartaba después de haber dicho al dispositivo que el mensaje había llegado. Un nuevo modo de medición elimina ese problema.

- **Requiere activación y no es el valor por defecto.** `eventSources.ingestMetering` sigue en `legacy`, y seguirá así hasta que se haya medido el modo en un entorno de pruebas de kind y en una ronda en la nube. Ponlo en `lagCredit` para usar el nuevo control, y de nuevo en `legacy` para volver al anterior. Es un valor de Helm que se renderiza en los pods de `event-sources` como variable de entorno, así que un pod de una versión anterior nunca lo ve. Un valor no reconocido detiene el pod al arrancar. La actualización solo avanza.
- **Qué hace.** El tráfico nuevo se sigue contando cuando lo lee el pod, pero la cuota de un inquilino crece con lo que su tasa acumula durante el retraso medido de los lectores, hasta el tiempo que cubre su ráfaga (nunca más de 5 segundos). A un inquilino que supera su techo se le sigue descartando el exceso, y una cola más antigua que esa cobertura se drena como antes.
- **Qué no cambia.** Los descartes por encima del techo, por una banda de sobrecarga o por el descarte estricto se cuentan en los contadores de límite de tasa existentes como antes. HTTP y las integraciones entrantes conservan sus límites. Los techos siguen siendo por réplica.
- **Cómo mide.** Cada réplica estima el desfase entre las marcas de anexado del broker y su propio reloj, y cuánto se retrasa cada lector, a partir de la información de entrega que el broker ya envía, y pregunta al broker unas cuatro veces por segundo si un stream sin actividad tiene algo en cola. Una réplica que arranca durante una pausa aún no ha medido el reloj del broker, así que durante los primeros momentos de esa pausa puede conceder menos crédito del debido.
- **Métricas nuevas,** vacías en modo `legacy` y ninguna con una etiqueta de inquilino: `devicechain_eventsources_ingest_shed_cause_total{stream,route,cause}`, `devicechain_eventsources_ingest_lag_credit_seconds{stream}`, `devicechain_eventsources_reader_lag_seconds{stream}`, `devicechain_eventsources_append_skew_seconds{stream}` y `devicechain_eventsources_append_skew_clamped_total{stream}`. **Alertas nuevas,** sin activarse en modo `legacy`: `IngestShedBeyondLagCredit` e `IngestAppendSkewLarge`.

#### Dimensionamiento {#v140-sizing}

- **`perf-16k` es un nuevo valor de `--size`** para 16 000 eventos por segundo sostenidos: NATS solicita 6 CPU y 6Gi, el almacén de eventos tiene un `max_wal_size` de 40960 MiB en un volumen de 260Gi, `event-processing` usa 2 CPU y 1536Mi, y el volumen de JetStream es de 36Gi para la tasa de diseño. La configuración medida sostuvo 16 000 eventos por segundo sin pérdidas; 20 000 eventos por segundo se almacenan sin pérdidas, pero superan el objetivo de latencia, así que se documenta como techo de rendimiento y no hay un `perf-20k`. Los valores de `perf-16k` se derivan de la rampa medida y aún no se han ejecutado tal como están escritos; la página de dimensionamiento lo indica.
- **Se corrige `perf-12k`:** `device-management` solicita 400m de CPU (antes 500m) y `event-sources` tiene un límite de memoria de 256Mi (antes 512Mi, con un máximo medido de 81 MiB de memoria residente). Una instancia registrada con `perf-12k` incorpora ambos cambios en su próximo `dcctl upgrade`.
- **Un `max_wal_size` relacional.** La nueva variable de OpenTofu en la raíz del clúster `postgres_max_wal_size_mib` (por defecto `0`, que equivale al 1 GB propio de PostgreSQL, por lo que no cambia ninguna instalación existente) lo fija mediante la definición del clúster. El plan rechaza una asignación superior a la cuarta parte de `postgres_storage` cuando el almacenamiento se expresa como una cantidad entera en Mi, Gi o Ti; otros formatos omiten esta comprobación. El almacén relacional pertenece al clúster, así que `dcctl bootstrap --size` no lo escribe: indícalo al instalar con `TF_VAR_postgres_max_wal_size_mib`, con un `TF_VAR_postgres_storage` suficientemente grande. `dcctl` no conserva este ajuste: un `dcctl install` posterior sin él restablece el valor por defecto de PostgreSQL. Repítelo en cada instalación. La página de dimensionamiento indica el valor con el que se midió cada tamaño.

Consulta [Dimensionamiento](./sizing.md).

#### Acciones de agentes (vista previa) {#v140-agent-actions}

Un agente de IA puede proponer reconocer o despejar alarmas. En esta vista previa, desactivada por defecto, la aprobación final **ejecuta el ACK o CLEAR como la persona que aprueba**, en una transacción de base de datos después de volver a consultar la política de agentes con las credenciales de esa persona. Si la alarma se activó de nuevo o se eliminó, cambiaron las dependencias o la política rechaza la acción (incluidos el interruptor del inquilino, el de instancia o la admisión del cliente), la propuesta termina `REFUSED` y no cambia ninguna alarma. Las filas de auditoría identifican a la persona que aprueba, en nombre de quién actúa y la propuesta. Las aprobaciones desde la consola también registran el cliente del agente; si el token de quien aprueba tiene su propio `client_id`, prevalece esa declaración. No hay modo automático. Consulta [Propuestas y aprobaciones](../concepts/mcp.md#agent-proposals).

- **Activa solo tras el despliegue.** El interruptor de instancia `agentActuation.enabled`, la suspensión por inquilino del operador y la política de agentes del inquilino controlan las decisiones. `v1.3.0` no tiene API de agentes, tablas de políticas ni operaciones de suspensión: las solicitudes de agentes fallan en esos pods y no se ejecuta nada. Los pods de `v1.4.0` respetan los interruptores. Activa el de instancia en una actualización separada cuando todos los pods sean `v1.4.0`. Quienes iniciaron sesión antes de la actualización deben volver a iniciarla antes de que su agente pueda proponer: un inicio de sesión anterior a `v1.4.0` no lleva época de sesión, y una propuesta hecha con él se rechaza como `PROPOSER_SESSION_ENDED`. Tras volver a iniciar sesión, el agente propone de nuevo.
- **Bandeja Approvals de la consola (vista previa).** Las personas pueden listar propuestas, leer su detalle, aprobar o rechazar y usar el interruptor de emergencia del inquilino. Si se requiere autenticación reforzada, la consola ofrece iniciar sesión de nuevo y vuelve a la propuesta. La política, las propuestas y el estado local de decisión se restablecen al cambiar el inquilino, usuario o sesión. La confirmación de la aprobación final indica que, al aprobar, la acción se ejecuta en tu nombre, y la bandeja muestra después el resultado: `EXECUTED` con el resultado, `REFUSED` con el motivo (incluido `DEPENDENCY_CHANGED` cuando las alarmas cambiaron) o `CONCURRENT_APPROVAL` cuando otra persona está decidiendo la misma propuesta, tras lo cual se actualiza. Frente a un servidor que aún devuelve el código obsoleto `EXECUTION_NOT_AVAILABLE`, indica que la aprobación no se registró y que ninguna alarma cambió.
- **Se vuelven a comprobar las aprobaciones.** Quien aprueba necesita el permiso de escritura de la acción, un rol aprobador y un segundo factor reciente. Las aprobaciones anteriores solo cuentan mientras sus autores sigan siendo aptos y su segundo factor siga vigente. Cambiar quién tiene un rol cambia qué aprobaciones cuentan.
- **El interruptor de emergencia actúa en la siguiente decisión.** Se acepta `setAgentActuation(enabled: false)` incluso si no se puede leer o validar la política. Una decisión aún no alcanzada rechaza la ejecución si está apagado; una acción que ya pasó esa decisión puede completarse. El interruptor no deshace acciones ejecutadas. Reactivarlas requiere un segundo factor reciente. Es un control a nivel de aplicación.
- **Las otras acciones no se ejecutan.** Solo las propuestas de ACK/CLEAR de alarma tienen una ruta de ejecución en esta vista previa; los comandos y despliegues no. Las herramientas MCP no ejecutan acciones.
- **Vigencia del proponente.** La plataforma vuelve a comprobar el inicio de sesión original y los permisos actuales. Un solicitante que no sea el proponente ni `agent:admin` recibe el motivo genérico `PROPOSER_NOT_IN_STANDING`; los motivos `PROPOSER_` almacenados también se generalizan.
- **Los destinos de una propuesta deben ser tokens de alarma válidos.** `proposeAlarmAction` rechaza un destino que no sea un token bien formado, de modo que una propuesta almacenada no puede llevar texto libre que una lectura posterior presentaría como un identificador de la plataforma.
- **Obsolescencia.** Ya no se devuelve `EXECUTION_NOT_AVAILABLE` y queda obsoleto; permanece en el vocabulario.

#### Tráfico saliente en desarrollo local {#v140-dev-egress}

`dcctl bootstrap` y `dcctl upgrade` aceptan `--dev-allow-egress <address-or-CIDR>` (repetible o separado por comas; `none` lo borra) para que un webhook o conector de un clúster local pueda acceder a un servicio de tu propia máquina o LAN. Se rechaza salvo que el contexto de Kubernetes empiece por `kind-` o `k3d-`, o sea `minikube`, `docker-desktop` o `rancher-desktop` (también con guion y sufijo de perfil). Es una clave aparte, `infrastructure.egress.devAllowedDestinations`, y el `allowedDestinations` del operador sigue sin poder ajustarse. Las direcciones de metadatos de la nube, los rangos de loopback, enlace local y multidifusión, y los demás rangos que nunca se permiten siguen bloqueados incluyas lo que incluyas, y una entrada que se solape con alguno hace fallar `dcctl` desde el principio. Se registra en la instancia para que las actualizaciones posteriores lo conserven, `dcctl instances` lo muestra como `DEV EGRESS ALLOWED`, y cada servicio que lo usa registra un aviso al arrancar. Consulta [Conectores salientes](../concepts/outbound-connectors.md). Configúralo solo cuando todos los pods sean `v1.4.0`: un pod `v1.3.0` que se reinicie rechaza una configuración que incluya alguna entrada.

#### Otros cambios en esta versión {#v140-other-changes}

Nada de lo que aparece aquí requiere actuar.

- **La página de [paneles de Grafana](./grafana-boards.md) tiene capturas** de los ocho paneles de una instancia `v1.3.0` bajo una carga sintética constante, con una interrupción programada mediante un script.
- **Los paneles de cola pendiente cuentan los mensajes en tránsito.** El cálculo alternativo del broker cuenta juntos los mensajes en espera y los recogidos pero aún sin confirmar, así que los mensajes que recogió un lector reiniciado siguen contándose; se corrigieron los paneles de cola de ingesta, persistencia, estado de dispositivos, mensajería, entrega y procesamiento de eventos. Los indicadores de OOM de la plataforma y de entregas rechazadas o en horario de silencio muestran `0`, en lugar de «Sin datos», mientras su servicio responsable está en marcha.
- **La recogida de despejes de notificación** informa como parcial, en lugar de fallida, una pasada que entregó y después agotó su presupuesto, y cuenta una sola vez a un inquilino cuyas dos lecturas fallaron.
- **Un fallo al eliminar el downlink pendiente de un inquilino eliminado** se registra como `store_failed`, y el barrido de confirmaciones descarta las filas de un inquilino eliminado antes de reclamarlas.
- **Paneles y alertas para las funciones de esta versión.** Los paneles Ingest, Command Delivery y Delivery añaden paneles para los uplinks MQTT de dispositivos, los acuses y downlinks LoRaWAN, los informes de downlink de integraciones, la ingesta con crédito de retraso, OCSP y la recogida de despejes, y siete alertas nuevas tienen [entradas en el runbook](./alert-runbook.md): `LpwanAckReportsAnomalous`, `LpwanAckSettlementFailing`, `LpwanDownlinkTrackingFailing`, `IntegrationDownlinkReportsDiscarded`, `DeviceUplinkMessagesFailing`, `X509OcspUnavailableAllowed` and `NotificationClearPickupFailing`.
- **Una lectura de base de datos rechazada** devuelve un error en lugar de un pánico por puntero nulo.
- **Un token de agente ya no puede cambiar el nombre del perfil de una persona:** `updateProfile` rechaza los tokens de agente.
- **Compilado con Go 1.27.4.** Los binarios e imágenes de `v1.4.0` se compilan con Go 1.27.4, que corrige una vulnerabilidad en `net/http` de la biblioteca estándar.

#### Cambios de esquema y vuelta atrás {#v140-going-back}

Las siguientes migraciones son aditivas y se añaden al final. Las columnas nuevas admiten nulos o tienen valores por defecto y las tablas nuevas empiezan vacías. Esto permite que coexistan pods durante la actualización; no es una vía de vuelta atrás.

- **Política de agentes** (`user-management`, migración `20261109120000`). Dos tablas nuevas, `iam_agent_policies` (una fila de cabecera por inquilino) e `iam_agent_policy_versions` (versiones publicadas que solo se añaden), y la bandera de agente en `iam_service_accounts`. No se precarga ninguna fila: un inquilino sin política publicada tiene desactivada la actuación de agentes.
- **Interruptor de desactivación del inquilino** (`user-management`, migración `20261109130000`). Tres columnas en `iam_agent_policies`: `tenant_actuation_off`, que no admite nulos y tiene false como valor por defecto, y `actuation_off_at` y `actuation_off_by`, que admiten nulos. `v1.3.0` no tiene operaciones de políticas de agentes.
- **Propuestas de agentes** (`device-management`, migración `20261125090000`). Una tabla nueva, `agent_proposals`.
- **Codificador de comandos** (`device-management`, migración `20261109000100`). Una columna que admite nulos, `encoder`, en las definiciones de comandos.
- **Notificar al despejar** (`notification-management`, migraciones `20261109000100`, `20261109000200` y `20261110000100`). Una columna `notify_on_clear` con valor por defecto false en las políticas, columnas por ciclo y un índice parcial en la tabla de estado, y `notify_on_clear_since` que admite nulos en las políticas, fijada en `updated_at` solo donde `notify_on_clear` es true y `notify_on_clear_since IS NULL`.
- **Origen del estado de notificación** (`notification-management`, migración `20261110000200`). Seis columnas que admiten nulos en la tabla de estado: `originator_type`, `originator_id`, `device_token`, `device_name`, `device_type_token` y `source_at`. Sin rellenar filas existentes.
- **Confirmaciones pendientes** (`outbound-connectors`, migraciones `20261109120000` y `20261110120000`). Una tabla nueva, `lpwan_pending_acks`, y una columna `device_ref` que admite nulos en ella.

**La actualización solo avanza; no hay vuelta a `v1.3.0`.** Una actualización fallida se repara ejecutando de nuevo `dcctl upgrade` con el `dcctl` de `v1.4.0`. Esa herramienta rechaza `--version v1.3.0`. Sigue la [política de versiones y actualización](./versioning-policy.md).

#### Problemas conocidos {#v140-known-issues}

- **Un downlink LoRaWAN confirmado sobre el que el servidor de red nunca informa se resuelve al llegar su plazo,** como `TIMEOUT` con entrega desconocida.
- **Una escalada que ya hubiera ganado su reclamación antes de que se marcara un despeje aún puede entregarse después de él,** y los destinos de una escalada registrados después de que se reclamara el despeje no reciben ningún despeje.
- **`dcctl bootstrap --size` no escribe el `max_wal_size` relacional.** Fíjalo al instalar y repítelo en cada instalación; una ejecución sin él restablece el valor por defecto de PostgreSQL.
- **Las propuestas distintas de ACK/CLEAR de alarma no se ejecutan,** aunque se aprueben; las herramientas MCP no ejecutan acciones.
- **El interruptor de emergencia no revierte una acción ejecutada.** Actúa en la siguiente decisión; una acción que ya la haya pasado puede completarse.

### v1.3.0 — los comandos llegan a dispositivos HTTP y LoRaWAN, y cada entrega deja constancia {#v130-upgrade}

`v1.3.0` es una actualización in situ desde `v1.2.0`: `dcctl install` para el clúster y después un `dcctl upgrade` por cada instancia, como se describe [para v0.17.0](https://github.com/devicechain-io/devicechain/blob/v0.17.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v0170-upgrade). Pasa a `dcctl install` los ajustes con los que se instaló el clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Durante la actualización se ejecutan diez migraciones añadidas (cinco en `device-management`, dos en `command-delivery`, dos en `event-management` y una en `notification-management`), y el diario de auditoría de cada área gana cuatro columnas que admiten nulos; agregan tablas, columnas que admiten nulos e índices, y no cambian ninguna fila existente. Consulta [Cambios de esquema y vuelta atrás](#v130-going-back). Ejecuta `dcctl install` antes de actualizar la instancia, como arriba: además pasa Alertmanager a la configuración de enrutamiento que escribe `dcctl` (consulta [Las alertas llegan a una persona](#v130-alert-routing)). **La audiencia de los tokens se aplica ahora por defecto:** lee [La audiencia de los tokens se aplica por defecto](#v130-token-audience) antes de actualizar, sobre todo si escribes tú mismo la configuración de la instancia. Si vienes de `v1.1.0`, lee primero las [notas de `v1.2.0`](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-upgrade) y sigúelas con `--version v1.3.0`.

```bash
dcctl install local --version v1.3.0
dcctl upgrade local devicechain --version v1.3.0 --dry-run
dcctl upgrade local devicechain --version v1.3.0
```

Se aplica la [política de versiones y actualización](./versioning-policy.md). Esta versión amplía las interfaces cubiertas (nuevas consultas, mutaciones, campos y valores de enumeración GraphQL; nuevas rutas HTTP para dispositivos; nuevas banderas y comandos de `dcctl`; un nuevo tipo en el SDK de C#) y no elimina ninguna. Tres cosas de la propia política son nuevas en esta versión:

- **Un nivel de vista previa.** Una superficie nueva puede pasar sus primeras versiones menores en [vista previa](./versioning-policy.md#preview-interfaces), donde todavía puede cambiar en una versión menor; las notas de la versión dicen entonces qué cambió y qué hacer. En esta versión las superficies en vista previa son las pruebas de reglas (la operación `evaluateRules` de la API de event-processing, los formatos de archivo de escenario y de prueba, y `dcctl test`) y `dcctl product export`, `plan` y `apply` con su formato de paquete (`product.devicechain.io/v1alpha1`). Puedes construir sobre ellas, y cualquier cambio se anunciará aquí.
- **El catálogo de herramientas MCP no está cubierto.** Los nombres de las herramientas, sus entradas y la forma de sus resultados pueden cambiar en una versión menor, y esta los cambia: consulta [Los resultados de las herramientas MCP indican su procedencia](#v130-mcp-provenance).
- **Valores de Helm.** Los valores que esta versión añade o cambia no están cubiertos por la política de compatibilidad, y esta sección es donde una versión menor lo indica. Cada valor nuevo está desactivado, o conserva el comportamiento de `v1.2.0`, por defecto.

Novedades:

- **La audiencia de los tokens se aplica por defecto,** como anunciaron las notas de `v1.2.0`. Una API rechaza con `401` un token vinculado a un recurso que no sirve, y el servidor de autorización rechaza con `invalid_target` una solicitud de un recurso que la instancia no sirve. Los tokens vinculados a nada, que incluyen todos los inicios de sesión y todos los tokens de servicio, no se ven afectados, y los tokens MCP tampoco. Consulta [La audiencia de los tokens se aplica por defecto](#v130-token-audience).
- **Los comandos llegan a dispositivos que no mantienen una conexión.** Un dispositivo que solo habla HTTP recoge sus comandos con un sondeo largo, confirma la recepción y responde. Un dispositivo detrás de un servidor de red ChirpStack v4 recibe su comando como un downlink LoRaWAN. Cada comando registra ahora cómo se observó su entrega, en todos los transportes. Consulta [Comandos por HTTP y constancia de entrega](#v130-http-command-pull) y [Downlink LoRaWAN](#v130-lorawan-downlink).
- **Las alertas llegan a una persona.** `dcctl install` y `dcctl upgrade` envían las alertas de la propia plataforma a un webhook, a una dirección de correo o a una configuración propia; un receptor de hombre muerto avisa cuando el propio sistema de alertas se calla; cada alerta enlaza con una entrada de la guía de alertas; y una instancia que no envía sus alertas a ningún sitio lo dice. Consulta [Las alertas llegan a una persona](#v130-alert-routing).
- **Los webhooks salientes se pueden firmar,** según la especificación abierta Standard Webhooks, para la acción de regla `httpCall` y para los canales de notificación webhook. Consulta [Webhooks salientes firmados](#v130-signed-webhooks).
- **El endpoint HTTP de eventos dice la verdad antes.** Un inquilino desconocido o una credencial que no se autentica se responden con `401` de inmediato, el `400` de un documento rechazado señala el campo culpable y un evento aceptado lleva un recibo. Consulta [El endpoint HTTP de eventos responde 401](#v130-http-events-401).
- **Un dispositivo puede enviar su propia carga útil para el decodificador de su perfil,** en una nueva ruta opcional. Consulta [La carga útil propia de un dispositivo, decodificada](#v130-device-uplinks).
- **Las conexiones con certificado se pueden comprobar con OCSP,** por ancla de confianza, desactivado por defecto. Consulta [OCSP para conexiones con certificado](#v130-ocsp).
- **El producto de un inquilino se puede guardar como archivos** con `dcctl product export`, `plan` y `apply` (vista previa). Consulta [Producto como código](#v130-product-as-code).
- **Las reglas de detección se pueden probar con pruebas unitarias** con `dcctl test` y la consulta `evaluateRules` (vista previa). Consulta [Pruebas de reglas](#v130-rule-tests).
- **Se registra el uso diario de eventos de cada inquilino,** y el operador puede leerlo. Consulta [Uso de eventos por inquilino](#v130-usage-metering).
- **Las campañas envían comandos según un calendario:** cron, fechas de calendario, festivos y la salida o la puesta del sol en la posición instalada de cada dispositivo. Consulta [Campañas](#v130-campaigns) y [Posición instalada](#v130-installed-position).
- **Se registran las entregas de notificaciones,** una fila por intento, con el estado de cada canal. Consulta [El registro de entregas de notificaciones](#v130-delivery-ledger).
- **Clasificación de alarmas en la consola:** las alarmas enlazan con su dispositivo y muestran unidades, se pueden reconocer varias a la vez, un dispositivo tiene una pestaña de Alarmas y la página de inicio cuenta las alarmas y los dispositivos en línea. Los operadores también pueden anotar qué resultó ser una alarma. Consulta [La consola busca dispositivos y clasifica alarmas](#v130-console) y [Disposiciones de alarmas](#v130-alarm-dispositions).
- **Términos de vocabulario en el modelo nativo:** los tipos de dispositivo, activo, área y cliente, los tipos de relación y las métricas pueden llevar términos de Brick, SAREF, Smart Data Models y NGSI-LD. Consulta [Vínculos semánticos](#v130-semantic-bindings).
- **Los resultados de las herramientas MCP marcan como datos el texto escrito por dispositivos y usuarios,** bajo un miembro `untrusted` con su procedencia. Consulta [Los resultados de las herramientas MCP indican su procedencia](#v130-mcp-provenance).

#### Antes de actualizar {#v130-before-you-upgrade}

Lo que exige actuar, en el orden en que hacerlo:

- **Comprueba si hay tokens vinculados al recurso equivocado y decide si te quedas fuera de la aplicación obligatoria durante esta versión menor.** La audiencia de los tokens se aplica ahora por defecto. En la instancia `v1.2.0`, `{__name__=~"devicechain_[a-z0-9]+_token_audience_checks_total", verdict="would_reject"}` y `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}` deben marcar cero; si es así, el nuevo valor por defecto no cambia nada para tus clientes. Si suministras tú mismo la configuración de la instancia (`instance.existingSecret`) y ejecutas el área MCP, añade antes el bloque `tokenAudience` al documento, o los agentes de IA perderán el acceso MCP. Consulta [La audiencia de los tokens se aplica por defecto](#v130-token-audience).
- **Si enrutaste las alertas editando a mano el Secret del propio Alertmanager, pasa antes esa configuración a `dcctl install --alert-config <archivo>`.** Desde esta versión Alertmanager lee el Secret `dc-alertmanager-config` que escribe `dcctl`, así que una configuración editada a mano deja de leerse cuando ejecutas `dcctl install`. Un clúster instalado con una versión anterior también necesita un `dcctl install` antes de que `dcctl upgrade` acepte las banderas `--alert-*`. Consulta [Las alertas llegan a una persona](#v130-alert-routing).
- **Ten en cuenta que `POST /{instanceId}/{tenant}/events` responde ahora `401` a un inquilino desconocido o a una credencial que no se autentica.** Estas solicitudes se respondían con `202` y luego se rechazaban, así que sus eventos nunca se almacenaban. Un dispositivo que recibe `401` necesita que se corrija el nombre del inquilino o su credencial; un dispositivo cuyos eventos se almacenan no ve ningún cambio. Durante la actualización gradual, los pods antiguos siguen respondiendo `202` a estas solicitudes. Consulta [El endpoint HTTP de eventos responde 401](#v130-http-events-401).
- **Activa el modo OCSP de un ancla de confianza solo cuando la actualización gradual haya terminado.** Mientras no todas las réplicas de `device-management` ejecuten esta versión, las antiguas no comprueban OCSP, así que un certificado que una réplica rechaza podría ser admitido por otra. Consulta [OCSP para conexiones con certificado](#v130-ocsp).
- **Activa también las demás funciones opcionales nuevas solo cuando termine el despliegue.** Todas están desactivadas por defecto y por el mismo motivo: un pod de `v1.2.0` que sigue en marcha no las conoce.
  - Pon el `commandChannel` de un tipo de dispositivo en `HTTP_PULL` solo cuando todas las réplicas de todos los servicios estén actualizadas; un `command-delivery` anterior publica los comandos de ese dispositivo donde nadie los lee, y caducan.
  - Activa `httpIngest.deviceUplinks` solo cuando todas las réplicas de `device-management` estén actualizadas; una anterior rechaza el uplink después de que se respondiera `202`.
  - Activa la firma de webhooks (`signingSecretRef` en una acción `httpCall`, `"signing": true` en un canal webhook) solo cuando todas las réplicas de `outbound-connectors` y `notification-management` estén actualizadas; una anterior entrega sin firmar.
  - Crea las campañas con disparadores de salida o puesta del sol después del despliegue: una réplica de `user-management` de `v1.2.0` se niega a emitir el token que lee las posiciones instaladas, así que la planificación solar informa de un error y lo reintenta hasta que esa réplica desaparece.
- **Mueve cualquier identificador de credencial Bearer de `httpCall` que empiece por `webhook-signing/`.** Ese espacio de nombres queda reservado para los secretos de firma, y una regla que nombre un identificador así se rechaza al publicarla. Consulta [Webhooks salientes firmados](#v130-signed-webhooks).
- **Actualiza los clientes MCP que leen texto libre del resultado de una herramienta.** El nombre, la descripción y el identificador externo de un dispositivo, el nombre y la unidad de una métrica, la clave de una alarma y el nombre y el error de un comando están ahora bajo `untrusted`, como `{ "value": ..., "source": ... }`. El catálogo de herramientas MCP no es una interfaz cubierta, así que esto llega en una versión menor. Consulta [Los resultados de las herramientas MCP indican su procedencia](#v130-mcp-provenance).
- **Espera que `devicechain_eventprocessing_detect_late_samples_total` marque más.** Las lecturas tardías que descarta una ventana fija cerrada o una sesión cerrada ahora se cuentan, como ya se contaban las de los demás tipos de regla; hasta ahora se descartaban sin dejar rastro. Los resultados de la detección no cambian. Vuelve a ajustar la referencia de un panel o una alerta que hayas calibrado con el ritmo anterior. Consulta [Pruebas de reglas](#v130-rule-tests).
- **Publica los perfiles y tipos de activo con vínculos semánticos después del despliegue.** Durante la ventana gradual, un pod antiguo que atiende la publicación escribe una instantánea sin los vínculos. El borrador los conserva, y la siguiente publicación desde un pod actualizado los fija. Consulta [Vínculos semánticos](#v130-semantic-bindings).
- **Espera que la migración de `event-management` tarde más en un almacén de eventos grande.** Construye un índice sobre la tabla de eventos fragmento a fragmento. Consulta [Uso de eventos por inquilino](#v130-usage-metering).
- **Añade las nuevas alertas y métricas a tu enrutamiento y tus paneles.** `devicechain_usermanagement_service_token_checks_total` es nueva ([detalles](#v130-service-token-ceiling)), igual que las series OCSP de las conexiones con certificado y `devicechain_notificationmanagement_ledger_writes_total`.
- **No hay que hacer nada por el resto de esta versión.** Todos los demás cambios son aditivos o están desactivados por defecto. `infrastructure.tokenAudience.enforcement: observe` se acepta solo en esta versión, como exclusión, y `v1.4` lo rechaza. Sigue vigente lo declarado obsoleto en `v1.2.0` para una solicitud `client_credentials` que indica un `resource` sin un `scope`.

Autoridades nuevas:

- **`usage:read`** es una autoridad de nivel de sistema. Lee el uso de eventos de cada inquilino en la API de administración de event-management. El rol integrado `superuser` tiene `*` y por tanto la tiene; concédela a otro rol de sistema para que ese rol pueda leer el uso. Ningún rol de inquilino puede tenerla.
- **`location:installed:read`** es una autoridad de nivel de inquilino. Lee la posición instalada de un dispositivo o de un activo y nada más. `location:read` la implica, así que un rol que ya tiene `location:read` (y `tenant-admin`, que tiene `*`) no necesita cambios. No forma parte de la base del rol de lectura: a un rol que deba ver las posiciones instaladas pero no la ubicación en vivo de los dispositivos se le concede solo `location:installed:read`.

Valores de Helm nuevos, cada uno desactivado o sin cambios por defecto:

| Valor | Por defecto | Qué hace |
| --- | --- | --- |
| `functionalAreas.device-management.config.callout.ocsp.enabled` | `false` | El interruptor de la instancia para OCSP. Desactivado, no se consulta a ningún respondedor; consulta [OCSP](#v130-ocsp) para las demás claves `ocsp.*`. |
| `functionalAreas.device-management.config.callout.ocsp.allowRequireCurrent` | `false` | Permite que un ancla use `REQUIRE_CURRENT`. |
| `functionalAreas.event-sources.config.httpIngest.deviceUplinks` | `false` | Sirve `POST /{instanceId}/{tenant}/devices/{token}/uplink`. |
| `functionalAreas.event-sources.config.httpIngest.maxCommandPollsPerTenant` | `256` | Sondeos de comandos HTTP retenidos por inquilino y por réplica; uno más se responde con `429`. No aparece en el `values.yaml` del chart; fíjalo si necesitas otro límite. |
| `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled` | `false` | Envía comandos a dispositivos LoRaWAN a través de su servidor de red. No aparece en el `values.yaml` del chart; fíjalo para activar la función. |

#### La audiencia de los tokens se aplica por defecto {#v130-token-audience}

Las notas de `v1.2.0` lo anunciaron: lo que [Los tokens se comprueban contra su recurso](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-token-audience) contaba como `would_reject` ahora se rechaza. Un `infrastructure.tokenAudience.enforcement` sin definir significa `enforce`, para las dos comprobaciones:

- **Una API rechaza un token vinculado a un recurso que no sirve,** con `401`, la misma respuesta que para un token no válido. La API de un área acepta un token vinculado a nada, a esa área o a un servidor MCP registrado.
- **El servidor de autorización rechaza una solicitud de un recurso que esta instancia no sirve:** `invalid_target` en el endpoint de autorización y en la concesión `client_credentials`, e `invalid_grant` para una renovación o un código de autorización vinculados a un recurso que ya no se sirve. Un token de renovación rechazado así queda gastado, de modo que el cliente debe autorizarse de nuevo.

Los tokens vinculados a nada no se ven afectados. Eso incluye todos los inicios de sesión desde la consola, la aplicación de paneles, el SDK de C# y `dcctl`, y todos los tokens de servicio. Los tokens MCP están vinculados al servidor MCP, que el chart registra, así que tampoco se ven afectados. Con `enforce`, los rechazos se cuentan en las mismas series con `verdict="rejected"`, y las dos alertas `info`, `TokenAudienceWouldReject` y `OAuthResourceWouldReject`, solo pueden dispararse en una instancia que haya fijado `observe`.

Así se ve un rechazo: una cuenta de servicio que pide un token vinculado a un área y lo usa en otra recibe un rechazo en la segunda.

```bash
TOKEN=$(curl -s -u "$CLIENT_ID:$CLIENT_SECRET" \
  -d grant_type=client_credentials -d scope=read-only \
  --data-urlencode "resource=https://dc.example.com/api/device-management" \
  https://dc.example.com/api/user-management/oauth/token | jq -r .access_token)

# Aceptado: el token está vinculado a device-management.
curl -s -o /dev/null -w '%{http_code}\n' https://dc.example.com/api/device-management/graphql \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{ __typename }"}'

# 401 con enforce: event-management no acepta un token vinculado a device-management.
curl -s -o /dev/null -w '%{http_code}\n' https://dc.example.com/api/event-management/graphql \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{ __typename }"}'
```

Una integración que llama a más de un área debe omitir el `resource`: todas las áreas aceptan un token sin vincular. Consulta [Vincular un token a un recurso](../user-guide/service-accounts.md#binding-a-token-to-a-resource).

**Antes de actualizar,** comprueba en la instancia `v1.2.0` si hay tráfico que la aplicación obligatoria rechazaría:

- El contador de las API es una serie por área, y su nombre lleva el área sin guiones: `devicechain_devicemanagement_token_audience_checks_total`, `devicechain_eventmanagement_token_audience_checks_total`, etc. Selecciónalas todas con `{__name__=~"devicechain_[a-z0-9]+_token_audience_checks_total", verdict="would_reject"}`. El contador del servidor de autorización es `devicechain_usermanagement_oauth_resource_checks_total{verdict="would_reject"}`. Los dos deben marcar cero, y las dos alertas deben estar en silencio.
- Si alguno se mueve, los avisos con límite de frecuencia nombran al cliente: `Token audience is not accepted by this plane` en el registro del área, y `OAuth resource is not one this instance serves` en el registro de `user-management`. Cambia el `resource` que envía ese cliente: omítelo o usa uno que la instancia sirva.

**Para quedarte fuera durante esta versión menor,** fija `enforcement: observe` antes de actualizar, para que ningún pod aplique la comprobación durante la actualización gradual. En los valores de Helm o en una [superposición de valores](./tuning-an-instance.md#tunable) de `dcctl` la clave es `instance.config.infrastructure.tokenAudience.enforcement`. `v1.4` rechaza `observe`: un servicio se detiene al arrancar, y el esquema de valores del chart lo rechaza. Sin esa exclusión, los pods antiguos y los nuevos coinciden durante la actualización, así que durante unos minutos un token vinculado al recurso equivocado puede ser aceptado por una réplica de `v1.2.0` y rechazado por una de `v1.3.0`.

:::warning Helm sin dcctl con `instance.existingSecret` pierde el acceso MCP salvo que el documento lo incluya
El chart no escribe el bloque `tokenAudience` en una configuración que suministras tú. Con `enforce`, el área `mcp` comprueba al arrancar que su `resourceUrl` figura en `infrastructure.tokenAudience.mcpResources`, y **se niega a arrancar** si no figura, así que los agentes de IA pierden el acceso MCP. `dcctl upgrade` rechaza ese documento antes de que se renueve nada, pero un `helm upgrade` directo no puede mirar dentro de tu Secret. Antes de actualizar, añade el bloque al documento, con tu origen y la `resourceUrl` del servidor MCP exactamente como está configurada el área `mcp`, con el esquema y el host en minúsculas:

```json
"infrastructure": {
  "tokenAudience": {
    "areaBaseUrl": "https://iot.example.com/api",
    "mcpResources": ["https://iot.example.com/api/mcp"]
  }
}
```

O fija `"enforcement": "observe"` en el mismo bloque. En los dos casos, **fija `instance.existingSecretChecksum` al sha256 del nuevo documento `instance`**; si no, no se renueva ningún pod y todos siguen leyendo el anterior.
:::

- **Si fijaste a mano `functionalAreas.mcp.config.resourceUrl`** con una mayúscula en el esquema o el host, o con el identificador de un área, como `<origin>/api/device-management`, `helm upgrade` ahora falla y explica por qué. Ponlo en minúsculas o cámbialo, o fija `observe`; con `observe`, el chart deja ese valor fuera de la lista, como hacía `v1.2.0`.
- **Volver a `v1.2.0`** no exige nada por esto: `v1.2.0` lee el mismo bloque y usa `observe` por defecto.

#### Las alertas llegan a una persona {#v130-alert-routing}

Cada regla de alerta que incluye el chart lleva ahora un `runbook_url` que abre su entrada en la nueva [Guía de alertas](./alert-runbook.md). Nada más de una regla cambia: nombres, expresiones, duraciones `for` y etiquetas son los mismos.

`dcctl install` y `dcctl upgrade` aceptan `--alert-webhook`, `--alert-email` (con `--alert-smtp-host`, `--alert-smtp-from`, `--alert-smtp-user` y `--alert-smtp-password-file`), `--alert-config`, `--alert-watchdog-webhook` y `--clear-alert-routing`. Alertmanager lee ahora su configuración del Secret `dc-alertmanager-config` del namespace `monitoring`, que `dcctl install` escribe antes de aplicar la pila de monitoreo. Volver a ejecutar `dcctl install` sin ninguna bandera `--alert-*` conserva el enrutamiento existente, y una primera instalación sin ellas escribe una configuración sin receptor, que es el comportamiento de las versiones anteriores. Indicar cualquier bandera `--alert-*` vuelve a declarar todo el enrutamiento. Con `--alert-watchdog-webhook`, la alerta `Watchdog`, que está siempre activa, va a ese receptor cada minuto, así que su silencio te dice que las alertas se han detenido.

Una instancia indica si alguien escucha en su nueva condición `AlertsRouted`, y `dcctl instances list` muestra `ALERTS NOT ROUTED` para una que no envía sus alertas a ningún sitio. La condición nunca hace que la instancia deje de estar lista.

Vuelve a ejecutar `dcctl install` una vez en un clúster instalado con una versión anterior antes de usar las banderas con `dcctl upgrade`; en caso contrario `dcctl upgrade` las rechaza, porque el Alertmanager anterior no lee ningún Secret nuestro. `dcctl` comprueba `--alert-config` en busca de los errores que dejarían las alertas sin enrutar, pero no es el analizador del propio Alertmanager: ejecuta antes `amtool check-config` sobre el archivo. Consulta [Enrutar las alertas a una persona](./observability.md#alert-routing).

#### Comandos por HTTP y constancia de entrega {#v130-http-command-pull}

Un dispositivo que solo habla HTTP, como un sensor de batería que se despierta, informa y vuelve a dormir, ahora puede recibir comandos. Pon el `commandChannel` de su tipo de dispositivo en `HTTP_PULL`, y sus comandos esperan a que el dispositivo los pida:

- `GET /{instanceId}/{tenant}/devices/{device}/commands?wait=0..30` retiene la solicitud hasta `wait` segundos (25 por defecto) hasta que haya un comando pendiente;
- `POST .../commands/{command}/ack` confirma la recepción, antes de que el dispositivo actúe;
- `POST .../commands/{command}/response` responde.

El dispositivo presenta su token de acceso en cada solicitud. Un tipo de dispositivo sin canal, o con `PUBLISH`, recibe sus comandos exactamente igual que antes. Un comando para un dispositivo que sondea puede llevar `deliveryMode: ONEWAY` (lo completa su confirmación) y puede fijar `maxAttempts`; para cualquier otro dispositivo esas opciones se rechazan con `DELIVERY_OPTION_NOT_APPLICABLE`. Cada réplica de `event-sources` retiene como máximo `httpIngest.maxCommandPollsPerTenant` sondeos de un inquilino (256 por defecto) y responde `429` por encima. El SDK de C# tiene un nuevo `HttpCommandPuller`.

Cada comando registra ahora la constancia de su entrega: `deliveredTime` y `deliveryEvidence` (`DEVICE_ACK` para la confirmación de un dispositivo que sondea, `DEVICE_RESPONSE` para una respuesta en cualquier transporte). No se añade ningún estado de comando. Consulta [Dispositivos que recogen sus comandos por HTTP](../concepts/commands.md#http-command-pull).

- **Actívalo después del despliegue.** Actualiza primero `device-management`, y pon `HTTP_PULL` solo cuando todas las réplicas de todos los servicios ejecuten esta versión. Una réplica de `command-delivery` ya actualizada mientras `device-management` todavía no lo está retiene los comandos de todos los dispositivos por los que pregunta, y los libera cuando `device-management` responde, así que los dispositivos normales sufren un retraso durante el solapamiento, nunca se envían al sitio equivocado.
- **La nueva ruta no se expone a través del ingress del chart,** igual que `/events`.

#### Downlink LoRaWAN {#v130-lorawan-downlink}

Un comando para un dispositivo que informa a través de una integración entrante se puede enviar ahora al dispositivo como un downlink LoRaWAN, encolado en su servidor de red ChirpStack v4. Esta primera versión acepta cargas útiles `hex` o `base64` en bruto y envía downlinks sin confirmación; `confirmed: true` se rechaza, nunca se rebaja. El enlace es un conector publicado del nuevo tipo `lorawan_downlink`, y la clave de API de ChirpStack es el secreto sellado del conector. El éxito significa que el servidor de red encoló el downlink, no que el dispositivo lo recibiera.

Está desactivado salvo que `functionalAreas.outbound-connectors.config.lpwanDownlink.enabled` sea `true`; entonces el chart hace que `command-delivery` despache estos comandos, y rechaza una copia de esa bandera fijada a mano. La consulta `connectorTypes` lista un valor más. La primera vez que se activa, un comando despachado en los segundos anteriores a que arranque el adaptador caduca. Consulta [Downlink LoRaWAN](../guides/inbound-integrations.md#lorawan-downlink).

#### Webhooks salientes firmados {#v130-signed-webhooks}

La acción de regla `httpCall` y el canal de notificación webhook pueden firmar lo que envían, según la especificación abierta Standard Webhooks. Una entrega firmada lleva `webhook-id`, `webhook-timestamp` y `webhook-signature`; el cuerpo no cambia, y una entrega sin firmar es exactamente la de antes. La plataforma genera el secreto de firma, que se muestra una sola vez y nunca vuelve a poder leerse, y se puede rotar con un solapamiento durante el cual las entregas llevan las dos firmas. Una entrega que debería firmarse y no puede se rechaza, nunca se envía sin firmar. Consulta [Firmar los webhooks salientes](../guides/signed-webhooks.md).

- **Activa la firma después del despliegue.** Una réplica anterior de `outbound-connectors` o de `notification-management` ignora `signingSecretRef` y `signing`, y entrega sin firmar.
- **`webhook-signing/` queda reservado.** Una regla cuyo identificador de credencial Bearer esté bajo él se rechaza al publicarla; mueve antes el identificador.
- **Un secreto nuevo tarda hasta un minuto en llegar a todas las entregas `httpCall`,** porque el servicio de conectores guarda los secretos en caché.

#### El endpoint HTTP de eventos responde 401 {#v130-http-events-401}

`POST /{instanceId}/{tenant}/events` respondía `202 Accepted` a solicitudes que nunca podían almacenarse: un nombre de inquilino con una errata o una credencial que no se autenticaba. Se rechazaban después, y al dispositivo se le había dicho que sus datos estaban a salvo. Ahora se rechazan en la entrada:

- **`401`** para un inquilino desconocido y para una credencial que no se autentica (desconocida, caducada, con un secreto erróneo, de otro dispositivo, o ausente donde la instancia la exige). Todas las causas reciben el mismo estado, las mismas cabeceras y el mismo cuerpo, `{"error":"unauthorized"}`, así que el endpoint no revela qué inquilinos ni qué dispositivos existen. Los operadores distinguen las causas en `total_http_ingest_refused{source,reason}` y en una línea de registro muestreada.
- **`400` sigue siendo `400`** para una carga útil mal formada. Un cuerpo que es JSON válido pero se rechaza lleva ahora un cuerpo JSON cuyos `errors` señalan cada valor culpable con un JSON Pointer; `message` es la frase que siempre llevó el cuerpo de texto plano. Un cuerpo que no es JSON conserva su `400` en texto plano.
- **`X-DC-Event-Id`** en un `202`, y en el `400` de un documento rechazado, es un recibo de la solicitud.

La comprobación falla en abierto: si los servicios a los que pregunta están lentos o no responden, la solicitud se acepta como antes y se juzga después. Una credencial que falla cinco veces seguidas deja de consultarse durante un tiempo (1 segundo, que se duplica hasta 60), así que el `401` no sirve para adivinar secretos. La respuesta en caché no es una autoridad: `device-management` sigue autenticando cada evento, así que una credencial recién desactivada se acepta en la entrada durante 30 segundos como mucho y después se rechaza como antes. Los rechazos en la entrada se cuentan y se registran, pero no son registros de eventos fallidos.

#### La carga útil propia de un dispositivo, decodificada {#v130-device-uplinks}

Un dispositivo que envía una carga útil binaria o un JSON propio del fabricante, en lugar del documento de evento de DeviceChain, puede publicarla en `POST /{instanceId}/{tenant}/devices/{token}/uplink`, y la lee el decodificador de carga útil del perfil de su tipo de dispositivo. El dispositivo presenta su propia credencial en las cabeceras `X-DeviceChain-Credential-*`, y se comprueba antes que nada: quien no la tiene recibe un único `401`, exista o no el dispositivo y tenga o no decodificador. La decodificación, su presupuesto, su estado y sus muestras son los mismos que usa la ruta de las integraciones entrantes. Un tipo de dispositivo sin decodificador se responde con `422`.

La ruta está desactivada salvo que `functionalAreas.event-sources.config.httpIngest.deviceUplinks` sea `true`. Actívala solo cuando todas las réplicas de `device-management` ejecuten esta versión: una réplica anterior rechaza ese uplink y lo registra como evento fallido después de que al dispositivo se le respondiera `202`.

#### OCSP para conexiones con certificado {#v130-ocsp}

Un ancla de confianza puede ahora preguntar al respondedor OCSP de la autoridad de certificación si un certificado sigue siendo válido, como segunda fuente de revocación junto a las [listas de revocación](../concepts/device-certificates.md#revocation) que subes. Está desactivado en todas partes por defecto: el interruptor de la instancia `functionalAreas.device-management.config.callout.ocsp.enabled` es `false`, y el nuevo `ocspMode` de cada ancla es `OFF`.

- **`ENFORCE_KNOWN`** rechaza un certificado que el respondedor declara revocado y admite uno cuyo estado no puede conocer.
- **`REQUIRE_CURRENT`** rechaza cualquier certificado cuyo estado no puede confirmar. Además necesita `callout.ocsp.allowRequireCurrent: true` y una URL de respondedor en el ancla (`ocspResponderUrl`).
- **Lo que cuesta.** OCSP se ejecuta solo después de que hayan pasado las comprobaciones de cadena, caducidad, lista de revocación y revocación manual, así que un certificado ya rechazado no cuesta ninguna consulta. Las respuestas buenas se guardan en caché; una respuesta de revocado rechaza la conexión y nunca se guarda. Las demás claves bajo `callout.ocsp` (`fetchDeadline`, `maxInFlight`, `maxAge`, `failureTtl`, `cacheEntries`, `maxResponderHostsPerTenant`) acotan las consultas, y sus valores por defecto están en el `values.yaml` del chart.
- **Lo que todavía no hace.** Una respuesta de revocado rechaza cada conexión, pero no se almacena como revocación ni termina las sesiones ya abiertas; para eso, sube una lista o revoca el certificado.
- **Resultados y métricas nuevos.** `callout_x509_total` gana los resultados `ocsp_revoked` y `ocsp_unavailable`, y cinco series `x509_ocsp_*` nuevas informan de las consultas, la caché, el cortocircuito y las conexiones admitidas sin respuesta.

**Cambia las anclas a `ENFORCE_KNOWN` o `REQUIRE_CURRENT` solo cuando la actualización gradual haya terminado.** Hasta entonces, las réplicas antiguas no comprueban OCSP, así que un certificado que una réplica rechaza podría ser admitido por otra. Con el interruptor de la instancia desactivado, un ancla en `REQUIRE_CURRENT` rechaza las nuevas conexiones con certificado, y la API se niega a activar un ancla.

#### Los resultados de las herramientas MCP indican su procedencia {#v130-mcp-provenance}

El catálogo de herramientas MCP (nombres de herramientas, entradas y forma de los resultados) no está cubierto por la promesa de compatibilidad. La [política de versiones](./versioning-policy.md) lo incluye ahora entre lo que puede cambiar en una versión menor, y esta versión lo cambia.

Cada herramienta de lectura mantiene los tokens y los valores calculados por la plataforma en el nivel superior y pasa el texto libre a `untrusted`, como `{ "value": ..., "source": ... }`, donde `source` es `device-reported`, `operator` o `agent`. Los campos que se mueven son el `name`, la `description` y el `externalId` de un dispositivo, el `name` y la `unit` de una métrica, el `alarmKey` de una alarma, y el `name` y el `error` de un comando. Un cliente que los lea debe leerlos ahí. Las clases que contiene un resultado se listan en `_meta["io.devicechain/provenance"]`, y la descripción de cada herramienta dice que el texto bajo `untrusted` son datos, nunca instrucciones. Consulta [Lo que ve un asistente](../concepts/mcp.md#what-an-assistant-sees).

También es nuevo, y aditivo: los tokens de acceso OAuth llevan un claim `sid` (la sesión de la concesión), el diario de auditoría registra el `client_id` y la sesión del token que hizo cada cambio, y la línea del registro de auditoría de MCP pasa a `schema: 2`, que añade `scopes`.

#### Se observa el techo de autoridad de los tokens de servicio {#v130-service-token-ceiling}

El endpoint que emite tokens de servicio comprueba ahora cada solicitud contra las autoridades que su llamante debería necesitar, y cuenta el veredicto en `devicechain_usermanagement_service_token_checks_total{caller,verdict}`. No rechaza nada y emite exactamente igual que antes. Un ritmo distinto de cero de `over_ceiling`, `wildcard` o `unknown_caller` señala un llamante que una versión posterior que aplique el techo rechazaría; hoy no se espera ninguno.

#### Producto como código (vista previa) {#v130-product-as-code}

`dcctl product export <dir>` escribe el producto de un inquilino como archivos: tipos de dispositivo, perfiles con sus métricas, métricas calculadas, decodificadores de carga útil con sus vectores de prueba, comandos, reglas de detección y grafos del lienzo, paneles, conectores e integraciones. `dcctl product plan <dir>` muestra lo que cambiaría aplicar los archivos y no escribe nada (`--fail-on-changes` convierte la deriva en un código de salida), y `dcctl product apply <dir>` hace converger el inquilino con los archivos y publica lo que cambió. Los secretos nunca aparecen en un paquete: un conector nombra su credencial por identificador (`env:NAME`), y cada paquete se examina en busca de valores con aspecto de secreto. Solo se toca lo que nombra el paquete.

Es una superficie en **vista previa**: el árbol de comandos y el formato de paquete (`product.devicechain.io/v1alpha1`) pueden cambiar en una versión menor, y `dcctl` rechaza una versión de paquete que no conoce. Se ejecuta por completo en `dcctl`, a través de las API de inquilino publicadas, así que nada cambia en el servidor. Consulta [Producto como código](../guides/product-as-code.md).

#### Pruebas de reglas (vista previa) {#v130-rule-tests}

`dcctl test <dir|archivo>` ejecuta pruebas unitarias deterministas de las reglas de detección. Un `scenario.yaml` describe dispositivos y sus lecturas (constantes, un paseo aleatorio acotado, un calendario por escalones, o un archivo CSV o NDJSON) con fallos (huecos, duplicados, reordenaciones, ventanas sin conexión) y una semilla, y los archivos `*.test.yaml` dicen lo que debe ocurrir: una alarma activada o despejada dentro de un plazo, nunca activada, un comando o un webhook solicitado, etc. `dcctl test` imprime un informe, puede escribir XML JUnit (`--junit`) y sale con `0` cuando se cumplieron todas las expectativas, `1` cuando falló una y `2` cuando algo no se pudo juzgar. Una ejecución que no puede responder nunca sale en verde.

Usa una consulta nueva, `evaluateRules` (API de inquilino de event-processing, `device:read`), que ejecuta las reglas publicadas de un perfil, o un borrador, sobre los eventos de la solicitud, en tiempo de evento con la tolerancia de retraso de producción, y devuelve las transiciones, las acciones que solicitarían y por qué un resultado es parcial. No ejecuta nada y no toca ningún flujo. Los formatos de archivo (`devicechain.io/ruletest/v1alpha1`), la consulta y el comando están en el nivel de **vista previa**.

- **`devicechain_eventprocessing_detect_late_samples_total` marca más.** Las lecturas tardías que descarta una ventana fija cerrada o una sesión cerrada ahora se cuentan, como las de los demás tipos de regla. Las detecciones no cambian; un panel o una alerta calibrados con el ritmo anterior necesitan una nueva referencia.

#### Uso de eventos por inquilino {#v130-usage-metering}

`event-management` guarda un registro diario de lo que usó cada inquilino: los eventos que ingirió y los dispositivos que los enviaron (distintos, el máximo en una hora y la media horaria). Un operador lo lee con la consulta `tenantEventUsage(tenant, from, to)` en el nuevo endpoint `/admin/graphql` de `event-management`, de ámbito de instancia, que requiere un token de identidad con `usage:read`. El registro informa del uso y no impone nada.

- **Los eventos se cuentan el día en que se recibieron,** no el día con el que están fechados, así que una lectura tardía o recuperada nunca revisa un día terminado, y un duplicado reentregado se cuenta una vez.
- **Una hora es definitiva 10 minutos después de cerrarse,** y una hora definitiva no se reescribe nunca. Un día figura como `complete` cuando sus 24 horas son definitivas. Por eso, **un evento almacenado más de 10 minutos después del final de la hora en que se recibió no se cuenta.**
- **El registro empieza el día UTC en que se ejecuta por primera vez el servicio actualizado.** Los días anteriores figuran como no completos. Los eventos recibidos después de la actualización pero fechados dentro de fragmentos que ya estaban comprimidos en ese momento no se cuentan.
- **La actualización construye un índice sobre la tabla de eventos** fragmento a fragmento, así que no bloquea las escrituras de toda la tabla, pero en un almacén grande la migración de `event-management` tarda más de lo habitual.

#### Campañas {#v130-campaigns}

Una campaña envía una serie de comandos, con una espera entre pasos, a los dispositivos de un grupo de entidades según un calendario: una expresión cron en una zona horaria con nombre, fechas y horas de calendario, o la salida o la puesta del sol más un desfase, calculadas para cada dispositivo a partir de su [posición instalada](#v130-installed-position). Los festivos suprimen un disparo; nunca lo mueven. Los cambios de horario de verano siguen una sola regla: una hora que cae en el salto de primavera se dispara en el salto, y una hora repetida se dispara una vez. Cada disparo se sigue por dispositivo (pendiente, despachado, rechazado u omitido, con un motivo), y `retryCampaignFailed` vuelve a enviar el comando del ciclo a los dispositivos cuyo comando no tuvo éxito. Los comandos pasan por la ruta habitual de lotes, así que las ventanas de mantenimiento se siguen aplicando. Todas las réplicas ejecutan el planificador, y la base de datos decide cuál envía cada comando, así que nada se envía dos veces.

- **Una campaña no se puede editar.** Bórrala y vuelve a crearla; una campaña recreada no envía nada que venciera antes de crearse.
- **Los destinos son grupos.** Usa un grupo dinámico para seleccionar los dispositivos de un área.
- **Los disparadores solares necesitan que termine el despliegue.** Mientras quede alguna réplica de `user-management` de `v1.2.0`, la planificación de un disparo de salida o puesta del sol informa de un error y lo reintenta.

#### Posición instalada {#v130-installed-position}

Un dispositivo o un activo que no se mueve, como una farola, una plaza de aparcamiento o un armario, puede tener ahora una posición instalada: `setInstalledPosition` y `clearInstalledPosition` (`device:write`), las columnas opcionales `installedLatitude`, `installedLongitude`, `installedElevation` e `installedAccuracy` de una importación de flota, y el campo `installedPosition` de `Device` y `Asset`. Leerla requiere la nueva `location:installed:read`, que `location:read` implica. Nunca recurre a una posición que haya informado el dispositivo. `positionWithheld`, en cada dispositivo y activo, le dice a quien llama que no puede ver la posición, sin revelar si existe. Un área puede nombrar su geocerca de límite (`boundaryFenceToken`); una geocerca que nombra un área no se puede borrar.

#### El registro de entregas de notificaciones {#v130-delivery-ledger}

Cada intento de enviar la notificación de una alarma por un canal escribe ahora una fila: qué lo desencadenó, la política, el canal, los destinatarios, el resultado (`SENT`, `FAILED`, `REFUSED` o `NOT_ATTEMPTED`), el número de intento, la latencia y un código de error clasificado. Se lee con `notificationAttempts` (`notification:read`); `NotificationChannel.deliveryHealth` da el último éxito, el último fallo y los fallos consecutivos de cada canal. Las URL de entrega, que pueden ser credenciales en sí mismas, nunca se almacenan. Las filas se conservan 7 días por defecto.

El registro nunca retrasa un aviso: las filas se escriben en segundo plano desde una cola acotada, y una cola llena descarta la fila y la cuenta en `devicechain_notificationmanagement_ledger_writes_total{outcome}`. Así, una fila demuestra que hubo un intento; una fila que falta no demuestra nada.

#### Disposiciones de alarmas {#v130-alarm-dispositions}

`setAlarmDisposition` etiqueta el ciclo actual de una alarma como `TRUE_POSITIVE`, `FALSE_POSITIVE` o `EXPECTED`, con una nota opcional (`alarm:write`). Una etiqueta pertenece a un ciclo: cuando la alarma vuelve a activarse, el nuevo ciclo empieza sin etiqueta y los ciclos anteriores conservan la suya. `Alarm.disposition` lee la etiqueta del ciclo actual, y `alarmDispositions` lista todos los ciclos etiquetados. Etiquetar no cambia nada más de la alarma. Un miembro limitado a clientes lee las etiquetas de los ciclos que podía ver y no puede escribir ninguna.

#### Vínculos semánticos {#v130-semantic-bindings}

Un inquilino puede vincular términos de un vocabulario al modelo que ya tiene: `semanticTypes` en los tipos de dispositivo, activo, área y cliente, `semanticPredicate` y `semanticInverse` en los tipos de relación, y `semanticProperty` y `semanticPointClass` en las métricas. Los vocabularios incluidos son Brick 1.4.4, SAREF 4.1.1 con SAREF4CITY y SAREF4BLDG, los Smart Data Models de alumbrado público, aparcamiento y medio ambiente, y los términos básicos de NGSI-LD. Un inquilino activa los que usa, y puede añadir prefijos propios, con `setSemanticContext` (`device:write`). Cada término se comprueba en existencia y tipo, se almacena como IRI completo, y se rechaza con `INVALID_ARGUMENT` nombrando el término; un cambio de contexto que dejaría sin explicar un vínculo almacenado se rechaza con `SEMANTIC_CONTEXT_IN_USE`. `thingModel(includeSemanticTypes: true)` añade la propiedad semántica y la clase de punto de cada métrica al `@type` de su propiedad; por defecto su salida no cambia.

- **Durante la ventana gradual,** un pod antiguo que publica un perfil o un tipo de activo escribe una instantánea sin los vínculos. El borrador los conserva, y la siguiente publicación desde un pod actualizado los fija.

#### La consola busca dispositivos y clasifica alarmas {#v130-console}

La búsqueda de dispositivos y la de tipos de dispositivo aceptan un nuevo argumento opcional cada una, `nameContains`: una coincidencia de subcadena sin distinguir mayúsculas en el nombre o el token, donde `%` y `_` se toman literalmente y un texto en blanco coincide con todo. Un cliente que no lo envía obtiene los mismos resultados que antes. En la consola, la lista de dispositivos tiene una búsqueda por nombre y un filtro por tipo de dispositivo, ambos guardados en la dirección de la página, y los selectores de tipo de los formularios de dispositivo, creación en lote, importación y perfil de aprovisionamiento buscan en el servidor mientras escribes, en lugar de leer los primeros 1.000 tipos.

La lista de alarmas enlaza cada alarma con su dispositivo, muestra los valores con la unidad que declara el perfil del dispositivo, reconoce varias alarmas seleccionadas en una sola solicitud y guarda sus filtros en la dirección de la página. Un dispositivo tiene una pestaña de Alarmas, y la página de inicio cuenta las alarmas activas por severidad y los dispositivos en línea y fuera de línea. Son cambios solo de la consola.

#### El reloj adelantado de un dispositivo ya no mueve la detección de otros inquilinos {#v130-detect-watermark}

La detección mantiene un único reloj lógico para toda la instancia, y antes seguía el tiempo de evento más reciente que había visto. La plataforma acepta tiempos de evento hasta cinco minutos por delante del reloj del servidor, de modo que un solo dispositivo con el reloj adelantado podía llevar ese reloj compartido por delante del tiempo real. Entonces los temporizadores de ausencia de todos los inquilinos se disparaban antes de tiempo y las lecturas con ventana de dispositivos con el reloj correcto se descartaban por tardías. Desde esta versión, un mensaje mueve el reloj compartido como máximo hasta el momento en que el broker lo almacenó, más la tolerancia de retraso configurada. El evento conserva su propio tiempo, por lo que sus lecturas se siguen ventaneando y ordenando en los instantes que informó el dispositivo, y la vista previa de repetición aplica el mismo límite.

- **Los resultados de detección cambian solo en el caso que estaba mal.** Un dispositivo cuyo reloj está dentro de la tolerancia del reloj del broker (5 segundos por defecto) se comporta como antes. Con un reloj más adelantado, las alarmas de ausencia de los demás dispositivos se disparan ahora en su plazo real, y los temporizadores del dispositivo adelantado esperan a que el tiempo real los alcance.
- **Las instantáneas llevan una versión del reloj.** Una instantánea de detección escrita por esta versión la registra, y restaurar una instantánea anterior sigue funcionando. En los primeros momentos tras la actualización, el reloj restaurado puede seguir tan adelantado como estaba; se corrige solo cuando el tiempo real lo alcanza, y el servicio registra una advertencia al restaurar una instantánea así. Durante una actualización gradual, un pod de la versión anterior que tome el relevo escribe instantáneas sin la versión y la ignora al leer, así que no hay que hacer nada.
- **No cubre el caso contrario.** Un dispositivo que sube lecturas antiguas (almacenar y reenviar) todavía puede ver cómo las reglas con ventana las descartan por tardías, porque el reloj compartido ya avanzó. Los relojes por dispositivo no forman parte de esta versión.

#### Agentes de borde {#v130-edge}

No hay que hacer nada; ambas cosas están desactivadas por defecto y no cambian nada hasta que se configura un agente para ellas.

- **Un agente puede conservar un evento hasta que la plataforma lo haya aceptado.** Con `uplink.retireOn: accept`, el agente borra un evento almacenado solo cuando la plataforma informa de que lo aceptó de forma duradera, y lo vuelve a ofrecer si la plataforma lo descartó por superar el límite de ingesta del inquilino. El valor por defecto, `puback`, se comporta como antes. Actualiza la plataforma antes de pasar un agente a `accept`: un agente en ese modo no puede conectarse a una plataforma anterior, y el error de conexión nombra `uplink.retireOn`. Cuando su cola local se acerca a su presupuesto, un agente también puede descartar eventos almacenados por clase de prioridad (`local.priority.*`). Consulta [Retirar eventos cuando la plataforma los acepta](./edge-services.md#retire-on-accept).
- **La parte del agente del downlink de comandos a dispositivos situados detrás de él** se entrega con `commands.enabled` desactivado. La plataforma todavía no enruta comandos de pasarela, así que no tiene efecto en esta versión.

#### Documentación, hosts de ejemplo y notas para agentes {#v130-docs-hosts}

La documentación de los SDK de .NET y Unity y el comentario de documentación de `DeviceChainClient.Origin` usaban `demo.devicechain.io` como host de ejemplo, que no se resuelve; ahora usan `devicechain.example.com`. El SDK no tiene un host por defecto, así que no cambia ningún comportamiento. Una página nueva, [API de dispositivos de un vistazo](../reference/device-apis.md), da un comando para cada forma en que un dispositivo llega a la plataforma, y los paquetes `@devicechain/client` y `@devicechain/widget-sdk` y el SDK de .NET incluyen cada uno un `AGENTS.md` para agentes de programación. Una página nueva, [Proceso de seguridad](./security-process.md), describe cómo se tratan las vulnerabilidades (el objetivo para acusar recibo y clasificar es ahora de 24 horas) y qué publica cada versión.

#### Otros cambios en esta versión {#v130-other-changes}

No hay que hacer nada.

- **LwM2M: información del dispositivo de los dispositivos que responden en TLV.** `lwm2m.readDeviceInfo` y la recogida de información del dispositivo al registrarse solo leían respuestas en texto plano, así que de un cliente que responde en LwM2M TLV a una lectura de un solo recurso, como hace Eclipse Leshan, no se registraba nada. Las respuestas TLV se decodifican ahora.
- **La [matriz de transportes](../reference/transport-matrix.md) se genera a partir de una ejecución de conformidad** en la que clientes de terceros (Eclipse Paho, curl, Eclipse Leshan y Eclipse Tahu) manejaron una instancia real, y cada celda se juzga por lo que almacenó la plataforma.
- **`dcctl install` ya no exige el comando `helm`.** El chart se instala con la biblioteca de Helm dentro de `dcctl`; `tofu` (o `terraform`) sigue siendo necesario.
- **`dcctl bootstrap --values` ya no falla en una instancia nueva** con "changed while the values overlay was being recorded". Una escritura de estado del operador movía la versión de la instancia mientras tanto; `dcctl` ahora reintenta cuando solo se movió el estado, y sigue rechazando cuando cambió cualquier otra cosa. Lo mismo vale para el registro del front end y del interruptor de sobres de eventos.

#### Cambios de esquema y vuelta atrás {#v130-going-back}

Se añaden las siguientes migraciones. Cada columna nueva admite nulos y cada tabla nueva empieza vacía, así que un servicio de `v1.2.0` que se ejecute contra el esquema nuevo escribe filas que todavía puede almacenar.

- **OCSP en las anclas de confianza** (`device-management`, migración `20261113100000`). Dos columnas que admiten nulos en `device_trust_anchors`: `ocsp_mode` y `ocsp_responder_url`. Un modo vacío se lee como `OFF`.
- **Canal de comandos HTTP** (`device-management`, migración `20261114120000`). Una columna que admite nulos, `command_channel` en `device_types`. Vacía se lee como `PUBLISH`.
- **Constancia de entrega** (`command-delivery`, migración `20261114120100`). Cinco columnas que admiten nulos en `commands`: `delivered_time`, `delivery_evidence`, `delivery_mode`, `max_attempts` y `delivery_attempts`.
- **Identidad en el diario de auditoría** (todas las áreas). La tabla del diario de auditoría `audit_events`, que cada área guarda junto a sus propias tablas, gana cuatro columnas que admiten nulos: `client_id`, `session_id`, `on_behalf_of` y `proposal`. Se añaden al arrancar, como siempre se ha creado la propia tabla, y no mediante una migración numerada.
- **Uso de eventos** (`event-management`, migraciones `20261107000000` y `20261108000000`). Cinco tablas nuevas, `tenant_usage_daily`, `tenant_usage_hourly`, `tenant_usage_devices`, `tenant_usage_ledger` y `tenant_usage_state`, y un índice BRIN sobre `events.processed_time`, construido fragmento a fragmento.
- **Registro de entregas de notificaciones** (`notification-management`, migración `20261108000100`). Una tabla nueva, `notification_attempts`, con tres índices.
- **Posición instalada** (`device-management`, migración `20261112120000`). Seis columnas que admiten nulos en `devices` y en `assets` (`installed_lat`, `installed_lon`, `installed_elev`, `installed_accuracy`, `installed_source` e `installed_updated_at`), una en `areas` (`boundary_fence_token`) y un índice parcial sobre ella.
- **Disposiciones de alarmas** (`device-management`, migración `20261114090000`). Una tabla nueva, `alarm_dispositions`, cuyas filas se eliminan con su alarma.
- **Campañas** (`command-delivery`, migración `20261116120000`). Tres tablas nuevas, `campaigns`, `campaign_cycles` y `campaign_elements`, con sus índices.
- **Vínculos semánticos** (`device-management`, migración `20261120120000`). Columnas que admiten nulos para los vínculos en las tablas de tipos, tipos de relación y métricas, y una tabla nueva, `semantic_contexts`.

**No hay que deshacer nada en ninguna base de datos para volver a `v1.2.0`:** las tablas, columnas e índices nuevos se quedan y los servicios de `v1.2.0` los ignoran. Antes de volver atrás, devuelve el modo OCSP de cada ancla de confianza a `OFF` y el `commandChannel` de cada tipo de dispositivo a `PUBLISH`, y desactiva `httpIngest.deviceUplinks`, `lpwanDownlink.enabled` y la firma de webhooks, porque `v1.2.0` no los conoce. Avanza cuando puedas: una actualización fallida se arregla volviendo a ejecutar `dcctl upgrade`.

#### Problemas conocidos {#v130-known-issues}

- **La confirmación MQTT en modo de fallo cerrado está desactivada por defecto,** como en `v1.2.0`. La página del [transporte MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker) indica los límites medidos con ella desactivada y activada.
- **Un cambio en la configuración del broker sigue desconectando a los dispositivos que admitió el callout de autenticación,** hasta que la biblioteca del broker incluya la corrección de origen. Una renovación de certificado no lo hace.
- **`eventSources.ingestMetering: lagCredit` sigue sin estar disponible,** y se rechaza al arrancar; consulta [Medición de la ingesta](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-ingest-metering). Llega, con activación opcional, en [`v1.4.0`](#v140-ingest-metering).
- **OCSP todavía no almacena lo que averigua ni termina las sesiones abiertas,** como se describe en [OCSP para conexiones con certificado](#v130-ocsp).
- **Un evento almacenado más de 10 minutos después del final de la hora en que se recibió no se cuenta en el registro de uso del inquilino,** y tampoco un evento recibido después de la actualización pero fechado dentro de un fragmento que ya estaba comprimido. Consulta [Uso de eventos por inquilino](#v130-usage-metering).

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
| <a id="v120-upgrade"></a>v1.2.0 | decodificadores de carga útil que escribe cada inquilino, y una revocación que cierra sesiones | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v1.2.0/docs/docs/deployment/releases-and-upgrades.md#v120-upgrade) |
| <a id="v110-upgrade"></a>v1.1.0 | la ingesta cuesta menos por evento, y los dispositivos fuera del clúster se conectan por MQTT | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v1.1.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v110-upgrade) |
| <a id="v100-upgrade"></a>v1.0.0 | la primera versión estable: una caída de la base de datos retiene eventos, y Traefik es el acceso por defecto | Sí | [Notas completas](https://github.com/devicechain-io/devicechain/blob/v1.0.0/docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/releases-and-upgrades.md#v100-upgrade) |
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
