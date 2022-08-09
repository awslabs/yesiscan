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

// TODO: should this be a subpackage?
package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
)

// Regexp is a simple backend that uses regular expressions to find certain
// license strings. It wraps the RegexpCore backend and adds the file input
// code.
type Regexp struct {
	*RegexpCore

	// Filename is an absolute path to a file that we will read the patterns
	// from. The struct is described below and an example is available in
	// the examples folder.
	Filename string

	// FileNamePattern is a string which will contain a specific file 
	// pattern and only scan files matching this specific pattern.
	FileNamePattern string
}

func (obj *Regexp) String() string {
	return obj.RegexpCore.String()
}

func (obj *Regexp) Setup(ctx context.Context) error {
	b, err := os.ReadFile(obj.Filename)
	if err != nil {
		// TODO: this error message is CLI specific, but should be generalized
		obj.Logf("either run with --no-backend-regexp or create your regexp pattern file at %s", obj.Filename)
		return errwrap.Wrapf(err, "could not read config file: %s", obj.Filename)
	}

	buffer := bytes.NewBuffer(b)
	if buffer.Len() == 0 {
		// TODO: should this be an error, or just a silent ignore?
		return fmt.Errorf("empty input file")
	}
	decoder := json.NewDecoder(buffer)

	var regexpConfig RegexpConfig // this gets populated during decode
	if err := decoder.Decode(&regexpConfig); err != nil {
		return errwrap.Wrapf(err, "error decoding regexp json output")
	}

	obj.RegexpCore.Rules = regexpConfig.Rules
	obj.RegexpCore.Origin = regexpConfig.Origin

	return obj.RegexpCore.Setup(ctx)
}

func (obj *Regexp) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	match, err := regexp.MatchString(obj.FileNamePattern, info.FileInfo.Name())
	if err != nil {
		return nil, errwrap.Wrapf(err, "Incorrect file pattern")
	}

	if match == false {
		return nil, nil // skip
	}
	return obj.RegexpCore.ScanData(ctx, data, info)
}

// RegexpConfig is the structure of the pattern config file.
type RegexpConfig struct {
	// Rules is the list of regexp and license id rules.
	Rules []*RegexpLicenseRule `json:"rules"`

	// Origin is the SPDX origin string if we want to have a custom
	// namespace for non-SPDX license ID's.
	Origin string `json:"origin"`

	// Comment adds a user friendly comment for this file. We could use it
	// to add a version string or maybe that could be a separate field.
	Comment string `json:"comment"`
}
