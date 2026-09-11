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
binary release. It is not moved. The next corrected candidate uses `v2.0.1-rc.1`;
formal `v2.0.1` requires all gates. Do not reuse a published source tag or asset name.

## Build inputs

`release-dependencies.json` pins wxkey v1.4.8 by full commit and WCDB 2.1.16 by
upstream release ZIP SHA256 and source commit. WCDB builds with CMake for arm64,
macOS 12+, CommonCrypto and zstd disabled. Runtime message decompression remains
in Go. The build reads no user's WeChat database and requires no sibling checkout.
`prepare-release-inputs.py` writes ignored `.build/inputs.json`, containing local
paths and computed binary hashes. It is never uploaded. The package includes
WCDB, SQLCipher and wxkey licenses. Go dependencies remain checksum-pinned by go.sum.

## Workflow and credentials

CI validates macOS native Keychain builds, Windows compatibility, installer
syntax, privacy scanning and release safety tests. Third-party Actions use commit
SHAs. Fork pull requests receive no release credentials. Candidate packaging runs
on a hosted arm64 Mac, not the Mac holding private WeChat data.

Create a private GitHub App `wx-cli-release-publisher`, webhook disabled, no user
permissions or events, only repository Contents read/write plus implicit Metadata
read. Install it for **only** `wechat-cli-releases`. Configure source repository
variable `WECHAT_CLI_RELEASE_APP_ID` and secret `WECHAT_CLI_RELEASE_APP_PRIVATE_KEY`.
Registering/installing the App requires GitHub account verification. The workflow
mints a short-lived installation token scoped to that single repository; there is
no personal token fallback. Place the credentials behind the `release` environment.

`release-macos.yml` always builds a candidate. Once App credentials are available
it uploads a draft and downloads every uploaded asset to compare exact bytes.
`promote-release.yml` verifies the draft again and requires a receipt bound to its
zip SHA256, version and source commit. Receipt checks must all be passed. Only then
can a draft become public/latest. Existing release assets are never clobbered.

## Signing and stable gate

A local candidate uses ad-hoc signatures explicitly marked as such. To build a
stable package set `WECHAT_CLI_SIGNING_MODE=developer_id`,
`WECHAT_CLI_SIGNING_IDENTITY` to a valid Developer ID identity and
`WECHAT_CLI_NOTARY_PROFILE` to an existing notarytool Keychain profile. The stable
package path requires a clean matching source tag, valid signatures and Accepted
notarization. Certificates/private keys and notarization credentials never enter
source or receipts. Installers preserve valid signatures instead of replacing
Developer ID signatures with ad-hoc signatures.

No valid identity, no accepted notarization, a partial archive, missing receipt,
unknown metadata, bad checksum, wrong commit, wrong architecture or incomplete
asset set blocks promotion. Ad-hoc candidates do not satisfy the stable gate.

## Local acceptance and rollback

Run `scripts/acceptance-macos.py --binary PATH --archive ZIP` against the exact
candidate. It reads live data in memory and writes only a private allowlisted
receipt. Empty messages/search/media are failures. Full archive coverage is
required. Existing legacy packages are only rollback candidates if their own
installer can start and read the preserved Keychain/config correctly.

Install/update/rollback/uninstall rehearsals must use a dedicated HOME and managed
install/bin/log directories; never purge the user's actual config, Keychain or
state to simulate a fresh machine. A same-host isolated installation is not proof
of a truly new Mac: a separate Mac/VM acceptance remains a separate receipt check.
Do not mark skipped or blocked checks passed. Preserve the current default release
and user's working installation until the corrected signed release is accepted.
