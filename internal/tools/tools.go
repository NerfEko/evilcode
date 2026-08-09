// Package tools implements the agent's tool set (plan.md §17). Tools are a
// plain slice, not a registry: there is one process, one user, and a handful of
// tools — an indirection layer would buy nothing.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// MaxResultBytes caps what a tool may return. A tool that dumps a whole
// repository into the context window is a cost bug, not a feature.
const MaxResultBytes = 50 * 1024

// DiffStat counts changed lines, for the `(+8 -5)` badge on edit rows (§9.5).
type DiffStat struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// Result is what a tool produces. Output is what the model sees; the rest is
// display metadata the TUI renders and the model never pays for.
type Result struct {
	// Output is the text handed back to the model.
	Output string

	// Diff is a unified diff for tools that changed a file, rendered by §9.3.
	Diff string

	// DiffStat accompanies Diff.
	DiffStat *DiffStat

	// Intent is a short human-readable summary for the tool row, when the tool
	// can describe itself better than its arguments do.
	Intent string

	// Display carries a tool-specific payload for the UI to render. It travels
	// with the result through the event channel rather than via a side channel,
	// which is what keeps it free of races with the render loop.
	Display any

	// Images are raw image bytes a tool result attaches for a vision model, and
	// for the UI to render inline. `read` sets it for an image file; every other
	// tool leaves it empty. The session store content-addresses the bytes into a
	// blob beside the log rather than inline, so a large picture cannot truncate
	// the replay.
	Images [][]byte

	// NoWrite is set by a write-capable tool that did not actually write (a
	// fully-failed multiedit), so swarm coordination does not queue a stale-file
	// notice for a file that never changed. write/edit leave it false.
	NoWrite bool

	// Repairs names the argument rewrites RunOne applied before the tool's
	// strict decode (an aliased field, a string-wrapped number coerced). It is
	// silent to the model — the output is unchanged — and shown in the tool row
	// so a quietly rewritten argument is findable later (§1.4).
	Repairs []string

	// EffectiveArgs is the repaired argument object used to run the tool. It is
	// kept out of the model result, but the agent copies it onto the finished
	// event so consumers such as daemon file-conflict tracking inspect the same
	// path the tool actually touched rather than the misspelled input.
	EffectiveArgs json.RawMessage `json:"-"`

	// Shown records source ranges included in Output. It is bookkeeping for the
	// session exposure ledger, never additional model-visible text.
	Shown []LineRange `json:"-"`

	// Held marks a command stopped by a safety gate. It is distinct from a
	// failed tool call so frontends can render a warning/reflection row without
	// implying that the command started and then failed.
	Held bool `json:"held,omitempty"`
}

