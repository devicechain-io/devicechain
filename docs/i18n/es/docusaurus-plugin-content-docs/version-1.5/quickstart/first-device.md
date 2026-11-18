---
title: Tu primer dispositivo
---

import YouTubeLite from '@site/src/components/YouTubeLite';

# Tu primer dispositivo {#your-first-device-end-to-end}

Al terminar esta página, un dispositivo que creaste habrá enviado una lectura y la estarás viendo en
la consola. No necesitas hardware ni firmware. El «dispositivo» es un comando `curl`, que es todo lo
que un dispositivo es visto desde la plataforma.

Calcula alrededor de media hora, la mayor parte esperando al arranque inicial.

**¿Prefieres verlo?** El video sigue esta página: instala DeviceChain en un portátil y envía la primera lectura de un dispositivo. Tiene audio en inglés, con subtítulos en inglés y subtítulos en español, y capítulos.

<YouTubeLite id="fWnVhI7icOk" title="Instala DeviceChain en un portátil y envía la primera lectura de un dispositivo" />

:::note Qué da por supuesto esta página
Necesitas `dcctl` más cuatro herramientas en tu `PATH`: `docker`, `kubectl`,
[`kind`](https://kind.sigs.k8s.io/) y [OpenTofu](https://opentofu.org/) (el binario `tofu`;
`terraform` también sirve). No necesitas un clúster de antemano. Los detalles están
más abajo en [Requisitos previos](#prerequisites).
:::

## Requisitos previos {#prerequisites}

`dcctl install` y `dcctl bootstrap` ejecutan primero cada uno una comprobación previa, y se
**detienen** si falta alguna de las cuatro herramientas. Una carencia te cuesta los diez primeros
segundos y no diez minutos.

- No necesitas el comando `helm`. `dcctl` lleva el chart dentro y lo instala con la biblioteca Go de
  Helm, y la comprobación previa no busca el binario.
- `ko` y `cloud-provider-kind` son solo advertencias. `ko` hace falta únicamente para compilar
  imágenes desde el código (`--build`).
- No hace falta un clúster de antemano. `dcctl install local` busca un clúster de kind llamado
  `devicechain` (o el nombre indicado con `--cluster`) y se ofrece a crear uno si no lo hay.
  `--kube-context <nombre>` lo apunta a un clúster que ya operas, y ese nunca lo crea ni lo
  borra.
- Los puertos **80**, **443** y **1883** de tu máquina deben estar libres. El clúster de kind los
  publica para la consola, HTTPS y MQTT, y crearlo falla si otro programa (a menudo otro clúster
  local de Kubernetes) ya ocupa alguno. `dcctl preflight local` nombra quién lo ocupa antes de
  empezar.
- **En Linux, un límite de inotify más alto.** `dcctl preflight local` exige `fs.inotify.max_user_instances` de al menos 256 para un clúster local de kind, y Ubuntu 24.04 de serie trae 128, por lo que falla e imprime el comando que hay que ejecutar. La primera línea sube el límite (la segunda, `max_user_watches`, es solo un aviso) y `/etc/sysctl.d` lo conserva tras los reinicios:

  ```bash
  echo 'fs.inotify.max_user_instances=512' | sudo tee /etc/sysctl.d/99-kind.conf
  echo 'fs.inotify.max_user_watches=524288' | sudo tee -a /etc/sysctl.d/99-kind.conf
  sudo sysctl --system
  ```
- Kubernetes **1.29 o posterior**, en cualquier caso. Las versiones más antiguas se rechazan, porque
  los charts de la base de datos las rechazan.

`dcctl preflight local` ejecuta las mismas comprobaciones, y las de tu motor de Docker, los límites del kernel, el disco y los puertos, sin arrancar nada. La
[guía de arranque inicial](../deployment/bootstrap.md#prerequisites) tiene el detalle.

Los comandos de abajo suponen que la instancia es alcanzable en `localhost` por HTTP sin cifrar, que
es lo que producen las opciones del paso 1.

## Obtener dcctl {#get-dcctl}

`dcctl` es un único archivo, adjunto a cada [versión de GitHub](https://github.com/devicechain-io/devicechain/releases)
como `dcctl_<versión>_<so>_<arq>`: un `.tar.gz` para Linux y macOS, un `.zip` para Windows, con
`<so>` igual a `linux`, `darwin` o `windows` y `<arq>` igual a `amd64` o `arm64`. El archivo
`checksums.txt` de la versión lista el resumen SHA-256 de cada uno. `<versión>` es la versión sin
la `v` inicial (`0.27.0` para la versión etiquetada `v0.27.0`).

En Linux o macOS:

```bash
curl -fLO https://github.com/devicechain-io/devicechain/releases/download/v<versión>/dcctl_<versión>_<so>_<arq>.tar.gz
curl -fLO https://github.com/devicechain-io/devicechain/releases/download/v<versión>/checksums.txt
sha256sum --ignore-missing -c checksums.txt   # en macOS: shasum -a 256 --ignore-missing -c checksums.txt
tar -xzf dcctl_<versión>_<so>_<arq>.tar.gz
sudo mv dcctl /usr/local/bin/
```

En Windows, en PowerShell:

```powershell
curl.exe -fLO https://github.com/devicechain-io/devicechain/releases/download/v<versión>/dcctl_<versión>_windows_amd64.zip
curl.exe -fLO https://github.com/devicechain-io/devicechain/releases/download/v<versión>/checksums.txt
(Get-FileHash dcctl_<versión>_windows_amd64.zip -Algorithm SHA256).Hash.ToLower()
Select-String windows_amd64 checksums.txt
Expand-Archive dcctl_<versión>_windows_amd64.zip -DestinationPath .
```

Los dos resúmenes deben coincidir. Después coloca `dcctl.exe` en un directorio de tu `PATH`.
`dcctl version` muestra la versión de la que procede, que es también la versión de las imágenes
que instala. Para comprobar además las firmas de la versión, consulta
[Verificar las versiones](../deployment/verifying-releases.md).

## 1. Levantar una instancia {#1-bring-up-an-instance}

Prepara el clúster una vez y después crea la instancia en él:

```bash
dcctl install local
dcctl bootstrap local devicechain --host localhost --no-tls
```

:::note En un script o un trabajo de CI
`dcctl bootstrap` pide una frase de contraseña para sellar el artefacto de custodia de la clave raíz. Sin un terminal donde preguntarla se detiene con `no escrow passphrase and no terminal to ask at`. Facilítala sin interacción, con un archivo o con una variable de entorno:

```bash
dcctl bootstrap local devicechain --host localhost --no-tls --escrow-passphrase-file ./escrow.pass
# o
DCCTL_ESCROW_PASSPHRASE='<passphrase>' dcctl bootstrap local devicechain --host localhost --no-tls
```

Para una instancia desechable puedes omitir la custodia con `--no-escrow` (`--dev` la implica). El coste: la clave raíz del almacén de secretos existe entonces solo dentro del clúster, así que si el clúster se pierde, todos los secretos que la instancia guardó quedan ilegibles para siempre, incluso desde una copia de seguridad de la base de datos. Consulta [la custodia de la clave raíz](../deployment/bootstrap.md#escrow).
:::

En un clúster kind local, el puerto **1883** de tu máquina llega al broker MQTT de la primera instancia que arranques. Habla TLS; consulta [Publicación de un evento](../guides/connecting-a-device.md#publishing-an-event) para conectarte.

`dcctl install` crea el clúster de kind e instala lo que comparten todas sus instancias: el operador
de DeviceChain y sus definiciones de recurso personalizado, la base de datos relacional, el operador
CloudNativePG, cert-manager, la monitorización y el ingress. Se ejecuta una vez por clúster, y
`dcctl bootstrap` se niega en un clúster donde no ha terminado. Consulta
[Instalar el clúster](../deployment/bootstrap.md#install).

El id de instancia (aquí `devicechain`) importa en dos sitios:

- Da nombre al namespace de Kubernetes de la instancia, como el id detrás del prefijo `dci-`
  (`dci-devicechain`).
- Es el primer segmento de todos los topics de dispositivo y rutas de ingesta de esta página.

Si eliges otro id, sustitúyelo en todas partes: tal cual en los topics y las rutas, y detrás del
prefijo `dci-` allí donde un comando nombre el namespace.

Cuando el arranque termina, imprime el namespace, la URL de la consola y la credencial del
superusuario. El superusuario es `superuser@devicechain.local`. No hay contraseña por defecto: el
arranque genera una para esta instancia y la imprime una sola vez, al final de su salida. Para
volver a leerla más tarde:

```bash
kubectl -n dci-devicechain get secret dci-devicechain-superuser -o jsonpath='{.data.password}' | base64 -d
```

Ese Secret conserva la contraseña que el superusuario recibió **al principio**. Si cambias la
contraseña en la consola, el Secret no se actualiza.

Abre la consola en la URL que imprimió el bootstrap (`http://localhost/` en una instalación por
defecto) e inicia sesión. Como superusuario llegas a la consola de **administración** de la
instancia. No tiene páginas de inquilino y está vacía de dispositivos, porque todavía no hay ningún
inquilino y todo dispositivo pertenece a uno. El superusuario administra la instancia; no es un
usuario de inquilino. Para trabajar en la consola de inquilino necesitas una identidad de
administrador de inquilino, que crea el paso siguiente.

## 2. Crear un inquilino {#2-create-a-tenant}

Crear un inquilino es administración a nivel de instancia. En lugar de recorrer la API de
administración a mano, usa el comando que lo hace en un solo paso:

```bash
dcctl sim create demo
```

`dcctl sim create` se comunica por defecto con la instancia en `localhost`. Si la consola se sirve en otro puerto, indica host y puerto en `--server`, y también el ingreso del plano de dispositivos, porque este se deriva del host y no conoce el puerto:
`dcctl sim create demo --server localhost:8080 --ingress http://localhost:8081`.

Este comando:

- acuña un inquilino `sim-demo`,
- crea una identidad `demo@sim.devicechain.local` limitada a él, con el rol de administrador de
  inquilino y sin ningún poder sobre la instancia, y
- escribe un fichero de handshake en `~/.devicechain/sims/demo.json`.

Lee la contraseña generada de tu identidad en el fichero de handshake:

```bash
cat ~/.devicechain/sims/demo.json
```

El campo `simPassword` es la contraseña de `demo@sim.devicechain.local`. Usarás ambos en el paso
siguiente.

Para ver el inquilino en la consola, cierra la sesión del superusuario e inicia sesión como
`demo@sim.devicechain.local` con esa contraseña. Esta es la identidad que abre la consola de
inquilino. Si prefieres no usar `dcctl sim create`, crea la identidad con la mutación
`createIdentity` de la API de administración de la instancia y concédele el rol de administrador de
inquilino en el inquilino con `addMembership`.

:::tip Tomado prestado del simulador
`dcctl sim create` es la primera mitad del flujo del [simulador](#where-to-go-next). Se usa aquí
porque acuña un inquilino más una identidad limitada, que es exactamente lo que necesitas; hacerlo a
mano supone tres mutaciones en la API de administración de la instancia. Todo lo que viene después
de este paso es la API de inquilino ordinaria que usa cualquier aplicación.
:::

## 3. Obtener un token de inquilino {#3-get-a-tenant-token}

La autenticación son dos llamadas. La primera demuestra quién eres. La segunda elige en qué
inquilino estás actuando, porque una persona puede pertenecer a varios.

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}",
       "variables":{"e":"demo@sim.devicechain.local","p":"<simPassword del paso 2>"}}'
```

Eso devuelve un `identityToken`. Dice quién eres, y nada sobre dónde estás actuando.
Intercámbialo por un `accessToken` con alcance de inquilino:

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}",
       "variables":{"t":"<identityToken>","n":"sim-demo"}}'
```

Guarda ese `accessToken`. Todas las llamadas a partir de aquí lo llevan:

```bash
export DC_TOKEN='<accessToken>'
```

Un token de acceso dura **15 minutos**. Cuando las llamadas empiecen a fallar con un error de autenticación, repite las dos
peticiones anteriores (`login` y después `selectTenant`) y exporta el nuevo `accessToken`.

## 4. Crear el dispositivo {#4-create-the-device}

Los dispositivos son tipados, así que primero creas un tipo de dispositivo. Todo se direcciona por
un **token** que tú eliges, un identificador estable y legible, y no por un id generado.

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceTypeCreateRequest){createDeviceType(request:$r){token}}",
       "variables":{"r":{"token":"temp-probe","name":"Sonda de temperatura"}}}'
```

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCreateRequest){createDevice(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001","deviceTypeToken":"temp-probe","name":"Sensor de banco"}}}'
```

Ahora dale una credencial al dispositivo. La credencial es lo que el dispositivo presenta para
demostrar que es él mismo, y la plataforma espera una por defecto.

Primero genera el identificador de la credencial y guárdalo en una variable del shell, para que este paso y el paso 6 usen el mismo:

```bash
export DC_CRED_ID="$(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || openssl rand -hex 16)"
```

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001-cred","deviceToken":"sensor-001",
                         "credentialType":"ACCESS_TOKEN",
                         "credentialId":"'"$DC_CRED_ID"'",
                         "enabled":true}}}'
