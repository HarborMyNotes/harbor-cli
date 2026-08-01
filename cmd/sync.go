// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-06-22

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/HarborMyNotes/harbor-cli/client"
	"github.com/HarborMyNotes/harbor-cli/config"
	"github.com/spf13/cobra"
)

// syncCmd is the parent for the raw USN sync engine — a power-user / agent
// surface best driven with --json.
var syncCmd = &cobra.Command{
	Use:     "sync",
	Short:   "Raw USN sync engine (pull, push, devices, ack)",
	GroupID: groupSync,
	Long: `Direct access to Harbor's Evernote-style USN sync engine: pull server
changes since a cursor, push local change envelopes, manage devices, and ack
progress. This is a JSON-first surface — pair it with --json for scripting.`,
}

// syncPullCmd pulls changes since a cursor, optionally paging to the end.
var syncPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull server changes since a USN cursor",
	Long:  "Pull all syncable records with usn greater than --after-usn. With --all, page through every chunk until caught up and merge the result.",
	Example: `  harbor sync pull --after-usn 0 --all --json
  harbor sync pull --after-usn 80 --limit 200`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		scopeID, err := resolveScopeID(c, creds, cmd)
		if err != nil {
			return err
		}
		deviceID := deviceIDOrDefault(cmd, creds)
		limit := 0
		if cmd.Flags().Changed("limit") {
			limit = intFlag(cmd, "limit")
		}
		out, err := runSyncPull(c, scopeID, deviceID, int64(intFlag(cmd, "after-usn")), limit, boolFlag(cmd, "all"))
		if err != nil {
			return mapSyncError(err)
		}
		printResult(out, displaySyncPull)
		return nil
	},
}

// syncPushCmd uploads a batch of change envelopes from a file or stdin.
var syncPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push local change envelopes from a JSON file or stdin",
	Long: `Upload a batch of change envelopes. --file is a JSON array of envelopes (or an object with a "changes" array). Use --file - to read stdin.

The server answers 200 and reports each change's outcome individually, so the
results are printed either way and the exit code says whether they all landed:
0 when none were refused, 4 when a refusal was a plan limit, and 1 for any other
refusal. A conflict is not a refusal — the change is not applied, but the
server_record you need to resolve it comes back in the results, so that stays 0.`,
	Example: `  harbor sync push --file changes.json
  cat changes.json | harbor sync push --file -`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		scopeID, err := resolveScopeID(c, creds, cmd)
		if err != nil {
			return err
		}
		deviceID := deviceIDOrDefault(cmd, creds)
		if deviceID == "" {
			return errors.New("a device id is required to push (run 'harbor sync register-device' or pass --device-id)")
		}
		changes, err := readChanges(stringFlag(cmd, "file"))
		if err != nil {
			return err
		}
		data, err := c.SyncPush(map[string]any{"scope_id": scopeID, "device_id": deviceID, "changes": changes})
		if err != nil {
			return mapSyncError(err)
		}
		// Print the results first, then fail on any refusal: the per-change
		// results are how a client reconciles (which USNs landed, which changes
		// to re-resolve), so they are output even on the failing path — exactly
		// as 'harbor status' prints its readiness table before exiting non-zero.
		printResult(data, displaySyncPush)
		return syncPushRejection(data)
	},
}

// syncDevicesCmd lists devices, the scope max USN, and the GC floor.
var syncDevicesCmd = &cobra.Command{
	Use:   "devices",
	Short: "List devices with sync status and the GC floor",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		data, err := c.ListDevices()
		if err != nil {
			return err
		}
		printResult(data, displaySyncDevices)
		return nil
	},
}

// syncRegisterDeviceCmd registers (upserts) a device.
var syncRegisterDeviceCmd = &cobra.Command{
	Use:   "register-device",
	Short: "Register (or refresh) a device",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		deviceID := deviceIDOrDefault(cmd, creds)
		if deviceID == "" {
			return errors.New("--device-id is required")
		}
		name := stringFlag(cmd, "name")
		if name == "" {
			name = creds.DeviceName
		}
		platform := stringFlag(cmd, "platform")
		if platform == "" {
			platform = "cli"
		}
		data, err := c.RegisterDevice(map[string]any{"device_id": deviceID, "name": name, "platform": platform})
		if err != nil {
			return err
		}
		printResult(data, displayDevice)
		return nil
	},
}

// syncRemoveDeviceCmd deregisters a device.
var syncRemoveDeviceCmd = &cobra.Command{
	Use:   "remove-device <device-id>",
	Short: "Remove (deregister) a device",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c, _, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		if _, err := c.RemoveDevice(args[0]); err != nil {
			return err
		}
		fmt.Println("Device removed.")
		return nil
	},
}

