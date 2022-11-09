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
	"context"
	"encoding/xml"
	"sort"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
)

const (
	// PomFilename is the file name used by the pomfiles.
	PomFilename = "pom.xml"
)

// Pom is a backend for Pom or Project Object Model files. It is an xml file
// commonly used by the Maven Project under the name pom.xml. We are getting the
// license names by parsing the pom.xml file.
type Pom struct {
	Debug bool
	Logf  func(format string, v ...interface{})
}

// String method returns the name of the backend.
func (obj *Pom) String() string {
	return "pom"
}

// ScanData method is used to extract license ids from data and return licenses
// based on the license ids.
func (obj *Pom) ScanData(ctx context.Context, data []byte, info *interfaces.Info) (*interfaces.Result, error) {
	// This check is taking place with the assumption that the file that will be
	// scanned will have to be named "pom.xml".
	if info.FileInfo.Name() != PomFilename {
		return nil, nil // skip
	}
	if info.FileInfo.IsDir() {
		return nil, nil // skip
	}
	if len(data) == 0 {
		return nil, nil // skip
	}

	licenseMap := make(map[string]struct{})
	var pomFileLicenses PomLicenses

	// parsing pom.xml file to get license names in struct
	if err := xml.Unmarshal(data, &pomFileLicenses); err != nil {
		// There is a parse error with the file, so we can't properly
		// examine it for licensing information with this pom scanner.
		result := &interfaces.Result{
			Confidence: 1.0, // TODO: what should we put here?
			Skip:       errwrap.Wrapf(err, "parse error"),
		}
		return result, nil
	}

	if len(pomFileLicenses.Names) == 0 {
		// If we did not get any license names from the pom file we return nil, nil.
		return nil, nil
	}

	// lid is license id
	for _, lid := range pomFileLicenses.Names {
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
		// XXX: Many Pom licenses are not SPDX, therefore we might want to add an alias
		// matcher in the future.
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

	return result, nil
}

// PomLicenses is a struct that helps store license names from the licenses
// field in a pom.xml file.
type PomLicenses struct {
	// Names is a variable that will store the license names from pom.xml.
	Names []string `xml:"licenses>license>name"`
}
