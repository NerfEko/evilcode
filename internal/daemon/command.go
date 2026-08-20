package daemon

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// Command applies a UI action to the server-owned runtime. Commands are
// deliberately named: credentials and maintenance operations must not be
// smuggled through ordinary prompts, where they would become conversation
// instead of durable runtime state.
func (sess *Session) Command(kind, arg, secret string) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "poke":
		if sess.poke == nil {
			return fmt.Errorf("auto-poke is not configured for this session")
		}
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "on":
			sess.poke.SetEnabled(true)
		case "off", "stop":
			sess.poke.SetEnabled(false)
		case "", "status":
		default:
			return fmt.Errorf("usage: /poke [on|off|status]")
		}
		sess.notice(sess.pokeStatus())
		return nil

	case "advisor":
		if sess.advisor == nil {
			return fmt.Errorf("advisor is not configured for this session")
		}
		switch strings.ToLower(strings.TrimSpace(arg)) {
		case "on":
			sess.advisor.SetEnabled(true)
		case "off":
			sess.advisor.SetEnabled(false)
		case "", "status":
		default:
			return fmt.Errorf("usage: /advisor [on|off|status]")
		}
		sess.notice(sess.advisor.Status())
		return nil

	case "memory":
		return sess.memoryCommand(arg)

	case "connect":
		if strings.ToLower(strings.TrimSpace(arg)) != "brave" {
			return fmt.Errorf("usage: /connect brave")
		}
		if sess.built.Brave == nil {
			return fmt.Errorf("Brave Search is not available in this session")
		}
		if sess.busy() {
			return fmt.Errorf("finish or interrupt the current turn first")
		}
		if err := config.SaveBraveSearchAPIKey(strings.TrimSpace(secret)); err != nil {
			return err
		}
		active := config.BraveSearchAPIKey()
		if active == "" {
			active = strings.TrimSpace(secret)
		}
		sess.built.Brave.APIKey = active
		sess.notice("Brave Search API key saved")
		return nil

	case "credential":
		return sess.setCredential(arg, secret)

	case "skills":
		if strings.ToLower(strings.TrimSpace(arg)) != "reload" {
			return fmt.Errorf("usage: /skills reload")
		}
		if sess.built.Skills == nil {
			return fmt.Errorf("skills are not configured for this session")
		}
		sess.built.Skills.Reload()
		var entries []agent.Skill
		for _, skill := range sess.built.Skills.Index() {
			entries = append(entries, agent.Skill{Name: skill.Name, Desc: skill.Desc, Path: skill.Path})
		}
		sess.built.Agent.Conv.SetSystemPrompt(agent.BuildSystemPrompt(
			sess.built.Project, entries, ""))
		sess.notice(fmt.Sprintf("Skills reloaded · %d available", len(sess.built.Skills.Index())))
		return nil

	case "lsp":
		if sess.built.LSP == nil {
			return fmt.Errorf("no language servers are configured for this session")
		}
		sess.notice(sess.lspStatus())
		return nil

	case "overnight":
		return sess.overnightCommand(arg)

	case "save", "unsave":
		pinned := kind == "save"
		if err := session.Save(config.DataDir(), sess.Name, pinned); err != nil {
			return err
		}
		if pinned {
			sess.notice("📌 Saved " + sess.Name)
		} else {
			sess.notice("Unpinned " + sess.Name)
		}
		return nil

	case "rename":
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("usage: /rename <new-name>")
		}
		if err := sess.srv.renameSession(sess, strings.TrimSpace(arg)); err != nil {
			return err
		}
		sess.notice("Renamed to " + sess.Name)
		sess.publishSnapshot()
		return nil

	case "fork":
		if strings.TrimSpace(arg) == "" {
			return fmt.Errorf("usage: /fork <new-name>")
		}
		if err := session.Fork(config.DataDir(), sess.Name, strings.TrimSpace(arg)); err != nil {
			return err
		}
		sess.notice("Forked to " + strings.TrimSpace(arg))
		return nil

	case "checkpoint":
		label := strings.TrimSpace(arg)
		if label == "" {
			label = fmt.Sprintf("checkpoint-%d", sess.built.Agent.Conv.Len())
		}
		if err := sess.built.Store.WriteCheckpoint(label); err != nil {
			return err
		}
		sess.notice("Checkpoint " + label)
		return nil

	case "rewind":
		return sess.rewind(arg)

	case "compact":
		return sess.compact()

	default:
		return fmt.Errorf("unknown server command %q", kind)
	}
}

