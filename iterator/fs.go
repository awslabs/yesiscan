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

package iterator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"

	//gitConfig "github.com/go-git/go-git/config" // with go modules disabled
	gitConfig "github.com/go-git/go-git/v5/config" // with go modules enabled (GO111MODULE=on or outside GOPATH)
)

const (
	// FileScheme is the standard prefix used for file path UID's.
	FileScheme = "file://"
)

// Fs is an iterator that scans your local filesystem at the specified path.
// Recursive scanners, while running a scan function, can also return more
// iterators. In this pattern, this iterator may be used to run on a cloned git
// directory after the git iterator pulled the files down onto the filesystem.
// If we encounter a git submodule (by finding a .gitmodules file) we will parse
// it and return a number of git iterators for each of the contained
// repositories.
// TODO: This iterator could learn how to identify go.mod files, python, java,
// etc, and learn how to iterate into those projects by returning new iterators.
// TODO: This iterator could grow a Copy option to copy the files into a new
// directory before iterating over them.
type Fs struct {
	Debug  bool
	Logf   func(format string, v ...interface{})
	Prefix safepath.AbsDir

	// Parser is a pointer to the parser that returned this. If it wasn't
	// returned by a parser, leave this nil. If this iterator came from an
	// iterator, then the Iterator handle should be filled instead.
	Parser interfaces.Parser

	// Iterator is a pointer to the iterator that returned this. If it
	// wasn't returned by an iterator, leave this nil. If this iterator came
	// from a parser, then the Parser handle should be filled instead.
	Iterator interfaces.Iterator

	// Path is the location of the fs to walk.
	Path safepath.Path

	// GenUID takes the safe path that would have been used to build the UID
	// and returns an improved UID that is more pleasantly human readable.
	// Specifying this function is optional, but if it is used, it's not
	// recommended to error unless there's a programming mistake, and you
	// must be confident that your results will be properly unique.
	GenUID func(safepath.Path) (string, error)

	// Unlock is a function that should be called as part of the Close
	// method once this resource is finished. It can be defined when
	// building this iterator in case we want a mechanism for the caller of
	// this iterator to tell the child when to unlock any in-use resources.
	// It must be safe to call this function more than once if necessary.
	// This is currently unused.
	Unlock func()
}

// String returns a human-readable representation of the fs path we're looking
// at. The output of this format is not guaranteed to be constant, so don't try
// to parse it.
func (obj *Fs) String() string {
	return fmt.Sprintf("fs: %s", obj.Path)
}

// Validate runs some checks to ensure this iterator was built correctly.
func (obj *Fs) Validate() error {
	if obj.Logf == nil {
		return fmt.Errorf("the Logf function must be specified")
	}
	if err := obj.Prefix.Validate(); err != nil {
		return err
	}

	if !obj.Path.IsAbs() {
		return fmt.Errorf("the Path must be absolute") // for
	}
	// TODO: check if path exists?
	return nil
}

// GetParser returns a handle to the parent parser that built this iterator if
// there is one.
func (obj *Fs) GetParser() interfaces.Parser { return obj.Parser }

// GetIterator returns a handle to the parent iterator that built this iterator
// if there is one.
func (obj *Fs) GetIterator() interfaces.Iterator { return obj.Iterator }

