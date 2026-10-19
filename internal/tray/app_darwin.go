//go:build darwin && cgo

package tray

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
#include <sys/sysctl.h>
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

static NSString *const kStayKey = @"stayInForeground";

// Blip positions in the tray icon: radius (points) and angle (degrees).
static const double kBlips[][2] = {{5.4, 35}, {3.2, 150}, {6.0, 245}, {4.4, 320}, {2.4, 85}, {6.2, 195}};
static const int kMaxBlips = sizeof(kBlips) / sizeof(kBlips[0]);

// The status of the menu bar item. The icon shows the status with a badge in a color.
typedef NS_ENUM(NSInteger, RadarStatus) {
	RadarIdle,    // yellow pause: no agent is active
	RadarWorking, // green check mark: agents work and no agent waits
	RadarWaiting, // red stop: an agent waits for your input
};

static void drawBadge(RadarStatus status, NSRect r) {
	NSColor *fill = status == RadarWaiting ? NSColor.systemRedColor : status == RadarWorking ? NSColor.systemGreenColor : NSColor.systemYellowColor;
	[fill setFill];
	[[NSBezierPath bezierPathWithOvalInRect:r] fill];
	CGFloat x = NSMinX(r), y = NSMinY(r), w = NSWidth(r);
	switch (status) {
	case RadarWorking: {
		NSBezierPath *check = [NSBezierPath bezierPath];
		[check moveToPoint:NSMakePoint(x + w * 0.27, y + w * 0.52)];
		[check lineToPoint:NSMakePoint(x + w * 0.44, y + w * 0.34)];
		[check lineToPoint:NSMakePoint(x + w * 0.74, y + w * 0.68)];
		check.lineWidth = 1.5;
		check.lineCapStyle = NSLineCapStyleRound;
		check.lineJoinStyle = NSLineJoinStyleRound;
		[NSColor.whiteColor setStroke];
		[check stroke];
		break;
	}
	case RadarIdle:
		[[NSColor colorWithWhite:0.15 alpha:1] setFill];
		[[NSBezierPath bezierPathWithRoundedRect:NSMakeRect(x + w * 0.31, y + w * 0.28, w * 0.13, w * 0.44) xRadius:0.4 yRadius:0.4] fill];
		[[NSBezierPath bezierPathWithRoundedRect:NSMakeRect(x + w * 0.56, y + w * 0.28, w * 0.13, w * 0.44) xRadius:0.4 yRadius:0.4] fill];
		break;
	case RadarWaiting:
		[NSColor.whiteColor setFill];
		[[NSBezierPath bezierPathWithRoundedRect:NSMakeRect(x + w * 0.3, y + w * 0.3, w * 0.4, w * 0.4) xRadius:0.5 yRadius:0.5] fill];
		break;
	}
}

