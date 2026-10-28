---
title: Dispositivos
sidebar_position: 3
---

import GuideShot from '@site/src/components/GuideShot';

# Dispositivos

Un dispositivo es una cosa física que se conecta a DeviceChain y envía datos: un termostato, un
contador, un localizador. Este capítulo trata la lista de dispositivos, las formas de añadirlos
(de uno en uno, en masa o desde un archivo CSV), la exportación de la lista, la deshabilitación de
un dispositivo y cada pestaña de la página de un dispositivo.

Es para los usuarios y administradores del inquilino que registran y cuidan los dispositivos. Todo
dispositivo pertenece a un tipo de dispositivo, así que necesitas al menos un tipo antes de añadir
un dispositivo. Los tipos de dispositivo se tratan en el
[capítulo siguiente](./device-types-and-profiles.md).

## La lista de dispositivos

Abre **Dispositivos** en la barra lateral y luego **Dispositivos** dentro de él.

<GuideShot id="devices-list" alt="La página Dispositivos. Arriba a la derecha están los botones Exportar CSV, Importar, Creación masiva y Nuevo dispositivo. Debajo hay una tabla con las columnas Estado, Token, Nombre, Tipo, Descripción y Creado. Doce filas muestran En línea y la primera, Boiler room thermostat, muestra un guion como estado. El tipo de todas las filas es Building Pulse Thermostat. El pie dice 'Mostrando 1–13 de 13 resultados' con los botones Anterior y Siguiente." />

La lista muestra 20 dispositivos por página. Selecciona una fila para abrir la página de ese
dispositivo. Las columnas son:

- **Estado**: si el dispositivo está conectado ahora mismo. **En línea** significa que está
  conectado o que ha enviado datos hace poco. **Desconectado** significa que su conexión informó
  de que se fue. **Fuera de línea** significa simplemente que hace un tiempo que no se sabe nada de
  él. Un guion significa que aún no se sabe nada: el dispositivo nunca ha enviado datos, o no
  tienes permiso para ver su estado.
- **Token**: el identificador único del dispositivo en este inquilino. No se puede cambiar.
- **Nombre**: un nombre legible. Un dispositivo deshabilitado lleva el distintivo
  **Deshabilitado** junto a su nombre.
- **Tipo**: el tipo de dispositivo, con los colores y el icono que se le dieron.
- **Descripción** y **Creado**.

Cómo decide DeviceChain si un dispositivo está en línea se explica en
[Presencia de dispositivo](../concepts/device-presence.md).

## Añadir un dispositivo

Elige **Nuevo dispositivo**.

<GuideShot id="devices-new" alt="El panel Nuevo dispositivo abierto sobre la lista. Tiene un selector Tipo de dispositivo, un campo Token que muestra {slug} con un botón para regenerarlo y la indicación 'Patrón: {slug}', un campo Nombre, un campo Descripción y un botón Crear dispositivo." />

1. Elige el **Tipo de dispositivo**.
2. Escribe un **Token**, o usa el botón junto al campo para generar uno a partir del patrón de
   tokens de tu instalación. El patrón se muestra bajo el campo. `{slug}` se rellena a partir del
   nombre que escribas.
