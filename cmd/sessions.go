// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// sessionsCmd is the parent for session (logged-in device) management.
var sessionsCmd = &cobra.Command{
	Use:     "sessions",
	Short:   "View and revoke active login sessions",
	GroupID: groupAccount,
	Long:    "A session is one login lineage on one device. List where you are signed in and revoke sessions individually or in bulk.",
}

// sessionsListCmd lists active sessions.
var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions (marks the current one)",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListSessions(pagingParams(cmd))
		if err != nil {
			return err
		}
		printResult(data, displaySessions)
		return nil
	},
}

// sessionsRevokeCmd revokes a single session.
var sessionsRevokeCmd = &cobra.Command{
	Use:     "revoke <family-id>",
	Short:   "Revoke a single session by id",
	Args:    cobra.ExactArgs(1),
	Example: "  harbor sessions revoke fam_3f9c...",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.RevokeSession(args[0]); err != nil {
			return err
		}
		fmt.Println("Session revoked.")
		return nil
	},
}

// sessionsRevokeOthersCmd signs out every other device.
var sessionsRevokeOthersCmd = &cobra.Command{
	Use:   "revoke-others",
	Short: "Revoke all sessions except the current one",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.RevokeSessions("current"); err != nil {
			return err
		}
		fmt.Println("All other sessions revoked.")
		return nil
	},
}

// sessionsRevokeAllCmd signs out everywhere (including this device).
var sessionsRevokeAllCmd = &cobra.Command{
	Use:   "revoke-all",
	Short: "Revoke all sessions, including this one",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.RevokeSessions(""); err != nil {
			return err
		}
		fmt.Println("All sessions revoked. You will need to log in again.")
		return nil
	},
}

// displaySessions renders the session list, flagging the current session.
func displaySessions(data []byte) {
	items := client.CollectionItems(data)
	headers := []string{"ID", "DEVICE", "DEVICE NAME", "IP", "LAST SEEN", "CURRENT"}
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		s := parseJSON(raw)
		rows = append(rows, []string{
			str(s, "id"),
			str(s, "device_id"),
			truncate(str(s, "device_name"), 24),
			str(s, "ip"),
			epochMS(num(s, "last_seen_at")),
			boolMark(boolean(s, "current")),
		})
	}
	printTable(headers, rows)
	printPagingFooter(data)
}

func init() {
	addPagingFlags(sessionsListCmd)
	sessionsCmd.AddCommand(sessionsListCmd, sessionsRevokeCmd, sessionsRevokeOthersCmd, sessionsRevokeAllCmd)
	rootCmd.AddCommand(sessionsCmd)
}
