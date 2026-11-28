---
section: before-upgrade
---

## en

- **A password set by accepting an invitation now signs in only to the tenants that vouched for it.** When a new person accepts a tenant invitation with a password, that password now opens that tenant, and any other tenant whose invitation the person later accepts by signing in with it. It no longer opens a membership another tenant attached to the same address in some other way: an invitation that was revoked and the member then enabled by hand, or a membership the operator added. Those memberships are left out of the sign-in's tenant list and refused when selected. Single sign-on is not affected. The upgrade marks existing accounts the same way: an account an invitation created keeps every tenant whose invitation it accepted and every membership the operator added before the upgrade, and loses only memberships that were enabled without an accepted invitation. To let such a person in everywhere, the operator sets their password (`setPassword`), which removes the restriction.

## es

- **Una contraseña establecida al aceptar una invitación ahora solo inicia sesión en los inquilinos que respondieron por ella.** Cuando una persona nueva acepta la invitación de un inquilino con una contraseña, esa contraseña abre ese inquilino y cualquier otro cuya invitación la persona acepte después iniciando sesión con ella. Ya no abre una membresía que otro inquilino haya asociado a la misma dirección de otra forma: una invitación revocada tras la cual el miembro se habilitó a mano, o una membresía que añadió el operador. Esas membresías no aparecen en la lista de inquilinos del inicio de sesión y se rechazan al seleccionarlas. El inicio de sesión único no se ve afectado. La actualización marca igual las cuentas existentes: una cuenta creada por una invitación conserva todos los inquilinos cuya invitación aceptó y todas las membresías que el operador añadió antes de la actualización, y solo pierde las membresías habilitadas sin una invitación aceptada. Para que esa persona entre en todas partes, el operador establece su contraseña (`setPassword`), lo que elimina la restricción.

## zh-CN

- **通过接受邀请设置的密码现在只能登录为其担保的租户。** 新用户使用密码接受某个租户的邀请后，该密码可以进入该租户，以及该用户之后用此密码登录并接受其邀请的其他租户。它不再能进入另一个租户以其他方式关联到同一地址的成员身份：邀请被撤销后又被手动启用的成员，或由运维人员添加的成员身份。这些成员身份不会出现在登录的租户列表中，选择时会被拒绝。单点登录不受影响。升级会以同样方式标记现有账户：由邀请创建的账户保留其已接受邀请的所有租户以及升级前运维人员添加的所有成员身份，只失去未经接受邀请就被启用的成员身份。若要让此人进入所有租户，运维人员为其设置密码（`setPassword`），即可解除该限制。
