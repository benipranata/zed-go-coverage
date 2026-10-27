package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func frame(method string, id any, params any) string {
	m := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		m["id"] = id
	}
	if params != nil {
		m["params"] = params
	}
	body, _ := json.Marshal(m)
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

func fileURI(p string) string { return "file://" + p }

type project struct {
	root, file, profile string
}

func newProject(t *testing.T) project {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "go.mod"), "// comment\nmodule example.com/m\n\ngo 1.22\n")
	return project{root, filepath.Join(root, "pkg", "a.go"), filepath.Join(root, "coverage.out")}
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runScript feeds the framed messages to a server and returns every message it wrote.
func runScript(t *testing.T, msgs ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	s := New(strings.NewReader(strings.Join(msgs, "")), &out, time.Hour)
	if err := s.Run(); err != nil {
		t.Fatal(err)
	}
	return readAll(t, &out)
}

func readAll(t *testing.T, r io.Reader) []map[string]any {
	t.Helper()
	br := bufio.NewReader(r)
	var msgs []map[string]any
	for {
		m, err := readFrame(br)
		if err == io.EOF {
			return msgs
		}
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, m)
	}
}

func readFrame(br *bufio.Reader) (map[string]any, error) {
	var n int
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		fmt.Sscanf(line, "Content-Length: %d", &n)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(body, &m)
}

func diagnosticsOf(m map[string]any) []any {
	return m["params"].(map[string]any)["diagnostics"].([]any)
}

func initMsgs(p project, opts any) []string {
	return []string{
		frame("initialize", 1, map[string]any{"rootUri": fileURI(p.root), "initializationOptions": opts}),
		frame("initialized", nil, map[string]any{}),
	}
}

func openMsg(p project, text string) string {
	return frame("textDocument/didOpen", nil, map[string]any{
		"textDocument": map[string]any{"uri": fileURI(p.file), "text": text},
	})
}

func docMsg(method, uri string) string {
	return frame(method, nil, map[string]any{"textDocument": map[string]any{"uri": uri}})
}

func TestOpenPublishesUncoveredBlocks(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\n"+
		"example.com/m/pkg/a.go:1.1,1.5 1 1\n"+
		"example.com/m/pkg/a.go:2.1,3.10 1 0\n"+
		"example.com/m/pkg/a.go:9.1,9.5 1 0\n"+
		"example.com/m/pkg/a.go:3.1,10.1 1 0\n"+
		"example.com/m/pkg/a.go:0.1,0.5 1 0\n"+
		"example.com/m/pkg/other.go:1.1,1.5 1 0\n")
	text := "ok\r\nx := \"é😀\"\nend\n"

	msgs := runScript(t, append(initMsgs(p, nil), openMsg(p, text), frame("exit", nil, nil))...)

	var got []map[string]any
	for _, m := range msgs {
		if m["method"] == "textDocument/publishDiagnostics" {
			got = append(got, m)
		}
	}
	last := got[len(got)-1]
	want := `[{"message":"not covered by tests","range":{"end":{"character":3,"line":2},"start":{"character":0,"line":1}},"severity":3,"source":"go-coverage"}]`
	// Blocks ending past the buffer (9.5, 10.1) are skipped; 3.10 clamps to the line end.
	b, _ := json.Marshal(diagnosticsOf(last)[:1])
	if string(b) != want {
		t.Fatalf("got %s, want %s", b, want)
	}
	if n := len(diagnosticsOf(last)); n != 1 {
		t.Fatalf("got %d diagnostics, want 1", n)
	}
}

func TestUTF16Columns(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.12 1 0\n")
	// "é" is 2 bytes/1 unit, "😀" is 4 bytes/2 units: byte col 11 -> unit col 7.
	msgs := runScript(t, append(initMsgs(p, nil), openMsg(p, "a := \"é😀\"\n"), frame("exit", nil, nil))...)
	d := diagnosticsOf(msgs[len(msgs)-1])[0].(map[string]any)["range"].(map[string]any)["end"].(map[string]any)
	if d["character"].(float64) != 10 {
		t.Fatalf("end character = %v", d["character"])
	}
}

