---
title: Reglas de detección
sidebar_position: 5
---

import GuideShot from '@site/src/components/GuideShot';

# Reglas de detección

Una regla de detección vigila la telemetría de un perfil de dispositivo y actúa cuando ocurre algo:
una lectura supera un límite, un dispositivo deja de informar o un valor sube demasiado rápido.
Cuando una regla se dispara puede generar una alarma, enviar un comando al dispositivo, llamar a un
webhook o publicar en un conector.

Este capítulo es para quien escribe o mantiene reglas. Cubre la lista de reglas, las tres formas de
crear una regla (el formulario, el lienzo y una descripción en lenguaje natural), la vista previa de
una regla contra el historial registrado y la pestaña **Estado de reglas**, que indica si las reglas
publicadas se están disparando. Para conocer a fondo los tipos de regla y las acciones, consulta
[Procesamiento de eventos y alarmas](../concepts/event-processing.md).

:::note Las reglas solo se ejecutan después de publicar el perfil
Las reglas pertenecen a un perfil de dispositivo, y lo que editas es el borrador del perfil. Una
regla nueva o modificada llega a la telemetría en vivo solo cuando publicas el perfil desde su
pestaña **Versiones**, que se trata en el capítulo *Tipos de dispositivo y perfiles*.
:::

## La lista de reglas

Abre un perfil de dispositivo desde **Dispositivos → Perfiles de dispositivo** y elige la pestaña
**Reglas de detección**. La lista muestra el nombre de cada regla, su tipo y si está habilitada.
**Editar** abre una regla, y el icono de la papelera la elimina tras pedirte confirmación. **Nueva
regla de detección** crea una nueva.

<GuideShot id="rules-list" alt="La pestaña Reglas de detección del perfil Building Pulse Thermostat Profile, con dos reglas de umbral habilitadas, Early heat warning y Building Pulse over-temperature, cada una con las acciones Editar y eliminar, y un botón Nueva regla de detección." />

Una regla deshabilitada se conserva con el perfil pero no se ejecuta. Úsalo para apartar una regla
en lugar de eliminarla.

## Cómo crear una regla

**Nueva regla de detección** abre un panel con tres modos en la parte superior: **Formulario**,
**Lienzo** y **Describir**. Los tres producen el mismo tipo de regla, y el mismo compilador la
comprueba. Al editar una regla existente solo tienes **Formulario** y **Lienzo**; **Describir** es
para reglas nuevas. Una regla creada en el lienzo se abre en el lienzo cuando la editas.

## Crear una regla con el formulario

El formulario es la forma más rápida de escribir una regla sencilla. Rellenas un nombre, una
descripción opcional, un token (el identificador permanente de la regla), el **Tipo de regla** y una
**Severidad**, y después los campos que necesita ese tipo de regla.

<GuideShot id="rule-form-new" alt="El panel Nueva regla de detección en modo Formulario. Arriba, un recuadro Empezar desde un preajuste ofrece Dispositivo en silencio, Por encima del umbral durante un tiempo y Batería baja. Debajo están los campos Nombre, Descripción y Token, Tipo de regla en Umbral, Severidad en ninguna, una casilla Habilitada, una casilla Limitar el ámbito a un grupo de entidades y la sección Condición." />

Las partes del formulario, de arriba abajo:

- **Empezar desde un preajuste** rellena el formulario con una alerta habitual. Se describe en la
  sección siguiente.
