// Package releases creates signed, content-addressed application releases.
// Signing happens offline; the running collaboration server needs no private key.
package releases

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const BlockSize = 262144
const MaxTotalSize int64 = 2 << 30
const SignatureDomain = "LinkNestX-update-manifest-v1\x00"

type Block struct {
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	BasePath   string `json:"basePath,omitempty"`
	BaseOffset int64  `json:"baseOffset,omitempty"`
}
type File struct {
	Path   string  `json:"path"`
	Mode   uint32  `json:"mode"`
	Size   int64   `json:"size"`
	SHA256 string  `json:"sha256"`
	Blocks []Block `json:"blocks"`
}
type Manifest struct {
	SchemaVersion int       `json:"schemaVersion"`
	AppID         string    `json:"appId"`
	Channel       string    `json:"channel"`
	Platform      string    `json:"platform"`
	Arch          string    `json:"arch"`
	Version       string    `json:"version"`
	BuildSequence uint64    `json:"buildSequence"`
	IssuedAt      time.Time `json:"issuedAt"`
	ExpiresAt     time.Time `json:"expiresAt"`
	EntryPoint    string    `json:"entryPoint"`
	TotalBytes    int64     `json:"totalBytes"`
	Files         []File    `json:"files"`
}

// Manifest is base64url without padding of exact JSON bytes. Verify before parsing.
type Envelope struct {
	KeyID     string `json:"keyId"`
	Manifest  string `json:"manifest"`
	Signature string `json:"signature"`
}

// UnmarshalJSON keeps every consumer of the envelope on the same strict wire
// format as the native verifier, including the offline verification command.
func (e *Envelope) UnmarshalJSON(raw []byte) error {
	if len(raw) > (8<<20)+1024 {
		return errors.New("oversized release envelope")
	}
	if err := uniqueJSON(raw); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if len(fields) != 3 {
		return errors.New("invalid release envelope fields")
	}
	for _, name := range []string{"keyId", "manifest", "signature"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("missing release envelope field")
		}
	}
	type plainEnvelope Envelope
	var parsed plainEnvelope
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*e = Envelope(parsed)
	return nil
}

type Options struct {
	Root, BaseRoot, Output, Platform, Arch, Version, EntryPoint string
	Sequence                                                    uint64
	IssuedAt, ExpiresAt                                         time.Time
	PrivateKey                                                  ed25519.PrivateKey
}

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func validVersion(value string) bool {
	parts := versionPattern.FindStringSubmatch(value)
	if parts == nil {
		return false
	}
	// Rust's semver crate represents the three core components as u64.
	for _, part := range parts[1:4] {
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func Hash(data []byte) string            { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
func KeyID(key ed25519.PublicKey) string { return Hash(key)[:16] }
func safePath(value string) bool {
	if value == "" || len(value) > 1024 || path.Clean(value) != value || strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\:\x00<>\"|?*") {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." || part == "" || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return false
		}
	}
	return true
}
func validTarget(platform, arch string) bool {
	return (platform == "windows" && arch == "x86_64") || (platform == "macos" && (arch == "aarch64" || arch == "x86_64"))
}

func Validate(m Manifest, now time.Time) error {
	if m.SchemaVersion != 1 || m.AppID != "com.linknestx.client" || m.Channel != "stable" || !validTarget(m.Platform, m.Arch) || !validVersion(m.Version) || m.BuildSequence == 0 {
		return errors.New("invalid release identity")
	}
	if m.IssuedAt.After(now.Add(5*time.Minute)) || !m.ExpiresAt.After(now) || !m.ExpiresAt.After(m.IssuedAt) || m.ExpiresAt.Sub(m.IssuedAt) > 366*24*time.Hour {
		return errors.New("invalid release lifetime")
	}
	if !safePath(m.EntryPoint) || len(m.Files) == 0 || len(m.Files) > 4096 || m.TotalBytes < 0 || m.TotalBytes > MaxTotalSize {
		return errors.New("invalid release tree")
	}
	seen := map[string]bool{}
	var total int64
	entry := false
	blocks := 0
	for _, file := range m.Files {
		key := strings.ToLower(file.Path)
		if !safePath(file.Path) || seen[key] || file.Size < 0 || file.Size > MaxTotalSize || !hashPattern.MatchString(file.SHA256) || (file.Mode != 0644 && file.Mode != 0755) {
			return errors.New("invalid release file")
		}
		for other := range seen {
			if strings.HasPrefix(key, other+"/") || strings.HasPrefix(other, key+"/") {
				return errors.New("file and directory collision")
			}
		}
		seen[key] = true
		entry = entry || file.Path == m.EntryPoint
		total += file.Size
		var length int64
		for i, b := range file.Blocks {
			blocks++
			if blocks > 16384 || !hashPattern.MatchString(b.SHA256) || b.Size <= 0 || b.Size > BlockSize || (i < len(file.Blocks)-1 && b.Size != BlockSize) || b.BaseOffset < 0 || b.BaseOffset > MaxTotalSize-b.Size || (b.BasePath != "" && !safePath(b.BasePath)) || (b.BasePath == "" && b.BaseOffset != 0) {
				return errors.New("invalid release block")
			}
			length += b.Size
		}
		if length != file.Size {
			return errors.New("release block lengths differ")
		}
	}
	if !entry || total != m.TotalBytes || total > MaxTotalSize {
		return errors.New("release total differs")
	}
	return nil
}

func Verify(envelope Envelope, key ed25519.PublicKey, now time.Time) (Manifest, error) {
	var m Manifest
	if len(key) != ed25519.PublicKeySize || envelope.KeyID != KeyID(key) || len(envelope.Manifest) > 8<<20 {
		return m, errors.New("unknown or oversized release key/manifest")
	}
	raw, err := base64.RawURLEncoding.DecodeString(envelope.Manifest)
	if err != nil {
		return m, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || !ed25519.Verify(key, append([]byte(SignatureDomain), raw...), signature) {
		return m, errors.New("release signature mismatch")
	}
	if err = uniqueJSON(raw); err != nil {
		return m, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return m, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return m, errors.New("trailing manifest data")
	}
	return m, Validate(m, now)
}

func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 24 {
			return errors.New("manifest nested too deeply")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate manifest field")
				}
				seen[name] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing manifest data")
	}
	return nil
}