// syncAckCmd advances a device's acked cursor.
var syncAckCmd = &cobra.Command{
	Use:     "ack",
	Short:   "Advance a device's acked USN cursor",
	Example: "  harbor sync ack --device-id cli-9f3a --acked-usn 95",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, creds, err := loadClientFromConfig()
		if err != nil {
			return err
		}
		deviceID := deviceIDOrDefault(cmd, creds)
		if deviceID == "" {
			return errors.New("--device-id is required")
		}
		if !cmd.Flags().Changed("acked-usn") {
			return errors.New("--acked-usn is required")
		}
		data, err := c.SyncAck(deviceID, int64(intFlag(cmd, "acked-usn")))
		if err != nil {
			return mapSyncError(err)
		}
		printResult(data, displayDevice)
		return nil
	},
}

// ===========================================================================
// Helpers
// ===========================================================================

// runSyncPull pulls one chunk, or (when all is true) pages through every chunk
// until caught up, merging them into one synthesized pull-response document.
// Extracted from the command so the paging loop is unit-testable.
func runSyncPull(c *client.Client, scopeID, deviceID string, afterUSN int64, limit int, all bool) ([]byte, error) {
	merged := []json.RawMessage{}
	var scopeMax float64
	hasMore := false
	for {
		body := map[string]any{"scope_id": scopeID, "after_usn": afterUSN}
		if limit > 0 {
			body["limit"] = limit
		}
		if deviceID != "" {
			body["device_id"] = deviceID
		}
		data, err := c.SyncPull(body)
		if err != nil {
			return nil, err
		}
		root := parseJSON(data)
		scopeMax = num(root, "scope_max_usn")
		hasMore = boolean(root, "has_more")
		chunk := chunkRaw(data)
		merged = append(merged, chunk...)
		if !all || !hasMore || len(chunk) == 0 {
			break
		}
		afterUSN = maxChunkUSN(chunk, afterUSN)
	}
	return mergedPull(scopeID, scopeMax, hasMore && !all, merged), nil
}

// resolveScopeID determines the sync scope_id: the --scope-id flag, then the
// cached user id, then a profile lookup (cached back to credentials). In v1 the
// scope_id is always the caller's own user id.
func resolveScopeID(c *client.Client, creds *config.Credentials, cmd *cobra.Command) (string, error) {
	if s := stringFlag(cmd, "scope-id"); s != "" {
		return s, nil
	}
	if creds.UserID != "" {
		return creds.UserID, nil
	}
	data, err := c.GetProfile()
	if err != nil {
		return "", fmt.Errorf("could not resolve scope id from profile: %w", err)
	}
	id := str(parseJSON(client.UnwrapData(data)), "id")
	if id == "" {
		return "", errors.New("could not resolve your user id (pass --scope-id)")
	}
	creds.UserID = id
	_ = config.Save(creds)
	return id, nil
}

// deviceIDOrDefault returns the --device-id flag value or the saved device id.
func deviceIDOrDefault(cmd *cobra.Command, creds *config.Credentials) string {
	if d := stringFlag(cmd, "device-id"); d != "" {
		return d
	}
	return creds.DeviceID
}

// readChanges loads a change-envelope array from a path ("-" = stdin). It
// accepts either a bare array or an object with a "changes" array.
func readChanges(path string) ([]any, error) {
	if path == "" {
		return nil, errors.New("--file is required (use --file - for stdin)")
	}
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read changes: %w", err)
	}
	var asArray []any
	if err := json.Unmarshal(raw, &asArray); err == nil {
		return asArray, nil
	}
	var asObj map[string]any
	if err := json.Unmarshal(raw, &asObj); err == nil {
		if ch, ok := asObj["changes"].([]any); ok {
			return ch, nil
		}
	}
	return nil, errors.New("changes file must be a JSON array of envelopes, or an object with a \"changes\" array")
}

// chunkRaw extracts the raw envelope elements from a pull response's chunk.
func chunkRaw(data []byte) []json.RawMessage {
	var env struct {
		Chunk []json.RawMessage `json:"chunk"`
	}
	_ = json.Unmarshal(data, &env)
	return env.Chunk
}

// maxChunkUSN returns the highest usn in a chunk (envelopes are ascending, so
// it is the last one), defaulting to fallback for an empty chunk.
func maxChunkUSN(chunk []json.RawMessage, fallback int64) int64 {
	max := fallback
	for _, raw := range chunk {
		if u := int64(num(parseJSON(raw), "usn")); u > max {
			max = u
		}
	}
	return max
}

