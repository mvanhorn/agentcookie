//go:build macapp

package macapp

import _ "embed"

//go:embed embedded/agentcookie.zip
var embeddedZip []byte
