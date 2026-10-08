// Command uibundle builds and signs the web interface that daemons download
// from vito.talk/ui/ (see internal/uibundle).
//
//	go run ./packaging/uibundle keygen              # once per publishing machine
//	go run ./packaging/uibundle build -out <dir>    # writes ui.json, .sig and the zip
//
// The bundle is web.Files as compiled into this tool, so it is exactly the tree
// the daemon embeds. The private key is read from -key, else VITO_UI_KEY, else
// ~/.vito-signing/ui-ed25519.key — never from the repository.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vito/internal/uibundle"
	"vito/web"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2:])
	case "build":
		err = build(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "uibundle:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: uibundle keygen [-key path] | build -out dir [-version v] [-min-api n] [-key path]")
	os.Exit(2)
}

func defaultKey() string {
	if k := os.Getenv("VITO_UI_KEY"); k != "" {
		return k
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vito-signing", "ui-ed25519.key")
}

func keygen(args []string) error {
	fl := flag.NewFlagSet("keygen", flag.ExitOnError)
	keyPath := fl.String("key", defaultKey(), "where to write the private key")
	_ = fl.Parse(args)
	if _, err := os.Stat(*keyPath); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite a signing key", *keyPath)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*keyPath, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Println("private key:", *keyPath)
	fmt.Println("public key (put in internal/uibundle PublicKey):", base64.StdEncoding.EncodeToString(pub))
	return nil
}

func build(args []string) error {
	fl := flag.NewFlagSet("build", flag.ExitOnError)
	out := fl.String("out", "", "directory to write ui.json, ui.json.sig and the zip into")
	version := fl.String("version", time.Now().UTC().Format("2006.01.02-1504"), "bundle version")
	minAPI := fl.Int("min-api", uibundle.API, "lowest daemon API level this interface works with")
	keyPath := fl.String("key", defaultKey(), "private key")
	_ = fl.Parse(args)
	if *out == "" {
		usage()
	}

	seed, err := os.ReadFile(*keyPath)
	if err != nil {
		return fmt.Errorf("read key: %w (run keygen first)", err)
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seed)))
	if err != nil || len(s) != ed25519.SeedSize {
		return fmt.Errorf("%s is not an ed25519 seed", *keyPath)
	}
	priv := ed25519.NewKeyFromSeed(s)
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), uibundle.PublicKey) {
		return fmt.Errorf("%s does not match the public key daemons trust", *keyPath)
	}

	// Zip the embedded tree. Store already-compressed formats as they are.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err = fs.WalkDir(web.Files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := web.Files.ReadFile(p)
		if err != nil {
			return err
		}
		method := zip.Deflate
		switch filepath.Ext(p) {
		case ".png", ".woff2":
			method = zip.Store
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p, Method: method, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	})
	if err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	sum := sha256.Sum256(buf.Bytes())
	man := uibundle.Manifest{
		Version: *version,
		API:     uibundle.API,
		MinAPI:  *minAPI,
		File:    "vito-ui-" + *version + ".zip",
		Size:    int64(buf.Len()),
		SHA256:  hex.EncodeToString(sum[:]),
		Built:   time.Now().UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw)) + "\n"

	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	// Older zips are removed so the published folder holds one bundle.
	old, _ := filepath.Glob(filepath.Join(*out, "vito-ui-*.zip"))
	for _, f := range old {
		_ = os.Remove(f)
	}
	if err := os.WriteFile(filepath.Join(*out, man.File), buf.Bytes(), 0o644); err != nil {
		return err
	}
	// The zip first, the manifest last: a daemon never sees a ui.json whose
	// file is not there yet.
	if err := os.WriteFile(filepath.Join(*out, "ui.json.sig"), []byte(sig), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "ui.json"), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("%s  %s  %d bytes  api %d (min %d)\n", man.Version, man.File, man.Size, man.API, man.MinAPI)
	return nil
}
