---
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
section: before-upgrade
---

## en

- **Preview promotion conditions.** The Preview rule now uses per-surface promotion conditions, with two minors after joining as the default. Rule testing and product as code are covered from v1.10. Agent actions and automation review are covered from v2.0, when approvals move to a common approval model. Newly listed surfaces are command-delivery's proposal operations, including `proposeCommand`; MCP's `propose_alarm_action`, `propose_command`, `get_proposal` and `list_my_proposals`; device-management's automation review operations; and event-processing's service-only `compareRuleVersions`. Read each surface's condition in the versioning policy before upgrading.

## es

- **Condiciones de promoción de la vista previa.** La regla de vista previa ahora usa condiciones de promoción por superficie, con dos versiones menores después de entrar como condición predeterminada. Las pruebas de reglas y el producto como código están cubiertos desde v1.10. Las acciones de agentes y la revisión de automatizaciones están cubiertas desde v2.0, cuando las aprobaciones pasen a un modelo común de aprobación. Las superficies recién enumeradas son las operaciones de propuestas de command-delivery, incluida `proposeCommand`; `propose_alarm_action`, `propose_command`, `get_proposal` y `list_my_proposals` de MCP; las operaciones de revisión de automatizaciones de device-management; y `compareRuleVersions` de event-processing (solo para servicios). Consulta la condición de cada superficie en la política de versiones antes de actualizar.

## zh-CN

- **预览提升条件。** 预览规则现按接口分别规定提升条件，默认是进入预览后经过两个次版本。规则测试和产品即代码从 v1.10 起受兼容性承诺保障。代理操作和自动化审查从 v2.0 起受兼容性承诺保障，届时审批将采用通用审批模型。新列出的接口包括 command-delivery 的提议操作（含 `proposeCommand`）；MCP 的 `propose_alarm_action`、`propose_command`、`get_proposal`、`list_my_proposals`；device-management 的自动化审查操作；以及 event-processing 中仅供服务调用的 `compareRuleVersions`。升级前请阅读版本策略中各接口的条件。
