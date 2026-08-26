package bundle

import (
	"strings"
	"testing"
)

func TestResetPasswordYAMLParses(t *testing.T) {
	for _, disable := range []bool{false, true} {
		source := resetPasswordYAML("ghcr.io/hinkolas/skalid:v0.1.0", "admin@example.com", "s3cret", disable)
		objects, err := ParseManifest([]byte(source))
		if err != nil {
			t.Fatalf("parse (disable2fa=%v): %v", disable, err)
		}
		if len(objects) != 2 {
			t.Fatalf("expected a secret and a job, got %d objects", len(objects))
		}
		if objects[0].GetKind() != "Secret" || objects[1].GetKind() != "Job" {
			t.Fatalf("unexpected kinds %s, %s", objects[0].GetKind(), objects[1].GetKind())
		}
		for _, object := range objects {
			if object.GetName() != resetPasswordName || object.GetNamespace() != Namespace {
				t.Fatalf("unexpected identity %s/%s", object.GetNamespace(), object.GetName())
			}
		}
		if strings.Contains(source, "--disable-2fa") != disable {
			t.Fatalf("disable2fa=%v not reflected in the job command", disable)
		}
		if strings.Contains(source, "s3cret") {
			t.Fatal("the password must only appear base64-encoded")
		}
	}
}