func TestLifecycleAndDocumentEvents(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	uri := fileURI(p.file)

	msgs := runScript(t, append(initMsgs(p, nil),
		openMsg(p, "ab\n"),
		frame("textDocument/didChange", nil, map[string]any{
			"textDocument":   map[string]any{"uri": uri},
			"contentChanges": []any{map[string]any{"text": "abc\n"}},
		}),
		frame("textDocument/didChange", nil, map[string]any{
			"textDocument":   map[string]any{"uri": uri},
			"contentChanges": []any{},
		}),
		docMsg("textDocument/didSave", uri),
		docMsg("textDocument/didClose", uri),
		frame("workspace/unknown", nil, nil),
		frame("workspace/unknown", 7, nil),
		frame("shutdown", 2, nil),
		frame("exit", nil, nil),
	)...)

	var counts []int
	var initResp, errResp, shutResp map[string]any
	for _, m := range msgs {
		switch {
		case m["method"] == "textDocument/publishDiagnostics":
			counts = append(counts, len(diagnosticsOf(m)))
		case m["id"] == float64(1):
			initResp = m
		case m["id"] == float64(7):
			errResp = m
		case m["id"] == float64(2):
			shutResp = m
		}
	}
	// open, change (cleared), save, close (cleared)
	if fmt.Sprint(counts) != "[1 0 1 0]" {
		t.Fatalf("diagnostic counts = %v", counts)
	}
	if initResp["result"].(map[string]any)["serverInfo"] == nil {
		t.Fatalf("bad initialize response: %v", initResp)
	}
	if errResp["error"].(map[string]any)["code"] != float64(-32601) {
		t.Fatalf("bad error response: %v", errResp)
	}
	if r, ok := shutResp["result"]; !ok || r != nil {
		t.Fatalf("bad shutdown response: %v", shutResp)
	}
}

func TestMalformedParamsAreIgnored(t *testing.T) {
	p := newProject(t)
	bad := func(method string) string { return frame(method, nil, "not an object") }
	msgs := runScript(t, append(initMsgs(p, nil),
		bad("textDocument/didOpen"),
		bad("textDocument/didChange"),
		bad("textDocument/didSave"),
		bad("textDocument/didClose"),
		frame("exit", nil, nil),
	)...)
	for _, m := range msgs {
		if m["method"] == "textDocument/publishDiagnostics" {
			t.Fatalf("unexpected diagnostics: %v", m)
		}
	}
}

func TestProfileOption(t *testing.T) {
	p := newProject(t)
	custom := filepath.Join(p.root, "out", "c.out")
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, custom, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	for _, profile := range []string{"out/c.out", custom} {
		opts := map[string]any{"profile": profile}
		msgs := runScript(t, append(initMsgs(p, opts), openMsg(p, "ab\n"), frame("exit", nil, nil))...)
		if n := len(diagnosticsOf(msgs[len(msgs)-1])); n != 1 {
			t.Fatalf("profile %q: got %d diagnostics, want 1", profile, n)
		}
	}
}

func TestNoDiagnosticsOutsideModule(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	outside := t.TempDir()
	for _, uri := range []string{fileURI(filepath.Join(outside, "x.go")), "untitled:Untitled-1", "file://%zz"} {
		msgs := runScript(t, append(initMsgs(p, nil),
			frame("textDocument/didOpen", nil, map[string]any{
				"textDocument": map[string]any{"uri": uri, "text": "ab\n"},
			}),
			frame("exit", nil, nil),
		)...)
		if n := len(diagnosticsOf(msgs[len(msgs)-1])); n != 0 {
			t.Fatalf("%s: got %d diagnostics, want 0", uri, n)
		}
	}
}

func TestNoModulePathInGoMod(t *testing.T) {
	p := newProject(t)
	write(t, filepath.Join(p.root, "go.mod"), "go 1.22\n")
	if _, ok := importPathFor(p.file); ok {
		t.Fatal("expected no import path")
	}
}

func TestReloadTracksProfileChanges(t *testing.T) {
	p := newProject(t)
	var out bytes.Buffer
	s := New(strings.NewReader(""), &out, time.Hour)
	s.root, s.profilePath = p.root, p.profile
	s.docs[fileURI(p.file)] = "ab\n"

	if err := s.reload(); err != nil || out.Len() != 0 {
		t.Fatalf("missing profile: err=%v out=%q", err, out.String())
	}

	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, &out); len(got) != 1 || len(diagnosticsOf(got[0])) != 1 {
		t.Fatalf("after create: %v", got)
	}

	if err := s.reload(); err != nil || out.Len() != 0 {
		t.Fatalf("unchanged profile: err=%v out=%q", err, out.String())
	}

	write(t, p.profile, "garbage")
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(p.profile, future, future)
	for i := 0; i < 2; i++ { // the failure is logged once, not on every retry
		if err := s.reload(); err != nil {
			t.Fatal(err)
		}
	}
	if got := readAll(t, &out); len(got) != 1 || got[0]["method"] != "window/logMessage" {
		t.Fatalf("invalid profile: %v", got)
	}

	if err := os.Remove(p.profile); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, &out); len(got) != 1 || len(diagnosticsOf(got[0])) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestReloadWithoutProfilePathIsNoop(t *testing.T) {
	s := New(strings.NewReader(""), io.Discard, time.Hour)
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
}

