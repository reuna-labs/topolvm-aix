//go:build aix

// device_class_manager_aix.go – AIX-specific validation additions.
//
// On AIX:
//   - Thin device classes are refused at startup (note 1.3): AIX LVM has no
//     thin provisioning.
//   - Original volume groups are refused (note 4.1): they place the LVCB in
//     the first block of each LV, which a database writing from offset 0 will
//     overwrite, corrupting the TopoLVM name record.  Scalable VGs (mkvg -S)
//     and big VGs (mkvg -B, or big VG with mklv -T O) store the LVCB in the
//     VGDA so LV data starts at offset 0.
//
// ValidateDeviceClassesAIX supplements ValidateDeviceClasses and is called
// from cmd/lvmd/app/root.go on AIX.
//
// VGSubtype is detected by `lsvg <vg>` and checking the VG TYPE field:
//   - "scalable" → OK
//   - "big"      → OK  (LVCB in VGDA when mklv -T O is used; we recommend it)
//   - "original" → refused
//
// [check] The exact VG TYPE strings need to be verified on a real AIX 7.3 box
// and captured as test fixtures (note 2.6 style).

package lvmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/topolvm/topolvm/internal/lvmd/command"
	lvmdTypes "github.com/topolvm/topolvm/pkg/lvmd/types"
	"os/exec"
)

// ValidateDeviceClassesAIX runs AIX-specific validation on top of the
// platform-neutral ValidateDeviceClasses.
func ValidateDeviceClassesAIX(ctx context.Context, deviceClasses []*lvmdTypes.DeviceClass) error {
	for _, dc := range deviceClasses {
		// Thin device classes are not supported on AIX (note 1.3).
		if dc.Type == lvmdTypes.TypeThin {
			return fmt.Errorf("device-class %q: thin provisioning is not supported on AIX; "+
				"remove the 'type: thin' setting and the thinpool config", dc.Name)
		}

		// Original volume groups are refused (note 4.1).
		if err := checkVGSubtype(ctx, dc.VolumeGroup); err != nil {
			return fmt.Errorf("device-class %q, volume group %q: %w", dc.Name, dc.VolumeGroup, err)
		}
	}
	return nil
}

// checkVGSubtype returns an error if the named VG is an "original" type VG.
// Scalable and big VGs are accepted.
func checkVGSubtype(ctx context.Context, vgName string) error {
	vgType, err := getVGType(ctx, vgName)
	if err != nil {
		return err
	}
	switch strings.ToLower(vgType) {
	case "scalable", "big", "":
		// "" means the field was not found (e.g. very old lsvg output); allow it.
		return nil
	case "original":
		return fmt.Errorf(
			"volume group %q is an original VG; TopoLVM requires a scalable VG (mkvg -S) "+
				"or a big VG; original VGs place the LVCB in the first block of each LV, "+
				"which a database writing from offset 0 will overwrite", vgName)
	default:
		// Unknown type; allow with a warning (don't fail on future AIX VG types).
		return nil
	}
}

// getVGType returns the VG TYPE field from `lsvg <vg>` output.
func getVGType(ctx context.Context, vgName string) (string, error) {
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsvg", vgName).Output()
	if err != nil {
		if command.IsLVMNotFound(err) {
			return "", command.ErrNotFound
		}
		return "", fmt.Errorf("lsvg %s: %w", vgName, err)
	}
	// Parse the two-column output for the "VG TYPE" key.
	// We reuse the extractKVPairs helper from the command package via a
	// local scan (the command package's function is unexported here, so we
	// inline a minimal version).
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		// Simple scan for "VG TYPE:  value"
		if idx := strings.Index(line, "VG TYPE:"); idx >= 0 {
			value := strings.TrimSpace(line[idx+len("VG TYPE:"):])
			// value may be followed by the next column (another key)
			if spaceIdx := strings.Index(value, "  "); spaceIdx >= 0 {
				value = strings.TrimSpace(value[:spaceIdx])
			}
			return value, nil
		}
	}
	return "", nil // key not found; not all AIX versions emit VG TYPE
}