// radarIcon draws a still radar with one blip for each active agent, and a status badge in the lower-right corner.
// The radar uses the text color of the menu bar, so that it shows in light and dark mode.
static NSImage *radarIcon(RadarStatus status, int blips) {
	NSImage *img = [NSImage imageWithSize:NSMakeSize(20, 18) flipped:NO drawingHandler:^BOOL(NSRect r) {
		NSPoint c = NSMakePoint(9, 9);
		NSColor *ink = NSColor.labelColor;
		NSBezierPath *outer = [NSBezierPath bezierPathWithOvalInRect:NSMakeRect(1.4, 1.4, 15.2, 15.2)];
		outer.lineWidth = 1.3;
		[[ink colorWithAlphaComponent:0.85] setStroke];
		[outer stroke];
		NSBezierPath *inner = [NSBezierPath bezierPathWithOvalInRect:NSMakeRect(5, 5, 8, 8)];
		inner.lineWidth = 0.8;
		[[ink colorWithAlphaComponent:0.45] setStroke];
		[inner stroke];
		NSBezierPath *line = [NSBezierPath bezierPath];
		[line moveToPoint:c];
		[line lineToPoint:NSMakePoint(c.x + 5.2, c.y + 5.2)];
		line.lineWidth = 1.2;
		line.lineCapStyle = NSLineCapStyleRound;
		[[ink colorWithAlphaComponent:0.85] setStroke];
		[line stroke];
		for (int i = 0; i < blips && i < kMaxBlips; i++) {
			double a = kBlips[i][1];
			NSPoint p = NSMakePoint(c.x + kBlips[i][0] * cos(a * M_PI / 180), c.y + kBlips[i][0] * sin(a * M_PI / 180));
			[ink setFill];
			[[NSBezierPath bezierPathWithOvalInRect:NSMakeRect(p.x - 1.2, p.y - 1.2, 2.4, 2.4)] fill];
		}
		[ink setFill];
		[[NSBezierPath bezierPathWithOvalInRect:NSMakeRect(c.x - 1, c.y - 1, 2, 2)] fill];
		// Clear a ring around the badge, so that the badge is separate from the radar.
		NSRect badge = NSMakeRect(10.5, 0, 9.5, 9.5);
		[NSGraphicsContext.currentContext setCompositingOperation:NSCompositingOperationClear];
		[[NSBezierPath bezierPathWithOvalInRect:NSInsetRect(badge, -1.2, -1.2)] fill];
		[NSGraphicsContext.currentContext setCompositingOperation:NSCompositingOperationSourceOver];
		drawBadge(status, badge);
		return YES;
	}];
	img.template = NO;
	img.accessibilityDescription = status == RadarWaiting ? @"Copilot Radar: an agent waits for you" : status == RadarWorking ? @"Copilot Radar: agents work" : @"Copilot Radar: no active agents";
	return img;
}

@interface RadarApp : NSObject <NSApplicationDelegate, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate>
@property(strong) NSURL *url;
@property(strong) NSString *version;
@property BOOL showAtStart;
@property(strong) NSStatusItem *item;
@property(strong) NSWindow *window;
@property(strong) WKWebView *web;
@property(strong) NSMenuItem *statusLine;
@property(strong) NSMenuItem *trayStay;
@property(strong) NSMenuItem *windowStay;
@property(strong) NSButton *pin;
@end

static RadarApp *radar;

static NSMenuItem *menuItem(NSString *title, SEL action, NSString *key, id target) {
	NSMenuItem *m = [[NSMenuItem alloc] initWithTitle:title action:action keyEquivalent:key];
	m.target = target;
	return m;
}

@implementation RadarApp

- (void)applicationDidFinishLaunching:(NSNotification *)note {
	[self buildMainMenu];
	[self buildStatusItem];
	[self buildWindow];
	[self applyStay];
	if (self.showAtStart) [self showWindow:nil];
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)app { return NO; }

- (BOOL)applicationShouldHandleReopen:(NSApplication *)app hasVisibleWindows:(BOOL)visible {
	[self showWindow:nil];
	return YES;
}

