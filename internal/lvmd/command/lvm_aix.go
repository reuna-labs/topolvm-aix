//go:build aix

// lvm_aix.go – AIX implementation of the VolumeGroup / LogicalVolume API.
//
// Key differences from the Linux (LVM2) implementation:
//
//   - LV names are at most 15 characters; the full TopoLVM UID is kept in
//     the LV label.  uidToAIXName() derives the short name.
//
//   - Thin pools do not exist on AIX.  FindPool/CreatePool/ListPools return
//     errors or empty results.  CreateVolume on a thin pool panics.
//
//   - Sizes are rounded up to whole physical partitions.
//
//   - After mklv, the head of the new LV is zeroed (first 1 MiB) to prevent
//     stale signatures confusing the node's safe-format logic (note 3.3).
//
//   - Original VGs are refused at startup; this is enforced by
//     ValidateVGSubtype (called from device_class_manager_aix.go).

package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/topolvm/topolvm"
)

// Names of AIX LVM commands.
const (
	cmdMklv    = "/usr/sbin/mklv"
	cmdExtendlv = "/usr/sbin/extendlv"
	cmdRmlv    = "/usr/sbin/rmlv"
	cmdChlv    = "/usr/sbin/chlv"
)

// ErrNotFound is returned when a VG or LV is not found.
var ErrNotFound = errors.New("not found")

// ErrNoMultipleOfSectorSize is returned when a volume is requested that is
// smaller than the minimum sector size.
var ErrNoMultipleOfSectorSize = fmt.Errorf("cannot create volume as given size "+
	"is not a multiple of %d and could get rejected", topolvm.MinimumSectorSize)

// VolumeGroup represents an AIX volume group.
type VolumeGroup struct {
	state vg
	// reportLvs is the cached result of a batch LV listing; nil means uncached.
	reportLvs map[string]lv
}

// getLVs returns the current state of LVs for the given VolumeGroup.
func getLVs(ctx context.Context, vg *VolumeGroup, lvname string) (map[string]lv, error) {
	if len(vg.reportLvs) > 0 {
		if lvname != "" {
			if l, ok := vg.reportLvs[lvname]; ok {
				return map[string]lv{lvname: l}, nil
			}
			return nil, ErrNotFound
		}
		return vg.reportLvs, nil
	}

	name := vg.state.name
	if lvname != "" {
		name += "/" + lvname
	}
	return getLVReport(ctx, name)
}

// Update refreshes the VolumeGroup state from the OS.
func (vg *VolumeGroup) Update(ctx context.Context) error {
	newVG, err := FindVolumeGroup(ctx, vg.Name())
	if err != nil {
		return err
	}
	vg.reportLvs = nil
	vg.state = newVG.state
	return nil
}

// Name returns the volume group name.
func (vg *VolumeGroup) Name() string { return vg.state.name }

// Size returns the capacity of the volume group in bytes.
func (vg *VolumeGroup) Size() (uint64, error) { return vg.state.size, nil }

// Free returns the free space of the volume group in bytes.
func (vg *VolumeGroup) Free() (uint64, error) { return vg.state.free, nil }

// FindVolumeGroup finds a named volume group on AIX.
func FindVolumeGroup(ctx context.Context, name string) (*VolumeGroup, error) {
	v, err := getVGReport(ctx, name)
	if err != nil {
		return nil, err
	}
	return &VolumeGroup{state: v}, nil
}

// SearchVolumeGroupList finds a VG by name in a pre-fetched slice.
func SearchVolumeGroupList(vgs []*VolumeGroup, name string) (*VolumeGroup, error) {
	for _, vg := range vgs {
		if vg.state.name == name {
			return vg, nil
		}
	}
	return nil, ErrNotFound
}

func filterLV(vgName string, lvs []lv) map[string]lv {
	filtered := map[string]lv{}
	for _, l := range lvs {
		if l.vgName == vgName {
			filtered[l.name] = l
		}
	}
	return filtered
}

// ListVolumeGroups lists all online VGs and their LVs.
func ListVolumeGroups(ctx context.Context) ([]*VolumeGroup, error) {
	vgs, lvs, err := getLVMState(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]*VolumeGroup, 0, len(vgs))
	for _, v := range vgs {
		groups = append(groups, &VolumeGroup{
			state:     v,
			reportLvs: filterLV(v.name, lvs),
		})
	}
	return groups, nil
}

