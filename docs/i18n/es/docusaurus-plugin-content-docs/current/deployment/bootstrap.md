---
sidebar_position: 1
title: Arranque inicial de una instancia
---

# Arranque inicial de una instancia

Un despliegue de DeviceChain se construye con dos comandos. `dcctl install` prepara un
clúster una sola vez. Después, `dcctl bootstrap` levanta en él una instancia completa de
DeviceChain —su infraestructura y todas las cargas de trabajo de servicio— tantas veces como
instancias quieras:

```bash
dcctl install local
dcctl bootstrap local my-instance
```

`dcctl` lleva su propio contenido. La configuración de infraestructura de OpenTofu, el chart
de Helm y los manifiestos del operador están todos incrustados en él, así que nunca necesitas
un checkout del árbol de código fuente ni `git` para desplegar.

Lo que no lleva son sus propias herramientas. `install` y `bootstrap` ejecutan `docker`,
`kubectl`, `helm` y `tofu` (o `terraform`) como binarios en tu `PATH`, y además `kind` en el
proveedor `local`. Ambos comandos comprueban que estén todos antes de empezar y se detienen
si falta alguno, así que instálalos primero. La lista completa está en
[Prerrequisitos](#prerequisites).

:::note Estado
DeviceChain está en fase previa al lanzamiento. `dcctl install local` y `dcctl bootstrap local`
están implementados y validados de extremo a extremo en Kubernetes local (kind).
`dcctl install local` crea el clúster de kind por ti si no existe ninguno, y te pregunta antes
salvo que pases `--yes`. El proveedor `gcp` es una mejora planificada.
:::

## Instalar el clúster {#install}

`dcctl install <provider>` prepara un clúster para alojar instancias de DeviceChain. Instala
los requisitos previos que comparten todas las instancias del clúster.

En el namespace `dc-k8s-system` instala el **operador de DeviceChain** y las dos definiciones
de recurso personalizado que reconcilia, `Instance` e `InstanceConfiguration`. Son lo que hace
que un clúster pueda alojar una instancia: `dcctl bootstrap` declara un `Instance`, y no puede
declarar uno en un clúster donde la definición no existe. Consulta
[el operador](./kubernetes-operator.md).

En el namespace `dc-system` instala:

- la base de datos relacional (`dc-rdb`), que contiene una base de datos por instancia;
- el almacén de objetos al que se archivan los respaldos de base de datos.

Cada uno en un namespace propio, instala:

- el operador CloudNativePG (`cnpg-system`);
- cert-manager (`cert-manager`);
- la monitorización (Prometheus y Grafana, en `monitoring` —consulta [Observabilidad](./observability.md));
- el controlador de ingress (`ingress-nginx`).

El operador es un solo controlador por clúster, compartido por todas las instancias que haya
en él. Por eso instalarlo es tarea del comando del clúster y no de cada instancia, y por eso
mover un clúster a una versión nueva empieza aquí: consulta
[Versiones y actualizaciones](./releases-and-upgrades.md#zero-downtime-upgrades).

`install` también crea la identidad de base de datos base con la que se crea el login de base
de datos propio de cada instancia, y registra la instalación en el clúster.

En qué clúster instala:

- **`local`** — `--cluster <name>` (por defecto `devicechain`) nombra un clúster de kind. Si
  no existe ninguno con ese nombre, `install` lo crea, preguntando antes salvo que pases
  `--yes`. Si existe, lo reutiliza.
- **Cualquier proveedor** — `--kube-context <ctx>` instala en un clúster que ya existe.
  `dcctl` nunca crea ni elimina un clúster al que se llega de este modo.

### Volver a ejecutar install {#re-running-install}

Volver a ejecutar `install` contra el mismo clúster lo hace converger: lo que ya está en su
sitio se deja como está, y lo que falta se añade.

**Cambiar sus ajustes** —`--ha`, `--compact`, la monitorización, los respaldos— se rechaza
mientras exista alguna instancia en el clúster. Cada instancia se construyó con los ajustes
vigentes cuando se arrancó, y ninguna se reconstruye cuando cambian. Eso incluye el archivo
externo: un `--backup-credentials-file` que nombre otro endpoint u otro bucket del almacén de
eventos también se rechaza, porque el almacén de eventos de cada instancia sigue archivando en
el que tenía cuando se construyó.

Hay una excepción: una nueva ejecución puede aumentar `--max-connections` mientras hay
instancias en marcha (consulta [el presupuesto de conexiones](#connection-budget)). Reducirlo
se rechaza como cualquier otro cambio. Una nueva ejecución que no pase `--max-connections`
conserva el presupuesto que ya tiene el clúster.

### Dónde guarda install su estado {#install-state}

Los requisitos previos se aplican con OpenTofu, y su estado vive en la máquina que ejecutó
`dcctl install`, en `~/.devicechain/clusters/<cluster-uid>/infra`.

El directorio se indexa por la identidad del clúster —el UID de su namespace `kube-system`— y
no por su nombre. Un clúster de kind borrado y vuelto a crear lleva el mismo nombre de contexto
sin contener ninguno de los recursos que describe el estado antiguo. El `cluster.json` que hay
junto a `infra/` registra el nombre del clúster y el kube-context por los que tú lo conoces,
de modo que puedes emparejar un directorio con su clúster:

```bash
cat ~/.devicechain/clusters/*/cluster.json
kubectl --context <kube-context> get namespace kube-system -o jsonpath='{.metadata.uid}'
```

Una nueva ejecución trabaja a partir de ese estado, así que **un clúster ya instalado solo
puede reinstalarse desde la máquina que tiene su directorio**. Ejecuta `dcctl install` contra
él desde otra máquina y se rechaza antes de aplicar nada:

```text
cluster ... is already installed (by dcctl <version>, <time>), but this machine holds no state
for it under ~/.devicechain/clusters/<cluster-uid>. It was installed from another machine, and
re-applying from empty state would try to create every prerequisite again. Run `dcctl install`
from the machine that installed it
```

Aplicar desde un estado vacío planificaría todos los requisitos previos como nuevos y fallaría
a medias con nombres ya en uso, así que la negativa es el resultado seguro. Pero convierte ese
directorio en precondición de toda nueva ejecución descrita en esta página, incluida la que
aumenta `--max-connections`.

El directorio es la única copia del estado de los requisitos previos del clúster, y ningún
respaldo de DeviceChain lo contiene. Consérvalo en la máquina desde la que instalas. Si otra
máquina va a tomar el relevo, copia antes a ella el directorio
`~/.devicechain/clusters/<cluster-uid>/` completo. Contiene el estado de la raíz del clúster,
credenciales incluidas, así que trátalo como tratas el directorio del depósito (escrow).

### TLS, flags e instalación en un portátil {#install-tls-and-flags}

`--no-tls` en `dcctl install` solo tiene sentido junto con `--compact`, donde descarta
cert-manager. Sin `--compact` se rechaza. Para servir una instancia por HTTP simple, pasa
`--no-tls` a `dcctl bootstrap`.

Los flags se enumeran en [Flags de instalación](#install-flags). En un portátil:

```bash
dcctl install local --dev
dcctl bootstrap local devicechain --dev
```

`dcctl bootstrap` se niega en un clúster donde `dcctl install` no ha terminado, y la negativa
nombra el comando de instalación que hay que ejecutar. `dcctl upgrade` se niega en el mismo
caso.

### Eliminar un clúster {#removing-a-cluster}

Todavía no hay ningún comando que desinstale los requisitos previos. [`dcctl destroy`](#destroy)
elimina una instancia y los deja en su sitio. Para eliminar un clúster local que creó
`dcctl install`, bórralo con kind:

```bash
kind delete cluster --name devicechain
docker rm -f kind-registry   # el registro de imágenes local, si usaste --build
```

kind no sabe nada de `~/.devicechain/clusters/`, así que borrar el clúster de este modo deja
atrás su directorio. El siguiente `dcctl destroy` de una instancia que estaba en ese clúster
descubre que el clúster ya no existe y elimina el directorio junto con el estado local propio
de la instancia —consulta [Eliminar una instancia](#destroy). O elimínalo a mano, una vez que
su `cluster.json` haya confirmado a qué clúster pertenecía.

### El presupuesto de conexiones {#connection-budget}

La base de datos relacional tiene un número fijo de conexiones, fijado por `--max-connections`
(por defecto `600`). Cada instancia reserva un límite de conexiones en su login de base de
datos, dimensionado a partir de las áreas que habilita. `dcctl bootstrap` rechaza una
instancia cuya reserva no cabe en lo que queda.

Esa comprobación se hace **antes de escribir nada de la instancia** —ni namespace, ni base de
datos, ni login—, así que un arranque rechazado no deja nada que limpiar. Un clúster pensado
para alojar muchas instancias, o instancias con muchas áreas habilitadas, necesita un
presupuesto mayor.

El presupuesto es el único ajuste de instalación que puede cambiar con instancias en marcha, y
solo hacia arriba: vuelve a ejecutar `dcctl install` con un `--max-connections` mayor. No sale
gratis. Cambiar el límite de conexiones de la base de datos reinicia sus instancias de base de
datos una a una. En un clúster instalado sin `--ha` solo hay una, así que **todas las
instancias del clúster pierden brevemente su base de datos** mientras se reinicia. Hazlo en un
momento tranquilo.

`dcctl upgrade` se admite contra el mismo presupuesto. Una versión puede cambiar lo que
necesitan las áreas relacionales de una instancia, así que la actualización compara la
necesidad de esta versión con el límite que ya tiene el login de la instancia, antes de
escribir nada. Una necesidad que no ha cambiado no se vuelve a admitir. Un aumento que el
presupuesto no puede admitir se rechaza sin haber movido nada:

```text
this release needs instance "my-instance"'s database login to hold <n> connections, up from <m>,
and nothing has been changed: ... Destroy an instance, or raise the budget by re-running
`dcctl install` with a larger --max-connections
```

El remedio es el de arriba, y reinicia la base de datos. En un clúster cuyo presupuesto está
casi agotado, auméntalo en un momento tranquilo *antes* de la ventana de actualización, en
lugar de descubrir la necesidad dentro de ella.

Un aumento que cabe se aplica antes de que los servicios se desplieguen. Si una versión
necesita *menos*, el login se recorta solo cuando todos los servicios están listos en la nueva
versión. Un recorte que falla no hace fallar la actualización, porque los servicios ya están en
marcha. Se imprime como una advertencia que termina en ``re-run `dcctl upgrade` to finish it``,
y hasta que lo hagas el login retiene más presupuesto del que necesita.
`dcctl upgrade --dry-run` informa de la comprobación que haría sin iniciar sesión en el
almacén.

## Qué hace bootstrap {#what-it-does}

El arranque inicial se ejecuta como una canalización (pipeline) ordenada que construye una
instancia, y te indica qué paso falló si alguno lo hace.

Es un verbo de creación. Todas las credenciales de la instancia se acuñan aquí —las
contraseñas de las bases de datos, la autoridad y los logins del broker, el secreto entre
servicios, la clave raíz del almacén de secretos— porque ninguna de ellas existe todavía.
Apúntalo a una instancia que ya esté en marcha y se detiene en el paso 3, antes de tocar la
infraestructura o el chart. Nombra el comando que sí mueve una instancia viva:
`dcctl upgrade`, descrito en
[Versiones y actualizaciones](./releases-and-upgrades.md#zero-downtime-upgrades).

Una ejecución que *falló* a mitad de camino es un caso distinto, y volver a ejecutarla sigue
siendo la forma de repararla. El paso 3 solo rechaza una instancia **viva**, que reconoce por
el documento de configuración que se escribe en el paso 8. Todo lo que se quede antes de eso
es una instancia a medio construir, y volver a ejecutar el arranque inicial es la manera
admitida de terminarla.

:::warning Excepción única: instancias creadas antes de que la base de datos pasara a CloudNativePG
La base de datos relacional pasó de ser un StatefulSet a un clúster de CloudNativePG, y no
existe una actualización en sitio. En una instancia creada antes de ese cambio, el arranque
inicial **se niega** y te indica cómo volcar los datos o descartarlos deliberadamente.
Consulta [la excepción de la base de datos heredada](#legacy-db-removal).
:::

### La excepción de la base de datos heredada {#legacy-db-removal}

El directorio de datos de un StatefulSet no puede ser adoptado por el operador, y por eso no
existe una actualización en sitio. La negativa es justamente el objetivo: sin ella, la base de
datos antigua se eliminaría y una nueva, vacía, ocuparía el mismo nombre de host, dejando una
instancia que parece perfectamente sana y no tiene ningún dato.

Esta es la única razón documentada para ejecutar `dcctl bootstrap` contra una instancia que ya
está viva, así que `--allow-legacy-db-removal` queda exceptuado tanto de la negativa del paso 3
como de esta. Nada más lo está.

El flag se divide por la misma línea que las bases de datos. La base de datos relacional
pertenece al clúster, así que la cubre `dcctl install --allow-legacy-db-removal`. El almacén de
eventos pertenece a la instancia, así que lo cubre `dcctl bootstrap --allow-legacy-db-removal`.

### Varias instancias en un mismo clúster {#several-instances}

Un clúster puede alojar más de una instancia de DeviceChain. Los servicios de cada instancia,
su broker (NATS), su almacén de eventos (TimescaleDB) y sus credenciales viven en un namespace
propio, que lleva el nombre de la instancia detrás del prefijo `dci-`: la instancia
`devicechain` se ejecuta en el namespace `dci-devicechain`.

El prefijo impide que el namespace de una instancia choque alguna vez con uno de los que usa
el propio clúster. `monitoring`, `cert-manager`, `ingress-nginx` y los demás quedan fuera de
alcance por construcción, así que ningún id de instancia puede ocupar uno de ellos.

Cada instancia se conecta a la base de datos relacional compartida con un login propio que es
dueño de exactamente una base de datos, de modo que ninguna instancia puede llegar a los datos
de otra.

Lo que comparten las instancias son los requisitos previos del clúster: el operador de
DeviceChain y sus definiciones de recurso personalizado, el controlador de ingress,
cert-manager, el operador CloudNativePG, la monitorización, la base de datos relacional y el
almacén de objetos de los respaldos. [`dcctl install`](#install) los instala una vez. Cada
arranque inicial los reutiliza y sigue los ajustes con los que se instaló el clúster: alta
disponibilidad, tamaño compacto, monitorización y respaldos.

Dos cosas de un clúster solo pueden pertenecer a una instancia, y el arranque inicial se ocupa
de ambas:

- **El host del ingress.** Un controlador de ingress al que se le dan dos instancias en un
  mismo host solo sirve una de ellas, en silencio. Un arranque inicial cuyo host ya sirve otra
  instancia se rechaza. Dale a cada instancia su propio `--host` (en un clúster local, por
  ejemplo `--host beta.localhost`).
- **El puerto MQTT local.** En un clúster local, el puerto 1883 de tu máquina llega solo al
  broker de la primera instancia. Los brokers de las instancias posteriores son alcanzables
  desde dentro del clúster, y el arranque inicial lo indica cuando ocurre.

#### El namespace de la instancia {#instance-namespace}

El arranque inicial comprueba el namespace de la instancia en el mismo punto. Una instancia es
dueña de `dci-<id>`: dcctl escribe ahí su clave raíz, el par de claves TLS de su broker y todas
sus credenciales de base de datos, y `dcctl destroy` elimina el namespace entero. Por eso un
`dci-<id>` que ya existe y no lleva la etiqueta `devicechain.io/instance=<id>` se rechaza antes
de escribir nada de eso.

Nada salvo dcctl crea namespaces bajo `dci-`. La causa probable es un `dcctl destroy` anterior
de esta misma instancia que no terminó. La negativa lo dice y nombra el comando para
terminarlo, `dcctl destroy <provider> <id>`. Un namespace que aún se está eliminando también se
rechaza, hasta que desaparece.

Si el namespace lo creaste tú a propósito —para llevar una cuota, una política o un RBAC
propios—, entrégaselo a la instancia y vuelve a ejecutar el arranque inicial:

```bash
kubectl label namespace dci-<id> devicechain.io/instance=<id>
```

#### Nombres de instancia {#instance-names}

Los nombres de instancia son letras minúsculas, dígitos y `-`, de 50 caracteres como máximo.
El nombre es, tal cual, la base de datos de la instancia y el login de esa base de datos.
También es la cola de otros dos nombres: el namespace es `dci-` más el nombre, y la release de
Helm es `dc-` más el nombre. El límite de 50 caracteres sale de esa release: Helm limita el
nombre de una release a 53 caracteres, así que al nombre en sí le quedan 50.

Una instancia construida antes de que cada instancia tuviera su propio namespace ejecuta su
broker y su almacén de eventos en el namespace compartido `dc-system`, y no se pueden mover en
sitio. El arranque inicial rechaza una instancia así e indica que hay que destruirla y volver a
arrancarla.

### Los pasos del arranque inicial {#bootstrap-steps}

Estos son los pasos que la ejecución va imprimiendo (`[8/10] Install instance (Helm)`), de modo
que un fallo nombra un paso que puedes encontrar aquí:

1. **Ensure local registry** (asegurar el registro local) — solo en la ruta de desarrollo
   `--build`: aprovisiona un registro local y compila todas las imágenes en él. En la ruta de
   imágenes publicadas no hace nada y lo indica. Va primero porque el chart que se instala más
   adelante nombra esas imágenes, y en la ruta `--build` este es el paso que las produce.
2. **Claim the cluster** (reclamar el clúster) — crea el namespace del operador y toma el
   **bloqueo del clúster**, antes de aplicar nada. Mientras está tomado, un segundo
   `dcctl bootstrap` contra el mismo clúster se rechaza en lugar de aplicarse silenciosamente
   por encima de este. Consulta [El bloqueo del clúster](./cluster-lock.md), que también
   explica qué hacer cuando resulta que el clúster está reclamado por otra persona.
3. **Refuse a rebuild** (rechazar una reconstrucción) — pregunta al clúster si esta instancia
   ya está viva y se detiene si lo está. Su posición es deliberada por ambos lados. Va
   *después* del bloqueo, porque un arranque inicial concurrente es justamente lo que dejaría
   obsoleta la respuesta entre leerla y actuar sobre ella. Va *antes* de que se aplique nada de
   la instancia, porque todos los pasos que vienen debajo escriben en un clúster que puede
   estar ejecutando ya la instancia sobre la que escribirían. Una ejecución en seco dice qué
   rechazaría una ejecución real, en lugar de ocultarlo.
4. **Check what other instances hold** (comprobar lo que tienen otras instancias) — pregunta
   al clúster qué host de ingress y qué puerto MQTT local tienen ya otras instancias, y se
   detiene si el host de esta instancia es uno de ellos. También mira el namespace en el que
   está a punto de construirse esta instancia, `dci-<id>`, y se detiene si existe y no es de
   esta instancia, o si aún se está eliminando. El paso se ejecuta antes de escribir nada, así
   que una negativa no deja nada detrás. Un puerto MQTT local que tiene otra instancia no
   detiene la ejecución, y se indica. Una ejecución en seco dice qué rechazaría una ejecución
   real. Consulta [Varias instancias en un mismo clúster](#several-instances), incluida la
   etiqueta que entrega a la instancia un namespace que creaste tú.
5. **Declare the instance** (declarar la instancia) — escribe la **declaración** de la
   instancia en el clúster: el proveedor y el clúster al que pertenece, el perfil, la versión
   de imagen y si sus bases de datos se están recuperando desde un archivo. Acto seguido se
   vuelve a leer, y todos los pasos siguientes trabajan con lo que se leyó y no con los flags
   que lo produjeron. El registro de lo que es esta instancia está en el clúster, no en tu
   portátil. Consulta
   [la declaración de la instancia](./kubernetes-operator.md#instance-declaration).
6. **Render configuration** (renderizar la configuración) — resuelve el id de la instancia, el
   namespace, el perfil y todas las credenciales generadas: el material de autenticación del
   broker (la contraseña de servicio compartida y la clave del emisor del callout), la
   autoridad certificadora que firma el propio certificado TLS del broker, el secreto de
   autenticación entre servicios y la **clave raíz del almacén de secretos**. Todas se acuñan
   aquí, porque el paso 3 ha establecido que no hay ninguna instancia viva de la que tomarlas.
   Terminar una instancia a medio construir es la excepción: ahí el paso vuelve a leer lo que
   una ejecución anterior ya dejó en el clúster en lugar de generar un segundo juego.
   El paso también registra las credenciales del broker en la máquina desde la que lo
   ejecutas, antes de configurar el broker con ellas. El broker se configura antes que la
   instancia, y sus credenciales ya no se pueden recuperar del clúster una vez están en él, así
   que esto es lo que te permite retomar una ejecución interrumpida con solo volver a
   ejecutarla. Además, la clave raíz se deposita en un archivo cifrado que tú conservas;
   consulta [Recuperación ante desastres](./disaster-recovery.md).
7. **Apply infrastructure** (aplicar la infraestructura) — ejecuta `tofu apply` sobre la
   configuración de OpenTofu incrustada vía
   [terraform-exec](https://github.com/hashicorp/terraform-exec), solo para esta instancia: su
   propio broker (NATS) y almacén de eventos (TimescaleDB) en su namespace, con el estado
   guardado en `~/.devicechain/instances/<instance>/infra`. El paso también crea el login y la
   base de datos propios de la instancia en la base de datos relacional compartida. Los
   requisitos previos compartidos del clúster no se aplican aquí; los dejó en su sitio
   [`dcctl install`](#install). Las ejecuciones posteriores son incrementales.
8. **Install instance (Helm)** (instalar la instancia) — escribe el **documento de
   configuración** de la instancia, del que cada servicio lee sus credenciales y sus
   endpoints. Después despliega el chart de Helm vía el SDK de Helm para Go, bloqueando hasta
   que las cargas de trabajo estén listas. Ese documento es lo que hace que la instancia esté
   viva, y lo que el paso 3 busca en cualquier ejecución posterior.
9. **Wait for readiness** (esperar a que todo esté listo) — sondea el Deployment de cada área
   habilitada hasta que haya terminado de desplegarse sobre la configuración que ha producido
   esta ejecución. Es una puerta de confirmación explícita, en lugar de confiar en la espera
   del propio paso de Helm. Que haya réplicas disponibles no basta: cuando se están
   sustituyendo pods, eso ya es cierto de los que están de salida. Así que el paso espera
   además a que se observe la nueva plantilla, a que todas las réplicas se hayan recreado sobre
   ella y a que no quede ninguna réplica antigua en ejecución. `dcctl upgrade` usa la misma
   puerta por la misma razón.
10. **Report access info** (informar de los datos de acceso) — imprime el namespace, el correo
    del superusuario y dónde se guarda su contraseña (y la propia contraseña, una sola vez, en
    la ejecución que la generó), y cómo llegar a la instancia.

:::tip `Ctrl+C` detiene una ejecución de forma limpia
Una ejecución interrumpida detiene la herramienta de infraestructura con elegancia —termina lo
que está haciendo y escribe su estado— y devuelve el bloqueo del clúster, así que basta con
volver a ejecutarla. Un segundo `Ctrl+C` sale de inmediato y renuncia a ambas cosas. Consulta
[Interrumpir una ejecución](./cluster-lock.md#interrupt).

Si la ejecución ya había llegado al paso 8, la instancia existe y el arranque inicial se negará
la próxima vez que lo ejecutes. Eso no es un callejón sin salida: la instancia está construida,
y `dcctl upgrade` es como la mueves a partir de ahí.
:::

Dado que los artefactos incrustados son los *mismos* que distribuye la plataforma, una
instancia arrancada de este modo ejercita el despliegue real. No puede desviarse de un
despliegue de producción.

### El destino de respaldo predeterminado {#default-backup-destination}

Los respaldos de base de datos necesitan un destino. De forma predeterminada, ese destino es un
**MinIO** de una sola réplica en el namespace `dc-system`, instalado por `dcctl install`. Así,
una instalación estándar produce instancias cuyo log de escritura anticipada (WAL) se archiva
de verdad, en lugar de instancias que llevan un plugin de respaldo sin ningún sitio donde
escribir.

:::info El destino de respaldo predeterminado es un componente AGPL
MinIO se distribuye bajo licencia AGPL-3.0. La edición comunitaria de MinIO se archivó en abril de
2026 y sus propias imágenes ya no se publican, así que DeviceChain ejecuta una compilación de una
bifurcación mantenida (`cgr.dev/chainguard/minio`), fijada por digest, que tus nodos descargan de
`cgr.dev`. Una compilación con parches llega a tu clúster solo cuando una versión de DeviceChain
mueve esa fijación. Para evitar tanto la licencia como esa dependencia, apunta los respaldos a un
almacenamiento fuera del clúster.
:::

Ninguna de las dos cosas afecta a la licencia Apache-2.0 de la propia DeviceChain. La imagen se
referencia, nunca se compila, modifica ni redistribuye, y la plataforma se comunica con ella
mediante la API HTTP de S3. Pero el componente sí se ejecuta en *tu* clúster, y muchas
organizaciones no permiten software AGPL con independencia de cómo se use.

El almacenamiento fuera del clúster es la configuración de producción recomendada de todos
modos, por un motivo que nada tiene que ver con las licencias: un bucket dentro del clúster
comparte su dominio de fallo, así que no puede constituir recuperación ante desastres. Pasa
`--backup-credentials-file` a `dcctl install` para nombrar un almacén de objetos que ya tengas.
Consulta [Recuperación ante desastres](./disaster-recovery.md) y el `backup_destination` de la
configuración de OpenTofu.

## Prerrequisitos {#prerequisites}

- **Un clúster de Kubernetes, versión 1.29 o más reciente**, y un kube-context que apunte a él.
  El mínimo proviene de los charts de CloudNativePG, que se niegan a instalarse por debajo de
  esa versión. `dcctl preflight` lo verifica por adelantado, porque de lo contrario el fallo
  aparece a mitad de un arranque que ya ha escrito tu archivo de depósito (escrow) de la clave
  raíz. Para el proveedor `local` esto es un clúster de kind, que `dcctl install local` crea
  por ti (`--cluster <name>`, por defecto `devicechain`). Pasa `--kube-context <name>` para
  usar en su lugar un clúster que ya tengas (kind / minikube / k3d / docker-desktop).
- **OpenTofu** (el binario `tofu`; `terraform` también funciona) en tu `PATH`. `dcctl` lo
  ejecuta para aprovisionar infraestructura. Instálalo desde
  [opentofu.org](https://opentofu.org). Ejecuta `dcctl preflight local` para comprobar esto y
  el resto de tu entorno de antemano.
- **`docker`, `kubectl` y `helm`** en tu `PATH`, y **`kind`** para el proveedor `local`. La
  comprobación previa falla si falta alguno. Docker debería ser un motor de Docker nativo y no
  Docker Desktop, y su daemon tiene que ser accesible.
- **`ko`**, solo para compilar imágenes desde el código fuente con `--build`. La comprobación
  previa avisa, en lugar de fallar, cuando falta.

## Origen de las imágenes {#image-source}

Por defecto, el arranque inicial despliega las **imágenes publicadas** desde
`ghcr.io/devicechain-io`, sin nada que compilar:

```bash
dcctl bootstrap local my-instance
```

Si trabajas desde un checkout del código fuente, puedes compilar las imágenes desde el código y
desplegar esas en su lugar con `--build`. Compila cada servicio y el operador con
[`ko`](https://ko.build), además de la consola web con `docker build`, en un registro local, y
despliega por referencia:

```bash
# desde un checkout de código fuente; requiere Docker + ko
dcctl bootstrap local my-instance --build
```

La única diferencia entre ambos caminos es el registro desde el que los pods extraen las
imágenes. La canalización, el chart y el operador son idénticos.

## Flags de bootstrap {#useful-flags}

| Flag | Propósito |
|------|-----------|
| `--cluster <name>` | Proveedor `local`: el clúster de kind en el que crear la instancia (por defecto `devicechain`). Debe estar ya [instalado](#install); el arranque inicial nunca crea un clúster. |
| `--kube-context <name>` | Apunta en su lugar a un clúster instalado a través de este kube-context. |
| `--profile <profile>` | Perfil de área funcional: `default` (el sistema estándar, usado cuando se omite), `full` (todo: añade inferencia de IA, conectores salientes, MCP, ingesta de Sparkplug B e ingesta de LwM2M), `telemetry` o `ingest-only`. |
| `--build` | Compila las imágenes desde el código fuente en un registro local (ruta para desarrolladores; necesita el árbol de código fuente + Docker + ko). |
| `--registry` / `--version` | Sobrescribe el registro/etiqueta de imagen (por defecto: `ghcr.io/devicechain-io` publicado, o `localhost:5000` + `dev` con `--build`). |
| `--host <name>` | Host de ingress en el que exponer la instancia (por defecto `devicechain.local`). Usa `localhost` en un clúster local para llegar a la consola sin editar `/etc/hosts`. |
| `--no-tls` | Sirve HTTP simple en lugar de un certificado autofirmado. Con `--host localhost`, un `http://localhost/` sin configuración adicional (sin advertencia de certificado). En un clúster instalado sin cert-manager está activado por defecto, y `--no-tls=false` se rechaza: no hay nada que emita el certificado. |
| `--dry-run` | Imprime lo que haría cada paso sin cambiar nada. Una ejecución en seco no toma el bloqueo del clúster; sí informa de si otro operador está reteniendo el clúster. |
| `--skip-preflight` | Omite las comprobaciones de entorno. |
| `--escrow-passphrase-file <path>` | Lee la frase de paso del depósito de la clave raíz desde un archivo en lugar de pedirla. Consulta más abajo. |
| `--escrow-file <path>` | Escribe el artefacto de depósito en otro sitio distinto de `~/.devicechain/escrow/`. |
| `--no-escrow` | **No** deposita la clave raíz. Solo para instancias desechables; implícito con `--dev`. A una instancia creada así se le puede dar un depósito más tarde —consulta [la reconciliación del depósito](./disaster-recovery.md#escrow-reconcile). |
| `--restore-root-key <path>` | Recuperación ante desastres: siembra la clave raíz de esta instancia desde un artefacto de depósito en lugar de acuñar una. Solo se acepta cuando hay algo que esa clave pueda abrir: una base de datos que el almacén relacional ya contiene para esta instancia (consulta [Recuperar una instancia](./disaster-recovery.md#recover)), o esta misma instancia a medio construir por una ejecución anterior que se está terminando. Tras `dcctl destroy` no se da ninguna de las dos cosas y el flag se **rechaza**: la siguiente instancia con ese nombre acuña una clave propia, así que arranca sin el flag y aparta antes el artefacto antiguo, porque el arranque inicial no sobrescribe ninguno. `dcctl secrets escrow show <path>` te dice para qué instancia se escribió un artefacto. |

### El depósito de la clave raíz {#escrow}

El arranque inicial escribe una copia cifrada de la **clave raíz del almacén de secretos** de
la instancia en `~/.devicechain/escrow/<instance>-rootkey.escrow`, sellada con una frase de
paso que tú eliges. Te pide esa frase de paso, o la toma de `--escrow-passphrase-file` o de
`DCCTL_ESCROW_PASSPHRASE`.

El depósito está activado por defecto. Una ejecución no interactiva sin frase de paso
**falla** en lugar de continuar sin ella:

```bash
# automatización
DCCTL_ESCROW_PASSPHRASE="$(pass show devicechain/prod-escrow)" \
  dcctl bootstrap local prod --yes

# una instancia desechable
dcctl bootstrap local scratch --dev
```

:::danger Este archivo no es opcional para nada que te importe
La clave raíz cifra todos los secretos que guarda la instancia. Vive solo en el etcd del
clúster, y **ningún respaldo de DeviceChain contiene etcd**. Sin este archivo, un respaldo de
base de datos restaurado en un clúster nuevo rehidrata secretos que nada puede descifrar. Lee
[Recuperación ante desastres](./disaster-recovery.md) antes de necesitarlo.
:::

Las áreas que guardan secretos se niegan a arrancar en lugar de servir credenciales que no
pueden abrir. El servicio user-management es una de ellas: sella la clave de firma de tokens,
así que nadie puede iniciar sesión. Te enteras de inmediato, y para entonces ya no hay nada que
hacer. [Recuperación ante desastres](./disaster-recovery.md) explica el procedimiento completo.

A una instancia que no tiene depósito —creada con `--no-escrow` o con `--dev`— se le puede dar
uno más tarde sin reconstruirla. `dcctl upgrade` escribe el artefacto que falta cuando le pasas
una frase de paso, y comprueba uno existente cada vez que se ejecuta. Consulta
[la reconciliación del depósito](./disaster-recovery.md#escrow-reconcile).

## Flags de instalación {#install-flags}

Estos son flags de [`dcctl install`](#install). Describen el clúster, y toda instancia
arrancada en él los sigue. Ninguno es un flag de `dcctl bootstrap`.

| Flag | Propósito |
|------|-----------|
| `--cluster <name>` | Proveedor `local`: el clúster de kind en el que instalar (por defecto `devicechain`), que se crea si no existe. |
| `--kube-context <name>` | Instala en el clúster existente al que apunta este kube-context. `dcctl` nunca lo crea ni lo elimina. |
| `--compact` | Preajuste de huella pequeña; consulta más abajo. |
| `--ha` | Alta disponibilidad; consulta más abajo. Requiere al menos **3 nodos planificables**. |
| `--no-tls` | Con `--compact`: no instala cert-manager y, por tanto, tampoco respaldos de base de datos. `--compact --no-tls=false` conserva ambos. Se rechaza sin `--compact`: usa `dcctl bootstrap --no-tls` para servir una instancia por HTTP simple. |
| `--no-monitoring` | Omite la pila de monitorización (Prometheus y Grafana). |
| `--no-cnpg` | Omite el operador CloudNativePG y el plugin de respaldo de base de datos. Para un clúster que **ya ejecuta CloudNativePG**: Helm no puede adoptar objetos creados por otro instalador, así que sin este flag la instalación falla. |
| `--backup-credentials-file <path>` | Envía los respaldos de base de datos a un almacén de objetos que ya tengas, descrito por un archivo JSON, en lugar del que hay dentro del clúster. Consulta [Recuperación ante desastres](./disaster-recovery.md). |
| `--restore-rdb-from <archive>` | Recuperación ante desastres: recupera el almacén relacional compartido desde esta ruta de archivo dentro del bucket de respaldos (`dc-rdb` para un almacén que nunca se ha restaurado) en lugar de inicializar uno vacío. Solo surte efecto cuando el almacén se **crea** —contra un clúster cuyo almacén ya existe no mueve ningún dato—, así que es una palanca de reconstrucción, no de reparación. Necesita el plugin de respaldo, así que se rechaza en un clúster instalado con `--no-cnpg` o con `--compact --no-tls`. Consulta [Recuperar una instancia](./disaster-recovery.md#recover). |
| `--restore-rdb-at <timestamp>` | Detiene esa recuperación en un instante en lugar de reproducir todo el archivo, para datos destruidos *correctamente*, por una migración defectuosa o un borrado por error; elige un momento estrictamente anterior al daño. Necesita `--restore-rdb-from`, y una marca de tiempo RFC 3339 con desfase explícito (`2026-07-27T13:59:00Z`): sin él, PostgreSQL la interpreta en la zona horaria del propio servidor en recuperación y se detiene en un momento distinto del que nombraste. |
| `--max-connections <n>` | El presupuesto de conexiones de la base de datos relacional (por defecto `600` en una primera instalación; una nueva ejecución sin él conserva el presupuesto actual); consulta [el presupuesto de conexiones](#connection-budget). Puede aumentarse, pero no reducirse, con instancias en marcha. |
| `--allow-legacy-db-removal` | La mitad relacional de la excepción única descrita en [Qué hace bootstrap](#what-it-does). |
| `--dry-run` | Imprime lo que haría cada paso sin cambiar nada. Una ejecución en seco no crea ningún clúster, así que las comprobaciones que necesitan leer uno —en particular la de capacidad de nodos de `--ha`— informan de lo que no pudieron ver en lugar de hacer fallar el ensayo. Lo que *sí* llegan a ver sigue siendo fatal: un clúster que responde y no puede alojar `--ha` también hace fallar una ejecución en seco. |
| `--yes` | No pregunta antes de crear un clúster de kind. |
| `--skip-preflight` | Omite las comprobaciones de entorno. |
| `--dev` | Comodidad local para un clúster en un portátil; implica `--build --yes`. |

### `--compact` {#--compact}

`--compact` es un preajuste para clústeres pequeños, que se elige al instalar. Compone palancas
que ya existen en lugar de añadir un eje de ajuste propio:

- techos por stream más bajos de JetStream y KV, y los volúmenes más pequeños que eso permite
  (3Gi JetStream, 2Gi Postgres relacional, 4Gi TimescaleDB);
- **solicitudes** (requests) de programación más bajas (25m / 64Mi), para que los pods quepan
  en un nodo pequeño. Los límites quedan intactos: bajar el límite de memoria convierte la
  presión en OOMKills y bajar el límite de CPU produce throttling, y ninguna de las dos cosas
  reduce nada;
- sin la pila de monitorización, el mayor consumidor individual;
- sin cert-manager, ya que con TLS desactivado nada necesita que se emita un certificado
  (mantener TLS conserva también cert-manager; consulta más abajo), y en consecuencia sin el
  plugin de respaldo de base de datos.

**No** cambia qué servicios se ejecutan. Eso sigue en el `--profile` de cada instancia, donde
queda nombrado y visible. Un perfil *más grande* que `default` —hoy solo `full`— se rechaza en
un clúster compacto. Las cifras compactas publicadas se miden sobre `default`, así que no
describirían una instancia que ejecuta cinco servicios más (inferencia de IA, conectores
salientes, MCP, ingesta de Sparkplug B e ingesta de LwM2M). Los perfiles más pequeños
(`telemetry`, `ingest-only`) sí se aceptan.

Puedes conservar tanto TLS como la monitorización. Un `--no-tls=false` o
`--no-monitoring=false` explícito en `dcctl install` se respeta, y el resto de las palancas
compactas siguen aplicándose. Mantener TLS también conserva cert-manager, que es lo que emite
el certificado. En un clúster instalado sin cert-manager, toda instancia se sirve sin TLS:
`dcctl bootstrap` activa `--no-tls` por defecto y rechaza `--no-tls=false`.

:::note Por qué `--compact --no-tls` descarta el plugin de respaldo
El plugin Barman Cloud emite sus propios certificados a través de cert-manager, así que
descartar cert-manager descarta también el plugin. Volver a activar TLS (`--no-tls=false`)
restablece ambos. Hacen falta *ambos* flags de instalación: `dcctl install --no-tls` sin
`--compact` se rechaza, y `--no-tls` en `dcctl bootstrap`, como en el ejemplo de URL local más
abajo, solo cambia cómo se sirve esa instancia.
:::

El operador CloudNativePG en sí se instala en *todo* clúster, incluido el compacto: un
Deployment que solicita 100m/128Mi, más sus CRDs. Es un costo de huella que el modo compacto no
evita, y es deliberado. El respaldo no es una función de alta disponibilidad, así que la capa
de almacenamiento tiene una sola forma en todas partes. Ambas bases de datos se ejecutan ahora
sobre el operador: tanto el almacén relacional como el de eventos.

:::caution Los tamaños de volumen son un presupuesto de tiempo, no de capacidad
El volumen de JetStream se deriva: los techos por stream se reservan por adelantado, así que
el volumen se dimensiona para contener su suma. Los dos volúmenes de base de datos no. Nada
poda las tablas de comandos o de alarmas, y `retentionDays` es `0` por defecto, es decir,
conservar los datos para siempre. En una instancia compacta pensada para ejecutarse
indefinidamente, establece una ventana de retención en lugar de confiar en el tamaño del
volumen.
:::

:::caution Elígelo antes de la primera instancia
Bajar un techo por debajo de lo que un stream o bucket de KV ya contiene tiene éxito en
silencio, no trunca nada y rechaza escrituras hasta que los datos envejecen y se purgan. Por
eso `dcctl install` se niega a cambiar sus ajustes mientras exista alguna instancia en el
clúster: `--compact` se decide una vez, antes de que haya nada ejecutándose bajo él.
:::

:::tip URL local sin configuración
`dcctl bootstrap local my-instance --build --host localhost --no-tls` expone la consola en
`http://localhost/`, sin entrada en el archivo hosts y sin advertencia de certificado.
:::

### `--ha` {#ha}

`--ha` se elige al instalar, y lo sigue toda instancia del clúster. Cada instancia ejecuta su
broker de mensajería como un clúster RAFT de 3 nodos, un servidor por nodo, con **cada stream
de JetStream y cada bucket KV replicados a lo largo de él**. La instancia sobrevive entonces a
la pérdida de cualquier nodo sin perder mensajes, sesiones de dispositivo ni estado en vivo.

```bash
dcctl install local --ha
dcctl bootstrap local my-instance
```

Ese único ajuste establece ambas mitades, y ese es justamente su propósito. El tamaño del
broker es infraestructura (OpenTofu); el factor de réplica por stream es configuración de la
instancia (Helm). Viven en herramientas distintas, ninguna de las cuales puede ver a la otra.
Elevar solo la primera es el modo de fallo que este flag existe para evitar: un clúster de
tres nodos cuyos streams siguen siendo de una sola réplica cuesta el triple de cómputo, informa
de tres pares sanos y no sobrevive a nada.

:::caution Sobrevive exactamente a la pérdida de un nodo
Tres servidores confirman por mayoría, de modo que dos siguen siendo quórum y uno no. Perder un
segundo nodo —incluido perder uno por una actualización continua de nodos mientras otro ya
está caído— detiene las escrituras hasta que un nodo regrese. Planifica el mantenimiento de un
nodo a la vez. Sobrevivir a dos pérdidas simultáneas requiere un clúster de 5 servidores, que
hoy no es una topología soportada.
:::

**Tres nodos planificables, no tres nodos.** Los servidores llevan una restricción dura de
antiafinidad. Si el clúster no puede colocar uno por nodo, el excedente queda en `Pending` en
lugar de duplicarse, porque réplicas colocadas en el mismo nodo costarían lo que cuesta la
replicación sin proteger de nada. `dcctl` cuenta los nodos planificables y rechaza la
operación antes de aprovisionar nada. En un clúster `kind` local esto significa tres workers:
kind solo elimina el taint del plano de control en un clúster de un solo nodo, de modo que un
plano de control más dos workers es un clúster de tres nodos con dos nodos utilizables.

#### Bases de datos con `--ha` {#ha-databases}

`--ha` también ejecuta la base de datos relacional como tres instancias con replicación
síncrona, detrás del mismo nombre de host `dc-postgresql` que los clientes ya usan. El operador
mantiene ese nombre de host y lo mueve para que siga a la primaria a través de una conmutación
por error, de modo que no cambia la configuración de ningún servicio.

La replicación síncrona es lo que obliga a *tres* instancias en lugar de dos. Una réplica en
espera debe confirmar cada commit, así que con solo dos instancias la pérdida de cualquiera de
ellas detiene todas las escrituras: peor disponibilidad que un solo nodo, a cambio de mayor
durabilidad. Una tercera instancia permite perder una réplica en espera sin que el clúster se
quede sin su réplica confirmadora.

El almacén de eventos también se replica en tres instancias, con una diferencia deliberada:
**no** retiene una escritura a la espera de una réplica. Si no hay ninguna disponible, vuelve a
la replicación asíncrona y se pone al día cuando reaparece una. Esa concesión es la correcta
para este almacén y la equivocada para el otro. Los eventos ya se conservan de forma duradera
aguas arriba en la capa de mensajería hasta que se persisten, así que las escrituras de una
conmutación por error pueden reproducirse. El registro de auditoría del almacén relacional no
tiene nada así aguas arriba, y por eso ese almacén se detiene. El costo es que el punto de
recuperación del almacén de eventos queda acotado por el retraso de replicación en lugar de
ser cero.

`--ha` no cambia el número de réplicas de servicio, y nada de esto sobrevive por sí solo a la
pérdida de un nodo. La replicación es lo que hace posible la recuperación, no lo que la
ejecuta.

:::caution Una escritura detenida queda confirmada, no rechazada
Esto se aplica al almacén relacional, que es el que se detiene. Cuando no hay ninguna réplica
en espera disponible, una escritura no falla: espera, y la fila ya se ha confirmado
localmente. Un cliente que se rinda y reintente escribirá dos veces salvo que la operación sea
idempotente. `statement_timeout` **no** acota esa espera, porque la espera ocurre después del
commit y no durante la sentencia.
:::

#### Cómo verificarlo {#verifying-it}

Una afirmación de alta disponibilidad vale solo lo que el broker realmente sostiene, así que
compruébalo ahí y no en la configuración renderizada:

```bash
dcctl ha verify --instance my-instance
```

Esto lee el broker en vivo. Verifica que cada stream, bucket KV y consumidor durable lleva el
factor de réplica declarado **con todos los pares al día**, y que los tres servidores están en
tres nodos distintos. Termina con código distinto de cero si algo se queda corto, e imprime
qué examinó para que un resultado correcto sobre un conjunto vacío no se confunda con un éxito
real.

## Después del arranque inicial {#after-bootstrap}

El comando imprime el namespace, la credencial de **superusuario** y cómo llegar a la
instancia a través del ingress del clúster. El superusuario es `superuser@devicechain.local`,
y no hay contraseña por defecto. El arranque genera una para la instancia, la guarda en el
Secret `dci-<instance>-superuser` del namespace de la instancia y la imprime una sola vez, al
final de la ejecución que la generó. Para volver a leerla:

```bash
kubectl -n dci-my-instance get secret dci-my-instance-superuser -o jsonpath='{.data.password}' | base64 -d
```

### La contraseña del superusuario {#superuser-password}

El servicio user-management lee esa contraseña una sola vez: para crear el superusuario la
primera vez que arranca con la tabla de identidades vacía. A partir de ahí, el Secret es un
registro de la contraseña que el superusuario recibió al principio. Cambiar la contraseña en
la consola no lo actualiza. Cuando un arranque **recupera** una instancia y sus identidades
vuelven con ella, el superusuario restaurado conserva la contraseña que tenía. El informe
entonces no imprime el valor del Secret, e indica que puede no ser la contraseña del
superusuario.

Las instancias arrancadas con una versión anterior no tienen ese Secret. Su superusuario se
creó con la contraseña por defecto que publicaban esas versiones. Ni `dcctl upgrade` ni una
nueva ejecución del arranque sobre la instancia en marcha (una restauración, o
`--allow-legacy-db-removal`) la cambian ni generan un Secret para ella, y ambos lo indican al
terminar. Si esa contraseña no se ha cambiado desde entonces, cámbiala en la consola.

Una instalación hecha solo con Helm, sin `dcctl`, debe crear ese Secret por su cuenta (clave
`password`) antes de que user-management arranque por primera vez, o nombrar otro con el valor
del chart `instance.superuserSecret`. Sin él, user-management se niega a crear el superusuario.

### Iniciar sesión en la consola {#sign-in}

La instancia incluye la **consola web**. El ingress la sirve en la raíz del host
(`https://<host>/`) y enruta `https://<host>/api/<area>/graphql` a cada servicio de área
funcional. Abre la consola en un navegador e inicia sesión con el correo electrónico y la
contraseña del superusuario.

Una instancia recién creada **no tiene tenants**, así que aterrizas en la consola de
administración (`/admin`) para crear tu primer tenant y asignar membresías. Cambia a un tenant
para llegar a la consola del tenant. (Para una instancia headless/solo de ingesta, despliega
con la consola deshabilitada; consulta el valor `frontend.enabled` del chart.)

Para inspeccionar la instancia en ejecución:

```bash
kubectl --context <kube-context> get pods -n dci-my-instance
```

### Ejecutar una simulación {#run-a-simulation}

Para explorar la consola con una flota en movimiento en lugar de una vacía, ejecuta una
**simulación**. `sim create` acuña una identidad y un tenant acotados en la instancia y escribe
el archivo de handshake que el proceso `dc-simulator` lee al arrancar:

```bash
dcctl sim create demo --instance my-instance --server localhost
```

El simulador inyecta entonces telemetría y alarmas por el mismo canal de dispositivo que usa
el hardware real; consulta
[Probarlo con datos simulados](../intro.md#trying-it-with-simulated-data).

## Eliminar una instancia {#destroy}

```bash
dcctl destroy local my-instance
```

`dcctl destroy` elimina **solo esa instancia**, en este orden:

1. Su release de Helm. El namespace de la instancia pertenece a esa release, así que
   desinstalarla elimina el namespace y todo lo que contiene, incluidos su broker NATS y su
   almacén de eventos.
2. Su estado de infraestructura, mediante `tofu destroy`, que elimina lo que ese estado aún
   contenga.
3. Su base de datos y su login de base de datos en la base de datos relacional compartida.
4. Su namespace, si sigue ahí. Un arranque que se detuvo antes de que Helm instalara nada deja
   un namespace sin release que desinstalar. Destroy espera a ver desaparecer el namespace por
   completo.
5. Comprueba que lo que eliminó ya no existe y, solo entonces, elimina su estado local en
   `~/.devicechain/instances/<instance>/`.

El artefacto de depósito de la clave raíz se conserva; consulta
[Recuperación ante desastres](./disaster-recovery.md#after-destroy).

Si un paso falla o se interrumpe —incluido un namespace que sigue terminando cuando se agota
la espera—, destroy termina con un error y conserva el estado local. Ejecutar de nuevo el mismo
comando continúa donde se detuvo.

Si la instancia sigue en marcha pero falta su estado local de infraestructura —porque se
perdió, o porque la instancia se arrancó desde otra máquina—, destroy **se niega**, porque sin
ese estado no puede ejecutar la destrucción de la infraestructura. `--without-state` elimina la
instancia de todos modos, por su release de Helm, su base de datos y su login, y su namespace,
e informa de que se omitió la destrucción de la infraestructura. Una instancia cuyo estado
local es anterior a la división de la infraestructura en una parte de clúster y otra de
instancia se rechaza del mismo modo, antes de cualquier cambio, y también necesita
`--without-state`. `dcctl destroy --all` acepta `--without-state`.

Destroy nunca elimina el clúster ni los requisitos previos que dejó `dcctl install`, así que el
siguiente `dcctl bootstrap` en el clúster no necesita instalar antes. Destruir una instancia y
volver a arrancarla con el mismo nombre es como se recrea una instancia. Una instancia
construida por una versión anterior, de antes de que existiera `dcctl install`, es la
excepción: también hay que recrear su clúster; consulta
[Versiones y actualizaciones](./releases-and-upgrades.md#pre-declaration-recreate).

Si el propio clúster de kind ya no existe —borrado con `kind delete cluster`—, destroy no tiene
nada que desinstalar y lo dice, limpiando solo el estado local. Eso es el directorio de la
instancia y el directorio propio del clúster en `~/.devicechain/clusters/<cluster-uid>/`, que
creó `dcctl install` y que `kind delete cluster` dejó atrás (consulta
[Instalar el clúster](#install)). Mientras lo hace, imprime:

```text
removing the gone cluster's local state (~/.devicechain/clusters/<cluster-uid>)
```

Todavía no hay ningún comando de desinstalación. Para eliminar un clúster local que creó
`dcctl install`, usa kind directamente, como se muestra en [Instalar el clúster](#install).
