package updater

import (
	"context"
	"fmt"
	"time"

	"exmon/internal/event"
	"exmon/internal/release"
	"exmon/internal/version"
)

// healthyAfter is how long the new version must run (with the control plane
// connected) before it reports itself healthy.
const healthyAfter = 2 * time.Minute

// CheckOnStart reports on an update trial when exmon starts. exmon-helper runs
// the trial (and the rollback); exmon only reads update\status\trial.json and
// writes its own health report to state\update-health.json:
//   - running the version on trial: once it has run for healthyAfter and
//     connected() is true, report healthy, so the helper ends the trial;
//   - the trial was rolled back: log that once.
//
// connected reports whether the control plane has connected. An update that
// breaks the control connection is rolled back, so devices are never stranded.
func CheckOnStart(ctx context.Context, dataDir string, log event.Logger, connected func() bool) {
	var t release.Trial
	ok, err := release.ReadJSON(release.TrialPath(dataDir), release.MaxRequestBytes, &t)
	if err != nil || !ok {
		return
	}
	healthPath := release.HealthPath(dataDir)
	var h release.Health
	release.ReadJSON(healthPath, release.MaxRequestBytes, &h)
	if h.DocVersion == t.DocVersion && h.Reported == t.Outcome && t.Outcome != "" {
		return // already logged
	}

	switch {
	case t.Outcome == "rolled_back":
		log.Log("UPDATE/rolled_back", fmt.Sprintf("version=%s was not healthy (%s); back on %s", t.ReleaseVersion, t.Detail, version.Version))
		release.WriteJSON(healthPath, release.Health{DocVersion: t.DocVersion, Version: version.Version, Reported: t.Outcome})
		return
	case t.Outcome == "healthy":
		release.WriteJSON(healthPath, release.Health{DocVersion: t.DocVersion, Version: version.Version, Healthy: true, Reported: t.Outcome})
		return
	case t.ReleaseVersion != version.Version:
		return // the helper hasn't swapped yet, or is rolling back
	}

	if neverHealthy() {
		log.Log("UPDATE/test_unhealthy", fmt.Sprintf("version=%s is a test build that never reports healthy; the helper will roll it back", t.ReleaseVersion))
		return
	}
	start := time.Now()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			if now.Sub(start) < healthyAfter || !connected() {
				continue
			}
			if err := release.WriteJSON(healthPath, release.Health{DocVersion: t.DocVersion, Version: version.Version, Healthy: true}); err != nil {
				log.Log("UPDATE/failed", fmt.Sprintf("version=%s step=health error=%q", t.ReleaseVersion, err.Error()))
				return
			}
			log.Log("UPDATE/healthy", fmt.Sprintf("version=%s (was %s) is running and connected", t.ReleaseVersion, t.PreviousVersion))
			return
		}
	}
}
