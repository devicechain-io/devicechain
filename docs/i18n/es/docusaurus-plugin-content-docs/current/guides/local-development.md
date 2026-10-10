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
GOWORK=off go mod tidy -diff   # no debe imprimir nada
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
    GOWORK=off go mod tidy -diff || { echo "not tidy, or tidiness could not be checked (see above)"; exit 1; }
    go build ./... && go vet ./... && go test ./... -count=1
  ) || { echo "FAILED: $m"; rc=1; }
done
echo "sweep exit status: $rc"
```

Cuatro detalles de ese bucle importan. Sin cualquiera de ellos, una comprobación pasaría sin mirar
nada:

- **La salida de `gofmt -l` se captura, no solo se ejecuta.** Termina con estado 0 *incluso cuando
  nombra archivos*, así que el bucle comprueba su salida con `[ -z "$fmt" ]`. Comprobar su estado de
  salida daría una barrera que nunca puede fallar.
- **`-count=1` no es opcional.** Unas pocas pruebas leen archivos fuera de su propio módulo. La
  caché de pruebas de Go no rastrea esos archivos, así que un PASS en caché puede sobrevivir a un
  cambio que debería hacerlo fallar.
- **Cada módulo se comprueba por separado para ver si está ordenado.** El workspace compila cada
  módulo a través de `go.work`, así que un `go.mod` al que le falta un requisito, o que todavía
  enumera uno que el código ya no usa, compila y pasa las pruebas sin errores.
  `GOWORK=off go mod tidy -diff` compara los archivos propios del módulo con lo que dejaría
  `go mod tidy` e imprime la diferencia. También falla, con el motivo, cuando no puede resolver el
  módulo en absoluto (sin conexión, por ejemplo), y por eso el mensaje del bucle nombra ambos casos.
  CI ejecuta la misma comprobación en cada módulo. Cuando imprima una diferencia, ejecuta
  `GOWORK=off go mod tidy` en ese módulo y confirma el resultado.
- **`rc` se registra, no solo se imprime.** Con `… || echo "FAILED: $m"` por sí solo, el estado de
  salida del bucle sería el del último `echo`. Todos los módulos podrían fallar y el recorrido
  seguiría pareciendo correcto.

La CI también ejecuta las pruebas de cada módulo con el detector de condiciones de carrera de Go.
Para ejecutar la misma comprobación en local sobre el módulo que cambiaste, desde la raíz del
repositorio:

```bash
hack/go-race.sh backend/services/event-processing   # o el módulo que hayas tocado
```

Imprime `race: COVERED <module>` y después ejecuta `go test -race -count=1 -timeout 20m ./...` en ese módulo. El
detector ralentiza las pruebas varias veces, así que una prueba que afirma un presupuesto de tiempo
de reloj puede fallar con él sin que haya ninguna condición de carrera. Corrige esa prueba para que
su presupuesto no dependa de lo rápido que se ejecute el binario.

**Las pruebas de dcctl nunca llegan a tu clúster.** Las pruebas de `backend/cli` parten de un
kubeconfig vacío, sea cual sea tu contexto actual. Todo paquete cuyas pruebas podrían tomar un
clúster del kubeconfig, de Helm o de OpenTofu tiene un `TestMain` que se ejecuta antes que cualquier
prueba. Apunta `KUBECONFIG` y la ruta de kubeconfig de las raíces de OpenTofu a un archivo vacío, y
borra las variables de ejecución dentro del clúster, `KUBE_*` y `HELM_KUBE*`. También elimina el
servidor por defecto al que Helm recurre con un kubeconfig vacío, `http://localhost:8080` o el que
indique `KUBERNETES_MASTER`. Así, una ejecución en tu máquina se comporta como en CI, y ninguna
prueba puede actuar sobre el clúster al que estás conectado. Una prueba que necesita un clúster
establece su propio `KUBECONFIG`. Un paquete nuevo en `backend/cli` cuyas pruebas puedan llegar a un
clúster necesita el mismo `TestMain`. Una prueba en `backend/cli/internal/kubeisolation` encuentra
esos paquetes a partir del grafo de importaciones y falla hasta que cada uno lo tenga.

