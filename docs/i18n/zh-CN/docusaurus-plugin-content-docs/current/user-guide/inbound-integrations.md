---
title: 入站集成
sidebar_position: 12.6
---

# 入站集成 {#inbound-integrations}

有些设备从不自己连接 DeviceChain。LoRaWAN 传感器与网关通信，由 ChirpStack 或 The Things Stack 这类**网络服务器**把解码结果通过 HTTPS 转发过来。**入站集成**（inbound integration）就是这种转发的接收端。它给发送方一个 URL 和一个承载令牌，找到每条消息所属的设备，并把读数存为普通测量值。

本章面向**租户管理员**，介绍控制台一侧：创建集成、连接发送方、为设备设置发送方使用的标识符，以及查看哪些数据到达了却无法使用。完整的约定（URL、限制、重复和重试如何处理，以及发送方会收到什么应答）参见[入站集成指南](/guides/inbound-integrations)。

**Integrations**（集成）位于侧边栏的 **Connect**（连接）下。该页面需要 `device:read` 权限才能打开，需要 `device:write` 权限才能做出任何更改。仅限于租户部分客户或分组的成员看不到它。一个租户可以拥有多少个集成由其层级的设置决定；创建超出上限的集成会被拒绝。

## 集成列表 {#the-list}

列表显示每个集成的名称、令牌和发送方类型、它可以访问的设备类型、最近一次请求认证失败的时间，以及状态。有三种徽标需要留意：

