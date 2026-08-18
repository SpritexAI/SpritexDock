package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewClientRejectsEmptyEndpoint(t *testing.T) {
	if _, err := NewClient(" "); err == nil {
		t.Fatal("NewClient accepted an empty endpoint")
	}
}

func TestImageReference(t *testing.T) {
	valid := "spritexdock/my-app:deployment-1"
	got, err := ImageReference("my-app", "deployment-1")
	if err != nil || got != valid {
		t.Fatalf("ImageReference() = %q, %v; want %q", got, err, valid)
	}
	for _, values := range [][2]string{
		{"my/app", "deployment"},
		{"my-app", "../../etc"},
		{"My-app", "deployment"},
		{"my-app", "deployment\n"},
	} {
		if _, err := ImageReference(values[0], values[1]); err == nil {
			t.Fatalf("ImageReference(%q, %q) accepted unsafe input", values[0], values[1])
		}
	}
}

func TestNormalizeLimits(t *testing.T) {
	limits, err := NormalizeLimits(Limits{MemoryBytes: 512, CPUs: 1, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if limits.LogBytes != defaultLogLimit || limits.PIDs != maxBuildPIDs {
		t.Fatalf("defaults = log %d, pids %d", limits.LogBytes, limits.PIDs)
	}
	for _, input := range []Limits{
		{MemoryBytes: 0, CPUs: 1, Timeout: time.Minute},
		{MemoryBytes: 1, CPUs: 0, Timeout: time.Minute},
		{MemoryBytes: 1, CPUs: 1, Timeout: 0},
		{MemoryBytes: 1, CPUs: 1, Timeout: time.Minute, LogBytes: -1},
		{MemoryBytes: 1, CPUs: 1, Timeout: time.Minute, PIDs: maxBuildPIDs + 1},
	} {
		if _, err := NormalizeLimits(input); err == nil {
			t.Fatalf("NormalizeLimits(%+v) accepted invalid input", input)
		}
	}
}

func TestValidateBuildRequest(t *testing.T) {
	base := BuildRequest{
		RepositoryURL:  "https://github.com/example/project.git",
		Branch:         "main",
		DockerfilePath: "Dockerfile",
		BuildContext:   ".",
	}
	if err := validateBuildRequest(base); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BuildRequest){
		func(req *BuildRequest) { req.RepositoryURL = "file:///tmp/repo" },
		func(req *BuildRequest) { req.RepositoryURL = "https://user:pass@example.com/repo" },
		func(req *BuildRequest) { req.RepositoryURL = "https://example.com/repo\n" },
		func(req *BuildRequest) { req.Branch = "../main" },
		func(req *BuildRequest) { req.BuildContext = "../" },
		func(req *BuildRequest) { req.DockerfilePath = "/Dockerfile" },
		func(req *BuildRequest) { req.CommitSHA = "not-a-sha" },
	} {
		request := base
		mutate(&request)
		if err := validateBuildRequest(request); err == nil {
			t.Fatalf("validateBuildRequest accepted %+v", request)
		}
	}
}

func TestArchiveContextRejectsSymlinkAndEscapes(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archiveContext(workspace, "../", "Dockerfile"); err == nil {
		t.Fatal("archiveContext accepted an escaping context")
	}
	if err := os.Symlink(filepath.Join(workspace, "Dockerfile"), filepath.Join(workspace, "link")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archiveContext(workspace, ".", "Dockerfile"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("archiveContext symlink error = %v", err)
	}
}

func TestArchiveContextRejectsDockerfileSymlink(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "real-Dockerfile")
	if err := os.WriteFile(target, []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(workspace, "Dockerfile")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := archiveContext(workspace, ".", "Dockerfile"); err == nil {
		t.Fatal("archiveContext accepted a symlink Dockerfile")
	}
}

func TestBoundedBufferPreservesWriterContract(t *testing.T) {
	var buffer boundedBuffer
	buffer.limit = 3
	input := []byte("abcdef")
	n, err := buffer.Write(input)
	if err != nil || n != len(input) || buffer.String() != "abc" {
		t.Fatalf("Write() = n=%d err=%v contents=%q", n, err, buffer.String())
	}
	if n, err := buffer.Write([]byte("z")); err != nil || n != 1 || buffer.String() != "abc" {
		t.Fatalf("overflow Write() = n=%d err=%v contents=%q", n, err, buffer.String())
	}
}