// FindVolume finds a named LV in this VolumeGroup.
// On AIX, name may be the TopoLVM UID (36 chars) or the AIX LV name (≤15 chars).
func (vg *VolumeGroup) FindVolume(ctx context.Context, name string) (*LogicalVolume, error) {
	volumes, err := vg.listVolumes(ctx, name)
	if err != nil {
		return nil, err
	}
	if vol, ok := volumes[name]; ok {
		return vol, nil
	}
	return nil, ErrNotFound
}

// ListVolumes lists all logical volumes in this volume group.
func (vg *VolumeGroup) ListVolumes(ctx context.Context) (map[string]*LogicalVolume, error) {
	return vg.listVolumes(ctx, "")
}

func (vg *VolumeGroup) listVolumes(ctx context.Context, name string) (map[string]*LogicalVolume, error) {
	ret := map[string]*LogicalVolume{}

	var lvs map[string]lv
	if vg.reportLvs != nil {
		lvs = vg.reportLvs
	} else {
		var err error
		if lvs, err = getLVs(ctx, vg, name); err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}

	for _, l := range lvs {
		if !l.isThinPool() {
			ret[l.name] = vg.convertLV(l)
		}
	}

	// Also allow look-up by TopoLVM UID (label) → return under the UID key
	// so that upstream code that uses the UID as the name can find the volume.
	if name != "" && len(name) == 36 {
		aixName := uidToAIXName(name)
		if l, ok := lvs[aixName]; ok && strings.EqualFold(l.lvLabel, name) {
			lv := vg.convertLV(l)
			// Return under both the UID and the AIX name.
			ret[name] = lv
		}
	}

	return ret, nil
}

func (vg *VolumeGroup) convertLV(l lv) *LogicalVolume {
	// The "name" exposed to TopoLVM callers is the UID (stored in the label)
	// when the LV was created by TopoLVM, otherwise the AIX name.
	topoName := l.name
	if isTopoLVMVolume(l.lvLabel) {
		topoName = l.lvLabel
	}

	return &LogicalVolume{
		fullname: fullName(l.name, vg),
		name:     topoName,
		path:     l.path,
		vg:       vg,
		size:     l.size,
		devMajor: uint32(l.major),
		devMinor: uint32(l.minor),
		tags:     l.tags,
		attr:     l.attr,
	}
}

// CreateVolume creates a logical volume on AIX.
// name is the TopoLVM UID; we derive the AIX LV name from it.
func (vg *VolumeGroup) CreateVolume(ctx context.Context, name string, size uint64, tags []string, stripe uint, stripeSize string, lvcreateOptions []string) error {
	if size%uint64(topolvm.MinimumSectorSize) != 0 {
		return ErrNoMultipleOfSectorSize
	}

	// Derive the AIX name (≤15 chars).
	aixName := lvNameForUID(name)

	// Calculate LP count.  We need the VG's PP size.
	ppSizeMiB, err := vgPPSizeMiB(ctx, vg.Name())
	if err != nil {
		return fmt.Errorf("CreateVolume: cannot get PP size: %w", err)
	}
	lpCount := mibToLPs(size, ppSizeMiB)
	if lpCount == 0 {
		lpCount = 1
	}

	// Build mklv arguments: mklv [-C y [-S size]] [-L label] -y <name> <vg> <lps>
	// We store the TopoLVM UID in the label with -L.
	// Label must fit in 127 chars (UUID is 36 chars — fine).
	label := name // the TopoLVM UID
	args := []string{"-y", aixName, "-L", label}
	args = append(args, mklvFlags(stripe, stripeSize, lvcreateOptions)...)
	args = append(args, vg.Name(), fmt.Sprintf("%d", lpCount))

	if _, err := runAIXCommand(ctx, cmdMklv, args...); err != nil {
		return fmt.Errorf("mklv: %w", err)
	}

	// Zero the first 1 MiB of the new LV to clear any stale signatures (note 3.3).
	if err := zeroLVHead("/dev/"+aixName, 1<<20); err != nil {
		// Best effort; log-worthy but not fatal for the create itself.
		_ = err
	}

	return nil
}

// vgPPSizeMiB returns the PP size in MiB for the named VG.
func vgPPSizeMiB(ctx context.Context, vgName string) (uint64, error) {
	out, err := runAIXCommand(ctx, "/usr/sbin/lsvg", vgName)
	if err != nil {
		return 0, err
	}
	kv := make(map[string]string)
	scanLines(out, func(line string) {
		for k, v := range extractKVPairs(line) {
			kv[k] = v
		}
	})
	return parsePPSize(kv)
}

