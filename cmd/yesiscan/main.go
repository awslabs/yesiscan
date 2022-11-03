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
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/lib"
	"github.com/awslabs/yesiscan/s3"
	"github.com/awslabs/yesiscan/util"
	"github.com/awslabs/yesiscan/util/ansi"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
	"github.com/awslabs/yesiscan/web"

	"github.com/mitchellh/go-homedir"
	"github.com/ssgelm/cookiejarparser"
	cli "github.com/urfave/cli/v2" // imports as package "cli"
)

// Hide a program/version string for build embedding.
//go:generate bash -c "basename $(pwd) | tr -d '\n' > .program"
//go:generate bash -c "git describe --match '[0-9]*.[0-9]*.[0-9]*' --tags --dirty --always > .version"

//go:embed .program
var program string

//go:embed .version
var version string

// autoConfigURI is set via -ldflags build time flags.
var autoConfigURI string

// autoConfigCookiePath is set via -ldflags build time flags.
var autoConfigCookiePath string

const (
	// ConfigFileName is the name of the config file used to pull in all the
	// various main settings that we want.
	ConfigFileName = "config.json"

	// MaxRedirects is the maximum number of redirects to allow for http
	// download operations. The internal golang maximum of ten is too low
	// for many situations. Firefox sets network.http.redirection-limit as
	// 20.
	MaxRedirects = 20 // do what firefox does
)

// CLI is the entry point for the CLI frontend.
func CLI(program, version string, debug bool) error {

	flags := []cli.Flag{
		&cli.StringFlag{
			Name:  "auto-config-uri",
			Usage: "override/specify an auto config URI",
		},
		&cli.StringFlag{
			Name:  "auto-config-cookie-path",
			Usage: "override/specify an auto config cookie path",
		},
		&cli.IntFlag{
			Name:  "auto-config-expiry-seconds",
			Usage: "minimum number of seconds before config must be re-downloaded",
		},
		&cli.BoolFlag{
			Name:  "auto-config-force-update",
			Usage: "force an auto config re-download",
		},
		&cli.BoolFlag{
			Name:  "quiet",
			Usage: "remove most log messages",
		},
		&cli.BoolFlag{
			Name:  "ansi-magic",
			Usage: "do some ansi terminal escape sequence magic",
		},
		&cli.BoolFlag{
			Name:  "no-ansi-magic",
			Usage: "do not use the ansi terminal escape sequence magic",
		},
		&cli.StringFlag{
			Name:  "regexp-path",
			Usage: "path to regexp rules file",
		},
		&cli.StringFlag{
			Name:  "config-path",
			Usage: "path to the main config file",
		},
		&cli.StringFlag{
			Name:  "output-type",
			Usage: "output type for reports, one of `html` or `text`",
		},
		&cli.StringFlag{
			Name:  "output-path",
			Usage: "output path for reports (specify a dash for stdout)",
		},
		&cli.StringFlag{
			Name:  "output-template",
			Usage: "output templated path for reports (specify a dash for stdout)",
		},
		&cli.StringFlag{
			Name:  "output-s3bucket",
			Usage: "bucket name to upload to s3",
		},
		&cli.StringFlag{
			Name:  "region",
			Usage: "region to use for s3 api requests",
		},
		&cli.StringSliceFlag{
			Name:  "profile",
			Usage: "license set filtering profile to include",
		},
		//&cli.StringSliceFlag{Name: "config"}, // TODO: map not list
	}
	// build the yes and no backend flags
	for _, b := range lib.Backends {
		f := &cli.BoolFlag{
			Name:     fmt.Sprintf("no-backend-%s", b),
			Usage:    "do not include this backend",
			Category: "backends",
		}
		flags = append(flags, f)
	}
	for _, b := range lib.Backends {
		f := &cli.BoolFlag{
			Name:     fmt.Sprintf("yes-backend-%s", b),
			Usage:    "only include this backend",
			Category: "backends",
		}
		flags = append(flags, f)
	}

	description := ""
	description += "Use yesiscan to perform license scanning on your code.\n"
	description += "For example, try running:\n"
	description += "yesiscan --output-type html --no-backend-scancode --no-backend-regexp https://github.com/amznpurple/license-finder-repo\n"
	app := &cli.App{
		Name:  program,
		Usage: "scan code for legal things",
		Authors: []*cli.Author{
			{Name: "James Shubin (@purpleidea)", Email: "purple@amazon.com"},
		},
		Description: strings.TrimSuffix(description, "\n"),
		Action: func(c *cli.Context) error {
			return App(c, program, version, debug)
		},
		Flags:                flags,
		EnableBashCompletion: true,

		Commands: []*cli.Command{
			{
				Name:    "web",
				Aliases: []string{"web"},
				Usage:   "launch a web server mode",
				Action: func(c *cli.Context) error {
					return Web(c, program, version, debug)
				},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{
						Name:  "profile",
						Usage: "license set filtering profile to include",
					},
					&cli.StringFlag{
						Name:  "listen",
						Usage: "address/port to listen on (eg: 127.0.0.1:8000)",
					},
				},
			},
		},
	}

	return app.Run(os.Args)
}

