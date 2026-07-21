package topology

import (
	"errors"
	"testing"
)

func TestValidateInput(t *testing.T) {
	valid := DependencyInput{
		NodeID:              "local",
		DependentServiceID:  "immich-server",
		DependencyServiceID: "immich-postgres",
		Label:               " requires database ",
	}
	if err := ValidateInput(valid); err != nil {
		t.Fatalf("valid topology input: %v", err)
	}
	if got := NormalizeInput(valid).Label; got != "requires database" {
		t.Fatalf("normalized label = %q", got)
	}

	tests := []struct {
		name  string
		input DependencyInput
		want  error
	}{
		{name: "missing node", input: DependencyInput{DependentServiceID: "a", DependencyServiceID: "b"}, want: ErrInvalidDependency},
		{name: "self edge", input: DependencyInput{NodeID: "local", DependentServiceID: "a", DependencyServiceID: "a"}, want: ErrSelfDependency},
		{name: "invalid service id", input: DependencyInput{NodeID: "local", DependentServiceID: "a b", DependencyServiceID: "b"}, want: ErrInvalidDependency},
		{name: "multiline label", input: DependencyInput{NodeID: "local", DependentServiceID: "a", DependencyServiceID: "b", Label: "one\ntwo"}, want: ErrInvalidDependency},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateInput(test.input); !errors.Is(err, test.want) {
				t.Fatalf("ValidateInput() error = %v, want %v", err, test.want)
			}
		})
	}
}
