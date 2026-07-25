---
sidebar_position: 14
title: Ingesta de Sparkplug-B
---

# Ingesta de Sparkplug-B

Muchas flotas industriales y de automatización de edificios ya publican
telemetría como [**Eclipse Sparkplug B**](https://sparkplug.eclipse.org/) sobre
un broker MQTT que ellas mismas operan. DeviceChain puede ingerir directamente
desde esas redes sin pedirles a los dispositivos que cambien nada: se une a tu
entorno Sparkplug como una **Host Application** y traduce el tráfico de borde
(edge) a los mismos eventos que produce cualquier otro transporte.

A diferencia de MQTT simple —donde DeviceChain *es* el broker— la ingesta de
Sparkplug funciona al revés: DeviceChain **se conecta hacia afuera, a tu
broker**, como cliente, se suscribe a los grupos de Sparkplug que configures, y
sigue el protocolo de sesión de Sparkplug.

## Qué hace

- **Se anuncia como una Host Application.** DeviceChain publica el mensaje
  `STATE` de Sparkplug para que los nodos de borde sepan que hay un consumidor
  en línea, y sigue el protocolo de negociación de nacimiento/muerte
  (birth/death) para saber en todo momento qué nodos y dispositivos están
  activos.

- **Sigue la sesión de Sparkplug.** Sparkplug es un protocolo con estado: los
  nodos de borde envían un certificado **BIRTH** que define sus métricas (y
  alias compactos), luego un flujo de mensajes **DATA** que los referencian, y
  un **DEATH** cuando quedan fuera de línea. DeviceChain ejecuta la máquina de
  sesión completa: rastrea los alias y la secuencia de mensajes de cada nodo,
  detecta un salto o un nacimiento (birth) perdido, y le pide al nodo que se
  vuelva a anunciar (un *rebirth*) cuando necesita resincronizarse —de modo que
  un mensaje descartado nunca corrompe silenciosamente lo que decodifica.

- **Asigna identidades de borde a dispositivos.** Cada `{group}/{node}` de
  Sparkplug (o `{group}/{node}/{device}` para un dispositivo bajo un nodo) se
  convierte en el [`externalId`](./domain-model.md) de un dispositivo de
  DeviceChain. Si habilitas el autorregistro para una fuente, se crea un
  dispositivo automáticamente la primera vez que se lo ve; en caso contrario,
  las identidades desconocidas se descartan y se contabilizan, así que
  mantienes el control de qué entra en tu registro.

- **Produce presencia autoritativa.** Un BIRTH de nodo o dispositivo marca al
  dispositivo correspondiente como **en línea**, y un DEATH lo marca como
  **fuera de línea** —inmediata y explícitamente. Esto hace de Sparkplug el
  primer transporte que impulsa la [**presencia de dispositivo asertada**](./device-presence.md):
  el estado en línea de un dispositivo Sparkplug es autoritativo, no inferido
  a partir de un tiempo de espera (timeout).

- **Alimenta la misma canalización.** Las mediciones decodificadas y los
  cambios de presencia fluyen hacia la canalización normal de
  decodificar → resolver → persistir, así que todo lo que viene después
  —historial, estado en vivo, paneles y el motor de detección— trata la
  telemetría de Sparkplug exactamente igual que cualquier otro transporte.

## Multitenencia y configuración

Cada **fuente** de Sparkplug se configura para un inquilino: la URL del broker,
las credenciales (suministradas como un secreto proyectado, nunca en
configuración en texto plano), y los grupos a los que suscribirse. Todo mensaje
que llega por una fuente se atribuye *al inquilino de esa fuente* —el inquilino
queda fijado por qué broker recibió el mensaje, nunca leído del tópico de
Sparkplug— así que la red de borde de un inquilino nunca puede confundirse con
la de otro.

## Alta disponibilidad

Solo **una** réplica del servicio de Sparkplug se conecta a un broker dado a la
vez, elegida mediante un arrendamiento (lease). Una segunda réplica queda en
espera y toma el control si la líder falla; al tomar el control, restablece la
sesión (pidiendo a los nodos que se vuelvan a anunciar) y reconcilia la
presencia de los dispositivos, de modo que una desconexión ocurrida durante el
traspaso no se pierde y ningún dispositivo queda mostrado incorrectamente como
en línea.

:::note Estado
La ingesta de Sparkplug-B está disponible como un servicio opcional (opt-in).
Ingiere mediciones e impulsa la [presencia de dispositivo](./device-presence.md)
autoritativa; se conecta a un broker por TLS o texto plano según la URL
configurada. Un segundo protocolo de borde nativo de estándares,
[LwM2M](./lwm2m.md), también está disponible. El CA personalizado / mTLS hacia
un broker privado está planificado.
:::
