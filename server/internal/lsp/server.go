// Package lsp implements a minimal language server that reports code not
// covered by tests, as read from a Go coverage profile, as diagnostics.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/benipranata/zed-go-coverage/server/internal/coverprofile"
)

const defaultProfile = "coverage.out"

const (
	showAll       = "all"
	showCovered   = "covered"
	showUncovered = "uncovered"
)

type Server struct {
	in       *bufio.Reader
	out      io.Writer
	interval time.Duration

	mu          sync.Mutex
	root        string
	profilePath string
	profile     coverprofile.Profile
	modTime     time.Time
	docs        map[string]string
	dirty       map[string]bool
	loadErr     string

	diagnostics bool
	show        string
	canRefresh  bool
	nextID      int
}

func New(in io.Reader, out io.Writer, pollInterval time.Duration) *Server {
	return &Server{
		in:       bufio.NewReader(in),
		out:      out,
		interval: pollInterval,
		docs:     map[string]string{},
		dirty:    map[string]bool{},

		diagnostics: true,
		show:        showAll,
	}
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result"`
}

type errorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type lspRange struct {
	Start position `json:"start"`
	End   position `json:"end"`
}

type diagnostic struct {
	Range    lspRange `json:"range"`
	Severity int      `json:"severity"`
	Source   string   `json:"source"`
	Message  string   `json:"message"`
}

const severityInformation = 3

// Custom semantic token types, styled by the client with background colors.
var tokenTypes = []string{"coverageCovered", "coverageUncovered"}

type token struct{ line, col, length, typ int }

// Run serves requests until an exit notification or the end of input.
func (s *Server) Run() error {
	done := make(chan struct{})
	defer close(done)
	go s.poll(done)

	for {
		msg, err := s.readMessage()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if msg.Method == "exit" {
			return nil
		}
		if err := s.handle(msg); err != nil {
			return err
		}
	}
}

func (s *Server) readMessage() (message, error) {
	length := -1
	for {
		line, err := s.in.ReadString('\n')
		if err != nil {
			return message{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &length); err != nil {
				return message{}, fmt.Errorf("lsp: bad Content-Length %q", v)
			}
		}
	}
	if length < 0 {
		return message{}, fmt.Errorf("lsp: missing Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(s.in, body); err != nil {
		return message{}, err
	}
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil {
		return message{}, fmt.Errorf("lsp: bad message: %w", err)
	}
	return msg, nil
}

func (s *Server) write(v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.out, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func (s *Server) reply(id json.RawMessage, result any) error {
	return s.write(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) handle(msg message) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Responses to our own requests carry no method.
	if msg.Method == "" {
		return nil
	}

	switch msg.Method {
	case "initialize":
		return s.initialize(msg)
	case "initialized":
		return s.reload()
	case "shutdown":
		return s.reply(msg.ID, nil)
	case "textDocument/didOpen":
		var p struct {
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return nil
		}
		s.docs[p.TextDocument.URI] = p.TextDocument.Text
		delete(s.dirty, p.TextDocument.URI)
		return s.publish(p.TextDocument.URI)
	case "textDocument/didChange":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil || len(p.ContentChanges) == 0 {
			return nil
		}
		// The profile no longer matches the buffer, so clear until the next save.
		s.docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
		s.dirty[p.TextDocument.URI] = true
		return s.sendDiagnostics(p.TextDocument.URI, nil)
	case "textDocument/documentColor":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		return s.reply(msg.ID, s.documentColors(p.TextDocument.URI))
	case "textDocument/semanticTokens/full":
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		return s.reply(msg.ID, map[string]any{"data": s.semanticTokens(p.TextDocument.URI)})
	case "textDocument/didSave":
		return s.withURI(msg, func(uri string) error {
			delete(s.dirty, uri)
			return s.publish(uri)
		})
	case "textDocument/didClose":
		return s.withURI(msg, func(uri string) error {
			delete(s.docs, uri)
			delete(s.dirty, uri)
			return s.sendDiagnostics(uri, nil)
		})
	}
	if len(msg.ID) > 0 {
		return s.write(errorResponse{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   rpcError{Code: -32601, Message: "method not found: " + msg.Method},
		})
	}
	return nil
}

func (s *Server) withURI(msg message, fn func(uri string) error) error {
	var p struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return nil
	}
	return fn(p.TextDocument.URI)
}

func (s *Server) initialize(msg message) error {
	var p struct {
		Capabilities struct {
			Workspace struct {
				SemanticTokens struct {
					RefreshSupport bool `json:"refreshSupport"`
				} `json:"semanticTokens"`
			} `json:"workspace"`
		} `json:"capabilities"`
		RootURI          string `json:"rootUri"`
		WorkspaceFolders []struct {
			URI string `json:"uri"`
		} `json:"workspaceFolders"`
		InitOption struct {
			Profile     string `json:"profile"`
			Diagnostics *bool  `json:"diagnostics"`
			Show        string `json:"show"`
		} `json:"initializationOptions"`
	}
	_ = json.Unmarshal(msg.Params, &p)
	rootURI := p.RootURI
	if rootURI == "" && len(p.WorkspaceFolders) > 0 {
		rootURI = p.WorkspaceFolders[0].URI
	}
	s.root = uriToPath(rootURI)
	profile := p.InitOption.Profile
	if profile == "" {
		profile = defaultProfile
	}
	if !filepath.IsAbs(profile) {
		profile = filepath.Join(s.root, profile)
	}
	s.profilePath = profile
	s.canRefresh = p.Capabilities.Workspace.SemanticTokens.RefreshSupport
	if p.InitOption.Show == showCovered || p.InitOption.Show == showUncovered {
		s.show = p.InitOption.Show
	}
	if p.InitOption.Diagnostics != nil {
		s.diagnostics = *p.InitOption.Diagnostics
	}

	return s.reply(msg.ID, map[string]any{
		"capabilities": map[string]any{
			"textDocumentSync": map[string]any{"openClose": true, "change": 1, "save": map[string]any{}},
			"colorProvider":    true,
			"semanticTokensProvider": map[string]any{
				"legend": map[string]any{"tokenTypes": tokenTypes, "tokenModifiers": []string{}},
				"full":   true,
			},
		},
		"serverInfo": map[string]any{"name": "go-coverage-lsp"},
	})
}

func (s *Server) poll(done <-chan struct{}) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			s.mu.Lock()
			_ = s.reload()
			s.mu.Unlock()
		}
	}
}

