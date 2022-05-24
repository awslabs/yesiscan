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

// Package licenses provides some structures for handling and representing
// software licenses. It uses SPDX representations for part of it, because there
// doesn't seem to be a better alternative. It doesn't guarantee that it
// implements all of the SPDX spec. If there's an aspect which you think was
// mis-implemented or is missing, please let us know.
// XXX: Add a test to check if the license-list-data submodule is up-to-date!
package licenses

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// licensesJson is populated automatically at build-time from the official spdx
// licenses.json file, which is linked into this repository as a git submodule.
//go:embed license-list-data/json/licenses.json
var licensesJSON []byte

//go:embed license-list-data/json/details/*.json
var licensesTextJSON embed.FS

//go:embed license-list-data/json/exceptions.json
var exceptionsJson []byte

//go:embed license-list-data/json/exceptions/*.json
var exceptionsTextJSON embed.FS

var (
	once        sync.Once
	LicenseList LicenseListSPDX // this gets populated during init()
)

func init() {
	once.Do(decode)
}

// TODO: import the exceptions if we ever decide we want to look at those.
func decode() {
	buffer := bytes.NewBuffer(licensesJSON)
	decoder := json.NewDecoder(buffer)
	if err := decoder.Decode(&LicenseList); err != nil {
		panic(fmt.Sprintf("error decoding spdx license list: %+v", err))
	}
	if len(LicenseList.Licenses) == 0 {
		panic(fmt.Sprintf("could not find any licenses to decode"))
	}

	// debug
	//dirEntry, err := licensesTextJSON.ReadDir("license-list-data/json/details")
	//if err != nil {
	//	panic(fmt.Sprintf("error: %+v", err))
	//}
	//for _, x := range dirEntry {
	//	fmt.Printf("Name: %+v\n", x.Name())
	//}

	for _, license := range LicenseList.Licenses {
		//fmt.Printf("ID: %+v\n", license.LicenseID) // debug

		f := "license-list-data/json/details/" + strings.TrimPrefix(license.Reference, "./")
		data, err := licensesTextJSON.ReadFile(f)
		if err != nil {
			panic(fmt.Sprintf("error reading spdx license file: %s, error: %+v", f, err))
		}
		//fmt.Printf("Data: %s\n", string(data)) // debug
		buffer := bytes.NewBuffer(data)
		decoder := json.NewDecoder(buffer)

		if err := decoder.Decode(&license); err != nil {
			panic(fmt.Sprintf("error decoding spdx license text: %+v", err))
		}
		//fmt.Printf("Text: %+v\n", license.Text) // debug
		if license.Text == "" {
			panic(fmt.Sprintf("could not find any license text for: %s", license.LicenseID))
		}
	}
}

// LicenseListSPDX is modelled after the official SPDX licenses.json file.
type LicenseListSPDX struct {
	Version string `json:"licenseListVersion"`

	Licenses []*LicenseSPDX `json:"licenses"`
}

// LicenseSPDX is modelled after the official SPDX license entries. It also
// includes fields from the referenced fields, which include the full text.
type LicenseSPDX struct {
	// Reference is a link to the full license .json file.
	Reference    string `json:"reference"`
	IsDeprecated bool   `json:"isDeprecatedLicenseId"`
	DetailsURL   string `json:"detailsUrl"`
	// ReferenceNumber is an index number for the license. I wouldn't
	// consider this to be stable over time.
	ReferenceNumber int64 `json:"referenceNumber"`
	// Name is a friendly name for the license.
	Name string `json:"name"`
	// LicenseID is the SPDX ID for the license.
	LicenseID     string   `json:"licenseId"`
	SeeAlso       []string `json:"seeAlso"`
	IsOSIApproved bool     `json:"isOsiApproved"`

	//IsDeprecated bool `json:"isDeprecatedLicenseId"` // appears again
	IsFSFLibre bool   `json:"isFsfLibre"`
	Text       string `json:"licenseText"`
}

// License is a representation of a license. It's better than a simple SPDX ID
// as a string, because it allows us to store alternative representations to an
// internal or different representation, as well as any other information that
// we want to have associated here.
type License struct {
	// SPDX is the well-known SPDX ID for the license.
	SPDX string

	// Origin shows a different license provenance, and associated custom
	// name. It should probably be a "reverse-dns" style unique identifier.
	Origin string
	// Custom is a custom string that is a unique identifier for the license
	// in the aforementioned Origin namespace.
	Custom string
}

