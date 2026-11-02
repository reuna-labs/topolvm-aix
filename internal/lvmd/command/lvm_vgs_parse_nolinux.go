//go:build !linux

// lvm_vgs_parse_aix.go – pure parsing of `lsvg` text output.
//
// Tagged !linux (not aix-only) so the parser can be unit-tested on macOS and
// AIX.  On Linux the lsvg commands are not used; add !linux to avoid
// conflicts with Linux types.  The OS-exec layer lives in lvm_vgs_aix.go.

package command

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// parseLsvgOutput parses the output of `lsvg <vg>`.
// It returns the name, total bytes, and free bytes.
func parseLsvgOutput(data []byte) (string, uint64, uint64, error) {
	kv := make(map[string]string)
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		for k, v := range extractKVPairs(sc.Text()) {
			kv[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return "", 0, 0, err
	}

	name, ok := kv["VOLUME GROUP"]
	if !ok {
		return "", 0, 0, errors.New("lsvg: VOLUME GROUP key not found")
	}
	name = strings.TrimSpace(name)

	ppSizeMiB, err := parsePPSize(kv)
	if err != nil {
		return "", 0, 0, fmt.Errorf("lsvg: %w", err)
	}

	totalPPs, err := parsePPCount(kv, "TOTAL PPs")
	if err != nil {
		return "", 0, 0, fmt.Errorf("lsvg: %w", err)
	}

	freePPs, err := parsePPCount(kv, "FREE PPs")
	if err != nil {
		return "", 0, 0, fmt.Errorf("lsvg: %w", err)
	}

	const mib = uint64(1 << 20)
	total := totalPPs * ppSizeMiB * mib
	free := freePPs * ppSizeMiB * mib
	return name, total, free, nil
}

// kvKeyRE matches a key in lsvg/lslv two-column output.
//
// A key is one or more capitalized words (each starting with A-Z or a digit,
// optionally followed by lowercase letters or digits) separated by single
// spaces.  The key is preceded by start-of-string or ≥2 spaces, and followed
// immediately by a colon.
//
// Examples: "VOLUME GROUP", "PP SIZE", "TOTAL PPs", "VG STATE", "FREE PPs"
var kvKeyRE = regexp.MustCompile(`(?:^|\s{2,})([A-Z][A-Za-z0-9]*(?: [A-Z][A-Za-z0-9]*)*)\s*:`)

// extractKVPairs parses one line of lsvg/lslv output into key→value pairs.
func extractKVPairs(line string) map[string]string {
	out := make(map[string]string)
	locs := kvKeyRE.FindAllStringSubmatchIndex(line, -1)
	if len(locs) == 0 {
		return out
	}
	for i, loc := range locs {
		// loc[2]:loc[3] is the capture group (key text without leading spaces).
		key := strings.TrimSpace(line[loc[2]:loc[3]])
		// The colon is the last character of the full match loc[0]:loc[1].
		colonAbs := loc[1] - 1
		valStart := colonAbs + 1
		var valEnd int
		if i+1 < len(locs) {
			valEnd = locs[i+1][0] // start of next full match (includes leading spaces)
		} else {
			valEnd = len(line)
		}
		value := strings.TrimSpace(line[valStart:valEnd])
		if key != "" {
			out[key] = value
		}
	}
	return out
}

// parsePPSize returns the physical partition size in MiB from a kv map.
// e.g. "PP SIZE" → "64 megabyte(s)" → 64
func parsePPSize(kv map[string]string) (uint64, error) {
	raw, ok := kv["PP SIZE"]
	if !ok {
		return 0, errors.New("PP SIZE key not found")
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return 0, fmt.Errorf("PP SIZE value is empty")
	}
	n, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse PP SIZE %q: %w", raw, err)
	}
	return n, nil
}

// parsePPCount parses a "N (M megabytes)" value, returning N.
func parsePPCount(kv map[string]string, key string) (uint64, error) {
	raw, ok := kv[key]
	if !ok {
		return 0, fmt.Errorf("%s key not found", key)
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return 0, fmt.Errorf("%s value is empty", key)
	}
	n, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse %s %q: %w", key, raw, err)
	}
	return n, nil
}

// parseLsvgL parses the output of `lsvg -l <vg>` and returns LV names.
//
//	datavg:
//	LV NAME      TYPE    LPs   PPs   PVs  LV STATE    MOUNT POINT
//	fslv00       jfs2    192   192   1    open/syncd  /data
func parseLsvgL(data []byte) ([]string, error) {
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	headerFound := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), ":") {
			continue
		}
		if strings.HasPrefix(line, "LV NAME") {
			headerFound = true
			continue
		}
		if !headerFound {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		names = append(names, fields[0])
	}
	return names, sc.Err()
}
