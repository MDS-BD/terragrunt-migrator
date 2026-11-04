package resolvers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/MDS-BD/terragrunt-migrator/internal/terragrunt"
	"github.com/hashicorp/terraform-json"
	"github.com/rs/zerolog"
)

// Extract resolves variables for components whose values are available in the
// Terraform state. Unsupported components do not require Terraform state.
func Extract(ctx context.Context, runner *terragrunt.Runner, component, cwd string) (map[string]string, error) {
	var resolve func(*tfjson.State) (map[string]string, error)
	switch component {
	case "ecs-service":
		resolve = ECS
	case "lambdas":
		resolve = Lambdas
	default:
		return nil, nil
	}

	zerolog.Ctx(ctx).Debug().Str("component", component).Str("cwd", cwd).Msg("Retrieving variables from Terraform state")
	// Match the TypeScript resolver: initialization is best-effort, but state
	// retrieval is required.
	if err := runner.Init(ctx, cwd, false); errors.Is(err, context.Canceled) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state, err := runner.Show(ctx, cwd)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		zerolog.Ctx(ctx).Error().Stack().Str("cwd", cwd).Err(err).Msg("Failed to retrieve Terraform variables")
		return nil, err
	}

	variables, err := resolve(state)
	if err == nil && variables == nil {
		err = fmt.Errorf("Unable to find variables for this component")
	}
	if err != nil {
		zerolog.Ctx(ctx).Error().Stack().Str("cwd", cwd).Err(err).Msg("Failed to retrieve Terraform variables")
	}
	return variables, err
}

// ECS extracts APP_VERSION from the first ECS task definition's container
// definitions and maps it to the Terraform input expected by the upgrader.
func ECS(state *tfjson.State) (map[string]string, error) {
	resource := firstResource(state, "aws_ecs_task_definition")
	if resource == nil {
		return nil, nil
	}
	definitions, ok := resource.AttributeValues["container_definitions"].(string)
	if !ok {
		return nil, nil
	}

	var containers []struct {
		Environment []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"environment"`
	}
	if err := json.Unmarshal([]byte(definitions), &containers); err != nil {
		return nil, fmt.Errorf("decode ECS container_definitions: %w", err)
	}
	for _, container := range containers {
		for _, variable := range container.Environment {
			if variable.Name == "APP_VERSION" {
				return map[string]string{"ecr_image_tag": variable.Value}, nil
			}
		}
	}
	return nil, nil
}

var lambdaNamePattern = regexp.MustCompile(`\["(\w+)"\]`)

// Lambdas extracts the base path, code version, and per-lambda package versions
// from module.lambdas resources in the Terraform state.
func Lambdas(state *tfjson.State) (map[string]string, error) {
	module := findModule(state, "module.lambdas")
	if module == nil {
		return nil, nil
	}
	var lambdas []*tfjson.StateResource
	for _, child := range module.ChildModules {
		if !strings.Contains(child.Address, "lambda[") {
			continue
		}
		for _, resource := range child.Resources {
			if resource.Type == "aws_lambda_function" {
				lambdas = append(lambdas, resource)
				break
			}
		}
	}
	if len(lambdas) == 0 {
		return nil, nil
	}

	firstKey, ok := resourceString(lambdas[0], "s3_key")
	if !ok {
		return nil, nil
	}
	path := strings.Split(firstKey, "/")
	if len(path) < 2 {
		return nil, nil
	}
	// The TS resolver drops the key filename, then uses the preceding segment
	// as lambda_code_version; all earlier segments form lambda_code_basepath.
	path = path[:len(path)-1]
	codeVersion := path[len(path)-1]
	basePath := strings.Join(path[:len(path)-1], "/")

	zipMap := make(map[string]string, len(lambdas))
	for _, lambda := range lambdas {
		key, hasKey := resourceString(lambda, "s3_key")
		name := lambdaNamePattern.FindStringSubmatch(lambda.Address)
		versions := strings.Split(key, "/")
		if !hasKey || len(name) < 2 || len(versions) == 0 || versions[len(versions)-1] == "" {
			return nil, nil
		}
		zipMap[name[1]] = versions[len(versions)-1]
	}
	encodedZipMap, err := json.Marshal(zipMap)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"lambda_code_version":  codeVersion,
		"lambda_code_basepath": basePath,
		"lambda_code_zip_map":  string(encodedZipMap),
	}, nil
}

func firstResource(state *tfjson.State, resourceType string) *tfjson.StateResource {
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return nil
	}
	for _, resource := range state.Values.RootModule.Resources {
		if resource.Type == resourceType {
			return resource
		}
	}
	return nil
}

func findModule(state *tfjson.State, address string) *tfjson.StateModule {
	if state == nil || state.Values == nil || state.Values.RootModule == nil {
		return nil
	}
	for _, module := range state.Values.RootModule.ChildModules {
		if module.Address == address {
			return module
		}
	}
	return nil
}

func resourceString(resource *tfjson.StateResource, key string) (string, bool) {
	if resource == nil || resource.AttributeValues == nil {
		return "", false
	}
	value, ok := resource.AttributeValues[key].(string)
	return value, ok
}
