// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/cloudmanic/harbor-cli/client"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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

// ===========================================================================
// Email to notes (the inbound email address)
// ===========================================================================

// errInboundEmailUndisclosed is returned when the profile came back with no
// email-to-note keys at all. The API omits them entirely — the keys are absent,
// not empty or false — for any client that may not see the address, because the
// address is a bearer secret that token revocation cannot revoke. An absent key
// therefore means "not disclosed to you" and must never be read as "off".
var errInboundEmailUndisclosed = errors.New("this client was not given an email-to-note address — sign in with 'harbor login' rather than a third-party token")

// errInboundEmailResetUnreadable is returned when a rotation was accepted but the
// new address could not be read back out of the response. The old address is
// already dead at that point, so this must fail loudly: the user needs to know
// they have an address they have not seen yet, not a success line.
var errInboundEmailResetUnreadable = errors.New("the rotation may have gone through, but the new address could not be read from the response — run 'harbor profile inbound-email show' to see your current address, and assume the old one no longer works")

// profileInboundEmailCmd is the parent for the account's email-to-note address.
var profileInboundEmailCmd = &cobra.Command{
	Use:   "inbound-email",
	Short: "Show, rotate, or turn off your email-to-note address",
	Long: `Manage your private email-to-note address.

Mail you forward (or CC/BCC) to the address becomes a note: the subject becomes
the title, the body becomes the note, and attachments come along. Two tokens,
placed anywhere in the subject or body, steer where it lands:

  @Notebook   file the note in that notebook by name — if you have no notebook
              by that name it goes to your default notebook instead
  #tag        apply an existing tag (repeatable) — unknown tags are ignored,
              never created

Treat the address like a password: anyone holding it can create notes in your
account, and revoking a token does not take that away. If it leaks, rotate it
with 'harbor profile inbound-email reset'.`,
}

// profileInboundEmailShowCmd shows the address and whether it accepts mail.
var profileInboundEmailShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show your email-to-note address and whether it is on",
	Long: `Show your private email-to-note address.

The address is shown whether or not it is turned on — switching it off drops
incoming mail but keeps the address, so turning it back on restores the same
one. With --json this prints the whole profile response (the address lives at
.data.inbound_email_address).`,
	Example: `  harbor profile inbound-email show
  harbor profile inbound-email show --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetProfile()
		if err != nil {
			return mapProfileError(err)
		}
		p, err := inboundEmailProfile(data)
		if err != nil {
			return err
		}
		printResult(data, func([]byte) { renderInboundEmailCard(p) })
		return nil
	},
}

// profileInboundEmailResetCmd rotates the address. It is destructive — the old
// address dies the instant the call succeeds — so it is gated behind a typed
// confirmation or --yes.
var profileInboundEmailResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Rotate your email-to-note address (the old one stops working at once)",
	Long: `Rotate your email-to-note address.

The random half of the address is regenerated and the recognizable slug is kept,
so the new address still looks like yours. THE OLD ADDRESS STOPS WORKING
IMMEDIATELY — anything still forwarding mail to it is dropped, so update your
forwarding rules and contacts afterwards.

Your on/off setting is not touched: rotating a disabled address leaves it
disabled. You will be asked to confirm by typing "yes" unless you pass --yes; in
--json or non-interactive use, --yes is required.`,
	Example: `  harbor profile inbound-email reset
  harbor profile inbound-email reset --yes --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if err := inboundEmailConfirmReset(boolFlag(cmd, "yes")); err != nil {
			return err
		}
		data, err := c.ResetInboundEmail()
		if err != nil {
			return mapProfileError(err)
		}
		// A rotation cannot be undone: if the server accepted it, the old address
		// is already dead. So a 2xx we cannot read the new address out of is a
		// failure, never a success line — telling the user "rotated" while
		// withholding the replacement would leave them with no working address
		// and no idea what happened. In --json the raw response still goes to
		// stdout so the caller keeps whatever came back.
		addr, ok := inboundEmailAddress(parseJSON(client.UnwrapData(data)))
		if !ok || addr == "" {
			if jsonOutput {
				printResult(data, func([]byte) {})
			}
			return errInboundEmailResetUnreadable
		}
		printResult(data, func([]byte) { renderInboundEmailReset(addr) })
		return nil
	},
}

