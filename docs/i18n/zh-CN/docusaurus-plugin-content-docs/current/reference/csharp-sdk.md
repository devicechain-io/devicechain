---
sidebar_position: 4
title: C# 和 Unity SDK
---

# C# 和 Unity SDK {#c-and-unity-sdks}

使用 `dotnet add package DeviceChain.Sdk` 安装 .NET 包。它面向
.NET 8 和 .NET Standard 2.1。你需要提供平台源地址；没有默认主机。
Unity 包在同一套 API 上增加了传输实现和主线程订阅处理机制。

## 客户端与身份验证 {#client-and-authentication}

```csharp
using System;
using DeviceChain.Sdk;

await using var client = new DeviceChainClient(new Uri("https://devicechain.example.com"));
await client.LoginAsync("me@example.com", "your-password");
await client.SelectTenantAsync("acme");
```

`LoginAsync(email, password, cancellationToken)` 返回 `IdentityAuth`，其中包含
`IdentityToken`、`ExpiresAt`、`Superuser` 和 `Memberships`。对于启用了
第二因素的账户，它会抛出 `DeviceChain.Sdk.Auth.MfaRequiredException`：在选择租户之前，
将异常的 `Challenge` 和用户的验证码传给 `VerifyMfaAsync(challenge, code, cancellationToken)`。
参见[双因素登录](../guides/two-factor-authentication.md#csharp)。

`SelectTenantAsync(tenant, cancellationToken)` 返回包含
`AccessToken`、`RefreshToken` 和 `ExpiresAt` 的 `AuthToken`。这些调用中的所有取消令牌
都是可选的。`client.Auth` 公开会话的 `Memberships`、`Superuser` 和当前的
`AccessToken`。使用 `GetAccessTokenAsync(cancellationToken)` 获取令牌，它会在令牌
即将过期时刷新；也可以使用 `RefreshAsync(cancellationToken)` 显式轮换令牌。
客户端将 `GetAccessTokenAsync` 接入 HTTP 和订阅身份验证。

## 查询与订阅 {#queries-and-subscriptions}

`client.Gql.SendAsync(area, query, variables, variablesInfo, dataInfo, options, cancellationToken)`
返回反序列化后的 GraphQL **数据对象**，而不是响应封装。`variablesInfo`
和 `dataInfo` 是由应用程序通过源生成器生成的 System.Text.Json 上下文
提供的 `JsonTypeInfo<T>`。`options` 和 `cancellationToken` 是可选的；使用
`new RequestOptions { Anonymous = true }` 可跳过持有者令牌。

例如，在使用 System.Text.Json 源生成器的 .NET 项目中：

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

将 `JsonElement` 替换为你的 DTO 及其生成的元数据，即可获得强类型结果。
`EmptyVariables` 位于 `DeviceChain.Sdk.Json` 中；SDK 自身的 `SdkJson` 上下文是内部类型。

`client.Subscriptions(area).SubscribeAsync(query, variables, variablesInfo, dataInfo, cancellationToken)`
返回 `IAsyncEnumerable<TData>`。每个区域共享一个套接字，并使用与 HTTP 相同的
序列化元数据。使用可选令牌取消枚举，并在结束时释放客户端。
套接字断开或过期后，使用方需要重新订阅；新连接会获取新的令牌。
重新连接后，请查询遗漏的历史记录。
`client.WebSocketUri(area)` 返回订阅端点；`Areas.Path(area)` 返回
其 HTTP 路径。请使用 `Area` 枚举，而不是字符串形式的区域名称。

`GraphQlRequestException` 公开 `Status`、`Errors` 和可选的 `RetryAfter`。
每个 `GraphQlError` 公开 `Message` 和 `Code`；请检查这些字段，而不是匹配消息文本。

## 发布遥测数据 {#publishing-telemetry}

`client.DevicePublisher(ingressOrigin, instanceId, tenant)` 创建 HTTP 发布器。
设备入口的源地址与平台 GraphQL 的源地址相互独立。
`EmitMeasurementsAsync(deviceToken, credentialId, metrics, occurredTime, cancellationToken)`
接受 `IReadOnlyDictionary<string, double>`。
`EmitLocationAsync(deviceToken, credentialId, fix, occurredTime, cancellationToken)` 接受
`new LocationFix(latitude, longitude)`，并可指定 `Elevation`、`Accuracy`、`Speed` 和
`Heading`。这两个方法都返回 `Task`；发生时间和取消令牌都是可选的。
坐标以 WGS84 度数表示，高程为相对于椭球面的米数，精度以米为单位，
速度以米每秒为单位，航向为从真北顺时针计算的度数。使用
`LocationFix.CanonicalHeading(degrees)` 规范化方位角；未知字段保留为 null。

对于 MQTT，构造 `new DeviceEventPublisher(new MqttDeviceEventCarrier(session, deviceToken))`。
会话必须已经启动，且令牌必须与会话的设备匹配。发送方法
仍接受 `credentialId`，但 MQTT 载体会将其从消息体中省略，因为 CONNECT
已经完成了设备身份验证。自定义 `IDeviceEventCarrier` 必须实现
`CarriesBodyCredential` 和 `SendAsync(deviceToken, jsonEvent, cancellationToken)`。

## MQTT 设备会话 {#mqtt-device-sessions}

相关类型位于 `DeviceChain.Sdk.Mqtt` 和 `DeviceChain.Sdk.Transport` 中。

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

在此片段中，宿主提供 URI、实例、租户、设备凭据、取消令牌，以及
`ApplyCommandAsync(DeviceCommand, CancellationToken)` 函数。
`CommandHandler` 返回 `Task<CommandOutcome>`；失败时使用 `CommandOutcome.Failed(error)`，
成功时使用 `CommandOutcome.Succeeded(payload)`，其中 payload 为可选的 JSON 文本。
`DeviceCommand` 携带 `Token`、`Name`、可选的 `JsonElement` 类型的 `Payload` 和到达顺序 `Sequence`。
`StartAsync` 同时要求处理程序和取消令牌，并等待订阅确认。
通过观察 `State` 和 `StateChanged`（`Starting`、`Ready`、`Reconnecting`、`Blind`、`Stopped`）、
`RunningCommands`、`QueuedCommands` 和 `MalformedFrames` 来了解运行状况。

常用的 `MqttSessionOptions` 设置包括 `Trust`（默认使用系统根证书）、
`ClientCertificate`、`ClientIdDiscriminator`、`KeepAlive`（60 秒）、
`OperationTimeout`（30 秒）、`CommandHistorySize`（256），以及重连延迟
`ReconnectInitialDelay` / `ReconnectMaxDelay`（1 / 30 秒）。
`MaxConcurrentCommands` 默认为 1。大于 1 时，使用 `CommandLane` 保持
相关命令的顺序；`CommandShutdownTimeout` 限制释放资源时等待处理程序的时长
（默认为 5 秒，上限为 `MaxCommandShutdownTimeout`，即 24 天）。
有关并发执行和重复投递的行为，参见
[SDK README](https://github.com/devicechain-io/devicechain/tree/main/sdks/csharp#running-commands-concurrently)；
有关 TLS 身份和信任，参见[证书身份验证](../guides/device-x509.md#csharp-sdk)。

## 通过 HTTP 拉取命令 {#http-command-pulling}

`DeviceChain.Sdk.Commands.HttpCommandPuller` 独立于外观类构造：
`new HttpCommandPuller(transport, ingressOrigin, instanceId, tenant, deviceToken, accessToken)`。
使用设备的访问令牌和一个 `IHttpTransport`，例如封装宿主所拥有的
`HttpClient` 的 `HttpClientTransport`。设备类型必须启用 `HTTP_PULL`。

1. `PullAsync(waitSeconds, cancellationToken)` 返回 `IReadOnlyList<PulledCommand>`；
   等待时间默认为 `DefaultWaitSeconds`（25），范围为 0 到 `MaxWaitSeconds`（30）。
   空列表表示没有命令到达。
2. 在执行操作之前调用 `AcknowledgeAsync(command.Token, cancellationToken)`。`Attempt`
   标识可能的重复投递，因此设备操作应保持幂等。
3. 如果 `DeliveryMode` 为 `"ONEWAY"`，确认即完成投递；不要响应。
   否则，在执行操作后调用 `RespondAsync(command.Token, success, payload, error, cancellationToken)`。
   `payload` 为可选的 `JsonElement`，`error` 为可选字符串。

`PulledCommand` 还公开 `Name`、可选的 `Payload` 和可选的 `ExpiresAt`。
这些拉取器方法中的取消令牌都是可选的。失败通过
`GraphQlRequestException` 报告；服务器限制轮询频率时，请遵循 `RetryAfter`。

## Unity {#unity}

使用 `sdks/unity/stage-sdk.sh` 准备 SDK，然后在 Unity 的 Package Manager 中
从磁盘添加 `sdks/unity/io.devicechain.sdk/package.json`。
该包要求 Unity 2021.3 或更高版本以及 .NET Standard 2.1 API 配置。

`DeviceChain.Sdk.Unity.DeviceChainRuntime.CreateClient(origin)` 接受 `Uri` 或
字符串，并返回与上文相同的可释放的 `DeviceChainClient`。它在所有平台上使用
`UnityWebRequestHttpTransport`，在原生平台上使用 `ClientWebSocketFactory`，
在 WebGL 播放器中使用 `WebGlWebSocketFactory`。WebGL 订阅由
`DeviceChainPump` 在主线程上自动推进。WebGL 播放器中的运行路径
仍未经验证；MQTT 需要原生构建。

在主线程上构造 Unity 客户端，以便其 HTTP 传输捕获 Unity 的
同步上下文。HTTP 请求和取消中止操作会自动调度回
该上下文。在主线程上开始订阅枚举以保留该上下文；将原生 MQTT 处理程序中的
场景更改调度回该上下文。在导入 Unity 之前，先将查询 DTO 和源生成的 JSON 上下文编译到 .NET
程序集中。客户端的所有者结束生命周期时，请释放客户端。

提供自身传输实现的宿主使用
`new DeviceChainClient(origin, httpTransport, webSocketFactory, readLoopDriver)`；
客户端不拥有这两个注入的传输实现。在支持线程的宿主上，`readLoopDriver` 是可选的。
对于自定义 WebGL 循环，传入 `ManualPumpDriver` 并在每帧调用 `Pump()`，
或使用 `DeviceChainPump.Attach(driver)` 和 `Detach(driver)` 管理 Unity 中的推进处理。
普通的 .NET 构造函数可以复用调用方拥有的 `HttpClient`；否则，它会拥有
并释放自己创建的 `HttpClient`。
