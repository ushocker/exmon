package main

import (
	"crypto/x509"
	"flag"
	"fmt"
	"os"

	"exmon/internal/enroll"
	"exmon/internal/winsys"
)

// caInit turns the key ykman generated in the YubiKey's slot 9c into a proper CA.
//
// `ykman piv certificates generate` makes a self-signed placeholder certificate,
// which Windows needs so it can find the key, but it may not be marked as a CA.
// This command finds that placeholder (through the smart-card key storage provider
// Windows ships), has the YubiKey sign a real CA certificate for the same key
// (CA:true, path length 0, certificate signing only), and writes it out. You then
// put it back in slot 9c with `ykman piv certificates import 9c ca.pem`.
func caInit(args []string) error {
	fs := flag.NewFlagSet("ca init", flag.ContinueOnError)
	cn := fs.String("cn", enroll.DefaultCAName, "common name of the placeholder certificate in slot 9c (and of the new CA)")
	years := fs.Int("years", 5, "CA lifetime in years (1-10)")
	out := fs.String("out", "ca.pem", "where to write the CA certificate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	placeholder, err := winsys.FindClientCert(winsys.CurrentUser, "My", func(c *x509.Certificate) bool {
		return c.Subject.CommonName == *cn
	}, true)
	if err != nil {
		return fmt.Errorf("no certificate %q with a usable key (is the YubiKey plugged in, with the slot 9c placeholder from the setup guide?): %w", *cn, err)
	}
	defer placeholder.Close()
	pin, err := winsys.ReadSecret("YubiKey PIV PIN: ")
	if err != nil {
		return err
	}
	placeholder.Signer.SetPIN(pin)
	fmt.Println("signing: touch the YubiKey when it blinks")
	der, err := enroll.MakeCA(placeholder.Signer, *cn, *years)
	if err != nil {
		return fmt.Errorf("sign the CA certificate: %w", err)
	}
	if err := os.WriteFile(*out, enroll.PEMCert(der), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s. Next:\n  ykman piv certificates import 9c %s\n", *out, *out)
	fmt.Println("then add it to the dev Event Grid namespace (EXMON_CA_PEM; docs/deploy/cloud-shell.md step 12).")
	return nil
}

// caSignCSR signs a test device's certificate request with the YubiKey CA, for
// when the YubiKey stays on the admin's machine instead of visiting the test PC.
func caSignCSR(args []string) error {
	fs := flag.NewFlagSet("ca sign-csr", flag.ContinueOnError)
	csrPath := fs.String("csr", "", "certificate request from `exmon enroll new --out`")
	days := fs.Int("days", 30, "certificate lifetime in days (1-90)")
	caName := fs.String("ca-name", enroll.DefaultCAName, "common name of the test CA on the YubiKey")
	out := fs.String("out", "", "where to write the signed certificate (default <name>.pem)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	b, err := os.ReadFile(*csrPath)
	if err != nil {
		return err
	}
	csr, err := enroll.ParseRequest(b)
	if err != nil {
		return err
	}
	ca, err := enroll.FindCA(*caName)
	if err != nil {
		return err
	}
	defer ca.Close()
	pin, err := winsys.ReadSecret("YubiKey PIV PIN: ")
	if err != nil {
		return err
	}
	ca.Signer.SetPIN(pin)
	fmt.Println("signing: touch the YubiKey when it blinks")
	der, err := enroll.SignDevice(ca, csr, *days)
	if err != nil {
		return err
	}
	if *out == "" {
		*out = csr.Subject.CommonName + ".pem"
	}
	if err := os.WriteFile(*out, enroll.PEMCert(der), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s for %s; on the test PC run: exmon enroll import --cert %s\n", *out, csr.Subject.CommonName, *out)
	return nil
}
