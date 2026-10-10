package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"exmon/internal/signdoc"
)

func init() {
	// init functions run before main; registering here keeps each command's
	// table entry next to its code.
	commands["keys export"] = command{"write a Key Vault signing key's public key for building into exmon", keysExport}
	commands["keys dev-init"] = command{"create a local development signing key (for -tags exmondev builds)", keysDevInit}
}

// keysExport fetches a signing key's public half from Key Vault and writes it as
// PEM, ready to commit under internal/signdoc/trust/.
func keysExport(args []string) error {
	fs := flag.NewFlagSet("keys export", flag.ContinueOnError)
	vault := fs.String("vault", "", "Key Vault name or URL")
	key := fs.String("key", "", "key name, e.g. exmon-config-signing")
	out := fs.String("out", "", "output file; its name (without .pem) becomes the key id, e.g. internal/signdoc/trust/config/cfg-2026a.pem")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *vault == "" || *key == "" || *out == "" {
		return errors.New("--vault, --key and --out are required")
	}
	token, err := azToken("https://vault.azure.net")
	if err != nil {
		return err
	}
	k, pub, err := getVaultKey(*vault, *key, token)
	if err != nil {
		return err
	}
	pemBytes, err := signdoc.PublicKeyPEM(pub)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, pemBytes, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s (key id %q, Key Vault version %s)\n", *out, keyIDFromPath(*out), filepath.Base(k.Key.Kid))
	fmt.Println("commit it and build a new exmon release; only builds containing it accept documents it signs.")
	return nil
}

// keysDevInit makes a development signing key: the private key goes where you
// say (outside the repository), the public key into trust-dev/ for exmondev builds.
func keysDevInit(args []string) error {
	fs := flag.NewFlagSet("keys dev-init", flag.ContinueOnError)
	priv := fs.String("private", "", `where to keep the private key, e.g. %USERPROFILE%\.exmon\dev.pem (NOT inside the repository)`)
	repo := fs.String("repo", ".", "the exmon repository root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *priv == "" {
		return errors.New("--private is required")
	}
	if filepath.Base(*priv) != "dev.pem" {
		return errors.New("name the private key file dev.pem: its name is the key id devices look for")
	}
	if _, err := os.Stat(*priv); err == nil {
		return fmt.Errorf("%s already exists; delete it first to make a new key", *priv)
	}
	privPEM, pubPEM, err := signdoc.NewDevKey()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*priv), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(*priv, privPEM, 0o600); err != nil {
		return err
	}
	for _, purpose := range []string{"config", "release"} {
		p := filepath.Join(*repo, "internal", "signdoc", "trust-dev", purpose, "dev.pem")
		if err := os.WriteFile(p, pubPEM, 0o644); err != nil {
			return fmt.Errorf("%w (run this from the repository root, or pass --repo)", err)
		}
	}
	fmt.Printf("wrote the private key to %s and the public key to internal/signdoc/trust-dev/{config,release}/dev.pem\n", *priv)
	fmt.Println("build test exes with:  go build -tags exmondev .   and sign with  --key-file", *priv)
	return nil
}

func keyIDFromPath(p string) string {
	b := filepath.Base(p)
	return b[:len(b)-len(filepath.Ext(b))]
}
