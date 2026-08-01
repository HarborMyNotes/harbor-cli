// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/spf13/cobra"
)

// planLimitCode is the API error code returned when a write is blocked by an
// entitlement gate. It covers BOTH the per-resource plan cap (details carry
// gate=plan_limit plus resource/used/limit) and the whole-account read-only
// freeze (gate=account_read_only, no per-resource numbers) — the API
// deliberately reuses one code so a client needs a single branch.
const planLimitCode = "plan_limit_reached"

// planLimitReadOnlyGate is the details value that distinguishes the
// whole-account freeze from a single resource hitting its cap.
const planLimitReadOnlyGate = "account_read_only"

// defaultUpgradePath is where the web app's plan screen lives. It is only a
// fallback: the API sends the real path in details.upgrade_url, which is
// configurable server-side.
const defaultUpgradePath = "/settings/plan"

// planResourceOrder is the order usage rows are shown in — the order a person
// runs into the caps, not alphabetical. Any resource the API adds later is
// appended after these, so a new cap still shows up without a CLI release.
var planResourceOrder = []string{"notes", "notebooks", "tags", "files", "tasks"}

// planResourcePlural maps the singular resource name used in a plan-limit
// error's details (resource: "notebook") to the plural used everywhere else,
// including the GET /usage keys.
var planResourcePlural = map[string]string{
	"note":     "notes",
	"notebook": "notebooks",
	"tag":      "tags",
	"file":     "files",
	"task":     "tasks",
}

// ===========================================================================
// Commands
// ===========================================================================

// usageCmd shows how much of each plan-capped resource the account is using.
var usageCmd = &cobra.Command{
	Use:     "usage",
	Short:   "Show how much of your plan you are using (notes, notebooks, tags, files, tasks)",
	GroupID: groupAccount,
	Args:    cobra.NoArgs,
	Long: `Show your usage against your plan's limits.

Every plan-capped resource is listed with what you have used and what your plan
allows; an unlimited resource shows ∞. Counts include items in the trash —
trashing a note does not free a slot, but expunging it does.

If your account is read-only (you are over your plan's limits, or a payment
lapsed), that is called out here too: creating and editing are blocked until you
are back under the limits or you upgrade.`,
	Example: `  harbor usage
  harbor usage --json | jq '.data.usage.notes'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetUsage()
		if err != nil {
			return err
		}
		printResult(data, displayUsage)
		return nil
	},
}

// planCmd shows the account's current plan and entitlement. It doubles as the
// parent for `harbor plan list`.
var planCmd = &cobra.Command{
	Use:     "plan",
	Short:   "Show your current plan (and, with 'list', the plans available to you)",
	GroupID: groupAccount,
	Args:    cobra.NoArgs,
	Long: `Show the plan this account is on: which plan, where the subscription comes
from, its status, when it renews, and whether it is scheduled to cancel.

Billing lives in the Harbor web app (or the App Store / Google Play, if that is
where you subscribed) — the CLI never takes payment. When a subscription is
managed somewhere else, this command tells you where to go to change it.

Run 'harbor usage' for your usage against this plan's limits.`,
	Example: `  harbor plan
  harbor plan list
  harbor plan --json | jq -r '.data.plan.code'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.GetSubscription()
		if err != nil {
			return err
		}
		printResult(data, displaySubscription)
		return nil
	},
}

// planListCmd lists the plans that can be offered to this account.
var planListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the plans available to you, with their prices and limits",
	Long: `List the plans offerable to this account — the same catalog the web app's
pricing page shows — with monthly and yearly prices and each plan's per-resource
limits (∞ means unlimited).

Choosing and paying for a plan happens in the web app; this is a read-only view.`,
	Example: `  harbor plan list
  harbor plan list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListPlans(pagingParams(cmd))
		if err != nil {
			return err
		}
		printResult(data, displayPlans)
		if !jsonOutput {
			printPagingFooter(data)
		}
		return nil
	},
}

// ===========================================================================
// Displays
// ===========================================================================

