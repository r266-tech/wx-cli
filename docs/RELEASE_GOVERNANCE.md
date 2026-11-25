# Source and release governance

The canonical source is `r266-tech/wx-cli`; release assets remain in
`r266-tech/wechat-cli-releases`. The historical source repository is inaccessible
and is never needed by build, install or update. Renaming a repository or dropping
history does not resolve a platform complaint; preservation of the old local
checkout is independent of publication eligibility.

## Identities and compatibility

Go 2.x modules require `/v2`: `github.com/r266-tech/wx-cli/v2`.
The executable is still `wechat-cli`. Configuration, Keychain service, installation
and user state locations remain compatible. No automatic user-state migration is
performed by repository migration. Additive JSON fields are version/commit metadata.

The initial public source tag `v2.0.0` identifies the initial import, not an accepted
binary release. It is not moved. `v2.0.1-rc.1` through `rc.6` were previews in this
repository. The `v2.0.1` tag never produced a package (the stable build still
called notarization); `v2.0.2` is the first v2 stable release. The `v2.0.4` tag
never produced a release (its bundled wxkey left WeChat closed after preparing the
shadow copy); `v2.0.5` replaces it. Do not reuse a published source tag or asset name.

## Build inputs

`release-dependencies.json` pins wxkey v1.4.11 by full commit and WCDB 2.1.16 by
upstream release ZIP SHA256 and source commit. WCDB builds with CMake for arm64,
macOS 12+, CommonCrypto and zstd disabled. Runtime message decompression remains
in Go. The build reads no user's WeChat database and requires no sibling checkout.
`prepare-release-inputs.py` writes ignored `.build/inputs.json`, containing local
paths and computed binary hashes. It is never uploaded. The package includes
WCDB, SQLCipher and wxkey licenses. Go dependencies remain checksum-pinned by go.sum.

## Workflow and credentials

CI validates macOS native Keychain builds, Windows compatibility, installer
syntax, privacy scanning and release safety tests. Third-party Actions use commit
SHAs. Fork pull requests receive no release credentials. Packages are built on a
hosted arm64 Mac, never on the Mac holding private WeChat data.

`release-macos.yml` builds a stable package when a pushed tag `vX.Y.Z` equals
`appVersion`; every other run builds a candidate. The maintainer downloads the
`macos-package` artifact, runs acceptance against those exact bytes, then runs
`scripts/publish-release.py --directory DIR --version X.Y.Z` with their own `gh`
login: it uploads a draft to `r266-tech/wechat-cli-releases`, downloads every
asset back and compares exact bytes. `--promote` re-verifies the draft, the
receipt and the source tag before making it public/latest. Existing release
assets are never clobbered. An optional GitHub App (`WECHAT_CLI_RELEASE_APP_ID`
plus `WECHAT_CLI_RELEASE_APP_PRIVATE_KEY` in the `release` environment) lets the
workflow upload the draft itself; it is automation, not a requirement.

## Signing and stable gate

Every stable release so far, v1.x included, is ad-hoc signed and not notarized.
The `curl | zsh` and `wechat-cli update` paths do not set the quarantine
attribute, so Gatekeeper does not evaluate them; integrity comes from HTTPS-only
GitHub downloads, the `.sha256` sidecar and the per-file manifest hashes.
A stable manifest therefore needs consistent signing: `adhoc` with notarization
`not_configured`, or `developer_id` with notarization `accepted`. Developer ID
(`WECHAT_CLI_SIGNING_MODE=developer_id`, `WECHAT_CLI_SIGNING_IDENTITY`,
`WECHAT_CLI_NOTARY_PROFILE`) remains supported and is an upgrade, not a gate.
Its practical gain is a stable code identity: each new ad-hoc build must be
re-authorized once for Keychain access (see below).

A stable package requires a clean source tag matching `appVersion`, a version
without a prerelease suffix, a passing acceptance receipt for those exact bytes,
and a complete, checksum-verified asset set. Unknown metadata, a bad checksum,
wrong commit or architecture, or an incomplete asset set blocks promotion.

## Local acceptance and rollback

Run `scripts/acceptance-macos.py --binary PATH --archive ZIP` against the exact
candidate. It verifies the executable bytes against the ZIP before live reads.
Digest and archive checks create private local exports; their message contents
are excluded from the allowlisted receipt. Empty messages/search/media are failures.
Full archive coverage is required. Installer rehearsal accepts `--archive ZIP`
(default: `PACKAGE.zip` beside `--package`) and verifies that the package,
archive hash and receipt identity match before running. Existing legacy packages are only rollback candidates if their own
installer can start and read the preserved Keychain/config correctly.

Install/update/rollback/uninstall rehearsals must use a dedicated HOME and managed
install/bin/log directories; never purge the user's actual config, Keychain or
state to simulate a fresh machine. A same-host isolated installation is not proof
of a truly new Mac: a separate Mac/VM acceptance remains a separate receipt check.
Do not mark skipped or blocked checks passed. `clean_machine` and
`release_endpoint_install` cannot be produced before publication on one Mac;
they are recorded as `not_run`, do not block promotion, and are checked right
after release (installing from the public endpoint into an isolated HOME). Preserve the current default release
and user's working installation until the corrected signed release is accepted.
Component updates stage all new files before replacement and restore previous
components if replacement fails. If the filesystem also prevents restoration,
the installer retains its backup directory and reports its recovery path.

Windows packaging records its own pinned WCDB build evidence and creates the
manifest before archiving. Windows artifacts remain unsigned candidates; their
bootstrap requires an explicit prerelease tag and `-AllowPrerelease` (CLI:
`update --tag vX.Y.Z-rc.N --allow-prerelease`). This does not establish Windows
stable signing or acceptance. Windows archive and uninstall-path regressions run
on the Windows CI runner; a macOS run does not replace native Windows acceptance.

### Keychain authorization after an update

An ad-hoc signed candidate has a different code identity from an older binary.
macOS can therefore require authorization to read the existing runtime item.
Normal commands remain noninteractive: `keychain status` reports
`keychain_access_required` with `loaded=false`, without exposing the item.
The unattended recovery is `wxkey bootstrap`: the next read migrates the fresh
keys into a Keychain item owned by the new binary (an item written by an older
binary is replaced, not modified). Alternatively, run
`wechat-cli keychain authorize --interactive` from the candidate installation
and complete the native macOS dialog, then retry `keychain status` and acceptance.
This command only reads the existing account item; it does not rewrite config,
export secrets, migrate records, or change the access list itself. Strict
read-only mode blocks it. Denial/cancellation leaves the old installation intact.
