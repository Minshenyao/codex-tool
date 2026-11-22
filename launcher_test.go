package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallLauncherCreatesSignedBundle(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("codesign requires macOS")
	}
	dir := t.TempDir()
	dest := filepath.Join(dir, launcherAppName)
	iconPath := filepath.Join(dir, "official-chatgpt.icns")
	iconData := []byte("test icon bytes")
	if err := os.WriteFile(iconPath, iconData, 0o644); err != nil {
		t.Fatal(err)
	}
	info := &appInfo{path: filepath.Join(dir, "ChatGPT.app"), iconPath: iconPath}
	if err := installLauncher(info, dest); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", dest).CombinedOutput(); err != nil {
		t.Fatalf("verify launcher signature: %v\n%s", err, out)
	}
	plist := filepath.Join(dest, "Contents", "Info.plist")
	if name := plistValue(plist, "CFBundleDisplayName"); name != "ChatGPT-中文" {
		t.Fatalf("unexpected launcher display name %q", name)
	}
	if icon := plistValue(plist, "CFBundleIconFile"); icon != "icon.icns" {
		t.Fatalf("unexpected launcher icon %q", icon)
	}
	resources := filepath.Join(dest, "Contents", "Resources")
	for _, name := range []string{"codex-zh-launcher.js", "verify.js"} {
		if info, err := os.Stat(filepath.Join(resources, name)); err != nil || info.Size() == 0 {
			t.Fatalf("missing launcher resource %s: %v", name, err)
		}
	}
	if copiedIcon, err := os.ReadFile(filepath.Join(resources, "icon.icns")); err != nil || string(copiedIcon) != string(iconData) {
		t.Fatalf("launcher did not copy the official icon: %v", err)
	}
	if err := installLauncher(info, dest); err == nil {
		t.Fatal("existing launcher was not protected from replacement")
	}
}

func TestInstallLauncherCopiesOfficialChatGPTIcon(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses the installed macOS ChatGPT application")
	}
	info := findCodexApp()
	if info == nil || info.iconPath == "" {
		t.Skip("signed official ChatGPT application is not installed")
	}
	dest := filepath.Join(t.TempDir(), launcherAppName)
	if err := installLauncher(info, dest); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(info.iconPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "Contents", "Resources", "icon.icns"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("installed launcher icon differs from the official ChatGPT icon")
	}
}
