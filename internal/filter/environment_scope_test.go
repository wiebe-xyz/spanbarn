package filter

import (
	"context"
	"testing"
)

func TestEnvironmentScope(t *testing.T) {
	base := context.Background()
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"all", ""}, {"production", "production"}, {"staging", "staging"},
	} {
		if got := EnvironmentScope(WithEnvironmentScope(base, tc.in)); got != tc.want {
			t.Errorf("scope %q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnvironmentOf(t *testing.T) {
	for _, tc := range []struct{ name, attrs, want string }{
		{"none", ``, ""},
		{"no attribute", `{"http.method":"GET"}`, ""},
		{"production", `{"deployment.environment":"production","host":"a"}`, "production"},
		{"mixed case", `{"deployment.environment":" Staging "}`, "staging"},
		{"only the new name", `{"deployment.environment.name":"staging"}`, ""},
		{"both names", `{"deployment.environment":"testing","deployment.environment.name":"staging"}`, "testing"},
		{"not a string", `{"deployment.environment":7}`, ""},
		{"escaped", `{"deployment.environment":"a\"b"}`, ""},
		{"unterminated", `{"deployment.environment":"stag`, ""},
		{"empty value", `{"deployment.environment":""}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := EnvironmentOf(tc.attrs); got != tc.want {
				t.Errorf("EnvironmentOf(%s) = %q, want %q", tc.attrs, got, tc.want)
			}
		})
	}
}

func TestFloorOperation(t *testing.T) {
	staging := `{"deployment.environment":"staging"}`
	for _, tc := range []struct{ name, attrs, want string }{
		{"no attributes", ``, "GET /x"},
		{"production", `{"deployment.environment":"production"}`, "GET /x"},
		{"upper case production", `{"deployment.environment":"Production"}`, "GET /x"},
		{"staging", staging, "staging\x00GET /x"},
		{"upper case staging", `{"deployment.environment":" STAGING "}`, "staging\x00GET /x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FloorOperation(tc.attrs, "GET /x"); got != tc.want {
				t.Errorf("FloorOperation(%s) = %q, want %q", tc.attrs, got, tc.want)
			}
		})
	}
}
