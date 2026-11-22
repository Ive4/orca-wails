from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import subprocess
import tempfile
import threading


REPO = Path(__file__).resolve().parent.parent
DESTINATION = 'https://example.invalid/'
OPEN_POPUP = f"window.open('{DESTINATION}', '_blank', 'noopener,noreferrer')"
BUTTON = f'<button id="open" onclick="{OPEN_POPUP}">Open</button>'
FIXTURES = {
    'anchor': (f'<a id="open" href="{DESTINATION}" target="_blank">Open</a>', 1),
    'shell': (BUTTON, 1),
    'other-page': (BUTTON, 0),
    'subframe': ('<iframe src="/frame?shell"></iframe>', 0),
    'without-gesture': (f'<script>{OPEN_POPUP}</script>', 0),
    'non-web-url': (BUTTON.replace(DESTINATION, 'file:///tmp/fixture'), 0),
    'with-credentials': (BUTTON.replace(DESTINATION, 'https://user@example.invalid/'), 0),
    'same-frame': (BUTTON.replace(OPEN_POPUP, f"location.assign('{DESTINATION}')"), 0),
}

# Run the production WebKit delegate and record the handoff without launching an app.
PROBE = r'''
static int browserRequests;

static BOOL recordOpenURL(id workspace, SEL selector, NSURL *url) {
    if (![url.absoluteString isEqualToString:@"https://example.invalid/"]) exit(2);
    browserRequests++;
    return YES;
}

static void finishProbe(void) {
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 600 * NSEC_PER_MSEC), dispatch_get_main_queue(), ^{
        printf("%d\n", browserRequests);
        exit(0);
    });
}

@interface LinkProbe : OrcaNavigation
@property(nonatomic, copy) NSString *fixture;
@end

@implementation LinkProbe
- (void)webView:(WKWebView *)view didFinishNavigation:(WKNavigation *)navigation {
    if ([self.fixture isEqualToString:@"without-gesture"]) {
        finishProbe();
        return;
    }
    NSString *document = [self.fixture isEqualToString:@"subframe"] ? @"frames[0].document" : @"document";
    NSString *click = [document stringByAppendingString:@".getElementById('open').click()"];
    // WebKit runs evaluateJavaScript with transient user activation.
    [view evaluateJavaScript:click completionHandler:^(id value, NSError *error) {
        if (error) {
            fprintf(stderr, "%s\n", error.localizedDescription.UTF8String);
            exit(2);
        }
        finishProbe();
    }];
}
@end

int main(int argc, char *argv[]) {
    [NSApplication sharedApplication];
    [NSApp setActivationPolicy:NSApplicationActivationPolicyProhibited];
    method_setImplementation(class_getInstanceMethod(NSWorkspace.class, @selector(openURL:)), (IMP)recordOpenURL);
    WKWebViewConfiguration *configuration = [WKWebViewConfiguration new];
    configuration.websiteDataStore = WKWebsiteDataStore.nonPersistentDataStore;
    configuration.preferences.javaScriptCanOpenWindowsAutomatically = NO;
    WKWebView *view = [[WKWebView alloc] initWithFrame:NSZeroRect configuration:configuration];
    LinkProbe *delegate = [LinkProbe new];
    delegate.origin = [NSURL URLWithString:@(argv[1])];
    delegate.pagePath = @"/web-index.html";
    delegate.fixture = @(argv[2]);
    view.navigationDelegate = delegate;
    view.UIDelegate = delegate;
    NSURLComponents *page = [NSURLComponents componentsWithURL:delegate.origin resolvingAgainstBaseURL:NO];
    page.path = [delegate.fixture isEqualToString:@"other-page"] ? @"/other" : @"/web-index.html";
    page.query = delegate.fixture;
    [view loadRequest:[NSURLRequest requestWithURL:page.URL]];
    [NSApp run];
}
'''


class FixtureHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        fixture = self.path.partition('?')[2]
        body = ('<!doctype html>' + FIXTURES[fixture][0]).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'text/html')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


def main():
    source = (REPO / 'window_darwin.go').read_text().split('/*\n', 1)[1].split('\n*/', 1)[0]
    native = '\n'.join(line for line in source.splitlines() if not line.startswith('#cgo'))
    with tempfile.TemporaryDirectory() as directory, ThreadingHTTPServer(('127.0.0.1', 0), FixtureHandler) as server:
        source_file = Path(directory) / 'probe.m'
        binary = Path(directory) / 'probe'
        source_file.write_text(native + PROBE)
        subprocess.run([
            'xcrun', 'clang', '-Wno-deprecated-declarations', '-I', REPO,
            '-framework', 'AppKit', '-framework', 'WebKit', source_file, '-o', binary,
        ], check=True)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        try:
            origin = f'http://127.0.0.1:{server.server_port}'
            for fixture, (_, expected) in FIXTURES.items():
                count = int(subprocess.check_output([binary, origin, fixture], timeout=15))
                if count != expected:
                    raise AssertionError(f'{fixture}: expected {expected} browser requests, got {count}')
        finally:
            server.shutdown()
            thread.join()
    print('macOS external-link checks passed')


if __name__ == '__main__':
    main()