// profileInboundEmailEnableCmd starts accepting mail at the address again.
var profileInboundEmailEnableCmd = &cobra.Command{
	Use:     "enable",
	Short:   "Start accepting mail at your email-to-note address",
	Long:    "Turn the address on. You get back the same address you had before — disabling never changed or cleared it.",
	Example: "  harbor profile inbound-email enable",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setInboundEmailEnabled(true)
	},
}

// profileInboundEmailDisableCmd stops mail to the address without giving it up.
var profileInboundEmailDisableCmd = &cobra.Command{
	Use:     "disable",
	Short:   "Stop accepting mail at your email-to-note address",
	Long:    "Turn the address off. Mail sent to it is dropped while it is off, but the address is kept — 'enable' restores the same one. To make the old address unusable instead, use 'reset'.",
	Example: "  harbor profile inbound-email disable",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setInboundEmailEnabled(false)
	},
}

// setInboundEmailEnabled flips the per-user switch through PUT /profile and
// renders the resulting card. The stored address is never touched by this call,
// so toggling off and on again returns the same address.
//
// It runs the same entitlement gate as 'show': a response without the address is
// an ERROR, not a card. Deciding that in a display function would only ever
// print — the command would exit 0 and a script would read "the switch was
// flipped" from a run that changed nothing.
func setInboundEmailEnabled(enabled bool) error {
	c, _, err := loadClientFromConfig()
	if err != nil {
		return err
	}
	data, err := c.SetInboundEmailEnabled(enabled)
	if err != nil {
		return mapProfileError(err)
	}
	p, err := inboundEmailProfile(data)
	if err != nil {
		return err
	}
	printResult(data, func([]byte) { renderInboundEmailCard(p) })
	return nil
}

// inboundEmailProfile parses a profile response and enforces the entitlement
// gate in one place, so every command that reads the card agrees on what the
// same response shape means. Missing keys mean the API did not disclose the
// address to this client, which is a failure to report — never a card to render
// with a blank or "off" address.
func inboundEmailProfile(data []byte) (map[string]any, error) {
	p := parseJSON(client.UnwrapData(data))
	if _, ok := inboundEmailAddress(p); !ok {
		return nil, errInboundEmailUndisclosed
	}
	return p, nil
}

// inboundEmailConfirmReset gates the destructive rotation for the command,
// resolving how the user should be asked (is this a terminal we can prompt on?)
// and delegating the decision to inboundEmailResetGuard.
func inboundEmailConfirmReset(yes bool) error {
	return inboundEmailResetGuard(jsonOutput, term.IsTerminal(int(os.Stdin.Fd())), yes, promptLine)
}

// inboundEmailResetGuard decides whether a rotation may proceed. Interactivity
// and the prompt are parameters rather than ambient state so every branch —
// including the typed-wrong-answer one — is reachable in a test; a destructive,
// non-undoable gate that only real hands can exercise is a gate that silently
// rots.
//
// Rules:
//   - --yes proceeds without asking (that is its whole job).
//   - Otherwise, in --json mode or when stdin is not a terminal (scripts, CI, AI
//     agents), refuse rather than prompt: nobody is there to answer, and the
//     rotation cannot be undone.
//   - On a terminal, require the word "yes" exactly, after saying plainly that
//     the old address dies immediately.
func inboundEmailResetGuard(jsonMode, interactive, yes bool, ask func(string) (string, error)) error {
	if yes {
		return nil
	}
	if jsonMode || !interactive {
		return errors.New("refusing to rotate your email-to-note address without confirmation — pass --yes")
	}
	fmt.Println("This rotates your email-to-note address. The old address stops working IMMEDIATELY and mail sent to it will be dropped.")
	answer, err := ask(`Type "yes" to confirm: `)
	if err != nil {
		return err
	}
	if answer != "yes" {
		return errors.New("aborted — your email-to-note address was not changed")
	}
	return nil
}

