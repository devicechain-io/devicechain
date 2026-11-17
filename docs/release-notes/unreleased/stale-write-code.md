---
section: whats-new
---

## en

- **A stale save carries `extensions.code: STALE_WRITE`, and `@devicechain/client` exports `isStaleWrite`.** A write refused because the record changed after the caller read it (an optimistic-concurrency precondition such as `expectedUpdatedAt` that no longer holds) now answers with `extensions.code` `STALE_WRITE` in every service; before, only connector updates carried a code and every other such refusal had none. The message is unchanged ("… was modified by another writer; reload and try again"), so clients that match it keep working, and it is still never `CONFLICT`, which stays reserved for a value that is already taken. `isStaleWrite(err)` in `@devicechain/client` is true for the code, falls back to the phrase for older servers, and is never true for a `CONFLICT`. The console uses it wherever it saves with a precondition.

## es

- **Un guardado obsoleto lleva `extensions.code: STALE_WRITE`, y `@devicechain/client` exporta `isStaleWrite`.** Una escritura rechazada porque el registro cambió después de que el llamante lo leyera (una precondición de concurrencia optimista, como `expectedUpdatedAt`, que ya no se cumple) responde ahora con `extensions.code` `STALE_WRITE` en todos los servicios; antes solo las actualizaciones de conectores llevaban un código y los demás rechazos de este tipo no tenían ninguno. El mensaje no cambia ("… was modified by another writer; reload and try again"), así que los clientes que lo reconocen siguen funcionando, y nunca es `CONFLICT`, que sigue reservado para un valor que ya está en uso. `isStaleWrite(err)` de `@devicechain/client` es verdadero para el código, recurre a la frase con servidores anteriores y nunca es verdadero para un `CONFLICT`. La consola lo usa en todos los guardados con precondición.

## zh-CN

- **过期保存会携带 `extensions.code: STALE_WRITE`，`@devicechain/client` 导出 `isStaleWrite`。** 因记录在调用方读取之后已被修改而被拒绝的写入（例如 `expectedUpdatedAt` 这类乐观并发前置条件不再成立）现在在所有服务中都会返回 `extensions.code` `STALE_WRITE`；此前只有连接器更新带有代码，其他此类拒绝都没有代码。消息保持不变（"… was modified by another writer; reload and try again"），因此依据该消息识别的客户端仍可正常工作；它也始终不会是 `CONFLICT`，后者仍专用于值已被占用的情况。`@devicechain/client` 中的 `isStaleWrite(err)` 对该代码返回 true，对较旧的服务器回退为匹配该消息，对 `CONFLICT` 永远不会返回 true。控制台在所有带前置条件的保存中都使用它。
