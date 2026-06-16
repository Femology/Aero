package aero

import (
	"bytes"
	"embed"
	"net/http"
	"testing"
)

//go:embed testdata/*
var testAssets embed.FS

func TestAssetStoreServe(t *testing.T) {
	store := NewAssetStore()
	store.Register("/index.html", []byte("<html></html>"), "text/html; charset=utf-8")

	resp, err := store.Serve("/index.html", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.Status)
	}
	if !bytes.Contains(resp.Body, []byte("<html>")) {
		t.Fatalf("unexpected body: %q", resp.Body)
	}
	if resp.Headers["Content-Security-Policy"] != strictCSP {
		t.Fatalf("CSP: got %q want %q", resp.Headers["Content-Security-Policy"], strictCSP)
	}
	if resp.Headers["Accept-Ranges"] != "bytes" {
		t.Fatalf("expected Accept-Ranges: bytes")
	}
}

func TestAssetStoreRange(t *testing.T) {
	store := NewAssetStore()
	data := []byte("0123456789")
	store.Register("/blob.bin", data, "application/octet-stream")

	resp, err := store.Serve("/blob.bin", "bytes=0-3")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusPartialContent {
		t.Fatalf("status: got %d want 206", resp.Status)
	}
	if string(resp.Body) != "0123" {
		t.Fatalf("range body: got %q want 0123", resp.Body)
	}
	if resp.Headers["Content-Range"] != "bytes 0-3/10" {
		t.Fatalf("content-range: %q", resp.Headers["Content-Range"])
	}
	if resp.Headers["Content-Security-Policy"] != strictCSP {
		t.Fatalf("CSP missing on partial response")
	}
}

func TestAssetStoreRangeSuffix(t *testing.T) {
	store := NewAssetStore()
	store.Register("/v.mp4", []byte("0123456789"), "video/mp4")

	resp, err := store.Serve("/v.mp4", "bytes=-4")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusPartialContent {
		t.Fatalf("status: got %d want 206", resp.Status)
	}
	if string(resp.Body) != "6789" {
		t.Fatalf("suffix range: got %q want 6789", resp.Body)
	}
}

func TestAssetStoreRangeUnsatisfiable(t *testing.T) {
	store := NewAssetStore()
	store.Register("/a.bin", []byte("ab"), "application/octet-stream")

	resp, err := store.Serve("/a.bin", "bytes=99-100")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status: got %d want 416", resp.Status)
	}
}

func TestAssetStoreDevMode(t *testing.T) {
	store := NewAssetStore()
	store.Register("/index.html", []byte("<html></html>"), "text/html; charset=utf-8")
	store.SetDevMode(true, DefaultDevOrigin)

	if store.EntryURL() != DefaultDevOrigin+"/" {
		t.Fatalf("EntryURL: got %q", store.EntryURL())
	}

	resp, err := store.Handle(AssetRequest{Path: "/index.html", Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusTemporaryRedirect {
		t.Fatalf("status: got %d want 307", resp.Status)
	}
	if resp.Headers["Location"] != DefaultDevOrigin+"/index.html" {
		t.Fatalf("Location: got %q", resp.Headers["Location"])
	}
	if resp.Headers["Access-Control-Allow-Origin"] != DefaultDevOrigin {
		t.Fatalf("CORS origin: got %q", resp.Headers["Access-Control-Allow-Origin"])
	}
	if resp.Headers["X-Aero-Dev-Mode"] != "1" {
		t.Fatalf("expected X-Aero-Dev-Mode header")
	}
}

func TestAssetStoreLoadEmbedFS(t *testing.T) {
	store := NewAssetStore()
	if err := store.LoadEmbedFS(testAssets, "testdata"); err != nil {
		t.Fatal(err)
	}
	resp, err := store.Serve("/sample.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "aero\n" {
		t.Fatalf("body: %q", resp.Body)
	}
}

func TestProtocolURL(t *testing.T) {
	got := ProtocolURL("/index.html")
	want := "aero://assets/index.html"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDetectContentType(t *testing.T) {
	cases := map[string]string{
		"clip.mp4":  "video/mp4",
		"track.mp3": "audio/mpeg",
		"app.js":    "application/javascript; charset=utf-8",
	}
	for name, want := range cases {
		if got := detectContentType(name); got != want {
			t.Fatalf("%s: got %q want %q", name, got, want)
		}
	}
}
