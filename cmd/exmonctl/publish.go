package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"exmon/internal/config"
	"exmon/internal/signdoc"
	"exmon/internal/winsys"
)

func init() {
	commands["config publish"] = command{"sign a cloud config and publish it to a ring or devices", configPublish}
	commands["release publish"] = command{"sign a release manifest and publish it to a ring or devices", releasePublish}
	commands["cmd send"] = command{"send a signed command (report-status, trace-now, check-now) to devices", cmdSend}
	commands["retained clear"] = command{"remove a retained config or release from devices' inboxes", retainedClear}
	commands["devices"] = command{"list devices, their ring and their last status", devicesList}
	commands["rollout status"] = command{"count devices by running version (from their status)", rolloutStatus}
	commands["sync run"] = command{"run the client sync runbook now instead of waiting for its schedule", syncRun}
}

// targets picks the devices a command acts on: --device (repeatable via commas)
// or every enabled device in --ring.
type targets struct {
	devices *string
	ring    *string
}

func targetFlags(fs *flag.FlagSet) targets {
	return targets{
		devices: fs.String("device", "", "device name(s), comma-separated"),
		ring:    fs.String("ring", "", "every enabled device in this ring (client attribute 'ring')"),
	}
}

func (t targets) resolve(p Profile) ([]string, error) {
	switch {
	case *t.devices != "" && *t.ring != "":
		return nil, errors.New("use --device or --ring, not both")
	case *t.devices != "":
		return strings.Split(*t.devices, ","), nil
	case *t.ring != "":
		return deviceIDs(p, *t.ring)
	}
	return nil, errors.New("say which devices: --device <name> or --ring <ring>")
}

// newVersion is a document version that only ever goes up: the time, to the
// second, as a number like 20261007153000.
func newVersion() int64 {
	v, _ := strconv.ParseInt(time.Now().UTC().Format("20060102150405"), 10, 64)
	return v
}

// seal builds the payload (header + fields), signs it and checks the result.
func seal(p Profile, kind string, fields map[string]any, expires time.Duration) ([]byte, int64, error) {
	ver := newVersion()
	now := time.Now().UTC()
	doc := map[string]any{"kind": kind, "version": ver, "issuedAt": now, "expiresAt": now.Add(expires)}
	for k, v := range fields {
		doc[k] = v
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, err
	}
	s, err := p.signer(kind)
	if err != nil {
		return nil, 0, err
	}
	env, err := signdoc.Seal(payload, s)
	if err != nil {
		return nil, 0, err
	}
	// Key Vault signs with the key's CURRENT version, labelled with the key id
	// from the profile. If someone made a new version (or the profile names the
	// wrong id), devices would reject everything. Check against the production
	// keys built into this exmonctl (the same files exmon embeds) first.
	// (A *vaultSigner type assertion: "is s a Key Vault signer?")
	if _, ok := s.(*vaultSigner); ok {
		keys, err := signdoc.BuiltinKeys()
		if err != nil {
			return nil, 0, err
		}
		if _, err := signdoc.VerifySignature(env, kind, keys); err != nil {
			return nil, 0, fmt.Errorf("devices would reject this document (%v): the vault key's current version must match internal/signdoc/trust/<config|release>/%s.pem, and exmonctl must be built from the same source as the deployed exmon", err, s.KeyID())
		}
	}
	if len(env) > 48<<10 {
		return nil, 0, fmt.Errorf("signed document is %d KB; keep it under 48 KB (Event Grid retains at most 64 KB)", len(env)>>10)
	}
	return env, ver, nil
}

// publishAll sends env to each device's inbox topic.
func publishAll(p Profile, ids []string, kind string, env []byte, retain bool, expiry time.Duration) error {
	b, err := connectBroker(p)
	if err != nil {
		return err
	}
	defer b.close()
	failed := 0
	for _, id := range ids {
		if err := b.publish(inboxTopic(id, kind), env, retain, expiry); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", id, err)
			failed++
		}
	}
	fmt.Printf("published to %d of %d device(s)\n", len(ids)-failed, len(ids))
	if failed > 0 {
		return fmt.Errorf("%d device(s) failed", failed)
	}
	return nil
}

