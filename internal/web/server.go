// Package web is `acp web`: a local dashboard on 127.0.0.1. Reads go straight to acp; every change
// runs only after the same session previewed it and the preview still says the same thing.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/thieung/acp/internal/core"
	"github.com/thieung/acp/internal/scan"
)

//go:embed static
var staticFS embed.FS

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

const maxBody = 64 << 10

// Server holds the sign-in code, the sessions and the operations of one `acp web` run.
type Server struct {
	root *core.Root
	run  core.Runner

	mu       sync.Mutex
	addr     string                       // host:port the browser uses; Host and Origin must match it
	code     string                       // one-time sign-in code, "" once used
	sessions map[string]map[string]string // token -> preview key -> digest
	ops      map[string]*operation
	order    []string
	busy     bool
	lastScan []byte
	active   int           // operations and sign-ins still running
	idle     chan struct{} // closed when active drops to 0; nil until someone waits
}

type operation struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	State    string          `json:"state"` // running | done
	Digest   string          `json:"digest,omitempty"`
	Envelope json.RawMessage `json:"envelope,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// New builds the server; SetAddr must be called once the listener's address is known.
func New(root *core.Root, run core.Runner) *Server {
	return &Server{root: root, run: run, code: token(), sessions: map[string]map[string]string{},
		ops: map[string]*operation{}}
}

func token() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// SetAddr records the host:port the dashboard is served on.
func (s *Server) SetAddr(hostport string) {
	s.mu.Lock()
	s.addr = hostport
	s.mu.Unlock()
}

// Code is the one-time sign-in code that goes in the URL fragment.
func (s *Server) Code() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// Changing reports whether a change or a sign-in holds the lock. Previews and scans do not count:
// stopping them loses nothing.
func (s *Server) Changing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Running reports whether an operation or a sign-in is still running.
func (s *Server) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active > 0
}

// Wait returns once no operation or sign-in is running, or with ctx's error. A shutdown calls it
// after the listener is closed, so a change the user started is not cut off halfway through.
func (s *Server) Wait(ctx context.Context) error {
	s.mu.Lock()
	if s.active == 0 {
		s.mu.Unlock()
		return nil
	}
	if s.idle == nil {
		s.idle = make(chan struct{})
	}
	idle := s.idle
	s.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// end counts one operation or sign-in as finished; each starts by raising active under mu.
func (s *Server) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active--; s.active == 0 && s.idle != nil {
		close(s.idle)
		s.idle = nil
	}
}

// Handler serves the static page and the API behind the Host, Origin and session checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session", s.session)
	mux.HandleFunc("GET /api/status", s.command("status"))
	mux.HandleFunc("GET /api/describe", s.command("describe"))
	mux.HandleFunc("GET /api/setup", s.command("setup"))
	mux.HandleFunc("GET /api/global", s.command("global", "--check"))
	mux.HandleFunc("GET /api/skills/{id}", s.skills)
	mux.HandleFunc("GET /api/explain", s.explain)
	mux.HandleFunc("GET /api/hooks", s.hooks)
	mux.HandleFunc("GET /api/scan", s.scanResult)
	mux.HandleFunc("POST /api/run", s.start)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("GET /api/ops/{id}", s.op)
	mux.HandleFunc("GET /api/running", s.running)
	mux.HandleFunc("GET /", s.static)
	return s.guard(mux)
}

func (s *Server) allowedHost(host string) bool {
	s.mu.Lock()
	addr := s.addr
	s.mu.Unlock()
	_, port, _ := strings.Cut(addr, ":")
	return addr != "" && (host == "127.0.0.1:"+port || host == "localhost:"+port)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		if !s.allowedHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			u, err := originHost(o)
			if err != nil || !s.allowedHost(u) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/session" && !s.signedIn(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "mở lại liên kết acp web in ra trong terminal"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		next.ServeHTTP(w, r)
	})
}

// originHost returns the host:port of an http origin.
func originHost(origin string) (string, error) {
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok || strings.ContainsAny(rest, "/?#@") {
		return "", errors.New("not an http origin")
	}
	return rest, nil
}

func bearer(r *http.Request) string {
	t, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return t
}

func (s *Server) signedIn(r *http.Request) bool {
	t := bearer(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[t]
	return t != "" && ok
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if raw, ok := v.(json.RawMessage); ok {
		w.Write(raw)
		return
	}
	json.NewEncoder(w).Encode(v)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	var in struct{ Code string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.code == "" || subtle.ConstantTimeCompare([]byte(in.Code), []byte(s.code)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "mã đăng nhập đã dùng hoặc không đúng; chạy lại acp web"})
		return
	}
	s.code = ""
	t := token()
	s.sessions[t] = map[string]string{}
	writeJSON(w, http.StatusOK, map[string]string{"token": t})
}

// acp runs one command with --json and returns its envelope. stdin, when not nil, is the child's
// standard input.
func (s *Server) acp(ctx context.Context, stdin []byte, args ...string) (json.RawMessage, error) {
	out, err := s.run(ctx, stdin, append(slices.Clone(args), "--json")...)
	if err != nil {
		return nil, err
	}
	// Redact decoded strings: the patterns do not understand JSON escapes, and a raw pass can break
	// the envelope or miss a quoted secret.
	var env any
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, errors.New("acp " + args[0] + " printed no JSON envelope")
	}
	b, err := json.Marshal(core.RedactValue(env))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// failed reports whether an envelope carries an error; such a preview cannot be applied.
func failed(env json.RawMessage) bool {
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(env, &e) != nil || e.Error != ""
}

func (s *Server) command(args ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.respond(w, r, args...)
	}
}

func (s *Server) respond(w http.ResponseWriter, r *http.Request, args ...string) {
	env, err := s.acp(r.Context(), nil, args...)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, env)
}

func (s *Server) skills(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !core.ValidName(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid project id"})
		return
	}
	s.respond(w, r, "skills", "list", id)
}

func (s *Server) explain(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		terms := []map[string]string{}
		for _, t := range scan.Glossary() {
			terms = append(terms, map[string]string{"term": t.Term, "meaning": t.Meaning})
		}
		writeJSON(w, http.StatusOK, map[string]any{"glossary": terms})
		return
	}
	ex, ok := scan.Explain(code, nil)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown finding code"})
		return
	}
	b, _ := core.MarshalJSON(ex.Object())
	writeJSON(w, http.StatusOK, json.RawMessage(b))
}

func (s *Server) hooks(w http.ResponseWriter, r *http.Request) {
	env, err := s.acp(r.Context(), nil, "hooks")
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	inv := scan.Inventory()
	var acp any
	json.Unmarshal(env, &acp)
	inv.Set("acp", acp)
	b, _ := core.MarshalJSON(inv)
	writeJSON(w, http.StatusOK, json.RawMessage(b))
}

func (s *Server) scanResult(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	last := s.lastScan
	s.mu.Unlock()
	if last == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chưa quét lần nào"})
		return
	}
	writeJSON(w, http.StatusOK, json.RawMessage(last))
}

// ------------------------------------------------------------------ sign-in

// loginTimeout bounds one sign-in; it runs on its own context so a closed tab cannot cut it short.
// It outlasts the child's own ak bounds (core.AKLoginTimeout plus core.AKWhoamiTimeout), so ak
// normally answers or times out first; if it does fire, the child stops ak before it exits.
var loginTimeout = 2 * time.Minute

// login signs in to AgentKit with an API key. It has nothing to preview, so it needs no digest,
// but it still waits its turn behind any other change. The key reaches acp only on stdin: it is
// never in argv, an operation or a session. Every copy this handler makes (the body, the raw JSON
// value, the decoded key) is a byte slice it clears once acp has run; buffers inside net/http and
// the kernel are out of its reach, so the wiping is best effort.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r.Body)
	defer clear(body)
	var in struct {
		Key json.RawMessage `json:"key"`
	}
	err = errors.Join(err, json.Unmarshal(body, &in))
	defer clear(in.Key)
	decoded, ok := jsonString(in.Key)
	defer clear(decoded)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	key := bytes.TrimSpace(decoded)
	if len(key) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nhập API key của AgentKit"})
		return
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "một thay đổi khác đang chạy; đợi nó xong"})
		return
	}
	s.busy = true
	s.active++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
		s.end()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	defer cancel()
	env, err := s.acp(ctx, key, "setup", "login", "--api-key-stdin")
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "đăng nhập quá lâu không xong; kiểm tra mạng rồi thử lại"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	var e struct {
		ExitCode int `json:"exit_code"`
	}
	if failed(env) || json.Unmarshal(env, &e) != nil || e.ExitCode != 0 {
		writeJSON(w, http.StatusBadRequest, env)
		return
	}
	writeJSON(w, http.StatusOK, env)
}

// readBody reads a request body into one buffer sized for the largest body the guard allows, so
// no outgrown copy of a secret is left behind uncleared.
func readBody(r io.Reader) ([]byte, error) {
	buf := make([]byte, maxBody+1)
	n := 0
	for {
		m, err := r.Read(buf[n:])
		n += m
		switch {
		case err == io.EOF:
			return buf[:n], nil
		case err != nil:
			return buf[:n], err
		case n == len(buf):
			return buf[:n], errors.New("request body too large")
		}
	}
}

// jsonString decodes a JSON string value into a new byte slice the caller can clear; the standard
// decoder only yields Go strings, which cannot be. Missing or null decodes as empty. The result is
// never longer than raw, so it is allocated once. Lone UTF-16 surrogates and invalid UTF-8 are
// refused rather than replaced: an API key has neither.
func jsonString(raw []byte) ([]byte, bool) {
	if raw == nil || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return nil, false
	}
	in := raw[1 : len(raw)-1]
	out := make([]byte, 0, len(in))
	bad := func() ([]byte, bool) { clear(out[:cap(out)]); return nil, false }
	for i := 0; i < len(in); i++ {
		c := in[i]
		if c < 0x20 || c == '"' {
			return bad()
		}
		if c != '\\' {
			out = append(out, c)
			continue
		}
		if i++; i == len(in) {
			return bad()
		}
		switch in[i] {
		case '"', '\\', '/':
			out = append(out, in[i])
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r := hex4(in[i+1:])
			i += 4
			if utf16.IsSurrogate(r) {
				if i+2 >= len(in) || in[i+1] != '\\' || in[i+2] != 'u' {
					return bad()
				}
				r = utf16.DecodeRune(r, hex4(in[i+3:]))
				i += 6
				if r == utf8.RuneError {
					return bad()
				}
			}
			if r < 0 {
				return bad()
			}
			out = utf8.AppendRune(out, r)
		default:
			return bad()
		}
	}
	if !utf8.Valid(out) {
		return bad()
	}
	return out, true
}

// hex4 is the value of four hex digits at the start of b, or -1.
func hex4(b []byte) rune {
	if len(b) < 4 {
		return -1
	}
	var r rune
	for _, c := range b[:4] {
		switch {
		case '0' <= c && c <= '9':
			c -= '0'
		case 'a' <= c && c <= 'f':
			c -= 'a' - 10
		case 'A' <= c && c <= 'F':
			c -= 'A' - 10
		default:
			return -1
		}
		r = r<<4 | rune(c)
	}
	return r
}

// ------------------------------------------------------------------ runs

type runIn struct {
	Kind   string `json:"kind"`
	Args   args   `json:"args"`
	Digest string `json:"digest"`
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var in runIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	k, ok := kinds[in.Kind]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown kind"})
		return
	}
	if err := k.check(s.root, in.Args); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	tok := bearer(r)
	if !k.mutation {
		id := s.launch(in.Kind, k.argv(in.Args), tok, previewKey(in.Kind, in.Args), nil)
		writeJSON(w, http.StatusAccepted, map[string]string{"op": id})
		return
	}
	if in.Digest == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "xem trước thay đổi này trước khi áp dụng"})
		return
	}
	key := previewKey(k.preview, in.Args)
	s.mu.Lock()
	seen := s.sessions[tok][key]
	if s.busy {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "một thay đổi khác đang chạy; đợi nó xong"})
		return
	}
	if seen == "" || seen != in.Digest {
		s.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "đã thay đổi, xem trước lại"})
		return
	}
	s.busy = true
	s.mu.Unlock()

	// Preview again under the busy flag: the change runs only if the machine still looks the same.
	env, err := s.acp(r.Context(), nil, kinds[k.preview].argv(in.Args)...)
	if err == nil && !failed(env) && digest(env) == in.Digest {
		id := s.launch(in.Kind, k.argv(in.Args), tok, "", func() {
			s.mu.Lock()
			s.busy = false
			delete(s.sessions[tok], key) // a digest applies once
			s.mu.Unlock()
		})
		writeJSON(w, http.StatusAccepted, map[string]string{"op": id})
		return
	}
	s.mu.Lock()
	s.busy = false
	if err == nil && !failed(env) {
		s.sessions[tok][key] = digest(env)
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusConflict, map[string]string{"error": "đã thay đổi, xem trước lại"})
}

func digest(env []byte) string {
	sum := sha256.Sum256(env)
	return hex.EncodeToString(sum[:])
}

func previewKey(kind string, a args) string {
	b, _ := json.Marshal(a)
	return kind + "\x00" + string(b)
}

// launch runs argv in the background as an operation. key is set for previews, whose digest the
// session keeps; done runs when the operation ends.
func (s *Server) launch(kind string, argv []string, tok, key string, done func()) string {
	o := &operation{ID: token()[:16], Kind: kind, State: "running"}
	s.mu.Lock()
	s.ops[o.ID] = o
	s.order = append(s.order, o.ID)
	// Keep the last 100 operations, but never drop one still running: its page polls it.
	for len(s.order) > 100 {
		i := slices.IndexFunc(s.order, func(id string) bool { return s.ops[id].State == "done" })
		if i < 0 {
			break
		}
		delete(s.ops, s.order[i])
		s.order = slices.Delete(s.order, i, i+1)
	}
	s.active++
	s.mu.Unlock()
	go func() {
		defer s.end()
		env, err := s.acp(context.Background(), nil, argv...)
		s.mu.Lock()
		o.State = "done"
		if err != nil {
			o.Error = err.Error()
		} else {
			o.Envelope = env
			if kind == "scan" {
				s.lastScan = env
			}
			if key != "" && !failed(env) {
				o.Digest = digest(env)
				if sess, ok := s.sessions[tok]; ok {
					sess[key] = o.Digest
				}
			}
		}
		s.mu.Unlock()
		if done != nil {
			done()
		}
	}()
	return o.ID
}

// running answers whether a change or a sign-in is still running, so the desktop app can ask
// before it quits.
func (s *Server) running(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"running": s.Changing()})
}

func (s *Server) op(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	o, ok := s.ops[r.PathValue("id")]
	var snapshot operation
	if ok {
		snapshot = *o
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown operation"})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// ------------------------------------------------------------------ static

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8",
	".js": "text/javascript; charset=utf-8", ".woff": "font/woff", ".txt": "text/plain; charset=utf-8",
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(r.URL.Path)
	if name == "/" {
		name = "/index.html"
	}
	ctype, ok := contentTypes[filepath.Ext(name)]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(staticFS, "static"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Write(b)
}
