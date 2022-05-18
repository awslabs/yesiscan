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
	// ScancodeProgram is the name of the scancode executable.
	ScancodeProgram = "scancode"
)

// Scancode is based on the python scancode project. It uses their heuristic to
// identify licenses and other things. It would probably be pretty easy to just
// take the core license identification heuristic and implement it in pure
// golang and then use it that way. At the moment, this is not as efficient as
// it could be because we spawn many slow separate python processes to scan.
// Please note that the project spells it ScanCode, but here we use Scancode.
type Scancode struct {
	Debug bool
	Logf  func(format string, v ...interface{})

	// SkipZeroResults tells this backend to avoid erroring when we aren't
	// able to determine if a file matches a known license. Since this
	// particular backend is not good at general file identification, and
	// only good at being presented with actual licenses, this is useful if
	// file filtering is not enabled.
	SkipZeroResults bool
}

func (obj *Scancode) String() string {
	return "scancode"
}

func (obj *Scancode) Validate(ctx context.Context) error {
	// This runs --help the first time to warm up scancode and finish the
	// setup in case it wasn't done previously. This is a silly way for it
	// to be built, but we'll go with it for now. This also checks that it
	// is in the path.

	args := []string{"--help"}

	obj.Logf("running: %s %s", ScancodeProgram, strings.Join(args, " "))

	// TODO: do we need to do the ^C handling?
	// XXX: is the ^C context cancellation propagating into this correctly?
	cmd := exec.CommandContext(ctx, ScancodeProgram, args...)
	cmd.Dir = ""
	//cmd.Env = []string{} // XXX: don't nuke python, filter eventually
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}
	return nil
}

func (obj *Scancode) ScanPath(ctx context.Context, path safepath.Path, info *interfaces.Info) (*interfaces.Result, error) {

	// TODO: eventually we can have scancode operate on whole dirs
	if info.FileInfo.IsDir() { // path.IsDir() should be the same.
		return nil, nil // skip
	}

	filename := path.Path()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// TODO: --processes $NUM_CPUS
	args := []string{"--license", "--copyright", "--full-root", "--json-pp", "-", filename}

	prog := fmt.Sprintf("%s %s", ScancodeProgram, strings.Join(args, " "))

	obj.Logf("running: %s", prog)

	// TODO: do we need to do the ^C handling?
	// XXX: is the ^C context cancellation propagating into this correctly?
	cmd := exec.CommandContext(ctx, ScancodeProgram, args...)

	cmd.Dir = ""
	//cmd.Env = []string{} // XXX: don't nuke python, filter eventually

	// ignore signals sent to parent process (we're in our own group)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	out, err := cmd.Output()
	if err != nil {
		return nil, errwrap.Wrapf(err, "error running: %s", prog)
	}

	buffer := bytes.NewBuffer(out)
	decoder := json.NewDecoder(buffer)

	var scancodeOutput ScancodeOutput // this gets populated during decode
	if err := decoder.Decode(&scancodeOutput); err != nil {
		panic(fmt.Sprintf("error decoding scancode output: %+v", err))
	}

	if len(scancodeOutput.Files) == 0 {
		// we should still see a file here but with no analysis if there
		// is no license found, even partially
		// programming error (probably in scancode)
		return nil, fmt.Errorf("scancode did not return info on: %s", filename)
	}

	var fileResult *ScancodeFileResult
	for _, x := range scancodeOutput.Files {

		// TODO: is this how this works?
		if errs := x.ScanErrors; len(errs) > 0 {
			for i, e := range errs {
				obj.Logf("scancode error at path: %s", filename)
				obj.Logf("scancode error(%d): %s", i, e)
			}
			return nil, fmt.Errorf("scancode got multiple errors")
		}

		if x.Type != "file" {
			obj.Logf("scancode got type %s at path: %s", x.Type, filename)
			// TODO: match other types?
			continue
		}

		if x.Path != filename {
			obj.Logf("scancode got unexpected file: %s", filename)
			continue
		}

		if fileResult != nil {
			obj.Logf("got path: %s", x.Path)
			obj.Logf("got path: %s", fileResult.Path)
			return nil, fmt.Errorf("scancode got multiple files")
		}
		fileResult = x // found
	}

	// analysis didn't discover anything
	if len(fileResult.Licenses) == 0 {
		if obj.SkipZeroResults {
			return nil, nil
		}
		return nil, interfaces.ErrUnknownLicense
	}

	result, err := scancodeLicensesHelper(fileResult.Licenses)
	if err != nil {
		return nil, err
	}

	return deduplicateResult(result)
}

