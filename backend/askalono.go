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
	"os/exec"
	"strings"
	"syscall"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
	"github.com/awslabs/yesiscan/util/safepath"
)

const (
	// AskalonoProgram is the name of the askalono executable.
	AskalonoProgram = "askalono"

	// AskalonoConfidenceError is the error string askalono returns for when
	// it doesn't have high enough confidence in a file.
	AskalonoConfidenceError = "Confidence threshold not high enough for any known license"
)

// Askalono is based on the rust askalono project. It uses the Sørensen–Dice
// coefficient for license comparison. It would be pretty easy, and preferable
// to use one of the many pre-existing golang Sørensen–Dice implementations and
// to have a pure golang solution for this, however it would be good to have at
// least one backend that exec's out to a remote process, and since this one is
// fairly self-contained, it is a good example to use before we try and wrap
// something more complicated like scancode.
// See: https://en.wikipedia.org/wiki/S%C3%B8rensen%E2%80%93Dice_coefficient
type Askalono struct {
	Debug bool
	Logf  func(format string, v ...interface{})

	// SkipZeroResults tells this backend to avoid erroring when we aren't
	// able to determine if a file matches a known license. Since this
	// particular backend is not good at general file identification, and
	// only good at being presented with actual licenses, this is useful if
	// file filtering is not enabled.
	SkipZeroResults bool
}

func (obj *Askalono) String() string {
	return "askalono"
}

func (obj *Askalono) Setup(ctx context.Context) error {
	// This runs --help to check this is in the path and running properly.

	args := []string{"--help"}

	obj.Logf("running: %s %s", AskalonoProgram, strings.Join(args, " "))

	// TODO: do we need to do the ^C handling?
	// XXX: is the ^C context cancellation propagating into this correctly?
	cmd := exec.CommandContext(ctx, AskalonoProgram, args...)
	cmd.Dir = ""
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.Error); ok && e.Err == exec.ErrNotFound {
			// TODO: this error message is CLI specific, but should be generalized
			obj.Logf("either run with --no-backend-askalono or install askalono into your $PATH")
		}
		return errwrap.Wrapf(err, "error running: %s", AskalonoProgram)
	}

	return nil
}

func (obj *Askalono) ScanPath(ctx context.Context, path safepath.Path, info *interfaces.Info) (*interfaces.Result, error) {

	if info.FileInfo.IsDir() { // path.IsDir() should be the same.
		return nil, nil // skip
	}

	filename := path.Path()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// yes the args need to go in this order, nothing else works...
	args := []string{"--format", "json", "identify", "--optimize", filename}

	prog := fmt.Sprintf("%s %s", AskalonoProgram, strings.Join(args, " "))

	// TODO: add a progress bar of some sort somewhere
	if obj.Debug {
		obj.Logf("running: %s", prog)
	}

	// TODO: do we need to do the ^C handling?
	// XXX: is the ^C context cancellation propagating into this correctly?
	cmd := exec.CommandContext(ctx, AskalonoProgram, args...)

	cmd.Dir = ""
	cmd.Env = []string{}

	// ignore signals sent to parent process (we're in our own group)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	out, reterr := cmd.Output()
	if reterr != nil {
		if obj.Debug {
			obj.Logf("error running: %s", prog)
		}
		// XXX: bug: https://github.com/jpeddicord/askalono/issues/74
		// don't error here because it might be askalono erroring but
		// still returning output as an error message... it should not
		// have been written this way, but askalono team probably won't
		// change things now.
		//return nil, errwrap.Wrapf(reterr, "error running: %s", prog)
	}

	buffer := bytes.NewBuffer(out)
	if buffer.Len() == 0 {
		// XXX: bug: https://github.com/jpeddicord/askalono/issues/74
		obj.Logf("askalono EOF bug, skipped: %s", filename)
		return nil, nil // skip, unfortunately
	}
	decoder := json.NewDecoder(buffer)

	var askalonoOutput AskalonoOutput // this gets populated during decode
	if err := decoder.Decode(&askalonoOutput); err != nil {
		// programming error, report this to us please
		return nil, errwrap.Wrapf(err, "error decoding askalono json output")
	}

	if askalonoOutput.Path != "" && askalonoOutput.Path != filename {
		// programming error (probably in askalono)
		if obj.Debug {
			obj.Logf("expected: %s", filename)
			obj.Logf("got path: %s", askalonoOutput.Path)
		}
		return nil, fmt.Errorf("path did not match what was expected")
	}

	if reterr != nil && askalonoOutput.Error == "" {
		// probably a bug in askalono
		return nil, errwrap.Wrapf(reterr, "askalono bug, error running: %s", prog)
	}

	if reterr != nil && askalonoOutput.Error == AskalonoConfidenceError {
		return nil, nil // skip
	}

	if e := askalonoOutput.Error; reterr != nil && e != "" {
		return nil, fmt.Errorf("unhandled askalono error: %s", e)
	}

	if askalonoOutput.Path != filename || askalonoOutput.Result == nil {
		// XXX: error or is SkipZeroResults what we want here?
		if obj.SkipZeroResults {
			return nil, nil
		}
		return nil, interfaces.ErrUnknownLicense
	}

	return askalonoResultHelper(askalonoOutput.Result)
}

