// Package version is the single source of truth for the Lupinus version
// number. wails.json's productVersion and frontend/package.json's version
// are stamped from this value by scripts/sync-version — see that script
// before bumping this by hand.
package version

const (
	Version = "0.2.9"
	Name    = "Lupinus"
	Author  = "Alperen Yavuz"
	Repo    = "https://github.com/alplix/lupinus"
	Support = "https://coff.ee/alplix"
)
