#!/usr/bin/env python3
# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
"""Alert runbook: the one place the chart's alerts, their runbook_url and the published
runbook page are tied together.

    hack/alert-runbook.py list   <repo>   # JSON of every alert read from the rule templates
    hack/alert-runbook.py gen    <repo>   # (re)write the runbook page in every locale
    hack/alert-runbook.py check  <repo>   # exit 1 naming every gap; writes nothing

THE RULES ARE READ AS TEXT, NOT RENDERED. Some groups render only under a gate (a profile,
an area), and a runbook that listed only what the default render holds would silently leave
those alerts without an entry. The templates are line-structured (every alert is a
`- alert: Name` item with an `annotations:` block); `check` fails on an alert it cannot read
that way instead of skipping it.

THE URL IS A FUNCTION OF THE NAME: <BASE>#<lowercased name>. One definition, used by the rule
text, by the generated headings and by the checker, so a rename moves all three or fails.
"""
import json
import os
import re
import sys

BASE = "https://docs.devicechain.io/deployment/alert-runbook"
RULES = "deploy/helm/devicechain/templates"
PAGES = {
    "en": "docs/docs/deployment/alert-runbook.md",
    "es": "docs/i18n/es/docusaurus-plugin-content-docs/current/deployment/alert-runbook.md",
    "zh-CN": "docs/i18n/zh-CN/docusaurus-plugin-content-docs/current/deployment/alert-runbook.md",
}

ALERT = re.compile(r"^(\s*)- alert:\s*([A-Za-z0-9_]+)\s*$")
GROUP = re.compile(r"^\s*- name:\s*(devicechain\.[A-Za-z0-9_.-]+)\s*$")


def url(name):
    return "%s#%s" % (BASE, name.lower())


def unwrap(text):
    """Turn a Helm-templated annotation into the text a reader sees in the notification.

    Alertmanager placeholders ({{ $labels.x }}, {{ $value }}) become a bracketed name, since
    the page describes the alert in general and the notification carries the value. A Helm
    value interpolated at render time (a number, a name) is not known here and reads as N.
    """
    def label(m):
        inner = m.group(1).strip()
        if inner.startswith("$labels."):
            return "‹" + inner[len("$labels."):].split("|")[0].strip() + "›"
        return "‹value›"

    text = re.sub(r'\{\{\s*"\{\{(.*?)\}\}"\s*\}\}', label, text)
    text = re.sub(r"\{\{`\{\{(.*?)\}\}`\}\}", label, text)
    text = re.sub(r"\{\{-?\s*[^{}]*?\s*-?\}\}", "N", text)
    return text.replace('\\"', '"').replace("\\\\", "\\")


def read_alerts(repo):
    alerts = []
    d = os.path.join(repo, RULES)
    for fn in sorted(os.listdir(d)):
        if not (fn.startswith("prometheusrule") and fn.endswith(".yaml")):
            continue
        with open(os.path.join(d, fn), encoding="utf-8", newline="") as fh:
            lines = fh.read().replace("\r\n", "\n").split("\n")
        group = ""
        i = 0
        while i < len(lines):
            g = GROUP.match(lines[i])
            if g:
                group = g.group(1)
            m = ALERT.match(lines[i])
            if not m:
                i += 1
                continue
            indent, name = len(m.group(1)), m.group(2)
            j = i + 1
            body = []
            while j < len(lines):
                ln = lines[j]
                if ln.strip() and (len(ln) - len(ln.lstrip())) <= indent and not ln.lstrip().startswith("#"):
                    break
                body.append(ln)
                j += 1
            a = {"name": name, "file": fn, "group": group, "severity": "", "summary": "",
                 "description": "", "runbook_url": None, "has_annotations": False}
            k = 0
            while k < len(body):
                ln, s = body[k], body[k].strip()
                if s == "annotations:":
                    a["has_annotations"] = True
                elif s.startswith("severity:"):
                    a["severity"] = s.split(":", 1)[1].strip().strip('"')
                elif s.startswith("runbook_url:"):
                    a["runbook_url"] = s.split(":", 1)[1].strip().strip('"')
                elif s.startswith("summary:"):
                    a["summary"] = unwrap(s.split(":", 1)[1].strip().strip('"'))
                elif s.startswith("description:"):
                    v = s.split(":", 1)[1].strip()
                    if v in (">", ">-", ">+", "|", "|-"):
                        ind = len(ln) - len(ln.lstrip())
                        parts = []
                        k += 1
                        while k < len(body) and (not body[k].strip() or len(body[k]) - len(body[k].lstrip()) > ind):
                            parts.append(body[k].strip())
                            k += 1
                        a["description"] = unwrap(" ".join(p for p in parts if p))
                        continue
                    a["description"] = unwrap(v.strip('"'))
                k += 1
            alerts.append(a)
            i = j
    return alerts


def md(text):
    """Escape what MDX would read as markup or an expression."""
    return (text.replace("\\", "\\\\").replace("{", "\\{").replace("}", "\\}")
            .replace("<", "&lt;").replace(">", "&gt;"))


