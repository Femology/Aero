# Aero Framework

Build lightning-fast, secure desktop apps with Go and HTML. Under 5MB.

An open-source desktop runtime by Femology.

---

## What is Aero?

Imagine you want to build a desktop app. Usually, frameworks (like Electron) force your computer to download a massive, heavy, 150MB copy of Google Chrome just to show your screen. This slows down computers and eats up memory.

**Aero does things differently.**

Every computer already has a native window built-in (Edge on Windows, Safari on Mac, WebKit on Linux). Aero is a magical, ultra-thin piece of glass. It opens the window your computer already has, and connects it securely to a blazing-fast Go brain.

Your HTML/CSS handles how it looks. Your Go code handles the heavy thinking. Aero is the invisible, lightning-fast bridge between them.

---

## Why Use Aero?

- **Incredibly Tiny:** Your final app will be under 5MB. No bloated browsers included.
- **Instant Startup:** Because it uses the computer's native screen, it opens instantly.
- **Zero-Trust Security:** Every single message passed between your UI and your Go engine is cryptographically locked using a 256-bit secret key. Hackers cannot intercept it.
- **One-Click Installers:** Aero automatically packs your app into `.exe` (Windows), `.dmg` (Mac), and `.deb` (Linux) files so anyone can download and install your tool.

---

## Quick Start (Getting Started)

You need Go (1.22+) and Rust (1.83+) installed on your computer.

If you are on Linux, run this first:

```bash
sudo apt install libwebkit2gtk-4.1-dev libgtk-3-dev libappindicator3-dev
```

Open your terminal and run these 3 simple commands:

```bash
# 1. Download the framework
git clone https://github.com/Femology/Aero.git
cd Aero/aero

# 2. Build the engine
make all

# 3. Run the Demo App!
make demo
```

A beautiful desktop window will open. Click **"Ping Go backend"** to test the speed.

---

## How to Build Your Own App

Aero makes it incredibly simple to write apps. You just need a few lines of Go code and a tiny bit of JavaScript.

### 1. The Go Brain (`main.go`)

This is where your logic lives. It creates the window and listens for the UI.

```go
package main

import (
    "context"
    "embed"
    "fmt"
    "log"

    aero "github.com/femology/aero/sdk/go"
)

// Embed your HTML/CSS files directly into the Go app!
//go:embed assets/*
var ui embed.FS

func main() {
    store := aero.NewAssetStore()
    store.LoadEmbedFS(ui, "assets")

    // Create the Desktop Window
    app, _ := aero.NewApp(aero.Config{
        ShellPath: "aero-shell",
        Assets:    store,
        Width:     900,
        Height:    600,
        Title:     "My Awesome App",
    })

    // Listen for the Frontend UI asking a question
    aero.HandleJSON(app, "app.greet", func(ctx context.Context, req struct {
        Name string `json:"name"`
    }) (any, error) {
        return fmt.Sprintf("Hello, %s! Welcome to Aero.", req.Name), nil
    })

    log.Fatal(app.Run(context.Background()))
}
```

### 2. The Frontend UI (JavaScript)

Call your Go brain directly from your JavaScript using the built-in Aero bridge.

```js
// Ask Go to do something and wait for the answer
const result = await window.__aero_invoke("app.greet", { name: "World" });
console.log(result); // Outputs: "Hello, World! Welcome to Aero."

// Listen for events pushed down from Go
window.addEventListener("aero:push", (event) => {
    console.log("Go sent a message!", event.detail);
});
```

---

## Dev Mode (Hot Reloading)

Want to use modern tools like React, Tailwind, or Vite? No problem.

Aero has a built-in Dev Mode. Just point Aero to your local Vite server in your Go config:

```go
app, _ := aero.NewApp(aero.Config{
    ShellPath: "aero-shell",
    DevMode:   true,
    DevOrigin: "http://localhost:5173", // Your React/Vite server!
})
```

Now, every time you save your React code, your desktop app will update instantly.

---

## How to Export Your App

Ready to share your tool with the world? Run this command:

```bash
make pack
```

Aero will compile your Go code, package your HTML, and produce installer files (`.exe`, `.dmg`, `.deb`) in your `dist/` folder.

---

## License

MIT. See the `LICENSE` file for the full text.

## Contact

Open an issue at https://github.com/Femology/Aero/issues