- (void)buildMainMenu {
	NSMenu *bar = [NSMenu new];

	NSMenu *appMenu = [[NSMenu alloc] initWithTitle:@"Copilot Radar"];
	[appMenu addItem:menuItem(@"About Copilot Radar", @selector(about:), @"", self)];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItem:menuItem(@"Hide Copilot Radar", @selector(hide:), @"h", NSApp)];
	[appMenu addItem:[NSMenuItem separatorItem]];
	[appMenu addItem:menuItem(@"Quit Copilot Radar", @selector(terminate:), @"q", NSApp)];

	NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
	[edit addItem:menuItem(@"Copy", @selector(copy:), @"c", nil)];
	[edit addItem:menuItem(@"Select All", @selector(selectAll:), @"a", nil)];

	NSMenu *view = [[NSMenu alloc] initWithTitle:@"View"];
	[view addItem:menuItem(@"Reload", @selector(reload:), @"r", self)];

	NSMenu *win = [[NSMenu alloc] initWithTitle:@"Window"];
	self.windowStay = menuItem(@"Stay in Foreground", @selector(toggleStay:), @"t", self);
	self.windowStay.keyEquivalentModifierMask = NSEventModifierFlagCommand | NSEventModifierFlagShift;
	[win addItem:self.windowStay];
	[win addItem:[NSMenuItem separatorItem]];
	[win addItem:menuItem(@"Minimize", @selector(performMiniaturize:), @"m", nil)];
	[win addItem:menuItem(@"Close", @selector(performClose:), @"w", nil)];

	for (NSMenu *m in @[ appMenu, edit, view, win ]) {
		NSMenuItem *holder = [NSMenuItem new];
		holder.submenu = m;
		[bar addItem:holder];
	}
	NSApp.mainMenu = bar;
	NSApp.windowsMenu = win;
}

- (void)buildStatusItem {
	self.item = [[NSStatusBar systemStatusBar] statusItemWithLength:NSVariableStatusItemLength];
	self.item.button.image = radarIcon(RadarIdle, 0);
	self.item.button.font = [NSFont monospacedDigitSystemFontOfSize:NSFont.systemFontSize weight:NSFontWeightMedium];
	self.item.button.imagePosition = NSImageLeft;
	self.item.button.toolTip = @"Copilot Radar";

	NSMenu *menu = [NSMenu new];
	self.statusLine = [[NSMenuItem alloc] initWithTitle:@"No active agents" action:nil keyEquivalent:@""];
	self.statusLine.enabled = NO;
	[menu addItem:self.statusLine];
	[menu addItem:[NSMenuItem separatorItem]];
	[menu addItem:menuItem(@"Show Radar", @selector(showWindow:), @"", self)];
	self.trayStay = menuItem(@"Stay in Foreground", @selector(toggleStay:), @"", self);
	[menu addItem:self.trayStay];
	[menu addItem:menuItem(@"Open in Browser", @selector(openInBrowser:), @"", self)];
	[menu addItem:[NSMenuItem separatorItem]];
	[menu addItem:menuItem(@"Quit Copilot Radar", @selector(terminate:), @"q", NSApp)];
	self.item.menu = menu;
}

- (void)buildWindow {
	NSRect frame = NSMakeRect(0, 0, 1440, 900);
	NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
	self.window = [[NSWindow alloc] initWithContentRect:frame styleMask:style backing:NSBackingStoreBuffered defer:NO];
	self.window.title = @"Copilot Radar";
	self.window.releasedWhenClosed = NO;
	self.window.delegate = self;
	self.window.minSize = NSMakeSize(720, 480);
	self.window.backgroundColor = [NSColor colorWithSRGBRed:0.05 green:0.07 blue:0.09 alpha:1];
	[self.window center];
	[self.window setFrameAutosaveName:@"CopilotRadarWindow"];

	WKWebViewConfiguration *cfg = [WKWebViewConfiguration new];
	self.web = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
	self.web.navigationDelegate = self;
	self.web.UIDelegate = self;
	[self.web setValue:@NO forKey:@"drawsBackground"];
	self.window.contentView = self.web;
	[self.web loadRequest:[NSURLRequest requestWithURL:self.url]];

	self.pin = [NSButton buttonWithImage:[NSImage imageWithSystemSymbolName:@"pin" accessibilityDescription:@"Stay in Foreground"]
	                              target:self
	                              action:@selector(toggleStay:)];
	self.pin.bordered = NO;
	self.pin.frame = NSMakeRect(0, 0, 32, 22);
	NSTitlebarAccessoryViewController *acc = [NSTitlebarAccessoryViewController new];
	acc.view = self.pin;
	acc.layoutAttribute = NSLayoutAttributeRight;
	[self.window addTitlebarAccessoryViewController:acc];
}

