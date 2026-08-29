package notary

import "errors"

// Errors.
var (
	// ErrUnsupported reports a submission the tool cannot make.
	ErrUnsupported = errors.New("notary: unsupported")
	// ErrCredentials reports missing or unusable notary credentials.
	ErrCredentials = errors.New("notary: credentials")
	// ErrRejected reports a submission Apple marked Invalid or Rejected.
	ErrRejected = errors.New("notary: submission rejected")
	// ErrTimeout reports a wait that ended while still in progress.
	ErrTimeout = errors.New("notary: timed out waiting for the submission")
)
