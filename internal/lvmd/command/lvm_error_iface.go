package command

// LVMError is an error that wraps the original error and the stderr output of
// the lvm command if found.  It also provides an exit code if present that can
// be used to determine the type of error from LVM.
// Regular inaccessible errors will have an exit code of 5.
//
// This interface is satisfied by the Linux lvmErr (in lvm_error.go) and by the
// AIX aixCmdErr (in lvm_error_aix.go).
type LVMError interface {
	error
	ExitCode() int
	Unwrap() error
}
