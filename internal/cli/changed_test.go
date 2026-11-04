package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestReapplyChangedUsesGitStatusAndExistingFilters(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	initial := map[string]string{
		".gitignore":                             "nested/ignored/terragrunt.hcl\n",
		"nested/staged/terragrunt.hcl":           "# target component-1.2.3\n",
		"nested/unstaged/terragrunt.hcl":         "# original\n",
		"nested/rename-from/terragrunt.hcl":      "# target component-1.2.3\n",
		"nested/deleted/terragrunt.hcl":          "# target component-1.2.3\n",
		"nested/clean/terragrunt.hcl":            "# target component-1.2.3\n",
		"nested/filter-pattern/terragrunt.hcl":   "# component-1.2.3\n",
		"nested/filter-component/terragrunt.hcl": "# target\n",
		"other/changed/terragrunt.hcl":           "# target component-1.2.3\n",
		"other/deleted/terragrunt.hcl":           "# target component-1.2.3\n",
	}
	for name, content := range initial {
		writeTestFile(t, root, name, content)
	}
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.name", "Changed Reapply Test")
	gitTest(t, root, "config", "commit.gpgsign", "false")
	gitTest(t, root, "config", "user.email", "changed-reapply@example.invalid")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-q", "-m", "initial")
	writeTestFile(t, root, ".gitignore", "nested/ignored/terragrunt.hcl\n")
	writeTestFile(t, root, "nested/ignored/terragrunt.hcl", "# target component-1.2.3 ignored\n")

	writeTestFile(t, root, "nested/staged/terragrunt.hcl", "# target component-1.2.3 staged\n")
	gitTest(t, root, "add", "nested/staged/terragrunt.hcl")
	writeTestFile(t, root, "nested/unstaged/terragrunt.hcl", "# target component-1.2.3 unstaged\n")
	if err := os.MkdirAll(filepath.Join(root, "nested", "rename-to"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "mv", "nested/rename-from/terragrunt.hcl", "nested/rename-to/terragrunt.hcl")
	if err := os.Remove(filepath.Join(root, "nested", "deleted", "terragrunt.hcl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "other", "deleted", "terragrunt.hcl")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "nested/filter-pattern/terragrunt.hcl", "# component-1.2.3 changed\n")
	writeTestFile(t, root, "nested/filter-component/terragrunt.hcl", "# target changed\n")
	writeTestFile(t, root, "other/changed/terragrunt.hcl", "# target component-1.2.3 outside\n")
	writeTestFile(t, root, "nested/un tracked/line\nname/terragrunt.hcl", "# target component-1.2.3 untracked\n")

	cwd := filepath.Join(root, "nested")
	changed, _, err := changedTerragruntFiles(context.Background(), cwd)
	if err != nil {
		t.Fatal(err)
	}
	wantChanged := []string{
		filepath.Join(root, "nested", "filter-component", "terragrunt.hcl"),
		filepath.Join(root, "nested", "filter-pattern", "terragrunt.hcl"),
		filepath.Join(root, "nested", "rename-to", "terragrunt.hcl"),
		filepath.Join(root, "nested", "staged", "terragrunt.hcl"),
		filepath.Join(root, "nested", "un tracked", "line\nname", "terragrunt.hcl"),
		filepath.Join(root, "nested", "unstaged", "terragrunt.hcl"),
	}
	if got := sortedMapKeys(changed); !reflect.DeepEqual(got, wantChanged) {
		t.Fatalf("changed paths = %#v, want %#v", got, wantChanged)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	var logOutput strings.Builder
	ctx := zerolog.New(&logOutput).WithContext(context.Background())
	if err := reapply(ctx, "component", "target", true, false, true); err != nil {
		t.Fatal(err)
	}
	wantSelected := []string{
		filepath.Join(root, "nested", "rename-to", "terragrunt.hcl"),
		filepath.Join(root, "nested", "staged", "terragrunt.hcl"),
		filepath.Join(root, "nested", "un tracked", "line\nname", "terragrunt.hcl"),
		filepath.Join(root, "nested", "unstaged", "terragrunt.hcl"),
	}
	if got := deploymentFiles(t, logOutput.String()); !reflect.DeepEqual(got, wantSelected) {
		t.Fatalf("--changed selected = %#v, want %#v", got, wantSelected)
	}

	logOutput.Reset()
	if err := reapply(ctx, "", "", true, false, false); err != nil {
		t.Fatal(err)
	}
	wantAll := []string{
		filepath.Join(root, "nested", "clean", "terragrunt.hcl"),
		filepath.Join(root, "nested", "filter-component", "terragrunt.hcl"),
		filepath.Join(root, "nested", "filter-pattern", "terragrunt.hcl"),
		filepath.Join(root, "nested", "ignored", "terragrunt.hcl"),
		filepath.Join(root, "nested", "rename-to", "terragrunt.hcl"),
		filepath.Join(root, "nested", "staged", "terragrunt.hcl"),
		filepath.Join(root, "nested", "un tracked", "line\nname", "terragrunt.hcl"),
		filepath.Join(root, "nested", "unstaged", "terragrunt.hcl"),
	}
	if got := deploymentFiles(t, logOutput.String()); !reflect.DeepEqual(got, wantAll) {
		t.Fatalf("normal reapply selected = %#v, want %#v", got, wantAll)
	}
}

func TestChangedFlagIsRegisteredOnlyForReapply(t *testing.T) {
	root := NewRoot("test")
	reapply, _, err := root.Find([]string{"reapply"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := reapply.Flags().Lookup("changed"); flag == nil || flag.DefValue != "false" {
		t.Fatalf("reapply --changed flag = %#v, want registered false flag", flag)
	}
	component, _, err := root.Find([]string{"upgrade-component"})
	if err != nil {
		t.Fatal(err)
	}
	if component.Flags().Lookup("changed") != nil {
		t.Fatal("upgrade-component unexpectedly has --changed")
	}
	lambdas, _, err := root.Find([]string{"upgrade-lambdas"})
	if err != nil {
		t.Fatal(err)
	}
	if lambdas.Flags().Lookup("changed") != nil {
		t.Fatal("upgrade-lambdas unexpectedly has --changed")
	}
}

func TestChangedReapplyRequiresGitRepository(t *testing.T) {
	_, _, err := changedTerragruntFiles(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("changedTerragruntFiles succeeded outside a Git repository")
	}
}

func TestParseGitStatusPathsUsesRenameDestination(t *testing.T) {
	status := []byte(" M modified file\x00R  nested/new name\x00nested/old name\x00?? untracked\nfile\x00 D deleted\x00")
	paths, err := parseGitStatusPaths(status)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"modified file", "nested/new name", "untracked\nfile", "deleted"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("parsed status paths = %#v, want %#v", paths, want)
	}
}

func writeTestFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func sortedMapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func deploymentFiles(t *testing.T, output string) []string {
	t.Helper()
	var files []string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		var record struct {
			Message string `json:"message"`
			File    string `json:"file"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("parse reapply log: %v", err)
		}
		if strings.HasPrefix(record.Message, "Deployment: ") {
			files = append(files, record.File)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return files
}
