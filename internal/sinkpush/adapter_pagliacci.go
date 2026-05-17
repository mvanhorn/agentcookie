package sinkpush

// NewPagliacci returns an adapter for pagliacci-pp-cli. Cookies are
// filtered to pagliacci.com hosts and written into
// ~/.config/pagliacci-pp-cli/config.toml + cookies.json. Format inferred
// from sibling pycookiecheat-style CLIs (airbnb, ebay) since the user
// is not currently logged in to pagliacci.com on MBP and a successful
// auth login --chrome capture was not available at adapter build time.
// End-to-end verification on Mac mini after first login is the open
// validation step; if pagliacci-pp-cli's actual session-file schema
// differs, this adapter's tests will surface it via the failing
// round-trip.
func NewPagliacci() *PycookiecheatStyleAdapter {
	return newPycookiecheatStyleAdapter(
		"pagliacci-pp-cli",
		"%pagliacci%",
		"pagliacci-pp-cli",
		"https://pagliacci.com",
	)
}
