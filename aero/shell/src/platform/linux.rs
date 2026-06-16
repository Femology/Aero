//! WebKitGTK native shell (Linux) — direct `-sys` bindings, no tao/wry.

use std::cell::RefCell;
use std::ffi::{CStr, CString};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use glib::translate::FromGlibPtrNone;
use gtk::prelude::*;
use gtk::{Window, Widget};
use webkit2gtk_sys::{
    webkit_uri_scheme_request_finish, webkit_uri_scheme_request_get_uri,
    webkit_web_context_get_default, webkit_web_context_register_uri_scheme,
    webkit_web_view_load_uri, webkit_web_view_new, webkit_web_view_run_javascript,
    WebKitURISchemeRequest, WebKitWebView,
};

use super::{ShellConfig, JsInjector};
use crate::ipc::{custom_event_script, AssetResponse, IpcMultiplexer};

thread_local! {
    static IPC: RefCell<Option<Arc<IpcMultiplexer>>> = RefCell::new(None);
}

static JS_INJECTOR: Mutex<Option<JsInjector>> = Mutex::new(None);
static RUNNING: AtomicBool = AtomicBool::new(true);

pub struct WebViewShell {
    window: Window,
    webview: usize, // kept for future native hooks (show/focus)
}

impl WebViewShell {
    pub fn new(
        config: ShellConfig,
        ipc: Arc<IpcMultiplexer>,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        gtk::init()?;
        register_aero_scheme(Arc::clone(&ipc))?;

        let window = Window::builder()
            .default_width(config.width as i32)
            .default_height(config.height as i32)
            .title(&config.title)
            .build();

        let widget = unsafe { webkit_web_view_new() };
        if widget.is_null() {
            return Err("failed to create WebKitWebView".into());
        }
        let webview = widget as *mut WebKitWebView;
        let webview_addr = webview as usize;

        window.add(&unsafe { Widget::from_glib_none(widget) });

        install_js_injector(webview_addr);

        ipc.on_push(move |event| {
            if let Ok(lock) = JS_INJECTOR.lock() {
                if let Some(inj) = lock.as_ref() {
                    inj(&custom_event_script(&event));
                }
            }
        });

        let ipc_shutdown = Arc::clone(&ipc);
        window.connect_delete_event(move |_, _| {
            RUNNING.store(false, Ordering::SeqCst);
            let _ = ipc_shutdown.notify_shutdown();
            glib::Propagation::Stop
        });

        let entry = CString::new(config.entry_url)?;
        unsafe {
            webkit_web_view_load_uri(webview, entry.as_ptr());
        }

        inject_bridge_script(webview_addr);

        Ok(Self {
            window,
            webview: webview_addr,
        })
    }

    pub fn run<F: Fn() -> bool>(&self, should_run: F) -> Result<(), Box<dyn std::error::Error>> {
        self.window.show_all();
        while should_run() && RUNNING.load(Ordering::SeqCst) {
            while gtk::events_pending() {
                gtk::main_iteration();
            }
            std::thread::sleep(std::time::Duration::from_millis(8));
        }
        Ok(())
    }
}

fn install_js_injector(webview_addr: usize) {
    let injector: JsInjector = Arc::new(move |script: &str| {
        let ctx = glib::MainContext::default();
        let script = script.to_string();
        ctx.invoke(move || {
            inject_js(webview_addr, &script);
        });
    });
    *JS_INJECTOR.lock().unwrap() = Some(injector);
}

fn register_aero_scheme(ipc: Arc<IpcMultiplexer>) -> Result<(), Box<dyn std::error::Error>> {
    IPC.with(|cell| *cell.borrow_mut() = Some(Arc::clone(&ipc)));

    unsafe {
        let context = webkit_web_context_get_default();
        let ipc_ptr = Arc::into_raw(ipc) as glib_sys::gpointer;
        webkit_web_context_register_uri_scheme(
            context,
            c"aero".as_ptr(),
            Some(aero_scheme_callback),
            ipc_ptr,
            Some(release_ipc_ptr),
        );
    }
    Ok(())
}

