package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"paasctl/internal/config"
	"paasctl/internal/deployments"
)

var addVCPUsName string

func init() {
	addVCPUsCmd.Flags().StringVar(&addVCPUsName, "name", "", "Deployment name")
	addCmd.AddCommand(addVCPUsCmd)
}

var addVCPUsCmd = &cobra.Command{
	Use:   "vcpus <count>",
	Short: "Increase VM vCPUs for an existing deployment",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON && strings.TrimSpace(addVCPUsName) == "" {
			failf("--json requires --name for non-interactive output")
		}

		incrementVCPUs, err := parseVCPUsIncrement(args[0])
		if err != nil {
			failf("invalid vcpus increment: %v", err)
		}

		cfg := config.Load()
		ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
		store := deployments.NewStore(ws)
		dep, depErr := selectDeploymentForStorage(store, addVCPUsName)
		if depErr != nil {
			failf("deployment selection failed: %v", depErr)
		}
		if dep.VMID <= 0 {
			failf("deployment %q has no vm id in metadata", dep.Name)
		}

		vm, vmErr := ws.GetVM(dep.VMID)
		if vmErr != nil {
			failf("failed to read vm info: %v", vmErr)
		}
		if vm.VCPUs <= 0 {
			failf("vm %d returned invalid current vcpus: %d", dep.VMID, vm.VCPUs)
		}

		newVCPUs := vm.VCPUs + incrementVCPUs
		if !outputJSON {
			fmt.Printf("Resizing VM %d vCPUs from %d to %d...\n", dep.VMID, vm.VCPUs, newVCPUs)
		}
		if err := ws.ResizeVMVCPUs(dep.VMID, newVCPUs); err != nil {
			failf("failed to resize VM vcpus: %v", err)
		}

		if outputJSON {
			printJSONOK(map[string]interface{}{
				"deployment":      dep.Name,
				"vm_id":           dep.VMID,
				"increment_vcpus": incrementVCPUs,
				"old_vcpus":       vm.VCPUs,
				"new_vcpus":       newVCPUs,
				"cloudspace":      resolvedCloudspace,
			})
			return
		}

		fmt.Printf("vCPU expansion completed for deployment %s (+%d).\n", dep.Name, incrementVCPUs)
	},
}

func parseVCPUsIncrement(value string) (int, error) {
	raw := strings.TrimSpace(value)
	raw = strings.TrimPrefix(raw, "+")
	if raw == "" {
		return 0, fmt.Errorf("value is empty")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("expected positive integer, got %q", value)
	}
	return n, nil
}
