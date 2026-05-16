// Command agentcookie is the unified CLI for the agentcookie product.
//
// It exposes subcommands for both source (the machine where the user logs in)
// and sink (the machine running AI agents). Configuration lives under
// ~/.config/agentcookie/ and is loaded lazily by each subcommand.
package main

import "github.com/mvanhorn/agentcookie/internal/cli"

func main() {
	cli.Execute()
}
