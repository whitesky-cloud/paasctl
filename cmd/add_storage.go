package cmd

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"paasctl/internal/clients"
	"paasctl/internal/config"
	"paasctl/internal/deployments"
)

const addStorageAgentReadyTimeout = 10 * time.Minute
const addStorageScriptPath = "/tmp/paasctl-storage-expand.sh"
const addStorageScriptLogPath = "/tmp/paasctl-storage-expand.sh.log"

var addStorageName string

func init() {
	addStorageCmd.Flags().StringVar(&addStorageName, "name", "", "Deployment name")
	addCmd.AddCommand(addStorageCmd)
}

var addStorageCmd = &cobra.Command{
	Use:   "storage <size>",
	Short: "Expand VM storage for an existing deployment",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		if outputJSON && strings.TrimSpace(addStorageName) == "" {
			failf("--json requires --name for non-interactive output")
		}

		incrementGiB, err := parseStorageIncrementGiB(args[0])
		if err != nil {
			failf("invalid storage increment: %v", err)
		}

		cfg := config.Load()
		ws, resolvedCloudspace := newWhiteSkyClient(cfg, true)
		store := deployments.NewStore(ws)
		dep, depErr := selectDeploymentForStorage(store, addStorageName)
		if depErr != nil {
			failf("deployment selection failed: %v", depErr)
		}
		if dep.VMID <= 0 {
			failf("deployment %q has no vm id in metadata", dep.Name)
		}

		cloudspace, csErr := ws.GetCloudspaceInfo()
		if csErr != nil {
			failf("failed to read cloudspace info: %v", csErr)
		}
		if strings.TrimSpace(cloudspace.Location) == "" {
			failf("cloudspace location is empty")
		}

		disks, diskErr := ws.ListVMDisks(dep.VMID)
		if diskErr != nil {
			failf("failed to list vm disks: %v", diskErr)
		}
		targetDisk, pickErr := pickBootDisk(disks)
		if pickErr != nil {
			failf("failed to identify target disk: %v", pickErr)
		}
		if targetDisk.DiskSize <= 0 {
			failf("selected disk has invalid size: %d", targetDisk.DiskSize)
		}

		newSizeGiB := targetDisk.DiskSize + incrementGiB
		if !outputJSON {
			fmt.Printf("Resizing VM %d disk %d from %d GiB to %d GiB...\n", dep.VMID, targetDisk.DiskID, targetDisk.DiskSize, newSizeGiB)
		}
		if err := ws.ResizeDisk(cloudspace.Location, targetDisk.DiskID, newSizeGiB); err != nil {
			failf("failed to resize disk: %v", err)
		}

		if !outputJSON {
			fmt.Printf("Waiting for VM QEMU agent to become ready (up to %s)...\n", addStorageAgentReadyTimeout)
		}
		output, err := waitForVMAgentAndUploadAndRun(ws, dep.VMID, addStorageScriptPath, vmStorageExpandScript(), addStorageAgentReadyTimeout)
		if err != nil {
			if strings.TrimSpace(output) != "" && !outputJSON {
				fmt.Println("Resize script output:")
				fmt.Println(decodeExecOutput(output))
			}
			failf("disk resized but failed to expand guest filesystem/lvm: %v", err)
		}

		if outputJSON {
			printJSONOK(map[string]interface{}{
				"deployment":    dep.Name,
				"vm_id":         dep.VMID,
				"disk_id":       targetDisk.DiskID,
				"increment_gib": incrementGiB,
				"old_size_gib":  targetDisk.DiskSize,
				"new_size_gib":  newSizeGiB,
				"cloudspace":    resolvedCloudspace,
			})
			return
		}

		fmt.Printf("Storage expansion completed for deployment %s (+%d GiB).\n", dep.Name, incrementGiB)
	},
}

func parseStorageIncrementGiB(value string) (int, error) {
	raw := strings.TrimSpace(value)
	raw = strings.TrimPrefix(raw, "+")
	if raw == "" {
		return 0, fmt.Errorf("value is empty")
	}

	re := regexp.MustCompile(`(?i)^([0-9]+)\s*([a-z]*)$`)
	m := re.FindStringSubmatch(raw)
	if len(m) != 3 {
		return 0, fmt.Errorf("expected format like 30G, 30GiB, or 30720M")
	}

	amount, err := strconv.Atoi(m[1])
	if err != nil || amount <= 0 {
		return 0, fmt.Errorf("invalid numeric value %q", m[1])
	}

	unit := strings.ToLower(strings.TrimSpace(m[2]))
	switch unit {
	case "", "g", "gb", "gib":
		return amount, nil
	case "m", "mb", "mib":
		return (amount + 1023) / 1024, nil
	case "t", "tb", "tib":
		if amount > (1<<31-1)/1024 {
			return 0, fmt.Errorf("value too large")
		}
		return amount * 1024, nil
	default:
		return 0, fmt.Errorf("unsupported unit %q", unit)
	}
}