// Tool is one callable capability.
type Tool struct {
	Name   string
	Desc   string
	Schema json.RawMessage

	// Exposure is the optional per-session ledger used to collapse repeated
	// source hits. It is attached by filesystem and command tool constructors.
	Exposure *Exposure

	// Run executes the tool. A returned error becomes an error tool result the
	// model can read and recover from — it does not abort the turn.
	Run func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Set is the collection of tools available to a turn.
type Set []Tool

// Find returns the tool with the given name.
func (s Set) Find(name string) (Tool, bool) {
	for _, t := range s {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Names lists the tool names in order.
func (s Set) Names() []string {
	out := make([]string, len(s))
	for i, t := range s {
		out[i] = t.Name
	}
	return out
}

// Call is one requested invocation.
type Call struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Outcome pairs a call with what running it produced.
type Outcome struct {
	Call   Call
	Result Result
	Err    error
}

// MaxConcurrent bounds parallel tool execution. Tools shell out and touch the
// filesystem; unbounded fan-out on a model that requests twenty calls would
// thrash the machine for no gain.
const MaxConcurrent = 8

// MaxBatch bounds how many calls one round may contain. The list comes from the
// model, so its length is not a fact about the machine: a run of degenerate
// output can ask for thousands, and every one of them is answered.
const MaxBatch = 64

// RunBatch executes calls concurrently and returns outcomes in the original
// order, so the transcript reads in the order the model asked rather than the
// order things happened to finish.
//
// A fixed pool rather than a goroutine per call. The semaphore bounded how many
// tools *ran*, which is what it was for, but the goroutines were created before
// it had any say — so a five-thousand-call round cost five thousand stacks and
// the scheduler that goes with them, whatever the concurrency cap said.
func (s Set) RunBatch(ctx context.Context, calls []Call) []Outcome {
	out := make([]Outcome, len(calls))

	// Anything past the cap is refused rather than dropped: an unanswered
	// tool_use is a transcript the provider rejects (H1.2).
	runnable := len(calls)
	if runnable > MaxBatch {
		runnable = MaxBatch
		for i := MaxBatch; i < len(calls); i++ {
			out[i] = Outcome{Call: calls[i], Err: fmt.Errorf(
				"this round asked for %d tool calls; %d is the limit. "+
					"Ask for fewer at a time", len(calls), MaxBatch)}
		}
	}

	workers := min(MaxConcurrent, runnable)
	queue := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				if ctx.Err() != nil {
					out[i] = Outcome{Call: calls[i], Err: ctx.Err()}
					continue
				}
				out[i] = s.RunOne(ctx, calls[i])
			}
		}()
	}
	for i := range runnable {
		select {
		case queue <- i:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(queue)
	wg.Wait()

	// Whatever was never dispatched is answered here. Every tool_use needs an
	// adjacent result (H1.2), and a cancelled batch that leaves blanks in the
	// middle of the slice is the same malformed transcript by another route.
	for i, o := range out {
		if o.Call.Name == "" && o.Err == nil {
			err := ctx.Err()
			if err == nil {
				err = fmt.Errorf("the tool was not run")
			}
			out[i] = Outcome{Call: calls[i], Err: err}
		}
	}
	return out
}

// RunOne executes a single call, converting a panic into an error rather than
// taking the process down: a bad tool argument must not kill a live session.
func (s Set) RunOne(ctx context.Context, call Call) (outcome Outcome) {
	outcome.Call = call
	defer func() {
		if r := recover(); r != nil {
			outcome.Err = fmt.Errorf("tool %q panicked: %v", call.Name, r)
		}
	}()

	tool, ok := s.Find(call.Name)
	if !ok {
		outcome.Err = fmt.Errorf("unknown tool %q (available: %s)",
			call.Name, strings.Join(s.Names(), ", "))
		return outcome
	}

	// Repair common argument mistakes before the tool's strict decode runs:
	// alias a name the model was trained on (command→cmd, file_path→path,
	// pattern→query, …) and coerce a number given as a string for the schema's
	// numeric fields. The repair is silent to the model — the tool runs against
	// the repaired args and its output is unchanged — but the repairs are
	// recorded on the result so the tool row can show what was rewritten (§1.4:
	// an argument quietly rewritten is one nobody finds later).
	repaired, repairs := repairArgs(call.Args, tool.Schema)
	res, err := tool.Run(ctx, repaired)
	res.Output = Truncate(res.Output)
	if tool.Exposure != nil {
		tool.Exposure.Record(res.Shown)
	}
	if len(repairs) > 0 {
		res.Repairs = repairs
		res.EffectiveArgs = repaired
	}
	outcome.Result = res
	outcome.Err = err
	return outcome
}

// Truncate caps a result, keeping both ends: the head says what the thing is
// and the tail usually holds the error or the summary, so cutting only the
// middle keeps a truncated result useful.
func Truncate(s string) string {
	if len(s) <= MaxResultBytes {
		return s
	}
	// How much was dropped is the useful half of the message — it is what tells
	// the model whether narrowing the request is worth it. Budget for the
	// longest count the note can carry rather than dropping it to stay under
	// the cap.
	const format = "\n\n… %d bytes of output truncated; narrow the request to see the rest …\n\n"
	available := MaxResultBytes - len(fmt.Sprintf(format, len(s)))
	head := available * 2 / 3
	tail := available - head
	// Cut on rune boundaries so truncation never emits a broken sequence.
	head = backToRuneBoundary(s, head)
	tailStart := forwardToRuneBoundary(s, len(s)-tail)
	return s[:head] + fmt.Sprintf(format, tailStart-head) + s[tailStart:]
}

func backToRuneBoundary(s string, i int) int {
	for i > 0 && !utf8Start(s[i]) {
		i--
	}
	return i
}

func forwardToRuneBoundary(s string, i int) int {
	for i < len(s) && !utf8Start(s[i]) {
		i++
	}
	return i
}

// utf8Start reports whether b can begin a UTF-8 sequence (i.e. is not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// argAliases maps parameter names from other agents' tool sets onto this one's.
// Models emit the names they were trained on far more stubbornly than they read
// the schema — deepseek sent "command" to bash five times running — and a strict
// decoder turns that into a retry loop the model cannot escape. Aliasing is only
// applied when the real name is absent, and the decode is still strict after, so
// a name this tool does not have is still an error.
var argAliases = map[string]string{
	"command":     "cmd",
	"file_path":   "path",
	"filePath":    "path",
	"old_string":  "old",
	"new_string":  "new",
	"replace_all": "all",
}

// repairAliases extends argAliases with names that are real fields in some
// tools and so cannot be renamed blindly: `pattern` is grep's real field, so
// `pattern`→`query` must only apply to a tool whose schema actually has
// `query`. repairArgs applies these conditionally on the schema.
var repairAliases = func() map[string]string {
	m := make(map[string]string, len(argAliases)+1)
	for k, v := range argAliases {
		m[k] = v
	}
	m["pattern"] = "query"
	return m
}()

// unmarshalArgs decodes tool arguments, rejecting unknown fields so a model
// that misspells a parameter is told rather than silently getting a default.
func unmarshalArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	err := strictDecode(raw, dst)
	if err == nil {
		return nil
	}
	// Retried only on failure, so the ordinary call pays nothing for this.
	if aliased, ok := applyAliases(raw); ok && strictDecode(aliased, dst) == nil {
		return nil
	}
	return fmt.Errorf("bad arguments: %w", err)
}

func strictDecode(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

// repairArgs fixes common argument mistakes against a tool's schema before
// the strict decode runs: rename a known alias onto the real field (only when
// the real field is absent), and coerce a number given as a string for the
// schema's numeric fields. It returns the repaired arguments and a list of
// human-readable repairs (empty when nothing changed). The repair is silent to
// the model; the repairs list is what the tool row shows.
func repairArgs(raw json.RawMessage, schema json.RawMessage) (json.RawMessage, []string) {
	var fields map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &fields) != nil {
		return raw, nil
	}
	var repairs []string
	// Repair aliases and numeric strings at every object level. Nested repair is
	// important for tools whose schemas contain arrays of argument objects, such
	// as multiedit.edits[].old_string/new_string/replace_all.
	repairObject(fields, schema, "", &repairs)

	if len(repairs) == 0 {
		return raw, nil
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return raw, repairs
	}
	return out, repairs
}

// repairObject walks one JSON object against its schema, applying conditional
// aliases and coercing string-wrapped numbers. Aliases are conditional on the
// current schema, so grep's real `pattern` field is never renamed to `query`.
func repairObject(obj map[string]json.RawMessage, schema json.RawMessage, prefix string, repairs *[]string) {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return
	}
	props := schemaProperties(schema)
	for _, alias := range sortedRepairAliases() {
		real := repairAliases[alias]
		if !props[real] {
			continue
		}
		v, ok := obj[alias]
		if !ok {
			continue
		}
		if _, taken := obj[real]; taken {
			continue
		}
		delete(obj, alias)
		obj[real] = v
		name := joinPath(prefix, alias) + "→" + joinPath(prefix, real)
		if prefix == "" {
			name = alias + "→" + real
		}
		*repairs = append(*repairs, name)
	}

	names := make([]string, 0, len(s.Properties))
	for name := range s.Properties {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		prop := s.Properties[name]
		v, ok := obj[name]
		if !ok {
			continue
		}
		if hasNumericType(prop) {
			if coerced, ok := coerceStringNumber(v); ok {
				obj[name] = coerced
				*repairs = append(*repairs, joinPath(prefix, name)+": string→number")
			}
			continue
		}
		// Recurse into a nested object or array of objects against the schema's
		// child definition. The recursion mutates fresh maps, so each level is
		// re-marshalled back into its slot.
		if child := childSchema(prop); child != nil {
			switch {
			case isObject(v):
				var m map[string]json.RawMessage
				if json.Unmarshal(v, &m) == nil {
					repairObject(m, child, joinPath(prefix, name), repairs)
					if b, err := json.Marshal(m); err == nil {
						obj[name] = b
					}
				}
			case isArray(v):
				var arr []json.RawMessage
				if json.Unmarshal(v, &arr) == nil {
					for i, item := range arr {
						var m map[string]json.RawMessage
						if json.Unmarshal(item, &m) == nil {
							repairObject(m, child, joinPath(prefix, name), repairs)
							if b, err := json.Marshal(m); err == nil {
								arr[i] = b
							}
						}
					}
					if b, err := json.Marshal(arr); err == nil {
						obj[name] = b
					}
				}
			}
		}
	}
}

