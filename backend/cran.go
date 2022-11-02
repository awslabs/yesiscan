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

// TODO: should this be a subpackage?
package backend

import (
	"bytes"
	"context"
	"errors"
	"net/mail"
	"regexp"
	"sort"
	"strings"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
)

const (
	// CranLicensePrefix is the string we look for when trying to find a
	// license.
	CranLicensePrefix = "License"

	// CranFilename is the filename used by the R metadata files.
	CranFilename = "DESCRIPTION"
)

var (
	// ErrInvalidLicenseFormat is an error used in the
	// CranDescriptionFileSubParser when licenses with invalid format are
	// found.
	ErrInvalidLicenseFormat = errors.New("invalid format in License(s)")

	// stripTrashCran is used to replace all strings which include file and
	// a filename and sometimes have a + or | before it. For example:
	// "| file LICENSE". This also replaces newline characters. source:
	// https://cran.rstudio.com/doc/manuals/r-devel/R-exts.html#Licensing
	stripTrashCran = regexp.MustCompile(`(([+,|]?([\n ])*)file([\n ])+\w+\b([\n ])*)|\n`)
)

// Cran is a backend for DESCRIPTION files which store R package metadata. We
// are getting the license names from the License field in the text file.
type Cran struct {
	Debug bool
	Logf  func(format string, v ...interface{})
}

// String method returns the name of the backend.
func (obj *Cran) String() string {
	return "cran"
}

// ScanData is used to extract license ids from data and return licenses based
// on the license ids.
func (obj *Cran) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	// This check is taking place with the assumption that the file that
	// will be scanned will be named "DESCRIPTION".
	if info.FileInfo.Name() != CranFilename {
		return nil, nil // skip
	}
	if info.FileInfo.IsDir() {
		return nil, nil // skip
	}
	if len(data) == 0 {
		return nil, nil // skip
	}

	// Appending a newline to the data because the parser needs to have a
	// SECOND trailing newline for it to work properly. Who knows why...
	data = append(data, "\n"...)
	reader := bytes.NewReader(data)
	// Parse the DESCRIPTION file using RFC5322 which is also used for mail.
	parsed, err := mail.ReadMessage(reader)
	if err != nil {
		return nil, errwrap.Wrapf(err, "parse error")
	}

	// Getting license information from License field.
	cranlicenseFields, ok := parsed.Header[CranLicensePrefix]
	if !ok {
		// This would mean we did not have a License field in the
		// DESCRIPTION file.
		return nil, nil
	}
	licenseMap := make(map[string]struct{})
	var subErr error
	for _, license := range cranlicenseFields {
		lids, err := CranDescriptionFileSubParser(license) // lid is licenseID
		if err != nil {
			subErr = errwrap.Append(subErr, err) // store for later
		}
		// Our parser might have partial results even when it errors.
		for _, lid := range lids {
			// TODO: should we normalize case here?
			licenseMap[lid] = struct{}{}
		}
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

		// If we find an unknown SPDX ID, we don't want to error,
		// because that would allow someone to put junk in their code to
		// prevent us scanning it. Instead, create an invalid license
		// but return it anyways. If we ever want to check validity, we
		// know to expect failures.
		// XXX: Some Cran licenses are not SPDX, therefore we might want
		// to add an alias matcher in the future.
		if err := license.Validate(); err != nil {
			//return nil, err
			license = &licenses.License{
				//SPDX: "",
				Origin: "", // unknown!
				Custom: id,
				// TODO: populate other fields here
				// (eg: found license text)
			}
		}

		licenseList = append(licenseList, license)
	}

	// We return any partial results, and even if we errored, because we can
	// now notify the user of these issues separately.
	result := &interfaces.Result{
		Licenses:   licenseList,
		Confidence: 1.0, // TODO: what should we put here?
		Skip:       errwrap.Wrapf(subErr, "cran sub-parser error"),
	}

	return result, nil
}

// CranDescriptionFileSubParser is used to parse the License field in
// DESCRIPTION files.
func CranDescriptionFileSubParser(input string) ([]string, error) {
	if input == "" {
		return nil, ErrInvalidLicenseFormat
	}
	// Removing all files and new line characters from input.
	input = stripTrashCran.ReplaceAllString(input, "")
	if input == "" {
		// We are returning nil, nil here because the input only
		// consisted of files for Licenses.
		return nil, nil
	}
	var result []string
	var err error
	// TODO: I have only seen | in between license strings for now. source:
	// https://cran.rstudio.com/doc/manuals/r-devel/R-exts.html#Licensing
	listLicenseNames := strings.Split(input, "|")
	for _, x := range listLicenseNames {
		license := strings.TrimSpace(x)
		if license == "" {
			err = ErrInvalidLicenseFormat
			continue
		}
		result = append(result, license)
	}
	return result, err
}
