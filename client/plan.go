// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-08-01

package client

// GetUsage fetches the caller's usage snapshot (data-wrapped): the effective
// plan, the whole-account read-only flag, and a { used, limit } pair for every
// plan-capped resource (notes, notebooks, tags, files, tasks). A limit of null
// means unlimited. Counts include live + trashed rows and exclude expunged
// ones, and they come from the same counting engine as the create-time
// plan_limit_reached guard, so the meter and the guard can never disagree.
func (c *Client) GetUsage() ([]byte, error) {
	return c.doGet("/usage", nil)
}

// GetSubscription fetches the caller's current entitlement snapshot
// (data-wrapped): the effective plan, where it comes from (free/stripe/apple/
// google/comp), its status and period end, and — importantly for a client that
// cannot take payment — managed_by/manage_url, which say who owns billing and
// where to send the user to change it.
func (c *Client) GetSubscription() ([]byte, error) {
	return c.doGet("/subscription", nil)
}

// ListPlans fetches the plans offerable to the caller (a standard collection
// envelope): the catalog behind a pricing grid, active + public only, ordered
// free first then cheapest monthly. Each plan carries its per-resource limits,
// where a null cap means unlimited.
func (c *Client) ListPlans(params map[string]string) ([]byte, error) {
	return c.doGet("/plans", params)
}