// String returns a string representation of whatever license is specified.
func (obj *License) String() string {
	if obj.Origin != "" && obj.Custom != "" {
		return fmt.Sprintf("%s(%s)", obj.Custom, obj.Origin)
	}

	if obj.Origin == "" && obj.Custom != "" {
		return fmt.Sprintf("%s(unknown)", obj.Custom) // TODO: display this differently?
	}

	// TODO: replace with a different short name if one exists
	return obj.SPDX
}

// Validate returns an error if the license doesn't have a valid representation.
// For example, if you express the license as an SPDX ID, this will validate
// that it is among the known licenses.
func (obj *License) Validate() error {
	if obj.SPDX != "" {
		// if an SPDX ID is specified, we validate based on it!
		_, err := ID(obj.SPDX)
		return err
	}

	// valid, but from an unknown origin
	if obj.Origin != "" && obj.Custom != "" {
		return nil
	}

	if obj.Origin == "" && obj.Custom != "" {
		return fmt.Errorf("unknown custom license: %s", obj.Custom)
	}

	return fmt.Errorf("unknown license format")
}

// Cmp compares two licenses and determines if they are identical.
func (obj *License) Cmp(license *License) error {
	if obj.SPDX != license.SPDX {
		return fmt.Errorf("the SPDX field differs")
	}
	if obj.Origin != license.Origin {
		return fmt.Errorf("the Origin field differs")
	}
	if obj.Custom != license.Custom {
		return fmt.Errorf("the Custom field differs")
	}

	return nil
}

// ID looks up the license from the imported list. Do not modify the result as
// it is the global database that everyone is using.
func ID(spdx string) (*LicenseSPDX, error) {
	for _, license := range LicenseList.Licenses {
		if spdx == license.LicenseID {
			return license, nil
		}
	}
	return nil, fmt.Errorf("license ID (%s) not found", spdx)
}

// StringToLicense takes an input string and returns a license struct. This can
// handle both normal SPDX ID's and the origin strings in the `name(origin)`
// format. It rarely returns an error unless you pass it an obviously fake
// license identifier.
// TODO: add some tests
func StringToLicense(name string) (*License, error) {
	license := &License{
		SPDX: name,
	}

	if err := license.Validate(); err == nil {
		return license, nil
	}

	// assume this for now...
	license = &License{
		//SPDX: "",
		Origin: "", // unknown
		Custom: name,
	}

	// parse the licenseName(origin) syntax
	ix := strings.Index(name, "(")
	if ix > -1 && strings.HasSuffix(name, ")") && (ix+1) < (len(name)-1) {
		license = &License{
			//SPDX: "",
			Origin: name[ix+1 : len(name)-1],
			Custom: name[0:ix],
		}
	}

	lhs := strings.Count(name, "(")
	rhs := strings.Count(name, ")")
	if lhs != rhs {
		return nil, fmt.Errorf("unbalanced parenthesis")
	}
	if lhs != 0 && lhs != 1 {
		return nil, fmt.Errorf("invalid parenthesis count")
	}

	return license, nil
}

// StringsToLicenses converts a list of input strings and converts them into the
// matching list of license structs. It accepts non-SPDX license names in the
// standard SPDX format of `name(origin)`.
func StringsToLicenses(inputs []string) ([]*License, error) {
	licenses := []*License{}

	for _, x := range inputs {
		license, err := StringToLicense(x)
		if err != nil {
			return nil, err
		}
		licenses = append(licenses, license)
	}

	return licenses, nil
}

// Join joins the string representations of a list of licenses with comma space.
func Join(licenses []*License) string {
	xs := []string{}
	for _, license := range licenses {
		xs = append(xs, license.String())
	}
	return strings.Join(xs, ", ")
}

// InList returns true if a license exists inside a list, otherwise false. It
// uses the license Cmp method to determine equality.
func InList(needle *License, haystack []*License) bool {
	for _, x := range haystack {
		if needle.Cmp(x) == nil {
			return true
		}
	}
	return false
}

// Union returns the union of licenses in both input lists. It uses the pointers
// from the first list in the results. It does not try to remove duplicates so
// if either list has duplicates, you may end up with duplicates in the result.
// It uses the license Cmp method to determine equality.
func Union(haystack1 []*License, haystack2 []*License) []*License {
	union := []*License{}
	for _, x := range haystack1 {
		if InList(x, haystack2) {
			union = append(union, x)
		}
	}
	return union
}
