/*
Copyright 2020 The Kubermatic Kubernetes Platform contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"os"
)

func main() {
	opts := GenconfCommand{}

	if len(os.Args) == 1 {
		fmt.Printf("usage: %s <command> [<args>]\n", os.Args[0])
		fmt.Println("Commands: ")
		fmt.Printf(" %s   Generates the Envoy configuration for the tunneling agent.", opts.String())
		return
	}

	switch os.Args[1] {
	case opts.String():
		opts.Flags().Parse(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "%q is not valid command.\n", os.Args[1])
		os.Exit(2)
	}

	err := opts.Exec()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error occurred while executing command: %s", err)
		os.Exit(2)
	}
}
