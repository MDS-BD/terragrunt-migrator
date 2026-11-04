package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/MDS-BD/terragrunt-migrator/internal/finder"
	"github.com/MDS-BD/terragrunt-migrator/internal/resolvers"
	"github.com/MDS-BD/terragrunt-migrator/internal/terragrunt"
	"github.com/MDS-BD/terragrunt-migrator/internal/upgrader"
	"github.com/Masterminds/semver/v3"
	"github.com/charmbracelet/huh"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// NewRoot creates the terragrunt-migrator command tree.
func NewRoot(version string) *cobra.Command {
	var dryRun, autoApprove, autoApply, changedOnly bool
	root := &cobra.Command{Use: "terragrunt-migrator", Short: "Upgrade Terraform components and reapply Terragrunt deployments", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes")
	root.PersistentFlags().BoolVar(&autoApprove, "auto-approve", false, "Whether to auto-approve changes to files")
	root.PersistentFlags().BoolVar(&autoApply, "auto-apply", false, "Whether Terragrunt should auto-apply changes")

	component := &cobra.Command{
		Use: "upgrade-component", Short: "Upgrade all instances of a component", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			name, err := inputValue(cmd, "component", "Enter the component you want to update (eg. ecs-service)")
			if err != nil {
				return err
			}
			rangeText, err := inputValue(cmd, "version-range", "Enter the version range you want to update. Please provide a semver range (eg. 3, 3.*, ~3)")
			if err != nil {
				return err
			}
			target, err := inputValue(cmd, "target-version", "Enter the target version you want to update. Please provide a semver range (eg. 3, 3.*, ~3)")
			if err != nil {
				return err
			}
			return upgradeComponent(cmd.Context(), cmd, name, rangeText, target, dryRun)
		},
	}
	component.Flags().String("component", "", "Component that needs to be updated")
	component.Flags().String("version-range", "", "Version range that needs to be updated (use semver syntax)")
	component.Flags().String("target-version", "", "The version you want to land on")

	lambdas := &cobra.Command{
		Use: "upgrade-lambdas", Short: "Upgrade all instances of lambdas. Launch from the repo root", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rangeText, err := inputValue(cmd, "version-range", "Enter the version range you want to update. Please provide a semver range (eg. 3, 3.*, ~3)")
			if err != nil {
				return err
			}
			target, err := inputValue(cmd, "target-version", "Enter the target version you want to update. Please provide a semver range (eg. 3, 3.*, ~3)")
			if err != nil {
				return err
			}
			return upgradeLambdas(cmd.Context(), cmd, rangeText, target, dryRun)
		},
	}
	lambdas.Flags().String("version-range", "", "Version range that needs to be updated (use semver syntax)")
	lambdas.Flags().String("target-version", "", "The version you want to land on")

	reapply := &cobra.Command{
		Use: "reapply", Short: "Run reapply for all matching deployments", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			componentName, _ := cmd.Flags().GetString("component")
			pattern, _ := cmd.Flags().GetString("regex")
			apply, err := boolValue(cmd, "auto-apply", "Do you want Terragrunt to auto-apply changes to files?", false)
			if err != nil {
				return err
			}
			return reapply(cmd.Context(), componentName, pattern, dryRun, apply, changedOnly)
		},
	}
	reapply.Flags().String("component", "", "Component to reapply (eg. ecs-service)")
	reapply.Flags().String("regex", "", "Regex to match into terragrunt.hcl files")
	reapply.Flags().BoolVar(&changedOnly, "changed", false, "Only reapply changed terragrunt.hcl files under the current directory (requires Git)")
	root.AddCommand(component, lambdas, reapply)
	return root
}

func inputValue(cmd *cobra.Command, name, message string) (string, error) {
	value, err := cmd.Flags().GetString(name)
	if err != nil {
		return "", err
	}
	if cmd.Flags().Changed(name) {
		return value, nil
	}
	if err := cmd.Context().Err(); err != nil {
		return "", err
	}
	var answer string
	field := huh.NewInput().Title(message).Value(&answer).Validate(func(v string) error {
		if strings.TrimSpace(v) == "" {
			return errors.New("value is required")
		}
		return nil
	})
	if err := runField(cmd, field); err != nil {
		return "", err
	}
	if err := cmd.Context().Err(); err != nil {
		return "", err
	}
	return answer, nil
}

