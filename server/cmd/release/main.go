// release is an offline tool. It never uploads artifacts or contacts a server.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/linknestx/linknestx/server/internal/releases"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: release keygen | publish | add-download | verify")
	}
	switch args[0] {
	case "keygen":
		f := flag.NewFlagSet("keygen", flag.ContinueOnError)
		private := f.String("private", "", "new private key PEM path (outside tracked files)")
		public := f.String("public", "", "new public key JSON path")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *private == "" || *public == "" || *private == *public {
			return errors.New("distinct private/public paths required")
		}
		for _, p := range []string{*private, *public} {
			if _, err := os.Lstat(p); !os.IsNotExist(err) {
				return errors.New("key output already exists or cannot be accessed")
			}
		}
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(*private), 0700); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(*public), 0755); err != nil {
			return err
		}
		if err = createExclusive(*private, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0600); err != nil {
			return err
		}
		metadata, _ := json.MarshalIndent(map[string]string{"keyId": releases.KeyID(pub), "publicKey": base64.RawURLEncoding.EncodeToString(pub)}, "", "  ")
		if err = createExclusive(*public, metadata, 0644); err != nil {
			return err
		}
		fmt.Println("Created release signing key; private key was not printed.")
		return nil
	case "publish":
		f := flag.NewFlagSet("publish", flag.ContinueOnError)
		var o releases.Options
		f.StringVar(&o.Root, "root", "", "application payload directory")
		f.StringVar(&o.BaseRoot, "base-root", "", "optional previous payload directory")
		f.StringVar(&o.Output, "output", "", "new release directory")
		f.StringVar(&o.Platform, "platform", "", "windows or macos")
		f.StringVar(&o.Arch, "arch", "", "x86_64 or aarch64")
		f.StringVar(&o.Version, "version", "", "release version")
		f.StringVar(&o.EntryPoint, "entry-point", "", "relative application executable")
		f.Uint64Var(&o.Sequence, "sequence", 0, "monotonically increasing build sequence")
		keyPath := f.String("private", "", "offline PEM signing key")
		days := f.Int("valid-days", 90, "manifest validity,1..366days")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if o.Root == "" || o.Output == "" || *days < 1 || *days > 366 {
			return os.ErrInvalid
		}
		key, err := readPrivate(*keyPath)
		if err != nil {
			return err
		}
		o.PrivateKey = key
		o.IssuedAt = time.Now().UTC()
		o.ExpiresAt = o.IssuedAt.Add(time.Duration(*days) * 24 * time.Hour)
		envelope, err := releases.Publish(o)
		if err != nil {
			return err
		}
		m, err := releases.Verify(envelope, key.Public().(ed25519.PublicKey), o.IssuedAt)
		if err != nil {
			return err
		}
		var reused int64
		for _, file := range m.Files {
			for _, block := range file.Blocks {
				if block.BasePath != "" {
					reused += block.Size
				}
			}
		}
		fmt.Printf("Published locally: %s %s %s; %d files, %d bytes, %d reusable bytes.\n", m.Version, m.Platform, m.Arch, len(m.Files), m.TotalBytes, reused)
		return nil
	case "add-download":
		f := flag.NewFlagSet("add-download", flag.ContinueOnError)
		root := f.String("output", "", "release directory")
		source := f.String("file", "", "installer/archive file")
		platform := f.String("platform", "", "platform")
		arch := f.String("arch", "", "architecture")
		version := f.String("version", "", "version")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *source == "" {
			return os.ErrInvalid
		}
		return releases.AddDownload(*root, *source, *platform, *arch, *version)
	case "verify":
		f := flag.NewFlagSet("verify", flag.ContinueOnError)
		manifest := f.String("manifest", "", "signed latest.json")
		pub := f.String("public", "", "public key JSON")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		raw, err := os.ReadFile(*manifest)
		if err != nil {
			return err
		}
		var envelope releases.Envelope
		if err = json.Unmarshal(raw, &envelope); err != nil {
			return err
		}
		raw, err = os.ReadFile(*pub)
		if err != nil {
			return err
		}
		var metadata struct {
			PublicKey string `json:"publicKey"`
		}
		if err = json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
		key, err := base64.RawURLEncoding.DecodeString(metadata.PublicKey)
		if err != nil {
			return err
		}
		m, err := releases.Verify(envelope, ed25519.PublicKey(key), time.Now().UTC())
		if err != nil {
			return err
		}
		fmt.Printf("Verified %s %s/%s build %d\n", m.Version, m.Platform, m.Arch, m.BuildSequence)
		return nil
	default:
		return errors.New("unknown release operation")
	}
}
func readPrivate(name string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private key must have owner-only permissions")
	}
	raw, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, os.ErrInvalid
	}
	value, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := value.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("Ed25519 key required")
	}
	return key, nil
}
func createExclusive(name string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	return closed
}
