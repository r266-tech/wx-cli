param(
  [string]$WcdbVersion = "2.1.16",
  [string]$OutDir = (Join-Path (Resolve-Path (Join-Path $PSScriptRoot "..")).Path "lib"),
  [string]$WorkDir = (Join-Path ([System.IO.Path]::GetTempPath()) "wechat-cli-wcdb-build"),
  [switch]$KeepWorkDir
)

$ErrorActionPreference = "Stop"

if ($WcdbVersion -notmatch '^v?\d+\.\d+\.\d+$') {
  throw "WcdbVersion must be a numeric release version such as 2.1.16"
}

$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd("\")
$normalizedWorkDir = [IO.Path]::GetFullPath($WorkDir).TrimEnd("\")
if ($normalizedWorkDir -ieq $tempRoot -or -not $normalizedWorkDir.StartsWith($tempRoot + "\", [StringComparison]::OrdinalIgnoreCase)) {
  throw "WorkDir must be a dedicated child of the system temporary directory: $WorkDir"
}
$cursor = $normalizedWorkDir
while ($cursor -ine $tempRoot) {
  if (Test-Path -LiteralPath $cursor) {
    $workItem = Get-Item -LiteralPath $cursor -Force
    if (($workItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
      throw "WorkDir and its parents below the temp root must not be symlinks or junctions: $WorkDir"
    }
  }
  $parent = [IO.Path]::GetDirectoryName($cursor)
  if ([string]::IsNullOrWhiteSpace($parent) -or $parent -ieq $cursor) {
    throw "WorkDir could not be safely resolved below the system temporary directory: $WorkDir"
  }
  $cursor = $parent.TrimEnd("\")
}
$WorkDir = $normalizedWorkDir

function Find-WcdbDll {
  param([string]$BuildDir)

  $candidates = Get-ChildItem -LiteralPath $BuildDir -Recurse -File -Filter "WCDB.dll" |
    Where-Object { $_.FullName -match "\\Release\\" } |
    Sort-Object Length -Descending
  if (-not $candidates) {
    $candidates = Get-ChildItem -LiteralPath $BuildDir -Recurse -File -Filter "WCDB.dll" |
      Sort-Object Length -Descending
  }
  if (-not $candidates) {
    throw "WCDB.dll was not produced under $BuildDir"
  }
  return $candidates[0].FullName
}

if (-not (Get-Command cmake -ErrorAction SilentlyContinue)) {
  throw "CMake is required to build WCDB.dll"
}

$versionNoPrefix = $WcdbVersion -replace '^v', ''
$sourceRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$lock = Get-Content -LiteralPath (Join-Path $sourceRoot "release-dependencies.json") -Raw | ConvertFrom-Json
if ($lock.schema_version -ne 1 -or $versionNoPrefix -ne $lock.wcdb.version -or $lock.wcdb.archive_sha256 -notmatch '^[0-9a-f]{64}$') {
  throw "WCDB version must match the pinned release-dependencies.json source"
}
$sourceUrl = $lock.wcdb.source_url
$archive = Join-Path $WorkDir "wcdb-$versionNoPrefix.zip"
$extractRoot = Join-Path $WorkDir "src"
$buildDir = Join-Path $WorkDir "build"

if (Test-Path $WorkDir) {
  Remove-Item -LiteralPath $WorkDir -Recurse -Force
}
New-Item -ItemType Directory -Force -Path $WorkDir, $extractRoot, $OutDir | Out-Null

try {
  Invoke-WebRequest -Uri $sourceUrl -OutFile $archive
  if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ine $lock.wcdb.archive_sha256) {
    throw "WCDB source checksum mismatch"
  }
  Expand-Archive -LiteralPath $archive -DestinationPath $extractRoot -Force

  $sourceDir = Join-Path $extractRoot "wcdb-$versionNoPrefix\src"
  if (-not (Test-Path $sourceDir)) {
    throw "WCDB source directory not found: $sourceDir"
  }

  $sqliteExportFlag = "/DSQLITE_API=__declspec(dllexport)"
  cmake -S $sourceDir -B $buildDir -G "Visual Studio 17 2022" -A x64 -DBUILD_SHARED_LIBS=ON -DCMAKE_MSVC_RUNTIME_LIBRARY=MultiThreaded "-DCMAKE_C_FLAGS=$sqliteExportFlag"
  if ($LASTEXITCODE -ne 0) { throw "cmake configure failed" }

  cmake --build $buildDir --config Release --target WCDB --parallel
  if ($LASTEXITCODE -ne 0) { throw "cmake build failed" }

  $dll = Find-WcdbDll -BuildDir $buildDir
  $dest = Join-Path $OutDir "libWCDB.dll"
  Copy-Item -LiteralPath $dll -Destination $dest -Force
  $evidence = [ordered]@{
    repository = $lock.wcdb.repository
    version = $versionNoPrefix
    source_url = $sourceUrl
    archive_sha256 = $lock.wcdb.archive_sha256
    commit_sha = $lock.wcdb.commit_sha
    build_platform = "windows-amd64"
    build_flags = @("-G", "Visual Studio 17 2022", "-A", "x64", "-DBUILD_SHARED_LIBS=ON", "-DCMAKE_MSVC_RUNTIME_LIBRARY=MultiThreaded", "-DCMAKE_C_FLAGS=$sqliteExportFlag")
    binary_sha256 = (Get-FileHash -LiteralPath $dest -Algorithm SHA256).Hash.ToLowerInvariant()
  }
  $evidence | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath "$dest.build-inputs.json" -Encoding UTF8

  [ordered]@{
    wcdb_version = $versionNoPrefix
    source_url = $sourceUrl
    dll = $dest
    bytes = (Get-Item -LiteralPath $dest).Length
  } | ConvertTo-Json -Depth 3
} finally {
  if (-not $KeepWorkDir -and (Test-Path $WorkDir)) {
    Remove-Item -LiteralPath $WorkDir -Recurse -Force -ErrorAction SilentlyContinue
  }
}
