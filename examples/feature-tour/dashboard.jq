# Copyright The DeviceChain Authors
# SPDX-License-Identifier: Apache-2.0
#
# The cold-room board, as the JSON `definition` of createDashboard. It is the board the console's
# "Create dashboard" on a device type's Profile tab builds (a device selector, one latest-value
# card per declared measurement, a chart, an alarm table, and a button per command that is safe to
# send from a board), written out so a script can create it without the console.
#
#   jq -n --arg title "..." --arg device coldroom-a1 --arg command boost-cooling --argjson buttons true -f dashboard.jq
#
# $buttons=false leaves the command button off: the customer's copy, because a customer user holds
# a read-only role and a button that always refuses is a poor thing to put in front of them.
def place($col; $row; $cols; $rows): {base: {mode: "grid", col: $col, row: $row, cols: $cols, rows: $rows}};
def slot($m): {kind: "slot", slot: "device", measurements: $m};

{
  schemaVersion: 3,
  title: $title,
  canvas: {breakpoints: {base: 0}, grid: {columns: 12, gutter: 8, minCellSize: 56, padding: 0}, sizing: "fill"},
  slots: {device: {label: "Cold room", type: "device", defaultBinding: {kind: "device", deviceToken: $device}}},
  widgets: ([
    {id: "selector", type: "entity-selector", layout: place(0; 0; 6; 1),
     options: {title: "Cold room", selectionTarget: "device"}},
    {id: "card-temperature", type: "latest-card", layout: place(0; 1; 3; 2), datasource: slot(["temperature"]),
     options: {title: "Temperature", measurement: "temperature", unit: "Cel", precision: 1, flashOnChange: true}},
    {id: "card-humidity", type: "latest-card", layout: place(3; 1; 3; 2), datasource: slot(["humidity"]),
     options: {title: "Humidity", measurement: "humidity", unit: "%", precision: 1, flashOnChange: true}},
    {id: "gauge-temperature", type: "gauge", layout: place(6; 1; 3; 3), datasource: slot(["temperature"]),
     options: {title: "Temperature", measurement: "temperature", unit: "Cel", min: -5, max: 15}},
    {id: "alarm-count", type: "alarm-count", layout: place(9; 1; 3; 2), datasource: slot([]),
     options: {title: "Active alarms", state: "ACTIVE"}},
    {id: "chart", type: "timeseries-chart", layout: place(0; 4; 8; 4), datasource: slot(["temperature"]),
     options: {title: "Temperature", unit: "Cel", markAlarms: true}},
    {id: "alarms", type: "alarm-table", layout: place(8; 4; 4; 4), datasource: slot([]),
     options: {title: "Alarms", state: "ACTIVE", maxRows: 25}}
  ] + (if $buttons then [
    {id: "command-1", type: "command-button", layout: place(9; 3; 3; 1), datasource: slot([]),
     options: {title: "Boost cooling", commandName: $command, commandLabel: "Boost cooling"}}
  ] else [] end))
}
