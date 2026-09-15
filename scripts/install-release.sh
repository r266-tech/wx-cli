#!/bin/zsh
set -euo pipefail

REPO="${WECHAT_CLI_RELEASE_REPO:-${WECHAT_CLI_REPO:-https://github.com/r266-tech/wechat-cli-releases}}"
TAG="${WECHAT_CLI_RELEASE_TAG:-latest}"
ASSET="${WECHAT_CLI_RELEASE_ASSET:-wechat-cli-latest-darwin-arm64.zip}"
KEEP_DOWNLOAD="${WECHAT_CLI_KEEP_DOWNLOAD:-0}"
JSON="${WECHAT_CLI_INSTALL_JSON:-0}"
ALLOW_UNSIGNED="${WECHAT_CLI_ALLOW_UNSIGNED:-0}"
ALLOW_PRERELEASE=0
MODE="install"
DRY_RUN=0
CLEANUP_DIR=""

typeset -a INSTALL_ARGS
INSTALL_ARGS=(--yes)
case "${WECHAT_CLI_WITH_ASR:-0}" in
  1|true|TRUE|yes|YES|on|ON)
    INSTALL_ARGS+=(--with-asr)
    ;;
esac

usage() {
  cat <<'EOF'
Usage:
  curl -fsSL https://github.com/r266-tech/wechat-cli-releases/releases/latest/download/install-release.sh | zsh
  ./scripts/install-release.sh [--dry-run] [--json] [--update] [--with-asr] [installer args...]
  ./scripts/install-release.sh --all [--json]   # install + first key bootstrap
  ./scripts/install-release.sh --repo r266-tech/wx-cli --tag v2.0.1-rc.5 --allow-prerelease

Environment:
  WECHAT_CLI_REPO             GitHub repo URL or owner/name. Default: r266-tech/wechat-cli-releases.
  WECHAT_CLI_RELEASE_TAG      GitHub release tag. Default: latest.
  WECHAT_CLI_RELEASE_ASSET    Release asset name. Default: wechat-cli-latest-darwin-arm64.zip.
  WECHAT_CLI_INSTALL_JSON     Pass --json to the bundled installer when set to 1.
  WECHAT_CLI_KEEP_DOWNLOAD    Keep the temporary download directory when set to 1.
  WECHAT_CLI_ALLOW_UNSIGNED   Allow a release without a .sha256 sidecar (development only).
EOF
}

say() {
  if [[ "$JSON" == "1" ]]; then
    print -r -- "$*" >&2
  else
    print -r -- "$*"
  fi
}

warn() {
  print -r -- "WARN: $*" >&2
}

fail() {
  print -r -- "ERROR: $*" >&2
  exit 1
}

have_cmd() {
  command -v "$1" >/dev/null 2>&1
}

repo_slug() {
  local repo="$1"
  repo="${repo#https://github.com/}"
  repo="${repo#http://github.com/}"
  repo="${repo%.git}"
  repo="${repo%/}"
  print -r -- "$repo"
}

