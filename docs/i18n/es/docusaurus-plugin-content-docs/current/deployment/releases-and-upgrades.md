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

## Líneas de versiones {#release-lines}

Cada versión menor inicia una línea de versiones, y sus versiones de parche pertenecen a esa línea. Las tres líneas más recientes tienen sus notas completas más abajo, de la más nueva a la más antigua. Las líneas anteriores tienen una entrada breve en [Versiones anteriores](#older-releases).

- **[v1.8](#v180-upgrade)**: [v1.8.0](#v180-upgrade)
- **[v1.7](#v170-upgrade)**: [v1.7.1](#v171-upgrade) · [v1.7.0](#v170-upgrade)
- **[v1.6](#v160-upgrade)**: [v1.6.0](#v160-upgrade)

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

Las tres líneas de versiones más recientes se documentan completas, de la más nueva a la más antigua. Dentro de una línea, las notas de una versión de parche van antes que las de la versión que corrige.

### v1.8.0 — en desarrollo {#v180-upgrade}

Estas notas son de la versión en desarrollo y crecen hasta su publicación.

#### Antes de actualizar a v1.8.0 {#v180-before-upgrade}

- **Ya están disponibles las ventanas de supresión de alarmas.** Declare una ventana única o recurrente para dispositivos, tipos de dispositivo, grupos, áreas, activos, clientes o todo el inquilino. Las alarmas se siguen generando, registrando y mostrando, marcadas como suprimidas; los avisos se retienen hasta que termina la ventana y se envían si la alarma sigue activa y sin reconocer. Las ventanas limitadas a dispositivos necesitan `alarm:write`; las más amplias también necesitan `notification:write`. Finalizar una ventana solo requiere `alarm:write`. Durante la actualización, las alarmas bajo una ventana aún pueden enviar avisos; confíe en la supresión cuando device-management y notification-management hayan terminado de desplegarse.
- **Las notificaciones de alarma se encolan en la base de datos de `device-management` antes de llegar al broker.** Una versión anterior no envía lo que queda en esa cola. Si alguna vez tiene que volver a ejecutar una versión anterior de `device-management`, espere primero a que el indicador `alarm_events_pending` marque 0 en todas las réplicas; lo que siga en la cola cuando la versión anterior tome el relevo no se envía.
- **Las instalaciones locales rechazan motores Docker insuficientes.** Una ejecución no compacta de `dcctl install local` que crea o reutiliza un clúster kind local ahora se detiene si Docker indica menos de 4 CPU o menos de 7,5 GiB de memoria. Entre 7,5 GiB y 8 GiB solo muestra una advertencia, de modo que un Docker Desktop configurado con 8 GB, que informa algo menos, no se rechaza. Quienes antes continuaban tras la advertencia con un motor más pequeño deben ampliar los recursos de Docker o usar `--compact` (o `--size compact`) para el preajuste de menor consumo. Las instalaciones compactas, `dcctl preflight` y `dcctl bootstrap` mantienen las advertencias.
- **Permiso de ubicación para el historial de notificaciones.** Los miembros con `notification:read` pero sin `location:read` dejan de ver los estados de notificación y los intentos de entrega de alarmas derivadas de geocercas o cuya clasificación de ubicación se desconoce. Las filas anteriores a la actualización y los eventos de réplicas antiguas de device-management carecen de clasificación y se ocultan hasta que la alarma vuelva a cambiar de estado con un productor nuevo. Estas consultas verifican las clasificaciones almacenadas en caché con device-management. Fallan con un error si device-management no está disponible o si no se ha configurado su endpoint o el secreto de servicio. El historial de alarmas que device-management ya no conserva se oculta a estos miembros. Conceda `location:read` a quienes necesiten este historial. Las notificaciones enviadas a canales elegidos por el administrador no cambian.
- **Las denegaciones por política se cuentan como `unwanted`, no como `dropped`.** Los bucles de procesamiento `resolve`, `device_reports` y `update_reports` ya no cuentan como `result="dropped"` el tráfico de un dispositivo deshabilitado ni los fallos de decodificación suprimidos tras agotar su presupuesto de registros. Ese tráfico se cuenta ahora con el nuevo `result="unwanted"`, y los paneles de fallos del panel de resumen lo excluyen. `dropped` significa ahora siempre que se renunció a un mensaje. No se renombra ninguna métrica. Durante una actualización progresiva, los pods que aún ejecutan la versión anterior cuentan este tráfico como `dropped` hasta que se reemplazan.

#### Novedades de v1.8.0 {#v180-whats-new}

- La consola permite iniciar y gestionar ventanas de mantenimiento de alarmas: una acción Iniciar mantenimiento en las páginas de dispositivos, áreas, grupos y activos, una etiqueta Mantenimiento en las alarmas y una página Mantenimiento de alarmas en Configuración.
- El servidor MCP muestra cuándo una alarma está bajo una ventana de mantenimiento, y la nueva herramienta `list_suppression_windows` permite listar las ventanas.
- **Las notificaciones de alarma sobreviven a una caída del broker o a un reinicio de `device-management`.** Un cambio de estado de una alarma (generada por la detección, escalada, borrada, o reconocida o borrada por un operador) se registra ahora junto con el propio cambio y se envía al broker desde ese registro hasta que el broker lo acepta. Antes, un cambio hecho mientras el broker era inaccesible, o justo antes de que `device-management` se reiniciara, podía guardarse sin que se enviara ninguna notificación. Mientras el broker es inaccesible, reconocer o borrar alarmas responde ahora en unos dos segundos, incluso en un reconocimiento masivo, donde cada alarma retenía antes la llamada unos 30 segundos. La notificación llega cuando el broker vuelve. La supresión por ventana de mantenimiento se decide cuando se hace el cambio, no cuando por fin se envía la notificación. Tres indicadores nuevos informan del retraso: `alarm_events_pending`, `alarm_event_oldest_age_seconds` y `alarm_events_quarantined`. Un evento en cuarentena no se pudo decodificar ni codificar; se conserva para su inspección y aparece entre las cartas muertas en lugar de reintentarse. Dos alertas nuevas los vigilan: `AlarmEventQuarantined` (crítica) y `AlarmEventRelayStalled` (un evento que espera más de dos minutos, durante cinco minutos). `alarm_event_dead_lettered_total` sigue registrado pero ya no aumenta.
- **Los ajustes del sistema conservan lo que acaba de guardar.** En **Ajustes del sistema**, un ajuste guardado o restablecido mientras la lista aún se recargaba mostraba por un momento su valor anterior, y una edición hecha en ese instante partía de él, de modo que el siguiente guardado deshacía el primero sin avisar. La página muestra ahora el valor que almacenó el servidor en cuanto responde el guardado.
- **Ajustes del sistema en chino simplificado.** Con la consola en 简体中文, la pantalla **Ajustes del sistema** está ahora traducida, incluidos sus editores de máscaras de token, idioma, mapa y marca. Su traducción ya existía pero nunca se cargaba, por lo que la pantalla se mostraba en inglés.
- **Las eliminaciones de dispositivos y los cambios de autoridad responden antes mientras `device-management` no puede llegar al broker.** Eliminar o deshabilitar un dispositivo, o cambiar una credencial, mientras `device-management` estaba aislado del broker retenía la llamada alrededor de un minuto, de modo que un cliente detrás del ingress recibía un tiempo de espera de la pasarela por un cambio que ya se había guardado. Una vez que `device-management` ha detectado la pérdida de la conexión, lo que tarda hasta unos 40 segundos, la llamada responde ahora en pocos segundos. Una llamada hecha antes responde en cuanto se detecta la pérdida. Una eliminación responde `true` en cuanto se guarda, y la eliminación se anuncia cuando el broker vuelve a estar accesible. Si no se puede confirmar que las sesiones MQTT abiertas del dispositivo terminaron, la llamada devuelve el error existente ("the change was saved, but the device's live broker sessions could not be confirmed ended ... repeat the operation"). Repetir la operación es seguro, y la plataforma también reintenta en segundo plano. Cada actualización de caché que debe un cambio guardado retiene ahora la llamada como máximo dos segundos y después se completa en segundo plano.
- **Cambiar el perfil de un tipo de dispositivo, o crear dispositivos en lote, ya no se bloquea mientras `device-management` no puede llegar al broker.** Estos cambios anuncian cada dispositivo afectado al motor de detección, un mensaje por dispositivo. Con el broker inaccesible, cada mensaje esperaba unos cinco segundos antes de fallar, así que un tipo con mil dispositivos retenía la llamada más de una hora. Una vez que `device-management` ha detectado la pérdida de la conexión, el cambio deja de anunciar dispositivos en el primer mensaje rechazado y responde. El cambio se guarda en cualquier caso, y el motor de detección incorpora los dispositivos que no se le anunciaron en su siguiente comparación con `device-management`, que se ejecuta cada cinco minutos.
- **Los eventos de entidades eliminadas permanecen desvinculados.** Los eventos recibidos antes de eliminar un cliente, área, activo, grupo o dispositivo ya no reaparecen bajo una entidad recreada con el mismo token cuando se almacenan después de procesar la eliminación.
- **Los mensajes capturados esperan a que vuelva la ubicación de particiones.** Con los grupos de particiones activados, un mensaje capturado cuyo último intento de entrega encontró la ubicación de particiones no disponible se retiene y se reenvía cuando esta vuelve, en lugar de registrarse como mensaje fallido sin su contenido. Esto se aplica tanto al stream de captura como al almacén de mensajes MQTT. La nueva métrica `placement_held_messages` indica cuántas entregas finales están retenidas. La retención tiene un límite por origen y dura durante la ejecución actual; al detenerse, los mensajes retenidos quedan sin confirmar y las entregas agotadas aún pueden registrarse. Las actualizaciones graduales no requieren cambios de protocolo ni de configuración; los pods antiguos conservan el comportamiento anterior.
- **Las suscripciones en vivo siguen los cambios de ubicación.** Con los grupos de particiones habilitados, una suscripción en vivo abierta después de un cambio de ubicación de un inquilino sigue su nuevo grupo.
- **`resolved-events` se puede particionar en una instalación nueva.** Con `resolved-events` en `infrastructure.nats.shardGroups.suffixes`, event-processing ejecuta ahora un trabajador de detección por grupo de particiones, cada uno con su propio punto de control, y device-state lee todos los grupos. Solo se admite cuando la instancia se instala por primera vez: en una instancia existente con event-processing desplegado, `dcctl upgrade` se niega a empezar a particionar `resolved-events` o a cambiar `count` mientras está particionado, y event-processing se niega a arrancar en una instancia que ya tiene historial de detección, con un número de grupos o un sufijo cambiado, o con el particionado desactivado después de haberlo activado. Si actualiza directamente con Helm, nada lo comprueba antes de la actualización: no añada `resolved-events` a la configuración de una instancia existente. Mantenga fija la ubicación de los inquilinos una vez particionado `resolved-events`. Dentro de cada grupo, las marcas de agua de detección y la finalización de temporizadores avanzan con el tráfico propio de ese grupo. El particionado sigue desactivado salvo que lo configure.
- **Dos advertencias genéricas cubren todas las tareas de mantenimiento y todos los bucles de procesamiento.** `PeriodicTaskStalled` se dispara cuando una tarea de mantenimiento no ha tenido éxito en ninguna réplica en tres veces su intervalo de pasada declarado más cinco minutos, o nunca ha tenido éxito en una réplica que declara un intervalo. `ProcessorLoopDropping` se dispara cuando un bucle de procesamiento renunció a un mensaje en los últimos 15 minutos; las denegaciones por política contadas como `unwanted` no la activan, y el bucle de estado de device-state sigue cubierto por `DeviceStateDroppingEvents`. Ambas son advertencias, y un bucle nuevo queda cubierto sin una regla propia. Durante una actualización progresiva, los pods que aún ejecutan la versión anterior cuentan el tráfico de un dispositivo deshabilitado como `dropped`, así que `ProcessorLoopDropping` puede dispararse hasta unos 15 minutos en una instancia donde un dispositivo deshabilitado sigue transmitiendo.
- **`PeriodicTaskStalled` ya no se dispara con una tarea sana que completa pasadas con menos frecuencia de la que sondea.** El barrido de la cola de agregados se activa cada cinco minutos pero deliberadamente termina un barrido solo aproximadamente una vez por hora, por lo que la alerta tomaba una tarea sana por detenida unos treinta minutos después de cada éxito. Una tarea de ese tipo ahora declara la cadencia que espera entre éxitos como `<task>_expected_success_interval_seconds`, y la alerta mide contra el mayor entre ese valor y el intervalo de sondeo. El nombre de la alerta, su severidad y la espera de diez minutos no cambian, y una tarea que no declara ese intervalo se evalúa exactamente igual que antes.
- **`dcctl broker facts` enumera los hechos de eliminación que anunció la plataforma.** Cuando se elimina una entidad, `device-management` anuncia la eliminación en el broker para que cada servicio que guarda una referencia a ella pueda quitarla. `dcctl broker facts <instance>` lee esos anuncios del broker como el administrador del broker. Muestra cuándo se almacenó cada uno, el tenant, el tipo, el id y el token de la entidad, la hora de eliminación y el id del mensaje, y admite los filtros `--tenant`, `--token` y `--since`. Responde a "¿se anunció la eliminación de X, y cuándo?" sin acceso a los discos del broker. El inicio de sesión del administrador del broker obtiene acceso de lectura a los anuncios de eliminación y nada más: el nuevo acceso no añade ningún derecho a eliminar, purgar ni publicar mensajes. Una instancia obtiene el acceso cuando se actualiza con el `dcctl` de esta versión.
- **Clientes MCP en el navegador.** El endpoint MCP ahora responde a las solicitudes preliminares CORS y expone las cabeceras de sesión y de desafío de autenticación, lo que permite conectar clientes de navegador mediante tokens bearer. No se requiere ninguna acción del operador.
- **El escenario portuario entrega fuera de la plataforma y decodifica LoRaWAN.** Una simulación `harborpulse` ahora también llama a un webhook de emergencia firmado cuando un medidor supera el nivel crítico de inundación, publica un boletín de inundación mediante un conector MQTT, envía un correo al oficial de guardia por cada alarma mayor o crítica y vuelve a enviarlo si nadie la reconoce, genera una alarma mientras su camión de respuesta está dentro de la geovalla de la llanura inundable, calcula el llenado del pozo de cada bomba a partir de su nivel y de la profundidad del pozo, y simula un servidor de red LoRaWAN para cuatro medidores de marea cuyos mensajes llegan mediante una integración ChirpStack y se decodifican con decodificadores de carga útil del inquilino, uno de ellos roto a propósito. `dcctl sim create --manifest harborpulse` asigna al inquilino un nivel propio, `hp-showcase`, que permite la métrica calculada. Las entregas van a un receptor dentro del clúster, el nuevo `scenario-sink`, en una dirección fija del clúster: para entregar, la instancia necesita el área `outbound-connectors` y esa dirección admitida en su permiso de salida. `scenario-verify` evalúa nueve invariantes más, y cinco de ellas leen el registro del receptor cuando se le pasa `--sink`. Vuelva a crear una simulación `harborpulse` existente con el dcctl de esta versión, porque su registro no tiene los nuevos endpoints.
- **El escenario portuario gestiona sus dispositivos como una flota.** Una simulación `harborpulse` ahora etiqueta sus farolas y bombas con atributos compartidos y publica cuatro grupos dinámicos de dispositivos sobre ellos, limita un aviso temprano del pozo húmedo a las bombas críticas, sigue al sol con dos campañas SOLAR planificadas por farola a partir de la posición de instalación de cada una (la del anochecer declara festivo el día siguiente), crea cada día simulado una campaña CALENDAR que atenúa las farolas de un muelle y otra que envía a una bomba un lavado disruptivo, retiene ese lavado con la ventana de mantenimiento de los controladores de bombas hasta que se abre el turno de la cuadrilla, y modela su distrito como una jerarquía de activos con los contadores de los clientes bajo sus edificios y una relación tipada `hp-feeds` desde cada alimentador hasta los dispositivos que alimenta. Las campañas y las ventanas de mantenimiento funcionan con el reloj real, así que la simulación escribe las tareas CALENDAR y el turno de la ventana de cada día a medida que avanza el día comprimido, y las campañas solares se evalúan por lo que la plataforma planificó para cada farola. `scenario-verify` evalúa ocho invariantes más, y el simulador acepta ocho valores más de `--scenario-defect`, uno para poner en rojo cada una de ellas. Las coordenadas del escenario ahora están en mar abierto, porque la ciudad ficticia no debe coincidir con una real.
- **Harborpulse incorpora actualizaciones firmadas, recuperación regulada y sustitución física.** Sus seis cargadores reciben firmware sintético 2.4.1 a las 02:00 de la noche siguiente, con dos actualizaciones simultáneas y un fallo de verificación deliberado. La recuperación de East Quay a las 21:15 reintenta los rechazos por límite de ingesta y conserva las lecturas originales. El día 0 a las 12:30, un medidor recibe una unidad física y una credencial nuevas sin cambiar su identidad lógica. `scenario-verify` comprueba ocho invariantes nuevas mediante consultas a la plataforma. `dcctl sim create --manifest harborpulse` usa ahora `hp-governed` por defecto (40 lecturas por segundo, ráfaga de 256 y una métrica calculada por perfil); las simulaciones creadas manualmente necesitan el mismo nivel para obtener un resultado limpio. El intercambio inicial añade el campo opcional `endpoints.updateMgmtGraphQL`; los registros anteriores lo derivan de `/api/device-management/graphql`; las rutas personalizadas deben indicarlo explícitamente. Cada cargador registra unas 90 entradas durante el corte, independientemente de los envíos y de la duración del día.
- **Harborpulse comprueba umbrales compartidos, comandos LoRaWAN y el relleno histórico de métricas.** Las alarmas de las bombas siguen atributos compartidos por dispositivo y un cambio de umbral durante el día. Un comando tipado llega a la cola ChirpStack del receptor mediante el adaptador LPWAN, devuelve confirmaciones de clase A y cambia la cadencia de las lecturas almacenadas. Un trabajo rellena las métricas calculadas de un intervalo cuya constante de profundidad faltaba inicialmente. Tres invariantes nuevos tienen defectos deliberados y controles sin conexión. El rig habilita el adaptador y el trabajador de relleno histórico; la salida de la plataforma sigue limitada a la dirección del receptor.
- El escenario harborpulse demuestra una ventana de mantenimiento de alarmas: el aviso por parpadeo de una lámpara de East Quay se retiene durante la sustitución de su controlador y se envía al terminar la ventana, mientras que el mismo parpadeo en una lámpara de North Quay avisa de inmediato.
- **La llanura inundable del escenario del puerto está donde dice su nombre.** En un simulador `harborpulse`, la geocerca "East Quay flood plain" y el depósito de obras se sitúan ahora detrás de East Quay, tierra adentro de sus farolas, de modo que el mapa de la consola los muestra donde el escenario los describe. El camión sigue entrando en la llanura inundable a las 17:45 y saliendo a las 21:45. Un simulador existente mueve ambas geocercas la próxima vez que aprovisiona. El archivo de disposición de la escena 3D incluye ahora también las horas previstas de los momentos del escenario, la ruta del camión y un marco envolvente, y el `/status` del simulador informa `scenario.layoutDigest`, el SHA-256 de la disposición que genera.
- **Una escena 3D para el escenario del puerto (primer hito).** `demos/harborpulse-unity` es un proyecto de Unity 6 (URP) que dibuja el distrito portuario del simulador `harborpulse`, dispuesto a partir de las coordenadas del propio escenario, bajo un cielo cuyo sol, luna, estrellas y tormenta siguen el día comprimido de la simulación en su `/status`. El agua del puerto es un shader propio con olas de Gerstner, espuma y lluvia, y su altura en cada muelle es la mediana de los mareógrafos de ese muelle tal como los informa la plataforma. Las farolas muestran lo último que la plataforma supo de cada una: encendida, apagada o en silencio. La escena lee la plataforma a través del SDK de C# de DeviceChain y nunca escribe en ella. Una vista previa sin conexión, claramente etiquetada, funciona sin instancia.
- **La escena 3D del puerto, segundo hito.** `demos/harborpulse-unity` construye ahora en código los muelles, las estaciones de bombeo (con un pozo húmedo en corte), los mareógrafos, los cargadores de vehículos eléctricos, las farolas, los edificios y el camión de servicio, y añade gradación de color y un reflejo planar en el agua del puerto. Muestra las operaciones del escenario tal como las registra la plataforma: alarmas de pozo húmedo que escalan en el mismo sitio, los comandos de arranque de la bomba con las marcas de tiempo de la propia plataforma, el camión en su última posición informada, el corte de alimentación y sus dispositivos en silencio, el mareógrafo sustituido tras el mismo dispositivo, la ventana de supresión del mantenimiento de la farola y el despliegue de firmware firmado en los cargadores con el resultado de cada uno. El tiempo, las cuadrillas y el propio corte se dibujan como el mundo del escenario y nunca como alarmas de la plataforma. La escena sigue limitándose a leer. El `/status` del simulador también enumera ahora los momentos de la historia con sus horas.
- **Creación simultánea de simuladores.** `dcctl sim create` ahora reserva los puertos de control automáticos entre procesos, evitando que las creaciones simultáneas seleccionen el mismo puerto. Las creaciones fallidas liberan sus reservas; las reservas abandonadas sin un simulador guardado caducan tras 24 horas. Los valores explícitos de `--control-addr` no cambian.
- **Configura la duración del día de harborpulse desde dcctl.** Ejecuta `dcctl sim run <name> --day-length 48m` para ampliar un día comprimido de simulación a 48 minutos. El valor predeterminado `0` conserva el día de 24 minutos del escenario; se rechazan los valores negativos y el simulador valida la duración mínima del día.

### v1.7.1 — las actualizaciones y los comandos de campaña llegan solo a los dispositivos a los que iban dirigidos {#v171-upgrade}

`v1.7.1` es una versión de parche y una actualización en el lugar desde `v1.7.0`: `dcctl install` para el clúster y luego un `dcctl upgrade` por cada instancia, como se describe en [Actualizaciones continuas](#rolling-upgrades), con el `dcctl` de v1.7.1 y `--version v1.7.1`. `update-management` añade una migración en su primer arranque. Añade una columna que puede quedar vacía, de modo que los pods de `v1.7.0` siguen funcionando mientras avanza la actualización. Como en toda versión, no hay vuelta atrás a la versión anterior.

```bash
dcctl install local --version v1.7.1
dcctl upgrade local devicechain --version v1.7.1 --dry-run
dcctl upgrade local devicechain --version v1.7.1
```

Esta versión no cambia ninguna interfaz cubierta. Añade un código de rechazo y de omisión, `DEVICE_REPLACED`, al vocabulario de códigos existente.

#### Antes de actualizar a v1.7.1 {#v171-before-upgrade}

- Mientras los pods antiguos y los nuevos coexisten durante la actualización continua, un pod de `v1.7.0` todavía puede avanzar un despliegue en curso a la manera antigua hasta que se sustituya. Termine la actualización antes de eliminar y volver a crear dispositivos a los que apunta un despliegue en curso.
- Un despliegue iniciado antes de la actualización registró solo los tokens de sus dispositivos. Para esos despliegues, `update-management` compara cuándo se creó cada dispositivo con cuándo empezó el despliegue (consulte [Los despliegues apuntan a los dispositivos con los que empezaron](#v171-rollout-identity)). Un dispositivo creado en los cinco minutos anteriores o posteriores al inicio del despliegue queda **retenido**: no se le indica que se actualice y el despliegue sigue en `RUNNING`. Si un despliegue sigue en curso con dispositivos en espera, abórtelo e inicie uno nuevo.

#### Novedades de v1.7.1 {#v171-whats-new}

- **La actualización asignada a un dispositivo recreado se mantiene.** Una solicitud de asignación que consultó un dispositivo antes de que este se eliminara y se volviera a crear con el mismo token ahora se rechaza con `DEVICE_REPLACED`. Ya no sustituye la actualización del nuevo dispositivo. Antes, al nuevo dispositivo se le dejaba de ofrecer su actualización hasta que se le asignaba de nuevo.
- **Los comandos de campaña vuelven a comprobar el grupo cuando se reintentan.** Cuando la creación del lote de una campaña falla por un motivo pasajero, los comandos se reintentan más tarde. Ahora se comprueban primero contra el grupo de la campaña: un dispositivo que salió del grupo entretanto se registra como `NOT_IN_GROUP` y no recibe ningún comando. Si no se puede leer el grupo, los comandos esperan; si siguen esperando después del margen de retraso de la campaña, se registran como no enviados a tiempo.
- **Las copias de seguridad de instancias particionadas incluyen todos los streams de grupo.** En una instancia con `infrastructure.nats.shardGroups` configurado, `dcctl backup` exporta ahora cada stream de grupo de un stream particionado (`<stream>-g1`, `<stream>-g2`, etc.) junto con su stream base y registra la distribución en el manifiesto. Un stream de grupo configurado que falta o no se puede leer, o un stream de grupo que la configuración desplegada no declara, hace fallar la exportación en lugar de producir una copia parcial. `dcctl restore --plan` enumera cada stream de grupo y rechaza un destino cuya distribución de grupos difiere de la de la copia. Las copias de una instancia particionada tomadas con un dcctl anterior contienen solo los streams base (grupo cero), no los streams de grupo; tome una copia nueva tras actualizar dcctl. Las instancias sin grupos de particiones no se ven afectadas y sus manifiestos no cambian.

#### Los despliegues apuntan a los dispositivos con los que empezaron {#v171-rollout-identity}

- `startUpdateRollout` ahora registra qué dispositivo es cada miembro del grupo, no solo su token. Un dispositivo creado más tarde con el mismo token es un dispositivo distinto: se omite con `DEVICE_REPLACED`, aunque esté en el grupo, y nunca se le indica que se actualice. Un miembro que sale del grupo mientras se inicia el despliegue no forma parte de él.
- Cuando se elimina un dispositivo, un destino del despliegue que todavía espera a ese dispositivo se omite con `DEVICE_NOT_FOUND`.
- Despliegues iniciados antes de v1.7.1: un dispositivo creado más de cinco minutos después del inicio del despliegue se omite con `DEVICE_REPLACED`, uno creado más de cinco minutos antes se actualiza como de costumbre y uno creado entre ambos queda retenido, como se describe en [Antes de actualizar a v1.7.1](#v171-before-upgrade).

### v1.7.0 — las eliminaciones llegan a todos los servicios y las sesiones resisten las interrupciones {#v170-upgrade}

`v1.7.0` es una actualización in situ desde `v1.6.0`: `dcctl install` para el clúster y después un `dcctl upgrade` para cada instancia, como se describe en [Actualizaciones graduales](#rolling-upgrades). Usa **el `dcctl` de v1.7.0 para ambos comandos** con OpenTofu o Terraform 1.10 o posterior, y proporciona a `dcctl install` los ajustes del clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. `device-management`, `ai-inference` y `update-management` añaden migraciones en su primer arranque, y la actualización solo avanza: no hay vuelta atrás a `v1.6.0`. Si vienes de `v1.5.x` o anterior, actualiza antes a través de cada versión menor intermedia, incluida `v1.6.x`, siguiendo las notas de cada versión con su propio `dcctl` y su `--version`: saltarse una versión menor no es una ruta probada (consulta [Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)). Para `v1.6.0`, son las [notas de `v1.6.0`](#v160-upgrade).

```bash
dcctl install local --version v1.7.0
dcctl upgrade local devicechain --version v1.7.0 --dry-run
dcctl upgrade local devicechain --version v1.7.0
```

Se aplica la [política de versiones y actualizaciones](./versioning-policy.md). Esta versión añade interfaces cubiertas y no elimina ni declara obsoleta ninguna. Que las alarmas de geocercas requieran `location:read` y los rechazos `MANAGED_BY_IDP` son correcciones de seguridad permitidas por la política; los demás rechazos y límites nuevos se indican en [Antes de actualizar a v1.7.0](#v170-before-upgrade). Los valores de Helm y la configuración de instancia quedan fuera de la compatibilidad cubierta.

Lo más destacado: eliminar un dispositivo, cliente, área, activo o grupo ahora se avisa de forma fiable, y cada servicio que guardaba algo suyo actúa en consecuencia: se cancelan los comandos pendientes, se eliminan el estado en vivo y las actualizaciones pendientes, las alarmas terminan y dejan de escalar, y se eliminan las comparticiones de paneles obsoletas. Una interrupción de red ya no cierra la sesión de nadie en la consola ni en `/dash`. Las instancias con grupos de fragmentación de NATS retienen la ingesta en lugar de descartarla como fallida. MCP, las suscripciones en vivo, las transferencias OTA y la inferencia de IA obtienen límites por inquilino, dispositivo o pod.

Novedades:

- [Antes de actualizar a v1.7.0](#v170-before-upgrade)
- [Novedades de v1.7.0](#v170-whats-new)
- [Las eliminaciones se avisan de forma fiable](#v170-deletion-announcements)
- [Se eliminan las comparticiones que nombran un cliente o grupo eliminado](#v170-deleted-anchor-shares)
- [Las alarmas de un dispositivo eliminado terminan y dejan de escalar](#v170-deleted-device-alarms)
- [Se cancelan los comandos pendientes de un dispositivo eliminado](#v170-deleted-device-commands)
- [Se elimina el estado en vivo de un dispositivo eliminado](#v170-deleted-device-state)
- [Se terminan las actualizaciones asignadas a un dispositivo eliminado](#v170-deleted-device-updates)
- [Se conservan los anclajes de eventos de una entidad recreada](#v170-deleted-entity-anchors)
- [Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido](#v170-shard-groups)

#### Antes de actualizar a v1.7.0 {#v170-before-upgrade}

Qué requiere acción, en el orden en que debe hacerse:

- **`dcctl install`, `dcctl bootstrap` y `dcctl preflight` comprueban la versión de OpenTofu o Terraform.** La comprobación previa ahora rechaza un binario anterior al `required_version` que declara la configuración de OpenTofu incluida en dcctl (actualmente `>= 1.10`), una versión preliminar o un binario cuya versión no puede leer. Antes solo comprobaba que el binario existiera, por lo que una instalación con OpenTofu 1.9 fallaba en `tofu init` cuando ya había actualizado el operador. Si ambos están instalados, se sigue prefiriendo OpenTofu, y es el binario que se comprueba. Instala OpenTofu o Terraform estable 1.10 o posterior antes de ejecutar el `dcctl install` de v1.7.0. `--skip-preflight` sigue omitiendo la comprobación.
- **Nota de actualización: el bróker se reinicia.** La actualización sustituye la imagen del bróker, así que cada pod del bróker se reinicia. Con `--mqtt-fail-closed` desactivado (el valor predeterminado), un mensaje QoS 1 que el bróker confirma mientras un stream cambia de líder puede perderse. Actívalo antes de actualizar (`dcctl upgrade --mqtt-fail-closed`) si los dispositivos no deben perder mensajes confirmados ([MQTT con cierre seguro](./observability.md#mqtt-fail-closed)).

**Instancias con grupos de fragmentación.**

- **Instancias con grupos de fragmentación: la ingesta se retiene en lugar de descartarse como fallida.** Nada de [Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido](#v170-shard-groups) cambia una instancia que no haya definido `infrastructure.nats.shardGroups`. En una que sí lo haya hecho, un grupo atrasado o una asignación inutilizable retienen ahora la ingesta y la reintentan en lugar de enviarla a mensajes fallidos, una solicitud HTTP o de integración rechazada responde `503` con `Retry-After`, y un servicio que no puede enrutar un flujo fragmentado lo rechaza. Lee esa subsección antes de actualizar.
- **Instancias con grupos de fragmentación: una asignación no debe nombrar un grupo igual o superior a `count`.** Con los [grupos de fragmentación de NATS](#v160-nats-shard-groups) configurados, asignar una vpartición a un grupo igual o superior a `nats.shardGroups.count` se rechaza ahora, y la réplica de la asignación de user-management se niega a publicar una tabla que nombre uno: cada pasada así falla y se cuenta en sus métricas de pasadas `placement_mirror`. Una tabla que ya nombra un grupo así no se respeta en parte: es una asignación inutilizable, de modo que la ingesta fragmentada se retiene para todos los inquilinos de la instancia hasta que la tabla encaje, como se describe en [Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido](#v170-shard-groups). Antes de actualizar, comprueba que ninguna vpartición esté asignada a un grupo igual o superior a `count`; si alguna lo está, aumenta primero `count` para cubrirlo. Las instancias sin `infrastructure.nats.shardGroups` no se ven afectadas.

**Corrige lo que ahora se rechaza.**

- **Se rechazan las máscaras de token que no generan nada.** Una máscara de token cuyos únicos marcadores tienen un ancho explícito de 0, como `dev{numeric-0}`, da a todas las entidades el mismo token, y ahora se rechaza guardar el ajuste de máscaras de token que la contenga. Una máscara así ya guardada conserva su valor, pero el siguiente guardado del ajuste falla hasta que se corrija, así que cámbiala antes de actualizar a un ancho positivo (`{numeric-4}`), un ancho omitido (`{alphanumeric}`), `{slug}` o `{uuid}`.
- **El tipo de valor de una faceta no puede cambiar mientras existan valores del tipo anterior.** Se rechaza cambiar el tipo de valor de una faceta mientras existan atributos SHARED activos del mismo tenant, familia de miembros y clave con valores no nulos del tipo anterior. Borre o migre esos valores antes de cambiar la declaración. Se pueden seguir editando la etiqueta y el vocabulario sin cambiar el tipo. No se necesita migración del esquema ni cambio de configuración. La comprobación se activa al reemplazar los pods de device-management, y los pods antiguos todavía aceptan estos cambios durante la actualización gradual, así que suspenda los cambios de tipo de faceta hasta que termine el despliegue.
- **Se rechaza marcar un tipo de relación como rastreado mientras existan relaciones de ese tipo creadas por dispositivos.** `updateEntityRelationshipType` rechaza marcar el tipo como rastreado mientras existan relaciones de ese tipo creadas por los propios dispositivos, porque pasarían a otorgar propiedad en ese momento; el rechazo las identifica. Elimínelas y después marque el tipo como rastreado.

**Revisa permisos y clientes antes del despliegue.**

- **Las alarmas de geocerca requieren `location:read`.** Una alarma generada por una regla que comprueba la pertenencia a una geocerca ahora solo la lee quien además tiene `location:read`, igual que `detectionStream` ya omite las detecciones de esa regla: las horas de activación y de borrado de la alarma son la entrada y la salida del dispositivo de la geocerca. Sin ese permiso, las consultas de alarmas, los recuentos de alarmas, `alarmDispositions` y la suscripción `alarmStream` omiten la alarma, y reconocerla, borrarla o etiquetarla responde como ante una alarma desconocida. Las demás alarmas no cambian. La actualización clasifica todas las alarmas existentes, juzgando una regla por si su definición, en el borrador o en cualquier versión publicada, menciona `inFence`. La clasificación recorre todas las alarmas en las migraciones del primer arranque de `device-management`, y una segunda sentencia vuelve a comprobar cada alarma que la primera no consideró de geocerca, por lo que con una tabla de alarmas grande ese arranque puede tardar notablemente más. Una alarma cuya regla ya no se encuentra, por ejemplo porque se eliminó su perfil de dispositivo, se trata como alarma de geocerca y también se omite. Durante la actualización progresiva, una alarma generada o actualizada por una réplica de la versión anterior puede omitirse hasta un minuto, hasta que se clasifica; eso incluye una alarma ya clasificada como no de geocerca, que se omite desde el momento en que esa réplica la actualiza, de modo que nunca se muestra una regla de geocerca que esa réplica le añada. Mientras conviven pods antiguos y nuevos durante la actualización progresiva, un pod `v1.6` sigue mostrando las alarmas de geocerca a todos los llamantes hasta que se sustituye. El conjunto básico de solo lectura y los tokens emitidos mediante el ámbito OAuth de solo lectura no incluyen `location:read`, así que un rol o token que supervise o reconozca alarmas de geocerca necesita que se le añada. Las notificaciones enviadas a los canales configurados no se ven afectadas.
- **Las ediciones del operador a una membresía gestionada por un IdP se rechazan con `MANAGED_BY_IDP`.** Las mutaciones del operador que deshabilitan, eliminan o cambian los roles de una membresía ahora devuelven `MANAGED_BY_IDP` mientras su conexión exista y siga atendiendo al tenant; de lo contrario, el siguiente inicio de sesión SSO desharía esos cambios. Para revocar el acceso, deshabilita la identidad, retira a la persona de los grupos del proveedor o deja de atender al tenant con esa conexión. Las membresías no gestionadas y aquellas cuya conexión ya no existe o ya no atiende al tenant siguen siendo editables.
- **Habilitar una membresía gestionada por un IdP también se rechaza con `MANAGED_BY_IDP`.** La mutación del operador que habilita una membresía ahora devuelve `MANAGED_BY_IDP` mientras la conexión de la membresía exista y siga atendiendo al tenant, como ya ocurre al deshabilitarla, eliminarla o cambiar sus roles. Cada inicio de sesión SSO fija el estado habilitado de esa membresía a partir de los grupos de la persona, así que una habilitación hecha por un operador contradiría al proveedor hasta que el siguiente inicio de sesión la deshabilitara de nuevo. Para dar acceso a la persona, añádela a un grupo que el proveedor asigne a un rol en el tenant; la membresía se habilita en su siguiente inicio de sesión. Habilitar una membresía no gestionada no cambia, y una membresía gestionada cuya conexión ya no existe o ya no atiende al tenant sigue sin poder volver a habilitarse: elimínala y añade una membresía en su lugar.
- **Los ámbitos de acceso de grupos dinámicos pueden ampliarse en el primer arranque.** En su primer arranque tras la actualización, `device-management` incorpora los dispositivos, activos, áreas y clientes existentes que ya deberían haber sido miembros de un grupo dinámico usado en una regla de detección o como ámbito de acceso, cuando el selector del grupo coincide con una entidad sin atributos (por ejemplo, una condición negada como `!("retired" in attr)`). Los miembros de ese ámbito de acceso pueden ver entonces dispositivos que antes no podían ver, y las reglas limitadas al grupo empiezan a evaluarlos. Antes de actualizar, revise los grupos dinámicos con esos selectores que sirven como ámbitos de acceso.
- **Los límites de SSO, MFA y autenticación reforzada cuentan un /64 IPv6 como un solo origen.** Las solicitudes de inicio de SSO, verificación MFA y autenticación reforzada desde direcciones IPv6 del mismo /64 ahora comparten un límite de frecuencia por origen, para evitar que la rotación de direcciones dentro de ese bloque agote la cuota global compartida. Las personas que inician sesión desde un mismo /64 IPv6, como una oficina o una red de operador, comparten ahora ese límite y pueden ser rechazadas antes. Los límites IPv4, los tamaños de ráfaga y los intervalos de reposición no cambian.

**Clientes que se encuentran con límites nuevos.**

- **Un comando caducado ya no se envía.** Antes, un comando cuya caducidad ya había pasado, pero que el barrido de caducidad aún no había cerrado, todavía podía reclamarse y publicarse a su dispositivo: al volver a habilitar un dispositivo que había estado deshabilitado más tiempo que la vida de sus comandos, mediante un aviso (nudge) o en un barrido de envío cuyo paso de caducidad había fallado. La reclamación de envío rechaza ahora ese comando y lo deja para el barrido de caducidad, que lo registra como caducado. Un comando que está exactamente en su hora de caducidad, o que no tiene caducidad, se sigue enviando. `markCommandSent` responde null para un comando caducado, igual que para cualquier reclamación que no gana.
- **Sondeo de comandos HTTP por dispositivo.** Cada dispositivo puede mantener como máximo dos sondeos de comandos en espera por réplica (uno, más una reconexión cuya conexión anterior el servidor aún no ha visto cerrarse). Los sondeos en espera adicionales reciben HTTP 429 con `Retry-After`, dejando capacidad para otros dispositivos. Mantenga un solo sondeo en espera por dispositivo y reintente según se indica; las solicitudes con `wait=0` y el límite del inquilino no cambian.
- **Las contraseñas incorrectas de dispositivo cuentan una sola vez, lleguen por donde lleguen.** Una contraseña incorrecta presentada en la ruta HTTP de cargas útiles del dispositivo ahora cuenta para la misma pausa por credencial que una contraseña MQTT incorrecta, en todas las réplicas, por lo que ya no se pueden repartir los intentos entre ambas vías para duplicarlos. Mientras una credencial está en pausa, una carga útil HTTP que la usa recibe `503` con `Retry-After` y una conexión MQTT que la usa se rechaza, como antes.
- **Las consultas de listas de eventos se detienen a los 60 segundos.** Las lecturas de `events`, `locationEvents`, `measurementEvents`, `alertEvents`, `stateChangeEvents` y `bucketedMeasurements` que no terminan en 60 segundos ahora se cancelan con un error que pide un intervalo de tiempo más acotado, en lugar de mantener ocupada una conexión a la base de datos hasta que terminen. La lectura de un usuario con ámbito limitado que especifica un ancla conserva su límite de 15 segundos.
- **Las suscripciones en vivo tienen un límite por inquilino y pod.** `measurementStream`, `alarmStream` y `detectionStream` aceptan cada uno como máximo 5.000 suscripciones simultáneas por inquilino en un pod de servicio; la siguiente se rechaza con `LIMIT_EXCEEDED`. Cierre las vistas en vivo que no utilice. El límite es por pod, así que más réplicas aumentan el total de la instancia.
- **El límite de 64 solicitudes en curso por proceso del servidor MCP es alcanzable, y la solicitud que lo supera recibe HTTP 429.** El presupuesto compartido de cuerpos de solicitud solo admitía 63 cuerpos de tamaño máximo, por lo que la solicitud simultánea número 64 recibía HTTP 503 con `Retry-After: 1` antes de alcanzar el límite de MCP. Ahora el presupuesto admite 64 (crece 64 bytes), y las solicitudes que superan el límite reciben HTTP 429 con `Retry-After: 1`, como está documentado. Un cliente que reintentaba el 503 debe reintentar el 429 de la misma forma. El límite de 4 en curso por llamante y el máximo de 1 MiB por cuerpo de solicitud no cambian.
- **Los clientes MCP deben enviar `initialize` por separado.** Una solicitud que abre una nueva sesión MCP (un POST sin la cabecera `Mcp-Session-Id`) ahora se rechaza con HTTP 400 y el error JSON-RPC `-32600` cuando su cuerpo es un lote JSON-RPC. Solo los clientes con versiones del protocolo anteriores a 2025-06-18 pueden enviar lotes; si alguno de los suyos incluye `initialize` en un lote con otras llamadas, cámbielo antes de actualizar para que envíe `initialize` solo y las demás llamadas en la sesión que este abre. Los lotes en una sesión existente no cambian.
- **Límites de sesiones MCP.** Las sesiones nuevas se rechazan con HTTP 429 y `Retry-After` cuando un usuario tiene 16 sesiones activas o el proceso MCP tiene 1.024. Los agentes deben reutilizar sesiones, cerrar las que no usan mediante DELETE y respetar `Retry-After`. Las sesiones existentes siguen disponibles; las sesiones eliminadas y las que caducan tras 30 minutos de inactividad liberan capacidad. Revise los clientes que inicializan repetidamente antes de actualizar; no es necesario cambiar la configuración.
- **Un dispositivo que pide en bucle la descarga de una actualización ahora se limita.** `update-management` responde al `download.request` de un dispositivo hasta cinco veces seguidas y después una vez por segundo, por dispositivo, igual que ya responde a un `sync` como máximo una vez cada cinco segundos. Una petición que supera ese límite se confirma sin respuesta y se cuenta en `devicechain_updatemanagement_update_reports_rejected_total{reason="throttled"}`, así que un dispositivo en bucle ya no retrasa los informes de actualización de los demás tenants. Un dispositivo que vuelve a pedir tras un reinicio o una descarga fallida sigue recibiendo respuesta.
- **Un tenant ya no puede ocupar todas las cargas de artefactos de una réplica.** Un tenant ocupa como máximo la mitad de las cargas simultáneas de artefactos de una réplica (`updates.maxConcurrentUploads`), redondeada hacia arriba y nunca menos de una. Una carga que supera la cuota de su tenant se rechaza antes de leer su cuerpo, con el mismo `503` y `Retry-After` que recibe una carga cuando la réplica está llena; reintenta cuando termine una de las cargas de ese tenant. El total de la réplica no cambia.
- **Los límites de frecuencia de la inferencia de IA se comparten entre réplicas.** La inferencia de IA guarda ahora el presupuesto de frecuencia y de ráfaga de cada inquilino en la base de datos de su propio servicio, de modo que todas las réplicas de ai-inference consumen del mismo presupuesto y una réplica reiniciada ya no empieza con una ráfaga nueva. La actualización añade una migración que crea la tabla de estado de admisión; la configuración y las API no cambian. El limitador compartido falla de forma cerrada: si la base de datos no se puede leer ni actualizar, la solicitud se rechaza en lugar de admitirse con un cupo local. El límite de cada inquilino se sigue consultando en user-management y sigue recurriendo al valor predeterminado de la plataforma cuando user-management no responde. Durante una actualización gradual, los pods antiguos conservan sus presupuestos locales hasta ser sustituidos, por lo que el límite compartido rige por completo cuando todos los pods de ai-inference ejecutan `v1.7.0`.
- **Se filtran los detalles de error de las campañas.** `Campaign.lastError` conserva los mensajes de configuración que permiten actuar; los fallos internos muestran un mensaje genérico de reintento y los detalles completos se registran en el servidor. Los mensajes siguen limitados a 500 bytes y se truncan en un límite UTF-8. El campo GraphQL y su tipo no cambian.

**Qué hace el primer arranque tras la actualización.**

- **Las comparticiones que dejaron eliminaciones anteriores se eliminan tras la actualización.** En su primer barrido, 15 minutos después de su primer arranque tras la actualización, `dashboard-management` elimina las comparticiones de dashboards que nombran un cliente o grupo de acceso eliminado antes, incluidas las eliminaciones anteriores a la actualización. El barrido necesita el secreto de servicio y el endpoint de `device-management` configurados; sin ellos no se inicia, y se registra una advertencia al arrancar. Consulte [Se eliminan las comparticiones que nombran un cliente o grupo eliminado](#v170-deleted-anchor-shares).
- **El primer arranque cancela los comandos que dejaron los dispositivos eliminados.** En su primer arranque tras la actualización, `command-delivery` lee las eliminaciones que el broker de mensajes aún conserva y cancela los comandos en cola, retenidos o aparcados de esos dispositivos, incluidas las eliminaciones anteriores a la actualización. Consulte [Se cancelan los comandos pendientes de un dispositivo eliminado](#v170-deleted-device-commands).
- **El primer arranque elimina el estado en vivo que dejaron los dispositivos eliminados.** En su primer arranque tras la actualización, `device-state` elimina el estado en vivo, las últimas lecturas, la última posición y los registros de lecturas descartadas de los dispositivos que ya no existen, incluidos los eliminados antes de la actualización, y después repite la comprobación cada 10 minutos. Un dispositivo que se eliminó y se volvió a crear con el mismo token antes de la actualización comparte un único registro de estado en vivo con el dispositivo eliminado, así que ese registro también se elimina: el último estado conocido y las últimas lecturas del dispositivo nuevo quedan vacíos hasta que vuelva a informar. Los eventos almacenados no se ven afectados. Consulte [Se elimina el estado en vivo de un dispositivo eliminado](#v170-deleted-device-state).
- **El primer arranque termina las actualizaciones asignadas a dispositivos eliminados.** En su primer arranque tras la actualización, `update-management` lee las eliminaciones que el broker de mensajes aún conserva y termina las asignaciones de actualización pendientes de esos dispositivos, incluidas las eliminaciones anteriores a la actualización. Sus objetivos de despliegue se muestran como cancelados. Consulte [Se terminan las actualizaciones asignadas a un dispositivo eliminado](#v170-deleted-device-updates).
- **Los agregados de mediciones ahora caducan con la retención.** Con `lifecycle.retentionDays` configurado en event-management, los agregados de mediciones por minuto que sustentan los gráficos y las lecturas por intervalos ahora se eliminan un intervalo de chunk (redondeado hacia arriba a días completos) después de las lecturas sin procesar, y las lecturas por intervalos ya no devuelven agregados anteriores a esa ventana. Desde el primer arranque tras la actualización, las lecturas por intervalos dejan de devolver el historial de agregados anterior a la ventana, por lo que los gráficos de ese rango no muestran datos; después, un trabajo en segundo plano elimina ese historial un chunk de almacenamiento completo cada vez. En una instancia actualizada desde una versión anterior, cada chunk de almacenamiento del agregado abarca 70 días, por lo que los agregados antiguos se ocultan de inmediato, pero solo se eliminan cuando los datos más recientes de su chunk de 70 días quedan fuera de la ventana. Las instancias con `retentionDays: 0` (el valor predeterminado) no se ven afectadas. Durante la actualización gradual, los pods que aún ejecutan v1.6 siguen sirviendo agregados antiguos hasta que se sustituyen.
- **Un cambio en la duración de los tokens de actualización llega ahora a una instancia en ejecución.** Subir o bajar `auth.refreshTokenTtlSeconds` solo cambiaba las instalaciones nuevas: un bucket de tokens de actualización existente seguía expirando las entradas con la antigüedad con la que se creó. `user-management` lleva ahora el bucket a la duración configurada al arrancar, y el administrador del broker hace lo mismo con los buckets cuya duración declara. Bajar la duración expira en ese reinicio las entradas guardadas más antiguas que el nuevo valor, de modo que las sesiones que respaldan terminan y esas personas vuelven a iniciar sesión.

#### Novedades de v1.7.0 {#v170-whats-new}

**API y SDK.**

- **Un guardado obsoleto lleva `extensions.code: STALE_WRITE`, y `@devicechain/client` exporta `isStaleWrite`.** Una escritura rechazada porque el registro cambió después de que el llamante lo leyera (una precondición de concurrencia optimista, como `expectedUpdatedAt`, que ya no se cumple) responde ahora con `extensions.code` `STALE_WRITE` en todos los servicios; antes solo las actualizaciones de conectores llevaban un código y los demás rechazos de este tipo no tenían ninguno. El mensaje no cambia ("… was modified by another writer; reload and try again"), así que los clientes que lo reconocen siguen funcionando, y nunca es `CONFLICT`, que sigue reservado para un valor que ya está en uso. `isStaleWrite(err)` de `@devicechain/client` es verdadero para el código, recurre a la frase con servidores anteriores y nunca es verdadero para un `CONFLICT`. La consola lo usa en todos los guardados con precondición.
- **`updateCustomerType` puede rechazar un guardado obsoleto.** Las ediciones de tipos de cliente aceptan ahora el argumento opcional `expectedUpdatedAt` en `updateCustomerType`. Envíe el último `updatedAt` leído para rechazar guardados obsoletos; ante "modified by another writer", vuelva a cargar e inténtelo de nuevo. Omitir el argumento conserva el comportamiento de última escritura. Esta protección está disponible cuando todas las réplicas de device-management terminan de actualizarse; las demás operaciones del registro no cambian. No se requiere migración de base de datos.
- **Listas de eventos sin total.** `EventSearchCriteria.includeTotal: false` omite el recuento exacto de eventos coincidentes, que domina la lectura de una página en un historial grande; `totalRecords` pasa a ser null y el nuevo `SearchResultsPagination.hasMore` indica si hay otra página. `hasMore` también se establece con el valor predeterminado. Durante la actualización gradual, un pod que aún ejecuta v1.6 rechaza como campos desconocidos tanto `includeTotal` como la selección de `hasMore`; use cualquiera de los dos solo cuando todos los pods de event-management ejecuten v1.7.
- **Muchos dispositivos en una sola suscripción de mediciones.** `measurementStream(deviceTokens:)` transmite lecturas de hasta 1000 dispositivos en una sola suscripción. Un pod que aún ejecute v1.6 rechaza el argumento durante la actualización gradual, por lo que los clientes solo deben enviarlo cuando todos los pods de event-management ejecuten v1.7, o volver a una suscripción por dispositivo si se rechaza.
- **Las suscripciones en vivo comparten un flujo por inquilino.** Cada pod lee ahora una sola vez el flujo en vivo de un inquilino y entrega a cada suscripción solo lo que le corresponde: las lecturas de su dispositivo en `measurementStream`, las alarmas de su originador en `alarmStream` y las detecciones de su perfil en `detectionStream`, de modo que un panel con muchos dispositivos ya no multiplica el trabajo por evento. Lo que recibe una suscripción no cambia. El nuevo límite de suscripciones por pod está en [Antes de actualizar a v1.7.0](#v170-before-upgrade).
- **Los totales de las listas se saturan en lugar de desbordarse.** Una lista con más de 2.147.483.647 filas coincidentes ahora indica 2.147.483.647 en `totalRecords` en lugar de un número desbordado, posiblemente negativo, y `pageStart`/`pageEnd` ya no se desbordan con números de página muy grandes.
- **`@devicechain/client` puede recuperarse de un HTTP 401.** Registre `setUnauthorizedHandler({ refresh, onEnd })` y una solicitud cuyo token de acceso rechace el servidor recibe una actualización forzada y una repetición antes de que termine la sesión; use como `refresh` el nuevo `session.forceRefresh(token)` de `createSessionTokens`. Un fallo temporal de la actualización (error de red, 5xx o 429) conserva la sesión y expone el error; una actualización rechazada, o un segundo 401 en la repetición, la termina. Sin un manejador, el SDK se comporta como antes.
- **`@devicechain/client` puede ejecutar una operación que gasta el token de actualización junto a sus propias actualizaciones.** `createSessionTokens` devuelve ahora `session.exchange(spend)`: `spend` recibe el token de actualización vigente y devuelve el par que lo sustituye, y se ejecuta como el único uso de ese token, después de cualquier actualización ya en curso y antes de cualquier actualización pedida mientras tanto, que toma su par en lugar de volver a gastar el token. La verificación adicional (step-up) de la consola pasa por él, de modo que una actualización forzada tras un HTTP 401 y una verificación adicional ya no pueden gastar entre las dos un mismo token de actualización y cerrar la sesión del usuario tras un código correcto.
- **Un corte de red ya no cierra la sesión de nadie.** Una renovación que no llega al servidor (sin respuesta, un 5xx o un 429) mantiene ahora la sesión mientras su token de renovación sea válido, en lugar de terminarla cuando caduca el token de acceso. En la consola y en `/dash`, las peticiones hechas con el token de acceso caducado fallan con el corte y vuelven a funcionar cuando el servidor regresa, y las pantallas murales de `/dash` siguen reintentando por sí solas. Solo un rechazo del servidor, o la caducidad del token de renovación, termina una sesión. Para quien usa `@devicechain/client`: en ese intervalo `getAccessToken()` sigue resolviendo a `null`, como antes, pero la sesión ya no se termina (no se llama a `onEnd`) y la siguiente llamada lo vuelve a intentar; `startAutoRefresh()` sigue reintentando hasta que caduca el token de renovación. Para que `getAccessToken()` rechace en su lugar con el error transitorio, de modo que una petición falle con el corte en vez de salir sin token, pase `transientRefreshErrors: 'throw'` a `createSessionTokens`, como hacen la consola y `/dash`.
- **`createSessionTokens` devuelve `ManagedSessionTokens`.** Extiende `SessionTokens`, en la que `forceRefresh` pasa a ser opcional, y mantiene `forceRefresh` obligatorio, así que el código que usa el resultado no se ve afectado. Si tipa un objeto propio como el resultado, por ejemplo un mock de prueba declarado como `ReturnType<typeof createSessionTokens>`, tipéelo como `SessionTokens`: es la interfaz pensada para los objetos que usted construye, y de lo contrario un miembro añadido más adelante a `ManagedSessionTokens` haría que el mock dejara de compilar.
- **Las suscripciones en vivo resisten una caída de la renovación del token.** Cuando el socket de una suscripción se reconectaba después de que el token de acceso hubiera caducado y la renovación del token no obtenía respuesta, o devolvía un 5xx o un 429, todas las suscripciones en vivo de esa área terminaban con un error, y los paneles mostraban sus datos como no disponibles hasta recargarlos. `@devicechain/client` reintenta ahora una reconexión cuyo obtenedor de token rechaza con ese error transitorio (como hace `getAccessToken()` con `transientRefreshErrors: 'throw'`, que usan la consola y `/dash`) con la misma espera progresiva que una red caída, y la notifica como reconexión en cuanto hay un token disponible. Una renovación rechazada sigue terminando la sesión.

**Escrituras concurrentes.**

- **Las actualizaciones y eliminaciones de proveedores de IA actúan sobre el proveedor que comprobaron.** Un `updateAiProvider` sin la precondición `expectedUpdatedAt` responde ahora que no existe cuando el proveedor se eliminó entre la lectura y la escritura, en lugar de volver a crearlo. Tampoco revierte ya un cambio de nombre simultáneo. Eliminar un proveedor borra la fila cuyas concesiones se comprobaron, aunque entretanto se produzcan un cambio de nombre y un proveedor nuevo con el token anterior, y la entrada del registro de auditoría de la eliminación registra el id y la etiqueta del proveedor.
- **Las escrituras concurrentes en la jerarquía de activos ya no pueden dar dos padres a un activo ni formar un ciclo.** Todas las formas de crear una arista de contención (`setAssetParent`, la creación individual o masiva de relaciones y el canje de reclamaciones) toman ahora un bloqueo por tenant antes de comprobar la jerarquía, de modo que las escrituras concurrentes se comprueban una tras otra. Una creación de relación que compite con otra escritura y daría a un activo un segundo padre ahora se rechaza, como ya ocurría cuando ambas no se solapaban; `setAssetParent` sigue sustituyendo al padre actual.
- **Las reclamaciones de dispositivos respetan los canjes simultáneos.** Cancelar una reclamación que acaba de canjearse devuelve `false` y la mantiene canjeada. Reabrir una reclamación revoca la relación del propietario actual antes de volver a ofrecerla; si cambia durante la reapertura, la operación se rechaza y puede reintentarse.
- **Propiedad de las cargas de artefactos.** Una carga caducada ya no puede liberar ni completar la concesión de una carga posterior, lo que mantiene una sola carga por artefacto entre réplicas. No se requiere ninguna acción del operador.

**Detección y campañas.**

- **El motor de detección espacia sus instantáneas según su coste.** Alcanzar `checkpointEvents` eventos ya no crea una instantánea de inmediato: el motor espera además a que pase cuatro veces el coste de la última instantánea, por lo que con un estado de detección grande el motor crea instantáneas con menos frecuencia, sin superar `checkpointIntervalSeconds` entre ellas, y siempre cuando las confirmaciones almacenadas alcanzan la mitad del límite del consumidor. Tras un fallo puede reproducir más de `checkpointEvents` eventos. No hay cambios de configuración.
- **Los puntos de control de detección llevan un registro de flancos de bajada.** El motor de detección registra ahora en sus puntos de control los flancos de bajada recientes de cada regla (durante diez minutos después del punto hasta el que ha registrado) y lee la hora de entrada de una activación cuando un punto de control la incluye. Cada activación y resolución se decide exactamente como en `v1.6`, y un pod `v1.6` de event-processing aún puede restaurar los puntos de control de esta versión durante la actualización gradual. Una versión posterior depende de estos campos, así que actualiza pasando por `v1.7` en lugar de saltártela: saltarse una versión menor no es una ruta probada ([Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)).
- **Un reinicio carga las versiones recientes del conjunto de geocercas antes de reproducir.** Al iniciar, event-processing carga ahora desde device-management la versión actual del conjunto de geocercas y hasta tres versiones anteriores antes de reproducir los eventos, de modo que los eventos históricos recientes pueden usar la geometría de su versión sin esperar un hecho de geocercas ni una vista previa. Las versiones más antiguas y las lecturas fallidas siguen sin resolverse y generan errores de contención contabilizados; no se precarga todo el historial. La reconciliación de inicio reintenta las versiones ausentes y omite las que ya están en memoria. Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod antiguo sigue cargando solo la versión actual al iniciar hasta que se sustituye.
- **Las instantáneas de geocercas registran la identidad de cada geocerca.** Las nuevas instantáneas de geocercas incluyen un campo opcional `fenceId` que coincide con `GeoFence.id`, para distinguir la eliminación y recreación del mismo token aunque la geometría no cambie. La entrada GraphQL lo expone como `fenceId: ID` nullable. Las instantáneas anteriores, incluidas las creadas por pods antiguos durante una actualización gradual, devuelven null: la identidad nunca se deduce del token actual. No se reescribe el historial. La siguiente modificación procesada por un pod nuevo captura la identidad en una nueva versión, incluso si solo cambian campos de presentación. No requiere migración ni intervención del operador.
- **Las campañas vuelven a comprobar la pertenencia al grupo antes de los pasos posteriores.** Antes de un paso posterior o de un instante solar de un dispositivo cuya hora programada llega más de un minuto después de la última comprobación de pertenencia, una campaña vuelve a comprobar el grupo. Un dispositivo que ha abandonado el grupo se omite solo en sus pasos que ya vencen (o vencen en el próximo minuto); sus pasos posteriores siguen pendientes y la pertenencia se vuelve a comprobar cuando vencen, de modo que un dispositivo que se reincorpora entretanto los recibe. Si el grupo no está disponible, se bloquea el envío, y los dispositivos que se incorporan después de la planificación siguen entrando en el siguiente ciclo planificado. No se requiere ninguna acción del operador.
- **Pasos cron de las campañas.** Un paso mucho mayor que el rango de su campo, como `40-59/9223372036854775803`, ya no se desborda hacia minutos fuera del rango que indica. Selecciona solo el primer valor del rango, como ya hacía cualquier paso mayor que el rango. Toda expresión aceptada antes se sigue aceptando.
- **Las entidades nuevas se incorporan a los grupos dinámicos coincidentes al crearse.** Un dispositivo, activo, área o cliente creado después de que un grupo dinámico empezara a usarse en una regla de detección o como ámbito de acceso ahora se incorpora al crearse si el selector del grupo coincide con una entidad sin atributos (por ejemplo, una condición negada como `!("retired" in attr)`). Los miembros de ese ámbito de acceso ahora ven los dispositivos creados después de que el grupo empezara a usarse, y las reglas limitadas al grupo los evalúan. Un grupo que ya se usaba antes de la actualización se pone al día en el minuto siguiente al primer arranque de `device-management`: se incorporan todas las entidades que coinciden con su selector, incluidas las creadas antes de la actualización, y desde entonces las entidades nuevas se incorporan al crearse. Lo mismo vale para un grupo que un pod v1.6 empiece a usar mientras convivan pods antiguos y nuevos durante la actualización gradual. Una entidad creada a través de un pod v1.6 en ese intervalo no se incorpora al crearse.

**Dispositivos, ingesta y conectores.**

- **Los cuerpos HTTP que no se pueden decodificar y van dirigidos a un tenant que no existe ya no se registran como decodificaciones fallidas.** El registro de decodificaciones fallidas es compartido por toda la instancia, así que un remitente que inventara nombres de tenant podía desplazar de él los registros de tenants reales. Ahora ese cuerpo se cuenta en `total_http_ingest_refused` con el motivo `unknown_tenant`, y el remitente recibe la misma respuesta que para cualquier otro cuerpo mal formado.
- **Tiempos de vida de los registros LwM2M.** Un Register o Update con un `lt` extremadamente grande, de más de unos 292 años, recibe ahora el tiempo de vida máximo configurado. Antes, el valor se desbordaba, el tiempo de vida caía al mínimo de un minuto y el dispositivo se marcaba como desconectado un minuto después.
- **Un tenant por encima de su tasa de salida ya no inunda el registro de mensajes fallidos.** `outbound-connectors` limita ahora los mensajes fallidos que escribe para los despachos descartados por tasa y para los que no se pueden entregar de forma definitiva, por tenant y entre todos los tenants, como REACT ya hace con las acciones que descarta su propia puerta. Un despacho por encima de ese límite se sigue confirmando y contando en `devicechain_outboundconnectors_connector_dispatch_total` con su resultado habitual, de modo que la alerta `ConnectorDispatchRateLimited` lee lo mismo; además se cuenta en la nueva `devicechain_outboundconnectors_connector_dispatch_unlettered_total`, y cada tenant recibe un mensaje de resumen por minuto en lugar de los individuales. Los despachos enviados a mensajes fallidos tras su último intento de entrega se siguen registrando uno a uno, como antes.

**Operación, métricas y dcctl.**

- **Métricas de consultas entre servicios, de tokens de servicio y de inicio de sesión.** `devicechain_svcclient_queries_total{result}` (`ok` o `error`) y `devicechain_svcclient_query_duration_seconds` cubren las llamadas entre servicios posteriores al control de alcance del llamante, incluidas la validación y la obtención del token. Las emisiones de tokens de servicio se cuentan por resultado en `devicechain_svcclient_service_token_mints_total{result}` (`ok` o `error`, incluidos los errores HTTP, de transporte y de respuesta inválida) y se miden con `devicechain_svcclient_service_token_mint_duration_seconds`; ninguna de las dos cuenta los aciertos de caché ni a quienes esperan la misma emisión, y el contador excluye los rechazos previos a la emisión. `devicechain_userclient_logins_total{result}` (`ok`, `error` o `mfa_challenge`) cuenta los intercambios de login, no la verificación MFA ni la selección de tenant, y `devicechain_userclient_refreshes_total{result}` (`ok` o `error`) cuenta las renovaciones antes de recurrir al login. Todas las series empiezan en cero y no llevan etiquetas de tenant, identidad, credencial, consulta, endpoint ni URL. No requiere cambios de configuración ni de base de datos; durante la actualización gradual solo los pods actualizados las exponen.
- **Se cuenta el uso del sello de texto heredado.** Dos contadores por proceso, `devicechain_credential_legacy_text_seal_reads_total` y `devicechain_credential_legacy_text_seal_writes_total`, cuentan el uso de la forma de texto heredada de las credenciales de dispositivo selladas en los mensajes de eventos. Las escrituras cuentan los sellos de texto codificados cuando `infrastructure.messaging.wireFormat` es `legacy`; las lecturas cuentan cada conversión de un sello de texto encontrado en un mensaje, incluidos valores malformados o vacíos y reentregas. El tráfico exclusivamente binario no incrementa ninguno. Ambos empiezan en cero y no tienen etiquetas, por lo que no exponen credenciales ni inquilinos. La forma de texto heredada se sigue leyendo y escribiendo exactamente igual; no se retira nada. No se requiere cambiar la configuración ni migrar datos; durante una actualización gradual solo los pods actualizados exponen los contadores.
- **El techo de autoridades de los tokens de servicio ya no señala a seis llamantes que estaban dentro de él.** `devicechain_usermanagement_service_token_checks_total` contaba las emisiones de `command-delivery`, `event-sources`, `outbound-connectors` y `event-management` como `over_ceiling`, y las de `device-state` y del propio `user-management` como `unknown_caller` con `caller="other"`, aunque cada uno pedía solo lo que usa. La tabla de techos coincide ahora con todos los servicios que emiten un token, así que esas series se quedan en cero. La comprobación sigue siendo solo de observación: no se rechaza ninguna emisión.
- **Los paneles incluidos desglosan el descarte de ingesta por clase de prioridad.** El panel Platform muestra ahora los mensajes y lecturas rechazados en el límite de ingesta de un inquilino por clase de prioridad (`shed_class`: `best-effort`, `bronze`, `silver` o `gold`), además de sus totales por origen, y el panel Ingest añade las series por clase de los mensajes descartados en el límite de lecturas. El tráfico de menor prioridad se descarta primero, así que el desglose muestra la importancia del tráfico descartado.
- **`JetStreamClusterUnused` ya no se dispara en una instancia con grupos de fragmentación.** Una instancia con [grupos de fragmentación de NATS](#v160-nats-shard-groups) ejecuta a propósito un broker de varios servidores con cada stream en una sola réplica, y la advertencia se disparaba en ella de forma continua. La alerta permanece ahora en silencio mientras algún servicio informe de un stream de grupo de fragmentación. Un broker en clúster de una sola réplica sin grupos de fragmentación la sigue generando. El nombre de la alerta no cambia.
- **La comprobación de la reserva de JetStream evalúa un único servidor del broker.** Con los [grupos de fragmentación](#v160-nats-shard-groups) activados, `dcctl` cuenta ahora lo que se le puede pedir a un solo servidor: cada stream sin fragmentar, más cada stream fragmentado una vez por cada grupo que pueda compartir servidor. El chart fija cada grupo a su propio servidor `dc-nats`, de modo que una instalación de tres grupos ya no suma tres copias contra el volumen de un único servidor; una instalación `perf-20k` con tres grupos cabe en el volumen de 40Gi del preset. La comprobación previa a la aplicación de la infraestructura usa además el `nats_jetstream_storage` que usted declare en `terraform.tfvars`, en un archivo `*.auto.tfvars` o en `TF_VAR_nats_jetstream_storage`, en lugar del tamaño del preset. Un volumen demasiado pequeño para la parte de un servidor se sigue rechazando antes de crear nada cuando su tamaño es un literal. Un tamaño dado como expresión o referencia no se puede leer antes de la aplicación, así que `dcctl` muestra un aviso, aplica la infraestructura y comprueba el volumen aprovisionado después, antes de instalar el chart.
- **Errores de manifiesto más claros en el operador.** Cuando el operador de Kubernetes no puede decodificar una sección de un manifiesto que aplica, el error indica la sección por su posición, empezando en 1, e incluye el mensaje del propio decodificador. Antes, la causa se perdía y el error terminaba en `<nil>`.
- **`dcctl ha verify --probe-mqtt` vuelve a autenticarse.** Desde `v1.1.0`, el de event-sources es el único inicio de sesión del broker por área que el broker acepta por MQTT, pero la sonda presentaba el del administrador del broker, por lo que se rechazaba en todas las instancias con inicios de sesión del broker por área. Ahora presenta el inicio de sesión de event-sources, que el broker ya acepta por MQTT; no cambia ningún permiso del broker. Una sonda rechazada indica ahora el inicio de sesión que presentó. Rara vez hace falta la opción: la propia conexión de event-sources crea los flujos MQTT del broker.
- **Un `dcctl bootstrap` rechazado deja el registro local de la instancia tal como estaba.** Cuando bootstrap se niega a ejecutarse sobre una instancia que ya está en marcha, `~/.devicechain/instances/<instance>/instance.json` se restaura byte a byte, incluido su `createdAt`. Antes, la ejecución rechazada lo reescribía con su propia hora de creación y su versión de dcctl.
- **`dcctl` avisa cuando sustituye una topología del broker que usted ha indicado.** `dcctl` siempre fija `ha` y `nats_cluster_replicas` por sí mismo, de modo que el número de servidores del broker y el número de réplicas de sus streams se mantienen juntos. No se usa el valor que usted indique para cualquiera de ellas en `TF_VAR_*` o en un archivo tfvars. `dcctl bootstrap` y `dcctl upgrade` imprimen ahora una nota cuando usted indica uno, y [Qué valor prevalece](./sizing.md#precedence) documenta la excepción.
- **Emite tokens de integración desde dcctl.** Ejecuta `dcctl product rotate-integration-token <integration-token>` después de crear una integración LoRaWAN o webhook. Captura el nuevo token desde stdout; se muestra una sola vez. Cada réplica de event-sources puede seguir aceptando el token anterior hasta 60 segundos después de la rotación, o hasta 15 minutos mientras no pueda contactar con device-management. Device-management sigue comprobando cada evento, así que este margen no autoriza nada adicional.

**Consola.**

- **El comando MQTT de la consola usa el puerto 8883.** El comando de conexión MQTT de la página del dispositivo marca ahora el puerto 8883, el puerto en el que se publica el broker fuera del clúster, en lugar del 1883, que es el puerto del broker dentro del clúster y nunca se publica. En un clúster kind local, sigue usando `localhost` y el puerto 1883.
- **Las identidades con un `%` en su correo se abren en la consola.** La página de la identidad decodificaba su dirección dos veces, así que esa identidad no se podía abrir, o se abría otra.
- **Las membresías gestionadas por un IdP dicen qué hacer en su lugar.** Cuando el servidor rechaza cambiar los roles de una membresía, habilitarla, deshabilitarla o quitarla porque la gestiona el proveedor de identidad, la página de la identidad deja de ofrecer esos cambios para ella y nombra las soluciones: cambiar los grupos de la persona en el proveedor (una persona que no está en ningún grupo que este asigne a un rol del inquilino queda deshabilitada en su siguiente inicio de sesión, y vuelve a habilitarse cuando lo está), deshabilitar la identidad o hacer que la conexión deje de atender al inquilino.
- **Las páginas de detalle de la consola abren tokens que contienen `%`.** Las páginas de detalle de dispositivos, tenants, niveles, roles, proveedores de IA, proveedores de identidad, integraciones, perfiles de aprovisionamiento, anclas de confianza y del registro decodificaban por segunda vez el token de su dirección. Un token con un `%` abría otro token, o la página no llegaba a cargarse. Ahora cada página lee el token una sola vez.
- **La consola da formato a fechas y números en su propio idioma.** Las marcas de tiempo y los recuentos seguían la configuración regional del navegador, así que una consola en español podía mostrar fechas en inglés. Ahora siguen el idioma configurado en la consola.
- **Más partes de la consola en chino simplificado.** Con la consola en 简体中文, las pantallas de dispositivos y el directorio de identidades están ahora traducidos; los paneles ya lo estaban. Las demás pantallas siguen mostrándose en inglés.

**Simuladores.**

- **Un escenario de simulador portuario.** `dcctl sim create --manifest harborpulse` recorre un distrito portuario ficticio (mareógrafos, bombas de aguas pluviales, alumbrado público, cargadores de vehículos eléctricos, dos edificios con contador y un camión de emergencias, 33 dispositivos) a lo largo de un día comprimido de 24 minutos con una tormenta, una marea viva, un corte de alimentación y una ráfaga de lecturas almacenadas y reenviadas. Define siete tipos de regla de detección, una alarma de inundación por mareógrafo que escala de gravedad y dos reglas cuyos comandos los dispositivos simulados ejecutan antes de responder.
- **El escenario portuario se comprueba a sí mismo.** Una simulación `harborpulse` publica ahora un panel de operador y un panel de solo lectura para el propietario de Quayside Lofts, informa en su `/status` de en qué punto de su día comprimido se encuentra y acepta `--day-length` para alargar el día. La nueva herramienta `scenario-verify` observa un día de una simulación `harborpulse` en marcha y relee la plataforma para confirmar que cada alarma de inundación escala y se borra, que los comandos de la bomba y del cargador reciben respuesta y cambian las lecturas, que la bomba con aire y el corte de alimentación se detectan exactamente en los dispositivos correctos, que la correlación del muelle se dispara y que las lecturas almacenadas se guardan con su hora original. Sale con 0 cuando todo se cumple, con 1 cuando algo falla y con 2 cuando no puede decidir.
- **Puertos de control separados para los simuladores.** `dcctl sim create` ahora asigna a un simulador nuevo el puerto más bajo entre 8090 y 8189 que no use otro simulador local guardado, lo que permite ejecutar varios a la vez. Volver a crear un simulador existente conserva la dirección que ya tiene, salvo que otro simulador guardado use ahora ese puerto, así que `dcctl sim status` sigue llegando a un simulador en ejecución. Se muestra la dirección elegida; use `--control-addr` para indicar una explícitamente.

#### Las eliminaciones se avisan de forma fiable {#v170-deletion-announcements}

- Al eliminar un dispositivo, cliente, área, activo o grupo, el aviso a los demás servicios se registra ahora en la misma transacción de base de datos que la eliminación, y `device-management` lo sigue publicando hasta que el broker de mensajes lo acepta. Antes, un aviso perdido por una caída del broker o un reinicio en el momento equivocado no se enviaba nunca, de modo que los dashboards compartidos, los anclajes de eventos y el estado de detección de la entidad eliminada podían quedarse atrás.
- El aviso lleva la hora de la eliminación, por tarde que se publique.
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, las eliminaciones que atiende un pod `v1.6` se avisan de la forma anterior.
- No requiere cambios de configuración.

#### Se eliminan las comparticiones que nombran un cliente o grupo eliminado {#v170-deleted-anchor-shares}

- Al eliminar un cliente o un grupo de acceso, `dashboard-management` elimina las comparticiones de dashboards que lo nombran. Si esa limpieza falla de forma persistente, el aviso se conserva ahora en el flujo de mensajes fallidos en lugar de descartarse.
- `dashboard-management` también comprueba cada 15 minutos, con las credenciales de servicio de la instancia, que el cliente o grupo de cada compartición sigue existiendo, y elimina las comparticiones que dejaron eliminaciones anteriores, incluso de antes de la actualización. Nunca elimina nada si esa consulta falla. Si el secreto de servicio o el endpoint de `device-management` no están configurados, la comprobación no se inicia y se registra una advertencia al arrancar.

#### Las alarmas de un dispositivo eliminado terminan y dejan de escalar {#v170-deleted-device-alarms}

- Al eliminar un dispositivo u otra entidad, cada una de sus alarmas activas termina ahora con un evento `CLEARED` que lleva el nuevo `clearReason: ORIGINATOR_DELETED` (también un nuevo campo `clearReason`, que admite nulo, en el `AlarmEvent` de la suscripción `alarmStream`). El escalado se detiene, y las políticas con `notifyOnClear` indican que la alarma terminó porque se eliminó su dispositivo.
- `notification-management` también consulta a `device-management`, con las credenciales de servicio de la instancia, antes de volver a notificar, y termina las notificaciones abiertas de alarmas que ya no existen, incluidas las que dejaron eliminaciones anteriores a la actualización. Si la consulta falla, notifica igualmente; los fallos se cuentan en `escalation_lookup_errors_total`. Consulta [Alarmas de un dispositivo eliminado](../guides/notification-channels.md#deleted-originator).
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod `v1.6` de `notification-management` trata ese despeje como uno normal, y una eliminación que atiende un pod `v1.6` de `device-management` no emite despeje; la comprobación previa a cada nueva notificación detiene su escalado, y la comprobación periódica termina esas notificaciones cuando tienen una hora.

#### Se cancelan los comandos pendientes de un dispositivo eliminado {#v170-deleted-device-commands}

- Al eliminar un dispositivo, `command-delivery` cancela los comandos que aún tenía para él (en cola, retenidos o aparcados) y se niega a enviar cualquier comando creado antes de la eliminación, incluido uno ya entregado a un transporte y aún no confirmado. Un dispositivo creado después con el mismo token no los recibe; los comandos creados para él tras la eliminación se envían con normalidad.
- En el primer arranque tras la actualización, `command-delivery` también lee las eliminaciones que el broker de mensajes aún conserva, de modo que los comandos que dejó cualquier dispositivo cuya eliminación aún se conserva allí se cancelan de la misma forma.
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod `v1.6` aún puede enviar uno de esos comandos hasta que se sustituye.

#### Se elimina el estado en vivo de un dispositivo eliminado {#v170-deleted-device-state}

- Al eliminar un dispositivo, `device-state` elimina su estado en vivo, sus últimas lecturas, su última posición y sus registros de lecturas descartadas. Un dispositivo creado después con el mismo token no hereda nada de eso, de modo que los miembros de su nuevo propietario no ven la última posición ni las lecturas del dispositivo anterior.
- `device-state` también pregunta ahora a `device-management` cada 10 minutos qué dispositivos siguen existiendo, con las credenciales de servicio de la instancia, y elimina el estado en vivo que dejaron dispositivos eliminados antes, incluso antes de la actualización. Nunca elimina nada si esa consulta falla. Si el secreto de servicio o los endpoints de `device-management` y `user-management` no están configurados, la comprobación se omite y se registra una advertencia al arrancar.
- Una lectura que la plataforma aceptó antes de la eliminación pero procesó después aún puede recrear brevemente el estado en vivo del dispositivo eliminado; la comprobación periódica lo elimina si ningún dispositivo tiene ese token.

#### Se terminan las actualizaciones asignadas a un dispositivo eliminado {#v170-deleted-device-updates}

- Al eliminar un dispositivo, `update-management` termina sus asignaciones de actualización pendientes, y sus objetivos de despliegue se muestran como cancelados. Un dispositivo creado después con el mismo token no las recibe ni puede descargar sus artefactos, aunque tenga otro perfil de dispositivo.
- Las asignaciones creadas antes de la actualización se comparan por tiempo: una anterior al dispositivo que ahora tiene su token no se ofrece a ese dispositivo.
- Mientras conviven pods antiguos y nuevos durante la actualización gradual, un pod `v1.6` aún puede servir una de esas asignaciones hasta que se sustituye.

#### Se conservan los anclajes de eventos de una entidad recreada {#v170-deleted-entity-anchors}

- Al eliminar un dispositivo, cliente, área, activo o grupo, `event-management` elimina los anclajes de los eventos que recibió antes de la eliminación. Antes lo decidía por la hora que indicaba el dispositivo, de modo que un dispositivo recreado con el mismo token que enviaba lecturas almacenadas con fecha anterior a la eliminación podía perder los anclajes de esas lecturas, que dejaban de aparecer bajo su cliente, área o activo. Ahora lo decide por la hora en que la plataforma recibió cada evento.

#### Los grupos de fragmentación retienen el trabajo en lugar de descartarlo como fallido {#v170-shard-groups}

- **Un atraso en cualquier grupo pausa el reenvío MQTT de todos los grupos.** El lector de captura deja de leer mientras el flujo `inbound-events` de cualquier grupo rechace mensajes, de modo que el atraso espera en el flujo de captura sin entregarse. Antes, los eventos de un grupo atrasado distinto del grupo cero se reintentaban y, tras cinco entregas, se enviaban a `failed-decode`. No se ofrece aislamiento por grupo de la captura MQTT.
- **La ingesta HTTP se admite por inquilino.** Con grupos de fragmentación activados, una solicitud HTTP o de integración solo se rechaza cuando el grupo de su propio inquilino rechaza mensajes, y un rechazo, incluido el causado por una asignación no disponible, responde `503` con una cabecera `Retry-After`. Los demás errores de publicación siguen respondiendo un `503` sin más.
- **Una asignación ausente o inutilizable retiene la ingesta.** Mientras la tabla de asignación falte, no pueda adoptarse o nombre un grupo igual o superior a `nats.shardGroups.count`, la ingesta fragmentada se retiene y se reintenta en lugar de enviarse a mensajes fallidos. El nuevo indicador `jetstream_shard_placement_unavailable` vale 1 en cada servicio afectado, y la alerta `JetStreamShardPlacementUnavailable` se activa a los dos minutos. Restaura una tabla de asignación compatible para reanudar.
- **Todos los escritores siguen la asignación.** Los escritores de `inbound-events` de device-state, sparkplug-ingest y lwm2m-ingest publican ahora cada inquilino en su propio grupo. Antes publicaban todos los inquilinos en el grupo cero, sin pasar por la asignación ni por la contrapresión descrita arriba.
- **Un servicio que no puede enrutar un flujo fragmentado lo rechaza.** Un servicio que no ha activado el enrutamiento por grupos se niega a leer o escribir un flujo fragmentado con un error explícito, en lugar de usar en silencio solo el grupo cero. `dcctl` y el chart siguen rechazando fragmentar `resolved-events` mientras device-state o event-processing estén desplegados, así que en una instancia que los ejecute fragmenta solo `inbound-events`.
- **La base para fragmentar `resolved-events` se incluye sin usarse.** device-state puede leer todos los grupos de un `resolved-events` fragmentado, el cliente del broker puede reproducir un grupo o el grupo del propio inquilino, y la vista previa de reglas y la comparación de versiones de event-processing leen el grupo del propio inquilino. Los trabajadores de detección por grupo están construidos, pero el servicio aún no los inicia. Nada de ello se ejecuta mientras se mantenga el rechazo anterior.

### v1.6.0 — permisos más estrictos y dispositivos conectados durante recargas del broker {#v160-upgrade}

`v1.6.0` es una actualización in situ desde `v1.5.0`: `dcctl install` para el clúster y después un `dcctl upgrade` para cada instancia, como se describe en [Actualizaciones graduales](#rolling-upgrades). Usa **el `dcctl` de v1.6.0 para ambos comandos**, y proporciona a `dcctl install` los ajustes del clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Una migración añadida en `device-management` hace únicas las claves de atributos activos y encola reparaciones. Consulta [Cambios de esquema en v1.6.0 y vuelta atrás](#v160-going-back). Si vienes de `v1.4.x` o anterior, actualiza antes a través de cada versión menor intermedia, incluida `v1.5.x`, siguiendo las notas de cada versión con su propio `dcctl` y su `--version`: saltarse una versión menor no es una ruta probada (consulta [Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)). Para `v1.5.0`, son las [notas de `v1.5.0`](#v150-upgrade).

```bash
dcctl install local --version v1.6.0
dcctl upgrade local devicechain --version v1.6.0 --dry-run
dcctl upgrade local devicechain --version v1.6.0
```

Se aplica la [política de versiones y actualizaciones](./versioning-policy.md). Esta versión añade interfaces cubiertas, no elimina ninguna y declara obsoletas exportaciones npm heredadas, que siguen funcionando ([detalles](#v160-npm-deprecations)). El rechazo de relaciones de dispositivos y las comprobaciones de autoridad más estrictas son correcciones de seguridad permitidas por la política. Las acciones de agentes, la evaluación de reglas y los nuevos comandos de copia y planificación de restauración son [Preview](./versioning-policy.md#preview-interfaces). Los valores de Helm y la configuración de instancia quedan fuera de la compatibilidad cubierta; mantén ausentes los grupos de shards hasta completar el despliegue.

Novedades:

- [Antes de actualizar a v1.6.0](#v160-before-upgrade)
- [Nuevos valores y comandos](#v160-values-commands)
- [Una recarga completa del broker mantiene los dispositivos admitidos por el callout de autenticación](#v160-broker-reload)
- [Las detecciones en vivo de reglas de geocercas necesitan acceso a la ubicación](#v160-detection-stream-location)
- [Las campañas solares necesitan acceso a la posición instalada](#v160-solar-campaign-position)
- [Autoridades para acciones automatizadas](#v160-action-authorities)
- [El límite de reglas del nivel se aplica al publicar un perfil](#v160-rule-ceiling)
- [Autores del borrador tras eliminar definiciones](#v160-review-deleters)
- [Los administradores delegados no pueden conceder permisos ni actuar por encima de los que poseen](#v160-no-escalation)
- [Seguridad: un dispositivo ya no puede asignarse a un cliente, área o activo](#v160-device-relationship-refused)
- [Se almacena el evento de una nueva relación](#v160-new-relationship-event)
- [La ruta HTTP de cargas útiles de dispositivos limita los intentos de adivinar credenciales](#v160-device-uplink-guessing)
- [Comprobaciones más estrictas de las credenciales HTTP](#v160-http-credentials)
- [Redirigir una credencial almacenada exige volver a proporcionarla](#v160-credential-resupply)
- [La rotación de claves de firma llega a todas las réplicas](#v160-signing-key-follow)
- [ingress-nginx ya no acepta snippets de configuración](#v160-nginx-snippets-off)
- [Las métricas y los endpoints de salud pasan al puerto ops](#v160-ops-port)
- [`dcctl destroy` rechaza rutas como instancia y ya no confía en un clúster supuesto](#v160-destroy-instance-name)
- [Límites de memoria de MCP](#v160-mcp-memory)
- [El descubrimiento MCP anuncia los ámbitos que aplica el servidor](#v160-mcp-discovery)
- [Validación de API y administración](#v160-api-checks)
- [Retención de comandos](#v160-command-holds)
- [La planificación de campañas cede el turno entre tenants](#v160-campaign-planning)
- [Las descargas, los envíos y las notificaciones se reparten entre tenants](#v160-tenant-fairness)
- [Plazo de escalado](#v160-escalation-clock)
- [Seguimiento acotado de Sparkplug](#v160-sparkplug-limits)
- [Los atributos de entidad son únicos por clave](#v160-entity-attributes)
- [Grupos de particiones de NATS opcionales](#v160-nats-shard-groups)
- [Copias de streams y objetos (Preview)](#v160-backup)
- [Creación en la consola y los paneles](#v160-console-dashboards)
- [Una pestaña de la consola ya no continúa con el inicio de sesión de otra pestaña](#v160-console-tab-session)
- [Herramientas de creación y SDK](#v160-authoring-tools)
- [Exportaciones npm heredadas obsoletas](#v160-npm-deprecations)
- [Un fallo de decodificación reenviado usa el presupuesto de registros](#v160-decoder-redelivery-budget)
- [Los fallos de ingesta inválidos identifican el dispositivo](#v160-invalid-failures-name-the-device)
- [Métricas y una alerta para las acciones de agentes (Preview)](#v160-agent-metrics)
- [Otros cambios en v1.6.0](#v160-other-changes)
- [Cambios de esquema en v1.6.0 y vuelta atrás](#v160-going-back)

#### Antes de actualizar a v1.6.0 {#v160-before-upgrade}

Qué requiere acción, en el orden en que debe hacerse:

- **Nota de actualización: herramientas.** Instala OpenTofu 1.10 o posterior (o Terraform 1.10 o posterior) en el equipo que ejecuta `dcctl`; la comprobación previa verifica su presencia, no la versión. Usa la herramienta v1.6.0, instala con los ajustes del clúster y después actualiza cada instancia. No hay degradación de versión.
- **Nota de actualización: el bróker se reinicia.** La actualización sustituye la imagen del bróker, así que cada pod del bróker se reinicia. Con `--mqtt-fail-closed` desactivado (el valor predeterminado), un mensaje QoS 1 que el bróker confirma mientras un stream cambia de líder puede perderse. Actívalo antes de actualizar (`dcctl upgrade --mqtt-fail-closed`) si los dispositivos no deben perder mensajes confirmados ([MQTT con cierre seguro](./observability.md#mqtt-fail-closed)).
- **Nota de actualización: comprobación previa de `dcctl install`.** En un clúster que conserva su controlador `ingress-nginx`, la instalación se niega a continuar mientras algún Ingress de esa clase lleve una anotación `*-snippet`. Actualiza antes la instancia propietaria o elimina la anotación ([detalles](#v160-nginx-snippets-off)).

**Corrige la configuración antes de reiniciar.** Revisa la configuración de instancia editada a mano; los ajustes inválidos impiden el arranque. Las claves de servicio siguientes van bajo `functionalAreas.<area>.config`.

- **La retención de claves debe cubrir los tokens de renovación si la rotación está activa.** En `user-management`, cuando `auth.signingKeyMaxAgeDays > 0`, `auth.signingKeyRetentionDays × 86400` debe ser al menos `auth.refreshTokenTtlSeconds` tras aplicar los valores predeterminados; se admite la igualdad. Antes, la rotación alargaba silenciosamente una retención menor. Una retención de `0` toma 8 días y el TTL de renovación predeterminado es 604800 segundos (7 días). Aumente la retención o reduzca el TTL antes de actualizar. Sin rotación, esta comparación no se aplica. El chart no compara estos campos y `dcctl` ya rechaza las superposiciones de este bloque auth.
- **Se rechaza el desbordamiento del TTL de tokens.** `user-management` rechaza ahora `auth.accessTokenTtlSeconds` o `auth.refreshTokenTtlSeconds` por encima de **9223372036 segundos**, antes de convertirlos en una duración. `0` sigue tomando los valores predeterminados (900 y 604800 segundos respectivamente); los TTL negativos ya eran inválidos. El esquema de Helm también aplica el límite superior.
- **Se rechaza el desbordamiento de la purga de tenants.** En `user-management`, `tenantPurge.intervalSeconds`, `tenantPurge.settleSeconds` y `tenantPurge.tokenHoldSeconds` no pueden superar **9223372036 segundos**. Siguen las reglas existentes: un intervalo negativo desactiva el coordinador; los valores settle/hold negativos se rechazan y se mantienen sus mínimos de seguridad.
- **Se rechaza el desbordamiento de cartas muertas.** En `user-management`, `deadLetters.retentionDays` no puede superar **106751 días** y `deadLetters.sweepSeconds` no puede superar **9223372036 segundos**. Se mantienen las reglas existentes de valores predeterminados, desactivación y retención mínima.
- **Se rechaza el desbordamiento de las sondas de presencia Sparkplug.** En `sparkplug-ingest`, `presence.probeSeconds` y `presence.probeWindowSeconds` no pueden superar **9223372036 segundos**. `0` sigue tomando 1800 y 10 segundos respectivamente. Se mantienen los mínimos: un intervalo explícito es de al menos 60 segundos; una ventana explícita es de al menos 5 segundos y estrictamente menor que el intervalo.

**Revisa permisos y clientes antes del despliegue.**

- **Nota de actualización:** Concede `location:read` a los consumidores de `detectionStream` de geocercas; el ámbito OAuth de solo lectura no lo incluye por sí solo. Concede `location:installed:read` o `location:read` a los roles que gestionan campañas `SOLAR`.
- **Nota de actualización:** Los autores de automatización necesitan `command:write` para `sendCommand` y `connector:write` para `httpCall` o `publish`, además de `device:write`, incluso al publicar o revertir. Revisa los roles de administrador delegado: las operaciones que superan sus autoridades actuales devuelven `ESCALATION`; usa un superusuario para esas operaciones.
- **Nota de actualización:** Sustituye las asignaciones `NewRelationship` con seguimiento o `contains` enviadas por dispositivos por códigos de reclamación o `createEntityRelationship`. Revisa las relaciones con seguimiento existentes; la actualización no las elimina. Los pods antiguos siguen aceptando los eventos durante el despliegue.
- **Nota de actualización:** Reduce las consultas MCP que superen 256 KiB de respuesta o 1.000 bloques temporales y maneja HTTP 429 con `Retry-After`. Limita los grafos borrador de `evaluateRules` a 256 reglas de detección; los grafos mayores se rechazan antes de compilar. Renueva las revisiones de automatización caducadas antes de publicar.
- **Nota de actualización: credenciales y emisores.** Los clientes que cambien el tipo o el destino de un conector, canal de notificación o proveedor de IA con una credencial almacenada deben enviar `secret` en la misma solicitud ([detalles](#v160-credential-resupply)). Una regla cuyo `httpCall` nombre un manejador de secreto `connector/` se rechaza al publicarla y, si ya está publicada, se envía a la cola de mensajes no entregados ([detalles](#v160-http-credentials)). Los emisores de cargas útiles HTTP deben esperar ante `429` y `503` con `Retry-After` ([detalles](#v160-device-uplink-guessing)).

**Durante y después del despliegue.**

- **Mantenga ausente `infrastructure.nats.shardGroups` durante todo el despliegue v1.5 → v1.6.** Un pod `v1.5` rechaza esta nueva clave como desconocida si carga la nueva configuración durante el despliegue. Termine de actualizar todos los pods antes de activarla. Si se incluye, `count` debe ser 2–16; los sufijos deben ser únicos y limitarse a `inbound-events` y `resolved-events`; las claves de `placementTags` deben ser números de grupo en `0..count-1`, incluido el grupo cero existente, con etiquetas no vacías ni compuestas solo por espacios. `dcctl` deriva una etiqueta de ubicación `dc-nats-<group>` para cada grupo, incluida `dc-nats-0`. Los bloques inválidos se rechazan. Lo que acepta `dcctl` es más restringido: véase [Grupos de particiones de NATS opcionales](#v160-nats-shard-groups).

- **Los atributos de entidad son únicos por clave.** La actualización elimina duplicados activos de escrituras iniciales simultáneas, conserva el menor ID y encola la reparación de pertenencias a grupos y configuración. La creación del índice bloquea brevemente las escrituras de atributos. Durante el despliegue gradual, una réplica antigua puede rechazar una escritura inicial concurrente; vuelva a intentarla. No se requiere cambiar la configuración.

- **Nota de actualización:** Añade `AgentExecutionsFailing` al enrutamiento de alertas y replica `nats:2.15.3-alpine` si mantienes preparada la alternativa del broker original. Detén las escrituras antes de usar los nuevos comandos de copia.
- **Nota de actualización: puerto de métricas.** Si tu propia configuración de Prometheus recopila métricas de los pods, cambia el puerto de destino de 8080 a 8083; los ServiceMonitors y las sondas del chart se actualizan automáticamente ([detalles](#v160-ops-port)).

#### Nuevos valores y comandos {#v160-values-commands}

| Ajuste o comando | Cambio / acción del operador |
|---|---|
| `--size perf-20k` | Nuevo preajuste de rendimiento: solicitud del broker de 6 CPU / 6Gi, límite de memoria de 10Gi, JetStream de 40Gi y almacenamiento de eventos de 260Gi. No promete el objetivo de latencia PUBACK de 50 ms; el preajuste aún no se ha medido tal como está definido. |
| GKE `nats_jetstream_storage_class` | Los brokers nuevos usan `premium-rwo` (`pd-ssd`) cuando existe esa clase GKE; se rechaza una clase GKE `premium-rwo` con otro tipo de disco. Prevalecen los valores explícitos. Los brokers/PVC existentes y las actualizaciones conservan su clase; esto no migra discos. |
| GKE `backup_object_store_storage_class` | Los PVC nuevos de copias usan una clase dedicada `pd-standard` (no `standard-rwo`/`pd-balanced`, que consume cuota SSD). Esta variable permite elegir otra. Las actualizaciones conservan el tamaño y la clase de los PVC existentes, sin migración automática. Los demás proveedores no cambian. |
| `infrastructure.nats.shardGroups` | Enrutamiento opcional por tenant para los dos streams de ingesta; su ausencia conserva el enrutamiento actual. Los streams de grupo multiplican las reservas por `count`: calcule la capacidad antes de activarlo. Actívelo solo tras completar el despliegue; véase [Grupos de particiones de NATS opcionales](#v160-nats-shard-groups). |
| `dcctl backup`, `dcctl backup inspect --verify`, `dcctl restore --plan` | Comandos en **vista previa** para conjuntos locales de copias de streams/objetos, verificación de bytes y planificación de restauración de solo lectura. No hay comando que aplique la restauración ni checkpoint coordinado de bases de datos. Véase [Exportar streams y contenido de objetos](./disaster-recovery.md#export-streams-objects). |

#### Una recarga completa del broker mantiene los dispositivos admitidos por el callout de autenticación {#v160-broker-reload}

Una recarga completa del broker cerraba todas las conexiones admitidas por el callout de autenticación, entre ellas las de todos los dispositivos MQTT que se autentican con un nombre de usuario. El broker ahora se compila con `nats-server` `v2.15.3`, por lo que un cambio en el conjunto de CA del Secret TLS del broker (una rotación de CA), o una recarga manual, mantiene abiertas esas conexiones. Una recarga sin reinicio solo ocurre cuando el sidecar config-reloader del chart envía SIGHUP porque cambió el Secret TLS montado, o cuando un operador ejecuta `nats-server --signal reload` a mano.

Cuando una recarga cambia los ajustes de autenticación del propio servidor (sus usuarios configurados, nkeys, etiquetas o ajustes TLS verify/map), `nats-server` sigue enviando las conexiones admitidas por el callout de nuevo al callout de autenticación. Una conexión que el callout rechace entonces se cierra.

- **No hay que hacer nada.**
- **Un cambio de configuración sigue reiniciando gradualmente el broker.** La configuración del broker se genera en un ConfigMap y el chart incorpora su hash a la plantilla de pods del StatefulSet. Cualquier cambio en esa configuración generada (ajustes, dimensionamiento o cuentas) reinicia los pods del broker, por lo que los dispositivos se reconectan, como antes; `dcctl upgrade` reinicia gradualmente el broker en cualquier actualización que cambie su imagen o configuración.
- **Un certificado TLS renovado** bajo la misma CA mantiene su comportamiento anterior, vigente desde `v0.27.0`: se aplica a las conexiones nuevas sin recarga.
- **La imagen de upstream para volver al broker original ahora es `nats:2.15.3-alpine`**: fija `server_image_repository = "nats"` y `server_image_tag = "2.15.3-alpine"`, como se describe en [Certificados de dispositivos](./device-certificates.md). Añade la nueva etiqueta a las imágenes que replicas si mantienes preparada esa vuelta atrás.
- **Un pod del broker que se detiene mientras aún arranca ahora se detiene enseguida.** El broker sigue el manejo de señales de `nats-server` `v2.15.3` de upstream: una recarga solicitada mientras JetStream aún se recupera se ejecuta cuando termina el arranque, y un SIGTERM, o la petición de modo lame duck que envía el hook preStop del pod, se atiende entretanto en lugar de esperar detrás de ella. Una petición de lame duck que llega antes de que el broker acepte clientes se ignora, así que el SIGTERM que le sigue detiene el pod. `nats-server -t --test_reload <archivo>` indica si un archivo de configuración puede recargarse sin reiniciar (código de salida 3 cuando no puede).

#### Las detecciones en vivo de reglas de geocercas necesitan acceso a la ubicación {#v160-detection-stream-location}

La suscripción `detectionStream` ya no entrega detecciones de reglas que comprueban si un dispositivo está dentro de una geocerca a quien no tenga `location:read`, al igual que la vista previa de reglas. Las demás detecciones no cambian. Un panel o agente que supervise reglas de geocercas necesita un rol o token que incluya acceso a la ubicación. Un token emitido con el ámbito OAuth de solo lectura no incluye `location:read`.

#### Las campañas solares necesitan acceso a la posición instalada {#v160-solar-campaign-position}

Crear una campaña `SOLAR`, leer sus elementos (`campaignElements`) o ciclos (`campaignCycles`) y reintentar sus fallos (`retryCampaignFailed`) ahora requiere además `location:installed:read` (o `location:read`). Las horas por dispositivo de un calendario solar revelan la posición instalada de cada dispositivo; incluso los recuentos de un ciclo pueden revelar resultados derivados de la posición en un grupo de un solo dispositivo. Las campañas `CRON` y `CALENDAR` no cambian. Añade esta autoridad a cualquier rol que gestione campañas solares.

#### Autoridades para acciones automatizadas {#v160-action-authorities}

Crear o publicar una acción automatizada requiere la autoridad para ejecutarla. Crear o actualizar una regla con una acción `sendCommand`, y publicar o volver a una versión de perfil cuyas reglas habilitadas incluyan una, requiere `command:write` además de `device:write`. Las acciones `httpCall` y `publish` requieren `connector:write`. Un rol que solo tenga `device:write` puede seguir creando y publicando reglas que únicamente generen alarmas. Las reglas ya publicadas siguen ejecutándose; se comprueban en la siguiente publicación o reversión. Revise los roles que crean automatizaciones antes de actualizar. Las réplicas antiguas de `device-management` no aplican esta comprobación hasta que se sustituyan.

#### El límite de reglas del nivel se aplica al publicar un perfil {#v160-rule-ceiling}

- **Techo de reglas opcional:** la publicación y reversión de perfiles aplican `rulesPerTenant` solo si el nivel lo fija explícitamente. Los valores ausentes/null y esquemas antiguos de user-management conservan el comportamiento existente; mantener o reducir reglas nunca consulta user-management. Otros fallos de consulta solo rechazan aumentos de reglas.

#### Autores del borrador tras eliminar definiciones {#v160-review-deleters}

La revisión de cambios normalmente no considera autoras del borrador a las personas que solo eliminaron reglas, métricas o comandos en otros perfiles. Las eliminaciones no atribuidas a una fila, anteriores a la actualización o realizadas por un pod aún sin actualizar durante una actualización gradual, se tratan de forma conservadora: si desapareció una definición del perfil revisado sin una eliminación atribuida que lo explique, toda persona que haya realizado una eliminación no atribuida desde el último cambio atribuido de esa definición cuenta como autora. Las revisiones ya ejecutadas conservan la lista de autores que registraron; ejecuta una nueva revisión si se excluyó a un aprobador. Al eliminar un perfil, ahora se escribe una entrada de auditoría por cada definición que se elimina.

- **No es necesario hacer nada.** Ejecuta una nueva revisión si se excluyó a un aprobador por una eliminación en otro perfil.

#### Los administradores delegados no pueden conceder permisos ni actuar por encima de los que poseen {#v160-no-escalation}

La API de administración del operador ahora limita una identidad cuyo rol de sistema no sea superusuario a los permisos que conceden sus roles de sistema actuales. La API del inquilino también comprueba el rol actual y sus miembros antes de reducir los permisos de un rol del inquilino.

- **API de administración del operador:** una identidad delegada solo puede asignar roles de sistema cuyos permisos posea; actuar sobre otra identidad (contraseña, enlaces de inicio de sesión, sesiones, habilitación o eliminación y roles de sistema) si posee todos los permisos que conceden los roles de sistema de esa identidad; y crear, cambiar o eliminar un rol dentro de los permisos que posee.
- **Membresías y asignaciones de roles al iniciar sesión:** en la API de administración del operador, una identidad delegada solo puede añadir o cambiar una membresía de un inquilino con roles del inquilino cuyos permisos posea, solo puede cambiar, deshabilitar o eliminar una membresía si posee todos los permisos que conceden sus roles, y solo puede asignar grupos de proveedores de identidad o inicios de sesión con aprovisionamiento justo a tiempo a roles del inquilino cuyos permisos posea. Actuar sobre otra identidad también requiere poseer todos los permisos que conceden sus membresías de inquilino, y vincular cuentas a una conexión de proveedor de identidad de inquilino, cambiar los inquilinos que abarca o cambiar su atributo de grupos requiere poseer todos los roles que concede esa conexión.
- **API del inquilino:** un administrador del inquilino solo puede cambiar un rol del inquilino si posee todos los permisos que concede, y solo puede quitarle permisos si posee todos los que poseen sus miembros.
- **Qué comprobar:** un rol de sistema delegado que necesite administrar superusuarios o definir roles con permisos superiores a los suyos ahora recibe `ESCALATION`; realice esos cambios como superusuario.

#### Seguridad: un dispositivo ya no puede asignarse a un cliente, área o activo {#v160-device-relationship-refused}

- Un evento `NewRelationship` que nombre un tipo de relación con seguimiento (el tipo integrado `assigned` o cualquier tipo marcado con seguimiento) o el tipo integrado `contains` se rechaza y se registra en el flujo `failed-events` con motivo `Invalid` en su primera entrega, sin reintentos. Esto cambia lo que hace un evento de dispositivo aceptado; la [política de versiones](./versioning-policy.md) lo permite para una corrección de seguridad.
- Asigna los dispositivos con un código de reclamación o con la mutación `createEntityRelationship`.
- Las relaciones creadas anteriormente por los dispositivos no se eliminan. Revisa las relaciones con seguimiento de tus dispositivos.
- Los tipos sin seguimiento, incluido `member`, se siguen creando, salvo que tu tenant haya marcado ese tipo con seguimiento. El propio evento `NewRelationship` ya no lleva el nuevo destino como ancla: una relación sin seguimiento no es propiedad, así que el evento queda acotado por las anclas existentes del dispositivo, igual que su siguiente evento de telemetría.
- Durante la actualización gradual, los pods que siguen en v1.5 crean la relación hasta que se sustituyen.
- El nuevo contador `resolve_relationship_refused_total` cuenta estos rechazos sin etiquetas de tenant o dispositivo; el log de advertencia indica el tenant y la correlación.

#### Se almacena el evento de una nueva relación {#v160-new-relationship-event}

Un evento `NewRelationship` enviado por un dispositivo ahora se almacena y aparece en `events` (tipo de evento `0`) con sus anclajes. Antes de esta versión se reintentaba y después se registraba como un fallo de ingesta con el motivo `API_CALL_FAILED`, aunque la relación se había creado. Un evento de un tipo que el almacén de eventos no admite ahora se rechaza en su primera entrega con el motivo `INVALID`, en lugar de reintentarse. Los eventos enviados antes de la actualización no se recuperan.

- **No hay que hacer nada.**

#### La ruta HTTP de cargas útiles de dispositivos limita los intentos de adivinar credenciales {#v160-device-uplink-guessing}

La ruta HTTP de cargas útiles de dispositivos, que se activa con `httpIngest.deviceUplinks`, ahora limita las solicitudes por dirección de origen (`429` con `Retry-After`). Tras fallos repetidos de credenciales para un dispositivo, deja de responder a los intentos de autenticación (`503` con `Retry-After`) en lugar de repetir `401`. Mientras se intenta adivinar la credencial de este dispositivo, todas sus cargas útiles HTTP, incluidas las que presentan la credencial correcta, reciben `503` con `Retry-After` hasta que termina la pausa (como máximo 60 segundos cada vez).

- **Los dispositivos conectados por MQTT al broker no se ven afectados.**
- **Espera antes de reintentar tras un `503`**, incluso si presentas una credencial incorrecta: espera al menos el tiempo indicado por `Retry-After`.

#### Comprobaciones más estrictas de las credenciales HTTP {#v160-http-credentials}

Una acción `httpCall` ya no puede nombrar la credencial de un conector. Un manejador de secreto del espacio de nombres `connector/` se rechaza al publicar una regla, y una regla publicada que lo use se envía a la cola de mensajes no entregados con el resultado `invalid`, en lugar de enviar la credencial del conector. Las réplicas antiguas de `outbound-connectors` no aplican esta restricción hasta que se sustituyen.

#### Redirigir una credencial almacenada exige volver a proporcionarla {#v160-credential-resupply}

**Cambiar el destino de una credencial almacenada.** `updateConnector`, `rollbackConnector`, `updateNotificationChannel` y `updateAiProvider` rechazan la solicitud con `SECRET_RESUPPLY_REQUIRED` si cambia el tipo o destino de un conector, canal de notificación o proveedor de IA con una credencial almacenada y no incluye `secret` en la misma solicitud. Vuelve a enviar la credencial o usa `secret: null` para eliminarla. Los cambios que no modifican el destino (nombre, tema, QoS, cabeceras personalizadas, URL base de enlaces, modelo, habilitación) no se ven afectados; los conectores, canales y proveedores existentes siguen funcionando. Para restaurar un conector a una versión con otro destino, usa `updateConnector` con su credencial. Con `dcctl product apply`, establece el identificador de credencial del conector cuando el manifiesto cambie su destino. Las réplicas antiguas no aplican esta comprobación hasta que sean reemplazadas.

#### La rotación de claves de firma llega a todas las réplicas {#v160-signing-key-follow}

Con `auth.signingKeyMaxAgeDays` configurado, user-management ahora rota una clave de firma demasiado antigua mientras se ejecuta: unos 15 minutos después de que arranque una réplica y cada vez que la clave supere esa edad después, no solo al arrancar. Todas las réplicas adoptan la nueva clave en un máximo de 30 segundos y la validan y publican en cuanto reciben un token o una solicitud de claves que la necesita. Las versiones anteriores aplicaban la rotación solo en la réplica que la realizaba, por lo que, con más de una réplica, algunos inicios de sesión fallaban hasta que las demás se reiniciaban.

- **No hace falta hacer nada.** Nada cambia cuando el ajuste es 0.

#### ingress-nginx ya no acepta snippets de configuración {#v160-nginx-snippets-off}

`dcctl install` desactiva los snippets de configuración en el controlador `ingress-nginx` que el clúster conserva y restablece su nivel de riesgo de anotaciones al valor predeterminado del controlador. Antes de aplicar cambios, la instalación se niega a continuar mientras algún Ingress de la clase de ese controlador lleve una anotación `*-snippet`, e identifica cada Ingress y anotación. Actualiza la instancia propietaria con `dcctl upgrade`, o elimina la anotación, y vuelve a ejecutar `dcctl install`.

Ningún chart desde `v0.25.2` establece snippets. Los clústeres nuevos, que solo ejecutan Traefik, no se ven afectados. Este cambio llega a cada clúster en su siguiente `dcctl install`.

#### Las métricas y los endpoints de salud pasan al puerto ops {#v160-ops-port}

Las métricas y los endpoints de salud de los servicios se han separado del puerto de la API. `/metrics`, `/healthz` y `/readyz` se sirven en el puerto 8083 (`ops`) de los pods y del Service de cada área y ya no son accesibles mediante `/api/<area>/...`. Los ServiceMonitors y las sondas del chart se actualizan automáticamente. Si recopilas métricas de los pods con tu propia configuración de Prometheus, cambia el puerto de destino de 8080 a 8083. Durante el despliegue gradual, no se recopilan métricas de los pods que aún no se han reemplazado.

#### `dcctl destroy` rechaza rutas como instancia y ya no confía en un clúster supuesto {#v160-destroy-instance-name}

`dcctl destroy` rechaza un argumento de instancia que no sea un nombre simple (`.`, `..` o cualquier valor que contenga un separador de ruta). Las versiones anteriores lo resolvían como un directorio bajo `~/.devicechain/instances` y eliminaban el destino resultante.

- **El estado local requiere pruebas.** `dcctl destroy` solo borra el estado local de un clúster ausente cuando la instancia tiene un registro de su clúster y el contexto registrado de ese clúster ya no responde. Una instancia sin registro pasa por la desinstalación normal, que falla si no se puede acceder al clúster; indique `--kube-context` o elimine manualmente el directorio de la instancia si su clúster realmente ha desaparecido.

#### Límites de memoria de MCP {#v160-mcp-memory}

- **Límites de memoria de MCP:** se rechazan por completo las respuestas GraphQL de más de 256 KiB y las agregaciones de más de 1.000 bloques temporales; reduce el intervalo de fechas, selecciona una métrica o amplía los bloques temporales. Los POST y las solicitudes con cuerpo se limitan a 4 en curso por usuario y 64 por proceso antes de leer o analizar el cuerpo. La admisión de mensajes reserva hasta 4 plazas por lote; los lotes válidos según la configuración (16 entradas por defecto) ejecutan las herramientas por grupos dentro de ese límite. Cada entrada sigue contando para el límite de frecuencia del usuario; las solicitudes adicionales reciben HTTP 429 con Retry-After. Al terminar la solicitud HTTP, se cancelan las herramientas y se liberan sus plazas de ejecución. La reserva de lectura del cuerpo se libera antes de ejecutar la herramienta; se mantiene el límite de 1 MiB por solicitud. No requiere cambios de configuración.

#### El descubrimiento MCP anuncia los ámbitos que aplica el servidor {#v160-mcp-discovery}

El descubrimiento MCP ahora ofrece `read-only` y `location`, además de `act:*` cuando `agentActuation.enabled` está activado en MCP. Los desafíos permiten usar estos metadatos en vez de fijar `read-only`. El acceso sigue requiriendo solo `read-only`; las herramientas de acción conservan sus controles de ámbitos y permisos. No hay que cambiar la configuración; los tokens existentes siguen siendo válidos. Vuelva a autorizar los clientes para conceder los ámbitos descubiertos y active también la actuación en el servidor de autorización para usar propuestas.

#### Validación de API y administración {#v160-api-checks}

- `evaluateRules` (Preview) rechaza grafos borrador de más de 256 reglas con `LIMIT_EXCEEDED` antes de compilar ninguna. La validación conserva la ruta de campo de cada diagnóstico para señalar el campo incorrecto.
- `existingEntityRefs` rechaza tipos desconocidos o más de 1.000 claves de un tipo, incluidos duplicados, antes de ejecutar consultas; antes podía rechazar tras otras consultas.
- `updateRole` de administración acepta `expectedUpdatedAt` opcional; rechaza una versión obsoleta para que el cliente recargue. Omitirlo mantiene la actualización incondicional.
- `setTenantEnabled` rechaza un tenant cuya purga haya comenzado y vuelve a comprobar su identidad bajo bloqueo, evitando que una purga concurrente o un token reutilizado cambie otro tenant.
- Las decisiones de ejecución de agentes (Preview) limitan los principales del proponente y los aprobadores a 256 caracteres y los IDs de sesión suministrados a UUID canónicos de 36 caracteres antes de leer sesiones; la cancelación detiene las lecturas posteriores. Los proponentes de cuentas de servicio pueden omitir el ID de sesión.
- Las revisiones aprobadas caducadas se leen y filtran como `EXPIRED`, incluso en el instante exacto de caducidad; publicar devuelve `REVIEW_EXPIRED` y rechazar devuelve `REVIEW_NOT_PENDING`. La caducidad se calcula con el vencimiento guardado y la política actual, sin una actualización de registro en segundo plano.

#### Retención de comandos {#v160-command-holds}

Eliminar una ventana de mantenimiento ya no libera incondicionalmente los comandos en espera. Si el tipo ya no tiene ventana, se liberan en aproximadamente un minuto; los dispositivos deshabilitados o los tipos sin resolver siguen retenidos hasta resolverse y después siguen su tipo. Si no se puede determinar el canal, los comandos siguen `QUEUED`, sin publicarse, hasta la primera pasada tras recuperarse (30 segundos por defecto), en lugar de pasar a `HELD`. El reconciliador continúa mientras libera comandos, hasta la mitad de su intervalo, y se detiene ante una página sin liberaciones. Nuevo contador: `command_delivery_dispatch_deferred_total`. No hay que hacer nada. Las réplicas antiguas mantienen el comportamiento anterior hasta finalizar la actualización gradual.

#### La planificación de campañas cede el turno entre tenants {#v160-campaign-planning}

- **La planificación de campañas cede el turno entre inquilinos.** Cada inquilino planifica como máximo 1.000.000 de elementos de campaña y 128 ciclos por pasada, y no inicia ningún ciclo nuevo una vez agotado un presupuesto de planificación de 2 segundos; un ciclo ya iniciado siempre termina, y el progreso se guarda tras cada ciclo para que los restantes continúen en la siguiente pasada. Las consultas de grupo y de ubicación de dispositivos agotan su tiempo a los 2 segundos y se informan como error de la pasada sin marcar la campaña como fallida. Se ejecutan hasta cuatro inquilinos simultáneamente. Los planes grandes pueden completar su ventana de 36 horas en varias pasadas. Estos límites no requieren configuración nueva ni migración y se aplican al actualizar los pods de command-delivery.

#### Las descargas, los envíos y las notificaciones se reparten entre tenants {#v160-tenant-fairness}

- **Descargas OTA:** Las descargas OTA permiten un flujo simultáneo por (inquilino, dispositivo, asignación) en cada réplica. Mientras otro inquilino ocupe un cupo, un inquilino que alcance o supere la mitad de `updates.maxConcurrentDownloads` (redondeada hacia abajo, mínimo uno; ocho con el valor predeterminado de 16) no puede iniciar otro flujo. Un único inquilino puede usar todos los cupos. Las solicitudes que excedan estos límites reciben la respuesta 503 de ocupado con `Retry-After: 5`; reintente tras ese plazo. No se requiere cambiar la configuración.
- **Envíos de conectores y downlinks LoRaWAN:** Los envíos de conectores salientes y los downlinks LoRaWAN limitan ahora cada tenant a la mitad del grupo de envíos de cada réplica (redondeada hacia abajo, mínimo uno), según `maxConcurrentSends`. Los mensajes que superan el límite esperan la reentrega del broker sin ocupar trabajadores de envío. Un grupo de uno sigue serializando los tenants; use al menos dos trabajadores para aislarlos. No requiere nueva configuración; el límite se aplica al actualizar cada pod.
- **La planificación de notificaciones está limitada por inquilino.** Cuatro trabajadores atienden a los inquilinos en orden rotativo. Cada inquilino deja de iniciar liberaciones y escalaciones tras el doble del presupuesto total de envío (80 segundos con los valores predeterminados) mientras otro inquilino espera un trabajador; si nadie espera, el inquilino continúa; una alarma ya iniciada conserva su presupuesto completo y las restantes quedan pendientes para el siguiente ciclo. Los avisos de resolución se procesan dentro de cada trabajo con su propio presupuesto. Los canales que agotan sus intentos por tiempo de espera se omiten durante el resto del trabajo; una escalación dirigida solo a esos canales conserva su nivel y se reintenta en el siguiente ciclo. La primera prueba aún puede consumir un nivel. No se requiere cambiar la configuración y la existente funciona durante las actualizaciones graduales.

#### Plazo de escalado {#v160-escalation-clock}

El plazo de escalado ahora se cuenta desde el envío de la notificación. Una alarma generada por una lectura que llega con retraso ya no escala en la siguiente pasada del planificador, y un reloj de dispositivo adelantado respecto a la plataforma ya no retrasa el escalado.

- **No hay que hacer nada.** La actualización añade automáticamente una marca de tiempo que admite valores nulos; las alarmas abiertas existentes conservan el cálculo anterior hasta su siguiente notificación.

#### Seguimiento acotado de Sparkplug {#v160-sparkplug-limits}

**Nota de actualización:** Cada fuente Sparkplug conserva como máximo 50.000 sesiones de nodo, incluidas 1.024 sin NBIRTH. Las identidades nuevas que superen un límite se descartan sin ingesta ni solicitud de renacimiento; los nodos existentes pueden renacer y las sesiones en línea nunca se expulsan. Los marcadores inactivos caducan a los 10 minutos y las sesiones sin conexión a las 24 horas; al alcanzar el límite, los nacimientos recuperan las sesiones sin conexión más antiguas. El historial de actividad admite 50.000 identidades: bajo presión elimina entradas antiguas y rechaza nuevas entradas si sigue lleno (mientras está lleno, una sonda no declara silenciosa una identidad no registrada). Se rechazan los niveles de grupo, nodo y dispositivo con más de 256 caracteres, de acuerdo con el límite del registro de dispositivos. Las negativas se cuentan en `devicechain_sparkplugingest_session_refused_total`, con `reason` igual a `placeholder_cap`, `session_cap` o `seen_cap`. Los avisos de capacidad y de cola llena se limitan, cada tipo, a uno por fuente cada 10 minutos; los registros por mensaje pasan a debug. No hace falta cambiar la configuración para una actualización gradual.

#### Los atributos de entidad son únicos por clave {#v160-entity-attributes}

- **Los atributos de entidad son únicos por clave.** La actualización elimina duplicados activos de escrituras iniciales simultáneas, conserva el menor ID y encola la reparación de pertenencias a grupos y configuración. La creación del índice bloquea brevemente las escrituras de atributos. Durante el despliegue gradual, una réplica antigua puede rechazar una escritura inicial concurrente; vuelva a intentarla. No se requiere cambiar la configuración.

#### Grupos de particiones de NATS opcionales {#v160-nats-shard-groups}

Los grupos de particiones de NATS siguen desactivados por defecto. Una instancia nueva
puede activarlos mediante `--values` en `instance.config.infrastructure.nats.shardGroups`
al instalarse. La opción admite entre 2 y 5 grupos con flujos R1 en un clúster de al
menos tres servidores del broker, y rechaza `--ha`. Las etiquetas de colocación usan los
nombres de los pods `dc-nats`; las etiquetas explícitas deben coincidir con esos nombres.
Los perfiles predeterminados permiten particionar `inbound-events`; se rechaza
`resolved-events` si se despliega device-state o event-processing, porque esos
consumidores solo leen el grupo cero. Cada flujo seleccionado reserva su límite completo
de bytes en cada grupo, y `dcctl` comprueba esa reserva antes de crear o modificar
cualquier infraestructura, así que amplíe primero el volumen de JetStream si la
comprobación lo rechaza.

`dcctl upgrade` rechaza activar los grupos de particiones en una instancia existente cuyo
broker es un único servidor, porque no se admite convertir en clúster un servidor
JetStream independiente que contiene datos. También rechaza una actualización cuyo
`--values` (o `--reset-values`) eliminaría un bloque `shardGroups` registrado, reduciría su
`count` o quitaría un flujo de sus `suffixes`, ya que los grupos pueden contener datos:
mantenga el bloque en cada archivo de valores de reemplazo.

#### Copias de streams y objetos (Preview) {#v160-backup}

`dcctl backup <instance> --destination <dir>` exporta streams conservados para checkpoints con sus consumidores y contenido de objetos a un nuevo conjunto local. Detén las escrituras primero; no coordina puntos de corte de bases de datos ni el modo de mantenimiento. Funciona en todos los perfiles y registra los streams ausentes porque su área no está desplegada. Cada stream, bucket KV y almacén de objetos declara su clase de copia.

`dcctl backup inspect <dir> --verify` verifica sin conexión el manifiesto, tamaño y SHA-256 de cada archivo listado; no demuestra integridad del inventario ni coherencia del checkpoint. `dcctl restore <instance> --from <dir> --plan` verifica bytes, identidad y cobertura antes de leer los streams y objetos de destino, y rechaza un plan que pueda sobrescribir contenido más reciente. Detén las escrituras de destino al planificar. `--force-foreign` permite una identidad ajena sin omitir las comprobaciones de cobertura o contenido reciente. No se aplica ninguna restauración. Para blobs de sistema de archivos, proporciona `--blob-directory` con la raíz del PVC montada o exportada en solo lectura. Consulta [Exportar streams y contenido de objetos](./disaster-recovery.md#export-streams-objects).

#### Creación en la consola y los paneles {#v160-console-dashboards}

- Los autores de paneles pueden elegir un punto de ruptura y mover, redimensionar o colocar widgets para él; los cambios de base siguen llegando a diseños que los heredan. Los diseños existentes siguen siendo válidos.
- El editor de reglas añade formularios Filter, Exists y Math para expresiones compatibles; siguen disponibles las expresiones personalizadas.
- **Admin → Settings → Functional areas** muestra las áreas desplegadas y cómo habilitar otras.
- Los diálogos de secretos e invitaciones de una sola visualización requieren **Done** para cerrar; Escape y los clics fuera mantienen el valor visible. Las páginas de detalle ignoran respuestas obsoletas tras navegar.
- El disparo de lotes reutiliza el token de idempotencia al reintentar y el panel de comandos ignora envíos duplicados. Los widgets de control de comandos limpian el estado pendiente al cambiar de dispositivo; la clasificación de alarmas agrupa las actualizaciones durante tormentas de eventos.
- Un panel repite la consulta de tipo de dispositivo si antes no encontró ninguno. Las paletas de gráficos por ámbito se actualizan al cambiar el tema del host. Los documentos GraphQL usan importaciones directas para reducir el paquete de consola.
- El texto restante de los widgets compartidos de los paneles está traducido al español y al chino simplificado.

#### Una pestaña de la consola ya no continúa con el inicio de sesión de otra pestaña {#v160-console-tab-session}

Una pestaña de la consola cuya sesión ha terminado ya no continúa con una sesión que otra pestaña abrió para otro inquilino o usuario; vuelve al inicio de sesión.

- **No es necesario hacer nada.**

#### Herramientas de creación y SDK {#v160-authoring-tools}

- Desde `frontend`, `npm run create-widget -- acme/tank-level ../my-widget` crea un paquete de widget tipado con SDK como dependencias pares y rechaza un destino existente. Si las versiones pares son `0.0.0-dev`, establece versiones publicadas antes de instalar.
- Puedes generar esquemas JSON de reglas y conectores con `go run ./cmd/rule-schema` desde `backend/services/event-processing` y `go run ./cmd/connector-schema mqtt` desde `backend/services/outbound-connectors`.
- Los paquetes publicados de SDK de cliente y widgets incluyen ahora su guía para agentes.

#### Exportaciones npm heredadas obsoletas {#v160-npm-deprecations}

Las siguientes exportaciones quedan obsoletas en `v1.6.0` y siguen funcionando sin cambios. Actualice las importaciones o llamadas con los reemplazos indicados. No se elimina ninguna exportación. La eliminación requiere una versión mayor tras al menos dos versiones menores de obsolescencia; si empieza en 1.6, no podrá eliminarse nada antes de una versión mayor posterior a 1.7. `BuiltinOptionIssue` nombra la forma existente del problema de un widget integrado (`widgetType`); el tipo general `@devicechain/dashboards.OptionIssue` (`widgetId`) sigue siendo compatible.

- `@devicechain/widgets` / `pickSample` queda obsoleta; use `@devicechain/widget-sdk.pickSample`.
- `@devicechain/widgets` / `optBoolean` queda obsoleta; use `@devicechain/widget-sdk.optBoolean`.
- `@devicechain/widgets` / `WidgetProps` queda obsoleta; use `@devicechain/widget-sdk.WidgetProps`.
- `@devicechain/widgets` / `WidgetComponent` queda obsoleta; use `@devicechain/widget-sdk.WidgetComponent`.
- `@devicechain/widgets` / `WidgetFrame` queda obsoleta; use `@devicechain/widget-sdk.WidgetFrame`.
- `@devicechain/widgets` / `WidgetSubjectProvider` queda obsoleta; use `@devicechain/widget-sdk.WidgetSubjectProvider`.
- `@devicechain/widgets` / `WidgetSelectProvider` queda obsoleta; use `@devicechain/widget-sdk.WidgetSelectProvider`.
- `@devicechain/widgets` / `useWidgetSelect` queda obsoleta; use `@devicechain/widget-sdk.useWidgetSelect`.
- `@devicechain/widgets` / `WidgetTimeZoneProvider` queda obsoleta; use `@devicechain/widget-sdk.WidgetTimeZoneProvider`.
- `@devicechain/widgets` / `useWidgetTimeZone` queda obsoleta; use `@devicechain/widget-sdk.useWidgetTimeZone`.
- `@devicechain/widgets` / `WidgetThemeRootProvider` queda obsoleta; use `@devicechain/widget-sdk.WidgetThemeRootProvider`.
- `@devicechain/widgets` / `useWidgetThemeRoot` queda obsoleta; use `@devicechain/widget-sdk.useWidgetThemeRoot`.
- `@devicechain/widgets` / `WidgetCandidatesProvider` queda obsoleta; use `@devicechain/widget-sdk.WidgetCandidatesProvider`.
- `@devicechain/widgets` / `useWidgetCandidates` queda obsoleta; use `@devicechain/widget-sdk.useWidgetCandidates`.
- `@devicechain/widgets` / `useFlashOnChange` queda obsoleta; use `@devicechain/widget-sdk.useFlashOnChange`.
- `@devicechain/widgets` / `flashTextStyle` queda obsoleta; use `@devicechain/widget-sdk.flashTextStyle`.
- `@devicechain/widgets` / `flashBackgroundStyle` queda obsoleta; use `@devicechain/widget-sdk.flashBackgroundStyle`.
- `@devicechain/widgets` / `FlashValue` queda obsoleta; use `@devicechain/widget-sdk.FlashValue`.
- `@devicechain/widgets` / `fitValueFont` queda obsoleta; use `@devicechain/widget-sdk.fitValueFont`.
- `@devicechain/widgets` / `useMeasurementStream` queda obsoleta; use `@devicechain/widget-sdk.useMeasurementStream`.
- `@devicechain/widgets` / `useAlarmStream` queda obsoleta; use `@devicechain/widget-sdk.useAlarmStream`.
- `@devicechain/widgets` / `useCommandStream` queda obsoleta; use `@devicechain/widget-sdk.useCommandStream`.
- `@devicechain/widgets` / `useLocationStream` queda obsoleta; use `@devicechain/widget-sdk.useLocationStream`.
- `@devicechain/widgets` / `useDatasourceAvailability` queda obsoleta; use `@devicechain/widget-sdk.useDatasourceAvailability`.
- `@devicechain/widgets` / `useDashboardHub` queda obsoleta; use `@devicechain/widget-sdk.useDashboardHub`.
- `@devicechain/widgets` / `useHubConnectionState` queda obsoleta; use `@devicechain/widget-sdk.useHubConnectionState`.
- `@devicechain/widgets` / `HubConnectionBadge` queda obsoleta; use `@devicechain/widget-sdk.HubConnectionBadge`.
- `@devicechain/widgets` / `useResolvedBindings` queda obsoleta; use `@devicechain/widget-sdk.useResolvedBindings`.
- `@devicechain/widgets` / `useViewerBindings` queda obsoleta; use `@devicechain/widget-sdk.useViewerBindings`.
- `@devicechain/widgets` / `useSlotCandidates` queda obsoleta; use `@devicechain/widget-sdk.useSlotCandidates`.
- `@devicechain/widgets` / `useCandidates` queda obsoleta; use `@devicechain/widget-sdk.useCandidates`.
- `@devicechain/widgets` / `css` queda obsoleta; use `@devicechain/widget-sdk.css`.
- `@devicechain/widgets` / `FlashDirection` queda obsoleta; use `@devicechain/widget-sdk.FlashDirection`.
- `@devicechain/widgets` / `WidgetFrameProps` queda obsoleta; use `@devicechain/widget-sdk.WidgetFrameProps`.
- `@devicechain/widgets` / `WidgetSelect` queda obsoleta; use `@devicechain/widget-sdk.WidgetSelect`.
- `@devicechain/widgets` / `WidgetCandidates` queda obsoleta; use `@devicechain/widget-sdk.WidgetCandidates`.
- `@devicechain/widgets` / `MeasurementStreamState` queda obsoleta; use `@devicechain/widget-sdk.MeasurementStreamState`.
- `@devicechain/widgets` / `AlarmStreamState` queda obsoleta; use `@devicechain/widget-sdk.AlarmStreamState`.
- `@devicechain/widgets` / `CommandStreamState` queda obsoleta; use `@devicechain/widget-sdk.CommandStreamState`.
- `@devicechain/widgets` / `LocationStreamState` queda obsoleta; use `@devicechain/widget-sdk.LocationStreamState`.
- `@devicechain/widgets` / `CandidatesState` queda obsoleta; use `@devicechain/widget-sdk.CandidatesState`.
- `@devicechain/widgets` / `DatasourceAvailability` queda obsoleta; use `@devicechain/widget-sdk.DatasourceAvailability`.
- `@devicechain/widgets` / `HubConnectionBadgeProps` queda obsoleta; use `@devicechain/widget-sdk.HubConnectionBadgeProps`.
- `@devicechain/widgets` / `ThemeVar` queda obsoleta; use `@devicechain/widget-sdk.ThemeVar`.
- `@devicechain/widgets` / `EChart` queda obsoleta; use `@devicechain/widget-sdk/echarts.EChart`.
- `@devicechain/widgets` / `buildLineOption` queda obsoleta; use `@devicechain/widget-sdk/echarts.buildLineOption`.
- `@devicechain/widgets` / `buildGaugeOption` queda obsoleta; use `@devicechain/widget-sdk/echarts.buildGaugeOption`.
- `@devicechain/widgets` / `resolveChartTheme` queda obsoleta; use `@devicechain/widget-sdk/echarts.resolveChartTheme`.
- `@devicechain/widgets` / `useChartTheme` queda obsoleta; use `@devicechain/widget-sdk/echarts.useChartTheme`.
- `@devicechain/widgets` / `EChartOption` queda obsoleta; use `@devicechain/widget-sdk/echarts.EChartOption`.
- `@devicechain/widgets` / `LineChartOptions` queda obsoleta; use `@devicechain/widget-sdk/echarts.LineChartOptions`.
- `@devicechain/widgets` / `GaugeChartOptions` queda obsoleta; use `@devicechain/widget-sdk/echarts.GaugeChartOptions`.
- `@devicechain/widgets` / `ChartTheme` queda obsoleta; use `@devicechain/widget-sdk/echarts.ChartTheme`.
- `@devicechain/widgets` / `WIDGET_CHANNEL` queda obsoleta; use `builtinWidgetDescriptors[id].channel`.
- `@devicechain/widgets` / `WIDGET_BINDS_DATASOURCE` queda obsoleta; use `builtinWidgetDescriptors[id].datasource.mode !== 'none'`.
- `@devicechain/widgets` / `isTerminalStatus` queda obsoleta; use `@devicechain/dashboards.isTerminalCommandStatus`.
- `@devicechain/widgets` / `OptionIssue` queda obsoleta; use `@devicechain/widgets.BuiltinOptionIssue`.

#### Un fallo de decodificación reenviado usa el presupuesto de registros {#v160-decoder-redelivery-budget}

Un fallo del decodificador de carga útil que se vuelve a entregar (por ejemplo, tras un reinicio con una acumulación de mensajes de enlace ascendente de un tipo de dispositivo cuyo decodificador falla) ahora usa el presupuesto de registros de fallos por tipo de dispositivo como cualquier otro fallo. Una vez agotado el presupuesto, se cuenta como suprimido y no se escribe en el flujo failed-events, aunque su primera entrega nunca haya generado un registro. Antes, un fallo reenviado siempre se registraba, por lo que un reinicio podía registrar todos los mensajes de enlace ascendente acumulados.

- **No hay que hacer nada.**

#### Los fallos de ingesta inválidos identifican el dispositivo {#v160-invalid-failures-name-the-device}

Los fallos de ingesta con motivo `INVALID` que device-management registró para un evento legible (un valor que incumple la definición de su métrica, un evento de más de 366 días o un rechazo del decodificador) ahora identifican el dispositivo y el tipo de evento, y se limitan por dispositivo como cualquier otro motivo. Antes de esta versión se mostraban sin dispositivo y compartían un límite de diez registros por hora para todo el tenant. Los registros indexados antes de la actualización no se reescriben.

- **No hay que hacer nada.** No se recuperan registros históricos. Ahora se aplican los límites existentes: diez registros por dispositivo y motivo por hora, con un máximo de 1000 registros detallados por tenant y motivo por hora. Una carga inválida en toda la flota puede alcanzar ese máximo.

#### Métricas y una alerta para las acciones de agentes (Preview) {#v160-agent-metrics}

`device-management` informa ahora de las acciones de agentes: propuestas por tipo y resultado, aprobaciones por resultado, ejecuciones por tipo y resultado con su duración, y propuestas que caducaron. El panel Platform incorpora una fila **Agent actions (preview)**, y una nueva alerta de aviso, `AgentExecutionsFailing`, se dispara cuando las aprobaciones finales no llegan a completarse de forma continuada. Las denegaciones no la disparan: un cambio de dependencia, una denegación de la política, una barrera de desactivación cerrada o una denegación del aprobador se cuentan con su propio resultado. Las métricas figuran en la [referencia de métricas](./metrics-reference.md), y la alerta tiene una entrada en el [manual de alertas](./alert-runbook.md#agentexecutionsfailing). Como las acciones de agentes, están en Preview: los nombres y las etiquetas aún pueden cambiar en una versión menor.

#### Otros cambios en v1.6.0 {#v160-other-changes}

- Un bootstrap rechazado antes de escribir mantiene la fase registrada; los rechazos de superposiciones de uplinks MQTT indican la opción contradictoria.
- Los diagnósticos y errores de OpenTelemetry usan los logs JSON estructurados de los servicios. El broker cierra su endpoint de métricas y espera a que termine al apagarse.
- El cliente PostgreSQL pasa a `pgx` 5.12.0 con correcciones de seguridad para `GHSA-j38q-h3ph-jjpq` y `GHSA-95fj-3fxw-g62g`. No requiere cambios de configuración.

#### Cambios de esquema en v1.6.0 y vuelta atrás {#v160-going-back}

Una migración añadida de `device-management` incorpora una cola de reparación de atributos y un índice único para claves de atributos activos. Conserva el menor ID entre duplicados y encola reparaciones de pertenencia a grupos y configuración; consulta [Atributos de entidad](#v160-entity-attributes). Tras la reparación, cada entidad y ámbito tiene un valor de atributo actual por clave. Ninguna otra área añade una migración de esquema en esta versión.

No hay degradación de versión. Reintenta una actualización fallida con la herramienta v1.6.0, avanza a una versión corregida o restaura desde una copia adecuada. Los comandos Preview de streams y objetos no sustituyen las copias de bases de datos ni aplican una restauración.

## Versiones anteriores {#older-releases}

Cuando se añade una nueva línea de versiones menor, la línea más antigua documentada completa pasa aquí como una entrada breve. Cada entrada enlaza con las notas completas en la documentación de esa versión, tal como estaban en su publicación, y con su versión en GitHub. Todas las versiones aparecen en [GitHub Releases](https://github.com/devicechain-io/devicechain/releases).

### v1.5.0 — cambios de automatización revisados y aprobaciones de agentes vinculadas al inicio de sesión {#v150-upgrade}

<a id="v150-before-you-upgrade"></a><a id="v150-dcctl"></a><a id="v150-configuration"></a><a id="v150-input-limits"></a><a id="v150-tightenings"></a><a id="v150-automation-review"></a><a id="v150-agent-actions"></a><a id="v150-lorawan"></a><a id="v150-observability"></a><a id="v150-concurrent-edits"></a><a id="v150-other-changes"></a><a id="v150-going-back"></a><a id="v150-known-issues"></a>

Notas completas: [documentación de v1.5](/1.5/deployment/releases-and-upgrades#v150-upgrade). Versión en GitHub: [v1.5.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.5.0).

### v1.4.0 — downlinks LoRaWAN confirmados, la carga útil propia de un dispositivo por MQTT y notificaciones que nombran el dispositivo {#v140-upgrade}

Notas completas: [documentación de v1.4](/1.4/deployment/releases-and-upgrades#v140-upgrade). Versión en GitHub: [v1.4.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.4.0).

### v1.3.0 — los comandos llegan a dispositivos HTTP y LoRaWAN, y las entregas dejan pruebas {#v130-upgrade}

<a id="v130-token-audience"></a>

Notas completas: [documentación de v1.3](/1.3/deployment/releases-and-upgrades#v130-upgrade). Versión en GitHub: [v1.3.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.3.0).

### v1.2.0 — decodificadores de carga útil que escribe cada inquilino, y una revocación que cierra sesiones {#v120-upgrade}

Notas completas: [documentación de v1.2](/1.2/deployment/releases-and-upgrades#v120-upgrade). Versión en GitHub: [v1.2.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.2.0).

### v1.1.0 — la ingesta cuesta menos por evento, y los dispositivos fuera del clúster se conectan por MQTT {#v110-upgrade}

<a id="v110-ingest"></a>

Notas completas: [documentación de v1.1](/1.1/deployment/releases-and-upgrades#v110-upgrade). Versión en GitHub: [v1.1.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.1.0).

### v1.0.0 — la primera versión estable: una caída de la base de datos retiene eventos, y Traefik es el acceso por defecto {#v100-upgrade}

Notas completas: [documentación de v1.0](/1.0/deployment/releases-and-upgrades#v100-upgrade). Versión en GitHub: [v1.0.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.0.0).

<a id="v0270-upgrade"></a><a id="v0260-upgrade"></a><a id="v0252-upgrade"></a><a id="v0251-upgrade"></a><a id="v0250-upgrade"></a><a id="v0240-upgrade"></a><a id="v0230-upgrade"></a><a id="v0220-upgrade"></a><a id="v0220-packages"></a><a id="v0211-upgrade"></a><a id="v0210-upgrade"></a><a id="v0200-upgrade"></a><a id="v0190-upgrade"></a><a id="v0180-upgrade"></a><a id="v0170-upgrade"></a><a id="v0160-upgrade"></a><a id="v0150-upgrade"></a><a id="v0120-upgrade"></a>

Las versiones anteriores a `v1.0.0` no se incluyen aquí. Sus notas están en [GitHub Releases](https://github.com/devicechain-io/devicechain/releases) y en el repositorio, en la etiqueta de cada versión.

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