func sortedRepairAliases() []string {
	out := make([]string, 0, len(repairAliases))
	for alias := range repairAliases {
		out = append(out, alias)
	}
	slices.Sort(out)
	return out
}

// childSchema returns the schema to validate a property's value against: the
// property's own schema for an object, or the items schema for an array.
func childSchema(prop json.RawMessage) json.RawMessage {
	var p struct {
		Type  any             `json:"type"`
		Items json.RawMessage `json:"items"`
	}
	if json.Unmarshal(prop, &p) != nil {
		return nil
	}
	switch t := p.Type.(type) {
	case string:
		if t == "array" {
			return p.Items
		}
		if t == "object" {
			return prop
		}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s == "array" {
				return p.Items
			}
			if s, ok := x.(string); ok && s == "object" {
				return prop
			}
		}
	}
	return nil
}

func isObject(v json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(v, &m) == nil
}

func isArray(v json.RawMessage) bool {
	var a []json.RawMessage
	return json.Unmarshal(v, &a) == nil
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

// numericFields lists the schema's properties whose type is integer or number.
// "type" may be a string or an array (e.g. ["integer", "null"]).
func numericFields(schema json.RawMessage) []string {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	var out []string
	for name, prop := range s.Properties {
		if hasNumericType(prop) {
			out = append(out, name)
		}
	}
	return out
}

// schemaProperties is the set of property names a tool's schema declares.
func schemaProperties(schema json.RawMessage) map[string]bool {
	out := map[string]bool{}
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return out
	}
	for name := range s.Properties {
		out[name] = true
	}
	return out
}

