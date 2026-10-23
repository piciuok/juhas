package effects

import "time"

// zap: a screen overlay is not possible under Wayland, so Linux relies on
// the critical notification and the tray animation instead.
func zap(time.Duration) bool { return false }

// Demo has nothing to show on Linux.
func Demo() bool { return false }
