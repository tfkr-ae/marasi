package main

import (
	"errors"
	"io"

	"github.com/spf13/cobra"
	"github.com/tfkr-ae/marasi/skills"
)

func init() {
	rootCmd.AddCommand(skillCmd)
}

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Print the agent skill file for this version",
	Long: `Print the agent skill file that matches this version of marasi.

The skill teaches a coding agent to drive Marasi through this CLI. Save it
where your agent loads skills, for example:

  marasi skill > ~/.agents/skills/marasi/SKILL.md`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if jsonOutput {
			return errors.New("skill does not support --json")
		}
		_, err := io.WriteString(cmd.OutOrStdout(), skills.Marasi)
		return err
	},
}
