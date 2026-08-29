// Looking up notarization tickets: Apple publishes each ticket in a public
// CloudKit database, keyed by the signed thing's digest, which is how
// stapler finds the ticket for a package it has never seen.
package staple

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/deploymenttheory/go-macos-pkg/pkg/xar"
)

// TicketLookupURL is Apple's public ticket-delivery database. No
// authentication is needed: tickets are public by design.
const TicketLookupURL = "https://api.apple-cloudkit.com/database/1/com.apple.gk.ticket-delivery/production/public/records/lookup"

// RecordName derives the CloudKit record name for a xar archive: the
// digest type code, then the first twenty bytes of the table-of-contents
// digest in hex. The codes are Apple's: 1 for SHA-1, 2 for SHA-256.
func RecordName(alg xar.ChecksumAlg, tocDigest []byte) (string, error) {
	var code int
	switch alg {
	case xar.ChecksumSHA1:
		code = 1
	case xar.ChecksumSHA256:
		code = 2
	default:
		return "", fmt.Errorf("staple: no ticket record for a %s digest", alg)
	}
	if len(tocDigest) > 20 {
		tocDigest = tocDigest[:20]
	}
	return fmt.Sprintf("2/%d/%s", code, hex.EncodeToString(tocDigest)), nil
}

// RecordNameFor derives the record name of an opened archive.
func RecordNameFor(x *xar.Reader) (string, error) {
	digest := x.StoredTOCDigest()
	if digest == nil {
		return "", errors.New("staple: the archive has no table-of-contents digest")
	}
	return RecordName(x.Header().ChecksumAlg, digest)
}

// Lookup fetches tickets from Apple.
type Lookup struct {
	URL    string
	Client *http.Client
}

// NewLookup returns a Lookup against Apple's database.
func NewLookup() *Lookup {
	return &Lookup{URL: TicketLookupURL, Client: &http.Client{Timeout: 60 * time.Second}}
}

type lookupRequest struct {
	Records []struct {
		RecordName string `json:"recordName"`
	} `json:"records"`
}

type lookupResponse struct {
	Records []struct {
		RecordName      string `json:"recordName"`
		RecordType      string `json:"recordType"`
		ServerErrorCode string `json:"serverErrorCode"`
		Reason          string `json:"reason"`
		Fields          struct {
			SignedTicket struct {
				Type  string `json:"type"`
				Value string `json:"value"`
			} `json:"signedTicket"`
		} `json:"fields"`
	} `json:"records"`
}

// Fetch returns the ticket for a record name, or ErrNoTicket when Apple
// has none: the package was never notarized, or its ticket is not yet
// published (they appear a little after notarization completes).
func (l *Lookup) Fetch(ctx context.Context, recordName string) ([]byte, error) {
	var req lookupRequest
	req.Records = append(req.Records, struct {
		RecordName string `json:"recordName"`
	}{recordName})
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("staple: ticket lookup: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("staple: ticket lookup returned %s", resp.Status)
	}
	var out lookupResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("staple: ticket lookup: malformed response: %w", err)
	}
	if len(out.Records) == 0 {
		return nil, ErrNoTicket
	}
	rec := out.Records[0]
	if rec.ServerErrorCode != "" || rec.Fields.SignedTicket.Value == "" {
		if rec.ServerErrorCode == "NOT_FOUND" || rec.ServerErrorCode == "" {
			return nil, ErrNoTicket
		}
		return nil, fmt.Errorf("staple: ticket lookup: %s (%s)", rec.ServerErrorCode, rec.Reason)
	}
	ticket, err := base64.StdEncoding.DecodeString(rec.Fields.SignedTicket.Value)
	if err != nil {
		return nil, fmt.Errorf("staple: ticket is not base64: %w", err)
	}
	if !bytes.HasPrefix(ticket, ticketMagic) {
		return nil, fmt.Errorf("staple: ticket does not begin with %q", ticketMagic)
	}
	return ticket, nil
}
