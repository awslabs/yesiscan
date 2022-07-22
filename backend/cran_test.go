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

package backend_test

import (
	"bytes"
	"context"
	"io/fs"
	"io/ioutil"
	"net/mail"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awslabs/yesiscan/backend"
	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/iterator"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
)

// CranDescriptionFileSubParser parses the string in the License field to get
// License names from DESCRIPTION files. If no Licenses are found nil is
// returned and any files mentioned are ignored by the parser.
func TestCranDescriptionFileSubParser(t *testing.T) {
	errVal := backend.ErrInvalidLicenseFormat
	tests := []struct {
		input  string
		output []string
		err    error
	}{
		{"", nil, errVal},
		{"||||||", nil, errVal},
		{"++--###", []string{"++--###"}, nil},
		{"file LICENSE", nil, nil},
		{"file any", nil, nil},
		{"MIT + file LICENSE", []string{"MIT"}, nil},
		{"MIT + file LICENSE | file LICENSE", []string{"MIT"}, nil},
		{"Artistic-2.0 | AGPL-3 + file LICENSE", []string{"Artistic-2.0", "AGPL-3"}, nil},
		{"GPL-2 | \n file LICENSE", []string{"GPL-2"}, nil},
		{"MIT + file LICENSE | file LICENSE | AGPL-3 + file anything", []string{"MIT", "AGPL-3"}, nil},
		{"Artistic-2.0 | AGPL-3 + file any | MIT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, nil},
		{"Artistic-2.0 | | MIT +file LICENSE", []string{"Artistic-2.0", "MIT"}, errVal},
		{"Artistic-2.0 | \n AGPL-3 + file  any | \n MIT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, nil},
		{"Artistic-2.0 | \n AGPL-3 \n + file any | \n MI\nT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "MIT"}, nil},
		{"Artistic-2.0 | \n AGPL-3 \n + file any | -+-+##& | \n MIT + file LICENSE", []string{"Artistic-2.0", "AGPL-3", "-+-+##&", "MIT"}, nil},
	}

	for _, test := range tests {
		output, err := backend.CranDescriptionFileSubParser(test.input)
		if err != test.err {
			t.Errorf("Error %v, Correct Error %v", err, test.err)
			continue
		}
		if !reflect.DeepEqual(output, test.output) {
			t.Logf("Output %v, Correct Output %v", output, test.output)
			continue
		}
		t.Logf("Success!")
	}
}

// getDescriptionFilePaths helps get the paths for all the files that are to be
// tested. In that directory there is a DESCRIPTION file whose file info is used
// by all the files in the directory hence being able to test multiple files as
// test cases in one directory without the file name of DESCRIPTION.
func getDescriptionFilePaths() ([]string, []string, fs.FileInfo, error) {
	var filePaths []string
	var fileNames []string
	var fileInfo fs.FileInfo
	err := filepath.Walk("./cran_test_cases",
		func(path string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if info.Name() == backend.CranFilename {
				fileInfo = info
				return nil
			}
			filePaths = append(filePaths, path)
			fileNames = append(fileNames, info.Name())
			return nil
		})

	return filePaths, fileNames, fileInfo, err
}

// reverseList reverses a list of strings.
func reverseList(input []string) {
	for i, j := 0, len(input)-1; i < j; i, j = i+1, j-1 {
		input[i], input[j] = input[j], input[i]
	}
}

// correctResults finds the correct license list and error based on input data.
func correctResults(data []byte) ([]string, error) {
	//appending \n to make sure of EOF being present in file.
	data = append(data, "\n"...)
	reader := bytes.NewReader(data)
	parsedInformation, err := mail.ReadMessage(reader)
	if err != nil {
		return nil, errwrap.Wrapf(err, "parse error")
	}
	header := parsedInformation.Header
	cranlicenses, ok := header[backend.CranLicensePrefix]
	if !ok {
		// This would mean we did not have a License field in the file.
		return nil, nil
	}
	license := strings.Join(cranlicenses, " | ")
	lids, err := backend.CranDescriptionFileSubParser(license)
	if lids == nil {
		// If we did not get any license IDs we are returning nil and any error that
		// we may encounter in the sub parser.
		return nil, errwrap.Wrapf(err, "cran sub-parser error")
	}
	// reversing lists of licenses since that is how it is returned in result interface
	reverseList(lids)
	return lids, errwrap.Wrapf(err, "cran sub-parser error")
}

// licenseList is a method which converts the list of licenses to a list of
// strings.
func licenseList(input []*licenses.License) []string {
	var result []string
	for i := range input {
		if input[i].SPDX == "" {
			result = append(result, input[i].Custom)
			continue
		}
		result = append(result, input[i].SPDX)
	}
	return result
}

// TestCranBackend tests whether the cran backend runs as intended.
func TestCranBackend(t *testing.T) {
	filePaths, fileNames, fileInfo, err := getDescriptionFilePaths()
	if err != nil {
		t.Errorf("Error in getting file paths: %v", err)
	}
	cranBackend := &backend.Cran{
		Debug: false,
		Logf: func(format string, v ...interface{}) {
			t.Logf("backend: "+format, v...)
		},
	}
	var ctx context.Context
	for i, path := range filePaths {
		data, err := ioutil.ReadFile(path)
		if err != nil {
			t.Errorf("File reading error: %v", err)
			continue
		}

		info := &interfaces.Info{
			FileInfo: fileInfo,
			UID:      iterator.FileScheme + path,
		}
		correctOutput, correctErr := correctResults(data)
		result, err := cranBackend.ScanData(ctx, data, info)
		fileName := fileNames[i]
		if err != correctErr {
			// Checking if any of the errors are nil before checking error messages.
			if err == nil || correctErr == nil {
				t.Logf("FileName: %v, Error: %v, Correct Error: %v", fileName, err, correctErr)
				continue
			}
			// Checking error.Error() message to see if the errors are truly different.
			if err.Error() != correctErr.Error() {
				t.Logf("FileName: %v, Error: %v, Correct Error: %v", fileName, err, correctErr)
				continue
			}
		}

		if result != nil {
			output := licenseList(result.Licenses)
			if !reflect.DeepEqual(output, correctOutput) {
				t.Logf("FileName: %v, Output: %v, Correct Output: %v", fileName, output, correctOutput)
				continue
			}
		} else {
			if correctOutput != nil {
				t.Logf("FileName: %v, Output: %v, Correct Output: %v", fileName, result, correctOutput)
				continue
			}
		}
		t.Logf("Success!")
	}
	return
}
