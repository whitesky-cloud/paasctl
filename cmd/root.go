package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"paasctl/internal/clients"
)

var (
	logAPI     bool
	outputJSON bool
	cloudspace string
)

var rootCmd = &cobra.Command{
	Use:           "paasctl",
	Short:         "CLI to deploy and manage PaaS apps in whitesky.cloud cloudspaces",
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		clients.SetDebugHTTP(logAPI)
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&logAPI, "log-api", false, "Log all API requests and responses to stderr")
	rootCmd.PersistentFlags().BoolVar(&outputJSON, "json", false, "Output command results as JSON")
	rootCmd.PersistentFlags().StringVar(&cloudspace, "cloudspace", "", "whitesky.cloud cloudspace name from config or direct cloudspace ID")
}

func Execute() error {
	if err := rootCmd.Execute(); err != nil {
		if outputJSON {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]interface{}{
				"ok":    false,
				"error": err.Error(),
			})
			os.Exit(1)
		}
		return err
	}
	return nil
}

func failf(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if outputJSON {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]interface{}{
			"ok":    false,
			"error": message,
		})
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}

func printJSON(value interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		failf("failed to encode json output: %v", err)
	}
}

func printJSONOK(value interface{}) {
	printJSON(map[string]interface{}{
		"ok":     true,
		"result": value,
	})
}

func warnf(format string, args ...interface{}) string {
	message := fmt.Sprintf(format, args...)
	if !outputJSON {
		fmt.Printf("Warning: %s\n", message)
	}
	return message
}
