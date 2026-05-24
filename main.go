package main

import (
	"flag"
	"io"
	"log"
	"os"
)

var version = "unknown"
var commit = "unknown"
var date = "unknown"

func runCLI(args []string) int {
	fs := flag.NewFlagSet("clickhouse-bulk", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configFile := fs.String("config", "config.json", "config file (json)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 && fs.Arg(0) == "version" {
		log.Printf("clickhouse-bulk v%s (commit: %s, built: %s)\n", version, commit, date)
		return 0
	}

	log.Printf("Starting clickhouse-bulk v%s (commit: %s, built: %s)\n", version, commit, date)

	cnf, err := ReadConfig(*configFile)
	if err != nil {
		log.Printf("ERROR: %+v\n", err)
		return 1
	}
	RunServer(cnf)
	return 0
}

func main() {
	log.SetOutput(os.Stdout)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	os.Exit(runCLI(os.Args[1:]))
}
