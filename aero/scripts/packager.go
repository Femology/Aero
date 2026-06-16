// Command packager orchestrates cross-compilation of the Go controller and Rust shell,
// writes release configuration files, and invokes platform installers (.exe / .dmg / .deb).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type manifest struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	GoOS        string    `json:"goos"`
	GoArch      string    `json:"goarch"`
	AppBinary   string    `json:"appBinary"`
	ShellBinary string    `json:"shellBinary"`
	BuiltAt     time.Time `json:"builtAt"`
}

func main() {
	root := flag.String("root", "..", "Aero repository root")
	appDir := flag.String("app", "../examples/demo", "Go application module directory")
	out := flag.String("out", "../dist", "output directory")
	goos := flag.String("os", runtime.GOOS, "target GOOS")
	goarch := flag.String("arch", runtime.GOARCH, "target GOARCH")
	appName := flag.String("name", "AeroApp", "application display name")
	appBin := flag.String("bin", "", "application binary name (default: slug of --name)")
	version := flag.String("version", "0.1.0", "release version")
	skipShell := flag.Bool("skip-shell", false, "skip Rust shell build (use existing binary in out dir)")
	flag.Parse()

	if *appBin == "" {
		*appBin = slug(*appName)
	}

	if err := run(runConfig{
		root:      *root,
		appDir:    *appDir,
		out:       *out,
		goos:      *goos,
		goarch:    *goarch,
		appName:   *appName,
		appBin:    *appBin,
		version:   *version,
		skipShell: *skipShell,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "packager: %v\n", err)
		os.Exit(1)
	}
}

type runConfig struct {
	root, appDir, out, goos, goarch, appName, appBin, version string
	skipShell                                                   bool
}

