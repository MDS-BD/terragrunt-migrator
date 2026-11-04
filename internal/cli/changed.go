package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func changedTerragruntFiles(ctx context.Context, cwd string) (map[string]struct{}, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, "", fmt.Errorf("resolve current directory: %w", err)
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, "", fmt.Errorf("resolve current directory symlinks: %w", err)
	}

	rootOutput, err := runGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "", fmt.Errorf("find Git repository root: %w", err)
	}
	root, err := filepath.Abs(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return nil, "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, "", fmt.Errorf("resolve Git repository root symlinks: %w", err)
	}
	cwdRelative, err := filepath.Rel(root, cwd)
	if err != nil {
		return nil, "", fmt.Errorf("locate current directory in Git repository: %w", err)
	}
	if cwdRelative == ".." || strings.HasPrefix(cwdRelative, ".."+string(filepath.Separator)) || filepath.IsAbs(cwdRelative) {
		return nil, "", fmt.Errorf("current directory %q is outside Git repository %q", cwd, root)
	}

	statusOutput, err := runGit(ctx, cwd, "-c", "status.relativePaths=false", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", ":/")
	if err != nil {
		return nil, "", fmt.Errorf("read Git working-tree status: %w", err)
	}
	paths, err := parseGitStatusPaths(statusOutput)
	if err != nil {
		return nil, "", fmt.Errorf("parse Git working-tree status: %w", err)
	}

	changed := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = filepath.Clean(filepath.FromSlash(path))
		if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return nil, "", fmt.Errorf("Git status returned path outside repository: %q", path)
		}
		relativeToCWD, err := filepath.Rel(cwdRelative, path)
		if err != nil {
			return nil, "", fmt.Errorf("scope Git status path %q to current directory: %w", path, err)
		}
		if relativeToCWD == ".." || strings.HasPrefix(relativeToCWD, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeToCWD) {
			continue
		}
		absolute := filepath.Join(root, path)
		if filepath.Base(absolute) != "terragrunt.hcl" {
			continue
		}
		info, err := os.Stat(absolute)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, "", fmt.Errorf("inspect changed file %q: %w", absolute, err)
		}
		if !info.IsDir() {
			changed[absolute] = struct{}{}
		}
	}
	return changed, cwd, nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	if len(output) != 0 {
		return nil, fmt.Errorf("%w: %s", err, bytes.TrimSpace(output))
	}
	return nil, err
}

func parseGitStatusPaths(status []byte) ([]string, error) {
	var paths []string
	for len(status) != 0 {
		separator := bytes.IndexByte(status, 0)
		if separator < 0 {
			return nil, fmt.Errorf("unterminated status record")
		}
		record := status[:separator]
		status = status[separator+1:]
		if len(record) < 4 || record[2] != ' ' {
			return nil, fmt.Errorf("invalid status record %q", record)
		}
		path := record[3:]
		if len(path) == 0 {
			return nil, fmt.Errorf("empty path in status record %q", record)
		}
		paths = append(paths, string(path))
		if record[0] == 'R' || record[0] == 'C' || record[1] == 'R' || record[1] == 'C' {
			sourceSeparator := bytes.IndexByte(status, 0)
			if sourceSeparator < 0 || sourceSeparator == 0 {
				return nil, fmt.Errorf("missing source path for rename or copy record %q", record)
			}
			status = status[sourceSeparator+1:]
		}
	}
	return paths, nil
}
