package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type reasoningLevel struct {
	Description string `json:"description"`
	Effort      string `json:"effort"`
}

type truncationPolicy struct {
	Limit int    `json:"limit"`
	Mode  string `json:"mode"`
}

type catalogModel struct {
	ApplyPatchToolType            string           `json:"apply_patch_tool_type"`
	AutoCompactTokenLimit         int              `json:"auto_compact_token_limit"`
	BaseInstructions              string           `json:"base_instructions"`
	ContextWindow                 int              `json:"context_window"`
	DefaultReasoningLevel         string           `json:"default_reasoning_level"`
	DefaultVerbosity              string           `json:"default_verbosity"`
	Description                   string           `json:"description"`
	DisplayName                   string           `json:"display_name"`
	EffectiveContextWindowPercent int              `json:"effective_context_window_percent"`
	ExperimentalSupportedTools    []string         `json:"experimental_supported_tools"`
	InputModalities               []string         `json:"input_modalities"`
	Priority                      int              `json:"priority"`
	ServiceTiers                  []any            `json:"service_tiers"`
	ShellType                     string           `json:"shell_type"`
	Slug                          string           `json:"slug"`
	SupportVerbosity              bool             `json:"support_verbosity"`
	SupportedInAPI                bool             `json:"supported_in_api"`
	SupportedReasoningLevels      []reasoningLevel `json:"supported_reasoning_levels"`
	SupportsParallelToolCalls     bool             `json:"supports_parallel_tool_calls"`
	SupportsReasoningSummaries    bool             `json:"supports_reasoning_summaries"`
	SupportsSearchTool            bool             `json:"supports_search_tool"`
	TruncationPolicy              truncationPolicy `json:"truncation_policy"`
	Visibility                    string           `json:"visibility"`
}

type catalogFile struct {
	Models []catalogModel `json:"models"`
}

func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(fmt.Sprintf("\\u%04X", r))
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
func buildCatalog(models []string, providerName string, ctx int, vision bool) catalogFile {
	modalities := []string{"text"}
	if vision {
		modalities = append(modalities, "image")
	}
	levels := []reasoningLevel{
		{Effort: "low"}, {Effort: "medium"}, {Effort: "high"}, {Effort: "xhigh"},
	}
	cat := catalogFile{}
	for i, id := range models {
		cat.Models = append(cat.Models, catalogModel{
			ApplyPatchToolType:            "freeform",
			AutoCompactTokenLimit:         (ctx/10)*8 + (ctx%10)*8/10,
			BaseInstructions:              "You are a coding assistant. Help the user with their programming tasks.",
			ContextWindow:                 ctx,
			DefaultReasoningLevel:         "high",
			DefaultVerbosity:              "low",
			Description:                   fmt.Sprintf("%s · %s", id, providerName),
			DisplayName:                   id,
			EffectiveContextWindowPercent: 95,
			ExperimentalSupportedTools:    []string{},
			InputModalities:               modalities,
			Priority:                      i + 1,
			ServiceTiers:                  []any{},
			ShellType:                     "shell_command",
			Slug:                          id,
			SupportVerbosity:              false,
			SupportedInAPI:                true,
			SupportedReasoningLevels:      levels,
			SupportsParallelToolCalls:     true,
			SupportsReasoningSummaries:    false,
			SupportsSearchTool:            false,
			TruncationPolicy:              truncationPolicy{Limit: 10000, Mode: "tokens"},
			Visibility:                    "list",
		})
	}
	return cat
}

var (
	relayIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	relayEnvPattern     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	errRelayLegacyToken = errors.New("供应方仍配置了旧版明文密钥，需确认迁移")
	fixedRelayModels    = []string{
		"deepseek-v4-flash",
		"deepseek-v4-pro",
		"gpt-6-luna",
		"glm-5.3",
		"glm-5.3-flash",
	}
)

func relayURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.ForceQuery || u.RawQuery != "" || strings.Contains(raw, "#") {
		return nil, errors.New("请输入不带账号、查询参数或片段的 HTTP(S) 地址")
	}
	return u, nil
}

func relayLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback()
}

