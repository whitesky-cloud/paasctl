package cmd

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"paasctl/internal/config"
)

func init() {
	configCmd.AddCommand(configPathCmd)
	configCmd.AddCommand(configKeysCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configUnsetCmd)
	configCmd.AddCommand(configUnlockCmd)
	configCmd.AddCommand(configRelockCmd)
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Show how to configure paasctl",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		requiredWhiteSky := []envRow{
			{Name: "PAASCTL_WHITESKY_TOKEN", Value: cfg.WhiteSky.Token, Required: true},
			{Name: "PAASCTL_WHITESKY_CUSTOMER_ID", Value: cfg.WhiteSky.CustomerID, Required: true},
		}
		requiredElestio := []envRow{
			{Name: "PAASCTL_ELESTIO_EMAIL", Value: cfg.Elestio.Email, Required: true},
			{Name: "PAASCTL_ELESTIO_API_TOKEN", Value: cfg.Elestio.APIToken, Required: true},
		}
		optional := []envRow{
			{Name: "PAASCTL_WHITESKY_BASE_URL", Value: cfg.WhiteSky.BaseURL, Default: "https://try.whitesky.cloud/api/1"},
			{Name: "PAASCTL_WHITESKY_IAM_BASE_URL", Value: cfg.WhiteSky.IAMBaseURL},
			{Name: "PAASCTL_WHITESKY_REQUEST_TIMEOUT", Value: cfg.WhiteSky.RequestTimeout.String(), Default: "300s"},
			{Name: "PAASCTL_ELESTIO_BASE_URL", Value: cfg.Elestio.BaseURL, Default: "https://api.elest.io"},
			{Name: "PAASCTL_ELESTIO_PROJECT_ID", Value: cfg.Elestio.ProjectID, Default: "<auto-infer from JWT when possible>"},
			{Name: "PAASCTL_ELESTIO_BYOVM_PRICE_PER_HOUR", Value: formatOptionalFloat(cfg.Elestio.BYOVMPricePerHour), Default: "0"},
			{Name: "PAASCTL_ELESTIO_BYOVM_PROVIDER_LABEL", Value: cfg.Elestio.BYOVMProviderLabel, Default: "whitesky.cloud"},
		}
		if outputJSON {
			printJSONOK(map[string]interface{}{
				"config_file":                config.DefaultConfigFilePath(),
				"environment_overrides_file": true,
				"required_whitesky":          envRowsJSON(requiredWhiteSky),
				"required_elestio":           envRowsJSON(requiredElestio),
				"optional":                   envRowsJSON(optional),
				"cloudspaces":                cfg.WhiteSky.Cloudspaces,
			})
			return
		}

		fmt.Println("paasctl configuration")
		fmt.Printf("Config file: %s\n", config.DefaultConfigFilePath())
		fmt.Println("Environment variables override config file values.")
		fmt.Println("")
		fmt.Println("Required for all commands using whitesky.cloud:")
		printEnvTable(requiredWhiteSky)
		fmt.Println("")
		fmt.Println("Configured whitesky.cloud cloudspaces:")
		printCloudspaces(cfg.WhiteSky.Cloudspaces)

		fmt.Println("")
		fmt.Println("Required for provider integration:")
		fmt.Println("Elestio:")
		printEnvTable(requiredElestio)

		fmt.Println("")
		fmt.Println("Optional settings:")
		printEnvTable(optional)

		fmt.Println("")
		fmt.Println("Example:")
		fmt.Println(`export PAASCTL_WHITESKY_TOKEN="..."`)
		fmt.Println(`export PAASCTL_WHITESKY_CUSTOMER_ID="..."`)
		fmt.Println(`export PAASCTL_WHITESKY_IAM_BASE_URL="https://<iam-domain>"`)
		fmt.Println(`export PAASCTL_WHITESKY_CLOUDSPACE_ID="..."`)
		fmt.Println(`export PAASCTL_ELESTIO_EMAIL="..."`)
		fmt.Println(`export PAASCTL_ELESTIO_API_TOKEN="..."`)
		fmt.Println(`export PAASCTL_WHITESKY_REQUEST_TIMEOUT="300s"`)
		fmt.Println("")
		fmt.Println(`Then run: ./paasctl deploy --provider elestio --template-id <id> --name <name>`)
	},
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the YAML config file path",
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON {
			printJSONOK(map[string]string{"path": config.DefaultConfigFilePath()})
			return
		}
		fmt.Println(config.DefaultConfigFilePath())
	},
}

