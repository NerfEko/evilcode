package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"evilcode/internal/provider"
)

// ExternalSource identifies one supported session writer.
type ExternalSource string

const (
	SourceClaude   ExternalSource = "claude"
	SourceCodex    ExternalSource = "codex"
	SourceOpenCode ExternalSource = "opencode"
)

// ImportedMessage is a provider message with the source timestamp retained
// while it is written into the native JSONL session.
type ImportedMessage struct {
	Message   provider.Message
	Timestamp time.Time
}

// ExternalSession is the normalized result of parsing one foreign transcript.
type ExternalSession struct {
	Source    ExternalSource
	SourceID  string
	Title     string
	Cwd       string
	Model     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Messages  []ImportedMessage
}

// ImportInfo describes the native session created from a foreign transcript.
type ImportInfo struct {
	Name     string
	Source   ExternalSource
	SourceID string
	Title    string
	Messages int
}

// ImportExternal resolves an external session ID, converts it, and writes a
// durable native session ready for the ordinary -resume path.
func ImportExternal(dataDir string, source ExternalSource, id string) (ImportInfo, error) {
	source, err := normalizeExternalSource(source)
	if err != nil {
		return ImportInfo{}, err
	}
	path, err := resolveExternalPath(source, id)
	if err != nil {
		return ImportInfo{}, err
	}
	return ImportExternalFile(dataDir, source, path, id)
}

// ImportExternalFile is the path-oriented form used by tests and callers that
// already discovered a transcript.
func ImportExternalFile(dataDir string, source ExternalSource, path, idHint string) (ImportInfo, error) {
	source, err := normalizeExternalSource(source)
	if err != nil {
		return ImportInfo{}, err
	}
	external, err := ParseExternalFile(source, path, idHint)
	if err != nil {
		return ImportInfo{}, err
	}
	if len(external.Messages) == 0 {
		return ImportInfo{}, fmt.Errorf("%s session %q has no user or assistant messages", source, external.SourceID)
	}

	name := importedSessionName(source, external.SourceID, path)
	nativePath := filepath.Join(Dir(dataDir), name+".jsonl")
	if _, err := os.Stat(nativePath); err == nil {
		// Imports are stable by source identity. Never append a second copy over
		// a native continuation; the first imported session is the one to resume.
		messages, readErr := Messages(nativePath)
		if readErr != nil {
			return ImportInfo{}, fmt.Errorf("existing imported session %q: %w", name, readErr)
		}
		return ImportInfo{Name: name, Source: source, SourceID: external.SourceID,
			Title: external.Title, Messages: len(messages)}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ImportInfo{}, err
	}

	store, err := CreateNamed(dataDir, name)
	if err != nil {
		return ImportInfo{}, fmt.Errorf("create imported session: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = store.Close()
			_ = os.Remove(store.Path)
			_ = os.RemoveAll(blobDir(store.Path))
		}
	}()

	for _, imported := range external.Messages {
		if err := appendImportedMessage(store, imported); err != nil {
			return ImportInfo{}, fmt.Errorf("write imported message: %w", err)
		}
	}
	if external.Title != "" {
		if err := store.WriteMeta(Meta{Kind: MetaTitle, Note: external.Title}); err != nil {
			return ImportInfo{}, err
		}
	}
	if err := store.WriteMeta(Meta{
		Kind: MetaImport, Source: string(source), SourceID: external.SourceID,
		Cwd: external.Cwd, Note: external.Model,
	}); err != nil {
		return ImportInfo{}, err
	}
	if err := store.Close(); err != nil {
		return ImportInfo{}, err
	}
	removeOnError = false
	return ImportInfo{Name: name, Source: source, SourceID: external.SourceID,
		Title: external.Title, Messages: len(external.Messages)}, nil
}

// ParseExternalFile normalizes one supported source without touching the
// native data directory.
func ParseExternalFile(source ExternalSource, path, idHint string) (ExternalSession, error) {
	source, err := normalizeExternalSource(source)
	if err != nil {
		return ExternalSession{}, err
	}
	switch source {
	case SourceClaude:
		return parseClaudeFile(path, idHint)
	case SourceCodex:
		return parseCodexFile(path, idHint)
	case SourceOpenCode:
		return parseOpenCodeFile(path, idHint)
	default:
		return ExternalSession{}, fmt.Errorf("unsupported external source %q", source)
	}
}

