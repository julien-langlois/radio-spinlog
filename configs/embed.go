// Package configs embeds the built-in station definitions into the binary,
// so an installed radio-spinlog works without a checkout of the repository.
package configs

import "embed"

// FS holds every <country>/radios.json of this directory
//
//go:embed */radios.json
var FS embed.FS