// AskalonoOutput is modelled after the askalono output format.
//
// example:
//{
//	"path": "/home/ANT.AMAZON.COM/purple/code/license-finder-repo/spdx.go",
//	"result": {
//		"score": 0.9310345,
//		"license": {
//			"name": "MIT",
//			"kind":"original",
//			"aliases": []
//		},
//		"containing": [
//			{
//				"score":0.993865,
//				"license": {
//					"name":"MIT",
//					"kind":"original",
//					"aliases": []
//				},
//				"line_range":[17,26]
//			}
//		]
//	}
//}
type AskalonoOutput struct {
	// Path is an absolute file path to the file being scanned.
	Path string `json:"path"`

	// Result specifies what it found.
	Result *AskalonoResultContaining `json:"result"`

	// Error is a string returned instead of Result on askalono error.
	Error string
}

// AskalonoResult is the generic result format returned by askalono. It is
// usually augmented by an additional field. That can be found in
// AskalonoResultRanged or AskalonoResultContaining.
type AskalonoResult struct {
	// Score is the matching score found. A 1.00 is a perfect match.
	Score float64 `json:"score"`

	// License points to the license information attached with this find.
	License *AskalonoLicense `json:"license"`
}

// AskalonoResultRanged is a version of the AskalonoResult that also contains
// the line range information.
type AskalonoResultRanged struct {
	*AskalonoResult

	// LineRangeRaw specifies where the match was found.
	LineRangeRaw []int64 `json:"line_range"`

	// TODO: add LineRangeStart and LineRangeEnd and Unmarshall into there!
}

// AskalonoResultContaining is a version of the AskalonoResult that also
// contains a list of additional AskalonoResultRanged matches.
type AskalonoResultContaining struct {
	*AskalonoResult

	// Containing has some further information about the output. It isn't
	// always populated, and I think it is only used when --optimize is used
	// *and* it didn't find an exact match. It lists all the other matches
	// it found.
	Containing []*AskalonoResultRanged `json:"containing"`
}

// AskalonoLicense is the format of the license struct returned by askalono.
type AskalonoLicense struct {
	// Name is the SPDX name of the license found.
	Name string `json:"name"`

	// Kind is some sort of license tag. So far I've found "original".
	Kind string `json:"kind"`

	// Aliases is probably aliases for this license. I've not found this
	// output anywhere atm, so I've left it as an interface.
	Aliases []interface{} `json:"aliases"`
}

func askalonoResultHelper(result *AskalonoResultContaining) (*interfaces.Result, error) {
	if result == nil {
		return nil, fmt.Errorf("got nil result")
	}

	if result.AskalonoResult != nil && result.AskalonoResult.License != nil {
		return askalonoLicenseHelper(result.AskalonoResult.License, result.Score)
	}

	if len(result.Containing) == 0 {
		// programming error (probably in askalono)
		return nil, fmt.Errorf("got nil license")
	}

	// TODO: add file content ranges
	// XXX: askalono can't currently find more than one license at a time,
	// so we don't handle that more complicated case for now. More info:
	// https://github.com/jpeddicord/askalono/issues/40
	r := result.Containing[0].AskalonoResult
	return askalonoLicenseHelper(r.License, r.Score)
}

func askalonoLicenseHelper(input *AskalonoLicense, confidence float64) (*interfaces.Result, error) {
	if input == nil {
		return nil, fmt.Errorf("got nil license")
	}

	license := &licenses.License{
		SPDX: input.Name,
		// TODO: populate other fields here (eg: found license text)
	}
	// FIXME: If license is not in SPDX, add a custom entry.
	if err := license.Validate(); err != nil {
		//return nil, err
		license = &licenses.License{
			//SPDX: "",
			Origin: "askalono.jpeddicord.github.com",
			Custom: input.Name,
			// TODO: populate other fields here (eg: found license text)
		}
	}
	return &interfaces.Result{
		Licenses: []*licenses.License{
			license,
		},
		Confidence: confidence,
	}, nil
}
