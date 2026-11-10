package releases

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseVersionCompatibility(t *testing.T) {
	for _, version := range []string{"0.0.0", "0.2.1", "1.0.0-rc.1", "1.0.0-0alpha", "1.0.0+build.01", "18446744073709551615.0.0"} {
		if !validVersion(version) {
			t.Errorf("valid SemVer rejected: %s", version)
		}
	}
	for _, version := range []string{"01.2.3", "1.02.3", "1.2.03", "0.2.1-..", "0.2.1-01", "1.0.0-rc..1", "1.0.0+", "1.0.0+build..1", "18446744073709551616.0.0", "v1.0.0"} {
		if validVersion(version) {
			t.Errorf("invalid native version accepted: %s", version)
		}
	}
}

func TestEnvelopeStrictWireFields(t *testing.T) {
	for _, raw := range []string{
		`{"keyId":"k","manifest":"m","signature":"s","KeyId":"k"}`,
		`{"KeyId":"k","manifest":"m","signature":"s"}`,
		`{"keyId":"k","manifest":"m","signature":null}`,
		`{"keyId":"k","manifest":"m","signature":"s","signature":"s"}`,
		`{"keyId":"k","manifest":"m","signature":"s"} {}`,
	} {
		var envelope Envelope
		if json.Unmarshal([]byte(raw), &envelope) == nil {
			t.Errorf("ambiguous envelope accepted: %s", raw)
		}
	}
}

func TestPublishSignedDeltaReconstructsAndReusesRealBytes(t *testing.T) {
	pub, private, _ := ed25519.GenerateKey(rand.Reader)
	base, target, output := t.TempDir(), t.TempDir(), t.TempDir()
	old := append([]byte(strings.Repeat("a", BlockSize)), []byte(strings.Repeat("b", BlockSize))...)
	want := append(append([]byte{}, old[:BlockSize]...), []byte("changed suffix")...)
	if err := os.WriteFile(filepath.Join(base, "LinkNestX.exe"), old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "LinkNestX.exe"), want, 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	options := Options{Root: target, BaseRoot: base, Output: output, Platform: "windows", Arch: "x86_64", Version: "0.2.1", Sequence: 2, EntryPoint: "LinkNestX.exe", IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour), PrivateKey: private}
	envelope, err := Publish(options)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Verify(envelope, pub, now)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.AppID != "com.linknestx.client" || len(manifest.Files) != 1 || manifest.Files[0].SHA256 != Hash(want) {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	var reconstructed []byte
	var reused, downloaded int64
	for _, block := range manifest.Files[0].Blocks {
		var data []byte
		if block.BasePath != "" {
			data = old[block.BaseOffset : block.BaseOffset+block.Size]
			reused += block.Size
		} else {
			data, err = os.ReadFile(filepath.Join(output, "objects", block.SHA256))
			if err != nil {
				t.Fatal(err)
			}
			downloaded += int64(len(data))
		}
		if Hash(data) != block.SHA256 {
			t.Fatal("block hash mismatch")
		}
		reconstructed = append(reconstructed, data...)
	}
	if string(reconstructed) != string(want) || reused == 0 || downloaded >= int64(len(want)) {
		t.Fatalf("invalid delta reused=%d downloaded=%d", reused, downloaded)
	}
	full, err := os.ReadFile(filepath.Join(output, "objects", manifest.Files[0].SHA256))
	if err != nil || string(full) != string(want) {
		t.Fatal("missing identical full fallback", err)
	}
	stored, err := os.ReadFile(filepath.Join(output, "windows", "x86_64", "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded Envelope
	if err = json.Unmarshal(stored, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(decoded, pub, now); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRejectsTamperExpiryAndUnsafeTree(t *testing.T) {
	pub, private, _ := ed25519.GenerateKey(rand.Reader)
	root, out := t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(root, "app"), []byte("binary"), 0700)
	now := time.Now().UTC()
	opts := Options{Root: root, Output: out, Platform: "macos", Arch: "aarch64", Version: "0.2.1", Sequence: 2, EntryPoint: "app", IssuedAt: now, ExpiresAt: now.Add(time.Hour), PrivateKey: private}
	env, err := Publish(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Verify(env, pub, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired manifest accepted")
	}
	env.Manifest = env.Manifest[:len(env.Manifest)-1] + "A"
	if _, err = Verify(env, pub, now); err == nil {
		t.Fatal("tamper accepted")
	}
	if err = os.Symlink("/etc/passwd", filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err = Publish(opts); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestSignedManifestRejectsDuplicateFields(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	raw := []byte(`{"schemaVersion":1,"schemaVersion":1}`)
	env := Envelope{KeyID: KeyID(pub), Manifest: base64.RawURLEncoding.EncodeToString(raw), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, append([]byte(SignatureDomain), raw...)))}
	if _, err := Verify(env, pub, time.Now()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected explicit duplicate rejection, got %v", err)
	}
}

func TestReleasePathsRejectWindowsAliasesAndControls(t *testing.T) {
	for _, name := range []string{"../bad", "C:/bad", "a\\b", "CON", "con.txt", "dir/LPT1.txt", "bad\r\nname", "file?name", "file:name", "name.", "name ", "a//b"} {
		if safePath(name) {
			t.Errorf("unsafe cross-platform path accepted: %q", name)
		}
	}
	for _, name := range []string{"Contents/MacOS/linknestx", "LinkNestX.exe", "Contents/Resources/icon.icns"} {
		if !safePath(name) {
			t.Errorf("valid path rejected: %q", name)
		}
	}
}

func TestPublicReleaseHandlerDoesNotExposeUnpublishedOrEscape(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "secret.txt"), []byte("private"), 0600)
	server := NewHandler(root)
	for _, path := range []string{"/updates/../secret.txt", "/updates/objects/../../secret.txt", "/releases/secret.txt", "/api/v1/unknown"} {
		w := httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/releases", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"releases":[]`) {
		t.Fatalf("unpublished catalog: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", "/downloads", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "暂未提供") {
		t.Fatalf("unpublished page %d", w.Code)
	}
	installer := filepath.Join(t.TempDir(), "LinkNestX_0.2.0_x64-setup.exe")
	os.WriteFile(installer, []byte("fixture installer"), 0600)
	if err := AddDownload(root, installer, "windows", "x86_64", "0.2.0"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/releases", nil))
	if !strings.Contains(w.Body.String(), "0.2.0") {
		t.Fatal(w.Body.String())
	}
	os.WriteFile(filepath.Join(root, "downloads", filepath.Base(installer)), []byte("corrupt"), 0600)
	w = httptest.NewRecorder()
	server.ServeHTTP(w, httptest.NewRequest("GET", "/releases/"+filepath.Base(installer), nil))
	if w.Code != 404 {
		t.Fatal("corrupt artifact served")
	}
}
