package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	releaseInstallShellURL      = "https://github.com/r266-tech/wechat-cli-releases/releases/latest/download/install-release.sh"
	releaseInstallPowerShellURL = "https://github.com/r266-tech/wechat-cli-releases/releases/latest/download/install-release.ps1"
)

type updateOptions struct {
	DryRun          bool
	KeepDownload    bool
	Repo            string
	Tag             string
	Asset           string
	InstallDir      string
	AllowPrerelease bool
}

func runUpdateCLI(args []string, opts cliOptions) {
	if opts.StrictReadOnly || strictReadOnlyMode() {
		exitCLIError(opts, 1, "strict_read_only_blocked", "update downloads and installs local files; rerun without strict read-only mode", "update", "update")
		return
	}
	updateOpts, err := parseUpdateArgs(args)
	if err != nil {
		exitCLIError(opts, 2, "invalid_argument", err.Error(), "update", "update")
	}

	data, err := runReleaseUpdate(updateOpts)
	if err != nil {
		exitCLIError(opts, 1, "update_failed", err.Error(), "update", "update")
	}
	writeCLISuccess("update", "update", data, opts)
}

func parseUpdateArgs(args []string) (updateOptions, error) {
	var opts updateOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dry-run":
			opts.DryRun = true
		case arg == "--keep-download":
			opts.KeepDownload = true
		case arg == "--allow-prerelease":
			opts.AllowPrerelease = true
		case arg == "--repo":
			v, err := updateArgValue(args, &i, arg)
			if err != nil {
				return opts, err
			}
			opts.Repo = v
		case strings.HasPrefix(arg, "--repo="):
			opts.Repo = strings.TrimSpace(strings.TrimPrefix(arg, "--repo="))
		case arg == "--tag":
			v, err := updateArgValue(args, &i, arg)
			if err != nil {
				return opts, err
			}
			opts.Tag = v
		case strings.HasPrefix(arg, "--tag="):
			opts.Tag = strings.TrimSpace(strings.TrimPrefix(arg, "--tag="))
		case arg == "--asset":
			v, err := updateArgValue(args, &i, arg)
			if err != nil {
				return opts, err
			}
			opts.Asset = v
		case strings.HasPrefix(arg, "--asset="):
			opts.Asset = strings.TrimSpace(strings.TrimPrefix(arg, "--asset="))
		default:
			return opts, fmt.Errorf("unknown update argument %q", arg)
		}
	}
	if opts.Repo == "" {
		opts.Repo = firstNonEmpty(envFirst("WECHAT_CLI_RELEASE_REPO", "WECHAT_CLI_REPO", "WX_MCP_REPO"), "r266-tech/wechat-cli-releases")
	}
	if opts.Tag == "" {
		opts.Tag = envFirst("WECHAT_CLI_RELEASE_TAG")
	}
	if opts.Repo != "" {
		opts.Repo = strings.TrimSuffix(strings.TrimPrefix(opts.Repo, "https://github.com/"), "/")
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`).MatchString(opts.Repo) {
			return opts, fmt.Errorf("--repo must be a GitHub owner/repository or https://github.com/owner/repository")
		}
	}
	if opts.Tag != "" && opts.Tag != "latest" && !regexp.MustCompile(`^v[0-9][0-9A-Za-z.+-]*$`).MatchString(opts.Tag) {
		return opts, fmt.Errorf("invalid release tag")
	}
	if opts.AllowPrerelease && (!strings.Contains(opts.Tag, "-") || opts.Tag == "latest") {
		return opts, fmt.Errorf("--allow-prerelease requires an explicit prerelease --tag")
	}
	if opts.AllowPrerelease && runtime.GOOS == "windows" {
		return opts, fmt.Errorf("prerelease bootstrap currently supports macOS only")
	}
	return opts, nil
}

func updateArgValue(args []string, idx *int, name string) (string, error) {
	if *idx+1 >= len(args) {
		return "", fmt.Errorf("%s requires a value", name)
	}
	*idx = *idx + 1
	v := strings.TrimSpace(args[*idx])
	if v == "" {
		return "", fmt.Errorf("%s requires a non-empty value", name)
	}
	return v, nil
}

func runReleaseUpdate(opts updateOptions) (map[string]any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("strict_read_only_blocked: update writes local files")
	}
	if strings.TrimSpace(opts.InstallDir) == "" {
		installDir, err := currentExecutableInstallDir()
		if err != nil {
			return nil, fmt.Errorf("resolve current install directory: %w", err)
		}
		opts.InstallDir = installDir
	}
	switch runtime.GOOS {
	case "darwin":
		return runDarwinReleaseUpdate(opts)
	case "windows":
		return startWindowsReleaseUpdate(opts)
	default:
		return nil, fmt.Errorf("wechat-cli update supports macOS and Windows releases only, not %s", runtime.GOOS)
	}
}

func currentExecutableInstallDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return installDirFromExecutable(exe)
}

func installDirFromExecutable(exe string) (string, error) {
	exe = strings.TrimSpace(exe)
	if exe == "" {
		return "", fmt.Errorf("executable path is empty")
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(abs); resolveErr == nil {
		abs = resolved
	}
	return filepath.Dir(filepath.Clean(abs)), nil
}

func runDarwinReleaseUpdate(opts updateOptions) (map[string]any, error) {
	data := updateResultBase(opts)
	data["status"] = "running"
	data["background"] = false

	cmd := exec.Command("/bin/zsh", darwinUpdateCommandArgs(opts)...)
	cmd.Env = updateCommandEnv(os.Environ(), opts)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	attachUpdaterOutput(data, stdout.Bytes(), stderr.Bytes())
	if err != nil {
		return nil, fmt.Errorf("release updater failed: %w%s", err, updateFailureHint(stdout.String(), stderr.String()))
	}
	data["status"] = "completed"
	if opts.DryRun {
		data["next_action"] = "Dry run only; rerun wechat-cli update without --dry-run to apply changes."
	} else {
		data["next_action"] = "Run wechat-cli sessions to verify the updated install."
	}
	return data, nil
}

func darwinUpdateCommandArgs(opts updateOptions) []string {
	script := `set -euo pipefail
url="$1"
shift
curl -fsSL "$url" | env WECHAT_CLI_INSTALL_JSON=1 zsh -s -- "$@"`
	args := []string{"-c", script, "wechat-cli-update", updateBootstrapURL(opts, "install-release.sh")}
	args = append(args, releaseInstallerArgs(opts)...)
	return args
}

func releaseInstallerArgs(opts updateOptions) []string {
	args := []string{"--update"}
	if opts.AllowPrerelease {
		args = append(args, "--allow-prerelease")
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	if opts.Repo != "" {
		args = append(args, "--repo", opts.Repo)
	}
	if opts.Tag != "" {
		args = append(args, "--tag", opts.Tag)
	}
	if opts.Asset != "" {
		args = append(args, "--asset", opts.Asset)
	}
	if opts.InstallDir != "" {
		args = append(args, "--install-dir", opts.InstallDir)
	}
	return args
}

func updateBootstrapURL(opts updateOptions, name string) string {
	repo := strings.TrimSuffix(strings.TrimPrefix(opts.Repo, "https://github.com/"), "/")
	if repo == "" {
		repo = "r266-tech/wechat-cli-releases"
	}
	base := "https://github.com/" + repo + "/releases/"
	if opts.Tag == "" || opts.Tag == "latest" {
		return base + "latest/download/" + name
	}
	return base + "download/" + opts.Tag + "/" + name
}

func startWindowsReleaseUpdate(opts updateOptions) (map[string]any, error) {
	data := updateResultBase(opts)
	logPath, err := updateLogPath()
	if err != nil {
		return nil, err
	}
	scriptPath, err := writeWindowsUpdateScript()
	if err != nil {
		return nil, err
	}

	args := []string{
		"-NoProfile",
		"-ExecutionPolicy", "Bypass",
		"-File", scriptPath,
		"-ParentPid", strconv.Itoa(os.Getpid()),
		"-Url", updateBootstrapURL(opts, "install-release.ps1"),
		"-LogPath", logPath,
		"-InstallDir", opts.InstallDir,
	}
	if opts.DryRun {
		args = append(args, "-DryRun")
	}
	if opts.KeepDownload {
		args = append(args, "-KeepDownload")
	}
	if opts.Repo != "" {
		args = append(args, "-Repo", opts.Repo)
	}
	if opts.Tag != "" {
		args = append(args, "-Tag", opts.Tag)
	}
	if opts.Asset != "" {
		args = append(args, "-Asset", opts.Asset)
	}

	cmd, err := startPowerShell(args)
	if err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	data["status"] = "started"
	data["background"] = true
	data["pid"] = pid
	data["log"] = logPath
	data["script"] = scriptPath
	data["next_action"] = "Wait for the updater to finish, then run wechat-cli sessions. Agents should inspect data.log if verification fails."
	return data, nil
}

func startPowerShell(args []string) (*exec.Cmd, error) {
	var errs []error
	for _, name := range []string{"powershell.exe", "pwsh.exe"} {
		cmd := exec.Command(name, args...)
		if err := cmd.Start(); err == nil {
			return cmd, nil
		} else {
			errs = append(errs, err)
		}
	}
	return nil, errors.Join(errs...)
}

func updateCommandEnv(env []string, opts updateOptions) []string {
	env = upsertEnv(env, "WECHAT_CLI_INSTALL_JSON", "1")
	if opts.KeepDownload {
		env = upsertEnv(env, "WECHAT_CLI_KEEP_DOWNLOAD", "1")
	}
	return env
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func updateResultBase(opts updateOptions) map[string]any {
	query := map[string]any{
		"command":  "update",
		"platform": runtime.GOOS,
		"method":   "release_bootstrap",
		"dry_run":  opts.DryRun,
	}
	if opts.Repo != "" {
		query["repo"] = opts.Repo
	}
	if opts.Tag != "" {
		query["tag"] = opts.Tag
	}
	if opts.Asset != "" {
		query["asset"] = opts.Asset
	}
	if opts.KeepDownload {
		query["keep_download"] = true
	}
	if opts.InstallDir != "" {
		query["install_dir"] = opts.InstallDir
	}
	return map[string]any{"query": query}
}

func attachUpdaterOutput(data map[string]any, stdout, stderr []byte) {
	if parsed, ok := parseJSONOutput(stdout); ok {
		data["installer"] = parsed
	} else if s := strings.TrimSpace(string(stdout)); s != "" {
		data["stdout"] = truncateUpdateOutput(s)
	}
	if s := strings.TrimSpace(string(stderr)); s != "" && envBoolAny("WECHAT_CLI_UPDATE_DEBUG") {
		data["stderr"] = truncateUpdateOutput(s)
	}
}

func parseJSONOutput(raw []byte) (any, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	return v, true
}

func truncateUpdateOutput(s string) string {
	const limit = 8000
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n... truncated ..."
}

func updateFailureHint(stdout, stderr string) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = strings.TrimSpace(stdout)
	}
	if msg == "" {
		return ""
	}
	return ": " + truncateUpdateOutput(msg)
}

func updateLogPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || strings.TrimSpace(base) == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "wechat-cli", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("update-%s.log", time.Now().Format("20060102-150405"))), nil
}

func writeWindowsUpdateScript() (string, error) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("wechat-cli-update-%d.ps1", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(windowsReleaseUpdateScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

const windowsReleaseUpdateScript = `
param(
  [int]$ParentPid,
  [string]$Url,
  [string]$LogPath,
  [string]$Repo = "",
  [string]$Tag = "",
  [string]$Asset = "",
  [string]$InstallDir = "",
  [switch]$DryRun,
  [switch]$KeepDownload
)

$ErrorActionPreference = "Stop"
New-Item -ItemType Directory -Force -Path (Split-Path -Parent $LogPath) | Out-Null

function Write-UpdateLog([string]$Text) {
  Add-Content -LiteralPath $LogPath -Value ("[{0}] {1}" -f (Get-Date).ToString("s"), $Text)
}

try {
  Write-UpdateLog "wechat-cli update started"
  if ($ParentPid -gt 0) {
    try {
      Wait-Process -Id $ParentPid -Timeout 120 -ErrorAction SilentlyContinue
    } catch {
    }
  }

  $bootstrap = Join-Path ([IO.Path]::GetTempPath()) ("wechat-cli-install-release-" + [Guid]::NewGuid().ToString("N") + ".ps1")
  Write-UpdateLog "Downloading bootstrap: $Url"
  Invoke-WebRequest -Uri $Url -OutFile $bootstrap -UseBasicParsing

  $installArgs = @("-NoProfile", "-ExecutionPolicy", "Bypass", "-File", $bootstrap, "-Update", "-Json")
  if ($DryRun) { $installArgs += "-DryRun" }
  if ($KeepDownload) { $installArgs += "-KeepDownload" }
  if (-not [string]::IsNullOrWhiteSpace($Repo)) { $installArgs += @("-Repo", $Repo) }
  if (-not [string]::IsNullOrWhiteSpace($Tag)) { $installArgs += @("-Tag", $Tag) }
  if (-not [string]::IsNullOrWhiteSpace($Asset)) { $installArgs += @("-Asset", $Asset) }
  if (-not [string]::IsNullOrWhiteSpace($InstallDir)) { $installArgs += @("-InstallDir", $InstallDir) }

  Write-UpdateLog "Running release updater"
  & powershell @installArgs 2>&1 | Tee-Object -FilePath $LogPath -Append | Out-Null
  $code = $LASTEXITCODE
  Write-UpdateLog "release updater exited with code $code"
  exit $code
} catch {
  Write-UpdateLog ("ERROR: " + $_.Exception.Message)
  exit 1
}
`
