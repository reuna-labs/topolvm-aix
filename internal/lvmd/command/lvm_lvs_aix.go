//go:build aix

// lvm_lvs_aix.go – OS-command layer for LV operations on AIX.
//
// Pure parsing (parseLslvOutput, lvAttrForAIX, uidToAIXName, etc.) lives in
// lvm_lvs_parse_aix.go (no build tag) so it can be unit-tested everywhere.
//
// See design notes 3.1–3.7 for the name-length, label, and safety constraints.

package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// DeviceSubtype values from the lslv DEVICESUBTYPE field (note 4.1).
// (lv struct is in lvm_lv_types_aix.go — no build tag for cross-platform tests.)
const (
	DeviceSubtypeNone    = ""       // unknown / not reported
	DeviceSubtypeWithLVCB = "DS_LV" // LVCB at first block of LV
	DeviceSubtypeNoLVCB  = "DS_LVZ" // LVCB in VGDA; data starts at offset 0
)

// getLVReport fetches LV(s) for a VG or a single "vg/lv" spec.
func getLVReport(ctx context.Context, lvSpec string) (map[string]lv, error) {
	var vgName, lvName string
	if idx := findSlash(lvSpec); idx >= 0 {
		vgName = lvSpec[:idx]
		lvName = lvSpec[idx+1:]
	} else {
		vgName = lvSpec
	}

	if lvName != "" {
		result, err := fetchOneLV(ctx, vgName, lvName)
		if err != nil {
			return nil, err
		}
		return map[string]lv{result.name: result}, nil
	}

	return fetchAllLVsForVG(ctx, vgName)
}

func findSlash(s string) int {
	for i, c := range s {
		if c == '/' {
			return i
		}
	}
	return -1
}

// fetchAllLVsForVG uses `lsvg -l <vg>` + individual lslv calls.
func fetchAllLVsForVG(ctx context.Context, vgName string) (map[string]lv, error) {
	out, err := runAIXCommand(ctx, "/usr/sbin/lsvg", "-l", vgName)
	if err != nil {
		if IsLVMNotFound(err) {
			return nil, errors.Join(ErrNotFound, err)
		}
		return nil, fmt.Errorf("lsvg -l %s: %w", vgName, err)
	}

	names, err := parseLsvgL(out)
	if err != nil {
		return nil, fmt.Errorf("parse lsvg -l %s: %w", vgName, err)
	}
	if len(names) == 0 {
		return nil, ErrNotFound
	}

	result := make(map[string]lv, len(names))
	for _, name := range names {
		l, err := fetchOneLV(ctx, vgName, name)
		if err != nil {
			// Skip LVs we can't inspect (e.g. boot LV, paging LV).
			continue
		}
		result[l.name] = l
	}
	return result, nil
}

// fetchOneLV runs `lslv <lv>` and returns the parsed struct.
func fetchOneLV(ctx context.Context, vgName, lvName string) (lv, error) {
	out, err := runAIXCommand(ctx, "/usr/sbin/lslv", lvName)
	if err != nil {
		if IsLVMNotFound(err) {
			return lv{}, errors.Join(ErrNotFound, err)
		}
		return lv{}, fmt.Errorf("lslv %s: %w", lvName, err)
	}

	l, err := parseLslvOutput(vgName, out)
	if err != nil {
		return lv{}, fmt.Errorf("parse lslv %s: %w", lvName, err)
	}

	// Populate major/minor via os.Stat.
	if fi, err2 := os.Stat(l.path); err2 == nil {
		l.major, l.minor = devNumFromFileInfo(fi)
	}
	return l, nil
}

// devNumFromFileInfo extracts major and minor numbers from os.FileInfo.
// Returns (0, 0) when unavailable (best-effort).
func devNumFromFileInfo(_ os.FileInfo) (uint64, uint64) {
	// On AIX ppc64 go, os.FileInfo.Sys() is *syscall.Stat_t.
	// The major/minor are only needed for the CSI DevMajor/DevMinor fields
	// which are informational.  Leave as 0 when we can't extract them without
	// a CGO dependency.
	return 0, 0
}

// randomHex returns 2n hex characters of randomness.
func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
