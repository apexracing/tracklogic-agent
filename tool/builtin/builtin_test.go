package builtin

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCurrentTime_UTC(t *testing.T) {
	tool := NewCurrentTime()
	out, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["timezone"] != "UTC" {
		t.Errorf("timezone = %v", m["timezone"])
	}
	rfc, _ := m["rfc3339"].(string)
	if _, err := time.Parse(time.RFC3339, rfc); err != nil {
		t.Errorf("rfc3339 invalid: %v", err)
	}
}

func TestCurrentTime_InvalidTZ(t *testing.T) {
	tool := NewCurrentTime()
	_, err := tool.Execute(context.Background(), map[string]any{"timezone": "Not/AZone"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}

	tool := NewListDir(dir)
	out, err := tool.Execute(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	entries := m["entries"].([]map[string]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
}

func TestListDir_Traversal(t *testing.T) {
	dir := t.TempDir()
	tool := NewListDir(dir)
	_, err := tool.Execute(context.Background(), map[string]any{"path": "../.."})
	if err == nil {
		t.Fatal("expected traversal error")
	}
	if !strings.Contains(err.Error(), "securely open") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestFileToolsRejectSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "secret-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := NewReadFile(root).Execute(context.Background(), map[string]any{"path": "secret-link"}); err == nil {
		t.Fatal("ReadFile followed a symlink outside the allowed root")
	}

	if err := os.Symlink(outside, filepath.Join(root, "outside-dir")); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, err := NewWriteFile(root).Execute(context.Background(), map[string]any{
		"path": "outside-dir/created.txt", "content": "escaped",
	}); err == nil {
		t.Fatal("WriteFile followed a symlink outside the allowed root")
	}
	if _, err := os.Stat(filepath.Join(outside, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("outside file exists or stat failed unexpectedly: %v", err)
	}
}

func TestReadFileRejectsOversizedContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(strings.Repeat("x", maxFileContentBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReadFile(root).Execute(context.Background(), map[string]any{"path": "large.txt"}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("ReadFile error = %v, want size limit", err)
	}
}

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	tool := NewHTTPGetWithConfig(HTTPGetConfig{AllowPrivateNetworks: true})
	out, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["status"] != 200 {
		t.Errorf("status = %v", m["status"])
	}
	if m["body"] != "hello" {
		t.Errorf("body = %v", m["body"])
	}
}

func TestHTTPGet_Truncate(t *testing.T) {
	big := strings.Repeat("x", maxHTTPBodyBytes+100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	tool := NewHTTPGetWithConfig(HTTPGetConfig{AllowPrivateNetworks: true})
	out, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["truncated"] != true {
		t.Error("expected truncated")
	}
	body := m["body"].(string)
	if len(body) != maxHTTPBodyBytes {
		t.Errorf("body len = %d", len(body))
	}
}

func TestHTTPGetBlocksPrivateNetworksByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("should not be reached"))
	}))
	defer server.Close()

	_, err := NewHTTPGet().Execute(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "disallowed address") {
		t.Fatalf("HTTPGet error = %v, want private-network rejection", err)
	}
}

func TestHTTPGetRevalidatesRedirectHost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host, port, _ := net.SplitHostPort(request.Host)
		_ = host
		http.Redirect(writer, request, "http://localhost:"+port+"/redirected", http.StatusFound)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	runtimeTool := NewHTTPGetWithConfig(HTTPGetConfig{
		AllowPrivateNetworks: true,
		AllowedHosts:         []string{parsed.Hostname()},
	})
	_, err = runtimeTool.Execute(context.Background(), map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("HTTPGet redirect error = %v", err)
	}
}

func TestJSONParse(t *testing.T) {
	tool := NewJSONParse()
	out, err := tool.Execute(context.Background(), map[string]any{
		"text": `{"a":1,"b":[true]}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	val := m["value"].(map[string]any)
	if val["a"].(float64) != 1 {
		t.Errorf("a = %v", val["a"])
	}
}

func TestJSONParse_Invalid(t *testing.T) {
	tool := NewJSONParse()
	_, err := tool.Execute(context.Background(), map[string]any{"text": "{bad"})
	if err == nil {
		t.Fatal("expected error")
	}
}
