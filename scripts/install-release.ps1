param(
  [switch]$DryRun,
  [switch]$Json,
  [switch]$Update,
  [string]$Repo = $env:WECHAT_CLI_REPO,
  [string]$Tag = $env:WECHAT_CLI_RELEASE_TAG,
  [string]$Asset = $env:WECHAT_CLI_RELEASE_ASSET,
  [string]$InstallDir = $env:WECHAT_CLI_INSTALL_DIR,
  [switch]$All,
  [switch]$WithASR,
  [switch]$BackgroundRefresh,
  [switch]$KeepDownload,
  [switch]$AllowUnsigned,
  [switch]$AllowPrerelease
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($Repo)) { $Repo = $env:WECHAT_CLI_RELEASE_REPO }
if ([string]::IsNullOrWhiteSpace($Repo)) { $Repo = "https://github.com/r266-tech/wechat-cli-releases" }
if ([string]::IsNullOrWhiteSpace($Tag)) { $Tag = "latest" }
if ([string]::IsNullOrWhiteSpace($Asset)) { $Asset = "wechat-cli-latest-windows-amd64.zip" }
if ($env:WECHAT_CLI_INSTALL_JSON -eq "1") { $Json = $true }
if ($env:WECHAT_CLI_KEEP_DOWNLOAD -eq "1") { $KeepDownload = $true }
if ($env:WECHAT_CLI_WITH_ASR -match '^(1|true|yes|on)$') { $WithASR = $true }
if ($env:WECHAT_CLI_ALLOW_UNSIGNED -match '^(1|true|yes|on)$') { $AllowUnsigned = $true }

function Write-Step([string]$Text) {
  if ($Json) {
    [Console]::Error.WriteLine($Text)
  } else {
    Write-Host $Text
  }
}

function Get-RepoSlug([string]$RepoValue) {
  $r = $RepoValue -replace '^https?://github.com/', ''
  $r = $r -replace '\.git$', ''
  return $r.TrimEnd('/')
}

function Get-RepoUrl([string]$RepoValue) {
  if ($RepoValue -match '^https?://') {
    return ($RepoValue -replace '\.git$', '').TrimEnd('/')
  }
  return "https://github.com/$($RepoValue -replace '\.git$', '')".TrimEnd('/')
}

function Get-AssetUrl([string]$Base, [string]$TagValue, [string]$AssetName) {
  if ($TagValue -eq "latest") {
    return "$Base/releases/latest/download/$AssetName"
  }
  return "$Base/releases/download/$TagValue/$AssetName"
}

function Get-FallbackAssetUrl([string]$Slug, [string]$TagValue) {
  if ($TagValue -eq "latest") {
    $api = "https://api.github.com/repos/$Slug/releases/latest"
  } else {
    $api = "https://api.github.com/repos/$Slug/releases/tags/$TagValue"
  }
  $release = Invoke-RestMethod -Uri $api -UseBasicParsing
  $match = $release.assets | Where-Object {
    $_.name -like "*windows-amd64.zip" -and $_.name -notlike "*.sha256"
  } | Select-Object -First 1
  if ($null -eq $match) { return "" }
  return $match.browser_download_url
}

function Save-Url([string]$Url, [string]$Path) {
  Invoke-WebRequest -Uri $Url -OutFile $Path -UseBasicParsing
}

