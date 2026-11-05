package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// attach uploads each file to the huddle server with an upload ticket and
// prints one JSON line per stored image; their ids go in ask or draft_answer.
func attach(ctx context.Context, uploadURL, ticket string, files []string, out io.Writer) error {
	client := &http.Client{Timeout: 2 * time.Minute}
	var ids []string
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		u, err := url.Parse(uploadURL)
		if err != nil {
			f.Close()
			return fmt.Errorf("upload url: %w", err)
		}
		q := u.Query()
		q.Set("name", filepath.Base(path))
		u.RawQuery = q.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), f)
		if err != nil {
			f.Close()
			return err
		}
		req.Header.Set("Authorization", "Bearer "+ticket)
		req.Header.Set("Content-Type", "application/octet-stream")
		res, err := client.Do(req)
		f.Close()
		if err != nil {
			return fmt.Errorf("upload %s: %w", path, err)
		}
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			return fmt.Errorf("upload %s: %s: %s", path, res.Status, strings.TrimSpace(string(body)))
		}
		var a struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &a); err != nil || a.ID == "" {
			return fmt.Errorf("upload %s: unexpected answer %q", path, body)
		}
		ids = append(ids, a.ID)
		fmt.Fprintln(out, strings.TrimSpace(string(body)))
	}
	b, _ := json.Marshal(ids)
	fmt.Fprintf(out, "attachments: %s\n", b)
	return nil
}