- **Tipo de regla** decide qué detecta la regla: Umbral, Duración, Ausencia, Conectividad, Agregado
  por ventana, Tasa de cambio, Repetición o Correlación de áreas. Cada tipo muestra sus propios
  campos. La sección de [tipos de condición](../concepts/event-processing.md#condition-types)
  explica cada uno.
- **Severidad** es opcional salvo que la regla genere una alarma, en cuyo caso la alarma la lleva.
- **Habilitada** activa o desactiva la regla sin eliminarla.
- **Limitar el ámbito a un grupo de entidades** restringe la regla a los miembros de una versión
  publicada de un grupo dinámico. Las reglas de ausencia y de correlación no admiten este límite.
- **Condición** es la comparación con la que se dispara la regla: una comparación estructurada de una
  métrica con un valor o con un atributo del dispositivo, una expresión CEL avanzada o, en algunos
  tipos, **Coincidir con todos los eventos**. Las reglas de ausencia y de conectividad no tienen
  condición.
- **Acciones** es lo que ocurre cuando la regla se dispara: **Generar alarma**, **Enviar comando**,
  **Llamar a un webhook** o **Publicar en un conector**. Una regla sin acciones no genera ninguna
  alarma. Las reglas de correlación de áreas no admiten acciones. Una acción **Enviar comando** admite además un
  campo opcional **Caduca tras** (por ejemplo `15m` o `2h`, de un minuto a siete días). Acota cuánto
  tiempo puede esperar el comando al dispositivo, contado desde el momento en que se dispara la regla;
  déjalo vacío para conservar el valor predeterminado de la plataforma, siete días. Un valor fuera de
  ese rango, o que no sea una duración, se rechaza al guardar la regla.

Mientras escribes, el formulario pregunta al compilador si la regla es válida y muestra la respuesta
bajo los campos, por ejemplo **La regla compila. ✓** o el error del compilador. Debajo aparecen un
panel **Vista previa contra el historial** (consulta
[Vista previa contra el historial](#previewing-against-history)) y, si la regla genera una alarma,
una comprobación de **Notificaciones**. **Crear regla de detección** guarda la regla en el borrador
del perfil.

### Empezar desde un preajuste

La fila de preajustes ofrece tres alertas que casi cualquier flota necesita:

- **Dispositivo en silencio** genera una alarma cuando un dispositivo del perfil deja de informar
  durante 15 minutos.
- **Por encima del umbral durante un tiempo** genera una alarma cuando una métrica se mantiene por
  encima de un límite durante 10 minutos. Tú eliges la métrica y el límite.
- **Batería baja** genera una alarma cuando el nivel de batería se mantiene por debajo del 20 %
  durante 10 minutos.

Elegir un preajuste rellena el formulario. No se guarda nada hasta que creas la regla, y todos los
campos siguen siendo editables. En los dos preajustes que vigilan una métrica, el formulario te pide
que la compruebes antes de crear la regla. La
[tabla de preajustes](../concepts/event-processing.md#start-from-a-preset) recoge los valores
exactos que rellena cada uno.

### ¿Se notificará a alguien?

Cuando la regla genera una alarma con una severidad, el panel **Notificaciones** comprueba si alguna
política de notificación habilitada envía las alarmas de esa severidad por un canal operativo.
Responde *Cubierto* o *Sin cubrir*.

Las políticas enrutan las alarmas por severidad, no por regla. Si no hay cobertura, **Crear una
política para** *severidad* añade una en el momento: eliges un canal y, para el correo, los
destinatarios. Esa política enviará entonces todas las alarmas de esa severidad del inquilino, de
cualquier regla. Necesitas antes un canal de entrega; consulta
[Configuración de canales de notificación](../guides/notification-channels.md).

El botón solo aparece si puedes gestionar políticas de notificación. La comprobación es orientativa.
Nunca te impide guardar la regla.

## Crear una regla en el lienzo

El lienzo muestra una regla como un pequeño flujo: una **Fuente** (la telemetría del perfil)
conectada a una condición, y la condición conectada a una o varias acciones. Es útil para reglas que
se bifurcan o reutilizan un valor calculado.

<GuideShot id="rule-canvas" alt="El panel Editar regla de detección en modo Lienzo para la regla bp-early-heat. Una paleta ofrece Fuente, Umbral, Duración, Ausencia, Conectividad, Agregado por ventana, Tasa de cambio, Repetición, Correlación de áreas, Cálculo, Bifurcación y Acción. El lienzo muestra un nodo Fuente del perfil conectado a un nodo Umbral, Early heat warning, temperature > 29, conectado a un nodo Acción que genera la alarma bp-early-heat. Junto a Guardar cambios aparece la indicación Compila · costo 5." />

Cómo trabajar en el lienzo:

- Añade nodos desde la paleta de la fila **Añadir:**. Hay un nodo para cada tipo de regla, además de
  **Cálculo** (dar nombre a un valor derivado), **Bifurcación** (enviar un disparo a acciones
  distintas según una condición) y **Acción**.
- Conecta una fuente a una condición, y la condición a una acción.
- Selecciona un nodo para editarlo en el panel de la derecha.
- Fíjate en la indicación junto al botón de guardar. **Compila** significa que la regla es válida; la
  cifra de costo es la estimación del compilador de cuánto cuesta evaluar la regla.

El formulario y el lienzo editan la misma regla. Una condición de guarda creada en el lienzo se
muestra, y se conserva, cuando abres la regla en el formulario. Si una regla guardada tiene ajustes
que el lienzo no puede mostrar, el lienzo lo avisa y desactiva el guardado en lugar de descartarlos.

## Describir una regla en lenguaje natural

**Describir** te permite escribir lo que quieres, por ejemplo *Generar una advertencia cuando el
termostato del vestíbulo se mantenga por encima de 28 grados durante diez minutos*, y elegir
**Redactar regla**. Un modelo de IA redacta la regla, el compilador la comprueba y el borrador se abre
en el formulario para que lo revises y lo guardes. No se guarda nada hasta que tú lo hagas.

<GuideShot id="rule-describe" alt="El panel Nueva regla de detección en modo Describir. Se ha escrito una descripción en el cuadro de texto y se ha pulsado Redactar regla. Debajo, un aviso amarillo dice, en inglés: NL rule drafting is not enabled on this deployment." />

Describir necesita el servicio de IA de la instancia, y el inquilino debe tener permiso para usarlo.
Cuando no está disponible, como en la instancia de la captura, el panel muestra un aviso y puedes
seguir usando el formulario o el lienzo. El aviso lo envía el servidor, por eso aparece en inglés.
Si un borrador no compila tras varios intentos, la consola lo indica y muestra lo que intentó la IA,
para que puedas afinar la descripción. Consulta
[Autoría asistida por IA](../concepts/ai-authoring.md) para saber cómo funciona la redacción y
quién la habilita.

## Vista previa contra el historial {#previewing-against-history}

Antes de publicar una regla puedes ver qué habría hecho. **Vista previa contra el historial**
reproduce la telemetría registrada del perfil a través de la regla y enumera las alarmas que habría
generado y resuelto. No se publica nada y no se crea ninguna alarma.

<GuideShot id="rule-replay-preview" alt="El panel Vista previa contra el historial bajo el lienzo, con la ventana de las últimas 24 h seleccionada. Indica 157 disparos en las últimas 24 horas, 1620 eventos escaneados y 75 ms, un aviso de que el historial anterior al evento más antiguo conservado no está disponible y una lista de filas GENERAR y RESOLVER para los termostatos bp-therm-003 a bp-therm-007 con sus horas." />

1. Elige una ventana: **Últimas 1 h**, **Últimas 6 h** o **Últimas 24 h**.
2. Elige **Ejecutar vista previa**.
3. Lee el resultado: cuántos disparos, cuántos eventos se analizaron y una lista de filas
   **GENERAR** y **RESOLVER**, cada una con el dispositivo y la hora.

En el lienzo, al seleccionar un disparo se traza su recorrido por los nodos, de modo que ves qué
condición coincidió y qué acción se ejecutó. El formulario tiene el mismo panel, sin el trazado. La
vista previa solo se ejecuta cuando la regla compila.

Si el perfil no conserva historial tan antiguo como la ventana, el panel lo indica y hace la vista
previa desde el evento más antiguo que tiene. Ese aviso también lo envía el servidor, así que aparece en
inglés. La vista previa es una guía, no una garantía: reproduce
desde un arranque en frío, así que una regla que espera un silencio largo se previsualiza con menos
precisión.

## Estado de reglas y detecciones en vivo

La pestaña **Estado de reglas** responde a «¿funciona mi regla?» para la versión activa publicada del
perfil.

<GuideShot id="rule-health" alt="La pestaña Estado de reglas del perfil Building Pulse Thermostat Profile. Una tabla muestra la regla Building Pulse over-temperature con estado Activa, sin último disparo y 0 disparos. Debajo, una sección Detecciones en vivo, marcada en vivo, dice: Esperando detecciones… los disparos aparecerán aquí a medida que se activen las reglas." />

- La tabla muestra cada regla publicada con su **Estado**, cuándo fue su **Último disparo** y
  cuántos **Disparos** acumula. **Activa** significa que la regla se está ejecutando. **Error de
  compilación** significa que la regla publicada no se pudo compilar, y el mensaje explica por qué.
- **Detecciones en vivo** muestra los disparos a medida que ocurren, cada uno marcado como generada o
  resuelta. El punto junto al título indica si la conexión está activa.

Aquí solo aparecen las reglas publicadas. En la captura, *Early heat warning* no figura en la tabla
porque solo existe en el borrador del perfil.

## Relacionado

- [Procesamiento de eventos y alarmas](../concepts/event-processing.md): tipos de regla, acciones y ciclo de vida de las alarmas
- [Autoría asistida por IA](../concepts/ai-authoring.md)
- [Configuración de canales de notificación](../guides/notification-channels.md)
- [Conectores de salida](../concepts/outbound-connectors.md), para las acciones de webhook y de publicación