// reload re-reads the profile when its modification time changed and
// republishes diagnostics for every open document.
func (s *Server) reload() error {
	if s.profilePath == "" {
		return nil
	}
	info, err := os.Stat(s.profilePath)
	if err != nil {
		if s.profile == nil {
			return nil
		}
		s.profile, s.modTime = nil, time.Time{}
	} else {
		if info.ModTime().Equal(s.modTime) {
			return nil
		}
		profile, err := loadProfile(s.profilePath)
		if err != nil {
			return s.logLoadError(err)
		}
		s.profile, s.modTime, s.loadErr = profile, info.ModTime(), ""
	}
	for uri := range s.docs {
		if s.dirty[uri] {
			continue
		}
		if err := s.publish(uri); err != nil {
			return err
		}
	}
	return nil
}

func loadProfile(name string) (coverprofile.Profile, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return coverprofile.Parse(f)
}

// logLoadError reports a failure once per distinct message, since the poller
// retries every interval.
func (s *Server) logLoadError(err error) error {
	msg := err.Error()
	if msg == s.loadErr {
		return nil
	}
	s.loadErr = msg
	return s.write(notification{
		JSONRPC: "2.0",
		Method:  "window/logMessage",
		Params:  map[string]any{"type": 2, "message": "go-coverage: " + msg},
	})
}

func (s *Server) publish(uri string) error {
	if err := s.refreshTokens(); err != nil {
		return err
	}
	if !s.diagnostics {
		return nil
	}
	return s.sendDiagnostics(uri, s.diagnosticsFor(uri))
}

// refreshTokens asks the client to re-request semantic tokens, which it only
// does on its own after edits.
func (s *Server) refreshTokens() error {
	if !s.canRefresh {
		return nil
	}
	s.nextID++
	return s.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      fmt.Sprintf("refresh-%d", s.nextID),
		"method":  "workspace/semanticTokens/refresh",
	})
}

func (s *Server) sendDiagnostics(uri string, diags []diagnostic) error {
	if diags == nil {
		diags = []diagnostic{}
	}
	return s.write(notification{
		JSONRPC: "2.0",
		Method:  "textDocument/publishDiagnostics",
		Params:  map[string]any{"uri": uri, "diagnostics": diags},
	})
}

