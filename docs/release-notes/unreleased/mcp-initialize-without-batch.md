---
section: before-upgrade
---

## en

- **MCP clients must send `initialize` on its own.** A request that opens a new MCP session (a POST without an `Mcp-Session-Id` header) is now refused with HTTP 400 and JSON-RPC error `-32600` when its body is a JSON-RPC batch. Only clients on protocol versions before 2025-06-18 can send batches; if one of yours puts `initialize` in a batch with other calls, change it to send `initialize` alone and the other calls on the session it opens before you upgrade. Batches on an existing session are unchanged.

## es

- **Los clientes MCP deben enviar `initialize` por separado.** Una solicitud que abre una nueva sesión MCP (un POST sin la cabecera `Mcp-Session-Id`) ahora se rechaza con HTTP 400 y el error JSON-RPC `-32600` cuando su cuerpo es un lote JSON-RPC. Solo los clientes con versiones del protocolo anteriores a 2025-06-18 pueden enviar lotes; si alguno de los suyos incluye `initialize` en un lote con otras llamadas, cámbielo antes de actualizar para que envíe `initialize` solo y las demás llamadas en la sesión que este abre. Los lotes en una sesión existente no cambian.

## zh-CN

- **MCP 客户端必须单独发送 `initialize`。** 打开新 MCP 会话的请求（不带 `Mcp-Session-Id` 请求头的 POST）如果其正文是 JSON-RPC 批处理，现在会以 HTTP 400 和 JSON-RPC 错误 `-32600` 被拒绝。只有使用 2025-06-18 之前协议版本的客户端才能发送批处理；如果您的某个客户端将 `initialize` 与其他调用放在同一批处理中，请在升级前将其改为单独发送 `initialize`，并在其打开的会话上发送其他调用。现有会话上的批处理不受影响。
