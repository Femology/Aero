//! Window lifecycle, IPC multiplexing, tray integrations, and graceful shutdown.

mod integrations;
mod ipc;
mod platform;

use std::env;
use std::io;
use std::process;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;

use ipc::IpcMultiplexer;
use platform::{ShellConfig, WebViewShell};

static RUNNING: AtomicBool = AtomicBool::new(true);

fn main() {
    if let Err(err) = run() {
        eprintln!("aero-shell: {err}");
        process::exit(1);
    }
}

fn run() -> Result<(), Box<dyn std::error::Error>> {
    let key_hex = env::var("AERO_HMAC_KEY").map_err(|_| "AERO_HMAC_KEY not set")?;
    let width: u32 = env::var("AERO_WIDTH")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(1024);
    let height: u32 = env::var("AERO_HEIGHT")
        .ok()
        .and_then(|s| s.parse().ok())
        .unwrap_or(768);
    let title = env::var("AERO_TITLE").unwrap_or_else(|_| "Aero".into());
    let entry_url = env::var("AERO_ENTRY_URL").unwrap_or_else(|_| "aero://assets/index.html".into());

    let ipc = IpcMultiplexer::connect(io::stdin(), io::stdout(), &key_hex)?;

    let config = ShellConfig {
        width,
        height,
        title: title.clone(),
        entry_url,
    };

    let shell = WebViewShell::new(config, Arc::clone(&ipc))?;

    let tray_show: Arc<dyn Fn() + Send + Sync> = Arc::new(|| eprintln!("aero: show window"));
    let tray_quit: Arc<dyn Fn() + Send + Sync> =
        Arc::new(|| RUNNING.store(false, Ordering::SeqCst));
    integrations::init_tray(integrations::TrayCallbacks {
        on_show: tray_show,
        on_quit: Arc::clone(&tray_quit),
    })?;

    if let Err(err) = integrations::notify(&title, "Aero shell started") {
        eprintln!("aero-shell: notification skipped: {err}");
    }

    shell.run(|| RUNNING.load(Ordering::SeqCst))?;

    ipc.notify_shutdown()?;
    Ok(())
}
