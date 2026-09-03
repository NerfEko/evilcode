package daemon

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The web surface's access control (plan-web.md §3). The unix socket checks
// peer credentials in the kernel; HTTP over loopback is connectable by any
// local process, so every request carries a bearer secret instead, and every
// mutating request additionally proves it was sent by a page on this origin.
// The token persists in a 0600 file next to the socket so a browser's cookie
// keeps working across daemon restarts, self-update included.

// webCookieName is the handoff cookie. HttpOnly + SameSite=Strict + Path=/ +
// one-year Max-Age, set only by the tokenized-URL exchange.
const webCookieName = "evilcode_web"

// webTokenLifetime is the cookie's Max-Age: one year. The token file outlives
// it; deleting the file rotates the secret.
const webTokenLifetime = 365 * 24 * 60 * 60

// webTokenPath is where the hex token lives: next to the socket, same owner.
func (s *Server) webTokenPath() string { return s.Path + ".web-token" }

// loadOrMintWebToken returns the token at path, minting it on first use. An
// existing file is validated and re-chmod'd 0600 on load — a backup restore or
// a copy must not leave the bearer secret world-readable. A corrupt file is a
// hard error, not a silent rotation: minting a replacement would quietly
// invalidate every browser cookie and handoff URL the user has.
func loadOrMintWebToken(path string) (token string, minted bool, err error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		tok := strings.TrimSpace(string(data))
		if err := validWebToken(tok); err != nil {
			return "", false, fmt.Errorf("daemon: %s is corrupt (%v); delete it to mint a new token", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", false, fmt.Errorf("daemon: re-securing %s: %w", path, err)
		}
		return tok, false, nil
	case os.IsNotExist(err):
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return "", false, fmt.Errorf("daemon: minting the web token: %w", err)
		}
		tok := hex.EncodeToString(raw)
		if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
			return "", false, fmt.Errorf("daemon: writing %s: %w", path, err)
		}
		return tok, true, nil
	default:
		return "", false, fmt.Errorf("daemon: reading %s: %w", path, err)
	}
}

// validWebToken accepts exactly 64 hex characters — what crypto/rand mints.
func validWebToken(tok string) error {
	if len(tok) != 64 {
		return fmt.Errorf("token is %d chars, want 64", len(tok))
	}
	if _, err := hex.DecodeString(tok); err != nil {
		return fmt.Errorf("token is not hex: %w", err)
	}
	return nil
}

// webAuth guards every web route.
type webAuth struct {
	// token is the live hex token, captured only when auth is enabled. A
	// closure over the value (not over s.web) keeps handlers safe after Close
	// nils the field.
	token string
	// addr is the bound host:port; the Host allowlist is built from it.
	addr string
	// requireAuth controls whether bearer/cookie authentication is enforced.
	requireAuth bool
}

// wrap authenticates r, enforces the cross-site rules on mutating verbs, and
// only then reaches the mux. Failures are uniform JSON errors.
func (a *webAuth) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.requireAuth {
			if q := r.URL.Query().Get("token"); q != "" {
				a.handoff(w, r, q)
				return
			}
			if !a.authorized(r) {
				webError(w, http.StatusUnauthorized, "authentication required: open the tokenized URL once, or send the token as a cookie or bearer")
				return
			}
		}
		if isMutating(r.Method) && !a.sameSite(r) {
			// Distinguish the two rejections: a rebinding Host is a hostile
			// name; a cross-origin POST is a hostile page. Both are 403.
			webError(w, http.StatusForbidden, "request must come from this origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handoff exchanges `?token=<hex>` for a cookie and redirects to /, stripping
// the token from the URL. A wrong token is a plain 401 — it never reaches the
// app shell, and the token is never reflected back into a response.
func (a *webAuth) handoff(w http.ResponseWriter, r *http.Request, candidate string) {
	if subtle.ConstantTimeCompare([]byte(candidate), []byte(a.token)) != 1 {
		webError(w, http.StatusUnauthorized, "unknown token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     webCookieName,
		Value:    a.token,
		Path:     "/",
		MaxAge:   webTokenLifetime,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// authorized accepts the handoff cookie or an Authorization: Bearer header,
// compared in constant time.
func (a *webAuth) authorized(r *http.Request) bool {
	if c, err := r.Cookie(webCookieName); err == nil {
		if subtle.ConstantTimeCompare([]byte(c.Value), []byte(a.token)) == 1 {
			return true
		}
	}
	auth := r.Header.Get("Authorization")
	bearer, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(bearer)), []byte(a.token)) == 1
}

// sameSite is the CSRF/DNS-rebinding discipline for mutating verbs. The
// backend Host must be the bound address or a loopback spelling of its port
// unless an HTTPS reverse proxy forwards the public host. Tailscale Serve
// terminates HTTPS and forwards that host in X-Forwarded-Host, so that host is
// used for the Origin/Referer comparison only when X-Forwarded-Proto is HTTPS.
// A request with neither Origin nor Referer is rejected — it cannot prove where
// it came from.
func (a *webAuth) sameSite(r *http.Request) bool {
	host := r.Host
	if forwardedHost := httpsForwardedHost(r); forwardedHost != "" {
		if !hostAllowed(r.Host, a.addr) && !sameHostPort(r.Host, forwardedHost) {
			return false
		}
		host = forwardedHost
	} else if !hostAllowed(r.Host, a.addr) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return sameHostPort(u.Host, host)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		if err != nil || u.Host == "" {
			return false
		}
		return sameHostPort(u.Host, host)
	}
	return false
}

func httpsForwardedHost(r *http.Request) string {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return ""
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if comma := strings.IndexByte(host, ','); comma >= 0 {
		host = strings.TrimSpace(host[:comma])
	}
	return host
}

// isMutating reports whether the method changes state, per the plan's "every
// mutating verb (POST)" rule — extended to the other state-changers so a
// future handler cannot forget the check.
func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// hostAllowed accepts the bound host:port plus loopback spellings of the same
// port. This is what turns a DNS-rebinding name (attacker.example resolving to
// 127.0.0.1) into a 403: the Host header names something outside the set.
func hostAllowed(host, bound string) bool {
	_, port, err := net.SplitHostPort(bound)
	if err != nil {
		return sameHostPort(host, bound)
	}
	allowed := map[string]bool{
		bound:               true,
		"localhost:" + port: true,
		"127.0.0.1:" + port: true,
		"[::1]:" + port:     true,
		"localhost":         port == "80",
		"127.0.0.1":         port == "80",
		"[::1]":             port == "80",
	}
	return allowed[strings.TrimSpace(host)]
}

// sameHostPort compares two host:port strings with a default-port allowance:
// "127.0.0.1" and "127.0.0.1:80" are the same origin over plain HTTP.
func sameHostPort(a, b string) bool {
	if a == b {
		return true
	}
	an, ap, aerr := net.SplitHostPort(a)
	bn, bp, berr := net.SplitHostPort(b)
	if aerr != nil || berr != nil {
		// One side omitted the port; treat bare as port 80 (http).
		aport, bport := ap, bp
		if aerr != nil {
			an, aport = a, "80"
		}
		if berr != nil {
			bn, bport = b, "80"
		}
		return an == bn && aport == bport
	}
	return an == bn && ap == bp
}
