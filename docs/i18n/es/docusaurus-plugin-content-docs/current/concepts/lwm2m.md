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

- **Convierte los objetos de sensor observados en mediciones.** DeviceChain **observa** (Observe) las *instancias de objeto de sensor* del dispositivo — aquellas cuyo id de objeto cae dentro del rango de IPSO Smart Objects configurado como telemetría, con un tope de 32 instancias por registro — y decodifica cada **Notify** en mediciones tipadas dentro del sobre (envelope) normal, de modo que la telemetría LwM2M llega al historial, al estado en vivo, a los paneles y al motor de detección exactamente igual que cualquier otra lectura. Los objetos de gestión (Security, Server, Device y el resto del conjunto OMA) nunca se observan.

  Hoy solo se decodifican las notificaciones en **SenML-JSON**, y eso tiene una consecuencia que conviene dimensionar de antemano: un cliente conforme **solo LwM2M 1.0** no puede producirlas — SenML llegó con LwM2M 1.1, así que un cliente 1.0 responde correctamente al Observe con `4.06 Not Acceptable`. Ese dispositivo sigue registrándose, impulsa la presencia y acepta comandos, pero no reporta **ninguna telemetría**. Esta es hoy la mayor brecha funcional del soporte de LwM2M; decodificar el formato TLV más antiguo es el trabajo pendiente que la cierra. Ambos rechazos se contabilizan, de modo que un operador puede verlo ocurrir en lugar de deducirlo a partir de datos ausentes.

- **Envía comandos y firmware hacia el dispositivo.** Los comandos de la plataforma se convierten en operaciones LwM2M de **Read / Write / Execute** sobre los recursos del dispositivo. Una actualización de firmware se impulsa de la misma manera, como una secuencia que ejecuta el operador y no como una única operación de la plataforma: eres tú quien emite los Write y el Execute contra el objeto estándar Firmware Update, como comandos ordinarios. DeviceChain mantiene los comandos de un mismo dispositivo en el orden en que los encolaste — un write de firmware y su execute nunca pueden reordenarse — pero no modela la actualización como un único trabajo gestionado. Un comando para un dispositivo que está actualmente dormido se **retiene de forma durable y se entrega en su próximo despertar** (modo cola), en lugar de descartarse — con un horizonte acotado para que un comando nunca espere indefinidamente.

- **Alimenta el mismo pipeline.** Las mediciones decodificadas y los cambios de presencia fluyen a través de la ruta normal de decodificación → resolución → persistencia, de modo que todo lo que sigue trata a los dispositivos LwM2M igual que a cualquier otro.

## Pertenencia a inquilino (tenancy) e identidad

Cada dispositivo se vincula a su inquilino mediante su **identidad PSK DTLS autenticada**, mapeada a un `(tenant, externalId)` en el momento de la conexión. Como la identidad se verifica durante el handshake, un dispositivo nunca puede presentar tráfico para otro inquilino, y la identidad en el cable es un identificador opaco en lugar de una cadena legible del tipo `tenant:device`.

## Alta disponibilidad

Una única réplica sirve el endpoint CoAP a la vez, sostenida por un arrendamiento (lease) de propiedad con vallado. Los dispositivos se conectan **hacia dentro** a un único socket UDP enlazado, así que esto no es una opción de ajuste: una segunda réplica compartiendo el Service recibiría — y descartaría — silenciosamente una parte de los datagramas. Solo quien mantiene el arrendamiento enlaza el socket; el despliegue se niega a renderizar más de una réplica.

Lo que aporta el arrendamiento es que **el reemplazo es seguro y automático**. Un pod de reemplazo no enlaza nada hasta haber adquirido el arrendamiento, de modo que nunca hay dos procesos enlazados al endpoint a la vez. Como un dispositivo en modo cola puede permanecer en silencio durante largos períodos por diseño, el nuevo líder reconstruye entonces la presencia a partir de la proyección durable y el tiempo de vida de registro de cada dispositivo en lugar de sondear — de modo que un relevo no marca falsamente como fuera de línea a dispositivos dormidos.

La recuperación es automática pero no instantánea: tarda lo que el pod de reemplazo necesite para planificarse y enlazar, más hasta 30 segundos de la ventana de vallado del arrendamiento. Los datagramas enviados durante esa ventana se pierden, y los mensajes CoAP confirmables se retransmiten, de modo que el relevo en sí pasa en gran medida desapercibido.

**Las observaciones, sin embargo, no lo sobreviven.** Las sesiones DTLS mueren con el proceso anterior, y el nuevo líder arranca sin ninguna — así que un Observe no se vuelve a emitir hasta que el dispositivo se registra de nuevo por su propia cuenta. La presencia se reconstruye; la telemetría no. El apagón está acotado únicamente por el tiempo de vida de registro de cada dispositivo, que para un dispositivo que usa el valor predeterminado de LwM2M son 86400 segundos — un día entero de silencio de un dispositivo perfectamente sano. La palanca es el **tiempo de vida de registro máximo** del servidor: bajarlo limita cuánto puede durar ese apagón, a costa de actualizaciones de registro más frecuentes. Ajústalo según cuánto tiempo estás dispuesto a quedarte sin telemetría tras un reinicio, no según la preferencia del dispositivo.

:::note Estado
La ingesta LwM2M está disponible como servicio opcional (opt-in) sobre CoAP/UDP con DTLS-PSK. Impulsa la [presencia de dispositivo](./device-presence.md) autoritativa, ingiere los objetos de sensor observados como mediciones, y envía comandos Read/Write/Execute en sentido descendente (downlink) (con retención y drenaje durables para dispositivos dormidos). La decodificación de Notify es **solo SenML-JSON**, de modo que un cliente solo LwM2M 1.0 obtiene presencia y comandos pero ninguna medición hasta que llegue la decodificación TLV. El alcance de la disponibilidad general (GA) son las credenciales PSK (X.509 / clave pública sin procesar y un servidor Bootstrap están planeados).
:::

## Cómo se opera

El endpoint CoAP lo sirve una única réplica propietaria, lo que le da a LwM2M algunas propiedades
operativas que conviene conocer antes de depender de él en producción: qué cuesta un relevo, por
qué las observaciones no vuelven por sí solas, y cómo acotar el hueco de telemetría. Todo ello se
cubre en **[Cómo operar los servicios de borde](../deployment/edge-services.md)**.
