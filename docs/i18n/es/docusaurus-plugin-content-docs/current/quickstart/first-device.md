---
title: Tu primer dispositivo
---

# Tu primer dispositivo {#your-first-device-end-to-end}

Al terminar esta página, un dispositivo que creaste habrá enviado una lectura y la estarás viendo en
la consola. No necesitas hardware ni firmware. El «dispositivo» es un comando `curl`, que es todo lo
que un dispositivo es visto desde la plataforma.

Calcula alrededor de media hora, la mayor parte esperando al arranque inicial.

:::note Qué da por supuesto esta página
Necesitas `dcctl` más cinco herramientas en tu `PATH`: `docker`, `kubectl`, `helm`,
[`kind`](https://kind.sigs.k8s.io/) y [OpenTofu](https://opentofu.org/) (el binario `tofu`;
`terraform` también sirve). No necesitas un clúster de antemano. Los detalles, y por qué `helm` está
en la lista, están más abajo en [Requisitos previos](#prerequisites).
:::

## Requisitos previos {#prerequisites}

`dcctl install` y `dcctl bootstrap` ejecutan primero cada uno una comprobación previa, y se
**detienen** si falta alguna de las cinco herramientas. Una carencia te cuesta los diez primeros
segundos y no diez minutos.

- `helm` es obligatorio. `dcctl` lleva el chart dentro y lo instala con la biblioteca Go de Helm en
  vez de con el comando, pero la comprobación previa busca el binario igualmente.
- `ko` y `cloud-provider-kind` son solo advertencias. `ko` hace falta únicamente para compilar
  imágenes desde el código (`--build`).
- No hace falta un clúster de antemano. `dcctl install local` busca un clúster de kind llamado
  `devicechain` (o el nombre indicado con `--cluster`) y se ofrece a crear uno si no lo hay.
  `--kube-context <nombre>` lo apunta a un clúster que ya operas, y ese nunca lo crea ni lo
  borra.
- Kubernetes **1.29 o posterior**, en cualquier caso. Las versiones más antiguas se rechazan, porque
  los charts de la base de datos las rechazan.

`dcctl preflight local` ejecuta exactamente estas comprobaciones sin arrancar nada. La
[guía de arranque inicial](../deployment/bootstrap.md#prerequisites) tiene el detalle.

Los comandos de abajo suponen que la instancia es alcanzable en `localhost` por HTTP sin cifrar, que
es lo que producen las opciones del paso 1.

## 1. Levantar una instancia {#1-bring-up-an-instance}

Prepara el clúster una vez y después crea la instancia en él:

```bash
dcctl install local
dcctl bootstrap local devicechain --host localhost --no-tls
```

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

Abre la consola en `http://localhost/` e inicia sesión. Está vacía, porque todavía no hay ningún
inquilino y todo dispositivo pertenece a uno.

## 2. Crear un inquilino {#2-create-a-tenant}

Crear un inquilino es administración a nivel de instancia. En lugar de recorrer la API de
administración a mano, usa el comando que lo hace en un solo paso:

```bash
dcctl sim create demo
```

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

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001-cred","deviceToken":"sensor-001",
                         "credentialType":"ACCESS_TOKEN",
                         "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
                         "enabled":true}}}'
```

Elige tu propio `credentialId`: cualquier cadena no adivinable. En una credencial `ACCESS_TOKEN`, el
`credentialId` **es** el secreto que presenta el dispositivo, así que trátalo como una contraseña y
no como un nombre.

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
       "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
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
         \"credentialId\":\"5f989616-2a0d-4160-8ae1-da5fad2898b2\",
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
| `429` en el `POST` de ingesta | El inquilino supera su límite de tasa de ingesta: estás enviando más rápido de lo que permite su nivel. El evento no se aceptó. La respuesta lleva una cabecera `Retry-After`, así que espera y vuelve a enviarlo. |
| `503` en el `POST` de ingesta | El evento no pudo entregarse al stream, y **no** se almacenó. Reinténtalo. Aparte de `429` tras esperar, los demás estados son terminales para esa petición. |
| No autorizado en una llamada a la API | El token de acceso ha caducado, o estás enviando el `identityToken` de la primera llamada del paso 3 en vez del `accessToken` de la segunda. |

## Adónde ir después {#where-to-go-next}

- **Ejecuta una flota simulada.** Un dispositivo no es una flota. El `dcctl sim create` del paso 2
  también preparó un escenario simulado. Compila y ejecuta el simulador para que aprovisione un
  inquilino poblado y emita de forma continua:

  ```bash
  cd backend/sims/dc-simulator && make build
  ./build/dc-simulator --handshake ~/.devicechain/sims/demo.json
  ```

  Después contrólalo con `dcctl sim status demo`, `dcctl sim stop demo` y `dcctl sim start demo`.
  El simulador usa el mismo endpoint de ingesta, así que también necesita la redirección de puerto
  del paso 5.

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
