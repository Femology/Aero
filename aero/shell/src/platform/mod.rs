//! OS-specific webview bindings — WebView2 (Windows), WKWebView (macOS), WebKitGTK (Linux).

use std::sync::Arc;

pub struct ShellConfig {
    pub width: u32,
    pub height: u32,
    pub title: String,
    pub entry_url: String,
}

#[cfg(target_os = "linux")]
#[path = "linux.rs"]
mod imp;

#[cfg(target_os = "macos")]
#[path = "macos.rs"]
mod imp;

#[cfg(windows)]
#[path = "windows.rs"]
mod imp;

#[cfg(not(any(target_os = "linux", target_os = "macos", windows)))]
#[path = "stub.rs"]
mod imp;

pub use imp::WebViewShell;

/// Shared handle for injecting JS push events into the active webview.
pub type JsInjector = Arc<dyn Fn(&str) + Send + Sync>;
