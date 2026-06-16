//! Length-prefixed framing, HMAC-SHA256 handshake, and bidirectional JSON-RPC multiplexing.
//!
//! Wire format: `[u32 BE length][payload]` — prevents newline injection attacks.
//! Security: 256-bit HMAC key via `AERO_HMAC_KEY` environment variable (hex).
//! Routing: requests carry `id`; push events omit `id` and dispatch `CustomEvent`s.

use crossbeam_channel::{bounded, Sender};
use hmac::{Hmac, Mac};
use rand::RngCore;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use sha2::Sha256;
use std::collections::HashMap;
use std::io::{self, Read, Write};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;

type HmacSha256 = Hmac<Sha256>;

/// Maximum frame payload size (16 MiB).
pub const MAX_FRAME: u32 = 16 << 20;
/// Length-prefix header size in bytes.
pub const HEADER_SIZE: usize = 4;
/// Required HMAC key length in bytes (256 bits).
pub const HMAC_KEY_BYTES: usize = 32;

/// JSON-RPC 2.0 parse error.
pub const ERR_PARSE: i32 = -32700;
/// JSON-RPC 2.0 invalid request.
pub const ERR_INVALID: i32 = -32600;
/// JSON-RPC 2.0 method not found.
pub const ERR_NOT_FOUND: i32 = -32601;
/// JSON-RPC 2.0 internal error.
pub const ERR_INTERNAL: i32 = -32603;

const HMAC_ACK: &[u8] = b"AERO-OK";

type RpcResult = Result<Value, RpcError>;
type ResponseSender = Sender<RpcResult>;

/// Structured JSON-RPC error returned from `call`.
#[derive(Debug, Clone)]
pub struct RpcError {
    pub code: i32,
    pub message: String,
}

impl std::fmt::Display for RpcError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "RPC {}: {}", self.code, self.message)
    }
}

impl std::error::Error for RpcError {}

/// Unsolicited push from the Go engine to the shell / frontend.
#[derive(Debug, Clone)]
pub struct PushEvent {
    pub method: String,
    pub params: Value,
}

#[derive(Debug, Serialize, Deserialize)]
struct RpcRequest {
    jsonrpc: String,
    #[serde(default)]
    id: Option<Value>,
    method: Option<String>,
    params: Option<Value>,
}

#[derive(Debug, Serialize, Deserialize)]
struct RpcResponse {
    jsonrpc: String,
    id: Option<Value>,
    result: Option<Value>,
    error: Option<RpcErrorBody>,
}

#[derive(Debug, Serialize, Deserialize)]
struct RpcErrorBody {
    code: i32,
    message: String,
}

#[derive(Debug, Serialize, Deserialize)]
struct PushMessage {
    jsonrpc: String,
    method: String,
    params: Value,
}

#[derive(Debug, Serialize, Deserialize)]
struct AssetParams {
    path: String,
    #[serde(default)]
    range: Option<String>,
}

/// Byte payload returned by the Go asset store for `aero://` requests.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct AssetResponse {
    pub status: u16,
    pub headers: std::collections::HashMap<String, String>,
    pub body: Vec<u8>,
    #[serde(rename = "contentType")]
    pub content_type: String,
}

/// Thread-safe IPC multiplexer with pending request correlation and push dispatch.
pub struct IpcMultiplexer {
    writer: Mutex<Box<dyn Write + Send>>,
    pending: Arc<Mutex<HashMap<u64, ResponseSender>>>,
    next_id: AtomicU64,
    on_push: Arc<Mutex<Option<Arc<dyn Fn(PushEvent) + Send + Sync>>>>,
}

impl IpcMultiplexer {
    /// Connects to the Go engine, completes the HMAC handshake, and starts the async reader.
    pub fn connect<R: Read + Send + 'static, W: Write + Send + 'static>(
        reader: R,
        writer: W,
        key_hex: &str,
    ) -> io::Result<Arc<Self>> {
        let key = decode_hmac_key(key_hex)?;

        let mut reader = reader;
        let mut writer = writer;
        client_handshake(&mut reader, &mut writer, &key)?;

        let pending = Arc::new(Mutex::new(HashMap::<u64, ResponseSender>::new()));
        let on_push: Arc<Mutex<Option<Arc<dyn Fn(PushEvent) + Send + Sync>>>> =
            Arc::new(Mutex::new(None));

        let mux = Arc::new(Self {
            writer: Mutex::new(Box::new(writer)),
            pending: Arc::clone(&pending),
            next_id: AtomicU64::new(1),
            on_push: Arc::clone(&on_push),
        });

        thread::Builder::new()
            .name("aero-ipc-reader".into())
            .spawn(move || read_loop(reader, pending, on_push))
            .map_err(|e| io::Error::new(io::ErrorKind::Other, e))?;

        Ok(mux)
    }

