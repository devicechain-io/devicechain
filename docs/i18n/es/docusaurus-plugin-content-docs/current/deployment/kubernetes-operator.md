---
sidebar_position: 3
title: Despliegue y operador
---

# Despliegue y operador

DeviceChain se despliega en dos capas, y dos comandos de `dcctl` las colocan. `dcctl install` prepara el **clúster**: los requisitos previos que comparten todas las instancias, más el **operador** de Kubernetes (construido con controller-runtime) y sus definiciones de recursos. No sabe nada de ninguna instancia concreta. `dcctl bootstrap` crea después una **instancia**: escribe un recurso **`Instance`** de ámbito de clúster que declara lo que va a construir, y el **chart de Helm** renderiza las cargas de trabajo de esa instancia. El operador observa el recurso `Instance`. (Los inquilinos no forman parte de su trabajo — son registros de base de datos del plano de control, vea abajo.)

:::note Estado
El chart de Helm renderiza las cargas de trabajo y la configuración por servicio hoy, y `dcctl bootstrap` y `dcctl upgrade` gestionan el ciclo de vida de una instancia. El operador actualmente **observa** el recurso `Instance` y no actúa sobre nada: es el bucle sobre el que aterrizará la **agregación de estado** de instancia, y hasta entonces el `Instance` no reporta ningún estado — consulte las propias cargas de trabajo, o `dcctl`, para saber si una instancia está sana. La agregación de estado y los overlays de Kustomize por entorno están planificados; ninguno ha comenzado.
:::

## Desplegando con Helm

El chart en `deploy/helm/devicechain` renderiza un Deployment + Service por **área funcional habilitada**, junto con los ConfigMaps de configuración por servicio y el Secret de configuración de instancia (porta credenciales de persistencia, por lo que es un Secret en lugar de un ConfigMap). Cada pod expone `/healthz` (liveness) y `/readyz` (readiness) para que un servicio que no está listo se mantenga fuera de rotación.

Usted elige qué servicios ejecutar con **ya sea** un perfil nombrado **o** un conjunto explícito:

| Perfil | Áreas funcionales |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — el sistema estándar, y a lo que resuelve un perfil sin establecer |
| `full` | todo lo que incluye esta compilación: `default`, más `ai-inference`, `outbound-connectors`, `mcp`, `sparkplug-ingest` y `lwm2m-ingest` — las áreas que `default` deja fuera porque cada una conlleva una decisión que tomar deliberadamente (una clave de proveedor de pago, una superficie de salida, una API orientada a agentes, un transporte de dispositivos Sparkplug B o LwM2M que abre su propio puerto de entrada) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

Todos los perfiles requieren la **clave raíz del almacén de secretos** de la instancia:
`user-management`, que se ejecuta en todos los perfiles, sella con ella la clave que firma los
tokens de inicio de sesión, y las áreas que almacenan credenciales de integración sellan
también las suyas. El chart falla el renderizado sin ella en lugar de dejar que
`user-management` entre en crash-loop. Genera un valor (`openssl rand -base64 32`),
consérvalo y pasa el **mismo** valor en cada instalación y actualización: una clave nueva
vuelve ilegibles los secretos ya almacenados bajo la anterior, e impide que nadie inicie
sesión. `dcctl bootstrap` acuña y deposita en escrow esta clave por ti; solo tienes que
suministrarla tú cuando manejas el chart directamente.

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

Para instalar una versión publicada, fije la etiqueta de imagen a una versión — las imágenes publicadas son públicas
en `ghcr.io/devicechain-io`, así que no hace falta construir nada localmente:

