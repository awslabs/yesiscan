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
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/awslabs/yesiscan/interfaces"
	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/safepath"
)

const (
	// ZipExtension is the standard extension used for zip URI's.
	ZipExtension = ".zip"

	// JarExtension is used for java .jar files. This is included here since
	// they are just zip files that are named differently.
	JarExtension = ".jar"
)

var (
	zipMapMutex *sync.Mutex
	zipMutexes  map[string]*sync.Mutex
)

func init() {
	zipMapMutex = &sync.Mutex{}
	zipMutexes = make(map[string]*sync.Mutex)
}

// Zip is an iterator that takes a .zip URI to download and performs the unzip
// operation. It will eventually return an Fs iterator since there's no need for
// it to know how to walk through a filesystem tree itself. It can use a local
// cache so that future calls to the same URI won't have to waste cycles, but
// only in cases when we can determine it will be the same file.
type Zip struct {
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

	// Path is the location of the file to unzip.
	Path safepath.AbsFile

	// FIXME: add zip max file limit field to prevent zip bombs

	// TODO: add zip password field

	// AllowAnyExtension specifies whether we will attempt to run if the
	// Path does not end with the correct zip extension.
	AllowAnyExtension bool

	// AllowedExtensions specifies a list of extensions that we are allowed
	// to try to decode from. If this is empty, then we allow only the
	// default of zip because allowing no extensions at all would make no
	// sense. If AllowAnyExtension is set, then this has no effect. All the
	// matches are case insensitive.
	AllowedExtensions []string

	// iterators store the list of which iterators we created, so we know
	// which ones we have to close!
	iterators []interfaces.Iterator

	// unlock is a function that should be called as part of the Close
	// method once this resource is finished. It can be defined when
	// building this iterator in case we want a mechanism for the caller of
	// this iterator to tell the child when to unlock any in-use resources.
	// It must be safe to call this function more than once if necessary.
	// This is currently used privately.
	unlock func()
}

// String returns a human-readable representation of the zip path we're looking
// at. The output of this format is not guaranteed to be constant, so don't try
// to parse it.
func (obj *Zip) String() string {
	return fmt.Sprintf("zip: %s", obj.Path)
}

// Validate runs some checks to ensure this iterator was built correctly.
func (obj *Zip) Validate() error {
	if obj.Logf == nil {
		return fmt.Errorf("the Logf function must be specified")
	}
	if err := obj.Prefix.Validate(); err != nil {
		return err
	}

	if obj.Path.Path() == "" {
		return fmt.Errorf("must specify a Path")
	}

	return obj.validateExtension()
}

// validateExtension is a helper function to process our extension validation.
func (obj *Zip) validateExtension() error {
	if obj.AllowAnyExtension {
		return nil
	}
	if obj.Path.HasExtInsensitive(ZipExtension) && len(obj.AllowedExtensions) == 0 {
		return nil
	}

	for _, x := range obj.AllowedExtensions {
		if obj.Path.HasExtInsensitive(x) {
			return nil
		}
	}

	if len(obj.AllowedExtensions) == 0 {
		return fmt.Errorf("the zip extension is required without the allow any extension option")
	}

	return fmt.Errorf("an allowed extension is required to run this iterator")
}

// GetParser returns a handle to the parent parser that built this iterator if
// there is one.
func (obj *Zip) GetParser() interfaces.Parser { return obj.Parser }

// GetIterator returns a handle to the parent iterator that built this iterator
// if there is one.
func (obj *Zip) GetIterator() interfaces.Iterator { return obj.Iterator }

