//go:build aix

// node_mount_parse_aix.go – pure parsing of AIX mount output.
//
// Tagged !linux so it can be tested on macOS and AIX.  The OS-exec caller
// (parseMountOutput) lives in node_aix.go (tagged aix).

package driver

import "strings"

// mountEntry represents one entry from `/usr/sbin/mount` output.
type mountEntry struct {
	device     string
	mountpoint string
	fstype     string
}

// parseMountLines parses the output of `/usr/sbin/mount` with no arguments.
//
// AIX mount output (note 2.5):
//
//	node    mounted         mounted over    vfs       date        options
//	----    -------         ------------    ---       ----        -------
//	        /dev/hd4        /               jfs2      ...         rw,...
//
// Rules:
//   - A row starting with whitespace has no node column (local mount).
//   - The date is three words.
//   - Options may be absent.
func parseMountLines(output string) []mountEntry {
	var entries []mountEntry
	lines := strings.Split(output, "\n")
	headerDone := false
	for _, line := range lines {
		if !headerDone {
			if strings.Contains(line, "-----") {
				headerDone = true
			}
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		// Minimum: device mountpoint vfs + 3 date words = 6 fields.
		if len(fields) < 6 {
			continue
		}
		var device, mountpoint string
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			// No node column.
			device = fields[0]
			mountpoint = fields[1]
		} else {
			// Has node column.
			device = fields[1]
			mountpoint = fields[2]
		}
		entries = append(entries, mountEntry{
			device:     device,
			mountpoint: mountpoint,
		})
	}
	return entries
}
