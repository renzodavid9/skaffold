/*
Copyright 2020 The Skaffold Authors

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

package tag

import (
	"context"
	"fmt"

	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/graph"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/runner/runcontext"
	"github.com/GoogleContainerTools/skaffold/pkg/skaffold/schema/latest"
)

type TaggerMux struct {
	taggers               []Tagger
	byImageName           map[string]Tagger
	allTaggers            [][]Tagger
	allTaggersByImageName map[string][]Tagger
}

func (t *TaggerMux) GenerateTag(ctx context.Context, image latest.Artifact) (string, error) {
	tagger, found := t.byImageName[image.ImageName]
	if !found {
		return "", fmt.Errorf("no valid tagger found for artifact: %q", image.ImageName)
	}
	return tagger.GenerateTag(ctx, image)
}

func (t *TaggerMux) GenerateTags(ctx context.Context, image latest.Artifact) ([]string, error) {
	taggers, found := t.allTaggersByImageName[image.ImageName]
	tags := []string{}
	if !found {
		return nil, fmt.Errorf("no valid taggers found for artifact: %q", image.ImageName)
	}

	for _, tagger := range taggers {
		tag, err := tagger.GenerateTag(ctx, image)
		if err != nil {
			return nil, err
		}
		tags = append(tags, tag)
	}

	return tags, nil
}

func NewTaggerMux(runCtx *runcontext.RunContext) (Tagger, error) {
	pipelines := runCtx.GetPipelines()
	m := make(map[string]Tagger)
	sl := make([]Tagger, len(pipelines))

	allTaggers := make([][]Tagger, len(pipelines))
	mAllTaggers := make(map[string][]Tagger)

	for _, p := range pipelines {
		taggers, err := getTaggers(runCtx, &p.Build.TagPolicies)
		if err != nil {
			return nil, fmt.Errorf("creating tagger: %w", err)
		}
		t := taggers[0] // Default tagger used for most of the operations
		sl = append(sl, t)
		allTaggers = append(allTaggers, taggers)

		for _, a := range p.Build.Artifacts {
			m[a.ImageName] = t
			mAllTaggers[a.ImageName] = taggers
		}
	}
	return &TaggerMux{taggers: sl, byImageName: m, allTaggers: allTaggers, allTaggersByImageName: mAllTaggers}, nil
}

func getTaggers(runCtx *runcontext.RunContext, t *[]latest.TagPolicy) ([]Tagger, error) {
	var taggers []Tagger

	for _, tagPolicy := range *t {
		tagger, err := getTagger(runCtx, &tagPolicy)

		if err != nil {
			return nil, err
		}

		taggers = append(taggers, tagger)
	}

	return taggers, nil
}

func getTagger(runCtx *runcontext.RunContext, t *latest.TagPolicy) (Tagger, error) {
	switch {
	case runCtx.CustomTag() != "":
		return &CustomTag{
			Tag: runCtx.CustomTag(),
		}, nil

	case t.EnvTemplateTagger != nil:
		return NewEnvTemplateTagger(t.EnvTemplateTagger.Template)

	case t.ShaTagger != nil:
		return &ChecksumTagger{}, nil

	case t.GitTagger != nil:
		return NewGitCommit(t.GitTagger.Prefix, t.GitTagger.Variant, t.GitTagger.IgnoreChanges)

	case t.DateTimeTagger != nil:
		return NewDateTimeTagger(t.DateTimeTagger.Format, t.DateTimeTagger.TimeZone), nil

	case t.InputDigest != nil:
		graph := graph.ToArtifactGraph(runCtx.Artifacts())
		return NewInputDigestTagger(runCtx, graph)

	case t.CustomTemplateTagger != nil:
		components, err := CreateComponents(runCtx, t.CustomTemplateTagger)

		if err != nil {
			return nil, fmt.Errorf("creating components: %w", err)
		}

		return NewCustomTemplateTagger(runCtx, t.CustomTemplateTagger.Template, components)

	default:
		return nil, fmt.Errorf("unknown tagger for strategy %+v", t)
	}
}

// CreateComponents creates a map of taggers for CustomTemplateTagger
func CreateComponents(runCtx *runcontext.RunContext, t *latest.CustomTemplateTagger) (map[string]Tagger, error) {
	components := map[string]Tagger{}

	for _, taggerComponent := range t.Components {
		name, c := taggerComponent.Name, taggerComponent.Component

		if _, ok := components[name]; ok {
			return nil, fmt.Errorf("multiple components with name %s", name)
		}

		switch {
		case c.EnvTemplateTagger != nil:
			components[name], _ = NewEnvTemplateTagger(c.EnvTemplateTagger.Template)

		case c.ShaTagger != nil:
			components[name] = &ChecksumTagger{}

		case c.GitTagger != nil:
			components[name], _ = NewGitCommit(c.GitTagger.Prefix, c.GitTagger.Variant, c.GitTagger.IgnoreChanges)

		case c.DateTimeTagger != nil:
			components[name] = NewDateTimeTagger(c.DateTimeTagger.Format, c.DateTimeTagger.TimeZone)

		case c.InputDigest != nil:
			graph := graph.ToArtifactGraph(runCtx.Artifacts())
			inputDigest, _ := NewInputDigestTagger(runCtx, graph)
			components[name] = inputDigest

		case c.CustomTemplateTagger != nil:
			return nil, fmt.Errorf("nested customTemplate components are not supported in skaffold (%s)", name)

		default:
			return nil, fmt.Errorf("unknown component for custom template: %s %+v", name, c)
		}
	}

	return components, nil
}