func normalizeExternalSource(source ExternalSource) (ExternalSource, error) {
	switch strings.ToLower(strings.TrimSpace(string(source))) {
	case "claude", "claude-code":
		return SourceClaude, nil
	case "codex", "openai-codex":
		return SourceCodex, nil
	case "opencode", "open-code":
		return SourceOpenCode, nil
	default:
		return "", fmt.Errorf("external source must be claude, codex, or opencode")
	}
}

func importedSessionName(source ExternalSource, sourceID, path string) string {
	identity := sourceID
	if identity == "" {
		identity = path
	}
	sum := sha256.Sum256([]byte(string(source) + "\x00" + identity))
	return "imported_" + string(source) + "_" + hex.EncodeToString(sum[:8])
}

func resolveExternalPath(source ExternalSource, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("external session id is required")
	}
	if info, err := os.Stat(id); err == nil && info.Mode().IsRegular() {
		return id, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(home, ".claude", "projects")
	switch source {
	case SourceCodex:
		root = filepath.Join(home, ".codex", "sessions")
	case SourceOpenCode:
		root = filepath.Join(home, ".local", "share", "opencode", "storage", "session")
	}
	var found string
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrPermission) {
				return filepath.SkipDir
			}
			return nil
		}
		if found != "" || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if source == SourceClaude && strings.Contains(filepath.ToSlash(path), "/subagents/") {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		wantExt := ".jsonl"
		if source == SourceOpenCode {
			wantExt = ".json"
		}
		if ext != wantExt {
			return nil
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if stem == id || strings.Contains(stem, id) {
			found = path
			return nil
		}
		if source == SourceCodex {
			if headerID, _ := externalFileID(source, path); headerID == id {
				found = path
			}
		} else if source == SourceOpenCode {
			if headerID, _ := externalFileID(source, path); headerID == id {
				found = path
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("%s session %q was not found under %s", source, id, root)
	}
	return found, nil
}

func externalFileID(source ExternalSource, path string) (string, error) {
	if source == SourceOpenCode {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return "", err
		}
		id, _ := value["id"].(string)
		return id, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	const maxExternalHeaderBytes = 1 << 20
	line, readErr := bufio.NewReader(io.LimitReader(file, maxExternalHeaderBytes+1)).ReadBytes('\n')
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", readErr
	}
	if len(line) > maxExternalHeaderBytes {
		return "", fmt.Errorf("external session header exceeds %d bytes", maxExternalHeaderBytes)
	}
	line = []byte(strings.TrimSpace(string(line)))
	var value map[string]any
	if err := json.Unmarshal(line, &value); err != nil {
		return "", err
	}
	if payload, ok := value["payload"].(map[string]any); ok {
		if id, ok := payload["id"].(string); ok {
			return id, nil
		}
	}
	id, _ := value["id"].(string)
	return id, nil
}

func appendImportedMessage(store *Store, imported ImportedMessage) error {
	data, err := encodeMessage(store.Path, imported.Message)
	if err != nil {
		return err
	}
	typeFor := TypeMeta
	switch imported.Message.Role {
	case provider.RoleUser:
		typeFor = TypeUser
	case provider.RoleAssistant:
		typeFor = TypeAssistant
	case provider.RoleTool:
		typeFor = TypeTool
	}
	return store.Append(Entry{TS: imported.Timestamp, Type: typeFor, Data: data})
}

func parseClaudeFile(path, idHint string) (ExternalSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return ExternalSession{}, err
	}
	defer file.Close()

	session := ExternalSession{Source: SourceClaude}
	toolNames := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if session.SourceID == "" {
			session.SourceID, _ = raw["sessionId"].(string)
			if session.SourceID == "" {
				session.SourceID, _ = raw["session_id"].(string)
			}
		}
		if session.Cwd == "" {
			session.Cwd, _ = raw["cwd"].(string)
		}
		timestamp := parseExternalTime(raw["timestamp"])
		if timestamp.IsZero() {
			timestamp = parseExternalTime(raw["created_at"])
		}
		if !timestamp.IsZero() {
			if session.CreatedAt.IsZero() {
				session.CreatedAt = timestamp
			}
			session.UpdatedAt = timestamp
		}
		messages, model := parseClaudeMessages(raw)
		if model != "" && session.Model == "" {
			session.Model = model
		}
		for _, message := range messages {
			for _, call := range message.ToolCalls {
				if call.ID != "" && call.Name != "" {
					toolNames[call.ID] = call.Name
				}
			}
			if message.Role == provider.RoleTool && message.ToolName == "" {
				message.ToolName = toolNames[message.ToolCallID]
			}
			if message.Role == provider.RoleUser && session.Title == "" && !isSyntheticExternalText(message.Content) {
				session.Title = truncateImportTitle(message.Content)
			}
			messageTimestamp := timestamp
			session.Messages = append(session.Messages, ImportedMessage{Message: message, Timestamp: messageTimestamp})
		}
	}
	if err := scanner.Err(); err != nil {
		return ExternalSession{}, err
	}
	if session.SourceID == "" {
		session.SourceID = idHint
	}
	if session.SourceID == "" {
		session.SourceID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if session.Title == "" {
		session.Title = "Claude session " + session.SourceID
	}
	return session, nil
}

