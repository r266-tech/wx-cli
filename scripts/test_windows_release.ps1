# Local fixture regression: no downloads, production installation, or user state.
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'install-release.ps1')
Add-Type -AssemblyName System.IO.Compression.FileSystem
Add-Type -AssemblyName System.IO.Compression
$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('wx-release-test-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null

function Get-TestHash([byte[]]$Bytes) {
  $hasher = [Security.Cryptography.SHA256]::Create()
  try { return [BitConverter]::ToString($hasher.ComputeHash($Bytes)).Replace('-', '').ToLowerInvariant() }
  finally { $hasher.Dispose() }
}

function New-TestArchive([string]$Path, [string]$Mutation) {
  $files = [ordered]@{'wechat-cli.exe' = 'CLI fixture'; 'libWCDB.dll' = 'WCDB fixture'; 'install.ps1' = 'throw "must never run"'}
  $artifacts = [ordered]@{}
  foreach ($name in $files.Keys) {
    $bytes = [Text.Encoding]::UTF8.GetBytes($files[$name])
    $artifacts[$name] = @{bytes = $bytes.Length; sha256 = (Get-TestHash $bytes)}
  }
  $manifest = @{schema_version = 2; source_repository = 'r266-tech/wx-cli'; version = '2.0.1-rc.5'; commit = ('a' * 40);
    platform_arch = 'windows-amd64'; channel = 'candidate'; signing_mode = 'unsigned'; notarization_status = 'not_applicable'; artifacts = $artifacts}
  if ($Mutation -eq 'hash') { $artifacts['install.ps1'].sha256 = ('0' * 64) }
  if ($Mutation -eq 'identity') { $manifest.platform_arch = 'darwin-arm64' }
  if ($Mutation -eq 'missing-file') { $files.Remove('wechat-cli.exe') }
  $archive = [System.IO.Compression.ZipFile]::Open($Path, [System.IO.Compression.ZipArchiveMode]::Create)
  try {
    foreach ($name in $files.Keys) {
      $entry = $archive.CreateEntry('pkg/' + $name)
      $writer = New-Object IO.StreamWriter($entry.Open())
      try { $writer.Write($files[$name]) } finally { $writer.Dispose() }
    }
    if ($Mutation -ne 'missing-manifest') {
      $entry = $archive.CreateEntry('pkg/release-manifest.json')
      $writer = New-Object IO.StreamWriter($entry.Open())
      try { $writer.Write(($manifest | ConvertTo-Json -Depth 8)) } finally { $writer.Dispose() }
    }
    $extra = switch ($Mutation) {
      'traversal' { 'pkg/../escaped' }
      'duplicate' { 'pkg/INSTALL.PS1' }
      'reserved' { 'pkg/CON.txt' }
      'alias' { 'pkg/./install.ps1' }
      'ads' { 'pkg/other:stream' }
    }
    if ($extra) { [void]$archive.CreateEntry($extra) }
  } finally { $archive.Dispose() }
}

try {
  foreach ($mutation in @('none', 'no-opt-in', 'hash', 'identity', 'missing-file', 'missing-manifest', 'traversal', 'duplicate', 'reserved', 'alias', 'ads')) {
    $archive = Join-Path $testRoot ($mutation + '.zip')
    $destination = Join-Path $testRoot $mutation
    New-TestArchive $archive $mutation
    $failure = $null
    try { $package = Expand-VerifiedRelease $archive $destination '2.0.1-rc.5' ($mutation -ne 'no-opt-in') }
    catch { $failure = $_.Exception.Message }
    if ($mutation -eq 'none') {
      if ($failure -or -not (Test-Path -LiteralPath (Join-Path $package 'install.ps1'))) { throw "valid fixture failed: $failure" }
    } else {
      if (-not $failure) { throw "invalid archive was accepted: $mutation" }
      if (Test-Path -LiteralPath $destination) { throw "invalid archive wrote files: $mutation" }
    }
  }
  # Load only the pure path predicate; do not execute install.ps1's entry point.
  $tokens = $null; $parseErrors = $null
  $ast = [Management.Automation.Language.Parser]::ParseFile((Join-Path $PSScriptRoot '../install.ps1'), [ref]$tokens, [ref]$parseErrors)
  if ($parseErrors.Count) { throw 'installer syntax invalid' }
  $predicate = $ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Test-ProcessInInstallRoots'}, $true)
  . ([ScriptBlock]::Create($predicate.Extent.Text))
  if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
    if (-not (Test-ProcessInInstallRoots 'C:\fixture\install\wechat-cli.exe' @('C:\fixture\install'))) { throw 'managed process not matched' }
    if (Test-ProcessInInstallRoots 'C:\fixture\install-other\unrelated.exe' @('C:\fixture\install')) { throw 'neighbor process matched' }
    if (Test-ProcessInInstallRoots '' @('C:\fixture\install')) { throw 'empty process path matched' }
  }
  # Invoke only component installation against text fixtures, with a one-shot
  # replacement failure. No PE file, bootstrap, cache, or WeChat process runs.
  foreach ($definition in $ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]}, $true)) {
    . ([ScriptBlock]::Create($definition.Extent.Text))
  }
  $AppName = 'wechat-cli'; $InstallMarker = '.wechat-cli-install'
  $actions = New-Object System.Collections.Generic.List[string]
  $warnings = New-Object System.Collections.Generic.List[string]
  $DryRun = $false
  $savedWcdb = $env:WECHAT_CLI_WCDB_LIB
  foreach ($scenario in @('success', 'failed-commit')) {
    $SourceDir = Join-Path $testRoot ('source-' + $scenario)
    $InstallDir = Join-Path $testRoot ('installed-' + $scenario)
    New-Item -ItemType Directory -Path $SourceDir, $InstallDir | Out-Null
    $names = @('wechat-cli.exe', 'libWCDB.dll', 'release-manifest.json', 'install.ps1', $InstallMarker)
    foreach ($name in $names) {
      [IO.File]::WriteAllText((Join-Path $InstallDir $name), "old $name")
      if ($name -ne $InstallMarker) { [IO.File]::WriteAllText((Join-Path $SourceDir $name), "new $name") }
    }
    $env:WECHAT_CLI_WCDB_LIB = Join-Path $SourceDir 'libWCDB.dll'
    $script:injectMoveFailure = $scenario -eq 'failed-commit'
    function Move-Item {
      param([string]$LiteralPath, [string]$Destination, [switch]$Force)
      if ($script:injectMoveFailure -and $LiteralPath -match '[/\\]new[/\\]wechat-cli\.exe$') {
        $script:injectMoveFailure = $false
        throw 'injected component replacement failure'
      }
      Microsoft.PowerShell.Management\Move-Item -LiteralPath $LiteralPath -Destination $Destination -Force:$Force
    }
    $failure = $null
    try { Install-Components } catch { $failure = $_.Exception.Message }
    if ($scenario -eq 'success') {
      if ($failure) { throw "component fixture failed: $failure" }
      foreach ($name in $names | Where-Object { $_ -ne $InstallMarker }) {
        if ([IO.File]::ReadAllText((Join-Path $InstallDir $name)) -ne "new $name") { throw "component was not installed: $name" }
      }
    } else {
      if (-not $failure) { throw 'component failure injection was not exercised' }
      foreach ($name in $names) {
        if ([IO.File]::ReadAllText((Join-Path $InstallDir $name)) -ne "old $name") { throw "rollback failed: $name" }
      }
    }
    if (Get-ChildItem -LiteralPath $testRoot -Directory -Filter '.wechat-cli-stage-*' -Force) { throw 'staging directory leaked' }
  }
  Remove-Item Function:Move-Item
  [Environment]::SetEnvironmentVariable('WECHAT_CLI_WCDB_LIB', $savedWcdb, 'Process')

  $sourceCode = Join-Path $SourceDir 'cmd/wechat-cli'
  New-Item -ItemType Directory -Force -Path $sourceCode | Out-Null
  [IO.File]::WriteAllText((Join-Path $sourceCode 'main.go'), 'source fixture')
  if ((Resolve-WechatCli).Mode -ne 'build') { throw 'source checkout selected a stale existing executable' }

  # Exercise the actual error handler with only the platform gate replaced.
  $installerText = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '../install.ps1'))
  $platformGate = $ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Assert-SupportedWindowsPlatform'}, $true)
  $installerText = $installerText.Replace($platformGate.Extent.Text, 'function Assert-SupportedWindowsPlatform {}')
  $drySource = Join-Path $testRoot 'dry-run-source'; New-Item -ItemType Directory -Path $drySource | Out-Null
  $dryScript = Join-Path $drySource 'install.ps1'; [IO.File]::WriteAllText($dryScript, $installerText)
  $dryInstall = Join-Path $testRoot 'dry-run-install'
  $engine = (Get-Process -Id $PID).Path
  $dryResult = & $engine -NoProfile -NonInteractive -File $dryScript -DryRun -Yes -Json -InstallDir $dryInstall -BinDir (Join-Path $testRoot 'dry-run-bin')
  if ($LASTEXITCODE -eq 0 -or ($dryResult | ConvertFrom-Json).status -ne 'blocked') { throw 'dry-run failure fixture did not fail as expected' }
  if (Test-Path -LiteralPath $dryInstall) { throw 'failed dry run created install directory or logs' }
  # The dry-run fixture intentionally exits non-zero; do not leak that expected
  # native exit code into the enclosing PowerShell process.
  $global:LASTEXITCODE = 0
  Write-Output 'Windows fixtures passed: 11 archive cases, component success/rollback, source build priority, and failed dry-run isolation.'
} finally {
  Remove-Item -LiteralPath $testRoot -Recurse -Force
}
