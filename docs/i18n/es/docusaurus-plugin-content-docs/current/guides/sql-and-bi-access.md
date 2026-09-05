---
sidebar_position: 8
title: Acceso SQL y BI
---

# Acceso SQL y BI

DeviceChain almacena la telemetría en **TimescaleDB**, que es PostgreSQL. Eso no es un detalle de
implementación que haya que sortear — es la integración. Cualquier herramienta que hable Postgres o
JDBC puede consultar su telemetría directamente: Metabase, Grafana, Power BI a través del
controlador de PostgreSQL u ODBC, `psql`, un cuaderno de notas, su propio proceso de informes. No
hay paso de exportación, ni un segundo almacén de datos que mantener sincronizado, ni un producto
aparte que licenciar.

Lo que esta guía configura es la parte que no viene gratis: un **rol de solo lectura que se puede
entregar con seguridad**. Apuntar una herramienta de BI a las credenciales de base de datos de la
propia plataforma le daría los datos de todos los inquilinos y acceso de escritura al almacén
operativo. La superficie analítica existe para que nunca tenga que hacerlo.

:::note Estado
Disponible. La superficie se crea en cada instalación; no tiene lectores hasta que usted declare
uno.
:::

## Qué puede ver un lector

Los lectores se conectan al almacén de eventos y consultan el **esquema `analytics`**. Contiene una
vista por cada relación de eventos, además de la agregación de mediciones precalculada — que suele
ser la que realmente quiere, porque ya está agrupada en intervalos y es barata de recorrer sobre
rangos largos.

