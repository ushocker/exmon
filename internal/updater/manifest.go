// Package updater is exmon's side of an update, announced by a signed release
// manifest on the control plane (inbox/release). exmon runs as LocalService, so
// it can't replace its own program; it prepares everything and hands over to the
// privileged exmon-helper service, which re-checks it all independently:
//
//  1. decide whether this device takes part (percentage of the ring) and when
//     (spread over spreadMinutes from the manifest's issue time);
//  2. download over HTTPS from an install-time allow-listed host into
//     update\incoming, checking size and SHA-256 while streaming;
//  3. write update\incoming\request.json (the signed manifest as received, and
//     the file name) and start exmon-helper;
//  4. after the helper swaps the exe and restarts exmon, report healthy once the
//     new version has run for 2 minutes and connected to the control plane.
package updater

import (
	"fmt"
	"slices"

	"exmon/internal/control"
	"exmon/internal/release"
)

// Manifest is the release manifest (see internal/release).
type Manifest = release.Manifest

// validate checks a manifest against the device's install-time update settings.
func validate(m Manifest, cfg control.UpdateConfig) error {
	if err := m.CheckShape(int64(cfg.MaxSizeMB) << 20); err != nil {
		return err
	}
	if !slices.Contains(cfg.AllowedHosts, m.Host()) {
		return fmt.Errorf("download host %q is not in control.update.allowedHosts", m.Host())
	}
	return nil
}
