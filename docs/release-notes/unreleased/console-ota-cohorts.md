---
section: whats-new
---

## en

- **The console can now run a staged OTA rollout.** A rollout created with cohorts shows its plan on the rollout page (each cohort's size, whether an operator promotes it, and whether it is complete, current or upcoming), and the devices table gains a Cohort column. When a cohort finishes cleanly the rollout pauses as "Cohort complete, ready to promote" and **Promote to next cohort** releases the next one; the button names the cohort on screen, so a stale tab or a second operator cannot promote twice. When a cohort finishes with failed or unknown devices, **Promote despite failures** asks for confirmation before it accepts them, and a rollout halted by its failure-rate limit offers a confirmed **Override failure-rate halt and resume** for the halt on screen. All three need `artifact:write`, like the other rollout controls. The counts on the page still cover the whole rollout, not one cohort. The new strings are in English and Spanish; Simplified Chinese shows English for the updates pages until that catalog is translated.

## es

- **La consola ya puede gestionar un despliegue OTA por fases.** Un despliegue creado con cohortes muestra su plan en la página del despliegue (el tamaño de cada cohorte, si la promueve un operador y si está completada, actual o pendiente), y la tabla de dispositivos incorpora una columna Cohorte. Cuando una cohorte termina sin fallos, el despliegue se pausa como «Cohorte completada, lista para promover» y **Promover a la siguiente cohorte** libera la siguiente; el botón nombra la cohorte que se ve en pantalla, de modo que una pestaña desactualizada o un segundo operador no pueden promover dos veces. Cuando una cohorte termina con dispositivos fallidos o desconocidos, **Promover pese a los fallos** pide confirmación antes de aceptarlos, y un despliegue detenido por su límite de tasa de fallos ofrece **Anular la parada por tasa de fallos y reanudar**, con confirmación, para la parada que se ve en pantalla. Las tres acciones requieren `artifact:write`, como los demás controles del despliegue. Los recuentos de la página siguen abarcando todo el despliegue, no una sola cohorte. Los textos nuevos están en inglés y español; el chino simplificado muestra inglés en las páginas de actualizaciones hasta que se traduzca ese catálogo.

## zh-CN

- **控制台现在可以管理分阶段的 OTA 发布。** 使用分组（cohort）创建的发布会在发布页面显示其计划（每个分组的规模、是否由操作员推进，以及已完成、当前或待处理状态），设备表也新增“分组”列。当某个分组顺利完成时，发布会暂停为“分组已完成，可推进”，点击**推进到下一分组**即可放行下一个分组；按钮针对屏幕上显示的分组，因此过期的页面或另一位操作员不会造成重复推进。当某个分组结束时存在失败或结果未知的设备，**忽略失败并推进**会在接受这些结果之前要求确认；因失败率上限而中止的发布则提供需确认的**忽略失败率中止并恢复**，针对屏幕上显示的那一次中止。这三项操作与其他发布控件一样需要 `artifact:write`。页面上的计数仍涵盖整个发布，而非单个分组。新增文案提供英文和西班牙文；在简体中文目录翻译完成之前，更新页面仍显示英文。
