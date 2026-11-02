//go:build !linux

// lvm_aix_parse_test.go – unit tests for AIX lsvg/lslv/mount parsers.
//
// Tagged !linux: the AIX parse functions are excluded from Linux builds
// (they reference the AIX lv struct).  Tests run on macOS and AIX.

package command

import (
	"strings"
	"testing"
)

// ── lsvg (VG) parser ─────────────────────────────────────────────────────────

func TestParseLsvgOutput(t *testing.T) {
	// Realistic lsvg datavg output from AIX 7.3 TL4.
	input := `VOLUME GROUP:       datavg                   VG IDENTIFIER:  00f8c0fa00004c000000018caaaa1234
VG STATE:           active                   PP SIZE:        64 megabyte(s)
VG PERMISSION:      read/write               TOTAL PPs:      318 (20352 megabytes)
MAX LVs:            256                      FREE PPs:       30 (1920 megabytes)
LVs:                3                        STALE PPs:      0
OPEN LVs:           1                        QUORUM:         2 (Enabled)
TOTAL PVs:          1                        VG DESCRIPTORS: 2
STALE PVs:          0                        STALE PARTITIONS: 0
ACTIVE PVs:         1                        AUTO ON:        yes
MAX PPs per VG:     32512                    MAX PVs:        1024
LTG size (Dynamic): 512 kilobyte(s)          AUTO SYNC:      no
`
	name, total, free, err := parseLsvgOutput([]byte(input))
	if err != nil {
		t.Fatalf("parseLsvgOutput error: %v", err)
	}
	if name != "datavg" {
		t.Errorf("name: got %q, want %q", name, "datavg")
	}
	const mib = uint64(1 << 20)
	wantTotal := uint64(318) * 64 * mib
	wantFree := uint64(30) * 64 * mib
	if total != wantTotal {
		t.Errorf("total: got %d, want %d", total, wantTotal)
	}
	if free != wantFree {
		t.Errorf("free: got %d, want %d", free, wantFree)
	}
}

func TestParseLsvgOutput_MissingField(t *testing.T) {
	// Missing FREE PPs should return an error.
	input := `VOLUME GROUP:       datavg
PP SIZE:        64 megabyte(s)
TOTAL PPs:      100 (6400 megabytes)
`
	_, _, _, err := parseLsvgOutput([]byte(input))
	if err == nil {
		t.Error("expected error for missing FREE PPs, got nil")
	}
}

// ── lsvg -l parser ────────────────────────────────────────────────────────────

func TestParseLsvgL(t *testing.T) {
	input := `datavg:
LV NAME             TYPE       LPs     PPs     PVs  LV STATE      MOUNT POINT
fslv00              jfs2       192     192     1    open/syncd    /data
fslv01              jfs2       64      64      1    closed/syncd  /var
hd5                 boot       1       1       1    closed/syncd  N/A
`
	names, err := parseLsvgL([]byte(input))
	if err != nil {
		t.Fatalf("parseLsvgL error: %v", err)
	}
	want := []string{"fslv00", "fslv01", "hd5"}
	if len(names) != len(want) {
		t.Fatalf("len(names)=%d, want %d: %v", len(names), len(want), names)
	}
	for i, n := range names {
		if n != want[i] {
			t.Errorf("names[%d]=%q, want %q", i, n, want[i])
		}
	}
}

func TestParseLsvgL_Empty(t *testing.T) {
	input := `datavg:
LV NAME             TYPE       LPs     PPs     PVs  LV STATE      MOUNT POINT
`
	names, err := parseLsvgL([]byte(input))
	if err != nil {
		t.Fatalf("parseLsvgL error: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty, got %v", names)
	}
}

// ── lvAttrForAIX ─────────────────────────────────────────────────────────────

