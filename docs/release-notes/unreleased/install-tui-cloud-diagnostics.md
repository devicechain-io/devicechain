---
section: whats-new
---

## en

- **The install terminal UI's preflight also checks an existing cluster, as advice.** With `--kube-context`, `dcctl install local --ui=tui` adds an "Existing cluster (advisory)" group after the preflight checks: whether the API server's name resolves, whether it answers over a TLS certificate the context trusts, whether the context's credentials are accepted, whether they may create what the install creates (asked with access reviews, which change nothing), and whether this machine can reach the provider, chart and image registries the install uses. They never go through a proxy: when one is configured, for the context or in the environment (`HTTPS_PROXY` and the like), the rows it would carry read NOT CHECKED. These checks only read, follow no redirect, give up after 5 seconds to connect and 15 seconds to answer, and never block Continue: a problem reads WARN, and anything they cannot decide reads UNKNOWN or NOT CHECKED, never PASS. The plain output, `dcctl preflight` and the install's own preflight are unchanged.

## es

- **La comprobación previa de la interfaz de terminal de la instalación también revisa un clúster existente, a modo de consejo.** Con `--kube-context`, `dcctl install local --ui=tui` añade un grupo "Existing cluster (advisory)" después de las comprobaciones previas: si el nombre del servidor de la API se resuelve, si responde con un certificado TLS en el que confía el contexto, si se aceptan las credenciales del contexto, si pueden crear lo que crea la instalación (preguntado con revisiones de acceso, que no cambian nada) y si esta máquina puede llegar a los registros de proveedores, charts e imágenes que usa la instalación. Nunca pasan por un proxy: cuando hay uno configurado, para el contexto o en el entorno (`HTTPS_PROXY` y similares), las filas que pasarían por él se leen como NOT CHECKED. Estas comprobaciones solo leen, no siguen redirecciones, se rinden tras 5 segundos para conectar y 15 segundos para responder, y nunca bloquean Continue: un problema se lee como WARN, y lo que no pueden decidir se lee como UNKNOWN o NOT CHECKED, nunca como PASS. La salida sin formato, `dcctl preflight` y la comprobación previa propia de la instalación no cambian.

## zh-CN

- **安装终端界面的预检现在也会以建议的形式检查现有集群。** 使用 `--kube-context` 时，`dcctl install local --ui=tui` 会在预检之后增加一组 "Existing cluster (advisory)"：API 服务器的名称能否解析，它是否以该上下文信任的 TLS 证书应答，该上下文的凭据是否被接受，这些凭据是否可以创建安装所创建的资源（通过不会改变任何内容的访问审查来询问），以及本机能否访问安装所用的提供程序、chart 和镜像仓库。它们从不经过代理：当上下文或环境（`HTTPS_PROXY` 等）中配置了代理时，需要经过该代理的行显示为 NOT CHECKED。这些检查只做读取，不跟随重定向，连接超过 5 秒、应答超过 15 秒即放弃，并且从不阻止 Continue：发现的问题显示为 WARN，无法判断的显示为 UNKNOWN 或 NOT CHECKED，绝不显示为 PASS。纯文本输出、`dcctl preflight` 以及安装自身的预检均保持不变。