3. Escribe un **Nombre** y, si quieres, una **Descripción**.
4. Si el dispositivo informa a través de un servidor de red LoRaWAN u otro remitente en lugar de
   conectarse por sí mismo, añade sus [alias](#aliases).
5. Elige **Crear dispositivo**.

Un dispositivo nuevo que se conecta por sí mismo no puede enviar nada hasta que tenga una credencial.
Añádela desde su pestaña [Credenciales](#credentials). Un dispositivo que informa a través de una
[integración de entrada](../guides/inbound-integrations.md) necesita un alias en su lugar.

### Alias {#aliases}

Un **alias** es el nombre con el que otro sistema conoce al dispositivo, como su DevEUI de LoRaWAN. El
campo **Alias** del formulario del dispositivo admite hasta 8, un valor por esquema:

1. Elige **Añadir alias**.
2. Escoge el esquema: **DevEUI de LoRaWAN** (16 dígitos hexadecimales), **Id de dispositivo del
   servidor de red** (el id del dispositivo en The Things Stack) o **Esquema propio** (un nombre tuyo y
   un valor).
3. Escribe el valor. Un DevEUI se guarda en mayúsculas, lo escribas como lo escribas.

Un alias pertenece a un solo dispositivo del inquilino. Una integración de entrada encuentra el
dispositivo al que se refiere un mensaje por su alias. Consulta
[Integraciones de entrada](../guides/inbound-integrations.md#aliases).

## Crear muchos dispositivos a la vez

**Creación masiva** crea un lote de dispositivos del mismo tipo, numerados en secuencia.

<GuideShot id="devices-bulk" alt="El panel Crear dispositivos en masa. Tiene un selector Tipo de dispositivo, Cantidad con el valor 10 (1–1000) e Índice inicial con el valor 1, una Plantilla de token device-{n:04d}, una Plantilla de nombre 'Device {n}' y una Plantilla de ID externo vacía con el ejemplo VIN-{random}. Un recuadro de Vista previa enumera de device-0001 Device 1 a device-0003 Device 3, unos puntos suspensivos y device-0010 Device 10. El botón Crear 10 dispositivos está desactivado, con la nota de que hay que elegir un tipo de dispositivo." />

- **Tipo de dispositivo**: todos los dispositivos del lote reciben este tipo.
- **Cantidad**: cuántos dispositivos crear, de 1 a 1000.
- **Índice inicial**: el primer número que se usa.
- **Plantilla de token**: debe incluir `{n}`, o `{n:0Wd}` para rellenar el número con ceros
  hasta el ancho W. `device-{n:04d}` da `device-0001`, `device-0002`, etc.
- **Plantilla de nombre** (opcional): puede usar `{n}` o `{n:0Wd}`.
- **Plantilla de ID externo** (opcional): tu propio identificador de negocio para cada
  dispositivo. También puede usar `{random}` para un identificador aleatorio por dispositivo.
  Cuando creas más de un dispositivo, debe incluir `{n}`, `{n:0Wd}` o `{random}` para que cada uno
  reciba un identificador distinto.

El recuadro **Vista previa** muestra los primeros y el último de los tokens y nombres que recibirá
el lote. Revísalo y elige **Crear N dispositivos**.

## Importar dispositivos desde un archivo CSV

**Importar** crea dispositivos a partir de un archivo CSV. Úsalo para cargar una flota que ya
tienes en una hoja de cálculo, o un archivo que te entregó el fabricante.

<GuideShot id="devices-import" alt="El panel Importar dispositivos desde CSV. Un control Credenciales ofrece Ninguna (seleccionada) y Credenciales de fábrica, con una indicación de que solo se crean los dispositivos. Debajo están los límites del archivo (5 MiB y 10000 filas, con fila de cabecera, UTF-8, separado por comas, punto y coma o tabulaciones), un botón Elegir un archivo CSV, un cuadro 'O pega las filas' con una cabecera y una fila de ejemplo, y un botón Usar las filas pegadas." />

1. En **Credenciales**, elige **Ninguna** para crear solo los dispositivos, o
   **Credenciales de fábrica** para crear cada dispositivo junto con la credencial que tu fábrica
   le asignó.
2. Elige un archivo con **Elegir un archivo CSV**, o pega las filas y elige
   **Usar las filas pegadas**. El archivo necesita una fila de cabecera y puede tener hasta
   10.000 filas y 5 MiB.
3. En **Asigna las columnas**, comprueba qué columna de tu archivo alimenta cada campo. La columna
   del token debe estar asignada, y cada fila necesita un tipo de dispositivo: asigna una columna
   de tipo o elige un **Tipo de dispositivo para todo el archivo**. Con
   **Credenciales de fábrica**, también deben estar asignadas las columnas del tipo y del id de la
   credencial. Una vista previa muestra las primeras filas tal
   como se enviarán.
4. Elige **Comprobar el archivo**. Es un ensayo: el servidor decide cada fila exactamente como lo
   haría la importación real, y no crea nada.
5. Lee **Lo que haría una importación**. Cada fila tiene un resultado: **Creado**, **Ya existe**,
   **Conflicto**, **No válido** o **Rechazado**, con detalles para lo que no se crearía. Puedes
   filtrar las filas por resultado.
6. Elige **Importar N dispositivos**, donde N es el número de filas que, según la comprobación,
   se crearían. Una barra de progreso muestra cuánto lleva.
   **Detener tras este lote** se detiene limpiamente entre lotes. Si vuelves a importar el mismo
   archivo, continúa donde se detuvo.
7. Al terminar, el resumen dice cuántos dispositivos se crearon, cuántos ya existían y cuántos
   requieren atención. **Descargar resultados** guarda el resultado de cada fila y
   **Descargar filas fallidas** guarda solo las filas que hay que corregir. Corrígelas e importa
   ese archivo. Los dispositivos ya creados aparecen como existentes y no se tocan.

Con **Credenciales de fábrica**, el archivo contiene secretos. Se envían una sola vez, nunca se
muestran ni se escriben en los resultados, y se borran de la página cuando termina la
importación. El archivo en sí queda bajo tu responsabilidad.

Las columnas, los modos de credenciales y todos los resultados se describen en detalle en
[Importar dispositivos desde CSV](../guides/importing-devices.md).

## Exportar la lista

**Exportar CSV** descarga todos los dispositivos de la lista en un archivo CSV. El archivo
contiene el token, el nombre, el tipo, el token del tipo, la descripción y la fecha de creación de
cada dispositivo, con las horas en formato ISO-8601. No incluye el estado en vivo. Las cabeceras
coinciden con lo que espera **Importar**, así que un archivo exportado se puede volver a importar
tal cual.

Mientras se exporta, el botón muestra cuántas filas lleva reunidas, junto a un botón
**Cancelar exportación**. Un mensaje confirma cuántas filas se exportaron. Una exportación se
detiene en 10.000 filas; si llega a ese límite, la consola te avisa de que el archivo contiene
solo las primeras 10.000.

## Deshabilitar y habilitar un dispositivo

Deshabilitar un dispositivo lo deja fuera sin eliminarlo. Úsalo para poner en cuarentena un
dispositivo que se comporta mal o que puede estar comprometido, y vuelve a habilitarlo cuando esté
corregido.

Abre el dispositivo y elige **Deshabilitar** arriba a la derecha. La consola te pide confirmación.
Mientras el dispositivo está deshabilitado:

- sus eventos se descartan en lugar de almacenarse, y no se recuperan al volver a habilitarlo;
- no puede abrir una conexión nueva, aunque una conexión que ya tenga sigue abierta hasta que
  termina. Cuando esa conexión termina, el dispositivo aparece como desconectado;
- los comandos nuevos que se le envían se rechazan;
- los comandos que ya estaban en cola para él quedan **retenidos**: ni se envían ni se cancelan.

Sus datos, credenciales y configuración se conservan. Un dispositivo deshabilitado lleva el
distintivo **Deshabilitado** en su página y en la lista de dispositivos.

Cuando hay comandos retenidos, la pestaña **Comandos** del dispositivo indica cuántos, por ejemplo
«2 comandos en cola retenidos mientras este dispositivo está deshabilitado». Salen, del más
antiguo al más reciente, cuando habilitas el dispositivo, o caducan a su propia hora de
vencimiento. Si no deben enviarse nunca, elige **Cancelar todos**. Un comando que ya se envió no
se puede recuperar.

Para recuperar el dispositivo, elige **Habilitar**. Sus eventos vuelven a almacenarse y puede
conectarse y recibir comandos.

La lista completa de lo que hace la deshabilitación, incluidas la configuración y las alarmas,
está en [Deshabilitar un dispositivo](../guides/device-credentials.md#disabling-a-device).

## La página del dispositivo

Selecciona un dispositivo en la lista para abrir su página. La cabecera muestra el nombre del
dispositivo, su token (con un botón para copiarlo), su tipo y, si está deshabilitado, el
distintivo **Deshabilitado**. A la derecha están **Deshabilitar** (o **Habilitar**) y
**Eliminar**. Eliminar un dispositivo no se puede deshacer.

La página se organiza en pestañas. Puede que no las veas todas: **Credenciales**, **Enviar datos** y
**Sustitución** requieren permiso para modificar dispositivos, y **Actualizaciones** solo aparece
cuando tu instalación ejecuta el servicio de actualizaciones y tienes permiso para verlas.

### Básico

**Básico** contiene el tipo, el token, el nombre y la descripción del dispositivo.

<GuideShot id="device-detail-basic" alt="La página del dispositivo Lobby thermostat, token bp-therm-001, de tipo Building Pulse Thermostat. Arriba a la derecha están los botones Deshabilitar y Eliminar. Las pestañas son Básico, Asignación, Facetas, Conectividad, Eventos, Comandos, Configuración, Actualizaciones, Credenciales y Sustitución. La pestaña Básico está abierta, con el Tipo de dispositivo, un campo Token atenuado con la nota 'El id del dispositivo; no se puede cambiar.', Nombre y Descripción, y un botón Guardar cambios." />

Puedes cambiar el tipo, el nombre y la descripción y elegir **Guardar cambios**. El token no se
puede cambiar.

### Asignación

**Asignación** vincula el dispositivo con el cliente, el área o el activo al que sirve. Organiza
los datos del dispositivo, pero no decide si puede enviar datos: un dispositivo sin asignación
sigue enviándolos.

<GuideShot id="device-detail-assignment" alt="La pestaña Asignación. Arriba hay un selector Tipo de destino con el valor Cliente, un selector Destino vacío y un botón Asignar. Debajo, una tabla enumera tres asignaciones, cada una con un botón Desasignar: el activo bp-hvac-01, el cliente bp-acme y el área bp-bldg-01." />

Elige un **Tipo de destino** (**Cliente**, **Área** o **Activo**), selecciona el **Destino** y
elige **Asignar**. Un dispositivo puede tener varias asignaciones a la vez. **Desasignar** quita
una. Los eventos que el dispositivo ya envió conservan las asignaciones con las que se
registraron.

Consulta [Gestión de asignaciones de dispositivos](../guides/managing-assignments.md).

### Facetas {#facets}

**Facetas** contiene los valores que describen el dispositivo, como la zona o la planta en la que
está. **Explorar** y los grupos dinámicos encuentran dispositivos por estos valores.

<GuideShot id="device-detail-facets" alt="La pestaña Facetas. En 'Valores de faceta' hay dos facetas, Zone (STRING) con el valor lobby y Floor (LONG) con el valor 1, cada una con los botones Guardar y Limpiar. Bajo Zone, una nota indica que cambiar el valor mueve la entidad dentro o fuera del grupo Rooftop units. Debajo, 'Otros atributos' enumera schedule (SHARED, STRING, weekday-standard) y setpoint (SHARED, DOUBLE, 22.5), cada uno con un botón Eliminar, y un botón Añadir atributo." />

- **Valores de faceta** enumera las facetas que tu inquilino ha declarado. Escribe un valor y
  elige **Guardar**, o **Limpiar** para quitarlo. Cuando un valor decide la pertenencia a un grupo
  dinámico, la pestaña te dice de qué grupos entraría o saldría el dispositivo con el cambio.
- **Otros atributos** enumera todo lo demás que se guarda en el dispositivo. Puedes añadir un
  atributo compartido con **Añadir atributo** y eliminarlo. Los valores que informa el
  dispositivo (**CLIENT**) o que fija la plataforma (**SERVER**) se muestran solo como
  referencia. Los atributos compartidos son también donde viven los valores de la
  [configuración](#configuration) del dispositivo.

Las facetas y Explorar tienen su propio capítulo.

### Conectividad

**Conectividad** muestra si el dispositivo está conectado y sus lecturas más recientes.

<GuideShot id="device-detail-connectivity" alt="La pestaña Conectividad. La sección Conectividad muestra el distintivo En línea, el Origen de la presencia 'Inferido a partir de la actividad' y las filas Última conexión, Última actividad y Última desconexión, cuyas horas están ocultas en esta captura. La sección Últimas lecturas enumera las mediciones co2, humidity, setpoint y temperature, con sus valores y horas ocultos." />

- El distintivo dice **En línea**, **Desconectado** o **Fuera de línea**.
- **Origen de la presencia** indica cómo se sabe. **Reportado por el transporte** significa que la
  propia conexión se lo comunicó a DeviceChain. **Inferido a partir de la actividad** significa que
  DeviceChain lo dedujo de lo reciente que es el último envío del dispositivo.
- **Última conexión**, **Última actividad** y **Última desconexión** dan las horas.
- **Últimas lecturas** enumera el último valor de cada medición y cuándo llegó.

Cuando un dispositivo inferido se queda callado, la pestaña añade la nota «Sin actividad
reciente»: hace un tiempo que no envía nada, algo que también puede parecer un dispositivo sano
con un intervalo de envío lento.

Un dispositivo cuyo perfil declara una posición, o que ya ha informado de una, tiene además una
sección **Última posición conocida**, con su latitud, longitud y, cuando se informan, la
elevación, la precisión, la velocidad y el rumbo. El termostato de la captura no cumple ninguna de
las dos condiciones, así que la sección no aparece.

### Eventos

**Eventos** enumera los 10 eventos más recientes que envió el dispositivo.

<GuideShot id="device-detail-events" alt="La pestaña Eventos: una tabla con las columnas Ocurrió, Tipo y Origen. Diez filas muestran el tipo Medición y el origen http1, con las horas ocultas en esta captura. Debajo se lee 'Mostrando los 10 eventos más recientes.'" />

Cada fila muestra cuándo ocurrió el evento, su tipo (por ejemplo, una medición) y el origen por el
que llegó. Para un historial más largo, usa un panel.

### Comandos {#commands}

**Comandos** es donde envías un comando a este dispositivo concreto y sigues los comandos que ya
se le enviaron.

<GuideShot id="device-detail-commands" alt="La pestaña Comandos. Un selector Comando vacío, con la indicación de que ofrece los comandos que declara el perfil publicado del dispositivo, y un botón Emitir comando desactivado. Debajo, una tabla con las columnas En cola, Nombre, Estado y Resultado muestra un comando, set-setpoint, con el estado SENT y todavía sin resultado." />

El selector **Comando** ofrece los comandos que declara el perfil del dispositivo. Elige uno y la
consola muestra un formulario para sus parámetros.

<GuideShot id="device-commands-parameter-form" alt="La pestaña Comandos con 'Set temperature setpoint (set-setpoint)' elegido. Aparecen dos campos de parámetro: setpoint, marcado como obligatorio, con la indicación 'Target temperature · in Cel · 10–30', y mode, un selector con la indicación 'Operating mode'. El botón Emitir comando ya está activado." />

Rellena los parámetros y elige **Emitir comando**. El formulario comprueba cada valor con el
perfil, por ejemplo el rango permitido, antes de enviar nada. Si el perfil no declara comandos,
puedes escribir cualquier nombre de comando y, opcionalmente, una carga útil.

La tabla de abajo enumera los comandos del dispositivo con su estado, como **QUEUED**, **SENT** o
**SUCCESSFUL**, y el resultado que informó el dispositivo. Los estados se muestran en inglés,
también en la consola en español. Un comando que aún no ha llegado al dispositivo (**QUEUED**,
**HELD** o **PARKED**) tiene un botón **Cancelar**. Un comando ya enviado no se puede cancelar.

Qué significa cada estado se explica en [Comandos](../concepts/commands.md#command-lifecycle), y
[Enviar un comando](../guides/sending-commands.md) cubre cómo enviar uno y seguirlo. Para enviar un
comando a muchos dispositivos a la vez, usa un lote de comandos.

### Configuración {#configuration}

**Configuración** muestra los ajustes que DeviceChain guarda para que el dispositivo los aplique,
y si el dispositivo ya los aplicó.

<GuideShot id="device-detail-configuration" alt="La pestaña Configuración. Un distintivo Pendiente y el texto de que la revisión #3 espera a aplicarse y el dispositivo no ha informado de ninguna revisión. Debajo están la Revisión deseada #3 con su resumen sha256, la Revisión informada 'Nunca ha informado' y guiones en Estado del informe, Informado y Última sincronización. Una tabla de Claves declaradas enumera setpoint (DOUBLE), schedule (STRING) y reportIntervalSeconds (LONG) con sus descripciones. 'Atributos compartidos no declarados' muestra floor y zone. Al final está el Documento deseado, una línea de JSON con schedule y setpoint." />

La configuración de un dispositivo es un pequeño documento construido a partir de sus atributos
compartidos. Solo se incluyen las claves que declara su perfil. Cada cambio crea una nueva
**revisión** numerada, que se envía al dispositivo.

- El distintivo de arriba dice si el dispositivo está al día. **Sincronizado** significa que
  aplicó la última revisión. **Pendiente** significa que hay una revisión esperando a aplicarse.
  **Desactualizado** significa que los valores cambiaron desde la última revisión. Se crea una
  revisión nueva la próxima vez que se escribe una clave declarada o que el dispositivo se
  sincroniza. **Sin revisión todavía** significa que aún no se creó ninguna.
- **Revisión deseada** es la última, y **Revisión informada** es la que el dispositivo dijo tener
  por última vez. **Estado del informe** es lo que hizo el dispositivo con ella: **Recibida**,
  **Aplicada** o **Rechazada**. Cuando el dispositivo rechaza una configuración, se enumeran sus
  motivos.
- **Claves declaradas** enumera las claves que declara el perfil, tomadas de su versión publicada
  activa. Sus valores se cambian como atributos compartidos en la pestaña [Facetas](#facets).
- **Atributos compartidos no declarados** son atributos compartidos que el perfil no nombra.
  Nunca se envían al dispositivo.
- **Documento deseado** es el documento exacto que se envía al dispositivo.
- **Historial de revisiones** enumera las revisiones del dispositivo, de la más reciente a la más
  antigua, y quién hizo cada una.

Puedes devolver un dispositivo a una configuración anterior o darle la de otro dispositivo. En los
dos casos la consola muestra primero una vista previa, y nada cambia hasta que eliges
**Aplicar**.

Para restaurar una revisión anterior, elige **Restaurar** en su fila del
**Historial de revisiones**.

<GuideShot id="device-configuration-restore" alt="La pestaña Configuración desplazada hacia abajo. Una vista previa 'Restaurar revisión #2' indica que se cambia 1 clave, que se crea una revisión nueva que se envía al dispositivo y que las revisiones anteriores no se modifican. Su tabla tiene las columnas Clave, Actual, Después y Cambio: schedule sigue en weekday-standard, y setpoint pasa de 22.5 a 21, marcado como Cambiada. Siguen los botones Aplicar y Cancelar. Debajo está el Historial de revisiones con las revisiones #3 y #2, sus resúmenes, sus horas de creación y quién las cambió, y un botón Restaurar en la #2." />

Para copiar la configuración de otro dispositivo, elígelo en **Copiar de otro dispositivo** y
elige **Previsualizar cambios**. La copia toma la última revisión de ese dispositivo.

<GuideShot id="device-configuration-copy" alt="La sección Copiar de otro dispositivo con Rooftop AHU 1 return air elegido. Una vista previa titulada con la copia de la configuración de bp-therm-002 indica que se cambian 3 claves. Su tabla muestra reportIntervalSeconds añadida con 300, schedule eliminada y setpoint cambiada de 22.5 a 19.5. Siguen los botones Aplicar y Cancelar." />

La vista previa enumera cada clave con su valor **Actual**, el valor que tendrá **Después** y si
queda **Añadida**, **Cambiada** o **Eliminada**. Al aplicar se crea una revisión nueva y se envía
al dispositivo. Las revisiones anteriores no se modifican. El cambio se rechaza si el perfil de
este dispositivo no declara todas las claves que lleva la configuración.

Cómo recibe un dispositivo su configuración y cómo informa de ella se describe en
[Restaurar o copiar una configuración](../guides/connecting-a-device.md#restore-copy-configuration).

### Actualizaciones

**Actualizaciones** muestra la actualización remota (OTA) asignada al dispositivo y su historial de
actualizaciones. Se trata en el capítulo de actualizaciones remotas.

### Credenciales {#credentials}

**Credenciales** contiene lo que usa el dispositivo para demostrar quién es al conectarse.

<GuideShot id="device-detail-credentials" alt="La pestaña Credenciales de Rooftop AHU 2 return air. Arriba, un formulario con un selector Tipo con el valor ACCESS_TOKEN, un campo Token de acceso con un botón Generar y un botón Agregar credencial. Debajo, una tabla enumera dos credenciales: MQTT_BASIC con el id rooftop-ahu-2 y la nota de que la contraseña está establecida y no se puede mostrar, habilitada, con los botones Establecer nueva contraseña y Eliminar; y ACCESS_TOKEN, cuyo id está oculto en esta captura, habilitada, con un botón Eliminar." />

Hay dos tipos:

- **ACCESS_TOKEN**: el dispositivo presenta un token. Pega el tuyo o elige **Generar**.
- **MQTT_BASIC**: el dispositivo se conecta con un usuario y una contraseña.

Elige el **Tipo**, rellena los campos y elige **Agregar credencial**. Un dispositivo puede tener
más de una credencial. **Eliminar** quita una, y después el dispositivo ya no puede autenticarse
con ella.

Una contraseña MQTT solo se muestra una vez: justo después de añadir la credencial, o después de
elegir **Establecer nueva contraseña** para cambiarla. La plataforma solo guarda un resumen de un
solo sentido, así que nunca puede volver a mostrar la contraseña.

<GuideShot id="device-credentials-shown-once" alt="La pestaña Credenciales con un recuadro destacado titulado 'Nueva contraseña establecida'. Pide copiarla ahora porque no se volverá a ver y la anterior ya no funciona; muestra el usuario rooftop-ahu-2 y la nueva contraseña con un botón para copiarla, y un botón Listo." />

Copia la contraseña, dásela al dispositivo y elige **Listo**. Cuando estableces una contraseña
nueva, la anterior deja de funcionar en cuanto guardas.

Consulta [Credenciales de dispositivo](../guides/device-credentials.md).

### Enviar datos {#send-data}

**Enviar datos** construye un comando listo para ejecutar que envía la primera medición del
dispositivo: un `mosquitto_pub` para MQTT o un `curl` para HTTP. Elige el transporte, y la
credencial si el dispositivo tiene más de una, y copia el comando con el botón de copiar.

El comando usa el token y la credencial del dispositivo, y las métricas de la versión
**publicada** de su perfil, con valores de ejemplo que el perfil acepta. Una métrica que solo
existe en el borrador no se usa, porque la plataforma comprueba las lecturas contra la versión
publicada. Cuando la versión publicada no tiene ninguna métrica que el ejemplo pueda usar, el
comando envía en su lugar una lectura no declarada, que se almacena siempre que su valor sea un
número. Se incluyen como máximo ocho métricas.

Los comandos están escritos para un shell POSIX como bash o zsh. En Windows, ejecútalos en WSL o
Git Bash: PowerShell y `cmd.exe` interpretan de otra forma las comillas y los saltos de línea, así
que el comando no enviaría lo que muestra.

La consola no puede conocer algunos valores, así que el comando deja un marcador para cada uno:

| Marcador | Sustitúyelo por |
| --- | --- |
| `<instance-id>` | El id de instancia elegido al instalar la instancia. Es el espacio de nombres de Kubernetes de la instancia sin el prefijo `dci-`. |
| `<mqtt-host>` | El host en el que se expone el listener MQTT. El listener usa TLS, así que guarda el certificado de la CA de la instancia como `instance-ca.crt`. |
| `<ingest-host>` | El host del listener de ingesta HTTP, puerto 8081. Una instalación predeterminada no lo expone fuera del clúster, salvo el webhook de integración entrante en `/api/integrations/{tenant}/{integration}` de la dirección HTTPS de la instancia; consulta [Tu primer dispositivo](../quickstart/first-device.md#5-open-a-path-to-the-ingest-endpoint). |
| `<password>` | La contraseña de una credencial `MQTT_BASIC`. Solo se muestra una vez, al establecerla. Si la has perdido, establece una nueva en la pestaña **Credenciales**. |

La pestaña no ofrece ningún comando cuando el comando no podría funcionar, y en su lugar indica
qué corregir:

- El dispositivo está **deshabilitado**. Los eventos de un dispositivo deshabilitado se descartan
  al llegar.
- El dispositivo no tiene ninguna credencial habilitada y sin caducar. Por defecto, la plataforma
  exige una credencial en cada conexión y en cada evento. Añade una primero en la pestaña
  **Credenciales**.

Un dispositivo que no está asignado a nada puede enviar datos; sus eventos se almacenan.

Como **Credenciales**, la pestaña requiere permiso para modificar dispositivos, porque el comando
incluye el token de acceso.

Un `202` de HTTP, o un acuse de recibo MQTT, significa que el evento se recibió, no que se
almacenó. Búscalo en la pestaña **Eventos**.

Consulta [Conectar un dispositivo](../guides/connecting-a-device.md).

### Sustitución

**Sustitución** sirve para cambiar el hardware: una unidad falló y otra ocupa su lugar. El
dispositivo conserva su identidad, su historial, sus alarmas, sus asignaciones y su pertenencia a
grupos. Solo cambian las credenciales.

<GuideShot id="device-detail-replacement" alt="La pestaña Sustitución de Boiler room thermostat. Una explicación indica que se vincula una unidad física nueva al dispositivo y que todo se conserva salvo las credenciales. El campo Motivo contiene 'Display unreadable after a power surge' y el campo Identificador de la unidad nueva contiene SN-5180-C, sobre un botón Sustituir unidad. Debajo, una tabla de historial muestra una sustitución anterior, con la hora oculta en esta captura: hecha por dana.okafor@acme-properties.example, con el motivo 'Failed sensor, swapped for the stockroom spare', unidad nueva SN-4471-B, 1 credencial retirada." />

1. Escribe el **Motivo**. Queda registrado en el historial de mantenimiento.
2. Escribe el **Identificador de la unidad nueva**: el número de serie o el IMEI del hardware
   nuevo. Queda registrado en el historial. El ID externo del dispositivo no cambia.
3. Elige **Sustituir unidad** y confirma.

Todas las credenciales que podía usar la unidad anterior se deshabilitan, y se emite una
credencial nueva para la unidad nueva. La credencial nueva se muestra una sola vez, así que cópiala
antes de elegir **Listo**. No se puede recuperar desde el historial.

La tabla de abajo enumera las sustituciones anteriores: cuándo, quién, por qué, la unidad nueva y
cuántas credenciales se retiraron.

## Relacionado

- [Tipos y perfiles de dispositivo](./device-types-and-profiles.md)
- [Modelo de dominio](../concepts/domain-model.md)
- [Presencia de dispositivo](../concepts/device-presence.md)
- [Comandos](../concepts/commands.md)
- [Importar dispositivos desde CSV](../guides/importing-devices.md)
- [Credenciales de dispositivo](../guides/device-credentials.md)
- [Gestión de asignaciones de dispositivos](../guides/managing-assignments.md)
- [Conexión de un dispositivo](../guides/connecting-a-device.md)
- [Integraciones de entrada](../guides/inbound-integrations.md)
