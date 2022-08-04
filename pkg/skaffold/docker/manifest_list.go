/*
Copyright 2019 The Skaffold Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package docker

import (
	"context"
	"fmt"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/output/log"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

type SinglePlatformImage struct {
	Platform *v1.Platform
	Image    string
}

func CreateManifestList(images []SinglePlatformImage, targetTag string) (string, error) {
	adds := make([]mutate.IndexAddendum, len(images))

	for i, image := range images {
		ref, err := name.ParseReference(image.Image, name.WeakValidation)
		if err != nil {
			return "", err
		}

		img, err := remote.Image(ref)
		if err != nil {
			return "", err
		}

		adds[i] = mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: image.Platform,
			},
		}
	}
	idx := mutate.AppendManifests(mutate.IndexMediaType(empty.Index, types.DockerManifestList), adds...)
	targetRef, err := name.ParseReference(targetTag, name.WeakValidation)
	if err != nil {
		return "", err
	}

	err = remote.WriteIndex(targetRef, idx, remote.WithAuthFromKeychain(primaryKeychain))
	if err != nil {
		return "", err
	}

	h, err := idx.Digest()
	if err != nil {
		return "", err
	}

	dig := fmt.Sprintf("%s", h)
	log.Entry(context.TODO()).Printf("Created ManifestList for image %s. Digest: %s\n", targetRef, dig)
	parsed, err := ParseReference(targetTag)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s:%s@%s", parsed.BaseName, parsed.Tag, dig), nil
}
