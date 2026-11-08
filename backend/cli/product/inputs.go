// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package product

import "reflect"

// Each resource has one function that renders it as the GraphQL input fields it manages. A
// bundle's resource and the tenant's live one are both rendered by it, so "does anything
// differ" is one map comparison, the changed keys are the update request, and the names of the
// changed keys are the plan's detail line. An absent value is an untyped nil: sent on an
// update it is an explicit null, which clears the field; left out of a create.

func (t DeviceType) input() map[string]any {
	return map[string]any{
		"name": str(t.Name), "description": str(t.Description), "imageUrl": str(t.ImageURL),
		"icon": str(t.Icon), "backgroundColor": str(t.BackgroundColor), "foregroundColor": str(t.ForegroundColor),
		"borderColor": str(t.BorderColor), "profileToken": str(t.Profile),
		"maintenanceWindowToken": str(t.MaintenanceWindow), "manufacturer": str(t.Manufacturer),
		"model": str(t.Model), "metadata": text(t.Metadata),
	}
}

func locInput(l *Location) any {
	if l == nil {
		return nil
	}
	// Both keys are always present, null when absent: an update is partial, so an omitted
	// key would leave the stored value in place instead of clearing it.
	return map[string]any{
		"expectedAccuracyMeters":        fptr(l.ExpectedAccuracyMeters),
		"expectedUpdateIntervalSeconds": iptr(l.ExpectedUpdateIntervalSeconds),
	}
}

func (p Profile) input() map[string]any {
	return map[string]any{
		"name": str(p.Name), "description": str(p.Description), "category": str(p.Category),
		"metadata": text(p.Metadata), "location": locInput(p.Location),
	}
}

func (m Metric) input() map[string]any {
	return map[string]any{
		"metricKey": m.Key, "name": str(m.Name), "description": str(m.Description), "dataType": m.DataType,
		"unit": str(m.Unit), "minValue": fptr(m.Min), "maxValue": fptr(m.Max), "enum": text(m.Enum),
		"descriptor": str(m.Descriptor), "metadata": text(m.Metadata),
	}
}

func (m CalculatedMetric) input() map[string]any {
	in := Metric{Key: m.Key, Name: m.Name, Description: m.Description, DataType: m.DataType, Unit: m.Unit,
		Min: m.Min, Max: m.Max, Enum: m.Enum, Descriptor: m.Descriptor, Metadata: m.Metadata}.input()
	in["expression"] = m.Expression
	return in
}

func (c Command) input() map[string]any {
	return map[string]any{
		"commandKey": c.Key, "name": str(c.Name), "description": str(c.Description),
		"parameterSchema": text(c.ParameterSchema), "impact": c.Impact, "metadata": text(c.Metadata),
	}
}

func (r Rule) input() map[string]any {
	return map[string]any{
		"name": str(r.Name), "description": str(r.Description), "definition": text(r.Definition),
		"authoringGraph": text(r.AuthoringGraph), "enabled": r.Enabled, "metadata": text(r.Metadata),
		"entityGroupToken": str(r.EntityGroupToken), "entityGroupVersion": iptr(r.EntityGroupVersion),
	}
}

func (in Integration) input() map[string]any {
	return map[string]any{
		"name": str(in.Name), "description": str(in.Description), "enabled": in.Enabled,
		"deviceTypeTokens": append([]string{}, in.DeviceTypes...), "identity": str(in.Identity),
		"generic": text(in.Generic), "linkMetrics": in.LinkMetrics, "metadata": text(in.Metadata),
	}
}

func (c Connector) input() map[string]any {
	return map[string]any{
		"name": str(c.Name), "description": str(c.Description), "type": c.Type, "config": text(c.Config),
	}
}

func (d Dashboard) input() map[string]any {
	return map[string]any{
		"name": str(d.Name), "description": str(d.Description), "definition": text(d.Definition),
	}
}

// diffInputs returns the entries of want that differ from have, and their names in order.
func diffInputs(want, have map[string]any) (changed map[string]any, names []string) {
	changed = map[string]any{}
	for k, w := range want {
		if !reflect.DeepEqual(w, have[k]) {
			changed[k] = w
			names = append(names, k)
		}
	}
	sortStrings(names)
	return changed, names
}

// createInput is a create request: the managed fields that hold a value, plus the extras.
func createInput(in map[string]any, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if v != nil {
			out[k] = v
		}
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
