---
section: whats-new
---

## en

- **dcctl: URL redaction, escrow writes and chosen secrets.** Three fixes to `dcctl install`, `dcctl bootstrap` and `dcctl setup`:
  - A URL with a username and a password is now always redacted in console, `--output json` and terminal UI output, including when the password starts with `/` or with digits (`s3://user:/secret@bucket`, `s3://admin:2024/secret@bucket`). Before, such a URL could be printed whole. URLs without credentials are printed exactly as before.
  - Two runs that write the root-key escrow artifact for the same instance name at the same moment can no longer replace each other's artifact. One run writes it; the other stops with the same "already exists, will not overwrite" message a later run gets.
  - An escrow passphrase, SMTP password or other value you choose that is part of one of dcctl's step names, such as `install`, `bootstrap` or `escrow`, no longer makes a run that succeeded exit with status 1, and a passphrase like that typed at the prompt is no longer refused. The value is still removed from every line dcctl prints.

## es

- **dcctl: redacción de URL, escritura del depósito de claves y secretos elegidos.** Tres correcciones en `dcctl install`, `dcctl bootstrap` y `dcctl setup`:
  - Una URL con usuario y contraseña ahora se redacta siempre en la consola, en la salida `--output json` y en la interfaz de terminal, también cuando la contraseña empieza por `/` o por dígitos (`s3://user:/secret@bucket`, `s3://admin:2024/secret@bucket`). Antes, una URL así podía mostrarse completa. Las URL sin credenciales se muestran exactamente igual que antes.
  - Dos ejecuciones que escriben al mismo tiempo el artefacto de depósito de la clave raíz para el mismo nombre de instancia ya no pueden reemplazar el artefacto de la otra. Una ejecución lo escribe; la otra se detiene con el mismo mensaje de «ya existe, no se sobrescribirá» que recibe una ejecución posterior.
  - Una frase de contraseña del depósito, una contraseña SMTP u otro valor elegido por usted que forme parte del nombre de un paso de dcctl, como `install`, `bootstrap` o `escrow`, ya no hace que una ejecución correcta termine con el estado 1, y una frase de contraseña así escrita en la solicitud ya no se rechaza. El valor se sigue eliminando de todas las líneas que muestra dcctl.

## zh-CN

- **dcctl：URL 脱敏、托管文件写入与自选密钥。** `dcctl install`、`dcctl bootstrap` 和 `dcctl setup` 的三项修复：
  - 带有用户名和密码的 URL 现在在控制台、`--output json` 输出和终端界面中始终会被脱敏，包括密码以 `/` 或数字开头的情况（`s3://user:/secret@bucket`、`s3://admin:2024/secret@bucket`）。此前，这类 URL 可能会被完整打印。不含凭据的 URL 的输出与之前完全相同。
  - 两次同时为同一实例名称写入根密钥托管文件的运行，不会再相互替换对方的文件。其中一次运行写入该文件；另一次运行会停止，并给出与之后的运行相同的“已存在，不会覆盖”提示。
  - 托管口令、SMTP 密码或其他由您选择的值，如果是 dcctl 某个步骤名称的一部分（例如 `install`、`bootstrap` 或 `escrow`），不再会导致已成功的运行以状态 1 退出；在提示中输入此类口令也不会再被拒绝。该值仍会从 dcctl 打印的每一行中移除。
