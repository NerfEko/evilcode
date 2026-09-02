package daemon

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The design-system guard (plan-web.md P4.5): authored stylesheets may style
// themselves exclusively through the tokens /theme.css emits from the daemon's
// palette (§7). A hex literal in app.css is a second copy of a color the
// generator already owns — it is how the web silently drifts from the TUI.
// Only renderThemeCSS (Go, not an authored stylesheet) may spell a color.

var webHexLiteral = regexp.MustCompile(`(?i)#[0-9a-f]{8}\b|#[0-9a-f]{6}\b|#[0-9a-f]{4}\b|#[0-9a-f]{3}\b`)

func TestWebAuthoredCSSHasNoHexLiterals(t *testing.T) {
	entries, err := fs.ReadDir(webAssets, "webassets/css")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".css") {
			continue
		}
		raw, err := fs.ReadFile(webAssets, "webassets/css/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if loc := webHexLiteral.FindString(line); loc != "" {
				t.Errorf("webassets/css/%s:%d: authored CSS carries the hex literal %q — use the palette token instead (P4.5)",
					e.Name(), i+1, loc)
			}
		}
	}
}

// The shell contract: the markup the app.js module graph expects must actually
// ship, every asset the markup references must be embedded or generated, and
// every relative import between modules must resolve. A typo in any of those
// otherwise only shows up as a blank page in a real browser.

func webShellHTML(t *testing.T) string {
	t.Helper()
	raw, err := fs.ReadFile(webAssets, "webassets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWebShellMountsExist(t *testing.T) {
	html := webShellHTML(t)
	for _, id := range []string{
		"app", "sidebar", "statusline", "roster", "new-session",
		"chat", "chat-back", "transcript", "rail", "sheet-root",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html is missing #%s; app.js cannot mount the shell", id)
		}
	}
}

func TestWebShellAssetReferencesResolve(t *testing.T) {
	html := webShellHTML(t)
	// Every href/src the shell ships must be an embedded asset or one of the
	// generated routes (theme.css, the manifest, the index itself).
	for _, m := range regexp.MustCompile(`(?:href|src)="([^"]+)"`).FindAllStringSubmatch(html, -1) {
		ref := m[1]
		if strings.HasPrefix(ref, "http:") || strings.HasPrefix(ref, "https:") {
			t.Errorf("index.html references an external URL %q; CSP forbids loading it", ref)
			continue
		}
		if ref == "/theme.css" || ref == "/manifest.webmanifest" || ref == "/" {
			continue
		}
		path := strings.TrimPrefix(ref, "/assets/")
		if path == ref {
			t.Errorf("index.html references %q outside /assets/ and the generated routes", ref)
			continue
		}
		if _, err := fs.Stat(webAssets, "webassets/"+path); err != nil {
			t.Errorf("index.html references /assets/%s, which is not embedded: %v", path, err)
		}
	}
}

func TestWebJSImportGraphResolves(t *testing.T) {
	var jsFiles []string
	err := fs.WalkDir(webAssets, "webassets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".js") {
			jsFiles = append(jsFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(jsFiles) == 0 {
		t.Fatal("the asset tree ships no JavaScript modules")
	}
	// import ... from "./x.js" and export ... from "../y.js" — every relative
	// target must exist inside the embed, or the browser gets a 404 module and
	// the whole graph fails to boot.
	importRe := regexp.MustCompile(`(?:^|\n)\s*(?:import|export)[^\n]*?from\s*["']([^"']+)["']`)
	dynamicImportRe := regexp.MustCompile(`import\(\s*["']([^"']+)["']\s*\)`)
	for _, file := range jsFiles {
		raw, err := fs.ReadFile(webAssets, file)
		if err != nil {
			t.Fatal(err)
		}
		dir := file[:strings.LastIndex(file, "/")]
		for _, re := range []*regexp.Regexp{importRe, dynamicImportRe} {
			for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
				spec := m[1]
				if !strings.HasPrefix(spec, ".") {
					// Bare specifiers are forbidden (no npm, no import maps);
					// flag them so a vendored file is an explicit decision.
					t.Errorf("%s imports bare module %q; the web app has no npm (§8)", file, spec)
					continue
				}
				target := path.Join(dir, spec)
				if _, err := fs.Stat(webAssets, target); err != nil {
					t.Errorf("%s imports %q, which is not embedded: %v", file, spec, err)
				}
			}
		}
	}
}

// The roster is the one place SessionInfo's whole vocabulary is visible; the
// verify item for Phase 4 checks it in a browser, but the API shape it renders
// is pinned here: every field the roster row renders must survive a JSON
// round-trip with the names the JS reads.
func TestWebSessionInfoJSONFieldNames(t *testing.T) {
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	info := SessionInfo{
		Name: "hex", Model: "m", Running: true, Clients: 1, Worker: true,
		Started: started, Stale: true,
		Task: "count things", Cwd: "/tmp", Title: "t",
		Modified: started, Crashed: true,
		Stored: true, Live: true, Messages: 3, Pending: 2,
	}
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"name"`, `"model"`, `"running"`, `"clients"`, `"worker"`, `"started"`,
		`"stale"`, `"task"`, `"cwd"`, `"title"`, `"modified"`, `"crashed"`,
		`"stored"`, `"live"`, `"messages"`, `"pending"`,
	} {
		if !strings.Contains(string(raw), field) {
			t.Errorf("SessionInfo JSON is missing %s; the roster cannot render it", field)
		}
	}
}