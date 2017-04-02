package main

import (
	"fmt"
	"io/ioutil"
	"os"
)

func main() {
	state, err := ioutil.ReadAll(os.Stdin)

	if err != nil {
		fmt.Fprintf(os.Stdout, "%s", err)
		panic(err)
	}

	os.Stdout.Write(state)
	os.Stdout.Close()
	os.Stdin.Close()
	os.Exit(0)
}
