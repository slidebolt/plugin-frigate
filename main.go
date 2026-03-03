package main

import (
	"log"

	runner "github.com/slidebolt/sdk-runner"
)

func main() {
	r, err := runner.NewRunner(NewPlugin())
	if err != nil {
		log.Fatal(err)
	}
	if err := r.Run(); err != nil {
		log.Fatal(err)
	}
}