package daemon

import (
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"net"
	"net/http"
	texttemplate "text/template"
	"time"

	"evilcode/internal/config"
	"evilcode/internal/theme"
)

// webAssets is the embedded app shell (plan-web.md §2): no build step, no
// npm — the first embed in the repo. Served under /assets/.
//
//go:embed webassets
var webAssets embed.FS

// The web surface is the daemon's opt-in HTTP listener beside its unix socket
// (plan-web.md §2). It is an adapter over the same session surface the unix
// protocol handlers call: no new state ownership, no second semantics. The
// listener is independent of the socket — either failing to bind must not take
// the other down — and it is closed by Server.Close.
//
// Every handler here must obey two rules the socket path enforces by
// construction: never hold s.mu or sess.mu across client I/O, and never trust
// a request that did not come through webauth (plan-web.md §3).

// DefaultWebReadHeaderTimeout bounds how long a client may take to send its
// request headers. A browser is fast; a hostile peer should not be able to tie
// up a goroutine by dribbling bytes.
const webReadHeaderTimeout = 10 * time.Second

// webState is one live HTTP listener. Guarded by Server.mu.
type webState struct {
	ln  net.Listener
	srv *http.Server
	// addr is the resolved host:port actually bound — what startup prints and
	// what the Host allowlist is built from. It is a copy, not the listener,
	// so Close can still be called after the listener is gone.
	addr string
	// token is the surface's bearer secret, minted or loaded at start (§3).
	token string
	// minted records whether this start created the token file, which decides
	// whether startup may print the full tokenized URL (once, ever).
	minted bool
}

// errServerClosed is returned by ListenWeb when the daemon is already tearing
// down: the caller must not interpret it as a bind failure.
var errServerClosed = errors.New("daemon: the server is shutting down")

// ListenWeb starts the HTTP surface on addr (host:port; empty uses the
// configured default). It returns an error if the bind fails; the unix socket
// keeps working either way. Starting twice is refused — the second caller gets
// the first listener's address in the error.
func (s *Server) ListenWeb(addr string) error {
	if addr == "" {
		addr = config.DefaultWebUIAddr
	}
	// The token is minted (or loaded and re-secured) before the bind: auth
	// without a secret is impossible, so a token failure must not start a
	// listener that would then 401 on everything.
	token, minted, err := loadOrMintWebToken(s.webTokenPath())
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("daemon: web UI cannot bind %s: %w", addr, err)
	}
	// ":0" and hostless forms resolve to something concrete; report and
	// enforce that form, not the requested one.
	bound := ln.Addr().String()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return errServerClosed
	}
	if s.web != nil {
		old := s.web.addr
		s.mu.Unlock()
		ln.Close()
		return fmt.Errorf("daemon: the web UI is already listening on %s", old)
	}
	w := &webState{ln: ln, addr: bound, token: token, minted: minted}
	s.web = w
	s.mu.Unlock()

	srv := &http.Server{
		Handler:           w.mux(s),
		ReadHeaderTimeout: webReadHeaderTimeout,
	}
	w.srv = srv
	go func() {
		// Serve returns ErrServerClosed on Close; anything else means the
		// listener died underneath us, which the Close path already handles.
		_ = srv.Serve(ln)
	}()
	return nil
}

// webMux builds the HTTP surface: routes wrapped in webauth (token
// authentication on every request, Origin+Host discipline on mutating verbs)
// and the §3 security headers on every response.
func (w *webState) mux(s *Server) http.Handler {
	mux := http.NewServeMux()

	assets, err := fs.Sub(webAssets, "webassets")
	if err != nil {
		// An embed layout mistake is a programming error; nothing serves.
		panic("daemon: webassets embed broken: " + err.Error())
	}
	indexTmpl := htmltemplate.Must(htmltemplate.ParseFS(webAssets, "webassets/index.html"))
	manifestTmpl := texttemplate.Must(texttemplate.ParseFS(webAssets, "webassets/manifest.webmanifest"))

	mux.HandleFunc("GET /{$}", s.webIndex(indexTmpl))
	mux.Handle("GET /assets/", webNoStore(http.StripPrefix("/assets", http.FileServerFS(assets))))
	mux.HandleFunc("GET /theme.css", s.webThemeCSS)
	mux.HandleFunc("GET /manifest.webmanifest", s.webManifest(manifestTmpl))

	auth := &webAuth{token: w.token, addr: w.addr}
	return securityHeaders(auth.wrap(mux))
}

// webCSP is the plan's Content-Security-Policy, verbatim (§3). Everything is
// 'self'; inline script cannot exist, so a sanitizer gap in vendored markdown
// rendering cannot execute code either.
const webCSP = "default-src 'none'; script-src 'self'; style-src 'self'; " +
	"img-src 'self' data: blob:; connect-src 'self'; manifest-src 'self'; " +
	"base-uri 'none'; frame-ancestors 'none'"

// securityHeaders stamps the §3 headers on every response, errors included.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", webCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// webNoStore marks static assets uncacheable. The binary may be replaced
// underneath a long-lived daemon (self-update), and a stale cached app.js
// pairing with a fresh daemon is worse than always re-fetching a few KB.
func webNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// webThemeColor is the palette color the browser chrome and the PWA
// background use: the card surface (user-bg), so the installed app sits in
// the theme rather than beside it.
func (s *Server) webThemeColor() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := ""
	if s.Cfg != nil {
		name = s.Cfg.Display.Theme
	}
	return theme.Hex(theme.ByName(name).Get(theme.RoleUserBg))
}

// webIndex serves the app shell with the palette-derived theme-color
// injected. The template is parsed once per listener start.
func (s *Server) webIndex(tmpl *htmltemplate.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := tmpl.Execute(w, struct{ ThemeColor string }{s.webThemeColor()}); err != nil {
			webError(w, http.StatusInternalServerError, "the app shell failed to render")
		}
	}
}

// webThemeCSS serves the generated palette tokens (§7).
func (s *Server) webThemeCSS(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	name := ""
	if s.Cfg != nil {
		name = s.Cfg.Display.Theme
	}
	css := renderThemeCSS(theme.ByName(name))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write([]byte(css))
}

// webManifest serves the PWA manifest with colors from the same palette.
func (s *Server) webManifest(tmpl *texttemplate.Template) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/manifest+json")
		if err := tmpl.Execute(w, struct{ ThemeColor string }{s.webThemeColor()}); err != nil {
			webError(w, http.StatusInternalServerError, "the manifest failed to render")
		}
	}
}

// webError writes the uniform error shape every web handler answers with
// (plan-web.md §4): {"error": "..."}. A bare 500 stays a bug; these are the
// 4xx users can act on.
func webError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	fmt.Fprintf(w, "{\"error\": %q}\n", msg)
}

// webAddr reports the bound host:port, or "" when the web surface is off.
// This is what `-status` and the startup line print.
func (s *Server) webAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.web == nil {
		return ""
	}
	return s.web.addr
}

// WebInfo is what startup reports about the web surface (§3, §10): the
// address, where the token lives, the token itself, and whether this start
// minted the token file. The full tokenized URL may be printed only when
// Minted is true — once, ever; every later start just names the file.
type WebInfo struct {
	Addr      string
	TokenPath string
	Token     string
	Minted    bool
}

// WebInfo returns nil while the web surface is off.
func (s *Server) WebInfo() *WebInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.web == nil {
		return nil
	}
	return &WebInfo{
		Addr:      s.web.addr,
		TokenPath: s.webTokenPath(),
		Token:     s.web.token,
		Minted:    s.web.minted,
	}
}
