# Cloud config security (proposal, not applicable today)

> **Status:** only relevant if the [control plane](control-plane.md) is built, so that exmon's config is delivered from the cloud. Nothing here is planned or implemented. For the current setup (local config, console or service), see [Config security review](config-security-review.md).

## The model

With a control plane, **the entire config comes from the cloud**: the cloud document is the only config a machine has, and the same config typically goes to many machines.

Two consequences shape everything below:

1. **Whoever controls the control plane controls every machine's config.** That includes an attacker who compromises it, a leaked signing key, or an honest mistake.
2. **The only limits a cloud config can't override are the ones built into the exmon binary.** A limit stored in the config is set by the same party that sets everything else, so it protects nothing.

So the protections here are either built into the binary, or set by a **different authority** than the control plane, such as the person deploying exmon through Intune.

## C1. Cloud-delivered paths

Locally, output paths are contained (see [F2](config-security-review.md#f2-output-paths-can-rename-delete-or-write-files--fixed-defence-in-depth)). From the cloud, they shouldn't be settable at all:

- The cloud document is decoded into its own struct that **has no path fields**: no `log.path`, `incident.dir`, `remote.spoolDir`, `remote.outbox`, `baseline.path` or `outputRoots`. Paths come from the binary's defaults, or from flags baked into the service at install.
- Because the fields don't exist, a cloud config can't set them even by mistake. With strict decoding (C4), trying to is an error.

## C2. Traffic abuse at fleet scale

One bad config pushed to every machine turns the fleet into a traffic generator against a third party: many machines pinging, connecting to and requesting a target, every 10 seconds, with unlimited endpoints and pings. It can also turn every machine into a scanner of its own internal network, with results forwarded to the SIEM.

Mitigations built into the binary, so they apply whatever the config says:

- **Global probe rate limit:** a token bucket shared by every probe action (pings, TCP connects, HTTP requests, path-trace hops), e.g. 600 actions per minute per machine. Actions beyond the limit are skipped and counted on a `PROBE/limited` line.
- **Link-local and metadata addresses refused:** `169.254.0.0/16` (including cloud metadata at `169.254.169.254`) and `fe80::/10`, checked **after** DNS resolution, so a name that resolves there is caught too.
- **HTTP redirects capped:** `checkHTTPWith` in `internal/probe/http.go` currently follows up to 10 redirects to any host; cap it at 5.
- **Stricter ranges for cloud configs** than the local ones in [F3](config-security-review.md#f3-no-upper-limits-on-numbers--fixed): `probe.intervalSeconds` at least 60; no "0 = unlimited" for `maxIncidents` or `log.maxSizeMB`; tighter caps on endpoint, HTTP test and trigger counts.

## C3. Target allow-list (optional)

An allow-list of domains and address ranges that probe targets must fall within. **It only adds protection if a different authority than the control plane sets it.** If the cloud config carried the allow-list, the control plane would set both the limit and the targets.

The workable option is **install-time flags**: e.g. `exmon.exe install --probe-allowed-targets "*.corp.example.com,10.0.0.0/8"`, pushed by whoever deploys exmon (Intune or another MDM) and stored in the service's command line, which only administrators can change. Cloud-delivered targets outside the list are dropped and reported in the device's status.

Without a separate deploying authority, rely on C2's built-in limits instead.

## C4. Signed, strictly decoded documents

Cloud configs must be signed and versioned, verified on the device, and decoded strictly (unknown keys rejected), with a logged diff whenever one is applied. This is covered by the [control plane proposal](control-plane.md#decisions), decisions 5 and 9, and its [device behaviour](control-plane.md#applying-a-config) section; it isn't repeated here.
