package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	pluginRepoURL   = "https://github.com/openai/plugins.git"
	pluginStoreDir  = "codex-tool-plugin-snapshots"
	marketplacePath = ".agents/plugins/marketplace.json"
	defaultMktName  = "my-curated"
	reservedMktName = "openai-curated"
)

type pluginStats struct {
	Total   int
	Local   int
	Dropped int
}

type pluginSnapshot struct {
	Dir   string
	Name  string
	Stats pluginStats
}

// preparePluginSnapshot creates a fresh clone; a registered snapshot can still be
// in use and can contain user changes, so subsequent runs leave it alone.
func preparePluginSnapshot(gitPath, repoURL, base, prefix string) (pluginSnapshot, error) {
	if !filepath.IsAbs(base) {
		return pluginSnapshot{}, fmt.Errorf("插件存放目录需要使用绝对路径")
	}
	if !validMarketName(prefix) {
		return pluginSnapshot{}, fmt.Errorf("市场名称前缀需包含 1 至 64 个 ASCII 字母、数字、下划线或连字符")
	}
	if strings.EqualFold(prefix, reservedMktName) {
		return pluginSnapshot{}, fmt.Errorf("请为本地市场选择其他名称前缀，%s 是官方市场名称", reservedMktName)
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return pluginSnapshot{}, fmt.Errorf("无法创建插件存放目录: %w", err)
	}
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		return pluginSnapshot{}, fmt.Errorf("无法解析插件存放目录: %w", err)
	}
	storePath := filepath.Join(base, pluginStoreDir)
	if err := os.Mkdir(storePath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return pluginSnapshot{}, fmt.Errorf("无法创建插件快照目录: %w", err)
	}
	storeInfo, err := os.Lstat(storePath)
	if err != nil {
		return pluginSnapshot{}, fmt.Errorf("无法读取插件快照目录: %w", err)
	}
	if !storeInfo.IsDir() {
		return pluginSnapshot{}, fmt.Errorf("插件快照路径必须是实际目录，请更换存放目录: %s", storePath)
	}
	store, err := os.OpenRoot(storePath)
	if err != nil {
		return pluginSnapshot{}, fmt.Errorf("无法打开插件快照目录: %w", err)
	}
	defer store.Close()
	if opened, err := store.Stat("."); err != nil || !os.SameFile(storeInfo, opened) {
		return pluginSnapshot{}, fmt.Errorf("插件快照目录已变化，请重试: %s", storePath)
	}

	name := prefix + "-" + strings.ToLower(rand.Text())
	if err := store.Mkdir(name, 0o700); err != nil {
		return pluginSnapshot{}, fmt.Errorf("无法创建本次插件快照目录: %w", err)
	}
	dir := filepath.Join(storePath, name)
	snapshotInfo, err := store.Lstat(name)
	if err != nil {
		return pluginSnapshot{}, fmt.Errorf("无法读取本次插件快照目录: %w", err)
	}
	ready := false
	defer func() {
		// Clean up only the directory created in this call, before registration.
		if !ready {
			if current, err := store.Lstat(name); err == nil && os.SameFile(current, snapshotInfo) {
				_ = store.RemoveAll(name)
			}
		}
	}()

	// Preserve normal Git configuration (including proxy settings). Disable
	// checkout hooks, templates and recursive submodule downloads for this clone.
	cmd := exec.Command(gitPath, "-c", "core.hooksPath=/dev/null", "clone", "--depth=1", "--single-branch", "--no-tags", "--no-recurse-submodules", "--no-local", "--template=", "--", repoURL, ".")
	cmd.Dir = dir
	cmd.Env = cloneGitEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return pluginSnapshot{}, fmt.Errorf("同步插件仓库失败: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	stats, err := sanitizeMarketplace(dir, name)
	if err != nil {
		return pluginSnapshot{}, fmt.Errorf("处理本地市场清单失败: %w", err)
	}
	if stats.Local == 0 {
		return pluginSnapshot{}, fmt.Errorf("仓库中没有可用的本地插件条目")
	}
	ready = true
	return pluginSnapshot{Dir: dir, Name: name, Stats: stats}, nil
}

