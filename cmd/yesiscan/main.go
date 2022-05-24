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
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
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
	"github.com/awslabs/yesiscan/util/licenses"
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

	home, err := os.UserHomeDir()
	if err != nil {
		logf("error finding home directory: %+v", err)
	}

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

	// is there at least one yes-?
	isAdditive := false ||
		c.Bool("yes-backend-licenseclassifier") ||
		c.Bool("yes-backend-spdx") ||
		c.Bool("yes-backend-askalono") ||
		c.Bool("yes-backend-scancode") ||
		c.Bool("yes-backend-bitbake") ||
		c.Bool("yes-backend-regexp") ||
		false

	cliFlag := func(f string) bool {
		if isAdditive && c.Bool(fmt.Sprintf("yes-backend-%s", f)) {
			return true
		}

		if !isAdditive && !c.Bool(fmt.Sprintf("no-backend-%s", f)) {
			return true
		}

		return false
	}

	if cliFlag("licenseclassifier") {
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

	if cliFlag("spdx") {
		spdxBackend := &backend.Spdx{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf("backend: "+format, v...)
			},
		}
		backends = append(backends, spdxBackend)
		backendWeights[spdxBackend] = 2.0 // TODO: adjust as needed
	}

	if cliFlag("askalono") {
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

	if cliFlag("scancode") {
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

	if cliFlag("bitbake") {
		bitbakeBackend := &backend.Bitbake{
			Debug: debug,
			Logf: func(format string, v ...interface{}) {
				logf("backend: "+format, v...)
			},
		}
		backends = append(backends, bitbakeBackend)
		backendWeights[bitbakeBackend] = 16.0 // TODO: adjust as needed
	}

	regexpPath := ""
	if cliFlag("regexp") {
		if c.IsSet("regexp-path") {
			regexpPath = c.String("regexp-path")
		} else {
			// TODO: implement proper XDG and maybe path precedence?
			if home != "" {
				regexpPath = filepath.Join(home, ".config/", program+"/", "regexp.json")
				regexpPath = filepath.Clean(regexpPath)
			}
		}
	}
	if regexpPath != "" {
		regexpBackend := &backend.Regexp{
			RegexpCore: &backend.RegexpCore{
				Debug: debug,
				Logf: func(format string, v ...interface{}) {
					logf("backend: "+format, v...)
				},
			},

			Filename: regexpPath,
		}
		backends = append(backends, regexpBackend)
		backendWeights[regexpBackend] = 8.0 // TODO: adjust as needed
	}

	//if cliFlag("example") {
	//	exampleBackend := &backend.ExampleClassifier{
	//		Debug: debug,
	//		Logf: func(format string, v ...interface{}) {
	//			logf("backend: "+format, v...)
	//		},
	//	}
	//	backends = append(backends, exampleBackend)
	//	backendWeights[exampleBackend] = 99.0 // TODO: adjust as needed
	//}

	// load the profiles earlier than needed to catch json typos and commas
	profiles := c.StringSlice("profile")
	profilesData := make(map[string]*lib.ProfileData)
	profilesData[lib.DefaultProfileName] = nil // add a "default" profile for fun
	// TODO: implement proper XDG and maybe path precedence?
	for _, x := range profiles {
		var err error
		data := []byte{}
		if home != "" {
			p := fmt.Sprintf("%s.json", x) // TODO: validate input string?
			profilePath := filepath.Join(home, ".config/", program+"/profiles/", p)
			profilePath = filepath.Clean(profilePath)
			data, err = os.ReadFile(profilePath)
			// check errors below...
		}
		if os.IsNotExist(err) || home == "" {
			data, err = os.ReadFile(x)
		}

		if err != nil {
			logf("profile %s: %s", x, err)
			err = nil // reset
			continue
		}

		buffer := bytes.NewBuffer(data)
		if buffer.Len() == 0 {
			// TODO: should this be an error, or just a silent ignore?
			logf("profile %s: empty input file", x)
			continue
		}
		decoder := json.NewDecoder(buffer)

		var profileConfig lib.ProfileConfig // this gets populated during decode
		if err := decoder.Decode(&profileConfig); err != nil {
			// TODO: should this be an error, or just a silent ignore?
			logf("profile %s: error decoding json output: %+v", err)
			continue
		}

		list, err := licenses.StringsToLicenses(profileConfig.Licenses)
		if err != nil {
			logf("profile %s: error parsing license: %+v", err)
			continue
		}

		profilesData[x] = &lib.ProfileData{
			Licenses: list,
			Exclude:  profileConfig.Exclude,
		}
	}

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

	// remove all the invalid/missing profiles, keep in the original order
	newProfiles := []string{}
	for _, x := range profiles {
		if _, exists := profilesData[x]; exists {
			newProfiles = append(newProfiles, x)
		}
	}
	profiles = newProfiles
	if len(profiles) == 0 {
		// add a default profile
		profiles = append(profiles, lib.DefaultProfileName)
	}

	for _, x := range profiles {
		pro, err := lib.SimpleProfiles(results, profilesData[x], backendWeights)
		if err != nil {
			return err
		}

		logf("profile %s:\n%s", x, pro)
	}

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

func stdinAsString(logf func(format string, v ...interface{})) (string, error) {
	logf("waiting for stdin...")
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
