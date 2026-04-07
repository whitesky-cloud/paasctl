package cmd

import (
	"fmt"
	"strings"

	"paasctl/internal/config"
	"paasctl/internal/providers"
)

func buildProvider(name string, cfg config.Config) (providers.Provider, error) {
	providerName := strings.ToLower(strings.TrimSpace(name))
	if providerName == "" {
		return nil, fmt.Errorf("missing provider; run `paasctl list providers` to see available providers")
	}

	switch providerName {
	case providers.ElestioName:
		return providers.NewElestioProvider(providers.ElestioConfig{
			BaseURL:            cfg.Elestio.BaseURL,
			Email:              cfg.Elestio.Email,
			APIToken:           cfg.Elestio.APIToken,
			ProjectID:          cfg.Elestio.ProjectID,
			BYOVMPricePerHour:  cfg.Elestio.BYOVMPricePerHour,
			BYOVMProviderLabel: cfg.Elestio.BYOVMProviderLabel,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q (available: %s)", name, providers.ElestioName)
	}
}
