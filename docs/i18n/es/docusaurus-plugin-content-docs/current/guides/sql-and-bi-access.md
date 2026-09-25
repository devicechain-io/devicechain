---
sidebar_position: 8
title: Acceso SQL y BI
---

# Acceso SQL y BI

DeviceChain almacena la telemetría en **TimescaleDB**, que es PostgreSQL. No tienes que sortear eso:
es la integración. Cualquier herramienta que hable Postgres o JDBC puede consultar tu telemetría
directamente: Metabase, Grafana, Power BI a través del controlador de PostgreSQL u ODBC, `psql`, un
notebook o tu propio proceso de informes. No hay paso de exportación, ni un segundo almacén de datos
que mantener sincronizado, ni un producto aparte que licenciar.

Esta guía configura la parte que no viene gratis: un **rol de lectura que se puede entregar con
seguridad**. Las credenciales de base de datos de la propia plataforma darían a una herramienta de BI
los datos de todos los inquilinos y acceso de escritura al almacén operativo. La superficie analítica
existe para que nunca tengas que entregarlas.

:::note Estado
Disponible. La superficie se crea en cada instalación; no tiene lectores hasta que declares uno.
:::

## Vistas analíticas {#what-a-reader-can-see}

Los lectores se conectan al almacén de eventos y consultan el **esquema `analytics`**. Contiene una
vista por cada relación de eventos, además de la agregación (rollup) de mediciones precalculada. La
agregación suele ser la que quieres: ya está agrupada en intervalos, así que es barata de recorrer
sobre rangos largos.

