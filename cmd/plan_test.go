// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package cmd

import (
	"strings"
	"testing"

	"github.com/HarborMyNotes/harbor-cli/client"
)

// usageFixture is a GET /usage response with a finite plan: notebooks are full,
// everything else has room.
const usageFixture = `{"data":{
  "plan":{"code":"starter","name":"Starter","source":"free","status":""},
  "is_read_only":false,
  "usage":{
    "notes":{"used":42,"limit":50},
    "notebooks":{"used":3,"limit":3},
    "tags":{"used":5,"limit":20},
    "files":{"used":1,"limit":50},
    "tasks":{"used":0,"limit":100}
  }}}`

// unlimitedUsageFixture is the same snapshot for a plan with no caps at all.
// Every limit is null, and notes is deliberately at zero used — a null limit
// read as the number 0 would make this account look completely full.
const unlimitedUsageFixture = `{"data":{
  "plan":{"code":"unlimited","name":"Unlimited","source":"comp","status":""},
  "is_read_only":false,
  "usage":{
    "notes":{"used":0,"limit":null},
    "notebooks":{"used":9,"limit":null}
  }}}`

// planLimitDetails is the details block the API sends when a create is refused
// at a per-resource cap. Every value is a string — the error envelope's detail
// values always are, so numbers arrive stringified.
func planLimitDetails() map[string]any {
	return map[string]any{
		"resource":           "notebook",
		"used":               "3",
		"limit":              "3",
		"plan_code":          "starter",
		"upgrade_url":        "/settings/plan",
		"gate":               "plan_limit",
		"current":            "3",
		"remediation_action": "upgrade",
		"remediation_label":  "Upgrade",
		"remediation_url":    "/settings/plan",
	}
}

// planLimitError builds the typed error a blocked create returns.
func planLimitError(message string, details map[string]any) *client.APIError {
	return &client.APIError{Code: planLimitCode, Message: message, Details: details, Status: 403}
}

// ===========================================================================
// harbor usage
// ===========================================================================

// TestUsageCommandCallsUsageEndpoint pins the wiring: the command must read the
// live snapshot from GET /usage rather than infer anything locally.
func TestUsageCommandCallsUsageEndpoint(t *testing.T) {
	m := newAPIMock(t, map[string]mockReply{"GET /api/v1/usage": {200, usageFixture}})
	out, err := runCLI(t, m, "usage")
	if err != nil {
		t.Fatalf("usage failed: %v", err)
	}
	if got := m.calls(); len(got) != 1 || got[0] != "GET /api/v1/usage" {
		t.Errorf("calls = %v, want one GET /api/v1/usage", got)
	}
	if !strings.Contains(out, "notebooks") || !strings.Contains(out, "Starter") {
		t.Errorf("usage output missing the plan or the rows:\n%s", out)
	}
}

// TestDisplayUsageFlagsTheResourceThatIsFull checks the whole point of the
// meter: naming which resource will refuse the next create, and how to fix it.
func TestDisplayUsageFlagsTheResourceThatIsFull(t *testing.T) {
	t.Setenv("HARBOR_API_URL", "https://harbor.example/api/v1")
	out := captureStdout(t, func() { displayUsage([]byte(usageFixture)) })

	if !strings.Contains(out, "At the limit: notebooks") {
		t.Errorf("output does not name the full resource:\n%s", out)
	}
	if !strings.Contains(out, "https://harbor.example/settings/plan") {
		t.Errorf("output has no absolute upgrade link:\n%s", out)
	}
	// notes has 8 left; a full resource must not swallow the healthy ones.
	if !strings.Contains(out, "42") || !strings.Contains(out, "8") {
		t.Errorf("output missing the used/remaining numbers:\n%s", out)
	}
}

// TestDisplayUsageShowsUnlimitedAsInfinity covers the null-limit contract. A
// JSON null decodes to a zero float, so reading it as a number would report an
// unlimited plan as full and tell the user to upgrade off the top plan.
func TestDisplayUsageShowsUnlimitedAsInfinity(t *testing.T) {
	out := captureStdout(t, func() { displayUsage([]byte(unlimitedUsageFixture)) })

	if !strings.Contains(out, "∞") {
		t.Errorf("unlimited plan not rendered as ∞:\n%s", out)
	}
	if strings.Contains(out, "At the limit") {
		t.Errorf("unlimited plan reported as full:\n%s", out)
	}
}

