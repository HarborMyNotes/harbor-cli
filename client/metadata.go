// Copyright 2026 Cloudmanic Labs, LLC. All rights reserved.
// Date: 2026-09-29

package client

import "net/url"

// Notes, notebooks and stacks share one metadata contract: the same four verbs
// on a `…/metadata` route, a bare JSON object as the request body, and
// `{"metadata": {…}}` as the answer. So the methods below take the route rather
// than repeating themselves per record type, and the three *MetadataPath
// helpers are the only place that knows where each route lives.

// NoteMetadataPath is the metadata route for one note.
func NoteMetadataPath(id string) string {
	return "/notes/" + id + "/metadata"
}

// NotebookMetadataPath is the metadata route for one notebook.
func NotebookMetadataPath(id string) string {
	return "/notebooks/" + id + "/metadata"
}

// StackMetadataPath is the metadata route for one stack. A stack is addressed
// by its name, which is free text, so it is escaped — a space or `?` in a name
// would otherwise change what the URL means.
func StackMetadataPath(name string) string {
	return "/stacks/" + url.PathEscape(name) + "/metadata"
}

// GetMetadata reads a record's metadata. Answers {"metadata": {…}}.
func (c *Client) GetMetadata(path string) ([]byte, error) {
	return c.doGet(path, nil)
}

// ReplaceMetadata stores obj as the record's whole metadata object (PUT).
// Answers {"metadata": {…}} as stored.
func (c *Client) ReplaceMetadata(path string, obj map[string]any) ([]byte, error) {
	return c.doPut(path, obj)
}

// MergeMetadata applies a JSON Merge Patch (RFC 7396) to the record's metadata:
// a key set to nil is removed, nested objects merge, anything else is set.
// Answers {"metadata": {…}} with the merged result.
func (c *Client) MergeMetadata(path string, patch map[string]any) ([]byte, error) {
	return c.doPatch(path, patch)
}

// ClearMetadata removes all of a record's metadata. Answers 204 with no body.
func (c *Client) ClearMetadata(path string) ([]byte, error) {
	return c.doDelete(path, nil)
}