func parseClaudeMessages(raw map[string]any) ([]provider.Message, string) {
	roleText, _ := raw["type"].(string)
	message, _ := raw["message"].(map[string]any)
	if message == nil {
		message = raw
	}
	if value, ok := message["role"].(string); ok {
		roleText = value
	}
	var role provider.Role
	switch roleText {
	case "user":
		role = provider.RoleUser
	case "assistant":
		role = provider.RoleAssistant
	default:
		return nil, ""
	}
	model, _ := message["model"].(string)
	content := message["content"]
	if text, ok := content.(string); ok {
		if isSyntheticExternalText(text) {
			return nil, model
		}
		return []provider.Message{{Role: role, Content: text}}, model
	}
	blocks, _ := content.([]any)
	var text []string
	var reasoning []string
	var calls []provider.ToolCall
	var results []provider.Message
	for _, item := range blocks {
		block, _ := item.(map[string]any)
		kind, _ := block["type"].(string)
		switch kind {
		case "text":
			if value, ok := block["text"].(string); ok {
				text = append(text, value)
			}
		case "thinking":
			if value, ok := block["thinking"].(string); ok {
				reasoning = append(reasoning, value)
			}
		case "tool_use":
			id, _ := block["id"].(string)
			name, _ := block["name"].(string)
			args, _ := json.Marshal(block["input"])
			calls = append(calls, provider.ToolCall{ID: id, Name: name, Args: args})
		case "tool_result":
			id, _ := block["tool_use_id"].(string)
			isError, _ := block["is_error"].(bool)
			results = append(results, provider.Message{Role: provider.RoleTool, ToolCallID: id, Content: externalText(block["content"]), IsError: isError})
		}
	}
	textValue := strings.TrimSpace(strings.Join(text, "\n"))
	reasoningValue := strings.TrimSpace(strings.Join(reasoning, "\n"))
	if isSyntheticExternalText(textValue) {
		return nil, model
	}
	if textValue == "" && reasoningValue == "" && len(calls) == 0 {
		return results, model
	}
	messageValue := provider.Message{Role: role, Content: textValue, Reasoning: reasoningValue, ToolCalls: calls}
	return append([]provider.Message{messageValue}, results...), model
}

