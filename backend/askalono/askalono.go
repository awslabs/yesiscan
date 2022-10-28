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

package askalono

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
)

const (
	// AskalonoVersion is the version string used in the git tag. These can
	// be seen here: https://github.com/jpeddicord/askalono/releases/
	AskalonoVersion = "0.4.6"
)

// AskalonoHashes maps version number, os, and then sha256sum. These are the
// hashes of the actual binaries, not the .zip files they are in. We ultimately
// care just about the integrity of the binary, so that's all we need to check.
// We don't also need to check the hash of the zip files, since we aren't
// worried about opening a zip file being dangerous.
// FIXME: We don't support different architectures for now. (eg: runtime.GOARCH)
var AskalonoHashes = map[string]map[string]string{
	"0.4.6": {
		"linux":   "a089146694cf433a4580c3da414cf43c70722ba6398d214fe41ca27b53deb476",
		"darwin":  "1e006e6c61ec4abd714ae930a94b2f447c57392d621a6e8367c7aaa4cb4f427c",
		"windows": "89f477e6e70e9bb58caf3b1f6a22fc6566e182ff81c3a920d49b6e6947ee97a1",
	},
}

//go:embed askalono-0.4.6-Linux.zip
var Askalono046Linux []byte

//go:embed askalono-0.4.6-macOS.zip
var Askalono046macOS []byte

//go:embed askalono-0.4.6-Windows.zip
var Askalono046Windows []byte

func init() {
	if _, err := GetExpectedHash(); err != nil {
		panic(fmt.Sprintf("error with askalono hash lookup: %v", err))
	}
}

// GetExpectedName returns the expected name of the binary for a given platform.
// This happens to also be the path it is expected to be found in the zip file
// because the packages contain that single file in the root. If this ever
// changes, then we need to add an additional GetExpectedPath method and change
// the logic.
func GetExpectedName() (safepath.RelFile, error) {
	switch os := runtime.GOOS; os {
	case "linux":
		return safepath.ParseIntoRelFile("askalono")
	case "darwin":
		return safepath.ParseIntoRelFile("askalono")
	case "windows":
		return safepath.ParseIntoRelFile("askalono.exe") // lol, windows
	default:
		return safepath.RelFile{}, fmt.Errorf("unsupported os: %s", os)
	}
}

// GetExpectedHash returns the expected hash of the binary for this version and
// OS.
func GetExpectedHash() (string, error) {
	m, exists := AskalonoHashes[AskalonoVersion]
	if !exists {
		return "", fmt.Errorf("no askalono hash found for version: %s", AskalonoVersion)
	}
	h, exists := m[runtime.GOOS]
	if !exists {
		return "", fmt.Errorf("no askalono hash found for os: %s", runtime.GOOS)
	}
	if h == "" {
		return "", fmt.Errorf("empty hash")
	}
	// the null hash, you can get this by running: `sha256sum /dev/null`
	if h == "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		return "", fmt.Errorf("null hash")
	}
	return h, nil
}

// GetZip returns the correct zipped package for this OS and ARCH. If it doesn't
// have one available, then it errors.
func GetZip() ([]byte, error) {
	if arch := runtime.GOARCH; arch != "amd64" {
		return nil, fmt.Errorf("unsupported arch: %s", arch)
	}

	var b []byte
	switch os := runtime.GOOS; os {
	case "linux":
		b = Askalono046Linux
	case "darwin":
		b = Askalono046macOS
	case "windows":
		b = Askalono046Windows
	default:
		return nil, fmt.Errorf("unsupported os: %s", os)
	}

	if len(b) == 0 {
		// Was it built/downloaded correctly?
		return nil, fmt.Errorf("empty binary")
	}
	return b, nil
}