// displayUsage renders the usage snapshot: a plan header, one row per capped
// resource, and — when something is full or the account is frozen — the
// specific thing that will fail next plus how to fix it.
func displayUsage(data []byte) {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		fmt.Println(string(data))
		return
	}

	fmt.Println(dim("Plan: ") + planLabel(nested(root, "plan")))

	usage := nested(root, "usage")
	rows := make([][]string, 0, len(usage))
	full := []string{}
	for _, name := range usageResourceNames(usage) {
		res, ok := usage[name].(map[string]any)
		if !ok {
			continue
		}
		used, limit, unlimited := planLimitPair(res)
		if unlimited {
			rows = append(rows, []string{name, trimFloat(used), "∞", "∞"})
			continue
		}
		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		left := trimFloat(remaining)
		if remaining == 0 {
			left = redWarn("0")
			full = append(full, name)
		}
		rows = append(rows, []string{name, trimFloat(used), trimFloat(limit), left})
	}
	printTable([]string{"RESOURCE", "USED", "LIMIT", "REMAINING"}, rows)

	if boolean(root, "is_read_only") {
		fmt.Println(redWarn("Your account is read-only.") + " You are over your plan's limits, so nothing can be created or edited.")
		fmt.Println("Delete what you no longer need (deleting and emptying the trash still work), or upgrade at " + bold(upgradeURL("")) + ".")
		return
	}
	if len(full) > 0 {
		fmt.Println(redWarn("At the limit: ") + strings.Join(full, ", ") + " — the next one you create will be refused.")
		fmt.Println("Free up room by deleting and then emptying the trash, or upgrade at " + bold(upgradeURL("")) + ".")
		return
	}
	fmt.Println(dim("Counts include trashed items; expunging them frees the slot. Run 'harbor plan' for your plan details."))
}

// displaySubscription renders the current entitlement as a detail view, then
// says where billing is actually changed — which is never the CLI.
func displaySubscription(data []byte) {
	root := parseJSON(client.UnwrapData(data))
	if root == nil {
		fmt.Println(string(data))
		return
	}

	pairs := [][2]string{
		{"Plan", planLabel(nested(root, "plan"))},
		{"Source", planSourceLabel(str(root, "source"))},
	}
	if status := str(root, "status"); status != "" {
		pairs = append(pairs, [2]string{"Status", colorizeStatus(status)})
	}
	if end := num(root, "current_period_end"); end > 0 {
		label := "Renews"
		if boolean(root, "cancel_at_period_end") {
			label = "Ends"
		}
		pairs = append(pairs, [2]string{label, epochMS(end) + dim(" ("+relTime(end)+")")})
	}
	if boolean(root, "cancel_at_period_end") {
		pairs = append(pairs, [2]string{"Cancels at period end", boolMark(true)})
	}
	if d := nested(root, "discount"); d != nil {
		pairs = append(pairs, [2]string{"Discount", discountLabel(d)})
	}
	if boolean(root, "is_read_only") {
		pairs = append(pairs, [2]string{"Read-only", redWarn("yes — you are over your plan's limits")})
	}
	pairs = append(pairs, [2]string{"Billing managed by", managedByLabel(str(root, "managed_by"), str(root, "manage_url"))})
	printKV(pairs)

	switch {
	case str(root, "manage_url") != "":
		fmt.Println("Manage this subscription at " + bold(str(root, "manage_url")) + ".")
	case str(root, "managed_by") == "comp":
		fmt.Println("Harbor granted this plan — contact support ('harbor support') to change it.")
	default:
		fmt.Println("Change plans at " + bold(upgradeURL("")) + " — the CLI does not take payment.")
	}
	fmt.Println(dim("Run 'harbor usage' for usage against this plan, or 'harbor plan list' to see what else is offered."))
}

// displayPlans renders the offerable plan catalog with prices and limits.
func displayPlans(data []byte) {
	items := client.CollectionItems(data)
	rows := make([][]string, 0, len(items))
	for _, raw := range items {
		p := parseJSON(raw)
		if p == nil {
			continue
		}
		currency := str(p, "currency")
		limits := nested(p, "limits")
		rows = append(rows, []string{
			str(p, "code"),
			str(p, "name"),
			money(p, "price_month_cents", currency),
			money(p, "price_year_cents", currency),
			planCap(limits, "max_notes"),
			planCap(limits, "max_notebooks"),
			planCap(limits, "max_tags"),
			planCap(limits, "max_files"),
			planCap(limits, "max_tasks"),
		})
	}
	printTable([]string{"CODE", "NAME", "MONTHLY", "YEARLY", "NOTES", "NOTEBOOKS", "TAGS", "FILES", "TASKS"}, rows)
	if len(rows) > 0 {
		fmt.Println(dim("∞ = unlimited. Subscribe or change plans at ") + bold(upgradeURL("")) + dim(" — the CLI does not take payment."))
	}
}

// ===========================================================================
// The plan-limit error
// ===========================================================================

// isPlanLimitError reports whether err is (or wraps) the API's entitlement-gate
// error. Commands never need to branch on this themselves: it drives both the
// rendering below and the dedicated exit code, centrally, so EVERY create path
// — notes, notebooks, tags, tasks, uploads, imports — behaves the same without
// each domain remembering to opt in.
func isPlanLimitError(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Code == planLimitCode
}

