const statusEl = document.getElementById("status");
const logEl = document.getElementById("log");
const pingBtn = document.getElementById("ping");

function log(msg) {
  logEl.textContent += msg + "\n";
  logEl.scrollTop = logEl.scrollHeight;
}

window.__aero_onPush = (detail) => {
  log(`push ${detail.method}: ${JSON.stringify(detail.params)}`);
};

window.addEventListener("aero:push", (e) => {
  if (typeof window.__aero_onPush === "function") {
    window.__aero_onPush(e.detail);
  }
});

statusEl.textContent = "Aero UI loaded via aero:// protocol";

pingBtn.addEventListener("click", async () => {
  if (typeof window.__aero_invoke !== "function") {
    log("IPC bridge not ready");
    return;
  }
  try {
    const resp = await window.__aero_invoke("demo.ping", { ts: Date.now() });
    log("ping → " + resp);
  } catch (err) {
    log("ping error: " + err);
  }
});
