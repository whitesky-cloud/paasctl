package cmd

import (
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"paasctl/internal/clients"
	"paasctl/internal/config"
	"paasctl/internal/deployments"
	"paasctl/internal/providers"
)

func init() {
	listCmd.AddCommand(listDeploymentsCmd)
	listCmd.AddCommand(listCloudspacesCmd)
	listCmd.AddCommand(listTLDsCmd)
	listCmd.AddCommand(listProvidersCmd)
	listCmd.AddCommand(templatesCmd)
	rootCmd.AddCommand(listCmd)
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List resources for the configured customer/cloudspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		return cmd.Help()
	},
}

var listDeploymentsCmd = &cobra.Command{
	Use:   "deployments",
	Short: "List deployed PaaS services/apps in the configured cloudspace",
	Run: func(cmd *cobra.Command, args []string) {
		runListDeployments()
	},
}

var listCloudspacesCmd = &cobra.Command{
	Use:   "cloudspaces",
	Short: "List configured whitesky.cloud cloudspaces",
	Run: func(cmd *cobra.Command, args []string) {
		runListCloudspaces()
	},
}

var listTLDsCmd = &cobra.Command{
	Use:   "tlds",
	Short: "List available top-level domains for the customer",
	Run: func(cmd *cobra.Command, args []string) {
		runListTLDs()
	},
}

var listProvidersCmd = &cobra.Command{
	Use:   "providers",
	Short: "List supported PaaS providers",
	Run: func(cmd *cobra.Command, args []string) {
		runListProviders()
	},
}