// ScancodeOutput is modelled after the scancode output format.
//
// example:
//{
//  "headers": [
//    {
//      "tool_name": "scancode-toolkit",
//      "tool_version": "30.1.0",
//      "options": {
//        "input": [
//          "/home/ANT.AMAZON.COM/purple/code/yesiscan/COPYING"
//        ],
//        "--copyright": true,
//        "--full-root": true,
//        "--json-pp": "-",
//        "--license": true,
//        "--only-findings": true,
//        "--summary-with-details": true
//      },
//      "notice": "Generated with ScanCode and provided on an \"AS IS\" BASIS, WITHOUT WARRANTIES\nOR CONDITIONS OF ANY KIND, either express or implied. No content created from\nScanCode should be considered or used as legal advice. Consult an Attorney\nfor any legal advice.\nScanCode is a free software code scanning tool from nexB Inc. and others.\nVisit https://github.com/nexB/scancode-toolkit/ for support and download.",
//      "start_timestamp": "2022-05-16T173951.395171",
//      "end_timestamp": "2022-05-16T173953.393255",
//      "output_format_version": "1.0.0",
//      "duration": 1.9980971813201904,
//      "message": null,
//      "errors": [],
//      "extra_data": {
//        "spdx_license_list_version": "3.14",
//        "OUTDATED": "WARNING: Outdated ScanCode Toolkit version! You are using an outdated version of ScanCode Toolkit: 30.1.0 released on: 2021-09-24. A new version is available with important improvements including bug and security fixes, updated license, copyright and package detection, and improved scanning accuracy. Please download and install the latest version of ScanCode. Visit https://github.com/nexB/scancode-toolkit/releases for details.",
//        "files_count": 1
//      }
//    }
//  ],
//  "summary": {
//    "license_expressions": [
//      {
//        "value": "apache-2.0",
//        "count": 1
//      }
//    ],
//    "copyrights": [
//      {
//        "value": null,
//        "count": 1
//      }
//    ],
//    "holders": [
//      {
//        "value": null,
//        "count": 1
//      }
//    ],
//    "authors": [
//      {
//        "value": null,
//        "count": 1
//      }
//    ]
//  },
//  "files": [
//    {
//      "path": "/home/ANT.AMAZON.COM/purple/code/yesiscan/COPYING",
//      "type": "file",
//      "licenses": [
//        {
//          "key": "apache-2.0",
//          "score": 100,
//          "name": "Apache License 2.0",
//          "short_name": "Apache 2.0",
//          "category": "Permissive",
//          "is_exception": false,
//          "is_unknown": false,
//          "owner": "Apache Software Foundation",
//          "homepage_url": "http://www.apache.org/licenses/",
//          "text_url": "http://www.apache.org/licenses/LICENSE-2.0",
//          "reference_url": "https://scancode-licensedb.aboutcode.org/apache-2.0",
//          "scancode_text_url": "https://github.com/nexB/scancode-toolkit/tree/develop/src/licensedcode/data/licenses/apache-2.0.LICENSE",
//          "scancode_data_url": "https://github.com/nexB/scancode-toolkit/tree/develop/src/licensedcode/data/licenses/apache-2.0.yml",
//          "spdx_license_key": "Apache-2.0",
//          "spdx_url": "https://spdx.org/licenses/Apache-2.0",
//          "start_line": 2,
//          "end_line": 202,
//          "matched_rule": {
//            "identifier": "apache-2.0.LICENSE",
//            "license_expression": "apache-2.0",
//            "licenses": [
//              "apache-2.0"
//            ],
//            "referenced_filenames": [],
//            "is_license_text": true,
//            "is_license_notice": false,
//            "is_license_reference": false,
//            "is_license_tag": false,
//            "is_license_intro": false,
//            "has_unknown": false,
//            "matcher": "1-hash",
//            "rule_length": 1581,
//            "matched_length": 1581,
//            "match_coverage": 100,
//            "rule_relevance": 100
//          }
//        }
//      ],
//      "license_expressions": [
//        "apache-2.0"
//      ],
//      "percentage_of_license_text": 100,
//      "copyrights": [],
//      "holders": [],
//      "authors": [],
//      "summary": {
//        "license_expressions": [
//          {
//            "value": "apache-2.0",
//            "count": 1
//          }
//        ],
//        "copyrights": [
//          {
//            "value": null,
//            "count": 1
//          }
//        ],
//        "holders": [
//          {
//            "value": null,
//            "count": 1
//          }
//        ],
//        "authors": [
//          {
//            "value": null,
//            "count": 1
//          }
//        ]
//      },
//      "scan_errors": []
//    }
//  ]
//}
type ScancodeOutput struct {
	// Headers are some output about scancode itself mostly.
	Headers interface{} `json:"headers"`

	// Summary is a top-level overview. We can build something similar
	// ourselves, so we don't need this.
	Summary interface{} `json:"summary"`

	// Files is an absolute file path to the file being scanned.
	Files []*ScancodeFileResult `json:"files"`
}

