package localstt

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// extractServer pulls the one file Vito needs — parakeet-server, or
// parakeet-server.exe — out of a release archive and writes it, executable,
// to dst. The CLI and the README that ship beside it are left in the archive.
func extractServer(archive, dst string) error {
	want := func(name string) bool {
		base := path.Base(strings.ReplaceAll(name, "\\", "/"))
		return base == "parakeet-server" || base == "parakeet-server.exe"
	}
	var src io.ReadCloser
	var err error
	switch {
	case strings.HasSuffix(archive, ".zip"):
		src, err = openZipEntry(archive, want)
	case strings.HasSuffix(archive, ".tar.gz"), strings.HasSuffix(archive, ".tgz"):
		src, err = openTarEntry(archive, want)
	default:
		return fmt.Errorf("unknown archive type: %s", filepath.Base(archive))
	}
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}

var errNoServer = errors.New("parakeet-server not found in archive")

func openZipEntry(archive string, want func(string) bool) (io.ReadCloser, error) {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	for _, f := range r.File {
		if want(f.Name) && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				r.Close()
				return nil, err
			}
			return &closeBoth{rc, r}, nil
		}
	}
	r.Close()
	return nil, errNoServer
}

func openTarEntry(archive string, want func(string) bool) (io.ReadCloser, error) {
	f, err := os.Open(archive)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.Close()
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && want(h.Name) {
			return &closeBoth{io.NopCloser(tr), f}, nil
		}
	}
	f.Close()
	return nil, errNoServer
}

// closeBoth closes the entry and the archive it came from.
type closeBoth struct {
	io.ReadCloser
	outer io.Closer
}

func (c *closeBoth) Close() error {
	err := c.ReadCloser.Close()
	if e := c.outer.Close(); err == nil {
		err = e
	}
	return err
}
