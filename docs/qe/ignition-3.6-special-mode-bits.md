# QE notes: Ignition 3.6 special mode bit tests (PR #6236)

## Background

PR #6236 bumps MCO's internal ignition spec from 3.5.0 to 3.6.0 (ignition/v2 2.20.0 to 2.26.0).

Ignition 3.6 formally adds support for special mode bits on files and directories:
- setuid (04000)
- setgid (02000)
- sticky (01000)

Before 3.6, the ignition v3.5 schema allowed any integer mode value. It emitted a warning for
special bits but gave no guarantee that the bits were honored.

## What was already done in the PR

The existing `[PolarionID:63477]` test was updated in-place:
- Version list extended from `["2.2.0" ... "3.5.0"]` to include `"3.6.0"`
- `defaultIgnitionVersion` changed from `"3.5.0"` to `"3.6.0"`
- Test title updated from "Default 3.5.0" to "Default 3.6.0"

The 3.5.0 test no longer exists as a separate test. It was replaced.

## What was written as part of this task

### E2E tests (`test/extended-priv/mco_storage.go`)

**`[TODO-POLARION] Ignition 3.6 special mode bits are applied to files on nodes [Disruptive]`**

Creates a 3.6.0 MachineConfig with two files:
- `/etc/mc-special-mode-setuid.test` at mode 04755 (setuid + rwxr-xr-x, decimal 2541)
- `/etc/mc-special-mode-setgid.test` at mode 02755 (setgid + rwxr-xr-x, decimal 1517)

Waits for the worker MCP to update, then runs `stat` on every worker node and asserts
`GetOctalPermissions()` returns `"4755"` and `"2755"` respectively.

This test documents the desired behavior. It will not pass until the two bugs below are fixed.

**`[TODO-POLARION] Ignition 3.5 MachineConfig with special mode bits silently strips them [Disruptive]`**

Creates a 3.5.0 MachineConfig with a file at mode 04755. Asserts that the pool updates
successfully (no degradation), and the file on each node has mode 0755. The special bits are
stripped.

This behavior is correct by design. The upconverter at line 36 of
`vendor/github.com/coreos/ignition/v2/config/v3_6/translate/translate.go`
uses a bitmask to remove special bits from any pre-3.6 config during v3.6 promotion:

```go
ret.Mode = util.IntToPtr(*old.Mode & ^07000)
```

After masking, mode 04755 becomes 0755. That value passes `validateIgn3FileModes` (0755 ≤ 0777).
The pool updates, the file lands at 0755, and no error is reported. This is intentional:
special bits in pre-3.6 configs had undefined behavior, so the upconverter strips them rather
than applying them.

The test name and assertion were updated after running against a live cluster confirmed this
behavior. The original test expected a pool degradation, which does not happen.

### Unit test (`pkg/controller/common/helpers_test.go`)

**`TestIgnitionConverterSpecialModeBitsDownconversion`**

Builds a 3.6.0 ignition config with a file at mode 04755. Passes it through the ignition
converter to 3.5.0 and 3.4.0. Asserts the converter returns an error containing
`"special mode bits"`. Also asserts the identity 3.6 to 3.6 conversion succeeds.

The converter at `vendor/github.com/coreos/ign-converter/translate/v36tov35/v36tov35.go`
returns an explicit error for `(mode & 07000) != 0`. This test documents and pins that behavior.

Test passes as of this writing.

## Bugs found in the PR

### Bug 1: `validateIgn3FileModes` blocks special bits unconditionally

**File:** `pkg/controller/common/helpers.go` lines 391-398

```go
func validateIgn3FileModes(cfg ign3types.Config) error {
    for _, file := range cfg.Storage.Files {
        if file.Mode != nil && os.FileMode(*file.Mode) > os.ModePerm {
            return fmt.Errorf("invalid mode %#o for %s, cannot exceed %#o", ...)
        }
    }
    return nil
}
```

`os.ModePerm` = 0777. Any mode with special bits (04755 = 2541 decimal) exceeds 0777 and is
rejected. This validation runs regardless of the ignition version in the config.

**Effect:** A 3.6.0 MC with special mode bits is rejected at render time with
"cannot exceed 0o777". The positive e2e test will not pass until this is fixed.

**Required fix:** Update the function to allow special bits for `cfg.Ignition.Version == "3.6.0"`. Continue rejecting them for 3.5.0 and earlier. The negative test depends on this.

Example fix:
```go
func validateIgn3FileModes(cfg ign3types.Config) error {
    allowSpecialBits := cfg.Ignition.Version == "3.6.0"
    maxMode := os.ModePerm
    if allowSpecialBits {
        maxMode = os.FileMode(07777)
    }
    for _, file := range cfg.Storage.Files {
        if file.Mode != nil && os.FileMode(*file.Mode) > maxMode {
            return fmt.Errorf("invalid mode %#o for %s, cannot exceed %#o", *file.Mode, file.Path, maxMode)
        }
    }
    return nil
}
```

### Bug 2: `os.FileMode` cast drops special bits in the daemon

**File:** `pkg/daemon/file_writers.go` around line 248

```go
mode = os.FileMode(*file.Mode)
```

Go's `os.FileMode` encodes special bits at non-POSIX bit positions:
- `os.ModeSetuid` = bit 23
- `os.ModeSetgid` = bit 22
- `os.ModeSticky` = bit 19

The POSIX integer stores them at:
- setuid = bit 11 (0o4000)
- setgid = bit 10 (0o2000)
- sticky = bit 9 (0o1000)

`os.FileMode(2541)` (= 04755 POSIX) has bit 11 set. `os.File.Chmod` calls `syscallMode()` which
only reads from bits 22-23 and 19. The POSIX special bits at positions 9-11 are not at those
positions in the Go type. `syscallMode()` produces mode 0755 and the special bits are silently lost.

**Effect:** After bug 1 is fixed, a file requested at mode 04755 will be written at 0755 on disk.

**Required fix:** Replace the raw cast with an explicit conversion:
```go
rawMode := *file.Mode
posixPerm := os.FileMode(rawMode & 0777)
if rawMode&0o4000 != 0 {
    posixPerm |= os.ModeSetuid
}
if rawMode&0o2000 != 0 {
    posixPerm |= os.ModeSetgid
}
if rawMode&0o1000 != 0 {
    posixPerm |= os.ModeSticky
}
mode = posixPerm
```

## Out of scope

`storage.directories` is not supported by MCO. The reconcile loop in
`pkg/controller/common/reconcile.go` rejects any MC that contains changed ignition directories:
```go
if !reflect.DeepEqual(oldIgn.Storage.Directories, newIgn.Storage.Directories) {
    return fmt.Errorf("ignition directories section contains changes")
}
```
Sticky-bit directory tests are out of scope until that restriction is lifted.

## Ignition version validation summary

| Version | Special bits in schema | ignition validator | MCO mode guard (helpers.go) |
|---|---|---|---|
| 3.5 and earlier | Allowed as integer | AddOnWarn (not fatal) | Rejects mode > 0777 (current behavior) |
| 3.6 | Officially supported | No warning | Must allow special bits (after bug 1 fix) |

The ignition library itself does not reject special bits in v3.5. It only warns. MCO's own
validator is what enforces the restriction. The negative test (3.5 rejected) relies on MCO's
validator, not ignition's.

## Placeholder Polarion IDs

Both new e2e tests use `TODO-POLARION` as a placeholder. Replace with real IDs from
Polarion before merging.