// zeroLVHead zeros the first `size` bytes of device at path.
func zeroLVHead(devPath string, size int) error {
	f, err := os.OpenFile(devPath, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	zeros := make([]byte, 4096)
	written := 0
	for written < size {
		n := size - written
		if n > len(zeros) {
			n = len(zeros)
		}
		nw, err := f.Write(zeros[:n])
		written += nw
		if err != nil {
			return err
		}
	}
	return nil
}

// scanLines calls f for each newline-delimited line in data.
func scanLines(data []byte, f func(string)) {
	for len(data) > 0 {
		i := 0
		for i < len(data) && data[i] != '\n' {
			i++
		}
		f(string(data[:i]))
		if i < len(data) {
			data = data[i+1:]
		} else {
			break
		}
	}
}

// FindPool is not supported on AIX.  AIX LVM has no thin pools.
func (vg *VolumeGroup) FindPool(_ context.Context, _ string) (*ThinPool, error) {
	return nil, fmt.Errorf("thin pools are not supported on AIX")
}

// ListPools always returns empty on AIX.
func (vg *VolumeGroup) ListPools(_ context.Context, _ string) (map[string]*ThinPool, error) {
	return map[string]*ThinPool{}, nil
}

// CreatePool is not supported on AIX.
func (vg *VolumeGroup) CreatePool(_ context.Context, _ string, _ uint64) (*ThinPool, error) {
	return nil, fmt.Errorf("thin pools are not supported on AIX")
}

// ThinPool represents a thin pool.  On AIX this type exists for interface
// compatibility only and all operations return errors.
type ThinPool struct {
	vg    *VolumeGroup
	state lv
}

// ThinPoolUsage holds current usage data for a thin pool.
type ThinPoolUsage struct {
	DataPercent     float64
	MetadataPercent float64
	VirtualBytes    uint64
	SizeBytes       uint64
}

// FreeBytes is not meaningful on AIX.
func (t ThinPoolUsage) FreeBytes(_ float64) (uint64, error) {
	return 0, errors.New("thin pools are not supported on AIX")
}

func fullName(name string, vg *VolumeGroup) string {
	return fmt.Sprintf("%v/%v", vg.Name(), name)
}

func newThinPool(vg *VolumeGroup, lvmLv lv) *ThinPool {
	return &ThinPool{vg, lvmLv}
}

// Name returns the thin pool name.
func (t *ThinPool) Name() string { return t.state.name }

// FullName returns a VG-prefixed name.
func (t *ThinPool) FullName() string { return t.state.fullName }

// VG returns the volume group.
func (t *ThinPool) VG() *VolumeGroup { return t.vg }

// Size returns the pool size.
func (t *ThinPool) Size() uint64 { return t.state.size }

// Resize returns an error on AIX.
func (t *ThinPool) Resize(_ context.Context, _ uint64) error {
	return errors.New("thin pools are not supported on AIX")
}

// ListVolumes returns an empty map on AIX.
func (t *ThinPool) ListVolumes(_ context.Context) (map[string]*LogicalVolume, error) {
	return map[string]*LogicalVolume{}, nil
}

// FindVolume returns ErrNotFound on AIX.
func (t *ThinPool) FindVolume(_ context.Context, _ string) (*LogicalVolume, error) {
	return nil, ErrNotFound
}

// CreateVolume panics on AIX (should never be called after validation).
func (t *ThinPool) CreateVolume(_ context.Context, _ string, _ uint64, _ []string, _ uint, _ string, _ []string) error {
	return errors.New("thin pools are not supported on AIX")
}

// Usage returns zeros on AIX.
func (t *ThinPool) Usage(_ context.Context) (*ThinPoolUsage, error) {
	return nil, errors.New("thin pools are not supported on AIX")
}

// LogicalVolume represents a logical volume.
type LogicalVolume struct {
	fullname string
	// name is the TopoLVM UID (or AIX name for non-TopoLVM volumes)
	name     string
	path     string
	vg       *VolumeGroup
	size     uint64
	origin   *string
	pool     *string
	devMajor uint32
	devMinor uint32
	tags     []string
	attr     string
}

// Name returns a volume name.
func (l *LogicalVolume) Name() string { return l.name }

// FullName returns a vg-prefixed volume name.
func (l *LogicalVolume) FullName() string { return l.fullname }

// Path returns the path to the logical volume.
func (l *LogicalVolume) Path() string { return l.path }

// VG returns the volume group.
func (l *LogicalVolume) VG() *VolumeGroup { return l.vg }

// Size returns the size of the volume.
func (l *LogicalVolume) Size() uint64 { return l.size }

// IsSnapshot always returns false on AIX.
func (l *LogicalVolume) IsSnapshot() bool { return false }

// Origin always returns nil on AIX.
func (l *LogicalVolume) Origin(_ context.Context) (*LogicalVolume, error) { return nil, nil }

// IsThin always returns false on AIX.
func (l *LogicalVolume) IsThin() bool { return false }

// Pool always returns nil on AIX.
func (l *LogicalVolume) Pool(_ context.Context) (*ThinPool, error) { return nil, nil }

// MajorNumber returns the device major number.
func (l *LogicalVolume) MajorNumber() uint32 { return l.devMajor }

// MinorNumber returns the device minor number.
func (l *LogicalVolume) MinorNumber() uint32 { return l.devMinor }

// Tags returns the tags of the logical volume.  Always empty on AIX.
func (l *LogicalVolume) Tags() []string { return l.tags }

// Attr returns the synthesised lv_attr string.
func (l *LogicalVolume) Attr() string { return l.attr }

// ThinSnapshot is not supported on AIX.
func (l *LogicalVolume) ThinSnapshot(_ context.Context, _ string, _ []string) error {
	return fmt.Errorf("thin snapshots are not supported on AIX")
}

// Activate changes the LV permission on AIX using `chlv -p r|w <lv>`.
func (l *LogicalVolume) Activate(ctx context.Context, access string) error {
	var perm string
	switch access {
	case "ro":
		perm = "r"
	case "rw":
		perm = "w"
	default:
		return fmt.Errorf("unknown access: %s for LogicalVolume %s", access, l.fullname)
	}
	// The AIX name is the last component of l.path.
	aixName := path.Base(l.path)
	if _, err := runAIXCommand(ctx, cmdChlv, "-p", perm, aixName); err != nil {
		return fmt.Errorf("chlv -p %s %s: %w", perm, aixName, err)
	}
	return nil
}

// Resize grows the logical volume.  Shrinking is not supported.
func (l *LogicalVolume) Resize(ctx context.Context, newSize uint64) error {
	if l.size > newSize {
		return fmt.Errorf("volume cannot be shrunk")
	}
	if l.size == newSize {
		return nil
	}

	ppSizeMiB, err := vgPPSizeMiB(ctx, l.vg.Name())
	if err != nil {
		return fmt.Errorf("Resize: cannot get PP size: %w", err)
	}

	currentLPs := mibToLPs(l.size, ppSizeMiB)
	newLPs := mibToLPs(newSize, ppSizeMiB)
	additionalLPs := newLPs - currentLPs
	if additionalLPs == 0 {
		// Already rounded up to enough PPs.
		return nil
	}

	aixName := path.Base(l.path)
	if _, err := runAIXCommand(ctx, cmdExtendlv, aixName, fmt.Sprintf("%d", additionalLPs)); err != nil {
		return fmt.Errorf("extendlv %s %d: %w", aixName, additionalLPs, err)
	}

	// Refresh size.
	vol, err := l.vg.FindVolume(ctx, l.name)
	if err != nil {
		return err
	}
	l.size = vol.size
	return nil
}

// RemoveVolume removes the named LV from the VG.
func (vg *VolumeGroup) RemoveVolume(ctx context.Context, name string) error {
	// name may be the TopoLVM UID; derive the AIX name.
	aixName := lvNameForUID(name)

	// Confirm the label matches before removing (note 3.2).
	l, err := fetchOneLV(ctx, vg.Name(), aixName)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return errors.Join(ErrNotFound, err)
		}
		return err
	}

	if isTopoLVMVolume(l.lvLabel) && !strings.EqualFold(l.lvLabel, name) {
		// Label names a different volume — refuse (note 3.2).
		return fmt.Errorf("RemoveVolume: LV %s label %q does not match requested %q",
			aixName, l.lvLabel, name)
	}

	if _, err := runAIXCommand(ctx, cmdRmlv, "-f", aixName); err != nil {
		if IsLVMNotFound(err) {
			return errors.Join(ErrNotFound, err)
		}
		return fmt.Errorf("rmlv -f %s: %w", aixName, err)
	}
	return nil
}

// Rename renames the logical volume on AIX using `chlv -n <new> <old>`.
// The LV must be closed (not mounted).
func (l *LogicalVolume) Rename(ctx context.Context, name string) error {
	oldAIXName := path.Base(l.path)
	newAIXName := lvNameForUID(name)
	if _, err := runAIXCommand(ctx, cmdChlv, "-n", newAIXName, oldAIXName); err != nil {
		return fmt.Errorf("chlv -n %s %s: %w", newAIXName, oldAIXName, err)
	}
	l.fullname = fullName(newAIXName, l.vg)
	l.name = name // keep TopoLVM UID as the name
	l.path = path.Join(path.Dir(l.path), newAIXName)
	return nil
}