unsafe extern "C" fn release_ipc_ptr(data: glib_sys::gpointer) {
    drop(Arc::from_raw(data as *const IpcMultiplexer));
}

unsafe extern "C" fn aero_scheme_callback(
    request: *mut WebKitURISchemeRequest,
    _user_data: glib_sys::gpointer,
) {
    if request.is_null() {
        return;
    }

    let uri_ptr = webkit_uri_scheme_request_get_uri(request);
    if uri_ptr.is_null() {
        return;
    }
    let uri = CStr::from_ptr(uri_ptr).to_string_lossy();
    let path = uri
        .strip_prefix("aero://assets")
        .or_else(|| uri.strip_prefix("aero://"))
        .unwrap_or(&uri);

    // Frontend → backend JSON-RPC bridge: aero://invoke/<method>?params=<json>
    if uri.starts_with("aero://invoke/") {
        let method = uri
            .strip_prefix("aero://invoke/")
            .unwrap_or("")
            .split('?')
            .next()
            .unwrap_or("");
        if !method.is_empty() {
            if let Some(resp) = IPC.with(|cell| {
                cell.borrow().as_ref().and_then(|ipc| {
                    ipc.call(method, serde_json::json!({}))
                        .ok()
                        .and_then(|v| serde_json::to_vec(&v).ok())
                })
            }) {
                serve_asset_bytes(request, resp, "application/json");
                return;
            }
        }
    }

    let asset = IPC.with(|cell| {
        cell.borrow()
            .as_ref()
            .and_then(|ipc| ipc.request_asset(path, None).ok())
    });

    match asset {
        Some(resp) => serve_asset(request, resp),
        None => serve_error(request, b"not found", "text/plain"),
    }
}

unsafe fn serve_asset_bytes(request: *mut WebKitURISchemeRequest, body: Vec<u8>, ctype: &str) {
    let len = body.len();
    let stream = memory_stream(&body);
    std::mem::forget(body);
    let ctype = CString::new(ctype).unwrap();
    webkit_uri_scheme_request_finish(request, stream, len as i64, ctype.as_ptr());
}

unsafe fn serve_error(request: *mut WebKitURISchemeRequest, body: &[u8], ctype: &str) {
    let stream = memory_stream(body);
    let ctype = CString::new(ctype).unwrap();
    webkit_uri_scheme_request_finish(
        request,
        stream,
        body.len() as i64,
        ctype.as_ptr(),
    );
}

unsafe fn serve_asset(request: *mut WebKitURISchemeRequest, resp: AssetResponse) {
    let body = resp.body;
    let len = body.len();
    let stream = memory_stream(&body);
    std::mem::forget(body);
    let ctype = CString::new(resp.content_type)
        .unwrap_or_else(|_| CString::new("application/octet-stream").unwrap());
    webkit_uri_scheme_request_finish(request, stream, len as i64, ctype.as_ptr());
}

unsafe fn memory_stream(data: &[u8]) -> *mut gio_sys::GInputStream {
    let bytes = glib_sys::g_bytes_new(data.as_ptr() as *const _, data.len());
    gio_sys::g_memory_input_stream_new_from_bytes(bytes)
}

fn inject_js(webview_addr: usize, script: &str) {
    let webview = webview_addr as *mut WebKitWebView;
    let cscript = match CString::new(script) {
        Ok(s) => s,
        Err(_) => return,
    };
    unsafe {
        webkit_web_view_run_javascript(
            webview,
            cscript.as_ptr(),
            std::ptr::null_mut(),
            None,
            std::ptr::null_mut(),
        );
    }
}

fn inject_bridge_script(webview_addr: usize) {
    let script = r#"
window.__aero_invoke = function(method, params) {
  return fetch('aero://invoke/' + method + '?p=' + encodeURIComponent(JSON.stringify(params || {})))
    .then(r => r.text());
};
window.addEventListener('aero:push', e => {
  if (typeof window.__aero_onPush === 'function') window.__aero_onPush(e.detail);
});
"#;
    glib::timeout_add_local_once(std::time::Duration::from_millis(300), move || {
        inject_js(webview_addr, script);
    });
}
