<!--
Harbor CLI — crypto/README.md
Copyright (c) 2026 Cloudmanic Labs, LLC. All rights reserved. Date: 2026-06-22
-->

# Harbor client encryption — canonical contract

This package is the reference implementation of Harbor's **client-side, end-to-end
note encryption**. The server is **zero-knowledge**: it stores opaque ciphertext
and an opaque per-user keystore and never holds, derives, or validates any key.
All key derivation, wrapping, and field encryption happens in the client.

Two formats must match **byte-for-byte across every Harbor client** (CLI, iOS,
web, …) for notes encrypted on one device to decrypt on another. This is that
contract. The server's only crypto behavior is a structural check of the field
envelope (`internal/notes/crypto.go`) and excluding encrypted content from
search/OCR/sharing/export.

## Key model — envelope encryption

1. **KEK** — `Argon2id(passphrase, salt)` → a 32-byte Key-Encryption-Key.
2. **Master key** — a random 32-byte key generated once at setup. Every encrypted
   note field is encrypted with the master key.
3. **Wrap** — the master key is encrypted ("wrapped") with the KEK and stored in
   the keystore. Changing the passphrase only re-wraps the master key — no note is
   ever re-encrypted.

## HRBK1 keystore (one synced, opaque per-user record)

`keystore.blob` is **UTF-8 JSON**, these fields exactly (snake_case is part of the
contract):

```json
{
  "version": "HRBK1",
  "kdf": "argon2id",
  "kdf_mem_kib": 65536,
  "kdf_iterations": 3,
  "kdf_parallelism": 1,
  "salt": "<base64url, unpadded, 16 bytes>",
  "wrapped_key": "<base64url, unpadded: iv(12) ‖ AES-256-GCM(KEK, master_key) ‖ tag(16)>"
}
```

- **Argon2id** (RFC 9106), default `m = 65536` KiB, `t = 3`, `p = 1`, 16-byte salt
  → 32-byte KEK.
- **Key wrap**: AES-256-GCM, key = KEK, nonce = the first 12 bytes of
  `wrapped_key`, **no AAD**.
- Exactly one live keystore row per user; it syncs like any record (last-write-wins).

## HRBC2 field envelope (a note's title and content)

```
"HRBC2" "." base64url(iv[12]) "." base64url(ciphertext ‖ tag[16])
```

- **AES-256-GCM** under the **master key**, a **fresh random 12-byte nonce** per
  operation (never reuse).
- **base64url is UNPADDED** (`RawURLEncoding`) — matches the server validator.
- **AAD** = the UTF-8 bytes of `recordID + ":" + fieldName` — **id first, a
  literal colon, then the field name**; `fieldName` is the literal `"title"` or
  `"content"`. e.g. `"9c2e…:title"`. This binds an envelope to one note and one
  field; the same AAD must be supplied to decrypt or GCM authentication fails.
  ⚠️ **This byte sequence is the cross-client interop contract, not a local
  choice.** The canonical definition lives in the web client
  (`app.harbor.my` → `web/src/lib/crypto/envelope.ts`); macOS/iOS, Android and
  Windows all match it byte-for-byte, and so must this CLI. The separator was
  missing here until #86, which made every CLI-sealed note undecryptable by every
  other client. The shared known-answer vector is pinned as a literal in
  `crypto_test.go` (`crossClientVector`, asserted in both directions by
  `TestSealField_CrossClientVector` and `TestOpenField_CrossClientVector`) so it
  cannot drift again.
- An encrypted note may have an **empty title** (sent as `""`, not an envelope);
  `content` must always be a valid envelope.

### Attachments (binary envelope — not yet implemented in the CLI)

```
"HRBC2"(5 ASCII bytes) ‖ iv(12) ‖ ciphertext ‖ tag(16)   // raw bytes
resources.hash = sha256(the whole binary envelope)
```

## Writing an encrypted note

Encrypted notes must be created with a **client-generated `id`** (sent in the
create body) because the field AAD binds to it. Send `is_encrypted: true` with
the sealed `title`/`content` through the normal REST `POST`/`PATCH /notes`
endpoints (which structurally validate the envelope). The plaintext shadow must
be empty. `append` is not supported on encrypted notes.

## Irreversibility

There is **no key escrow and no reset**. If the passphrase is lost, the master
key — and every encrypted note — is permanently unrecoverable.

See the CLI surface in `cmd/crypto.go` and the authoritative server spec at
`docs/encryption.md` in the API repo.
