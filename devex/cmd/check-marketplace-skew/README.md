# check-marketplace-skew

Checks published AWS Marketplace RHCOS AMIs against MCO's boot-image skew band, so a stale or
mismatched Marketplace image can be caught before customers hit it. This is the check logic behind
a periodic CI job (see `docs/design/aws-marketplace.md` for the full design); this tool is the part
that runs locally or in CI — filing/searching Jira bugs on failure is a separate, thin wrapper.

For each known Marketplace product code (OCP/OKE/OPP × x86_64/arm64, their EMEA x86_64 variants,
and ROSA), it fetches every published AMI and checks whether **at least one** falls within an
acceptable version band:

- **Floor**: `RHCOSVersionBootImageSkewLimit`/`OCPVersionBootImageSkewLimit`
  (`pkg/controller/common/constants.go`) as they stood ~4 months ago, reconstructed from this
  repo's own git history — not persisted state — so Marketplace gets a grace period to catch up
  after MCO bumps its skew limit.
- **Ceiling**: the branch's own pinned RHCOS version, fetched live from `openshift/installer`'s
  `data/data/coreos/coreos-rhel-10.json` on the matching branch.

The check is existential, not singular: Marketplace can keep multiple AMI versions live at once, so
the current/default one being out of band doesn't necessarily mean there's no compliant option.

## Usage

```console
$ make check-marketplace-skew
Skew-limit floor: RHCOS 9.2 (OCP 4.13.0)
Installer ceiling (x86_64): 10.2
Installer ceiling (arm64): 10.2

PRODUCT           PRODUCT ID                            RESULT  MATCHED AMI            DETAIL
OCP x86_64        59ead7de-2540-4653-a8b0-fa7926d5c845   PASS    ami-0123456789abcdef0  9.6.20260210-0
OKE x86_64        963b36c3-de6f-48ed-b802-2b38b2a2cdeb   PASS    ami-0fedcba9876543210  9.6.20260210-0
...
```

Flags:

- `--region` (default `us-east-1`): AWS region to query `DescribeImages` in. A single region is
  sufficient — the version signal lives in the AMI `Name`/`Description` text, which is consistent
  across every region a Marketplace listing replicates to.
- `--installer-branch` (default: current MCO branch): the `openshift/installer` branch to fetch the
  RHCOS ceiling from. Override for local testing against a different release branch.
- `--repo-root` (default: autodetected): the MCO git checkout to reconstruct skew-limit history
  from. Must be a full (non-shallow) clone — the tool errors out on a shallow checkout.
- `--json`: emit a structured JSON report instead of a human-readable table, for a CI wrapper to
  parse when deciding whether to file/update a Jira bug.

Exit code is non-zero if any product code fails its band check.

## Credentials

Uses the AWS SDK's default credential chain (environment variables, shared config file, instance
profile) — there's no in-cluster Secret/STS flow here, unlike the boot image controller.

## Known gap

There are no live AWS credentials in CI for this repo's own test suite, so `DescribeImages`/
`CheckProduct` aren't exercised end-to-end by `go test`. Cross-check tool output manually against
AWS CLI Marketplace-AMI queries once credentials are available.
