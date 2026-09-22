package main

import (
	"fmt"
	"io"
	"os"

	"github.com/Hinkolas/skali/internal/checkout"
	"github.com/Hinkolas/skali/internal/compiler"
	"github.com/Hinkolas/skali/internal/manifest"
)

// compileReviewed is the local authoring boundary. The compiler and server
// remain pure: neither can accidentally read checkout state or write files.
func compileReviewed(document *manifest.Document, stderr io.Writer) (*compiler.Result, error) {
	revision, known, err := checkout.Review(document.Path)
	if err != nil {
		return nil, err
	}
	if known {
		if diagnostics := manifest.ReviewChanges(document, revision); len(diagnostics) > 0 {
			return nil, diagnostics
		}
		if revision > manifest.CurrentRevision() {
			fmt.Fprintf(stderr, "note: %s was locally reviewed through manifest revision %d; this compiler understands %d; retaining local history\n", document.Path, revision, manifest.CurrentRevision())
		}
	}
	result, err := compiler.Compile(document)
	if err != nil {
		return nil, err
	}
	if !known {
		stored, err := checkout.SaveReview(document.Path, manifest.CurrentRevision(), true)
		if err != nil {
			fmt.Fprintf(stderr, "warning: could not record local manifest review for %s: %v\n", document.Path, err)
		} else if diagnostics := manifest.ReviewChanges(document, stored); len(diagnostics) > 0 {
			return nil, diagnostics
		}
	}
	return result, nil
}

func reviewOutput(outputs []io.Writer) io.Writer {
	if len(outputs) > 0 {
		return outputs[0]
	}
	return os.Stderr
}
