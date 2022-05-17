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
	"path/filepath"
	"strings"

	"github.com/awslabs/yesiscan/backend"
	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/lib"
	"github.com/awslabs/yesiscan/parser"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"

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
			return Main(c, program, debug, logf)
		},
		Flags: []cli.Flag{
			&cli.BoolFlag{Name: "no-backend-licenseclassifier"},
			&cli.BoolFlag{Name: "no-backend-spdx"},
			&cli.BoolFlag{Name: "no-backend-askalono"},
			&cli.BoolFlag{Name: "no-backend-scancode"},
			//&cli.BoolFlag{Name: "no-backend-example"},
		},
	}

	return app.Run(os.Args)
}

// Main is the general entry point for running this software.
// TODO: replace the *cli.Context with a more general context that can be used
// by all the different frontends.
func Main(c *cli.Context, program string, debug bool, logf func(format string, v ...interface{})) error {

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(userCacheDir, interfaces.Umask); err != nil {
		return err
	}
	prefix := filepath.Join(userCacheDir, program)
	if err := os.MkdirAll(prefix, interfaces.Umask); err != nil {
		return err
	}
	safePrefixAbsDir, err := safepath.ParseIntoAbsDir(prefix)
	if err != nil {
		return err
	}
	logf("prefix: %s", safePrefixAbsDir)

	// TODO: add more --flags to specify which parser/backends to use...

	inputStrings := []string{}

	for i := 0; i < c.NArg(); i++ {
		s := c.Args().Get(i)
		if s == "-" { // stdin
			var err error
			s, err = stdinAsString(logf)
			if err != nil {
				return err
			}
		}
		inputStrings = append(inputStrings, s)
	}
	if c.NArg() == 0 { // if we didn't get any args, assume stdin
		s, err := stdinAsString(logf)
		if err != nil {
			return err
		}
		inputStrings = append(inputStrings, s)
	}

	iterators := []interfaces.Iterator{}
	for _, s := range inputStrings {
		trivialURIParser := &parser.TrivialURIParser{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf(format, v...)
			},
			Prefix: safePrefixAbsDir,
			Input:  s,
		}
		logf("input: %s", s)

		ixs, err := trivialURIParser.Parse() // parser returns iterators
		if err != nil {
			return errwrap.Wrapf(err, "parser failed")
		}
		iterators = append(iterators, ixs...)
	}

	backends := []interfaces.Backend{}
	backendWeights := make(map[interfaces.Backend]float64)

	if !c.Bool("no-backend-licenseclassifier") {
		licenseClassifierBackend := &backend.LicenseClassifier{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf("backend: "+format, v...)
			},
			IncludeHeaders:       false,
			UseDefaultConfidence: false,

			// useful for testing before we add file name filtering
			//SkipZeroResults: true,
		}
		backends = append(backends, licenseClassifierBackend)
		backendWeights[licenseClassifierBackend] = 1.0 // TODO: adjust as needed
	}

	if !c.Bool("no-backend-spdx") {
		spdxBackend := &backend.Spdx{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf("backend: "+format, v...)
			},
		}
		backends = append(backends, spdxBackend)
		backendWeights[spdxBackend] = 2.0 // TODO: adjust as needed
	}

	if !c.Bool("no-backend-askalono") {
		askalonoBackend := &backend.Askalono{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf("backend: "+format, v...)
			},

			// useful for testing before we add file name filtering
			//SkipZeroResults: true,
		}
		backends = append(backends, askalonoBackend)
		backendWeights[askalonoBackend] = 4.0 // TODO: adjust as needed
	}

	if !c.Bool("no-backend-scancode") {
		scancodeBackend := &backend.Scancode{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf("backend: "+format, v...)
			},

			// useful for testing before we add file name filtering
			//SkipZeroResults: true,
		}
		backends = append(backends, scancodeBackend)
		backendWeights[scancodeBackend] = 8.0 // TODO: adjust as needed
	}

	//if !c.Bool("no-backend-example") {
	//	exampleBackend := &backend.ExampleClassifier{
	//		Debug: debug,
	//		Logf: func(format string, v ...interface{}) {
	//			logf("backend: "+format, v...)
	//		},
	//	}
	//	backends = append(backends, exampleBackend)
	//	backendWeights[exampleBackend] = 99.0 // TODO: adjust as needed
	//}

	core := &lib.Core{
		Debug: debug,
		Logf: func(format string, v ...interface{}) {
			logf("core: "+format, v...)
		},
		Backends:        backends,
		Iterators:       iterators, // TODO: should this be passed into Run instead?
		ShutdownOnError: false,     // set to true for "perfect" scanning.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := core.Init(ctx); err != nil {
		return errwrap.Wrapf(err, "could not initialize core")
	}

	results, err := core.Run(ctx)
	if err != nil {
		return errwrap.Wrapf(err, "core run failed")
	}

	str, err := lib.SimpleResults(results, backendWeights)
	if err != nil {
		return err
	}

	logf("Results...\n%s", str)

	return nil
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
		logf("failed: %+v", err)
		os.Exit(1)
		return
	}
	logf("Done!")
	os.Exit(0)
}

func stdinAsString(logf func(format string, v ...interface{})) (string, error) {
	logf("waiting for stdin...")
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
