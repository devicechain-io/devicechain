---
title: Multitenencia
---

# Multitenencia

Cada instancia de DeviceChain ejecuta un **único conjunto compartido de microservicios** que atiende a todos sus inquilinos. No levanta una pila de pods separada para cada inquilino. En su lugar, el aislamiento entre inquilinos se aplica en las capas de mensajería y almacenamiento.

## La instancia y sus inquilinos

Un único recurso personalizado de Kubernetes modela la plataforma misma:

- **`Instance`** (con alcance de clúster): uno por instalación. Representa la plataforma.

Los inquilinos no son recursos de Kubernetes. Un inquilino es un **registro de base de datos** del plano de control: una entrada en el registro más su configuración por inquilino. Creas inquilinos bajo demanda a través de la API de administración de la instancia y de la consola `/admin`. Los inquilinos comparten los servicios de la instancia y no obtienen pods propios.

Una instancia recién creada no tiene inquilinos. Solo siembra un superusuario, que crea el primer inquilino desde la consola de administración.

## Aislamiento {#isolation}

- **Almacenamiento (aplicado).** Cada fila propiedad de un inquilino lleva su inquilino, en una columna `tenant_id` en casi todas las tablas (algunas tablas del motor de detección la llaman `tenant`). Un alcance de base de datos central aplica un predicado `WHERE tenant_id = …`, sobre la columna de inquilino que tenga la tabla, a toda lectura y estampa el inquilino en toda escritura. Si una consulta con alcance de inquilino no tiene inquilino en el contexto, el alcance la rechaza, de modo que un filtro olvidado no puede filtrar los datos de otro inquilino. En las solicitudes a la API, el inquilino proviene de la afirmación (claim) de inquilino del JWT verificado de quien llama (las llamadas internas entre servicios son la única excepción, descrita más abajo). En los mensajes, el inquilino se deriva del asunto (subject) de mensajería.
- **Mensajería (aplicado).** Los asuntos tienen alcance por inquilino (`{instance}.{tenant}.{suffix}`), de modo que el tráfico de cada inquilino tiene su propio espacio de nombres en el bus. En el plano de dispositivos, el broker lo aplica:
  - los listeners MQTT/NATS usan TLS;
  - un auth-callout de NATS, en el que el broker pide a la plataforma que autorice cada conexión, vincula cada conexión de dispositivo a los asuntos de su propio inquilino;
  - los puntos de escritura y suscripción de mensajería rechazan un segmento de inquilino malformado.

  Como resultado, un dispositivo no puede publicar en los asuntos de otro inquilino ni suscribirse a ellos.
- **Autenticación.** Los JWT llevan afirmaciones de inquilino que resuelven el inquilino de la solicitud. Los servicios las validan localmente, sin una llamada de red por solicitud.

## Eliminar un inquilino

Un inquilino es un registro de base de datos, no un conjunto de pods, así que eliminarlo no consiste en desmontar infraestructura. Sus datos son filas, flujos, búsquedas en caché y objetos subidos, repartidos por todos los sistemas de almacenamiento que usa la instancia y todos indexados por el token del inquilino.

Por eso la eliminación es un ciclo de vida:

1. El acceso se corta de inmediato.
2. Los datos se recuperan en segundo plano.
3. El token permanece reservado hasta que termina la recuperación y ninguna conexión anterior a la eliminación podría seguir escribiendo bajo él.

Consulta [Eliminación de inquilinos](../deployment/tenant-deletion.md).

## Por qué microservicios compartidos

Un único conjunto de servicios para todos los inquilinos mantiene pequeña la huella del clúster y simple el modelo operativo. El alcance aplicado a nivel de fila, junto con el alcance por asuntos en el bus, proporciona el aislamiento que importa. Los servicios compartidos determinan el inquilino de cada solicitud o mensaje y limitan automáticamente a él todo acceso a datos.

El inquilino de la ruta de API se toma de la afirmación de inquilino del JWT RS256 verificado de quien llama. La única excepción es una llamada interna entre servicios: su token de servicio no tiene afirmación de inquilino, así que el servicio que llama indica el inquilino en un encabezado de la solicitud, que solo se respeta después de verificar la firma del token de servicio. El pod compartido consume los mensajes de todos los inquilinos a través de un asunto comodín y deriva el inquilino de cada mensaje a partir de su asunto. Se ha eliminado un mecanismo temporal anterior que confiaba en un encabezado de inquilino fijado por un gateway.

:::note Estado
El alcance de inquilino en tiempo de ejecución en la ruta de datos se aplica hoy. Una consulta con alcance de inquilino sin inquilino se rechaza.
:::