func TestReloadUnreadableProfile(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\n")
	if err := os.Chmod(p.profile, 0); err != nil {
		t.Fatal(err)
	}
	s := New(strings.NewReader(""), io.Discard, time.Hour)
	s.profilePath = p.profile
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	if s.profile != nil {
		t.Fatal("profile should stay unset")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func TestWriteErrorsPropagate(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	s := New(strings.NewReader(""), failWriter{}, time.Hour)
	s.profilePath = p.profile
	s.docs[fileURI(p.file)] = "ab\n"
	if err := s.reload(); err == nil {
		t.Fatal("expected write error")
	}

	if err := s.write(func() {}); err == nil {
		t.Fatal("expected marshal error")
	}

	in := strings.NewReader(frame("initialize", 1, map[string]any{}))
	if err := New(in, failWriter{}, time.Hour).Run(); err == nil {
		t.Fatal("expected Run to return the write error")
	}
}

func TestReadErrors(t *testing.T) {
	for name, in := range map[string]string{
		"bad length":     "Content-Length: x\r\n\r\n",
		"missing length": "Other: 1\r\n\r\n",
		"short body":     "Content-Length: 10\r\n\r\n{}",
		"bad json":       "Content-Length: 3\r\n\r\nabc",
	} {
		t.Run(name, func(t *testing.T) {
			if err := New(strings.NewReader(in), io.Discard, time.Hour).Run(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestRunStopsAtEOF(t *testing.T) {
	if err := New(strings.NewReader(""), io.Discard, time.Hour).Run(); err != nil {
		t.Fatal(err)
	}
}

func TestPollRepublishesWhenProfileChanges(t *testing.T) {
	p := newProject(t)
	pr, pw := io.Pipe()
	outR, outW := io.Pipe()
	s := New(pr, outW, 5*time.Millisecond)
	go func() {
		_ = s.Run()
		outW.Close()
	}()

	br := bufio.NewReader(outR)
	send := func(msgs ...string) {
		for _, m := range msgs {
			if _, err := io.WriteString(pw, m); err != nil {
				t.Error(err)
			}
		}
	}
	go send(append(initMsgs(p, nil), openMsg(p, "ab\n"))...)
	for _, want := range []int{-1, 0} { // initialize response, then empty diagnostics
		m, err := readFrame(br)
		if err != nil {
			t.Fatal(err)
		}
		if want == 0 && len(diagnosticsOf(m)) != 0 {
			t.Fatalf("unexpected diagnostics: %v", m)
		}
	}

	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	m, err := readFrame(br)
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnosticsOf(m)) != 1 {
		t.Fatalf("expected one diagnostic: %v", m)
	}

	go send(frame("exit", nil, nil))
	if _, err := readFrame(br); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestDirtyDocumentsAreNotRepublishedOnReload(t *testing.T) {
	tests := []struct {
		name   string
		events func(uri string) []string
		want   int
	}{
		{"edited", func(uri string) []string {
			return []string{frame("textDocument/didChange", nil, map[string]any{
				"textDocument":   map[string]any{"uri": uri},
				"contentChanges": []any{map[string]any{"text": "abc\n"}},
			})}
		}, 0},
		{"saved after edit", func(uri string) []string {
			return []string{
				frame("textDocument/didChange", nil, map[string]any{
					"textDocument":   map[string]any{"uri": uri},
					"contentChanges": []any{map[string]any{"text": "abc\n"}},
				}),
				docMsg("textDocument/didSave", uri),
			}
		}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			uri := fileURI(p.file)
			var out bytes.Buffer
			s := New(strings.NewReader(strings.Join(append(initMsgs(p, nil), append([]string{openMsg(p, "ab\n")}, tc.events(uri)...)...), "")+frame("exit", nil, nil)), &out, time.Hour)
			if err := s.Run(); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
			if err := s.reload(); err != nil {
				t.Fatal(err)
			}
			if got := len(readAll(t, &out)); got != tc.want {
				t.Fatalf("republished %d documents, want %d", got, tc.want)
			}
		})
	}
}

func TestInitializeWorkspaceFolders(t *testing.T) {
	p := newProject(t)
	write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
	msgs := runScript(t,
		frame("initialize", 1, map[string]any{
			"workspaceFolders": []any{map[string]any{"uri": fileURI(p.root)}},
		}),
		frame("initialized", nil, map[string]any{}),
		openMsg(p, "ab\n"),
		frame("exit", nil, nil),
	)
	if n := len(diagnosticsOf(msgs[len(msgs)-1])); n != 1 {
		t.Fatalf("got %d diagnostics, want 1", n)
	}
}

func TestUriToPath(t *testing.T) {
	tests := []struct{ name, uri, want string }{
		{"unix", "file:///a/b.go", "/a/b.go"},
		{"windows drive", "file:///C:/a/b.go", "C:/a/b.go"},
		{"not a file", "untitled:x", ""},
		{"invalid", "file://%zz", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := uriToPath(tc.uri); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestModulePath(t *testing.T) {
	tests := []struct {
		name, goMod, want string
		ok                bool
	}{
		{"plain", "module example.com/m\n", "example.com/m", true},
		{"quoted with comment", "module \"example.com/m\" // note\n", "example.com/m", true},
		{"tab separated", "module\texample.com/m\n", "example.com/m", true},
		{"longer keyword", "modulefoo example.com/m\n", "", false},
		{"bare keyword", "module\n", "", false},
		{"empty path", "module \"\"\n", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(t.TempDir(), "go.mod")
			write(t, f, tc.goMod)
			got, ok := modulePath(f)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestLogLoadErrorWriteFailure(t *testing.T) {
	s := New(strings.NewReader(""), failWriter{}, time.Hour)
	if err := s.logLoadError(errors.New("boom")); err == nil {
		t.Fatal("expected write error")
	}
}

func tokensReq(uri string) string {
	return frame("textDocument/semanticTokens/full", 5, map[string]any{"textDocument": map[string]any{"uri": uri}})
}

func tokenData(t *testing.T, msgs []map[string]any) string {
	t.Helper()
	for _, m := range msgs {
		if m["id"] == float64(5) {
			return fmt.Sprint(m["result"].(map[string]any)["data"])
		}
	}
	t.Fatalf("no token response in %v", msgs)
	return ""
}

func TestSemanticTokens(t *testing.T) {
	const profile = `mode: set
example.com/m/pkg/a.go:1.1,1.3 1 1
example.com/m/pkg/a.go:2.2,4.3 1 0
example.com/m/pkg/a.go:9.1,9.2 1 0
`
	tests := []struct {
		name  string
		text  string
		extra func(uri string) []string
		want  string
	}{
		{
			name: "covered and multi-line uncovered blocks",
			text: "ab\n\tcd\n\n\tef\n",
			// covered 1:1-3; uncovered 2:2-end, 4:1-3; the empty line 3 and the
			// out-of-range block at line 9 produce nothing.
			want: "[0 0 2 0 0 1 1 2 1 0 2 0 2 1 0]",
		},
		{
			name: "dirty document is empty",
			text: "ab\n\tcd\n\n\tef\n",
			extra: func(uri string) []string {
				return []string{frame("textDocument/didChange", nil, map[string]any{
					"textDocument":   map[string]any{"uri": uri},
					"contentChanges": []any{map[string]any{"text": "ab\n"}},
				})}
			},
			want: "[]",
		},
		{
			name: "unknown document is empty",
			text: "ab\n",
			extra: func(uri string) []string {
				return []string{docMsg("textDocument/didClose", uri)}
			},
			want: "[]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			write(t, p.profile, profile)
			uri := fileURI(p.file)
			script := append(initMsgs(p, nil), openMsg(p, tc.text))
			if tc.extra != nil {
				script = append(script, tc.extra(uri)...)
			}
			script = append(script, tokensReq(uri), frame("exit", nil, nil))
			got := tokenData(t, runScript(t, script...))
			if got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSemanticTokensOutsideModule(t *testing.T) {
	p := newProject(t)
	uri := fileURI(filepath.Join(t.TempDir(), "x.go"))
	msgs := runScript(t, append(initMsgs(p, nil),
		frame("textDocument/didOpen", nil, map[string]any{
			"textDocument": map[string]any{"uri": uri, "text": "ab\n"},
		}),
		tokensReq(uri), frame("exit", nil, nil))...)
	if got := tokenData(t, msgs); got != "[]" {
		t.Fatalf("got %s", got)
	}
}

const clientResponseBody = `{"jsonrpc":"2.0","id":"x","result":null}`

var clientResponse = fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(clientResponseBody), clientResponseBody)

func TestDiagnosticsOptionAndRefresh(t *testing.T) {
	tests := []struct {
		name        string
		opts        any
		refreshCap  bool
		wantDiags   int
		wantRefresh int
	}{
		{"defaults", nil, false, 2, 0},
		{"diagnostics off", map[string]any{"diagnostics": false}, false, 0, 0},
		{"refresh supported", map[string]any{"diagnostics": false}, true, 0, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 0\n")
			caps := map[string]any{}
			if tc.refreshCap {
				caps["workspace"] = map[string]any{"semanticTokens": map[string]any{"refreshSupport": true}}
			}
			msgs := runScript(t,
				frame("initialize", 1, map[string]any{
					"rootUri": fileURI(p.root), "initializationOptions": tc.opts, "capabilities": caps,
				}),
				frame("initialized", nil, map[string]any{}),
				openMsg(p, "ab\n"),
				// A client response to our refresh request must be ignored.
				clientResponse,
				docMsg("textDocument/didSave", fileURI(p.file)),
				frame("exit", nil, nil),
			)
			var diags, refresh int
			for _, m := range msgs {
				switch m["method"] {
				case "textDocument/publishDiagnostics":
					if len(diagnosticsOf(m)) > 0 {
						diags++
					}
				case "workspace/semanticTokens/refresh":
					refresh++
				}
				if _, isErr := m["error"]; isErr {
					t.Fatalf("unexpected error response: %v", m)
				}
			}
			if diags != tc.wantDiags {
				t.Fatalf("diagnostics = %d, want %d", diags, tc.wantDiags)
			}
			if refresh != tc.wantRefresh {
				t.Fatalf("refresh = %d, want %d", refresh, tc.wantRefresh)
			}
		})
	}
}

func TestPublishRefreshWriteFailure(t *testing.T) {
	s := New(strings.NewReader(""), failWriter{}, time.Hour)
	s.canRefresh = true
	if err := s.publish("file:///x.go"); err == nil {
		t.Fatal("expected write error")
	}
}

func TestDocumentColors(t *testing.T) {
	const profile = `mode: set
example.com/m/pkg/a.go:1.1,1.3 1 1
example.com/m/pkg/a.go:2.2,2.4 1 0
`
	tests := []struct {
		name string
		text string
		want []float64 // red of each returned color
	}{
		{"covered then uncovered", "ab\n\tcd\n", []float64{coveredColor.Red, uncoveredColor.Red}},
		{"no blocks in range", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			write(t, p.profile, profile)
			uri := fileURI(p.file)
			msgs := runScript(t, append(initMsgs(p, nil), openMsg(p, tc.text),
				frame("textDocument/documentColor", 5, map[string]any{"textDocument": map[string]any{"uri": uri}}),
				frame("exit", nil, nil))...)
			var got []float64
			for _, m := range msgs {
				if m["id"] != float64(5) {
					continue
				}
				for _, c := range m["result"].([]any) {
					got = append(got, c.(map[string]any)["color"].(map[string]any)["red"].(float64))
				}
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShowOption(t *testing.T) {
	tests := []struct {
		name string
		opts any
		want int
	}{
		{"default shows both", nil, 2},
		{"all", map[string]any{"show": "all"}, 2},
		{"uncovered only", map[string]any{"show": "uncovered"}, 1},
		{"covered only", map[string]any{"show": "covered"}, 1},
		{"unknown falls back to all", map[string]any{"show": "bogus"}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newProject(t)
			write(t, p.profile, "mode: set\nexample.com/m/pkg/a.go:1.1,1.3 1 1\nexample.com/m/pkg/a.go:2.2,2.4 1 0\n")
			uri := fileURI(p.file)
			msgs := runScript(t, append(initMsgs(p, tc.opts), openMsg(p, "ab\n\tcd\n"),
				frame("textDocument/documentColor", 5, map[string]any{"textDocument": map[string]any{"uri": uri}}),
				frame("exit", nil, nil))...)
			for _, m := range msgs {
				if m["id"] == float64(5) {
					if n := len(m["result"].([]any)); n != tc.want {
						t.Fatalf("got %d colors, want %d", n, tc.want)
					}
					return
				}
			}
			t.Fatal("no documentColor response")
		})
	}
}
