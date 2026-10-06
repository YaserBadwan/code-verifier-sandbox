package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/YaserBadwan/code-verifier-sandbox/internal/health"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/judge"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/languages"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox"
	"github.com/YaserBadwan/code-verifier-sandbox/internal/sandbox/sandboxtest"
)

func newTestServer(t *testing.T, runner *sandboxtest.Runner, ready bool, cors []string) *httptest.Server {
	return newGuardedServer(t, runner, ready, cors, nil)
}

func newGuardedServer(t *testing.T, runner *sandboxtest.Runner, ready bool, cors, hosts []string) *httptest.Server {
	return newServerWithUI(t, runner, ready, cors, hosts, nil)
}

func newServerWithUI(t *testing.T, runner *sandboxtest.Runner, ready bool, cors, hosts []string, ui fs.FS) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	langs, _ := languages.Select([]string{"python"})
	j := judge.New(judge.Config{
		Runner: runner, Images: sandboxtest.Images{}, Languages: langs,
		Limits: judge.Limits{
			DefaultTimeLimit: time.Second, MaxTimeLimit: 5 * time.Second,
			DefaultMemoryMB: 128, MaxMemoryMB: 512, CompileTimeout: 10 * time.Second, CompileMemoryMB: 512,
			OutputLimitBytes: 1 << 20, ReportBytes: 1 << 16, MaxSourceBytes: 1 << 16, MaxTests: 10,
		},
		MaxConcurrent: 2, MaxQueue: 4, Log: log,
	})
	srv := httptest.NewServer(New(Options{
		Judge: j,
		Health: func(context.Context) health.Report {
			return health.Report{Ready: ready, Docker: "ok", Images: map[string]string{"python:3.14-alpine": "ready"}}
		},
		Log: log, MaxBodyBytes: 4096, CORSOrigins: cors, AllowedHosts: hosts, UI: ui,
	}))
	t.Cleanup(srv.Close)
	return srv
}

const goodBody = `{"language":"python","source_code":"print(input())","test_cases":[{"input":"1","expected_output":"1"},{"input":"2","expected_output":"3"}]}`

func post(t *testing.T, url, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

func TestVerify(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	resp, out := post(t, srv.URL+"/v1/verify", goodBody)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %v", resp.StatusCode, out)
	}
	if out["verdict"] != "VERDICT_WRONG_ANSWER" || out["passed"] != float64(1) || out["total"] != float64(2) {
		t.Fatalf("unexpected body %v", out)
	}
	results := out["results"].([]any)
	first := results[0].(map[string]any)
	if first["verdict"] != "VERDICT_ACCEPTED" || first["stdout"] != "1" {
		t.Fatalf("unexpected first result %v", first)
	}
	// zero values are still present so clients don't have to guess
	if _, ok := first["exit_code"]; !ok {
		t.Fatal("exit_code should always be present")
	}
}

func TestVerifyAcceptsCamelCase(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	body := `{"language":"python","sourceCode":"print(1)","testCases":[{"input":"1","expectedOutput":"1"}],"compareMode":"COMPARE_MODE_TOKENS"}`
	resp, out := post(t, srv.URL+"/v1/verify", body)
	if resp.StatusCode != 200 || out["verdict"] != "VERDICT_ACCEPTED" {
		t.Fatalf("status %d %v", resp.StatusCode, out)
	}
}

func TestVerifyErrors(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	cases := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"broken json", `{"language":`, 400, "INVALID_ARGUMENT"},
		{"unknown field", `{"langauge":"python"}`, 400, "INVALID_ARGUMENT"},
		{"unknown language", `{"language":"cobol","source_code":"x","test_cases":[{}]}`, 400, "INVALID_ARGUMENT"},
		{"wrong type", `{"language":"python","time_limit_ms":"soon"}`, 400, "INVALID_ARGUMENT"},
		{"too big", `{"language":"python","source_code":"` + strings.Repeat("x", 5000) + `"}`, 413, "RESOURCE_EXHAUSTED"},
		{"empty", ``, 400, "INVALID_ARGUMENT"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp, out := post(t, srv.URL+"/v1/verify", c.body)
			if resp.StatusCode != c.status {
				t.Fatalf("status %d, want %d, body %v", resp.StatusCode, c.status, out)
			}
			errObj, _ := out["error"].(map[string]any)
			if errObj["code"] != c.code || errObj["message"] == "" {
				t.Fatalf("unexpected error body %v", out)
			}
		})
	}
}

func TestVerifyStream(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	resp, err := http.Post(srv.URL+"/v1/verify?stream=true", "application/json", strings.NewReader(goodBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var keys []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("bad line %q", sc.Text())
		}
		for k := range line {
			keys = append(keys, k)
		}
	}
	if strings.Join(keys, ",") != "compile,test_result,test_result,summary" {
		t.Fatalf("unexpected stream %v", keys)
	}
}

func TestVerifyStreamValidationError(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	resp, out := post(t, srv.URL+"/v1/verify?stream=1", `{"language":"cobol","source_code":"x","test_cases":[{}]}`)
	if resp.StatusCode != 400 || out["error"] == nil {
		t.Fatalf("errors before streaming starts should be normal responses, got %d %v", resp.StatusCode, out)
	}
}

func TestVerifyStreamLateError(t *testing.T) {
	runner := sandboxtest.NewRunner()
	runner.CompileFunc = func(sandbox.Spec) (sandbox.Result, error) { return sandbox.Result{}, nil }
	runner.RunFunc = func(ctx context.Context, spec sandbox.Spec) (sandbox.Result, error) {
		panic("boom")
	}
	srv := newTestServer(t, runner, true, nil)
	resp, err := http.Post(srv.URL+"/v1/verify?stream=true", "application/json", strings.NewReader(goodBody))
	if err != nil {
		// the panic aborts the connection, which is also acceptable for a streaming response
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "compile") {
		t.Fatalf("expected the compile line before the failure, got %q", body)
	}
}

