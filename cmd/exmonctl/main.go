// exmonctl is the administrator's tool for an exmon fleet: it creates the test CA
// on a YubiKey and signs test certificates, and (in later commands) signs and
// publishes configs, releases and commands over MQTT.
//
// It runs on an admin's workstation, never on monitored PCs.
package main

import (
	"fmt"
	"os"
	"sort"
)

// command is one exmonctl command: its one-line help and what it runs.
type command struct {
	help string
	run  func(args []string) error
}

// commands maps "group action" (or a single word) to a command. New commands
// register here.
var commands = map[string]command{
	"ca init":     {"create the test CA certificate for the YubiKey's slot 9c key", caInit},
	"ca sign-csr": {"sign a test device's certificate request with the YubiKey CA", caSignCSR},
}

func main() {
	args := os.Args[1:]
	if len(args) >= 2 {
		if c, ok := commands[args[0]+" "+args[1]]; ok {
			exit(c.run(args[2:]))
			return
		}
	}
	if len(args) >= 1 {
		if c, ok := commands[args[0]]; ok {
			exit(c.run(args[1:]))
			return
		}
	}
	usage()
	if len(args) > 0 && args[0] != "help" && args[0] != "-h" && args[0] != "--help" {
		os.Exit(2)
	}
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "exmonctl:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println("usage: exmonctl <command> [flags]   (add -h after a command for its flags)")
	fmt.Println()
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Printf("  %-22s %s\n", n, commands[n].help)
	}
}