func parseCodexFile(path, idHint string) (ExternalSession, error) {
	file, err := os.Open(path)
	if err != nil {
		return ExternalSession{}, err
	}
	defer file.Close()
	session := ExternalSession{Source: SourceCodex}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var raw map[string]any
		if json.Unmarshal(scanner.Bytes(), &raw) != nil {
			continue
		}
		lineType, _ := raw["type"].(string)
		payload, _ := raw["payload"].(map[string]any)
		meta := raw
		if lineType == "session_meta" && payload != nil {
			meta = payload
		}
		if session.SourceID == "" {
			session.SourceID, _ = meta["id"].(string)
		}
		if session.Cwd == "" {
			session.Cwd, _ = meta["cwd"].(string)
		}
		if session.Model == "" {
			session.Model, _ = meta["model"].(string)
		}
		if lineType == "session_meta" {
			continue
		}
		message := raw
		if lineType == "response_item" && payload != nil {
			if kind, _ := payload["type"].(string); kind != "message" {
				continue
			}
			message = payload
		} else if lineType != "message" {
			continue
		}
		roleText, _ := message["role"].(string)
		var role provider.Role
		switch roleText {
		case "user":
			role = provider.RoleUser
		case "assistant":
			role = provider.RoleAssistant
		default:
			continue
		}
		text := externalText(message["content"])
		if text == "" {
			text = externalText(message["summary"])
		}
		if text == "" {
			continue
		}
		timestamp := parseExternalTime(raw["timestamp"])
		if timestamp.IsZero() {
			timestamp = parseExternalTime(message["timestamp"])
		}
		if !timestamp.IsZero() {
			if session.CreatedAt.IsZero() {
				session.CreatedAt = timestamp
			}
			session.UpdatedAt = timestamp
		}
		if session.Title == "" && role == provider.RoleUser {
			session.Title = truncateImportTitle(text)
		}
		session.Messages = append(session.Messages, ImportedMessage{Message: provider.Message{Role: role, Content: text}, Timestamp: timestamp})
	}
	if err := scanner.Err(); err != nil {
		return ExternalSession{}, err
	}
	if session.SourceID == "" {
		session.SourceID = idHint
	}
	if session.SourceID == "" {
		session.SourceID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if session.Title == "" {
		session.Title = "Codex session " + session.SourceID
	}
	return session, nil
}

func parseOpenCodeFile(path, idHint string) (ExternalSession, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ExternalSession{}, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ExternalSession{}, err
	}
	session := ExternalSession{Source: SourceOpenCode, SourceID: idHint}
	session.SourceID, _ = raw["id"].(string)
	if session.SourceID == "" {
		session.SourceID = idHint
	}
	session.Title, _ = raw["title"].(string)
	session.Title = truncateImportTitle(session.Title)
	session.Cwd, _ = raw["directory"].(string)
	if times, ok := raw["time"].(map[string]any); ok {
		session.CreatedAt = parseUnixMillis(times["created"])
		session.UpdatedAt = parseUnixMillis(times["updated"])
	}
	storageRoot := openCodeStorageRoot(path)
	messageRoot := filepath.Join(storageRoot, "message", session.SourceID)
	partRoot := filepath.Join(storageRoot, "part")
	var messageFiles []string
	_ = filepath.WalkDir(messageRoot, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr == nil && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			messageFiles = append(messageFiles, filePath)
		}
		return nil
	})
	sort.Strings(messageFiles)
	for _, messagePath := range messageFiles {
		messageData, readErr := os.ReadFile(messagePath)
		if readErr != nil {
			continue
		}
		var message map[string]any
		if json.Unmarshal(messageData, &message) != nil {
			continue
		}
		roleText, _ := message["role"].(string)
		var role provider.Role
		switch roleText {
		case "user":
			role = provider.RoleUser
		case "assistant":
			role = provider.RoleAssistant
		default:
			continue
		}
		messageID, _ := message["id"].(string)
		if session.Model == "" {
			session.Model, _ = message["modelID"].(string)
			if session.Model == "" {
				if model, ok := message["model"].(map[string]any); ok {
					session.Model, _ = model["modelID"].(string)
				}
			}
		}
		text := externalText(message["content"])
		if messageID != "" {
			text = strings.TrimSpace(strings.Join([]string{text, readOpenCodeParts(partRoot, messageID)}, "\n"))
		}
		if text == "" {
			continue
		}
		timestamp := parseOpenCodeTime(message["time"])
		if timestamp.IsZero() {
			if stat, statErr := os.Stat(messagePath); statErr == nil {
				timestamp = stat.ModTime()
			}
		}
		if session.Title == "" && role == provider.RoleUser {
			session.Title = truncateImportTitle(text)
		}
		session.Messages = append(session.Messages, ImportedMessage{Message: provider.Message{Role: role, Content: text}, Timestamp: timestamp})
	}
	if len(session.Messages) == 0 {
		if inline, ok := raw["messages"].([]any); ok {
			for _, item := range inline {
				message, _ := item.(map[string]any)
				roleText, _ := message["role"].(string)
				if roleText != "user" && roleText != "assistant" {
					continue
				}
				text := externalText(message["content"])
				if text == "" {
					continue
				}
				role := provider.RoleUser
				if roleText == "assistant" {
					role = provider.RoleAssistant
				}
				session.Messages = append(session.Messages, ImportedMessage{Message: provider.Message{Role: role, Content: text}})
			}
		}
	}
	sort.SliceStable(session.Messages, func(a, b int) bool {
		aZero := session.Messages[a].Timestamp.IsZero()
		bZero := session.Messages[b].Timestamp.IsZero()
		if aZero != bZero {
			return !aZero
		}
		if aZero {
			return false
		}
		return session.Messages[a].Timestamp.Before(session.Messages[b].Timestamp)
	})
	if session.SourceID == "" {
		session.SourceID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if session.Title == "" {
		session.Title = "OpenCode session " + session.SourceID
	}
	return session, nil
}

