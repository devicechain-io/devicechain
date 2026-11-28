---
section: before-upgrade
---

## en

- **Rotate a credential you passed in a URL to dcctl, and read two OpenTofu outputs differently.** If you gave dcctl a restore source, image registry, backup endpoint, object-store endpoint or broker URL with a user name, password or token in it, treat logs and CI output from earlier runs as having recorded it, and rotate it. Separately, the `backup_endpoint_url` and `database_restored_from` outputs of the OpenTofu roots, and the restore source in a plan, are now sensitive and show as `(sensitive value)`. dcctl reads them as before; a script that reads plain `tofu output` must use `tofu output -raw <name>` or `tofu output -json`.

## es

- **Rote una credencial que haya pasado a dcctl en una URL y lea dos salidas de OpenTofu de otra forma.** Si indicó a dcctl un origen de restauración, un registro de imágenes, un endpoint de copias de seguridad, un endpoint de almacén de objetos o una URL del broker con un nombre de usuario, una contraseña o un token, considere que los registros y la salida de CI de ejecuciones anteriores la recogieron, y rótela. Además, las salidas `backup_endpoint_url` y `database_restored_from` de las raíces de OpenTofu, y el origen de restauración en un plan, ahora son sensibles y se muestran como `(sensitive value)`. dcctl las lee igual que antes; un script que lea `tofu output` sin más debe usar `tofu output -raw <nombre>` o `tofu output -json`.

## zh-CN

- **请轮换您曾在传给 dcctl 的 URL 中携带的凭据，并以新的方式读取两个 OpenTofu 输出。** 如果您曾向 dcctl 提供带有用户名、密码或令牌的恢复源、镜像仓库、备份端点、对象存储端点或 broker URL，请将之前运行的日志和 CI 输出视为已记录了该凭据，并轮换它。另外，OpenTofu 根模块的 `backup_endpoint_url` 和 `database_restored_from` 输出以及计划中的恢复源现在属于敏感值，显示为 `(sensitive value)`。dcctl 的读取方式不变；直接读取 `tofu output` 的脚本需改用 `tofu output -raw <名称>` 或 `tofu output -json`。
