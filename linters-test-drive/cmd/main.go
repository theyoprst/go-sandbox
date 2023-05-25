package main

import (
	"context"
	"flag"
	"os"

	"github.com/google/subcommands"

	"github.com/theyoprst/go-sandbox/linters-test-drive/internal/testdrive"
)

func main() {
	subcommands.Register(subcommands.HelpCommand(), "")
	subcommands.Register(subcommands.FlagsCommand(), "")
	subcommands.Register(subcommands.CommandsCommand(), "")
	subcommands.Register(&testdrive.Cmd{}, "")

	flag.Parse()
	ctx := context.Background()
	os.Exit(int(subcommands.Execute(ctx)))
}
