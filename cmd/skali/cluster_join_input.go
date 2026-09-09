package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/Hinkolas/skali/internal/clusterstate"
	"github.com/Hinkolas/skali/internal/installer"
)

func validateJoinToken(value string) error {
	value = clusterstate.NormalizeToken(value)
	if reconciledToken(value) {
		_, err := clusterstate.ParseToken(value)
		return err
	}
	_, err := installer.InspectJoinToken(value)
	return err
}

func joinTokenInput(ctx context.Context, token, file string, tokenSet, fileSet bool, env string, stdin io.Reader) (string, error) {
	if tokenSet && fileSet {
		return "", fmt.Errorf("supply --token or --token-file, not both")
	}
	switch {
	case tokenSet:
	case fileSet:
		var data []byte
		var err error
		if file == "-" {
			data, err = io.ReadAll(io.LimitReader(stdin, 16385))
		} else {
			data, err = readHostFile(ctx, file)
		}
		if err != nil {
			return "", fmt.Errorf("read join token: %w", err)
		}
		token = string(data)
	default:
		token = env
	}
	if len(token) > 16384 {
		return "", fmt.Errorf("join token exceeds 16384 characters")
	}
	token = clusterstate.NormalizeToken(token)
	if token == "" {
		if tokenSet || fileSet {
			return "", fmt.Errorf("join token is empty")
		}
		return "", nil // Existing enrollment may supply durable credentials or a saved token.
	}
	if err := validateJoinToken(token); err != nil {
		return "", err
	}
	return token, nil
}

func promptJoinToken(ctx context.Context, reader *bufio.Reader) (string, error) {
	return promptSession(os.Stdout, reader).VisibleToken(ctx, validateJoinToken)
}

func joinCapabilities(ctx context.Context, caps, allowed []string, interactive bool) ([]string, error) {
	if len(caps) > 0 {
		return caps, nil
	}
	if len(allowed) > 0 {
		return append([]string(nil), allowed...), nil
	}
	if interactive {
		return promptCapabilities(ctx, os.Stdout, bufio.NewReader(os.Stdin))
	}
	return nil, fmt.Errorf("this invitation does not specify default capabilities; pass --capabilities")
}
