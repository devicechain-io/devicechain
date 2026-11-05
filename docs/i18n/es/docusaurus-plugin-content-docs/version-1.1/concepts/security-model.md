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
| Desconocido frente al primer contacto | Un dispositivo sin credencial solo puede registrarse con la clave y el secreto compartidos de un perfil | Un secreto de 256 bits generado por el servidor, trabajo acotado antes de comprobarlo y un tope por perfil. Véase [Autorregistro](#devices) |
| Persona o programa frente a la API | Un token firmado, comprobado en cada servicio | Firma del token, reclamación de inquilino y permisos |
| Secreto frente a lector | Los secretos se sellan y nunca se devuelven | Cifrado de sobre bajo la clave raíz de la instancia |
| Inquilino frente a la red | Un destino aportado por un inquilino no puede alcanzar direcciones internas | Una comprobación en el momento de la conexión |
| Internet frente a los endpoints propios de la plataforma | El emisor de tokens de servicio y el conjunto de claves de firma se sirven en un puerto al que no llega ninguna ruta | Un listener interno independiente, no una regla de ingress |
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
- **Credenciales en reposo.** La contraseña de una credencial `MQTT_BASIC` y el token de una credencial `ACCESS_TOKEN`
  se guardan solo como resúmenes unidireccionales con clave, bajo una clave derivada de la clave raíz de la instancia,
  que no se guarda en la base de datos. El secreto de un perfil de aprovisionamiento y el secreto de una reclamación de
  dispositivo se guardan del mismo modo, cada uno bajo una clave propia. Cada uno se muestra una vez, en la respuesta a la llamada que lo establece, y
  nunca se vuelve a devolver; una lectura de una credencial de token de acceso devuelve una huella que no puede
  presentarse como el token. Por tanto, una fuga de la base de datos o de una copia de seguridad no da ninguna
  contraseña, token, secreto de aprovisionamiento ni secreto de reclamación que funcione.
- **Autorregistro.** Un dispositivo que aún no tiene credencial puede registrarse por sí mismo mediante HTTPS presentando la clave y el secreto compartidos de un perfil de aprovisionamiento ([cómo configurarlo](../guides/device-self-registration.md)). El secreto lo genera el servidor (256 bits aleatorios), se guarda solo como un resumen con clave y se compara en tiempo constante. Todas las formas en que puede fallar la autenticación, incluidos un inquilino o una clave desconocidos, un perfil deshabilitado o caducado y un inquilino en eliminación, reciben el mismo `401` con el mismo coste, de modo que la respuesta nunca dice qué parte era incorrecta. El trabajo que se hace para un desconocido está acotado antes de comprobar el secreto: un plazo para el cuerpo de la solicitud, un tope de 8 KiB, un límite de solicitudes en curso y una cuota por dirección de cliente. Esa dirección es la del cliente real solo cuando `infrastructure.http.trustedProxies` nombra los proxies situados delante de los servicios; véase [Direcciones de cliente y proxies de confianza](../deployment/bootstrap.md#trusted-proxies). Un límite por inquilino, tomado del nivel del inquilino o de un valor propio, se aplica una vez aceptado el secreto. El dispositivo elige su propia credencial y solo se conserva un resumen de ella. **El secreto de la flota es el ancla de confianza del primer contacto.** Quien lo tenga puede registrar cualquier número de serie que nadie haya registrado aún con `ALLOW_NEW`, o cualquier número de serie reservado que aún no se haya registrado con `CHECK_PRE_PROVISIONED`, hasta el `maxDevices` del perfil. Nunca puede apropiarse de un dispositivo que ya tiene una credencial ni de uno que no se reservó para el perfil. Si se filtra, [rótalo y revoca lo que registró](../guides/device-self-registration.md#compromise).
- **Certificados de cliente.** Un dispositivo puede autenticarse en el broker MQTT de la plataforma con un certificado de cliente X.509, demostrando su clave en el handshake TLS, de modo que no se guarda ni se envía nada reproducible. La plataforma comprueba la cadena contra un ancla de confianza que el tenant añadió, en cada conexión, exigiendo un uso de clave clientAuth y rechazando SHA-1, claves débiles y CA con restricciones de nombre. **El ancla de confianza es el límite de la identidad:** quien pueda obtener un certificado de esa CA puede registrarse con un perfil de certificado, hasta su tope de dispositivos, así que ancla en la CA más estrecha y deshabilita el ancla ante un compromiso. El tenant sale del usuario MQTT, nunca del certificado. El client id de MQTT queda fijado al dispositivo, de modo que un certificado no puede apoderarse de la sesión de otro dispositivo, y una primera conexión cuyo client id no coincide se rechaza antes de escribir nada. Una sesión termina en la caducidad más temprana de la cadena. No hay CRL ni OCSP; revoca deshabilitando la credencial o el ancla, lo que termina las sesiones abiertas, y usa certificados de corta duración. Consulta [Identidad por certificado](./device-certificates.md).
- **Credenciales en el cuerpo de un evento.** En el broker MQTT de la plataforma la conexión ya ha autenticado al dispositivo, así que
  se rechaza un evento que además lleve una credencial en su cuerpo, nunca se acepta con la credencial
  eliminada: un dispositivo que aún la envía deja de informar, y el rechazo se cuenta para poder localizar el dispositivo y rotar su
  credencial. Por HTTP y a través de un broker propio, donde el cuerpo es la vía de la credencial, la contraseña o el token se sella antes de
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
- **Integraciones.** Las [cuentas de servicio](../user-guide/service-accounts.md) de un inquilino intercambian un ID de
  cliente y un secreto por un token de acceso de 5 minutos (la concesión OAuth `client_credentials`), sin token de
  actualización y sin el inicio de sesión de ninguna persona. Una cuenta tiene exactamente los permisos de sus roles, su
  secreto solo se guarda como un resumen con clave, y lo que hace se audita con su propio nombre.
- **Integraciones de entrada.** Un servidor de red LoRaWAN o un remitente de webhook se autentica con un token
  bearer por integración, generado por el servidor, mostrado una sola vez y almacenado solo como un resumen con clave. El
  token se comprueba antes de leer el cuerpo y otra vez cuando se procesa el evento. Habla en nombre de todos los
  dispositivos de sus tipos en alcance que tengan un alias, así que limita bien el alcance de una
  [integración](../guides/inbound-integrations.md#trust) y rota su token si pudo filtrarse.
- **Inicio de sesión único.** Un inquilino puede exigir el inicio de sesión mediante su propio proveedor de identidad. Consulte
  [Inicio de sesión único](./single-sign-on.md).
- **Agentes de IA.** El [servidor MCP](./mcp.md) opcional llama a la plataforma con el token del propio llamador. No tiene ningún token
  propio, así que un agente solo puede leer lo que puede leer su usuario.
- **Límites de abuso.** La ingesta tiene un techo de tasa por inquilino, los conectores de salida tienen techos por inquilino, el servidor MCP
  limita a cada llamador, las solicitudes GraphQL tienen ejecución y tamaño de cuerpo acotados, y los intentos fallidos de inicio de sesión tienen límite de tasa.
  No hay un límite de tasa de solicitudes por inquilino en la propia API GraphQL.

## Autenticación en dos pasos {#two-factor}

Una cuenta puede exigir un código de una aplicación de autenticación después de su contraseña. [Cómo usarla](../guides/two-factor-authentication.md); aquí se explica qué garantiza y dónde termina.

- **El segundo factor controla la emisión del token.** Cuando la contraseña de una cuenta con segundo factor es correcta, el inicio de sesión no emite nada: ni sesión ni token, solo un desafío de corta duración. Los tokens se emiten cuando se acepta el código. Como el token no existe hasta entonces, también queda cubierta la API de administración de los operadores, que confía en cualquier token válido.
- **El servidor registra cómo demostró la persona quién es.** Todo token derivado del inicio de sesión lleva una afirmación `amr`: `["pwd","otp","mfa"]` para un código de la aplicación, `["pwd","mfa"]` para un código de recuperación y `["pwd"]` para una contraseña sola. Se lee del registro de la sesión, nunca del cliente. La exigencia de un inquilino se comprueba ahí en cada concesión de entrada al inquilino: al seleccionarlo, al renovar una sesión y en el paso de autorización de una conexión MCP u OAuth. Una sesión anterior a la actualización no registra ningún `amr`, así que un inquilino que exige el factor la rechaza. Los tokens de servicio y los de cuentas de servicio nunca llevan la afirmación, y un token que la declara es rechazado.
- **Secretos en reposo.** El secreto del autenticador se guarda cifrado en el [almacén de secretos](#secrets) bajo la clave raíz de la instancia. Se lee solo para comprobar un código, se devuelve una vez al configurarlo y nunca más, y no se guarda en caché. Un código de recuperación lleva 80 bits aleatorios y se guarda solo como un resumen unidireccional.
- **Un código sirve una sola vez.** Un código solo se acepta para un intervalo posterior al último aceptado, y dos peticiones con el mismo código producen un único ganador. Se aceptan códigos de un intervalo de 30 segundos a cada lado del momento actual.
- **Los intentos se cuentan antes de comprobarse.** Cada código incorrecto se carga a la cuenta antes de evaluarse, de modo que una ráfaga de intentos simultáneos no puede superar el límite. Tras 5 códigos incorrectos desde una misma dirección de origen, esa dirección queda bloqueada, y tras 20 desde todas las direcciones juntas el autenticador se bloquea para todos; cada bloqueo dura un minuto, duplicándose con cada fallo adicional hasta 15 minutos. Un intento bloqueado recibe exactamente la misma respuesta que un código incorrecto. Los códigos de recuperación están exentos del bloqueo, porque no se pueden adivinar.
- **Las cuentas de servicio, los dispositivos y el inicio de sesión único quedan fuera.** Una cuenta de servicio nunca inicia sesión con contraseña. Una sesión de inicio de sesión único se rige por los métodos de autenticación que exige su conexión, no por el ajuste de un inquilino. Consulta [Inicio de sesión único](./single-sign-on.md).

Lo que no hace, dicho con claridad:

- **Frente a quien ya tiene la contraseña, frena los intentos pero no los detiene.** Cada intento de adivinar un código viene de alguien que superó el paso de la contraseña. En el límite, después de los primeros 20 intentos, un atacante obtiene unos cuatro intentos por hora, y cada uno tiene tres códigos válidos entre un millón que acertar. Quien siga adivinando a ese ritmo durante un año tiene alrededor de un **10 % de probabilidad** de entrar. La alerta `MfaIdentityLocked` se dispara con el primer bloqueo por esa razón, y su remedio es restablecer la contraseña.
- **Quien conoce la contraseña puede mantener bloqueado el autenticador del dueño.** Renovar el bloqueo exige un código incorrecto cada vez que caduca. El dueño aún puede iniciar sesión con un código de recuperación, y restablecer la contraseña lo termina. No hay bloqueo permanente, porque uno permanente permitiría a cualquiera que conozca una dirección dejar fuera a su dueño.
- **Un emisor que controle la cabecera de dirección reenviada puede renovar la asignación por dirección.** El límite global entre todas las direcciones se mantiene.
- **La exigencia de un inquilino obliga a ese inquilino, no a los operadores.** Decide quién puede entrar en el inquilino. No controla la consola de administración ni la API de administración, y todavía no existe una exigencia para toda la instancia dirigida a los operadores. Un superusuario con una sesión solo de contraseña puede entrar en un inquilino que exige el factor, y cada entrada se audita. Los operadores deberían activarlo, y el registro de auditoría y las alertas son la forma de vigilar los intentos contra sus cuentas.
- **El desafío indica que la contraseña era correcta.** Es inherente a cualquier segundo factor que sigue a la contraseña. No se revela nada más sobre la cuenta hasta que se acepta el código.
- **Los códigos de recuperación se consumen antes de emitir el token.** Si el servicio falla entre ambos pasos, el código se pierde y la persona inicia sesión de nuevo con otro.

## Endpoints que nunca salen del clúster {#internal-endpoints}

Dos endpoints existen solo para los servicios de la propia plataforma. `/auth/service-token` intercambia el secreto
de servicio compartido por un token de corta duración, y `/auth/jwks` es el conjunto de claves que todos los servicios
usan para validar tokens. `user-management` sirve ambos en un puerto interno independiente (8082), no en el público.
El chart publica ese puerto solo en el Service interno del clúster. Ningún ingress, ruta ni balanceador de carga hace
referencia a él.

Por eso el puerto público responde 404 para ambas rutas, sea cual sea lo que haya delante, incluso si no hay ningún
ingress. Nada en el borde es un control de seguridad:

- **Un controlador de ingress propio no necesita ninguna regla para estas rutas.** Enruta a los Services que crea
  el chart, como harías de todos modos, y el emisor de tokens no es alcanzable desde fuera.
- **El emisor de tokens está protegido por el secreto de servicio compartido.** El secreto se compara en tiempo
  constante, y una instancia sin secreto configurado se niega a emitir. El puerto interno mantiene el endpoint fuera
  de la red pública; el secreto es lo que autentica a quien logra alcanzarlo.
- **El chart no establece ninguna anotación snippet de nginx.** Un snippet de configuración permite a quien pueda
  escribir un objeto Ingress inyectar configuración de nginx, y aquí nada necesita uno. `ingress-nginx`, en un clúster que
  aún lo ejecuta, sigue aceptando snippets en esta versión, porque las instancias que aún no se han actualizado
  dependen de ellos; una versión posterior los desactivará cuando ninguna instancia los use.
- **Una política de red restringe aún más el puerto interno, pero es defensa en profundidad y está desactivada por
  defecto.** Con `networkPolicy.enabled` el chart limita el puerto a los pods del namespace de la propia instancia, en
  un clúster cuyo plugin de red aplique políticas. Las instalaciones de `dcctl` se ejecutan con la política desactivada.
- **Un verificador de tokens externo usa en su lugar el conjunto de claves público.** Los metadatos del servidor de
  autorización (`jwks_uri`) apuntan a `/oauth/jwks`, que sirve las mismas claves públicas cuando hay una URL de emisor
  configurada.

Si ejecutas tu propio motor de políticas, permite que los pods del namespace de la instancia lleguen al puerto 8082 de
`user-management`, y nada más. Cada servicio lo usa para obtener el conjunto de claves al arrancar y para emitir su token
de servicio.

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
3. **Confirmaciones MQTT que el broker puede perder.** Por defecto, el broker puede confirmar un mensaje QoS 1 y
   perderlo después de dos formas. Una es mientras el flujo de captura cambia de líder, algo que provoca un reinicio del broker, una
   actualización o la pérdida de un nodo, con cualquier carga. La otra no necesita ningún reinicio: con carga sostenida, si
   `event-sources` se queda atrás y la cola del flujo de captura alcanza su límite, el flujo descarta sus mensajes sin leer más
   antiguos. `devicechain_<area>_jetstream_consumer_unread_skipped_total` los cuenta y salta la alerta
   `JetStreamDurableLostUnread`. Con `infrastructure.nats.mqttFailClosed` activado, una
   confirmación significa que la plataforma conserva el mensaje; a cambio, el broker rechaza las publicaciones QoS 1
   mientras no hay líder o su almacén de mensajes está lleno, y la instancia sostiene menos tráfico, así que es para instalaciones dimensionadas para
   mantenerse por debajo de ese techo. El ajuste está desactivado por defecto; `--mqtt-fail-closed` en `dcctl bootstrap` o `dcctl upgrade` lo activa, y
   [Observabilidad](../deployment/observability.md#mqtt-fail-closed) describe el equilibrio.
4. **La clave raíz es legible para cualquiera que pueda leer el Secret de Kubernetes de la instancia.** Restrinja el acceso a ese
   Secret y al etcd del clúster en consecuencia.
5. **La autenticación en dos pasos protege la contraseña, y solo la contraseña.** No obliga al plano de administración de los operadores a cumplir la exigencia de un inquilino, un atacante que conoce una contraseña y sigue adivinando al límite de bloqueo tiene alrededor de un 10 % de probabilidad al año, y quien conoce una contraseña puede mantener bloqueado el autenticador de una cuenta renovando el bloqueo. Consulta [Autenticación en dos pasos](#two-factor).
6. **No se ha publicado ninguna prueba de penetración externa.**

Nada de esta página sustituye su propia revisión. Si una entrada de aquí es un problema para su despliegue, la sección
correspondiente de arriba indica dónde está el control.
