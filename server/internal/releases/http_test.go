package releases

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadCatalogServesWindows7Installers(t *testing.T) {
	root, source := t.TempDir(), t.TempDir()
	for _, arch := range []string{"x86", "x86_64"} {
		name := "LinkNestX_0.3.0_windows7_" + arch + "-setup.exe"
		installer := filepath.Join(source, name)
		if err := os.WriteFile(installer, []byte("installer "+arch), 0600); err != nil {
			t.Fatal(err)
		}
		if err := AddDownload(root, installer, "windows7", arch, "0.3.0"); err != nil {
			t.Fatalf("add windows7/%s: %v", arch, err)
		}
	}
	server := NewHandler(root)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/releases", nil))
	var catalog Catalog
	if w.Code != 200 {
		t.Fatalf("catalog status: %d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Releases) != 2 {
		t.Fatalf("want both Win7 installers, got %+v", catalog.Releases)
	}
	for i, arch := range []string{"x86", "x86_64"} {
		d := catalog.Releases[i]
		if d.Platform != "windows7" || d.Arch != arch || d.Version != "0.3.0" {
			t.Fatalf("unexpected catalog entry: %+v", d)
		}
		w = httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest("GET", d.URL, nil))
		if w.Code != 200 || w.Body.String() != "installer "+arch {
			t.Fatalf("windows7/%s download: %d %q", arch, w.Code, w.Body.String())
		}
	}
}

func TestDownloadCatalogRejectsInvalidTargets(t *testing.T) {
	for _, target := range []struct{ platform, arch string }{
		{"windows7", "aarch64"},
		{"windows7", "x64"},
		{"windows7", ""},
		{"windows", "x86"},
		{"windows", "aarch64"},
		{"macos", "x86"},
		{"linux", "x86_64"},
	} {
		t.Run(target.platform+"/"+target.arch, func(t *testing.T) {
			root := t.TempDir()
			installer := filepath.Join(t.TempDir(), "installer.exe")
			if err := os.WriteFile(installer, []byte("installer"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := AddDownload(root, installer, target.platform, target.arch, "0.3.0"); !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("invalid target add: %v", err)
			}
			if err := AddDownload(root, installer, "windows", "x86_64", "0.3.0"); err != nil {
				t.Fatal(err)
			}
			catalog := readCatalog(root)
			catalog.Releases[0].Platform = target.platform
			catalog.Releases[0].Arch = target.arch
			raw, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, "downloads.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			server := NewHandler(root)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/releases", nil))
			if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"releases":[]}` {
				t.Fatalf("invalid target catalog: %d %s", w.Code, w.Body.String())
			}
			w = httptest.NewRecorder()
			server.ServeHTTP(w, httptest.NewRequest("GET", "/releases/installer.exe", nil))
			if w.Code != 404 {
				t.Fatalf("invalid target download: %d", w.Code)
			}
		})
	}
}

func TestDownloadPageExplainsWindowsInstallerRequirements(t *testing.T) {
	for _, target := range []struct{ platform, arch, label, requirement string }{
		{"windows", "x86_64", "Windows 10/11 · 64 位", "已包含 WebView2 离线安装包"},
		{"windows7", "x86", "Windows 7 SP1 · 32 位", "无需 WebView2"},
		{"windows7", "x86_64", "Windows 7 SP1 · 64 位", "无需 WebView2"},
	} {
		t.Run(target.platform+"/"+target.arch, func(t *testing.T) {
			root := t.TempDir()
			installer := filepath.Join(t.TempDir(), "installer.exe")
			if err := os.WriteFile(installer, []byte("installer"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := AddDownload(root, installer, target.platform, target.arch, "0.3.0"); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			NewHandler(root).ServeHTTP(w, httptest.NewRequest("GET", "/downloads", nil))
			if w.Code != 200 || !strings.Contains(w.Body.String(), target.label) || !strings.Contains(w.Body.String(), target.requirement) {
				t.Fatalf("missing target label or runtime guidance: %d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "Windows 需要 WebView2") {
				t.Fatal("page still claims every Windows installer needs WebView2")
			}
		})
	}
}

func TestWindows7InstallersDoNotEnableSignedUpdates(t *testing.T) {
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, output := t.TempDir(), t.TempDir()
	if err = os.WriteFile(filepath.Join(root, "LinkNestX.exe"), []byte("binary"), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	opts := Options{Root: root, Output: output, Platform: "windows", Arch: "x86_64", Version: "0.3.0", Sequence: 3, EntryPoint: "LinkNestX.exe", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PrivateKey: private}
	envelope, err := Publish(opts)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Verify(envelope, pub, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"x86", "x86_64"} {
		t.Run(arch, func(t *testing.T) {
			m := manifest
			m.Platform, m.Arch = "windows7", arch
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			signed := Envelope{KeyID: KeyID(pub), Manifest: base64.RawURLEncoding.EncodeToString(raw), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, append([]byte(SignatureDomain), raw...)))}
			if _, err = Verify(signed, pub, now); err == nil || !strings.Contains(err.Error(), "identity") {
				t.Fatalf("Win7 signed manifest must reject its target: %v", err)
			}
			path := filepath.Join(output, "windows7", arch, "latest.json")
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			NewHandler(output).ServeHTTP(w, httptest.NewRequest("GET", "/updates/windows7/"+arch+"/latest.json", nil))
			if w.Code != 404 {
				t.Fatalf("Win7 update route exposed: %d", w.Code)
			}
		})
	}
}
