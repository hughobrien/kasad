package web

import "embed"

// DatastarFile is the vendored datastar bundle; see vendor/README.md.
const DatastarFile = "vendor/datastar-1.0.1.min.js"

//go:embed style.css favicon.svg vendor
var assetFS embed.FS