- (BOOL)stay { return [[NSUserDefaults standardUserDefaults] boolForKey:kStayKey]; }

- (void)toggleStay:(id)sender {
	[[NSUserDefaults standardUserDefaults] setBool:!self.stay forKey:kStayKey];
	[self applyStay];
}

- (void)applyStay {
	BOOL on = self.stay;
	self.window.level = on ? NSFloatingWindowLevel : NSNormalWindowLevel;
	self.window.collectionBehavior = on ? (NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary)
	                                    : NSWindowCollectionBehaviorDefault;
	NSControlStateValue state = on ? NSControlStateValueOn : NSControlStateValueOff;
	self.trayStay.state = state;
	self.windowStay.state = state;
	NSString *tip = on ? @"Stay in Foreground: on" : @"Stay in Foreground: off";
	self.pin.image = [NSImage imageWithSystemSymbolName:(on ? @"pin.fill" : @"pin") accessibilityDescription:tip];
	self.pin.contentTintColor = on ? NSColor.controlAccentColor : nil;
	self.pin.toolTip = tip;
}

- (void)showWindow:(id)sender {
	[NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
	[self.window makeKeyAndOrderFront:nil];
	[NSApp activateIgnoringOtherApps:YES];
}

// Closing the window hides it. The app stays in the menu bar and leaves the Dock.
- (BOOL)windowShouldClose:(NSWindow *)sender {
	[self.window orderOut:nil];
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
	return NO;
}

- (void)reload:(id)sender { [self.web reload]; }

- (void)openInBrowser:(id)sender { [[NSWorkspace sharedWorkspace] openURL:self.url]; }

- (void)about:(id)sender {
	[NSApp orderFrontStandardAboutPanelWithOptions:@{
		NSAboutPanelOptionApplicationName : @"Copilot Radar",
		NSAboutPanelOptionApplicationVersion : self.version,
		NSAboutPanelOptionVersion : @"",
	}];
}

// Links to other hosts open in the default browser, not in the radar window.
- (void)webView:(WKWebView *)web decidePolicyForNavigationAction:(WKNavigationAction *)action decisionHandler:(void (^)(WKNavigationActionPolicy))handler {
	NSURL *u = action.request.URL;
	BOOL local = [u.host isEqualToString:self.url.host] && [u.port isEqual:self.url.port];
	if (!local && action.navigationType == WKNavigationTypeLinkActivated) {
		[[NSWorkspace sharedWorkspace] openURL:u];
		handler(WKNavigationActionPolicyCancel);
		return;
	}
	handler(WKNavigationActionPolicyAllow);
}

- (WKWebView *)webView:(WKWebView *)web createWebViewWithConfiguration:(WKWebViewConfiguration *)cfg forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
	if (action.request.URL) [[NSWorkspace sharedWorkspace] openURL:action.request.URL];
	return nil;
}

// sectionHeader makes a menu section title. macOS 13 has no section headers, so the app then uses a disabled item.
static NSMenuItem *sectionHeader(NSString *title) {
	if (@available(macOS 14.0, *)) return [NSMenuItem sectionHeaderWithTitle:title];
	NSMenuItem *m = [[NSMenuItem alloc] initWithTitle:title action:nil keyEquivalent:@""];
	m.enabled = NO;
	return m;
}

// The menu shows the models in use below the status line. The model items have the tag kModelTag.
static const NSInteger kModelTag = 7;

