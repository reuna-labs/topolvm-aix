//go:build aix

// lvm_vgs_aix.go – OS-command layer for VG operations on AIX.
//
// Pure parsing helpers (parseLsvgOutput, extractKVPairs, parseLsvgL, etc.)
// live in lvm_vgs_parse_aix.go (no build tag) so they can be unit-tested on
// any platform.

package command

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
)

// vg mirrors the Linux struct; only fields used by the rest of lvmd are kept.
type vg struct {
	name string
	uuid string // not exposed by lsvg; left empty on AIX
	size uint64 // bytes (PP_SIZE × TOTAL PPs)
	free uint64 // bytes (PP_SIZE × FREE PPs)
}

// getVGReport fetches a single VG's info using `lsvg <name>`.
func getVGReport(ctx context.Context, name string) (vg, error) {
	out, err := runAIXCommand(ctx, "/usr/sbin/lsvg", name)
	if err != nil {
		if IsLVMNotFound(err) {
			return vg{}, errors.Join(ErrNotFound, err)
		}
		return vg{}, err
	}

	vgName, total, free, err := parseLsvgOutput(out)
	if err != nil {
		return vg{}, fmt.Errorf("getVGReport %q: %w", name, err)
	}
	if vgName != name {
		return vg{}, fmt.Errorf("getVGReport: got VG %q, expected %q", vgName, name)
	}

	return vg{name: vgName, size: total, free: free}, nil
}

// listOnlineVGNames returns the names of all online VGs via `lsvg -o`.
func listOnlineVGNames(ctx context.Context) ([]string, error) {
	out, err := runAIXCommand(ctx, "/usr/sbin/lsvg", "-o")
	if err != nil {
		return nil, fmt.Errorf("lsvg -o: %w", err)
	}
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		name := strings.TrimSpace(sc.Text())
		if name != "" {
			names = append(names, name)
		}
	}
	return names, sc.Err()
}