func openCodeStorageRoot(sessionPath string) string {
	for candidate := filepath.Dir(sessionPath); ; candidate = filepath.Dir(candidate) {
		messageDir := filepath.Join(candidate, "message")
		partDir := filepath.Join(candidate, "part")
		messageInfo, messageErr := os.Stat(messageDir)
		partInfo, partErr := os.Stat(partDir)
		if (messageErr == nil && messageInfo.IsDir()) || (partErr == nil && partInfo.IsDir()) {
			return candidate
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	return filepath.Dir(filepath.Dir(sessionPath))
}

func readOpenCodeParts(root, messageID string) string {
	dir := filepath.Join(root, messageID)
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && filepath.Ext(entry.Name()) == ".json" {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	var text []string
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var part map[string]any
		if json.Unmarshal(data, &part) != nil {
			continue
		}
		kind, _ := part["type"].(string)
		switch kind {
		case "text", "reasoning", "output_text":
			if value, ok := part["text"].(string); ok && strings.TrimSpace(value) != "" {
				text = append(text, value)
			}
		case "tool":
			state, _ := part["state"].(map[string]any)
			input := externalText(state["input"])
			output := externalText(state["output"])
			if input != "" || output != "" {
				text = append(text, strings.TrimSpace(strings.Join([]string{"[tool] " + input, output}, "\n")))
			}
		}
	}
	return strings.Join(text, "\n")
}

func externalText(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case []any:
		var parts []string
		for _, item := range value {
			if text := externalText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if kind, _ := value["type"].(string); kind == "image" {
			return "[image]"
		}
		for _, key := range []string{"text", "content", "summary", "message"} {
			if nested, ok := value[key]; ok {
				if text := externalText(nested); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func parseExternalTime(value any) time.Time {
	switch value := value.(type) {
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	case float64:
		if value > 1e12 {
			return time.UnixMilli(int64(value))
		}
		return time.Unix(int64(value), 0)
	case json.Number:
		if number, err := value.Int64(); err == nil {
			if number > 1e12 {
				return time.UnixMilli(number)
			}
			return time.Unix(number, 0)
		}
	}
	return time.Time{}
}

func parseUnixMillis(value any) time.Time {
	number, ok := value.(float64)
	if !ok || number <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(number))
}

func parseOpenCodeTime(value any) time.Time {
	if object, ok := value.(map[string]any); ok {
		if created := parseUnixMillis(object["created"]); !created.IsZero() {
			return created
		}
	}
	return parseExternalTime(value)
}

func isSyntheticExternalText(text string) bool {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{
		"<local-command-caveat>", "<local-command-stdout>", "<local-command-stderr>",
		"<command-name>", "<command-message>", "<command-args>",
	} {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func truncateImportTitle(text string) string {
	text = strings.TrimSpace(strings.Split(text, "\n")[0])
	runes := []rune(text)
	if len(runes) <= 80 {
		return text
	}
	return string(runes[:77]) + "..."
}