| Vista | Qué contiene |
| --- | --- |
| `analytics.events` | El sobre base del evento: dispositivo, tipo, tiempos, origen |
| `analytics.measurement_events` | Lecturas numéricas con nombre, con unidad y tipo de dato |
| `analytics.measurement_rollups` | Suma / mín. / máx. / recuento por minuto, por dispositivo y métrica |
| `analytics.location_events` | Posiciones — latitud, longitud, elevación, precisión, velocidad y rumbo. Requiere la concesión de posición; consulta [La posición es una concesión aparte](#position-is-a-separate-grant) |
| `analytics.alert_events` | Alertas reportadas por el dispositivo |
| `analytics.state_change_events` | La línea temporal de conexión/desconexión |
| `analytics.event_anchors` | Los anclajes de relación estampados en cada evento al escribirlo |

Cada vista está filtrada al **inquilino propio del lector**, automáticamente. No hay ninguna columna
de inquilino que recordar filtrar, ninguna vista que elegir por inquilino, y nada que una consulta
pueda hacer para ampliar el resultado. Consulta [Cómo se aplica el
límite](#how-the-boundary-is-enforced).

La agregación mantiene en vivo el intervalo actual, todavía en curso, de modo que un panel que la lea
no queda ciego hasta la última actualización.

## Declarar un lector {#declaring-a-reader}

Un lector es un rol de inicio de sesión de PostgreSQL llamado **`analytics_<id del inquilino>`**. El
inquilino se toma del nombre del rol, por lo que `analytics_acme` lee el inquilino `acme` y nada más.

:::danger El nombre del rol es el inquilino, y es el único sitio donde se escribe
Si llamas a un lector `analytics_acmecorp` cuando el id de tu inquilino es `acme`, no leerá
absolutamente nada: cada consulta devuelve cero filas, sin error. No hay un segundo sitio donde
corregir el error, ni ningún mensaje que lo señale. Comprueba el id del inquilino en la consola antes
de crear el rol. El id del inquilino debe tener 53 caracteres o menos; consulta [Límites del
nombre](#name-limits).
:::

### Límites del nombre {#name-limits}

El id del inquilino debe tener **53 caracteres o menos**. PostgreSQL limita el nombre de un rol a 63
bytes y *trunca* uno más largo en lugar de rechazarlo, lo que produciría en silencio un lector para
otro inquilino. Tu despliegue rechaza un nombre que se truncaría, así que obtienes un apply rechazado
en lugar de una sorpresa.

### Pasos {#steps}

1. **Crea un Secret de Kubernetes con la contraseña.** La plataforma nunca genera ni almacena esta
   credencial; es tuya, y la base de datos se reconcilia para coincidir con ella. El Secret va en el
   namespace propio de la instancia —`dci-` seguido del id de la instancia—, junto al almacén de
   eventos que lo lee. Ponle la etiqueta `cnpg.io/reload` (consulta [La etiqueta de
   recarga](#the-reload-label)):

   ```bash
   kubectl create secret generic analytics-acme-credentials \
     --namespace dci-<instance-id> \
     --type kubernetes.io/basic-auth \
     --from-literal=username=analytics_acme \
     --from-literal=password="$(openssl rand -base64 24)"

   kubectl label secret analytics-acme-credentials \
     --namespace dci-<instance-id> cnpg.io/reload=true
   ```

2. **Declara el rol en las variables de tu despliegue**, con un límite de conexiones:

   ```hcl
   timescale_analytics_readers = [
     {
       name             = "analytics_acme"
       connection_limit = 5
       password_secret  = "analytics-acme-credentials"
     },
   ]
   ```

3. **Aplica.** El rol aparece, se une al grupo de lectores y puede conectarse. No hay que reiniciar
   nada.

:::warning La etiqueta es lo que hace que la rotación funcione
Sin `cnpg.io/reload`, los cambios posteriores en el Secret no se detectan con prontitud, y la rotación
no ocurre en un tiempo previsible. Consulta [La etiqueta de recarga](#the-reload-label).
:::

### La etiqueta de recarga {#the-reload-label}

Sin `cnpg.io/reload`, la base de datos sí toma la contraseña cuando el rol se crea por primera vez.
Sin embargo, los **cambios** posteriores en el Secret no se detectan con prontitud. La etiqueta pide
al operador de la base de datos que vigile las actualizaciones del Secret, y el paso de rotación de
más abajo la necesita para surtir efecto en un tiempo previsible.

Dos detalles que suelen pillar desprevenido a más de uno:

- Lo que cuenta es la *presencia* de la etiqueta, así que cualquier valor sirve.
- El `username` del Secret debe coincidir **exactamente** con el nombre del rol, sin salto de línea
  final.

Cuando el operador de la base de datos no puede reconciliar un rol, indica el rol y la causa bajo el
`status.managedRolesStatus` del Cluster de la base de datos, no en los registros de la propia
DeviceChain. Mira allí antes de dar por hecho que la contraseña es incorrecta.

Si creaste el Secret antes de que se documentara el paso de etiquetado anterior, añade la etiqueta
ahora. Hasta que lo hagas, un cambio de contraseña puede quedar sin aplicar durante un tiempo
impredecible.

### Rotar o revocar {#rotate-or-revoke}

Para rotar la contraseña, cámbiala en el Secret. La base de datos se reconcilia para coincidir, sin
reinicios.

Para revocar el acceso:

1. Elimina la entrada de `timescale_analytics_readers` y aplica.
2. **Elimina el rol como superusuario:**

   ```sql
   DROP ROLE analytics_acme;
   ```

Quitar la entrada solo hace que tu despliegue deje de declarar el rol. El operador de la base de datos
deja en su sitio un rol que ya no gestiona, con su contraseña y su pertenencia al grupo de lectores
intactas. Eliminar el rol primero tampoco funciona: mientras siga declarado, el operador lo vuelve a
crear.

## La posición es una concesión aparte {#position-is-a-separate-grant}

Un lector declarado como arriba lee telemetría, alertas, la línea temporal de conexión/desconexión y
los sobres de los eventos, **pero no las posiciones de los dispositivos**. La latitud, la longitud, la
elevación, la precisión, la velocidad y el rumbo están detrás de una segunda concesión, que activas
por lector:

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

Aplica, y el lector podrá consultar `analytics.location_events`. Sin ello, esa vista concreta devuelve
`permission denied` y ninguna otra vista se ve afectada.

Esta es la propia línea de la plataforma, no una cautela añadida para BI. En todo DeviceChain, leer
**dónde está** un dispositivo es un permiso distinto de leer **qué mide**. El recorrido de un vehículo
o de una persona es un hecho de otra naturaleza que una serie de temperaturas, así que la posición
queda deliberadamente fuera del conjunto básico de solo lectura y solo se obtiene mediante una
concesión explícita.

A una sesión SQL no se le puede preguntar qué permisos tiene: se autentica como un rol y no lleva nada
más. Así que el permiso se expresa de la única forma en que una base de datos puede expresarlo: como
una concesión, sostenida por un segundo rol de grupo al que el lector se une cuando tú lo indicas.

Un lector ordinario conserva el **sobre**. `analytics.events` contiene todos los eventos, incluidos
los de ubicación, así que el lector sigue pudiendo ver que ocurrió un evento de ubicación, de qué
dispositivo y cuándo. No puede ver dónde.

:::tip Qué lectores lo necesitan
Los paneles de flotas, logística, servicio de campo y seguimiento de activos, sí. Un panel de métricas
o de alertas normalmente no, y un lector sin posición es una credencial menos cuya pérdida revela los
movimientos de alguien. Actívalo donde el panel realmente dibuje un mapa o calcule una distancia.
:::

:::note Actualizar una instalación existente
Los lectores declarados antes de que existiera esta separación tenían la posición. En el primer
reinicio de `event-management` tras la actualización, este retira esa concesión a todo lector no
marcado con `reads_location = true`, así que un panel que dibuje posiciones devuelve
`permission denied` hasta que lo configures. Es deliberado: la concesión se vuelve a derivar de tu
declaración cada vez que arranca `event-management`, en lugar de acumularse.
:::

## Conectar una herramienta de BI {#connecting-a-bi-tool}

Apunta la herramienta al almacén de eventos como a cualquier base de datos PostgreSQL:

| Ajuste | Valor |
| --- | --- |
| Host | el servicio del almacén de eventos (`dc-timescaledb-single` dentro del clúster) |
| Puerto | `5432` |
| Base de datos | el **id de tu instancia** |
| Esquema | `analytics` |
| Usuario | `analytics_<id del inquilino>` |
| Contraseña | la que pusiste en el Secret |

El nombre de la base de datos es el id de la instancia y no un nombre fijo, porque un servidor aloja
una base de datos por instancia.

Desde fuera del clúster, expón el almacén como expondrías cualquier otra base de datos: un
port-forward para algo puntual, o un ingress con TLS como es debido para una conexión permanente. Para
una comprobación rápida:

```bash
kubectl port-forward -n dci-<instance-id> svc/dc-timescaledb-single 5432:5432
psql "postgres://analytics_acme@localhost:5432/<instance-id>" \
  -c "SELECT device_token, name, bucket, sum_value / count_value AS avg
      FROM analytics.measurement_rollups
      WHERE bucket > now() - interval '1 hour'
      ORDER BY bucket DESC LIMIT 20;"
```

Ninguna de estas herramientas necesita un complemento de DeviceChain:

- **Grafana:** añade una fuente de datos **PostgreSQL** con esos ajustes.
- **Metabase:** añade una base de datos **PostgreSQL**.
- **Power BI:** usa **Obtener datos → Base de datos PostgreSQL**.

## Cómo se aplica el límite {#how-the-boundary-is-enforced}

Esta sección determina qué puedes hacer con seguridad con las credenciales de un lector.

### Filtro de inquilino {#tenant-filter}

**El filtro de inquilino está compilado dentro de las vistas y se basa en el rol autenticado.** Cada
vista lleva `WHERE tenant_id = <el inquilino del rol autenticado>`. Esa identidad es el rol con el que
inició sesión la sesión —el `session_user` de PostgreSQL— y un lector no puede cambiarla:

- `SET ROLE` cambia el rol *actual*, nunca el de la sesión.
- `SET SESSION AUTHORIZATION`, la única sentencia que sí lo haría, se rechaza a quien no sea
  superusuario.
- No existe ningún ajuste de sesión que lo sobrescriba.

Un rol cuyo nombre no lleva un inquilino reconocido no resuelve a nada y lee cero filas. El fallo es
siempre «no ve nada», nunca «lo ve todo».

### Sin privilegios sobre tablas {#no-table-privileges}

**Un lector no tiene ningún privilegio sobre las tablas subyacentes.** No puede alcanzar las
hypertables en bruto ni siquiera por su nombre. Esa es también la razón por la que es de solo lectura:
tiene `SELECT` sobre las vistas que se le concedieron y nada más, así que no hay ningún privilegio de
escritura que ejercer. Es una concesión, no un ajuste, así que ningún cliente puede desactivarla.

El mismo mecanismo separa la posición. Un lector sin `reads_location` no tiene ningún privilegio sobre
`analytics.location_events`, de modo que las coordenadas quedan inalcanzables en lugar de filtradas.

### Reparación en cada arranque {#repair-on-every-start}

**Ambas capas se restablecen cada vez que arranca el servicio `event-management`.** Es el servicio,
no la base de datos, quien lo hace, así que reiniciar solo la base de datos no repara nada. En cada
arranque, `event-management`:

- reconstruye la función que resuelve el inquilino de una sesión;
- verifica cada vista, y la reconstruye si falta, expone columnas incorrectas, ha perdido su predicado
  de inquilino o ha dejado de ser una barrera de seguridad;
- vuelve a converger los privilegios.

Así, ni un privilegio concedido a mano durante una investigación ni una vista editada durante una
sobreviven en silencio a ella. Un reinicio de `event-management` es una reparación.

Eso cubre la concesión de posición en todas las direcciones en que puede ampliarse. Un `GRANT` sobre
`analytics.location_events` hecho a un lector por su nombre, al grupo general de lectores o a `PUBLIC`
se retira en el siguiente arranque del servicio. Lo que tiene un lector se deriva de tu declaración
cada vez, nunca se acumula. Por eso también quitar `reads_location` retira el acceso de verdad, en
lugar de dejar en pie la última concesión.

### Límite de conexiones {#connection-cap}

**Las conexiones están limitadas por rol, y el límite obliga.** `connection_limit` se aplica en la
autenticación: superado, la conexión se rechaza. Esto impide que un consumidor analítico agote el
pool de conexiones de la propia plataforma. Ese fallo sería de otro modo silencioso, porque los pools
se abren de forma perezosa y la base de datos sigue reportando buena salud mientras la aplicación ya
no puede alcanzarla. Tu despliegue se niega a generar un lector sin límite, y rechaza un conjunto de
lectores cuyos límites no caben en el servidor.

:::warning El límite acota conexiones, no carga
El límite impide que la analítica se quede con las *conexiones* que la plataforma necesita. No impide
que las consultas sobre esas conexiones compitan por CPU, disco y el pool compartido de workers
paralelos de PostgreSQL. Así que «la analítica no puede interferir con la ingesta» es
**parcialmente** cierto. Consulta [Carga y réplicas de lectura](#load-and-read-replicas).
:::

### Carga y réplicas de lectura {#load-and-read-replicas}

Las consultas sobre las conexiones de un lector compiten por CPU, disco y el pool compartido de
workers paralelos de PostgreSQL, el mismo del que tiran la compresión, la retención y el refresco de
la agregación. La vía del agotamiento de conexiones está cerrada; la de la contención de recursos, no.

Si eso importa para tu carga de trabajo, ejecuta BI contra una **réplica de lectura**. Un despliegue
replicado ya expone un servicio de solo lectura junto al primario. Apuntar los lectores allí sitúa la
contención en un nodo cuyo único trabajo es atenderlos.

En la réplica, PostgreSQL resuelve un conflicto reteniendo la reproducción durante un tiempo acotado y
cancelando después la consulta analítica larga. El límite es de 30 segundos por defecto —su
`max_standby_streaming_delay`, que el despliegue deja en ese valor—, de modo que la réplica nunca se
queda atrás indefinidamente.

### El coste de la consulta no está limitado {#query-cost-is-not-capped}

No hay ningún límite de tiempo de consulta sobre un lector, y añadir uno no sería el control que
parece. El `statement_timeout` de PostgreSQL puede darse a un rol como **valor por defecto, no como
techo**: cualquier cliente lo sube para su propia sesión con una sola sentencia, y no hay forma de
impedirlo.

Establecer uno sigue mereciendo la pena como protección frente a un panel accidentalmente caro. Es una
operación de superusuario sobre la base de datos, no algo que la plataforma pueda hacer por ti:

```sql
ALTER ROLE analytics_acme SET statement_timeout = '60s';
```

El límite de conexiones es el control que realmente obliga. Dimensiónalo, y dimensiona el almacén,
suponiendo que cada una de esas conexiones puede estar ejecutando una consulta larga.

## Notas prácticas {#practical-notes}

- **Consulta la agregación, no la tabla en bruto, para cualquier rango largo.** Es una agregación
  continua: el trabajo ya está hecho. Recorrer un mes de ella es barato; recorrer un mes de mediciones
  en bruto no lo es.
- **Un inquilino tiene un solo rol de lector, y todas sus herramientas lo comparten.** El nombre del
  rol *es* el inquilino, así que `acme` tiene exactamente un nombre de lector legal, y un despliegue
  que declare un segundo es rechazado. Dos herramientas de un mismo inquilino comparten el límite de
  conexiones y la decisión sobre la posición, y no se pueden revocar por separado. Dimensiona
  `connection_limit` para todas ellas juntas, y activa `reads_location` si *alguna* de ellas necesita
  un mapa.
- **Un lector sobrevive a un cambio de esquema pero no gana nada de él automáticamente.** Las vistas
  exponen un conjunto fijo de columnas. Una columna añadida a la plataforma más tarde aparece en la
  superficie analítica cuando se añade allí deliberadamente, no antes.
- **Un lector ve algunos metadatos más allá de su propio inquilino.** Los catálogos de PostgreSQL son
  legibles por cualquier rol conectado. Un lector puede enumerar los nombres de los demás roles del
  servidor (y por tanto qué inquilinos tienen acceso BI), ver cuándo están activas esas sesiones y ver
  nombres internos de tablas y chunks. No puede leer ni una fila de todo eso. Si eso importa, da a cada
  cliente su propia instancia.
- **Eliminar un inquilino no elimina su rol de lector.** Al desmantelarlo, quita el rol de las
  variables de tu despliegue y después elimínalo como superusuario. La telemetría se borra, así que el
  rol no lee nada, pero un inicio de sesión que sigue existiendo es un inicio de sesión que alguien
  conserva. **Además, un id de inquilino puede reutilizarse, y el rol leería entonces los datos de su
  sucesor.** Eliminar el rol cierra ambas brechas; quitarlo solo de tu declaración lo deja en la base
  de datos, como se describe en [Declarar un lector](#declaring-a-reader).
