---
sidebar_position: 3
title: Despliegue y operador
---

# Despliegue y operador

DeviceChain se despliega en dos capas declarativas: un **chart de Helm** renderiza las cargas de trabajo de la plataforma, y un **operador** de Kubernetes (construido con controller-runtime) gestiona el ciclo de vida de `Instance`. Ambas son declarativas y compatibles con GitOps. (Los inquilinos no forman parte del trabajo del operador — son registros de base de datos del plano de control, vea abajo.)

:::note Estado
El chart de Helm renderiza las cargas de trabajo y la configuración por servicio hoy. La **agregación de estado** de instancia del operador y la **recarga en caliente de configuración** están en curso. Los overlays de Kustomize por entorno están planificados.
:::

## Desplegando con Helm

El chart en `deploy/helm/devicechain` renderiza un Deployment + Service por **área funcional habilitada**, junto con los ConfigMaps de configuración por servicio y el Secret de configuración de instancia (porta credenciales de persistencia, por lo que es un Secret en lugar de un ConfigMap). Cada pod expone `/healthz` (liveness) y `/readyz` (readiness) para que un servicio que no está listo se mantenga fuera de rotación.

Usted elige qué servicios ejecutar con **ya sea** un perfil nombrado **o** un conjunto explícito:

| Perfil | Áreas funcionales |
|---|---|
| `default` | user-management, device-management, event-sources, event-management, device-state, dashboard-management, command-delivery, notification-management, event-processing — el sistema estándar, y a lo que resuelve un perfil sin establecer |
| `full` | todo lo de `default`, más `ai-inference`, `outbound-connectors`, y `mcp`: las áreas que alcanzan fuera de la instancia, cada una de las cuales conlleva una decisión que tomar deliberadamente (una clave de proveedor de pago, una superficie de salida, una API orientada a agentes) |
| `telemetry` | user-management, device-management, event-sources, event-management, device-state, dashboard-management |
| `ingest-only` | user-management, device-management, event-sources |

Cualquier perfil que ejecute un área con almacén de secretos —como hace el perfil `default`,
a través de `notification-management`— requiere la **clave raíz del almacén de secretos** de
la instancia, y el chart falla el renderizado sin ella en lugar de dejar que el área entre en
crash-loop. Genera un valor (`openssl rand -base64 32`), consérvalo y pasa el **mismo** valor
en cada instalación y actualización: una clave nueva vuelve ilegibles los secretos ya
almacenados bajo la anterior. `dcctl bootstrap` acuña y deposita en escrow esta clave por ti;
solo tienes que suministrarla tú cuando manejas el chart directamente.

```bash
DC_ROOT_KEY="$(openssl rand -base64 32)"   # genérala UNA vez, y consérvala

helm install dc deploy/helm/devicechain \
  --set instance.id=devicechain \
  --set instance.config.infrastructure.secrets.rootKey="$DC_ROOT_KEY"

# Ejecuta un conjunto menor de servicios. `telemetry` no ejecuta ningún área con
# almacén de secretos, así que no necesita clave raíz.
helm install dc deploy/helm/devicechain --set profile=telemetry
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
procedimiento de actualización, que es `helm upgrade` para los servicios **más** `dcctl upgrade`
para el operador, ya que el operador no forma parte del chart.

`user-management` y `device-management` son el núcleo requerido; `event-management`, `device-state`, y `command-delivery` son opcionales de forma independiente. El chart **falla el renderizado** si una selección omite un servicio del núcleo requerido o una dependencia dura de un servicio habilitado — de modo que una topología rota se detecta en el momento de instalación, no después de que los pods entren en crash-loop. Los valores se validan contra el `values.schema.json` del chart en el momento de aplicación.

## Recursos personalizados

- **`Instance`** (con alcance de clúster; `instances.core.devicechain.io`, nombre corto `dci`) — uno por instalación, declarando la identidad y configuración de la instancia.

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
ante todo, porque reescribirlo apuntaría `dcctl destroy` a un clúster distinto. Es el
servidor de la API el que rechaza esas ediciones, no una comprobación de `dcctl`.

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

**`dcctl destroy` libera el finalizador por sí mismo**, como último paso, una vez que la
instancia ya no está —así que en el caso normal no hay nada que hacer a mano. (Cuando
`destroy` elimina el clúster entero, la declaración se va con él.)

:::note Un destroy que falla deja la declaración en su sitio a propósito
Sigue registrando en qué clúster vive la instancia, que es lo que necesita una nueva
ejecución, y dice `Destroying` en lugar de `Ready`, de modo que el siguiente lector puede
saber que está mirando un desmontaje en curso. Un `dcctl bootstrap` sobre una declaración
así **se niega** en lugar de construir media instancia nueva sobre media instancia
antigua; le indica que termine primero el desmontaje.
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
| Cargas de trabajo | **Chart de Helm** | Deployments, Services, ConfigMaps de configuración por área, y el Secret de configuración de instancia |
| Ciclo de vida | **Operador** | Agregación de estado de `Instance` y recarga en caliente de configuración |
| Configuración de negocio | kubectl / UI | inquilinos y sus ajustes |

OpenTofu se ejecuta una vez en la creación del clúster; el chart renderiza las cargas de trabajo; el operador se ejecuta de forma continua, reconciliando el ciclo de vida. El arranque del clúster nunca vive en el código de la aplicación o del operador — es responsabilidad de la capa de infraestructura. Los módulos de OpenTofu viven en [`deploy/opentofu`](https://github.com/devicechain-io/devicechain/tree/main/deploy/opentofu); aprovisionan el nivel de base de datos con guardas de retención para que sobreviva al desmontaje de la aplicación (vea [Versiones y actualizaciones](./releases-and-upgrades.md#data-durability)).