func run(cfg runConfig) error {
	root, err := filepath.Abs(cfg.root)
	if err != nil {
		return err
	}
	appDir, err := filepath.Abs(cfg.appDir)
	if err != nil {
		return err
	}
	out, err := filepath.Abs(cfg.out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	appPath := filepath.Join(out, cfg.appBin+exeSuffix(cfg.goos))
	shellPath := filepath.Join(out, "aero-shell"+exeSuffix(cfg.goos))

	fmt.Printf("packager: cross-compiling Go app (%s/%s) -> %s\n", cfg.goos, cfg.goarch, appPath)
	if err := buildGoApp(appDir, appPath, cfg.goos, cfg.goarch); err != nil {
		return err
	}

	if cfg.skipShell {
		if _, err := os.Stat(shellPath); err != nil {
			return fmt.Errorf("shell binary not found at %s (--skip-shell set)", shellPath)
		}
		fmt.Printf("packager: using existing shell binary at %s\n", shellPath)
	} else {
		fmt.Printf("packager: building Rust shell (%s/%s) -> %s\n", cfg.goos, cfg.goarch, shellPath)
		if err := buildRustShell(filepath.Join(root, "shell"), shellPath, cfg.goos, cfg.goarch); err != nil {
			return err
		}
	}

	if err := writeManifest(out, manifest{
		Name:        cfg.appName,
		Version:     cfg.version,
		GoOS:        cfg.goos,
		GoArch:      cfg.goarch,
		AppBinary:   filepath.Base(appPath),
		ShellBinary: filepath.Base(shellPath),
		BuiltAt:     time.Now().UTC(),
	}); err != nil {
		return err
	}

	if err := writeLauncher(out, cfg, appPath, shellPath); err != nil {
		return err
	}

	switch cfg.goos {
	case "windows":
		return packNSIS(out, cfg, appPath, shellPath)
	case "darwin":
		return packDMG(out, cfg, appPath, shellPath)
	case "linux":
		return packDeb(out, cfg, appPath, shellPath)
	default:
		return fmt.Errorf("unsupported target OS: %s", cfg.goos)
	}
}

func buildGoApp(appDir, outPath, goos, goarch string) error {
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", outPath, ".")
	cmd.Dir = appDir
	cmd.Env = append(os.Environ(),
		"GOOS="+goos,
		"GOARCH="+goarch,
		"CGO_ENABLED=0",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildRustShell(shellDir, outPath, goos, goarch string) error {
	args := []string{"build", "--release"}
	if triple := cargoTargetTriple(goos, goarch); triple != "" {
		args = append(args, "--target", triple)
	}

	cmd := exec.Command("cargo", args...)
	cmd.Dir = shellDir
	cmd.Env = append(os.Environ(), "CARGO_TERM_COLOR=never")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cargo build: %w", err)
	}

	var built string
	if triple := cargoTargetTriple(goos, goarch); triple != "" {
		built = filepath.Join(shellDir, "target", triple, "release", "aero-shell"+exeSuffix(goos))
	} else {
		built = filepath.Join(shellDir, "target", "release", "aero-shell"+exeSuffix(goos))
	}

	if err := copyFile(built, outPath); err != nil {
		return fmt.Errorf("copy shell binary: %w", err)
	}
	return nil
}

func writeManifest(out string, m manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(out, "manifest.json")
	fmt.Printf("packager: wrote %s\n", path)
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func writeLauncher(out string, cfg runConfig, appPath, shellPath string) error {
	launcher := filepath.Join(out, "launch.sh")
	if cfg.goos == "windows" {
		launcher = filepath.Join(out, "launch.bat")
		body := fmt.Sprintf("@echo off\r\nset AERO_SHELL=%~dp0%s\r\n\"%~dp0%s\"\r\n",
			filepath.Base(shellPath), filepath.Base(appPath))
		fmt.Printf("packager: wrote %s\n", launcher)
		return os.WriteFile(launcher, []byte(body), 0o644)
	}

	body := fmt.Sprintf("#!/bin/sh\nexport AERO_SHELL=\"$(dirname \"$0\")/%s\"\nexec \"$(dirname \"$0\")/%s\" \"$@\"\n",
		filepath.Base(shellPath), filepath.Base(appPath))
	fmt.Printf("packager: wrote %s\n", launcher)
	if err := os.WriteFile(launcher, []byte(body), 0o755); err != nil {
		return err
	}
	return nil
}

func packNSIS(out string, cfg runConfig, appPath, shellPath string) error {
	script := filepath.Join(out, "installer.nsi")
	installerOut := filepath.Join(out, cfg.appName+"-"+cfg.version+"-"+cfg.goarch+".exe")

	body := fmt.Sprintf(`!include "MUI2.nsh"

!define APP_NAME "%[1]s"
!define APP_VERSION "%[2]s"
!define APP_PUBLISHER "Aero"
!define APP_EXE "%[3]s"
!define SHELL_EXE "%[4]s"

Name "${APP_NAME}"
OutFile "%[5]s"
InstallDir "$PROGRAMFILES64\\${APP_NAME}"
RequestExecutionLevel user

!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

Section "Install"
  SetOutPath "$INSTDIR"
  File "%[6]s"
  File "%[7]s"
  WriteUninstaller "$INSTDIR\\Uninstall.exe"
  CreateShortCut "$DESKTOP\\${APP_NAME}.lnk" "$INSTDIR\\${APP_EXE}" "" "$INSTDIR\\${APP_EXE}" 0
SectionEnd

Section "Uninstall"
  Delete "$INSTDIR\\${APP_EXE}"
  Delete "$INSTDIR\\${SHELL_EXE}"
  Delete "$INSTDIR\\Uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$DESKTOP\\${APP_NAME}.lnk"
SectionEnd
`, cfg.appName, cfg.version, filepath.Base(appPath), filepath.Base(shellPath),
		installerOut, appPath, shellPath)

	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Printf("packager: wrote NSIS script %s\n", script)

	if _, err := exec.LookPath("makensis"); err != nil {
		fmt.Printf("packager: makensis not found; staged NSIS script only\n")
		return nil
	}
	cmd := exec.Command("makensis", script)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("makensis: %w", err)
	}
	fmt.Printf("packager: installer -> %s\n", installerOut)
	return nil
}

func packDMG(out string, cfg runConfig, appPath, shellPath string) error {
	appBundle := filepath.Join(out, cfg.appName+".app")
	contents := filepath.Join(appBundle, "Contents")
	macosDir := filepath.Join(contents, "MacOS")
	resourcesDir := filepath.Join(contents, "Resources")

	for _, dir := range []string{macosDir, resourcesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if err := copyFile(shellPath, filepath.Join(macosDir, "aero-shell")); err != nil {
		return err
	}
	if err := copyFile(appPath, filepath.Join(macosDir, cfg.appBin)); err != nil {
		return err
	}

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>%s</string>
  <key>CFBundleIdentifier</key><string>dev.aero.%s</string>
  <key>CFBundleName</key><string>%s</string>
  <key>CFBundleVersion</key><string>%s</string>
  <key>CFBundleShortVersionString</key><string>%s</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
</dict>
</plist>
`, cfg.appBin, slug(cfg.appName), cfg.appName, cfg.version, cfg.version)

	plistPath := filepath.Join(contents, "Info.plist")
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	fmt.Printf("packager: wrote %s\n", plistPath)

	dmgPath := filepath.Join(out, cfg.appName+"-"+cfg.version+".dmg")
	if _, err := exec.LookPath("hdiutil"); err != nil {
		fmt.Printf("packager: hdiutil not found; staged .app bundle at %s\n", appBundle)
		return nil
	}
	cmd := exec.Command("hdiutil", "create", "-volname", cfg.appName, "-srcfolder", appBundle, "-ov", "-format", "UDZO", dmgPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hdiutil: %w", err)
	}
	fmt.Printf("packager: installer -> %s\n", dmgPath)
	return nil
}

func packDeb(out string, cfg runConfig, appPath, shellPath string) error {
	staging := filepath.Join(out, "deb-root")
	binDir := filepath.Join(staging, "usr", "bin")
	shareDir := filepath.Join(staging, "usr", "share", slug(cfg.appName))

	for _, dir := range []string{binDir, shareDir, filepath.Join(staging, "DEBIAN")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if err := copyFile(appPath, filepath.Join(binDir, cfg.appBin)); err != nil {
		return err
	}
	if err := copyFile(shellPath, filepath.Join(binDir, "aero-shell")); err != nil {
		return err
	}

	desktop := fmt.Sprintf(`[Desktop Entry]
Name=%s
Exec=%s
Type=Application
Categories=Utility;
Version=%s
`, cfg.appName, cfg.appBin, cfg.version)
	if err := os.WriteFile(filepath.Join(shareDir, cfg.appName+".desktop"), []byte(desktop), 0o644); err != nil {
		return err
	}

	debArch := debArch(cfg.goarch)
	control := fmt.Sprintf(`Package: %s
Version: %s
Architecture: %s
Maintainer: Aero <dev@aero.local>
Description: %s desktop application
Depends: libwebkit2gtk-4.1-0, libgtk-3-0
`, slug(cfg.appName), cfg.version, debArch, cfg.appName)
	if err := os.WriteFile(filepath.Join(staging, "DEBIAN", "control"), []byte(control), 0o644); err != nil {
		return err
	}
	fmt.Printf("packager: wrote deb control (%s)\n", filepath.Join(staging, "DEBIAN", "control"))

	debOut := filepath.Join(out, slug(cfg.appName)+"_"+cfg.version+"_"+debArch+".deb")
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		fmt.Printf("packager: dpkg-deb not found; staged deb root at %s\n", staging)
		return nil
	}
	cmd := exec.Command("dpkg-deb", "--root-owner-group", "--build", staging, debOut)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("dpkg-deb: %w", err)
	}
	fmt.Printf("packager: installer -> %s\n", debOut)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o755)
	if strings.HasSuffix(dst, ".desktop") || strings.HasSuffix(dst, ".plist") || strings.HasSuffix(dst, ".json") {
		mode = 0o644
	}
	return os.WriteFile(dst, in, mode)
}

func exeSuffix(goos string) string {
	if goos == "windows" {
		return ".exe"
	}
	return ""
}

func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if b.Len() > 0 && b.String()[b.Len()-1] != '-' {
				b.WriteByte('-')
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "aero-app"
	}
	return out
}

func debArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "amd64"
	case "arm64":
		return "arm64"
	case "386":
		return "i386"
	case "arm":
		return "armhf"
	default:
		return goarch
	}
}

func cargoTargetTriple(goos, goarch string) string {
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return ""
	}
	key := goos + "/" + goarch
	triples := map[string]string{
		"linux/amd64":   "x86_64-unknown-linux-gnu",
		"linux/arm64":   "aarch64-unknown-linux-gnu",
		"darwin/amd64":  "x86_64-apple-darwin",
		"darwin/arm64":  "aarch64-apple-darwin",
		"windows/amd64": "x86_64-pc-windows-msvc",
	}
	return triples[key]
}
