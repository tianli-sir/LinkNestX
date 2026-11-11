package releases

import (
	"encoding/json"
	"html"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A platform or format mix-up must not recommend a portable archive over its
// installer, and every displayed download must resolve to the verified bytes.
func TestDownloadsPageGroupsVerifiedInstallersAndAlternatives(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	fixtures := []struct {
		name, platform, arch, version string
	}{
		{"LinkNestX_0.11.0_x64.msi", "windows", "x86_64", "0.11.0"},
		{"LinkNestX_0.9.0_x64-setup.exe", "windows", "x86_64", "0.9.0"},
		{"LinkNestX_0.10.0_x64-setup.exe", "windows", "x86_64", "0.10.0"},
		{"LinkNestX_0.11.0_windows7_x64.zip", "windows7", "x86_64", "0.11.0"},
		{"LinkNestX_0.10.0_windows7_x64-setup.exe", "windows7", "x86_64", "0.10.0"},
		{"LinkNestX_0.10.0_windows7_x86-setup.exe", "windows7", "x86", "0.10.0"},
		{"LinkNestX_0.10.0_aarch64.dmg", "macos", "aarch64", "0.10.0"},
		{"LinkNestX_0.10.0_x64.dmg", "macos", "x86_64", "0.10.0"},
	}
	contents := strings.Repeat("a", 1536)
	for _, f := range fixtures {
		path := filepath.Join(source, f.name)
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if err := AddDownload(root, path, f.platform, f.arch, f.version); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewHandler(root)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/downloads", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.HasSuffix(strings.TrimSpace(body), "</html>") || strings.Contains(body, "ZgotmplZ") {
		t.Fatalf("download page did not finish rendering: %d %.300s", w.Code, body)
	}
	for _, want := range []string{`id="windows"`, `id="windows7"`, `id="macos"`, "Apple 芯片", "Intel 芯片", "1.5 KB", "v0.10.0", "SHA-256", `href="/app/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing download information %q", want)
		}
	}
	for _, pair := range [][2]string{
		{"LinkNestX_0.10.0_x64-setup.exe", "LinkNestX_0.9.0_x64-setup.exe"},
		{"LinkNestX_0.10.0_x64-setup.exe", "LinkNestX_0.11.0_x64.msi"},
		{"LinkNestX_0.10.0_windows7_x64-setup.exe", "LinkNestX_0.11.0_windows7_x64.zip"},
	} {
		if strings.Index(body, `href="/releases/`+pair[0]+`"`) >= strings.Index(body, `href="/releases/`+pair[1]+`"`) {
			t.Errorf("recommended installer %s must precede alternative %s", pair[0], pair[1])
		}
	}
	links := regexp.MustCompile(`href="(/releases/[^"]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	for _, link := range links {
		url := html.UnescapeString(link[1])
		seen[url] = true
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 || w.Body.String() != contents {
			t.Errorf("download link %q does not serve the artifact: %d", url, w.Code)
		}
	}
	if len(seen) != len(fixtures) {
		t.Errorf("want exactly %d artifact URLs, got %d", len(fixtures), len(seen))
	}
}

func TestDownloadsPageExcludesInvalidOrMissingCatalogFiles(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	for _, name := range []string{"valid.exe", "missing.dmg", "corrupt.exe", "invalid.exe"} {
		path := filepath.Join(source, name)
		if err := os.WriteFile(path, []byte("valid fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := AddDownload(root, path, "windows", "x86_64", "0.3.0"); err != nil {
			t.Fatal(err)
		}
	}
	catalog := readCatalog(root)
	catalog.Releases[3].URL = "https://example.invalid/unsafe.exe"
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "downloads.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "downloads", "missing.dmg")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "downloads", "corrupt.exe"), []byte("other fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewHandler(root).ServeHTTP(w, httptest.NewRequest("GET", "/downloads/", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `href="/releases/valid.exe"`) {
		t.Fatalf("valid artifact missing: %d", w.Code)
	}
	for _, unwanted := range []string{"missing.dmg", "corrupt.exe", "invalid.exe", "example.invalid"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unverified artifact was rendered: %s", unwanted)
		}
	}
	if strings.Contains(body, `href="/releases/LinkNestX`) {
		t.Fatal("page synthesized an unpublished artifact URL")
	}
}

func TestDownloadsPageEmptyCatalogHasNoDownloadLinks(t *testing.T) {
	w := httptest.NewRecorder()
	NewHandler(t.TempDir()).ServeHTTP(w, httptest.NewRequest("GET", "/downloads", nil))
	body := w.Body.String()
	if w.Code != 200 || !strings.HasSuffix(strings.TrimSpace(body), "</html>") {
		t.Fatalf("empty catalog did not render: %d", w.Code)
	}
	if strings.Contains(body, `href="/releases/`) || !strings.Contains(body, "暂未提供") || !strings.Contains(body, `href="/app/"`) {
		t.Fatal("empty page must explain availability and offer the web app without fake downloads")
	}
	if strings.Contains(body, "<script") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download page must retain static content security")
	}
}

func TestDownloadVersionPrecedence(t *testing.T) {
	for _, pair := range [][2]string{
		{"0.10.0", "0.9.9"},
		{"1.0.0", "1.0.0-rc.9"},
		{"1.0.0-rc.10", "1.0.0-rc.2"},
		{"1.0.0-beta", "1.0.0-alpha"},
		{"1.0.0-beta.1", "1.0.0-beta"},
		{"1.0.0-alpha", "1.0.0-9"},
	} {
		if compareDownloadVersion(pair[0], pair[1]) <= 0 || compareDownloadVersion(pair[1], pair[0]) >= 0 {
			t.Errorf("wrong precedence for %s > %s", pair[0], pair[1])
		}
	}
	if compareDownloadVersion("1.0.0+build.7", "1.0.0+build.8") != 0 {
		t.Fatal("build metadata must not affect version precedence")
	}
}

func TestDownloadSizeLabels(t *testing.T) {
	for _, tc := range []struct {
		size int64
		want string
	}{{0, "0 B"}, {1023, "1023 B"}, {1024, "1.0 KB"}, {1536, "1.5 KB"}, {1048576, "1.0 MB"}, {1610612736, "1.5 GB"}} {
		if got := downloadSize(tc.size); got != tc.want {
			t.Errorf("size %d: got %q, want %q", tc.size, got, tc.want)
		}
	}
}

func TestDownloadsPageUsesOnlyPublishedFallbackFormats(t *testing.T) {
	for _, fixture := range []struct{ name, platform, arch, format string }{
		{"only.msi", "windows", "x86_64", "MSI 安装包"},
		{"only.zip", "windows7", "x86", "ZIP 便携版"},
		{"only.pkg", "macos", "aarch64", "PKG 安装包"},
		{"only.bin", "macos", "x86_64", "客户端文件"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(t.TempDir(), fixture.name)
			if err := os.WriteFile(source, []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := AddDownload(root, source, fixture.platform, fixture.arch, "0.3.0"); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			NewHandler(root).ServeHTTP(w, httptest.NewRequest("GET", "/downloads", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), fixture.format) {
				t.Fatalf("fallback format missing: %d %s", w.Code, w.Body.String())
			}
			links := regexp.MustCompile("href=\"(/releases/[^\"]+)\"").FindAllStringSubmatch(w.Body.String(), -1)
			if len(links) != 1 || links[0][1] != "/releases/"+fixture.name {
				t.Fatalf("unexpected fallback links: %v", links)
			}
		})
	}
}

func TestDownloadsPageHeadReturnsHeadersWithoutBody(t *testing.T) {
	w := httptest.NewRecorder()
	NewHandler(t.TempDir()).ServeHTTP(w, httptest.NewRequest("HEAD", "/downloads", nil))
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("invalid HEAD response: %d %q %s", w.Code, w.Header(), w.Body.String())
	}
}
