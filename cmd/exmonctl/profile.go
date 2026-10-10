package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"exmon/internal/signdoc"
)

// Profile holds the names of one environment's Azure resources, so commands
// don't need them all as flags. It is read from exmonctl.json in the current
// folder, or the file given with --profile; flags override it.
//
//	{
//	  "subscription": "…", "resourceGroup": "rg-exmon-prod",
//	  "namespace": "exmon-prod-eg", "host": "exmon-prod-eg.eastus-1.ts.eventgrid.azure.net",
//	  "vault": "exmonprodkv…", "configKey": "exmon-config-signing", "configKeyId": "cfg-2026a",
//	  "releaseKey": "exmon-release-signing", "releaseKeyId": "rel-2026a",
//	  "automationAccount": "exmon-prod-automation", "runbook": "Sync-ExmonClients"
//	}
//
// It holds no secrets: every Azure call uses your own `az login`.
type Profile struct {
	Subscription      string `json:"subscription"`
	ResourceGroup     string `json:"resourceGroup"`
	Namespace         string `json:"namespace"`
	Host              string `json:"host"`
	Vault             string `json:"vault"`
	ConfigKey         string `json:"configKey"`
	ConfigKeyID       string `json:"configKeyId"`
	ReleaseKey        string `json:"releaseKey"`
	ReleaseKeyID      string `json:"releaseKeyId"`
	AutomationAccount string `json:"automationAccount"`
	Runbook           string `json:"runbook"`

	keyFile string // --key-file: sign with a local dev key instead of Key Vault
}

// profileFlags adds the common flags to fs and returns a function that loads
// the profile and applies the flags.
func profileFlags(fs *flag.FlagSet) func() (Profile, error) {
	path := fs.String("profile", "exmonctl.json", "profile file with the environment's Azure names")
	host := fs.String("host", "", "Event Grid MQTT host (overrides the profile)")
	keyFile := fs.String("key-file", "", "sign with this local dev key (dev.pem) instead of Key Vault")
	return func() (Profile, error) {
		var p Profile
		b, err := os.ReadFile(*path)
		switch {
		case err == nil:
			dec := json.NewDecoder(bytesReader(b))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&p); err != nil {
				return p, fmt.Errorf("%s: %w", *path, err)
			}
		case errors.Is(err, os.ErrNotExist) && *path == "exmonctl.json":
			// no profile: flags only
		default:
			return p, err
		}
		if *host != "" {
			p.Host = *host
		}
		p.keyFile = *keyFile
		return p, nil
	}
}

// signer returns what signs documents of this kind: a dev key file, or the
// profile's Key Vault key for configs/commands or for releases.
func (p Profile) signer(kind string) (signdoc.Signer, error) {
	if p.keyFile != "" {
		return signdoc.LoadFileSigner(p.keyFile)
	}
	key, id := p.ConfigKey, p.ConfigKeyID
	if kind == signdoc.KindRelease {
		key, id = p.ReleaseKey, p.ReleaseKeyID
	}
	if p.Vault == "" || key == "" || id == "" {
		return nil, fmt.Errorf("signing a %s needs vault, the key name and its key id in the profile (or --key-file)", kind)
	}
	return newVaultSigner(p.Vault, key, id)
}

func (p Profile) needARM() error {
	if p.Subscription == "" || p.ResourceGroup == "" || p.Namespace == "" {
		return errors.New("the profile needs subscription, resourceGroup and namespace")
	}
	return nil
}