type relaySettings struct {
	providerID, providerName, baseURL, envKey, activeModel string
	removeLegacyToken                                      bool
}

type relayEdit struct {
	start, end int
	text       string
}

type relayField struct {
	key, value string
}

// relayTOMLStringEnd recognizes quoted keys and values without interpreting value text.
func relayTOMLStringEnd(s string, at int, allowMultiline bool) (int, bool) {
	quote := s[at]
	multiline := allowMultiline && len(s) >= at+3 && s[at+1] == quote && s[at+2] == quote
	i := at + 1
	if multiline {
		i += 2
	}
	for i < len(s) {
		switch s[i] {
		case '\\':
			if quote == '"' {
				if !multiline && i+1 < len(s) && (s[i+1] == '\n' || s[i+1] == '\r') {
					return 0, false
				}
				i += 2
				continue
			}
		case '\r', '\n':
			if !multiline {
				return 0, false
			}
		default:
			if s[i] == quote {
				if !multiline {
					return i + 1, true
				}
				if len(s) >= i+3 && s[i+1] == quote && s[i+2] == quote {
					end := i + 3
					for end < len(s) && end < i+5 && s[end] == quote {
						end++
					}
					return end, true
				}
			}
		}
		i++
	}
	return 0, false
}

// relayTOMLPath reads a dotted key, returning the remaining part of the statement.
func relayTOMLPath(s string) (path []string, rest string, ok bool) {
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return nil, "", false
		}
		var name string
		if s[0] == '"' || s[0] == '\'' {
			end, ok := relayTOMLStringEnd(s, 0, false)
			if !ok {
				return nil, "", false
			}
			if s[0] == '"' {
				var err error
				name, err = strconv.Unquote(s[:end])
				if err != nil {
					return nil, "", false
				}
			} else {
				name = s[1 : end-1]
			}
			s = s[end:]
		} else {
			end := 0
			for end < len(s) && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-", rune(s[end])) {
				end++
			}
			if end == 0 {
				return nil, "", false
			}
			name, s = s[:end], s[end:]
		}
		path = append(path, name)
		s = strings.TrimLeft(s, " \t")
		if !strings.HasPrefix(s, ".") {
			return path, s, true
		}
		s = s[1:]
	}
}

// relayTOMLValueEnd skips complete values, including arrays and multiline strings.
// Only statement boundaries matter because unrelated values are kept verbatim.
func relayTOMLValueEnd(s string) (int, bool) {
	var stack []byte
	hasValue := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\r':
		case '\n':
			if len(stack) == 0 {
				return i + 1, hasValue
			}
		case '#':
			end := strings.IndexByte(s[i:], '\n')
			if end == -1 {
				return len(s), hasValue && len(stack) == 0
			}
			i += end - 1
		case '\'', '"':
			end, ok := relayTOMLStringEnd(s, i, true)
			if !ok {
				return 0, false
			}
			i = end - 1
			hasValue = true
		case '[', '{':
			end := byte(']')
			if c == '{' {
				end = '}'
			}
			stack = append(stack, end)
			hasValue = true
		case ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != c {
				return 0, false
			}
			stack = stack[:len(stack)-1]
		default:
			hasValue = true
		}
	}
	return len(s), hasValue && len(stack) == 0
}

func relayTOMLLineEnd(s string) int {
	if n := strings.IndexByte(s, '\n'); n >= 0 {
		return n + 1
	}
	return len(s)
}

