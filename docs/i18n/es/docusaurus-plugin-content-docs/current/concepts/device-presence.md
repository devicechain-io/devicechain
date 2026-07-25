---
sidebar_position: 13
title: Presencia de dispositivo
---

# Presencia de dispositivo

DeviceChain mantiene una señal de **presencia** en vivo para cada dispositivo — si está actualmente en línea, y cuándo se conectó, desconectó o reportó actividad por última vez. La presencia forma parte del [último estado conocido](./architecture.md) de un dispositivo (la misma proyección que contiene sus mediciones y ubicación más recientes), y aparece en la pestaña **Connectivity** del dispositivo en la consola.

Lo importante es entender *cómo* DeviceChain decide que un dispositivo está en línea, porque depende del transporte.

## Dos formas de conocer la presencia

Todo dispositivo lleva una **fuente de presencia** (presence source) que indica cómo se determina su estado en línea/fuera de línea:

- **Inferida** (predeterminada) — DeviceChain no tiene una señal explícita de conexión/desconexión del transporte, por lo que infiere la presencia a partir de la **actividad**. Un dispositivo se considera en línea mientras esté enviando datos; si permanece en silencio más tiempo que su **tiempo de espera de inactividad** (inactivity timeout), un barrido en segundo plano lo marca como fuera de línea. Este es el modelo correcto para transportes sin conexión (HTTP simple, CoAP) y para clientes MQTT sencillos que no se anuncian a sí mismos.

- **Afirmada** (asserted) — el transporte le indica a DeviceChain *explícitamente* cuándo un dispositivo se conecta y se desconecta, por lo que la presencia es **autoritativa** en lugar de deducida. La primera vez que llega una señal de este tipo para un dispositivo, DeviceChain cambia ese dispositivo a la fuente afirmada y, a partir de entonces:
  - su estado en línea/fuera de línea se rige **únicamente** por señales explícitas de conexión/desconexión — un paquete de datos aislado nunca puede marcar como en línea a un dispositivo que la plataforma ha registrado como fuera de línea;
  - el barrido de inactividad lo deja en paz — un dispositivo afirmado que queda en silencio *no* se asume muerto, porque si hubiera fallado, el transporte lo habría indicado.

Un dispositivo permanece **inferido** hasta que un transporte que afirma presencia produce una señal para él, por lo que nada cambia para los dispositivos existentes a menos que comiencen a llegar a través de un transporte que afirme presencia. Hoy dos transportes afirman presencia: [Sparkplug-B](./sparkplug.md), cuyos mensajes BIRTH y DEATH son exactamente estas señales explícitas de conexión/desconexión, y [LwM2M](./lwm2m.md), cuyo ciclo de vida de registro — registro, actualización periódica y baja de registro (o un tiempo de vida vencido) — hace lo mismo.

## Por qué importa la distinción

La presencia inferida es conveniente pero lenta y ambigua: "fuera de línea" solo significa "no ha hablado recientemente", lo cual es lento para detectar una desconexión real y ciego para dispositivos que reportan en un intervalo largo. La presencia afirmada es inmediata e inequívoca — una desconexión es una desconexión en el instante en que el transporte la reporta — que es lo que se desea para cualquier cosa sobre la que se vaya a alarmar o actuar.

Mantener los dos modos como una marca explícita por dispositivo significa que un dispositivo en un transporte sin conexión conserva su comportamiento de tiempo de espera habitual, mientras que un dispositivo en un transporte consciente de la presencia obtiene la señal autoritativa, y ambos nunca interfieren entre sí.

:::note Estado
La presencia de dispositivo — tanto inferida como afirmada — está disponible, con [Sparkplug-B](./sparkplug.md) y [LwM2M](./lwm2m.md) como transportes que afirman presencia. Se puede **redactar una regla de detección directamente sobre un borde de conexión/desconexión**: la [condición de Connectivity](./event-processing.md#condition-types) genera una alarma en el instante en que llega una desconexión autoritativa y la resuelve al reconectar — sin tiempo de espera que ajustar. Complementa la regla de Absence basada en tiempo de espera (muerte autoritativa frente a silencio inferido), y ambas están pensadas para usarse en conjunto. Una desconexión autoritativa también actualiza el estado en vivo del dispositivo, visible en la pestaña Connectivity.
:::