// renderPlanLimitError prints the human form of a plan-limit rejection to
// stderr. The person reading it is usually mid-script and has no idea why the
// command stopped, so it answers exactly three questions: which limit was hit,
// what that means, and what to do next. The API's own message is the headline
// (it is the wording every Harbor client shares); the CLI adds the numbers, an
// absolute upgrade link — details.upgrade_url is a web-app PATH, useless in a
// terminal on its own — and the local escape hatches.
func renderPlanLimitError(apiErr *client.APIError) {
	fmt.Fprintln(os.Stderr, redWarn("Error: ")+planLimitHeadline(apiErr))
	fmt.Fprintln(os.Stderr, dim("  code: "+apiErr.Code))
	for _, line := range planLimitLines(apiErr) {
		fmt.Fprintln(os.Stderr, "  "+line)
	}
}

// planLimitHeadline is the API's message, falling back to CLI wording when the
// server sent none — an error whose whole job is explaining itself must never
// come out blank.
func planLimitHeadline(apiErr *client.APIError) string {
	if msg := strings.TrimSpace(apiErr.Message); msg != "" {
		return msg
	}
	if planLimitIsReadOnly(apiErr) {
		return "Your account is read-only because you are over your plan's limits."
	}
	return "You have reached your plan's limit."
}

// planLimitLines builds the explanation body: the numbers behind the headline,
// then what to do about it. Kept separate from printing so the wording is
// testable without capturing stderr.
func planLimitLines(apiErr *client.APIError) []string {
	d := apiErr.Details
	lines := []string{}

	if planLimitIsReadOnly(apiErr) {
		lines = append(lines, "Your whole account is frozen — creates and edits are blocked until you are back under your plan's limits.")
		lines = append(lines, "Deleting still works, so you can free up room: delete what you no longer need, then 'harbor trash empty'.")
	} else if resource := str(d, "resource"); resource != "" {
		plural := resourcePlural(resource)
		if used, limit := str(d, "used"), str(d, "limit"); used != "" && limit != "" {
			lines = append(lines, fmt.Sprintf("You are using %s of %s %s%s.", used, limit, plural, planCodeSuffix(d)))
		} else {
			lines = append(lines, fmt.Sprintf("You have reached your %s limit%s.", plural, planCodeSuffix(d)))
		}
		lines = append(lines, fmt.Sprintf("Only %s are blocked — everything else still works. Trashed %s still count; expunge them ('harbor trash empty') to free a slot.", plural, plural))
	}

	lines = append(lines, "Upgrade at "+bold(upgradeURL(str(d, "upgrade_url")))+" — plans are changed in the Harbor web app, not the CLI.")
	lines = append(lines, dim("Run 'harbor usage' to see every limit on this plan."))
	return lines
}

// planLimitIsReadOnly reports whether this rejection is the whole-account
// read-only freeze rather than one resource hitting its cap. The API marks it
// with gate/reason = account_read_only; a rejection carrying no resource at all
// is treated the same way, since per-resource copy would be a lie.
func planLimitIsReadOnly(apiErr *client.APIError) bool {
	d := apiErr.Details
	if str(d, "gate") == planLimitReadOnlyGate || str(d, "reason") == planLimitReadOnlyGate {
		return true
	}
	return str(d, "resource") == ""
}

// planCodeSuffix renders " on the <code> plan" when the API named the plan, and
// nothing when it did not.
func planCodeSuffix(d map[string]any) string {
	if code := str(d, "plan_code"); code != "" {
		return " on the " + code + " plan"
	}
	return ""
}

// ===========================================================================
// Helpers
// ===========================================================================

