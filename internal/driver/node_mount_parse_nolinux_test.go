//go:build aix

// node_mount_parse_aix_test.go – unit tests for AIX mount output parsing.
//
// Tagged !linux (runs on macOS and AIX where the parse files are also built).

package driver

import "testing"

func TestParseMountLines(t *testing.T) {
	// Realistic AIX /usr/sbin/mount output.
	input := `
node       mounted        mounted over  vfs       date           options
----       -------        ------------  ---       ----           -------
           /dev/hd4       /             jfs2      Nov 01 10:00   rw,log=/dev/hd8
           /dev/hd2       /usr          jfs2      Nov 01 10:00   rw,log=/dev/hd8
nfshost    /export/share  /nfs/share    nfs       Nov 01 10:01   ro
           /dev/fslv00    /data         jfs2      Nov 01 10:02   rw,log=INLINE
`
	entries := parseMountLines(input)

	// Expected: 4 entries
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %+v", len(entries), entries)
	}

	cases := []struct{ device, mountpoint string }{
		{"/dev/hd4", "/"},
		{"/dev/hd2", "/usr"},
		{"/export/share", "/nfs/share"},
		{"/dev/fslv00", "/data"},
	}
	for i, tc := range cases {
		e := entries[i]
		if e.device != tc.device {
			t.Errorf("entries[%d].device=%q, want %q", i, e.device, tc.device)
		}
		if e.mountpoint != tc.mountpoint {
			t.Errorf("entries[%d].mountpoint=%q, want %q", i, e.mountpoint, tc.mountpoint)
		}
	}
}

func TestParseMountLines_Empty(t *testing.T) {
	input := `
node       mounted        mounted over  vfs       date           options
----       -------        ------------  ---       ----           -------
`
	entries := parseMountLines(input)
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestParseMountLines_LocalOnly(t *testing.T) {
	// All local mounts (lines start with whitespace).
	input := `node       mounted        mounted over  vfs       date        options
----       -------        ------------  ---       ----        -------
           /dev/hd4       /             jfs2      Nov 01 10:00  rw
           /dev/hd2       /usr          jfs2      Nov 01 10:00  rw
`
	entries := parseMountLines(input)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].device != "/dev/hd4" {
		t.Errorf("device[0]=%q, want /dev/hd4", entries[0].device)
	}
}
