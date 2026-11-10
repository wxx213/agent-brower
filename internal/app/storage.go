package app

import (
	"agent-web/internal/assets"
	"archive/zip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Version is set by the release build via -ldflags -X.
var Version = "dev"

func secret() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func dataHome() string {
	if s := os.Getenv("AGENT_WEB_HOME"); s != "" {
		p, _ := filepath.Abs(s)
		return p
	}
	h, _ := os.UserConfigDir()
	return filepath.Join(h, "agent-web")
}
func atomicJSON(path string, value any) error {
	b, e := json.MarshalIndent(value, "", "  ")
	if e != nil {
		return e
	}
	return atomicFile(path, b, 0600)
}
func atomicFile(path string, b []byte, mode os.FileMode) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".write-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func browserPath(home, override string) (string, error) {
	if override == "" {
		override = os.Getenv("AGENT_WEB_BROWSER")
	}
	if override != "" {
		p, e := filepath.Abs(override)
		if e != nil {
			return "", e
		}
		st, e := os.Stat(p)
		if e != nil {
			return "", e
		}
		if st.IsDir() {
			return "", fmt.Errorf("browser must be an executable")
		}
		return p, nil
	}
	archive, e := assets.Files.Open("browser/browser.zip")
	if e != nil {
		return "", fmt.Errorf("development binary has no embedded browser; set AGENT_WEB_BROWSER or build a standalone release")
	}
	defer archive.Close()
	hasher := sha256.New()
	if _, e = io.Copy(hasher, archive); e != nil {
		return "", e
	}
	hash := hasher.Sum(nil)
	root := filepath.Join(home, "runtime", hex.EncodeToString(hash[:8]))
	marker := filepath.Join(root, ".executable")
	if b, e := os.ReadFile(marker); e == nil {
		p := filepath.Join(root, string(b))
		if _, e = os.Stat(p); e == nil {
			return p, nil
		}
	}
	if e = os.MkdirAll(filepath.Dir(root), 0700); e != nil {
		return "", e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(root), ".extract-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(tmp)
	stat, e := archive.Stat()
	if e != nil {
		return "", e
	}
	reader, ok := archive.(io.ReaderAt)
	if !ok {
		return "", fmt.Errorf("embedded archive does not support random access")
	}
	r, e := zip.NewReader(reader, stat.Size())
	if e != nil {
		return "", e
	}
	var executable string
	for _, f := range r.File {
		name := filepath.FromSlash(f.Name)
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("invalid archive path")
		}
		p := filepath.Join(tmp, clean)
		if f.FileInfo().IsDir() {
			e = os.MkdirAll(p, 0700)
		} else {
			if f.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("browser archive contains unsupported symlink")
			}
			if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
				return "", e
			}
			src, err := f.Open()
			if err != nil {
				return "", err
			}
			mode := os.FileMode(0600)
			if f.Mode()&0111 != 0 {
				mode = 0700
			}
			if filepath.Base(p) == "chrome-headless-shell" {
				mode = 0700
				executable = clean
			}
			dst, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				src.Close()
				return "", err
			}
			_, e = io.Copy(dst, src)
			src.Close()
			ce := dst.Close()
			if e == nil {
				e = ce
			}
		}
		if e != nil {
			return "", e
		}
	}
	if executable == "" {
		return "", fmt.Errorf("archive has no chrome-headless-shell")
	}
	if e = os.WriteFile(filepath.Join(tmp, ".executable"), []byte(executable), 0600); e != nil {
		return "", e
	}
	if e = os.Rename(tmp, root); e != nil {
		return "", e
	}
	return filepath.Join(root, executable), nil
}