```

El `credentialId` es cualquier cadena no adivinable; el comando anterior generó una para ti, así que cada lector obtiene un secreto distinto. En una credencial `ACCESS_TOKEN`, el
`credentialId` **es** el secreto que presenta el dispositivo, así que trátalo como una contraseña y
no como un nombre. La plataforma conserva solo un resumen irreversible de él, así que guarda tu propia
copia: la respuesta a esta solicitud es la última vez que puede leerse.

Actualiza la lista **Dispositivos** de la consola. Ahí está `sensor-001`, todavía sin datos.

## 5. Abrir una vía hacia el endpoint de ingesta {#5-open-a-path-to-the-ingest-endpoint}

El tráfico de dispositivo no entra por la misma puerta que la API. El ingress publica la consola y
`/api/…`. El listener de ingesta de dispositivo es un puerto aparte que una instalación estándar
**no** expone fuera del clúster. Redirige el puerto:

```bash
kubectl -n dci-devicechain port-forward svc/event-sources 8081:8081
```

Déjalo corriendo en su propia terminal.

:::note Por qué existe este paso
Es una propiedad de la instalación por defecto, no de tu configuración. Hacer que el endpoint de
ingesta de una flota sea alcanzable públicamente es una decisión que un operador debe tomar a
propósito, así que nada la toma por ti. Un despliegue real lo expone deliberadamente; para un solo
`curl` desde tu portátil, una redirección de puerto es lo más sencillo.
:::

## 6. Enviar una lectura {#6-send-a-reading}

Este `curl` es el dispositivo:

```bash
curl -i -X POST http://localhost:8081/devicechain/sim-demo/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001",
       "eventType":"Measurement",
       "credentialType":"ACCESS_TOKEN",
       "credentialId":"'"$DC_CRED_ID"'",
       "payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

