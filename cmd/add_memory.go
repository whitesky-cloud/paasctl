package cmd

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"paasctl/internal/config"
	"paasctl/internal/deployments"
)

var addMemoryName string

func init() {
	addMemoryCmd.Flags().StringVar(&addMemoryName, "name", "", "Deployment name")
	addCmd.AddCommand(addMemoryCmd)
}

var addMemoryCmd = &cobra.Command{
	Use:   "memory <size>",
	Short: "Increase VM memory for an existing deployment",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON && strings.TrimSpace(addMemoryName) == "" {
			failf("--json requires --name for non-interactive output")
		}

		incrementMiB, err := parseMemoryIncrementMiB(args[0])
		if err != nil {
			failf("invalid memory increment: %v", err)
		}

		cfg := config.Load()
		ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
		store := deployments.NewStore(ws)
		dep, depErr := selectDeploymentForStorage(store, addMemoryName)
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
		if vm.MemoryMiB <= 0 {
			failf("vm %d returned invalid current memory: %d MiB", dep.VMID, vm.MemoryMiB)
		}

		newMemoryMiB := vm.MemoryMiB + incrementMiB
		if !outputJSON {
			fmt.Printf("Resizing VM %d memory from %d MiB to %d MiB...\n", dep.VMID, vm.MemoryMiB, newMemoryMiB)
		}
		if err := ws.ResizeVMMemory(dep.VMID, newMemoryMiB); err != nil {
			failf("failed to resize VM memory: %v", err)
		}

		if outputJSON {
			printJSONOK(map[string]interface{}{
				"deployment":     dep.Name,
				"vm_id":          dep.VMID,
				"increment_mib":  incrementMiB,
				"old_memory_mib": vm.MemoryMiB,
				"new_memory_mib": newMemoryMiB,
				"cloudspace":     resolvedCloudspace,
			})
			return
		}

		fmt.Printf("Memory expansion completed for deployment %s (+%d MiB).\n", dep.Name, incrementMiB)
	},
}

func parseMemoryIncrementMiB(value string) (int, error) {
	raw := strings.TrimSpace(value)
	raw = strings.TrimPrefix(raw, "+")
	if raw == "" {
		return 0, fmt.Errorf("value is empty")
	}

	re := regexp.MustCompile(`(?i)^([0-9]+)\s*([a-z]*)$`)
	m := re.FindStringSubmatch(raw)
	if len(m) != 3 {
		return 0, fmt.Errorf("expected format like 512m, 1g, or 1024MiB")
	}

	amount, err := strconv.Atoi(m[1])
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("invalid numeric value %q", m[1])
	}

	unit := strings.ToLower(strings.TrimSpace(m[2]))
	switch unit {
	case "", "m", "mb", "mib":
		return amount, nil
	case "g", "gb", "gib":
		if amount > (1<<31-1)/1024 {
			return 0, fmt.Errorf("value too large")
		}
		return amount * 1024, nil
	case "t", "tb", "tib":
		if amount > (1<<31-1)/(1024*1024) {
			return 0, fmt.Errorf("value too large")
		}
		return amount * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unsupported unit %q", unit)
	}
}
