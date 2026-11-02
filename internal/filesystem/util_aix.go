//go:build aix

// util_aix.go – filesystem detection and OS calls for the AIX node service.
//
// Key differences from Linux:
//
//   - There is no blkid on AIX.  Filesystem detection uses the JFS2
//     superblock magic "J2SB" at offset 0x8000 (note 3.3).
//
//   - AIX's golang.org/x/sys/unix.Statfs_t has Bsize but no Frsize (note 2.10).
//     We use Bsize for block-size calculations.
//
//   - Mknod on AIX creates device nodes with integer device numbers that
//     encode major/minor; use unix.Mknod.
//
// Safe-format rules (note 3.3):
//   - An LV whose first MiB is all-zero is considered blank: format it.
//   - An LV with "J2SB" at offset 0x8000 has a JFS2 filesystem: mount it.
//   - Anything else (non-zero, non-JFS2) is refused.

package filesystem

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// jfs2Magic is the 4-byte magic at offset 0x8000 that identifies a JFS2
// filesystem superblock.
const (
	jfs2SBOffset = 0x8000 // 32768 bytes
	jfs2Magic    = "J2SB"
)

// DetectFilesystem returns "jfs2" if the device has a JFS2 filesystem, "" if
// it is blank (first 1 MiB is all zero), or an error if it carries
// unrecognised data (refusing to format, note 3.3).
func DetectFilesystem(device string) (string, error) {
	f, err := os.Open(device)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return "", err
	}

	// Read enough to cover the JFS2 superblock location (0x8000 = 32768) plus
	// 4 bytes of magic, and also the first 1 MiB for the blank check.
	const readLen = 1 << 20 // 1 MiB
	buf := make([]byte, readLen)
	if _, err := io.ReadFull(f, buf); err != nil && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("read %s: %w", device, err)
	}

	// Check JFS2 superblock magic.
	if len(buf) >= jfs2SBOffset+4 {
		magic := string(buf[jfs2SBOffset : jfs2SBOffset+4])
		if magic == jfs2Magic {
			return "jfs2", nil
		}
	}

	// Check whether head is blank (all zeros).
	if isZero(buf) {
		return "", nil
	}

	// The device has data that is neither JFS2 nor blank.  Refuse to format.
	return "", fmt.Errorf("device %s: head is non-zero and not JFS2; refusing to format (use fsck -n -V jfs2 to inspect)", device)
}

// isZero returns true if all bytes in b are 0.
func isZero(b []byte) bool {
	// Check in 8-byte chunks for speed.
	for len(b) >= 8 {
		if binary.LittleEndian.Uint64(b) != 0 {
			return false
		}
		b = b[8:]
	}
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

type temporaryer interface {
	Temporary() bool
}

// Stat wraps unix.Stat to handle EINTR.
func Stat(path string, stat *unix.Stat_t) error {
	for {
		err := unix.Stat(path, stat)
		if err == nil {
			return nil
		}
		if e, ok := err.(temporaryer); ok && e.Temporary() {
			continue
		}
		return err
	}
}

// Mknod wraps unix.Mknod to handle EINTR.
func Mknod(path string, mode uint32, dev int) error {
	for {
		err := unix.Mknod(path, mode, dev)
		if err == nil {
			return nil
		}
		if e, ok := err.(temporaryer); ok && e.Temporary() {
			continue
		}
		return err
	}
}

// Statfs wraps unix.Statfs to handle EINTR.
func Statfs(path string, buf *unix.Statfs_t) error {
	for {
		err := unix.Statfs(path, buf)
		if err == nil {
			return nil
		}
		if e, ok := err.(temporaryer); ok && e.Temporary() {
			continue
		}
		return err
	}
}

// BlockSize returns the filesystem block size from a Statfs_t.
// On AIX, Statfs_t has Bsize but no Frsize (note 2.10).  JFS2 reports
// f_blocks in f_bsize units, confirmed by comparing df output.
func BlockSize(sfs *unix.Statfs_t) int64 {
	return int64(sfs.Bsize)
}