// Recurse runs a simple recursive iterator that walks through a local
// filesystem path. It applies a scan function to everything that it encounters.
// While iterating, it may also discover certain files that it can use to
// produce new iterators.
func (obj *Fs) Recurse(ctx context.Context, scan interfaces.ScanFunc) ([]interfaces.Iterator, error) {

	mu := &sync.Mutex{} // guards any concurrently modified state
	iterators := []interfaces.Iterator{}

	obj.Logf("running %s", obj.String())

	if !obj.Path.IsAbs() {
		return nil, fmt.Errorf("path is not absolute")
	}

	// it's a single file, not a directory
	if !obj.Path.IsDir() {
		absFile := safepath.UnsafeParseIntoAbsFile(obj.Path.Path())

		// XXX: symlink detection?
		fileInfo, err := os.Stat(obj.Path.Path()) // XXX: stat or Lstat?
		if err != nil {
			return nil, errwrap.Wrapf(err, "could not stat during single file scan")
		}
		if fileInfo.IsDir() {
			return nil, fmt.Errorf("input path contained no trailing slash but is a dir")
		}
		uid := FileScheme + obj.Path.String() // the (ugly) default
		if obj.GenUID != nil {
			var err error
			uid, err = obj.GenUID(obj.Path)
			if err != nil {
				// probable programming error
				return nil, errwrap.Wrapf(err, "the GetUID func failed")
			}
		}
		info := &interfaces.Info{
			FileInfo: fileInfo,
			UID:      uid,
		}

		if absFile.HasExtInsensitive(ZipExtension) || absFile.HasExtInsensitive(JarExtension) {
			iterator := &Zip{
				Debug: obj.Debug,
				Logf: func(format string, v ...interface{}) {
					obj.Logf(format, v...) // TODO: add a prefix?
				},
				Prefix: obj.Prefix,

				Iterator: obj,

				Path: absFile,

				//AllowAnyExtension: false, // not helpful here
				AllowedExtensions: []string{
					ZipExtension,
					JarExtension,
				},
			}

			mu.Lock()
			iterators = append(iterators, iterator)
			mu.Unlock()
			return iterators, nil
		}

		isGzip := false
		for _, x := range GzipExtensions {
			if absFile.HasExtInsensitive(x) {
				isGzip = true
				break
			}
		}
		if isGzip {
			iterator := &Gzip{
				Debug: obj.Debug,
				Logf: func(format string, v ...interface{}) {
					obj.Logf(format, v...) // TODO: add a prefix?
				},
				Prefix: obj.Prefix,

				Iterator: obj,

				Path: absFile,

				//AllowAnyExtension: false, // not helpful here
			}

			mu.Lock()
			iterators = append(iterators, iterator)
			mu.Unlock()
			return iterators, nil
		}

		isBzip2 := false
		for _, x := range Bzip2Extensions {
			if absFile.HasExtInsensitive(x) {
				isBzip2 = true
				break
			}
		}
		if isBzip2 {
			iterator := &Bzip2{
				Debug: obj.Debug,
				Logf: func(format string, v ...interface{}) {
					obj.Logf(format, v...) // TODO: add a prefix?
				},
				Prefix: obj.Prefix,

				Iterator: obj,

				Path: absFile,

				//AllowAnyExtension: false, // not helpful here
			}

			mu.Lock()
			iterators = append(iterators, iterator)
			mu.Unlock()
			return iterators, nil
		}

		//return nil, errwrap.Wrapf(scan(ctx, obj.Path, info), "single file scan func failed")
		// We want to ignore the ErrUnknownLicense results, and error if
		// we hit any actual errors that we should bubble upwards.
		if err := scan(ctx, obj.Path, info); err != nil && !errors.Is(err, interfaces.ErrUnknownLicense) {
			// XXX: ShutdownOnError?
			return nil, errwrap.Wrapf(err, "single file scan func failed")
		}

		return iterators, nil // iterators should be empty
	}

	// TODO: Replace this with a parallel walk for performance
	// TODO: Maybe add a separate flag/switch for it in the options?
	// TODO: Make sure result aggregation and skipdir support still works!
	// TODO: Replace this with a walk that accepts safepath types instead.
	err := filepath.Walk(obj.Path.Path(), func(path string, fileInfo fs.FileInfo, err error) error {
		if err != nil {
			// prevent panic by handling failure accessing a path
			return errwrap.Wrapf(err, "fail inside walk with: %s", path)
		}

		// XXX: rename so we don't confuse safePath with safepath
		safePath, err := safepath.ParseIntoPath(path, fileInfo.IsDir())
		if err != nil {
			return err
		}

		// Mechanism to end a particularly long walk early if needed...
		// In an effort to short-circuit things if needed, we run a
		// check ourselves and break out early if we see that we have
		// cancelled early.
		select {
		case <-ctx.Done():
			return errwrap.Wrapf(ctx.Err(), "ended walk early")
		default:
		}

		// skip symlinks
		if fileInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
			return nil
		}

		// Check for a .gitmodules file.
		gitIterators, err := obj.GitSubmodulesHelper(ctx, safePath)
		if err != nil {
			return err
		}
		if gitIterators != nil && len(gitIterators) > 0 {
			mu.Lock()
			for _, iterator := range gitIterators {
				iterators = append(iterators, iterator)
			}
			mu.Unlock()
		}

		// Skip iterating over certain paths.
		if skip, err := SkipPath(safePath, fileInfo); skip || err != nil {
			if obj.Debug && (skip || err == interfaces.SkipDir) {
				obj.Logf("skipping: %s", safePath.String())
			}
			return err // nil to skip, interfaces.SkipDir, or error
		}

		if !safePath.IsDir() && safePath.IsAbs() {
			absFile := safepath.UnsafeParseIntoAbsFile(safePath.Path())
			if absFile.HasExtInsensitive(ZipExtension) || absFile.HasExtInsensitive(JarExtension) {
				iterator := &Zip{
					Debug: obj.Debug,
					Logf: func(format string, v ...interface{}) {
						obj.Logf(format, v...) // TODO: add a prefix?
					},
					Prefix: obj.Prefix,

					Iterator: obj,

					Path: absFile,

					//AllowAnyExtension: false, // not helpful here
					AllowedExtensions: []string{
						ZipExtension,
						JarExtension,
					},
				}

				mu.Lock()
				iterators = append(iterators, iterator)
				mu.Unlock()
				// NOTE: if we return nil here, then we block
				// any scanners that might want to handle a
				// whole .zip file in one go specially...
			}
		}

		if obj.Debug {
			obj.Logf("visited file or dir: %q", path)
		}

		uid := FileScheme + safePath.String() // the (ugly) default
		if obj.GenUID != nil {
			var err error
			uid, err = obj.GenUID(safePath)
			if err != nil {
				// probable programming error
				return errwrap.Wrapf(err, "the GetUID func failed")
			}
		}
		info := &interfaces.Info{
			FileInfo: fileInfo,
			UID:      uid,
		}
		// We want to ignore the ErrUnknownLicense results, and error if
		// we hit any actual errors that we should bubble upwards.
		if err := scan(ctx, safePath, info); err != nil && !errors.Is(err, interfaces.ErrUnknownLicense) {
			// XXX: ShutdownOnError?
			return errwrap.Wrapf(err, "scan func failed")
		}

		return nil
	})
	//if obj.Debug { obj.Logf("walk done!") } // debug

	return iterators, errwrap.Wrapf(err, "walk failed")
}

