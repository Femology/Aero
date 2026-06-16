//! WKWebView native shell (macOS) — direct objc/cocoa bindings, no tao/wry.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use cocoa::appkit::{
    NSApp, NSApplication, NSApplicationActivateIgnoringOtherApps, NSBackingStoreBuffered,
    NSWindow, NSWindowStyleMask,
};
use cocoa::base::{id, nil, YES};
use cocoa::foundation::{NSAutoreleasePool, NSRect, NSSize, NSString};
use objc::{class, msg_send, sel, sel_impl};

use super::ShellConfig;
use crate::ipc::{custom_event_script, IpcMultiplexer, PushEvent};

static RUNNING: AtomicBool = AtomicBool::new(true);

pub struct WebViewShell {
    window: id,
    webview: id,
    _ipc: Arc<IpcMultiplexer>,
}

impl WebViewShell {
    pub fn new(
        config: ShellConfig,
        ipc: Arc<IpcMultiplexer>,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        unsafe {
            let _pool = NSAutoreleasePool::new(nil);

            let app = NSApp();
            app.setActivationPolicy_(cocoa::appkit::NSApplicationActivationPolicyRegular);
            app.activateIgnoringOtherApps_(NSApplicationActivateIgnoringOtherApps);

            let rect = NSRect::new(
                cocoa::foundation::NSPoint::new(0., 0.),
                NSSize::new(config.width as f64, config.height as f64),
            );

            let window: id = msg_send![class!(NSWindow), alloc];
            let window: id = msg_send![
                window,
                initWithContentRect: rect
                styleMask: NSWindowStyleMask::NSTitledWindowMask
                    | NSWindowStyleMask::NSClosableWindowMask
                    | NSWindowStyleMask::NSMiniaturizableWindowMask
                    | NSWindowStyleMask::NSResizableWindowMask
                backing: NSBackingStoreBuffered
                defer: NO
            ];
            let title = NSString::alloc(nil).init_str(&config.title);
            let _: () = msg_send![window, setTitle: title];

            let config_obj: id = msg_send![class!(WKWebViewConfiguration), new];
            let webview: id = msg_send![class!(WKWebView), alloc];
            let webview: id = msg_send![webview, initWithFrame: rect configuration: config_obj];
            let _: () = msg_send![window, setContentView: webview];

            let url_str = NSString::alloc(nil).init_str(&config.entry_url);
            let url: id = msg_send![class!(NSURL), URLWithString: url_str];
            let request: id = msg_send![class!(NSURLRequest), requestWithURL: url];
            let _: () = msg_send![webview, loadRequest: request];

            let ipc_close = Arc::clone(&ipc);
            hook_window_close(window, ipc_close);

            let wv_push = webview;
            ipc.on_push(move |event| {
                let script = NSString::alloc(nil).init_str(&custom_event_script(&event));
                let _: () = msg_send![wv_push, evaluateJavaScript: script completionHandler: nil];
            });

            Ok(Self {
                window,
                webview,
                _ipc: ipc,
            })
        }
    }

    pub fn run<F: Fn() -> bool>(&self, should_run: F) -> Result<(), Box<dyn std::error::Error>> {
        unsafe {
            let _: () = msg_send![self.window, makeKeyAndOrderFront: nil];
            let app = NSApp();
            while should_run() && RUNNING.load(Ordering::SeqCst) {
                let mode = NSString::alloc(nil).init_str("NSDefaultRunLoopMode");
                let event: id = msg_send![
                    app,
                    nextEventMatchingMask: u64::MAX
                    untilDate: nil
                    inMode: mode
                    dequeue: YES
                ];
                if event != nil {
                    let _: () = msg_send![app, sendEvent: event];
                }
                let _: () = msg_send![app, updateWindows];
                std::thread::sleep(std::time::Duration::from_millis(8));
            }
        }
        Ok(())
    }
}

unsafe fn hook_window_close(window: id, ipc: Arc<IpcMultiplexer>) {
    let _ = (window, ipc);
    // NSWindowDelegate `windowWillClose:` sends ipc.notify_shutdown() in the ObjC shim.
}
#[link(name = "WebKit", kind = "framework")]
extern "C" {}

#[link(name = "Cocoa", kind = "framework")]
extern "C" {}
