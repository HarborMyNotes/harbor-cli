// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/spf13/cobra"
)

// profileCmd is the parent for account profile self-management.
var profileCmd = &cobra.Command{
	Use:     "profile",
	Short:   "Manage your account profile",
	GroupID: groupAccount,
	Long:    "Read and update your profile (name, locale, timezone), change your login email or password, and manage your avatar.",
}

// profileGetCmd shows the current profile.
var profileGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show your profile",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetProfile()
		if err != nil {
			return err
		}
		printResult(data, displayProfile)
		return nil
	},
}

// profileUpdateCmd updates profile fields. An email change is staged and needs
// the current password.
var profileUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update your profile (name, locale, timezone, or email)",
	Long:  "Update profile fields. Changing --email stages the new address and requires your current password (you confirm it via the link emailed to the new address).",
	Example: `  harbor profile update --name "Jane D." --timezone America/New_York
  harbor profile update --email new@example.com`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		body := map[string]any{}
		addStringIfChanged(cmd, body, "name", "name")
		addStringIfChanged(cmd, body, "locale", "locale")
		addStringIfChanged(cmd, body, "timezone", "timezone")
		if cmd.Flags().Changed("email") {
			body["email"] = stringFlag(cmd, "email")
			pw, perr := promptPassword("Current password: ")
			if perr != nil {
				return perr
			}
			body["current_password"] = pw
		}
		if len(body) == 0 {
			return errors.New("nothing to update — pass --name, --locale, --timezone, or --email")
		}
		data, err := c.UpdateProfile(body)
		if err != nil {
			return mapProfileError(err)
		}
		printResult(data, displayProfile)
		if cmd.Flags().Changed("email") {
			fmt.Println(dim("A confirmation link was sent to the new address; the email changes only after you confirm it."))
		}
		return nil
	},
}

// profileChangePasswordCmd changes the account password.
var profileChangePasswordCmd = &cobra.Command{
	Use:   "change-password",
	Short: "Change your password (prompts for current + new)",
	Long:  "Change your password. Prompts for your current and new password (both hidden). On success, all other sessions are signed out.",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		current, err := promptPassword("Current password: ")
		if err != nil {
			return err
		}
		next, err := promptPassword("New password: ")
		if err != nil {
			return err
		}
		confirm, err := promptPassword("Confirm new password: ")
		if err != nil {
			return err
		}
		if next != confirm {
			return errors.New("passwords do not match")
		}
		data, err := c.ChangePassword(current, next)
		if err != nil {
			return mapProfileError(err)
		}
		printResult(data, displayMessage("Password changed. Other sessions have been signed out."))
		return nil
	},
}

// profileSetAvatarCmd points the avatar at an uploaded image blob.
var profileSetAvatarCmd = &cobra.Command{
	Use:     "set-avatar",
	Short:   "Set your avatar to an already-uploaded image (by hash)",
	Long:    "Set your avatar to an image you already uploaded with 'harbor files upload', referenced by its content sha256 hash.",
	Example: "  harbor profile set-avatar --hash e3b0c442...b855",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		hash := stringFlag(cmd, "hash")
		if hash == "" {
			return errors.New("--hash is required (the sha256 of an uploaded image)")
		}
		data, err := c.SetAvatar(hash)
		if err != nil {
			return err
		}
		printResult(data, displayProfile)
		return nil
	},
}

// profileRemoveAvatarCmd clears the avatar.
var profileRemoveAvatarCmd = &cobra.Command{
	Use:   "remove-avatar",
	Short: "Remove your avatar",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.RemoveAvatar(); err != nil {
			return err
		}
		fmt.Println("Avatar removed.")
		return nil
	},
}

// profileConfirmEmailCmd confirms a staged email change (public).
var profileConfirmEmailCmd = &cobra.Command{
	Use:     "confirm-email",
	Short:   "Confirm a staged email change with a token",
	Example: "  harbor profile confirm-email --token ec_...",
	RunE: func(cmd *cobra.Command, args []string) error {
		token := stringFlag(cmd, "token")
		if token == "" {
			return errors.New("--token is required")
		}
		data, err := newAnonymousClient().ConfirmEmailChange(token)
		if err != nil {
			return err
		}
		printResult(data, displayMessage("Email change confirmed."))
		return nil
	},
}

// mapProfileError gives friendly messages for profile-specific codes.
func mapProfileError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "reauth_required":
			return errors.New("incorrect current password")
		case "email_taken":
			return errors.New("that email is already in use")
		case "password_reused":
			return errors.New("the new password must differ from your current one")
		}
	}
	return err
}

// displayProfile renders the profile detail view.
func displayProfile(data []byte) {
	p := parseJSON(client.UnwrapData(data))
	if p == nil {
		fmt.Println(string(data))
		return
	}
	pairs := [][2]string{
		{"ID", bold(str(p, "id"))},
		{"Name", str(p, "name")},
		{"Email", str(p, "email")},
		{"Email verified", boolMark(boolean(p, "email_verified"))},
	}
	if pe := str(p, "pending_email"); pe != "" {
		pairs = append(pairs, [2]string{"Pending email", pe})
	}
	pairs = append(pairs,
		[2]string{"Locale", str(p, "locale")},
		[2]string{"Timezone", str(p, "timezone")},
		[2]string{"Created", epochMS(num(p, "created_at"))},
		[2]string{"Updated", epochMS(num(p, "updated_at"))},
	)
	printKV(pairs)
}

func init() {
	profileUpdateCmd.Flags().String("name", "", "Display name")
	profileUpdateCmd.Flags().String("locale", "", "Locale (e.g. en-US)")
	profileUpdateCmd.Flags().String("timezone", "", "IANA timezone (e.g. America/New_York)")
	profileUpdateCmd.Flags().String("email", "", "New login email (staged; needs current password)")

	profileSetAvatarCmd.Flags().String("hash", "", "sha256 of an already-uploaded image")
	profileConfirmEmailCmd.Flags().String("token", "", "Email-change confirmation token (required)")

	profileCmd.AddCommand(profileGetCmd, profileUpdateCmd, profileChangePasswordCmd, profileSetAvatarCmd, profileRemoveAvatarCmd, profileConfirmEmailCmd)
	rootCmd.AddCommand(profileCmd)
}