Sustituya `<version>` por una etiqueta realmente publicada; la
[página de versiones](https://github.com/devicechain-io/devicechain/releases) las lista.

```bash
helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY" \
  --set image.tag=<version>
```

Vea [Versiones y actualizaciones](./releases-and-upgrades.md) para el modelo de versionado y el
procedimiento de actualización. Para una instancia que arrancó con el bootstrap son dos
comandos: `dcctl install` mueve el operador, que pertenece al clúster, y `dcctl upgrade` mueve
el documento de configuración y la versión desplegada, que pertenecen a la instancia. El
operador no forma parte del chart, así que algo externo al chart tiene que moverlo. Para una
instancia gobernada solo desde el chart es `helm upgrade`, con sus
valores [trasladados a mano](./releases-and-upgrades.md#chart-only-upgrade).

`user-management` y `device-management` son el núcleo requerido; `event-management`, `device-state`, y `command-delivery` son opcionales de forma independiente. El chart **falla el renderizado** si una selección omite un servicio del núcleo requerido o una dependencia dura de un servicio habilitado — de modo que una topología rota se detecta en el momento de instalación, no después de que los pods entren en crash-loop. Los valores se validan contra el `values.schema.json` del chart en el momento de aplicación.

## Recursos personalizados

- **`Instance`** (con alcance de clúster; `instances.core.devicechain.io`, nombre corto `dci`) — uno por instalación, declarando la identidad y configuración de la instancia.
- **`InstanceConfiguration`** (con alcance de clúster; `instanceconfigurations.core.devicechain.io`) — la configuración renderizada a la que se resuelve un `Instance`, y contra la que reconcilia el operador.

Ambas definiciones las instala [`dcctl install`](./bootstrap.md#install), junto con el propio
controlador, y tienen **alcance de clúster en todos los sentidos**: una sola copia por clúster,
compartida por todas las instancias que haya en él, versionada con el clúster y no con ninguna
instancia concreta. Por eso el comando que prepara un clúster es el comando que las mueve.
`dcctl bootstrap` y `dcctl upgrade` solo las leen: un clúster sin definiciones, o con unas
identificablemente de otra versión, se rechaza indicando el comando de instalación que hay que
ejecutar. Las definiciones instaladas a mano no llevan constancia de qué versión las puso, así
que esas se dejan pasar con una nota que nombra el mismo comando, en lugar de rechazarse.

Los inquilinos **no** son recursos personalizados — son registros de base de datos del plano de control creados a través de la API de administración de instancia y la consola `/admin`, compartiendo los servicios de la instancia (vea [Multitenencia](../concepts/multi-tenancy.md)).

```bash
kubectl get instances      # platform
```

## La declaración de la instancia {#instance-declaration}

`dcctl bootstrap` **escribe** uno de estos objetos para la instancia que está a punto de
construir, y después lo vuelve a leer y trabaja con lo que ha leído en lugar de con los
flags que lo produjeron. Eso hace que el registro de lo que es la instancia esté en el
clúster, y no en la máquina donde se escribió el comando: a qué proveedor y clúster
pertenece, el perfil y la versión de imagen que ejecuta, si sus bases de datos se
recuperaron desde un archivo, qué compilación de `dcctl` lo escribió por última vez y qué
estaba intentando hacer la última ejecución.

Un operador en una segunda máquina puede, por tanto, leer la instancia sin ningún estado
local:

```bash
kubectl --context <kube-context> get instances
kubectl --context <kube-context> get instance <id> -o yaml
```

Parte de la especificación es **inmutable** una vez escrita —el vínculo con el clúster
ante todo, porque reescribirlo apuntaría `dcctl destroy` a un clúster distinto. Ambas
capas rechazan esa edición: el CRD lleva las reglas como expresiones de validación, así
que el servidor de la API rechaza una edición a mano con `kubectl`, y `dcctl` compara esos
mismos campos con la declaración que ya está en el clúster antes de escribir.

### Leer la columna PHASE {#phase}

`kubectl get instances` imprime una columna **PHASE**, leída de la anotación
`core.devicechain.io/phase` de la declaración. `dcctl instances list` —que solo funciona en
la máquina que arrancó la instancia, a partir de sus registros locales— lee la misma
anotación y la muestra en palabras en su columna STATUS.

:::caution La fase es intención, no salud
Registra lo que **la última ejecución de `dcctl` estaba intentando hacer**. Nada de lo que
la escribe ha mirado un pod, y por eso `dcctl instances list` nunca dice `running`. Para
saber si las cargas de trabajo están en marcha, mírelas: `kubectl get pods -n dci-<id>`.
:::

| PHASE | STATUS en `dcctl instances list` | Qué significa |
|---|---|---|
| `Bootstrapping` | `bootstrap started, not finished` | `dcctl bootstrap` escribió la declaración y todavía no ha escrito una fase final. **Así es también como se ve un bootstrap sano que se está ejecutando ahora mismo en otra terminal.** |
| `Upgrading` | `upgrade started, not finished` | Lo mismo, para `dcctl upgrade`. |
| `Ready` | `declared ready` | El último bootstrap o upgrade terminó. No dice nada sobre los pods de hoy. |
| `Failed` | `last run failed` | El último bootstrap o upgrade devolvió un error. |
| `Destroying` | ``PART-WAY DESTROYED — re-run `dcctl destroy` `` | `dcctl destroy` empezó y no terminó. Lo escribe **antes** de borrar nada, así que un destroy interrumpido en cualquier punto posterior se ve aquí. |
| *(vacío)* | `declared, phase not recorded` | La declaración la escribió un `dcctl` anterior a la existencia de la anotación. |

Antes de actuar sobre `Bootstrapping` o `Upgrading`, compruebe si la ejecución sigue en
marcha: una ejecución viva reescribe la fase a `Ready` o `Failed` al terminar, incluso cuando
se interrumpe con Ctrl+C. Solo una ejecución que nunca llegó a escribir su final —una máquina
que perdió la corriente, una terminal que fue matada— deja uno de estos valores atrás, y
ninguno de los dos bloquea nada: `dcctl bootstrap` y `dcctl upgrade` se ejecutan sobre ellos
sin objeción. La única fase sobre la que los comandos **actúan** es `Destroying` (vea la nota
más abajo). Una fila que dice `declared, unknown phase "…"` la escribió un `dcctl` más nuevo
que el que la está listando.

`dcctl instances list` responde algunas cosas antes de llegar a la fase, y cada una recibe
sus propias palabras en lugar de una palabra sana: `PART-WAY DESTROYED` a partir del marcador
de destroy de esta misma máquina, incluso cuando no se puede alcanzar el clúster;
`cluster gone — stale local state`; `no record — destroy will guess the cluster` para una
instancia arrancada antes de que se registrara el clúster; `cluster present, no declaration`;
`declaration marked for deletion` (vea [abajo](#finalizer)); y `could not check: …` siempre
que una comprobación falló o agotó su tiempo.

### `kubectl delete instance` no termina {#finalizer}

Una declaración lleva un **finalizador**, así que borrarla a mano deja el objeto en su
sitio, marcado para eliminación, hasta que `dcctl` lo libere:

```bash
kubectl delete instance prod    # no retorna; el objeto permanece, ahora terminando
```

Es deliberado, y protege dos cosas distintas. Una declaración borrada a mano dejaría
huérfana una instancia viva —namespaces, bases de datos, volúmenes y cargas de trabajo
todos en marcha, sin nada en el clúster que registre a qué pertenecen. Y como las reglas
de inmutabilidad funcionan comparando la nueva versión del objeto con la anterior, un
objeto recreado no tiene versión anterior contra la que comparar: borrar y volver a
aplicar reapuntaría el vínculo con el clúster en dos pasos que, por separado, parecen
legítimos.

**`dcctl destroy` libera el finalizador por sí mismo**, como último paso en el clúster, una
vez desaparecido el namespace y antes de eliminar el estado local —así que en el caso normal
no hay nada que hacer a mano. `destroy`
nunca elimina el clúster en sí, así que este es el paso que elimina la declaración.

:::note Un destroy que falla deja la declaración en su sitio a propósito
Salvo que falle solo al eliminar el estado local, cuando la declaración ya no está, sigue
registrando en qué clúster vive la instancia, que es lo que necesita una nueva
ejecución, y dice `Destroying` en lugar de `Ready`, de modo que el siguiente lector puede
saber que está mirando un desmontaje en curso. Tanto `dcctl bootstrap` como `dcctl upgrade`
sobre una declaración así **se niegan** en lugar de construir media instancia nueva sobre
media instancia antigua; el rechazo le indica que termine el desmontaje con
`dcctl destroy <id>`, que es reanudable —o, si está seguro de que no queda nada de la
instancia, que retire la declaración con `dcctl instances release <id>`, que no destruye nada.
:::

### Eliminar una declaración sin destruir nada {#release}

```bash
dcctl instances release <id> --kube-context <kube-context>
```

Esto libera el finalizador y borra la declaración. **No destruye nada.** Los namespaces,
las bases de datos, los volúmenes y las cargas de trabajo siguen todos en marcha
después, y `dcctl` simplemente deja de tener un registro de a qué pertenecen —que es
justamente por lo que el comando le obliga a decir en voz alta que eso es lo que quiere.
Existe porque, de lo contrario, un finalizador cuyo liberador ha desaparecido dejaría al
clúster con un objeto que nadie puede borrar.

Imprime lo que va a hacer y le pide que escriba de vuelta el nombre de la instancia;
`--yes` omite esa confirmación para uso automatizado. `--kube-context` hace falta desde
cualquier máquina que no haya arrancado la instancia, ya que es lo que le dice a `dcctl`
qué clúster tiene la declaración.

Si lo que quiere es eliminar la **instancia**, ejecute `dcctl destroy` en su lugar.

## Separación de responsabilidades

DeviceChain divide deliberadamente cada capa:

| Capa | Herramienta | Responsabilidad |
|---|---|---|
| Infraestructura | **OpenTofu** | NATS, TimescaleDB, namespaces, ingress, TLS |
| Cargas de trabajo | **Chart de Helm** | Deployments, Services y los ConfigMaps de configuración por área |
| Ciclo de vida | **Operador** | observa el recurso `Instance`; hoy no actúa sobre nada (la agregación de estado está planificada) |
| Identidad y configuración de instancia | **`dcctl`** | escribe la declaración `Instance` y el Secret de configuración de instancia que montan las cargas de trabajo |
| Configuración de negocio | kubectl / UI | inquilinos y sus ajustes |

OpenTofu se ejecuta al instalar un clúster (`dcctl install`, para los requisitos previos que comparten todas las instancias) y al arrancar una instancia (para el bróker y el almacén de eventos propios de esa instancia); `dcctl install` aplica además el operador y sus definiciones, a partir de manifiestos incrustados en la CLI y no a través de ninguna de las otras dos capas; el chart renderiza las cargas de trabajo; el operador observa el recurso `Instance` (vea la nota de estado al principio de esta página para saber qué hace hoy con él). El arranque del clúster nunca vive en el código de la aplicación o del operador — es responsabilidad de la capa de infraestructura. Los módulos de OpenTofu viven en [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu); aprovisionan el nivel de base de datos con guardas de retención para que sobreviva al desmontaje de la aplicación (vea [Versiones y actualizaciones](./releases-and-upgrades.md#data-durability)).
