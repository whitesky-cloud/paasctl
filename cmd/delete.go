package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"paasctl/internal/config"
	"paasctl/internal/deployments"
)

var (
	deleteName      string
	deletePermanent bool
)

func init() {
	deleteCmd.Flags().StringVar(&deleteName, "name", "", "Deployment name")
	deleteCmd.Flags().BoolVar(&deletePermanent, "permanent", true, "Permanently delete VM")
	_ = deleteCmd.MarkFlagRequired("name")

	rootCmd.AddCommand(deleteCmd)
}

var deleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "Delete a deployed PaaS service/app",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
		store := deployments.NewStore(ws)

		dep, err := store.FindByName(deleteName)
		if err != nil {
			failf("lookup failed: %v", err)
		}
		if !confirmDeletionName(dep.Name) {
			failf("deletion cancelled by user")
		}

		warnings := make([]string, 0)
		if strings.TrimSpace(dep.Provider) != "" {
			provider, providerErr := buildProvider(dep.Provider, cfg)
			if providerErr != nil {
				warnings = append(warnings, warnf("skipping provider cleanup: %v", providerErr))
			} else if cfgErr := provider.Validate(); cfgErr != nil {
				warnings = append(warnings, warnf("skipping %s delete (missing config: %v)", provider.Name(), cfgErr))
			} else if err := provider.DeleteService(dep.Deployment, deletePermanent); err != nil {
				warnings = append(warnings, warnf("failed to delete %s service: %v", provider.Name(), err))
			}
		}

		for _, pf := range dep.PortForwards {
			if pf.ID == "" {
				continue
			}
			if err := ws.DeletePortForward(pf.ID); err != nil {
				warnings = append(warnings, warnf("failed to delete portforward %s: %v", pf.ID, err))
			}
		}

		for _, lb := range dep.LoadBalancers {
			if lb.ID == "" {
				continue
			}
			if err := ws.DeleteLoadBalancer(lb.ID); err != nil {
				warnings = append(warnings, warnf("failed to delete load balancer %s: %v", lb.ID, err))
			}
		}

		if dep.ServerPoolID != "" && dep.ServerPoolHostID != "" {
			if err := ws.RemoveHostFromServerPool(dep.ServerPoolID, dep.ServerPoolHostID); err != nil {
				warnings = append(warnings, warnf("failed to remove server pool host %s: %v", dep.ServerPoolHostID, err))
			}
		}

		if dep.ServerPoolID != "" {
			if err := ws.DeleteServerPool(dep.ServerPoolID); err != nil {
				warnings = append(warnings, warnf("failed to delete server pool %s: %v", dep.ServerPoolID, err))
			}
		}

		if dep.VMID > 0 && strings.TrimSpace(dep.PublicIPAddress) != "" && strings.TrimSpace(dep.Domain) != "" {
			if err := ws.DeleteVMExternalNICDomain(dep.VMID, dep.PublicIPAddress, dep.Domain); err != nil {
				warnings = append(warnings, warnf("failed to delete whitesky.cloud DNS record %s: %v", dep.Domain, err))
			}
		}

		if dep.ExternalNetworkID != "" && dep.ExternalNetworkIP != "" {
			if err := ws.RemoveCloudspaceExternalNetwork(dep.ExternalNetworkID, dep.ExternalNetworkIP); err != nil {
				warnings = append(warnings, warnf("failed to remove external network ip %s (%s): %v", dep.ExternalNetworkIP, dep.ExternalNetworkID, err))
			}
		}

		if dep.VMID > 0 {
			if err := ws.DeleteVM(dep.VMID, deletePermanent); err != nil {
				failf("failed to delete VM %d: %v", dep.VMID, err)
			}
		}

		if err := store.Delete(dep.NoteID); err != nil {
			failf("failed to delete deployment note %s: %v", dep.NoteID, err)
		}

		if outputJSON {
			printJSONOK(map[string]interface{}{
				"deployment": dep.Name,
				"vm_id":      dep.VMID,
				"permanent":  deletePermanent,
				"warnings":   warnings,
				"cloudspace": resolvedCloudspace,
			})
			return
		}

		fmt.Printf("Deployment %s deleted.\n", dep.Name)
	},
}

func confirmDeletionName(expected string) bool {
	if outputJSON {
		_, _ = fmt.Fprintf(os.Stderr, "Type deployment name to confirm deletion (%s): ", expected)
	} else {
		fmt.Printf("Type deployment name to confirm deletion (%s): ", expected)
	}
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line) == expected
}
