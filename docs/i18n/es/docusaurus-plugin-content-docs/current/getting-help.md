---
sidebar_position: 6
title: Cómo obtener ayuda
---

# Cómo obtener ayuda

DeviceChain es anterior a la versión 1.0 y se desarrolla de forma abierta. Si algo no funcionó, o
una página no te dijo lo que necesitabas, cuéntanoslo. Un informe temprano e incompleto vale más
que uno pulido y tardío, porque las interfaces aún pueden cambiar en respuesta a lo que la gente
se encuentra.

## Adónde acudir

| Lo que tienes | Adónde va |
| --- | --- |
| Una pregunta, o algo que te resultó confuso | [Discusiones](https://github.com/devicechain-io/devicechain/discussions) |
| Una idea o solicitud de funcionalidad | [Discusiones → Ideas](https://github.com/devicechain-io/devicechain/discussions/categories/ideas) |
| Algo no funciona | [Abre una incidencia](https://github.com/devicechain-io/devicechain/issues/new/choose) |
| Una vulnerabilidad de seguridad | Escribe a **admin@devicechain.io** — por favor, no la publiques |

No necesitas estar seguro de que es un error para comentarlo. Si no pudiste saber si el
comportamiento que viste era el previsto, esa ambigüedad ya merece reportarse.

## Cómo redactar una buena incidencia

Los formularios de incidencia piden lo que, de otro modo, tendríamos que volver a solicitarte. Dos
respuestas son las que más tiempo ahorran:

- **Qué versión.** Ejecuta `dcctl version`, o indica la etiqueta del chart o de la imagen que
  instalaste.
- **Dónde se detuvo.** No la historia completa: el último paso que funcionó y el primero que no.
  Si los datos nunca llegan, indica si el dispositivo se conectó, si se registraron eventos y si la
  consola mostró algo, en ese orden.

En los problemas de instalación, el entorno importa más de lo habitual. La puesta en marcha es la
parte que menos podemos reproducir, porque solo tenemos nuestras propias máquinas. Incluye:

- tu distribución y versión de Kubernetes
- el sistema operativo y la arquitectura de CPU del host
- la salida de `kubectl get pods -A`

`dcctl preflight` detecta por sí solo muchos problemas de entorno. Pega su salida incluso cuando no
reporte fallos.

:::caution Depura los datos antes de pegarlos
Los registros y la salida de los comandos pueden contener tokens, cadenas de conexión y nombres de
host internos. Las incidencias y las discusiones son públicas.
:::

## Telemetría que falta: tres causas comunes {#things-that-silently-swallow-data}

Si falta telemetría, estas tres causas explican la mayoría de los informes. Descartarlas primero
suele ser más rápido que esperar una respuesta.

- **El token del dispositivo no está registrado, o su credencial es rechazada.** Que el dispositivo
  esté sin asignar *no* es el problema: los eventos de un dispositivo sin asignar se almacenan y se
  proyectan, solo que no llevan ningún cliente, área o activo al que atribuirse. El problema es un
  token que la plataforma no conoce, o una credencial que rechaza. El transporte ya ha respondido
  cuando el evento se resuelve, así que el rechazo nunca llega al emisor. El evento se envía a la
  cola de mensajes no entregados (dead letter) y se registra con nivel de advertencia en
  device-management.
- **La medición no está en el perfil, y su valor no es un número.** Las mediciones se almacenan
  como números, así que:
  - Una medición sin declarar con valor numérico se almacena tal cual, sin ninguna advertencia.
  - Una medición sin declarar con valor no numérico se descarta —solo esa entrada— con una
    advertencia en los registros de device-management.
  - Una medición *declarada* cuyo valor no coincide con su tipo declarado es peor: el evento
    completo se envía a la cola de mensajes no entregados.

  Declarar la métrica en el perfil, con el tipo de dato correcto, resuelve ambos casos.
- **Estás consultando un inquilino distinto** de aquel al que reporta el dispositivo.

## Qué esperar

DeviceChain lo mantiene un equipo pequeño, así que una respuesta puede tardar unos días. Una
incidencia que pasa un tiempo sin respuesta no ha sido ignorada. Los informes que incluyen una
versión y un punto de detención claro se resuelven antes, porque nadie necesita una ida y vuelta
antes de empezar a revisarlos.