func (sess *Session) pokeStatus() string {
	if sess.poke.Enabled() {
		return "Auto-poke is ON · /poke off to stop"
	}
	return "Auto-poke is OFF · /poke on to enable"
}

func (sess *Session) memoryCommand(arg string) error {
	if sess.built.Memory == nil {
		return fmt.Errorf("memory is not configured for this session")
	}
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch strings.ToLower(verb) {
	case "on":
		sess.built.Memory.SetEnabled(true)
		sess.notice("🧠 Memory ON")
	case "off":
		sess.built.Memory.SetEnabled(false)
		sess.notice("🧠 Memory OFF")
	case "forget":
		id, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			return fmt.Errorf("usage: /memory forget <id>")
		}
		found, err := sess.built.Memory.Forget(id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("no memory #%d", id)
		}
		sess.notice(fmt.Sprintf("🧠 Forgot #%d", id))
	case "", "status", "list":
		if strings.EqualFold(verb, "list") {
			sess.notice(sess.memoryList(rest))
			return nil
		}
		state := "OFF"
		if sess.built.Memory.Enabled() {
			state = "ON"
		}
		sess.notice(fmt.Sprintf("🧠 Memory %s · %d remembered · %s",
			state, len(sess.built.Memory.All()), sess.built.Memory.ScopeLabel()))
	default:
		return fmt.Errorf("usage: /memory [on|off|forget <id>|list]")
	}
	return nil
}

