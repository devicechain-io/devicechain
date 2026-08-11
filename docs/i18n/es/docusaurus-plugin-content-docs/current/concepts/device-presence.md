---
title: Presencia de dispositivo
---

# Presencia de dispositivo

DeviceChain mantiene una señal de **presencia** en vivo para cada dispositivo — si está actualmente en línea, y cuándo se conectó, desconectó o reportó actividad por última vez. La presencia forma parte del [último estado conocido](./architecture.md) de un dispositivo (la misma proyección que contiene sus mediciones más recientes), y aparece en la pestaña **Connectivity** del dispositivo en la consola.

Lo importante es entender *cómo* DeviceChain decide que un dispositivo está en línea, porque depende del transporte.

## Dos formas de conocer la presencia

Todo dispositivo lleva una **fuente de presencia** (presence source) que indica cómo se determina su estado en línea/fuera de línea:

- **Inferida** (predeterminada) — DeviceChain no tiene una señal explícita de conexión/desconexión del transporte, por lo que infiere la presencia a partir de la **actividad**. Un dispositivo se considera en línea mientras esté enviando datos; si permanece en silencio más tiempo que su **tiempo de espera de inactividad** (inactivity timeout), un barrido en segundo plano lo marca como fuera de línea. Este es el modelo correcto para transportes sin conexión (HTTP simple, CoAP) y para clientes MQTT sencillos que no se anuncian a sí mismos.

- **Afirmada** (asserted) — el transporte le indica a DeviceChain *explícitamente* cuándo un dispositivo se conecta y se desconecta, por lo que la presencia es **autoritativa** en lugar de deducida. La primera vez que llega una señal de este tipo para un dispositivo, DeviceChain cambia ese dispositivo a la fuente afirmada y, a partir de entonces:
  - su estado en línea/fuera de línea se rige **únicamente** por señales explícitas de conexión/desconexión — un paquete de datos aislado nunca puede marcar como en línea a un dispositivo que la plataforma ha registrado como fuera de línea;
  - el barrido de inactividad lo deja en paz — un dispositivo afirmado que queda en silencio *no* se asume muerto, porque el silencio no es evidencia de muerte en un transporte cuyo cometido es justamente reportar la muerte de forma explícita. Mezclar ambos modos permitiría marcar como fuera de línea a un dispositivo que reporta en un intervalo largo mientras la plataforma ha sido informada de que está conectado.

Un dispositivo permanece **inferido** hasta que un transporte que afirma presencia produce una señal para él, por lo que nada cambia para los dispositivos existentes a menos que comiencen a llegar a través de un transporte que afirme presencia. Hoy dos transportes afirman presencia: [Sparkplug-B](./sparkplug.md), cuyos mensajes BIRTH y DEATH son exactamente estas señales explícitas de conexión/desconexión, y [LwM2M](./lwm2m.md), cuyo ciclo de vida de registro — registro, actualización periódica y baja de registro (o un tiempo de vida vencido) — hace lo mismo.

La consecuencia de esa omisión es deliberada, y conviene entenderla antes de depender de ella: **un dispositivo afirmado no tiene una red de seguridad por inactividad.** Su señal de desconexión solo puede venir del transporte, así que si esa señal nunca llega — un certificado de muerte (death) de Sparkplug perdido junto con la conexión, o un dispositivo LwM2M cuyo tiempo de vida de registro aún no ha vencido (el valor predeterminado del propio LwM2M es de 86400 segundos, un día completo) — el dispositivo sigue apareciendo en línea sin nada que lo corrija. Qué vigilar, y cómo acotar esa ventana, está en **[Cómo operar los servicios de borde](../deployment/edge-services.md)**.

La *fuente* de presencia en sí no se expone todavía en ninguna parte. Tanto la pestaña Connectivity de la consola como las herramientas de dispositivo de [MCP](./mcp.md) muestran el estado en línea/fuera de línea resultante y las horas de última conexión, desconexión y actividad — pero no si ese estado fue afirmado por el transporte o inferido a partir de un tiempo de espera. La distinción está viva en la plataforma e impulsa el comportamiento descrito aquí; simplemente no es algo que hoy se pueda leer en una pantalla.

## Por qué importa la distinción

La presencia inferida es conveniente pero lenta y ambigua: "fuera de línea" solo significa "no ha hablado recientemente", lo cual es lento para detectar una desconexión real y ciego para dispositivos que reportan en un intervalo largo. La presencia afirmada es inmediata e inequívoca — una desconexión es una desconexión en el instante en que el transporte la reporta — que es lo que se desea para cualquier cosa sobre la que se vaya a alarmar o actuar.

Mantener los dos modos como una marca explícita por dispositivo significa que un dispositivo en un transporte sin conexión conserva su comportamiento de tiempo de espera habitual, mientras que un dispositivo en un transporte consciente de la presencia obtiene la señal autoritativa, y ambos nunca interfieren entre sí.

:::note Estado
La presencia de dispositivo — tanto inferida como afirmada — está disponible, con [Sparkplug-B](./sparkplug.md) y [LwM2M](./lwm2m.md) como transportes que afirman presencia. Una **regla de detección puede dispararse directamente sobre un borde de conexión/desconexión**: la [condición de Connectivity](./event-processing.md#condition-types) genera una alarma en el instante en que llega una desconexión autoritativa y la resuelve al reconectar — sin tiempo de espera que ajustar. El motor ya la evalúa hoy, pero ninguna superficie de autoría de la consola la ofrece todavía — el generador de formularios y el lienzo de automatización omiten ambos ese tipo de condición, de modo que una regla de conectividad se define enviando la regla directamente a la API. **No abra ninguna en el editor de formulario de la consola**: no reconoce el tipo, así que lee la regla como una regla de umbral y al guardar reemplaza la definición original, sin aviso alguno. (El lienzo la rechaza correctamente, nombrando el tipo no soportado.) Complementa la regla de Absence basada en tiempo de espera (muerte autoritativa frente a silencio inferido), y ambas están pensadas para usarse en conjunto. Una desconexión autoritativa también actualiza el estado en vivo del dispositivo, de modo que la pestaña Connectivity muestra el dispositivo fuera de línea en el instante en que el transporte lo reporta.
:::

## Cómo se opera

La presencia vale lo que vale la señal que hay detrás, y los dos transportes que la afirman se
ejecutan cada uno como una única réplica propietaria — lo que le da a la presencia algunas
propiedades operativas que conviene conocer antes de alarmar sobre ella: qué cuesta un relevo, por
qué un dispositivo afirmado puede quedarse mostrando en línea, y cómo acotar eso. Todo ello se
cubre en **[Cómo operar los servicios de borde](../deployment/edge-services.md)**.
