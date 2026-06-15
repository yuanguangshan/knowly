package web

import (
	_ "embed"
)

//go:embed index.html
var indexHTML []byte

//go:embed manifest.json
var manifestJSON []byte

//go:embed sw.js
var swJS []byte