HEAD = {
    "en": ("Alert Runbook",
           "Every alert the chart ships has a `runbook_url` that lands on its entry here. Each entry says how loud the alert is, what "
           "the alert is telling you and what to do about it. The text is the alert's own description, so the notification and this "
           "page never disagree.\n\nAlerts only reach a person when Alertmanager has a receiver. See "
           "[Routing alerts to a person](./observability.md#alert-routing) to set one with `dcctl install --alert-webhook`, "
           "`--alert-email` or `--alert-config`.",
           "Severity", "Rule group", "What it means and what to do"),
    "es": ("Guía de alertas",
           "Cada alerta que incluye el chart tiene un `runbook_url` que lleva a su entrada aquí. Cada entrada indica la gravedad de la "
           "alerta, qué te está diciendo y qué hacer al respecto. El texto es la propia descripción de la alerta, de modo que la "
           "notificación y esta página nunca se contradicen.\n\nLas alertas solo llegan a una persona cuando Alertmanager tiene un "
           "receptor. Consulta [Enrutar las alertas a una persona](./observability.md#alert-routing) para configurarlo con "
           "`dcctl install --alert-webhook`, `--alert-email` o `--alert-config`.",
           "Gravedad", "Grupo de reglas", "Qué significa y qué hacer"),
    "zh-CN": ("告警手册",
              "Chart 附带的每一条告警都有一个 `runbook_url`，指向本页中对应的条目。每个条目说明告警的严重程度、告警在告诉你什么，"
              "以及该怎么处理。正文就是告警自身的描述，因此通知与本页不会互相矛盾。\n\n只有当 Alertmanager 配置了接收器，告警才会送达"
              "真人。请参阅[将告警路由给真人](./observability.md#alert-routing)，用 `dcctl install --alert-webhook`、`--alert-email` "
              "或 `--alert-config` 进行设置。",
              "严重程度", "规则组", "含义与处理方式"),
}


def render(alerts, loc):
    title, intro, sev, grp, what = HEAD[loc]
    out = ["---", "sidebar_position: 5", "title: %s" % title, "---", "",
           "{/* GENERATED by hack/alert-runbook.py from the chart's rule templates. Do not edit; run `hack/alert-runbook.py gen .` */}",
           "", "# %s" % title, "", intro, ""]
    groups = {}
    for a in alerts:
        groups.setdefault(a["group"] or "ungrouped", []).append(a)
    for g in sorted(groups):
        out += ["## `%s`" % g, ""]
        for a in sorted(groups[g], key=lambda x: x["name"]):
            out += ["### %s {#%s}" % (a["name"], a["name"].lower()), "",
                    "**%s:** `%s` · **%s:** `%s`" % (sev, a["severity"] or "unset", grp, g), ""]
            if a["summary"]:
                out += [md(a["summary"]), ""]
            if a["description"]:
                out += ["**%s.** %s" % (what, md(a["description"])), ""]
    return "\n".join(out).rstrip("\n") + "\n"


def problems(repo, alerts):
    bad = []
    if not alerts:
        bad.append("found no alerts under %s: the reader is broken, not the chart" % RULES)
    seen = {}
    for a in alerts:
        if a["name"] in seen:
            bad.append("%s is defined twice (%s and %s): its runbook anchor would be ambiguous" % (a["name"], seen[a["name"]], a["file"]))
        seen[a["name"]] = a["file"]
        if not a["has_annotations"]:
            bad.append("%s (%s) has no annotations block" % (a["name"], a["file"]))
        elif a["runbook_url"] is None:
            bad.append("%s (%s) has no runbook_url; want %s" % (a["name"], a["file"], url(a["name"])))
        elif a["runbook_url"] != url(a["name"]):
            bad.append("%s (%s) has runbook_url %s; want %s" % (a["name"], a["file"], a["runbook_url"], url(a["name"])))
        if not a["summary"]:
            bad.append("%s (%s) has no summary" % (a["name"], a["file"]))
    for loc, rel in PAGES.items():
        p = os.path.join(repo, rel)
        if not os.path.exists(p):
            bad.append("%s is missing: run hack/alert-runbook.py gen ." % rel)
        else:
            with open(p, encoding="utf-8", newline="") as fh:
                have = fh.read()
            if have != render(alerts, loc):
                bad.append("%s is stale against the rules: run hack/alert-runbook.py gen ." % rel)
    return bad


def main(argv):
    if len(argv) != 3 or argv[1] not in ("list", "gen", "check"):
        sys.exit(__doc__)
    cmd, repo = argv[1], argv[2]
    alerts = read_alerts(repo)
    if cmd == "list":
        print(json.dumps(alerts, indent=1, ensure_ascii=False))
    elif cmd == "gen":
        for loc, rel in PAGES.items():
            p = os.path.join(repo, rel)
            os.makedirs(os.path.dirname(p), exist_ok=True)
            with open(p, "w", encoding="utf-8", newline="\n") as f:
                f.write(render(alerts, loc))
        print("wrote %d alerts to %d pages" % (len(alerts), len(PAGES)))
    else:
        bad = problems(repo, alerts)
        for b in bad:
            print("  " + b)
        if bad:
            sys.exit(1)
        print("%d alerts, each with its runbook_url and an entry in %d locales" % (len(alerts), len(PAGES)))


if __name__ == "__main__":
    main(sys.argv)
