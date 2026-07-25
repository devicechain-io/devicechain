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

1. **Renderizar la configuración** — resuelve el id de la instancia, el namespace,
   el perfil y una contraseña de base de datos generada.
2. **Aplicar la infraestructura** — ejecuta `tofu apply` sobre la configuración de
   OpenTofu incrustada (NATS, PostgreSQL, TimescaleDB, ingress de NGINX,
   cert-manager) vía [terraform-exec](https://github.com/hashicorp/terraform-exec).
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

## Prerrequisitos {#prerequisites}

- **Un clúster de Kubernetes y un kube-context** que apunte a él. Para el
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
  certificado (mantener TLS conserva también cert-manager —ver más abajo).

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

Carga datos de ejemplo en una instancia en ejecución a través de la API con:

```bash
dcctl seed construction --server localhost --instance my-instance
```
