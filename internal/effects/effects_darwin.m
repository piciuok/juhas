#import <Cocoa/Cocoa.h>
#import <QuartzCore/QuartzCore.h>

// JuhasZapView: a yellow glow pulsing along the screen edges and a few
// jagged discharges that re-strike every few frames.
@interface JuhasZapView : NSView
@property double start;
@property double duration;
@property int tick;
@property(strong) NSArray<NSBezierPath *> *bolts;
@end

static double rnd(void) { return (double)arc4random_uniform(10000) / 10000.0; }

// bolt from p towards q, jagged, with an optional branch.
static NSBezierPath *makeBolt(NSPoint p, NSPoint q) {
	NSBezierPath *b = [NSBezierPath bezierPath];
	[b moveToPoint:p];
	int steps = 9 + arc4random_uniform(5);
	double dx = (q.x - p.x) / steps, dy = (q.y - p.y) / steps;
	double len = hypot(q.x - p.x, q.y - p.y);
	for (int i = 1; i <= steps; i++) {
		double jitter = (rnd() - 0.5) * len * 0.12;
		// perpendicular offset
		double nx = -dy, ny = dx, nl = hypot(nx, ny);
		NSPoint next = NSMakePoint(p.x + dx * i + nx / nl * jitter, p.y + dy * i + ny / nl * jitter);
		[b lineToPoint:next];
		if (i == steps / 2 && rnd() < 0.7) { // branch
			NSPoint br = NSMakePoint(next.x + (rnd() - 0.5) * len * 0.5, next.y + len * 0.18 * (dy >= 0 ? 1 : -1));
			[b lineToPoint:br];
			[b moveToPoint:next];
		}
	}
	return b;
}

@implementation JuhasZapView
- (BOOL)isFlipped { return YES; }

- (void)strike {
	NSRect r = self.bounds;
	NSMutableArray *bs = [NSMutableArray array];
	int n = 4 + arc4random_uniform(3);
	for (int i = 0; i < n; i++) {
		NSPoint p, q;
		switch (arc4random_uniform(4)) {
		case 0: // from the top edge
			p = NSMakePoint(r.size.width * rnd(), 0);
			q = NSMakePoint(p.x + (rnd() - 0.5) * r.size.width * 0.4, r.size.height * (0.40 + 0.3 * rnd()));
			break;
		case 1: // from the left edge
			p = NSMakePoint(0, r.size.height * rnd());
			q = NSMakePoint(r.size.width * (0.25 + 0.2 * rnd()), p.y + (rnd() - 0.5) * r.size.height * 0.4);
			break;
		case 2: // from the right edge
			p = NSMakePoint(r.size.width, r.size.height * rnd());
			q = NSMakePoint(r.size.width * (0.55 + 0.2 * rnd()), p.y + (rnd() - 0.5) * r.size.height * 0.4);
			break;
		default: // from the menu bar corner, where the icon sits
			p = NSMakePoint(r.size.width * (0.75 + 0.2 * rnd()), 0);
			q = NSMakePoint(p.x - r.size.width * 0.3 * rnd(), r.size.height * (0.35 + 0.3 * rnd()));
		}
		[bs addObject:makeBolt(p, q)];
	}
	self.bolts = bs;
}

- (void)drawRect:(NSRect)dirty {
	double t = (CACurrentMediaTime() - self.start) / self.duration;
	if (t > 1) t = 1;
	double a = t < 0.08 ? 1.0 : sin(t * M_PI);    // hit hard, then fade
	if (a < 0) a = 0;
	double flick = (self.tick % 2) ? 1.0 : 0.6;    // flicker
	NSRect r = self.bounds;

	// full-screen flashes: at the strike and a second one mid-way
	double f1 = fmax(0, 1 - t / 0.10), f2 = fmax(0, 1 - fabs(t - 0.45) / 0.06);
	double flash = fmax(f1 * 0.45, f2 * 0.28);
	if (flash > 0) {
		[[NSColor colorWithRed:1 green:0.97 blue:0.80 alpha:flash] set];
		NSRectFillUsingOperation(r, NSCompositingOperationSourceOver);
	}

	for (int i = 0; i < 18; i++) {                   // edge glow
		CGFloat w = 8 + i * 9;
		[[NSColor colorWithRed:1 green:0.80 blue:0.0 alpha:a * flick * 0.34 * (1 - i / 18.0)] set];
		NSBezierPath *p = [NSBezierPath bezierPathWithRect:NSInsetRect(r, w / 2, w / 2)];
		p.lineWidth = w;
		[p stroke];
	}
	if (self.tick % 2 == 0 || self.bolts == nil) [self strike];
	for (NSBezierPath *b in self.bolts) {
		b.lineJoinStyle = NSLineJoinStyleRound;
		b.lineCapStyle = NSLineCapStyleRound;
		[[NSColor colorWithRed:1 green:0.80 blue:0.1 alpha:a * flick * 0.45] set];
		b.lineWidth = 26; [b stroke];
		[[NSColor colorWithRed:1 green:0.92 blue:0.45 alpha:a * flick * 0.9] set];
		b.lineWidth = 9; [b stroke];
		[[NSColor colorWithWhite:1 alpha:a * flick] set];
		b.lineWidth = 3.5; [b stroke];
	}
	self.tick++;
}
@end

void juhasZap(double seconds) {
	dispatch_async(dispatch_get_main_queue(), ^{
		for (NSScreen *s in [NSScreen screens]) {
			NSWindow *w = [[NSWindow alloc] initWithContentRect:s.frame
			                                          styleMask:NSWindowStyleMaskBorderless
			                                            backing:NSBackingStoreBuffered
			                                              defer:NO];
			w.releasedWhenClosed = NO;
			w.opaque = NO;
			w.backgroundColor = NSColor.clearColor;
			w.hasShadow = NO;
			w.ignoresMouseEvents = YES;
			w.level = NSScreenSaverWindowLevel;
			w.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorStationary |
			                       NSWindowCollectionBehaviorFullScreenAuxiliary | NSWindowCollectionBehaviorIgnoresCycle;
			[w setFrame:s.frame display:NO];
			JuhasZapView *v = [[JuhasZapView alloc] initWithFrame:NSMakeRect(0, 0, s.frame.size.width, s.frame.size.height)];
			v.start = CACurrentMediaTime();
			v.duration = seconds;
			w.contentView = v;
			[w orderFrontRegardless];
			[NSTimer scheduledTimerWithTimeInterval:1.0 / 30.0 repeats:YES block:^(NSTimer *timer) {
				[v setNeedsDisplay:YES];
				if (CACurrentMediaTime() - v.start > seconds) {
					[timer invalidate];
					[w orderOut:nil];
					[w close];
				}
			}];
		}
	});
}

// juhasZapStandalone runs the effect without the tray (the `juhas zap`
// command): a minimal accessory app whose run loop stops afterwards.
void juhasZapStandalone(double seconds) {
	[NSApplication sharedApplication];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	juhasZap(seconds);
	dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)((seconds + 0.3) * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
		[NSApp stop:nil];
		// stop takes effect after the next event
		[NSApp postEvent:[NSEvent otherEventWithType:NSEventTypeApplicationDefined location:NSZeroPoint modifierFlags:0
		                                   timestamp:0 windowNumber:0 context:nil subtype:0 data1:0 data2:0] atStart:YES];
	});
	[NSApp run];
}