func TestPanicBeforeResponse(t *testing.T) {
	runner := sandboxtest.NewRunner()
	runner.CompileFunc = func(sandbox.Spec) (sandbox.Result, error) { panic("boom") }
	srv := newTestServer(t, runner, true, nil)
	resp, out := post(t, srv.URL+"/v1/verify", goodBody)
	if resp.StatusCode != 500 || out["error"] == nil {
		t.Fatalf("expected a json 500, got %d %v", resp.StatusCode, out)
	}
}

func TestLanguages(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	resp, err := http.Get(srv.URL + "/v1/languages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Languages []struct {
			ID    string `json:"id"`
			Ready bool   `json:"ready"`
		} `json:"languages"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Languages) != 1 || out.Languages[0].ID != "python" || !out.Languages[0].Ready {
		t.Fatalf("unexpected %+v", out)
	}
}

func TestHealthEndpoints(t *testing.T) {
	ready := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	notReady := newTestServer(t, sandboxtest.NewRunner(), false, nil)
	for _, c := range []struct {
		url  string
		code int
	}{
		{ready.URL + "/healthz", 200},
		{notReady.URL + "/healthz", 200},
		{ready.URL + "/readyz", 200},
		{notReady.URL + "/readyz", 503},
	} {
		resp, err := http.Get(c.url)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.code {
			t.Fatalf("%s returned %d, want %d", c.url, resp.StatusCode, c.code)
		}
	}
}

func TestRoutingErrors(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	resp, out := post(t, srv.URL+"/nope", "{}")
	if resp.StatusCode != 404 || out["error"] == nil {
		t.Fatalf("expected json 404, got %d %v", resp.StatusCode, out)
	}
	get, err := http.Get(srv.URL + "/v1/verify")
	if err != nil {
		t.Fatal(err)
	}
	get.Body.Close()
	if get.StatusCode != 405 || get.Header.Get("Allow") != "POST" {
		t.Fatalf("GET on verify should be 405 with Allow POST, got %d %q", get.StatusCode, get.Header.Get("Allow"))
	}
}

func TestCORS(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, []string{"http://localhost:3000"})
	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/verify", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Fatalf("preflight failed %d %v", resp.StatusCode, resp.Header)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/languages", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unlisted origins must be refused, got %d", resp.StatusCode)
	}
}

func TestBrowserAttacksAreRefused(t *testing.T) {
	runner := sandboxtest.NewRunner()
	srv := newGuardedServer(t, runner, true, nil, []string{"localhost", "127.0.0.1", "::1"})
	send := func(contentType, origin, host string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/verify", strings.NewReader(goodBody))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	cases := []struct {
		name, contentType, origin, host string
		want                            int
	}{
		{"plain client", "application/json", "", "", 200},
		{"json with charset", "application/json; charset=utf-8", "", "", 200},
		{"localhost host", "application/json", "", "localhost:8080", 200},
		{"cross site form post", "text/plain", "https://evil.example", "", 403},
		{"text plain without origin", "text/plain", "", "", 415},
		{"no content type", "", "", "", 415},
		{"any foreign origin", "application/json", "https://evil.example", "", 403},
		{"null origin", "application/json", "null", "", 403},
		{"dns rebinding", "application/json", "", "attacker.example:8080", 403},
	}
	for _, c := range cases {
		if got := send(c.contentType, c.origin, c.host); got != c.want {
			t.Fatalf("%s got %d, want %d", c.name, got, c.want)
		}
	}
	// 3 legitimate requests with 2 tests each
	if runner.Runs() != 6 {
		t.Fatalf("only the legitimate requests should have run code, got %d runs", runner.Runs())
	}
}

func TestNoCORSByDefault(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/languages", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("browsers should be refused unless their origin is configured")
	}
}

func TestServesThePage(t *testing.T) {
	ui := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>Code Verifier</title>")},
		"app.js":     {Data: []byte("console.log(1)")},
	}
	srv := newServerWithUI(t, sandboxtest.NewRunner(), true, nil, []string{"localhost", "127.0.0.1", "::1"}, ui)
	for _, c := range []struct {
		path, wantType string
		want           int
	}{
		{"/", "text/html", 200},
		{"/ui/app.js", "text/javascript", 200},
		{"/ui/missing.css", "", 404},
		{"/index.html", "", 404},
	} {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want || !strings.HasPrefix(resp.Header.Get("Content-Type"), c.wantType) {
			t.Fatalf("%s got %d %q", c.path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if c.want == 200 && !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s is missing the security headers", c.path)
		}
	}
}

func TestPageCanCallTheAPI(t *testing.T) {
	runner := sandboxtest.NewRunner()
	srv := newServerWithUI(t, runner, true, nil, []string{"localhost", "127.0.0.1", "::1"}, fstest.MapFS{"index.html": {Data: []byte("x")}})
	send := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/verify", strings.NewReader(goodBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := send(srv.URL); got != 200 {
		t.Fatalf("the page's own origin should be allowed, got %d", got)
	}
	if got := send("http://127.0.0.1:1"); got != 403 {
		t.Fatalf("another local port is a different origin and must be refused, got %d", got)
	}
}

func TestNoPageWithoutUI(t *testing.T) {
	srv := newTestServer(t, sandboxtest.NewRunner(), true, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("expected 404 without a UI, got %d", resp.StatusCode)
	}
}