// Recurse runs a simple iterator that is responsible for unzipping a zip URI
// into a local filesystem path. If this happens successfully, it will return a
// new FsIterator that is initialized to this root path.
func (obj *Zip) Recurse(ctx context.Context, scan interfaces.ScanFunc) ([]interfaces.Iterator, error) {
	relDir := safepath.UnsafeParseIntoRelDir("zip/")
	prefix := safepath.JoinToAbsDir(obj.Prefix, relDir)
	if err := os.MkdirAll(prefix.Path(), interfaces.Umask); err != nil {
		return nil, err
	}

	// make a unique ID for the directory
	// XXX: we can consider different algorithms or methods here later...
	now := strconv.FormatInt(time.Now().UnixMilli(), 10) // itoa but int64
	sum := sha256.Sum256([]byte(obj.Path.Path() + now))
	hashRelDir, err := safepath.ParseIntoRelDir(fmt.Sprintf("%x", sum))
	if err != nil {
		return nil, err
	}
	// ensure it gets put into a folder so it doesn't explode current dir
	zipAbsDir := safepath.JoinToAbsDir(prefix, hashRelDir)

	zipMapMutex.Lock()
	mu, exists := zipMutexes[obj.Path.Path()]
	if !exists {
		mu = &sync.Mutex{}
		zipMutexes[obj.Path.Path()] = mu
	}
	zipMapMutex.Unlock()

	if obj.Debug {
		obj.Logf("locking: %s", obj.String())
	}
	mu.Lock() // locking happens here (unlock on all errors/returns!)
	once := &sync.Once{}
	obj.unlock = func() {
		fn := func() {
			if obj.Debug {
				obj.Logf("unlocking: %s", obj.String())
			}
			mu.Unlock()
		}
		once.Do(fn)
	}

	// XXX: unlock when context closes?

	// XXX: If the destination dir has contents, consider removing them
	// first. This is one reason why we have a mutex.

	// Open the zip archive for reading.
	// FIXME: use a variant that can take a context
	z, err := zip.OpenReader(obj.Path.Path())
	if err != nil {
		obj.unlock()
		return nil, errwrap.Wrapf(err, "error opening path %s", obj.Path)
	}
	defer z.Close()
	if z.Comment != "" {
		obj.Logf("zip has comment: %s", z.Comment)
	}

	filesTotal := 0
	bytesTotal := int64(64)
	// Iterate through the files in the archive.
	// XXX: can a child directory appear before a parent?
	// TODO: add a recurring progress logf if it takes longer than 30 sec
	for _, x := range z.File {
		// In an effort to short-circuit things if needed, we run a
		// check ourselves and break out early if we see that we have
		// cancelled early.
		select {
		case <-ctx.Done():
			obj.unlock()
			return nil, errwrap.Wrapf(ctx.Err(), "ended unzipping early")
		default:
		}

		// TODO: obj.Debug ?
		obj.Logf("zip: %s", x.Name)

		if x.FileInfo().IsDir() {
			relDir, err := safepath.ParseIntoRelDir(x.Name)
			if err != nil {
				// programming error
				obj.unlock()
				return nil, err
			}

			// this is where the new dir will be created
			absDir := safepath.JoinToAbsDir(zipAbsDir, relDir)

			// XXX: sanity check (is output in the dir?)
			// TODO: we could add this, but safepath automatically does this
			// if absDir is not inside of zipAbsDir then error

			// XXX: which mode method?
			if err := os.MkdirAll(absDir.Path(), os.ModePerm); err != nil {
			//if err := os.MkdirAll(absDir.Path(), x.Mode()); err != nil {
				// programming error
				obj.unlock()
				return nil, err
			}

			continue
		}

		relFile, err := safepath.ParseIntoRelFile(x.Name)
		if err != nil {
			// programming error
			obj.unlock()
			return nil, err
		}

		// this is where the output file will be stored
		absFile := safepath.JoinToAbsFile(zipAbsDir, relFile)

		// XXX: sanity check (is output in the dir?)
		// TODO: we could add this, but safepath automatically does this
		// if absFile is not inside of zipAbsDir then error

		absDir := absFile.Dir() // get the absDir that absFile is in

		// XXX: which mode to use? Maybe we are assuming a mode here
		// because we haven't seen that dir yet! Maybe if we pre-sort
		// all of the zip file entries first...
		if err := os.MkdirAll(absDir.Path(), os.ModePerm); err != nil {
		//if err := os.MkdirAll(absDir.Path(), x.Mode()); err != nil {
			// programming error
			obj.unlock()
			return nil, err
		}

		// write to this location
		dest, err := os.OpenFile(absFile.Path(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, x.Mode())
		if err != nil {
			obj.unlock()
			return nil, errwrap.Wrapf(err, "error writing our file to disk at %s", absFile)
		}

		// open the actual source file
		f, err := x.Open()
		if err != nil {
			dest.Close() // close dest file on error!
			obj.unlock()
			return nil, errwrap.Wrapf(err, "error opening file %s", x.Name)
		}
		// don't `defer` close here because we want to free in the loop

		// FIXME: use a variant that can take a context
		size, err := io.Copy(dest, f)
		if err != nil {
			f.Close()    // close file on error!
			dest.Close() // close dest file on error!
			obj.unlock()
			return nil, errwrap.Wrapf(err, "error writing our file to disk at %s", absFile)
		}
		obj.Logf("unzipped: %d bytes to disk at %s", size, absFile)

		f.Close()    // close on success to save memory!
		dest.Close() // close dest file on error!

		filesTotal++
		bytesTotal += int64(size)
	}

	// TODO: change to human readable bytes
	obj.Logf("unzipped: %d files from %s into %s (%d bytes)", filesTotal, obj.String(), zipAbsDir, bytesTotal)

	obj.iterators = []interfaces.Iterator{}

	// if it's a single zip file we return an fs iterator and let the fs
	// iterator sort that out...
	iterator := &Fs{
		Debug: obj.Debug,
		Logf: func(format string, v ...interface{}) {
			obj.Logf(format, v...) // TODO: add a prefix?
		},
		Prefix: obj.Prefix,

		Iterator: obj,

		Path: zipAbsDir,

		//Unlock: unlock,
	}
	obj.iterators = append(obj.iterators, iterator)

	return obj.iterators, nil
}

// Close shuts down the iterator and/or performs clean up after the Recurse
// method has run. This must be called if you run Recurse.
func (obj *Zip) Close() error {
	if obj.unlock != nil {
		obj.unlock()
	}
	var errs error
	for i := len(obj.iterators) - 1; i >= 0; i-- { // reverse order (stacks!)
		if err := obj.iterators[i].Close(); err != nil {
			errs = errwrap.Append(errs, err)
		}
	}
	return errs
}
