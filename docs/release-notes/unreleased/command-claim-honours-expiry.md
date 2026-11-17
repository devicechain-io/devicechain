---
section: whats-new
---

## en

- **A command past its expiry is no longer dispatched.** Before, a command whose expiry had passed but which the expiry sweep had not yet settled could still be claimed and published to its device: when a device disabled for longer than its commands' lifetime was enabled again, by a nudge, or by a dispatch sweep whose expiry step had failed. The dispatch claim now refuses such a command and leaves it for the expiry sweep, which records it as expired. A command exactly at its expiry time, or with no expiry, is still dispatched. `markCommandSent` answers null for an expired command, as it does for any claim it does not win.

## es

- **Un comando caducado ya no se envía.** Antes, un comando cuya caducidad ya había pasado, pero que el barrido de caducidad aún no había cerrado, todavía podía reclamarse y publicarse a su dispositivo: al volver a habilitar un dispositivo que había estado deshabilitado más tiempo que la vida de sus comandos, mediante un aviso (nudge) o en un barrido de envío cuyo paso de caducidad había fallado. La reclamación de envío rechaza ahora ese comando y lo deja para el barrido de caducidad, que lo registra como caducado. Un comando que está exactamente en su hora de caducidad, o que no tiene caducidad, se sigue enviando. `markCommandSent` responde null para un comando caducado, igual que para cualquier reclamación que no gana.

## zh-CN

- **已过期的命令不再被下发。** 此前，已超过到期时间但尚未被到期清理处理的命令仍可能被认领并发布到其设备：例如重新启用一台停用时间超过其命令有效期的设备时、通过唤醒（nudge）时，或在到期步骤失败的下发清理中。现在，下发认领会拒绝此类命令，并将其留给到期清理，由后者将其记录为已过期。恰好处于到期时间或没有到期时间的命令仍会被下发。对于已过期的命令，`markCommandSent` 返回 null，与其未赢得的任何认领相同。
