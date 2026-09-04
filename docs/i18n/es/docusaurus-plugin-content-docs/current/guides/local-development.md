---
sidebar_position: 1
title: Desarrollo local
---

# Desarrollo local

DeviceChain está diseñado para ejecutarse localmente con solo dos dependencias: **NATS** y **TimescaleDB**. Sin Java, Kafka, ZooKeeper, Redis, Keycloak ni Mosquitto.

:::note Estado
DeviceChain es pre-release. Esta guía trata sobre trabajar en el árbol de código: compilar el
workspace de Go y ejecutar un único servicio contra dependencias que tú mismo has iniciado.
Si lo que quieres es una **instancia completa en ejecución**, usa `dcctl` y la
[Guía rápida](../quickstart/first-device.md); levanta todo con un solo comando.
:::

## Requisitos previos

- **Go** 1.26 o posterior — el workspace declara `go 1.26.6`, y CI compila con la versión que nombra `go.work`
- **Node** 22 o posterior (para el frontend y esta documentación; CI compila con 26)
- **Docker** (para ejecutar TimescaleDB)
- **nats-server** (un único binario de ~10 MB)

## 1. Iniciar la infraestructura

```bash
# TimescaleDB (PostgreSQL + TimescaleDB extension)
docker run -d --name dc-timescaledb \
  -p 5432:5432 \
  -e POSTGRES_PASSWORD=devicechain \
  timescale/timescaledb-ha:pg17
```

NATS necesita JetStream y —si quieres conectar un dispositivo por MQTT— la pasarela MQTT
integrada del broker. **No existe una opción de línea de comandos para MQTT**: es un bloque
de configuración, y requiere tanto JetStream como un nombre de servidor explícito. Así que
escribe un pequeño archivo de configuración en lugar de pasar banderas:

```bash
cat > nats.conf <<'EOF'
server_name: dc-local
jetstream: enabled
http_port: 8222
mqtt { port: 1883 }
EOF

nats-server -c nats.conf
```

El servidor registra `Listening for MQTT clients on mqtt://0.0.0.0:1883` cuando la pasarela
está activa. Si solo necesitas mensajería y JetStream, `nats-server -js -m 8222` es
suficiente —pero **no** abre ningún listener MQTT.

## 2. Compilar el workspace

El backend es un workspace de Go (`go.work`) que abarca la biblioteca principal (core), el
operador, la CLI y los servicios. Las compilaciones están **acotadas al módulo**: la raíz del
repositorio no es en sí un módulo de Go, por lo que un patrón `./...` anclado ahí no coincide
con nada —

```
pattern ./...: directory prefix . does not contain modules listed in go.work
```

— y ni `go build ./...` ni `go build ./backend/...` funcionan desde la raíz del árbol.
Compila desde dentro del módulo en el que estés trabajando, que es también lo que hace CI:

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
  ( cd "$m" && go build ./... && go vet ./... && go test ./... -count=1 ) \
    || { echo "FAILED: $m"; rc=1; }
done
echo "sweep exit status: $rc"
```

Dos detalles de ese bucle son esenciales. `-count=1` no es una precaución redundante: unas
pocas pruebas leen archivos **fuera** de su propio módulo, algo que la caché de pruebas de Go
no rastrea, así que un PASS en caché puede sobrevivir a un cambio que debería hacerlas
fallar. Y registrar `rc` importa porque `… || echo "FAILED: $m"` por sí solo dejaría el
estado de salida del bucle en el del último `echo`: todos los módulos podrían fallar y el
recorrido seguiría pareciendo correcto.

## 3. Ejecutar un servicio

Cada servicio es un único binario. La configuración se suministra mediante variables de entorno / configuración; consulta el paquete `config` de cada servicio para ver los ajustes disponibles.

```bash
go run ./backend/services/event-sources
```

(`go run` recibe la ruta de un solo paquete, así que se resuelve dentro de ese módulo y
funciona desde la raíz del repositorio —a diferencia de los patrones `./...` de arriba.)

## Estructura del repositorio

```
backend/
  core/                 shared library (lifecycle, NATS, GORM, GraphQL, config, auth, secrets)
  k8s/                  operator + CRD types
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