- **Disabled**（已禁用）：集成不接受数据。
- **Rejected token**（令牌被拒）：针对该集成的请求认证失败。请检查发送方出示的令牌。
- **N undecoded**（N 条未解码）：到达了无法从中读出读数的上行消息。参见[无可读内容的上行消息](#undecoded)。

## 创建集成 {#creating}

开始之前，请为发送方背后的设备准备好设备类型，最好为每个想要带单位的解码值声明一个[指标](/user-guide/device-types-and-profiles#metrics)。然后选择 **New integration**（新建集成）并填写：

- **Sender type**（发送方类型）：**ChirpStack v4**、**The Things Stack v3**、**Generic JSON** 或 **Generic binary**。它决定如何解析消息体，创建之后**无法更改**。
- **Token**（令牌）：发送方所投递 URL 的一部分。创建之后无法更改。发送方用来认证的承载令牌由系统为你生成。
- **Name**（名称）和 **Description**（描述）。
- **Device types**（设备类型）：无论发送方声称什么，只有这些类型的设备才能通过该集成访问。至少选择一个。
- **Device identifier**（设备标识符，网络服务器）：发送方如何称呼设备，用 **DevEUI**，或者在 The Things Stack 中用网络服务器里的 **Device id**。
- **Store link metrics**（存储链路指标）：在每条已解码的上行消息旁存储信号强度、信噪比和网关数量。
- **Accepting data**（接受数据）：关闭后立即停止通过该集成解析设备。

对于通用发送方，还会出现 **Where the data is**（数据所在位置）部分：

- **Identifier scheme name**（标识符方案名称）：发送方的标识符在设备上存储时使用的名称，即别名 `custom.<name>`。请使用小写字母、数字、连字符和下划线。
- **Identifier comes from**（标识符来源）：**Header**（请求头）、**Body field**（消息体字段）或 **Query parameter**（查询参数）。二进制消息体没有字段，所以 **Generic binary** 只能用请求头或查询参数。
- **Identifier location**（标识符位置）：请求头名称（例如 `X-Device-Id`）、指向字段的 JSON 指针（例如 `/device/id`）或查询参数名称。
- **Items array**（条目数组，可选）：指向消息体中某个数组的 JSON 指针；每个元素是一条上行消息。当消息体本身就是一条上行消息时留空。
- **Sender time**（发送方时间，可选）：指向发送方记录的时间的 JSON 指针。不设置时使用接收时间，重试的请求会被存储两次。
- **Raw frame**（原始帧）和 **Frame encoding**（帧编码）（可选，仅 JSON）：编码后的原始帧所在位置，留给解码器使用。该帧目前不会被解码。

选择 **Create integration**。接下来的对话框 **Integration created**（集成已创建）会显示**承载令牌**。**它只显示一次。**之后无法再次读取；如果丢失，请轮换。请现在就把它填入发送方。

## 连接发送方 {#connecting}

选择一个集成即可打开其页面。**Connect the sender**（连接发送方）按该发送方类型列出需要在发送方中填写的内容，每个值旁都有复制按钮：端点 URL、请求头名称和请求头值。承载令牌本身不会在这里再次显示。

### ChirpStack v4 {#chirpstack}

在 ChirpStack 中，打开应用程序，添加一个 **HTTP** 集成，并按页面内容填写：

- 将 **Payload encoding** 设为 **JSON**。Protobuf 集成会被拒绝，并给出指明该设置的消息。
- 填入页面上的 **Event endpoint URL**。ChirpStack 会自己在 URL 中加上事件类型；只读取上行消息，其他事件会被确认并忽略。
- 添加请求头 `Authorization`，值为 `Bearer <承载令牌>`。
- 在 ChirpStack 设备配置文件上设置**编解码器**。没有它，上行消息不带解码值，会被计为未解码。

### The Things Stack v3 {#the-things-stack}

在 The Things Stack 中，打开应用程序，添加一个 JSON 格式的 **Custom webhook**，并填入页面上的 **Base URL** 和 `Authorization` 请求头。在页面列出的路径之一上启用上行消息。其他路径上的消息会被确认并忽略。请在应用程序或设备上设置 **payload formatter**，否则上行消息会被计为未解码。

### 通用发送方 {#generic}

对于 **Generic JSON** 和 **Generic binary**，页面会给出一个示例请求，展示标识符放在何处。通用发送方必须把每条上行消息作为 HTTP `POST` 发送，并在 `Authorization` 请求头中带上承载令牌。具体的请求形式见指南中的 [Generic JSON](/guides/inbound-integrations#generic-json) 和 [Generic binary](/guides/inbound-integrations#generic-binary) 部分。Generic binary 只统计上行消息，并保留前若干字节作为样本：它用来查看发送方发来了什么，而不是用来存储读数。

### 发送方的证书 {#sender-certificate}

托管的网络服务器不会信任提供自签名证书的实例。如果你的实例就是这样，请在连接托管的发送方之前，向运维人员索取公共 CA 签发的证书。

## 为设备设置发送方的标识符 {#aliases}

消息按发送方的叫法称呼设备，而不是按 DeviceChain 的叫法。设备**别名**会把这个名称记录在设备上。打开设备表单，使用 **Aliases** 字段：选择 **Add alias**，选取 **LoRaWAN DevEUI**、**Network server device id** 或 **Custom scheme**，然后输入值。一台设备最多有八个别名，每种方案一个值，同一方案和值在租户内只能属于一台设备。DevEUI 在存储时会被规范化，因此大小写均可。

集成的 **Device identifier** 决定查找哪种方案：DevEUI 读取别名 `lorawan.deveui`，网络服务器设备 id 读取 `tts.device-id`，通用集成读取 `custom.<name>`。只带有其他方案的设备不会被找到。别名的完整说明见[用别名标识设备](/guides/inbound-integrations#aliases)。

## 已见到的未注册设备 {#unregistered}

你不必先创建设备。在集成页面上，**Unregistered devices seen**（已见到的未注册设备）列出该集成收到、但其范围内没有任何设备携带的标识符，包括方案、标识符、被见到的次数和最近一次见到的时间。每一项旁边的 **Create a device with this alias**（用此别名创建设备）会打开已填好别名的设备表单；设备创建之后，其数据便开始被存储。

该列表仅用于观察。**Clear list**（清空列表）会忘掉列表中的全部内容，发送方仍在发送的标识符会再次出现。

## 流量与问题 {#traffic}

集成页面上的 **Traffic**（流量）面板显示该集成收到、但无法使用的内容：有多少条上行消息没有可读内容、最近一条何时到达、其最后一帧的十六进制样本、最近一次请求被拒绝的时间，以及令牌的生成时间。

### 无可读内容的上行消息 {#undecoded}

当上行消息不含任何带数值的已解码对象时，就会被计为未解码。对于网络服务器，常见原因是缺少编解码器或 payload formatter：请在那里的设备配置文件上设置一个，并使用 JSON 编码。DeviceChain 不会解码原始帧。

### 令牌被拒 {#rejected-token}

**Rejected token** 表示有请求认证失败。发送方可能持有旧令牌，例如在轮换之后。请检查请求头的值，或轮换令牌并填入新令牌。

## 轮换令牌与删除 {#rotating}

- **Rotate token**（轮换令牌）会替换承载令牌。旧令牌立即失效，没有重叠期，因此请马上更新发送方。新令牌只显示一次。
- **Delete**（删除）会移除该集成。发送方立即被拒绝。设备上的别名会保留。

## 相关内容

- [入站集成指南](/guides/inbound-integrations)：URL、限制、重复与重试，以及故障排查。
- [设备](/user-guide/devices)：设备表单及其别名。
- [设备类型与配置文件](/user-guide/device-types-and-profiles)：声明已解码值所对应的指标。
- [预配置与信任锚](/user-guide/provisioning-and-trust-anchors)：自行连接的设备。
