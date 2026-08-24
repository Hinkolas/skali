package manifest

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidatePlatforms(t *testing.T) {
	t.Parallel()

	valid := parseForValidation(t, `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    platforms: [linux/amd64]
  api:
    build:
      context: ./api
    platforms: [linux/arm64, linux/amd64]
`)
	require.Empty(t, Validate(valid))

	cases := []struct {
		name   string
		source string
		path   string
	}{
		{
			name: "unknown platform",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    platforms: [linux/riscv64]
`,
			path: "applications.web.platforms[0]",
		},
		{
			name: "duplicate platform",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    platforms: [linux/arm64, linux/arm64]
`,
			path: "applications.web.platforms[1]",
		},
		{
			name: "empty platform entry",
			source: `
version: "1"
name: demo
applications:
  web:
    image: example.invalid/web:1
    platforms: [""]
`,
			path: "applications.web.platforms[0]",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			document := parseForValidation(t, testCase.source)
			require.Contains(t, diagnosticPaths(document), testCase.path)
		})
	}
}
