/*
Copyright The Helm Authors.

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

package releaseutil

import (
	"log"
	"path"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"k8s.io/klog"
	"sigs.k8s.io/yaml"

	"helm.sh/helm/pkg/chartutil"
	"helm.sh/helm/pkg/release"
)

// Manifest represents a manifest file, which has a name and some content.
type Manifest struct {
	Name    string
	Content string
	Head    *SimpleHead
}

// manifestFile represents a file that contains a manifest.
type manifestFile struct {
	entries map[string]string
	path    string
	apis    chartutil.VersionSet
}

// result is an intermediate structure used during sorting.
type result struct {
	hooks   []*release.Hook
	generic []Manifest
}

// TODO: Refactor this out. It's here because naming conventions were not followed through.
// So fix the Test hook names and then remove this.
var events = map[string]release.HookEvent{
	release.HookPreInstall.String():   release.HookPreInstall,
	release.HookPostInstall.String():  release.HookPostInstall,
	release.HookPreDelete.String():    release.HookPreDelete,
	release.HookPostDelete.String():   release.HookPostDelete,
	release.HookPreUpgrade.String():   release.HookPreUpgrade,
	release.HookPostUpgrade.String():  release.HookPostUpgrade,
	release.HookPreRollback.String():  release.HookPreRollback,
	release.HookPostRollback.String(): release.HookPostRollback,
	release.HookTest.String():         release.HookTest,
	// Support test-success for backward compatibility with Helm 2 tests
	"test-success": release.HookTest,
	"crd-install":         release.HookCRDInstall,
}

// SortManifests takes a map of filename/YAML contents, splits the file
// by manifest entries, and sorts the entries into hook types.
//
// The resulting hooks struct will be populated with all of the generated hooks.
// Any file that does not declare one of the hook types will be placed in the
// 'generic' bucket.
//
// Files that do not parse into the expected format are simply placed into a map and
// returned.
func SortManifests(files map[string]string, apis chartutil.VersionSet, sort KindSortOrder) ([]*release.Hook, []Manifest, error) {
	result := &result{}

	for filePath, c := range files {

		// Skip partials. We could return these as a separate map, but there doesn't
		// seem to be any need for that at this time.
		if strings.HasPrefix(path.Base(filePath), "_") {
			continue
		}
		// Skip empty files and log this.
		if strings.TrimSpace(c) == "" {
			continue
		}

		manifestFile := &manifestFile{
			entries: SplitManifests(c),
			path:    filePath,
			apis:    apis,
		}

		if err := manifestFile.sort(result); err != nil {
			return result.hooks, result.generic, err
		}
	}

	return result.hooks, sortByKind(result.generic, sort), nil
}

// sort takes a manifestFile object which may contain multiple resource definition
// entries and sorts each entry by hook types, and saves the resulting hooks and
// generic manifests (or non-hooks) to the result struct.
//
// To determine hook type, it looks for a YAML structure like this:
//
//  kind: SomeKind
//  apiVersion: v1
// 	metadata:
//		annotations:
//			helm.sh/hook: pre-install
//
// To determine the policy to delete the hook, it looks for a YAML structure like this:
//
//  kind: SomeKind
//  apiVersion: v1
//  metadata:
// 		annotations:
// 			helm.sh/hook-delete-policy: hook-succeeded
func (file *manifestFile) sort(result *result) error {
	for _, m := range file.entries {
		var entry SimpleHead
		if err := yaml.Unmarshal([]byte(m), &entry); err != nil {
			return errors.Wrapf(err, "YAML parse error on %s", file.path)
		}

		if entry.Version != "" && !file.apis.Has(entry.Version) {
			err := errors.Errorf("apiVersion %q in %s is not available", entry.Version, file.path)
			klog.Warning("apiVersion not found when parse yaml, may be a crd, skip for now:", err)
			//return errors.Errorf("apiVersion %q in %s is not available", entry.Version, file.path)
		}

		if !hasAnyAnnotation(entry) {
			result.generic = append(result.generic, Manifest{
				Name:    file.path,
				Content: m,
				Head:    &entry,
			})
			continue
		}

		hookTypes, ok := entry.Metadata.Annotations[release.HookAnnotation]
		if !ok {
			result.generic = append(result.generic, Manifest{
				Name:    file.path,
				Content: m,
				Head:    &entry,
			})
			continue
		}

		hw := calculateHookWeight(entry)

		h := &release.Hook{
			Name:           entry.Metadata.Name,
			Kind:           entry.Kind,
			Path:           file.path,
			Manifest:       m,
			Events:         []release.HookEvent{},
			Weight:         hw,
			DeletePolicies: []release.HookDeletePolicy{},
		}

		isUnknownHook := false
		for _, hookType := range strings.Split(hookTypes, ",") {
			hookType = strings.ToLower(strings.TrimSpace(hookType))
			e, ok := events[hookType]
			if !ok {
				isUnknownHook = true
				break
			}
			h.Events = append(h.Events, e)
		}

		if isUnknownHook {
			log.Printf("info: skipping unknown hook: %q", hookTypes)
			continue
		}

		result.hooks = append(result.hooks, h)

		operateAnnotationValues(entry, release.HookDeleteAnnotation, func(value string) {
			h.DeletePolicies = append(h.DeletePolicies, release.HookDeletePolicy(value))
		})
	}

	return nil
}

// hasAnyAnnotation returns true if the given entry has any annotations at all.
func hasAnyAnnotation(entry SimpleHead) bool {
	return entry.Metadata != nil &&
		entry.Metadata.Annotations != nil &&
		len(entry.Metadata.Annotations) != 0
}

// calculateHookWeight finds the weight in the hook weight annotation.
//
// If no weight is found, the assigned weight is 0
func calculateHookWeight(entry SimpleHead) int {
	hws := entry.Metadata.Annotations[release.HookWeightAnnotation]
	hw, err := strconv.Atoi(hws)
	if err != nil {
		hw = 0
	}
	return hw
}

// operateAnnotationValues finds the given annotation and runs the operate function with the value of that annotation
func operateAnnotationValues(entry SimpleHead, annotation string, operate func(p string)) {
	if dps, ok := entry.Metadata.Annotations[annotation]; ok {
		for _, dp := range strings.Split(dps, ",") {
			dp = strings.ToLower(strings.TrimSpace(dp))
			operate(dp)
		}
	}
}
