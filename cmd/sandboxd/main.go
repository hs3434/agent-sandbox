// Command sandboxd is the control-plane entrypoint for the agent sandbox
// platform. P0-m2 wires config-driven assembly here (backend registry,
// audit sink, MCP server) — see design-spec-v3 §14.1 for the seam layout.
package main

import "fmt"

func main() {
	fmt.Println("sandboxd (skeleton) — control-plane assembly lands in P0-m2")
}