func pickBootDisk(disks []clients.VMDisk) (clients.VMDisk, error) {
	if len(disks) == 0 {
		return clients.VMDisk{}, fmt.Errorf("vm has no disks")
	}

	candidates := make([]clients.VMDisk, 0, len(disks))
	for _, disk := range disks {
		if disk.DiskID <= 0 {
			continue
		}
		candidates = append(candidates, disk)
	}
	if len(candidates) == 0 {
		return clients.VMDisk{}, fmt.Errorf("vm disk list did not include valid disk ids")
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		ai := parseDiskOrder(candidates[i].Order)
		aj := parseDiskOrder(candidates[j].Order)
		if ai != aj {
			return ai < aj
		}
		if candidates[i].PciBus != candidates[j].PciBus {
			return candidates[i].PciBus < candidates[j].PciBus
		}
		if candidates[i].PciSlot != candidates[j].PciSlot {
			return candidates[i].PciSlot < candidates[j].PciSlot
		}
		return candidates[i].DiskID < candidates[j].DiskID
	})

	return candidates[0], nil
}

func parseDiskOrder(order string) int {
	v, err := strconv.Atoi(strings.TrimSpace(order))
	if err != nil {
		return 1<<30 - 1
	}
	return v
}

func selectDeploymentForStorage(store *deployments.Store, name string) (deployments.StoredDeployment, error) {
	if strings.TrimSpace(name) != "" {
		return store.FindByName(name)
	}

	items, err := store.List()
	if err != nil {
		return deployments.StoredDeployment{}, err
	}

	candidates := make([]deployments.StoredDeployment, 0, len(items))
	for _, item := range items {
		if item.VMID <= 0 {
			continue
		}
		candidates = append(candidates, item)
	}
	if len(candidates) == 0 {
		return deployments.StoredDeployment{}, fmt.Errorf("no deployments with vm id found")
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

func waitForVMAgentAndUploadAndRun(ws *clients.WhiteSkyClient, vmID int, scriptPath, scriptContent string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		err := ws.WriteVMFile(vmID, scriptPath, []byte(scriptContent+"\n"), false)
		if err != nil {
			if !isVMAgentNotRunningError(err) {
				return "", fmt.Errorf("failed to upload resize script: %w", err)
			}
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}

		if _, err := ws.ExecCommand(vmID, fmt.Sprintf("chmod 700 %s", scriptPath)); err != nil {
			if !isVMAgentNotRunningError(err) {
				return "", fmt.Errorf("failed to chmod resize script: %w", err)
			}
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}

		_, err = ws.ExecCommand(vmID, fmt.Sprintf("/bin/sh %s", scriptPath))
		logData, _ := ws.ReadVMFile(vmID, addStorageScriptLogPath, 2*1024*1024, 0)
		out := decodeExecOutput(logData)
		if err == nil {
			_ = ws.DeleteVMFile(vmID, scriptPath)
			_ = ws.DeleteVMFile(vmID, addStorageScriptLogPath)
			return out, nil
		}
		if !isVMAgentNotRunningError(err) {
			if strings.TrimSpace(out) == "" {
				return "", err
			}
			return out, fmt.Errorf("%w; script output: %s", err, strings.TrimSpace(out))
		}
		lastErr = err
		time.Sleep(5 * time.Second)
	}

	if lastErr != nil {
		return "", fmt.Errorf("timeout after %s waiting for VM agent: %w", timeout, lastErr)
	}
	return "", fmt.Errorf("timeout after %s waiting for VM agent", timeout)
}

func isVMAgentNotRunningError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "vm agent must be enabled and running") || strings.Contains(msg, "agent status: not_running")
}

