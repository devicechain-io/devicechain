---
sidebar_position: 1
title: Desarrollo local
---

# Desarrollo local

Puedes ejecutar DeviceChain localmente con solo dos dependencias: NATS y TimescaleDB. No necesitas
Java, Kafka, ZooKeeper, Redis, Keycloak ni Mosquitto.

:::note Estado
DeviceChain es pre-release. Esta guía trata sobre trabajar en el árbol de código: compilar el
workspace de Go y ejecutar un único servicio contra dependencias que tú mismo has iniciado. Para
tener una instancia completa en ejecución, usa `dcctl` y la [Guía rápida](../quickstart/first-device.md).
Dos comandos, `dcctl install` y `dcctl bootstrap`, levantan todo.
:::

## Requisitos previos

- **Go** 1.26 o posterior. El workspace declara `go 1.26.6`, y CI compila con la versión que nombra `go.work`.
- **Node** 22 o posterior, para el frontend y esta documentación. CI compila con 26.
- **Docker**, para ejecutar TimescaleDB.
- **nats-server**, un único binario de unos 10 MB.

## 1. Iniciar la infraestructura

Inicia TimescaleDB en Docker:

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17
```

NATS necesita JetStream. Para conectar un dispositivo por MQTT, también necesita la pasarela MQTT
integrada del broker. MQTT **no tiene una opción de línea de comandos**: es un bloque de
configuración, y requiere JetStream. Un broker en clúster debe fijar además `server_name`. Escribe
un pequeño archivo de configuración en lugar de pasar banderas:

```bash
cat > nats.conf <<'EOF'
server_name: dc-local
jetstream: enabled
http_port: 8222
mqtt { port: 1883 }
EOF

nats-server -c nats.conf
```

Cuando la pasarela está activa, el servidor registra `Listening for MQTT clients on mqtt://0.0.0.0:1883`.

Si solo necesitas la mensajería básica y JetStream, `nats-server -js -m 8222` es suficiente. No
abre ningún listener MQTT.

## 2. Compilar el workspace

El backend es un workspace de Go (`go.work`) que abarca la biblioteca principal (core), el operador,
la CLI y los servicios. Las compilaciones están acotadas al módulo. La raíz del repositorio no es en
sí un módulo de Go, por lo que un patrón `./...` anclado ahí no coincide con nada:

```
pattern ./...: directory prefix . does not contain modules listed in go.work or their selected dependencies
```

Ni `go build ./...` ni `go build ./backend/...` funcionan desde la raíz del árbol. Compila desde
dentro del módulo en el que estés trabajando, como hace CI:

```bash
cd backend/core     # ...o el módulo que hayas tocado
gofmt -l .          # no debe imprimir nada
go build ./...
go vet ./...
go test ./... -count=1
```

Para recorrer todo el workspace, deja que `go.work` enumere sus propios módulos en lugar de
listarlos a mano:

```bash
rc=0
for m in $(go list -m -f '{{.Dir}}'); do
  ( cd "$m" || exit 1
    fmt="$(gofmt -l .)"; [ -z "$fmt" ] || { echo "not gofmt-clean:"; echo "$fmt"; exit 1; }
    go build ./... && go vet ./... && go test ./... -count=1
  ) || { echo "FAILED: $m"; rc=1; }
done
echo "sweep exit status: $rc"
```

Tres detalles de ese bucle importan. Sin cualquiera de ellos, una comprobación pasaría sin mirar
nada:

- **La salida de `gofmt -l` se captura, no solo se ejecuta.** Termina con estado 0 *incluso cuando
  nombra archivos*, así que el bucle comprueba su salida con `[ -z "$fmt" ]`. Comprobar su estado de
  salida daría una barrera que nunca puede fallar.
- **`-count=1` no es opcional.** Unas pocas pruebas leen archivos fuera de su propio módulo. La
  caché de pruebas de Go no rastrea esos archivos, así que un PASS en caché puede sobrevivir a un
  cambio que debería hacerlo fallar.
- **`rc` se registra, no solo se imprime.** Con `… || echo "FAILED: $m"` por sí solo, el estado de
  salida del bucle sería el del último `echo`. Todos los módulos podrían fallar y el recorrido
  seguiría pareciendo correcto.

## 3. Ejecutar un servicio

Cada servicio es un único binario que no acepta banderas. No arranca con un entorno vacío. Al
iniciarse, lee su identidad de variables de entorno y sus ajustes de dos documentos en rutas fijas.
El chart de Helm monta esos mismos dos documentos en cada pod.

- `DC_INSTANCE_ID` y `DC_MS_FUNCTIONAL_AREA` son **obligatorias**: la instancia a la que pertenece
  el servicio y el área del propio servicio (`event-sources` para el comando de abajo). El servicio
  se niega a arrancar si falta cualquiera de las dos.
- `DC_LOG_CONSOLE=1` cambia la salida de log de JSON a un formato de consola legible. No cambia
  cuánto se registra. El nivel viene de `infrastructure.logging.level` en el documento de
  instancia, y es `info` cuando el documento no lo fija (consulta
  [Registros](../deployment/observability.md#logs)).
- `/etc/dci-config/instance` es el documento de toda la instancia: nombre de host y puerto de NATS,
  los ajustes de base de datos y persistencia, y el resto de la infraestructura compartida. Su forma
  es el tipo `InstanceConfiguration` del paquete `config` de la biblioteca principal. Aquí es donde
  apuntas el servicio al NATS y al TimescaleDB que iniciaste en el paso 1.
- `/etc/dct-config/<functional-area>` es el documento por servicio, tipado en el paquete `config`
  del propio servicio (aquí, el del servicio event-sources). Un documento vacío es válido y aplica
  los valores por defecto tipados.

Ambos documentos se decodifican de forma estricta: una clave desconocida se rechaza, no se ignora.
Ambas rutas son constantes, sin bandera ni variable de entorno que las sustituya. Para ejecutar un
servicio contra tu propia infraestructura, escribe esos dos archivos bajo `/etc` y exporta las
variables:

```bash
export DC_INSTANCE_ID=dc-local DC_MS_FUNCTIONAL_AREA=event-sources DC_LOG_CONSOLE=1
go run ./backend/services/event-sources
```

`go run` recibe la ruta de un solo paquete, así que se resuelve dentro de ese módulo y funciona
desde la raíz del repositorio, a diferencia de los patrones `./...` de arriba.

Si prefieres que los archivos se generen por ti en lugar de escribirlos a mano, usa `dcctl install`
y `dcctl bootstrap`, que producen una instancia completa (consulta la nota al inicio de esta página).

## Estructura del repositorio

```
backend/
  core/                 biblioteca compartida (lifecycle, NATS, GORM, GraphQL, config, auth, secrets)
  k8s/                  operador + tipos de CRD
  services/             un módulo por microservicio — user-management, device-management,
                        event-sources, event-management, device-state, command-delivery,
                        dashboard-management, notification-management, event-processing,
                        outbound-connectors, ai-inference, mcp, y las áreas de ingesta edge
  edge/                 el agente edge
  sims/                 el simulador de dispositivos
  cli/                  dcctl
  tools/                herramientas solo para mantenedores (no se distribuyen)
frontend/               workspace npm: las apps de consola y dashboard más los paquetes compartidos
docs/                   este sitio de documentación
deploy/                 chart de Helm + módulos de OpenTofu
sdks/                   SDKs de cliente
```

## Próximos pasos

- [Conexión de un dispositivo](./connecting-a-device.md)
- [Arquitectura](../concepts/architecture.md)
