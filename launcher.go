package main

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

//go:embed files/launcher.js files/verify.js
var launcherFiles embed.FS

const (
	launcherAppName = "ChatGPT-中文.app"
	codexBundleID   = "com.openai.codex"
	codexTeamID     = "2DC432GLL2"
)

type appInfo struct {
	path     string
	version  string
	nodePath string
	iconPath string
}

func plistValue(path, key string) string {
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :"+key, path).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func findCodexApp() *appInfo {
	home, _ := os.UserHomeDir()
	paths := []string{"/Applications/ChatGPT.app", "/Applications/Codex.app"}
	if home != "" {
		paths = append(paths, filepath.Join(home, "Applications", "ChatGPT.app"), filepath.Join(home, "Applications", "Codex.app"))
	}
	if out, err := exec.Command("/usr/bin/mdfind", "kMDItemCFBundleIdentifier == '"+codexBundleID+"'").Output(); err == nil {
		paths = append(paths, strings.Split(strings.TrimSpace(string(out)), "\n")...)
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			continue
		}
		plist := filepath.Join(path, "Contents", "Info.plist")
		if plistValue(plist, "CFBundleIdentifier") != codexBundleID {
			continue
		}
		requirement := `anchor apple generic and identifier "` + codexBundleID + `" and certificate leaf[subject.OU] = "` + codexTeamID + `"`
		if err := exec.Command("/usr/bin/codesign", "--verify", "-R="+requirement, path).Run(); err != nil {
			continue
		}
		info := &appInfo{path: path, version: plistValue(plist, "CFBundleShortVersionString")}
		node := filepath.Join(path, "Contents", "Resources", "cua_node", "bin", "node")
		if stat, err := os.Stat(node); err == nil && stat.Mode().IsRegular() && stat.Mode()&0o111 != 0 {
			info.nodePath = node
		}
		iconName := plistValue(plist, "CFBundleIconFile")
		if filepath.Base(iconName) == iconName {
			iconPath := filepath.Join(path, "Contents", "Resources", iconName)
			if stat, err := os.Lstat(iconPath); err == nil && stat.Mode().IsRegular() {
				info.iconPath = iconPath
			}
		}
		return info
	}
	return nil
}

func installedLauncherPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户目录：%w", err)
	}
	return filepath.Join(home, "Applications", launcherAppName), nil
}

const infoPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>ChatGPT-中文</string>
  <key>CFBundleDisplayName</key><string>ChatGPT-中文</string>
  <key>CFBundleIdentifier</key><string>local.chatgpt.zh.launcher</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>CFBundleShortVersionString</key><string>0.1.3</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>launcher</string>
  <key>CFBundleIconFile</key><string>icon.icns</string>
  <key>LSUIElement</key><true/>
</dict>
</plist>
`

const launcherScript = `#!/bin/bash
set -eu
umask 077
resources="$(cd "$(dirname "$0")/../Resources" && pwd)"
app="$(cat "$resources/codex-app-path")"
node="$app/Contents/Resources/cua_node/bin/node"
log="$HOME/Library/Logs/ChatGPT-中文启动器.log"
mkdir -p "$(dirname "$log")"
requirement='anchor apple generic and identifier "com.openai.codex" and certificate leaf[subject.OU] = "2DC432GLL2"'
if [ ! -x "$node" ] || ! /usr/bin/codesign --verify "-R=$requirement" "$app" >> "$log" 2>&1; then
  printf '%s\n' '未找到原先安装位置的官方 Codex 或其内置 Node。请重新安装启动器。' >> "$log"
  /usr/bin/osascript -e 'display dialog "无法启动。请查看 ChatGPT-中文启动器.log，或直接打开官方客户端。" buttons {"好"} default button 1 with icon caution'
  exit 1
fi
exec "$node" "$resources/codex-zh-launcher.js" --app "$app" --verbose >> "$log" 2>&1
`

func taskInstall() error {
	info := findCodexApp()
	if info == nil || info.nodePath == "" || info.iconPath == "" {
		return errors.New("请先安装含内置 Node 和应用图标的官方 ChatGPT 桌面应用")
	}
	dest, err := installedLauncherPath()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("目标已存在：%s。请先将旧启动器移到其他位置", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查安装目标失败：%w", err)
	}
	fmt.Printf("桌面应用：%s（版本 %s）\n安装到：%s\n", info.path, info.version, dest)
	if !confirm("安装实验性中文启动器", false) {
		fmt.Println("已取消安装。")
		return nil
	}
	if err := installLauncher(info, dest); err != nil {
		return err
	}
	fmt.Printf("启动器已安装在 %s。退出官方应用后，可在 Finder 打开启动器。\n", dest)
	return nil
}

func installLauncher(info *appInfo, dest string) error {
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".codex-tool-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	bundle := filepath.Join(staging, launcherAppName)
	macOS := filepath.Join(bundle, "Contents", "MacOS")
	resources := filepath.Join(bundle, "Contents", "Resources")
	for _, dir := range []string{macOS, resources} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	for _, spec := range []struct {
		name string
		body []byte
		mode os.FileMode
	}{
		{filepath.Join(bundle, "Contents", "Info.plist"), []byte(infoPlist), 0o644},
		{filepath.Join(macOS, "launcher"), []byte(launcherScript), 0o755},
		{filepath.Join(resources, "codex-app-path"), []byte(info.path + "\n"), 0o644},
	} {
		if err := os.WriteFile(spec.name, spec.body, spec.mode); err != nil {
			return err
		}
	}
	for name, target := range map[string]string{"launcher.js": "codex-zh-launcher.js", "verify.js": "verify.js"} {
		data, err := launcherFiles.ReadFile("files/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(resources, target), data, 0o644); err != nil {
			return err
		}
	}
	icon, err := os.ReadFile(info.iconPath)
	if err != nil {
		return fmt.Errorf("读取官方 ChatGPT 图标失败：%w", err)
	}
	if len(icon) == 0 {
		return errors.New("官方 ChatGPT 图标为空")
	}
	if err := os.WriteFile(filepath.Join(resources, "icon.icns"), icon, 0o644); err != nil {
		return fmt.Errorf("复制官方 ChatGPT 图标失败：%w", err)
	}
	if output, err := exec.Command("/usr/bin/codesign", "--sign", "-", bundle).CombinedOutput(); err != nil {
		return fmt.Errorf("签名启动器失败：%w %s", err, strings.TrimSpace(string(output)))
	}
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("安装目标已存在：%s", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(bundle, dest)
}

func taskVerify() error {
	info := findCodexApp()
	if info == nil || info.nodePath == "" {
		return errors.New("找不到官方 Codex 桌面应用的 Node")
	}
	// 验证使用当前构建自带的脚本，避免执行安装目录中后来被修改的文件。
	staging, err := os.MkdirTemp("", "codex-tool-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, name := range []string{"launcher.js", "verify.js"} {
		data, err := launcherFiles.ReadFile("files/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(staging, name), data, 0o600); err != nil {
			return err
		}
	}
	cmd := exec.Command(info.nodePath, filepath.Join(staging, "verify.js"))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("未能确认启动器内部状态，请查看 ~/Library/Logs/ChatGPT-中文启动器.log：%w", err)
	}
	return nil
}