`202 Accepted` significa que el evento se encoló. Dos reglas de ese cuerpo atrapan a casi todo el
mundo alguna vez:

- **Todo payload envuelve sus lecturas en `entries`**, incluso una sola.
- **Todo valor numérico es una cadena JSON:** `"21.5"`, no `21.5`. Un número desnudo se rechaza.

La ruta es `/{instanceId}/{tenant}/events`. `devicechain` es la instancia del paso 1 y `sim-demo` es
el inquilino del paso 2. Un `404` aquí significa que el **id de instancia** está mal, porque la ruta
solo existe bajo el id propio de esta instancia.

Un **inquilino** equivocado no devuelve `404`. Cualquier nombre de inquilino bien formado se acepta
con `202`, exista o no un inquilino con ese nombre. El evento se descarta más abajo en la cadena, y
nada en la respuesta lo dice. Si un `202` no produce datos, comprueba el nombre del inquilino antes
que cualquier otra cosa.

Envía algunas lecturas más con valores distintos, para tener una línea que mirar en vez de un
punto:

```bash
for t in 21.9 22.4 22.1 23.0; do
  curl -s -o /dev/null -X POST http://localhost:8081/devicechain/sim-demo/events \
    -H 'Content-Type: application/json' \
    -d "{\"device\":\"sensor-001\",\"eventType\":\"Measurement\",
         \"credentialType\":\"ACCESS_TOKEN\",
         \"credentialId\":\"$DC_CRED_ID\",
         \"payload\":{\"entries\":[{\"measurements\":{\"temperature\":\"$t\"}}]}}"
  sleep 1
done
```