// inboundEmailAddress pulls the email-to-note address out of a profile object,
// reporting whether the key was present at all. Absence is meaningful: the API
// omits the field for clients that may not see the address, which is a different
// thing from an empty or disabled address (see errInboundEmailUndisclosed).
func inboundEmailAddress(p map[string]any) (string, bool) {
	if p == nil {
		return "", false
	}
	if _, ok := p["inbound_email_address"]; !ok {
		return "", false
	}
	return str(p, "inbound_email_address"), true
}

// inboundAddressDisplay renders the address for a human. When the switch is off
// the address is struck through and badged rather than hidden — it is still the
// account's address, just inactive, which is how the web Settings card shows it.
func inboundAddressDisplay(addr string, enabled bool) string {
	if addr == "" {
		return dim("(not provisioned yet — try again in a moment)")
	}
	if enabled {
		return addr
	}
	return strike(addr) + "  " + dim("(off)")
}

// renderInboundEmailCard renders the email-to-note card from an already-parsed
// and already-gated profile object (GET /profile, or the PUT that just toggled
// the switch). The address is always shown; the state is spelled out in words so
// it survives --no-color and pipes, where the strikethrough does not.
func renderInboundEmailCard(p map[string]any) {
	addr, _ := inboundEmailAddress(p)
	enabled := boolean(p, "inbound_email_enabled")
	printKV([][2]string{
		{"Address", inboundAddressDisplay(addr, enabled)},
		{"Status", onOff(enabled)},
	})
	if enabled {
		fmt.Println(dim("Forward mail here to create notes. Add @Notebook or #tag in the subject or body to steer where it lands."))
		return
	}
	fmt.Println(dim("Mail sent to this address is dropped while it is off. Run 'harbor profile inbound-email enable' to start accepting it again."))
}

// renderInboundEmailReset renders the rotation result. The response carries the
// new address only — a reset deliberately leaves the on/off switch alone — so
// this says the setting is unchanged rather than implying the address is live.
// It is only reached with an address in hand: see errInboundEmailResetUnreadable.
func renderInboundEmailReset(addr string) {
	fmt.Println("Address rotated. The old address stops working immediately.")
	printKV([][2]string{{"New address", bold(addr)}})
	fmt.Println(dim("Your on/off setting is unchanged. Update anything that forwards mail to the old address."))
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
		case "inbound_email_forbidden":
			return errInboundEmailUndisclosed
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
	)
	// The email-to-note fields are disclosed per client — absent keys mean "not
	// disclosed to you", never "off" — so only show the rows when they are there.
	if addr, ok := inboundEmailAddress(p); ok {
		enabled := boolean(p, "inbound_email_enabled")
		pairs = append(pairs,
			[2]string{"Inbound email", inboundAddressDisplay(addr, enabled)},
			[2]string{"Inbound email enabled", boolMark(enabled)},
		)
	}
	pairs = append(pairs,
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

	profileInboundEmailResetCmd.Flags().Bool("yes", false, "Skip the confirmation prompt (required in --json/non-interactive mode)")
	profileInboundEmailCmd.AddCommand(profileInboundEmailShowCmd, profileInboundEmailResetCmd, profileInboundEmailEnableCmd, profileInboundEmailDisableCmd)

	profileCmd.AddCommand(profileGetCmd, profileUpdateCmd, profileChangePasswordCmd, profileSetAvatarCmd, profileRemoveAvatarCmd, profileConfirmEmailCmd, profileInboundEmailCmd)
	rootCmd.AddCommand(profileCmd)
}