func vmStorageExpandScript() string {
	return strings.TrimSpace(`
LOG_FILE="` + addStorageScriptLogPath + `"
exec >"$LOG_FILE" 2>&1

set -eu
set -x

ROOT_SRC="$(findmnt -n -o SOURCE / 2>/dev/null || true)"
if [ -z "$ROOT_SRC" ]; then
  echo "failed: could not detect root source" >&2
  exit 1
fi

ROOT_REAL="$(readlink -f "$ROOT_SRC" 2>/dev/null || echo "$ROOT_SRC")"
ROOT_FS="$(findmnt -n -o FSTYPE / 2>/dev/null || true)"

echo "=== initial state ==="
findmnt /
lsblk
if command -v pvs >/dev/null 2>&1; then pvs; fi
if command -v vgs >/dev/null 2>&1; then vgs; fi
if command -v lvs >/dev/null 2>&1; then lvs -a -o +devices; fi

ROOT_LV=""
TARGET_BLOCK="$ROOT_REAL"

ROOT_TYPE="$(lsblk -no TYPE "$ROOT_REAL" 2>/dev/null | head -n1 | tr -d ' ')"
if [ "$ROOT_TYPE" = "lvm" ]; then
  ROOT_LV="$ROOT_SRC"
fi

if [ -z "$ROOT_LV" ] && command -v lvs >/dev/null 2>&1 && lvs "$ROOT_SRC" >/dev/null 2>&1; then
  ROOT_LV="$ROOT_SRC"
fi

if [ -z "$ROOT_LV" ] && command -v lvs >/dev/null 2>&1 && lvs "$ROOT_REAL" >/dev/null 2>&1; then
  ROOT_LV="$ROOT_REAL"
fi

if [ -n "$ROOT_LV" ]; then
  VG_NAME="$(lvs --noheadings -o vg_name "$ROOT_LV" 2>/dev/null | head -n1 | xargs || true)"
  if [ -n "$VG_NAME" ] && command -v pvs >/dev/null 2>&1; then
    PV_DEV="$(pvs --noheadings -o pv_name --select "vg_name=$VG_NAME" 2>/dev/null | head -n1 | xargs || true)"
    if [ -n "$PV_DEV" ]; then
      TARGET_BLOCK="$PV_DEV"
    else
      echo "failed: could not resolve PV device for VG $VG_NAME" >&2
      exit 1
    fi
  else
    echo "failed: could not resolve VG name for root LV $ROOT_LV" >&2
    exit 1
  fi
fi

PART_DEV="$TARGET_BLOCK"
PART_BASENAME="$(basename "$PART_DEV")"
PART_NUM="$(lsblk -no PARTNUM "$PART_DEV" 2>/dev/null | head -n1 | tr -d ' ')"
if [ -z "$PART_NUM" ]; then
  PART_NUM="$(echo "$PART_BASENAME" | sed -nE 's/.*[^0-9]([0-9]+)$/\1/p')"
fi

DISK_DEV=""
if echo "$PART_BASENAME" | grep -Eq '^nvme[0-9]+n[0-9]+p[0-9]+$'; then
  DISK_DEV="/dev/$(echo "$PART_BASENAME" | sed -E 's/p[0-9]+$//')"
elif echo "$PART_BASENAME" | grep -Eq '^[a-z]+[0-9]+$'; then
  DISK_DEV="/dev/$(echo "$PART_BASENAME" | sed -E 's/[0-9]+$//')"
else
  DISK_PARENT="$(lsblk -no PKNAME "$PART_DEV" 2>/dev/null | head -n1 | tr -d ' ')"
  if [ -n "$DISK_PARENT" ] && [ "$DISK_PARENT" != "$PART_BASENAME" ]; then
    DISK_DEV="/dev/$DISK_PARENT"
  fi
fi

if [ -z "$DISK_DEV" ]; then
  DISK_DEV="$PART_DEV"
fi

if [ -n "$PART_NUM" ] && [ "$DISK_DEV" != "$PART_DEV" ]; then
  PART_SIZE_BEFORE="$(lsblk -bn -o SIZE "$PART_DEV" 2>/dev/null | head -n1 | tr -d ' ')"
  DISK_SIZE_BEFORE="$(lsblk -bn -o SIZE "$DISK_DEV" 2>/dev/null | head -n1 | tr -d ' ')"
  echo "Before growpart: disk=$DISK_DEV size=$DISK_SIZE_BEFORE bytes, partition=$PART_DEV size=$PART_SIZE_BEFORE bytes"

  if command -v growpart >/dev/null 2>&1; then
    growpart "$DISK_DEV" "$PART_NUM"
  elif command -v parted >/dev/null 2>&1; then
    parted -s "$DISK_DEV" "resizepart $PART_NUM 100%"
  else
    echo "failed: neither growpart nor parted is installed" >&2
    exit 1
  fi

  DISK_BASENAME="$(basename "$DISK_DEV")"
  PART_SIZE_AFTER="$PART_SIZE_BEFORE"
  ATTEMPT=1
  while [ "$ATTEMPT" -le 15 ]; do
    if command -v partprobe >/dev/null 2>&1; then
      partprobe "$DISK_DEV" || true
    fi
    if command -v partx >/dev/null 2>&1; then
      partx -u "$DISK_DEV" || true
    fi
    if command -v blockdev >/dev/null 2>&1; then
      blockdev --rereadpt "$DISK_DEV" || true
    fi
    if [ -w "/sys/class/block/$DISK_BASENAME/device/rescan" ]; then
      echo 1 >"/sys/class/block/$DISK_BASENAME/device/rescan" || true
    fi
    if command -v udevadm >/dev/null 2>&1; then
      udevadm settle || true
    fi

    PART_SIZE_AFTER="$(lsblk -bn -o SIZE "$PART_DEV" 2>/dev/null | head -n1 | tr -d ' ')"
    DISK_SIZE_AFTER="$(lsblk -bn -o SIZE "$DISK_DEV" 2>/dev/null | head -n1 | tr -d ' ')"
    echo "After growpart attempt $ATTEMPT: disk=$DISK_DEV size=$DISK_SIZE_AFTER bytes, partition=$PART_DEV size=$PART_SIZE_AFTER bytes"

    if [ -n "$PART_SIZE_BEFORE" ] && [ -n "$PART_SIZE_AFTER" ] && [ "$PART_SIZE_AFTER" -gt "$PART_SIZE_BEFORE" ]; then
      break
    fi
    ATTEMPT=$((ATTEMPT + 1))
    sleep 2
  done

  if [ -n "$PART_SIZE_BEFORE" ] && [ -n "$PART_SIZE_AFTER" ] && [ "$PART_SIZE_AFTER" -le "$PART_SIZE_BEFORE" ]; then
    echo "warning: partition size did not appear to grow after retries (before=$PART_SIZE_BEFORE after=$PART_SIZE_AFTER); continuing with pvresize/lvextend checks" >&2
    echo "debug: /proc/partitions"
    cat /proc/partitions || true
  fi
fi

FS_TARGET="$ROOT_REAL"
if [ -n "$ROOT_LV" ]; then
  PV_SIZE_BEFORE="$(pvs --noheadings -o pv_size "$PART_DEV" 2>/dev/null | head -n1 | xargs || true)"
  LV_SIZE_BEFORE="$(lvs --noheadings -o lv_size "$ROOT_LV" 2>/dev/null | head -n1 | xargs || true)"
  echo "Before LVM resize: pv=$PART_DEV size=$PV_SIZE_BEFORE, lv=$ROOT_LV size=$LV_SIZE_BEFORE"

  if ! command -v pvresize >/dev/null 2>&1; then
    echo "failed: pvresize is required for LVM root filesystem" >&2
    exit 1
  fi
  if ! command -v lvextend >/dev/null 2>&1; then
    echo "failed: lvextend is required for LVM root filesystem" >&2
    exit 1
  fi
  pvresize "$PART_DEV"
  lvextend -l +100%FREE "$ROOT_LV"

  PV_SIZE_AFTER="$(pvs --noheadings -o pv_size "$PART_DEV" 2>/dev/null | head -n1 | xargs || true)"
  LV_SIZE_AFTER="$(lvs --noheadings -o lv_size "$ROOT_LV" 2>/dev/null | head -n1 | xargs || true)"
  echo "After LVM resize: pv=$PART_DEV size=$PV_SIZE_AFTER, lv=$ROOT_LV size=$LV_SIZE_AFTER"
  if [ -n "$LV_SIZE_BEFORE" ] && [ -n "$LV_SIZE_AFTER" ] && [ "$LV_SIZE_BEFORE" = "$LV_SIZE_AFTER" ]; then
    echo "failed: LV size did not change after lvextend (before=$LV_SIZE_BEFORE after=$LV_SIZE_AFTER)" >&2
    exit 1
  fi

  FS_TARGET="$ROOT_LV"
fi

case "$ROOT_FS" in
  ext2|ext3|ext4)
    if ! command -v resize2fs >/dev/null 2>&1; then
      echo "failed: resize2fs is required for $ROOT_FS filesystem" >&2
      exit 1
    fi
    resize2fs "$FS_TARGET"
    ;;
  xfs)
    if ! command -v xfs_growfs >/dev/null 2>&1; then
      echo "failed: xfs_growfs is required for xfs filesystem" >&2
      exit 1
    fi
    xfs_growfs /
    ;;
  btrfs)
    if ! command -v btrfs >/dev/null 2>&1; then
      echo "failed: btrfs command is required for btrfs filesystem" >&2
      exit 1
    fi
    btrfs filesystem resize max /
    ;;
  *)
    echo "failed: unsupported root filesystem type: $ROOT_FS" >&2
    exit 1
    ;;
esac

echo "=== final state ==="
lsblk
if command -v pvs >/dev/null 2>&1; then pvs; fi
if command -v vgs >/dev/null 2>&1; then vgs; fi
if command -v lvs >/dev/null 2>&1; then lvs -a -o +devices; fi
echo "Root filesystem after expansion:"
df -h /
`)
}

func decodeExecOutput(value string) string {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return raw
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return value
	}
	text := string(decoded)
	if strings.TrimSpace(text) == "" {
		return value
	}
	return text
}
