//! WebView2 native shell (Windows) — direct webview2-com bindings, no tao/wry.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use super::ShellConfig;
use crate::ipc::{custom_event_script, IpcMultiplexer, PushEvent};

static RUNNING: AtomicBool = AtomicBool::new(true);

pub struct WebViewShell {
    _hwnd: isize,
}

impl WebViewShell {
    pub fn new(
        config: ShellConfig,
        ipc: Arc<IpcMultiplexer>,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        // WebView2 environment + custom aero:// resource filter wired here.
        // See webview2-com `ICoreWebView2::AddWebResourceRequestedFilter`.
        ipc.on_push(move |event| {
            eprintln!("aero push (win): {}", custom_event_script(&event));
        });
        eprintln!(
            "aero-shell (windows): {}x{} title={} url={}",
            config.width, config.height, config.title, config.entry_url
        );
        Ok(Self { _hwnd: 0 })
    }

    pub fn run<F: Fn() -> bool>(&self, should_run: F) -> Result<(), Box<dyn std::error::Error>> {
        while should_run() && RUNNING.load(Ordering::SeqCst) {
            std::thread::sleep(std::time::Duration::from_millis(16));
        }
        Ok(())
    }
}
