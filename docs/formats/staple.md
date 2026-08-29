# Notarization tickets and stapling

Notarizing a package does not change the package. Apple keeps a
**ticket** — a CMS blob it signed, beginning `s8ch` — in a public
CloudKit database, keyed by the package's signature digest, and macOS
looks it up when the package is opened. **Stapling** attaches the ticket
to the file so the check works offline.

## Where the ticket goes

After the end of the xar archive (the last byte the table of contents
accounts for), as a trailer:

```
[xar archive]
[Trailer: magic "t8lr", version 1, type 1 (terminator), length 0, unused]   16 bytes
[ticket bytes]
[Trailer: magic "t8lr", version 1, type 2 (ticket), length N, unused]       16 bytes
```

The trailers are little-endian — the only little-endian structure in the
whole format. A reader finds a ticket by reading the last 16 bytes.
Nothing inside the archive changes, so signatures stay valid. Re-signing
produces a new digest, so `sign` removes any ticket first.

## Looking a ticket up

`POST https://api.apple-cloudkit.com/database/1/com.apple.gk.ticket-delivery/production/public/records/lookup`
with `{"records":[{"recordName":"2/<type>/<hex>"}]}`, no authentication.
`<type>` is 1 for a SHA-1 table-of-contents digest and 2 for SHA-256;
`<hex>` is the first twenty bytes of that digest. The response's
`fields.signedTicket.value` is the base64 ticket; a `serverErrorCode` of
`NOT_FOUND` means Apple has none (not notarized, or not published yet —
tickets appear shortly after acceptance).

This is what `macospkg staple` does, and what `verify --online` uses to
answer whether Apple has a ticket for exactly this package.
