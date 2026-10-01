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
sitio se deja como está, y lo que falta se añade. Eso incluye el volumen del almacén de objetos de
respaldo, que conserva el tamaño que tiene; consulta
[Tamaño del almacén de objetos de respaldo](#backup-store-size).

**Cambiar sus ajustes** —`--ha`, `--compact`, la monitorización, los respaldos,
`--backup-snapshot-class`— se rechaza mientras exista alguna instancia en el clúster. Cada instancia se construyó con los ajustes
vigentes cuando se arrancó, y ninguna se reconstruye cuando cambian. Eso incluye el archivo
externo: un `--backup-credentials-file` que nombre otro endpoint u otro bucket del almacén de
eventos también se rechaza, porque el almacén de eventos de cada instancia sigue archivando en
el que tenía cuando se construyó.

Hay una excepción: una nueva ejecución puede aumentar `--max-connections` mientras hay
instancias en marcha (consulta [el presupuesto de conexiones](#connection-budget)). Reducirlo
se rechaza como cualquier otro cambio. Una nueva ejecución que no pase `--max-connections`
conserva el presupuesto que ya tiene el clúster.

**Una ejecución que falló a mitad se repara de la misma forma.** Cuando hayas corregido la
causa, vuelve a ejecutar el mismo `dcctl install`. Si el almacén de objetos de los respaldos no
pudo arrancar la primera vez, por ejemplo porque no se pudo descargar su imagen, la nueva
ejecución lo elimina, lo crea de nuevo y espera a que esté listo, igual que la primera. Si la
causa sigue ahí, la nueva ejecución falla del mismo modo. Antes de dar el clúster por instalado,
`install` comprueba además que el almacén de objetos de los respaldos ha terminado su despliegue,
así que un almacén que quedó sin estar listo tras un cambio fallido anterior se notifica en
lugar de pasarse por alto.

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

Los proveedores de OpenTofu están fijados a versiones exactas, y cada ejecución, incluida
`dcctl destroy`, lleva el `.terraform.lock.hcl` del directorio a las versiones que fija este
dcctl. Por eso cada ejecución consulta al registro de proveedores qué versiones existen, así
que el registro, o un espejo de proveedores que hayas configurado, debe ser accesible.

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

Cada servicio mantiene abiertas entre usos todas las conexiones que ha abierto su pool, hasta el
tamaño del pool (20 si no se fija `maxOpenConnections`), y cierra cada una una hora después de
abrirla. La reserva permite a cada área un pod con un pool completo del tamaño predeterminado, más
otro pod durante un despliegue, así que con los valores predeterminados todos los pools caben en ella. Un
servicio con `replicas` por encima de 1, o con un `maxOpenConnections` mayor, puede mantener más
que eso después de un periodo de mucha carga, así que comprueba esos ajustes frente al límite de
conexiones de la instancia.

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

#### Ventanas de recuperación {#backup-retention}

Cada base de datos conserva su propia ventana de recuperación: el intervalo de tiempo dentro del
cual se puede restaurar a cualquier punto. La base de datos relacional, que guarda inquilinos,
usuarios, dispositivos, reglas, secretos y el último estado conocido de cada dispositivo, conserva
**30 días**; se ajusta con `backup_retention_rdb` en la configuración de OpenTofu del clúster. El
almacén de eventos de cada instancia conserva **7 días**; se ajusta con `backup_retention_tsdb` en
la configuración de la instancia. La base de datos relacional tiene la ventana más larga porque sin
ella no se puede reconstruir una instancia, y los errores de los que se restaura, como una
migración defectuosa o un borrado por error, se descubren a menudo días después. El historial de
eventos es voluminoso, tiene su propio [ciclo de vida de los datos](../concepts/architecture.md), y
su log es lo que llena el almacén de respaldos.

Una ventana es un número entero y una unidad: `d` para días, `w` para semanas o `m` para **meses**
(no minutos). Cualquier otra forma se rechaza antes de empezar la aplicación. Una ventana vacía
conserva todos los respaldos. Alargar una ventana guarda más en el almacén de respaldos: consulta
más abajo.

#### Tamaño del almacén de objetos de respaldo {#backup-store-size}

El almacén predeterminado tiene 160 GiB, dimensionado a partir del almacén de eventos
predeterminado para que, con una ingesta sostenida, se llene primero el volumen del almacén de
eventos. Mientras una instancia está tranquila, el log de escritura anticipada que archiva no
cuesta casi nada. Con ingesta sostenida, el log crece al ritmo de las escrituras: medido en Google
Kubernetes Engine, hasta unos 1,9 KB por evento ingerido para las dos bases de datos juntas,
frente a aproximadamente 1 KB por evento de datos almacenados. (Esta página daba antes
aproximadamente 1 KB por evento para el archivo; esa cifra era baja). 160 GiB guardan el archivo
de un almacén de eventos de 32 GiB lleno, más un respaldo base completo de cada base de datos, con
más de un tercio del almacén todavía libre. Así, las alertas del propio almacén de respaldos no
se disparan, y las alertas del almacén de eventos nombran la causa.

Eso se cumple para una instancia cuyo almacén de eventos se llena en aproximadamente un día. No se
cumple:

- **con varias instancias que ingieren de forma continua.** El almacén pertenece al clúster, y
  cada instancia añade su propio archivo. Añade unos 160 GiB por cada una de esas instancias, o
  envía los respaldos a un almacén de objetos que gestiones tú (`--backup-credentials-file`), que
  es de todos modos la configuración de producción recomendada.
- **cuando el almacén de eventos tarda más de aproximadamente un día en llenarse.** El almacén
  guarda un respaldo base completo de cada base de datos por cada día de la
  [ventana de recuperación](#backup-retention) de esa base de datos, más el log archivado desde
  entonces. Con las ventanas predeterminadas son 32 copias comprimidas de la base de datos
  relacional y 9 del almacén de eventos, y tienen que caber junto al log: 32 veces el tamaño
  comprimido de la base de datos relacional más 9 veces el del almacén de eventos tiene que quedar
  por debajo de 160 GiB. La base de datos relacional ocupa normalmente megabytes, así que en la
  práctica el almacén de eventos tiene que comprimirse bastante por debajo de unos 17 GiB.
- **cuando una ventana de retención (`retentionDays`) limita los datos almacenados de una
  instancia.** Su almacén de eventos no se llena nunca, pero sigue enviando aquí su log: una semana
  del almacén de eventos y 30 días de la base de datos relacional, cuyo log crece con la ingesta
  porque registra el último estado conocido de cada dispositivo. En la única medición realizada,
  de una flota pequeña que informa con frecuencia, los respaldos de la base de datos relacional
  eran aproximadamente un 14 % del contenido del almacén. Con esa proporción, unos 100 eventos por
  segundo sostenidos llenan el almacén predeterminado antes de contar ningún respaldo base. La
  proporción no está medida para flotas más grandes o más lentas, donde probablemente es mayor: si
  el log de la base de datos relacional fuera todo el archivo, la cifra sería de unos 35 eventos
  por segundo.
- **cuando amplías el almacén de eventos.** Cada GiB que añades al volumen del almacén de eventos
  necesita unos cinco GiB más aquí.

En esos casos, el aviso son las alertas descritas en
[Respaldos que dejan de enviarse](./observability.md#backup-archiving):
`BackupDestinationAlmostFull` al 85 % de ocupación y `BackupDestinationFillingFast` con un ritmo
pronunciado, y `PostgresWALArchivingFailing` cuando el archivado ya se ha detenido. Un respaldo
base llega de golpe, así que un almacén que ya pasa del 85 % puede llenarse con el siguiente
respaldo nocturno: actúa con la primera alerta. Cuando el almacén está lleno, el archivado se
detiene para todas las instancias del clúster, y cada base de datos conserva el log sin enviar en
su propio volumen hasta que también se llena y la base de datos se detiene.

El tamaño del almacén se fija cuando `dcctl install` lo crea por primera vez. Volver a ejecutar
install, también como primer paso de una actualización, conserva el tamaño que tiene el almacén, y
lo mismo hace un `tofu apply` directo de la configuración de OpenTofu. Para ampliarlo, con una
StorageClass que permita la expansión de volúmenes:

```bash
kubectl -n dc-system patch pvc dc-object-store-data \
  -p '{"spec":{"resources":{"requests":{"storage":"320Gi"}}}}'
```

En kind, el volumen no está limitado a su tamaño: usa lo que tenga el disco del host.

Acortar una ventana de recuperación (`backup_retention_tsdb` para el almacén de eventos de una
instancia, `backup_retention_rdb` para la base de datos relacional) no resuelve la ingesta: guarda
menos historial pero sigue guardando un día completo de log, y renuncia a alcance de recuperación
para ganar espacio. Las alertas descritas en
[Respaldos que dejan de enviarse](./observability.md#backup-archiving) avisan antes de que se
llene el almacén o un volumen de base de datos.

#### Respaldos base como instantáneas de volumen {#snapshot-base-backups}

En un clúster cuyo controlador de almacenamiento toma instantáneas de volumen CSI, `dcctl install
--backup-snapshot-class <class>` toma el respaldo base diario de cada base de datos como una
instantánea de volumen de sus discos, en lugar de una copia completa en el almacén de respaldos.
Sin la opción no cambia nada.

- **Lo que no cambia.** Cada base de datos sigue archivando su log de escritura anticipada en el
  almacén de respaldos, de forma continua. Un respaldo base completo sigue yendo al almacén de
  respaldos una vez por semana (el domingo a las 04:00): el almacén solo poda el log antiguo en
  relación con los respaldos base que guarda, así que sin ninguno conservaría cada segmento hasta
  llenarse, y toda restauración lee el almacén.
- **La clase.** Tiene que existir, tener `deletionPolicy: Delete` y pertenecer al controlador de
  almacenamiento que aprovisiona los volúmenes de las bases de datos (normalmente el de la
  StorageClass predeterminada). `dcctl install` comprueba las tres cosas antes de cambiar nada, y
  lo mismo hace cada `dcctl bootstrap`, así que una clase borrada después de la instalación se
  detecta antes de construir una instancia. El clúster necesita un controlador de instantáneas
  CSI: Google Kubernetes Engine y Azure AKS lo incluyen con sus controladores de disco; en Amazon
  EKS, instala antes el complemento del controlador de instantáneas.
- **Retención.** CloudNativePG no borra las instantáneas antiguas. Lo hace el operador de
  DeviceChain, cada diez minutos: conserva todas las instantáneas dentro de la
  [ventana de recuperación](#backup-retention) de la base de datos y la más reciente anterior a
  ella, y borra el resto, lo que borra también la copia del proveedor.
  `DatabaseSnapshotPruningStalled` se dispara cuando deja de hacerlo. Cada pasada registra su
  hora en la programación, en la anotación `devicechain.io/snapshot-retention-checked-at`.
- **Lo que no hace.** Ninguna restauración lee una instantánea. Una restauración
  (`--restore-rdb-from`, `--restore-tsdb-from`) lee el almacén de respaldos: el respaldo base
  semanal más reciente y el log desde entonces, así que puede reproducir hasta una semana de log.
  Las instantáneas de una instancia se borran con su namespace cuando se destruye. Las
  instantáneas tomadas en tu proveedor de nube sobreviven a un clúster que se borre sin destruir
  antes sus instancias, y guardan el contenido de las bases de datos, incluidos los datos que ha
  eliminado el borrado de un inquilino, hasta que las borres allí.
- **Lo que hace con el almacén de respaldos.** El almacén guarda ahora hasta una semana más de log
  de cada base de datos: el log hasta el respaldo base semanal más reciente anterior a cada
  ventana. Guarda menos copias completas, pero donde el log es la mayor parte de lo que guarda, se
  llena antes. Con las ventanas y el almacén predeterminados, y la proporción relacional medida
  más arriba, las copias base como instantáneas de volumen llenan el almacén a unos 60 eventos por
  segundo sostenidos, no a unos 100; y a unos 29 si todo fuera registro de la base de datos
  relacional. Las alertas de arriba son el aviso, igual que sin instantáneas. No cambia el tamaño
  predeterminado del almacén: la regla de dimensionamiento de [más arriba](#backup-store-size)
  cuenta un respaldo base completo de cada base de datos y el log de cada evento que guarda un
  almacén de eventos lleno, sea cual sea la programación de los respaldos base, y un respaldo base
  completo sigue llegando al almacén al crearse cada base de datos y luego cada semana, así que la
  regla da los mismos 160 GiB con o sin instantáneas.
- **Pertenece al clúster.** Todas las instancias lo siguen, y cambiarlo se rechaza mientras haya
  instancias en el clúster, como los demás [ajustes de install](#re-running-install).

Las alertas de las instantáneas se describen en
[Respaldos que dejan de enviarse](./observability.md#backup-archiving).

## Prerrequisitos {#prerequisites}

- **Un clúster de Kubernetes, versión 1.29 o más reciente**, y un kube-context que apunte a él.
  El mínimo proviene de los charts de CloudNativePG, que se niegan a instalarse por debajo de
  esa versión. `dcctl preflight` lo verifica por adelantado, porque de lo contrario el fallo
  aparece a mitad de un arranque que ya ha escrito tu archivo de depósito (escrow) de la clave
  raíz. Para el proveedor `local` esto es un clúster de kind, que `dcctl install local` crea
  por ti (`--cluster <name>`, por defecto `devicechain`). Pasa `--kube-context <name>` para
  usar en su lugar un clúster que ya tengas (kind / minikube / k3d / docker-desktop).
- **Disco para los volúmenes persistentes**, en la StorageClass predeterminada del clúster. En un
  clúster que no es local (ni kind, ni minikube, ni k3d, ni docker-desktop, ni rancher-desktop), con
  los ajustes de install predeterminados (sin `--compact`, `--no-cnpg`, `--no-monitoring` ni
  `--backup-credentials-file`), `dcctl install --ha` reclama 204 GiB para el clúster: tres
  volúmenes de la base de datos relacional, el [almacén de respaldos](#backup-store-size)
  (160 GiB) y el Prometheus de la pila de monitorización. Cada instancia reclama 144 GiB más, tres
  volúmenes del almacén de eventos y tres del broker de mensajes: 348 GiB para un clúster con una
  instancia. Sin `--ha`, el clúster reclama 188 GiB y cada instancia 48 GiB. Cada instancia
  adicional que ingiere de forma continua obliga además a ampliar el almacén de respaldos en unos
  160 GiB, como explica [Tamaño del almacén](#backup-store-size). En un proveedor de nube,
  comprueba antes la cuota de disco. Un proyecto nuevo de Google Cloud permite 500 GB de SSD por
  región, y cuenta cada GiB de volumen como un GB; las dos clases de disco de Google Kubernetes
  Engine y los discos de arranque de los nodos cuentan para ella, así que una instalación `--ha`
  predeterminada con una instancia no cabe en el clúster que crea la
  [guía de Google Kubernetes Engine](https://github.com/devicechain-io/devicechain/blob/main/deploy/gke/README.md#before-you-start):
  la guía indica la cuota que hay que solicitar. En un clúster local la pila de monitorización no guarda
  ningún volumen, y en kind los tamaños no se aplican.
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
| `--backup-snapshot-class <class>` | Toma el respaldo base diario de cada base de datos como una instantánea de volumen CSI con esta VolumeSnapshotClass, en lugar de una copia completa en el almacén de respaldos; una copia completa sigue yendo al almacén cada semana, y las restauraciones siguen leyendo el almacén. La clase tiene que existir, usar `deletionPolicy: Delete` y pertenecer al controlador que aprovisiona los volúmenes de las bases de datos. Se rechaza con `--no-cnpg` o `--compact --no-tls`, que no dejan respaldos. Consulta [Respaldos base como instantáneas de volumen](#snapshot-base-backups). |
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
  (3Gi JetStream, 2Gi Postgres relacional, 4Gi TimescaleDB, y un almacén de objetos de respaldo
  de 20Gi cuando se mantiene TLS);
- **solicitudes** (requests) de programación más bajas (25m / 64Mi), para que los pods quepan
  en un nodo pequeño. Los límites quedan intactos: bajar el límite de memoria convierte la
  presión en OOMKills y bajar el límite de CPU produce throttling, y ninguna de las dos cosas
  reduce nada. `device-management`, `event-management`, `event-sources` y `device-state`
  conservan sus límites de CPU más altos, y las solicitudes por servicio que usa una instalación
  predeterminada se desactivan para que las más bajas se apliquen a todos los servicios;
  consulta [Dimensionamiento de los servicios](#service-sizing);
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

#### Cuando se detiene la primaria de una base de datos {#ha-database-failover}

Una instancia de base de datos se detiene cuando se elimina su pod, cuando se drena su nodo y
cuando se despliega un cambio en su configuración. La instancia primero escribe un checkpoint,
después deja de aceptar conexiones nuevas y concede cinco segundos a los clientes conectados
para que se desconecten. Los servicios de la plataforma mantienen abiertas sus conexiones a la
base de datos mientras se ejecutan, así que esperar más solo retrasaría lo que viene después.
Pasados esos cinco segundos, la instancia cierra todas las conexiones abiertas y se detiene.
Las escrituras que estaban en curso fallan, y los servicios las reintentan.

Con `--ha`, se promueve una réplica en espera en cuanto la antigua primaria se ha detenido, y
`dc-postgresql` o `dc-timescaledb-single` pasa a apuntar a ella. En las pruebas, la nueva
primaria aceptaba escrituras entre 20 y 25 segundos después de que la antigua cerrara sus
conexiones, aproximadamente medio minuto después de eliminar el pod. En una base de datos que
tiene esta configuración, desplegar un cambio de configuración no reinicia la primaria en su
sitio: primero se reinician las réplicas en espera, después el papel de primaria se traspasa a
una réplica al día, y la antigua primaria se reinicia como réplica. En las pruebas, ese
traspaso interrumpió las escrituras durante unos diez segundos. Un almacén de eventos creado
antes de que existiera esta configuración mantiene el reinicio en su sitio hasta que se le
aplica el parche descrito en las
[notas de la versión](./releases-and-upgrades.md#database-primary-failover-in-seconds).

Una instalación de una sola instancia no tiene ninguna réplica que promover. Su base de datos
no está disponible hasta que la instancia se ha reiniciado, y las escrituras esperan. En las
pruebas, con una base de datos pequeña, las escrituras se reanudaron unos 15 segundos después
de detenerse; un reinicio con más registro de escritura anticipada que reproducir tarda más.

En ambos casos, la capa de mensajería conserva los eventos hasta que se almacenan. Cada uno se
entrega hasta cinco veces, con un minuto entre entregas, antes de abandonarlo y
[registrarlo como no entregado](./observability.md#max-delivery-records). Por eso una
interrupción de la base de datos de menos de unos cuatro minutos no deja ningún evento sin
entregar.

Una instancia que se está deteniendo dispone como máximo de dos minutos en total. Si para
entonces no se ha detenido, se detiene de forma forzosa y se elimina su pod. El motivo más
probable es que siga intentando copiar su último registro de escritura anticipada (WAL) a un
almacén de copias de seguridad inaccesible. Los datos confirmados siguen donde se
confirmaron, pero puede faltar parte del archivo de copias de seguridad: puede que una
restauración a un momento concreto no alcance un instante dentro de ese hueco, y la alerta
`PostgresWALArchivingFailing` ya está activa. Las restauraciones a momentos posteriores a la
siguiente copia base no se ven afectadas. Una copia base que esté en curso cuando se detiene la
primaria se abandona, y la siguiente copia programada se ejecuta con normalidad.

Los mismos dos minutos cubren también un problema conocido del operador de base de datos. Una
instancia puede apagar PostgreSQL correctamente y después no terminar: su registro acaba con
`failed waiting for all runnables to end within grace period of 30s`, y su pod se queda en
`Terminating` aunque la base de datos ya se ha detenido. Los datos no tienen ningún problema, y
el pod se elimina al cumplirse los dos minutos. Un pod que aún no tiene este límite (uno creado
antes de que se introdujera, o cualquier instancia de un almacén de eventos que no se haya
modificado como describen las notas de la versión) sigue teniendo treinta minutos; las
[notas de la versión](./releases-and-upgrades.md#database-primary-failover-in-seconds) explican
cómo reconocer ese caso y resolverlo.

Con `--ha`, una primaria que se está degradando (por un traspaso, o porque está fallando)
también se detiene de forma abrupta si no se ha apagado en dos minutos. En el almacén relacional
eso no pierde nada, porque cada commit se retiene hasta que una réplica lo tiene. El almacén de
eventos no espera a una réplica cuando no hay ninguna disponible, así que un commit hecho
mientras no había ninguna réplica conectada solo existe en su primaria, y se pierde si esa
primaria se sustituye antes de que una réplica se ponga al día. Es la concesión sobre el punto
de recuperación descrita más arriba, y una parada abrupta es una vía más para llegar a ella.

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

#### Perder un nodo y recuperarlo {#ha-node-loss}

`--ha` sobrevive a la pérdida de un nodo. Así se ve desde fuera, en el orden en que ocurre:

- **El broker elige nuevos líderes en segundos.** Los streams cuyo líder estaba en el nodo
  perdido eligen uno nuevo, y en las pruebas las escrituras confirmadas se reanudaron en unos
  diez segundos. Las publicaciones en curso en ese momento fallan, y un dispositivo que publica
  por HTTP puede recibir algunas respuestas `503` y debe reintentar. Las conexiones nuevas a
  través del servicio del broker pueden seguir fallando de forma intermitente durante unos 45
  segundos, hasta que Kubernetes da el nodo por perdido y deja de dirigir tráfico al servidor que
  había en él.
- **El procesamiento de eventos puede detenerse durante un minuto, aproximadamente.** Si el
  servidor del broker del nodo perdido era el líder del stream de eventos entrantes, los eventos
  de los dispositivos se siguen aceptando, pero su resolución puede detenerse durante un minuto,
  sin que se notifique ningún error, antes de reanudarse y procesar lo acumulado. No se pierde
  nada; las alarmas y los eventos almacenados de ese minuto llegan con retraso.
- **Los pods de los servicios se mueven al cabo de un minuto y cuarto, aproximadamente.**
  Kubernetes tarda primero entre 40 y 50 segundos en dar el nodo por perdido. Después desaloja
  cada pod de servicio de ese nodo al cabo de `nodeLossTolerationSeconds` (30 por defecto; `null`
  restablece los 300 del propio Kubernetes) y lo arranca en otro nodo. Con una réplica por
  servicio, que es lo predeterminado, un servicio cuyo pod estaba en el nodo perdido no está
  disponible hasta entonces. Las instancias de base de datos y el operador de base de datos usan
  los mismos 30 segundos. Los servidores del broker no: Kubernetes no los recrea en otro nodo
  mientras no pueda confirmar que el anterior se ha detenido, así que un plazo más corto no
  aportaría nada.
- **Una primaria de base de datos que estaba en el nodo perdido conmuta.** Se promueve una
  réplica en cuanto el operador de base de datos ve que la primaria es inaccesible, y
  `dc-postgresql` o `dc-timescaledb-single` pasa a apuntar a ella. Tarda más que
  [detener una primaria](#ha-database-failover), porque nada avisa al operador de que la primaria
  ha desaparecido; en las pruebas, con el propio operador en un nodo superviviente, la nueva
  primaria aceptaba escrituras unos dos minutos después de perder el nodo. Mientras tanto, los
  eventos esperan en la capa de mensajería.
- **Los pods desalojados del nodo perdido aparecen en `Terminating` hasta que vuelve.** Kubernetes no puede
  confirmar que se han detenido, así que los deja ahí. No elimines a la fuerza un pod cuyo nodo
  es inaccesible: en el caso de una instancia de base de datos o de un servidor del broker, eso
  permite que arranque un sustituto mientras el original puede seguir ejecutándose al otro lado
  del fallo. Si la máquina no va a volver, confirma que está apagada y elimina después su objeto
  Node; Kubernetes elimina entonces sus pods. Una instancia de base de datos cuyo nodo vuelve se
  reincorpora como réplica.
- **Recuperar el nodo también es una interrupción breve.** Un servidor del broker que quedó
  aislado ha seguido celebrando elecciones por su cuenta y, al reincorporarse, los streams y
  consumidores de los demás servidores vuelven a elegir a sus líderes. Cuenta con que JetStream
  responda "temporalmente no disponible" durante unos segundos, unos 45 segundos después de que
  vuelva el nodo, con que la ingesta HTTP rechace algunas publicaciones en ese intervalo, y con
  que algunos eventos que ya estaban en curso se procesen hasta un minuto tarde. No se pierde
  nada, y los servicios se vuelven a enlazar solos.

Planifica la vuelta de un nodo como planificas su pérdida, y no retires un segundo nodo hasta
que `dcctl ha verify` vuelva a pasar.

#### Dónde se ejecutan las primarias de las bases de datos {#ha-database-primaries}

Cada base de datos prefiere un nodo que no ejecute la primaria de otra base de datos de
DeviceChain. En las pruebas, con tres nodos de 8 vCPU y las primarias relacional y del almacén de
eventos en el mismo nodo, ese nodo funcionó al 94-98 % de CPU mientras los otros dos estaban al
45-51 %.

- **Es una preferencia, no un requisito.** Un clúster con menos nodos sigue planificando todas
  las instancias de base de datos. La única excepción es un `ResourceQuota` con el ámbito
  `CrossNamespacePodAffinity`: rechaza los pods cuya ubicación tiene en cuenta otros espacios de
  nombres, sea preferencia o no, así que rechaza estos pods de base de datos en un espacio de
  nombres donde lo prohíba. La configuración de admisión de cuotas del servidor de API puede
  imponer el mismo límite a todo espacio de nombres sin una cuota que lo admita; consulta las
  [notas de versión](./releases-and-upgrades.md#next-upgrade).
- **Se aplica cuando se planifica un pod de base de datos.** Con `--ha` en tres nodos, cada nodo
  ya ejecuta una instancia de cada base de datos, así que en la práctica la preferencia decide una
  sola cosa: cuando se crea el almacén de eventos de una instancia, su primera primaria va a un
  nodo que no ejecuta la primaria relacional. Una [conmutación por error](#ha-database-failover),
  un traspaso, o el traspaso con el que termina una actualización progresiva de una base de
  datos, todavía pueden dejar las dos primarias en un mismo nodo, y la preferencia no las vuelve
  a separar.
- **Un almacén de eventos creado antes de que existiera esta preferencia no participa.** Sus pods
  no llevan la etiqueta que buscan las demás bases de datos ni prefieren nada, y nada lo vuelve a
  aplicar. En una instalación anterior, la preferencia solo se aplica a las instancias creadas
  después.

Para ver dónde están las primarias (esto lista la primaria de cada base de datos, incluida una
creada antes de que existiera la preferencia):

```bash
kubectl get pods -A -l cnpg.io/instanceRole=primary -o wide
```

Si dos de ellas comparten nodo, traspasa la primaria de una base de datos a una réplica en espera
de otro nodo. Con el plugin de `kubectl` de CloudNativePG:

```bash
kubectl cnpg promote dc-rdb dc-rdb-2 -n dc-system
```

o, sin el plugin:

```bash
kubectl -n dc-system patch cluster dc-rdb --subresource=status --type=merge \
  -p '{"status":{"targetPrimary":"dc-rdb-2"}}'
```

Elige una réplica en un nodo que, según el primer comando, no tenga ninguna primaria. Un traspaso
interrumpe brevemente las escrituras de la base de datos (consulta
[Cuando se detiene la primaria de una base de datos](#ha-database-failover)), y los servicios las
reintentan. Una instalación de una sola instancia no tiene ninguna réplica a la que traspasar.

### Dimensionamiento de los servicios {#service-sizing}

Cada servicio de backend solicita 128Mi de memoria y tiene un límite de 256Mi. La CPU se
dimensiona por servicio a partir de mediciones:

| Servicio | Solicitud de CPU | Límite de CPU |
| --- | --- | --- |
| `device-management` | 500m | 2 núcleos |
| `event-management` | 400m | 2 núcleos |
| `device-state` | 400m | 2 núcleos |
| `event-sources` | 150m | 2 núcleos |
| cualquier otro servicio de backend | 100m | 500m |

Con [`--compact`](#--compact), cada servicio de backend solicita en cambio 25m y 64Mi, y los
límites quedan como arriba. La consola se dimensiona por separado.

Estos cuatro servicios hacen el trabajo por evento: recibir, resolver y almacenar cada evento, y
fusionarlo en el estado en vivo de cada dispositivo. Sus límites están dimensionados para el
tráfico en vivo de los dispositivos al techo de ingesta predeterminado de un inquilino, 1000
mensajes por segundo con una lectura por mensaje, y para unos 4000 eventos por segundo, el ritmo
que sostenía una instalación predeterminada antes de que se aumentaran los valores de persistencia
de `event-management` (consulta [Rendimiento medido](#measured-throughput)).

- **Las solicitudes son lo que cada servicio usa al techo predeterminado de un inquilino.** Una
  solicitud es la CPU que el planificador reserva para un pod en su nodo. Cada una de las de
  arriba es lo que ese servicio usó, medido, al techo predeterminado, redondeado hacia arriba.
  Con todos los servicios solicitando los mismos 100m, el planificador no podía distinguir los
  servicios ocupados de los inactivos al ubicarlos, y puso los más ocupados en un mismo nodo. La
  memoria se queda en 128Mi: en v0.18.0 ningún servicio de la ruta de eventos usó más de 40Mi en
  ninguna muestra, ni siquiera a 4800 eventos por segundo.
- **Los límites no reservan nada.** Kubernetes planifica un pod según sus solicitudes, así que
  los límites más altos no necesitan espacio adicional en un nodo. Solo permiten que un servicio
  ocupado use la CPU que el nodo tiene libre. `--compact` reduce todas las solicitudes y no toca
  los límites.
- **Bajar un límite de CPU recorta el rendimiento; no ahorra capacidad.** Con 500m, en un clúster
  kind `--ha` de cuatro nodos, `device-management` resolvía como máximo unos 720 eventos por
  segundo, frenado por su límite en casi todos los periodos de planificación, y cada evento por
  encima de eso esperaba en el stream de entrada. Medido allí sin un límite que lo frenara, usaba
  hasta 0,96 milinúcleos de CPU por evento y `event-management` hasta 0,53 por evento almacenado,
  así que el techo predeterminado necesita alrededor de un núcleo y de medio núcleo; cada límite es
  el doble, porque un límite de CPU se aplica en periodos cortos y un servicio ocupado lo alcanza
  en ráfagas mucho antes que su media. En un clúster en la nube de tres nodos, con 500m,
  `event-sources` quedaba frenado por su límite en el 84% de los periodos de planificación a 4000
  eventos por segundo, y sus respuestas más lentas limitaron el ritmo al que podían enviar los
  dispositivos a unos 4300 eventos por segundo; `device-state` fusionaba el estado en vivo a no
  más de unos 2300 eventos por segundo, así que la vista en vivo de los dispositivos se retrasaba
  minutos. Medidos sin un límite que los frenara, usan 0,14 y 0,37 milinúcleos por evento, así que
  a 4000 eventos por segundo necesitan alrededor de medio núcleo y de núcleo y medio.
- **Los servicios más ocupados evitan el primario del almacén de eventos.** `device-management`,
  `event-management` y `event-sources` prefieren un nodo que no ejecute el primario del almacén
  de eventos de la instancia (en las instalaciones que usan CloudNativePG, la opción
  predeterminada), que es el proceso individual más ocupado de una instalación. Es una
  preferencia, no un requisito: en un clúster con menos nodos que servicios ocupados se siguen
  planificando. Solo se aplica cuando se planifica un pod, así que una conmutación por error de
  la base de datos no mueve los pods en ejecución. Con la configuración ajustada de abajo, sacar
  `event-management` de ese nodo elevó el ritmo sostenido de unos 4750 a unos 5600 eventos por
  segundo; su efecto con la configuración predeterminada no se ha medido. Para desactivarlo en un
  servicio, establece `functionalAreas.<servicio>.avoidEventStorePrimary: false`.
- **Las primarias de las bases de datos prefieren nodos distintos.** En las pruebas, un nodo que
  ejecutaba a la vez la primaria relacional y la del almacén de eventos funcionó al 94-98 % de CPU
  mientras los demás estaban a la mitad, aproximadamente. Consulta
  [Dónde se ejecutan las primarias de las bases de datos](#ha-database-primaries), incluido cómo
  comprobarlo después de una conmutación por error o de una actualización.
- **Más tráfico necesita más.** Varios inquilinos enviando cada uno a su techo, mensajes con
  muchas lecturas, o un inquilino [admitido por encima de su
  techo](../concepts/governance.md#ingest-above-ceiling) mientras se pone al día necesitan más
  que esto. Añade `replicas` a `device-management`, que escala horizontalmente (más réplicas
  reordenan un poco más los eventos de un mismo dispositivo; consulta `watermarkLatenessSeconds`
  en [la configuración del motor de detección](./detection-engine.md#configuration)), o aumenta
  el límite de un servicio:

  ```yaml
  functionalAreas:
    device-management:
      resources:
        limits:
          cpu: "4"
  ```

  La CPU y la memoria de un servicio vienen de tres sitios, y cada uno prevalece sobre el
  anterior, clave a clave: los `resources` de nivel superior, después la solicitud de CPU medida
  del servicio (`functionalAreas.<servicio>.measuredRequests`), y después los
  `functionalAreas.<servicio>.resources` propios del servicio. Así que indica solo lo que cambia.
  Para garantizar la CPU cuando el nodo está saturado, aumenta también la solicitud en los
  `resources` propios del servicio. Una solicitud por encima del límite de su servicio se rechaza
  al generar el chart, y el rechazo indica de dónde vino cada valor.

  Los límites y las solicitudes de CPU propios de esos cuatro servicios se definen de la misma
  forma, así que los `resources` de nivel superior no los sustituyen: un límite de nivel superior
  de 4 núcleos da 4 núcleos a los demás servicios de backend y deja estos cuatro en 2, y un
  `requests.cpu` de nivel superior no les llega. Define los suyos en `functionalAreas`, como
  arriba. En una instalación solo con el chart, `useMeasuredRequests: false` desactiva las
  solicitudes medidas, de modo que las solicitudes de nivel superior se aplican a todos los
  servicios; es lo que hace `--compact`.

La métrica que muestra un servicio frenado por su límite es
`container_cpu_cfs_throttled_periods_total` de su contenedor.

#### Rendimiento medido {#measured-throughput}

| Versión | Clúster | Configuración | Ritmo sostenido | Resultado |
| --- | --- | --- | --- | --- |
| v0.18.0 | Google Kubernetes Engine, 3 × n2-standard-8 (8 vCPU cada uno), discos persistentes SSD, `--ha` | los valores predeterminados de v0.18.0, anteriores al dimensionamiento de arriba | unos 3800 eventos/s | Dos ejecuciones de 10 minutos a 4000 eventos/s ofrecidos almacenaron cada una los 2 399 000 eventos aceptados, sin perder ni duplicar ninguno. La etapa más lenta mantuvo el 96,5% del ritmo ofrecido en una ejecución (la resolución, 3858 por segundo) y el 98% en la otra. El estado en vivo de los dispositivos solo siguió el ritmo hasta unos 2300 eventos por segundo, con el límite de 500m que tenía entonces `device-state`. |
| v0.18.0 | el mismo | ajustada: ver abajo | unos 5600 eventos/s | Ejecuciones de 180 segundos. A 5600 ofrecidos, cada etapa mantuvo al menos el 98,9% del ritmo ofrecido, la cola se vació en 3 segundos y cada evento aceptado se almacenó exactamente una vez. El estado en vivo siguió el ritmo. |
| después de v0.18.0, antes de sus valores de persistencia | el mismo | el dimensionamiento de arriba, con `event-management` en `persistence.writers: 5` y `persistence.maxBatch: 32` | unos 3900 eventos/s | Dos ejecuciones de 10 minutos a 4000 eventos/s ofrecidos almacenaron cada una los 2 400 000 eventos aceptados, sin perder ni duplicar ninguno. El almacenamiento fue la etapa más lenta, con el 96,7% y el 95,7% del ritmo ofrecido. El estado en vivo de los dispositivos se mantuvo a unos 40 segundos. |
| después de v0.18.0 | el mismo | ajustada: `event-management` con `persistence.writers: 10` y `persistence.maxBatch: 64`; ver abajo | unos 6000 eventos/s | Ejecuciones de 180 segundos. A 6000 ofrecidos, cada etapa mantuvo al menos el 98% del ritmo ofrecido, la cola se vació en 5 segundos y cada evento aceptado se almacenó exactamente una vez. Mantenido 5 minutos a 6000, el almacenamiento conservó el 96%, así que la cifra sostenida es de unos 5800 a 6000. |

Las filas de v0.18.0 se midieron en v0.18.0 y las demás en la versión de desarrollo que la
siguió, todas con el generador de carga en un nodo aparte y un almacén de eventos replicado
(`--ha`). Con la configuración predeterminada de v0.18.0, lo que frenó el ritmo fue el grupo de
resolutores de `device-management` y, más allá de él, los límites de CPU de `event-sources` y
`device-state` que el dimensionamiento de arriba aumenta. Con ellos aumentados, el límite pasó a
ser el almacenamiento de eventos: 5 escritores que confirmaban hasta 32 eventos cada uno llenaban
todos los lotes desde 4400 eventos por segundo, y cada confirmación tardaba unos 38 milisegundos,
así que el almacenamiento se detenía cerca de 4200 por segundo. Por eso `event-management` usa
ahora por defecto 10 escritores y lotes de hasta 64 (ver [Persistencia de
eventos](./observability.md#event-persistence)).

La fila ajustada de v0.18.0 usó `device-management` con 2 réplicas, `resolution.workers: 32` y
`rdbConfiguration.maxOpenConnections: 48`; `event-sources` con 2 réplicas; `event-management` con
`persistence.writers: 10` y `persistence.maxBatch: 64`, en un nodo sin el primario del almacén de
eventos; `device-state` con `projection.writers: 5`, `projection.maxBatch: 64` y
`projection.lingerMillis: 25`; límites de CPU de 4 núcleos (2 para `event-processing`); y límites
de memoria de 1Gi. La fila ajustada posterior dejó `device-management` con sus valores por defecto
y una réplica, y por lo demás usó los mismos ajustes de `event-management` y `device-state`, con
límites de CPU de 4 núcleos y de memoria de 1Gi para `device-management`, `event-sources`,
`event-management` y `device-state`. En ella, `event-management` usó como mucho unos 1,7 núcleos; no
se midió con su límite por defecto de 2. Sus lotes quedaron por debajo de 32 de media en todas las
ejecuciones, así que la medición no muestra que un lote de 64 ayude más que uno de 32. Una
instalación predeterminada con los nuevos valores de persistencia no se ha medido de extremo a
extremo, así que aquí no se da un ritmo sostenido para ella. Los lotes fueron de unos 21 eventos de media a 6000 por segundo; por encima, el almacenamiento dejó de
crecer con lotes de 28 a 30 eventos de media, por debajo del límite, mientras dos de los tres nodos, uno
de ellos el del almacén de eventos, estaban al 86-95% de CPU. No se aisló cuál de esas dos cosas
limitó el ritmo, pero para más rendimiento en ese clúster hacen falta más nodos antes que más
ajustes por servicio.

#### Volumen del almacén de eventos {#event-store-volume}

Cada instancia del almacén de eventos tiene un volumen de 32Gi, así que tres con `--ha` (4Gi cada
uno con `--compact`). Una medición almacenada ocupa alrededor de 1,05 KB, y el registro de
escritura anticipada de la base de datos ocupa alrededor de 1,1 GB más mientras las copias de
seguridad le siguen el ritmo, así que 32Gi contienen unos 27 millones de eventos: unas siete
horas de un inquilino que envía a su techo predeterminado completo.

El almacén de eventos comprime las imágenes de página de su registro de escritura anticipada
(`wal_compression = lz4`). Tras cada punto de control, el primer cambio en una página escribe la
página entera en el registro, y en este almacén la mayoría de esas páginas son páginas de índice.
En una comparación en una versión de desarrollo posterior a v0.18.0, en un clúster de la misma
forma que el de [Rendimiento medido](#measured-throughput), con un ajuste distinto del de las filas
de esa tabla, a 5200 eventos por segundo ofrecidos, la compresión redujo el registro escrito por
evento almacenado de unos 3,0 KB a unos 1,7 KB, y los puntos de control forzados por el tamaño del
registro bajaron en una proporción parecida (de 5,8 a 3,2 por millón de eventos almacenados). No
cambia lo que contiene este volumen: el registro sigue ocupando alrededor de 1,1 GB mientras las
copias de seguridad le siguen el ritmo, así que la cifra anterior se mantiene. El
[tamaño del almacén de respaldos](#backup-store-size) se midió sin compresión y no se ha vuelto a
medir con ella. El almacén relacional no comprime su registro.

Con el [almacén de respaldos predeterminado](#backup-store-size) y una sola instancia, este
volumen es lo primero que se llena con una ingesta sostenida, y `DatabaseVolumeFillingFast` avisa
antes. Si se llena primero el almacén de respaldos (varias instancias en él, un almacén más
pequeño o un almacén de eventos ampliado por encima de su tamaño predeterminado), el archivado se
detiene, y el registro que la base de datos no puede enviar se acumula en este volumen hasta que
se llena y la base de datos se detiene; las [alertas](./observability.md#backup-archiving) avisan
antes de ambas cosas. Una ventana de retención
(`retentionDays` en la configuración `lifecycle` de event-management) limita los datos
almacenados en una instancia que deba funcionar indefinidamente, pero no el archivo.

El tamaño se fija al crear una instancia; `dcctl upgrade` no cambia el volumen de una instancia
existente. Para ampliarlo, con una StorageClass que permita la expansión de volúmenes:

```bash
kubectl -n dci-<instance> patch clusters.postgresql.cnpg.io dc-tsdb --type merge \
  -p '{"spec":{"storage":{"size":"64Gi"}}}'
```

Ampliar este volumen mueve el punto en que el almacén de respaldos se llena primero. Amplía el
almacén de respaldos unas cinco veces lo que añadas aquí; consulta
[Tamaño del almacén de objetos de respaldo](#backup-store-size).

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
5. Los respaldos de su almacén de eventos, cuando están en el almacén de objetos propio del
   clúster (el [destino predeterminado](#default-backup-destination)): se borra todo lo que hay
   bajo la ruta a la que archivaba el almacén de eventos de la instancia —su archivo del log de
   escritura anticipada y sus respaldos base—, y destroy comprueba después que la ruta ha
   quedado vacía.
6. Comprueba que lo que eliminó ya no existe y, solo entonces, elimina su estado local en
   `~/.devicechain/instances/<instance>/`.

El artefacto de depósito de la clave raíz se conserva; consulta
[Recuperación ante desastres](./disaster-recovery.md#after-destroy).

### Qué pasa con los respaldos de la instancia {#destroy-backups}

Destroy lee a qué ruta archiva el almacén de eventos de la instancia antes de cambiar nada, la
muestra, y solo elimina esa ruta cuando el namespace de la instancia ya no existe, de modo que
nada sigue escribiendo en ella. Elimina esa única ruta y nada más: ni los respaldos de la base de
datos relacional compartida, ni los de otra instancia, ni un archivo anterior que haya quedado con
el mismo nombre de instancia.

- **Los respaldos en un almacén de objetos que tú proporcionaste**
  (`dcctl install --backup-credentials-file`) no se borran nunca. Son la copia que sobrevive al
  clúster. Destroy indica dónde están; bórralos tú cuando ya no los necesites.
- **`--keep-backups`** conserva también los respaldos del almacén interno. Úsalo cuando vayas a
  reconstruir la instancia a partir de ellos con `dcctl bootstrap --restore-tsdb-from` en el
  mismo clúster: un destroy sin esa opción borra precisamente el archivo que lee esa
  restauración. `dcctl destroy --all` acepta `--keep-backups` y lo aplica a todas las instancias.
- **Los respaldos base como instantáneas de volumen** (`dcctl install --backup-snapshot-class`)
  del almacén de eventos de la instancia están en su namespace, y se borran con él, con
  `--keep-backups` o sin él. Ninguna restauración los lee: `--restore-tsdb-from` lee el almacén de
  respaldos, que `--keep-backups` conserva.
- **Si no se puede acceder al almacén de objetos**, o destroy no puede saber qué ruta es la de la
  instancia, el destroy termina igualmente. Indica qué dejó, y su línea final no da la instancia
  por destruida del todo.

Un destroy que se interrumpe después de que el almacén de eventos haya desaparecido, incluido uno
interrumpido mientras elimina los respaldos, recuerda la ruta que leyó, así que ejecutarlo de nuevo
sigue eliminándolos.

Las versiones anteriores a esta dejaban los respaldos de una instancia destruida en el almacén
interno, donde nada los elimina nunca. Después de eliminar los respaldos de la propia instancia,
destroy enumera las rutas del bucket que parecen archivos anteriores del mismo nombre de
instancia, y las deja. No enumera nada cuando deja en su sitio los respaldos de la propia
instancia: con `--keep-backups`, en un almacén externo, o cuando no pudo acceder al almacén o
borrar en él. Para encontrar y eliminar esos archivos a
mano, accede al almacén mediante un port-forward y usa cualquier cliente S3, por ejemplo la CLI
de AWS:

```bash
kubectl -n dc-system port-forward svc/dc-object-store 9000:9000 &
export AWS_ACCESS_KEY_ID="$(kubectl -n dc-system get secret dc-object-store-credentials \
  -o jsonpath='{.data.MINIO_ROOT_USER}' | base64 -d)"
export AWS_SECRET_ACCESS_KEY="$(kubectl -n dc-system get secret dc-object-store-credentials \
  -o jsonpath='{.data.MINIO_ROOT_PASSWORD}' | base64 -d)"
aws s3 ls s3://devicechain-tsdb/ --endpoint-url http://127.0.0.1:9000
aws s3 rm --recursive s3://devicechain-tsdb/<path>/ --endpoint-url http://127.0.0.1:9000
```

Cada instancia en marcha archiva bajo la ruta que nombra su almacén de eventos; puedes verla con
`kubectl -n dci-<instance> get clusters.postgresql.cnpg.io dc-tsdb -o yaml` (el `serverName`
bajo `spec.plugins`). Elimina solo rutas que no use ninguna instancia en marcha y de las que nadie
vaya a restaurar.

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
