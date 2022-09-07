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

package s3

import (
	"errors"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"time"

	"github.com/awslabs/yesiscan/util/errwrap"

	s3config "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/aws"
)

const (
	// GrantReadAllUsers is the constant used to give read access to all.
	GrantReadAllUsers = "uri=http://acs.amazonaws.com/groups/global/AllUsers"

	// DefaultRegion is a region to use if none are specified.
	DefaultRegion = "ca-central-1" // yul
)

// PubURL returns the public URL for an object in a given region and bucket.
// This depends on you setting the appropriate permissions and choosing valid
// input parameters. No validation is done, this is just templating.
func PubURL(region, bucket, object string) string {
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", bucket, region, object)
}

// Inputs is the set of information required to use the Store method.
type Inputs struct {
	// Region is the region where we will push the data.
	Region string

	// BucketName is the name of the bucket.
	BucketName string

	// CreateBucket is true if we wish to create the bucket if it's missing.
	CreateBucket bool

	// ObjectName is the name of the object.
	ObjectName string

	// GrantReadAllUsers specifies that all users read access will be set on
	// this object. Only use this if you are certain you want anyone on the
	// internet to be able to read this object.
	GrantReadAllUsers bool

	// ContentType is what is set for the object if it is non-nil.
	ContentType *string

	// Data is the actual data to store.
	Data []byte

	Debug bool
	Logf  func(format string, v ...interface{})
}


// Store takes some inputs and stores the data into s3. If successful, it
// returns a presign URL that can be shared to give access to the object. If you
// chose to make the object public, then it can also be accessed using the
// well-known public URL as obtained by the PubURL function. This depends on you
// having appropriate AWS credentials set up on your machine for the account you
// want to use.
func Store(ctx context.Context, inputs *Inputs) (string, error) {
	if inputs.Debug {
		inputs.Logf("begin s3...")
		defer inputs.Logf("done s3")
	}

	// TODO: check if region is valid?
	if inputs.Region == "" {
		return "", fmt.Errorf("empty region")
	}

	cfg, err := s3config.LoadDefaultConfig(ctx, s3config.WithRegion(inputs.Region))
	if err != nil {
		return "", errwrap.Wrapf(err, "config error")
	}
	cfg.Region = inputs.Region
	client := s3.NewFromConfig(cfg)

	if inputs.CreateBucket {
		if inputs.Debug {
			inputs.Logf("creating bucket...")
		}
		createBucketInput := &s3.CreateBucketInput{
			Bucket: &inputs.BucketName,

			// The configuration information for the bucket.
			CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
				// Specifies the Region where the bucket will be
				// created. If you don't specify a Region, the
				// bucket is created in the US East
				// (N. Virginia) Region (us-east-1).
				//LocationConstraint: s3types.BucketLocationConstraintCaCentral1,
				// it's a string region
				LocationConstraint: s3types.BucketLocationConstraint(inputs.Region),
			},
		}

		_, err := client.CreateBucket(ctx, createBucketInput)
		//*CreateBucketOutput
		if err == nil {
			inputs.Logf("bucket created")
		}

		// ignore the error if it shows bucket already exists
		var bucketErr error
		for err != nil {
			bucketErr = err // we have an error!
			if _, ok := err.(*s3types.BucketAlreadyOwnedByYou); ok {
				bucketErr = nil // ignore me!
				break
			}
			err = errors.Unwrap(err)
		}
		if bucketErr != nil {
			return "", errwrap.Wrapf(bucketErr, "bucket creation issue")
		}
		if inputs.Debug {
			inputs.Logf("bucket should exist")
		}
	}

	body := bytes.NewReader(inputs.Data) // support seek

	// we hash this to make idempotent puts avoid copying the data again...
	h := md5.New()
	if _, err := io.Copy(h, body); err != nil {
		return "", errwrap.Wrapf(err, "copy to hash error")
	}
	// rewind after hashing
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return "", errwrap.Wrapf(err, "seek error")
	}

	md5s := base64.StdEncoding.EncodeToString(h.Sum(nil))
	if inputs.Debug {
		inputs.Logf("md5s: %s", md5s)
	}

	putObjectInput := &s3.PutObjectInput{
		Bucket: &inputs.BucketName, // this member is required

		Key: &inputs.ObjectName, // this member is required

		// For using values that are not seekable (io.Seeker) see,
		// https://aws.github.io/aws-sdk-go-v2/docs/sdk-utilisties/s3/#unseekable-streaming-input
		Body: body, // io.Reader

		ContentMD5: &md5s,

		ContentType: inputs.ContentType,

		StorageClass: s3types.StorageClassStandard,
	}
	if inputs.GrantReadAllUsers { // give all users on internet read access!
		putObjectInput.GrantRead = aws.String(GrantReadAllUsers)
	}

	inputs.Logf("putting object...")
	if _, err := client.PutObject(ctx, putObjectInput); err != nil {
		return "", errwrap.Wrapf(err, "put error")
	}

	// X-Amz-Expires must be less than a week (in seconds); that is, the
	// given X-Amz-Expires must be less than 604800 seconds. (equal is okay)
	// TODO: i suppose we could allow the user to specify the expiry time,
	// but the maximum is so short, we'll hardcode this in here for now.
	presignClient := s3.NewPresignClient(client, s3.WithPresignExpires(7 * 24 * time.Hour))

	presignResult, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(inputs.BucketName),
		Key:    aws.String(inputs.ObjectName),
	})

	if err != nil {
		return "", errwrap.Wrapf(err, "presign error")
	}

	return presignResult.URL, nil
}
