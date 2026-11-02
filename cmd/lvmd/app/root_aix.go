//go:build aix

// root_aix.go – AIX-specific startup hooks for lvmd.
//
// This file is compiled on AIX only.  It provides runAIXStartupChecks(), which
// is called from subMain() via the build-tagged hook below.

package app

import (
	"context"

	"github.com/topolvm/topolvm/internal/lvmd"
	lvmdTypes "github.com/topolvm/topolvm/pkg/lvmd/types"
)

// runPlatformStartupChecks performs AIX-specific startup validation:
//   1. Thin device classes are refused (no thin pools on AIX).
//   2. Original volume groups are refused.
func runPlatformStartupChecks(ctx context.Context, deviceClasses []*lvmdTypes.DeviceClass) error {
	return lvmd.ValidateDeviceClassesAIX(ctx, deviceClasses)
}
