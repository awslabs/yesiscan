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

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/lib"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/web"

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
func CLI(program, version string, debug bool, logf func(format string, v ...interface{})) error {

	app := &cli.App{
		Name:  program,
		Usage: "scan code for legal things",
		Action: func(c *cli.Context) error {

			quiet := c.Bool("quiet")
			outputPath := c.String("output-path")
			if outputPath == "-" || quiet { // if output is stdout, noop logs
				logf = func(format string, v ...interface{}) {
					// noop
				}
			}
			args := []string{}
			for i := 0; i < c.NArg(); i++ {
				s := c.Args().Get(i)
				args = append(args, s)
			}

			flags := make(map[string]bool)
			names := []string{
				"no-backend-licenseclassifier",
				"no-backend-cran",
				"no-backend-pom",
				"no-backend-spdx",
				"no-backend-askalono",
				"no-backend-scancode",
				"no-backend-bitbake",
				"no-backend-regexp",
				"yes-backend-licenseclassifier",
				"yes-backend-cran",
				"yes-backend-pom",
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
				Version: version,
				Debug:   debug,
				Logf:    logf,

				Args:  args,
				Flags: flags,

				Profiles: c.StringSlice("profile"),

				RegexpPath: c.String("regexp-path"),
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			output, err := m.Run(ctx)
			if err != nil {
				return err
			}

			if outputPath != "" {
				// FIXME: add a method to render an html version
				// TODO: when we render an html version, should
				// it look the same as the web `save` output?
				s, err := lib.ReturnOutputFile(output)
				if err != nil {
					return err
				}

				if c.String("output-type") == "html" {
					s, err = web.ReturnOutputHtml(output)
					if err != nil {
						return err
					}
				}

				if outputPath == "-" {
					// NOTE: if we get asked for stdout, we
					// turn off other output to make it sane
					// TODO: should logs go to stderr instead?
					quiet = true           // redundant for now
					_, err := fmt.Print(s) // to stdout
					return err
				}

				// TODO: is this the umask we should use?
				if err := os.WriteFile(outputPath, []byte(s), interfaces.Umask); err != nil {
					logf("could not write output file: %+v", err)
				}
			}

			if !quiet {
				s, err := lib.ReturnOutputConsole(output)
				if err != nil {
					return err
				}

				fmt.Print(s) // display it
			}

			return nil
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "no-backend-licenseclassifier"},
			&cli.BoolFlag{Name: "no-backend-cran"},
			&cli.BoolFlag{Name: "no-backend-pom"},
			&cli.BoolFlag{Name: "no-backend-spdx"},
			&cli.BoolFlag{Name: "no-backend-askalono"},
			&cli.BoolFlag{Name: "no-backend-scancode"},
			&cli.BoolFlag{Name: "no-backend-bitbake"},
			&cli.BoolFlag{Name: "no-backend-regexp"},
			&cli.StringFlag{Name: "regexp-path"},
			//&cli.BoolFlag{Name: "no-backend-example"},
			&cli.BoolFlag{Name: "yes-backend-licenseclassifier"},
			&cli.BoolFlag{Name: "yes-backend-cran"},
			&cli.BoolFlag{Name: "yes-backend-pom"},
			&cli.BoolFlag{Name: "yes-backend-spdx"},
			&cli.BoolFlag{Name: "yes-backend-askalono"},
			&cli.BoolFlag{Name: "yes-backend-scancode"},
			&cli.BoolFlag{Name: "yes-backend-bitbake"},
			&cli.BoolFlag{Name: "yes-backend-regexp"},
			&cli.StringSliceFlag{Name: "profile"},
			&cli.StringFlag{Name: "output-path"},
			&cli.StringFlag{Name: "output-type"},
			&cli.BoolFlag{Name: "quiet"},
		},
		EnableBashCompletion: true,

		Commands: []*cli.Command{
			{
				Name:    "web",
				Aliases: []string{"web"},
				Usage:   "launch a web server mode",
				Action: func(c *cli.Context) error {
					return Web(c, program, version, debug, logf)
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "profile"},
				},
			},
		},
	}

	return app.Run(os.Args)
}

func main() {
	debug := false // TODO: hardcoded for now
	logf := func(format string, v ...interface{}) {
		fmt.Fprintf(os.Stderr, "main: "+format+"\n", v...)
	}
	program = strings.TrimSpace(program)
	version = strings.TrimSpace(version)
	if program == "" || version == "" {
		// run `go generate` before you build it.
		logf("program was not compiled correctly")
		os.Exit(1)
		return
	}

	// FIXME: We discard output from lib's that use `log` package directly.
	log.SetOutput(io.Discard)

	err := CLI(program, version, debug, logf) // TODO: put these args in an input struct
	if err != nil {
		if debug {
			logf("failed: %+v", err)
		} else {
			logf("failed: %+v", errwrap.Cause(err))
		}
		os.Exit(1)
		return
	}
	os.Exit(0)
}
