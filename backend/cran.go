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
	// CranMaxBytesLine sets a larger maximum for file line scanning than the
	// default of bufio.MaxScanTokenSize which is sort of small.
	CranMaxBytesLine = 1024 * 1024 * 8 // 8 MiB

	// CranLicensePrefix is the string we look for when trying to find a license.
	CranLicensePrefix = "License"

	// CranFilename is the file name used by the R metadata files.
	CranFilename = "DESCRIPTION"
)

var (
	// ErrInvalidLicenseFormat is an error used in the CranDescriptionFileParser
	// when licenses with invalid format are found.
	ErrInvalidLicenseFormat error = errors.New("Invalid format in License(s)")

	// stripTrashCran is used to replace all strings which include file and a file
	// name and sometimes have a + or | before it. For example: "| file LICENSE".
	// This also replaces new line characters.
	// source: https://cran.rstudio.com/doc/manuals/r-devel/R-exts.html#Licensing
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

	// Appending \n to data because most of the times DESCRIPTION files do not have
	// an EOF character which would lead the parser to fail.
	data = append(data, "\n"...)
	reader := bytes.NewReader(data)
	// Parsing DESCRIPTION file.
	parsedInformation, err := mail.ReadMessage(reader)
	if err != nil {
		return nil, errwrap.Wrapf(err, "parse error")
	}

	header := parsedInformation.Header
	// Getting license information from License field.
	cranlicenses, ok := header[CranLicensePrefix]
	if !ok {
		// This would mean we did not have a License field in DESCRIPTION file.
		return nil, nil
	}
	license := strings.Join(cranlicenses, " | ")
	lids, err := CranDescriptionFileSubParser(license) // lid is licenseID
	if lids == nil {
		// If we did not get any license IDs we are returning nil and any error that
		// we may encounter in the sub parser.
		return nil, errwrap.Wrapf(err, "cran sub-parser error")
	}
	licenseMap := make(map[string]struct{})
	for _, lid := range lids {
		// TODO: should we normalize case here?
		licenseMap[lid] = struct{}{}
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
		// XXX: Some Cran licenses are not SPDX, therefore we might want to add an
		// alias matcher in the future.
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
	return result, errwrap.Wrapf(err, "cran sub-parser error")
}

// CranDescriptionFileSubParser is used to parse License field in DESCRIPTION
// files.
func CranDescriptionFileSubParser(input string) ([]string, error) {
	if input == "" {
		return nil, ErrInvalidLicenseFormat
	}
	// Removing all files and new line characters from input.
	input = stripTrashCran.ReplaceAllString(input, "")
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
		if license == "" {
			err = ErrInvalidLicenseFormat
			continue
		}
		result = append(result, license)
	}
	return result, err
}