var configKeysCmd = &cobra.Command{
	Use:   "keys",
	Short: "List supported YAML config keys",
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON {
			printJSONOK(config.ConfigurableKeys())
			return
		}
		for _, key := range config.ConfigurableKeys() {
			fmt.Println(key)
		}
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a value in the YAML config file",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		value := args[1]
		if err := config.SetFileValue(key, value); err != nil {
			failf("failed to set config value: %v", err)
		}
		if outputJSON {
			printJSONOK(map[string]string{"key": key, "path": config.DefaultConfigFilePath()})
			return
		}
		fmt.Printf("Set %s in %s\n", key, config.DefaultConfigFilePath())
	},
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <key>",
	Short: "Remove a value from the YAML config file",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		key := args[0]
		if err := config.UnsetFileValue(key); err != nil {
			failf("failed to unset config value: %v", err)
		}
		if outputJSON {
			printJSONOK(map[string]string{"key": key, "path": config.DefaultConfigFilePath()})
			return
		}
		fmt.Printf("Unset %s in %s\n", key, config.DefaultConfigFilePath())
	},
}

var configUnlockCmd = &cobra.Command{
	Use:   "unlock",
	Short: "Unlock encrypted YAML config secrets using ssh-agent",
	Run: func(cmd *cobra.Command, args []string) {
		password, err := configReadPassword("Config password: ")
		if err != nil {
			failf("failed to read password: %v", err)
		}
		if err := config.Unlock(password); err != nil {
			failf("failed to unlock config: %v", err)
		}
		if outputJSON {
			printJSONOK(map[string]bool{"unlocked": true})
			return
		}
		fmt.Println("Config unlocked in ssh-agent.")
	},
}

var configRelockCmd = &cobra.Command{
	Use:   "relock",
	Short: "Remove the config unlock token from ssh-agent",
	Run: func(cmd *cobra.Command, args []string) {
		if err := config.Relock(); err != nil {
			failf("failed to relock config: %v", err)
		}
		if outputJSON {
			printJSONOK(map[string]bool{"unlocked": false})
			return
		}
		fmt.Println("Config relocked.")
	},
}

type envRow struct {
	Name     string
	Value    string
	Default  string
	Required bool
}

func envRowsJSON(rows []envRow) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]interface{}{
			"name":     row.Name,
			"status":   envStatus(row),
			"value":    envDisplayValue(row),
			"required": row.Required,
			"default":  row.Default,
		})
	}
	return out
}

func printEnvTable(rows []envRow) {
	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ENV\tSTATUS\tVALUE")
	fmt.Fprintln(w, "---\t------\t-----")
	for _, row := range rows {
		fmt.Fprintf(w, "%s\t%s\t%s\n", row.Name, envStatus(row), envDisplayValue(row))
	}
	_ = w.Flush()
}

func printCloudspaces(cloudspaces map[string]config.CloudspaceConfig) {
	if len(cloudspaces) == 0 {
		fmt.Println("<none>")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCLOUDSPACE_ID")
	fmt.Fprintln(w, "----\t-------------")
	for _, name := range sortedCloudspaceNames(cloudspaces) {
		fmt.Fprintf(w, "%s\t%s\n", name, cloudspaces[name].CloudspaceID)
	}
	_ = w.Flush()
}

func sortedCloudspaceNames(cloudspaces map[string]config.CloudspaceConfig) []string {
	names := make([]string, 0, len(cloudspaces))
	for name := range cloudspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func envStatus(row envRow) string {
	if row.Value != "" {
		return "set"
	}
	if row.Required {
		return "missing"
	}
	return "default"
}

func envDisplayValue(row envRow) string {
	if row.Value == "" {
		if row.Default != "" {
			return row.Default
		}
		return "-"
	}
	if isSecretEnv(row.Name) {
		return maskSecret(row.Value)
	}
	return row.Value
}

func isSecretEnv(name string) bool {
	return name == "PAASCTL_WHITESKY_TOKEN" || name == "PAASCTL_ELESTIO_API_TOKEN"
}

func maskSecret(value string) string {
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func formatOptionalFloat(v float64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%g", v)
}

func configReadPassword(prompt string) (string, error) {
	return config.ReadPassword(prompt)
}