func TestLvAttrForAIX(t *testing.T) {
	cases := []struct {
		permission string
		copies     int
		lvState    string
		vgState    string
		wantType   byte
		wantPerm   byte
		wantOpen   byte
		wantHealth byte
	}{
		{"read/write", 1, "opened/syncd", "active/complete", '-', 'w', 'o', '-'},
		{"read/write", 1, "closed/syncd", "active/complete", '-', 'w', '-', '-'},
		{"read/only", 1, "closed/syncd", "active/complete", '-', 'r', '-', '-'},
		{"read/write", 2, "opened/syncd", "active/complete", 'm', 'w', 'o', '-'},
		{"read/write", 1, "opened/stale", "active/complete", '-', 'w', 'o', 'p'},
		{"read/write", 1, "opened/syncd", "active/partial", '-', 'w', 'o', 'p'},
	}
	for _, tc := range cases {
		attr := lvAttrForAIX(tc.permission, tc.copies, tc.lvState, tc.vgState)
		if len(attr) != 10 {
			t.Errorf("attr len=%d, want 10: %q", len(attr), attr)
			continue
		}
		if attr[0] != tc.wantType {
			t.Errorf("type %q: got %c, want %c", tc.lvState, attr[0], tc.wantType)
		}
		if attr[1] != tc.wantPerm {
			t.Errorf("perm %q: got %c, want %c", tc.permission, attr[1], tc.wantPerm)
		}
		if attr[5] != tc.wantOpen {
			t.Errorf("open %q: got %c, want %c", tc.lvState, attr[5], tc.wantOpen)
		}
		if attr[8] != tc.wantHealth {
			t.Errorf("health %q/%q: got %c, want %c", tc.lvState, tc.vgState, attr[8], tc.wantHealth)
		}
	}
}

// ── uidToAIXName ─────────────────────────────────────────────────────────────

func TestUIDToAIXName(t *testing.T) {
	uid := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	name := uidToAIXName(uid)
	if len(name) > 15 {
		t.Errorf("name too long: %q (%d chars)", name, len(name))
	}
	if !strings.HasPrefix(name, "tv") {
		t.Errorf("name should start with 'tv': %q", name)
	}
	// Deterministic: same input → same output.
	if uidToAIXName(uid) != name {
		t.Error("uidToAIXName is not deterministic")
	}
	// Different UID → different name (collision resistance).
	uid2 := "6ba7b811-9dad-11d1-80b4-00c04fd430c8"
	if uidToAIXName(uid2) == name {
		t.Errorf("collision between %q and %q", uid, uid2)
	}
}

// ── isTopoLVMVolume ───────────────────────────────────────────────────────────

func TestIsTopoLVMVolume(t *testing.T) {
	cases := []struct{ label string; want bool }{
		{"6ba7b810-9dad-11d1-80b4-00c04fd430c8", true},
		{"6BA7B810-9DAD-11D1-80B4-00C04FD430C8", true}, // upper-case
		{"hd4", false},
		{"", false},
		{"not-a-uuid-at-all", false},
	}
	for _, tc := range cases {
		got := isTopoLVMVolume(tc.label)
		if got != tc.want {
			t.Errorf("isTopoLVMVolume(%q)=%v, want %v", tc.label, got, tc.want)
		}
	}
}

// ── normaliseStripeSize ───────────────────────────────────────────────────────

func TestNormaliseStripeSize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"64k", "64K"},
		{"64K", "64K"},
		{"128m", "128M"},
		{"", ""},
		{"1g", "1G"},
	}
	for _, tc := range cases {
		got := normaliseStripeSize(tc.in)
		if got != tc.want {
			t.Errorf("normaliseStripeSize(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── mibToLPs ─────────────────────────────────────────────────────────────────

func TestMibToLPs(t *testing.T) {
	cases := []struct {
		sizeBytes  uint64
		ppSizeMiB  uint64
		wantLPs    uint64
	}{
		{64 * 1 << 20, 64, 1},              // exact one PP
		{65 * 1 << 20, 64, 2},              // one byte over → round up
		{128 * 1 << 20, 64, 2},             // exact two PPs
		{0, 64, 0},
		{1, 64, 1},                          // even 1 byte → 1 LP
	}
	for _, tc := range cases {
		got := mibToLPs(tc.sizeBytes, tc.ppSizeMiB)
		if got != tc.wantLPs {
			t.Errorf("mibToLPs(%d, %d)=%d, want %d", tc.sizeBytes, tc.ppSizeMiB, got, tc.wantLPs)
		}
	}
}
