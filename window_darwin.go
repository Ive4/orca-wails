package main

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework AppKit -framework WebKit
#include <stdlib.h>
#include <stdbool.h>
#include <sys/stat.h>
#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

@protocol OrcaWebviewHost <NSObject>
@property(readonly) WKWebView *webView;
@end

@interface OrcaNavigation : NSObject <WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandlerWithReply>
@property(nonatomic, strong) NSURL *origin;
@property(nonatomic, strong) id<WKNavigationDelegate, WKUIDelegate> upstream;
@property(nonatomic, strong) NSWindow *htmlPreview;
@end

@implementation OrcaNavigation

- (void)dealloc {
    if (_htmlPreview) {
        [NSNotificationCenter.defaultCenter removeObserver:self name:NSWindowWillCloseNotification object:_htmlPreview];
        [_htmlPreview close];
    }
#if !__has_feature(objc_arc)
    [_htmlPreview release];
    [_origin release];
    [_upstream release];
    [super dealloc];
#endif
}
- (BOOL)respondsToSelector:(SEL)selector {
    return [super respondsToSelector:selector] || [self.upstream respondsToSelector:selector];
}

- (id)forwardingTargetForSelector:(SEL)selector {
    if ([self.upstream respondsToSelector:selector]) return self.upstream;
    return [super forwardingTargetForSelector:selector];
}

- (BOOL)matchesOrigin:(NSURL *)url {
    return !url.user && !url.password &&
        [url.scheme isEqualToString:self.origin.scheme] &&
        [url.host isEqualToString:self.origin.host] &&
        [url.port isEqualToNumber:self.origin.port];
}

- (BOOL)previewHTMLAtPath:(NSString *)path {
    NSString *extension = path.pathExtension.lowercaseString;
    if (![extension isEqualToString:@"html"] && ![extension isEqualToString:@"htm"]) return NO;
    NSURL *file = [NSURL fileURLWithPath:path isDirectory:NO].URLByResolvingSymlinksInPath;
    const char *filePath = file.fileSystemRepresentation;
    struct stat info;
    if (!filePath || stat(filePath, &info) != 0 || !S_ISREG(info.st_mode)) return NO;

    if (!self.htmlPreview) {
        NSRect frame = NSMakeRect(0, 0, 960, 700);
        NSWindow *window = [[NSWindow alloc] initWithContentRect:frame
            styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
            backing:NSBackingStoreBuffered defer:NO];
        window.releasedWhenClosed = NO;
        [NSNotificationCenter.defaultCenter addObserver:self selector:@selector(previewWindowWillClose:)
            name:NSWindowWillCloseNotification object:window];
        WKWebViewConfiguration *configuration = [WKWebViewConfiguration new];
        configuration.websiteDataStore = [WKWebsiteDataStore nonPersistentDataStore];
        WKWebView *view = [[WKWebView alloc] initWithFrame:frame configuration:configuration];
        window.contentView = view;
        [window center];
        self.htmlPreview = window;
#if !__has_feature(objc_arc)
        [view release];
        [configuration release];
        [window release];
#endif
    }
    self.htmlPreview.title = [path.lastPathComponent stringByAppendingString:@" · Orca"];
    WKWebView *view = (WKWebView *)self.htmlPreview.contentView;
    [view loadFileURL:file allowingReadAccessToURL:file.URLByDeletingLastPathComponent];
    [self.htmlPreview makeKeyAndOrderFront:nil];
    return YES;
}

- (void)previewWindowWillClose:(NSNotification *)notification {
    NSWindow *window = notification.object;
    // Drop the WebView after AppKit finishes closing the window.
    dispatch_async(dispatch_get_main_queue(), ^{
        if (self.htmlPreview == window && !window.isVisible) {
            [NSNotificationCenter.defaultCenter removeObserver:self name:NSWindowWillCloseNotification object:window];
            self.htmlPreview = nil;
        }
    });
}

- (void)userContentController:(WKUserContentController *)controller
        didReceiveScriptMessage:(WKScriptMessage *)message
        replyHandler:(void (^)(id, NSString *))reply {
    WKFrameInfo *frame = message.frameInfo;
    NSURL *page = frame.request.URL;
    WKSecurityOrigin *sender = frame.securityOrigin;
    if (!frame.mainFrame || ![self matchesOrigin:page] ||
        ![page.path isEqualToString:@"/web-index.html"] ||
        ![sender.protocol isEqualToString:self.origin.scheme] ||
        ![sender.host isEqualToString:self.origin.host] || sender.port != self.origin.port.integerValue) {
        reply(@{@"ok": @NO, @"reason": @"remote-runtime-unsupported"}, nil);
        return;
    }
    NSString *path = [message.body isKindOfClass:[NSString class]] ? message.body : nil;
    if (![path hasPrefix:@"/"] || path.length > 32768 ||
        [path rangeOfCharacterFromSet:[NSCharacterSet characterSetWithRange:NSMakeRange(0, 1)]].location != NSNotFound) {
        reply(@{@"ok": @NO, @"reason": @"not-absolute"}, nil);
        return;
    }
    if ([message.name isEqualToString:@"orcaPreviewHTML"]) {
        reply(@([self previewHTMLAtPath:path]), nil);
        return;
    }
    const char *filePath = path.fileSystemRepresentation;
    struct stat info;
    if (!filePath || lstat(filePath, &info) != 0) {
        reply(@{@"ok": @NO, @"reason": @"not-found"}, nil);
        return;
    }
    [[NSWorkspace sharedWorkspace] activateFileViewerSelectingURLs:@[[NSURL fileURLWithPath:path]]];
    reply(@{@"ok": @YES}, nil);
}

