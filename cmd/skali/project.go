package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/manifest"
)

func newValidateCmd() *cobra.Command {
	var manifestPath string
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate a Skali project manifest",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, document, err := loadAndCompile(manifestPath)
			if err != nil {
				return err
			}
			fmt.Fprintf(command.OutOrStdout(),
				"valid %s\n  project: %s\n  applications: %d\n  databases: %d\n  buckets: %d\n  backups: %d\n  required variables: %d\n  definition: sha256:%s\n",
				document.Path,
				result.Definition.Name,
				len(result.Definition.Applications),
				len(result.Definition.Databases),
				len(result.Definition.Buckets),
				len(result.Definition.Backups),
				len(result.Definition.RequiredVariables),
				result.Hash,
			)
			return nil
		},
	}
	command.Flags().StringVar(&manifestPath, "manifest", "", "manifest path; defaults to skali.yml or skali.yaml")
	return command
}

func newCompileCmd() *cobra.Command {
	var (
		manifestPath string
		target       string
		envFile      string
		namespace    string
		images       []string
	)
	command := &cobra.Command{
		Use:   "compile",
		Short: "Compile a Skali manifest into canonical IR or Kubernetes objects",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, _, err := loadAndCompile(manifestPath)
			if err != nil {
				return err
			}
			switch target {
			case "definition":
				data, err := json.MarshalIndent(result, "", "  ")
				if err != nil {
					return fmt.Errorf("encode compiled definition: %w", err)
				}
				_, err = fmt.Fprintln(command.OutOrStdout(), string(data))
				return err
			case "kubernetes":
				values, err := readValues(envFile)
				if err != nil {
					return err
				}
				buildImages, err := parseImages(images)
				if err != nil {
					return err
				}
				if namespace == "" {
					namespace = "skali-" + result.Definition.Name
				}
				objects, err := kubernetes.Render(result, kubernetes.Options{
					Namespace:   namespace,
					Variables:   values,
					BuildImages: buildImages,
				})
				if err != nil {
					return err
				}
				data, err := kubernetes.MarshalYAML(objects)
				if err != nil {
					return err
				}
				_, err = command.OutOrStdout().Write(data)
				return err
			default:
				return fmt.Errorf("unsupported compile target %q; use definition or kubernetes", target)
			}
		},
	}
	command.Flags().StringVar(&manifestPath, "manifest", "", "manifest path; defaults to skali.yml or skali.yaml")
	command.Flags().StringVar(&target, "target", "definition", "compile target: definition or kubernetes")
	command.Flags().StringVar(&envFile, "env-file", "", "dotenv file used to resolve project variables for Kubernetes rendering")
	command.Flags().StringVar(&namespace, "namespace", "", "Kubernetes namespace used for rendering")
	command.Flags().StringArrayVar(&images, "image", nil, "prepared image for a build application, as service=reference")
	return command
}

func loadAndCompile(explicit string) (*compiler.Result, *manifest.Document, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return nil, nil, fmt.Errorf("get working directory: %w", err)
	}
	path, err := manifest.Discover(explicit, workingDirectory)
	if err != nil {
		return nil, nil, err
	}
	document, err := manifest.ParseFile(path)
	if err != nil {
		return nil, nil, err
	}
	result, err := compiler.Compile(document)
	if err != nil {
		return nil, nil, err
	}
	return result, document, nil
}

func readValues(path string) (map[string]string, error) {
	if path == "" {
		return map[string]string{}, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve --env-file: %w", err)
	}
	values, err := godotenv.Read(absolute)
	if err != nil {
		return nil, fmt.Errorf("read environment file %s: %w", absolute, err)
	}
	return values, nil
}

func parseImages(values []string) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		service, image, ok := strings.Cut(value, "=")
		if !ok || service == "" || image == "" {
			return nil, fmt.Errorf("invalid --image %q; expected service=reference", value)
		}
		if _, exists := result[service]; exists {
			return nil, fmt.Errorf("duplicate --image for service %q", service)
		}
		result[service] = image
	}
	return result, nil
}
