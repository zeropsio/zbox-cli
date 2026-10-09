package cmd

import "github.com/spf13/cobra"

// The root help lists the commands in groups, the sandbox lifecycle first, so the long flat list reads by task.
func init() {
	groups := []struct {
		id, title string
		cmds      []*cobra.Command
	}{
		{"sandbox", "Sandboxes:", []*cobra.Command{newCmd, lsCmd, getCmd, setCmd, rmCmd, parkCmd, wakeCmd, forkCmd, snapshotCmd, metricsCmd}},
		{"inside", "Inside a sandbox:", []*cobra.Command{shellCmd, execCmd, processCmd, sessionCmd, fileCmd, sshCmd, sshKeyCmd}},
		{"network", "Ports, access and integrations:", []*cobra.Command{portCmd, openCmd, credentialCmd, policyCmd, attachCmd, detachCmd, attachmentsCmd, integrationCmd, connectorCmd}},
		{"workspace", "Workspace:", []*cobra.Command{baseCmd, templateCmd, opCmd, eventsCmd, webhookCmd, lineageCmd, exportCmd, importCmd, quarantineCmd, workspaceCmd, consoleCmd, switchCmd, statusCmd, mcpCmd}},
		{"account", "Account and setup:", []*cobra.Command{loginCmd, logoutCmd, registerCmd, setupCmd, teardownCmd}},
	}
	for _, g := range groups {
		rootCmd.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
		for _, c := range g.cmds {
			c.GroupID = g.id
		}
	}
	rootCmd.SetHelpCommandGroupID("account")
	rootCmd.SetCompletionCommandGroupID("account")
}