function Test-Sha256([string]$ZipPath, [string]$ShaPath) {
  if (-not (Test-Path -LiteralPath $ShaPath)) { throw "checksum file missing after successful download" }
  $tokens = (Get-Content -LiteralPath $ShaPath -Raw) -split '\s+'
  $expected = [string]$tokens[0]
  if ($expected -notmatch '^[0-9a-fA-F]{64}$') {
    throw "invalid sha256 file"
  }
  $expected = $expected.ToLowerInvariant()
  $actual = (Get-FileHash -LiteralPath $ZipPath -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($expected -ne $actual) {
    throw "sha256 mismatch for downloaded release zip"
  }
}

function Confirm-ReleaseChecksum([string]$Url, [string]$ZipPath, [string]$ShaPath) {
  try {
    Save-Url "$Url.sha256" $ShaPath
  } catch {
    if ($AllowUnsigned) {
      Write-Warning "release checksum file not found; continuing because unsigned installs were explicitly allowed."
      return $false
    }
    throw "release checksum file not found; refusing unsigned release. Use -AllowUnsigned only for development."
  }

  # A downloaded checksum is an integrity assertion. Any parse error or
  # mismatch must abort instead of being treated like a missing sidecar.
  Test-Sha256 $ZipPath $ShaPath
  return $true
}

function Expand-VerifiedRelease([string]$ZipPath, [string]$Destination, [string]$ExpectedVersion, [bool]$Preview) {
  Add-Type -AssemblyName System.IO.Compression.FileSystem
  if (Test-Path -LiteralPath $Destination) { throw "extraction destination must be new" }
  $archive = [IO.Compression.ZipFile]::OpenRead($ZipPath)
  try {
    if ($archive.Entries.Count -gt 2000) { throw "release archive budget exceeded" }
    $entries = @{}
    $roots = @{}
    [long]$total = 0
    foreach ($entry in $archive.Entries) {
      $name = $entry.FullName
      $parts = $name.TrimEnd('/').Split('/')
      if ([string]::IsNullOrWhiteSpace($name) -or $name.Contains('\') -or $name.StartsWith('/') -or
          $name -match '[:\x00-\x1f]' -or ($parts | Where-Object { $_ -in @('', '.', '..') -or $_ -match '[. ]$' -or $_ -match '^(?i:CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)' }).Count -gt 0) {
        throw "unsafe release path"
      }
      if ((($entry.ExternalAttributes -shr 16) -band 0xF000) -eq 0xA000) { throw "release symlink is forbidden" }
      $key = $name.TrimEnd('/')
      if ($entries.ContainsKey($key)) { throw "duplicate release path" }
      $entries[$key] = $entry
      $roots[$parts[0]] = $true
      $total += $entry.Length
    }
    if ($total -gt 268435456) { throw "release archive budget exceeded" }
    if ($roots.Count -ne 1) { throw "expected one release root" }
    $prefix = @($roots.Keys)[0] + '/'
    $manifestEntry = $entries[$prefix + 'release-manifest.json']
    if ($null -eq $manifestEntry -or $manifestEntry.Length -gt 2097152) { throw "release manifest missing or too large" }
    $reader = New-Object IO.StreamReader($manifestEntry.Open())
    try { $manifest = $reader.ReadToEnd() | ConvertFrom-Json } finally { $reader.Dispose() }
    if ($manifest.schema_version -ne 2 -or $manifest.source_repository -cne 'r266-tech/wx-cli' -or
        $manifest.version -cne $ExpectedVersion -or $manifest.platform_arch -cne 'windows-amd64' -or
        $manifest.commit -cnotmatch '^[0-9a-f]{40}$') { throw "release manifest identity mismatch" }
    # Windows packages currently have no accepted signing/acceptance pipeline.
    # Keep them explicit previews instead of treating an unsigned ZIP as stable.
    if (-not $Preview -or $ExpectedVersion -notmatch '^\d+\.\d+\.\d+-[0-9A-Za-z.-]+$' -or
        $manifest.channel -cne 'candidate' -or $manifest.signing_mode -cne 'unsigned' -or
        $manifest.notarization_status -cne 'not_applicable') { throw "Windows candidate requires an explicit prerelease tag and -AllowPrerelease" }
    $artifacts = @{}
    foreach ($property in $manifest.artifacts.PSObject.Properties) {
      if ($artifacts.ContainsKey($property.Name)) { throw "duplicate manifest artifact" }
      $artifacts[$property.Name] = $property.Value
    }
    foreach ($required in @('wechat-cli.exe', 'libWCDB.dll', 'install.ps1')) {
      if (-not $artifacts.ContainsKey($required)) { throw "required release artifact missing" }
    }
    $files = @($archive.Entries | Where-Object { -not $_.FullName.EndsWith('/') -and $_.FullName -cne ($prefix + 'release-manifest.json') })
    if ($files.Count -ne $artifacts.Count) { throw "manifest file set mismatch" }
    foreach ($entry in $files) {
      $name = $entry.FullName.Substring($prefix.Length)
      $expected = $artifacts[$name]
      if ($null -eq $expected -or $expected.bytes -ne $entry.Length -or $expected.sha256 -cnotmatch '^[0-9a-f]{64}$') { throw "manifest artifact mismatch" }
      $stream = $entry.Open()
      $hasher = [Security.Cryptography.SHA256]::Create()
      try { $hash = [BitConverter]::ToString($hasher.ComputeHash($stream)).Replace('-', '').ToLowerInvariant() }
      finally { $hasher.Dispose(); $stream.Dispose() }
      if ($hash -cne $expected.sha256) { throw "manifest artifact hash mismatch" }
    }
  } finally { $archive.Dispose() }
  [IO.Compression.ZipFile]::ExtractToDirectory($ZipPath, $Destination)
  return Join-Path $Destination $prefix.TrimEnd('/')
}

function Invoke-ReleaseInstall {
  if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "this installer is for Windows; use scripts/install-release.sh on macOS"
  }
  $osArch = if (-not [string]::IsNullOrWhiteSpace($env:PROCESSOR_ARCHITEW6432)) {
    $env:PROCESSOR_ARCHITEW6432
  } else {
    $env:PROCESSOR_ARCHITECTURE
  }
  if (-not [Environment]::Is64BitOperatingSystem -or $osArch -ne "AMD64") {
    throw "this release installer supports Windows amd64 only"
  }

  $slug = Get-RepoSlug $Repo
  if ($slug -notmatch '^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$') { throw "invalid GitHub repository" }
  $base = Get-RepoUrl $Repo
  if ($AllowPrerelease -and ($Tag -eq 'latest' -or $Tag -notmatch '^v\d+\.\d+\.\d+-[0-9A-Za-z.-]+$')) { throw "-AllowPrerelease requires an explicit prerelease tag" }
  if ($Tag -eq 'latest') {
    $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$slug/releases/latest" -UseBasicParsing
    $Tag = $release.tag_name
  }
  if ($Tag -notmatch '^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { throw "invalid release tag" }
  if ($Asset -eq 'wechat-cli-latest-windows-amd64.zip') { $Asset = "wechat-cli-$Tag-windows-amd64.zip" }
  if ($Asset -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*\.zip$') { throw "invalid release asset filename" }
  $url = Get-AssetUrl $base $Tag $Asset
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("wechat-cli-install-" + [Guid]::NewGuid().ToString("N"))
  $extract = Join-Path $tmp "extract"
  New-Item -ItemType Directory -Force -Path $tmp | Out-Null

  try {
    $zip = Join-Path $tmp $Asset
    $sha = Join-Path $tmp "$Asset.sha256"

    Write-Step "Downloading wechat-cli release: $url"
    try {
      Save-Url $url $zip
    } catch {
      Write-Warning "stable asset download failed; querying GitHub release metadata."
      $fallback = Get-FallbackAssetUrl $slug $Tag
      if ([string]::IsNullOrWhiteSpace($fallback)) {
        throw "could not find a windows-amd64 release asset for $slug"
      }
      $url = $fallback
      $zip = Join-Path $tmp (Split-Path ([Uri]$fallback).AbsolutePath -Leaf)
      Write-Step "Downloading wechat-cli release: $url"
      Save-Url $url $zip
    }

    if (Confirm-ReleaseChecksum $url $zip $sha) {
      Write-Step "Verified release checksum."
    }

    $packageRoot = Expand-VerifiedRelease $zip $extract ($Tag.Substring(1)) ([bool]$AllowPrerelease)
    $installer = Join-Path $packageRoot 'install.ps1'
    if (-not (Test-Path -LiteralPath $installer -PathType Leaf)) {
      throw "install.ps1 not found inside release zip"
    }

    $installerArgs = @()
    if ($Update) {
      $installerArgs += "-Update"
    }
    if ($All) {
      $installerArgs += "-All"
    }
    if ($WithASR) {
      $installerArgs += "-WithASR"
    }
    $installerArgs += "-Yes"
    if ($DryRun) { $installerArgs += "-DryRun" }
    if ($Json) { $installerArgs += "-Json" }
    if (-not [string]::IsNullOrWhiteSpace($InstallDir)) {
      $installerArgs += @("-InstallDir", $InstallDir)
    }
    if ($BackgroundRefresh) { $installerArgs += "-BackgroundRefresh" }

    Write-Step "Running bundled installer from $packageRoot"
    & powershell -NoProfile -ExecutionPolicy Bypass -File $installer @installerArgs
    if ($LASTEXITCODE -ne 0) {
      exit $LASTEXITCODE
    }
  } finally {
    if ($KeepDownload) {
      Write-Step "Keeping download directory: $tmp"
    } elseif (Test-Path -LiteralPath $tmp) {
      Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
  }
}

if ($MyInvocation.InvocationName -ne ".") {
  Invoke-ReleaseInstall
}
