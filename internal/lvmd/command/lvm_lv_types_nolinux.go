//go:build aix

// lvm_lv_types_aix.go – AIX logical volume data type.
//
// Tagged !linux: on Linux the lv struct is defined in lvm_lvs.go (tagged
// !aix).  On macOS/AIX/other platforms this file provides the struct so that
// lvm_lvs_parse_aix.go and lvm_aix_parse_test.go can compile.

package command

// lv holds all state for one AIX logical volume.
// On Linux this type is unused at runtime but must compile.
type lv struct {
	name     string // AIX LV name (≤15 chars, derived from UID)
	fullName string // vg/lv
	// uuid is the TopoLVM UID stored in the LV label; empty for non-topolvm LVs.
	uuid   string
	path   string // /dev/<lvname>
	major  uint64
	minor  uint64
	origin string
	originSize uint64
	poolLV  string
	tags    []string // always nil on AIX (UID in label)
	attr    string   // synthesised 10-char LVM2-style attr
	vgName  string
	size    uint64   // bytes (LP count × PP size)

	// AIX-specific
	lvLabel       string // raw LV label (TopoLVM UID or user label)
	deviceSubtype string // DS_LVZ or DS_LV
	copies        int
	permission    string
	lvState       string
	vgState       string

	dataPercent     float64
	metaDataPercent float64
}

func (u *lv) isThinPool() bool { return false }
