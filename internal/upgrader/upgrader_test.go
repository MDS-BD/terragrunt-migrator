package upgrader

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRewriteTerragruntSourcePreservesUnrelatedBytes(t *testing.T) {
	assertRewriteFixture(t, "terragrunt-comments.hcl", "terragrunt-comments.want.hcl", "terragrunt.hcl", "ecs-service", "5.0.14")
}

func TestRewriteInterpolatedSourcePreservesInterpolation(t *testing.T) {
	assertRewriteFixture(t, "terragrunt-interpolation.hcl", "terragrunt-interpolation.want.hcl", "terragrunt.hcl", "ecs-service", "5.0.14")
}

func TestRewriteTabsPreservesUnrelatedBytes(t *testing.T) {
	assertRewriteFixture(t, "terragrunt-tabs.hcl", "terragrunt-tabs.want.hcl", "terragrunt.hcl", "ecs-service", "5.0.14")
}

func TestRewriteComponentRegexCapturingGroups(t *testing.T) {
	assertRewriteFixture(t, "terragrunt-regex-group.hcl", "terragrunt-regex-group.want.hcl", "terragrunt.hcl", "(ecs-service|lambdas)", "5.0.14")
}

func TestRewriteComponentRegexCapturingGroupsNoOp(t *testing.T) {
	input := readFixture(t, "terragrunt-regex-group-noop.hcl")
	got, changed, err := Rewrite(input, "terragrunt.hcl", "(ecs-service|lambdas)", "5.0.14")
	if err != nil {
		t.Fatalf("Rewrite returned error: %v", err)
	}
	if changed {
		t.Errorf("Rewrite reported a change when grouped component version already matched")
	}
	if !bytes.Equal(got, input) {
		t.Errorf("no-op Rewrite changed bytes\n got: %q\nwant: %q", got, input)
	}
}

func TestRewriteNoOp(t *testing.T) {
	input := readFixture(t, "terragrunt-noop.hcl")
	got, changed, err := Rewrite(input, "terragrunt.hcl", "ecs-service", "5.0.14")
	if err != nil {
		t.Fatalf("Rewrite returned error: %v", err)
	}
	if changed {
		t.Errorf("Rewrite reported a change when version already matched")
	}
	if !bytes.Equal(got, input) {
		t.Errorf("no-op Rewrite changed bytes\n got: %q\nwant: %q", got, input)
	}
}

func TestRewriteMalformedHCL(t *testing.T) {
	input := readFixture(t, "terragrunt-malformed.hcl")
	got, changed, err := Rewrite(input, "terragrunt.hcl", "ecs-service", "5.0.14")
	if err == nil {
		t.Error("Rewrite did not report malformed HCL")
	}
	if changed {
		t.Errorf("Rewrite reported a change for malformed HCL")
	}
	if !bytes.Equal(got, input) {
		t.Errorf("malformed HCL input was changed: got %q", got)
	}
}

func TestRewriteLambdaModuleSource(t *testing.T) {
	assertRewriteFixture(t, "lambdas.tf", "lambdas.want.tf", "lambdas.tf", "lambdas", "1.0.25")
}

func assertRewriteFixture(t *testing.T, inputName, wantName, filename, component, targetVersion string) {
	t.Helper()
	input := readFixture(t, inputName)
	want := readFixture(t, wantName)
	got, changed, err := Rewrite(input, filename, component, targetVersion)
	if err != nil {
		t.Fatalf("Rewrite returned error: %v", err)
	}
	if !changed {
		t.Error("Rewrite did not report a change")
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Rewrite mismatch\n got: %q\nwant: %q", got, want)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return content
}
