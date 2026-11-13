// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/payloadencode"
)

func validateCommandEncoder(raw, schema *string) error {
	if raw == nil {
		return nil
	}
	e, err := payloadencode.Parse([]byte(*raw))
	if err != nil {
		return err
	}
	var params []ParameterSpec
	if schema != nil && *schema != "" {
		params, err = decodeParameterSchemaStrict([]byte(*schema))
		if err != nil {
			return err
		}
	}
	if len(params) == 0 {
		return fmt.Errorf("encoder requires a non-empty parameter schema")
	}
	if err := ValidateParameterSchema(params); err != nil {
		return err
	}
	info := make([]payloadencode.ParamInfo, 0, len(params))
	for _, p := range params {
		info = append(info, payloadencode.ParamInfo{Name: p.Name, Kind: string(p.DataType)})
	}
	return e.ValidateAgainst(info)
}