// Ignore Git variables that could redirect writes to a caller's repository.
func cloneGitEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_INDEX_FILE", "GIT_QUARANTINE_PATH",
			"GIT_SHALLOW_FILE", "GIT_NAMESPACE", "GIT_TERMINAL_PROMPT":
			continue
		}
		out = append(out, entry)
	}
	return append(out, "GIT_TERMINAL_PROMPT=0")
}

type pluginEntry struct {
	Name   string `json:"name"`
	Source struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	} `json:"source"`
}

func sanitizeMarketplace(dir, name string) (pluginStats, error) {
	var stats pluginStats
	root, err := os.OpenRoot(dir)
	if err != nil {
		return stats, err
	}
	defer root.Close()
	for _, path := range []string{".agents", ".agents/plugins"} {
		info, err := root.Lstat(path)
		if err != nil {
			return stats, err
		}
		if !info.IsDir() {
			return stats, fmt.Errorf("市场清单的父目录不能是符号链接或文件: %s", path)
		}
	}
	info, err := root.Lstat(marketplacePath)
	if err != nil {
		return stats, err
	}
	if !info.Mode().IsRegular() {
		return stats, fmt.Errorf("市场清单必须是普通文件: %s", marketplacePath)
	}
	raw, err := root.ReadFile(marketplacePath)
	if err != nil {
		return stats, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return stats, fmt.Errorf("市场清单不是有效的 JSON 对象: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(doc["plugins"], &entries); err != nil {
		return stats, fmt.Errorf("市场清单中的 plugins 不是有效的数组: %w", err)
	}
	kept := make([]json.RawMessage, 0, len(entries))
	for _, entry := range entries {
		var plugin pluginEntry
		if json.Unmarshal(entry, &plugin) != nil || strings.TrimSpace(plugin.Name) == "" ||
			plugin.Source.Source != "local" || !localPluginDir(root, plugin.Source.Path) {
			continue
		}
		kept = append(kept, entry)
	}
	stats.Total, stats.Local = len(entries), len(kept)
	stats.Dropped = stats.Total - stats.Local
	if stats.Local == 0 {
		return stats, nil
	}

	doc["name"], _ = json.Marshal(name)
	if rawInterface, ok := doc["interface"]; ok {
		var iface map[string]json.RawMessage
		if err := json.Unmarshal(rawInterface, &iface); err != nil {
			return stats, fmt.Errorf("市场清单中的 interface 不是有效的对象: %w", err)
		}
		if iface != nil {
			iface["displayName"] = doc["name"]
			doc["interface"], err = json.Marshal(iface)
			if err != nil {
				return stats, err
			}
		}
	}
	doc["plugins"], err = json.Marshal(kept)
	if err != nil {
		return stats, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return stats, err
	}
	return stats, root.WriteFile(marketplacePath, append(out, '\n'), 0o644)
}

func localPluginDir(root *os.Root, path string) bool {
	if !filepath.IsLocal(path) || strings.ContainsAny(path, "\\\x00") {
		return false
	}
	for _, component := range strings.Split(filepath.ToSlash(path), "/") {
		if component == ".." || strings.EqualFold(component, ".git") {
			return false
		}
	}
	// Root.Stat checks containment while resolving any symbolic links.
	info, err := root.Stat(path)
	return err == nil && info.IsDir()
}

func findCodexCLI() string {
	if p, err := exec.LookPath("codex"); err == nil {
		return p
	}
	appDirs := []string{"/Applications"}
	if home, err := os.UserHomeDir(); err == nil {
		appDirs = append(appDirs, filepath.Join(home, "Applications"))
	}
	for _, dir := range appDirs {
		for _, app := range []string{"ChatGPT.app", "Codex.app"} {
			p := filepath.Join(dir, app, "Contents/Resources/codex-cli/CodexCLI.app/Contents/MacOS/codex")
			if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return p
			}
		}
	}
	return ""
}

func runCodex(cli string, args ...string) (string, error) {
	out, err := exec.Command(cli, args...).Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if stderr := strings.TrimSpace(string(exitErr.Stderr)); stderr != "" {
			return "", fmt.Errorf("%w: %s", err, stderr)
		}
	}
	return strings.TrimSpace(string(out)), err
}