// InstallBinary installs an askalono binary into this dir if it's not there
// already or if it has the wrong hash. It then returns its extracted size, and
// its complete path.
func InstallBinary(absDir safepath.AbsDir) (int64, safepath.AbsFile, error) {
	// NOTE: see this comment in the docs for this function. If the way the
	// zip files is built changes, we might need to change this for a
	// GetExpectedPath function call instead.
	relFileExpected, err := GetExpectedName()
	if err != nil {
		return 0, safepath.AbsFile{}, err
	}

	// this is where the output file will be stored
	absFile := safepath.JoinToAbsFile(absDir, relFileExpected)

	// First check the hash of the file at this location... If it's okay,
	// then we're done early!

	expectedHash, err := GetExpectedHash()
	if err != nil {
		// programming error, this was checked in init()
		return 0, safepath.AbsFile{}, err
	}

	if f, err := os.Open(absFile.Path()); err != nil && !os.IsNotExist(err) {
		// serious filesystem problem
		return 0, safepath.AbsFile{}, err
	} else if err == nil {
		// check the sha256 sum
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			f.Close() // close it when we exit this block
			return 0, safepath.AbsFile{}, err
		}
		f.Close() // close it when we exit this block

		if fmt.Sprintf("%x", h.Sum(nil)) != expectedHash {
			// The expected binary destination file is invalid. So
			// delete it. We will re-write it later. This is safest.
			if err := os.Remove(absFile.Path()); err != nil {
				return 0, safepath.AbsFile{}, errwrap.Wrapf(err, "error deleting invalid file: %s", absFile.Path())
			}
		}
	}

	b, err := GetZip()
	if err != nil {
		return 0, safepath.AbsFile{}, err
	}

	// Open the zip archive for reading.
	// FIXME: use a variant that can take a context
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return 0, safepath.AbsFile{}, err
	}
	//defer z.Close() // no close method exists
	if z.Comment != "" {
		//obj.Logf("zip has comment: %s", z.Comment)
	}

	// Iterate through the files in the archive.
	// XXX: can a child directory appear before a parent?
	// TODO: add a recurring progress logf if it takes longer than 30 sec
	var x *zip.File
	for _, x = range z.File {
		// TODO: obj.Debug ?
		//obj.Logf("zip: %s", x.Name)

		if x.FileInfo().IsDir() {
			continue
		}

		relFile, err := safepath.ParseIntoRelFile(x.Name)
		if err != nil {
			// programming error
			return 0, safepath.AbsFile{}, err
		}

		if relFileExpected.Cmp(relFile) == nil {
			break // found
		}
	}
	if x == nil {
		return 0, safepath.AbsFile{}, fmt.Errorf("did not file %s in zip archive", relFileExpected.Path())
	}

	// NOTE: On the difference between absDir and absFile.Dir()... If they
	// differ, that's because the relfile has a parent relDir component.

	// XXX: which mode method?
	if err := os.MkdirAll(absFile.Dir().Path(), os.ModePerm); err != nil {
		return 0, safepath.AbsFile{}, err
	}

	// open the actual source file
	// we need to read this into a buffer, because this is a ReadCloser, not
	// a ReadSeekCloser. We want to make sure it passes the hash, before we
	// write it out to disk.
	f, err := x.Open()
	if err != nil {
		return 0, safepath.AbsFile{}, errwrap.Wrapf(err, "error opening file %s", x.Name)
	}
	// don't `defer` close here because we want to free in the loop

	data, err := io.ReadAll(f)
	if err != nil {
		f.Close() // close file on error!
		return 0, safepath.AbsFile{}, err
	}
	f.Close()

	sum := sha256.Sum256(data)
	if h := fmt.Sprintf("%x", sum); h != expectedHash {
		return 0, safepath.AbsFile{}, fmt.Errorf("unexpected askalono binary hash of: %s", h)
	}

	// At this point, we can write out the file...
	// XXX: which mode method?
	if err := os.WriteFile(absFile.Path(), data, os.ModePerm); err != nil {
		return 0, safepath.AbsFile{}, errwrap.Wrapf(err, "error writing our file to disk at %s", absFile.Path())
	}

	return int64(len(data)), absFile, nil // this is where the new binary was copied to
}