    /// Registers a handler for unsolicited Go push events (no JSON-RPC id).
    pub fn on_push<F>(&self, handler: F)
    where
        F: Fn(PushEvent) + Send + Sync + 'static,
    {
        *self.on_push.lock().unwrap() = Some(Arc::new(handler));
    }

    /// Registers the default push handler that injects `window.dispatchEvent` into the webview.
    pub fn on_push_custom_events<F>(&self, inject_js: F)
    where
        F: Fn(&str) + Send + Sync + 'static,
    {
        self.on_push(move |event| {
            inject_js(&custom_event_script(&event));
        });
    }

    /// Issues a JSON-RPC 2.0 request and blocks until the correlated response arrives.
    pub fn call(&self, method: &str, params: Value) -> Result<Value, RpcError> {
        let id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let (tx, rx) = bounded(1);
        self.pending.lock().unwrap().insert(id, tx);

        let req = RpcRequest {
            jsonrpc: "2.0".into(),
            id: Some(Value::from(id)),
            method: Some(method.to_string()),
            params: Some(params),
        };

        let payload = serde_json::to_vec(&req).map_err(|e| RpcError {
            code: ERR_INTERNAL,
            message: e.to_string(),
        })?;
        self.write_frame(&payload).map_err(|e| RpcError {
            code: ERR_INTERNAL,
            message: e,
        })?;

        rx.recv().map_err(|_| RpcError {
            code: ERR_INTERNAL,
            message: "IPC response channel closed".into(),
        })?
    }

    /// Fetches an asset from the Go engine for the `aero://` protocol handler.
    pub fn request_asset(&self, path: &str, range: Option<&str>) -> Result<AssetResponse, RpcError> {
        let params = AssetParams {
            path: path.to_string(),
            range: range.map(str::to_string),
        };
        let result = self.call(
            "aero.asset",
            serde_json::to_value(&params).map_err(|e| RpcError {
                code: ERR_INTERNAL,
                message: e.to_string(),
            })?,
        )?;
        serde_json::from_value(result).map_err(|e| RpcError {
            code: ERR_INTERNAL,
            message: e.to_string(),
        })
    }

    /// Sends a one-way shutdown notification (no id — no response expected).
    pub fn notify_shutdown(&self) -> Result<(), RpcError> {
        let req = RpcRequest {
            jsonrpc: "2.0".into(),
            id: None,
            method: Some("aero.shutdown".into()),
            params: None,
        };
        let payload = serde_json::to_vec(&req).map_err(|e| RpcError {
            code: ERR_INTERNAL,
            message: e.to_string(),
        })?;
        self.write_frame(&payload).map_err(|e| RpcError {
            code: ERR_INTERNAL,
            message: e,
        })
    }

    fn write_frame(&self, payload: &[u8]) -> Result<(), String> {
        let mut w = self.writer.lock().unwrap();
        write_frame(&mut *w, payload).map_err(|e| e.to_string())
    }
}

/// Builds JS that dispatches a `CustomEvent` for a Go push message.
pub fn custom_event_script(event: &PushEvent) -> String {
    let detail = serde_json::json!({
        "method": event.method,
        "params": event.params,
    });
    format!("window.dispatchEvent(new CustomEvent('aero:push', {{detail: {detail}}}));")
}

fn read_loop<R: Read>(
    mut reader: R,
    pending: Arc<Mutex<HashMap<u64, ResponseSender>>>,
    on_push: Arc<Mutex<Option<Arc<dyn Fn(PushEvent) + Send + Sync>>>>,
) {
    loop {
        let frame = match read_frame(&mut reader) {
            Ok(f) => f,
            Err(_) => break,
        };
        route_inbound_frame(&frame, &pending, &on_push);
    }
}

fn route_inbound_frame(
    frame: &[u8],
    pending: &Arc<Mutex<HashMap<u64, ResponseSender>>>,
    on_push: &Arc<Mutex<Option<Arc<dyn Fn(PushEvent) + Send + Sync>>>>,
) {
    let msg: Value = match serde_json::from_slice(frame) {
        Ok(v) => v,
        Err(_) => return,
    };

    // JSON-RPC response (has id + result or error, no method).
    if msg.get("method").is_none() && msg.get("id").is_some() {
        if let Ok(resp) = serde_json::from_value::<RpcResponse>(msg) {
            if let Some(id) = resp.id.and_then(value_to_u64) {
                if let Some(tx) = pending.lock().unwrap().remove(&id) {
                    let result = match (resp.result, resp.error) {
                        (Some(r), _) => Ok(r),
                        (_, Some(e)) => Err(RpcError {
                            code: e.code,
                            message: e.message,
                        }),
                        _ => Err(RpcError {
                            code: ERR_INTERNAL,
                            message: "empty RPC response".into(),
                        }),
                    };
                    let _ = tx.send(result);
                }
            }
        }
        return;
    }

    // Push event from Go: has method, omits id.
    if msg.get("id").is_none() {
        if let Ok(push) = serde_json::from_value::<PushMessage>(msg) {
            if let Some(handler) = on_push.lock().unwrap().clone() {
                handler(PushEvent {
                    method: push.method,
                    params: push.params,
                });
            }
        }
    }
}

