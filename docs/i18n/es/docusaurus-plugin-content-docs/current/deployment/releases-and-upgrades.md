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

- **[v1.10](#v1100-upgrade)**: [v1.10.0](#v1100-upgrade)
- **[v1.9](#v190-upgrade)**: [v1.9.0](#v190-upgrade)
- **[v1.8](#v180-upgrade)**: [v1.8.0](#v180-upgrade)

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

### v1.10.0 — en desarrollo {#v1100-upgrade}

Estas notas son de la versión en desarrollo y crecen hasta su publicación.

#### Antes de actualizar a v1.10.0 {#v1100-before-upgrade}

#### Novedades de v1.10.0 {#v1100-whats-new}

### v1.9.0 — las actualizaciones se despliegan por cohortes y las herramientas comprueban lo que sirve una instancia {#v190-upgrade}

`v1.9.0` es una actualización in situ desde `v1.8.x`: `dcctl install` para el clúster y después un `dcctl upgrade` para cada instancia, como se describe en [Actualizaciones graduales](#rolling-upgrades). Usa **el `dcctl` de v1.9.0 para ambos comandos** con OpenTofu o Terraform 1.10 o posterior, y proporciona a `dcctl install` los ajustes del clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Se añaden trece migraciones, que se ejecutan en el primer arranque de cada servicio: tres en `command-delivery`, cuatro en `device-management`, cuatro en `event-management`, una en `update-management` y una en `event-processing`. Cada una puede volver a ejecutarse tras un fallo. Crean tablas, índices y un rollup y añaden columnas que pueden quedar vacías, y las claves de configuración que añade esta versión son opcionales y no existen salvo que las establezcas, de modo que los pods de `v1.8.x` siguen funcionando junto a los de `v1.9.0` mientras avanza la actualización. La actualización solo avanza: no hay vuelta atrás a `v1.8.x`. `dcctl upgrade` ahora se niega a saltarse una versión menor. Si vienes de `v1.7.x` o anterior, actualiza antes a través de cada versión menor intermedia, incluida `v1.8.x`, siguiendo las notas de cada versión con su propio `dcctl` y `--version` (consulta [Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)). Para `v1.8.0`, son las [notas de `v1.8.0`](#v180-upgrade).

```bash
dcctl install local --version v1.9.0
dcctl upgrade local devicechain --version v1.9.0 --dry-run
dcctl upgrade local devicechain --version v1.9.0
```

Se aplica la [política de versiones y actualizaciones](./versioning-policy.md). Esta versión añade interfaces cubiertas y no elimina ninguna. Declara obsoletos el recurso personalizado `InstanceConfiguration` y los campos `spec.grafanaSSO` y `spec.compact` de una declaración `Instance`, que siguen funcionando hasta una versión mayor posterior. La actualización de la biblioteca GraphQL rechaza algunos literales numéricos que antes se aceptaban; los rechazos y cambios de comportamiento nuevos se indican en [Antes de actualizar a v1.9.0](#v190-before-upgrade) y [Novedades de v1.9.0](#v190-whats-new). Los valores de Helm y la configuración de instancia quedan fuera de la compatibilidad cubierta.

Lo más destacado: un despliegue OTA puede avanzar por canarios con nombre y porcentajes acumulados y detenerse según una tasa de fallos, y la consola lo ejecuta. `dcctl` y la consola comprueban el esquema que sirve una instancia antes de enviar una petición, y cada versión publica un JSON Schema de su configuración de instancia. Los agentes pueden proponer por MCP un comando de dispositivo que una persona aprueba en la consola (Preview, desactivado por defecto), y un comando enviado por una automatización registra por qué se envió. Con MQTT con cierre ante fallos activado, `event-sources` ya puede escribir sobres de eventos (opcional, desactivado por defecto). Los valores de facetas se pueden importar por lotes, también desde un archivo CSV en la consola, las relaciones se pueden gestionar desde `dcctl` y los gráficos de largo alcance leen un rollup de una hora. Una lectura por el token de un dispositivo recreado ya no devuelve los eventos del dispositivo eliminado, las integraciones entrantes siguen aceptando mensajes mientras `device-management` no está disponible brevemente, y el liderazgo del broker se equilibra en la instalación.

Novedades:

- [Antes de actualizar a v1.9.0](#v190-before-upgrade)
- [Novedades de v1.9.0](#v190-whats-new)

#### Antes de actualizar a v1.9.0 {#v190-before-upgrade}

- `dcctl bootstrap` y `dcctl upgrade` ahora leen InstanceOperations antes de actuar. La identidad que ejecuta cualquiera de los comandos necesita `list` sobre `instanceoperations.core.devicechain.io`; cluster-admin ya tiene este permiso. Si la definición del recurso no existe, se considera que no hay solicitudes.
- **`dcctl upgrade` ahora se niega mientras otra ejecución de dcctl tiene el bloqueo del clúster, en lugar de avisar y continuar.** Toma el bloqueo antes de su primera escritura y vuelve a comprobar el operador del clúster mientras lo tiene, de modo que un `dcctl install` no puede mover el operador por debajo. El bloqueo es uno por clúster, así que dos actualizaciones de instancias DISTINTAS del mismo clúster ya no pueden ejecutarse a la vez: ejecútalas una tras otra. Una cuenta sin `create` y `delete` sobre `leases.coordination.k8s.io` en `dc-k8s-system` ahora se rechaza en lugar de recibir un aviso. Una ejecución que fue terminada deja el bloqueo atrás: tómalo con `dcctl instances reclaim` (espera la duración de un lease y te pide que escribas de vuelta el titular). Una ejecución que sigue viva lo sigue renovando y no se puede reclamar: detén antes ese proceso. Dos cambios relacionados: `dcctl instances release` se niega cuando el operador del clúster no coincide con este dcctl (pasa `--force` para liberar de todos modos; un clúster sin ningún operador se sigue dejando pasar, que es para lo que existe el comando, y ahora necesita `get` sobre `customresourcedefinitions`), y las escrituras de dcctl que reemplazan una declaración de instancia entera (bootstrap, upgrade y sus registros de anotaciones) rechazan una declaración que lleva campos que este dcctl no conoce, en lugar de borrarlos. Usa el dcctl que coincide con el clúster.
- **Las escrituras de atributos de dispositivo toman un bloqueo por dispositivo.** Toda escritura de los atributos de un dispositivo se ejecuta ahora en una transacción y toma primero un bloqueo sobre ese dispositivo: establecer o eliminar un atributo (incluido el ámbito CLIENT, que antes se escribía sin transacción), restaurar o copiar la configuración de un dispositivo, importar valores de facetas para dispositivos y eliminar un dispositivo. Dos escrituras sobre el mismo dispositivo se aplican una después de la otra; las escrituras sobre dispositivos distintos no se esperan entre sí. El bloqueo en sí añade una función de base de datos (el protocolo de escritura descrito más abajo añade una tabla). Si una fila de atributo sigue ocupada tras cinco reintentos breves, `setEntityAttribute`, `deleteEntityAttribute`, `restoreDeviceConfiguration` y `copyDeviceConfiguration` responden con el nuevo código de error reintentable `ATTRIBUTE_WRITE_BUSY` y no se escribe nada; repita la solicitud. Mientras la actualización se despliega, una escritura atendida por un pod actualizado puede ceder brevemente ante una escritura del mismo atributo atendida por un pod que aún no se ha actualizado. Tres métricas nuevas lo registran: `device_attribute_lock_wait_seconds`, `device_attribute_write_yields_total` y `device_attribute_write_deadlocks_total`.
- **Las escrituras de atributos de dispositivo siguen un protocolo de escritura.** Toda escritura de los atributos de un dispositivo, y la purga de un inquilino, marcan ahora su transacción como conforme al protocolo de escritura de atributos de dispositivo, y cada asignación, eliminación o recreación de un atributo de dispositivo avanza una generación por clave guardada en una tabla nueva, `entity_attribute_generations`. En esta versión nada exige el protocolo; una versión posterior depende de que todos los pods de esta lo sigan, así que actualice de versión menor en versión menor. Mientras la actualización se despliega, un dispositivo eliminado por un pod que aún no se ha actualizado puede dejar filas de atributos; una pasada de limpieza en una sola réplica las elimina en un plazo de diez minutos, y `device_attribute_orphans_removed_total{table}` cuenta lo que eliminó.
- **Permiso reservado para operaciones de flota.** El nuevo permiso de inquilino `fleet:operate` queda reservado para operaciones de flota en una versión posterior; ninguna API lo comprueba todavía. No se añade a ningún rol integrado (los administradores de inquilino que tienen `*` ya lo satisfacen). Concédalo a un rol personalizado solo **después de que haya finalizado el despliegue de la actualización**: un pod de user-management que aún no se haya actualizado rechaza cualquier edición de un rol cuyo conjunto de permisos contenga un nombre que no reconoce, incluso una edición no relacionada de un rol que ya lo tenga.
- **La configuración de GKE en `deploy/gke` ahora crea su clúster mediante un módulo compartido.** Las variables, los valores por defecto y las salidas no cambian, y un estado aplicado desde una copia anterior pasa a las nuevas direcciones en el siguiente `tofu apply` (`tofu plan` muestra cada recurso como un movimiento y no planifica nada más). Cuando un estado se haya aplicado desde esta versión, no lo aplique desde una copia anterior: la configuración antigua ya no encuentra sus recursos en las direcciones previas, así que planificaría eliminar el clúster y crear uno nuevo. Si ejecuta pruebas de carga desde varias copias, actualícelas todas primero. El paso «If an apply fails part-way» del README ahora nombra el clúster como `module.gke_cluster.google_container_cluster.this`.
- **Obsoletos: el recurso personalizado `InstanceConfiguration` y los campos `spec.grafanaSSO` y `spec.compact` de una declaración `Instance`.** En esta versión nada cambia para una instancia, y una versión mayor posterior los elimina. `InstanceConfiguration` (`dcic`) no se usa, porque cada servicio lee la configuración de la instancia del Secret montado, y el servidor de API avisa ahora cuando se lee o se escribe uno. `spec.grafanaSSO` no tiene efecto: el inicio de sesión de Grafana se configura para todo el clúster. `spec.compact` se sustituye por `spec.size: compact`, que dcctl ya escribe junto a él: **conserva ambos campos y no quites `compact`**. dcctl sigue leyendo `compact` y rechaza una declaración cuyo `size` es `compact` sin `compact`, así que quitarlo a mano bloquea todos los arranques iniciales y las actualizaciones posteriores. No hace falta hacer nada.
- **Actualice una segunda o tercera instancia local solo con el `dcctl` de esta versión o de una posterior.** Una instancia a la que esta versión asignó el puerto de nodo MQTT 31884 o 31885 lo conserva en todas las actualizaciones posteriores. Un `dcctl` anterior solo conoce el 31883, y su actualización elimina el puerto MQTT de esa instancia, de modo que el broker queda accesible solo dentro del clúster.
- **`dcctl upgrade` rechaza saltarse una versión menor.** Saltarse una versión menor nunca ha sido una ruta con soporte; ahora `dcctl upgrade` lo exige. Lee la versión que ejecutan realmente las cargas de trabajo de la instancia (de las imágenes en ejecución, no de la versión declarada, de modo que una actualización que se detuvo a medias se juzga por lo que sigue ejecutándose) y rechaza un destino más de una versión menor por delante, indicando a qué versión menor actualizar primero. Al cruzar una versión mayor solo permite su primera versión menor. Si no se puede leer la versión en ejecución, la actualización también se rechaza. `--allow-skipped-minor` anula el rechazo; esa ruta no tiene soporte ni está probada, y la opción nunca desactiva una protección que aplique la base de datos.

#### Novedades de v1.9.0 {#v190-whats-new}

- **Un dispositivo cuyo registro automático sigue fallando ya no ralentiza su origen.** Cuando un origen de integración, Sparkplug o LwM2M registra automáticamente un dispositivo y device-management rechaza la creación (por ejemplo, porque el origen indica un tipo de dispositivo que no existe), ese dispositivo espera ahora un breve periodo de espera antes de volver a intentarlo. El periodo empieza en 10 segundos y se duplica mientras continúan los fallos, hasta 5 minutos. Los mensajes que envía dentro de ese periodo se descartan y se cuentan junto con los demás descartes de dispositivos no registrados, sin otra llamada a device-management. El periodo de espera es por dispositivo, de modo que los demás dispositivos del mismo origen no se ven afectados, y termina en cuanto el dispositivo se registra. A un dispositivo LwM2M dentro de ese periodo se le indica que vuelva a registrarse más tarde.
- **El liderazgo del broker se reparte en la instalación, y puede ver dónde está.** `dcctl bootstrap` termina ahora equilibrando qué servidor del broker de mensajes lidera cada stream, el mismo paso de mejor esfuerzo que `dcctl upgrade` ejecuta después de reiniciar el broker. Nunca hace fallar el bootstrap. Ese paso, `dcctl upgrade` y `dcctl broker rebalance` mantienen además el stream de eventos resueltos fuera del servidor que lidera el almacén de mensajes MQTT del broker, y mueven los consumidores de eventos resueltos fuera de un servidor que lidera más streams por evento que alguna de sus réplicas. Los streams de ingesta de dispositivos y los streams MQTT siguen sin moverse nunca. El nuevo comando `dcctl ha leaders` informa, sin mover nada, de qué servidor lidera cada stream por evento y cada consumidor de eventos resueltos, y de si el reequilibrado podría separarlos más (`PASS`, `FAIL` o `UNSETTLED`, con `--json` y `--expect-fail`). Un `dcctl` anterior no conoce el almacén de mensajes MQTT y puede mover los eventos resueltos a su servidor, así que ejecute el reequilibrado con el `dcctl` de esta versión.
- **Importación de valores de facetas por lotes.** La nueva mutación GraphQL `importFacetValues` acepta hasta 1.000 valores en dispositivos, activos, áreas y clientes existentes identificados por token. Valida cada valor según el tipo declarado de la faceta, escribe en el ámbito SHARED e informa del resultado de cada fila, incluidos los errores. Use `dryRun: true` para obtener una vista previa; al repetir valores sin cambios, devuelve `EXISTS` sin volver a escribirlos. La importación de dispositivos desde CSV sigue disponible mediante la mutación existente `importDevices`.
- **Un comando enviado por una automatización registra por qué se envió.** Cuando la acción de envío de comandos de una regla envía un comando, este incluye ahora un `cause`: la regla y el perfil que se activaron, el tipo de detección, su hora exacta y el valor que la provocó y, cuando la misma regla también apunta a una alarma en esa detección, la clave de la alarma (lo que por sí solo no prueba que la alarma se generara). `Command.cause` es nuevo en la API GraphQL de command-delivery, junto con `CommandCause.deviceId`, que permite asociar la alarma con el dispositivo correcto aunque se haya reutilizado un token de dispositivo. Para ver una causa se necesita `device:read`, además de `alarm:read` cuando nombra una alarma y `location:read` cuando la regla evalúa una geocerca. Los miembros limitados a algunos clientes no la ven. Solo los servicios de la plataforma pueden establecer una causa; una solicitud que la establece con el token de una persona se rechaza. La herramienta MCP `list_commands` también devuelve la causa, y `list_alarms` devuelve ahora el `originatorId` de cada alarma. Los comandos enviados por personas, por lotes o antes de esta versión no tienen causa, y algunos enviados durante el despliegue de la actualización pueden no tenerla. La nueva métrica `react_command_cause_omitted_total` cuenta los comandos que solo se aceptaron al reenviarlos sin ella.
- **La consola ya no sobrescribe el cambio de otra persona en un área, un activo, un cliente o un grupo.** Al editar el nombre, la descripción o el tipo de un área, un activo o un cliente, o el nombre o la descripción de un grupo de áreas, activos, clientes o dispositivos, se envía ahora la versión con la que se abrió el formulario. Si otra persona guardó un cambio entretanto, el guardado se rechaza con un mensaje que lo indica, y lo que escribiste se conserva en el formulario. Antes, el guardado posterior reemplazaba en silencio al anterior.
- **La creación masiva de dispositivos acepta una plantilla de token con espacios sobrantes.** El formulario de creación masiva de la consola ya recortaba la plantilla de token antes de enviarla, pero comprobaba primero el texto sin recortar, de modo que una plantilla pegada con un espacio al principio o al final se rechazaba con "El token generado no es válido". Ahora comprueba la plantilla tal como se envía.
- **Importa valores de facetas desde un archivo CSV en la consola.** En la pantalla Facetas, **Importar valores** lee un archivo CSV de hasta 10.000 filas y 5 MiB, asigna sus columnas (tipo de entidad, token de la entidad, clave de faceta, valor), muestra una vista previa de cada fila con una ejecución de prueba y luego asigna los valores en lotes de 1.000 como máximo. Una fila que repite una entidad y una clave de faceta anteriores en cualquier punto del archivo no se envía y se informa. Los resultados se pueden filtrar y descargar, las filas rechazadas se pueden reintentar por separado y el archivo de filas fallidas conserva solo las cuatro columnas de importación, de modo que una columna de credenciales del archivo de origen nunca se vuelve a escribir.
- **La consola ya puede gestionar un despliegue OTA por fases.** Un despliegue creado con cohortes muestra su plan en la página del despliegue (el tamaño de cada cohorte, si la promueve un operador y si está completada, actual o pendiente), y la tabla de dispositivos incorpora una columna Cohorte. Cuando una cohorte termina sin fallos, el despliegue se pausa como «Cohorte completada, lista para promover» y **Promover a la siguiente cohorte** libera la siguiente; el botón nombra la cohorte que se ve en pantalla, de modo que una pestaña desactualizada o un segundo operador no pueden promover dos veces. Cuando una cohorte termina con dispositivos fallidos o desconocidos, **Promover pese a los fallos** pide confirmación antes de aceptarlos, y un despliegue detenido por su límite de tasa de fallos ofrece **Anular la parada por tasa de fallos y reanudar**, con confirmación, para la parada que se ve en pantalla. Las tres acciones requieren `artifact:write`, como los demás controles del despliegue. Los recuentos de la página siguen abarcando todo el despliegue, no una sola cohorte. Los textos nuevos están en inglés y español; el chino simplificado muestra inglés en las páginas de actualizaciones hasta que se traduzca ese catálogo.
- **La consola y `@devicechain/client` pueden rechazar las solicitudes que una instancia no puede atender.** Una consola creada para una versión más reciente y conectada a una instancia más antigua enviaba solicitudes que la instancia no entendía y fallaba con un error poco claro. La negociación pide a cada endpoint GraphQL el esquema que sirve, mediante una solicitud `OPTIONS` autenticada, y comprueba cada solicitud con él antes de enviarla. Una solicitud que usa un campo o una entrada que la instancia no tiene se rechaza, no se envía nada y el error indica qué falta. No se recorta ni se reescribe nada. Una instancia anterior a v1.9.0 no ofrece este descubrimiento, por lo que no se puede confirmar su versión: la solicitud se rechaza con un error que menciona v1.9.0 y no se envía a ciegas. La consola activa la negociación solo donde puede ser una versión distinta de su instancia: con `vite dev` o en una compilación hecha con `VITE_SCHEMA_NEGOTIATION=on`. Una consola servida por su propia instancia la mantiene desactivada, de modo que un proxy que responda a `OPTIONS` no puede romperla. No se comprueban el inicio de sesión, la renovación de tokens ni las suscripciones en vivo. Para su propia aplicación, `@devicechain/client` añade `enableSchemaNegotiation` (con un `release` opcional), `disableSchemaNegotiation`, `resetSchemaNegotiation`, `SchemaSkewError` e `isSchemaSkewError`; está desactivado salvo que lo habilite y requiere el paquete par `graphql` que ya instala. Si un proxy CORS o un firewall de aplicaciones web responde a `OPTIONS` por sí mismo, el descubrimiento falla y la negociación rechaza todas las solicitudes, así que deje que `OPTIONS` llegue a los endpoints GraphQL. La aplicación de visualización `/dash` todavía no la habilita.
- **La consola ya está disponible por completo en chino simplificado.** Todas las pantallas de la consola, incluidas las páginas de actualizaciones por aire, los firmantes de actualizaciones, las facetas de exploración, los perfiles de dispositivo, los inquilinos, los miembros y las pantallas de administración, se muestran ahora en chino simplificado en lugar de recurrir al inglés.
- **Ejecutor dentro del clúster (Preview).** `dcctl executor enroll/status/unenroll` instalan, inspeccionan y retiran un ejecutor fijado por su propietario: su espacio de nombres, ServiceAccount, Deployment, RBAC, registro y un rol de solicitante sin vincular. No ejecuta nada en esta versión: las solicitudes se rechazan con `NotExecutableInThisBuild` y ninguna instancia cambia. El registro exige un archivo de custodia cuya huella coincide con la clave raíz desplegada de cada instancia (`OwnerOffline`). `dcctl install` instala dos nuevas definiciones de recursos, `ExecutorEnrollment` e `InstanceOperation`, sin desplegar el ejecutor. Como en cada versión, un dcctl anterior rechaza un clúster cuyas definiciones haya actualizado un `dcctl install` de la versión 1.9.
- **Más comandos de `dcctl` pueden imprimir JSON para scripts.** `dcctl dead-letters list`, `dcctl tenant-deletion status` y `dcctl instances list` aceptan `-o json` (la misma opción de `dcctl events failed`), y `dcctl ha verify` y `dcctl ha verify-db` aceptan `--json` (como `dcctl ha leaders`). Con cualquiera de las dos, la salida estándar contiene solo el JSON, y un `instances list` vacío imprime `[]` en lugar de una frase. Los códigos de salida no cambian: `tenant-deletion status` sigue terminando con error salvo que se alcance el objetivo, y `--expect-fail` sigue invirtiendo las comprobaciones de `ha`, con su mensaje de control negativo en stderr en modo JSON.
- **Gestiona relaciones entre entidades con dcctl.** Usa `dcctl relationship add`, `list` y `remove` para crear, consultar y eliminar relaciones tipadas mediante la API del tenant. Los comandos admiten `--instance` o `--server`, inicio de sesión con segundo factor y salida en tabla o JSON (`--json` o `--output json`). El listado admite paginación y filtros por origen, destino, tipo de relación y seguimiento.
- **dcctl negocia las capacidades de la API.** Los comandos de tenant y administración comprueban el esquema de destino antes de enviar operaciones o campos de entrada. Las funciones no compatibles indican cómo actualizar. Cada sesión conserva los resultados correctos por URL exacta y rechaza las redirecciones de detección. Las instancias recientes publican su esquema real a clientes autenticados sin habilitar la introspección anónima. Para las instancias antiguas, dcctl usa la versión registrada en el recurso Instance del clúster, localizado mediante el registro local de la instancia o `--instance`. Una versión estable v1.7.x/v1.8.x registrada permite el contrato conservador v1.7 en los endpoints correspondientes. Un rechazo de OPTIONS nunca identifica la versión: una versión desconocida sigue requiriendo detección, con indicaciones para seleccionar un registro de instancia legible o usar la detección de v1.9. Los clientes de la biblioteca core registran versiones verificadas de forma independiente con `WithLegacyEndpointVersion`; las funciones en vista previa y las que están fuera del contrato antiguo requieren la detección de v1.9. Los comandos y las opciones siguen disponibles.
- **`dcctl bootstrap --sharding`.** Un nuevo indicador elige el nivel de particionado de una instancia al instalarla. El nivel queda registrado en la instancia, y después no se puede cambiar, añadir ni quitar. `off`, el valor por defecto, genera exactamente lo mismo que dcctl generaba antes de que existiera el indicador y no registra nada. `scale` se rechaza porque aún no está disponible. Antes de usar `--sharding`, ejecute `dcctl install` para que la definición de Instance del clúster registre el campo. Si omite el indicador y define un bloque `shardGroups` propio en `--values`, funciona exactamente igual que antes, y el resumen de bootstrap y `dcctl upgrade` informan de esa configuración como `custom`.
- **Las lecturas de salud del decodificador de payloads siguen siendo rápidas en periodos largos.** El recuento de salud del decodificador (`payloadDecoderHealth`) se suma ahora en la base de datos en lugar de cargar en device-management cada fila por réplica y minuto. Una nueva migración añade dos índices a la tabla de salud del decodificador: uno sirve la lectura por perfil y el otro la depuración horaria de las filas antiguas. La respuesta no cambia, incluido qué último error se informa. La migración se ejecuta durante la actualización y retiene brevemente las escrituras en esa tabla mientras se crean los índices.
- Las eliminaciones simultáneas de un dispositivo devuelven `false` cuando otra eliminación se completa primero, y al detenerse las fuentes de eventos contabilizan las retenciones por ubicación abandonadas con `reason="shutdown"`.
- **Se informa el tamaño de la tabla de entidades eliminadas, con una alerta.** El almacén de eventos conserva una fila por cada token de entidad distinto que se haya eliminado, de modo que una entidad creada después con el mismo token no pueda leer el historial de eventos de la eliminada. Nada depura esa tabla. Ahora aparece en las series `append_only_table_bytes` y `append_only_table_rows_estimate` (`table="entity_deletion_watermarks"`, exportadas por event-management), y la nueva advertencia `EntityDeletionWatermarksLarge` se activa cuando supera 1 GiB (unos cinco millones de tokens eliminados distintos) durante una hora. Su entrada en el runbook de alertas da la consulta que nombra al inquilino responsable.
- **Historial de informes de configuración de dispositivos.** El servicio conserva internamente un historial de 200 días de los resultados de configuración de dispositivos aceptados; este historial todavía no está disponible en la API. La nueva métrica `device_configuration_reports_pruned_total{reason="orphan|retention"}` cuenta las filas eliminadas por el mantenimiento.
- **Eliminación simultánea de dispositivos.** Se corrigieron bloqueos mutuos de la base de datos al eliminar una puerta de enlace mientras se crea un dispositivo hijo y se actualizan atributos, y al eliminar una entidad mientras se restaura o copia su configuración. Las puertas de enlace con dispositivos hijos siguen rechazando la eliminación.
- La reparación de titulares de dispositivos eliminados ahora aumenta el intervalo entre comprobaciones de ausencias confirmadas desde una hora hasta siete días, reduciendo las consultas repetidas a device-management. Los mensajes del registro siguen detectando rápidamente los dispositivos creados de nuevo. Si falta un mensaje, un barrido correcto detecta la recreación en la siguiente pasada programada, en un máximo de siete días más un intervalo de barrido. Ninguna eliminación se omite permanentemente.
- **Las órdenes de parada de agentes mantienen el bloqueo al agotar los reintentos de entrega.** Si device-management no puede aplicar una orden de parada en su última entrega, genera un mensaje fallido con motivo `unprocessable` y bloquea las ejecuciones de alarmas de agentes de todos los inquilinos de ese proceso hasta su reinicio. Una vez recuperado el almacenamiento, repita la orden de parada y reinicie los procesos afectados de device-management; las órdenes aplicadas correctamente y las consultas satisfactorias al broker no eliminan este bloqueo.
- **Se informa el tamaño de la tabla de titulares de tokens de dispositivo.** El almacén de eventos conserva una fila por cada token de dispositivo que algún dispositivo activo haya tenido. Es lo que permite que una lectura por un token eliminado y vuelto a crear devuelva solo los eventos del dispositivo nuevo. Nada depura esa tabla. Ahora aparece en las series `append_only_table_bytes` y `append_only_table_rows_estimate` (`table="device_token_holders"`, exportadas por event-management). No tiene una alerta de tamaño propia: crece con la flota, así que una tabla grande suele ser una flota grande. Los dispositivos que se eliminan y se vuelven a crear una y otra vez con tokens nuevos también hacen crecer la tabla de entidades eliminadas, que vigila `EntityDeletionWatermarksLarge`, y las alertas de volumen del almacén de eventos cubren el espacio.
- **Un dispositivo recreado ya no muestra los eventos del dispositivo eliminado.** Cuando se elimina un dispositivo y se crea uno nuevo con el mismo token, la lectura de eventos por ese token (eventos, mediciones, ubicaciones, alertas, cambios de presencia, mediciones agrupadas y la vista previa de métricas calculadas, también a través de MCP) devuelve solo lo que la plataforma recibió para el nuevo dispositivo a partir del momento de la eliminación, incluidas las lecturas almacenadas en búfer con una fecha anterior. Un token eliminado que no se ha vuelto a crear sigue leyendo el historial completo de su dispositivo, como antes. Las mediciones agrupadas de un token recreado se calculan a partir de las lecturas almacenadas en lugar de los resúmenes preagregados. Esto se aplica a las eliminaciones procesadas por v1.8.0 o posterior.
- **Protección frente a ediciones simultáneas.** Las mutaciones GraphQL de device-management `updateArea`, `updateAsset`, `updateCustomer` y `updateEntityGroup` ahora aceptan el argumento opcional `expectedUpdatedAt`. Envíe el último `updatedAt` leído de la entidad para rechazar un guardado desactualizado con `STALE_WRITE`; vuelva a cargar antes de reintentar. Omitir el argumento conserva el comportamiento en el que prevalece la última escritura.
- **Preparación de operaciones de flota (Preview).** La consulta `fleetOperationReadiness` y `dcctl fleet-op readiness` indican si esta instancia puede ejecutar operaciones de flota. En esta versión informan de que el cumplimiento no está instalado y ningún tipo de operación es ejecutable, lo cual es lo esperado. Requiere `fleet:operate`; se rechazan los miembros con ámbito limitado. El plazo de vista previa comienza en v1.9.
- **Las peticiones GraphQL se analizan de forma más estricta.** Se actualiza la biblioteca GraphQL, y literales numéricos que antes se aceptaban ahora se rechazan como errores de sintaxis: un cero inicial (`01`), un punto decimal al final o al principio (`1.`, `.5`), un entero fuera de rango y un signo menos delante de algo que no es un número. Además, un documento queda limitado a 100.000 comparaciones de solapamiento de campos durante la validación. Las peticiones bien formadas no se ven afectadas.
- **La verificación de alta disponibilidad rechaza las listas incompletas de consumidores.** `dcctl ha verify` ahora se niega a evaluar una instantánea cuando la cantidad de consumidores enumerados de un flujo difiere de la indicada por el broker. El error identifica el flujo y ambas cantidades; vuelva a ejecutar el comando para recopilar una instantánea completa.
- **La escena 3D del puerto tiene una costa detrás del pueblo y todos los modelos son más detallados.** En `demos/harborpulse-unity`, la neblina desvanece ahora el bosque lejano, de modo que unos árboles más escasos y frondosos (arces, abedules, robles, abetos y pinos) se alzan sobre colinas con claros, afloramientos de granito y una cumbre contra el cielo. El pueblo tiene menos edificios, pero más detallados, con porches, molduras, chimeneas y tejados variados. Las farolas, los cargadores, los medidores, las estaciones de bombeo, el camión, los barcos, las boyas, las balizas y los muelles tienen modelos más detallados, y los muelles muestran juntas de hormigón, remates de piedra, adoquines y marcas viales. Una farola ya no se superpone con un cargador ni con un coche, y una prueba fija que ningún elemento se superponga con otro. El recuento de alarmas nocturno se lee mejor.
- **La escena 3D del puerto gana profundidad detrás del pueblo y una hora que se lee de un vistazo.** En `demos/harborpulse-unity`, unas estribaciones boscosas y una sierra intermedia se alzan ahora entre los últimos árboles y la cresta lejana, y la neblina se oscurece bajo el horizonte, de modo que el terreno retrocede en capas en lugar de terminar en una franja pálida. Un sol bajo ilumina las caras que miran hacia él y las nubes desde abajo, y la luna platea las nubes de la noche, así que el amanecer, el día, el anochecer y la noche se distinguen incluso desde la vista general, que mira al norte. La vista general 9:16 tiene un nuevo encuadre que incluye el cielo y el amanecer, la costa abierta tiene una orilla rocosa e islotes, los afloramientos de granito parecen roca, una farola encendida tiene un resplandor suave y redondo, y el camión de servicio es un modelo completo de cerca. Una compilación para navegador toma ahora sus opciones de vista previa de la dirección de la página (`?hp-offline&hp-start-hour=17.3`) y dibuja el agua correctamente.
- **La escena 3D del puerto muestra por qué la plataforma envió un comando.** En `demos/harborpulse-unity`, un día en vivo dice ahora «The platform tells it to derate» cuando el comando de reducción del cargador recalentado lleva la causa que la plataforma registró con él y esa causa nombra la alarma que está en pantalla; la escena une ambos por la clave de la alarma, el registro del dispositivo y la hora de la detección, y cuando falta el vínculo, o el lector no tiene permiso para verlo, se queda en «Alarm raised». El panel de la bomba durante la tormenta muestra `start-pump sent` del mismo modo. `hack/harborpulse-rig.sh` comprueba que el token que sirve cada simulador puede leer la causa de un comando, y un reproductor iniciado con `-hp-control-no-cause` muestra la alternativa a propósito. La escena incorpora además un peñasco de granito lo bastante alto para ser un punto de referencia desde la vista general, chubascos sobre la lejanía en la vista general 9:16, un cielo de tormenta más calmado, una terminal de ferris más corta que ya no oculta un cargador, vistas de los pozos de bombeo sin nada aparcado delante y con sus etiquetas de nivel fuera del rótulo, y una mañana que ya no se ve azul.
- **La escena 3D del puerto gana un director.** `demos/harborpulse-unity` dibuja ahora Port Alder como un puerto compacto en herradura que cabe en una sola cámara, con cada dispositivo en una posición dibujada que conserva su muelle y su orden a lo largo del frente marítimo. Un director sigue el día simulado: lleva la cámara a cada momento de la historia, escribe un subtítulo breve y construye en pantalla una cadena de causa y efecto, y cada paso aparece solo cuando se ha leído el registro de la propia plataforma que lo respalda. Un paso que no puede confirmar nunca se muestra como hecho; la escena dice menos. La navegación libre toma la cámara en cualquier momento y la devuelve tras 20 segundos sin actividad. Una ejecución con un defecto sembrado muestra un aviso rojo TEST CONTROL, y una escena construida para otra disposición apaga el director y lo indica. Cada ejecución en vivo se graba en archivos que pueden comprobarse después. La escena sigue limitándose a leer.
- **La escena 3D del puerto se ilumina como un día nublado junto al mar, y sus noches se pueden leer.** En `demos/harborpulse-unity`, la luz del día es suave y cubierta, con sombras tenues y neblina en las colinas. La tormenta oscurece el cielo pero deja el pueblo legible, y de noche la luz ambiente se mantiene en un nivel de luna. Las ventanas, las farolas, las luces de los muelles y las balizas de alarma brillan ahora también en el reproductor compilado, no solo en el editor; antes podían verse apagadas allí. El pueblo tiene sus propias farolas, y sus ventanas y farolas del este se apagan durante el corte del alimentador este. Los subtítulos se leen en todas las páginas. Una nueva opción `-hp-frame-stats` registra los tiempos de fotograma.
- **La escena 3D del puerto muestra más cielo, un bosque más tupido y un tiempo que se ve llegar.** En `demos/harborpulse-unity`, las colinas detrás del pueblo son más bajas y las cámaras de la historia están más bajas, de modo que el cielo aparece en cada plano. El cielo sigue el reloj del escenario y la tormenta: un amanecer rosado, nubes de buen tiempo al mediodía, nubes que crecen durante las tres horas previas a la tormenta, un cielo cubierto y oscuro con lluvia, y un cielo que se despeja después hasta una noche con luna. El amanecer, el anochecer y la tormenta están iluminados para que el pueblo siga siendo legible. El bosque es más denso, con unas tres quintas partes de coníferas verdes y colores de otoño en el resto, con prados y repisas de granito encajadas en la ladera, y se desvanece en la neblina ante una cresta lejana. Los edificios, coches y barcos cercanos a la cámara ganan puertas, marcos de ventana, porches, ruedas y herrajes, mientras que los lejanos siguen siendo sencillos.
- **La tormenta de la escena 3D del puerto tiene ahora un atardecer, y su amanecer ya no es oscuro.** En `demos/harborpulse-unity`, la tormenta que cubre todos los anocheceres del escenario se detiene antes del horizonte occidental: una franja de cielo iluminado asoma bajo las nubes durante la última hora del día, el sol bajo alcanza el pueblo a través de ella y la lluvia que tiene delante queda iluminada a contraluz. El amanecer es más luminoso y los muelles ya no se ven azules por la mañana. Las colinas detrás del pueblo tienen una línea de árboles con colores de otoño en manchas, las lajas de granito tienen bloques sueltos y algunos peñascos, la vista general 9:16 mantiene el puerto en el centro del encuadre y la cuadrilla de la farola llega con una plataforma elevadora. La lluvia lejana cae en cortinas en lugar de formar un único campo gris. Una imagen fija puede generarse como comprobación de aspecto con buen tiempo (`-hp-stills-weather fair`), que queda rotulada como tal y no cambia nada de lo que informa el escenario.
- **La escena 3D del puerto se lee bien en un encuadre 9:16 y su terreno ya no deja ver la cuadrícula.** En `demos/harborpulse-unity`, la vista en corte de un pozo de bombeo tiene una versión propia para el encuadre vertical, de modo que las etiquetas de nivel quedan entre el rótulo y el panel de eventos en lugar de debajo de ellos. La vista general 9:16 incorpora su propio peñasco de granito en el bosque al oeste de la Dársena, y ambos peñascos muestran ahora juntas estrechas en lugar de franjas claras y oscuras. Los colores del terreno se funden a lo largo del relieve en vez de cambiar de un cuadrado a otro. Los chubascos solo se dibujan desde un punto de vista elevado, donde se leen como lluvia; desde la vista general 16:9 y desde los muelles las sierras lejanas vuelven a desvanecerse de manera uniforme. La vista amplia de la página del conector recalentado ya no mira por encima del techo del ferri atracado.
- **La escena 3D del puerto gana un pueblo de verdad.** En `demos/harborpulse-unity`, la cuadrícula uniforme de casas idénticas detrás del puerto se sustituye por un pueblo más pequeño y rústico: una calle principal que sube tierra adentro con algunas calles laterales, una mezcla de casas de tablas de madera, bloques de tiendas de ladrillo, cobertizos junto al agua, una posada y una iglesia blanca, con árboles entre ellos y colinas boscosas con colores de otoño al fondo. El frente marítimo gana un parque con césped y un quiosco, muelles de madera sobre pilotes y barcos amarrados. El agua del puerto muestra ahora un reflejo suave que se desvanece con la distancia en lugar de un espejo nítido, con un tono turquesa en las aguas poco profundas junto a los muelles. Los muelles y todos los dispositivos siguen exactamente donde estaban.
- **Gráficos de largo alcance más rápidos para los datos nuevos.** Las lecturas de medidas agrupadas por un número entero de horas (1 h, 2 h, 6 h, 24 h, etc.) se sirven ahora desde un segundo rollup de una hora construido sobre el rollup existente de un minuto, de modo que un gráfico largo lee unos cientos de filas preagregadas por métrica en lugar de decenas de miles. La actualización crea el nuevo rollup sin rellenarlo, por lo que la mejora se aplica a los datos escritos después de la actualización; las lecturas anteriores se leen del nivel de un minuto exactamente como antes, y los resultados son los mismos en ambos casos. Las horas que el rango corta a medias, incluida la hora en curso, también se leen del nivel de un minuto. El rollup se refresca en segundo plano cada minuto y se conserva durante la misma ventana de retención que el rollup de un minuto. El interruptor existente `DisableRollupReads` desactiva ambos niveles.
- **Esquema de configuración de instancia por versión.** Descargue `instance-config.schema.json` de la versión de destino exacta en GitHub, o ejecute `dcctl config-schema` de esa versión para obtener el mismo esquema sin conexión. Se genera a partir de los tipos de configuración de Go, valida la estructura del documento de instancia y rechaza claves desconocidas. El arranque sigue comprobando las restricciones semánticas y entre campos.
- **Las integraciones de entrada siguen aceptando mensajes ascendentes mientras device-management no está disponible durante un tiempo breve.** event-sources recuerda durante cinco minutos a qué dispositivo corresponde el identificador de una integración. Antes de esta versión, cuando esa respuesta tenía más de cinco minutos, un mensaje ascendente que llegaba mientras no se podía contactar con device-management se respondía con `503`. Ahora se usa la última respuesta durante un máximo de 15 minutos desde que device-management la dio, el mismo límite que ya usan las consultas del token de la integración y del decodificador de carga útil, y el mensaje se acepta y se procesa cuando device-management vuelve. device-management sigue comprobando la integración, el dispositivo y su identificador en cada evento, de modo que un dispositivo que entretanto se haya deshabilitado, eliminado o asignado a otro identificador se rechaza en ese momento. Una respuesta que device-management realmente da, como un identificador no registrado o una integración deshabilitada, nunca se sustituye por otra anterior. Los informes de mensajes descendentes (confirmaciones de un servidor de red) no se incluyen: se siguen respondiendo con `503` mientras no se puede consultar a device-management, como antes. Una solicitud atendida de este modo se cuenta en el nuevo resultado `resolve_stale` de `integration_requests_total`, además de su resultado habitual, y event-sources registra una advertencia.
- **Una segunda y una tercera instancia local reciben su propio puerto MQTT.** En un clúster kind local, `dcctl bootstrap` asigna ahora a cada instancia nueva el primer puerto de nodo MQTT libre que publica el clúster. Antes solo la primera instancia era accesible desde el host y las demás quedaban accesibles solo dentro del clúster. Un clúster creado con esta versión publica tres: los puertos de host 1883, 1884 y 1885. Un clúster creado antes publica solo el 1883 y se comporta como hasta ahora. El resumen del bootstrap muestra la dirección (`MQTT: ssl://localhost:<puerto>`). El puerto se registra con la instancia, de modo que `dcctl sim create` lo utiliza. Una actualización conserva el puerto que ya tiene la instancia y nunca le asigna uno nuevo. Crear un clúster local nuevo necesita ahora que los puertos de host 1884 y 1885 estén libres, además del 1883; `dcctl preflight` lo comprueba.
- **Un broker MQTT caído ya no bloquea a un cliente ocupado.** El origen de eventos MQTT externo, el host Sparkplug y el enlace ascendente del agente de borde hacia la nube ahora limitan cada escritura de socket hacia el broker a 5 segundos y declaran de forma explícita su keep-alive de 30 segundos. Antes, un publicador que había llenado su socket frente a un interlocutor que dejó de responder podía quedarse bloqueado indefinidamente en esa escritura sin detectar la conexión muerta; ahora cierra la conexión y se reconecta.
- **Despliegues OTA por cohortes.** Puedes definir canarios con nombre seguidos de porcentajes acumulativos. Los destinos SKIPPED y CANCELLED no bloquean la siguiente cohorte. Los resultados fallidos o UNKNOWN pausan el avance con `COHORT_FAILED`; `promoteUpdateRollout` exige `overrideFailure: true` para aceptarlos y rechaza destinos pendientes o en curso. UNKNOWN conserva su plaza de concurrencia. Si UNKNOWN llena el límite antes de enviar todos los canarios, el despliegue se pausa de forma visible: resuelve o abandona el intento y reanuda para terminar los canarios pendientes. Hay promoción manual. La política opcional de tasa de fallos usa una ventana y una muestra mínima; el umbral es 1–99 y solo se pausa al superarlo. La anulación conserva las pruebas de la parada numerada; cambiar solo la versión de un intento reconocido no vuelve a detenerlo.
- **Activar después de actualizar.** La configuración fija `infrastructure.updateManagement.rolloutSafety` vale false por defecto. Con ella desactivada, crear o iniciar un despliegue con cohortes o `failureRate` devuelve `ROLLOUT_SAFETY_DISABLED`. Mantenla desactivada durante la actualización de versión. Después de actualizar **todas las réplicas de update-management**, actívala en una **actualización de configuración separada**. Las réplicas antiguas omiten las barreras de cohortes y tasa, permiten reanudar estas pausas de forma ordinaria y no pueden representar los nuevos motivos. Los despliegues ordinarios conservan sus valores y límites de concurrencia.
- **Una entrega retenida se abandona a los 15 minutos.** Mientras la ubicación de shard de un inquilino no está disponible, event-sources retiene sus entregas de último intento. Un inquilino cuya ubicación nunca vuelve ya no las retiene indefinidamente: tras 15 minutos la entrega queda sin confirmar, como sin la retención. El nuevo contador `total_placement_hold_fallback{source,reason}` cuenta las entregas no retenidas porque la retención estaba llena (`full`) o que dejaron de retenerse (`expired`). Un mensaje retenido y recuperado tampoco se cuenta ya dos veces como entrante.
- **Los streams replicados siguen replicados durante un reinicio del broker.** En un broker en clúster, un servicio cuya conexión estaba brevemente entre servidores durante un reinicio escalonado trataba el broker como un único servidor. Registraba que todos los streams y buckets se estaban creando con una sola réplica, y un stream o bucket creado en ese momento podía crearse con una réplica y quedarse así. Ahora un servicio conserva la topología que vio por última vez mientras se reconecta, y antes de haber visto ninguna usa el número de réplicas configurado. Un stream o bucket que ya está por debajo del número configurado lo señala la alerta `JetStreamNotReplicatedAsConfigured`, y `dcctl broker reconcile` lo eleva.
- **Los eventos resueltos esperan a que vuelva la ubicación de particiones.** Con los grupos de particiones activados para resolved-events, un evento entrante cuyo último intento de entrega tuvo algunos de sus eventos resueltos rechazados porque la ubicación de particiones no estaba disponible se retiene y se completa cuando esta vuelve, en lugar de registrarse como mensaje fallido sin que sus eventos se resuelvan nunca. Solo se vuelven a publicar los eventos resueltos rechazados; los que ya estaban almacenados no se repiten. Esto se aplica por igual a los eventos entrantes, a los envíos de integraciones y a los sobres de eventos resueltos. La nueva métrica `resolve_placement_held_sources` indica cuántas entregas finales están retenidas, y `resolve_placement_hold_fallback_total` cuenta, por motivo, las que la retención no aceptó o dejó de retener. La retención tiene un tamaño limitado y retiene un mensaje como máximo 15 minutos, tras lo cual lo deja sin confirmar como antes; dura durante la ejecución actual, y al detenerse, los mensajes retenidos quedan sin confirmar y las entregas agotadas aún pueden registrarse. Las actualizaciones graduales no requieren cambios de protocolo ni de configuración; los pods antiguos conservan el comportamiento anterior.
- **Las estadísticas de salud de reglas ya no se acumulan para versiones de perfil reemplazadas.** Cada publicación de un perfil de dispositivo inicia nuevas estadísticas de disparo para las reglas de esa versión. Las estadísticas de la versión reemplazada se conservaban indefinidamente, aunque la vista de salud de reglas solo las muestra si una reversión vuelve a activar esa versión. El procesamiento de eventos ahora las elimina cuando ha pasado una semana desde el último disparo de las reglas de esa versión. Las estadísticas de la versión activa de un perfil se conservan siempre, al igual que las de un perfil que no tiene versión activa. Si una reversión vuelve a activar más tarde una versión cuyas estadísticas se eliminaron, la salud de reglas lo indica: el nuevo campo `historyRemovedAt` de `RuleHealth` da la hora de la eliminación, la consola muestra un aviso, y los recuentos de disparos y la hora del último disparo solo incluyen los disparos desde ese momento. Una sola réplica ejecuta la limpieza cada hora, en lotes acotados por tenant. La nueva métrica `devicechain_eventprocessing_rule_stat_sweep_deleted_total` cuenta las filas que elimina.
- **Inicio del procesamiento de eventos con particiones.** Al iniciar una instancia con particiones, ya no se intenta eliminar un consumidor heredado sin particiones en el flujo de un grupo distinto de cero, lo que elimina la advertencia de permisos del broker. El grupo cero sigue eliminando su consumidor heredado durante una actualización en la instancia existente. Los permisos del broker no cambian.
- **La eliminación de tenants se completa con resolved-events particionado.** En una instancia con resolved-events en grupos de particionado, eliminar un tenant ya no se queda bloqueado en "no DETECT engine answered" con el token del tenant retenido. event-processing eliminaba el tenant de todos los grupos, pero el broker rechazaba sus respuestas, porque enviaba una respuesta por grupo sobre una sola conexión y el broker acepta una por solicitud. Ahora envía una única respuesta que cubre todos los grupos. Durante una actualización gradual, una eliminación puede seguir bloqueada mientras coexisten pods antiguos y nuevos de user-management o event-processing, y se reanuda en la siguiente pasada del coordinador una vez actualizados ambos; después se completa tras las esperas habituales de estabilización y de retención del token. Las instancias sin resolved-events particionado no se ven afectadas.
- **`dcctl sim create` ya no apunta una simulación al broker de otra instancia.** En un clúster local donde todos los puertos MQTT publicados pertenecen a otras instancias, una instancia solo es accesible dentro del clúster. `dcctl sim create` para esa instancia recurría antes al primer puerto MQTT del clúster, que es el broker de otra instancia. Ahora se niega e indica que se pase `--mqtt-broker`. El registro de la instancia indica ahora explícitamente cuándo una instancia no tiene puerto MQTT. Para una instancia registrada antes de esta versión, dcctl consulta en su lugar los Services de la propia instancia, de modo que una instancia que sí tiene el primer puerto sigue resolviéndose a él.
- **Las excavadoras de la demo Site Pulse tienen su propio perfil de dispositivo.** Una excavadora no transporta carga ni tiene neumáticos, pero compartía el perfil de equipo con las cargadoras y los camiones, por lo que la tabla de la flota declaraba para ella carga útil y presión de neumáticos y mostraba "sin datos en el rango". El tipo excavadora usa ahora un perfil propio que declara solo combustible, temperatura del motor y horas de motor, con los mismos comandos y las reglas de bajo combustible y sobrecalentamiento, pero sin regla de neumáticos, de modo que esas celdas indican "no informa de esto". Una instancia de demo creada por una versión anterior conserva el perfil compartido hasta que se vuelva a crear.
- **El rechazo de una solicitud de vinculación de inicio de sesión único ahora se audita.** Al rechazar una solicitud pendiente con `rejectSsoLinkRequest` se registra una fila `identity_link_rejected` en el registro de auditoría de autenticación, igual que la aprobación registra `identity_linked`. La fila indica el administrador que la rechazó, la solicitud y la conexión; nunca registra el correo ni el subject que declaró el proveedor. Rechazar una solicitud que ya no existe, o que se deniega, no registra nada.
- **Un solo término en chino simplificado por concepto.** La consola y la documentación en chino simplificado usan ahora las mismas palabras para las mismas cosas: perfil de aprovisionamiento, artefacto, faceta, despliegue gradual, proveedor de identidad, secreto y firmante ya no varían entre pantallas ni entre la consola y la documentación.
- **MQTT con cierre ante fallos puede escribir sobres de eventos (opcional, desactivado por defecto).** Con `infrastructure.nats.mqttFailClosed` activado, `event-sources` escribía hasta ahora cada mensaje MQTT por separado en el stream inbound-events, aunque los [sobres de eventos](#event-envelopes) estuvieran activados. El nuevo ajuste de `event-sources` `capture.mqttStoreEnvelopes` le permite escribirlos como sobres, lo que reduce el trabajo del broker por evento. Solo tiene efecto cuando `infrastructure.nats.mqttFailClosed` e `infrastructure.messaging.envelopes.inbound` están ambos activados, y no cambia nada en otro caso. Un mensaje se confirma en el almacén de mensajes MQTT solo cuando su sobre está en el stream inbound-events, y un mensaje reentregado se reconoce y no se escribe dos veces. Actívelo en un `dcctl upgrade` aparte, después de que todos los pods de `event-sources` ejecuten esta versión, en `functionalAreas.event-sources.config.capture.mqttStoreEnvelopes` en un archivo de valores (consulte [Ajustar una instancia](./tuning-an-instance.md)). Cuando el ajuste cambia, `event-sources` espera a que se resuelvan los mensajes que tenía en curso con el ajuste anterior antes de escribir de la nueva forma.
- **Simulador Harborpulse:** Añade cargadores MQTT y autenticados con certificados, una alarma de conectividad, configuración deseada y notificada, y farolas que consultan comandos por HTTP. Incluye verificaciones de los datos de la plataforma y controles negativos aislados. Se conserva la flota original y sus ciclos de actualización y corte eléctrico.
- **Progreso del despliegue por cohorte.** La API GraphQL de gestión de actualizaciones ahora muestra los recuentos de resultados de cada cohorte y permite filtrar los dispositivos de destino por cohorte. Los recuentos siguen las mismas reglas que el progreso global del despliegue.
- **Aislamiento de tenants y preguntas MCP en Harborpulse.** El banco de validación comprueba dos tenants con identificadores de dispositivo iguales y compara las respuestas del servicio MCP real con los datos de GraphQL. Registra un cliente OAuth público por ejecución y usa el token de usuario PKCE de un miembro del tenant. Dos defectos provocados demuestran que cada comprobación puede fallar de forma independiente.
- **Propuestas de comandos de agentes (Preview).** Los comandos incorporan los campos aditivos `causeDetails` y `proposal`. El campo heredado `cause` conserva su significado de detección y es nulo para un comando creado por una propuesta de agente.

- MCP añade la herramienta estática `propose_command` bajo `act:commands`, solo cuando están activadas las acciones MCP e `infrastructure.mcp.commandProposals`. `get_proposal` y `list_my_proposals` aceptan `area: "command-delivery"`; omitirlo conserva el comportamiento de alarmas. Las sesiones MCP actualizan su lista de herramientas después de activar la clave. La bandeja Aprobaciones añade una pestaña Comandos con paginación propia, argumentos JSON completos, encarnación, impacto, huella de definición, plazo, cuenta atrás y justificación como texto no confiable. Aprobar envía solo el token y el resumen mostrado y pone el comando en la cola habitual; puede esperar mantenimiento, caducar o fallar, y nunca recupera uno ya enviado.
- Command-delivery lee los datos del dispositivo con el token del solicitante: agentes que proponen y personas que aprueban necesitan `device:read`; sin él se rechaza con `FORBIDDEN`. Los límites son 200 propuestas por hora por cliente e independientemente por persona y 20 abiertas por cliente; la política puede reducirlos. El agente sigue su propuesta ejecutada mediante `get_proposal(area: "command-delivery").resultRef`; `list_commands` no muestra su propuesta de origen.

- Command-delivery ofrece `proposeCommand`, `approveAgentProposal`, `rejectAgentProposal`, `agentProposal` y `agentProposals` (Preview). El ajuste `infrastructure.mcp.commandProposals` está desactivado por defecto; mientras lo esté, se rechazan las propuestas y las aprobaciones. Actívelo solo mediante una actualización independiente después de completar el despliegue de la versión: los pods de una versión anterior rechazan la clave. El servicio de autorización del broker concede a command-delivery el nuevo permiso de lectura del flujo de desactivación de agentes; complete el despliegue, incluido dicho servicio, antes de activarlo.
- Cada propuesta se dirige a un único dispositivo. Las personas la aprueban en la consola sobre una instantánea de la definición de comando publicada del dispositivo, que se vuelve a comprobar al aprobar. Esta comprobación actualizada no es un bloqueo entre servicios: un cambio remoto posterior aún puede coincidir con la confirmación. El plazo de entrega del comando queda fijado al proponerlo: una hora por defecto y un máximo de 24 horas.
- Los valores predeterminados de la plataforma son 50 ejecuciones por hora por cliente de agente e independientemente por persona, 100 propuestas abiertas y 1000 propuestas por hora por inquilino, y 200 ejecuciones por hora por inquilino; las simulaciones (dry runs) tienen su propio límite de 60 por minuto por cliente de agente y por persona. Las propuestas decididas y sus claves de solicitud se conservan 30 días; el `requestId` del agente se recuerda durante ese plazo. Una desactivación del inquilino finaliza las propuestas pendientes y cancela los comandos de agentes que siguen esperando (en cola, retenidos o aparcados); no puede recuperar uno ya enviado.

### v1.8.0 — las ventanas de mantenimiento retienen los avisos de alarma y las entregas resisten las interrupciones {#v180-upgrade}

`v1.8.0` es una actualización in situ desde `v1.7.x`: `dcctl install` para el clúster y después un `dcctl upgrade` para cada instancia, como se describe en [Actualizaciones graduales](#rolling-upgrades). Usa **el `dcctl` de v1.8.0 para ambos comandos** con OpenTofu o Terraform 1.10 o posterior, y proporciona a `dcctl install` los ajustes del clúster, como `--cluster`, `--kube-context`, `--ha`, `--compact` y `--database-node-selector`, además de `--version`. Se añaden seis migraciones, que se ejecutan en el primer arranque de cada servicio: dos en `device-management`, dos en `notification-management`, una en `event-management` y una en `event-processing`. Cada una puede volver a ejecutarse tras un fallo. Crean tablas y añaden columnas que pueden quedar vacías, y no se añade ninguna clave de configuración, de modo que los pods de `v1.7.x` siguen funcionando junto a los de `v1.8.0` mientras avanza la actualización. La supresión de alarmas solo se aplica por completo cuando `device-management` y `notification-management` han terminado de desplegarse, como se describe en [Antes de actualizar a v1.8.0](#v180-before-upgrade). La actualización solo avanza: no hay vuelta atrás a `v1.7.x`. Si vienes de `v1.7.0`, `update-management` añade también la migración de `v1.7.1`, y también se aplican las [notas de `v1.7.1`](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.1). Si vienes de `v1.6.x` o anterior, actualiza antes a través de cada versión menor intermedia, incluida `v1.7.x`, siguiendo las notas de cada versión con su propio `dcctl` y su `--version`: saltarse una versión menor no es una ruta probada (consulta [Rutas de actualización admitidas](./versioning-policy.md#upgrade-paths)). Para `v1.7.0`, son las [notas de `v1.7.0`](#v170-upgrade).

```bash
dcctl install local --version v1.8.0
dcctl upgrade local devicechain --version v1.8.0 --dry-run
dcctl upgrade local devicechain --version v1.8.0
```

Se aplica la [política de versiones y actualizaciones](./versioning-policy.md). Esta versión añade interfaces cubiertas y no elimina ni declara obsoleta ninguna. Ocultar el historial de notificaciones de las alarmas derivadas de geocercas a los miembros sin `location:read` es una corrección de seguridad permitida por la política; los demás rechazos y cambios de comportamiento nuevos se indican en [Antes de actualizar a v1.8.0](#v180-before-upgrade). Los valores de Helm y la configuración de instancia quedan fuera de la compatibilidad cubierta.

Lo más destacado: las ventanas de mantenimiento de alarmas, declaradas sobre dispositivos, tipos de dispositivo, grupos, áreas, activos, clientes o todo un inquilino, marcan como suprimidas las alarmas generadas bajo ellas y retienen sus avisos hasta que termina la ventana; la consola permite iniciarlas y gestionarlas, y MCP las muestra. La notificación de un cambio de estado de una alarma se registra junto con el propio cambio y se envía desde ese registro en cuanto el broker la acepta, aunque haya una caída del broker o un reinicio de `device-management`, y las eliminaciones de dispositivos y los cambios de autorización responden antes mientras el broker no es accesible. Con los grupos de particiones de NATS, los mensajes capturados esperan a que vuelva la ubicación de particiones, las suscripciones en vivo siguen los cambios de ubicación y `resolved-events` se puede particionar en una instalación nueva. Los eventos almacenados después de eliminar una entidad ya no se vuelven a asociar a una entidad recreada con el mismo token. El escenario de simulador `harborpulse` ejercita ahora entregas salientes, LoRaWAN, campañas de flota, actualizaciones firmadas y una ventana de mantenimiento, y su verificador evalúa cada una.

Novedades:

- [Antes de actualizar a v1.8.0](#v180-before-upgrade)
- [Novedades de v1.8.0](#v180-whats-new)

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

## Versiones anteriores {#older-releases}

Cuando se añade una nueva línea de versiones menor, la línea más antigua documentada completa pasa aquí como una entrada breve. Cada entrada enlaza con las notas completas en la documentación de esa versión, tal como estaban en su publicación, y con su versión en GitHub. Todas las versiones aparecen en [GitHub Releases](https://github.com/devicechain-io/devicechain/releases).

### v1.7.0 — las eliminaciones llegan a todos los servicios y las sesiones resisten las interrupciones {#v170-upgrade}

<a id="v171-upgrade"></a><a id="v171-before-upgrade"></a><a id="v171-whats-new"></a><a id="v171-rollout-identity"></a><a id="v170-before-upgrade"></a><a id="v170-whats-new"></a><a id="v170-deletion-announcements"></a><a id="v170-deleted-anchor-shares"></a><a id="v170-deleted-device-alarms"></a><a id="v170-deleted-device-commands"></a><a id="v170-deleted-device-state"></a><a id="v170-deleted-device-updates"></a><a id="v170-deleted-entity-anchors"></a><a id="v170-shard-groups"></a>

Notas completas: [documentación de v1.7](/1.7/deployment/releases-and-upgrades#v170-upgrade). Versión en GitHub: [v1.7.1](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.1), [v1.7.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.7.0).

### v1.6.0 — permisos más estrictos y dispositivos conectados durante recargas del broker {#v160-upgrade}

<a id="v160-before-upgrade"></a><a id="v160-values-commands"></a><a id="v160-broker-reload"></a><a id="v160-detection-stream-location"></a><a id="v160-solar-campaign-position"></a><a id="v160-action-authorities"></a><a id="v160-rule-ceiling"></a><a id="v160-review-deleters"></a><a id="v160-no-escalation"></a><a id="v160-device-relationship-refused"></a><a id="v160-new-relationship-event"></a><a id="v160-device-uplink-guessing"></a><a id="v160-http-credentials"></a><a id="v160-credential-resupply"></a><a id="v160-signing-key-follow"></a><a id="v160-nginx-snippets-off"></a><a id="v160-ops-port"></a><a id="v160-destroy-instance-name"></a><a id="v160-mcp-memory"></a><a id="v160-mcp-discovery"></a><a id="v160-api-checks"></a><a id="v160-command-holds"></a><a id="v160-campaign-planning"></a><a id="v160-tenant-fairness"></a><a id="v160-escalation-clock"></a><a id="v160-sparkplug-limits"></a><a id="v160-entity-attributes"></a><a id="v160-nats-shard-groups"></a><a id="v160-backup"></a><a id="v160-console-dashboards"></a><a id="v160-console-tab-session"></a><a id="v160-authoring-tools"></a><a id="v160-npm-deprecations"></a><a id="v160-decoder-redelivery-budget"></a><a id="v160-invalid-failures-name-the-device"></a><a id="v160-agent-metrics"></a><a id="v160-other-changes"></a><a id="v160-going-back"></a>

Notas completas: [documentación de v1.6](/1.6/deployment/releases-and-upgrades#v160-upgrade). Versión en GitHub: [v1.6.0](https://github.com/devicechain-io/devicechain/releases/tag/v1.6.0).

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
