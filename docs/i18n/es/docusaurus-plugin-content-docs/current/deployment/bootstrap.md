---
sidebar_position: 1
title: Arranque inicial de una instancia
---

# Arranque inicial de una instancia

`dcctl bootstrap` levanta una instancia completa de DeviceChain —infraestructura,
el operador y todas las cargas de trabajo de servicio— con un solo comando:

```bash
dcctl bootstrap local my-instance
```

`dcctl` es un **binario autocontenido**. La configuración de infraestructura de
OpenTofu, el chart de Helm y los manifiestos del operador están todos incrustados
en él, así que no necesitas un checkout del árbol de código fuente, `git`,
`kubectl`, `kustomize` ni `helm` en tu máquina —solo `dcctl` y un clúster donde
desplegar.

:::note Estado
DeviceChain está en fase previa al lanzamiento (pre-release). `dcctl bootstrap local`
está implementado y validado de extremo a extremo en Kubernetes local (kind). El
proveedor `gcp` y la creación automática de clúster local son mejoras planificadas
—consulta [Prerrequisitos](#prerequisites).
:::

## Qué hace

El arranque inicial se ejecuta como una canalización (pipeline) ordenada e
**idempotente** —volver a ejecutarlo converge al mismo estado y te indica qué paso
falló si alguno lo hace:

:::warning Excepción única: instancias creadas antes de que la base de datos pasara a CloudNativePG
La base de datos relacional pasó de ser un StatefulSet a un clúster de CloudNativePG, y no
existe una actualización en sitio: el directorio de datos de un StatefulSet no puede ser
adoptado por el operador. En una instancia creada antes de ese cambio, volver a ejecutar el
arranque inicial **se niega** en lugar de converger, y te indica cómo volcar los datos o
descartarlos deliberadamente. Esa negativa es justamente el objetivo: sin ella la base de
datos antigua se eliminaría y una nueva, vacía, ocuparía el mismo nombre de host, dejando una
instancia que parece perfectamente sana y no tiene ningún dato.
:::

1. **Renderizar la configuración** — resuelve el id de la instancia, el namespace,
   el perfil y todas las credenciales generadas: el material de autenticación del
   bróker (la contraseña de servicio compartida y la clave del emisor del callout),
   el secreto de autenticación entre servicios y la **clave raíz del almacén de
   secretos**. Todas se acuñan en la primera instalación y **se reutilizan tal cual
   cuando la instancia ya existe**: el pipeline le pregunta al clúster qué está
   ejecutando la instancia antes de generar nada, y se detiene en vez de suponer si
   no puede determinarlo. Además, la clave raíz se deposita en un archivo cifrado que
   tú conservas; consulta [Recuperación ante desastres](./disaster-recovery.md).
2. **Aplicar la infraestructura** — ejecuta `tofu apply` sobre la configuración de
   OpenTofu incrustada (NATS, PostgreSQL, TimescaleDB, ingress de NGINX,
   cert-manager, el operador CloudNativePG y su plugin de respaldo Barman Cloud,
   y el almacén de objetos al que ese plugin archiva)
   vía [terraform-exec](https://github.com/hashicorp/terraform-exec).
   El estado se guarda en `~/.devicechain/<instance>/infra`, de modo que las
   ejecuciones posteriores son incrementales.
3. **Instalar el núcleo (core)** — renderiza el operador (CRDs + RBAC +
   controlador) y lo aplica directamente con la API de Kubernetes.
4. **Instalar la instancia** — despliega el chart de Helm vía el SDK de Helm para
   Go, bloqueando hasta que las cargas de trabajo estén listas.
5. **Sembrar (seed) e informar** — la credencial de superusuario se siembra
   automáticamente mediante el servicio de user-management en el primer arranque;
   el comando la imprime al final (junto con el namespace y los punteros de
   acceso).

Dado que los artefactos incrustados son los *mismos* que distribuye la
plataforma, una instancia arrancada de este modo ejercita el despliegue real —no
puede desviarse de un despliegue de producción.

:::info El destino de respaldo predeterminado es un componente AGPL
Los respaldos de base de datos necesitan un destino y, de forma predeterminada, ese destino
es un **MinIO** de una sola réplica en el namespace de tu instancia, para que un arranque
estándar produzca una instancia cuyo log de escritura anticipada (WAL) se archive de verdad,
en lugar de una que lleve un plugin de respaldo sin ningún sitio donde escribir.

Dos cosas que conviene saber antes de aceptar ese valor predeterminado. MinIO se distribuye
bajo licencia **AGPL-3.0**, y la edición comunitaria de MinIO entró en modo de mantenimiento
en diciembre de 2025 y se archivó en abril de 2026, por lo que la imagen fijada no recibe más
parches de seguridad. Ninguna de las dos cosas afecta a la licencia Apache-2.0 de la propia
DeviceChain —la imagen se referencia, nunca se compila, modifica ni redistribuye, y la
plataforma se comunica con ella mediante la API HTTP de S3—, pero el componente se ejecuta en
*tu* clúster, y muchas organizaciones no permiten software AGPL con independencia de cómo se
use.

Apunta el destino de respaldo a un almacenamiento fuera del clúster para evitar ambas cosas.
Esa es la configuración de producción recomendada de todos modos, por un motivo que nada
tiene que ver con las licencias: un bucket dentro del clúster comparte su dominio de fallo,
así que no puede constituir recuperación ante desastres. Consulta
[Recuperación ante desastres](./disaster-recovery.md) y el `backup_destination` de la
configuración de OpenTofu.
:::

## Prerrequisitos {#prerequisites}

- **Un clúster de Kubernetes, versión 1.29 o más reciente**, y un kube-context que
  apunte a él. El mínimo proviene de los charts de CloudNativePG, que se niegan a
  instalarse por debajo de esa versión; `dcctl preflight` lo verifica por
  adelantado, porque de lo contrario el fallo aparece a mitad de un levantamiento
  que ya ha escrito tu archivo de custodia (escrow) de la clave raíz. Para el
  proveedor `local` esto es un clúster local (kind / minikube / k3d /
  docker-desktop). `dcctl` autodetecta un contexto local; pasa
  `--kube-context <name>` para elegir uno explícitamente. (Hoy el proveedor
  `local` selecciona un contexto existente; crear el clúster por ti es una
  incorporación planificada.)
- **OpenTofu** (el binario `tofu`; `terraform` también funciona) en tu `PATH`.
  `dcctl` lo gobierna para aprovisionar infraestructura. Instálalo desde
  [opentofu.org](https://opentofu.org). Ejecuta `dcctl preflight local` para
  comprobar esto y el resto de tu entorno de antemano.

## Origen de las imágenes

Por defecto, el arranque inicial despliega las **imágenes publicadas** desde
`ghcr.io/devicechain-io` —nada que compilar:

```bash
dcctl bootstrap local my-instance
```

Los desarrolladores que trabajan desde un checkout de código fuente pueden
compilar las imágenes desde el código y desplegar esas en su lugar con
`--build`, que compila cada servicio y el operador con
[`ko`](https://ko.build) —además de la consola web con `docker build`— en un
registro local, y despliega por referencia:

```bash
# desde un checkout de código fuente; requiere Docker + ko
dcctl bootstrap local my-instance --build
```

La única diferencia entre ambos caminos es el registro desde el que los pods
extraen las imágenes —la canalización, el chart y el operador son idénticos.

## Flags útiles

| Flag | Propósito |
|------|-----------|
| `--kube-context <name>` | Apunta a un kube-context específico (por defecto: autodetecta uno local). |
| `--profile <profile>` | Perfil de área funcional: `default` (el sistema estándar, usado cuando se omite), `full` (todo —añade inferencia de IA, conectores salientes y MCP), `telemetry`, o `ingest-only`. |
| `--build` | Compila las imágenes desde el código fuente en un registro local (ruta para desarrolladores; necesita el árbol de código fuente + Docker + ko). |
| `--registry` / `--version` | Sobrescribe el registro/etiqueta de imagen (por defecto: `ghcr.io/devicechain-io` publicado, o `localhost:5000` + `dev` con `--build`). |
| `--host <name>` | Host de ingress en el que exponer la instancia (por defecto `devicechain.local`). Usa `localhost` en un clúster local para llegar a la consola **sin editar `/etc/hosts`**. |
| `--no-tls` | Sirve HTTP simple en lugar de un certificado autofirmado. Con `--host localhost`, un `http://localhost/` sin configuración adicional (sin advertencia de certificado). |
| `--compact` | Preajuste de huella pequeña —ver más abajo. |
| `--ha` | Alta disponibilidad de mensajería —ver más abajo. Requiere al menos **3 nodos planificables**. |
| `--no-cnpg` | Omite el operador CloudNativePG y el plugin de respaldo de base de datos. Para un clúster que **ya ejecuta CloudNativePG**: Helm no puede adoptar objetos creados por otro instalador, así que sin esta bandera el apply de infraestructura falla. |
| `--dry-run` | Imprime lo que haría cada paso sin cambiar nada. |
| `--skip-preflight` | Omite las comprobaciones de entorno. |

### `--compact`

Un preajuste para clústeres pequeños. Compone palancas que ya existen en lugar de
añadir un eje de ajuste propio:

- techos por-stream más bajos de JetStream y KV, y los volúmenes más pequeños que
  eso permite (2Gi JetStream, 2Gi Postgres relacional, 4Gi TimescaleDB);
- **solicitudes** (requests) de programación más bajas (25m / 64Mi), para que los
  pods quepan en un nodo pequeño —los límites quedan intactos, ya que bajar el
  límite de memoria convierte la presión en OOMKills y bajar el límite de CPU
  produce throttling, ninguno de los cuales reduce nada realmente;
- sin la pila de monitoreo, el mayor consumidor individual;
- sin cert-manager, ya que con TLS desactivado nada necesita que se emita un
  certificado (mantener TLS conserva también cert-manager —ver más abajo), y en
  consecuencia sin el plugin de respaldo de base de datos.

**No** cambia qué servicios se ejecutan —eso se controla en `--profile`, donde
queda nombrado y visible. Un perfil *más grande* que `default` —hoy solo
`full`— es rechazado: las cifras compactas publicadas se miden sobre `default`,
así que no describirían una instancia que ejecuta tres servicios más. Los
perfiles más pequeños (`telemetry`, `ingest-only`) sí se aceptan.

Tanto TLS como el monitoreo pueden conservarse: un `--no-tls=false` o
`--no-monitoring=false` explícito se respeta, y el resto de las palancas
compactas siguen aplicándose. Mantener TLS también conserva cert-manager, que es
lo que emite el certificado. `--grafana-sso` necesita la pila de monitoreo donde
vive Grafana, así que se rechaza a menos que la conserves con
`--no-monitoring=false`.

:::note Por qué `--compact --no-tls` descarta el plugin de respaldo
El plugin Barman Cloud emite sus propios certificados a través de cert-manager, así
que descartar cert-manager descarta también el plugin. Volver a activar TLS
(`--no-tls=false`) restablece ambos. Ten en cuenta que hacen falta *ambas* banderas:
`--no-tls` por sí sola —como en el ejemplo de URL local más abajo— conserva
cert-manager y por lo tanto conserva el plugin.

El operador CloudNativePG en sí se instala en *todo* levantamiento, incluido el
compacto —un Deployment que solicita 100m/128Mi, más sus CRDs—. Ese es un costo de
huella que el modo compacto no evita, y es deliberado: el respaldo no es una función
de alta disponibilidad, así que la capa de almacenamiento tiene una sola forma en
todas partes.

Ambas bases de datos se ejecutan ahora sobre el operador: tanto el almacén relacional
como el de eventos.
:::

:::caution Los tamaños de volumen son un presupuesto de tiempo, no de capacidad
El volumen de JetStream se deriva: los techos por-stream se reservan por
adelantado, así que el volumen se dimensiona para contener su suma. Los dos
volúmenes de base de datos no. Nada poda las tablas de comandos o de alarmas, y
`retentionDays` es `0` por defecto —conservar los datos para siempre— así que en
una instancia compacta pensada para ejecutarse indefinidamente, establece una
ventana de retención en lugar de confiar en el tamaño del volumen.
:::

:::caution Aplícalo a un clúster nuevo
Bajar un techo por debajo de lo que un stream o bucket de KV ya contiene tiene
éxito silenciosamente, no trunca nada, y rechaza escrituras hasta que los datos
envejezcan y se purguen. `--compact` es seguro en un primer arranque; no es la
misma operación aplicada a una instancia en ejecución.
:::

:::tip URL local sin configuración
`dcctl bootstrap local my-instance --build --host localhost --no-tls` expone la
consola en `http://localhost/` —sin entrada en el archivo hosts y sin advertencia
de certificado.
:::

### `--ha` {#ha}

Ejecuta el broker de mensajería como un clúster RAFT de 3 nodos, un servidor por nodo, con
**cada stream de JetStream y cada bucket KV replicados a lo largo del clúster**. La
instancia sobrevive entonces a la pérdida de cualquier nodo sin perder mensajes, sesiones
de dispositivo ni estado en vivo.

```bash
dcctl bootstrap local mi-instancia --ha
```

Ambas mitades se establecen a partir de ese único flag, y ese es justamente su propósito.
El tamaño del broker es infraestructura (OpenTofu); el factor de réplica por stream es
configuración de la instancia (Helm). Viven en herramientas distintas, ninguna de las
cuales puede ver a la otra, y elevar solo la primera es el modo de fallo que este flag
existe para evitar: un clúster de tres nodos cuyos streams siguen siendo de una sola
réplica cuesta el triple de cómputo, informa tres pares saludables y no sobrevive a nada.

:::caution Sobrevive exactamente a la pérdida de UN nodo
Tres servidores confirman por mayoría, de modo que dos siguen siendo quórum y uno no.
Perder un segundo nodo —incluido perder uno por una actualización continua de nodos
mientras otro ya está caído— detiene las escrituras hasta que un nodo regrese. Planifica
el mantenimiento de un nodo a la vez. Sobrevivir a dos pérdidas simultáneas requiere un
clúster de 5 servidores, que hoy no es una topología soportada.
:::

**Tres nodos planificables, no tres nodos.** Los servidores llevan una restricción dura de
antiafinidad, así que si el clúster no puede colocar uno por nodo el excedente queda en
`Pending` en lugar de duplicarse: réplicas colocadas en el mismo nodo costarían lo que
cuesta la replicación sin proteger de nada. `dcctl` cuenta los nodos planificables y
rechaza la operación antes de aprovisionar nada. En un clúster `kind` local esto significa
**tres workers**: kind solo elimina el taint del plano de control en un clúster de un solo
nodo, de modo que un plano de control más dos workers es un clúster de tres nodos con dos
nodos utilizables.

**Lo que también hace.** Ejecuta la base de datos relacional como tres instancias con
replicación síncrona, detrás del mismo nombre de host `dc-postgresql` que los clientes ya
usan: ese nombre lo mantiene el operador y sigue a la instancia primaria a través de una
conmutación por error, de modo que no cambia la configuración de ningún servicio.

La replicación síncrona es lo que obliga a *tres* instancias en lugar de dos. Una réplica
en espera debe confirmar cada escritura, así que con solo dos instancias la pérdida de
cualquiera de ellas detiene todas las escrituras: peor disponibilidad que un solo nodo, a
cambio de mayor durabilidad. Una tercera instancia permite perder una réplica sin que el
clúster se quede sin réplica confirmadora.

El almacén de eventos también se replica en tres instancias, pero con una diferencia
deliberada: **no** retiene una escritura a la espera de una réplica. Si no hay ninguna
disponible, vuelve a la replicación asíncrona y se pone al día cuando reaparece una. Esa
concesión es la correcta para este almacén y la equivocada para el otro. Los eventos ya se
conservan de forma duradera aguas arriba en la capa de mensajería hasta que se persisten,
así que las escrituras de una conmutación por error pueden reproducirse; el registro de
auditoría del almacén relacional no tiene ese respaldo, y por eso él sí se detiene. El
costo es que el punto de recuperación del almacén de eventos queda acotado por el retraso
de replicación en lugar de ser cero.

**Lo que no hace.** El número de réplicas de servicio no cambia, y nada de esto sobrevive
por sí solo a la pérdida de un nodo: la replicación es lo que hace posible la recuperación,
no lo que la ejecuta.

:::caution Una escritura detenida queda confirmada, no rechazada
Esto se aplica al almacén relacional, que es el que se detiene.

Cuando no hay ninguna réplica en espera disponible, una escritura no falla: espera, y la
fila ya se ha confirmado localmente. Un cliente que se rinda y reintente escribirá dos
veces salvo que la operación sea idempotente. Ten en cuenta además que `statement_timeout`
**no** acota esa espera, porque la espera ocurre después de la confirmación y no durante
la sentencia.
:::

#### Cómo verificarlo {#verifying-it}

Una afirmación de alta disponibilidad vale solo lo que el broker realmente sostiene, así
que compruébalo ahí y no en la configuración renderizada:

```bash
dcctl ha verify --instance mi-instancia
```

Esto lee el broker en vivo y verifica que cada stream, bucket KV y consumidor durable
lleva el factor de réplica declarado **con todos los pares al día**, y que los tres
servidores están en tres nodos distintos. Termina con código distinto de cero si algo se
queda corto, e imprime qué examinó para que un resultado correcto sobre un conjunto vacío
no se confunda con un éxito real.

## Después del arranque inicial

El comando imprime el namespace, la credencial de **superusuario**, y cómo
llegar a la instancia a través del ingress del clúster. El superusuario se
siembra con una contraseña por defecto —**cámbiala de inmediato**.

La instancia incluye la **consola web**: el ingress la sirve en la raíz del host
(`https://<host>/`) y enruta `https://<host>/api/<area>/graphql` a cada servicio
de área funcional. Abre la consola en un navegador e inicia sesión con el correo
electrónico y la contraseña del superusuario. Una instancia recién creada
**no tiene inquilinos** (tenant-less), así que aterrizas en la consola de
administración (`/admin`) para crear tu primer inquilino y asignar membresías;
cambia a un inquilino para llegar a la consola del inquilino. (Para una instancia
headless/solo-ingesta, despliega con la consola deshabilitada —ver el valor
`frontend.enabled` del chart.)

Para inspeccionar la instancia en ejecución:

```bash
kubectl --context <kube-context> get pods -n my-instance
```

Para explorar la consola con una flota en movimiento en lugar de una vacía,
ejecute una **simulación**. `sim create` acuña una identidad y un tenant acotados
en la instancia y escribe el archivo de handshake que el proceso `dc-simulator`
lee al arrancar:

```bash
dcctl sim create demo --instance my-instance --server localhost
```

El simulador inyecta entonces telemetría y alarmas por el mismo cable de
dispositivo que usa el hardware real — véase [Probarlo con datos
simulados](../intro.md#probarlo-con-datos-simulados).
