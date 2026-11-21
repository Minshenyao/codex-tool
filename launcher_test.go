package main

import (
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
	info := &appInfo{path: filepath.Join(dir, "ChatGPT.app")}
	if err := installLauncher(info, dest); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--strict", dest).CombinedOutput(); err != nil {
		t.Fatalf("verify launcher signature: %v\n%s", err, out)
	}
	resources := filepath.Join(dest, "Contents", "Resources")
	for _, name := range []string{"codex-zh-launcher.js", "verify.js"} {
		if info, err := os.Stat(filepath.Join(resources, name)); err != nil || info.Size() == 0 {
			t.Fatalf("missing launcher resource %s: %v", name, err)
		}
	}
	if err := installLauncher(info, dest); err == nil {
		t.Fatal("existing launcher was not protected from replacement")
	}
}
