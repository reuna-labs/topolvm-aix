//go:build !linux

// lvm_lvs_parse_nolinux.go – pure AIX LV parsing functions.
//
// Tagged !linux: these functions have no OS dependencies and can be
// unit-tested on macOS and AIX.  None of them reference the lv struct.

package command

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// uidToAIXName derives a deterministic ≤15-char AIX LV name from a TopoLVM
// volume UID.  Format: "tv" + 8 hex digits of FNV-32a(uid).
func uidToAIXName(uid string) string {
	h := fnv32a([]byte(uid))
	return fmt.Sprintf("tv%08x", h)
}

// fnv32a computes the FNV-1a 32-bit hash.
func fnv32a(data []byte) uint32 {
	const (
		offset32 uint32 = 2166136261
		prime32  uint32 = 16777619
	)
	h := offset32
	for _, b := range data {
		h ^= uint32(b)
		h *= prime32
	}
	return h
}

// uuidRE matches a standard 36-char RFC 4122 UUID (lower-case).
var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// isTopoLVMVolume returns true when label looks like a TopoLVM UID.
func isTopoLVMVolume(label string) bool {
	return uuidRE.MatchString(strings.ToLower(strings.TrimSpace(label)))
}

// lvNameForUID returns the AIX name for a TopoLVM UID, or the value itself
// if it is not a UUID.
func lvNameForUID(uid string) string {
	if uuidRE.MatchString(strings.ToLower(uid)) {
		return uidToAIXName(uid)
	}
	return uid
}

// lvAttrForAIX synthesises a 10-char LVM2-style lv_attr string from lslv data.
// See design note 3.5.
func lvAttrForAIX(permission string, copies int, lvState, vgState string) string {
	volType := byte('-')
	if copies > 1 {
		volType = 'm'
	}

	perm := byte('w')
	if strings.Contains(permission, "read/only") || strings.Contains(permission, "read only") {
		perm = 'r'
	}

	open := byte('-')
	lsLower := strings.ToLower(lvState)
	if strings.HasPrefix(lsLower, "opened") || strings.HasPrefix(lsLower, "open") {
		open = 'o'
	}

	health := byte('-')
	if strings.Contains(lsLower, "stale") {
		health = 'p'
	}
	if strings.Contains(strings.ToLower(vgState), "partial") {
		health = 'p'
	}

	return fmt.Sprintf("%c%c%c%c%c%c%c%c%c%c",
		volType, perm, 'n', '-', 'a', open, '-', '-', health, '-')
}

// mklvFlags builds mklv arguments from TopoLVM/LVM2 parameters (note 3.6).
func mklvFlags(stripe uint, stripeSize string, lvcreateOptions []string) []string {
	var flags []string
	if stripe > 0 {
		flags = append(flags, "-C", "y")
		if stripeSize != "" {
			flags = append(flags, "-S", normaliseStripeSize(stripeSize))
		}
	}
	flags = append(flags, lvcreateOptions...)
	return flags
}

// normaliseStripeSize converts "64k" → "64K" (AIX wants uppercase suffix).
func normaliseStripeSize(s string) string {
	if len(s) == 0 {
		return s
	}
	last := rune(s[len(s)-1])
	if unicode.IsLower(last) {
		return s[:len(s)-1] + string(unicode.ToUpper(last))
	}
	return s
}

// mibToLPs converts a byte count to a physical partition count, rounded up.
func mibToLPs(sizeBytes, ppSizeMiB uint64) uint64 {
	ppBytes := ppSizeMiB * (1 << 20)
	if ppBytes == 0 {
		return 0
	}
	return (sizeBytes + ppBytes - 1) / ppBytes
}
