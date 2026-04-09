package cmd

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"paasctl/internal/clients"
	"paasctl/internal/config"
	"paasctl/internal/deployments"
	"paasctl/internal/runtime"
)

var (
	deployProvider         string
	deployName             string
	deployTemplateID       int
	deployVCPUs            int
	deployMemoryMiB        int
	deployImageID          int
	deployDiskSizeGiB      int
	deploySSHPortPublic    int
	deployBootstrapCommand string
	deployPortsCSV         string
	deployInfraTimeout     time.Duration
	deployProviderTimeout  time.Duration
	noPlanApproval         bool
)

func init() {
	deployCmd.Flags().StringVar(&deployProvider, "provider", "", "PaaS provider to use")
	deployCmd.Flags().StringVar(&deployName, "name", "", "Deployment name")
	deployCmd.Flags().IntVar(&deployTemplateID, "template-id", 0, "Provider template ID")
	deployCmd.Flags().IntVar(&deployVCPUs, "vcpus", 2, "VM vCPUs")
	deployCmd.Flags().IntVar(&deployMemoryMiB, "memory", 4096, "VM memory in MiB")
	deployCmd.Flags().IntVar(&deployImageID, "image-id", 0, "whitesky.cloud image ID (optional)")
	deployCmd.Flags().IntVar(&deployDiskSizeGiB, "disk-size", 40, "VM boot disk size in GiB")
	deployCmd.Flags().IntVar(&deploySSHPortPublic, "ssh-public-port", 2222, "Public port mapped to VM SSH (local 22)")
	deployCmd.Flags().StringVar(&deployBootstrapCommand, "bootstrap-command", "", "Override bootstrap command if template response has no command")
	deployCmd.Flags().StringVar(&deployPortsCSV, "ports", "", "Additional app ports to expose (comma-separated)")
	deployCmd.Flags().DurationVar(&deployInfraTimeout, "timeout", 10*time.Minute, "Timeout for infrastructure readiness such as SSH and bootstrap")
	deployCmd.Flags().DurationVar(&deployProviderTimeout, "provider-timeout", 6*time.Hour, "Timeout for provider-side application deployment readiness")
	deployCmd.Flags().BoolVar(&noPlanApproval, "no-plan-approval", false, "Skip interactive plan approval prompt")

	_ = deployCmd.MarkFlagRequired("name")
	_ = deployCmd.MarkFlagRequired("provider")
	_ = deployCmd.MarkFlagRequired("template-id")

	rootCmd.AddCommand(deployCmd)
}

var deployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "Deploy a PaaS service/app into a whitesky.cloud VM",
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON && !noPlanApproval {
			failf("--json requires --no-plan-approval for non-interactive output")
		}

		cfg := config.Load()
		provider, err := buildProvider(deployProvider, cfg)
		if err != nil {
			failf("config error: %v", err)
		}
		if err := provider.Validate(); err != nil {
			failf("config error: %v", err)
		}

		ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
		store := deployments.NewStore(ws)
		deployer := runtime.Deployer{
			WhiteSky: ws,
			Provider: provider,
			Store:    store,
		}

		if !outputJSON {
			fmt.Println("[plan] Building deployment plan...")
		}
		plan, err := deployer.BuildPlan(runtime.DeployOptions{
			Name:             deployName,
			TemplateID:       deployTemplateID,
			VCPUs:            deployVCPUs,
			MemoryMiB:        deployMemoryMiB,
			ImageID:          deployImageID,
			DiskSizeGiB:      deployDiskSizeGiB,
			SSHPortPublic:    deploySSHPortPublic,
			BootstrapCommand: deployBootstrapCommand,
			AdditionalPorts:  parsePortsCSV(deployPortsCSV),
			InfraTimeout:     deployInfraTimeout,
			ProviderTimeout:  deployProviderTimeout,
		}, func(message string) {
			if !outputJSON {
				fmt.Printf("[plan] %s\n", message)
			}
		})
		if err != nil {
			failf("failed to build deploy plan: %v", err)
		}
		if !outputJSON {
			printPlan(plan)
		}

		if !noPlanApproval && !confirmPlanApproval() {
			failf("deployment cancelled by user")
		}

		stepNum := 0
		dep, err := deployer.Deploy(plan, func(message string) {
			stepNum++
			if !outputJSON {
				fmt.Printf("[%d] %s...\n", stepNum, message)
			}
		})
		if err != nil {
			failf("deploy failed: %v", err)
		}
		if outputJSON {
			printJSONOK(map[string]interface{}{
				"deployment": dep,
				"plan":       plan,
				"steps":      stepNum,
				"cloudspace": resolvedCloudspace,
			})
			return
		}
		fmt.Printf("[%d] Deployment completed.\n", stepNum+1)

		fmt.Printf("Deployment created: %s\n", dep.Name)
		fmt.Printf("VM ID: %d\n", dep.VMID)
		for _, pf := range dep.PortForwards {
			fmt.Printf("Port forward: %s local %d -> public %d (%s)\n", pf.ID, pf.LocalPort, pf.PublicPort, pf.Protocol)
		}
		if dep.Domain != "" {
			fmt.Printf("Domain: %s\n", dep.Domain)
		}
		if note := deploymentNextStepNote(cfg, dep); note != "" {
			fmt.Println("")
			fmt.Println(note)
		}
	},
}

func parsePortsCSV(value string) []int {
	parts := strings.Split(value, ",")
	out := make([]int, 0, len(parts))
	for _, raw := range parts {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p, err := strconv.Atoi(raw)
		if err == nil && p > 0 {
			out = append(out, p)
		}
	}
	return out
}

