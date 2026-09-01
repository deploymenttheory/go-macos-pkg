package notary

import "errors"

// Errors owned by this package. ErrRejected and ErrTimeout are aliased from
// the SDK in client.go so errors.Is keeps matching the SDK's workflow.
var (
	// ErrUnsupported reports a submission the tool cannot make.
	ErrUnsupported = errors.New("notary: unsupported")
	// ErrCredentials reports missing or unusable notary credentials.
	ErrCredentials = errors.New("notary: credentials")
)
