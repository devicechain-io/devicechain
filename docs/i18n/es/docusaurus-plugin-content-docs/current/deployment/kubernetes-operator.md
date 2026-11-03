---
sidebar_position: 3
title: Despliegue y operador
---

# Despliegue y operador

DeviceChain se despliega en dos capas, y cada una la coloca un comando de `dcctl`:

- **`dcctl install`** prepara el **clúster**. Instala los requisitos previos que comparten todas
  las instancias, más el **operador** de Kubernetes (construido con controller-runtime) y sus
  definiciones de recursos. No sabe nada de ninguna instancia concreta.
- **`dcctl bootstrap`** crea una **instancia**. Escribe un recurso **`Instance`** de ámbito de
  clúster que declara lo que va a construir, y el **chart de Helm** renderiza las cargas de
  trabajo de esa instancia.

El operador observa el recurso `Instance`. Los inquilinos no forman parte de su trabajo: son
registros de base de datos del plano de control (consulta
[Recursos personalizados](#custom-resources)).

:::note Estado
Disponible: el chart de Helm renderiza las cargas de trabajo y la configuración por servicio, y
`dcctl bootstrap` y `dcctl upgrade` gestionan el ciclo de vida de una instancia. El operador
observa el recurso `Instance` y escribe en su [estado](#instance-status) lo que observa de las
cargas de trabajo de la instancia. Su otra única tarea es en un clúster
instalado con [`--backup-snapshot-class`](./bootstrap.md#snapshot-base-backups): cada diez
minutos borra los respaldos base como instantáneas de volumen de las bases de datos que han
salido de su ventana de recuperación, cosa que CloudNativePG no hace. Planificado, sin empezar:
los overlays de Kustomize por entorno.
:::

`kubectl get dci` responde «¿está en marcha, y en qué versión?» sin leer las cargas de trabajo:

```text
NAME   PROVIDER   CLUSTER           PROFILE   REQUESTED   OPERATION   READY   RUNNING   AGE
prod   local      kind-devicechain  default   v1.0.3      Ready       True    v1.0.3    12d
```

## Desplegando con Helm

El chart en `deploy/helm/devicechain` renderiza:

- un Deployment y un Service por cada área funcional habilitada;
- los ConfigMaps de configuración por servicio;
- el Secret de configuración de la instancia. Lleva credenciales de persistencia, y por eso es
  un Secret y no un ConfigMap.

Cada pod expone `/healthz` (liveness) y `/readyz` (readiness), de modo que un servicio que no
está listo se mantiene fuera de rotación.

Eliges qué servicios ejecutar con un perfil con nombre o con un conjunto explícito:

| Perfil | Áreas funcionales |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing, update-management — el sistema estándar, y a lo que resuelve un perfil sin establecer |
| `full` | todo lo que incluye esta compilación: `default`, más `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest`, `lwm2m-ingest` — las áreas que `default` deja fuera porque cada una conlleva una decisión que conviene tomar deliberadamente (una clave de proveedor de pago, una superficie de salida, una API orientada a agentes, un transporte de dispositivos Sparkplug B o LwM2M que abre su propio puerto de entrada) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

### La clave raíz del almacén de secretos {#root-key}

Todos los perfiles requieren la **clave raíz del almacén de secretos** de la instancia.
`user-management`, que se ejecuta en todos los perfiles, sella con ella la clave que firma los
tokens de inicio de sesión. Las áreas que almacenan credenciales de integración también sellan
las suyas con ella. Sin la clave, el chart falla el renderizado en lugar de dejar que
`user-management` entre en crash-loop.

Genera un valor (`openssl rand -base64 32`), consérvalo y pasa el **mismo** valor en cada
instalación y actualización. Una clave nueva vuelve ilegibles los secretos ya almacenados bajo
la anterior, e impide que nadie inicie sesión. `dcctl bootstrap` acuña esta clave y la deposita
en escrow por ti; solo tienes que suministrarla tú cuando manejas el chart directamente.

```bash
DC_ROOT_KEY="$(openssl rand -base64 32)"   # genérala UNA vez, y consérvala

helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"

# Ejecuta un conjunto menor de servicios. Todos los perfiles necesitan la clave
# raíz, también los más pequeños.
helm install dc deploy/helm/devicechain --set profile=telemetry \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"
```

### Instalar una versión publicada {#released-version}

Para instalar una versión publicada, fija la etiqueta de imagen a una versión. Las imágenes
publicadas son públicas en `ghcr.io/devicechain-io`, así que no hace falta construir nada
localmente. Sustituye `<version>` por una etiqueta realmente publicada; la
[página de versiones](https://github.com/devicechain-io/devicechain/releases) las lista.

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

[Versiones y actualizaciones](./releases-and-upgrades.md) explica el modelo de versionado y el
procedimiento de actualización. Cómo actualizas depende de cómo se creó la instancia:

- **Una instancia que arrancaste con bootstrap** requiere dos comandos. `dcctl install` mueve el
  operador, que pertenece al clúster. `dcctl upgrade` mueve el bróker y el almacén de eventos de
  la instancia, su documento de configuración y su versión desplegada, que pertenecen a la
  instancia. El operador no forma parte del chart, así
  que algo externo al chart tiene que moverlo.
- **Una instancia gobernada solo desde el chart** requiere `helm upgrade`, con tus valores
  [trasladados a mano](./releases-and-upgrades.md#chart-only-upgrade).

### Reglas de selección de servicios {#selection-rules}

`user-management` y `device-management` son el núcleo requerido. `event-management`,
`device-state` y `command-delivery` son opcionales de forma independiente. El chart **falla el
renderizado** si una selección omite un servicio del núcleo requerido o una dependencia dura de
un servicio habilitado, de modo que una topología rota se detecta en el momento de la
instalación, no después de que los pods entren en crash-loop. Los valores se validan contra el
`values.schema.json` del chart en el momento de aplicarlos.

## Recursos personalizados {#custom-resources}

`dcctl install` define dos recursos personalizados de ámbito de clúster:

- **`Instance`** (`instances.core.devicechain.io`, nombre corto `dci`) — uno por instalación,
  que declara la identidad y la configuración de la instancia.
- **`InstanceConfiguration`** (`instanceconfigurations.core.devicechain.io`, nombre corto `dcic`)
  — un recurso que puede contener un documento de configuración de instancia. La definición se
  instala, pero `dcctl bootstrap` no crea ninguno y el operador no lo observa. Cada pod lee la
  configuración de la instancia del Secret de configuración de instancia montado en él.

[`dcctl install`](./bootstrap.md#install) instala ambas definiciones junto con el propio
controlador. Tienen ámbito de clúster en todos los sentidos: una sola copia por clúster,
compartida por todas las instancias que haya en él, y versionada con el clúster y no con
ninguna instancia concreta. Por eso el comando que prepara un clúster es el comando que las
mueve.

`dcctl bootstrap` y `dcctl upgrade` solo leen las definiciones. Rechazan un clúster sin
definiciones, o con unas identificablemente de otra versión, e indican el comando de
instalación que hay que ejecutar. Las definiciones instaladas a mano no dejan constancia de qué
versión las puso, así que esas se dejan pasar con una nota que nombra el mismo comando, en
lugar de rechazarse.

### Estado de la instancia {#instance-status}

El operador es el único que escribe el `status` de una `Instance`, y registra lo que
**observó**: lee los Deployments de la instancia en cada cambio de la `Instance` o de alguno de
ellos, así que el estado sigue un despliegue mientras ocurre. Otras dos cosas del recurso son de
naturaleza distinta:

| Dónde | Qué dice | Quién lo escribe |
|---|---|---|
| `status` | lo que el operador **observó** de las cargas de trabajo | solo el operador |
| anotaciones (`core.devicechain.io/phase`, `last-applied-by`, `last-applied-at`) | lo que la última ejecución de `dcctl` **pretendía** o hizo; la columna `OPERATION` | `dcctl` |
| el Lease `dcctl` en el espacio de nombres del operador | quién opera sobre el clúster **ahora mismo** | `dcctl` |

`dcctl` nunca escribe `status`, y el operador nunca lee las anotaciones: `OPERATION` puede decir
`Upgrading` mientras `READY` dice `True`, y ambas cosas son ciertas.

| Campo | Significado |
|---|---|
| `status.observedGeneration` | la generación de la especificación a partir de la cual se calculó el estado |
| `status.conditions` | `Ready`, `WorkloadsAvailable`, `RolloutComplete`, `BrokerCertificateValid` (abajo) |
| `status.version.requested` | `spec.imageVersion` |
| `status.version.running` | la etiqueta de imagen que ejecuta cada área con pods deseados; `mixed` (reservado: nunca es una etiqueta de versión) mientras difieren; vacío si no se puede derivar |
| `status.areas[]` | una entrada por área funcional: `name`, `desiredReplicas`, `readyReplicas`, `availableReplicas`, `updatedReplicas` e `image` (la referencia completa de la imagen, no solo la etiqueta). Las condiciones se calculan a partir de `availableReplicas` |

El estado es pequeño y acotado: una entrada por área funcional, y nada por inquilino ni por
dispositivo.

| Condición | Motivo (`reason`) | Estado | Significado |
|---|---|---|---|
| `WorkloadsAvailable` | `AllAreasAvailable` | True | cada área que quiere pods tiene todos disponibles |
| | `NoWorkloads` | False | no existe ningún Deployment de área (aún no renderizado, o eliminado), o todas las áreas están a cero réplicas |
| | `AreaUnavailable` | False | al menos un área quiere pods y no tiene ninguno disponible |
| | `AreaDegraded` | False | cada área tiene algún pod disponible, pero al menos una tiene menos de los que quiere |
| `RolloutComplete` | `RolloutComplete` | True | cada Deployment ha convergido a su plantilla actual y la versión en ejecución es la solicitada |
| | `NoWorkloads` | False | no existe ningún Deployment de área |
| | `RolloutInProgress` | False | un área no ha convergido: quedan pods antiguos, o el controlador del Deployment no ha observado la última plantilla |
| | `ProgressDeadlineExceeded` | False | un Deployment informa de que su despliegue ha dejado de progresar |
| | `VersionMismatch` | False | ha convergido, pero en una versión distinta de `spec.imageVersion` |
| | `VersionUnknown` | Unknown | ha convergido, pero la versión en ejecución no se puede derivar (imágenes fijadas solo por digest) |
| `BrokerCertificateValid` | `CertificateValid` | True | el certificado TLS del broker y la autoridad que lo firmó tienen más de 30 días de vigencia |
| | `CertificateExpiringSoon` | True | uno de los dos caduca en menos de 30 días; `dcctl upgrade` renueva el certificado |
| | `CertificateExpired` | False | uno de los dos ha caducado: el broker rechaza las conexiones TLS hasta que se renueve |
| | `CertificateNotPublished` | Unknown | no se ha publicado ninguna caducidad para la instancia (la tocó por última vez un `dcctl` anterior a esto) |
| | `CertificateUnreadable` | Unknown | la caducidad publicada no es una hora RFC 3339 |
| `Ready` | `Ready` | True | `WorkloadsAvailable` y `RolloutComplete` son ambas True, y el certificado del broker no ha caducado |
| | (el motivo de la primera condición que no es True) | False / Unknown | |

Una carga de trabajo ausente siempre se informa con un motivo; nunca se lee como sana. `Ready`
es el estado asentado: es False mientras hay un despliegue en curso aunque los pods sigan
sirviendo, así que lee `WorkloadsAvailable` para saber si la instancia está sirviendo.

#### Caducidad del certificado del broker {#broker-certificate-expiry}

El certificado TLS del broker es válido durante un año y solo se renueva cuando se ejecuta
`dcctl upgrade`, dentro de los últimos 30 días del certificado. Una instancia que nunca se
actualiza perdería por tanto el TLS del broker en el aniversario de su bootstrap sin que nada lo
avisara. `BrokerCertificateValid` es ese aviso en la propia `Instance`.

El operador no tiene acceso a los Secrets, así que no lee el certificado. `dcctl` publica cuándo
caducan el certificado y su autoridad, como dos anotaciones de la `Instance`, cada vez que los
coloca o los renueva (`dcctl bootstrap` y cada `dcctl upgrade`, también uno que no renueva nada,
de modo que una instancia más antigua las obtiene en su siguiente upgrade):

| Anotación | Valor |
|---|---|
| `core.devicechain.io/broker-certificate-not-after` | cuándo caduca el certificado del broker, RFC 3339, UTC |
| `core.devicechain.io/broker-ca-not-after` | cuándo caduca la autoridad de certificación que lo firmó |

El operador compara esas fechas con su propio reloj, así que la condición cambia con el paso del
tiempo sin que cambie nada más en el clúster: se revisa en cada límite y al menos cada hora. El
mensaje nombra la fecha, de modo que no cambia de una revisión a la siguiente.

`Ready` solo sigue al caso False. Un certificado próximo a caducar deja `Ready` como está, porque
la instancia está sirviendo y un estado que pasara a no listo por una fecha renovable enseñaría a
ignorarlo; uno caducado significa que el TLS hacia el broker falla, que es lo que `Ready` existe
para decir. `Unknown` nunca cambia `Ready`. Si las cargas de trabajo tampoco están sanas, `Ready`
conserva el motivo de las cargas de trabajo, porque es lo primero que hay que arreglar.

Las instancias con monitorización activada también reciben las alertas
`BrokerCertificateExpiringSoon` y `BrokerCertificateExpiryCritical`, que leen la caducidad del
certificado que el broker está sirviendo y no de estas anotaciones (consulta el [runbook de
alertas](./observability.md#broker-client-certificates)). La condición es lo que cubre una
instancia sin monitorización, y lo único que cubre la autoridad de certificación.

`Instance` es una API soportada. Pueden añadirse campos de estado, tipos de condición y motivos
nuevos; nada se elimina ni cambia de significado. Un lector debe tratar como Unknown un tipo de
condición o un motivo que no reconozca.

Los inquilinos **no** son recursos personalizados. Son registros de base de datos del plano de
control, creados a través de la API de administración de la instancia y la consola `/admin`, y
comparten los servicios de la instancia (consulta
[Multitenencia](../concepts/multi-tenancy.md)).

```bash
kubectl get instances      # platform
```

## La declaración de la instancia {#instance-declaration}

`dcctl bootstrap` escribe un objeto `Instance` para la instancia que va a construir. Después lo
vuelve a leer y trabaja con lo que ha leído, no con los flags que lo produjeron. Eso hace que el
registro de lo que es la instancia esté en el clúster, y no en la máquina donde se escribió el
comando:

- a qué proveedor y clúster pertenece;
- el perfil y la versión de imagen que ejecuta;
- si sus bases de datos se recuperaron desde un archivo;
- qué compilación de `dcctl` lo escribió por última vez;
- qué intentaba hacer la última ejecución.

Por tanto, un operador en una segunda máquina puede leer la instancia sin ningún estado local:

```bash
kubectl --context <kube-context> get instances
kubectl --context <kube-context> get instance <id> -o yaml
```

Parte de la especificación es **inmutable** una vez escrita. Lo que más importa es el vínculo
con el clúster, porque reescribirlo apuntaría `dcctl destroy` a un clúster distinto. Ambas
capas rechazan esa edición:

- El CRD lleva las reglas como expresiones de validación, así que el servidor de la API
  rechaza una edición a mano hecha con `kubectl`.
- `dcctl` compara esos mismos campos con la declaración que ya está en el clúster antes de
  escribir.

### Leer la columna OPERATION {#phase}

`kubectl get instances` imprime una columna **OPERATION**, leída de la anotación
`core.devicechain.io/phase` de la declaración. `dcctl instances list` lee la misma anotación y
la muestra en palabras en su columna STATUS. Solo funciona en la máquina que arrancó la
instancia, a partir de los registros locales de esa máquina.

:::warning La operación es intención, no salud
Registra lo que **la última ejecución de `dcctl` intentaba hacer**. Nada de lo que la escribe
ha mirado un pod, y por eso `dcctl instances list` nunca dice `running`. Si las cargas de
trabajo están en marcha lo dice la columna **READY**, que el operador calcula a partir de los
Deployments (consulta [Estado de la instancia](#instance-status)).
:::

| OPERATION | STATUS en `dcctl instances list` | Qué significa |
|---|---|---|
| `Bootstrapping` | `bootstrap started, not finished` | `dcctl bootstrap` escribió la declaración y todavía no ha escrito una fase final. Así se ve también un bootstrap sano que se está ejecutando ahora mismo en otra terminal. |
| `Upgrading` | `upgrade started, not finished` | Lo mismo, para `dcctl upgrade`. |
| `Ready` | `declared ready` | El último bootstrap o upgrade terminó. No dice nada sobre los pods de hoy. |
| `Failed` | `last run failed` | El último bootstrap o upgrade devolvió un error. |
| `Destroying` | ``PART-WAY DESTROYED — re-run `dcctl destroy` `` | `dcctl destroy` empezó y no terminó. Lo escribe **antes** de borrar nada, así que un destroy interrumpido en cualquier punto posterior se ve aquí. |
| *(vacío)* | `declared, phase not recorded` | La declaración la escribió un `dcctl` anterior a la existencia de la anotación. |

Antes de actuar sobre `Bootstrapping` o `Upgrading`, comprueba si la ejecución sigue en marcha.
Una ejecución viva reescribe la fase a `Ready` o `Failed` al terminar, incluso cuando se
interrumpe con Ctrl+C. Solo una ejecución que nunca llegó a escribir su final deja uno de estos
valores atrás: una máquina que perdió la corriente, una terminal que fue matada. Ninguno de los
dos bloquea nada: `dcctl bootstrap` y `dcctl upgrade` se ejecutan sobre ellos sin objeción. El
único valor de fase sobre el que actúan los comandos es `Destroying` (consulta
[más abajo](#finalizer)).

Además de la fase, hay otra anotación sobre la que actúan.
`core.devicechain.io/bootstrap-unfinished: "true"` marca una instancia cuyo primer arranque
inicial todavía no ha terminado con éxito. Mientras está ahí, y la fase no es `Ready`,
`dcctl bootstrap` se ejecuta sobre la instancia para terminarla aunque su documento de
configuración exista. El arranque inicial o la actualización
que termina con éxito la retira en la misma escritura que registra `Ready`. No la añadas a mano:
en una instancia en marcha cuya fase no sea `Ready`, deja pasar una nueva ejecución del arranque
inicial sobre ella.

Una fila que dice `declared, unknown phase "…"` la escribió un `dcctl` más nuevo que el que la
está listando.

`dcctl instances list` comprueba algunas cosas antes de llegar a la fase. Cada una recibe sus
propias palabras en lugar de una que parezca sana:

| STATUS | Cuándo |
|---|---|
| `PART-WAY DESTROYED` | A partir del marcador de destroy de esta misma máquina, incluso cuando no se puede alcanzar el clúster |
| `cluster gone — stale local state` | El clúster ya no existe |
| `no record — destroy will guess the cluster` | Una instancia arrancada antes de que se registrara el clúster |
| `cluster present, no declaration` | El clúster existe pero no contiene ninguna declaración |
| `declaration marked for deletion` | Consulta [más abajo](#finalizer) |
| `could not check: …` | Una comprobación falló o agotó su tiempo |

### `kubectl delete instance` no termina {#finalizer}

Una declaración lleva un **finalizador**. Borrarla a mano deja el objeto en su sitio, marcado
para eliminación, hasta que `dcctl` lo libere:

```bash
kubectl delete instance prod    # no retorna; el objeto permanece, ahora terminando
```

Es deliberado, y protege dos cosas distintas:

- **La instancia.** Una declaración borrada a mano dejaría huérfana una instancia viva:
  namespaces, bases de datos, volúmenes y cargas de trabajo todos en marcha, sin nada en el
  clúster que registre a qué pertenecen.
- **El vínculo con el clúster.** Las reglas de inmutabilidad funcionan comparando la nueva
  versión del objeto con la anterior. Un objeto recreado no tiene versión anterior contra la
  que comparar, así que borrar y volver a aplicar reapuntaría el vínculo con el clúster en dos
  pasos que, por separado, parecen legítimos.

**`dcctl destroy` libera el finalizador por sí mismo**, como su último paso en el clúster: una
vez desaparecido el namespace y antes de eliminar el estado local. En el caso normal no hay
nada que hacer a mano. `destroy` nunca elimina el clúster en sí, así que este es el paso que
elimina la declaración.

Un destroy que falla deja la declaración en su sitio a propósito, salvo que falle solo al
eliminar el estado local, cuando la declaración ya no está. La declaración que queda:

- sigue registrando en qué clúster vive la instancia, que es lo que necesita una nueva
  ejecución;
- dice `Destroying` en lugar de `Ready`, de modo que el siguiente lector puede saber que está
  mirando un desmontaje en curso.

:::note Bootstrap y upgrade rechazan una instancia a medio destruir
`dcctl bootstrap` y `dcctl upgrade` sobre una declaración así se niegan, en lugar de construir
media instancia nueva sobre media instancia antigua. El rechazo te indica que termines el
desmontaje con `dcctl destroy <id>`, que es reanudable, o, si estás seguro de que no queda nada
de la instancia, que retires la declaración con `dcctl instances release <id>`, que no destruye
nada.
:::

### Eliminar una declaración sin destruir nada {#release}

```bash
dcctl instances release <id> --kube-context <kube-context>
```

Esto libera el finalizador y borra la declaración. **No destruye nada.** Los namespaces, las
bases de datos, los volúmenes y las cargas de trabajo siguen todos en marcha después, y `dcctl`
deja de tener un registro de a qué pertenecen. Por eso el comando te obliga a confirmar
explícitamente que eso es lo que quieres. Existe porque, de lo contrario, un finalizador cuyo
liberador ha desaparecido dejaría al clúster con un objeto que nadie puede borrar.

Imprime lo que va a hacer y te pide que escribas de vuelta el nombre de la instancia; `--yes`
omite esa confirmación para uso automatizado. `--kube-context` hace falta desde cualquier
máquina que no haya arrancado la instancia, ya que es lo que le dice a `dcctl` qué clúster
tiene la declaración.

Para eliminar la **instancia**, ejecuta `dcctl destroy`.

## Separación de responsabilidades

DeviceChain asigna deliberadamente cada capa a una sola herramienta:

| Capa | Herramienta | Responsabilidad |
|---|---|---|
| Infraestructura | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Cargas de trabajo | **Chart de Helm** | Deployments, Services y los ConfigMaps de configuración por área |
| Ciclo de vida | **Operador** | observa el recurso `Instance` y sus Deployments y escribe el [estado](#instance-status) observado; poda los respaldos base como instantáneas de volumen a su ventana de recuperación |
| Identidad y configuración de instancia | **`dcctl`** | escribe la declaración `Instance` y el Secret de configuración de instancia que montan las cargas de trabajo |
| Configuración de negocio | API de administración de la instancia / consola `/admin`, y la API / consola propia de cada inquilino | los inquilinos y los ajustes propios de cada inquilino (como la marca) |

Cada capa se ejecuta en un momento distinto:

- **OpenTofu** se ejecuta al instalar un clúster (`dcctl install`, para los requisitos previos
  que comparten todas las instancias) y al arrancar una instancia (para el bróker y el almacén
  de eventos propios de esa instancia).
- **`dcctl install`** aplica además el operador y sus definiciones, a partir de manifiestos
  incrustados en la CLI y no a través de ninguna de las otras dos capas.
- **El chart** renderiza las cargas de trabajo.
- **El operador** observa el recurso `Instance` y los Deployments de la instancia (consulta
  [Estado de la instancia](#instance-status) para saber qué escribe).

El arranque del clúster nunca vive en el código de la aplicación ni del operador; es tarea de la
capa de infraestructura. Los módulos de OpenTofu viven en
[`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu).
Aprovisionan el nivel de base de datos con guardas de retención para que sobreviva al
desmontaje de la aplicación (consulta
[Versiones y actualizaciones](./releases-and-upgrades.md#data-durability)).
