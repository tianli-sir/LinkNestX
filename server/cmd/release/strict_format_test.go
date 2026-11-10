package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewVerifierRejectsMalformedEnvelope(t *testing.T) {
	root := t.TempDir()
	private, public := filepath.Join(root, "private.pem"), filepath.Join(root, "public.json")
	if err := run([]string{"keygen", "--private", private, "--public", public}); err != nil {
		t.Fatal(err)
	}
	payload, output := filepath.Join(root, "payload"), filepath.Join(root, "releases")
	os.Mkdir(payload, 0700)
	os.WriteFile(filepath.Join(payload, "LinkNestX.exe"), []byte("fixture"), 0700)
	if err := run([]string{"publish", "--root", payload, "--output", output, "--platform", "windows", "--arch", "x86_64", "--version", "0.2.1", "--sequence", "2", "--entry-point", "LinkNestX.exe", "--private", private}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(output, "windows", "x86_64", "latest.json")
	raw, _ := os.ReadFile(manifest)
	var fields map[string]string
	json.Unmarshal(raw, &fields)
	for _, prefix := range []string{`"unexpected":true,`, `"keyId":"wrong",`} {
		mutated := strings.Replace(string(raw), "{", "{"+prefix, 1)
		os.WriteFile(manifest, []byte(mutated), 0600)
		if err := run([]string{"verify", "--manifest", manifest, "--public", public}); err == nil {
			t.Errorf("verified envelope with forbidden prefix %s", prefix)
		}
	}
	for _, version := range []string{"01.2.3", "0.2.1-..", "0.2.1-01"} {
		if err := run([]string{"publish", "--root", payload, "--output", output, "--platform", "windows", "--arch", "x86_64", "--version", version, "--sequence", "3", "--entry-point", "LinkNestX.exe", "--private", private}); err == nil {
			t.Errorf("published invalid semver %s", version)
		}
	}
}
