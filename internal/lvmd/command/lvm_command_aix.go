//go:build aix

// lvm_command_aix.go – AIX shims for the LVM command infrastructure.
//
// On AIX there is no single `lvm` binary; each operation uses a dedicated
// command (mklv, extendlv, rmlv, chlv, lsvg, lslv, …).  The Linux functions
// callLVM/callLVMInto/callLVMStreamed are therefore not used.  They are
// provided as stubs so that any compile-time reference in shared code does not
// break the AIX build.
//
// SetLVMPath and SetLVMCommandPrefix are documented as no-ops on AIX per
// note 3.6 of the design document.

package command

import (
	"context"
	"os"
	"os/exec"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// On AIX, lvmCommandPrefix is not used (there is no single lvm binary).
// The variable is kept so that SetLVMPath/SetLVMCommandPrefix compile.
var lvmCommandPrefix = []string{"/sbin/lvm"}

// SetLVMPath is a no-op on AIX.
func SetLVMPath(_ string) {}

// SetLVMCommandPrefix is a no-op on AIX.
func SetLVMCommandPrefix(_ []string) {}

// runAIXCommand runs the given AIX command and returns its stdout.
// stderr is captured and included in errors.
func runAIXCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	log.FromContext(ctx).V(verbosityLVMStateNoUpdate).Info("invoking AIX command", "cmd", name, "args", args)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = os.Environ()
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, &aixCmdErr{
				cmd:    name,
				args:   args,
				err:    err,
				stderr: ee.Stderr,
			}
		}
		return nil, &aixCmdErr{cmd: name, args: args, err: err}
	}
	return out, nil
}
