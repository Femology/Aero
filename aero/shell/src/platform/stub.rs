//! Headless fallback when building on unsupported targets.

use std::sync::Arc;

use super::ShellConfig;
use crate::ipc::IpcMultiplexer;

pub struct WebViewShell;

impl WebViewShell {
    pub fn new(
        config: ShellConfig,
        ipc: Arc<IpcMultiplexer>,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        eprintln!(
            "aero-shell (headless): {}x{} title={} url={}",
            config.width, config.height, config.title, config.entry_url
        );
        ipc.on_push(|event| {
            eprintln!("aero push: {} {:?}", event.method, event.params);
        });
        Ok(Self)
    }

    pub fn run<F: Fn() -> bool>(&self, should_run: F) -> Result<(), Box<dyn std::error::Error>> {
        while should_run() {
            std::thread::sleep(std::time::Duration::from_millis(100));
        }
        Ok(())
    }
}
