package version

import "fmt"

const (
	// Major is the major version component (breaking protocol changes).
	Major = 0

	// Minor is the minor version component (new features, backward-compatible).
	Minor = 2

	// Patch is the patch version component (bug fixes).
	Patch = 0

	// ProtocolVersion is the peer-to-peer wire protocol version.
	// Increment when the wire format changes in a backward-incompatible way.
	ProtocolVersion uint32 = 1

	// ClientName identifies this implementation.
	ClientName = "fairchain"
)

// String returns the semantic version string (e.g. "0.1.0").
func String() string {
	return fmt.Sprintf("%d.%d.%d", Major, Minor, Patch)
}

// UserAgent returns the BIP-style user agent (e.g. "/fairchain:0.1.0/").
func UserAgent() string {
	return fmt.Sprintf("/%s:%s/", ClientName, String())
}
