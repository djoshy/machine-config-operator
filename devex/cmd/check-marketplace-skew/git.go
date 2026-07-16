package main

import (
	"fmt"
	"os/exec"
	"strings"
)

// runGit shells out to git in repoRoot, matching hack/update_amis.py's own precedent of
// shelling to git rather than pulling in a Go git library.
func runGit(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("git %s failed: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// resolveRepoRoot returns repoRoot unchanged if set, otherwise autodetects it from the
// current working directory via git rev-parse --show-toplevel.
func resolveRepoRoot(repoRoot string) (string, error) {
	if repoRoot != "" {
		return repoRoot, nil
	}
	out, err := runGit(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("failed to autodetect repo root: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// isShallowRepo reports whether repoRoot is a shallow git clone, in which case the
// skew-limit git-history reconstruction cannot see far enough back to be trusted.
func isShallowRepo(repoRoot string) (bool, error) {
	out, err := runGit(repoRoot, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

// currentGitBranch returns the checked-out branch name in repoRoot.
func currentGitBranch(repoRoot string) (string, error) {
	out, err := runGit(repoRoot, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
