//go:build !linux

package tunnelhealth

import (
	"context"
	"errors"
	"time"
)

// DialProbe needs SO_BINDTODEVICE, which only Linux has; every production
// node is Linux. Elsewhere (a developer's machine) it reports that instead
// of guessing, and tests inject their own Probe.
func DialProbe(ctx context.Context, iface string) (time.Duration, error) {
	return 0, errors.New("interface probing is only supported on linux")
}
