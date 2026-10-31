<!--
Copyright The DeviceChain Authors
SPDX-License-Identifier: Apache-2.0
-->

# Evidence

Output of `./tour.sh sites/site-a.env all` and `./tour.sh sites/site-b.env all`, run one after the
other against one fresh instance on a fresh kind cluster:

- DeviceChain v0.27.0 (`dcctl install local --compact`, `dcctl bootstrap local tour --host localhost --no-tls --no-escrow`);
- the console on a non-default host port, so `DC_URL` was set to match (the scripts take it from the environment);
- Docker Desktop on Windows 11, scripts run under Git Bash.

| File | What it is |
| --- | --- |
| `site-a-run.txt`, `site-b-run.txt` | The full output of each run: provisioning, then every `PASS` line of the checks |
| `screenshots/site-a-operator-board.png`, `site-b-operator-board.png` | The operator's board in each tenant |
| `screenshots/site-a-operator-alarms.png` | The operator's Alarms page: both rooms raised and cleared the alarm |
| `screenshots/site-a-customer-devices.png`, `site-a-customer-board.png` | What the customer user of site A sees: one device, one board, one alarm |
| `screenshots/site-b-customer-board.png` | The customer user of site B |
| `screenshots/console-create-dashboard-from-device-type.png` | **Devices → Device Types → Cold room → Profile → Create dashboard** |
