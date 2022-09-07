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

// Package interfaces has all the common interfaces and structs that are needed
// throughout this software. It is imported by many packages. It must not import
// any packages other than stdlib and util libraries. This is so that we avoid
// dependency loops.
package interfaces

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/awslabs/yesiscan/util/errwrap"
	"github.com/awslabs/yesiscan/util/licenses"
	"github.com/awslabs/yesiscan/util/safepath"
)

// Error is a constant error type that implements error.
type Error string

// Error fulfills the error interface of this type.
func (e Error) Error() string { return string(e) }

const (
	// ErrUnknownLicense should be returned by any backend when it can't
	// identify the license that a particular file is under. This is a
	// distinct condition from identifying a license but with a extremely
	// low confidence.
	ErrUnknownLicense = Error("license is unknown")

	// Umask is the value used whenever we need to make a directory.
	Umask = 0770 // TODO: what should this be?
)

// Parser is the interface that every parser must implement. You populate the
// associated implementing struct with the desired inputs that you'd like, and
// ultimately Parse will be called to return an initial set of iterators to be
// used.
type Parser interface {
	fmt.Stringer

	// Parse runs the parser and returns the initial set of iterators.
	// TODO: this API might change.
	Parse() ([]Iterator, error)
}

// Iterator is the interface that is used by anything that walks through data.
// Iterators provide a mechanism to run a scan function, but they also allow us
// to return new iterators.
type Iterator interface {
	fmt.Stringer

	// Validate checks that the iterator struct has been built correctly.
	Validate() error

	// GetParser returns a handle to the parent parser that built this
	// iterator if there is one.
	GetParser() Parser

	// GetIterator returns a handle to the parent iterator that built this
	// iterator if there is one.
	GetIterator() Iterator

	// Recurse runs a simple recursive iterator, applying a scan function to
	// everything that it encounters. While iterating, it may also discover
	// certain files that it can use to produce new iterators from.
	// TODO: this API might change.
	Recurse(context.Context, ScanFunc) ([]Iterator, error)

	// Close unlocks access to any resources held by this iterator. It is
	// safe to be run more than once. It *must* be called if Recurse is
	// called to prevent deadlocks.
	Close() error
}

// ScanFunc is a type alias that expresses the signature of the scan function
// that we use in the iterators. It takes a context for cancellation, a safepath
// that we use to convey what to scan, and a fileinfo field about the file that
// is being scanned.
type ScanFunc = func(context.Context, safepath.Path, *Info) error

// SkipDir is a copy of the golang path/filepath or io/fs SkipDir. This is a
// special error value that is used to signal that we're done with a particular
// directory. It is copied here so that it can be used more easily without
// consumers needing to import the right package.
var SkipDir = fs.SkipDir

// Info is a struct representing the additional info passed to the scan
// function.
type Info struct {
	// FileInfo is the standard fs.FileInfo struct that is associated with
	// this file.
	FileInfo fs.FileInfo

	// UID is the unique identifier that is associated with each result. It
	// is what is used as the key in the ResultSet. This UID is often
	// modified with the GenUID function to return something more useful, as
	// a human readable UID is more valuable than an internal path.
	UID string
}

// Backend is the common interface for backends. Any useful backend must also
// implement one of the extended backends. Different interfaces exist to support
// the different mechanisms available to scan files. They are not all equally
// efficient, and it is recommended to only implement one extended backend
// unless you know what you're doing.
type Backend interface {
	fmt.Stringer
}

// SetupBackend adds a method that can be run if the backend has some initial
// one-time validation or setup to do. It should always be safe and idempotent.
type SetupBackend interface {
	Backend

	// Setup runs an operation to check if things are okay. It should be
	// idempotent and generally safe to run. It can perform validation
	// operations and return false if anything went wrong. You should cancel
	// any work you are doing as fast as possible if the context is
	// cancelled.
	Setup(ctx context.Context) error
}

// DataBackend is the extended backend that is most efficient for receiving data
// since all the reads are done once, and each backend only has to read from one
// memory address. You should implement this backend if you can. It assumes that
// individual files are small enough to easily fit into memory.
type DataBackend interface {
	Backend

	// ScanData takes a byte array and into about it and results a result.
	// It's important to make sure that you error if you are cancelled by
	// the context and you didn't finish all the work you had. If the
	// backend returns interfaces.SkipDir, then this is the signal that it
	// doesn't need to return any different information in a deeper
	// hierarchy of that scan. This is common when we have a backend that
	// makes a whole repository or directory determination. In this case,
	// the iterator stores this rejection so that we don't wastefully call
	// the backend again with a child path. The backend will likely want to
	// also return a result with the SkipDir. It must not return SkipDir in
	// response to a non directory path. This must be able to handle
	// receiving an empty byte array, which can happen if a directory path
	// is presented. Since a byte array is effectively a pointer to the set
	// of data that each backend will share the same view of, you must *not*
	// edit this data in any way, since this would change the view of it for
	// every backend, and unexpected things might happen.
	// TODO: this API might change.
	ScanData(ctx context.Context, data []byte, info *Info) (*Result, error)
}

// PathBackend is the extended backend that is most logical to humans and which
// is what most poorly (as defined by written in a bubble without any desire for
// code reuse by others) backends will implement.
type PathBackend interface {
	Backend

	// ScanPath takes a path and info about it and returns a result. It's
	// important to make sure that you error if you are cancelled by the
	// context and you didn't finish all the work you had. If the backend
	// returns interfaces.SkipDir, then this is the signal that it doesn't
	// need to return any different information in a deeper hierarchy of
	// that scan. This is common when we have a backend that makes a whole
	// repository or directory determination. In this case, the iterator
	// stores this rejection so that we don't wastefully call the backend
	// again with a child path. The backend will likely want to also return
	// a result with the SkipDir. It must not return SkipDir in response to
	// a non directory path.
	// TODO: this API might change.
	ScanPath(ctx context.Context, path safepath.Path, info *Info) (*Result, error)
}

