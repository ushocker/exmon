// exmon-helper is the small privileged half of exmon. It runs as its own service
// (LocalSystem), started at boot and on demand by the exmon service, does one
// pass, and stops:
//   - grants the exmon service use of the client certificate's TPM key;
//   - applies an update exmon prepared, after checking everything again itself.
//
// Its settings come only from its own service command line, which `exmon install`
// writes and only an administrator can change. See internal/helper.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"

	"exmon/internal/helper"
	"exmon/internal/signdoc"
	"exmon/internal/winsys"
)

func main() {
	fs := flag.NewFlagSet("exmon-helper", flag.ExitOnError)
	certSubject := fs.String("cert-subject", "", "client certificate CN whose key exmon may use")
	certIssuer := fs.String("cert-issuer", "", "or: certificates issued by this CA")
	publisher := fs.String("publisher", "", "code-signing certificate CN updates must carry")
	requireSig := fs.Bool("require-authenticode", true, "require a valid code signature on updates")
	healthMinutes := fs.Int("health-minutes", 10, "how long a new version has to report healthy (5-60)")
	maxSizeMB := fs.Int("max-size-mb", 64, "largest update accepted (8-256)")
	args := os.Args[1:]
	once := len(args) > 0 && args[0] == "run" // `exmon-helper run`: one pass in a console (testing)
	if once {
		args = args[1:]
	}
	fs.Parse(args)

	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	sid, err := winsys.ServiceSID("exmon")
	if err != nil {
		fail(err)
	}
	keys, err := signdoc.BuiltinKeys()
	if err != nil {
		fail(err)
	}
	cfg := helper.Config{
		DataDir:             filepath.Join(os.Getenv("ProgramData"), "exmon"),
		InstallDir:          filepath.Dir(exe),
		Service:             "exmon",
		ServiceSID:          sid,
		CertSubject:         *certSubject,
		CertIssuer:          *certIssuer,
		PublisherCN:         *publisher,
		RequireAuthenticode: *requireSig,
		HealthMinutes:       min(max(*healthMinutes, 5), 60),
		MaxSizeMB:           min(max(*maxSizeMB, 8), 256),
	}
	deps := helper.Deps{
		Keys:             keys,
		Now:              time.Now,
		Authentic:        winsys.VerifyAuthenticode,
		InstalledVersion: helper.ExeVersion,
		Services:         helper.Services{},
	}

	if isService, _ := svc.IsWindowsService(); isService {
		if err := helper.RunService(cfg, deps); err != nil {
			os.Exit(1)
		}
		return
	}
	if !once {
		fmt.Println("exmon-helper runs as a service, installed by `exmon install`. For a one-off pass in an elevated console: exmon-helper run [flags]")
		return
	}
	deps.Log = func(warning bool, msg string) {
		if warning {
			msg = "WARNING: " + msg
		}
		fmt.Println(msg)
	}
	if err := helper.Run(context.Background(), cfg, deps); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "exmon-helper:", err)
	os.Exit(1)
}