// Close shuts down the iterator and/or performs clean up after the Recurse
// method has run. This must be called if you run Recurse.
func (obj *Fs) Close() error {
	if obj.Unlock != nil {
		obj.Unlock()
	}
	return nil
}

// GitSubmodulesHelper is a helper that checks for a .gitmodules file and
// produces the iterators that come from it.
func (obj *Fs) GitSubmodulesHelper(ctx context.Context, path safepath.Path) ([]interfaces.Iterator, error) {
	// TODO: this could happen in init() if we wanted to optimize perf a bit
	gitModulesRelFile, err := safepath.ParseIntoRelFile(".gitmodules")
	if err != nil {
		return nil, err // bug!
	}

	absFile, ok := path.(safepath.AbsFile)
	if !ok || absFile.Base().Cmp(gitModulesRelFile) != nil {
		return nil, nil
	}

	// parse absFile and get a list of URL's
	contents, err := os.ReadFile(absFile.Path())
	if err != nil {
		return nil, err
	}

	// found some possible git submodules
	modules := gitConfig.NewModules()
	if err := modules.Unmarshal(contents); err != nil {
		return nil, err
	}

	iterators := []interfaces.Iterator{}
	names := []string{}
	for name := range modules.Submodules {
		names = append(names, name)
	}
	sort.Strings(names) // loop in deterministic order
	for _, name := range names {
		submodule := modules.Submodules[name]
		if err := submodule.Validate(); err != nil {
			return nil, err
		}

		if obj.Debug {
			obj.Logf("found git submodule named: %s", submodule.Name)
		}

		iterator := &Git{
			Debug: obj.Debug,
			Logf: func(format string, v ...interface{}) {
				obj.Logf(format, v...) // TODO: add a prefix?
			},
			Prefix: obj.Prefix,

			Iterator: obj,

			//submodule.Path
			URL: submodule.URL,
			//submodule.Branch // TODO: use this?
			TrimGitSuffix: true,
		}
		iterators = append(iterators, iterator)
	}

	return iterators, nil
}
