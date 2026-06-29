// Command agentcarto-plugin-claude is the executable that serves AgentCarto's Claude plugin as a
// subprocess. The host (agentcarto) launches it as a child process and talks to it over net/rpc.
// Running it standalone exits after a failed handshake (per go-plugin's design).
package main

import (
	"github.com/agentcarto/core/plugin"
	"github.com/agentcarto/plugin-claude"
)

func main() {
	plugin.Serve(claude.Factory{})
}
