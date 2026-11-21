package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const usage = `codex-tool — Codex 桌面配置助手

用法：codex-tool [命令]

  relay            配置兼容 Responses API 的自定义供应方
  plugins          注册本地插件市场快照
  plugins-status   查看已注册的插件市场
  install          安装实验性中文启动器
  verify           检查中文启动器的运行状态
  status           查看本地配置和安装状态
  help             显示此说明

首版面向 Apple Silicon Mac。直接运行可打开交互菜单。`

var stdin *bufio.Reader

type promptError struct{ cause error }

func main() {
	stdin = bufio.NewReader(os.Stdin)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "codex-tool:", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	// 集中结束交互命令，输入流关闭时不会采纳任何默认确认。
	defer func() {
		if recovered := recover(); recovered != nil {
			if prompt, ok := recovered.(promptError); ok {
				err = prompt.cause
				return
			}
			panic(recovered)
		}
	}()

	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Println(usage)
		return nil
	}
	if len(args) > 1 {
		return errors.New("命令不接受额外参数，请查看 --help")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return fmt.Errorf("首版面向 Apple Silicon Mac，当前平台 %s/%s 不在支持范围内", runtime.GOOS, runtime.GOARCH)
	}
	if len(args) == 1 {
		return runCommand(args[0])
	}

	for {
		fmt.Print(`
Codex 桌面配置助手

  1) 配置中转站
  2) 注册本地插件市场
  3) 安装实验性中文启动器
  4) 查看本地配置和应用状态
  5) 查看插件市场状态
  6) 检查中文启动器的运行状态
  0) 退出

`)
		choice := ask("选择", "0")
		commands := map[string]string{
			"1": "relay", "2": "plugins", "3": "install", "4": "status", "5": "plugins-status", "6": "verify",
		}
		if choice == "0" {
			return nil
		}
		cmd, ok := commands[choice]
		if !ok {
			fmt.Fprintln(os.Stderr, "请输入菜单中的数字。")
			continue
		}
		if err := runCommand(cmd); err != nil {
			fmt.Fprintln(os.Stderr, "codex-tool:", err)
		}
	}
}

func runCommand(cmd string) error {
	switch cmd {
	case "install":
		return taskInstall()
	case "verify":
		return taskVerify()
	case "relay":
		return taskRelay()
	case "status":
		return taskStatus()
	case "plugins":
		return taskPlugins()
	case "plugins-status":
		return taskPluginsStatus()
	default:
		return fmt.Errorf("未知命令 %q，请查看 --help", cmd)
	}
}

func ask(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]：", label, def)
	} else {
		fmt.Printf("%s：", label)
	}
	line, err := stdin.ReadString('\n')
	if errors.Is(err, io.EOF) {
		panic(promptError{errors.New("输入已结束，操作取消")})
	}
	if err != nil {
		panic(promptError{fmt.Errorf("读取输入失败：%w", err)})
	}
	if line = strings.TrimSpace(line); line == "" {
		return def
	}
	return line
}

func askRequired(label string) string {
	for {
		if value := ask(label, ""); value != "" {
			return value
		}
		fmt.Println("不能为空，请重新输入。")
	}
}

func confirm(label string, def bool) bool {
	defaultChoice := "n"
	if def {
		defaultChoice = "y"
	}
	for {
		switch strings.ToLower(ask(label+" (y/n)", defaultChoice)) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		}
	}
}

func pickModel(models []string, label string) string {
	for i, model := range models {
		fmt.Printf("  %d) %s\n", i+1, model)
	}
	for {
		value := ask(label, "1")
		if number, err := strconv.Atoi(value); err == nil && number >= 1 && number <= len(models) {
			return models[number-1]
		}
		fmt.Println("请输入列表中的编号。")
	}
}

func codexHomeDir() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex")
}

func taskStatus() error {
	if info := findCodexApp(); info != nil {
		fmt.Printf("Codex 应用：%s（版本 %s）\n", info.path, info.version)
		if info.nodePath == "" {
			fmt.Println("桌面内置 Node：未找到")
		}
	} else {
		fmt.Println("Codex 应用：未找到已签名的官方客户端")
	}

	if dir := codexHomeDir(); dir != "" {
		config := filepath.Join(dir, "config.toml")
		if _, err := os.Stat(config); errors.Is(err, os.ErrNotExist) {
			fmt.Printf("Codex 配置：%s（尚未创建）\n", config)
		} else if err != nil {
			return fmt.Errorf("检查 Codex 配置失败：%w", err)
		} else {
			fmt.Printf("Codex 配置：%s\n", config)
		}
	}

	path, err := installedLauncherPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Println("中文启动器：未安装")
	} else if err != nil {
		return fmt.Errorf("检查中文启动器失败：%w", err)
	} else {
		fmt.Printf("中文启动器：%s（运行状态请用 verify 检查）\n", path)
	}
	return nil
}