// TestDisplayUsageWarnsWhenAccountIsReadOnly checks the frozen-account case
// wins over the per-resource one: when everything is blocked, saying only
// "notebooks are full" would send the user off to delete the wrong things.
func TestDisplayUsageWarnsWhenAccountIsReadOnly(t *testing.T) {
	frozen := strings.Replace(usageFixture, `"is_read_only":false`, `"is_read_only":true`, 1)
	out := captureStdout(t, func() { displayUsage([]byte(frozen)) })

	if !strings.Contains(out, "read-only") {
		t.Errorf("read-only account not called out:\n%s", out)
	}
	if strings.Contains(out, "At the limit") {
		t.Errorf("read-only account reported as a single full resource:\n%s", out)
	}
}

// TestUsageResourceNamesKeepsDocumentedOrderAndAddsNewOnes pins the row order
// and, just as importantly, that a resource the API adds later still appears.
func TestUsageResourceNamesKeepsDocumentedOrderAndAddsNewOnes(t *testing.T) {
	usage := map[string]any{
		"tasks": map[string]any{}, "notes": map[string]any{},
		"widgets": map[string]any{}, "notebooks": map[string]any{},
	}
	got := usageResourceNames(usage)
	want := []string{"notes", "notebooks", "tasks", "widgets"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

// TestPlanLimitPairSeparatesNullFromZero is the unit-level guard behind the
// ∞ rendering: an absent or null cap is "unlimited", a real 0 is not.
func TestPlanLimitPairSeparatesNullFromZero(t *testing.T) {
	used, limit, unlimited := planLimitPair(map[string]any{"used": float64(7), "limit": nil})
	if !unlimited || used != 7 {
		t.Errorf("null limit: used=%v limit=%v unlimited=%v, want 7/unlimited", used, limit, unlimited)
	}
	if _, _, unlimited := planLimitPair(map[string]any{"used": float64(0), "limit": float64(0)}); unlimited {
		t.Error("a zero limit was treated as unlimited")
	}
	if _, _, unlimited := planLimitPair(map[string]any{"used": float64(1)}); !unlimited {
		t.Error("a missing limit key was treated as capped")
	}
}

// ===========================================================================
// harbor plan / harbor plan list
// ===========================================================================

// TestPlanCommandCallsSubscriptionEndpoint pins the wiring for the plan view.
func TestPlanCommandCallsSubscriptionEndpoint(t *testing.T) {
	body := `{"data":{"plan":{"code":"starter","name":"Starter","is_free":true},"source":"free",
	  "status":"","current_period_end":null,"cancel_at_period_end":false,"is_read_only":false,
	  "managed_by":"","manage_url":"","can_cancel":false,"discount":null}}`
	m := newAPIMock(t, map[string]mockReply{"GET /api/v1/subscription": {200, body}})
	out, err := runCLI(t, m, "plan")
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if got := m.calls(); len(got) != 1 || got[0] != "GET /api/v1/subscription" {
		t.Errorf("calls = %v, want one GET /api/v1/subscription", got)
	}
	if !strings.Contains(out, "Starter") || !strings.Contains(out, "free plan") {
		t.Errorf("plan output missing the plan or its source:\n%s", out)
	}
}

// TestPlanListCommandCallsPlansEndpoint pins the catalog wiring and that paging
// flags are forwarded rather than silently dropped.
func TestPlanListCommandCallsPlansEndpoint(t *testing.T) {
	body := `{"data":[{"code":"unlimited","name":"Unlimited","is_free":false,
	  "price_month_cents":999,"price_year_cents":9900,"currency":"usd",
	  "limits":{"max_notes":null,"max_notebooks":null,"max_tags":null,"max_files":null,"max_tasks":null}}],
	  "paging":{"limit":1,"offset":0,"total":1,"has_more":false}}`
	m := newAPIMock(t, map[string]mockReply{"GET /api/v1/plans": {200, body}})
	out, err := runCLI(t, m, "plan", "list", "--limit", "1")
	if err != nil {
		t.Fatalf("plan list failed: %v", err)
	}
	if got := m.queryOf(t, "GET /api/v1/plans").Get("limit"); got != "1" {
		t.Errorf("limit query = %q, want 1", got)
	}
	if !strings.Contains(out, "$9.99") || !strings.Contains(out, "$99.00") {
		t.Errorf("prices not rendered from cents:\n%s", out)
	}
	// All five caps are null on this plan. Counting them (rather than looking
	// for one ∞) keeps the assertion off the legend in the footer, which says
	// "∞ = unlimited" whether or not a single cell rendered.
	if got := strings.Count(out, "∞"); got < 5 {
		t.Errorf("null caps not rendered as ∞ (%d of 5 cells):\n%s", got, out)
	}
}

// TestDisplaySubscriptionShowsRenewalAndCancellation checks a paid subscription
// reads correctly: a scheduled cancel must say the plan ENDS, not renews.
func TestDisplaySubscriptionShowsRenewalAndCancellation(t *testing.T) {
	body := `{"data":{"plan":{"code":"unlimited","name":"Unlimited","is_free":false},"source":"stripe",
	  "status":"active","current_period_end":1798761600000,"cancel_at_period_end":true,
	  "is_read_only":false,"managed_by":"stripe","manage_url":"","can_cancel":true,"discount":null}}`
	out := captureStdout(t, func() { displaySubscription([]byte(body)) })

	if !strings.Contains(out, "Ends") {
		t.Errorf("a scheduled cancel was rendered as a renewal:\n%s", out)
	}
	if !strings.Contains(out, "Cancels at period end") {
		t.Errorf("cancellation not called out:\n%s", out)
	}
	if !strings.Contains(out, "the Harbor web app") {
		t.Errorf("billing owner not named:\n%s", out)
	}
}

// TestDisplaySubscriptionRoutesStoreBillingElsewhere covers the case the CLI
// can do least about: a store subscription it must not offer to change, only
// point at.
func TestDisplaySubscriptionRoutesStoreBillingElsewhere(t *testing.T) {
	body := `{"data":{"plan":{"code":"unlimited","name":"Unlimited","is_free":false},"source":"apple",
	  "status":"active","current_period_end":1798761600000,"cancel_at_period_end":false,
	  "is_read_only":false,"managed_by":"apple",
	  "manage_url":"https://apps.apple.com/account/subscriptions","can_cancel":false,"discount":null}}`
	out := captureStdout(t, func() { displaySubscription([]byte(body)) })

	// The owner row must name the store AND carry its link — "paid (Apple App
	// Store)" on the source row is a different statement, so the assertion has
	// to be specific enough not to be satisfied by it.
	if !strings.Contains(out, "the App Store — https://apps.apple.com/account/subscriptions") {
		t.Errorf("store-managed billing not named with its link:\n%s", out)
	}
	if !strings.Contains(out, "Manage this subscription at https://apps.apple.com/account/subscriptions") {
		t.Errorf("no instruction to manage the subscription in the store:\n%s", out)
	}
}

// TestMoneyRendersCentsAndFreeAndForeignCurrency pins price formatting,
// including that a non-USD price is not given a dollar sign.
func TestMoneyRendersCentsAndFreeAndForeignCurrency(t *testing.T) {
	p := map[string]any{"free": float64(0), "usd": float64(999), "eur": float64(1250)}
	if got := money(p, "free", "usd"); got != "Free" {
		t.Errorf("zero price = %q, want Free", got)
	}
	if got := money(p, "usd", "usd"); got != "$9.99" {
		t.Errorf("usd price = %q, want $9.99", got)
	}
	if got := money(p, "eur", "eur"); got != "12.50 EUR" {
		t.Errorf("eur price = %q, want 12.50 EUR", got)
	}
}

// ===========================================================================
// The plan-limit error
// ===========================================================================

// TestPlanLimitErrorExplainsWhichCapAndWhatToDo is the heart of the issue: the
// person reading this is mid-script and does not know why it stopped, so the
// message has to carry the numbers, an openable link, and a next step.
func TestPlanLimitErrorExplainsWhichCapAndWhatToDo(t *testing.T) {
	t.Setenv("HARBOR_API_URL", "https://harbor.example/api/v1")
	body := strings.Join(planLimitLines(planLimitError("You've reached your plan's limit of 3 notebooks.", planLimitDetails())), "\n")

	if !strings.Contains(body, "3 of 3 notebooks") {
		t.Errorf("used/limit not spelled out:\n%s", body)
	}
	if !strings.Contains(body, "starter plan") {
		t.Errorf("plan not named:\n%s", body)
	}
	if !strings.Contains(body, "https://harbor.example/settings/plan") {
		t.Errorf("upgrade path not resolved to an openable URL:\n%s", body)
	}
	if !strings.Contains(body, "harbor usage") {
		t.Errorf("no pointer to the usage command:\n%s", body)
	}
	// Hitting one cap blocks only that resource; saying otherwise would send
	// the user hunting for a problem they do not have.
	if !strings.Contains(body, "Only notebooks are blocked") {
		t.Errorf("per-resource scope not explained:\n%s", body)
	}
	// Only notes have a recycle bin, so pointing a notebook cap at the trash
	// would send the user somewhere that cannot free a single slot.
	if strings.Contains(body, "trash") {
		t.Errorf("notebook cap pointed the user at the trash:\n%s", body)
	}
}

// TestPlanLimitOnNotesMentionsTheTrash is the counterpart: notes DO have a
// recycle bin, and a trashed note keeps holding its slot — the one case where
// "I already deleted things" does not explain why the cap is still full.
func TestPlanLimitOnNotesMentionsTheTrash(t *testing.T) {
	details := map[string]any{"resource": "note", "used": "50", "limit": "50", "plan_code": "starter"}
	body := strings.Join(planLimitLines(planLimitError("You've reached your plan's limit of 50 notes.", details)), "\n")

	if !strings.Contains(body, "50 of 50 notes") {
		t.Errorf("note cap numbers missing:\n%s", body)
	}
	if !strings.Contains(body, "harbor trash empty") {
		t.Errorf("note cap did not mention the trash:\n%s", body)
	}
}

// TestPlanLimitErrorExplainsTheReadOnlyFreeze covers the other gate behind the
// same code: the whole account is frozen, so per-resource copy would be wrong.
func TestPlanLimitErrorExplainsTheReadOnlyFreeze(t *testing.T) {
	details := map[string]any{
		"gate": planLimitReadOnlyGate, "reason": planLimitReadOnlyGate,
		"plan_code": "starter", "upgrade_url": "/settings/plan",
	}
	body := strings.Join(planLimitLines(planLimitError("Your account is read-only.", details)), "\n")

	if !strings.Contains(body, "frozen") {
		t.Errorf("freeze not explained:\n%s", body)
	}
	if !strings.Contains(body, "Deleting still works") {
		t.Errorf("escape hatch not offered:\n%s", body)
	}
	if strings.Contains(body, "Only ") {
		t.Errorf("a whole-account freeze was described as one resource:\n%s", body)
	}
}

// TestPlanLimitWithoutResourceReadsAsWholeAccount guards the shape the API
// documents for the read-only gate but a future gate might send bare: with no
// resource there is nothing per-resource to say, so it must not claim there is.
func TestPlanLimitWithoutResourceReadsAsWholeAccount(t *testing.T) {
	if !planLimitIsReadOnly(planLimitError("blocked", map[string]any{"plan_code": "starter"})) {
		t.Error("a rejection with no resource was treated as a per-resource cap")
	}
	if planLimitIsReadOnly(planLimitError("blocked", planLimitDetails())) {
		t.Error("a per-resource cap was treated as a whole-account freeze")
	}
}

// TestPlanLimitHeadlineFallsBackWhenServerSaysNothing keeps the one error whose
// job is to explain itself from coming out blank.
func TestPlanLimitHeadlineFallsBackWhenServerSaysNothing(t *testing.T) {
	if got := planLimitHeadline(planLimitError("", planLimitDetails())); !strings.Contains(got, "limit") {
		t.Errorf("headline = %q, want a fallback mentioning the limit", got)
	}
	if got := planLimitHeadline(planLimitError("  ", map[string]any{"gate": planLimitReadOnlyGate})); !strings.Contains(got, "read-only") {
		t.Errorf("read-only headline = %q, want the freeze wording", got)
	}
	if got := planLimitHeadline(planLimitError("Server wording wins.", planLimitDetails())); got != "Server wording wins." {
		t.Errorf("headline = %q, want the server's message verbatim", got)
	}
}

// TestRenderPlanLimitErrorWritesToStderrOnly is the piping guarantee: a
// diagnostic on stdout would land in whatever the command was piped into.
func TestRenderPlanLimitErrorWritesToStderrOnly(t *testing.T) {
	var errOut string
	out := captureStdout(t, func() {
		errOut = captureStderr(t, func() {
			renderPlanLimitError(planLimitError("You've reached your plan's limit of 3 notebooks.", planLimitDetails()))
		})
	})
	if out != "" {
		t.Errorf("plan-limit error leaked onto stdout: %q", out)
	}
	if !strings.Contains(errOut, "3 of 3 notebooks") || !strings.Contains(errOut, planLimitCode) {
		t.Errorf("stderr missing the explanation or the code:\n%s", errOut)
	}
}

// TestIsPlanLimitErrorMatchesOnlyTheGate keeps the dedicated exit code from
// firing for unrelated failures.
func TestIsPlanLimitErrorMatchesOnlyTheGate(t *testing.T) {
	if !isPlanLimitError(planLimitError("blocked", nil)) {
		t.Error("the plan-limit error was not recognized")
	}
	if isPlanLimitError(apiErr("validation_failed")) {
		t.Error("an unrelated API error was read as a plan limit")
	}
	if isPlanLimitError(nil) {
		t.Error("nil was read as a plan limit")
	}
}

// ===========================================================================
// Helpers
// ===========================================================================

// TestUpgradeURLResolvesAgainstTheEnvironmentInUse covers the reason the CLI
// cannot just print details.upgrade_url: it is a path, and it belongs to
// whichever Harbor this command is talking to.
func TestUpgradeURLResolvesAgainstTheEnvironmentInUse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HARBOR_API_URL", "https://staging.harbor.example/api/v1")

	if got := upgradeURL("/settings/plan"); got != "https://staging.harbor.example/settings/plan" {
		t.Errorf("relative path = %q", got)
	}
	if got := upgradeURL("settings/plan"); got != "https://staging.harbor.example/settings/plan" {
		t.Errorf("path without a leading slash = %q", got)
	}
	if got := upgradeURL(""); got != "https://staging.harbor.example"+defaultUpgradePath {
		t.Errorf("empty path = %q", got)
	}
	if got := upgradeURL("https://billing.example/upgrade"); got != "https://billing.example/upgrade" {
		t.Errorf("absolute URL was rewritten: %q", got)
	}
}

// TestResourcePluralCoversUnknownResources makes sure a cap the API adds after
// this build still reads as English rather than being dropped.
func TestResourcePluralCoversUnknownResources(t *testing.T) {
	cases := map[string]string{
		"note": "notes", "notebook": "notebooks", "tag": "tags",
		"file": "files", "task": "tasks", "widget": "widgets", "notes": "notes",
	}
	for in, want := range cases {
		if got := resourcePlural(in); got != want {
			t.Errorf("resourcePlural(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPlanLabelDegradesWhenNoPlanResolved covers the documented case where the
// API resolved no plan at all — an empty row would read as a rendering bug.
func TestPlanLabelDegradesWhenNoPlanResolved(t *testing.T) {
	noColorFlag = true
	colorReady = false
	defer func() { noColorFlag = false; colorReady = false }()

	if got := planLabel(nil); got != "(none)" {
		t.Errorf("planLabel(nil) = %q", got)
	}
	if got := planLabel(map[string]any{"code": "starter"}); got != "starter" {
		t.Errorf("code-only label = %q", got)
	}
	if got := planLabel(map[string]any{"name": "Starter", "code": "starter"}); got != "Starter (starter)" {
		t.Errorf("full label = %q", got)
	}
}