- (void)openLink:(WKNavigationAction *)action {
    NSURL *url = action.request.URL;
    if (action.navigationType == WKNavigationTypeLinkActivated && url.host &&
        !url.user && !url.password &&
        ([url.scheme isEqualToString:@"https"] || [url.scheme isEqualToString:@"http"])) {
        [[NSWorkspace sharedWorkspace] openURL:url];
    }
}

- (void)webView:(WKWebView *)view decidePolicyForNavigationAction:(WKNavigationAction *)action
        decisionHandler:(void (^)(WKNavigationActionPolicy))decision {
    if (action.targetFrame && (!action.targetFrame.mainFrame ||
        [self matchesOrigin:action.request.URL] ||
        [action.request.URL.absoluteString isEqualToString:@"about:blank"])) {
        decision(WKNavigationActionPolicyAllow);
        return;
    }
    [self openLink:action];
    decision(WKNavigationActionPolicyCancel);
}

- (WKWebView *)webView:(WKWebView *)view createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
        forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
    [self openLink:action];
    return nil;
}
@end

static char orcaNavigationKey;
static bool installOrcaWebview(void *pointer, const char *origin, const char *script, bool finder) {
    id<OrcaWebviewHost> window = (__bridge id<OrcaWebviewHost>)pointer;
    if (!window || ![window respondsToSelector:@selector(webView)]) return false;
    WKWebView *view = window.webView;
    if (!view) return false;
    OrcaNavigation *delegate = [OrcaNavigation new];
    delegate.origin = [NSURL URLWithString:[NSString stringWithUTF8String:origin]];
    delegate.upstream = (id<WKNavigationDelegate, WKUIDelegate>)view.navigationDelegate;
    objc_setAssociatedObject(view, &orcaNavigationKey, delegate, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
    view.navigationDelegate = delegate;
    view.UIDelegate = delegate;
#if !__has_feature(objc_arc)
    [delegate release];
#endif

    WKUserContentController *controller = view.configuration.userContentController;
    [controller removeScriptMessageHandlerForName:@"external"];
    if (finder) {
        [controller addScriptMessageHandlerWithReply:delegate contentWorld:WKContentWorld.pageWorld name:@"orcaRevealInFinder"];
        [controller addScriptMessageHandlerWithReply:delegate contentWorld:WKContentWorld.pageWorld name:@"orcaPreviewHTML"];
    }
    WKUserScript *userScript = [[WKUserScript alloc]
        initWithSource:[NSString stringWithUTF8String:script]
        injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES];
    [controller addUserScript:userScript];
#if !__has_feature(objc_arc)
    [userScript release];
#endif
    return true;
}

static bool orcaPageLoaded(void *pointer) {
    id<OrcaWebviewHost> window = (__bridge id<OrcaWebviewHost>)pointer;
    WKWebView *view = window.webView;
    OrcaNavigation *delegate = objc_getAssociatedObject(view, &orcaNavigationKey);
    return [delegate matchesOrigin:view.URL] && [view.URL.path isEqualToString:@"/web-index.html"];
}

static void orcaEvaluate(void *pointer, const char *script) {
    if (!orcaPageLoaded(pointer)) return;
    id<OrcaWebviewHost> window = (__bridge id<OrcaWebviewHost>)pointer;
    [window.webView evaluateJavaScript:[NSString stringWithUTF8String:script] completionHandler:nil];
}
*/
import "C"

import (
	"encoding/json"
	"net/url"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func configureOrcaWindow(_ *application.WebviewWindowOptions, _ string, _ *url.URL) {}

func loadOrcaWindow(window *application.WebviewWindow, page *url.URL, script string, show func()) error {
	pageOrigin := page.Scheme + "://" + page.Host
	finder := page.Hostname() == "127.0.0.1"
	if finder {
		originJSON, _ := json.Marshal(pageOrigin)
		script = `if (window.top === window && location.origin === ` + string(originJSON) + ` &&
location.pathname === "/web-index.html") {
  const finder = window.webkit.messageHandlers.orcaRevealInFinder;
  Object.defineProperty(window, "__orcaWailsRevealInFinder", { value: path => finder.postMessage(path) });
  const preview = window.webkit.messageHandlers.orcaPreviewHTML;
  Object.defineProperty(window, "__orcaWailsPreviewHTML", { value: async path => (await preview.postMessage(path)) === true });
}
` + script
	}
	origin := C.CString(pageOrigin)
	defer C.free(unsafe.Pointer(origin))
	source := C.CString(script)
	defer C.free(unsafe.Pointer(source))
	if !C.installOrcaWebview(window.NativeWindow(), origin, source, C.bool(finder)) {
		return browserUnavailable()
	}
	window.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(*application.WindowEvent) {
		application.InvokeSync(func() {
			if native := window.NativeWindow(); native != nil && C.orcaPageLoaded(native) {
				show()
			}
		})
	})
	window.SetURL(page.String())
	return nil
}

func evalOrca(window *application.WebviewWindow, script string) {
	if native := window.NativeWindow(); native != nil {
		source := C.CString(script)
		defer C.free(unsafe.Pointer(source))
		C.orcaEvaluate(native, source)
	}
}

func (d *desktop) handleWindowMessage(application.Window, string, *application.OriginInfo) {}
