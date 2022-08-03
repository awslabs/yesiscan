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
	"fmt"
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
)

// cranFileInfo struct helps make any input file to be DESCRIPTION files.
type cranFileInfo struct {
	fileInfo fs.FileInfo
}

func (obj *cranFileInfo) Name() string       { return backend.CranFilename }
func (obj *cranFileInfo) Size() int64        { return obj.fileInfo.Size() }
func (obj *cranFileInfo) Mode() fs.FileMode  { return obj.fileInfo.Mode() }
func (obj *cranFileInfo) ModTime() time.Time { return obj.fileInfo.ModTime() }
func (obj *cranFileInfo) IsDir() bool        { return obj.fileInfo.IsDir() }
func (obj *cranFileInfo) Sys() interface{}   { return obj.fileInfo.Sys() }

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

	for i, test := range tests {
		out, err := backend.CranDescriptionFileSubParser(test.input)
		if err != test.err {
			t.Errorf("err: %v, exp err: %v", err, test.err)
			continue
		}
		if !reflect.DeepEqual(out, test.output) {
			t.Errorf("out: %v, exp out: %v", out, test.output)
			continue
		}
		t.Logf("test# %d succeeded!", i)
	}
}

// TestCranBackend tests whether the cran backend runs as intended.
func TestCranBackend(t *testing.T) {
	inputfilePaths, err := filepath.Glob("./cran_test_cases/*.input")
	if err != nil {
		t.Errorf("error getting input files: %v", err)
		return
	}
	cranBackend := &backend.Cran{
		Debug: false,
		Logf: func(format string, v ...interface{}) {
			t.Logf("backend: "+format, v...)
		},
	}
	for _, path := range inputfilePaths {
		inputFileInfo, err := os.Stat(path)
		if err != nil {
			t.Errorf("error getting FileInfo: %v", err)
			continue
		}
		data, err := ioutil.ReadFile(path)
		if err != nil {
			t.Errorf("error reading input file: %v", err)
			continue
		}
		fileInfo := &cranFileInfo{
			fileInfo: inputFileInfo,
		}
		info := &interfaces.Info{
			FileInfo: fileInfo,
			UID:      iterator.FileScheme + path,
		}

		outputFilePath := strings.TrimSuffix(path, ".input") + ".output"
		errorFilePath := strings.TrimSuffix(path, ".input") + ".error"
		// TODO: if there is no error file, assume we expect no error
		outputContents, outputFileErr := ioutil.ReadFile(outputFilePath)
		if outputFileErr != nil {
			t.Errorf("error reading output file: %v", outputFileErr)
		}
		errorContents, errorFileErr := ioutil.ReadFile(errorFilePath)
		if errorFileErr != nil {
			t.Errorf("error reading error file: %v", errorFileErr)
		}
		if outputFileErr != nil || errorFileErr != nil {
			// give both statements a chance to tell us what's
			// missing before we go on to the next test case
			continue
		}

		expOut := strings.TrimSuffix(string(outputContents), "\n")
		var expErr error
		if s := strings.TrimSuffix(string(errorContents), "\n"); s != "" {
			expErr = fmt.Errorf(s)
		}

		result, err := cranBackend.ScanData(context.Background(), data, info)
		if (err == nil) != (expErr == nil) { // xor
			t.Errorf("filename: %v, err: %v", path, err)
			t.Errorf("filename: %v, exp: %v", path, expErr)
			continue
		}
		if err != nil && expErr != nil {
			if err.Error() != expErr.Error() { // compare the strings
				t.Errorf("filename: %v, err: %v", path, err)
				t.Errorf("filename: %v, exp: %v", path, expErr)
				continue
			}
		}

		var out string
		if result != nil {
			out = licenses.Join(result.Licenses)
		}
		if out != expOut {
			t.Errorf("filename: %v, out: %v", path, out)
			t.Errorf("filename: %v, exp: %v", path, expOut)
			continue
		}

		t.Logf("Success!")
	}
}
