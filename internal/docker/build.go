package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/jsonmessage"
	units "github.com/docker/go-units"
)

const (
	maxContextBytes = 512 << 20
	maxContextFiles = 100000
)

var imageReferencePattern = regexp.MustCompile(`^spritexdock/[a-z0-9]+(?:-[a-z0-9]+)*:[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// BuildRequest contains already validated public application build data.
type BuildRequest struct {
	ApplicationSlug string
	DeploymentID    string
	RepositoryURL   string
	Branch          string
	CommitSHA       string
	DockerfilePath  string
	BuildContext    string
	Limits          Limits
}

// BuildResult contains the local image tag and bounded build output.
type BuildResult struct {
	ImageReference string
	Logs           string
}

// ImageReference returns the deterministic local image tag for a build.
func ImageReference(slug, deploymentID string) (string, error) {
	if !imageReferencePattern.MatchString("spritexdock/" + slug + ":" + deploymentID) {
		return "", fmt.Errorf("invalid image reference components")
	}
	return "spritexdock/" + slug + ":" + deploymentID, nil
}

// Build checks out a public repository and submits an ephemeral tar context to Docker.
func Build(ctx context.Context, cli *client.Client, req BuildRequest) (result BuildResult, err error) {
	if cli == nil {
		return result, fmt.Errorf("docker client must not be nil")
	}
	limits, err := NormalizeLimits(req.Limits)
	if err != nil {
		return result, err
	}
	imageRef, err := ImageReference(req.ApplicationSlug, req.DeploymentID)
	if err != nil {
		return result, err
	}
	if err := validateBuildRequest(req); err != nil {
		return result, err
	}

	buildCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()

	workspace, err := os.MkdirTemp("", "spritexdock-build-")
	if err != nil {
		return result, fmt.Errorf("create build workspace: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(workspace); err == nil && cleanupErr != nil {
			err = fmt.Errorf("remove build workspace: %w", cleanupErr)
		}
	}()

	if err := checkout(buildCtx, req.RepositoryURL, req.Branch, req.CommitSHA, workspace); err != nil {
		return result, err
	}
	archive, dockerfile, err := archiveContext(workspace, req.BuildContext, req.DockerfilePath)
	if err != nil {
		return result, err
	}

	response, err := cli.ImageBuild(buildCtx, archive, types.ImageBuildOptions{
		Tags:        []string{imageRef},
		Remove:      true,
		ForceRemove: true,
		Memory:      limits.MemoryBytes,
		MemorySwap:  limits.MemoryBytes,
		CPUPeriod:   cpuPeriod,
		CPUQuota:    cpuQuota(limits.CPUs),
		Dockerfile:  dockerfile,
		NetworkMode: "none",
		SecurityOpt: []string{"no-new-privileges"},
		Ulimits:     []*units.Ulimit{{Name: "nproc", Soft: limits.PIDs, Hard: limits.PIDs}},
	})
	if err != nil {
		return result, fmt.Errorf("start docker build: %w", err)
	}
	defer response.Body.Close()

	var logs boundedBuffer
	logs.limit = limits.LogBytes
	if err := jsonmessage.DisplayJSONMessagesStream(response.Body, &logs, 0, false, nil); err != nil {
		cleanupErr := removeImage(context.Background(), cli, imageRef)
		if cleanupErr != nil {
			return result, fmt.Errorf("docker build: %w (cleanup: %v)", err, cleanupErr)
		}
		return result, fmt.Errorf("docker build: %w", err)
	}
	if err := buildCtx.Err(); err != nil {
		cleanupErr := removeImage(context.Background(), cli, imageRef)
		if cleanupErr != nil {
			return result, fmt.Errorf("docker build: %w (cleanup: %v)", err, cleanupErr)
		}
		return result, fmt.Errorf("docker build: %w", err)
	}
	result.ImageReference = imageRef
	result.Logs = logs.String()
	return result, nil
}

func validateBuildRequest(req BuildRequest) error {
	parsed, err := url.ParseRequestURI(req.RepositoryURL)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return fmt.Errorf("repository URL must be a public HTTP(S) URL without credentials")
	}
	if req.Branch == "" || len(req.Branch) > 250 || !regexp.MustCompile(`^[A-Za-z0-9._/-]+$`).MatchString(req.Branch) || strings.Contains(req.Branch, "..") || strings.HasPrefix(req.Branch, "/") || strings.HasSuffix(req.Branch, "/") {
		return fmt.Errorf("invalid repository branch")
	}
	if err := validateRelativePath(req.BuildContext); err != nil {
		return fmt.Errorf("build context: %w", err)
	}
	if err := validateRelativePath(req.DockerfilePath); err != nil {
		return fmt.Errorf("Dockerfile path: %w", err)
	}
	if req.CommitSHA != "" && !regexp.MustCompile(`^[a-fA-F0-9]{7,64}$`).MatchString(req.CommitSHA) {
		return fmt.Errorf("invalid commit SHA")
	}
	return nil
}

func validateRelativePath(path string) error {
	if path == "" || filepath.IsAbs(path) || strings.Contains(path, "\\") {
		return fmt.Errorf("path must be relative")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path traversal is not allowed")
	}
	return nil
}

func checkout(ctx context.Context, repositoryURL, branch, commitSHA, workspace string) error {
	args := []string{"clone", "--depth", "1", "--branch", branch, "--single-branch", repositoryURL, workspace}
	if output, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("checkout repository: %w", safeCommandOutput(output))
	}
	if commitSHA == "" {
		return nil
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", workspace, "fetch", "--depth", "1", "origin", commitSHA).CombinedOutput(); err != nil {
		return fmt.Errorf("fetch revision: %w", safeCommandOutput(output))
	}
	if output, err := exec.CommandContext(ctx, "git", "-C", workspace, "checkout", "--detach", commitSHA).CombinedOutput(); err != nil {
		return fmt.Errorf("checkout revision: %w", safeCommandOutput(output))
	}
	return nil
}

func safeCommandOutput(output []byte) error {
	message := strings.TrimSpace(string(output))
	if len(message) > 512 {
		message = message[:512]
	}
	if message == "" {
		return errors.New("command failed")
	}
	return errors.New(message)
}

func archiveContext(workspace, contextPath, dockerfilePath string) (io.Reader, string, error) {
	root := filepath.Join(workspace, filepath.Clean(contextPath))
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", fmt.Errorf("resolve build context: %w", err)
	}
	workspaceResolved, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, "", fmt.Errorf("resolve workspace: %w", err)
	}
	if !withinPath(workspaceResolved, resolvedRoot) {
		return nil, "", fmt.Errorf("build context escapes workspace")
	}
	dockerfile := filepath.ToSlash(filepath.Clean(dockerfilePath))
	if _, err := filepath.Rel(".", dockerfile); err != nil || strings.HasPrefix(dockerfile, "../") || dockerfile == ".." {
		return nil, "", fmt.Errorf("Dockerfile path escapes build context")
	}
	if dockerfile, err = dockerfilePathWithinContext(resolvedRoot, dockerfile); err != nil {
		return nil, "", err
	}

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	files := 0
	var total int64
	err = filepath.Walk(resolvedRoot, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if files >= maxContextFiles {
			return fmt.Errorf("build context contains too many files")
		}
		rel, err := filepath.Rel(resolvedRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("build context contains symlink %q", rel)
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		files++
		if info.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(writer, io.LimitReader(file, maxContextBytes-total+1))
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		total += written
		if total > maxContextBytes {
			return fmt.Errorf("build context exceeds %d bytes", maxContextBytes)
		}
		return nil
	})
	if err != nil {
		_ = writer.Close()
		return nil, "", fmt.Errorf("archive build context: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close build context archive: %w", err)
	}
	return bytes.NewReader(buffer.Bytes()), dockerfile, nil
}

func withinPath(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func dockerfilePathWithinContext(root, dockerfile string) (string, error) {
	candidate := filepath.Join(root, filepath.FromSlash(dockerfile))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve Dockerfile: %w", err)
	}
	if !withinPath(root, resolved) {
		return "", fmt.Errorf("Dockerfile path escapes build context")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat Dockerfile: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Dockerfile must be a regular file")
	}
	return filepath.ToSlash(filepath.Clean(dockerfile)), nil
}

func removeImage(ctx context.Context, cli *client.Client, reference string) error {
	_, err := cli.ImageRemove(ctx, reference, image.RemoveOptions{Force: true, PruneChildren: true})
	if client.IsErrNotFound(err) {
		return nil
	}
	return err
}

// RemoveImage removes a local image by reference. NotFound is treated as success.
func RemoveImage(ctx context.Context, cli *client.Client, reference string) error {
	return removeImage(ctx, cli, reference)
}

type boundedBuffer struct {
	bytes.Buffer
	limit int64
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - int64(b.Len())
	if remaining <= 0 {
		return written, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	_, err := b.Buffer.Write(p)
	return written, err
}
