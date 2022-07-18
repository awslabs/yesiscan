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
	"bufio"
	"bytes"
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
)

const (
	// CranMaxBytesLine sets a larger maximum for file line scanning than
	// the default of bufio.MaxScanTokenSize which is sort of small.
	CranMaxBytesLine = 1024 * 1024 * 8 // 8 MiB

	// CranLicensePrefix is the string we look for when trying to find a
	// license.
	CranLicensePrefix = "License:"

	// CranFilename is the file name used by the R metadata files.
	CranFilename = "DESCRIPTION"
)

var (
	// ErrInvalidLicenseFormat is an error used in the CranDescriptionFileParser
	// when licenses with invalid format are found.
	ErrInvalidLicenseFormat error = errors.New("Invalid format in License(s)")

	// replaceFilesInLicenseString is used to replace all strings which include file
	// and a file name and sometimes have a + or | before it. For example: "| file
	// LICENSE".
	// source: https://cran.rstudio.com/doc/manuals/r-devel/R-exts.html#Licensing
	replaceFilesInLicenseString = regexp.MustCompile(`(?m)([+,|]?([ ])*)file([ ])+\w+\b([ ])*`)

	// licenseFormatChecker checks if a specific string does not contain letters.
	// If the string does not contain letters then it is definitely not valid to be
	// considered a license.
	licenseFormatChecker = regexp.MustCompile(`\B[^a-zA-Z]+\B`)

	// replaceNextLineCharacters is used to replace all characters which separated
	// the license input to another line.
	replaceNextLineCharacters = regexp.MustCompile(`\r?\n|\r`)

	// fieldFinder is used to identify fields in the DESCRIPTION file. An example
	// of a field is "License:".
	fieldFinder = regexp.MustCompile(`(?m)^\b[A-Z].*?\b:`)
)

// Cran is a backend for DESCRIPTION files which store R package
// metadata. We are getting the license names from the License field in the text
// file.
type Cran struct {
	Debug bool
	Logf  func(format string, v ...interface{})
}

// String method returns the name of the backend.
func (obj *Cran) String() string {
	return "cran"
}

// ScanData method is used to extract license ids from data and return licenses
// based on the license ids.
func (obj *Cran) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	// This check is taking place with the assumption that the file that will be
	// scanned will have to be named "DESCRIPTION".
	if info.FileInfo.Name() != CranFilename {
		return nil, nil // skip
	}
	if info.FileInfo.IsDir() {
		return nil, nil // skip
	}
	if len(data) == 0 {
		return nil, nil // skip
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var license string
	// boolean to check if we are in the license field
	var isInLicenseField bool = false
	licenseMap := make(map[string]struct{})
	reader := bytes.NewReader(data)
	scanner := bufio.NewScanner(reader)
	buf := []byte{}                       // create a buffer for very long lines
	scanner.Buffer(buf, CranMaxBytesLine) // set the max size of that buffer
	for scanner.Scan() {
		// In an effort to short-circuit things if needed, we run a check ourselves
		// and break out early if we see that we have cancelled early.
		select {
		case <-ctx.Done():
			return nil, errwrap.Wrapf(ctx.Err(), "scanner ended early")
		default:
		}

		s := scanner.Text() // newlines will be stripped here
		prefix := string(fieldFinder.Find([]byte(s)))
		if prefix != CranLicensePrefix {
			if len(prefix) > 0 && isInLicenseField {
				// new field has been found after license field
				break
			}
			if isInLicenseField {
				license += s
			}
			continue
		}
		isInLicenseField = true
		license = s[len(CranLicensePrefix):]
	}
	lids, err := CranDescriptionFileParser(license) // lid is licenseID
	err = errwrap.Append(err, scanner.Err())        // adding scanner error to error from parser
	if err != nil && lids == nil {
		return nil, errwrap.Wrapf(err, "cran parser or scanner error")
	}
	for _, lid := range lids {
		// TODO: should we normalize case here?
		licenseMap[lid] = struct{}{}
	}

	if len(licenseMap) == 0 {
		// NOTE: this is NOT the same as interfaces.ErrUnknownLicense because in this
		// scenario, we're comfortable (ish) the parser is exhaustive at finding a
		// license with this methodology. We want to return nil, but we error only if
		// CranDescriptionFileParser did and so normally this returns nil, nil.
		return nil, errwrap.Wrapf(err, "cran parser or scanner error")
	}

	ids := []string{}
	for id := range licenseMap {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic order

	licenseList := []*licenses.License{}

	for _, id := range ids {
		license := &licenses.License{
			SPDX: id,
			// TODO: populate other fields here?
		}

		// If we find an unknown SPDX ID, we don't want to error, because that would
		// allow someone to put junk in their code to prevent us scanning it. Instead,
		// create an invalid license but return it anyways. If we ever want to check
		// validity, we know to expect failures.
		// XXX: Some Cran licenses are not SPDX, therefore we might want to add
		// an alias matcher in the future.
		if err := license.Validate(); err != nil {
			//return nil, err
			license = &licenses.License{
				//SPDX: "",
				Origin: "", // unknown!
				Custom: id,
				// TODO: populate other fields here (eg: found license text)
			}
		}

		licenseList = append(licenseList, license)
	}

	result := &interfaces.Result{
		Licenses:   licenseList,
		Confidence: 1.0, // TODO: what should we put here?
	}

	// We perform the strange task of processing any partial results, and returning
	// some even if we errored, because the spdx code seems to think this is better
	// than no results. I'll do the same, but there is no guarantee the calling
	// iterator will use these. (Currently it does not!)
	return result, errwrap.Wrapf(err, "cran parser or scanner error")
}

// CranDescriptionFileParser is used to parse License field in DESCRIPTION
// files.
func CranDescriptionFileParser(input string) ([]string, error) {
	// Removing all next line characters from input.
	input = replaceNextLineCharacters.ReplaceAllString(input, "")
	if input == "" {
		return nil, ErrInvalidLicenseFormat
	}
	// Checking if input only consists of invalid License characters then nil and
	// ErrInvalidLicenseFormat should be returned.
	if licenseFormatChecker.ReplaceAllString(input, "") == "" {
		return nil, ErrInvalidLicenseFormat
	}
	// Removing all file names from input.
	input = replaceFilesInLicenseString.ReplaceAllString(input, "")
	if input == "" {
		// We are returning nil, nil here because the input only consisted of files
		// for Licenses.
		return nil, nil
	}
	var result []string
	var err error
	// XXX: I have only seen | in between license strings for now.
	// source: https://cran.rstudio.com/doc/manuals/r-devel/R-exts.html#Licensing
	listLicenseNames := strings.Split(input, "|")
	for i := range listLicenseNames {
		license := strings.TrimSpace(listLicenseNames[i])
		// NOTE: This is not the same as the second if statement since we are checking
		// each of the separated Licenses if they meet the general License format and
		// not the whole string.
		if licenseFormatChecker.ReplaceAllString(license, "") == "" {
			err = ErrInvalidLicenseFormat
			continue
		}
		result = append(result, license)
	}
	return result, err
}
