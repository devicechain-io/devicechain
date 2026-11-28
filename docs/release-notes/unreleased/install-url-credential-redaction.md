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
  without credentials are shown exactly as before.

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
  exactamente igual que antes.

## zh-CN

- **dcctl 不再打印您提供的 URL 中嵌入的凭据。** 恢复源（`--restore-rdb-from`、
  `--restore-tsdb-from`）、镜像仓库（`--registry`）、备份端点、对象存储端点和 broker URL
  （`--nats-url`）以前会被完整打印，因此写在其中的用户名、密码、签名查询或片段令牌会出现在终端、
  `--output json` 事件流和终端界面中。现在，dcctl 在显示此类值时，会将用户信息、查询和片段替换为
  `REDACTED`（`s3://user:pass@bucket/archive` 显示为 `s3://REDACTED@bucket/archive`），即使它们经过
  百分号编码或该值不是格式正确的 URL 也是如此。dcctl 运行的工具（OpenTofu、ko、docker）会回显这些值，
  其输出也以同样方式过滤。不含凭据的值的显示与以前完全相同。
