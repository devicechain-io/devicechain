---
sidebar_position: 7
title: 为出站 Webhook 签名
---

# 为出站 Webhook 签名 {#signing-outbound-webhooks}

仅凭网络上看到的内容，webhook 接收方无法回答一个问题：这个请求真的来自 DeviceChain 吗，它是否就是发出时的那一个？**签名**可以回答。已签名的投递携带三个请求头，由开放的 [Standard Webhooks](https://www.standardwebhooks.com) 规范定义，因此接收方只需几行代码，或任何实现了该规范的库，就能验证。

签名**默认关闭**。没有要求签名的 webhook 与之前完全一致，逐字节相同。可以签名两类投递：

- **通知 webhook 渠道**：告警触发时通知人员或聊天工具（参见[配置通知渠道](notification-channels.md)）；
- 检测规则上的 **`httpCall` 动作**：向某个系统发送由你塑造的载荷（参见[出站连接器](../concepts/outbound-connectors.md)）。

两种情况下请求体都相同。签名只增加请求头，不改变其他任何内容。

## 已签名的投递携带什么 {#what-a-signed-delivery-carries}

| 请求头 | 值 |
| --- | --- |
| `webhook-id` | 消息的标识。同一条消息的多次重试中保持不变，接收方可据此丢弃重复项。 |
| `webhook-timestamp` | 整数 Unix 秒，每次尝试都会重新取值。接收方会拒绝过旧的时间戳，从而限制被截获的请求能被利用多久。 |
| `webhook-signature` | 一个或多个以空格分隔的条目，格式为 `v1,<base64 的 HMAC-SHA256>`。 |

签名覆盖 `<webhook-id>.<webhook-timestamp>.<原始请求体>`，以你的签名密钥作为密钥。请求体、id 或时间戳改动一个字节，签名就不再匹配。

对于 `httpCall` 动作，`webhook-id` 是该次派发的幂等键，与 `X-DC-Idempotency-Key` 中发送的值相同。对于通知渠道，它是每次投递新生成的 `msg_…` id，该投递重试时沿用。如果消息代理在重启后重新投递整条告警，会产生新的 id，因此要做端到端去重，还应使用载荷中的告警字段。

## 签名密钥 {#the-signing-secret}

签名密钥形如 `whsec_` 后接 base64 文本。它由平台生成，**只在创建它的变更（mutation）的响应中显示一次**，并以密封形式保存在平台的加密密钥存储中。之后无法通过任何 API 读回：列表和查询只会告诉你是否存在。创建时请把它复制到接收方的配置中。

每个通知渠道可以有一个密钥，每个签名句柄也可以有一个。**句柄**是你为 `httpCall` 密钥选的名字（字母、数字和 `- _ . /`，最长 256 个字符）；多条规则可以共用同一个。

## 为通知 webhook 渠道签名 {#sign-a-notification-webhook-channel}

像往常一样创建渠道，再生成它的密钥，然后在渠道的 `config` 中打开签名。需要 `notification:write`。

```graphql
mutation {
  generateNotificationChannelSigningSecret(token: "oncall-hook") {
    secret      # whsec_… — 只显示一次
  }
}
```

```graphql
mutation {
  updateNotificationChannel(token: "oncall-hook", request: {
    config: "{\"url\":\"https://hooks.example.com/alarms\",\"auth\":\"bearer\",\"signing\":true}"
  }) { token hasSigningSecret }
}
```

签名可与渠道的 `auth` 模式组合使用：`bearer` 渠道仍会发送 `Authorization` 请求头，签名请求头则附加在其上。

请在设置 `"signing": true` **之前**生成密钥。设置了签名却没有密钥的渠道不会以未签名形式发送：第一次投递即被拒绝且不重试，通知服务会记录租户和渠道令牌。该情况计入 `devicechain_notificationmanagement_deliveries_refused_total{reason="signing"}`，可设置告警。

## 为 `httpCall` 动作签名 {#sign-an-httpcall-action}

在某个句柄下生成密钥，然后在动作的 `signingSecretRef` 中指定该句柄。需要 `connector:write`。

```graphql
mutation {
  generateWebhookSigningSecret(handle: "hooks/prod") {
    handle
    secret      # whsec_… — 只显示一次
  }
}
```

在规则表单中，`httpCall` 动作有一个**签名密钥句柄**字段，自动化画布的动作节点上也有同样的字段。通过 API 时，它是动作的 `signingSecretRef` 字段：

```json
{
  "type": "httpCall",
  "httpCall": {
    "url": "https://hooks.example.com/devices",
    "bodyTemplate": "'{\"series\":\"' + series + '\"}'",
    "signingSecretRef": "hooks/prod"
  }
}
```

如果规则指定的句柄没有存储的密钥，其投递在第一次尝试时即作为无效项进入死信，绝不会以未签名形式发出调用。删除了仍被规则引用的密钥时也是如此。连接器服务通过约一分钟的缓存读取密钥，因此新建、轮换或删除的密钥最多需要这么久才能作用到所有投递。

要在不读取密钥的前提下查看某个句柄是否有密钥、是否正在轮换：

```graphql
query { webhookSigningSecret(handle: "hooks/prod") { handle rotating } }
```

## 轮换密钥 {#rotating-a-secret}

轮换不会丢失任何投递，因为在一段时间内两个密钥同时签名。

1. **轮换。** 生成新密钥，只返回一次，并保留旧密钥。

   ```graphql
   mutation { rotateNotificationChannelSigningSecret(token: "oncall-hook") { secret rotating } }
   # 或者，针对 httpCall 句柄：
   mutation { rotateWebhookSigningSecret(handle: "hooks/prod") { secret rotating } }
   ```

   此后每次投递的 `webhook-signature` 中有**两个**条目，一个由新密钥签名，一个由旧密钥签名。持有任一密钥的接收方都会接受。

2. **按自己的节奏将接收方迁移**到新密钥。能同时持有两个密钥的验证器会让这一步变得毫无波澜。

3. **结束轮换。** 旧密钥停止签名，投递恢复为一个条目。

   ```graphql
   mutation { endNotificationChannelSigningRotation(token: "oncall-hook") { token signingRotationOpen } }
   # 或者：
   mutation { endWebhookSigningRotation(handle: "hooks/prod") { handle rotating } }
   ```

在轮换尚未结束时再次轮换，会用当前密钥替换旧密钥，因此两次轮换之前的密钥不再使用。若要彻底停止签名，请先从渠道中移除 `"signing": true`（或从规则中移除 `signingSecretRef`），再用 `deleteNotificationChannelSigningSecret` 或 `deleteWebhookSigningSecret` 删除密钥。删除渠道也会删除它的密钥。

## 验证投递 {#verifying-a-delivery}

接收方必须按以下顺序做四件事：

1. 取得与收到时完全一致的**原始请求体**。解析 JSON 后再序列化会改变字节，从而破坏签名。
2. 检查 `webhook-id`、`webhook-timestamp` 和 `webhook-signature` 都存在，并且时间戳与你的时钟相差不超过几分钟（前后均可）。通常的容差是五分钟。
3. 用密钥对 `<id>.<时间戳>.<原始请求体>` 计算 `HMAC-SHA256`（密钥是 `whsec_` 之后那段 base64 文本解码后的字节），编码为 base64，并加上前缀 `v1,`。
4. 以恒定时间与 `webhook-signature` 中**每个**以空格分隔的条目比较，任何一个匹配即接受。忽略不以 `v1,` 开头的条目。

下面三个示例正是这样做的。请传入你当前持有的所有密钥，轮换期间是两个。

### Go

```go
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Verify checks a delivery. body must be the raw bytes received, before any JSON decoding.
func Verify(h http.Header, body []byte, now time.Time, secrets ...string) error {
	id := h.Get("webhook-id")
	ts := h.Get("webhook-timestamp")
	sigs := h.Get("webhook-signature")
	if id == "" || ts == "" || sigs == "" {
		return errors.New("missing webhook headers")
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("malformed webhook-timestamp")
	}
	// Whole seconds, not a time.Duration: a timestamp centuries away would saturate a Duration.
	if sec > now.Unix()+300 || sec < now.Unix()-300 {
		return errors.New("timestamp outside the tolerance window")
	}
	for _, secret := range secrets {
		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, "whsec_"))
		if err != nil {
			return err
		}
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(id + "." + ts + "."))
		mac.Write(body)
		want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
		for _, s := range strings.Fields(sigs) {
			if hmac.Equal([]byte(s), []byte(want)) {
				return nil
			}
		}
	}
	return errors.New("no valid signature")
}
```

用直接从请求中读取的请求体调用它，例如先 `body, _ := io.ReadAll(r.Body)`，再 `Verify(r.Header, body, time.Now(), secret)`。

### Node

```js
const crypto = require('node:crypto');

// rawBody must be the Buffer or string exactly as received, before JSON.parse.
function verify(headers, rawBody, secrets, now = Date.now()) {
  const id = headers['webhook-id'];
  const ts = headers['webhook-timestamp'];
  const sigs = headers['webhook-signature'];
  if (!id || !ts || !sigs) throw new Error('missing webhook headers');
  if (!/^\d+$/.test(ts)) throw new Error('malformed webhook-timestamp');
  if (Math.abs(now / 1000 - Number(ts)) > 300) throw new Error('timestamp outside the tolerance window');

  for (const secret of secrets) {
    const key = Buffer.from(secret.replace(/^whsec_/, ''), 'base64');
    const want = 'v1,' + crypto.createHmac('sha256', key).update(`${id}.${ts}.`).update(rawBody).digest('base64');
    for (const sig of sigs.split(' ')) {
      if (sig.length === want.length && crypto.timingSafeEqual(Buffer.from(sig), Buffer.from(want))) return;
    }
  }
  throw new Error('no valid signature');
}
```

在 Express 中，请在该路由上用 `express.raw({ type: 'application/json' })` 获取原始请求体，调用 `verify(req.headers, req.body, secrets)`，之后才 `JSON.parse`。Node 会把请求头名称转为小写，代码正是按此编写的。

### Python

```python
import base64
import hashlib
import hmac
import time


def verify(headers, raw_body, secrets, tolerance=300, now=None):
    """Raise ValueError unless the delivery is authentic and fresh.

    raw_body is the bytes exactly as received, before any JSON decoding.
    """
    msg_id = headers.get("webhook-id")
    ts = headers.get("webhook-timestamp")
    sigs = headers.get("webhook-signature")
    if not (msg_id and ts and sigs):
        raise ValueError("missing webhook headers")
    try:
        sent = int(ts)
    except ValueError:
        raise ValueError("malformed webhook-timestamp")
    if abs((time.time() if now is None else now) - sent) > tolerance:
        raise ValueError("timestamp outside the tolerance window")

    for secret in secrets:
        key = base64.b64decode(secret.removeprefix("whsec_"))
        digest = hmac.new(key, f"{msg_id}.{ts}.".encode() + raw_body, hashlib.sha256).digest()
        want = "v1," + base64.b64encode(digest).decode()
        for sig in sigs.split():
            if hmac.compare_digest(sig, want):
                return
    raise ValueError("no valid signature")
```

在 Flask 中，用 `request.get_data()` 作为 `raw_body`，用 `request.headers` 作为 `headers`；在 FastAPI 中使用 `await request.body()`。`removeprefix` 需要 Python 3.9 或更高版本。

### 检查你的验证器 {#check-your-verifier}

规范发布了一个测试向量。正确的验证器应在其被签名的时刻接受它：

| 字段 | 值 |
| --- | --- |
| 密钥 | `whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw` |
| `webhook-id` | `msg_p5jXN8AQM9LWM0D4loKWxJek` |
| `webhook-timestamp` | `1614265330` |
| 请求体 | `{"test": 2432232314}` |
| `webhook-signature` | `v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=` |

同一请求若请求体改动一个字节，或时钟晚了一小时，验证器必须拒绝。

## 失败时的行为 {#failure-behaviour}

| 情形 | 结果 |
| --- | --- |
| 未启用签名 | 投递不变，没有 `webhook-*` 请求头。 |
| 已启用签名且密钥存在 | 投递携带三个请求头。 |
| 已启用签名，但没有存储密钥，或存储的值不是有效密钥 | 投递被拒绝且**不重试**，不会发送任何内容。渠道将其计为 `reason="signing"`；`httpCall` 作为无效项进入死信。 |
| 密钥存储暂时不可用 | 与任何瞬时故障一样，投递会重试。 |
| 你配置的某个请求头名为 `webhook-id`、`webhook-timestamp` 或 `webhook-signature` | 在已签名的投递中，由平台的值替换它。 |
