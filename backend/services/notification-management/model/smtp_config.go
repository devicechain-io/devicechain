// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
)

type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	From     string `json:"from"`
	Username string `json:"username"`
	Security string `json:"security"`
}

const (
	smtpSecurityStartTLS = "starttls"
	smtpSecurityTLS      = "tls"
	smtpSecurityNone     = "none"
)

func ParseSMTPConfig(token string, config *string) (*SMTPConfig, error) {
	cfg := &SMTPConfig{}
	if config != nil {
		if err := json.Unmarshal([]byte(*config), cfg); err != nil {
			return nil, fmt.Errorf("smtp channel %q has invalid config: %w", token, err)
		}
	}
	if cfg.Host == "" {
		return nil, fmt.Errorf("smtp channel %q config is missing host", token)
	}
	if cfg.From == "" {
		return nil, fmt.Errorf("smtp channel %q config is missing from", token)
	}
	if cfg.Security == "" {
		cfg.Security = smtpSecurityStartTLS
	}
	switch cfg.Security {
	case smtpSecurityStartTLS, smtpSecurityTLS, smtpSecurityNone:
	default:
		return nil, fmt.Errorf("smtp channel %q has unknown security %q (want starttls|tls|none)", token, cfg.Security)
	}
	if cfg.Port == 0 {
		if cfg.Security == smtpSecurityTLS {
			cfg.Port = 465
		} else {
			cfg.Port = 587
		}
	}
	return cfg, nil
}