- (void)showModels:(NSArray<NSString *> *)models {
	NSMenu *menu = self.item.menu;
	for (NSMenuItem *m in [menu.itemArray copy]) {
		if (m.tag == kModelTag) [menu removeItem:m];
	}
	if (models.count == 0) return;
	NSInteger at = [menu indexOfItem:self.statusLine] + 1;
	NSMenuItem *sep = [NSMenuItem separatorItem];
	sep.tag = kModelTag;
	[menu insertItem:sep atIndex:at++];
	NSMenuItem *head = sectionHeader(@"Models in use");
	head.tag = kModelTag;
	[menu insertItem:head atIndex:at++];
	for (NSString *line in models) {
		NSMenuItem *m = [[NSMenuItem alloc] initWithTitle:line action:nil keyEquivalent:@""];
		m.image = [NSImage imageWithSystemSymbolName:@"cpu" accessibilityDescription:nil];
		m.enabled = NO;
		m.tag = kModelTag;
		[menu insertItem:m atIndex:at++];
	}
}

// The menu shows the sessions that wait for the user directly below the status line.
// The items have the tag kWaitTag. A click on an item shows the app of the session.
static const NSInteger kWaitTag = 8;

- (void)showWaits:(NSArray<NSString *> *)waits {
	NSMenu *menu = self.item.menu;
	for (NSMenuItem *m in [menu.itemArray copy]) {
		if (m.tag == kWaitTag) [menu removeItem:m];
	}
	if (waits.count == 0) return;
	NSInteger at = [menu indexOfItem:self.statusLine] + 1;
	NSMenuItem *sep = [NSMenuItem separatorItem];
	sep.tag = kWaitTag;
	[menu insertItem:sep atIndex:at++];
	NSMenuItem *head = sectionHeader(@"Waiting for you");
	head.tag = kWaitTag;
	[menu insertItem:head atIndex:at++];
	for (NSString *rec in waits) {
		NSArray<NSString *> *f = [rec componentsSeparatedByString:@"\t"];
		if (f.count < 5) continue;
		NSMenuItem *m = [[NSMenuItem alloc] initWithTitle:f[0] action:@selector(jump:) keyEquivalent:@""];
		m.target = self;
		m.representedObject = f;
		m.image = [NSImage imageWithSystemSymbolName:@"hand.raised.fill" accessibilityDescription:nil];
		m.toolTip = @"Show the app of this session";
		m.tag = kWaitTag;
		[menu insertItem:m atIndex:at++];
	}
}

static pid_t parentOf(pid_t pid) {
	struct kinfo_proc kp;
	size_t len = sizeof(kp);
	int mib[4] = {CTL_KERN, KERN_PROC, KERN_PROC_PID, pid};
	if (sysctl(mib, 4, &kp, &len, NULL, 0) != 0 || len == 0) return 0;
	return kp.kp_eproc.e_ppid;
}

static void bringToFront(NSRunningApplication *app) {
	if (@available(macOS 14.0, *)) {
		[app activateFromApplication:NSRunningApplication.currentApplication options:NSApplicationActivateAllWindows];
	} else {
		[app activateWithOptions:NSApplicationActivateAllWindows];
	}
}

// appOf finds the app that shows a process. It walks up the parent processes,
// for example from the Copilot CLI to the shell and then to Terminal.
static NSRunningApplication *appOf(pid_t pid) {
	for (int i = 0; pid > 1 && i < 32; i++) {
		NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
		if (app && app.activationPolicy == NSApplicationActivationPolicyRegular) return app;
		pid = parentOf(pid);
	}
	return nil;
}

// jump shows the session of a menu item. The fields are: title, URL, PID, folder, and bundle ID.
- (void)jump:(NSMenuItem *)sender {
	NSArray<NSString *> *f = sender.representedObject;
	NSWorkspace *ws = NSWorkspace.sharedWorkspace;
	if (f[1].length) {
		NSURL *u = [NSURL URLWithString:f[1]];
		if (u && [ws openURL:u]) return;
	}
	if (f[4].length) {
		NSURL *appURL = [ws URLForApplicationWithBundleIdentifier:f[4]];
		if (appURL) {
			NSWorkspaceOpenConfiguration *cfg = [NSWorkspaceOpenConfiguration configuration];
			cfg.activates = YES;
			if (f[3].length) {
				// The editor shows the window that has the folder open.
				[ws openURLs:@[[NSURL fileURLWithPath:f[3]]] withApplicationAtURL:appURL configuration:cfg completionHandler:nil];
			} else {
				[ws openApplicationAtURL:appURL configuration:cfg completionHandler:nil];
			}
			return;
		}
	}
	NSRunningApplication *app = appOf((pid_t)f[2].intValue);
	if (app) {
		bringToFront(app);
		return;
	}
	[self showWindow:nil];
}