func (s *Server) diagnosticsFor(uri string) []diagnostic {
	file := uriToPath(uri)
	importPath, ok := importPathFor(file)
	if !ok {
		return nil
	}
	lines := strings.Split(s.docs[uri], "\n")
	var diags []diagnostic
	for _, b := range s.profile[importPath] {
		if b.Count > 0 || b.StartLine < 1 || b.EndLine < 1 || b.StartLine > len(lines) || b.EndLine > len(lines) {
			continue
		}
		diags = append(diags, diagnostic{
			Range: lspRange{
				Start: position{b.StartLine - 1, utf16Col(lines[b.StartLine-1], b.StartCol-1)},
				End:   position{b.EndLine - 1, utf16Col(lines[b.EndLine-1], b.EndCol-1)},
			},
			Severity: severityInformation,
			Source:   "go-coverage",
			Message:  "not covered by tests",
		})
	}
	return diags
}

// utf16Col converts a zero-based byte offset into a UTF-16 code unit offset.
func utf16Col(line string, byteCol int) int {
	line = strings.TrimSuffix(line, "\r")
	if byteCol > len(line) {
		byteCol = len(line)
	}
	return len(utf16.Encode([]rune(line[:byteCol])))
}

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	// file:///C:/dir yields /C:/dir, which is not a valid Windows path.
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		return p[1:]
	}
	return p
}

// importPathFor derives the import path that go test writes into profiles
// from the nearest enclosing go.mod.
func importPathFor(file string) (string, bool) {
	if file == "" {
		return "", false
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if mod, ok := modulePath(filepath.Join(dir, "go.mod")); ok {
			rel, _ := filepath.Rel(dir, file)
			return path.Join(mod, filepath.ToSlash(rel)), true
		}
		if dir == filepath.Dir(dir) {
			return "", false
		}
	}
}

func modulePath(goMod string) (string, bool) {
	data, err := os.ReadFile(goMod)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "//")
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module")
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		if mod := strings.Trim(strings.TrimSpace(rest), `"`); mod != "" {
			return mod, true
		}
	}
	return "", false
}

// semanticTokens returns the LSP-encoded coverage tokens for a saved document.
func (s *Server) semanticTokens(uri string) []int {
	data := []int{}
	prevLine, prevCol := 0, 0
	for _, t := range s.coverageTokens(uri) {
		if t.line != prevLine {
			prevCol = 0
		}
		data = append(data, t.line-prevLine, t.col-prevCol, t.length, t.typ, 0)
		prevLine, prevCol = t.line, t.col
	}
	return data
}

type color struct {
	Red   float64 `json:"red"`
	Green float64 `json:"green"`
	Blue  float64 `json:"blue"`
	Alpha float64 `json:"alpha"`
}

type colorInformation struct {
	Range lspRange `json:"range"`
	Color color    `json:"color"`
}

// Translucent so the code stays readable on either theme.
var (
	coveredColor   = color{0.18, 0.63, 0.26, 0.2}
	uncoveredColor = color{0.97, 0.32, 0.29, 0.2}
)

// documentColors reports the coverage spans as colors, which clients can
// render as a background behind the text.
func (s *Server) documentColors(uri string) []colorInformation {
	out := []colorInformation{}
	for _, t := range s.coverageTokens(uri) {
		c := coveredColor
		if t.typ == 1 {
			c = uncoveredColor
		}
		out = append(out, colorInformation{
			Range: lspRange{
				Start: position{t.line, t.col},
				End:   position{t.line, t.col + t.length},
			},
			Color: c,
		})
	}
	return out
}

// coverageTokens returns the single-line covered/uncovered spans of a saved
// document, in document order. Multi-line blocks are split per line because
// clients may not support spans across lines.
func (s *Server) coverageTokens(uri string) []token {
	if s.dirty[uri] {
		return nil
	}
	importPath, ok := importPathFor(uriToPath(uri))
	if !ok {
		return nil
	}
	text, ok := s.docs[uri]
	if !ok {
		return nil
	}
	lines := strings.Split(text, "\n")
	var tokens []token
	for _, b := range s.profile[importPath] {
		if b.StartLine < 1 || b.EndLine < b.StartLine || b.EndLine > len(lines) {
			continue
		}
		typ := 0
		if b.Count == 0 {
			typ = 1
		}
		if (typ == 0 && s.show == showUncovered) || (typ == 1 && s.show == showCovered) {
			continue
		}
		for n := b.StartLine; n <= b.EndLine; n++ {
			line := lines[n-1]
			start, end := 0, utf16Col(line, len(line))
			if n == b.StartLine {
				start = utf16Col(line, b.StartCol-1)
			}
			if n == b.EndLine {
				end = utf16Col(line, b.EndCol-1)
			}
			if end > start {
				tokens = append(tokens, token{n - 1, start, end - start, typ})
			}
		}
	}
	return tokens
}
