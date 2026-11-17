---
section: before-upgrade
---

## en

- **The MCP server's limit of 64 requests in flight per process is reachable, and the request past it receives HTTP 429.** The shared request-body budget held only 63 maximum-size bodies, so the 64th concurrent request received HTTP 503 with `Retry-After: 1` before the MCP limit was reached. The budget now holds 64 (it grows by 64 bytes), and requests beyond the limit receive HTTP 429 with `Retry-After: 1`, as documented. A client that retried the 503 should retry the 429 the same way. The limit of 4 in flight per caller and the 1 MiB request-body ceiling are unchanged.

## es

- **El límite de 64 solicitudes en curso por proceso del servidor MCP es alcanzable, y la solicitud que lo supera recibe HTTP 429.** El presupuesto compartido de cuerpos de solicitud solo admitía 63 cuerpos de tamaño máximo, por lo que la solicitud simultánea número 64 recibía HTTP 503 con `Retry-After: 1` antes de alcanzar el límite de MCP. Ahora el presupuesto admite 64 (crece 64 bytes), y las solicitudes que superan el límite reciben HTTP 429 con `Retry-After: 1`, como está documentado. Un cliente que reintentaba el 503 debe reintentar el 429 de la misma forma. El límite de 4 en curso por llamante y el máximo de 1 MiB por cuerpo de solicitud no cambian.

## zh-CN

- **MCP 服务器每进程 64 个进行中请求的上限现在可以达到，超出的请求会收到 HTTP 429。** 共享请求体预算此前只能容纳 63 个最大尺寸的请求体，因此第 64 个并发请求会在达到 MCP 上限之前收到带 `Retry-After: 1` 的 HTTP 503。该预算现在可容纳 64 个（增加 64 字节），超出上限的请求会收到带 `Retry-After: 1` 的 HTTP 429，与文档一致。此前会重试 503 的客户端应以同样方式重试 429。每个调用者 4 个进行中请求的上限和 1 MiB 的请求体上限保持不变。
