---
title: Modelo de dominio
---

# Modelo de dominio

DeviceChain modela el mundo físico con un pequeño conjunto de conceptos componibles. La decisión que lo define es que el *contexto* de un dispositivo se expresa como un **grafo de relaciones tipado** (typed relationship graph), no como un registro de asignación fijo. Eso mantiene el modelo abierto a nuevos tipos de entidad con el tiempo.

## Entidades principales

- **Device (Dispositivo)**: la cosa que se conecta e informa. Cada dispositivo es una instancia de un tipo de dispositivo.
- **Device Type (Tipo de dispositivo)**: la capa de taxonomía e identidad. Contiene el nombre de una clase de dispositivo, su apariencia (icono y colores) y su clasificación para agrupar y filtrar. Un tipo de dispositivo hace referencia como máximo a un perfil de dispositivo.
- **Device Profile (Perfil de dispositivo)**: el contrato de capacidades. Es un recurso distinto y versionado (borrador → publicar → revertir) que posee las definiciones de métricas, comandos y reglas de detección de una clase de dispositivo. Muchos tipos de dispositivo pueden compartir un mismo perfil, así que defines la configuración de capacidades una vez y la reutilizas. Un dispositivo resuelve sus capacidades a través de `device → type → profile`. Un tipo de dispositivo sin perfil es válido: clasifica y muestra sus dispositivos, pero no otorga ningún contrato de capacidades tipado.
- **Asset (Activo)**: la cosa del mundo real que un dispositivo monitorea, clasificada por un **tipo de activo** definido por el inquilino. Un tipo de activo puede publicar un esquema de propiedades versionado, y las propiedades de sus activos se validan contra él. Los activos forman una jerarquía padre/hijo, con un solo padre por activo y sin ciclos.
- **Area (Área)**: una ubicación espacial u organizacional con nombre, clasificada por un **tipo de área**. Los límites espaciales se modelan por separado como [geocercas](./geofencing.md), no como campos del área.
- **Customer (Cliente)**: un propietario organizacional, clasificado por un **tipo de cliente**.
- **Groups (Grupos)**: un único **grupo de entidades** uniforme reúne miembros de una sola familia (dispositivos, activos, áreas o clientes), elegida al crear el grupo. La membresía es estática, una lista explícita de miembros, o dinámica, un selector guardado sobre los atributos de los miembros que se resuelve en el momento de la lectura (consulta [Facetas y grupos dinámicos](#facets-and-dynamic-groups)).

Los dispositivos, activos, áreas, clientes y grupos se direccionan todos de la misma manera: mediante un tipo de entidad más un token estable por inquilino. Esa dirección uniforme es lo que permite que las relaciones, los grupos y la indexación de eventos funcionen de forma genérica sobre todos ellos. Los tipos de dispositivo y los perfiles de dispositivo no son entidades direccionables en este sentido; son configuración a través de la cual se resuelve un dispositivo.

## Relaciones {#relationships}

DeviceChain no vincula un dispositivo a una única asignación fija `(customer, area, asset)`. En su lugar, conecta las entidades con relaciones tipadas y dirigidas:

- Una relación tiene un **origen** (source), un **destino** (target) y un **tipo de relación**.
- Un tipo de relación lleva un indicador **`Tracked`**.

El indicador `Tracked` es central. Cuando un dispositivo informa un evento, la plataforma registra cada una de las relaciones rastreadas del dispositivo como un **anclaje** (anchor) en ese evento. Un anclaje es una entrada `(anchor_type, anchor_token)` en el conjunto de anclajes del evento: el tipo de entidad del destino y su token estable por inquilino.

Un dispositivo puede tener varias relaciones rastreadas, por ejemplo un cliente *y* un área *y* un activo. La lectura se puede consultar entonces por cada una de ellas: "cada lectura de temperatura del Edificio 7" y "…del cliente Acme" la encuentran las dos. Los anclajes se capturan en el momento de la escritura, así que el historial permanece intacto cuando más tarde reasignas el dispositivo.

**La asignación organiza; no bloquea.** Un dispositivo que tiene credenciales pero aún no está asignado sigue informando telemetría. Sus eventos se resuelven con un conjunto de anclajes vacío en lugar de descartarse. Cuando asignas el dispositivo más tarde, sus eventos posteriores reciben un anclaje de cliente/área/activo. Consulta [Gestión de asignaciones de dispositivos](../guides/managing-assignments.md).

## Atributos y eventos {#attributes-vs-events}

DeviceChain separa el estado actual del historial:

- Los **eventos** son el registro de series temporales de solo anexado (append-only) de todo lo que informa un dispositivo: mediciones, ubicaciones, alertas, invocaciones y respuestas de comandos, y cambios de estado. Residen en hypertables de TimescaleDB.
- Los **atributos** son el estado actual de clave-valor de una entidad, en tres ámbitos:
  - `CLIENT`: informado por el dispositivo.
  - `SERVER`: metadatos exclusivos de la plataforma que el dispositivo nunca ve.
  - `SHARED`: definido por la plataforma y legible por el dispositivo. Es el canal para la configuración remota y los destinos OTA.

## Facetas y grupos dinámicos {#facets-and-dynamic-groups}

Los atributos también funcionan como **facetas de clasificación**, los ejes por los que navegas y filtras las entidades. Un **registro de facetas** por inquilino declara qué claves de atributo son facetas para una familia de entidades dada. Eso da a la interfaz de navegación de la consola sus ejes y el autocompletado de valores. El registro declara solo *qué claves* son facetas; los valores permanecen como atributos en las propias entidades.

Un **grupo dinámico** convierte un filtro de facetas en una membresía guardada que se actualiza sola. Su selector es una expresión booleana sobre los atributos de los miembros, por ejemplo `attr["climate"] == "arid" && attr["country"] == "US"`. La escribes en [CEL](https://github.com/google/cel-go), el mismo lenguaje de expresiones que usa el motor de detección.

La plataforma valida el selector y limita su costo cuando guardas el grupo. Después resuelve la membresía en el momento de la lectura traduciendo la expresión a una consulta de base de datos indexada, nunca escaneando cada entidad. Por eso un grupo dinámico siempre refleja el estado actual de los atributos, sin ninguna caché materializada que mantener sincronizada. Un grupo estático, en cambio, mantiene una lista explícita de miembros.

En la consola, la pantalla **Explorar** compone un selector a partir de los ejes de facetas, previsualiza en vivo el recuento de coincidencias y lo guarda como un grupo dinámico. La pantalla **Facetas** administra el registro.

## Comandos

Un perfil de dispositivo declara los comandos que aceptan sus dispositivos. Un comando emitido se persiste y se rastrea a lo largo de un ciclo de vida, en lugar de enviarse y olvidarse. [Comandos](./commands.md) cubre ambos aspectos.

## Identidad y credenciales

Un dispositivo tiene una **identidad** estable a la que hace referencia todo lo demás, separada de sus **credenciales**, el material que usa para autenticarse. Las credenciales son conectables (pluggable): token de acceso, MQTT-basic (usuario + contraseña) y certificado X.509. Así, un dispositivo puede rotar credenciales o tener varias sin cambiar su identidad.

El secreto de una credencial es de **solo escritura**: lo envías cuando registras la credencial y nunca se devuelve en una lectura. Eso cubre solo la contraseña de MQTT-basic. Para un token de acceso o un certificado, el id de la credencial es en sí mismo la prueba de posesión, así que leer las credenciales de un dispositivo requiere la autoridad `device:write` y no `device:read`. Consulta [Credenciales de dispositivo](../guides/device-credentials.md#reading-a-credential).

Un dispositivo también puede llevar un **`externalId`** opcional: una clave de negocio propiedad del cliente, como un VIN, un número de serie, un código GS1 o una etiqueta de activo. Es distinto tanto de la identidad interna como de la credencial. Es:

- opaco, sin restricciones de formato,
- único dentro de un inquilino cuando está presente, y
- nunca se usa para direccionamiento ni autenticación.

Su propósito es la búsqueda y la integración: hacer coincidir un dispositivo de DeviceChain con el identificador que tus otros sistemas ya usan para esa misma cosa física.

## Eventos

Cada evento registra:

- el dispositivo que lo informa,
- el tipo de evento,
- las marcas de tiempo informada por el dispositivo y recibida por la plataforma,
- un id de correlación externo opcional (`altId`) para una ingesta idempotente, y
- el conjunto de anclajes de relación resueltos descrito arriba: cero o más filas `(anchor_type, anchor_token)`. El conjunto está vacío cuando el dispositivo no está asignado.

Las categorías de eventos incluyen mediciones, ubicaciones, alertas, invocaciones y respuestas de comandos, y cambios de estado.

Las mediciones son **autodescriptivas**. Cuando una lectura coincide con una métrica definida en el perfil del dispositivo, la plataforma estampa la unidad y el tipo de dato de esa métrica directamente sobre la lectura persistida y sobre la proyección en vivo del último estado conocido. Un consumidor que lee una medición obtiene su semántica (`22.4 °C`, un `DOUBLE`) sin una segunda consulta al perfil.
