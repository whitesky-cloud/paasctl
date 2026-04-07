package cmd

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"paasctl/internal/clients"
	"paasctl/internal/config"
	"paasctl/internal/deployments"
	"paasctl/internal/providers"
)

var (
	addDomainName      string
	addDomainTLD       string
	addDomainSubdomain string
)

func init() {
	addDomainCmd.Flags().StringVar(&addDomainName, "name", "", "Deployment name")
	addDomainCmd.Flags().StringVar(&addDomainTLD, "tld", "", "Top-level domain to use (optional)")
	addDomainCmd.Flags().StringVar(&addDomainSubdomain, "subdomain", "", "Subdomain label to use (optional)")
	addCmd.AddCommand(addDomainCmd)
}

var addDomainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Add a custom domain to an existing deployment",
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON && (strings.TrimSpace(addDomainName) == "" || strings.TrimSpace(addDomainTLD) == "" || strings.TrimSpace(addDomainSubdomain) == "") {
			failf("--json requires --name, --tld, and --subdomain for non-interactive output")
		}

		cfg := config.Load()
		ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
		store := deployments.NewStore(ws)
		dep, depErr := selectDeployment(store, addDomainName)
		if depErr != nil {
			failf("deployment selection failed: %v", depErr)
		}
		provider, providerErr := buildProvider(dep.Provider, cfg)
		if providerErr != nil {
			failf("provider setup failed: %v", providerErr)
		}
		if err := provider.Validate(); err != nil {
			failf("config error: %v", err)
		}

		tld, tldErr := selectTopLevelDomain(ws, addDomainTLD)
		if tldErr != nil {
			failf("top-level domain selection failed: %v", tldErr)
		}

		subdomain, subErr := selectSubdomain(dep.Name, addDomainSubdomain)
		if subErr != nil {
			failf("subdomain selection failed: %v", subErr)
		}
		domain := buildDomainForName(subdomain, tld)

		if dep.VMID <= 0 || strings.TrimSpace(dep.PublicIPAddress) == "" {
			failf("deployment %q missing vm id or public ip in metadata; cannot add whitesky.cloud DNS record", dep.Name)
		}
		if err := ws.AddVMExternalNICDomain(dep.VMID, dep.PublicIPAddress, domain); err != nil {
			failf("failed to add domain to whitesky.cloud DNS: %v", err)
		}

		if err := provider.AddDomain(dep.Deployment, domain); err != nil {
			failf("failed to add domain %s: %v", domain, err)
		}

		if outputJSON {
			printJSONOK(map[string]interface{}{
				"deployment": dep.Name,
				"provider":   dep.Provider,
				"domain":     domain,
				"cloudspace": resolvedCloudspace,
			})
			return
		}

		fmt.Printf("Domain added to deployment %s: %s\n", dep.Name, domain)
	},
}

func buildDomainForName(name, tld string) string {
	label := sanitizeLabel(name)
	suffix := strings.Trim(strings.ToLower(strings.TrimSpace(tld)), ".")
	if label == "" {
		label = "app"
	}
	if suffix == "" {
		return label
	}
	return label + "." + suffix
}

func sanitizeLabel(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func selectDeployment(store *deployments.Store, name string) (deployments.StoredDeployment, error) {
	if strings.TrimSpace(name) != "" {
		return store.FindByName(name)
	}

	items, err := store.List()
	if err != nil {
		return deployments.StoredDeployment{}, err
	}

	candidates := make([]deployments.StoredDeployment, 0)
	for _, item := range items {
		if strings.TrimSpace(item.Provider) == "" {
			continue
		}
		if !hasProviderServiceID(item.Deployment) {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return deployments.StoredDeployment{}, fmt.Errorf("no deployments with stored provider service id found")
	}

	sort.Slice(candidates, func(i, j int) bool {
		return strings.ToLower(candidates[i].Name) < strings.ToLower(candidates[j].Name)
	})

	fmt.Println("Select deployment:")
	for i, item := range candidates {
		fmt.Printf("%d) %s (%s)\n", i+1, item.Name, item.TemplateName)
	}
	index, err := promptSelectIndex(len(candidates), "Deployment")
	if err != nil {
		return deployments.StoredDeployment{}, err
	}
	return candidates[index], nil
}

func hasProviderServiceID(dep deployments.Deployment) bool {
	if strings.TrimSpace(dep.ProviderServiceID) != "" {
		return true
	}
	return dep.Provider == providers.ElestioName && strings.TrimSpace(dep.ElestioServerID) != ""
}

func selectTopLevelDomain(ws *clients.WhiteSkyClient, requested string) (string, error) {
	tlds, err := ws.ListCustomerTopLevelDomains()
	if err != nil {
		return "", err
	}
	valid := make([]string, 0, len(tlds))
	for _, item := range tlds {
		d := strings.ToLower(strings.Trim(strings.TrimSpace(item.Domain), "."))
		if d == "" || !item.Valid {
			continue
		}
		valid = append(valid, d)
	}
	if len(valid) == 0 {
		return "", fmt.Errorf("no valid top-level domains found")
	}
	sort.Strings(valid)
	valid = dedupeStrings(valid)

	vcoDomain, _ := ws.GetCustomerVCOTopLevelDomain()
	preferred, _ := ws.SelectPreferredTopLevelDomain()
	preferred = strings.ToLower(strings.Trim(strings.TrimSpace(preferred), "."))

	if strings.TrimSpace(requested) != "" {
		req := strings.ToLower(strings.Trim(strings.TrimSpace(requested), "."))
		for _, tld := range valid {
			if tld == req {
				return tld, nil
			}
		}
		return "", fmt.Errorf("requested tld %q is not in available valid tlds", requested)
	}

	fmt.Println("Select top-level domain:")
	for i, tld := range valid {
		source := "customer"
		if clients.IsSystemProvidedTopLevelDomain(tld, vcoDomain) {
			source = "system"
		}
		pref := ""
		if tld == preferred {
			pref = " (preferred)"
		}
		fmt.Printf("%d) %s [%s]%s\n", i+1, tld, source, pref)
	}
	index, err := promptSelectIndex(len(valid), "TLD")
	if err != nil {
		return "", err
	}
	return valid[index], nil
}

func selectSubdomain(defaultValue, requested string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		s := sanitizeLabel(requested)
		if s == "" {
			return "", fmt.Errorf("invalid subdomain %q", requested)
		}
		return s, nil
	}

	defaultSub := sanitizeLabel(defaultValue)
	if defaultSub == "" {
		defaultSub = "app"
	}

	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("Subdomain [%s]: ", defaultSub)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		raw := strings.TrimSpace(line)
		if raw == "" {
			return defaultSub, nil
		}

		s := sanitizeLabel(raw)
		if s == "" {
			fmt.Println("Invalid subdomain, please use letters, numbers, or dashes.")
			continue
		}
		if s != strings.ToLower(raw) {
			fmt.Printf("Using sanitized subdomain: %s\n", s)
		}
		return s, nil
	}
}

func promptSelectIndex(max int, label string) (int, error) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Printf("%s number [1-%d]: ", label, max)
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(line))
		if convErr != nil || n < 1 || n > max {
			fmt.Println("Invalid selection.")
			continue
		}
		return n - 1, nil
	}
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
