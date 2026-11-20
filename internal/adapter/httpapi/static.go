package httpapi

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

//go:embed all:dist
var embeddedDist embed.FS

const placeholderSPAHTML = "<!doctype html><html lang=\"vi\"><head><meta charset=\"utf-8\"><title>Max</title></head><body><div id=\"app\"></div></body></html>"

var inlineScriptRe = regexp.MustCompile(`(?is)<script(?:\s[^>]*)?>(.*?)</script>`)

func defaultDistFS() fs.FS {
	sub, err := fs.Sub(embeddedDist, "dist")
	if err != nil {
		return embeddedDist
	}
	return sub
}

func loadSPAHTML(distFS fs.FS) []byte {
	for _, name := range []string{"200.html", "index.html"} {
		if b, err := fs.ReadFile(distFS, name); err == nil && len(b) > 0 {
			return b
		}
	}
	return []byte(placeholderSPAHTML)
}

func scriptCSPHashes(htmlBytes []byte) []string {
	matches := inlineScriptRe.FindAllSubmatch(htmlBytes, -1)
	var hashes []string
	for _, m := range matches {
		if len(m) < 2 || len(bytes.TrimSpace(m[1])) == 0 {
			continue
		}
		sum := sha256.Sum256(m[1])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}

func acceptsEncoding(header, target string) bool {
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, params, hasParams := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(name), target) {
			continue
		}
		if hasParams {
			for p := range strings.SplitSeq(params, ";") {
				k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
				if ok && strings.EqualFold(strings.TrimSpace(k), "q") {
					q := strings.TrimSpace(v)
					if q == "0" || q == "0.0" || q == "0.00" || q == "0.000" {
						return false
					}
				}
			}
		}
		return true
	}
	return false
}

func contentTypeByPath(cleanPath string) string {
	ext := strings.ToLower(path.Ext(cleanPath))
	switch ext {
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".html":
		return "text/html; charset=utf-8"
	default:
		if ct := mime.TypeByExtension(ext); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}

func servePrecompressedOrRaw(w http.ResponseWriter, r *http.Request, distFS fs.FS, clean string, modTime time.Time) bool {
	w.Header().Set("Vary", "Accept-Encoding")
	ct := contentTypeByPath(clean)
	ae := r.Header.Get("Accept-Encoding")

	if acceptsEncoding(ae, "br") {
		if b, err := fs.ReadFile(distFS, clean+".br"); err == nil {
			w.Header().Set("Content-Encoding", "br")
			w.Header().Set("Content-Type", ct)
			http.ServeContent(w, r, path.Base(clean), modTime, bytes.NewReader(b))
			return true
		}
	}
	if acceptsEncoding(ae, "gzip") {
		if b, err := fs.ReadFile(distFS, clean+".gz"); err == nil {
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", ct)
			http.ServeContent(w, r, path.Base(clean), modTime, bytes.NewReader(b))
			return true
		}
	}
	b, err := fs.ReadFile(distFS, clean)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, path.Base(clean), modTime, bytes.NewReader(b))
	return true
}

func newStaticHandler(distFS fs.FS) (http.Handler, string) {
	if distFS == nil {
		distFS = defaultDistFS()
	}
	spaHTML := loadSPAHTML(distFS)
	csp := buildCSPHeader(scriptCSPHashes(spaHTML))

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			writeJSONError(w, http.StatusNotFound, "not_found")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == ".gitkeep" || strings.HasSuffix(clean, ".br") || strings.HasSuffix(clean, ".gz") {
			http.NotFound(w, r)
			return
		}

		if clean != "" && clean != "200.html" && clean != "index.html" {
			if info, err := fs.Stat(distFS, clean); err == nil && !info.IsDir() {
				if strings.HasPrefix(clean, "_app/immutable/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
				if servePrecompressedOrRaw(w, r, distFS, clean, info.ModTime()) {
					return
				}
			}
			if strings.HasPrefix(clean, "_app/") || path.Ext(clean) != "" {
				http.NotFound(w, r)
				return
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(spaHTML)
		}
	})
	return h, csp
}
