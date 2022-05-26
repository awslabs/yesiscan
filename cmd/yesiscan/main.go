// Copyright Amazon.com Inc or its affiliates and the project contributors
// Written by James Shubin <purple@amazon.com> and the project contributors
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not
// use this file except in compliance with the License. You may obtain a copy of
// the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations under
// the License.
//
// We will never require a CLA to submit a patch. All contributions follow the
// `inbound == outbound` rule.
//
// This is not an official Amazon product. Amazon does not offer support for
// this project.

package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/awslabs/yesiscan/lib"
	"github.com/awslabs/yesiscan/util/errwrap"

	cli "github.com/urfave/cli/v2" // imports as package "cli"
)

// Hide a program/version string for build embedding.
//go:generate bash -c "basename $(pwd) | tr -d '\n' > .program"
//go:generate bash -c "git describe --match '[0-9]*.[0-9]*.[0-9]*' --tags --dirty --always > .version"

//go:embed .program
var program string

//go:embed .version
var version string

// CLI is the entry point for the CLI frontend.
func CLI(program string, debug bool, logf func(format string, v ...interface{})) error {

	app := &cli.App{
		Name:  program,
		Usage: "scan code for legal things",
		Action: func(c *cli.Context) error {

			args := []string{}
			for i := 0; i < c.NArg(); i++ {
				s := c.Args().Get(i)
				args = append(args, s)
			}

			flags := make(map[string]bool)
			names := []string{
				"no-backend-licenseclassifier",
				"no-backend-spdx",
				"no-backend-askalono",
				"no-backend-scancode",
				"no-backend-bitbake",
				"no-backend-regexp",
				"yes-backend-licenseclassifier",
				"yes-backend-spdx",
				"yes-backend-askalono",
				"yes-backend-scancode",
				"yes-backend-bitbake",
				"yes-backend-regexp",
			}
			for _, f := range names {
				if c.IsSet(f) {
					flags[f] = c.Bool(f)
				}
			}

			m := &lib.Main{
				Program: program,
				Debug:   debug,
				Logf:    logf,

				Args:  args,
				Flags: flags,

				Profiles: c.StringSlice("profile"),

				RegexpPath: c.String("regexp-path"),
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			return m.Run(ctx)
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "no-backend-licenseclassifier"},
			&cli.BoolFlag{Name: "no-backend-spdx"},
			&cli.BoolFlag{Name: "no-backend-askalono"},
			&cli.BoolFlag{Name: "no-backend-scancode"},
			&cli.BoolFlag{Name: "no-backend-bitbake"},
			&cli.BoolFlag{Name: "no-backend-regexp"},
			&cli.StringFlag{Name: "regexp-path"},
			//&cli.BoolFlag{Name: "no-backend-example"},
			&cli.BoolFlag{Name: "yes-backend-licenseclassifier"},
			&cli.BoolFlag{Name: "yes-backend-spdx"},
			&cli.BoolFlag{Name: "yes-backend-askalono"},
			&cli.BoolFlag{Name: "yes-backend-scancode"},
			&cli.BoolFlag{Name: "yes-backend-bitbake"},
			&cli.BoolFlag{Name: "yes-backend-regexp"},
			&cli.StringSliceFlag{Name: "profile"},
		},
		EnableBashCompletion: true,
	}

	return app.Run(os.Args)
}

func main() {
	debug := false // TODO: hardcoded for now
	logf := func(format string, v ...interface{}) {
		fmt.Printf("main: "+format+"\n", v...)
	}
	program = strings.TrimSpace(program)
	version = strings.TrimSpace(version)
	if program == "" || version == "" {
		// run `go generate` before you build it.
		logf("program was not compiled correctly")
		os.Exit(1)
		return
	}

	logf("Hello from purpleidea! This is %s, version: %s", program, version)
	// FIXME: We discard output from lib's that use `log` package directly.
	log.SetOutput(io.Discard)

	err := CLI(program, debug, logf) // TODO: put these args in an input struct
	if err != nil {
		if debug {
			logf("failed: %+v", err)
		} else {
			logf("failed: %+v", errwrap.Cause(err))
		}
		os.Exit(1)
		return
	}
	logf("Done!")
	os.Exit(0)
}
