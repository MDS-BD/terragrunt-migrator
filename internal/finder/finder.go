package finder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/rs/zerolog"
)

type Options struct {
	Filename                     string
	ContentRegex, ComponentRegex *regexp.Regexp
	VersionRange                 *semver.Constraints
}

var ignoredDirectories = map[string]struct{}{".terragrunt-cache": {}, "venv": {}, "node_modules": {}, ".git": {}}

// Find visits matching files in depth-first alphabetical order.
func Find(ctx context.Context, dir string, opts Options, yield func(string) error) error {
	return walk(ctx, dir, opts, yield)
}

func walk(ctx context.Context, dir string, opts Options, yield func(string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir) // ReadDir sorts by filename.
	if err != nil {
		zerolog.Ctx(ctx).Error().Stack().Str("dir", dir).Err(err).Msg("Could not read directory")
		return nil
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			if _, skip := ignoredDirectories[entry.Name()]; skip {
				continue
			}
			if err := walk(ctx, path, opts, yield); err != nil {
				return err
			}
			continue
		}
		if !entry.Type().IsRegular() || entry.Name() != opts.Filename {
			continue
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read matched file %s: %w", path, err)
		}
		if opts.VersionRange != nil {
			if opts.ContentRegex == nil {
				continue
			}
			match := opts.ContentRegex.FindSubmatch(content)
			if len(match) < 2 {
				continue
			}
			version, err := semver.NewVersion(strings.TrimSpace(string(match[1])))
			if err != nil || !opts.VersionRange.Check(version) {
				continue
			}
		} else {
			if opts.ContentRegex != nil && !opts.ContentRegex.Match(content) {
				continue
			}
			if opts.ComponentRegex != nil && !opts.ComponentRegex.Match(content) {
				continue
			}
		}
		if yield != nil {
			if err := yield(path); err != nil {
				return err
			}
		}
	}
	return nil
}
