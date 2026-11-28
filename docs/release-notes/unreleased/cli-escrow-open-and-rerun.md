---
section: before-upgrade
---

## en

- **Bootstrap re-runs need the existing escrow passphrase.** If a root-key escrow artifact already exists, `dcctl bootstrap` asks once for the passphrase it was written with and opens it before any cluster steps. Supply that original passphrase through the prompt, `--escrow-passphrase-file`, or `DCCTL_ESCROW_PASSPHRASE`; a different passphrase or damaged artifact stops the run early. To check recovery against a live instance, use `dcctl secrets escrow verify <path> --instance <instance> --open` with the passphrase file or environment variable. Without `--open`, verification still compares only the cleartext header fingerprint. Kubeconfig output redaction also covers URL passwords, JWTs, and opaque passphrases under ordinary plugin setting names, while preserving ordinary profile, region, and namespace names.

## es

- **Volver a ejecutar bootstrap requiere la contraseña del depósito existente.** Si ya existe un archivo de custodia de la clave raíz, `dcctl bootstrap` solicita una sola vez la contraseña con la que se escribió y lo abre antes de los pasos del clúster. Proporciona esa contraseña original mediante la solicitud, `--escrow-passphrase-file` o `DCCTL_ESCROW_PASSPHRASE`; una contraseña distinta o un archivo dañado detiene la ejecución al principio. Para comprobar la recuperación frente a una instancia activa, usa `dcctl secrets escrow verify <path> --instance <instance> --open` con el archivo de contraseña o la variable de entorno. Sin `--open`, la verificación sigue comparando solo la huella de la cabecera en texto claro. La ocultación de datos de kubeconfig también cubre contraseñas de URL, JWT y contraseñas opacas bajo nombres de configuración ordinarios de complementos, y conserva los nombres ordinarios de perfiles, regiones y espacios de nombres.

## zh-CN

- **重新运行 bootstrap 需要现有托管文件的口令。** 如果根密钥托管文件已存在，`dcctl bootstrap` 会询问一次写入该文件时使用的口令，并在执行任何集群步骤之前打开文件。通过提示、`--escrow-passphrase-file` 或 `DCCTL_ESCROW_PASSPHRASE` 提供原口令；口令不同或文件损坏都会提前终止运行。要针对运行中的实例检查恢复能力，请使用 `dcctl secrets escrow verify <path> --instance <instance> --open`，并通过口令文件或环境变量提供口令。不加 `--open` 时，验证仍然仅比较明文头部的指纹。kubeconfig 输出脱敏还会覆盖普通插件设置名称下的 URL 密码、JWT 和不透明口令，同时保留普通配置、区域和命名空间名称。