func hasNumericType(prop json.RawMessage) bool {
	var p struct {
		Type any `json:"type"`
	}
	if json.Unmarshal(prop, &p) != nil {
		return false
	}
	switch t := p.Type.(type) {
	case string:
		return t == "integer" || t == "number"
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && (s == "integer" || s == "number") {
				return true
			}
		}
	}
	return false
}

// coerceStringNumber turns a JSON string holding a finite number into a JSON
// number token. NaN/±Inf are rejected: json.Marshal would fail on them and
// leaving the field as a string makes strict decode reject the call, which is
// the honest outcome for an argument that is not a real number.
func coerceStringNumber(v json.RawMessage) (json.RawMessage, bool) {
	var s string
	if json.Unmarshal(v, &s) != nil {
		return nil, false
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return nil, false
	}
	if n, err := strconv.ParseInt(t, 10, 64); err == nil {
		b, _ := json.Marshal(n)
		return b, true
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, false
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, false
	}
	return b, true
}
func applyAliases(raw json.RawMessage) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, false
	}
	changed := false
	for alias, real := range argAliases {
		v, ok := fields[alias]
		if !ok {
			continue
		}
		if _, taken := fields[real]; taken {
			continue
		}
		delete(fields, alias)
		fields[real] = v
		changed = true
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return out, true
}
