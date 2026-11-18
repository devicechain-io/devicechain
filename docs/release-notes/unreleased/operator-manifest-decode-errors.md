---
section: whats-new
---

## en

- **Clearer operator manifest errors.** When the Kubernetes operator cannot decode a section of a manifest it applies, the error names the section by its 1-based position and carries the decoder's own message. Before, the cause was lost and the error ended in `<nil>`.

## es

- **Errores de manifiesto más claros en el operador.** Cuando el operador de Kubernetes no puede decodificar una sección de un manifiesto que aplica, el error indica la sección por su posición, empezando en 1, e incluye el mensaje del propio decodificador. Antes, la causa se perdía y el error terminaba en `<nil>`.

## zh-CN

- **更清晰的 operator 清单错误。** 当 Kubernetes operator 无法解码其应用的清单中的某个部分时，错误会按从 1 开始的位置指出该部分，并附带解码器自身的消息。此前原因会丢失，错误以 `<nil>` 结尾。
