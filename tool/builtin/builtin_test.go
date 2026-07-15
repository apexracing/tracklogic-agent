package builtin

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	tool := NewHTTPGet()
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

	tool := NewHTTPGet()
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
