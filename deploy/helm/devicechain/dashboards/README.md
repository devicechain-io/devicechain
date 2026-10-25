# Board conventions

How to add or change a Grafana board in this directory. Every `*.json` here is rendered once
per instance by `templates/grafana-dashboard.yaml` (read its header first) and checked by
`hack/check-dashboards.sh`, which enforces most of what follows.

## Adding a board: three steps

1. Create `dashboards/<stem>.json` following the scheme below.
2. Add `<stem>` to the `BOARDS` list at the top of `hack/check-dashboards.sh`. That one entry
   pins the board's ConfigMap name (`{instance}-<stem>-dashboard`) and data key
   (`{instance}-<stem>.json`), which are published names. The check fails if a file is not
   in the list or an entry has no file.
3. Run `hack/check-dashboards.sh` and `hack/check-dashboards.sh --self-test`.

## Identity

| What | Rule |
| --- | --- |
| File | `dashboards/<stem>.json`, `<stem>` lower-case words joined by `-` (`overview`, `event-processing`) |
| `uid` | `dc-<stem>-ops`. The chart replaces it per instance with a hash, so the file's value only names the board |
| `title` | `DeviceChain — <Name>`. The chart appends ` [<instance>]` |
| `tags` | include `devicechain`, then the board's own words. The chart adds one tag unique to the instance |
| Folder | not set in the file. The chart files every board of an instance under `devicechain-<instance>` |
| `links` | leave empty. The chart adds a "DeviceChain boards" dropdown that lists this instance's boards and no other's |

## Template variables

Every board declares exactly these three, in this order, and nothing else is needed for scoping:

- `datasource`: the Prometheus data source picker (copy it from any board).
- `namespace`: `{"type": "constant", "hide": 2, "query": ""}`. The chart replaces it with the instance's namespace.
  Every series selector ends `namespace="$namespace"`.
- `instance`: `{"type": "constant", "hide": 2, "query": ""}`. The chart replaces it with the instance ID.
  Only the alerts table below uses it: the alert rules label alerts with the ID, not the namespace.

There is no instance picker. A board cannot read another instance's data.

## Linking to another board

Write `@board:<stem>@` wherever the uid goes, for example a panel link `/d/@board:event-processing@`
or a data link `/d/@board:messaging@?var-datasource=${datasource}`. The chart substitutes that
instance's own uid; a reference to a board that does not exist fails the render. Never paste a
uid: it is a hash of the instance, so a pasted one points into another instance (the check
refuses it).

## The database namespace

The relational database and the backup object store live in a shared namespace
(`metrics.databaseNamespace`), not in the instance's. A board that shows them writes `@dbnamespace@`
where the namespace goes, and the chart substitutes the configured value in the file text, the same way
it does `@board:<stem>@`. Select the instance's own event store and the shared database together with
`namespace=~"@dbnamespace@|$namespace"`; say in the description that the shared database serves every
instance in the cluster.

## The firing-alerts table

Every board starts with this panel at `x=0, y=0, w=24`. Copy it, change only the `alertname`
regex in `expr`, and start the board's own panels at `y=7`. Keep `id` 100 for it and use other
ids for everything else. `.+` lists every alert (the overview); a stage board lists its own
alerts by name, for example `Detect.*|React.*`. The overview shows all of them, so an alert a
stage board does not match is still visible there.

```json
{
  "id": 100,
  "type": "table",
  "title": "Firing alerts",
  "description": "Alerts firing right now for this instance. An empty table means none are firing. Alerts come from the instance's Prometheus rules, so the table stays empty if those rules are not installed.",
  "gridPos": {
    "h": 6,
    "w": 24,
    "x": 0,
    "y": 0
  },
  "datasource": {
    "type": "prometheus",
    "uid": "${datasource}"
  },
  "targets": [
    {
      "refId": "A",
      "datasource": {
        "type": "prometheus",
        "uid": "${datasource}"
      },
      "instant": true,
      "format": "table",
      "expr": "max by (alertname, severity) (ALERTS{alertstate=\"firing\", devicechain_instance=\"$instance\", alertname=~\".+\"})"
    }
  ],
  "fieldConfig": {
    "defaults": {
      "custom": {
        "align": "auto",
        "cellOptions": {
          "type": "auto"
        }
      },
      "color": {
        "mode": "thresholds"
      },
      "thresholds": {
        "mode": "absolute",
        "steps": [
          {
            "color": "green",
            "value": null
          }
        ]
      }
    },
    "overrides": [
      {
        "matcher": {
          "id": "byName",
          "options": "severity"
        },
        "properties": [
          {
            "id": "mappings",
            "value": [
              {
                "type": "value",
                "options": {
                  "critical": {
                    "color": "red",
                    "index": 0
                  },
                  "warning": {
                    "color": "yellow",
                    "index": 1
                  }
                }
              }
            ]
          },
          {
            "id": "custom.cellOptions",
            "value": {
              "type": "color-text"
            }
          }
        ]
      }
    ]
  },
  "options": {
    "showHeader": true,
    "cellHeight": "sm",
    "sortBy": [
      {
        "displayName": "severity",
        "desc": false
      }
    ]
  },
  "transformations": [
    {
      "id": "organize",
      "options": {
        "excludeByName": {
          "Time": true,
          "Value": true
        },
        "indexByName": {
          "alertname": 0,
          "severity": 1
        },
        "renameByName": {
          "alertname": "Alert",
          "severity": "severity"
        }
      }
    }
  ]
}
```

The check requires: first panel, type `table`, title `Firing alerts`, at the top left, selecting
`ALERTS{alertstate="firing", devicechain_instance="$instance", ...}`. Any other
`devicechain_instance` selector must also use `"$instance"`, never a literal ID.

## Queries

- Select every series with `namespace="$namespace"`. Series names are checked against the Go
  registrations; a name that does not exist fails the check.
- Rates over time: `rate(...[$__rate_interval])`, never a fixed window.
- Events whose healthy count is zero (stranded, refused, lost, poisoned): a **stat** shows
  `round(sum(increase(...[$__range])))` as an instant query, so one occurrence stays visible for
  the whole selected range, and a **time series** beside it shows the rate. A rate stat shows one
  event as a tiny fraction and then goes green again.
- Gauges that only the replica holding a lease reports: aggregate with `max()` over the instance,
  and say in the description that a standby reads zero.
- Histograms export only `_bucket`, `_sum` and `_count`. Use `histogram_quantile(q, sum by (le)
  (rate(<name>_bucket[$__rate_interval])))`, never the bare name.

## Units and panels

- Set `fieldConfig.defaults.unit` on every stat and time series: `s`, `bytes`, `percentunit`
  (0..1), `ops` (events per second), `short` (counts). Do not put two units on one axis; split
  the panel or use a field override for the second.
- A stat that should alarm sets threshold steps. The first step is `green` with `value: null`.
- A panel that has nothing to show on a default install (an opt-in service) says so in its
  description.
- Give every panel a `description` that says what the number means and what a reader should do
  when it is bad, in the order an on-call asks.

## Text

Board titles, row titles, panel titles, descriptions and legends are published in screenshots.
Write plain operator language: no decision-record or ADR numbers, no design-decision tags, no
paths to private planning documents, no red-circle markers, no engine-internal vocabulary without
a plain explanation. The check refuses the first four.

## Checking by hand

```bash
hack/check-dashboards.sh              # files, series, names, two-instance render
hack/check-dashboards.sh --self-test  # every rule above, each broken alone and caught
```
