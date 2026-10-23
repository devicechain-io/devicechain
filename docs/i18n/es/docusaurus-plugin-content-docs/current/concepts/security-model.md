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

- **Transporte.** Los listeners MQTT y NATS del broker usan TLS por defecto. Solo puede desactivarse para depurar en
  local. La ingesta HTTP queda protegida por el TLS del ingress de la instancia.
- **El broker afirma la identidad.** El broker pide a la plataforma que apruebe cada conexión, y la plataforma
  vincula la conexión a los asuntos de ese único dispositivo. La identidad de dispositivo en la que confía la plataforma para un evento es el
  asunto por el que el broker lo entregó, no una afirmación de la carga útil. Un dispositivo no puede publicar en los asuntos de otro dispositivo ni
  de otro inquilino, ni leer sus comandos. El id de cliente MQTT debe además ser el
  `{instanceId}:{tenant}:{deviceToken}` del propio dispositivo, seguido opcionalmente de `:` y un sufijo que elija el dispositivo,
  de modo que un dispositivo no pueda apropiarse de la sesión de otro.
- **Credenciales.** El modo de autenticación de dispositivos por defecto es `required`: solo se acepta un evento de un dispositivo autenticado.
  En el broker MQTT de la plataforma, el dispositivo se autentica al conectarse. Por HTTP, o a través de un broker propio, la credencial
  viaja en el cuerpo del evento. Una vez que una credencial ha autenticado un dispositivo, se rechaza un evento que nombre a otro
  dispositivo. Desactivar, rotar o eliminar una credencial rechaza de inmediato las nuevas conexiones y cierra las sesiones abiertas
  del dispositivo. Consulte [Credenciales de dispositivo](../guides/device-credentials.md), incluida la rapidez con que una
  revocación alcanza a los eventos en curso.
- **Credenciales en reposo.** La contraseña de una credencial `MQTT_BASIC` se guarda solo como un resumen unidireccional con clave
  y nunca se devuelve. Una credencial `ACCESS_TOKEN` es distinta hoy: el token es el identificador de la
  credencial y se guarda como un valor que la plataforma puede leer. Una fuga de la base de datos o de una copia de
  seguridad expondría los tokens de todos los dispositivos autenticados por token. Trate esas copias en consecuencia y
  prefiera `MQTT_BASIC` cuando pueda. Está previsto guardar los tokens de acceso solo como resumen antes de la 1.0.0.
- **Credenciales en el cuerpo de un evento.** En el broker MQTT de la plataforma la conexión ya ha autenticado al dispositivo, así que
  se rechaza un evento que además lleve una credencial en su cuerpo. Un ajuste transitorio, `capture.bodyCredential: strip`, acepta ese
  evento sin la credencial para que una flota siga informando mientras se actualiza su firmware; está previsto retirarlo antes de la
  1.0.0. Por HTTP y a través de un broker propio, donde el cuerpo es la vía de la credencial, la contraseña o el token se sella antes de
  colocar el evento en el flujo interno de la plataforma. En todos los transportes, la copia de un mensaje no decodificable que se
  conserva para diagnóstico tiene sus credenciales eliminadas.
