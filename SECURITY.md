# Security

Please report suspected vulnerabilities privately through GitHub's
"Report a vulnerability" form on this repository rather than in a public
issue. We will acknowledge the report and work with you on a fix and a
coordinated disclosure.

This tool handles signing keys and notarization credentials. It never sends a
private key anywhere: signing happens in-process, and only the package's
SHA-256 and the package bytes go to Apple's notary service.
