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
@property(nonatomic, copy) NSString *pagePath;
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
    [_pagePath release];
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
        (self.origin.port ? [url.port isEqualToNumber:self.origin.port] : url.port == nil);
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
        ![page.path isEqualToString:self.pagePath] ||
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

- (void)webView:(WKWebView *)view didStartProvisionalNavigation:(WKNavigation *)navigation {
    if ([self.upstream respondsToSelector:_cmd]) {
        [self.upstream webView:view didStartProvisionalNavigation:navigation];
    }
}

- (void)openExternalURL:(NSURL *)url {
    if (url.host.length > 0 && !url.user && !url.password &&
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
    if (action.navigationType == WKNavigationTypeLinkActivated) {
        [self openExternalURL:action.request.URL];
    }
    decision(WKNavigationActionPolicyCancel);
}

- (WKWebView *)webView:(WKWebView *)view createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
        forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
    // Orca's shell actions call window.open from the main page with a WebKit user gesture.
    WKFrameInfo *source = action.sourceFrame;
    NSURL *page = source.request.URL;
    if (action.navigationType == WKNavigationTypeLinkActivated ||
        (action.navigationType == WKNavigationTypeOther && source.mainFrame &&
         [self matchesOrigin:page] && [page.path isEqualToString:self.pagePath])) {
        [self openExternalURL:action.request.URL];
    }
    return nil;
}
@end

static char orcaNavigationKey;
static BOOL (*webViewAcceptsFirstMouse)(id, SEL, NSEvent *);

static BOOL orcaAcceptsFirstMouse(id view, SEL selector, NSEvent *event) {
    if (event.type == NSEventTypeLeftMouseDown && objc_getAssociatedObject(view, &orcaNavigationKey)) {
        return YES;
    }
    return webViewAcceptsFirstMouse(view, selector, event);
}

static bool installOrcaWebview(void *pointer, const char *origin, const char *path, const char *script, bool finder) {
    id<OrcaWebviewHost> window = (__bridge id<OrcaWebviewHost>)pointer;
    if (!window || ![window respondsToSelector:@selector(webView)]) return false;
    WKWebView *view = window.webView;
    if (!view) return false;
    // Opt the main WebView into first-click delivery while keeping Wails' KVO subclass.
    static dispatch_once_t firstMouseInstalled;
    dispatch_once(&firstMouseInstalled, ^{
        SEL selector = @selector(acceptsFirstMouse:);
        Method method = class_getInstanceMethod(WKWebView.class, selector);
        webViewAcceptsFirstMouse = (BOOL (*)(id, SEL, NSEvent *))method_getImplementation(method);
        class_replaceMethod(WKWebView.class, selector, (IMP)orcaAcceptsFirstMouse, method_getTypeEncoding(method));
    });
    OrcaNavigation *delegate = [OrcaNavigation new];
    delegate.origin = [NSURL URLWithString:[NSString stringWithUTF8String:origin]];
    delegate.pagePath = [NSString stringWithUTF8String:path];
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
    return [delegate matchesOrigin:view.URL] && [view.URL.path isEqualToString:delegate.pagePath];
}

static void orcaEvaluate(void *pointer, const char *script) {
    if (!orcaPageLoaded(pointer)) return;
    id<OrcaWebviewHost> window = (__bridge id<OrcaWebviewHost>)pointer;
    [window.webView evaluateJavaScript:[NSString stringWithUTF8String:script] completionHandler:nil];
}

// Read the previous origin's storage using an inert document and the same
// website data store that was used by the main window.
@interface OrcaLegacyStorage : NSObject <WKNavigationDelegate>
@property(nonatomic, strong) WKWebView *source;
@property(nonatomic, strong) WKWebView *target;
@property(nonatomic, strong) NSURL *page;
@property(nonatomic, copy) NSString *exportScript;
@property(nonatomic) BOOL finished;
@end

@implementation OrcaLegacyStorage
- (void)dealloc {
    [_source release];
    [_target release];
    [_page release];
    [_exportScript release];
    [super dealloc];
}

- (void)finish:(NSString *)snapshot {
    if (self.finished) return;
    self.finished = YES;
    self.source.navigationDelegate = nil;
    [self.source stopLoading];
    self.source = nil;
    NSData *data = [snapshot dataUsingEncoding:NSUTF8StringEncoding];
    if (data.length && data.length <= (2 << 20)) {
        NSString *encoded = [data base64EncodedStringWithOptions:0];
        NSString *script = [NSString stringWithFormat:
            @"(() => { if (location.protocol + '//' + location.host !== 'wails://localhost' || location.pathname !== '/desktop/index.html') return;"
             "try { const marker = 'orca.wails.desktopStorageMigrated.v1';"
             "if (localStorage.getItem(marker)) return;"
             "const bytes = Uint8Array.from(atob('%@'), c => c.charCodeAt(0));"
             "const snapshot = JSON.parse(new TextDecoder().decode(bytes));"
             "for (const [key, value] of Object.entries(snapshot))"
             "if (localStorage.getItem(key) === null) localStorage.setItem(key, value);"
             "localStorage.setItem(marker, '1'); } catch {} })();", encoded];
        WKUserScript *import = [[[WKUserScript alloc] initWithSource:script
            injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES] autorelease];
        [self.target.configuration.userContentController addUserScript:import];
    }
    [self.target loadRequest:[NSURLRequest requestWithURL:self.page]];
    self.target = nil;
}

