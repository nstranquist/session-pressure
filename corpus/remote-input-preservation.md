# Remote input preservation — WORK-254

Owner: codex, session 01a1c547-f41d-7d02-ac3b-a539d1ff6de5.
Status: landed, installed and automatic behavior verified, 2026-11-10.
User request (summarised, not verbatim): fix catalog packaging and expand useful
runner work while preserving delivery throughput.

## Terms, architecture and provenance

A repository input is a Git-tracked file or unignored pending file inside the
selected module/repository scope. An embed input can be a tracked generated
artifact. A source manifest records bytes and Unix permissions. The Mac seals
these inputs; the gr3 scheduler admits and retains the host-bound request. Remote
execution uses Linux AMD64; release construction also targets Darwin ARM64 with
the pinned native C toolchain. Runtime acceptance must occur on the Mac.

## Decisions and alternatives

001. Retain tracked generated compiler inputs (5/5, adoption cutoff 4/5).
Reject excluding every dist/bin directory (1/5): Go embed can require them.
Reject copying all generated directories (2/5): ignored dependency trees and
large artifacts are outside the authored candidate. Reconsider with a bounded
explicit artifact manifest. Existing credential-name, symlink and size exclusions
remain authoritative even for tracked files.
002. Keep private staging directories and restore archived file permissions
(5/5). Reject applying a private-directory umask to regular source permissions
(1/5): source/extraction tests rely on their portable mode. Private enclosing
directories continue to restrict access; this does not authorize public staging.
003. Promote complete verified native bundles and the immutable resident helper
(5/5). CLI promotion alone (2/5) leaves coordinated work on old code. Inventory
selection and host bindings remain unchanged during repair.

## Evidence and corrections

Source c05af04c08c4fa6fddfe57fa860ef374af3af391 retains tracked embeds. Pinning
regression: TestRemoteTrackedEmbedAssetsSurviveSourceSnapshot covers both upload
scopes, ignored/untracked artifacts, credentials, symlinks and size accounting.
Full sessionpressure suite + vet passed on gr3:
431e0466-b194-4662-95e9-e77dcc615981.
Native release passed: 91b7eaba-02fe-45cb-a259-b17e2ea75473. The initial release
attempt failed due to malformed quoted GOFLAGS; that receipt remains retained.
The complete bundle was installed with expected-current b90b4b80..., then
`monitor install` promoted the immutable helper. Policy JSON was identical
before/after; the new resident report was ready at c05af04c.
Catalog automatic full Go suite then passed on gr3:
3984f4a8-17c5-4001-a748-c36805cc7cf5. The first automatic retry still used the old
helper and failed; this corrects the premature inference that a new CLI alone
would prove the repair in coordinated work.

Processtree's sealed archive race/vet passed, but its automatic trial failed on
regular permissions becoming 0600. Both tar extraction paths now explicitly
preserve archived permissions. TestRemoteSourceDeltaKeepsFileModes now checks
exact 0644 seed permissions and 0755 executable delta permissions, rather than
only checking an executable bit. All results are host-bound to gr3; runner 2 is
retired and runner 1 remains explicit reserve.

Installed f3074a2e matched CLI/helper hashes with unchanged policy. Automatic
processtree retry 1090f9d7-10ca-4898-9699-31414fce436a still failed: a cached
base had correct bytes and manifest but damaged actual modes. Stage verification
now checks actual modes as well as content and drops/reseeds damaged bases. The
mode regression corrupts an existing base and proves automatic reseeding. Full
package and vet passed: 8c70f336-0a05-489b-ad9f-a3d5aecdedc5.

## Open proof items

- [x] Full package + vet: 5a62e98d-6381-4c76-a51d-643ea3b3bb0f (gr3, exit 0).
- [x] Guarded publication 036dc8d4: source 0f9f5340; native bundle 82eed293 passed.
  CLI/helper match source, ready resident; policy unchanged, artifact drift false.
- [x] Installed-helper full automatic processtree 41885446 and catalog 4788ca36.
  Installed default without inventory override: processtree 716a97a9, catalog 039261a9-73aa-44ed-9d31-c3dca1f69a4b.
- [x] Final inventory generation 2026-11-10-runner3-extended-verified installed;
  enrollment and Node/Python proof documented in nicos-ops corpus.

The nicos-ops corpus owns the cross-repository trial ledger and delivery-admission
measurements. This change does not assert native Windows, browser or GPU proof.
