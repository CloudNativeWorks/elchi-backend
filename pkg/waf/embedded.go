package waf

import _ "embed"

//go:embed data/crs_rules_4.14.0.json
var EmbeddedRules4140 []byte

//go:embed data/crs_rules_4.14.0_metadata.json
var EmbeddedMetadata4140 []byte
