package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
)

func main() {
	var (
		region          string
		installerBranch string
		repoRoot        string
		jsonOut         bool
	)

	rootCmd := &cobra.Command{
		Use:   "check-marketplace-skew",
		Short: "Checks published AWS Marketplace RHCOS AMIs against the MCO boot-image skew band.",
		RunE: func(_ *cobra.Command, _ []string) error {
			return run(context.Background(), region, installerBranch, repoRoot, jsonOut)
		},
	}

	rootCmd.PersistentFlags().StringVar(&region, "region", "us-east-1", "AWS region to query DescribeImages against. A single region is sufficient: the version signal lives in the AMI Name/Description text, which is consistent across every region a Marketplace listing replicates to.")
	rootCmd.PersistentFlags().StringVar(&installerBranch, "installer-branch", "", "openshift/installer branch to fetch the RHCOS ceiling from (default: current MCO branch)")
	rootCmd.PersistentFlags().StringVar(&repoRoot, "repo-root", "", "MCO git checkout root (default: autodetected via git rev-parse --show-toplevel)")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit structured JSON instead of a human-readable table")

	os.Exit(cli.Run(rootCmd))
}

func run(ctx context.Context, region, installerBranch, repoRoot string, jsonOut bool) error {
	repoRoot, err := resolveRepoRoot(repoRoot)
	if err != nil {
		return err
	}

	shallow, err := isShallowRepo(repoRoot)
	if err != nil {
		return err
	}
	if shallow {
		return fmt.Errorf("%s is a shallow git checkout; the skew-limit reconstruction needs full history to look back %d months", repoRoot, graceMonths)
	}

	floor, err := HistoricalSkewLimits(repoRoot, time.Now())
	if err != nil {
		return err
	}

	if installerBranch == "" {
		installerBranch, err = currentGitBranch(repoRoot)
		if err != nil {
			return err
		}
	}

	ceilings := map[string]string{}
	for _, arch := range []string{"x86_64", "arm64"} {
		token, _, err := FetchInstallerCeiling(ctx, installerBranch, arch)
		if err != nil {
			return fmt.Errorf("fetching installer ceiling for %s: %w", arch, err)
		}
		ceilings[arch] = token
	}

	client, err := NewEC2Client(ctx, region)
	if err != nil {
		return err
	}

	report := Report{Floor: floor, Ceiling: ceilings}
	for _, product := range allProductSpecs() {
		result, err := CheckProduct(ctx, client, product, floor.RHCOS, ceilings[product.Arch])
		if err != nil {
			return fmt.Errorf("checking product %s (%s): %w", product.Name, product.ID, err)
		}
		report.Results = append(report.Results, result)
	}

	if jsonOut {
		if err := report.WriteJSON(os.Stdout); err != nil {
			return err
		}
	} else {
		report.WriteTable(os.Stdout)
	}

	if report.AnyFailed() {
		failed := 0
		for _, res := range report.Results {
			if !res.Pass {
				failed++
			}
		}
		return fmt.Errorf("%d of %d product codes failed the skew band check", failed, len(report.Results))
	}
	return nil
}
