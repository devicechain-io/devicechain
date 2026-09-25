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
observa el recurso `Instance` y no actúa sobre nada. Planificado, sin empezar: la agregación
del estado de la instancia y los overlays de Kustomize por entorno.
:::

La agregación de estado llegará sobre el bucle de observación del operador. Hasta entonces, el
`Instance` no reporta ningún estado, así que consulta las propias cargas de trabajo, o `dcctl`,
para saber si una instancia está sana.

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
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — el sistema estándar, y a lo que resuelve un perfil sin establecer |
| `full` | todo lo que incluye esta compilación: `default`, más `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest` y `lwm2m-ingest` — las áreas que `default` deja fuera porque cada una conlleva una decisión que conviene tomar deliberadamente (una clave de proveedor de pago, una superficie de salida, una API orientada a agentes, un transporte de dispositivos Sparkplug B o LwM2M que abre su propio puerto de entrada) |
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
  operador, que pertenece al clúster. `dcctl upgrade` mueve el documento de configuración y la
  versión desplegada, que pertenecen a la instancia. El operador no forma parte del chart, así
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

### Leer la columna PHASE {#phase}

`kubectl get instances` imprime una columna **PHASE**, leída de la anotación
`core.devicechain.io/phase` de la declaración. `dcctl instances list` lee la misma anotación y
la muestra en palabras en su columna STATUS. Solo funciona en la máquina que arrancó la
instancia, a partir de los registros locales de esa máquina.

:::warning La fase es intención, no salud
Registra lo que **la última ejecución de `dcctl` intentaba hacer**. Nada de lo que la escribe
ha mirado un pod, y por eso `dcctl instances list` nunca dice `running`. Para saber si las
cargas de trabajo están en marcha, míralas: `kubectl get pods -n dci-<id>`.
:::

| PHASE | STATUS en `dcctl instances list` | Qué significa |
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
dos bloquea nada: `dcctl bootstrap` y `dcctl upgrade` se ejecutan sobre ellos sin objeción. La
única fase sobre la que actúan los comandos es `Destroying` (consulta [más abajo](#finalizer)).

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
| Ciclo de vida | **Operador** | observa el recurso `Instance`; hoy no actúa sobre nada (la agregación de estado está planificada) |
| Identidad y configuración de instancia | **`dcctl`** | escribe la declaración `Instance` y el Secret de configuración de instancia que montan las cargas de trabajo |
| Configuración de negocio | API de administración de la instancia / consola `/admin`, y la API / consola propia de cada inquilino | los inquilinos y los ajustes propios de cada inquilino (como la marca) |

Cada capa se ejecuta en un momento distinto:

- **OpenTofu** se ejecuta al instalar un clúster (`dcctl install`, para los requisitos previos
  que comparten todas las instancias) y al arrancar una instancia (para el bróker y el almacén
  de eventos propios de esa instancia).
- **`dcctl install`** aplica además el operador y sus definiciones, a partir de manifiestos
  incrustados en la CLI y no a través de ninguna de las otras dos capas.
- **El chart** renderiza las cargas de trabajo.
- **El operador** observa el recurso `Instance` (consulta la nota de estado al principio de esta
  página para saber qué hace hoy con él).

El arranque del clúster nunca vive en el código de la aplicación ni del operador; es tarea de la
capa de infraestructura. Los módulos de OpenTofu viven en
[`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu).
Aprovisionan el nivel de base de datos con guardas de retención para que sobreviva al
desmontaje de la aplicación (consulta
[Versiones y actualizaciones](./releases-and-upgrades.md#data-durability)).