func printPlan(plan runtime.DeployPlan) {
	fmt.Println("Deployment plan:")
	w := tabwriter.NewWriter(os.Stdout, 2, 2, 2, ' ', 0)
	fmt.Fprintf(w, "Name:\t%s\n", plan.Name)
	fmt.Fprintf(w, "Provider:\t%s\n", plan.Provider)
	fmt.Fprintf(w, "Template:\t%s (%d) version %s\n", plan.TemplateName, plan.TemplateID, plan.TemplateVersion)
	if plan.ImageName != "" || plan.ImageOSName != "" || plan.Location != "" {
		fmt.Fprintf(w, "Image:\t%s [id=%d, os=%s, location=%s]\n", plan.ImageName, plan.ImageID, plan.ImageOSName, plan.Location)
	} else {
		fmt.Fprintf(w, "Image:\tID %d (provided via flag)\n", plan.ImageID)
	}
	fmt.Fprintf(w, "VM resources:\t%d vCPU, %d MiB RAM, %d GiB disk\n", plan.VCPUs, plan.MemoryMiB, plan.DiskSizeGiB)
	fmt.Fprintf(w, "Public bind IP:\t%s\n", plan.PublicIPAddress)
	if plan.ExternalNetworkID != "" && plan.ExternalNetworkIP != "" {
		fmt.Fprintf(w, "Extra external IP:\t%s (network id=%s, type=%s)\n", plan.ExternalNetworkIP, plan.ExternalNetworkID, plan.ExternalNetworkType)
	}
	fmt.Fprintf(w, "Port mappings:\t%s\n", joinMappings(plan.PortMappings))
	fmt.Fprintf(w, "Bootstrap:\t%s\n", plan.BootstrapCommand)
	fmt.Fprintf(w, "Infra timeout:\t%s\n", plan.InfraTimeout)
	fmt.Fprintf(w, "Provider timeout:\t%s\n", plan.ProviderTimeout)
	_ = w.Flush()
	fmt.Println("Planned steps:")
	fmt.Println("1. Create VM")
	if plan.ExternalNetworkID != "" && plan.ExternalNetworkIP != "" {
		fmt.Println("2. Add extra cloudspace external network IP")
		fmt.Println("3. Create port forwards for required ports")
		fmt.Println("4. Wait for SSH through port forward")
		fmt.Printf("5. Authorize %s SSH access (bootstrap command)\n", plan.Provider)
		fmt.Printf("6. Trigger %s app/service deployment\n", plan.Provider)
		fmt.Printf("7. Add custom domain to %s service\n", plan.Provider)
		fmt.Println("8. Save deployment metadata note")
		fmt.Println("9. Rollback all created resources if any step fails")
		return
	}
	fmt.Println("2. Create port forwards for required ports")
	fmt.Println("3. Wait for SSH through port forward")
	fmt.Printf("4. Authorize %s SSH access (bootstrap command)\n", plan.Provider)
	fmt.Printf("5. Trigger %s app/service deployment\n", plan.Provider)
	fmt.Printf("6. Add custom domain to %s service\n", plan.Provider)
	fmt.Println("7. Save deployment metadata note")
	fmt.Println("8. Rollback all created resources if any step fails")
}

func confirmPlanApproval() bool {
	fmt.Print("Proceed with this plan? [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func joinMappings(mappings []runtime.PortMapping) string {
	if len(mappings) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, len(mappings))
	for _, m := range mappings {
		parts = append(parts, fmt.Sprintf("%d->%d", m.LocalPort, m.PublicPort))
	}
	return strings.Join(parts, ", ")
}

func deploymentNextStepNote(cfg config.Config, dep deployments.Deployment) string {
	if dep.Provider != "elestio" {
		return ""
	}
	link := elestioServicesDashboardURL(cfg, dep)
	if link == "" {
		return ""
	}
	return "Note: Open Elestio to find connection information and other details for the freshly deployed application:\n" + link + "\nOpen the newly created service there to start using it."
}

func elestioServicesDashboardURL(cfg config.Config, dep deployments.Deployment) string {
	projectID := strings.TrimSpace(dep.ProviderProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(dep.ElestioProjectID)
	}
	if projectID == "" {
		return ""
	}

	projectName := "default-project"
	client := clients.NewElestioClient(clients.ElestioConfig{BaseURL: cfg.Elestio.BaseURL})
	if jwt, err := client.GetJWT(cfg.Elestio.Email, cfg.Elestio.APIToken); err == nil {
		if projects, err := client.ListProjects(jwt); err == nil {
			for _, project := range projects {
				if strings.TrimSpace(project.ProjectID) == projectID && strings.TrimSpace(project.ProjectName) != "" {
					projectName = strings.TrimSpace(project.ProjectName)
					break
				}
			}
		}
	}

	dashBase := deriveElestioDashboardBaseURL(cfg.Elestio.BaseURL)
	if dashBase == "" {
		return ""
	}
	return strings.TrimRight(dashBase, "/") + "/" + url.PathEscape(projectID) + "/" + url.PathEscape(projectName) + "/services"
}

func deriveElestioDashboardBaseURL(apiBase string) string {
	raw := strings.TrimSpace(apiBase)
	if raw == "" {
		return "https://dash.elest.io"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "https://dash.elest.io"
	}
	switch {
	case strings.HasPrefix(u.Host, "api."):
		u.Host = "dash." + strings.TrimPrefix(u.Host, "api.")
	case u.Host == "api.elest.io":
		u.Host = "dash.elest.io"
	default:
		u.Host = "dash.elest.io"
	}
	u.Path = ""
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
