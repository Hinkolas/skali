package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// Installing the completion script. Each shell scans one directory for
// per-command scripts, so a file in the right place needs no startup-file
// edit; zsh is the exception when no site-functions directory on its fpath
// is writable.

func newCompletionInstallCommand() *cobra.Command {
	var shell string
	command := &cobra.Command{
		Use:   "install",
		Short: "Install the completion script where your shell loads it",
		Long: "Writes the completion script for your login shell (or --shell) into the\n" +
			"directory the shell already scans, so no startup file changes: bash\n" +
			"loads ~/.local/share/bash-completion/completions, fish\n" +
			"~/.config/fish/completions, and zsh a site-functions directory on its\n" +
			"fpath, Homebrew's when it is writable. Without one, the script lands in\n" +
			"~/.local/share/zsh/site-functions and the command prints the fpath line\n" +
			"to add before compinit in ~/.zshrc.\n\n" +
			"An installed script is refreshed by skali upgrade. New shells pick it\n" +
			"up; running shells need a restart (zsh: rm ~/.zcompdump first if the\n" +
			"new commands do not appear).",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if shell == "" {
				shell = loginShell(os.Getenv)
			}
			if shell == "" {
				return errors.New("cannot tell your shell from $SHELL; pass --shell bash, zsh, or fish")
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			return installCompletion(command.OutOrStdout(), command.Root(), shell, newCompletionInstaller(home))
		},
	}
	command.Flags().StringVar(&shell, "shell", "", "shell to install for: bash, zsh, or fish (default: your login shell)")
	_ = command.RegisterFlagCompletionFunc("shell", fixedCompletions("bash", "zsh", "fish"))
	return command
}

// loginShell names the user's shell: SKALI_SHELL for scripts, else $SHELL.
func loginShell(getenv func(string) string) string {
	if shell := getenv("SKALI_SHELL"); shell != "" {
		return filepath.Base(shell)
	}
	if shell := getenv("SHELL"); shell != "" {
		return filepath.Base(shell)
	}
	return ""
}

// completionInstaller knows where each shell loads completion scripts from
// for one home directory.
type completionInstaller struct {
	home string
	// dataDir and configDir are the XDG bases, already defaulted.
	dataDir, configDir string
	// siteFunctions are zsh directories on the default fpath of common
	// setups; the first writable one takes the script.
	siteFunctions []string
}

func newCompletionInstaller(home string) completionInstaller {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		configDir = filepath.Join(home, ".config")
	}
	return completionInstaller{
		home:      home,
		dataDir:   dataDir,
		configDir: configDir,
		siteFunctions: []string{
			"/opt/homebrew/share/zsh/site-functions",
			"/usr/local/share/zsh/site-functions",
		},
	}
}

// paths lists where a shell's script may already be installed, and where
// a new install goes: the first writable candidate, else the fallback in
// the user's data directory.
func (i completionInstaller) paths(shell string) (candidates []string, fallback string, err error) {
	switch shell {
	case "bash":
		return nil, filepath.Join(i.dataDir, "bash-completion", "completions", "skali"), nil
	case "fish":
		return nil, filepath.Join(i.configDir, "fish", "completions", "skali.fish"), nil
	case "zsh":
		for _, dir := range i.siteFunctions {
			candidates = append(candidates, filepath.Join(dir, "_skali"))
		}
		return candidates, filepath.Join(i.dataDir, "zsh", "site-functions", "_skali"), nil
	case "powershell":
		return nil, "", errors.New("powershell has no install location; add `skali completion powershell | Out-String | Invoke-Expression` to your profile")
	}
	return nil, "", fmt.Errorf("unsupported shell %q; expected bash, zsh, or fish", shell)
}

// target picks the path a fresh install writes, and the hint the user
// needs when the shell will not find it on its own.
func (i completionInstaller) target(shell string) (path, hint string, err error) {
	candidates, fallback, err := i.paths(shell)
	if err != nil {
		return "", "", err
	}
	for _, candidate := range candidates {
		if probeWritableDir(filepath.Dir(candidate)) == nil {
			return candidate, "", nil
		}
	}
	if shell == "zsh" {
		hint = fmt.Sprintf("add fpath+=(%s) before compinit in ~/.zshrc so zsh finds it",
			tildePath(i.home, filepath.Dir(fallback)))
	}
	return fallback, hint, nil
}

// installed lists the scripts present for a shell, wherever they were put.
func (i completionInstaller) installed(shell string) []string {
	candidates, fallback, err := i.paths(shell)
	if err != nil {
		return nil
	}
	var present []string
	for _, path := range append(candidates, fallback) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			present = append(present, path)
		}
	}
	return present
}

// installCompletion generates the script from root and writes it where the
// shell loads it.
func installCompletion(out io.Writer, root *cobra.Command, shell string, installer completionInstaller) error {
	path, hint, err := installer.target(shell)
	if err != nil {
		return err
	}
	var script bytes.Buffer
	if err := completionScript(root, shell, &script); err != nil {
		return err
	}
	if err := writeCompletionFile(path, script.Bytes()); err != nil {
		return err
	}
	fmt.Fprintf(out, "installed %s completions to %s\n", shell, tildePath(installer.home, path))
	if hint != "" {
		fmt.Fprintln(out, hint)
	}
	fmt.Fprintln(out, "restart your shell to load them")
	return nil
}

func writeCompletionFile(path string, script []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, script, 0o644)
}
