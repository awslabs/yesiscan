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

package lib

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/awslabs/yesiscan/backend"
	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/parser"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
	"github.com/awslabs/yesiscan/util/safepath"
)

// Backends are a list of the available backends. We will eventually replace
// this with a registration mechanism.
var Backends = []string{
	"licenseclassifier",
	"cran",
	"pom",
	"spdx",
	"askalono",
	"scancode",
	"bitbake",
	"regexp",
}

// Main is the general entry point for running this software. Populate this
// struct with the inputs and then call the Run() method.
type Main struct {
	Program string
	Version string
	Debug   bool
	Logf    func(format string, v ...interface{})

	// This is the argv of the function.
	Args []string

	// Backends gives us a list of backends we use. If the corresponding
	// bool value in the map is true, then the backend is enabled. It can be
	// false if we want to show that it exists but is not enabled. This is
	// useful for display purposes.
	Backends map[string]bool

	// Profiles is the list of profiles to use. Either the names from
	// ~/.config/yesiscan/profiles/<name>.json or full paths.
	Profiles []string

	// RegexpPath specifies a path the regular expressions to use.
	RegexpPath string
}

// Run is the main method for the Main struct. We use a struct as a way to pass
// in a ton of different arguments in a cleaner way.
func (obj *Main) Run(ctx context.Context) (*Output, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(userCacheDir, interfaces.Umask); err != nil {
		return nil, err
	}
	prefix := filepath.Join(userCacheDir, obj.Program)
	if err := os.MkdirAll(prefix, interfaces.Umask); err != nil {
		return nil, err
	}
	safePrefixAbsDir, err := safepath.ParseIntoAbsDir(prefix)
	if err != nil {
		return nil, err
	}
	obj.Logf("prefix: %s", safePrefixAbsDir)

	home, err := os.UserHomeDir()
	if err != nil {
		obj.Logf("error finding home directory: %+v", err)
	}

	// TODO: add more --flags to specify which parser/iterators to use...

	inputStrings := []string{}

	for _, s := range obj.Args {
		if s == "-" { // stdin
			var err error
			s, err = stdinAsString(obj.Logf)
			if err != nil {
				return nil, err
			}
		}
		inputStrings = append(inputStrings, s)
	}
	if len(obj.Args) == 0 { // if we didn't get any args, assume stdin
		s, err := stdinAsString(obj.Logf)
		if err != nil {
			return nil, err
		}
		inputStrings = append(inputStrings, s)
	}

	iterators := []interfaces.Iterator{}
	for _, s := range inputStrings {
		trivialURIParser := &parser.TrivialURIParser{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf(format, v...)
			},
			Prefix: safePrefixAbsDir,
			Input:  s,
		}
		obj.Logf("input: %s", s)

		ixs, err := trivialURIParser.Parse() // parser returns iterators
		if err != nil {
			return nil, errwrap.Wrapf(err, "parser failed")
		}
		iterators = append(iterators, ixs...)
	}

	backends := []interfaces.Backend{}
	backendWeights := make(map[interfaces.Backend]float64)

	if enabled, _ := obj.Backends["licenseclassifier"]; enabled {
		licenseClassifierBackend := &backend.LicenseClassifier{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
			IncludeHeaders:       false,
			UseDefaultConfidence: false,
		}
		backends = append(backends, licenseClassifierBackend)
		backendWeights[licenseClassifierBackend] = 1.0 // TODO: adjust as needed
	}

	if enabled, _ := obj.Backends["cran"]; enabled {
		cranBackend := &backend.Cran{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
		}
		backends = append(backends, cranBackend)
		backendWeights[cranBackend] = 2.0 // TODO: adjust as needed
	}

	if enabled, _ := obj.Backends["pom"]; enabled {
		pomBackend := &backend.Pom{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
		}
		backends = append(backends, pomBackend)
		backendWeights[pomBackend] = 2.0 // TODO: adjust as needed
	}

	if enabled, _ := obj.Backends["spdx"]; enabled {
		spdxBackend := &backend.Spdx{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
		}
		backends = append(backends, spdxBackend)
		backendWeights[spdxBackend] = 2.0 // TODO: adjust as needed
	}

	if enabled, _ := obj.Backends["askalono"]; enabled {
		askalonoBackend := &backend.Askalono{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
			Prefix: safePrefixAbsDir,
		}
		backends = append(backends, askalonoBackend)
		backendWeights[askalonoBackend] = 4.0 // TODO: adjust as needed
	}

	if enabled, _ := obj.Backends["scancode"]; enabled {
		scancodeBackend := &backend.Scancode{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
		}
		backends = append(backends, scancodeBackend)
		backendWeights[scancodeBackend] = 8.0 // TODO: adjust as needed
	}

	if enabled, _ := obj.Backends["bitbake"]; enabled {
		bitbakeBackend := &backend.Bitbake{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf("backend: "+format, v...)
			},
		}
		backends = append(backends, bitbakeBackend)
		backendWeights[bitbakeBackend] = 16.0 // TODO: adjust as needed
	}

	regexpPath := ""
	if enabled, _ := obj.Backends["regexp"]; enabled {
		if obj.RegexpPath != "" {
			regexpPath = obj.RegexpPath
		} else {
			// TODO: implement proper XDG and maybe path precedence?
			if home != "" {
				regexpPath = filepath.Join(home, ".config/", obj.Program+"/", "regexp.json")
				regexpPath = filepath.Clean(regexpPath)
			}
		}
	}
	if regexpPath != "" {
		regexpBackend := &backend.Regexp{
			RegexpCore: &backend.RegexpCore{
				Debug: obj.Debug,
				Logf: func(format string, v ...interface{}) {
					obj.Logf("backend: "+format, v...)
				},
			},

			Filename: regexpPath,
		}
		backends = append(backends, regexpBackend)
		backendWeights[regexpBackend] = 8.0 // TODO: adjust as needed
	}

	//if enabled, _ := obj.Backends["example"]; enabled {
	//	exampleBackend := &backend.ExampleClassifier{
	//		Debug: obj.Debug,
	//		Logf: func(format string, v ...interface{}) {
	//			obj.Logf("backend: "+format, v...)
	//		},
	//	}
	//	backends = append(backends, exampleBackend)
	//	backendWeights[exampleBackend] = 99.0 // TODO: adjust as needed
	//}

	// load the profiles earlier than needed to catch json typos and commas
	profilesData := make(map[string]*ProfileData)
	profilesData[DefaultProfileName] = nil // add a "default" profile for fun
	// TODO: implement proper XDG and maybe path precedence?
	for _, x := range obj.Profiles {
		var err error
		data := []byte{}
		if home != "" {
			p := fmt.Sprintf("%s.json", x) // TODO: validate input string?
			profilePath := filepath.Join(home, ".config/", obj.Program+"/profiles/", p)
			profilePath = filepath.Clean(profilePath)
			data, err = os.ReadFile(profilePath)
			// check errors below...
		}
		if os.IsNotExist(err) || home == "" {
			data, err = os.ReadFile(x)
		}

		if err != nil {
			obj.Logf("profile %s: %s", x, err)
			err = nil // reset
			continue
		}

		buffer := bytes.NewBuffer(data)
		if buffer.Len() == 0 {
			// TODO: should this be an error, or just a silent ignore?
			obj.Logf("profile %s: empty input file", x)
			continue
		}
		decoder := json.NewDecoder(buffer)

		var profileConfig ProfileConfig // this gets populated during decode
		if err := decoder.Decode(&profileConfig); err != nil {
			// TODO: should this be an error, or just a silent ignore?
			obj.Logf("profile %s: error decoding json output: %+v", err)
			continue
		}

		list, err := licenses.StringsToLicenses(profileConfig.Licenses)
		if err != nil {
			obj.Logf("profile %s: error parsing license: %+v", err)
			continue
		}

		profilesData[x] = &ProfileData{
			Licenses: list,
			Exclude:  profileConfig.Exclude,
		}
	}

	core := &Core{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf("core: "+format, v...)
		},
		Backends:  backends,
		Iterators: iterators, // TODO: should this be passed into Run instead?
		// XXX: deprecate this because we have IteratorError now...
		ShutdownOnError: false, // set to true for "perfect" scanning.
	}

	if err := core.Init(ctx); err != nil {
		return nil, errwrap.Wrapf(err, "could not initialize core")
	}

	results, passes, warnings, err := core.Run(ctx)
	if err != nil {
		return nil, errwrap.Wrapf(err, "core run failed")
	}

	// remove all the invalid/missing profiles, keep in the original order
	profiles := []string{}
	for _, x := range obj.Profiles {
		if _, exists := profilesData[x]; exists {
			profiles = append(profiles, x)
		}
	}
	if len(profiles) == 0 {
		// add a default profile
		profiles = append(profiles, DefaultProfileName)
	}

	return &Output{
		Program:        obj.Program,
		Version:        obj.Version,
		Args:           inputStrings,
		Backends:       obj.Backends,
		Results:        results,
		Passes:         passes,
		Warnings:       warnings,
		Profiles:       profiles,
		ProfilesData:   profilesData,
		BackendWeights: backendWeights,
	}, nil
}

