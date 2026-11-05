package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttachUploadsEachFile(t *testing.T) {
	var got []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer hdl_up_x" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.URL.Query().Get("name")+"="+string(b))
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"img_`+r.URL.Query().Get("name")[:1]+`"}`)
	}))
	defer hs.Close()
	dir := t.TempDir()
	for _, n := range []string{"a.png", "b.png"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out strings.Builder
	err := attach(context.Background(), hs.URL+"/attachments", "hdl_up_x",
		[]string{filepath.Join(dir, "a.png"), filepath.Join(dir, "b.png")}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.png=a.png,b.png=b.png" || !strings.Contains(out.String(), `attachments: ["img_a","img_b"]`) {
		t.Fatalf("got %v, printed %q", got, out.String())
	}
	if err := attach(context.Background(), hs.URL+"/attachments", "wrong", []string{filepath.Join(dir, "a.png")}, io.Discard); err == nil {
		t.Fatal("a bad ticket should fail")
	}
}