- **Pasarelas.** Una pasarela, como el [agente de borde](../deployment/edge-services.md#gateway-identity), es un dispositivo que habla por otros dispositivos. Inicia sesión como sí misma y publica en su propio espacio de nombres, y la plataforma acepta un evento solo de un dispositivo que se le haya asignado. Una pasarela no puede publicar en los subjects de otra pasarela ni de otro dispositivo; una que nombre un dispositivo que no representa recibe un rechazo, queda registrada y se le informa, en lugar de confirmarse y perderse. **Una pasarela habla por sus hijos.** Una pasarela comprometida puede falsificar eventos solo de los dispositivos que tiene asignados, y solo hasta que se la deshabilita o se revoca su credencial. Lo que afirma sobre *qué cliente local* envió un evento es tan fiable como la autenticación local de su propia red. Asignar un dispositivo a una pasarela requiere el mismo permiso que emitirle una credencial, porque concede la misma capacidad.

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

Cada servicio registra una entrada en el diario, con la identidad que actúa, por cada creación, actualización y eliminación que hace
en los datos de un inquilino, y el servicio de inicio de sesión registra además eventos como accesos, accesos fallidos y resultados del
inicio de sesión único. La pantalla de Auditoría de un inquilino muestra el diario del registro, y solo las entradas de ese inquilino.
Los eventos de inicio de sesión se leen a través de la API de administración del operador, y los diarios de otras áreas, como
conectores, paneles y actualizaciones, se registran pero no se muestran en la pantalla de Auditoría.

Una entrada del registro se escribe en la misma transacción que el cambio que describe, de modo que ambos se guardan juntos o no se
guarda ninguno. Si la entrada no puede escribirse, el cambio no se realiza y la llamada informa de un fallo, así que un cambio nunca
queda sin registrar y un fallo notificado nunca corresponde a un cambio guardado. Un fallo al escribir una entrada de inicio de sesión se anota en el log y no bloquea el
acceso. Las entradas nunca se editan, salvo que eliminar un inquilino vacía los nombres de sus entradas. El diario no se depura
automáticamente. Consulte [Auditoría](../user-guide/audit.md).

## Lo que aún no se hace cumplir {#accepted-gaps}

Estas carencias son conocidas y se aceptan en la versión actual. Planifique en torno a ellas.

1. **Una instancia actualizada conserva el inicio de sesión compartido del broker hasta su siguiente actualización.** Cada área se conecta al broker con un
   inicio de sesión propio, limitado a los asuntos, flujos, consumidores y buckets que usa, y ningún servicio puede crear,
   cambiar ni borrar un flujo o un bucket: eso lo hace un inicio de sesión de administración, en un job que el chart ejecuta antes de cada
   instalación y actualización, y un servicio solo comprueba que lo que abre coincide con lo que declara su versión y se niega a arrancar, indicando la diferencia,
   cuando no es así. Un servicio comprometido no puede por tanto reconfigurar un flujo para escribir en los asuntos que solo un dispositivo
   puede escribir, así que no puede falsificar eventos de dispositivo, respuestas a comandos ni reportes de dispositivo por esa vía. Lo que queda
   es el inicio de sesión compartido que usaban las versiones anteriores. Una instancia nueva nunca lo tiene. Una instancia actualizada
   desde una versión que lo tenía lo conserva durante la actualización que da a cada área un inicio de sesión propio, en la
   configuración del broker y en la configuración de instancia que lee cada pod, con los permisos que siempre tuvo, incluido
   administrar flujos, así que un pod de esa instancia puede presentarlo. La siguiente `dcctl upgrade` lo elimina de ambas. Los servicios que aceptan tráfico de dispositivos (event-sources, device-management, lwm2m-ingest y sparkplug-ingest) siguen
   siendo parte del núcleo de confianza de la plataforma, porque escriben datos de dispositivo por diseño. Dos permisos son más amplios a propósito: event-sources puede actualizar los almacenes MQTT propios del broker, y
   user-management puede purgar los datos de un inquilino eliminado en todos los flujos. Un servicio comprometido todavía puede causar
   pérdida, aunque no falsificación, llenando un flujo en el que puede crear consumidores hasta que se descartan mensajes más antiguos.
2. **El inicio de sesión de la base de datos es compartido por todos los servicios de una instancia.** Todos los servicios acceden a la base de datos
   relacional de la instancia con un único inicio de sesión, así que un servicio comprometido podría leer y escribir las tablas de otra área. El aislamiento de inquilinos
   en la base de datos descansa en el ámbito de inquilino de la aplicación, no en permisos de la base de datos. Los roles separados por
   servicio están previstos para una versión 1.0.x.
3. **Los tokens de acceso se guardan legibles**, como se describe en [Dispositivos](#devices). Está previsto guardarlos solo
   como resumen antes de la 1.0.0.
4. **Confirmaciones MQTT durante un cambio de líder del broker.** Por defecto, el broker puede confirmar un mensaje QoS 1 y
   perderlo después si el flujo de captura cambia de líder. Con `infrastructure.nats.mqttFailClosed` activado, una
   confirmación significa que la plataforma conserva el mensaje. El ajuste está desactivado por defecto, y
   [Observabilidad](../deployment/observability.md) describe el equilibrio.
5. **La clave raíz es legible para cualquiera que pueda leer el Secret de Kubernetes de la instancia.** Restrinja el acceso a ese
   Secret y al etcd del clúster en consecuencia.
6. **No se ha publicado ninguna prueba de penetración externa.**

Nada de esta página sustituye su propia revisión. Si una entrada de aquí es un problema para su despliegue, la sección
correspondiente de arriba indica dónde está el control.
