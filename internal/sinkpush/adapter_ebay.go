package sinkpush

// NewEbay returns an adapter for ebay-pp-cli. Cookies are filtered to
// ebay.com hosts and written into ~/.config/ebay-pp-cli/config.toml.
// Format verified on 2026-05-17 against an existing ebay-pp-cli
// config.toml on Matt's MBP (created by ebay-pp-cli auth login). The
// same struct also writes cookies.json -- ebay-pp-cli on MBP only had
// config.toml after its native auth flow, but writing the shadow file
// is harmless and matches the pattern for sibling CLIs.
func NewEbay() *PycookiecheatStyleAdapter {
	return newPycookiecheatStyleAdapter(
		"ebay-pp-cli",
		"%ebay%",
		"ebay-pp-cli",
		"https://www.ebay.com",
	)
}
