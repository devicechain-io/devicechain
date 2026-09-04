---
title: Su primer dispositivo
---

# Su primer dispositivo, de principio a fin

Al terminar esta página, un dispositivo creado por usted habrá enviado una lectura, y usted estará
mirando esa lectura en la consola. Sin hardware y sin firmware — el «dispositivo» es un comando
`curl`, que es todo lo que un dispositivo es visto desde la plataforma.

Calcule alrededor de media hora, la mayor parte esperando al arranque inicial.

:::note Qué da por supuesto esta página
`dcctl`, un clúster de Kubernetes (1.29 o posterior) al que pueda llegar con `kubectl`, y
[OpenTofu](https://opentofu.org/) (`tofu`) en su `PATH`. `dcctl` no crea el clúster —
[kind](https://kind.sigs.k8s.io/) es la opción habitual en local. Todo lo demás lo lleva dentro.
`dcctl preflight local` comprueba todo esto antes de empezar, y la
[guía de arranque inicial](../deployment/bootstrap.md#prerequisites) tiene el detalle.

Los comandos de abajo suponen que la instancia es alcanzable en `localhost` por HTTP sin cifrar, que
es lo que producen las opciones del paso 1.
:::

## 1. Levantar una instancia

```bash
dcctl bootstrap local devicechain --host localhost --no-tls
```

El id de instancia —aquí `devicechain`— no es decorativo. Pasa a ser el namespace, y es el primer
segmento de todos los topics de dispositivo y rutas de ingesta de esta página. Si elige otro,
sustitúyalo en todas partes.

Cuando el arranque termina, imprime el namespace, la URL de la consola y la credencial del
superusuario. El superusuario por defecto es `superuser@devicechain.local` con la contraseña
`devicechain`.

Abra la consola en `http://localhost/` e inicie sesión. Estará vacía — todavía no hay ningún
inquilino, y todo dispositivo pertenece a uno.

## 2. Crear un inquilino

Un inquilino es administración a nivel de instancia, así que en lugar de recorrer la API de
administración a mano, use el comando que hace todo el trámite de una vez:

```bash
dcctl sim create demo
```

Eso acuña un inquilino `sim-demo`, crea una identidad `demo@sim.devicechain.local` limitada a él con
el rol de administrador de inquilino y sin poder alguno sobre la instancia, y escribe un fichero de
handshake en `~/.devicechain/sims/demo.json`. Lea de ahí la contraseña generada de su identidad:

```bash
cat ~/.devicechain/sims/demo.json
```

El campo `simPassword` es la contraseña de `demo@sim.devicechain.local`. Usará ambos en el paso
siguiente.

:::tip Este comando existe para ejecutar simulaciones, y aquí es útil por su efecto secundario
`dcctl sim create` es en realidad la primera mitad del flujo del [simulador](#where-to-go-next). Lo
tomamos prestado porque acuñar un inquilino más una identidad limitada es exactamente lo que usted
necesita, y hacerlo a mano supone tres mutaciones en la API de administración de la instancia. Todo
lo que viene después de este paso es la API de inquilino ordinaria que usa cualquier aplicación.
:::

## 3. Obtener un token de inquilino

La autenticación son dos llamadas. La primera demuestra quién es usted; la segunda elige en qué
inquilino está actuando, porque una persona puede pertenecer a varios.

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($e:String!,$p:String!){login(email:$e,password:$p){identityToken}}",
       "variables":{"e":"demo@sim.devicechain.local","p":"<simPassword del paso 2>"}}'
```

Eso devuelve un `identityToken` — dice quién es usted, y nada sobre dónde está actuando.
Intercámbielo por un `accessToken` con alcance de inquilino:

```bash
curl -s -X POST http://localhost/api/user-management/graphql \
  -H 'Content-Type: application/json' \
  -d '{"query":"mutation($t:String!,$n:String!){selectTenant(identityToken:$t,tenant:$n){accessToken}}",
       "variables":{"t":"<identityToken>","n":"sim-demo"}}'
```

Guarde ese `accessToken`. Todas las llamadas a partir de aquí lo llevan:

```bash
export DC_TOKEN='<accessToken>'
```

## 4. Crear el dispositivo

Los dispositivos son tipados, así que primero va un tipo de dispositivo. Todo se direcciona por un
**token** que usted elige —un identificador estable y legible— y no por un id generado.

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

Ahora déle una credencial. Es lo que el dispositivo presenta para demostrar que es él mismo; la
plataforma espera una por defecto.

```bash
curl -s -X POST http://localhost/api/device-management/graphql \
  -H "Authorization: Bearer $DC_TOKEN" -H 'Content-Type: application/json' \
  -d '{"query":"mutation($r:DeviceCredentialCreateRequest!){createDeviceCredential(request:$r){token}}",
       "variables":{"r":{"token":"sensor-001-cred","deviceToken":"sensor-001",
                         "credentialType":"ACCESS_TOKEN",
                         "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
                         "enabled":true}}}'
```

Elija su propio `credentialId` — cualquier cadena no adivinable. En una credencial `ACCESS_TOKEN` el
`credentialId` **es** el secreto que presenta el dispositivo, así que trátelo como una contraseña y
no como un nombre.

Actualice la lista **Dispositivos** de la consola y ahí está `sensor-001`, todavía sin datos.

## 5. Abrir una vía hacia el endpoint de ingesta

El tráfico de dispositivo no entra por la misma puerta que la API. El ingress publica la consola y
`/api/…`; el listener de ingesta de dispositivo es un puerto aparte que una instalación estándar
**no** expone fuera del clúster. Redirija el puerto:

```bash
kubectl -n devicechain port-forward svc/event-sources 8081:8081
```

Déjelo corriendo en su propia terminal.

:::note Por qué existe este paso
Es una propiedad de la instalación por defecto, no de su configuración: hacer que el endpoint de
ingesta de una flota sea alcanzable públicamente es una decisión que un operador debe tomar a
propósito, así que nada la toma por usted. Un despliegue real lo expone deliberadamente; para un solo
`curl` desde su portátil, una redirección de puerto es lo más pequeño que puede hacer.
:::

## 6. Enviar una lectura

Esto es el dispositivo.

```bash
curl -i -X POST http://localhost:8081/devicechain/sim-demo/events \
  -H 'Content-Type: application/json' \
  -d '{"device":"sensor-001",
       "eventType":"Measurement",
       "credentialType":"ACCESS_TOKEN",
       "credentialId":"5f989616-2a0d-4160-8ae1-da5fad2898b2",
       "payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

`202 Accepted` significa que el evento se encoló. Dos cosas de ese cuerpo merecen atención ahora,
porque atrapan a casi todo el mundo una vez:

- **Todo payload envuelve sus lecturas en `entries`**, incluso una sola.
- **Todo valor numérico es una cadena JSON.** `"21.5"`, no `21.5`. Un número desnudo se rechaza.

La ruta es `/{instanceId}/{tenant}/events` — `devicechain` es la instancia del paso 1 y `sim-demo` el
inquilino del paso 2. Un `404` aquí casi siempre significa que uno de los dos está mal.

Envíe algunas más con valores distintos, para tener una línea que mirar en vez de un punto:

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

## 7. Ver sus datos

**En la consola**, abra `http://localhost/devices/sensor-001`. El dispositivo aparece ahora como
activo, con `temperature` y su último valor.

**Por la API**, lo mismo:

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

Eso es un dispositivo de principio a fin: registrado, con credencial, reportando y consultable.

## Si algo no funcionó

| Lo que ve | Normalmente significa |
| --- | --- |
| `404` en el `POST` de ingesta | El id de instancia o el inquilino de la ruta están mal. Son `devicechain` y `sim-demo` salvo que los cambiara. |
| Conexión rechazada en `:8081` | La redirección de puerto del paso 5 no está corriendo. |
| `400` en el `POST` de ingesta | Un número desnudo en vez de una cadena, o lecturas no envueltas en `entries`. |
| El evento se acepta pero no aparece nada | La credencial no coincidió. El `credentialId` del cuerpo debe ser exactamente el que creó en el paso 4. |
| `429` en el `POST` de ingesta | El inquilino supera su límite de tasa de ingesta — está enviando más rápido de lo que permite su nivel. |
| No autorizado en una llamada a la API | El token de acceso ha caducado, o está enviando el `identityToken` de la primera llamada del paso 3 en vez del `accessToken` de la segunda. |

## Adónde ir después {#where-to-go-next}

- **Un dispositivo no es una flota.** El `dcctl sim create` del paso 2 preparó además un escenario
  simulado. Compile y ejecute el simulador para que aprovisione un inquilino poblado y emita de forma
  continua:

  ```bash
  cd backend/sims/dc-simulator && make build
  ./build/dc-simulator --handshake ~/.devicechain/sims/demo.json
  ```

  Después, `dcctl sim status demo`, `dcctl sim stop demo`, `dcctl sim start demo`. Tenga en cuenta que
  el simulador usa el mismo endpoint de ingesta, así que también necesita la redirección de puerto del
  paso 5.

- **[Conexión de un dispositivo](../guides/connecting-a-device.md)** — el transporte real, MQTT, con la
  credencial en la conexión además de en el evento, más todas las formas de payload y las reglas que
  impone el pipeline.
- **[Matriz de capacidades por transporte](../reference/transport-matrix.md)** — qué admite cada
  transporte en cada dirección, antes de comprometerse con uno.
- **[Envío de un comando](../guides/sending-commands.md)** — la otra dirección.
- **[Procesamiento de eventos](../concepts/event-processing.md)** — convertir esas lecturas en alarmas.

## Limpieza

```bash
dcctl sim destroy demo
dcctl destroy local devicechain
```
