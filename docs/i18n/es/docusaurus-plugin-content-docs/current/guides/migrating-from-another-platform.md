---
title: Pasar a DeviceChain desde otra plataforma IoT
---

# Pasar a DeviceChain desde otra plataforma IoT

Esta guía es para un equipo que opera una flota en otra plataforma IoT y quiere saber qué implica pasar a DeviceChain. Relaciona los conceptos que ya usas con sus equivalentes en DeviceChain, enumera las vías de migración que existen hoy, dice con claridad qué no tiene equivalente directo y propone un orden de trabajo.

No existe un conversor de un solo paso. DeviceChain no lee los archivos de exportación de otra plataforma, de modo que una migración consiste en reconstruir tu modelo en DeviceChain y mover tus dispositivos. Lo que más pesa, los dispositivos y sus credenciales, se puede cargar en bloque. Lo que es lógica, como las reglas y los paneles, se reconstruye a mano. Esta guía no nombra otros productos, porque la correspondencia depende de los conceptos y no del producto.

## Cómo se corresponden los conceptos {#concept-map}

Las plataformas usan palabras distintas para cosas parecidas, y la misma palabra para cosas distintas. Lee la columna derecha como «el concepto más cercano de DeviceChain» y lee las notas donde la correspondencia es parcial.

| En tu plataforma actual | En DeviceChain | Notas |
| --- | --- | --- |
| **Dispositivo** | [Dispositivo](../concepts/domain-model.md#entidades-principales), identificado por un **token** que eliges tú | El token es la identidad y no cambia. Tu nombre o número de serie anterior puede ir en `externalId` (único por tenant) y tu identificador anterior en un [alias](../user-guide/devices.md#aliases). |
| **Perfil o tipo de dispositivo** | Un [tipo de dispositivo](../user-guide/device-types-and-profiles.md) que referencia un [perfil de dispositivo](../user-guide/device-types-and-profiles.md) versionado | DeviceChain divide la idea en dos. El **tipo** clasifica y muestra los dispositivos. El **perfil** es el contrato versionado: contiene las definiciones de métricas, los comandos, las reglas de detección y las claves de configuración. Varios tipos pueden compartir un perfil. |
| **Clave de telemetría** | Una **métrica** declarada en el perfil, con tipo de dato y unidad | Las lecturas llegan como `measurements` con el nombre de la métrica como clave. Una métrica es `DOUBLE`, `INT` o `BOOLEAN` y se almacena como serie temporal (un booleano se almacena como 0 o 1). Consulta las [formas del payload](./connecting-a-device.md#payload-shapes) y [Measurement](./connecting-a-device.md#measurement). |
| **Atributos** (del cliente, del servidor, compartidos) | [Atributos](../concepts/domain-model.md#attributes-vs-events) con los ámbitos `CLIENT`, `SERVER` y `SHARED` | Los ámbitos significan lo mismo: lo informa el dispositivo, solo de la plataforma, y fijado por la plataforma para el dispositivo. Los atributos compartidos que un perfil declara como configuración se entregan al dispositivo; consulta la [entrega de configuración](./connecting-a-device.md#reserved-device-topics). Los atributos también sirven como [facetas](../user-guide/groups-facets-browse.md) para explorar y agrupar. |
| **Activo** | [Activo](../user-guide/assets-customers-areas.md), clasificado por un tipo de activo | Un tipo de activo puede publicar un [contrato de propiedades](../user-guide/assets-customers-areas.md#asset-types) versionado. Los activos se anidan, con un solo padre cada uno. |
| **Relaciones** entre entidades | [Relaciones tipadas y dirigidas](../concepts/domain-model.md#relationships) | Un tipo de relación puede marcarse como **tracked** (con seguimiento). Una relación con seguimiento desde un dispositivo se anota en cada evento como ancla, de modo que las lecturas se pueden consultar por cliente, área o activo y conservan ese historial aunque reasignes el dispositivo más tarde. La pertenencia a grupos usa el mismo grafo. Consulta [gestionar asignaciones](./managing-assignments.md). |
| **Cliente** | [Cliente](../user-guide/assets-customers-areas.md), más **usuarios de cliente** cuya membresía se limita a los datos de ese cliente | Un cliente es un registro de tu tenant. Dar a una persona acceso limitado a él es algo aparte; consulta los [usuarios de cliente](../user-guide/members-roles-and-customer-users.md#customer-users). Los clientes no se anidan. |
| **Tenant**, perfil de tenant | [Tenant](../concepts/multi-tenancy.md) y [nivel de tenant](../concepts/tenant-tiers.md) | Una instalación atiende a muchos tenants, cada uno aislado de los demás. Un nivel es el paquete definido por el operador que fija los límites de un tenant. |
| **Usuarios y roles** | Miembros, [roles](../user-guide/members-roles-and-customer-users.md#roles) e [inicio de sesión único](../concepts/single-sign-on.md) | El inicio de sesión único usa OpenID Connect. Para scripts, usa una [cuenta de servicio](../user-guide/service-accounts.md). |
| **Grupo de dispositivos** | [Grupo de entidades](../user-guide/groups-facets-browse.md), estático o dinámico | Un grupo dinámico es un selector guardado sobre atributos, que se resuelve al leer. Un grupo contiene una sola familia de miembros: dispositivos, activos, áreas o clientes. |
| **Cadena de reglas, flujo o workflow** | [Reglas de detección](../concepts/event-processing.md) en el perfil, creadas con un [formulario o en el lienzo](../user-guide/detection-rules.md) | Una regla observa eventos y, cuando se cumple su condición, actúa. Las condiciones y lo que no pueden hacer están en [lo que no tiene equivalente directo](#no-equivalent). |
| **Alarma** | [Alarma](../user-guide/alarms.md) con ciclo de vida, severidad y escalado | La levantan y la despejan las reglas de detección. Un dispositivo también puede informar un evento de alerta. |
| **Notificación o entrega de alertas** | [Políticas de notificación](../user-guide/notification-policies.md) | Política por tenant, con entrega por correo y webhook, y escalado. |
| **Llamadas a otros sistemas** | [Conectores de salida](../concepts/outbound-connectors.md) y la acción de webhook | Una acción de regla puede llamar a un webhook o publicar en MQTT, Kafka, AWS SNS o AWS SQS. |
| **Telemetría derivada o calculada** | [Métricas calculadas](../user-guide/calculated-metrics.md) | Se almacenan como una métrica propia y se pueden rellenar sobre el historial. |
| **RPC a un dispositivo** | [Comandos](../concepts/commands.md) | Un comando se declara en el perfil con parámetros tipados, se valida al emitirlo, se conserva hasta que el dispositivo es alcanzable y se sigue hasta que el dispositivo informa un resultado o vence su tiempo de vida. Consulta [enviar comandos](./sending-commands.md) y [comandar una flota](./commanding-a-fleet.md). |
| **Actualización de firmware o software** | [Actualizaciones por aire](../concepts/over-the-air-updates.md): artefactos, firmantes, asignaciones y despliegues por grupo | El lado del dispositivo es un protocolo pequeño que implementas tú; consulta [construir un dispositivo actualizable](./building-an-updatable-device.md). |
| **Panel** | [Panel](../concepts/dashboards.md), un recurso versionado del tenant (borrador, publicar, revertir) | Se reconstruye, no se importa; consulta [paneles](#dashboards). |
| **Credenciales de dispositivo** (token de acceso, básica, certificado) | [Credenciales](./device-credentials.md): `ACCESS_TOKEN`, `MQTT_BASIC` y [X.509](./device-x509.md) | Detalles en [credenciales y conexión de dispositivos](#connecting-devices). |
| **Gateway** | Un [dispositivo gateway](../deployment/edge-services.md#gateway-identity) que habla por los dispositivos que se le asignan | También se puede usar con el agente de borde. |
| **Integración con un servidor de red LoRaWAN** | [Integración de entrada](./inbound-integrations.md) | Un webhook que asocia los uplinks del servidor de red con dispositivos. |

## Vías de migración que existen hoy {#paths}

Esto es lo que puedes hacer ahora. Cada punto es una función documentada, no un plan.

### Construir tu modelo mediante la API {#model}

Antes de que los dispositivos puedan informar, DeviceChain necesita el modelo en el que informan: tipos y perfiles de dispositivo, tipos de activo y de cliente, activos, clientes y las relaciones entre ellos. No hay un archivo de importación para esto. Créalos en la consola o automatízalos con la [API GraphQL](../reference/graphql-api.md), que tiene una mutación para cada uno (`createDeviceProfile`, `createDeviceType`, `createAsset`, `createCustomer`, `createEntityRelationships`, etc.). La mutación de relaciones admite hasta 1000 relaciones en una llamada.

Un script se autentica con una [cuenta de servicio](../user-guide/service-accounts.md#obtaining-a-token): una cuenta no humana de tu tenant que presenta un ID de cliente y un secreto y recibe un token de corta duración. Dale un rol que conceda solo lo que el script necesita.

El perfil es donde va la mayor parte del esfuerzo, porque es el contrato. Decide una vez la clave, el tipo y la unidad de cada métrica, publica el perfil y luego incorpora los dispositivos. Una versión publicada queda congelada; un cambio posterior es una versión nueva. Consulta [tipos y perfiles de dispositivo](../user-guide/device-types-and-profiles.md).

### Cargar dispositivos desde un archivo CSV {#csv}

[Importar dispositivos desde CSV](./importing-devices.md) es la vía en bloque para un inventario. Está en la consola (**Dispositivos → Importar**) y por GraphQL como `importDevices`.

- Las columnas son `token`, `deviceTypeToken`, `externalId`, `name`, `description`, `metadata` y, si quieres reservar el dispositivo para el autorregistro, `provisioningProfileToken`.
- Con **credenciales de fábrica**, el archivo lleva además `credentialType`, `credentialId` y, para `MQTT_BASIC`, `credentialValue`. Así conservas el token de acceso actual de un dispositivo: ponlo en `credentialId` con el tipo `ACCESS_TOKEN`, y un dispositivo que presente ese token sigue funcionando sin reprogramarlo con uno nuevo. El secreto se guarda solo como un resumen con clave y no se vuelve a mostrar.
- La importación solo crea y se puede ejecutar de nuevo sin riesgo. El paso **Comprobar el archivo** es una ejecución de prueba que decide cada fila exactamente como lo hará la importación real y no escribe nada.
- Un archivo puede tener 10 000 filas y 5 MiB; divide una flota mayor en varios archivos.
- Exporta tu inventario anterior a CSV con los encabezados anteriores, o usa **Exportar CSV** de la consola de un tenant de DeviceChain como plantilla.

Las credenciales del archivo deben cumplir las reglas de DeviceChain. Una contraseña `MQTT_BASIC` debe tener al menos 16 bytes, así que una contraseña antigua y más corta no se puede trasladar y hay que rotarla. Las filas que incumplen una regla vuelven como `INVALID` con el motivo, y las filas fallidas se pueden descargar, corregir e importar de nuevo.

### Registrar dispositivos cuando se conectan {#self-registration}

Para dispositivos que puedes reprogramar o reconfigurar, el [autorregistro](./device-self-registration.md) permite que un dispositivo se cree a sí mismo con la clave y el secreto de un perfil de aprovisionamiento y un número de serie, y reciba su token y su client id de MQTT. `dcctl device register` hace ese papel desde una shell, lo que sirve para probar un perfil antes de que lo use un dispositivo; consulta la [referencia de comandos](../reference/dcctl.md#dcctl-device-register). Las flotas basadas en certificados pueden usar [credenciales X.509](./device-x509.md) con un ancla de confianza que añada tu tenant.

### Redirigir dispositivos MQTT {#connecting-devices}

Un dispositivo que ya habla MQTT necesita cambiar sus ajustes de conexión y, en general, también su payload. Ten en cuenta estas diferencias respecto a lo que hace hoy:

- **Dirección del broker y TLS.** El listener es TLS y el dispositivo se conecta con la CA de la instancia; consulta los [ajustes de conexión](./connecting-a-device.md#connection-settings).
- **Client id.** Debe ser `{instanceId}:{tenant}:{deviceToken}`, opcionalmente con un `:sufijo`. El broker rechaza cualquier otro client id, incluido uno aleatorio que invente una biblioteca.
- **Nombre de usuario.** Es `{tenant}:{credentialId}`, no solo el id de la credencial.
- **Topic.** Los eventos se publican en `{instanceId}/{tenant}/devices/{token}/events`.
- **Cuerpo.** Un sobre JSON con `device`, `eventType` y `payload.entries`, en el que cada lectura es una cadena JSON. Por MQTT el cuerpo no lleva credencial. Consulta [el cuerpo del evento](./connecting-a-device.md#el-cuerpo-del-evento) y [tres identificadores](./connecting-a-device.md#three-identifiers), la lista de los tres valores que más se confunden.

Si no puedes cambiar el firmware, coloca un pequeño puente delante: un proceso que se suscriba a los topics antiguos y publique en DeviceChain, o un [dispositivo gateway](../deployment/edge-services.md#gateway-identity) que hable por un conjunto de dispositivos asignados a él. Los dispositivos por HTTP envían el mismo sobre al endpoint de ingesta descrito en [Conectar un dispositivo](./connecting-a-device.md).

Los dispositivos detrás de un servidor de red LoRaWAN no cambian en absoluto. Añade una [integración de entrada](./inbound-integrations.md) y apunta el webhook del servidor de red a ella. Los dispositivos Sparkplug B y LwM2M se conectan mediante sus propios adaptadores; consulta [Sparkplug](../concepts/sparkplug.md) y [LwM2M](../concepts/lwm2m.md), y la [matriz de transportes](../reference/transport-matrix.md) para ver qué admite cada uno.

### Usar dcctl para la instancia, no para tus datos {#dcctl}

`dcctl` instala, arranca, actualiza e inspecciona una instancia; consulta la [referencia de comandos](../reference/dcctl.md) y [bootstrap](../deployment/bootstrap.md). No importa entidades ni lecturas. Úsalo para levantar la instancia que recibirá la migración y para las comprobaciones que ofrece, como `dcctl device register` arriba.

### Trasladar el historial {#history}

Nada convierte por ti el historial almacenado de otra plataforma: no existe un importador de historial, y leer la base de datos o los archivos de exportación de la plataforma anterior es trabajo tuyo. Lo que ofrece DeviceChain es la vía de ingesta, que acepta lecturas con su propia hora. El `occurredTime` de una entrada es el instante en que se almacena, se grafica y se evalúa, de modo que puedes reproducir el historial por la misma vía de ingesta que los datos en vivo. Se aplican dos límites: una lectura cuyo `occurredTime` sea más de **366 días** anterior al momento en que la plataforma la recibe se rechaza, y la ingesta tiene un límite de tasa por tenant, así que una reproducción grande debe espaciarse. Pon un `altId` en cada evento reproducido para que un reintento no lo almacene dos veces. Consulta [cargas diferidas y reglas con ventana](./connecting-a-device.md#buffered-uploads-and-windowed-rules) y [calidad de servicio](./connecting-a-device.md#quality-of-service).

Si necesitas un historial más antiguo, o el volumen es grande, decide primero si lo necesitas en la plataforma nueva. Mantener el almacén antiguo en solo lectura para informes y empezar el nuevo desde el cambio suele ser la respuesta más barata. Para que los datos de DeviceChain sean accesibles desde tus herramientas de informes actuales, consulta [acceso SQL y BI](./sql-and-bi-access.md).

### Reconstruir los paneles {#dashboards}

Un panel es una definición JSON que DeviceChain puede exportar e importar, pero solo importa paneles de DeviceChain: los widgets están enlazados a las métricas y entidades propias de DeviceChain. Reconstruye cada panel en el [editor de lienzo](../user-guide/dashboards.md), usando ranuras (slots) para que un panel se enlace a un dispositivo o a un cliente al abrirse en lugar de nombrar uno. Un panel importado se guarda como borrador con un token nuevo y nunca se publica por ti; consulta [importar un panel](../user-guide/dashboards.md#importar-un-panel). Cuando tengas unos cuantos paneles, expórtalos como tus plantillas.

## Un ejemplo completo {#worked-example}

El repositorio incluye una evaluación ejecutable en `examples/migration-pilot`
([en GitHub](https://github.com/devicechain-io/devicechain/tree/main/examples/migration-pilot)).
Parte de una exportación pequeña e inventada de "la plataforma anterior": 25 dispositivos de dos
tipos, sus claves de telemetría, dos reglas de umbral y una regla basada en scripts, la descripción
de un panel y un token de acceso por dispositivo. Después hace, con `curl` y `jq` contra la API
GraphQL pública y nada más:

1. **Un plan.** Un archivo de mapeo convierte cada concepto en su equivalente de DeviceChain, y el
   plan muestra el resultado, incluido lo que no se trasladará, antes de escribir nada.
2. **El modelo.** Crea los perfiles, las métricas, las reglas de detección y los tipos de
   dispositivo, y publica los perfiles.
3. **Una prueba en seco y luego la importación.** La importación de dispositivos descrita
   [más arriba](#csv) comprueba las 25 filas sin escribir y después crea 23 dispositivos con sus
   tokens de acceso existentes. Las otras dos vuelven con un motivo: una contraseña de menos de 16
   bytes y un número de serie que aparece dos veces.
4. **Un informe de conciliación.** Cada elemento de la exportación queda contabilizado como creado,
   ya existente u omitido con un motivo, y después los totales se leen de DeviceChain y se comparan.
5. **Un piloto en paralelo.** Tres dispositivos simulados publican a la vez en un sustituto de la
   plataforma anterior y en DeviceChain, y se comparan los valores. Cambiar una línea de la
   configuración del dispositivo lo devuelve a la plataforma anterior, y el ejemplo muestra tanto
   eso como el bloqueo firme en DeviceChain.

Es una vía de viabilidad, no un conversor. No lee por ti los archivos de la plataforma anterior, no
reconstruye reglas ni paneles, no convierte el historial, y un dispositivo sigue necesitando que se
cambien sus ajustes de conexión y su payload. El ejemplo lo detalla, incluido lo que cuesta cada
límite.

## Lo que no tiene equivalente directo {#no-equivalent}

Algunas cosas no se trasladan, y unas pocas no están previstas. Comprueba esta lista con lo que usa tu despliegue actual antes de comprometer una fecha.

- **Un conversor de archivos de otra plataforma.** DeviceChain no tiene un importador para el formato de exportación de otra plataforma, para sus paneles ni para sus definiciones de reglas. Se reconstruyen.
- **Reglas como un grafo libre de nodos.** Las reglas de detección son condiciones sobre eventos, escritas con un formulario, en un lienzo o en el mismo lenguaje de expresiones que usa el motor de detección, y actúan levantando una alarma, enviando un comando, llamando a un webhook o publicando en un conector. No hay un nodo que ejecute código de script arbitrario a mitad de un flujo, ni un motor de flujos de propósito general. Una cadena que hace enriquecimiento, ramificación y llamadas externas en un solo grafo suele convertirse en varias reglas más una métrica calculada, y todo lo más complejo corresponde a un servicio al que llamas por webhook. Lee [procesamiento de eventos](../concepts/event-processing.md) antes de planificar el traslado.
- **Decodificadores de payload escritos por ti.** La plataforma almacena los valores que decodifica un servidor de red y puede asociar un cuerpo JSON genérico; no ejecuta un script de decodificación que le entregues. Si tus dispositivos envían un payload binario opaco, decodifícalo antes de que llegue a DeviceChain, en el códec del servidor de red o en un puente. Consulta [integraciones de entrada](./inbound-integrations.md).
- **Lecturas de texto como historial.** Las lecturas numéricas y booleanas se almacenan como series temporales. Una lectura de texto no, así que una señal de tipo cadena se conserva como estado, por ejemplo un atributo, y no se grafica como historial.
- **Clientes anidados.** Los clientes no tienen padres. Los activos se anidan y las relaciones pueden expresar otra estructura. Si dependías de una jerarquía de clientes para la visibilidad, modélala con grupos y membresías con ámbito y comprueba lo que ve un usuario.
- **Varios de los comportamientos de actualización por aire que ofrecen algunas plataformas.** Los despliegues no tienen oleadas, promoción de canarios ni programación, y solo los dispositivos conectados por MQTT reciben actualizaciones gestionadas. Consulta [actualizaciones por aire](../concepts/over-the-air-updates.md).
- **Controladores de protocolos industriales, SNMP y una aplicación móvil nativa.** Los mismos límites que en [Por qué DeviceChain](../why-devicechain.md#not-yet): usa un gateway que hable el protocolo y reenvíe por MQTT o HTTP.
- **Plataformas distintas de Kubernetes.** DeviceChain se ejecuta en Kubernetes. Si vienes de una instalación en un solo servidor, presupuesta ese cambio además del traslado de datos.

Si una pieza que falta te bloquea, dilo en las [Discussions](https://github.com/devicechain-io/devicechain/discussions/categories/ideas) del proyecto; la [hoja de ruta](https://github.com/devicechain-io/devicechain/blob/main/ROADMAP.md) muestra lo que ya está previsto.

## Un plan sugerido {#plan}

El orden siguiente reduce el riesgo: se mueven primero unos pocos dispositivos, se ejecutan las dos plataformas en paralelo y solo se corta cuando se ve que la nueva coincide con la antigua.

### 1. Inventario y diseño

1. Enumera lo que operas: clases de dispositivos, las claves de telemetría y unidades de cada una, atributos, comandos, reglas, paneles, clientes, usuarios e integraciones.
2. Marca cada elemento como trasladado, reconstruido o descartado, con ayuda de las tablas anteriores.
3. Diseña en papel el modelo de DeviceChain: un perfil por clase de dispositivo, las claves y unidades de las métricas, los tipos de relación que marcarás con seguimiento y tu estructura de clientes y áreas. Elige tokens que conservarás durante toda la vida del dispositivo.
4. Decide cómo se conectará cada clase de dispositivo: sin cambios detrás de un servidor de red, reconfigurada o detrás de un puente.

### 2. Levantar un piloto

1. Levanta una instancia con el [inicio rápido](../quickstart/first-device.md) para una prueba, o sigue [bootstrap](../deployment/bootstrap.md) para una real, y lee [ajustar una instancia](../deployment/tuning-an-instance.md) y el [rendimiento medido](../deployment/bootstrap.md#measured-throughput) frente al tamaño de tu flota.
2. Crea el modelo mediante la API o la consola, publica los perfiles e importa desde CSV una **flota piloto** de unas decenas de dispositivos, con sus credenciales actuales si las vas a conservar.
3. Redirige esos dispositivos, o su puente, y confirma que las lecturas llegan con las unidades correctas en las pestañas **Conectividad** y **Eventos** del dispositivo.
4. Reconstruye las reglas que esos dispositivos necesitan y el panel que usan sus propietarios, y confirma que una alarma se levanta, notifica y despeja como lo hace hoy.

### 3. Ejecutar en paralelo

1. Envía las lecturas de los dispositivos piloto a ambas plataformas. Un dispositivo o un puente puede publicar en las dos, o un servidor de red puede tener dos webhooks.
2. Compara ambas durante un periodo que incluya las condiciones que te importan: las mismas alarmas a las mismas horas, los mismos totales de un día, un comando que llega a un dispositivo y recibe respuesta.
3. Corrige las diferencias en el modelo, no en los datos. Una unidad o una clave incorrecta es una versión nueva del perfil.
4. Amplía el piloto por clase de dispositivo, importando cada clase desde su propio archivo CSV y comprobando antes de pasar a la siguiente.
5. Forma a quienes la operarán. Crea sus [miembros y roles](../user-guide/members-roles-and-customer-users.md), configura el [inicio de sesión único](./configuring-sso.md) si lo usas e invita a los usuarios de cliente a la vista de su cliente antes de salir en producción.

### 4. Hacer el cambio

1. Elige una ventana tranquila y un plan de vuelta atrás: la plataforma antigua sigue funcionando y sus dispositivos no se eliminan.
2. Redirige los dispositivos restantes, clase por clase, vigilando las cuentas de conexiones y eventos de cada clase antes de empezar la siguiente. Las flotas con autorregistro se mueven a medida que se configura cada dispositivo.
3. Reproduce el historial que decidiste traer, con `altId` y `occurredTime` definidos.
4. Traslada las notificaciones de alarmas y las integraciones a DeviceChain, y desactívalas en la plataforma antigua para que nadie reciba el aviso dos veces.
5. Mantén la plataforma antigua en solo lectura el tiempo que necesites su historial y luego retírala.

### 5. Después del cambio

Vigila las métricas y alertas de la instancia desde [observabilidad](../deployment/observability.md), configura la [copia de seguridad y restauración](../deployment/disaster-recovery.md) en la que pienses apoyarte y revisa la [política de versiones](../deployment/versioning-policy.md) para saber qué cambiará y qué no en una actualización.

## Relacionado

- [Por qué DeviceChain](../why-devicechain.md): para qué sirve DeviceChain y qué todavía no hace.
- [Modelo de dominio](../concepts/domain-model.md): las entidades y relaciones con las que se corresponde esta página.
- [Importar dispositivos desde CSV](./importing-devices.md) y [Conectar un dispositivo](./connecting-a-device.md).
- [API GraphQL](../reference/graphql-api.md): la superficie que maneja un script de migración.
