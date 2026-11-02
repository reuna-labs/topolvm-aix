//go:build aix

// lvm_lslv_parse_aix_test.go – tests for parseLslvOutput (aix-only).

package command

import "testing"

func TestParseLslvOutput(t *testing.T) {
	uid := "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	input := `LOGICAL VOLUME:     tv3b34ef12              VOLUME GROUP:   datavg
  LV IDENTIFIER:      00f8c0fa00004c000000018caaaa1234.1   PERMISSION:    read/write
  VG STATE:           active/complete          LV STATE:       opened/syncd
  TYPE:               jfs2                     WRITE VERIFY:   off
  MAX LPs:            512                      PP SIZE:        64 megabyte(s)
  COPIES:             1                        SCHED POLICY:   parallel
  LPs:                3                        PPs:            3
  STALE PPs:          0                        BB POLICY:      relocatable
  INTER-POLICY:       minimum                  INTRA-POLICY:   center
  MOUNT POINT:        /data                    LABEL:          ` + uid + `
  MIRROR WRITE CONSISTENCY: on/ACTIVE
  EACH LP COPY ON A SEPARATE PV ?: yes
  Serialize IO ?:     NO
  DEVICESUBTYPE:      DS_LVZ
`
	l, err := parseLslvOutput("datavg", []byte(input))
	if err != nil {
		t.Fatalf("parseLslvOutput error: %v", err)
	}
	if l.vgName != "datavg" {
		t.Errorf("vgName=%q, want datavg", l.vgName)
	}
	if l.lvLabel != uid {
		t.Errorf("lvLabel=%q, want %q", l.lvLabel, uid)
	}
	if l.deviceSubtype != "DS_LVZ" {
		t.Errorf("deviceSubtype=%q, want DS_LVZ", l.deviceSubtype)
	}
	if l.permission != "read/write" {
		t.Errorf("permission=%q, want read/write", l.permission)
	}
	if l.copies != 1 {
		t.Errorf("copies=%d, want 1", l.copies)
	}
	const want = uint64(3) * 64 * (1 << 20) // 3 LPs × 64 MiB
	if l.size != want {
		t.Errorf("size=%d, want %d", l.size, want)
	}
	if l.lvState != "opened/syncd" {
		t.Errorf("lvState=%q, want opened/syncd", l.lvState)
	}
}
