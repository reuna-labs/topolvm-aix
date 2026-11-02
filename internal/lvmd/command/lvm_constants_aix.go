//go:build aix

package command

// verbosity constants mirror those in lvm_command.go (Linux).
const (
	verbosityLVMStateUpdate   = 1
	verbosityLVMStateNoUpdate = 4
)
