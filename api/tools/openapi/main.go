package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/getkin/kin-openapi/openapi2"
	"github.com/getkin/kin-openapi/openapi2conv"
	"github.com/getkin/kin-openapi/openapi3"
	"sigs.k8s.io/yaml"
)

func convert(ctx context.Context, input []byte) ([]byte, error) {
	var source openapi2.T
	if err := yaml.Unmarshal(input, &source); err != nil {
		return nil, fmt.Errorf("read Swagger YAML: %w", err)
	}
	document, err := openapi2conv.ToV3(&source)
	if err != nil {
		return nil, fmt.Errorf("convert to OpenAPI 3: %w", err)
	}

	// Swagger 2 represents both bearer credentials as Authorization API keys.
	// The OpenAPI 3 schemes let clients add the Bearer prefix themselves.
	for name, credential := range map[string]struct{ format, description string }{
		"UserAccessJWT": {"JWT", "Send the user access JWT. Refresh and logout use the refresh token in the JSON body."},
		"DeviceBearer":  {"", "Send the device credential in <device_id>.<secret> form. The API assigns the telemetry_writer capability to the authenticated device key."},
	} {
		ref := document.Components.SecuritySchemes[name]
		if ref == nil || ref.Value == nil {
			return nil, fmt.Errorf("missing security scheme %s", name)
		}
		scheme := ref.Value
		scheme.Type = "http"
		scheme.Scheme = "bearer"
		scheme.BearerFormat = credential.format
		scheme.Description = credential.description
		scheme.Name, scheme.In = "", ""
	}

	// Swaggo's pinned Swagger 2 generator cannot express this strict bound.
	baseline := document.Components.Schemas["BaselineInput"]
	if baseline == nil || baseline.Value == nil {
		return nil, errors.New("missing BaselineInput schema")
	}
	stddev := baseline.Value.Properties["stddev_rms_g"]
	if stddev == nil || stddev.Value == nil || stddev.Value.Min == nil || *stddev.Value.Min != 0 {
		return nil, errors.New("baseline standard deviation schema must have a minimum of zero")
	}
	exclusive := true
	stddev.Value.ExclusiveMin = openapi3.ExclusiveBound{Bool: &exclusive}

	if err := document.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate OpenAPI 3 document: %w", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAPI 3 document: %w", err)
	}
	return yaml.JSONToYAML(encoded)
}

func run(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errors.New("usage: go run . SWAGGER_YAML OPENAPI_YAML")
	}
	input, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	output, err := convert(ctx, input)
	if err != nil {
		return err
	}
	return os.WriteFile(args[1], output, 0o644)
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
