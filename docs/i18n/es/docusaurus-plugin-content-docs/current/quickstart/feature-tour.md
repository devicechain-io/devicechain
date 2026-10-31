---
title: Recorrido por las funciones
---

# Recorrido por las funciones {#feature-tour}

[Tu primer dispositivo](./first-device.md) termina en la primera lectura. Este recorrido sigue
adelante. Al final, un sitio funciona de extremo a extremo: un tipo de dispositivo y su perfil, dos
dispositivos que envían lecturas, un panel, una regla que genera una alarma y envía un comando, la
respuesta del dispositivo a ese comando y un **usuario cliente** que inicia sesión y ve solo su propio
dispositivo.

Después repites el mismo recorrido como un segundo tenant, con otro cliente, cambiando un único
archivo de parámetros. No se bifurca nada.

El recorrido es un conjunto de scripts de shell del repositorio, en
[`examples/feature-tour`](https://github.com/devicechain-io/devicechain/tree/main/examples/feature-tour).
Llaman a la misma API GraphQL pública y al mismo `dcctl` que usarías tú, y cada paso de abajo indica
también dónde lo hace la consola. DeviceChain no tiene una importación para un sitio completo: los
scripts son llamadas ordinarias a la API que puedes leer y modificar.

Calcula unos 45 minutos una vez que haya una instancia en marcha.

## Qué necesitas {#what-you-need}

- Una instancia en marcha y su consola. El [paso 1 de Tu primer dispositivo](./first-device.md#1-bring-up-an-instance)
  levanta una en un clúster local de kind. No necesitas su tenant ni su dispositivo: el recorrido
  crea los suyos.
- `bash`, `curl`, `jq`, `openssl`, `docker` y `kubectl` en tu `PATH`. En Windows usa Git Bash.
- Una copia del repositorio, para obtener los scripts:

  ```bash
  git clone https://github.com/devicechain-io/devicechain
  cd devicechain/examples/feature-tour
  ```

Tres ajustes indican dónde está tu instancia. Cada uno tiene un valor por defecto que coincide con un
`dcctl install local` normal seguido de `dcctl bootstrap local tour --host localhost --no-tls`, y cada
uno puede sustituirse desde el entorno:

| Variable | Valor por defecto | Defínela cuando |
| --- | --- | --- |
| `DC_URL` | `http://localhost` | La consola se sirve en otro host o puerto. |
| `INSTANCE` | `tour` | Tu instancia de `dcctl` tiene otro nombre. Da nombre al namespace `dci-<instance>`, del que los scripts leen la autoridad certificadora del broker y la contraseña del superusuario. |
| `MQTT_HOST` | `devicechain-control-plane` | Tu clúster de kind no se llama `devicechain`. Es el nombre del contenedor del nodo del clúster, `<cluster>-control-plane`. |

Si tu `kubectl` tiene más de un contexto, define también `KUBE_CONTEXT`.

```bash
export DC_URL=http://localhost INSTANCE=devicechain MQTT_HOST=devicechain-control-plane
```

Los dispositivos simulados son contenedores de la red de Docker `kind`, así que llegan al puerto de
nodo MQTT del broker por el nombre del nodo. En un clúster que no sea kind, ejecuta
[`device/coldroom.sh`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/device/coldroom.sh)
desde cualquier máquina que alcance el broker, o apunta tu propio cliente a él. Consulta
[Conexión de un dispositivo](../guides/connecting-a-device.md).

## Ejecútalo {#run-it}

```bash
./tour.sh sites/site-a.env all
```

Eso aprovisiona todo, arranca los dos dispositivos, espera a que la regla actúe y después comprueba
cada afirmación de esta página, imprimiendo `PASS` o `FAIL` en cada una. Las comprobaciones terminan
con `tour tour-a: all checks passed`. Las dos mitades pueden ejecutarse por separado:

```bash
./tour.sh sites/site-a.env up       # provision, dashboards, customer user, start the devices
./tour.sh sites/site-a.env verify   # wait for the loop, then check the operator's and the customer's views
```

Los scripts pueden ejecutarse de nuevo sin riesgo. Lo que ya existe se deja como está, y un panel
cuya definición cambió se actualiza y se publica como una versión nueva.

Las contraseñas que generan los scripts (la del administrador del sitio, la del usuario cliente y las
de los dispositivos) se escriben en `.state/<tenant>/`, que git ignora y que solo tú puedes leer. Nunca
se imprimen.

Las secciones siguientes recorren lo que hizo cada script y muestran el mismo paso en la consola.

## 1. El tenant y su administrador {#1-the-tenant}

`01-provision.sh` empieza en el nivel de instancia. Con la contraseña del superusuario tomada del
Secret que creó `dcctl bootstrap`, crea el tenant (`createTenant` en la API de administración) y una
identidad para el administrador del propio sitio, `operator@coldchain-a.example`, que es
`tenant-admin` de ese tenant y de ningún otro.

Todo lo que sigue se ejecuta como ese administrador, que es con quien inicias sesión: la consola del
superusuario es la consola de **Admin** de la instancia, sin páginas de tenant. En la consola, el mismo
trabajo es **Tenants → New tenant** e **Identities** en la consola de Admin.

Para iniciar sesión en la consola del tenant, abre `DC_URL`, escribe `operator@coldchain-a.example` y
usa la contraseña de `.state/tour-a/operator-password`.

## 2. Un tipo de dispositivo y su perfil {#2-device-type-and-profile}

Un **tipo de dispositivo** clasifica dispositivos, y su **perfil** es el contrato: qué mediciones
envía un dispositivo, qué comandos acepta y qué reglas vigilan sus lecturas. El script crea el perfil
`cold-room` con:

- las mediciones `temperature` y `humidity`,
- un comando, `boost-cooling`, y
- una regla de detección, `cold-room-too-warm`: cuando `temperature` supera el límite del sitio,
  genera una alarma mayor **y** envía `boost-cooling`. La regla es [`rule.json`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/rule.json),
  que es lo que guarda el formulario de reglas de la consola.

Después publica el perfil y crea el tipo de dispositivo `cold-room` sobre él. **Publicar importa:**
las reglas solo llegan a las lecturas en vivo cuando el perfil está publicado, y la plataforma compila
la regla en ese momento, de modo que una regla que no puede compilar se rechaza ahí y no falla en
silencio más tarde. Un perfil que declara comandos acepta únicamente esos comandos.

En la consola: **Devices → Device Profiles** (reglas, métricas y comandos) y **Devices → Device
Types**. Consulta [Tipos y perfiles de dispositivo](/user-guide/device-types-and-profiles) y
[Reglas de detección](/user-guide/detection-rules).

## 3. Dispositivos {#3-devices}

El script crea dos dispositivos del tipo, `coldroom-a1` y `coldroom-a2`, y da a cada uno una
credencial `MQTT_BASIC`. Un dispositivo presenta el nombre de usuario `<tenant>:<credentialId>` y su
contraseña; la plataforma guarda solo un resumen (digest) de la contraseña.

En la consola: **Devices → Devices** y después la pestaña **Credentials** de un dispositivo. Consulta
[Dispositivos](/user-guide/devices) y [Credenciales de dispositivo](../guides/device-credentials.md).

## 4. Un cliente y cuál es su dispositivo {#4-a-customer}

El script crea un tipo de cliente y el cliente `acme`, y **asigna** `coldroom-a1` a él: una relación
de tipo `assigned` del dispositivo al cliente. `coldroom-a2` se deja sin asignar a propósito, en el
mismo tenant. Eso permite que el último paso muestre al cliente viendo un dispositivo mientras el
operador ve ambos.

En la consola: **Organization → Customers** y la pestaña **Assignment** del dispositivo. Consulta
[Activos, clientes y áreas](/user-guide/assets-customers-areas) y
[Gestión de asignaciones](../guides/managing-assignments.md).

## 5. Lecturas {#5-readings}

```bash
./02-devices.sh sites/site-a.env up
```

arranca un contenedor por dispositivo. Cada uno son unas pocas líneas de shell alrededor de
`mosquitto_pub` y `mosquitto_sub`. La cámara fría se calienta un grado cada cuatro segundos, publica
`temperature` y `humidity` y mantiene abierta una suscripción para los comandos. No sabe nada de la
regla, la alarma ni el panel: informa y obedece. La suscripción es también lo que muestra al
dispositivo en línea.

En pocos segundos las lecturas están en la consola (la página del dispositivo, con los últimos
valores) y por la API:

```bash
curl -s "$DC_URL/api/device-state/graphql" -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"{latestMeasurements(deviceToken:\"coldroom-a1\"){name value unit}}"}'
```

`$DC_TOKEN` es un token de acceso del tenant. El [paso 3 de Tu primer dispositivo](./first-device.md#3-get-a-tenant-token)
muestra las dos llamadas que lo obtienen.

## 6. Tu primer panel {#6-first-dashboard}

Un panel es una definición JSON de widgets enlazados a un **slot**, como «el dispositivo que se está
viendo». Hay dos maneras de crear el primero, y el recorrido usa las dos.

**En la consola, desde el tipo de dispositivo.** Abre **Devices → Device Types → Cold room**, elige
la pestaña **Profile** y elige **Create dashboard**. El panel lateral lee lo que declara el perfil y
muestra una vista previa del tablero: un selector de dispositivo, una tarjeta por medición, un
gráfico, una tabla de alarmas y un botón por cada comando que es seguro enviar desde un tablero. Dale
un token, elige **Create draft and open editor** y el tablero se abre con datos en vivo. Es un
**borrador**: los espectadores no ven nada hasta que lo publicas desde el panel **History** del
editor. Consulta [Crear un panel desde un tipo de dispositivo](/user-guide/dashboards#from-a-device-type).

**Desde un script.** `03-dashboards.sh` construye el mismo tipo de tablero a partir de
[`dashboard.jq`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/dashboard.jq)
y lo crea con `createDashboard`, lo publica con `publishDashboard`, y lo hace dos veces:

- `tour-a-ops`, para el operador, con un botón **Send Boost cooling**; y
- `tour-a-customer`, sin el botón, que **comparte** con el cliente `acme`
  (`shareDashboard`). Un miembro limitado a un cliente ve un panel solo cuando está compartido con
  ese cliente y publicado.

Abre `DC_URL/dashboards/tour-a-ops`. Los dos tableros no difieren en nada salvo el botón, que es la
razón de generarlos a partir de una sola definición.

## 7. La regla genera una alarma {#7-the-rule-raises-an-alarm}

Espera medio minuto. Cuando una cámara pasa el límite, la regla se dispara: aparece una alarma en
**Monitor → Alarms** (y en la tabla y el contador de alarmas del tablero), y **se borra sola** cuando
la lectura baja de nuevo. Nada redacta el borrado: una regla se activa por flanco y su alarma dura
mientras se cumple su condición. La lista de alarmas conserva una fila por cada clave de alarma en
cada dispositivo, con su última activación.

La cámara vuelve a calentarse después de enfriarse, así que el ciclo se repite: una alarma y un
comando por cada exceso. Consulta [Alarmas](/user-guide/alarms) y
[Procesamiento de eventos](../concepts/event-processing.md).

## 8. El comando y su resultado {#8-command-outcome}

La misma activación envió el comando `boost-cooling`. El dispositivo lo recibió en su topic de
comandos, enfrió y **respondió**, con el token del propio comando y el nonce de la entrega. La
respuesta es lo que pasa el comando de `SENT` a `SUCCESSFUL`. Un dispositivo que obedece pero nunca
responde deja el comando en `SENT` hasta que vence el tiempo de espera.

`05-verify.sh` también envía un comando propio, como lo harías tú:

```graphql
mutation {
  createCommand(request: {
    token: "my-first-command",
    deviceToken: "coldroom-a1",
    name: "boost-cooling",
    payload: "{\"targetC\":3}"
  }) {
    command { token status }
    rejection { code reason }
  }
}
```

Vuelve como `QUEUED` y, unos segundos después, como `SUCCESSFUL`:

```graphql
query {
  commandsByToken(tokens: ["my-first-command"]) { status outcome responsePayload }
}
```

Comprueba `rejection` además de los errores: un comando rechazado es una respuesta correcta que lleva
un `rejection`. En la consola, la pestaña **Commands** de la página del dispositivo lista el
historial, y el botón del tablero envía uno. Consulta [Envío de un comando](../guides/sending-commands.md) y
[Comandos](../concepts/commands.md#command-lifecycle).

## 9. La vista del cliente {#9-customer-view}

`04-customer-user.sh` invita a `ops@acme.example` como miembro del tenant con el rol integrado de solo
lectura `viewer` y un ámbito de acceso de **un cliente**, `acme`, y después acepta la invitación con
una contraseña generada. En la consola esto se hace en **Access → Members → Invite member**, que
muestra un enlace de invitación de un solo uso que entregas tú mismo. El script lo acepta para que el
recorrido pueda ejecutarse sin supervisión.

Inicia sesión como `ops@acme.example` (la contraseña está en `.state/tour-a/customer-password`). El
usuario ve:

- una barra lateral con **Dashboards**, **Alarms** y **Devices**, y nada más;
- un dispositivo, `coldroom-a1`;
- un panel, `tour-a-customer`, con lecturas en vivo y la alarma de su propia cámara;
- ninguna señal de `coldroom-a2`, de sus lecturas, de sus alarmas ni del tablero del operador.

El indicador del tenant, arriba a la derecha, dice `Acme Foods · viewer`. `05-verify.sh` comprueba
cada uno de estos puntos como el usuario cliente, y también que enviar un comando se rechaza. Consulta
[Miembros, roles y usuarios cliente](/user-guide/members-roles-and-customer-users).

## Qué imprimen las comprobaciones {#what-the-checks-print}

```text
PASS  the rule raised an alarm on coldroom-a1 and its command ended SUCCESSFUL
PASS  createCommand boost-cooling was accepted (QUEUED), not rejected
PASS  your own command reached SUCCESSFUL (the device answered; last status: SUCCESSFUL)
PASS  the operator sees both devices (2)
PASS  the operator sees alarms on both devices (2 devices with alarms)
PASS  the customer sees exactly coldroom-a1 (saw: coldroom-a1)
PASS  the customer sees alarms for coldroom-a1 only (saw: coldroom-a1)
PASS  the customer reads no state of coldroom-a2 (0 measurements)
PASS  the customer lists only the board shared with them (saw: tour-a-customer)
PASS  the operator's board is invisible to the customer
PASS  the read-only customer user cannot send a command (refused: FORBIDDEN)
```

Un usuario cliente que pide un dispositivo que no es suyo recibe una respuesta vacía, no un error,
así que un script no puede distinguir «no es tuyo» de «no existe». Es lo previsto.

## El mismo recorrido para un segundo sitio {#second-site}

Un sitio es un archivo de parámetros. [`sites/site-a.env`](https://github.com/devicechain-io/devicechain/blob/main/examples/feature-tour/sites/site-a.env)
contiene todo lo que difiere entre sitios: el tenant, su administrador, el cliente, los tokens de los
dos dispositivos y los números que usan la regla y las cámaras simuladas.

```bash
./tour.sh sites/site-b.env all
```

El sitio B es otro tenant, `tour-b`, con otro cliente (`globex`, cuyo usuario es
`ops@globex.example`), otros dispositivos y un límite de alarma más bajo (6 grados en lugar de 8). Solo
difiere el archivo. Para crear un sitio propio, copia un archivo de sitio, cambia los valores y
ejecuta el mismo comando. Cada tenant tiene su propio administrador, usuario cliente, contraseña de
dispositivo y paneles, y ninguno puede ver al otro.

## Capítulos opcionales {#optional-chapters}

Son más breves, y cada uno lleva a la página que contiene el detalle. La columna **Activado por
defecto** indica si una instalación estándar ya ejecuta la función o si primero hay que activar algo.

| Capítulo | Qué añade | Requisitos previos | ¿Activado por defecto? | Empieza aquí |
| --- | --- | --- | --- | --- |
| Alta y autorregistro | Los dispositivos se presentan con una clave de aprovisionamiento compartida y crean su propia credencial, en lugar de crearse antes. | Un perfil de aprovisionamiento (**Connect → Provisioning Profiles**). Los dispositivos se registran por HTTPS a través del ingress de API de la instancia, así que necesitan una ruta hasta él. | La capacidad forma parte de la instalación estándar. Nada se registra hasta que creas un perfil de aprovisionamiento. | [Autorregistro de dispositivos](../guides/device-self-registration.md) |
| Certificados de cliente X.509 | Los dispositivos se autentican ante el broker con un certificado, fijado a un dispositivo o confiado mediante una CA que registra dispositivos en su primera conexión. | `openssl` y una CA de confianza (la guía crea una desechable). Solo MQTT; sin CRL ni OCSP. `examples/x509-device` del repositorio recorre todo el camino. | Nada que activar. La confianza existe solo cuando añades un ancla de confianza o fijas un certificado. | [Conexión con un certificado](../guides/device-x509.md) |
| Autenticación de dos factores | Un código de una app de autenticación en los inicios de sesión con contraseña, para una cuenta o exigido a un tenant. | La app de autenticación de cada persona; relojes correctos en todos los nodos. | Integrada y **no exigida**: cada persona la activa en **Account security**, y un administrador del tenant puede exigirla. | [Autenticación de dos factores](../guides/two-factor-authentication.md) |
| Configuración del dispositivo | Una configuración deseada, construida a partir de los atributos compartidos que declara el perfil, entregada al dispositivo con revisiones y un informe de aplicada o rechazada. | Declara las claves en el perfil y publícalo. El dispositivo debe usar MQTT; los dispositivos que solo hablan HTTP no reciben este canal. | Disponible en toda instalación. Un dispositivo no recibe nada hasta que el perfil declara claves. | [Conexión de un dispositivo](../guides/connecting-a-device.md#reserved-device-topics) |
| Actualizaciones remotas firmadas | Un catálogo de artefactos, firmantes, asignaciones firmadas y despliegues, con cada intento seguido hasta que el dispositivo demuestra la versión nueva. | Un almacén de objetos para el contenido de los artefactos (un volumen compartido o S3), un firmante registrado y un dispositivo MQTT que siga el protocolo de actualización. El dispositivo de referencia es `examples/ota-device`. | En el perfil `default`. Los perfiles `telemetry` e `ingest-only` lo omiten, y una instancia sin almacén de objetos rechaza los artefactos con `OBJECT_STORE_NOT_CONFIGURED`. | [Actualizaciones remotas](../concepts/over-the-air-updates.md) y [Firma de actualizaciones](../guides/signing-updates.md) |
| SQL y BI | SQL de solo lectura sobre la telemetría de tu propio tenant para Metabase, Grafana, Power BI o `psql`, mediante el esquema `analytics`. | Un lector que tú declaras. Las credenciales de base de datos de la propia plataforma nunca se entregan a una herramienta de BI. | El esquema `analytics` existe en toda instalación y no tiene lectores hasta que declaras uno. | [Acceso SQL y BI](../guides/sql-and-bi-access.md) |
| Widgets personalizados | Tu propio widget de React, dibujado en los paneles con el mismo enlace de datos que los integrados. | Node y npm, y recompilar la consola o `/dash` incluyendo tu paquete. No hay instalador en tiempo de ejecución. | No. Solo están los widgets integrados. | [Escritura de un widget personalizado](../guides/writing-a-custom-widget.md) y [Adición de paquetes de widgets](../guides/adding-widget-packages.md) |

## Si algo no funcionó {#if-something-did-not-work}

| Qué ves | Normalmente significa |
| --- | --- |
| `error: ... cannot sign in` de un script | No se ejecutó un paso anterior. Ejecuta primero `./01-provision.sh <site>`; crea la identidad con la que inician sesión los scripts posteriores. |
| `could not read the broker CA` | `INSTANCE` no es el nombre de tu instancia, o `kubectl` apunta a otro clúster. Define `INSTANCE` y `KUBE_CONTEXT`. |
| La comprobación espera y falla en la primera línea | Los dispositivos no están publicando. Ejecuta `./02-devices.sh <site> logs`. `publish refused` significa que la credencial o el nombre del tenant son incorrectos; un error de conexión significa que `MQTT_HOST` no es el nombre del contenedor del nodo de tu clúster. |
| Un dispositivo está en línea pero no aparece ninguna alarma | El perfil no se publicó, o el límite de la regla está por encima de donde la cámara deja de calentarse. Revisa `TOUR_ALARM_ABOVE` en el archivo del sitio. |
| El registro de un dispositivo dice `command ... answered` pero el comando sigue en `SENT` | La respuesta no se aceptó. Debe llevar el `token` y el `dispatchNonce` del propio comando. |
| El usuario cliente no ve ningún dispositivo | El dispositivo no está asignado al cliente, o no se definió el ámbito del miembro. `./04-customer-user.sh <site>` imprime el ámbito que encontró. |

## Limpieza {#cleaning-up}

```bash
./tour.sh sites/site-a.env down
./tour.sh sites/site-b.env down
```

detiene los dispositivos simulados. Los tenants permanecen. Eliminar un tenant revoca el acceso de
inmediato, pero su token se retiene al menos 12 horas antes de poder reutilizarse, y se rechaza
eliminar un tenant que aún tiene miembros. Consulta [Eliminación de un tenant](../deployment/tenant-deletion.md).
Para repetir el recorrido desde cero de inmediato, destruye la instancia con
`dcctl destroy local <instance>` y arranca una nueva, o da nuevos tokens de tenant al archivo del sitio.

## Siguientes pasos {#where-to-go-next}

- **[Paneles](/user-guide/dashboards)**: el lienzo, los widgets, las versiones y el uso compartido.
- **[Reglas de detección](/user-guide/detection-rules)**: el formulario, el lienzo y la reproducción del historial a través de una regla.
- **[Políticas de notificación](/user-guide/notification-policies)**: avisar a una persona cuando se genera una alarma.
- **[Comandos para una flota](../guides/commanding-a-fleet.md)**: un comando para muchos dispositivos.