func boolValue(cmd *cobra.Command, name, message string, defaultValue bool) (bool, error) {
	value, err := cmd.Flags().GetBool(name)
	if err != nil {
		return false, err
	}
	if changed(cmd, name) {
		return value, nil
	}
	if err := cmd.Context().Err(); err != nil {
		return false, err
	}
	value = defaultValue
	if err := runField(cmd, huh.NewConfirm().Title(message).Value(&value)); err != nil {
		return false, err
	}
	if err := cmd.Context().Err(); err != nil {
		return false, err
	}
	return value, nil
}

func runField(cmd *cobra.Command, field huh.Field) error {
	err := huh.NewForm(huh.NewGroup(field)).WithInput(cmd.InOrStdin()).WithOutput(cmd.ErrOrStderr()).RunWithContext(cmd.Context())
	if errors.Is(err, huh.ErrUserAborted) {
		return context.Canceled
	}
	return err
}

func changed(cmd *cobra.Command, name string) bool {
	return cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name)
}

var numericTriple = regexp.MustCompile(`\d+\.\d+\.\d+`)

func validateTarget(target string) error {
	if !numericTriple.MatchString(target) {
		return fmt.Errorf("invalid target version provided: %q", target)
	}
	return nil
}

func upgradeComponent(ctx context.Context, cmd *cobra.Command, component, rangeText, target string, dryRun bool) error {
	approved, err := boolValue(cmd, "auto-approve", "Do you want to auto-approve changes to files?", true)
	if err != nil {
		return err
	}
	autoApply, err := boolValue(cmd, "auto-apply", "Do you want Terragrunt to auto-apply changes to files?", false)
	if err != nil {
		return err
	}
	if err := validateTarget(target); err != nil {
		zerolog.Ctx(ctx).Info().Err(err).Msg(err.Error())
		return err
	}
	constraints, err := semver.NewConstraint(rangeText)
	if err != nil {
		return fmt.Errorf("invalid version range %q: %w", rangeText, err)
	}
	if dryRun {
		dryRunNotice(ctx)
	}
	// Preserve TypeScript RegExp interpolation semantics; Go's RE2 rejects some JS constructs.
	contentRE, err := regexp.Compile("(?s)" + component + `-(\d+\.\d+\.\d+)`)
	if err != nil {
		zerolog.Ctx(ctx).Info().Str("component", component).Err(err).Msg("Invalid component provided")
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	found := false
	err = finder.Find(ctx, cwd, finder.Options{Filename: "terragrunt.hcl", ContentRegex: contentRE, VersionRange: constraints}, func(path string) error {
		found = true
		return upgradeFile(ctx, cmd, path, component, target, approved, autoApply, dryRun, false, "")
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		zerolog.Ctx(ctx).Error().Stack().Str("dir", cwd).Err(err).Msg("An error occurred while searching")
		return err
	}
	if !found {
		zerolog.Ctx(ctx).Info().Msg("No terragrunt.hcl files found.")
	}
	return nil
}

func upgradeLambdas(ctx context.Context, cmd *cobra.Command, rangeText, target string, dryRun bool) error {
	approved, err := boolValue(cmd, "auto-approve", "Do you want to auto-approve changes to files?", true)
	if err != nil {
		return err
	}
	autoApply, err := boolValue(cmd, "auto-apply", "Do you want Terragrunt to auto-apply changes to files?", false)
	if err != nil {
		return err
	}
	if err := validateTarget(target); err != nil {
		zerolog.Ctx(ctx).Info().Err(err).Msg(err.Error())
		return err
	}
	constraints, err := semver.NewConstraint(rangeText)
	if err != nil {
		return fmt.Errorf("invalid version range %q: %w", rangeText, err)
	}
	if dryRun {
		dryRunNotice(ctx)
	}
	contentRE := regexp.MustCompile(`lambdas-(\d+\.\d+\.\d+)`)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	found := false
	err = finder.Find(ctx, filepath.Join(cwd, "components"), finder.Options{Filename: "lambdas.tf", ContentRegex: contentRE, VersionRange: constraints}, func(moduleFile string) error {
		found = true
		moduleName := filepath.Base(filepath.Dir(moduleFile))
		stageRE, err := regexp.Compile(`source\s*=.+` + moduleName)
		if err != nil {
			return err
		}
		canSkip := false
		return finder.Find(ctx, filepath.Join(cwd, "stages"), finder.Options{Filename: "terragrunt.hcl", ContentRegex: stageRE}, func(deployment string) error {
			err := upgradeFile(ctx, cmd, deployment, "lambdas", target, approved, autoApply, dryRun, canSkip, moduleFile)
			canSkip = true // TS enables this after the first deployment, even if processing failed.
			return err
		})
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		zerolog.Ctx(ctx).Error().Stack().Str("dir", cwd).Err(err).Msg("An error occurred while searching")
		return err
	}
	if !found {
		zerolog.Ctx(ctx).Info().Msg("No lambdas.tf files found.")
	}
	return nil
}

func dryRunNotice(ctx context.Context) {
	zerolog.Ctx(ctx).Info().Msg("Dry run: no files will be changed; Terragrunt init/show may run")
}

func upgradeFile(ctx context.Context, cmd *cobra.Command, file, component, target string, approved, autoApply, dryRun, canSkip bool, componentFile string) error {
	cwd := filepath.Dir(file)
	stage, tfComponent := filepath.Base(filepath.Dir(cwd)), filepath.Base(cwd)
	logger := zerolog.Ctx(ctx).With().Str("tfStage", stage).Str("tfComponent", tfComponent).Logger()
	ctx = logger.WithContext(ctx)
	log := zerolog.Ctx(ctx)
	log.Info().Str("file", file).Msg("Deployment: " + stage + "/" + tfComponent)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	runner := &terragrunt.Runner{}
	variables, err := resolvers.Extract(ctx, runner, component, cwd)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Error().Stack().Err(err).Msg("Failed to resolve variables; skipping file")
		return nil
	}
	editPath := file
	if componentFile != "" {
		editPath = componentFile
	}
	original, err := os.ReadFile(editPath)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Error().Stack().Str("file", editPath).Err(err).Msg("Failed to read file; skipping file")
		return nil
	}
	updated, changed, err := upgrader.Rewrite(original, filepath.Base(editPath), component, target)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Error().Stack().Str("file", editPath).Err(err).Msg("Failed to rewrite file; skipping file")
		return nil
	}
	if !changed {
		if !canSkip {
			log.Info().Msg("No version change; skipping deployment")
			return nil
		}
		log.Info().Msg("No version change required")
	} else {
		log.Info().Msg("Preparing file change")
		oldLine, newLine := previewLines(original, updated, component, target)
		log.Info().Str("oldLine", strings.TrimSpace(oldLine)).Str("newLine", strings.TrimSpace(newLine)).Msg("Proposed change")
		applyChange := approved
		if !approved {
			if dryRun {
				log.Info().Msg("Dry run: approval prompt skipped")
				applyChange = true
			} else {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				applyChange, err = confirmChange(ctx, cmd, editPath)
				if err != nil {
					return fmt.Errorf("file approval prompt: %w", err)
				}
			}
		}
		if !applyChange {
			log.Info().Msg("File change declined")
			return nil
		}
		if dryRun {
			log.Info().Str("file", editPath).Msg("Dry run: file would be updated")
		} else {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := os.WriteFile(editPath, updated, 0o644); err != nil {
				log.Error().Stack().Str("file", editPath).Err(err).Msg("Failed to write file; skipping file")
				return nil
			}
			log.Info().Str("file", editPath).Msg("File updated")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := runner.Init(ctx, cwd, dryRun); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Error().Stack().Err(err).Msg("Terragrunt init failed; skipping apply")
		return nil
	}
	if err := runner.Apply(ctx, cwd, variables, autoApply, dryRun); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Error().Stack().Err(err).Msg("Terragrunt apply failed; skipping file")
		return nil
	}
	if dryRun {
		log.Info().Msg("Deployment dry run complete")
	} else {
		log.Info().Msg("Deployment complete")
	}
	return nil
}

