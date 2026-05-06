package cli

import (
	"github.com/spf13/cobra"

	"github.com/SHC-Labs/drift/internal/hook"
)

// newInternalCmd is the parent for hidden subcommands the AI client hook
// scripts call. End users never see these in --help; cobra's Hidden flag
// suppresses them.
func newInternalCmd() *cobra.Command {
	internal := &cobra.Command{
		Use:    "internal",
		Hidden: true,
		Short:  "Internal commands invoked by hook scripts. Not for human use.",
	}
	hookCmd := &cobra.Command{
		Use:    "hook",
		Hidden: true,
		Short:  "Hook handlers fired by the AI client",
	}
	hookCmd.AddCommand(
		&cobra.Command{
			Use:    "prompt-submit",
			Hidden: true,
			Short:  "UserPromptSubmit hook handler",
			RunE: func(cmd *cobra.Command, args []string) error {
				return hook.PromptSubmit(cmd.Context(), cmd.OutOrStdout())
			},
		},
		&cobra.Command{
			Use:    "post-tool-use",
			Hidden: true,
			Short:  "PostToolUse hook handler",
			RunE: func(cmd *cobra.Command, args []string) error {
				return hook.PostToolUse(cmd.Context(), cmd.InOrStdin())
			},
		},
	)
	internal.AddCommand(hookCmd)
	return internal
}
