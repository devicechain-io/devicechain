---
sidebar_position: 3
title: Multitenencia
---

# Multitenencia

DeviceChain ejecuta un **único conjunto compartido de microservicios por instancia** que atiende a todos los inquilinos, en lugar de levantar una pila separada de pods para cada inquilino. El aislamiento se aplica en las capas de mensajería y almacenamiento.

## La instancia y sus inquilinos

Un recurso personalizado de Kubernetes modela la plataforma misma:

- **`DeviceChainInstance`** (con alcance de clúster) — uno por instalación. Representa la plataforma.

Los inquilinos **no** son recursos de Kubernetes. Un inquilino es un **registro de base de datos** del plano de control — una entrada de registro más configuración por inquilino — creado bajo demanda a través de la API de administración de la instancia y la consola `/admin`. Los inquilinos comparten los servicios de la instancia y **no** obtienen sus propios pods. Una instancia recién creada **no tiene inquilinos**: solo siembra un superusuario, quien crea el primer inquilino desde la consola de administración.

## Aislamiento {#isolation}

- **Almacenamiento (aplicado)** — cada fila propiedad de un inquilino lleva un `tenant_id`, y un alcance de base de datos central aplica un predicado `WHERE tenant_id = …` a toda lectura y lo estampa en toda escritura. El alcance es de **fallo cerrado**: una consulta con alcance de inquilino sin inquilino en el contexto es rechazada, de modo que un filtro faltante no puede filtrar los datos de otro inquilino. El inquilino por solicitud proviene de la afirmación (claim) de inquilino verificada del JWT de quien llama, y el inquilino por mensaje se deriva del asunto (subject) de mensajería.
- **Mensajería (aplicado)** — los asuntos (subjects) tienen alcance por inquilino (`{instance}.{tenant}.{suffix}`), de modo que el tráfico de un inquilino queda espaciado por nombres en el bus. En el **plano de dispositivos** esto se aplica en el broker: los listeners MQTT/NATS usan TLS, un auth-callout de NATS vincula cada conexión de dispositivo a los asuntos de su propio inquilino, y los puntos de escritura/suscripción de mensajería rechazan un segmento de inquilino malformado — de modo que un dispositivo no puede publicar en los asuntos de otro inquilino ni suscribirse a ellos.
- **Autenticación** — los JWT llevan afirmaciones (claims) de inquilino que resuelven el inquilino de la solicitud; los servicios las validan localmente sin una llamada de red por solicitud.

## Por qué microservicios compartidos

Ejecutar un único conjunto de servicios para todos los inquilinos mantiene pequeña la huella del clúster y simple el modelo operativo, mientras que el alcance aplicado a nivel de fila (más el alcance de asuntos en el bus) provee el aislamiento que importa. Los servicios compartidos derivan el inquilino de cada solicitud o mensaje y limitan automáticamente a él todo el acceso a datos.

:::note Estado
El alcance de inquilino en tiempo de ejecución en la ruta de datos se aplica hoy (con fallo cerrado): el inquilino de la ruta de API se obtiene de la afirmación (claim) de inquilino del JWT RS256 verificado de quien llama, y el pod compartido consume los mensajes de todos los inquilinos a través de un asunto (subject) comodín, derivando el inquilino de cada mensaje a partir de su asunto. La costura temporal anterior de encabezado de gateway confiable ha sido eliminada.
:::
