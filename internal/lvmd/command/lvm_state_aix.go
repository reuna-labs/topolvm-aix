//go:build aix

// lvm_state_aix.go – batch VG+LV state retrieval on AIX.
//
// On AIX there is no "fullreport" command.  We simulate it with:
//   1. lsvg -o   → list of online VG names
//   2. lsvg <vg> → VG details (size, free)
//   3. lsvg -l <vg> → LV names in VG
//   4. lslv <lv>   → LV details (one call per LV)
//
// Note 3.7 warns that the 1+N call cost is significant.  The VolumeGroup
// reportLvs cache in lvm_aix.go mitigates repeated calls within one RPC.

package command

import (
	"context"
	"errors"
	"fmt"
)

// getLVMState is the AIX implementation of the batch state query.  It returns
// all online VGs and all their LVs in one logical operation.
func getLVMState(ctx context.Context) ([]vg, []lv, error) {
	vgNames, err := listOnlineVGNames(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("getLVMState: %w", err)
	}

	var vgs []vg
	var lvs []lv

	for _, name := range vgNames {
		v, err := getVGReport(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, nil, fmt.Errorf("getLVMState vg %q: %w", name, err)
		}
		vgs = append(vgs, v)

		lvsForVG, err := fetchAllLVsForVG(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// VG has no LVs; that's fine
				continue
			}
			return nil, nil, fmt.Errorf("getLVMState lvs in %q: %w", name, err)
		}
		for _, l := range lvsForVG {
			lvs = append(lvs, l)
		}
	}

	return vgs, lvs, nil
}