// ScancodeFileResult is the struct returned for each scanned file.
type ScancodeFileResult struct {
	// Path is the absolute path of the file scanned. It's only absolute if
	// we run scancode with the --full-root arg which we do.
	Path string `json:"path"`

	// Type is the type of file scanned. The most common string result is
	// "file".
	Type string `json:"type"`

	// Licenses is the list of licenses found.
	Licenses []*ScancodeLicenseResult `json:"licenses"`

	// LicenseExpressions is the list of licenses found. I think these are
	// all SPDX ID's, or rather the scancode version of this.
	LicenseExpressions []string `json:"license_expressions"`

	// PercentageOfLicenseText is some sort of a scoring result. It's not
	// clear if it's a measure of "what percentage of this file contains
	// this license?" vs. "how accurate is the match to this license?". In
	// any case, remember to divide by 100 if you want the more useful ratio
	// value.
	PercentageOfLicenseText float64 `json:"percentage_of_license_text"`

	// Copyrights is unused here at this time.
	Copyrights []interface{} `json:"copyrights"`

	// Holders is unused here at this time.
	Holders []interface{} `json:"holders"`

	// Authors is unused here at this time.
	Authors []interface{} `json:"authors"`

	// Summary is unused here at this time.
	Summary interface{} `json:"summary"`

	// ScanErrors is unused here at this time.
	// XXX: maybe we should check this?
	ScanErrors []interface{} `json:"scan_errors"`
}