repo_url() {
  local repo="$1"
  if [[ "$repo" == http://* || "$repo" == https://* ]]; then
    repo="${repo%.git}"
    print -r -- "${repo%/}"
  else
    repo="${repo%.git}"
    print -r -- "https://github.com/${repo%/}"
  fi
}

download_file() {
  local url="$1"
  local dest="$2"
  if have_cmd curl; then
    curl -fsSL --retry 3 --connect-timeout 15 -o "$dest" "$url"
  elif have_cmd wget; then
    wget -O "$dest" "$url"
  else
    fail "curl or wget is required to download wechat-cli."
  fi
}

cleanup_download() {
  [[ -n "${CLEANUP_DIR:-}" ]] || return
  rm -rf "$CLEANUP_DIR"
}

fetch_text() {
  local url="$1"
  if have_cmd curl; then
    curl -fsL --retry 3 --connect-timeout 15 "$url"
  elif have_cmd wget; then
    wget -qO- "$url"
  else
    fail "curl or wget is required to query GitHub releases."
  fi
}

asset_url() {
  local base="$1"
  local slug="$2"
  local tag="$3"
  local asset="$4"
  if [[ "$tag" == "latest" ]]; then
    print -r -- "$base/releases/latest/download/$asset"
  else
    print -r -- "$base/releases/download/$tag/$asset"
  fi
}

fallback_asset_url() {
  local slug="$1"
  local tag="$2"
  local api
  if [[ "$tag" == "latest" ]]; then
    api="https://api.github.com/repos/$slug/releases/latest"
  else
    api="https://api.github.com/repos/$slug/releases/tags/$tag"
  fi
  fetch_text "$api" \
    | sed -nE 's/.*"browser_download_url": "([^"]*darwin-arm64\.zip)".*/\1/p' \
    | grep -v '\.sha256$' \
    | head -n 1 \
    || true
}

verify_sha256() {
  local zip="$1"
  local sha_file="$2"
  [[ -f "$sha_file" ]] || fail "checksum file missing after successful download."
  have_cmd shasum || fail "shasum is required to verify a downloaded checksum file."
  local expected actual
  expected="$(awk '{print tolower($1); exit}' "$sha_file")"
  actual="$(shasum -a 256 "$zip" | awk '{print tolower($1)}')"
  [[ "${#expected}" -eq 64 ]] || fail "invalid sha256 file."
  case "$expected" in
    *[!0-9a-f]*) fail "invalid sha256 file." ;;
  esac
  [[ "$expected" == "$actual" ]] || fail "sha256 mismatch for downloaded release zip."
}

parse_args() {
  while [[ "$#" -gt 0 ]]; do
    case "$1" in
      --help|-h)
        usage
        exit 0
        ;;
      --dry-run)
        DRY_RUN=1
        INSTALL_ARGS+=(--dry-run)
        shift
        ;;
      --json)
        JSON=1
        shift
        ;;
      --allow-unsigned)
        ALLOW_UNSIGNED=1
        shift
        ;;
      --allow-prerelease)
        ALLOW_PRERELEASE=1
        shift
        ;;
      --update)
        MODE="update"
        INSTALL_ARGS+=(--update)
        shift
        ;;
      --tag)
        [[ "$#" -ge 2 ]] || fail "--tag requires a value."
        TAG="$2"
        shift 2
        ;;
      --tag=*)
        TAG="${1#*=}"
        shift
        ;;
      --asset)
        [[ "$#" -ge 2 ]] || fail "--asset requires a value."
        ASSET="$2"
        shift 2
        ;;
      --asset=*)
        ASSET="${1#*=}"
        shift
        ;;
      --repo)
        [[ "$#" -ge 2 ]] || fail "--repo requires a value."
        REPO="$2"
        shift 2
        ;;
      --repo=*)
        REPO="${1#*=}"
        shift
        ;;
      *)
        INSTALL_ARGS+=("$1")
        shift
        ;;
    esac
  done
  if [[ "$JSON" == "1" ]]; then
    INSTALL_ARGS+=(--json)
  fi
  if [[ "$DRY_RUN" -eq 1 && "$MODE" == "install" ]]; then
    :
  fi
}

main() {
  parse_args "$@"

  if [[ "$ALLOW_PRERELEASE" == "1" ]]; then
    [[ "$TAG" != "latest" && "$TAG" == v[0-9]*-* ]] || fail "--allow-prerelease requires an explicit prerelease --tag."
    say "Installing an explicitly selected preview; Apple notarization may be unavailable. Checksum and manifest verification remain required."
  fi

  [[ "$(uname -s)" == "Darwin" ]] || fail "this installer is for macOS; use scripts/install-release.ps1 on Windows."
  [[ "$(uname -m)" == "arm64" ]] || fail "this release installer supports macOS arm64 only."
  have_cmd unzip || fail "unzip is required."

  local slug base url tmp zip sha extract install_script install_dir fallback
  slug="$(repo_slug "$REPO")"
  base="$(repo_url "$REPO")"
  have_cmd python3 || fail "python3 is required for manifest validation."
  if [[ "$TAG" == "latest" ]]; then
    TAG="$(fetch_text "https://api.github.com/repos/$slug/releases/latest" | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"])')" || fail "release_endpoint_unavailable"
  fi
  [[ "$TAG" == v[0-9]* ]] || fail "invalid release tag"
  if [[ "$ASSET" == "wechat-cli-latest-darwin-arm64.zip" ]]; then
    ASSET="wechat-cli-${TAG}-darwin-arm64.zip"
  fi
  url="$(asset_url "$base" "$slug" "$TAG" "$ASSET")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/wechat-cli-install.XXXXXX")"
  if [[ "$KEEP_DOWNLOAD" != "1" ]]; then
    CLEANUP_DIR="$tmp"
    trap cleanup_download EXIT INT TERM
  else
    say "Keeping download directory: $tmp"
  fi

  zip="$tmp/$ASSET"
  sha="$tmp/$ASSET.sha256"
  extract="$tmp/extract"


  say "Downloading wechat-cli release: $url"
  if ! download_file "$url" "$zip"; then
    warn "stable asset download failed; querying GitHub release metadata."
    fallback="$(fallback_asset_url "$slug" "$TAG")"
    [[ -n "$fallback" ]] || fail "could not find a darwin-arm64 release asset for $slug."
    url="$fallback"
    zip="$tmp/${fallback:t}"
    say "Downloading wechat-cli release: $url"
    download_file "$url" "$zip"
  fi

  if download_file "$url.sha256" "$sha" >/dev/null 2>&1; then
    verify_sha256 "$zip" "$sha"
    say "Verified release checksum."
  else
    if [[ "$ALLOW_UNSIGNED" == "1" || "$ALLOW_UNSIGNED" == "true" || "$ALLOW_UNSIGNED" == "yes" ]]; then
      warn "release checksum file not found; continuing because unsigned installs were explicitly allowed."
    else
      fail "release checksum file not found; refusing unsigned release. Set WECHAT_CLI_ALLOW_UNSIGNED=1 only for development."
    fi
  fi

  # Embedded dependency-free verifier runs before any installer from the zip.
  python3 - "$zip" "$extract" "${TAG#v}" "$ALLOW_PRERELEASE" <<'VERIFY_RELEASE_PY'
