---
section: before-upgrade
---

## en

- **Token masks that generate nothing are refused.** A token mask whose only placeholders have an explicit width of 0, such as `dev{numeric-0}`, gives every entity the same token, and saving the token-masks setting with one is now refused. A stored mask like this keeps its value, but the next save of the setting fails until it is fixed, so change it to a positive width (`{numeric-4}`), an omitted width (`{alphanumeric}`), `{slug}` or `{uuid}` before you upgrade.

## es

- **Se rechazan las máscaras de token que no generan nada.** Una máscara de token cuyos únicos marcadores tienen un ancho explícito de 0, como `dev{numeric-0}`, da a todas las entidades el mismo token, y ahora se rechaza guardar el ajuste de máscaras de token que la contenga. Una máscara así ya guardada conserva su valor, pero el siguiente guardado del ajuste falla hasta que se corrija, así que cámbiala antes de actualizar a un ancho positivo (`{numeric-4}`), un ancho omitido (`{alphanumeric}`), `{slug}` o `{uuid}`.

## zh-CN

- **拒绝不生成任何字符的令牌掩码。** 若令牌掩码中唯一的占位符都显式指定宽度为 0（例如 `dev{numeric-0}`），所有实体都会得到相同的令牌，现在保存包含此类掩码的令牌掩码设置会被拒绝。已保存的此类掩码会保留原值，但在修正之前，该设置的下一次保存会失败。因此请在升级前将其改为正数宽度（`{numeric-4}`）、省略宽度（`{alphanumeric}`）、`{slug}` 或 `{uuid}`。