// mergeRelayConfig edits only the selected settings in their original tables.
// It rejects forms that cannot be changed without risking duplicate TOML keys.
func mergeRelayConfig(old string, settings relaySettings, catalogPath string) (string, error) {
	if !utf8.ValidString(old) {
		return "", errors.New("config.toml 不是有效的 UTF-8")
	}
	rootFields := []relayField{
		{"model", tomlQuote(settings.activeModel)},
		{"model_provider", tomlQuote(settings.providerID)},
		{"model_catalog_json", tomlQuote(catalogPath)},
	}
	providerFields := []relayField{
		{"name", tomlQuote(settings.providerName)},
		{"base_url", tomlQuote(settings.baseURL)},
		{"wire_api", `"responses"`},
		{"env_key", ""},
		{"experimental_bearer_token", ""},
	}
	if settings.envKey != "" {
		providerFields[3].value = tomlQuote(settings.envKey)
	}
	newline := "\n"
	if strings.Contains(old, "\r\n") {
		newline = "\r\n"
	}
	var edits []relayEdit
	rootEnd, providerStart := len(old), -1
	var section []string
	rootSeen, providerSeen := make(map[string]bool), make(map[string]bool)
	legacyToken := false
	for at := 0; at < len(old); {
		s := strings.TrimLeft(old[at:], " \t")
		if s == "" || s[0] == '\r' || s[0] == '\n' || s[0] == '#' {
			at += relayTOMLLineEnd(old[at:])
			continue
		}
		conflict := func() (string, error) {
			line := 1 + strings.Count(old[:at], "\n")
			return "", fmt.Errorf("无法安全更新 config.toml 第 %d 行，请检查 TOML 结构", line)
		}
		table := s[0] == '['
		arrayTable := strings.HasPrefix(s, "[[")
		if table {
			s = s[1:]
			if arrayTable {
				s = s[1:]
			}
		}
		path, rest, ok := relayTOMLPath(s)
		if !ok {
			return conflict()
		}
		var end int
		var value string
		if table {
			close := "]"
			if arrayTable {
				close = "]]"
			}
			if !strings.HasPrefix(rest, close) {
				return conflict()
			}
			tail := strings.TrimSpace(rest[len(close):relayTOMLLineEnd(rest)])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return conflict()
			}
			end = at + relayTOMLLineEnd(old[at:])
			section = path
			if at < rootEnd {
				rootEnd = at
			}
		} else {
			if !strings.HasPrefix(rest, "=") {
				return conflict()
			}
			n, ok := relayTOMLValueEnd(rest[1:])
			if !ok {
				return conflict()
			}
			end = len(old) - len(rest) + 1 + n
			value = rest[1 : 1+n]
			fullPath := make([]string, 0, len(section)+len(path))
			fullPath = append(fullPath, section...)
			path = append(fullPath, path...)
		}

		switch {
		case path[0] == "model_providers":
			if len(path) == 1 && (!table || arrayTable) {
				return conflict()
			}
			if len(path) >= 2 && path[1] == settings.providerID {
				if table && len(path) == 2 {
					if arrayTable || providerStart >= 0 {
						return conflict()
					}
					providerStart = end
				} else if !table && len(section) < 2 {
					return conflict()
				} else if len(path) >= 3 {
					if path[2] == "auth" {
						return "", errors.New("所选供应方已使用其他鉴权配置，请在 config.toml 中确认后再迁移")
					}
					if path[2] == "requires_openai_auth" {
						flag, _, _ := strings.Cut(value, "#")
						if table || len(path) != 3 || strings.TrimSpace(flag) != "false" {
							return "", errors.New("所选供应方已启用 OpenAI 鉴权，请在 config.toml 中确认后再迁移")
						}
					}
					for _, field := range providerFields {
						if path[2] != field.key {
							continue
						}
						if table || len(path) != 3 || providerSeen[field.key] {
							return conflict()
						}
						legacyToken = legacyToken || field.key == "experimental_bearer_token"
						providerSeen[field.key] = true
						edits = append(edits, relayEdit{at, end, relayFieldLine(field, newline)})
					}
				}
			}
		default:
			for _, field := range rootFields {
				if path[0] != field.key {
					continue
				}
				if table || len(path) != 1 || rootSeen[field.key] {
					return conflict()
				}
				rootSeen[field.key] = true
				edits = append(edits, relayEdit{at, end, relayFieldLine(field, newline)})
			}
		}
		at = end
	}
	if legacyToken && !settings.removeLegacyToken {
		return "", errRelayLegacyToken
	}
	place := func(at int, text string) {
		if text != "" {
			if at > 0 && old[at-1] != '\n' {
				text = newline + text
			}
			edits = append(edits, relayEdit{at, at, text})
		}
	}
	place(rootEnd, missingRelayFields(rootFields, rootSeen, newline))
	newFields := missingRelayFields(providerFields, providerSeen, newline)
	if providerStart < 0 {
		place(len(old), newline+"[model_providers."+settings.providerID+"]"+newline+newFields)
	} else {
		place(providerStart, newFields)
	}
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].start == edits[j].start {
			return edits[i].end < edits[j].end
		}
		return edits[i].start < edits[j].start
	})
	var b strings.Builder
	at := 0
	for _, edit := range edits {
		b.WriteString(old[at:edit.start])
		b.WriteString(edit.text)
		at = edit.end
	}
	b.WriteString(old[at:])
	return b.String(), nil
}

