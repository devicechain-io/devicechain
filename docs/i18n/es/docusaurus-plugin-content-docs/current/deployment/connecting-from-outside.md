---
sidebar_position: 4.8
title: Conectar dispositivos desde fuera del clúster
---

# Conectar dispositivos desde fuera del clúster {#connecting-from-outside}

Los dispositivos llegan al bróker MQTT de la plataforma por TLS en el puerto TCP **8883**. En un clúster en la nube no se abre nada a internet hasta que lo pides: una instalación nueva y cada actualización dejan MQTT dentro del clúster, y activas la exposición por instancia con `dcctl`. Esta página cubre las dos formas de hacerlo, el nombre DNS y el certificado que necesitan tus dispositivos, el cortafuegos y cómo comprobar el resultado desde una máquina fuera del clúster.

En un clúster local `kind` nada de esto hace falta: `dcctl` ya asigna el puerto 1883 de tu máquina al bróker, como se describe en [Conectar un dispositivo](../guides/connecting-a-device.md#publishing-an-event).

:::note El MQTT en texto plano nunca se expone
Ambos modos transportan únicamente el listener TLS del bróker, en el 8883. El puerto en texto plano no se publica, y el chart se niega a renderizar una exposición sobre un bróker cuyo propio TLS está desactivado. El propio bróker termina el TLS y hace la autenticación de dispositivos, incluidos los [certificados de dispositivo](./device-certificates.md); el controlador de ingreso nunca ve el tráfico en claro. El TLS del bróker no depende del de la consola, así que un clúster compacto que sirve su consola por HTTP plano (`--no-tls`) puede exponer MQTT igualmente.
:::

## Elegir un modo {#modes}

| | `gateway` | `loadbalancer` |
| --- | --- | --- |
| Qué se crea | Una ruta en el Gateway de la instancia, servida por Traefik en el puerto TCP 8883 | Un `Service` de tipo `LoadBalancer` para el puerto TCP 8883, directo a los pods del bróker |
| Cómo encuentra un dispositivo su instancia | Por el nombre de host al que se conecta (indicación de nombre de servidor TLS, SNI) | Por la dirección del propio balanceador; el nombre de host es solo DNS |
| Instancias por dirección | Varias: una dirección compartida, un nombre de host cada una | Un balanceador por instancia |
| Qué dirección del dispositivo ve el bróker | La del controlador de ingreso | La del propio dispositivo, donde el balanceador de la nube la conserva |
| Requiere | El front end Gateway y Gateway API (el valor por defecto en una instalación nueva) y un paso único en el clúster | Una nube que aprovisione Services `LoadBalancer` |

Usa `gateway` cuando la instancia ya se sirve a través de Traefik y quieres una sola dirección para varias instancias. Usa `loadbalancer` en un clúster sin soporte de Gateway API, o cuando quieres un balanceador L4 propio para el bróker, por ejemplo para restringirlo a un conjunto de rangos de origen.

## Activarlo {#enable}

Elige un **nombre de host** para la instancia, por ejemplo `mqtt.acme.example.com`. Los dispositivos marcan este nombre, el certificado del bróker lo incluye y, en modo `gateway`, el Gateway enruta por él. Debe ser un nombre DNS completo, en minúsculas, y cada instancia de un clúster necesita el suyo.

### Modo gateway {#enable-gateway}

El modo gateway necesita que Traefik escuche en el 8883. Eso publica un puerto nuevo en su balanceador, así que es un paso del clúster que das una vez, de forma deliberada:

```bash
dcctl install <provider> --mqtt-gateway
```

Dale a `install` las mismas opciones con las que se instaló el clúster, como `--ha`, igual que en cualquier [actualización progresiva](./releases-and-upgrades.md#rolling-upgrades). Sin `--mqtt-gateway`, una instalación conserva lo que tiene el controlador, de modo que una instalación posterior nunca cierra el puerto por accidente; `--mqtt-gateway=false` lo cierra.

Después, por instancia:

```bash
dcctl bootstrap <provider> acme --mqtt-exposure gateway --mqtt-host mqtt.acme.example.com
# o, en una instancia que ya existe:
dcctl upgrade   <provider> acme --mqtt-exposure gateway --mqtt-host mqtt.acme.example.com
```

`dcctl` rechaza la petición, antes de cambiar nada, cuando Traefik no escucha en el 8883, cuando la instancia no se sirve por el front end Gateway o cuando otra instancia ya usa ese nombre de host.

### Modo balanceador de carga {#enable-loadbalancer}

```bash
dcctl bootstrap <provider> acme --mqtt-exposure loadbalancer --mqtt-host mqtt.acme.example.com
dcctl upgrade   <provider> acme --mqtt-exposure loadbalancer --mqtt-host mqtt.acme.example.com
```

Esto crea el Service `dc-nats-mqtt-lb` en el namespace de la instancia, con `externalTrafficPolicy: Local`. Las anotaciones propias de cada nube, como un balanceador interno, y una lista de rangos de origen permitidos se fijan mediante la [superposición de ajuste](./tuning-an-instance.md) bajo `mqttExposure.loadBalancer`:

```yaml
mqttExposure:
  loadBalancer:
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-internal: "true"
    sourceRanges:
      - 203.0.113.0/24
```

**Un `sourceRanges` vacío permite cualquier dirección de origen (`0.0.0.0/0`).** Defínelo, o restringe el puerto en el cortafuegos de la nube, salvo que tus dispositivos se conecten realmente desde cualquier parte.

En EKS, un Service `LoadBalancer` obtiene un balanceador Classic salvo que se anote para un Network Load Balancer; añade ahí `service.beta.kubernetes.io/aws-load-balancer-type: nlb`. Las anotaciones `aws-load-balancer-scheme` y `aws-load-balancer-nlb-target-type` solo las lee el AWS Load Balancer Controller, que debe estar instalado para que surtan efecto; `aws-load-balancer-internal: "true"` la respeta el proveedor de nube integrado en Kubernetes. Para el balanceador de Traefik en modo gateway, lo hace `dcctl install --aws-nlb traefik`.

### Qué hace una actualización {#enable-effects}

El ajuste se registra en la instancia y lo conserva cada `dcctl upgrade` posterior, así que das las opciones una sola vez. El nombre de host se añade al certificado del bróker. El bróker adopta en caliente un certificado que solo ha ganado el nombre de host, sin reiniciarse y sin desconectar a los dispositivos conectados; el certificado se vuelve a emitir bajo la misma autoridad, así que no hay nada que volver a confiar. (Un cambio que además altere las rutas del bróker o su autoridad sigue reiniciando el bróker, y un reinicio del bróker es, como cualquier reinicio, una ventana en la que se pueden perder mensajes ya confirmados salvo que el [MQTT fail-closed](../reference/transport-matrix.md#mqtt--the-platform-broker) esté activado.) Si el certificado no se puede volver a emitir, porque la instancia no conserva la autoridad que lo firmó, `dcctl` se detiene antes de cambiar nada y lo indica.

Para desactivar la exposición usa `--mqtt-exposure none`. Elimina la ruta o el Service; el certificado conserva el nombre adicional hasta su próxima renovación, lo que es inofensivo.

## DNS {#dns}

Apunta el nombre de host a la dirección que ahora responde en el 8883:

```bash
# gateway: la dirección del controlador de ingreso
kubectl -n traefik get service traefik
# loadbalancer: el balanceador del bróker de la instancia
kubectl -n dci-acme get service dc-nats-mqtt-lb
```

Usa un registro `A` para una dirección IP y un `CNAME` para un nombre, tal como lo informa la nube (EKS informa un nombre; GKE y AKS, una dirección IP). El nombre de host puede ser el mismo con el que la instancia sirve su consola, o uno aparte como `mqtt.acme.example.com`. En modo gateway varias instancias comparten la dirección del controlador, cada una con su propio nombre.

## Puertos y cortafuegos {#firewall}

| Puerto | Quién se conecta | Abierto a |
| --- | --- | --- |
| TCP 8883 | Dispositivos, a la dirección del controlador o del balanceador del bróker | Las redes donde están tus dispositivos |

No hace falta abrir nada más para MQTT. La nube crea la regla de un Service `LoadBalancer` en GKE y AKS. En AWS, comprueba que el grupo de seguridad del balanceador, y el de los nodos detrás de él, permita TCP 8883 desde tus dispositivos. Se necesitan balanceadores de la nube que pasen la conexión en la capa 4: un balanceador HTTP de capa 7 no puede transportar MQTT.

En modo gateway, el bróker ve la dirección del controlador de ingreso como origen de todos los dispositivos, no la del propio dispositivo. Todo lo que se base en la dirección de origen de un dispositivo en el bróker se comparte, por tanto, entre todos los dispositivos que están detrás de un controlador. El modo balanceador de carga con un balanceador que conserva la dirección del cliente (un Network Load Balancer en AWS, el balanceador de red de Google Cloud) no tiene este límite.

El límite por dirección del registro en la primera conexión (`callout.jitSourceTrusted`, consulta el README del chart) debe seguir **desactivado** en modo gateway, porque todos los dispositivos compartirían la dirección del controlador y un dispositivo ruidoso agotaría la cuota de todos. En modo balanceador de carga actívalo solo detrás de un balanceador que conserve las direcciones de cliente.

En un clúster `kind` local que llega al bróker mediante asignaciones de puertos del host, el 8883 necesita su propia asignación al nodo, igual que el 1883; la configuración local por defecto asigna solo el 1883.

### Inundaciones de conexiones {#connection-floods}

Un puerto abierto a internet está abierto a cualquiera que se conecte, sea un dispositivo válido o no. Cada conexión cuesta un handshake TLS en el bróker y, para un dispositivo que presenta credenciales, una ida y vuelta por el callout de autenticación. Acótalo en tres niveles:

- Restringe las direcciones de origen: `sourceRanges` en modo balanceador de carga, o una regla de cortafuegos de la nube en cualquiera de los dos modos, a las redes que usan tus dispositivos.
- Limita el bróker con `--broker-max-connections` en `dcctl bootstrap` o `dcctl upgrade`. Fija el `max_connections` del bróker, contando por igual dispositivos y clientes de la plataforma. Sin él, el bróker conserva el límite propio del servidor, como antes. Defínelo por encima de tu flota, con margen para una tormenta de reconexiones, y por debajo de lo que pueden soportar los pods del bróker; los dispositivos que lo superen se rechazan. Se registra en la instancia y lo conservan las actualizaciones posteriores; `--broker-max-connections=0` vuelve al límite del servidor.
- Vigila las métricas de conexiones y autenticación del bróker (consulta [Observabilidad](./observability.md)).

Los inicios de sesión propios de la plataforma en el bróker están limitados al protocolo NATS y no pueden usarse por este puerto: una credencial de la plataforma filtrada no puede presentarse por MQTT. Solo el inicio de sesión de event-sources, que ejecuta la fuente MQTT de la plataforma, puede hablar MQTT.

## El certificado en el que deben confiar tus dispositivos {#certificates}

El bróker presenta un certificado emitido por la autoridad certificadora propia de la instancia, y el nombre de host que elegiste figura entre sus nombres. Los dispositivos deben confiar en esa autoridad, como en cualquier [conexión MQTT a la instancia](../guides/connecting-a-device.md#publishing-an-event). Expórtala desde una máquina con acceso al clúster:

```bash
kubectl -n dci-acme get secret dc-nats-tls -o jsonpath='{.data.ca\.crt}' | base64 -d > instance-ca.crt
```

Distribuye `instance-ca.crt` con el firmware o la configuración de tus dispositivos. Es un certificado público. `dcctl upgrade` renueva el certificado del bróker cuando le quedan menos de 30 días de validez; la autoridad sigue siendo la misma, así que los dispositivos siguen confiando en ella tras las renovaciones.

**Cuándo cambia la autoridad.** La autoridad se crea una sola vez, cuando se crea por primera vez el namespace de la instancia, y desde entonces se conserva en la instancia: un `dcctl upgrade` y un `dcctl bootstrap` repetido sobre una instancia en marcha la conservan y solo emiten un certificado nuevo bajo ella. Una autoridad nueva existe solo para una instancia nueva y para una instancia que destruyes y vuelves a construir, incluida una restaurada desde una copia de seguridad en un clúster reconstruido: la autoridad no forma parte de una copia de seguridad de la base de datos ni del depósito de la clave raíz, así que una instancia reconstruida obtiene una nueva. Todo dispositivo que fijó la autoridad anterior debe recibir el nuevo `instance-ca.crt`. Si fijas la autoridad en el firmware, planifícalo antes de reconstruir una instancia.

Los dispositivos deben enviar el nombre de host como nombre de servidor TLS. En modo gateway así se encuentra la instancia correcta: una conexión que no envía nombre de servidor, o para la que el Gateway no tiene ruta, se cierra. Conectarse por dirección IP falla, por tanto, en modo gateway; conéctate por nombre.

## Comprobarlo desde fuera {#verify}

Desde una máquina fuera del clúster, cuando el DNS ya resuelva:

```bash
# 1. Responde el certificado correcto y se verifica contra la CA de la instancia
openssl s_client -connect mqtt.acme.example.com:8883 \
  -servername mqtt.acme.example.com -CAfile instance-ca.crt </dev/null \
  | grep -E 'subject=|Verify return code'
```

Deberías ver el nombre de host en el sujeto (o entre los nombres alternativos) y `Verify return code: 0 (ok)`.

```bash
# 2. Un dispositivo puede conectarse y publicar
mosquitto_pub --cafile instance-ca.crt \
  -h mqtt.acme.example.com -p 8883 \
  -i 'devicechain:acme:sensor-001' \
  -u 'acme:<credentialId>' -P '<credentialSecret>' \
  -t 'devicechain/acme/devices/sensor-001/events' \
  -m '{"device":"sensor-001","eventType":"Measurement","payload":{"entries":[{"measurements":{"temperature":"21.5"}}]}}'
```

Añade `-d` para ver el `CONNACK` del bróker y el `PUBLISH`. Un dispositivo que se autentica con un certificado añade `--cert` y `--key`, como en [Conectar con un certificado](../guides/device-x509.md).

## Cuando no funciona {#troubleshooting}

| Lo que ves | Qué significa |
| --- | --- |
| `dcctl` se niega con "needs the ingress controller to listen on 8883" | Ejecuta `dcctl install <provider> --mqtt-gateway` una vez para el clúster, o usa `--mqtt-exposure loadbalancer` |
| `dcctl install --mqtt-gateway=false` se niega | Una instancia aún enruta MQTT por ese puerto. Pásala antes a `--mqtt-exposure none` o `loadbalancer` |
| El renderizado del chart falla con "the broker's TLS is off" | Solo en un chart instalado a mano: el MQTT en texto plano nunca se expone. `dcctl` siempre activa el TLS del bróker |
| `dcctl` se niega porque el nombre de host está en uso | Otra instancia del clúster usa ese nombre. Da a cada instancia el suyo |
| La conexión se cierra durante el handshake, `no peer certificate available` | En modo gateway: no se envió nombre de servidor, o ninguna ruta coincide con él. Conéctate con el nombre de host exacto; revisa `kubectl -n dci-acme get tlsroute devicechain-mqtt` |
| `certificate verify failed` o discrepancia de nombre de host | El dispositivo confía en otra autoridad, o marca un nombre que el certificado no incluye. Compara el nombre con la salida del paso 1 |
| Se agota el tiempo de espera | El DNS apunta a otro sitio, o el cortafuegos o el grupo de seguridad bloquea el TCP 8883. En modo gateway comprueba que `kubectl -n traefik get service traefik` lista el 8883 |
| El listener `mqtt-tls` del Gateway no está `Programmed` | `kubectl -n dci-acme describe gateway devicechain` indica el motivo; la causa habitual es que el controlador no tiene entrypoint en el 8883 |

El comportamiento MQTT de la plataforma, los límites de ingesta y las garantías de entrega son los mismos sea cual sea la forma en que un dispositivo llega al bróker; consulta [el transporte MQTT](../reference/transport-matrix.md#mqtt--the-platform-broker).
