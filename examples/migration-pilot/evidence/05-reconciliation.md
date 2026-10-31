# Reconciliation report

Source: `examples/migration-pilot/source` (sanitized export of a previous platform).
Target: tenant `migration-pilot`.

## 1. Accounting: everything in the export is accounted for

| Item | In export | Created | Already there | Skipped | Accounted | Match |
| --- | ---: | ---: | ---: | ---: | ---: | :---: |
| profile | 2 | 2 | 0 | 0 | 2 | yes |
| deviceType | 2 | 2 | 0 | 0 | 2 | yes |
| metric | 8 | 7 | 0 | 1 | 8 | yes |
| rule | 3 | 2 | 0 | 1 | 3 | yes |
| dashboard | 1 | 0 | 0 | 1 | 1 | yes |
| device | 25 | 23 | 0 | 2 | 25 | yes |
| credential | 25 | 23 | 0 | 2 | 25 | yes |
| attribute | 25 | 23 | 0 | 2 | 25 | yes |

## 2. Read back: DeviceChain holds what the ledger says

| Item | Ledger (created + already there) | Counted in DeviceChain | Match |
| --- | ---: | ---: | :---: |
| metric | 7 | 7 | yes |
| rule | 2 | 2 | yes |
| device | 23 | 23 | yes |
| credential | 23 | 23 | yes |
| attribute | 23 | 23 | yes |

## 3. Skipped, and why

| Item | Source | Reason |
| --- | --- | --- |
| metric | Cold Chain Sensor.status_text | a metric must be numeric or boolean; report the value as a device attribute instead |
| rule | Pump pressure check and restart | not carried: a free-form script flow has no equivalent; rebuild it as rules plus a webhook |
| dashboard | Cold chain overview | dashboards are rebuilt by hand in the canvas editor |
| device | PC-0007 (Pump controller 7) | INVALID_VALUE: credentialValue must be at least 16 bytes for an MQTT_BASIC credential |
| credential | PC-0007 | its device was not imported |
| device | CC-0003 (Cold room sensor 17 (spare)) | DUPLICATE_IN_FILE: token also appears at row 3 |
| credential | CC-0003 | its device was not imported |
| attribute | pc-0007 site | its device was not imported |
| attribute | cc-0003 site | its device was not imported |

**Result: reconciled.**
