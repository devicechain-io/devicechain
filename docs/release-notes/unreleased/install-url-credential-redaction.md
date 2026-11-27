---
section: whats-new
---

## en

- **dcctl no longer prints credentials embedded in a URL you give it.** A restore source
  (`--restore-rdb-from`, `--restore-tsdb-from`), an image registry (`--registry`), a backup
  endpoint, an object-store endpoint and a broker URL (`--nats-url`) were printed in full, so a
  user name, password, signed query or fragment token written into one reached the terminal, the
  `--output json` event stream and the terminal UI. Wherever dcctl shows such a value it now
  replaces the user information, the query and the fragment with `REDACTED`
  (`s3://user:pass@bucket/archive` is shown as `s3://REDACTED@bucket/archive`), including when
  they are percent-encoded or the value is not a well-formed URL. The output of the tools dcctl
  runs (OpenTofu, ko, docker), which quotes those values back, is filtered the same way. Values
  without credentials are shown exactly as before. If you have passed a credential this way,
  treat logs and CI output from earlier runs as having recorded it, and rotate it.
- **The OpenTofu roots mark the restore source and the backup endpoint as sensitive.** The
  `backup_endpoint_url` and `database_restored_from` outputs, and the restore source in a plan,
  are now shown as `(sensitive value)`. dcctl reads them as before; if you read them by hand, use
  `tofu output -raw <name>` or `tofu output -json`.

## es

- **dcctl ya no imprime las credenciales incluidas en una URL que se le pasa.** Un origen de
  restauración (`--restore-rdb-from`, `--restore-tsdb-from`), un registro de imágenes
  (`--registry`), un endpoint de copias de seguridad, un endpoint de almacén de objetos y una URL
  del broker (`--nats-url`) se imprimían completos, de modo que un nombre de usuario, una
  contraseña, una consulta firmada o un token en el fragmento escritos en ellos llegaban a la
  terminal, al flujo de eventos de `--output json` y a la interfaz de terminal. Allí donde dcctl
  muestra uno de estos valores, ahora sustituye la información de usuario, la consulta y el
  fragmento por `REDACTED` (`s3://user:pass@bucket/archive` se muestra como
  `s3://REDACTED@bucket/archive`), también cuando están codificados con porcentajes o el valor no
  es una URL bien formada. La salida de las herramientas que dcctl ejecuta (OpenTofu, ko, docker),
  que repite esos valores, se filtra de la misma forma. Los valores sin credenciales se muestran
  exactamente igual que antes. Si ha pasado una credencial de esta forma, considere que los
  registros y la salida de CI de ejecuciones anteriores la recogieron, y rótela.
- **Las raíces de OpenTofu marcan como sensibles el origen de restauración y el endpoint de
  copias de seguridad.** Las salidas `backup_endpoint_url` y `database_restored_from`, y el origen
  de restauración en un plan, se muestran ahora como `(sensitive value)`. dcctl las lee igual que
  antes; si las lee a mano, use `tofu output -raw <nombre>` o `tofu output -json`.

## zh-CN

- **dcctl 不再打印您提供的 URL 中嵌入的凭据。** 恢复源（`--restore-rdb-from`、
  `--restore-tsdb-from`）、镜像仓库（`--registry`）、备份端点、对象存储端点和 broker URL
  （`--nats-url`）以前会被完整打印，因此写在其中的用户名、密码、签名查询或片段令牌会出现在终端、
  `--output json` 事件流和终端界面中。现在，dcctl 在显示此类值时，会将用户信息、查询和片段替换为
  `REDACTED`（`s3://user:pass@bucket/archive` 显示为 `s3://REDACTED@bucket/archive`），即使它们经过
  百分号编码或该值不是格式正确的 URL 也是如此。dcctl 运行的工具（OpenTofu、ko、docker）会回显这些值，
  其输出也以同样方式过滤。不含凭据的值的显示与以前完全相同。如果您曾以这种方式传入凭据，请将之前运行的
  日志和 CI 输出视为已记录了该凭据，并轮换它。
- **OpenTofu 根模块将恢复源和备份端点标记为敏感值。** `backup_endpoint_url` 和
  `database_restored_from` 输出以及计划中的恢复源现在显示为 `(sensitive value)`。dcctl 的读取方式
  不变；如果您手动读取，请使用 `tofu output -raw <名称>` 或 `tofu output -json`。
