---
sidebar_position: 6
title: Cómo obtener ayuda
---

# Cómo obtener ayuda

DeviceChain es anterior a la versión 1.0 y se desarrolla de forma abierta. Si algo no
funcionó, o si una página no le indicó lo que necesitaba, queremos saberlo — un informe
temprano e incompleto vale más que uno pulido y tardío, porque las interfaces aún pueden
cambiar en respuesta a lo que los usuarios encuentran.

## Adónde acudir

| Lo que tiene | Adónde va |
| --- | --- |
| Una pregunta, o algo que le resultó confuso | [Discusiones](https://github.com/devicechain-io/devicechain/discussions) |
| Una idea o solicitud de funcionalidad | [Discusiones → Ideas](https://github.com/devicechain-io/devicechain/discussions/categories/ideas) |
| Algo no funciona | [Abra una incidencia](https://github.com/devicechain-io/devicechain/issues/new/choose) |
| Una vulnerabilidad de seguridad | Escriba a **admin@devicechain.io** — por favor no la publique |

No necesita estar seguro de que se trata de un error antes de comentarlo. Si no pudo
determinar si el comportamiento que observó era el previsto, esa ambigüedad ya merece
ser reportada.

## Cómo redactar una buena incidencia

Los formularios de incidencia piden justamente aquello que de otro modo tendríamos que
solicitarle después. Los dos que más tiempo ahorran:

**Qué versión.** Ejecute `dcctl version`, o indique la etiqueta del chart o de la imagen
que instaló.

**Dónde se detuvo.** No hace falta la historia completa — basta con el último paso que
funcionó y el primero que no. Cuando los datos nunca llegan, eso significa indicar si el
dispositivo se conectó, si se registraron eventos y si la consola mostró algo, en ese
orden.

En los problemas de instalación el entorno importa más de lo habitual, porque la puesta
en marcha es la parte que menos podemos reproducir — solo disponemos de nuestras propias
máquinas. Incluya su distribución y versión de Kubernetes, el sistema operativo y la
arquitectura de CPU del host, y la salida de `kubectl get pods -A`. `dcctl preflight`
detecta por sí solo muchos problemas de entorno, y vale la pena pegar su salida incluso
cuando no reporta fallos.

:::caution Depure los datos antes de pegarlos
Los registros y la salida de los comandos pueden contener tokens, cadenas de conexión y
nombres de host internos. Las incidencias y las discusiones son públicas.
:::

## Cosas que descartan datos silenciosamente

Si faltan datos de telemetría, estas tres causas explican la mayoría de los informes, y
descartarlas primero suele ser más rápido que esperar una respuesta:

- **El dispositivo no está asignado.** Los eventos de un dispositivo sin asignar se
  descartan sin que aparezca ningún error visible para el emisor.
- **La medición no está en el perfil.** El perfil de un tipo de dispositivo declara las
  mediciones que la plataforma acepta; cualquier otra se descarta durante la
  decodificación.
- **Está consultando un inquilino distinto** de aquel al que reporta el dispositivo.

## Qué esperar

DeviceChain lo mantiene un equipo pequeño, por lo que una respuesta puede tardar unos
días — una incidencia que permanece sin responder un tiempo no ha sido ignorada. Los
informes que incluyen una versión y un punto de detención claro se resuelven más rápido,
porque no requieren ninguna ida y vuelta antes de que alguien pueda empezar a revisarlos.
