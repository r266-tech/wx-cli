package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestUpdaterBootstrapUsesRequestedRepositoryAndTag(t *testing.T) {
	opts, err := parseUpdateArgs([]string{"--repo", "https://github.com/r266-tech/wx-cli", "--tag", "v2.0.1-rc.5"})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/r266-tech/wx-cli/releases/download/v2.0.1-rc.5/install-release.sh"
	if !slices.Contains(darwinUpdateCommandArgs(opts), want) {
		t.Fatal("updater downloaded bootstrap from a different release")
	}
}

func TestInstallerSuccessRequiresOneMatchingResult(t *testing.T) {
	for _, fixture := range []struct {
		raw    string
		dryRun bool
		ok     bool
	}{
		{`{"ok":true,"status":"ok"}`, false, true},
		{`{"ok":true,"status":"dry_run"}`, true, true},
		{``, false, false},
		{`{"ok":false,"status":"blocked"}`, false, false},
		{`{"ok":true,"status":"ok"} {"ok":false}`, false, false},
		{`{"ok":true,"status":"ok"} trailing output`, false, false},
		{`{"ok":true}`, false, false},
		{`{"ok":true,"status":"blocked"}`, false, false},
		{`{"ok":true,"status":"dry_run"}`, false, false},
		{`{"ok":true,"status":"ok"}`, true, false},
	} {
		if err := validateInstallerSuccess([]byte(fixture.raw), fixture.dryRun); (err == nil) != fixture.ok {
			t.Fatalf("raw=%q dryRun=%v err=%v", fixture.raw, fixture.dryRun, err)
		}
	}
}

func TestDarwinUpdaterRejectsEmptySuccessfulBootstrap(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS updater subprocess")
	}
	dir := t.TempDir()
	// A successful empty HTTP response must not report a completed install.
	if err := os.WriteFile(filepath.Join(dir, "curl"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := runDarwinReleaseUpdate(updateOptions{InstallDir: filepath.Join(dir, "install")}); err == nil {
		t.Fatal("empty bootstrap reported a completed update")
	}
}

func TestPreviewUpdateRequiresExplicitPrerelease(t *testing.T) {
	for _, args := range [][]string{{"--allow-prerelease"}, {"--allow-prerelease", "--tag", "latest"}, {"--allow-prerelease", "--tag", "v2.0.1"}, {"--repo", "https://example.com/x/y"}, {"--tag", "v2.0.1/../../x"}, {"--asset", "../../outside.zip"}, {"--asset", `C:\outside.zip`}} {
		if _, err := parseUpdateArgs(args); err == nil {
			t.Fatalf("accepted invalid update selection: %v", args)
		}
	}
	opts, err := parseUpdateArgs([]string{"--allow-prerelease", "--tag", "v2.0.1-rc.5"})
	if err != nil || !slices.Contains(releaseInstallerArgs(opts), "--allow-prerelease") {
		t.Fatal("explicit preview flag not forwarded", err)
	}
}

func TestUpdaterHonorsReleaseEnvironment(t *testing.T) {
	t.Setenv("WECHAT_CLI_RELEASE_REPO", "r266-tech/wx-cli")
	t.Setenv("WECHAT_CLI_RELEASE_TAG", "v2.0.1-rc.5")
	opts, err := parseUpdateArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Repo != "r266-tech/wx-cli" || opts.Tag != "v2.0.1-rc.5" {
		t.Fatal("release environment ignored")
	}
}

func TestStrictReadOnlyBlocksUpdaterBeforeDownload(t *testing.T) {
	t.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
	if _, err := runReleaseUpdate(updateOptions{DryRun: true}); err == nil {
		t.Fatal("strict read-only updater was allowed to download files")
	}
}