func regularTree(root string) ([]string, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("release root must be a real directory")
	}
	var names []string
	var total int64
	err = filepath.WalkDir(root, func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("release trees cannot contain symbolic links")
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("nonregular release input")
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !safePath(relative) {
			return errors.New("unsafe release path")
		}
		total += info.Size()
		if total > MaxTotalSize || len(names) >= 4096 {
			return errors.New("release exceeds limits")
		}
		names = append(names, relative)
		return nil
	})
	sort.Strings(names)
	return names, err
}

func writeAtomic(name string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".release-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temp, name)
}

func Publish(o Options) (Envelope, error) {
	var envelope Envelope
	if len(o.PrivateKey) != ed25519.PrivateKeySize {
		return envelope, errors.New("invalid private key")
	}
	root, err := filepath.Abs(o.Root)
	if err != nil {
		return envelope, err
	}
	out, err := filepath.Abs(o.Output)
	if err != nil {
		return envelope, err
	}
	if out == root || strings.HasPrefix(out, root+string(os.PathSeparator)) {
		return envelope, errors.New("output must be outside application root")
	}
	names, err := regularTree(root)
	if err != nil {
		return envelope, err
	}
	reuse := map[string]Block{}
	if o.BaseRoot != "" {
		baseNames, err := regularTree(o.BaseRoot)
		if err != nil {
			return envelope, err
		}
		for _, name := range baseNames {
			f, err := os.Open(filepath.Join(o.BaseRoot, filepath.FromSlash(name)))
			if err != nil {
				return envelope, err
			}
			buffer := make([]byte, BlockSize)
			var offset int64
			for {
				n, e := io.ReadFull(f, buffer)
				if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
					f.Close()
					return envelope, e
				}
				if n == 0 {
					break
				}
				hash := Hash(buffer[:n])
				if _, ok := reuse[hash]; !ok {
					reuse[hash] = Block{SHA256: hash, Size: int64(n), BasePath: name, BaseOffset: offset}
				}
				offset += int64(n)
				if e != nil {
					break
				}
			}
			f.Close()
		}
	}
	m := Manifest{SchemaVersion: 1, AppID: "com.linknestx.client", Channel: "stable", Platform: o.Platform, Arch: o.Arch, Version: o.Version, BuildSequence: o.Sequence, IssuedAt: o.IssuedAt.UTC(), ExpiresAt: o.ExpiresAt.UTC(), EntryPoint: o.EntryPoint, Files: []File{}}
	for _, name := range names {
		full := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Stat(full)
		if err != nil {
			return envelope, err
		}
		mode := uint32(0644)
		if info.Mode()&0111 != 0 {
			mode = 0755
		}
		input, err := os.Open(full)
		if err != nil {
			return envelope, err
		}
		hash := sha256.New()
		record := File{Path: name, Mode: mode, Size: info.Size(), Blocks: []Block{}}
		buffer := make([]byte, BlockSize)
		for {
			n, e := io.ReadFull(input, buffer)
			if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
				input.Close()
				return envelope, e
			}
			if n == 0 {
				break
			}
			data := buffer[:n]
			hash.Write(data)
			block := Block{SHA256: Hash(data), Size: int64(n)}
			if prior, ok := reuse[block.SHA256]; ok && prior.Size == block.Size {
				block.BasePath = prior.BasePath
				block.BaseOffset = prior.BaseOffset
			}
			record.Blocks = append(record.Blocks, block)
			if err = writeAtomic(filepath.Join(out, "objects", block.SHA256), data, 0600); err != nil {
				input.Close()
				return envelope, err
			}
			if e != nil {
				break
			}
		}
		input.Close()
		record.SHA256 = hex.EncodeToString(hash.Sum(nil))
		m.TotalBytes += record.Size
		m.Files = append(m.Files, record)
		if err = copyObject(full, filepath.Join(out, "objects", record.SHA256), record.SHA256, record.Size); err != nil {
			return envelope, err
		}
	}
	if err = Validate(m, o.IssuedAt); err != nil {
		return envelope, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return envelope, err
	}
	public := o.PrivateKey.Public().(ed25519.PublicKey)
	envelope = Envelope{KeyID: KeyID(public), Manifest: base64.RawURLEncoding.EncodeToString(raw), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(o.PrivateKey, append([]byte(SignatureDomain), raw...)))}
	encoded, _ := json.MarshalIndent(envelope, "", "  ")
	err = writeAtomic(filepath.Join(out, o.Platform, o.Arch, "latest.json"), encoded, 0600)
	return envelope, err
}

func copyObject(source, destination, expected string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(destination), ".object-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, size+1))
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("input changed while publishing %s", filepath.Base(source))
	}
	return os.Rename(out.Name(), destination)
}
