package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"paasctl/internal/clients"
	"paasctl/internal/config"
)

var (
	templatesProvider string
	templatesCategory string
	templatesSearch   string
)

func init() {
	templatesCmd.Flags().StringVar(&templatesProvider, "provider", "", "PaaS provider to list templates from")
	templatesCmd.Flags().StringVar(&templatesCategory, "category", "", "Filter by template category (case-insensitive)")
	templatesCmd.Flags().StringVar(&templatesSearch, "search", "", "Filter by template name (case-insensitive substring)")
	_ = templatesCmd.MarkFlagRequired("provider")
}

var templatesCmd = &cobra.Command{
	Use:   "templates",
	Short: "List available templates from a PaaS provider",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		provider, providerErr := buildProvider(templatesProvider, cfg)
		if providerErr != nil {
			failf("config error: %v", providerErr)
		}

		items, err := provider.ListTemplates()
		if err != nil {
			failf("failed to list %s templates: %v", provider.Name(), err)
		}

		filtered := make([]clients.TemplateSpec, 0, len(items))
		for _, item := range items {
			if templatesCategory != "" && !strings.Contains(strings.ToLower(item.Category), strings.ToLower(templatesCategory)) {
				continue
			}
			if templatesSearch != "" && !strings.Contains(strings.ToLower(item.TemplateName), strings.ToLower(templatesSearch)) {
				continue
			}
			filtered = append(filtered, item)
		}

		if len(filtered) == 0 {
			if outputJSON {
				printJSONOK(filtered)
				return
			}
			fmt.Println("No templates found.")
			return
		}

		if outputJSON {
			printJSONOK(filtered)
			return
		}

		w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tCATEGORY\tNAME\tVERSION")
		fmt.Fprintln(w, "--\t--------\t----\t-------")
		for _, item := range filtered {
			fmt.Fprintf(
				w,
				"%d\t%s\t%s\t%s\n",
				item.TemplateID,
				clip(item.Category, 24),
				clip(item.TemplateName, 32),
				clip(item.TemplateVersion, 20),
			)
		}
		_ = w.Flush()
	},
}

func clip(value string, max int) string {
	v := strings.TrimSpace(value)
	if max <= 0 || len(v) <= max {
		return v
	}
	if max <= 3 {
		return v[:max]
	}
	return v[:max-3] + "..."
}