// ScancodeLicenseResult is the struct returned for each license entry in the
// ScancodeFileResult.
type ScancodeLicenseResult struct {

	// Key is the SPDX ID's, or rather the scancode version of this I think.
	Key string `json:"key"`

	// Score is the confidence interval of the match I think. It is a
	// percentage as well, so divide by 100 for the more useful ratio.
	Score float64 `json:"score"`

	// Name is the long name of the license, eg: "Apache License 2.0".
	Name string `json:"name"`

	// ShortName is the short name of the license, eg: "Apache 2.0".
	ShortName string `json:"short_name"`

	// Category is the license category, eg: "Permissive". We don't use this
	// classification in this project.
	Category string `json:"category"`

	// IsException needs to be defined here. TODO: what is this?
	IsException bool `json:"is_exception"`

	// IsUnknown needs to be defined here. TODO: what is this?
	IsUnknown bool `json:"is_unknown"`

	// Owner is the author of the license. Eg: "Apache Software Foundation".
	// TODO: Is this correct?
	Owner string `json:"owner"`

	// HomepageUrl is the home of the license.
	HomepageUrl string `json:"homepage_url"`

	// TextUrl is the location where you can find the license.
	TextUrl string `json:"text_url"`

	// ReferenceUrl is the reference link in the aboutcode.org database.
	// TODO: Is this correct?
	ReferenceUrl string `json:"reference_url"`

	// ScancodeTextUrl is the location of the scancode text for this
	// license. Eg: https://github.com/nexB/scancode-toolkit/tree/develop/src/licensedcode/data/licenses/apache-2.0.LICENSE
	ScancodeTextUrl string `json:"scancode_text_url"`

	// ScancodeDataUrl is the location of the scancode data for this
	// license. Eg: https://github.com/nexB/scancode-toolkit/tree/develop/src/licensedcode/data/licenses/apache-2.0.yml
	ScancodeDataUrl string `json:"scancode_data_url"`

	// SpdxLicenseKey is the SPDX ID of this license. Eg: "Apache-2.0".
	SpdxLicenseKey string `json:"spdx_license_key"`

	// SpdxUrl is the location of the SPDX page for this license. Eg:
	// https://spdx.org/licenses/Apache-2.0
	SpdxUrl string `json:"spdx_url"`

	// StartLine is the line number for the start of the license match.
	StartLine int64 `json:"start_line"`

	// EndLine is the line number for the end of the license match.
	EndLine int64 `json:"end_line"`

	// MatchedRule is a big struct describing how this match was done. This
	// is currently not being used here.
	MatchedRule interface{} `json:"matched_rule"`
}

func scancodeLicensesHelper(input []*ScancodeLicenseResult) (*interfaces.Result, error) {
	// this should get called with at least one license
	if len(input) == 0 {
		return nil, fmt.Errorf("got empty result")
	}

	confidence := float64(1.0)
	output := []*licenses.License{}
	for _, x := range input {
		result, err := scancodeLicenseHelper(x)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, fmt.Errorf("unexpected nil result")
		}
		if d := len(result.Licenses); d != 1 {
			return nil, fmt.Errorf("expected 1 license, got: %d", d)
		}
		// TODO: do we need to epsilon compare?
		if result.Confidence == 0.0 {
			// maybe this is a programming error somewhere as this
			// would set all of them to zero
			return nil, fmt.Errorf("got zero confidence")
		}
		l := result.Licenses[0]
		output = append(output, l)
		// XXX: since we occasionally remove duplicates, is this bad for
		// the math?
		confidence = confidence * result.Confidence
	}

	return &interfaces.Result{
		Licenses:   output,
		Confidence: confidence,
	}, nil
}

func scancodeLicenseHelper(input *ScancodeLicenseResult) (*interfaces.Result, error) {
	if input == nil {
		return nil, fmt.Errorf("got nil license")
	}

	name := input.Key
	if s := input.SpdxLicenseKey; s != "" {
		name = s
	}

	license := &licenses.License{
		SPDX: name,
		// TODO: populate other fields here (eg: found license text)
	}
	// FIXME: If license is not in SPDX, add a custom entry.
	if err := license.Validate(); err != nil {
		//return nil, err
		license = &licenses.License{
			//SPDX: "",
			Origin: "scancode-toolkit.nexB.github.com",
			Custom: name,
			// TODO: populate other fields here (eg: found license text)
		}
	}
	return &interfaces.Result{
		Licenses: []*licenses.License{
			license,
		},
		Confidence: input.Score / 100,
	}, nil
}

func deduplicateResult(input *interfaces.Result) (*interfaces.Result, error) {
	if input == nil {
		// TODO: should we just pass through instead?
		return nil, fmt.Errorf("nil input")
	}

	output := []*licenses.License{}
	for _, x := range input.Licenses {
		if licenses.InList(x, output) { // duplicate license!
			continue
		}
		output = append(output, x)
	}

	return &interfaces.Result{
		Licenses:   output,
		Confidence: input.Confidence,
	}, nil
}