fn value_to_u64(v: Value) -> Option<u64> {
    match v {
        Value::Number(n) => n.as_u64(),
        _ => None,
    }
}

/// Writes a 4-byte big-endian length prefix followed by payload.
pub fn write_frame<W: Write>(w: &mut W, payload: &[u8]) -> io::Result<()> {
    if payload.len() as u32 > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "frame too large",
        ));
    }
    let len = (payload.len() as u32).to_be_bytes();
    w.write_all(&len)?;
    w.write_all(payload)?;
    w.flush()
}

/// Reads a length-prefixed frame from r.
pub fn read_frame<R: Read>(r: &mut R) -> io::Result<Vec<u8>> {
    let mut hdr = [0u8; HEADER_SIZE];
    r.read_exact(&mut hdr)?;
    let n = u32::from_be_bytes(hdr);
    if n > MAX_FRAME {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "frame too large",
        ));
    }
    let mut buf = vec![0u8; n as usize];
    r.read_exact(&mut buf)?;
    Ok(buf)
}

fn decode_hmac_key(key_hex: &str) -> io::Result<Vec<u8>> {
    let key = hex::decode(key_hex).map_err(|e| io::Error::new(io::ErrorKind::InvalidData, e))?;
    if key.len() != HMAC_KEY_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "HMAC key must be 256 bits",
        ));
    }
    Ok(key)
}

fn sign(key: &[u8], msg: &[u8]) -> Vec<u8> {
    let mut mac = HmacSha256::new_from_slice(key).expect("HMAC key length");
    mac.update(msg);
    mac.finalize().into_bytes().to_vec()
}

fn client_handshake<R: Read, W: Write>(
    reader: &mut R,
    writer: &mut W,
    key: &[u8],
) -> io::Result<()> {
    let challenge = read_frame(reader)?;
    if challenge.len() != HMAC_KEY_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "invalid handshake challenge",
        ));
    }
    write_frame(writer, &sign(key, &challenge))?;
    let ack = read_frame(reader)?;
    if ack != HMAC_ACK {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "HMAC handshake failed",
        ));
    }
    Ok(())
}

/// Server-side handshake (used in tests).
#[allow(dead_code)]
pub fn server_handshake<R: Read, W: Write>(
    reader: &mut R,
    writer: &mut W,
    key: &[u8],
) -> io::Result<()> {
    if key.len() != HMAC_KEY_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "HMAC key must be 256 bits",
        ));
    }
    let mut challenge = [0u8; HMAC_KEY_BYTES];
    rand::thread_rng().fill_bytes(&mut challenge);
    write_frame(writer, &challenge)?;
    let response = read_frame(reader)?;
    if response != sign(key, &challenge) {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "HMAC handshake failed",
        ));
    }
    write_frame(writer, HMAC_ACK)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::{Read, Write};

    struct MemPipe {
        inner: Arc<Mutex<Vec<u8>>>,
    }

    impl MemPipe {
        fn pair() -> (MemPipe, MemPipe) {
            let buf = Arc::new(Mutex::new(Vec::new()));
            (MemPipe { inner: Arc::clone(&buf) }, MemPipe { inner: buf })
        }
    }

    impl Write for MemPipe {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            self.inner.lock().unwrap().extend_from_slice(buf);
            Ok(buf.len())
        }
        fn flush(&mut self) -> io::Result<()> {
            Ok(())
        }
    }

    impl Read for MemPipe {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            let mut inner = self.inner.lock().unwrap();
            if inner.is_empty() {
                return Ok(0);
            }
            let n = buf.len().min(inner.len());
            buf[..n].copy_from_slice(&inner[..n]);
            inner.drain(..n);
            Ok(n)
        }
    }

    #[test]
    fn framing_roundtrip() {
        let payload = br#"{"jsonrpc":"2.0","method":"ping"}"#;
        let mut buf = Vec::new();
        write_frame(&mut buf, payload).unwrap();
        assert_eq!(&buf[..4], &(payload.len() as u32).to_be_bytes());
        let mut cursor = io::Cursor::new(buf);
        let got = read_frame(&mut cursor).unwrap();
        assert_eq!(got, payload);
    }

    #[test]
    fn custom_event_script_format() {
        let script = custom_event_script(&PushEvent {
            method: "tick".into(),
            params: serde_json::json!({"n": 1}),
        });
        assert!(script.contains("CustomEvent('aero:push'"));
        assert!(script.contains("tick"));
    }
}