// TODO: implement a new iterator that simply passes the root dir off to a
// special backend that does its own iteration. This needs a new extended
// backend interface as well. (RootBackend)
type RootBackend interface {
	Backend

	// TODO: this API might change.
	ScanRoot(ctx context.Context, path safepath.Path, info *Info) (ResultSet, error)
}

// TODO: add a backend that lets you seek through data yourself in case of very
// large files that we don't want to load into memory all at once. (SeekBackend)
type SeekBackend interface {
	Backend

	// TODO: this API might change.
	// TODO: should this be io.Reader or io.ReaderAt, or io.Seeker instead?
	ScanSeek(ctx context.Context, file fs.File, info *Info) (*Result, error)
}

// Result is the datastructure that is returned from every scanner. Each result
// has a primary determination, associated confidence, and other information.
// In addition, additional secondary (less-likely) determinations can be stored.
// These are stored as a nested field, instead of having the primary return type
// be a []*Result because that would be more complicated and in most cases there
// will only be one result.
type Result struct {
	// Licenses is a list of licenses that make up this determination. Each
	// of these is considered to be combined by the logical AND. If any of
	// these should individually be a logical OR, then use the mechanism
	// inside of the License struct to express that.
	Licenses []*licenses.License

	// Confidence represents the amount of certainty we have in this
	// determination. A value of 1.0 means absolute certainty, where as a
	// value of 0.0 means that there is no confidence in the result.
	Confidence float64

	// Meta stores some metadata about a result. This is populated by the
	// engine for tracking purposes, and isn't meant to be either read or
	// set by the implemented backend that returns this.
	Meta *Meta

	// More is a list of additional possible results. They should be ordered
	// by decreasing confidence. You must NOT nest results more than one
	// level deep. (IOW, these results must not contain child results.)
	// TODO: is it okay to support storing multiple results?
	More []*Result
}

// Cmp compares two results and returns nil if they are the same. We don't
// currently compare all fields in the structs.
func (obj *Result) Cmp(result *Result) error {
	if (obj == nil) && (result == nil) {
		return nil // same
	}

	if (obj == nil) != (result == nil) { // xor
		return fmt.Errorf("the results differ")
	}

	if len(obj.Licenses) != len(result.Licenses) {
		return fmt.Errorf("length of licenses differ")
	}
	for i, x := range obj.Licenses {
		if err := x.Cmp(result.Licenses[i]); err != nil {
			return err
		}
	}

	if obj.Confidence != result.Confidence { // TODO: epsilon?
		return fmt.Errorf("confidence values don't match: %.4f != %.4f", obj.Confidence, result.Confidence)
	}

	// We don't have a substantial reason to need to compare these right now.
	//if (obj.Meta == nil) != (result.Meta == nil) { // xor
	//	return fmt.Errorf("the meta fields differ")
	//}
	//if obj.Meta != nil && result.Meta != nil {
	//	if err := obj.Meta.Cmp(result.Meta); err != nil {
	//		return errwrap.Wrapf(err, "the meta fields differ")
	//	}
	//}

	// XXX: why does this compare fail when checking the same repo?
	// XXX: I think the google licenseclassifier backend isn't deterministic
	//if len(obj.More) != len(result.More) {
	//	return fmt.Errorf("length of more results differs")
	//}
	//for i, x := range obj.More {
	//	if err := x.Cmp(result.More[i]); err != nil {
	//		fmt.Printf("a: %+v\n", x)
	//		fmt.Printf("b: %+v\n", result.More[i])
	//		return err
	//	}
	//}

	return nil
}

// Meta stores some metadata about the scanning operation. It is used to make
// the results more informative if a display engine or formatter would like to
// do so.
type Meta struct {
	// Iterator is a pointer to the iterator that was used to obtain the
	// result that we scanned. It is stored here to be available for
	// querying if so required.
	Iterator Iterator

	// Backend is a pointer to the backend that was used to obtain the
	// result that we scanned. It is stored here to be available for
	// querying if so required.
	//
	// NOTE: Since the ResultSet contains a map with Backend in it already,
	// this field is redundant, but it is here for consistency with the idea
	// of storing the Result's associated metadata alongside it.
	Backend Backend
}

// ResultSet is the organized set of results that is produced after running a
// series of backends on a series of paths, which results in a series of
// results. The first map has keys corresponding to the paths in our canonical
// form with directories represented with a trailing slash, and the second map
// has pointers to each backend.
type ResultSet = map[string]map[Backend]*Result

// MergeResultSets does what you expect, however it errors if it would have to
// overwrite data.
func MergeResultSets(r1, r2 ResultSet) (ResultSet, error) {
	resultSet := make(map[string]map[Backend]*Result)

	for p, m := range r1 {
		if _, exists := resultSet[p]; !exists {
			resultSet[p] = make(map[Backend]*Result)
		}

		for b, r := range m {
			if old, exists := resultSet[p][b]; exists {
				if err := old.Cmp(r); err != nil {
					return nil, errwrap.Wrapf(err, "duplicate result for %s in %s", p, b)
				}
			}
			resultSet[p][b] = r
		}
	}

	for p, m := range r2 {
		if _, exists := resultSet[p]; !exists {
			resultSet[p] = make(map[Backend]*Result)
		}

		for b, r := range m {
			if old, exists := resultSet[p][b]; exists {
				if err := old.Cmp(r); err != nil {
					return nil, errwrap.Wrapf(err, "duplicate result for %s in %s", p, b)
				}
			}
			resultSet[p][b] = r
		}
	}

	return resultSet, nil
}
