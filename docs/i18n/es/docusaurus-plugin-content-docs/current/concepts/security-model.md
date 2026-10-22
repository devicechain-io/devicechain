---
title: Modelo de seguridad
---

# Modelo de seguridad {#security-model}

Esta página indica dónde traza DeviceChain sus límites de confianza, qué hace cumplir cada uno hoy y qué no hace cumplir
todavía. Las carencias aceptadas están en su propia sección para que pueda planificar en torno a ellas.

Para notificar una vulnerabilidad, siga el proceso de la
[política de seguridad](https://github.com/devicechain-io/devicechain/blob/main/SECURITY.md).

## Límites de confianza de un vistazo {#boundaries}

| Límite | Qué separa ambos lados | Quién lo hace cumplir |
| --- | --- | --- |
| Inquilino frente a inquilino | Cada fila de un inquilino y cada asunto de mensaje lleva el inquilino | Un ámbito de base de datos en cada consulta y asuntos por inquilino en el broker |
| Dispositivo frente a dispositivo | La conexión de un dispositivo solo puede usar sus propios asuntos | El broker, mediante una llamada de autorización a la plataforma |
| Persona o programa frente a la API | Un token firmado, comprobado en cada servicio | Firma del token, reclamación de inquilino y permisos |
| Secreto frente a lector | Los secretos se sellan y nunca se devuelven | Cifrado de sobre bajo la clave raíz de la instancia |
| Inquilino frente a la red | Un destino aportado por un inquilino no puede alcanzar direcciones internas | Una comprobación en el momento de la conexión |
| Servicios de la plataforma entre sí | Inicios de sesión separados en el broker y la base de datos | **Parcialmente.** Consulte [lo que aún no se hace cumplir](#accepted-gaps) |

## Inquilinos {#tenants}

Un único conjunto de servicios atiende a todos los inquilinos, así que el aislamiento se aplica en los datos y en los mensajes, no con pods separados.

- **Almacenamiento.** Las tablas de un inquilino llevan el inquilino, y un ámbito central de base de datos añade la condición del inquilino a cada
  lectura y lo estampa en cada escritura. Una consulta con ámbito de inquilino que no tiene inquilino en su contexto se rechaza, no se ejecuta sin filtrar.
- **Mensajería.** Los asuntos tienen ámbito por inquilino, y un segmento de inquilino mal formado se rechaza donde un servicio publica o se suscribe.
- **API.** El inquilino de una solicitud se toma de la reclamación de inquilino del token verificado del llamador. La única excepción es un
  servicio que llama a otro: nombra el inquilino en una cabecera, que solo se acepta tras verificar la firma del
  token de servicio.

Consulte [Multitenencia](./multi-tenancy.md).

## Dispositivos {#devices}

- **Transporte.** Los listeners MQTT y NATS usan TLS.
- **El broker afirma la identidad.** El broker pide a la plataforma que apruebe cada conexión, y la plataforma
  vincula la conexión a los asuntos de ese único dispositivo. La identidad de dispositivo en la que confía la plataforma para un evento es el
  asunto por el que el broker lo entregó, no una afirmación de la carga útil. Un dispositivo no puede publicar en los asuntos de otro dispositivo ni
  de otro inquilino, ni leer sus comandos. El id de cliente MQTT debe además empezar por el
  `{instanceId}:{tenant}:{deviceToken}` del propio dispositivo, de modo que un dispositivo no pueda apropiarse de la sesión de otro.
- **Credenciales.** El modo de autenticación de dispositivos por defecto es `required`: un evento debe presentar una credencial válida, y un
  dispositivo autenticado no puede enviar eventos como otro dispositivo. Desactivar o eliminar una credencial detiene de inmediato las nuevas
  conexiones. Consulte [Credenciales de dispositivo](../guides/device-credentials.md), incluida la rapidez con que una
  revocación alcanza a los eventos en curso.
- **Credenciales en reposo.** La contraseña de una credencial `MQTT_BASIC` se guarda solo como un resumen unidireccional con clave, se muestra una vez
  al establecerla y nunca se devuelve. Una credencial `ACCESS_TOKEN` es distinta hoy: el token es el identificador de la
  credencial y se guarda como un valor que la plataforma puede leer. Una fuga de la base de datos o de una copia de
  seguridad expondría los tokens de todos los dispositivos autenticados por token. Trate esas copias en consecuencia y
  prefiera `MQTT_BASIC` cuando pueda.
- **Credenciales en tránsito.** La contraseña o el token que llega con un evento se sella antes de colocar el evento en el flujo
  interno de la plataforma, y la copia de un mensaje no decodificable que se conserva para diagnóstico tiene sus
  credenciales eliminadas.

## Personas y programas {#api-access}

- **Tokens.** El inicio de sesión emite tokens de acceso y de actualización firmados con RS256. Los servicios los validan localmente con un
  conjunto de claves publicado, así que ninguna solicitud espera una llamada al servicio de inicio de sesión. Las claves de firma las tiene solo `user-management`, selladas bajo la
  clave raíz de la instancia.
- **Permisos.** Los roles llevan permisos, y la API los comprueba en cada llamada. Una pertenencia puede limitarse a los datos de un
  cliente, y los miembros, invitaciones y roles que un inquilino gestiona por sí mismo no pueden conceder más de lo que posee quien
  los concede. Consulte [Gobernanza y cuotas](./governance.md).
- **Inicio de sesión único.** Un inquilino puede exigir el inicio de sesión mediante su propio proveedor de identidad. Consulte
  [Inicio de sesión único](./single-sign-on.md).
- **Agentes de IA.** El [servidor MCP](./mcp.md) opcional llama a la plataforma con el token del propio llamador. No tiene ningún token
  propio, así que un agente solo puede leer lo que puede leer su usuario.
- **Límites de abuso.** La ingesta tiene un techo de tasa por inquilino, los conectores de salida tienen techos por inquilino, el servidor MCP
  limita a cada llamador, las solicitudes GraphQL tienen ejecución y tamaño de cuerpo acotados, y los intentos fallidos de inicio de sesión tienen límite de tasa.
  No hay un límite de tasa de solicitudes por inquilino en la propia API GraphQL.

## Secretos {#secrets}

Las credenciales que guarda un inquilino, como los ajustes de conectores y los secretos de cliente del proveedor de identidad, se almacenan en un
almacén de secretos cifrado. Cada valor se sella con su propia clave de datos AES-256-GCM, y esa clave se envuelve con la
**clave raíz** de la instancia, que reside en el Secret de Kubernetes de la instancia. Un valor es de solo escritura a través de la API: se
resuelve dentro del servidor cuando se usa y nunca se devuelve. Los cambios se auditan por quién, cuándo y qué identificador, nunca por valor.

La clave raíz es lo único de lo que el almacén no puede recuperarse. Conserve una copia en custodia: sin ella, una instancia restaurada
no puede descifrar sus secretos y nadie puede iniciar sesión. Consulte [Recuperación ante desastres](../deployment/disaster-recovery.md).
El almacén admite un proveedor de claves conectable, pero la clave raíz de la instancia es el único proveedor que se incluye hoy.

## Tráfico de salida {#egress}

Los webhooks, los relés de correo y los conectores de salida envían a destinos que aporta un inquilino. Cada una de esas conexiones se comprueba en el momento en que se establece,
contra la dirección a la que el nombre se resolvió realmente, de modo que un nombre que cambia su respuesta entre una comprobación y una
conexión no puede eludirla. Se rechazan las direcciones de bucle local, privadas, de NAT de operador, de enlace local y de metadatos de la nube, y un
destino rechazado es un fallo permanente, no un reintento. Un operador puede permitir direcciones privadas
concretas. El `networkPolicy` del chart es una segunda capa, desactivada por defecto. Consulte
[Conectores de salida](./outbound-connectors.md).

## Auditoría {#audit}

La plataforma registra una entrada en el diario por cada creación, actualización y eliminación que hace en el registro de un inquilino, con la
identidad que actúa, y por eventos de inicio de sesión como accesos, accesos fallidos y resultados del inicio de sesión único. Si una entrada del
registro no puede escribirse, el cambio falla. Las entradas nunca se editan, y un inquilino solo ve las suyas. Los cambios en
conectores, paneles, reglas y actualizaciones no están en este diario, y el diario no se depura
automáticamente. Consulte [Auditoría](../user-guide/audit.md).

## Lo que aún no se hace cumplir {#accepted-gaps}

Estas carencias son conocidas y se aceptan en la versión actual. Planifique en torno a ellas.

1. **Los servicios de la plataforma comparten hoy un único inicio de sesión en el broker.** Todos los servicios se conectan al broker con el mismo inicio de sesión. A ese inicio de sesión se le
   deniegan los asuntos que solo un dispositivo puede escribir, lo que impide que un servicio publique un evento de dispositivo por
   error, pero también puede administrar los flujos del broker, y un flujo puede configurarse para escribir en
   esos asuntos. Un servicio comprometido podría, por tanto, falsificar eventos de dispositivo, respuestas a comandos y reportes de dispositivo
   de cualquier inquilino. Se está construyendo un inicio de sesión propio para cada servicio, limitado a los asuntos y flujos que necesita,
   y esta entrada cambiará cuando se publique. Hasta entonces, trate los servicios que aceptan tráfico de
   dispositivos como parte del núcleo de confianza de la plataforma.
2. **El inicio de sesión de la base de datos es compartido por todos los servicios de una instancia.** Todos los servicios acceden a la base de datos
   relacional de la instancia con un único inicio de sesión, así que un servicio comprometido podría leer y escribir las tablas de otra área. El aislamiento de inquilinos
   en la base de datos descansa en el ámbito de inquilino de la aplicación, no en permisos de la base de datos. Los roles separados por
   servicio están previstos para una versión posterior a la 1.0.
3. **Los tokens de acceso se guardan legibles**, como se describe en [Dispositivos](#devices).
4. **Confirmaciones MQTT durante un cambio de líder del broker.** Por defecto, el broker puede confirmar un mensaje QoS 1 y
   perderlo después si el flujo de captura cambia de líder. Con `infrastructure.nats.mqttFailClosed` activado, una
   confirmación significa que la plataforma conserva el mensaje. El ajuste está desactivado por defecto, y
   [Observabilidad](../deployment/observability.md) describe el equilibrio.
5. **La clave raíz es legible para cualquiera que pueda leer el Secret de Kubernetes de la instancia.** Restrinja el acceso a ese
   Secret y al etcd del clúster en consecuencia.
6. **No se ha publicado ninguna prueba de penetración externa.**

Nada de esta página sustituye su propia revisión. Si una entrada de aquí es un problema para su despliegue, la sección
correspondiente de arriba indica dónde está el control.
