package main

import "testing"

func TestGateAuthSchemaPinSupportsDevAndProductionAuthDescriptors(t *testing.T) {
	for _, environment := range []string{"dev", "prod"} {
		if err := validateAuthSchemaPin("auth", environment, true, 3); err != nil {
			t.Errorf("schema-3 %s Auth descriptor: %v", environment, err)
		}
	}
}

func TestGateAuthSchemaPinPreservesSchemaFourTargets(t *testing.T) {
	for _, environment := range []string{"local", "dev", "prod"} {
		if err := validateAuthSchemaPin("auth", environment, true, 4); err != nil {
			t.Errorf("schema-4 %s Auth descriptor: %v", environment, err)
		}
	}
}

func TestGateAuthSchemaPinKeepsOtherSelectorsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, target, environment string
		descriptor                bool
		version                   int
	}{
		{name: "local schema 3", target: "auth", environment: "local", descriptor: true, version: 3},
		{name: "BFF schema 3", target: "bff", environment: "dev", descriptor: true, version: 3},
		{name: "schema 3 without descriptor", target: "auth", environment: "prod", version: 3},
		{name: "unknown schema", target: "auth", environment: "prod", descriptor: true, version: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateAuthSchemaPin(tc.target, tc.environment, tc.descriptor, tc.version); err == nil {
				t.Fatalf("accepted unsupported selector: %+v", tc)
			}
		})
	}
}
