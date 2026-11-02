//go:build aix

// lvm_lslv_parse_aix.go – parseLslvOutput, which returns the AIX lv struct.
//
// Kept aix-only because it references the lv struct (defined in
// lvm_lv_types_nolinux.go, also aix-only).

package command

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// parseLslvOutput parses the output of `lslv <lv>` into a partially-filled lv.
func parseLslvOutput(vgName string, data []byte) (lv, error) {
	kv := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		for k, v := range extractKVPairs(sc.Text()) {
			kv[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return lv{}, err
	}

	lvName, ok := kv["LOGICAL VOLUME"]
	if !ok {
		return lv{}, fmt.Errorf("lslv: LOGICAL VOLUME key not found")
	}
	lvName = strings.TrimSpace(lvName)

	label := strings.TrimSpace(kv["LABEL"])
	devSub := strings.TrimSpace(kv["DEVICESUBTYPE"])
	permission := strings.TrimSpace(kv["PERMISSION"])

	copies := 1
	if raw, ok2 := kv["COPIES"]; ok2 {
		fields := strings.Fields(raw)
		if len(fields) > 0 {
			if c, err := strconv.Atoi(fields[0]); err == nil && c > 0 {
				copies = c
			}
		}
	}

	// LPs → size in bytes.
	ppSizeMiB, _ := parsePPSize(kv) // 0 if absent
	var lpCount uint64
	if raw, ok2 := kv["LPs"]; ok2 {
		fields := strings.Fields(raw)
		if len(fields) > 0 {
			lpCount, _ = strconv.ParseUint(fields[0], 10, 64)
		}
	}

	var sizeBytes uint64
	if ppSizeMiB > 0 && lpCount > 0 {
		sizeBytes = lpCount * ppSizeMiB * (1 << 20)
	}

	lvState := strings.TrimSpace(kv["LV STATE"])
	vgStateVal := strings.TrimSpace(kv["VG STATE"])
	attr := lvAttrForAIX(permission, copies, lvState, vgStateVal)

	return lv{
		name:          lvName,
		fullName:      vgName + "/" + lvName,
		uuid:          label, // TopoLVM UID stored in label
		path:          "/dev/" + lvName,
		attr:          attr,
		vgName:        vgName,
		size:          sizeBytes,
		lvLabel:       label,
		deviceSubtype: devSub,
		copies:        copies,
		permission:    permission,
		lvState:       lvState,
		vgState:       vgStateVal,
	}, nil
}