// mergedPull rebuilds a single pull-response document from accumulated chunks.
func mergedPull(scopeID string, scopeMax float64, hasMore bool, chunk []json.RawMessage) []byte {
	out, _ := json.Marshal(map[string]any{
		"scope_id":      scopeID,
		"scope_max_usn": scopeMax,
		"has_more":      hasMore,
		"chunk":         chunk,
	})
	return out
}

// mapSyncError gives friendly messages for sync-specific codes.
func mapSyncError(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "resync_required":
			return errors.New("sync cursor is too old — restart a full sync with --after-usn 0")
		case "scope_forbidden":
			return errors.New("the sync scope does not belong to you")
		}
	}
	return err
}

// syncPushRejectedCode is the CLI-side error code for a push the server
// answered 200 to but refused changes inside. It is not an API code — no such
// envelope exists on the wire — so it is named for what it describes and kept
// stable, because a script branching on it has nothing else to branch on.
const syncPushRejectedCode = "sync_push_rejected"

// syncPushRejectedError is a push whose changes the server refused. It wraps a
// normal error envelope (so it renders and JSON-encodes like every other
// failure) and adds the one thing the envelope cannot carry: whether an
// entitlement gate was the cause, which decides between exit 4 and exit 1.
type syncPushRejectedError struct {
	*client.APIError
	// planLimit is true when at least one refusal was plan_limit_reached.
	planLimit bool
}

// Unwrap exposes the wrapped envelope so errors.As finds it — that is what
// gives this error the standard rendering and the --json error shape.
func (e *syncPushRejectedError) Unwrap() error { return e.APIError }

// ExitCode reports which documented code this failure deserves. A refusal by an
// entitlement gate is the same wall as a REST plan_limit_reached — the account
// said no and retrying will never help — so it gets the same 4 no matter which
// endpoint hit it. Any other refusal is an ordinary failure: 1.
func (e *syncPushRejectedError) ExitCode() int {
	if e.planLimit {
		return exitPlanLimit
	}
	return exitError
}

// syncPushRejection turns the refusals a push reports inside a 200 into a real
// error, so the exit code matches what actually happened.
//
// Push is the only write in the CLI that cannot fail loudly: the endpoint
// applies each change in its own transaction and reports the outcome per
// change, so a batch where the server refused EVERY change still comes back
// 200. Exiting 0 there is the worst answer available — a wrapper script cannot
// tell "pushed everything" from "pushed nothing" and carries on as if the work
// happened.
//
// Only status "rejected" counts. A "conflict" is not a refusal: the change did
// not apply, but that is a defined, routine outcome of the sync protocol whose
// resolution (take server_record, write a conflict copy, push again) is handed
// to the client in the same response. Failing on it would make an ordinary sync
// loop look broken.
func syncPushRejection(data []byte) error {
	results := toSlice(parseJSON(data)["results"])
	details := map[string]any{}
	planLimit := false
	for _, r := range results {
		if str(r, "status") != "rejected" {
			continue
		}
		reason := str(r, "error")
		if reason == "" {
			reason = "rejected"
		}
		if reason == planLimitCode {
			planLimit = true
		}
		details[syncPushChangeLabel(r)] = reason
	}
	if len(details) == 0 {
		return nil
	}

	msg := fmt.Sprintf("the server refused %d of the %d %s in this push",
		len(details), len(results), pluralize(len(results), "change", "changes"))
	if planLimit {
		// The refusal carries the bare wire code and nothing else — no
		// resource, no counts — so this says only what is true. 'harbor usage'
		// is where the numbers actually are.
		msg += " — your account is at a plan limit, so those creates were rejected. " +
			"Run 'harbor usage' to see which limit, or upgrade at " + upgradeURL("")
	}
	return &syncPushRejectedError{
		APIError:  &client.APIError{Code: syncPushRejectedCode, Message: msg, Details: details},
		planLimit: planLimit,
	}
}

// syncPushChangeLabel names a refused change for the error details. The
// change_id is the client's own handle on it — the thing it can look up in the
// batch it just sent — so that is the key, falling back to type/id for a
// server that omitted it.
func syncPushChangeLabel(r map[string]any) string {
	if id := str(r, "change_id"); id != "" {
		return id
	}
	if t, id := str(r, "type"), str(r, "id"); t != "" || id != "" {
		return strings.TrimSpace(t + " " + id)
	}
	return "change"
}

// ===========================================================================
// Display
// ===========================================================================