func relayFieldLine(field relayField, newline string) string {
	if field.value == "" {
		return ""
	}
	return field.key + " = " + field.value + newline
}

func missingRelayFields(fields []relayField, seen map[string]bool, newline string) string {
	var b strings.Builder
	for _, field := range fields {
		if !seen[field.key] {
			b.WriteString(relayFieldLine(field, newline))
		}
	}
	return b.String()
}

type relayConfigSnapshot struct {
	data   []byte
	exists bool
}

func readRelayConfig(path string) (relayConfigSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return relayConfigSnapshot{}, nil
	}
	if err != nil {
		return relayConfigSnapshot{}, fmt.Errorf("无法检查现有配置：%w", err)
	}
	if !info.Mode().IsRegular() {
		return relayConfigSnapshot{}, errors.New("config.toml 必须是普通文件，不能是目录或符号链接")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return relayConfigSnapshot{}, fmt.Errorf("无法读取现有配置：%w", err)
	}
	return relayConfigSnapshot{data: data, exists: true}, nil
}

func (original relayConfigSnapshot) checkCurrent(path string) error {
	current, err := readRelayConfig(path)
	if err != nil {
		return err
	}
	if current.exists != original.exists || !bytes.Equal(original.data, current.data) {
		return errors.New("config.toml 已在操作期间改变，请重新运行 relay")
	}
	return nil
}

// writeRelayFile gives each file a unique name and private permissions (0600).
func writeRelayFile(dir, pattern string, data []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func saveRelayConfig(dir string, original relayConfigSnapshot, settings relaySettings, catalog catalogFile) (backupPath, catalogPath string, err error) {
	configPath := filepath.Join(dir, "config.toml")
	if err := original.checkCurrent(configPath); err != nil {
		return "", "", err
	}
	if _, err := mergeRelayConfig(string(original.data), settings, ""); err != nil {
		return "", "", err
	}
	catalogData, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("无法生成模型目录：%w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("无法创建配置目录：%w", err)
	}
	if original.exists {
		pattern := "config.toml.bak-" + time.Now().Format("20060102T150405") + "-*"
		backupPath, err = writeRelayFile(dir, pattern, original.data)
		if err != nil {
			return "", "", fmt.Errorf("无法备份原配置，已取消写入：%w", err)
		}
	}

	// Publish a new catalog first. The old config and every backup can keep using
	// their previous catalog, and the new config only refers to a complete file.
	newCatalog, err := writeRelayFile(dir, settings.providerID+"-models-*.json", catalogData)
	if err != nil {
		return backupPath, "", fmt.Errorf("无法写入模型目录：%w", err)
	}
	defer func() {
		if catalogPath == "" {
			_ = os.Remove(newCatalog)
		}
	}()
	newConfig, err := mergeRelayConfig(string(original.data), settings, newCatalog)
	if err != nil {
		return backupPath, "", err
	}
	staged, err := writeRelayFile(dir, ".config.toml-*", []byte(newConfig))
	if err != nil {
		return backupPath, "", fmt.Errorf("无法写入配置临时文件：%w", err)
	}
	defer os.Remove(staged)
	// Make the backup and catalog directory entries durable before publishing
	// a config that refers to the catalog.
	parent, err := os.Open(dir)
	if err != nil {
		return backupPath, "", fmt.Errorf("无法打开配置目录：%w", err)
	}
	syncErr := parent.Sync()
	closeErr := parent.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return backupPath, "", fmt.Errorf("无法同步配置目录：%w", err)
	}
	if err := original.checkCurrent(configPath); err != nil {
		return backupPath, "", err
	}
	if original.exists {
		err = os.Rename(staged, configPath)
	} else {
		// Link does not overwrite a config created by another process.
		err = os.Link(staged, configPath)
	}
	if err != nil {
		return backupPath, "", fmt.Errorf("无法写入 config.toml：%w", err)
	}
	return backupPath, newCatalog, nil
}