// App is the main entry point action for the regular yesiscan cli application.
func App(c *cli.Context, program, version string, debug bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	bigIntStr := "" // for our int
	var autoConfigExpirySeconds int
	var autoConfigForceUpdate bool
	var quiet bool
	var ansiMagic bool
	var regexpPath string
	// config-path makes no sense here
	var outputType string
	var outputPath string
	var outputTemplate string
	var outputS3Bucket string
	region := s3.DefaultRegion
	profiles := []string{}
	configs := make(map[string]string)
	backends := make(map[string]bool)

	// load from main config file or xdg if config is empty
	config, err := GetConfig(c.String("config-path"))
	if err != nil {
		return err
	}
	if config != nil {
		if config.AutoConfigURI != nil {
			// set this global var
			autoConfigURI = *config.AutoConfigURI
		}
		if config.AutoConfigCookiePath != nil {
			// set this global var
			autoConfigCookiePath = *config.AutoConfigCookiePath
		}
		if config.AutoConfigExpirySeconds != nil {
			// set this global var
			autoConfigExpirySeconds = *config.AutoConfigExpirySeconds
		}
		if config.AutoConfigForceUpdate != nil {
			// set this global var
			autoConfigForceUpdate = *config.AutoConfigForceUpdate
		}
		if config.Quiet != nil {
			quiet = *config.Quiet
		}
		if config.AnsiMagic != nil {
			ansiMagic = *config.AnsiMagic
		}
		if config.RegexpPath != nil {
			regexpPath = *config.RegexpPath
		}
		// config-path makes no sense here
		if config.OutputType != nil {
			outputType = *config.OutputType
		}
		if config.OutputPath != nil {
			outputPath = *config.OutputPath
		}
		if config.OutputTemplate != nil {
			outputTemplate = *config.OutputTemplate
		}
		if config.OutputS3Bucket != nil {
			outputS3Bucket = *config.OutputS3Bucket
		}
		if config.Region != nil {
			region = *config.Region
		}
		if config.Profiles != nil {
			profiles = []string{} // erase any previous
			for _, x := range *config.Profiles {
				profiles = append(profiles, x)
			}
		}
		if config.Configs != nil {
			configs = make(map[string]string) // erase any previous
			for k, v := range *config.Configs {
				configs[k] = v
			}
		}
		if config.Backends != nil {
			for k, v := range config.Backends {
				backends[k] = v // copy
			}
		}
	}

	// Command line options override anything in the config.
	if c.IsSet("auto-config-uri") {
		autoConfigURI = c.String("auto-config-uri")
	}
	if c.IsSet("auto-config-cookie-path") {
		autoConfigCookiePath = c.String("auto-config-cookie-path")
	}
	if c.IsSet("auto-config-expiry-seconds") {
		autoConfigExpirySeconds = c.Int("auto-config-expiry-seconds")
	}
	if c.IsSet("auto-config-force-update") {
		autoConfigForceUpdate = c.Bool("auto-config-force-update")
	}
	if c.IsSet("quiet") {
		quiet = c.Bool("quiet")
	}
	if c.IsSet("ansi-magic") {
		ansiMagic = c.Bool("ansi-magic")
	}
	if c.IsSet("no-ansi-magic") {
		ansiMagic = !c.Bool("no-ansi-magic")
	}
	if c.IsSet("regexp-path") {
		regexpPath = c.String("regexp-path")
	}
	// config-path makes no sense here
	if c.IsSet("output-type") {
		outputType = c.String("output-type")
	}
	if c.IsSet("output-path") {
		outputPath = c.String("output-path")
	}
	if c.IsSet("output-template") {
		outputTemplate = c.String("output-template")
	}
	if c.IsSet("output-s3bucket") {
		outputS3Bucket = c.String("output-s3bucket")
	}
	if c.IsSet("region") {
		region = c.String("region")
	}
	if c.IsSet("profile") {
		profiles = []string{} // erase any previous
		for _, x := range c.StringSlice("profile") {
			profiles = append(profiles, x)
		}
	}
	//if c.IsSet("config") {
	//	configs = make(map[string]string) // erase any previous
	//	for k, x := range c.StringSlice("config") { // TODO: map not list
	//		configs[k] = v
	//	}
	//}

	// If no args or flags are specified, just show the help text.
	if c.NArg() == 0 && c.NumFlags() == 0 {
		return cli.ShowAppHelp(c)
	}

	logf := (&ansi.Logf{
		Prefix:   "main: ",
		Ellipsis: "...",
		Enable:   ansiMagic,
		Prefixes: []string{
			//"core: ",
			"backend: installed: ",
			"backend: running: ",
			"iterator: ",
			"core: scanner: scanning: ",
		},
	}).Init()
	logf("Hello from purpleidea! This is %s, version: %s", program, version)
	defer logf("Done!")

	if autoConfigForceUpdate && autoConfigURI == "" { // be helpful
		logf("unable to force auto-config update because auto-config-uri is empty")
	}

	isExpired := false
	if autoConfigForceUpdate {
		isExpired = true

	} else if autoConfigExpirySeconds == 0 {
		isExpired = true

	} else if autoConfigExpirySeconds < 0 {
		isExpired = false

	} else if autoConfigURI != "" {
		p, err := GetConfigPath(c.String("config-path"))
		if err != nil {
			return err
		}

		fileInfo, err := os.Stat(p)
		if os.IsNotExist(err) { // no config exists here...
			isExpired = true
		} else if err != nil {
			return err
		}

		if time.Since(fileInfo.ModTime()).Milliseconds() > int64(autoConfigExpirySeconds)*1000 {
			isExpired = true
		}
	}

	// did we just recurse to reload config?
	// we need to detect this because if we recursed, isExpiry will be false
	// when it's really an invalid reason for being false when config is new
	isRecurse := false
	pcSelf, _, _, ok0 := runtime.Caller(0)   // self
	pcCaller, _, _, ok1 := runtime.Caller(1) // caller
	details0 := runtime.FuncForPC(pcSelf)
	details1 := runtime.FuncForPC(pcCaller)
	if ok0 && details0 != nil && ok1 && details1 != nil && details0.Name() == details1.Name() {
		isRecurse = true
	}

	// auto config URI magic...
	var autoConfigError error
	if autoConfigURI != "" && (isExpired || isRecurse) { // we must try to auto config
		logf("getting config from: %s", autoConfigURI)
		data, err := DownloadConfig(autoConfigURI)
		if err != nil {
			return errwrap.Wrapf(err, "autoConfigURI download failed on: %s", autoConfigURI)
		}
		p, err := GetConfigPath(c.String("config-path"))
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) { // no config exists here...
			b = []byte{} // (implied, but now cleanly initialized)
			err = nil    // clear this
		} else if err != nil {
			return err
		}

		isJson := func(d []byte) error {
			buffer := bytes.NewBuffer(d)
			if buffer.Len() == 0 {
				return fmt.Errorf("empty config file")
			}
			decoder := json.NewDecoder(buffer)

			var configData Config // this gets populated during decode
			err := decoder.Decode(&configData)
			return errwrap.Wrapf(err, "invalid json")
		}

		// if equal, we don't need to change the config...
		// check it's valid json before writing it? (for portal errors)
		if err, equal := isJson(data), bytes.Equal(data, b); (!equal || isExpired) && err == nil {

			// store new config file (this also update the mtime!)
			logf("writing new config...")
			d := filepath.Dir(p)
			// maybe the ~/.config/yesiscan/ dir doesn't exist yet!
			if _, err := os.Stat(d); os.IsNotExist(err) { // no config exists here...
				if err := os.MkdirAll(d, interfaces.Umask); err != nil {
					return errwrap.Wrapf(err, "couldn't make config dir at: %s", d)
				}
			}

			// XXX: set umask for u=rw,go=
			if err := os.WriteFile(p, data, interfaces.Umask); err != nil {
				return errwrap.Wrapf(err, "autoConfigURI store failed on: %s", p)
			}

			// recurse!
			if !equal { // otherwise we'd infinitely loop!
				logf("recursing on new config...")
				return App(c, program, version, debug)
			}

		} else if err != nil {
			// provide logs so users know something is wrong...
			autoConfigError = err
			logf("invalid config file at URI: %s", autoConfigURI)
			logf("error with config file: %+v", err)
			if autoConfigCookiePath == "" {
				logf("do you need an auth cookie?")
			} else {
				logf("is your auth cookie (%s) valid?", autoConfigCookiePath)
			}
		}
	}

	// more auto config URI magic...
	recurse := false
	configKeys := []string{}
	// only run this if expired or recursing and no previous error...
	// if we had a previous error, the root config is invalid, stop trying!
	if (isExpired || isRecurse) && autoConfigError == nil {
		for k := range configs {
			configKeys = append(configKeys, k)
		}
	}
	sort.Strings(configKeys) // deterministic

	var absFile safepath.AbsFile
	if len(configKeys) > 0 {
		p, err := GetConfigPath(c.String("config-path"))
		if err != nil {
			return err
		}
		if absFile, err = safepath.ParseIntoAbsFile(p); err != nil {
			return err
		}
	}

	for _, k := range configKeys {
		// check that k is inside of the directory that p is in
		// this prevents us writing to /root or something unwanted

		safeAbsDir := absFile.Dir() // dir that k needs to be within

		h, err := homedir.Expand(k)
		if err != nil {
			return errwrap.Wrapf(err, "invalid path of: %s", k)
		}
		kAbsFile, err := safepath.ParseIntoAbsFile(h)
		if err != nil {
			return err
		}
		kSafeAbsDir := kAbsFile.Dir() // dir that k is in

		// eg: safeAbsDir is ~/.config/yesiscan/
		// eg: kSafeAbsDir is ~/.config/yesiscan/profiles/
		if !safepath.HasPrefix(kSafeAbsDir, safeAbsDir) {
			return fmt.Errorf("invalid config: can't download file to: %s", kSafeAbsDir)
		}

		v := configs[k] // key must exist

		logf("getting additional config from: %s", v)
		data, err := DownloadConfig(v)
		if err != nil {
			return errwrap.Wrapf(err, "autoConfigURI download failed on: %s", v)
		}

		b, err := os.ReadFile(h)
		if os.IsNotExist(err) { // no config exists here...
			b = []byte{} // (implied, but now cleanly initialized)
			err = nil    // clear this
		} else if err != nil {
			return err
		}

		// TODO: is this good enough to verify a json format w/o schema?
		isJson := func(d []byte) error {
			var j json.RawMessage
			return json.Unmarshal(d, &j)
		}

		// if equal, we don't need to change the config...
		// check it's valid json before writing it? (for portal errors)
		if err, equal := isJson(data), bytes.Equal(data, b); (!equal || isExpired) && err == nil {

			// store new config file (this also update the mtime!)
			logf("writing new additional config to: %s", h)
			d := filepath.Dir(h)
			// maybe the ~/.config/yesiscan/?/ dir doesn't exist yet!
			if _, err := os.Stat(d); os.IsNotExist(err) { // no config exists here...
				if err := os.MkdirAll(d, interfaces.Umask); err != nil {
					return errwrap.Wrapf(err, "couldn't make config dir at: %s", d)
				}
			}

			// XXX: set umask for u=rw,go=
			if err := os.WriteFile(h, data, interfaces.Umask); err != nil {
				return errwrap.Wrapf(err, "autoConfigURI store additional failed on: %s", k)
			}

			// recurse at the end...
			if !equal { // otherwise we'd infinitely loop!
				recurse = true
			}

		} else if err != nil {
			// provide logs so users know something is wrong...
			logf("invalid additional config file at URI: %s", v)
			logf("error with config file: %+v", err)
			if autoConfigCookiePath == "" {
				logf("do you need an auth cookie?")
			} else {
				logf("is your auth cookie (%s) valid?", autoConfigCookiePath)
			}
		}
	}
	if recurse {
		logf("recursing on new additional config...")
		return App(c, program, version, debug)
	}

	if outputPath == "-" || outputTemplate == "-" || quiet { // if output is stdout, noop logs
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

	for _, b := range lib.Backends {
		// if undefined, then look at the flags...
		if _, exists := backends[b]; !exists {
			backends[b] = isBackendEnabled(b)
			continue
		}
		// if the yes or no flag was set, then use that
		if c.Bool(fmt.Sprintf("no-backend-%s", b)) {
			backends[b] = false
		}
		if c.Bool(fmt.Sprintf("yes-backend-%s", b)) {
			backends[b] = true
		}
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
		logf("s3: setup verification...")
		if _, err := s3.Store(ctx, inputs); err != nil {
			logf("s3: are your s3 credentials valid?")
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

		Profiles: profiles,

		RegexpPath: regexpPath,
	}

	output, err := m.Run(ctx)
	if err != nil {
		return err
	}

	s := ""
	if outputPath != "" || outputTemplate != "" || outputS3Bucket != "" {
		var err error
		// TODO: when we render an html version, should
		// it look the same as the web `save` output?
		if outputType == "text" {
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
		if outputType == "text" {
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
		if err := os.WriteFile(outputPath, []byte(s), 0660); err != nil {
			logf("could not write output file: %+v", err)
		}
	} else if outputTemplate != "" {
		// TODO: should we block certain patterns like ".." or similar?
		replacements := map[string]interface{}{
			"..": "", // old -> new
			//"date": time.Now().Format(time.RFC3339), // colons upset xdg-open
			//"date": time.Now().Unix(), // works perfectly
			"date": strings.ReplaceAll(time.Now().Format(time.RFC3339), ":", "-"),
		}

		outputPath := util.NamedArgsTemplate(outputTemplate, replacements)

		// TODO: is this the umask we should use?
		// XXX: set umask for u=rw,go=
		if err := os.WriteFile(outputPath, []byte(s), 0660); err != nil {
			logf("could not write templated output file: %+v", err)
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
}

// NamedArgsTemplate takes a format string that contains named args wrapped in
// curly brackets, and templates them in. For example, "hello {name}!" will turn
// into "hello world!" if you pass a map with "name" => "world" into it.
func NamedArgsTemplate(format string, replacements map[string]interface{}) string {
	keys := []string{}
	for k := range replacements {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	args := []string{}
	for _, k := range keys {
		s1 := "{" + k + "}"
		args = append(args, s1)
		s2 := fmt.Sprint(replacements[k])
		args = append(args, s2)
	}

	return strings.NewReplacer(args...).Replace(format)
}

// Config is a list of settings stored in the users ~/.config/ directory.
// TODO: should this get moved into the lib package?
type Config struct {
	// AutoConfigURI is a special URI which if set, will try and pull a
	// config from that location on startup. It will use the cookie file
	// stored at AutoConfigCookiePath if specified. If successful, it will
	// check if the config is different from what is currently stored. If so
	// then it will validate if it is a valid json config. If so it will
	// replace (overwrite!) the current config and then recursively begin
	// the process again. The only thing preventing infinite recursion here
	// is the fact that you probably would not chain 100 configs, one after
	// another...
	AutoConfigURI *string `json:"auto-config-uri"`

	// AutoConfigCookiePath is a special URI which if set will point to a
	// netscape/libcurl style cookie file to use when making the get
	// download requests. This is useful if you store your config behind
	// some gateway that needs a magic cookie for auth.
	AutoConfigCookiePath *string `json:"auto-config-cookie-path"`

	// AutoConfigExpirySeconds is the minimum number of seconds to wait
	// before attempting to downloading a new auto-config if one is
	// available. If this is unset or zero, then this will always attempt a
	// download. If this is -1, then this will not ever attempt a download
	// unless force is used.
	AutoConfigExpirySeconds *int `json:"auto-config-expiry-seconds"`

	// AutoConfigForceUpdate will force an auto-config download if it is
	// possible to do so.
	AutoConfigForceUpdate *bool `json:"auto-config-force-update"`

	// Quiet will prevent the tool from talking too much on the console.
	// This is implied if you use the stdout option of --output-path.
	Quiet *bool `json:"quiet"`

	// AnsiMagic will do some ansi terminal escape sequence magic to keep
	// the console output cleaner if this is set.
	AnsiMagic *bool `json:"ansi-magic"`

	// RegexpPath specifies a path the regular expressions to use.
	RegexpPath *string `json:"regexp-path"`
	// config-path makes no sense here

	// OutputType is the format the report will be sent as. Options include
	// "html" and "text".
	OutputType *string `json:"output-type"`

	// OutputPath is the location where the report will be saved. This will
	// overwrite any existing file at this location. Use with caution. If
	// you specify the - character (dash) then it will print to stdout.
	OutputPath *string `json:"output-path"`

	// OutputTemplate is the location where the report will be saved. This
	// will overwrite any existing file at this location. Use with caution.
	// If you specify the - character (dash) then it will print to stdout.
	// This option is identical to the OutputPath option, except that it
	// accepts named format strings. Each named format string must be
	// surrounded by curly braces. Certain dangerous values will be stripped
	// from the output template, so don't try and be malicious or strange.
	// The list of valid format string names are as follows.
	// "date": Returns the RFC3339 date with colons changed to dashes.
	OutputTemplate *string `json:"output-template"`

	// OutputS3Bucket prints the report to an S3 bucket with this name. Make
	// sure you don't have anything important in the bucket as it might
	// overwrite any file in there as the report name is chosen
	// automatically.
	OutputS3Bucket *string `json:"output-s3bucket"`

	// Region specifies the S3 region to use when writing to the S3 bucket.
	Region *string `json:"region"`

	// Profiles is the list of profiles to use. Either the names from
	// ~/.config/yesiscan/profiles/<name>.json or full paths.
	Profiles *[]string `json:"profiles"`

	// Configs is the list of config additions to use. These files are
	// downloaded from the URI's (map values) and put into the corresponding
	// source (map keys).
	Configs *map[string]string `json:"configs"`

	// Backends gives us a list of backends we use. If the corresponding
	// bool value in the map is true, then the backend is enabled. If it is
	// false that it is not enabled. If it not listed then its behaviour is
	// undefined.
	Backends map[string]bool `json:"backends"`
}

// GetConfig loads the config file data into a struct.
func GetConfig(p string) (*Config, error) {

	configPath, err := GetConfigPath(p)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) && p == "" {
		return nil, nil // no config, no error
	}
	if err != nil {
		return nil, errwrap.Wrapf(err, "error reading config file")
	}

	buffer := bytes.NewBuffer(data)
	if buffer.Len() == 0 {
		return nil, fmt.Errorf("empty config file: %s", configPath)
	}
	decoder := json.NewDecoder(buffer)

	var configData Config // this gets populated during decode
	if err := decoder.Decode(&configData); err != nil {
		// TODO: should this be an error, or just a silent ignore?
		return nil, errwrap.Wrapf(err, "error decoding json output of: %s", configPath)
	}

	return &configData, nil
}

// GetConfigPath returns the expected path to the main config.json file given
// the input arg for that setting.
func GetConfigPath(configPath string) (string, error) {
	// If config path is set, we look in there for a config, otherwise we
	// use the default xdg path.
	if configPath != "" {
		return configPath, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", errwrap.Wrapf(err, "error finding home directory")
	}
	if home == "" {
		return "", fmt.Errorf("home directory is empty")
	}

	// TODO: get program from an input var perhaps?
	p := filepath.Join(home, ".config/", program+"/", ConfigFileName)
	return filepath.Clean(p), nil
}

// DownloadConfig pulls a config from a magic URI and returns the contents.
func DownloadConfig(uri string) ([]byte, error) {
	if uri == "" {
		return nil, fmt.Errorf("empty URI")
	}

	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	if u.Scheme == "https" {
		client := &http.Client{
			CheckRedirect: func() func(req *http.Request, via []*http.Request) error {
				redirects := 0
				return func(req *http.Request, via []*http.Request) error {
					if redirects > MaxRedirects {
						return fmt.Errorf("stopped after %d redirects", MaxRedirects)
					}
					redirects++
					return nil
				}
			}(),
		}
		if autoConfigCookiePath != "" {
			p, err := homedir.Expand(autoConfigCookiePath)
			if err != nil {
				return nil, errwrap.Wrapf(err, "invalid path of: %s", autoConfigCookiePath)
			}
			cookieJar, err := cookiejarparser.LoadCookieJarFile(p)
			if err != nil {
				return nil, errwrap.Wrapf(err, "error loading cookie from: %s", autoConfigCookiePath)
			}
			client.Jar = cookieJar
		}

		resp, err := client.Get(uri)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		return body, nil
	}

	return nil, fmt.Errorf("unsupported URI: %s", uri)
}

func main() {
	debug := false // TODO: hardcoded for now

	program = strings.TrimSpace(program)
	version = strings.TrimSpace(version)
	if program == "" || version == "" {
		// run `go generate` before you build it.
		fmt.Printf("program was not compiled correctly\n")
		os.Exit(1)
		return
	}

	// FIXME: We discard output from lib's that use `log` package directly.
	log.SetOutput(io.Discard)

	// TODO: put these args in an input struct
	if err := CLI(program, version, debug); err != nil {
		if debug {
			fmt.Printf("failed: %+v\n", err)
		} else {
			fmt.Printf("failed: %+v\n", errwrap.Cause(err))
		}
		os.Exit(1)
		return
	}
	os.Exit(0)
}
