package cmd

import "github.com/spf13/cobra"

// The root help lists the commands in groups, the sandbox lifecycle first, so the long flat list reads by task.
func init() {
	groups := []struct {
		id, title string
		cmds      []*cobra.Command
	}{
		{"sandbox", "Sandboxes:", []*cobra.Command{newCmd, lsCmd, getCmd, setCmd, renameCmd, rmCmd, parkCmd, wakeCmd, resizeCmd, forkCmd, snapshotCmd, metricsCmd}},
		{"inside", "Inside a sandbox:", []*cobra.Command{shellCmd, execCmd, processCmd, psCmd, sessionCmd, fileCmd, sshCmd, sshConfigCmd, sshKeyCmd}},
		{"network", "Ports, access and integrations:", []*cobra.Command{portCmd, openCmd, credentialCmd, policyCmd, attachCmd, detachCmd, attachmentsCmd, integrationCmd, connectorCmd}},
		{"workspace", "Workspace:", []*cobra.Command{baseCmd, templateCmd, opCmd, eventsCmd, webhookCmd, tokenCmd, lineageCmd, exportCmd, importCmd, quarantineCmd, workspaceCmd, mcpCmd}},
		{"account", "Account and setup:", []*cobra.Command{loginCmd, logoutCmd, registerCmd, setupCmd}},
	}
	for _, g := range groups {
		rootCmd.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
		for _, c := range g.cmds {
			c.GroupID = g.id
		}
	}
	rootCmd.SetHelpCommandGroupID("account")
	rootCmd.SetCompletionCommandGroupID("account")

	// `zbox workspace --help` groups its subcommands the way its Long names them.
	subgroups := []struct {
		id, title string
		cmds      []*cobra.Command
	}{
		{"known", "Workspaces known here:", []*cobra.Command{workspaceLsCmd, switchCmd, workspaceAuthorizeCmd, workspaceUseCmd, workspaceForgetCmd}},
		{"current", "The workspace in use:", []*cobra.Command{statusCmd, workspacePolicyCmd, workspaceDomainCmd, workspaceKeyCmd, consoleCmd}},
		{"life", "Its VM and service:", []*cobra.Command{workspaceStartCmd, workspaceStopCmd, workspaceRestartCmd, workspaceReloadCmd, workspaceResizeCmd, teardownCmd}},
	}
	for _, g := range subgroups {
		workspaceCmd.AddGroup(&cobra.Group{ID: g.id, Title: g.title})
		for _, c := range g.cmds {
			c.GroupID = g.id
		}
	}
}
