package releases

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Download struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}
type Catalog struct {
	Releases []Download `json:"releases"`
}

// Installer downloads include legacy clients outside the signed update format.
func validDownloadTarget(platform, arch string) bool {
	return validTarget(platform, arch) || (platform == "windows7" && (arch == "x86" || arch == "x86_64"))
}

func digestFile(name string) (string, int64, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxTotalSize+1))
	return hex.EncodeToString(h.Sum(nil)), n, err
}
func AddDownload(root, source, platform, arch, version string) error {
	if !validDownloadTarget(platform, arch) || !validVersion(version) {
		return os.ErrInvalid
	}
	name := filepath.Base(source)
	if !safePath(name) || strings.Contains(name, "/") {
		return os.ErrInvalid
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxTotalSize {
		return os.ErrInvalid
	}
	hash, size, err := digestFile(source)
	if err != nil {
		return err
	}
	if err = copyObject(source, filepath.Join(root, "downloads", name), hash, size); err != nil {
		return err
	}
	catalog := readCatalog(root)
	next := Catalog{Releases: []Download{}}
	for _, d := range catalog.Releases {
		if d.Name != name {
			next.Releases = append(next.Releases, d)
		}
	}
	next.Releases = append(next.Releases, Download{Platform: platform, Arch: arch, Version: version, Name: name, URL: "/releases/" + name, Size: size, SHA256: hash})
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(root, "downloads.json"), raw, 0600)
}
func readCatalog(root string) Catalog {
	c := Catalog{Releases: []Download{}}
	if root == "" {
		return c
	}
	f, err := os.Open(filepath.Join(root, "downloads.json"))
	if err != nil {
		return c
	}
	defer f.Close()
	var raw Catalog
	if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(&raw) != nil || len(raw.Releases) > 100 {
		return c
	}
	for _, d := range raw.Releases {
		if !validDownloadTarget(d.Platform, d.Arch) || !validVersion(d.Version) || !safePath(d.Name) || strings.Contains(d.Name, "/") || d.URL != "/releases/"+d.Name || d.Size < 0 || d.Size > MaxTotalSize || !hashPattern.MatchString(d.SHA256) {
			continue
		}
		name := filepath.Join(root, "downloads", d.Name)
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() != d.Size {
			continue
		}
		hash, size, err := digestFile(name)
		if err != nil || size != d.Size || hash != d.SHA256 {
			continue
		}
		c.Releases = append(c.Releases, d)
	}
	return c
}

type handler struct{ root string }

// NewHandler serves only fixed release routes. Mount at /downloads, /api/v1/releases,
// /releases/ and /updates/; no authentication or server state is exposed.
func NewHandler(root string) http.Handler { return &handler{root: root} }
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != "GET" && r.Method != "HEAD" {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", 405)
		return
	}
	if strings.ContainsAny(r.URL.Path, "\\\x00") || strings.Contains(r.URL.Path, "..") || strings.Contains(r.URL.EscapedPath(), "%2f") {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/api/v1/releases" {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(readCatalog(h.root))
		return
	}
	if r.URL.Path == "/downloads" || r.URL.Path == "/downloads/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		var page bytes.Buffer
		if err := downloadPage.Execute(&page, buildDownloadPage(readCatalog(h.root))); err != nil {
			http.Error(w, "download page unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodHead {
			w.Write(page.Bytes())
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/releases/") {
		name := strings.TrimPrefix(r.URL.Path, "/releases/")
		for _, d := range readCatalog(h.root).Releases {
			if d.Name == name {
				w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(d.Name, `"`, "")+`"`)
				serveRegular(w, r, h.root, "downloads/"+name)
				return
			}
		}
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/updates/") {
		rel := strings.TrimPrefix(r.URL.Path, "/updates/")
		parts := strings.Split(rel, "/")
		if len(parts) == 2 && parts[0] == "objects" && hashPattern.MatchString(parts[1]) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			serveRegular(w, r, h.root, rel)
			return
		}
		if len(parts) == 3 && validTarget(parts[0], parts[1]) && parts[2] == "latest.json" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			serveRegular(w, r, h.root, rel)
			return
		}
	}
	http.NotFound(w, r)
}
func serveRegular(w http.ResponseWriter, r *http.Request, root, relative string) {
	if root == "" || !safePath(relative) {
		http.NotFound(w, r)
		return
	}
	current := root
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			http.NotFound(w, r)
			return
		}
	}
	f, err := os.Open(current)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

//go:embed downloads.html
var downloadPageHTML string

var downloadPage = template.Must(template.New("downloads").Parse(downloadPageHTML))
