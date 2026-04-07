package cmd

import (
	"paasctl/internal/clients"
	"paasctl/internal/config"
)

func newWhiteSkyClient(cfg config.Config, requireCloudspace bool) (*clients.WhiteSkyClient, string) {
	wsCfg := cfg.WhiteSky
	resolvedName := ""
	if requireCloudspace {
		var err error
		wsCfg, resolvedName, err = cfg.ResolveWhiteSky(cloudspace)
		if err != nil {
			failf("config error: %v", err)
		}
	} else {
		if err := cfg.ValidateWhiteSkyCredentials(); err != nil {
			failf("config error: %v", err)
		}
		if selected, name, err := cfg.ResolveWhiteSky(cloudspace); err == nil {
			wsCfg = selected
			resolvedName = name
		}
	}

	return clients.NewWhiteSkyClient(clients.WhiteSkyConfig{
		BaseURL:        wsCfg.BaseURL,
		IAMBaseURL:     wsCfg.IAMBaseURL,
		Token:          wsCfg.Token,
		CustomerID:     wsCfg.CustomerID,
		CloudspaceID:   wsCfg.CloudspaceID,
		RequestTimeout: wsCfg.RequestTimeout,
	}), resolvedName
}
