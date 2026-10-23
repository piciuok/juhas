// Package effects shows the "kick": a short on-screen lightning effect when
// an urgent PR/MR shows up.
package effects

import "time"

// Duration of the on-screen effect.
const Duration = 2400 * time.Millisecond

// Zap plays the effect (where the platform can) and returns whether it did.
func Zap() bool { return zap(Duration) }
