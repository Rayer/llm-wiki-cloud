package clirelease

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"1.0.0-rc.1", "1.0.0-rc.2", -1},
		{"1.0.0-rc.2", "1.0.0", -1},
		{"1.0.0", "1.0.0+build.1", 0},
		{"1.10.0", "1.9.0", 1},
	}
	for _, test := range tests {
		got, err := CompareVersions(test.left, test.right)
		if err != nil || got != test.want {
			t.Errorf("CompareVersions(%q, %q) = %d, %v; want %d", test.left, test.right, got, err, test.want)
		}
	}
}

func TestManifestRequiresFourDistinctTargetsAndRealURLs(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 1,
		Version:       "1.2.3",
		SourceCommit:  "0123456789abcdef0123456789abcdef01234567",
		Channel:       "stable",
		Assets: []Asset{
			{OS: "darwin", Arch: "arm64", AssetName: "lwc-sync_1.2.3_darwin_arm64.tar.gz", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{OS: "darwin", Arch: "amd64", AssetName: "lwc-sync_1.2.3_darwin_amd64.tar.gz", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{OS: "windows", Arch: "amd64", AssetName: "lwc-sync_1.2.3_windows_amd64.zip", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{OS: "linux", Arch: "amd64", AssetName: "lwc-sync_1.2.3_linux_amd64.tar.gz", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest: %v", err)
	}
	manifest.Assets[0].DownloadURL = "http://downloads.example.test/cli"
	if err := manifest.Validate(); err == nil {
		t.Fatal("HTTP download URL unexpectedly accepted")
	}
}

func TestManifestRejectsPrereleaseOnStableChannel(t *testing.T) {
	manifest := Manifest{
		SchemaVersion: 1,
		Version:       "1.2.3-rc.1",
		SourceCommit:  "0123456789abcdef0123456789abcdef01234567",
		Channel:       "stable",
		Assets: []Asset{
			{OS: "darwin", Arch: "arm64", AssetName: "lwc-sync_1.2.3-rc.1_darwin_arm64.tar.gz", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{OS: "darwin", Arch: "amd64", AssetName: "lwc-sync_1.2.3-rc.1_darwin_amd64.tar.gz", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{OS: "windows", Arch: "amd64", AssetName: "lwc-sync_1.2.3-rc.1_windows_amd64.zip", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			{OS: "linux", Arch: "amd64", AssetName: "lwc-sync_1.2.3-rc.1_linux_amd64.tar.gz", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	}
	if err := manifest.Validate(); err == nil {
		t.Fatal("prerelease on stable channel unexpectedly accepted")
	}
}
