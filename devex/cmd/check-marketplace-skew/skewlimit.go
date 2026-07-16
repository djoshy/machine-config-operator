package main

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// SkewLimits holds the reconstructed value of MCO's boot-image skew-limit constants at a point in time.
type SkewLimits struct {
	RHCOS string // e.g. "9.2" — the operative floor for AMI-token comparison
	OCP   string // e.g. "4.13.0" — surfaced for context/output only
}

const (
	// graceMonths gives Marketplace publishers time to catch up after MCO bumps its skew limit.
	graceMonths = 4
	// skewLimitsPath is relative to the MCO repo root.
	skewLimitsPath = "pkg/controller/common/constants.go"
)

// HistoricalSkewLimits reconstructs the RHCOSVersionBootImageSkewLimit/OCPVersionBootImageSkewLimit
// constants (pkg/controller/common/constants.go) as they stood graceMonths before asOf, via git
// history rather than persisted state. This anchors the grace period to when the constants
// actually held a given value, so a double-bump within the window can't reset the clock the way
// anchoring to "time since last bump" would.
func HistoricalSkewLimits(repoRoot string, asOf time.Time) (SkewLimits, error) {
	cutoff := asOf.AddDate(0, -graceMonths, 0)

	rev, err := gitRevisionBefore(repoRoot, skewLimitsPath, cutoff)
	if err != nil {
		return SkewLimits{}, err
	}

	usedFallback := false
	if rev == "" {
		// No commit predates the grace window (repo/checkout younger than graceMonths) — there's
		// no older data to grant a grace period against, so use the oldest value available.
		rev, err = gitOldestRevision(repoRoot, skewLimitsPath)
		if err != nil {
			return SkewLimits{}, err
		}
		usedFallback = true
	}

	src, err := gitShowFile(repoRoot, rev, skewLimitsPath)
	if err != nil {
		return SkewLimits{}, err
	}

	limits, err := parseSkewLimitConstants(src)
	if err != nil {
		return SkewLimits{}, fmt.Errorf("skew-limit constants not present in %s as of commit %s (cutoff %s): %w", skewLimitsPath, rev, cutoff.Format(time.RFC3339), err)
	}

	if usedFallback {
		klog.Warningf("no commit predates the %d-month grace window; using the oldest available value of the skew-limit constants (commit %s)", graceMonths, rev)
	}

	return limits, nil
}

// gitRevisionBefore returns the most recent commit touching path as of asOf, or "" (no error) if
// no such commit exists.
func gitRevisionBefore(repoRoot, path string, asOf time.Time) (string, error) {
	out, err := runGit(repoRoot, "log", "--format=%H", "--before="+asOf.Format(time.RFC3339), "-n1", "--", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// gitOldestRevision returns the oldest commit touching path.
func gitOldestRevision(repoRoot, path string) (string, error) {
	out, err := runGit(repoRoot, "log", "--format=%H", "--", path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("no git history found for %s", path)
	}
	return lines[len(lines)-1], nil
}

// gitShowFile returns path's content as of rev.
func gitShowFile(repoRoot, rev, path string) (string, error) {
	return runGit(repoRoot, "show", rev+":"+path)
}

var (
	rhcosSkewLimitRe = regexp.MustCompile(`RHCOSVersionBootImageSkewLimit\s*=\s*"([^"]+)"`)
	ocpSkewLimitRe   = regexp.MustCompile(`OCPVersionBootImageSkewLimit\s*=\s*"([^"]+)"`)
)

// parseSkewLimitConstants extracts the two skew-limit string-literal constants from a copy of
// constants.go's source text. Deliberately does not build/vet the historical revision — that's
// slow, fragile (an old revision may not compile standalone against current vendor state), and
// unnecessary for extracting two string literals.
func parseSkewLimitConstants(src string) (SkewLimits, error) {
	rhcosMatch := rhcosSkewLimitRe.FindStringSubmatch(src)
	ocpMatch := ocpSkewLimitRe.FindStringSubmatch(src)
	if rhcosMatch == nil || ocpMatch == nil {
		return SkewLimits{}, fmt.Errorf("could not find both RHCOSVersionBootImageSkewLimit and OCPVersionBootImageSkewLimit constants")
	}
	return SkewLimits{RHCOS: rhcosMatch[1], OCP: ocpMatch[1]}, nil
}