- (void)webView:(WKWebView *)view didFinishNavigation:(WKNavigation *)navigation {
    [view evaluateJavaScript:self.exportScript completionHandler:^(id result, NSError *error) {
        [self finish:!error && [result isKindOfClass:NSString.class] ? result : nil];
    }];
}
@end

static void orcaLoadDesktopWithLegacyStorage(void *pointer, const char *page,
                                            unsigned short port, const char *script) {
    id<OrcaWebviewHost> window = (__bridge id<OrcaWebviewHost>)pointer;
    OrcaLegacyStorage *migration = [[[OrcaLegacyStorage alloc] init] autorelease];
    migration.target = window.webView;
    migration.page = [NSURL URLWithString:[NSString stringWithUTF8String:page]];
    migration.exportScript = [NSString stringWithUTF8String:script];
    WKWebViewConfiguration *config = [[[WKWebViewConfiguration alloc] init] autorelease];
    config.websiteDataStore = migration.target.configuration.websiteDataStore;
    migration.source = [[[WKWebView alloc] initWithFrame:NSZeroRect configuration:config] autorelease];
    migration.source.navigationDelegate = migration;
    NSString *origin = [NSString stringWithFormat:@"http://127.0.0.1:%hu/web-index.html", port];
    [migration.source loadHTMLString:@"<!doctype html><meta charset=utf-8><title>Orca</title>"
                             baseURL:[NSURL URLWithString:origin]];
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC), dispatch_get_main_queue(), ^{
        [migration finish:nil];
    });
}
*/
import "C"

import (
	_ "embed"
	"encoding/json"
	"net/url"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed assets/desktop-storage-migration.js
var desktopStorageExport string

func configureOrcaWindow(options *application.WebviewWindowOptions, _ string, _ *url.URL) {
	options.Mac.WebviewPreferences.JavaScriptCanOpenWindowsAutomatically = application.Disabled
}

func loadOrcaWindow(window *application.WebviewWindow, page *url.URL, script string, legacy legacyDesktopStorage, show func()) error {
	pageOrigin := page.Scheme + "://" + page.Host
	finder := page.Hostname() == "127.0.0.1" || page.Path == desktopIndex
	if finder {
		originJSON, _ := json.Marshal(pageOrigin)
		pathJSON, _ := json.Marshal(page.Path)
		script = `if (window.top === window && location.protocol + '//' + location.host === ` + string(originJSON) + ` &&
location.pathname === ` + string(pathJSON) + `) {
  const finder = window.webkit.messageHandlers.orcaRevealInFinder;
  Object.defineProperty(window, "__orcaWailsRevealInFinder", { value: path => finder.postMessage(path) });
  const preview = window.webkit.messageHandlers.orcaPreviewHTML;
  Object.defineProperty(window, "__orcaWailsPreviewHTML", { value: async path => (await preview.postMessage(path)) === true });
}
` + script
	}
	origin := C.CString(pageOrigin)
	defer C.free(unsafe.Pointer(origin))
	path := C.CString(page.Path)
	defer C.free(unsafe.Pointer(path))
	source := C.CString(script)
	defer C.free(unsafe.Pointer(source))
	if !C.installOrcaWebview(window.NativeWindow(), origin, path, source, C.bool(finder)) {
		return browserUnavailable()
	}
	window.OnWindowEvent(events.Mac.WebViewDidFinishNavigation, func(*application.WindowEvent) {
		application.InvokeSync(func() {
			if native := window.NativeWindow(); native != nil && C.orcaPageLoaded(native) {
				show()
			}
		})
	})
	if legacy.Port == 0 || (legacy.InstallationID == "" && legacy.PublicKey == "") {
		window.SetURL(page.String())
	} else {
		identity, _ := json.Marshal(legacy)
		pageURL := C.CString(page.String())
		export := C.CString(desktopStorageExport + "(" + string(identity) + ");")
		defer C.free(unsafe.Pointer(pageURL))
		defer C.free(unsafe.Pointer(export))
		C.orcaLoadDesktopWithLegacyStorage(window.NativeWindow(), pageURL, C.ushort(legacy.Port), export)
	}
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