| Vista | Qué contiene |
| --- | --- |
| `analytics.events` | El sobre base del evento: dispositivo, tipo, tiempos, origen |
| `analytics.measurement_events` | Lecturas numéricas con nombre, con unidad y tipo de dato |
| `analytics.measurement_rollups` | Suma / mín. / máx. / recuento por minuto, por dispositivo y métrica |
| `analytics.location_events` | **Posiciones** — latitud, longitud, elevación, precisión, velocidad y rumbo. Requiere la concesión de posición; véase [La posición es una concesión aparte](#la-posicion-es-una-concesion-aparte) |
| `analytics.alert_events` | Alertas reportadas por el dispositivo |
| `analytics.state_change_events` | La línea temporal de conexión/desconexión |
| `analytics.event_anchors` | Los anclajes de relación estampados en cada evento al escribirlo |

Todas ellas están filtradas al **inquilino propio del lector**, automáticamente. No hay ninguna
columna de inquilino que recordar filtrar, ninguna vista que elegir por inquilino, y nada que una
consulta pueda hacer para ampliar el resultado — véase [Cómo se aplica el
límite](#como-se-aplica-el-limite) más abajo.

La agregación mantiene en vivo el intervalo actual, todavía en curso, de modo que un panel que la
lea no queda ciego hasta la última actualización.

## Declarar un lector

Un lector es un rol de inicio de sesión de PostgreSQL llamado **`analytics_<id del inquilino>`**. El
inquilino se toma del nombre del rol, por lo que `analytics_acme` lee el inquilino `acme` y nada
más.

:::danger El nombre del rol es el inquilino, y es el único sitio donde se escribe
Llame a un lector `analytics_acme` y leerá `acme`. Llámelo `analytics_acmecorp` cuando el id de su
inquilino es `acme` y no leerá absolutamente nada — cada consulta devuelve cero filas, sin error.
No hay un segundo sitio donde corregir el error, ni ningún mensaje que lo señale. Compruebe el id
del inquilino en la consola antes de crear el rol.

**El id del inquilino debe tener 53 caracteres o menos.** PostgreSQL limita el nombre de un rol a
63 bytes y *trunca* los más largos en lugar de rechazarlos, lo que produciría en silencio un lector
para otro inquilino. Su despliegue rechaza un nombre que se truncaría, así que esto es un apply
rechazado y no una sorpresa — pero es la razón por la que existe el límite.
:::

**1. Cree un Secret de Kubernetes con la contraseña.** La plataforma nunca genera ni almacena esta
credencial; es suya, y la base de datos se reconcilia para coincidir con ella.

```bash
kubectl create secret generic analytics-acme-credentials \
  --namespace dc-system \
  --type kubernetes.io/basic-auth \
  --from-literal=username=analytics_acme \
  --from-literal=password="$(openssl rand -base64 24)"
```

**2. Declare el rol en las variables de su despliegue**, con un límite de conexiones:

```hcl
timescale_analytics_readers = [
  {
    name             = "analytics_acme"
    connection_limit = 5
    password_secret  = "analytics-acme-credentials"
  },
]
```

Aplique. El rol aparece, se une al grupo de lectores y puede conectarse. No hay que reiniciar nada.

Para rotar la contraseña, cámbiela en el Secret; para revocar el acceso, elimine la entrada y
aplique.

## La posición es una concesión aparte {#la-posicion-es-una-concesion-aparte}

Un lector declarado como arriba lee telemetría, alertas, la línea temporal de conexión/desconexión y
los sobres de los eventos — **pero no las posiciones de los dispositivos**. La latitud, la longitud,
la elevación, la precisión, la velocidad y el rumbo están detrás de una segunda concesión, y usted la
activa por lector:

```hcl
timescale_analytics_readers = [
  {
    name             = "analytics_acme"
    connection_limit = 5
    password_secret  = "analytics-acme-credentials"
    reads_location   = true
  },
]
```

Aplique, y el lector podrá consultar `analytics.location_events`. Sin ello, esa vista concreta
devuelve `permission denied` y ninguna otra vista se ve afectada.

Esto no es una cautela añadida para BI; es la propia línea de la plataforma. Leer **dónde está** un
dispositivo es un permiso distinto de leer **qué mide** en todo DeviceChain — el recorrido de un
vehículo o de una persona es un hecho de otra naturaleza que una serie de temperaturas, así que la
posición queda deliberadamente fuera del conjunto básico de solo lectura y solo se tiene por
concesión explícita. A una sesión SQL no se le puede preguntar qué permisos tiene; se autentica como
un rol y no lleva nada más. De modo que el permiso se expresa de la única forma en que una base de
datos puede expresarlo — como una concesión, sostenida por un segundo rol de grupo al que el lector
se une cuando usted lo indica.

Lo que un lector ordinario conserva es el **sobre**: `analytics.events` contiene todos los eventos,
incluidos los de ubicación, así que sigue pudiendo ver que ocurrió un evento de ubicación, de qué
dispositivo y cuándo. Simplemente no puede ver dónde.

:::tip Qué lectores lo necesitan
Los paneles de flotas, logística, servicio de campo y seguimiento de activos, sí. Un panel de
métricas o de alertas normalmente no — y un lector que no necesita la posición es una credencial
menos cuya pérdida revela los movimientos de alguien. Actívelo donde el panel realmente dibuje un
mapa o calcule una distancia.
:::

:::note Actualizar una instalación existente
Si declaró lectores antes de que existiera esta separación, tenían la posición. En el primer
reinicio tras la actualización, el almacén de eventos retira esa concesión a todo lector que usted no
haya marcado con `reads_location = true` — un panel que dibuje posiciones empezará a devolver
`permission denied` hasta que lo configure. Esa convergencia es deliberada: la concesión se vuelve a
derivar de su declaración en cada arranque, en lugar de acumularse.
:::

## Conectar una herramienta de BI

Apunte la herramienta al almacén de eventos como a cualquier base de datos PostgreSQL:

| Ajuste | Valor |
| --- | --- |
| Host | el servicio del almacén de eventos (`dc-timescaledb-single` dentro del clúster) |
| Puerto | `5432` |
| Base de datos | el **id de su instancia** |
| Esquema | `analytics` |
| Usuario | `analytics_<id del inquilino>` |
| Contraseña | la que puso en el Secret |

El nombre de la base de datos es el id de la instancia y no un nombre fijo, porque un servidor aloja
una base de datos por instancia.

Desde fuera del clúster, exponga el almacén como expondría cualquier otra base de datos — un
port-forward para algo puntual, o un ingress con TLS para una conexión permanente. Para una
comprobación rápida:

```bash
kubectl port-forward -n dc-system svc/dc-timescaledb-single 5432:5432
psql "postgres://analytics_acme@localhost:5432/<id-de-instancia>" \
  -c "SELECT device_token, name, bucket, sum_value / count_value AS avg
      FROM analytics.measurement_rollups
      WHERE bucket > now() - interval '1 hour'
      ORDER BY bucket DESC LIMIT 20;"
```

En Grafana, añada una fuente de datos **PostgreSQL** con esos ajustes. En Metabase, añada una base
de datos **PostgreSQL**. En Power BI, use **Obtener datos → Base de datos PostgreSQL**. Ninguna de
ellas necesita un complemento de DeviceChain.

## Cómo se aplica el límite {#como-se-aplica-el-limite}

Conviene entenderlo, porque determina qué puede hacer con seguridad con las credenciales de un
lector.

**El filtro de inquilino está compilado dentro de las vistas y se basa en el rol autenticado.**
Cada vista lleva `WHERE tenant_id = <el inquilino del rol autenticado>`. Esa identidad es el rol con
el que inició sesión — el `session_user` de PostgreSQL — y un lector no puede cambiarla: `SET ROLE`
cambia el rol *actual* pero nunca el de la sesión, y la única sentencia que sí lo haría (`SET
SESSION AUTHORIZATION`) se rechaza a quien no sea superusuario. Tampoco existe ningún ajuste de
sesión que lo sobreescriba. Un rol cuyo nombre no lleva un inquilino reconocido no resuelve a nada y
lee cero filas — la dirección del fallo es siempre «no ve nada», nunca «lo ve todo».

**Un lector no tiene ningún privilegio sobre las tablas subyacentes.** No puede alcanzar las
hipertablas en bruto ni siquiera por su nombre, que es también la razón por la que es de solo
lectura: tiene `SELECT` sobre las vistas que se le concedieron y nada más, así que no hay ningún
privilegio de escritura que ejercer. Eso es una concesión de permisos, no un ajuste — nada que un
cliente pueda desactivar. Es también como se separa la posición: un lector sin `reads_location` no
tiene ningún privilegio sobre `analytics.location_events`, de modo que las coordenadas quedan
inalcanzables en lugar de filtradas.

**Ambas capas se restablecen cada vez que arranca el almacén de eventos.** Las vistas se
reconstruyen y los privilegios se vuelven a converger en cada arranque, de modo que ni un permiso
concedido a mano durante una investigación ni una vista editada durante una le sobreviven en
silencio. Un reinicio es una reparación.

Eso cubre en concreto la concesión de posición, y en todas las direcciones en que puede ampliarse:
un `GRANT` sobre `analytics.location_events` hecho a un lector por su nombre, al grupo general de
lectores o a `PUBLIC` se retira en el siguiente arranque. Lo que un lector acaba teniendo se deriva
de su declaración cada vez, nunca se acumula — que es también por qué quitar `reads_location` retira
el acceso de verdad, en lugar de dejar en pie la última concesión.

**Las conexiones están limitadas por rol, y el límite obliga.** `connection_limit` se aplica en la
autenticación: superado, la conexión se rechaza. Esto es lo que impide que un consumidor analítico
agote el pool de conexiones de la propia plataforma — un fallo que de otro modo sería silencioso,
porque los pools se abren de forma perezosa y la base de datos sigue reportando buena salud mientras
la aplicación ya no puede alcanzarla. Su despliegue se niega a generar un lector sin límite, y se
niega a generar un conjunto de lectores cuyos límites no caben en el servidor.

:::caution El límite acota conexiones, no carga
Un límite de conexiones impide que un consumidor analítico se quede con las *conexiones* que la
plataforma necesita. No impide que las consultas sobre esas conexiones compitan por CPU, disco y el
pool compartido de workers paralelos de PostgreSQL — el mismo del que tiran la compresión, la
retención y el refresco de la agregación. Así que «la analítica no puede interferir con la ingesta»
es **parcialmente** cierto: la vía del agotamiento de conexiones está cerrada y la de la contención
de recursos no.

Si eso importa para su carga de trabajo, ejecute BI contra una **réplica de lectura**. Un despliegue
replicado ya expone un servicio de solo lectura junto al primario; apuntar los lectores allí sitúa
la contención en un nodo cuyo único trabajo es atenderlos, y PostgreSQL resuelve allí un conflicto
cancelando la consulta analítica larga en lugar de retrasando la réplica.
:::

:::caution Lo que *no* está limitado: el coste de la consulta
No hay ningún límite de tiempo de consulta sobre un lector, y añadir uno no sería el control que
parece. El `statement_timeout` de PostgreSQL puede darse a un rol como **valor por defecto, no como
techo** — cualquier cliente lo sube para su propia sesión con una sola sentencia, y no hay forma de
impedirlo. Establecer uno sigue mereciendo la pena como protección frente a un panel
accidentalmente caro, y es una operación de superusuario sobre la base de datos, no algo que la
plataforma pueda hacer por usted:

```sql
ALTER ROLE analytics_acme SET statement_timeout = '60s';
```

El límite de conexiones es el control que realmente obliga. Dimensiónelo, y dimensione el almacén,
suponiendo que cada una de esas conexiones puede estar ejecutando una consulta larga.
:::

## Notas prácticas

- **Consulte la agregación, no la tabla en bruto, para cualquier rango largo.** Es una agregación
  continua: el trabajo ya está hecho, y recorrer un mes de ella es barato mientras que recorrer un
  mes de mediciones en bruto no lo es.
- **Cada inquilino tiene un rol de lector, y todas sus herramientas lo comparten.** El nombre del
  rol *es* el inquilino, así que `acme` tiene exactamente un nombre de lector legal y no hay un
  segundo que declarar — un despliegue que lo intente es rechazado. Cuente con ello: dos
  herramientas de un mismo inquilino comparten el límite de conexiones, comparten la decisión sobre
  la posición y no se pueden revocar por separado. Dimensione `connection_limit` para todas ellas
  juntas, y active `reads_location` si *alguna* de ellas necesita un mapa.
- **Un lector sobrevive a un cambio de esquema pero no gana nada de él automáticamente.** Las vistas
  exponen un conjunto fijo de columnas; una columna añadida a la plataforma más tarde aparece en la
  superficie analítica cuando se añade allí deliberadamente, no antes.
- **Un lector ve algunos metadatos más allá de su propio inquilino.** Los catálogos de PostgreSQL
  son legibles por cualquier rol conectado, así que un lector puede enumerar los nombres de los
  demás roles del servidor (y por tanto qué inquilinos tienen acceso BI), ver cuándo están activas
  esas sesiones y ver nombres internos de tablas y chunks. No puede leer ni una fila de todo eso. Si
  eso importa, dé a cada cliente su propia instancia.
- **Eliminar un inquilino no elimina su rol de lector.** Quite el rol de las variables de su
  despliegue como parte del desmantelamiento. La telemetría se borra, así que el rol no lee nada —
  pero un inicio de sesión que sigue existiendo es un inicio de sesión que alguien conserva, **y un
  id de inquilino puede reutilizarse, en cuyo caso ese rol leería los datos de su sucesor.** Quitar
  el rol es el paso que cierra ambas cosas.
