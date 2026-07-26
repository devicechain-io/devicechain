---
sidebar_position: 15
title: Ingesta LwM2M
---

# Ingesta LwM2M

Las flotas restringidas y celulares suelen hablar [**OMA LwM2M**](https://lwm2m.openmobilealliance.org/) — un estándar compacto de gestión de dispositivos sobre CoAP. DeviceChain termina LwM2M directamente: los dispositivos se conectan a él mediante **CoAP/UDP asegurado con DTLS**, y su registro, telemetría y firmware se asignan todos al mismo modelo de dispositivo único que usa cualquier otro transporte.

A diferencia de la ingesta Sparkplug — donde DeviceChain se conecta hacia *tu* broker —, los dispositivos LwM2M se conectan **hacia adentro**, al endpoint CoAP asegurado de DeviceChain, la misma forma que la ruta MQTT dorada (golden path).

## Qué hace

- **Autentica cada dispositivo en el handshake.** Un dispositivo presenta una **identidad de clave precompartida (PSK) DTLS**; DeviceChain resuelve esa identidad a un inquilino y un dispositivo antes de que fluya cualquier tráfico de aplicación. Una identidad desconocida o malformada hace fallar el handshake y nunca llega al registro — la pertenencia a un inquilino (tenancy) proviene de la identidad *autenticada*, nunca de nada que el dispositivo declare en un payload. Con el autorregistro habilitado para una credencial, la fila del dispositivo se crea la primera vez que se conecta una identidad aprovisionada. Los clientes itinerantes (un dispositivo cuya dirección de red cambia) se siguen mediante el Connection ID de DTLS, de modo que un dispositivo celular conserva su sesión a través de un cambio de IP.

- **Impulsa la presencia autoritativa a partir del ciclo de vida del registro.** Un **Register** de LwM2M marca el dispositivo como **en línea**, las **Updates** periódicas mantienen la sesión activa, y un **Deregister** — o un tiempo de vida de registro (registration lifetime) vencido — lo marca como **fuera de línea**. Al igual que [Sparkplug-B](./sparkplug.md), esto convierte a LwM2M en un transporte [**que afirma presencia**](./device-presence.md) (presence-asserting): el estado en línea de un dispositivo es autoritativo, no se infiere a partir de un tiempo de espera.

- **Convierte los recursos observados en mediciones.** DeviceChain **observa** (Observe) los recursos del dispositivo y decodifica cada **Notify** (SenML) en mediciones tipadas dentro del sobre (envelope) normal, de modo que la telemetría LwM2M llega al historial, al estado en vivo, a los paneles y al motor de detección exactamente igual que cualquier otra lectura.

- **Envía comandos y firmware hacia el dispositivo.** Los comandos de la plataforma se convierten en operaciones LwM2M de **Read / Write / Execute** sobre los recursos del dispositivo, y una actualización de firmware se descompone sobre esas mismas primitivas contra el objeto estándar Firmware Update. Un comando para un dispositivo que está actualmente dormido se **retiene de forma durable y se entrega en su próximo despertar** (modo cola), en lugar de descartarse — con un horizonte acotado para que un comando nunca espere indefinidamente.

- **Alimenta el mismo pipeline.** Las mediciones decodificadas y los cambios de presencia fluyen a través de la ruta normal de decodificación → resolución → persistencia, de modo que todo lo que sigue trata a los dispositivos LwM2M igual que a cualquier otro.

## Pertenencia a inquilino (tenancy) e identidad

Cada dispositivo se vincula a su inquilino mediante su **identidad PSK DTLS autenticada**, mapeada a un `(tenant, externalId)` en el momento de la conexión. Como la identidad se verifica durante el handshake, un dispositivo nunca puede presentar tráfico para otro inquilino, y la identidad en el cable es un identificador opaco en lugar de una cadena legible del tipo `tenant:device`.

## Alta disponibilidad

Una única réplica sirve el endpoint CoAP a la vez, sostenida por un arrendamiento (lease) de propiedad con vallado. Los dispositivos se conectan **hacia dentro** a un único socket UDP enlazado, así que esto no es una opción de ajuste: una segunda réplica compartiendo el Service recibiría — y descartaría — silenciosamente una parte de los datagramas. Solo quien mantiene el arrendamiento enlaza el socket; el despliegue se niega a renderizar más de una réplica.

Lo que aporta el arrendamiento es que **el reemplazo es seguro y automático**. Un pod de reemplazo no enlaza nada hasta haber adquirido el arrendamiento, de modo que nunca hay dos procesos enlazados al endpoint a la vez. Como un dispositivo en modo cola puede permanecer en silencio durante largos períodos por diseño, el nuevo líder reconstruye entonces la presencia a partir de la proyección durable y el tiempo de vida de registro de cada dispositivo en lugar de sondear — de modo que un relevo no marca falsamente como fuera de línea a dispositivos dormidos.

La recuperación es automática pero no instantánea: tarda lo que el pod de reemplazo necesite para planificarse y enlazar, más hasta 30 segundos de la ventana de vallado del arrendamiento. Los datagramas enviados durante esa ventana se pierden, lo que en LwM2M es normalmente invisible — los mensajes CoAP confirmables se retransmiten y los recursos observados se reanudan en la siguiente notificación.

:::note Estado
La ingesta LwM2M está disponible como servicio opcional (opt-in) sobre CoAP/UDP con DTLS-PSK. Impulsa la [presencia de dispositivo](./device-presence.md) autoritativa, ingiere los recursos observados como mediciones, y envía comandos Read/Write/Execute y actualizaciones de firmware en sentido descendente (downlink) (con retención y drenaje durables para dispositivos dormidos). El alcance de la disponibilidad general (GA) son las credenciales PSK (X.509 / clave pública sin procesar y un servidor Bootstrap están planeados).
:::
