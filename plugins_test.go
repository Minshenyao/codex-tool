package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writePluginFixture(t *testing.T, filename, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readPluginFixture(t *testing.T, filename string) string {
	t.Helper()
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func TestPluginSnapshotsPreserveExistingFiles(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is unavailable")
	}
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	upstream := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = upstream
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_ALLOW_PROTOCOL=file"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "--template=")
	writePluginFixture(t, filepath.Join(upstream, marketplacePath), `{"name":"upstream","plugins":[{"name":"demo","source":{"source":"local","path":"./demo"}}]}`)
	workfile := filepath.Join("demo", "content.txt")
	commit := func(content string) {
		t.Helper()
		writePluginFixture(t, filepath.Join(upstream, workfile), content)
		git("add", ".")
		git("-c", "user.name=Fixture Author", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	}
	commit("first upstream revision")
	base := t.TempDir()
	writePluginFixture(t, filepath.Join(base, "my-file"), "user content")

	first, err := preparePluginSnapshot(gitPath, upstream, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats.Local != 1 || !strings.HasPrefix(first.Name, "test-") || filepath.Base(first.Dir) != first.Name || filepath.Base(filepath.Dir(first.Dir)) != pluginStoreDir {
		t.Fatalf("unexpected snapshot: %+v", first)
	}
	writePluginFixture(t, filepath.Join(first.Dir, workfile), "user changes")
	writePluginFixture(t, filepath.Join(first.Dir, "untracked"), "user untracked file")
	commit("second upstream revision")

	// An inherited Git context may point at an existing clone.
	t.Setenv("GIT_DIR", filepath.Join(first.Dir, ".git"))
	t.Setenv("GIT_WORK_TREE", first.Dir)
	second, err := preparePluginSnapshot(gitPath, upstream, base, "test")
	if err != nil {
		t.Fatal(err)
	}
	if second.Dir == first.Dir || second.Name == first.Name {
		t.Fatalf("reused existing snapshot: %+v", second)
	}
	if _, err := preparePluginSnapshot(gitPath, filepath.Join(base, "missing"), base, "test"); err == nil {
		t.Fatal("expected missing source to fail")
	}
	for path, expected := range map[string]string{
		filepath.Join(base, "my-file"):        "user content",
		filepath.Join(first.Dir, workfile):    "user changes",
		filepath.Join(first.Dir, "untracked"): "user untracked file",
		filepath.Join(second.Dir, workfile):   "second upstream revision",
	} {
		if got := readPluginFixture(t, path); got != expected {
			t.Errorf("%s: got %q, want %q", path, got, expected)
		}
	}
	entries, err := os.ReadDir(filepath.Join(base, pluginStoreDir))
	if err != nil || len(entries) != 2 {
		t.Fatalf("the store should have both successful snapshots: %v, %v", entries, err)
	}
}

func TestPluginSnapshotRejectsSymlinkStore(t *testing.T) {
	base, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(base, pluginStoreDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePluginSnapshot("unused", "unused", base, "test"); err == nil {
		t.Fatal("expected symlink store to be refused")
	}
	if entries, err := os.ReadDir(target); err != nil || len(entries) != 0 {
		t.Fatalf("symlink destination was changed: %v, %v", entries, err)
	}
}

func TestSanitizeMarketplaceKeepsOnlyContainedLocalDirectories(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	outside := filepath.Join(parent, "outside")
	writePluginFixture(t, filepath.Join(outside, "data"), "outside")
	writePluginFixture(t, filepath.Join(repo, "plugins", "demo", "data"), "demo")
	if err := os.Symlink("../outside", filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(repo, marketplacePath)
	writePluginFixture(t, manifest, `{
		"name": "upstream", "interface": {"displayName":"original", "description":"retained"},
		"plugins": [
			{"name":"demo", "source":{"source":"local","path":"./plugins/demo"},"description":"retained"},
			{"name":"remote", "source":{"source":"github","path":"./plugins/demo"}},
			{"name":"other-dir", "source":{"source":"local","path":"../outside"}},
			{"name":"missing", "source":{"source":"local","path":"./missing"}},
			{"name":"linked", "source":{"source":"local","path":"./linked"}},
			{"name":"file", "source":{"source":"local","path":"./plugins/demo/data"}}
		]}`)
	stats, err := sanitizeMarketplace(repo, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 6 || stats.Local != 1 || stats.Dropped != 5 {
		t.Fatalf("unexpected filtering result: %+v", stats)
	}
	var doc struct {
		Name      string
		Interface map[string]string
		Plugins   []struct{ Name, Description string }
	}
	if err := json.Unmarshal([]byte(readPluginFixture(t, manifest)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Name != "test-version" || doc.Interface["displayName"] != doc.Name || doc.Interface["description"] != "retained" ||
		len(doc.Plugins) != 1 || doc.Plugins[0].Name != "demo" || doc.Plugins[0].Description != "retained" {
		t.Fatalf("unexpected filtered manifest: %+v", doc)
	}
}

func TestSanitizeMarketplaceRejectsSymlinkManifest(t *testing.T) {
	repo := t.TempDir()
	target := filepath.Join(t.TempDir(), "marketplace.json")
	const original = `{"name":"outside","plugins":[]}`
	writePluginFixture(t, target, original)
	manifest := filepath.Join(repo, marketplacePath)
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := sanitizeMarketplace(repo, "test-version"); err == nil {
		t.Fatal("expected a symlinked marketplace manifest to be refused")
	}
	if got := readPluginFixture(t, target); got != original {
		t.Fatalf("symlink destination was changed: %q", got)
	}
}
