# Storage reclaim

Use `session-pressure --json storage status` to check the current free space.
Use `session-pressure --json storage providers` to list the available providers.
Use `session-pressure --json storage plan` to measure a dry-run plan.

The plan can take time because it measures provider directories. The provider
list reports presence and does not measure every directory. A zero in that list
does not prove that a directory is empty.

## Interpret the plan

`estimated_bytes` has the meaning given by each provider's `estimate_kind`.
An inventory size or upper bound is not a guaranteed reclaim result. The
headline `estimated_reclaimable_bytes` excludes these unproven amounts. A zero
headline does not mean that no cleanup is possible. APFS sharing and concurrent
writes can also change the space recovered.

The Go cache hotness evidence describes file ages. The current Go cache action
does not select cold files. It requires an explicit full wipe and can cause
rebuilds. Keep the Go module cache unless a separate recovery plan covers it.

Simulator devices and the retained Nico archives are report-only. The current
provider cannot select individual simulator devices or archive entries. It must
not delete a whole collection to reclaim space.

## Apply a reviewed action

Review the selected provider and its side effects before an apply. The
`brew-cache` provider invokes `brew cleanup --prune=all -s`. This command can
remove old installed formula versions as well as cached downloads.

Apply requires a named provider or `--auto-safe`, plus `--apply`. Automatic
apply also requires storage admission enforcement. The command stops when the
target free space is met. A `no_action` outcome and a `skipped_providers`
reason show when it did not run a provider. Check the receipt and free space
after an action. Do not treat the plan estimate as a receipt.

## Archive data

An archive must have a retention plan and a separate backup. Check the archive
contents and integrity before removing a local copy. Test a restore and keep
the encryption keys available. Check the remote lifecycle rules and costs.
Keep active build caches local unless a tested cache design meets the latency
and recovery requirements.