// Output combines all of the returned data from Run() into a consistent form.
type Output struct {
	Program string
	Version string

	// TODO: we could build and return a UID here instead of doing it in
	// web and separately generating a time UID for --output-template.
	Args           []string
	Backends       map[string]bool
	Results        map[string]map[interfaces.Backend]*interfaces.Result
	Passes         []string
	Warnings       map[string]error
	Profiles       []string
	ProfilesData   map[string]*ProfileData
	BackendWeights map[interfaces.Backend]float64
}

// ReturnOutputConsole returns a string of output, formatted for the console.
func ReturnOutputConsole(output *Output) (string, error) {
	s := ""
	summary := true // TODO: perhaps configure this somewhere or as a flag?
	for _, x := range output.Profiles {
		pro, err := SimpleProfiles(output.Results, output.Passes, output.Warnings, output.ProfilesData[x], summary, output.BackendWeights, "ansi")
		if err != nil {
			return "", err
		}

		s += fmt.Sprintf("profile %s:\n%s\n", x, pro)
	}

	return s, nil
}

// ReturnOutputFile returns a string of output, formatted for a text file.
func ReturnOutputFile(output *Output) (string, error) {
	s := ""
	summary := true // TODO: perhaps configure this somewhere or as a flag?
	for _, x := range output.Profiles {
		pro, err := SimpleProfiles(output.Results, output.Passes, output.Warnings, output.ProfilesData[x], summary, output.BackendWeights, "text")
		if err != nil {
			return "", err
		}

		s += fmt.Sprintf("profile %s:\n%s\n", x, pro)
	}

	return s, nil
}

func stdinAsString(logf func(format string, v ...interface{})) (string, error) {
	logf("waiting for stdin...")
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
