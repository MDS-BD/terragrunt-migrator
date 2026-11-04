package finder

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"

	"github.com/Masterminds/semver/v3"
)

var testFiles = map[string]string{
	"a/terragrunt.hcl":                 "version = \"component-1.2.3\"\n team = yes",
	"b/terragrunt.hcl":                 "version = \"component-1.3.0\"\n team = yes",
	"c/terragrunt.hcl":                 "version = \"component-2.0.0\"\n team = yes",
	"d/terragrunt.hcl":                 "version = \"component-1.2.9\"\n team = no",
	".terragrunt-cache/terragrunt.hcl": "version = \"component-1.2.3\" team = yes",
	"venv/terragrunt.hcl":              "version = \"component-1.2.3\" team = yes",
	".git/terragrunt.hcl":              "version = \"component-1.2.3\" team = yes",
	"a/node_modules/x/terragrunt.hcl":  "version = \"component-1.2.3\" team = yes",
}

func TestFindOrderSkipConjunctionAndVersionBoundaries(t *testing.T) {
	root := t.TempDir()
	symlinkTarget := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(symlinkTarget, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(symlinkTarget, "terragrunt.hcl"), []byte("version = \"component-1.2.3\" team = yes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(symlinkTarget, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(symlinkTarget, "terragrunt.hcl"), filepath.Join(root, "terragrunt.hcl")); err != nil {
		t.Fatal(err)
	}
	for name, content := range testFiles {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	if err := Find(context.Background(), root, Options{Filename: "terragrunt.hcl", ContentRegex: regexp.MustCompile(`component-(\d+\.\d+\.\d+)`), ComponentRegex: regexp.MustCompile(`team = yes`)}, func(path string) error { got = append(got, path); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "a", "terragrunt.hcl"), filepath.Join(root, "b", "terragrunt.hcl"), filepath.Join(root, "c", "terragrunt.hcl")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	constraint, err := semver.NewConstraint(">= 1.2.3, < 2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := Find(context.Background(), root, Options{Filename: "terragrunt.hcl", ContentRegex: regexp.MustCompile(`component-(\d+\.\d+\.\d+)`), VersionRange: constraint}, func(path string) error { got = append(got, path); return nil }); err != nil {
		t.Fatal(err)
	}
	want = []string{filepath.Join(root, "a", "terragrunt.hcl"), filepath.Join(root, "b", "terragrunt.hcl"), filepath.Join(root, "d", "terragrunt.hcl")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