// upgradeURL turns the API's upgrade path into something a person can actually
// open. The API sends a web-app path (e.g. /settings/plan) because it does not
// know which client is asking; a terminal needs the whole URL, resolved against
// the SAME environment this command talked to — pointing a self-hosted or
// staging user at production would send them to the wrong account entirely.
func upgradeURL(raw string) string {
	path := strings.TrimSpace(raw)
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if path == "" {
		path = defaultUpgradePath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return webOrigin() + path
}

// webOrigin is the web app's origin for whichever API this command is talking
// to: the API base URL minus its /api/v1 suffix.
func webOrigin() string {
	creds, _ := config.Load() // best effort: only for the base URL
	base := strings.TrimRight(resolveBaseURL(creds), "/")
	return strings.TrimSuffix(base, "/api/v1")
}

// resourcePlural renders a plan-limit error's singular resource name in the
// plural, matching the GET /usage keys. An unknown resource (a cap added
// server-side after this build) is pluralized naively rather than dropped.
func resourcePlural(resource string) string {
	if plural, ok := planResourcePlural[resource]; ok {
		return plural
	}
	if strings.HasSuffix(resource, "s") {
		return resource
	}
	return resource + "s"
}

// usageResourceNames orders the usage map: the known resources first, in the
// order they are documented, then anything new the API added, alphabetically.
func usageResourceNames(usage map[string]any) []string {
	names := make([]string, 0, len(usage))
	seen := map[string]bool{}
	for _, name := range planResourceOrder {
		if _, ok := usage[name]; ok {
			names = append(names, name)
			seen[name] = true
		}
	}
	extra := make([]string, 0)
	for name := range usage {
		if !seen[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(names, extra...)
}

// planLimitPair reads a { used, limit } usage entry. A null limit means
// unlimited, which is NOT the same as a limit of zero — reading a JSON null as
// 0 would report an unlimited plan as completely full.
func planLimitPair(res map[string]any) (used, limit float64, unlimited bool) {
	used = num(res, "used")
	val, ok := res["limit"]
	if !ok || val == nil {
		return used, 0, true
	}
	return used, num(res, "limit"), false
}

// planCap renders one of a plan's per-resource caps, showing ∞ for a null
// (unlimited) cap.
func planCap(limits map[string]any, key string) string {
	if limits == nil {
		return "—"
	}
	val, ok := limits[key]
	if !ok || val == nil {
		return "∞"
	}
	return str(limits, key)
}

// planLabel renders a plan object as "Name (code)", degrading gracefully when
// the API resolved no plan at all.
func planLabel(plan map[string]any) string {
	name, code := str(plan, "name"), str(plan, "code")
	switch {
	case name != "" && code != "":
		return bold(name) + dim(" ("+code+")")
	case name != "":
		return bold(name)
	case code != "":
		return bold(code)
	default:
		return dim("(none)")
	}
}

// planSourceLabel spells out where an entitlement comes from, since the raw
// values ("comp", "free") are jargon outside the API.
func planSourceLabel(source string) string {
	switch source {
	case "free":
		return "free plan"
	case "stripe":
		return "paid (card, via the web app)"
	case "apple":
		return "paid (Apple App Store)"
	case "google":
		return "paid (Google Play)"
	case "comp":
		return "complimentary (granted by Harbor)"
	case "":
		return "—"
	default:
		return source
	}
}

// managedByLabel says which surface owns billing, and — for a store
// subscription the CLI cannot touch — where to go instead.
func managedByLabel(managedBy, manageURL string) string {
	switch managedBy {
	case "":
		return "— " + dim("(nothing to manage on a free plan)")
	case "stripe":
		return "the Harbor web app"
	case "apple":
		return "the App Store" + manageURLSuffix(manageURL)
	case "google":
		return "Google Play" + manageURLSuffix(manageURL)
	case "comp":
		return "Harbor " + dim("(complimentary — contact support to change it)")
	default:
		return managedBy + manageURLSuffix(manageURL)
	}
}

// manageURLSuffix appends the store's management link when the API sent one.
func manageURLSuffix(manageURL string) string {
	if manageURL == "" {
		return ""
	}
	return " — " + manageURL
}

// discountLabel summarizes an applied promo for the plan detail view.
func discountLabel(d map[string]any) string {
	parts := []string{}
	if name := str(d, "name"); name != "" {
		parts = append(parts, name)
	}
	if pct := num(d, "percent_off"); pct > 0 {
		parts = append(parts, trimFloat(pct)+"% off")
	} else if amt := num(d, "amount_off_cents"); amt > 0 {
		parts = append(parts, centsToPrice(amt, str(d, "currency"))+" off")
	}
	if len(parts) == 0 {
		return "yes"
	}
	return strings.Join(parts, " · ")
}

// money renders a price field in cents, showing a zero price as "Free".
func money(p map[string]any, key, currency string) string {
	cents := num(p, key)
	if cents == 0 {
		return "Free"
	}
	return centsToPrice(cents, currency)
}

// centsToPrice formats integer cents as a price. USD gets the familiar $ form;
// anything else is rendered with its currency code rather than a guessed
// symbol.
func centsToPrice(cents float64, currency string) string {
	amount := fmt.Sprintf("%.2f", cents/100)
	if currency == "" || strings.EqualFold(currency, "usd") {
		return "$" + amount
	}
	return amount + " " + strings.ToUpper(currency)
}

func init() {
	addPagingFlags(planListCmd)
	planCmd.AddCommand(planListCmd)
	rootCmd.AddCommand(planCmd, usageCmd)
}