**`backend/k8s` y `backend/cli` necesitan un servidor de API local para sus pruebas.** Algunas de
sus pruebas inician un `kube-apiserver` y un `etcd` reales como procesos locales (sin ningún
clúster): las reglas de validación del operador solo las aplica un servidor de API, y `backend/cli`
instala el chart en uno para cada perfil que renderiza `dcctl`, porque algunas reglas de la
especificación de pods son invisibles para un renderizado. Sin los binarios, esas pruebas fallan en
lugar de omitirse. Instálalos una vez y apunta las pruebas a ellos:

```bash
cd backend/k8s && make envtest
export KUBEBUILDER_ASSETS="$(bin/setup-envtest use "$(sed -n 's/^ENVTEST_K8S_VERSION[[:space:]]*=[[:space:]]*//p' Makefile)" -p path)"
```

En Windows, las pruebas de `backend/cli` que inician un servidor de API no se compilan: la
biblioteca de pruebas que lo inicia no compila allí en la versión que fija este repositorio. El
resto de las pruebas de ese paquete sigue compilando. Las pruebas de `backend/k8s/controllers` no
se compilan en Windows.

### Fuzzing

`go test` sin más solo ejecuta las entradas semilla de cada prueba de fuzzing. Para hacer fuzzing,
usa el script. Encuentra todas las pruebas de fuzzing incluidas en el repositorio (ignora los archivos
sin seguimiento) y ejecuta cada una durante un
tiempo fijo:

```bash
hack/fuzz.sh                         # todas las pruebas de fuzzing, 60 s cada una
FUZZTIME=300s hack/fuzz.sh           # más tiempo
hack/fuzz.sh --list                  # qué ejecutaría
hack/fuzz.sh --target backend/core/graphql FuzzRootFieldLimit   # solo una
```

Juzga cada ejecución por lo que informó, no solo por su código de salida. Cualquier resultado
distinto de `PASS` o `TOLERATED` es un fallo:

- `FINDING`: una entrada falló. Go la guarda en `testdata/fuzz/<Nombre>/` dentro del paquete, y el
  script la copia a su directorio de registros. Haz commit de ella ahí para convertirla en una
  prueba de regresión permanente.
- `NOT-RUN`: no hubo fuzzing. `go test -fuzz` sale con 0 cuando su patrón no coincide con ninguna
  prueba de fuzzing, así que el código de salida por sí solo lo daría por bueno.
- `SEED-FAIL`: una de las entradas semilla del repositorio falló antes de empezar el fuzzing.
- `HANG`: la ejecución no terminó dentro de su tiempo límite. `KILLED`: se terminó a la fuerza, ya
  sea por el límite de tiempo o porque el sistema se quedó sin memoria.
- `TOLERATED`: la ejecución hizo fuzzing durante todo su tiempo y no encontró nada, pero el motor
  de fuzzing de Go informó después `context deadline exceeded` como un fallo. Es una condición de
  carrera conocida en el coordinador de fuzzing de Go al final del tiempo límite, no un hallazgo.
  Solo se acepta esa línea exacta y única, y solo cuando la ejecución llegó a su tiempo completo;
  aun así se informa como advertencia.
- `FAILED`: cualquier otro caso, como un proceso de fuzzing que murió. El registro completo dice por
  qué.

Cada ejecución tiene su propio directorio temporal, y se conserva su registro completo. Fuera de CI
usa dos procesos de fuzzing, porque cada uno es un proceso aparte con su propia memoria; cambia eso
con `FUZZ_PARALLEL`. Las mismas ejecuciones se repiten cada noche sobre `main`, y sus registros se
guardan como artefacto del workflow.

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
                        outbound-connectors, ai-inference, mcp, update-management, y las
                        áreas de ingesta edge
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