- (void)setWorking:(int)working waiting:(int)waiting models:(NSArray<NSString *> *)models waits:(NSArray<NSString *> *)waits {
	int active = working + waiting;
	self.item.button.image = radarIcon(waiting > 0 ? RadarWaiting : working > 0 ? RadarWorking : RadarIdle, active);
	[self showModels:models];
	[self showWaits:waits];
	self.item.button.title = active > 0 ? [NSString stringWithFormat:@" %d", active] : @"";
	NSString *line = active == 0 ? @"No active agents" : [NSString stringWithFormat:@"%d working · %d waiting for you", working, waiting];
	self.statusLine.title = line;
	self.item.button.toolTip = [@"Copilot Radar — " stringByAppendingString:line];
}

@end

static void radarRun(const char *url, const char *version, int show) {
	@autoreleasepool {
		[NSApplication sharedApplication];
		radar = [RadarApp new];
		radar.url = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
		radar.version = [NSString stringWithUTF8String:version];
		radar.showAtStart = show != 0;
		NSApp.delegate = radar;
		[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
		[NSApp run];
	}
}

static NSArray<NSString *> *linesOf(const char *s) {
	NSString *all = [NSString stringWithUTF8String:s];
	return all.length ? [all componentsSeparatedByString:@"\n"] : @[];
}

// models and waits are lists of lines, separated by newlines. The function copies them before it returns.
static void radarSetStatus(int working, int waiting, const char *models, const char *waits) {
	NSArray<NSString *> *m = linesOf(models), *w = linesOf(waits);
	dispatch_async(dispatch_get_main_queue(), ^{
		[radar setWorking:working waiting:waiting models:m waits:w];
	});
}

static void radarQuit(void) {
	dispatch_async(dispatch_get_main_queue(), ^{
		[NSApp terminate:nil];
	});
}
*/
import "C"

import (
	"context"
	"runtime"
	"strings"
	"unsafe"
)

const Supported = true

// Cocoa must run on the main thread. main runs on the main thread only if init locks it.
func init() { runtime.LockOSThread() }

func setTray(state []byte) {
	st := statusOf(state)
	lines := make([]string, len(st.models))
	for i, m := range st.models {
		lines[i] = m.String()
	}
	waits := make([]string, len(st.waits))
	for i, w := range st.waits {
		waits[i] = w.record()
	}
	cs := C.CString(strings.Join(lines, "\n"))
	defer C.free(unsafe.Pointer(cs))
	cw := C.CString(strings.Join(waits, "\n"))
	defer C.free(unsafe.Pointer(cw))
	C.radarSetStatus(C.int(st.working), C.int(st.waiting), cs, cw)
}

// Run shows the menu bar item and the radar window. It returns only when the app quits.
func Run(ctx context.Context, opt Options, h Source) {
	go func() {
		ch := h.Subscribe()
		defer h.Unsubscribe(ch)
		if b := h.State(); b != nil {
			setTray(b)
		}
		for {
			select {
			case <-ctx.Done():
				C.radarQuit()
				return
			case b := <-ch:
				setTray(b)
			}
		}
	}()
	cu, cv := C.CString(opt.URL), C.CString(opt.Version)
	defer C.free(unsafe.Pointer(cu))
	defer C.free(unsafe.Pointer(cv))
	show := C.int(0)
	if opt.ShowWindow {
		show = 1
	}
	C.radarRun(cu, cv, show)
}