// displaySyncPull prints a per-type change summary from a pull response.
func displaySyncPull(data []byte) {
	root := parseJSON(data)
	chunk := toSlice(root["chunk"])
	counts := map[string][2]int{} // type -> {live, deleted}
	for _, e := range chunk {
		t := str(e, "type")
		c := counts[t]
		if boolean(e, "deleted") {
			c[1]++
		} else {
			c[0]++
		}
		counts[t] = c
	}
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)
	rows := make([][]string, 0, len(types))
	for _, t := range types {
		rows = append(rows, []string{t, fmt.Sprintf("%d", counts[t][0]), fmt.Sprintf("%d", counts[t][1])})
	}
	printTable([]string{"TYPE", "LIVE", "DELETED"}, rows)
	fmt.Printf("%s changes: %d · scope_max_usn: %s · has_more: %s\n",
		dim("total"), len(chunk), trimFloat(num(root, "scope_max_usn")), boolMark(boolean(root, "has_more")))
}

// displaySyncPush prints per-change push results.
func displaySyncPush(data []byte) {
	root := parseJSON(data)
	results := toSlice(root["results"])
	headers := []string{"CHANGE ID", "TYPE", "ID", "STATUS", "NEW USN", "NOTE"}
	rows := make([][]string, 0, len(results))
	for _, r := range results {
		note := str(r, "error")
		if boolean(r, "deduped") {
			note = appendNote(note, "deduped")
		}
		if _, ok := r["server_record"]; ok {
			note = appendNote(note, "server_record returned")
		}
		rows = append(rows, []string{
			shortID(str(r, "change_id"), 10),
			str(r, "type"),
			shortID(str(r, "id"), 8),
			colorizeStatus(str(r, "status")),
			str(r, "new_usn"),
			note,
		})
	}
	printTable(headers, rows)
	fmt.Printf("%s scope_max_usn: %s\n", dim("→"), trimFloat(num(root, "scope_max_usn")))
}

// displaySyncDevices prints the device list plus scope/GC info.
func displaySyncDevices(data []byte) {
	root := parseJSON(data)
	devices := toSlice(root["data"])
	headers := []string{"DEVICE ID", "NAME", "PLATFORM", "LAST SEEN", "ACKED USN", "STALE"}
	rows := make([][]string, 0, len(devices))
	for _, d := range devices {
		rows = append(rows, []string{
			str(d, "device_id"),
			str(d, "name"),
			str(d, "platform"),
			epochMS(num(d, "last_seen")),
			str(d, "last_acked_usn"),
			boolMark(boolean(d, "stale")),
		})
	}
	printTable(headers, rows)
	fmt.Printf("%s scope_max_usn: %s · gc_floor: %s\n",
		dim("→"), trimFloat(num(root, "scope_max_usn")), trimFloat(num(root, "gc_floor")))
}

// displayDevice renders a single device object as a detail view.
func displayDevice(data []byte) {
	d := parseJSON(client.UnwrapData(data))
	if d == nil {
		fmt.Println(string(data))
		return
	}
	printKV([][2]string{
		{"Device ID", bold(str(d, "device_id"))},
		{"Name", str(d, "name")},
		{"Platform", str(d, "platform")},
		{"Last seen", epochMS(num(d, "last_seen"))},
		{"Last pushed USN", str(d, "last_pushed_usn")},
		{"Last acked USN", str(d, "last_acked_usn")},
		{"Created", epochMS(num(d, "created_at"))},
	})
}

// appendNote joins note fragments with a separator.
func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

func init() {
	syncPullCmd.Flags().Int("after-usn", 0, "Return records with usn greater than this (0 = full sync)")
	syncPullCmd.Flags().Int("limit", 0, "Max changes per chunk (default 100, cap 500)")
	syncPullCmd.Flags().String("device-id", "", "Advance this device's cursor (defaults to your device)")
	syncPullCmd.Flags().Bool("all", false, "Page through every chunk until caught up")
	syncPullCmd.Flags().String("scope-id", "", "Sync scope id (defaults to your user id)")

	syncPushCmd.Flags().String("file", "", "JSON file of change envelopes (use - for stdin)")
	syncPushCmd.Flags().String("device-id", "", "Pushing device id (defaults to your device)")
	syncPushCmd.Flags().String("scope-id", "", "Sync scope id (defaults to your user id)")

	syncRegisterDeviceCmd.Flags().String("device-id", "", "Device id (defaults to your device)")
	syncRegisterDeviceCmd.Flags().String("name", "", "Device display name")
	syncRegisterDeviceCmd.Flags().String("platform", "cli", "Device platform")

	syncAckCmd.Flags().String("device-id", "", "Device id (defaults to your device)")
	syncAckCmd.Flags().Int("acked-usn", 0, "Highest USN fully applied (required)")

	syncCmd.AddCommand(syncPullCmd, syncPushCmd, syncDevicesCmd, syncRegisterDeviceCmd, syncRemoveDeviceCmd, syncAckCmd)
	rootCmd.AddCommand(syncCmd)
}