func configPublish(args []string) error {
	fs := flag.NewFlagSet("config publish", flag.ContinueOnError)
	load := profileFlags(fs)
	tg := targetFlags(fs)
	file := fs.String("file", "", `cloud config: the settings, shaped like exmon.json (e.g. {"probe": {...}})`)
	days := fs.Int("expires-days", 365, "how long new devices joining can still receive it (it keeps working after)")
	dry := fs.Bool("dry-run", false, "only validate and sign; don't publish")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := load()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	if err := config.ValidateCloudConfig(raw); err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	ids, err := tg.resolve(p)
	if err != nil && !*dry {
		return err
	}
	env, ver, err := seal(p, signdoc.KindConfig, map[string]any{"config": json.RawMessage(raw)}, time.Duration(*days)*24*time.Hour)
	if err != nil {
		return err
	}
	fmt.Printf("cloud config version %d signed (%d bytes)\n", ver, len(env))
	if *dry {
		return nil
	}
	return publishAll(p, ids, "config", env, true, 0)
}

func releasePublish(args []string) error {
	fs := flag.NewFlagSet("release publish", flag.ContinueOnError)
	load := profileFlags(fs)
	tg := targetFlags(fs)
	ver := fs.String("version", "", "the exmon version, e.g. 1.5.0")
	file := fs.String("file", "", "the signed exmon.exe (for its size and SHA-256)")
	dl := fs.String("url", "", "where devices download it (https, on their allowedHosts)")
	minV := fs.String("min-version", "", "devices older than this refuse it")
	percent := fs.Int("percent", 100, "share of the targeted devices that take it (1-100)")
	spread := fs.Int("spread", 60, "spread downloads over this many minutes")
	downgrade := fs.Bool("allow-downgrade", false, "this is a rollback to an older version")
	days := fs.Int("expires-days", 30, "how long devices can still receive it")
	skipSig := fs.Bool("skip-signature-check", false, "don't check the exe's code signature here (unsigned test builds)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := load()
	if err != nil {
		return err
	}
	if *ver == "" || *file == "" || *dl == "" {
		return errors.New("--version, --file and --url are required")
	}
	if !*skipSig {
		cert, err := winsys.VerifyAuthenticode(*file)
		if err != nil {
			return fmt.Errorf("%s isn't validly signed (%w); devices requiring a signature would refuse it", *file, err)
		}
		fmt.Printf("code signature OK: %s\n", cert.Subject.CommonName)
	}
	f, err := os.Open(*file)
	if err != nil {
		return err
	}
	h := sha256.New()
	size, err := io.Copy(h, f)
	f.Close()
	if err != nil {
		return err
	}
	ids, err := tg.resolve(p)
	if err != nil {
		return err
	}
	fields := map[string]any{"releaseVersion": *ver, "url": *dl, "sha256": hex.EncodeToString(h.Sum(nil)),
		"size": size, "percent": *percent, "spreadMinutes": *spread}
	if *minV != "" {
		fields["minVersion"] = *minV
	}
	if *downgrade {
		fields["allowDowngrade"] = true
	}
	env, docVer, err := seal(p, signdoc.KindRelease, fields, time.Duration(*days)*24*time.Hour)
	if err != nil {
		return err
	}
	fmt.Printf("release manifest for %s signed (document version %d, %d bytes, sha256 %s)\n", *ver, docVer, size, fields["sha256"])
	return publishAll(p, ids, "release", env, true, 0)
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("cmd send", flag.ContinueOnError)
	load := profileFlags(fs)
	tg := targetFlags(fs)
	name := fs.String("command", "", "report-status, trace-now or check-now")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *name {
	case "report-status", "trace-now", "check-now":
	default:
		return fmt.Errorf("--command %q must be report-status, trace-now or check-now", *name)
	}
	p, err := load()
	if err != nil {
		return err
	}
	ids, err := tg.resolve(p)
	if err != nil {
		return err
	}
	env, _, err := seal(p, signdoc.KindCommand, map[string]any{"command": *name}, time.Hour)
	if err != nil {
		return err
	}
	// Not retained, and the broker drops it if the device doesn't collect it within an hour.
	return publishAll(p, ids, "cmd", env, false, time.Hour)
}

func retainedClear(args []string) error {
	fs := flag.NewFlagSet("retained clear", flag.ContinueOnError)
	load := profileFlags(fs)
	tg := targetFlags(fs)
	kind := fs.String("kind", "", "config or release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *kind != "config" && *kind != "release" {
		return errors.New("--kind must be config or release")
	}
	p, err := load()
	if err != nil {
		return err
	}
	ids, err := tg.resolve(p)
	if err != nil {
		return err
	}
	// An empty retained message deletes the retained one. Devices ignore it, and
	// keep what they already applied.
	return publishAll(p, ids, *kind, nil, true, 0)
}

// statuses collects every device's retained status.
func statuses(p Profile) (map[string]map[string]any, error) {
	b, err := connectBroker(p)
	if err != nil {
		return nil, err
	}
	defer b.close()
	got, err := b.collect("exmon/v1/device/+/status", 5*time.Second)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]any{}
	for topic, payload := range got {
		parts := strings.Split(topic, "/")
		if len(parts) != 5 {
			continue
		}
		var st map[string]any
		if json.Unmarshal(payload, &st) == nil {
			out[parts[3]] = st
		}
	}
	return out, nil
}