func (sess *Session) memoryList(rawScope string) string {
	scope := memory.Scope(strings.TrimSpace(rawScope))
	if scope != "" && !scope.Valid() {
		return "usage: /memory list [project|global]"
	}
	all := sess.built.Memory.List(scope)
	if len(all) == 0 {
		if scope == "" {
			return "🧠 Nothing remembered in the current project/global view."
		}
		return fmt.Sprintf("🧠 Nothing remembered in %s scope.", scope)
	}
	view := "project + global"
	if scope != "" {
		view = string(scope)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🧠 %d memories in %s scope, newest first:\n", len(all), view)
	for i, rec := range all {
		if i == 20 {
			fmt.Fprintf(&b, "… and %d more\n", len(all)-20)
			break
		}
		fmt.Fprintf(&b, "#%d (%s) %s\n", rec.ID, rec.Kind, memory.Truncate(rec.Text, 72))
	}
	return strings.TrimRight(b.String(), "\n")
}

func (sess *Session) setCredential(target, key string) error {
	target = strings.TrimSpace(target)
	key = strings.TrimSpace(key)
	if target == "" || key == "" {
		return fmt.Errorf("provider and key are required")
	}
	if err := config.SaveProviderAPIKey(target, key); err != nil {
		return err
	}
	if sess.built.Agent.Provider != nil && sess.built.Agent.Provider.Name() == target {
		switch p := sess.built.Agent.Provider.(type) {
		case *provider.Ollama:
			p.APIKey = key
		case *provider.OpenAI:
			p.APIKey = key
		}
	}
	sess.notice(target + " API key saved")
	return nil
}

func (sess *Session) lspStatus() string {
	statuses := sess.built.LSP.Status()
	if len(statuses) == 0 {
		return "no language servers are configured"
	}
	var b strings.Builder
	b.WriteString("Language servers:\n")
	for _, status := range statuses {
		state := "ready, not started"
		if status.Running {
			state = "running"
		} else if status.Err != "" {
			state = status.Err
		}
		fmt.Fprintf(&b, "● %-11s %s — %s\n", status.Language, status.Command, state)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (sess *Session) compact() error {
	if sess.built.Agent.Compactor == nil || !sess.built.Agent.Compactor.Enabled() {
		return fmt.Errorf("compaction is not configured for this session")
	}
	if sess.busy() {
		return fmt.Errorf("finish or interrupt the current turn first")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	before := sess.built.Agent.Conv.Len()
	summary, err := sess.built.Agent.Compactor.Compact(ctx, sess.built.Agent.Conv)
	if err != nil {
		return err
	}
	sess.notice(fmt.Sprintf("📦 Compacted %d messages into a summary\n\n%s", before, summary))
	sess.publishSnapshot()
	return nil
}

func (sess *Session) notice(text string) {
	sess.publishEvent(agent.Event{Kind: agent.EventNotice, Text: text, Level: agent.LevelInfo})
}

func (sess *Session) overnightCommand(arg string) error {
	if sess.overnight == nil {
		return fmt.Errorf("overnight is not configured for this session")
	}
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "off", "stop":
		if sess.overnight.stop("you stopped it") {
			sess.cancelTurn()
			_, _ = sess.writeOvernightReport()
		}
		sess.notice(sess.overnight.status())
		return nil
	case "status":
		sess.notice(sess.overnight.status())
		return nil
	case "report":
		path, err := sess.writeOvernightReport()
		if err != nil {
			return err
		}
		if path == "" {
			sess.notice(sess.overnight.status() + " · no overnight run has been recorded yet")
		} else {
			sess.notice(sess.overnight.status() + " · report: " + path)
		}
		return nil
	case "":
		if sess.built.Todos == nil || strings.TrimSpace(sess.built.Todos.Summary()) == "" {
			return fmt.Errorf("overnight needs a todo list to work through")
		}
		if sess.busy() {
			return fmt.Errorf("finish or interrupt the current turn first")
		}
		if err := sess.overnight.start(time.Now()); err != nil {
			return err
		}
		sess.notice(fmt.Sprintf("⏳ Overnight armed · at most %d turns, %d tokens, %d hours", overnightMaxTurns, overnightBudget, overnightHours))
		sess.InputRequest("overnight-1", overnightPrompt)
		return nil
	default:
		return fmt.Errorf("usage: /overnight [off|status|report]")
	}
}

func (sess *Session) rewind(arg string) error {
	if sess.busy() {
		return fmt.Errorf("finish or interrupt the current turn first")
	}
	points, err := session.RewindPoints(sess.built.Store.Path)
	if err != nil {
		return err
	}
	if len(points) == 0 {
		return fmt.Errorf("nothing to rewind to yet")
	}
	if strings.TrimSpace(arg) == "" {
		var b strings.Builder
		b.WriteString("Rewind points — /rewind N to collapse back to one\n")
		for _, point := range points {
			fmt.Fprintf(&b, "  %d  %s\n", point.Index, strings.ReplaceAll(point.Prompt, "\n", " "))
		}
		sess.notice(strings.TrimRight(b.String(), "\n"))
		return nil
	}
	var n int
	if _, err := fmt.Sscanf(strings.TrimSpace(arg), "%d", &n); err != nil || n < 1 || n > len(points) {
		return fmt.Errorf("usage: /rewind 1..%d", len(points))
	}
	before := sess.built.Agent.Conv.Messages()
	if len(before) > 0 && before[0].Role == provider.RoleSystem {
		before = before[1:]
	}
	kept, err := sess.built.Store.Rewind(config.DataDir(), points[n-1].Entry)
	if err != nil {
		return err
	}
	if sess.built.Agent.Compactor != nil {
		sess.built.Agent.Compactor.ResetSemanticHistory()
	}
	discarded := before
	if len(before) > len(kept) {
		discarded = before[len(kept):]
	}
	sess.built.Agent.Conv.Reset(kept)
	if summary := session.CollapseSummary(discarded); summary != "" {
		sess.built.Agent.Conv.Append(provider.Message{Role: provider.RoleUser, Content: summary})
	}
	sess.notice(fmt.Sprintf("Rewound to point %d · a summary of what was pruned was kept", n))
	sess.publishSnapshot()
	return nil
}
