package main

import (
	"log"

	runner "github.com/slidebolt/sdk-runner"
)

func main() {
	if err := runner.RunCLI(func() runner.Plugin { return NewPlugin() }); err != nil {
		log.Fatal(err)
	}
}
