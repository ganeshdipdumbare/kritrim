package a2a

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/ganeshdipdumbare/kritrim"
	"github.com/ganeshdipdumbare/kritrim/internal/netguard"
)

// imageTypes are the image formats every kritrim provider accepts.
var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// textType reports whether a file of this type is passed to the model as
// text.
func textType(mt string) bool {
	return strings.HasPrefix(mt, "text/") || mt == "application/json" || mt == "application/xml" ||
		mt == "application/yaml" || mt == "application/x-yaml" || mt == "application/csv"
}

// inputFiles turns the message's file parts into images and text the agent
// can read. Images go to the model as images; text files are appended to
// the prompt in a <file> block; anything else is refused, because the
// model cannot read it and silently dropping it would answer a different
// question.
func (s *Server) inputFiles(ctx context.Context, msg Message) (prompt string, images []kritrim.Image, err error) {
	var text strings.Builder
	total := 0
	for _, p := range msg.Parts {
		if p.Kind != KindFile {
			continue
		}
		if p.File == nil {
			return "", nil, errors.New("file part has no file")
		}
		f := *p.File
		if len(f.Bytes) > 0 && f.URI != "" {
			return "", nil, fmt.Errorf("file %q has both bytes and uri", f.Name)
		}
		data := f.Bytes
		mt := f.MimeType
		if f.URI != "" {
			var fetched string
			data, fetched, err = s.fetchFile(ctx, f.URI)
			if err != nil {
				return "", nil, fmt.Errorf("file %q: %w", f.Name, err)
			}
			if mt == "" {
				mt = fetched
			}
		}
		if len(data) == 0 {
			return "", nil, fmt.Errorf("file %q is empty", f.Name)
		}
		total += len(data)
		if total > s.cfg.MaxFileBytes {
			return "", nil, fmt.Errorf("files exceed %d bytes", s.cfg.MaxFileBytes)
		}
		mt, _, _ = mime.ParseMediaType(mt)
		switch {
		case imageTypes[mt]:
			images = append(images, kritrim.Image{MediaType: mt, Data: data})
		case textType(mt):
			if !utf8.Valid(data) {
				return "", nil, fmt.Errorf("file %q is not valid UTF-8", f.Name)
			}
			name := strings.NewReplacer(`"`, "", "<", "", ">", "").Replace(f.Name)
			fmt.Fprintf(&text, "\n\n<file name=%q type=%q>\n%s\n</file>", name, mt, data)
		default:
			return "", nil, fmt.Errorf("file %q has unsupported type %q (images and text only)", f.Name, mt)
		}
	}
	return text.String(), images, nil
}

// fetchFile downloads a file URI from an allowed host. The connection is
// checked at dial time (public addresses only, unless the host is listed
// exactly), redirects are not followed, and the body is capped.
func (s *Server) fetchFile(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, "", errors.New("uri must be http(s)")
	}
	if !allowed(u.Hostname(), s.cfg.FileHosts) {
		return nil, "", fmt.Errorf("uri host %q is not in FileHosts", u.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := s.fileClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("fetch: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(s.cfg.MaxFileBytes)+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > s.cfg.MaxFileBytes {
		return nil, "", fmt.Errorf("file exceeds %d bytes", s.cfg.MaxFileBytes)
	}
	return b, resp.Header.Get("Content-Type"), nil
}

// fileHTTPClient guards file fetches the way push requests are guarded.
// Exact FileHosts entries may be private; wildcard entries must resolve to
// public addresses.
func fileHTTPClient(base *http.Client, hosts []string) (*http.Client, error) {
	exact := map[string]bool{}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if !strings.HasPrefix(h, "*.") {
			exact[h] = true
		}
	}
	guarded, ok := netguard.Client(base, func(host string) bool { return exact[host] })
	if !ok {
		return nil, errors.New("a2a: FileClient must use an *http.Transport so file fetches can be checked")
	}
	c := *guarded
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c, nil
}
