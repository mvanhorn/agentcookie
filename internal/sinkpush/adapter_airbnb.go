package sinkpush

// NewAirbnb returns an adapter for airbnb-pp-cli. Cookies are filtered
// to airbnb.com hosts and written into ~/.config/airbnb-pp-cli/config.toml
// and ~/.config/airbnb-pp-cli/cookies.json. Verified end-to-end on
// 2026-05-17 by inspecting the files airbnb-pp-cli auth login --chrome
// writes after a successful Chrome cookie import on MBP -- this adapter
// reproduces that exact shape.
func NewAirbnb() *PycookiecheatStyleAdapter {
	return newPycookiecheatStyleAdapter(
		"airbnb-pp-cli",
		"%airbnb%",
		"airbnb-pp-cli",
		"https://www.airbnb.com",
	)
}
