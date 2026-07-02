package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Hinkolas/skali/internal/cliconfig"
)

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Manage masters (kubectl-style contexts)",
	}
	cmd.AddCommand(newContextListCmd(), newContextUseCmd(), newContextAddCmd())
	return cmd
}

func newContextListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List contexts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			if len(cfg.Contexts) == 0 {
				fmt.Println("no contexts; run `skali auth login --master <url>`")
				return nil
			}
			names := make([]string, 0, len(cfg.Contexts))
			for name := range cfg.Contexts {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				marker := " "
				if name == cfg.CurrentContext {
					marker = "*"
				}
				loggedIn := ""
				if cfg.Contexts[name].Token != "" {
					loggedIn = "  [logged in]"
				}
				fmt.Printf("%s %s  %s%s\n", marker, name, cfg.Contexts[name].Master, loggedIn)
			}
			return nil
		},
	}
}

func newContextUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Switch the current context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			name := args[0]
			if cfg.Contexts[name] == nil {
				return fmt.Errorf("context %q does not exist; run `skali context list`", name)
			}
			cfg.CurrentContext = name
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("switched to context %q\n", name)
			return nil
		},
	}
}

func newContextAddCmd() *cobra.Command {
	var master string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a context for a master",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cliconfig.Load()
			if err != nil {
				return err
			}
			name := args[0]
			if cfg.Contexts[name] != nil {
				return fmt.Errorf("context %q already exists", name)
			}
			cfg.Contexts[name] = &cliconfig.Context{Master: strings.TrimRight(master, "/")}
			if cfg.CurrentContext == "" {
				cfg.CurrentContext = name
			}
			if err := cliconfig.Save(cfg); err != nil {
				return err
			}
			fmt.Printf("added context %q (%s)\n", name, master)
			return nil
		},
	}
	cmd.Flags().StringVar(&master, "master", "", "master URL, e.g. https://skali.example.com (required)")
	_ = cmd.MarkFlagRequired("master")
	return cmd
}