func previewLines(original, updated []byte, component, target string) (string, string) {
	pattern := regexp.MustCompile("(?s)" + component + `-\d+\.\d+\.\d+`)
	oldLine, newLine := "", ""
	for line := range strings.SplitSeq(string(original), "\n") {
		if pattern.MatchString(line) {
			oldLine = line
			break
		}
	}
	for line := range strings.SplitSeq(string(updated), "\n") {
		if strings.Contains(line, component+"-"+target) {
			newLine = line
			break
		}
	}
	return oldLine, newLine
}

func confirmChange(ctx context.Context, cmd *cobra.Command, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	approved := true // Inquirer confirm prompts default to yes.
	field := huh.NewConfirm().Title("Do you want to apply this change to " + path + "?").Value(&approved)
	err := runField(cmd, field)
	if err == nil {
		err = ctx.Err()
	}
	return approved, err
}

func reapply(ctx context.Context, component, pattern string, dryRun, autoApply, changedOnly bool) error {
	if dryRun {
		dryRunNotice(ctx)
	}
	var contentRE, componentRE *regexp.Regexp
	var err error
	if pattern != "" {
		contentRE, err = regexp.Compile(pattern)
		if err != nil {
			zerolog.Ctx(ctx).Info().Str("regex", pattern).Err(err).Msg("Invalid regex provided")
			return err
		}
	}
	if component != "" {
		componentRE = regexp.MustCompile("(?s)" + regexp.QuoteMeta(component) + `-(\d+\.\d+\.\d+)`)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var changedFiles map[string]struct{}
	if changedOnly {
		changedFiles, cwd, err = changedTerragruntFiles(ctx, cwd)
		if err != nil {
			return fmt.Errorf("--changed requires a Git working tree: %w", err)
		}
	}
	found := false
	err = finder.Find(ctx, cwd, finder.Options{Filename: "terragrunt.hcl", ContentRegex: contentRE, ComponentRegex: componentRE}, func(file string) error {
		if changedOnly {
			if _, ok := changedFiles[filepath.Clean(file)]; !ok {
				return nil
			}
		}
		found = true
		return reapplyFile(ctx, file, dryRun, autoApply)
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		zerolog.Ctx(ctx).Error().Stack().Str("dir", cwd).Err(err).Msg("An error occurred while searching")
		return err
	}
	if !found {
		zerolog.Ctx(ctx).Info().Msg("No terragrunt.hcl files found.")
	}
	return nil
}

var reapplyComponent = regexp.MustCompile(`//([^"?]+)`)

func reapplyFile(ctx context.Context, file string, dryRun, autoApply bool) error {
	cwd := filepath.Dir(file)
	stage, tfComponent := filepath.Base(filepath.Dir(cwd)), filepath.Base(cwd)
	logger := zerolog.Ctx(ctx).With().Str("tfStage", stage).Str("tfComponent", tfComponent).Logger()
	ctx = logger.WithContext(ctx)
	log := zerolog.Ctx(ctx)
	log.Info().Str("file", file).Msg("Deployment: " + stage + "/" + tfComponent)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	component := ""
	if content, err := os.ReadFile(file); err == nil {
		if match := reapplyComponent.FindSubmatch(content); len(match) > 1 {
			component = string(match[1])
		}
	}
	runner := &terragrunt.Runner{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr}
	variables, err := resolvers.Extract(ctx, runner, component, cwd)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Error().Stack().Err(err).Msg("Failed to resolve variables; skipping file")
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := runner.Init(ctx, cwd, dryRun); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Error().Stack().Err(err).Msg("Terragrunt init failed; skipping apply")
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := runner.Apply(ctx, cwd, variables, autoApply, dryRun); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return err
		}
		log.Error().Stack().Err(err).Msg("Terragrunt apply failed; skipping file")
		return nil
	}
	if dryRun {
		log.Info().Msg("Deployment dry run complete")
	} else {
		log.Info().Msg("Deployment complete")
	}
	return nil
}
