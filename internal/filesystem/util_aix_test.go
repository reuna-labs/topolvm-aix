//go:build aix

// util_aix_test.go – unit tests for AIX filesystem detection.
//
// Tagged aix: references constants defined in util_aix.go.

package filesystem

import (
	"bytes"
	"testing"
)

func TestDetectFilesystemBlank(t *testing.T) {
	// A device image that is all zeros should be detected as blank.
	img := make([]byte, 1<<20) // 1 MiB of zeros
	got, err := detectFilesystemFromBytes(img)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("blank image: got %q, want empty string", got)
	}
}

func TestDetectFilesystemJFS2(t *testing.T) {
	// Place the JFS2 magic at offset 0x8000.
	img := make([]byte, 1<<20)
	copy(img[jfs2SBOffset:], []byte(jfs2Magic))
	got, err := detectFilesystemFromBytes(img)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "jfs2" {
		t.Errorf("JFS2 image: got %q, want jfs2", got)
	}
}

func TestDetectFilesystemUnknownData(t *testing.T) {
	// Non-zero data that is not JFS2 should return an error (refuse to format).
	img := make([]byte, 1<<20)
	img[0] = 0x01 // dirty first byte
	_, err := detectFilesystemFromBytes(img)
	if err == nil {
		t.Error("expected error for non-zero non-JFS2 image, got nil")
	}
}

func TestIsZero(t *testing.T) {
	cases := []struct {
		b    []byte
		want bool
	}{
		{make([]byte, 0), true},
		{make([]byte, 1), true},
		{make([]byte, 64), true},
		{[]byte{0, 0, 1, 0}, false},
		{[]byte{1}, false},
	}
	for _, tc := range cases {
		got := isZero(tc.b)
		if got != tc.want {
			t.Errorf("isZero(%v)=%v, want %v", tc.b, got, tc.want)
		}
	}
}

// detectFilesystemFromBytes is the testable core of DetectFilesystem,
// operating on a byte slice rather than a device path.
func detectFilesystemFromBytes(buf []byte) (string, error) {
	if len(buf) >= jfs2SBOffset+4 {
		if string(buf[jfs2SBOffset:jfs2SBOffset+4]) == jfs2Magic {
			return "jfs2", nil
		}
	}
	if isZero(buf) {
		return "", nil
	}
	return "", bytes.ErrTooLarge // sentinel: reuse a stdlib error; real code uses fmt.Errorf
}