func devicesList(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ContinueOnError)
	load := profileFlags(fs)
	ring := fs.String("ring", "", "only this ring")
	stale := fs.Int("stale-minutes", 75, "show a device as stale when it claims online but its last status is older than this (keep it above 2 x control.statusMinutes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := load()
	if err != nil {
		return err
	}
	clients, err := listClients(p)
	if err != nil {
		return err
	}
	st, err := statuses(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: no statuses:", err)
	}
	sort.Slice(clients, func(i, j int) bool { return authName(clients[i]) < authName(clients[j]) })
	fmt.Printf("%-40s %-10s %-9s %-8s %-10s %s\n", "DEVICE", "RING", "STATE", "ONLINE", "VERSION", "LAST STATUS")
	for _, c := range clients {
		a := c.Properties.Attributes
		if a["role"] != "device" || (*ring != "" && a["ring"] != *ring) {
			continue
		}
		s := st[authName(c)]
		fmt.Printf("%-40s %-10s %-9s %-8s %-10s %s\n", authName(c), a["ring"], c.Properties.State, onlineText(s, time.Now(), time.Duration(*stale)*time.Minute), orDash(s["version"]), orDash(s["time"]))
	}
	return nil
}

// onlineText shows the online flag, or "stale" when the device says it is online
// but hasn't reported for staleAfter. Devices republish their status every
// control.statusMinutes (15 by default), so it missed several: its "offline"
// message never arrived (and the broker's last will didn't fire either).
func onlineText(s map[string]any, now time.Time, staleAfter time.Duration) string {
	if s["online"] == true {
		if ts, ok := s["time"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil && now.Sub(t) > staleAfter {
				return "stale"
			}
		}
	}
	return orDash(s["online"])
}

// orDash prints a status field, or "-" when the device never reported it
// (a missing map key gives Go's zero value, nil, which %v prints as "<nil>").
func orDash(v any) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(v)
}

func rolloutStatus(args []string) error {
	fs := flag.NewFlagSet("rollout status", flag.ContinueOnError)
	load := profileFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := load()
	if err != nil {
		return err
	}
	st, err := statuses(p)
	if err != nil {
		return err
	}
	count := map[string]int{}
	for _, s := range st {
		v := fmt.Sprint(s["version"])
		if s["online"] != true {
			v += " (offline)"
		}
		count[v]++
	}
	keys := make([]string, 0, len(count))
	for k := range count {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%6d  %s\n", count[k], k)
	}
	return nil
}

func syncRun(args []string) error {
	fs := flag.NewFlagSet("sync run", flag.ContinueOnError)
	load := profileFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := load()
	if err != nil {
		return err
	}
	job, err := startRunbook(p)
	if err != nil {
		return err
	}
	fmt.Printf("started runbook %s (job %s); see its output in the Automation account\n", p.Runbook, job)
	return nil
}
