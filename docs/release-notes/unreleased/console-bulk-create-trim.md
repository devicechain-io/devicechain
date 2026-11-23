---
section: whats-new
---

## en

- **Bulk device create accepts a token template with stray spaces.** The console's bulk create form already trimmed the token template before sending it, but checked the untrimmed text first, so a template pasted with a leading or trailing space was refused with "Rendered token is not valid". It now checks the template exactly as it is sent.

## es

- **La creación masiva de dispositivos acepta una plantilla de token con espacios sobrantes.** El formulario de creación masiva de la consola ya recortaba la plantilla de token antes de enviarla, pero comprobaba primero el texto sin recortar, de modo que una plantilla pegada con un espacio al principio o al final se rechazaba con "El token generado no es válido". Ahora comprueba la plantilla tal como se envía.

## zh-CN

- **批量创建设备现在接受带多余空格的令牌模板。** 控制台的批量创建表单在发送前本来就会去掉令牌模板首尾的空格，但校验却针对未去除空格的文本，因此首尾带空格的模板会被拒绝，并提示“生成的令牌无效”。现在校验的就是实际发送的模板。