## 7. Ver tus datos {#7-see-your-data}

En la consola, abre `http://localhost/devices/sensor-001`. El dispositivo aparece ahora como
**En línea**, con `temperature` y su último valor. Nada lo declaró en línea: para un dispositivo
sobre HTTP, la presencia se infiere del hecho de que llegó un evento.

Por la API, los mismos últimos valores:

```bash
curl -s -X POST http://localhost/api/device-state/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{latestMeasurements(deviceToken:\"sensor-001\"){name value unit occurredTime}}"}'
```

Y el histórico en lugar del último valor:

```bash
curl -s -X POST http://localhost/api/event-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{measurementEvents(criteria:{pageNumber:1,pageSize:20,deviceToken:\"sensor-001\"}){results{name value occurredTime} pagination{totalRecords}}}"}'
```

Ya tienes un dispositivo de principio a fin: registrado, con credencial, reportando y consultable.

## Solución de problemas {#if-something-did-not-work}

| Lo que ves | Normalmente significa |
| --- | --- |
| `404` en el `POST` de ingesta | El **id de instancia** de la ruta está mal. Es `devicechain` salvo que lo cambiaras. Un inquilino equivocado no produce esto. |
| Conexión rechazada en `:8081` | La redirección de puerto del paso 5 no está corriendo. |
| `400` en el `POST` de ingesta | Un número desnudo en vez de una cadena, lecturas no envueltas en `entries`, o un segmento de inquilino que no es un token válido. |
| `202`, pero no aparece nada | O el **inquilino** no existe (se acepta un nombre bien formado, exista o no ese inquilino), o la credencial no coincidió. El `credentialId` del cuerpo debe ser exactamente el que creaste en el paso 4. |
| `429` en el `POST` de ingesta | El inquilino supera su techo de ingesta: estás enviando más lecturas por segundo de las que permite su nivel. El evento no se aceptó. La respuesta lleva una cabecera `Retry-After`, así que espera y vuelve a enviarlo. |
| `503` con cabecera `Retry-After` en el `POST` de ingesta | La plataforma está rechazando eventos nuevos de todos los inquilinos ([contrapresión](../deployment/observability.md#ingest-backpressure)). El evento **no** se almacenó. Espera los segundos que indica `Retry-After` (10) y vuelve a enviarlo. `curl -i` muestra las cabeceras de la respuesta. |
| `503` sin cabecera `Retry-After` en el `POST` de ingesta | El evento no pudo entregarse al stream, y **puede** que se haya almacenado igualmente. Vuelve a enviarlo, pero el reenvío almacena una segunda copia salvo que el evento lleve `altId` y `occurredTime` (consulta [calidad de servicio](../guides/connecting-a-device.md#quality-of-service)). |
| No autorizado en una llamada a la API | El token de acceso ha caducado, o estás enviando el `identityToken` de la primera llamada del paso 3 en vez del `accessToken` de la segunda. |

`202`, `400` y `404` son terminales para esa petición: no la vuelvas a enviar sin cambios. Reintenta
un `429` y cualquiera de los dos tipos de `503`, como se describe arriba.

## A continuación: una alarma y un comando {#next-an-alarm-and-a-command}

Sigue con `sensor-001` en `sim-demo`, con la sesión de `demo@sim.devicechain.local`. El dispositivo
creado en el paso 4 no tiene perfil: almacena lecturas, pero no tiene reglas de detección ni comandos
declarados. Renueva `DC_TOKEN` con el paso 3 si ha caducado.

### Generar tu primera alarma {#first-alarm}

1. En **Devices → Device Profiles**, crea un perfil con token `temp-probe-profile`, nombre
   **Temperature probe profile** y categoría **sensor**. En **Metrics**, añade una métrica con token
   y clave `temperature`, tipo **DOUBLE** y unidad `Cel`.
2. En **Devices → Device Types**, abre `temp-probe`. En **Profile**, adjunta `temp-probe-profile`.
   Consulta [Tipos de dispositivo y perfiles](../user-guide/device-types-and-profiles.md).
3. En **Detection Rules** del perfil, elige **New detection rule → Form**. Indica token
   `temperature-high`, nombre **Temperature high**, tipo **Threshold**, severidad **Warning** y
   **Enabled**. Configura la condición `temperature > 25` y añade una acción **Raise alarm** con
   clave de alarma `temperature-high`. Guarda la regla y elige **Publish** en **Versions**. Un
   borrador guardado no se ejecuta. Consulta [Reglas de detección](../user-guide/detection-rules.md).
4. Repite la petición HTTP del paso 6 con `"temperature":"30.0"`. En **Monitor → Alarms**, busca
   la alarma activa de `sensor-001`. Envía `"temperature":"21.5"` para resolverla. Envía estas lecturas
   nuevas después de publicar; las anteriores no se reproducen automáticamente.

### Enviar tu primer comando {#first-command}

1. En **Commands** del mismo perfil, añade un comando con token y clave `identify`, nombre
   **Identify**, impacto **Normal** y sin esquema de parámetros. Vuelve a **Publish** el perfil.
2. El `curl` HTTP del paso 6 no recibe comandos. Antes de enviar uno, sigue
   [Recibir comandos](../guides/connecting-a-device.md#receiving-commands) para iniciar un suscriptor
   MQTT (instala los clientes Mosquitto y usa la CA de la instancia como indica esa guía).
   Usa host `localhost`, id de cliente `devicechain:sim-demo:sensor-001:sub`, usuario
   `sim-demo:$DC_CRED_ID` (expande la variable), contraseña `$DC_CRED_ID` para su credencial
   `ACCESS_TOKEN` y tema `devicechain/sim-demo/device-commands/sensor-001`. Déjalo conectado. Para conexiones locales a `localhost`, sigue también las instrucciones de la guía sobre el nombre de host TLS.
3. Emite este comando de prueba inocuo en la API del inquilino:

```bash
curl -s -X POST http://localhost/api/command-delivery/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation { createCommand(request: {token: \"first-identify\", deviceToken: \"sensor-001\", name: \"identify\", payload: \"{}\"}) { command { token status } rejection { code reason } } }"}'
```

4. Comprueba `rejection`: debe ser null y debe existir `command`. Lee el sobre de entrega en el
   suscriptor. Sigue [Responder a un comando](../guides/connecting-a-device.md#responding-to-a-command)
   para publicar `success: true` en `devicechain/sim-demo/command-responses/sensor-001`, con las
   mismas credenciales MQTT, otro sufijo de id de cliente y los valores reales de `token` y
   `dispatchNonce` del sobre. Esto confirma la prueba; no actúa ningún dispositivo físico.
5. En **Commands** de `sensor-001`, comprueba que `first-identify` alcanza **SUCCESSFUL**.
   Sin respuesta permanece **SENT** hasta agotar el plazo; sin conexión receptora puede permanecer
   **HELD**. [Enviar un comando](../guides/sending-commands.md) explica rechazos, consultas y
   cancelación. Para repetir la prueba como un comando nuevo, elige otro token de comando.

## Adónde ir después {#where-to-go-next}

- **Ejecuta una flota simulada.** Un dispositivo no es una flota. El `dcctl sim create` del paso 2
  también preparó un escenario simulado. Descarga el simulador y ejecútalo para que aprovisione un
  inquilino poblado y emita de forma continua. No necesitas el toolchain de Go: `dc-simulator` va
  adjunto a cada [release de GitHub](https://github.com/devicechain-io/devicechain/releases) como
  `dc-simulator_<versión>_<so>_<arquitectura>` (un `.tar.gz`, o un `.zip` en Windows), para Linux,
  macOS y Windows en `amd64` y `arm64`, con sus resúmenes SHA-256 en `dc-simulator_checksums.txt`
  en la misma release. Descomprímelo y luego ponlo en tu `PATH` o indícaselo a `dcctl`:

  ```bash
  tar -xzf dc-simulator_<versión>_<so>_<arquitectura>.tar.gz
  dcctl sim run demo --simulator ./dc-simulator
  ```

  `dcctl sim run` encuentra por sí mismo el archivo de handshake y el puerto de control de la
  simulación, y busca `dc-simulator` en tu `PATH` cuando omites `--simulator`. Quien contribuya y
  compile desde un checkout del código puede ejecutar `cd backend/sims/dc-simulator && make build`
  y usar `./build/dc-simulator`.

  Después contrólalo con `dcctl sim status demo`, `dcctl sim stop demo` y `dcctl sim start demo`.
  El simulador usa el mismo endpoint de ingesta, así que también necesita la redirección de puerto
  del paso 5.

- **[Recorrido por las funciones](./feature-tour.md)**: sigue desde aquí hasta un panel, una regla
  que genera una alarma y envía un comando, la respuesta del dispositivo y un usuario cliente que
  ve solo su propio dispositivo; después repite el mismo recorrido como segundo tenant.

- **[Conexión de un dispositivo](../guides/connecting-a-device.md)**: el transporte real, MQTT, con
  la credencial en la conexión además de en el evento, más todas las formas de payload y las reglas
  que impone el pipeline.
- **[Matriz de capacidades por transporte](../reference/transport-matrix.md)**: qué admite cada
  transporte en cada dirección, antes de comprometerte con uno.
- **[Envío de un comando](../guides/sending-commands.md)**: la otra dirección.
- **[Procesamiento de eventos](../concepts/event-processing.md)**: convertir esas lecturas en alarmas.

## Limpieza {#cleaning-up}

```bash
dcctl sim destroy demo
dcctl destroy local devicechain
```

`dcctl destroy` elimina la instancia y deja el clúster instalado, listo para el siguiente arranque
inicial. Espera a que el namespace de la instancia desaparezca por completo, así que el clúster
queda listo de inmediato para un arranque con el mismo nombre. Si se interrumpe, ejecutarlo de
nuevo termina el trabajo.

:::warning Aparta antes el artefacto de depósito
Antes de volver a arrancar con el mismo nombre, aparta el artefacto de depósito que nombra
`destroy`. La siguiente instancia acuña una clave propia, y el arranque inicial no sobrescribe la
anterior.
:::

Para eliminar también el clúster:

```bash
kind delete cluster --name devicechain
```
