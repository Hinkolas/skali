package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/kubernetes"
	"github.com/Hinkolas/skali/internal/manifest"
	"github.com/Hinkolas/skali/internal/revision"
	"github.com/Hinkolas/skali/internal/values"
	versionpkg "github.com/Hinkolas/skali/internal/version"
)

func newValidateCmd() *cobra.Command {
	var (
		manifestPath  string
		envFile       string
		ignoreUnknown bool
	)
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate a Skali project manifest and optionally an environment file",
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
			if envFile != "" {
				resolved, path, err := resolveValues(result, envFile, ignoreUnknown)
				if err != nil {
					return err
				}
				fmt.Fprintf(command.OutOrStdout(), "  values %s: %d plain, %d secret\n",
					path, len(resolved.Plain), len(resolved.Secret))
			}
			return nil
		},
	}
	command.Flags().StringVar(&manifestPath, "manifest", "", "manifest path; defaults to skali.yml or skali.yaml")
	command.Flags().StringVar(&envFile, "env-file", "", "dotenv file validated against the manifest's value requirements")
	command.Flags().BoolVar(&ignoreUnknown, "ignore-unknown-values", false, "drop environment-file keys the manifest does not require")
	return command
}

func newCompileCmd() *cobra.Command {
	var (
		manifestPath  string
		target        string
		envFile       string
		ignoreUnknown bool
		environment   string
		namespace     string
		images        []string
	)
	command := &cobra.Command{
		Use:   "compile",
		Short: "Compile a Skali manifest into canonical IR, a revision, or Kubernetes objects",
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
			case "revision":
				resolved, _, err := resolveValues(result, envFile, ignoreUnknown)
				if err != nil {
					return err
				}
				buildImages, err := parseImages(images)
				if err != nil {
					return err
				}
				artifacts, err := artifactsForRevision(result, buildImages)
				if err != nil {
					return err
				}
				built, err := revision.Build(revision.Input{
					Result:          result,
					Environment:     environment,
					Values:          resolved,
					Artifacts:       artifacts,
					CompilerVersion: versionpkg.Version,
				})
				if err != nil {
					return err
				}
				data, err := json.MarshalIndent(built, "", "  ")
				if err != nil {
					return fmt.Errorf("encode revision: %w", err)
				}
				_, err = fmt.Fprintln(command.OutOrStdout(), string(data))
				return err
			case "kubernetes":
				resolved, _, err := resolveValues(result, envFile, ignoreUnknown)
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
					Variables:   resolved.Merged(),
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
				return fmt.Errorf("unsupported compile target %q; use definition, revision, or kubernetes", target)
			}
		},
	}
	command.Flags().StringVar(&manifestPath, "manifest", "", "manifest path; defaults to skali.yml or skali.yaml")
	command.Flags().StringVar(&target, "target", "definition", "compile target: definition, revision, or kubernetes")
	command.Flags().StringVar(&environment, "environment", "local", "environment name recorded in revision output")
	command.Flags().StringVar(&envFile, "env-file", "", "dotenv file used to resolve project variables for Kubernetes rendering")
	command.Flags().BoolVar(&ignoreUnknown, "ignore-unknown-values", false, "drop environment-file keys the manifest does not require")
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

// resolveValues imports and validates the selected environment file against
// the compiled definition. Without a file it resolves an empty value set, so
// definitions whose values all carry defaults still render.
func resolveValues(result *compiler.Result, envFile string, ignoreUnknown bool) (values.Resolved, string, error) {
	file := &values.File{Path: "(none)", Values: map[string]string{}}
	if envFile != "" {
		parsed, err := values.ParseFile(envFile)
		if err != nil {
			return values.Resolved{}, "", err
		}
		file = parsed
	}
	resolved, err := values.Resolve(result.Definition.RequiredVariables, file, values.Options{IgnoreUnknown: ignoreUnknown})
	if err != nil {
		return values.Resolved{}, "", fmt.Errorf("%s: %w", file.Path, err)
	}
	return resolved, file.Path, nil
}

// artifactsForRevision converts pinned --image references into revision
// artifacts. Revisions require resolved digests, so unpinned references are
// rejected rather than resolved over the network.
func artifactsForRevision(result *compiler.Result, images map[string]string) (map[string]revision.Artifact, error) {
	artifacts := make(map[string]revision.Artifact, len(images))
	for service, reference := range images {
		base, digest, ok := strings.Cut(reference, "@")
		if !ok || !strings.HasPrefix(digest, "sha256:") {
			return nil, fmt.Errorf("--image %s must be pinned as reference@sha256:... for revision output", service)
		}
		kind := revision.KindImport
		if application, exists := result.Definition.Applications[service]; exists && application.Source.Kind == "build" {
			kind = revision.KindBuildLocal
		}
		artifacts[service] = revision.Artifact{Reference: base, Digest: digest, Kind: kind}
	}
	return artifacts, nil
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
