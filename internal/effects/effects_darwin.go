package effects

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework QuartzCore
void juhasZap(double seconds);
void juhasZapStandalone(double seconds);
*/
import "C"

import (
	"runtime"
	"time"
)

// Cocoa must run on the main thread; keep the main goroutine there.
func init() { runtime.LockOSThread() }

// zap draws over every screen in a transparent, click-through window. It is
// dispatched to the main thread, where the systray run loop lives.
func zap(d time.Duration) bool {
	C.juhasZap(C.double(d.Seconds()))
	return true
}

// Demo plays the effect from a command (no tray running) and returns after it.
func Demo() bool {
	C.juhasZapStandalone(C.double(Duration.Seconds()))
	return true
}