func registerPluginSnapshot(cli string, snapshot pluginSnapshot) error {
	out, err := runCodex(cli, "plugin", "marketplace", "add", "--json", snapshot.Dir)
	if err != nil {
		return err
	}
	var result struct {
		Name string `json:"marketplaceName"`
		Root string `json:"installedRoot"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return fmt.Errorf("无法读取 Codex CLI 返回的市场注册结果: %w", err)
	}
	if result.Name != snapshot.Name || !samePluginDirectory(result.Root, snapshot.Dir) {
		return fmt.Errorf("Codex CLI 返回的市场名称或目录与本次快照不一致")
	}
	return nil
}

func samePluginDirectory(a, b string) bool {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	left, err := os.Stat(a)
	if err != nil || !left.IsDir() {
		return false
	}
	right, err := os.Stat(b)
	return err == nil && os.SameFile(left, right)
}

func marketCounts(cli, name string) (installed, available int, err error) {
	args := []string{"plugin", "list", "--json", "--available"}
	if name != "" {
		args = append(args, "--marketplace="+name)
	}
	out, err := runCodex(cli, args...)
	if err != nil {
		return 0, 0, err
	}
	var doc struct {
		Installed []json.RawMessage `json:"installed"`
		Available []json.RawMessage `json:"available"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return 0, 0, err
	}
	if doc.Installed == nil || doc.Available == nil {
		return 0, 0, fmt.Errorf("Codex CLI 未返回已安装和可用插件列表")
	}
	return len(doc.Installed), len(doc.Available), nil
}

func validMarketName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func defaultPluginDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "codex-zh", "plugins")
}

func taskPlugins() error {
	fmt.Println("\n---------------- 插件市场 ----------------")
	cli := findCodexCLI()
	if cli == "" {
		return fmt.Errorf("未找到 Codex CLI，请安装官方 Codex CLI 或更新 ChatGPT/Codex 桌面版")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return fmt.Errorf("未找到 Git，请先安装 Git 或运行 xcode-select --install")
	}

	fmt.Println("Codex CLI:", cli)
	dir := strings.TrimSpace(ask("插件存放目录", defaultPluginDir()))
	prefix := strings.TrimSpace(ask("市场名称前缀", defaultMktName))
	fmt.Println("正在从 GitHub 同步插件快照 ...")
	snapshot, err := preparePluginSnapshot(gitPath, pluginRepoURL, dir, prefix)
	if err != nil {
		return err
	}
	fmt.Println("快照目录:", snapshot.Dir)
	fmt.Printf("条目 %d，保留本地源 %d，剔除 %d\n", snapshot.Stats.Total, snapshot.Stats.Local, snapshot.Stats.Dropped)

	if err := registerPluginSnapshot(cli, snapshot); err != nil {
		// The CLI could have written its config before encountering an error.
		return fmt.Errorf("无法确认市场 %s 已注册，快照保留在 %s。可运行 plugins-status 核对: %w", snapshot.Name, snapshot.Dir, err)
	}
	fmt.Printf("市场 %s 已注册，可在 Codex 插件面板查看。\n", snapshot.Name)
	if inst, avail, err := marketCounts(cli, snapshot.Name); err == nil {
		fmt.Printf("此市场可用插件 %d 个，已安装 %d 个。\n", avail, inst)
	} else {
		fmt.Printf("无法读取此市场的插件列表，可运行 plugins-status 核对: %v\n", err)
	}
	return nil
}

func taskPluginsStatus() error {
	cli := findCodexCLI()
	if cli == "" {
		return fmt.Errorf("未找到 Codex CLI，请安装官方 Codex CLI 或更新 ChatGPT/Codex 桌面版")
	}
	out, err := runCodex(cli, "plugin", "marketplace", "list")
	if err != nil {
		return fmt.Errorf("读取插件市场失败: %w", err)
	}
	fmt.Println("已注册市场:")
	fmt.Println(out)
	inst, avail, err := marketCounts(cli, "")
	if err != nil {
		return fmt.Errorf("读取插件列表失败: %w", err)
	}
	fmt.Printf("\n可用插件 %d 个，已安装 %d 个\n", avail, inst)
	return nil
}
