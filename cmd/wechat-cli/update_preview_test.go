package main

import (
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

func TestPreviewUpdateRequiresExplicitPrerelease(t *testing.T) {
	for _, args := range [][]string{{"--allow-prerelease"}, {"--allow-prerelease", "--tag", "latest"}, {"--allow-prerelease", "--tag", "v2.0.1"}, {"--repo", "https://example.com/x/y"}, {"--tag", "v2.0.1/../../x"}} {
		if _, err := parseUpdateArgs(args); err == nil {
			t.Fatalf("accepted invalid update selection: %v", args)
		}
	}
	if runtime.GOOS == "windows" {
		return
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