"""Standalone bootstrap verifier. Kept dependency-free for embedding in installers."""
import hashlib
import json
import re
import stat
import sys
import zipfile
from pathlib import Path, PurePosixPath


def verify_and_extract(archive, destination, expected_version, allow_prerelease=False):
    dest=Path(destination)
    with zipfile.ZipFile(archive) as z:
        infos=z.infolist()
        if len(infos)>2000 or sum(i.file_size for i in infos)>256*1024*1024:raise ValueError('release archive budget exceeded')
        names=set();roots=set()
        for i in infos:
            p=PurePosixPath(i.filename)
            if p.is_absolute() or '..' in p.parts or '\\' in i.filename or ':' in i.filename or stat.S_ISLNK(i.external_attr>>16):raise ValueError('unsafe release path')
            if i.filename in names:raise ValueError('duplicate release path')
            names.add(i.filename);roots.add(p.parts[0])
        if len(roots)!=1:raise ValueError('expected one release root')
        prefix=roots.pop()+'/'
        d=json.loads(z.read(prefix+'release-manifest.json'))
        if d.get('schema_version')!=2 or d.get('source_repository')!='r266-tech/wx-cli':raise ValueError('manifest schema/source mismatch')
        if d.get('version')!=expected_version or d.get('platform_arch')!='darwin-arm64':raise ValueError('release version/platform mismatch')
        if not re.fullmatch('[0-9a-f]{40}',d.get('commit','')):raise ValueError('invalid source commit')
        preview = allow_prerelease in (True, '1', 'true')
        stable = d.get('channel')=='stable' and d.get('signing_mode')=='developer_id' and d.get('notarization_status')=='accepted'
        candidate = (preview and re.fullmatch(r'\d+\.\d+\.\d+-[0-9A-Za-z.-]+', expected_version)
                     and d.get('channel')=='candidate' and d.get('signing_mode') in ('adhoc','developer_id')
                     and d.get('notarization_status') in ('not_configured','accepted'))
        if not (stable or candidate):raise ValueError('release is not a verified stable package; preview requires an explicit prerelease tag and --allow-prerelease')
        files={i.filename[len(prefix):] for i in infos if not i.is_dir()}-{'release-manifest.json'}
        if files!=set(d['artifacts']):raise ValueError('manifest file set mismatch')
        for name,expected in d['artifacts'].items():
            raw=z.read(prefix+name)
            if len(raw)!=expected['bytes'] or hashlib.sha256(raw).hexdigest()!=expected['sha256']:raise ValueError('manifest artifact hash mismatch')
        if dest.exists():raise ValueError('destination must be new')
        dest.mkdir(parents=True,mode=0o700);z.extractall(dest)
        for i in infos:
            if not i.is_dir():(dest/i.filename).chmod(0o755 if (i.external_attr>>16)&0o111 else 0o644)
    return dest/prefix

if __name__=='__main__':print(verify_and_extract(*sys.argv[1:]))
VERIFY_RELEASE_PY
  [[ "$?" -eq 0 ]] || fail "release manifest verification failed"
  install_script="$(find "$extract" -maxdepth 3 -type f -name install.sh | head -n 1)"
  [[ -n "$install_script" ]] || fail "install.sh not found inside release zip"
  install_dir="${install_script:h}"

  say "Running bundled installer from $install_dir"
  ( cd "$install_dir" && ./install.sh "${INSTALL_ARGS[@]}" )
}

if [[ "${ZSH_EVAL_CONTEXT:-}" != *:file ]]; then
  main "$@"
fi
