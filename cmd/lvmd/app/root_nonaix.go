//go:build !aix

package app

import (
	"context"

	lvmdTypes "github.com/topolvm/topolvm/pkg/lvmd/types"
)

// runPlatformStartupChecks is a no-op on non-AIX platforms.
func runPlatformStartupChecks(_ context.Context, _ []*lvmdTypes.DeviceClass) error {
	return nil
}
