---
sidebar_position: 1
title: Arranque inicial de una instancia
---

# Arranque inicial de una instancia

Un despliegue de DeviceChain se construye con dos comandos. `dcctl install` prepara un
clúster **una sola vez**, y `dcctl bootstrap` levanta en él una instancia completa de
DeviceChain —su infraestructura y todas las cargas de trabajo de servicio— tantas veces
como instancias quieras:

```bash
dcctl install local
dcctl bootstrap local my-instance
```

`dcctl` lleva su propio **contenido**: la configuración de infraestructura de
OpenTofu, el chart de Helm y los manifiestos del operador están todos incrustados
en él, así que nunca necesitas un checkout del árbol de código fuente ni `git`
para desplegar.

Lo que no lleva son las **herramientas**. `install` y `bootstrap` ejecutan `docker`,
`kubectl`, `helm` y `tofu` (o `terraform`) como binarios en tu `PATH`, y en el proveedor
`local` también `kind`. Comprueban que estén todos antes de empezar y se detienen si
falta alguno, así que instálalos primero —la lista completa, y para qué se usa
cada uno, está en [Prerrequisitos](#prerequisites).

:::note Estado
DeviceChain está en fase previa al lanzamiento (pre-release). `dcctl install local` y
`dcctl bootstrap local` están implementados y validados de extremo a extremo en Kubernetes
local (kind). `dcctl install local` crea el clúster de kind por ti si no existe ninguno —te
pregunta antes, salvo que pases `--yes`. El proveedor `gcp` es una mejora planificada.
:::

## Instalar el clúster {#install}

`dcctl install <provider>` prepara un clúster para alojar instancias de DeviceChain. Instala
los requisitos previos que comparten todas las instancias del clúster, en el namespace
`dc-system`:

- el operador CloudNativePG;
- la base de datos relacional (`dc-rdb`), que contiene una base de datos por instancia;
- el almacén de objetos al que se archivan los respaldos de base de datos;
- cert-manager;
- la monitorización (Prometheus y Grafana —consulta [Observabilidad](./observability.md));
- el controlador de ingress.

Además crea la identidad de base de datos base con la que se crea el login de base de datos
propio de cada instancia, y registra la instalación en el clúster.

En qué clúster instala:

- **`local`** — `--cluster <name>` (por defecto `devicechain`) nombra un clúster de kind.
  Si no existe ninguno con ese nombre, `install` lo crea, preguntando antes salvo que pases
  `--yes`; si existe, lo reutiliza.
- **Cualquier proveedor** — `--kube-context <ctx>` instala en un clúster que ya existe.
  `dcctl` nunca crea ni elimina un clúster al que se llega de este modo.

Volver a ejecutar `install` contra el mismo clúster lo hace converger: lo que ya está en su
sitio se deja como está, y lo que falta se añade. **Cambiar sus ajustes** —`--ha`,
`--compact`, la monitorización, los respaldos— se rechaza mientras exista alguna instancia
en el clúster, porque cada instancia se construyó con los ajustes vigentes cuando se
arrancó, y ninguna se reconstruye cuando cambian. Eso incluye el archivo externo: un
`--backup-credentials-file` que nombre otro endpoint u otro bucket del almacén de eventos
también se rechaza, porque el almacén de eventos de cada instancia sigue archivando en el
que tenía cuando se construyó.

Hay una excepción: una nueva ejecución **puede aumentar** `--max-connections` mientras hay
instancias en marcha (consulta [el presupuesto de conexiones](#connection-budget)).
Reducirlo se rechaza como cualquier otro cambio, y una nueva ejecución que no pase
`--max-connections` conserva el presupuesto que ya tiene el clúster.

`--no-tls` en `dcctl install` solo tiene sentido junto con `--compact`, donde descarta
cert-manager; sin `--compact` se rechaza. Para servir una instancia por HTTP simple, pasa
`--no-tls` a `dcctl bootstrap`.

Los flags se enumeran en [Flags de instalación](#install-flags). En un portátil:

```bash
dcctl install local --dev
dcctl bootstrap local devicechain --dev
```

`dcctl bootstrap` **se niega** en un clúster donde `dcctl install` no ha terminado, y la
negativa nombra el comando de instalación que hay que ejecutar. `dcctl upgrade` se niega en
el mismo caso.

Todavía no hay ningún comando que desinstale los requisitos previos. [`dcctl destroy`](#destroy)
elimina una instancia y los deja en su sitio. Para eliminar un clúster local que creó
`dcctl install`, bórralo con kind:

```bash
kind delete cluster --name devicechain
docker rm -f kind-registry   # el registro de imágenes local, si usaste --build
```

### El presupuesto de conexiones {#connection-budget}

La base de datos relacional tiene un número fijo de conexiones, fijado por
`--max-connections` (por defecto `600`). Cada instancia reserva un límite de conexiones en
su login de base de datos, dimensionado a partir de las áreas que habilita, y
`dcctl bootstrap` rechaza una instancia cuya reserva no cabe en lo que queda. Esa
comprobación se hace **antes de escribir nada de la instancia** —ni namespace, ni base de
datos, ni login—, así que un arranque rechazado no deja nada que limpiar. Un clúster pensado
para alojar muchas instancias, o instancias con muchas áreas habilitadas, necesita un
presupuesto mayor.

El presupuesto es el único ajuste de instalación que puede cambiar con instancias en marcha,
y solo hacia arriba: vuelve a ejecutar `dcctl install` con un `--max-connections` mayor. No
sale gratis. Cambiar el límite de conexiones de la base de datos reinicia sus instancias de
base de datos una a una; en un clúster instalado sin `--ha` solo hay una, así que **todas las
instancias del clúster pierden brevemente su base de datos** mientras se reinicia. Hazlo en
un momento tranquilo.

## Qué hace {#what-it-does}

El arranque inicial se ejecuta como una canalización (pipeline) ordenada que **construye**
una instancia, y te indica qué paso falló si alguno lo hace.

Es un verbo de creación. Todas las credenciales de la instancia se acuñan aquí —las
contraseñas de las bases de datos, la autoridad y los inicios de sesión del bróker, el secreto
entre servicios, la clave raíz del almacén de secretos— porque ninguna de ellas existe
todavía. Apúntalo a una instancia que ya esté en marcha y se detiene en el paso 3 —antes de
tocar el operador, la infraestructura o el chart— y nombra el comando que sí mueve una
instancia viva: `dcctl upgrade`, descrito en
[Versiones y actualizaciones](./releases-and-upgrades.md#zero-downtime-upgrades).

Una ejecución que *falló* a mitad de camino es otro caso distinto, y volver a ejecutarla sigue
siendo la forma de repararla. Lo que el paso 3 rechaza es una instancia **viva**, que reconoce
por el documento de configuración que se escribe en el paso 9 —de modo que todo lo que se
quede antes de eso es una instancia a medio construir, y volver a ejecutar el arranque inicial
es la manera admitida de terminarla.

:::warning Excepción única: instancias creadas antes de que la base de datos pasara a CloudNativePG
La base de datos relacional pasó de ser un StatefulSet a un clúster de CloudNativePG, y no
existe una actualización en sitio: el directorio de datos de un StatefulSet no puede ser
adoptado por el operador. En una instancia creada antes de ese cambio, el arranque inicial
**se niega** y te indica cómo volcar los datos o descartarlos deliberadamente. Esa negativa es
justamente el objetivo: sin ella la base de datos antigua se eliminaría y una nueva, vacía,
ocuparía el mismo nombre de host, dejando una instancia que parece perfectamente sana y no
tiene ningún dato.

Esta es la única razón documentada para ejecutar `dcctl bootstrap` contra una instancia que ya
está viva, así que `--allow-legacy-db-removal` queda exceptuado tanto de la negativa del paso
3 como de esta. Nada más lo está.

El flag se divide por la misma línea que las bases de datos: la base de datos relacional
pertenece al clúster, así que la cubre `dcctl install --allow-legacy-db-removal`, y el
almacén de eventos pertenece a la instancia, así que lo cubre
`dcctl bootstrap --allow-legacy-db-removal`.
:::

**Varias instancias en un mismo clúster.** Un clúster puede alojar más de una instancia de
DeviceChain. Los servicios de cada instancia, su bróker (NATS), su almacén de eventos
(TimescaleDB) y sus credenciales viven en un namespace que lleva el nombre de la instancia,
y cada instancia se conecta a la base de datos relacional compartida con un login propio
que es dueño de exactamente una base de datos, de modo que ninguna instancia puede llegar a
los datos de otra. Lo que comparten las instancias son los requisitos previos del clúster:
el controlador de ingress, cert-manager, el operador CloudNativePG, la monitorización, la
base de datos relacional y el almacén de objetos de los respaldos.
[`dcctl install`](#install) los instala una vez; cada arranque inicial los reutiliza y sigue
los ajustes con los que se instaló el clúster: alta disponibilidad, tamaño compacto,
monitorización y respaldos.

Dos cosas de un clúster solo pueden pertenecer a una instancia, y el arranque inicial se
ocupa de ambas:

- **El host del ingress.** Un controlador de ingress al que se le dan dos instancias en un
  mismo host solo sirve una de ellas, en silencio. Un arranque inicial cuyo host ya sirve
  otra instancia se rechaza; dale a cada instancia su propio `--host` (en un clúster local,
  por ejemplo `--host beta.localhost`).
- **El puerto MQTT local.** En un clúster local, el puerto 1883 de tu máquina llega solo al
  bróker de la primera instancia. Los brókeres de las instancias posteriores son
  alcanzables desde dentro del clúster, y el arranque inicial lo indica cuando ocurre.

Los nombres de instancia son letras minúsculas, dígitos y `-`, de 50 caracteres como
máximo, porque el nombre es también el namespace de la instancia, su base de datos y el
login de esa base de datos.

Una instancia construida antes de que cada instancia tuviera su propio namespace ejecuta su
bróker y su almacén de eventos en el namespace compartido `dc-system`, y no se pueden mover
en sitio. El arranque inicial rechaza una instancia así e indica que hay que destruirla y
volver a arrancarla.

Los pasos de abajo son los que la ejecución va imprimiendo (`[5/12] Install core
components`), de modo que un fallo nombra un paso que puedes encontrar aquí:

1. **Asegurar el registro local** (*Ensure local registry*) — solo en la ruta de
   desarrollo `--build`: aprovisiona un registro local y compila todas las imágenes en
   él. En la ruta de imágenes publicadas no hace nada y lo indica. Va primero porque el
   operador que se instala cuatro pasos después nombra una imagen, y en la ruta `--build`
   este es el paso que la produce.
2. **Reclamar el clúster** (*Claim the cluster*) — crea el namespace del operador y toma
   el **bloqueo del clúster**, antes de aplicar nada. Mientras está tomado, un segundo
   `dcctl bootstrap` contra el mismo clúster se rechaza en lugar de aplicarse
   silenciosamente por encima de este. Consulta [El bloqueo del clúster](./cluster-lock.md)
   —esa página también explica qué hacer cuando resulta que el clúster está reclamado por
   otra persona.
3. **Rechazar una reconstrucción** (*Refuse a rebuild*) — pregunta al clúster si esta
   instancia ya está viva y se detiene si lo está. Su posición es deliberada por ambos lados:
   *después* del bloqueo, porque un arranque inicial concurrente es justamente lo que dejaría
   obsoleta la respuesta entre leerla y actuar sobre ella, y *antes* de que se aplique nada de
   la instancia, porque todos los pasos que vienen debajo escriben en un clúster que puede
   estar ejecutando ya la instancia sobre la que escribirían. Una ejecución en seco dice qué
   rechazaría una ejecución real, en lugar de ocultarlo.
4. **Comprobar lo que tienen otras instancias** (*Check what other instances hold*) — pregunta
   al clúster qué host de ingress y qué puerto MQTT local tienen ya otras instancias, y se
   detiene si el host de esta instancia es uno de ellos. Se ejecuta antes de escribir nada,
   así que una negativa no deja nada detrás; un puerto MQTT local que tiene otra instancia no
   detiene la ejecución, y se indica. Una ejecución en seco dice qué rechazaría una ejecución
   real. Consulta **Varias instancias en un mismo clúster** más arriba.
5. **Instalar los componentes del núcleo** (*Install core components*) — renderiza el
   operador (CRDs + RBAC + controlador) y lo aplica directamente con la API de
   Kubernetes. Va por delante de la aplicación de infraestructura porque la definición de
   una instancia debe existir en el clúster antes de que nada pueda describirle una —y
   describir una es justamente el paso siguiente.
6. **Declarar la instancia** (*Declare the instance*) — escribe la **declaración** de la
   instancia en el clúster: el proveedor y el clúster al que pertenece, el perfil, la
   versión de imagen y si sus bases de datos se están recuperando desde un archivo. Acto
   seguido se vuelve a leer, y todos los pasos siguientes trabajan con lo que se leyó y no
   con los flags que lo produjeron —de modo que el registro de lo que es esta instancia
   está en el clúster, no en tu portátil. Consulta
   [la declaración de la instancia](./kubernetes-operator.md#instance-declaration).
7. **Renderizar la configuración** (*Render configuration*) — resuelve el id de la
   instancia, el namespace, el perfil y todas las credenciales generadas: el material de
   autenticación del bróker (la contraseña de servicio compartida y la clave del emisor
   del callout), la autoridad certificadora que firma el propio certificado TLS del bróker,
   el secreto de autenticación entre servicios y la **clave raíz del almacén de secretos**.
   Todas se acuñan aquí, porque el paso 3 ha establecido que no hay ninguna instancia viva de
   la que tomarlas. Terminar una instancia a medio construir es la excepción: ahí el paso
   vuelve a leer lo que una ejecución anterior ya dejó en el clúster en lugar de generar un
   segundo juego. También registra las credenciales del bróker en la máquina desde la
   que lo ejecutas, antes de configurar el bróker con ellas, de modo que una ejecución
   interrumpida a mitad de camino se retome con solo volver a ejecutarla —el bróker se
   configura antes que la instancia, y sus credenciales ya no se pueden recuperar del clúster
   una vez están en él. Además, la clave raíz se deposita en un archivo cifrado que tú
   conservas; consulta [Recuperación ante desastres](./disaster-recovery.md).
8. **Aplicar la infraestructura** (*Apply infrastructure*) — ejecuta `tofu apply` sobre la
   configuración de OpenTofu incrustada vía
   [terraform-exec](https://github.com/hashicorp/terraform-exec), solo para esta instancia:
   su propio bróker (NATS) y almacén de eventos (TimescaleDB) en su namespace, con el estado
   guardado en `~/.devicechain/instances/<instance>/infra`. El paso también crea el login y
   la base de datos propios de la instancia en la base de datos relacional compartida. Los
   requisitos previos compartidos del clúster no se aplican aquí: los dejó en su sitio
   [`dcctl install`](#install). Las ejecuciones posteriores son incrementales.
9. **Instalar la instancia (Helm)** (*Install instance (Helm)*) — escribe el **documento de
   configuración** de la instancia —del que cada servicio lee sus credenciales y sus
   endpoints— y después despliega el chart de Helm vía el SDK de Helm para Go, bloqueando
   hasta que las cargas de trabajo estén listas. Ese documento es lo que hace que la instancia
   esté viva, y lo que el paso 3 busca en cualquier ejecución posterior.
10. **Sembrar la credencial de administración** (*Seed admin credential*) — la credencial
   de superusuario la siembra el servicio user-management en el primer arranque; este paso
   fija los valores que imprimirá el informe final.
11. **Esperar a que todo esté listo** (*Wait for readiness*) — sondea el Deployment de cada
    área habilitada hasta que haya terminado de desplegarse sobre la configuración que ha
    producido esta ejecución, como puerta de confirmación explícita en lugar de confiar en
    la espera del propio paso de Helm. Que haya réplicas disponibles no basta: cuando se
    están sustituyendo pods eso ya es cierto de los que están de salida, así que el paso
    espera además a que se observe la nueva plantilla, a que todas las réplicas se hayan
    recreado sobre ella y a que no quede ninguna réplica antigua en ejecución. `dcctl
    upgrade` usa la misma puerta por la misma razón.
12. **Informar de los datos de acceso** (*Report access info*) — imprime el namespace, la
    credencial de superusuario y cómo llegar a la instancia.

:::tip `Ctrl+C` detiene una ejecución de forma limpia
Una ejecución interrumpida detiene la herramienta de infraestructura con elegancia —termina
lo que está haciendo y escribe su estado— y devuelve el bloqueo del clúster, así que basta
con volver a ejecutarla. Un **segundo** `Ctrl+C` sale de inmediato y renuncia a ambas cosas.
Consulta [Interrumpir una ejecución](./cluster-lock.md#interrupt).

Si la ejecución ya había llegado al paso 9, la instancia existe y el arranque inicial se
negará la próxima vez que lo ejecutes. Eso no es un callejón sin salida: la instancia está
construida, y `dcctl upgrade` es como se mueve a partir de ahí.
:::

Dado que los artefactos incrustados son los *mismos* que distribuye la
plataforma, una instancia arrancada de este modo ejercita el despliegue real —no
puede desviarse de un despliegue de producción.

:::info El destino de respaldo predeterminado es un componente AGPL
Los respaldos de base de datos necesitan un destino y, de forma predeterminada, ese destino
es un **MinIO** de una sola réplica en el namespace `dc-system`, instalado por
`dcctl install`, para que una instalación estándar produzca instancias cuyo log de escritura
anticipada (WAL) se archive de verdad, en lugar de instancias que lleven un plugin de
respaldo sin ningún sitio donde escribir.

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
así que no puede constituir recuperación ante desastres. Pasa `--backup-credentials-file` a
`dcctl install` para nombrar un almacén de objetos que ya tengas; consulta
[Recuperación ante desastres](./disaster-recovery.md) y el `backup_destination` de la
configuración de OpenTofu.
:::

## Prerrequisitos {#prerequisites}

- **Un clúster de Kubernetes, versión 1.29 o más reciente**, y un kube-context que
  apunte a él. El mínimo proviene de los charts de CloudNativePG, que se niegan a
  instalarse por debajo de esa versión; `dcctl preflight` lo verifica por
  adelantado, porque de lo contrario el fallo aparece a mitad de un levantamiento
  que ya ha escrito tu archivo de custodia (escrow) de la clave raíz. Para el
  proveedor `local` esto es un clúster de kind, que `dcctl install local` crea por
  ti (`--cluster <name>`, por defecto `devicechain`); pasa `--kube-context <name>`
  para usar en su lugar un clúster que ya tengas (kind / minikube / k3d /
  docker-desktop).
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
| `--cluster <name>` | Proveedor `local`: el clúster de kind en el que crear la instancia (por defecto `devicechain`). Debe estar ya [instalado](#install); el arranque inicial nunca crea un clúster. |
| `--kube-context <name>` | Apunta a un clúster instalado a través de este kube-context. |
| `--profile <profile>` | Perfil de área funcional: `default` (el sistema estándar, usado cuando se omite), `full` (todo —añade inferencia de IA, conectores salientes y MCP), `telemetry`, o `ingest-only`. |
| `--build` | Compila las imágenes desde el código fuente en un registro local (ruta para desarrolladores; necesita el árbol de código fuente + Docker + ko). |
| `--registry` / `--version` | Sobrescribe el registro/etiqueta de imagen (por defecto: `ghcr.io/devicechain-io` publicado, o `localhost:5000` + `dev` con `--build`). |
| `--host <name>` | Host de ingress en el que exponer la instancia (por defecto `devicechain.local`). Usa `localhost` en un clúster local para llegar a la consola **sin editar `/etc/hosts`**. |
| `--no-tls` | Sirve HTTP simple en lugar de un certificado autofirmado. Con `--host localhost`, un `http://localhost/` sin configuración adicional (sin advertencia de certificado). En un clúster instalado sin cert-manager está activado por defecto, y `--no-tls=false` se rechaza: no hay nada que emita el certificado. |
| `--dry-run` | Imprime lo que haría cada paso sin cambiar nada. Una ejecución en seco no toma el bloqueo del clúster; sí informa de si otro operador está reteniendo el clúster. |
| `--skip-preflight` | Omite las comprobaciones de entorno. |

## Flags de instalación {#install-flags}

Estos son flags de [`dcctl install`](#install). Describen el clúster, y toda instancia
arrancada en él los sigue; ninguno es un flag de `dcctl bootstrap`.

| Flag | Propósito |
|------|-----------|
| `--cluster <name>` | Proveedor `local`: el clúster de kind en el que instalar (por defecto `devicechain`), que se crea si no existe. |
| `--kube-context <name>` | Instala en el clúster existente al que apunta este kube-context. `dcctl` nunca lo crea ni lo elimina. |
| `--compact` | Preajuste de huella pequeña —ver más abajo. |
| `--ha` | Alta disponibilidad —ver más abajo. Requiere al menos **3 nodos planificables**. |
| `--no-tls` | Con `--compact`: no instala cert-manager y, por tanto, tampoco respaldos de base de datos. `--compact --no-tls=false` conserva ambos. Se rechaza sin `--compact`: usa `dcctl bootstrap --no-tls` para servir una instancia por HTTP simple. |
| `--no-monitoring` | Omite la pila de monitoreo (Prometheus y Grafana). |
| `--no-cnpg` | Omite el operador CloudNativePG y el plugin de respaldo de base de datos. Para un clúster que **ya ejecuta CloudNativePG**: Helm no puede adoptar objetos creados por otro instalador, así que sin esta bandera la instalación falla. |
| `--backup-credentials-file <path>` | Envía los respaldos de base de datos a un almacén de objetos que ya tengas, descrito por un archivo JSON, en lugar del que hay dentro del clúster. Consulta [Recuperación ante desastres](./disaster-recovery.md). |
| `--max-connections <n>` | El presupuesto de conexiones de la base de datos relacional (por defecto `600` en una primera instalación; una nueva ejecución sin él conserva el presupuesto actual) —consulta [el presupuesto de conexiones](#connection-budget). Puede aumentarse, pero no reducirse, con instancias en marcha. |
| `--allow-legacy-db-removal` | La mitad relacional de la excepción única descrita en [Qué hace](#what-it-does). |
| `--dry-run` | Imprime lo que haría cada paso sin cambiar nada. Una ejecución en seco no crea ningún clúster, así que las comprobaciones que necesitan leer uno —en particular la de capacidad de nodos de `--ha`— informan de lo que no pudieron ver en lugar de hacer fallar el ensayo. Lo que sí llegan a ver sigue siendo fatal: un clúster que responde y no puede alojar `--ha` también hace fallar una ejecución en seco. |
| `--yes` | No pregunta antes de crear un clúster de kind. |
| `--skip-preflight` | Omite las comprobaciones de entorno. |
| `--dev` | Comodidad local para un clúster en un portátil; implica `--yes`. |

### `--compact`

Un preajuste para clústeres pequeños, que se elige al instalar. Compone palancas que ya
existen en lugar de añadir un eje de ajuste propio:

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

**No** cambia qué servicios se ejecutan —eso se controla en el `--profile` de cada
instancia, donde queda nombrado y visible. Un perfil *más grande* que `default` —hoy solo
`full`— es rechazado en un clúster compacto: las cifras compactas publicadas se miden sobre
`default`, así que no describirían una instancia que ejecuta tres servicios más. Los
perfiles más pequeños (`telemetry`, `ingest-only`) sí se aceptan.

Tanto TLS como el monitoreo pueden conservarse: un `--no-tls=false` o
`--no-monitoring=false` explícito en `dcctl install` se respeta, y el resto de las palancas
compactas siguen aplicándose. Mantener TLS también conserva cert-manager, que es
lo que emite el certificado. En un clúster instalado sin cert-manager, toda instancia se
sirve sin TLS: `dcctl bootstrap` activa `--no-tls` por defecto y rechaza `--no-tls=false`.

:::note Por qué `--compact --no-tls` descarta el plugin de respaldo
El plugin Barman Cloud emite sus propios certificados a través de cert-manager, así
que descartar cert-manager descarta también el plugin. Volver a activar TLS
(`--no-tls=false`) restablece ambos. Ten en cuenta que hacen falta *ambas* banderas de
instalación: `dcctl install --no-tls` sin `--compact` se rechaza —y `--no-tls` en
`dcctl bootstrap`, como en el ejemplo de URL local más abajo, solo cambia cómo se sirve esa
instancia.

El operador CloudNativePG en sí se instala en *todo* clúster, incluido el
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

:::caution Elígelo antes de la primera instancia
Bajar un techo por debajo de lo que un stream o bucket de KV ya contiene tiene
éxito silenciosamente, no trunca nada, y rechaza escrituras hasta que los datos
envejezcan y se purguen. Por eso `dcctl install` se niega a cambiar sus ajustes mientras
exista alguna instancia en el clúster: `--compact` se decide una vez, antes de que haya
nada ejecutándose bajo él.
:::

:::tip URL local sin configuración
`dcctl bootstrap local my-instance --build --host localhost --no-tls` expone la
consola en `http://localhost/` —sin entrada en el archivo hosts y sin advertencia
de certificado.
:::

### `--ha` {#ha}

Se elige al instalar, y lo sigue toda instancia del clúster. Cada instancia ejecuta su
broker de mensajería como un clúster RAFT de 3 nodos, un servidor por nodo, con
**cada stream de JetStream y cada bucket KV replicados a lo largo del clúster**. La
instancia sobrevive entonces a la pérdida de cualquier nodo sin perder mensajes, sesiones
de dispositivo ni estado en vivo.

```bash
dcctl install local --ha
dcctl bootstrap local mi-instancia
```

Ambas mitades se establecen a partir de ese único ajuste, y ese es justamente su propósito.
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

## Eliminar una instancia {#destroy}

```bash
dcctl destroy local my-instance
```

`dcctl destroy` elimina **solo esa instancia**: su release de Helm, su base de datos y su
login de base de datos, su namespace y su estado local en
`~/.devicechain/instances/<instance>/`. El artefacto de depósito (escrow) de la clave raíz se
conserva —consulta
[Recuperación ante desastres](./disaster-recovery.md#after-destroy). Nunca elimina el
clúster ni los requisitos previos que dejó `dcctl install`, así que el siguiente
`dcctl bootstrap` en el clúster no necesita instalar antes. Destruir una instancia y volver
a arrancarla con el mismo nombre es como se recrea una instancia. Una instancia construida
por una versión anterior, de antes de que existiera `dcctl install`, es la excepción: también
hay que recrear su clúster —consulta
[Versiones y actualizaciones](./releases-and-upgrades.md#pre-declaration-recreate).

Todavía no hay ningún comando de desinstalación. Para eliminar un clúster local que creó
`dcctl install`, usa kind directamente, como se muestra en [Instalar el clúster](#install).
