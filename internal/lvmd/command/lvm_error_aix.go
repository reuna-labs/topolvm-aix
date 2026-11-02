//go:build aix

package command

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
)

// AIX "not found" message from getlvodm (0516-306).
var aixNotFoundPattern = regexp.MustCompile(`0516-306`)

// aixCmdErr wraps an error from an AIX LVM command.
type aixCmdErr struct {
	cmd    string
	args   []string
	err    error
	stderr []byte
}

func (e *aixCmdErr) Error() string {
	if len(e.stderr) > 0 {
		return fmt.Sprintf("%s %v: %v: %s", e.cmd, e.args, e.err, string(bytes.TrimSpace(e.stderr)))
	}
	return fmt.Sprintf("%s %v: %v", e.cmd, e.args, e.err)
}

func (e *aixCmdErr) Unwrap() error { return e.err }

func (e *aixCmdErr) ExitCode() int {
	type exitCoder interface {
		ExitCode() int
	}
	var ec exitCoder
	if errors.As(e.err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// IsLVMNotFound returns true when the AIX command reported that the
// requested VG or LV does not exist (getlvodm 0516-306).
func IsLVMNotFound(err error) bool {
	if err == nil {
		return false
	}
	var ce *aixCmdErr
	if !errors.As(err, &ce) {
		return false
	}
	return aixNotFoundPattern.Match([]byte(ce.Error()))
}

// AsLVMError returns the LVMError from an error if it is an *aixCmdErr.
func AsLVMError(err error) (LVMError, bool) {
	var ce *aixCmdErr
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
