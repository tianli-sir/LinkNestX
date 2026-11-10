package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineKeygenPublishVerifyAndRefuseOverwrite(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "keys", "private.pem")
	public := filepath.Join(root, "public.json")
	if err := run([]string{"keygen", "--private", private, "--public", public}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(private)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(private)
	if info.Mode().Perm() != 0600 {
		t.Fatal("private key permissions")
	}
	if err := run([]string{"keygen", "--private", private, "--public", public}); err == nil {
		t.Fatal("key overwritten")
	}
	after, _ := os.ReadFile(private)
	if string(after) != string(before) {
		t.Fatal("key changed")
	}
	metadata, _ := os.ReadFile(public)
	var fields map[string]string
	if json.Unmarshal(metadata, &fields) != nil || len(fields) != 2 || fields["publicKey"] == "" {
		t.Fatal("public trust resource invalid")
	}
	payload := filepath.Join(root, "payload")
	os.Mkdir(payload, 0700)
	os.WriteFile(filepath.Join(payload, "LinkNestX.exe"), []byte("fixture executable"), 0700)
	out := filepath.Join(root, "releases")
	if err := run([]string{"publish", "--root", payload, "--output", out, "--platform", "windows", "--arch", "x86_64", "--version", "0.2.1", "--sequence", "2", "--entry-point", "LinkNestX.exe", "--private", private}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", "--manifest", filepath.Join(out, "windows", "x86_64", "latest.json"), "--public", public}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(private, 0644)
	if _, err := readPrivate(private); err == nil {
		t.Fatal("read publicly accessible private key")
	}
}
