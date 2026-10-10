package main

import (
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"exmon/internal/enroll"
	"exmon/internal/service"
	"exmon/internal/winsys"
)

const enrollUsage = `exmon enroll gives a TEST device its MQTT client certificate (production devices get theirs from Intune).

  exmon enroll new --name exmon-test-01 [--allow-software-key]
        create the device key in the TPM and a certificate request
  exmon enroll sign-local [--days 30] [--ca-name "exmon Test Device CA"]
        sign the request with the YubiKey test CA plugged into this PC, and install the certificate
  exmon enroll import --cert exmon-test-01.pem
        install a certificate signed elsewhere (exmonctl ca sign-csr)
  exmon enroll show
        show the enrolment and its certificate
  exmon enroll remove
        delete the device certificate and key

All of them need an elevated (Run as administrator) prompt.`

// enrollCmd runs `exmon enroll <step> ...`.
func enrollCmd(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Println(enrollUsage)
		return nil
	}
	if !winsys.IsElevated() {
		return errors.New("exmon enroll must be run from an elevated (Run as administrator) prompt")
	}
	steps := map[string]func([]string) error{
		"new": enrollNew, "sign-local": enrollSignLocal, "import": enrollImport,
		"show": enrollShow, "remove": enrollRemove,
	}
	step, ok := steps[args[0]]
	if !ok {
		return fmt.Errorf("unknown step %q\n\n%s", args[0], enrollUsage)
	}
	return step(args[1:])
}

func requestPath(st enroll.State) string {
	return filepath.Join(service.DataDir(), "state", st.Name+".csr")
}

func enrollNew(args []string) error {
	fs := flag.NewFlagSet("enroll new", flag.ContinueOnError)
	name := fs.String("name", "", "device name from the dev namespace's client pool, e.g. exmon-test-01")
	soft := fs.Bool("allow-software-key", false, "use a software key if there is no TPM (VMs)")
	out := fs.String("out", "", "also write the certificate request here (for signing on another machine)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The state folder must exist with its admin-only permissions before anything is written to it.
	if err := service.LockDown(os.Stdout); err != nil {
		return err
	}
	st, csr, err := enroll.NewRequest(*name, *soft)
	if err != nil {
		return err
	}
	if err := st.Save(service.DataDir()); err != nil {
		return err
	}
	if err := os.WriteFile(requestPath(st), csr, 0o644); err != nil {
		return err
	}
	if *out != "" {
		if err := os.WriteFile(*out, csr, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("created the device key for %s in %s\n", st.Name, st.Provider)
	if st.Provider != winsys.ProviderTPM {
		fmt.Println("WARNING: this is a software key, not a TPM key; use it only on test VMs")
	}
	fmt.Println("next: plug in the YubiKey and run  exmon enroll sign-local")
	if *out != "" {
		fmt.Printf("  or sign %s elsewhere with  exmonctl ca sign-csr  and run  exmon enroll import --cert <file>\n", *out)
	}
	return nil
}

func enrollSignLocal(args []string) error {
	fs := flag.NewFlagSet("enroll sign-local", flag.ContinueOnError)
	days := fs.Int("days", 30, "certificate lifetime in days (1-90)")
	caName := fs.String("ca-name", enroll.DefaultCAName, "common name of the test CA certificate on the YubiKey")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := enroll.LoadState(service.DataDir())
	if err != nil {
		return err
	}
	reqPEM, err := os.ReadFile(requestPath(st))
	if err != nil {
		return fmt.Errorf("certificate request: %w (run exmon enroll new again)", err)
	}
	csr, err := enroll.ParseRequest(reqPEM)
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
		return fmt.Errorf("sign with the YubiKey: %w", err)
	}
	return finishImport(st, der)
}

func enrollImport(args []string) error {
	fs := flag.NewFlagSet("enroll import", flag.ContinueOnError)
	certPath := fs.String("cert", "", "signed certificate (PEM or DER)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, err := enroll.LoadState(service.DataDir())
	if err != nil {
		return err
	}
	b, err := os.ReadFile(*certPath)
	if err != nil {
		return err
	}
	der, err := enroll.ParseCertPEM(b)
	if err != nil {
		return err
	}
	return finishImport(st, der)
}

func finishImport(st enroll.State, der []byte) error {
	cert, err := enroll.Import(st, der)
	if err != nil {
		return err
	}
	fmt.Printf("installed certificate CN=%s, issued by %q, valid until %s, in LocalMachine\\My\n",
		cert.Subject.CommonName, cert.Issuer.CommonName, cert.NotAfter.Format("2006-01-02"))
	grantServiceKeyAccess(st)
	fmt.Println("unplug the YubiKey; then install or update the service, for example:")
	fmt.Printf("  exmon.exe install --control-host <namespace>.<region>-1.ts.eventgrid.azure.net --control-cert-subject %s --control-cert-issuer %q\n",
		st.Name, cert.Issuer.CommonName)
	return nil
}

func enrollShow([]string) error {
	st, err := enroll.LoadState(service.DataDir())
	if err != nil {
		return err
	}
	fmt.Printf("name:     %s\nkey:      %s in %s\n", st.Name, st.KeyName, st.Provider)
	cc, err := winsys.FindClientCert(winsys.LocalMachine, "My", func(c *x509.Certificate) bool {
		return c.Subject.CommonName == st.Name
	}, false)
	if err != nil {
		fmt.Println("certificate: none usable yet:", err)
		return nil
	}
	defer cc.Close()
	fmt.Printf("certificate: CN=%s, issuer %q, %s to %s\n", cc.Leaf.Subject.CommonName, cc.Leaf.Issuer.CommonName,
		cc.Leaf.NotBefore.Format("2006-01-02"), cc.Leaf.NotAfter.Format("2006-01-02"))
	return nil
}

func enrollRemove([]string) error {
	st, err := enroll.LoadState(service.DataDir())
	if err != nil {
		return err
	}
	n, err := winsys.RemoveCerts(winsys.LocalMachine, "My", func(c *x509.Certificate) bool {
		return c.Subject.CommonName == st.Name && !strings.EqualFold(c.Issuer.CommonName, c.Subject.CommonName)
	})
	if err != nil {
		return err
	}
	if err := winsys.DeleteKey(st.Provider, st.KeyName, st.Machine); err != nil {
		return err
	}
	os.Remove(requestPath(st))
	os.Remove(enroll.StatePath(service.DataDir()))
	fmt.Printf("removed %d certificate(s) and the key for %s\n", n, st.Name)
	return nil
}

// grantServiceKeyAccess lets the exmon service (LocalService) use the new key
// straight away. exmon-helper does the same at every boot and after renewals; if
// the service isn't installed yet, install's first helper run takes care of it.
func grantServiceKeyAccess(st enroll.State) {
	sid, err := winsys.ServiceSID(service.Name)
	if err != nil {
		fmt.Println("the exmon service isn't installed yet; exmon-helper will let it use the key once it is")
		return
	}
	key, err := winsys.OpenKey(st.Provider, st.KeyName, st.Machine)
	if err != nil {
		fmt.Println("warning: couldn't open the key to grant the exmon service access:", err)
		return
	}
	defer key.Close()
	if _, err := key.GrantKeyRead(sid); err != nil {
		fmt.Println("warning: couldn't let the exmon service use the key:", err)
		return
	}
	fmt.Println(`the exmon service (NT SERVICE\exmon) may now use the key`)
}
