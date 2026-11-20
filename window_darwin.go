package main

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework AppKit -framework WebKit
#include <stdlib.h>
#include <stdbool.h>
#import <AppKit/AppKit.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>

@protocol OrcaWebviewHost <NSObject>
@property(readonly) WKWebView *webView;
@end

@interface OrcaNavigation : NSObject <WKNavigationDelegate, WKUIDelegate>
@property(nonatomic, strong) NSURL *origin;
@property(nonatomic, strong) id<WKNavigationDelegate, WKUIDelegate> upstream;
@end

@implementation OrcaNavigation

#if !__has_feature(objc_arc)
- (void)dealloc {
    [_origin release];
    [_upstream release];
    [super dealloc];
}
#endif
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
static bool installOrcaWebview(void *pointer, const char *origin, const char *script) {
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

    [view.configuration.userContentController removeScriptMessageHandlerForName:@"external"];
    WKUserScript *userScript = [[WKUserScript alloc]
        initWithSource:[NSString stringWithUTF8String:script]
        injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES];
    [view.configuration.userContentController addUserScript:userScript];
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
	"net/url"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func configureOrcaWindow(_ *application.WebviewWindowOptions, _ string, _ *url.URL) {}

func loadOrcaWindow(window *application.WebviewWindow, page *url.URL, script string, show func()) error {
	origin := C.CString(page.Scheme + "://" + page.Host)
	defer C.free(unsafe.Pointer(origin))
	source := C.CString(script)
	defer C.free(unsafe.Pointer(source))
	if !C.installOrcaWebview(window.NativeWindow(), origin, source) {
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
