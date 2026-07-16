package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHistoricalSkewLimits_RealHistory exercises HistoricalSkewLimits against this repo's own
// git history for pkg/controller/common/constants.go — a real, presently-exercisable case, not a
// fixture. The two constants were introduced in commit e2b09f2 (2025-11-19) and haven't changed
// since, so both the "stable value" and "predates introduction" paths can be proven against it.
func TestHistoricalSkewLimits_RealHistory(t *testing.T) {
	repoRoot, err := resolveRepoRoot("")
	require.NoError(t, err)

	t.Run("cutoff after introduction returns the current stable value", func(t *testing.T) {
		limits, err := HistoricalSkewLimits(repoRoot, time.Now())
		require.NoError(t, err)
		assert.Equal(t, "9.2", limits.RHCOS)
		assert.Equal(t, "4.13.0", limits.OCP)
	})

	t.Run("cutoff before introduction fails loudly", func(t *testing.T) {
		// asOf - graceMonths(4) ≈ 2025-06-01, well before the 2025-11-19 introduction commit,
		// but well after the repo's own history begins — gitRevisionBefore finds a real
		// revision, just one that predates the constants existing under these names.
		asOf := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
		_, err := HistoricalSkewLimits(repoRoot, asOf)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "skew-limit constants not present")
	})
}

// TestHistoricalSkewLimits_GracePeriod exercises the double-bump-within-window grace period using
// a synthetic temp repo, since real MCO history hasn't bumped the skew limit recently enough to
// prove this path end-to-end.
func TestHistoricalSkewLimits_GracePeriod(t *testing.T) {
	repoRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repoRoot, skewLimitsPath)), 0o755))

	runGitCmd := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoRoot
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v failed: %s", args, out)
	}

	runGitCmd(nil, "init", "-q")
	runGitCmd(nil, "config", "user.email", "test@example.com")
	runGitCmd(nil, "config", "user.name", "test")

	commitConstants := func(rhcos, ocp, date string) {
		t.Helper()
		src := "package common\n\nconst (\n\tRHCOSVersionBootImageSkewLimit = \"" + rhcos + "\"\n\tOCPVersionBootImageSkewLimit   = \"" + ocp + "\"\n)\n"
		require.NoError(t, os.WriteFile(filepath.Join(repoRoot, skewLimitsPath), []byte(src), 0o644))
		runGitCmd(nil, "add", "-A")
		runGitCmd([]string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", "set "+rhcos+"/"+ocp)
	}

	// Bump B lands just before the cutoff (now-4mo); bump C lands just after it, inside the
	// window. If the grace period were naively anchored to "time since the last bump" (bump C,
	// ~3 months ago), it would look like there's still time left on the clock. Reconstructing
	// the actual value as of the cutoff sidesteps that: it should return bump B's value ("9.1"),
	// which is what was genuinely in effect 4 months ago, regardless of the later bump C.
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	commitConstants("9.0", "4.12.0", time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))   // bump A: long-standing baseline
	commitConstants("9.1", "4.13.0", time.Date(2025, 12, 15, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)) // bump B: before cutoff
	commitConstants("9.2", "4.13.0", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))   // bump C: after cutoff, within the window

	limits, err := HistoricalSkewLimits(repoRoot, now)
	require.NoError(t, err)
	assert.Equal(t, "9.1", limits.RHCOS)
}