func taskRelay() error {
	dir, err := filepath.Abs(ask("CODEX_HOME 目录", codexHomeDir()))
	if err != nil {
		return fmt.Errorf("无法确定配置目录：%w", err)
	}
	original, err := readRelayConfig(filepath.Join(dir, "config.toml"))
	if err != nil {
		return err
	}
	baseURL, err := relayURL(askRequired("中转站地址（通常以 /v1 结尾）"))
	if err != nil {
		return err
	}
	if !strings.HasSuffix(strings.TrimRight(baseURL.Path, "/"), "/v1") &&
		confirm("地址未以 /v1 结尾，追加 /v1", true) {
		baseURL = baseURL.JoinPath("v1")
	}
	settings := relaySettings{
		baseURL: strings.TrimRight(baseURL.String(), "/"),
		envKey:  ask("API 密钥的环境变量名（只填变量名，留空表示不使用 env_key）", ""),
	}
	var secret string
	if settings.envKey != "" {
		if !relayEnvPattern.MatchString(settings.envKey) {
			return errors.New("环境变量名只能使用字母、数字和下划线，不能以数字开头")
		}
		secret = os.Getenv(settings.envKey)
		if strings.TrimSpace(secret) == "" {
			return errors.New("该环境变量没有值，请先在启动本工具的环境中设置它")
		}
		if baseURL.Scheme != "https" {
			if !relayLoopback(baseURL.Hostname()) {
				return errors.New("使用密钥的中转地址必须是 HTTPS；HTTP 仅允许本机回环地址")
			}
			if !confirm("本机回环地址将通过 HTTP 发送密钥", false) {
				return nil
			}
		}
	}
	settings.providerID = ask("供应方 ID（config.toml 段名）", "relay")
	if !relayIDPattern.MatchString(settings.providerID) {
		return errors.New("供应方 ID 必须由 1 到 64 个字母、数字、连字符或下划线组成")
	}
	settings.providerName = ask("供应方显示名称", "我的中转站")
	if strings.ContainsFunc(settings.providerName, unicode.IsControl) {
		return errors.New("供应方显示名称不能含控制字符")
	}
	_, err = mergeRelayConfig(string(original.data), settings, "")
	if errors.Is(err, errRelayLegacyToken) {
		if !confirm("移除所选供应方的旧明文密钥（原配置备份仍含旧密钥）", false) {
			return nil
		}
		settings.removeLegacyToken = true
	} else if err != nil {
		return err
	}

	fmt.Println("固定模型列表：")
	settings.activeModel = pickModel(fixedRelayModels, "启动后默认使用的模型")
	contextWindow, err := strconv.Atoi(ask("上下文窗口大小", "272000"))
	if err != nil || contextWindow <= 0 {
		return errors.New("上下文窗口大小必须是正整数")
	}
	vision := confirm("模型支持图片输入", false)

	fmt.Printf("供应方 %s，默认模型 %s，接口协议 responses\n", settings.providerID, settings.activeModel)
	fmt.Println("配置文件：", filepath.Join(dir, "config.toml"))
	if !confirm("备份已有配置并写入新的中转配置", false) {
		return nil
	}
	backup, catalog, err := saveRelayConfig(dir, original, settings, buildCatalog(fixedRelayModels, settings.providerName, contextWindow, vision))
	if backup != "" {
		fmt.Println("原配置备份：", backup)
	}
	if err != nil {
		return err
	}
	fmt.Println("模型目录：", catalog)
	fmt.Println("配置已写入，请手动重启 Codex。如使用 env_key，请确保 Codex 进程也能读取该环境变量。")
	return nil
}
