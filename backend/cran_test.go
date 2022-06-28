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
	"context"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/awslabs/yesiscan/backend"
	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/iterator"
	"github.com/awslabs/yesiscan/util/licenses"
	"github.com/awslabs/yesiscan/util/errwrap"
)

// cranFileInfo struct helps mask files to be DESCRIPTION files hence any file
// will qualify as a DESCRIPTION file.
type cranFileInfo struct {
	FileInfo fs.FileInfo
}

func (obj *cranFileInfo) Name() string       { return backend.CranFilename }
func (obj *cranFileInfo) Size() int64        { return obj.FileInfo.Size() }
func (obj *cranFileInfo) Mode() fs.FileMode  { return obj.FileInfo.Mode() }
func (obj *cranFileInfo) ModTime() time.Time { return obj.FileInfo.ModTime() }
func (obj *cranFileInfo) IsDir() bool        { return obj.FileInfo.IsDir() }
func (obj *cranFileInfo) Sys() interface{}   { return obj.FileInfo.Sys() }

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

// getDescriptionFilePaths helps get the paths for all input, output and error
// files that are to be tested. Each test must have an input, output and error
// file.
func getDescriptionFilePaths() ([]string, []string, []string, error) {
	var inputfilePaths []string
	var outputfilePaths []string
	var errorfilePaths []string
	files, err := filepath.Glob("./cran_test_cases/*.input")
	if err != nil {
		return nil, nil, nil, err
	}
	for _, file := range files {
		inputfilePaths = append(inputfilePaths, file)
		outputfilePath := strings.TrimSuffix(file, ".input") + ".output"
		errorfilePath := strings.TrimSuffix(file, ".input") + ".error"
		_, outputfilepatherr := os.Stat(outputfilePath)
		_, errorfilepatherr := os.Stat(errorfilePath)
		if outputfilepatherr != nil || errorfilepatherr != nil {
			outputfilePaths = append(outputfilePaths, "")
			errorfilePaths = append(errorfilePaths, "")
			continue
		}
		outputfilePaths = append(outputfilePaths, outputfilePath)
		errorfilePaths = append(errorfilePaths, errorfilePath)
	}
	return inputfilePaths, outputfilePaths, errorfilePaths, err
}

// getResults helps get results from output and error files
func getResults(outputfilepath string, errorfilepath string) ([]string, string, error) {
	var licenses []string = nil
	errorMessage := ""
	output, outputErr := ioutil.ReadFile(outputfilepath)
	message, messageErr := ioutil.ReadFile(errorfilepath)
	err := errwrap.Append(outputErr, messageErr)
	if err != nil {
		return nil, "", err
	}
	textOuput := string(output)
	textOuput = strings.TrimSuffix(textOuput, "\n")
	licenses = strings.Split(textOuput, ",")
	if licenses[0] == "" && len(licenses) == 1 {
		licenses = nil
	}
	errorMessage = string(message)
	errorMessage = strings.TrimSuffix(errorMessage, "\n")
	return licenses, errorMessage, nil

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
	inputfilePaths, outputfilePaths, errorfilePaths, err := getDescriptionFilePaths()
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
	for i, path := range inputfilePaths {
		inputFileInfo, err := os.Stat(path)
		if err != nil {
			t.Errorf("Error getting FileInfo: %v", err)
			continue
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			t.Errorf("File reading error: %v", err)
			continue
		}
		fileInfo := &cranFileInfo{
			FileInfo: inputFileInfo,
		}
		info := &interfaces.Info{
			FileInfo: fileInfo,
			UID:      iterator.FileScheme + path,
		}
		if outputfilePaths[i] == "" || errorfilePaths[i] == "" {
			t.Errorf("Error or output file not present for:  %v", path)
			continue
		}
		correctOutput, correctErrMessage, err := getResults(outputfilePaths[i], errorfilePaths[i])
		if err != nil {
			t.Errorf("Output or Error file reading error: %v", err)
			continue
		}
		result, err := cranBackend.ScanData(ctx, data, info)
		// Checking if any of the errors are nil before checking error messages.
		if err != nil {
			if err.Error() != correctErrMessage {
				t.Errorf("FileName: %v, Error: %v, Correct Error: %v", path, err.Error(), correctErrMessage)
				continue
			}
		} else {
			if correctErrMessage != "" {
				t.Errorf("FileName: %v, Error: %v, Correct Error: %v", path, err, correctErrMessage)
				continue
			}
		}
		if result != nil {
			output := licenseList(result.Licenses)
			if !reflect.DeepEqual(output, correctOutput) {
				t.Logf("FileName: %v, Output: %v, Correct Output: %v", path, output, correctOutput)
				continue
			}
		} else {
			if correctOutput != nil {
				t.Logf("FileName: %v, Output: %v, Correct Output: %v", path, result, correctOutput)
				continue
			}
		}
		t.Logf("Success!")
	}
	return
}
