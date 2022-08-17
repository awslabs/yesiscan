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
	"crypto/rand"
	"crypto/sha512"
	_ "embed"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/lib"
	"github.com/awslabs/yesiscan/s3"
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

	flags := []cli.Flag{
		&cli.StringFlag{Name: "regexp-path"},
		&cli.StringSliceFlag{Name: "profile"},
		&cli.StringFlag{Name: "output-s3bucket"},
		&cli.StringFlag{Name: "output-path"},
		&cli.StringFlag{Name: "output-type"},
		&cli.BoolFlag{Name: "quiet"},
	}
	// build the yes and no backend flags
	for _, b := range lib.Backends {
		f := &cli.BoolFlag{Name: fmt.Sprintf("no-backend-%s", b)}
		flags = append(flags, f)
	}
	for _, b := range lib.Backends {
		f := &cli.BoolFlag{Name: fmt.Sprintf("yes-backend-%s", b)}
		flags = append(flags, f)
	}

	app := &cli.App{
		Name:  program,
		Usage: "scan code for legal things",
		Action: func(c *cli.Context) error {
			logf("Hello from purpleidea! This is %s, version: %s", program, version)
			defer logf("Done!")

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			quiet := c.Bool("quiet")
			outputPath := c.String("output-path")
			region := s3.DefaultRegion                    // XXX: get from config
			outputS3Bucket := c.String("output-s3bucket") // XXX: get from config
			bigIntStr := ""                               // for our int
			if outputPath == "-" || quiet {               // if output is stdout, noop logs
				logf = func(format string, v ...interface{}) {
					// noop
				}
			}
			args := []string{}
			for i := 0; i < c.NArg(); i++ {
				s := c.Args().Get(i)
				args = append(args, s)
			}

			// is there at least one yes-?
			isAdditive := false
			for _, f := range lib.Backends {
				if c.Bool(fmt.Sprintf("yes-backend-%s", f)) {
					isAdditive = true
				}
			}

			// isBackendEnabled specifies if a particular backend
			// should be enabled based on the lookup of the flags.
			isBackendEnabled := func(f string) bool {
				if isAdditive && c.Bool(fmt.Sprintf("yes-backend-%s", f)) {
					return true
				}

				if !isAdditive && !c.Bool(fmt.Sprintf("no-backend-%s", f)) {
					return true
				}

				return false
			}

			backends := make(map[string]bool)
			for _, b := range lib.Backends {
				backends[b] = isBackendEnabled(b)
			}

			if outputS3Bucket != "" { // do a test-for-auth run

				bigInt, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt64))
				if err != nil {
					return errwrap.Wrapf(err, "random number generation error")
				}
				bigIntStr = bigInt.String()

				objectName := program // arbitrary, but unique
				contentType := "text/plain"
				inputs := &s3.Inputs{
					Region:            region,
					BucketName:        outputS3Bucket,
					CreateBucket:      true,
					ObjectName:        objectName,
					GrantReadAllUsers: true,
					ContentType:       &contentType,
					Data:              []byte(program), // arbitrary
					Debug:             debug,
					Logf: func(format string, v ...interface{}) {
						logf("s3: "+format, v...)
					},
				}
				// XXX: find a way to check if credentials are
				// good, early in the operation before the scan,
				// otherwise we will end up running a whole scan
				// and then throwing away the results...
				if _, err := s3.Store(ctx, inputs); err != nil {
					return errwrap.Wrapf(err, "s3 setup error")
				}
			}

			m := &lib.Main{
				Program: program,
				Version: version,
				Debug:   debug,
				Logf:    logf,

				Args:     args,
				Backends: backends,

				Profiles: c.StringSlice("profile"),

				RegexpPath: c.String("regexp-path"),
			}

			output, err := m.Run(ctx)
			if err != nil {
				return err
			}

			s := ""
			if outputPath != "" || outputS3Bucket != "" {
				var err error
				// TODO: when we render an html version, should
				// it look the same as the web `save` output?
				if c.String("output-type") == "text" {
					if s, err = lib.ReturnOutputFile(output); err != nil {
						return err
					}
				} else {
					if s, err = web.ReturnOutputHtml(output); err != nil {
						return err
					}
				}
			}

			if outputS3Bucket != "" {
				ext := "html"
				contentType := "text/html"
				if c.String("output-type") == "text" {
					ext = "txt"
					contentType = "text/plain"
				}

				// make a unique ID for the file
				// XXX: we can consider different algorithms or methods here later...
				// We want this hash to be basically impossible
				// to guess, so that you can only get it if you
				// have the secret link.
				if bigIntStr == "" { // make sure we really have one
					// programming error
					return fmt.Errorf("random number generation logic error")
				}
				now := strconv.FormatInt(time.Now().UnixMilli(), 10) // itoa but int64
				sum := sha512.Sum512([]byte(s + now + bigIntStr))    // XXX: for now
				uid := fmt.Sprintf("%x", sum)
				objectName := fmt.Sprintf("%s-%s.%s", program, uid, ext) // TODO: arbitrary

				inputs := &s3.Inputs{
					Region:            region,
					BucketName:        outputS3Bucket,
					CreateBucket:      true,
					ObjectName:        objectName,
					GrantReadAllUsers: true,
					ContentType:       &contentType,
					Data:              []byte(s),
					Debug:             debug,
					Logf: func(format string, v ...interface{}) {
						logf("s3: "+format, v...)
					},
				}
				// XXX: find a way to check if credentials are
				// good, early in the operation before the scan,
				// otherwise we will end up running a whole scan
				// and then throwing away the results...
				u, err := s3.Store(ctx, inputs)
				if err != nil {
					logf("could not write s3 file: %+v", err)
				} else {
					fmt.Printf("S3 Sig URL: %s\n", u)
					fmt.Printf("S3 Pub URL: %s\n", s3.PubURL(region, outputS3Bucket, objectName))
				}
			}

			if outputPath == "-" {
				// NOTE: if we get asked for stdout, we
				// turn off other output to make it sane
				// TODO: should logs go to stderr instead?
				quiet = true           // redundant for now
				_, err := fmt.Print(s) // to stdout
				return err

			} else if outputPath != "" {
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
		Flags:                flags,
		EnableBashCompletion: true,

		Commands: []*cli.Command{
			{
				Name:    "web",
				Aliases: []string{"web"},
				Usage:   "launch a web server mode",
				Action: func(c *cli.Context) error {
					logf("Hello from purpleidea! This is %s, version: %s", program, version)
					defer logf("Done!")

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
