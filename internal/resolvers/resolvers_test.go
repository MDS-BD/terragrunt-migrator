package resolvers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-json"
)

func stateFromFixture(t *testing.T, name string) *tfjson.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read state fixture %q: %v", name, err)
	}
	var state tfjson.State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("decode state fixture %q: %v", name, err)
	}
	return &state
}

var missingECSFixtures = []string{
	"ecs-missing-root-resources.json",
	"ecs-missing-container-definitions.json",
	"ecs-missing-app-version.json",
}

var missingLambdaFixtures = []string{
	"lambdas-missing-map-key.json",
	"lambdas-invalid-map-name.json",
}
var lambdaExtractionZipMap = map[string]string{
	"Communications": "communications.zip",
	"Notifier":       "notifier.zip",
}

var lambdaDuplicateZipMap = map[string]string{
	"Communications": "last.zip",
}

func TestECSExtractsAppVersion(t *testing.T) {
	state := stateFromFixture(t, "ecs-app-version.json")
	variables, err := ECS(state)
	if err != nil {
		t.Errorf("ECS() error = %v", err)
		return
	}
	if variables["ecr_image_tag"] != "1.2.3" {
		t.Errorf("ecr_image_tag = %q, want %q", variables["ecr_image_tag"], "1.2.3")
	}
}

func TestECSMissingFieldsReturnNoVariables(t *testing.T) {
	for _, fixture := range missingECSFixtures {
		variables, err := ECS(stateFromFixture(t, fixture))
		if err != nil {
			t.Errorf("ECS() error = %v", err)
			continue
		}
		if variables != nil {
			t.Errorf("ECS() = %#v, want no variables", variables)
		}
	}
}

func TestECSInvalidContainerJSONReturnsError(t *testing.T) {
	state := stateFromFixture(t, "ecs-invalid-container-json.json")
	if _, err := ECS(state); err == nil {
		t.Error("ECS() error = nil, want invalid container JSON error")
	}
}

func TestLambdasExtractsCodePathsAndVersions(t *testing.T) {
	state := stateFromFixture(t, "lambdas-extraction.json")
	variables, err := Lambdas(state)
	if err != nil {
		t.Errorf("Lambdas() error = %v", err)
		return
	}
	var zipMap map[string]string
	if err := json.Unmarshal([]byte(variables["lambda_code_zip_map"]), &zipMap); err != nil {
		t.Errorf("decode lambda_code_zip_map: %v", err)
		return
	}
	if variables["lambda_code_version"] != "release-42" || variables["lambda_code_basepath"] != "artifacts" || !reflect.DeepEqual(zipMap, lambdaExtractionZipMap) {
		t.Errorf("Lambdas() = %#v, zip map = %#v", variables, zipMap)
	}
}

func TestLambdasDuplicateNamesUseLastZipValue(t *testing.T) {
	state := stateFromFixture(t, "lambdas-duplicate-name.json")
	variables, err := Lambdas(state)
	if err != nil {
		t.Errorf("Lambdas() error = %v", err)
		return
	}
	var zipMap map[string]string
	if err := json.Unmarshal([]byte(variables["lambda_code_zip_map"]), &zipMap); err != nil {
		t.Errorf("decode lambda_code_zip_map: %v", err)
		return
	}
	if !reflect.DeepEqual(zipMap, lambdaDuplicateZipMap) {
		t.Errorf("Communications zip map = %#v, want %#v", zipMap, lambdaDuplicateZipMap)
	}
}

func TestLambdasMissingMapKeysReturnNoVariables(t *testing.T) {
	for _, fixture := range missingLambdaFixtures {
		variables, err := Lambdas(stateFromFixture(t, fixture))
		if err != nil {
			t.Errorf("Lambdas() error = %v", err)
			continue
		}
		if variables != nil {
			t.Errorf("Lambdas() = %#v, want no variables", variables)
		}
	}
}
