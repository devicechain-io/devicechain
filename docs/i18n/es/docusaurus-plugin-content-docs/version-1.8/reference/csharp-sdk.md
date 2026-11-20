---
sidebar_position: 4
title: SDK de C# y Unity
---

# SDK de C# y Unity {#c-and-unity-sdks}

Instala el paquete de .NET con `dotnet add package DeviceChain.Sdk`. Está dirigido a
.NET 8 y .NET Standard 2.1. Tú proporcionas el origen de la plataforma; no hay un host predeterminado.
El paquete de Unity añade transportes y un mecanismo de procesamiento de suscripciones en el hilo principal a esta misma API.

## Cliente y autenticación {#client-and-authentication}

```csharp
using System;
using DeviceChain.Sdk;

await using var client = new DeviceChainClient(new Uri("https://devicechain.example.com"));
await client.LoginAsync("me@example.com", "your-password");
await client.SelectTenantAsync("acme");
```

`LoginAsync(email, password, cancellationToken)` devuelve `IdentityAuth` con
`IdentityToken`, `ExpiresAt`, `Superuser` y `Memberships`. Para una cuenta con un
segundo factor, lanza `DeviceChain.Sdk.Auth.MfaRequiredException`: pasa su
`Challenge` y el código de la persona a `VerifyMfaAsync(challenge, code, cancellationToken)`
antes de seleccionar un inquilino. Consulta el [inicio de sesión con dos factores](../guides/two-factor-authentication.md#csharp).

`SelectTenantAsync(tenant, cancellationToken)` devuelve un `AuthToken` que contiene
`AccessToken`, `RefreshToken` y `ExpiresAt`. Todos los tokens de cancelación de estas llamadas
son opcionales. `client.Auth` expone los valores `Memberships`, `Superuser` y el
`AccessToken` actual de la sesión. Usa `GetAccessTokenAsync(cancellationToken)` para obtener un token
que se renueva si está próximo a caducar, o `RefreshAsync(cancellationToken)` para rotarlo explícitamente.
El cliente conecta `GetAccessTokenAsync` con la autenticación de HTTP y de las suscripciones.

## Consultas y suscripciones {#queries-and-subscriptions}

`client.Gql.SendAsync(area, query, variables, variablesInfo, dataInfo, options, cancellationToken)`
devuelve el **objeto de datos** GraphQL deserializado, no un contenedor de respuesta. `variablesInfo`
y `dataInfo` son valores `JsonTypeInfo<T>` proporcionados por el contexto de
System.Text.Json generado a partir del código fuente de tu aplicación. `options` y `cancellationToken` son opcionales; usa
`new RequestOptions { Anonymous = true }` para omitir el token de portador.

Por ejemplo, en un proyecto de .NET con el generador de código fuente de System.Text.Json:

```csharp
using System;
using System.Text.Json;
using System.Text.Json.Serialization;
using DeviceChain.Sdk;
using DeviceChain.Sdk.Json;

await using var client = new DeviceChainClient(new Uri("https://devicechain.example.com"));
await client.LoginAsync("me@example.com", "your-password");
await client.SelectTenantAsync("acme");

JsonElement data = await client.Gql.SendAsync(
    Area.DeviceManagement,
    "query{devices(criteria:{pageNumber:1,pageSize:10}){results{token name}}}",
    EmptyVariables.Value, AppJson.Default.EmptyVariables, AppJson.Default.JsonElement);
Console.WriteLine(data.GetProperty("devices"));

[JsonSerializable(typeof(EmptyVariables))]
[JsonSerializable(typeof(JsonElement))]
internal partial class AppJson : JsonSerializerContext { }
```

Sustituye `JsonElement` por tu DTO y sus metadatos generados para obtener resultados tipados.
`EmptyVariables` está en `DeviceChain.Sdk.Json`; el contexto `SdkJson` del propio SDK es interno.

`client.Subscriptions(area).SubscribeAsync(query, variables, variablesInfo, dataInfo, cancellationToken)`
devuelve `IAsyncEnumerable<TData>`. Comparte un socket por área y acepta los mismos
metadatos de serialización que HTTP. Cancela la enumeración con el token opcional y libera
el cliente al finalizar. Si un socket se desconecta o caduca, el consumidor debe volver a
suscribirse; la nueva conexión obtiene un token nuevo. Consulta el historial perdido tras reconectarte.
`client.WebSocketUri(area)` devuelve el endpoint de suscripción; `Areas.Path(area)` devuelve
su ruta HTTP. Usa el enum `Area` en lugar de un nombre de área como cadena.

`GraphQlRequestException` expone `Status`, `Errors` y el valor opcional `RetryAfter`.
Cada `GraphQlError` expone `Message` y `Code`; examina estos valores en lugar de comparar el texto del mensaje.

## Publicación de telemetría {#publishing-telemetry}

`client.DevicePublisher(ingressOrigin, instanceId, tenant)` crea un publicador HTTP.
El origen de entrada de dispositivos es distinto del origen GraphQL de la plataforma.
`EmitMeasurementsAsync(deviceToken, credentialId, metrics, occurredTime, cancellationToken)`
acepta un `IReadOnlyDictionary<string, double>`.
`EmitLocationAsync(deviceToken, credentialId, fix, occurredTime, cancellationToken)` acepta
`new LocationFix(latitude, longitude)` con los campos opcionales `Elevation`, `Accuracy`, `Speed` y
`Heading`. Ambos métodos devuelven `Task`; el instante del evento y el token de cancelación son opcionales.
Las coordenadas son grados WGS84, la elevación se expresa en metros sobre el elipsoide, la precisión en metros,
la velocidad en metros por segundo y el rumbo en grados en sentido horario desde el norte verdadero. Usa
`LocationFix.CanonicalHeading(degrees)` para normalizar un rumbo; deja los campos desconocidos en null.

Para MQTT, construye `new DeviceEventPublisher(new MqttDeviceEventCarrier(session, deviceToken))`.
La sesión ya debe estar iniciada y el token debe corresponder a su dispositivo. Los métodos de emisión
siguen aceptando `credentialId`, pero el transporte MQTT lo omite del cuerpo porque CONNECT
ya ha autenticado el dispositivo. Un `IDeviceEventCarrier` personalizado debe implementar
`CarriesBodyCredential` y `SendAsync(deviceToken, jsonEvent, cancellationToken)`.

## Sesiones de dispositivos MQTT {#mqtt-device-sessions}

Los tipos están en `DeviceChain.Sdk.Mqtt` y `DeviceChain.Sdk.Transport`.

```csharp
var options = new MqttSessionOptions(brokerUri, instanceId, tenant, deviceToken, credentialId);
await using var session = new MqttDeviceSession(options);
await session.StartAsync(async (command, ct) =>
{
    // Apply the command to your device before returning its outcome.
    await ApplyCommandAsync(command, ct); // your application function
    return CommandOutcome.Succeeded();
}, cancellationToken);
```

En este fragmento, el host proporciona la URI, la instancia, el inquilino, la credencial del dispositivo,
el token de cancelación y la función `ApplyCommandAsync(DeviceCommand, CancellationToken)`.
`CommandHandler` devuelve `Task<CommandOutcome>`; usa `CommandOutcome.Failed(error)` para un
fallo o `CommandOutcome.Succeeded(payload)` para un éxito, donde payload es texto JSON opcional.
`DeviceCommand` contiene `Token`, `Name`, un `JsonElement` opcional en `Payload` y la secuencia de llegada `Sequence`.
`StartAsync` requiere tanto el controlador como el token de cancelación y espera la confirmación de la suscripción.
Observa `State` y `StateChanged` (`Starting`, `Ready`, `Reconnecting`, `Blind`, `Stopped`),
`RunningCommands`, `QueuedCommands` y `MalformedFrames` para supervisar el estado de funcionamiento.

Entre los ajustes útiles de `MqttSessionOptions` están `Trust` (raíces del sistema de forma predeterminada),
`ClientCertificate`, `ClientIdDiscriminator`, `KeepAlive` (60 segundos),
`OperationTimeout` (30 segundos), `CommandHistorySize` (256) y los retardos de reconexión
`ReconnectInitialDelay` / `ReconnectMaxDelay` (1 / 30 segundos).
`MaxConcurrentCommands` tiene un valor predeterminado de 1. Con valores superiores a 1, usa `CommandLane` para conservar el orden
de los comandos relacionados; `CommandShutdownTimeout` limita la espera de los controladores al liberar recursos
(5 segundos de forma predeterminada, con un máximo de `MaxCommandShutdownTimeout`, 24 días).
Consulta el [README del SDK](https://github.com/devicechain-io/devicechain/tree/main/sdks/csharp#running-commands-concurrently)
para conocer el comportamiento de la ejecución concurrente y la entrega duplicada, y la
[autenticación con certificados](../guides/device-x509.md#csharp-sdk) para la identidad y la confianza TLS.

## Recepción de comandos mediante consultas HTTP {#http-command-pulling}

`DeviceChain.Sdk.Commands.HttpCommandPuller` se construye por separado de la fachada:
`new HttpCommandPuller(transport, ingressOrigin, instanceId, tenant, deviceToken, accessToken)`.
Usa el token de acceso del dispositivo y un `IHttpTransport`, como `HttpClientTransport` que envuelva
un `HttpClient` propiedad del host. El tipo de dispositivo debe tener habilitado `HTTP_PULL`.

1. `PullAsync(waitSeconds, cancellationToken)` devuelve `IReadOnlyList<PulledCommand>`;
   la espera predeterminada es `DefaultWaitSeconds` (25), con un intervalo de 0 a `MaxWaitSeconds` (30).
   Una lista vacía significa que no ha llegado ningún comando.
2. Llama a `AcknowledgeAsync(command.Token, cancellationToken)` antes de actuar. `Attempt`
   identifica una posible reentrega, así que haz que la operación del dispositivo sea idempotente.
3. Si `DeliveryMode` es `"ONEWAY"`, la confirmación completa la entrega; no respondas.
   En caso contrario, llama a `RespondAsync(command.Token, success, payload, error, cancellationToken)`
   después de actuar. `payload` es un `JsonElement` opcional y `error` una cadena opcional.

`PulledCommand` también expone `Name` y los valores opcionales `Payload` y `ExpiresAt`.
Los tokens de cancelación de estos métodos del receptor son opcionales. Los fallos usan
`GraphQlRequestException`; respeta `RetryAfter` cuando el servidor limite la frecuencia de las consultas.

## Unity {#unity}

Prepara el SDK con `sdks/unity/stage-sdk.sh` y luego añade
`sdks/unity/io.devicechain.sdk/package.json` desde el disco en el Package Manager de Unity.
El paquete requiere Unity 2021.3 o posterior y el perfil de API .NET Standard 2.1.

`DeviceChain.Sdk.Unity.DeviceChainRuntime.CreateClient(origin)` acepta un `Uri` o una
cadena y devuelve el mismo `DeviceChainClient` liberable que se muestra arriba. Usa
`UnityWebRequestHttpTransport` en todas las plataformas, `ClientWebSocketFactory` en las plataformas
nativas y `WebGlWebSocketFactory` en el reproductor WebGL. `DeviceChainPump` procesa
automáticamente las suscripciones WebGL en el hilo principal. La ruta de ejecución en el reproductor
WebGL sigue sin verificarse; MQTT requiere una compilación nativa.

Construye el cliente de Unity en el hilo principal para que su transporte HTTP capture el
contexto de sincronización de Unity. Las solicitudes HTTP y las interrupciones por cancelación se envían
automáticamente a ese contexto. Inicia la enumeración de suscripciones en el hilo principal para
conservar ese contexto; envía a él los cambios de escena desde los controladores MQTT nativos. Compila tus DTO de consulta y el contexto JSON generado a partir del código fuente en un ensamblado de .NET
antes de importarlos en Unity. Libera el cliente cuando finalice su propietario.

Un host que proporciona sus propios transportes usa
`new DeviceChainClient(origin, httpTransport, webSocketFactory, readLoopDriver)`;
el cliente no es propietario de ninguno de los dos transportes inyectados. `readLoopDriver` es opcional en los hosts
con hilos. Para un bucle WebGL personalizado, pasa un `ManualPumpDriver` y llama a `Pump()` en cada fotograma,
o usa `DeviceChainPump.Attach(driver)` y `Detach(driver)` para gestionar el procesamiento en Unity.
El constructor de .NET sin transportes inyectados puede reutilizar un `HttpClient` propiedad del llamador; en caso contrario, es propietario
del que crea y lo libera.