func runListProviders() {
	if outputJSON {
		printJSONOK(providers.Supported())
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tDESCRIPTION")
	fmt.Fprintln(w, "----\t-----------")
	for _, provider := range providers.Supported() {
		fmt.Fprintf(w, "%s\t%s\n", provider.Name, provider.Description)
	}
	_ = w.Flush()
}

func runListDeployments() {
	cfg := config.Load()
	ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
	store := deployments.NewStore(ws)

	items, err := store.List()
	if err != nil {
		failf("failed to list deployments: %v", err)
	}
	if len(items) == 0 {
		if outputJSON {
			printJSONOK([]interface{}{})
			return
		}
		fmt.Println("No deployments found in cloudspace notes.")
		return
	}

	liveDomains := fetchLiveProviderDomains(items, cfg)
	if outputJSON {
		out := make([]map[string]interface{}, 0, len(items))
		for _, item := range items {
			out = append(out, map[string]interface{}{
				"name":        item.Name,
				"provider":    item.Provider,
				"template":    item.TemplateName,
				"template_id": item.TemplateID,
				"vm_id":       item.VMID,
				"domains":     displayDomains(item, liveDomains[item.Name]),
				"created_at":  item.CreatedAt,
				"deployment":  item.Deployment,
				"cloudspace":  resolvedCloudspace,
			})
		}
		printJSONOK(out)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROVIDER\tTEMPLATE\tVM_ID\tDOMAINS\tCREATED_AT")
	for _, item := range items {
		fmt.Fprintf(w, "%s\t%s\t%s(%d)\t%d\t%s\t%s\n",
			item.Name,
			item.Provider,
			item.TemplateName,
			item.TemplateID,
			item.VMID,
			displayDomains(item, liveDomains[item.Name]),
			item.CreatedAt,
		)
	}
	_ = w.Flush()
}

func runListCloudspaces() {
	cfg := config.Load()
	rows := configuredCloudspaces(cfg)
	if len(rows) == 0 {
		if outputJSON {
			printJSONOK([]interface{}{})
			return
		}
		fmt.Println("No configured cloudspaces found.")
		return
	}

	canLookupName := cfg.ValidateWhiteSkyCredentials() == nil
	for i := range rows {
		rows[i].Location = decodeWhiteSkyLocation(rows[i].CloudspaceID)
		if !canLookupName {
			continue
		}
		ws := clients.NewWhiteSkyClient(clients.WhiteSkyConfig{
			BaseURL:        cfg.WhiteSky.BaseURL,
			IAMBaseURL:     cfg.WhiteSky.IAMBaseURL,
			Token:          cfg.WhiteSky.Token,
			CustomerID:     cfg.WhiteSky.CustomerID,
			CloudspaceID:   rows[i].CloudspaceID,
			RequestTimeout: cfg.WhiteSky.RequestTimeout,
		})
		info, err := ws.GetCloudspaceInfo()
		if err == nil {
			rows[i].Name = strings.TrimSpace(info.Name)
			if strings.TrimSpace(rows[i].Location) == "" {
				rows[i].Location = strings.TrimSpace(info.Location)
			}
		}
	}

	if outputJSON {
		printJSONOK(rows)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintln(w, "ALIAS\tCLOUDSPACE_ID\tNAME\tLOCATION")
	fmt.Fprintln(w, "-----\t-------------\t----\t--------")
	for _, row := range rows {
		name := row.Name
		if name == "" {
			name = "-"
		}
		location := row.Location
		if location == "" {
			location = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", row.Alias, row.CloudspaceID, name, location)
	}
	_ = w.Flush()
}

type cloudspaceRow struct {
	Alias        string `json:"alias"`
	CloudspaceID string `json:"cloudspace_id"`
	Name         string `json:"name,omitempty"`
	Location     string `json:"location,omitempty"`
}

func configuredCloudspaces(cfg config.Config) []cloudspaceRow {
	rows := make([]cloudspaceRow, 0, len(cfg.WhiteSky.Cloudspaces))
	for _, alias := range sortedConfiguredCloudspaceNames(cfg.WhiteSky.Cloudspaces) {
		id := strings.TrimSpace(cfg.WhiteSky.Cloudspaces[alias].CloudspaceID)
		if id == "" {
			continue
		}
		rows = append(rows, cloudspaceRow{
			Alias:        alias,
			CloudspaceID: id,
		})
	}
	return rows
}

func sortedConfiguredCloudspaceNames(cloudspaces map[string]config.CloudspaceConfig) []string {
	names := make([]string, 0, len(cloudspaces))
	for name := range cloudspaces {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func decodeWhiteSkyLocation(cloudspaceID string) string {
	raw := strings.TrimSpace(cloudspaceID)
	if raw == "" {
		return ""
	}

	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(raw)
	}
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(raw)
	}
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(raw)
	}
	if err != nil {
		return ""
	}

	parts := strings.SplitN(string(decoded), ":", 2)
	return strings.TrimSpace(parts[0])
}

func displayDomains(item deployments.StoredDeployment, liveDomain string) string {
	if strings.TrimSpace(liveDomain) != "" {
		return liveDomain
	}
	if strings.TrimSpace(item.Domain) == "" {
		return "-"
	}
	return strings.TrimSpace(item.Domain)
}

func runListTLDs() {
	cfg := config.Load()
	ws, _ := newWhiteSkyClient(cfg, false)

	tlds, err := ws.ListCustomerTopLevelDomains()
	if err != nil {
		failf("failed to list top-level domains: %v", err)
	}
	if len(tlds) == 0 {
		if outputJSON {
			printJSONOK([]interface{}{})
			return
		}
		fmt.Println("No top-level domains found for this customer.")
		return
	}

	vcoDomain, vcoErr := ws.GetCustomerVCOTopLevelDomain()
	if vcoErr != nil {
		failf("failed to identify VCO top-level domain: %v", vcoErr)
	}
	preferred, prefErr := ws.SelectPreferredTopLevelDomain()
	if prefErr != nil {
		preferred = ""
	}

	rows := make([]map[string]interface{}, 0, len(tlds))
	for _, item := range tlds {
		domain := strings.TrimSpace(item.Domain)
		if domain == "" {
			continue
		}
		source := "customer"
		if clients.IsSystemProvidedTopLevelDomain(domain, vcoDomain) {
			source = "system"
		}
		preferredFlag := strings.EqualFold(domain, strings.TrimSpace(preferred))
		rows = append(rows, map[string]interface{}{
			"tld":       domain,
			"valid":     item.Valid,
			"source":    source,
			"preferred": preferredFlag,
		})
	}
	if outputJSON {
		printJSONOK(rows)
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TLD\tVALID\tSOURCE\tPREFERRED")
	fmt.Fprintln(w, "---\t-----\t------\t---------")
	for _, row := range rows {
		preferredFlag := "no"
		if row["preferred"].(bool) {
			preferredFlag = "yes"
		}
		fmt.Fprintf(w, "%s\t%t\t%s\t%s\n", row["tld"], row["valid"], row["source"], preferredFlag)
	}
	_ = w.Flush()
}

func fetchLiveProviderDomains(items []deployments.StoredDeployment, cfg config.Config) map[string]string {
	out := make(map[string]string)
	providerCache := make(map[string]providers.Provider)
	for _, item := range items {
		if strings.TrimSpace(item.Provider) == "" {
			continue
		}
		provider := providerCache[item.Provider]
		if provider == nil {
			built, err := buildProvider(item.Provider, cfg)
			if err != nil || built.Validate() != nil {
				continue
			}
			provider = built
			providerCache[item.Provider] = built
		}
		domains, err := provider.LiveDomains(item.Deployment)
		if err != nil || len(domains) == 0 {
			continue
		}
		out[item.Name] = strings.Join(domains, ",")
	}
	return out
}
